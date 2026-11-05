package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 本文件实现任务执行权在应用层的统一表达。
//
// 服务端只允许一个进程使用同一份数据目录，因此"谁在执行这个任务"由进程内的任务协调器
// 登记：一次工作流推进必须持有该任务的执行凭据，凭据在进程内释放之前一直有效。任务执行
// 凭据是唯一的所有权令牌，所有可能产生副作用的入口都必须先取得它。

// taskGrantFor 返回适配指定任务的执行凭据。
//
// 它用于把"调度器取得的执行权"传给启动与编排链路，同时防止把凭据用在别的任务上：
// 任务标识不匹配时返回错误而不是静默改写。
func taskGrantFor(grant TaskExecutionGrant, taskID domain.ID) (TaskExecutionGrant, error) {
	if !grant.Valid() {
		return TaskExecutionGrant{}, fmt.Errorf(
			"%w: 本次调用没有任务执行凭据",
			ErrWorkflowPrerequisite,
		)
	}
	if grant.TaskID() != taskID {
		return TaskExecutionGrant{}, fmt.Errorf(
			"%w: 执行权属于任务 %s，不能用于任务 %s",
			ErrWorkflowPrerequisite,
			grant.TaskID(),
			taskID,
		)
	}
	return grant, nil
}

// checkTaskExecution 核验任务执行权在执行权交接时是否仍然有效。
//
// 规则复用 domain.DecideTaskExecutionHandover：它同时校验任务状态、取消意图与已有执行
// 记录，本方法只把结论翻译成应用层错误。
//
// 它用于"建立或恢复工作流执行"这类要求任务处于可推进步骤状态的入口；计划生成与统一
// 启动可以从 queued 开始，因此使用 checkTaskOwnership。
func (s *TaskWorkflowService) checkTaskExecution(
	ctx context.Context,
	task domain.Task,
	grant TaskExecutionGrant,
) error {
	if _, err := taskGrantFor(grant, task.ID); err != nil {
		return err
	}
	if !grant.Held() {
		return fmt.Errorf(
			"%w: 任务 %s 的执行权已经释放",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	var active *domain.WorkflowRun
	run, err := s.store.GetActiveWorkflowRun(ctx, task.ID)
	if err == nil {
		active = run
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf(
			"%w: 读取任务 %s 的未结束执行记录失败: %w",
			ErrWorkflowPrerequisite,
			task.ID,
			err,
		)
	}
	handover := domain.DecideTaskExecutionHandover(task, active)
	if !handover.Allowed {
		if handover.Reason == domain.TaskQueueRejectionActiveWorkflowRun {
			return fmt.Errorf(
				"%w: %s: %s",
				ErrWorkflowConflict,
				handover.Reason,
				handover.Detail,
			)
		}
		return fmt.Errorf("%w: %s: %s", ErrWorkflowPrerequisite, handover.Reason, handover.Detail)
	}
	return nil
}

// writeTask 写入任务状态。
//
// grant 非 nil 时先核验执行权仍在本次调用手中：执行权已经释放说明本次推进不再是任务的
// 当前执行者，迟到的写入必须被拒绝，而不是覆盖后来者的记录。grant 为 nil 表示这次写入
// 来自没有执行者的恢复流程（例如启动恢复按持久化证据补齐终态），此时只能用状态条件写入，
// 并且调用方必须已经确认没有其他执行者在推进。
func (s *TaskWorkflowService) writeTask(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	grant *TaskExecutionGrant,
) error {
	if err := checkGrantHeld(task.ID, grant); err != nil {
		return err
	}
	return s.store.UpdateTaskIfStatus(ctx, task, expected)
}

// acquireGrant 返回本次操作使用的执行凭据。
//
// 调用方未提供凭据时由服务自行取得：执行权仍是唯一所有权令牌，只是取得者变成服务本身。
// 调用方提供了凭据时不做任何取得，避免一次操作产生两个执行登记。
//
// 返回的 release 只在凭据由本方法取得时才真正释放；由调用方提供时为空操作——释放责任
// 属于凭据持有者。
func (s *TaskWorkflowService) acquireGrant(
	ctx context.Context,
	task domain.Task,
	grant TaskExecutionGrant,
) (TaskExecutionGrant, func(), error) {
	if err := ctx.Err(); err != nil {
		return TaskExecutionGrant{}, nil, fmt.Errorf("%w: %w", ErrWorkflowStopped, err)
	}
	if grant.Valid() {
		adopted, err := taskGrantFor(grant, task.ID)
		if err != nil {
			return TaskExecutionGrant{}, nil, err
		}
		return adopted, func() {}, nil
	}
	acquired, ok := s.coordination.TryAcquire(task.ID)
	if !ok {
		return TaskExecutionGrant{}, nil, fmt.Errorf(
			"%w: 任务 %s 正被另一个执行入口推进",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	return acquired, func() { s.coordination.Release(acquired) }, nil
}

// checkTaskOwnership 核验任务执行权，但不要求任务处于可推进步骤的状态。
//
// 计划生成与统一启动可以从 queued 开始：它们的前置条件由 CheckPlanGenerationStart /
// CheckTaskLaunchStart 判定，本方法只回答"本次调用是否仍持有该任务的执行权"。任务被
// 删除、已请求取消或执行权已释放时一律拒绝，因为这些情况下推进会产生副作用却没有合法
// 所有者。
func (s *TaskWorkflowService) checkTaskOwnership(
	task domain.Task,
	grant TaskExecutionGrant,
) error {
	if _, err := taskGrantFor(grant, task.ID); err != nil {
		return err
	}
	if task.Deleted() {
		return fmt.Errorf(
			"%w: 任务 %s 已被删除",
			ErrWorkflowPrerequisite,
			task.ID,
		)
	}
	if task.CancelRequested {
		return fmt.Errorf(
			"%w: 任务 %s 已请求取消，不能继续推进",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	if !grant.Held() {
		return fmt.Errorf(
			"%w: 任务 %s 的执行权已经释放",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	return nil
}

// checkGrantHeld 核验执行权仍在本次调用手中。
//
// grant 为空表示本次写入没有执行者；非空时执行权必须仍然有效，否则返回失去执行权的
// 错误，调用方必须停止推进而不是继续写入。
func checkGrantHeld(taskID domain.ID, grant *TaskExecutionGrant) error {
	if grant == nil {
		return nil
	}
	if !grant.Valid() {
		return fmt.Errorf(
			"%w: 本次调用没有任务执行凭据",
			ErrWorkflowConflict,
		)
	}
	if grant.TaskID() != taskID {
		return fmt.Errorf(
			"%w: 执行权属于任务 %s，与目标任务 %s 不一致",
			ErrWorkflowConflict,
			grant.TaskID(),
			taskID,
		)
	}
	if !grant.Held() {
		return fmt.Errorf(
			"%w: 任务 %s 的执行权已经释放，拒绝迟到写入",
			errWorkflowLeaseLost,
			taskID,
		)
	}
	return nil
}

// updateTaskGuarded 写入任务状态，并在持有执行权时先核验执行权仍然有效。
//
// grant 为 nil 时退化为状态条件写入；非空时迟到的旧执行写入会被拒绝。
func updateTaskGuarded(
	ctx context.Context,
	store interface {
		UpdateTaskIfStatus(ctx context.Context, task domain.Task, expected []domain.TaskStatus) error
	},
	task domain.Task,
	expected []domain.TaskStatus,
	grant *TaskExecutionGrant,
) error {
	if err := checkGrantHeld(task.ID, grant); err != nil {
		return err
	}
	return store.UpdateTaskIfStatus(ctx, task, expected)
}
