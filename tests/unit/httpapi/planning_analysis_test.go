package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/logging"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 规划会话业务接口与两阶段分析接口的协议测试。
//
// 模型调用由受控替身承担：这些用例验证协议转换、错误映射、并发拒绝与状态迁移，
// 不依赖真实模型或外部网络。

// apiRequirement 是提交给规划会话的用户需求。
const apiRequirement = "为规划会话补齐两阶段结构化分析"

// apiAnalysisJSON 返回一份通过领域校验的需求分析 JSON。
func apiAnalysisJSON() string {
	return `{"summary":"补齐两阶段结构化分析","goals":["产出结构化需求分析"],` +
		`"current_state":["已有规划会话与消息表"],"scope":["internal/app"],` +
		`"out_of_scope":["不创建正式任务"],"constraints":["保持项目只读"],` +
		`"risks":["模型输出不稳定"],"acceptance_criteria":["非法输出一律失败"]}`
}

// apiPlanJSON 返回一份通过领域校验的实施方案 JSON。
func apiPlanJSON() string {
	return `{"summary":"按两个阶段推进","tasks":[` +
		`{"id":"task-1","title":"领域契约","description":"定义并校验结构",` +
		`"depends_on":[],"acceptance_criteria":["非法字段被拒绝"]},` +
		`{"id":"task-2","title":"编排","description":"串联两个角色",` +
		`"depends_on":["task-1"],"acceptance_criteria":["失败不留下半成品"]}]}`
}

// apiFakeAgent 是规划角色调用的受控替身。
type apiFakeAgent struct {
	mu    sync.Mutex
	texts map[string]string
	errs  map[string]error
	hold  chan struct{}
	// calls 统计进入 RunRole 的次数。
	calls int
}

// newAPIFakeAgent 构造替身并给定两个阶段的默认成功输出。
func newAPIFakeAgent() *apiFakeAgent {
	return &apiFakeAgent{
		texts: map[string]string{
			string(domain.RoleTypeRequirementAnalyst): apiAnalysisJSON(),
			string(domain.RoleTypePlanner):            apiPlanJSON(),
		},
		errs: make(map[string]error),
	}
}

// setError 让指定角色的调用返回错误。
func (f *apiFakeAgent) setError(roleType domain.RoleType, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[string(roleType)] = err
}

// setText 覆盖指定角色的最终文本。
func (f *apiFakeAgent) setText(roleType domain.RoleType, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts[string(roleType)] = text
}

// holdCalls 让后续调用阻塞，直到 release 被调用。
func (f *apiFakeAgent) holdCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hold == nil {
		f.hold = make(chan struct{})
	}
}

// release 放行被阻塞的调用。
func (f *apiFakeAgent) release() {
	f.mu.Lock()
	hold := f.hold
	f.hold = nil
	f.mu.Unlock()
	if hold != nil {
		close(hold)
	}
}

// entered 判断是否已有调用进入执行。
func (f *apiFakeAgent) entered() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls > 0
}

// RunRole 记录一次调用并按配置返回结果。
func (f *apiFakeAgent) RunRole(
	ctx context.Context,
	request app.RunPlanningRoleRequest,
) (app.PlanningAgentResult, error) {
	f.mu.Lock()
	f.calls++
	text := f.texts[request.RoleType]
	runErr := f.errs[request.RoleType]
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return f.result(request, text, agent.RunCancelled), ctx.Err()
		}
	}
	if runErr != nil {
		return f.result(request, text, agent.RunFailed), runErr
	}
	return f.result(request, text, agent.RunCompleted), nil
}

// result 构造一次调用的执行结果。
func (f *apiFakeAgent) result(
	request app.RunPlanningRoleRequest,
	text string,
	status agent.RunStatus,
) app.PlanningAgentResult {
	return app.PlanningAgentResult{
		RoleSessionID: "role-session-" + request.RoleType,
		Result: agent.RunResult{
			Status:    status,
			FinalText: text,
			ProcessID: 4242,
			Session:   agent.SessionFiles{Completeness: agent.SessionComplete},
		},
	}
}

// planningAnalysisAPIFixture 是装配了规划会话与两阶段分析接口的测试现场。
type planningAnalysisAPIFixture struct {
	*planningAPIFixture
	agent *apiFakeAgent
	// analysisHandler 已装配规划会话与两阶段分析服务。
	analysisHandler http.Handler
	// analysisBare 未装配两阶段分析服务，用于核验 503。
	analysisBare http.Handler
}

// newPlanningAnalysisAPIFixture 装配两阶段分析接口。
func newPlanningAnalysisAPIFixture(t *testing.T) *planningAnalysisAPIFixture {
	t.Helper()
	base := newPlanningAPIFixture(t)
	agentPort := newAPIFakeAgent()
	analysis, err := app.NewPlanningAnalysisService(app.PlanningAnalysisServiceOptions{
		Store: base.db,
		Agent: agentPort,
	})
	if err != nil {
		t.Fatalf("构造规划分析服务失败: %v", err)
	}
	logger, err := testkit.Logger(logging.LevelError)
	if err != nil {
		t.Fatalf("构造日志器失败: %v", err)
	}
	return &planningAnalysisAPIFixture{
		planningAPIFixture: base,
		agent:              agentPort,
		analysisHandler: httpapi.NewHandler(httpapi.Deps{
			PlanningSessions: base.sessions,
			PlanningAnalysis: analysis,
			Logger:           logger.Slog(),
		}),
		analysisBare: httpapi.NewHandler(httpapi.Deps{
			PlanningSessions: base.sessions,
			Logger:           logger.Slog(),
		}),
	}
}

// analyzingSession 创建一个已提交需求的 analyzing 会话。
func (f *planningAnalysisAPIFixture) analyzingSession(t *testing.T) domain.PlanningSession {
	t.Helper()
	session := f.newSession(t)
	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/messages",
		map[string]string{"content": apiRequirement})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("提交规划消息应返回 201，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	return f.reloadSession(t, session.ID)
}

// reloadSession 重新读取规划会话的持久化状态。
func (f *planningAnalysisAPIFixture) reloadSession(
	t *testing.T,
	sessionID domain.ID,
) domain.PlanningSession {
	t.Helper()
	session, err := f.sessions.Get(f.ctx, sessionID.String())
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return session
}

// analysisPath 返回会话两阶段分析接口路径。
func analysisPath(sessionID domain.ID) string {
	return httpapi.PathPlanningSessions + "/" + sessionID.String() + "/analysis"
}

// TestPlanningAPICreateAndListSessions 验收创建与按项目列表。
func TestPlanningAPICreateAndListSessions(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	recorder := f.send(t, f.analysisHandler, http.MethodPost, httpapi.PathPlanningSessions,
		map[string]string{"project_id": f.project.ID})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("创建规划会话应返回 201，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var created httpapi.PlanningSessionDetailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v", err)
	}
	if created.Session.Status != string(domain.PlanningSessionActive) {
		t.Fatalf("新建会话状态应为 active，实际 %s", created.Session.Status)
	}

	recorder = f.send(t, f.analysisHandler, http.MethodGet,
		httpapi.PathPlanningSessions+"?project_id="+f.project.ID, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("查询规划会话列表应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var list struct {
		Records []httpapi.PlanningSessionResponse `json:"records"`
		Total   int                               `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析列表响应失败: %v", err)
	}
	if list.Total != 1 || len(list.Records) != 1 {
		t.Fatalf("会话列表数量不符: %+v", list)
	}

	// 缺少项目参数必须按参数错误返回，而不是返回空列表。
	recorder = f.send(t, f.analysisHandler, http.MethodGet, httpapi.PathPlanningSessions, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少 project_id 应返回 400，实际 %d", recorder.Code)
	}
}

// TestPlanningAPIAnalysisFullFlow 验收两阶段分析的完整协议流程。
func TestPlanningAPIAnalysisFullFlow(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.analyzingSession(t)

	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		analysisPath(session.ID), map[string]string{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("执行两阶段分析应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var payload httpapi.PlanningAnalysisResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析分析响应失败: %v", err)
	}
	if payload.Session.Status != string(domain.PlanningSessionPreviewReady) {
		t.Fatalf("分析成功后状态应为 preview_ready，实际 %s", payload.Session.Status)
	}
	if payload.Revision.RevisionNumber != 1 {
		t.Fatalf("版本号应为 1，实际 %d", payload.Revision.RevisionNumber)
	}
	if payload.Revision.SnapshotVersion != domain.PlanningRevisionSnapshotVersion {
		t.Fatalf("快照版本不符: %d", payload.Revision.SnapshotVersion)
	}
	if len(payload.Messages) != 2 {
		t.Fatalf("本次应追加两个角色消息，实际 %d", len(payload.Messages))
	}
	if len(payload.RequirementAnalysis.Goals) == 0 || len(payload.ImplementationPlan.Tasks) != 2 {
		t.Fatalf("分析结果字段不完整: %+v", payload)
	}

	// 版本列表必须能读回同一份快照。
	recorder = f.send(t, f.analysisHandler, http.MethodGet,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/revisions", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("查询版本列表应返回 200，实际 %d", recorder.Code)
	}
	var revisions httpapi.PlanningRevisionListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &revisions); err != nil {
		t.Fatalf("解析版本列表失败: %v", err)
	}
	if revisions.Total != 1 {
		t.Fatalf("版本数量应为 1，实际 %d", revisions.Total)
	}
	var analysis domain.RequirementAnalysis
	if err := json.Unmarshal(revisions.Records[0].RequirementAnalysis, &analysis); err != nil {
		t.Fatalf("解析版本中的需求分析失败: %v", err)
	}
	if analysis.Summary != payload.RequirementAnalysis.Summary {
		t.Fatalf("版本快照与分析结果不一致: %q", analysis.Summary)
	}

	// 消息列表包含用户消息与两个角色消息。
	recorder = f.send(t, f.analysisHandler, http.MethodGet,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/messages", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("查询消息列表应返回 200，实际 %d", recorder.Code)
	}
	var messages httpapi.PlanningMessageListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &messages); err != nil {
		t.Fatalf("解析消息列表失败: %v", err)
	}
	if messages.Total != 3 {
		t.Fatalf("消息数量应为 3，实际 %d", messages.Total)
	}
}

// TestPlanningAPIAnalysisRequiresAnalyzingSession 验收非 analyzing 会话返回 409。
func TestPlanningAPIAnalysisRequiresAnalyzingSession(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.newSession(t)
	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		analysisPath(session.ID), map[string]string{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("active 会话执行分析应返回 409，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodeConflict {
		t.Fatalf("错误代码应为 %s，实际 %s", httpapi.CodeConflict, code)
	}
	if f.agent.calls != 0 {
		t.Fatalf("状态不满足时不得发起模型调用，实际 %d 次", f.agent.calls)
	}
}

// TestPlanningAPIAnalysisRejectsConcurrentRequest 验收并发分析返回 409。
func TestPlanningAPIAnalysisRejectsConcurrentRequest(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.analyzingSession(t)
	f.agent.holdCalls()

	done := make(chan int, 1)
	go func() {
		recorder := f.send(t, f.analysisHandler, http.MethodPost,
			analysisPath(session.ID), map[string]string{})
		done <- recorder.Code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !f.agent.entered() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !f.agent.entered() {
		t.Fatal("等待首个分析进入执行超时")
	}

	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		analysisPath(session.ID), map[string]string{})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("并发分析应返回 409，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	f.agent.release()
	if code := <-done; code != http.StatusOK {
		t.Fatalf("首个分析应返回 200，实际 %d", code)
	}
}

// TestPlanningAPIAnalysisFailureIsReported 验收分析失败如实返回并记录失败状态。
func TestPlanningAPIAnalysisFailureIsReported(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.analyzingSession(t)
	f.agent.setError(domain.RoleTypeRequirementAnalyst, errPlanningAPIAnalysis("容器执行失败"))

	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		analysisPath(session.ID), map[string]string{})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("分析失败应返回 500，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if code := planningErrorCode(t, recorder); code != httpapi.CodeExecutionFailed {
		t.Fatalf("错误代码应为 %s，实际 %s", httpapi.CodeExecutionFailed, code)
	}
	stored := f.reloadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionFailed {
		t.Fatalf("失败后会话状态应为 failed，实际 %s", stored.Status)
	}
	if !strings.Contains(stored.ErrorSummary, "需求分析阶段") {
		t.Fatalf("失败摘要没有记录阶段: %q", stored.ErrorSummary)
	}

	// 显式重试：替身恢复正常后应当成功并产生版本。
	f.agent.setError(domain.RoleTypeRequirementAnalyst, nil)
	recorder = f.send(t, f.analysisHandler, http.MethodPost,
		analysisPath(session.ID)+"/retry", map[string]string{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("重试应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	stored = f.reloadSession(t, session.ID)
	if stored.Status != domain.PlanningSessionPreviewReady {
		t.Fatalf("重试成功后状态应为 preview_ready，实际 %s", stored.Status)
	}
	if stored.ErrorSummary != "" {
		t.Fatalf("重试成功后失败摘要未清空: %q", stored.ErrorSummary)
	}
}

// TestPlanningAPIAnalysisWithoutService 验收未装配分析服务时返回 503。
func TestPlanningAPIAnalysisWithoutService(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.analyzingSession(t)
	recorder := f.send(t, f.analysisBare, http.MethodPost,
		analysisPath(session.ID), map[string]string{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配分析服务应返回 503，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
}

// TestPlanningAPIAnalysisRejectsUnknownField 验收请求体未知字段被拒绝。
func TestPlanningAPIAnalysisRejectsUnknownField(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.analyzingSession(t)
	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		analysisPath(session.ID), map[string]string{"role_type": "planner"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应返回 400，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if f.agent.calls != 0 {
		t.Fatalf("参数错误不得发起模型调用，实际 %d 次", f.agent.calls)
	}
}

// TestPlanningAPIMessageAndAbandonFlow 验收消息提交与放弃。
func TestPlanningAPIMessageAndAbandonFlow(t *testing.T) {
	f := newPlanningAnalysisAPIFixture(t)
	session := f.newSession(t)

	recorder := f.send(t, f.analysisHandler, http.MethodPost,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/messages",
		map[string]string{"content": "   "})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("空消息应返回 400，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	if f.agent.calls != 0 {
		t.Fatalf("提交消息不应触发模型调用，实际 %d 次", f.agent.calls)
	}

	recorder = f.send(t, f.analysisHandler, http.MethodPost,
		httpapi.PathPlanningSessions+"/"+session.ID.String()+"/abandon", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("放弃规划应返回 200，实际 %d，响应体 %s",
			recorder.Code, recorder.Body.String())
	}
	var payload httpapi.PlanningSessionDetailResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析放弃响应失败: %v", err)
	}
	if payload.Session.Status != string(domain.PlanningSessionAbandoned) {
		t.Fatalf("放弃后状态应为 abandoned，实际 %s", payload.Session.Status)
	}
}

// errPlanningAPIAnalysis 是分析接口测试使用的固定错误。
type errPlanningAPIAnalysis string

// Error 实现 error 接口。
func (e errPlanningAPIAnalysis) Error() string {
	return string(e)
}
