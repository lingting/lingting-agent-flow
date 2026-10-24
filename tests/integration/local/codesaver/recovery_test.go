// 代码保存恢复的持久化集成测试。
//
// 与 codesaver_test.go 的差别：这里使用真实 SQLite 文件与正式迁移保存记录，并
// 用真实 Git 仓库产生 commit / push / merge。因此断言的对象是数据库里留下的
// code_saves 行，而不是内存替身；"重启"是关闭连接后用同一路径重新打开。
//
// 只把容器内命令映射到本机仓库（与本包其余用例一致）：代码保存组件、领域判定与
// 存储层全部走生产代码。
package codesaver_local

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// recoveryFixture 是一套可跨"进程退出 → 重新打开数据库"的代码保存现场。
type recoveryFixture struct {
	ctx       context.Context
	instance  *repo
	executor  *localExecutor
	path      string
	db        *sqlite.DB
	workspace *testkit.Workspace
}

// newRecoveryFixture 在工作空间中创建真实仓库与真实数据库。
func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	workspace := testkit.New(t)
	path := filepath.Join(workspace.Root(), "agentflow.db")
	fixture := &recoveryFixture{
		ctx:       ctx,
		instance:  newRepo(t),
		path:      path,
		workspace: workspace,
	}
	fixture.executor = newExecutor(fixture.instance)
	fixture.db = fixture.open(t)
	return fixture
}

// open 打开（或重新打开）数据库并注册关闭清理。
func (f *recoveryFixture) open(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(f.ctx, sqlite.Options{Path: f.path})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %s: %v", f.path, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	return db
}

// restart 关闭当前连接并用同一文件重新打开，模拟进程退出后重新启动。
func (f *recoveryFixture) restart(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	f.db = f.open(t)
}

// saver 构造使用真实存储与真实仓库的代码保存组件。
func (f *recoveryFixture) saver(recorder codesaver.Recorder) *codesaver.Saver {
	saver, err := codesaver.New(codesaver.Options{
		Executor: f.executor,
		Guard:    &fakeGuard{container: newContainer()},
		Recorder: recorder,
	})
	if err != nil {
		panic(err)
	}
	return saver
}

// recordByTask 读取任务唯一的保存记录。
func (f *recoveryFixture) recordByTask(t *testing.T, taskID string) domain.CodeSaveRecord {
	t.Helper()
	records, err := f.db.ListCodeSaveRecordsByTask(f.ctx, taskID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("期望 1 条代码保存记录，实际 %d 条", len(records))
	}
	return records[0]
}

// failingRecorder 把真实存储包装成"指定阶段写入失败"的仓储。
//
// 失败来自存储层而不是业务代码：Git 操作真实发生，只有数据库这一侧失败，因此
// 构造的是真实的"Git 已成功、记录没落盘"窗口。
type failingRecorder struct {
	store storage.Store
	// failWhen 非空时让满足条件的写入失败。
	failWhen func(domain.CodeSaveRecord) bool
}

// Create 写入新记录。
func (r failingRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	return r.store.CreateCodeSaveRecord(ctx, record)
}

// Update 在谓词命中时返回错误，否则写入真实存储。
func (r failingRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	if r.failWhen != nil && r.failWhen(record) {
		return errors.New("数据库写入失败")
	}
	return r.store.UpdateCodeSaveRecord(ctx, record)
}

// Get 读取记录。
func (r failingRecorder) Get(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeSaveRecord, error) {
	return r.store.GetCodeSaveRecord(ctx, id)
}

// ListByTask 按任务列出记录。
func (r failingRecorder) ListByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	return r.store.ListCodeSaveRecordsByTask(ctx, taskID)
}

// storeRecorder 只做真实存储写入，用于重启后的只读核验。
type storeRecorder struct {
	store storage.Store
}

// Create 写入新记录。
func (r storeRecorder) Create(ctx context.Context, record domain.CodeSaveRecord) error {
	return r.store.CreateCodeSaveRecord(ctx, record)
}

// Update 更新记录。
func (r storeRecorder) Update(ctx context.Context, record domain.CodeSaveRecord) error {
	return r.store.UpdateCodeSaveRecord(ctx, record)
}

// Get 读取记录。
func (r storeRecorder) Get(
	ctx context.Context,
	id domain.ID,
) (*domain.CodeSaveRecord, error) {
	return r.store.GetCodeSaveRecord(ctx, id)
}

// ListByTask 按任务列出记录。
func (r storeRecorder) ListByTask(
	ctx context.Context,
	taskID string,
) ([]domain.CodeSaveRecord, error) {
	return r.store.ListCodeSaveRecordsByTask(ctx, taskID)
}

// verify 执行一次"读取记录 → 只读核验 → 领域判定 → 条件写回"的完整恢复路径。
//
// 它复现 ContainerService.VerifyCodeSave 的语义，但不引入 SSH 与 Docker：只读
// 核验由 codesaver.Saver.Observe 在真实仓库上完成。
func (f *recoveryFixture) verify(t *testing.T) (domain.CodeSaveRecoveryDecision, bool) {
	t.Helper()
	decision, persisted, _ := f.verifyWith(t, nil)
	return decision, persisted
}

// verifyWith 执行完整的恢复路径并额外返回数据库中的真实记录。
//
// 冲突时返回的记录是重新读取到的真实值：调用方必须依据它决定下一步，而不是
// 依据本次核验判定。
func (f *recoveryFixture) verifyWith(
	t *testing.T,
	concurrent func(*domain.CodeSaveRecord),
) (domain.CodeSaveRecoveryDecision, bool, domain.CodeSaveRecord) {
	t.Helper()
	record, err := f.db.GetCodeSaveRecord(f.ctx, f.recordByTask(t, testTaskID).ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if concurrent != nil {
		// 模拟"核验期间记录被其他执行推进"：并发写入发生在读取之后、写回之前。
		concurrent(record)
	}
	observation := f.saver(storeRecorder{store: f.db}).Observe(f.ctx, request(), *record)
	decision := domain.DecideCodeSaveRecovery(*record, observation)
	candidate, applyErr := record.ApplyRecovery(decision, time.Now().UTC())
	if applyErr != nil {
		t.Fatalf("写回恢复结论失败: %v", applyErr)
	}
	writeErr := f.db.UpdateCodeSaveRecoveryResult(f.ctx, candidate, record.RecoveryStatus)
	if writeErr != nil {
		if !errors.Is(writeErr, storage.ErrConflict) {
			t.Fatalf("写入核验结果失败: %v", writeErr)
		}
		// 冲突时重新读取真实记录：旧核验结果不得覆盖新事实。
		latest, readErr := f.db.GetCodeSaveRecord(f.ctx, record.ID)
		if readErr != nil {
			t.Fatalf("冲突后重新读取记录失败: %v", readErr)
		}
		return decision, false, *latest
	}
	return decision, true, candidate
}

// TestRecoveryCommitAppliedAfterPersistFailure 覆盖"提交已成功、记录没落盘"。
//
// 故障窗口：`git commit` 真实产生提交，但提交结果的写入失败，数据库停在
// commit_status=pending。重启后只读核验必须能依据基线 + 第一父关系确认提交
// 已经发生，并把阶段状态写回数据库。
func TestRecoveryCommitAppliedAfterPersistFailure(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	headBefore := fixture.instance.rev(t, "HEAD")
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			return record.CommitStatus == domain.CommitStatusSucceeded
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	_, err := fixture.saver(recorder).Run(fixture.ctx, request())
	if err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	if codesaver.ErrorCodeOf(err) != codesaver.CodeStateUnconfirmed {
		t.Fatalf("期望 state_unconfirmed，实际 %v", err)
	}
	// 真实 Git 已经产生提交。
	headAfter := fixture.instance.rev(t, "HEAD")
	if headAfter == headBefore {
		t.Fatal("真实 Git 应已产生新提交")
	}
	stored := fixture.recordByTask(t, testTaskID)
	if stored.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("数据库应停留在 pending，实际 %s", stored.CommitStatus)
	}
	if stored.BaselineCommitSHA != headBefore {
		t.Fatalf("提交基线应为 %s，实际 %q", headBefore, stored.BaselineCommitSHA)
	}
	// 重启后核验：只依据持久化记录与真实仓库事实。
	fixture.restart(t)
	decision, persisted := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认提交已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.CommitSHA != headAfter {
		t.Fatalf("确认的提交应为 %s，实际 %s", headAfter, decision.CommitSHA)
	}
	if decision.SafeToRetry {
		t.Fatal("提交已发生时不得提示可以重试")
	}
	if !persisted {
		t.Fatal("核验结论必须成功写回数据库")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("确认已发生的提交必须落盘为 succeeded，实际 %s", final.CommitStatus)
	}
	if final.CommitSHA != headAfter {
		t.Fatalf("落盘的提交应为 %s，实际 %s", headAfter, final.CommitSHA)
	}
}

// TestRecoveryPushAppliedAfterPersistFailure 覆盖"推送已成功、记录没落盘"。
func TestRecoveryPushAppliedAfterPersistFailure(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			return record.PushStatus == domain.PushStatusSucceeded
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	_, err := fixture.saver(recorder).Run(fixture.ctx, request())
	if err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	// 提交已经落盘，推送停在 pending：远程分支实际上已经收到提交。
	stored := fixture.recordByTask(t, testTaskID)
	if stored.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交应已落盘，实际 %s", stored.CommitStatus)
	}
	if stored.PushStatus != domain.PushStatusPending {
		t.Fatalf("推送应停留在 pending，实际 %s", stored.PushStatus)
	}
	remoteHead := fixture.instance.bareRev(t, "refs/heads/"+testTaskBranch)
	if remoteHead != stored.CommitSHA {
		t.Fatalf("远程任务分支应已包含提交 %s，实际 %s", stored.CommitSHA, remoteHead)
	}
	fixture.restart(t)
	decision, persisted := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认推送已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if !persisted {
		t.Fatal("核验结论必须成功写回数据库")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("确认已发生的推送必须落盘为 succeeded，实际 %s", final.PushStatus)
	}
	if final.RemoteCommitSHA != remoteHead {
		t.Fatalf("落盘的远程 commit 应为 %s，实际 %s", remoteHead, final.RemoteCommitSHA)
	}
}

// TestRecoveryMergeAppliedAfterPersistFailure 覆盖"合并已成功、记录没落盘"。
func TestRecoveryMergeAppliedAfterPersistFailure(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			return record.MergeStatus == domain.MergeStatusSucceeded
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	saveRequest := request()
	saveRequest.MergeAfterPush = true
	_, err := fixture.saver(recorder).Run(fixture.ctx, saveRequest)
	if err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	stored := fixture.recordByTask(t, testTaskID)
	if stored.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("任务分支推送应已落盘，实际 %s", stored.PushStatus)
	}
	if stored.MergeStatus != domain.MergeStatusPending {
		t.Fatalf("合并应停留在 pending，实际 %s", stored.MergeStatus)
	}
	if stored.BaselineBaseCommitSHA == "" {
		t.Fatal("合并前必须落盘基础分支基线")
	}
	remoteBase := fixture.instance.bareRev(t, "refs/heads/"+testBaseBranch)
	if remoteBase == stored.BaselineBaseCommitSHA {
		t.Fatal("远程基础分支应已被本次合并推进")
	}
	fixture.restart(t)
	decision, persisted := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认合并已经发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.MergeSHA != remoteBase {
		t.Fatalf("确认的合并提交应为 %s，实际 %s", remoteBase, decision.MergeSHA)
	}
	if !persisted {
		t.Fatal("核验结论必须成功写回数据库")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.MergeStatus != domain.MergeStatusSucceeded {
		t.Fatalf("确认已发生的合并必须落盘为 succeeded，实际 %s", final.MergeStatus)
	}
	if final.MergeSHA != remoteBase {
		t.Fatalf("落盘的合并提交应为 %s，实际 %s", remoteBase, final.MergeSHA)
	}
}

// TestRecoveryDoesNotAttributeForeignCommit 覆盖"HEAD 变化但不是本次提交"。
//
// 关键约束：HEAD 已变化不足以证明本次 commit 成功。只有第一父等于已记录基线时
// 才能归因；否则必须保留不确定性，且不得写回 succeeded。
func TestRecoveryDoesNotAttributeForeignCommit(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	headBefore := fixture.instance.rev(t, "HEAD")
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			return record.CommitStatus == domain.CommitStatusSucceeded
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.saver(recorder).Run(fixture.ctx, request()); err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	// 模拟"另一个来源"在任务分支上产生了提交：它的第一父是本次提交而不是记录的基线。
	fixture.instance.writeFile(t, "internal/other.go", "package internal\n")
	fixture.instance.git(t, fixture.instance.work, "add", "--all")
	fixture.instance.git(t, fixture.instance.work, "commit", "-m", "其他来源的提交")
	foreignHead := fixture.instance.rev(t, "HEAD")
	if foreignHead == headBefore {
		t.Fatal("测试现场应产生新的 HEAD")
	}
	fixture.restart(t)
	decision, _ := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("无法归因时必须保留不确定性，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.SafeToRetry {
		t.Fatal("无法归因时不得提示可以重试")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.CommitStatus == domain.CommitStatusSucceeded {
		t.Fatal("无法归因时不得把提交标记为成功")
	}
	if final.CommitSHA != "" {
		t.Fatalf("无法归因时不得写入提交 SHA，实际 %q", final.CommitSHA)
	}
}

// TestRecoveryPushMismatchIsNotReportedAsSuccess 覆盖"远程被其他参与者推进"。
func TestRecoveryPushMismatchIsNotReportedAsSuccess(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			return record.PushStatus == domain.PushStatusSucceeded
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.saver(recorder).Run(fixture.ctx, request()); err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	stored := fixture.recordByTask(t, testTaskID)
	// 其他参与者把远程任务分支推进到另一个提交。
	fixture.instance.writeFile(t, "internal/other.go", "package internal\n")
	fixture.instance.git(t, fixture.instance.work, "add", "--all")
	fixture.instance.git(t, fixture.instance.work, "commit", "-m", "他人的提交")
	fixture.instance.git(
		t,
		fixture.instance.work,
		"push",
		fixture.instance.bare,
		"HEAD:refs/heads/"+testTaskBranch,
	)
	foreignRemote := fixture.instance.bareRev(t, "refs/heads/"+testTaskBranch)
	if foreignRemote == stored.CommitSHA {
		t.Fatal("测试现场应让远程指向其他提交")
	}
	fixture.restart(t)
	decision, _ := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("远程与预期不一致时必须转人工，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.SafeToRetry {
		t.Fatal("远程不一致时不得提示可以重试（禁止强制推送）")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.PushStatus == domain.PushStatusSucceeded {
		t.Fatal("远程不一致时不得把推送标记为成功")
	}
	// 他人的提交没有被覆盖。
	if current := fixture.instance.bareRev(t, "refs/heads/"+testTaskBranch); current != foreignRemote {
		t.Fatalf("不得覆盖他人提交：期望 %s，实际 %s", foreignRemote, current)
	}
}

// TestRecoveryMissingBaselineStaysUnverifiable 覆盖"缺少基线"的现场。
//
// 历史记录没有基线时无法判断 HEAD 是否被本次操作推进：必须转人工核验，
// 而不是把"看起来没变化"当成可以重试的依据。
func TestRecoveryMissingBaselineStaysUnverifiable(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		testTaskID,
		testContainerID,
		testDockerID,
		testRemoteName,
		"",
		testTaskBranch,
		testBaseBranch,
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	fixture.restart(t)
	decision, _ := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("缺少基线时必须转人工，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.SafeToRetry {
		t.Fatal("缺少基线时不得提示可以重试")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.CommitStatus == domain.CommitStatusSucceeded {
		t.Fatal("缺少基线时不得把提交标记为成功")
	}
}

// TestRecoveryConflictKeepsNewerRecord 覆盖"核验期间记录被并发推进"。
//
// 旧核验结果不得覆盖新记录：写入按冲突失败，调用方必须重新读取真实记录，
// 且不能把这次核验报告为恢复成功。
func TestRecoveryConflictKeepsNewerRecord(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		testTaskID,
		testContainerID,
		testDockerID,
		testRemoteName,
		"",
		testTaskBranch,
		testBaseBranch,
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	baseline := fixture.instance.rev(t, "HEAD")
	if err := record.MarkBaselineCommit(baseline, time.Now().UTC()); err != nil {
		t.Fatalf("写入提交基线失败: %v", err)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	// 核验读取记录之后，另一个执行者先写入了结论：持有旧取值的写入必须冲突。
	concurrent := func(current *domain.CodeSaveRecord) {
		if err := fixture.db.UpdateCodeSaveRecovery(
			fixture.ctx,
			current.ID,
			current.RecoveryStatus,
			domain.RecoveryStatusUnverifiable,
			time.Now().UTC(),
		); err != nil {
			t.Fatalf("写入并发核验结论失败: %v", err)
		}
	}
	decision, persisted, latest := fixture.verifyWith(t, concurrent)
	if persisted {
		t.Fatal("记录已被推进时核验结论不得写回成功")
	}
	if decision.Status != domain.RecoveryStatusConfirmedNotApplied {
		t.Fatalf("HEAD 未变化时应确认提交未发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	// 调用方拿到的是重新读取到的真实记录，而不是本次核验判定。
	if latest.RecoveryStatus != domain.RecoveryStatusUnverifiable {
		t.Fatalf("冲突后必须返回数据库中的真实记录，实际 %s", latest.RecoveryStatus)
	}
	final := latest
	if final.RecoveryStatus != domain.RecoveryStatusUnverifiable {
		t.Fatalf("并发写入的结论不得被覆盖，实际 %s", final.RecoveryStatus)
	}
	if final.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("冲突时不得推进阶段状态，实际 %s", final.CommitStatus)
	}
}

// TestRecoveryConfirmedNotAppliedAllowsResume 覆盖"确认未发生"后的续跑资格。
//
// 确认未发生只表示"核验时有证据证明操作没有发生"：它允许重新执行，但真正的
// 副作用仍由代码保存组件在执行前重新核验分支、工作区与远程状态。
func TestRecoveryConfirmedNotAppliedAllowsResume(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		testTaskID,
		testContainerID,
		testDockerID,
		testRemoteName,
		"",
		testTaskBranch,
		testBaseBranch,
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := record.MarkBaselineCommit(fixture.instance.rev(t, "HEAD"), time.Now().UTC()); err != nil {
		t.Fatalf("写入提交基线失败: %v", err)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	fixture.restart(t)
	decision, persisted := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusConfirmedNotApplied {
		t.Fatalf("HEAD 未变化时应确认提交未发生，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if !decision.SafeToRetry {
		t.Fatal("确认未发生且记录可信时应允许重试")
	}
	if !persisted {
		t.Fatal("核验结论必须成功写回数据库")
	}
	final := fixture.recordByTask(t, testTaskID)
	// 确认"提交未发生"同时说明后续阶段也没有发生：不提交就不会推送，因此整条
	// 记录不再有未决副作用，可以重新执行。
	if action := domain.DecideCodeSaveResume(final).Action; action != domain.CodeSaveResumeProceed {
		t.Fatalf("确认未发生后应允许重新执行，实际 %s", action)
	}
	// 重新执行：真实 Git 会产生提交并推送，且不会因为上一次核验而重复提交。
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	result, runErr := fixture.saver(storeRecorder{store: fixture.db}).Run(fixture.ctx, request())
	if runErr != nil {
		t.Fatalf("确认未发生后重新执行应成功: %v", runErr)
	}
	if result.Record.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("重新执行应产生提交，实际 %s", result.Record.CommitStatus)
	}
	records, listErr := fixture.db.ListCodeSaveRecordsByTask(fixture.ctx, testTaskID)
	if listErr != nil {
		t.Fatalf("读取代码保存记录失败: %v", listErr)
	}
	if len(records) != 2 {
		t.Fatalf("重新执行应追加一条新记录，实际 %d 条", len(records))
	}
}

// TestRecoveryUnverifiableBlocksResume 覆盖"无法核验时禁止自动推进"。
//
// 现场不可达（这里用不存在的远程路径模拟）时核验必须给出 unverifiable，
// 并禁止自动重放：记录保持原样，等待人工处理。
func TestRecoveryUnverifiableBlocksResume(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		testTaskID,
		testContainerID,
		testDockerID,
		testRemoteName,
		"",
		testTaskBranch,
		testBaseBranch,
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	if err := record.MarkCommit(
		domain.CommitStatusSucceeded,
		fixture.instance.rev(t, "HEAD"),
		"提交",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("推进提交状态失败: %v", err)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	// 远程不可达：核验必须保留不确定性而不是判为"没有推送"。
	fixture.executor.mu.Lock()
	fixture.executor.brokenRemote = true
	fixture.executor.mu.Unlock()
	decision, persisted := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("远程不可达时必须保留不确定性，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if !strings.Contains(decision.Summary, "无法核验") {
		t.Fatalf("判定说明必须指出无法核验，实际 %q", decision.Summary)
	}
	if decision.SafeToRetry {
		t.Fatal("无法核验时不得提示可以重试")
	}
	if !persisted {
		t.Fatal("无法核验的结论同样必须写回数据库")
	}
	final := fixture.recordByTask(t, testTaskID)
	if action := domain.DecideCodeSaveResume(final).Action; action != domain.CodeSaveResumeBlocked {
		t.Fatalf("无法核验的记录必须禁止自动推进，实际 %s", action)
	}
	// 禁止重放：推送命令没有被执行过。
	if pushes := fixture.executor.pushedCount(); pushes != 0 {
		t.Fatalf("核验不得执行推送，实际 %d 次", pushes)
	}
}

// TestRecoveryMergeBaseAdvancedByOthersStaysUnverifiable 覆盖"基础分支被他人推进"。
//
// 关键约束：基础分支变化不足以证明本次合并成功。当远程基础分支既不是记录的基线，
// 也不是唯一可推算的合并提交时，必须保留不确定性，并且不得触发任何自动合并。
func TestRecoveryMergeBaseAdvancedByOthersStaysUnverifiable(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			return record.MergeStatus == domain.MergeStatusSucceeded
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	saveRequest := request()
	saveRequest.MergeAfterPush = true
	if _, err := fixture.saver(recorder).Run(fixture.ctx, saveRequest); err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	stored := fixture.recordByTask(t, testTaskID)
	if stored.BaselineBaseCommitSHA == "" {
		t.Fatal("合并前必须落盘基础分支基线")
	}
	// 其他参与者把基础分支推进到一个与本次合并不相关的提交。
	fixture.instance.git(t, fixture.instance.work, "checkout", testBaseBranch)
	fixture.instance.writeFile(t, "internal/other.go", "package internal\n")
	fixture.instance.git(t, fixture.instance.work, "add", "--all")
	fixture.instance.git(t, fixture.instance.work, "commit", "-m", "他人推进基础分支")
	fixture.instance.git(
		t,
		fixture.instance.work,
		"push",
		fixture.instance.bare,
		"HEAD:refs/heads/"+testBaseBranch,
	)
	foreignBase := fixture.instance.bareRev(t, "refs/heads/"+testBaseBranch)
	if foreignBase == stored.BaselineBaseCommitSHA {
		t.Fatal("测试现场应让基础分支前进")
	}
	fixture.restart(t)
	decision, _ := fixture.verify(t)
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("无法归因的合并必须保留不确定性，实际 %s（%s）", decision.Status, decision.Summary)
	}
	if decision.SafeToRetry {
		t.Fatal("未决合并不得提示可以重试")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.MergeStatus == domain.MergeStatusSucceeded {
		t.Fatal("无法归因时不得把合并标记为成功")
	}
	if final.MergeSHA != "" {
		t.Fatalf("无法归因时不得写入合并提交，实际 %q", final.MergeSHA)
	}
	// 他人的基础分支提交没有被覆盖。
	if current := fixture.instance.bareRev(t, "refs/heads/"+testBaseBranch); current != foreignBase {
		t.Fatalf("不得覆盖他人的基础分支提交：期望 %s，实际 %s", foreignBase, current)
	}
}

// TestRecoveryUnconfirmedMarkerSurvivesWriteFailure 覆盖"记录写入失败后标记落盘"。
//
// 故障窗口：提交结果写入失败。此时必须把"记录不可信"这一事实单独落盘，使后续恢复
// 读到 unconfirmed 而不是看似可信的 pending；否则重试会直接产生重复提交。
func TestRecoveryUnconfirmedMarkerSurvivesWriteFailure(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	recorder := failingRecorder{
		store: fixture.db,
		failWhen: func(record domain.CodeSaveRecord) bool {
			// 只让"提交已成功"这一状态写入失败：其余写入（包括不可信标记）正常落盘。
			return record.CommitStatus == domain.CommitStatusSucceeded &&
				record.PersistStatus != domain.PersistStatusUnconfirmed
		},
	}
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	if _, err := fixture.saver(recorder).Run(fixture.ctx, request()); err == nil {
		t.Fatal("记录写入失败时必须返回错误")
	}
	stored := fixture.recordByTask(t, testTaskID)
	if !stored.PersistStatus.IsUnconfirmed() {
		t.Fatalf("写入失败后数据库必须记录 unconfirmed，实际 %q", stored.PersistStatus)
	}
	if stored.PersistError == "" {
		t.Fatal("写入失败摘要必须落盘")
	}
	// 不可信记录必须拒绝继续推进：数据库中的 pending 不能证明提交没有发生。
	if action := domain.DecideCodeSaveResume(stored).Action; action != domain.CodeSaveResumeVerify {
		t.Fatalf("记录不可信时必须要求先核验，实际 %s", action)
	}
	// 即使只读核验给出确定结论，也不得据此直接重试，必须先让记录收敛。
	observation := fixture.saver(storeRecorder{store: fixture.db}).Observe(
		fixture.ctx,
		request(),
		stored,
	)
	decision := domain.DecideCodeSaveRecovery(stored, observation)
	if decision.SafeToRetry {
		t.Fatal("记录不可信时核验结论不得提示可以重试")
	}
}

// TestRecoveryStagesConvergeProgressively 覆盖多阶段核验在同一记录上依次收敛。
//
// 这是最容易卡死恢复的窗口：提交确认已发生后，推送的核验结论必须能落库。若第二个
// 结论写不进去，数据库会永远停在 push_status=pending，恢复只能反复要求核验。
func TestRecoveryStagesConvergeProgressively(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryFixture(t)
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		testTaskID,
		testContainerID,
		testDockerID,
		testRemoteName,
		"",
		testTaskBranch,
		testBaseBranch,
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	headBefore := fixture.instance.rev(t, "HEAD")
	if err := record.MarkBaselineCommit(headBefore, time.Now().UTC()); err != nil {
		t.Fatalf("写入提交基线失败: %v", err)
	}
	if err := fixture.db.CreateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	// 真实产生一个提交，但记录里的提交状态保持 pending。
	fixture.instance.writeFile(t, "internal/feature.go", "package internal\n")
	fixture.instance.git(t, fixture.instance.work, "add", "--all")
	fixture.instance.git(t, fixture.instance.work, "commit", "-m", "真实提交")
	headAfter := fixture.instance.rev(t, "HEAD")
	// 第一次核验：确认提交已发生，阶段状态落库。
	first, persisted := fixture.verify(t)
	if first.Status != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("应确认提交已经发生，实际 %s（%s）", first.Status, first.Summary)
	}
	if !persisted {
		t.Fatal("核验结论必须成功写回数据库")
	}
	afterCommit := fixture.recordByTask(t, testTaskID)
	if afterCommit.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应已落库为 succeeded，实际 %s", afterCommit.CommitStatus)
	}
	if afterCommit.CommitSHA != headAfter {
		t.Fatalf("提交 SHA 应为 %s，实际 %s", headAfter, afterCommit.CommitSHA)
	}
	// 第二次核验：远程任务分支不存在，因此推送确实没有发生。
	second, secondPersisted, _ := fixture.verifyWith(t, nil)
	if second.Status != domain.RecoveryStatusConfirmedNotApplied {
		t.Fatalf("应确认推送没有发生，实际 %s（%s）", second.Status, second.Summary)
	}
	if !secondPersisted {
		t.Fatal("第二个阶段的核验结论必须能写回数据库")
	}
	final := fixture.recordByTask(t, testTaskID)
	if final.RecoveryStatus != domain.RecoveryStatusConfirmedNotApplied {
		t.Fatalf("恢复结论应更新为 confirmed_not_applied，实际 %s", final.RecoveryStatus)
	}
	// 后续阶段的结论不得回退已确认的提交事实。
	if final.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("不得回退已确认的提交，实际 %s", final.CommitStatus)
	}
	if final.CommitSHA != headAfter {
		t.Fatalf("不得清空提交 SHA，实际 %q", final.CommitSHA)
	}
	// 记录已收敛：不再有未决外部副作用，可以重新执行。
	if action := domain.DecideCodeSaveResume(final).Action; action != domain.CodeSaveResumeProceed {
		t.Fatalf("两个阶段都确认后应允许继续推进，实际 %s", action)
	}
}
