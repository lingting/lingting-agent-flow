package httpapi

import (
	"net/http"
)

// API 路径参数名。
//
// 路由模式中的 {name} 与 Request.PathValue 共用同一组常量，避免两处拼写漂移：
// 参数名写错会让取值静默变空，而不是在注册阶段报错。
const (
	paramTaskID        = "taskID"
	paramStepID        = "stepID"
	paramAttemptID     = "attemptID"
	paramProjectID     = "projectID"
	paramHostID        = "hostID"
	paramRoleID        = "roleID"
	paramContainerID   = "containerID"
	paramRecordID      = "recordID"
	paramSessionID     = "sessionID"
	paramRequirementID = "requirementID"
	paramRunID         = "runID"
	paramEnvType       = "environmentType"
	paramEnvName       = "environmentName"
	paramEnvVersion    = "versionNumber"
)

// 路由模式中的动态路径段。
const (
	matchTaskID        = "{" + paramTaskID + "}"
	matchStepID        = "{" + paramStepID + "}"
	matchAttemptID     = "{" + paramAttemptID + "}"
	matchProjectID     = "{" + paramProjectID + "}"
	matchHostID        = "{" + paramHostID + "}"
	matchRoleID        = "{" + paramRoleID + "}"
	matchContainerID   = "{" + paramContainerID + "}"
	matchRecordID      = "{" + paramRecordID + "}"
	matchSessionID     = "{" + paramSessionID + "}"
	matchRequirementID = "{" + paramRequirementID + "}"
	matchRunID         = "{" + paramRunID + "}"
	matchEnvType       = "{" + paramEnvType + "}"
	matchEnvName       = "{" + paramEnvName + "}"
	matchEnvVersion    = "{" + paramEnvVersion + "}"
)

// 路由模式前缀。
//
// 组合出的模式与接口路径一一对应：动态段一律使用通配符表达，不再依赖字符串前缀与
// 段数判断来识别路径形状。
const (
	matchTasks           = PathTasks
	matchTask            = matchTasks + "/" + matchTaskID
	matchTaskSteps       = matchTask + "/" + stepsSegment
	matchStep            = matchTaskSteps + "/" + matchStepID
	matchStepAttempts    = matchStep + "/" + attemptsSegment
	matchAttempt         = matchStepAttempts + "/" + matchAttemptID
	matchStepValidations = matchStep + "/" + validationsSegment
	matchTaskContainers  = matchTask + "/" + containersSegment

	matchRoles    = PathRoles
	matchRole     = matchRoles + "/" + matchRoleID
	matchProjects = PathProjects
	matchProject  = matchProjects + "/" + matchProjectID
	matchHosts    = PathHosts
	matchHost     = matchHosts + "/" + matchHostID

	matchEnvironments        = PathEnvironments
	matchEnvironment         = matchEnvironments + "/" + matchEnvType + "/" + matchEnvName
	matchEnvironmentVersions = matchEnvironment + "/" + environmentVersionsSegment
	matchEnvironmentVersion  = matchEnvironmentVersions + "/" + matchEnvVersion

	matchPlanningSessions  = PathPlanningSessions
	matchPlanningSession   = matchPlanningSessions + "/" + matchSessionID
	matchPlanningMessages  = matchPlanningSession + "/" + planningMessagesSegment
	matchPlanningRevisions = matchPlanningSession + "/" + planningRevisionsSegment
	matchPlanningAnalysis  = matchPlanningSession + "/" + planningAnalysisSegment
	matchPlanningRetry     = matchPlanningAnalysis + "/" + retrySegment
	matchPlanningAbandon   = matchPlanningSession + "/" + planningAbandonSegment
	matchPlanningRuntime   = matchPlanningSession + "/" + planningRuntimeSegment
	matchPlanningPrepare   = matchPlanningRuntime + "/" + planningPrepareSegment
	matchPlanningRelease   = matchPlanningRuntime + "/" + planningReleaseSegment
	matchPlanningConfirm   = matchPlanningSession + "/" + planningConfirmSegment

	matchRequirements        = PathRequirements
	matchRequirement         = matchRequirements + "/" + matchRequirementID
	matchProjectRequirements = matchProject + "/" + requirementsSegment
)

// apiRouteMethod 是路由上允许的一个 HTTP 方法及其处理函数。
type apiRouteMethod struct {
	method  string
	handler http.HandlerFunc
}

// apiRoute 是一条 API 路由的注册信息。
//
// 同一路径允许的方法各自注册，另注册一个不限方法的兜底处理器给出统一的 JSON 405：
// 标准库 ServeMux 自带的 405 是纯文本，与项目统一错误协议不兼容。静态资源、请求标识
// 中间件与 SSE 都不在这里，它们分别由外层分流、withRequestID 与协议处理器承担。
type apiRoute struct {
	// pattern 是注册到 ServeMux 的路径模式，动态路径段使用 {name} 通配符。
	pattern string
	// methods 是允许的方法与处理函数。
	//
	// 标准库的 "GET" 方法模式同时匹配 HEAD，因此这里只写 GET，不重复注册 HEAD。
	methods []apiRouteMethod
	// allow 是 405 响应 Allow 头的原文。
	allow string
	// methodNotAllowed 是 405 响应的错误消息原文。
	//
	// 逐条保留原措辞：各接口的文案并不完全一致（例如事件流与其余 GET 接口不同），
	// 不能用统一格式化函数推导。
	methodNotAllowed string
	// guard 是在判定 HTTP 方法之前必须完成的检查。
	//
	// 它承担两类原有行为：服务依赖缺失时先返回 503，以及路径取值非法时先返回
	// 400 / 404。返回 false 表示已经写出响应，后续检查与方法判定都不再执行。
	guard func(w http.ResponseWriter, r *http.Request) bool
}

// routes 返回全部 API 路由。
//
// 注册顺序不代表匹配顺序：路径模式由 ServeMux 按具体程度匹配，因此不再需要
// "更具体形状先判定"的手工顺序分发。
func (rt *router) routes() []apiRoute {
	// 服务依赖缺失时的前置检查：这些接口在服务未装配时任何方法都返回 503，
	// 与"未知方法先返回 405"的接口不同，因此必须保留各自原有顺序。
	taskService := rt.availabilityGuard(rt.tasks.tasks != nil, "任务服务未初始化")
	projectService := rt.availabilityGuard(rt.projects.projects != nil, "项目服务未初始化")
	roleService := rt.availabilityGuard(rt.roles.roles != nil, "角色服务未初始化")
	hostService := rt.availabilityGuard(rt.hosts.hosts != nil, "执行节点服务未初始化")
	recordService := rt.availabilityGuard(rt.executions.records != nil, "执行记录服务未初始化")
	historyService := rt.availabilityGuard(rt.history.history != nil, "历史查询服务未初始化")
	streamService := rt.availabilityGuard(rt.eventStream.history != nil, "历史查询服务未初始化")
	codeSaveService := rt.availabilityGuard(
		rt.codeSaves.queries != nil,
		"代码保存记录查询服务未初始化",
	)
	codeRecoveryService := rt.availabilityGuard(
		rt.codeRecovery.containers != nil || rt.codeRecovery.eligibility != nil,
		"代码保存恢复服务未初始化",
	)
	launchService := rt.availabilityGuard(
		rt.taskLaunch.launch != nil,
		"任务启动服务未初始化",
	)
	retryService := rt.availabilityGuard(
		rt.taskRetry.service != nil,
		"任务重试服务未初始化",
	)
	workflowService := rt.availabilityGuard(
		rt.workflow.workflow != nil,
		"工作流编排服务未初始化",
	)
	planningSessionService := rt.availabilityGuard(
		rt.planning.sessions != nil,
		"规划会话服务未初始化",
	)
	planningRuntimeService := rt.availabilityGuard(
		rt.planning.runtime != nil,
		"规划运行时服务未初始化",
	)
	planningAnalysisService := rt.availabilityGuard(
		rt.planning.analysis != nil,
		"规划分析服务未初始化",
	)
	requirementService := rt.availabilityGuard(
		rt.requirement.service != nil,
		"需求服务未初始化",
	)

	// 环境接口在判定方法之前只校验段值：非法环境类型、名称或版本号按参数错误返回
	// 400，不伪装成"资源不存在"，也不因为方法不匹配而改变结论。
	environmentRef, environmentVersion := rt.environments.guards()

	return []apiRoute{
		// 健康检查、系统信息与基础运行配置：只读 GET，方法先于服务依赖判定。
		{
			pattern:          PathHealth,
			methods:          get(rt.healthHandler),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          PathSystemInfo,
			methods:          get(rt.systemInfoHandler),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          PathSettingsConfig,
			methods:          get(rt.settingsConfigHandler),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},

		// 角色：服务未装配时任何方法都返回 503；集合固定为四个，不提供新增与删除。
		{
			pattern:          matchRoles,
			methods:          get(rt.roles.list),
			guard:            roleService,
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern: matchRole,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.roles.detail},
				{method: http.MethodPut, handler: rt.roles.update},
			},
			guard:            guardFor(roleService, rt.pathIDGuard(paramRoleID)),
			allow:            "GET, HEAD, PUT",
			methodNotAllowed: "该接口只支持 GET 与 PUT 请求",
		},

		// 项目：集合支持列表与创建，单项目支持详情、整体更新与逻辑删除。
		{
			pattern: matchProjects,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.projects.list},
				{method: http.MethodPost, handler: rt.projects.create},
			},
			guard:            projectService,
			allow:            "GET, HEAD, POST",
			methodNotAllowed: "该接口只支持 GET 与 POST 请求",
		},
		{
			pattern: matchProject,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.projects.detail},
				{method: http.MethodPut, handler: rt.projects.update},
				{method: http.MethodDelete, handler: rt.projects.remove},
			},
			guard:            guardFor(projectService, rt.pathIDGuard(paramProjectID)),
			allow:            "GET, HEAD, PUT, DELETE",
			methodNotAllowed: "该接口只支持 GET、PUT 与 DELETE 请求",
		},

		// 执行节点：集合支持列表与创建，单节点支持详情、更新、逻辑删除与重新校验。
		{
			pattern: matchHosts,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.hosts.list},
				{method: http.MethodPost, handler: rt.hosts.create},
			},
			guard:            hostService,
			allow:            "GET, HEAD, POST",
			methodNotAllowed: "该接口只支持 GET 与 POST 请求",
		},
		{
			pattern: matchHost,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.hosts.detail},
				{method: http.MethodPatch, handler: rt.hosts.update},
				{method: http.MethodPut, handler: rt.hosts.update},
				{method: http.MethodDelete, handler: rt.hosts.remove},
			},
			guard:            guardFor(hostService, rt.pathIDGuard(paramHostID)),
			allow:            "GET, HEAD, PATCH, PUT, DELETE",
			methodNotAllowed: "该接口只支持 GET、PATCH、PUT 与 DELETE 请求",
		},
		{
			pattern:          matchHost + "/" + hostValidateSegment,
			methods:          post(rt.hosts.validate),
			guard:            guardFor(hostService, rt.pathIDGuard(paramHostID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},

		// 环境：集合路径支持列表，init 初始化工作空间，其余按环境引用与版本号表达。
		{
			pattern:          matchEnvironments,
			methods:          get(rt.environments.serveList),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET, HEAD 请求",
		},
		{
			pattern:          matchEnvironments + "/" + environmentInitSegment,
			methods:          post(rt.environments.serveInit),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchEnvironment,
			methods:          get(rt.environments.definitionRoute),
			guard:            environmentRef,
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET, HEAD 请求",
		},
		{
			pattern:          matchEnvironmentVersions,
			methods:          post(rt.environments.createVersionRoute),
			guard:            environmentRef,
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchEnvironment + "/" + environmentBuildSegment,
			methods:          post(rt.environments.buildRoute),
			guard:            environmentRef,
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchEnvironmentVersion,
			methods:          get(rt.environments.versionRoute),
			guard:            environmentVersion,
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET, HEAD 请求",
		},
		{
			pattern:          matchEnvironmentVersion + "/" + environmentDockerfileSegment,
			methods:          get(rt.environments.dockerfileRoute),
			guard:            environmentVersion,
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET, HEAD 请求",
		},
		{
			pattern:          matchEnvironmentVersion + "/" + environmentBuildLogSegment,
			methods:          get(rt.environments.buildLogRoute),
			guard:            environmentVersion,
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET, HEAD 请求",
		},
		{
			pattern:          matchEnvironmentVersion + "/" + environmentBuildStatusSegment,
			methods:          get(rt.environments.buildStatusRoute),
			guard:            environmentVersion,
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET, HEAD 请求",
		},

		// 任务：集合支持列表与创建，单任务支持详情、逻辑删除与取消。
		{
			pattern: matchTasks,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.tasks.list},
				{method: http.MethodPost, handler: rt.tasks.create},
			},
			guard:            taskService,
			allow:            "GET, HEAD, POST",
			methodNotAllowed: "该接口只支持 GET 与 POST 请求",
		},
		{
			pattern: matchTask,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.tasks.detail},
				{method: http.MethodDelete, handler: rt.tasks.remove},
			},
			guard:            guardFor(taskService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD, DELETE",
			methodNotAllowed: "该接口只支持 GET 与 DELETE 请求",
		},
		{
			pattern:          matchTask + "/" + cancelSegment,
			methods:          post(rt.tasks.cancel),
			guard:            guardFor(taskService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},

		// 统一任务启动、显式重试与续跑资格：各有独立依赖，判定先于任务管理服务。
		{
			pattern:          matchTask + "/" + startSegment,
			methods:          post(rt.taskLaunch.start),
			guard:            guardFor(launchService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + retrySegment,
			methods:          post(rt.taskRetry.retry),
			guard:            guardFor(retryService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + retryEligibilitySegment,
			methods:          get(rt.taskRetry.eligibility),
			guard:            guardFor(retryService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},

		// 代码保存：列表只依赖存储，核验与续跑资格由恢复服务承担。
		{
			pattern:          matchTask + "/" + codeSavesSegment,
			methods:          get(rt.codeSaves.list),
			guard:            guardFor(codeSaveService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern: matchTask + "/" + codeSavesSegment + "/" + matchRecordID + "/" + codeSaveVerifySegment,
			methods: post(rt.codeRecovery.verify),
			guard: guardFor(
				codeRecoveryService,
				rt.pathIDGuard(paramTaskID, paramRecordID),
			),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + resumeEligibilitySegment,
			methods:          get(rt.codeRecovery.serveEligibility),
			guard:            guardFor(codeRecoveryService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},

		// 任务容器：列表只依赖存储，处置要求容器服务存在。
		{
			pattern:          matchTaskContainers,
			methods:          get(rt.taskContainers.list),
			guard:            rt.pathIDGuard(paramTaskID),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchTaskContainers + "/" + matchContainerID + "/" + containerDeleteSegment,
			methods:          post(rt.taskContainers.disposalRoute(containerDeleteSegment)),
			guard:            rt.pathIDGuard(paramTaskID, paramContainerID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTaskContainers + "/" + matchContainerID + "/" + containerDetachSegment,
			methods:          post(rt.taskContainers.disposalRoute(containerDetachSegment)),
			guard:            rt.pathIDGuard(paramTaskID, paramContainerID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},

		// 事件时间线与实时流：都是只读历史读取，实时流只支持 GET。
		{
			pattern:          matchTask + "/" + eventsSegment,
			methods:          get(rt.history.listEvents),
			guard:            guardFor(historyService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchTask + "/" + eventsSegment + "/" + eventStreamSegment,
			methods:          get(rt.eventStream.streamTaskEvents),
			guard:            guardFor(streamService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET",
			methodNotAllowed: "事件流只支持 GET 请求",
		},
		{
			pattern:          matchTask + "/" + sessionsSegment + "/" + matchSessionID,
			methods:          get(rt.executions.getSession),
			guard:            guardFor(recordService, rt.pathIDGuard(paramTaskID, paramSessionID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},

		// 执行记录：只读查询，不注册任何写入口。
		{
			pattern:          matchTaskSteps,
			methods:          get(rt.executions.listSteps),
			guard:            guardFor(recordService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchStepAttempts,
			methods:          get(rt.executions.listAttempts),
			guard:            guardFor(recordService, rt.pathIDGuard(paramTaskID, paramStepID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern: matchAttempt + "/" + sessionsSegment,
			methods: get(rt.executions.listSessions),
			guard: guardFor(
				recordService,
				rt.pathIDGuard(paramTaskID, paramStepID, paramAttemptID),
			),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchStepValidations,
			methods:          get(rt.executions.listValidations),
			guard:            guardFor(recordService, rt.pathIDGuard(paramTaskID, paramStepID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern: matchStepValidations + "/" + matchRunID + "/" + feedbackSegment,
			methods: get(rt.executions.getValidationFeedback),
			guard: guardFor(
				recordService,
				rt.pathIDGuard(paramTaskID, paramStepID, paramRunID),
			),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},

		// 步骤动作：方法先于服务依赖判定，各自依赖不同服务。
		{
			pattern:          matchStep + "/" + executeSegment,
			methods:          post(rt.stepExec.serve),
			guard:            rt.pathIDGuard(paramTaskID, paramStepID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + executeStepsSegment,
			methods:          post(rt.stepExec.serveSteps),
			guard:            rt.pathIDGuard(paramTaskID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchStep + "/" + executeAndValidateSegment,
			methods:          post(rt.stepExec.serveExecuteAndValidate),
			guard:            rt.pathIDGuard(paramTaskID, paramStepID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchStep + "/" + openRepairCycleSegment,
			methods:          post(rt.stepExec.serveOpenRepairCycle),
			guard:            rt.pathIDGuard(paramTaskID, paramStepID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchStep + "/" + repairCycleEligibilitySegment,
			methods:          get(rt.stepExec.serveRepairCycleEligibility),
			guard:            rt.pathIDGuard(paramTaskID, paramStepID),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchStep + "/" + repairCyclesSegment,
			methods:          get(rt.history.listRepairCycles),
			guard:            guardFor(historyService, rt.pathIDGuard(paramTaskID, paramStepID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchAttempt + "/" + validateSegment,
			methods:          post(rt.validation.serve),
			guard:            rt.pathIDGuard(paramTaskID, paramStepID, paramAttemptID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchAttempt + "/" + initializeCodeSegment,
			methods:          post(rt.codeInit.serve),
			guard:            rt.pathIDGuard(paramTaskID, paramStepID, paramAttemptID),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},

		// 任务级工作流与计划生成：编排器装配后才能执行。
		{
			pattern:          matchTask + "/" + planSegment,
			methods:          post(rt.workflow.plan),
			guard:            guardFor(workflowService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + executeWorkflowSegment,
			methods:          post(rt.workflow.start),
			guard:            guardFor(workflowService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + cancelWorkflowSegment,
			methods:          post(rt.workflow.cancel),
			guard:            guardFor(workflowService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + resumeWorkflowSegment,
			methods:          post(rt.workflow.resume),
			guard:            guardFor(workflowService, rt.pathIDGuard(paramTaskID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchTask + "/" + workflowSegment,
			methods:          get(rt.workflow.status),
			guard:            guardFor(workflowService, rt.pathIDGuard(paramTaskID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},

		// 规划会话业务：集合支持按项目列表与创建，单会话提供详情、消息、版本与放弃。
		{
			pattern: matchPlanningSessions,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.planning.list},
				{method: http.MethodPost, handler: rt.planning.create},
			},
			guard:            planningSessionService,
			allow:            "GET, HEAD, POST",
			methodNotAllowed: "该接口只支持 GET 与 POST 请求",
		},
		{
			pattern:          matchPlanningSession,
			methods:          get(rt.planning.detail),
			guard:            guardFor(planningSessionService, rt.pathIDGuard(paramSessionID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern: matchPlanningMessages,
			methods: []apiRouteMethod{
				{method: http.MethodGet, handler: rt.planning.listMessages},
				{method: http.MethodPost, handler: rt.planning.submitMessage},
			},
			guard:            guardFor(planningSessionService, rt.pathIDGuard(paramSessionID)),
			allow:            "GET, HEAD, POST",
			methodNotAllowed: "该接口只支持 GET 与 POST 请求",
		},
		{
			pattern:          matchPlanningRevisions,
			methods:          get(rt.planning.listRevisions),
			guard:            guardFor(planningSessionService, rt.pathIDGuard(paramSessionID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchPlanningAbandon,
			methods:          post(rt.planning.abandon),
			guard:            guardFor(planningSessionService, rt.pathIDGuard(paramSessionID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		// 两阶段分析：显式触发，占用由数据库条件更新保证并发唯一。
		{
			pattern:          matchPlanningAnalysis,
			methods:          post(rt.planning.analyze),
			guard:            guardFor(planningAnalysisService, rt.pathIDGuard(paramSessionID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern: matchPlanningRetry,
			methods: post(rt.planning.retryAnalysis),
			guard: guardFor(
				planningAnalysisService,
				planningSessionService,
				rt.pathIDGuard(paramSessionID),
			),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		// 规划运行时：诊断接口只读，准备与释放显式调用生产服务。
		{
			pattern:          matchPlanningRuntime,
			methods:          get(rt.planning.inspectRuntime),
			guard:            guardFor(planningRuntimeService, rt.pathIDGuard(paramSessionID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchPlanningPrepare,
			methods:          post(rt.planning.prepareRuntime),
			guard:            guardFor(planningRuntimeService, rt.pathIDGuard(paramSessionID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		{
			pattern:          matchPlanningRelease,
			methods:          post(rt.planning.releaseRuntime),
			guard:            guardFor(planningRuntimeService, rt.pathIDGuard(paramSessionID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		// 方案确认：把指定 Revision 转换成正式 Requirement + Task，任务保持待执行。
		{
			pattern:          matchPlanningConfirm,
			methods:          post(rt.requirement.confirm),
			guard:            guardFor(requirementService, rt.pathIDGuard(paramSessionID)),
			allow:            "POST",
			methodNotAllowed: "该接口只支持 POST 请求",
		},
		// 正式需求只读查询：需求详情与项目需求列表。
		{
			pattern:          matchRequirement,
			methods:          get(rt.requirement.detail),
			guard:            guardFor(requirementService, rt.pathIDGuard(paramRequirementID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
		{
			pattern:          matchProjectRequirements,
			methods:          get(rt.requirement.listByProject),
			guard:            guardFor(requirementService, rt.pathIDGuard(paramProjectID)),
			allow:            "GET, HEAD",
			methodNotAllowed: "该接口只支持 GET 请求",
		},
	}
}

// get 构造只读路由的方法表：GET 模式由标准库同时匹配 HEAD。
func get(handler http.HandlerFunc) []apiRouteMethod {
	return []apiRouteMethod{{method: http.MethodGet, handler: handler}}
}

// post 构造只写路由的方法表。
func post(handler http.HandlerFunc) []apiRouteMethod {
	return []apiRouteMethod{{method: http.MethodPost, handler: handler}}
}

// availabilityGuard 返回服务依赖缺失时的前置检查。
//
// available 为 true 时不产生任何检查，路由随后只按方法判定。
func (rt *router) availabilityGuard(
	available bool,
	message string,
) func(w http.ResponseWriter, r *http.Request) bool {
	if available {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) bool {
		writeError(w, r, rt.logger, http.StatusServiceUnavailable, CodeInternal, message)
		return false
	}
}

// pathIDGuard 返回路径标识的前置检查。
//
// 标识为空、只有空白或解码后含路径分隔符时按接口不存在处理：这类取值不能作为一个
// 资源标识交给业务服务，其他做法都会把非法路径伪装成业务层的"资源不存在"。
func (rt *router) pathIDGuard(names ...string) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		for _, name := range names {
			if !validPathID(r.PathValue(name)) {
				writeError(
					w,
					r,
					rt.logger,
					http.StatusNotFound,
					CodeNotFound,
					"接口不存在: "+r.URL.Path,
				)
				return false
			}
		}
		return true
	}
}

// guardFor 按给定顺序组合前置检查，nil 检查被忽略。
//
// 组合为空时返回 nil：没有前置检查的路由只按方法判定。
func guardFor(
	checks ...func(w http.ResponseWriter, r *http.Request) bool,
) func(w http.ResponseWriter, r *http.Request) bool {
	pending := make([]func(w http.ResponseWriter, r *http.Request) bool, 0, len(checks))
	for _, check := range checks {
		if check != nil {
			pending = append(pending, check)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) bool {
		for _, check := range pending {
			if !check(w, r) {
				return false
			}
		}
		return true
	}
}

// newMux 建立 API 路由表并挂载全部处理器。
//
// 未知的 /api 路径统一返回 JSON 404，不会落到前端的 SPA 回退上：/api 与 /api/
// 是 API 保留路径，"/" 兜住其余直接进入本处理器的请求。
func (rt *router) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, route := range rt.routes() {
		rt.register(mux, route)
	}
	for _, pattern := range []string{APIRoot, APIRoot + "/", "/"} {
		mux.HandleFunc(pattern, rt.notFoundHandler)
	}
	return mux
}

// register 注册一条路由的方法处理器与 405 兜底处理器。
//
// 注册冲突（模式互相包含却都不更具体）由 ServeMux 直接 panic：它是实现错误，
// 不会被 recover 掩盖。
func (rt *router) register(mux *http.ServeMux, route apiRoute) {
	for _, entry := range route.methods {
		mux.HandleFunc(entry.method+" "+route.pattern, rt.guarded(route.guard, entry.handler))
	}
	mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
		if route.guard != nil && !route.guard(w, r) {
			return
		}
		w.Header().Set("Allow", route.allow)
		writeError(
			w,
			r,
			rt.logger,
			http.StatusMethodNotAllowed,
			CodeMethodNotAllowed,
			route.methodNotAllowed,
		)
	})
}

// guarded 把前置检查与处理函数组合成一个处理函数。
//
// 检查返回 false 表示已经写出响应（503 / 400 / 404），处理函数不再执行。
func (rt *router) guarded(
	guard func(w http.ResponseWriter, r *http.Request) bool,
	handler http.HandlerFunc,
) http.HandlerFunc {
	if guard == nil {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r) {
			return
		}
		handler(w, r)
	}
}
