// 代码保存恢复判定的单元测试。
//
// 用例只覆盖 domain.DecideCodeSaveRecovery 这一个纯函数：它不读数据库、不执行
// Git 命令，因此每个"失败窗口"都可以被完整枚举。判定表是本轮恢复语义的唯一
// 依据，任何调整都必须同时修改这里。
package domain_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/domain"
)

// 判定用例使用的固定 commit SHA。
const (
	recoveryBaseline      = "1111111111111111111111111111111111111111"
	recoveryHead          = "2222222222222222222222222222222222222222"
	recoveryOtherCommit   = "3333333333333333333333333333333333333333"
	recoveryMergeCommit   = "4444444444444444444444444444444444444444"
	recoveryBaselineBase  = "5555555555555555555555555555555555555555"
	recoveryDivergedBase  = "6666666666666666666666666666666666666666"
	recoveryUnrelatedBase = "7777777777777777777777777777777777777777"
)

// pendingCommitRecord 构造"提交成功但状态没落盘"窗口的记录。
func pendingCommitRecord() domain.CodeSaveRecord {
	return domain.CodeSaveRecord{
		TaskID:            "task-1",
		TaskBranch:        "agent/task-1",
		CommitStatus:      domain.CommitStatusPending,
		PushStatus:        domain.PushStatusPending,
		MergeStatus:       domain.MergeStatusNotRequested,
		BaselineCommitSHA: recoveryBaseline,
		PersistStatus:     domain.PersistStatusPersisted,
	}
}

// TestDecideCodeSaveRecovery 覆盖恢复判定的全部组合。
func TestDecideCodeSaveRecovery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		record         domain.CodeSaveRecord
		observation    domain.CodeSaveObservation
		expectedStatus domain.RecoveryStatus
		expectedSHA    string
		expectedRetry  bool
	}{
		// 提交窗口：HEAD 已前进说明提交确实发生，只是记录没写进去。
		{
			name:           "提交已发生但记录仍是 pending",
			record:         pendingCommitRecord(),
			observation:    domain.CodeSaveObservation{HeadCommit: recoveryHead},
			expectedStatus: domain.RecoveryStatusConfirmedApplied,
			expectedSHA:    recoveryHead,
		},
		// 提交窗口：HEAD 未变化说明提交确实没有发生，可以安全重试。
		{
			name:           "提交未发生且 HEAD 未变化",
			record:         pendingCommitRecord(),
			observation:    domain.CodeSaveObservation{HeadCommit: recoveryBaseline},
			expectedStatus: domain.RecoveryStatusConfirmedNotApplied,
			expectedRetry:  true,
		},
		// 提交窗口：没有基线时不得推断，历史记录一律转人工核验。
		{
			name: "历史记录缺少提交基线",
			record: domain.CodeSaveRecord{
				TaskID:       "task-1",
				TaskBranch:   "agent/task-1",
				CommitStatus: domain.CommitStatusPending,
				PushStatus:   domain.PushStatusPending,
				MergeStatus:  domain.MergeStatusNotRequested,
			},
			observation:    domain.CodeSaveObservation{HeadCommit: recoveryHead},
			expectedStatus: domain.RecoveryStatusUnverifiable,
		},
		// 提交窗口：读不到 HEAD 时同样不能推断。
		{
			name:           "提交窗口无法读取 HEAD",
			record:         pendingCommitRecord(),
			observation:    domain.CodeSaveObservation{HeadCommit: ""},
			expectedStatus: domain.RecoveryStatusUnverifiable,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideCodeSaveRecovery(testCase.record, testCase.observation)
			if decision.Status != testCase.expectedStatus {
				t.Fatalf(
					"期望结论 %s，实际 %s（%s）",
					testCase.expectedStatus,
					decision.Status,
					decision.Summary,
				)
			}
			if decision.CommitSHA != testCase.expectedSHA {
				t.Fatalf("期望提交 %q，实际 %q", testCase.expectedSHA, decision.CommitSHA)
			}
			if decision.SafeToRetry != testCase.expectedRetry {
				t.Fatalf("可重试判定不符：期望 %v", testCase.expectedRetry)
			}
		})
	}
}

// TestDecideCodeSaveRecoveryForPush 覆盖推送窗口的判定。
//
// 推送窗口只用远程引用说话：远端指向预期提交即确认已推送，指向其他提交按冲突
// 处理并禁止自动重试。
func TestDecideCodeSaveRecoveryForPush(t *testing.T) {
	t.Parallel()
	base := func() domain.CodeSaveRecord {
		return domain.CodeSaveRecord{
			TaskID:       "task-1",
			TaskBranch:   "agent/task-1",
			CommitStatus: domain.CommitStatusSucceeded,
			CommitSHA:    recoveryHead,
			PushStatus:   domain.PushStatusPending,
			MergeStatus:  domain.MergeStatusNotRequested,
		}
	}
	cases := []struct {
		name           string
		observation    domain.CodeSaveObservation
		expectedStatus domain.RecoveryStatus
		expectedRetry  bool
	}{
		{
			name:           "远程已包含预期提交",
			observation:    domain.CodeSaveObservation{RemoteTaskCommit: recoveryHead},
			expectedStatus: domain.RecoveryStatusConfirmedApplied,
		},
		{
			name:           "远程任务分支不存在",
			observation:    domain.CodeSaveObservation{RemoteTaskCommit: ""},
			expectedStatus: domain.RecoveryStatusConfirmedNotApplied,
			expectedRetry:  true,
		},
		{
			name:           "远程指向其他提交",
			observation:    domain.CodeSaveObservation{RemoteTaskCommit: recoveryOtherCommit},
			expectedStatus: domain.RecoveryStatusUnverifiable,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideCodeSaveRecovery(base(), testCase.observation)
			if decision.Status != testCase.expectedStatus {
				t.Fatalf("期望结论 %s，实际 %s", testCase.expectedStatus, decision.Status)
			}
			if decision.SafeToRetry != testCase.expectedRetry {
				t.Fatalf("可重试判定不符：期望 %v", testCase.expectedRetry)
			}
		})
	}
}

// TestDecideCodeSaveRecoveryForMerge 覆盖合并窗口的判定。
//
// 关键约束：仅仅发现基础分支发生变化，不足以证明合并由本次操作产生。
func TestDecideCodeSaveRecoveryForMerge(t *testing.T) {
	t.Parallel()
	base := func() domain.CodeSaveRecord {
		return domain.CodeSaveRecord{
			TaskID:                "task-1",
			TaskBranch:            "agent/task-1",
			BaseBranch:            "main",
			CommitStatus:          domain.CommitStatusSucceeded,
			CommitSHA:             recoveryHead,
			PushStatus:            domain.PushStatusSucceeded,
			RemoteCommitSHA:       recoveryHead,
			MergeEnabled:          true,
			MergeStatus:           domain.MergeStatusPending,
			BaselineBaseCommitSHA: recoveryBaselineBase,
		}
	}
	cases := []struct {
		name           string
		observation    domain.CodeSaveObservation
		expectedStatus domain.RecoveryStatus
		expectedMerge  string
		expectedRetry  bool
	}{
		{
			name: "基础分支未变化",
			observation: domain.CodeSaveObservation{
				RemoteBaseCommit:      recoveryBaselineBase,
				MergeCommitResolvable: true,
			},
			expectedStatus: domain.RecoveryStatusConfirmedNotApplied,
			expectedRetry:  true,
		},
		{
			name: "基础分支指向预期合并提交",
			observation: domain.CodeSaveObservation{
				RemoteBaseCommit:      recoveryMergeCommit,
				ExpectedMergeCommit:   recoveryMergeCommit,
				MergeCommitResolvable: true,
			},
			expectedStatus: domain.RecoveryStatusConfirmedApplied,
			expectedMerge:  recoveryMergeCommit,
		},
		{
			// 基础分支确实变了，但无法证明是本次合并造成的。
			name: "基础分支变化但无法推算合并提交",
			observation: domain.CodeSaveObservation{
				RemoteBaseCommit:      recoveryDivergedBase,
				MergeCommitResolvable: false,
			},
			expectedStatus: domain.RecoveryStatusUnverifiable,
		},
		{
			name: "基础分支指向与预期合并提交不一致",
			observation: domain.CodeSaveObservation{
				RemoteBaseCommit:      recoveryUnrelatedBase,
				ExpectedMergeCommit:   recoveryMergeCommit,
				MergeCommitResolvable: true,
			},
			expectedStatus: domain.RecoveryStatusUnverifiable,
		},
		{
			name: "缺少合并基线",
			observation: domain.CodeSaveObservation{
				RemoteBaseCommit:      recoveryMergeCommit,
				ExpectedMergeCommit:   recoveryMergeCommit,
				MergeCommitResolvable: true,
			},
			expectedStatus: domain.RecoveryStatusUnverifiable,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := base()
			if testCase.name == "缺少合并基线" {
				record.BaselineBaseCommitSHA = ""
			}
			decision := domain.DecideCodeSaveRecovery(record, testCase.observation)
			if decision.Status != testCase.expectedStatus {
				t.Fatalf("期望结论 %s，实际 %s（%s）", testCase.expectedStatus, decision.Status, decision.Summary)
			}
			if decision.MergeSHA != testCase.expectedMerge {
				t.Fatalf("期望合并提交 %q，实际 %q", testCase.expectedMerge, decision.MergeSHA)
			}
			if decision.SafeToRetry != testCase.expectedRetry {
				t.Fatalf("可重试判定不符：期望 %v", testCase.expectedRetry)
			}
		})
	}
}

// TestDecideCodeSaveRecoveryPrioritizesUnconfirmedRecord 校验记录不可信时先核验。
//
// 记录写入未确认时，即使现场看起来一切正常也不能判定；反过来，现场看起来"没发生"
// 也不能据此重试——记录本身可能是落后的。
func TestDecideCodeSaveRecoveryPrioritizesUnconfirmedRecord(t *testing.T) {
	t.Parallel()
	record := pendingCommitRecord()
	record.PersistStatus = domain.PersistStatusUnconfirmed
	decision := domain.DecideCodeSaveRecovery(record, domain.CodeSaveObservation{
		HeadCommit: recoveryBaseline,
	})
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("记录不可信时必须转人工核验，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("记录不可信时不得判定可以安全重试")
	}
}

// TestDecideCodeSaveRecoveryWithoutObservation 校验核验失败时保留不确定性。
func TestDecideCodeSaveRecoveryWithoutObservation(t *testing.T) {
	t.Parallel()
	record := pendingCommitRecord()
	decision := domain.DecideCodeSaveRecovery(record, domain.CodeSaveObservation{
		ReadFailed: true,
		ReadError:  "容器不可访问",
	})
	if decision.Status != domain.RecoveryStatusUnverifiable {
		t.Fatalf("核验失败时必须保留不确定性，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("核验失败时不得判定可以安全重试")
	}
	if !containsText(decision.Summary, "容器不可访问") {
		t.Fatalf("判定说明应包含核验失败原因，实际 %q", decision.Summary)
	}
}

// TestDecideCodeSaveRecoveryWithoutPendingStage 校验没有待确认阶段时不判成功。
func TestDecideCodeSaveRecoveryWithoutPendingStage(t *testing.T) {
	t.Parallel()
	record := domain.CodeSaveRecord{
		TaskID:       "task-1",
		TaskBranch:   "agent/task-1",
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    recoveryHead,
		PushStatus:   domain.PushStatusSucceeded,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	decision := domain.DecideCodeSaveRecovery(record, domain.CodeSaveObservation{})
	if decision.Status != domain.RecoveryStatusUnverified {
		t.Fatalf("没有待确认阶段时应保持未核验，实际 %s", decision.Status)
	}
	if decision.SafeToRetry {
		t.Fatal("没有待确认阶段时不应提示重试")
	}
}

// containsText 判断文本是否包含子串。
func containsText(value, fragment string) bool {
	return strings.Contains(value, fragment)
}
