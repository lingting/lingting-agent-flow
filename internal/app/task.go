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

// TaskService 是任务管理的应用服务入口。
//
// CLI 与 HTTP 接口都通过本服务创建、查询、取消与删除任务；任务执行由
// app.TaskWorkflowService 负责，本层只做参数校验与状态编排。
type TaskService struct {
	store  storage.Store
	logger *slog.Logger
	now    func() time.Time
}

// TaskServiceOptions 是构造任务服务的输入。
type TaskServiceOptions struct {
	Store  storage.Store
	Logger *slog.Logger
	Now    func() time.Time
}

// NewTaskService 构造任务服务。
func NewTaskService(options TaskServiceOptions) (*TaskService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("任务服务初始化失败: 存储不能为空")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &TaskService{
		store:  options.Store,
		logger: logger,
		now:    now,
	}, nil
}

// CreateTaskRequest 是创建任务的输入。
//
// 该结构只包含非机密配置：私钥、密码与 Token 一律不进入任务配置。
type CreateTaskRequest struct {
	// Title 是需求标题。
	Title string
	// Description 是需求描述。
	Description string
	// Priority 是需求优先级，空值使用 normal。
	Priority domain.Priority
	// Goal 是任务目标描述，空值时使用需求描述。
	Goal string
	// HostID 是执行节点标识，必填。
	HostID string
	// RepositoryURL 是仓库地址。
	RepositoryURL string
	// BaseBranch 是基础分支。
	BaseBranch string
	// Environment 是语言环境名称。
	Environment string
	// EnvironmentVersion 是锁定的环境版本标签，可空。
	EnvironmentVersion string
	// ProjectID 是可选的项目标识。
	ProjectID string
	// Caches 是允许挂载的缓存类型。
	Caches []string
	// WorkDir 是容器内项目目录，空值使用默认目录。
	WorkDir string
	// Verification 是任务级必需验证命令。
	Verification domain.VerificationSpec
	// Retry 是任务级重试策略。
	Retry domain.RetryConfig
	// MergeAfterPush 表示推送成功后合并基础分支。
	MergeAfterPush bool
	// PlannerRoleID 是规划角色标识。
	PlannerRoleID string
	// DeveloperRoleID 是开发角色标识。
	DeveloperRoleID string
	// ValidatorRoleID 是验证角色标识。
	ValidatorRoleID string
	// MaxPlanSteps 是计划步骤数上限。
	MaxPlanSteps int
	// AgentTimeoutSeconds 是单次 Agent 调用超时秒数。
	AgentTimeoutSeconds int
	// VerificationTimeoutSeconds 是单条验证命令默认超时秒数。
	VerificationTimeoutSeconds int
	// IdempotencyKey 是客户端请求标识，重复提交不会创建第二个任务。
	IdempotencyKey string
}

// CreateTask 校验配置并创建任务。
//
// 非法配置在任务执行前失败：不会先创建容器再发现必填字段缺失。
func (s *TaskService) CreateTask(
	ctx context.Context,
	request CreateTaskRequest,
) (domain.Task, error) {
	prepared, err := s.prepareTaskRequest(ctx, request)
	if err != nil {
		return domain.Task{}, err
	}
	if prepared.idempotencyKey != "" {
		existing, lookupErr := s.store.GetTaskByIdempotencyKey(ctx, prepared.idempotencyKey)
		if lookupErr == nil {
			return *existing, nil
		}
		if !errors.Is(lookupErr, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("创建任务失败: %w", lookupErr)
		}
	}
	now := s.now()
	requirement, err := domain.NewRequirement(
		prepared.title,
		prepared.description,
		prepared.priority,
		now,
	)
	if err != nil {
		return domain.Task{}, fmt.Errorf("创建任务失败: %w", err)
	}
	task, err := domain.NewTask(requirement.ID, prepared.repository, prepared.baseBranch, "", now)
	if err != nil {
		return domain.Task{}, fmt.Errorf("创建任务失败: %w", err)
	}
	task.Config = prepared.config
	task.HostID = prepared.hostID
	task.IdempotencyKey = prepared.idempotencyKey
	if err := task.Validate(); err != nil {
		return domain.Task{}, fmt.Errorf("创建任务失败: %w", err)
	}
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		if err := tx.CreateRequirement(ctx, requirement); err != nil {
			return err
		}
		if err := tx.CreateTask(ctx, task); err != nil {
			return err
		}
		event, eventErr := domain.NewEvent(
			task.ID,
			nil,
			nil,
			nil,
			domain.EventTypeTaskCreated,
			nil,
			now,
		)
		if eventErr != nil {
			return eventErr
		}
		_, eventErr = tx.AppendEvent(ctx, event)
		return eventErr
	})
	if err != nil {
		return domain.Task{}, fmt.Errorf("创建任务失败: %w", err)
	}
	return task, nil
}

// preparedTask 是通过全部校验的建任务参数。
type preparedTask struct {
	title          string
	description    string
	priority       domain.Priority
	repository     string
	baseBranch     string
	hostID         *domain.ID
	config         domain.TaskConfig
	idempotencyKey string
}

// prepareTaskRequest 校验建任务参数。
func (s *TaskService) prepareTaskRequest(
	ctx context.Context,
	request CreateTaskRequest,
) (preparedTask, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" {
		return preparedTask{}, fmt.Errorf("创建任务失败: 需求标题不能为空")
	}
	description := strings.TrimSpace(request.Description)
	if description == "" {
		description = title
	}
	priority := request.Priority
	if priority == "" {
		priority = domain.PriorityNormal
	}
	if err := priority.Validate(); err != nil {
		return preparedTask{}, fmt.Errorf("创建任务失败: %w", err)
	}
	repository, err := domain.ParseGitRepositoryURL(request.RepositoryURL)
	if err != nil {
		return preparedTask{}, fmt.Errorf("创建任务失败: 仓库地址非法: %w", err)
	}
	baseBranch := strings.TrimSpace(request.BaseBranch)
	if baseBranch == "" {
		return preparedTask{}, fmt.Errorf("创建任务失败: 必须显式指定基础分支")
	}
	normalizedBase, err := domain.NormalizeGitBranch(baseBranch)
	if err != nil {
		return preparedTask{}, fmt.Errorf("创建任务失败: 基础分支非法: %w", err)
	}
	hostID, err := s.resolveHost(ctx, request.HostID)
	if err != nil {
		return preparedTask{}, err
	}
	environment, err := domain.NewEnvironmentRef(
		domain.EnvironmentTypeLanguage,
		request.Environment,
	)
	if err != nil {
		return preparedTask{}, fmt.Errorf("创建任务失败: 环境配置非法: %w", err)
	}
	goal := strings.TrimSpace(request.Goal)
	if goal == "" {
		goal = description
	}
	config := domain.TaskConfig{
		Goal:                       goal,
		Environment:                environment,
		EnvironmentVersion:         strings.TrimSpace(request.EnvironmentVersion),
		ProjectID:                  strings.TrimSpace(request.ProjectID),
		Caches:                     request.Caches,
		WorkDir:                    strings.TrimSpace(request.WorkDir),
		Verification:               request.Verification,
		Retry:                      request.Retry,
		MergeAfterPush:             request.MergeAfterPush,
		PlannerRoleID:              domain.ID(strings.TrimSpace(request.PlannerRoleID)),
		DeveloperRoleID:            domain.ID(strings.TrimSpace(request.DeveloperRoleID)),
		ValidatorRoleID:            domain.ID(strings.TrimSpace(request.ValidatorRoleID)),
		MaxPlanSteps:               request.MaxPlanSteps,
		AgentTimeoutSeconds:        request.AgentTimeoutSeconds,
		VerificationTimeoutSeconds: request.VerificationTimeoutSeconds,
	}
	if err := config.Validate(); err != nil {
		return preparedTask{}, fmt.Errorf("创建任务失败: %w", err)
	}
	if err := s.ensureRoles(ctx, config); err != nil {
		return preparedTask{}, err
	}
	return preparedTask{
		title:          title,
		description:    description,
		priority:       priority,
		repository:     repository.Raw(),
		baseBranch:     normalizedBase,
		hostID:         &hostID,
		config:         config,
		idempotencyKey: strings.TrimSpace(request.IdempotencyKey),
	}, nil
}

// resolveHost 解析并校验执行节点。
func (s *TaskService) resolveHost(ctx context.Context, hostID string) (domain.ID, error) {
	trimmed := strings.TrimSpace(hostID)
	if trimmed == "" {
		return "", fmt.Errorf("%w: 必须指定执行节点", ErrTaskInvalidInput)
	}
	id := domain.ID(trimmed)
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("创建任务失败: %w", err)
	}
	host, err := s.store.GetExecutionHost(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", fmt.Errorf(
				"%w: 执行节点 %s 不存在",
				ErrTaskInvalidInput,
				id,
			)
		}
		return "", fmt.Errorf("创建任务失败: 读取执行节点失败: %w", err)
	}
	if !host.Enabled {
		return "", fmt.Errorf("创建任务失败: 执行节点 %s 已被禁用", host.ID)
	}
	return host.ID, nil
}

// ensureRoles 校验角色存在且类型匹配。
func (s *TaskService) ensureRoles(ctx context.Context, cfg domain.TaskConfig) error {
	checks := []struct {
		id       domain.ID
		expected domain.RoleType
	}{
		{cfg.PlannerRoleID, domain.RoleTypePlanner},
		{cfg.DeveloperRoleID, domain.RoleTypeDeveloper},
	}
	if !cfg.ValidatorRoleID.IsEmpty() {
		checks = append(checks, struct {
			id       domain.ID
			expected domain.RoleType
		}{cfg.ValidatorRoleID, domain.RoleTypeValidator})
	}
	for _, check := range checks {
		role, err := s.store.GetRole(ctx, check.id)
		if err != nil {
			return fmt.Errorf("创建任务失败: 读取角色 %s 失败: %w", check.id, err)
		}
		if role.Type != check.expected {
			return fmt.Errorf(
				"创建任务失败: 角色 %s 的类型是 %s，期望 %s",
				check.id,
				role.Type,
				check.expected,
			)
		}
		if !role.Enabled {
			return fmt.Errorf("创建任务失败: 角色 %s 已被禁用", check.id)
		}
	}
	return nil
}

// Requirement 读取任务关联的原始需求。
//
// 任务的名称与描述由需求承载，管理接口需要它来展示可读信息；需求不存在时
// 返回 storage.ErrNotFound，由调用方决定如何呈现。
func (s *TaskService) Requirement(
	ctx context.Context,
	requirementID domain.ID,
) (*domain.Requirement, error) {
	if err := requirementID.Validate(); err != nil {
		return nil, err
	}
	requirement, err := s.store.GetRequirement(ctx, requirementID)
	if err != nil {
		return nil, fmt.Errorf("读取需求失败: %w", err)
	}
	return requirement, nil
}

// GetTask 读取任务详情。
func (s *TaskService) GetTask(ctx context.Context, taskID string) (domain.Task, error) {
	id := domain.ID(strings.TrimSpace(taskID))
	if err := id.Validate(); err != nil {
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	return *task, nil
}

// ListTasks 按条件查询任务列表。
func (s *TaskService) ListTasks(
	ctx context.Context,
	filter storage.TaskFilter,
) ([]domain.Task, error) {
	tasks, err := s.store.ListTasks(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("查询任务列表失败: %w", err)
	}
	return tasks, nil
}

// TaskDetail 是任务详情的聚合视图。
type TaskDetail struct {
	Task          domain.Task
	Steps         []domain.Step
	Attempts      []domain.Attempt
	Sessions      []domain.Session
	CodeSaves     []domain.CodeSaveRecord
	Containers    []domain.Container
	Verifications []domain.VerificationResult
}

// Detail 读取任务的完整执行历史。
func (s *TaskService) Detail(ctx context.Context, taskID string) (TaskDetail, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskDetail{}, err
	}
	steps, err := s.store.ListStepsByTask(ctx, task.ID)
	if err != nil {
		return TaskDetail{}, fmt.Errorf("读取任务步骤失败: %w", err)
	}
	detail := TaskDetail{Task: task, Steps: steps}
	for _, step := range steps {
		attempts, attemptErr := s.store.ListAttemptsByStep(ctx, step.ID)
		if attemptErr != nil {
			return TaskDetail{}, fmt.Errorf("读取执行尝试失败: %w", attemptErr)
		}
		detail.Attempts = append(detail.Attempts, attempts...)
		for _, attempt := range attempts {
			results, resultErr := s.store.ListVerificationResultsByAttempt(ctx, attempt.ID)
			if resultErr != nil {
				return TaskDetail{}, fmt.Errorf("读取验证结果失败: %w", resultErr)
			}
			detail.Verifications = append(detail.Verifications, results...)
		}
	}
	sessions, err := s.store.ListSessionsByTask(ctx, task.ID)
	if err != nil {
		return TaskDetail{}, fmt.Errorf("读取会话记录失败: %w", err)
	}
	detail.Sessions = sessions
	records, err := s.store.ListCodeSaveRecordsByTask(ctx, task.ID.String())
	if err != nil {
		return TaskDetail{}, fmt.Errorf("读取代码保存记录失败: %w", err)
	}
	detail.CodeSaves = records
	if strings.TrimSpace(task.ContainerID) != "" {
		containerID := domain.ID(task.ContainerID)
		if container, lookupErr := s.store.GetContainer(ctx, containerID); lookupErr == nil {
			detail.Containers = append(detail.Containers, *container)
		}
	}
	return detail, nil
}

// CancelTask 记录取消请求并把任务收敛为取消状态。
//
// 取消请求先落盘：即使当前没有执行者，后续推进也会看到取消请求并停止。
func (s *TaskService) CancelTask(ctx context.Context, taskID string) (domain.Task, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if task.Status.IsTerminal() {
		return domain.Task{}, fmt.Errorf(
			"取消任务失败: 任务已处于终态 %s",
			task.Status,
		)
	}
	now := s.now()
	if err := task.RequestCancel(now); err != nil {
		return domain.Task{}, fmt.Errorf("取消任务失败: %w", err)
	}
	if !task.Status.IsActive() {
		if err := task.Transition(domain.TaskStatusCancelling, now); err != nil {
			return domain.Task{}, fmt.Errorf("取消任务失败: %w", err)
		}
	}
	if err := s.store.UpdateTask(ctx, task); err != nil {
		return domain.Task{}, fmt.Errorf("取消任务失败: %w", err)
	}
	event, err := domain.NewEvent(
		task.ID,
		nil,
		nil,
		nil,
		domain.EventTypeTaskCancelRequested,
		nil,
		now,
	)
	if err != nil {
		return domain.Task{}, fmt.Errorf("取消任务失败: %w", err)
	}
	if _, err := s.store.AppendEvent(ctx, event); err != nil {
		return domain.Task{}, fmt.Errorf("取消任务失败: %w", err)
	}
	return task, nil
}

// ListUnfinishedTasks 返回尚未结束的任务，用于进程重启后的恢复识别。
func (s *TaskService) ListUnfinishedTasks(ctx context.Context) ([]domain.Task, error) {
	tasks, err := s.store.ListTasks(ctx, storage.TaskFilter{
		ExcludeStatuses: []domain.TaskStatus{
			domain.TaskStatusCompleted,
			domain.TaskStatusCancelled,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("查询未结束任务失败: %w", err)
	}
	return tasks, nil
}
