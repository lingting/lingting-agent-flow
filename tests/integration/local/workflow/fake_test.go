// Package workflow_test 验证任务级工作流编排在真实 SQLite 上的重启恢复行为。
//
// 用例使用真实数据库文件与真实的 app.TaskWorkflowService，只把外部依赖（Agent
// 执行、容器核验、步骤闭环）替换为受控替身：恢复链路本身、事务边界、状态机与
// 持久化写入全部走产品代码，因此断言的是数据库里真实留下的现场，而不是方法的
// 返回值。
package workflow_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// errUnexpectedStepRun 表示恢复流程错误地进入了步骤执行。
//
// 它必须让用例失败：启动恢复只收敛持久化现场，不得重放任何 Agent 工作。
var errUnexpectedStepRun = errors.New("恢复流程不应执行步骤闭环")

// 计划生成用例使用的合法计划输出。
const validPlanJSON = `{"summary":"分两步实现需求","steps":[` +
	`{"id":"step-1","title":"实现接口","objective":"按需求实现接口",` +
	`"instructions":"只修改当前工作区","acceptance_criteria":["接口可以编译"]},` +
	`{"id":"step-2","title":"补齐测试","objective":"为接口补齐测试",` +
	`"instructions":"不要修改构建脚本","acceptance_criteria":["测试可以运行"]}]}`

// agentReply 是一次受控 Agent 调用的结果。
type agentReply struct {
	status    agent.RunStatus
	finalText string
	failure   string
	exitCode  int
	// duringCall 在返回结果之前被调用，用于在"调用进行中"触发服务停止等动作。
	//
	// 它在独立 goroutine 中执行：调用方此时仍在 RunAgent 内部，同步等待会死锁。
	duringCall func()
}

// fakeContainer 是容器核验与容器内 Agent 调用的替身。
//
// 它不写数据库、不启动进程：只返回受控结果并记录调用次数，使"是否重放了 Planner"
// 这类断言可以直接比较次数。
type fakeContainer struct {
	mu         sync.Mutex
	running    bool
	inspectErr error
	calls      int
	requests   []app.ContainerAgentRequest
	// replies 是依次返回的受控结果；由 mu 保护，可在调用进行中安全设置。
	replies []agentReply
}

// InspectContainer 返回受控的容器状态。
func (f *fakeContainer) InspectContainer(
	ctx context.Context,
	request app.InspectContainerRequest,
) (app.ContainerDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return app.ContainerDetail{}, f.inspectErr
	}
	return app.ContainerDetail{
		Container: domain.Container{
			ID:       domain.ID(request.ContainerID),
			HostID:   "host-1",
			Name:     "agentflow-workflow",
			DockerID: "docker-" + request.ContainerID,
			State:    domain.ContainerStateRunning,
		},
		Inspect: &docker.ContainerInspect{Running: f.running},
	}, nil
}

// RunAgent 返回受控的 Agent 调用结果。
func (f *fakeContainer) RunAgent(
	ctx context.Context,
	request app.ContainerAgentRequest,
) (app.ContainerAgentResult, error) {
	f.mu.Lock()
	index := f.calls
	f.calls++
	f.requests = append(f.requests, request)
	reply := agentReply{status: agent.RunCompleted, finalText: validPlanJSON}
	if index < len(f.replies) {
		reply = f.replies[index]
	}
	f.mu.Unlock()
	if reply.duringCall != nil {
		go reply.duringCall()
	}
	now := time.Now().UTC()
	sessionID := domain.ID(request.SessionID)
	if sessionID.IsEmpty() {
		sessionID = domain.NewID()
	}
	result := agent.RunResult{
		SessionID:    sessionID.String(),
		InvocationID: request.InvocationID,
		Status:       reply.status,
		FinalText:    reply.finalText,
		Failure:      reply.failure,
		ExitCode:     reply.exitCode,
		StartedAt:    now,
		FinishedAt:   now,
		Session: agent.SessionFiles{
			JSONLPath:    "sessions/" + sessionID.String() + "/session.jsonl",
			Completeness: agent.SessionComplete,
			ExportStatus: agent.ExportNotAttempted,
		},
	}
	if reply.status != agent.RunCompleted {
		result.Session.Completeness = agent.SessionPartial
	}
	return app.ContainerAgentResult{Result: result}, nil
}

// setReplies 设置后续调用的受控结果。
func (f *fakeContainer) setReplies(replies ...agentReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = replies
}

// callCount 返回 Agent 调用次数。
func (f *fakeContainer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRunner 是单步骤开发—验证闭环的替身。
//
// 恢复用例不进入步骤执行，因此它只需要满足接口并记录是否被调用：任何一次调用都
// 意味着恢复流程重放了 Agent 工作。
type fakeRunner struct {
	mu    sync.Mutex
	calls int
}

// ExecuteAndValidate 记录调用并返回失败：恢复流程不应走到这里。
func (r *fakeRunner) ExecuteAndValidate(
	ctx context.Context,
	request app.ExecuteAndValidateRequest,
) (app.ExecuteAndValidateResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return app.ExecuteAndValidateResult{}, errUnexpectedStepRun
}

// callCount 返回闭环调用次数。
func (r *fakeRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// failingStore 包装真实存储，只把指定方法替换为可控失败。
//
// 失败来自存储层而不是业务代码，因此验证的是真实的错误传播与"不得报告恢复完成"
// 的语义。
type failingStore struct {
	app.TaskWorkflowStore
	listStepsErr  error
	updateTaskErr error
	activeRunErr  error
	listRunsErr   error
	updateRunErr  error
}

// ListStepsByTask 在配置了错误时返回该错误。
func (s *failingStore) ListStepsByTask(
	ctx context.Context,
	taskID domain.ID,
) ([]domain.Step, error) {
	if s.listStepsErr != nil {
		return nil, s.listStepsErr
	}
	return s.TaskWorkflowStore.ListStepsByTask(ctx, taskID)
}

// UpdateTaskIfStatus 在配置了错误时返回该错误。
func (s *failingStore) UpdateTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
) error {
	if s.updateTaskErr != nil {
		return s.updateTaskErr
	}
	return s.TaskWorkflowStore.UpdateTaskIfStatus(ctx, task, expected)
}

// GetActiveWorkflowRun 在配置了错误时返回该错误。
func (s *failingStore) GetActiveWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	if s.activeRunErr != nil {
		return nil, s.activeRunErr
	}
	return s.TaskWorkflowStore.GetActiveWorkflowRun(ctx, taskID)
}

// ListUnfinishedWorkflowRuns 在配置了错误时返回该错误。
func (s *failingStore) ListUnfinishedWorkflowRuns(
	ctx context.Context,
) ([]domain.WorkflowRun, error) {
	if s.listRunsErr != nil {
		return nil, s.listRunsErr
	}
	return s.TaskWorkflowStore.ListUnfinishedWorkflowRuns(ctx)
}

// UpdateWorkflowRun 在配置了错误时返回该错误。
func (s *failingStore) UpdateWorkflowRun(
	ctx context.Context,
	run domain.WorkflowRun,
) error {
	if s.updateRunErr != nil {
		return s.updateRunErr
	}
	return s.TaskWorkflowStore.UpdateWorkflowRun(ctx, run)
}
