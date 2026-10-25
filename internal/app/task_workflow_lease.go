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

// 本文件实现任务租约与工作流执行权的统一。
//
// P3-M1 的契约是：**任务租约是唯一所有权令牌，WorkflowRun 的执行租约是它在执行
// 阶段的续期视图**。因此一次工作流推进必须同时满足两个条件：
//
//  1. 当前进程仍持有该任务的有效任务租约（owner + epoch 匹配且未过期）；
//  2. WorkflowRun 的 WorkerID 与任务租约的 owner 一致。
//
// 第 2 条通过"工作流执行者标识取自任务租约 owner"实现：StartWorkflow 与
// ResumeWorkflow 不再各自生成 owner，而是要求调用方给出一个**内部执行权**，
// 该执行权只能由任务领取产生。

// WorkflowExecutionGrant 是一次任务执行权的内部凭据。
//
// 它由任务领取（TaskLeaseService.Claim）产生，只在应用层内部传递：HTTP 与 CLI
// 接口不得接受调用方构造的 owner / epoch，因此本类型不参与任何请求结构体解析。
type WorkflowExecutionGrant struct {
	// Lease 是数据库确认过的任务租约身份。
	Lease domain.TaskLeaseHandle
	// TTL 是租约有效期，用于工作流心跳续租任务租约。
	TTL time.Duration
}

// Validate 校验执行权凭据是否完整。
func (g WorkflowExecutionGrant) Validate() error {
	if err := g.Lease.Validate(); err != nil {
		return err
	}
	if g.TTL <= 0 {
		return fmt.Errorf("%w: 任务租约有效期必须大于 0", ErrWorkflowPrerequisite)
	}
	return nil
}

// GrantFor 返回适配指定任务标识的执行权凭据。
//
// 它用于把"调度器领取到的执行权"传给启动与编排链路，同时防止把凭据用在别的
// 任务上：任务标识不匹配时返回错误而不是静默改写。
func (g WorkflowExecutionGrant) GrantFor(taskID domain.ID) (WorkflowExecutionGrant, error) {
	if err := g.Validate(); err != nil {
		return WorkflowExecutionGrant{}, err
	}
	if g.Lease.TaskID != taskID {
		return WorkflowExecutionGrant{}, fmt.Errorf(
			"%w: 执行权属于任务 %s，不能用于任务 %s",
			ErrWorkflowPrerequisite,
			g.Lease.TaskID,
			taskID,
		)
	}
	return g, nil
}

// workflowOwner 返回工作流执行者标识。
//
// 它与任务租约 owner 完全相同：这是"两套所有权合并为一套"的落点。任何新的
// 工作流执行记录都因此天然绑定到某一次任务领取。
func (g WorkflowExecutionGrant) workflowOwner() string {
	return strings.TrimSpace(g.Lease.Owner)
}

// checkTaskLease 核验任务执行权在执行权交接时是否仍然有效。
//
// 规则复用 domain.DecideTaskExecutionHandover：它同时校验任务状态、取消意图、
// 任务租约与已有执行记录，本方法只把结论翻译成应用层错误。
//
// 它用于"建立或恢复工作流执行"这类要求任务处于可推进步骤状态的入口；计划生成与
// 统一启动可以从 queued 开始，因此使用 checkTaskOwnership。
func (s *TaskWorkflowService) checkTaskLease(
	ctx context.Context,
	task domain.Task,
	grant WorkflowExecutionGrant,
) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	if grant.Lease.TaskID != task.ID {
		return fmt.Errorf(
			"%w: 执行权属于任务 %s，与目标任务 %s 不一致",
			ErrWorkflowPrerequisite,
			grant.Lease.TaskID,
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
	handover := domain.DecideTaskExecutionHandover(task, active, grant.workflowOwner(), s.now())
	if !handover.Allowed {
		// 租约被他人持有属于冲突，其余拒绝原因属于前置条件缺失。
		if handover.Reason == domain.TaskQueueRejectionLeaseHeld {
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

// renewTaskLease 以 owner + epoch 为条件延长任务租约。
//
// 续租失败说明本次执行者已经失去任务执行权：调用方必须立即停止推进，不得继续
// 执行副作用，也不得继续写入状态。
func (s *TaskWorkflowService) renewTaskLease(
	ctx context.Context,
	grant WorkflowExecutionGrant,
) error {
	err := s.store.RenewTaskLease(ctx, storage.TaskLeaseRenewRequest{
		Handle: grant.Lease,
		TTL:    grant.TTL,
		At:     s.now(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", errWorkflowLeaseLost, err)
	}
	return nil
}

// holdTaskLease 判断给定任务记录是否仍由该执行权持有。
//
// 它只读传入的记录，不读数据库：调用方必须传入重新读取的最新记录。
func (s *TaskWorkflowService) holdTaskLease(
	task domain.Task,
	grant WorkflowExecutionGrant,
) bool {
	return domain.TaskHoldsLease(task, grant.Lease, s.now())
}

// releaseTaskLease 释放任务租约并记录真实结果。
//
// 释放失败不掩盖错误：调用方必须知道"名额没有被空出"，否则调度器会以为可以立刻
// 领取新任务而实际名额仍然被占。释放冲突（租约已被接管）只记录日志，因为它说明
// 本次执行者已经不再拥有该任务，没有可释放的所有权。
func (s *TaskWorkflowService) releaseTaskLease(
	ctx context.Context,
	grant WorkflowExecutionGrant,
	reason string,
) error {
	_, err := s.store.ReleaseTaskLease(ctx, grant.Lease, s.now())
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			s.logger.WarnContext(
				ctx,
				"任务租约已不属于本次执行者，跳过释放",
				slog.String("task_id", grant.Lease.TaskID.String()),
				slog.String("owner", grant.Lease.Owner),
				slog.Int64("epoch", grant.Lease.Epoch),
				slog.String("reason", reason),
				slog.String("error", errorSummary(err)),
			)
			return nil
		}
		return fmt.Errorf("释放任务租约失败: %w", err)
	}
	s.appendEvent(ctx, grant.Lease.TaskID, nil, nil, domain.EventTypeTaskReleased, map[string]any{
		"owner":        grant.Lease.Owner,
		"epoch":        grant.Lease.Epoch,
		"reason":       reason,
		"released_at":  s.now().Format(time.RFC3339Nano),
		"release_kind": "workflow",
	})
	return nil
}

// workflowLeaseLostError 判断错误是否表示执行权丢失。
func workflowLeaseLostError(err error) bool {
	return errors.Is(err, errWorkflowLeaseLost) || errors.Is(err, ErrTaskLeaseLost)
}

// workflowGrantKey 返回用于在途执行登记的键。
//
// 同一任务同一时刻最多一条在途执行，因此键只需要任务标识；但释放逻辑必须比对
// 代次，避免旧执行的释放动作误伤新领取。
func workflowGrantKey(grant WorkflowExecutionGrant) string {
	return grant.Lease.TaskID.String()
}

// writeTask 写入任务状态，并在持有执行权时附带租约条件。
//
// grant 为 nil 表示这次写入来自没有执行者的恢复流程（例如启动恢复按持久化证据补齐
// 终态）：此时只能用状态条件写入，并且调用方必须已经确认没有其他执行者在推进。
// grant 非 nil 时写入附带 owner + epoch 条件，旧代次的迟到写入因此被拒绝。
func (s *TaskWorkflowService) writeTask(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	grant *WorkflowExecutionGrant,
) error {
	if grant == nil {
		return s.store.UpdateTaskIfStatus(ctx, task, expected)
	}
	return s.store.UpdateTaskLeaseGuard(ctx, task, expected, grant.Lease)
}

// ensureGrant 返回本次操作使用的执行权凭据。
//
// 调用方未提供凭据时由服务自行领取：任务租约仍是唯一所有权令牌，只是领取者变成
// 服务本身，而不是另立一套与调度无关的 owner。调用方提供了凭据时不做任何领取，
// 避免一次操作产生两个 owner。
//
// 返回的 release 在凭据由本方法领取时释放租约；由调用方提供时为空操作——释放责任
// 属于凭据持有者。
func (s *TaskWorkflowService) ensureGrant(
	ctx context.Context,
	task domain.Task,
	grant WorkflowExecutionGrant,
	statuses []domain.TaskStatus,
) (WorkflowExecutionGrant, domain.Task, func(), error) {
	if grant.Lease.TaskID != "" {
		return grant, task, func() {}, nil
	}
	handle, err := s.store.AcquireTaskLease(ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "workflow-" + domain.NewID().String(),
		TTL:      s.lease,
		Statuses: statuses,
		At:       s.now(),
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return WorkflowExecutionGrant{}, task, nil, fmt.Errorf(
				"%w: 任务 %s 已被其他执行者持有",
				ErrWorkflowConflict,
				task.ID,
			)
		}
		return WorkflowExecutionGrant{}, task, nil, fmt.Errorf("领取任务租约失败: %w", err)
	}
	// 重新读取任务：租约刚由本方法写入，调用方手上的快照还没有 owner / epoch，
	// 用它做执行权判定会误判为"不持有"。
	refreshed, loadErr := s.loadTask(ctx, task.ID)
	if loadErr != nil {
		_, releaseErr := s.store.ReleaseTaskLease(context.WithoutCancel(ctx), handle, s.now())
		if releaseErr != nil {
			s.logger.WarnContext(
				ctx,
				"读取任务失败后释放任务租约失败",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(releaseErr)),
			)
		}
		return WorkflowExecutionGrant{}, task, nil, loadErr
	}
	task = refreshed
	acquired := WorkflowExecutionGrant{Lease: handle, TTL: s.lease}
	release := func() {
		if releaseErr := s.releaseTaskLease(
			context.WithoutCancel(ctx),
			acquired,
			"service_self_claimed",
		); releaseErr != nil {
			s.logger.WarnContext(
				ctx,
				"释放服务自行领取的任务租约失败，名额将受租约有效期约束",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(releaseErr)),
			)
		}
	}
	return acquired, task, release, nil
}

// workflowClaimStatuses 返回服务自行领取工作流执行权时允许的任务状态集合。
//
// 它与显式工作流入口一致：只有允许推进步骤的状态才可能启动或恢复工作流。
func workflowClaimStatuses() []domain.TaskStatus {
	return domain.ExplicitWorkflowStatuses()
}

// planClaimStatuses 返回服务自行领取计划生成执行权时允许的任务状态集合。
func planClaimStatuses() []domain.TaskStatus {
	return domain.ExplicitPlanningStatuses()
}

// checkTaskOwnership 核验任务执行权，但不要求任务处于可推进步骤的状态。
//
// 计划生成与统一启动可以从 queued 开始：它们的前置条件由 CheckPlanGenerationStart /
// CheckTaskLaunchStart 判定，本方法只回答"本次执行者是否仍持有该任务的执行权"。
// 任务被删除、已请求取消或执行权已转移时一律拒绝，因为这些情况下推进会产生副作用
// 却没有合法所有者。
func (s *TaskWorkflowService) checkTaskOwnership(
	task domain.Task,
	grant WorkflowExecutionGrant,
) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	if grant.Lease.TaskID != task.ID {
		return fmt.Errorf(
			"%w: 执行权属于任务 %s，与目标任务 %s 不一致",
			ErrWorkflowPrerequisite,
			grant.Lease.TaskID,
			task.ID,
		)
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
	if !s.holdTaskLease(task, grant) {
		return fmt.Errorf(
			"%w: 任务 %s 的执行权不属于本次领取（owner %s，代次 %d）",
			ErrWorkflowConflict,
			task.ID,
			grant.Lease.Owner,
			grant.Lease.Epoch,
		)
	}
	return nil
}

// leaseOf 把执行权凭据转换为可选的租约身份。
//
// 凭据为空表示调用方没有任务执行权，此时只能使用状态条件写入；凭据非空时写入
// 附带 owner + epoch 条件，因此旧代次的执行者无法覆盖新执行者的状态。
func leaseOf(grant *WorkflowExecutionGrant) *domain.TaskLeaseHandle {
	if grant == nil {
		return nil
	}
	handle := grant.Lease
	return &handle
}

// holdsGrant 判断任务记录是否仍由该凭据持有执行权。
//
// grant 为 nil 时不做租约判定（调用方本就不持有执行权，写入使用状态条件）。
func holdsGrant(task domain.Task, grant *WorkflowExecutionGrant, at time.Time) bool {
	if grant == nil {
		return true
	}
	return domain.TaskHoldsLease(task, grant.Lease, at)
}

// updateTaskGuarded 写入任务状态，并在持有执行权时附带租约条件。
//
// grant 为 nil 时退化为状态条件写入；非空时使用 owner + epoch 栅栏，因此迟到的
// 旧代次写入会被数据库拒绝。
func updateTaskGuarded(
	ctx context.Context,
	store interface {
		UpdateTaskIfStatus(ctx context.Context, task domain.Task, expected []domain.TaskStatus) error
		UpdateTaskLeaseGuard(
			ctx context.Context,
			task domain.Task,
			expected []domain.TaskStatus,
			handle domain.TaskLeaseHandle,
		) error
	},
	task domain.Task,
	expected []domain.TaskStatus,
	grant *WorkflowExecutionGrant,
) error {
	if grant == nil {
		return store.UpdateTaskIfStatus(ctx, task, expected)
	}
	return store.UpdateTaskLeaseGuard(ctx, task, expected, grant.Lease)
}
