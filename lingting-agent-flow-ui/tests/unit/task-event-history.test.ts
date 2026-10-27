import { describe, expect, it } from "vitest";

import type { RepairCycle, TaskEvent } from "../../src/lib/api";
import {
  EVENT_SUMMARY_PLACEHOLDER,
  EVENT_TYPE_LABEL,
  eventRefs,
  eventSummaryText,
  eventTypeLabel,
  formatHistoryTime,
  hasMoreEvents,
  HISTORY_PLACEHOLDER,
  isKnownEventType,
  mergeEventPages,
  parseHistoryTime,
  REPAIR_CYCLE_EMPTY_TEXT,
  REPAIR_CYCLE_OPEN_TEXT,
  repairCycleDisplay,
} from "../../src/pages/tasks/history";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/**
 * 一条事件样本，字段与 Go 端 `httpapi.EventResponse` 一致。
 *
 * `event_label` / `summary` / 关联标识在 Go 端带 `omitempty`，缺省即不出现。
 */
function event(overrides: Partial<TaskEvent> = {}): TaskEvent {
  return {
    created_at: "2026-10-26T08:00:00Z",
    event_type: "task.created",
    id: 1,
    task_id: "task-1",
    type_known: true,
    ...overrides,
  };
}

/** 一个修复周期样本，字段与 Go 端 `httpapi.RepairCycleResponse` 一致 */
function cycle(overrides: Partial<RepairCycle> = {}): RepairCycle {
  return {
    id: "cycle-1",
    max_attempts: 3,
    opened_at: "2026-10-26T08:00:00Z",
    opened_by: "user_reopen",
    sequence: 1,
    step_id: "step-1",
    task_id: "task-1",
    ...overrides,
  };
}

describe("事件类型文案", () => {
  it("已知类型使用服务端给出的中文说明", () => {
    expect(eventTypeLabel(event({ event_label: "任务创建" }))).toBe("任务创建");
  });

  it("缺少服务端标签时使用本地映射", () => {
    expect(eventTypeLabel(event({ event_type: "verification.passed" }))).toBe("验证通过");
    expect(EVENT_TYPE_LABEL["workflow.blocked"]).toBe("工作流已停止");
  });

  it("未知类型原样保留，不压缩成统一文案", () => {
    const unknown = event({ event_type: "legacy.removed", type_known: false });
    expect(eventTypeLabel(unknown)).toBe("legacy.removed");
    expect(eventTypeLabel(event({ event_type: "another.unknown" }))).toBe("another.unknown");
    expect(isKnownEventType(unknown)).toBe(false);
  });

  it("已知类型被标记为已定义", () => {
    expect(isKnownEventType(event({ type_known: true }))).toBe(true);
  });

  it("服务端标签优先于本地映射", () => {
    expect(eventTypeLabel(event({ event_label: "自定义说明", event_type: "task.created" }))).toBe(
      "自定义说明",
    );
  });

  it("空白服务端标签回退到类型名", () => {
    expect(eventTypeLabel(event({ event_label: "   ", event_type: "unknown.type" }))).toBe(
      "unknown.type",
    );
  });
});

describe("事件摘要", () => {
  it("有安全摘要时原样展示", () => {
    expect(eventSummaryText(event({ summary: "失败阶段 verify" }))).toBe("失败阶段 verify");
  });

  it("没有摘要时显示中性占位符而不是空字符串", () => {
    expect(eventSummaryText(event())).toBe(EVENT_SUMMARY_PLACEHOLDER);
    expect(eventSummaryText(event({ summary: "   " }))).toBe(EVENT_SUMMARY_PLACEHOLDER);
  });

  it("事件对象本身不携带原始 payload 字段", () => {
    // 类型层面就没有 payload：这里断言渲染取不到任何原始内容。
    const record = event({ summary: "输出已脱敏" });
    expect("payload" in record).toBe(false);
    expect(eventSummaryText(record)).toBe("输出已脱敏");
  });
});

describe("事件关联标识", () => {
  it("只返回真实存在且非空的标识", () => {
    expect(eventRefs(event())).toEqual({});
    expect(eventRefs(event({ attempt_id: "  " }))).toEqual({});
    expect(
      eventRefs(event({ attempt_id: "attempt-1", session_id: "session-1", step_id: "step-1" })),
    ).toEqual({ attemptID: "attempt-1", sessionID: "session-1", stepID: "step-1" });
  });
});

describe("时间本地化", () => {
  it("解析合法时间戳", () => {
    expect(parseHistoryTime("2026-10-26T08:00:00Z")).toBe(Date.parse("2026-10-26T08:00:00Z"));
  });

  it("空值与非法值都返回 undefined", () => {
    expect(parseHistoryTime()).toBeUndefined();
    expect(parseHistoryTime("")).toBeUndefined();
    expect(parseHistoryTime("not-a-date")).toBeUndefined();
  });

  it("合法时间按本地时区格式化", () => {
    const parsed = Date.parse("2026-10-26T08:00:00Z");
    expect(formatHistoryTime("2026-10-26T08:00:00Z")).toBe(new Date(parsed).toLocaleString());
  });

  it("缺失或非法时间显示同一个中性占位符", () => {
    expect(formatHistoryTime()).toBe(HISTORY_PLACEHOLDER);
    expect(formatHistoryTime("not-a-date")).toBe(HISTORY_PLACEHOLDER);
  });
});

describe("分页合并", () => {
  it("首屏直接采用服务端顺序", () => {
    const first = [event({ id: 1 }), event({ id: 2 })];
    expect(mergeEventPages([], first)).toEqual(first);
  });

  it("加载更多时追加并保持服务端顺序", () => {
    const merged = mergeEventPages([event({ id: 1 })], [event({ id: 2 }), event({ id: 3 })]);
    expect(merged.map((item) => item.id)).toEqual([1, 2, 3]);
  });

  it("重复标识不产生重复行", () => {
    const merged = mergeEventPages(
      [event({ id: 1 }), event({ id: 2 })],
      [event({ id: 2 }), event({ id: 3 })],
    );
    expect(merged.map((item) => item.id)).toEqual([1, 2, 3]);
  });

  it("空新页不改变已有列表", () => {
    const existing = [event({ id: 1 })];
    expect(mergeEventPages(existing, [])).toEqual(existing);
  });

  it("依据真实总数判断是否还有后续页", () => {
    expect(hasMoreEvents(0, 5)).toBe(true);
    expect(hasMoreEvents(5, 5)).toBe(false);
    expect(hasMoreEvents(3, 3)).toBe(false);
    expect(hasMoreEvents(0, Number.NaN)).toBe(false);
  });
});

describe("修复周期展示", () => {
  it("完整记录展示序号、起止时间、来源与上限", () => {
    const display = repairCycleDisplay(
      cycle({ closed_at: "2026-10-26T09:00:00Z", max_attempts: 5, sequence: 2 }),
    );
    expect(display.sequenceText).toBe("第 2 个周期");
    expect(display.openedText).toBe(formatHistoryTime("2026-10-26T08:00:00Z"));
    expect(display.closedText).toBe(formatHistoryTime("2026-10-26T09:00:00Z"));
    expect(display.hasClosed).toBe(true);
    expect(display.closeHint).toBeUndefined();
    expect(display.openedByText).toBe("user_reopen");
    expect(display.maxAttemptsText).toBe("5 次");
  });

  it("结束时间缺失时显示中性占位符与说明，不推断成功或失败", () => {
    const display = repairCycleDisplay(cycle());
    expect(display.closedText).toBe(HISTORY_PLACEHOLDER);
    expect(display.hasClosed).toBe(false);
    expect(display.closeHint).toBe(REPAIR_CYCLE_OPEN_TEXT);
  });

  it("结束时间非法时同样按缺失处理", () => {
    const display = repairCycleDisplay(cycle({ closed_at: "not-a-date" }));
    expect(display.hasClosed).toBe(false);
    expect(display.closeHint).toBe(REPAIR_CYCLE_OPEN_TEXT);
  });

  it("开启来源为空时显示占位符", () => {
    expect(repairCycleDisplay(cycle({ opened_by: "  " })).openedByText).toBe(HISTORY_PLACEHOLDER);
  });

  it("开启时间非法时显示占位符", () => {
    expect(repairCycleDisplay(cycle({ opened_at: "not-a-date" })).openedText).toBe(
      HISTORY_PLACEHOLDER,
    );
  });

  it("尝试上限非法时显示占位符", () => {
    expect(repairCycleDisplay(cycle({ max_attempts: Number.NaN })).maxAttemptsText).toBe(
      HISTORY_PLACEHOLDER,
    );
  });

  it("空历史文案明确说明不代表未执行", () => {
    expect(REPAIR_CYCLE_EMPTY_TEXT).toBe("暂无修复周期记录");
  });
});
