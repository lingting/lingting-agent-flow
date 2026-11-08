/**
 * 需求规划接口：规划会话、消息、方案版本与两阶段分析。
 *
 * 约定：
 * - 全部请求经统一 `apiRequest` 发出，页面不直接调用 `fetch`；
 * - 路径与服务端 `httpapi.PathPlanningSessions` 一致，标识按段编码；
 * - 会话状态、消息发送方与 Revision 快照都原样保留服务端取值，前端不新增状态名，
 *   也不在本层补默认值；
 * - 两阶段分析是同步长请求（后端每个阶段默认 30 分钟），因此 `/analysis` 与
 *   `/analysis/retry` 显式覆盖客户端默认超时，生命周期交给服务端阶段超时与请求上下文。
 */
import { API_PREFIX, apiRequest, type ApiRequestOptions } from "./client";

/** 规划会话集合路径 */
export const PLANNING_SESSIONS_PATH = `${API_PREFIX}/planning/sessions`;

/** 需求分析快照：八个字段与 Go 端 `domain.RequirementAnalysis` 一一对应 */
export type RequirementAnalysis = {
  summary: string;
  goals: string[];
  current_state: string[];
  scope: string[];
  out_of_scope: string[];
  constraints: string[];
  risks: string[];
  acceptance_criteria: string[];
};

/** 实施方案中的任务草案；它不是正式 Task，只表达依赖与顺序 */
export type ImplementationPlanTask = {
  id: string;
  title: string;
  description: string;
  /** 前置任务草案标识；空数组表示无前置依赖 */
  depends_on: string[];
  acceptance_criteria: string[];
};

/** 实施方案快照 */
export type ImplementationPlan = {
  summary: string;
  /** 顺序即执行顺序 */
  tasks: ImplementationPlanTask[];
};

/**
 * 规划会话。
 *
 * `status` 保留 `string`：服务端读取路径保留未知状态原样输出，前端不得把它映射成
 * 某个默认状态。`host_id` / `container_id` / `source_commit` 等字段在服务端省略，
 * 表示尚未绑定执行节点或尚未准备运行环境。
 */
export type PlanningSession = {
  id: string;
  project_id: string;
  status: string;
  host_id?: string;
  container_id?: string;
  source_commit?: string;
  analyst_session_id?: string;
  planner_session_id?: string;
  adopted_revision_id?: string;
  error_summary?: string;
  created_at: string;
  updated_at: string;
  finished_at?: string;
};

/** 一条规划消息；`sender_type` 同样保留未知取值 */
export type PlanningMessage = {
  id: string;
  planning_session_id: string;
  sequence: number;
  sender_type: string;
  role_id?: string;
  content: string;
  created_at: string;
};

/**
 * 一版规划快照。
 *
 * 两个快照在服务端写库之前就已经通过结构化校验，接口按原始 JSON 输出落库内容，
 * 因此这里直接使用领域类型，前端不重复定义校验规则，也不裁剪字段。
 */
export type PlanningRevision = {
  id: string;
  planning_session_id: string;
  revision_number: number;
  snapshot_version: number;
  requirement_analysis: RequirementAnalysis;
  implementation_plan: ImplementationPlan;
  /** 本轮结构化校验结论；尚未校验时服务端省略该字段 */
  validation_result?: unknown;
  created_at: string;
};

/** 会话列表响应；服务端固定使用 records / total */
export type PlanningSessionListResponse = {
  records: PlanningSession[];
  total: number;
};

/** 单会话查询与创建、放弃的响应体 */
export type PlanningSessionDetailResponse = {
  session: PlanningSession;
};

/** 消息列表响应 */
export type PlanningMessageListResponse = {
  records: PlanningMessage[];
  total: number;
};

/** 提交用户消息的响应体：会话已推进到 analyzing，是否附带本轮用户消息由服务端决定 */
export type PlanningMessageSubmitResponse = {
  session: PlanningSession;
  message?: PlanningMessage;
};

/** 方案版本列表响应 */
export type PlanningRevisionListResponse = {
  records: PlanningRevision[];
  total: number;
};

/** 一轮两阶段分析的结果：只有真实落库后服务端才返回它 */
export type PlanningAnalysisResponse = {
  session: PlanningSession;
  revision: PlanningRevision;
  messages: PlanningMessage[];
  requirement_analysis: RequirementAnalysis;
  implementation_plan: ImplementationPlan;
};

/** 创建规划会话的请求体：只接受项目标识 */
export type CreatePlanningSessionRequest = {
  project_id: string;
};

/** 提交用户消息的请求体：发送方、序号与时间全部由服务端决定 */
export type SubmitPlanningMessageRequest = {
  content: string;
};

/** 分析请求体：`host_id` 仅在运行环境尚未准备时使用 */
export type PlanningAnalysisRequest = {
  host_id?: string;
};

/** 显式重试请求体：可选的用户补充消息与执行节点 */
export type PlanningAnalysisRetryRequest = {
  content?: string;
  host_id?: string;
};

/** 单个规划会话的路径 */
export function planningSessionPath(sessionID: string): string {
  if (!sessionID.trim()) {
    throw new Error("规划会话标识不能为空");
  }
  return `${PLANNING_SESSIONS_PATH}/${encodeURIComponent(sessionID)}`;
}

/** 会话消息子资源路径 */
export function planningMessagesPath(sessionID: string): string {
  return `${planningSessionPath(sessionID)}/messages`;
}

/** 会话方案版本子资源路径 */
export function planningRevisionsPath(sessionID: string): string {
  return `${planningSessionPath(sessionID)}/revisions`;
}

/** 两阶段分析动作路径 */
export function planningAnalysisPath(sessionID: string): string {
  return `${planningSessionPath(sessionID)}/analysis`;
}

/** 显式重试动作路径 */
export function planningAnalysisRetryPath(sessionID: string): string {
  return `${planningAnalysisPath(sessionID)}/retry`;
}

/** 放弃规划动作路径 */
export function planningAbandonPath(sessionID: string): string {
  return `${planningSessionPath(sessionID)}/abandon`;
}

/** 按项目列出规划会话；服务端要求 project_id 查询参数，缺省返回 400 */
export function fetchPlanningSessions(projectID: string, options: ApiRequestOptions = {}) {
  const project = projectID.trim();
  if (!project) {
    throw new Error("查询规划会话必须指定项目标识");
  }
  const query = `?project_id=${encodeURIComponent(project)}`;
  return apiRequest<PlanningSessionListResponse>(`${PLANNING_SESSIONS_PATH}${query}`, options);
}

/** 创建规划会话；项目不存在时服务端返回 404 */
export function createPlanningSession(
  body: CreatePlanningSessionRequest,
  options: ApiRequestOptions = {},
) {
  return apiRequest<PlanningSessionDetailResponse>(PLANNING_SESSIONS_PATH, {
    ...options,
    body: JSON.stringify({ project_id: body.project_id }),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
  });
}

/** 查询会话详情 */
export function fetchPlanningSession(sessionID: string, options: ApiRequestOptions = {}) {
  return apiRequest<PlanningSessionDetailResponse>(planningSessionPath(sessionID), options);
}

/** 查询会话消息，服务端按序号升序返回 */
export function fetchPlanningMessages(sessionID: string, options: ApiRequestOptions = {}) {
  return apiRequest<PlanningMessageListResponse>(planningMessagesPath(sessionID), options);
}

/** 追加一条用户消息；成功后会话进入 analyzing，模型调用尚未发生 */
export function submitPlanningMessage(
  sessionID: string,
  body: SubmitPlanningMessageRequest,
  options: ApiRequestOptions = {},
) {
  return apiRequest<PlanningMessageSubmitResponse>(planningMessagesPath(sessionID), {
    ...options,
    body: JSON.stringify({ content: body.content }),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
  });
}

/** 查询全部方案版本，服务端按版本号升序返回 */
export function fetchPlanningRevisions(sessionID: string, options: ApiRequestOptions = {}) {
  return apiRequest<PlanningRevisionListResponse>(planningRevisionsPath(sessionID), options);
}

/**
 * 同步执行一轮两阶段分析。
 *
 * 默认不设置客户端超时：一轮分析包含两次模型调用，累计时间可能远超统一请求层的
 * 10 秒默认值，此时按超时中断会把仍在服务端执行的分析误判为失败。调用方也不传
 * 取消信号，页面卸载不会取消这次请求。
 */
export function runPlanningAnalysis(
  sessionID: string,
  body: PlanningAnalysisRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<PlanningAnalysisResponse>(planningAnalysisPath(sessionID), {
    ...options,
    body: JSON.stringify({ host_id: body.host_id ?? "" }),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
    timeoutMs: options.timeoutMs ?? 0,
  });
}

/**
 * 显式重试失败会话（或无执行占用的 analyzing 会话）。
 *
 * 与 `/analysis` 一样是长请求：同样不设置客户端超时，也不由页面取消。执行权由服务端
 * 数据库条件更新判定，已有执行占用时返回 409，页面不得据此自动重复提交。
 */
export function retryPlanningAnalysis(
  sessionID: string,
  body: PlanningAnalysisRetryRequest = {},
  options: ApiRequestOptions = {},
) {
  const payload: Record<string, string> = {};
  const content = body.content;
  if (content !== undefined) {
    payload.content = content;
  }
  const hostID = body.host_id?.trim();
  if (hostID) {
    payload.host_id = hostID;
  }
  return apiRequest<PlanningAnalysisResponse>(planningAnalysisRetryPath(sessionID), {
    ...options,
    body: JSON.stringify(payload),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
    timeoutMs: options.timeoutMs ?? 0,
  });
}

/** 放弃规划；只写会话状态，消息与方案版本全部保留 */
export function abandonPlanningSession(sessionID: string, options: ApiRequestOptions = {}) {
  return apiRequest<PlanningSessionDetailResponse>(planningAbandonPath(sessionID), {
    ...options,
    method: "POST",
  });
}
