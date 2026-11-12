// 本文件是 tests/integration/remote/agent/agent_test.go 中用例的迁移结果（2 条）。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、工作空间取编排进程下发的目录、
// 包级容器终检改为用例级终检；断言与远程操作保持原样。
package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记远程容器内真实 Pi 2 条用例，顺序与源文件一致。
//
// 两条用例都要先准备远端镜像、再执行真实容器内操作，执行上限按来源 go test 的
// 口径放大到 15 分钟。
var (
	_ = register("source-fetch", 15*time.Minute, runRemoteSourceFetch)
	_ = register("agent-run-sessions", 20*time.Minute, runRemoteAgentRunSessions)
)

// 原 TestRemoteSourceFetch 验证代码确实被获取到远程任务容器内部。
func runRemoteSourceFetch(t *testrunner.Context) {
	tg := requireEnabled(t)
	gitConfig := resolveGitConfig(t)
	repository := gitConfig.ResolvedRepository()
	ws := newWorkspace(t)
	client := dial(t, ws, tg)
	exec := newRemoteExecutor(t, client)
	guardContainerResiduals(t, exec.runner)
	ctx, cancel := context.WithTimeout(context.Background(), remoteRunTimeout)
	t.Cleanup(cancel)

	suffix := randomSuffix(t)
	record := createContainer(t, ctx, exec, suffix)
	probeContainerTools(t, ctx, exec, record.DockerID)

	applyGitCredentials(t, ctx, exec, record.DockerID, gitConfig)
	taskID := "task-" + suffix
	provider, err := source.NewProvider(source.Options{
		Executor: exec,
		Guard:    &guard{runner: exec.runner, record: record},
	})
	if err != nil {
		t.Fatalf("构造代码来源失败: %v", err)
	}
	result, err := provider.Fetch(ctx, source.Request{
		TaskID:        taskID,
		ContainerID:   record.ID.String(),
		RepositoryURL: repository,
	})
	if err != nil {
		t.Fatalf("远程代码获取失败: %v", err)
	}
	if result.Commit == "" {
		t.Fatalf("结果缺少 commit SHA")
	}
	if result.TaskBranch != "agent/"+taskID {
		t.Fatalf("期望任务分支 agent/%s，实际 %q", taskID, result.TaskBranch)
	}
	// 代码必须在容器内部，且容器内目录确实存在 Git 工作区。
	check, checkErr := exec.runner.ExecCommand(ctx, record.DockerID, []string{
		"sh", "-c",
		"test -d " + containerWorkDir + "/.git && git -C " + containerWorkDir + " rev-parse HEAD",
	}, remoteOpTimeout)
	if checkErr != nil || !check.Succeeded() {
		t.Fatalf("容器内工作区校验失败: %v（退出码 %d）", checkErr, check.ExitCode)
	}
	containerCommit := strings.TrimSpace(check.Stdout)
	if containerCommit != result.Commit {
		t.Fatalf("容器内 commit 与结果不一致: %s != %s", containerCommit, result.Commit)
	}
	t.Logf("CONTAINER_COMMIT=%s", containerCommit)
	t.Logf("BASE_BRANCH=%s", result.BaseBranch)
	// 远程宿主机上不应出现项目代码目录。
	hostCheck, hostErr := exec.runner.Run(
		ctx,
		"host check",
		"test -d "+containerWorkDir+" && echo present || echo absent",
		remoteOpTimeout,
	)
	if hostErr != nil {
		t.Fatalf("检查宿主机目录失败: %v", hostErr)
	}
	t.Logf("HOST_WORKDIR=%s", strings.TrimSpace(hostCheck.Stdout))
}

// 原 TestRemoteAgentRunSessions 验证容器内 Pi 调用、独立 Session 与本机持久化。
func runRemoteAgentRunSessions(t *testrunner.Context) {
	tg := requireEnabled(t)
	model := resolveModel(t)
	containerPi := resolveContainerPi(t)
	ws := newWorkspace(t)
	client := dial(t, ws, tg)
	exec := newRemoteExecutor(t, client)
	guardContainerResiduals(t, exec.runner)
	ctx, cancel := context.WithTimeout(context.Background(), remoteRunTimeout)
	t.Cleanup(cancel)

	suffix := randomSuffix(t)
	record := createContainer(t, ctx, exec, suffix)
	probeContainerTools(t, ctx, exec, record.DockerID)
	model = applyContainerPi(t, ctx, exec, record.DockerID, containerPi, model)
	requireContainerModel(t, ctx, exec, record.DockerID, model)

	sessionRoot := ws.SessionRoot()
	runner, err := pi.NewContainerRunner(pi.Options{
		Timeout:       remoteRunTimeout,
		ExportTimeout: remoteOpTimeout,
	}, exec, &guard{runner: exec.runner, record: record})
	if err != nil {
		t.Fatalf("构造容器内 Runner 失败: %v", err)
	}
	// 两次调用必须使用不同的调用标识与 Session 文件。
	invocationIDs := make([]string, 0, 2)
	sessionPaths := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		result, runErr := runner.Run(ctx, agent.RunRequest{
			SessionID:   fmt.Sprintf("session-%s-%d", suffix, index),
			TaskID:      "task-" + suffix,
			RoleType:    "developer",
			Model:       model,
			Prompt:      "只回答 READY 这一个词，不要使用任何工具。",
			SessionRoot: sessionRoot,
			Timeout:     remoteRunTimeout,
			Container: &agent.ContainerTarget{
				ContainerID: record.ID.String(),
				DockerID:    record.DockerID,
				WorkDir:     containerWorkDir,
				SessionDir:  containerSessions,
			},
		}, nil)
		if result.Session.JSONLPath != "" {
			t.Logf("SESSION_JSONL_%d=%s", index, result.Session.JSONLPath)
		}
		if runErr != nil {
			t.Fatalf("第 %d 次远程 Agent 调用失败: %v（Session: %s）",
				index+1, runErr, result.Session.JSONLPath)
		}
		if result.Status != agent.RunCompleted {
			t.Fatalf("第 %d 次调用期望状态 %s，实际 %s",
				index+1, agent.RunCompleted, result.Status)
		}
		if result.ContainerID != record.ID.String() {
			t.Fatalf("结果中的容器标识不符: %s", result.ContainerID)
		}
		if result.DockerID != record.DockerID {
			t.Fatalf("结果中的 Docker 容器 ID 不符: %s", result.DockerID)
		}
		if result.Session.Completeness != agent.SessionComplete {
			t.Fatalf("第 %d 次调用期望 Session 完整，实际 %s（%s）",
				index+1, result.Session.Completeness, result.Session.Error)
		}
		if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
			t.Fatalf("本机 Session 文件不存在: %v", statErr)
		}
		invocationIDs = append(invocationIDs, result.InvocationID)
		sessionPaths = append(sessionPaths, result.Session.JSONLPath)
	}
	if invocationIDs[0] == invocationIDs[1] {
		t.Fatalf("两次调用使用了相同的调用标识: %s", invocationIDs[0])
	}
	if sessionPaths[0] == sessionPaths[1] {
		t.Fatalf("两次调用使用了相同的 Session 文件: %s", sessionPaths[0])
	}
	// 删除容器后本机 Session 仍可读。
	if _, removeErr := exec.runner.RemoveContainer(ctx, record.DockerID); removeErr != nil {
		t.Fatalf("删除测试容器失败: %v", removeErr)
	}
	for _, sessionPath := range sessionPaths {
		content, readErr := os.ReadFile(sessionPath)
		if readErr != nil {
			t.Fatalf("容器删除后本机 Session 应仍可读: %v", readErr)
		}
		if len(content) == 0 {
			t.Fatalf("本机 Session 内容为空: %s", sessionPath)
		}
	}
	t.Logf("INVOCATION_IDS=%s,%s", invocationIDs[0], invocationIDs[1])
}
