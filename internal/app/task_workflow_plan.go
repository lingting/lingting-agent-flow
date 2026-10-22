package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 计划生成的默认参数。
const (
	// defaultPlanTimeout 是未配置超时时的单次 Planner 调用超时。
	//
	// 它与任务级 agent_timeout_seconds 的区别是"没有任务配置可用"时的兜底值：
	// 任何情况下都不允许一次规划调用无限期地占用任务。
	defaultPlanTimeout = 30 * time.Minute
	// maxPlanPrompt 是调用方补充提示词的长度上限。
	maxPlanPrompt = 64 << 10
)

// GeneratePlanRequest 是通过接口发起一次计划生成的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务与可选的执行参数，
// 不能指定 Session 路径、工作目录、命令行参数或角色覆盖。
type GeneratePlanRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// Prompt 是可选的补充提示词。
	//
	// 非空时只作为补充说明追加在编排器生成的上下文之后，不能替换任务目标、
	// 执行环境约束与输出格式要求。
	Prompt string
	// Timeout 是本次 Planner 调用的超时时间，零值使用任务配置或服务默认值。
	Timeout time.Duration
}

// PlanGenerationResult 是一次计划生成的结果。
//
// 它如实区分四件独立的事实：任务最终状态、是否已把计划写入数据库、生成的步骤、
// 以及失败发生在哪个阶段。调用方不需要再查执行记录就能判断计划是否可用。
type PlanGenerationResult struct {
	TaskID domain.ID
	// TaskStatus 是收尾后读取到的任务真实状态。
	TaskStatus domain.TaskStatus
	// Summary 是 Planner 给出的一句话计划摘要，只写入事件负载。
	Summary string
	// Steps 是已持久化的计划步骤，按序号升序排列。
	Steps []domain.Step
	// SessionID 是本次 Planner 调用的会话标识，调用失败时同样存在。
	SessionID domain.ID
	// Persisted 表示计划是否已完整写入数据库。
	//
	// 为 false 时任务不会处于 planned，且数据库中不应存在部分步骤。
	Persisted bool
	// FailureStage 是失败发生的阶段名，成功时为空。
	FailureStage string
	// Failure 是失败原因摘要，成功时为空。
	Failure string
	// StartedAt / FinishedAt 是本次调用的起止时间。
	StartedAt  time.Time
	FinishedAt time.Time
}

// GeneratePlan 在任务容器内调用 Planner 生成结构化计划并持久化。
//
// 语义固定为：
//   - 只接受状态允许规划、没有已保存计划、没有未结束工作流的任务；
//   - 先以任务状态条件更新进入 planning，这是跨进程的并发闸门；
//   - Planner 调用使用独立 Session，会话记录在进程启动之前写入；
//   - 输出必须解析为结构化计划并通过确定性校验，否则任务不会进入 planned；
//   - 计划步骤与任务状态在同一事务内写入，不会留下"计划已保存但状态未推进"的半成品；
//   - 失败时按真实结论收敛任务状态，不重试、不自动修复、不删除容器。
func (s *TaskWorkflowService) GeneratePlan(
	ctx context.Context,
	request GeneratePlanRequest,
) (PlanGenerationResult, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	prompt, err := s.validatePlanOptions(request.Prompt)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	if s.sessions == nil {
		return PlanGenerationResult{}, fmt.Errorf(
			"%w: 本次运行未提供会话服务，无法记录 Planner 执行会话",
			ErrWorkflowPrerequisite,
		)
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	steps, err := s.loadSteps(ctx, task.ID)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	if err := domain.CheckPlanGenerationStart(task, steps); err != nil {
		return PlanGenerationResult{}, s.classifyStartError(err)
	}
	// 未结束的工作流正在推进同一任务的计划与步骤：规划不能与之竞争同一份现场。
	if _, runErr := s.store.GetActiveWorkflowRun(ctx, task.ID); runErr == nil {
		return PlanGenerationResult{}, fmt.Errorf(
			"%w: 任务 %s 已有未结束的工作流执行，不能同时生成计划",
			ErrWorkflowConflict,
			task.ID,
		)
	} else if !errors.Is(runErr, storage.ErrNotFound) {
		return PlanGenerationResult{}, fmt.Errorf("读取工作流执行记录失败: %w", runErr)
	}
	if err := s.checkHostReady(ctx, task); err != nil {
		return PlanGenerationResult{}, err
	}
	if err := s.checkContainerReady(ctx, task); err != nil {
		return PlanGenerationResult{}, err
	}
	role, err := s.loadPlannerRole(ctx, task)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	// 开发步骤必须绑定开发角色：计划里的每个 develop 步骤都会由该角色执行，
	// 因此角色在计划生成阶段就确定下来，之后不再变化。
	developerRole, err := s.loadDeveloperRole(ctx, task)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	// 进程内以任务为键拒绝重复请求；跨进程并发仍由下面的状态条件更新拒绝。
	unlock, acquired := s.taskLocks.tryLock(task.ID.String())
	if !acquired {
		return PlanGenerationResult{}, fmt.Errorf(
			"%w: 任务 %s 正在生成计划",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	defer unlock()

	runCtx, finishRun, runErr := s.beginRun(ctx)
	if runErr != nil {
		return PlanGenerationResult{}, runErr
	}
	defer finishRun()

	// 进入 planning 必须落库：写入失败时不调用 Planner，避免出现"真实调用过但
	// 数据库里没有任何痕迹"的情况。
	planning, err := s.beginPlanning(runCtx, task)
	if err != nil {
		return PlanGenerationResult{}, err
	}
	result := PlanGenerationResult{
		TaskID:     planning.ID,
		TaskStatus: planning.Status,
		StartedAt:  s.now(),
	}
	prompt, err = planPrompt(planning, prompt)
	if err != nil {
		return s.failPlanGeneration(context.WithoutCancel(ctx), planning, result, "prompt", err)
	}
	timeout := s.planTimeout(planning, request.Timeout)
	outcome, callErr := s.callPlanner(runCtx, planning, role, prompt, timeout)
	result.SessionID = outcome.session.ID
	if outcome.session.ID.IsEmpty() {
		result.SessionID = domain.ID(outcome.result.SessionID)
	}
	// 结论以执行结果为准而不是以 error 为准：取消与超时可能不返回 error，
	// 但它们同样没有产出可用计划，绝不能继续解析输出。
	if callErr != nil || outcome.outcome != domain.StepExecutionCompleted {
		stage := "planner_call"
		switch outcome.outcome {
		case domain.StepExecutionCancelled:
			stage = "planner_cancelled"
		case domain.StepExecutionInterrupted:
			stage = "planner_interrupted"
		case domain.StepExecutionTimedOut:
			stage = "planner_timeout"
		}
		cause := callErr
		if cause == nil {
			cause = fmt.Errorf("Planner 调用未正常结束: %s", outcome.outcome)
		}
		return s.failPlanGeneration(
			context.WithoutCancel(ctx),
			planning,
			result,
			stage,
			cause,
		)
	}
	draft, err := ParsePlanOutput(outcome.result.FinalText)
	if err != nil {
		return s.failPlanGeneration(
			context.WithoutCancel(ctx),
			planning,
			result,
			"parse_output",
			err,
		)
	}
	plan, err := domain.BuildPlan(
		planning,
		draft,
		role,
		developerRole,
		planning.Config.Verification,
		s.now(),
	)
	if err != nil {
		return s.failPlanGeneration(
			context.WithoutCancel(ctx),
			planning,
			result,
			"validate_plan",
			err,
		)
	}
	// 计划写入必须使用未取消的上下文：Planner 已经真实执行完成，此时因请求上下文
	// 结束而丢弃整份计划，会让用户为一次成功调用付出无结果的代价。
	persisted, err := s.persistPlan(context.WithoutCancel(ctx), planning, plan, outcome.session.ID)
	result.Summary = plan.Summary
	if err != nil {
		return s.failPlanGeneration(
			context.WithoutCancel(ctx),
			planning,
			result,
			"persist_plan",
			err,
		)
	}
	result.Steps = persisted
	result.Persisted = true
	result.TaskStatus = domain.TaskStatusPlanned
	result.FinishedAt = s.now()
	s.logger.InfoContext(
		ctx,
		"计划生成已完成",
		slog.String("task_id", task.ID.String()),
		slog.Int("steps", len(persisted)),
		slog.String("session_id", result.SessionID.String()),
	)
	return result, nil
}

// validatePlanOptions 校验调用方给出的执行参数。
func (s *TaskWorkflowService) validatePlanOptions(prompt string) (string, error) {
	trimmed := strings.TrimSpace(prompt)
	if len(trimmed) > maxPlanPrompt {
		return "", fmt.Errorf(
			"%w: 补充提示词长度超过上限 %d",
			ErrWorkflowInvalidInput,
			maxPlanPrompt,
		)
	}
	return trimmed, nil
}

// planTimeout 返回本次 Planner 调用的超时时间。
//
// 优先级固定为：请求显式给出 > 任务配置 > 服务默认值。
func (s *TaskWorkflowService) planTimeout(task domain.Task, requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	if seconds := task.Config.AgentTimeoutSeconds; seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if s.defaultPlanTimeout > 0 {
		return s.defaultPlanTimeout
	}
	return defaultPlanTimeout
}

// loadPlannerRole 读取并校验任务配置的规划角色。
//
// 角色类型必须与阶段匹配、必须处于启用状态：类型不符的"规划角色"会让 Planner
// 用开发者的权限与提示词运行，这不是可接受的降级。
func (s *TaskWorkflowService) loadPlannerRole(
	ctx context.Context,
	task domain.Task,
) (domain.Role, error) {
	roleID := task.Config.PlannerRoleID
	if roleID.IsEmpty() {
		return domain.Role{}, fmt.Errorf(
			"%w: 任务 %s 未配置规划角色",
			ErrWorkflowPrerequisite,
			task.ID,
		)
	}
	role, err := s.store.GetRole(ctx, roleID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Role{}, fmt.Errorf(
				"%w: 任务 %s 关联的规划角色 %s 不存在",
				ErrWorkflowPrerequisite,
				task.ID,
				roleID,
			)
		}
		return domain.Role{}, fmt.Errorf("读取规划角色失败: %w", err)
	}
	if role.Type != domain.RoleTypePlanner {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，计划生成需要 %s",
			ErrWorkflowPrerequisite,
			role.ID,
			role.Type,
			domain.RoleTypePlanner,
		)
	}
	if !role.Enabled {
		return domain.Role{}, fmt.Errorf(
			"%w: 规划角色 %s 已被禁用",
			ErrWorkflowPrerequisite,
			role.ID,
		)
	}
	if strings.TrimSpace(role.Model) == "" {
		return domain.Role{}, fmt.Errorf(
			"%w: 规划角色 %s 未配置模型",
			ErrWorkflowPrerequisite,
			role.ID,
		)
	}
	return *role, nil
}

// loadDeveloperRole 读取并校验任务配置的开发角色。
//
// 计划中的每个开发步骤都会绑定该角色：角色缺失、类型不符或已禁用时，计划即使生成
// 成功也无法执行，因此在规划阶段就拒绝。
func (s *TaskWorkflowService) loadDeveloperRole(
	ctx context.Context,
	task domain.Task,
) (domain.Role, error) {
	roleID := task.Config.DeveloperRoleID
	if roleID.IsEmpty() {
		return domain.Role{}, fmt.Errorf(
			"%w: 任务 %s 未配置开发角色",
			ErrWorkflowPrerequisite,
			task.ID,
		)
	}
	role, err := s.store.GetRole(ctx, roleID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Role{}, fmt.Errorf(
				"%w: 任务 %s 关联的开发角色 %s 不存在",
				ErrWorkflowPrerequisite,
				task.ID,
				roleID,
			)
		}
		return domain.Role{}, fmt.Errorf("读取开发角色失败: %w", err)
	}
	if role.Type != domain.RoleTypeDeveloper {
		return domain.Role{}, fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，开发步骤需要 %s",
			ErrWorkflowPrerequisite,
			role.ID,
			role.Type,
			domain.RoleTypeDeveloper,
		)
	}
	if !role.Enabled {
		return domain.Role{}, fmt.Errorf(
			"%w: 开发角色 %s 已被禁用",
			ErrWorkflowPrerequisite,
			role.ID,
		)
	}
	return *role, nil
}

// beginPlanning 以状态条件更新把任务推进到 planning。
//
// 这是跨进程的并发闸门：两个规划请求最多只有一个能完成该迁移，另一个得到状态冲突，
// 因此不会出现两个 Planner 同时为同一任务生成计划。
func (s *TaskWorkflowService) beginPlanning(
	ctx context.Context,
	task domain.Task,
) (domain.Task, error) {
	previous := task.Status
	if err := task.Transition(domain.TaskStatusPlanning, s.now()); err != nil {
		return task, fmt.Errorf("%w: %w", ErrWorkflowConflict, err)
	}
	if err := s.store.UpdateTaskIfStatus(ctx, task, []domain.TaskStatus{previous}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return task, fmt.Errorf(
				"%w: 任务 %s 的状态已变更，无法开始计划生成",
				ErrWorkflowConflict,
				task.ID,
			)
		}
		return task, fmt.Errorf("写入任务状态失败: %w", err)
	}
	s.appendEvent(ctx, task.ID, nil, nil, domain.EventTypeTaskStarted, map[string]any{
		"status": string(domain.TaskStatusPlanning),
		"stage":  "plan",
	})
	return task, nil
}

// callPlanner 在任务容器内执行一次 Planner 调用，并收尾会话记录。
//
// 顺序固定为：创建会话记录并置为 running → 容器内调用 → 写入产出与归档。
// 会话记录先于进程启动写入，因此启动失败也会留下可诊断的记录，不会残留 running。
func (s *TaskWorkflowService) callPlanner(
	ctx context.Context,
	task domain.Task,
	role domain.Role,
	prompt string,
	timeout time.Duration,
) (planCallOutcome, error) {
	invocationID := agent.NewInvocationID()
	session, err := s.sessions.Start(ctx, StartRequest{
		// Planner 是任务级调用：没有步骤与尝试归属。
		Scope:        domain.SessionScope{TaskID: task.ID},
		Role:         role,
		InvocationID: invocationID,
		ContainerID:  strings.TrimSpace(task.ContainerID),
	})
	if err != nil {
		return planCallOutcome{}, fmt.Errorf("创建规划会话失败: %w", err)
	}
	result, runErr := s.containers.RunAgent(ctx, ContainerAgentRequest{
		ContainerID:  task.ContainerID,
		TaskID:       task.ID.String(),
		RoleType:     role.Type.String(),
		Model:        role.Model,
		SystemPrompt: role.SystemPrompt,
		Prompt:       prompt,
		SessionRoot:  s.sessions.LocalRoot(),
		SessionID:    session.ID.String(),
		InvocationID: invocationID,
		Timeout:      timeout,
		// 工作目录来自执行环境描述：Planner 必须能看到真实的仓库上下文。
		WorkDir: task.Config.EffectiveWorkDir(),
	})
	// 会话收尾必须使用未取消的上下文：取消与超时恰恰是最需要写入结果的场景。
	finished, finishErr := s.sessions.Finish(context.WithoutCancel(ctx), FinishRequest{
		SessionID: session.ID,
		Result:    result.Result,
		Archive:   archiveOf(result.Result),
	})
	outcome := planCallOutcome{
		session: finished,
		result:  result.Result,
		outcome: classifyPlanCall(result.Result, ctx),
	}
	if finishErr != nil {
		return outcome, fmt.Errorf("写入规划会话结果失败: %w", finishErr)
	}
	if runErr != nil {
		return outcome, runErr
	}
	return outcome, nil
}

// planCallOutcome 是一次 Planner 调用的原始结果。
type planCallOutcome struct {
	// session 是已持久化的会话记录，执行失败时同样存在。
	session domain.Session
	// result 是执行器返回的原始结果。
	result agent.RunResult
	// outcome 是本次调用结论，用于决定任务收敛状态。
	outcome domain.StepExecutionOutcome
}

// classifyPlanCall 把执行器结果映射为执行结论。
//
// 结论只描述"本次进程调用如何结束"，不代表计划是否可用：调用成功但输出非法的
// 情况由解析与校验阶段处理。
func classifyPlanCall(result agent.RunResult, ctx context.Context) domain.StepExecutionOutcome {
	switch result.Status {
	case agent.RunCompleted:
		return domain.StepExecutionCompleted
	case agent.RunTimedOut:
		return domain.StepExecutionTimedOut
	case agent.RunCancelled:
		if isServiceStop(ctx) {
			return domain.StepExecutionInterrupted
		}
		return domain.StepExecutionCancelled
	default:
		return domain.StepExecutionFailed
	}
}

// persistPlan 在一个事务内写入计划步骤、推进任务状态并追加事件。
//
// 三件事必须同时成立：部分步骤不落库、状态不先于步骤推进、摘要不丢失。因此它们
// 共享同一个事务边界，任一写入失败整体回滚，不会留下可被执行器消费的半成品计划。
func (s *TaskWorkflowService) persistPlan(
	ctx context.Context,
	task domain.Task,
	plan domain.Plan,
	sessionID domain.ID,
) ([]domain.Step, error) {
	planned := task
	previous := planned.Status
	if err := planned.Transition(domain.TaskStatusPlanned, s.now()); err != nil {
		return nil, fmt.Errorf("推进任务状态失败: %w", err)
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		sessionIDPointer(sessionID),
		domain.EventTypePlanCreated,
		marshalStepExecutionPayload(map[string]any{
			"summary":     plan.Summary,
			"steps":       len(plan.Steps),
			"step_titles": planStepTitles(plan.Steps),
			"session_id":  sessionID.String(),
		}),
		s.now(),
	)
	if err != nil {
		return nil, fmt.Errorf("构造计划事件失败: %w", err)
	}
	if err := s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.CreateSteps(ctx, plan.Steps); err != nil {
			return fmt.Errorf("写入计划步骤失败: %w", err)
		}
		if err := tx.UpdateTaskIfStatus(ctx, planned, []domain.TaskStatus{previous}); err != nil {
			return fmt.Errorf("写入任务状态失败: %w", err)
		}
		if _, err := tx.AppendEvent(ctx, event); err != nil {
			return fmt.Errorf("写入计划事件失败: %w", err)
		}
		return nil
	}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil, fmt.Errorf(
				"%w: 任务 %s 的状态已变更，计划未写入",
				ErrWorkflowConflict,
				task.ID,
			)
		}
		return nil, err
	}
	return plan.Steps, nil
}

// failPlanGeneration 收敛规划失败的任务并如实返回结果。
//
// 收敛使用状态前置条件：任务在此期间被其他操作推进时不覆盖，冲突时读取真实状态。
// 失败不会删除容器、不会重试，也不会留下看起来可执行的计划。
func (s *TaskWorkflowService) failPlanGeneration(
	ctx context.Context,
	task domain.Task,
	result PlanGenerationResult,
	stage string,
	cause error,
) (PlanGenerationResult, error) {
	next := domain.DecidePlanGenerationFailure(planOutcomeOfStage(stage))
	summary := errorSummary(cause)
	if summary == "" {
		summary = "计划生成失败"
	}
	converged := task
	expected := task.Status
	next = convergePlanTask(&converged, next, summary, s.now())
	if next != expected {
		if err := s.store.UpdateTaskIfStatus(
			ctx,
			converged,
			[]domain.TaskStatus{expected},
		); err != nil {
			s.logger.WarnContext(
				ctx,
				"写入任务状态失败",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(err)),
			)
			if current, readErr := s.store.GetTask(ctx, task.ID); readErr == nil {
				converged = *current
			}
		}
	}
	eventType := domain.EventTypeTaskFailed
	if next == domain.TaskStatusRecoveryRequired {
		eventType = domain.EventTypeTaskRecoveryRequired
	}
	if next == domain.TaskStatusCancelling {
		eventType = domain.EventTypeTaskCancelRequested
	}
	s.appendEvent(ctx, task.ID, nil, nil, eventType, map[string]any{
		"stage":   stage,
		"status":  string(converged.Status),
		"summary": summary,
	})
	result.TaskStatus = converged.Status
	result.FailureStage = stage
	result.Failure = summary
	result.Persisted = false
	if result.FinishedAt.IsZero() {
		result.FinishedAt = s.now()
	}
	s.logger.WarnContext(
		ctx,
		"计划生成失败",
		slog.String("task_id", task.ID.String()),
		slog.String("stage", stage),
		slog.String("status", string(converged.Status)),
		slog.String("error", summary),
	)
	return result, fmt.Errorf("%w: %s: %w", ErrWorkflowPrerequisite, stage, cause)
}

// planOutcomeOfStage 把失败阶段映射为执行结论。
//
// 只有取消与中断阶段对应"调用被外部终止"：解析与校验失败说明 Planner 输出了不可用
// 的内容，持久化失败说明计划没有落库，两者都是确定性失败。
func planOutcomeOfStage(stage string) domain.StepExecutionOutcome {
	switch stage {
	case "planner_cancelled":
		return domain.StepExecutionCancelled
	case "planner_interrupted":
		return domain.StepExecutionInterrupted
	}
	return domain.StepExecutionFailed
}

// sessionIDPointer 把会话标识转换为可空形式，空值返回 nil。
func sessionIDPointer(id domain.ID) *domain.ID {
	if id.IsEmpty() {
		return nil
	}
	return &id
}

// planStepTitles 返回步骤标题列表，用于事件负载中的可读摘要。
func planStepTitles(steps []domain.Step) []string {
	titles := make([]string, 0, len(steps))
	for _, step := range steps {
		titles = append(titles, step.Title)
	}
	return titles
}

// convergePlanTask 按状态机把任务收敛到目标状态并写入对应结论。
//
// 取消需要两步收敛：planning → cancelling → cancelled，因此这里显式走完路径；
// 其余目标状态一步完成。状态机拒绝时保持原状态并返回当前状态：不伪造"已收敛"的
// 结论，残留现场由启动恢复按持久化事实处理。
func convergePlanTask(
	task *domain.Task,
	target domain.TaskStatus,
	summary string,
	at time.Time,
) domain.TaskStatus {
	if task == nil {
		return target
	}
	current := task.Status
	if target == domain.TaskStatusCancelling {
		if err := task.RequestCancel(at); err != nil {
			return current
		}
		if err := task.Transition(domain.TaskStatusCancelling, at); err != nil {
			return current
		}
		if err := task.Finish(domain.TaskResultCancelled, summary, at); err != nil {
			return current
		}
		if err := task.Transition(domain.TaskStatusCancelled, at); err != nil {
			return task.Status
		}
		return task.Status
	}
	if err := task.Transition(target, at); err != nil {
		return current
	}
	if target == domain.TaskStatusFailed {
		if err := task.Finish(domain.TaskResultFailed, summary, at); err != nil {
			return task.Status
		}
	}
	return task.Status
}
