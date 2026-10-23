package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
	"github.com/lingting/agentflow/internal/storage"
)

// 代码初始化服务的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrCodeInitNotFound 表示任务、步骤或尝试不存在，或不属于同一条任务链。
	ErrCodeInitNotFound = errors.New("app: 代码初始化目标不存在")
	// ErrCodeInitInvalidInput 表示初始化请求参数不合法。
	ErrCodeInitInvalidInput = errors.New("app: 代码初始化请求参数非法")
	// ErrCodeInitConflict 表示当前状态不允许开始初始化，或该尝试已被初始化占用。
	ErrCodeInitConflict = errors.New("app: 代码初始化状态冲突")
	// ErrCodeInitPrerequisite 表示初始化所需的前置条件未满足。
	//
	// 例如任务没有受管理的运行中容器，或容器未登记尝试归属。该错误必须在执行任何
	// Git 命令之前返回，不允许退化为在服务进程的当前目录执行。
	ErrCodeInitPrerequisite = errors.New("app: 代码初始化前置条件未满足")
	// ErrCodeInitFailed 表示初始化已经执行但没有成功。
	//
	// 它携带失败阶段与脱敏原因，调用方据此返回"执行失败"而不是把它当成内部错误。
	ErrCodeInitFailed = errors.New("app: 代码初始化未成功")
	// ErrCodeInitNotPersisted 表示初始化已执行但结果未能可靠持久化。
	//
	// 它绝不能被报告为"代码已就绪"：结论再正确，没有可靠保存也不能当作可用证据。
	ErrCodeInitNotPersisted = errors.New("app: 代码初始化结果未能持久化")
)

// 代码初始化的默认参数。
const (
	// defaultCodeInitTimeout 是整次代码初始化的默认超时。
	//
	// 第一版不做浅克隆，完整历史可能较大，因此比普通查询命令宽松。
	defaultCodeInitTimeout = 20 * time.Minute
	// maxCodeInitTimeout 是允许请求的最大初始化超时。
	maxCodeInitTimeout = 60 * time.Minute
)

// CodeInitRequest 是通过接口发起一次代码初始化的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务、步骤、尝试与可选的
// 执行参数，不能指定容器、Session 路径、命令行参数或凭据。
type CodeInitRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是待执行的步骤标识。
	StepID string
	// AttemptID 是待初始化的执行尝试标识。
	AttemptID string
	// WorkDir 是容器内目标工作目录，空值使用任务配置的目录。
	WorkDir string
	// Timeout 是本次初始化的超时，零值使用默认值。
	Timeout time.Duration
}

// CodeInitResult 是一次代码初始化的结果。
//
// 它显式区分三件事：初始化状态、代码是否就绪、结果是否可靠持久化。
// 三者都不能互相替代：skipped 表示工作区此前已就绪且本次未改动，
// persisted=false 表示记录可能不完整。
type CodeInitResult struct {
	// Record 是本次初始化的执行记录。
	Record domain.CodeFetchRecord
	// RecordID 是本次初始化记录标识。
	RecordID domain.ID
	// TaskID / StepID / AttemptID 是本次初始化的归属。
	TaskID    domain.ID
	StepID    domain.ID
	AttemptID domain.ID
	// Status 是初始化状态。
	Status domain.CodeFetchStatus
	// Succeeded 表示代码已经就绪（succeeded 或 skipped）。
	Succeeded bool
	// ContainerID 是执行本次初始化的容器登记记录 ID。
	ContainerID string
	// DockerID 是 Docker 容器 ID。
	DockerID string
	// WorkDir 是容器内项目目录。
	WorkDir string
	// TaskBranch 是任务分支名。
	TaskBranch string
	// BaseBranch 是识别出的基础分支。
	BaseBranch string
	// Commit 是实际检出的 commit SHA。
	Commit string
	// Stage 是最后一个阶段，失败时即为失败阶段。
	Stage domain.CodeFetchStage
	// ErrorCode 是可诊断的错误类别。
	ErrorCode string
	// Failure 是脱敏后的失败原因。
	Failure string
	// Persisted 表示记录是否全部可靠写入数据库。
	Persisted bool
	// PersistenceErrors 是写入失败的原因。
	PersistenceErrors []string
	StartedAt         time.Time
	FinishedAt        time.Time
}

// CodeInitializeStore 是代码初始化服务依赖的存储能力组合。
type CodeInitializeStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	GetContainer(ctx context.Context, id domain.ID) (*domain.Container, error)
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	storage.CodeFetchStore
}

// CodeInitializeContainerPort 是代码初始化对任务容器的需求。
//
// 容器只被核验与使用：协调器不会创建、启动、删除或接管容器，也不会在容器
// 不可用时静默回退到本机执行。
type CodeInitializeContainerPort interface {
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// FetchSourceWith 用指定代码获取方式执行一次容器内代码获取。
	FetchSourceWith(
		ctx context.Context,
		containerID string,
		request source.FetchRequest,
	) (source.FetchResult, error)
}

// CodeInitializeOptions 是构造代码初始化服务的输入。
type CodeInitializeOptions struct {
	// Store 是持久化入口，必填。
	Store CodeInitializeStore
	// Containers 是容器核验与容器内代码获取能力，必填。
	Containers CodeInitializeContainerPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// DefaultTimeout 覆盖未配置超时时的默认超时。
	DefaultTimeout time.Duration
}

// CodeInitializeService 是代码初始化（代码获取 + 任务分支准备）的协调器。
//
// 职责边界：
//   - 校验任务、步骤、尝试、容器与执行节点的归属与状态；
//   - 先写入初始化记录，再执行容器内 Git 命令，最后写回真实结果；
//   - 同一 Attempt 已经初始化成功时不重新 Clone，只记录 skipped。
//
// 它不实现 Push、Merge、代码保存或容器生命周期，也不决定任务最终是否成功。
type CodeInitializeService struct {
	store          CodeInitializeStore
	containers     CodeInitializeContainerPort
	logger         *slog.Logger
	now            func() time.Time
	defaultTimeout time.Duration
	// running 记录本进程内正在初始化的尝试，用于快速拒绝重复请求。
	//
	// 它是第一道防线而不是一致性保证：真正的幂等边界由数据库记录与目标目录
	// 检查共同承担。
	running *stepExecutionRegistry
}

// NewCodeInitializeService 构造代码初始化协调器。
func NewCodeInitializeService(options CodeInitializeOptions) (*CodeInitializeService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("代码初始化服务初始化失败: 存储不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("代码初始化服务初始化失败: 容器能力不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	timeout := options.DefaultTimeout
	if timeout <= 0 {
		timeout = defaultCodeInitTimeout
	}
	return &CodeInitializeService{
		store:          options.Store,
		containers:     options.Containers,
		logger:         logger,
		now:            now,
		defaultTimeout: timeout,
		running:        newStepExecutionRegistry(),
	}, nil
}

// Initialize 为一个明确的执行尝试准备代码工作区。
//
// 执行顺序固定为：校验归属与状态 → 核验容器与执行节点 → 判定幂等 → 写入记录 →
// 容器内获取代码 → 写回真实结果。任一步失败都会把记录收敛为对应的失败状态，
// 不会因为失败而删除容器或工作区。
func (s *CodeInitializeService) Initialize(
	ctx context.Context,
	request CodeInitRequest,
) (CodeInitResult, error) {
	taskID, err := parseStepExecutionID("任务", request.TaskID)
	if err != nil {
		return CodeInitResult{}, fmt.Errorf("%w: %w", ErrCodeInitInvalidInput, err)
	}
	stepID, err := parseStepExecutionID("步骤", request.StepID)
	if err != nil {
		return CodeInitResult{}, fmt.Errorf("%w: %w", ErrCodeInitInvalidInput, err)
	}
	attemptID, err := parseStepExecutionID("尝试", request.AttemptID)
	if err != nil {
		return CodeInitResult{}, fmt.Errorf("%w: %w", ErrCodeInitInvalidInput, err)
	}
	timeout, err := s.initializeTimeout(request.Timeout)
	if err != nil {
		return CodeInitResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return CodeInitResult{}, err
	}
	step, err := s.loadStep(ctx, task, stepID)
	if err != nil {
		return CodeInitResult{}, err
	}
	attempt, err := s.loadAttempt(ctx, task, step, attemptID)
	if err != nil {
		return CodeInitResult{}, err
	}
	if err := checkCodeInitAllowed(task, attempt); err != nil {
		return CodeInitResult{}, fmt.Errorf("%w: %w", ErrCodeInitConflict, err)
	}
	workDir, err := s.resolveWorkDir(task, request.WorkDir)
	if err != nil {
		return CodeInitResult{}, err
	}
	container, err := s.requireContainer(ctx, task, attempt)
	if err != nil {
		return CodeInitResult{}, err
	}
	release, acquired := s.running.acquire(attempt.ID)
	if !acquired {
		return CodeInitResult{}, fmt.Errorf(
			"%w: 尝试 %s 的代码正在初始化中",
			ErrCodeInitConflict,
			attempt.ID,
		)
	}
	defer release()

	if reused, found, reuseErr := s.reuseInitialized(ctx, task, step, attempt, container, workDir); found {
		return reused, reuseErr
	}
	record, err := s.beginRecord(ctx, task, step, attempt, container, workDir)
	if err != nil {
		return CodeInitResult{}, err
	}
	result, fetchErr := s.runFetch(ctx, runCodeFetchRequest{
		record:    record,
		task:      task,
		step:      step,
		attempt:   attempt,
		container: container,
		workDir:   workDir,
		timeout:   timeout,
	})
	if fetchErr != nil {
		return result, fetchErr
	}
	return result, nil
}

// runCodeFetchRequest 是一次真实代码获取的输入。
type runCodeFetchRequest struct {
	record    domain.CodeFetchRecord
	task      domain.Task
	step      domain.Step
	attempt   domain.Attempt
	container domain.Container
	workDir   string
	timeout   time.Duration
}

// runFetch 执行一次容器内代码获取并写回结果。
//
// 无论获取以何种方式结束，都会返回一个结果对象：失败、超时与取消不是"没有结果"，
// 而是有明确结论的结果。持久化错误不会阻止返回值，而是记录在 PersistenceErrors 中，
// 使调用方看到"执行事实"与"状态写入是否完整"两个独立事实。
func (s *CodeInitializeService) runFetch(
	ctx context.Context,
	request runCodeFetchRequest,
) (CodeInitResult, error) {
	fetchRequest := source.FetchRequest{
		ProjectID:   request.task.Config.ProjectID,
		TaskID:      request.task.ID.String(),
		AttemptID:   request.attempt.ID.String(),
		SourceType:  domain.CodeSourceGit,
		Repository:  request.task.RepositoryPath,
		WorkDir:     request.workDir,
		BaseBranch:  request.task.BaseRef,
		ContainerID: request.container.ID.String(),
		Timeout:     request.timeout,
	}
	fetched, fetchErr := s.containers.FetchSourceWith(
		ctx,
		request.container.ID.String(),
		fetchRequest,
	)
	result := CodeInitResult{
		Record:      request.record,
		RecordID:    request.record.ID,
		TaskID:      request.task.ID,
		StepID:      request.step.ID,
		AttemptID:   request.attempt.ID,
		ContainerID: request.container.ID.String(),
		DockerID:    request.container.DockerID,
		WorkDir:     request.workDir,
		TaskBranch:  request.record.TaskBranch,
		StartedAt:   fetched.StartedAt,
		FinishedAt:  fetched.FinishedAt,
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = request.record.StartedAt
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = s.now()
	}
	record := request.record
	var markErr error
	if fetchErr == nil && fetched.Succeeded() {
		markErr = record.MarkSucceeded(
			fetched.BaseBranch,
			fetched.BaseSelection,
			fetched.BaseCommit,
			fetched.TaskBranchSource.String(),
			fetched.Commit,
			result.FinishedAt,
		)
	} else {
		status := codeFetchStatusOf(fetchErr, ctx)
		stage := fetched.Stage
		if stage == "" {
			stage = domain.CodeFetchStageClone
		}
		summary := fetched.ErrorSummary
		if summary == "" {
			summary = errorSummary(fetchErr)
		}
		if summary == "" {
			summary = "代码获取未成功"
		}
		markErr = record.MarkFailed(status, stage, fetched.ErrorCode, summary, result.FinishedAt)
	}
	persisted, persistErr := s.persistRecord(ctx, record, markErr)
	result.Record = persisted
	result.Status = persisted.Status
	result.Succeeded = persisted.Succeeded()
	result.BaseBranch = persisted.BaseBranch
	result.Commit = persisted.CommitSHA
	result.Stage = persisted.Stage
	result.ErrorCode = persisted.ErrorCode
	result.Failure = persisted.ErrorSummary
	result.Persisted = persistErr == nil
	if persistErr != nil {
		result.PersistenceErrors = []string{persistErr.Error()}
	}
	s.appendEvent(ctx, request, persisted)
	s.logResult(ctx, result)
	if persistErr != nil {
		return result, errors.Join(ErrCodeInitNotPersisted, persistErr)
	}
	if !result.Succeeded {
		return result, fmt.Errorf(
			"%w（%s，阶段 %s）: %s",
			ErrCodeInitFailed,
			result.Status,
			result.Stage,
			result.Failure,
		)
	}
	return result, nil
}

// persistRecord 写入初始化记录并返回写入后的记录。
//
// 领域状态迁移失败时仍尝试写入，使"执行已发生但记录不完整"可以被观察到，
// 而不是把失败原因彻底丢失。
func (s *CodeInitializeService) persistRecord(
	ctx context.Context,
	record domain.CodeFetchRecord,
	markErr error,
) (domain.CodeFetchRecord, error) {
	writeCtx := context.WithoutCancel(ctx)
	var errs []error
	if markErr != nil {
		errs = append(errs, fmt.Errorf("标记代码初始化结果失败: %w", markErr))
	}
	if updateErr := s.store.UpdateCodeFetchRecord(writeCtx, record); updateErr != nil {
		errs = append(errs, fmt.Errorf("写入代码初始化记录失败: %w", updateErr))
	}
	if len(errs) == 0 {
		return record, nil
	}
	return record, errors.Join(errs...)
}

// reuseInitialized 判断该 Attempt 是否已经完成过初始化。
//
// 已经成功初始化时不重新 Clone：重新 Clone 会覆盖容器内可能已经产生的开发成果。
// 此时写入一条 skipped 记录并返回上次核验到的 commit。
func (s *CodeInitializeService) reuseInitialized(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	container domain.Container,
	workDir string,
) (CodeInitResult, bool, error) {
	records, err := s.store.ListCodeFetchRecordsByAttempt(ctx, attempt.ID.String())
	if err != nil {
		return CodeInitResult{}, false, fmt.Errorf("读取代码初始化记录失败: %w", err)
	}
	var previous *domain.CodeFetchRecord
	for index := range records {
		record := records[index]
		if !record.Succeeded() {
			continue
		}
		if strings.TrimSpace(record.ContainerID) != container.ID.String() {
			// 旧容器上的成功记录不适用于当前容器：重新开始任务必须创建新容器，
			// 旧容器内的代码不会被迁移过来。
			continue
		}
		previous = &record
		break
	}
	if previous == nil {
		return CodeInitResult{}, false, nil
	}
	record, err := s.beginRecord(ctx, task, step, attempt, container, workDir)
	if err != nil {
		return CodeInitResult{}, true, err
	}
	finishedAt := s.now()
	if markErr := record.MarkSkipped(previous.CommitSHA, finishedAt); markErr != nil {
		return CodeInitResult{}, true, fmt.Errorf("标记代码初始化结果失败: %w", markErr)
	}
	persisted, persistErr := s.persistRecord(ctx, record, nil)
	result := CodeInitResult{
		Record:      persisted,
		RecordID:    persisted.ID,
		TaskID:      task.ID,
		StepID:      step.ID,
		AttemptID:   attempt.ID,
		Status:      persisted.Status,
		Succeeded:   persisted.Succeeded(),
		ContainerID: container.ID.String(),
		DockerID:    container.DockerID,
		WorkDir:     workDir,
		TaskBranch:  persisted.TaskBranch,
		BaseBranch:  persisted.BaseBranch,
		Commit:      persisted.CommitSHA,
		Stage:       persisted.Stage,
		Persisted:   persistErr == nil,
		StartedAt:   persisted.StartedAt,
		FinishedAt:  finishedAt,
	}
	if persistErr != nil {
		result.PersistenceErrors = []string{persistErr.Error()}
	}
	s.debug(
		ctx,
		"代码初始化已跳过：该尝试的工作区此前已就绪",
		slog.String("attempt_id", attempt.ID.String()),
		slog.String("commit", persisted.CommitSHA),
	)
	return result, true, nil
}

// beginRecord 写入一条 pending 记录并推进到 running。
//
// 记录先于任何容器内命令写入：执行失败也要留下可追踪的痕迹，而不是只有一条
// 无法定位阶段的错误。
func (s *CodeInitializeService) beginRecord(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
	container domain.Container,
	workDir string,
) (domain.CodeFetchRecord, error) {
	record, err := domain.NewCodeFetchRecord(
		"",
		task.Config.ProjectID,
		task.ID.String(),
		step.ID.String(),
		attempt.ID.String(),
		domain.CodeSourceGit,
		workDir,
		container.ID.String(),
		container.DockerID,
		s.now(),
	)
	if err != nil {
		return domain.CodeFetchRecord{}, fmt.Errorf("%w: %w", ErrCodeInitInvalidInput, err)
	}
	if err := s.store.CreateCodeFetchRecord(ctx, record); err != nil {
		return domain.CodeFetchRecord{}, fmt.Errorf(
			"%w: 写入代码初始化记录失败: %w",
			ErrCodeInitNotPersisted,
			err,
		)
	}
	if err := record.MarkRunning(domain.CodeFetchStageProbe, s.now()); err != nil {
		return domain.CodeFetchRecord{}, fmt.Errorf("标记代码初始化开始失败: %w", err)
	}
	if err := s.store.UpdateCodeFetchRecord(ctx, record); err != nil {
		return domain.CodeFetchRecord{}, fmt.Errorf(
			"%w: 写入代码初始化记录失败: %w",
			ErrCodeInitNotPersisted,
			err,
		)
	}
	return record, nil
}

// loadTask 读取任务，不存在或已删除时返回"不存在"。
func (s *CodeInitializeService) loadTask(
	ctx context.Context,
	id domain.ID,
) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrCodeInitNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrCodeInitNotFound, id)
	}
	return *task, nil
}

// loadStep 读取步骤并确认它属于指定任务。
func (s *CodeInitializeService) loadStep(
	ctx context.Context,
	task domain.Task,
	id domain.ID,
) (domain.Step, error) {
	step, err := s.store.GetStep(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrCodeInitNotFound, id)
		}
		return domain.Step{}, fmt.Errorf("读取步骤失败: %w", err)
	}
	if step.TaskID != task.ID {
		// 归属不成立与不存在返回同一错误：不能让调用方通过状态码差异探测其他任务。
		return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrCodeInitNotFound, id)
	}
	return *step, nil
}

// loadAttempt 读取尝试并确认它属于指定任务与步骤。
func (s *CodeInitializeService) loadAttempt(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	id domain.ID,
) (domain.Attempt, error) {
	attempt, err := s.store.GetAttempt(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Attempt{}, fmt.Errorf("%w: 尝试 %s", ErrCodeInitNotFound, id)
		}
		return domain.Attempt{}, fmt.Errorf("读取执行尝试失败: %w", err)
	}
	if attempt.StepID != step.ID || attempt.TaskID != task.ID {
		return domain.Attempt{}, fmt.Errorf("%w: 尝试 %s", ErrCodeInitNotFound, id)
	}
	return *attempt, nil
}

// requireContainer 核验执行节点与任务容器，并确认容器登记了本次尝试。
//
// 容器必须带有与本请求一致的 attempt_id：容器未登记尝试归属时无法证明它属于
// 本次执行，此时返回前置条件错误，由环境准备阶段负责补写，而不是在这里猜测。
func (s *CodeInitializeService) requireContainer(
	ctx context.Context,
	task domain.Task,
	attempt domain.Attempt,
) (domain.Container, error) {
	if err := s.ensureHostUsable(ctx, task); err != nil {
		return domain.Container{}, err
	}
	containerID := strings.TrimSpace(task.ContainerID)
	if containerID == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务 %s 还没有任务容器；请先完成环境准备后再初始化代码",
			ErrCodeInitPrerequisite,
			task.ID,
		)
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: containerID,
		Sync:        true,
	})
	if err != nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 核验任务容器失败: %w",
			ErrCodeInitPrerequisite,
			err,
		)
	}
	container := detail.Container
	if container.Detached {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已脱离 AgentFlow 管理，拒绝自动接管",
			ErrCodeInitPrerequisite,
			container.ID,
		)
	}
	if container.State == domain.ContainerStateRemoved {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已被删除",
			ErrCodeInitPrerequisite,
			container.ID,
		)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 当前未运行，无法初始化代码",
			ErrCodeInitPrerequisite,
			container.ID,
		)
	}
	if strings.TrimSpace(container.DockerID) == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 缺少 Docker 容器标识",
			ErrCodeInitPrerequisite,
			container.ID,
		)
	}
	if container.AttemptID == nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 未登记尝试归属，请先完成环境准备（准备时需指定 --attempt）",
			ErrCodeInitPrerequisite,
			container.ID,
		)
	}
	if *container.AttemptID != attempt.ID {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 登记的是尝试 %s，与本次尝试 %s 不一致",
			ErrCodeInitPrerequisite,
			container.ID,
			*container.AttemptID,
			attempt.ID,
		)
	}
	return container, nil
}

// ensureHostUsable 校验执行节点是否允许执行真实远程操作。
//
// 任务没有绑定执行节点时跳过：该情况会在容器核验阶段以更具体的错误报告。
// 判定本身由 checkHostUsableForCodeFetch 提供，尝试级与任务级共用同一份规则。
func (s *CodeInitializeService) ensureHostUsable(
	ctx context.Context,
	task domain.Task,
) error {
	if err := checkHostUsableForCodeFetch(ctx, s.store, task); err != nil {
		return fmt.Errorf("%w: %w", ErrCodeInitPrerequisite, err)
	}
	return nil
}

// resolveWorkDir 返回本次初始化使用的容器内工作目录。
//
// 优先级固定为：请求显式给出 > 任务配置 > 容器默认项目目录。规则只在
// resolveCodeFetchWorkDir 中定义一次，尝试级与任务级共用同一份判定。
func (s *CodeInitializeService) resolveWorkDir(
	task domain.Task,
	requested string,
) (string, error) {
	workDir, err := resolveCodeFetchWorkDir(task, requested)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCodeInitInvalidInput, err)
	}
	return workDir, nil
}

// initializeTimeout 返回本次初始化的超时时间。
func (s *CodeInitializeService) initializeTimeout(requested time.Duration) (time.Duration, error) {
	if requested <= 0 {
		return s.defaultTimeout, nil
	}
	if requested > maxCodeInitTimeout {
		return 0, fmt.Errorf(
			"%w: 初始化超时不能超过 %s",
			ErrCodeInitInvalidInput,
			maxCodeInitTimeout,
		)
	}
	return requested, nil
}

// appendEvent 追加代码初始化结果事件。
//
// 事件缺失不影响初始化状态：写入失败只记录日志，不会把已确认的结果改回失败。
func (s *CodeInitializeService) appendEvent(
	ctx context.Context,
	request runCodeFetchRequest,
	record domain.CodeFetchRecord,
) {
	event, err := domain.NewEvent(
		request.task.ID,
		&request.step.ID,
		&request.attempt.ID,
		nil,
		domain.EventTypeSourceFetched,
		marshalStepExecutionPayload(map[string]any{
			"record_id":    record.ID.String(),
			"status":       record.Status.String(),
			"stage":        record.Stage.String(),
			"source_type":  record.SourceType.String(),
			"repository":   record.Repository,
			"work_dir":     record.WorkDir,
			"container_id": record.ContainerID,
			"base_branch":  record.BaseBranch,
			"task_branch":  record.TaskBranch,
			"commit":       record.CommitSHA,
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造代码初始化事件失败",
			slog.String("task_id", request.task.ID.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		s.logger.WarnContext(
			ctx,
			"记录代码初始化事件失败",
			slog.String("task_id", request.task.ID.String()),
			slog.String("attempt_id", request.attempt.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// logResult 记录初始化结果。
func (s *CodeInitializeService) logResult(ctx context.Context, result CodeInitResult) {
	level := slog.LevelInfo
	if !result.Succeeded {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(
		ctx,
		level,
		"代码初始化已结束",
		slog.String("task_id", result.TaskID.String()),
		slog.String("step_id", result.StepID.String()),
		slog.String("attempt_id", result.AttemptID.String()),
		slog.String("container", result.ContainerID),
		slog.String("status", result.Status.String()),
		slog.String("stage", result.Stage.String()),
		slog.String("task_branch", result.TaskBranch),
		slog.String("commit", result.Commit),
		slog.Bool("persisted", result.Persisted),
	)
}

// debug 记录调试信息。
func (s *CodeInitializeService) debug(ctx context.Context, message string, attrs ...slog.Attr) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}

// checkCodeInitAllowed 校验当前状态是否允许开始一次代码初始化。
//
// 只允许尚未开始执行或明确可以继续推进的尝试：已经结束的尝试不会再被初始化，
// 避免把历史执行的工作区重新覆盖。
func checkCodeInitAllowed(task domain.Task, attempt domain.Attempt) error {
	if task.Deleted() {
		return fmt.Errorf("任务 %s 已被删除", task.ID)
	}
	if task.CancelRequested {
		return fmt.Errorf("任务 %s 已请求取消，不能初始化代码", task.ID)
	}
	switch task.Status {
	case domain.TaskStatusQueued,
		domain.TaskStatusPreparing,
		domain.TaskStatusPlanning,
		domain.TaskStatusPlanned,
		domain.TaskStatusDeveloping,
		domain.TaskStatusRetrying,
		domain.TaskStatusPaused,
		domain.TaskStatusRecoveryRequired:
	default:
		return fmt.Errorf(
			"任务 %s 当前状态 %s 不允许初始化代码",
			task.ID,
			task.Status,
		)
	}
	switch attempt.Status {
	case domain.AttemptStatusRunning,
		domain.AttemptStatusFailed,
		domain.AttemptStatusInterrupted,
		domain.AttemptStatusRecoveryRequired:
		return nil
	default:
		return fmt.Errorf(
			"尝试 %s 当前状态 %s 不允许初始化代码",
			attempt.ID,
			attempt.Status,
		)
	}
}
