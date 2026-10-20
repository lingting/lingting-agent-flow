package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// newAttemptForTest 构造一个处于 running 的测试尝试。
func newAttemptForTest(t *testing.T) domain.Attempt {
	t.Helper()
	attempt, err := domain.NewAttempt(
		domain.NewID(),
		domain.NewID(),
		1,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造执行尝试失败: %v", err)
	}
	return attempt
}

// TestAttemptLegalTransitions 校验尝试的合法状态迁移。
func TestAttemptLegalTransitions(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct {
		name   string
		finish func(*domain.Attempt) error
		want   domain.AttemptStatus
	}{
		{"完成", func(a *domain.Attempt) error { return a.Complete(now) }, domain.AttemptStatusCompleted},
		{"失败", func(a *domain.Attempt) error { return a.Fail("构建失败", now) }, domain.AttemptStatusFailed},
		{"取消", func(a *domain.Attempt) error { return a.Cancel("用户取消", now) }, domain.AttemptStatusCancelled},
		{"超时", func(a *domain.Attempt) error { return a.Timeout("超时", now) }, domain.AttemptStatusTimedOut},
		{"中断", func(a *domain.Attempt) error { return a.Interrupt("宿主退出", now) }, domain.AttemptStatusInterrupted},
	}
	for _, testCase := range cases {
		attempt := newAttemptForTest(t)
		if err := testCase.finish(&attempt); err != nil {
			t.Fatalf("%s 迁移失败: %v", testCase.name, err)
		}
		if attempt.Status != testCase.want {
			t.Fatalf("%s 状态不符: %s", testCase.name, attempt.Status)
		}
		if attempt.FinishedAt == nil {
			t.Fatalf("%s 应当记录结束时间", testCase.name)
		}
		if !attempt.Status.IsTerminal() {
			t.Fatalf("%s 应当属于终态: %s", testCase.name, attempt.Status)
		}
	}
}

// TestAttemptTerminalCannotBeReopened 校验终态尝试不能被重复完成重新打开。
func TestAttemptTerminalCannotBeReopened(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	attempt := newAttemptForTest(t)
	if err := attempt.Complete(now); err != nil {
		t.Fatalf("完成失败: %v", err)
	}
	// 重复完成、失败、取消都必须是非法迁移，历史事实不能被改写。
	for name, err := range map[string]error{
		"重复完成": attempt.Complete(now),
		"改为失败": attempt.Fail("重试后失败", now),
		"改为取消": attempt.Cancel("取消", now),
		"改为超时": attempt.Timeout("超时", now),
	} {
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("%s 应当返回 ErrInvalidTransition，实际: %v", name, err)
		}
	}
	if attempt.Status != domain.AttemptStatusCompleted {
		t.Fatalf("终态不应被改写，实际: %s", attempt.Status)
	}
}

// TestAttemptFailedRequiresSummary 校验失败必须给出摘要。
func TestAttemptFailedRequiresSummary(t *testing.T) {
	t.Parallel()
	attempt := newAttemptForTest(t)
	if err := attempt.Fail("   ", time.Now().UTC()); !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("空失败摘要应当返回 ErrInvalidValue，实际: %v", err)
	}
	if attempt.Status != domain.AttemptStatusRunning {
		t.Fatalf("失败的迁移不应改变状态，实际: %s", attempt.Status)
	}
}

// TestAttemptCancelledFlagIsSet 校验取消会同步标记 cancelled 字段。
func TestAttemptCancelledFlagIsSet(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cancelled := newAttemptForTest(t)
	if err := cancelled.Cancel("用户取消", now); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if !cancelled.Cancelled {
		t.Fatal("取消后 cancelled 字段必须为真")
	}
	if cancelled.TimedOut {
		t.Fatal("取消不应同时标记为超时")
	}
	timedOut := newAttemptForTest(t)
	if err := timedOut.Timeout("超时", now); err != nil {
		t.Fatalf("标记超时失败: %v", err)
	}
	if !timedOut.TimedOut || timedOut.Cancelled {
		t.Fatalf("超时与取消必须分别表达: %+v", timedOut)
	}
}

// TestAttemptUnknownCanBeResolved 校验不确定结果可以在核验后收敛。
func TestAttemptUnknownCanBeResolved(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	attempt := newAttemptForTest(t)
	if err := attempt.MarkUnknown("远程结果无法确认", now); err != nil {
		t.Fatalf("标记不确定失败: %v", err)
	}
	if attempt.Status.IsTerminal() {
		t.Fatal("不确定状态不是终态，必须先核验")
	}
	if err := attempt.Resolve(domain.AttemptStatusFailed, "核验为失败", now); err != nil {
		t.Fatalf("核验收敛失败: %v", err)
	}
	if attempt.Status != domain.AttemptStatusFailed {
		t.Fatalf("收敛状态不符: %s", attempt.Status)
	}
	// 核验只能收敛为确定状态。
	other := newAttemptForTest(t)
	if err := other.MarkUnknown("不确定", now); err != nil {
		t.Fatalf("标记不确定失败: %v", err)
	}
	if err := other.Resolve(domain.AttemptStatusUnknown, "仍不确定", now); err == nil {
		t.Fatal("核验不应收敛为不确定状态")
	}
}

// TestStepLegalTransitions 校验步骤合法状态迁移。
func TestStepLegalTransitions(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	step := newStepForTest(t)
	if err := step.Transition(domain.StepStatusRunning, now); err != nil {
		t.Fatalf("启动步骤失败: %v", err)
	}
	if step.StartedAt == nil {
		t.Fatal("启动后必须记录开始时间")
	}
	if err := step.Transition(domain.StepStatusCompleted, now); err != nil {
		t.Fatalf("完成步骤失败: %v", err)
	}
	if step.FinishedAt == nil {
		t.Fatal("完成后必须记录结束时间")
	}
	if !step.Status.IsTerminal() {
		t.Fatalf("completed 应当是终态: %s", step.Status)
	}
}

// TestStepPendingCanBeSkippedOrCancelled 校验未开始的步骤可以直接跳过或取消。
func TestStepPendingCanBeSkippedOrCancelled(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	skipped := newStepForTest(t)
	if err := skipped.Transition(domain.StepStatusSkipped, now); err != nil {
		t.Fatalf("跳过步骤失败: %v", err)
	}
	if !skipped.Status.IsTerminal() {
		t.Fatal("skipped 应当是终态")
	}
	cancelled := newStepForTest(t)
	if err := cancelled.Transition(domain.StepStatusCancelled, now); err != nil {
		t.Fatalf("取消步骤失败: %v", err)
	}
	if !cancelled.Status.IsTerminal() {
		t.Fatal("cancelled 应当是终态")
	}
}

// TestStepTerminalCannotReturnToRunning 校验终态步骤不能重新开始。
func TestStepTerminalCannotReturnToRunning(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	step := newStepForTest(t)
	if err := step.Transition(domain.StepStatusCancelled, now); err != nil {
		t.Fatalf("取消步骤失败: %v", err)
	}
	if err := step.Transition(domain.StepStatusRunning, now); !errors.Is(
		err,
		domain.ErrInvalidTransition,
	) {
		t.Fatalf("终态回到 running 应当被拒绝，实际: %v", err)
	}
}

// TestStepUnknownStatusIsRejectedOnTransition 校验未知状态不能作为迁移目标。
func TestStepUnknownStatusIsRejectedOnTransition(t *testing.T) {
	t.Parallel()
	step := newStepForTest(t)
	err := step.Transition(domain.StepStatus("not_a_status"), time.Now().UTC())
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("未知状态应当返回 ErrInvalidValue，实际: %v", err)
	}
}

// TestSessionExecutionAndArchiveAreIndependent 校验会话执行与归档相互独立。
func TestSessionExecutionAndArchiveAreIndependent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	session := newSessionForTest(t)
	if err := session.MarkRunning("pi-session"); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := session.Complete(now); err != nil {
		t.Fatalf("完成失败: %v", err)
	}
	// 归档失败不改变执行状态。
	if err := session.ApplyArchive(domain.SessionArchiveFailed, ""); err != nil {
		t.Fatalf("记录归档失败时出错: %v", err)
	}
	if session.Status != domain.SessionStatusCompleted {
		t.Fatalf("归档失败不应改变执行状态: %s", session.Status)
	}
	if session.ArchiveStatus != domain.SessionArchiveFailed {
		t.Fatalf("归档状态不符: %s", session.ArchiveStatus)
	}
}

// TestSessionFailedCallStillRecordsArchive 校验调用失败仍可记录归档成功。
func TestSessionFailedCallStillRecordsArchive(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	session := newSessionForTest(t)
	if err := session.MarkRunning("pi-session"); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := session.Fail(now); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	if err := session.ApplyArchive(domain.SessionArchiveSucceeded, "/archive/a.jsonl"); err != nil {
		t.Fatalf("记录归档成功时出错: %v", err)
	}
	if session.Status != domain.SessionStatusFailed {
		t.Fatalf("归档成功不应把失败改写为成功: %s", session.Status)
	}
	if session.ArchivePath != "/archive/a.jsonl" {
		t.Fatalf("归档位置不符: %q", session.ArchivePath)
	}
}

// TestSessionArchiveCannotBeOverwritten 校验已确认的归档结果不能被改写。
func TestSessionArchiveCannotBeOverwritten(t *testing.T) {
	t.Parallel()
	session := newSessionForTest(t)
	if err := session.ApplyArchive(domain.SessionArchiveSucceeded, "/a.jsonl"); err != nil {
		t.Fatalf("记录归档失败: %v", err)
	}
	err := session.ApplyArchive(domain.SessionArchiveFailed, "")
	if !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("改写归档结果应当返回 ErrInvalidState，实际: %v", err)
	}
	if session.ArchiveStatus != domain.SessionArchiveSucceeded {
		t.Fatalf("归档状态不应被改写: %s", session.ArchiveStatus)
	}
}

// TestSessionArchiveSucceededRequiresPath 校验归档成功必须给出位置。
func TestSessionArchiveSucceededRequiresPath(t *testing.T) {
	t.Parallel()
	session := newSessionForTest(t)
	err := session.ApplyArchive(domain.SessionArchiveSucceeded, "  ")
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("缺少归档位置应当返回 ErrInvalidValue，实际: %v", err)
	}
}

// TestSessionArchiveRejectsUnknownStatus 校验归档状态写入时拒绝拼写错误。
func TestSessionArchiveRejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	session := newSessionForTest(t)
	err := session.ApplyArchive(domain.SessionArchiveStatus("archived_v2"), "/a.jsonl")
	if !errors.Is(err, domain.ErrInvalidValue) {
		t.Fatalf("未知归档状态应当返回 ErrInvalidValue，实际: %v", err)
	}
}

// TestSessionExportStatusIsIndependent 校验导出状态与执行状态独立。
func TestSessionExportStatusIsIndependent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	session := newSessionForTest(t)
	if err := session.MarkRunning("pi-session"); err != nil {
		t.Fatalf("标记运行失败: %v", err)
	}
	if err := session.ApplyArtifacts(domain.SessionArtifacts{
		JSONLPath:    "/a.jsonl",
		RunStatus:    domain.SessionRunCompleted,
		Completeness: domain.SessionCompletenessComplete,
		ExportStatus: domain.SessionExportFailed,
		ExitCode:     0,
	}, now); err != nil {
		t.Fatalf("记录产出失败: %v", err)
	}
	if session.Status != domain.SessionStatusCompleted {
		t.Fatalf("HTML 导出失败不应把调用判为失败: %s", session.Status)
	}
	if session.ExportStatus != domain.SessionExportFailed {
		t.Fatalf("导出状态不符: %s", session.ExportStatus)
	}
	if session.JSONLPath != "/a.jsonl" {
		t.Fatalf("原始会话路径必须保留: %q", session.JSONLPath)
	}
}

// TestUnexecutedRecordsAreNotMarkedSuccessful 校验未执行的记录不会被标成成功。
func TestUnexecutedRecordsAreNotMarkedSuccessful(t *testing.T) {
	t.Parallel()
	step := newStepForTest(t)
	if step.Status.IsTerminal() {
		t.Fatal("新建步骤不应是终态")
	}
	if step.StartedAt != nil || step.FinishedAt != nil {
		t.Fatal("新建步骤不应有开始或结束时间")
	}
	session := newSessionForTest(t)
	if session.Status.IsTerminal() {
		t.Fatal("新建会话不应是终态")
	}
	if session.ArchiveStatus != "" && session.ArchiveStatus != domain.SessionArchiveNotAttempted {
		t.Fatalf("新建会话不应有归档结论: %s", session.ArchiveStatus)
	}
}

// newStepForTest 构造一个处于 pending 的测试步骤。
func newStepForTest(t *testing.T) domain.Step {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	step, err := domain.NewStep(
		domain.NewID(),
		1,
		"步骤标题",
		"步骤目标",
		"执行要求",
		role,
		domain.VerificationSpec{},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造步骤失败: %v", err)
	}
	return step
}

// newSessionForTest 构造一个处于 starting 的测试会话。
func newSessionForTest(t *testing.T) domain.Session {
	t.Helper()
	role, err := domain.NewRole(
		"开发工程师",
		domain.RoleTypeDeveloper,
		"provider/model",
		"提示词",
		domain.DefaultToolsPolicy(domain.RoleTypeDeveloper),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造角色失败: %v", err)
	}
	session, err := domain.NewSession(
		domain.SessionScope{TaskID: domain.NewID()},
		role,
		"pi",
		"provider/model",
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("构造会话失败: %v", err)
	}
	return session
}
