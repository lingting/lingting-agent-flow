package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// stepExecutionStore 实现 storage.StepExecutionStore。
//
// 它只提供"占用"这一条写入路径，执行结果本身仍由调用方通过
// UpdateAttempt / UpdateStep / UpdateTask 分别写入：存储层不解释执行结论。
type stepExecutionStore struct {
	q queryer
}

// BeginStepExecution 在一个事务内占用步骤、创建尝试并推进任务与步骤状态。
//
// 事务内的顺序固定为：读取任务与步骤 → 校验状态前置条件 → 分配尝试序号并写入
// 尝试 → 条件更新步骤 → 条件更新任务。任一步的状态前置条件不成立时整体回滚，
// 因此并发请求最多只有一个能成功创建 running 尝试。
func (s stepExecutionStore) BeginStepExecution(
	ctx context.Context,
	request storage.StepExecutionStartRequest,
) (storage.StepExecutionStart, error) {
	if err := request.TaskID.Validate(); err != nil {
		return storage.StepExecutionStart{}, err
	}
	if err := request.StepID.Validate(); err != nil {
		return storage.StepExecutionStart{}, err
	}
	if err := request.ExpectedTaskStatus.Validate(); err != nil {
		return storage.StepExecutionStart{}, err
	}
	if err := request.ExpectedStepStatus.Validate(); err != nil {
		return storage.StepExecutionStart{}, err
	}
	at := request.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	withTx, ok := s.q.(transactioner)
	if !ok {
		// 已在事务内：复用当前事务，原子性由调用方的事务边界保证。
		return s.begin(ctx, request, at)
	}
	var started storage.StepExecutionStart
	err := withTx.runInTx(ctx, func(tx queryer) error {
		inner := stepExecutionStore{q: tx}
		outcome, err := inner.begin(ctx, request, at)
		if err != nil {
			return err
		}
		started = outcome
		return nil
	})
	if err != nil {
		return storage.StepExecutionStart{}, err
	}
	return started, nil
}

// begin 在已开启的事务内执行占用。
func (s stepExecutionStore) begin(
	ctx context.Context,
	request storage.StepExecutionStartRequest,
	at time.Time,
) (storage.StepExecutionStart, error) {
	tasks := taskStore{q: s.q}
	steps := stepStore{q: s.q}
	attempts := attemptStore{q: s.q}

	task, err := tasks.GetTask(ctx, request.TaskID)
	if err != nil {
		return storage.StepExecutionStart{}, err
	}
	if task.Deleted() {
		return storage.StepExecutionStart{}, notFoundError("任务", request.TaskID.String())
	}
	if task.Status != request.ExpectedTaskStatus {
		return storage.StepExecutionStart{}, fmt.Errorf(
			"sqlite: 任务 %s 的状态已从 %s 变为 %s，拒绝占用步骤: %w",
			task.ID,
			request.ExpectedTaskStatus,
			task.Status,
			storage.ErrConflict,
		)
	}
	step, err := steps.GetStep(ctx, request.StepID)
	if err != nil {
		return storage.StepExecutionStart{}, err
	}
	if step.TaskID != task.ID {
		return storage.StepExecutionStart{}, wrapError(
			"占用步骤执行",
			errors.Join(
				storage.ErrInvalidData,
				fmt.Errorf("步骤 %s 不属于任务 %s", step.ID, task.ID),
			),
		)
	}
	if step.Status != request.ExpectedStepStatus {
		return storage.StepExecutionStart{}, fmt.Errorf(
			"sqlite: 步骤 %s 的状态已从 %s 变为 %s，拒绝占用: %w",
			step.ID,
			request.ExpectedStepStatus,
			step.Status,
			storage.ErrConflict,
		)
	}
	if step.CurrentAttemptID != nil {
		return storage.StepExecutionStart{}, fmt.Errorf(
			"sqlite: 步骤 %s 已被执行尝试 %s 占用: %w",
			step.ID,
			*step.CurrentAttemptID,
			storage.ErrConflict,
		)
	}
	attempt, err := attempts.assignAndInsert(ctx, storage.StartAttemptRequest{
		TaskID: task.ID,
		StepID: step.ID,
		At:     at,
	})
	if err != nil {
		return storage.StepExecutionStart{}, err
	}
	attempt.ContainerID = strings.TrimSpace(request.ContainerID)
	attempt.DockerID = strings.TrimSpace(request.DockerID)
	attempt.InputDigest = strings.TrimSpace(request.InputDigest)
	if err := attempts.UpdateAttempt(ctx, attempt); err != nil {
		return storage.StepExecutionStart{}, err
	}

	if err := step.Transition(domain.StepStatusRunning, at); err != nil {
		return storage.StepExecutionStart{}, wrapError("占用步骤执行", err)
	}
	if err := step.SetCurrentAttempt(&attempt.ID); err != nil {
		return storage.StepExecutionStart{}, err
	}
	if err := steps.UpdateStepIfStatus(
		ctx,
		*step,
		[]domain.StepStatus{request.ExpectedStepStatus},
	); err != nil {
		return storage.StepExecutionStart{}, err
	}

	if task.Status != domain.TaskStatusDeveloping {
		if err := task.Transition(domain.TaskStatusDeveloping, at); err != nil {
			return storage.StepExecutionStart{}, wrapError("推进任务状态", err)
		}
	}
	if err := task.SetCurrentStep(&step.ID); err != nil {
		return storage.StepExecutionStart{}, err
	}
	if err := task.SetCurrentAttempt(&attempt.ID); err != nil {
		return storage.StepExecutionStart{}, err
	}
	if err := tasks.UpdateTaskIfStatus(
		ctx,
		*task,
		[]domain.TaskStatus{request.ExpectedTaskStatus},
	); err != nil {
		return storage.StepExecutionStart{}, err
	}
	return storage.StepExecutionStart{Attempt: attempt, Task: *task, Step: *step}, nil
}
