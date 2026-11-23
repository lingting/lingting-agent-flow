// 本文件是 tests/integration/remote/workflow/{workflow_test.go,e2e_env_test.go} 中
// 前置检查用例的迁移结果（3 条）。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、工作空间取编排进程下发的目录、
// 真实依赖授权改由编排进程下发；断言保持原样。
package workflow

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/testrunner"
	"github.com/lingting/agentflow/tests/testkit"
)

// 登记前置检查 3 条用例。
var (
	_ = registerWithoutDB("preconditions", 10*time.Minute, runRemoteWorkflowPreconditions)
	_ = registerWithoutDB(
		"requires-host-key-verification",
		10*time.Minute,
		runRemoteWorkflowRequiresHostKeyVerification,
	)
	_ = registerWithoutDB("e2e-preconditions", 10*time.Minute, runRemoteE2EPreconditions)
)

// 原 TestRemoteWorkflowPreconditions 校验真实远程工作流的前置条件。
//
// 该用例只做前置检查，不创建容器：它确认开关、目标配置、模型标识与测试工作空间
// 都已就绪，使后续真实执行失败时不会被误判为环境缺失。
func runRemoteWorkflowPreconditions(t *testrunner.Context) {
	target := loadTarget(t)
	if strings.TrimSpace(target.model) == "" {
		t.Fatal("真实模型标识不能为空")
	}
	if target.model != testkit.DefaultPiModel {
		t.Fatalf("模型必须显式给出，实际: %s", target.model)
	}
	ws := testkit.NewAt(t, t.WorkspaceDir())
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

// 原 TestRemoteWorkflowRequiresHostKeyVerification 校验测试入口不会关闭主机密钥校验。
//
// 主机密钥校验属于硬性约束：测试不得通过关闭校验来规避失败，因此这里显式声明
// 该约束，并确认配置中不存在"跳过校验"的开关。
func runRemoteWorkflowRequiresHostKeyVerification(t *testrunner.Context) {
	loadTarget(t)
	sshConfig, _ := testkit.LoadSSHConfig(t)
	if strings.TrimSpace(sshConfig.PrivateKeyPath) == "" {
		t.Fatalf("必须通过 %s 配置私钥路径，本版本只支持私钥认证", testkit.ConfigPath(t, testkit.SSHConfigFile))
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowOpTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("上下文不可用: %v", err)
	}
}

// 原 TestRemoteE2EPreconditions 校验真实端到端验收的前置条件并输出实际环境信息。
//
// 该用例不创建容器、不调用模型：它把"配置不全导致的跳过"与"配置齐全后的真实失败"
// 区分开，并记录实际观测到的 Docker 版本与模型标识，便于事后复核。
func runRemoteE2EPreconditions(t *testrunner.Context) {
	env := newE2EEnv(t)
	// 预检校验工作空间是否可写，因此这里使用远程机器上真实存在的目录：
	// 不创建任何测试专属目录，也不修改远端任何内容。
	preflight := env.runner.Preflight(env.ctx, docker.PreflightOptions{
		WorkspaceRoot:   "/tmp",
		CreateWorkspace: false,
	})
	if !preflight.Ready() {
		t.Fatalf("远程 Docker 预检未通过(%s): %s", preflight.ErrorCode, preflight.Detail)
	}
	t.Logf("REMOTE_DOCKER CLI=%s ENGINE=%s API=%s MIN_API=%s",
		preflight.CLIVersion, preflight.EngineVersion,
		preflight.APIVersion, preflight.MinAPIVersion)
	if preflight.CLIVersion != docker.MinimumCLIVersion &&
		preflight.CLIVersion < docker.MinimumCLIVersion {
		t.Fatalf("Docker CLI 版本低于基线 %s: %s",
			docker.MinimumCLIVersion, preflight.CLIVersion)
	}
	if preflight.EngineVersion < docker.MinimumEngineVersion {
		t.Fatalf("Docker Engine 版本低于基线 %s: %s",
			docker.MinimumEngineVersion, preflight.EngineVersion)
	}
	t.Logf("GIT_REPOSITORY=%s", env.repo.ResolvedRepository())
	t.Logf("PI_MODEL=%s", env.model)
}
