package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 正式需求确认与查询的业务错误类别。
//
// 与其余应用服务一致：调用方通过 errors.Is 判断类别并映射 HTTP 状态码，不解析错误文本。
var (
	// ErrRequirementInvalidInput 表示请求参数或要确认的方案不合法。
	ErrRequirementInvalidInput = errors.New("app: 需求请求参数非法")
	// ErrRequirementNotFound 表示需求、规划会话或所属项目不存在。
	ErrRequirementNotFound = errors.New("app: 需求目标不存在")
	// ErrRequirementRevisionNotFound 表示方案版本不存在或不属于当前规划会话。
	ErrRequirementRevisionNotFound = errors.New("app: 确认方案版本不存在")
	// ErrRequirementStateConflict 表示会话状态不允许确认，或并发确认已经推进了状态。
	ErrRequirementStateConflict = errors.New("app: 需求确认状态冲突")
	// ErrRequirementPrerequisite 表示确认所需的前置条件不满足（节点、角色、项目配置）。
	ErrRequirementPrerequisite = errors.New("app: 需求确认前置条件不满足")
	// ErrRequirementDeleteUnavailable 表示需求删除能力未装配（缺少任务执行协调器）。
	//
	// 删除必须先取得全部关联任务的执行权，否则无法排除"删除与启动并发"的现场；
	// 协调器缺失时删除失败关闭，不静默跳过并发保护。
	ErrRequirementDeleteUnavailable = errors.New("app: 需求删除能力不可用")
)

// RequirementService 是正式需求的业务编排入口。
//
// 职责边界：
//   - 把用户指定的那一版 PlanningRevision 转换成 1 个正式 Requirement 与 N 个正式 Task；
//   - 在同一个事务内写需求、任务、任务顺序与会话状态；
//   - 提供需求详情与项目需求列表的只读查询；
//   - 提供需求聚合逻辑删除：需求与其关联任务同一事务软删除，执行历史全部保留。
//
// 它不启动任务、不调用 Scheduler / TaskLaunchService / TaskWorkflowService，也不创建
// Step、Attempt、Session 或容器：生成任务之后任务保持 queued。
type RequirementService struct {
	store storage.Store
	now   func() time.Time
	// coordination 是与 TaskLaunch / Scheduler / 步骤执行共享的任务执行协调器。
	//
	// 删除流程用它取得全部关联任务的执行权，阻止新的执行入口在删除期间推进子任务；
	// 它必须是 Server 装配的同一实例，各自新建锁表会让互斥失效。
	coordination *TaskExecutionCoordinator
}

// RequirementServiceOptions 是构造需求服务的输入。
type RequirementServiceOptions struct {
	// Store 是持久化入口；确认与删除流程的原子写入使用它的 WithTx。
	Store storage.Store
	// Now 是时间源，默认返回 UTC 当前时间。
	Now func() time.Time
	// Coordination 是任务执行协调器，需求删除依赖它取得关联任务的执行权。
	//
	// 为空时确认与查询照常工作，只有删除返回 ErrRequirementDeleteUnavailable：
	// 无法取得执行权的删除不得继续。
	Coordination *TaskExecutionCoordinator
}

// NewRequirementService 构造需求服务。
func NewRequirementService(options RequirementServiceOptions) (*RequirementService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("需求服务初始化失败: 存储不能为空")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RequirementService{
		store:        options.Store,
		now:          now,
		coordination: options.Coordination,
	}, nil
}

// ConfirmRequirementRequest 是确认方案的输入。
//
// 请求只接受三项服务端无法自行推断的事实：确认哪个会话、采纳哪一版方案，以及可选的
// 执行节点。需求标识、任务列表、任务顺序、任务配置、任务状态与项目仓库配置全部由服务端
// 依据已确认的 Revision 与项目计算，调用方无法覆盖。
type ConfirmRequirementRequest struct {
	// PlanningSessionID 是待确认的规划会话标识。
	PlanningSessionID string
	// RevisionID 是用户指定采纳的方案版本标识，必须是该会话已生成的有效版本。
	RevisionID string
	// HostID 是可选执行节点；为空时使用项目的 default_host_id。
	HostID string
}

// ConfirmRequirementResult 是一次确认的创建结果。
type ConfirmRequirementResult struct {
	// Requirement 是本次创建的正式需求。
	Requirement domain.Requirement
	// Tasks 是本次创建的正式任务，按 requirement_sequence 升序排列。
	Tasks []domain.Task
	// AdoptedRevisionID 是被采纳的方案版本标识。
	AdoptedRevisionID domain.ID
}

// RequirementDetail 是需求详情：需求本身与按顺序排列的正式任务。
type RequirementDetail struct {
	Requirement domain.Requirement
	Tasks       []domain.Task
}

// Confirm 校验用户指定的方案版本，并在单个事务内生成正式需求与任务。
//
// 事务边界（任一步失败整体回滚，不留下半成品实体）：
//  1. 事务内重新读取规划会话并核验状态仍是 preview_ready；
//  2. 核验 Revision 属于该会话、快照版本可读、分析与方案通过领域校验；
//  3. 核验项目存在且未逻辑删除，与会话所属项目一致；
//  4. 解析执行节点与任务级角色，构造并校验每个任务的配置快照；
//  5. 创建 1 个 Requirement 与 N 个 Task（写入 requirement_sequence）；
//  6. 以 preview_ready 为前置条件把会话推进为 confirmed 并写入 adopted_revision_id。
//
// 并发确认只允许一个成功：状态条件更新以 preview_ready 为前置条件，落后的一方整体回滚
// 并返回冲突，不会创建第二个需求或第二批任务。
func (s *RequirementService) Confirm(
	ctx context.Context,
	request ConfirmRequirementRequest,
) (ConfirmRequirementResult, error) {
	sessionID, revisionID, err := parseConfirmRequirementRequest(request)
	if err != nil {
		return ConfirmRequirementResult{}, err
	}
	var result ConfirmRequirementResult
	err = s.store.WithTx(ctx, func(ctx context.Context, tx storage.Tx) error {
		session, err := s.readConfirmableSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		project, err := loadRequirementProject(ctx, tx, session.ProjectID)
		if err != nil {
			return err
		}
		revision, analysis, plan, err := loadConfirmableRevision(ctx, tx, sessionID, revisionID)
		if err != nil {
			return err
		}
		plannedTasks, err := plan.ConfirmableTasks()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
		}
		title, err := domain.RequirementTitleFromAnalysis(analysis)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
		}
		description, err := domain.RequirementDescriptionFromAnalysis(analysis)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
		}
		hostID, err := resolveRequirementHost(ctx, tx, request.HostID, project)
		if err != nil {
			return err
		}
		configs, err := buildConfirmedTaskConfigs(ctx, tx, project, plannedTasks)
		if err != nil {
			return err
		}
		now := s.now()
		requirement, err := domain.NewConfirmedRequirement(domain.ConfirmedRequirementInput{
			ProjectID:         project.ID,
			PlanningSessionID: session.ID,
			Title:             title,
			Description:       description,
			Priority:          domain.PriorityNormal,
			AnalysisJSON:      revision.RequirementAnalysisJSON,
			PlanJSON:          revision.ImplementationPlanJSON,
			At:                now,
		})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
		}
		if err := tx.CreateRequirement(ctx, requirement); err != nil {
			return fmt.Errorf("写入需求失败: %w", err)
		}
		tasks, err := createConfirmedTasks(ctx, tx, requirement, project, plannedTasks, configs, hostID, now)
		if err != nil {
			return err
		}
		if err := session.Confirm(revisionID, now); err != nil {
			return mapPlanningTransitionFailure(err)
		}
		if err := tx.UpdatePlanningSessionIfStatus(
			ctx,
			session,
			[]domain.PlanningSessionStatus{domain.PlanningSessionPreviewReady},
		); err != nil {
			return mapRequirementSessionUpdateFailure(err, sessionID)
		}
		result = ConfirmRequirementResult{
			Requirement:       requirement,
			Tasks:             tasks,
			AdoptedRevisionID: revisionID,
		}
		return nil
	})
	if err != nil {
		return ConfirmRequirementResult{}, err
	}
	return result, nil
}

// Get 读取单个需求的持久化事实。
//
// 读取路径不调用领域校验，也不做状态映射：数据库不限定状态取值集合，未知状态必须原样
// 返回，不能被改写为默认状态或当作损坏数据丢弃。
func (s *RequirementService) Get(ctx context.Context, requirementIDValue string) (domain.Requirement, error) {
	id := domain.ID(strings.TrimSpace(requirementIDValue))
	if err := id.Validate(); err != nil {
		return domain.Requirement{}, fmt.Errorf("%w: 需求标识不能为空", ErrRequirementInvalidInput)
	}
	requirement, err := s.store.GetRequirement(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Requirement{}, fmt.Errorf("%w: %s", ErrRequirementNotFound, id)
		}
		return domain.Requirement{}, fmt.Errorf("读取需求失败: %w", err)
	}
	return *requirement, nil
}

// readActiveRequirement 读取需求并拒绝已逻辑删除的记录。
//
// 删除是逻辑删除，行仍在数据库里；读取路径因此必须显式区分"存在但已删除"与"存在且
// 可用"，否则删除后的需求会继续出现在详情与进度查询里。
func (s *RequirementService) readActiveRequirement(
	ctx context.Context,
	requirementIDValue string,
) (domain.Requirement, error) {
	requirement, err := s.Get(ctx, requirementIDValue)
	if err != nil {
		return domain.Requirement{}, err
	}
	if requirement.Deleted() {
		return domain.Requirement{}, fmt.Errorf(
			"%w: 需求 %s 已删除",
			ErrRequirementNotFound,
			requirement.ID,
		)
	}
	return requirement, nil
}

// Detail 读取需求详情与按 requirement_sequence 排列的正式任务。
//
// 已逻辑删除的需求按"不存在"处理：删除后的需求不再出现在默认视图里，详情接口也不能
// 返回一条看起来仍然可执行的需求。
func (s *RequirementService) Detail(
	ctx context.Context,
	requirementIDValue string,
) (RequirementDetail, error) {
	requirement, err := s.readActiveRequirement(ctx, requirementIDValue)
	if err != nil {
		return RequirementDetail{}, err
	}
	tasks, err := s.listRequirementTasks(ctx, s.store, requirement.ID)
	if err != nil {
		return RequirementDetail{}, err
	}
	return RequirementDetail{Requirement: requirement, Tasks: tasks}, nil
}

// ListByProject 按项目列出正式需求。
//
// 项目必须真实存在且未被逻辑删除：项目下线后不再从该项目的入口列出需求，错误信息也要
// 能区分"标识写错"与"项目已下线"。排序由存储层固定为优先级、创建时间与写入顺序，
// 不依赖 SQLite 偶然返回顺序。
func (s *RequirementService) ListByProject(
	ctx context.Context,
	projectIDValue string,
) ([]domain.Requirement, error) {
	projectID := strings.ToLower(strings.TrimSpace(projectIDValue))
	if err := domain.RequireProjectID(projectID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
	}
	if _, err := loadRequirementProject(ctx, s.store, projectID); err != nil {
		return nil, err
	}
	requirements, err := s.store.ListRequirements(ctx, storage.RequirementFilter{ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("查询项目需求列表失败: %w", err)
	}
	return requirements, nil
}

// Progress 计算需求进度。
//
// 它只读取关联任务的真实持久化状态并交给 domain.DecideRequirementProgress 聚合：不使用
// 时间戳、接口调用结果或模型输出来推断完成度，也不在应用层重写阻塞判定。
//
// 已逻辑删除的需求按"不存在"处理：删除后的需求不再出现在默认视图，进度查询也不应
// 返回一条看起来仍然可执行的需求。
func (s *RequirementService) Progress(
	ctx context.Context,
	requirementIDValue string,
) (domain.RequirementProgress, error) {
	requirement, err := s.readActiveRequirement(ctx, requirementIDValue)
	if err != nil {
		return domain.RequirementProgress{}, err
	}
	tasks, err := s.listRequirementTasks(ctx, s.store, requirement.ID)
	if err != nil {
		return domain.RequirementProgress{}, err
	}
	facts := make([]domain.ProjectTaskFact, 0, len(tasks))
	for _, task := range tasks {
		facts = append(facts, domain.ProjectTaskFact{Requirement: requirement, Task: task})
	}
	progress := domain.DecideRequirementProgress(requirement.ID, facts)
	if err := progress.Validate(); err != nil {
		return domain.RequirementProgress{}, fmt.Errorf("需求进度计算失败: %w", err)
	}
	return progress, nil
}

// parseConfirmRequirementRequest 校验确认请求的标识书写形式。
func parseConfirmRequirementRequest(
	request ConfirmRequirementRequest,
) (domain.ID, domain.ID, error) {
	sessionID := domain.ID(strings.TrimSpace(request.PlanningSessionID))
	if err := sessionID.Validate(); err != nil {
		return "", "", fmt.Errorf("%w: 规划会话标识不能为空", ErrRequirementInvalidInput)
	}
	revisionID := domain.ID(strings.TrimSpace(request.RevisionID))
	if err := revisionID.Validate(); err != nil {
		return "", "", fmt.Errorf("%w: 方案版本标识不能为空", ErrRequirementInvalidInput)
	}
	return sessionID, revisionID, nil
}

// readConfirmableSession 在事务内读取规划会话并核验它仍处于待确认状态。
//
// 状态条件在事务内重新核验而不是只依赖事务外的预检：并发确认与迟到的取消都可能在这两
// 个时刻之间改动状态。
func (s *RequirementService) readConfirmableSession(
	ctx context.Context,
	tx storage.Tx,
	sessionID domain.ID,
) (domain.PlanningSession, error) {
	session, err := tx.GetPlanningSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.PlanningSession{}, fmt.Errorf("%w: %s", ErrRequirementNotFound, sessionID)
		}
		return domain.PlanningSession{}, fmt.Errorf("读取规划会话失败: %w", err)
	}
	if session.Status != domain.PlanningSessionPreviewReady {
		return domain.PlanningSession{}, fmt.Errorf(
			"%w: 规划会话 %s 处于 %s，只有待确认的会话才能确认",
			ErrRequirementStateConflict,
			sessionID,
			session.Status,
		)
	}
	return *session, nil
}

// loadRequirementProject 读取确认生成需求所属的项目。
func loadRequirementProject(
	ctx context.Context,
	tx storage.Tx,
	projectID string,
) (domain.Project, error) {
	trimmed := strings.ToLower(strings.TrimSpace(projectID))
	if trimmed == "" {
		return domain.Project{}, fmt.Errorf("%w: 规划会话没有关联项目", ErrRequirementNotFound)
	}
	project, err := tx.GetProject(ctx, domain.ID(trimmed))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, fmt.Errorf("%w: 项目 %s 不存在或已删除", ErrRequirementNotFound, trimmed)
		}
		return domain.Project{}, fmt.Errorf("读取项目失败: %w", err)
	}
	if project.Deleted() {
		return domain.Project{}, fmt.Errorf("%w: 项目 %s 已删除", ErrRequirementNotFound, trimmed)
	}
	return *project, nil
}

// loadConfirmableRevision 读取并校验用户指定的方案版本。
//
// 版本必须属于当前会话：读取按会话过滤，因此"属于其他会话的版本"与"版本不存在"返回同一
// 个错误，调用方无法通过错误差异探测其他会话产生了哪些版本。快照版本、需求分析与实施
// 方案都必须能独立解析，解析失败不写入任何正式实体。
func loadConfirmableRevision(
	ctx context.Context,
	tx storage.Tx,
	sessionID domain.ID,
	revisionID domain.ID,
) (domain.PlanningRevision, domain.RequirementAnalysis, domain.ImplementationPlan, error) {
	revisions, err := tx.ListPlanningRevisions(ctx, sessionID)
	if err != nil {
		return domain.PlanningRevision{}, domain.RequirementAnalysis{}, domain.ImplementationPlan{},
			fmt.Errorf("读取规划版本失败: %w", err)
	}
	var found *domain.PlanningRevision
	for index := range revisions {
		if revisions[index].ID == revisionID {
			found = &revisions[index]
			break
		}
	}
	if found == nil {
		return domain.PlanningRevision{}, domain.RequirementAnalysis{}, domain.ImplementationPlan{},
			fmt.Errorf(
				"%w: 方案版本 %s 不属于规划会话 %s",
				ErrRequirementRevisionNotFound,
				revisionID,
				sessionID,
			)
	}
	if err := found.CheckSnapshotVersion(); err != nil {
		return domain.PlanningRevision{}, domain.RequirementAnalysis{}, domain.ImplementationPlan{},
			fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
	}
	analysis, err := found.RequirementAnalysis()
	if err != nil {
		return domain.PlanningRevision{}, domain.RequirementAnalysis{}, domain.ImplementationPlan{},
			fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
	}
	plan, err := found.ImplementationPlan()
	if err != nil {
		return domain.PlanningRevision{}, domain.RequirementAnalysis{}, domain.ImplementationPlan{},
			fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
	}
	return *found, analysis, plan, nil
}

// resolveRequirementHost 解析本次确认使用的执行节点。
//
// 显式给出的节点优先，其次使用项目默认节点；两者都没有时直接失败，不静默回退到本机执行，
// 也不取执行节点列表里的第一个。规划会话自己的 HostID 只描述规划容器，不能当作任务默认
// 配置使用。
func resolveRequirementHost(
	ctx context.Context,
	tx storage.Tx,
	requested string,
	project domain.Project,
) (domain.ID, error) {
	value := strings.TrimSpace(requested)
	if value == "" {
		value = strings.TrimSpace(project.DefaultHostID)
	}
	if value == "" {
		return "", fmt.Errorf(
			"%w: 项目 %s 未设置默认执行节点，确认必须显式指定执行节点",
			ErrRequirementPrerequisite,
			project.ID,
		)
	}
	hostID := domain.ID(value)
	if err := hostID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
	}
	host, err := tx.GetExecutionHost(ctx, hostID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", fmt.Errorf("%w: 执行节点 %s 不存在", ErrRequirementPrerequisite, hostID)
		}
		return "", fmt.Errorf("读取执行节点失败: %w", err)
	}
	if !host.Enabled {
		return "", fmt.Errorf("%w: 执行节点 %s 已被禁用", ErrRequirementPrerequisite, hostID)
	}
	return host.ID, nil
}

// buildConfirmedTaskConfigs 为每个任务独立构造并校验配置快照。
//
// 配置来源固定为同一个项目快照：仓库、基础分支、环境、环境版本、缓存与工作目录都从项目
// 继承，任务级角色按固定内置角色解析。每个任务的结果独立计算并校验，循环中不共享可变
// 状态，因此不会出现"后面的任务继承了被前面修改过的配置"。
func buildConfirmedTaskConfigs(
	ctx context.Context,
	tx storage.Tx,
	project domain.Project,
	plannedTasks []domain.PlannedTask,
) ([]domain.TaskConfig, error) {
	roles, err := resolveConfirmedTaskRoles(ctx, tx)
	if err != nil {
		return nil, err
	}
	configs := make([]domain.TaskConfig, 0, len(plannedTasks))
	for _, planned := range plannedTasks {
		config := domain.TaskConfig{
			Goal:               confirmedTaskGoal(planned.Draft),
			Environment:        project.Environment,
			EnvironmentVersion: project.EnvironmentVersion,
			ProjectID:          project.ID,
			Caches:             append([]string(nil), project.Caches...),
			WorkDir:            project.EffectiveWorkDir(),
			Verification: domain.VerificationSpec{
				AcceptanceCriteria: append([]string(nil), planned.Draft.AcceptanceCriteria...),
			},
			PlannerRoleID:   roles.planner,
			DeveloperRoleID: roles.developer,
			ValidatorRoleID: roles.validator,
		}
		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf(
				"%w: 需求内第 %d 个任务的任务配置不合法: %w",
				ErrRequirementPrerequisite,
				planned.Sequence,
				err,
			)
		}
		if err := ensureTaskRoles(ctx, tx, config); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRequirementPrerequisite, err)
		}
		configs = append(configs, config)
	}
	return configs, nil
}

// confirmedTaskRoles 是确认生成任务使用的固定角色标识。
type confirmedTaskRoles struct {
	planner   domain.ID
	developer domain.ID
	validator domain.ID
}

// resolveConfirmedTaskRoles 解析确认生成任务使用的角色。
//
// 规划角色与开发角色是任务执行的必需角色：缺失、禁用或类型不符都让确认失败，不静默
// 换成别的角色。验证角色是可选的（与 TaskConfig 的语义一致）：只有在存在、启用且类型
// 正确时才写入配置快照，否则留空表示"不做辅助评估"。
func resolveConfirmedTaskRoles(ctx context.Context, tx storage.Tx) (confirmedTaskRoles, error) {
	planner, err := requireConfirmedRole(ctx, tx, domain.RoleIDPlanner, domain.RoleTypePlanner)
	if err != nil {
		return confirmedTaskRoles{}, err
	}
	developer, err := requireConfirmedRole(ctx, tx, domain.RoleIDDeveloper, domain.RoleTypeDeveloper)
	if err != nil {
		return confirmedTaskRoles{}, err
	}
	roles := confirmedTaskRoles{planner: planner, developer: developer}
	validator, err := tx.GetRole(ctx, domain.RoleIDValidator)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return roles, nil
		}
		return confirmedTaskRoles{}, fmt.Errorf("读取角色 %s 失败: %w", domain.RoleIDValidator, err)
	}
	if validator.Enabled && validator.Type == domain.RoleTypeValidator {
		roles.validator = validator.ID
	}
	return roles, nil
}

// requireConfirmedRole 读取一个必需角色并校验启用状态与类型。
func requireConfirmedRole(
	ctx context.Context,
	tx storage.Tx,
	roleID domain.ID,
	expected domain.RoleType,
) (domain.ID, error) {
	role, err := tx.GetRole(ctx, roleID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", fmt.Errorf("%w: 角色 %s 不存在", ErrRequirementPrerequisite, roleID)
		}
		return "", fmt.Errorf("读取角色 %s 失败: %w", roleID, err)
	}
	if !role.Enabled {
		return "", fmt.Errorf("%w: 角色 %s 已被禁用", ErrRequirementPrerequisite, roleID)
	}
	if role.Type != expected {
		return "", fmt.Errorf(
			"%w: 角色 %s 的类型是 %s，期望 %s",
			ErrRequirementPrerequisite,
			roleID,
			role.Type,
			expected,
		)
	}
	return role.ID, nil
}

// confirmedTaskGoal 返回确认生成任务的 Goal 文本。
//
// 正式 Task 实体本身不承载标题与描述，方案里的任务内容因此落在任务配置快照上：Goal 由
// 方案中的标题与描述按固定格式组成，验收条件写入任务级验证规格。这是唯一一处转换规则，
// 生成后不随执行过程改写。
func confirmedTaskGoal(draft domain.ImplementationPlanTask) string {
	title := strings.TrimSpace(draft.Title)
	description := strings.TrimSpace(draft.Description)
	if description == "" {
		return title
	}
	return title + "\n\n" + description
}

// createConfirmedTasks 依次创建正式任务并写入需求内顺序。
//
// 每个任务使用同一份项目继承配置快照与同一执行节点；状态由领域构造固定为 queued，本函数
// 不推进状态，也不创建 Step / Attempt / Session。
func createConfirmedTasks(
	ctx context.Context,
	tx storage.Tx,
	requirement domain.Requirement,
	project domain.Project,
	plannedTasks []domain.PlannedTask,
	configs []domain.TaskConfig,
	hostID domain.ID,
	at time.Time,
) ([]domain.Task, error) {
	if len(plannedTasks) != len(configs) {
		return nil, fmt.Errorf("需求确认失败: 任务草案与任务配置数量不一致")
	}
	repository, err := domain.ParseGitRepositoryURL(project.RepositoryURL)
	if err != nil {
		return nil, fmt.Errorf("%w: 项目仓库地址非法: %w", ErrRequirementPrerequisite, err)
	}
	baseBranch := project.NormalizedBaseBranch()
	created := make([]domain.Task, 0, len(plannedTasks))
	for index, planned := range plannedTasks {
		host := hostID
		task, taskErr := domain.NewTask(requirement.ID, repository.Raw(), baseBranch, "", at)
		if taskErr != nil {
			return nil, fmt.Errorf(
				"%w: 需求内第 %d 个任务不合法: %w",
				ErrRequirementInvalidInput,
				planned.Sequence,
				taskErr,
			)
		}
		task.Config = configs[index]
		task.HostID = &host
		if err := task.SetRequirementSequence(planned.Sequence); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
		}
		if err := task.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRequirementInvalidInput, err)
		}
		if err := tx.CreateTask(ctx, task); err != nil {
			return nil, fmt.Errorf("写入需求内第 %d 个任务失败: %w", planned.Sequence, err)
		}
		created = append(created, task)
	}
	return created, nil
}

// listRequirementTasks 读取需求下的正式任务并按需求内顺序排列。
//
// 排序规则是确定性的：有顺序的按顺序升序，没有顺序的（历史任务）排在后面并按创建时间、
// 标识升序。查询本身不依赖 requirement_sequence，因此历史任务不会被丢弃。
func (s *RequirementService) listRequirementTasks(
	ctx context.Context,
	tx storage.Tx,
	requirementID domain.ID,
) ([]domain.Task, error) {
	tasks, err := tx.ListTasks(ctx, storage.TaskFilter{RequirementID: requirementID})
	if err != nil {
		return nil, fmt.Errorf("查询需求任务失败: %w", err)
	}
	sort.SliceStable(tasks, func(left, right int) bool {
		return taskBefore(tasks[left], tasks[right])
	})
	return tasks, nil
}

// taskBefore 比较两个任务在需求内的展示顺序。
func taskBefore(left, right domain.Task) bool {
	switch {
	case left.RequirementSequence != nil && right.RequirementSequence != nil:
		if *left.RequirementSequence != *right.RequirementSequence {
			return *left.RequirementSequence < *right.RequirementSequence
		}
	case left.RequirementSequence != nil:
		return true
	case right.RequirementSequence != nil:
		return false
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID.String() < right.ID.String()
}

// mapRequirementSessionUpdateFailure 把条件更新的失败映射为需求业务错误。
func mapRequirementSessionUpdateFailure(err error, sessionID domain.ID) error {
	switch {
	case errors.Is(err, storage.ErrConflict):
		return fmt.Errorf(
			"%w: 规划会话 %s 的状态已变更，拒绝过期确认",
			ErrRequirementStateConflict,
			sessionID,
		)
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrRequirementNotFound, sessionID)
	default:
		return fmt.Errorf("更新规划会话失败: %w", err)
	}
}
