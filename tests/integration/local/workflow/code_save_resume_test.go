// 代码保存核验与续跑资格的集成测试。
//
// 这些用例覆盖此前两个真实缺口：
//  1. 容器被删除或脱离管理时，核验直接返回错误、结论不落盘，记录永远停在
//     pending，恢复只能反复要求核验同一现场；
//  2. 续跑被拒绝时只给出错误分类，不告诉用户"哪条记录、下一步该做什么"。
//
// 用例使用真实 SQLite 与正式领域判定；远程 Git 与容器由受控替身提供。
package workflow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// newVerifyContainerService 构造只依赖数据库的容器服务。
//
// 本文件的两个核验用例都在"远程会话之前"返回结论，因此不需要可用的 SSH 连接；
// 但容器服务仍然要求一个 SSH 服务实例才能构造，这里给出一个不建立连接的实例。
func newVerifyContainerService(
	t *testing.T,
	fixture *recoveryGateFixture,
) *app.ContainerService {
	t.Helper()
	sshService, err := app.NewSSHService(fixture.db, fixture.logger, app.SSHOptions{})
	if err != nil {
		t.Fatalf("构造 SSH 服务失败: %v", err)
	}
	service, err := app.NewContainerService(app.DockerServiceOptions{
		SSH:    sshService,
		Store:  fixture.db,
		Logger: fixture.logger,
	})
	if err != nil {
		t.Fatalf("构造容器服务失败: %v", err)
	}
	return service
}

// TestDecideCodeSaveResumeForUnverifiableBlocks 验证已核验但无法确认时阻止推进。
//
// 该用例固定的是领域判定：unverifiable 不是"可以重试"，也不是"没有发生"。
func TestDecideCodeSaveResumeForUnverifiableBlocks(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, func(record *domain.CodeSaveRecord) {
		record.PushStatus = domain.PushStatusUnknown
		record.RecoveryStatus = domain.RecoveryStatusUnverifiable
	})
	decision := domain.DecideTaskCodeSaveResume(fixture.codeSaveRecords(t))
	if decision.Action != domain.CodeSaveResumeBlocked {
		t.Fatalf("unverifiable 必须阻止推进，实际 %s", decision.Action)
	}
	if decision.ErrorClass != "code_save_indeterminate" {
		t.Fatalf("错误分类不正确: %s", decision.ErrorClass)
	}
	// 记录本身不得被判定过程改写。
	records := fixture.codeSaveRecords(t)
	if records[0].RecoveryStatus != domain.RecoveryStatusUnverifiable {
		t.Fatalf("判定不得改写恢复结论，实际 %s", records[0].RecoveryStatus)
	}
}

// TestResumeEligibilityReportsRecordIDs 验证续跑资格给出可直接执行的下一步。
func TestResumeEligibilityReportsRecordIDs(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, func(record *domain.CodeSaveRecord) {
		record.PushStatus = domain.PushStatusPending
	})
	service, err := app.NewResumeEligibilityService(app.ResumeEligibilityOptions{
		Store: fixture.db,
	})
	if err != nil {
		t.Fatalf("构造续跑资格服务失败: %v", err)
	}
	result, err := service.Get(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询续跑资格失败: %v", err)
	}
	if result.CanResume {
		t.Fatal("存在未决代码保存记录时不得报告可以继续")
	}
	if len(result.CodeSaveRecords) != 1 {
		t.Fatalf("必须列出需要处理的记录，实际 %d 条", len(result.CodeSaveRecords))
	}
	// 用例必须给出可直接执行的命令，否则用户仍然无从下手。
	want := "--record " + result.CodeSaveRecords[0].ID.String()
	found := false
	for _, action := range result.NextActions {
		if strings.Contains(action, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("下一步操作必须包含具体记录标识 %s，实际 %v", want, result.NextActions)
	}
	if !strings.Contains(result.Summary, "阻塞项") {
		t.Fatalf("说明必须指出存在阻塞项: %s", result.Summary)
	}
}

// TestResumeEligibilityProceedsAfterConverged 验证已收敛记录允许继续。
func TestResumeEligibilityProceedsAfterConverged(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, func(record *domain.CodeSaveRecord) {
		record.PushStatus = domain.PushStatusSucceeded
		record.RemoteCommitSHA = record.CommitSHA
	})
	service, err := app.NewResumeEligibilityService(app.ResumeEligibilityOptions{
		Store: fixture.db,
	})
	if err != nil {
		t.Fatalf("构造续跑资格服务失败: %v", err)
	}
	result, err := service.Get(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询续跑资格失败: %v", err)
	}
	if !result.CanResume {
		t.Fatalf("已收敛记录不应阻塞推进，实际阻塞项 %v", result.Blocking)
	}
	if len(result.CodeSaveRecords) != 0 {
		t.Fatalf("已收敛记录不应出现在待处理列表，实际 %d 条", len(result.CodeSaveRecords))
	}
}

// TestResumeEligibilityReportsUnreachableContainer 验证容器不可用时给出阻塞分类。
func TestResumeEligibilityReportsUnreachableContainer(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, nil)
	service, err := app.NewResumeEligibilityService(app.ResumeEligibilityOptions{
		Store:      fixture.db,
		Containers: &fakeContainer{running: false},
	})
	if err != nil {
		t.Fatalf("构造续跑资格服务失败: %v", err)
	}
	result, err := service.Get(fixture.ctx, fixture.task.ID.String())
	if err != nil {
		t.Fatalf("查询续跑资格失败: %v", err)
	}
	if result.CanResume {
		t.Fatal("容器未运行时不得报告可以继续")
	}
	if !containsBlocker(result.Blocking, "container_not_running") {
		t.Fatalf("阻塞分类必须指出容器不可运行，实际 %v", result.Blocking)
	}
}

// TestVerifyCodeSaveDegradesWhenContainerMissing 验证容器缺失时降级为 unverifiable。
func TestVerifyCodeSaveDegradesWhenContainerMissing(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, func(record *domain.CodeSaveRecord) {
		record.PushStatus = domain.PushStatusPending
	})
	// 使记录指向一个不存在的容器登记记录：核验无法开始，但结论必须落盘。
	records := fixture.codeSaveRecords(t)
	record := records[0]
	record.ContainerID = "container-deregistered"
	if err := fixture.db.UpdateCodeSaveRecord(fixture.ctx, record); err != nil {
		t.Fatalf("更新代码保存记录失败: %v", err)
	}
	service := newVerifyContainerService(t, fixture)
	result, verifyErr := service.VerifyCodeSave(fixture.ctx, app.CodeSaveVerifyRequest{
		RecordID: record.ID.String(),
	})
	if verifyErr != nil {
		t.Fatalf("容器登记记录缺失时应降级为 unverifiable 而不是返回错误: %v", verifyErr)
	}
	if result.Decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("结论应为 unverifiable，实际 %s", result.Decision.Status)
	}
	if !result.Persisted {
		t.Fatal("降级结论必须落盘，否则恢复仍然无出路")
	}
	updated, err := fixture.db.GetCodeSaveRecord(fixture.ctx, record.ID)
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	if updated.RecoveryStatus != domain.RecoveryStatusUnverifiable {
		t.Fatalf("数据库中的恢复结论应为 unverifiable，实际 %s", updated.RecoveryStatus)
	}
	// 降级不得把 pending 伪造成成功。
	if updated.PushStatus == domain.PushStatusSucceeded {
		t.Fatal("无法核验时不得把推送状态改成成功")
	}
}

// TestVerifyCodeSaveRejectsUnknownRecord 验证记录不存在时仍返回参数错误。
func TestVerifyCodeSaveRejectsUnknownRecord(t *testing.T) {
	t.Parallel()
	fixture := newRecoveryGateFixture(t, nil)
	service := newVerifyContainerService(t, fixture)
	_, verifyErr := service.VerifyCodeSave(fixture.ctx, app.CodeSaveVerifyRequest{
		RecordID: domain.NewID().String(),
	})
	if verifyErr == nil {
		t.Fatal("记录不存在时必须返回错误，而不是伪造结论")
	}
	if !errors.Is(verifyErr, storage.ErrNotFound) && !strings.Contains(verifyErr.Error(), "不存在") {
		t.Fatalf("错误应说明记录不存在: %v", verifyErr)
	}
}

// codeSaveRecords 读取任务的代码保存记录。
func (f *recoveryGateFixture) codeSaveRecords(t *testing.T) []domain.CodeSaveRecord {
	t.Helper()
	records, err := f.db.ListCodeSaveRecordsByTask(f.ctx, f.task.ID.String())
	if err != nil {
		t.Fatalf("读取代码保存记录失败: %v", err)
	}
	return records
}

// containsBlocker 判断阻塞分类列表中是否包含指定项。
func containsBlocker(blocking []string, want string) bool {
	for _, blocker := range blocking {
		if blocker == want {
			return true
		}
	}
	return false
}
