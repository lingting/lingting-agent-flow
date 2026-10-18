package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 工作流引擎的默认参数。
const (
	// DefaultLeaseTTL 是任务租约的默认有效期。
	DefaultLeaseTTL = 5 * time.Minute
	// DefaultMaxParallel 是单个进程内允许并行推进的任务数量上限。
	DefaultMaxParallel = 4
	// DefaultReviewTimeout 是辅助评估调用的默认超时。
	DefaultReviewTimeout = 20 * time.Minute
)

// Options 是构造工作流引擎的输入。
type Options struct {
	// Store 是持久化入口，必填。
	Store storage.Store
	// Ports 是下层能力集合，必填。
	Ports Ports
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
	// Owner 是执行者标识，用于任务租约；为空时自动生成。
	Owner string
	// LeaseTTL 是租约有效期，零值使用 DefaultLeaseTTL。
	LeaseTTL time.Duration
	// MaxParallel 是并行推进的任务数量上限，零值使用 DefaultMaxParallel。
	MaxParallel int
	// Now 是可注入的时钟，零值使用 time.Now。
	Now func() time.Time
	// ReviewTimeout 是辅助评估调用超时，零值使用 DefaultReviewTimeout。
	ReviewTimeout time.Duration
}

// Engine 是任务执行的控制层。
//
// Engine 不直接实现 SSH、Docker、Pi 或 Git 操作：它只依据持久化状态、验证结果与
// 代码保存结果推进状态机，因此可以在没有真实外部依赖时被完整测试。
type Engine struct {
	store  storage.Store
	ports  Ports
	logger *slog.Logger
	owner  string

	leaseTTL      time.Duration
	maxParallel   int
	now           func() time.Time
	reviewTimeout time.Duration

	// slots 限制单个进程内同时推进的任务数量。
	slots chan struct{}
	// taskLocks 保证同一任务在本进程内不会被两个 goroutine 同时推进。
	taskLocks keyedMutex
}

// NewEngine 构造工作流引擎。
func NewEngine(options Options) (*Engine, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("工作流引擎初始化失败: 存储不能为空")
	}
	if err := validatePorts(options.Ports); err != nil {
		return nil, err
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	owner := strings.TrimSpace(options.Owner)
	if owner == "" {
		owner = "worker-" + domain.NewID().String()
	}
	leaseTTL := options.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = DefaultLeaseTTL
	}
	maxParallel := options.MaxParallel
	if maxParallel <= 0 {
		maxParallel = DefaultMaxParallel
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	reviewTimeout := options.ReviewTimeout
	if reviewTimeout <= 0 {
		reviewTimeout = DefaultReviewTimeout
	}
	return &Engine{
		store:         options.Store,
		ports:         options.Ports,
		logger:        logger,
		owner:         owner,
		leaseTTL:      leaseTTL,
		maxParallel:   maxParallel,
		now:           now,
		reviewTimeout: reviewTimeout,
		slots:         make(chan struct{}, maxParallel),
	}, nil
}

// validatePorts 校验全部下层能力都已提供。
func validatePorts(ports Ports) error {
	switch {
	case ports.Containers == nil:
		return fmt.Errorf("工作流引擎初始化失败: 缺少容器能力")
	case ports.Source == nil:
		return fmt.Errorf("工作流引擎初始化失败: 缺少代码获取能力")
	case ports.Agents == nil:
		return fmt.Errorf("工作流引擎初始化失败: 缺少 Agent 调用能力")
	case ports.Commands == nil:
		return fmt.Errorf("工作流引擎初始化失败: 缺少命令执行能力")
	case ports.CodeSaver == nil:
		return fmt.Errorf("工作流引擎初始化失败: 缺少代码保存能力")
	default:
		return nil
	}
}

// Owner 返回执行者标识。
func (e *Engine) Owner() string {
	return e.owner
}

// MaxParallel 返回并行推进的任务数量上限。
func (e *Engine) MaxParallel() int {
	return e.maxParallel
}

// RunTasks 并行推进多个任务，返回每个任务的结果。
//
// 并行度由引擎配置控制；单个任务失败不会中断其他任务。
func (e *Engine) RunTasks(ctx context.Context, ids []domain.ID) map[domain.ID]error {
	results := make(map[domain.ID]error, len(ids))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, id := range ids {
		mu.Lock()
		results[id] = nil
		mu.Unlock()
		wg.Add(1)
		go func(taskID domain.ID) {
			defer wg.Done()
			err := e.RunTask(ctx, taskID)
			mu.Lock()
			results[taskID] = err
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return results
}

// RunTask 推进一个任务直到它到达稳定状态。
//
// 稳定状态包括终态（completed/failed/cancelled）、recovery_required 与 paused：
// 这些状态不会在本次调用内被继续推进，必须由用户显式重试或恢复。
func (e *Engine) RunTask(ctx context.Context, taskID domain.ID) error {
	if err := taskID.Validate(); err != nil {
		return err
	}
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-e.slots }()

	unlock := e.taskLocks.Lock(taskID.String())
	defer unlock()

	task, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("推进任务失败: %w", err)
	}
	if !task.Status.CanStart() {
		return fmt.Errorf(
			"推进任务失败: 任务状态 %s 不允许开始推进",
			task.Status,
		)
	}
	if err := e.store.AcquireTaskLease(ctx, storage.TaskLeaseRequest{
		TaskID: taskID,
		Owner:  e.owner,
		TTL:    e.leaseTTL,
		At:     e.now(),
	}); err != nil {
		return fmt.Errorf("推进任务失败: 领取任务租约失败: %w", err)
	}
	defer func() {
		// 释放租约失败不影响任务结果：租约本身会过期。
		if releaseErr := e.store.ReleaseTaskLease(
			context.WithoutCancel(ctx),
			taskID,
			e.owner,
			e.now(),
		); releaseErr != nil {
			e.logger.Warn(
				"释放任务租约失败",
				slog.String("task_id", taskID.String()),
				slog.String("error", releaseErr.Error()),
			)
		}
	}()

	task, err = e.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("推进任务失败: %w", err)
	}
	return e.advance(ctx, *task)
}

// advance 按阶段顺序推进任务，直到出现终止条件。
func (e *Engine) advance(ctx context.Context, task domain.Task) error {
	for {
		if err := ctx.Err(); err != nil {
			return e.handleCancel(ctx, task, err)
		}
		if task.CancelRequested {
			return e.handleCancel(ctx, task, nil)
		}
		switch task.Status {
		case domain.TaskStatusQueued, domain.TaskStatusPreparing,
			domain.TaskStatusFailed, domain.TaskStatusPaused,
			domain.TaskStatusRecoveryRequired:
			next, err := e.prepare(ctx, task)
			if err != nil {
				return err
			}
			task = next
		case domain.TaskStatusPlanned, domain.TaskStatusRetrying:
			next, err := e.execute(ctx, task)
			if err != nil {
				return err
			}
			task = next
		case domain.TaskStatusVerifying:
			next, err := e.finalize(ctx, task)
			if err != nil {
				return err
			}
			task = next
		default:
			return nil
		}
		if !task.Status.IsActive() {
			return nil
		}
	}
}

// prepare 完成环境准备、代码获取与规划，把任务推进到 planned。
func (e *Engine) prepare(ctx context.Context, task domain.Task) (domain.Task, error) {
	if err := e.transition(ctx, &task, domain.TaskStatusPreparing); err != nil {
		return task, err
	}
	container, err := e.ensureContainer(ctx, &task)
	if err != nil {
		return task, err
	}
	if err := e.fetchSource(ctx, &task, container); err != nil {
		return task, err
	}
	if err := e.transition(ctx, &task, domain.TaskStatusPlanning); err != nil {
		return task, err
	}
	steps, err := e.plan(ctx, &task, container)
	if err != nil {
		return task, err
	}
	if err := e.storeSteps(ctx, task.ID, steps); err != nil {
		return task, err
	}
	if err := e.transition(ctx, &task, domain.TaskStatusPlanned); err != nil {
		return task, err
	}
	return task, nil
}

// ensureContainer 解析或创建任务容器。
//
// 复用规则：
//   - 只有"继续推进"语义的状态（paused、recovery_required）可以复用同一容器；
//   - 失败或等待中的任务不得静默复用旧容器：必须先通过 task retry 显式处置旧容器，
//     再由工作流创建新容器并重新获取代码；
//   - 已脱离管理或已删除的容器不会被自动接管。
func (e *Engine) ensureContainer(
	ctx context.Context,
	task *domain.Task,
) (ContainerInfo, error) {
	if task.HostID == nil {
		return ContainerInfo{}, e.failTask(
			ctx,
			task,
			"prepare",
			domain.TaskResultFailed,
			"任务未配置执行节点，无法准备容器",
		)
	}
	if existing := strings.TrimSpace(task.ContainerID); existing != "" {
		if !containerReusable(*task) {
			return ContainerInfo{}, e.failTask(
				ctx,
				task,
				"prepare",
				domain.TaskResultFailed,
				"失败任务重新开始必须创建新容器；"+
					"请先执行 task retry 并显式选择旧容器处置方式（delete 或 detach）",
			)
		}
		info, err := e.ports.Containers.Inspect(ctx, existing)
		if err != nil {
			return ContainerInfo{}, e.failTask(
				ctx,
				task,
				"prepare",
				domain.TaskResultFailed,
				"核验任务容器失败: "+err.Error(),
			)
		}
		if info.Detached {
			return ContainerInfo{}, e.failTask(
				ctx,
				task,
				"prepare",
				domain.TaskResultFailed,
				"任务容器已脱离 AgentFlow 管理，拒绝自动接管",
			)
		}
		if !info.Removed {
			if !info.Running {
				return ContainerInfo{}, e.failTask(
					ctx,
					task,
					"prepare",
					domain.TaskResultFailed,
					"任务容器当前不可执行命令",
				)
			}
			return info, nil
		}
	}
	info, err := e.ports.Containers.Create(ctx, ContainerSpec{
		HostID:       task.HostID.String(),
		Ref:          task.Config.Environment,
		VersionLabel: task.Config.EnvironmentVersion,
		ProjectID:    task.Config.ProjectID,
		Caches:       task.Config.Caches,
		WorkDir:      task.Config.EffectiveWorkDir(),
		TaskID:       task.ID,
	})
	if err != nil {
		return ContainerInfo{}, e.failTask(
			ctx,
			task,
			"prepare",
			domain.TaskResultFailed,
			"创建任务容器失败: "+err.Error(),
		)
	}
	if err := task.SetContainer(info.ID); err != nil {
		return ContainerInfo{}, err
	}
	if err := e.persistTask(ctx, *task); err != nil {
		return ContainerInfo{}, err
	}
	if err := e.appendEvent(ctx, *task, nil, nil, nil,
		domain.EventTypeContainerCreated,
		map[string]any{"container_id": info.ID, "docker_id": info.DockerID},
	); err != nil {
		return ContainerInfo{}, err
	}
	return info, nil
}

// containerReusable 判断任务是否允许复用已有容器。
//
// 只有继续推进既有执行的语义允许复用；失败任务的重新开始必须创建新容器。
func containerReusable(task domain.Task) bool {
	switch task.Status {
	case domain.TaskStatusPaused, domain.TaskStatusRecoveryRequired:
		return true
	default:
		return false
	}
}

// fetchSource 在容器内获取代码并记录任务分支信息。
func (e *Engine) fetchSource(
	ctx context.Context,
	task *domain.Task,
	container ContainerInfo,
) error {
	info, err := e.ports.Source.Fetch(ctx, SourceSpec{
		ContainerID:   container.ID,
		TaskID:        task.ID.String(),
		RepositoryURL: task.RepositoryPath,
		WorkDir:       task.Config.EffectiveWorkDir(),
		BaseBranch:    task.BaseRef,
	})
	if err != nil {
		return e.failTask(
			ctx,
			task,
			"source",
			domain.TaskResultFailed,
			"获取源代码失败: "+err.Error(),
		)
	}
	if strings.TrimSpace(info.WorkDir) != "" {
		if err := task.SetWorktreePath(info.WorkDir); err != nil {
			return err
		}
	}
	if err := e.persistTask(ctx, *task); err != nil {
		return err
	}
	return e.appendEvent(ctx, *task, nil, nil, nil,
		domain.EventTypeSourceFetched,
		map[string]any{
			"repository":  info.Repository,
			"base_branch": info.BaseBranch,
			"base_commit": info.BaseCommit,
			"task_branch": info.TaskBranch,
			"commit":      info.Commit,
		},
	)
}

// storeSteps 把计划步骤写入数据库并记录事件。
func (e *Engine) storeSteps(ctx context.Context, taskID domain.ID, steps []domain.Step) error {
	if err := e.store.CreateSteps(ctx, steps); err != nil {
		return fmt.Errorf("写入计划步骤失败: %w", err)
	}
	return nil
}

// execute 按顺序执行开发步骤，并在需要时创建新的尝试。
func (e *Engine) execute(ctx context.Context, task domain.Task) (domain.Task, error) {
	if err := e.transition(ctx, &task, domain.TaskStatusDeveloping); err != nil {
		return task, err
	}
	for {
		steps, err := e.store.ListStepsByTask(ctx, task.ID)
		if err != nil {
			return task, fmt.Errorf("读取计划步骤失败: %w", err)
		}
		if uncertain, needsRecovery := uncertainStep(steps); needsRecovery {
			return task, e.requireRecovery(
				ctx,
				&task,
				"execute",
				"步骤 "+uncertain.Title+" 的执行结果不确定，需要先核验现场",
			)
		}
		step, ok := nextRunnableStep(steps)
		if !ok {
			break
		}
		if err := e.transition(ctx, &task, domain.TaskStatusVerifying); err != nil {
			return task, err
		}
		next, err := e.runStep(ctx, task, step)
		if err != nil {
			return next, err
		}
		task = next
		if !task.Status.IsActive() {
			return task, nil
		}
		if task.Status == domain.TaskStatusRetrying {
			if err := e.transition(ctx, &task, domain.TaskStatusDeveloping); err != nil {
				return task, err
			}
		}
	}
	return e.transitionAndReturn(ctx, task, domain.TaskStatusVerifying)
}

// nextRunnableStep 返回序号最小的可执行步骤。
//
// 结果不确定的步骤（recovery_required）不会被自动重放：必须先核验实际状态，
// 否则会在未知现场上重复执行有副作用的操作。
func nextRunnableStep(steps []domain.Step) (domain.Step, bool) {
	for _, step := range steps {
		switch step.Status {
		case domain.StepStatusPending, domain.StepStatusFailed, domain.StepStatusRetryWait:
			return step, true
		}
	}
	return domain.Step{}, false
}

// uncertainStep 返回第一个需要恢复核验的步骤。
func uncertainStep(steps []domain.Step) (domain.Step, bool) {
	for _, step := range steps {
		if step.Status == domain.StepStatusRecoveryRequired {
			return step, true
		}
	}
	return domain.Step{}, false
}

// transitionAndReturn 迁移任务状态并返回更新后的任务。
func (e *Engine) transitionAndReturn(
	ctx context.Context,
	task domain.Task,
	next domain.TaskStatus,
) (domain.Task, error) {
	if err := e.transition(ctx, &task, next); err != nil {
		return task, err
	}
	return task, nil
}

// transition 迁移任务状态并持久化。
func (e *Engine) transition(
	ctx context.Context,
	task *domain.Task,
	next domain.TaskStatus,
) error {
	if task.Status == next {
		return nil
	}
	previous := task.Status
	if err := task.Transition(next, e.now()); err != nil {
		return fmt.Errorf("任务状态迁移失败: %w", err)
	}
	if err := e.persistTaskIfStatus(ctx, *task, previous); err != nil {
		return err
	}
	eventType := domain.EventTypeTaskStarted
	if next.IsTerminal() {
		eventType = domain.EventTypeTaskCompleted
	}
	return e.appendEvent(ctx, *task, nil, nil, nil, eventType, map[string]any{
		"from": string(previous),
		"to":   string(next),
	})
}

// persistTask 无条件写入任务。
func (e *Engine) persistTask(ctx context.Context, task domain.Task) error {
	if err := e.store.UpdateTask(ctx, task); err != nil {
		return fmt.Errorf("保存任务失败: %w", err)
	}
	return nil
}

// persistTaskIfStatus 以条件更新写入任务，避免过期结果覆盖新状态。
func (e *Engine) persistTaskIfStatus(
	ctx context.Context,
	task domain.Task,
	expected domain.TaskStatus,
) error {
	if err := e.store.UpdateTaskIfStatus(
		ctx,
		task,
		[]domain.TaskStatus{expected},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf(
				"保存任务失败: 任务状态已被其他执行者推进（期望 %s）: %w",
				expected,
				err,
			)
		}
		return fmt.Errorf("保存任务失败: %w", err)
	}
	return nil
}

// appendEvent 追加任务事件。
func (e *Engine) appendEvent(
	ctx context.Context,
	task domain.Task,
	stepID, attemptID, sessionID *domain.ID,
	eventType domain.EventType,
	payload map[string]any,
) error {
	event, err := domain.NewEvent(
		task.ID,
		stepID,
		attemptID,
		sessionID,
		eventType,
		marshalPayload(payload),
		e.now(),
	)
	if err != nil {
		return fmt.Errorf("构造任务事件失败: %w", err)
	}
	if _, err := e.store.AppendEvent(ctx, event); err != nil {
		return fmt.Errorf("写入任务事件失败: %w", err)
	}
	return nil
}

// failTask 把任务推进到失败状态并记录原因。
//
// 只有工作流引擎可以调用：底层组件只报告事实，不决定任务结论。
func (e *Engine) failTask(
	ctx context.Context,
	task *domain.Task,
	stage string,
	result domain.TaskResult,
	summary string,
) error {
	failure := fmt.Errorf("%s: %s", stage, summary)
	if task.Status.IsTerminal() {
		return failure
	}
	if err := task.Finish(result, summary, e.now()); err != nil {
		return errors.Join(failure, err)
	}
	if task.Status != domain.TaskStatusFailed {
		if err := task.Transition(domain.TaskStatusFailed, e.now()); err != nil {
			if !errors.Is(err, domain.ErrInvalidTransition) {
				return errors.Join(failure, err)
			}
		}
	}
	if persistErr := e.store.UpdateTask(ctx, *task); persistErr != nil {
		return errors.Join(failure, fmt.Errorf("保存任务失败: %w", persistErr))
	}
	eventType := domain.EventTypeTaskFailed
	if result == domain.TaskResultCancelled {
		eventType = domain.EventTypeTaskCancelled
	}
	if eventErr := e.appendEvent(ctx, *task, nil, nil, nil, eventType, map[string]any{
		"stage":   stage,
		"summary": summary,
	}); eventErr != nil {
		return errors.Join(failure, eventErr)
	}
	return failure
}

// handleCancel 处理取消请求并收敛任务状态。
//
// 取消不是普通失败：必须先把任务推进到 cancelling，记录取消请求时间，再根据
// 现场处置策略收敛为 cancelled 或 recovery_required。
func (e *Engine) handleCancel(
	ctx context.Context,
	task domain.Task,
	cause error,
) error {
	unlocked := context.WithoutCancel(ctx)
	if !task.CancelRequested {
		if err := task.RequestCancel(e.now()); err != nil {
			return err
		}
	}
	if task.Status != domain.TaskStatusCancelling {
		if err := task.Transition(domain.TaskStatusCancelling, e.now()); err != nil {
			return fmt.Errorf("取消任务失败: %w", err)
		}
	}
	if err := e.persistTask(unlocked, task); err != nil {
		return err
	}
	if err := e.appendEvent(unlocked, task, nil, nil, nil,
		domain.EventTypeTaskCancelRequested,
		map[string]any{"reason": cancelReason(cause)},
	); err != nil {
		return err
	}
	// 中断默认保留容器现场：失败与取消都不删除现场，避免丢失诊断证据。
	if err := task.Finish(
		domain.TaskResultCancelled,
		"任务已按用户请求取消，容器现场保留",
		e.now(),
	); err != nil {
		return err
	}
	if err := task.Transition(domain.TaskStatusCancelled, e.now()); err != nil {
		return fmt.Errorf("取消任务失败: %w", err)
	}
	if err := e.persistTask(unlocked, task); err != nil {
		return err
	}
	return e.appendEvent(unlocked, task, nil, nil, nil,
		domain.EventTypeTaskCancelled,
		map[string]any{"reason": cancelReason(cause)},
	)
}

// cancelReason 返回取消原因摘要。
func cancelReason(cause error) string {
	if cause == nil {
		return "用户请求取消"
	}
	if errors.Is(cause, context.Canceled) {
		return "调用方取消"
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return "调用超时"
	}
	return cause.Error()
}

// agentRunStatus 判断 Agent 调用的运行结果。
func agentSucceeded(result agent.RunResult) bool {
	return result.Status == agent.RunCompleted
}

// sessionFailureSummary 生成会话失败摘要。
func sessionFailureSummary(result agent.RunResult) string {
	if result.Failure != "" {
		return result.Failure
	}
	if result.Status == agent.RunCompleted {
		return ""
	}
	return "Agent 调用未正常完成: " + string(result.Status)
}
