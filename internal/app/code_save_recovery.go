package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// CodeSaveRecoveryStore 是代码保存恢复扫描依赖的存储能力。
//
// 只读：扫描不重放任何 Git 操作，也不改写记录的阶段状态。它唯一的写动作是记录
// 无法推进的事实——发现需要人工核验的记录时写一条任务事件，使"任务为什么停在这里"
// 在时间线上可追溯。
type CodeSaveRecoveryStore interface {
	// ListTasks 按筛选条件读取任务，用于定位仍未结束的任务。
	ListTasks(ctx context.Context, filter storage.TaskFilter) ([]domain.Task, error)
	// ListCodeSaveRecordsByTask 按任务列出代码保存记录，最新的在前。
	ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
	// AppendEvent 追加任务事件。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// CodeSaveRecoveryOptions 是构造代码保存恢复扫描服务的输入。
type CodeSaveRecoveryOptions struct {
	// Store 是持久化入口，必填。
	Store CodeSaveRecoveryStore
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// CodeSaveRecoveryService 在服务启动时找出需要人工处理的代码保存记录。
//
// 职责边界（这是本服务刻意不做的事）：
//   - 不重放 commit / push / merge：外部副作用只能由用户显式触发，或由工作流在
//     记录已收敛后重新执行；
//   - 不自动调用只读核验：核验需要建立远程会话，而启动阶段不应把每个任务的容器都
//     连一遍；需要核验的任务由用户用 `agentflow code verify --record <id>` 处理；
//   - 不修改记录的阶段状态与恢复结论：它只读记录并追加一条可追溯的事件。
//
// 它的价值是"不再静默"：进程重启后，仍有结果不明副作用的记录会被显式列出，用户
// 不必先猜到该去哪张表里查。
type CodeSaveRecoveryService struct {
	store  CodeSaveRecoveryStore
	logger *slog.Logger
	now    func() time.Time
}

// NewCodeSaveRecoveryService 构造代码保存恢复扫描服务。
func NewCodeSaveRecoveryService(
	options CodeSaveRecoveryOptions,
) (*CodeSaveRecoveryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("代码保存恢复扫描服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CodeSaveRecoveryService{store: options.Store, logger: logger, now: now}, nil
}

// Name 返回后台服务名称。
func (s *CodeSaveRecoveryService) Name() string {
	return "code_save_recovery"
}

// Start 扫描未结束任务的代码保存记录，并记录需要人工核验的现场。
//
// 扫描是只读的：发现"必须先核验"或"禁止自动重放"的记录时只追加一条事件并写日志，
// 不执行任何 Git 操作，也不推进任务或工作流状态。读取失败让启动失败：无法判断
// 是否存在未决副作用时继续启动，会把不确定性伪装成"没有需要处理的事情"。
func (s *CodeSaveRecoveryService) Start(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{})
	if err != nil {
		return fmt.Errorf("读取任务列表失败: %w", err)
	}
	for _, task := range tasks {
		if task.Status.IsFinished() {
			// 已经结束的任务不再推进：它的记录保持原样，供人工查阅与核验。
			continue
		}
		records, listErr := s.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
		if listErr != nil {
			return fmt.Errorf("读取任务 %s 的代码保存记录失败: %w", task.ID, listErr)
		}
		decision := domain.DecideTaskCodeSaveResume(records)
		if decision.Action == domain.CodeSaveResumeProceed {
			continue
		}
		s.report(ctx, task, records, decision)
	}
	return nil
}

// Stop 不做事：扫描是启动时的一次性动作，没有长期 goroutine。
func (s *CodeSaveRecoveryService) Stop(ctx context.Context) error {
	return nil
}

// report 记录一条"需要人工处理"的现场。
//
// 事件写入失败只记日志：它不影响任何业务状态，也不能让服务启动失败。
func (s *CodeSaveRecoveryService) report(
	ctx context.Context,
	task domain.Task,
	records []domain.CodeSaveRecord,
	decision domain.CodeSaveResumeDecision,
) {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.HasUnresolvedSideEffect() || record.PersistStatus.IsUnconfirmed() {
			ids = append(ids, record.ID.String())
		}
	}
	s.logger.WarnContext(
		ctx,
		"存在需要人工核验的代码保存记录",
		slog.String("task_id", task.ID.String()),
		slog.String("task_status", string(task.Status)),
		slog.String("error_class", decision.ErrorClass),
		slog.String("summary", decision.Summary),
		slog.Any("record_ids", ids),
	)
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeCodeSaveRecoveryRequired,
		marshalStepExecutionPayload(map[string]any{
			"error_class": decision.ErrorClass,
			"summary":     decision.Summary,
			"action":      string(decision.Action),
			"record_ids":  ids,
			"task_status": string(task.Status),
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(ctx, "构造代码保存恢复事件失败", slog.String("error", err.Error()))
		return
	}
	if _, appendErr := s.store.AppendEvent(ctx, event); appendErr != nil {
		s.logger.WarnContext(
			ctx,
			"写入代码保存恢复事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", appendErr.Error()),
		)
	}
}
