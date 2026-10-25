package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 任务管理 API 的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本；
// 面向用户的说明由错误本身携带，不包含内部实现细节。
var (
	// ErrTaskInvalidInput 表示请求参数不合法，例如名称为空或分页参数越界。
	ErrTaskInvalidInput = errors.New("app: 任务请求参数非法")
	// ErrTaskNotFound 表示任务不存在，或已被逻辑删除。
	ErrTaskNotFound = errors.New("app: 任务不存在")
	// ErrTaskStateConflict 表示当前状态不允许该操作，例如删除执行中的任务。
	ErrTaskStateConflict = errors.New("app: 任务状态不允许该操作")
)

// 任务列表分页参数的默认值与上限。
//
// 上限是硬约束：调用方传入更大的值不会读取全表，而是被拒绝，避免一次请求
// 拉走整张任务表。
const (
	// DefaultTaskPageSize 是未指定 page size 时使用的默认页大小。
	DefaultTaskPageSize = 20
	// MaxTaskPageSize 是单次查询允许的最大页大小。
	MaxTaskPageSize = 100
)

// 任务名称与描述的长度上限。
const (
	// MaxTaskNameLength 是任务名称允许的最大字符数。
	MaxTaskNameLength = 200
	// MaxTaskDescriptionLength 是任务描述允许的最大字符数。
	MaxTaskDescriptionLength = 4000
)

// TaskPageRequest 是任务列表的查询参数。
//
// Current 与 Size 使用 1 起始的页号与页大小，与前端表格的分页语义一致。
type TaskPageRequest struct {
	// Current 是页号，从 1 开始；小于等于 0 时使用 1。
	Current int
	// Size 是页大小；小于等于 0 时使用默认值，超过上限时被拒绝。
	Size int
	// Statuses 按状态筛选，为空表示不限制。
	Statuses []domain.TaskStatus
}

// CreateTaskFromAPIRequest 是任务管理接口的建任务输入。
//
// 它复用 CLI 的执行型配置字段：任务在创建时就已具备完整执行配置，后续步骤的
// 执行器可以直接使用，不需要再补一次配置。凭据字段不在其中。
type CreateTaskFromAPIRequest struct {
	// Name 是任务名称，必填。
	Name string
	// Description 是任务描述或执行目标，必填。
	Description string
	// ProjectID 是可选的项目标识。
	ProjectID string
	// 以下字段与 CreateTaskRequest 同义，由接口层原样传入。
	Priority                   domain.Priority
	HostID                     string
	RepositoryURL              string
	BaseBranch                 string
	Environment                string
	EnvironmentVersion         string
	Caches                     []string
	WorkDir                    string
	Verification               domain.VerificationSpec
	Retry                      domain.RetryConfig
	MergeAfterPush             bool
	PlannerRoleID              string
	DeveloperRoleID            string
	ValidatorRoleID            string
	MaxPlanSteps               int
	AgentTimeoutSeconds        int
	VerificationTimeoutSeconds int
	IdempotencyKey             string
}

// normalize 校验并归一化建任务输入，返回可交给 CreateTask 的请求。
//
// 名称与描述在这里校验并转换成需求的标题与描述：任务管理接口不引入第二套
// 命名，仍以需求作为名称与描述的承载者。
func (r CreateTaskFromAPIRequest) normalize() (CreateTaskRequest, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return CreateTaskRequest{}, fmt.Errorf("%w: 任务名称不能为空", ErrTaskInvalidInput)
	}
	if len([]rune(name)) > MaxTaskNameLength {
		return CreateTaskRequest{}, fmt.Errorf(
			"%w: 任务名称不能超过 %d 个字符",
			ErrTaskInvalidInput,
			MaxTaskNameLength,
		)
	}
	description := strings.TrimSpace(r.Description)
	if description == "" {
		return CreateTaskRequest{}, fmt.Errorf("%w: 任务描述不能为空", ErrTaskInvalidInput)
	}
	if len([]rune(description)) > MaxTaskDescriptionLength {
		return CreateTaskRequest{}, fmt.Errorf(
			"%w: 任务描述不能超过 %d 个字符",
			ErrTaskInvalidInput,
			MaxTaskDescriptionLength,
		)
	}
	projectID := strings.TrimSpace(r.ProjectID)
	if err := domain.ValidateProjectID(projectID); err != nil {
		return CreateTaskRequest{}, fmt.Errorf("%w: %w", ErrTaskInvalidInput, err)
	}
	return CreateTaskRequest{
		Title:                      name,
		Description:                description,
		Priority:                   r.Priority,
		Goal:                       description,
		HostID:                     r.HostID,
		RepositoryURL:              r.RepositoryURL,
		BaseBranch:                 r.BaseBranch,
		Environment:                r.Environment,
		EnvironmentVersion:         r.EnvironmentVersion,
		ProjectID:                  projectID,
		Caches:                     r.Caches,
		WorkDir:                    r.WorkDir,
		Verification:               r.Verification,
		Retry:                      r.Retry,
		MergeAfterPush:             r.MergeAfterPush,
		PlannerRoleID:              r.PlannerRoleID,
		DeveloperRoleID:            r.DeveloperRoleID,
		ValidatorRoleID:            r.ValidatorRoleID,
		MaxPlanSteps:               r.MaxPlanSteps,
		AgentTimeoutSeconds:        r.AgentTimeoutSeconds,
		VerificationTimeoutSeconds: r.VerificationTimeoutSeconds,
		IdempotencyKey:             r.IdempotencyKey,
	}, nil
}

// CreateTaskFromAPI 校验任务管理接口的输入并创建任务。
//
// 它不做任何执行动作：任务只落到 queued，是否执行由调用方另行决定。
func (s *TaskService) CreateTaskFromAPI(
	ctx context.Context,
	request CreateTaskFromAPIRequest,
) (domain.Task, error) {
	prepared, err := request.normalize()
	if err != nil {
		return domain.Task{}, err
	}
	return s.CreateTask(ctx, prepared)
}

// GetTaskForAPI 读取未被逻辑删除的任务。
//
// 已删除的任务按"不存在"处理：列表里看不到的记录，详情也不应该能读到。
func (s *TaskService) GetTaskForAPI(ctx context.Context, taskID string) (domain.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: %s", ErrTaskNotFound, strings.TrimSpace(taskID))
		}
		return domain.Task{}, err
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	return task, nil
}

// ListTasksForAPI 按页返回任务，并给出满足筛选条件的总数。
//
// 分页参数在这里校验：页大小超过上限直接拒绝，不会退化为全表查询。
func (s *TaskService) ListTasksForAPI(
	ctx context.Context,
	request TaskPageRequest,
) (storage.TaskPage, error) {
	size := request.Size
	if size <= 0 {
		size = DefaultTaskPageSize
	}
	if size > MaxTaskPageSize {
		return storage.TaskPage{}, fmt.Errorf(
			"%w: 页大小不能超过 %d",
			ErrTaskInvalidInput,
			MaxTaskPageSize,
		)
	}
	current := request.Current
	if current <= 0 {
		current = 1
	}
	for _, status := range request.Statuses {
		if err := status.Validate(); err != nil {
			return storage.TaskPage{}, fmt.Errorf("%w: %w", ErrTaskInvalidInput, err)
		}
	}
	page, err := s.store.ListTaskPage(ctx, storage.TaskFilter{
		Statuses: request.Statuses,
		Limit:    size,
		Offset:   (current - 1) * size,
	})
	if err != nil {
		return storage.TaskPage{}, fmt.Errorf("查询任务列表失败: %w", err)
	}
	return page, nil
}

// CancelTaskForAPI 处理任务取消请求。
//
// 语义按当前状态区分：
//   - queued：任务还没有开始执行，直接收敛为 cancelled；
//   - 其他未结束状态：只记录取消请求并推进到 cancelling，实际停止由执行者确认；
//   - 已处于 cancelling：重复请求是幂等的，返回当前状态；
//   - 终态：返回状态冲突，不会把已结束的任务改写。
func (s *TaskService) CancelTaskForAPI(
	ctx context.Context,
	taskID string,
) (domain.Task, error) {
	task, err := s.GetTaskForAPI(ctx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	// 条件更新必须以读取到的原始状态作为前置条件：任务对象随后会被推进，
	// 直接用推进后的状态做条件会导致更新永远匹配不到。
	originalStatus := task.Status
	now := s.now()
	// 已结束（含 failed）的任务不能被取消：CancelImmediately 的判定不覆盖 failed，
	// 因此这里必须先按 IsFinished 拒绝，再交给统一的领域规则。
	if originalStatus.IsFinished() {
		return domain.Task{}, fmt.Errorf(
			"%w: 任务已结束，状态为 %s",
			ErrTaskStateConflict,
			originalStatus,
		)
	}
	repeated, err := task.ApplyCancellationRequest(now)
	if err != nil {
		return domain.Task{}, fmt.Errorf("%w: %w", ErrTaskStateConflict, err)
	}
	if repeated {
		// 重复取消：请求已记录，等待执行者确认停止，不改写时间戳。
		return task, nil
	}
	if err := s.store.UpdateTaskCancellation(
		ctx,
		task,
		[]domain.TaskStatus{originalStatus},
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return domain.Task{}, fmt.Errorf(
				"%w: 任务状态已被其他操作改变，请重新读取",
				ErrTaskStateConflict,
			)
		}
		return domain.Task{}, fmt.Errorf("取消任务失败: %w", err)
	}
	if err := s.appendTaskEvent(ctx, task, domain.EventTypeTaskCancelRequested, now); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// DeleteTask 逻辑删除任务。
//
// 只有终态任务可以删除：执行中的任务必须先停止，否则会留下无人接管的现场。
// 删除只标记 deleted_at，历史记录（步骤、尝试、会话、事件）全部保留。
func (s *TaskService) DeleteTask(ctx context.Context, taskID string) error {
	task, err := s.GetTaskForAPI(ctx, taskID)
	if err != nil {
		return err
	}
	now := s.now()
	if err := task.MarkDeleted(now); err != nil {
		return fmt.Errorf("%w: %w", ErrTaskStateConflict, err)
	}
	if err := s.store.DeleteTask(
		ctx,
		task.ID,
		[]domain.TaskStatus{task.Status},
		now,
	); err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return fmt.Errorf(
				"%w: 任务状态已被其他操作改变，请重新读取",
				ErrTaskStateConflict,
			)
		}
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
		}
		return fmt.Errorf("删除任务失败: %w", err)
	}
	return nil
}

// appendTaskEvent 追加任务级事件，失败时返回带上下文的错误。
func (s *TaskService) appendTaskEvent(
	ctx context.Context,
	task domain.Task,
	eventType domain.EventType,
	at time.Time,
) error {
	event, err := domain.NewEvent(task.ID, nil, nil, nil, eventType, nil, at)
	if err != nil {
		return fmt.Errorf("记录任务事件失败: %w", err)
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		return fmt.Errorf("记录任务事件失败: %w", err)
	}
	return nil
}
