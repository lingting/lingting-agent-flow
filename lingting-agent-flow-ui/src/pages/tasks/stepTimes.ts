/**
 * 步骤起止时间的展示计算。
 *
 * 与 React 无关：把服务端返回的时间字段变换成"页面上应该显示什么"，因此缺失值、
 * 无法解析的取值与"仍在执行"的三种情况都可以被直接断言。
 *
 * 三条硬约束：
 *   1. 只展示服务端真实返回的时间，绝不从 created_at / updated_at / 任务时间推算；
 *   2. 缺失或无法解析时统一显示中性占位符，不显示伪造时刻，也不显示空字符串；
 *   3. 仍在执行（还没有结束状态）的步骤不显示完成时间，避免让人误以为已经结束。
 */
import type { Step } from "@/lib/api";

/** 时间缺失或无法解析时使用的中性占位符 */
export const STEP_TIME_PLACEHOLDER = "—";

/** 步骤时间展示结论 */
export type StepTimeDisplay = {
  /** 开始时间的展示文本；缺失时为占位符 */
  startedText: string;
  /** 结束时间的展示文本；缺失时（含仍在执行）为占位符 */
  finishedText: string;
  /** 是否有可展示的真实开始时间 */
  hasStarted: boolean;
  /** 是否有可展示的真实结束时间 */
  hasFinished: boolean;
  /**
   * 执行耗时说明。
   *
   * 只在起止时间都真实存在且顺序合理时给出，否则为 undefined——不估算、不用当前
   * 时间补一个"已用时长"，避免把仍在执行的步骤显示成有确定耗时。
   */
  durationText?: string;
};

/** 把服务端时间文本解析为时间戳；空值与非法值返回 undefined */
export function parseStepTime(value?: string): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  if (Number.isNaN(parsed)) return undefined;
  return parsed;
}

/**
 * 按本地时区格式化服务端时间。
 *
 * 服务端统一以 UTC RFC3339 返回，展示时转换成阅读者所在时区；无法解析时返回
 * 占位符而不是原始字符串，避免把非法取值当成有效时间读。
 */
export function formatStepTime(value?: string): string {
  const parsed = parseStepTime(value);
  if (parsed === undefined) return STEP_TIME_PLACEHOLDER;
  return new Date(parsed).toLocaleString();
}

/** 把毫秒时长格式化成可读文本；非法或负值返回 undefined */
export function formatStepDuration(millis?: number): string | undefined {
  if (millis === undefined || !Number.isFinite(millis) || millis < 0) return undefined;
  const totalSeconds = Math.floor(millis / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours} 小时 ${minutes} 分`;
  if (minutes > 0) return `${minutes} 分 ${seconds} 秒`;
  return `${seconds} 秒`;
}

/**
 * 计算一个步骤的时间展示结论。
 *
 * 结束时间只来自 `finished_at`：它由服务端在"执行周期结束"时写入，因此正在执行的
 * 步骤天然没有该字段。这里也不依据 `status` 反推时间，状态与时间必须各自如实展示。
 */
export function stepTimeDisplay(step: Pick<Step, "started_at" | "finished_at">): StepTimeDisplay {
  const started = parseStepTime(step.started_at);
  const finished = parseStepTime(step.finished_at);
  const display: StepTimeDisplay = {
    finishedText: finished === undefined ? STEP_TIME_PLACEHOLDER : formatStepTime(step.finished_at),
    hasFinished: finished !== undefined,
    hasStarted: started !== undefined,
    startedText: started === undefined ? STEP_TIME_PLACEHOLDER : formatStepTime(step.started_at),
  };
  if (started === undefined || finished === undefined) return display;
  // 时间顺序异常（结束早于开始）时不给出耗时：那说明数据本身有问题，计算出来的
  // 耗时只会把错误掩盖成一条看似合理的说明。
  if (finished < started) return display;
  return { ...display, durationText: formatStepDuration(finished - started) };
}

/** 步骤行的时间摘要文本：开始时间 → 结束时间（含耗时） */
export function stepTimeSummary(step: Pick<Step, "started_at" | "finished_at">): string {
  const display = stepTimeDisplay(step);
  if (!display.hasStarted && !display.hasFinished) return "尚未开始";
  if (!display.hasFinished) return `开始于 ${display.startedText}`;
  if (!display.hasStarted) return `结束于 ${display.finishedText}`;
  const duration = display.durationText ? `（耗时 ${display.durationText}）` : "";
  return `${display.startedText} → ${display.finishedText}${duration}`;
}
