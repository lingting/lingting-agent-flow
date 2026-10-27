import { describe, expect, it } from "vitest";

import type { Step } from "../../src/lib/api";
import {
  formatStepDuration,
  formatStepTime,
  parseStepTime,
  STEP_TIME_PLACEHOLDER,
  stepTimeDisplay,
  stepTimeSummary,
} from "../../src/pages/tasks/stepTimes";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份步骤样本，只关心时间字段 */
function step(overrides: Partial<Step> = {}): Step {
  return {
    created_at: "2026-10-26T08:00:00Z",
    id: "step-1",
    name: "实现登录步骤",
    objective: "按验收条件实现登录接口",
    sequence: 1,
    status: "pending",
    status_terminal: false,
    step_type: "develop",
    task_id: "task-1",
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

describe("步骤时间解析", () => {
  it("解析 RFC3339 文本为时间戳", () => {
    expect(parseStepTime("2026-10-26T08:00:00Z")).toBe(Date.parse("2026-10-26T08:00:00Z"));
  });

  it("缺失与非法取值都不被当作有效时间", () => {
    expect(parseStepTime(undefined)).toBeUndefined();
    expect(parseStepTime("")).toBeUndefined();
    expect(parseStepTime("不是时间")).toBeUndefined();
  });

  it("按本地时区格式化，非法值返回占位符", () => {
    const text = formatStepTime("2026-10-26T08:00:00Z");
    // 展示内容与浏览器时区相关，但必须与本地格式化结果一致，而不是原始 UTC 字符串。
    expect(text).toBe(new Date("2026-10-26T08:00:00Z").toLocaleString());
    expect(formatStepTime(undefined)).toBe(STEP_TIME_PLACEHOLDER);
    expect(formatStepTime("2026-13-45T99:99:99Z")).toBe(STEP_TIME_PLACEHOLDER);
  });

  it("时长格式化覆盖秒、分与小时", () => {
    expect(formatStepDuration(9_000)).toBe("9 秒");
    expect(formatStepDuration(90_000)).toBe("1 分 30 秒");
    expect(formatStepDuration(3_900_000)).toBe("1 小时 5 分");
    expect(formatStepDuration(-1)).toBeUndefined();
    expect(formatStepDuration(Number.NaN)).toBeUndefined();
  });
});

describe("步骤时间展示", () => {
  it("未开始的步骤两个时间都是占位符，且没有耗时", () => {
    const display = stepTimeDisplay(step());
    expect(display.hasStarted).toBe(false);
    expect(display.hasFinished).toBe(false);
    expect(display.startedText).toBe(STEP_TIME_PLACEHOLDER);
    expect(display.finishedText).toBe(STEP_TIME_PLACEHOLDER);
    expect(display.durationText).toBeUndefined();
    expect(stepTimeSummary(step())).toBe("尚未开始");
  });

  it("正在执行的步骤只显示开始时间，不伪造完成时间", () => {
    const display = stepTimeDisplay(step({ started_at: "2026-10-26T08:00:00Z" }));
    expect(display.hasStarted).toBe(true);
    expect(display.hasFinished).toBe(false);
    expect(display.finishedText).toBe(STEP_TIME_PLACEHOLDER);
    expect(display.durationText).toBeUndefined();
    expect(stepTimeSummary(step({ started_at: "2026-10-26T08:00:00Z" }))).toContain("开始于");
  });

  it("已结束的步骤展示起止时间与耗时", () => {
    const display = stepTimeDisplay(
      step({
        finished_at: "2026-10-26T08:02:30Z",
        started_at: "2026-10-26T08:00:00Z",
      }),
    );
    expect(display.hasStarted).toBe(true);
    expect(display.hasFinished).toBe(true);
    expect(display.durationText).toBe("2 分 30 秒");
  });

  it("结束早于开始时不计算耗时，但仍如实展示两个时间", () => {
    const display = stepTimeDisplay(
      step({
        finished_at: "2026-10-26T07:59:00Z",
        started_at: "2026-10-26T08:00:00Z",
      }),
    );
    expect(display.durationText).toBeUndefined();
    expect(display.hasStarted).toBe(true);
    expect(display.hasFinished).toBe(true);
    expect(display.startedText).not.toBe(STEP_TIME_PLACEHOLDER);
    expect(display.finishedText).not.toBe(STEP_TIME_PLACEHOLDER);
  });

  it("只有结束时间时显示结束时间，不补一个开始时间", () => {
    const display = stepTimeDisplay(step({ finished_at: "2026-10-26T08:02:00Z" }));
    expect(display.hasStarted).toBe(false);
    expect(display.startedText).toBe(STEP_TIME_PLACEHOLDER);
    expect(stepTimeSummary(step({ finished_at: "2026-10-26T08:02:00Z" }))).toContain("结束于");
  });

  it("缺失时间时摘要不会显示任何时刻", () => {
    const summary = stepTimeSummary(step({ status: "failed" }));
    expect(summary).toBe("尚未开始");
    expect(summary).not.toContain("2026");
  });

  it("不会用 created_at / updated_at 冒充起止时间", () => {
    const display = stepTimeDisplay(
      step({
        created_at: "2026-10-26T07:00:00Z",
        updated_at: "2026-10-26T09:00:00Z",
      }),
    );
    expect(display.startedText).toBe(STEP_TIME_PLACEHOLDER);
    expect(display.finishedText).toBe(STEP_TIME_PLACEHOLDER);
  });

  it("自动刷新拿到新快照后展示同步更新", () => {
    const pending = step();
    const running = step({ started_at: "2026-10-26T08:00:00Z", status: "running" });
    const completed = step({
      finished_at: "2026-10-26T08:01:00Z",
      started_at: "2026-10-26T08:00:00Z",
      status: "completed",
    });
    expect(stepTimeDisplay(pending).hasStarted).toBe(false);
    expect(stepTimeDisplay(running).hasStarted).toBe(true);
    expect(stepTimeDisplay(running).hasFinished).toBe(false);
    expect(stepTimeDisplay(completed).hasFinished).toBe(true);
    expect(stepTimeDisplay(completed).durationText).toBe("1 分 0 秒");
  });

  it("刷新失败时保留的时间仍是最后一次真实快照", () => {
    // 刷新失败不会产生新对象，页面继续用上一份快照计算，结果必须保持稳定。
    const lastSuccess = step({
      finished_at: "2026-10-26T08:01:00Z",
      started_at: "2026-10-26T08:00:00Z",
      status: "completed",
    });
    const before = stepTimeDisplay(lastSuccess);
    const after = stepTimeDisplay({ ...lastSuccess });
    expect(after).toEqual(before);
    expect(after.hasFinished).toBe(true);
  });

  it("未知状态不会影响时间展示结论", () => {
    const display = stepTimeDisplay(
      step({
        started_at: "2026-10-26T08:00:00Z",
        status: "awaiting_review" as Step["status"],
      }),
    );
    expect(display.hasStarted).toBe(true);
    expect(display.hasFinished).toBe(false);
  });

  it("失败与待核验步骤的时间照常展示，不因状态而改写", () => {
    const failed = stepTimeDisplay(
      step({
        finished_at: "2026-10-26T08:05:00Z",
        started_at: "2026-10-26T08:00:00Z",
        status: "failed",
      }),
    );
    expect(failed.hasStarted).toBe(true);
    expect(failed.hasFinished).toBe(true);
    expect(failed.durationText).toBe("5 分 0 秒");
  });
});
