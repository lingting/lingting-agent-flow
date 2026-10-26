import { describe, expect, it } from "vitest";

import type { StartTaskResponse, Task } from "../../src/lib/api";
import {
  canStartTask,
  describeLaunchResult,
  launchStageMeta,
  workspaceReady,
  workspaceStatusLabel,
} from "../../src/pages/tasks/launch";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份任务样本 */
function task(overrides: Partial<Task> = {}): Task {
  return {
    cancel_requested: false,
    created_at: "2025-01-01T00:00:00Z",
    description: "描述",
    id: "task-1",
    name: "任务",
    project_id: "",
    status: "queued",
    status_finished: false,
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

/** 一份启动响应样本 */
function launch(overrides: Partial<StartTaskResponse> = {}): StartTaskResponse {
  return {
    container_prepared: true,
    plan_persisted: true,
    plan_steps: [],
    stage: "workflow",
    task_id: "task-1",
    task_status: "developing",
    workflow_started: true,
    ...overrides,
  };
}

describe("统一启动的准入判定", () => {
  it("与服务端 AllowsExplicitLaunch 一致：允许 queued 与可继续推进的状态", () => {
    for (const status of [
      "queued",
      "preparing",
      "planned",
      "retrying",
      "paused",
      "recovery_required",
    ] as const) {
      expect(canStartTask(task({ status }))).toBe(true);
    }
  });

  it("failed 不由统一启动隐式承担", () => {
    expect(canStartTask(task({ status: "failed", status_finished: true }))).toBe(false);
  });

  it("已结束或执行中的状态不允许启动", () => {
    for (const status of ["developing", "verifying", "saving", "completed", "cancelled"] as const) {
      expect(canStartTask(task({ status }))).toBe(false);
    }
  });

  it("已请求取消的任务不允许启动", () => {
    expect(canStartTask(task({ cancel_requested: true, status: "queued" }))).toBe(false);
  });
});

describe("启动阶段展示", () => {
  it("覆盖 Go 端全部阶段取值", () => {
    for (const stage of ["validate", "container", "workspace", "plan", "workflow"] as const) {
      const meta = launchStageMeta(stage);
      expect(meta.label.length).toBeGreaterThan(0);
      expect(meta.description.length).toBeGreaterThan(0);
    }
  });

  it("未知阶段原样展示，不猜测语义", () => {
    expect(launchStageMeta("unknown_stage").label).toBe("unknown_stage");
  });

  it("只有 succeeded 与 skipped 表示工作区就绪", () => {
    expect(workspaceReady("succeeded")).toBe(true);
    expect(workspaceReady("skipped")).toBe(true);
    for (const status of ["pending", "running", "failed", "cancelled", "timed_out", "uncertain"]) {
      expect(workspaceReady(status)).toBe(false);
    }
    expect(workspaceReady(undefined)).toBe(false);
  });

  it("工作区状态缺失时显示占位符而不是成功", () => {
    expect(workspaceStatusLabel(undefined)).toBe("—");
    expect(workspaceStatusLabel("failed")).toBe("失败");
    expect(workspaceStatusLabel("succeeded")).toBe("已就绪");
  });
});

describe("启动结论", () => {
  it("workflow_started=true 才算启动成功", () => {
    const outcome = describeLaunchResult(launch());

    expect(outcome.kind).toBe("started");
    expect(outcome.title).toContain("已启动");
    expect(outcome.description).toContain("工作流已启动");
  });

  it("workflow_started=false 不显示为启动成功", () => {
    const outcome = describeLaunchResult(
      launch({
        container_prepared: false,
        failure: "执行节点未通过环境预检",
        failure_stage: "validate",
        plan_persisted: false,
        stage: "validate",
        task_status: "queued",
        workflow_started: false,
      }),
    );

    expect(outcome.kind).toBe("blocked");
    expect(outcome.title).toContain("未启动");
    expect(outcome.description).toContain("执行节点未通过环境预检");
    expect(outcome.description).toContain("任务状态：queued");
    expect(outcome.description).not.toContain("工作流已启动");
  });

  it("计划已持久化但工作流未启动时仍按未启动展示", () => {
    const outcome = describeLaunchResult(
      launch({
        failure: "启动工作流失败",
        failure_stage: "workflow",
        plan_persisted: true,
        stage: "workflow",
        task_status: "planned",
        workflow_started: false,
      }),
    );

    expect(outcome.kind).toBe("blocked");
    expect(outcome.description).toContain("已持久化");
    expect(outcome.description).toContain("启动工作流失败");
  });

  it("容器已准备同样不替代 workflow_started", () => {
    const outcome = describeLaunchResult(
      launch({
        container_prepared: true,
        failure: "Planner 调用超时",
        failure_stage: "plan",
        plan_persisted: false,
        stage: "plan",
        task_status: "preparing",
        workflow_started: false,
      }),
    );

    expect(outcome.kind).toBe("blocked");
    expect(outcome.description).toContain("本次已创建");
    expect(outcome.description).toContain("Planner 调用超时");
  });

  it("失败摘要缺失时给出明确说明而不是空字符串", () => {
    const outcome = describeLaunchResult(
      launch({ stage: "container", task_status: "queued", workflow_started: false }),
    );

    expect(outcome.kind).toBe("blocked");
    expect(outcome.description).toContain("服务端未启动工作流");
  });

  it("展示真实任务状态与阶段，不从阶段推断成功", () => {
    const outcome = describeLaunchResult(
      launch({ stage: "workspace", task_status: "recovery_required", workflow_started: false }),
    );

    expect(outcome.description).toContain("初始化工作区");
    expect(outcome.description).toContain("任务状态：recovery_required");
  });
});
