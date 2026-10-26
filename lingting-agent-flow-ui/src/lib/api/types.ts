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
