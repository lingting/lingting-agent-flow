// 代码保存恢复续跑资格与恢复写回的单元测试。
//
// 这些用例只覆盖纯函数：DecideCodeSaveResume、DecideTaskCodeSaveResume 与
// CodeSaveRecord.ApplyRecovery。它们不读数据库、不执行 Git 命令，因此可以把
// "已发生 / 确定未发生 / 无法确认" 与 "是否允许重试" 的组合完整枚举。
package domain_test

import (
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// resumeRecord 构造一条"提交成功、推送 pending"的记录，作为续跑判定的起点。
func resumeRecord() domain.CodeSaveRecord {
	return domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       "task-1",
		TaskBranch:   "agent/task-1",
		ContainerID:  "container-1",
		RemoteName:   "origin",
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    recoveryHead,
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
}

// TestDecideCodeSaveResume 覆盖单个记录的续跑资格判定。
func TestDecideCodeSaveResume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		mutate         func(*domain.CodeSaveRecord)
		expectedAction domain.CodeSaveResumeAction
		expectedClass  string
	}{
		{
			name:           "没有未决副作用时允许继续",
			mutate:         func(r *domain.CodeSaveRecord) { r.PushStatus = domain.PushStatusSucceeded },
			expectedAction: domain.CodeSaveResumeProceed,
		},
		{
			// 数据库中的 pending 不能证明推送没有发生。
			name:           "推送仍为 pending 时要求先核验",
			mutate:         func(r *domain.CodeSaveRecord) {},
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_indeterminate",
		},
		{
			name:           "推送结果未知时要求先核验",
			mutate:         func(r *domain.CodeSaveRecord) { r.PushStatus = domain.PushStatusUnknown },
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_indeterminate",
		},
		{
			name:           "推送冲突时要求先核验",
			mutate:         func(r *domain.CodeSaveRecord) { r.PushStatus = domain.PushStatusConflict },
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_indeterminate",
		},
		{
			// 记录可能落后于真实 Git 状态：即使看起来没有未决副作用也必须先核验。
			name: "记录写入未确认时要求先核验",
			mutate: func(r *domain.CodeSaveRecord) {
				r.PushStatus = domain.PushStatusSucceeded
				r.PersistStatus = domain.PersistStatusUnconfirmed
			},
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_unconfirmed",
		},
		{
			// 已核验但拿不到可信事实：禁止自动重放，保留现场等待人工处理。
			name: "已核验为无法确认时禁止自动重放",
			mutate: func(r *domain.CodeSaveRecord) {
				r.RecoveryStatus = domain.RecoveryStatusUnverifiable
			},
			expectedAction: domain.CodeSaveResumeBlocked,
			expectedClass:  "code_save_indeterminate",
		},
		{
			// 核验确认没有发生：允许继续，但真正的副作用仍由代码保存组件重新核验前置条件。
			name: "已核验确认未发生时允许继续",
			mutate: func(r *domain.CodeSaveRecord) {
				r.RecoveryStatus = domain.RecoveryStatusConfirmedNotApplied
			},
			expectedAction: domain.CodeSaveResumeProceed,
		},
		{
			name: "已核验确认已发生时没有未决副作用",
			mutate: func(r *domain.CodeSaveRecord) {
				r.PushStatus = domain.PushStatusSucceeded
				r.RecoveryStatus = domain.RecoveryStatusConfirmedApplied
			},
			expectedAction: domain.CodeSaveResumeProceed,
		},
		{
			name: "提交结果未知时要求先核验",
			mutate: func(r *domain.CodeSaveRecord) {
				r.CommitStatus = domain.CommitStatusUnknown
				r.PushStatus = domain.PushStatusNotAttempted
			},
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_indeterminate",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := resumeRecord()
			testCase.mutate(&record)
			decision := domain.DecideCodeSaveResume(record)
			if decision.Action != testCase.expectedAction {
				t.Fatalf("期望动作 %s，实际 %s（%s）",
					testCase.expectedAction, decision.Action, decision.Summary)
			}
			if decision.ErrorClass != testCase.expectedClass {
				t.Fatalf("期望分类 %q，实际 %q", testCase.expectedClass, decision.ErrorClass)
			}
			if decision.Summary == "" {
				t.Fatal("判定必须给出可读说明")
			}
		})
	}
}

// TestDecideTaskCodeSaveResume 覆盖按任务全部记录判定的续跑资格。
func TestDecideTaskCodeSaveResume(t *testing.T) {
	t.Parallel()
	verified := func() domain.CodeSaveRecord {
		record := resumeRecord()
		record.RecoveryStatus = domain.RecoveryStatusConfirmedNotApplied
		return record
	}
	unverifiable := func() domain.CodeSaveRecord {
		record := resumeRecord()
		record.RecoveryStatus = domain.RecoveryStatusUnverifiable
		return record
	}
	unconfirmed := func() domain.CodeSaveRecord {
		record := resumeRecord()
		record.PushStatus = domain.PushStatusSucceeded
		record.PersistStatus = domain.PersistStatusUnconfirmed
		return record
	}
	clean := func() domain.CodeSaveRecord {
		record := resumeRecord()
		record.PushStatus = domain.PushStatusSucceeded
		return record
	}
	cases := []struct {
		name           string
		records        []domain.CodeSaveRecord
		expectedAction domain.CodeSaveResumeAction
		expectedClass  string
	}{
		{
			name:           "没有任何记录时允许继续",
			records:        nil,
			expectedAction: domain.CodeSaveResumeProceed,
		},
		{
			name:           "全部记录都已收敛时允许继续",
			records:        []domain.CodeSaveRecord{clean(), verified()},
			expectedAction: domain.CodeSaveResumeProceed,
		},
		{
			name:           "仍有未核验的未决副作用时要求先核验",
			records:        []domain.CodeSaveRecord{clean(), verified(), resumeRecord()},
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_indeterminate",
		},
		{
			name:           "任一记录无法确认时禁止自动重放",
			records:        []domain.CodeSaveRecord{clean(), unverifiable(), resumeRecord()},
			expectedAction: domain.CodeSaveResumeBlocked,
			expectedClass:  "code_save_indeterminate",
		},
		{
			// 写入未确认比"无法确认"更保守：记录本身不可信，连核验结论都可能是旧的。
			name:           "写入未确认优先于其他结论",
			records:        []domain.CodeSaveRecord{unverifiable(), unconfirmed()},
			expectedAction: domain.CodeSaveResumeVerify,
			expectedClass:  "code_save_unconfirmed",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := domain.DecideTaskCodeSaveResume(testCase.records)
			if decision.Action != testCase.expectedAction {
				t.Fatalf("期望动作 %s，实际 %s（%s）",
					testCase.expectedAction, decision.Action, decision.Summary)
			}
			if decision.ErrorClass != testCase.expectedClass {
				t.Fatalf("期望分类 %q，实际 %q", testCase.expectedClass, decision.ErrorClass)
			}
		})
	}
}

// TestApplyRecoveryWritesConfirmedStage 校验确认发生的事实会被写回记录。
//
// 只写恢复结论是不够的：数据库必须同时前进阶段状态，否则下一次恢复仍然看到
// pending，会把已经发生的外部副作用当成"尚未确认"反复核验。
func TestApplyRecoveryWritesConfirmedStage(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	decision := domain.CodeSaveRecoveryDecision{
		Status:    domain.RecoveryStatusConfirmedApplied,
		Stage:     "commit",
		CommitSHA: recoveryHead,
		Summary:   "提交已经发生",
	}
	record := domain.CodeSaveRecord{
		ID:                domain.NewID(),
		TaskID:            "task-1",
		TaskBranch:        "agent/task-1",
		ContainerID:       "container-1",
		RemoteName:        "origin",
		CommitStatus:      domain.CommitStatusPending,
		PushStatus:        domain.PushStatusPending,
		MergeStatus:       domain.MergeStatusNotRequested,
		BaselineCommitSHA: recoveryBaseline,
	}
	updated, err := record.ApplyRecovery(decision, at)
	if err != nil {
		t.Fatalf("写回恢复结论失败: %v", err)
	}
	if updated.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("确认提交已发生时必须推进提交状态，实际 %s", updated.CommitStatus)
	}
	if updated.CommitSHA != recoveryHead {
		t.Fatalf("确认提交已发生时必须写入提交 SHA，实际 %q", updated.CommitSHA)
	}
	if updated.RecoveryStatus != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("恢复结论未写回: %s", updated.RecoveryStatus)
	}
	// 推送阶段不受提交阶段影响：它仍然是 pending，需要单独核验。
	if updated.PushStatus != domain.PushStatusPending {
		t.Fatalf("写回提交结论不得推进推送状态，实际 %s", updated.PushStatus)
	}
	// 原记录不被修改：判定与写回都必须作用在副本上。
	if record.CommitStatus != domain.CommitStatusPending {
		t.Fatal("写回不得修改原记录")
	}
}

// TestApplyRecoveryKeepsPendingForUnconfirmed 校验不确定结论不推进阶段状态。
func TestApplyRecoveryKeepsPendingForUnconfirmed(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	for _, status := range []domain.RecoveryStatus{
		domain.RecoveryStatusUnverifiable,
		domain.RecoveryStatusConfirmedNotApplied,
	} {
		record := domain.CodeSaveRecord{
			ID:                domain.NewID(),
			TaskID:            "task-1",
			TaskBranch:        "agent/task-1",
			ContainerID:       "container-1",
			RemoteName:        "origin",
			CommitStatus:      domain.CommitStatusPending,
			PushStatus:        domain.PushStatusPending,
			MergeStatus:       domain.MergeStatusNotRequested,
			BaselineCommitSHA: recoveryBaseline,
		}
		updated, err := record.ApplyRecovery(domain.CodeSaveRecoveryDecision{
			Status:  status,
			Stage:   "commit",
			Summary: "判定说明",
		}, at)
		if err != nil {
			t.Fatalf("%s 写回失败: %v", status, err)
		}
		if updated.CommitStatus != domain.CommitStatusPending {
			t.Fatalf("%s 不得推进提交状态，实际 %s", status, updated.CommitStatus)
		}
		if updated.CommitSHA != "" {
			t.Fatalf("%s 不得臆造提交 SHA，实际 %q", status, updated.CommitSHA)
		}
		if updated.RecoveryStatus != status {
			t.Fatalf("恢复结论未写回: %s", updated.RecoveryStatus)
		}
	}
}

// TestApplyRecoveryIsIdempotent 校验同一结论重复写回不会失败。
func TestApplyRecoveryIsIdempotent(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	decision := domain.CodeSaveRecoveryDecision{
		Status:  domain.RecoveryStatusConfirmedApplied,
		Stage:   "push",
		Summary: "推送已经发生",
	}
	record := domain.CodeSaveRecord{
		ID:           domain.NewID(),
		TaskID:       "task-1",
		TaskBranch:   "agent/task-1",
		ContainerID:  "container-1",
		RemoteName:   "origin",
		CommitStatus: domain.CommitStatusSucceeded,
		CommitSHA:    recoveryHead,
		PushStatus:   domain.PushStatusPending,
		MergeStatus:  domain.MergeStatusNotRequested,
	}
	first, err := record.ApplyRecovery(decision, at)
	if err != nil {
		t.Fatalf("首次写回失败: %v", err)
	}
	if first.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("确认推送已发生时必须推进推送状态，实际 %s", first.PushStatus)
	}
	second, err := first.ApplyRecovery(decision, at.Add(time.Second))
	if err != nil {
		t.Fatalf("重复写回同一结论不应失败: %v", err)
	}
	if second.PushStatus != domain.PushStatusSucceeded {
		t.Fatalf("重复写回不得回退状态，实际 %s", second.PushStatus)
	}
	if second.RecoveryStatus != domain.RecoveryStatusConfirmedApplied {
		t.Fatalf("重复写回不得改写恢复结论，实际 %s", second.RecoveryStatus)
	}
}

// TestApplyRecoveryAcrossStages 校验同一记录上多个阶段的核验结论可以依次收敛。
//
// 一条记录里提交、推送、合并是依次推进的三个阶段，但恢复结论只有一个字段。核验
// 总是针对"第一个仍待确认的阶段"，因此同一字段会被不同阶段依次占用：提交确认已
// 发生后，下一次核验判定的是推送，其结果可能是 confirmed_not_applied。若禁止这类
// 迁移，数据库会永远停在 push_status=pending，恢复只能反复要求核验而无法收敛。
func TestApplyRecoveryAcrossStages(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	record := domain.CodeSaveRecord{
		ID:                domain.NewID(),
		TaskID:            "task-1",
		TaskBranch:        "agent/task-1",
		ContainerID:       "container-1",
		RemoteName:        "origin",
		CommitStatus:      domain.CommitStatusPending,
		PushStatus:        domain.PushStatusPending,
		MergeStatus:       domain.MergeStatusNotRequested,
		BaselineCommitSHA: recoveryBaseline,
	}
	// 第一次核验：提交确实已经发生。
	afterCommit, err := record.ApplyRecovery(domain.CodeSaveRecoveryDecision{
		Status:    domain.RecoveryStatusConfirmedApplied,
		Stage:     "commit",
		CommitSHA: recoveryHead,
		Summary:   "提交已经发生",
	}, at)
	if err != nil {
		t.Fatalf("写回提交结论失败: %v", err)
	}
	if afterCommit.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("提交状态应推进为 succeeded，实际 %s", afterCommit.CommitStatus)
	}
	// 第二次核验：推送确实没有发生。结论针对的是另一个阶段，必须允许迁移。
	afterPush, err := afterCommit.ApplyRecovery(domain.CodeSaveRecoveryDecision{
		Status:  domain.RecoveryStatusConfirmedNotApplied,
		Stage:   "push",
		Summary: "远程任务分支不存在，推送没有发生",
	}, at.Add(time.Second))
	if err != nil {
		t.Fatalf("写回推送结论失败: %v", err)
	}
	if afterPush.RecoveryStatus != domain.RecoveryStatusConfirmedNotApplied {
		t.Fatalf("恢复结论应更新为 confirmed_not_applied，实际 %s", afterPush.RecoveryStatus)
	}
	// 已确认的提交事实不得因为后续阶段的结论而回退。
	if afterPush.CommitStatus != domain.CommitStatusSucceeded {
		t.Fatalf("后续阶段结论不得回退已确认的提交，实际 %s", afterPush.CommitStatus)
	}
	if afterPush.CommitSHA != recoveryHead {
		t.Fatalf("后续阶段结论不得清空提交 SHA，实际 %q", afterPush.CommitSHA)
	}
	// 推送被确认没有发生：整条记录不再有未决副作用，可以重新执行。
	if afterPush.HasUnresolvedSideEffect() {
		t.Fatal("确认推送未发生后不应再有未决副作用")
	}
	if action := domain.DecideCodeSaveResume(afterPush).Action; action != domain.CodeSaveResumeProceed {
		t.Fatalf("确认未发生后应允许重新执行，实际 %s", action)
	}
}
