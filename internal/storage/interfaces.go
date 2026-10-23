// Package storage 定义应用层需要的持久化接口与统一存储错误。
//
// 接口只表达存储需求，不暴露 SQLite 或 SQL 驱动类型；领域对象不依赖驱动。
// 跨实体的关键状态更新必须通过 Tx 在同一事务内完成，不能在应用层分多次调用
// 独立方法后假定操作具有原子性。
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 统一存储错误，调用方通过 errors.Is 判断错误类别。
var (
	// ErrNotFound 表示目标记录不存在。
	ErrNotFound = errors.New("storage: 记录不存在")
	// ErrConflict 表示唯一约束冲突，例如重复主键或重复序号。
	ErrConflict = errors.New("storage: 唯一约束冲突")
	// ErrForeignKey 表示引用完整性校验失败，引用的记录不存在或仍被引用。
	//
	// 业务 Schema 不声明外键，该错误由应用层显式校验产生，保留它是为了让调用方
	// 继续按"引用不成立"处理，而不必区分校验发生在数据库还是应用层。
	ErrForeignKey = errors.New("storage: 引用完整性校验失败")
	// ErrInvalidData 表示数据不满足校验，例如非空、长度或字段组合不一致。
	ErrInvalidData = errors.New("storage: 数据不符合约束")
	// ErrBusy 表示数据库被其他连接或进程占用，等待锁超时。
	ErrBusy = errors.New("storage: 数据库忙")
	// ErrUnsupportedSchema 表示数据库版本高于当前程序支持的版本。
	ErrUnsupportedSchema = errors.New("storage: 不支持的数据库版本")
)

// RoleSeed 是角色初始化的最小定义，由配置层转换后传入。
type RoleSeed struct {
	ID           domain.ID
	Name         string
	Type         domain.RoleType
	Runner       string
	Model        string
	SystemPrompt string
	ToolsPolicy  domain.ToolsPolicy
}

// RoleStore 保存运行时角色配置。
type RoleStore interface {
	CreateRole(ctx context.Context, role domain.Role) error
	UpdateRole(ctx context.Context, role domain.Role) error
	// DeleteRole 逻辑删除角色：只标记删除时间并禁用，不删除历史引用。
	// 被历史步骤或会话引用时仍然成功，记录不会级联删除。
	DeleteRole(ctx context.Context, id domain.ID) error
	GetRole(ctx context.Context, id domain.ID) (*domain.Role, error)
	ListRoles(ctx context.Context) ([]domain.Role, error)
}

// ExecutionHostStore 保存通过 SSH 管理的执行节点。
//
// 接口只表达存储需求：默认节点的唯一性由数据库约束保证，切换默认节点必须通过
// SetDefaultExecutionHost 在单个事务内完成，不能先查询再分两次写入。
type ExecutionHostStore interface {
	CreateExecutionHost(ctx context.Context, host domain.ExecutionHost) error
	// UpdateExecutionHost 按主键更新节点配置，is_default 与 enabled 也随记录一起写入。
	UpdateExecutionHost(ctx context.Context, host domain.ExecutionHost) error
	// DeleteExecutionHost 逻辑删除节点配置，不删除远程工作空间或远程资源。
	// 历史容器、环境版本与任务记录保留节点标识，不会被级联删除。
	DeleteExecutionHost(ctx context.Context, id domain.ID) error
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
	ListExecutionHosts(ctx context.Context) ([]domain.ExecutionHost, error)
	// SetDefaultExecutionHost 在单个事务内清除原默认节点并设置新的默认节点。
	SetDefaultExecutionHost(ctx context.Context, id domain.ID) error
	// GetDefaultExecutionHost 返回当前默认节点，没有默认节点时返回 storage.ErrNotFound。
	GetDefaultExecutionHost(ctx context.Context) (*domain.ExecutionHost, error)
	// UpdateExecutionHostValidation 以校验代次为条件写入校验状态与结果。
	//
	// 只有数据库中的代次仍等于 generation 时才写入，返回 false 表示本次结果已过期
	// 且没有任何字段被修改。该校验代次条件更新是"旧结果不得覆盖新配置"的落点。
	UpdateExecutionHostValidation(
		ctx context.Context,
		host domain.ExecutionHost,
		generation int64,
	) (bool, error)
	// ListExecutionHostsByValidationStatus 返回指定校验状态的全部未删除节点。
	//
	// 服务启动时用它恢复停留在 checking 的记录。
	ListExecutionHostsByValidationStatus(
		ctx context.Context,
		status domain.ValidationStatus,
	) ([]domain.ExecutionHost, error)
}

// RequirementFilter 约束需求查询条件，零值表示不限制。
type RequirementFilter struct {
	// Statuses 限制需求状态，为空表示不限制。
	Statuses []domain.RequirementStatus
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
}

// RequirementStore 保存用户提交的原始需求。
type RequirementStore interface {
	CreateRequirement(ctx context.Context, requirement domain.Requirement) error
	UpdateRequirement(ctx context.Context, requirement domain.Requirement) error
	GetRequirement(ctx context.Context, id domain.ID) (*domain.Requirement, error)
	ListRequirements(ctx context.Context, filter RequirementFilter) ([]domain.Requirement, error)
}

// TaskFilter 约束任务查询条件，零值表示不限制。
//
// 逻辑删除的任务默认不返回：只有 IncludeDeleted 为 true 时才把已删除记录纳入
// 查询范围，用于审计与历史核对。
type TaskFilter struct {
	// RequirementID 限制任务所属需求。
	RequirementID domain.ID
	// HostID 限制任务所属执行节点。
	HostID domain.ID
	// Statuses 限制任务状态，为空表示不限制。
	Statuses []domain.TaskStatus
	// ExcludeStatuses 排除指定状态，用于筛选未结束任务。
	ExcludeStatuses []domain.TaskStatus
	// IncludeDeleted 表示把已逻辑删除的任务也纳入查询。
	IncludeDeleted bool
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
	// Offset 跳过前面若干条记录，用于分页；小于等于 0 表示不跳过。
	Offset int
}

// TaskPage 是一次分页查询的结果。
//
// Total 是满足筛选条件的总记录数，与 Limit / Offset 无关，调用方据此计算页数。
type TaskPage struct {
	// Tasks 是当前页的任务，按创建时间倒序排列。
	Tasks []domain.Task
	// Total 是满足筛选条件的记录总数。
	Total int
}

// TaskStore 保存需求的一次执行实例。
//
// 关键状态更新必须使用条件更新接口：UpdateTaskIfStatus 只在数据库中的状态仍属于
// 允许集合时才写入，避免过期的执行结果覆盖更新的状态。
type TaskStore interface {
	CreateTask(ctx context.Context, task domain.Task) error
	UpdateTask(ctx context.Context, task domain.Task) error
	// UpdateTaskIfStatus 在数据库中的状态仍属于 expected 时写入任务。
	//
	// 状态不匹配时返回 storage.ErrConflict，调用方不得假定更新成功。
	UpdateTaskIfStatus(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
	) error
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	// GetTaskByIdempotencyKey 按幂等键查询任务，不存在时返回 storage.ErrNotFound。
	GetTaskByIdempotencyKey(ctx context.Context, key string) (*domain.Task, error)
	// ListTasks 按创建时间倒序返回任务，默认不包含已逻辑删除的记录。
	ListTasks(ctx context.Context, filter TaskFilter) ([]domain.Task, error)
	// ListTaskPage 按筛选条件分页返回任务，并给出满足条件的总数。
	//
	// 总数与分页参数无关，调用方不需要另外实现一次 COUNT 查询。
	ListTaskPage(ctx context.Context, filter TaskFilter) (TaskPage, error)
	// DeleteTask 逻辑删除任务：只标记 deleted_at，不删除任务行与历史记录。
	//
	// 条件更新保证并发安全：数据库中的任务必须未被删除且仍处于 expected
	// 状态之一，否则返回 storage.ErrConflict。
	DeleteTask(
		ctx context.Context,
		id domain.ID,
		expected []domain.TaskStatus,
		at time.Time,
	) error
	// AcquireTaskLease 以原子条件更新领取任务租约。
	//
	// 只有任务处于允许开始的状态、且当前租约为空或已过期时才成功；
	// 否则返回 storage.ErrConflict，保证同一任务不会被两个执行者同时推进。
	AcquireTaskLease(ctx context.Context, request TaskLeaseRequest) error
	// ReleaseTaskLease 释放由 owner 持有的租约，其他持有者的租约不会被清除。
	ReleaseTaskLease(ctx context.Context, id domain.ID, owner string, at time.Time) error
}

// TaskLeaseRequest 是一次任务租约领取请求。
type TaskLeaseRequest struct {
	// TaskID 是目标任务。
	TaskID domain.ID
	// Owner 是执行者标识，必须非空。
	Owner string
	// TTL 是租约有效期，必须大于 0。
	TTL time.Duration
	// Statuses 是允许领取的状态集合，为空表示只允许可开始的状态。
	Statuses []domain.TaskStatus
	// At 是领取时间，零值使用当前时间。
	At time.Time
}

// StepStore 保存 Planner 生成的结构化开发步骤。
type StepStore interface {
	CreateStep(ctx context.Context, step domain.Step) error
	// CreateSteps 在一个事务边界内写入同一任务的多个步骤。
	CreateSteps(ctx context.Context, steps []domain.Step) error
	UpdateStep(ctx context.Context, step domain.Step) error
	// UpdateStepIfStatus 在数据库中的状态仍属于 expected 时写入步骤。
	//
	// 状态不匹配时返回 storage.ErrConflict，调用方不得假定更新成功。旧尝试的迟到
	// 结果必须经过该条件更新，不能无条件覆盖更新的步骤状态。
	UpdateStepIfStatus(
		ctx context.Context,
		step domain.Step,
		expected []domain.StepStatus,
	) error
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
	// ListStepPage 按序号稳定排序分页返回任务的步骤，并给出总数。
	ListStepPage(ctx context.Context, filter StepFilter) (StepPage, error)
	// ListTasksWithOccupiedSteps 返回存在执行占用步骤的任务标识。
	//
	// 占用指 running / developing / verifying：这些状态表示"曾经有一次执行拿到了
	// 占用"。进程在 Agent 调用期间退出时，占用会永久留在数据库里并阻止该步骤再次
	// 执行，因此启动恢复需要先找到这些任务。查询只读，不修改任何记录。
	ListTasksWithOccupiedSteps(ctx context.Context) ([]domain.ID, error)
}

// StepFilter 约束步骤查询条件，零值表示不限制。
type StepFilter struct {
	// TaskID 限制步骤所属任务，必填。
	TaskID domain.ID
	// Statuses 限制步骤状态，为空表示不限制。
	Statuses []domain.StepStatus
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
	// Offset 跳过前面若干条记录，小于等于 0 表示不跳过。
	Offset int
}

// StepPage 是一次步骤分页查询的结果。
type StepPage struct {
	// Steps 是当前页的步骤，按 sequence 升序排列。
	Steps []domain.Step
	// Total 是满足筛选条件的记录总数。
	Total int
}

// AttemptStore 保存一个步骤的每次完整执行尝试。
type AttemptStore interface {
	CreateAttempt(ctx context.Context, attempt domain.Attempt) error
	// StartAttempt 在一个事务内分配尝试序号、创建 Attempt 并写入。
	//
	// 序号分配与插入必须在同一事务内完成：先查最大序号再插入会让并发执行拿到相同
	// 序号，唯一约束只能事后报错，无法保证"每次重试都得到新序号"。由于序号只有
	// 存储层知道，Attempt 实体也由存储层在事务内构造，调用方不能自带序号。
	StartAttempt(
		ctx context.Context,
		request StartAttemptRequest,
	) (domain.Attempt, error)
	// UpdateAttempt 按主键更新尝试。
	//
	// 只允许从 running 结束一次；已结束的尝试不会被重新打开，迟到的结果必须先用
	// GetAttempt 读取当前状态再经状态机判定。
	UpdateAttempt(ctx context.Context, attempt domain.Attempt) error
	// UpdateAttemptIfStatus 在数据库中的状态仍属于 expected 时写入尝试。
	//
	// 状态不匹配时返回 storage.ErrConflict：旧尝试的迟到结果不能覆盖新状态。
	UpdateAttemptIfStatus(
		ctx context.Context,
		attempt domain.Attempt,
		expected []domain.AttemptStatus,
	) error
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	ListAttemptsByStep(ctx context.Context, stepID domain.ID) ([]domain.Attempt, error)
	// ListAttemptPage 按尝试序号稳定排序分页返回步骤的尝试，并给出总数。
	ListAttemptPage(ctx context.Context, filter AttemptFilter) (AttemptPage, error)
	// NextAttemptNumber 返回该步骤下一个可用的尝试序号。
	//
	// 只用于展示与预估，不能作为写入依据：写入必须使用 StartAttempt。
	NextAttemptNumber(ctx context.Context, stepID domain.ID) (int, error)
}

// StartAttemptRequest 是一次"开始执行尝试"的输入。
//
// 只包含调用方确实知道的信息：序号由存储层分配，开始时间由调用方按业务时钟给出。
type StartAttemptRequest struct {
	// TaskID 是尝试所属任务，必须与步骤所属任务一致。
	TaskID domain.ID
	// StepID 是尝试所属步骤。
	StepID domain.ID
	// At 是尝试的开始时间，零值使用当前时间。
	At time.Time
}

// AttemptFilter 约束尝试查询条件，零值表示不限制。
type AttemptFilter struct {
	// StepID 限制尝试所属步骤，必填。
	StepID domain.ID
	// Statuses 限制尝试状态，为空表示不限制。
	Statuses []domain.AttemptStatus
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
	// Offset 跳过前面若干条记录，小于等于 0 表示不跳过。
	Offset int
}

// AttemptPage 是一次尝试分页查询的结果。
type AttemptPage struct {
	// Attempts 是当前页的尝试，按 attempt_number 升序排列。
	Attempts []domain.Attempt
	// Total 是满足筛选条件的记录总数。
	Total int
}

// SessionStore 保存每次 Agent 执行会话的元数据。
type SessionStore interface {
	// CreateSession 在写入前校验会话的任务、步骤与尝试归属是否自洽。
	CreateSession(ctx context.Context, session domain.Session) error
	UpdateSession(ctx context.Context, session domain.Session) error
	// UpdateSessionArchive 只更新会话的归档状态与归档位置。
	//
	// 归档与执行结果、HTML 导出相互独立，因此单独提供一个只改归档字段的入口，
	// 避免为了写归档结果而整体覆盖会话状态。
	UpdateSessionArchive(ctx context.Context, session domain.Session) error
	GetSession(ctx context.Context, id domain.ID) (*domain.Session, error)
	ListSessionsByTask(ctx context.Context, taskID domain.ID) ([]domain.Session, error)
	ListSessionsByAttempt(ctx context.Context, attemptID domain.ID) ([]domain.Session, error)
	// ListSessionPage 按创建时间稳定排序分页返回会话，并给出总数。
	ListSessionPage(ctx context.Context, filter SessionFilter) (SessionPage, error)
}

// SessionFilter 约束会话查询条件，零值表示不限制。
type SessionFilter struct {
	// TaskID 限制会话所属任务，必填。
	TaskID domain.ID
	// StepID 限制会话所属步骤。
	StepID *domain.ID
	// AttemptID 限制会话所属尝试。
	AttemptID *domain.ID
	// RoleID 限制会话的 Agent 角色。
	RoleID *domain.ID
	// Statuses 限制会话执行状态，为空表示不限制。
	Statuses []domain.SessionStatus
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
	// Offset 跳过前面若干条记录，小于等于 0 表示不跳过。
	Offset int
}

// SessionPage 是一次会话分页查询的结果。
type SessionPage struct {
	// Sessions 是当前页的会话，按创建时间升序排列。
	Sessions []domain.Session
	// Total 是满足筛选条件的记录总数。
	Total int
}

// VerificationStore 保存每次验证的结构化结果，历史记录只追加不覆盖。
type VerificationStore interface {
	// CreateVerificationResult 在写入前校验任务、步骤与尝试的归属是否自洽。
	CreateVerificationResult(ctx context.Context, result domain.VerificationResult) error
	GetVerificationResult(ctx context.Context, id domain.ID) (*domain.VerificationResult, error)
	ListVerificationResultsByAttempt(
		ctx context.Context,
		attemptID domain.ID,
	) ([]domain.VerificationResult, error)
}

// ValidationRunStartRequest 是一次"占用并开始验证"的输入。
//
// 只包含调用方已经确认的信息：验证记录本身由存储层在同一事务内创建，
// 调用方不能指定任意标识或时间。
type ValidationRunStartRequest struct {
	// TaskID 是任务标识，必须与步骤所属任务一致。
	TaskID domain.ID
	// StepID 是待验证的步骤。
	StepID domain.ID
	// AttemptID 是待验证的执行尝试。
	AttemptID domain.ID
	// ExpectedTaskStatus 是占用时数据库中任务必须处于的状态。
	ExpectedTaskStatus domain.TaskStatus
	// Spec 是本次验证的配置快照。
	Spec domain.ValidationSpec
	// SpecSnapshot 是配置的 JSON 快照。
	SpecSnapshot string
	// ExecutionEnv 是执行环境类型。
	ExecutionEnv string
	// Workspace 是执行命令的工作区标识。
	Workspace string
	// ContainerID 是执行本次验证的容器登记记录 ID。
	ContainerID string
	// At 是验证开始时间，零值使用当前时间。
	At time.Time
}

// ValidationRunFinishRequest 是一次"写入验证结论"的输入。
type ValidationRunFinishRequest struct {
	// RunID 是验证记录标识。
	RunID domain.ID
	// Status 是验证流程终态。
	Status domain.ValidationRunStatus
	// Verdict 是最终结论。
	Verdict domain.VerificationVerdict
	// Summary 是结论摘要。
	Summary string
	// FailureSummary 是失败或不确定结论的原因。
	FailureSummary string
	// ErrorDetail 是验证流程自身的错误。
	ErrorDetail string
	// PassedChecks / FailedChecks / SkippedChecks 是统计计数。
	PassedChecks  int
	FailedChecks  int
	SkippedChecks int
	// Duration 是验证总耗时。
	Duration time.Duration
	// At 是结束时间，零值使用当前时间。
	At time.Time
}

// ValidationStore 保存确定性验证的运行记录与逐项检查结果。
//
// 写入分为两步：先以状态条件占用并创建验证记录，再逐项追加检查结果，最后写入
// 结论。这样部分检查完成后发生错误时，已经产生的证据仍然可追溯，不会因为最后
// 一项失败而整体丢失。
type ValidationStore interface {
	// BeginValidationRun 在单个事务内校验归属、占用验证并创建验证记录。
	//
	// 校验内容包括：任务与步骤归属一致、尝试属于该步骤、任务状态仍为期望值、
	// 该尝试没有正在运行的验证。任一条件不成立返回 ErrConflict，调用方据此返回
	// 状态冲突，而不是启动第二组检查。
	BeginValidationRun(
		ctx context.Context,
		request ValidationRunStartRequest,
	) (domain.ValidationRun, error)
	// GetValidationRun 读取单条验证记录。
	GetValidationRun(ctx context.Context, id domain.ID) (*domain.ValidationRun, error)
	// ListValidationRunsByAttempt 按创建时间返回某次尝试的全部验证记录。
	ListValidationRunsByAttempt(
		ctx context.Context,
		attemptID domain.ID,
	) ([]domain.ValidationRun, error)
	// ListValidationRunsByStep 按创建时间返回某个步骤的全部验证记录。
	//
	// 步骤级查询用于"最新验证结果"：重试会创建新的尝试，历史验证记录仍然保留。
	ListValidationRunsByStep(
		ctx context.Context,
		stepID domain.ID,
	) ([]domain.ValidationRun, error)
	// FinishValidationRun 以"仍在运行"为条件写入结论与终态。
	//
	// 已确认终态不会被迟到结果覆盖：状态不匹配返回 ErrConflict。
	FinishValidationRun(ctx context.Context, request ValidationRunFinishRequest) error
	// FailValidationRun 把验证记录收敛为失败终态并记录原因。
	FailValidationRun(
		ctx context.Context,
		id domain.ID,
		status domain.ValidationRunStatus,
		reason string,
		at time.Time,
	) error
	// AppendValidationCheckResult 追加一条检查结果。
	//
	// 逐项追加而不是一次性写入：部分检查完成后发生错误时，已有结果仍然可追溯。
	AppendValidationCheckResult(
		ctx context.Context,
		result domain.ValidationCheckResult,
	) error
	// ListValidationCheckResults 按执行顺序返回某个验证记录的全部检查结果。
	ListValidationCheckResults(
		ctx context.Context,
		runID domain.ID,
	) ([]domain.ValidationCheckResult, error)
}

// ValidationFeedbackStore 保存验证失败的结构化反馈。
//
// 反馈与原始 Validation Run 一起保留，不替代它：Validation Run 保存流程状态与最终
// 结论，反馈保存"下一次 Developer 执行可以引用的失败问题清单"。
type ValidationFeedbackStore interface {
	// CreateValidationFailureFeedback 追加一条失败反馈。
	//
	// 同一轮验证至多一条反馈，重复写入返回 ErrConflict；历史反馈不会被覆盖。
	CreateValidationFailureFeedback(
		ctx context.Context,
		feedback domain.ValidationFailureFeedback,
	) error
	// GetValidationFailureFeedback 读取某轮验证的失败反馈，不存在时返回 ErrNotFound。
	GetValidationFailureFeedback(
		ctx context.Context,
		runID domain.ID,
	) (*domain.ValidationFailureFeedback, error)
	// LatestValidationFailureFeedbackByStep 返回步骤最近一次的失败反馈。
	//
	// 用于显式重试时引用上一轮问题；没有反馈时返回 ErrNotFound。
	LatestValidationFailureFeedbackByStep(
		ctx context.Context,
		stepID domain.ID,
	) (*domain.ValidationFailureFeedback, error)
}

// EventFilter 约束事件查询条件，零值表示不限制。
type EventFilter struct {
	// StepID 限制事件所属步骤。
	StepID *domain.ID
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
}

// EventStore 保存任务工作流的关键事件，只提供追加与查询。
type EventStore interface {
	// AppendEvent 追加事件并返回带自增标识的记录。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	ListEventsByTask(ctx context.Context, taskID domain.ID, filter EventFilter) ([]domain.Event, error)
}

// RoleSeeder 提供角色初始化所需的最低能力。
//
// 初始化只在角色表为空时导入，日常启动不覆盖已有角色；对已有角色的显式同步
// 必须作为独立操作实现，不能隐含在普通启动流程中。
type RoleSeeder interface {
	SeedRoles(ctx context.Context, seeds []RoleSeed) (int, error)
}

// Tx 在一个事务内提供各实体的存储能力。
//
// 通过 WithTx 取得，事务的提交与回滚由存储层管理，调用方不直接持有事务对象。
// EnvironmentVersionFilter 约束环境版本查询条件，零值表示不限制。
type EnvironmentVersionFilter struct {
	// HostID 限制版本所属节点。
	HostID domain.ID
	// Ref 限制版本所属环境，Name 为空表示不限制名称。
	Ref *domain.EnvironmentRef
	// Statuses 限制构建状态，为空表示不限制。
	Statuses []domain.BuildStatus
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
}

// EnvironmentVersionStore 保存不可覆盖的环境配置版本。
//
// 版本记录只追加不覆盖：CreateEnvironmentVersion 遇到已存在的版本号返回
// storage.ErrConflict，历史版本内容与哈希不得被新版本修改。
type EnvironmentVersionStore interface {
	CreateEnvironmentVersion(ctx context.Context, version domain.EnvironmentVersion) error
	// UpdateEnvironmentVersion 只更新构建状态、镜像 ID、构建时间与错误摘要。
	UpdateEnvironmentVersion(ctx context.Context, version domain.EnvironmentVersion) error
	GetEnvironmentVersion(
		ctx context.Context,
		hostID domain.ID,
		ref domain.EnvironmentRef,
		number int,
	) (*domain.EnvironmentVersion, error)
	ListEnvironmentVersions(
		ctx context.Context,
		filter EnvironmentVersionFilter,
	) ([]domain.EnvironmentVersion, error)
	// NextEnvironmentVersionNumber 返回该环境下已用最大版本号加一。
	NextEnvironmentVersionNumber(
		ctx context.Context,
		hostID domain.ID,
		ref domain.EnvironmentRef,
	) (int, error)
}

// ContainerStore 保存 AgentFlow 管理的远程容器登记记录。
type ContainerStore interface {
	CreateContainer(ctx context.Context, container domain.Container) error
	// UpdateContainer 按主键更新容器记录，不存在时返回 storage.ErrNotFound。
	UpdateContainer(ctx context.Context, container domain.Container) error
	GetContainer(ctx context.Context, id domain.ID) (*domain.Container, error)
	// GetContainerByName 按节点与容器名称查询，用于恢复远程操作后的状态。
	GetContainerByName(ctx context.Context, hostID domain.ID, name string) (*domain.Container, error)
	ListContainers(ctx context.Context, filter domain.ContainerFilter) ([]domain.Container, error)
}

// CodeSaveStore 保存代码保存操作的记录。
//
// Commit、Push 与 Merge 的结果在同一条记录内分别保存；远程 Git 操作与本机
// 数据库无法共享事务，因此每次状态推进后都要单独写入，使中断后仍能定位最后
// 一个已确认完成的阶段。
type CodeSaveStore interface {
	CreateCodeSaveRecord(ctx context.Context, record domain.CodeSaveRecord) error
	// UpdateCodeSaveRecord 按主键更新记录，不存在时返回 storage.ErrNotFound。
	UpdateCodeSaveRecord(ctx context.Context, record domain.CodeSaveRecord) error
	// UpdateCodeSaveRecovery 以读取时的恢复结论为条件写入一次只读核验结论。
	//
	// 只写 recovery_status 与 updated_at 两列：核验是只读流程，不得借一次更新把
	// 提交、推送、合并状态一并覆盖回读取时的旧值。expected 是读取记录时的
	// RecoveryStatus（空值表示尚未核验），数据库中的取值已变化时返回
	// storage.ErrConflict，调用方必须重新读取真实记录而不是覆盖新事实。
	UpdateCodeSaveRecovery(
		ctx context.Context,
		id domain.ID,
		expected domain.RecoveryStatus,
		status domain.RecoveryStatus,
		at time.Time,
	) error
	GetCodeSaveRecord(ctx context.Context, id domain.ID) (*domain.CodeSaveRecord, error)
	// ListCodeSaveRecordsByTask 按任务列出记录，最新的在前。
	ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
}

// CodeFetchStore 保存代码初始化（代码获取 + 任务分支准备）的执行记录。
//
// 记录只追加不覆盖：一次初始化对应一条记录，失败与成功都必须落库，使"同一
// Attempt 是否已经初始化成功"可以由记录直接判断，而不是重新探测容器内文件系统。
type CodeFetchStore interface {
	CreateCodeFetchRecord(ctx context.Context, record domain.CodeFetchRecord) error
	// UpdateCodeFetchRecord 按主键更新记录，不存在时返回 storage.ErrNotFound。
	//
	// 已进入终态的记录不会被重新打开：调用方必须先读取当前状态再经状态机判定。
	UpdateCodeFetchRecord(ctx context.Context, record domain.CodeFetchRecord) error
	GetCodeFetchRecord(ctx context.Context, id domain.ID) (*domain.CodeFetchRecord, error)
	// ListCodeFetchRecordsByAttempt 按尝试列出记录，最新的在前。
	ListCodeFetchRecordsByAttempt(
		ctx context.Context,
		attemptID string,
	) ([]domain.CodeFetchRecord, error)
	// ListCodeFetchRecordsByTask 按任务列出记录，最新的在前。
	ListCodeFetchRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeFetchRecord, error)
}

// TaskWorkspaceInitStore 保存任务级工作区初始化（容器内代码获取 + 任务分支准备）的记录。
//
// 它与 CodeFetchStore 是不同层次的概念：CodeFetchStore 记录"针对某个 Attempt 的
// 一次初始化"，本接口记录"某个任务在某个容器代次上的工作区是否已就绪"。统一任务
// 启动发生在任何 Step / Attempt 存在之前，因此不能复用 code_fetches 的必填字段。
//
// 记录只追加不覆盖：一次初始化对应一条记录，失败与成功都必须落库，使"该任务在该
// 容器上是否已初始化成功"可以由记录直接判断，而不是重新探测容器内文件系统。
type TaskWorkspaceInitStore interface {
	CreateTaskWorkspaceInit(ctx context.Context, record domain.TaskWorkspaceInitRecord) error
	// UpdateTaskWorkspaceInit 按主键更新记录，不存在时返回 storage.ErrNotFound。
	//
	// 已进入终态的记录不会被重新打开：调用方必须先读取当前状态再经状态机判定。
	UpdateTaskWorkspaceInit(ctx context.Context, record domain.TaskWorkspaceInitRecord) error
	GetTaskWorkspaceInit(
		ctx context.Context,
		id domain.ID,
	) (*domain.TaskWorkspaceInitRecord, error)
	// ListTaskWorkspaceInitsByTask 按任务列出记录，最新的在前。
	ListTaskWorkspaceInitsByTask(
		ctx context.Context,
		taskID string,
	) ([]domain.TaskWorkspaceInitRecord, error)
	// ListTaskWorkspaceInitsByContainer 按容器列出记录，最新的在前。
	ListTaskWorkspaceInitsByContainer(
		ctx context.Context,
		containerID string,
	) ([]domain.TaskWorkspaceInitRecord, error)
	// ListInterruptedTaskWorkspaceInits 返回仍停留在 pending / running 的记录。
	//
	// 启动恢复用它定位"进程退出后仍被占用"的初始化现场，不重放任何远程操作。
	ListInterruptedTaskWorkspaceInits(
		ctx context.Context,
	) ([]domain.TaskWorkspaceInitRecord, error)
}

type Tx interface {
	RoleStore
	ExecutionHostStore
	RequirementStore
	TaskStore
	StepStore
	AttemptStore
	SessionStore
	VerificationStore
	// ValidationStore 提供确定性验证的运行记录与检查结果持久化。
	// ValidationStore 提供确定性验证的运行记录与检查结果持久化。
	ValidationStore
	// ValidationFeedbackStore 提供验证失败反馈的追加与查询。
	ValidationFeedbackStore
	EventStore
	EnvironmentVersionStore
	ContainerStore
	CodeSaveStore
	// CodeFetchStore 提供代码初始化记录的追加与查询。
	CodeFetchStore
	// TaskWorkspaceInitStore 提供任务级工作区初始化记录的追加与查询。
	TaskWorkspaceInitStore
	// StepExecutionStore 提供单步骤执行的原子占用，与其余实体共享同一事务边界。
	StepExecutionStore
	// WorkflowRunStore 提供任务级工作流执行记录的追加与推进。
	WorkflowRunStore
}

// Store 是持久化的统一入口。
//
// 直接调用 Store 上的方法等价于每个方法各自一个事务；需要跨实体原子更新时
// 使用 WithTx，并在回调内使用传入的 Tx，不得混用 Store 上的方法。
type Store interface {
	Tx
	RoleSeeder
	// WithTx 在单个事务内执行 fn，fn 返回错误或发生 panic 时回滚。
	WithTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// Ping 检查存储是否可用，不读写任何业务数据。
	//
	// 健康检查用它区分"HTTP 服务已启动"与"SQLite 实际可用"，不得用其他
	// 业务查询代替。
	Ping(ctx context.Context) error
	// Close 关闭数据库连接，已关闭时重复调用返回 nil。
	Close() error
}
