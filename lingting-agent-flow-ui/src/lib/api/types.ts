/**
 * AgentFlow HTTP API 的公共类型。
 *
 * 类型与 Go 端 `internal/httpapi` 及 `internal/app` 的响应结构一一对应；
 * 只有本阶段真实存在的接口才在这里声明，不提前定义尚未实现的业务实体。
 */

/** 单项健康检查结果状态 */
export type HealthCheckStatus = "pass" | "fail";

/** 单项健康检查结果 */
export type HealthCheck = {
  /** 检查项名称，例如 `http_api`、`database` */
  name: string;
  /** 该项是否通过 */
  status: HealthCheckStatus;
  /** 失败原因，通过时缺省 */
  message?: string;
};

/** 健康检查总体状态：`ok` 表示全部通过，`degraded` 表示存在不可用依赖 */
export type HealthStatus = "ok" | "degraded";

/** `GET /api/v1/health` 的响应 */
export type HealthResponse = {
  status: HealthStatus;
  checks: HealthCheck[];
  /** 检查时间，ISO 8601 */
  checked_at: string;
};

/** `GET /api/v1/system/info` 的响应 */
export type SystemInfoResponse = {
  version: string;
  commit: string;
  build_date: string;
  go_version: string;
  /** 编译目标平台，形如 `windows/amd64` */
  platform: string;
};

/**
 * 任务状态。
 *
 * 取值与 Go 端 `domain.TaskStatus` 完全一致，不在前端维护第二套状态名称。
 * 语义：`queued` 等待执行、`completed` 成功、`failed` 失败、`cancelled` 已取消；
 * `preparing`…`saving` 属于执行过程中的中间状态；`cancelling` 表示已请求取消
 * 但尚未确认停止。
 */
export type TaskStatus =
  | "queued"
  | "preparing"
  | "planning"
  | "planned"
  | "developing"
  | "verifying"
  | "saving"
  | "cancelling"
  | "retrying"
  | "paused"
  | "recovery_required"
  | "completed"
  | "failed"
  | "cancelled";

/**
 * 任务的最终业务结论，与 Go 端 `domain.TaskResult` 一致。
 *
 * 它与 `TaskStatus` 是两件不同的事实：状态表达"处在哪个业务阶段"，结论表达
 * "这次执行的结果是什么"。空字符串表示服务端尚未提供最终结论，不得由状态推断。
 */
export type TaskResult = "succeeded" | "failed" | "cancelled";

/**
 * 任务的收尾闭环派生结论，与 Go 端 `domain.TaskCloseoutState` 一致。
 *
 * 它是只读视图而不是第二套状态：取值由 `status`、`final_result` 与最近一次工作流
 * 执行记录派生。`completed` + `inconsistent` 这类现场必须按"待人工核验"理解。
 */
export type TaskCloseoutState =
  | "confirmed"
  | "pending"
  | "unconfirmed"
  | "inconsistent"
  | "cancelled";

/** 任务对外表示，与 Go 端 `httpapi.TaskResponse` 一一对应 */
export type Task = {
  id: string;
  name: string;
  description: string;
  status: TaskStatus;
  /** 任务是否处于终态（`completed`、`cancelled`），用于区分"已结束"与"可重试" */
  status_terminal: boolean;
  /** 任务是否已停止执行，用于决定取消与删除是否可用；`failed` 也属于已停止 */
  status_finished: boolean;
  /**
   * 最终业务结论；服务端未提供时该字段缺省（Go 端为 `omitempty`）。
   *
   * 缺省表示"未提供最终结论"，不能据此推断任务成功或失败。
   * 未知取值按原始字符串处理，界面原样展示。
   */
  final_result?: TaskResult | (string & {});
  /**
   * 收尾闭环结论；Go 端没有 `omitempty`，因此正常情况下总是存在。
   *
   * 类型保留 `string` 分支：服务端新增取值时界面仍能原样展示，不因类型假设隐藏真实数据。
   */
  closeout?: TaskCloseoutState | (string & {});
  /** 收尾闭环结论的可读说明；缺省时界面使用中性提示，不自行编造 */
  closeout_summary?: string;
  /** 关联项目标识，未关联时为空字符串 */
  project_id: string;
  cancel_requested: boolean;
  cancel_requested_at?: string;
  /** 失败原因摘要，成功或未结束时缺省 */
  error?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
};

/** `GET /api/v1/tasks` 的分页响应 */
export type TaskListResponse = {
  records: Task[];
  total: number;
  current: number;
  size: number;
};

/** 任务列表查询参数 */
export type TaskListQuery = {
  current?: number;
  size?: number;
  status?: TaskStatus[];
};

/**
 * 创建任务的请求体。
 *
 * 名称与描述必填；其余字段沿用执行型配置。状态、开始时间与完成时间不可由客户端指定。
 */
export type CreateTaskRequest = {
  name: string;
  description: string;
  project_id?: string;
  priority?: "low" | "normal" | "high";
  host_id: string;
  repository_url: string;
  base_branch: string;
  environment: string;
  environment_version?: string;
  caches?: string[];
  work_dir?: string;
  commands?: string[];
  acceptance_criteria?: string[];
  max_attempts?: number;
  retry_timeout?: boolean;
  retry_verification?: boolean;
  retry_network?: boolean;
  retry_wait_seconds?: number;
  merge_after_push?: boolean;
  planner_role_id: string;
  developer_role_id: string;
  validator_role_id?: string;
  max_plan_steps?: number;
  agent_timeout_seconds?: number;
  verification_timeout_seconds?: number;
  idempotency_key?: string;
};

/**
 * 执行节点环境预检的总体状态。
 *
 * 取值与 Go 端 `domain.ValidationStatus` 完全一致：`checking` 表示尚未完成当前配置
 * 版本的预检，`normal` 表示全部必需预检项通过，`abnormal` 表示至少一项必需检查失败。
 * 前端不新增状态名，也不把 `checking` 显示为成功。
 */
export type ValidationStatus = "checking" | "normal" | "abnormal";

/**
 * 单项检查的结果状态。
 *
 * 只用于校验详情，不参与总体状态判定：`skipped` 与 `not_run` 都不算通过。
 */
export type ValidationCheckStatus = "passed" | "failed" | "timed_out" | "skipped" | "not_run";

/** 单个外部 FROM 镜像的元数据查询结果 */
export type ImageMetadataResult = {
  reference: string;
  resolved?: string;
  status: ValidationCheckStatus;
  error_code?: string;
  message?: string;
  digest?: string;
  platforms?: string[];
  duration_millis: number;
};

/** 一项校验的结果 */
export type ValidationCheck = {
  name: string;
  title: string;
  status: ValidationCheckStatus;
  error_code?: string;
  message?: string;
  started_at: string;
  completed_at?: string;
  duration_millis: number;
  images?: ImageMetadataResult[];
  details?: string[];
};

/** 一次预检的完整结构化结果 */
export type ValidationDetails = {
  validator_version: string;
  generation: number;
  checks: ValidationCheck[];
  capabilities: string[];
  started_at: string;
  completed_at?: string;
  duration_millis: number;
};

/** 执行节点（SSH 机器配置）对外表示，与 Go 端 `httpapi.HostResponse` 一一对应 */
export type Host = {
  id: string;
  name: string;
  host: string;
  port: number;
  username: string;
  workspace_root: string;
  is_default: boolean;
  enabled: boolean;
  /** 认证方式：后端只支持无口令私钥认证 */
  auth_method: "private_key";
  private_key_path: string;
  host_key_algorithm?: string;
  host_key_fingerprint?: string;
  last_connect_status: string;
  /** 环境预检总体状态 */
  validation_status: ValidationStatus;
  /** 最新校验结果的摘要或失败原因 */
  validation_message?: string;
  /** 各项校验的结构化结果 */
  validation_details?: ValidationDetails;
  validation_started_at?: string;
  validation_completed_at?: string;
  /** 校验代次，用于识别结果是否属于当前配置 */
  validation_generation: number;
  /** 是否允许用于真实远程任务执行；由服务端按准入规则计算 */
  usable_for_remote_task: boolean;
  created_at: string;
  updated_at: string;
};

/** `GET /api/v1/hosts` 的响应 */
export type HostListResponse = {
  records: Host[];
  total: number;
};

/** 新增执行节点的请求体；校验字段一律由服务端决定 */
export type CreateHostRequest = {
  id?: string;
  name?: string;
  host: string;
  port?: number;
  username: string;
  workspace_root?: string;
  /** 认证方式固定为私钥，可不传 */
  auth_method?: "private_key";
  /** 本机私钥文件路径，必填 */
  private_key_path: string;
  default?: boolean;
};

/** 修改执行节点的请求体；缺省字段表示不修改 */
export type UpdateHostRequest = {
  name?: string;
  host?: string;
  port?: number;
  username?: string;
  workspace_root?: string;
  auth_method?: "private_key";
  private_key_path?: string;
  enabled?: boolean;
  default?: boolean;
};

/**
 * 任务级工作流的执行状态。
 *
 * 取值与 Go 端 `domain.WorkflowRunStatus` 完全一致，不与 `TaskStatus` 混用：
 * `TaskStatus` 描述任务的业务阶段，本类型描述"这一次编排执行是否在推进"。
 * `blocked` 与 `recovery_required` 都不是终态，需要用户显式恢复。
 */
export type WorkflowRunStatus =
  | "pending"
  | "running"
  | "blocked"
  | "cancel_requested"
  | "cancelled"
  | "completed"
  | "failed"
  | "recovery_required";

/** 工作流当前所处的执行阶段 */
export type WorkflowStage = "waiting" | "step_execution" | "code_save" | "finalize" | "stopped";

/** 一次工作流执行的对外表示，与 Go 端 `httpapi.WorkflowRunResponse` 一一对应 */
export type WorkflowRun = {
  run_id: string;
  task_id: string;
  status: WorkflowRunStatus;
  /** 是否已结束（completed / cancelled / failed） */
  status_terminal: boolean;
  /** 是否正在推进 */
  status_active: boolean;
  stage: WorkflowStage;
  current_step_id?: string;
  current_attempt_id?: string;
  last_completed_step_id?: string;
  last_completed_sequence: number;
  completed_steps: number;
  total_steps: number;
  cancel_requested: boolean;
  error_class?: string;
  error_summary?: string;
  /**
   * 服务端是否具备代码保存能力。
   *
   * 为 false 时任务不会因为步骤全部通过验证而被判定为完整成功，页面据此如实提示，
   * 不把"步骤通过"显示为任务成功。
   */
  code_saver_available: boolean;
  plan_digest: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
  revision: number;
};

/** 单个步骤的工作流进度，字段全部来自数据库 */
export type WorkflowStep = {
  step_id: string;
  sequence: number;
  title: string;
  step_type: string;
  status: string;
  result?: string;
  error?: string;
  attempt_id?: string;
  started_at?: string;
  finished_at?: string;
};

/** `GET /api/v1/tasks/{taskID}/workflow` 的响应 */
export type WorkflowStatusResponse = {
  task_id: string;
  task_status: TaskStatus;
  /** 是否存在执行记录；为 false 时 run 缺省 */
  has_run: boolean;
  run?: WorkflowRun;
  steps: WorkflowStep[];
};

/** 启动工作流的请求体；步骤与顺序完全由服务端计划决定 */
export type StartWorkflowRequest = {
  prompt?: string;
  timeout_seconds?: number;
  validation_timeout_seconds?: number;
};

/** 恢复工作流的请求体 */
export type ResumeWorkflowRequest = {
  run_id: string;
  prompt?: string;
  timeout_seconds?: number;
  validation_timeout_seconds?: number;
};

/**
 * 统一任务启动的阶段。
 *
 * 取值与 Go 端 `domain.TaskLaunchStage` 完全一致：阶段只说明这一次启动走到了哪一步，
 * 不表示任务成功。前端据此展示"卡在哪个阶段"，不得自行推断。
 */
export type TaskLaunchStage = "validate" | "container" | "workspace" | "plan" | "workflow";

/**
 * 任务级工作区初始化状态。
 *
 * 取值与 Go 端 `domain.TaskWorkspaceInitStatus` 一致；只有 `succeeded` 与 `skipped`
 * 表示代码已就绪，其余取值都不是成功。
 */
export type TaskWorkspaceInitStatus =
  | "pending"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "timed_out"
  | "skipped"
  | "uncertain";

/** `POST /api/v1/tasks/{taskID}/start` 的请求体；只能传执行参数 */
export type StartTaskRequest = {
  /** 可选补充说明，会追加在计划与每一步骤的提示词之后 */
  prompt?: string;
  /** 单个步骤开发阶段的超时秒数，0 或省略表示使用任务配置或服务端默认值 */
  timeout_seconds?: number;
  /** 单个步骤验证阶段的超时秒数，0 或省略表示使用服务端默认值 */
  validation_timeout_seconds?: number;
  /** 本次规划调用的超时秒数，0 或省略表示使用任务配置或服务端默认值 */
  plan_timeout_seconds?: number;
};

/** 已持久化的计划步骤，与 Go 端 `httpapi.PlanStepResponse` 一一对应 */
export type PlanStep = {
  step_id: string;
  sequence: number;
  title: string;
  /** 步骤类型，与 Go 端 `domain.StepType` 一致 */
  step_type: string;
  /** 步骤状态，与 Go 端 `domain.StepStatus` 一致 */
  status: string;
  /** 锁定到步骤上的执行角色标识 */
  role_id?: string;
  /** 生效的必需检查名称 */
  required_checks: string[];
};

/**
 * `POST /api/v1/tasks/{taskID}/start` 的响应（202）。
 *
 * 结构显式区分四件事：走到了哪个阶段、任务当前真实状态、计划是否真的写入数据库、
 * 以及工作流是否真的启动。`workflow_started` 为 false 时不得把任务展示为已开始执行。
 */
export type StartTaskResponse = {
  task_id: string;
  /** 本次启动最后进入的阶段 */
  stage: TaskLaunchStage;
  /** 收尾后读取到的任务真实状态 */
  task_status: TaskStatus;
  /** 本次启动使用的容器登记记录 ID，未准备时缺省 */
  container_id?: string;
  /** 本次调用是否真的创建了容器 */
  container_prepared: boolean;
  /** 本次启动观察到的任务级工作区初始化状态，未走到该阶段时缺省 */
  workspace_status?: TaskWorkspaceInitStatus;
  /** 工作区初始化核验到的 commit SHA */
  workspace_commit?: string;
  /** 计划是否已完整写入数据库 */
  plan_persisted: boolean;
  /** 已持久化的计划步骤，按序号升序 */
  plan_steps: PlanStep[];
  /** 本次调用是否真的启动了工作流推进 */
  workflow_started: boolean;
  /** 启动后的执行记录，未启动时缺省 */
  run?: WorkflowRun;
  /** 失败阶段，成功时缺省 */
  failure_stage?: string;
  /** 失败原因摘要，成功时缺省 */
  failure?: string;
  started_at?: string;
  finished_at?: string;
};

/**
 * 环境类型。
 *
 * 取值与 Go 端 `domain.EnvironmentType` 一致：基础环境名称固定为 `base`，
 * 语言环境名称遵循 `^[a-z0-9][a-z0-9-]{0,31}$`。
 */
export type EnvironmentType = "base" | "language";

/** 环境版本的构建状态，取值与 Go 端 `domain.BuildStatus` 一致 */
export type BuildStatus = "pending" | "building" | "succeeded" | "failed";

/** 环境版本的对外表示，与 Go 端 `httpapi.EnvironmentVersionResponse` 一一对应 */
export type EnvironmentVersion = {
  /** 版本标签，例如 v0001 */
  version: string;
  /** 递增版本号 */
  number: number;
  /** 真实构建状态 */
  build_status: BuildStatus;
  /**
   * 该版本当前是否可以用于创建容器。
   *
   * 由服务端按"构建成功且已核实镜像 ID"计算，前端不得从 image_ref 或版本号推断。
   */
  usable: boolean;
  /** 创建版本时确定的镜像引用 */
  image_ref: string;
  /** 构建成功后核实的实际镜像 ID，未构建成功时缺省 */
  image_id?: string;
  /** 依赖的父基础环境版本标签，基础环境缺省 */
  parent_version?: string;
  /** 构建失败的可诊断摘要 */
  error_summary?: string;
  created_at: string;
  /** 构建完成时间，未构建成功时缺省 */
  built_at?: string;
};

/**
 * 单个环境的对外表示，与 Go 端 `httpapi.EnvironmentResponse` 一一对应。
 *
 * `current_version` 是当前可用版本，`latest_version` 是已创建的最大版本标签；
 * 两者语义不同，不能把最新版本默认当成可用版本。
 */
export type Environment = {
  type: EnvironmentType;
  name: string;
  /** 当前可用版本标签，没有可用版本时缺省 */
  current_version?: string;
  /** 已创建的最大版本标签 */
  latest_version: string;
  /** 该环境的全部版本，按版本号升序 */
  versions: EnvironmentVersion[];
};

/** `GET /api/v1/environments?host_id=…` 的响应 */
export type EnvironmentListResponse = {
  records: Environment[];
  total: number;
};

/**
 * 角色类型。
 *
 * 取值与 Go 端 `domain.RoleType` 完全一致：`planner` 规划、`developer` 开发、
 * `validator` 验证。创建任务时按类型分别选择对应角色，不得混用。
 */
export type RoleType = "planner" | "developer" | "validator";

/**
 * 角色的对外表示，与 Go 端 `httpapi.RoleResponse` 一一对应。
 *
 * 只包含创建任务选择角色所需的最小字段：`system_prompt`、工具策略与时间戳不在接口
 * 响应中，前端也不得请求或展示它们。已禁用角色仍会返回，调用方必须按 `enabled`
 * 自行排除。
 */
export type Role = {
  id: string;
  name: string;
  type: RoleType;
  /** 执行器标识 */
  runner: string;
  /** 模型标识 */
  model: string;
  /** 角色是否启用；为 false 时不可用于新任务 */
  enabled: boolean;
};

/** `GET /api/v1/roles` 的响应 */
export type RoleListResponse = {
  records: Role[];
  total: number;
};

/**
 * 步骤状态，取值与 Go 端 `domain.StepStatus` 完全一致。
 *
 * `running` / `developing` / `verifying` 表示步骤正被一次执行占用；
 * `completed` 只表示该步骤的执行与验证已收口，不代表任务通过验收。
 */
export type StepStatus =
  | "pending"
  | "running"
  | "developing"
  | "verifying"
  | "completed"
  | "failed"
  | "retry_wait"
  | "skipped"
  | "cancelled"
  | "recovery_required";

/**
 * 步骤的对外表示，与 Go 端 `httpapi.StepResponse` 一一对应。
 *
 * 字段全部来自数据库：`status_terminal` 是服务端派生的判据，前端不自行推断；
 * `current_attempt_id` 为空表示当前没有执行占用。
 */
export type Step = {
  id: string;
  task_id: string;
  /** 步骤类型，与 Go 端 `domain.StepType` 一致 */
  step_type: string;
  /** 步骤标题 */
  name: string;
  /** 步骤目标 */
  objective: string;
  sequence: number;
  status: StepStatus;
  /** 步骤是否已结束，前端据此决定是否需要轮询 */
  status_terminal: boolean;
  input_summary?: string;
  output_summary?: string;
  /** 失败原因摘要 */
  error?: string;
  /** 失败发生的阶段名 */
  failure_stage?: string;
  /** 当前正在执行的尝试，没有执行占用时缺省 */
  current_attempt_id?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
};

/** `GET /api/v1/tasks/{taskID}/steps` 的分页响应 */
export type StepListResponse = {
  records: Step[];
  total: number;
  current: number;
  size: number;
};

/**
 * 尝试状态，取值与 Go 端 `domain.AttemptStatus` 完全一致。
 *
 * `unknown` 与 `recovery_required` 表示结果无法确认，必须先核验再决定下一步。
 */
export type AttemptStatus =
  | "running"
  | "completed"
  | "failed"
  | "cancelled"
  | "interrupted"
  | "timed_out"
  | "unknown"
  | "recovery_required";

/**
 * 执行尝试的对外表示，与 Go 端 `httpapi.AttemptResponse` 一一对应。
 *
 * 一次尝试对应一次真实的 Agent 调用；`session_ids` 是该尝试关联的会话。
 */
export type Attempt = {
  id: string;
  task_id: string;
  step_id: string;
  attempt_number: number;
  status: AttemptStatus;
  status_terminal: boolean;
  /** 主执行退出码，未能取得时为 -1 */
  exit_code: number;
  timed_out: boolean;
  cancelled: boolean;
  error_class?: string;
  failure_stage?: string;
  error?: string;
  result_summary?: string;
  /** 本次尝试的验证判定，与验证流程状态不同 */
  verification_verdict?: string;
  retry_reason?: string;
  session_ids: string[];
  created_at: string;
  started_at: string;
  finished_at?: string;
};

/** `GET /api/v1/tasks/{taskID}/steps/{stepID}/attempts` 的分页响应 */
export type AttemptListResponse = {
  records: Attempt[];
  total: number;
  current: number;
  size: number;
};

/**
 * Agent 会话的对外表示，与 Go 端 `httpapi.SessionResponse` 一一对应。
 *
 * 执行状态、归档状态与导出状态是三个独立事实，不得互相推导。
 */
export type Session = {
  id: string;
  task_id: string;
  step_id?: string;
  attempt_id?: string;
  agent_role: string;
  role_id: string;
  provider: string;
  model: string;
  status: string;
  status_terminal: boolean;
  run_status?: string;
  /** 外部运行器的会话标识 */
  session_id?: string;
  invocation_id?: string;
  session_file_path?: string;
  archive_status: string;
  archive_path?: string;
  export_status: string;
  html_export_path?: string;
  exit_code?: number;
  error?: string;
  created_at: string;
  started_at: string;
  finished_at?: string;
};

/** `GET /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/sessions` 的分页响应 */
export type SessionListResponse = {
  records: Session[];
  total: number;
  current: number;
  size: number;
};

/**
 * 验证流程状态，与 Go 端 `domain.ValidationRunStatus` 一致。
 *
 * 它只表达"验证流程本身跑到哪一步"，与最终结论是两件事。
 */
export type ValidationRunStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "cancelled"
  | "timed_out";

/**
 * 验证结论，与 Go 端 `domain.VerificationVerdict` 一致。
 *
 * `inconclusive` 表示没有拿到可用证据，不能当作通过。
 */
export type VerificationVerdict = "passed" | "failed" | "inconclusive";

/** 单项检查的状态，与 Go 端 `domain.ValidationCheckStatus` 一致 */
export type ValidationCheckResultStatus =
  | "passed"
  | "failed"
  | "timed_out"
  | "cancelled"
  | "skipped"
  | "error";

/**
 * 单项检查结果，与 Go 端 `httpapi.ValidateCheckResultResponse` 一一对应。
 *
 * 完整输出只通过 `stdout_path` / `stderr_path` 暴露，响应里只有摘要。
 */
export type ValidationCheckResult = {
  check_id: string;
  check_name: string;
  required: boolean;
  sequence: number;
  status: ValidationCheckResultStatus;
  exit_code: number;
  timed_out: boolean;
  cancelled: boolean;
  /** 结果是否可信；为 false 时不得当作代码问题 */
  certain: boolean;
  required_passed: boolean;
  duration_ms: number;
  stdout_path?: string;
  stderr_path?: string;
  stdout_summary?: string;
  stderr_summary?: string;
  output_truncated: boolean;
  output_archive_error?: string;
  failure_reason?: string;
  error_detail?: string;
  started_at: string;
  finished_at: string;
};

/** 语义审查的结构化评估，与 Go 端 `domain.AgentAssessment` 一一对应 */
export type AgentAssessment = {
  passed: boolean;
  summary?: string;
  findings?: string[];
  evidence?: string[];
  suggest_fix?: boolean;
  needs_human_review?: boolean;
};

/**
 * 一次验证记录，与 Go 端 `httpapi.ValidationRunResponse` 一一对应。
 *
 * `status`、`verdict` 与检查结果分别输出：调用方必须能区分"流程跑完了"、
 * "结论是通过"与"哪些检查真实执行过"。
 */
export type ValidationRun = {
  id: string;
  task_id: string;
  step_id: string;
  attempt_id: string;
  status: ValidationRunStatus;
  verdict: VerificationVerdict;
  summary?: string;
  failure_summary?: string;
  error_detail?: string;
  passed_checks: number;
  failed_checks: number;
  skipped_checks: number;
  total_checks: number;
  execution_env?: string;
  workspace?: string;
  container_id?: string;
  review_ran: boolean;
  review_status?: string;
  review_summary?: string;
  validator_session_id?: string;
  assessment?: AgentAssessment;
  checks?: ValidationCheckResult[];
  started_at: string;
  finished_at?: string;
};

/**
 * 某个步骤的验证历史，与 Go 端 `httpapi.ValidationHistoryResponse` 一一对应。
 *
 * `state` 是服务端从最新记录派生的验证状态，前端不自行从原始记录推断。
 */
export type ValidationHistoryResponse = {
  step_id: string;
  state: string;
  latest?: ValidationRun;
  records: ValidationRun[];
  total: number;
};

/** 单条未通过检查的反馈，与 Go 端 `httpapi.ValidationCheckFeedbackResponse` 一一对应 */
export type ValidationCheckFeedback = {
  check_id: string;
  check_name: string;
  required: boolean;
  status: string;
  exit_code: number;
  failure_reason?: string;
  error_detail?: string;
  stdout_summary?: string;
  stderr_summary?: string;
  timed_out: boolean;
  cancelled: boolean;
  infrastructure_error: boolean;
  started_at: string;
  finished_at: string;
};

/**
 * 一次验证失败的反馈，与 Go 端 `httpapi.ValidationFailureFeedbackResponse` 一一对应。
 *
 * 它只描述"上一轮验证失败了什么"，不是当前验证结论。
 */
export type ValidationFailureFeedback = {
  task_id: string;
  step_id: string;
  developer_attempt_id: string;
  validation_run_id: string;
  developer_session_ids?: string[];
  verdict: VerificationVerdict;
  summary?: string;
  infrastructure_error: boolean;
  checks: ValidationCheckFeedback[];
  created_at: string;
};

/**
 * 单步骤执行的结论，与 Go 端 `domain.StepExecutionOutcome` 一致。
 *
 * 它只描述"本次 Agent 调用如何结束"，不代表步骤验收通过。
 */
export type StepExecutionOutcome =
  | "completed"
  | "failed"
  | "timed_out"
  | "cancelled"
  | "interrupted";

/** 单步骤执行的请求体；只能传执行参数 */
export type ExecuteStepRequest = {
  prompt?: string;
  timeout_seconds?: number;
};

/**
 * `POST /api/v1/tasks/{taskID}/steps/{stepID}/execute` 的响应。
 *
 * `success` 只表示本次调用正常结束且状态已完整写入，不表示任务成功。
 */
export type ExecuteStepResponse = {
  task_id: string;
  step_id: string;
  attempt_id: string;
  attempt_number: number;
  session_id?: string;
  outcome: StepExecutionOutcome;
  success: boolean;
  summary?: string;
  error?: string;
  exit_code: number;
  attempt_status: string;
  step_status: string;
  task_status: TaskStatus;
  session_status?: string;
  session_run_status?: string;
  archive_status: string;
  export_status: string;
  finalized: boolean;
  finalization_errors?: string[];
  started_at: string;
  finished_at: string;
};

/** 顺序执行的请求体；步骤顺序完全由服务端计划决定 */
export type ExecuteStepsRequest = {
  prompt?: string;
  timeout_seconds?: number;
};

/** 顺序执行中单个步骤的结果 */
export type ExecuteStepsStepResult = {
  step_id: string;
  sequence: number;
  title: string;
  skipped: boolean;
  skip_reason?: string;
  attempt_id?: string;
  session_id?: string;
  outcome?: StepExecutionOutcome;
  step_status?: string;
  summary?: string;
  error?: string;
  finalized: boolean;
  started_at?: string;
  finished_at?: string;
};

/**
 * `POST /api/v1/tasks/{taskID}/execute-steps` 的响应。
 *
 * `stopped` 与 `success` 不能互相替代：前者表示是否提前停止，后者表示全部步骤
 * 是否都以正常结束且状态已完整写入。
 */
export type ExecuteStepsResponse = {
  task_id: string;
  steps: ExecuteStepsStepResult[];
  executed_steps: number;
  stopped: boolean;
  success: boolean;
  stop_reason?: string;
  stopped_step_id?: string;
  task_status: TaskStatus;
  started_at: string;
  finished_at: string;
};

/** 开发—验证闭环的请求体；只能传执行参数 */
export type ExecuteAndValidateStepRequest = {
  prompt?: string;
  timeout_seconds?: number;
  validation_timeout_seconds?: number;
};

/**
 * `POST /api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate` 的响应。
 *
 * 开发结论、验证是否发起、验证结论与最终状态分别输出；`verified` 只有在真实验证
 * 通过时才为 true。
 */
export type ExecuteAndValidateStepResponse = {
  task_id: string;
  step_id: string;
  attempt_id?: string;
  attempt_number: number;
  session_id?: string;
  outcome: StepExecutionOutcome;
  developer_summary?: string;
  developer_error?: string;
  exit_code: number;
  developer_finalized: boolean;
  developer_finalization_errors?: string[];
  validation_ran: boolean;
  validation_skip_reason?: string;
  validation_run_id?: string;
  validation_status?: ValidationRunStatus;
  validation_verdict?: VerificationVerdict;
  validation_passed: boolean;
  validation_summary?: string;
  validation_failure_summary?: string;
  checks?: ValidationCheckResult[];
  validation_persisted: boolean;
  validation_persistence_errors?: string[];
  review_ran: boolean;
  review_status?: string;
  review_summary?: string;
  validator_session_id?: string;
  assessment?: AgentAssessment;
  infrastructure_error: boolean;
  feedback_persisted: boolean;
  verification_state: string;
  step_status: StepStatus;
  task_status: TaskStatus;
  verified: boolean;
  round: number;
  attempts: number;
  max_attempts: number;
  /** 修复循环的下一步动作：retry / completed / stop */
  repair_decision: string;
  repair_class?: string;
  repair_summary?: string;
  finalized: boolean;
  finalization_errors?: string[];
  started_at: string;
  finished_at: string;
};

/** 发起一次确定性验证的请求体；只能传验证参数 */
export type ValidateAttemptRequest = {
  timeout_seconds?: number;
};

/**
 * `POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate` 的响应。
 *
 * `status`、`verdict` 与 `persisted` 三者不能互相替代。
 */
export type ValidateAttemptResponse = {
  validation_run_id: string;
  task_id: string;
  step_id: string;
  attempt_id: string;
  status: ValidationRunStatus;
  verdict: VerificationVerdict;
  passed: boolean;
  summary?: string;
  failure_summary?: string;
  passed_checks: number;
  failed_checks: number;
  skipped_checks: number;
  execution_env?: string;
  workspace?: string;
  container_id?: string;
  persisted: boolean;
  persistence_errors?: string[];
  review_ran: boolean;
  review_status?: string;
  review_summary?: string;
  validator_session_id?: string;
  assessment?: AgentAssessment;
  checks: ValidationCheckResult[];
  started_at: string;
  finished_at: string;
};

/** 初始化代码的请求体；容器与仓库地址都由服务端按记录生成 */
export type InitializeCodeRequest = {
  work_dir?: string;
  timeout_seconds?: number;
};

/**
 * `POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code` 的响应。
 *
 * `succeeded` 表示代码是否已就绪，`persisted` 表示记录是否可靠写入，两者独立。
 */
export type InitializeCodeResponse = {
  record_id: string;
  task_id: string;
  step_id: string;
  attempt_id: string;
  source_type: string;
  repository?: string;
  status: string;
  succeeded: boolean;
  container_id: string;
  work_dir: string;
  base_branch?: string;
  task_branch: string;
  commit?: string;
  stage?: string;
  error_code?: string;
  error?: string;
  persisted: boolean;
  persistence_errors?: string[];
  started_at: string;
  finished_at: string;
};

/**
 * `POST /api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle` 的响应。
 *
 * `opened` 语义由 HTTP 状态码表达：成功即已登记，失败返回统一错误结构；
 * 该操作只登记新的预算作用域，不启动任何 Agent 调用。
 */
export type OpenRepairCycleResponse = {
  task_id: string;
  step_id: string;
  cycle_id: string;
  cycle_sequence: number;
  max_attempts: number;
  /** 上一周期已消耗的尝试数，用于说明"为什么允许重置" */
  previous_consumed: number;
  opened_at: string;
};

/**
 * `POST /api/v1/tasks/{taskID}/plan` 的请求体；只能传执行参数
 */
export type PlanRequest = {
  prompt?: string;
  timeout_seconds?: number;
};

/**
 * 计划中单个步骤的对外表示，与 Go 端 `httpapi.PlanStepResponse` 一一对应
 * （与统一启动响应中的 `PlanStep` 同形，复用同一类型）。
 */
export type PlanStepResult = PlanStep;

/**
 * `POST /api/v1/tasks/{taskID}/plan` 的响应。
 *
 * `persisted` 为 false 时计划没有写入数据库，前端不得展示为"计划已就绪"；
 * `failure_stage` / `failure` 说明失败发生在哪个阶段。
 */
export type PlanResponse = {
  task_id: string;
  task_status: TaskStatus;
  persisted: boolean;
  summary?: string;
  steps: PlanStepResult[];
  session_id?: string;
  failure_stage?: string;
  failure?: string;
  started_at?: string;
  finished_at?: string;
};
