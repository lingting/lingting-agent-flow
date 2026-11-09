/**
 * 需求页的停止与删除判定（纯函数）。
 *
 * 约束：
 * - 状态集合与服务端一致：允许随需求一起软删除的任务状态是
 *   `queued` / `completed` / `failed` / `cancelled`，其余状态（含 `cancelling`、
 *   `paused`、`recovery_required` 与任何未知取值）一律拒绝；
 * - 停止只作用于仍需执行者收敛的任务（与服务端 `TaskStatus.IsActive` 一致），
 *   `queued` 任务不需要为了删除先被伪装成 `cancelled`；
 * - 判定只用于按钮可用性说明，真正的准入由服务端在事务内重新核验，前端禁用不承担并发安全。
 */
import { ApiError, type ConfirmedTask } from "@/lib/api";

/** 需求聚合删除允许的任务状态，与 `domain.RequirementDeletionTaskStatuses` 一致 */
const DELETABLE_TASK_STATUSES = ["queued", "completed", "failed", "cancelled"];

/**
 * 仍需执行者推进或收敛的任务状态，与 `domain.TaskStatus.IsActive` 一致。
 *
 * `cancelling` 属于该集合，但它表示"停止请求已记录、等待真实收敛"，因此不再提供停止按钮。
 */
const ACTIVE_TASK_STATUSES = [
  "preparing",
  "planning",
  "planned",
  "developing",
  "verifying",
  "saving",
  "retrying",
  "cancelling",
];

/** 停止请求已记录、等待真实收敛的状态 */
const STOPPING_TASK_STATUS = "cancelling";

/** 需要人工核验、不能直接删除的状态 */
const MANUAL_TASK_STATUSES = ["paused", "recovery_required"];

/** 判断任务状态是否允许随需求一起逻辑删除 */
export function isTaskDeletableWithRequirement(status: string): boolean {
  return DELETABLE_TASK_STATUSES.includes(status);
}

/** 判断任务是否仍需执行者推进（可以请求停止） */
export function isTaskStoppable(status: string): boolean {
  return ACTIVE_TASK_STATUSES.includes(status) && status !== STOPPING_TASK_STATUS;
}

/** 判断任务是否处于"停止中"：请求已记录，但还没有真实收敛 */
export function isTaskStopping(status: string): boolean {
  return status === STOPPING_TASK_STATUS;
}

/** 删除可用性：不可用时必须给出原因，页面不保留"一定失败"的可点击入口 */
export type RequirementDeletionAvailability = {
  available: boolean;
  reason?: string;
};

/** 返回当前需求下仍需停止的任务；`cancelling` 与已结束任务都不在其中 */
export function stoppableTasks(tasks: ConfirmedTask[]): ConfirmedTask[] {
  return tasks.filter((task) => isTaskStoppable(task.status));
}

/**
 * 判定删除操作是否可用。
 *
 * 任务存在时按"第一个阻止删除的任务"给出原因：执行中、停止中、待核验与未知状态的处置
 * 方式不同，不能合并成同一句"有任务未结束"。
 */
export function describeRequirementDeletionAvailability(
  tasks: ConfirmedTask[],
): RequirementDeletionAvailability {
  for (const task of tasks) {
    if (isTaskDeletableWithRequirement(task.status)) continue;
    return { available: false, reason: describeBlockingTask(task) };
  }
  return { available: true };
}

/** 描述单个任务为什么阻止删除 */
function describeBlockingTask(task: ConfirmedTask): string {
  const label = `任务 ${task.id}`;
  if (isTaskStopping(task.status)) {
    return `${label} 正在停止（cancelling），需要等待执行者确认真实收敛后才能删除需求。`;
  }
  if (ACTIVE_TASK_STATUSES.includes(task.status)) {
    return `${label} 仍在执行（${task.status}），请先停止任务并等待其真实收敛。`;
  }
  if (MANUAL_TASK_STATUSES.includes(task.status)) {
    return `${label} 处于 ${task.status}，需要人工核验现场后才能删除需求。`;
  }
  return `${label} 的状态是 ${task.status}，服务端未确认该状态可以随需求删除。`;
}

/** 删除前的任务状态摘要：按需求内顺序列出序号、标识与真实状态 */
export function describeRequirementTasksForDeletion(tasks: ConfirmedTask[]): string[] {
  return tasks.map((task, index) => {
    const order = task.requirement_sequence ?? index + 1;
    return `${order}. ${task.id}（${task.status}）`;
  });
}

/** 把需求停止 / 删除的错误转成可读说明，区分结果未知与业务拒绝 */
export function describeRequirementActionError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") {
      return "无法连接 AgentFlow 服务，本次操作的结果未知，请重新读取需求状态确认。";
    }
    if (error.kind === "timeout") {
      return "请求超时，服务端可能仍在处理，请重新读取需求状态确认。";
    }
    if (error.kind === "aborted") return "请求已取消。";
    if (error.status === 400) return `请求参数不合法：${error.message}`;
    if (error.status === 404) return "该需求不存在或已被删除，请刷新需求列表。";
    if (error.status === 409) return `操作被拒绝：${error.message}`;
    if (error.status === 503) return "需求删除能力不可用，请检查服务端装配后重试。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 判断错误是否意味着"结果未知"：连接中断或超时都不代表服务端没有执行 */
export function isUnknownRequirementActionOutcome(error: unknown): boolean {
  return error instanceof ApiError && (error.kind === "network" || error.kind === "timeout");
}
