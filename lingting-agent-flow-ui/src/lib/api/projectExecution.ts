/**
 * 项目执行接口：项目执行状态、开始 / 停止、项目任务列表与需求进度。
 *
 * 约定：
 * - 全部请求经统一 `apiRequest` 发出，页面不直接调用 `fetch`；
 * - 开始与停止不接受请求体：任务状态、执行权、epoch 与执行结果全部由服务端依据持久化
 *   事实计算，前端无法指定"执行哪个任务"或"跳过前序任务"；
 * - 状态与进度原样保留服务端取值，前端不新增状态名，也不补默认值：未知取值按只读方式
 *   展示，避免把服务端新增状态显示成"未知错误"；
 * - 查询接口是只读的：轮询执行状态不会触发启动、续租或释放。
 */
import { apiRequest, type ApiRequestOptions } from "./client";
import { PROJECTS_PATH } from "./endpoints";
import { REQUIREMENTS_PATH } from "./planning";

/** 任务数量统计，与服务端 `ProjectTaskCountsResponse` 一一对应 */
export type ProjectTaskCounts = {
  total: number;
  completed: number;
  running: number;
  pending: number;
  failed: number;
  recovery_required: number;
  cancelled: number;
  paused: number;
};

/** 项目执行状态投影；未知取值原样返回 */
export type ProjectExecutionStatus = string;

/** 项目执行状态 */
export type ProjectExecutionSnapshot = {
  project_id: string;
  status: ProjectExecutionStatus;
  counts: ProjectTaskCounts;
  requirements: number;
  blocked: boolean;
  blocked_task_id?: string;
  blocked_reason?: string;
  current_task_id?: string;
  next_task_id?: string;
  lease_active: boolean;
  lease_epoch?: number;
  lease_expires_at?: string;
  stop_requested: boolean;
  stopping: boolean;
};

/** 项目内一个正式任务（按项目内执行顺序返回） */
export type ProjectExecutionTask = {
  id: string;
  requirement_id: string;
  requirement_title: string;
  requirement_sequence: number | null;
  position: number;
  status: string;
  host_id?: string;
  cancel_requested: boolean;
  blocking: boolean;
  blocking_reason?: string;
  failure_summary?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
};

/** 项目执行状态与任务列表的组合响应 */
export type ProjectExecutionResponse = {
  project_id: string;
  execution: ProjectExecutionSnapshot;
  tasks: ProjectExecutionTask[];
};

/** 项目正式任务列表响应 */
export type ProjectTaskListResponse = {
  records: ProjectExecutionTask[];
  total: number;
};

/** 需求进度；未知进度状态原样返回 */
export type RequirementProgressResponse = {
  requirement_id: string;
  status: string;
  counts: ProjectTaskCounts;
  current: number;
  total: number;
  blocked: boolean;
  blocked_task_id?: string;
  blocked_reason?: string;
  current_task_id?: string;
  next_task_id?: string;
};

/** 单个项目的路径前缀 */
function projectPathPrefix(projectID: string): string {
  const trimmed = projectID.trim();
  if (!trimmed) {
    throw new Error("项目标识不能为空");
  }
  return `${PROJECTS_PATH}/${encodeURIComponent(trimmed)}`;
}

/** 开始项目执行的路径 */
export function projectStartPath(projectID: string): string {
  return `${projectPathPrefix(projectID)}/start`;
}

/** 请求停止项目执行的路径 */
export function projectCancelPath(projectID: string): string {
  return `${projectPathPrefix(projectID)}/cancel`;
}

/** 项目执行状态的路径 */
export function projectExecutionPath(projectID: string): string {
  return `${projectPathPrefix(projectID)}/execution`;
}

/** 项目正式任务列表的路径 */
export function projectTasksPath(projectID: string): string {
  return `${projectPathPrefix(projectID)}/tasks`;
}

/** 需求进度的路径 */
export function requirementProgressPath(requirementID: string): string {
  const trimmed = requirementID.trim();
  if (!trimmed) {
    throw new Error("需求标识不能为空");
  }
  return `${REQUIREMENTS_PATH}/${encodeURIComponent(trimmed)}/progress`;
}

/**
 * 开始项目执行。
 *
 * 成功后服务端已取得项目执行权并触发一次扫描；任务是否真的开始执行只能由随后的状态
 * 查询确认，因此页面在成功后重新读取执行状态，不用按钮本地状态推断。
 */
export function startProjectExecution(projectID: string, options: ApiRequestOptions = {}) {
  return apiRequest<ProjectExecutionResponse>(projectStartPath(projectID), {
    ...options,
    method: "POST",
  });
}

/**
 * 请求停止项目执行。
 *
 * 服务端只记录停止意图并调用既有任务取消入口；在途任务真实收敛前状态仍是执行中，
 * 页面必须按返回的真实状态展示，不能显示为"已停止"。
 */
export function cancelProjectExecution(projectID: string, options: ApiRequestOptions = {}) {
  return apiRequest<ProjectExecutionResponse>(projectCancelPath(projectID), {
    ...options,
    method: "POST",
  });
}

/** 查询项目执行状态与任务列表（只读） */
export function fetchProjectExecution(projectID: string, options: ApiRequestOptions = {}) {
  return apiRequest<ProjectExecutionResponse>(projectExecutionPath(projectID), options);
}

/** 查询项目正式任务列表（只读） */
export function fetchProjectTasks(projectID: string, options: ApiRequestOptions = {}) {
  return apiRequest<ProjectTaskListResponse>(projectTasksPath(projectID), options);
}

/** 查询需求进度（只读） */
export function fetchRequirementProgress(
  requirementID: string,
  options: ApiRequestOptions = {},
) {
  return apiRequest<RequirementProgressResponse>(requirementProgressPath(requirementID), options);
}
