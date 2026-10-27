/**
 * 恢复工作流操作的判定、竞态票据与结果解释。
 *
 * 本模块是纯函数与纯数据，不发起请求、不读写浏览器状态，因此"资格是否允许发起恢复"、
 * "迟到响应是否被采用"与"请求结果如何解释"都可以被直接断言，不依赖浏览器环境。
 *
 * 四条硬约束：
 *   1. 恢复入口的可用性只来自服务端 `resume-eligibility` 的 `can_resume`：这里不出现任务
 *      状态或工作流状态白名单，也不从 `status_terminal` / `closeout` / `code_save_action`
 *      推断"能不能恢复"；
 *   2. 资格快照是查询时刻的结论，不是执行授权：服务端在真正恢复时仍会重新核验租约、容器、
 *      执行节点与代码保存副作用，因此前端一律先调用恢复接口再以真实响应为准；
 *   3. 恢复请求成功、工作流已受理与工作流最终完成是三件不同的事，结论文案逐项依据响应字段，
 *      绝不把 202 读成"任务已完成"；
 *   4. 未知状态值、缺省执行记录与缺省时间都原样保留或用中性文案说明，不补造业务结论。
 */
import type { ResumeEligibilityResponse, WorkflowRun } from "@/lib/api";

/** 恢复操作被禁用且没有任何服务端资格快照时的固定说明 */
export const RESUME_BLOCKED_WITHOUT_ELIGIBILITY =
  "尚未取得允许恢复的服务端资格结论，不能发起恢复工作流";

/**
 * 恢复入口的禁用原因。
 *
 * 只区分展示文案，不改变任何业务判定：是否允许恢复永远由 `can_resume` 决定。
 */
export type ResumeSubmitBlockReason =
  /** 允许提交，没有禁用原因 */
  | ""
  /** 资格请求在途 */
  | "eligibility_loading"
  /** 资格查询失败且没有可用快照 */
  | "eligibility_error"
  /** 资格快照不属于当前任务 */
  | "stale_snapshot"
  /** 恢复请求在途 */
  | "running"
  /** 资格快照不可用（从未成功查询） */
  | "no_eligibility"
  /** 服务端判定存在阻塞 */
  | "blocked";

/** 一次恢复请求的票据：序号 + 任务标识 */
export type ResumeTicket = { requestID: number; taskID: string };

/** 新建票据计数器 */
export function createResumeGate(): { requestID: number } {
  return { requestID: 0 };
}

/** 发起一次恢复请求：序号递增并返回本次票据 */
export function beginResumeRequest(gate: { requestID: number }, taskID: string): ResumeTicket {
  gate.requestID += 1;
  return { requestID: gate.requestID, taskID };
}

/**
 * 判断一次恢复响应是否仍然有效。
 *
 * 三个条件都必须成立：请求没有被取消、它仍是最后一次发起的请求、它描述的任务仍是当前
 * 打开的任务。任一不成立都说明响应已经过期，不能用来更新当前界面。
 */
export function acceptResumeResponse(
  gate: { requestID: number },
  ticket: ResumeTicket,
  activeTaskID: string | undefined,
  aborted: boolean,
): boolean {
  if (aborted) return false;
  if (gate.requestID !== ticket.requestID) return false;
  return (activeTaskID ?? "") === ticket.taskID;
}

/** 判断恢复入口当前是否阻塞，并给出禁用原因 */
export type ResumeSubmitInput = {
  /** 资格快照：只有在查询成功时才存在 */
  eligibility?: ResumeEligibilityResponse;
  /** 该快照归属的任务标识 */
  eligibilityTaskID?: string;
  /** 资格加载状态：`loading` / `error` / `ready` / `idle` */
  eligibilityStatus: "idle" | "loading" | "error" | "ready";
  /**
   * 最近一次刷新失败的错误说明。
   *
   * 刷新失败时快照已过期：它可以继续展示给用户参考，但不能作为当前恢复操作的授权依据，
   * 因此这里一旦非空就禁用恢复入口。
   */
  eligibilityRefreshError?: string;
  /** 当前打开的任务标识 */
  taskID?: string;
  /** 是否有恢复请求在途 */
  running: boolean;
};

/**
 * 读取恢复入口的禁用原因（空字符串表示允许提交）。
 *
 * 判定顺序固定：先排除"资格不可用"，再看服务端结论。这样"查询中""查询失败""刷新失败"
 * "快照过期"都会先于 `can_resume` 生效，避免把上一任务或已过期的允许结论当成当前授权。
 */
export function resumeSubmitBlockReason(input: ResumeSubmitInput): ResumeSubmitBlockReason {
  const {
    eligibility,
    eligibilityRefreshError,
    eligibilityStatus,
    eligibilityTaskID,
    running,
    taskID,
  } = input;
  if (running) return "running";
  if (eligibilityStatus === "loading") return "eligibility_loading";
  if (eligibilityStatus === "error") return "eligibility_error";
  if (!eligibility) return "no_eligibility";
  if (!taskID || eligibilityTaskID !== taskID) return "stale_snapshot";
  // 刷新失败意味着手里只有过期快照：它仍可展示，但不是当前可恢复的授权依据。
  if (eligibilityRefreshError) return "stale_snapshot";
  return eligibility.can_resume ? "" : "blocked";
}

/** 恢复入口是否可用：仅当服务端 `can_resume` 为真且没有其他阻塞原因 */
export function canSubmitResume(input: ResumeSubmitInput): boolean {
  return resumeSubmitBlockReason(input) === "";
}

/** 禁用原因对应的面向用户说明 */
export const RESUME_SUBMIT_BLOCK_TEXT: Record<Exclude<ResumeSubmitBlockReason, "">, string> = {
  blocked: "服务端判定当前存在阻塞，处理后请刷新续跑资格再尝试恢复。",
  eligibility_error: "续跑资格当前无法确认，不能发起恢复。",
  eligibility_loading: "续跑资格正在查询中，请等待结论返回。",
  no_eligibility: RESUME_BLOCKED_WITHOUT_ELIGIBILITY,
  running: "恢复请求已发出，正在等待服务端的真实结果。",
  stale_snapshot: "当前展示的资格结论已过期或不属于该任务，请重新查询续跑资格。",
};

/** 读取禁用原因对应的说明；允许提交时返回空字符串 */
export function resumeSubmitBlockText(input: ResumeSubmitInput): string {
  const reason = resumeSubmitBlockReason(input);
  return reason === "" ? "" : RESUME_SUBMIT_BLOCK_TEXT[reason];
}

/**
 * 恢复请求结果不确定时的固定说明。
 *
 * 客户端超时、网络中断与调用方取消都意味着"服务端可能已经受理并推进了工作流"，因此必须
 * 明确提示无法确认，要求先重新查询工作流状态与续跑资格，并且不自动重复提交。
 */
export const RESUME_UNKNOWN_OUTCOME_HINT =
  "无法确认服务端是否已受理本次恢复。请先重新查询工作流状态与续跑资格，再决定是否重新发起；页面不会自动重复提交。";

/** 判断错误是否属于"结果不确定"（超时、网络中断或调用方取消） */
export function isUncertainResumeError(kind: string): boolean {
  return kind === "timeout" || kind === "network" || kind === "aborted";
}

/** 恢复请求的下一步提示：恢复请求结果不确定时先观察，不允许自动重发 */
export type ResumeOutcome = {
  /** 结论标题 */
  title: string;
  /** 结论说明：只描述本次请求与工作流当前状态 */
  description: string;
  /** 逐条事实，全部来自服务端响应字段 */
  details: string[];
  /** 是否需要用户关注（被服务端拒绝或结果不确定） */
  attention: boolean;
};

/** 服务端返回的零值时间：Go 端 `time.Time{}` 序列化为该字符串，不是真实时刻 */
const ZERO_TIME = "0001-01-01T00:00:00Z";

/** 判断服务端时间字段是否为真实时刻（缺省、零值或无法解析都返回 false） */
export function isRealTime(value?: string): boolean {
  if (!value) return false;
  if (value === ZERO_TIME) return false;
  return !Number.isNaN(new Date(value).getTime());
}

/** 时间展示：使用本地时区，缺省或非法值返回中性占位符 */
export function formatResumeTime(value?: string): string {
  if (!isRealTime(value)) return "服务端未记录";
  return new Date(value as string).toLocaleString();
}

/** 工作流执行状态展示：已知取值用中文标签，未知取值原样返回 */
export function resumeRunStatusLabel(status: string, dict: Record<string, string>): string {
  return dict[status] ?? status;
}

/**
 * 把恢复接口的响应用户可读化。
 *
 * 只陈述三件可从响应直接读到的事实：本次恢复请求已被受理、执行记录的当前状态、当前进度。
 * 恢复请求被受理不等于工作流会成功，也不等于任务已完成；任务是否成功仍以最终业务结论与
 * 收尾闭环为准，因此文案里明确写出这一点。
 */
export function describeResumeResult(
  result: WorkflowRun,
  options: {
    /** 工作流状态字典：已知取值翻译为中文，未知取值原样展示 */
    statusLabels: Record<string, string>;
  },
): ResumeOutcome {
  const status = resumeRunStatusLabel(String(result.status ?? ""), options.statusLabels);
  const details = [
    `执行记录：${result.run_id}`,
    `执行状态：${status}`,
    `进度：已完成 ${result.completed_steps} / ${result.total_steps} 步`,
  ];
  if (isRealTime(result.started_at)) {
    details.push(`开始时间：${formatResumeTime(result.started_at)}`);
  }

  if (result.status === "running") {
    return {
      attention: false,
      description:
        "服务端已受理本次恢复并把执行记录推进到执行中。恢复请求被受理不代表工作流会成功，也不代表任务已完成；请以工作流进度、最终业务结论与收尾闭环为准。",
      details,
      title: "恢复请求已受理，工作流已开始推进",
    };
  }

  if (result.status === "pending") {
    return {
      attention: true,
      description:
        "服务端已受理本次恢复，但执行记录仍处于等待启动。这不表示工作流已推进，请刷新工作流进度确认。",
      details,
      title: "恢复请求已受理，执行记录尚未开始推进",
    };
  }

  return {
    attention: true,
    description:
      "服务端已受理本次恢复，但执行记录没有回到执行中。请按真实执行状态与续跑资格判断下一步，页面不会自动重发。",
    details,
    title: "恢复请求已受理，但执行记录未进入执行中",
  };
}

/** 恢复请求本身失败时的中性结论，用于与"结果不确定"区分 */
export const RESUME_FAILED_TITLE = "恢复请求被服务端拒绝";

/** 恢复请求结果不确定时的结论标题 */
export const RESUME_UNCERTAIN_TITLE = "恢复请求结果无法确认";
