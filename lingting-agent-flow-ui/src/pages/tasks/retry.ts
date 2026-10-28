/**
 * 显式重试的展示元数据、竞态判定与结果解释。
 *
 * 本模块是纯函数与纯数据，不发起请求、不读写浏览器状态，因此刷新失败、任务切换与
 * "迟到响应"等行为可以被直接断言。
 *
 * 三条硬约束：
 *   1. 重试资格只来自服务端 `can_retry`：这里不出现任务状态白名单，也不从
 *      `status_terminal` / `status_finished` / 状态名称推断"能不能重试"；
 *   2. 请求成功、任务已重开、工作流已启动与最终业务成功是四件不同的事，结论文案逐项
 *      依据响应字段，绝不由其中一个推断另一个；
 *   3. 未知阻塞分类、未知任务状态与缺省可选字段都原样保留，并用中性文案说明。
 */
import type { RetryEligibilityResponse, RetryTaskResponse } from "@/lib/api";
import { workflowRunStatusMeta } from "@/pages/tasks/status";

/** 一条阻塞分类的展示结论 */
export type RetryBlockingMeta = {
  /** 中文标签；未知分类时为服务端原始代码 */
  label: string;
  /** 该阻塞项的确切含义 */
  description: string;
  /** 是否为前端已知分类；false 时不得附加任何业务解释 */
  known: boolean;
};

/**
 * 已知阻塞分类的解释。
 *
 * 取值来源：`internal/domain/task_retry.go` 的 `TaskRetryRejection` 常量。文案只解释
 * "服务端为什么拒绝"，不复述判定规则，也不给前端可自行执行的修复动作。
 */
export type RetryBlockingCode =
  | "task_deleted"
  | "cancel_requested"
  | "status_not_retryable"
  | "lease_held"
  | "active_workflow_run"
  | "step_occupied"
  | "recovery_residue"
  | "external_side_effect_pending"
  | "prerequisite_missing"
  | "nothing_to_retry";

export const RETRY_BLOCKING_META: Record<RetryBlockingCode, RetryBlockingMeta> = {
  task_deleted: {
    description: "任务已被逻辑删除，不能再次开启执行周期。",
    known: true,
    label: "任务已删除",
  },
  cancel_requested: {
    description: "任务已记录取消请求，取消意图优先于重试；请先由服务端确认取消结果。",
    known: true,
    label: "任务已请求取消",
  },
  status_not_retryable: {
    description: "任务当前状态不属于允许显式重试的集合，当前只开放 failed。",
    known: true,
    label: "任务状态不允许重试",
  },
  lease_held: {
    description: "任务执行权仍被其他执行者有效持有；此时重试会出现两个执行者。",
    known: true,
    label: "任务执行权未释放",
  },
  active_workflow_run: {
    description: "任务仍有未结束的工作流执行记录，无法证明旧执行者已经停止。",
    known: true,
    label: "仍有未结束的执行记录",
  },
  step_occupied: {
    description: "仍有步骤被一次执行占用，旧执行者可能仍在推进。",
    known: true,
    label: "仍有步骤在执行",
  },
  recovery_residue: {
    description: "存在结果不确定、必须先人工核验的步骤，不能在未知现场上重放执行。",
    known: true,
    label: "存在待核验的步骤",
  },
  external_side_effect_pending: {
    description: "仍有未核验的外部副作用（例如代码保存的提交、推送与合并），禁止自动重放。",
    known: true,
    label: "仍有未核验的外部副作用",
  },
  prerequisite_missing: {
    description: "启动所需的某项前置条件未满足，重试会在启动阶段失败。",
    known: true,
    label: "启动前置条件未满足",
  },
  nothing_to_retry: {
    description: "计划中已经没有需要重新执行的步骤，重试不会产生任何新的执行。",
    known: true,
    label: "没有需要重试的步骤",
  },
};

/** 读取阻塞分类的展示元数据；未知分类原样展示，不猜测语义 */
export function retryBlockingMeta(code: string): RetryBlockingMeta {
  const known = RETRY_BLOCKING_META[code as RetryBlockingCode];
  return (
    known ?? {
      description: "服务端返回了当前版本未定义的阻塞分类，请按原始代码排查。",
      known: false,
      label: code,
    }
  );
}

/** 服务端阻塞列表；`null` 与缺失都按"没有给出阻塞项"处理 */
export function retryBlockingList(response: RetryEligibilityResponse): string[] {
  return response.blocking ?? [];
}

/** 资格摘要缺省时的中性提示：不由前端编造结论 */
export const RETRY_SUMMARY_MISSING = "服务端未提供重试资格摘要";

/** 读取服务端摘要；为空时返回中性提示 */
export function retrySummary(response: RetryEligibilityResponse): string {
  const summary = response.summary?.trim();
  if (summary) return summary;
  return RETRY_SUMMARY_MISSING;
}

/** 资格结论的展示结论 */
export type RetryVerdict = {
  title: string;
  description: string;
  color: "success" | "warning";
  attention: boolean;
};

/**
 * 把服务端 `can_retry` 翻译为结论文案。
 *
 * 只依据服务端的布尔值：允许时也只能表述为"查询时刻允许重试"，因为重试接口会重新判定
 * 资格与执行权；不允许时明确说明存在阻塞。
 */
export function retryVerdict(canRetry: boolean): RetryVerdict {
  if (canRetry) {
    return {
      attention: false,
      color: "success",
      description:
        "这是查询时刻的结论：服务端在真正执行重试前仍会重新判定资格与任务执行权。允许重试不代表任务会成功。",
      title: "当前查询结果允许重试",
    };
  }
  return {
    attention: true,
    color: "warning",
    description: "服务端判定当前存在阻塞，不能直接发起重试；请按下方阻塞项处理后重新查询资格。",
    title: "当前存在阻塞，不能重试",
  };
}

/** 重试资格从未查询、查询失败或已过期时，操作入口的固定禁用说明 */
export const RETRY_BLOCKED_WITHOUT_ELIGIBILITY = "尚未取得允许重试的服务端结论，不能发起重试";

/**
 * 判断当前是否允许提交重试。
 *
 * 唯一依据是"最近一次成功取得的资格快照"里的 `can_retry`：快照缺省（从未查询成功）、
 * 请求在途或不属于当前任务时一律不允许。这里不读取任务状态，因此不存在第二套状态白名单。
 */
export function canSubmitRetry(options: {
  /** 最近一次成功取得的资格快照；查询失败时为 undefined */
  eligibility?: RetryEligibilityResponse;
  /** 该快照归属的任务标识 */
  eligibilityTaskID?: string;
  /** 当前打开的任务标识 */
  taskID?: string;
  /** 是否有重试请求在途 */
  running: boolean;
}): boolean {
  const { eligibility, eligibilityTaskID, taskID, running } = options;
  if (running) return false;
  if (!eligibility) return false;
  if (!taskID || eligibilityTaskID !== taskID) return false;
  return eligibility.can_retry;
}

/**
 * 资格请求的票据：序号 + 任务标识。
 *
 * 序号识别"同一任务的多次刷新"（只有最后一次生效），任务标识识别"切换任务后的迟到响应"
 * （旧任务的结果不能覆盖新任务的结论）。
 */
export type RetryTicket = { requestID: number; taskID: string };

/** 新建票据计数器 */
export function createRetryGate(): { requestID: number } {
  return { requestID: 0 };
}

/** 发起一次资格请求：序号递增并返回本次票据 */
export function beginRetryRequest(gate: { requestID: number }, taskID: string): RetryTicket {
  gate.requestID += 1;
  return { requestID: gate.requestID, taskID };
}

/** 判断一次资格响应是否仍然有效：未被取消、仍是最新一次请求、且归属当前任务 */
export function acceptRetryResponse(
  gate: { requestID: number },
  ticket: RetryTicket,
  activeTaskID: string | undefined,
  aborted: boolean,
): boolean {
  if (aborted) return false;
  if (gate.requestID !== ticket.requestID) return false;
  return (activeTaskID ?? "") === ticket.taskID;
}

/** 一次重试结果的分类：只有 workflow_started 为真才算工作流真的启动 */
export type RetryOutcomeKind = "started" | "not_started";

/** 重试结果的展示结论 */
export type RetryOutcome = {
  kind: RetryOutcomeKind;
  title: string;
  /** 逐条事实，全部来自服务端响应字段 */
  details: string[];
};

/** 服务端返回的零值时间：Go 端 `time.Time{}` 序列化为该字符串，不是真实时刻 */
const ZERO_TIME = "0001-01-01T00:00:00Z";

/** 判断服务端时间字段是否为真实时刻（缺省、零值或无法解析都返回 false） */
export function isRealTime(value?: string): boolean {
  if (!value) return false;
  if (value === ZERO_TIME) return false;
  return !Number.isNaN(new Date(value).getTime());
}

/** 任务状态展示：已知取值用中文标签，未知取值原样返回 */
export function retryTaskStatusLabel(status: string, dict: Record<string, string>): string {
  return dict[status] ?? status;
}

/**
 * 把重试响应翻译为用户可读的结论。
 *
 * 规则固定：
 *   - 请求返回成功只说明"服务端完成了本次操作"，不等于任务成功；
 *   - `workflow_started=true` 才是"工作流真的启动了"，此时展示真实任务状态、执行记录与
 *     执行代次；
 *   - 否则一律按"任务已重开但工作流未启动"展示真实任务状态与失败阶段 / 原因，绝不显示为
 *     "重试已开始"；
 *   - 缺失的可选字段用中性说明，未知状态值原样保留，不由其他字段推断成功。
 */
export function describeRetryResult(
  result: RetryTaskResponse,
  options: {
    /** 状态字典：已知取值翻译为中文，未知取值原样展示 */
    statusLabels: Record<string, string>;
    /** 阶段标签函数：把 `failure_stage` 翻译为中文 */
    stageLabel: (stage: string) => string;
  },
): RetryOutcome {
  const status = retryTaskStatusLabel(String(result.task_status ?? ""), options.statusLabels);
  const previous = retryTaskStatusLabel(String(result.previous_status ?? ""), options.statusLabels);
  const details = [
    `任务状态：重试前 ${previous} → 当前 ${status}`,
    `执行代次：${result.lease_epoch}`,
  ];
  if (isRealTime(result.started_at)) {
    details.push(`开始时间：${new Date(result.started_at as string).toLocaleString()}`);
  }

  if (result.workflow_started) {
    const run = result.run;
    const runStatus = run ? workflowRunStatusMeta(run.status).label : "";
    details.push(
      run
        ? `执行记录：${run.run_id}（${runStatus}，已完成 ${run.completed_steps} / ${run.total_steps} 步）`
        : "执行记录：服务端未返回本次启动的执行记录",
    );
    return {
      details,
      kind: "started",
      title: "重试已重开任务并启动工作流",
    };
  }

  const stage = result.failure_stage
    ? options.stageLabel(result.failure_stage)
    : "服务端未提供失败阶段";
  const failure = result.failure?.trim() || "服务端未提供失败原因";
  details.push(`失败阶段：${stage}`, `失败原因：${failure}`);
  return {
    details,
    kind: "not_started",
    title: "任务已重开，但工作流没有启动",
  };
}

/**
 * 请求结果不确定时的固定说明。
 *
 * 客户端超时、连接中断或主动取消都意味着"服务端可能已经完成本次有副作用的操作"，
 * 因此必须提示无法确认，并明确不自动重复提交。
 */
export const RETRY_UNKNOWN_OUTCOME_HINT =
  "无法确认服务端是否已完成本次重试操作。请手动刷新任务详情与重试资格后再决定是否重新发起；页面不会自动重复提交。";

/** 判断错误是否属于"结果不确定"（超时、网络中断或调用方取消） */
export function isUncertainRetryError(kind: string): boolean {
  return kind === "timeout" || kind === "network" || kind === "aborted";
}
