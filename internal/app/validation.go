package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 验证服务的错误类别。
//
// Handler 通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrValidationNotFound 表示任务、步骤或尝试不存在，或不属于同一条任务链。
	ErrValidationNotFound = errors.New("app: 验证目标不存在")
	// ErrValidationInvalidInput 表示验证请求参数或检查配置不合法。
	ErrValidationInvalidInput = errors.New("app: 验证请求参数非法")
	// ErrValidationConflict 表示当前状态不允许开始验证，或验证已被占用。
	ErrValidationConflict = errors.New("app: 验证状态冲突")
	// ErrValidationPrerequisite 表示验证所需的前置条件未满足。
	//
	// 例如任务没有受管理的运行中容器。该错误必须在执行任何命令之前返回，
	// 不允许退化为"在服务进程的当前目录执行验证命令"。
	ErrValidationPrerequisite = errors.New("app: 验证前置条件未满足")
	// ErrValidationNotPersisted 表示验证已执行但结果未能可靠持久化。
	//
	// 它绝不能被报告为"验证成功"：结论再正确，没有可靠保存也不能当作可用证据。
	ErrValidationNotPersisted = errors.New("app: 验证结果未能持久化")
)

// 验证服务的默认参数。
const (
	// defaultValidationTimeout 是整次验证的默认总超时。
	defaultValidationTimeout = 30 * time.Minute
)

// ValidationExecuteRequest 是通过接口发起一次验证的输入。
//
// 关联标识全部来自经过校验的数据库记录：调用方只能指定任务、步骤与尝试，
// 不能指定工作目录、命令行参数或 Session 路径。
type ValidationExecuteRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是待验证的步骤标识。
	StepID string
	// AttemptID 是待验证的执行尝试标识。
	AttemptID string
	// Timeout 是本次验证的总超时，零值使用默认值。
	Timeout time.Duration
	// Checks 是可选的检查覆盖配置。
	//
	// 为空时使用步骤中声明的验证规格；非空时替换本次验证的检查集合，
	// 但校验规则与结论计算完全一致，调用方无法借此放宽必需检查门槛。
	Checks []domain.ValidationCheckDefinition
}

// ValidationExecuteResult 是一次验证的结果。
//
// 它显式区分三件事：验证流程状态、最终结论、结果是否可靠持久化。
// 三者都不能互相替代：completed 不代表 passed，passed 也不代表已保存。
type ValidationExecuteResult struct {
	// ValidationRunID 是本次验证记录标识。
	ValidationRunID domain.ID
	TaskID          domain.ID
	StepID          domain.ID
	AttemptID       domain.ID
	// Status 是验证流程状态。
	Status domain.ValidationRunStatus
	// Verdict 是最终结论。
	Verdict domain.VerificationVerdict
	// Passed 是结论的便捷布尔值，等价于 Verdict == passed。
	Passed bool
	// Summary 是结论摘要。
	Summary string
	// FailureSummary 是失败或不确定结论的原因。
	FailureSummary string
	// Checks 是逐项检查结果。
	Checks []domain.ValidationCheckResult
	// PassedChecks / FailedChecks / SkippedChecks 是统计计数。
	PassedChecks  int
	FailedChecks  int
	SkippedChecks int
	// ExecutionEnv 是执行环境类型。
	ExecutionEnv string
	// Workspace 是执行命令的工作区标识。
	Workspace string
	// ContainerID 是执行本次验证的容器登记记录 ID。
	ContainerID string
	// ReviewRan 表示本轮验证是否真的发起了 Validator Agent 语义审查。
	ReviewRan bool
	// ReviewStatus 是语义审查的实际结论；未发起审查时为空。
	ReviewStatus domain.ReviewStatus
	// ReviewSummary 是语义审查的可读说明（已脱敏并裁剪）。
	ReviewSummary string
	// ValidatorSessionID 是语义审查使用的会话标识，未发起审查时为空。
	ValidatorSessionID domain.ID
	// Assessment 是语义审查产出的结构化评估，未取得可解释结论时为空。
	Assessment *domain.AgentAssessment
	// Persisted 表示结论与检查结果是否全部可靠写入数据库。
	Persisted bool
	// PersistenceErrors 是写入失败的原因。
	PersistenceErrors []string
	StartedAt         time.Time
	FinishedAt        time.Time
}

// ValidationStore 是验证服务依赖的存储能力组合。
type ValidationStore interface {
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
	// GetContainer 读取容器登记记录，用于核验任务容器的真实归属。
	GetContainer(ctx context.Context, id domain.ID) (*domain.Container, error)
	UpdateAttemptIfStatus(
		ctx context.Context,
		attempt domain.Attempt,
		expected []domain.AttemptStatus,
	) error
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	storage.ValidationStore
}

// ReviewPort 是验证服务对语义审查能力的需求。
//
// 实现只能返回"审查实际发生了什么"：调用失败、超时与输出不可解释都不是错误，
// 而是必须如实上报的审查状态。只有前置条件无法满足（未配置验证角色）才返回错误。
type ReviewPort interface {
	Review(ctx context.Context, request ReviewRequest) (ReviewOutcome, error)
}

// ValidationContainerPort 是验证服务对任务容器的需求。
//
// 容器只被核验与使用：验证服务不会创建、启动、删除或接管容器，也不会在容器
// 不可用时静默回退到宿主机执行。命令执行一律经由统一执行环境接口完成。
type ValidationContainerPort interface {
	// InspectContainer 核验容器当前状态。
	InspectContainer(ctx context.Context, request InspectContainerRequest) (ContainerDetail, error)
	// Exec 在受管理容器内执行一条非交互命令。
	Exec(ctx context.Context, request ExecRequest) (ExecResult, error)
}

// ValidationOptions 是构造验证服务的输入。
type ValidationOptions struct {
	// Store 是持久化入口，必填。
	Store ValidationStore
	// Containers 是容器核验与命令执行能力，必填。
	Containers ValidationContainerPort
	// OutputRoot 是检查输出的本机持久化根目录，必填。
	OutputRoot string
	// Reviewer 是语义审查能力，可选。
	//
	// 非空时，在必需确定性检查全部通过之后调用 Validator Agent；为空表示本次
	// 服务不提供语义审查，此时结论只依据确定性检查（不会被误当作"审查通过"）。
	Reviewer ReviewPort
	// Logger 是日志器，为空时使用默认日志器。
	Logger *slog.Logger
	// Now 是可注入的时钟，零值使用当前时间。
	Now func() time.Time
	// DefaultTimeout 覆盖整次验证的默认超时。
	DefaultTimeout time.Duration
}

// ValidationService 是确定性验证引擎。
//
// 职责边界：
//   - 校验任务、步骤、尝试的归属与状态，并以数据库条件更新原子占用验证；
//   - 按配置顺序执行检查，逐项记录真实结果；
//   - 必需确定性检查全部通过后，按需调用 Validator Agent 做语义审查；
//   - 依据 domain.DecideValidation 的确定性规则计算结论（Agent 结论无权改写
//     命令退出码，也无权让未执行的检查变成通过）；
//   - 把结论与检查结果持久化，并把判定写回执行尝试。
//
// 它不实现完整工作流编排：不创建容器、不自动重试、不决定任务最终状态，
// 也不决定是否要进入修复循环。语义审查只是验证决策链的一环：审查失败无法让验证
// 通过，审查通过也无法让失败的必需检查变成通过。
type ValidationService struct {
	store      ValidationStore
	containers ValidationContainerPort
	outputs    *checkOutputWriter
	logger     *slog.Logger
	now        func() time.Time
	// defaultTimeout 是整次验证的默认总超时。
	defaultTimeout time.Duration
	// running 记录本进程内正在验证的尝试，用于快速拒绝重复请求。
	// running 记录本进程内正在验证的尝试，用于快速拒绝重复请求。
	//
	// 它是第一道防线而不是一致性保证：真正的占用由数据库条件更新完成。
	running *validationRegistry
	// reviewer 是语义审查能力，为 nil 表示本次运行不提供语义审查。
	reviewer ReviewPort
	// lifecycle 管理后台服务状态与在途验证，使 Server 停止时能取消并等待收敛。
	lifecycle validationLifecycle
}

// ValidationOutputRoot 返回检查输出的本机持久化根目录。
//
// 目录与数据库放在同一数据目录下：检查输出属于本机持久化数据，不写入项目目录，
// 也不依赖容器生命周期。
func ValidationOutputRoot(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	database := strings.TrimSpace(cfg.Storage.DatabasePath)
	if database == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(database), "validations")
}

// NewValidationService 构造确定性验证引擎。
func NewValidationService(options ValidationOptions) (*ValidationService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("验证服务初始化失败: 存储不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("验证服务初始化失败: 容器能力不能为空")
	}
	root := strings.TrimSpace(options.OutputRoot)
	if root == "" {
		return nil, fmt.Errorf("验证服务初始化失败: 检查输出目录不能为空")
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
		timeout = defaultValidationTimeout
	}
	return &ValidationService{
		store:          options.Store,
		containers:     options.Containers,
		outputs:        newCheckOutputWriter(root),
		logger:         logger,
		now:            now,
		defaultTimeout: timeout,
		running:        newValidationRegistry(),
		reviewer:       options.Reviewer,
	}, nil
}

// ActiveValidations 返回本进程内正在执行的验证数量。
func (s *ValidationService) ActiveValidations() int {
	return s.running.len()
}

// Validate 执行一次确定性验证。
//
// 执行是同步的：只有验证进入终态后才返回结果，不存在"已受理但仍在后台"的语义。
// ctx 取消与服务停止都会传播到实际命令：前者收敛为 cancelled，后者收敛为
// interrupted 语义的 failed，两者都必须可区分。
func (s *ValidationService) Validate(
	ctx context.Context,
	request ValidationExecuteRequest,
) (ValidationExecuteResult, error) {
	taskID, err := parseValidationID("任务", request.TaskID)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	stepID, err := parseValidationID("步骤", request.StepID)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	attemptID, err := parseValidationID("尝试", request.AttemptID)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	task, step, attempt, err := s.loadScope(ctx, taskID, stepID, attemptID)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	if err := checkValidationStart(task, step, attempt); err != nil {
		return ValidationExecuteResult{}, err
	}
	spec, err := resolveValidationSpec(step, request.Checks)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	// 任务级 verification_timeout_seconds 是单条检查的默认超时：它只在检查本身与
	// 覆盖配置都没有声明超时时生效，并写入配置快照，使"当时用了多少超时"可审计。
	if spec.DefaultTimeoutSeconds == 0 && task.Config.VerificationTimeoutSeconds > 0 {
		spec.DefaultTimeoutSeconds = task.Config.VerificationTimeoutSeconds
	}
	container, err := s.requireContainer(ctx, task)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	environment, err := resolveValidationEnvironment(
		container,
		task.Config.EffectiveWorkDir(),
	)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	port, err := newContainerExecPort(s.containers)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	executor, err := newCheckEnvironment(port, environment)
	if err != nil {
		return ValidationExecuteResult{}, err
	}
	return s.runValidation(ctx, runValidationRequest{
		task:        task,
		step:        step,
		attempt:     attempt,
		spec:        spec,
		container:   container,
		environment: environment,
		executor:    executor,
		timeout:     s.validationTimeout(request.Timeout),
	})
}

// validationTimeout 返回本次验证的总超时时间。
//
// 它是整次验证的硬上限（所有检查共享），与"单条检查的超时"是两个不同的概念：
// 前者由请求或服务默认值决定，后者由检查配置与任务级 verification_timeout_seconds
// 决定。总超时小于单条检查超时时，流程会以 timed_out 结束，如实反映超时。
func (s *ValidationService) validationTimeout(requested time.Duration) time.Duration {
	if requested > 0 {
		return requested
	}
	return s.defaultTimeout
}

// loadScope 读取并校验任务、步骤与尝试的归属链。
func (s *ValidationService) loadScope(
	ctx context.Context,
	taskID, stepID, attemptID domain.ID,
) (domain.Task, domain.Step, domain.Attempt, error) {
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return domain.Task{}, domain.Step{}, domain.Attempt{}, err
	}
	step, err := s.loadStep(ctx, task, stepID)
	if err != nil {
		return domain.Task{}, domain.Step{}, domain.Attempt{}, err
	}
	attempt, err := s.loadAttempt(ctx, task, step, attemptID)
	if err != nil {
		return domain.Task{}, domain.Step{}, domain.Attempt{}, err
	}
	return task, step, attempt, nil
}

// loadTask 读取任务，不存在或已删除时返回"不存在"。
func (s *ValidationService) loadTask(
	ctx context.Context,
	id domain.ID,
) (domain.Task, error) {
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrValidationNotFound, id)
		}
		return domain.Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	if task.Deleted() {
		return domain.Task{}, fmt.Errorf("%w: 任务 %s", ErrValidationNotFound, id)
	}
	return *task, nil
}

// loadStep 读取步骤并确认它属于指定任务。
func (s *ValidationService) loadStep(
	ctx context.Context,
	task domain.Task,
	id domain.ID,
) (domain.Step, error) {
	step, err := s.store.GetStep(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrValidationNotFound, id)
		}
		return domain.Step{}, fmt.Errorf("读取步骤失败: %w", err)
	}
	if step.TaskID != task.ID {
		// 归属不成立与不存在返回同一错误：不能让调用方通过状态码差异探测其他任务。
		return domain.Step{}, fmt.Errorf("%w: 步骤 %s", ErrValidationNotFound, id)
	}
	return *step, nil
}

// loadAttempt 读取尝试并确认它属于指定步骤与任务。
func (s *ValidationService) loadAttempt(
	ctx context.Context,
	task domain.Task,
	step domain.Step,
	id domain.ID,
) (domain.Attempt, error) {
	attempt, err := s.store.GetAttempt(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Attempt{}, fmt.Errorf("%w: 尝试 %s", ErrValidationNotFound, id)
		}
		return domain.Attempt{}, fmt.Errorf("读取尝试失败: %w", err)
	}
	if attempt.StepID != step.ID {
		return domain.Attempt{}, fmt.Errorf("%w: 尝试 %s", ErrValidationNotFound, id)
	}
	if !attempt.TaskID.IsEmpty() && attempt.TaskID != task.ID {
		return domain.Attempt{}, fmt.Errorf("%w: 尝试 %s", ErrValidationNotFound, id)
	}
	return *attempt, nil
}

// parseValidationID 解析路径或请求中的标识。
func parseValidationID(label, raw string) (domain.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: %s标识不能为空", ErrValidationInvalidInput, label)
	}
	if strings.Contains(trimmed, "/") {
		return "", fmt.Errorf("%w: %s标识非法", ErrValidationInvalidInput, label)
	}
	return domain.ID(trimmed), nil
}

// resolveValidationSpec 解析本次验证实际使用的检查配置。
//
// 调用方可以覆盖检查集合，但不能借此放宽门槛：覆盖后的配置同样要求存在启用的
// 必需检查，且每一项都必须合法。
func resolveValidationSpec(
	step domain.Step,
	overrides []domain.ValidationCheckDefinition,
) (domain.ValidationSpec, error) {
	if len(overrides) > 0 {
		spec := domain.ValidationSpec{Checks: overrides}
		if err := validateResolvedSpec(spec); err != nil {
			return domain.ValidationSpec{}, err
		}
		return spec, nil
	}
	spec := ValidationSpecFromStep(step)
	if err := validateResolvedSpec(spec); err != nil {
		return domain.ValidationSpec{}, err
	}
	return spec, nil
}

// validateResolvedSpec 校验生效的检查配置是否可用于判定。
//
// 没有启用的必需检查时拒绝开始验证：否则会创建一条注定失败的验证记录，把配置
// 问题伪装成"验证未通过"。该规则与 domain.DecideValidation 的规则一致，只是
// 在更早的位置以参数错误暴露。
func validateResolvedSpec(spec domain.ValidationSpec) error {
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrValidationInvalidInput, err)
	}
	if !spec.HasRequiredCheck() {
		return fmt.Errorf(
			"%w: 验证配置缺少启用的必需检查，无法判定验证是否通过",
			ErrValidationInvalidInput,
		)
	}
	return nil
}

// ValidationSpecFromStep 把步骤中声明的验证命令转换为确定性检查配置。
//
// 步骤的 VerificationSpec 是既有模型，这里只做映射，不引入第二套检查定义：
// 命令名作为稳定标识，命令与参数逐项保留，必需性沿用原有语义。
func ValidationSpecFromStep(step domain.Step) domain.ValidationSpec {
	commands := step.VerificationSpec.Commands
	checks := make([]domain.ValidationCheckDefinition, 0, len(commands))
	for index, command := range commands {
		checks = append(checks, domain.ValidationCheckDefinition{
			ID:                command.Name,
			Name:              command.Name,
			Type:              domain.ValidationCheckTypeCommand,
			Enabled:           true,
			Required:          command.IsRequired(),
			Sequence:          index + 1,
			Command:           command.Command,
			Args:              command.Args,
			WorkDir:           command.WorkDir,
			TimeoutSeconds:    command.TimeoutSeconds,
			MaxOutputBytes:    command.MaxOutputBytes,
			ExpectedExitCodes: command.AllowedExitCodes,
		})
	}
	return domain.ValidationSpec{Checks: checks}
}

// checkValidationStart 校验当前状态是否允许开始验证。
//
// 只读校验：它不修改任何状态，也不创建验证记录，因此可以在事务外先做快速失败。
// 真正的占用必须在事务内以状态条件更新完成，不能只依赖本函数的判断。
func checkValidationStart(
	task domain.Task,
	step domain.Step,
	attempt domain.Attempt,
) error {
	run := domain.ValidationRun{}
	if err := run.CheckScope(task, step, attempt); err != nil {
		return fmt.Errorf("%w: %w", ErrValidationConflict, err)
	}
	if !domain.ValidationTaskStatus(task.Status) {
		return fmt.Errorf(
			"%w: 任务 %s 当前状态 %s 不允许执行验证",
			ErrValidationConflict,
			task.ID,
			task.Status,
		)
	}
	if !domain.CanValidateAttempt(attempt.Status) {
		return fmt.Errorf(
			"%w: 尝试 %s 当前状态 %s 不允许执行验证；执行尚未结束或结果无法确认",
			ErrValidationConflict,
			attempt.ID,
			attempt.Status,
		)
	}
	if task.CancelRequested {
		return fmt.Errorf(
			"%w: 任务 %s 已请求取消，不能开始新的验证",
			ErrValidationConflict,
			task.ID,
		)
	}
	return nil
}

// requireContainer 核验执行节点与任务容器，确认可以在容器内执行检查命令。
//
// 本方法不创建容器、不启动容器，也不回退到本机执行：前置条件缺失时返回明确的
// 可诊断错误，由用户先完成环境准备。
func (s *ValidationService) requireContainer(
	ctx context.Context,
	task domain.Task,
) (domain.Container, error) {
	if err := s.ensureHostUsable(ctx, task); err != nil {
		return domain.Container{}, err
	}
	// 归属核验先于任何远程操作：任务记录的 container_id 只是普通文本列，必须确认
	// 它确实指向登记了该任务归属的容器记录，否则检查命令会进入别的任务现场。
	owned, err := loadOwnedContainer(ctx, s.store, task)
	if err != nil {
		return domain.Container{}, fmt.Errorf("%w: %w", ErrValidationPrerequisite, err)
	}
	detail, err := s.containers.InspectContainer(ctx, InspectContainerRequest{
		ContainerID: owned.ID.String(),
		Sync:        true,
	})
	if err != nil {
		return domain.Container{}, fmt.Errorf(
			"%w: 核验任务容器失败: %w",
			ErrValidationPrerequisite,
			err,
		)
	}
	if detail.Container.Detached {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已脱离 AgentFlow 管理，拒绝自动接管",
			ErrValidationPrerequisite,
			detail.Container.ID,
		)
	}
	if detail.Container.State == domain.ContainerStateRemoved {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 已被删除",
			ErrValidationPrerequisite,
			detail.Container.ID,
		)
	}
	if detail.Inspect == nil || !detail.Inspect.Running {
		return domain.Container{}, fmt.Errorf(
			"%w: 任务容器 %s 当前未运行，无法执行验证",
			ErrValidationPrerequisite,
			detail.Container.ID,
		)
	}
	return detail.Container, nil
}

// ensureHostUsable 校验执行节点是否允许执行真实远程操作。
//
// 任务没有绑定执行节点时跳过：该情况会在容器核验阶段以更具体的错误报告。
func (s *ValidationService) ensureHostUsable(ctx context.Context, task domain.Task) error {
	if task.HostID == nil {
		return nil
	}
	host, err := s.store.GetExecutionHost(ctx, *task.HostID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf(
				"%w: 任务 %s 关联的执行节点 %s 不存在",
				ErrValidationPrerequisite,
				task.ID,
				*task.HostID,
			)
		}
		return fmt.Errorf("读取执行节点失败: %w", err)
	}
	if err := EnsureHostUsableForRemoteTask(*host); err != nil {
		return fmt.Errorf("%w: %w", ErrValidationPrerequisite, err)
	}
	return nil
}

// validationRegistry 记录本进程内正在验证的尝试。
//
// 它只用于快速拒绝同一进程内的重复请求与观测在途数量；数据库条件更新才是
// 唯一的一致性保证，跨进程并发同样会被拒绝。
type validationRegistry struct {
	mu      sync.Mutex
	entries map[domain.ID]struct{}
}

// newValidationRegistry 构造验证登记表。
func newValidationRegistry() *validationRegistry {
	return &validationRegistry{entries: make(map[domain.ID]struct{})}
}

// acquire 尝试登记一个正在验证的尝试。
func (r *validationRegistry) acquire(attemptID domain.ID) (func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[domain.ID]struct{})
	}
	if _, exists := r.entries[attemptID]; exists {
		return func() {}, false
	}
	r.entries[attemptID] = struct{}{}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.entries, attemptID)
	}, true
}

// len 返回正在验证的尝试数量。
func (r *validationRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// marshalSpecSnapshot 生成验证配置的 JSON 快照。
func marshalSpecSnapshot(spec domain.ValidationSpec) string {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	return string(encoded)
}
