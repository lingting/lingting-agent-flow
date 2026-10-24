package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// recoverStalledDelivery 收敛停滞在收尾阶段（verifying / saving）的任务。
//
// 收尾把任务从 developing 依次推进到 verifying、saving、completed，任何一次写入
// 中断都会留下一个"既不能启动也不能恢复"的任务现场：`CheckTaskLaunchStart` 与
// `TaskStatus.AllowsStepExecution` 都不接受这两个状态，`recoverUnfinished` 只收敛
// 执行记录、不改写任务。本方法补上这一条收敛路径。
//
// 语义固定为：
//   - 只处理 verifying / saving，其余状态由各自的恢复入口负责，不重复判定；
//   - 执行记录仍持有有效执行权时不抢占；
//   - 执行记录已确认完成时，按持久化事实重算收尾结论：满足则补齐任务成功终态，
//     不满足则收敛为 recovery_required；
//   - 其余情况一律收敛为 recovery_required 并说明原因，不重放任何外部操作；
//   - 所有写入带状态前置条件，冲突视为"其他执行者已完成同一次收敛"。
//
// 判定规则集中在 domain.DecideTaskCloseoutRecovery，本方法不复制第二套规则。
// 写入失败必须让启动失败：停滞任务没有被收敛时服务继续启动，用户会看到一个
// 永远无法继续的任务。
func (s *TaskWorkflowService) recoverStalledDelivery(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		Statuses: []domain.TaskStatus{
			domain.TaskStatusVerifying,
			domain.TaskStatusSaving,
		},
	})
	if err != nil {
		return fmt.Errorf("读取收尾停滞任务失败: %w", err)
	}
	now := s.now()
	for _, task := range tasks {
		if err := s.recoverStalledTask(ctx, task, now); err != nil {
			return fmt.Errorf("收敛任务 %s 的收尾停滞现场失败: %w", task.ID, err)
		}
	}
	return nil
}

// recoverStalledTask 收敛单条停滞任务的收尾现场。
func (s *TaskWorkflowService) recoverStalledTask(
	ctx context.Context,
	task domain.Task,
	now time.Time,
) error {
	latest, err := s.latestWorkflowRun(ctx, task.ID)
	if err != nil {
		return err
	}
	input := domain.TaskCloseoutRecoveryInput{
		Task: task,
		Run:  latest,
		Now:  now,
	}
	if latest != nil && latest.Status == domain.WorkflowRunCompleted {
		// 执行记录已是终态、无法再被改写：任务能否收口只能按持久化证据重算。
		// 重算失败时按"证据无法确认"收敛，不得据此判定成功。
		decision, decisionErr := s.recomputeCloseout(ctx, task)
		if decisionErr != nil {
			s.logger.WarnContext(
				ctx,
				"收尾停滞恢复无法重算收尾证据",
				slog.String("task_id", task.ID.String()),
				slog.String("run_id", latest.ID.String()),
				slog.String("error", errorSummary(decisionErr)),
			)
			decision = domain.WorkflowCloseoutDecision{
				Status:     domain.WorkflowRunRecoveryRequired,
				ErrorClass: domain.CloseoutClassEvidenceMissing,
				Summary:    "按持久化事实无法重算收尾结论: " + errorSummary(decisionErr),
			}
		}
		input.Decision = decision
	}
	result := domain.DecideTaskCloseoutRecovery(input)
	if !result.Needed {
		return nil
	}
	if result.Complete {
		return s.completeStalledTask(ctx, task, *latest, result)
	}
	return s.stopStalledTask(ctx, task, latest, result)
}

// completeStalledTask 补齐停滞任务的成功终态。
//
// 只有执行记录已经确认 completed 且证据重算为通过时才走到这里：此时收尾的副作用
// 已全部完成，恢复只是补齐任务字段，不重放任何外部操作。
func (s *TaskWorkflowService) completeStalledTask(
	ctx context.Context,
	task domain.Task,
	run domain.WorkflowRun,
	result domain.TaskCloseoutRecoveryDecision,
) error {
	updated, err := s.completeTask(context.WithoutCancel(ctx), task)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 其他执行者已经推进了任务：不是失败，也不重复写事件。
			return nil
		}
		// 无法补齐完成结论时不得报告成功：退化为需要人工核验。
		return s.stopStalledTask(ctx, task, &run, domain.TaskCloseoutRecoveryDecision{
			Needed:     true,
			Next:       domain.TaskStatusRecoveryRequired,
			ErrorClass: domain.CloseoutClassTaskCompletionFailed,
			Summary:    "执行记录已确认完成，但补齐任务成功终态失败: " + errorSummary(err),
		})
	}
	s.appendEvent(context.WithoutCancel(ctx), task.ID, nil, nil,
		domain.EventTypeWorkflowCompleted, map[string]any{
			"run_id":      run.ID.String(),
			"status":      string(domain.WorkflowRunCompleted),
			"error_class": result.ErrorClass,
			"summary":     result.Summary,
			"task_status": string(updated.Status),
			"recovery":    true,
		})
	return nil
}

// stopStalledTask 把停滞任务收敛为需要人工核验。
func (s *TaskWorkflowService) stopStalledTask(
	ctx context.Context,
	task domain.Task,
	run *domain.WorkflowRun,
	result domain.TaskCloseoutRecoveryDecision,
) error {
	recovered := task
	previous := recovered.Status
	if previous != result.Next {
		if err := recovered.Transition(result.Next, s.now()); err != nil {
			return err
		}
	}
	recovered.FailureSummary = result.Summary
	writeCtx := context.WithoutCancel(ctx)
	if err := s.store.UpdateTaskIfStatus(writeCtx, recovered, []domain.TaskStatus{previous}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 另一个进程已经推进了该任务：不是失败，也不重复写事件。
			return nil
		}
		return err
	}
	payload := map[string]any{
		"stage":       "closeout",
		"status":      string(result.Next),
		"error_class": result.ErrorClass,
		"summary":     result.Summary,
		"recovery":    true,
	}
	if run != nil {
		payload["run_id"] = run.ID.String()
		payload["run_status"] = string(run.Status)
	}
	s.appendEvent(writeCtx, task.ID, nil, nil, domain.EventTypeTaskRecoveryRequired, payload)
	return nil
}

// latestWorkflowRun 返回任务最近一次执行记录，没有时返回 nil。
//
// 读取失败不能被当作"没有执行记录"：那会让恢复在没有证据的情况下把任务收敛为
// 需要人工核验，掩盖真实的数据库问题。
func (s *TaskWorkflowService) latestWorkflowRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	runs, err := s.store.ListWorkflowRunsByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("读取任务 %s 的工作流执行记录失败: %w", taskID, err)
	}
	if len(runs) == 0 {
		return nil, nil
	}
	latest := runs[0]
	return &latest, nil
}

// recomputeCloseout 按持久化事实重算一次收尾结论。
//
// 它复用 finalize 中的同一组证据来源（步骤、代码保存记录、会话归档），但**不执行**
// 任何副作用：代码保存不再运行，只读取已有记录。这正是"执行记录已完成、任务未收口"
// 场景下唯一允许的动作。
func (s *TaskWorkflowService) recomputeCloseout(
	ctx context.Context,
	task domain.Task,
) (domain.WorkflowCloseoutDecision, error) {
	steps, err := s.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return domain.WorkflowCloseoutDecision{}, fmt.Errorf("读取计划步骤失败: %w", err)
	}
	codeSave, err := s.inspectCodeSave(ctx, task)
	if err != nil {
		return domain.WorkflowCloseoutDecision{}, err
	}
	return domain.DecideWorkflowCloseout(domain.WorkflowCloseoutInput{
		StepsCompleted:        allExecutableStepsCompleted(steps),
		CodeSaveRequested:     true,
		CodeSaveSucceeded:     codeSave.succeeded,
		CodeSaveIndeterminate: codeSave.indeterminate,
		CodeSaveUnconfirmed:   codeSave.unconfirmed,
		SessionPersisted:      s.sessionsPersisted(ctx, task),
	}), nil
}

// inspectCodeSave 只读地判断任务的代码保存是否已安全完成。
//
// 它不调用代码保存组件，只读已有的保存记录：记录缺失、不可信或仍有未决副作用时
// 一律按"未成功"回答，绝不因为"没有记录"就假设保存成功。
func (s *TaskWorkflowService) inspectCodeSave(
	ctx context.Context,
	task domain.Task,
) (codeSaveOutcome, error) {
	if s.codeSaver == nil {
		return codeSaveOutcome{unavailable: true}, nil
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return codeSaveOutcome{}, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	if len(records) == 0 {
		return codeSaveOutcome{}, nil
	}
	// 记录按创建时间倒序返回：最近一次保存决定了任务的交付结论。
	record := records[0]
	return codeSaveOutcome{
		succeeded:     codeSaveRecordSucceeded(record),
		indeterminate: codeSaveRecordIndeterminate(record),
		unconfirmed:   record.PersistStatus.IsUnconfirmed(),
	}, nil
}

// codeSaveRecordSucceeded 依据已持久化记录判断代码保存是否达到"已安全保存"。
func codeSaveRecordSucceeded(record domain.CodeSaveRecord) bool {
	if record.ID.IsEmpty() || record.PersistStatus.IsUnconfirmed() {
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

// codeSaveRecordIndeterminate 依据已持久化记录判断结果是否仍需人工核验。
func codeSaveRecordIndeterminate(record domain.CodeSaveRecord) bool {
	if record.ID.IsEmpty() || record.PersistStatus.IsUnconfirmed() {
		return false
	}
	return record.HasUnresolvedSideEffect()
}

// reportCloseoutInconsistencies 报告"任务被标记为成功、执行记录没有确认完成"的现场。
//
// completed 是任务终态，恢复**不得**改写它：把终态回退成 recovery_required 会让
// "已完成"不再可信，也无法区分真实成功与收尾失败。因此这里只做只读发现：
//   - 追加一条 closeout_inconsistent 事件（按工作流记录去重，重启不重复刷屏）；
//   - 写一条告警日志，便于运维排查。
//
// 读取失败让启动失败：无法判断是否存在不一致现场时继续启动，会把"结论存疑"的
// 任务当作确定成功对外展示。
func (s *TaskWorkflowService) reportCloseoutInconsistencies(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskStatusCompleted},
	})
	if err != nil {
		return fmt.Errorf("读取已完成任务失败: %w", err)
	}
	now := s.now()
	for _, task := range tasks {
		latest, readErr := s.latestWorkflowRun(ctx, task.ID)
		if readErr != nil {
			return readErr
		}
		state := domain.DecideTaskCloseoutView(domain.TaskCloseoutViewInput{
			Task: task,
			Run:  latest,
			Now:  now,
		})
		if state != domain.TaskCloseoutInconsistent {
			continue
		}
		s.reportCloseoutInconsistency(ctx, task, latest)
	}
	return nil
}

// reportCloseoutInconsistency 报告单条不一致现场。
//
// 事件写入失败只记录日志：它不影响任何业务状态，也不能让服务启动失败——报告本身
// 不是恢复结论，任务状态不需要因此改变。
func (s *TaskWorkflowService) reportCloseoutInconsistency(
	ctx context.Context,
	task domain.Task,
	run *domain.WorkflowRun,
) {
	runID := ""
	runStatus := ""
	if run != nil {
		runID = run.ID.String()
		runStatus = string(run.Status)
	}
	s.logger.WarnContext(
		ctx,
		"存在任务结论与执行记录不一致的现场",
		slog.String("task_id", task.ID.String()),
		slog.String("task_status", string(task.Status)),
		slog.String("final_result", string(task.FinalResult)),
		slog.String("run_id", runID),
		slog.String("run_status", runStatus),
	)
	payload := map[string]any{
		"stage":         "closeout_reconcile",
		"task_status":   string(task.Status),
		"final_result":  string(task.FinalResult),
		"run_id":        runID,
		"run_status":    runStatus,
		"summary":       domain.TaskCloseoutInconsistentSummary(task, run),
		"recovery_auto": false,
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalStepExecutionPayload(payload),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造任务结论不一致事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入任务结论不一致事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}
