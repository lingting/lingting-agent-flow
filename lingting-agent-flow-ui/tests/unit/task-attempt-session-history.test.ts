import { describe, expect, it } from "vitest";

import type { Attempt, Session } from "../../src/lib/api";
import {
  ATTEMPT_CANCELLED_LABEL,
  ATTEMPT_ERROR_CLASS_LABEL,
  ATTEMPT_FAILURE_STAGE_LABEL,
  ATTEMPT_TIMED_OUT_LABEL,
  attemptErrorClassLabel,
  attemptFailureFacts,
  attemptFailureStageLabel,
  SESSION_ARCHIVE_STATUS_LABEL,
  SESSION_EXPORT_STATUS_LABEL,
  SESSION_STATUS_MISSING_LABEL,
  sessionArchiveStatusLabel,
  sessionExportStatusLabel,
  sessionRunStatusLabel,
  sessionStatusLabel,
} from "../../src/pages/tasks/steps";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/**
 * 一份尝试样本，字段与 Go 端 `httpapi.AttemptResponse` 一致。
 *
 * `timed_out` / `cancelled` 在 Go 端没有 `omitempty`，因此默认给出布尔值；
 * `error_class` / `failure_stage` 带 `omitempty`，缺省即不出现。
 */
function attempt(overrides: Partial<Attempt> = {}): Attempt {
  return {
    attempt_number: 1,
    cancelled: false,
    created_at: "2026-10-26T08:00:00Z",
    exit_code: 0,
    id: "attempt-1",
    session_ids: [],
    started_at: "2026-10-26T08:00:00Z",
    status: "completed",
    status_terminal: true,
    step_id: "step-1",
    task_id: "task-1",
    timed_out: false,
    ...overrides,
  };
}

/** 一份会话样本，字段与 Go 端 `httpapi.SessionResponse` 一致 */
function session(overrides: Partial<Session> = {}): Session {
  return {
    agent_role: "developer",
    archive_status: "succeeded",
    created_at: "2026-10-26T08:00:00Z",
    export_status: "succeeded",
    id: "session-1",
    model: "group/free",
    provider: "openai",
    role_id: "role-1",
    started_at: "2026-10-26T08:00:00Z",
    status: "completed",
    status_terminal: true,
    task_id: "task-1",
    ...overrides,
  };
}

describe("尝试的超时与取消标记", () => {
  it("timed_out=true 时给出已超时标记", () => {
    const facts = attemptFailureFacts(attempt({ status: "timed_out", timed_out: true }));
    expect(facts.timedOut).toBe(ATTEMPT_TIMED_OUT_LABEL);
    expect(facts.timedOut).toBe("已超时");
  });

  it("timed_out=false 时不产生超时警告", () => {
    expect(attemptFailureFacts(attempt({ timed_out: false })).timedOut).toBeUndefined();
  });

  it("cancelled=true 时给出已取消标记", () => {
    const facts = attemptFailureFacts(attempt({ cancelled: true, status: "cancelled" }));
    expect(facts.cancelled).toBe(ATTEMPT_CANCELLED_LABEL);
    expect(facts.cancelled).toBe("已取消");
  });

  it("cancelled=false 时不产生取消警告", () => {
    expect(attemptFailureFacts(attempt({ cancelled: false })).cancelled).toBeUndefined();
  });

  it("超时与取消是两个独立事实，不会互相推导", () => {
    const timedOutOnly = attemptFailureFacts(attempt({ timed_out: true }));
    expect(timedOutOnly.timedOut).toBe("已超时");
    expect(timedOutOnly.cancelled).toBeUndefined();

    const cancelledOnly = attemptFailureFacts(attempt({ cancelled: true }));
    expect(cancelledOnly.cancelled).toBe("已取消");
    expect(cancelledOnly.timedOut).toBeUndefined();
  });

  it("布尔事实不依赖 status：status 为 timed_out 但布尔为 false 时不显示超时", () => {
    // 接口把布尔值与状态分开输出；前端只采信布尔字段，不从状态反推。
    expect(attemptFailureFacts(attempt({ status: "timed_out" })).timedOut).toBeUndefined();
  });
});

describe("尝试的错误分类与失败阶段", () => {
  it("error_class 有值时准确展示已知分类的中文说明", () => {
    const facts = attemptFailureFacts(attempt({ error_class: "timeout", status: "timed_out" }));
    expect(facts.errorClass).toBe("执行超时");
    expect(facts.errorClassKnown).toBe(true);
  });

  it("failure_stage 有值时准确展示已知阶段的中文说明", () => {
    const facts = attemptFailureFacts(attempt({ failure_stage: "execute", status: "failed" }));
    expect(facts.failureStage).toBe("执行阶段");
    expect(facts.failureStageKnown).toBe(true);
  });

  it("错误分类与失败阶段缺省时不产生虚构值", () => {
    const facts = attemptFailureFacts(attempt());
    expect(facts.errorClass).toBeUndefined();
    expect(facts.failureStage).toBeUndefined();
    expect(facts.errorClassKnown).toBe(false);
    expect(facts.failureStageKnown).toBe(false);
  });

  it("未知 error_class 保留原始文本，且不猜成已知分类", () => {
    const facts = attemptFailureFacts(attempt({ error_class: "quota_exhausted" }));
    expect(facts.errorClass).toBe("quota_exhausted");
    expect(facts.errorClassKnown).toBe(false);
    expect(attemptErrorClassLabel("quota_exhausted")).toBeUndefined();
  });

  it("未知 failure_stage 保留原始文本，且不猜成已知阶段", () => {
    const facts = attemptFailureFacts(attempt({ failure_stage: "commit" }));
    expect(facts.failureStage).toBe("commit");
    expect(facts.failureStageKnown).toBe(false);
    expect(attemptFailureStageLabel("commit")).toBe("commit");
  });

  it("缺失的 error_class / failure_stage 读取函数返回 undefined", () => {
    expect(attemptErrorClassLabel(undefined)).toBeUndefined();
    expect(attemptErrorClassLabel("")).toBeUndefined();
    expect(attemptFailureStageLabel(undefined)).toBeUndefined();
    expect(attemptFailureStageLabel("")).toBeUndefined();
  });

  it("分类映射只覆盖服务端已确认会写入的取值", () => {
    expect(ATTEMPT_ERROR_CLASS_LABEL).toEqual({
      agent_failure: "Agent 调用失败",
      network: "网络类失败",
      timeout: "执行超时",
      verification_failure: "验证未通过",
    });
    expect(ATTEMPT_FAILURE_STAGE_LABEL).toEqual({
      execute: "执行阶段",
      recover: "服务恢复阶段",
      verify: "验证阶段",
    });
  });
});

describe("会话归档状态标签", () => {
  it("覆盖 domain.SessionArchiveStatus 的全部取值", () => {
    expect(SESSION_ARCHIVE_STATUS_LABEL).toEqual({
      failed: "归档失败",
      not_attempted: "未归档",
      succeeded: "已归档",
    });
  });

  it("每个已核实的归档状态都映射到正确中文标签", () => {
    expect(sessionArchiveStatusLabel("not_attempted")).toBe("未归档");
    expect(sessionArchiveStatusLabel("succeeded")).toBe("已归档");
    expect(sessionArchiveStatusLabel("failed")).toBe("归档失败");
  });

  it("只有 succeeded 被显示为已归档，其余都不是", () => {
    expect(sessionArchiveStatusLabel("not_attempted")).not.toContain("已归档");
    expect(sessionArchiveStatusLabel("failed")).not.toContain("已归档");
  });

  it("未知归档状态原样显示，不被渲染成空或成功", () => {
    expect(sessionArchiveStatusLabel("archived_to_object_storage")).toBe(
      "archived_to_object_storage",
    );
    expect(sessionArchiveStatusLabel("archived_to_object_storage")).not.toBe("已归档");
  });

  it("缺省归档状态使用中性占位符，不误报成功", () => {
    expect(sessionArchiveStatusLabel(undefined)).toBe(SESSION_STATUS_MISSING_LABEL);
    expect(sessionArchiveStatusLabel("")).toBe(SESSION_STATUS_MISSING_LABEL);
    expect(SESSION_STATUS_MISSING_LABEL).toBe("未提供");
    expect(sessionArchiveStatusLabel(undefined)).not.toContain("已归档");
  });
});

describe("会话导出状态标签", () => {
  it("覆盖 domain.SessionExportStatus 的全部取值", () => {
    expect(SESSION_EXPORT_STATUS_LABEL).toEqual({
      failed: "导出失败",
      not_attempted: "未导出",
      succeeded: "已导出",
    });
  });

  it("每个已核实的导出状态都映射到正确中文标签", () => {
    expect(sessionExportStatusLabel("not_attempted")).toBe("未导出");
    expect(sessionExportStatusLabel("succeeded")).toBe("已导出");
    expect(sessionExportStatusLabel("failed")).toBe("导出失败");
  });

  it("未知导出状态原样显示", () => {
    expect(sessionExportStatusLabel("pending_upload")).toBe("pending_upload");
    expect(sessionExportStatusLabel("pending_upload")).not.toBe("已导出");
  });

  it("缺省导出状态使用中性占位符，不误报成功", () => {
    expect(sessionExportStatusLabel(undefined)).toBe(SESSION_STATUS_MISSING_LABEL);
    expect(sessionExportStatusLabel(undefined)).not.toContain("已导出");
  });
});

describe("归档状态与导出状态相互独立", () => {
  it("归档成功不会让导出状态变成已导出", () => {
    const failedExport = session({ archive_status: "succeeded", export_status: "failed" });
    expect(sessionArchiveStatusLabel(failedExport.archive_status)).toBe("已归档");
    expect(sessionExportStatusLabel(failedExport.export_status)).toBe("导出失败");
  });

  it("导出成功不会让归档状态变成已归档", () => {
    const failedArchive = session({ archive_status: "failed", export_status: "succeeded" });
    expect(sessionArchiveStatusLabel(failedArchive.archive_status)).toBe("归档失败");
    expect(sessionExportStatusLabel(failedArchive.export_status)).toBe("已导出");
  });

  it("两张映射表不共用语义：同名取值标签不同", () => {
    // 两表都含 succeeded / failed，但分别描述"归档"与"导出"，标签必须不同。
    expect(SESSION_ARCHIVE_STATUS_LABEL.succeeded).not.toBe(SESSION_EXPORT_STATUS_LABEL.succeeded);
    expect(SESSION_ARCHIVE_STATUS_LABEL.failed).not.toBe(SESSION_EXPORT_STATUS_LABEL.failed);
  });

  it("会话执行状态不参与归档与导出判定", () => {
    // 执行状态为 completed 的会话仍可能归档失败、导出未尝试。
    const completed = session({
      archive_status: "failed",
      export_status: "not_attempted",
      status: "completed",
    });
    expect(sessionStatusLabel(completed.status)).toBe("正常结束");
    expect(sessionArchiveStatusLabel(completed.archive_status)).toBe("归档失败");
    expect(sessionExportStatusLabel(completed.export_status)).toBe("未导出");
  });
});

describe("会话执行状态与运行结果标签", () => {
  it("覆盖 domain.SessionStatus 的全部取值", () => {
    expect(sessionStatusLabel("starting")).toBe("启动中");
    expect(sessionStatusLabel("running")).toBe("执行中");
    expect(sessionStatusLabel("completed")).toBe("正常结束");
    expect(sessionStatusLabel("failed")).toBe("失败");
    expect(sessionStatusLabel("cancelled")).toBe("已取消");
    expect(sessionStatusLabel("interrupted")).toBe("已中断");
  });

  it("执行状态正常结束不被表述为验收通过", () => {
    expect(sessionStatusLabel("completed")).not.toContain("通过");
    expect(sessionStatusLabel("completed")).not.toContain("成功");
  });

  it("未知执行状态原样显示，缺省使用中性占位符", () => {
    expect(sessionStatusLabel("suspended")).toBe("suspended");
    expect(sessionStatusLabel(undefined)).toBe(SESSION_STATUS_MISSING_LABEL);
  });

  it("运行结果标签覆盖全部取值，缺省时不渲染", () => {
    expect(sessionRunStatusLabel("completed")).toBe("运行正常结束");
    expect(sessionRunStatusLabel("failed")).toBe("运行失败");
    expect(sessionRunStatusLabel("cancelled")).toBe("运行已取消");
    expect(sessionRunStatusLabel("timed_out")).toBe("运行已超时");
    expect(sessionRunStatusLabel(undefined)).toBeUndefined();
    expect(sessionRunStatusLabel("")).toBeUndefined();
    expect(sessionRunStatusLabel("unknown_run")).toBe("unknown_run");
  });
});
