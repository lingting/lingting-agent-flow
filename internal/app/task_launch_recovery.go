package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// RecoverStepOccupancy 收敛进程退出留下的步骤执行占用残留。
//
// 被占用的步骤（running / developing / verifying）只说明"曾经有一次执行拿到了占用"。
// 进程在 Pi 调用期间退出时，占用会永久留在数据库里，而占用本身会阻止该步骤再次执行，
// 使任务既无法推进也无法自动收敛。
//
// 本方法只把这类步骤收敛为 recovery_required，等待人工核验现场：
//   - 不重放任何 Agent 调用；
//   - 不删除容器，现场一律保留；
//   - 不改写已确认的终态，所有写入都带状态前置条件。
//
// 判定规则集中在 domain.DecideStepOccupancyRecovery，本方法不复制第二套规则。
func (s *TaskLaunchService) RecoverStepOccupancy(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	occupied, err := s.store.ListTasksWithOccupiedSteps(ctx)
	if err != nil {
		return fmt.Errorf("读取执行占用步骤失败: %w", err)
	}
	for _, taskID := range occupied {
		if err := s.recoverTaskStepOccupancy(ctx, taskID); err != nil {
			return err
		}
	}
	return nil
}

// recoverTaskStepOccupancy 收敛单条任务上的步骤占用残留。
func (s *TaskLaunchService) recoverTaskStepOccupancy(
	ctx context.Context,
	taskID domain.ID,
) error {
	steps, err := s.store.ListStepsByTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("恢复任务 %s 的步骤占用失败: %w", taskID, err)
	}
	active, err := s.taskHasActiveRun(ctx, taskID)
	if err != nil {
		return fmt.Errorf("恢复任务 %s 的步骤占用失败: %w", taskID, err)
	}
	for _, step := range steps {
		needed, next := domain.DecideStepOccupancyRecovery(domain.TaskLaunchStepOccupancyInput{
			Step:              step,
			ActiveWorkflowRun: active,
		})
		if !needed {
			continue
		}
		if err := s.recoverStepOccupancy(ctx, step, next); err != nil {
			return fmt.Errorf(
				"恢复任务 %s 的步骤 %s 占用失败: %w",
				taskID,
				step.ID,
				err,
			)
		}
	}
	return nil
}

// taskHasActiveRun 判断任务是否仍有未结束的工作流执行记录。
func (s *TaskLaunchService) taskHasActiveRun(
	ctx context.Context,
	taskID domain.ID,
) (bool, error) {
	_, err := s.store.GetActiveWorkflowRun(ctx, taskID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	return false, err
}

// recoverStepOccupancy 把一个被占用的步骤收敛为需要人工核验。
//
// 写入带状态前置条件：状态已被其他进程推进时按"同一次收敛已完成"处理，不重复写入
// 事件，也不把本地对象覆盖到数据库。
func (s *TaskLaunchService) recoverStepOccupancy(
	ctx context.Context,
	step domain.Step,
	next domain.StepStatus,
) error {
	previous := step.Status
	if err := step.Transition(next, s.now()); err != nil {
		return err
	}
	step.FailureSummary = domain.StepOccupancyRecoverySummary()
	if err := step.SetCurrentAttempt(nil); err != nil {
		return err
	}
	if err := s.store.UpdateStepIfStatus(ctx, step, []domain.StepStatus{previous}); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 另一个进程已经推进了该步骤：不是失败，也不重复写入事件。
			return nil
		}
		return err
	}
	s.appendEvent(ctx, step.TaskID, domain.EventTypeTaskRecoveryRequired, map[string]any{
		"stage":         "recover_step_occupancy",
		"step_id":       step.ID.String(),
		"sequence":      step.Sequence,
		"previous":      string(previous),
		"status":        string(next),
		"summary":       step.FailureSummary,
		"recovery_auto": true,
	})
	return nil
}
