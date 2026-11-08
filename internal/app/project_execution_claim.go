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

// 本文件实现项目执行权对自动调度的约束，以及执行权的续租与收敛。
//
// 分工固定：
//   - 调度器负责全局并发名额、任务执行权与统一启动；
//   - 本闸门只回答"这个任务现在有没有资格被自动领取"，不参与启动；
//   - 两个入口都使用 domain.DecideProjectExecutionClaim / DecideProjectExecutionPlan，
//     不存在第二份顺序或阻塞判定。

// TaskSchedulerClaimCandidate 是调度器交给项目闸门的候选事实。
type TaskSchedulerClaimCandidate struct {
	// Task 是候选任务。
	Task domain.Task
	// Requirement 是候选任务所属需求，读取失败时为空（此时无法判定项目归属）。
	Requirement *domain.Requirement
}

// ProjectClaimRejection 是一次被拒绝的项目级领取判定。
type ProjectClaimRejection struct {
	// TaskID 是被拒绝的任务标识。
	TaskID domain.ID
	// Reason 是拒绝原因的稳定取值。
	Reason string
	// Detail 是可读说明。
	Detail string
}

// TaskSchedulerProjectGate 是调度器对项目级执行约束的需求。
//
// 它不实现启动流程，只过滤候选并收敛执行权；调度器在扫描开始前调用一次 Reconcile，
// 在分派之前调用一次 FilterClaims。
type TaskSchedulerProjectGate interface {
	// FilterClaims 过滤候选任务，返回允许自动领取的候选与拒绝原因。
	FilterClaims(
		ctx context.Context,
		candidates []TaskSchedulerClaimCandidate,
	) ([]TaskSchedulerClaimCandidate, []ProjectClaimRejection)
	// Reconcile 收敛本进程持有的项目执行权。
	//
	// 任务真实收敛后释放执行权；在途任务尚未收敛时保留执行权。
	Reconcile(ctx context.Context) error
}

// projectGateFacts 是单个项目的闸门判定事实。
type projectGateFacts struct {
	plan  domain.ProjectExecutionPlan
	lease *domain.ProjectExecutionLease
	state *storage.ProjectExecutionState
	err   error
}

// FilterClaims 过滤候选任务，只保留项目执行权允许的唯一队首任务。
//
// 非项目任务（所属需求没有项目关联）不进入项目闸门：它们是历史手工创建的任务，没有
// 项目执行权可以约束，因此保持既有自动调度行为。
//
// 读取失败时只拒绝该项目自己的候选并记录日志：一个项目的存储错误不得让其他项目停止
// 执行，也不得被当作"可调度"。
func (s *ProjectExecutionService) FilterClaims(
	ctx context.Context,
	candidates []TaskSchedulerClaimCandidate,
) ([]TaskSchedulerClaimCandidate, []ProjectClaimRejection) {
	allowed := make([]TaskSchedulerClaimCandidate, 0, len(candidates))
	rejections := make([]ProjectClaimRejection, 0)
	if len(candidates) == 0 {
		return allowed, rejections
	}
	now := s.now()
	cache := make(map[domain.ID]*projectGateFacts, 4)
	for _, candidate := range candidates {
		projectID := requirementProjectID(candidate.Requirement)
		if projectID == "" {
			allowed = append(allowed, candidate)
			continue
		}
		facts, cached := cache[projectID]
		if !cached {
			facts = s.loadGateFacts(ctx, projectID, now)
			cache[projectID] = facts
		}
		if facts.err != nil {
			rejections = append(rejections, ProjectClaimRejection{
				TaskID: candidate.Task.ID,
				Reason: projectClaimReasonUnreadable,
				Detail: facts.err.Error(),
			})
			continue
		}
		decision := domain.DecideProjectExecutionClaim(domain.ProjectExecutionTaskCheckInput{
			TaskID:        candidate.Task.ID,
			Plan:          facts.plan,
			Lease:         facts.lease,
			Owner:         s.owner,
			Now:           now,
			StopRequested: facts.state != nil && facts.state.StopRequested(),
		})
		if decision.Allowed {
			allowed = append(allowed, candidate)
			continue
		}
		rejections = append(rejections, ProjectClaimRejection{
			TaskID: candidate.Task.ID,
			Reason: decision.Reason,
			Detail: decision.Detail,
		})
	}
	return allowed, rejections
}

// projectClaimReasonUnreadable 表示项目事实读取失败，无法判定领取资格。
const projectClaimReasonUnreadable = "project_execution_unreadable"

// loadGateFacts 读取单个项目的闸门判定事实。
func (s *ProjectExecutionService) loadGateFacts(
	ctx context.Context,
	projectID domain.ID,
	now time.Time,
) *projectGateFacts {
	facts, err := s.store.ListProjectTaskFacts(ctx, projectID)
	if err != nil {
		return &projectGateFacts{err: fmt.Errorf("读取项目 %s 的任务失败: %w", projectID, err)}
	}
	lease, err := s.loadLease(ctx, projectID)
	if err != nil {
		return &projectGateFacts{err: err}
	}
	state, err := s.loadExecutionState(ctx, projectID)
	if err != nil {
		return &projectGateFacts{err: err}
	}
	return &projectGateFacts{
		plan:  domain.DecideProjectExecutionPlan(domainFacts(facts)),
		lease: lease,
		state: state,
	}
}

// Reconcile 收敛本进程持有的项目执行权。
//
// 规则固定为：
//   - 在途任务尚未真实收敛 → 保留执行权，继续等待（期间不领取后续任务）；
//   - 用户已请求停止且没有在途任务 → 释放执行权，意图记为 paused；
//   - 队首任务需要人工处置 → 释放执行权，意图记为 blocked；
//   - 项目任务全部真实完成，或项目已下线 → 释放执行权，意图记为 idle；
//   - 仍有可领取任务 → 保留执行权，等待调度器领取。
//
// 已过期的执行权不做任何处理：它不再属于本进程，重新领取只能由用户再次开始，或由新的
// 开始请求按 epoch 递增取得；收敛流程不会因此重放任何远程调用。
func (s *ProjectExecutionService) Reconcile(ctx context.Context) error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	leases, err := s.store.ListProjectExecutionLeases(ctx, s.owner)
	if err != nil {
		return fmt.Errorf("查询本进程的项目执行权失败: %w", err)
	}
	if len(leases) == 0 {
		return nil
	}
	now := s.now()
	failures := make([]error, 0, len(leases))
	for _, lease := range leases {
		if !lease.ActiveAt(now) {
			// 执行权已过期：不再领取任务，也不在这里重新取得。
			continue
		}
		if err := s.reconcileLease(ctx, lease, now); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// reconcileLease 收敛单个项目的执行权。
func (s *ProjectExecutionService) reconcileLease(
	ctx context.Context,
	lease domain.ProjectExecutionLease,
	now time.Time,
) error {
	projectID := lease.ProjectID
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// 项目已下线：只释放执行权，不写执行意图，也不改动任何任务。
			return s.releaseLease(ctx, projectID, &lease, now)
		}
		return fmt.Errorf("读取项目 %s 失败: %w", projectID, err)
	}
	facts, err := s.store.ListProjectTaskFacts(ctx, projectID)
	if err != nil {
		return fmt.Errorf("读取项目 %s 的任务失败: %w", projectID, err)
	}
	state, err := s.loadExecutionState(ctx, projectID)
	if err != nil {
		return err
	}
	plan := domain.DecideProjectExecutionPlan(domainFacts(facts))
	if plan.Counts.Running > 0 {
		// 在途任务仍在推进：执行权必须保留到它真实收敛。
		return nil
	}
	switch {
	case state != nil && state.StopRequested():
		return s.finishLease(
			ctx,
			projectID,
			lease,
			now,
			domain.ProjectExecutionStatePaused,
			state.StopRequestedAt,
		)
	case plan.Blocked:
		return s.finishLease(ctx, projectID, lease, now, domain.ProjectExecutionStateBlocked, nil)
	case plan.HeadState == domain.ProjectTaskHeadCompleted:
		return s.finishLease(ctx, projectID, lease, now, domain.ProjectExecutionStateIdle, nil)
	default:
		// 仍有可领取任务：保留执行权，等待调度器在下一次扫描中领取。
		return nil
	}
}

// finishLease 释放执行权并写入执行意图。
func (s *ProjectExecutionService) finishLease(
	ctx context.Context,
	projectID domain.ID,
	lease domain.ProjectExecutionLease,
	now time.Time,
	state domain.ProjectExecutionState,
	stopRequestedAt *time.Time,
) error {
	if err := s.releaseLease(ctx, projectID, &lease, now); err != nil {
		return err
	}
	recorded := storage.ProjectExecutionState{
		ProjectID:       projectID,
		State:           state,
		StopRequestedAt: stopRequestedAt,
		UpdatedAt:       now,
	}
	if err := s.store.SetProjectExecutionState(ctx, recorded); err != nil {
		return fmt.Errorf("写入项目执行意图失败: %w", err)
	}
	s.logger.InfoContext(
		ctx,
		"已释放项目执行权",
		slog.String("project_id", projectID.String()),
		slog.Int64("epoch", lease.Epoch),
		slog.String("state", string(state)),
	)
	return nil
}

// releaseLease 以 owner + epoch 为条件释放执行权。
//
// 条件不成立（执行权已换人或已过期）说明本次调用不再是该项目执行权的持有者：只记录
// 事实，不覆盖新持有者的登记，也不把它当成需要上报的失败。
func (s *ProjectExecutionService) releaseLease(
	ctx context.Context,
	projectID domain.ID,
	lease *domain.ProjectExecutionLease,
	now time.Time,
) error {
	if lease == nil || lease.ProjectID.IsEmpty() {
		return nil
	}
	if err := domain.DecideProjectLeaseRelease(lease, s.owner, lease.Epoch); err != nil {
		s.logger.WarnContext(
			ctx,
			"项目执行权已不属于本进程，跳过释放",
			slog.String("project_id", projectID.String()),
			slog.String("error", errorSummary(err)),
		)
		return nil
	}
	if err := s.store.ReleaseProjectExecutionLease(
		ctx,
		projectID,
		s.owner,
		lease.Epoch,
		now,
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			s.logger.WarnContext(
				ctx,
				"项目执行权已被其他执行者取代，跳过释放",
				slog.String("project_id", projectID.String()),
				slog.Int64("epoch", lease.Epoch),
			)
			return nil
		}
		return fmt.Errorf("释放项目 %s 的执行权失败: %w", projectID, err)
	}
	return nil
}

// RenewLeases 续租本进程持有的全部项目执行权。
//
// 续租失败只停止该项目的后续领取（调度器闸门会按执行权事实拒绝候选），不影响其他项目，
// 也不会重放任何无法确认是否已经发生的远程调用。
func (s *ProjectExecutionService) RenewLeases(ctx context.Context) error {
	leases, err := s.store.ListProjectExecutionLeases(ctx, s.owner)
	if err != nil {
		return fmt.Errorf("查询本进程的项目执行权失败: %w", err)
	}
	if len(leases) == 0 {
		return nil
	}
	now := s.now()
	expiresAt := now.Add(s.ttl)
	failures := make([]error, 0)
	for _, lease := range leases {
		if err := domain.DecideProjectLeaseRenew(&lease, s.owner, lease.Epoch, now, expiresAt); err != nil {
			s.logger.WarnContext(
				ctx,
				"项目执行权无法续租，停止领取该项目的新任务",
				slog.String("project_id", lease.ProjectID.String()),
				slog.Int64("epoch", lease.Epoch),
				slog.String("error", errorSummary(err)),
			)
			continue
		}
		renewErr := s.store.RenewProjectExecutionLease(
			ctx,
			lease.ProjectID,
			s.owner,
			lease.Epoch,
			now,
			expiresAt,
		)
		if renewErr != nil {
			s.logger.WarnContext(
				ctx,
				"项目执行权续租失败，停止领取该项目的新任务",
				slog.String("project_id", lease.ProjectID.String()),
				slog.Int64("epoch", lease.Epoch),
				slog.String("error", errorSummary(renewErr)),
			)
			failures = append(failures, renewErr)
		}
	}
	return errors.Join(failures...)
}

// ReleaseOnStop 在服务正常停止时释放没有在途任务的项目执行权。
//
// 仍有在途任务的项目保留执行权：停止过程中无法证明任务已经收敛，提前释放会让另一个
// 执行者在现场不明时重新领取。保留的执行权按 TTL 到期，重启后也不会因此重放任务。
func (s *ProjectExecutionService) ReleaseOnStop(ctx context.Context) error {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	leases, err := s.store.ListProjectExecutionLeases(ctx, s.owner)
	if err != nil {
		return fmt.Errorf("查询本进程的项目执行权失败: %w", err)
	}
	now := s.now()
	failures := make([]error, 0, len(leases))
	for _, lease := range leases {
		if !lease.ActiveAt(now) {
			continue
		}
		facts, err := s.store.ListProjectTaskFacts(ctx, lease.ProjectID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		plan := domain.DecideProjectExecutionPlan(domainFacts(facts))
		if plan.Counts.Running > 0 {
			s.logger.InfoContext(
				ctx,
				"项目仍有在途任务，保留执行权直至到期",
				slog.String("project_id", lease.ProjectID.String()),
				slog.Int64("epoch", lease.Epoch),
			)
			continue
		}
		if err := s.releaseLease(ctx, lease.ProjectID, &lease, now); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// requirementProjectID 返回候选任务所属项目标识。
func requirementProjectID(requirement *domain.Requirement) domain.ID {
	if requirement == nil {
		return ""
	}
	return domain.ID(strings.TrimSpace(requirement.ProjectID))
}
