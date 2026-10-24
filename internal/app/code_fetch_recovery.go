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

// CodeFetchRecoveryStore 是尝试级代码获取残留收敛依赖的存储能力。
type CodeFetchRecoveryStore interface {
	// ListInterruptedCodeFetchRecords 返回仍停留在 pending / running 的记录。
	ListInterruptedCodeFetchRecords(ctx context.Context) ([]domain.CodeFetchRecord, error)
	// UpdateCodeFetchRecordIfStatus 在数据库中的状态仍属于 expected 时写入记录。
	UpdateCodeFetchRecordIfStatus(
		ctx context.Context,
		record domain.CodeFetchRecord,
		expected []domain.CodeFetchStatus,
	) error
	// ListUnfinishedWorkflowRuns 返回全部未结束的执行记录。
	ListUnfinishedWorkflowRuns(ctx context.Context) ([]domain.WorkflowRun, error)
	// AppendEvent 追加任务事件。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// CodeFetchRecoveryOptions 是构造尝试级代码获取残留收敛服务的输入。
type CodeFetchRecoveryOptions struct {
	// Store 是持久化入口，必填。
	Store CodeFetchRecoveryStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// CodeFetchRecoveryService 收敛进程退出后停留在活动状态的尝试级代码获取记录。
//
// 职责边界：
//   - 只把"已确定没有执行者"的 pending / running 记录收敛为 uncertain；
//   - 不重放任何容器内 Git 操作，不删除容器与工作区；
//   - 所有写入都带状态前置条件，迟到结果与并发收敛都不会覆盖已确认的终态；
//   - 未决现场只报告，不自动判定"代码没有获取到"。
//
// 收敛目标固定为 uncertain 而不是 failed：`pending` 与 `running` 都不能证明 Clone
// 是否真的发生过，空 `commit_sha` 同样不是"未发生"的证据。
type CodeFetchRecoveryService struct {
	store  CodeFetchRecoveryStore
	logger *slog.Logger
	now    func() time.Time
}

// NewCodeFetchRecoveryService 构造代码获取残留收敛服务。
func NewCodeFetchRecoveryService(
	options CodeFetchRecoveryOptions,
) (*CodeFetchRecoveryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("代码获取残留收敛服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CodeFetchRecoveryService{store: options.Store, logger: logger, now: now}, nil
}

// Name 返回后台服务名称。
func (s *CodeFetchRecoveryService) Name() string {
	return "code_fetch_recovery"
}

// Start 收敛残留的代码获取记录。
//
// 任一条记录收敛失败都让启动失败：残留已落库时服务继续启动，会让一条无法解释的
// 活动记录一直阻止该 Attempt 重新初始化，用户既看不到原因也无法自行处置。
func (s *CodeFetchRecoveryService) Start(ctx context.Context) error {
	live, err := liveExecutionTasks(ctx, s.store, s.now())
	if err != nil {
		return err
	}
	records, err := s.store.ListInterruptedCodeFetchRecords(ctx)
	if err != nil {
		return fmt.Errorf("读取中断的代码获取记录失败: %w", err)
	}
	for _, record := range records {
		if err := s.recoverRecord(ctx, record, live); err != nil {
			return fmt.Errorf("收敛代码获取记录 %s 失败: %w", record.ID, err)
		}
	}
	return nil
}

// Stop 不做事：收敛是启动时的一次性动作，没有长期 goroutine。
func (s *CodeFetchRecoveryService) Stop(ctx context.Context) error {
	return nil
}

// recoverRecord 收敛单条中断记录。
func (s *CodeFetchRecoveryService) recoverRecord(
	ctx context.Context,
	record domain.CodeFetchRecord,
	live map[domain.ID]struct{},
) error {
	taskID := domain.ID(record.TaskID)
	_, hasOwner := live[taskID]
	needed, next := domain.DecideCodeFetchRecovery(domain.CodeFetchRecoveryInput{
		Record:    record,
		LiveOwner: hasOwner,
	})
	if !needed {
		return nil
	}
	previous := record.Status
	if err := record.MarkFailed(
		next,
		domain.CodeFetchStagePersist,
		"interrupted",
		domain.CodeFetchInterruptedSummary(),
		s.now(),
	); err != nil {
		return err
	}
	writeCtx := context.WithoutCancel(ctx)
	if err := s.store.UpdateCodeFetchRecordIfStatus(
		writeCtx,
		record,
		[]domain.CodeFetchStatus{previous},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			// 另一个进程已经推进了该记录（例如迟到的 succeeded）：不是失败，
			// 也不重复写入事件，绝不覆盖新结论。
			return nil
		}
		return err
	}
	s.appendRecoveryEvent(writeCtx, record, previous, next)
	return nil
}

// appendRecoveryEvent 记录一次中断收敛事件。
//
// 事件写入失败只记录日志：它不能替代已经落库的收敛结论，也不能让启动失败。
func (s *CodeFetchRecoveryService) appendRecoveryEvent(
	ctx context.Context,
	record domain.CodeFetchRecord,
	previous, next domain.CodeFetchStatus,
) {
	taskID := domain.ID(record.TaskID)
	event, err := domain.NewEvent(
		taskID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalStepExecutionPayload(map[string]any{
			"stage":         "recover_code_fetch",
			"record_id":     record.ID.String(),
			"attempt_id":    record.AttemptID,
			"container_id":  record.ContainerID,
			"task_branch":   record.TaskBranch,
			"previous":      previous.String(),
			"status":        next.String(),
			"summary":       record.ErrorSummary,
			"recovery_auto": true,
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造代码获取收敛事件失败",
			slog.String("record", record.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入代码获取收敛事件失败",
			slog.String("record", record.ID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}
