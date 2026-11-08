// Package storage 定义应用层需要的持久化接口与统一存储错误。
//
// 接口只表达存储需求，不暴露 SQLite 或 SQL 驱动类型；领域对象不依赖驱动。
// 跨实体的关键状态更新必须通过 Tx 在同一事务内完成，不能在应用层分多次调用
// 独立方法后假定操作具有原子性。
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
)

// RoleSeed 是角色初始化的最小定义：由内置的固定角色默认值转换为存储定义后传入。
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
	// ProjectID 限制需求所属项目，空值表示不限制。
	//
	// 历史手工需求没有项目关联，因此它不会出现在任何项目需求列表中：空值不是
	// "默认项目"，而是"这个需求确实没有项目来源"。
	ProjectID string
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

// ProjectFilter 约束项目查询条件，零值表示不限制。
//
// 逻辑删除的项目默认不返回：只有 IncludeDeleted 为 true 时才把已删除记录纳入
// 查询范围，用于审计与历史核对。
type ProjectFilter struct {
	// IncludeDeleted 表示把已逻辑删除的项目也纳入查询。
	IncludeDeleted bool
}

// ProjectStore 保存项目登记信息。
//
// 项目标识由调用方给出并与缓存卷共享范围、远程管理工作目录名绑定，因此读取必须
// 区分"记录不存在"（ErrNotFound）与"记录已删除"：前者说明标识写错，后者说明项目
// 已下线，两者都不能被当作可用项目继续建任务。
type ProjectStore interface {
	CreateProject(ctx context.Context, project domain.Project) error
	// UpdateProject 按主键更新项目，标识不可修改。
	UpdateProject(ctx context.Context, project domain.Project) error
	// DeleteProject 逻辑删除项目：只写 deleted_at，不删除历史引用。
	DeleteProject(ctx context.Context, id domain.ID) error
	GetProject(ctx context.Context, id domain.ID) (*domain.Project, error)
	ListProjects(ctx context.Context, filter ProjectFilter) ([]domain.Project, error)
}

// ProjectExecutionState 是项目执行意图的持久化记录。
//
// 它不是第二套状态机：接口返回的项目状态由 domain.DecideProjectExecution 依据任务真实
// 状态、执行权与停止意图计算；这里只保存"最后一次决策写下的记录"与用户的停止意图，
// 使停止请求与执行权归属在进程重启后仍然可识别。
type ProjectExecutionState struct {
	// ProjectID 是项目标识。
	ProjectID domain.ID
	// State 是最后一次决策写下的状态记录，枚举式取值不受数据库白名单限制。
	State domain.ProjectExecutionState
	// StopRequestedAt 非空表示用户已请求停止领取新任务。
	StopRequestedAt *time.Time
	// UpdatedAt 是记录写入时间。
	UpdatedAt time.Time
}

// StopRequested 判断用户是否已请求停止领取新任务。
func (s ProjectExecutionState) StopRequested() bool {
	return s.StopRequestedAt != nil
}

// ProjectTaskFact 是项目内一个正式任务连同所属需求的持久化事实。
//
// 任务归属项目只有一条路径：requirements.project_id。历史手工任务没有项目关联，因此
// 不属于任何项目，也不会出现在项目执行计划里。
type ProjectTaskFact struct {
	// Requirement 是任务所属需求的持久化记录。
	Requirement domain.Requirement
	// Task 是项目内的正式任务。
	Task domain.Task
}

// ProjectExecutionStore 保存项目级执行权、项目执行意图与项目任务集合。
//
// 三种能力都只做条件写入或只读查询：
//   - 取得执行权只允许"没有登记"或"已到期"，未到期的执行权不能被抢占；
//   - 续租、释放都带 owner + epoch 条件，旧持有者的迟到写入被拒绝；
//   - 释放不删除行、不重置 epoch，释放后按到期处理，允许重新取得并递增 epoch。
//
// 并发正确性由 SQL 条件写入保证：调用方不得把进程内互斥当作唯一保证。
type ProjectExecutionStore interface {
	// AcquireProjectExecutionLease 以条件写入取得项目执行权。
	//
	// 没有登记或已有登记已到期时成功，并返回取得后的记录（epoch 已递增）；未到期时
	// 返回 storage.ErrConflict，调用方必须按"项目正忙"处理。
	AcquireProjectExecutionLease(
		ctx context.Context,
		projectID domain.ID,
		owner string,
		now time.Time,
		expiresAt time.Time,
	) (domain.ProjectExecutionLease, error)
	// RenewProjectExecutionLease 以 owner + epoch 为条件延长执行权有效期。
	//
	// 条件不成立（执行权已换人、已过期或记录不存在）时返回 storage.ErrConflict，
	// 调用方必须停止继续领取任务。
	RenewProjectExecutionLease(
		ctx context.Context,
		projectID domain.ID,
		owner string,
		epoch int64,
		now time.Time,
		expiresAt time.Time,
	) error
	// ReleaseProjectExecutionLease 以 owner + epoch 为条件释放执行权。
	//
	// 释放只把有效期置为 now，不删除行、不重置 epoch；条件不成立返回
	// storage.ErrConflict，旧持有者不得释放新持有者的执行权。
	ReleaseProjectExecutionLease(
		ctx context.Context,
		projectID domain.ID,
		owner string,
		epoch int64,
		now time.Time,
	) error
	// GetProjectExecutionLease 读取项目执行权，不存在时返回 storage.ErrNotFound。
	GetProjectExecutionLease(ctx context.Context, projectID domain.ID) (*domain.ProjectExecutionLease, error)
	// ListProjectExecutionLeases 返回指定持有者的全部执行权登记，按项目标识排序。
	ListProjectExecutionLeases(ctx context.Context, owner string) ([]domain.ProjectExecutionLease, error)
	// GetProjectExecutionState 读取项目执行意图，不存在时返回 storage.ErrNotFound。
	GetProjectExecutionState(ctx context.Context, projectID domain.ID) (*ProjectExecutionState, error)
	// SetProjectExecutionState 整体写入项目执行意图（按项目标识覆盖）。
	SetProjectExecutionState(ctx context.Context, state ProjectExecutionState) error
	// ListProjectTaskFacts 返回项目内未逻辑删除的正式任务与所属需求。
	//
	// 顺序由领域层的项目内排序规则决定，存储层不实现业务排序。
	ListProjectTaskFacts(ctx context.Context, projectID domain.ID) ([]ProjectTaskFact, error)
}

// PlanningSessionStore 保存需求规划会话及其消息与方案版本。
//
// 规划会话与 Task / Step / Attempt / Session 执行记录是两套模型：规划阶段只产生
// 消息、分析与方案草案，不产生正式任务，因此不复用执行记录表，也不写入它们。
//
// 消息序号与方案版本号由存储层在写入事务内分配：调用方不能自带序号覆盖历史记录，
// 返回的实体带有实际分配的序号。
type PlanningSessionStore interface {
	CreatePlanningSession(ctx context.Context, session domain.PlanningSession) error
	// UpdatePlanningSession 按主键更新规划会话，所属项目不可修改。
	//
	// 它写入整行字段（业务状态与运行时绑定一起写），因此只适用于调用方确实持有该会话
	// 全部最新事实的场景。规划运行时保存绑定必须使用 UpdatePlanningSessionRuntimeBinding
	// 的条件更新，否则会用旧对象覆盖并发推进的状态、方案版本与角色会话标识。
	UpdatePlanningSession(ctx context.Context, session domain.PlanningSession) error
	// UpdatePlanningSessionIfStatus 在数据库中的状态仍属于 expected 时写入规划会话。
	//
	// 状态不匹配时返回 storage.ErrConflict：迟到的规划结果不能覆盖已确认、已放弃或
	// 已失败的会话结论。
	UpdatePlanningSessionIfStatus(
		ctx context.Context,
		session domain.PlanningSession,
		expected []domain.PlanningSessionStatus,
	) error
	// UpdatePlanningSessionRuntimeBinding 以调用方读到的运行时绑定为条件写入新的运行时绑定。
	//
	// 它只更新 host_id、container_id、source_commit 与 updated_at：status、finished_at、
	// adopted_revision_id、error_summary 与两个角色的 Pi 会话标识由业务操作维护，运行时
	// 绑定写入不得覆盖它们。expected 是读取会话时看到的绑定，next 是准备写入的绑定；
	// 条件不成立说明绑定已被其他操作改写，返回 storage.ErrConflict，会话不存在返回
	// storage.ErrNotFound。调用方必须重新读取会话，不能用旧绑定覆盖新事实。
	UpdatePlanningSessionRuntimeBinding(
		ctx context.Context,
		id domain.ID,
		expected PlanningSessionRuntimeBinding,
		next PlanningSessionRuntimeBinding,
		at time.Time,
	) error
	// UpdatePlanningSessionRoleBindings 以调用方读到的角色会话绑定为条件写入新的绑定。
	//
	// 它只更新 analyst_session_id、planner_session_id 与 updated_at：状态、结束时间、
	// 采纳的方案版本、失败摘要、运行时绑定与所属项目保持数据库中的值。两个角色的标识
	// 成对写入，避免并发初始化时只覆盖其中一个；条件不成立说明绑定已被其他操作改写，
	// 返回 storage.ErrConflict，会话不存在返回 storage.ErrNotFound。调用方必须重新读取
	// 会话并复用获胜的绑定，不能退化成无条件覆盖。
	UpdatePlanningSessionRoleBindings(
		ctx context.Context,
		id domain.ID,
		expected PlanningSessionRoleBinding,
		next PlanningSessionRoleBinding,
		at time.Time,
	) error
	// GetPlanningSession 读取单个规划会话，不存在时返回 storage.ErrNotFound。
	GetPlanningSession(ctx context.Context, id domain.ID) (*domain.PlanningSession, error)
	// ListPlanningSessionsByProject 按项目列出规划会话，最近的在前。
	ListPlanningSessionsByProject(
		ctx context.Context,
		projectID string,
	) ([]domain.PlanningSession, error)
	// AppendPlanningMessage 在一个事务内分配消息序号并追加一条消息。
	AppendPlanningMessage(
		ctx context.Context,
		request AppendPlanningMessageRequest,
	) (domain.PlanningMessage, error)
	// ListPlanningMessages 按序号升序返回会话的全部消息。
	ListPlanningMessages(ctx context.Context, sessionID domain.ID) ([]domain.PlanningMessage, error)
	// AppendPlanningRevision 在一个事务内分配版本号并追加一个方案版本。
	AppendPlanningRevision(
		ctx context.Context,
		request AppendPlanningRevisionRequest,
	) (domain.PlanningRevision, error)
	// ListPlanningRevisions 按版本号升序返回会话的全部方案版本。
	ListPlanningRevisions(
		ctx context.Context,
		sessionID domain.ID,
	) ([]domain.PlanningRevision, error)
	// ClaimPlanningAnalysis 以数据库条件更新抢占一轮分析的执行权。
	//
	// 抢占条件固定为"会话状态是 analyzing 且当前没有占用，或占用标识就是本次提交的
	// 标识"：前者是首次取得执行权，后者是同一次执行在重试入口预先占用之后的确认，
	// 因此重复确认是幂等的。影响行数为 0 说明状态不允许或已被其他执行者占用，返回
	// storage.ErrConflict。占用标识由调用方生成，释放时必须提交同一个标识，所以并发
	// 抢占只有一方能开始两阶段模型调用。
	ClaimPlanningAnalysis(ctx context.Context, id domain.ID, claimID string, at time.Time) error
	// ReleasePlanningAnalysis 以占用标识为条件释放一轮分析的执行权。
	//
	// 只清除 analysis_claim_id 与 analysis_claimed_at 并刷新 updated_at：会话状态、
	// 结束时间、采纳的方案版本、失败摘要与运行时绑定保持数据库中的值。标识不匹配说明
	// 占用已不属于本次调用，返回 storage.ErrConflict，调用方不得覆盖别人的结论。
	ReleasePlanningAnalysis(ctx context.Context, id domain.ID, claimID string, at time.Time) error
	// ListPlanningAnalysisClaims 返回当前仍持有分析占用的规划会话。
	//
	// 它只按"存在占用"筛选：状态条件由调用方再次核对，服务端启动恢复据此判断上一次
	// 进程是否留下无人推进的分析执行，不用于任何跨进程租约或续租。
	ListPlanningAnalysisClaims(ctx context.Context) ([]PlanningAnalysisClaim, error)
	// PlanningAnalysisClaim 返回单个会话当前的分析占用标识，没有占用时返回空字符串。
	//
	// 它只用于"在同一个事务里先读出占用、再按该标识释放"的显式终止操作：终止一轮
	// 运行必须同时清掉它的占用，否则失败会话会带着无人推进的占用无法重试。会话不存在
	// 时返回 storage.ErrNotFound。
	PlanningAnalysisClaim(ctx context.Context, id domain.ID) (string, error)
}

// PlanningAnalysisClaim 是一条仍然有效的分析占用记录。
//
// 它是"某个执行者正在推进这一轮分析"的唯一持久化证据：没有它就不存在执行权，因此
// 服务端启动恢复只处理带占用的会话，不推断"分析可能中断了"。
type PlanningAnalysisClaim struct {
	// SessionID 是持有占用的规划会话。
	SessionID domain.ID
	// ClaimID 是执行者生成的占用标识，恢复与释放都必须带同一个标识。
	ClaimID string
	// ClaimedAt 是占用写入时间。
	ClaimedAt time.Time
}

// PlanningSessionRuntimeBinding 是规划会话的运行期绑定。
//
// 它只包含执行节点、规划容器与固定代码快照：这三项由规划运行时准备与释放，与会话业务
// 状态、采纳的方案版本、失败摘要以及两个角色的 Pi 会话标识互不相干。条件更新用它表达
// "更新前的预期绑定"，因此比较必须把空值与 NULL 视为同一个"未绑定"状态。
type PlanningSessionRuntimeBinding struct {
	// HostID 是承载规划容器的执行节点。
	HostID *domain.ID
	// ContainerID 是规划容器的登记记录标识。
	ContainerID string
	// SourceCommit 是代码快照固定的完整 commit SHA。
	SourceCommit string
}

// PlanningSessionRuntimeBindingOf 读出规划会话当前的运行时绑定。
func PlanningSessionRuntimeBindingOf(session domain.PlanningSession) PlanningSessionRuntimeBinding {
	return PlanningSessionRuntimeBinding{
		HostID:       session.HostID,
		ContainerID:  session.ContainerID,
		SourceCommit: session.SourceCommit,
	}
}

// Empty 判断运行时绑定是否已全部清空。
//
// 释放运行环境时写入空绑定：它表示"会话不再占用运行时资源"，与业务状态无关，已确认或
// 已放弃的会话同样需要清理容器与快照的关联。
func (b PlanningSessionRuntimeBinding) Empty() bool {
	return b.HostID == nil &&
		strings.TrimSpace(b.ContainerID) == "" &&
		strings.TrimSpace(b.SourceCommit) == ""
}

// PlanningSessionRoleBinding 是规划会话两个角色的 Pi 会话标识绑定。
//
// 它与运行时绑定互不相干：执行节点、规划容器与固定代码快照由规划运行时维护，角色会话
// 标识由规划 Agent 调用方维护。条件更新用它表达"更新前的预期绑定"，因此比较必须把
// 空值与 NULL 视为同一个"未绑定"状态。
type PlanningSessionRoleBinding struct {
	// AnalystSessionID 是需求分析师角色的 Pi 侧会话标识。
	AnalystSessionID string
	// PlannerSessionID 是方案规划师角色的 Pi 侧会话标识。
	PlannerSessionID string
}

// PlanningSessionRoleBindingOf 读出规划会话当前的角色会话绑定。
func PlanningSessionRoleBindingOf(session domain.PlanningSession) PlanningSessionRoleBinding {
	return PlanningSessionRoleBinding{
		AnalystSessionID: session.AnalystSessionID,
		PlannerSessionID: session.PlannerSessionID,
	}
}

// Complete 判断两个角色的会话标识是否都已绑定。
func (b PlanningSessionRoleBinding) Complete() bool {
	return strings.TrimSpace(b.AnalystSessionID) != "" &&
		strings.TrimSpace(b.PlannerSessionID) != ""
}

// AppendPlanningMessageRequest 是一次"追加规划消息"的输入。
//
// 它不包含序号与标识：序号由存储层在事务内分配，标识由领域层构造，从而保证调用方
// 无法用自带序号覆盖已经写给用户的历史消息。
type AppendPlanningMessageRequest struct {
	// PlanningSessionID 是消息所属规划会话。
	PlanningSessionID domain.ID
	// SenderType 是消息发送方类型。
	SenderType domain.PlanningSenderType
	// RoleID 是发送消息的角色配置标识；用户与系统消息为空。
	RoleID *domain.ID
	// Content 是消息正文，按原样保存。
	Content string
	// At 是消息创建时间，零值使用当前时间。
	At time.Time
}

// AppendPlanningRevisionRequest 是一次"追加规划版本"的输入。
//
// 与消息一样不包含版本号与标识：版本号由存储层在事务内分配，历史版本只追加不覆盖。
type AppendPlanningRevisionRequest struct {
	// PlanningSessionID 是版本所属规划会话。
	PlanningSessionID domain.ID
	// RequirementAnalysisJSON 是需求分析师的结构化分析快照。
	RequirementAnalysisJSON json.RawMessage
	// ImplementationPlanJSON 是方案规划师的结构化方案快照。
	ImplementationPlanJSON json.RawMessage
	// ValidationResultJSON 是本轮方案校验结果，为空表示尚未校验。
	ValidationResultJSON json.RawMessage
	// At 是版本生成时间，零值使用当前时间。
	At time.Time
}

// TaskFilter 约束任务查询条件，零值表示不限制。
//
// 逻辑删除的任务默认不返回：只有 IncludeDeleted 为 true 时才把已删除记录纳入
// 查询范围，用于审计与历史核对。
type TaskFilter struct {
	// RequirementID 限制任务所属需求。
	RequirementID domain.ID
	// ProjectID 限制任务所属项目。
	//
	// 任务归属项目只有一条路径：requirements.project_id。该条件因此按需求表的项目关联
	// 过滤，并且只匹配未逻辑删除的需求：已删除需求下的任务不再属于任何项目视图。
	ProjectID string
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
	// UpdateTaskCancellation 在任务状态仍属于 expected 时写入取消请求。
	//
	// 它只更新取消相关列（status、cancel_requested、cancel_requested_at、
	// final_result、failure_summary、finished_at、updated_at），不触碰
	// 取消列与任务状态：取消是用户意图，不是执行权交接。
	//
	// 状态不匹配时返回 storage.ErrConflict，调用方不得假定取消已被记录。
	UpdateTaskCancellation(
		ctx context.Context,
		task domain.Task,
		expected []domain.TaskStatus,
	) error
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
	// ListAttemptsByStatus 返回全部处于指定状态的尝试，按开始时间升序。
	//
	// 启动恢复用它定位"进程退出后仍停留在 running"的残留：这类记录没有任何执行者
	// 会再推进，必须由恢复流程收敛。
	ListAttemptsByStatus(
		ctx context.Context,
		statuses []domain.AttemptStatus,
	) ([]domain.Attempt, error)
}

// StartAttemptRequest 是一次"开始执行尝试"的输入。
//
// 只包含调用方确实知道的信息：序号由存储层分配，开始时间由调用方按业务时钟给出。
type StartAttemptRequest struct {
	// TaskID 是尝试所属任务，必须与步骤所属任务一致。
	TaskID domain.ID
	// StepID 是尝试所属步骤。
	StepID domain.ID
	// BudgetScope 是本次尝试消耗的预算作用域。
	//
	// 为空表示该尝试不属于任何已登记的执行周期（历史数据或首轮执行）。
	BudgetScope domain.RepairBudgetScope
	// BudgetScopeID 是本次尝试所属的执行周期标识。
	//
	// 只有用户显式开启的新周期才会传入不同的取值：恢复与重复调用必须复用同一个
	// 标识，因此修复预算在进程重启后仍然一致。
	BudgetScopeID domain.ID
	// At 是尝试的开始时间，零值使用当前时间。
	At time.Time
}

// RepairCycleStore 保存开发执行周期的登记与结束。
//
// 周期是"修复预算算在哪一次逻辑执行里"的持久化依据：只有用户显式入口才能开启
// 新周期，进程重启、HTTP 重试与 workflow 恢复都必须复用同一个周期标识。
type RepairCycleStore interface {
	// StartRepairCycle 在一个事务内分配周期序号并写入新的活动周期。
	//
	// 序号分配与插入共享同一事务：先查最大值再插入会让并发开启拿到相同序号。
	// 同一步骤已有活动周期时返回 ErrConflict，调用方必须重新读取现场。
	StartRepairCycle(ctx context.Context, request StartRepairCycleRequest) (domain.RepairCycle, error)
	// CloseRepairCycle 结束一个活动周期，重复结束是幂等的。
	CloseRepairCycle(ctx context.Context, id domain.ID, at time.Time) error
	// GetActiveRepairCycle 返回步骤当前的活动周期。
	//
	// 没有活动周期时返回 ErrNotFound：调用方据此判断"首轮执行"与"周期已结束"。
	GetActiveRepairCycle(ctx context.Context, stepID domain.ID) (*domain.RepairCycle, error)
	// ListRepairCyclesByStep 按周期序号返回步骤的全部周期登记。
	ListRepairCyclesByStep(ctx context.Context, stepID domain.ID) ([]domain.RepairCycle, error)
}

// StartRepairCycleRequest 是一次"开启执行周期"的输入。
type StartRepairCycleRequest struct {
	// TaskID 是周期所属任务，必须与步骤所属任务一致。
	TaskID domain.ID
	// StepID 是周期所属步骤。
	StepID domain.ID
	// MaxAttempts 是新周期生效的尝试次数上限（含首轮）。
	MaxAttempts int
	// OpenedBy 是开启来源的稳定标签。
	OpenedBy string
	// At 是开启时间，零值使用当前时间。
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
	// ListSessionsByStatus 返回全部处于指定执行状态的会话，按创建时间升序。
	//
	// 启动恢复用它定位"进程退出后仍停留在 starting / running"的残留会话。
	ListSessionsByStatus(
		ctx context.Context,
		statuses []domain.SessionStatus,
	) ([]domain.Session, error)
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
	// ReviewStatus 是语义审查这一次实际发生了什么。
	//
	// 它与 Status / Verdict 互相独立：审查调用失败不得改写确定性检查流程状态，
	// 也不得让结论变成通过。零值表示本次没有要求语义审查。
	ReviewStatus domain.ReviewStatus
	// ReviewSummary 是语义审查的可读说明（已脱敏并裁剪）。
	ReviewSummary string
	// Assessment 是语义审查产出的结构化评估，未取得可解释结论时为空。
	Assessment *domain.AgentAssessment
	// ValidatorSessionID 是本次语义审查使用的会话标识，未发起审查时为空。
	ValidatorSessionID *domain.ID
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
	// ListValidationRunsByStatus 返回全部处于指定流程状态的验证记录。
	//
	// 启动恢复用它定位"进程退出后仍停留在 pending / running"的残留验证：
	// 这类记录会永久阻塞同一尝试的新验证，必须被收敛为终态。
	ListValidationRunsByStatus(
		ctx context.Context,
		statuses []domain.ValidationRunStatus,
	) ([]domain.ValidationRun, error)
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
//
// 排序固定为 (created_at, id) 升序：`id` 是自增主键，作为同一毫秒内多条事件
// 的稳定次序。由于 id 单调递增，`(created_at, id)` 的顺序在分页查询中也是稳定
// 的（键唯一），不会漏行也不会重复行。
type EventFilter struct {
	// StepID 限制事件所属步骤。
	StepID *domain.ID
	// Types 限制事件类型，为空表示不限制。
	//
	// 类型取值不做白名单校验：历史数据中可能存在当前版本不再定义的类型，
	// 这样的记录必须仍可被按类型查询出来，而不是被静默丢弃。
	Types []domain.EventType
	// Limit 限制返回条数，小于等于 0 表示不限制。
	Limit int
	// Offset 是跳过的记录条数，小于等于 0 表示不跳过。
	Offset int
}

// EventStore 保存任务工作流的关键事件，只提供追加与查询。
type EventStore interface {
	// AppendEvent 追加事件并返回带自增标识的记录。
	AppendEvent(ctx context.Context, event domain.Event) (domain.Event, error)
	// ListEventsByTask 按 (created_at, id) 升序返回任务的事件时间线。
	ListEventsByTask(ctx context.Context, taskID domain.ID, filter EventFilter) ([]domain.Event, error)
	// CountEventsByTask 返回满足筛选条件的事件总数，供分页响应使用。
	CountEventsByTask(ctx context.Context, taskID domain.ID, filter EventFilter) (int, error)
	// ListEventsByTaskAfterID 按 id 升序返回任务中 id 大于 afterID 的事件。
	//
	// 排序刻意只用 id 而不复用 `(created_at, id)`：`id` 是自增主键，代表写入先后，
	// 实时推送的游标语义是"已推送到哪一条"，必须与这个单调序列一致。若按
	// `(created_at, id)` 排序并同时按 id 截断，一批结果里的最大 id 可能不是游标之后
	// 的最大 id，推进游标就会静默漏掉已经写入的事件。
	//
	// 已存在的 idx_events_task (task_id, id) 索引直接服务该查询：按 id 范围扫描后再按
	// 任务过滤，不需要统计总数，也不随历史增长退化为全表扫描。
	ListEventsByTaskAfterID(
		ctx context.Context,
		taskID domain.ID,
		afterID int64,
		limit int,
	) ([]domain.Event, error)
	// LatestEventIDByTask 返回任务当前最新的自增事件标识，任务没有事件时返回 0。
	//
	// 实时推送用它在"没有游标"时确定起点，历史分页用它给出读取时刻的最新事件标识，
	// 两者都只需要一次索引查询，不需要读取事件内容。
	LatestEventIDByTask(ctx context.Context, taskID domain.ID) (int64, error)
}

// RoleSeeder 提供角色初始化所需的最低能力。
//
// 初始化按固定角色标识补齐缺失记录，已有角色（含用户修改过的配置）不被覆盖；
// 角色集合固定为四个，不提供新增或删除入口。
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
	// UpdateEnvironmentVersion 只更新构建状态、镜像 ID、构建时间与错误摘要。
	UpdateEnvironmentVersion(ctx context.Context, version domain.EnvironmentVersion) error
	// UpdateEnvironmentVersionIfStatus 在库中状态仍属于 expected 时更新构建结果。
	//
	// 它用于收敛"没有执行者仍在推进"的构建记录：状态前置条件使迟到的构建结果不会
	// 覆盖已被收敛的结论。状态不在 expected 中时返回 storage.ErrConflict。
	UpdateEnvironmentVersionIfStatus(
		ctx context.Context,
		version domain.EnvironmentVersion,
		expected []domain.BuildStatus,
	) error
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
	// UpdateContainerIfState 在数据库中的管理状态仍属于 expected 时写入容器记录。
	//
	// 恢复与收敛路径必须使用本方法：影响行数为 0 说明状态已被其他执行者推进，
	// 按 storage.ErrConflict 返回，调用方必须重新读取真实记录而不是覆盖新结论。
	UpdateContainerIfState(
		ctx context.Context,
		container domain.Container,
		expected []domain.ContainerState,
	) error
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
	// UpdateCodeSaveRecoveryResult 以读取时的恢复结论为条件写入一次完整的核验结果。
	//
	// 与 UpdateCodeSaveRecovery 的区别：核验确认某个 Git 操作已经发生时，必须把
	// 该阶段的状态、提交 SHA 与恢复结论一起收敛，否则"已确认发生"这个事实只存在于
	// 返回值里，下一次读取仍然看到 pending，恢复就会反复要求核验。
	//
	// 条件仍然以读取时的 recovery_status 为准：核验期间记录被其他执行推进时返回
	// storage.ErrConflict，调用方必须重新读取真实记录而不是覆盖新事实。
	UpdateCodeSaveRecoveryResult(
		ctx context.Context,
		record domain.CodeSaveRecord,
		expected domain.RecoveryStatus,
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
	// UpdateCodeFetchRecordIfStatus 在数据库中的状态仍属于 expected 时写入记录。
	//
	// 恢复与收敛路径必须使用本方法：影响行数为 0 说明状态已被其他执行者推进，
	// 按 storage.ErrConflict 返回，调用方必须重新读取真实记录而不是覆盖新结论。
	UpdateCodeFetchRecordIfStatus(
		ctx context.Context,
		record domain.CodeFetchRecord,
		expected []domain.CodeFetchStatus,
	) error
	GetCodeFetchRecord(ctx context.Context, id domain.ID) (*domain.CodeFetchRecord, error)
	// ListCodeFetchRecordsByAttempt 按尝试列出记录，最新的在前。
	ListCodeFetchRecordsByAttempt(
		ctx context.Context,
		attemptID string,
	) ([]domain.CodeFetchRecord, error)
	// ListCodeFetchRecordsByTask 按任务列出记录，最新的在前。
	ListCodeFetchRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeFetchRecord, error)
	// ListInterruptedCodeFetchRecords 返回仍停留在 pending / running 的记录。
	//
	// 启动恢复用它定位"进程退出后仍处于活动状态"的初始化现场，不重放任何容器内
	// Git 操作；是否仍有执行者由调用方按任务执行权判定。
	ListInterruptedCodeFetchRecords(ctx context.Context) ([]domain.CodeFetchRecord, error)
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
	// UpdateTaskWorkspaceInitIfStatus 在数据库中的状态仍属于 expected 时写入记录。
	//
	// 恢复与收敛路径必须使用本方法：影响行数为 0 说明状态已被其他执行者推进，
	// 按 storage.ErrConflict 返回，调用方必须重新读取真实记录而不是覆盖新结论。
	UpdateTaskWorkspaceInitIfStatus(
		ctx context.Context,
		record domain.TaskWorkspaceInitRecord,
		expected []domain.TaskWorkspaceInitStatus,
	) error
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
	// ProjectStore 提供项目登记信息的读写；项目是任务必填的关联对象。
	ProjectStore
	// ProjectExecutionStore 提供项目执行权、项目执行意图与项目任务集合的读写。
	//
	// 它只做条件写入与只读查询：并发正确性来自 SQL 条件，不来自进程内互斥。
	ProjectExecutionStore
	// PlanningSessionStore 提供规划会话、消息与方案版本的读写；规划会话不进入
	// Task 执行记录。
	PlanningSessionStore
	StepStore
	AttemptStore
	SessionStore
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
	// RepairCycleStore 提供开发执行周期的登记与查询，是修复预算的持久化依据。
	RepairCycleStore
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
