package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// GetValidationFailureFeedback 返回指定验证运行的失败反馈。
//
// 归属链必须完整成立：任务未被逻辑删除、步骤属于该任务、验证记录属于该任务与该
// 步骤、反馈确实由该验证记录产生。只凭 runID 读取会让调用方用 A 任务的路径拿到
// B 任务的反馈，因此这里逐层校验，任一不成立都返回 ErrExecutionRecordNotFound。
//
// 反馈缺失不等于验证通过：没有反馈可能是验证通过、可能是历史数据未写入，调用方
// 必须读取验证记录本身的 verdict 才能判断结论。
func (s *ExecutionRecordService) GetValidationFailureFeedback(
	ctx context.Context,
	taskID, stepID, runID string,
) (domain.ValidationFailureFeedback, error) {
	run, err := s.requireValidationRun(ctx, taskID, stepID, runID)
	if err != nil {
		return domain.ValidationFailureFeedback{}, err
	}
	feedback, err := s.store.GetValidationFailureFeedback(ctx, run.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.ValidationFailureFeedback{}, fmt.Errorf(
				"%w: 验证记录 %s 没有失败反馈",
				ErrExecutionRecordNotFound,
				run.ID.String(),
			)
		}
		return domain.ValidationFailureFeedback{}, fmt.Errorf("读取验证失败反馈失败: %w", err)
	}
	// 存储层按 runID 唯一读取，仍然校验归属：错配的记录不得被当作合法反馈返回。
	if feedback.ValidationRunID != run.ID ||
		feedback.StepID != run.StepID ||
		feedback.TaskID != run.TaskID {
		return domain.ValidationFailureFeedback{}, fmt.Errorf(
			"读取验证失败反馈失败: 反馈 %s 与验证记录 %s 的归属不一致",
			feedback.ValidationRunID,
			run.ID,
		)
	}
	return *feedback, nil
}

// requireValidationRun 读取验证记录并确认它属于指定任务与步骤。
//
// 三层归属必须同时成立：验证记录独立存在并不代表它属于路径上的任务与步骤。
func (s *ExecutionRecordService) requireValidationRun(
	ctx context.Context,
	taskID, stepID, runID string,
) (domain.ValidationRun, error) {
	step, err := s.requireStep(ctx, taskID, stepID)
	if err != nil {
		return domain.ValidationRun{}, err
	}
	parsed, err := parseRecordID(runID)
	if err != nil {
		return domain.ValidationRun{}, err
	}
	run, err := s.store.GetValidationRun(ctx, parsed)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.ValidationRun{}, fmt.Errorf(
				"%w: 验证记录 %s",
				ErrExecutionRecordNotFound,
				parsed.String(),
			)
		}
		return domain.ValidationRun{}, fmt.Errorf("读取验证记录失败: %w", err)
	}
	if run.StepID != step.ID || run.TaskID != step.TaskID {
		return domain.ValidationRun{}, fmt.Errorf(
			"%w: 验证记录 %s",
			ErrExecutionRecordNotFound,
			parsed.String(),
		)
	}
	return *run, nil
}
