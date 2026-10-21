package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// runValidationRequest 是一次验证执行的内部输入。
type runValidationRequest struct {
	task      domain.Task
	step      domain.Step
	attempt   domain.Attempt
	spec      domain.ValidationSpec
	container domain.Container
	executor  CheckExecutor
	timeout   time.Duration
}

// runValidation 执行一次完整验证并持久化全部事实。
//
// 顺序固定为：登记在途验证 → 原子占用并创建验证记录 → 逐项执行检查并逐项落盘 →
// 计算确定性结论 → 写入结论 → 把判定写回执行尝试 → 追加任务事件。
//
// 每一步的失败都不会静默吞掉：检查结果逐项写入，因此"最后一项检查失败"不会让
// 此前的结果丢失；结论写入失败会让 Persisted 为 false，绝不会被报告成成功。
func (s *ValidationService) runValidation(
	ctx context.Context,
	request runValidationRequest,
) (ValidationExecuteResult, error) {
	runCtx, finishRun, runErr := s.beginRun(ctx)
	if runErr != nil {
		return ValidationExecuteResult{}, runErr
	}
	defer finishRun()
	// 整次验证的硬上限：超时后剩余检查记为 skipped，流程收敛为 timed_out。
	// 超时与调用方取消必须可区分，因此使用带原因的取消而不是裸 deadline。
	runCtx, cancelTimeout := context.WithTimeoutCause(
		runCtx,
		request.timeout,
		context.DeadlineExceeded,
	)
	defer cancelTimeout()

	release, acquired := s.running.acquire(request.attempt.ID)
	if !acquired {
		return ValidationExecuteResult{}, fmt.Errorf(
			"%w: 尝试 %s 正在验证中",
			ErrValidationConflict,
			request.attempt.ID,
		)
	}
	defer release()

	run, err := s.beginValidationRun(runCtx, request)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	s.logger.InfoContext(
		runCtx,
		"验证已开始",
		slog.String("task_id", request.task.ID.String()),
		slog.String("step_id", request.step.ID.String()),
		slog.String("attempt_id", request.attempt.ID.String()),
		slog.String("validation_run_id", run.ID.String()),
		slog.Int("checks", run.TotalChecks),
	)

	execution := s.executeChecks(runCtx, run, request)
	decision := domain.DecideValidation(domain.ValidationDecisionInput{
		Spec:      request.spec,
		Results:   execution.results,
		RunStatus: domain.ValidationRunStatusCompleted,
	})
	if terminal := execution.terminalStatus(); terminal != domain.ValidationRunStatusCompleted {
		// 验证流程本身没有跑完（取消、超时或服务停止）：此时结论只能是 failed，
		// 由 DecideValidation 依据 RunStatus 给出，不需要另写一套判定。
		decision = domain.DecideValidation(domain.ValidationDecisionInput{
			Spec:      request.spec,
			Results:   execution.results,
			RunStatus: terminal,
		})
	}
	return s.finalizeValidation(runCtx, run, request, execution, decision)
}

// beginValidationRun 原子占用并创建验证记录。
func (s *ValidationService) beginValidationRun(
	ctx context.Context,
	request runValidationRequest,
) (domain.ValidationRun, error) {
	specSnapshot := marshalSpecSnapshot(request.spec)
	run, err := s.store.BeginValidationRun(ctx, storage.ValidationRunStartRequest{
		TaskID:             request.task.ID,
		StepID:             request.step.ID,
		AttemptID:          request.attempt.ID,
		ExpectedTaskStatus: request.task.Status,
		Spec:               request.spec,
		SpecSnapshot:       specSnapshot,
		ExecutionEnv:       request.executor.Name(),
		Workspace:          request.task.Config.EffectiveWorkDir(),
		ContainerID:        request.container.ID.String(),
		At:                 s.now(),
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrConflict):
			return domain.ValidationRun{}, fmt.Errorf(
				"%w: 尝试 %s 已有进行中的验证或任务状态已变更",
				ErrValidationConflict,
				request.attempt.ID,
			)
		case errors.Is(err, storage.ErrNotFound):
			return domain.ValidationRun{}, fmt.Errorf(
				"%w: 验证归属记录已不存在",
				ErrValidationNotFound,
			)
		}
		return domain.ValidationRun{}, fmt.Errorf("创建验证记录失败: %w", err)
	}
	if _, err := s.appendEvent(ctx, request.task, &request.step.ID, &request.attempt.ID, nil,
		domain.EventTypeVerificationStarted,
		map[string]any{
			"validation_run_id": run.ID.String(),
			"execution_env":     request.executor.Name(),
			"workspace":         request.task.Config.EffectiveWorkDir(),
			"total_checks":      run.TotalChecks,
		},
	); err != nil {
		// 事件写入失败不回滚验证记录：验证本身已经持久化，缺失的只是时间线条目。
		s.logger.WarnContext(
			ctx,
			"记录验证开始事件失败",
			slog.String("validation_run_id", run.ID.String()),
			slog.String("error", err.Error()),
		)
	}
	return run, nil
}

// checkExecution 是一次检查执行的整体结果。
type checkExecution struct {
	results []domain.ValidationCheckResult
	// cancelled 表示验证被调用方取消。
	cancelled bool
	// timedOut 表示验证整体超时。
	timedOut bool
	// interrupted 表示服务停止导致验证中断。
	interrupted bool
	// stopReason 是流程未跑完的原因。
	stopReason string
}

// terminalStatus 返回验证流程的终态。
func (e checkExecution) terminalStatus() domain.ValidationRunStatus {
	switch {
	case e.interrupted:
		return domain.ValidationRunStatusFailed
	case e.timedOut:
		return domain.ValidationRunStatusTimedOut
	case e.cancelled:
		return domain.ValidationRunStatusCancelled
	default:
		return domain.ValidationRunStatusCompleted
	}
}

// executeChecks 按配置顺序执行全部启用的检查，并逐项落盘结果。
//
// 任何一条检查失败都不会中断后续检查：完整记录全部结果比提前退出更有价值。
// 只有调用方取消、整体超时或服务停止才会停止执行，此时剩余检查记为 skipped 并
// 说明原因——未执行的必需检查不会被当作通过。
func (s *ValidationService) executeChecks(
	ctx context.Context,
	run domain.ValidationRun,
	request runValidationRequest,
) checkExecution {
	ordered := request.spec.Ordered()
	execution := checkExecution{results: make([]domain.ValidationCheckResult, 0, len(ordered))}
	for _, definition := range ordered {
		if status, reason, stopped := stopStateOf(ctx); stopped {
			execution.markStopped(status, reason)
			execution.appendSkipped(ctx, s, run, definition, reason)
			continue
		}
		result := s.runOneCheck(ctx, run, request, definition)
		execution.results = append(execution.results, result)
	}
	// 再次确认上下文状态：最后一条检查结束与取消同时发生时，流程状态仍必须反映
	// 真实的触发来源，不能被"检查已经跑完"掩盖。
	if status, reason, stopped := stopStateOf(ctx); stopped && !execution.stopped() {
		execution.markStopped(status, reason)
	}
	return execution
}

// stopped 判断流程是否已经被中断。
func (e checkExecution) stopped() bool {
	return e.cancelled || e.timedOut || e.interrupted
}

// markStopped 记录流程中断的原因。
func (e *checkExecution) markStopped(status domain.ValidationRunStatus, reason string) {
	switch status {
	case domain.ValidationRunStatusCancelled:
		e.cancelled = true
	case domain.ValidationRunStatusTimedOut:
		e.timedOut = true
	default:
		e.interrupted = true
	}
	if e.stopReason == "" {
		e.stopReason = reason
	}
}

// appendSkipped 记录一条因流程中断而未执行的检查。
func (execution *checkExecution) appendSkipped(
	ctx context.Context,
	service *ValidationService,
	run domain.ValidationRun,
	definition domain.ValidationCheckDefinition,
	reason string,
) {
	result := skippedCheckResult(run.ID, definition, reason, service.now())
	execution.results = append(execution.results, result)
	if err := service.store.AppendValidationCheckResult(ctx, result); err != nil {
		service.logger.ErrorContext(
			ctx,
			"写入跳过检查结果失败",
			slog.String("validation_run_id", run.ID.String()),
			slog.String("check_id", definition.ID),
			slog.String("error", err.Error()),
		)
	}
}

// runOneCheck 执行单条检查并落盘结果。
func (s *ValidationService) runOneCheck(
	ctx context.Context,
	run domain.ValidationRun,
	request runValidationRequest,
	definition domain.ValidationCheckDefinition,
) domain.ValidationCheckResult {
	startedAt := s.now()
	checkRequest := CheckExecutionRequest{
		CheckID:        definition.ID,
		CheckName:      definition.Name,
		Command:        definition.Command,
		Args:           definition.Args,
		WorkDir:        definition.WorkDir,
		Timeout:        checkTimeoutOf(definition, request.spec),
		MaxOutputBytes: checkMaxOutputOf(definition, request.spec),
	}
	executionResult, execErr := request.executor.Exec(ctx, checkRequest)
	finishedAt := s.now()
	status, reason := evaluateCheckStatus(definition, executionResult, execErr)
	result := domain.ValidationCheckResult{
		ID:              domain.NewID(),
		ValidationRunID: run.ID,
		CheckID:         definition.ID,
		CheckName:       definition.Name,
		Required:        definition.IsRequired(),
		Sequence:        definition.Sequence,
		Definition:      definition,
		Status:          status,
		ExitCode:        executionResult.ExitCode,
		StdoutSummary:   summarizeOutput(executionResult.Stdout),
		StderrSummary:   summarizeOutput(executionResult.Stderr),
		OutputTruncated: executionResult.Truncated,
		TimedOut:        executionResult.TimedOut,
		Cancelled:       executionResult.Cancelled,
		Certain:         executionResult.Certain,
		FailureReason:   reason,
		ErrorDetail:     errorSummary(execErr),
		Duration:        executionResult.Duration,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		CreatedAt:       finishedAt,
	}
	if result.Duration <= 0 {
		result.Duration = finishedAt.Sub(startedAt)
	}
	// 输出落盘与判定解耦：归档失败只记录原因，不改变退出码与检查状态。
	stdoutPath, stderrPath, archiveErr := s.outputs.archive(run.ID, definition.ID, executionResult)
	result.StdoutPath = stdoutPath
	result.StderrPath = stderrPath
	result.OutputArchiveError = archiveErr
	if archiveErr != "" {
		s.logger.WarnContext(
			ctx,
			"检查输出归档失败",
			slog.String("validation_run_id", run.ID.String()),
			slog.String("check_id", definition.ID),
			slog.String("error", archiveErr),
		)
	}
	if err := s.store.AppendValidationCheckResult(ctx, result); err != nil {
		// 逐项写入失败时保留内存结果：结论计算仍基于真实事实，最终由
		// Persisted 标记如实报告"结果未能完整保存"。
		s.logger.ErrorContext(
			ctx,
			"写入检查结果失败",
			slog.String("validation_run_id", run.ID.String()),
			slog.String("check_id", definition.ID),
			slog.String("error", err.Error()),
		)
	}
	return result
}

// skippedCheckResult 构造一条未执行检查的结果。
func skippedCheckResult(
	runID domain.ID,
	definition domain.ValidationCheckDefinition,
	reason string,
	at time.Time,
) domain.ValidationCheckResult {
	return domain.ValidationCheckResult{
		ID:              domain.NewID(),
		ValidationRunID: runID,
		CheckID:         definition.ID,
		CheckName:       definition.Name,
		Required:        definition.IsRequired(),
		Sequence:        definition.Sequence,
		Definition:      definition,
		Status:          domain.ValidationCheckStatusSkipped,
		ExitCode:        -1,
		FailureReason:   reason,
		StartedAt:       at,
		FinishedAt:      at,
		CreatedAt:       at,
	}
}

// stopStateOf 依据上下文判断验证是否被中断。
func stopStateOf(ctx context.Context) (domain.ValidationRunStatus, string, bool) {
	if ctx == nil {
		return domain.ValidationRunStatusCompleted, "", false
	}
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, ErrValidationEngineStopped):
		return domain.ValidationRunStatusFailed, "服务停止导致验证中断", true
	case errors.Is(cause, context.DeadlineExceeded):
		return domain.ValidationRunStatusTimedOut, "验证整体超时", true
	case errors.Is(cause, context.Canceled):
		return domain.ValidationRunStatusCancelled, "调用方取消验证", true
	default:
		return domain.ValidationRunStatusCompleted, "", false
	}
}

// finalizeValidation 写入结论、检查统计与执行尝试判定。
//
// 三条写入各自独立执行并使用未取消的上下文：取消或超时恰恰是最需要写入终态的
// 场景。任一失败都记录在 PersistenceErrors 中，且不会被报告成成功。
func (s *ValidationService) finalizeValidation(
	ctx context.Context,
	run domain.ValidationRun,
	request runValidationRequest,
	execution checkExecution,
	decision domain.ValidationDecision,
) (ValidationExecuteResult, error) {
	writeCtx := context.WithoutCancel(ctx)
	finishedAt := s.now()
	status := execution.terminalStatus()
	passed, failed, skipped := domain.CountCheckResults(execution.results)
	summary := summarizeValidationRun(decision, len(execution.results))
	failureSummary := strings.TrimSpace(decision.FailureSummary)
	if decision.Verdict != domain.VerificationVerdictPassed && failureSummary == "" {
		failureSummary = "验证未通过"
	}
	result := ValidationExecuteResult{
		ValidationRunID: run.ID,
		TaskID:          request.task.ID,
		StepID:          request.step.ID,
		AttemptID:       request.attempt.ID,
		Status:          status,
		Verdict:         decision.Verdict,
		Passed:          decision.Passed(),
		Summary:         summary,
		FailureSummary:  failureSummary,
		Checks:          execution.results,
		PassedChecks:    passed,
		FailedChecks:    failed,
		SkippedChecks:   skipped,
		ExecutionEnv:    request.executor.Name(),
		Workspace:       request.task.Config.EffectiveWorkDir(),
		ContainerID:     request.container.ID.String(),
		StartedAt:       run.StartedAt,
		FinishedAt:      finishedAt,
	}
	var persistenceErrors []string
	finishErr := s.store.FinishValidationRun(writeCtx, storage.ValidationRunFinishRequest{
		RunID:          run.ID,
		Status:         status,
		Verdict:        decision.Verdict,
		Summary:        summary,
		FailureSummary: failureSummary,
		ErrorDetail:    execution.stopReason,
		PassedChecks:   passed,
		FailedChecks:   failed,
		SkippedChecks:  skipped,
		Duration:       finishedAt.Sub(run.StartedAt),
		At:             finishedAt,
	})
	if finishErr != nil {
		persistenceErrors = append(persistenceErrors, finishErr.Error())
		s.logFinalizeFailure(writeCtx, "写入验证结论失败", run, finishErr)
	}
	if err := s.recordAttemptVerdict(writeCtx, request, run, decision, summary); err != nil {
		persistenceErrors = append(persistenceErrors, err.Error())
		s.logFinalizeFailure(writeCtx, "写入执行尝试判定失败", run, err)
	}
	result.PersistenceErrors = persistenceErrors
	result.Persisted = len(persistenceErrors) == 0
	s.appendFinalizeEvent(writeCtx, request, run, result)
	s.logger.InfoContext(
		writeCtx,
		"验证已结束",
		slog.String("validation_run_id", run.ID.String()),
		slog.String("attempt_id", request.attempt.ID.String()),
		slog.String("status", string(status)),
		slog.String("verdict", string(decision.Verdict)),
		slog.Int("passed_checks", passed),
		slog.Int("failed_checks", failed),
		slog.Int("skipped_checks", skipped),
		slog.Bool("persisted", result.Persisted),
	)
	if !result.Persisted {
		// 结果未能可靠保存时不得返回"成功"：调用方必须看到写入失败这一事实。
		return result, fmt.Errorf(
			"%w: %s",
			ErrValidationNotPersisted,
			strings.Join(persistenceErrors, "；"),
		)
	}
	return result, nil
}

// recordAttemptVerdict 把确定性判定写回执行尝试。
//
// 判定来自 domain.DecideValidation，不是 Agent 的自然语言结论。写入使用状态前置
// 条件：尝试状态已变化时保留数据库真实值，不覆盖更新的状态。
func (s *ValidationService) recordAttemptVerdict(
	ctx context.Context,
	request runValidationRequest,
	run domain.ValidationRun,
	decision domain.ValidationDecision,
	summary string,
) error {
	attempt := request.attempt
	if err := attempt.SetVerification(decision.Verdict, summary, &run.ID); err != nil {
		return fmt.Errorf("设置验证判定失败: %w", err)
	}
	if err := s.store.UpdateAttemptIfStatus(
		ctx,
		attempt,
		[]domain.AttemptStatus{attempt.Status},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 迟到结果不覆盖已确认的事实：冲突时读取数据库真实值并如实报告。
			current, readErr := s.store.GetAttempt(ctx, attempt.ID)
			if readErr != nil {
				return fmt.Errorf("读取执行尝试失败: %w", readErr)
			}
			if current.VerificationID != nil && *current.VerificationID == run.ID {
				return nil
			}
			return fmt.Errorf(
				"执行尝试 %s 的状态已变更，验证判定未写入",
				attempt.ID,
			)
		}
		return fmt.Errorf("写入验证判定失败: %w", err)
	}
	return nil
}

// appendFinalizeEvent 追加验证结论事件。
func (s *ValidationService) appendFinalizeEvent(
	ctx context.Context,
	request runValidationRequest,
	run domain.ValidationRun,
	result ValidationExecuteResult,
) {
	eventType := domain.EventTypeVerificationFailed
	if result.Passed {
		eventType = domain.EventTypeVerificationPassed
	}
	payload := map[string]any{
		"validation_run_id": run.ID.String(),
		"status":            string(result.Status),
		"verdict":           string(result.Verdict),
		"passed_checks":     result.PassedChecks,
		"failed_checks":     result.FailedChecks,
		"skipped_checks":    result.SkippedChecks,
		"persisted":         result.Persisted,
	}
	if result.FailureSummary != "" {
		payload["failure_summary"] = result.FailureSummary
	}
	if _, err := s.appendEvent(ctx, request.task, &request.step.ID, &request.attempt.ID, nil,
		eventType, payload,
	); err != nil {
		s.logger.WarnContext(
			ctx,
			"记录验证结论事件失败",
			slog.String("validation_run_id", run.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// logFinalizeFailure 记录终态写入失败。
func (s *ValidationService) logFinalizeFailure(
	ctx context.Context,
	message string,
	run domain.ValidationRun,
	err error,
) {
	s.logger.ErrorContext(
		ctx,
		message,
		slog.String("validation_run_id", run.ID.String()),
		slog.String("error", err.Error()),
	)
}

// appendEvent 追加任务事件。
func (s *ValidationService) appendEvent(
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
		s.now(),
	)
	if err != nil {
		return domain.Event{}, fmt.Errorf("构造任务事件失败: %w", err)
	}
	stored, err := s.store.AppendEvent(ctx, event)
	if err != nil {
		return domain.Event{}, fmt.Errorf("写入任务事件失败: %w", err)
	}
	return stored, nil
}

// summarizeValidationRun 生成验证结论的单行摘要。
func summarizeValidationRun(decision domain.ValidationDecision, total int) string {
	switch decision.Verdict {
	case domain.VerificationVerdictPassed:
		return fmt.Sprintf("验证通过（%d 项检查）", total)
	case domain.VerificationVerdictInconclusive:
		return "验证结果不确定: " + decision.FailureSummary
	default:
		return "验证失败: " + decision.FailureSummary
	}
}
