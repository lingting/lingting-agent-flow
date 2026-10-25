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

// 本文件实现"用户显式重试失败任务"的最小闭环。
//
// 职责边界：
//   - 用领域规则（domain.DecideTaskRetryEligibility）判定重试资格，不在本层另写条件；
//   - 用既有任务租约（TaskLeaseService.Claim）取得执行权，成功后把 failed 重开为
//     preparing 并交接给既有统一启动流程（TaskLaunchService.Launch）；
//   - 不新建第二套启动流程：容器准备、工作区初始化、计划生成与工作流启动全部由
//     TaskLaunchService 承担；
//   - 不覆盖历史：Attempt / Session / WorkflowRun / CodeSave 记录一律保留，新执行周期
//     由新的任务租约代次与新的执行记录表达。
//
// 它不实现自动重试：重试必须由用户显式发起，调度器永远只领取 queued。

// 显式重试的应用层错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrTaskRetryNotFound 表示目标任务不存在或已被删除。
	ErrTaskRetryNotFound = errors.New("app: 任务重试目标不存在")
	// ErrTaskRetryConflict 表示当前状态或执行权不允许重试。
	ErrTaskRetryConflict = errors.New("app: 任务重试状态冲突")
	// ErrTaskRetryPrerequisite 表示重试所需的前置条件未满足。
	ErrTaskRetryPrerequisite = errors.New("app: 任务重试前置条件未满足")
	// ErrTaskRetryFailed 表示重试已经真实开始但启动流程没有成功。
	//
	// 它与前三个类别分开表达：任务确实被重开为 preparing，失败发生在之后的某个阶段。
	ErrTaskRetryFailed = errors.New("app: 任务重试流程未成功")
)

// TaskRetryStore 是显式重试依赖的存储能力组合。
//
// 它由 storage.Store 直接满足；测试也可以只实现用到的部分，构造"写入失败"这类真实
// 失败路径而不改动业务代码。
type TaskRetryStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	GetActiveWorkflowRun(ctx context.Context, taskID domain.ID) (*domain.WorkflowRun, error)
	ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
	// UpdateTaskLeaseGuard 在仍持有任务执行权时写入任务状态。
	//
	// 重开任务的写入必须附带 owner + epoch 条件：否则旧代次可以覆盖新领取者的记录。
	UpdateTaskLeaseGuard(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
		handle domain.TaskLeaseHandle,
	) error
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
}

// TaskRetryLeasePort 是显式重试对任务租约能力的需求。
type TaskRetryLeasePort interface {
	Claim(ctx context.Context, request TaskClaimRequest) (TaskClaim, error)
	Release(ctx context.Context, handle domain.TaskLeaseHandle) error
	TTL() time.Duration
	RenewInterval() time.Duration
}

// TaskRetryLaunchPort 是显式重试对统一启动能力的需求。
//
// 重试不重复实现任何启动阶段：它只把重新打开的任务连同执行权交给既有启动流程。
type TaskRetryLaunchPort interface {
	Launch(ctx context.Context, request TaskLaunchRequest) (TaskLaunchResult, error)
}

// TaskRetryOptions 是构造显式重试服务的输入。
type TaskRetryOptions struct {
	// Store 是持久化入口，必填。
	Store TaskRetryStore
	// Leases 提供任务领取与租约维护，必填。
	Leases TaskRetryLeasePort
	// Launch 提供统一启动能力，必填。
	Launch TaskRetryLaunchPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
}

// TaskRetryResult 是一次显式重试的结果。
//
// 结构显式区分四件事：重试前的状态、重开后的状态、新执行代次，以及启动流程的真实结果。
// `WorkflowStarted` 为 false 时任务已进入新周期但没有启动编排，调用方不得把它当作已开始执行。
type TaskRetryResult struct {
	TaskID domain.ID
	// PreviousStatus 是重试前的任务状态。
	PreviousStatus domain.TaskStatus
	// TaskStatus 是重试结束后读取到的任务真实状态。
	TaskStatus domain.TaskStatus
	// Epoch 是本次重试取得的任务领取代次。
	Epoch int64
	// Owner 是本次重试的领取者标识。
	Owner string
	// Launch 是统一启动流程的真实结果。
	Launch TaskLaunchResult
	// WorkflowStarted 表示本次重试真的启动了工作流推进。
	WorkflowStarted bool
	// FailureStage / Failure 是启动流程失败时的阶段与原因摘要。
	FailureStage string
	Failure      string
}

// TaskRetryService 是任务显式重试的唯一应用层入口。
//
// 职责边界：
//   - 读取重试判定所需的持久化事实，并调用 domain.DecideTaskRetryEligibility；
//   - 以任务租约取得执行权后把任务重开为 preparing，再交接给统一启动流程；
//   - 失败如实返回，不自动重试、不删除容器、不重放外部操作。
type TaskRetryService struct {
	store  TaskRetryStore
	leases TaskRetryLeasePort
	launch TaskRetryLaunchPort
	logger *slog.Logger
	now    func() time.Time
}

// NewTaskRetryService 构造显式重试服务。
func NewTaskRetryService(options TaskRetryOptions) (*TaskRetryService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("任务重试服务初始化失败: 存储不能为空")
	}
	if options.Leases == nil {
		return nil, fmt.Errorf("任务重试服务初始化失败: 任务租约服务不能为空")
	}
	if options.Launch == nil {
		return nil, fmt.Errorf("任务重试服务初始化失败: 启动服务不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &TaskRetryService{
		store:  options.Store,
		leases: options.Leases,
		launch: options.Launch,
		logger: logger,
		now:    now,
	}, nil
}

// Retry 显式重试一个失败任务。
//
// 顺序固定为：
//  1. 读取任务、步骤、未结束执行记录与代码保存记录，由领域规则判定资格；
//  2. 以 `failed` 状态集合原子领取任务租约（执行权必须失效才可能领取成功）；
//  3. 重新读取任务并再次判定资格：领取成功不代表现场没有变化；
//  4. 用租约栅栏写入把任务重开为 preparing，并追加可追溯事件；
//  5. 把执行权交接给统一启动流程。
//
// 第 2 步先于第 4 步是关键：租约是唯一所有权令牌，先取得它才能保证"重开任务"与
// "后续启动"由同一个执行代次完成。任一步失败都不会留下"看起来已重试"的状态。
func (s *TaskRetryService) Retry(
	ctx context.Context,
	taskID string,
	request TaskRetryRequest,
) (TaskRetryResult, error) {
	id, err := parseRetryTaskID(taskID)
	if err != nil {
		return TaskRetryResult{}, err
	}
	task, err := s.loadTask(ctx, id)
	if err != nil {
		return TaskRetryResult{}, err
	}
	previous := task.Status
	decision, err := s.decide(ctx, task, "")
	if err != nil {
		return TaskRetryResult{}, err
	}
	if !decision.Allowed {
		return TaskRetryResult{}, classifyRetryRejection(decision)
	}
	claim, err := s.leases.Claim(ctx, TaskClaimRequest{
		TaskID:   task.ID,
		Owner:    "retry-" + domain.NewID().String(),
		Statuses: []domain.TaskStatus{domain.TaskStatusFailed},
	})
	if err != nil {
		return TaskRetryResult{}, classifyRetryClaimError(err)
	}
	result := TaskRetryResult{
		TaskID:         task.ID,
		PreviousStatus: previous,
		Epoch:          claim.Handle.Epoch,
		Owner:          claim.Handle.Owner,
	}
	// 领取成功后任务记录已带租约：后续判定必须基于这份快照，否则会误判"不持有执行权"。
	task = claim.Task
	decision, err = s.decide(ctx, task, claim.Handle.Owner)
	if err != nil {
		s.releaseQuietly(ctx, claim.Handle)
		return result, err
	}
	if !decision.Allowed {
		s.releaseQuietly(ctx, claim.Handle)
		return result, classifyRetryRejection(decision)
	}
	reopened, reopenErr := s.reopen(ctx, task, claim.Handle)
	if reopenErr != nil {
		s.releaseQuietly(ctx, claim.Handle)
		return result, reopenErr
	}
	result.TaskStatus = reopened.Status
	s.appendRetryEvent(ctx, reopened, claim.Handle, previous)
	launchResult, launchErr := s.launch.Launch(ctx, TaskLaunchRequest{
		TaskID:            reopened.ID.String(),
		Prompt:            request.Prompt,
		Timeout:           request.Timeout,
		ValidationTimeout: request.ValidationTimeout,
		PlanTimeout:       request.PlanTimeout,
		Grant: WorkflowExecutionGrant{
			Lease: claim.Handle,
			TTL:   s.leaseTTL(),
		},
	})
	result.Launch = launchResult
	result.WorkflowStarted = launchErr == nil && launchResult.WorkflowStarted
	if launchErr == nil {
		if current, loadErr := s.loadTask(ctx, reopened.ID); loadErr == nil {
			result.TaskStatus = current.Status
		}
		return result, nil
	}
	result.FailureStage = launchResult.FailureStage
	result.Failure = launchResult.Failure
	if result.FailureStage == "" {
		result.FailureStage = launchResult.Stage.String()
	}
	if result.Failure == "" {
		result.Failure = errorSummary(launchErr)
	}
	// 服务停止导致的取消不释放租约：无法确认远程操作是否已经发生，盲目释放会让其他
	// 执行者在未知现场上重放。其余失败释放本次领取，使任务可以再次被显式重试。
	if !isRetryServiceStop(launchErr) {
		s.releaseQuietly(ctx, claim.Handle)
	}
	if current, loadErr := s.loadTask(ctx, reopened.ID); loadErr == nil {
		result.TaskStatus = current.Status
	}
	return result, fmt.Errorf("%w: %w", ErrTaskRetryFailed, launchErr)
}

// TaskRetryRequest 是一次显式重试的执行参数。
//
// 只接受执行参数：任务标识来自路径，要重开哪个周期、用哪些步骤完全由持久化事实决定，
// 调用方不能指定步骤列表、Session 路径、工作目录或角色覆盖。
type TaskRetryRequest struct {
	// Prompt 是可选补充提示词。
	Prompt string
	// Timeout 是单个步骤开发阶段的超时，零值使用任务配置或服务默认值。
	Timeout time.Duration
	// ValidationTimeout 是单个步骤验证阶段的超时。
	ValidationTimeout time.Duration
	// PlanTimeout 是本次 Planner 调用的超时。
	PlanTimeout time.Duration
}

// Eligibility 返回任务的只读重试资格，不写任何状态。
//
// 它用于让用户在执行重试之前看到阻塞项，而不是先收到一个 409。
func (s *TaskRetryService) Eligibility(
	ctx context.Context,
	taskID string,
) (domain.TaskRetryDecision, error) {
	id, err := parseRetryTaskID(taskID)
	if err != nil {
		return domain.TaskRetryDecision{}, err
	}
	task, err := s.loadTask(ctx, id)
	if err != nil {
		return domain.TaskRetryDecision{}, err
	}
	return s.decide(ctx, task, "")
}

// decide 读取重试判定所需的持久化事实并调用领域规则。
//
// 读取失败必须上抛：无法判断是否存在未决副作用时放行重试，会在未知现场上重放外部操作。
func (s *TaskRetryService) decide(
	ctx context.Context,
	task domain.Task,
	owner string,
) (domain.TaskRetryDecision, error) {
	steps, err := s.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return domain.TaskRetryDecision{}, fmt.Errorf("读取计划步骤失败: %w", err)
	}
	var active *domain.WorkflowRun
	run, runErr := s.store.GetActiveWorkflowRun(ctx, task.ID)
	switch {
	case runErr == nil:
		active = run
	case !errors.Is(runErr, storage.ErrNotFound):
		return domain.TaskRetryDecision{}, fmt.Errorf("读取执行记录失败: %w", runErr)
	}
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return domain.TaskRetryDecision{}, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	return domain.DecideTaskRetryEligibility(domain.TaskRetryInput{
		Task:              task,
		ActiveWorkflowRun: active,
		Steps:             steps,
		CodeSaveRecords:   records,
		Now:               s.now(),
		Owner:             owner,
	}), nil
}

// reopen 用租约栅栏把失败任务重开为一次新的执行周期。
func (s *TaskRetryService) reopen(
	ctx context.Context,
	task domain.Task,
	handle domain.TaskLeaseHandle,
) (domain.Task, error) {
	previous := task.Status
	reopened := task
	if err := reopened.ReopenForRetry(s.now()); err != nil {
		return task, fmt.Errorf("%w: %w", ErrTaskRetryConflict, err)
	}
	if err := s.store.UpdateTaskLeaseGuard(
		context.WithoutCancel(ctx),
		reopened,
		[]domain.TaskStatus{previous},
		handle,
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return task, fmt.Errorf(
				"%w: 任务 %s 的状态或执行权已变更，重试未生效",
				ErrTaskRetryConflict,
				task.ID,
			)
		}
		return task, fmt.Errorf("重开任务失败: %w", err)
	}
	return reopened, nil
}

// appendRetryEvent 追加一次可追溯的重试事件。
//
// 事件写入失败只记录日志：状态是唯一真相来源，审计失败不应让一次真实重试被回滚。
func (s *TaskRetryService) appendRetryEvent(
	ctx context.Context,
	task domain.Task,
	handle domain.TaskLeaseHandle,
	previous domain.TaskStatus,
) {
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskRetried,
		marshalStepExecutionPayload(map[string]any{
			"previous_status": string(previous),
			"status":          string(task.Status),
			"owner":           handle.Owner,
			"epoch":           handle.Epoch,
			"retried_at":      s.now().Format(time.RFC3339Nano),
			"source":          "explicit_retry",
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造任务重试事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		s.logger.WarnContext(
			ctx,
			"写入任务重试事件失败",
			slog.String("task_id", task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// releaseQuietly 释放本次领取的租约，失败只记录日志。
//
// 释放冲突说明租约已被接管：那不是错误，也不应覆盖新持有者的记录。
func (s *TaskRetryService) releaseQuietly(ctx context.Context, handle domain.TaskLeaseHandle) {
	releaseCtx := context.WithoutCancel(ctx)
	if err := s.leases.Release(releaseCtx, handle); err != nil {
		s.logger.WarnContext(
			releaseCtx,
			"重试结束后释放任务租约失败，名额将受租约有效期约束",
			slog.String("task_id", handle.TaskID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// leaseTTL 返回重试交接使用的租约有效期。
func (s *TaskRetryService) leaseTTL() time.Duration {
	if interval := s.leases.RenewInterval(); interval > 0 {
		return interval * 3
	}
	return defaultTaskLeaseTTL
}

// loadTask 读取未被逻辑删除的任务。
func (s *TaskRetryService) loadTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrTaskRetryNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrTaskRetryNotFound, id)
	}
	return *task, nil
}

// parseRetryTaskID 解析并校验路径中的任务标识。
func parseRetryTaskID(raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 任务标识不能为空", ErrTaskRetryNotFound)
	}
	id := domain.ID(trimmed)
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrTaskRetryNotFound, err)
	}
	return id, nil
}

// classifyRetryRejection 把领域拒绝结论翻译成应用层错误类别。
//
// 目标或状态问题属于冲突（用户需要换一个任务或先处理现场），前置条件缺失属于 412：
// 把两者压成同一个状态码会让用户误以为"再试一次就能成功"。
func classifyRetryRejection(decision domain.TaskRetryDecision) error {
	summary := domain.TaskRetrySummary(decision)
	switch {
	case decision.Reject(domain.TaskRetryRejectionDeleted):
		return fmt.Errorf("%w: %s", ErrTaskRetryNotFound, summary)
	case decision.Reject(domain.TaskRetryRejectionPrerequisiteMissing):
		return fmt.Errorf("%w: %s", ErrTaskRetryPrerequisite, summary)
	default:
		return fmt.Errorf("%w: %s", ErrTaskRetryConflict, summary)
	}
}

// classifyRetryClaimError 把领取错误映射为可区分的重试错误。
func classifyRetryClaimError(err error) error {
	switch {
	case errors.Is(err, ErrTaskLeaseNotFound):
		return fmt.Errorf("%w: %w", ErrTaskRetryNotFound, err)
	case errors.Is(err, ErrTaskLeaseConflict):
		return fmt.Errorf("%w: %w", ErrTaskRetryConflict, err)
	case errors.Is(err, ErrTaskLeaseInvalid):
		return fmt.Errorf("%w: %w", ErrTaskRetryPrerequisite, err)
	default:
		return fmt.Errorf("领取任务执行权失败: %w", err)
	}
}

// isRetryServiceStop 判断错误是否来自服务停止。
//
// 服务停止与用户取消必须区分：前者不是用户的决定，因此不能推进任务到取消，也不能
// 触发任何自动重试；两者都不能被当作"可以立刻释放现场"。
func isRetryServiceStop(err error) bool {
	return errors.Is(err, ErrTaskLaunchStopped) ||
		errors.Is(err, ErrWorkflowStopped) ||
		errors.Is(err, ErrStepExecutorStopped) ||
		errors.Is(err, ErrSchedulerStopped) ||
		errors.Is(err, context.Canceled)
}
