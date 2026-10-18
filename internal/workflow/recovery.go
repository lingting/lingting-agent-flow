package workflow

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

// RecoveryReport 是一次恢复核验的结果。
//
// 报告只描述"实际确认到了什么"与"建议的下一步"，不会伪装成任务已完成：
// 无法确认的状态必须保留为 recovery_required。
type RecoveryReport struct {
	// Task 是核验后的任务记录。
	Task domain.Task
	// ContainerState 是核验到的容器状态说明。
	ContainerState string
	// UnfinishedSteps 是仍未完成的步骤标题。
	UnfinishedSteps []string
	// UncertainAttempts 是结果无法确认的尝试标识。
	UncertainAttempts []string
	// MissingSessions 是缺少本机 Session 文件的会话标识。
	MissingSessions []string
	// CodeSaveState 是代码保存状态的说明。
	CodeSaveState string
	// Recommendation 是建议的下一步操作。
	Recommendation string
	// RecoveryRequired 表示任务仍需人工处理。
	RecoveryRequired bool
}

// Recovery 负责核验不确定状态并尝试安全恢复。
//
// 恢复不会盲目重放整个工作流：先逐项核验容器、Git 工作区、推送与合并结果、
// 会话完整性，再决定是继续推进、失败还是保持 recovery_required。
type Recovery struct {
	store  storage.Store
	ports  Ports
	logger *slog.Logger
	now    func() time.Time
}

// RecoveryOptions 是构造恢复核验器的输入。
type RecoveryOptions struct {
	Store  storage.Store
	Ports  Ports
	Logger *slog.Logger
	Now    func() time.Time
}

// NewRecovery 构造恢复核验器。
func NewRecovery(options RecoveryOptions) (*Recovery, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("恢复核验器初始化失败: 存储不能为空")
	}
	if err := validatePorts(options.Ports); err != nil {
		return nil, fmt.Errorf("恢复核验器初始化失败: %w", err)
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Recovery{
		store:  options.Store,
		ports:  options.Ports,
		logger: logger,
		now:    now,
	}, nil
}

// Recover 核验任务的实际状态并给出可诊断结论。
func (r *Recovery) Recover(ctx context.Context, taskID domain.ID) (RecoveryReport, error) {
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return RecoveryReport{}, fmt.Errorf("恢复核验失败: %w", err)
	}
	report := RecoveryReport{Task: *task}
	if err := r.inspectContainer(ctx, task, &report); err != nil {
		return report, err
	}
	if err := r.inspectSteps(ctx, task, &report); err != nil {
		return report, err
	}
	if err := r.inspectSessions(ctx, task, &report); err != nil {
		return report, err
	}
	if err := r.inspectCodeSaves(ctx, task, &report); err != nil {
		return report, err
	}
	report.Recommendation = recommendation(task, report)
	if task.Status.IsTerminal() {
		report.Recommendation = "任务已结束（" + string(task.Status) + "），无需恢复"
		report.RecoveryRequired = false
		return report, nil
	}
	report.RecoveryRequired = true
	if err := r.markRecoveryRequired(ctx, task, report); err != nil {
		return report, err
	}
	updated, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return report, fmt.Errorf("恢复核验失败: %w", err)
	}
	report.Task = *updated
	return report, nil
}

// inspectContainer 核验任务容器的实际状态。
func (r *Recovery) inspectContainer(
	ctx context.Context,
	task *domain.Task,
	report *RecoveryReport,
) error {
	if strings.TrimSpace(task.ContainerID) == "" {
		report.ContainerState = "任务尚未创建容器"
		return nil
	}
	info, err := r.ports.Containers.Inspect(ctx, task.ContainerID)
	if err != nil {
		report.ContainerState = "容器状态无法确认: " + err.Error()
		return nil
	}
	switch {
	case info.Removed:
		report.ContainerState = "容器已在远程删除"
	case info.Detached:
		report.ContainerState = "容器已脱离 AgentFlow 管理，不会再被自动接管"
	case info.Running:
		report.ContainerState = "容器正在运行且受 AgentFlow 管理"
	default:
		report.ContainerState = "容器存在但当前不可执行命令"
	}
	return nil
}

// inspectSteps 核验步骤与尝试的实际结果。
func (r *Recovery) inspectSteps(
	ctx context.Context,
	task *domain.Task,
	report *RecoveryReport,
) error {
	steps, err := r.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("恢复核验失败: 读取步骤失败: %w", err)
	}
	for _, step := range steps {
		if step.Status.IsTerminal() {
			continue
		}
		report.UnfinishedSteps = append(report.UnfinishedSteps, step.Title)
		attempts, attemptErr := r.store.ListAttemptsByStep(ctx, step.ID)
		if attemptErr != nil {
			return fmt.Errorf("恢复核验失败: 读取尝试失败: %w", attemptErr)
		}
		for _, attempt := range attempts {
			if attempt.Status.IsTerminal() {
				continue
			}
			report.UncertainAttempts = append(report.UncertainAttempts, attempt.ID.String())
			// 执行期未结束的尝试不可能仍在运行：控制进程已经退出。
			interrupted := attempt
			if err := interrupted.Interrupt(
				"控制进程退出后核验：该尝试未正常结束",
				r.now(),
			); err != nil && !errors.Is(err, domain.ErrInvalidTransition) {
				return fmt.Errorf("恢复核验失败: %w", err)
			}
			if err := r.store.UpdateAttempt(ctx, interrupted); err != nil {
				return fmt.Errorf("恢复核验失败: 保存尝试失败: %w", err)
			}
		}
	}
	return nil
}

// inspectSessions 核验会话文件的完整性。
//
// 会话记录存在但本机文件缺失时必须如实记录：容器保留不代表 Session 已安全保存。
func (r *Recovery) inspectSessions(
	ctx context.Context,
	task *domain.Task,
	report *RecoveryReport,
) error {
	sessions, err := r.store.ListSessionsByTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("恢复核验失败: 读取会话失败: %w", err)
	}
	for _, session := range sessions {
		if session.Completeness.HasJSONL() {
			continue
		}
		report.MissingSessions = append(report.MissingSessions, session.ID.String())
	}
	return nil
}

// inspectCodeSaves 核验代码保存的最终状态。
func (r *Recovery) inspectCodeSaves(
	ctx context.Context,
	task *domain.Task,
	report *RecoveryReport,
) error {
	records, err := r.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return fmt.Errorf("恢复核验失败: 读取代码保存记录失败: %w", err)
	}
	if len(records) == 0 {
		report.CodeSaveState = "尚未执行代码保存"
		return nil
	}
	latest := records[0]
	report.CodeSaveState = codeSaveSummary(latest)
	if latest.PushStatus == domain.PushStatusUnknown ||
		latest.MergeStatus == domain.MergeStatusUnknown {
		report.CodeSaveState += "（结果不确定，需要先核验远程分支）"
	}
	return nil
}

// markRecoveryRequired 把未结束任务标记为需要恢复核验。
func (r *Recovery) markRecoveryRequired(
	ctx context.Context,
	task *domain.Task,
	report RecoveryReport,
) error {
	if task.Status == domain.TaskStatusRecoveryRequired {
		return nil
	}
	previous := task.Status
	if err := task.Transition(domain.TaskStatusRecoveryRequired, r.now()); err != nil {
		// 状态机不允许时保留原状态，只报告诊断结论，不伪造状态。
		r.logger.Warn(
			"无法把任务标记为需要恢复核验",
			slog.String("task_id", task.ID.String()),
			slog.String("status", string(previous)),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if err := r.store.UpdateTaskIfStatus(
		ctx,
		*task,
		[]domain.TaskStatus{previous},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf("恢复核验失败: 任务状态已被其他执行者推进")
		}
		return fmt.Errorf("恢复核验失败: %w", err)
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRecoveryRequired,
		marshalPayload(map[string]any{
			"container":   report.ContainerState,
			"code_save":   report.CodeSaveState,
			"unfinished":  len(report.UnfinishedSteps),
			"uncertain":   len(report.UncertainAttempts),
			"missing_ssn": len(report.MissingSessions),
		}),
		r.now(),
	)
	if err != nil {
		return fmt.Errorf("恢复核验失败: %w", err)
	}
	if _, err := r.store.AppendEvent(ctx, event); err != nil {
		return fmt.Errorf("恢复核验失败: %w", err)
	}
	return nil
}

// PrepareRestart 为失败任务准备重新执行。
//
// 规则：重新开始必须使用新容器。旧容器必须由用户显式选择删除或脱离管理，
// 本方法不会在缺少授权时自动删除或脱离现场，也不会迁移旧容器中的未推送代码。
func (r *Recovery) PrepareRestart(
	ctx context.Context,
	taskID domain.ID,
	disposition domain.RestartDisposition,
) error {
	if err := disposition.Validate(); err != nil {
		return fmt.Errorf("重新开始失败: %w", err)
	}
	task, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("重新开始失败: %w", err)
	}
	previous := task.Status
	if task.Status.IsTerminal() {
		return fmt.Errorf("重新开始失败: 任务已结束（%s），不能重新执行", task.Status)
	}
	if !task.Status.CanStart() {
		return fmt.Errorf(
			"重新开始失败: 任务状态 %s 正在被推进，不能重新开始",
			task.Status,
		)
	}
	if strings.TrimSpace(task.ContainerID) != "" {
		info, inspectErr := r.ports.Containers.Inspect(ctx, task.ContainerID)
		if inspectErr != nil {
			return fmt.Errorf("重新开始失败: 核验旧容器失败: %w", inspectErr)
		}
		if info.Detached && disposition == domain.RestartDispositionDetach {
			return fmt.Errorf("重新开始失败: 旧容器已脱离管理")
		}
		if !info.Removed && !info.Detached {
			if disposeErr := r.ports.Containers.Dispose(
				ctx,
				task.ContainerID,
				disposition,
			); disposeErr != nil {
				return fmt.Errorf("重新开始失败: 处置旧容器失败: %w", disposeErr)
			}
		}
	}
	// 旧容器不再关联到任务：新执行必须创建新容器并重新获取代码。
	if err := task.SetContainer(""); err != nil {
		return err
	}
	if err := task.SetCurrentStep(nil); err != nil {
		return err
	}
	// 状态保持可开始状态：真正进入 preparing 由工作流引擎在推进时完成。
	task.UpdatedAt = r.now()
	if err := r.store.UpdateTaskIfStatus(ctx, *task, []domain.TaskStatus{previous}); err != nil {
		return fmt.Errorf("重新开始失败: %w", err)
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRetrying,
		marshalPayload(map[string]any{
			"disposition": string(disposition),
			"new_attempt": "will_create_new_container",
		}),
		r.now(),
	)
	if err != nil {
		return fmt.Errorf("重新开始失败: %w", err)
	}
	if _, err := r.store.AppendEvent(ctx, event); err != nil {
		return fmt.Errorf("重新开始失败: %w", err)
	}
	return nil
}

// recommendation 依据核验结论给出建议的下一步操作。
func recommendation(task *domain.Task, report RecoveryReport) string {
	switch {
	case len(report.UncertainAttempts) > 0:
		return "存在结果未知的尝试；核验容器内 Git 状态后再决定是否重试"
	case strings.Contains(report.ContainerState, "脱离"):
		return "容器已脱离管理；如需继续请先创建新任务或显式处置现场"
	case strings.Contains(report.ContainerState, "无法确认"):
		return "先恢复 SSH 连接并核验容器实际状态"
	case task.Status == domain.TaskStatusSaving:
		return "核验远程任务分支与基础分支的实际 commit 后再决定是否重试保存"
	case len(report.MissingSessions) > 0:
		return "部分会话文件缺失；先确认容器内是否仍有可用 Session 再清理容器"
	default:
		return "状态已核验，可执行 task retry 重新开始（必须显式选择旧容器处置方式）"
	}
}
