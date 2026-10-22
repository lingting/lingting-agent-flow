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
	s.appendEvent(ctx, task.ID, nil, nil, domain.EventTypeWorkflowCanceled, map[string]any{
		"run_id": persisted.ID.String(),
		"stage":  string(persisted.Stage),
	})
	// 取消信号通过生命周期登记的在途上下文传播：实际中断由步骤闭环与 Pi Runner 完成。
	s.cancelInFlight(task.ID)
	return workflowRunView(persisted, s.CodeSaverAvailable()), nil
}

// GetWorkflowStatus 返回任务当前的工作流进度。
//
// 优先返回未结束的执行记录；没有未结束记录时返回最近一次执行记录，使"上一次为什么
// 停止"仍然可查询。任务从未启动过工作流时 HasRun 为 false，接口返回真实空状态。
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
	steps, err := s.loadSteps(ctx, task.ID)
	if err != nil {
		return WorkflowStatusResult{}, err
	}
	result := WorkflowStatusResult{
		TaskID:     task.ID,
		TaskStatus: task.Status,
		Steps:      workflowStepViews(steps),
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
		if len(runs) == 0 {
			return result, nil
		}
		latest := runs[0]
		run = &latest
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

// recoverPlanGeneration 在服务启动时处理停留在 planning 的任务。
//
// 进程在 Planner 调用期间退出会留下 planning 任务。这里只依据持久化事实收敛，
// 绝不重放 Planner 调用：存在步骤说明上一轮事务已提交，收敛为 planned；没有任何
// 步骤说明无法判断调用是否真的发生，收敛为 recovery_required 等待人工核验。
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
			s.logger.WarnContext(
				ctx,
				"读取计划步骤失败",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(stepsErr)),
			)
			continue
		}
		next := domain.DecidePlanGenerationRecovery(len(steps) > 0)
		recovered := task
		previous := recovered.Status
		if err := recovered.Transition(next, s.now()); err != nil {
			s.logger.WarnContext(
				ctx,
				"收敛计划生成现场失败",
				slog.String("task_id", task.ID.String()),
				slog.String("target", string(next)),
				slog.String("error", errorSummary(err)),
			)
			continue
		}
		if next == domain.TaskStatusRecoveryRequired {
			recovered.FailureSummary = "计划生成在服务退出时中断，无法确认是否真的调用过 Planner，需要人工核验"
		}
		if err := s.store.UpdateTaskIfStatus(
			context.WithoutCancel(ctx),
			recovered,
			[]domain.TaskStatus{previous},
		); err != nil {
			s.logger.WarnContext(
				ctx,
				"写入计划生成恢复状态失败",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(err)),
			)
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

// recoverUnfinished 在服务启动时处理未结束的执行记录。
//
// 处理方式严格依据数据库事实：
//   - 停留在 running 的记录说明上一次进程已经退出：租约有效期内不动，过期后把现场
//     收敛为 blocked（可恢复）并记录原因，不自动重放 Pi 调用；
//   - pending / blocked / recovery_required 的记录保持原状，等待用户显式恢复；
//   - 已记录取消请求的记录收敛为 cancelled，因为执行者已经不存在。
func (s *TaskWorkflowService) recoverUnfinished(ctx context.Context) error {
	runs, err := s.store.ListUnfinishedWorkflowRuns(ctx)
	if err != nil {
		return fmt.Errorf("恢复未结束工作流失败: %w", err)
	}
	now := s.now()
	for _, run := range runs {
		if err := s.recoverRun(ctx, run, now); err != nil {
			// 单条记录恢复失败不影响服务启动：记录保持原状，用户仍可通过接口查询与处理。
			s.logger.WarnContext(
				ctx,
				"恢复工作流执行记录失败",
				slog.String("run_id", run.ID.String()),
				slog.String("task_id", run.TaskID.String()),
				slog.String("error", errorSummary(err)),
			)
		}
	}
	return nil
}

// recoverRun 处理单条未结束的执行记录。
func (s *TaskWorkflowService) recoverRun(
	ctx context.Context,
	run domain.WorkflowRun,
	now time.Time,
) error {
	switch run.Status {
	case domain.WorkflowRunCancelRequested:
		if err := run.Cancel("取消请求在上一次运行中已记录，执行者退出后确认停止", now); err != nil {
			return err
		}
		if _, err := s.persistRun(context.WithoutCancel(ctx), run); err != nil {
			return err
		}
		s.appendEvent(ctx, run.TaskID, nil, nil, domain.EventTypeWorkflowCompleted, map[string]any{
			"run_id": run.ID.String(),
			"status": string(run.Status),
			"reason": "服务启动时确认取消已生效",
		})
		return nil
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
