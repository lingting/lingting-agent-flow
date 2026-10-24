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

// 任务租约的应用层错误类别。
//
// 与存储层的 storage.ErrConflict 分开表达：上层据此区分"这一次没抢到"（正常竞争）
// 与"数据库不可用"（可重试的运行时故障），不解析错误文本。
var (
	// ErrTaskLeaseConflict 表示任务已被其他执行者持有，或本次领取已失效。
	ErrTaskLeaseConflict = errors.New("app: 任务执行权冲突")
	// ErrTaskLeaseInvalid 表示租约请求参数不合法。
	ErrTaskLeaseInvalid = errors.New("app: 任务租约请求非法")
	// ErrTaskLeaseLost 表示本次执行者已不再持有任务执行权。
	ErrTaskLeaseLost = errors.New("app: 任务执行权已丢失")
)

// 任务租约的默认参数。
const (
	// defaultTaskLeaseTTL 是任务租约的默认有效期。
	//
	// 它必须明显长于一次环境准备与计划生成，否则任务会在正常推进过程中被其他
	// 执行者接管；同时也不能过长，否则进程崩溃后残留的领取会长时间占住名额。
	defaultTaskLeaseTTL = 10 * time.Minute
	// minTaskLeaseRenewInterval 是续租间隔的下限，避免可注入的极短租约产生过密心跳。
	minTaskLeaseRenewInterval = 50 * time.Millisecond
)

// TaskLeaseStore 是任务租约服务依赖的存储能力。
//
// 它只包含租约与任务读写：租约服务不推进工作流、不创建容器、不调用 Agent。
type TaskLeaseStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	AcquireTaskLease(
		ctx context.Context,
		request storage.TaskLeaseRequest,
	) (domain.TaskLeaseHandle, error)
	RenewTaskLease(ctx context.Context, request storage.TaskLeaseRenewRequest) error
	ReleaseTaskLease(
		ctx context.Context,
		handle domain.TaskLeaseHandle,
		at time.Time,
	) (bool, error)
	UpdateTaskLeaseGuard(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
		handle domain.TaskLeaseHandle,
	) error
	AppendTaskLeaseEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// TaskLeaseOptions 是构造任务租约服务的输入。
type TaskLeaseOptions struct {
	// Store 是持久化入口，必填。
	Store TaskLeaseStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// TTL 是租约有效期，零值使用默认值。
	TTL time.Duration
}

// TaskLeaseService 是任务租约的唯一应用层入口。
//
// 职责边界：
//   - 用领域规则决定"这一次能否领取"，用存储层的单条条件 UPDATE 决定"谁真的拿到"；
//   - 领取成功后返回数据库确认过的 (owner, epoch) 身份，调用方不得自行构造；
//   - 续租与释放都以 owner + epoch 为条件，失败即视为执行权丢失；
//   - 每次领取与释放都写入可追溯事件。
//
// 它不实现队列扫描、并发名额控制与工作流推进：这些分别属于调度器与工作流编排器。
type TaskLeaseService struct {
	store  TaskLeaseStore
	logger *slog.Logger
	now    func() time.Time
	ttl    time.Duration
}

// NewTaskLeaseService 构造任务租约服务。
func NewTaskLeaseService(options TaskLeaseOptions) (*TaskLeaseService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("任务租约服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = defaultTaskLeaseTTL
	}
	return &TaskLeaseService{
		store:  options.Store,
		logger: logger,
		now:    now,
		ttl:    ttl,
	}, nil
}

// TTL 返回租约有效期。
func (s *TaskLeaseService) TTL() time.Duration {
	return s.ttl
}

// RenewInterval 返回建议的续租间隔。
//
// 取租约时长的三分之一：即使连续两次续租失败，租约仍有剩余时间，不会因为一次
// 瞬时数据库错误就让其他执行者提前接管。
func (s *TaskLeaseService) RenewInterval() time.Duration {
	if s.ttl <= 0 {
		return 0
	}
	interval := s.ttl / 3
	if interval < minTaskLeaseRenewInterval {
		interval = minTaskLeaseRenewInterval
	}
	return interval
}

// TaskClaimRequest 是一次任务领取请求。
//
// Statuses 必须由调用方显式给出：自动调度只能传 queued，任何"默认集合"都会让
// failed / paused / recovery_required 的任务被自动重放。
type TaskClaimRequest struct {
	// TaskID 是目标任务。
	TaskID domain.ID
	// Owner 是领取者标识，由调用方生成且每次领取唯一。
	Owner string
	// Statuses 是允许领取的状态集合，必须非空。
	Statuses []domain.TaskStatus
}

// TaskClaim 是一次成功领取的结果。
type TaskClaim struct {
	// Handle 是数据库确认过的租约身份。
	Handle domain.TaskLeaseHandle
	// Task 是领取成功后的任务记录快照。
	//
	// 它是"领取那一刻"的事实，调用方在产生副作用之前必须重新读取并核验执行权。
	Task domain.Task
	// ExpiresAt 是本次租约的到期时间。
	ExpiresAt time.Time
	// Takeover 表示本次领取建立在"上一个持有者的租约已过期"之上。
	Takeover bool
	// ClaimedAt 是领取时间。
	ClaimedAt time.Time
}

// Claim 以原子条件更新领取任务租约。
//
// 语义固定为：
//   - 领域判定（DecideTaskClaim）只回答"能否尝试"，真正的所有权由存储层决定；
//   - 存储层冲突返回 ErrTaskLeaseConflict，数据库错误原样上抛（可重试，不得当作
//     "没抢到"）；
//   - 领取成功后回读任务并核对 owner + epoch，确认数据库中的事实与返回值一致；
//   - 最后追加领取事件。事件写入失败时租约仍然有效，但必须记录日志：状态是唯一
//     真相来源，审计失败不应让一次真实领取被回滚成"没领取"。
func (s *TaskLeaseService) Claim(
	ctx context.Context,
	request TaskClaimRequest,
) (TaskClaim, error) {
	if err := request.TaskID.Validate(); err != nil {
		return TaskClaim{}, fmt.Errorf("%w: %w", ErrTaskLeaseInvalid, err)
	}
	if len(request.Statuses) == 0 {
		return TaskClaim{}, fmt.Errorf(
			"%w: 可领取状态集合不能为空",
			ErrTaskLeaseInvalid,
		)
	}
	owner := strings.TrimSpace(request.Owner)
	if owner == "" {
		return TaskClaim{}, fmt.Errorf("%w: 执行者标识不能为空", ErrTaskLeaseInvalid)
	}
	at := s.now()
	handle, err := s.store.AcquireTaskLease(ctx, storage.TaskLeaseRequest{
		TaskID:   request.TaskID,
		Owner:    owner,
		TTL:      s.ttl,
		Statuses: request.Statuses,
		At:       at,
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return TaskClaim{}, fmt.Errorf("%w: %w", ErrTaskLeaseConflict, err)
		}
		return TaskClaim{}, fmt.Errorf("领取任务租约失败: %w", err)
	}
	task, err := s.loadClaimedTask(ctx, handle)
	if err != nil {
		// 领取已经成功但无法确认：不释放租约，因为释放同样可能失败；由调用方按
		// 错误处理，租约最终自然过期，名额窗口受 TTL 约束。
		return TaskClaim{}, err
	}
	claim := TaskClaim{
		Handle:    handle,
		Task:      *task,
		ExpiresAt: at.Add(s.ttl),
		ClaimedAt: at,
		Takeover:  task.LeaseEpoch > 1,
	}
	s.appendLeaseEvent(ctx, *task, domain.EventTypeTaskClaimed, map[string]any{
		"owner":      handle.Owner,
		"epoch":      handle.Epoch,
		"takeover":   claim.Takeover,
		"claimed_at": at.Format(time.RFC3339Nano),
		"expires_at": claim.ExpiresAt.Format(time.RFC3339Nano),
	})
	return claim, nil
}

// loadClaimedTask 读取任务并核对本次领取确实生效。
//
// 读取失败或核对失败都按错误返回：调用方不得在"无法确认持有执行权"的情况下启动
// 任何有副作用的阶段。
func (s *TaskLeaseService) loadClaimedTask(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
) (*domain.Task, error) {
	task, err := s.store.GetTask(ctx, handle.TaskID)
	if err != nil {
		return nil, fmt.Errorf("领取后读取任务失败: %w", err)
	}
	if !handle.Matches(*task) || !task.LeaseActive(s.now()) {
		return nil, fmt.Errorf(
			"%w: 领取后任务 %s 的租约已不属于本次领取（owner %s，代次 %d）",
			ErrTaskLeaseLost,
			handle.TaskID,
			handle.Owner,
			handle.Epoch,
		)
	}
	return task, nil
}

// Renew 以 owner + epoch 为条件延长租约。
//
// 续期失败一律返回 ErrTaskLeaseLost：租约过期、已被接管与数据库不可用都意味着
// 调用方不能再假定自己拥有执行权，必须停止推进后续阶段。
func (s *TaskLeaseService) Renew(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
) error {
	if err := handle.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrTaskLeaseInvalid, err)
	}
	err := s.store.RenewTaskLease(ctx, storage.TaskLeaseRenewRequest{
		Handle: handle,
		TTL:    s.ttl,
		At:     s.now(),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTaskLeaseLost, err)
	}
	return nil
}

// Release 释放本次领取的租约。
//
// 返回的错误语义固定：
//   - 释放成功返回 nil；
//   - 租约已不属于本次领取（被接管、代次变化）返回 ErrTaskLeaseConflict，
//     调用方不得据此认为名额已经空出；
//   - 数据库错误原样上抛。
func (s *TaskLeaseService) Release(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
) error {
	if err := handle.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrTaskLeaseInvalid, err)
	}
	released, err := s.store.ReleaseTaskLease(ctx, handle, s.now())
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf("%w: %w", ErrTaskLeaseConflict, err)
		}
		return fmt.Errorf("释放任务租约失败: %w", err)
	}
	outcome, err := domain.CheckTaskLeaseRelease(boolToAffected(released))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTaskLeaseConflict, err)
	}
	if outcome != domain.TaskLeaseReleased {
		return fmt.Errorf("%w: 未释放任何租约", ErrTaskLeaseConflict)
	}
	s.appendLeaseEventForHandle(ctx, handle, domain.EventTypeTaskReleased, map[string]any{
		"owner":        handle.Owner,
		"epoch":        handle.Epoch,
		"released_at":  s.now().Format(time.RFC3339Nano),
		"release_kind": "explicit",
	})
	return nil
}

// UpdateGuarded 在仍持有执行权时写入任务状态。
//
// 调用方必须先取得最新任务记录，本方法只负责让写入附带 owner + epoch 条件：
// 状态写入与执行权校验因此成为同一条数据库条件，不存在"校验通过后执行权被抢走"
// 的窗口。
func (s *TaskLeaseService) UpdateGuarded(
	ctx context.Context,
	task domain.Task,
	expected []domain.TaskStatus,
	handle domain.TaskLeaseHandle,
) error {
	if err := handle.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrTaskLeaseInvalid, err)
	}
	err := s.store.UpdateTaskLeaseGuard(ctx, task, expected, handle)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf("%w: %w", ErrTaskLeaseLost, err)
		}
		return fmt.Errorf("写入任务状态失败: %w", err)
	}
	return nil
}

// Hold 判断给定租约在指定时刻是否仍持有执行权。
//
// 它只读记录，不读数据库：调用方必须传入重新读取的最新任务记录。
func (s *TaskLeaseService) Hold(
	task domain.Task,
	handle domain.TaskLeaseHandle,
) bool {
	return domain.TaskHoldsLease(task, handle, s.now())
}

// appendLeaseEvent 追加租约事件，失败只记录日志。
func (s *TaskLeaseService) appendLeaseEvent(
	ctx context.Context,
	task domain.Task,
	eventType domain.EventType,
	payload map[string]any,
) {
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		eventType,
		marshalStepExecutionPayload(payload),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造任务租约事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("event", string(eventType)),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendTaskLeaseEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入任务租约事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("event", string(eventType)),
			slog.String("error", errorSummary(err)),
		)
	}
}

// appendLeaseEventForHandle 在只知道租约身份时追加事件。
//
// 释放路径不再重新读取任务：读取失败不应让已经成功的释放变成"未知"，因此事件
// 以租约身份里的任务标识为准。
func (s *TaskLeaseService) appendLeaseEventForHandle(
	ctx context.Context,
	handle domain.TaskLeaseHandle,
	eventType domain.EventType,
	payload map[string]any,
) {
	s.appendLeaseEvent(ctx, domain.Task{ID: handle.TaskID}, eventType, payload)
}

// boolToAffected 把"是否释放成功"转换为受影响行数。
//
// 领域判定 CheckTaskLeaseRelease 以行数为输入；存储层已用布尔表达同一事实，
// 这里做一次显式转换，避免在两个地方各自实现一次判定。
func boolToAffected(released bool) int64 {
	if released {
		return 1
	}
	return 0
}
