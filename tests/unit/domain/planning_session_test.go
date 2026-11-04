package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 本文件覆盖规划会话的状态机与角色会话标识规则。
//
// 用例只调用真实的领域方法：状态迁移、结束时间与采纳版本的一致性、双角色标识的
// 互斥都由 domain 自己判定，这里不复制状态白名单，也不通过直接赋值状态字段构造
// "看起来合法"的中间态。

// planningBaseTime 是规划会话用例的固定时间基准。
//
// 领域层只做 UTC 与毫秒归一化，因此时间断言不依赖真实时钟。
var planningBaseTime = time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

// newActivePlanningSession 构造一条测试用 active 规划会话。
func newActivePlanningSession(t *testing.T) domain.PlanningSession {
	t.Helper()
	session, err := domain.NewPlanningSession("demo-service", planningBaseTime)
	if err != nil {
		t.Fatalf("构造规划会话失败: %v", err)
	}
	return session
}

// newPreviewReadySession 构造一条已生成方案、等待用户决定的规划会话。
func newPreviewReadySession(t *testing.T) domain.PlanningSession {
	t.Helper()
	session := newActivePlanningSession(t)
	at := planningBaseTime.Add(time.Minute)
	if err := session.BeginAnalysis(at); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	if err := session.MarkPreviewReady(at.Add(time.Minute)); err != nil {
		t.Fatalf("记录方案生成失败: %v", err)
	}
	return session
}

// TestPlanningSessionAnalysisRoundTrip 校验首轮分析与多轮继续分析的正常迁移。
func TestPlanningSessionAnalysisRoundTrip(t *testing.T) {
	t.Parallel()
	session := newActivePlanningSession(t)
	if session.Status != domain.PlanningSessionActive {
		t.Fatalf("新建会话应处于 active，实际 %s", session.Status)
	}
	if !session.Status.IsActive() || !session.Status.CanAcceptMessage() {
		t.Fatalf("active 会话应可继续输入，实际 active=%v accept=%v",
			session.Status.IsActive(), session.Status.CanAcceptMessage())
	}

	firstRound := planningBaseTime.Add(time.Minute)
	if err := session.BeginAnalysis(firstRound); err != nil {
		t.Fatalf("开始首轮分析失败: %v", err)
	}
	if session.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("开始分析后应处于 analyzing，实际 %s", session.Status)
	}
	if session.Status.CanAcceptMessage() {
		t.Fatal("analyzing 期间不允许追加消息")
	}
	if session.FinishedAt != nil {
		t.Fatalf("运行中的分析不应有结束时间，实际 %v", session.FinishedAt)
	}
	if !session.UpdatedAt.Equal(firstRound) {
		t.Fatalf("开始分析应刷新更新时间，期望 %v，实际 %v", firstRound, session.UpdatedAt)
	}

	previewAt := firstRound.Add(time.Minute)
	if err := session.MarkPreviewReady(previewAt); err != nil {
		t.Fatalf("记录方案生成失败: %v", err)
	}
	if session.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("本轮收敛后应处于 preview_ready，实际 %s", session.Status)
	}
	if !session.Status.CanAcceptMessage() {
		t.Fatal("preview_ready 会话应允许继续提交消息")
	}
	if session.AdoptedRevisionID != nil {
		t.Fatalf("未确认的会话不应记录采纳版本，实际 %v", session.AdoptedRevisionID)
	}
	if err := session.Validate(); err != nil {
		t.Fatalf("待确认会话应通过校验: %v", err)
	}

	// preview_ready 可以回到 analyzing：用户在同一容器、同一代码快照上继续对话。
	secondRound := previewAt.Add(time.Minute)
	if err := session.BeginAnalysis(secondRound); err != nil {
		t.Fatalf("多轮继续分析失败: %v", err)
	}
	if session.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("第二轮开始后应处于 analyzing，实际 %s", session.Status)
	}
	if !session.UpdatedAt.Equal(secondRound) {
		t.Fatalf("第二轮开始应刷新更新时间，期望 %v，实际 %v", secondRound, session.UpdatedAt)
	}
}

// TestPlanningSessionFailedNeedsExplicitRetry 校验 failed 只能经显式重试回到 analyzing。
func TestPlanningSessionFailedNeedsExplicitRetry(t *testing.T) {
	t.Parallel()
	session := newActivePlanningSession(t)
	at := planningBaseTime.Add(time.Minute)
	if err := session.BeginAnalysis(at); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	failedAt := at.Add(time.Minute)
	if err := session.Fail("模型不可用", failedAt); err != nil {
		t.Fatalf("记录分析失败失败: %v", err)
	}
	if session.Status != domain.PlanningSessionFailed {
		t.Fatalf("失败后应处于 failed，实际 %s", session.Status)
	}
	if session.ErrorSummary != "模型不可用" {
		t.Fatalf("失败摘要不符: %q", session.ErrorSummary)
	}
	if session.FinishedAt == nil || !session.FinishedAt.Equal(failedAt) {
		t.Fatalf("失败会话应记录结束时间 %v，实际 %v", failedAt, session.FinishedAt)
	}
	if session.Status.IsTerminal() {
		t.Fatal("failed 不是终态，仍允许显式重试")
	}
	if !session.Status.IsFinished() {
		t.Fatal("failed 表示本轮已结束，需要释放运行环境")
	}
	if session.Status.CanAcceptMessage() {
		t.Fatal("failed 会话不得通过普通消息入口继续")
	}

	// 普通开始入口不得隐式重置失败结论。
	if err := session.BeginAnalysis(failedAt.Add(time.Minute)); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("failed 会话调用 BeginAnalysis 应返回 ErrInvalidState，实际 %v", err)
	}
	if session.Status != domain.PlanningSessionFailed {
		t.Fatalf("被拒绝的调用不应改变状态，实际 %s", session.Status)
	}

	// 显式重试是唯一出口，并清空上一轮的失败摘要与结束时间。
	retryAt := failedAt.Add(2 * time.Minute)
	if err := session.RetryAnalysis(retryAt); err != nil {
		t.Fatalf("显式重试失败: %v", err)
	}
	if session.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("重试后应处于 analyzing，实际 %s", session.Status)
	}
	if session.ErrorSummary != "" {
		t.Fatalf("重试应清空失败摘要，实际 %q", session.ErrorSummary)
	}
	if session.FinishedAt != nil {
		t.Fatalf("重试后不应保留结束时间，实际 %v", session.FinishedAt)
	}
	if err := session.Validate(); err != nil {
		t.Fatalf("重试后的会话应通过校验: %v", err)
	}

	// 只有 failed 允许重试：非失败状态的重试必须被拒绝。
	active := newActivePlanningSession(t)
	if err := active.RetryAnalysis(planningBaseTime.Add(time.Minute)); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("非失败会话调用 RetryAnalysis 应返回 ErrInvalidState，实际 %v", err)
	}
	if active.Status != domain.PlanningSessionActive {
		t.Fatalf("被拒绝的重试不应改变状态，实际 %s", active.Status)
	}
}

// TestPlanningSessionConfirmBindsRevision 校验确认必须关联有效方案版本，并同时落结束时间。
func TestPlanningSessionConfirmBindsRevision(t *testing.T) {
	t.Parallel()
	session := newPreviewReadySession(t)

	// 空版本标识不是"未指定"，而是非法输入。
	if err := session.Confirm("", planningBaseTime.Add(3*time.Minute)); !errors.Is(
		err,
		domain.ErrEmptyID,
	) {
		t.Fatalf("空方案版本标识应返回标识校验错误，实际 %v", err)
	}
	if session.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("被拒绝的确认不应改变状态，实际 %s", session.Status)
	}

	revisionID := domain.NewID()
	confirmAt := planningBaseTime.Add(4 * time.Minute)
	if err := session.Confirm(revisionID, confirmAt); err != nil {
		t.Fatalf("确认方案版本失败: %v", err)
	}
	if session.Status != domain.PlanningSessionConfirmed {
		t.Fatalf("确认后应处于 confirmed，实际 %s", session.Status)
	}
	if session.AdoptedRevisionID == nil || *session.AdoptedRevisionID != revisionID {
		t.Fatalf("确认后应记录采纳版本 %s，实际 %v", revisionID, session.AdoptedRevisionID)
	}
	if session.FinishedAt == nil || !session.FinishedAt.Equal(confirmAt) {
		t.Fatalf("确认后应记录结束时间 %v，实际 %v", confirmAt, session.FinishedAt)
	}
	if !session.UpdatedAt.Equal(confirmAt) {
		t.Fatalf("确认后应刷新更新时间，期望 %v，实际 %v", confirmAt, session.UpdatedAt)
	}
	if err := session.Validate(); err != nil {
		t.Fatalf("已确认会话应通过校验: %v", err)
	}

	// 重复确认会改写已采纳的方案版本，必须拒绝。
	otherRevision := domain.NewID()
	if err := session.Confirm(otherRevision, confirmAt.Add(time.Minute)); !errors.Is(
		err,
		domain.ErrInvalidState,
	) {
		t.Fatalf("重复确认应返回 ErrInvalidState，实际 %v", err)
	}
	if *session.AdoptedRevisionID != revisionID {
		t.Fatalf("被拒绝的确认不得改写采纳版本，实际 %v", *session.AdoptedRevisionID)
	}
}

// TestPlanningSessionAdoptedRevisionRequiresConfirmed 校验采纳版本与状态必须一致。
func TestPlanningSessionAdoptedRevisionRequiresConfirmed(t *testing.T) {
	t.Parallel()
	session := newPreviewReadySession(t)
	adopted := domain.NewID()
	session.AdoptedRevisionID = &adopted
	if err := session.Validate(); err == nil {
		t.Fatal("未确认的会话记录采纳版本应被拒绝")
	}
}

// TestPlanningSessionAbandonFollowsStateMachine 校验放弃入口遵守领域状态机。
func TestPlanningSessionAbandonFollowsStateMachine(t *testing.T) {
	t.Parallel()

	active := newActivePlanningSession(t)
	activeAbandonAt := planningBaseTime.Add(time.Minute)
	if err := active.Abandon(activeAbandonAt); err != nil {
		t.Fatalf("放弃 active 会话失败: %v", err)
	}
	if active.Status != domain.PlanningSessionAbandoned {
		t.Fatalf("放弃后应处于 abandoned，实际 %s", active.Status)
	}
	if active.FinishedAt == nil || !active.FinishedAt.Equal(activeAbandonAt) {
		t.Fatalf("放弃后应记录结束时间 %v，实际 %v", activeAbandonAt, active.FinishedAt)
	}

	previewReady := newPreviewReadySession(t)
	previewAbandonAt := planningBaseTime.Add(5 * time.Minute)
	if err := previewReady.Abandon(previewAbandonAt); err != nil {
		t.Fatalf("放弃 preview_ready 会话失败: %v", err)
	}
	if previewReady.Status != domain.PlanningSessionAbandoned {
		t.Fatalf("放弃后应处于 abandoned，实际 %s", previewReady.Status)
	}
	if previewReady.AdoptedRevisionID != nil {
		t.Fatalf("放弃不应记录采纳版本，实际 %v", previewReady.AdoptedRevisionID)
	}

	// 一轮分析在途时不允许通过普通放弃入口跳过它。
	analyzing := newActivePlanningSession(t)
	at := planningBaseTime.Add(time.Minute)
	if err := analyzing.BeginAnalysis(at); err != nil {
		t.Fatalf("开始分析失败: %v", err)
	}
	if err := analyzing.Abandon(at.Add(time.Minute)); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("analyzing 期间放弃应返回 ErrInvalidTransition，实际 %v", err)
	}
	if analyzing.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("被拒绝的放弃不应改变状态，实际 %s", analyzing.Status)
	}
	if analyzing.FinishedAt != nil {
		t.Fatalf("被拒绝的放弃不应写入结束时间，实际 %v", analyzing.FinishedAt)
	}

	// 重复放弃同一状态是幂等的空操作：状态与结束时间都不被改写，也不会重新打开会话。
	if err := active.Abandon(activeAbandonAt.Add(time.Minute)); err != nil {
		t.Fatalf("重复放弃同一状态应幂等，实际 %v", err)
	}
	if active.Status != domain.PlanningSessionAbandoned {
		t.Fatalf("重复放弃不应改变状态，实际 %s", active.Status)
	}
	if active.FinishedAt == nil || !active.FinishedAt.Equal(activeAbandonAt) {
		t.Fatalf("重复放弃不应改写结束时间，实际 %v", active.FinishedAt)
	}
}

// TestPlanningSessionTerminalStatesStayClosed 校验终态不会被任何入口重新打开。
func TestPlanningSessionTerminalStatesStayClosed(t *testing.T) {
	t.Parallel()

	confirmed := newPreviewReadySession(t)
	confirmAt := planningBaseTime.Add(3 * time.Minute)
	if err := confirmed.Confirm(domain.NewID(), confirmAt); err != nil {
		t.Fatalf("确认方案版本失败: %v", err)
	}
	abandoned := newActivePlanningSession(t)
	abandonAt := planningBaseTime.Add(time.Minute)
	if err := abandoned.Abandon(abandonAt); err != nil {
		t.Fatalf("放弃会话失败: %v", err)
	}

	cases := []struct {
		name    string
		session domain.PlanningSession
		at      time.Time
	}{
		{name: "已确认", session: confirmed, at: confirmAt.Add(time.Minute)},
		{name: "已放弃", session: abandoned, at: abandonAt.Add(time.Minute)},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			session := testCase.session
			if !session.Status.IsTerminal() {
				t.Fatalf("终态判定不符: %s", session.Status)
			}
			if session.Status.IsActive() {
				t.Fatalf("终态不应被当作进行中: %s", session.Status)
			}
			if session.Status.CanAcceptMessage() {
				t.Fatalf("终态不应接受新消息: %s", session.Status)
			}
			if err := session.BeginAnalysis(testCase.at); !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("终态调用 BeginAnalysis 应返回 ErrInvalidState，实际 %v", err)
			}
			if err := session.RetryAnalysis(testCase.at); !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("终态调用 RetryAnalysis 应返回 ErrInvalidState，实际 %v", err)
			}
			if err := session.Transition(domain.PlanningSessionActive, testCase.at); !errors.Is(
				err,
				domain.ErrInvalidTransition,
			) {
				t.Fatalf("终态回到 active 应返回 ErrInvalidTransition，实际 %v", err)
			}
			// 已确认的会话不能放弃；已放弃的会话重复放弃是幂等的空操作。
			abandonErr := session.Abandon(testCase.at)
			if session.Status == domain.PlanningSessionConfirmed && abandonErr == nil {
				t.Fatal("已确认的会话不应允许放弃")
			}
			if session.Status != domain.PlanningSessionAbandoned && abandonErr == nil {
				t.Fatalf("终态被改写成 %s", session.Status)
			}
		})
	}
}

// TestPlanningSessionRoleSessionsStayDistinct 校验两个规划角色的会话标识规则。
func TestPlanningSessionRoleSessionsStayDistinct(t *testing.T) {
	t.Parallel()
	session := newActivePlanningSession(t)
	at := planningBaseTime.Add(time.Minute)

	// 两个角色取值相同说明上下文会串线，必须在写入前拒绝。
	if err := session.BindRoleSessions("shared-session", "shared-session", at); !errors.Is(
		err,
		domain.ErrInvalidValue,
	) {
		t.Fatalf("两个角色标识相同应返回字段校验错误，实际 %v", err)
	}
	if session.AnalystSessionID != "" || session.PlannerSessionID != "" {
		t.Fatalf("被拒绝的绑定不应写入任何标识，实际 %q / %q",
			session.AnalystSessionID, session.PlannerSessionID)
	}

	analyst := "analyst-session-1"
	planner := "planner-session-1"
	if err := session.BindRoleSessions(analyst, planner, at); err != nil {
		t.Fatalf("绑定角色会话失败: %v", err)
	}
	if session.AnalystSessionID != analyst || session.PlannerSessionID != planner {
		t.Fatalf("角色会话标识不符: %q / %q", session.AnalystSessionID, session.PlannerSessionID)
	}

	// 同一会话多轮复用同一对标识：重复写入相同取值是幂等的，不会改写已绑定的标识。
	rebindAt := at.Add(time.Minute)
	if err := session.BindRoleSessions(analyst, planner, rebindAt); err != nil {
		t.Fatalf("重复绑定相同角色会话失败: %v", err)
	}
	if session.AnalystSessionID != analyst || session.PlannerSessionID != planner {
		t.Fatalf("重复绑定改写了角色会话标识: %q / %q",
			session.AnalystSessionID, session.PlannerSessionID)
	}
	if !session.UpdatedAt.Equal(rebindAt) {
		t.Fatalf("角色绑定应刷新更新时间，期望 %v，实际 %v", rebindAt, session.UpdatedAt)
	}
	if !session.Status.IsActive() {
		t.Fatalf("角色绑定不应改变业务状态，实际 %s", session.Status)
	}
}
