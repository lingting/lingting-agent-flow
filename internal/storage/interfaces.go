// Package storage 定义应用层需要的持久化接口与统一存储错误。
//
// 接口只表达存储需求，不暴露 SQLite 或 SQL 驱动类型；领域对象不依赖驱动。
// 跨实体的关键状态更新必须通过 Tx 在同一事务内完成，不能在应用层分多次调用
// 独立方法后假定操作具有原子性。
package storage

import (
	"context"
	"errors"

	"github.com/lingting/agentflow/internal/domain"
)

// 统一存储错误，调用方通过 errors.Is 判断错误类别。
var (
	// ErrNotFound 表示目标记录不存在。
	ErrNotFound = errors.New("storage: 记录不存在")
	// ErrConflict 表示唯一约束冲突，例如重复主键或重复序号。
	ErrConflict = errors.New("storage: 唯一约束冲突")
	// ErrForeignKey 表示外键约束失败，引用的记录不存在或仍被引用。
	ErrForeignKey = errors.New("storage: 外键约束失败")
	// ErrInvalidData 表示数据不满足数据库约束，例如状态取值或非空校验。
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
	// DeleteRole 删除角色。仍被步骤或会话历史引用时返回 storage.ErrForeignKey，
	// 不会级联删除历史记录。
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
	// DeleteExecutionHost 删除节点配置，不删除远程工作空间或远程资源。
	DeleteExecutionHost(ctx context.Context, id domain.ID) error
	GetExecutionHost(ctx context.Context, id domain.ID) (*domain.ExecutionHost, error)
	ListExecutionHosts(ctx context.Context) ([]domain.ExecutionHost, error)
	// SetDefaultExecutionHost 在单个事务内清除原默认节点并设置新的默认节点。
	SetDefaultExecutionHost(ctx context.Context, id domain.ID) error
	// GetDefaultExecutionHost 返回当前默认节点，没有默认节点时返回 storage.ErrNotFound。
	GetDefaultExecutionHost(ctx context.Context) (*domain.ExecutionHost, error)
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
type TaskFilter struct {
	// RequirementID 限制任务所属需求。
	RequirementID domain.ID
	// Statuses 限制任务状态，为空表示不限制。
	Statuses []domain.TaskStatus
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
}

// TaskStore 保存需求的一次执行实例。
type TaskStore interface {
	CreateTask(ctx context.Context, task domain.Task) error
	UpdateTask(ctx context.Context, task domain.Task) error
	GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
	ListTasks(ctx context.Context, filter TaskFilter) ([]domain.Task, error)
}

// StepStore 保存 Planner 生成的结构化开发步骤。
type StepStore interface {
	CreateStep(ctx context.Context, step domain.Step) error
	// CreateSteps 在一个事务边界内写入同一任务的多个步骤。
	CreateSteps(ctx context.Context, steps []domain.Step) error
	UpdateStep(ctx context.Context, step domain.Step) error
	GetStep(ctx context.Context, id domain.ID) (*domain.Step, error)
	ListStepsByTask(ctx context.Context, taskID domain.ID) ([]domain.Step, error)
}

// AttemptStore 保存一个步骤的每次完整执行尝试。
type AttemptStore interface {
	CreateAttempt(ctx context.Context, attempt domain.Attempt) error
	UpdateAttempt(ctx context.Context, attempt domain.Attempt) error
	GetAttempt(ctx context.Context, id domain.ID) (*domain.Attempt, error)
	ListAttemptsByStep(ctx context.Context, stepID domain.ID) ([]domain.Attempt, error)
	// NextAttemptNumber 返回该步骤下一个可用的尝试序号。
	NextAttemptNumber(ctx context.Context, stepID domain.ID) (int, error)
}

// SessionStore 保存每次 Agent 执行会话的元数据。
type SessionStore interface {
	// CreateSession 在写入前校验会话的任务、步骤与尝试归属是否自洽。
	CreateSession(ctx context.Context, session domain.Session) error
	UpdateSession(ctx context.Context, session domain.Session) error
	GetSession(ctx context.Context, id domain.ID) (*domain.Session, error)
	ListSessionsByTask(ctx context.Context, taskID domain.ID) ([]domain.Session, error)
	ListSessionsByAttempt(ctx context.Context, attemptID domain.ID) ([]domain.Session, error)
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
type Tx interface {
	RoleStore
	ExecutionHostStore
	RequirementStore
	TaskStore
	StepStore
	AttemptStore
	SessionStore
	VerificationStore
	EventStore
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
	// Close 关闭数据库连接，已关闭时重复调用返回 nil。
	Close() error
}
