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
  Environment,
  EnvironmentListResponse,
  EnvironmentType,
  EnvironmentVersion,
  ResumeWorkflowRequest,
  RoleListResponse,
  StartTaskRequest,
  StartTaskResponse,
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

/** 统一任务启动的路径：准备容器 → 初始化工作区 → 生成计划 → 启动工作流 */
export function taskStartPath(taskID: string): string {
  return `${taskPath(taskID)}/start`;
}

/**
 * 启动任务级工作流的路径。
 *
 * 它要求任务已有受管理容器与已持久化步骤，只能用于继续推进既有现场；
 * 新任务的显式启动必须走 `taskStartPath`（统一启动），否则必然 409。
 */
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
 * 统一启动任务：准备容器 → 初始化工作区 → 生成计划 → 启动工作流。
 *
 * 服务端只在启动流程得出真实结论后才返回响应，成功时是 202（表示"已受理并得出
 * 结论"，不表示任务已完成）。响应中的 `workflow_started` 才是"工作流真的启动了"
 * 的唯一依据；失败时服务端返回非 2xx 与统一错误结构，因此调用方不会拿到伪造的成功。
 *
 * 该操作有外部副作用（可能创建容器、初始化远程工作区、调用 Planner），因此调用方
 * 不得自动重试。
 */
export function startTask(
  taskID: string,
  body: StartTaskRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<StartTaskResponse>(taskStartPath(taskID), {
    ...options,
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
    // 启动流程包含远程容器准备与 Planner 调用，耗时远超普通请求，因此不设客户端超时；
    // 超时与取消由服务端流程自身管理，避免客户端提前断开后留下"未知状态"的现场。
    timeoutMs: options.timeoutMs ?? 0,
  });
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

/** 角色集合路径；本阶段只提供只读查询，不存在角色 CRUD */
export const ROLES_PATH = `${API_PREFIX}/roles`;

/**
 * 查询全部角色。
 *
 * 已禁用角色也会返回（`enabled=false`），用于诊断；创建任务的角色选择器必须自行
 * 排除它们，不能仅因为接口返回了记录就认为该角色可用于新任务。
 */
export function fetchRoles(options: ApiRequestOptions = {}) {
  return apiRequest<RoleListResponse>(ROLES_PATH, options);
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

/** 环境集合路径 */
export const ENVIRONMENTS_PATH = `${API_PREFIX}/environments`;

/**
 * 单个环境版本的路径。
 *
 * 环境引用由 `type` 与 `name` 两个路径段表达（与 Go 端 `httpapi` 的解析规则一致），
 * 不使用 `type/name` 单段形式：斜杠在 URL 中是路径分隔符，拼成单段会依赖隐式的
 * 转义解码规则。
 */
export function environmentVersionPath(
  type: EnvironmentType,
  name: string,
  number: number,
): string {
  if (!name.trim()) {
    throw new Error("环境名称不能为空");
  }
  if (!Number.isInteger(number) || number <= 0) {
    throw new Error(`环境版本号必须是正整数: ${number}`);
  }
  return `${ENVIRONMENTS_PATH}/${encodeURIComponent(type)}/${encodeURIComponent(
    name,
  )}/versions/${number}`;
}

/**
 * 查询指定执行节点上的环境与全部版本。
 *
 * `host_id` 是唯一节点定位方式，缺失时服务端返回 400；调用方不应在未填写执行节点时
 * 发起请求。查询是纯读取：不建立 SSH / Docker 连接，也不产生远程副作用。
 */
export function fetchEnvironments(hostID: string, options: ApiRequestOptions = {}) {
  const trimmed = hostID.trim();
  if (!trimmed) {
    throw new Error("执行节点标识不能为空");
  }
  const params = new URLSearchParams({ host_id: trimmed });
  return apiRequest<EnvironmentListResponse>(`${ENVIRONMENTS_PATH}?${params.toString()}`, options);
}

/** 查询单个环境版本的构建状态与元数据 */
export function fetchEnvironmentVersion(
  hostID: string,
  type: EnvironmentType,
  name: string,
  number: number,
  options: ApiRequestOptions = {},
) {
  const trimmed = hostID.trim();
  if (!trimmed) {
    throw new Error("执行节点标识不能为空");
  }
  const params = new URLSearchParams({ host_id: trimmed });
  return apiRequest<EnvironmentVersion>(
    `${environmentVersionPath(type, name, number)}?${params.toString()}`,
    options,
  );
}

/**
 * 从环境记录中取可用于任务启动的版本。
 *
 * 只有服务端标记 `usable` 的版本才可以被选中；`current_version` 是服务端计算的
 * 当前可用版本，`latest_version` 只是最新创建版本，二者不能混用。找不到可用版本时
 * 返回 undefined，调用方必须提示"该环境暂不可用"，不得猜测一个版本。
 */
export function usableEnvironmentVersions(environment: Environment): EnvironmentVersion[] {
  return environment.versions.filter((version) => version.usable);
}

/**
 * 取环境的默认可用版本：优先服务端给出的 `current_version`，否则取最后一个可用版本。
 *
 * 返回值仍必须是 `usable` 的版本，绝不会退回到构建中或构建失败的版本。
 */
export function defaultUsableVersion(environment: Environment): EnvironmentVersion | undefined {
  const usable = usableEnvironmentVersions(environment);
  if (usable.length === 0) {
    return undefined;
  }
  const current = environment.current_version;
  if (current) {
    const matched = usable.find((version) => version.version === current);
    if (matched) {
      return matched;
    }
  }
  return usable[usable.length - 1];
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
