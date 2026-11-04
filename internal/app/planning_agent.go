package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// 规划 Agent 调用的错误类别。
//
// 与其余应用服务一致：调用方通过 errors.Is 判断类别，不解析错误文本。
var (
	// ErrPlanningAgentInvalidInput 表示请求参数不合法，或规划会话记录不满足领域约束。
	ErrPlanningAgentInvalidInput = errors.New("app: 规划 Agent 调用参数非法")
	// ErrPlanningAgentNotFound 表示规划会话、项目、角色配置或规划容器记录不存在。
	ErrPlanningAgentNotFound = errors.New("app: 规划 Agent 目标不存在")
	// ErrPlanningAgentPrerequisite 表示规划运行环境、角色配置或容器运行状态不满足调用条件。
	ErrPlanningAgentPrerequisite = errors.New("app: 规划 Agent 前置条件未满足")
	// ErrPlanningAgentStateConflict 表示会话状态、容器归属或角色会话绑定与调用要求冲突。
	ErrPlanningAgentStateConflict = errors.New("app: 规划 Agent 现场与调用要求冲突")
)

// PlanningAgentService 是一次规划角色 Pi 调用的编排入口。
//
// 职责边界：
//   - 读取并校验规划会话、所属项目、规划容器与固定角色配置；
//   - 为需求分析师与方案规划师分别维护稳定的 Pi 会话标识、容器内固定路径与本机归档目录；
//   - 通过 ContainerService.RunAgent 在规划容器内执行一次 Pi，并返回真实执行结果。
//
// 它不推进会话状态、不追加消息与 Revision、不解析结构化输出，也不创建或释放容器：
// 运行环境未准备时只调用 PlanningRuntimeService.Prepare 复用既有能力。外部接口不接受
// Session 路径、Pi 参数、工具列表、模型或容器标识，这些全部由服务端决定。
type PlanningAgentService struct {
	store      storage.Store
	roles      *RoleService
	containers *ContainerService
	// runtime 用于在规划运行环境尚未准备时复用既有准备流程，可为空。
	runtime *PlanningRuntimeService
	// sessionRoot 是本机 Session 持久化根目录的绝对路径。
	sessionRoot string
	// locks 按规划会话串行化规划调用：同一会话的两个角色共用一个稳定 Session 目录，
	// 并发调用会让同一份 Pi JSONL 同时被两个进程读写。
	locks keyedMutex
	now   func() time.Time
}

// PlanningAgentServiceOptions 是构造规划 Agent 服务的输入。
type PlanningAgentServiceOptions struct {
	// Store 是持久化入口。
	Store storage.Store
	// Roles 是固定角色配置的统一读取入口。
	Roles *RoleService
	// Containers 是受管理容器与容器内 Agent 调用入口。
	Containers *ContainerService
	// Runtime 是可选的规划运行时服务：会话尚未准备运行环境时用它准备容器与快照。
	Runtime *PlanningRuntimeService
	// SessionRoot 是本机 Session 持久化根目录，必须落在配置的数据目录下。
	SessionRoot string
	// Now 是时间源，默认返回 UTC 当前时间。
	Now func() time.Time
}

// NewPlanningAgentService 构造规划 Agent 服务。
func NewPlanningAgentService(
	options PlanningAgentServiceOptions,
) (*PlanningAgentService, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("规划 Agent 服务初始化失败: 存储不能为空")
	}
	if options.Roles == nil {
		return nil, fmt.Errorf("规划 Agent 服务初始化失败: 角色服务不能为空")
	}
	if options.Containers == nil {
		return nil, fmt.Errorf("规划 Agent 服务初始化失败: 容器服务不能为空")
	}
	sessionRoot, err := resolveSessionRoot(options.SessionRoot)
	if err != nil {
		return nil, fmt.Errorf("规划 Agent 服务初始化失败: %w", err)
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PlanningAgentService{
		store:       options.Store,
		roles:       options.Roles,
		containers:  options.Containers,
		runtime:     options.Runtime,
		sessionRoot: sessionRoot,
		now:         now,
	}, nil
}

// RunPlanningRoleRequest 是一次规划角色调用的输入。
//
// 它只接受规划会话、角色类型、提示词与可选的执行节点：Session 路径、Pi 命令参数、
// 模型、系统提示词、工具策略与容器标识全部由服务端按当前配置与记录决定。
type RunPlanningRoleRequest struct {
	// PlanningSessionID 是目标规划会话标识。
	PlanningSessionID string
	// RoleType 是本次调用的角色类型，只允许 requirement_analyst 与 planner。
	RoleType string
	// Prompt 是本次调用的完整提示词。
	Prompt string
	// HostID 仅在规划运行环境尚未准备时用于准备容器；已准备时不得指向其他执行节点。
	HostID string
}

// PlanningAgentResult 是一次规划角色调用的结果。
//
// 它只描述已经发生的事实：Pi 的真实执行结果、本轮使用的角色会话标识与路径。调用方
// 需要自行判断执行结果是否可用，本服务不会把失败伪装成成功。
type PlanningAgentResult struct {
	// Session 是本轮调用使用的规划会话记录。
	Session domain.PlanningSession
	// Role 是本次调用使用的固定角色配置。
	Role domain.Role
	// RoleType 是本次调用的角色类型。
	RoleType domain.RoleType
	// Container 是承载本次调用的规划容器记录。
	Container domain.Container
	// RoleSessionID 是本轮使用的角色 Pi 会话标识。
	RoleSessionID string
	// ContainerSessionPath 是容器内固定的 Session 文件路径。
	ContainerSessionPath string
	// ArchivePath 是本机可恢复的最新 Session 快照路径。
	ArchivePath string
	// SessionRoot 是本次调用使用的本机 Session 根目录。
	SessionRoot string
	// Result 是 Pi 的执行结果，其中包含稳定 Session 的恢复与归档状态。
	Result agent.RunResult
}

// RunRole 在规划容器内执行一次规划角色的 Pi 调用。
//
// 执行顺序固定为：校验请求与会话状态 → 校验所属项目 → 读取并校验固定角色配置 → 核验
// 规划容器归属与运行状态 → 确保两个角色的会话标识已绑定 → 生成稳定 Session 与本机归档
// 路径 → 通过 ContainerService.RunAgent 执行。远程执行只走现有容器服务，不直接访问
// SSH、Docker CLI 或 SFTP。
func (s *PlanningAgentService) RunRole(
	ctx context.Context,
	request RunPlanningRoleRequest,
) (PlanningAgentResult, error) {
	result := PlanningAgentResult{}
	sessionID, roleType, prompt, err := parsePlanningAgentRequest(request)
	if err != nil {
		return result, err
	}
	unlock := s.locks.Lock(sessionID.String())
	defer unlock()
	session, err := s.loadPlanningAgentSession(ctx, sessionID)
	if err != nil {
		return result, err
	}
	if err := session.Validate(); err != nil {
		return result, fmt.Errorf(
			"%w: 规划会话记录不满足领域约束: %w",
			ErrPlanningAgentInvalidInput,
			err,
		)
	}
	// 只允许分析在途的会话调用规划角色：终态、失败与未启动的会话都不能在此推断出
	// 一轮可用的分析上下文。
	if session.Status != domain.PlanningSessionAnalyzing {
		return result, fmt.Errorf(
			"%w: 规划会话处于 %s，只有 analyzing 会话可以调用规划角色",
			ErrPlanningAgentStateConflict,
			session.Status,
		)
	}
	project, err := s.loadPlanningAgentProject(ctx, session)
	if err != nil {
		return result, err
	}
	role, err := s.loadPlanningRole(ctx, roleType)
	if err != nil {
		return result, err
	}
	session, container, err := s.ensurePlanningAgentContainer(ctx, session, request.HostID)
	if err != nil {
		return result, err
	}
	session, err = s.ensureRoleSessions(ctx, session)
	if err != nil {
		return result, err
	}
	roleSessionID, err := planningRoleSessionID(session, roleType)
	if err != nil {
		return result, err
	}
	layout, err := planningAgentSessionLayout(
		s.sessionRoot,
		session.ID.String(),
		roleSessionID,
	)
	if err != nil {
		return result, err
	}
	invocationID := agent.NewInvocationID()
	outcome, runErr := s.containers.RunAgent(ctx, ContainerAgentRequest{
		ContainerID:  container.ID.String(),
		RoleType:     roleType.String(),
		Model:        role.Model,
		SystemPrompt: role.SystemPrompt,
		ToolsPolicy:  role.ToolsPolicy,
		Prompt:       prompt,
		SessionRoot:  s.sessionRoot,
		SessionID:    session.ID.String(),
		InvocationID: invocationID,
		WorkDir:      project.EffectiveWorkDir(),
		StableSession: &agent.StableSessionTarget{
			ContainerPath: layout.containerSession,
			ArchiveDir:    layout.archiveDir,
		},
	})
	result = PlanningAgentResult{
		Session:              session,
		Role:                 role,
		RoleType:             roleType,
		Container:            container,
		RoleSessionID:        roleSessionID,
		ContainerSessionPath: layout.containerSession,
		ArchivePath:          layout.archiveFile,
		SessionRoot:          s.sessionRoot,
		Result:               outcome.Result,
	}
	if runErr != nil {
		return result, fmt.Errorf("规划角色调用失败: %w", runErr)
	}
	return result, nil
}

// parsePlanningAgentRequest 校验请求参数并归一化角色类型与提示词。
func parsePlanningAgentRequest(
	request RunPlanningRoleRequest,
) (domain.ID, domain.RoleType, string, error) {
	sessionID := domain.ID(strings.TrimSpace(request.PlanningSessionID))
	if err := sessionID.Validate(); err != nil {
		return "", "", "", fmt.Errorf("%w: 规划会话标识不能为空", ErrPlanningAgentInvalidInput)
	}
	roleType, err := parsePlanningRoleType(request.RoleType)
	if err != nil {
		return "", "", "", err
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return "", "", "", fmt.Errorf("%w: 提示词不能为空", ErrPlanningAgentInvalidInput)
	}
	return sessionID, roleType, prompt, nil
}

// loadPlanningAgentSession 读取规划会话，并把"记录不存在"映射为业务错误。
func (s *PlanningAgentService) loadPlanningAgentSession(
	ctx context.Context,
	sessionID domain.ID,
) (domain.PlanningSession, error) {
	session, err := s.store.GetPlanningSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.PlanningSession{}, fmt.Errorf(
				"%w: 规划会话 %s 不存在",
				ErrPlanningAgentNotFound,
				sessionID,
			)
		}
		return domain.PlanningSession{}, fmt.Errorf("读取规划会话失败: %w", err)
	}
	return *session, nil
}

// loadPlanningAgentProject 读取并校验规划会话所属项目。
func (s *PlanningAgentService) loadPlanningAgentProject(
	ctx context.Context,
	session domain.PlanningSession,
) (domain.Project, error) {
	projectID := domain.ID(strings.TrimSpace(session.ProjectID))
	if err := projectID.Validate(); err != nil {
		return domain.Project{}, fmt.Errorf(
			"%w: 规划会话的项目标识非法: %w",
			ErrPlanningAgentInvalidInput,
			err,
		)
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.Project{}, fmt.Errorf(
				"%w: 规划会话所属项目 %s 不存在或已删除",
				ErrPlanningAgentNotFound,
				projectID,
			)
		}
		return domain.Project{}, fmt.Errorf("读取规划会话所属项目失败: %w", err)
	}
	if project.Deleted() {
		return domain.Project{}, fmt.Errorf(
			"%w: 项目 %s 已删除，不能再调用规划角色",
			ErrPlanningAgentPrerequisite,
			projectID,
		)
	}
	if project.ID != session.ProjectID {
		return domain.Project{}, fmt.Errorf(
			"%w: 规划会话与项目不一致，拒绝把调用落到另一个项目",
			ErrPlanningAgentStateConflict,
		)
	}
	return *project, nil
}
