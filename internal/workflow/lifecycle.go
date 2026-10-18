package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
)

// internalStepSequence 是内部阶段步骤使用的序号基值。
//
// 内部阶段（例如规划）需要独立的步骤记录来承载 Attempt 与 Session 归属，
// 但它们不是计划步骤：使用独立序号区间可以避免与计划步骤的 1..N 序号冲突，
// 计划步骤的连续性校验也不会被破坏。
const internalStepSequence = 1000

// createAttemptStep 创建一个内部阶段步骤并写入数据库。
func (e *Engine) createAttemptStep(
	ctx context.Context,
	task *domain.Task,
	stepType domain.StepType,
	title, objective string,
	role domain.Role,
) (domain.Step, error) {
	step, err := domain.NewAgentStep(
		task.ID,
		internalStepSequence,
		stepType,
		title,
		objective,
		"",
		role,
		domain.VerificationSpec{},
		task.Config.Retry,
		e.now(),
	)
	if err != nil {
		return domain.Step{}, fmt.Errorf("创建工作流步骤失败: %w", err)
	}
	if err := e.store.CreateStep(ctx, step); err != nil {
		return domain.Step{}, fmt.Errorf("写入工作流步骤失败: %w", err)
	}
	if err := step.Transition(domain.StepStatusRunning, e.now()); err != nil {
		return domain.Step{}, fmt.Errorf("启动工作流步骤失败: %w", err)
	}
	if err := e.store.UpdateStep(ctx, step); err != nil {
		return domain.Step{}, fmt.Errorf("保存工作流步骤失败: %w", err)
	}
	return step, nil
}

// completeInternalStep 把内部阶段步骤标记为完成。
func (e *Engine) completeInternalStep(
	ctx context.Context,
	step domain.Step,
	summary string,
) error {
	step.Result = strings.TrimSpace(summary)
	if err := step.Transition(domain.StepStatusCompleted, e.now()); err != nil {
		return fmt.Errorf("标记工作流步骤完成失败: %w", err)
	}
	if err := e.store.UpdateStep(ctx, step); err != nil {
		return fmt.Errorf("保存工作流步骤失败: %w", err)
	}
	return nil
}

// runStepLifecycle 为内部阶段执行一次带重试上限的尝试循环。
//
// 每次尝试都创建新的 Attempt 与独立 Session；失败后按任务重试策略决定是否重试。
func (e *Engine) runStepLifecycle(
	ctx context.Context,
	task *domain.Task,
	step domain.Step,
	run func(ctx context.Context, attempt *domain.Attempt) error,
) error {
	policy := effectiveRetry(task.Config.Retry, step.Retry, step.MaxAttempts)
	number := 1
	for {
		attempt, err := domain.NewAttempt(step.ID, number, e.now())
		if err != nil {
			return fmt.Errorf("创建执行尝试失败: %w", err)
		}
		if err := e.store.CreateAttempt(ctx, attempt); err != nil {
			return fmt.Errorf("写入执行尝试失败: %w", err)
		}
		if err := e.appendEvent(ctx, *task, &step.ID, &attempt.ID, nil,
			domain.EventTypeAttemptStarted,
			map[string]any{"attempt": number, "stage": string(step.Type)},
		); err != nil {
			return err
		}
		runErr := run(ctx, &attempt)
		if runErr == nil {
			if err := attempt.Complete(e.now()); err != nil {
				return fmt.Errorf("标记尝试完成失败: %w", err)
			}
			if err := e.store.UpdateAttempt(ctx, attempt); err != nil {
				return fmt.Errorf("保存执行尝试失败: %w", err)
			}
			return e.completeInternalStep(ctx, step, string(step.Type)+" 阶段完成")
		}
		stage, class, summary, timedOut, cancelled := classifyStepError(runErr)
		if cancelled {
			if attempt.Status == domain.AttemptStatusRunning {
				interrupted := attempt
				if err := interrupted.Cancel(summary, e.now()); err != nil {
					return fmt.Errorf("标记尝试取消失败: %w", err)
				}
				interrupted.FailureStage = stage
				interrupted.ErrorClass = string(class)
				if err := e.store.UpdateAttempt(ctx, interrupted); err != nil {
					return fmt.Errorf("保存执行尝试失败: %w", err)
				}
			}
			return e.handleCancel(ctx, *task, context.Canceled)
		}
		if attempt.Status == domain.AttemptStatusRunning {
			var markErr error
			if timedOut {
				markErr = attempt.Timeout(summary, e.now())
			} else {
				markErr = attempt.Fail(summary, e.now())
			}
			if markErr != nil {
				return fmt.Errorf("标记尝试失败失败: %w", markErr)
			}
			attempt.ErrorClass = string(class)
			attempt.FailureStage = stage
			if err := e.store.UpdateAttempt(ctx, attempt); err != nil {
				return fmt.Errorf("保存执行尝试失败: %w", err)
			}
		}
		if err := e.appendEvent(ctx, *task, &step.ID, &attempt.ID, nil,
			domain.EventTypeAttemptFailed,
			map[string]any{"stage": stage, "class": string(class), "summary": summary},
		); err != nil {
			return err
		}
		if !policy.Allows(class) || number >= policy.Limit() {
			return e.failTask(
				ctx,
				task,
				stage,
				domain.TaskResultFailed,
				fmt.Sprintf("%s 阶段失败且不再重试: %s", stage, summary),
			)
		}
		number++
	}
}

// startAttempt 创建并启动一次开发步骤尝试。
func (e *Engine) startAttempt(
	ctx context.Context,
	task *domain.Task,
	step *domain.Step,
) (domain.Attempt, error) {
	number, err := e.store.NextAttemptNumber(ctx, step.ID)
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("查询尝试序号失败: %w", err)
	}
	attempt, err := domain.NewAttempt(step.ID, number, e.now())
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("创建执行尝试失败: %w", err)
	}
	attempt.InputDigest = digestInput(*task, *step)
	if err := e.store.CreateAttempt(ctx, attempt); err != nil {
		return domain.Attempt{}, fmt.Errorf("写入执行尝试失败: %w", err)
	}
	if err := step.SetCurrentAttempt(&attempt.ID); err != nil {
		return domain.Attempt{}, err
	}
	if step.Status == domain.StepStatusPending || step.Status == domain.StepStatusRetryWait {
		if err := step.Transition(domain.StepStatusRunning, e.now()); err != nil {
			return domain.Attempt{}, fmt.Errorf("启动步骤失败: %w", err)
		}
	}
	if err := e.store.UpdateStep(ctx, *step); err != nil {
		return domain.Attempt{}, fmt.Errorf("保存步骤失败: %w", err)
	}
	if err := task.SetCurrentStep(&step.ID); err != nil {
		return domain.Attempt{}, err
	}
	if err := task.SetCurrentAttempt(&attempt.ID); err != nil {
		return domain.Attempt{}, err
	}
	if err := e.store.UpdateTask(ctx, *task); err != nil {
		return domain.Attempt{}, fmt.Errorf("保存任务失败: %w", err)
	}
	if err := e.appendEvent(ctx, *task, &step.ID, &attempt.ID, nil,
		domain.EventTypeAttemptStarted,
		map[string]any{"attempt": number, "step_type": string(step.Type)},
	); err != nil {
		return domain.Attempt{}, err
	}
	return attempt, nil
}

// digestInput 生成尝试输入配置的摘要，用于区分不同输入的同一步骤尝试。
func digestInput(task domain.Task, step domain.Step) string {
	payload, err := json.Marshal(struct {
		TaskConfig domain.TaskConfig
		StepID     string
		Objective  string
		Spec       domain.VerificationSpec
	}{
		TaskConfig: task.Config,
		StepID:     step.ID.String(),
		Objective:  step.Objective,
		Spec:       step.VerificationSpec,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// finalize 完成代码保存与成功清理。
//
// 只有全部开发步骤完成、所有必需验证通过后才会进入该阶段；代码保存结果分别
// 记录 Commit、Push 与 Merge，未确认的保存结果不会让任务进入 succeeded。
func (e *Engine) finalize(ctx context.Context, task domain.Task) (domain.Task, error) {
	steps, err := e.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return task, fmt.Errorf("读取计划步骤失败: %w", err)
	}
	if !allStepsCompleted(steps) {
		return task, e.failTask(
			ctx,
			&task,
			"finalize",
			domain.TaskResultFailed,
			"仍有未完成的必需步骤，拒绝进入成功保存阶段",
		)
	}
	if err := e.transition(ctx, &task, domain.TaskStatusSaving); err != nil {
		return task, err
	}
	outcome, saveErr := e.ports.CodeSaver.Save(ctx, CodeSaveSpec{
		ContainerID:    task.ContainerID,
		ProjectID:      task.Config.ProjectID,
		TaskID:         task.ID.String(),
		AttemptID:      attemptIDOf(task),
		WorkDir:        task.Config.EffectiveWorkDir(),
		BaseBranch:     task.BaseRef,
		MergeAfterPush: task.Config.MergeAfterPush,
	})
	record := outcome.Record
	if !record.ID.IsEmpty() {
		if err := e.appendEvent(ctx, task, nil, nil, nil,
			domain.EventTypeCodeSaved,
			map[string]any{
				"commit_status": record.CommitStatus.String(),
				"commit_sha":    record.CommitSHA,
				"push_status":   record.PushStatus.String(),
				"merge_status":  record.MergeStatus.String(),
			},
		); err != nil {
			return task, err
		}
	}
	if saveErr != nil {
		if requiresRecovery(record) {
			return task, e.requireRecovery(
				ctx,
				&task,
				"saving",
				"代码保存结果无法确认: "+saveErr.Error(),
			)
		}
		return task, e.failTask(
			ctx,
			&task,
			"saving",
			domain.TaskResultFailed,
			"代码保存失败: "+saveErr.Error(),
		)
	}
	if !codeSaved(record) {
		if requiresRecovery(record) {
			return task, e.requireRecovery(
				ctx,
				&task,
				"saving",
				"代码保存状态需要人工核验",
			)
		}
		return task, e.failTask(
			ctx,
			&task,
			"saving",
			domain.TaskResultFailed,
			"代码保存未完成: "+codeSaveSummary(record),
		)
	}
	return e.succeed(ctx, task, record)
}

// attemptIDOf 返回任务当前尝试标识，缺失时返回空字符串。
func attemptIDOf(task domain.Task) string {
	if task.CurrentAttemptID == nil {
		return ""
	}
	return task.CurrentAttemptID.String()
}

// allStepsCompleted 判断计划步骤是否全部完成或被允许跳过。
func allStepsCompleted(steps []domain.Step) bool {
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		switch step.Status {
		case domain.StepStatusCompleted:
		case domain.StepStatusSkipped:
			if step.Type.IsRequired() {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// codeSaved 判断代码保存是否达到"已安全保存"的门槛。
//
// 只有任务分支推送成功才算安全保存；合并按配置要求单独判断。
func codeSaved(record domain.CodeSaveRecord) bool {
	if record.ID.IsEmpty() {
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

// requiresRecovery 判断代码保存结果是否需要人工核验。
func requiresRecovery(record domain.CodeSaveRecord) bool {
	if record.ID.IsEmpty() {
		return false
	}
	return record.RequiresManualRecovery()
}

// codeSaveSummary 生成代码保存结果的摘要。
func codeSaveSummary(record domain.CodeSaveRecord) string {
	if record.ID.IsEmpty() {
		return "没有代码保存记录"
	}
	parts := []string{
		"提交=" + record.CommitStatus.String(),
		"推送=" + record.PushStatus.String(),
		"合并=" + record.MergeStatus.String(),
	}
	if record.ErrorSummary != "" {
		parts = append(parts, "错误="+record.ErrorSummary)
	}
	return strings.Join(parts, "，")
}

// succeed 在成功清理后把任务标记为完成。
//
// 成功清理只删除受管理的任务容器与任务级临时资源；共享镜像、共享缓存卷、
// 远程项目配置目录与 Session 都不会被删除。清理失败时任务停在
// recovery_required，不会被报告为完全成功。
func (e *Engine) succeed(
	ctx context.Context,
	task domain.Task,
	record domain.CodeSaveRecord,
) (domain.Task, error) {
	// 清理前核验会话持久化状态：缺失或部分保存必须记录，不能假定 Session 已安全保存。
	if err := e.recordSessionState(ctx, task); err != nil {
		return task, err
	}
	if strings.TrimSpace(task.ContainerID) != "" {
		info, err := e.ports.Containers.Inspect(ctx, task.ContainerID)
		if err != nil {
			return task, e.requireRecovery(
				ctx,
				&task,
				"cleanup",
				"清理前核验容器失败: "+err.Error(),
			)
		}
		if info.Detached {
			// 已脱离管理的容器不会被自动删除：任务业务成功，但清理未完成。
			return task, e.requireRecovery(
				ctx,
				&task,
				"cleanup",
				"任务容器已脱离 AgentFlow 管理，成功清理未完成",
			)
		}
		if !info.Removed {
			if err := e.ports.Containers.Dispose(
				ctx,
				task.ContainerID,
				domain.RestartDispositionDelete,
			); err != nil {
				return task, e.requireRecovery(
					ctx,
					&task,
					"cleanup",
					"删除任务容器失败: "+err.Error(),
				)
			}
		}
		if err := e.appendEvent(ctx, task, nil, nil, nil,
			domain.EventTypeContainerCleaned,
			map[string]any{"container_id": task.ContainerID},
		); err != nil {
			return task, err
		}
	}
	if err := task.Finish(
		domain.TaskResultSucceeded,
		"任务完成: "+codeSaveSummary(record),
		e.now(),
	); err != nil {
		return task, err
	}
	if err := task.SetCurrentStep(nil); err != nil {
		return task, err
	}
	if err := task.Transition(domain.TaskStatusCompleted, e.now()); err != nil {
		return task, fmt.Errorf("标记任务完成失败: %w", err)
	}
	if err := e.persistTaskIfStatus(ctx, task, domain.TaskStatusSaving); err != nil {
		return task, err
	}
	if err := e.appendEvent(ctx, task, nil, nil, nil,
		domain.EventTypeTaskCompleted,
		map[string]any{"summary": task.FailureSummary},
	); err != nil {
		return task, err
	}
	return task, nil
}

// recordSessionState 核验并记录会话文件的真实持久化状态。
//
// 该检查只记录事实，不阻止成功清理：Session 文件在本机独立保存，容器删除不会
// 导致已保存的会话丢失；缺失状态同样会作为事件留在任务时间线上。
func (e *Engine) recordSessionState(ctx context.Context, task domain.Task) error {
	sessions, err := e.store.ListSessionsByTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("核验会话持久化状态失败: %w", err)
	}
	missing := 0
	partial := 0
	for _, session := range sessions {
		switch session.Completeness {
		case domain.SessionCompletenessMissing:
			missing++
		case domain.SessionCompletenessPartial, domain.SessionCompletenessExportFailed:
			partial++
		}
	}
	if missing == 0 && partial == 0 {
		return nil
	}
	return e.appendEvent(ctx, task, nil, nil, nil,
		domain.EventTypeSessionCompleted,
		map[string]any{
			"sessions": len(sessions),
			"missing":  missing,
			"partial":  partial,
		},
	)
}

// requireRecovery 把任务推进到 recovery_required 并记录原因。
//
// 该状态表达"实际执行结果或资源状态无法自动确认"：既不是成功，也不是普通失败，
// 必须由用户显式执行恢复核验。
func (e *Engine) requireRecovery(
	ctx context.Context,
	task *domain.Task,
	stage, summary string,
) error {
	previous := task.Status
	if err := task.Transition(domain.TaskStatusRecoveryRequired, e.now()); err != nil {
		if errors.Is(err, domain.ErrInvalidTransition) {
			// 状态机不允许从当前状态直接进入 recovery_required 时，
			// 记录失败而不是伪造状态。
			return e.failTask(ctx, task, stage, domain.TaskResultFailed, summary)
		}
		return fmt.Errorf("标记恢复状态失败: %w", err)
	}
	if err := e.persistTaskIfStatus(ctx, *task, previous); err != nil {
		return err
	}
	if err := e.appendEvent(ctx, *task, nil, nil, nil,
		domain.EventTypeTaskRecoveryRequired,
		map[string]any{"stage": stage, "summary": summary},
	); err != nil {
		return err
	}
	return fmt.Errorf("%s: %s（任务已标记为需要恢复核验）", stage, summary)
}
