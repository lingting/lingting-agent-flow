package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 修复循环的默认参数。
const (
	// maxStepWorkflowRepairRounds 是单次闭环调用允许推进的轮次硬上限。
	//
	// 步骤生效的尝试上限由 domain.RetryPolicy 决定（默认 3，最大 10），领域判定
	// domain.DecideRepairLoop 已经按它限制轮次；这里再加一层进程内硬限制，防止异常
	// 数据让一次 HTTP 请求无限期占用执行资源。
	maxStepWorkflowRepairRounds = domain.MaxTaskMaxAttempts + 1
)

// ExecuteAndValidate 执行一个步骤的开发阶段，并在验证未通过且仍允许修复时继续下一轮。
//
// 语义固定为：
//   - 每一轮开发与验证都使用任务对应的同一个容器工作区，中间不重新 Clone、不清理改动；
//   - 每一轮都创建新的 Attempt、独立 Session 与新的 Validation Run，历史一律保留；
//   - 只有开发阶段正常结束、执行记录已写入、验证配置有效且上下文未被取消时才启动验证；
//   - 步骤的最终状态只由真实验证结果决定：开发成功但未验证不会进入 completed；
//   - 修复是否继续只由 domain.DecideRepairLoop 依据持久化事实判定，编排器不复制规则；
//   - 达到尝试上限、验证结论不确定、基础设施异常、记录未可靠落库、取消与开发失败
//     一律停止，不自动重试、不自动删除容器。
//
// 尝试次数口径：MaxAttempts 表示"本次闭环调用允许的 Developer 执行总次数（含首轮）"。
// 计数只累加本轮真实创建并落库的 Attempt，因此它是持久化事实而不是进程内愿望；用户
// 显式发起的下一次执行是一次新的显式决定，因此从新的预算开始计数。
//
// 执行是同步的：只有收口状态写入完成或确定无法写入后才返回。
func (s *StepWorkflowService) ExecuteAndValidate(
	ctx context.Context,
	request ExecuteAndValidateRequest,
) (ExecuteAndValidateResult, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}
	stepID, err := parseWorkflowID("步骤", request.StepID)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}
	step, err := s.loadStep(ctx, task, stepID)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}
	if err := domain.CheckStepExecutionStart(task, step); err != nil {
		return ExecuteAndValidateResult{}, fmt.Errorf("%w: %w", ErrStepExecutionConflict, err)
	}
	if err := s.checkPrerequisites(ctx, task, step); err != nil {
		return ExecuteAndValidateResult{}, err
	}
	// 验证配置在开发之前确认：配置无法支撑判定时，跑完开发只会得到一次注定失败的
	// 闭环，并把配置问题伪装成代码问题。
	if err := requireStepVerificationSpec(step); err != nil {
		return ExecuteAndValidateResult{}, err
	}
	// 首轮修复依据同样在开发之前读取：读取失败时直接拒绝进入循环，避免跑完一轮后
	// 才发现提示词里的"上一次问题"并不可信。
	feedback, err := s.initialFeedback(ctx, step)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}
	prompt, err := stepWorkflowPrompt(task, step, request.Prompt, feedback)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}

	runCtx, finishRun, runErr := s.beginRun(ctx)
	if runErr != nil {
		return ExecuteAndValidateResult{}, runErr
	}
	defer finishRun()

	release, acquired := s.running.acquire(step.ID)
	if !acquired {
		return ExecuteAndValidateResult{}, fmt.Errorf(
			"%w: 步骤 %s 正在执行开发—验证闭环",
			ErrStepExecutionConflict,
			step.ID,
		)
	}
	defer release()

	return s.runRepairLoop(runCtx, request, task, step, prompt)
}

// runRepairLoop 逐轮执行开发与完整验证，直到验证通过或出现停止条件。
//
// 循环只消费 domain.DecideRepairLoop 的判定结果：轮次上限、停止类别与终态映射都在
// 领域层定义，编排器不复制第二套规则。
func (s *StepWorkflowService) runRepairLoop(
	runCtx context.Context,
	request ExecuteAndValidateRequest,
	task domain.Task,
	step domain.Step,
	initialPrompt string,
) (ExecuteAndValidateResult, error) {
	prompt := initialPrompt
	// attempts 只统计本轮真实创建并落库的 Attempt：计数即持久化事实。
	attempts := 0
	var last ExecuteAndValidateResult
	for round := 1; ; round++ {
		if err := runCtx.Err(); err != nil {
			return s.stopLoopBeforeRound(runCtx, last, step, attempts)
		}
		if round > maxStepWorkflowRepairRounds {
			return s.closeRepairLoop(runCtx, last, step, domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassAttemptsExhausted,
				Summary: "单次闭环最多推进 " + itoaForPrompt(maxStepWorkflowRepairRounds) +
					" 轮，已停止自动修复",
			}, attempts)
		}
		// 每轮都重新读取步骤：上一轮收口后状态已变化，必须在最新现场上判断能否继续。
		current, err := s.loadStep(runCtx, task, step.ID)
		if err != nil {
			return last, err
		}
		if round > 1 && !current.Status.CanStartExecution() {
			// 状态机不允许的现场不绕过：停止并如实报告，不伪造新一轮执行。
			return s.closeRepairLoop(runCtx, last, current, domain.RepairDecision{
				Kind:  domain.RepairDecisionStop,
				Class: domain.RepairClassNotPersisted,
				Summary: "步骤 " + current.ID.String() + " 当前状态 " + string(current.Status) +
					" 不允许继续执行，停止自动修复",
			}, attempts)
		}
		result, executeErr := s.executeRepairRound(runCtx, request, current, prompt, round)
		last = result
		if executeErr != nil {
			// 协调器返回错误意味着本次没有产生真实执行（状态冲突、前置条件缺失、
			// 目标不存在等）：错误必须原样返回，不能伪装成"执行后正常停止"，
			// 也不能通过消耗重试次数来掩盖它。
			return result, executeErr
		}
		if !result.AttemptID.IsEmpty() {
			attempts++
		}
		decision := domain.DecideRepairLoop(repairLoopInputOf(current, result, attempts, runCtx))
		if decision.CanRepair() && result.AttemptID.IsEmpty() {
			// 没有产生 Attempt 却要"再执行一轮"说明现场与判定不一致：继续循环会
			// 重放没有记录的调用，因此按"结果无法确认"停止。
			decision = domain.RepairDecision{
				Kind:    domain.RepairDecisionStop,
				Class:   domain.RepairClassNotPersisted,
				Summary: "本轮没有产生可追溯的执行尝试记录，停止自动修复",
			}
		}
		result.Repair = decision
		s.appendRepairEvent(runCtx, result, decision, round, attempts, current.MaxAttempts)
		if decision.Completed() {
			// 成功路径也要回报本次闭环的尝试次数与轮次，使调用方看到完整事实。
			result.Attempts = attempts
			return result, nil
		}
		if !decision.CanRepair() {
			return s.closeRepairLoop(runCtx, result, current, decision, attempts)
		}
		next, err := s.repairPrompt(runCtx, task, current, request.Prompt, result)
		if err != nil {
			return s.closeRepairLoop(runCtx, result, current, domain.RepairDecision{
				Kind:    domain.RepairDecisionStop,
				Class:   domain.RepairClassFeedbackUnavailable,
				Summary: "读取下一轮修复依据失败: " + errorSummary(err),
			}, attempts)
		}
		prompt = next
		s.logger.InfoContext(
			runCtx,
			"验证未通过，进入下一轮修复",
			slog.String("task_id", result.TaskID.String()),
			slog.String("step_id", result.StepID.String()),
			slog.String("attempt_id", result.AttemptID.String()),
			slog.String("validation_run_id", result.ValidationRunID.String()),
			slog.Int("round", round+1),
			slog.Int("attempts", attempts),
			slog.Int("max_attempts", current.MaxAttempts),
		)
	}
}

// executeRepairRound 执行一轮"开发 + 验证 + 收口"。
func (s *StepWorkflowService) executeRepairRound(
	runCtx context.Context,
	request ExecuteAndValidateRequest,
	step domain.Step,
	prompt string,
	round int,
) (ExecuteAndValidateResult, error) {
	task, err := s.loadTask(runCtx, step.TaskID)
	if err != nil {
		return ExecuteAndValidateResult{}, err
	}
	result := ExecuteAndValidateResult{
		TaskID:    task.ID,
		StepID:    step.ID,
		Round:     round,
		StartedAt: s.now(),
	}
	developer, devErr := s.steps.Execute(runCtx, StepExecuteRequest{
		TaskID:  task.ID.String(),
		StepID:  step.ID.String(),
		Prompt:  prompt,
		Timeout: request.Timeout,
		// 开发成功只进入 verifying：此时既没有通过验证，也没有失败。
		DeferCompletion: true,
	})
	result = applyDeveloperResult(result, developer)
	if devErr != nil {
		// 协调器返回错误意味着本次没有产生真实执行（状态冲突、前置条件缺失、
		// 目标不存在等）：错误必须原样返回，不能伪装成"执行后正常停止"。
		result.FinishedAt = s.now()
		return result, devErr
	}
	if developer.Outcome != domain.StepExecutionCompleted {
		// 开发失败、超时、取消或中断一律不启动验证：此时没有待验证的完成品。
		result.ValidationSkipReason = "开发阶段结论为 " + string(developer.Outcome) + "，不启动验证"
		result.FinishedAt = s.now()
		return s.finalizeFailedDeveloper(runCtx, result)
	}
	if !developer.Finalized {
		// 执行记录未完整写入时无法可靠地把验证关联到本次尝试。
		result.ValidationSkipReason = "开发阶段的执行记录未能完整写入，不启动验证"
		result.Verification = domain.StepVerificationUnverified
		result.FinishedAt = s.now()
		return s.closeout(runCtx, result, domain.StepCloseoutInput{
			Outcome:      developer.Outcome,
			Verification: domain.StepVerificationUnverified,
		})
	}
	return s.validateAndCloseout(runCtx, runCtx, result, request.ValidationTimeout)
}

// repairLoopInputOf 依据本轮真实结果构造领域判定输入。
func repairLoopInputOf(
	step domain.Step,
	result ExecuteAndValidateResult,
	attempts int,
	ctx context.Context,
) domain.RepairLoopInput {
	maxAttempts := step.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = domain.DefaultMaxAttempts
	}
	return domain.RepairLoopInput{
		StepType:            step.Type,
		Attempts:            attempts,
		MaxAttempts:         maxAttempts,
		Retry:               step.Retry,
		Outcome:             result.Outcome,
		VerificationRan:     result.ValidationRan,
		Verification:        result.Verification,
		ValidationPersisted: result.ValidationPersisted,
		InfrastructureError: result.InfrastructureError,
		FeedbackPersisted:   result.FeedbackPersisted,
		Finalized:           result.Finalized,
		Cancelled:           ctx != nil && ctx.Err() != nil,
	}
}

// repairPrompt 构造下一轮修复的提示词。
//
// 依据只来自上一轮真实验证产生的失败反馈：按 ValidationRunID 精确读取并校验归属，
// 因此不会误用其他步骤或其他任务的反馈。读取失败、反馈缺失或归属不符时返回错误，
// 由调用方停止循环——没有可信依据就不重放 Agent 调用。
func (s *StepWorkflowService) repairPrompt(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	additional string,
	previous ExecuteAndValidateResult,
) (string, error) {
	if previous.ValidationRunID.IsEmpty() {
		return "", fmt.Errorf(
			"%w: 上一轮没有验证记录，无法构造修复依据",
			ErrStepWorkflowInvalidConfig,
		)
	}
	feedback, err := s.store.GetValidationFailureFeedback(ctx, previous.ValidationRunID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", fmt.Errorf(
				"%w: 验证记录 %s 没有失败反馈",
				ErrStepWorkflowInvalidConfig,
				previous.ValidationRunID,
			)
		}
		return "", fmt.Errorf("读取验证失败反馈失败: %w", err)
	}
	if feedback.StepID != step.ID || feedback.TaskID != task.ID {
		// 数据库不声明外键，归属必须由应用层校验：拿错反馈会让下一轮修复方向完全错误。
		return "", fmt.Errorf(
			"%w: 失败反馈 %s 与步骤 %s 的归属不一致",
			ErrStepWorkflowInvalidConfig,
			feedback.ValidationRunID,
			step.ID,
		)
	}
	return stepWorkflowPrompt(task, step, additional, feedback)
}

// closeRepairLoop 依据领域判定写入修复循环的最终收口状态。
//
// 每一轮的收口已经由单轮逻辑写入 failed / cancelled / recovery_required；这里只在
// 循环结束时把步骤最终状态明确为领域判定给出的结论，使调用方看到的是确定终态而不是
// 上一轮的中间状态。
func (s *StepWorkflowService) closeRepairLoop(
	ctx context.Context,
	result ExecuteAndValidateResult,
	step domain.Step,
	decision domain.RepairDecision,
	attempts int,
) (ExecuteAndValidateResult, error) {
	writeCtx := context.WithoutCancel(ctx)
	closeout := domain.DecideRepairCloseout(domain.RepairCloseoutInput{
		Decision:    decision,
		Attempts:    attempts,
		MaxAttempts: step.MaxAttempts,
	})
	// 收口摘要必须保留最后一轮的真实失败证据（失败检查名与退出码）：只写"达到上限"
	// 会让操作者失去"到底哪里没通过"的信息。
	if closeout.Status != domain.StepStatusCompleted {
		if detail := strings.TrimSpace(verificationStopSummary(result)); detail != "" {
			closeout.Summary += "；" + detail
		}
	}
	result.Repair = decision
	result.RepairCloseout = closeout
	result.Attempts = attempts
	closed, err := s.applyRepairCloseout(writeCtx, result, closeout)
	closed.Repair = decision
	closed.RepairCloseout = closeout
	closed.Attempts = attempts
	if err != nil {
		closed.FinalizationErrors = append(closed.FinalizationErrors, err.Error())
		closed.Finalized = false
		s.logger.ErrorContext(
			writeCtx,
			"写入修复循环收口状态失败",
			slog.String("task_id", closed.TaskID.String()),
			slog.String("step_id", closed.StepID.String()),
			slog.String("error", err.Error()),
		)
	}
	closed.Verified = closeout.Verified() && closed.StepStatus == domain.StepStatusCompleted
	if closeout.Status == domain.StepStatusRecoveryRequired {
		// 需要人工核验时同步推进任务：与单轮收口保持同一套规则，不另立判定。
		task, taskErr := s.applyTaskCloseout(writeCtx, closed, domain.StepCloseoutDecision{
			Status:  closeout.Status,
			Summary: closeout.Summary,
		})
		if taskErr != nil {
			closed.FinalizationErrors = append(closed.FinalizationErrors, taskErr.Error())
			closed.Finalized = false
		} else {
			closed.TaskStatus = task.Status
		}
	}
	s.appendRepairEvent(writeCtx, closed, decision, closed.Round, attempts, step.MaxAttempts)
	s.logger.InfoContext(
		writeCtx,
		"修复循环已结束",
		slog.String("task_id", closed.TaskID.String()),
		slog.String("step_id", closed.StepID.String()),
		slog.String("class", string(decision.Class)),
		slog.Int("attempts", attempts),
		slog.Int("max_attempts", step.MaxAttempts),
		slog.String("step_status", string(closed.StepStatus)),
		slog.Bool("verified", closed.Verified),
	)
	return closed, nil
}

// stopLoopBeforeRound 处理"首轮之前或轮次之间被取消"的情况。
//
// 此时没有产生新的执行，因此不写步骤状态：取消由既有取消语义处理，这里只如实返回
// 现场，避免用一个伪造的终态覆盖真实记录。
func (s *StepWorkflowService) stopLoopBeforeRound(
	ctx context.Context,
	last ExecuteAndValidateResult,
	step domain.Step,
	attempts int,
) (ExecuteAndValidateResult, error) {
	cause := context.Cause(ctx)
	decision := domain.RepairDecision{
		Kind:    domain.RepairDecisionStop,
		Class:   domain.RepairClassCancelled,
		Summary: "修复循环在轮次之间被取消: " + cancelCauseText(cause),
	}
	last.Repair = decision
	last.Attempts = attempts
	s.appendRepairEvent(context.WithoutCancel(ctx), last, decision, last.Round, attempts, step.MaxAttempts)
	if last.TaskID.IsEmpty() {
		return last, fmt.Errorf("%w: %w", ErrStepExecutorStopped, cause)
	}
	return last, nil
}

// cancelCauseText 生成取消原因说明。
func cancelCauseText(cause error) string {
	switch {
	case cause == nil:
		return "调用方取消"
	case errors.Is(cause, ErrStepExecutorStopped):
		return "服务关闭导致中断"
	case errors.Is(cause, context.Canceled):
		return "调用方取消"
	case errors.Is(cause, context.DeadlineExceeded):
		return "调用超时"
	default:
		return errorSummary(cause)
	}
}

// applyRepairCloseout 把修复循环的收口结论写入步骤。
//
// 写入使用状态前置条件：只有步骤仍处于可再次执行或正在执行的状态时才改写，已确认的
// 终态（completed / cancelled / skipped）不会被覆盖。
func (s *StepWorkflowService) applyRepairCloseout(
	ctx context.Context,
	result ExecuteAndValidateResult,
	closeout domain.RepairCloseoutDecision,
) (ExecuteAndValidateResult, error) {
	step, err := s.store.GetStep(ctx, result.StepID)
	if err != nil {
		return result, fmt.Errorf("读取步骤失败: %w", err)
	}
	result.StepStatus = step.Status
	if step.Status == closeout.Status {
		// 上一轮已经把步骤收口为同一状态：补写失败摘要，但不重复迁移状态。
		return s.updateRepairStepSummary(ctx, step, closeout, result)
	}
	if !repairableStepStatus(step.Status) {
		// 现场已被其他操作推进：不覆盖，如实返回数据库值。
		return result, nil
	}
	previous := step.Status
	step.Result = closeout.Summary
	step.FailureStage = ""
	step.FailureSummary = ""
	if closeout.Status != domain.StepStatusCompleted {
		step.FailureStage = "verify"
		step.FailureSummary = closeout.Summary
	}
	if err := step.Transition(closeout.Status, s.now()); err != nil {
		return result, fmt.Errorf("标记步骤修复终态失败: %w", err)
	}
	// 可再次执行与终态都必须释放占用，否则用户显式重新执行会被占用挡住。
	if closeout.Status.CanStartExecution() || closeout.Status.IsTerminal() {
		if err := step.SetCurrentAttempt(nil); err != nil {
			return result, err
		}
	}
	if err := s.store.UpdateStepIfStatus(ctx, *step, []domain.StepStatus{previous}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := s.store.GetStep(ctx, step.ID)
			if readErr != nil {
				return result, fmt.Errorf("读取步骤终态失败: %w", readErr)
			}
			result.StepStatus = current.Status
			return result, nil
		}
		return result, fmt.Errorf("写入步骤修复终态失败: %w", err)
	}
	result.StepStatus = step.Status
	return result, nil
}

// updateRepairStepSummary 在步骤已处于目标状态时补写失败摘要。
//
// 它只更新说明字段，不做状态迁移：达到尝试上限这类结论落在上一轮已经写入的 failed
// 状态上，用户需要看到"为什么停在这里"，但状态本身不需要再变一次。
func (s *StepWorkflowService) updateRepairStepSummary(
	ctx context.Context,
	step *domain.Step,
	closeout domain.RepairCloseoutDecision,
	result ExecuteAndValidateResult,
) (ExecuteAndValidateResult, error) {
	if closeout.Status == domain.StepStatusCompleted || step.FailureSummary == closeout.Summary {
		return result, nil
	}
	previous := step.Status
	step.Result = closeout.Summary
	step.FailureStage = "verify"
	step.FailureSummary = closeout.Summary
	if err := s.store.UpdateStepIfStatus(ctx, *step, []domain.StepStatus{previous}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return result, nil
		}
		return result, fmt.Errorf("写入步骤失败摘要失败: %w", err)
	}
	result.StepStatus = step.Status
	return result, nil
}

// repairableStepStatus 判断步骤当前状态是否允许修复循环写入最终收口结论。
func repairableStepStatus(status domain.StepStatus) bool {
	switch status {
	case domain.StepStatusRunning,
		domain.StepStatusDeveloping,
		domain.StepStatusVerifying,
		domain.StepStatusFailed,
		domain.StepStatusRecoveryRequired,
		domain.StepStatusRetryWait:
		return true
	default:
		return false
	}
}

// appendRepairEvent 追加一条修复轮次事件。
//
// 事件承载"第几轮、上限多少、对应哪次 Attempt 与 Validation Run、为什么继续或停止"，
// 使操作者可以事后追溯整条修复链。写入失败只记录日志，不改变权威业务状态。
func (s *StepWorkflowService) appendRepairEvent(
	ctx context.Context,
	result ExecuteAndValidateResult,
	decision domain.RepairDecision,
	round int,
	attempts int,
	maxAttempts int,
) {
	if result.TaskID.IsEmpty() {
		return
	}
	payload := map[string]any{
		"round":                round,
		"attempts":             attempts,
		"max_attempts":         maxAttempts,
		"attempt_id":           result.AttemptID.String(),
		"session_id":           result.SessionID.String(),
		"validation_run_id":    result.ValidationRunID.String(),
		"validation_status":    string(result.ValidationStatus),
		"verification_verdict": string(result.ValidationVerdict),
		"verification_state":   string(result.Verification),
		"decision":             string(decision.Kind),
		"repair_class":         string(decision.Class),
		"repair_summary":       decision.Summary,
		"step_status":          string(result.StepStatus),
		"feedback_persisted":   result.FeedbackPersisted,
		"infrastructure_error": result.InfrastructureError,
		"final":                !decision.CanRepair(),
	}
	s.appendCloseoutEvent(ctx, result, domain.EventTypeRepairRound, payload)
}
