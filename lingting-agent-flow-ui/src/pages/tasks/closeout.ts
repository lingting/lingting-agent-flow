/**
 * 任务最终结论与收尾闭环的展示元数据。
 *
 * 这里只做"把服务端事实翻译成中文标签"，不新增状态、不改变语义，也不做任何推断：
 *
 *   - `status` 表达任务处在哪个业务阶段；
 *   - `final_result` 表达这次执行的最终业务结论；
 *   - `closeout` 表达任务结论是否已经闭环。
 *
 * 三者是三个独立事实，任何两个都不能互相推导。因此本模块的两个入口函数都要求
 * 调用方同时给出真实字段值，缺省与未知取值一律走"未提供 / 未知"分支，绝不回落到
 * 成功文案。
 */
import type { DictItem } from "lingting-react-ui";

import type { Task, TaskCloseoutState, TaskResult } from "@/lib/api";

/** 一条展示结论：标签、颜色、是否已知取值、是否需要用户注意 */
export type ConclusionMeta = {
  /** 中文标签；未知取值时为服务端原始值 */
  label: string;
  /** 标签颜色，与 Ant Design 的语义色一致 */
  color: string;
  /** 是否为前端已知取值；为 false 时不得附加任何语义解释 */
  known: boolean;
  /** 是否需要用户关注（未提供、未闭环、不一致等） */
  attention: boolean;
};

/** 全部最终结论，覆盖 Go 端 `domain.TaskResult` 的每一个非空取值 */
export const TASK_RESULT_META: Record<TaskResult, ConclusionMeta> = {
  succeeded: { attention: false, color: "success", known: true, label: "成功" },
  failed: { attention: true, color: "error", known: true, label: "失败" },
  cancelled: { attention: false, color: "default", known: true, label: "已取消" },
};

/** 全部收尾闭环结论，覆盖 Go 端 `domain.TaskCloseoutState` 的每一个取值 */
export const TASK_CLOSEOUT_META: Record<TaskCloseoutState, ConclusionMeta> = {
  confirmed: { attention: false, color: "success", known: true, label: "已确认收尾" },
  pending: { attention: false, color: "processing", known: true, label: "收尾进行中" },
  unconfirmed: { attention: true, color: "warning", known: true, label: "收尾未闭环" },
  inconsistent: { attention: true, color: "error", known: true, label: "收尾不一致，需人工核验" },
  cancelled: { attention: false, color: "default", known: true, label: "已按取消结束" },
};

/** 最终结论缺省时的固定文案：不推断、不当作成功 */
export const TASK_RESULT_MISSING_LABEL = "未提供最终结论";
/** 收尾闭环缺省时的固定文案：服务端没有给出闭环结论 */
export const TASK_CLOSEOUT_MISSING_LABEL = "未提供收尾结论";
/** 收尾说明缺省时的中性提示 */
export const TASK_CLOSEOUT_SUMMARY_MISSING = "服务端未提供收尾说明";

/** 最终结论字典，供 DictTag 使用（仅用于已知取值） */
export const TASK_RESULT_DICT: DictItem<string>[] = Object.entries(TASK_RESULT_META).map(
  ([value, meta]) => ({ label: meta.label, tagColor: meta.color, value }),
);

/** 收尾闭环字典，供 DictTag 使用（仅用于已知取值） */
export const TASK_CLOSEOUT_DICT: DictItem<string>[] = Object.entries(TASK_CLOSEOUT_META).map(
  ([value, meta]) => ({ label: meta.label, tagColor: meta.color, value }),
);

/** 判断最终结论是否为前端已知取值 */
export function isKnownTaskResult(result?: string): result is TaskResult {
  return result !== undefined && Object.hasOwn(TASK_RESULT_META, result);
}

/** 判断收尾闭环是否为前端已知取值 */
export function isKnownCloseout(closeout?: string): closeout is TaskCloseoutState {
  return closeout !== undefined && Object.hasOwn(TASK_CLOSEOUT_META, closeout);
}

/**
 * 读取最终结论的展示元数据。
 *
 * `closeout` 参与判定只为一件真实的事：`final_result=succeeded` 但收尾没有确认时，
 * 不能给出绿色成功标签。除此之外不改变标签含义。
 */
export function taskResultDisplay(result?: string, closeout?: string): ConclusionMeta {
  if (result === undefined || result === "") {
    return { attention: true, color: "default", known: false, label: TASK_RESULT_MISSING_LABEL };
  }
  if (!isKnownTaskResult(result)) {
    return { attention: true, color: "default", known: false, label: result };
  }
  const meta = TASK_RESULT_META[result];
  if (result === "succeeded" && closeout !== "confirmed") {
    // 成功结论本身成立，但收尾尚未确认：用警告色，避免读成"整体已通过验收"。
    return { ...meta, color: "warning" };
  }
  return meta;
}

/**
 * 读取收尾闭环的展示元数据。
 *
 * 已知取值按领域语义展示；`pending` 额外标记为需要关注，因为它表示"收尾尚未结束"，
 * 用户不能把它读成已完成。未知取值原样展示，不猜测为已确认。
 */
export function closeoutDisplay(closeout?: string): ConclusionMeta {
  if (closeout === undefined || closeout === "") {
    return { attention: true, color: "default", known: false, label: TASK_CLOSEOUT_MISSING_LABEL };
  }
  if (!isKnownCloseout(closeout)) {
    return { attention: true, color: "default", known: false, label: closeout };
  }
  const meta = TASK_CLOSEOUT_META[closeout];
  if (closeout === "pending") {
    return { ...meta, attention: true };
  }
  return meta;
}

/**
 * 收尾说明。
 *
 * 优先使用服务端提供的 `closeout_summary`：它是领域层给出的唯一权威解释。
 * 缺省时返回中性提示，不由前端生成与事实可能矛盾的解释。
 */
export function closeoutSummary(task: Task): string {
  const summary = task.closeout_summary?.trim();
  if (summary) return summary;
  return TASK_CLOSEOUT_SUMMARY_MISSING;
}

/**
 * 是否需要向用户明确警示"不得当作确定成功"。
 *
 * 只有服务端明确给出未闭环或不一致的现场才警示；缺省与未知取值同样需要提示，
 * 因为它们都意味着"没有确认过的成功"。
 */
export function conclusionNeedsAttention(task: Task): boolean {
  const result = taskResultDisplay(task.final_result, task.closeout);
  const closeout = closeoutDisplay(task.closeout);
  return result.attention || closeout.attention;
}

/**
 * 警示文案。
 *
 * 只在确实需要关注时返回内容，且不描述任何前端推断出的业务结论：
 * 具体原因来自服务端 `closeout_summary`。
 */
export function conclusionWarning(task: Task): string | undefined {
  if (!conclusionNeedsAttention(task)) return undefined;
  const result = taskResultDisplay(task.final_result, task.closeout);
  const facts: string[] = [];
  if (task.final_result === "succeeded" && task.closeout !== "confirmed") {
    facts.push("最终结论为成功，但收尾没有确认完成");
  }
  if (task.closeout === "inconsistent") {
    facts.push("任务被标记为成功但执行记录没有确认完成，必须人工核验");
  }
  if (task.closeout === "unconfirmed") {
    facts.push("任务结论尚未闭环，仍需继续推进或人工核验");
  }
  if (task.closeout === "pending") {
    facts.push("工作流仍在推进，收尾尚未结束");
  }
  if (result.label === TASK_RESULT_MISSING_LABEL) {
    facts.push("服务端没有提供最终业务结论，任务状态不能替代结论");
  }
  if (!isKnownTaskResult(task.final_result) && task.final_result !== undefined && task.final_result !== "") {
    facts.push(`最终结论取值 ${task.final_result} 未在当前版本中定义`);
  }
  if (!isKnownCloseout(task.closeout) && task.closeout !== undefined && task.closeout !== "") {
    facts.push(`收尾结论取值 ${task.closeout} 未在当前版本中定义`);
  }
  facts.push("该任务不得当作已通过验收。");
  return facts.join("；");
}
