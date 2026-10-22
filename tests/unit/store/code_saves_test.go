// 代码保存记录的 SQLite 仓储测试。
//
// 这些用例直接验证仓储写入、状态字段约束与并发写入行为，不需要 Git、SSH 或
// Docker。
package sqlite_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// newCodeSaveRecord 构造一条待执行的代码保存记录。
func newCodeSaveRecord(t *testing.T, taskID string) domain.CodeSaveRecord {
	t.Helper()
	record, err := domain.NewCodeSaveRecord(
		domain.NewID(),
		taskID,
		"container-1",
		"docker-container-1",
		"origin",
		"https://git.example.com/group/repo.git",
		"agent/"+taskID,
		"main",
		false,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造代码保存记录失败: %v", err)
	}
	return record
}

// TestCodeSaveRecordLifecycle 校验代码保存记录的写入、更新与读取。
func TestCodeSaveRecordLifecycle(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeSaveRecord(t, "task-1")
	record.ProjectID = "project-1"
	if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	stored, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if stored.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("提交状态应为 pending，实际 %s", stored.CommitStatus)
	}
	if stored.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("未请求合并时合并状态应为 not_requested，实际 %s", stored.MergeStatus)
	}
	commitSHA := "1111111111111111111111111111111111111111"
	if err := stored.MarkCommit(
		domain.CommitStatusSucceeded,
		commitSHA,
		"AgentFlow: save task task-1",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	remoteSHA := "2222222222222222222222222222222222222222"
	if err := stored.MarkPush(domain.PushStatusSucceeded, remoteSHA, time.Now().UTC()); err != nil {
		t.Fatalf("记录推送结果失败: %v", err)
	}
	stored.Finish(time.Now().UTC())
	if err := db.UpdateCodeSaveRecord(ctx, *stored); err != nil {
		t.Fatalf("更新代码保存记录失败: %v", err)
	}
	reloaded, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("再次读取代码保存记录失败: %v", err)
	}
	if reloaded.CommitSHA != commitSHA {
		t.Fatalf("提交 SHA 应为 %s，实际 %s", commitSHA, reloaded.CommitSHA)
	}
	if reloaded.RemoteCommitSHA != remoteSHA {
		t.Fatalf("远程 commit 应为 %s，实际 %s", remoteSHA, reloaded.RemoteCommitSHA)
	}
	if reloaded.FinishedAt == nil {
		t.Fatal("结束时间应被持久化")
	}
	if reloaded.MergeStatus != domain.MergeStatusNotRequested {
		t.Fatalf("合并状态应为 not_requested，实际 %s", reloaded.MergeStatus)
	}
}

// TestCodeSaveRecordsAreIndependent 校验不同任务的记录互不覆盖。
func TestCodeSaveRecordsAreIndependent(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	first := newCodeSaveRecord(t, "task-1")
	second := newCodeSaveRecord(t, "task-2")
	for _, record := range []domain.CodeSaveRecord{first, second} {
		if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
			t.Fatalf("写入代码保存记录失败: %v", err)
		}
	}
	if err := first.MarkCommit(
		domain.CommitStatusSucceeded,
		"1111111111111111111111111111111111111111",
		"提交一",
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("记录提交结果失败: %v", err)
	}
	if err := db.UpdateCodeSaveRecord(ctx, first); err != nil {
		t.Fatalf("更新代码保存记录失败: %v", err)
	}
	other, err := db.GetCodeSaveRecord(ctx, second.ID)
	if err != nil {
		t.Fatalf("读取另一条记录失败: %v", err)
	}
	if other.CommitStatus != domain.CommitStatusPending {
		t.Fatalf("另一任务的提交状态不应被覆盖，实际 %s", other.CommitStatus)
	}
	records, err := db.ListCodeSaveRecordsByTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("按任务查询记录失败: %v", err)
	}
	if len(records) != 1 || records[0].ID != first.ID {
		t.Fatalf("按任务查询应只返回本任务记录，实际 %d 条", len(records))
	}
}

// TestCodeSaveRecordRequiresCommitSHA 校验数据库拒绝缺少 commit SHA 的成功记录。
func TestCodeSaveRecordRequiresCommitSHA(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeSaveRecord(t, "task-1")
	record.CommitStatus = domain.CommitStatusSucceeded
	err := db.CreateCodeSaveRecord(ctx, record)
	if err == nil {
		t.Fatal("缺少 commit SHA 的成功记录必须被拒绝")
	}
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("期望字段校验错误，实际 %v", err)
	}
}

// TestCodeSaveRecordRejectsMergeWithoutRequest 校验未请求合并时不能记录合并成功。
func TestCodeSaveRecordRejectsMergeWithoutRequest(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeSaveRecord(t, "task-1")
	if err := record.MarkMerge(
		domain.MergeStatusSucceeded,
		"3333333333333333333333333333333333333333",
		time.Now().UTC(),
	); err == nil {
		t.Fatal("未请求合并时不允许记录合并结果")
	}
	if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("未请求合并的记录应可写入: %v", err)
	}
}

// TestCodeSaveRecordNotFound 校验读取不存在的记录返回存储层未找到错误。
func TestCodeSaveRecordNotFound(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	if _, err := db.GetCodeSaveRecord(ctx, domain.NewID()); err == nil {
		t.Fatal("读取不存在的记录必须失败")
	} else if !isNotFound(err) {
		t.Fatalf("期望 storage.ErrNotFound，实际 %v", err)
	}
}

// TestCodeSaveRecordConcurrentWrites 校验并发写入不同记录不会互相阻塞或覆盖。
func TestCodeSaveRecordConcurrentWrites(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	const total = 6
	records := make([]domain.CodeSaveRecord, 0, total)
	for index := 0; index < total; index++ {
		records = append(records, newCodeSaveRecord(t, "task-"+itoa(index)))
	}
	var group sync.WaitGroup
	errs := make(chan error, total)
	for _, record := range records {
		group.Add(1)
		go func(item domain.CodeSaveRecord) {
			defer group.Done()
			if err := db.CreateCodeSaveRecord(ctx, item); err != nil {
				errs <- err
				return
			}
			loaded, err := db.GetCodeSaveRecord(ctx, item.ID)
			if err != nil {
				errs <- err
				return
			}
			if err := loaded.MarkCommit(
				domain.CommitStatusSucceeded,
				"1111111111111111111111111111111111111111",
				"并发提交",
				time.Now().UTC(),
			); err != nil {
				errs <- err
				return
			}
			errs <- db.UpdateCodeSaveRecord(ctx, *loaded)
		}(record)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发写入不应失败: %v", err)
		}
	}
	for _, record := range records {
		loaded, err := db.GetCodeSaveRecord(ctx, record.ID)
		if err != nil {
			t.Fatalf("读取记录失败: %v", err)
		}
		if loaded.CommitStatus != domain.CommitStatusSucceeded {
			t.Fatalf("每条记录都应独立更新，实际 %s", loaded.CommitStatus)
		}
	}
}

// isNotFound 判断错误是否为存储层未找到。
func isNotFound(err error) bool {
	return err != nil && containsError(err, storage.ErrNotFound)
}

// containsError 判断错误链中是否包含目标错误。
func containsError(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// itoa 把整数转换为十进制字符串。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestCodeSaveRecordRecoveryFields 校验恢复判定字段可以被完整读写。
//
// 这五个字段是"Git 已成功但记录没落盘"这一失败窗口的唯一判据，必须能独立
// 持久化：基线、落盘状态、落盘失败摘要与核验结论互不覆盖。
func TestCodeSaveRecordRecoveryFields(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeSaveRecord(t, "task-recovery")
	if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	baseline := "1111111111111111111111111111111111111111"
	baselineBase := "2222222222222222222222222222222222222222"
	if err := record.MarkBaselineCommit(baseline, time.Now().UTC()); err != nil {
		t.Fatalf("记录提交基线失败: %v", err)
	}
	if err := record.MarkBaselineBaseCommit(baselineBase, time.Now().UTC()); err != nil {
		t.Fatalf("记录合并基线失败: %v", err)
	}
	record.MarkPersistFailure("database is locked", time.Now().UTC())
	if err := db.UpdateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("更新代码保存记录失败: %v", err)
	}
	stored, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if stored.BaselineCommitSHA != baseline {
		t.Fatalf("提交基线丢失: %s", stored.BaselineCommitSHA)
	}
	if stored.BaselineBaseCommitSHA != baselineBase {
		t.Fatalf("合并基线丢失: %s", stored.BaselineBaseCommitSHA)
	}
	if stored.PersistStatus != domain.PersistStatusUnconfirmed {
		t.Fatalf("落盘状态应为 unconfirmed，实际 %s", stored.PersistStatus)
	}
	if stored.PersistError == "" {
		t.Fatal("落盘失败摘要必须被保存")
	}
	if !stored.RequiresManualRecovery() {
		t.Fatal("记录未可靠写入时必须要求人工核验")
	}
	// 落盘恢复成功后摘要必须清空，否则读者会误判记录仍然不可信。
	stored.MarkPersisted(time.Now().UTC())
	if err := db.UpdateCodeSaveRecord(ctx, *stored); err != nil {
		t.Fatalf("更新落盘状态失败: %v", err)
	}
	recovered, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("再次读取记录失败: %v", err)
	}
	if recovered.PersistStatus != domain.PersistStatusPersisted {
		t.Fatalf("落盘状态应为 persisted，实际 %s", recovered.PersistStatus)
	}
	if recovered.PersistError != "" {
		t.Fatalf("落盘成功后摘要必须清空，实际 %q", recovered.PersistError)
	}
}

// TestCodeSaveRecordRecoveryStatus 校验核验结论可以写入并被读回。
func TestCodeSaveRecordRecoveryStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	record := newCodeSaveRecord(t, "task-verify")
	if err := db.CreateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("写入代码保存记录失败: %v", err)
	}
	if err := record.MarkRecovery(domain.RecoveryStatusUnverifiable, time.Now().UTC()); err != nil {
		t.Fatalf("记录核验结论失败: %v", err)
	}
	if err := db.UpdateCodeSaveRecord(ctx, record); err != nil {
		t.Fatalf("更新代码保存记录失败: %v", err)
	}
	stored, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if stored.RecoveryStatus != domain.RecoveryStatusUnverifiable {
		t.Fatalf("恢复结论未原样保存: %s", stored.RecoveryStatus)
	}
	// 远端恢复可达后可以依据新事实收敛为确定结论。
	if err := stored.MarkRecovery(domain.RecoveryStatusConfirmedApplied, time.Now().UTC()); err != nil {
		t.Fatalf("修正核验结论失败: %v", err)
	}
	if err := db.UpdateCodeSaveRecord(ctx, *stored); err != nil {
		t.Fatalf("更新恢复结论失败: %v", err)
	}
	confirmed, err := db.GetCodeSaveRecord(ctx, record.ID)
	if err != nil {
		t.Fatalf("再次读取记录失败: %v", err)
	}
	if confirmed.RecoveryStatus != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("恢复结论未更新: %s", confirmed.RecoveryStatus)
	}
	// 已确认的事实不允许退回不确定：真实 Git 事实不会因一次查询失败而消失。
	if err := confirmed.MarkRecovery(domain.RecoveryStatusUnverifiable, time.Now().UTC()); err == nil {
		t.Fatal("已确认的恢复结论不应退回不确定状态")
	}
}

// TestCodeSaveRecordLegacyRowsRemainReadable 校验历史记录（新列为 NULL）仍可读取。
//
// 0019 之前写入的记录没有基线与落盘状态：空值必须按 persisted 处理，
// 不得被误判为"记录不可信"，也不得凭空生成基线。
func TestCodeSaveRecordLegacyRowsRemainReadable(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(ctx, `INSERT INTO code_saves
		(id, task_id, container_id, remote_name, task_branch, commit_status,
		 push_status, merge_enabled, merge_status, created_at, updated_at)
		VALUES ('legacy-save', 'task-legacy', 'container-1', 'origin',
		 'agent/task-legacy', 'pending', 'pending', 0, 'not_requested', 1, 1)`); err != nil {
		t.Fatalf("写入历史记录失败: %v", err)
	}
	loaded, err := db.GetCodeSaveRecord(ctx, "legacy-save")
	if err != nil {
		t.Fatalf("读取历史记录失败: %v", err)
	}
	if loaded.BaselineCommitSHA != "" || loaded.BaselineBaseCommitSHA != "" {
		t.Fatal("历史记录不应凭空产生基线")
	}
	if loaded.PersistStatus.IsUnconfirmed() {
		t.Fatal("空落盘状态必须按 persisted 处理")
	}
	if loaded.RequiresManualRecovery() {
		t.Fatal("历史记录的空落盘状态不应触发人工核验")
	}
	// 没有基线的 pending 记录在恢复判定中必须转人工，而不是被推断为未发生。
	decision := domain.DecideCodeSaveRecovery(*loaded, domain.CodeSaveObservation{
		HeadCommit: "1111111111111111111111111111111111111111",
	})
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("缺少基线的历史记录不得被推断，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("缺少基线的历史记录不得提示可以安全重试")
	}
}

// TestCodeSaveRecordAcceptsUnknownRecoveryValues 校验未知恢复状态可以写入并原样读回。
//
// 数据库不声明枚举白名单：新增取值必须能在不修改 Schema 的情况下写入。
func TestCodeSaveRecordAcceptsUnknownRecoveryValues(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := testContext(t)
	raw := rawDB(t, db)
	if _, err := raw.ExecContext(ctx, `INSERT INTO code_saves
		(id, task_id, container_id, remote_name, task_branch, commit_status,
		 push_status, merge_enabled, merge_status, persist_status, recovery_status,
		 created_at, updated_at)
		VALUES ('probe-save', 'task-probe', 'container-1', 'origin',
		 'agent/task-probe', 'pending', 'pending', 0, 'not_requested',
		 'awaiting_review', 'mirror_confirmed', 1, 1)`); err != nil {
		t.Fatalf("写入未知恢复状态失败: %v", err)
	}
	loaded, err := db.GetCodeSaveRecord(ctx, "probe-save")
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if loaded.PersistStatus != domain.PersistStatus("awaiting_review") {
		t.Fatalf("未知落盘状态未原样保存: %s", loaded.PersistStatus)
	}
	if loaded.RecoveryStatus != domain.RecoveryStatus("mirror_confirmed") {
		t.Fatalf("未知恢复结论未原样保存: %s", loaded.RecoveryStatus)
	}
}
