import { describe, expect, it } from "vitest";

import type { Task } from "../../src/lib/api";
import {
  closeoutDisplay,
  closeoutSummary,
  conclusionNeedsAttention,
  conclusionWarning,
  isKnownCloseout,
  isKnownTaskResult,
  TASK_CLOSEOUT_META,
  TASK_CLOSEOUT_MISSING_LABEL,
  TASK_CLOSEOUT_SUMMARY_MISSING,
  TASK_RESULT_META,
  TASK_RESULT_MISSING_LABEL,
  taskResultDisplay,
} from "../../src/pages/tasks/closeout";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份任务样本，只覆盖结论展示用到的字段 */
function task(overrides: Partial<Task> = {}): Task {
  return {
    cancel_requested: false,
    created_at: "2026-10-26T08:00:00Z",
    description: "描述",
    id: "task-1",
    name: "任务",
    project_id: "",
    status: "developing",
    status_finished: false,
    status_terminal: false,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

describe("最终结论与收尾闭环元数据", () => {
  it("覆盖 Go 端 TaskResult 的全部非空取值", () => {
    expect(Object.keys(TASK_RESULT_META).sort()).toEqual(["cancelled", "failed", "succeeded"]);
  });

  it("覆盖 Go 端 TaskCloseoutState 的全部取值", () => {
    expect(Object.keys(TASK_CLOSEOUT_META).sort()).toEqual([
      "cancelled",
      "confirmed",
      "inconsistent",
      "pending",
      "unconfirmed",
    ]);
  });

  it("已知取值的中文标签不含英文状态名", () => {
    for (const meta of [...Object.values(TASK_RESULT_META), ...Object.values(TASK_CLOSEOUT_META)]) {
      expect(meta.label.length).toBeGreaterThan(0);
      expect(meta.label).not.toMatch(/^[a-z_]+$/);
    }
  });

  it("只有 confirmed 与 succeeded 组合给出成功色", () => {
    for (const closeout of Object.keys(TASK_CLOSEOUT_META)) {
      const result = taskResultDisplay("succeeded", closeout);
      expect(result.color).toBe(closeout === "confirmed" ? "success" : "warning");
    }
  });
});

describe("最终结论展示", () => {
  it("缺失时不推断成功", () => {
    const result = taskResultDisplay(undefined, "confirmed");
    expect(result.label).toBe(TASK_RESULT_MISSING_LABEL);
    expect(result.known).toBe(false);
    expect(result.color).not.toBe("success");
    expect(result.attention).toBe(true);
  });

  it("空字符串与缺失等价", () => {
    expect(taskResultDisplay("", "confirmed").label).toBe(TASK_RESULT_MISSING_LABEL);
  });

  it("failed 与 cancelled 展示各自的业务含义", () => {
    expect(taskResultDisplay("failed", "unconfirmed").label).toBe("失败");
    expect(taskResultDisplay("failed", "unconfirmed").color).toBe("error");
    expect(taskResultDisplay("cancelled", "cancelled").label).toBe("已取消");
  });

  it("succeeded + confirmed 才显示为确定成功", () => {
    const result = taskResultDisplay("succeeded", "confirmed");
    expect(result.label).toBe("成功");
    expect(result.color).toBe("success");
    expect(result.attention).toBe(false);
  });

  it("未知取值原样展示且不猜测语义", () => {
    const result = taskResultDisplay("partially_succeeded", "confirmed");
    expect(result.label).toBe("partially_succeeded");
    expect(result.known).toBe(false);
    expect(result.color).not.toBe("success");
  });

  it("isKnownTaskResult 不把未知值当作已知", () => {
    expect(isKnownTaskResult("succeeded")).toBe(true);
    expect(isKnownTaskResult("unknown")).toBe(false);
    expect(isKnownTaskResult(undefined)).toBe(false);
  });
});

describe("收尾闭环展示", () => {
  it("confirmed 表示收尾已确认，不需要警示", () => {
    const meta = closeoutDisplay("confirmed");
    expect(meta.label).toBe("已确认收尾");
    expect(meta.color).toBe("success");
    expect(meta.attention).toBe(false);
  });

  it("pending 明确表示仍在推进且需要关注", () => {
    const meta = closeoutDisplay("pending");
    expect(meta.label).toBe("收尾进行中");
    expect(meta.attention).toBe(true);
    expect(meta.label).not.toContain("已完成");
  });

  it("unconfirmed 明确表示尚未闭环", () => {
    const meta = closeoutDisplay("unconfirmed");
    expect(meta.label).toBe("收尾未闭环");
    expect(meta.attention).toBe(true);
    expect(meta.color).not.toBe("success");
  });

  it("inconsistent 提示人工核验且不是成功色", () => {
    const meta = closeoutDisplay("inconsistent");
    expect(meta.label).toContain("人工核验");
    expect(meta.attention).toBe(true);
    expect(meta.color).not.toBe("success");
  });

  it("cancelled 表示按取消结束", () => {
    expect(closeoutDisplay("cancelled").label).toBe("已按取消结束");
  });

  it("缺失时给出中性提示而不是已确认", () => {
    const meta = closeoutDisplay(undefined);
    expect(meta.label).toBe(TASK_CLOSEOUT_MISSING_LABEL);
    expect(meta.known).toBe(false);
    expect(meta.attention).toBe(true);
    expect(meta.color).not.toBe("success");
  });

  it("未知取值原样展示", () => {
    const meta = closeoutDisplay("half_confirmed");
    expect(meta.label).toBe("half_confirmed");
    expect(meta.known).toBe(false);
    expect(meta.attention).toBe(true);
  });

  it("isKnownCloseout 不把未知值当作已知", () => {
    expect(isKnownCloseout("inconsistent")).toBe(true);
    expect(isKnownCloseout("done")).toBe(false);
    expect(isKnownCloseout(undefined)).toBe(false);
  });
});

describe("收尾说明", () => {
  it("优先使用服务端提供的说明", () => {
    expect(
      closeoutSummary(task({ closeout: "confirmed", closeout_summary: "任务与执行记录一致确认收尾完成" })),
    ).toBe("任务与执行记录一致确认收尾完成");
  });

  it("缺失时使用中性提示，不生成与服务端矛盾的描述", () => {
    expect(closeoutSummary(task({ closeout: "unconfirmed" }))).toBe(TASK_CLOSEOUT_SUMMARY_MISSING);
    expect(closeoutSummary(task({ closeout_summary: "   " }))).toBe(TASK_CLOSEOUT_SUMMARY_MISSING);
  });
});

describe("是否需要警示", () => {
  it("succeeded + confirmed 不需要警示", () => {
    const value = task({ closeout: "confirmed", final_result: "succeeded", status: "completed" });
    expect(conclusionNeedsAttention(value)).toBe(false);
    expect(conclusionWarning(value)).toBeUndefined();
  });

  it("status=completed 但缺少 final_result 需要警示", () => {
    const value = task({ closeout: "unconfirmed", status: "completed", status_finished: true });
    expect(conclusionNeedsAttention(value)).toBe(true);
    expect(conclusionWarning(value)).toContain("没有提供最终业务结论");
    expect(conclusionWarning(value)).toContain("不得当作已通过验收");
  });

  it("succeeded + unconfirmed 明确说明尚未闭环", () => {
    const value = task({ closeout: "unconfirmed", final_result: "succeeded", status: "completed" });
    const warning = conclusionWarning(value);
    expect(warning).toContain("最终结论为成功，但收尾没有确认完成");
    expect(warning).toContain("尚未闭环");
  });

  it("inconsistent 明确要求人工核验", () => {
    const value = task({ closeout: "inconsistent", final_result: "succeeded", status: "completed" });
    expect(conclusionWarning(value)).toContain("必须人工核验");
    expect(taskResultDisplay(value.final_result, value.closeout).color).not.toBe("success");
  });

  it("pending 说明收尾尚未结束", () => {
    const value = task({ closeout: "pending", final_result: "succeeded", status: "completed" });
    expect(conclusionNeedsAttention(value)).toBe(true);
    expect(conclusionWarning(value)).toContain("收尾尚未结束");
  });

  it("failed 与 cancelled 不产生成功提示", () => {
    for (const finalResult of ["failed", "cancelled"]) {
      const value = task({ closeout: "unconfirmed", final_result: finalResult, status: "failed" });
      const warning = conclusionWarning(value) ?? "";
      expect(warning).not.toContain("成功");
      expect(taskResultDisplay(value.final_result, value.closeout).color).not.toBe("success");
    }
  });

  it("未知取值提示原始值未定义", () => {
    const value = task({ closeout: "future_state", final_result: "future_result" });
    const warning = conclusionWarning(value) ?? "";
    expect(warning).toContain("future_state");
    expect(warning).toContain("future_result");
    expect(warning).toContain("未在当前版本中定义");
  });
});
