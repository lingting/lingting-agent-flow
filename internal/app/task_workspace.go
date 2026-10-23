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

// 任务级工作区初始化的错误类别。
//
// Handler 与启动服务通过 errors.Is 判断类别并决定后续动作，不解析错误文本。
var (
	// ErrTaskWorkspaceNotFound 表示任务或容器不存在。
	ErrTaskWorkspaceNotFound = errors.New("app: 工作区初始化目标不存在")
	// ErrTaskWorkspaceInvalidInput 表示初始化请求参数不合法。
	ErrTaskWorkspaceInvalidInput = errors.New("app: 工作区初始化请求参数非法")
	// ErrTaskWorkspaceConflict 表示当前状态不允许开始初始化，或现场已被占用。
	ErrTaskWorkspaceConflict = errors.New("app: 工作区初始化状态冲突")
	// ErrTaskWorkspacePrerequisite 表示初始化所需的前置条件未满足。
	//
	// 例如任务没有受管理的运行中容器，或执行节点未通过环境预检。该错误必须在执行
	// 任何 Git 命令之前返回，不允许退化为在服务进程的当前目录执行。
	ErrTaskWorkspacePrerequisite = errors.New("app: 工作区初始化前置条件未满足")
	// ErrTaskWorkspaceFailed 表示初始化已经执行但没有成功。
	ErrTaskWorkspaceFailed = errors.New("app: 工作区初始化未成功")
	// ErrTaskWorkspaceUncertain 表示远端执行结果无法确认。
	//
	// 它绝不能被当作失败后可直接重试：现场必须保留，先核验容器内实际状态。
	ErrTaskWorkspaceUncertain = errors.New("app: 工作区初始化结果无法确认")
	// ErrTaskWorkspaceNotPersisted 表示初始化已执行但结果未能可靠持久化。
	ErrTaskWorkspaceNotPersisted = errors.New("app: 工作区初始化结果未能持久化")
)

// 任务级工作区初始化的默认参数。
const (
	// defaultTaskWorkspaceTimeout 是一次任务级工作区初始化的默认超时。
	defaultTaskWorkspaceTimeout = 20 * time.Minute
	// maxTaskWorkspaceTimeout 是允许请求的最大初始化超时。
	maxTaskWorkspaceTimeout = 60 * time.Minute
)

// TaskWorkspaceRequest 是通过接口发起一次任务级工作区初始化的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务与可选的执行参数，
// 不能指定容器、仓库地址、凭据、Session 路径或命令行参数。
type TaskWorkspaceRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// WorkDir 是容器内目标工作目录，空值使用任务配置的目录。
	WorkDir string
	// Timeout 是本次初始化的超时，零值使用默认值。
	Timeout time.Duration
}

// TaskWorkspaceResult 是一次任务级工作区初始化的结果。
//
// 它显式区分三件事：初始化状态、代码是否就绪、结果是否可靠持久化。
// 三者都不能互相替代：skipped 表示该任务在该容器上此前已就绪且本次未改动，
// persisted=false 表示记录可能不完整。
type TaskWorkspaceResult struct {
	// Record 是本次初始化的执行记录。
	Record domain.TaskWorkspaceInitRecord
	// RecordID 是本次初始化记录标识。
	RecordID domain.ID
	// TaskID / ContainerID 是本次初始化的归属。
	TaskID      domain.ID
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
	// Status 是初始化状态。
	Status domain.TaskWorkspaceInitStatus
	// Succeeded 表示工作区已经就绪（succeeded 或 skipped）。
	Succeeded bool
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
	// StartedAt / FinishedAt 是本次初始化的起止时间。
	StartedAt  time.Time
	FinishedAt time.Time
}

// TaskWorkspaceStore 是任务级工作区初始化服务依赖的存储能力组合。
type TaskWorkspaceStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	codeFetchHostStore
	storage.TaskWorkspaceInitStore
}

// TaskWorkspaceContainerPort 是任务级工作区初始化对任务容器的需求。
//
// 容器只被核验与使用：本服务不会创建、启动、删除或接管容器，也不会在容器
// 不可用时静默回退到本机执行。
type TaskWorkspaceContainerPort interface {
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// FetchSourceWith 用指定代码获取方式执行一次容器内代码获取。
	//
	// 实现复用与尝试级初始化完全相同的代码路径（ContainerService → source.GitFetcher
	// → source.Provider），因此任务级与尝试级不会出现两套 Git 命令。
	FetchSourceWith(
		ctx context.Context,
		containerID string,
		request source.FetchRequest,
	) (source.FetchResult, error)
}

// TaskWorkspaceOptions 是构造任务级工作区初始化服务的输入。
type TaskWorkspaceOptions struct {
	// Store 是持久化入口，必填。
	Store TaskWorkspaceStore
	// Containers 是容器核验与容器内代码获取能力，必填。
	Containers TaskWorkspaceContainerPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// DefaultTimeout 覆盖未配置超时时的默认超时。
	DefaultTimeout time.Duration
}

// TaskWorkspaceService 是任务级工作区初始化的协调器。
//
// 职责边界：
//   - 校验任务、容器与执行节点的归属与状态；
//   - 先写入初始化记录，再执行容器内 Git 命令，最后写回真实结果；
//   - 同一任务同一容器已经初始化成功时不重新 Clone，只记录 skipped；
//   - 结果无法确认时收敛为 uncertain 并停止，绝不重放代码获取。
//
// 它与尝试级的 CodeInitializeService 是不同层次的服务：本服务发生在任何 Step /
// Attempt 存在之前，因此不校验（也不伪造）step_id / attempt_id；它不实现 Push、
// Merge、代码保存或容器生命周期，也不决定任务最终是否成功。
type TaskWorkspaceService struct {
	store          TaskWorkspaceStore
	containers     TaskWorkspaceContainerPort
	logger         *slog.Logger
	now            func() time.Time
	defaultTimeout time.Duration
	// running 记录本进程内正在初始化的任务，用于快速拒绝重复请求。
	//
	// 它是第一道防线而不是一致性保证：真正的幂等边界由数据库记录、部分唯一索引
	// 与目标目录检查共同承担。
	running *stepExecutionRegistry
}

// NewTaskWorkspaceService 构造任务级工作区初始化协调器。
func NewTaskWorkspaceService(options TaskWorkspaceOptions) (*TaskWorkspaceService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("工作区初始化服务初始化失败: 存储不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("工作区初始化服务初始化失败: 容器能力不能为空")
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
		timeout = defaultTaskWorkspaceTimeout
	}
	return &TaskWorkspaceService{
		store:          options.Store,
		containers:     options.Containers,
		logger:         logger,
		now:            now,
		defaultTimeout: timeout,
		running:        newStepExecutionRegistry(),
	}, nil
}

// InitializeTaskWorkspace 为任务容器准备代码工作区。
//
// 执行顺序固定为：校验归属与状态 → 核验容器与执行节点 → 判定幂等 → 写入记录 →
// 容器内获取代码 → 写回真实结果。任一步失败都会把记录收敛为对应的失败状态，
// 不会因为失败而删除容器或工作区。
func (s *TaskWorkspaceService) InitializeTaskWorkspace(
	ctx context.Context,
	request TaskWorkspaceRequest,
) (TaskWorkspaceResult, error) {
	taskID, err := parseTaskWorkspaceID(request.TaskID)
	if err != nil {
		return TaskWorkspaceResult{}, err
	}
	timeout, err := s.initializeTimeout(request.Timeout)
	if err != nil {
		return TaskWorkspaceResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return TaskWorkspaceResult{}, err
	}
	workDir, err := s.resolveWorkDir(task, request.WorkDir)
	if err != nil {
		return TaskWorkspaceResult{}, err
	}
	container, err := s.requireContainer(ctx, task)
	if err != nil {
		return TaskWorkspaceResult{}, err
	}
	release, acquired := s.running.acquire(task.ID)
	if !acquired {
		return TaskWorkspaceResult{}, fmt.Errorf(
			"%w: 任务 %s 的工作区正在初始化中",
			ErrTaskWorkspaceConflict,
			task.ID,
		)
	}
	defer release()

	if reused, found, reuseErr := s.reuseInitialized(ctx, task, container, workDir); found {
		return reused, reuseErr
	}
	record, err := s.beginRecord(ctx, task, container, workDir)
	if err != nil {
		// 判定已经确认需要初始化，但记录没能写入：后续阶段必须停止，不能在没有
		// 任何执行痕迹的情况下继续生成计划。
		return TaskWorkspaceResult{}, err
	}
	return s.runFetch(ctx, taskWorkspaceFetchRequest{
		record:    record,
		task:      task,
		container: container,
		workDir:   workDir,
		timeout:   timeout,
	})
}

// taskWorkspaceFetchRequest 是一次真实代码获取的输入。
type taskWorkspaceFetchRequest struct {
	record    domain.TaskWorkspaceInitRecord
	task      domain.Task
	container domain.Container
	workDir   string
	timeout   time.Duration
}

// runFetch 执行一次容器内代码获取并写回结果。
//
// 无论获取以何种方式结束，都会返回一个结果对象：失败、超时、取消与结果不确定
// 不是"没有结果"，而是有明确结论的结果。持久化错误不会阻止返回值，而是记录在
// PersistenceErrors 中，使调用方看到"执行事实"与"状态写入是否完整"两个独立事实。
func (s *TaskWorkspaceService) runFetch(
	ctx context.Context,
	request taskWorkspaceFetchRequest,
) (TaskWorkspaceResult, error) {
	fetchRequest := source.FetchRequest{
		ProjectID:   request.task.Config.ProjectID,
		TaskID:      request.task.ID.String(),
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
	result := TaskWorkspaceResult{
		Record:      request.record,
		RecordID:    request.record.ID,
		TaskID:      request.task.ID,
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
		status := taskWorkspaceInitStatusOf(fetchErr, ctx)
		stage := codeFetchStageOrDefault(fetched.Stage)
		summary := fetched.ErrorSummary
		if summary == "" {
			summary = errorSummary(fetchErr)
		}
		if summary == "" {
			summary = "代码获取未成功"
		}
		markErr = record.MarkFailed(
			status,
			stage,
			fetched.ErrorCode,
			summary,
			result.FinishedAt,
		)
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
		return result, errors.Join(ErrTaskWorkspaceNotPersisted, persistErr)
	}
	switch result.Status {
	case domain.TaskWorkspaceInitStatusUncertain:
		// 结果无法确认既不是失败也不是成功：现场必须保留，且不得盲目重放 Clone。
		return result, fmt.Errorf(
			"%w（阶段 %s）: %s",
			ErrTaskWorkspaceUncertain,
			result.Stage,
			result.Failure,
		)
	case domain.TaskWorkspaceInitStatusSucceeded, domain.TaskWorkspaceInitStatusSkipped:
		return result, nil
	default:
		return result, fmt.Errorf(
			"%w（%s，阶段 %s）: %s",
			ErrTaskWorkspaceFailed,
			result.Status,
			result.Stage,
			result.Failure,
		)
	}
}

// persistRecord 写入初始化记录并返回写入后的记录。
//
// 领域状态迁移失败时仍尝试写入，使"执行已发生但记录不完整"可以被观察到，
// 而不是把失败原因彻底丢失。
func (s *TaskWorkspaceService) persistRecord(
	ctx context.Context,
	record domain.TaskWorkspaceInitRecord,
	markErr error,
) (domain.TaskWorkspaceInitRecord, error) {
	writeCtx := context.WithoutCancel(ctx)
	var errs []error
	if markErr != nil {
		errs = append(errs, fmt.Errorf("标记工作区初始化结果失败: %w", markErr))
	}
	if updateErr := s.store.UpdateTaskWorkspaceInit(writeCtx, record); updateErr != nil {
		errs = append(errs, fmt.Errorf("写入工作区初始化记录失败: %w", updateErr))
	}
	if len(errs) == 0 {
		return record, nil
	}
	return record, errors.Join(errs...)
}

// reuseInitialized 判断该任务在该容器上是否已经完成过初始化。
//
// 已经成功初始化时不重新 Clone：重新 Clone 会覆盖容器内可能已经产生的开发成果。
// 此时写入一条 skipped 记录并返回上次核验到的 commit。
//
// 判定按容器代次进行：旧容器上的成功记录不适用于当前容器，任务重新开始时必须
// 在新容器上重新初始化。
func (s *TaskWorkspaceService) reuseInitialized(
	ctx context.Context,
	task domain.Task,
	container domain.Container,
	workDir string,
) (TaskWorkspaceResult, bool, error) {
	records, err := s.store.ListTaskWorkspaceInitsByTask(ctx, task.ID.String())
	if err != nil {
		return TaskWorkspaceResult{}, false, fmt.Errorf("读取工作区初始化记录失败: %w", err)
	}
	var previous *domain.TaskWorkspaceInitRecord
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
		return TaskWorkspaceResult{}, false, nil
	}
	record, err := s.beginRecord(ctx, task, container, workDir)
	if err != nil {
		// 工作区此前确实就绪，但本次无法写入 skipped 记录：返回执行事实与持久化
		// 失败两件事，不能因为记录写不进去就重新 Clone。
		return TaskWorkspaceResult{
			TaskID:            task.ID,
			ContainerID:       container.ID.String(),
			DockerID:          container.DockerID,
			WorkDir:           workDir,
			TaskBranch:        record.TaskBranch,
			Commit:            previous.CommitSHA,
			Status:            domain.TaskWorkspaceInitStatusSkipped,
			Succeeded:         true,
			Stage:             domain.CodeFetchStageVerify,
			Persisted:         false,
			PersistenceErrors: []string{err.Error()},
		}, true, err
	}
	finishedAt := s.now()
	if markErr := record.MarkSkipped(previous.CommitSHA, finishedAt); markErr != nil {
		return TaskWorkspaceResult{}, true, fmt.Errorf("标记工作区初始化结果失败: %w", markErr)
	}
	persisted, persistErr := s.persistRecord(ctx, record, nil)
	result := TaskWorkspaceResult{
		Record:      persisted,
		RecordID:    persisted.ID,
		TaskID:      task.ID,
		ContainerID: container.ID.String(),
		DockerID:    container.DockerID,
		WorkDir:     workDir,
		TaskBranch:  persisted.TaskBranch,
		BaseBranch:  persisted.BaseBranch,
		Commit:      persisted.CommitSHA,
		Status:      persisted.Status,
		Succeeded:   persisted.Succeeded(),
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
		"工作区初始化已跳过：该任务在当前容器上此前已就绪",
		slog.String("task_id", task.ID.String()),
		slog.String("container", container.ID.String()),
		slog.String("commit", persisted.CommitSHA),
	)
	return result, true, nil
}

// beginRecord 写入一条 pending 记录并推进到 running。
//
// 记录先于任何容器内命令写入：执行失败也要留下可追踪的痕迹，而不是只有一条
// 无法定位阶段的错误。
func (s *TaskWorkspaceService) beginRecord(
	ctx context.Context,
	task domain.Task,
	container domain.Container,
	workDir string,
) (domain.TaskWorkspaceInitRecord, error) {
	record, err := domain.NewTaskWorkspaceInitRecord(
		"",
		task.ID.String(),
		container.ID.String(),
		container.DockerID,
		domain.CodeSourceGit,
		workDir,
		s.now(),
	)
	if err != nil {
		return domain.TaskWorkspaceInitRecord{}, fmt.Errorf("%w: %w", ErrTaskWorkspaceInvalidInput, err)
	}
	record.Repository = task.RepositoryPath
	if err := s.store.CreateTaskWorkspaceInit(ctx, record); err != nil {
		return domain.TaskWorkspaceInitRecord{}, fmt.Errorf(
			"%w: 写入工作区初始化记录失败: %w",
			ErrTaskWorkspaceNotPersisted,
			err,
		)
	}
	if err := record.MarkRunning(domain.CodeFetchStageProbe, s.now()); err != nil {
		return domain.TaskWorkspaceInitRecord{}, fmt.Errorf("标记工作区初始化开始失败: %w", err)
	}
	if err := s.store.UpdateTaskWorkspaceInit(ctx, record); err != nil {
		return domain.TaskWorkspaceInitRecord{}, fmt.Errorf(
			"%w: 写入工作区初始化记录失败: %w",
			ErrTaskWorkspaceNotPersisted,
			err,
		)
	}
	return record, nil
}

// loadTask 读取任务，不存在或已删除时返回"不存在"。
func (s *TaskWorkspaceService) loadTask(
	ctx context.Context,
	id domain.ID,
) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrTaskWorkspaceNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrTaskWorkspaceNotFound, id)
	}
	return *task, nil
}

// requireContainer 核验执行节点与任务容器。
//
// 容器必须受管理、未 detached 且处于运行中。与尝试级初始化不同，本方法不校验
// 容器的 attempt_id 绑定：任务级初始化发生在任何 Attempt 存在之前，要求绑定会
// 迫使调用方伪造尝试归属。
func (s *TaskWorkspaceService) requireContainer(
	ctx context.Context,
	task domain.Task,
) (domain.Container, error) {
	if err := checkHostUsableForCodeFetch(ctx, s.store, task); err != nil {
		return domain.Container{}, fmt.Errorf("%w: %w", ErrTaskWorkspacePrerequisite, err)
	}
	containerID := strings.TrimSpace(task.ContainerID)
	if containerID == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务 %s 还没有任务容器；请先完成环境准备后再初始化工作区",
			ErrTaskWorkspacePrerequisite,
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
			ErrTaskWorkspacePrerequisite,
			err,
		)
	}
	container := detail.Container
	if container.Detached {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已脱离 AgentFlow 管理，拒绝自动接管",
			ErrTaskWorkspacePrerequisite,
			container.ID,
		)
	}
	if container.State == domain.ContainerStateRemoved {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已被删除",
			ErrTaskWorkspacePrerequisite,
			container.ID,
		)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 当前未运行，无法初始化工作区",
			ErrTaskWorkspacePrerequisite,
			container.ID,
		)
	}
	if strings.TrimSpace(container.DockerID) == "" {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 缺少 Docker 容器标识",
			ErrTaskWorkspacePrerequisite,
			container.ID,
		)
	}
	return container, nil
}

// resolveWorkDir 返回本次初始化使用的容器内工作目录。
//
// 规则只在 resolveCodeFetchWorkDir 中定义一次，尝试级与任务级共用同一份判定。
func (s *TaskWorkspaceService) resolveWorkDir(
	task domain.Task,
	requested string,
) (string, error) {
	workDir, err := resolveCodeFetchWorkDir(task, requested)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrTaskWorkspaceInvalidInput, err)
	}
	return workDir, nil
}

// initializeTimeout 返回本次初始化的超时时间。
func (s *TaskWorkspaceService) initializeTimeout(
	requested time.Duration,
) (time.Duration, error) {
	if requested <= 0 {
		return s.defaultTimeout, nil
	}
	if requested > maxTaskWorkspaceTimeout {
		return 0, fmt.Errorf(
			"%w: 初始化超时不能超过 %s",
			ErrTaskWorkspaceInvalidInput,
			maxTaskWorkspaceTimeout,
		)
	}
	return requested, nil
}

// appendEvent 追加工作区初始化结果事件。
//
// 事件缺失不影响初始化状态：写入失败只记录日志，不会把已确认的结果改回失败。
func (s *TaskWorkspaceService) appendEvent(
	ctx context.Context,
	request taskWorkspaceFetchRequest,
	record domain.TaskWorkspaceInitRecord,
) {
	event, err := domain.NewEvent(
		request.task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeSourceFetched,
		marshalStepExecutionPayload(map[string]any{
			"record_id":     record.ID.String(),
			"scope":         "task_workspace",
			"status":        record.Status.String(),
			"stage":         record.Stage.String(),
			"source_type":   record.SourceType.String(),
			"repository":    record.Repository,
			"work_dir":      record.WorkDir,
			"container_id":  record.ContainerID,
			"base_branch":   record.BaseBranch,
			"task_branch":   record.TaskBranch,
			"commit":        record.CommitSHA,
			"error_code":    record.ErrorCode,
			"error_summary": record.ErrorSummary,
		}),
		s.now(),
	)
	if err != nil {
		s.logger.WarnContext(
			ctx,
			"构造工作区初始化事件失败",
			slog.String("task_id", request.task.ID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), event); err != nil {
		s.logger.WarnContext(
			ctx,
			"记录工作区初始化事件失败",
			slog.String("task_id", request.task.ID.String()),
			slog.String("container", request.container.ID.String()),
			slog.String("error", errorSummary(err)),
		)
	}
}

// logResult 记录初始化结果。
func (s *TaskWorkspaceService) logResult(ctx context.Context, result TaskWorkspaceResult) {
	level := slog.LevelInfo
	if !result.Succeeded {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(
		ctx,
		level,
		"任务级工作区初始化已结束",
		slog.String("task_id", result.TaskID.String()),
		slog.String("container", result.ContainerID),
		slog.String("status", result.Status.String()),
		slog.String("stage", result.Stage.String()),
		slog.String("task_branch", result.TaskBranch),
		slog.String("commit", result.Commit),
		slog.Bool("persisted", result.Persisted),
	)
}

// debug 记录调试信息。
func (s *TaskWorkspaceService) debug(ctx context.Context, message string, attrs ...slog.Attr) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.LogAttrs(ctx, slog.LevelDebug, message, attrs...)
}

// parseTaskWorkspaceID 解析并校验任务标识。
func parseTaskWorkspaceID(raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 任务标识不能为空", ErrTaskWorkspaceInvalidInput)
	}
	if strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: 任务标识非法", ErrTaskWorkspaceInvalidInput)
	}
	return domain.ID(trimmed), nil
}
