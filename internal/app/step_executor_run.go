package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// runAttempt 执行一次真实 Agent 调用并持久化最终状态。
//
// 无论调用以何种方式结束，都会返回一个结果对象：失败、超时与取消不是"没有结果"，
// 而是有明确结论的结果。持久化错误不会阻止返回值，而是记录在 FinalizationErrors
// 中，使调用方看到"执行事实"与"状态写入是否完整"两个独立事实。
func (e *StepExecutor) runAttempt(
	ctx context.Context,
	request runAttemptRequest,
) StepExecutionResult {
	run := e.callAgent(ctx, request)
	outcome, summary, failure := classifyStepExecutionRun(run, ctx)
	result := StepExecutionResult{
		TaskID:             request.task.ID,
		StepID:             request.step.ID,
		AttemptID:          request.attempt.ID,
		SessionID:          run.session.ID,
		Outcome:            outcome,
		Summary:            summary,
		Failure:            failure,
		ExitCode:           run.result.ExitCode,
		SessionStatus:      run.session.Status,
		SessionRunStatus:   run.session.RunStatus,
		ArchiveStatus:      run.session.ArchiveStatus,
		ExportStatus:       run.session.ExportStatus,
		AttemptStatus:      outcome.AttemptStatus(),
		StepStatus:         stepStatusOfOutcome(outcome, outcome.AttemptStatus()),
		TaskStatus:         domain.TaskStatusDeveloping,
		StartedAt:          run.result.StartedAt,
		FinishedAt:         run.result.FinishedAt,
		FinalizationErrors: nil,
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = request.attempt.StartedAt
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = e.now()
	}
	finalization := e.finalizeExecution(ctx, finalizeRequest{
		task:    request.task,
		step:    request.step,
		attempt: request.attempt,
		run:     run,
		outcome: outcome,
		summary: summary,
		failure: failure,
	})
	result.AttemptStatus = finalization.attemptStatus
	result.StepStatus = finalization.stepStatus
	result.TaskStatus = finalization.taskStatus
	result.FinalizationErrors = finalization.errs
	result.Finalized = len(finalization.errs) == 0
	return result
}

// callAgent 通过容器内执行端口调用 Pi，并把会话记录一并返回。
//
// 调用失败时仍然尽力返回可用的会话记录：Session 是排查问题的唯一线索，
// 不能因为执行失败而丢弃。
func (e *StepExecutor) callAgent(
	ctx context.Context,
	request runAttemptRequest,
) stepExecutionRun {
	agentResult, runErr := e.containers.RunAgent(ctx, ContainerAgentRequest{
		ContainerID:  request.containerID,
		TaskID:       request.task.ID.String(),
		StepID:       request.step.ID.String(),
		AttemptID:    request.attempt.ID.String(),
		RoleType:     request.role.Type.String(),
		Model:        request.role.Model,
		SystemPrompt: request.role.SystemPrompt,
		Prompt:       request.prompt,
		SessionRoot:  e.sessions.LocalRoot(),
		Timeout:      request.timeout,
	})
	run := stepExecutionRun{result: agentResult.Result, runErr: runErr}
	sessionID := domain.ID(strings.TrimSpace(agentResult.Result.SessionID))
	if sessionID.IsEmpty() {
		e.logger.ErrorContext(
			ctx,
			"Agent 调用未返回会话标识",
			slog.String("task_id", request.task.ID.String()),
			slog.String("step_id", request.step.ID.String()),
			slog.String("attempt_id", request.attempt.ID.String()),
			slog.String("error", errorSummary(runErr)),
		)
		return run
	}
	session, loadErr := e.sessions.Load(context.WithoutCancel(ctx), sessionID)
	if loadErr != nil {
		// 会话元数据无法读取时不伪造记录：执行事实通过返回结果与日志暴露，
		// 会话状态留空，调用方据此判断"执行完成但记录不完整"。
		e.logger.ErrorContext(
			ctx,
			"读取会话记录失败",
			slog.String("session_id", sessionID.String()),
			slog.String("task_id", request.task.ID.String()),
			slog.String("step_id", request.step.ID.String()),
			slog.String("error", loadErr.Error()),
		)
		return run
	}
	run.session = session
	return run
}

// finalizeRequest 是一次最终状态写入的输入。
type finalizeRequest struct {
	task    domain.Task
	step    domain.Step
	attempt domain.Attempt
	run     stepExecutionRun
	outcome domain.StepExecutionOutcome
	summary string
	failure string
}

// finalizeOutcome 描述最终状态写入的结果。
type finalizeOutcome struct {
	attemptStatus domain.AttemptStatus
	stepStatus    domain.StepStatus
	taskStatus    domain.TaskStatus
	errs          []string
}

// finalizeExecution 把真实执行结果写入 Attempt、Step 与 Task。
//
// 三条写入各自独立执行并使用"未取消的上下文"：取消或超时恰恰是最需要写入终态的
// 场景。任一写入失败都会被记录，且不会阻止其余写入——避免普通执行错误阻止状态清理。
func (e *StepExecutor) finalizeExecution(
	ctx context.Context,
	request finalizeRequest,
) finalizeOutcome {
	writeCtx := context.WithoutCancel(ctx)
	outcome := finalizeOutcome{
		attemptStatus: request.outcome.AttemptStatus(),
		stepStatus:    stepStatusOfOutcome(request.outcome, request.outcome.AttemptStatus()),
		taskStatus:    request.task.Status,
	}
	attempt, attemptErr := e.finishAttempt(writeCtx, request)
	if attemptErr != nil {
		outcome.errs = append(outcome.errs, attemptErr.Error())
		e.logFinalizeFailure(writeCtx, "写入执行尝试终态失败", request, attemptErr)
	} else {
		outcome.attemptStatus = attempt.Status
	}
	step, stepErr := e.finishStep(writeCtx, request, outcome.attemptStatus)
	if stepErr != nil {
		outcome.errs = append(outcome.errs, stepErr.Error())
		e.logFinalizeFailure(writeCtx, "写入步骤终态失败", request, stepErr)
	} else {
		outcome.stepStatus = step.Status
	}
	task, taskErr := e.finishTask(writeCtx, request, outcome.attemptStatus)
	if taskErr != nil {
		outcome.errs = append(outcome.errs, taskErr.Error())
		e.logFinalizeFailure(writeCtx, "写入任务终态失败", request, taskErr)
	} else {
		outcome.taskStatus = task.Status
	}
	e.appendEvents(writeCtx, request, outcome)
	e.logger.InfoContext(
		writeCtx,
		"步骤执行已结束",
		slog.String("task_id", request.task.ID.String()),
		slog.String("step_id", request.step.ID.String()),
		slog.String("attempt_id", request.attempt.ID.String()),
		slog.String("session_id", request.run.session.ID.String()),
		slog.String("outcome", string(request.outcome)),
		slog.String("attempt_status", string(outcome.attemptStatus)),
		slog.String("step_status", string(outcome.stepStatus)),
		slog.String("task_status", string(outcome.taskStatus)),
		slog.Bool("finalized", len(outcome.errs) == 0),
	)
	return outcome
}

// finishAttempt 把执行结论写入执行尝试。
//
// 已经进入终态的尝试不会被改写：迟到结果不能覆盖已确认的事实，此时保留数据库值。
func (e *StepExecutor) finishAttempt(
	ctx context.Context,
	request finalizeRequest,
) (domain.Attempt, error) {
	attempt := request.attempt
	attempt.ExitCode = request.run.result.ExitCode
	if !request.run.session.ID.IsEmpty() {
		if err := attempt.AddSession(request.run.session.ID); err != nil {
			return attempt, fmt.Errorf("关联会话失败: %w", err)
		}
	}
	if attempt.Status != domain.AttemptStatusRunning {
		return attempt, nil
	}
	at := request.run.result.FinishedAt
	if at.IsZero() {
		at = e.now()
	}
	var markErr error
	switch request.outcome {
	case domain.StepExecutionCompleted:
		markErr = attempt.Complete(at)
	case domain.StepExecutionTimedOut:
		markErr = attempt.Timeout(request.summary, at)
	case domain.StepExecutionCancelled:
		markErr = attempt.Cancel(request.summary, at)
	case domain.StepExecutionInterrupted:
		markErr = attempt.Interrupt(request.summary, at)
	default:
		summary := strings.TrimSpace(request.failure)
		if summary == "" {
			summary = "Agent 调用失败"
		}
		markErr = attempt.Fail(summary, at)
	}
	if markErr != nil {
		return attempt, fmt.Errorf("标记执行尝试终态失败: %w", markErr)
	}
	// 成功路径不写入失败分类与失败阶段：它们只描述失败原因，留在成功的尝试上会误导诊断。
	if request.outcome != domain.StepExecutionCompleted {
		attempt.FailureStage = "execute"
		attempt.ErrorClass = string(errorClassOf(request.outcome))
		attempt.FailureSummary = failureSummary(request)
	}
	if err := e.store.UpdateAttemptIfStatus(
		ctx,
		attempt,
		[]domain.AttemptStatus{domain.AttemptStatusRunning},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return e.rereadAttempt(ctx, attempt)
		}
		return attempt, fmt.Errorf("写入执行尝试终态失败: %w", err)
	}
	return attempt, nil
}

// rereadAttempt 在条件更新冲突时读取数据库中的真实终态。
//
// 冲突说明已有其他写入确认了终态；此时返回数据库值而不是覆盖它。
func (e *StepExecutor) rereadAttempt(
	ctx context.Context,
	attempt domain.Attempt,
) (domain.Attempt, error) {
	current, err := e.store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		return attempt, fmt.Errorf("读取执行尝试终态失败: %w", err)
	}
	return *current, nil
}

// finishStep 把执行结论写入步骤。
func (e *StepExecutor) finishStep(
	ctx context.Context,
	request finalizeRequest,
	attemptStatus domain.AttemptStatus,
) (domain.Step, error) {
	step := request.step
	if step.Status != domain.StepStatusRunning {
		return step, nil
	}
	at := request.run.result.FinishedAt
	if at.IsZero() {
		at = e.now()
	}
	step.FailureStage = "execute"
	step.Result = request.summary
	failure := failureSummary(request)
	if request.outcome == domain.StepExecutionCompleted {
		step.FailureStage = ""
		step.FailureSummary = ""
	}
	next := stepStatusOfOutcome(request.outcome, attemptStatus)
	if err := step.Transition(next, at); err != nil {
		return step, fmt.Errorf("标记步骤终态失败: %w", err)
	}
	if next.IsTerminal() {
		if err := step.SetCurrentAttempt(nil); err != nil {
			return step, err
		}
	}
	if next == domain.StepStatusFailed || next == domain.StepStatusCancelled {
		step.FailureSummary = failure
	}
	if err := e.store.UpdateStepIfStatus(
		ctx,
		step,
		[]domain.StepStatus{domain.StepStatusRunning},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := e.store.GetStep(ctx, step.ID)
			if readErr != nil {
				return step, fmt.Errorf("读取步骤终态失败: %w", readErr)
			}
			return *current, nil
		}
		return step, fmt.Errorf("写入步骤终态失败: %w", err)
	}
	return step, nil
}

// finishTask 按现有任务状态机推进任务状态。
//
// 它显式拒绝把单个步骤的成功当作任务成功：任务状态只会进入"仍在执行"或
// "需要恢复核验"，最终结论留给验证与代码保存阶段。
func (e *StepExecutor) finishTask(
	ctx context.Context,
	request finalizeRequest,
	attemptStatus domain.AttemptStatus,
) (domain.Task, error) {
	task := request.task
	next := request.task.Status
	switch request.outcome {
	case domain.StepExecutionCompleted:
		// 单个步骤成功不代表任务成功：任务保持 developing，等待验证与代码保存。
		next = domain.TaskStatusDeveloping
	case domain.StepExecutionTimedOut, domain.StepExecutionFailed:
		if task.Status.AllowsStepExecution() {
			if err := task.Transition(domain.TaskStatusRecoveryRequired, e.now()); err == nil {
				next = domain.TaskStatusRecoveryRequired
				task.FailureSummary = failureSummary(request)
			}
		}
	case domain.StepExecutionCancelled:
		next, task = e.cancelTask(ctx, request)
	case domain.StepExecutionInterrupted:
		// 服务关闭或宿主异常导致的执行中断不是用户取消：现场仍保留，但结果无法
		// 自动确认，因此收敛为 need recovery，不写入"已取消"的结论。
		if err := e.requireTaskRecovery(&task, request); err == nil {
			next = domain.TaskStatusRecoveryRequired
		}
	default:
		next = request.task.Status
	}
	if next == domain.TaskStatusDeveloping && task.Status != domain.TaskStatusDeveloping {
		if err := task.Transition(next, e.now()); err != nil {
			return task, fmt.Errorf("标记任务状态失败: %w", err)
		}
	}
	if next == domain.TaskStatusCancelled {
		return e.persistCancelledTask(ctx, task, request.task.Status)
	}
	if next == domain.TaskStatusRecoveryRequired {
		if err := e.persistTaskStatus(ctx, task, next, request.task.Status); err != nil {
			return task, err
		}
		return task, nil
	}
	if err := e.store.UpdateTaskIfStatus(
		ctx,
		task,
		[]domain.TaskStatus{request.task.Status},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := e.store.GetTask(ctx, task.ID)
			if readErr != nil {
				return task, fmt.Errorf("读取任务终态失败: %w", readErr)
			}
			return *current, nil
		}
		return task, fmt.Errorf("写入任务状态失败: %w", err)
	}
	return task, nil
}

// cancelTask 把取消请求落盘并按状态机收敛为 cancelling 再到 cancelled。
func (e *StepExecutor) cancelTask(
	ctx context.Context,
	request finalizeRequest,
) (domain.TaskStatus, domain.Task) {
	task := request.task
	if !task.CancelRequested {
		if err := task.RequestCancel(e.now()); err != nil {
			return task.Status, task
		}
	}
	if task.Status != domain.TaskStatusCancelling {
		if err := task.Transition(domain.TaskStatusCancelling, e.now()); err != nil {
			// 状态机不允许时保持原状态：不伪造"已取消"的结论。
			return task.Status, task
		}
	}
	if err := task.Finish(
		domain.TaskResultCancelled,
		"步骤执行被取消，容器现场保留",
		e.now(),
	); err != nil {
		return task.Status, task
	}
	if err := task.Transition(domain.TaskStatusCancelled, e.now()); err != nil {
		return task.Status, task
	}
	return domain.TaskStatusCancelled, task
}

// persistCancelledTask 写入已收敛为 cancelled 的任务。
func (e *StepExecutor) persistCancelledTask(
	ctx context.Context,
	task domain.Task,
	expected domain.TaskStatus,
) (domain.Task, error) {
	if err := e.store.UpdateTaskIfStatus(ctx, task, []domain.TaskStatus{expected}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := e.store.GetTask(ctx, task.ID)
			if readErr != nil {
				return task, fmt.Errorf("读取任务终态失败: %w", readErr)
			}
			return *current, nil
		}
		return task, fmt.Errorf("写入任务状态失败: %w", err)
	}
	return task, nil
}

// persistTaskStatus 推进任务到指定状态并写入。
func (e *StepExecutor) persistTaskStatus(
	ctx context.Context,
	task domain.Task,
	next, expected domain.TaskStatus,
) error {
	if task.Status != next {
		if err := task.Transition(next, e.now()); err != nil {
			return fmt.Errorf("标记任务状态失败: %w", err)
		}
	}
	if err := e.store.UpdateTaskIfStatus(ctx, task, []domain.TaskStatus{expected}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf("写入任务状态失败: 状态已被其他操作推进: %w", err)
		}
		return fmt.Errorf("写入任务状态失败: %w", err)
	}
	return nil
}

// requireTaskRecovery 把任务标记为需要恢复核验。
func (e *StepExecutor) requireTaskRecovery(task *domain.Task, request finalizeRequest) error {
	if err := task.Transition(domain.TaskStatusRecoveryRequired, e.now()); err != nil {
		return err
	}
	task.FailureSummary = failureSummary(request)
	return nil
}

// appendEvents 追加执行结论事件。
//
// 事件缺失不影响执行状态：写入失败只记录日志，不会把已确认的结果改回失败。
func (e *StepExecutor) appendEvents(
	ctx context.Context,
	request finalizeRequest,
	outcome finalizeOutcome,
) {
	eventType := domain.EventTypeStepCompleted
	switch request.outcome {
	case domain.StepExecutionCompleted:
		eventType = domain.EventTypeStepCompleted
	case domain.StepExecutionCancelled:
		eventType = domain.EventTypeTaskCancelRequested
	case domain.StepExecutionInterrupted:
		eventType = domain.EventTypeAttemptInterrupted
	default:
		eventType = domain.EventTypeAttemptFailed
	}
	sessionID := request.run.session.ID
	var sessionPtr *domain.ID
	if !sessionID.IsEmpty() {
		sessionPtr = &sessionID
	}
	payload := map[string]any{
		"outcome":        string(request.outcome),
		"attempt_status": string(outcome.attemptStatus),
		"step_status":    string(outcome.stepStatus),
		"task_status":    string(outcome.taskStatus),
		"exit_code":      request.run.result.ExitCode,
	}
	if summary := failureSummary(request); summary != "" {
		payload["summary"] = summary
	}
	if _, err := e.appendEvent(
		ctx,
		request.task,
		&request.step.ID,
		&request.attempt.ID,
		sessionPtr,
		eventType,
		payload,
	); err != nil {
		e.logger.WarnContext(
			ctx,
			"记录执行结果事件失败",
			slog.String("task_id", request.task.ID.String()),
			slog.String("step_id", request.step.ID.String()),
			slog.String("attempt_id", request.attempt.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// appendEvent 追加任务事件。
func (e *StepExecutor) appendEvent(
	ctx context.Context,
	task domain.Task,
	stepID, attemptID, sessionID *domain.ID,
	eventType domain.EventType,
	payload map[string]any,
) (domain.Event, error) {
	event, err := domain.NewEvent(
		task.ID,
		stepID,
		attemptID,
		sessionID,
		eventType,
		marshalStepExecutionPayload(payload),
		e.now(),
	)
	if err != nil {
		return domain.Event{}, fmt.Errorf("构造任务事件失败: %w", err)
	}
	stored, err := e.store.AppendEvent(ctx, event)
	if err != nil {
		return domain.Event{}, fmt.Errorf("写入任务事件失败: %w", err)
	}
	return stored, nil
}

// logFinalizeFailure 记录最终状态写入失败。
func (e *StepExecutor) logFinalizeFailure(
	ctx context.Context,
	message string,
	request finalizeRequest,
	err error,
) {
	e.logger.ErrorContext(
		ctx,
		message,
		slog.String("task_id", request.task.ID.String()),
		slog.String("step_id", request.step.ID.String()),
		slog.String("attempt_id", request.attempt.ID.String()),
		slog.String("session_id", request.run.session.ID.String()),
		slog.String("outcome", string(request.outcome)),
		slog.String("error", err.Error()),
	)
}

// classifyStepExecutionRun 把执行端口的原始结果映射为执行结论。
func classifyStepExecutionRun(
	run stepExecutionRun,
	ctx context.Context,
) (domain.StepExecutionOutcome, string, string) {
	status := run.result.Status
	if status == "" {
		// 执行端口没有给出运行结果时，只能依据上下文与错误判断结论；
		// 这种情况绝不能被当成"完成"。
		return classifyMissingResult(run, ctx)
	}
	switch status {
	case agent.RunCompleted:
		// 成功必须可核验：没有真实运行痕迹的"成功"按失败处理，不伪造完成。
		if startedErr := checkAgentStarted(run); startedErr != nil {
			return domain.StepExecutionFailed, startedErr.Error(), startedErr.Error()
		}
		return domain.StepExecutionCompleted, completedSummary(run.result), ""
	case agent.RunTimedOut:
		return domain.StepExecutionTimedOut, sessionSummary(run.result), failureText(run, "Agent 调用超时")
	case agent.RunCancelled:
		// 服务关闭与调用方取消都表现为取消，但必须区分：前者是进程中断。
		if isServiceStop(ctx) {
			return domain.StepExecutionInterrupted, sessionSummary(run.result), failureText(run, "服务已停止")
		}
		return domain.StepExecutionCancelled, sessionSummary(run.result), failureText(run, "调用方取消执行")
	default:
		return domain.StepExecutionFailed, sessionSummary(run.result), failureText(run, "Agent 调用失败")
	}
}

// classifyMissingResult 在没有运行结果时按上下文与错误推断结论。
//
// 缺少运行结果意味着执行器没有报告真实结束状态，因此只会得到失败、超时或取消，
// 不可能得到"完成"。
func classifyMissingResult(
	run stepExecutionRun,
	ctx context.Context,
) (domain.StepExecutionOutcome, string, string) {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.StepExecutionTimedOut, "调用超时被终止", failureText(run, "Agent 调用超时")
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		if isServiceStop(ctx) {
			return domain.StepExecutionInterrupted, "服务已停止", failureText(run, "服务已停止")
		}
		return domain.StepExecutionCancelled, "调用方取消执行", failureText(run, "调用方取消执行")
	}
	if errors.Is(run.runErr, context.DeadlineExceeded) {
		return domain.StepExecutionTimedOut, "调用超时被终止", failureText(run, "Agent 调用超时")
	}
	if errors.Is(run.runErr, context.Canceled) {
		return domain.StepExecutionCancelled, "调用方取消执行", failureText(run, "调用方取消执行")
	}
	return domain.StepExecutionFailed, "Agent 调用未返回运行结果", failureText(run, "Agent 调用失败")
}

// completedSummary 生成成功摘要。
func completedSummary(result agent.RunResult) string {
	text := strings.TrimSpace(result.FinalText)
	if text == "" {
		return "Agent 调用已完成"
	}
	return "Agent 调用已完成: " + agent.TruncateText(text, 1024)
}

// sessionSummary 生成失败或中断摘要。
func sessionSummary(result agent.RunResult) string {
	if summary := strings.TrimSpace(result.Failure); summary != "" {
		return agent.TruncateText(summary, 1024)
	}
	return "Agent 调用未正常完成: " + string(result.Status)
}

// failureText 组合失败原因，优先使用执行器自身给出的诊断。
func failureText(run stepExecutionRun, fallback string) string {
	for _, candidate := range []string{run.result.Failure, errorSummary(run.runErr)} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return agent.TruncateText(trimmed, 1024)
		}
	}
	return fallback
}

// failureSummary 返回写入步骤与任务的失败摘要。
func failureSummary(request finalizeRequest) string {
	if request.outcome == domain.StepExecutionCompleted {
		return ""
	}
	if summary := strings.TrimSpace(request.failure); summary != "" {
		return summary
	}
	if summary := strings.TrimSpace(request.summary); summary != "" {
		return summary
	}
	return "步骤执行未成功"
}

// stepStatusOfOutcome 把执行结论映射为步骤终态。
//
// 失败的步骤按现有状态机进入 failed 并使用 failed 作为终态；超时与中断同样以
// failed 表达，同时在尝试记录中保留更精确的 timed_out / interrupted 状态。
func stepStatusOfOutcome(
	outcome domain.StepExecutionOutcome,
	attemptStatus domain.AttemptStatus,
) domain.StepStatus {
	switch outcome {
	case domain.StepExecutionCompleted:
		return domain.StepStatusCompleted
	case domain.StepExecutionCancelled:
		return domain.StepStatusCancelled
	default:
		if attemptStatus == domain.AttemptStatusCancelled {
			return domain.StepStatusCancelled
		}
		return domain.StepStatusFailed
	}
}

// errorClassOf 把执行结论映射为可复用的错误类别。
func errorClassOf(outcome domain.StepExecutionOutcome) domain.RetryErrorClass {
	switch outcome {
	case domain.StepExecutionTimedOut:
		return domain.RetryClassTimeout
	default:
		return domain.RetryClassAgentFailure
	}
}

// errorSummary 返回错误的紧凑摘要，空错误返回空字符串。
func errorSummary(err error) string {
	if err == nil {
		return ""
	}
	return agent.TruncateText(strings.TrimSpace(err.Error()), 512)
}

// marshalStepExecutionPayload 把事件负载编码为 JSON，失败时退化为空对象。
func marshalStepExecutionPayload(payload map[string]any) []byte {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// isServiceStop 判断取消是否由后台服务停止引起。
func isServiceStop(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	cause := context.Cause(ctx)
	return errors.Is(cause, ErrAgentRunnerStopped) ||
		errors.Is(cause, ErrStepExecutorStopped)
}
