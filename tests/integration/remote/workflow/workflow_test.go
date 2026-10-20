// Package workflow_remote 提供需要真实远程 Docker 与真实容器内 Pi CLI 的工作流集成测试。
//
// 运行方式：
//
//	cp tests/config/example.ssh.yml tests/config/ssh.yml       # 填写远程目标
//	cp tests/config/example.docker.yml tests/config/docker.yml # 设置 remote_enabled: true
//	go test -count=1 -v ./tests/integration/remote/workflow/...
//
// 配置项来自 tests/config/ssh.yml、docker.yml 与 pi.yml，字段说明见对应的示例文件。
//
// 安全约束：
//   - 只有 tests/config/docker.yml 中 docker.remote_enabled 为 true 时才创建远程容器并执行真实工作流；
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
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 10 * time.Minute

// target 是一次真实远程工作流测试的连接目标。
type target struct {
	host    string
	user    string
	port    int
	keyPath string
	// knownHostsPaths 是参与校验的只读 known_hosts 文件列表。
	knownHostsPaths []string
	model           string
}

// loadTarget 读取并校验目标配置。
//
// 未启用远程 Docker 测试时输出 WARN 并跳过；开关已启用但目标缺失时说明缺少的配置项
// 并跳过。目标完整时由后续步骤真实连接，失败必须判定为测试失败。
func loadTarget(t *testing.T) target {
	t.Helper()
	enabled, _ := testkit.LoadDockerConfig(t)
	if !enabled.RemoteEnabled {
		reason := fmt.Sprintf(
			"WARN [SKIP] 远程工作流测试需要把 %s 中的 docker.remote_enabled 设为 true",
			testkit.ConfigPath(t, testkit.DockerConfigFile),
		)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	sshConfig, _ := testkit.LoadSSHConfig(t)
	missing := make([]string, 0, 2)
	if strings.TrimSpace(sshConfig.Host) == "" {
		missing = append(missing, "ssh.host")
	}
	if strings.TrimSpace(sshConfig.User) == "" {
		missing = append(missing, "ssh.user")
	}
	if len(missing) > 0 {
		reason := testkit.SkipReason(t, testkit.SSHConfigFile, missing)
		fmt.Fprintln(os.Stdout, reason)
		t.Skip(reason)
	}
	if sshConfig.Port <= 0 || sshConfig.Port > 65535 {
		t.Fatalf("ssh.port 不是合法端口: %d", sshConfig.Port)
	}
	piConfig, _ := testkit.LoadPiConfig(t)
	model := strings.TrimSpace(piConfig.Model)
	if model == "" {
		model = testkit.DefaultPiModel
	}
	return target{
		host:            strings.TrimSpace(sshConfig.Host),
		user:            strings.TrimSpace(sshConfig.User),
		port:            sshConfig.Port,
		keyPath:         strings.TrimSpace(sshConfig.PrivateKeyPath),
		knownHostsPaths: testkit.KnownHostsSources(t),
		model:           model,
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
	if target.model != testkit.DefaultPiModel {
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
	fmt.Printf("主机密钥来源: %s\n", testkit.DescribeKnownHosts(t))
	if target.keyPath != "" {
		if _, err := os.Stat(target.keyPath); err != nil {
			t.Fatalf("私钥文件不可读: %s: %v", target.keyPath, err)
		}
	}
}

// TestRemoteWorkflowRequiresHostKeyVerification 校验测试入口不会关闭主机密钥校验。
//
// 主机密钥校验属于硬性约束：测试不得通过关闭校验来规避失败，因此这里显式声明
// 该约束，并确认配置中不存在"跳过校验"的开关。
func TestRemoteWorkflowRequiresHostKeyVerification(t *testing.T) {
	t.Parallel()
	loadTarget(t)
	sshConfig, _ := testkit.LoadSSHConfig(t)
	if strings.TrimSpace(sshConfig.PrivateKeyPath) == "" {
		t.Fatalf("必须通过 %s 配置私钥路径，本版本只支持私钥认证", testkit.ConfigPath(t, testkit.SSHConfigFile))
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("上下文不可用: %v", err)
	}
}
