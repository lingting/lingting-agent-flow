// Package workflow_test 验证统一启动在真实持久化现场上的完整主链路。
//
// 本文件覆盖 P1-M1 的完成定义：一次启动从"容器已就绪"经任务级工作区初始化与
// Planner 计划生成，最终到达"任务为 planned 且计划步骤已落库"。用例使用真实
// SQLite、真实 app.TaskLaunchService、真实 app.TaskWorkspaceService 与真实
// app.TaskWorkflowService，只把容器内 Git 与容器内 Pi 调用替换为受控替身，
// 因此断言的是数据库里真实留下的现场，而不是方法的返回值。
package workflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// chainPlanJSON 是 Planner 替身返回的合法计划输出。
const chainPlanJSON = `{"summary":"分两步实现需求","steps":[` +
	`{"id":"step-1","title":"实现接口","objective":"按需求实现接口",` +
	`"instructions":"只修改当前工作区","acceptance_criteria":["接口可以编译"]},` +
	`{"id":"step-2","title":"补齐测试","objective":"为接口补齐测试",` +
	`"instructions":"不要修改构建脚本","acceptance_criteria":["测试可以运行"]}]}`

// 链路用例使用的固定 commit 与容器标识。
const (
	chainContainerID = "container-chain"
	chainDockerID    = "docker-container-chain"
	chainBaseSHA     = "1111111111111111111111111111111111111111"
	chainHeadSHA     = "2222222222222222222222222222222222222222"
)

// chainContainers 同时满足容器核验、容器内代码获取与容器内 Agent 调用三个端口。
//
// 它不写数据库、不启动进程：只返回受控结果并记录调用次数，使"是否重复 Clone"、
// "是否重复调用 Planner"这类断言可以直接比较次数。
type chainContainers struct {
	mu sync.Mutex
	// running 表示容器核验结论中的运行状态。
	running bool
	// inspectCalls 统计容器核验次数。
	inspectCalls int
	// fetchCalls 统计容器内代码获取次数。
	fetchCalls int
	// agentCalls 统计容器内 Agent 调用次数。
	agentCalls int
	// finalText 是 Agent 调用返回的最终文本。
	finalText string
	// fetchErr 是代码获取返回的错误，用于构造真实的工作区阶段失败。
	fetchErr error
}

// InspectContainer 返回受控的容器状态。
func (c *chainContainers) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inspectCalls++
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			DockerID: chainDockerID,
			Name:     "agentflow-chain",
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: c.running},
	}, nil
}

// FetchSourceWith 返回一次受控的成功代码获取。
func (c *chainContainers) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	c.mu.Lock()
	c.fetchCalls++
	fetchErr := c.fetchErr
	c.mu.Unlock()
	now := time.Now().UTC()
	if fetchErr != nil {
		// 失败结果同样带上阶段与摘要：记录据此区分"克隆失败"与"结果不确定"。
		return source.FetchResult{
			ProjectID:    request.ProjectID,
			TaskID:       request.TaskID,
			SourceType:   domain.CodeSourceGit,
			ContainerID:  containerID,
			DockerID:     chainDockerID,
			WorkDir:      request.WorkDir,
			Stage:        domain.CodeFetchStageClone,
			ErrorCode:    source.CodeRepositoryAccessFailed.String(),
			ErrorSummary: "无法访问代码仓库",
			StartedAt:    now,
			FinishedAt:   now,
		}, fetchErr
	}
	return source.FetchResult{
		ProjectID:        request.ProjectID,
		TaskID:           request.TaskID,
		AttemptID:        request.AttemptID,
		SourceType:       domain.CodeSourceGit,
		Repository:       "https://git.example.com/group/repo.git",
		ContainerID:      containerID,
		DockerID:         chainDockerID,
		WorkDir:          request.WorkDir,
		BaseBranch:       "main",
		BaseSelection:    domain.GitBranchSelectionOriginHead,
		BaseCommit:       chainBaseSHA,
		TaskBranch:       "agent/" + request.TaskID,
		TaskBranchSource: domain.TaskBranchSourceNew,
		Commit:           chainHeadSHA,
		Stage:            domain.CodeFetchStageVerify,
		StartedAt:        now,
		FinishedAt:       now,
	}, nil
}

// RunAgent 返回受控的 Planner 输出。
func (c *chainContainers) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	c.mu.Lock()
	c.agentCalls++
	finalText := c.finalText
	c.mu.Unlock()
	now := time.Now().UTC()
	sessionID := domain.ID(request.SessionID)
	if sessionID.IsEmpty() {
		sessionID = domain.NewID()
	}
	return app.ContainerAgentResult{Result: agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: request.InvocationID,
		Status:       agent.RunCompleted,
		FinalText:    finalText,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/" + sessionID.String() + "/session.jsonl",
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportNotAttempted,
		},
	}}, nil
}

// calls 返回三类调用的次数快照。
func (c *chainContainers) calls() (inspect, fetch, agentCalls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inspectCalls, c.fetchCalls, c.agentCalls
}

// chainWorkflowStub 是工作流启动的受控替身。
//
// 链路用例只关心"启动是否推进到计划就绪"，因此编排器用替身：真实编排器会在后台
// 执行步骤，与用例对计划现场的断言产生竞态。
type chainWorkflowStub struct {
	mu    sync.Mutex
	calls int
}

// StartWorkflow 记录调用并返回一条运行中的执行记录。
func (w *chainWorkflowStub) StartWorkflow(
	ctx context.Context,
	request app.StartWorkflowRequest,
) (app.WorkflowRunView, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	return app.WorkflowRunView{
		RunID:  domain.NewID(),
		TaskID: domain.ID(request.TaskID),
		Status: domain.WorkflowRunRunning,
		Stage:  domain.WorkflowStageStepExecution,
	}, nil
}

// callCount 返回工作流启动次数。
func (w *chainWorkflowStub) callCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// chainServices 是链路用例装配出的一组真实服务。
type chainServices struct {
	launch    *app.TaskLaunchService
	workflow  *app.TaskWorkflowService
	workspace *app.TaskWorkspaceService
}

// newChainServices 用真实服务装配启动链路，只替换容器内 Git 与 Pi 调用。
func (f *launchFixture) newChainServices(
	t *testing.T,
	containers *chainContainers,
	workflowPort app.TaskLaunchWorkflowPort,
) *chainServices {
	t.Helper()
	workspace, err := app.NewTaskWorkspaceService(app.TaskWorkspaceOptions{
		Store:      f.db,
		Containers: containers,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造工作区初始化服务失败: %v", err)
	}
	sessions, err := app.NewSessionService(
		f.db,
		testkit.New(t).SessionRoot(),
		f.logger.Slog(),
	)
	if err != nil {
		t.Fatalf("构造会话服务失败: %v", err)
	}
	workflow, err := app.NewTaskWorkflowService(app.TaskWorkflowOptions{
		Store:      f.db,
		Steps:      &fakeRunner{},
		Containers: containers,
		Sessions:   sessions,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造任务级编排器失败: %v", err)
	}
	service, err := app.NewTaskLaunchService(app.TaskLaunchOptions{
		Store:      f.db,
		Planner:    workflow,
		Workspace:  workspace,
		Workflow:   workflowPort,
		Containers: containers,
		Logger:     f.logger.Slog(),
	})
	if err != nil {
		t.Fatalf("构造启动服务失败: %v", err)
	}
	if err := service.Start(f.ctx); err != nil {
		t.Fatalf("启动启动服务失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := service.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止启动服务失败: %v", stopErr)
		}
	})
	if err := workflow.Start(f.ctx); err != nil {
		t.Fatalf("启动任务级编排器失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := workflow.Stop(stopCtx); stopErr != nil {
			t.Errorf("停止任务级编排器失败: %v", stopErr)
		}
	})
	return &chainServices{launch: service, workflow: workflow, workspace: workspace}
}

// steps 读取任务当前已持久化的计划步骤。
func (f *launchFixture) steps(t *testing.T) []domain.Step {
	t.Helper()
	steps, err := f.db.ListStepsByTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("读取步骤失败: %v", err)
	}
	return steps
}

// TestLaunchChainReachesPlannedWithPersistedPlan 校验一次启动经工作区初始化与
// 计划生成到达 planned，且完整计划已落库。
func TestLaunchChainReachesPlannedWithPersistedPlan(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.attachContainer(t, chainContainerID)
	containers := &chainContainers{running: true, finalText: chainPlanJSON}
	workflowPort := &chainWorkflowStub{}
	services := fixture.newChainServices(t, containers, workflowPort)

	result, err := services.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if !result.PlanPersisted {
		t.Fatalf("计划应已落库: %+v", result)
	}
	// 断言数据库里的真实事实：任务状态与计划步骤都来自持久化记录。
	stored, loadErr := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if loadErr != nil {
		t.Fatalf("读取任务失败: %v", loadErr)
	}
	if stored.Status != domain.TaskStatusPlanned {
		t.Fatalf("任务状态应为 planned，实际 %s", stored.Status)
	}
	steps := fixture.steps(t)
	// 计划固定以规划阶段步骤开头，其后是 Planner 生成的开发步骤。
	if len(steps) != 3 {
		t.Fatalf("计划步骤数量不符: %d", len(steps))
	}
	if steps[0].Type != domain.StepTypePlan {
		t.Fatalf("首个步骤必须是规划阶段: %s", steps[0].Type)
	}
	for index, step := range steps {
		if step.Sequence != index+1 {
			t.Fatalf("步骤序号不连续: %d", step.Sequence)
		}
		if index == 0 {
			continue
		}
		if step.Type != domain.StepTypeDevelop {
			t.Fatalf("后续步骤必须是开发步骤: %s", step.Type)
		}
		if !step.VerificationSpec.HasCommands() {
			t.Fatalf("任务级必需命令未合并进步骤 %d", step.Sequence)
		}
	}
	// 工作区初始化必须留下成功记录，且带可核验 commit。
	records, listErr := fixture.db.ListTaskWorkspaceInitsByTask(
		fixture.ctx,
		fixture.task.ID.String(),
	)
	if listErr != nil {
		t.Fatalf("读取工作区初始化记录失败: %v", listErr)
	}
	if len(records) != 1 {
		t.Fatalf("工作区初始化记录数量不符: %d", len(records))
	}
	if records[0].Status != domain.TaskWorkspaceInitStatusSucceeded {
		t.Fatalf("工作区初始化应成功，实际 %s", records[0].Status)
	}
	if records[0].CommitSHA != chainHeadSHA {
		t.Fatalf("工作区 commit 不符: %s", records[0].CommitSHA)
	}
	if records[0].ContainerID != chainContainerID {
		t.Fatalf("工作区记录未关联当前容器: %s", records[0].ContainerID)
	}
	inspect, fetch, agentCalls := containers.calls()
	if inspect == 0 {
		t.Fatal("启动必须核验容器可用性")
	}
	if fetch != 1 {
		t.Fatalf("工作区初始化应执行一次代码获取，实际 %d 次", fetch)
	}
	if agentCalls != 1 {
		t.Fatalf("计划生成应调用一次 Planner，实际 %d 次", agentCalls)
	}
	if workflowPort.callCount() != 1 {
		t.Fatalf("计划就绪后应启动一次编排，实际 %d 次", workflowPort.callCount())
	}
}

// TestLaunchChainIsIdempotentOnRepeat 校验重复启动不重复 Clone、不覆盖计划，
// 且如实报告计划已持久化。
func TestLaunchChainIsIdempotentOnRepeat(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.attachContainer(t, chainContainerID)
	containers := &chainContainers{running: true, finalText: chainPlanJSON}
	workflowPort := &chainWorkflowStub{}
	services := fixture.newChainServices(t, containers, workflowPort)

	first, err := services.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	if !first.PlanPersisted {
		t.Fatalf("首次启动应报告计划已落库: %+v", first)
	}
	planned := fixture.steps(t)

	// 第二次启动：工作区已就绪、计划已持久化，因此不得重复执行外部操作。
	second, err := services.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err != nil {
		t.Fatalf("重复启动失败: %v", err)
	}
	if second.Stage != domain.TaskLaunchStageWorkflow {
		t.Fatalf("重复启动阶段应为 workflow，实际 %s", second.Stage)
	}
	// 计划早已落库：本次没有执行计划生成，但计划确实是已持久化的，必须如实报告。
	if !second.PlanPersisted {
		t.Fatalf("计划已存在时必须报告已持久化: %+v", second)
	}
	_, fetch, agentCalls := containers.calls()
	if fetch != 1 {
		t.Fatalf("工作区已就绪时不得重复 Clone，实际 %d 次", fetch)
	}
	if agentCalls != 1 {
		t.Fatalf("计划已存在时不得重复调用 Planner，实际 %d 次", agentCalls)
	}
	// 计划不得被覆盖：步骤标识与内容保持首次结果。
	replayed := fixture.steps(t)
	if len(replayed) != len(planned) {
		t.Fatalf("计划步骤数量被改写: %d != %d", len(replayed), len(planned))
	}
	for index := range planned {
		if replayed[index].ID != planned[index].ID {
			t.Fatalf("第 %d 个步骤标识被改写: %s != %s", index, replayed[index].ID, planned[index].ID)
		}
	}
}

// TestLaunchChainStopsOnWorkspaceFailure 校验工作区失败后不再生成计划。
func TestLaunchChainStopsOnWorkspaceFailure(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.attachContainer(t, chainContainerID)
	containers := &chainContainers{running: true, finalText: chainPlanJSON}
	// 代码获取失败：工作区初始化必须停在该阶段，不得继续生成计划，也不得
	// 退化为在宿主机执行。
	containers.fetchErr = &source.Error{
		Code:   source.CodeRepositoryAccessFailed,
		Op:     "git clone",
		Detail: "无法访问代码仓库",
	}
	workflowPort := &chainWorkflowStub{}
	services := fixture.newChainServices(t, containers, workflowPort)

	result, err := services.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if err == nil {
		t.Fatal("代码获取失败时启动必须失败")
	}
	if result.FailureStage != domain.TaskLaunchStageWorkspace.String() {
		t.Fatalf("失败阶段应为 workspace，实际 %s", result.FailureStage)
	}
	_, fetch, agentCalls := containers.calls()
	if fetch != 1 {
		t.Fatalf("工作区阶段应尝试一次代码获取，实际 %d 次", fetch)
	}
	if agentCalls != 0 {
		t.Fatalf("工作区失败后不得调用 Planner，实际 %d 次", agentCalls)
	}
	if workflowPort.callCount() != 0 {
		t.Fatalf("工作区失败后不得启动编排，实际 %d 次", workflowPort.callCount())
	}
	if len(fixture.steps(t)) != 0 {
		t.Fatal("工作区失败后不得留下计划步骤")
	}
	stored, loadErr := fixture.db.GetTask(fixture.ctx, fixture.task.ID)
	if loadErr != nil {
		t.Fatalf("读取任务失败: %v", loadErr)
	}
	if stored.Status == domain.TaskStatusPlanned {
		t.Fatal("工作区失败时任务不得进入 planned")
	}
}

// TestLaunchChainRejectsUncertainWorkspaceWithoutReplay 校验工作区结果不确定时
// 启动停止且不重放代码获取。
func TestLaunchChainRejectsUncertainWorkspaceWithoutReplay(t *testing.T) {
	t.Parallel()
	fixture := newLaunchFixture(t)
	fixture.attachContainer(t, chainContainerID)
	// 直接写入一条 uncertain 记录：等价于上一次远端操作结果无法确认。
	record, recordErr := domain.NewTaskWorkspaceInitRecord(
		"",
		fixture.task.ID.String(),
		chainContainerID,
		chainDockerID,
		domain.CodeSourceGit,
		domain.DefaultContainerWorkDir,
		fixture.now,
	)
	if recordErr != nil {
		t.Fatalf("构造工作区初始化记录失败: %v", recordErr)
	}
	if markErr := record.MarkRunning(domain.CodeFetchStageClone, fixture.now); markErr != nil {
		t.Fatalf("标记运行失败: %v", markErr)
	}
	if markErr := record.MarkFailed(
		domain.TaskWorkspaceInitStatusUncertain,
		domain.CodeFetchStageClone,
		"state_uncertain",
		"远端执行结果无法确认",
		fixture.now,
	); markErr != nil {
		t.Fatalf("标记不确定失败: %v", markErr)
	}
	if createErr := fixture.db.CreateTaskWorkspaceInit(fixture.ctx, record); createErr != nil {
		t.Fatalf("写入记录失败: %v", createErr)
	}

	containers := &chainContainers{running: true, finalText: chainPlanJSON}
	workflowPort := &chainWorkflowStub{}
	services := fixture.newChainServices(t, containers, workflowPort)

	_, err := services.launch.Launch(fixture.ctx, app.TaskLaunchRequest{
		TaskID: fixture.task.ID.String(),
	})
	if !errors.Is(err, app.ErrTaskLaunchConflict) {
		t.Fatalf("工作区结果不确定时必须返回状态冲突，实际 %v", err)
	}
	_, fetch, agentCalls := containers.calls()
	if fetch != 0 {
		t.Fatalf("结果不确定时不得重放代码获取，实际 %d 次", fetch)
	}
	if agentCalls != 0 {
		t.Fatalf("结果不确定时不得调用 Planner，实际 %d 次", agentCalls)
	}
	if workflowPort.callCount() != 0 {
		t.Fatalf("结果不确定时不得启动编排，实际 %d 次", workflowPort.callCount())
	}
	// 现场必须保留：记录仍是 uncertain，且历史记录不被删除。
	records, listErr := fixture.db.ListTaskWorkspaceInitsByTask(
		fixture.ctx,
		fixture.task.ID.String(),
	)
	if listErr != nil {
		t.Fatalf("读取工作区初始化记录失败: %v", listErr)
	}
	if len(records) != 1 || records[0].Status != domain.TaskWorkspaceInitStatusUncertain {
		t.Fatalf("不确定现场必须原样保留: %+v", records)
	}
}
