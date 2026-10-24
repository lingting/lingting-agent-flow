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

// ExecutionResidueStore 是残留收敛依赖的存储能力组合。
//
// 它只读取残留记录并做条件更新：收敛不创建新的 Attempt、Session 或 ValidationRun，
// 也不重放任何 Agent 调用。
type ExecutionResidueStore interface {
	// ListAttemptsByStatus 返回指定状态的尝试。
	ListAttemptsByStatus(ctx context.Context, statuses []domain.AttemptStatus) ([]domain.Attempt, error)
	// UpdateAttemptIfStatus 在数据库中的状态仍属于 expected 时写入尝试。
	UpdateAttemptIfStatus(
		ctx context.Context,
		attempt domain.Attempt,
		expected []domain.AttemptStatus,
	) error
	// ListSessionsByStatus 返回指定执行状态的会话。
	ListSessionsByStatus(ctx context.Context, statuses []domain.SessionStatus) ([]domain.Session, error)
	// UpdateSession 写入会话。
	UpdateSession(ctx context.Context, session domain.Session) error
	// ListValidationRunsByStatus 返回指定流程状态的验证记录。
	ListValidationRunsByStatus(
		ctx context.Context,
		statuses []domain.ValidationRunStatus,
	) ([]domain.ValidationRun, error)
	// FailValidationRun 把验证记录收敛为终态。
	FailValidationRun(
		ctx context.Context,
		id domain.ID,
		status domain.ValidationRunStatus,
		reason string,
		at time.Time,
	) error
	// ListUnfinishedWorkflowRuns 返回全部未结束的执行记录。
	//
	// 它是"原执行者是否仍持有执行权"的唯一判据来源：只有租约已过期的记录才允许
	// 收敛其残留 Attempt / Session / ValidationRun。
	ListUnfinishedWorkflowRuns(ctx context.Context) ([]domain.WorkflowRun, error)
}

// ExecutionResidueService 收敛进程退出后残留在活动状态的三类记录。
//
// 职责边界：
//   - 只把"已确定没有执行者"的残留收敛为合法终态；
//   - 不重放任何 Agent 调用、不删除任何现场、不创建新的执行记录；
//   - 所有写入都带状态前置条件，迟到结果与并发收敛都不会覆盖已确认的终态。
//
// 它不实现重试资格判定：`interrupted` 的尝试是否可以被自动重跑属于 P2-M3，
// 本服务只保证残留记录不再停留在无法解释的活动状态。
type ExecutionResidueService struct {
	store  ExecutionResidueStore
	logger *slog.Logger
	now    func() time.Time
}

// ExecutionResidueOptions 是构造残留收敛服务的输入。
type ExecutionResidueOptions struct {
	// Store 是持久化入口，必填。
	Store ExecutionResidueStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// NewExecutionResidueService 构造残留收敛服务。
func NewExecutionResidueService(options ExecutionResidueOptions) (*ExecutionResidueService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("残留收敛服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ExecutionResidueService{store: options.Store, logger: logger, now: now}, nil
}

// Name 返回后台服务名称。
func (s *ExecutionResidueService) Name() string {
	return "execution_residue"
}

// Start 收敛三类残留记录。
//
// 顺序固定为：先确定"哪些任务的执行者已经失去执行权"，再收敛 ValidationRun、
// Attempt 与 Session。判定依据只有持久化事实与租约：租约仍有效的执行记录说明
// 可能有其他进程正在推进，其残留一律不动。
//
// 任一条记录收敛失败都让启动失败：残留记录没有落库时服务继续启动，会让"无法解释
// 的活动状态"被当作已恢复，之后该 Attempt 再也无法验证、该步骤也无法再执行。
func (s *ExecutionResidueService) Start(ctx context.Context) error {
	live, err := s.liveTasks(ctx)
	if err != nil {
		return err
	}
	if err := s.recoverValidationRuns(ctx, live); err != nil {
		return err
	}
	if err := s.recoverAttempts(ctx, live); err != nil {
		return err
	}
	return s.recoverSessions(ctx, live)
}

// Stop 不做事：残留收敛是启动时的一次性动作，没有长期 goroutine。
func (s *ExecutionResidueService) Stop(ctx context.Context) error {
	return nil
}

// liveTasks 返回"仍有执行者持有有效执行权"的任务标识集合。
//
// 只有 `running` 且租约仍然有效的执行记录说明"可能有执行者正在推进"：
// `blocked` / `recovery_required` / `pending` / `cancel_requested` 都已经没有
// 执行权（`Stop` / `Fail` / `Cancel` 清空 worker_id 与租约，`pending` 从未写入）。
//
// 返回集合而不是"残留任务集合"是刻意的：没有执行记录的任务同样可能留下 Attempt /
// Session 残留（进程在创建 WorkflowRun 之前就退出），它们没有任何执行者，必须被
// 收敛。用"不在集合里"表达"可以收敛"，覆盖了这两种情况。
func (s *ExecutionResidueService) liveTasks(ctx context.Context) (map[domain.ID]struct{}, error) {
	return liveExecutionTasks(ctx, s.store, s.now())
}

// liveExecutionTasks 返回"仍有执行者持有有效执行权"的任务标识集合。
//
// 它是"能否安全收敛残留"的唯一判据来源，供所有残留收敛入口复用：
// ExecutionResidueService 用它收敛 Attempt / Session / ValidationRun，
// CodeFetchRecoveryService 用它收敛 Attempt 级代码获取记录。两处各写一套判定会让
// "任务是否还有执行者"出现第二种解释，进而导致一个服务抢占另一个服务正在处理的现场。
func liveExecutionTasks(
	ctx context.Context,
	store unfinishedWorkflowRunLister,
	now time.Time,
) (map[domain.ID]struct{}, error) {
	runs, err := store.ListUnfinishedWorkflowRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取未结束执行记录失败: %w", err)
	}
	live := make(map[domain.ID]struct{}, len(runs))
	for _, run := range runs {
		if run.Status != domain.WorkflowRunRunning {
			continue
		}
		if !run.LeaseActive(now) {
			// 租约已过期：持有者没有续租，进程很可能已经退出。
			continue
		}
		live[run.TaskID] = struct{}{}
	}
	return live, nil
}

// unfinishedWorkflowRunLister 是读取未结束执行记录的最小存储能力。
type unfinishedWorkflowRunLister interface {
	ListUnfinishedWorkflowRuns(ctx context.Context) ([]domain.WorkflowRun, error)
}

// recoverValidationRuns 收敛残留的验证记录。
//
// 语义固定为：残留的 pending / running 验证一律收敛为 failed + verdict=failed，
// 因为"流程没有跑完"永远不可能支持"验证通过"。它必须与"确定性检查已经失败"区分：
// 前者由 error_detail 说明中断来源，failure_summary 使用"流程未完成"的措辞，
// 不出现"必需检查未通过"这类暗示代码有问题的结论。
func (s *ExecutionResidueService) recoverValidationRuns(
	ctx context.Context,
	live map[domain.ID]struct{},
) error {
	runs, err := s.store.ListValidationRunsByStatus(ctx, []domain.ValidationRunStatus{
		domain.ValidationRunStatusPending,
		domain.ValidationRunStatusRunning,
	})
	if err != nil {
		return fmt.Errorf("读取残留验证记录失败: %w", err)
	}
	now := s.now()
	for _, run := range runs {
		if _, ok := live[run.TaskID]; ok {
			// 该任务仍有持有有效租约的执行者：验证可能仍在推进，不抢占。
			continue
		}
		reason := validationResidueSummary(run.Status)
		if err := s.store.FailValidationRun(
			context.WithoutCancel(ctx),
			run.ID,
			domain.ValidationRunStatusFailed,
			reason,
			now,
		); err != nil {
			if errors.Is(err, storage.ErrConflict) {
				// 另一个进程或迟到的执行结果已经收敛了这条记录：不是失败。
				continue
			}
			return fmt.Errorf("收敛验证记录 %s 失败: %w", run.ID, err)
		}
		s.logger.InfoContext(
			ctx,
			"已收敛残留验证记录",
			slog.String("validation_run_id", run.ID.String()),
			slog.String("task_id", run.TaskID.String()),
			slog.String("attempt_id", run.AttemptID.String()),
			slog.String("previous", string(run.Status)),
		)
	}
	return nil
}

// validationResidueSummary 返回残留验证收敛时写入的说明。
//
// 说明必须表达"验证没有跑完"，不得使用任何暗示"检查已经失败"的措辞：两者的恢复
// 动作不同，混淆会让用户误以为代码有问题。
func validationResidueSummary(status domain.ValidationRunStatus) string {
	return "验证在进程退出时仍处于 " + string(status) +
		" 状态，没有跑完，无法得到可用证据；由启动恢复收敛为失败终态，需要重新验证"
}

// recoverAttempts 收敛残留的执行尝试。
//
// 收敛目标固定为 interrupted：它表达的正是"编排器或宿主进程异常结束，未能给出
// 正常结论"。残留记录绝不被改回 running，也绝不被改成 completed。
//
// 已有验证判定（verification_id 非空）的尝试说明验证曾经跑过，此时仍按 interrupted
// 收敛：判定字段保留原值，步骤层的收口由步骤占用收敛负责。
func (s *ExecutionResidueService) recoverAttempts(
	ctx context.Context,
	live map[domain.ID]struct{},
) error {
	attempts, err := s.store.ListAttemptsByStatus(ctx, []domain.AttemptStatus{
		domain.AttemptStatusRunning,
	})
	if err != nil {
		return fmt.Errorf("读取残留执行尝试失败: %w", err)
	}
	now := s.now()
	for _, attempt := range attempts {
		if _, ok := live[attempt.TaskID]; ok {
			// 任务仍有持有有效租约的执行者：尝试可能仍在执行，不抢占。
			continue
		}
		recovered := attempt
		if err := recovered.Interrupt(attemptResidueSummary, now); err != nil {
			// 状态机拒绝说明本地状态已过期：重新读取由条件更新负责，不覆盖数据库。
			s.logger.WarnContext(
				ctx,
				"收敛残留执行尝试被拒绝",
				slog.String("attempt_id", attempt.ID.String()),
				slog.String("status", string(attempt.Status)),
				slog.String("error", errorSummary(err)),
			)
			continue
		}
		recovered.FailureStage = "recover"
		recovered.ErrorClass = string(domain.RetryClassAgentFailure)
		if err := s.store.UpdateAttemptIfStatus(
			context.WithoutCancel(ctx),
			recovered,
			[]domain.AttemptStatus{domain.AttemptStatusRunning},
		); err != nil {
			if errors.Is(err, storage.ErrConflict) {
				continue
			}
			return fmt.Errorf("收敛执行尝试 %s 失败: %w", attempt.ID, err)
		}
		s.logger.InfoContext(
			ctx,
			"已收敛残留执行尝试",
			slog.String("attempt_id", attempt.ID.String()),
			slog.String("task_id", attempt.TaskID.String()),
			slog.String("step_id", attempt.StepID.String()),
		)
	}
	return nil
}

// attemptResidueSummary 返回残留尝试收敛时写入的说明。
const attemptResidueSummary = "上一次执行的进程已退出，尝试仍处于 running，无法确认执行结果；由启动恢复收敛为中断，需要人工核验现场"

// recoverSessions 收敛残留的会话。
//
// 收敛目标固定为 interrupted：它是"编排器或宿主进程异常结束"的正式表达。
//
// 关键约束：不得删除已有的 jsonl_path / html_path / stdout_path / stderr_path /
// process_id / exit_code / archive_path。这些字段是唯一的现场，收敛只改执行状态、
// 结束时间与失败摘要。
//
// 三维度（completeness / export_status / archive_status）保持原值：归档成功仍然
// 是归档成功，收敛执行状态不得改写归档事实。
func (s *ExecutionResidueService) recoverSessions(
	ctx context.Context,
	live map[domain.ID]struct{},
) error {
	sessions, err := s.store.ListSessionsByStatus(ctx, []domain.SessionStatus{
		domain.SessionStatusStarting,
		domain.SessionStatusRunning,
	})
	if err != nil {
		return fmt.Errorf("读取残留会话失败: %w", err)
	}
	now := s.now()
	for _, session := range sessions {
		if _, ok := live[session.TaskID]; ok {
			// 任务仍有持有有效租约的执行者：会话可能仍在运行，不抢占。
			continue
		}
		recovered := session
		if err := recovered.Interrupt(now); err != nil {
			s.logger.WarnContext(
				ctx,
				"收敛残留会话被拒绝",
				slog.String("session_id", session.ID.String()),
				slog.String("status", string(session.Status)),
				slog.String("error", errorSummary(err)),
			)
			continue
		}
		recovered.RunStatus = domain.SessionRunFailed
		recovered.FailureSummary = joinSummary(
			recovered.FailureSummary,
			sessionResidueSummary,
		)
		if err := s.store.UpdateSession(context.WithoutCancel(ctx), recovered); err != nil {
			return fmt.Errorf("收敛会话 %s 失败: %w", session.ID, err)
		}
		s.logger.InfoContext(
			ctx,
			"已收敛残留会话",
			slog.String("session_id", session.ID.String()),
			slog.String("task_id", session.TaskID.String()),
			slog.String("previous", string(session.Status)),
		)
	}
	return nil
}

// sessionResidueSummary 返回残留会话收敛时写入的说明。
const sessionResidueSummary = "上一次执行的进程已退出，会话仍处于活动状态；由启动恢复收敛为中断，已有产物路径原样保留"
