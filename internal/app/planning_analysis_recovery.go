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

// planningAnalysisInterruptedSummary 是进程中断留下的失败原因摘要。
//
// 它说明的是"上一轮分析没有执行者"这一已核实的现场，而不是猜测阶段：真正的执行阶段
// 只存在于中断进程的内存里，恢复过程不会伪造它。
const planningAnalysisInterruptedSummary = "Server 进程中断：上一轮分析没有执行者，已按失败收敛"

// errPlanningAnalysisRecoverySkip 表示这条占用不需要恢复。
//
// 它只用于让事务整体回滚并跳过当前会话：读取到的状态或占用已被其他执行者推进时，
// 恢复不得覆盖数据库中的新事实。
var errPlanningAnalysisRecoverySkip = errors.New(
	"app: 规划分析占用已不需要恢复",
)

// PlanningAnalysisRecoveryStore 是分析占用恢复依赖的存储能力。
//
// 只有三项能力：列出仍然存在的占用、按事务读取会话、按事务写入状态与占用。恢复不触碰
// 规划容器、Session 文件或任何运行时资源。
type PlanningAnalysisRecoveryStore interface {
	// ListPlanningAnalysisClaims 返回当前仍持有分析占用的规划会话。
	ListPlanningAnalysisClaims(ctx context.Context) ([]storage.PlanningAnalysisClaim, error)
	// WithTx 在单个事务内执行状态收敛与占用释放。
	WithTx(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) error
}

// PlanningAnalysisRecoveryOptions 是构造分析占用恢复服务的输入。
type PlanningAnalysisRecoveryOptions struct {
	// Store 是持久化入口，必填。
	Store PlanningAnalysisRecoveryStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// PlanningAnalysisRecoveryService 在服务启动时收敛上一次进程中断留下的分析占用。
//
// 职责边界：
//   - 只处理"状态是 analyzing 且仍有占用"的会话，其余状态一律不动；
//   - 用领域状态迁移方法把会话收敛为 failed 并写入明确的进程中断原因；
//   - 以原占用标识为条件释放占用，条件不成立时整体回滚，不覆盖其他执行者的新事实；
//   - 不重建、不删除、不重新准备规划容器，也不改变规划运行时的生命周期。
//
// 它依赖 Server 的独占进程锁：同一份服务端数据目录同时只有一个进程，因此启动时读到的
// 占用不可能属于另一台正在运行的 Server，遗留占用只可能是上一次进程中断留下的。
type PlanningAnalysisRecoveryService struct {
	store  PlanningAnalysisRecoveryStore
	logger *slog.Logger
	now    func() time.Time
}

// NewPlanningAnalysisRecoveryService 构造分析占用恢复服务。
func NewPlanningAnalysisRecoveryService(
	options PlanningAnalysisRecoveryOptions,
) (*PlanningAnalysisRecoveryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("规划分析占用恢复服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PlanningAnalysisRecoveryService{
		store:  options.Store,
		logger: logger,
		now:    now,
	}, nil
}

// Name 返回后台服务名称。
func (s *PlanningAnalysisRecoveryService) Name() string {
	return "planning_analysis_recovery"
}

// Start 收敛遗留的分析占用。
//
// 读取失败让启动失败：无法判断是否存在遗留占用时继续启动，会让这些会话在 analyzing
// 上永久停留，而且没有任何界面能解释原因。
func (s *PlanningAnalysisRecoveryService) Start(ctx context.Context) error {
	claims, err := s.store.ListPlanningAnalysisClaims(ctx)
	if err != nil {
		return fmt.Errorf("读取规划分析占用失败: %w", err)
	}
	for _, claim := range claims {
		if err := s.recoverClaim(ctx, claim); err != nil {
			return fmt.Errorf("收敛规划会话 %s 的分析占用失败: %w", claim.SessionID, err)
		}
	}
	if len(claims) == 0 {
		return nil
	}
	s.logger.InfoContext(
		ctx,
		"已检查遗留的规划分析占用",
		slog.Int("claim_count", len(claims)),
	)
	return nil
}

// Stop 不做事：收敛是启动时的一次性动作，没有长期 goroutine。
func (s *PlanningAnalysisRecoveryService) Stop(ctx context.Context) error {
	return nil
}

// recoverClaim 收敛单条遗留占用。
//
// 状态迁移与占用释放共享同一个事务：状态条件更新或占用标识条件不成立时整体回滚，
// 因此恢复既不会覆盖其他执行者已经推进的状态，也不会释放别人的占用。
func (s *PlanningAnalysisRecoveryService) recoverClaim(
	ctx context.Context,
	claim storage.PlanningAnalysisClaim,
) error {
	// 恢复写入使用未取消的上下文：进程停止过程中的启动收敛同样要把结论落库。
	writeCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		planningFailureWriteTimeout,
	)
	defer cancel()
	at := s.now()
	err := s.store.WithTx(writeCtx, func(ctx context.Context, tx storage.Tx) error {
		session, err := readPlanningSession(ctx, tx, claim.SessionID)
		if err != nil {
			if errors.Is(err, ErrPlanningSessionNotFound) {
				// 会话已被删除：占用没有归属，不需要恢复。
				return errPlanningAnalysisRecoverySkip
			}
			return err
		}
		if session.Status != domain.PlanningSessionAnalyzing {
			// 状态已不是 analyzing：其他执行者已经推进了这轮分析，不覆盖新事实。
			return errPlanningAnalysisRecoverySkip
		}
		if err := session.Fail(planningAnalysisInterruptedSummary, at); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			session,
			[]domain.PlanningSessionStatus{domain.PlanningSessionAnalyzing},
		); err != nil {
			if errors.Is(err, storage.ErrConflict) {
				return errPlanningAnalysisRecoverySkip
			}
			return mapPlanningUpdateFailure(err, claim.SessionID)
		}
		if err := tx.ReleasePlanningAnalysis(
			ctx,
			claim.SessionID,
			claim.ClaimID,
			at,
		); err != nil {
			if errors.Is(err, storage.ErrConflict) {
				// 占用已不属于这条记录：整体回滚，保留数据库中的新事实。
				return errPlanningAnalysisRecoverySkip
			}
			return mapPlanningClaimFailure(err, claim.SessionID)
		}
		return nil
	})
	if err == nil {
		s.logger.WarnContext(
			writeCtx,
			"已收敛进程中断遗留的规划分析",
			slog.String("planning_session_id", claim.SessionID.String()),
			slog.String("summary", planningAnalysisInterruptedSummary),
		)
		return nil
	}
	if errors.Is(err, errPlanningAnalysisRecoverySkip) {
		s.logger.WarnContext(
			writeCtx,
			"规划分析占用已被其他执行者推进，跳过恢复",
			slog.String("planning_session_id", claim.SessionID.String()),
			slog.String("error", err.Error()),
		)
		return nil
	}
	return err
}
