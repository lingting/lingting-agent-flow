package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// keyedMutex 按字符串键提供互斥锁。
//
// 不同键之间完全并发，同一键上的操作串行化；无引用时自动回收条目，
// 避免长时间运行后无限增长。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

// keyedLock 是一个带引用计数的互斥锁。
type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// Lock 取得指定键的锁，返回解锁函数。
func (m *keyedMutex) Lock(key string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*keyedLock)
	}
	entry, ok := m.locks[key]
	if !ok {
		entry = &keyedLock{}
		m.locks[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}

// marshalPayload 把事件负载编码为 JSON，失败时返回空对象。
func marshalPayload(payload map[string]any) json.RawMessage {
	if len(payload) == 0 {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

// plan 调用 Planner 生成计划并校验结构化输出。
func (e *Engine) plan(
	ctx context.Context,
	task *domain.Task,
	container ContainerInfo,
) ([]domain.Step, error) {
	role, err := e.role(ctx, task.Config.PlannerRoleID, domain.RoleTypePlanner)
	if err != nil {
		return nil, e.failTask(ctx, task, "plan", domain.TaskResultFailed, err.Error())
	}
	// 计划步骤由 Developer 角色执行：Planner 只负责生成计划。
	developer, err := e.role(ctx, task.Config.DeveloperRoleID, domain.RoleTypeDeveloper)
	if err != nil {
		return nil, e.failTask(ctx, task, "plan", domain.TaskResultFailed, err.Error())
	}
	step, err := e.createAttemptStep(ctx, task, domain.StepTypePlan, "规划执行计划",
		"生成可执行的结构化开发计划", role)
	if err != nil {
		return nil, err
	}
	var planned []domain.Step
	if err := e.runStepLifecycle(ctx, task, step, func(
		ctx context.Context,
		attempt *domain.Attempt,
	) error {
		outcome, runErr := e.ports.Agents.Run(ctx, AgentInvocation{
			ContainerID: container.ID,
			Scope: domain.SessionScope{
				TaskID:    task.ID,
				StepID:    &step.ID,
				AttemptID: &attempt.ID,
			},
			Role:    role,
			Prompt:  planPrompt(*task),
			Timeout: e.agentTimeout(*task),
		})
		if runErr != nil || !agentSucceeded(outcome.Result) {
			summary := sessionFailureSummary(outcome.Result)
			if summary == "" && runErr != nil {
				summary = runErr.Error()
			}
			return &stepError{
				stage:    "plan",
				class:    domain.RetryClassAgentFailure,
				summary:  "规划调用失败: " + summary,
				attempt:  attempt,
				session:  outcome,
				timeout:  outcome.Result.Status == agent.RunTimedOut,
				canceled: outcome.Result.Status == agent.RunCancelled,
			}
		}
		if err := attempt.AddSession(outcome.Session.ID); err != nil {
			return err
		}
		output, parseErr := ParsePlanOutput(outcome.Result.FinalText)
		if parseErr != nil {
			return &stepError{
				stage:   "plan",
				class:   domain.RetryClassAgentFailure,
				summary: parseErr.Error(),
				attempt: attempt,
				session: outcome,
			}
		}
		steps, buildErr := BuildPlanSteps(
			*task,
			output,
			developer,
			task.Config.Verification,
			e.now(),
		)
		if buildErr != nil {
			return &stepError{
				stage:   "plan",
				class:   domain.RetryClassAgentFailure,
				summary: buildErr.Error(),
				attempt: attempt,
				session: outcome,
			}
		}
		planned = steps
		return nil
	}); err != nil {
		return nil, err
	}
	steps := planned
	if len(steps) == 0 {
		return nil, e.failTask(
			ctx,
			task,
			"plan",
			domain.TaskResultFailed,
			"规划阶段未产出任何可执行步骤",
		)
	}
	if err := e.appendEvent(ctx, *task, &step.ID, nil, nil,
		domain.EventTypePlanCreated,
		map[string]any{"steps": len(steps)},
	); err != nil {
		return nil, err
	}
	return steps, nil
}

// runStep 执行一个开发步骤并完成验证与代码保存判定。
func (e *Engine) runStep(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
) (domain.Task, error) {
	if step.Type != domain.StepTypeDevelop {
		// 非开发步骤由固定阶段处理，不能进入开发执行路径。
		return task, e.failTask(
			ctx,
			&task,
			"execute",
			domain.TaskResultFailed,
			"计划中包含不可执行的步骤类型: "+string(step.Type),
		)
	}
	role, err := e.role(ctx, step.RoleID, domain.RoleTypeDeveloper)
	if err != nil {
		return task, e.failTask(ctx, &task, "execute", domain.TaskResultFailed, err.Error())
	}
	containerID := strings.TrimSpace(task.ContainerID)
	if containerID == "" {
		return task, e.failTask(
			ctx,
			&task,
			"execute",
			domain.TaskResultFailed,
			"任务缺少容器关联，无法执行开发步骤",
		)
	}
	var lastError error
	for {
		evidence, err := e.failureEvidence(ctx, step.ID)
		if err != nil {
			return task, err
		}
		outcome, runErr := e.runDevelopAttempt(ctx, &task, step, role, containerID, evidence)
		if runErr == nil {
			return e.verifyStep(ctx, task, outcome.step, outcome.attempt)
		}
		lastError = runErr
		next, retryErr := e.handleStepFailure(ctx, &task, outcome.step, outcome.attempt, runErr)
		if retryErr != nil {
			return next, retryErr
		}
		if next.Status != domain.TaskStatusRetrying {
			return next, lastError
		}
		if err := e.transition(ctx, &task, domain.TaskStatusDeveloping); err != nil {
			return task, err
		}
		refreshed, err := e.store.GetStep(ctx, step.ID)
		if err != nil {
			return task, fmt.Errorf("读取步骤失败: %w", err)
		}
		step = *refreshed
	}
}

// developOutcome 是一次开发执行尝试的结果。
type developOutcome struct {
	step    domain.Step
	attempt domain.Attempt
}

// runDevelopAttempt 执行一次开发尝试。
//
// 每次尝试都创建新的 Attempt 与新的 Session：重试不会覆盖历史记录。
func (e *Engine) runDevelopAttempt(
	ctx context.Context,
	task *domain.Task,
	step domain.Step,
	role domain.Role,
	containerID string,
	evidence []string,
) (developOutcome, error) {
	attempt, err := e.startAttempt(ctx, task, &step)
	if err != nil {
		return developOutcome{step: step}, err
	}
	attempt.ContainerID = containerID
	if err := e.store.UpdateAttempt(ctx, attempt); err != nil {
		return developOutcome{step: step, attempt: attempt}, fmt.Errorf(
			"保存执行尝试失败: %w",
			err,
		)
	}
	outcome, runErr := e.ports.Agents.Run(ctx, AgentInvocation{
		ContainerID: containerID,
		Scope: domain.SessionScope{
			TaskID:    task.ID,
			StepID:    &step.ID,
			AttemptID: &attempt.ID,
		},
		Role:    role,
		Prompt:  developPrompt(*task, step, evidence),
		Timeout: e.agentTimeout(*task),
	})
	if runErr != nil || !agentSucceeded(outcome.Result) {
		summary := sessionFailureSummary(outcome.Result)
		if summary == "" && runErr != nil {
			summary = runErr.Error()
		}
		return developOutcome{step: step, attempt: attempt}, &stepError{
			stage:    "develop",
			class:    domain.RetryClassAgentFailure,
			summary:  "开发调用失败: " + summary,
			attempt:  &attempt,
			session:  outcome,
			timeout:  outcome.Result.Status == agent.RunTimedOut,
			canceled: outcome.Result.Status == agent.RunCancelled,
		}
	}
	attempt.ExitCode = outcome.Result.ExitCode
	if err := attempt.AddSession(outcome.Session.ID); err != nil {
		return developOutcome{step: step, attempt: attempt}, err
	}
	return developOutcome{step: step, attempt: attempt}, nil
}

// verifyStep 先执行确定性验证，再按需调用辅助评估，最后写入判定。
func (e *Engine) verifyStep(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
) (domain.Task, error) {
	runner := newVerificationRunner(
		e.ports.Commands,
		time.Duration(task.Config.VerificationTimeoutSeconds)*time.Second,
	)
	results, err := runner.run(
		ctx,
		task.ContainerID,
		step.VerificationSpec,
		task.Config.EffectiveWorkDir(),
	)
	if err != nil {
		return task, e.failTask(
			ctx,
			&task,
			"verify",
			domain.TaskResultFailed,
			"执行确定性验证失败: "+err.Error(),
		)
	}
	// 必需检查失败时跳过辅助评估以节省时间，但必须记录该阶段被跳过。
	blocking := blockingCommands(results)
	var assessment *domain.AgentAssessment
	var validatorSessionID *domain.ID
	reviewSkipped := false
	reviewRequested := !task.Config.ValidatorRoleID.IsEmpty()
	if len(blocking) > 0 {
		reviewSkipped = true
	} else if reviewRequested {
		assessment, validatorSessionID, err = e.review(ctx, &task, step, attempt, results)
		if err != nil {
			return task, err
		}
	}
	result, err := domain.NewVerificationResult(domain.VerificationInput{
		TaskID:             task.ID,
		StepID:             step.ID,
		AttemptID:          attempt.ID,
		ValidatorSessionID: validatorSessionID,
		Summary:            reviewSummary(assessment),
		CommandResults:     results,
		Assessment:         assessment,
		ReviewRequested:    reviewRequested,
		ReviewSkipped:      reviewSkipped,
	}, e.now())
	if err != nil {
		return task, e.failTask(
			ctx,
			&task,
			"verify",
			domain.TaskResultFailed,
			"构造验证结果失败: "+err.Error(),
		)
	}
	if err := e.store.CreateVerificationResult(ctx, result); err != nil {
		return task, fmt.Errorf("保存验证结果失败: %w", err)
	}
	if err := attempt.SetVerification(result.Verdict, summarizeVerification(result), &result.ID); err != nil {
		return task, fmt.Errorf("记录验证判定失败: %w", err)
	}
	if err := e.store.UpdateAttempt(ctx, attempt); err != nil {
		return task, fmt.Errorf("保存执行尝试失败: %w", err)
	}
	eventType := domain.EventTypeVerificationPassed
	if result.Verdict != domain.VerificationVerdictPassed {
		eventType = domain.EventTypeVerificationFailed
	}
	if err := e.appendEvent(ctx, task, &step.ID, &attempt.ID, validatorSessionID,
		eventType,
		map[string]any{
			"verdict":         string(result.Verdict),
			"failure_summary": result.FailureSummary,
			"review_skipped":  reviewSkipped,
		},
	); err != nil {
		return task, err
	}
	if result.Verdict == domain.VerificationVerdictPassed {
		return e.completeStep(ctx, task, step, attempt, result)
	}
	failure := &stepError{
		stage:   "verify",
		class:   domain.RetryClassVerificationFailure,
		summary: result.FailureSummary,
		attempt: &attempt,
	}
	if result.Verdict == domain.VerificationVerdictInconclusive {
		failure.class = domain.RetryClassTimeout
		failure.summary = "验证结果不确定: " + result.FailureSummary
	}
	next, retryErr := e.handleStepFailure(ctx, &task, step, attempt, failure)
	if retryErr != nil {
		return next, retryErr
	}
	return next, nil
}

// review 调用 Validator Agent 进行辅助评估。
//
// 辅助评估使用独立 Session；结果不可解释时返回 nil 评估，由判定逻辑判为
// inconclusive，绝不静默当作通过。
func (e *Engine) review(
	ctx context.Context,
	task *domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	results []domain.CommandResult,
) (*domain.AgentAssessment, *domain.ID, error) {
	role, err := e.role(ctx, task.Config.ValidatorRoleID, domain.RoleTypeValidator)
	if err != nil {
		// 未配置验证角色时视为"没有辅助评估"：必需命令全部通过仍然可以通过，
		// 但会记录评估缺失的原因。
		e.logger.Debug("跳过辅助评估", "task_id", task.ID.String(), "reason", err.Error())
		return nil, nil, nil
	}
	reviewCtx, cancel := context.WithTimeout(ctx, e.reviewTimeout)
	defer cancel()
	outcome, runErr := e.ports.Agents.Run(reviewCtx, AgentInvocation{
		ContainerID: task.ContainerID,
		Scope: domain.SessionScope{
			TaskID:    task.ID,
			StepID:    &step.ID,
			AttemptID: &attempt.ID,
		},
		Role:    role,
		Prompt:  reviewPrompt(*task, step, results),
		Timeout: e.reviewTimeout,
	})
	sessionID := outcome.Session.ID
	if runErr != nil || !agentSucceeded(outcome.Result) {
		if errors.Is(runErr, context.Canceled) && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		e.logger.Debug(
			"辅助评估调用未完成",
			"task_id", task.ID.String(),
			"step_id", step.ID.String(),
			"error", sessionFailureSummary(outcome.Result),
		)
		return nil, nil, nil
	}
	assessment, parseErr := ParseAssessment(outcome.Result.FinalText)
	if parseErr != nil {
		e.logger.Debug(
			"辅助评估输出不可解释",
			"task_id", task.ID.String(),
			"step_id", step.ID.String(),
			"error", parseErr.Error(),
		)
		return nil, nil, nil
	}
	if sessionID.IsEmpty() {
		return &assessment, nil, nil
	}
	return &assessment, &sessionID, nil
}

// completeStep 把步骤与尝试标记为成功。
func (e *Engine) completeStep(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	result domain.VerificationResult,
) (domain.Task, error) {
	if err := attempt.Complete(e.now()); err != nil {
		return task, fmt.Errorf("标记尝试完成失败: %w", err)
	}
	if err := e.store.UpdateAttempt(ctx, attempt); err != nil {
		return task, fmt.Errorf("保存执行尝试失败: %w", err)
	}
	step.Result = summarizeVerification(result)
	if err := step.Transition(domain.StepStatusCompleted, e.now()); err != nil {
		return task, fmt.Errorf("标记步骤完成失败: %w", err)
	}
	if err := step.SetCurrentAttempt(nil); err != nil {
		return task, err
	}
	if err := e.store.UpdateStep(ctx, step); err != nil {
		return task, fmt.Errorf("保存步骤失败: %w", err)
	}
	if err := e.appendEvent(ctx, task, &step.ID, &attempt.ID, nil,
		domain.EventTypeStepCompleted,
		map[string]any{"summary": step.Result},
	); err != nil {
		return task, err
	}
	return task, nil
}

// handleStepFailure 按重试策略处理失败：要么创建新的尝试，要么终止任务。
func (e *Engine) handleStepFailure(
	ctx context.Context,
	task *domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	cause error,
) (domain.Task, error) {
	stage, class, summary, timedOut, cancelled := classifyStepError(cause)
	if cancelled {
		// 取消不是普通失败：先把尝试标记为 cancelled，再收敛任务状态。
		if attempt.Status == domain.AttemptStatusRunning {
			interrupted := attempt
			if err := interrupted.Cancel(summary, e.now()); err != nil {
				return *task, fmt.Errorf("标记尝试取消失败: %w", err)
			}
			interrupted.FailureStage = stage
			interrupted.ErrorClass = string(class)
			if err := e.store.UpdateAttempt(ctx, interrupted); err != nil {
				return *task, fmt.Errorf("保存执行尝试失败: %w", err)
			}
		}
		return *task, e.handleCancel(ctx, *task, context.Canceled)
	}
	finished := attempt
	if finished.Status == domain.AttemptStatusRunning {
		var err error
		if timedOut {
			err = finished.Timeout(summary, e.now())
		} else {
			err = finished.Fail(summary, e.now())
		}
		if err != nil {
			return *task, fmt.Errorf("标记尝试失败失败: %w", err)
		}
		finished.ErrorClass = string(class)
		finished.FailureStage = stage
		if err := e.store.UpdateAttempt(ctx, finished); err != nil {
			return *task, fmt.Errorf("保存执行尝试失败: %w", err)
		}
	}
	if err := e.appendEvent(ctx, *task, &step.ID, &finished.ID, nil,
		domain.EventTypeAttemptFailed,
		map[string]any{"stage": stage, "class": string(class), "summary": summary},
	); err != nil {
		return *task, err
	}
	policy := effectiveRetry(task.Config.Retry, step.Retry, step.MaxAttempts)
	if !policy.Allows(class) || finished.AttemptNumber >= policy.Limit() {
		step.FailureStage = stage
		step.Result = summary
		if err := step.Transition(domain.StepStatusFailed, e.now()); err != nil {
			return *task, fmt.Errorf("标记步骤失败失败: %w", err)
		}
		if err := e.store.UpdateStep(ctx, step); err != nil {
			return *task, fmt.Errorf("保存步骤失败: %w", err)
		}
		return *task, e.failTask(
			ctx,
			task,
			stage,
			domain.TaskResultFailed,
			fmt.Sprintf("步骤 %s 失败且不再重试: %s", step.Title, summary),
		)
	}
	step.FailureStage = stage
	if err := step.Transition(domain.StepStatusRetryWait, e.now()); err != nil {
		return *task, fmt.Errorf("标记步骤重试等待失败: %w", err)
	}
	if err := e.store.UpdateStep(ctx, step); err != nil {
		return *task, fmt.Errorf("保存步骤失败: %w", err)
	}
	if err := task.Transition(domain.TaskStatusRetrying, e.now()); err != nil {
		return *task, fmt.Errorf("任务状态迁移失败: %w", err)
	}
	if err := e.persistTaskIfStatus(ctx, *task, domain.TaskStatusVerifying); err != nil {
		return *task, err
	}
	if err := e.appendEvent(ctx, *task, &step.ID, &finished.ID, nil,
		domain.EventTypeTaskRetrying,
		map[string]any{
			"attempt": finished.AttemptNumber,
			"class":   string(class),
			"summary": summary,
		},
	); err != nil {
		return *task, err
	}
	if wait := time.Duration(task.Config.Retry.WaitSeconds) * time.Second; wait > 0 {
		if err := sleep(ctx, wait); err != nil {
			return *task, e.handleCancel(ctx, *task, err)
		}
	}
	return *task, nil
}

// sleep 在等待期间响应取消。
func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// blockingCommands 返回阻止成功的命令结果。
func blockingCommands(results []domain.CommandResult) []domain.CommandResult {
	blocking := make([]domain.CommandResult, 0, len(results))
	for _, result := range results {
		if result.BlocksSuccess() {
			blocking = append(blocking, result)
		}
	}
	return blocking
}

// failureEvidence 收集步骤历史失败证据，用于修复型重试。
func (e *Engine) failureEvidence(
	ctx context.Context,
	stepID domain.ID,
) ([]string, error) {
	attempts, err := e.store.ListAttemptsByStep(ctx, stepID)
	if err != nil {
		return nil, fmt.Errorf("读取尝试历史失败: %w", err)
	}
	evidence := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.VerificationID == nil {
			continue
		}
		result, err := e.store.GetVerificationResult(ctx, *attempt.VerificationID)
		if err != nil {
			continue
		}
		if result.Verdict == domain.VerificationVerdictPassed {
			continue
		}
		for _, command := range result.CommandResults {
			if summary := commandFailureSummary(command); summary != "" {
				evidence = append(evidence, summary)
			}
		}
		if result.Assessment != nil {
			for _, finding := range result.Assessment.Findings {
				evidence = append(evidence, "评估问题: "+finding)
			}
		}
		if len(evidence) == 0 && result.FailureSummary != "" {
			evidence = append(evidence, result.FailureSummary)
		}
	}
	if len(evidence) > maxEvidenceInPrompt {
		evidence = evidence[len(evidence)-maxEvidenceInPrompt:]
	}
	return evidence, nil
}

// reviewSummary 生成辅助评估摘要。
func reviewSummary(assessment *domain.AgentAssessment) string {
	if assessment == nil {
		return ""
	}
	return assessment.Summary
}

// effectiveRetry 返回生效的重试策略。
//
// 步骤级策略优先；未配置时使用任务级策略；两者都未配置时使用默认上限。
func effectiveRetry(taskRetry, stepRetry domain.RetryConfig, maxAttempts int) domain.RetryConfig {
	policy := stepRetry
	if policy.IsZero() {
		policy = taskRetry
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = maxAttempts
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = domain.DefaultMaxAttempts
	}
	return policy
}

// agentTimeout 返回任务配置的单次 Agent 调用超时。
func (e *Engine) agentTimeout(task domain.Task) time.Duration {
	seconds := task.Config.AgentTimeoutSeconds
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// role 读取并校验角色配置。
func (e *Engine) role(
	ctx context.Context,
	roleID domain.ID,
	expected domain.RoleType,
) (domain.Role, error) {
	if roleID.IsEmpty() {
		return domain.Role{}, fmt.Errorf("角色配置缺失: 需要 %s 角色", expected)
	}
	stored, err := e.store.GetRole(ctx, roleID)
	if err != nil {
		return domain.Role{}, fmt.Errorf("读取角色 %s 失败: %w", roleID, err)
	}
	if stored.Type != expected {
		return domain.Role{}, fmt.Errorf(
			"角色 %s 的类型是 %s，但该阶段需要 %s",
			roleID,
			stored.Type,
			expected,
		)
	}
	if !stored.Enabled {
		return domain.Role{}, fmt.Errorf("角色 %s 已被禁用", roleID)
	}
	return *stored, nil
}

// stepError 描述一次阶段执行失败的分类信息。
//
// 它把"哪个阶段失败、属于哪类错误、是否可重试"显式携带给重试决策，
// 避免仅凭错误文本猜测失败原因。
type stepError struct {
	stage    string
	class    domain.RetryErrorClass
	summary  string
	attempt  *domain.Attempt
	session  AgentOutcome
	timeout  bool
	canceled bool
}

// Error 实现 error 接口。
func (x *stepError) Error() string {
	if x == nil {
		return ""
	}
	return x.stage + ": " + x.summary
}

// Unwrap 暴露底层取消语义，使调用方可以用 errors.Is 判断。
func (x *stepError) Unwrap() error {
	if x == nil || !x.canceled {
		return nil
	}
	return context.Canceled
}

// classifyStepError 从错误中提取阶段、类别与摘要。
func classifyStepError(err error) (string, domain.RetryErrorClass, string, bool, bool) {
	var failure *stepError
	if errors.As(err, &failure) {
		return failure.stage, failure.class, failure.summary, failure.timeout, failure.canceled
	}
	class := domain.RetryClassAgentFailure
	if errors.Is(err, context.DeadlineExceeded) {
		class = domain.RetryClassTimeout
	}
	if errors.Is(err, context.Canceled) {
		return "execute", class, cancelReason(err), false, true
	}
	return "execute", class, err.Error(), false, false
}
