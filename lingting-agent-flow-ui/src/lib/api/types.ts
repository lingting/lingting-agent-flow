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
  /** 任务是否已结束，用于决定可用的操作 */
  status_terminal: boolean;
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
