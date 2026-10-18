/**
 * 本阶段已实现的 API 端点。
 *
 * 只封装真实存在的接口；任务、项目、机器、环境等业务接口在实现后追加到这里，
 * 页面不直接调用 `fetch`。
 */
import { API_PREFIX, apiRequest, type ApiRequestOptions } from "./client";
import type { HealthResponse, SystemInfoResponse } from "./types";

/** 健康检查路径 */
export const HEALTH_PATH = `${API_PREFIX}/health`;

/** 系统信息路径 */
export const SYSTEM_INFO_PATH = `${API_PREFIX}/system/info`;

/**
 * 查询 HTTP API 与本地持久化的可用性。
 *
 * 服务不可用时后端返回 503 与统一错误结构，调用方会收到 `ApiError`。
 */
export function fetchHealth(options: ApiRequestOptions = {}) {
  return apiRequest<HealthResponse>(HEALTH_PATH, options);
}

/** 查询程序版本等系统信息。 */
export function fetchSystemInfo(options: ApiRequestOptions = {}) {
  return apiRequest<SystemInfoResponse>(SYSTEM_INFO_PATH, options);
}
