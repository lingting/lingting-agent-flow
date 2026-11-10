// 本文件是 tests/integration/local/codesaver/codesaver_test.go 中 12 个用例的迁移结果。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、仓库根目录取用例工作空间；
// 断言与注释保持原样。
package codesaver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 12 条用例，顺序与源文件一致，便于与 R0 冻结清单逐条对账。
var (
	_ = register("save-commits-and-pushes-task-branch", runSaveCommitsAndPushesTaskBranch)
	_ = register("save-no-changes-does-not-create-commit", runSaveNoChangesDoesNotCreateCommit)
	_ = register("save-merge-includes-task-commits", runSaveMergeIncludesTaskCommits)
	_ = register("push-failure-is-not-reported-as-success", runPushFailureIsNotReportedAsSuccess)
	_ = register("merge-conflict-keeps-inspectable-state", runMergeConflictKeepsInspectableState)
	_ = register("concurrent-saves-use-separate-repositories", runConcurrentSavesUseSeparateRepositories)
	_ = register("inspect-worktree-reports-branch-and-changes", runInspectWorktreeReportsBranchAndChanges)
	_ = register("inspect-worktree-rejects-unmanaged-container", runInspectWorktreeRejectsUnmanagedContainer)
	_ = register("commit-succeeded-but-record-failed-is-recoverable", runCommitSucceededButRecordFailedIsRecoverable)
	_ = register("observe-detects-remote-mismatch", runObserveDetectsRemoteMismatch)
	_ = register("observe-reports-unreachable-remote", runObserveReportsUnreachableRemote)
	_ = register("observe-confirms-merge-applied", runObserveConfirmsMergeApplied)
)

// 原 TestSaveCommitsAndPushesTaskBranch 校验真实 Git 下生成提交并推送任务分支。
func runSaveCommitsAndPushesTaskBranch(t *testrunner.Context) {
	instance := newRepo(t)
	// 工作目录映射到真实仓库：执行器把容器内路径替换为 localRepo。
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	result, err := saver.Run(context.Background(), request())
	if err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	record := recorder.single(t)
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应为 succeeded，实际 %s", record.CommitStatus)
	}
	head := instance.rev(t, "HEAD")
	if record.CommitSHA != head {
		t.Fatalf("记录的提交 SHA 应为 %s，实际 %s", head, record.CommitSHA)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
	remoteHead := instance.bareRev(t, "refs/heads/"+testTaskBranch)
	if remoteHead != head {
		t.Fatalf("远程任务分支应指向 %s，实际 %s", head, remoteHead)
	}
	if record.RemoteCommitSHA != remoteHead {
		t.Fatalf("记录的远程 commit 应为 %s，实际 %s", remoteHead, record.RemoteCommitSHA)
	}
	if record.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("默认不应合并，实际 %s", record.MergeStatus)
	}
	baseRemote := instance.bareRev(t, "refs/heads/"+testBaseBranch)
	if baseRemote == head {
		t.Fatal("未请求合并时基础分支不应包含任务提交")
	}
	if !result.Record.SavedToRemote() {
		t.Fatal("任务分支推送成功后 SavedToRemote 应为真")
	}
	t.Logf("用例工作空间: %s", filepath.Dir(instance.work))
}

// 原 TestSaveNoChangesDoesNotCreateCommit 校验没有变更时不制造空提交。
func runSaveNoChangesDoesNotCreateCommit(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	before := instance.rev(t, "HEAD")
	result, err := saver.Run(context.Background(), request())
	if err != nil {
		t.Fatalf("无变更时不应失败: %v", err)
	}
	if !result.NoChanges {
		t.Fatal("期望记录 no_changes")
	}
	if after := instance.rev(t, "HEAD"); after != before {
		t.Fatalf("不应创建空提交: %s -> %s", before, after)
	}
	record := recorder.single(t)
	if record.CommitStatus != domain.CommitStatusNoChanges {
		t.Fatalf("提交状态应为 no_changes，实际 %s", record.CommitStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("远程已包含任务分支时推送状态应为 succeeded，实际 %s", record.PushStatus)
	}
}

// 原 TestSaveMergeIncludesTaskCommits 校验显式开启合并后基础分支包含任务提交。
func runSaveMergeIncludesTaskCommits(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	saveRequest := request()
	saveRequest.MergeAfterPush = true
	result, err := saver.Run(context.Background(), saveRequest)
	if err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	record := recorder.single(t)
	if record.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", record.MergeStatus)
	}
	remoteBase := instance.bareRev(t, "refs/heads/"+testBaseBranch)
	if remoteBase != record.MergeSHA {
		t.Fatalf("远程基础分支应为 %s，实际 %s", record.MergeSHA, remoteBase)
	}
	// 基础分支历史必须包含任务提交。
	log := instance.git(t, instance.bare, "log", "--format=%H", "refs/heads/"+testBaseBranch)
	if !strings.Contains(log, record.CommitSHA) {
		t.Fatalf("基础分支历史应包含任务提交 %s: %s", record.CommitSHA, log)
	}
	// 合并提交必须同时包含两个父提交。
	parents := strings.Fields(instance.git(
		t,
		instance.bare,
		"rev-list", "--parents", "-n", "1",
		"refs/heads/"+testBaseBranch,
	))
	if len(parents) != 3 {
		t.Fatalf("合并提交应有两个父提交: %v", parents)
	}
	if !result.Succeeded() {
		t.Fatal("完整保存流程应判定成功")
	}
}

// 原 TestPushFailureIsNotReportedAsSuccess 校验推送失败不会被误报为成功。
func runPushFailureIsNotReportedAsSuccess(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	// 让远程不可访问：执行器把远程映射为不存在的仓库。
	executor.brokenRemote = true
	_, err := saver.Run(context.Background(), request())
	if err == nil {
		t.Fatal("推送失败时必须返回错误")
	}
	record := recorder.single(t)
	if record.PushStatus == domain.PushStatusSucceeded {
		t.Fatal("推送失败不得记录成功")
	}
	if record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交应已成功，实际 %s", record.CommitStatus)
	}
	if record.SavedToRemote() {
		t.Fatal("推送失败时 SavedToRemote 必须为假")
	}
	if !record.RequiresManualRecovery() {
		t.Fatal("推送失败应标记为需要人工处理")
	}
}

// 原 TestMergeConflictKeepsInspectableState 校验合并冲突时保留可检查的 Git 状态。
func runMergeConflictKeepsInspectableState(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	// 在任务分支与基础分支上制造同一文件的冲突内容。
	instance.writeFile(t, "internal/conflict.go", "任务分支版本\n")
	instance.git(t, instance.work, "add", "--all")
	instance.git(t, instance.work, "commit", "-m", "任务分支修改")
	instance.git(t, instance.work, "checkout", testBaseBranch)
	instance.writeFile(t, "internal/conflict.go", "基础分支版本\n")
	instance.git(t, instance.work, "add", "--all")
	instance.git(t, instance.work, "commit", "-m", "基础分支修改")
	// 直接推送到 bare 仓库：工作仓库的 origin 已被改写为 https 形式地址。
	instance.git(t, instance.work, "push", instance.bare, testBaseBranch)
	instance.git(t, instance.work, "checkout", testTaskBranch)
	taskCommit := instance.rev(t, "refs/heads/"+testTaskBranch)
	saveRequest := request()
	saveRequest.MergeAfterPush = true
	_, err := saver.Run(context.Background(), saveRequest)
	if codesaver.ErrorCodeOf(err) != codesaver.CodeMergeConflict {
		t.Fatalf("期望 merge_conflict，实际 %v", err)
	}
	record := recorder.single(t)
	if record.MergeStatus != domain.MergeStatusConflict {
		t.Fatalf("合并状态应为 conflict，实际 %s", record.MergeStatus)
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务分支应保持推送成功，实际 %s", record.PushStatus)
	}
	remoteTask := instance.bareRev(t, "refs/heads/"+testTaskBranch)
	if remoteTask != taskCommit {
		t.Fatalf("远程任务分支应保留已推送提交 %s，实际 %s", taskCommit, remoteTask)
	}
	// 冲突后不应留下未完成的合并状态，也不应重置任务分支。
	status := instance.git(t, instance.work, "status", "--porcelain")
	if strings.Contains(status, "UU") {
		t.Fatalf("应中止自动合并，实际工作区仍有冲突: %s", status)
	}
	branch := strings.TrimSpace(instance.git(t, instance.work, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != testBaseBranch {
		t.Logf("合并失败后当前分支为 %s", branch)
	}
}

// 原 TestConcurrentSavesUseSeparateRepositories 校验并发操作使用独立仓库。
func runConcurrentSavesUseSeparateRepositories(t *testrunner.Context) {
	requireGit(t)
	const total = 3
	var group sync.WaitGroup
	errs := make(chan error, total)
	for index := 0; index < total; index++ {
		group.Add(1)
		go func(offset int) {
			defer group.Done()
			instance := newRepo(t)
			executor := newExecutor(instance)
			recorder := newRecorder()
			saver := newSaver(t, instance, recorder, executor)
			instance.writeFile(t, "internal/concurrent.go", "package internal\n")
			if _, err := saver.Run(context.Background(), request()); err != nil {
				errs <- err
				return
			}
			remoteHead := instance.bareRev(t, "refs/heads/"+testTaskBranch)
			if remoteHead != instance.rev(t, "HEAD") {
				errs <- errors.New("远程任务分支与本地 HEAD 不一致")
				return
			}
			errs <- nil
		}(index)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发保存不应失败: %v", err)
		}
	}
}

// 原 TestInspectWorktreeReportsBranchAndChanges 校验只读核验返回真实分支、HEAD 与变更。
//
// 该用例直接使用本机真实 Git 仓库，验证"记录代码状态"与"保存代码"看到的是同一个
// 工作区：核验不提交、不推送，也不写入任何代码保存记录。
func runInspectWorktreeReportsBranchAndChanges(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	inspector, err := codesaver.NewInspector(codesaver.InspectorOptions{
		Executor: executor,
		Guard:    &fakeGuard{container: newContainer()},
	})
	if err != nil {
		t.Fatalf("构造工作区核验器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	clean, err := inspector.Inspect(ctx, request())
	if err != nil {
		t.Fatalf("核验干净工作区失败: %v", err)
	}
	if clean.Branch != testTaskBranch {
		t.Fatalf("分支不符: %s", clean.Branch)
	}
	if clean.Head != instance.rev(t, "HEAD") {
		t.Fatalf("HEAD 不符: %s", clean.Head)
	}
	if !clean.Clean {
		t.Fatalf("初始工作区应当是干净的: %+v", clean)
	}

	instance.writeFile(t, "internal/app.go", "package app\n// 修改\n")
	instance.writeFile(t, "internal/new.go", "package app\n")
	dirty, err := inspector.Inspect(ctx, request())
	if err != nil {
		t.Fatalf("核验脏工作区失败: %v", err)
	}
	if dirty.Clean {
		t.Fatalf("存在未提交变更时不应报告干净: %+v", dirty)
	}
	if dirty.Modified == 0 {
		t.Fatalf("应识别出已修改文件: %+v", dirty)
	}
	if dirty.Added == 0 {
		t.Fatalf("应识别出新增文件: %+v", dirty)
	}
	// 核验不得提交：HEAD 必须与核验前一致。
	if dirty.Head != clean.Head {
		t.Fatalf("核验不应改变 HEAD: %s -> %s", clean.Head, dirty.Head)
	}
	if executor.pushedCount() != 0 {
		t.Fatal("核验不应执行推送")
	}
}

// 原 TestInspectWorktreeRejectsUnmanagedContainer 校验容器不可用时核验直接失败。
func runInspectWorktreeRejectsUnmanagedContainer(t *testrunner.Context) {
	instance := newRepo(t)
	detached := newContainer()
	detached.Detached = true
	detached.State = domain.ContainerStateDetached
	inspector, err := codesaver.NewInspector(codesaver.InspectorOptions{
		Executor: newExecutor(instance),
		Guard:    &fakeGuard{container: detached},
	})
	if err != nil {
		t.Fatalf("构造工作区核验器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if _, inspectErr := inspector.Inspect(ctx, request()); inspectErr == nil {
		t.Fatal("已脱离管理的容器不应允许核验")
	}
}

// 原 TestCommitSucceededButRecordFailedIsRecoverable 校验真实 Git 下的提交落盘失败窗口。
//
// 场景：Git 真的产生了提交，但提交结果写不进数据库。此时数据库停留在
// commit_status=pending，唯一能区分"已提交"与"未提交"的证据是提交之前落盘的
// HEAD 基线。本用例验证这条证据链在真实 Git 上成立。
func runCommitSucceededButRecordFailedIsRecoverable(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	headBefore := instance.rev(t, "HEAD")
	// 只让"提交成功"那一次写入失败，基线写入仍然成功。
	recorder.failUpdateWhen(errors.New("database is locked"), func(record domain.CodeSaveRecord) bool {
		return record.CommitStatus == domain.CommitStatusSucceeded
	})
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	result, err := saver.Run(context.Background(), request())
	if err == nil {
		t.Fatal("状态写入失败时必须返回错误")
	}
	if codesaver.ErrorCodeOf(err) != codesaver.CodeStateUnconfirmed {
		t.Fatalf("期望 state_unconfirmed，实际 %v", err)
	}
	// 真实 Git 已经产生了提交。
	headAfter := instance.rev(t, "HEAD")
	if headAfter == headBefore {
		t.Fatal("真实 Git 应已产生新提交")
	}
	stored := recorder.snapshot(t, result.Record.ID)
	if stored.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("数据库应停留在 pending，实际 %s", stored.CommitStatus)
	}
	if stored.BaselineCommitSHA != headBefore {
		t.Fatalf("提交基线应为提交前的 HEAD %s，实际 %q", headBefore, stored.BaselineCommitSHA)
	}
	// 只读核验可以依据基线与真实 HEAD 确认"提交已经发生"。
	observation := saver.Observe(context.Background(), request(), stored)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if observation.HeadCommit != headAfter {
		t.Fatalf("核验读到的 HEAD 应为 %s，实际 %s", headAfter, observation.HeadCommit)
	}
	decision := domain.DecideCodeSaveRecovery(stored, observation)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认提交已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.CommitSHA != headAfter {
		t.Fatalf("确认的提交应为 %s，实际 %s", headAfter, decision.CommitSHA)
	}
	if decision.SafeToRetry {
		t.Fatal("提交已发生时不得提示可以重试")
	}
}

// 原 TestObserveDetectsRemoteMismatch 校验核验能识别远程与预期提交不一致。
//
// 远端指向其他提交时必须判为不确定并禁止自动重试：重推可能覆盖远程历史。
func runObserveDetectsRemoteMismatch(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	// 在任务分支之外推进远程任务分支：模拟其他来源已经推送过。
	instance.writeFile(t, "internal/other.go", "package internal\n")
	instance.git(t, instance.work, "add", "--all")
	instance.git(t, instance.work, "commit", "-m", "他人提交")
	// 直接推到 bare 仓库：工作仓库的 origin 已被改写成 https 形式，不能访问公网。
	instance.git(t, instance.work, "push", instance.bare, "HEAD:refs/heads/"+testTaskBranch)
	head := instance.rev(t, "HEAD")
	// 构造一条"提交已成功、推送仍是 pending"的记录，但预期提交指向别的 commit。
	record := domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       testTaskID,
		TaskBranch:   testTaskBranch,
		ContainerID:  testContainerID,
		RemoteName:   testRemoteName,
		BaseBranch:   testBaseBranch,
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    "1111111111111111111111111111111111111111",
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	observation := saver.Observe(context.Background(), request(), record)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if observation.RemoteTaskCommit != head {
		t.Fatalf("远程任务分支应为 %s，实际 %s", head, observation.RemoteTaskCommit)
	}
	decision := domain.DecideCodeSaveRecovery(record, observation)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("远程与预期不一致时应转人工核验，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("远程与预期不一致时不得提示可以重试")
	}
}

// 原 TestObserveReportsUnreachableRemote 校验远程不可达时保留不确定性。
func runObserveReportsUnreachableRemote(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	executor.mu.Lock()
	executor.brokenRemote = true
	executor.mu.Unlock()
	record := domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       testTaskID,
		TaskBranch:   testTaskBranch,
		ContainerID:  testContainerID,
		RemoteName:   testRemoteName,
		BaseBranch:   testBaseBranch,
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    instance.rev(t, "HEAD"),
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	observation := saver.Observe(context.Background(), request(), record)
	if !observation.ReadFailed {
		t.Fatal("远程不可达时必须报告核验失败")
	}
	decision := domain.DecideCodeSaveRecovery(record, observation)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("远程不可达时必须保留不确定性，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("远程不可达时不得提示可以重试")
	}
}

// 原 TestObserveConfirmsMergeApplied 校验真实合并后可以确认合并已经发生。
//
// 判定要求远程基础分支等于唯一的候选合并提交：第一父为合并前基线、第二父为
// 任务分支提交。仅仅"基础分支变了"不构成证据。
func runObserveConfirmsMergeApplied(t *testrunner.Context) {
	instance := newRepo(t)
	executor := newExecutor(instance)
	recorder := newRecorder()
	saver := newSaver(t, instance, recorder, executor)
	instance.writeFile(t, "internal/feature.go", "package internal\n")
	requestWithMerge := request()
	requestWithMerge.MergeAfterPush = true
	result, err := saver.Run(context.Background(), requestWithMerge)
	if err != nil {
		t.Fatalf("保存流程应成功: %v", err)
	}
	stored := recorder.snapshot(t, result.Record.ID)
	if stored.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("合并状态应为 succeeded，实际 %s", stored.MergeStatus)
	}
	if stored.BaselineBaseCommitSHA == "" {
		t.Fatal("合并前必须落盘基础分支基线")
	}
	// 把记录回退成"合并仍是 pending"，模拟合并结果没落盘的窗口。
	pending := stored
	pending.MergeStatus = domain.MergeStatusPending
	pending.MergeSHA = ""
	observation := saver.Observe(context.Background(), request(), pending)
	if observation.ReadFailed {
		t.Fatalf("核验不应失败: %s", observation.ReadError)
	}
	if !observation.MergeCommitResolvable {
		t.Fatalf("应能唯一确定合并提交，实际无法推算（%+v）", observation)
	}
	if observation.ExpectedMergeCommit != stored.MergeSHA {
		t.Fatalf("推算的合并提交应为 %s，实际 %s", stored.MergeSHA, observation.ExpectedMergeCommit)
	}
	decision := domain.DecideCodeSaveRecovery(pending, observation)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认合并已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.MergeSHA != stored.MergeSHA {
		t.Fatalf("确认的合并提交应为 %s，实际 %s", stored.MergeSHA, decision.MergeSHA)
	}
	if decision.SafeToRetry {
		t.Fatal("合并已发生时不得提示可以重试")
	}
}
