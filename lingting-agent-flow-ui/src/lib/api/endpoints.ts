/**
 * 本阶段已实现的 API 端点。
 *
 * 只封装真实存在的接口；项目、机器、环境等业务接口在实现后追加到这里，
 * 页面不直接调用 `fetch`。
 */
import { API_PREFIX, ApiError, apiRequest, type ApiRequestOptions } from "./client";
import type { ApiErrorBody, ApiErrorResponse } from "./client";
import type {
  CreateHostRequest,
  CreateTaskRequest,
  ResumeWorkflowRequest,
  StartWorkflowRequest,
  WorkflowRun,
  WorkflowStatusResponse,
  HealthResponse,
  Host,
  HostListResponse,
  SystemInfoResponse,
  Task,
  TaskListQuery,
  TaskListResponse,
  UpdateHostRequest,
} from "./types";

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

/** 任务集合路径 */
export const TASKS_PATH = `${API_PREFIX}/tasks`;

/** 单个任务的路径 */
export function taskPath(taskID: string): string {
  if (!taskID.trim()) {
    throw new Error("任务标识不能为空");
  }
  return `${TASKS_PATH}/${encodeURIComponent(taskID)}`;
}

/** 取消任务的路径 */
export function taskCancelPath(taskID: string): string {
  return `${taskPath(taskID)}/cancel`;
}

/** 任务工作流状态查询路径 */
export function taskWorkflowPath(taskID: string): string {
  return `${taskPath(taskID)}/workflow`;
}

/** 启动任务级工作流的路径 */
export function taskExecuteWorkflowPath(taskID: string): string {
  return `${taskPath(taskID)}/execute-workflow`;
}

/** 取消任务级工作流的路径 */
export function taskCancelWorkflowPath(taskID: string): string {
  return `${taskPath(taskID)}/cancel-workflow`;
}

/** 恢复任务级工作流的路径 */
export function taskResumeWorkflowPath(taskID: string): string {
  return `${taskPath(taskID)}/resume-workflow`;
}

/** 查询任务当前的工作流进度；任务从未启动过工作流时 has_run 为 false */
export function fetchTaskWorkflow(taskID: string, options: ApiRequestOptions = {}) {
  return apiRequest<WorkflowStatusResponse>(taskWorkflowPath(taskID), options);
}

/**
 * 启动任务级工作流。
 *
 * 服务端立即返回 202 与执行记录的真实初始状态，编排在后台推进；调用方通过
 * `fetchTaskWorkflow` 轮询进度，不能把 202 当作"任务已完成"。
 */
export function startTaskWorkflow(
  taskID: string,
  body: StartWorkflowRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<WorkflowRun>(taskExecuteWorkflowPath(taskID), {
    ...options,
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
  });
}

/** 请求取消任务级工作流；返回取消请求落盘后的真实状态 */
export function cancelTaskWorkflow(taskID: string, options: ApiRequestOptions = {}) {
  return apiRequest<WorkflowRun>(taskCancelWorkflowPath(taskID), {
    ...options,
    method: "POST",
  });
}

/** 恢复已停止的任务级工作流 */
export function resumeTaskWorkflow(
  taskID: string,
  body: ResumeWorkflowRequest,
  options: ApiRequestOptions = {},
) {
  return apiRequest<WorkflowRun>(taskResumeWorkflowPath(taskID), {
    ...options,
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
  });
}

/** 把查询参数拼接到任务列表路径上，空值不参与拼接 */
function taskListPath(query: TaskListQuery): string {
  const params = new URLSearchParams();
  if (query.current !== undefined) params.set("current", String(query.current));
  if (query.size !== undefined) params.set("size", String(query.size));
  for (const status of query.status ?? []) {
    params.append("status", status);
  }
  const encoded = params.toString();
  return encoded ? `${TASKS_PATH}?${encoded}` : TASKS_PATH;
}

/** 查询任务列表，支持分页与状态筛选 */
export function fetchTasks(query: TaskListQuery = {}, options: ApiRequestOptions = {}) {
  return apiRequest<TaskListResponse>(taskListPath(query), options);
}

/** 查询单个任务详情 */
export function fetchTask(taskID: string, options: ApiRequestOptions = {}) {
  return apiRequest<Task>(taskPath(taskID), options);
}

/** 创建任务；成功时返回 201 与创建后的完整任务对象 */
export function createTask(body: CreateTaskRequest, options: ApiRequestOptions = {}) {
  return apiRequest<Task>(TASKS_PATH, {
    ...options,
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
  });
}

/** 请求取消任务；返回取消后的任务状态 */
export function cancelTask(taskID: string, options: ApiRequestOptions = {}) {
  return apiRequest<Task>(taskCancelPath(taskID), { ...options, method: "POST" });
}

/**
 * 删除任务。
 *
 * 服务端成功时返回 204 且无响应体，因此这里不使用 `apiRequest`（它要求 JSON 响应体）。
 */
export async function deleteTask(taskID: string, options: ApiRequestOptions = {}) {
  const path = taskPath(taskID);
  const method = "DELETE";
  const controller = new AbortController();
  const externalSignal = options.signal;
  const abortFromCaller = () => controller.abort();
  if (externalSignal) {
    if (externalSignal.aborted) {
      throw new ApiError("aborted", "请求已取消");
    }
    externalSignal.addEventListener("abort", abortFromCaller, { once: true });
  }
  try {
    let response: Response;
    try {
      response = await fetch(path, {
        headers: { Accept: "application/json", ...options.headers },
        method,
        signal: controller.signal,
      });
    } catch (error) {
      if (externalSignal?.aborted) {
        throw new ApiError("aborted", "请求已取消", { cause: error });
      }
      throw new ApiError("network", "无法连接 AgentFlow 服务", { cause: error });
    }
    if (response.ok) {
      return { request: { method, path } };
    }
    const text = await response.text();
    let detail: ApiErrorBody | undefined;
    try {
      const parsed = JSON.parse(text) as unknown;
      if (typeof parsed === "object" && parsed !== null && "error" in parsed) {
        detail = (parsed as ApiErrorResponse).error;
      }
    } catch {
      detail = undefined;
    }
    throw new ApiError("http", detail?.message ?? `服务返回错误状态 ${response.status}`, {
      code: detail?.code,
      requestId: detail?.request_id ?? response.headers.get("X-Request-Id") ?? undefined,
      status: response.status,
    });
  } finally {
    externalSignal?.removeEventListener("abort", abortFromCaller);
  }
}

/** 执行节点集合路径 */
export const HOSTS_PATH = `${API_PREFIX}/hosts`;

/** 单个执行节点的路径 */
export function hostPath(hostID: string): string {
  if (!hostID.trim()) {
    throw new Error("执行节点标识不能为空");
  }
  return `${HOSTS_PATH}/${encodeURIComponent(hostID)}`;
}

/** 重新校验执行节点的路径 */
export function hostValidatePath(hostID: string): string {
  return `${hostPath(hostID)}/validate`;
}

/** 查询全部执行节点 */
export function fetchHosts(options: ApiRequestOptions = {}) {
  return apiRequest<HostListResponse>(HOSTS_PATH, options);
}

/** 查询单个执行节点 */
export function fetchHost(hostID: string, options: ApiRequestOptions = {}) {
  return apiRequest<Host>(hostPath(hostID), options);
}

/** 新增执行节点；成功时返回 201 与真实初始状态（checking） */
export function createHost(body: CreateHostRequest, options: ApiRequestOptions = {}) {
  return apiRequest<Host>(HOSTS_PATH, {
    ...options,
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
  });
}

/** 修改执行节点 */
export function updateHost(
  hostID: string,
  body: UpdateHostRequest,
  options: ApiRequestOptions = {},
) {
  return apiRequest<Host>(hostPath(hostID), {
    ...options,
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "PATCH",
  });
}

/**
 * 请求重新校验执行节点。
 *
 * 服务端立即返回 202 与 checking 状态，校验在后台执行，调用方需要轮询详情。
 */
export function validateHost(hostID: string, options: ApiRequestOptions = {}) {
  return apiRequest<Host>(hostValidatePath(hostID), { ...options, method: "POST" });
}

/**
 * 删除执行节点（逻辑删除）。
 *
 * 服务端成功时返回 204 且无响应体，因此这里不使用 `apiRequest`。
 */
export async function deleteHost(hostID: string, options: ApiRequestOptions = {}) {
  const path = hostPath(hostID);
  const method = "DELETE";
  const controller = new AbortController();
  const externalSignal = options.signal;
  const abortFromCaller = () => controller.abort();
  if (externalSignal) {
    if (externalSignal.aborted) {
      throw new ApiError("aborted", "请求已取消");
    }
    externalSignal.addEventListener("abort", abortFromCaller, { once: true });
  }
  try {
    let response: Response;
    try {
      response = await fetch(path, {
        headers: { Accept: "application/json", ...options.headers },
        method,
        signal: controller.signal,
      });
    } catch (error) {
      if (externalSignal?.aborted) {
        throw new ApiError("aborted", "请求已取消", { cause: error });
      }
      throw new ApiError("network", "无法连接 AgentFlow 服务", { cause: error });
    }
    if (response.ok) {
      return { request: { method, path } };
    }
    const text = await response.text();
    let detail: ApiErrorBody | undefined;
    try {
      const parsed = JSON.parse(text) as unknown;
      if (typeof parsed === "object" && parsed !== null && "error" in parsed) {
        detail = (parsed as ApiErrorResponse).error;
      }
    } catch {
      detail = undefined;
    }
    throw new ApiError("http", detail?.message ?? `服务返回错误状态 ${response.status}`, {
      code: detail?.code,
      requestId: detail?.request_id ?? response.headers.get("X-Request-Id") ?? undefined,
      status: response.status,
    });
  } finally {
    externalSignal?.removeEventListener("abort", abortFromCaller);
  }
}
