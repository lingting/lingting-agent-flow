/**
 * 修复周期（执行周期）开启资格的纯函数部分。
 *
 * 这里回答的唯一问题是"当前这一步能不能提交「开启新执行周期」"，判定依据**只有**
 * 服务端 `GET .../repair-cycle-eligibility` 的结论：预算是否用尽、上一周期是否已
 * 收敛、历史尝试的周期归属是否可解释都由服务端依据持久化记录计算，前端不再自行
 * 复制这些领域规则。
 *
 * 四条不可动摇的约束：
 *   1. 只有最新、且归属于当前任务与当前步骤的资格明确返回 `can_open_cycle=true`
 *      时才放行；加载中、查询失败、快照已过期或无法确认一律不放行；
 *   2. 阻塞原因使用服务端 `summary` 与 `blocking`，未知类别原样保留，绝不映射成允许；
 *   3. 允许开启只代表"预算作用域可以登记"，不得表述为可以执行开发、验证已通过或
 *      任务即将完成；
 *   4. 本模块不发起请求，也不写任何状态。
 */
import type { RepairCycleEligibilityResponse } from "@/lib/api";
import type { ActionEligibility } from "@/pages/tasks/steps";

/** 一次资格查询的状态 */
export type RepairCycleEligibilityState =
  /** 缺少任务或步骤标识，未发起查询 */
  | { status: "idle" }
  /** 首次查询中，还没有任何结论 */
  | { status: "loading" }
  /**
   * 已取得服务端结论。
   *
   * `error` 非空表示后续刷新失败：展示的仍是上一次成功取得的快照，它可能已过期，
   * 因此不得当作当前最新的准入结论。
   *
   * `refreshing` 为真表示正在重新查询：旧快照只用于说明现场，同样不得放行。
   */
  | {
      status: "ready";
      data: RepairCycleEligibilityResponse;
      error?: string;
      refreshing?: boolean;
    }
  /** 查询失败且没有可用快照：资格未知，不得当作"可以开启" */
  | { status: "error"; error: string };

/** 资格快照的归属：只有归属当前任务与步骤的结论才能用于判定 */
export type RepairCycleEligibilityOwner = {
  taskID?: string;
  stepID?: string;
};

/** 已知阻塞类别的中文标签；未知类别必须原样保留，不能映射成允许 */
const BLOCKER_LABEL: Record<string, string> = {
  cancel_requested: "任务已请求取消",
  attempts_exhausted: "已达到尝试次数上限",
  cycle_closed: "上一执行周期已结束",
  cycle_ownership_unconfirmed: "执行周期记录归属不一致",
  cycle_unresolved: "历史执行的结果或周期归属无法确认",
  execution_active: "步骤仍被当前执行占用",
  new_cycle_denied: "当前不满足开启新周期的条件",
  prerequisite_incomplete: "前序步骤尚未完成",
  task_status_not_executable: "任务状态不允许推进步骤",
};

/** 阻塞类别标签：已知类别给中文说明，未知类别原样返回服务端取值 */
export function repairCycleBlockerLabel(blocker: string): string {
  const trimmed = blocker.trim();
  return BLOCKER_LABEL[trimmed] ?? trimmed;
}

/** 判断快照是否属于当前任务与步骤；不归属时必须重新查询，不能直接沿用 */
export function eligibilityBelongsTo(
  data: RepairCycleEligibilityResponse | undefined,
  owner: RepairCycleEligibilityOwner,
): boolean {
  if (!data) return false;
  const taskID = owner.taskID?.trim() ?? "";
  const stepID = owner.stepID?.trim() ?? "";
  return taskID !== "" && stepID !== "" && data.task_id === taskID && data.step_id === stepID;
}

/**
 * 「开启新执行周期」按钮的可用性与原因。
 *
 * `busy`（动作进行中）由调用方自行处理：它属于流程状态而不是准入失败，调用方不应
 * 把它写成本函数的业务原因。
 */
export function describeRepairCycleAction(
  state: RepairCycleEligibilityState,
  owner: RepairCycleEligibilityOwner,
): ActionEligibility {
  if (state.status === "idle") {
    return { allowed: false, reason: "尚未选择步骤，无法确认开启新执行周期的资格。" };
  }
  if (state.status === "loading") {
    return {
      allowed: false,
      reason: "正在向服务端查询开启新执行周期的资格，确认之前不能提交。",
    };
  }
  if (state.status === "error") {
    return {
      allowed: false,
      reason:
        `无法确认开启新执行周期的资格（${state.error}）。` + "资格未知时不会放行，请刷新后重试。",
    };
  }
  if (!eligibilityBelongsTo(state.data, owner)) {
    return {
      allowed: false,
      reason: "最后一次资格查询不属于当前任务或当前步骤，需要重新查询后才能提交。",
    };
  }
  if (state.error) {
    return {
      allowed: false,
      reason:
        `资格刷新失败（${state.error}），下面展示的是上一次查询结果，可能已经过期。` +
        "请刷新确认最新资格后再提交。",
    };
  }
  if (state.refreshing) {
    // 刷新中的旧快照只用于说明现场：它描述的是上一次查询，不能当作最新准入结论。
    return {
      allowed: false,
      reason: "正在重新确认开启新执行周期的资格，确认之前不能提交。",
    };
  }
  if (!state.data.can_open_cycle) {
    const labels = state.data.blocking.map(repairCycleBlockerLabel);
    const suffix = labels.length > 0 ? `（阻塞：${labels.join("、")}）` : "";
    const summary = state.data.summary.trim();
    return {
      allowed: false,
      reason: summary !== "" ? `${summary}${suffix}` : `服务端未允许开启新的执行周期${suffix}`,
    };
  }
  return { allowed: true, reason: "" };
}

/**
 * 资格快照的可读事实，用于在按钮下方直接展示（不依赖悬停）。
 *
 * 只翻译服务端给出的字段，不推断业务结论：没有执行记录时说明"首轮即第一个周期"；
 * 允许开启时明确写出"只登记预算，不会执行开发"。
 */
export function describeRepairCycleFacts(
  state: RepairCycleEligibilityState,
  owner: RepairCycleEligibilityOwner,
): string[] {
  if (state.status === "loading") return ["正在查询服务端资格…"];
  if (state.status === "error") return [`资格未知：${state.error}`];
  if (state.status !== "ready" || !eligibilityBelongsTo(state.data, owner)) return [];
  const data = state.data;
  const facts: string[] = [];
  // 服务端说明永远排在最前：它解释了"为什么允许 / 为什么阻塞"，是页面上可读的
  // 主依据，不依赖悬停提示。
  const summary = data.summary.trim();
  if (summary !== "") facts.push(summary);
  if (data.can_open_cycle) {
    facts.push("该动作只登记预算，不会执行开发；随后仍需显式发起执行与验证。");
  } else {
    const labels = data.blocking.map(repairCycleBlockerLabel);
    if (labels.length > 0) facts.push(`服务端阻塞类别：${labels.join("、")}`);
  }
  if (data.attempts > 0) {
    const cycleText = data.active_cycle ? `活动周期 #${data.cycle_sequence}` : "上一周期已结束";
    facts.push(
      `${cycleText}：已用 ${data.consumed_attempts} / ${data.max_attempts} 次，` +
        `剩余 ${data.remaining_attempts} 次；历史尝试共 ${data.attempts} 次。`,
    );
  } else {
    facts.push("该步骤还没有任何执行记录：首轮执行本身就是第一个执行周期。");
  }
  if (state.error) {
    facts.push(`资格刷新失败（${state.error}），以上为上一次查询结果，可能已经过期。`);
  }
  if (state.refreshing) {
    facts.push("正在重新查询服务端资格，以上为上一次查询结果。");
  }
  return facts;
}
