package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/lingting/agentflow/internal/app"
)

// 具体接口路径，供路由注册与文档引用共用。
const (
	// PathHealth 是健康检查路径。
	PathHealth = APIPrefix + "/health"
	// PathSystemInfo 是系统信息路径。
	PathSystemInfo = APIPrefix + "/system/info"
)

// Deps 是构造 API 处理器所需的依赖。
type Deps struct {
	// Roles 是角色配置服务，为空时角色接口返回服务不可用。
	//
	// 角色集合固定为四个，由服务启动时按内置默认定义补齐；接口只提供查询与可编辑字段
	// 的更新，不提供新增与删除。
	Roles *app.RoleService
	// Projects 是项目管理服务，为空时项目接口返回服务不可用。
	//
	// 项目是任务必填的关联对象，因此它不是可选能力：服务未装配时接口返回 503，
	// 而不是伪造空列表。
	Projects *app.ProjectService
	// Health 提供健康检查结果，为空时健康检查返回服务不可用。
	Health *app.HealthService
	// SystemInfo 返回系统信息，为空时接口返回服务不可用。
	SystemInfo func() app.SystemInfo
	// RuntimeConfig 是服务启动时实际生效的基础运行配置快照，为空时该接口返回服务
	// 不可用：没有快照就不能用默认值补造一份配置。
	RuntimeConfig *app.RuntimeConfig
	// Tasks 是任务管理服务，为空时任务接口返回服务不可用。
	Tasks *app.TaskService
	// Hosts 是执行节点服务，为空时执行节点接口返回服务不可用。
	Hosts *app.ExecutionHostService
	// Environments 是环境只读查询服务，为空时环境查询接口返回服务不可用。
	//
	// 它只读取持久化的环境与版本元数据：不建立远程会话，也不会触发构建或初始化。
	Environments *app.EnvironmentQueryService
	// EnvironmentWrites 是环境同步写路径，为空时初始化、创建版本、读取远程
	// Dockerfile 与构建日志的接口返回服务不可用。
	//
	// 它由 app.EnvironmentService 实现：远程连接、版本目录与上下文校验都在应用层，
	// HTTP 层只做协议转换。写路径与只读查询分开注入，使"看环境"不需要远程能力。
	EnvironmentWrites EnvironmentWriter
	// EnvironmentBuilds 是后台环境构建入口，为空时构建接口返回服务不可用。
	//
	// 构建是长操作：受理后由应用层后台服务执行，接口只返回真实初始状态。
	EnvironmentBuilds EnvironmentBuilder
	// ExecutionRecords 是执行记录查询服务，为空时执行记录接口返回服务不可用。
	ExecutionRecords *app.ExecutionRecordService
	// StepExecutor 是单步骤执行协调器，为空时步骤执行接口返回服务不可用。
	StepExecutor *app.StepExecutor
	// Validation 是确定性验证引擎，为空时验证接口返回服务不可用。
	Validation *app.ValidationService
	// StepWorkflow 是单步骤开发—验证闭环协调器，为空时闭环接口返回服务不可用。
	StepWorkflow *app.StepWorkflowService
	// Workflow 是任务级工作流编排器，为空时工作流接口返回服务不可用。
	Workflow *app.TaskWorkflowService
	// CodeInitialize 是代码初始化协调器，为空时初始化接口返回服务不可用。
	CodeInitialize *app.CodeInitializeService
	// TaskLaunch 是统一任务启动服务，为空时启动接口返回服务不可用。
	TaskLaunch *app.TaskLaunchService
	// Containers 是容器服务，提供代码保存的只读核验能力。
	//
	// 为空时核验接口返回服务不可用，而不是伪造"核验已通过"。
	//
	// 注意：代码保存记录的列表查询**不**使用它。ContainerService 由
	// newRemoteDockerService 构造，强制要求 SSH 与存储同时存在，把只读列表查询
	// 绑在它上面会让"看记录"也需要远程能力；列表查询使用只依赖存储的
	// CodeSaveQueries。
	Containers *app.ContainerService
	// CodeSaveQueries 是代码保存记录只读查询服务，为空时列表接口返回服务不可用。
	//
	// 它只依赖存储：不建立 SSH / Docker 会话、不核验容器、不执行 Git 命令。
	CodeSaveQueries *app.CodeSaveQueryService
	// ResumeEligibility 计算任务的续跑资格，为空时该接口返回服务不可用。
	ResumeEligibility *app.ResumeEligibilityService
	// History 提供事件时间线与修复周期历史的只读查询。
	//
	// 为空时两个历史接口返回服务不可用，而不是伪造空列表：空历史与"查询服务
	// 未装配"是两件完全不同的事实。
	History *app.HistoryQueryService
	// Shutdown 在 HTTP 服务开始关闭时被关闭，供实时事件流结束推送循环。
	//
	// 为空表示没有关闭信号（例如测试直接构造处理器）：此时推送循环只依赖请求上下
	// 文取消。事件流是长连接，若不主动结束，服务关闭会一直等到连接级超时。
	Shutdown <-chan struct{}
	// TaskRetry 是任务显式重试服务，为空时重试接口返回服务不可用。
	//
	// 重试入口只在领域规则判定通过后才把 failed 任务重开为新的执行周期；它复用
	// 统一启动流程，不实现第二套启动逻辑。
	TaskRetry *app.TaskRetryService
	// TaskContainers 提供任务容器的只读查询与显式处置（delete / detach）。
	//
	// 为空时两个接口返回服务不可用。它与 Containers 分开注入：容器列表只依赖存储，
	// 处置则要求容器服务存在，两者不是同一种依赖。
	TaskContainers *app.TaskContainerDisposalService
	// PlanningSessions 是规划会话服务，为空时规划会话接口返回服务不可用。
	//
	// 它只依赖存储：读取会话记录不需要 SSH 或 Docker，因此与 PlanningRuntime 分开注入。
	PlanningSessions *app.PlanningSessionService
	// PlanningRuntime 是规划运行时服务，为空时现场核验与准备/释放接口返回服务不可用。
	//
	// 它需要容器服务（SSH + Docker）：现场核验与恢复都必须在真实远程环境上执行，
	// 没有远程能力时不得退化为"看起来核验通过"。
	PlanningRuntime *app.PlanningRuntimeService
	// PlanningAnalysis 是两阶段分析编排服务，为空时分析接口返回服务不可用。
	//
	// 它需要规划角色调用能力（容器内 Pi）：没有远程能力时不得产出"看起来成功"的分析。
	PlanningAnalysis *app.PlanningAnalysisService
	// Requirement 是方案确认与正式需求查询服务，为空时相关接口返回服务不可用。
	//
	// 它只依赖存储：确认时生成正式需求与任务不接触 SSH、Docker 或 Pi。
	Requirement *app.RequirementService
	// ProjectExecution 是项目执行协调服务，为空时项目执行与进度接口返回服务不可用。
	//
	// 它需要既有任务取消入口，但不实现任何任务执行流程：开始项目执行只取得项目执行权
	// 并触发一次调度扫描，启动仍由统一启动服务完成。
	ProjectExecution *app.ProjectExecutionService
	// Logger 是日志器，为空时使用 slog.Default。
	Logger *slog.Logger
}

// NewHandler 构造挂在根路径上的 API 处理器。
//
// 未注册的 /api 路径（含 /api 本身）一律返回统一 JSON 404 或 405，不会落到前端的
// SPA 回退上；/api 保留路径空间的判定由 IsAPIPath 负责。
func NewHandler(deps Deps) http.Handler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	router := &router{
		roles:    newRoleHandler(deps.Roles, logger),
		projects: newProjectHandler(deps.Projects, logger),
		environments: newEnvironmentHandler(
			deps.Environments,
			deps.EnvironmentWrites,
			deps.EnvironmentBuilds,
			logger,
		),
		health:        deps.Health,
		systemInfo:    deps.SystemInfo,
		runtimeConfig: deps.RuntimeConfig,
		tasks:         newTaskHandler(deps.Tasks, logger),
		hosts:         newHostHandler(deps.Hosts, logger),
		executions:    newExecutionHandler(deps.ExecutionRecords, logger),
		stepExec: newStepExecutionHandler(
			deps.StepExecutor,
			deps.StepWorkflow,
			logger,
		),
		workflow:     newWorkflowHandler(deps.Workflow, logger),
		validation:   newValidationHandler(deps.Validation, logger),
		codeInit:     newCodeInitializeHandler(deps.CodeInitialize, logger),
		taskLaunch:   newTaskLaunchHandler(deps.TaskLaunch, logger),
		codeRecovery: newCodeRecoveryHandler(deps.Containers, deps.ResumeEligibility, logger),
		codeSaves:    newCodeSaveListHandler(deps.CodeSaveQueries, logger),
		taskRetry:    newTaskRetryHandler(deps.TaskRetry, logger),
		taskContainers: &containerHandler{
			service: deps.TaskContainers,
			logger:  logger,
		},
		history:     newHistoryHandler(deps.History, logger),
		eventStream: newEventStreamHandler(deps.History, logger, deps.Shutdown),
		planning: newPlanningHandler(
			deps.PlanningSessions,
			deps.PlanningRuntime,
			deps.PlanningAnalysis,
			logger,
		),
		requirement: newRequirementHandler(deps.Requirement, logger),
		projectExecution: newProjectExecutionHandler(
			deps.ProjectExecution,
			deps.Requirement,
			logger,
		),
		logger: logger,
	}
	return withRequestID(router.newMux())
}

// router 持有 API 处理器。
//
// 每个字段都是一个已经装配好的处理器：服务依赖缺失的判定发生在路由前置检查里，
// 处理器本身不再承担"服务是否可用"的分发职责。
type router struct {
	// roles 处理角色配置的查询与更新；服务未初始化时接口返回 503。
	roles *roleHandler
	// projects 处理项目管理接口；服务未初始化时接口返回 503。
	projects   *projectHandler
	health     *app.HealthService
	systemInfo func() app.SystemInfo
	// runtimeConfig 是启动阶段生成的运行配置快照，为空时该接口返回服务不可用。
	runtimeConfig *app.RuntimeConfig
	tasks         *taskHandler
	hosts         *hostHandler
	// environments 处理环境查询与写路径；服务未初始化时接口返回 503。
	environments *environmentHandler
	executions   *executionHandler
	stepExec     *stepExecutionHandler
	// workflow 处理任务级工作流与计划生成，缺失依赖时接口返回 503。
	workflow   *workflowHandler
	validation *validationHandler
	codeInit   *codeInitializeHandler
	// taskLaunch 处理统一任务启动，缺失依赖时接口返回 503。
	taskLaunch *taskLaunchHandler
	// codeRecovery 处理代码保存核验与续跑资格，缺失依赖时接口返回 503。
	codeRecovery *codeRecoveryHandler
	// codeSaves 处理代码保存记录只读列表，为空时列表接口返回服务不可用。
	//
	// 它与 codeRecovery 分开持有：列表查询只依赖存储，不要求 SSH / Docker。
	codeSaves *codeSaveListHandler
	// taskRetry 处理任务显式重试与重试资格，为空时接口返回服务不可用。
	taskRetry *taskRetryHandler
	// taskContainers 处理任务容器的查询与显式处置，为空时接口返回服务不可用。
	taskContainers *containerHandler
	// history 处理事件时间线与修复周期历史的只读查询。
	history *historyHandler
	// eventStream 提供任务事件的只读实时流，为空时该接口返回服务不可用。
	eventStream *eventStreamHandler
	// planning 处理规划会话诊断与规划运行时核验/准备/释放，缺失依赖时接口返回 503。
	//
	// 它只做协议转换：会话读取、现场核验与运行时动作全部由生产 app 服务执行。
	planning *planningHandler
	// requirement 处理方案确认与正式需求查询，缺失依赖时接口返回 503。
	//
	// 确认接口挂在规划的会话路径下：它操作的对象是某个规划会话采纳的方案版本。
	requirement *requirementHandler
	// projectExecution 处理项目执行动作、项目状态与需求进度，缺失依赖时接口返回 503。
	//
	// 它只做协议转换与错误映射：执行权、项目内顺序与进度聚合全部由 domain / app 决定。
	projectExecution *projectExecutionHandler
	logger           *slog.Logger
}

// healthHandler 处理健康检查请求。
//
// 只有整体状态为 ok 时返回 200；存在不可用依赖时返回 503，前端据此显示未连接。
func (rt *router) healthHandler(w http.ResponseWriter, r *http.Request) {
	if rt.health == nil {
		writeError(w, r, rt.logger, http.StatusServiceUnavailable, CodeInternal, "健康检查服务未初始化")
		return
	}
	health := rt.health.Check(r.Context())
	status := http.StatusOK
	if health.Status != app.HealthStatusOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, r, rt.logger, status, health)
}

// systemInfoHandler 处理系统信息请求。
func (rt *router) systemInfoHandler(w http.ResponseWriter, r *http.Request) {
	if rt.systemInfo == nil {
		writeError(w, r, rt.logger, http.StatusServiceUnavailable, CodeInternal, "系统信息服务未初始化")
		return
	}
	writeJSON(w, r, rt.logger, http.StatusOK, rt.systemInfo())
}

// notFoundHandler 返回统一格式的 404。
//
// 它同时兜住 /api、/api/ 与其余未注册路径：未知 API 请求只得到 JSON 错误，
// 不会落到前端的 SPA 回退上。
func (rt *router) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, rt.logger, http.StatusNotFound, CodeNotFound, "接口不存在: "+r.URL.Path)
}
