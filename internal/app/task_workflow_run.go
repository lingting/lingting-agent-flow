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

// StartWorkflowRequest 是通过接口发起一次任务级工作流执行的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务与可选的执行参数，
// 不能指定 Session 路径、工作目录、命令行参数、角色覆盖，也不能指定要执行哪些
// 步骤或改变步骤顺序。
type StartWorkflowRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// Prompt 是可选的补充提示词，会追加在每一步骤的提示词之后。
	Prompt string
	// Timeout 是单个步骤开发阶段的超时，零值使用任务配置或服务默认值。
	Timeout time.Duration
	// ValidationTimeout 是单个步骤验证阶段的超时，零值使用服务默认值。
	ValidationTimeout time.Duration
}

// ResumeWorkflowRequest 是恢复一次已停止工作流执行的输入。
//
// 恢复必须给出 RunID：它让"恢复"明确作用于某一次执行，而不是隐式地重放任务。
type ResumeWorkflowRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// RunID 是待恢复的工作流执行记录标识。
	RunID string
	// Prompt / Timeout / ValidationTimeout 与启动时同义。
	Prompt            string
	Timeout           time.Duration
	ValidationTimeout time.Duration
}

// WorkflowRunView 是一次工作流执行的对外表示。
//
// 它把"编排进度"与"任务状态"分别呈现：调用方不需要再从多个接口拼接进度，也不会
// 把阻塞或需要人工核验误读为成功。
type WorkflowRunView struct {
	RunID  domain.ID
	TaskID domain.ID
	// Status 是工作流执行状态。
	Status domain.WorkflowRunStatus
	// Stage 是当前执行阶段。
	Stage domain.WorkflowStage
	// CurrentStepID / CurrentAttemptID 是当前游标。
	CurrentStepID    *domain.ID
	CurrentAttemptID *domain.ID
	// LastCompletedStepID 是最后一个已确认完成的步骤。
	LastCompletedStepID *domain.ID
	// LastCompletedSequence 是最后一个已确认完成步骤的序号。
	LastCompletedSequence int
	// CompletedSteps / TotalSteps 是进度计数。
	CompletedSteps int
	TotalSteps     int
	// CancelRequested 表示已记录取消请求。
	CancelRequested bool
	// ErrorClass / ErrorSummary 是最近一次停止的原因。
	ErrorClass   string
	ErrorSummary string
	// CodeSaverAvailable 表示本次运行是否具备代码保存能力。
	//
	// 为 false 时任务收尾会停留在 blocked：步骤全部通过验证不等于任务完整成功。
	CodeSaverAvailable bool
	// PlanDigest 是本次执行所用计划的内容摘要。
	PlanDigest string
	// StartedAt / FinishedAt / UpdatedAt 是执行时间。
	StartedAt  *time.Time
	FinishedAt *time.Time
	UpdatedAt  time.Time
	// Revision 是乐观并发版本号。
	Revision int64
}

// WorkflowStatusResult 是任务当前工作流状态查询的结果。
type WorkflowStatusResult struct {
	TaskID domain.ID
	// TaskStatus 是任务的业务状态。
	TaskStatus domain.TaskStatus
	// Run 是当前（或最近一次）执行记录；任务从未启动过工作流时为空值。
	Run WorkflowRunView
	// HasRun 表示是否存在执行记录。
	HasRun bool
	// Steps 是按序号排列的步骤进度快照。
	Steps []WorkflowStepView
}

// WorkflowStepView 是单个步骤在工作流中的进度快照。
//
// 所有字段都来自数据库，不包含推测值：状态为 pending 表示确实还没有执行。
type WorkflowStepView struct {
	StepID   domain.ID
	Sequence int
	Title    string
	Type     domain.StepType
	Status   domain.StepStatus
	// Result / FailureSummary 是步骤的结论与失败原因摘要。
	Result         string
	FailureSummary string
	// CurrentAttemptID 是步骤当前被占用的尝试。
	CurrentAttemptID *domain.ID
	// StartedAt / FinishedAt 是步骤时间。
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// StartWorkflow 为任务创建并推进一次工作流执行。
//
// 语义固定为：
//   - 只执行数据库中已经持久化并通过校验的计划，不生成、不补充计划；
//   - 每次调用创建一条新的执行记录（同一任务已有未结束执行时返回状态冲突）；
//   - 执行在受生命周期管理的后台推进，接口立即返回真实的初始状态；
//   - 不自动重试、不自动修复代码、不自动删除容器。
func (s *TaskWorkflowService) StartWorkflow(
	ctx context.Context,
	request StartWorkflowRequest,
) (WorkflowRunView, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	if err := s.validateExecutionOptions(request.Prompt); err != nil {
		return WorkflowRunView{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	steps, err := s.loadSteps(ctx, task.ID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	if err := domain.CheckWorkflowStart(task, steps); err != nil {
		return WorkflowRunView{}, s.classifyStartError(err)
	}
	if err := s.checkContainerReady(ctx, task); err != nil {
		return WorkflowRunView{}, err
	}
	if err := s.checkHostReady(ctx, task); err != nil {
		return WorkflowRunView{}, err
	}
	// 进程内以任务为键串行化：并发启动不会各自创建一条执行记录。
	// 跨进程并发仍由 workflow_runs 的部分唯一索引拒绝。
	unlock, acquired := s.taskLocks.tryLock(task.ID.String())
	if !acquired {
		return WorkflowRunView{}, fmt.Errorf(
			"%w: 任务 %s 已有工作流在推进",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	launched := false
	defer func() {
		if !launched {
			unlock()
		}
	}()
	run, err := domain.NewWorkflowRun(task.ID, steps, s.now())
	if err != nil {
		return WorkflowRunView{}, fmt.Errorf("%w: %w", ErrWorkflowPrerequisite, err)
	}
	if err := s.store.CreateWorkflowRun(ctx, run); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return WorkflowRunView{}, fmt.Errorf(
				"%w: 任务 %s 已有未结束的工作流执行",
				ErrWorkflowConflict,
				task.ID,
			)
		}
		return WorkflowRunView{}, fmt.Errorf("写入工作流执行记录失败: %w", err)
	}
	// 有副作用的阶段之前先持久化"正在执行"：阶段写入失败时直接返回错误，
	// 不会留下一条看似已启动却没有进度的记录。
	run, err = s.beginRunRecord(ctx, run)
	if err != nil {
		return workflowRunView(run, s.CodeSaverAvailable()), err
	}
	s.appendEvent(ctx, task.ID, nil, nil, domain.EventTypeWorkflowStarted, map[string]any{
		"run_id":      run.ID.String(),
		"steps":       run.TotalSteps,
		"plan_digest": run.PlanDigest,
	})
	s.launch(run, workflowExecution{
		prompt:            request.Prompt,
		timeout:           request.Timeout,
		validationTimeout: request.ValidationTimeout,
	}, unlock)
	launched = true
	return workflowRunView(run, s.CodeSaverAvailable()), nil
}

// ResumeWorkflow 恢复一次已停止的工作流执行。
//
// 恢复只从数据库中已确认的进度继续：已完成的步骤不会被重新执行，结果不确定的现场
// 不会被盲目重放。起点由步骤状态与执行记录游标共同决定。
func (s *TaskWorkflowService) ResumeWorkflow(
	ctx context.Context,
	request ResumeWorkflowRequest,
) (WorkflowRunView, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	runID, err := parseWorkflowID("工作流执行", request.RunID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	if err := s.validateExecutionOptions(request.Prompt); err != nil {
		return WorkflowRunView{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	run, err := s.loadRun(ctx, task.ID, runID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	if !run.Status.CanStart() {
		return WorkflowRunView{}, fmt.Errorf(
			"%w: 工作流执行当前状态 %s 不允许恢复",
			ErrWorkflowConflict,
			run.Status,
		)
	}
	if run.CancelRequested {
		return WorkflowRunView{}, fmt.Errorf(
			"%w: 工作流执行已记录取消请求，不能恢复",
			ErrWorkflowConflict,
		)
	}
	steps, err := s.loadSteps(ctx, task.ID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	if err := s.verifyPlanUnchanged(run, steps); err != nil {
		return WorkflowRunView{}, err
	}
	if err := domain.CheckWorkflowStart(task, steps); err != nil {
		return WorkflowRunView{}, s.classifyStartError(err)
	}
	if err := s.checkContainerReady(ctx, task); err != nil {
		return WorkflowRunView{}, err
	}
	if err := s.checkHostReady(ctx, task); err != nil {
		return WorkflowRunView{}, err
	}
	// 恢复可能与上一次执行的收尾重叠：状态已经落盘为 blocked 时，上一次推进可能仍在
	// 释放任务锁，短暂等待比直接报冲突更符合用户预期；超过时限仍返回冲突。
	unlock, acquired := s.taskLocks.lockWithin(ctx, task.ID.String(), resumeLockTimeout)
	if !acquired {
		return WorkflowRunView{}, fmt.Errorf(
			"%w: 任务 %s 已有工作流在推进",
			ErrWorkflowConflict,
			task.ID,
		)
	}
	launched := false
	defer func() {
		if !launched {
			unlock()
		}
	}()
	// 恢复同样先持久化"正在执行"：写入失败时直接返回错误，不启动后台推进。
	run, err = s.beginRunRecord(ctx, run)
	if err != nil {
		return workflowRunView(run, s.CodeSaverAvailable()), err
	}
	s.appendEvent(ctx, task.ID, nil, nil, domain.EventTypeWorkflowResumed, map[string]any{
		"run_id":          run.ID.String(),
		"completed_steps": run.CompletedSteps,
		"total_steps":     run.TotalSteps,
	})
	s.launch(run, workflowExecution{
		prompt:            request.Prompt,
		timeout:           request.Timeout,
		validationTimeout: request.ValidationTimeout,
		resumed:           true,
	}, unlock)
	launched = true
	return workflowRunView(run, s.CodeSaverAvailable()), nil
}

// CancelWorkflow 请求取消任务当前的工作流执行。
//
// 取消不是"立即停止"：请求先落盘，再向正在执行的步骤传播取消信号，最终状态由真实
// 收敛结果决定。重复请求是幂等的，返回当前状态而不重复改写时间戳。
func (s *TaskWorkflowService) CancelWorkflow(
	ctx context.Context,
	taskID string,
) (WorkflowRunView, error) {
	id, err := parseWorkflowID("任务", taskID)
	if err != nil {
		return WorkflowRunView{}, err
	}
	task, err := s.loadTask(ctx, id)
	if err != nil {
		return WorkflowRunView{}, err
	}
	run, err := s.store.GetActiveWorkflowRun(ctx, task.ID)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return WorkflowRunView{}, fmt.Errorf("读取工作流执行记录失败: %w", err)
		}
		// 没有未结束的执行时，重复取消必须保持幂等：已经取消过的记录直接返回当前
		// 状态，而不是把"重复请求"报成"没有正在执行的工作流"。
		latest, latestErr := s.latestCancelledRun(ctx, task.ID)
		if latestErr != nil {
			return WorkflowRunView{}, latestErr
		}
		if latest == nil {
			return WorkflowRunView{}, fmt.Errorf(
				"%w: 任务 %s 没有正在执行的工作流",
				ErrWorkflowNotFound,
				task.ID,
			)
		}
		return workflowRunView(*latest, s.CodeSaverAvailable()), nil
	}
	if run.Status.IsTerminal() {
		return workflowRunView(*run, s.CodeSaverAvailable()), nil
	}
	if run.CancelRequested {
		// 重复取消：请求已记录，等待实际中断收敛，不改写时间戳。
		return workflowRunView(*run, s.CodeSaverAvailable()), nil
	}
	if err := run.RequestCancel(s.now()); err != nil {
		return WorkflowRunView{}, fmt.Errorf("%w: %w", ErrWorkflowConflict, err)
	}
	if run.Status != domain.WorkflowRunCancelRequested {
		if err := run.Transition(domain.WorkflowRunCancelRequested, s.now()); err != nil {
			return WorkflowRunView{}, fmt.Errorf("%w: %w", ErrWorkflowConflict, err)
		}
	}
	persisted, err := s.persistRun(context.WithoutCancel(ctx), *run)
	if err != nil {
		return workflowRunView(*run, s.CodeSaverAvailable()), err
	}
	// 取消意图必须在两层同时落盘：执行记录层表达"这一次编排要停止"，任务层表达
	// "用户不再希望这个任务继续推进"。只写其中一层时，进程若在两次写入之间退出，
	// 重启后就只能看到一半意图（B5）。
	//
	// 顺序固定为"先执行记录、后任务"：执行记录层是并发闸门，写入成功后其他进程
	// 无法再启动新的编排；任务层写入失败时执行记录层的意图仍然有效，由启动恢复
	// 的取消意图协调补齐（recoverCancellationIntents）。
	s.recordTaskCancelIntent(ctx, task.ID)
	s.appendEvent(ctx, task.ID, nil, nil, domain.EventTypeWorkflowCanceled, map[string]any{
		"run_id": persisted.ID.String(),
		"stage":  string(persisted.Stage),
	})
	// 取消信号通过生命周期登记的在途上下文传播：实际中断由步骤闭环与 Pi Runner 完成。
	s.cancelInFlight(task.ID)
	return workflowRunView(persisted, s.CodeSaverAvailable()), nil
}

// recordTaskCancelIntent 把取消意图写入任务层。
//
// 它只记录意图（`cancel_requested`）并把任务推进到 cancelling，不伪造"已停止"：
// 实际停止由执行者确认，最终 cancelled 由收敛流程写入。
//
// 写入失败不回滚执行记录层的取消请求，也不阻止取消信号传播：意图已经落在执行
// 记录上，启动恢复会依据它补齐任务层。重复请求保持幂等，不改写取消请求时间。
func (s *TaskWorkflowService) recordTaskCancelIntent(ctx context.Context, taskID domain.ID) {
	task, err := s.store.GetTask(context.WithoutCancel(ctx), taskID)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"读取任务失败，取消意图只落在执行记录层",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if task.Status.IsTerminal() {
		// 已结束的任务不再重新打开：它的终态比"取消意图"更晚、更权威。
		return
	}
	now := s.now()
	previous := task.Status
	if !task.CancelRequested {
		if err := task.RequestCancel(now); err != nil {
			s.logger.WarnContext(
				ctx,
				"记录任务取消请求失败",
				slog.String("task_id", taskID.String()),
				slog.String("error", errorSummary(err)),
			)
			return
		}
	}
	if task.Status != domain.TaskStatusCancelling {
		if err := task.Transition(domain.TaskStatusCancelling, now); err != nil {
			// 状态机不允许时只保留取消请求字段：不伪造"已进入取消流程"。
			s.logger.WarnContext(
				ctx,
				"推进任务到 cancelling 被拒绝，仅记录取消请求",
				slog.String("task_id", taskID.String()),
				slog.String("status", string(previous)),
				slog.String("error", errorSummary(err)),
			)
		}
	}
	if err := s.store.UpdateTaskIfStatus(
		context.WithoutCancel(ctx),
		*task,
		[]domain.TaskStatus{previous},
	); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入任务取消意图失败，等待启动恢复补齐",
			slog.String("task_id", taskID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// GetWorkflowStatus 返回任务当前的工作流进度。
//
// 优先返回未结束的执行记录；没有未结束记录时返回最近一次执行记录，使"上一次为什么
// 停止"仍然可查询。任务从未启动过工作流时 HasRun 为 false，接口返回真实空状态。
//
// 读取顺序固定为"执行记录 → 步骤 → 任务"，不能反过来：收尾阶段先写任务终态、
// 再写执行记录终态，若先读任务再读执行记录，一次正在完成的收尾会被读成
// "任务仍在 saving、执行记录已 completed"这种不可能同时成立的组合。按此顺序
// 读取时，看到的执行记录状态一定不新于随后读到的任务与步骤状态。
func (s *TaskWorkflowService) GetWorkflowStatus(
	ctx context.Context,
	taskID string,
) (WorkflowStatusResult, error) {
	id, err := parseWorkflowID("任务", taskID)
	if err != nil {
		return WorkflowStatusResult{}, err
	}
	task, err := s.loadTask(ctx, id)
	if err != nil {
		return WorkflowStatusResult{}, err
	}
	run, err := s.store.GetActiveWorkflowRun(ctx, task.ID)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return WorkflowStatusResult{}, fmt.Errorf("读取工作流执行记录失败: %w", err)
	}
	if run == nil {
		runs, listErr := s.store.ListWorkflowRunsByTask(ctx, task.ID)
		if listErr != nil {
			return WorkflowStatusResult{}, fmt.Errorf("读取工作流执行记录失败: %w", listErr)
		}
		if len(runs) > 0 {
			latest := runs[0]
			run = &latest
		}
	}
	// 任务与步骤在读取执行记录之后读取：见方法注释中的顺序约束。
	current, err := s.loadTask(ctx, task.ID)
	if err != nil {
		return WorkflowStatusResult{}, err
	}
	steps, err := s.loadSteps(ctx, current.ID)
	if err != nil {
		return WorkflowStatusResult{}, err
	}
	result := WorkflowStatusResult{
		TaskID:     current.ID,
		TaskStatus: current.Status,
		Steps:      workflowStepViews(steps),
	}
	if run == nil {
		return result, nil
	}
	result.HasRun = true
	result.Run = workflowRunView(*run, s.CodeSaverAvailable())
	return result, nil
}

// beginRunRecord 把执行记录推进到 running 并持久化。
//
// 它是"先落盘、再执行"的落点：只有状态写入成功，后台推进才会被启动。写入失败时
// 记录保持 pending，调用方拿到明确错误，而不是一条看似已启动却没有进度的记录。
func (s *TaskWorkflowService) beginRunRecord(
	ctx context.Context,
	run domain.WorkflowRun,
) (domain.WorkflowRun, error) {
	if err := run.Start(s.owner, s.lease, s.now()); err != nil {
		return run, fmt.Errorf("%w: %w", ErrWorkflowConflict, err)
	}
	persisted, err := s.persistRun(ctx, run)
	if err != nil {
		return persisted, fmt.Errorf("写入工作流执行状态失败: %w", err)
	}
	return persisted, nil
}

// validateExecutionOptions 校验执行参数。
func (s *TaskWorkflowService) validateExecutionOptions(prompt string) error {
	if len(prompt) > maxWorkflowPrompt {
		return fmt.Errorf(
			"%w: 补充提示词不能超过 %d 个字符",
			ErrWorkflowInvalidInput,
			maxWorkflowPrompt,
		)
	}
	return nil
}

// classifyStartError 把领域校验错误映射为应用层错误类别。
//
// 规则只在 domain 中定义一次：这里只做类别转换，不重新判断业务语义。
func (s *TaskWorkflowService) classifyStartError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrInvalidState) {
		return fmt.Errorf("%w: %w", ErrWorkflowConflict, err)
	}
	return fmt.Errorf("%w: %w", ErrWorkflowPrerequisite, err)
}

// checkContainerReady 核验任务容器仍然受管理且可执行命令。
//
// 容器缺失、已脱离管理或不可运行时返回前置条件错误：编排器不会创建、启动或接管
// 容器，也不会静默回退到本机执行。
func (s *TaskWorkflowService) checkContainerReady(
	ctx context.Context,
	task domain.Task,
) error {
	containerID := strings.TrimSpace(task.ContainerID)
	if containerID == "" {
		return fmt.Errorf(
			"%w: 任务 %s 没有受管理的任务容器",
			ErrWorkflowPrerequisite,
			task.ID,
		)
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: containerID,
		Sync:        true,
	})
	if err != nil {
		return fmt.Errorf(
			"%w: 核验任务容器失败: %w",
			ErrWorkflowPrerequisite,
			err,
		)
	}
	if detail.Container.Detached {
		return fmt.Errorf(
			"%w: 任务容器已脱离 AgentFlow 管理，拒绝自动接管",
			ErrWorkflowPrerequisite,
		)
	}
	if detail.Container.State == domain.ContainerStateRemoved {
		return fmt.Errorf(
			"%w: 任务容器已在远程删除",
			ErrWorkflowPrerequisite,
		)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return fmt.Errorf(
			"%w: 任务容器当前不可执行命令",
			ErrWorkflowPrerequisite,
		)
	}
	return nil
}

// checkHostReady 核验执行节点的环境预检状态。
//
// 判断复用统一的 EnsureHostUsableForRemoteTask：编排器不复制第二套准入规则，也不
// 通过调整工作流绕过 SSHConfig 校验。
func (s *TaskWorkflowService) checkHostReady(
	ctx context.Context,
	task domain.Task,
) error {
	if task.HostID == nil {
		return nil
	}
	host, err := s.store.GetExecutionHost(ctx, *task.HostID)
	if err != nil {
		return fmt.Errorf(
			"%w: 读取执行节点 %s 失败: %w",
			ErrWorkflowPrerequisite,
			*task.HostID,
			err,
		)
	}
	if err := EnsureHostUsableForRemoteTask(*host); err != nil {
		return fmt.Errorf("%w: %w", ErrWorkflowPrerequisite, err)
	}
	return nil
}

// latestCancelledRun 返回最近一次已记录取消请求的执行记录。
//
// 只有"取消请求已经生效"的记录会被返回：其余终态（完成、失败）重复取消仍应按
// 状态冲突处理，而不是伪装成幂等成功。
func (s *TaskWorkflowService) latestCancelledRun(
	ctx context.Context,
	taskID domain.ID,
) (*domain.WorkflowRun, error) {
	runs, err := s.store.ListWorkflowRunsByTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("读取工作流执行记录失败: %w", err)
	}
	if len(runs) == 0 {
		return nil, nil
	}
	latest := runs[0]
	if latest.CancelRequested && latest.Status == domain.WorkflowRunCancelled {
		return &latest, nil
	}
	return nil, nil
}

// loadRun 读取属于指定任务的工作流执行记录。
func (s *TaskWorkflowService) loadRun(
	ctx context.Context,
	taskID, runID domain.ID,
) (domain.WorkflowRun, error) {
	run, err := s.store.GetWorkflowRun(ctx, runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.WorkflowRun{}, fmt.Errorf("%w: 工作流执行 %s", ErrWorkflowNotFound, runID)
		}
		return domain.WorkflowRun{}, fmt.Errorf("读取工作流执行记录失败: %w", err)
	}
	if run.TaskID != taskID {
		// 归属不成立与不存在返回同一错误：不能让调用方通过状态码差异探测其他任务。
		return domain.WorkflowRun{}, fmt.Errorf("%w: 工作流执行 %s", ErrWorkflowNotFound, runID)
	}
	return *run, nil
}

// verifyPlanUnchanged 核验恢复所用的计划与登记时一致。
//
// 计划被重新生成时不能沿用旧游标：那会把新步骤当成"已完成"或跳过必要步骤。
func (s *TaskWorkflowService) verifyPlanUnchanged(
	run domain.WorkflowRun,
	steps []domain.Step,
) error {
	digest, err := domain.WorkflowPlanDigest(steps)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWorkflowPrerequisite, err)
	}
	if digest != run.PlanDigest {
		return fmt.Errorf(
			"%w: 计划内容与执行记录登记时不一致，不能沿用旧进度恢复",
			ErrWorkflowConflict,
		)
	}
	return nil
}

// 启动恢复写入任务状态时使用的固定摘要。
//
// 摘要必须说明"是恢复收敛得出的结论"，否则用户无法区分自动收敛与真实执行结果。
const (
	// planRecoverySummary 说明计划生成现场无法自动确认。
	planRecoverySummary = "计划生成在服务退出时中断，无法确认是否真的调用过 Planner，需要人工核验"
	// cancellingRecoverySummary 说明取消现场已由启动恢复收敛。
	cancellingRecoverySummary = "取消请求在上一次运行中已记录，执行者随进程退出后由启动恢复确认停止"
	// cancelRecoverySummary 说明执行记录层的取消已由启动恢复确认生效。
	cancelRecoverySummary = "取消请求在上一次运行中已记录，执行者退出后确认停止"
)

// recoverPlanGeneration 在服务启动时处理停留在 planning 的任务。
//
// 进程在 Planner 调用期间退出会留下 planning 任务。这里只依据持久化事实收敛，
// 绝不重放 Planner 调用：存在步骤说明上一轮事务已提交，收敛为 planned；没有任何
// 步骤说明无法判断调用是否真的发生，收敛为 recovery_required 等待人工核验。
//
// 任一条记录读取或写入失败都必须让启动失败：恢复结论没有落库时服务继续启动，
// 会让残留现场被当作"已收敛"而无人处理。
func (s *TaskWorkflowService) recoverPlanGeneration(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskStatusPlanning},
	})
	if err != nil {
		return fmt.Errorf("恢复计划生成现场失败: %w", err)
	}
	for _, task := range tasks {
		steps, stepsErr := s.store.ListStepsByTask(ctx, task.ID)
		if stepsErr != nil {
			return fmt.Errorf(
				"恢复任务 %s 的计划生成现场失败: 读取计划步骤失败: %w",
				task.ID,
				stepsErr,
			)
		}
		next := domain.DecidePlanGenerationRecovery(len(steps) > 0)
		recovered := task
		previous := recovered.Status
		if err := recovered.Transition(next, s.now()); err != nil {
			return fmt.Errorf(
				"恢复任务 %s 的计划生成现场失败: 收敛到 %s 被拒绝: %w",
				task.ID,
				next,
				err,
			)
		}
		if next == domain.TaskStatusRecoveryRequired {
			recovered.FailureSummary = planRecoverySummary
		}
		updated, updateErr := s.writeRecoveredTask(
			context.WithoutCancel(ctx),
			recovered,
			previous,
		)
		if updateErr != nil {
			return fmt.Errorf(
				"恢复任务 %s 的计划生成现场失败: 写入任务状态失败: %w",
				task.ID,
				updateErr,
			)
		}
		if !updated {
			// 另一个进程已经完成同一次收敛：这不是失败，本次恢复不再写入事件。
			continue
		}

		eventType := domain.EventTypePlanCreated
		if next == domain.TaskStatusRecoveryRequired {
			eventType = domain.EventTypeTaskRecoveryRequired
		}
		s.appendEvent(ctx, task.ID, nil, nil, eventType, map[string]any{
			"stage":  "recover_plan",
			"status": string(next),
			"steps":  len(steps),
		})
	}
	return nil
}

// recoverCancelling 在服务启动时处理停留在 cancelling 的任务。
//
// 取消请求先落盘再传播，进程可能在传播过程中退出，留下 cancelling 任务。这里同样
// 只依据持久化事实收敛，不重放任何取消传播：
//   - 存在未结束的执行记录：执行者现场无法确认，收敛为 recovery_required；
//   - 没有记录取消请求：无法确认取消流程真的走过，收敛为 recovery_required；
//   - 已记录取消请求且没有未结束的执行记录：执行者已随进程退出，收敛为 cancelled。
//
// 结论由 domain.DecideCancellationRecovery 计算，本方法不复制第二套规则。
func (s *TaskWorkflowService) recoverCancelling(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		Statuses: []domain.TaskStatus{domain.TaskStatusCancelling},
	})
	if err != nil {
		return fmt.Errorf("恢复取消现场失败: %w", err)
	}
	for _, task := range tasks {
		activeRunID, runErr := s.activeRunID(ctx, task.ID)
		if runErr != nil {
			return fmt.Errorf(
				"恢复任务 %s 的取消现场失败: %w",
				task.ID,
				runErr,
			)
		}
		next := domain.DecideCancellationRecovery(domain.CancellationRecoveryInput{
			CancelRequested: task.CancelRequested,
			ActiveRunID:     activeRunID,
		})
		recovered, convergeErr := convergeCancellingTask(task, next, s.now())
		if convergeErr != nil {
			return fmt.Errorf(
				"恢复任务 %s 的取消现场失败: 收敛到 %s 被拒绝: %w",
				task.ID,
				next,
				convergeErr,
			)
		}
		updated, updateErr := s.writeRecoveredTask(
			context.WithoutCancel(ctx),
			recovered,
			domain.TaskStatusCancelling,
		)
		if updateErr != nil {
			return fmt.Errorf(
				"恢复任务 %s 的取消现场失败: 写入任务状态失败: %w",
				task.ID,
				updateErr,
			)
		}
		if !updated {
			// 另一个进程已经完成同一次收敛：这不是失败，本次恢复不再写入事件。
			continue
		}
		eventType := domain.EventTypeTaskCancelled
		if next == domain.TaskStatusRecoveryRequired {
			eventType = domain.EventTypeTaskRecoveryRequired
		}
		s.appendEvent(ctx, task.ID, nil, nil, eventType, map[string]any{
			"stage":             "recover_cancel",
			"status":            string(next),
			"cancel_requested":  task.CancelRequested,
			"active_workflow":   activeRunID != "",
			"cancelling_before": true,
		})
	}
	return nil
}

// writeRecoveredTask 以状态为前置条件写入恢复结论。
//
// 返回 false 表示另一个进程已经完成了同一次收敛：恢复是幂等的收敛操作，竞争结果
// 由数据库状态决定，因此这种情况不算失败，调用方也不应重复写入恢复事件。写入错误
// 仍然必须向上返回，否则"恢复结论没有落库"会被当作已完成。
func (s *TaskWorkflowService) writeRecoveredTask(
	ctx context.Context,
	task domain.Task,
	expected domain.TaskStatus,
) (bool, error) {
	err := s.store.UpdateTaskIfStatus(ctx, task, []domain.TaskStatus{expected})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrConflict) {
		current, readErr := s.store.GetTask(ctx, task.ID)
		if readErr != nil {
			return false, fmt.Errorf(
				"%w: 任务状态已被其他操作推进，且重新读取失败: %v",
				ErrWorkflowConflict,
				readErr,
			)
		}
		if current.Status == task.Status {
			// 状态已经等于目标值：同一次收敛已由其他进程写入。
			return false, nil
		}
		return false, fmt.Errorf(
			"%w: 任务 %s 的状态已被推进到 %s，恢复结论未写入",
			ErrWorkflowConflict,
			task.ID,
			current.Status,
		)
	}
	return false, err
}

// activeRunID 返回任务当前未结束的执行记录标识，没有时返回空字符串。
//
// 读取失败不能被当作"没有未结束记录"：那会让恢复在没有证据的情况下判定取消生效。
func (s *TaskWorkflowService) activeRunID(ctx context.Context, taskID domain.ID) (string, error) {
	run, err := s.store.GetActiveWorkflowRun(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("读取未结束的执行记录失败: %w", err)
	}
	return run.ID.String(), nil
}

// convergeCancellingTask 按状态机把残留 cancelling 任务收敛到目标状态。
//
// 收敛为 cancelled 时先写入取消结论再推进状态：状态进入终态却没有结论会让任务在
// 管理接口里显示为"已取消但没有原因"。
func convergeCancellingTask(
	task domain.Task,
	target domain.TaskStatus,
	at time.Time,
) (domain.Task, error) {
	recovered := task
	switch target {
	case domain.TaskStatusCancelled:
		if !recovered.CancelRequested {
			return task, fmt.Errorf("%w: 任务没有取消请求记录", domain.ErrInvalidState)
		}
		if err := recovered.Finish(
			domain.TaskResultCancelled,
			cancellingRecoverySummary,
			at,
		); err != nil {
			return task, err
		}
		if err := recovered.Transition(domain.TaskStatusCancelled, at); err != nil {
			return task, err
		}
	case domain.TaskStatusRecoveryRequired:
		if err := recovered.Transition(domain.TaskStatusRecoveryRequired, at); err != nil {
			return task, err
		}
		recovered.FailureSummary = cancellingRecoverySummary
	default:
		return task, fmt.Errorf(
			"%w: 取消现场只能收敛为 cancelled 或 recovery_required，收到 %s",
			domain.ErrInvalidState,
			target,
		)
	}
	return recovered, nil
}

// recoverUnfinished 在服务启动时处理未结束的执行记录。
//
// 处理方式严格依据数据库事实：
//   - 停留在 running 的记录说明上一次进程已经退出：租约有效期内不动，过期后把现场
//     收敛为 blocked（可恢复）并记录原因，不自动重放 Pi 调用；
//   - pending / blocked / recovery_required 的记录保持原状，等待用户显式恢复；
//   - 已记录取消请求的记录收敛为 cancelled，因为执行者已经不存在。
//
// 单条记录收敛失败必须让启动失败：记录停留在 running 或 cancel_requested 时服务
// 继续启动，会让"未经处理的在途执行"被当作已恢复。
func (s *TaskWorkflowService) recoverUnfinished(ctx context.Context) error {
	runs, err := s.store.ListUnfinishedWorkflowRuns(ctx)
	if err != nil {
		return fmt.Errorf("恢复未结束工作流失败: %w", err)
	}
	now := s.now()
	for _, run := range runs {
		if err := s.recoverRun(ctx, run, now); err != nil {
			return fmt.Errorf(
				"恢复执行记录 %s（任务 %s）失败: %w",
				run.ID,
				run.TaskID,
				err,
			)
		}
	}
	return nil
}

// recoverCancellationIntents 在服务启动时把执行记录层的取消意图补齐到任务层。
//
// 它覆盖 B5 的故障窗口：取消入口先写执行记录、后写任务层，进程可能停在两次写入
// 之间。此时执行记录已经被 recoverUnfinished 收敛为 cancelled，任务却仍是
// developing 等状态，之后还能被 StartWorkflow 重新推进——用户取消意图就丢失了。
//
// 只处理"最近一次执行记录已记录取消请求、执行记录已不再未结束、任务层还没有
// 取消请求"的任务：其余情况由 recoverRun 与 recoverCancelling 各自负责，避免
// 两条恢复路径互相覆盖。
//
// 任一条任务收敛失败都让启动失败：取消意图没有落库时服务继续启动，会让用户以为
// 取消已经生效。
func (s *TaskWorkflowService) recoverCancellationIntents(ctx context.Context) error {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		ExcludeStatuses: []domain.TaskStatus{
			domain.TaskStatusCompleted,
			domain.TaskStatusCancelled,
		},
	})
	if err != nil {
		return fmt.Errorf("恢复取消意图失败: %w", err)
	}
	for _, task := range tasks {
		if task.CancelRequested {
			continue
		}
		runs, listErr := s.store.ListWorkflowRunsByTask(ctx, task.ID)
		if listErr != nil {
			return fmt.Errorf("恢复任务 %s 的取消意图失败: %w", task.ID, listErr)
		}
		if len(runs) == 0 || !runs[0].CancelRequested {
			continue
		}
		if err := s.convergeCancelIntent(ctx, task.ID); err != nil {
			return fmt.Errorf("恢复任务 %s 的取消意图失败: %w", task.ID, err)
		}
	}
	return nil
}

// recoverRun 处理单条未结束的执行记录。
//
// 取消路径必须同时收敛任务层（B5）：取消入口先写执行记录、后写任务层，进程可能
// 停在两次写入之间。执行记录层确认取消生效后，任务层的取消意图由
// convergeCancelIntent 补齐；该补齐是幂等的，重复执行不会重复改写时间戳。
func (s *TaskWorkflowService) recoverRun(
	ctx context.Context,
	run domain.WorkflowRun,
	now time.Time,
) error {
	switch run.Status {
	case domain.WorkflowRunCancelRequested:
		if err := run.Cancel(cancelRecoverySummary, now); err != nil {
			return err
		}
		persisted, err := s.persistRun(context.WithoutCancel(ctx), run)
		if err != nil {
			return err
		}
		s.appendEvent(ctx, run.TaskID, nil, nil, domain.EventTypeWorkflowCompleted, map[string]any{
			"run_id": persisted.ID.String(),
			"status": string(persisted.Status),
			"reason": "服务启动时确认取消已生效",
		})
		// 取消请求可能已落库但任务层尚未同步（B5）：这里补齐任务层意图。
		return s.convergeCancelIntent(ctx, persisted.TaskID)
	case domain.WorkflowRunRunning:
		if run.LeaseActive(now) {
			// 租约仍然有效：可能有其他进程正在推进，不抢占也不改写。
			return nil
		}
		if err := run.Stop(
			domain.WorkflowRunBlocked,
			"worker_interrupted",
			"上一次执行的进程已经退出，现场需要人工核验后恢复",
			now,
		); err != nil {
			return err
		}
		if _, err := s.persistRun(context.WithoutCancel(ctx), run); err != nil {
			return err
		}
		s.appendEvent(ctx, run.TaskID, nil, nil, domain.EventTypeWorkflowBlocked, map[string]any{
			"run_id":     run.ID.String(),
			"reason":     "服务启动时发现执行中断",
			"stage":      string(run.Stage),
			"step_id":    idString(run.CurrentStepID),
			"attempt_id": idString(run.CurrentAttemptID),
		})
		return nil
	case domain.WorkflowRunPending:
		// 记录已登记但从未真正开始推进（例如阶段写入失败）：租约到期后收敛为可恢复
		// 的 blocked，避免一条永久 pending 的记录阻塞该任务的后续执行。
		if run.UpdatedAt.After(now.Add(-s.lease)) {
			return nil
		}
		if err := run.Stop(
			domain.WorkflowRunBlocked,
			"not_started",
			"上一次登记的工作流执行没有开始推进，可以显式恢复",
			now,
		); err != nil {
			return err
		}
		if _, err := s.persistRun(context.WithoutCancel(ctx), run); err != nil {
			return err
		}
		s.appendEvent(ctx, run.TaskID, nil, nil, domain.EventTypeWorkflowBlocked, map[string]any{
			"run_id": run.ID.String(),
			"reason": "服务启动时发现未开始的执行记录",
		})
		return nil
	default:
		return nil
	}
}

// convergeCancelIntent 把执行记录层的取消意图同步到任务层。
//
// 它处理 B5 的故障窗口：取消入口先写执行记录、后写任务层，进程可能停在两次写入
// 之间，重启后任务仍处于 developing 等状态，看起来可以继续推进——用户取消意图
// 就这样丢失了。
//
// 语义固定为：
//   - 只有执行记录层已记录取消请求、任务层还没有时才会改写任务；
//   - 目标状态由 domain.DecideCancellationRecovery 计算，本方法不复制第二套规则；
//   - 写入使用状态前置条件；另一个进程已完成同一次收敛时按"已完成"处理，不重复
//     写入事件；
//   - 读取或写入失败必须返回错误：取消意图没有落库时不得报告取消已生效。
func (s *TaskWorkflowService) convergeCancelIntent(
	ctx context.Context,
	taskID domain.ID,
) error {
	loaded, err := s.store.GetTask(context.WithoutCancel(ctx), taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// 任务已不存在：没有需要同步的目标，执行记录层的结论仍然有效。
			return nil
		}
		return fmt.Errorf("读取任务 %s 失败: %w", taskID, err)
	}
	task := *loaded
	if task.Deleted() || task.Status.IsTerminal() {
		// 已结束或已删除的任务不再被恢复流程重新打开。
		return nil
	}
	activeRunID, runErr := s.activeRunID(context.WithoutCancel(ctx), task.ID)
	if runErr != nil {
		return fmt.Errorf("读取任务 %s 的未结束执行失败: %w", task.ID, runErr)
	}
	if !domain.DecideCancellationIntentRecovery(domain.CancellationIntentInput{
		TaskCancelRequested: task.CancelRequested,
		RunCancelRequested:  true,
		RunActive:           activeRunID != "",
	}) {
		// 任务层已记录意图，或执行记录仍未结束：不在此处改写任务状态。
		return nil
	}
	next := domain.DecideCancellationRecovery(domain.CancellationRecoveryInput{
		CancelRequested: true,
		ActiveRunID:     activeRunID,
	})
	previous := task.Status
	recovered := task
	if err := recovered.RequestCancel(s.now()); err != nil {
		return fmt.Errorf("恢复任务 %s 的取消请求失败: %w", task.ID, err)
	}
	if next == domain.TaskStatusCancelled {
		if err := recovered.Transition(domain.TaskStatusCancelling, s.now()); err != nil {
			return fmt.Errorf("恢复任务 %s 的取消流程失败: %w", task.ID, err)
		}
		if err := recovered.Finish(
			domain.TaskResultCancelled,
			domain.CancellationIntentRecoverySummary(),
			s.now(),
		); err != nil {
			return fmt.Errorf("恢复任务 %s 的取消结论失败: %w", task.ID, err)
		}
		if err := recovered.Transition(domain.TaskStatusCancelled, s.now()); err != nil {
			return fmt.Errorf("恢复任务 %s 的取消终态失败: %w", task.ID, err)
		}
	} else {
		if err := recovered.Transition(domain.TaskStatusRecoveryRequired, s.now()); err != nil {
			return fmt.Errorf("恢复任务 %s 的取消现场失败: %w", task.ID, err)
		}
		recovered.FailureSummary = domain.CancellationIntentRecoverySummary()
	}
	updated, updateErr := s.writeRecoveredTask(
		context.WithoutCancel(ctx),
		recovered,
		previous,
	)
	if updateErr != nil {
		return fmt.Errorf("恢复任务 %s 的取消意图失败: 写入任务状态失败: %w", task.ID, updateErr)
	}
	if !updated {
		// 另一个进程已经完成同一次收敛：不重复写入事件。
		return nil
	}
	eventType := domain.EventTypeTaskCancelled
	if next != domain.TaskStatusCancelled {
		eventType = domain.EventTypeTaskRecoveryRequired
	}
	s.appendEvent(ctx, task.ID, nil, nil, eventType, map[string]any{
		"stage":            "recover_cancel_intent",
		"status":           string(next),
		"previous":         string(previous),
		"cancel_requested": true,
		"active_workflow":  activeRunID != "",
		"recovery_auto":    true,
	})
	return nil
}

// workflowRunView 把执行记录转换为对外表示。
func workflowRunView(run domain.WorkflowRun, codeSaverAvailable bool) WorkflowRunView {
	return WorkflowRunView{
		RunID:                 run.ID,
		TaskID:                run.TaskID,
		Status:                run.Status,
		Stage:                 run.Stage,
		CurrentStepID:         run.CurrentStepID,
		CurrentAttemptID:      run.CurrentAttemptID,
		LastCompletedStepID:   run.LastCompletedStepID,
		LastCompletedSequence: run.LastCompletedSequence,
		CompletedSteps:        run.CompletedSteps,
		TotalSteps:            run.TotalSteps,
		CancelRequested:       run.CancelRequested,
		ErrorClass:            run.ErrorClass,
		ErrorSummary:          run.ErrorSummary,
		CodeSaverAvailable:    codeSaverAvailable,
		PlanDigest:            run.PlanDigest,
		StartedAt:             run.StartedAt,
		FinishedAt:            run.FinishedAt,
		UpdatedAt:             run.UpdatedAt,
		Revision:              run.Revision,
	}
}

// workflowStepViews 把步骤转换为进度快照。
func workflowStepViews(steps []domain.Step) []WorkflowStepView {
	views := make([]WorkflowStepView, 0, len(steps))
	for _, step := range steps {
		views = append(views, WorkflowStepView{
			StepID:           step.ID,
			Sequence:         step.Sequence,
			Title:            step.Title,
			Type:             step.Type,
			Status:           step.Status,
			Result:           step.Result,
			FailureSummary:   step.FailureSummary,
			CurrentAttemptID: step.CurrentAttemptID,
			StartedAt:        step.StartedAt,
			FinishedAt:       step.FinishedAt,
		})
	}
	return views
}

// idString 返回可空标识的字符串形式，空值返回空字符串。
func idString(id *domain.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
