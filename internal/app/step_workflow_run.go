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

// 闭环协调器的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。开发与验证
// 各自的错误类别保持原样向上传递，因此调用方仍能区分"状态冲突"与"前置条件缺失"。
var (
	// ErrStepWorkflowInvalidConfig 表示步骤的验证配置无法支撑验证判定。
	//
	// 例如没有任何启用的必需检查。它必须在开发之前暴露：否则会跑完一次注定无法
	// 验证的开发，并把配置问题伪装成"验证未通过"。
	ErrStepWorkflowInvalidConfig = errors.New("app: 步骤验证配置无效")
)

// ExecuteAndValidate 执行一个步骤的开发阶段，并在开发成功后立即执行验证。
//
// 语义固定为：
//   - 开发与验证都使用任务对应的同一个容器工作区，中间不重新 Clone、不清理改动；
//   - 每次调用创建新的 Attempt 与独立 Session，不复用旧记录；
//   - 只有开发阶段正常结束、执行记录已写入、验证配置有效且上下文未被取消时才启动验证；
//   - 步骤的最终状态只由真实验证结果决定：开发成功但未验证不会进入 completed；
//   - 验证失败后不自动重试、不自动修复代码、不自动删除容器，等待用户显式决定。
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
	feedback := s.latestFeedback(ctx, step)
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

	result := ExecuteAndValidateResult{TaskID: task.ID, StepID: step.ID, StartedAt: s.now()}
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
		return s.finalizeFailedDeveloper(ctx, result)
	}
	if !developer.Finalized {
		// 执行记录未完整写入时无法可靠地把验证关联到本次尝试。
		result.ValidationSkipReason = "开发阶段的执行记录未能完整写入，不启动验证"
		result.Verification = domain.StepVerificationUnverified
		result.FinishedAt = s.now()
		return s.closeout(ctx, result, domain.StepCloseoutInput{
			Outcome:      developer.Outcome,
			Verification: domain.StepVerificationUnverified,
		})
	}
	return s.validateAndCloseout(ctx, runCtx, result, request.ValidationTimeout)
}

// applyDeveloperResult 把开发阶段结果写入闭环结果。
func applyDeveloperResult(
	result ExecuteAndValidateResult,
	developer StepExecutionResult,
) ExecuteAndValidateResult {
	result.AttemptID = developer.AttemptID
	result.SessionID = developer.SessionID
	result.AttemptNumber = developer.AttemptNumber
	result.Outcome = developer.Outcome
	result.DeveloperSummary = developer.Summary
	result.DeveloperFailure = developer.Failure
	result.ExitCode = developer.ExitCode
	result.DeveloperFinalized = developer.Finalized
	result.DeveloperFinalizationErrors = developer.FinalizationErrors
	result.StepStatus = developer.StepStatus
	result.TaskStatus = developer.TaskStatus
	return result
}

// finalizeFailedDeveloper 处理"开发阶段未成功"的收口。
//
// 步骤与任务的终态已经由单步骤执行协调器写入，这里不重复写入，只把真实结果原样
// 汇报：失败、超时、取消与中断都不能被改写为通过。
func (s *StepWorkflowService) finalizeFailedDeveloper(
	ctx context.Context,
	result ExecuteAndValidateResult,
) (ExecuteAndValidateResult, error) {
	result.Verification = domain.StepVerificationUnverified
	result.Verified = false
	result.Finalized = result.DeveloperFinalized
	result.FinalizationErrors = append([]string(nil), result.DeveloperFinalizationErrors...)
	s.appendCloseoutEvent(ctx, result, domain.EventTypeAttemptFailed, map[string]any{
		"outcome":               string(result.Outcome),
		"validation_ran":        false,
		"validation_skip":       result.ValidationSkipReason,
		"step_status":           string(result.StepStatus),
		"developer_finalized":   result.DeveloperFinalized,
		"infrastructure_error":  false,
		"step_workflow_outcome": "developer_not_completed",
	})
	return result, nil
}

// validateAndCloseout 执行验证阶段并收口步骤状态。
func (s *StepWorkflowService) validateAndCloseout(
	ctx context.Context,
	runCtx context.Context,
	result ExecuteAndValidateResult,
	requestedTimeout time.Duration,
) (ExecuteAndValidateResult, error) {
	validationResult, validationErr := s.validation.Validate(runCtx, ValidationExecuteRequest{
		TaskID:    result.TaskID.String(),
		StepID:    result.StepID.String(),
		AttemptID: result.AttemptID.String(),
		Timeout:   s.validationTimeout(requestedTimeout),
	})
	ran := validationErr == nil ||
		errors.Is(validationErr, ErrValidationNotPersisted) ||
		!validationResult.ValidationRunID.IsEmpty()
	result = applyValidationResult(result, validationResult, ran)
	if !ran {
		// 验证根本没有启动：没有拿到任何证据，因此既不判通过，也不伪装成代码失败。
		result.ValidationSkipReason = validationSkipReasonOf(validationErr)
		result.InfrastructureError = true
		result.Verification = domain.StepVerificationInfrastructure
		result.FinishedAt = s.now()
		return s.closeout(ctx, result, domain.StepCloseoutInput{
			Outcome:      result.Outcome,
			Verification: domain.StepVerificationInfrastructure,
		})
	}
	verification := domain.DeriveStepVerificationStateFrom(
		result.ValidationStatus,
		result.ValidationVerdict,
		result.Checks,
	)
	result.Verification = verification
	result.InfrastructureError = domain.ValidationInfrastructureAnomaly(
		result.ValidationStatus,
		result.Checks,
	)
	if !result.ValidationPersisted {
		// 结论没能可靠保存时不能报告成功：已产生的检查证据仍然如实返回，但收口
		// 只能按"结果无法确认"处理，否则会把一次未落库的通过写成已验证完成。
		result.ValidationSkipReason = "验证结果未能可靠写入数据库"
		result.InfrastructureError = true
		verification = domain.StepVerificationInfrastructure
		result.Verification = verification
	}
	result.FeedbackPersisted = s.persistFeedback(ctx, result)
	result.FinishedAt = s.now()
	closed, err := s.closeout(ctx, result, domain.StepCloseoutInput{
		Outcome:         result.Outcome,
		VerificationRan: true,
		Verification:    verification,
	})
	if !closed.ValidationPersisted {
		return closed, fmt.Errorf(
			"%w: %s",
			ErrValidationNotPersisted,
			strings.Join(closed.ValidationPersistenceErrors, "；"),
		)
	}
	return closed, err
}

// applyValidationResult 把验证阶段结果写入闭环结果。
func applyValidationResult(
	result ExecuteAndValidateResult,
	validation ValidationExecuteResult,
	ran bool,
) ExecuteAndValidateResult {
	result.ValidationRan = ran
	result.ValidationRunID = validation.ValidationRunID
	result.ValidationStatus = validation.Status
	result.ValidationVerdict = validation.Verdict
	result.ValidationPassed = validation.Passed
	result.ValidationSummary = validation.Summary
	result.ValidationFailureSummary = validation.FailureSummary
	result.Checks = validation.Checks
	result.ValidationPersisted = validation.Persisted
	result.ValidationPersistenceErrors = validation.PersistenceErrors
	result.ReviewRan = validation.ReviewRan
	result.ReviewStatus = validation.ReviewStatus
	result.ReviewSummary = validation.ReviewSummary
	result.ValidatorSessionID = validation.ValidatorSessionID
	result.Assessment = validation.Assessment
	return result
}

// persistFeedback 保存验证失败的结构化反馈，供后续显式重试引用。
//
// 只有确实存在未通过的检查时才写入：通过的验证没有需要修复的问题。写入失败不回滚
// 验证结论，但必须如实报告，因为下一次执行会因此拿不到上一轮的问题清单。
func (s *StepWorkflowService) persistFeedback(
	ctx context.Context,
	result ExecuteAndValidateResult,
) bool {
	if result.ValidationVerdict == domain.VerificationVerdictPassed {
		return false
	}
	if result.ValidationRunID.IsEmpty() {
		return false
	}
	if len(failedChecksOf(result.Checks)) == 0 {
		return false
	}
	attempt, err := s.store.GetAttempt(context.WithoutCancel(ctx), result.AttemptID)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"读取执行尝试失败，跳过验证失败反馈",
			slog.String("attempt_id", result.AttemptID.String()),
			slog.String("error", errorSummary(err)),
		)
		return false
	}
	feedback := domain.NewValidationFailureFeedback(domain.ValidationFeedbackInput{
		TaskID:  result.TaskID,
		StepID:  result.StepID,
		Attempt: *attempt,
		Run: domain.ValidationRun{
			ID:      result.ValidationRunID,
			TaskID:  result.TaskID,
			StepID:  result.StepID,
			Status:  result.ValidationStatus,
			Verdict: result.ValidationVerdict,
			Summary: result.ValidationFailureSummary,
		},
		Results:   result.Checks,
		Developer: attempt.SessionIDs,
	}, s.now())
	if err := s.store.CreateValidationFailureFeedback(
		context.WithoutCancel(ctx),
		feedback,
	); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入验证失败反馈失败",
			slog.String("validation_run_id", result.ValidationRunID.String()),
			slog.String("error", errorSummary(err)),
		)
		return false
	}
	return true
}

// failedChecksOf 返回未通过的检查结果。
func failedChecksOf(checks []domain.ValidationCheckResult) []domain.ValidationCheckResult {
	failed := make([]domain.ValidationCheckResult, 0, len(checks))
	for _, check := range checks {
		if !check.Status.Passed() {
			failed = append(failed, check)
		}
	}
	return failed
}

// closeout 依据真实验证结果收口步骤与任务状态。
//
// 步骤状态只在它仍处于 verifying 时被改写：开发阶段已经收敛为 failed / cancelled /
// recovery_required 的步骤保持原状态，避免用"验证通过"覆盖真实的开发失败。
func (s *StepWorkflowService) closeout(
	ctx context.Context,
	result ExecuteAndValidateResult,
	input domain.StepCloseoutInput,
) (ExecuteAndValidateResult, error) {
	decision := domain.DecideStepCloseout(input)
	result.Verified = decision.Verified()
	writeCtx := context.WithoutCancel(ctx)
	var failures []string
	step, stepErr := s.applyStepCloseout(writeCtx, result, decision)
	if stepErr != nil {
		failures = append(failures, stepErr.Error())
		s.logger.ErrorContext(
			writeCtx,
			"写入步骤收口状态失败",
			slog.String("task_id", result.TaskID.String()),
			slog.String("step_id", result.StepID.String()),
			slog.String("error", stepErr.Error()),
		)
	} else {
		result.StepStatus = step.Status
	}
	// Verified 必须以数据库中的真实步骤状态为准：步骤已被其他操作推进时不覆盖，
	// 也不能仅凭决策结果声称"已验证完成"。
	result.Verified = decision.Verified() && result.StepStatus == domain.StepStatusCompleted
	task, taskErr := s.applyTaskCloseout(writeCtx, result, decision)
	if taskErr != nil {
		failures = append(failures, taskErr.Error())
		s.logger.ErrorContext(
			writeCtx,
			"写入任务状态失败",
			slog.String("task_id", result.TaskID.String()),
			slog.String("error", taskErr.Error()),
		)
	} else {
		result.TaskStatus = task.Status
	}
	result.FinalizationErrors = append(result.FinalizationErrors, failures...)
	// Finalized 只表达"本次闭环需要的写入是否全部成功"：没有发起验证时不需要验证
	// 写入成功，但一旦发起就必须可靠落库。
	result.Finalized = len(result.FinalizationErrors) == 0 &&
		result.DeveloperFinalized &&
		(!result.ValidationRan || result.ValidationPersisted)
	s.appendCloseoutEvent(writeCtx, result, closeoutEventType(result), map[string]any{
		"outcome":               string(result.Outcome),
		"validation_ran":        result.ValidationRan,
		"validation_run_id":     result.ValidationRunID.String(),
		"validation_status":     string(result.ValidationStatus),
		"verification_verdict":  string(result.ValidationVerdict),
		"verification_state":    string(result.Verification),
		"infrastructure_error":  result.InfrastructureError,
		"step_status":           string(result.StepStatus),
		"task_status":           string(result.TaskStatus),
		"verified":              result.Verified,
		"finalized":             result.Finalized,
		"feedback_persisted":    result.FeedbackPersisted,
		"review_ran":            result.ReviewRan,
		"review_status":         string(result.ReviewStatus),
		"step_workflow_outcome": closeoutOutcomeName(result),
	})
	s.logger.InfoContext(
		writeCtx,
		"步骤开发—验证闭环已结束",
		slog.String("task_id", result.TaskID.String()),
		slog.String("step_id", result.StepID.String()),
		slog.String("attempt_id", result.AttemptID.String()),
		slog.String("outcome", string(result.Outcome)),
		slog.Bool("validation_ran", result.ValidationRan),
		slog.String("verification", string(result.Verification)),
		slog.String("step_status", string(result.StepStatus)),
		slog.Bool("verified", result.Verified),
		slog.Bool("finalized", result.Finalized),
		slog.String("review_status", string(result.ReviewStatus)),
	)
	return result, nil
}

// applyStepCloseout 写入步骤的收口状态。
//
// 写入使用状态前置条件：只有步骤仍处于 verifying 时才改写，迟到的结果不会覆盖已经
// 确认的终态。步骤已不在 verifying 时保留数据库真实值。
func (s *StepWorkflowService) applyStepCloseout(
	ctx context.Context,
	result ExecuteAndValidateResult,
	decision domain.StepCloseoutDecision,
) (domain.Step, error) {
	step, err := s.store.GetStep(ctx, result.StepID)
	if err != nil {
		return domain.Step{}, fmt.Errorf("读取步骤失败: %w", err)
	}
	if step.Status != domain.StepStatusVerifying {
		// 现场已被其他操作推进：不覆盖，如实返回数据库值。
		return *step, nil
	}
	at := s.now()
	step.Result = decision.Summary
	step.FailureSummary = ""
	step.FailureStage = ""
	if decision.Status != domain.StepStatusCompleted {
		step.FailureStage = "verify"
		step.FailureSummary = closeoutFailureSummary(result, decision)
	}
	if err := step.Transition(decision.Status, at); err != nil {
		return *step, fmt.Errorf("标记步骤收口状态失败: %w", err)
	}
	// 只有仍处于执行占用中的状态才保留当前尝试：一旦收口为可再次执行的状态
	// （failed / recovery_required）或终态，就必须释放占用，否则用户显式重新执行时
	// 会被"步骤已被执行尝试占用"挡住。
	if decision.Status.CanStartExecution() || decision.Status.IsTerminal() {
		if err := step.SetCurrentAttempt(nil); err != nil {
			return *step, err
		}
	}
	if err := s.store.UpdateStepIfStatus(
		ctx,
		*step,
		[]domain.StepStatus{domain.StepStatusVerifying},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := s.store.GetStep(ctx, step.ID)
			if readErr != nil {
				return *step, fmt.Errorf("读取步骤终态失败: %w", readErr)
			}
			return *current, nil
		}
		return *step, fmt.Errorf("写入步骤收口状态失败: %w", err)
	}
	return *step, nil
}

// closeoutFailureSummary 生成步骤失败摘要。
func closeoutFailureSummary(
	result ExecuteAndValidateResult,
	decision domain.StepCloseoutDecision,
) string {
	detail := strings.TrimSpace(result.ValidationFailureSummary)
	if detail == "" {
		detail = strings.TrimSpace(result.ValidationSkipReason)
	}
	if detail == "" {
		detail = decision.Summary
	}
	return detail
}

// applyTaskCloseout 按现有任务状态机推进任务状态。
//
// 单个步骤通过验证不代表任务完成：任务保持 developing，最终状态由后续代码保存与
// 任务级编排决定。只有"需要人工核验"的结论才把任务推进到 recovery_required。
func (s *StepWorkflowService) applyTaskCloseout(
	ctx context.Context,
	result ExecuteAndValidateResult,
	decision domain.StepCloseoutDecision,
) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, result.TaskID)
	if err != nil {
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if decision.Status != domain.StepStatusRecoveryRequired {
		return *task, nil
	}
	if !task.Status.AllowsStepExecution() {
		// 任务已经被取消或结束：不把用户显式结束的任务重新打开。
		return *task, nil
	}
	if task.Status == domain.TaskStatusRecoveryRequired {
		return *task, nil
	}
	expected := task.Status
	if err := task.Transition(domain.TaskStatusRecoveryRequired, s.now()); err != nil {
		return *task, fmt.Errorf("标记任务需要恢复核验失败: %w", err)
	}
	task.FailureSummary = closeoutFailureSummary(result, decision)
	if err := s.store.UpdateTaskIfStatus(ctx, *task, []domain.TaskStatus{expected}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			current, readErr := s.store.GetTask(ctx, task.ID)
			if readErr != nil {
				return *task, fmt.Errorf("读取任务终态失败: %w", readErr)
			}
			return *current, nil
		}
		return *task, fmt.Errorf("写入任务状态失败: %w", err)
	}
	return *task, nil
}

// closeoutEventType 返回收口事件类型。
func closeoutEventType(result ExecuteAndValidateResult) domain.EventType {
	switch {
	case result.Verified:
		return domain.EventTypeStepCompleted
	case result.InfrastructureError:
		return domain.EventTypeTaskRecoveryRequired
	case result.ValidationRan:
		return domain.EventTypeVerificationFailed
	default:
		return domain.EventTypeVerificationSkipped
	}
}

// closeoutOutcomeName 返回收口结论的稳定名称，便于事件审计。
func closeoutOutcomeName(result ExecuteAndValidateResult) string {
	switch {
	case result.Verified:
		return "verified"
	case result.InfrastructureError:
		return "infrastructure_error"
	case result.ValidationRan:
		return "verification_failed"
	case result.Outcome != domain.StepExecutionCompleted:
		return "developer_not_completed"
	default:
		return "verification_not_run"
	}
}

// appendCloseoutEvent 追加闭环收口事件。
//
// 事件写入失败只记录日志，不回滚已写入的收口状态，也不删除执行记录。
func (s *StepWorkflowService) appendCloseoutEvent(
	ctx context.Context,
	result ExecuteAndValidateResult,
	eventType domain.EventType,
	payload map[string]any,
) {
	var sessionPtr *domain.ID
	if !result.SessionID.IsEmpty() {
		sessionID := result.SessionID
		sessionPtr = &sessionID
	}
	var attemptPtr *domain.ID
	if !result.AttemptID.IsEmpty() {
		attemptID := result.AttemptID
		attemptPtr = &attemptID
	}
	event, err := domain.NewEvent(
		result.TaskID,
		&result.StepID,
		attemptPtr,
		sessionPtr,
		eventType,
		marshalStepExecutionPayload(payload),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造闭环事件失败",
			slog.String("task_id", result.TaskID.String()),
			slog.String("step_id", result.StepID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入闭环事件失败",
			slog.String("task_id", result.TaskID.String()),
			slog.String("step_id", result.StepID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// requireStepVerificationSpec 校验步骤的验证配置是否可用于判定。
//
// 规则与确定性验证引擎一致：至少存在一项启用的必需检查。没有必需检查时拒绝进入
// 闭环，而不是跑完开发后得到一条注定失败的验证记录。
func requireStepVerificationSpec(step domain.Step) error {
	spec := ValidationSpecFromStep(step)
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrStepWorkflowInvalidConfig, err)
	}
	if !spec.HasRequiredCheck() {
		return fmt.Errorf(
			"%w: 步骤 %s 的验证配置缺少启用的必需检查，无法判定验证是否通过",
			ErrStepWorkflowInvalidConfig,
			step.ID,
		)
	}
	return nil
}

// validationSkipReasonOf 生成验证未能启动的原因。
func validationSkipReasonOf(err error) string {
	if err == nil {
		return "验证未能启动"
	}
	return "验证未能启动: " + errorSummary(err)
}
