package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 规划会话租约的错误类别。
//
// 三个类别对应完全不同的处理方式，调用方通过 errors.Is 判断，不解析错误文本：
//   - 竞争（conflict）：同一规划会话正被其他执行者占用，属于正常并发；
//   - 丢失（lost）：本次执行者已不能确认自己持有执行权，必须立即停止并如实上报；
//   - 不可用（unavailable）：租约数据库操作失败，属于可重试的运行时故障。
//
// 数据库暂时不可用不得被伪装成"其他请求正在运行"：那会让调用方把一个需要重试的故障
// 当作稍后可以再来的竞争。各应用服务把这三个类别映射成自己的错误体系。
var (
	// errPlanningLeaseConflict 表示同一规划会话的租约正被其他执行者持有。
	errPlanningLeaseConflict = errors.New("app: 规划会话租约正被其他执行者持有")
	// errPlanningLeaseLost 表示本次执行者已失去规划会话租约。
	errPlanningLeaseLost = errors.New("app: 规划会话租约已丢失")
	// errPlanningLeaseUnavailable 表示规划会话租约的数据库操作失败。
	errPlanningLeaseUnavailable = errors.New("app: 规划会话租约存储不可用")
)

// 规划会话租约的默认参数。
const (
	// defaultPlanningLeaseTTL 是规划会话租约的默认有效期。
	//
	// 一次规划角色调用可能持续数分钟（Pi 执行加 Session 归档），因此执行期间由后台任务
	// 续租；TTL 只约束"执行者消失后多久可以被接管"。它必须明显长于一次续租往返，也
	// 不能过长，否则进程崩溃后残留的租约会长时间占住会话。
	defaultPlanningLeaseTTL = 2 * time.Minute
	// minPlanningLeaseRenewInterval 是续租间隔的下限，避免可注入的极短租约产生过密心跳。
	minPlanningLeaseRenewInterval = 50 * time.Millisecond
	// planningLeaseOperationTimeout 是单次租约读写（领取、续期、释放）的超时。
	//
	// 它同时约束续租的后台任务：一次续租不能无限挂起，否则"无法确认持有权"会被拖成
	// 永远不会发生的判断。
	planningLeaseOperationTimeout = 5 * time.Second
)

// PlanningLeaseOptions 是构造规划会话租约服务的输入。
type PlanningLeaseOptions struct {
	// Store 是租约的持久化入口，必填。
	Store storage.PlanningSessionLeaseStore
	// Now 是可注入的时钟，零值使用 UTC 当前时间。
	Now func() time.Time
	// TTL 是租约有效期，零值使用默认值。
	TTL time.Duration
}

// PlanningSessionLeaseService 是规划会话跨进程执行租约的唯一入口。
//
// 职责边界：
//   - 用一条原子条件写入决定"谁取得这个规划会话的执行权"，并返回数据库确认过的身份；
//   - 执行期间由后台任务按固定间隔续期，续期失败立即取消业务上下文；
//   - 收尾时先停止续租并等待其退出，再以 owner + epoch 为条件释放租约。
//
// 它不做规划业务判断、不操作容器或 Session 文件，也不实现通用分布式锁：租约只服务
// PlanningSession 这一种资源，且不提供跨资源的加锁顺序或队列。
type PlanningSessionLeaseService struct {
	store storage.PlanningSessionLeaseStore
	now   func() time.Time
	ttl   time.Duration
}

// NewPlanningSessionLeaseService 构造规划会话租约服务。
func NewPlanningSessionLeaseService(
	options PlanningLeaseOptions,
) (*PlanningSessionLeaseService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("规划会话租约服务初始化失败: 存储不能为空")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = defaultPlanningLeaseTTL
	}
	return &PlanningSessionLeaseService{store: options.Store, now: now, ttl: ttl}, nil
}

// TTL 返回租约有效期。
func (s *PlanningSessionLeaseService) TTL() time.Duration {
	return s.ttl
}

// RenewInterval 返回续租间隔。
//
// 取租约时长的三分之一：即使连续两次续租失败，租约仍有剩余时间，不会因为一次瞬时数据库
// 错误就让其他执行者提前接管。
func (s *PlanningSessionLeaseService) RenewInterval() time.Duration {
	if s.ttl <= 0 {
		return 0
	}
	interval := s.ttl / 3
	if interval < minPlanningLeaseRenewInterval {
		interval = minPlanningLeaseRenewInterval
	}
	return interval
}

// OperationTimeout 返回单次租约读写的超时。
func (s *PlanningSessionLeaseService) OperationTimeout() time.Duration {
	return planningLeaseOperationTimeout
}

// Acquire 领取规划会话租约，并在领取成功后立即开始续租。
//
// owner 由服务端生成且每次领取唯一，调用方无法指定；代次由存储层分配，应用层既不计算
// 也不传入。返回的 lease 携带交给业务执行的上下文：一旦续租失败，该上下文会被取消，
// 使正在进行中的 Pi 执行与 Session 归档尽快收敛。
//
// 续租的后台上下文不绑定调用方上下文：请求被取消后 Runner 仍可能需要完成 Session 提取
// 与归档，因此租约必须继续有效，直到调用方显式收尾（Settle）。
func (s *PlanningSessionLeaseService) Acquire(
	ctx context.Context,
	sessionID domain.ID,
	operation domain.PlanningLeaseOperation,
) (*planningLease, error) {
	acquireCtx, cancel := context.WithTimeout(ctx, s.OperationTimeout())
	defer cancel()
	handle, err := s.store.AcquirePlanningSessionLease(
		acquireCtx,
		storage.PlanningSessionLeaseRequest{
			PlanningSessionID: sessionID,
			Owner:             domain.NewID().String(),
			Operation:         operation,
			TTL:               s.ttl,
			At:                s.now(),
		},
	)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return nil, fmt.Errorf(
				"%w: 规划会话 %s 正被其他执行者占用: %w",
				errPlanningLeaseConflict,
				sessionID,
				err,
			)
		}
		return nil, fmt.Errorf("%w: 领取规划会话租约失败: %w", errPlanningLeaseUnavailable, err)
	}
	execCtx, cancelExec := context.WithCancelCause(ctx)
	lease := &planningLease{
		service: s,
		handle:  handle,
		base:    context.WithoutCancel(ctx),
		ctx:     execCtx,
		cancel:  cancelExec,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go lease.renewLoop()
	return lease, nil
}

// renew 以 owner + epoch 为条件延长租约。
func (s *PlanningSessionLeaseService) renew(
	ctx context.Context,
	handle domain.PlanningSessionLeaseHandle,
) error {
	err := s.store.RenewPlanningSessionLease(
		ctx,
		storage.PlanningSessionLeaseRenewRequest{Handle: handle, TTL: s.ttl, At: s.now()},
	)
	if err != nil {
		// 竞争、过期与数据库故障都无法再确认持有权，一律按丢失处理；底层原因保留在
		// 错误链里，使"明确被接管"与"暂时无法确认"在诊断上仍可区分。
		return fmt.Errorf("%w: 续期规划会话租约失败: %w", errPlanningLeaseLost, err)
	}
	return nil
}

// release 以 owner + epoch 为条件释放租约。
func (s *PlanningSessionLeaseService) release(
	ctx context.Context,
	handle domain.PlanningSessionLeaseHandle,
) error {
	released, err := s.store.ReleasePlanningSessionLease(ctx, handle, s.now())
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 租约已被接管或已过期：本次执行者无权释放，不得据此认为会话已空闲。
			return fmt.Errorf("%w: %w", errPlanningLeaseLost, err)
		}
		return fmt.Errorf("%w: 释放规划会话租约失败: %w", errPlanningLeaseUnavailable, err)
	}
	if !released {
		return fmt.Errorf("%w: 没有释放任何规划会话租约", errPlanningLeaseLost)
	}
	return nil
}

// planningLease 是一次规划会话租约的持有状态。
//
// 它把"业务执行上下文"与"续租后台上下文"分开：业务上下文会被业务取消或生命周期停止
// 取消，而续租只由 Settle 结束，直到 Runner 与归档路径都收敛为止。
type planningLease struct {
	service *PlanningSessionLeaseService
	handle  domain.PlanningSessionLeaseHandle
	// base 是续租与释放使用的父上下文：保留调用方的值，但不继承其取消信号。
	base context.Context
	// ctx 是交给业务执行的上下文：租约丢失时被取消。
	ctx context.Context
	// cancel 取消业务执行上下文，参数是取消原因。
	cancel context.CancelCauseFunc
	// stop 由 Settle 关闭，用于结束续租循环。
	stop chan struct{}
	// done 在续租循环退出时关闭。
	done chan struct{}
	// stopOnce 保证 stop 只被关闭一次。
	stopOnce sync.Once
	// mu 保护 lost。
	mu sync.Mutex
	// lost 是续租失败的原因，为空表示仍持有租约。
	lost error
}

// Context 返回交给业务执行的上下文。
//
// 该上下文在租约丢失时被取消。它同时继承调用方上下文的取消信号：两者任一发生都要求
// 正在进行的远程调用尽快收敛。
func (l *planningLease) Context() context.Context {
	return l.ctx
}

// Settle 停止续租、等待续租退出，并释放本次领取的租约。
//
// 调用时机固定为"远程执行与 Session 归档都已返回之后"：Pi 进程退出不等于本轮调用结束，
// 归档尚未完成时其他执行者仍不能开始下一轮。返回的错误保留租约丢失与释放失败的区分，
// 调用方必须把它反映到自己的结果上，不得因为远程执行成功就忽略收尾失败。
func (l *planningLease) Settle() error {
	l.stopOnce.Do(func() { close(l.stop) })
	<-l.done
	releaseCtx, cancel := context.WithTimeout(l.base, l.service.OperationTimeout())
	defer cancel()
	releaseErr := l.service.release(releaseCtx, l.handle)
	// 业务执行已完成：释放业务上下文，避免留下未调用的 cancel。
	l.cancel(nil)
	return errors.Join(l.lostError(), releaseErr)
}

// renewLoop 按固定间隔续租，直到 Settle 结束或续租失败。
func (l *planningLease) renewLoop() {
	defer close(l.done)
	ticker := time.NewTicker(l.service.RenewInterval())
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.base, l.service.OperationTimeout())
			err := l.service.renew(ctx, l.handle)
			cancel()
			if err != nil {
				l.markLost(err)
				return
			}
		}
	}
}

// markLost 记录租约丢失并取消业务执行上下文。
//
// 取消是"旧执行者必须停止"的唯一手段：数据库代次能阻止旧持有者继续续期与释放新租约，
// 但容器里的 Pi 进程不会自己读取数据库，必须由取消让本轮远程调用开始收敛。
func (l *planningLease) markLost(err error) {
	l.mu.Lock()
	if l.lost == nil {
		l.lost = err
	}
	lost := l.lost
	l.mu.Unlock()
	l.cancel(lost)
}

// lostError 返回续租失败原因，仍持有租约时返回 nil。
func (l *planningLease) lostError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost
}

// planningAgentLeaseFailure 把租约层错误映射为规划 Agent 的错误类别。
//
// 竞争、丢失与存储故障必须能区分：它们分别对应"稍后重试"、"立即停止并如实上报"与
// "按数据库故障处理"，调用方不得通过解析错误文本判断。
func planningAgentLeaseFailure(err error, sessionID string) error {
	switch {
	case errors.Is(err, errPlanningLeaseConflict):
		return fmt.Errorf("%w: 规划会话 %s 的租约被其他执行者持有: %w", ErrPlanningAgentBusy, sessionID, err)
	case errors.Is(err, errPlanningLeaseLost):
		return fmt.Errorf("%w: 规划会话 %s 的执行权已不可确认: %w", ErrPlanningAgentLeaseLost, sessionID, err)
	default:
		return fmt.Errorf("%w: 规划会话 %s 的租约操作失败: %w", ErrPlanningAgentLeaseUnavailable, sessionID, err)
	}
}

// planningRuntimeLeaseFailure 把租约层错误映射为规划运行时的错误类别。
//
// 它保留失败阶段（lease），使"取得租约失败"与"租约收尾失败"在同一类别下仍可定位。
func planningRuntimeLeaseFailure(err error, stage PlanningRuntimeStage, detail string) error {
	kind := ErrPlanningRuntimeLeaseUnavailable
	switch {
	case errors.Is(err, errPlanningLeaseConflict):
		kind = ErrPlanningRuntimeBusy
	case errors.Is(err, errPlanningLeaseLost):
		kind = ErrPlanningRuntimeLeaseLost
	}
	return planningRuntimeFailure(kind, stage, detail, err)
}
