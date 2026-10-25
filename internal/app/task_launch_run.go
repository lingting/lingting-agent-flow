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

// Launch 执行一次统一启动：准备容器 → 生成计划 → 启动工作流。
//
// 语义固定为：
//   - 依据持久化事实决定从哪个阶段开始，已完成的阶段不会重复执行；
//   - 任一阶段失败立即停止，不自动重试、不删除容器、不重放外部操作；
//   - 每一步都在产生远程副作用之前完成必要的状态校验与占用；
//   - 结果如实区分"走到哪个阶段""任务真实状态""计划是否落库""编排是否启动"。
//
// 已存在可用容器时不会创建第二个容器：重复准备只会留下孤儿容器，而孤儿容器既
// 无法被执行链路使用，也不应被自动删除。
func (s *TaskLaunchService) Launch(
	ctx context.Context,
	request TaskLaunchRequest,
) (TaskLaunchResult, error) {
	taskID, err := parseLaunchTaskID(request.TaskID)
	if err != nil {
		return TaskLaunchResult{}, err
	}
	prompt := strings.TrimSpace(request.Prompt)
	if err := validateLaunchOptions(
		prompt,
		request.Timeout,
		request.ValidationTimeout,
		request.PlanTimeout,
	); err != nil {
		return TaskLaunchResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return TaskLaunchResult{}, err
	}
	// 进程内以任务为键拒绝重复请求；跨进程并发仍由各阶段自身的条件更新拒绝。
	unlock, acquired := s.taskLocks.tryLock(task.ID.String())
	if !acquired {
		return TaskLaunchResult{}, fmt.Errorf(
			"%w: 任务 %s 正在启动中",
			ErrTaskLaunchConflict,
			task.ID,
		)
	}
	defer unlock()

	runCtx, finishRun, runErr := s.beginLaunch(ctx)
	if runErr != nil {
		return TaskLaunchResult{}, runErr
	}
	defer finishRun()

	result := TaskLaunchResult{
		TaskID:    task.ID,
		Stage:     domain.TaskLaunchStageValidate,
		StartedAt: s.now(),
	}
	// 调用方未提供执行权凭据时由服务自行领取：任务租约仍是唯一所有权令牌，
	// 只是领取者变成服务本身，而不是另立一套 owner。
	grant, refreshed, releaseGrant, grantErr := s.ensureGrant(runCtx, task, request.Grant)
	if grantErr != nil {
		return s.failLaunch(result, domain.TaskLaunchStageValidate, grantErr)
	}
	task = refreshed
	request.Grant = grant
	// 启动成功时租约由工作流在执行阶段继续持有，失败时立即释放，避免占住名额。
	releaseOwnGrant := true
	defer func() {
		if releaseOwnGrant {
			releaseGrant()
		}
	}()
	if err := request.Grant.Validate(); err != nil {
		return s.failLaunch(result, domain.TaskLaunchStageValidate, fmt.Errorf(
			"%w: %w",
			ErrTaskLaunchPrerequisite,
			err,
		))
	}
	if request.Grant.Lease.TaskID != task.ID {
		return s.failLaunch(result, domain.TaskLaunchStageValidate, fmt.Errorf(
			"%w: 执行权属于任务 %s，与目标任务 %s 不一致",
			ErrTaskLaunchPrerequisite,
			request.Grant.Lease.TaskID,
			task.ID,
		))
	}
	if !domain.TaskHoldsLease(task, request.Grant.Lease, s.now()) {
		return s.failLaunch(result, domain.TaskLaunchStageValidate, fmt.Errorf(
			"%w: 任务 %s 的执行权不属于本次领取（owner %s，代次 %d）",
			ErrTaskLaunchConflict,
			task.ID,
			request.Grant.Lease.Owner,
			request.Grant.Lease.Epoch,
		))
	}
	// 启动阶段可能持续数十分钟：在整段流程中维持任务租约，否则任务会在准备过程中
	// 被其他执行者接管，而本地流程仍在远程执行副作用。
	stopHeartbeat := s.startLeaseHeartbeat(runCtx, request.Grant)
	defer stopHeartbeat()

	input, err := s.loadLaunchInput(runCtx, task)
	if err != nil {
		return s.failLaunch(result, domain.TaskLaunchStageValidate, err)
	}
	stage, err := domain.DecideTaskLaunchNext(input)
	if err != nil {
		return TaskLaunchResult{}, classifyLaunchError(err)
	}
	result.TaskStatus = input.Task.Status
	result.ContainerID = strings.TrimSpace(input.Task.ContainerID)
	// needsPlan 记录本次启动是否真的需要生成计划。
	//
	// 进入 plan 分支可能来自 container 或 workspace 的 fallthrough，而那时计划
	// 可能早已持久化：容器准备与工作区初始化都要求计划尚未生成，但它们的完成
	// 并不蕴含"计划还需要生成"。判定只在 domain 中定义一次，这里只复用它读到的
	// 事实，不重新判断业务语义。
	needsPlan := len(input.Steps) == 0
	// 计划是否已持久化与"本次是否执行了计划生成"是两件事：计划早已落库时启动会
	// 跳过 plan 阶段，但它依然是已持久化的计划。因此这里先按读到的持久化事实给出
	// 结论，只有真正执行了计划生成时才用生成结果覆盖。
	result.PlanPersisted = !needsPlan
	s.appendEvent(runCtx, task.ID, domain.EventTypeTaskStarted, map[string]any{
		"stage":        string(stage),
		"status":       string(input.Task.Status),
		"container_id": result.ContainerID,
		"plan_steps":   len(input.Steps),
		"launch":       "decided",
	})

	switch stage {
	case domain.TaskLaunchStageContainer:
		prepared, prepareErr := s.prepareContainer(runCtx, input.Task)
		result.Stage = domain.TaskLaunchStageContainer
		result.ContainerID = prepared.containerID
		result.ContainerPrepared = prepared.created
		if prepareErr != nil {
			return s.failLaunch(result, domain.TaskLaunchStageContainer, prepareErr)
		}
		// 容器准备成功后任务记录已写回容器关联，重新读取以拿到真实状态。
		refreshed, reloadErr := s.loadTask(runCtx, task.ID)
		if reloadErr != nil {
			return s.failLaunch(result, domain.TaskLaunchStageContainer, reloadErr)
		}
		result.TaskStatus = refreshed.Status
		fallthrough
	case domain.TaskLaunchStageWorkspace:
		initialized, initErr := s.initializeWorkspace(runCtx, task.ID)
		result.Stage = domain.TaskLaunchStageWorkspace
		result.WorkspaceStatus = initialized.Status
		result.WorkspaceCommit = initialized.Commit
		if initErr != nil {
			return s.failLaunch(result, domain.TaskLaunchStageWorkspace, initErr)
		}
		fallthrough
	case domain.TaskLaunchStagePlan:
		if needsPlan {
			planResult, planErr := s.planTask(
				runCtx,
				task.ID,
				prompt,
				request.PlanTimeout,
				request.Grant,
			)
			result.Stage = domain.TaskLaunchStagePlan
			result.PlanSteps = planResult.Steps
			result.PlanPersisted = planResult.Persisted
			result.TaskStatus = planResult.TaskStatus
			if planErr != nil {
				if planResult.FailureStage != "" {
					result.FailureStage = planResult.FailureStage
				}
				return s.failLaunch(result, domain.TaskLaunchStagePlan, planErr)
			}
		}
		fallthrough
	case domain.TaskLaunchStageWorkflow:
		run, startErr := s.startWorkflow(
			runCtx,
			task.ID,
			prompt,
			request.Timeout,
			request.ValidationTimeout,
			request.Grant,
		)
		result.Stage = domain.TaskLaunchStageWorkflow
		result.Run = run
		result.WorkflowStarted = startErr == nil
		if startErr != nil {
			return s.failLaunch(result, domain.TaskLaunchStageWorkflow, startErr)
		}
		// 工作流已经接手任务租约：启动流程不再释放它。
		releaseOwnGrant = false
	default:
		return s.failLaunch(
			result,
			domain.TaskLaunchStageValidate,
			fmt.Errorf("%w: 无法确定启动阶段 %s", ErrTaskLaunchPrerequisite, stage),
		)
	}

	// 收尾阶段读取真实状态：即使前面各阶段都成功，任务状态也只能来自数据库。
	if current, loadErr := s.loadTask(runCtx, task.ID); loadErr == nil {
		result.TaskStatus = current.Status
		result.ContainerID = strings.TrimSpace(current.ContainerID)
	}
	result.FinishedAt = s.now()
	s.logger.InfoContext(
		ctx,
		"任务启动流程已完成",
		slog.String("task_id", task.ID.String()),
		slog.String("stage", string(result.Stage)),
		slog.String("status", string(result.TaskStatus)),
		slog.String("container", result.ContainerID),
		slog.Bool("plan_persisted", result.PlanPersisted),
		slog.Bool("workflow_started", result.WorkflowStarted),
	)
	return result, nil
}

// loadLaunchInput 读取计算启动阶段所需的持久化事实。
//
// 容器可用性必须来自真实核验而不是"任务记录了容器 ID"：容器可能已被删除、停止或
// 脱离管理，凭记录推断会得到错误结论。
func (s *TaskLaunchService) loadLaunchInput(
	ctx context.Context,
	task domain.Task,
) (domain.TaskLaunchInput, error) {
	steps, err := s.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return domain.TaskLaunchInput{}, fmt.Errorf("读取计划步骤失败: %w", err)
	}
	hasActive := false
	if _, runErr := s.store.GetActiveWorkflowRun(ctx, task.ID); runErr == nil {
		hasActive = true
	} else if !errors.Is(runErr, storage.ErrNotFound) {
		return domain.TaskLaunchInput{}, fmt.Errorf("读取工作流执行记录失败: %w", runErr)
	}
	usable := false
	if strings.TrimSpace(task.ContainerID) != "" {
		checked, checkErr := s.containerUsable(ctx, task)
		if checkErr != nil {
			// 核验失败不等于"容器不可用"：远程不可达时创建新容器会让现场更糟。
			// 这种情况按前置条件缺失报告，由用户确认节点与容器状态后再启动。
			return domain.TaskLaunchInput{}, checkErr
		}
		usable = checked
	}
	workspace, err := s.loadWorkspaceState(ctx, task, usable)
	if err != nil {
		return domain.TaskLaunchInput{}, err
	}
	return domain.TaskLaunchInput{
		Task:                 task,
		Steps:                steps,
		HasActiveWorkflowRun: hasActive,
		ContainerUsable:      usable,
		Workspace:            workspace,
	}, nil
}

// loadWorkspaceState 读取当前容器上的任务级工作区初始化现场。
//
// 判定只使用"当前容器代次"的记录：旧容器上的成功记录不适用于新容器，任务重新
// 开始时必须在新容器上重新初始化。
func (s *TaskLaunchService) loadWorkspaceState(
	ctx context.Context,
	task domain.Task,
	containerUsable bool,
) (domain.TaskWorkspaceState, error) {
	state := domain.TaskWorkspaceState{ContainerUsable: containerUsable}
	if !containerUsable {
		return state, nil
	}
	records, err := s.store.ListTaskWorkspaceInitsByTask(ctx, task.ID.String())
	if err != nil {
		return domain.TaskWorkspaceState{}, fmt.Errorf("读取工作区初始化记录失败: %w", err)
	}
	containerID := strings.TrimSpace(task.ContainerID)
	for index := range records {
		if strings.TrimSpace(records[index].ContainerID) != containerID {
			continue
		}
		record := records[index]
		state.Record = &record
		break
	}
	return state, nil
}

// containerUsable 核验任务记录关联的容器当前是否受管理且运行中。
//
// 归属不成立时返回前置条件错误而不是"不可用"：不可用会让启动流程去创建第二个容器，
// 而"任务引用着不属于自己的容器记录"必须由人工核验，不能靠新建容器掩盖。
func (s *TaskLaunchService) containerUsable(
	ctx context.Context,
	task domain.Task,
) (bool, error) {
	if s.containers == nil {
		return false, fmt.Errorf(
			"%w: 本次运行未提供容器核验能力，无法确认任务容器是否可用",
			ErrTaskLaunchPrerequisite,
		)
	}
	owned, err := loadOwnedContainer(ctx, s.store, task)
	if err != nil {
		if errors.Is(err, ErrTaskContainerMissing) {
			return false, nil
		}
		return false, fmt.Errorf("%w: %w", ErrTaskLaunchPrerequisite, err)
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: owned.ID.String(),
		Sync:        true,
	})
	if err != nil {
		return false, fmt.Errorf(
			"%w: 核验任务容器失败: %w",
			ErrTaskLaunchPrerequisite,
			err,
		)
	}
	if detail.Container.Detached {
		return false, nil
	}
	if detail.Container.State == domain.ContainerStateRemoved {
		return false, nil
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return false, nil
	}
	return true, nil
}

// preparedContainer 是一次容器准备的结果。
type preparedContainer struct {
	// containerID 是准备后任务实际使用的容器登记记录 ID。
	containerID string
	// created 表示本次调用真的创建了容器。
	created bool
}

// prepareContainer 依据任务记录准备执行环境。
//
// 已存在可用容器时不会走到这里：判定由 domain.DecideTaskLaunchNext 完成。因此本方法
// 只处理"确实需要准备"的场景，重复调用不会创建第二个容器。
func (s *TaskLaunchService) prepareContainer(
	ctx context.Context,
	task domain.Task,
) (preparedContainer, error) {
	if s.environments == nil {
		return preparedContainer{}, fmt.Errorf(
			"%w: 本次运行未提供环境准备能力，无法为任务 %s 创建容器",
			ErrTaskLaunchPrerequisite,
			task.ID,
		)
	}
	// 创建新容器之前确认任务没有未处置的旧容器：容器停止或状态不明时直接建
	// 第二个容器，会让第一代容器变成永远无法回收的孤儿。旧容器只能由用户显式
	// delete 或 detach，启动流程不替用户处置现场。
	if err := s.ensureNoPendingContainer(ctx, task); err != nil {
		return preparedContainer{}, err
	}
	prepared, err := s.environments.PrepareForTask(ctx, TaskEnvironmentRequest{
		TaskID: task.ID.String(),
	})
	result := preparedContainer{containerID: prepared.Environment.Container().ContainerID}
	result.created = result.containerID != ""
	if err != nil {
		// 准备失败时保留已创建容器的标识：容器可能已经存在，调用方需要能定位它，
		// 而不是只拿到一条没有现场信息的错误。
		return result, fmt.Errorf(
			"%w: 准备任务容器失败: %w",
			ErrTaskLaunchPrerequisite,
			err,
		)
	}
	if !prepared.Succeeded() {
		return result, fmt.Errorf(
			"%w: 准备任务容器未成功（失败阶段 %s）",
			ErrTaskLaunchPrerequisite,
			prepared.FailedStep(),
		)
	}
	return result, nil
}

// initializeWorkspace 在任务容器内初始化代码工作区。
//
// 复用任务级工作区初始化服务：启动流程不构造 Git 命令，也不探测容器文件系统。
// 缺少该能力时返回前置条件错误，而不是跳过初始化去执行一个没有代码的工作区。
func (s *TaskLaunchService) initializeWorkspace(
	ctx context.Context,
	taskID domain.ID,
) (TaskWorkspaceResult, error) {
	if s.workspace == nil {
		return TaskWorkspaceResult{}, fmt.Errorf(
			"%w: 本次运行未提供工作区初始化能力，无法为任务 %s 准备代码工作区",
			ErrTaskLaunchPrerequisite,
			taskID,
		)
	}
	result, err := s.workspace.InitializeTaskWorkspace(ctx, TaskWorkspaceRequest{
		TaskID: taskID.String(),
	})
	if err != nil {
		// 结果无法确认既不是"失败后可以直接重试"，也不是成功：原样上抛，让调用方
		// 按类别映射，并且不继续生成计划。
		return result, fmt.Errorf("%w: %w", ErrTaskLaunchFailed, err)
	}
	return result, nil
}

// planTask 在任务容器内生成并持久化计划。
func (s *TaskLaunchService) planTask(
	ctx context.Context,
	taskID domain.ID,
	prompt string,
	timeout time.Duration,
	grant WorkflowExecutionGrant,
) (PlanGenerationResult, error) {
	if s.planner == nil {
		return PlanGenerationResult{}, fmt.Errorf(
			"%w: 本次运行未提供计划生成能力，无法为任务 %s 生成计划",
			ErrTaskLaunchPrerequisite,
			taskID,
		)
	}
	result, err := s.planner.GeneratePlan(ctx, GeneratePlanRequest{
		TaskID:  taskID.String(),
		Prompt:  prompt,
		Timeout: timeout,
		Grant:   grant,
	})
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrTaskLaunchFailed, err)
	}
	return result, nil
}

// startWorkflow 启动任务级工作流执行。
func (s *TaskLaunchService) startWorkflow(
	ctx context.Context,
	taskID domain.ID,
	prompt string,
	timeout, validationTimeout time.Duration,
	grant WorkflowExecutionGrant,
) (WorkflowRunView, error) {
	if s.workflow == nil {
		return WorkflowRunView{}, fmt.Errorf(
			"%w: 本次运行未提供工作流编排能力，无法启动任务 %s",
			ErrTaskLaunchPrerequisite,
			taskID,
		)
	}
	run, err := s.workflow.StartWorkflow(ctx, StartWorkflowRequest{
		TaskID:            taskID.String(),
		Prompt:            prompt,
		Timeout:           timeout,
		ValidationTimeout: validationTimeout,
		Grant:             grant,
	})
	if err != nil {
		return run, fmt.Errorf("%w: %w", ErrTaskLaunchFailed, err)
	}
	return run, nil
}

// failLaunch 收敛启动失败的结果并如实返回。
//
// 失败不会删除容器、不会重试，也不会留下看起来可用的阶段结论：调用方拿到的
// FailureStage 就是真实停下的阶段。
func (s *TaskLaunchService) failLaunch(
	result TaskLaunchResult,
	stage domain.TaskLaunchStage,
	cause error,
) (TaskLaunchResult, error) {
	summary := errorSummary(cause)
	if summary == "" {
		summary = "启动流程未成功"
	}
	result.Stage = stage
	// 下层服务给出的更具体阶段优先保留：它说明的是真实停在哪一步。
	if result.FailureStage == "" {
		result.FailureStage = stage.String()
	}
	result.Failure = summary
	if result.FinishedAt.IsZero() {
		result.FinishedAt = s.now()
	}
	s.logger.WarnContext(
		context.WithoutCancel(context.Background()),
		"任务启动流程未成功",
		slog.String("task_id", result.TaskID.String()),
		slog.String("stage", stage.String()),
		slog.String("failure_stage", result.FailureStage),
		slog.String("status", string(result.TaskStatus)),
		slog.String("error", summary),
	)
	return result, cause
}

// ensureNoPendingContainer 确认任务没有仍受管理的旧容器记录。
//
// 启动只允许在"任务没有任何可能存在的容器"或"旧容器已被显式处置"时创建新容器：
//   - 停止的容器：工作区可能仍保存未推送的代码；
//   - 状态不明（unknown / creating）的容器：远程可能仍然存在，两代容器会让任务
//     关联只能指向其中一个；
//   - 孤儿容器（登记了任务关联但未被任务引用）：工作区状态未知。
//
// 判定只读：本方法不修改状态，也不执行远程操作。读取失败同样阻止创建：
// 无法判断是否存在旧容器时新建容器会制造无法解释的孤儿。
func (s *TaskLaunchService) ensureNoPendingContainer(
	ctx context.Context,
	task domain.Task,
) error {
	records, err := s.store.ListContainers(ctx, domain.ContainerFilter{
		TaskID: &task.ID,
	})
	if err != nil {
		return fmt.Errorf(
			"%w: 读取任务 %s 的容器记录失败: %w",
			ErrTaskLaunchPrerequisite,
			task.ID,
			err,
		)
	}
	for _, container := range records {
		if container.State == domain.ContainerStateRemoved ||
			container.State == domain.ContainerStateDetached ||
			container.Detached {
			continue
		}
		return fmt.Errorf(
			"%w: 任务 %s 仍有未处置的容器记录 %s（状态 %s）；"+
				"请先执行 agentflow docker container inspect %s 核验现场，"+
				"再显式 delete 或 detach 后重新启动",
			ErrTaskLaunchPrerequisite,
			task.ID,
			container.ID,
			container.State,
			container.ID,
		)
	}
	return nil
}

// startLeaseHeartbeat 在启动期间周期性续租任务租约，返回停止函数。
//
// 续租失败只记录日志并取消启动上下文：本地流程立即停止推进，由上层按真实阶段
// 收敛。未提供续租能力时返回空停止函数——此时执行权在阶段边界仍会被核验，但长
// 阶段可能因租约自然过期而失去执行权，因此生产接线必须提供该能力。
func (s *TaskLaunchService) startLeaseHeartbeat(
	ctx context.Context,
	grant WorkflowExecutionGrant,
) func() {
	if s.leases == nil {
		return func() {}
	}
	interval := s.leases.RenewInterval()
	if interval <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.leases.Renew(ctx, grant.Lease); err != nil {
					s.logger.WarnContext(
						ctx,
						"启动阶段任务租约续期失败",
						slog.String("task_id", grant.Lease.TaskID.String()),
						slog.String("error", errorSummary(err)),
					)
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// ensureGrant 返回本次启动使用的执行权凭据。
//
// 调用方未提供凭据时由启动服务自行领取；提供了凭据时不做任何领取，避免一次启动
// 产生两个 owner。返回的 release 只在凭据由本方法领取时真正释放租约。
func (s *TaskLaunchService) ensureGrant(
	ctx context.Context,
	task domain.Task,
	grant WorkflowExecutionGrant,
) (WorkflowExecutionGrant, domain.Task, func(), error) {
	if grant.Lease.TaskID != "" {
		return grant, task, func() {}, nil
	}
	handle, err := s.store.AcquireTaskLease(ctx, storage.TaskLeaseRequest{
		TaskID:   task.ID,
		Owner:    "launch-" + domain.NewID().String(),
		TTL:      s.leaseTTL(),
		Statuses: domain.ExplicitLaunchStatuses(),
		At:       s.now(),
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return WorkflowExecutionGrant{}, task, nil, fmt.Errorf(
				"%w: 任务 %s 已被其他执行者持有",
				ErrTaskLaunchConflict,
				task.ID,
			)
		}
		return WorkflowExecutionGrant{}, task, nil, fmt.Errorf("领取任务租约失败: %w", err)
	}
	// 重新读取任务：租约刚由本方法写入，调用方手上的快照还没有 owner / epoch，
	// 用它做执行权判定会误判为"不持有"。
	refreshed, loadErr := s.loadTask(ctx, task.ID)
	if loadErr != nil {
		_, releaseErr := s.store.ReleaseTaskLease(context.WithoutCancel(ctx), handle, s.now())
		if releaseErr != nil {
			s.logger.WarnContext(
				ctx,
				"读取任务失败后释放任务租约失败",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(releaseErr)),
			)
		}
		return WorkflowExecutionGrant{}, task, nil, loadErr
	}
	task = refreshed
	acquired := WorkflowExecutionGrant{Lease: handle, TTL: s.leaseTTL()}
	release := func() {
		releaseCtx := context.WithoutCancel(ctx)
		_, releaseErr := s.store.ReleaseTaskLease(releaseCtx, handle, s.now())
		if releaseErr != nil {
			s.logger.WarnContext(
				releaseCtx,
				"释放启动流程自行领取的任务租约失败，名额将受租约有效期约束",
				slog.String("task_id", task.ID.String()),
				slog.String("error", errorSummary(releaseErr)),
			)
		}
	}
	return acquired, task, release, nil
}

// leaseTTL 返回启动流程使用的租约有效期。
//
// 它来自租约服务的建议值：未提供续租能力时取默认有效期，避免无限期占用名额。
func (s *TaskLaunchService) leaseTTL() time.Duration {
	if s.leases != nil {
		if ttl := s.leases.RenewInterval() * 3; ttl > 0 {
			return ttl
		}
	}
	return defaultTaskLeaseTTL
}
