/**
 * 事件时间线与修复周期历史的展示计算。
 *
 * 与 React 无关：把服务端返回的历史记录变换成"页面上应该显示什么"，因此文案、
 * 占位符、摘要安全性和分页合并都可以被直接断言，而不依赖浏览器。
 *
 * 五条硬约束：
 *   1. 未知事件类型必须原样显示类型名，不能被统一显示成"未知事件"；
 *   2. 没有安全摘要时显示中性占位符，绝不把原始 payload 当作摘要展示；
 *   3. `closed_at` 缺失只表示"尚未记录结束时间"，不推断成功、失败或仍在执行；
 *   4. 时间一律按本地时区格式化，缺失或非法时显示同一个中性占位符；
 *   5. 不根据任何历史记录推断当前任务或步骤状态。
 */
import type { RepairCycle, TaskEvent } from "@/lib/api";

/** 历史展示统一使用的中性占位符 */
export const HISTORY_PLACEHOLDER = "—";

/** 没有可展示摘要时的说明文本 */
export const EVENT_SUMMARY_PLACEHOLDER = "无可展示的摘要";

/** 修复周期尚未记录结束时间时的中性文案 */
export const REPAIR_CYCLE_OPEN_TEXT = "尚未记录结束时间";

/** 修复周期为空时的说明文本 */
export const REPAIR_CYCLE_EMPTY_TEXT = "暂无修复周期记录";

/**
 * 已知事件类型的中文说明。
 *
 * 与服务端 `app.EventTypeLabel` 一一对应；未知类型不在此表中，展示时必须回退到
 * 原始 `event_type`，这样新增或历史类型仍能被用户区分。
 */
export const EVENT_TYPE_LABEL: Record<string, string> = {
  "attempt.failed": "执行尝试失败",
  "attempt.interrupted": "执行尝试中断",
  "attempt.started": "执行尝试开始",
  "code.saved": "代码保存",
  "code_save.recovery_required": "代码保存需要人工核验",
  "container.cleaned": "容器清理",
  "container.created": "容器创建",
  "container.retained": "容器保留",
  "plan.created": "计划生成",
  "repair.cycle_opened": "开启修复周期",
  "repair.round": "修复轮次记录",
  "requirement.submitted": "需求提交",
  "session.completed": "Agent 会话结束",
  "session.started": "Agent 会话开始",
  "source.fetched": "代码获取",
  "step.completed": "步骤完成",
  "step.started": "步骤开始",
  "task.cancel_requested": "收到取消请求",
  "task.cancelled": "任务取消",
  "task.claimed": "任务领取",
  "task.completed": "任务完成",
  "task.created": "任务创建",
  "task.failed": "任务失败",
  "task.lease_released": "任务租约释放",
  "task.recovery_required": "需要人工核验",
  "task.retried": "任务已重试",
  "task.retrying": "任务重试中",
  "task.started": "任务开始",
  "verification.failed": "验证未通过",
  "verification.passed": "验证通过",
  "verification.skipped": "验证跳过",
  "verification.started": "验证开始",
  "workflow.blocked": "工作流已停止",
  "workflow.cancel_requested": "工作流取消",
  "workflow.code_saved": "工作流代码保存",
  "workflow.completed": "工作流完成",
  "workflow.failed": "工作流失败",
  "workflow.recovery_required": "工作流需要人工核验",
  "workflow.resumed": "工作流恢复",
  "workflow.started": "工作流开始",
  "workflow.step_passed": "工作流步骤通过",
};

/**
 * 事件类型展示文案。
 *
 * 优先使用服务端给出的 `event_label`（服务端才知道当前版本定义了哪些类型），
 * 其次用本地映射，最后原样返回 `event_type`——绝不返回"未知事件"这类会把不同
 * 类型压缩成同一文本的兜底。
 */
export function eventTypeLabel(event: Pick<TaskEvent, "event_label" | "event_type">): string {
  const fromServer = event.event_label?.trim();
  if (fromServer) return fromServer;
  const known = EVENT_TYPE_LABEL[event.event_type];
  if (known) return known;
  return event.event_type;
}

/** 判断事件类型是否属于当前版本定义的类型 */
export function isKnownEventType(event: Pick<TaskEvent, "type_known">): boolean {
  return event.type_known;
}

/**
 * 把服务端时间文本解析为时间戳；空值与非法值返回 undefined。
 *
 * 与步骤时间共用同一套判定：非法取值不能被当作有效时间读，否则页面会显示
 * "Invalid Date"或伪造时刻。
 */
export function parseHistoryTime(value?: string): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  if (Number.isNaN(parsed)) return undefined;
  return parsed;
}

/**
 * 按本地时区格式化历史时间；缺失或非法时返回中性占位符。
 */
export function formatHistoryTime(value?: string): string {
  const parsed = parseHistoryTime(value);
  if (parsed === undefined) return HISTORY_PLACEHOLDER;
  return new Date(parsed).toLocaleString();
}

/**
 * 事件摘要的展示文本。
 *
 * 服务端已经只返回脱敏后的安全摘要，这里只负责"没有摘要时说什么"：显示中性
 * 占位符而不是空字符串，避免用户以为这一行渲染失败。
 */
export function eventSummaryText(event: Pick<TaskEvent, "summary">): string {
  const summary = event.summary?.trim();
  if (!summary) return EVENT_SUMMARY_PLACEHOLDER;
  return summary;
}

/** 事件关联标识的展示文本；只保留真实存在且非空的标识 */
export type EventRefs = {
  stepID?: string;
  attemptID?: string;
  sessionID?: string;
};

/**
 * 提取事件真实关联的步骤、尝试与会话标识。
 *
 * 只在字段存在且非空时给出：把缺失显示成 `undefined` 或空标签会让"这个事件
 * 属于整个任务"与"这个事件的归属字段丢了"无法区分。
 */
export function eventRefs(
  event: Pick<TaskEvent, "step_id" | "attempt_id" | "session_id">,
): EventRefs {
  const refs: EventRefs = {};
  const step = event.step_id?.trim();
  if (step) refs.stepID = step;
  const attempt = event.attempt_id?.trim();
  if (attempt) refs.attemptID = attempt;
  const session = event.session_id?.trim();
  if (session) refs.sessionID = session;
  return refs;
}

/**
 * 合并新一页事件到已有列表。
 *
 * 服务端按 `(created_at, id)` 升序返回，因此"加载更多"只需追加。这里仍按 `id`
 * 去重：分页期间若有新事件写入，偏移查询可能让同一条记录出现在相邻两页，
 * 去重让列表不出现重复行，也不打断服务端顺序。
 */
export function mergeEventPages(existing: TaskEvent[], next: TaskEvent[]): TaskEvent[] {
  if (existing.length === 0) return next;
  if (next.length === 0) return existing;
  const seen = new Set(existing.map((event) => event.id));
  const merged = [...existing];
  for (const event of next) {
    if (seen.has(event.id)) continue;
    merged.push(event);
    seen.add(event.id);
  }
  return merged;
}

/**
 * 实时事件的展示顺序：`(created_at, id)` 升序，与历史分页契约一致。
 *
 * 实时流按自增 `id`（写入先后）送达，这与 `(created_at, id)` 在正常情况下是同一个
 * 顺序。事件时间来自业务时钟，理论上仍可能不单调，因此合并后统一按契约排序，而不是
 * 直接采用到达顺序：同一批事件里出现更早的业务时间时，页面不应把它排到最后。
 *
 * 时间非法或缺失时退回按 `id` 比较，避免把不可解析的时间当作有效时刻。
 */
function compareEvents(a: TaskEvent, b: TaskEvent): number {
  const left = parseHistoryTime(a.created_at);
  const right = parseHistoryTime(b.created_at);
  if (left !== undefined && right !== undefined && left !== right) {
    return left - right;
  }
  return a.id - b.id;
}

/**
 * 把实时事件合并进已有实时列表。
 *
 * 去重口径是真实事件 `id`：同一事件可能因重连（游标重叠）或历史与实时两侧竞争而被
 * 收到多次，重复行会让用户以为发生了两次。返回新数组，不修改入参。
 */
export function mergeLiveEvents(existing: TaskEvent[], incoming: TaskEvent[]): TaskEvent[] {
  if (incoming.length === 0) return existing;
  const seen = new Set(existing.map((event) => event.id));
  const merged = [...existing];
  for (const event of incoming) {
    if (seen.has(event.id)) continue;
    merged.push(event);
    seen.add(event.id);
  }
  return merged.sort(compareEvents);
}

/**
 * 判断是否还有后续事件页。
 *
 * 依据服务端给出的真实总数字段，而不是"本页条数是否等于页大小"：后者在最后一页
 * 恰好填满时会多发起一次必然为空的请求。
 */
export function hasMoreEvents(loaded: number, total: number): boolean {
  if (!Number.isFinite(total)) return false;
  return loaded < total;
}

/** 修复周期一行的展示结论 */
export type RepairCycleDisplay = {
  /** 周期序号展示文本 */
  sequenceText: string;
  /** 开启时间展示文本 */
  openedText: string;
  /** 结束时间展示文本；缺失时为中性占位符 */
  closedText: string;
  /** 是否有真实结束时间 */
  hasClosed: boolean;
  /** 结束时间说明：缺失时说明"尚未记录"，存在时为空 */
  closeHint?: string;
  /** 开启来源展示文本 */
  openedByText: string;
  /** 尝试次数上限展示文本 */
  maxAttemptsText: string;
};

/**
 * 计算一个修复周期的展示结论。
 *
 * `closed_at` 缺失只表示"还没有记录结束时间"。这里不读任何周期状态字段，也不
 * 依据 `max_attempts` 或步骤状态推断周期是否结束、是否成功。
 */
export function repairCycleDisplay(
  cycle: Pick<RepairCycle, "sequence" | "opened_at" | "closed_at" | "opened_by" | "max_attempts">,
): RepairCycleDisplay {
  const closed = parseHistoryTime(cycle.closed_at);
  const openedBy = cycle.opened_by?.trim();
  const display: RepairCycleDisplay = {
    closedText: closed === undefined ? HISTORY_PLACEHOLDER : formatHistoryTime(cycle.closed_at),
    hasClosed: closed !== undefined,
    maxAttemptsText: Number.isFinite(cycle.max_attempts)
      ? `${cycle.max_attempts} 次`
      : HISTORY_PLACEHOLDER,
    openedByText: openedBy ? openedBy : HISTORY_PLACEHOLDER,
    openedText: formatHistoryTime(cycle.opened_at),
    sequenceText: `第 ${cycle.sequence} 个周期`,
  };
  if (closed === undefined) display.closeHint = REPAIR_CYCLE_OPEN_TEXT;
  return display;
}
