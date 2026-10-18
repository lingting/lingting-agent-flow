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
