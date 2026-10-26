/**
 * 执行现场相关接口：计划、步骤、尝试、会话、验证与修复周期。
 *
 * 与 `endpoints.ts` 的分工：那里是任务、执行节点、环境、角色等管理资源，这里是
 * 挂在任务之下的执行链资源。两者共用同一套 `apiRequest` 与 `ApiError`，页面不
 * 直接调用 `fetch`，也不自行拼接业务状态。
 *
 * 路径形状与 Go 端 `internal/httpapi` 的解析规则一一对应：
 *   - 计划：`{taskID}/plan`
 *   - 顺序执行：`{taskID}/execute-steps`
 *   - 步骤：`{taskID}/steps`、`{taskID}/steps/{stepID}/execute`、
 *     `{taskID}/steps/{stepID}/execute-and-validate`、
 *     `{taskID}/steps/{stepID}/open-repair-cycle`
 *   - 尝试：`{taskID}/steps/{stepID}/attempts`、
 *     `{taskID}/steps/{stepID}/attempts/{attemptID}/validate`、
 *     `{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code`
 *   - 会话：`{taskID}/steps/{stepID}/attempts/{attemptID}/sessions`
 *   - 验证：`{taskID}/steps/{stepID}/validations`、
 *     `{taskID}/steps/{stepID}/validations/{runID}/feedback`
 */
import { apiRequest, type ApiRequestOptions } from "./client";
import { taskPath } from "./endpoints";
import type {
  AttemptListResponse,
  ExecuteAndValidateStepRequest,
  ExecuteAndValidateStepResponse,
  ExecuteStepRequest,
  ExecuteStepResponse,
  ExecuteStepsRequest,
  ExecuteStepsResponse,
  InitializeCodeRequest,
  InitializeCodeResponse,
  OpenRepairCycleResponse,
  PlanRequest,
  PlanResponse,
  SessionListResponse,
  StepListResponse,
  ValidateAttemptRequest,
  ValidateAttemptResponse,
  ValidationFailureFeedback,
  ValidationHistoryResponse,
} from "./types";

/**
 * 单次执行类请求的默认客户端超时。
 *
 * 这些接口都是同步的：服务端要等 Agent 调用或检查命令真正结束后才返回。客户端不设
 * 超时，避免在服务端仍在执行时提前断开，留下"前端以为失败、后端仍在跑"的现场；
 * 超时与取消由服务端流程自身管理。
 */
const NO_CLIENT_TIMEOUT = 0;

/** 校验路径段标识，空值直接拒绝而不是拼出一个无效路径 */
function requireSegment(value: string, label: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    throw new Error(`${label}不能为空`);
  }
  return encodeURIComponent(trimmed);
}

/** 计划生成路径 */
export function taskPlanPath(taskID: string): string {
  return `${taskPath(taskID)}/plan`;
}

/** 顺序执行路径 */
export function taskExecuteStepsPath(taskID: string): string {
  return `${taskPath(taskID)}/execute-steps`;
}

/** 步骤集合路径 */
export function taskStepsPath(taskID: string): string {
  return `${taskPath(taskID)}/steps`;
}

/** 单个步骤路径 */
export function stepPath(taskID: string, stepID: string): string {
  return `${taskStepsPath(taskID)}/${requireSegment(stepID, "步骤标识")}`;
}

/** 单步骤执行路径 */
export function stepExecutePath(taskID: string, stepID: string): string {
  return `${stepPath(taskID, stepID)}/execute`;
}

/** 开发—验证闭环路径 */
export function stepExecuteAndValidatePath(taskID: string, stepID: string): string {
  return `${stepPath(taskID, stepID)}/execute-and-validate`;
}

/** 显式开启修复周期路径 */
export function stepOpenRepairCyclePath(taskID: string, stepID: string): string {
  return `${stepPath(taskID, stepID)}/open-repair-cycle`;
}

/** 尝试集合路径 */
export function stepAttemptsPath(taskID: string, stepID: string): string {
  return `${stepPath(taskID, stepID)}/attempts`;
}

/** 单个尝试路径 */
export function attemptPath(taskID: string, stepID: string, attemptID: string): string {
  return `${stepAttemptsPath(taskID, stepID)}/${requireSegment(attemptID, "尝试标识")}`;
}

/** 确定性验证路径 */
export function attemptValidatePath(taskID: string, stepID: string, attemptID: string): string {
  return `${attemptPath(taskID, stepID, attemptID)}/validate`;
}

/** 代码初始化路径 */
export function attemptInitializeCodePath(
  taskID: string,
  stepID: string,
  attemptID: string,
): string {
  return `${attemptPath(taskID, stepID, attemptID)}/initialize-code`;
}

/** 尝试下会话集合路径 */
export function attemptSessionsPath(taskID: string, stepID: string, attemptID: string): string {
  return `${attemptPath(taskID, stepID, attemptID)}/sessions`;
}

/** 步骤验证历史路径 */
export function stepValidationsPath(taskID: string, stepID: string): string {
  return `${stepPath(taskID, stepID)}/validations`;
}

/** 单次验证失败反馈路径 */
export function validationFeedbackPath(taskID: string, stepID: string, runID: string): string {
  return `${stepValidationsPath(taskID, stepID)}/${requireSegment(runID, "验证记录标识")}/feedback`;
}

/** 分页查询参数 */
export type ExecutionPageQuery = {
  current?: number;
  size?: number;
  status?: string[];
};

/** 把分页与状态筛选拼到路径上；空值不参与拼接 */
function withPageQuery(path: string, query: ExecutionPageQuery): string {
  const params = new URLSearchParams();
  if (query.current !== undefined) params.set("current", String(query.current));
  if (query.size !== undefined) params.set("size", String(query.size));
  for (const status of query.status ?? []) {
    params.append("status", status);
  }
  const encoded = params.toString();
  return encoded ? `${path}?${encoded}` : path;
}

/** 查询任务的步骤列表；任务不存在时返回 404，而不是空列表 */
export function fetchTaskSteps(
  taskID: string,
  query: ExecutionPageQuery = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<StepListResponse>(withPageQuery(taskStepsPath(taskID), query), options);
}

/** 查询某步骤下的执行尝试列表 */
export function fetchStepAttempts(
  taskID: string,
  stepID: string,
  query: ExecutionPageQuery = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<AttemptListResponse>(
    withPageQuery(stepAttemptsPath(taskID, stepID), query),
    options,
  );
}

/** 查询某次尝试下的会话列表 */
export function fetchAttemptSessions(
  taskID: string,
  stepID: string,
  attemptID: string,
  query: ExecutionPageQuery = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<SessionListResponse>(
    withPageQuery(attemptSessionsPath(taskID, stepID, attemptID), query),
    options,
  );
}

/** 查询某步骤的验证历史；`records` 按创建时间升序，最后一条是最新验证 */
export function fetchStepValidations(
  taskID: string,
  stepID: string,
  options: ApiRequestOptions = {},
) {
  return apiRequest<ValidationHistoryResponse>(stepValidationsPath(taskID, stepID), options);
}

/**
 * 查询某次验证的失败反馈。
 *
 * 没有反馈时服务端返回 404，调用方必须把它当作"该轮验证没有失败反馈"，
 * 不能当作空反馈或验证通过。
 */
export function fetchValidationFeedback(
  taskID: string,
  stepID: string,
  runID: string,
  options: ApiRequestOptions = {},
) {
  return apiRequest<ValidationFailureFeedback>(
    validationFeedbackPath(taskID, stepID, runID),
    options,
  );
}

/** POST 请求的公共选项：JSON 请求体 + 不设客户端超时 */
function postOptions(body: unknown, options: ApiRequestOptions): ApiRequestOptions {
  return {
    ...options,
    body: JSON.stringify(body ?? {}),
    headers: { "Content-Type": "application/json", ...options.headers },
    method: "POST",
    timeoutMs: options.timeoutMs ?? NO_CLIENT_TIMEOUT,
  };
}

/**
 * 生成计划。
 *
 * 该操作会真实调用 Planner，并且只有服务端把计划写入数据库后 `persisted` 才为 true；
 * 调用方不得仅凭 HTTP 成功就认为计划已就绪。它不创建容器，因此要求任务已有受管理
 * 容器与规划角色。
 */
export function generatePlan(
  taskID: string,
  body: PlanRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<PlanResponse>(taskPlanPath(taskID), postOptions(body, options));
}

/** 执行单个步骤；同步返回，只有 Attempt 进入终态后才返回 */
export function executeStep(
  taskID: string,
  stepID: string,
  body: ExecuteStepRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<ExecuteStepResponse>(
    stepExecutePath(taskID, stepID),
    postOptions(body, options),
  );
}

/** 按计划顺序依次执行开发步骤；步骤顺序完全由服务端决定 */
export function executeSteps(
  taskID: string,
  body: ExecuteStepsRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<ExecuteStepsResponse>(taskExecuteStepsPath(taskID), postOptions(body, options));
}

/** 单步骤开发—验证闭环；只有真实验证通过时 `verified` 才为 true */
export function executeAndValidateStep(
  taskID: string,
  stepID: string,
  body: ExecuteAndValidateStepRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<ExecuteAndValidateStepResponse>(
    stepExecuteAndValidatePath(taskID, stepID),
    postOptions(body, options),
  );
}

/** 对指定尝试发起确定性验证 */
export function validateAttempt(
  taskID: string,
  stepID: string,
  attemptID: string,
  body: ValidateAttemptRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<ValidateAttemptResponse>(
    attemptValidatePath(taskID, stepID, attemptID),
    postOptions(body, options),
  );
}

/** 初始化指定尝试的容器内代码工作区 */
export function initializeAttemptCode(
  taskID: string,
  stepID: string,
  attemptID: string,
  body: InitializeCodeRequest = {},
  options: ApiRequestOptions = {},
) {
  return apiRequest<InitializeCodeResponse>(
    attemptInitializeCodePath(taskID, stepID, attemptID),
    postOptions(body, options),
  );
}

/**
 * 显式开启一个新的执行周期（修复周期）。
 *
 * 服务端只在上一周期已收敛且预算用尽或策略不再允许修复时才允许；拒绝原因通过
 * `ApiError.message` 返回。该操作只登记预算作用域，不启动任何 Agent 调用。
 */
export function openRepairCycle(taskID: string, stepID: string, options: ApiRequestOptions = {}) {
  return apiRequest<OpenRepairCycleResponse>(
    stepOpenRepairCyclePath(taskID, stepID),
    postOptions({}, options),
  );
}
