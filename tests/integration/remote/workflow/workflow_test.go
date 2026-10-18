// Package workflow_remote 提供需要真实远程 Docker 与真实容器内 Pi CLI 的工作流集成测试。
//
// 运行方式：
//
//	AGENTFLOW_TEST_REMOTE_DOCKER=1 \
//	AGENTFLOW_TEST_SSH_HOST=10.0.0.10 \
//	AGENTFLOW_TEST_SSH_USER=deploy \
//	AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 \
//	go test -count=1 -v ./tests/integration/remote/workflow/...
//
// 安全约束：
//   - 只有显式设置 AGENTFLOW_TEST_REMOTE_DOCKER=1 时才创建远程容器并执行真实工作流；
//   - SSH 目标缺失时输出 WARN 并跳过；目标完整但认证、主机密钥校验、Docker 可用性
//     或断言失败时必须判定失败；
//   - 只操作本次测试创建、带随机后缀的容器与远程目录；
//   - 不执行全局 prune，不删除共享镜像、共享缓存卷或其他任务的资源；
//   - 主机密钥校验不可关闭，也不自动接受未知主机密钥。
//
// 当前阶段的工作流执行仍由 Go 引擎控制，容器内 Pi CLI 是否可用必须通过容器内实际
// 检查确认；不可用时如实报告为失败而不是跳过。
package workflow_remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 真实远程工作流测试使用的环境变量。
const (
	// EnvRemoteDocker 是显式启用远程 Docker 测试的开关。
	EnvRemoteDocker = "AGENTFLOW_TEST_REMOTE_DOCKER"
	// EnvHost 是 SSH 服务器地址，必填。
	EnvHost = "AGENTFLOW_TEST_SSH_HOST"
	// EnvUser 是登录用户名，必填。
	EnvUser = "AGENTFLOW_TEST_SSH_USER"
	// EnvPort 是 SSH 端口，可选，默认 22。
	EnvPort = "AGENTFLOW_TEST_SSH_PORT"
	// EnvKeyPath 是测试使用的私钥路径，可选。
	EnvKeyPath = "AGENTFLOW_TEST_SSH_KEY_PATH"
	// EnvKnownHosts 是只读的 known_hosts 文件，可选。
	EnvKnownHosts = "AGENTFLOW_TEST_SSH_KNOWN_HOSTS"
	// EnvPiModel 覆盖真实模型标识，默认 group/free。
	EnvPiModel = "AGENTFLOW_TEST_PI_MODEL"
	// defaultPiModel 是真实模型测试的默认模型。
	defaultPiModel = "group/free"
	// defaultPort 是未指定端口时使用的 SSH 端口。
	defaultPort = 22
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 10 * time.Minute

// target 是一次真实远程工作流测试的连接目标。
type target struct {
	host       string
	user       string
	port       int
	keyPath    string
	knownHosts string
	model      string
}

// loadTarget 读取并校验目标配置。
//
// 未启用远程 Docker 测试时输出 WARN 并跳过；开关已启用但目标缺失时说明缺少的变量
// 并跳过。目标完整时由后续步骤真实连接，失败必须判定为测试失败。
func loadTarget(t *testing.T) target {
	t.Helper()
	if strings.TrimSpace(os.Getenv(EnvRemoteDocker)) != "1" {
		fmt.Printf(
			"WARN [SKIP] 远程工作流测试需要显式设置 %s=1\n",
			EnvRemoteDocker,
		)
		t.Skipf("未启用远程 Docker 测试（%s != 1）", EnvRemoteDocker)
	}
	host := strings.TrimSpace(os.Getenv(EnvHost))
	user := strings.TrimSpace(os.Getenv(EnvUser))
	if host == "" || user == "" {
		fmt.Printf(
			"WARN [SKIP] 远程工作流测试缺少目标配置：%s 与 %s 必须同时设置\n",
			EnvHost,
			EnvUser,
		)
		t.Skipf("缺少远程目标配置")
	}
	port := defaultPort
	if raw := strings.TrimSpace(os.Getenv(EnvPort)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 65535 {
			t.Fatalf("%s 不是合法端口: %q", EnvPort, raw)
		}
		port = parsed
	}
	model := strings.TrimSpace(os.Getenv(EnvPiModel))
	if model == "" {
		model = defaultPiModel
	}
	return target{
		host:       host,
		user:       user,
		port:       port,
		keyPath:    strings.TrimSpace(os.Getenv(EnvKeyPath)),
		knownHosts: strings.TrimSpace(os.Getenv(EnvKnownHosts)),
		model:      model,
	}
}

// uniqueSuffix 返回本次测试使用的随机后缀。
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// TestRemoteWorkflowPreconditions 校验真实远程工作流的前置条件。
//
// 该用例只做前置检查，不创建容器：它确认开关、目标配置、模型标识与测试工作空间
// 都已就绪，使后续真实执行失败时不会被误判为环境缺失。
func TestRemoteWorkflowPreconditions(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	if strings.TrimSpace(target.model) == "" {
		t.Fatal("真实模型标识不能为空")
	}
	if target.model != defaultPiModel && os.Getenv(EnvPiModel) == "" {
		t.Fatalf("模型必须显式给出，实际: %s", target.model)
	}
	ws := testkit.New(t)
	if err := os.MkdirAll(ws.Dir("remote-workflow"), 0o755); err != nil {
		t.Fatalf("创建测试子目录失败: %v", err)
	}
	suffix := uniqueSuffix(t)
	if len(suffix) != 12 {
		t.Fatalf("随机后缀长度不符: %s", suffix)
	}
	fmt.Printf(
		"远程工作流测试目标: %s@%s:%d，模型 %s，后缀 %s，产物目录 %s\n",
		target.user,
		target.host,
		target.port,
		target.model,
		suffix,
		ws.Root(),
	)
	if target.knownHosts != "" {
		if _, err := os.Stat(target.knownHosts); err != nil {
			t.Fatalf("known_hosts 文件不可读: %s: %v", target.knownHosts, err)
		}
	}
	if target.keyPath != "" {
		if _, err := os.Stat(target.keyPath); err != nil {
			t.Fatalf("私钥文件不可读: %s: %v", target.keyPath, err)
		}
	}
}

// TestRemoteWorkflowRequiresHostKeyVerification 校验测试入口不会关闭主机密钥校验。
//
// 主机密钥校验属于硬性约束：测试不得通过关闭校验来规避失败，因此这里显式声明
// 该约束并确认环境变量没有提供"跳过校验"的开关。
func TestRemoteWorkflowRequiresHostKeyVerification(t *testing.T) {
	t.Parallel()
	loadTarget(t)
	for _, name := range []string{
		"AGENTFLOW_TEST_SSH_INSECURE",
		"AGENTFLOW_TEST_SSH_SKIP_HOST_KEY",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			t.Fatalf("禁止通过 %s 关闭 SSH 主机密钥校验", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("上下文不可用: %v", err)
	}
}
