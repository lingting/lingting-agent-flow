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

// 本文件实现"调度器重启前的任务租约残留核验"。
//
// 为什么需要它：任务租约是执行权的唯一令牌，但**租约过期只证明持有者没有续租，
// 不证明它发出的远程操作已经停止**。因此启动时不能把过期租约直接清空、也不能据此
// 把任务改回 queued 来"恢复吞吐量"：
//
//   - 清空租约会让调度器立刻在未知现场上重放 Clone / Pi 调用 / commit / push；
//   - 改回 queued 会把"结果不确定"伪装成"从未开始"。
//
// 本服务只做两件安全的事：
//  1. 把过期租约的失效事实落成可追溯的现场记录（任务事件 + 日志），供用户核验；
//  2. 让 `TaskScheduler` 在自动扫描前能看到这份现场，从而不领取需要人工处理的任务。
//
// 它不释放租约、不改写任务状态、不删除容器：租约仍按有效期自然失效，任务状态仍由
// TaskWorkflowService 的恢复入口按证据收敛。

// TaskLeaseRecoveryStore 是租约残留核验依赖的存储能力。
type TaskLeaseRecoveryStore interface {
	// ListTasks 按筛选条件读取任务，用于定位仍有租约记录的任务。
	ListTasks(ctx context.Context, filter storage.TaskFilter) ([]domain.Task, error)
	// GetActiveWorkflowRun 读取任务未结束的执行记录，没有时返回 storage.ErrNotFound。
	GetActiveWorkflowRun(ctx context.Context, taskID domain.ID) (*domain.WorkflowRun, error)
	// AppendEvent 追加任务事件。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// TaskLeaseRecoveryOptions 是构造租约残留核验服务的输入。
type TaskLeaseRecoveryOptions struct {
	// Store 是持久化入口，必填。
	Store TaskLeaseRecoveryStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// TaskLeaseRecoveryService 在服务启动时核验残留的任务租约现场。
//
// 职责边界（这是本服务刻意不做的事）：
//   - 不释放租约：释放会让名额立刻空出，而旧执行者的远程操作可能仍在进行；
//   - 不改写任务状态：`blocked` / `recovery_required` 任务不得仅因租约过期被重放；
//   - 不清理容器、不重放任何外部操作；
//   - 不把"租约过期"当作"执行已停止"的证据。
type TaskLeaseRecoveryService struct {
	store  TaskLeaseRecoveryStore
	logger *slog.Logger
	now    func() time.Time
}

// NewTaskLeaseRecoveryService 构造租约残留核验服务。
func NewTaskLeaseRecoveryService(
	options TaskLeaseRecoveryOptions,
) (*TaskLeaseRecoveryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("租约残留核验服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &TaskLeaseRecoveryService{store: options.Store, logger: logger, now: now}, nil
}

// Name 返回后台服务名称。
func (s *TaskLeaseRecoveryService) Name() string {
	return "task_lease_recovery"
}

// Start 核验残留租约并记录现场。
//
// 读取失败让启动失败：无法判断是否存在残留租约时继续启动，会让调度器在未知现场上
// 领取任务。事件写入失败只记录日志——它是诊断信息，不是状态迁移。
func (s *TaskLeaseRecoveryService) Start(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		ExcludeStatuses: []domain.TaskStatus{
			domain.TaskStatusCompleted,
			domain.TaskStatusCancelled,
		},
	})
	if err != nil {
		return fmt.Errorf("读取残留租约任务失败: %w", err)
	}
	now := s.now()
	for _, task := range tasks {
		if err := s.inspect(ctx, task, now); err != nil {
			return fmt.Errorf("核验任务 %s 的租约残留失败: %w", task.ID, err)
		}
	}
	return nil
}

// Stop 不做事：核验是启动时的一次性动作，没有长期 goroutine。
func (s *TaskLeaseRecoveryService) Stop(ctx context.Context) error {
	return nil
}

// inspect 核验单条任务的租约现场。
func (s *TaskLeaseRecoveryService) inspect(
	ctx context.Context,
	task domain.Task,
	now time.Time,
) error {
	owner := task.LeaseOwner
	if owner == "" {
		// 没有租约记录：没有需要核验的现场。
		return nil
	}
	active, err := s.activeRun(ctx, task.ID)
	if err != nil {
		return err
	}
	if active {
		// 仍有未结束的执行记录：执行者可能正在推进，租约是否过期都不由本服务判定。
		return nil
	}
	if task.LeaseActive(now) {
		// 租约仍有效：说明有执行者持有执行权，不能被抢占，也不产生残留结论。
		return nil
	}
	// 过期租约 + 没有未结束执行记录：这是"进程退出留下的残留"。
	// 结论必须明确说明"租约过期不等于远程操作已停止"。
	s.logger.WarnContext(
		ctx,
		"发现过期的任务租约残留，租约不自动释放，需要人工核验现场",
		slog.String("task_id", task.ID.String()),
		slog.String("task_status", string(task.Status)),
		slog.String("lease_owner", owner),
		slog.Int64("lease_epoch", task.LeaseEpoch),
	)
	event, eventErr := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalStepExecutionPayload(map[string]any{
			"stage":         "recover_lease",
			"task_status":   string(task.Status),
			"lease_owner":   owner,
			"lease_epoch":   task.LeaseEpoch,
			"expires_at":    formatLeaseExpiry(task.LeaseExpiresAt),
			"summary":       leaseResidueSummary(),
			"recovery_auto": false,
		}),
		now,
	)
	if eventErr != nil {
		s.logger.WarnContext(
			ctx,
			"构造租约残留事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(eventErr)),
		)
		return nil
	}
	if _, appendErr := s.store.AppendEvent(context.WithoutCancel(ctx), event); appendErr != nil {
		s.logger.WarnContext(
			ctx,
			"写入租约残留事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(appendErr)),
		)
	}
	return nil
}

// activeRun 判断任务是否存在未结束的工作流执行记录。
//
// 读取失败必须上抛：把"读不到记录"当作"没有执行者"会让残留结论建立在错误事实上。
func (s *TaskLeaseRecoveryService) activeRun(
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
	return false, fmt.Errorf("读取执行记录失败: %w", err)
}

// leaseResidueSummary 返回租约残留的固定说明。
//
// 说明必须同时表达三件事：这是自动发现的现场、租约过期不等于远程操作已停止、
// 后续动作需要人工核验。
func leaseResidueSummary() string {
	return "服务启动时发现过期的任务租约且没有未结束的执行记录：" +
		"租约过期只说明持有者没有续租，不能证明它发出的远程操作已经停止；" +
		"租约保留至自然失效，不自动释放、不改写任务状态，请先核验容器与 Git 现场"
}

// formatLeaseExpiry 把可空的租约到期时间渲染为可读字符串。
func formatLeaseExpiry(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}
