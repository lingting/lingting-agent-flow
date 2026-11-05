package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// workflowExecution 是一次在途执行的可选参数。
type workflowExecution struct {
	prompt            string
	timeout           time.Duration
	validationTimeout time.Duration
	// resumed 表示本次是恢复而不是首次启动，仅用于日志与事件区分。
	resumed bool
	// grant 是本次执行的任务执行权凭据。
	//
	// 它是本次执行持有的任务执行权凭据：执行权覆盖整个推进过程，结束时释放。
	grant TaskExecutionGrant
}

// cancelRegistry 记录在途工作流的取消函数，使取消请求能传播到实际执行。
//
// 它以任务标识为键：同一任务同一时刻最多一条在途执行，因此取消请求不会误伤其他
// 任务的执行。
type cancelRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelCauseFunc
}

// register 登记一次在途执行，返回注销函数。
func (r *cancelRegistry) register(taskID string, cancel context.CancelCauseFunc) func() {
	r.mu.Lock()
	if r.cancels == nil {
		r.cancels = make(map[string]context.CancelCauseFunc)
	}
	r.cancels[taskID] = cancel
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		// 只删除自己登记的条目：后来的执行可能已经接管同一任务。
		if current, ok := r.cancels[taskID]; ok && sameCancel(current, cancel) {
			delete(r.cancels, taskID)
		}
		r.mu.Unlock()
	}
}

// cancel 取消指定任务的在途执行，返回是否找到在途执行。
func (r *cancelRegistry) cancel(taskID string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[taskID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	cancel(errWorkflowCancelRequested)
	return true
}

// sameCancel 判断两个取消函数是否指向同一次执行。
//
// 函数值不可比较，因此用指向同一闭包的指针地址判断；地址不同的闭包视为不同执行。
func sameCancel(a, b context.CancelCauseFunc) bool {
	return fmt.Sprintf("%p", a) == fmt.Sprintf("%p", b)
}

// cancelInFlight 向指定任务的在途工作流传播取消信号。
func (s *TaskWorkflowService) cancelInFlight(taskID domain.ID) {
	if s.cancels.cancel(taskID.String()) {
		s.logger.DebugContext(
			context.Background(),
			"已向在途工作流传播取消信号",
			slog.String("task_id", taskID.String()),
		)
	}
}

// launch 在受生命周期管理的后台 goroutine 中推进工作流。
//
// 执行权由调用方在创建执行记录之前取得，由后台 goroutine 在推进结束时释放：这样
// "启动"与"推进"之间不存在其他入口插入的窗口。
func (s *TaskWorkflowService) launch(
	run domain.WorkflowRun,
	exec workflowExecution,
) {
	go s.drive(run, exec)
}

// drive 执行工作流主循环。
//
// 语义固定为：
//   - 只按已持久化的计划顺序推进，不生成、不补充计划；
//   - 每一步都先持久化阶段与游标，再执行有副作用的调用；
//   - 只有当前步骤验证通过才推进下一步；
//   - 任何失败、取消或不确定状态都立即停止，不自动重试。
func (s *TaskWorkflowService) drive(
	run domain.WorkflowRun,
	exec workflowExecution,
) {
	// 执行权覆盖整个推进过程，结束时（无论成功、阻塞或恢复态）在这里释放。
	defer s.coordination.Release(exec.grant)
	ctx, finish, err := s.beginRun(context.Background())
	if err != nil {
		// 服务已停止：记录保持原状，由启动恢复流程按数据库事实处理。
		s.logger.WarnContext(
			context.Background(),
			"工作流未能开始推进",
			slog.String("run_id", run.ID.String()),
			slog.String("task_id", run.TaskID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	defer finish()

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	unregister := s.cancels.register(run.TaskID.String(), cancelRun)
	defer unregister()

	state := s.advance(runCtx, run, exec)
	s.logger.InfoContext(
		context.WithoutCancel(ctx),
		"任务级工作流执行已结束",
		slog.String("run_id", state.ID.String()),
		slog.String("task_id", state.TaskID.String()),
		slog.String("status", string(state.Status)),
		slog.String("stage", string(state.Stage)),
		slog.Int("completed_steps", state.CompletedSteps),
		slog.Int("total_steps", state.TotalSteps),
	)
}

// advance 推进工作流直到出现终止条件。
//
// 返回值是最后一次成功写入的执行记录，用于日志与诊断；写入失败时保留内存状态，
// 数据库中的记录仍然是可查询的真实值。
func (s *TaskWorkflowService) advance(
	ctx context.Context,
	run domain.WorkflowRun,
	exec workflowExecution,
) domain.WorkflowRun {
	writeCtx := context.WithoutCancel(ctx)
	for {
		if run.CancelRequested || ctx.Err() != nil {
			return s.stopForInterruption(writeCtx, run, ctx)
		}
		steps, err := s.loadSteps(writeCtx, run.TaskID)
		if err != nil {
			return s.failRun(writeCtx, run, "load_steps", err)
		}
		step, ok, nextErr := domain.NextStepToExecute(steps)
		if nextErr != nil {
			// 现场不确定或已有执行占用：停止并如实报告，不跳过、不接管。
			return s.stopRun(writeCtx, run, domain.WorkflowRunBlocked, "step_not_executable", nextErr)
		}
		if !ok {
			return s.finalize(writeCtx, run, steps, exec.grant)
		}
		next, stop := s.runStep(ctx, writeCtx, run, step, exec)
		run = next
		if stop {
			return run
		}
	}
}

// runStep 执行一个计划步骤并依据真实结果推进游标。
//
// 步骤类型决定执行路径：规划阶段只做真实的规划执行，开发步骤走开发—验证闭环。
// 两条路径的成功条件不同，不能互相替代。
//
// 返回值 stop 为 true 表示工作流已经收敛或停止，调用方不再继续推进。
func (s *TaskWorkflowService) runStep(
	ctx context.Context,
	writeCtx context.Context,
	run domain.WorkflowRun,
	step domain.Step,
	exec workflowExecution,
) (domain.WorkflowRun, bool) {
	stepID := step.ID
	if err := run.SetStage(domain.WorkflowStageStepExecution, &stepID, nil, s.now()); err != nil {
		return s.failRun(writeCtx, run, "stage", err), true
	}
	persisted, err := s.persistRun(writeCtx, run)
	if err != nil {
		return s.failRun(writeCtx, run, "stage", err), true
	}
	run = persisted
	s.appendEvent(writeCtx, run.TaskID, &stepID, nil, domain.EventTypeStepStarted, map[string]any{
		"run_id":   run.ID.String(),
		"step_id":  stepID.String(),
		"sequence": step.Sequence,
		"phase":    "step_started",
		"title":    step.Title,
	})
	if step.Type == domain.StepTypePlan {
		return s.runPlanStep(ctx, writeCtx, run, step, exec)
	}
	result, runErr := s.steps.ExecuteAndValidate(ctx, ExecuteAndValidateRequest{
		TaskID:            run.TaskID.String(),
		StepID:            stepID.String(),
		Prompt:            exec.prompt,
		Timeout:           exec.timeout,
		ValidationTimeout: exec.validationTimeout,
		// 任务执行权随工作流传入：步骤闭环因此与编排器共用同一套所有权，
		// 开发阶段的任务终态写入附带 owner + epoch 栅栏。
		Grant: &exec.grant,
	})
	if runErr != nil {
		// 协调器返回错误意味着本次没有产生真实执行（状态冲突、前置条件缺失、
		// 目标不存在等）：错误必须如实记录，不能伪装成"执行后正常停止"。
		if ctx.Err() != nil || run.CancelRequested || errors.Is(runErr, ErrWorkflowStopped) {
			return s.stopForInterruption(writeCtx, run, ctx), true
		}
		return s.stopRun(writeCtx, run, domain.WorkflowRunBlocked, "step_not_executed", runErr), true
	}
	if result.Verified && result.StepStatus == domain.StepStatusCompleted {
		return s.recordStepPassed(writeCtx, run, step, result.AttemptID, nil)
	}
	return s.stopForStepResult(writeCtx, run, step, result, ctx), true
}

// runPlanStep 执行规划阶段步骤，并依据真实 Planner 执行结论推进游标。
//
// 规划阶段不做确定性代码验证：任务级必需命令依赖 Developer 的产出，只能在开发步骤
// 之后成立。因此这里的成功条件是"Planner 真实执行完成且执行记录已写入"，而不是
// "验证通过"——不会凭空产生 ValidationRun，也不会把执行成功说成验证通过。
func (s *TaskWorkflowService) runPlanStep(
	ctx context.Context,
	writeCtx context.Context,
	run domain.WorkflowRun,
	step domain.Step,
	exec workflowExecution,
) (domain.WorkflowRun, bool) {
	result, runErr := s.steps.ExecutePlanning(ctx, ExecutePlanningRequest{
		TaskID:  run.TaskID.String(),
		StepID:  step.ID.String(),
		Prompt:  exec.prompt,
		Timeout: exec.timeout,
		// 任务执行权随工作流传入：规划阶段的任务终态写入同样附带 owner + epoch 栅栏。
		Grant: &exec.grant,
	})
	if runErr != nil {
		// 协调器返回错误意味着本次没有产生真实执行：错误必须如实记录。
		if ctx.Err() != nil || run.CancelRequested || errors.Is(runErr, ErrWorkflowStopped) {
			return s.stopForInterruption(writeCtx, run, ctx), true
		}
		return s.stopRun(writeCtx, run, domain.WorkflowRunBlocked, "step_not_executed", runErr), true
	}
	if result.Outcome == domain.StepExecutionCompleted &&
		result.StepStatus == domain.StepStatusCompleted &&
		result.Finalized {
		return s.recordStepPassed(writeCtx, run, step, result.AttemptID, map[string]any{
			"phase": "plan",
		})
	}
	return s.stopForPlanResult(writeCtx, run, step, result, ctx), true
}

// recordStepPassed 记录一个步骤真实通过，并推进执行记录游标。
//
// 调用方必须先依据真实执行证据确认通过；本函数只负责写入这一事实并追加事件。
// extra 用于补充该阶段特有的追溯字段。
func (s *TaskWorkflowService) recordStepPassed(
	writeCtx context.Context,
	run domain.WorkflowRun,
	step domain.Step,
	attemptID domain.ID,
	extra map[string]any,
) (domain.WorkflowRun, bool) {
	stepID := step.ID
	if err := run.RecordStepCompleted(step, s.now()); err != nil {
		return s.failRun(writeCtx, run, "record_step", err), true
	}
	persisted, persistErr := s.persistRun(writeCtx, run)
	if persistErr != nil {
		// 数据库状态写入失败时即使步骤真实通过也不能推进后续步骤。
		return s.failRun(writeCtx, run, "record_step", persistErr), true
	}
	payload := map[string]any{
		"run_id":          persisted.ID.String(),
		"step_id":         stepID.String(),
		"sequence":        step.Sequence,
		"attempt_id":      attemptID.String(),
		"completed_steps": persisted.CompletedSteps,
		"total_steps":     persisted.TotalSteps,
	}
	for key, value := range extra {
		payload[key] = value
	}
	s.appendEvent(writeCtx, run.TaskID, &stepID, idPtr(attemptID),
		domain.EventTypeWorkflowStepPassed, payload)
	return persisted, false
}

// stopForPlanResult 依据规划阶段的真实执行结果收敛工作流状态。
//
// 只有真实执行完成才可能继续推进；其余结论都必须停止，并按结论区分可恢复停止与需要
// 人工核验的现场。规划阶段不自动重试，也不进入开发步骤的自动修复循环。
func (s *TaskWorkflowService) stopForPlanResult(
	writeCtx context.Context,
	run domain.WorkflowRun,
	step domain.Step,
	result ExecutePlanningResult,
	ctx context.Context,
) domain.WorkflowRun {
	stepID := step.ID
	payload := map[string]any{
		"run_id":     run.ID.String(),
		"step_id":    stepID.String(),
		"sequence":   step.Sequence,
		"phase":      "plan",
		"attempt_id": result.AttemptID.String(),
		"outcome":    string(result.Outcome),
		// 规划阶段没有验证记录：事件里不写 validation_run_id，避免被读成"验证未通过"。
		"step_status": string(result.StepStatus),
		"finalized":   result.Finalized,
	}
	switch {
	case ctx.Err() != nil || run.CancelRequested ||
		result.Outcome == domain.StepExecutionCancelled:
		if run.CancelRequested || result.Outcome == domain.StepExecutionCancelled ||
			errors.Is(context.Cause(ctx), errWorkflowCancelRequested) {
			s.appendEvent(writeCtx, run.TaskID, &stepID, idPtr(result.AttemptID),
				domain.EventTypeWorkflowCanceled, payload)
		}
		return s.stopForInterruption(writeCtx, run, ctx)
	case !result.Finalized:
		// 执行记录没有可靠写入：结果无法确认，既不判定失败也不判定通过。
		return s.stopWithEvent(writeCtx, run, domain.WorkflowRunRecoveryRequired,
			"execution_unconfirmed", planningStopSummary(result),
			domain.EventTypeWorkflowRecovery, payload)
	case result.Outcome == domain.StepExecutionInterrupted ||
		result.Outcome == domain.StepExecutionTimedOut:
		return s.stopWithEvent(writeCtx, run, domain.WorkflowRunRecoveryRequired,
			"plan_interrupted", planningStopSummary(result),
			domain.EventTypeWorkflowBlocked, payload)
	default:
		return s.stopWithEvent(writeCtx, run, domain.WorkflowRunBlocked,
			"plan_failed", planningStopSummary(result),
			domain.EventTypeWorkflowBlocked, payload)
	}
}

// stopForStepResult 依据步骤闭环的真实结果收敛工作流状态。
//
// 只有"验证通过"会继续推进；其余结论都必须停止，并按结论区分可恢复停止与需要人工
// 核验的现场。
func (s *TaskWorkflowService) stopForStepResult(
	writeCtx context.Context,
	run domain.WorkflowRun,
	step domain.Step,
	result ExecuteAndValidateResult,
	ctx context.Context,
) domain.WorkflowRun {
	stepID := step.ID
	payload := map[string]any{
		"run_id":           run.ID.String(),
		"step_id":          stepID.String(),
		"sequence":         step.Sequence,
		"attempt_id":       result.AttemptID.String(),
		"outcome":          string(result.Outcome),
		"validation_ran":   result.ValidationRan,
		"verification":     string(result.Verification),
		"step_status":      string(result.StepStatus),
		"infrastructure":   result.InfrastructureError,
		"finalized":        result.Finalized,
		"validation_error": result.ValidationFailureSummary,
		// 修复循环的追溯字段：轮次、上限、判定与原因。
		"round":          result.Round,
		"attempts":       result.Attempts,
		"repair_class":   string(result.Repair.Class),
		"repair_summary": result.Repair.Summary,
	}
	switch {
	case ctx.Err() != nil || run.CancelRequested ||
		result.Outcome == domain.StepExecutionCancelled:
		if run.CancelRequested || result.Outcome == domain.StepExecutionCancelled ||
			errors.Is(context.Cause(ctx), errWorkflowCancelRequested) {
			s.appendEvent(writeCtx, run.TaskID, &stepID, idPtr(result.AttemptID),
				domain.EventTypeWorkflowCanceled, payload)
		}
		return s.stopForInterruption(writeCtx, run, ctx)
	case result.Repair.Class != "":
		// 修复循环已经给出结构化停止类别：直接消费领域判定，不在这里重新推导。
		class, status, eventType := repairStopMapping(result.Repair.Class)
		return s.stopWithEvent(writeCtx, run, status, class,
			repairStopSummary(result), eventType, payload)
	case result.Outcome != domain.StepExecutionCompleted:
		// 开发阶段未成功：失败与中断都停止自动推进，不自动重试。
		class := "developer_failed"
		status := domain.WorkflowRunBlocked
		if result.Outcome == domain.StepExecutionInterrupted || result.Outcome == domain.StepExecutionTimedOut {
			class = "developer_interrupted"
			status = domain.WorkflowRunRecoveryRequired
		}
		return s.stopWithEvent(writeCtx, run, status, class,
			developerStopSummary(result), domain.EventTypeWorkflowBlocked, payload)
	case result.InfrastructureError || !result.ValidationPersisted || !result.Finalized:
		// 执行记录或验证证据没有可靠写入：结果无法确认，不能判定失败也不能判定通过。
		return s.stopWithEvent(writeCtx, run, domain.WorkflowRunRecoveryRequired,
			"verification_unconfirmed", unconfirmedStopSummary(result),
			domain.EventTypeWorkflowRecovery, payload)
	default:
		return s.stopWithEvent(writeCtx, run, domain.WorkflowRunBlocked,
			"verification_failed", verificationStopSummary(result),
			domain.EventTypeWorkflowBlocked, payload)
	}
}

// repairStopMapping 把修复循环的停止类别映射为工作流停止状态与错误分类。
//
// 规则只在这里表达一次：需要人工核验的类别进入 recovery_required，其余真实失败
// 进入可恢复的 blocked。取消由调用方先行处理，不会走到这里。
func repairStopMapping(
	class domain.RepairFailureClass,
) (string, domain.WorkflowRunStatus, domain.EventType) {
	switch class {
	case domain.RepairClassDeveloperFailed:
		return "developer_failed", domain.WorkflowRunBlocked, domain.EventTypeWorkflowBlocked
	case domain.RepairClassDeveloperTimedOut, domain.RepairClassDeveloperInterrupted:
		return "developer_interrupted", domain.WorkflowRunRecoveryRequired,
			domain.EventTypeWorkflowBlocked
	case domain.RepairClassVerificationNotRun,
		domain.RepairClassInfrastructure,
		domain.RepairClassNotPersisted,
		domain.RepairClassFeedbackUnavailable:
		return "verification_unconfirmed", domain.WorkflowRunRecoveryRequired,
			domain.EventTypeWorkflowRecovery
	case domain.RepairClassVerificationInconclusive:
		return "verification_inconclusive", domain.WorkflowRunRecoveryRequired,
			domain.EventTypeWorkflowRecovery
	case domain.RepairClassAttemptsExhausted:
		return "attempts_exhausted", domain.WorkflowRunBlocked, domain.EventTypeWorkflowBlocked
	case domain.RepairClassRepairDisabled:
		return "verification_failed", domain.WorkflowRunBlocked, domain.EventTypeWorkflowBlocked
	case domain.RepairClassUnsupportedStep:
		return "step_not_repairable", domain.WorkflowRunBlocked, domain.EventTypeWorkflowBlocked
	default:
		return "verification_failed", domain.WorkflowRunBlocked, domain.EventTypeWorkflowBlocked
	}
}

// repairStopSummary 生成修复循环停止的说明。
func repairStopSummary(result ExecuteAndValidateResult) string {
	detail := strings.TrimSpace(result.Repair.Summary)
	if detail == "" {
		detail = verificationStopSummary(result)
	}
	return "修复循环停止（" + string(result.Repair.Class) + "，共 " +
		itoaForPrompt(result.Attempts) + " 次开发尝试）: " + detail
}

// finalize 在全部开发步骤完成后执行任务收尾。
//
// 收尾结论只由 domain.DecideWorkflowCloseout 依据真实证据计算：步骤通过验证不等于
// 任务成功，代码保存与会话归档同样是必需条件。
func (s *TaskWorkflowService) finalize(
	writeCtx context.Context,
	run domain.WorkflowRun,
	steps []domain.Step,
	grant TaskExecutionGrant,
) domain.WorkflowRun {
	if err := run.SetStage(domain.WorkflowStageFinalize, nil, nil, s.now()); err != nil {
		return s.failRun(writeCtx, run, "finalize", err)
	}
	persisted, err := s.persistRun(writeCtx, run)
	if err != nil {
		return s.failRun(writeCtx, run, "finalize", err)
	}
	run = persisted

	task, err := s.loadTask(writeCtx, run.TaskID)
	if err != nil {
		return s.stopRun(writeCtx, run, domain.WorkflowRunRecoveryRequired, "task_unreadable", err)
	}
	stepsCompleted := allExecutableStepsCompleted(steps)
	codeSave, codeSaveErr := s.saveCode(writeCtx, task, &run)
	sessionPersisted := s.sessionsPersisted(writeCtx, task)

	decision := domain.DecideWorkflowCloseout(domain.WorkflowCloseoutInput{
		StepsCompleted:        stepsCompleted,
		CodeSaveRequested:     true,
		CodeSaveSucceeded:     codeSave.succeeded,
		CodeSaveIndeterminate: codeSave.indeterminate,
		CodeSaveUnconfirmed:   codeSave.unconfirmed,
		SessionPersisted:      sessionPersisted,
	})
	errorClass := decision.ErrorClass
	summary := decision.Summary
	switch {
	case codeSaveErr != nil && !codeSave.indeterminate && !codeSave.unconfirmed &&
		decision.Status == domain.WorkflowRunBlocked:
		summary = summary + "：" + errorSummary(codeSaveErr)
	case codeSave.unavailable:
		errorClass = "code_saver_unavailable"
		summary = "本服务未提供代码保存能力，任务不能判定为完整成功"
	}
	return s.applyCloseout(writeCtx, run, task, decision, errorClass, summary, grant)
}

// applyCloseout 写入收尾结论与任务状态。
func (s *TaskWorkflowService) applyCloseout(
	writeCtx context.Context,
	run domain.WorkflowRun,
	task domain.Task,
	decision domain.WorkflowCloseoutDecision,
	errorClass, summary string,
	grant TaskExecutionGrant,
) domain.WorkflowRun {
	payload := map[string]any{
		"run_id":          run.ID.String(),
		"status":          string(decision.Status),
		"error_class":     errorClass,
		"summary":         summary,
		"completed_steps": run.CompletedSteps,
		"total_steps":     run.TotalSteps,
	}
	if !decision.Completed() {
		eventType := domain.EventTypeWorkflowBlocked
		if decision.Status == domain.WorkflowRunRecoveryRequired {
			eventType = domain.EventTypeWorkflowRecovery
		}
		return s.stopWithEvent(writeCtx, run, decision.Status, errorClass, summary, eventType, payload)
	}
	// 收尾写入顺序固定为"执行记录 → 任务"，执行记录的条件写入是栅栏：
	//   - 写入失败说明本次执行已经失去执行权（版本冲突、执行权已释放）或数据库不可用，
	//     此时绝不能继续写任务成功终态，否则会在没有执行权的情况下伪造任务成功；
	//   - 写入成功后才允许推进任务，此时任务写入失败只会留下
	//     "执行记录 completed + 任务 recovery_required"，即"未完成"，
	//     由启动恢复的收尾停滞入口补齐，不会伪造成功。
	if err := run.Complete(summary, s.now()); err != nil {
		return s.stopRun(writeCtx, run, domain.WorkflowRunRecoveryRequired, "closeout_failed", err)
	}
	persisted, persistErr := s.persistRun(writeCtx, run)
	if persistErr != nil {
		// 记录没能写成 completed：内存副本已经进入终态，无法再经状态机收敛为
		// recovery_required。此时必须回到数据库事实并如实报告"收尾未落盘"，
		// 不能对外宣布完成，也不能把 running 残留当成已收尾留在库里。
		return s.stopUnpersistedCloseout(writeCtx, run, persistErr)
	}
	updated, err := s.completeTask(writeCtx, task, &grant)
	if err != nil {
		// 执行记录已经确认完成，任务状态写入失败：如实收敛为需要人工核验。
		// 该路径不再改写执行记录（它已是终态），由启动恢复按持久化证据补齐。
		s.appendEvent(writeCtx, run.TaskID, nil, nil, domain.EventTypeWorkflowRecovery, map[string]any{
			"run_id":      persisted.ID.String(),
			"status":      string(domain.WorkflowRunCompleted),
			"error_class": domain.CloseoutClassTaskCompletionFailed,
			"summary":     "执行记录已确认完成，任务状态写入失败: " + errorSummary(err),
		})
		s.markTaskRecoveryRequired(writeCtx, task, domain.CloseoutClassTaskCompletionFailed,
			"执行记录已确认完成，但任务状态写入失败: "+errorSummary(err))
		return persisted
	}
	payload["task_status"] = string(updated.Status)
	s.appendEvent(writeCtx, run.TaskID, nil, nil, domain.EventTypeWorkflowCompleted, payload)
	return persisted
}

// markTaskRecoveryRequired 把任务收敛为需要人工核验。
//
// 它只用于"执行记录已经确认完成、但任务状态写入失败"的场景：此时不能再把任务
// 声明为成功，也不能让任务停留在 saving / verifying 这种既不能启动也不能恢复的
// 中间状态。写入失败只记录日志，由启动恢复的收尾停滞入口再次收敛。
func (s *TaskWorkflowService) markTaskRecoveryRequired(
	ctx context.Context,
	task domain.Task,
	errorClass, summary string,
) {
	current, err := s.store.GetTask(context.WithoutCancel(ctx), task.ID)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"任务收尾失败后无法重新读取任务，保留原状态等待启动恢复",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	recovered := *current
	if recovered.Status.IsTerminal() {
		// 其他执行者已经把任务收敛到终态：不覆盖。
		return
	}
	if recovered.Status != domain.TaskStatusRecoveryRequired {
		previous := recovered.Status
		if transitionErr := recovered.Transition(domain.TaskStatusRecoveryRequired, s.now()); transitionErr != nil {
			s.logger.WarnContext(
				ctx,
				"任务收尾失败后无法收敛为需要人工核验",
				slog.String("task_id", task.ID.String()),
				slog.String("status", string(previous)),
				slog.String("error", errorSummary(transitionErr)),
			)
			return
		}
		if updateErr := s.store.UpdateTaskIfStatus(
			context.WithoutCancel(ctx),
			recovered,
			[]domain.TaskStatus{previous},
		); updateErr != nil {
			s.logger.WarnContext(
				ctx,
				"任务收尾失败后写入需要人工核验状态失败",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(updateErr)),
			)
			return
		}
	}
	recovered.FailureSummary = summary
	s.appendEvent(context.WithoutCancel(ctx), task.ID, nil, nil,
		domain.EventTypeTaskRecoveryRequired, map[string]any{
			"stage":       "closeout",
			"error_class": errorClass,
			"summary":     summary,
		})
}

// completeTask 把任务推进到成功终态。
//
// 任务状态迁移仍由 domain 状态机校验：编排器只按既有规则依次推进到 verifying 与
// saving，再写入 succeeded 与 completed，不直接赋值状态字段。
func (s *TaskWorkflowService) completeTask(
	ctx context.Context,
	task domain.Task,
	grant *TaskExecutionGrant,
) (domain.Task, error) {
	now := s.now()
	for _, next := range taskCompletionPath(task.Status) {
		previous := task.Status
		if err := task.Transition(next, now); err != nil {
			return task, fmt.Errorf("推进任务状态到 %s 失败: %w", next, err)
		}
		// 成功终态的每一步都必须附带任务执行权条件：旧代次的迟到写入不得把任务
		// 写成 completed，也不得清除新领取者写入的取消意图。
		if err := s.writeTask(ctx, task, []domain.TaskStatus{previous}, grant); err != nil {
			return task, fmt.Errorf("写入任务状态失败: %w", err)
		}
	}
	if err := task.Finish(
		domain.TaskResultSucceeded,
		"全部必需步骤已通过验证，代码已保存",
		now,
	); err != nil {
		return task, err
	}
	previous := task.Status
	if err := task.Transition(domain.TaskStatusCompleted, now); err != nil {
		return task, fmt.Errorf("标记任务完成失败: %w", err)
	}
	if err := task.SetCurrentStep(nil); err != nil {
		return task, err
	}
	if err := s.writeTask(ctx, task, []domain.TaskStatus{previous}, grant); err != nil {
		return task, fmt.Errorf("写入任务状态失败: %w", err)
	}
	return task, nil
}

// taskCompletionPath 返回从当前状态走到 saving 的合法迁移路径。
//
// 路径显式列出，而不是贪心地尝试所有状态：任务状态机允许 recovery_required 回到
// preparing，贪心尝试会把一个已验证完成的任务重新拉回环境准备阶段。
func taskCompletionPath(current domain.TaskStatus) []domain.TaskStatus {
	switch current {
	case domain.TaskStatusSaving:
		return nil
	case domain.TaskStatusVerifying:
		return []domain.TaskStatus{domain.TaskStatusSaving}
	case domain.TaskStatusDeveloping:
		return []domain.TaskStatus{domain.TaskStatusVerifying, domain.TaskStatusSaving}
	case domain.TaskStatusPreparing:
		return []domain.TaskStatus{
			domain.TaskStatusPlanning,
			domain.TaskStatusPlanned,
			domain.TaskStatusDeveloping,
			domain.TaskStatusVerifying,
			domain.TaskStatusSaving,
		}
	default:
		// planned / retrying / paused / recovery_required 都可以直接进入 developing。
		return []domain.TaskStatus{
			domain.TaskStatusDeveloping,
			domain.TaskStatusVerifying,
			domain.TaskStatusSaving,
		}
	}
}

// stopForInterruption 依据真实中断来源收敛本次推进。
//
// 三种来源必须区分，否则诊断信息会把它们混成同一种结果：
//   - 用户取消（`cancel_requested` 已落盘）→ 走 stopForCancel；
//   - 执行权已丢失（续租失败）→ 不写任何终态：数据库里的记录已属于别的执行者，
//     本次推进只停止，由真实持有者决定后续；
//   - 服务关闭或调用方取消 → 收敛为 recovery_required，现场保留等待人工核验。
//
// 不把执行权丢失写成 cancelled / blocked：那会用过期执行者的本地对象覆盖真实记录，
// 正是续租机制要阻止的覆盖。
func (s *TaskWorkflowService) stopForInterruption(
	writeCtx context.Context,
	run domain.WorkflowRun,
	ctx context.Context,
) domain.WorkflowRun {
	if run.CancelRequested || errors.Is(context.Cause(ctx), errWorkflowCancelRequested) {
		return s.stopForCancel(writeCtx, run, ctx)
	}
	if errors.Is(context.Cause(ctx), errWorkflowLeaseLost) {
		return run
	}
	return s.stopRun(
		writeCtx,
		run,
		domain.WorkflowRunRecoveryRequired,
		"worker_interrupted",
		fmt.Errorf("%s", cancelCause(ctx)),
	)
}

// stopForCancel 处理取消收敛。
//
// 取消不是普通失败：请求已落盘，执行者确认中断后把记录收敛为 cancelled，并把任务
// 按状态机推进到 cancelled；无法确认现场时收敛为 recovery_required。
func (s *TaskWorkflowService) stopForCancel(
	writeCtx context.Context,
	run domain.WorkflowRun,
	ctx context.Context,
) domain.WorkflowRun {
	// 取消请求由另一个请求线程写入数据库，本地副本可能已经过期：先读取真实状态，
	// 避免用旧状态做迁移（running → cancelled 会被状态机拒绝）。
	if current, err := s.store.GetWorkflowRun(writeCtx, run.ID); err == nil {
		run = *current
	}
	if !run.CancelRequested {
		if err := run.RequestCancel(s.now()); err != nil {
			return s.failRun(writeCtx, run, "cancel", err)
		}
	}
	if run.Status != domain.WorkflowRunCancelRequested {
		if err := run.Transition(domain.WorkflowRunCancelRequested, s.now()); err != nil {
			// 状态机不允许时保留原状态，只记录原因，不伪造"已取消"。
			return s.stopRun(writeCtx, run, domain.WorkflowRunRecoveryRequired,
				"cancel_not_applicable", err)
		}
	}
	persisted, err := s.persistRun(writeCtx, run)
	if err != nil {
		return persisted
	}
	run = persisted
	if err := run.Cancel("工作流执行已按用户请求取消，容器现场保留", s.now()); err != nil {
		return s.stopRun(writeCtx, run, domain.WorkflowRunRecoveryRequired, "cancel_failed", err)
	}
	persisted, err = s.persistRun(writeCtx, run)
	if err != nil {
		return persisted
	}
	s.cancelTask(writeCtx, run.TaskID)
	s.appendEvent(writeCtx, run.TaskID, nil, nil, domain.EventTypeWorkflowCanceled, map[string]any{
		"run_id":       run.ID.String(),
		"completed":    persisted.CompletedSteps,
		"total_steps":  persisted.TotalSteps,
		"cancel_cause": cancelCause(ctx),
	})
	return persisted
}

// cancelTask 把任务收敛为已取消。
//
// 任务状态推进失败不回滚工作流结论：工作流记录已经如实说明取消已生效，任务状态由
// 用户通过任务接口处理。
func (s *TaskWorkflowService) cancelTask(ctx context.Context, taskID domain.ID) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil || task.Status.IsTerminal() {
		return
	}
	now := s.now()
	previous := task.Status
	if !task.CancelRequested {
		if err := task.RequestCancel(now); err != nil {
			return
		}
	}
	if task.Status != domain.TaskStatusCancelling {
		if err := task.Transition(domain.TaskStatusCancelling, now); err != nil {
			return
		}
	}
	if err := task.Finish(domain.TaskResultCancelled, "工作流执行已取消", now); err != nil {
		return
	}
	if err := task.Transition(domain.TaskStatusCancelled, now); err != nil {
		return
	}
	if err := s.store.UpdateTaskIfStatus(ctx, *task, []domain.TaskStatus{previous}); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入任务取消状态失败",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// stopUnpersistedCloseout 处理"执行记录的完成结论没能落盘"的现场。
//
// 只有在全部收尾条件满足、`run.Complete` 已经在内存副本上成功，而执行记录的终态
// 写入失败时才会走到这里。此时内存副本已经进入终态，任何状态机迁移都会被拒绝，
// 因此不能再用本地对象覆盖数据库：
//   - 先重新读取数据库中的真实记录，把停止状态写在真实值之上；
//   - 该写入同样失败时保留 running 现场并记录日志，由启动恢复按进程中断收敛；
//   - 任何情况下都不写 "工作流已完成" 事件。
//
// 注意：调用本方法时任务**尚未**被推进到成功终态（收尾顺序已改为"先执行记录、
// 后任务"），因此这里不需要、也不允许回退任务状态。
func (s *TaskWorkflowService) stopUnpersistedCloseout(
	ctx context.Context,
	run domain.WorkflowRun,
	cause error,
) domain.WorkflowRun {
	summary := "收尾结论已判定完成，但执行记录终态写入失败: " + errorSummary(cause)
	latest, readErr := s.store.GetWorkflowRun(ctx, run.ID)
	if readErr != nil {
		s.logger.ErrorContext(
			ctx,
			"收尾写入失败且无法重新读取执行记录，保留原始现场",
			slog.String("run_id", run.ID.String()),
			slog.String("task_id", run.TaskID.String()),
			slog.String("error", errorSummary(readErr)),
		)
		return run
	}
	return s.stopWithEvent(ctx, *latest, domain.WorkflowRunRecoveryRequired,
		"closeout_failed", summary, domain.EventTypeWorkflowRecovery, map[string]any{
			"run_id":          latest.ID.String(),
			"completed_steps": latest.CompletedSteps,
			"total_steps":     latest.TotalSteps,
		})
}

// stopRun 把工作流收敛为可恢复停止或需要人工核验，并记录原因。
func (s *TaskWorkflowService) stopRun(
	ctx context.Context,
	run domain.WorkflowRun,
	status domain.WorkflowRunStatus,
	errorClass string,
	cause error,
) domain.WorkflowRun {
	summary := ""
	if cause != nil {
		summary = cause.Error()
	}
	return s.stopWithEvent(ctx, run, status, errorClass, summary, domain.EventTypeWorkflowBlocked, nil)
}

// stopWithEvent 写入停止状态并追加事件。
func (s *TaskWorkflowService) stopWithEvent(
	ctx context.Context,
	run domain.WorkflowRun,
	status domain.WorkflowRunStatus,
	errorClass, summary string,
	eventType domain.EventType,
	payload map[string]any,
) domain.WorkflowRun {
	if status != domain.WorkflowRunBlocked && status != domain.WorkflowRunRecoveryRequired {
		status = domain.WorkflowRunBlocked
	}
	if err := run.Stop(status, errorClass, summary, s.now()); err != nil {
		// 状态机不允许时退化为失败终态，避免记录长期停留在 running。
		if failErr := run.Fail(errorClass, summary, s.now()); failErr != nil {
			return run
		}
	}
	persisted, err := s.persistRun(ctx, run)
	if err != nil {
		return run
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["run_id"] = persisted.ID.String()
	payload["status"] = string(persisted.Status)
	payload["error_class"] = errorClass
	payload["summary"] = summary
	payload["completed_steps"] = persisted.CompletedSteps
	payload["total_steps"] = persisted.TotalSteps
	s.appendEvent(ctx, persisted.TaskID, persisted.CurrentStepID, persisted.CurrentAttemptID,
		eventType, payload)
	return persisted
}

// failRun 把工作流收敛为失败终态。
func (s *TaskWorkflowService) failRun(
	ctx context.Context,
	run domain.WorkflowRun,
	stage string,
	cause error,
) domain.WorkflowRun {
	summary := stage
	if cause != nil {
		summary = stage + ": " + cause.Error()
	}
	if err := run.Fail(stage, summary, s.now()); err != nil {
		return run
	}
	persisted, err := s.persistRun(ctx, run)
	if err != nil {
		return run
	}
	s.appendEvent(ctx, persisted.TaskID, nil, nil, domain.EventTypeWorkflowFailed, map[string]any{
		"run_id":  persisted.ID.String(),
		"stage":   stage,
		"summary": summary,
	})
	return persisted
}

// codeSaveOutcome 是一次代码保存的结论投影。
type codeSaveOutcome struct {
	// succeeded 表示任务分支已成功推送（并按配置完成合并）。
	succeeded bool
	// indeterminate 表示 Git 结果无法确认，需要先核验远程分支。
	indeterminate bool
	// unconfirmed 表示记录本身未能可靠写入，数据库可能落后于真实 Git 状态。
	unconfirmed bool
	// unavailable 表示本次运行没有代码保存能力。
	unavailable bool
}

// saveCode 在收尾阶段执行一次代码保存。
//
// 它只调用既有的代码保存能力：编排器不构造 Git 命令，也不决定"推送成功即任务成功"。
func (s *TaskWorkflowService) saveCode(
	ctx context.Context,
	task domain.Task,
	run *domain.WorkflowRun,
) (codeSaveOutcome, error) {
	if s.codeSaver == nil {
		return codeSaveOutcome{unavailable: true}, nil
	}
	if err := run.SetStage(domain.WorkflowStageCodeSave, nil, nil, s.now()); err != nil {
		return codeSaveOutcome{}, err
	}
	persisted, persistErr := s.persistRun(ctx, *run)
	if persistErr != nil {
		// 阶段写入失败时不执行有副作用的远程操作：否则中断后无法定位保存进度。
		return codeSaveOutcome{}, persistErr
	}
	*run = persisted
	result, err := s.codeSaver.SaveCode(ctx, CodeSaveRequest{
		ContainerID:    task.ContainerID,
		ProjectID:      task.Config.ProjectID,
		TaskID:         task.ID.String(),
		WorkDir:        task.Config.EffectiveWorkDir(),
		BaseBranch:     task.BaseRef,
		MergeAfterPush: task.Config.MergeAfterPush,
	})
	record := result.Result.Record
	outcome := codeSaveOutcome{
		succeeded:     codeSaveSucceeded(result, record),
		indeterminate: codeSaveIndeterminate(result, record),
		unconfirmed:   !result.Result.Persisted(),
	}
	s.appendEvent(ctx, task.ID, nil, nil, domain.EventTypeWorkflowCodeSaved, map[string]any{
		"run_id":         run.ID.String(),
		"record_id":      record.ID.String(),
		"commit_status":  record.CommitStatus.String(),
		"push_status":    record.PushStatus.String(),
		"merge_status":   record.MergeStatus.String(),
		"persist_status": record.PersistStatus.Normalized().String(),
		"persisted":      result.Result.Persisted(),
		"error_summary":  record.ErrorSummary,
	})
	return outcome, err
}

// sessionsPersisted 核验会话文件是否已可靠保存。
//
// 判定同时要求原始会话与归档都成立：完整性必须保留可用的原始 JSONL，归档状态必须
// 是 succeeded 且给出归档位置。export_failed 只代表 HTML 导出失败，原始 JSONL 已保存
// 并归档时不算失败；没有会话记录、只保存了部分内容、归档失败、未尝试归档、归档位置
// 缺失或查询失败一律不算完成。
func (s *TaskWorkflowService) sessionsPersisted(
	ctx context.Context,
	task domain.Task,
) bool {
	sessions, err := s.store.ListSessionsByTask(ctx, task.ID)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"核验会话持久化状态失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return false
	}
	if len(sessions) == 0 {
		return false
	}
	for _, session := range sessions {
		if !sessionArchived(session) {
			return false
		}
	}
	return true
}

// sessionArchived 判断单条会话的原始文件是否已可靠归档。
func sessionArchived(session domain.Session) bool {
	switch session.Completeness {
	case domain.SessionCompletenessComplete, domain.SessionCompletenessExportFailed:
	default:
		// partial 与 missing 都表示原始会话不完整或不存在，不能算归档完成。
		return false
	}
	if session.ArchiveStatus != domain.SessionArchiveSucceeded {
		return false
	}
	return strings.TrimSpace(session.ArchivePath) != ""
}

// allExecutableStepsCompleted 判断全部可自动推进的步骤是否都已确认完成。
//
// 规划阶段步骤与开发步骤都参与判定：只统计开发步骤会让"计划以 plan 开头"的执行
// 在规划阶段未收敛时被误判为可以收尾。
func allExecutableStepsCompleted(steps []domain.Step) bool {
	found := false
	for _, step := range steps {
		switch step.Type {
		case domain.StepTypePlan, domain.StepTypeDevelop:
		default:
			continue
		}
		found = true
		switch step.Status {
		case domain.StepStatusCompleted, domain.StepStatusSkipped:
			continue
		default:
			return false
		}
	}
	return found
}

// codeSaveSucceeded 判断代码保存是否达到"已安全保存"的门槛。
//
// 只有任务分支推送成功才算安全保存；合并按配置要求单独判断。
//
// 前置条件是"记录已可靠落盘"：内存中的结论不能替代数据库事实，否则持续写入失败时
// 任务会被误判为完整成功。
func codeSaveSucceeded(
	result CodeSaveResult,
	record domain.CodeSaveRecord,
) bool {
	if record.ID.IsEmpty() || !result.Result.Persisted() {
		return false
	}
	if record.PushStatus != domain.PushStatusSucceeded {
		return false
	}
	if record.MergeEnabled {
		return record.MergeStatus == domain.MergeStatusSucceeded
	}
	return true
}

// codeSaveIndeterminate 判断代码保存结果是否需要人工核验。
//
// 记录写入未确认时不在此处判定：它由 codeSaveOutcome.unconfirmed 单独表达，两者的
// 恢复动作不同，不能合并成一个分类。
//
// 判定依据是"是否仍有未决外部副作用"：已经核验确认未发生（confirmed_not_applied）
// 的记录虽然仍需人工判断是否重试，但它的 Git 事实已经明确，不再属于"结果无法确认"。
// 使用 RequiresManualRecovery 会把这类已核验的记录继续判为不确定，使恢复永远无法收敛。
func codeSaveIndeterminate(
	result CodeSaveResult,
	record domain.CodeSaveRecord,
) bool {
	if record.ID.IsEmpty() || !result.Result.Persisted() {
		return false
	}
	if record.PersistStatus.IsUnconfirmed() {
		return false
	}
	return record.HasUnresolvedSideEffect()
}

// planningStopSummary 生成规划阶段停止的说明。
//
// 规划阶段没有验证记录：说明里只描述 Planner 的真实执行结论，不引用并不存在的
// 验证结果。
func planningStopSummary(result ExecutePlanningResult) string {
	detail := strings.TrimSpace(result.Failure)
	if detail == "" {
		detail = strings.TrimSpace(result.Summary)
	}
	if detail == "" {
		detail = "规划阶段未成功"
	}
	return "规划阶段执行结论为 " + string(result.Outcome) + ": " + detail
}

// developerStopSummary 生成开发阶段停止的说明。
func developerStopSummary(result ExecuteAndValidateResult) string {
	detail := strings.TrimSpace(result.DeveloperFailure)
	if detail == "" {
		detail = strings.TrimSpace(result.DeveloperSummary)
	}
	if detail == "" {
		detail = "开发阶段未成功"
	}
	return "步骤开发阶段结论为 " + string(result.Outcome) + ": " + detail
}

// unconfirmedStopSummary 生成"结果无法确认"的说明。
func unconfirmedStopSummary(result ExecuteAndValidateResult) string {
	details := append([]string(nil), result.ValidationPersistenceErrors...)
	details = append(details, result.FinalizationErrors...)
	details = append(details, result.DeveloperFinalizationErrors...)
	summary := "步骤执行记录或验证证据未能可靠写入，结果无法确认"
	if len(details) > 0 {
		summary += ": " + strings.Join(details, "；")
	}
	return summary
}

// verificationStopSummary 生成验证未通过的说明。
func verificationStopSummary(result ExecuteAndValidateResult) string {
	detail := strings.TrimSpace(result.ValidationFailureSummary)
	if detail == "" {
		detail = strings.TrimSpace(result.ValidationSkipReason)
	}
	if detail == "" {
		detail = "验证未通过"
	}
	return "步骤验证结论为 " + string(result.ValidationVerdict) + ": " + detail
}

// cancelCause 返回取消的触发来源说明。
func cancelCause(ctx context.Context) string {
	cause := context.Cause(ctx)
	switch {
	case cause == nil:
		return "用户请求取消"
	case errors.Is(cause, errWorkflowLeaseLost):
		return "执行权已丢失，本次推进已停止"
	case errors.Is(cause, ErrWorkflowStopped):
		return "服务关闭导致中断"
	case errors.Is(cause, errWorkflowCancelRequested):
		return "用户请求取消"
	case errors.Is(cause, context.Canceled):
		return "调用方取消"
	case errors.Is(cause, context.DeadlineExceeded):
		return "调用超时"
	default:
		return cause.Error()
	}
}

// idPtr 把标识转换为可空指针，空标识返回 nil。
func idPtr(id domain.ID) *domain.ID {
	if id.IsEmpty() {
		return nil
	}
	value := id
	return &value
}
