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
