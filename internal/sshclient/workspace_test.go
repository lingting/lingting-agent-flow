package sshclient_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
)

// TestResolveWorkspaceDefault 验证默认工作空间按远程 Home 展开。
func TestResolveWorkspaceDefault(t *testing.T) {
	root, err := sshclient.ResolveWorkspace("", "/home/tester")
	if err != nil {
		t.Fatalf("解析默认工作空间失败: %v", err)
	}
	if root != "/home/tester/.lingting/agentflow" {
		t.Fatalf("默认工作空间解析不符: %s", root)
	}
}

// TestResolveWorkspaceTilde 验证 ~ 前缀按远程 Home 展开。
func TestResolveWorkspaceTilde(t *testing.T) {
	root, err := sshclient.ResolveWorkspace("~/agentflow/ci", "/home/tester")
	if err != nil {
		t.Fatalf("解析工作空间失败: %v", err)
	}
	if root != "/home/tester/agentflow/ci" {
		t.Fatalf("工作空间解析不符: %s", root)
	}
}

// TestResolveWorkspaceAbsolute 验证绝对路径保持不变。
func TestResolveWorkspaceAbsolute(t *testing.T) {
	root, err := sshclient.ResolveWorkspace("/srv/agentflow", "/home/tester")
	if err != nil {
		t.Fatalf("解析工作空间失败: %v", err)
	}
	if root != "/srv/agentflow" {
		t.Fatalf("绝对工作空间应保持原值: %s", root)
	}
}

// TestResolveWorkspaceRejectsUnsafe 验证越界与非法工作空间被拒绝。
func TestResolveWorkspaceRejectsUnsafe(t *testing.T) {
	cases := map[string]string{
		"Home 本身":     "~",
		"根目录":         "/",
		"Home 绝对路径":   "/home/tester",
		"相对路径":        "agentflow",
		"包含上级目录引用":    "~/../etc",
		"包含反斜杠":       `~\agentflow`,
		"缺少 ~ 与 / 前缀": "agentflow/ci",
	}
	for name, configured := range cases {
		if _, err := sshclient.ResolveWorkspace(configured, "/home/tester"); err == nil {
			t.Fatalf("%s 应当被拒绝: %q", name, configured)
		}
	}
}

// TestResolveWorkspaceRejectsInvalidHome 验证非法 Home 目录被拒绝。
func TestResolveWorkspaceRejectsInvalidHome(t *testing.T) {
	if _, err := sshclient.ResolveWorkspace("~/agentflow", "relative/home"); err == nil {
		t.Fatal("非法 Home 目录应当被拒绝")
	}
}

// TestResolveHome 验证通过已认证连接解析远程 Home。
func TestResolveHome(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	home, err := sshclient.ResolveHome(testContext(t), client, 0)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	if !strings.HasPrefix(home, "/") {
		t.Fatalf("远程 Home 应当是绝对路径: %s", home)
	}
}

// TestResolveHomeRequiresClient 验证缺少连接时返回明确错误。
func TestResolveHomeRequiresClient(t *testing.T) {
	if _, err := sshclient.ResolveHome(testContext(t), nil, 0); err == nil {
		t.Fatal("缺少连接时应当返回错误")
	}
}

// TestEnsureWorkspaceCreatesDirectory 验证工作空间按需创建并可写。
func TestEnsureWorkspaceCreatesDirectory(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	configured := tempRemotePath(t) + "-workspace"
	workspace, err := sshclient.EnsureWorkspace(testContext(t), client, configured, true)
	if err != nil {
		t.Fatalf("创建工作空间失败: %v", err)
	}
	if !workspace.Exists || !workspace.Created || !workspace.Writable {
		t.Fatalf("工作空间状态不符: %+v", workspace)
	}
	if workspace.Root != configured {
		t.Fatalf("解析后的工作空间不符: %s", workspace.Root)
	}
}

// TestEnsureWorkspaceWithoutCreate 验证未授权时不会创建工作空间目录。
func TestEnsureWorkspaceWithoutCreate(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	configured := tempRemotePath(t) + "-missing"
	workspace, err := sshclient.EnsureWorkspace(testContext(t), client, configured, false)
	if err != nil {
		t.Fatalf("检查工作空间不应失败: %v", err)
	}
	if workspace.Exists || workspace.Created {
		t.Fatalf("未授权时不应创建目录: %+v", workspace)
	}
	if workspace.Root != configured {
		t.Fatalf("解析后的工作空间不符: %s", workspace.Root)
	}
}

// TestEnsureWorkspaceExistingDirectory 验证已存在目录不会被重复创建。
func TestEnsureWorkspaceExistingDirectory(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	configured := tempRemotePath(t) + "-existing"
	if _, err := sshclient.EnsureWorkspace(testContext(t), client, configured, true); err != nil {
		t.Fatalf("首次创建工作空间失败: %v", err)
	}
	workspace, err := sshclient.EnsureWorkspace(testContext(t), client, configured, true)
	if err != nil {
		t.Fatalf("再次检查工作空间失败: %v", err)
	}
	if !workspace.Exists {
		t.Fatalf("工作空间应当存在: %+v", workspace)
	}
	if workspace.Created {
		t.Fatalf("已存在的目录不应标记为本次创建: %+v", workspace)
	}
}
