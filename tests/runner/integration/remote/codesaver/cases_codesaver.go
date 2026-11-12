// 本文件是 tests/integration/remote/codesaver/codesaver_test.go 中用例的迁移结果。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、工作空间取编排进程下发的目录、
// 包级容器终检改为用例级终检；断言与远程操作保持原样。
package codesaver

import (
	"context"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记真实远程代码保存 1 条用例。
var _ = register("save-code-inside-remote-container", runSaveCodeInsideRemoteContainer)

// 原 TestSaveCodeInsideRemoteContainer 在真实远程容器内完成提交、推送与合并。
func runSaveCodeInsideRemoteContainer(t *testrunner.Context) {
	loaded := loadTarget(t)
	workspace := newWorkspace(t)
	t.Logf("测试工作空间: %s", workspace.Root())
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	client := loaded.dial(t, workspace)
	// 连接必须通过 t.Cleanup 关闭，而不是 defer：容器清理同样注册为 t.Cleanup，
	// 后进先出保证连接在容器清理完成之后才关闭。
	workspace.Cleanup(client.Close)
	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	guardContainerResiduals(t, runner)
	suffix := randomSuffix(t)
	container := createTestContainer(t, ctx, runner, loaded.image, suffix, testWorkDir)
	dockerID := container.DockerID
	// 容器清理责任已在创建之前登记：用例结束时会停止并删除它，并按真实远程状态
	// 核验它确实已不存在。
	taskID := "task-" + suffix
	taskBranch, branchErr := domain.TaskBranchName(taskID)
	if branchErr != nil {
		t.Fatalf("生成任务分支失败: %v", branchErr)
	}
	barePath := prepareTestRepository(t, ctx, runner, dockerID, testWorkDir, taskBranch)
	t.Logf("测试仓库: %s（bare: %s）", testWorkDir, barePath)
	recorder := &memoryRecorder{}
	saver, err := codesaver.New(codesaver.Options{
		Executor: containerExecutor{runner: runner, dockerID: dockerID},
		Guard:    containerGuard{container: container},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	// 在容器内修改代码，模拟 Agent 已经完成开发。
	writeContainerFile(t, ctx, runner, dockerID, testWorkDir, "feature.go")
	result, err := saver.Run(ctx, codesaver.Request{
		MachineID:      container.HostID.String(),
		ContainerID:    container.ID.String(),
		DockerID:       dockerID,
		TaskID:         taskID,
		WorkDir:        testWorkDir,
		RemoteName:     testRemoteName,
		BaseBranch:     testBaseBranch,
		MergeAfterPush: true,
		CommitMessage:  "AgentFlow: 远程容器代码保存测试",
		Author: domain.GitAuthor{
			Name:  "AgentFlow Tester",
			Email: "agentflow@example.com",
		},
	})
	if err != nil {
		t.Fatalf("远程容器内代码保存失败: %v", err)
	}
	record := recorder.single(t)
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应为 succeeded，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	if record.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", record.MergeStatus)
	}
	if !result.Succeeded() {
		t.Fatal("完整保存流程应判定成功")
	}
	remoteTask := remoteRef(t, ctx, runner, dockerID, barePath, "refs/heads/"+record.TaskBranch)
	if remoteTask != record.CommitSHA {
		t.Fatalf("远程任务分支应为 %s，实际 %s", record.CommitSHA, remoteTask)
	}
	remoteBase := remoteRef(t, ctx, runner, dockerID, barePath, "refs/heads/"+testBaseBranch)
	if remoteBase != record.MergeSHA {
		t.Fatalf("远程基础分支应为 %s，实际 %s", record.MergeSHA, remoteBase)
	}
}
