package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// TestNewExecutionHostAppliesDefaults 验证默认端口、默认工作空间与启用状态。
func TestNewExecutionHostAppliesDefaults(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "测试节点", "10.0.0.10", 0, "deploy", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if host.Port != domain.DefaultSSHPort {
		t.Fatalf("默认端口不符: %d", host.Port)
	}
	if host.WorkspaceRoot != domain.DefaultWorkspaceRoot {
		t.Fatalf("默认工作空间不符: %s", host.WorkspaceRoot)
	}
	if !host.Enabled {
		t.Fatal("新建节点应当默认启用")
	}
	if host.IsDefault {
		t.Fatal("新建节点不应默认为默认节点")
	}
	if host.ID.IsEmpty() {
		t.Fatal("未指定标识时应当自动生成")
	}
	if host.Address() != "10.0.0.10:22" {
		t.Fatalf("地址拼接不符: %s", host.Address())
	}
}

// TestNewExecutionHostKeepsExplicitWorkspace 验证工作空间可以被单独覆盖。
func TestNewExecutionHostKeepsExplicitWorkspace(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost(
		domain.ID("node-1"),
		"节点",
		"build.example.internal",
		2222,
		"ci",
		"/srv/agentflow/",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if host.ID != domain.ID("node-1") {
		t.Fatalf("显式标识被覆盖: %s", host.ID)
	}
	if host.WorkspaceRoot != "/srv/agentflow" {
		t.Fatalf("工作空间末尾分隔符应被去除: %s", host.WorkspaceRoot)
	}
	if host.Address() != "build.example.internal:2222" {
		t.Fatalf("地址拼接不符: %s", host.Address())
	}
}

// TestExecutionHostAddressHandlesIPv6 验证 IPv6 地址会被正确加方括号。
func TestExecutionHostAddressHandlesIPv6(t *testing.T) {
	t.Parallel()
	host, err := domain.NewExecutionHost("", "节点", "[2001:db8::1]", 22, "root", "", time.Now())
	if err != nil {
		t.Fatalf("构造执行节点失败: %v", err)
	}
	if host.Host != "2001:db8::1" {
		t.Fatalf("IPv6 方括号应被去除: %s", host.Host)
	}
	if host.Address() != "[2001:db8::1]:22" {
		t.Fatalf("IPv6 地址拼接不符: %s", host.Address())
	}
}

// TestExecutionHostRejectsInvalidFields 验证字段校验覆盖各类非法输入。
func TestExecutionHostRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		name, host, username, workspace string
		port                            int
		field                           string
	}{
		"缺少名称":       {"", "10.0.0.10", "deploy", "", 22, "name"},
		"缺少主机地址":     {"节点", "", "deploy", "", 22, "host"},
		"主机地址含端口":    {"节点", "10.0.0.10:22", "deploy", "", 22, "host"},
		"主机名含下划线":    {"节点", "build_host", "deploy", "", 22, "host"},
		"端口为零":       {"节点", "10.0.0.10", "deploy", "", -1, "port"},
		"端口超出上限":     {"节点", "10.0.0.10", "deploy", "", 70000, "port"},
		"缺少用户名":      {"节点", "10.0.0.10", "", "", 22, "username"},
		"用户名含空格":     {"节点", "10.0.0.10", "de ploy", "", 22, "username"},
		"工作空间是根目录":   {"节点", "10.0.0.10", "deploy", "/", 22, "workspace_root"},
		"工作空间是 Home": {"节点", "10.0.0.10", "deploy", "~", 22, "workspace_root"},
		"工作空间为相对路径":  {"节点", "10.0.0.10", "deploy", "agentflow", 22, "workspace_root"},
		"工作空间含反斜杠":   {"节点", "10.0.0.10", "deploy", `C:\agentflow`, 22, "workspace_root"},
		"工作空间含上级引用":  {"节点", "10.0.0.10", "deploy", "/srv/../../etc", 22, "workspace_root"},
		"工作空间含双斜杠":   {"节点", "10.0.0.10", "deploy", "/srv//agentflow", 22, "workspace_root"},
	}
	for name, testCase := range cases {
		_, err := domain.NewExecutionHost(
			"",
			testCase.name,
			testCase.host,
			testCase.port,
			testCase.username,
			testCase.workspace,
			time.Now(),
		)
		if err == nil {
			t.Fatalf("%s 应当被拒绝", name)
		}
		if !errors.Is(err, domain.ErrInvalidValue) {
			t.Fatalf("%s 应当返回字段校验错误，实际: %v", name, err)
		}
		var validation *domain.ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("%s 应当返回 ValidationError，实际: %v", name, err)
		}
		if validation.Field != testCase.field {
			t.Fatalf("%s 的字段不符: 期望 %s，实际 %s", name, testCase.field, validation.Field)
		}
	}
}

// TestExecutionHostValidateAcceptsIPv4AndHostname 验证常见地址形式可以通过校验。
func TestExecutionHostValidateAcceptsIPv4AndHostname(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1", "192.168.1.20", "node-01.example.internal"} {
		if _, err := domain.NewExecutionHost(
			"",
			"节点",
			address,
			22,
			"deploy",
			"",
			time.Now(),
		); err != nil {
			t.Fatalf("地址 %s 应当被接受: %v", address, err)
		}
	}
}

// TestExecutionHostValidateRejectsMissingID 验证缺少标识的实体不可持久化。
func TestExecutionHostValidateRejectsMissingID(t *testing.T) {
	t.Parallel()
	host := domain.ExecutionHost{
		Name:          "节点",
		Host:          "10.0.0.10",
		Port:          22,
		Username:      "deploy",
		WorkspaceRoot: domain.DefaultWorkspaceRoot,
		Enabled:       true,
	}
	if err := host.Validate(); !errors.Is(err, domain.ErrEmptyID) {
		t.Fatalf("应当返回标识为空错误，实际: %v", err)
	}
}

// TestDefaultWorkspaceRootIsPOSIX 验证默认工作空间遵循 POSIX 路径语义。
func TestDefaultWorkspaceRootIsPOSIX(t *testing.T) {
	t.Parallel()
	if domain.DefaultWorkspaceRoot != "~/.lingting/agentflow" {
		t.Fatalf("默认工作空间不符: %s", domain.DefaultWorkspaceRoot)
	}
	if strings.ContainsRune(domain.DefaultWorkspaceRoot, '\\') {
		t.Fatalf("默认工作空间不应包含 Windows 分隔符: %s", domain.DefaultWorkspaceRoot)
	}
}
