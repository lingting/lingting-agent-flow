package planninganalysis_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// 两阶段分析编排的测试数据。
const (
	// analysisProjectID 是用例使用的项目标识。
	analysisProjectID = "planning-analysis-demo"
	// analysisVersionLabel 是项目使用的环境版本标签。
	analysisVersionLabel = "v1"
	// analysisRequirement 是提交给规划会话的用户需求。
	analysisRequirement = "为规划会话补齐两阶段结构化分析"
)

// validAnalysisJSON 返回一份通过领域校验的需求分析 JSON。
func validAnalysisJSON() string {
	return `{"summary":"补齐两阶段结构化分析",` +
		`"goals":["产出结构化需求分析"],` +
		`"current_state":["已有规划会话与消息表"],` +
		`"scope":["internal/app"],` +
		`"out_of_scope":["不创建正式任务"],` +
		`"constraints":["保持项目只读"],` +
		`"risks":["模型输出不稳定"],` +
		`"acceptance_criteria":["非法输出一律失败"]}`
}

// validPlanJSON 返回一份通过领域校验的实施方案 JSON。
func validPlanJSON() string {
	return `{"summary":"按两个阶段推进",` +
		`"tasks":[` +
		`{"id":"task-1","title":"领域契约","description":"定义并校验结构",` +
		`"depends_on":[],"acceptance_criteria":["非法字段被拒绝"]},` +
		`{"id":"task-2","title":"编排","description":"串联两个角色",` +
		`"depends_on":["task-1"],"acceptance_criteria":["失败不留下半成品"]}]}`
}

// fakeAnalysisAgent 是规划角色调用的受控替身。
//
// 它按角色类型返回配置好的最终文本与执行状态，并记录每一次调用：用例据此断言"分析师
// 失败时不调用规划师"这类顺序事实，而不依赖真实模型或外部网络。
type fakeAnalysisAgent struct {
	mu       sync.Mutex
	requests []app.RunPlanningRoleRequest
	texts    map[string]string
	errs     map[string]error
	statuses map[string]agent.RunStatus
	// hold 非空时 RunRole 在返回之前等待它关闭或被取消，用于并发用例。
	hold chan struct{}
	// entered 每次进入 RunRole 时写入角色类型，供并发用例同步。
	entered chan string
	// calls 统计进入 RunRole 的次数。
	calls int32
}

// newFakeAnalysisAgent 构造替身并给定两个阶段的默认成功输出。
func newFakeAnalysisAgent() *fakeAnalysisAgent {
	return &fakeAnalysisAgent{
		texts: map[string]string{
			string(domain.RoleTypeRequirementAnalyst): validAnalysisJSON(),
			string(domain.RoleTypePlanner):            validPlanJSON(),
		},
		errs:     make(map[string]error),
		statuses: make(map[string]agent.RunStatus),
		entered:  make(chan string, 32),
	}
}

// setText 覆盖指定角色的最终文本。
func (f *fakeAnalysisAgent) setText(roleType domain.RoleType, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts[string(roleType)] = text
}

// setError 让指定角色的调用返回错误。
func (f *fakeAnalysisAgent) setError(roleType domain.RoleType, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[string(roleType)] = err
}

// setStatus 覆盖指定角色的执行状态。
func (f *fakeAnalysisAgent) setStatus(roleType domain.RoleType, status agent.RunStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[string(roleType)] = status
}

// RunRole 记录一次调用并按配置返回结果。
func (f *fakeAnalysisAgent) RunRole(
	ctx context.Context,
	request app.RunPlanningRoleRequest,
) (app.PlanningAgentResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	text := f.texts[request.RoleType]
	runErr := f.errs[request.RoleType]
	status := f.statuses[request.RoleType]
	hold := f.hold
	f.mu.Unlock()
	atomic.AddInt32(&f.calls, 1)
	select {
	case f.entered <- request.RoleType:
	default:
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return f.buildResult(request, text, agent.RunCancelled), ctx.Err()
		}
	}
	if runErr != nil {
		return f.buildResult(request, text, agent.RunFailed), runErr
	}
	if status == "" {
		status = agent.RunCompleted
	}
	return f.buildResult(request, text, status), nil
}

// buildResult 构造一次调用的执行结果。
func (f *fakeAnalysisAgent) buildResult(
	request app.RunPlanningRoleRequest,
	text string,
	status agent.RunStatus,
) app.PlanningAgentResult {
	roleType := domain.RoleType(request.RoleType)
	return app.PlanningAgentResult{
		Role:          domain.Role{ID: domain.ID(request.RoleType), Type: roleType},
		RoleType:      roleType,
		RoleSessionID: "role-session-" + request.RoleType,
		Result: agent.RunResult{
			Status:    status,
			FinalText: text,
			ProcessID: 4242,
			Session: agent.SessionFiles{
				Completeness: agent.SessionComplete,
			},
		},
	}
}

// callsFor 返回指定角色被调用的次数。
func (f *fakeAnalysisAgent) callsFor(roleType domain.RoleType) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if request.RoleType == string(roleType) {
			count++
		}
	}
	return count
}

// roleTypes 按调用顺序返回参与的角色类型。
func (f *fakeAnalysisAgent) roleTypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	types := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		types = append(types, request.RoleType)
	}
	return types
}

// requestAt 返回第 index 次调用收到的请求。
func (f *fakeAnalysisAgent) requestAt(t *testing.T, index int) app.RunPlanningRoleRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index >= len(f.requests) {
		t.Fatalf("第 %d 次调用的请求不存在，实际记录 %d 次", index+1, len(f.requests))
	}
	return f.requests[index]
}

// waitEntered 等待指定数量进入 RunRole 的调用。
func (f *fakeAnalysisAgent) waitEntered(t *testing.T, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case <-f.entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("等待第 %d 次规划角色调用进入执行超时", index+1)
		}
	}
}

// holdCalls 让后续调用在 releaseHold 之前一直阻塞。
func (f *fakeAnalysisAgent) holdCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hold == nil {
		f.hold = make(chan struct{})
	}
}

// releaseHold 放行被阻塞的调用；重复调用不会重复关闭通道。
func (f *fakeAnalysisAgent) releaseHold() {
	f.mu.Lock()
	hold := f.hold
	f.hold = nil
	f.mu.Unlock()
	if hold != nil {
		close(hold)
	}
}

// analysisFixture 是一套装配好两阶段分析服务的测试现场。
type analysisFixture struct {
	db       *sqlite.DB
	ctx      context.Context
	database string
	project  domain.Project
	sessions *app.PlanningSessionService
	analysis *app.PlanningAnalysisService
	agent    *fakeAnalysisAgent
}

// analysisFixtureOptions 允许用例替换存储与角色调用能力。
type analysisFixtureOptions struct {
	// store 覆盖默认存储，用于模拟持久化失败。
	store storage.Store
	// agent 覆盖默认替身。
	agent *fakeAnalysisAgent
	// holdAgent 让替身在构造后立即阻塞调用。
	holdAgent bool
}

// newAnalysisFixture 在测试工作空间中准备项目与两阶段分析服务。
func newAnalysisFixture(t *testing.T, options analysisFixtureOptions) *analysisFixture {
	t.Helper()
	workspace := testkit.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: workspace.DatabasePath()})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("关闭测试数据库失败: %v", closeErr)
		}
	})
	if _, err := db.SeedRoles(ctx, app.RoleSeeds()); err != nil {
		t.Fatalf("初始化固定角色失败: %v", err)
	}
	project, err := domain.NewProject(domain.ProjectInput{
		ID:                 analysisProjectID,
		Name:               "规划分析项目",
		RepositoryURL:      "ssh://git.example.com/team/analysis-repo",
		BaseBranch:         "main",
		Environment:        domain.EnvironmentRef{Type: domain.EnvironmentTypeLanguage, Name: "go"},
		EnvironmentVersion: analysisVersionLabel,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("构造项目失败: %v", err)
	}
	if err := db.CreateProject(ctx, project); err != nil {
		t.Fatalf("写入项目失败: %v", err)
	}
	sessions, err := app.NewPlanningSessionService(app.PlanningSessionServiceOptions{Store: db})
	if err != nil {
		t.Fatalf("构造规划会话服务失败: %v", err)
	}
	agent := options.agent
	if agent == nil {
		agent = newFakeAnalysisAgent()
	}
	if options.holdAgent {
		agent.holdCalls()
	}
	store := options.store
	if store == nil {
		store = db
	}
	analysis, err := app.NewPlanningAnalysisService(app.PlanningAnalysisServiceOptions{
		Store: store,
		Agent: agent,
	})
	if err != nil {
		t.Fatalf("构造规划分析服务失败: %v", err)
	}
	return &analysisFixture{
		db:       db,
		ctx:      ctx,
		database: workspace.DatabasePath(),
		project:  project,
		sessions: sessions,
		analysis: analysis,
		agent:    agent,
	}
}

// analyzingSession 创建一个已提交需求并进入 analyzing 的规划会话。
func (f *analysisFixture) analyzingSession(t *testing.T) domain.PlanningSession {
	t.Helper()
	session, err := f.sessions.Create(f.ctx, app.CreatePlanningSessionRequest{ProjectID: f.project.ID})
	if err != nil {
		t.Fatalf("创建规划会话失败: %v", err)
	}
	start, err := f.sessions.SubmitMessage(f.ctx, session.ID.String(), app.SubmitPlanningMessageRequest{
		Content: analysisRequirement,
	})
	if err != nil {
		t.Fatalf("提交规划消息失败: %v", err)
	}
	if start.Session.Status != domain.PlanningSessionAnalyzing {
		t.Fatalf("提交消息后状态应为 analyzing，实际 %s", start.Session.Status)
	}
	return start.Session
}

// loadSession 重新读取规划会话的持久化事实。
func (f *analysisFixture) loadSession(t *testing.T, sessionID domain.ID) domain.PlanningSession {
	t.Helper()
	session, err := f.db.GetPlanningSession(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("读取规划会话失败: %v", err)
	}
	return *session
}

// revisions 返回会话已持久化的全部方案版本。
func (f *analysisFixture) revisions(t *testing.T, sessionID domain.ID) []domain.PlanningRevision {
	t.Helper()
	revisions, err := f.db.ListPlanningRevisions(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("查询规划版本失败: %v", err)
	}
	return revisions
}

// messages 返回会话已持久化的全部消息。
func (f *analysisFixture) messages(t *testing.T, sessionID domain.ID) []domain.PlanningMessage {
	t.Helper()
	messages, err := f.db.ListPlanningMessages(f.ctx, sessionID)
	if err != nil {
		t.Fatalf("查询规划消息失败: %v", err)
	}
	return messages
}

// countRows 统计指定表的行数，用于核验"没有创建正式实体"。
func (f *analysisFixture) countRows(t *testing.T, table string) int {
	t.Helper()
	raw := testkit.OpenSQLite(t, f.db.Path())
	var count int
	statement := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if err := raw.QueryRow(statement).Scan(&count); err != nil {
		t.Fatalf("统计 %s 行数失败: %v", table, err)
	}
	return count
}

// failingRevisionStore 让追加方案版本失败，用于验证"没有半成品成功状态"。
//
// 它只覆盖一个写入方法，其余能力全部委托给真实存储：这样事务边界、回滚与条件更新仍由
// 真实 SQLite 执行，失败点则精确落在 Revision 写入上。
type failingRevisionStore struct {
	storage.Store
}

// WithTx 在真实事务内用失败替身覆盖方案版本写入。
func (s failingRevisionStore) WithTx(
	ctx context.Context,
	fn func(ctx context.Context, tx storage.Tx) error,
) error {
	return s.Store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		return fn(ctx, failingRevisionTx{Tx: tx})
	})
}

// failingRevisionTx 只让追加方案版本失败。
type failingRevisionTx struct {
	storage.Tx
}

// AppendPlanningRevision 模拟方案版本写入失败。
func (t failingRevisionTx) AppendPlanningRevision(
	ctx context.Context,
	request storage.AppendPlanningRevisionRequest,
) (domain.PlanningRevision, error) {
	return domain.PlanningRevision{}, fmt.Errorf("模拟方案版本写入失败")
}
