// 本文件提供"远端代码保存副作用结果不明时需求删除失败关闭"的真实环境验收。
//
// 运行方式与同目录的 codesaver_test.go 相同：需要真实 SSH 目标、真实远程 Docker 与
// tests/config/docker.yml 中的任务镜像（remote_enabled 必须为 true）。
//
// 场景严格对应方案 R1-E2：
//  1. 在真实容器内准备真实 bare 仓库，制造一次真实的提交与推送；
//  2. 让"推送已成功"的本地持久化失败，使数据库里的记录停留在"推送未确认"；
//  3. 用真实 SQLite 与真实需求服务对该任务请求删除，必须返回冲突且不写入任何删除标记；
//  4. 回查远端 Git 与容器现场，确认删除失败关闭后既没有回滚 Git，也没有清理容器。
//
// 这样构造的是"远端操作已经发生、本地没有持久化结果"，与"远端操作没有开始"不同：
// 后者由应用层确定性用例覆盖。

package codesaver_remote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// failingRecorder 在第 N 次写入时失败，用于制造"远端已发生、本地未持久化"。
//
// 失败点固定在推送结果写入：提交结果已经落盘，推送的真实结果没有落盘，因此数据库里的
// 记录仍然是推送 pending，而远端任务分支已经指向新的提交。
type failingRecorder struct {
	*memoryRecorder
	// updates 是已发生的 Update 调用次数。
	updates int
}

// Update 在"推送结果写回"时稳定失败，模拟数据库在关键写入上不可用。
//
// 稳定失败必须覆盖组件内部的重试，否则一次瞬时失败会被重试掩盖；而"记录不可信"的
// 标记写入（PersistStatus=unconfirmed）仍然允许落盘，这正是生产代码的真实路径。
func (r *failingRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	r.updates++
	if record.PushStatus == domain.PushStatusSucceeded && !record.PersistStatus.IsUnconfirmed() {
		return errors.New("模拟本地持久化失败")
	}
	return r.memoryRecorder.Update(ctx, record)
}

// TestDeleteRequirementRejectsUnknownCodeSaveSideEffect 校验真实副作用结果不明时拒绝删除。
func TestDeleteRequirementRejectsUnknownCodeSaveSideEffect(t *testing.T) {
	loaded := loadTarget(t)
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	client := loaded.dial(t, workspace)
	workspace.Cleanup(client.Close)
	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}

	// 先建好真实数据库现场：需求与处于 failed 的任务（failed 属于允许随需求删除的状态）。
	db, err := sqlite.Open(ctx, sqlite.Options{Path: filepath.Join(workspace.Root(), "delete.db")})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	workspace.Cleanup(db.Close)
	now := time.Now().UTC()
	requirement, err := domain.NewRequirement("真实环境删除验收", "验证结果不明时失败关闭", domain.PriorityNormal, now)
	if err != nil {
		t.Fatalf("构造需求失败: %v", err)
	}
	if err := db.CreateRequirement(ctx, requirement); err != nil {
		t.Fatalf("写入需求失败: %v", err)
	}
	hostID := domain.NewID()
	task, err := domain.NewTask(
		requirement.ID,
		testRepositoryURL,
		testBaseBranch,
		"",
		now,
	)
	if err != nil {
		t.Fatalf("构造任务失败: %v", err)
	}
	task.HostID = &hostID
	task.Config = domain.TaskConfig{
		Goal:            "在真实容器内完成代码保存",
		Environment:     domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "node"},
		PlannerRoleID:   "planner",
		DeveloperRoleID: "developer",
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	for _, next := range []domain.TaskStatus{domain.TaskStatusFailed} {
		if err := task.Transition(next, now); err != nil {
			t.Fatalf("任务迁移到 %s 失败: %v", next, err)
		}
	}
	if err := db.UpdateTask(ctx, task); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}

	suffix := randomSuffix(t)
	container := createTestContainer(t, ctx, runner, loaded.image, suffix, testWorkDir)
	dockerID := container.DockerID
	taskBranch, err := domain.TaskBranchName(task.ID.String())
	if err != nil {
		t.Fatalf("生成任务分支失败: %v", err)
	}
	barePath := prepareTestRepository(t, ctx, runner, dockerID, testWorkDir, taskBranch)
	writeContainerFile(t, ctx, runner, dockerID, testWorkDir, "feature.go")

	recorder := &failingRecorder{memoryRecorder: &memoryRecorder{}}
	saver, err := codesaver.New(codesaver.Options{
		Executor: containerExecutor{runner: runner, dockerID: dockerID},
		Guard:    containerGuard{container: container},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("构造代码保存组件失败: %v", err)
	}
	result, runErr := saver.Run(ctx, codesaver.Request{
		MachineID:     container.HostID.String(),
		ContainerID:   container.ID.String(),
		DockerID:      dockerID,
		TaskID:        task.ID.String(),
		WorkDir:       testWorkDir,
		RemoteName:    testRemoteName,
		BaseBranch:    testBaseBranch,
		CommitMessage: "AgentFlow: 结果不明场景验收",
		Author: domain.GitAuthor{
			Name:  "AgentFlow Tester",
			Email: "agentflow@example.com",
		},
	})
	t.Logf(
		"保存结果: 落盘=%t 推送状态=%s；错误=%v；持久化失败=%v；Update 次数=%d",
		result.Persisted(),
		result.Record.PushStatus,
		runErr,
		result.PersistenceErrors,
		recorder.updates,
	)
	if runErr == nil {
		t.Fatal("本地持久化失败时保存必须返回错误")
	}

	// 数据库里留下的是"记录不可信"的现场：推送已经在远端发生，但写回失败使记录被标记为
	// unconfirmed，删除流程因此不能只凭数据库里的状态推断操作没有发生。
	stored := recorder.single(t)
	if stored.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交结果应先落盘，实际 %s", stored.CommitStatus)
	}
	if stored.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("远端推送已经发生，记录中的推送结果应为 succeeded，实际 %s", stored.PushStatus)
	}
	if !stored.PersistStatus.IsUnconfirmed() {
		t.Fatalf("写入失败必须把记录标记为 unconfirmed，实际 %s", stored.PersistStatus)
	}
	decision := domain.DecideCodeSaveResume(stored)
	if decision.Action != domain.CodeSaveResumeVerify {
		t.Fatalf("推送结果不明时必须先核验，实际 %s（%s）", decision.Action, decision.Summary)
	}
	if err := db.CreateCodeSaveRecord(ctx, stored); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	// 远端确实已经收到提交：这是"远端操作已经发生"的唯一证据来源。
	remoteTask := remoteRef(t, ctx, runner, dockerID, barePath, "refs/heads/"+stored.TaskBranch)
	if remoteTask != stored.CommitSHA {
		t.Fatalf("远端任务分支应为 %s，实际 %s", stored.CommitSHA, remoteTask)
	}

	service, err := app.NewRequirementService(app.RequirementServiceOptions{
		Coordination: app.NewTaskExecutionCoordinator(),
		Store:        db,
	})
	if err != nil {
		t.Fatalf("构造需求服务失败: %v", err)
	}
	if _, deleteErr := service.Delete(
		ctx,
		requirement.ID.String(),
	); !errors.Is(deleteErr, app.ErrRequirementStateConflict) {
		t.Fatalf("结果不明时应拒绝删除，实际: %v", deleteErr)
	}

	persistedRequirement, err := db.GetRequirement(ctx, requirement.ID)
	if err != nil {
		t.Fatalf("读取需求失败: %v", err)
	}
	if persistedRequirement.DeletedAt != nil {
		t.Fatal("删除被拒绝时不得写入需求删除时间")
	}
	persistedTask, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persistedTask.DeletedAt != nil {
		t.Fatal("删除被拒绝时不得写入任务删除时间")
	}

	// 失败关闭不得触及远端副作用：Git 不回滚，容器不被清理。
	if after := remoteRef(
		t,
		ctx,
		runner,
		dockerID,
		barePath,
		"refs/heads/"+stored.TaskBranch,
	); after != stored.CommitSHA {
		t.Fatalf("删除被拒绝后远端任务分支不得变化: %s", after)
	}
	inspect, err := runner.InspectContainer(ctx, dockerID)
	if err != nil {
		t.Fatalf("读取容器状态失败: %v", err)
	}
	if !inspect.IsActivelyRunning() {
		t.Fatalf("删除被拒绝后容器必须保持运行: %s", inspect.State)
	}
}
