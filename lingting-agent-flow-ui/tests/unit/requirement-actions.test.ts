import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  deleteRequirement,
  REQUIREMENTS_PATH,
  requirementPath,
  type ConfirmedTask,
  type DeleteRequirementResponse,
} from "../../src/lib/api";
import {
  describeRequirementActionError,
  describeRequirementDeletionAvailability,
  describeRequirementTasksForDeletion,
  isTaskDeletableWithRequirement,
  isTaskStoppable,
  isTaskStopping,
  isUnknownRequirementActionOutcome,
  stoppableTasks,
} from "../../src/pages/requirements/requirementActions";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/** 构造一个任务记录 */
function sampleTask(overrides: Partial<ConfirmedTask> = {}): ConfirmedTask {
  return {
    acceptance_criteria: ["可以独立验收"],
    base_ref: "main",
    created_at: "2026-11-08T02:00:00Z",
    goal: "实现任务",
    id: "task-1",
    requirement_id: "req-1",
    requirement_sequence: 1,
    repository_path: "ssh://git.example.com/team/demo",
    status: "queued",
    ...overrides,
  };
}

/** 构造删除成功响应 */
function deletedResponse(): DeleteRequirementResponse {
  return {
    deleted_at: "2026-11-08T03:00:00Z",
    deleted_task_count: 2,
    requirement_id: "req-1",
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("需求删除接口", () => {
  it("删除走需求路径的 DELETE，不带请求体", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(deletedResponse()));
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await deleteRequirement("req-1");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`${REQUIREMENTS_PATH}/req-1`);
    expect(url).toBe(requirementPath("req-1"));
    expect(init.method).toBe("DELETE");
    expect(init.body).toBeUndefined();
    expect(data).toEqual(deletedResponse());
  });

  it("保留服务端返回的真实任务数量", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ ...deletedResponse(), deleted_task_count: 7 })),
    );
    const { data } = await deleteRequirement("req-1");
    expect(data.deleted_task_count).toBe(7);
  });

  it("409 冲突不会被包装成成功", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "state_conflict", message: "任务仍在执行" } },
          { status: 409 },
        ),
      ),
    );
    await expect(deleteRequirement("req-1")).rejects.toBeInstanceOf(ApiError);
  });
});

describe("需求删除状态判定", () => {
  it("只有 queued / completed / failed / cancelled 允许随需求删除", () => {
    for (const status of ["queued", "completed", "failed", "cancelled"]) {
      expect(isTaskDeletableWithRequirement(status)).toBe(true);
    }
    for (const status of [
      "preparing",
      "planning",
      "planned",
      "developing",
      "verifying",
      "saving",
      "retrying",
      "cancelling",
      "paused",
      "recovery_required",
      "mystery",
    ]) {
      expect(isTaskDeletableWithRequirement(status)).toBe(false);
    }
  });

  it("停止只作用于仍需收敛的任务，cancelling 不重复停止", () => {
    for (const status of [
      "preparing",
      "planning",
      "planned",
      "developing",
      "verifying",
      "saving",
      "retrying",
    ]) {
      expect(isTaskStoppable(status)).toBe(true);
    }
    // 停止中的任务不再提供重复提交，其余状态不需要停止
    expect(isTaskStoppable("cancelling")).toBe(false);
    expect(isTaskStopping("cancelling")).toBe(true);
    for (const status of [
      "queued",
      "completed",
      "failed",
      "cancelled",
      "paused",
      "recovery_required",
      "mystery",
    ]) {
      expect(isTaskStoppable(status)).toBe(false);
    }
  });

  it("stoppableTasks 只返回需要停止的任务", () => {
    const tasks = [
      sampleTask({ id: "task-1", status: "completed" }),
      sampleTask({ id: "task-2", status: "developing", requirement_sequence: 2 }),
      sampleTask({ id: "task-3", status: "cancelling", requirement_sequence: 3 }),
      sampleTask({ id: "task-4", status: "queued", requirement_sequence: 4 }),
    ];
    expect(stoppableTasks(tasks).map((task) => task.id)).toEqual(["task-2"]);
  });

  it("任务全部安全时删除可用", () => {
    const tasks = [
      sampleTask({ id: "task-1", status: "failed" }),
      sampleTask({ id: "task-2", status: "queued", requirement_sequence: 2 }),
    ];
    expect(describeRequirementDeletionAvailability(tasks)).toEqual({ available: true });
  });

  it("执行中、停止中、待核验与未知状态给出不同的禁用原因", () => {
    const developing = describeRequirementDeletionAvailability([
      sampleTask({ id: "task-1", status: "developing" }),
    ]);
    expect(developing.available).toBe(false);
    expect(developing.reason).toContain("仍在执行（developing）");
    expect(developing.reason).toContain("先停止任务并等待其真实收敛");

    const stopping = describeRequirementDeletionAvailability([
      sampleTask({ id: "task-1", status: "cancelling" }),
    ]);
    expect(stopping.reason).toContain("正在停止（cancelling）");

    const paused = describeRequirementDeletionAvailability([
      sampleTask({ id: "task-1", status: "paused" }),
    ]);
    expect(paused.reason).toContain("需要人工核验现场");

    const unknown = describeRequirementDeletionAvailability([
      sampleTask({ id: "task-1", status: "mystery" }),
    ]);
    expect(unknown.reason).toContain("服务端未确认该状态可以随需求删除");
  });

  it("删除前摘要按需求内顺序列出标识与状态", () => {
    const tasks = [
      sampleTask({ id: "task-1", status: "completed", requirement_sequence: 1 }),
      sampleTask({ id: "task-2", status: "failed", requirement_sequence: 2 }),
    ];
    expect(describeRequirementTasksForDeletion(tasks)).toEqual([
      "1. task-1（completed）",
      "2. task-2（failed）",
    ]);
  });
});

describe("需求操作错误分类", () => {
  it("409 展示服务端拒绝原因", () => {
    const error = new ApiError("http", "任务仍在执行", {
      code: "state_conflict",
      status: 409,
    });
    expect(describeRequirementActionError(error)).toBe("操作被拒绝：任务仍在执行");
  });

  it("404 提示需求不存在或已删除", () => {
    const error = new ApiError("http", "需求不存在", { code: "not_found", status: 404 });
    expect(describeRequirementActionError(error)).toContain("不存在或已被删除");
  });

  it("503 提示删除能力不可用", () => {
    const error = new ApiError("http", "服务内部错误", {
      code: "internal_error",
      status: 503,
    });
    expect(describeRequirementActionError(error)).toContain("需求删除能力不可用");
  });

  it("网络中断与超时都表示结果未知", () => {
    const network = new ApiError("network", "连接中断");
    const timeout = new ApiError("timeout", "请求超时");
    expect(describeRequirementActionError(network)).toContain("结果未知");
    expect(describeRequirementActionError(timeout)).toContain("请重新读取需求状态确认");
    expect(isUnknownRequirementActionOutcome(network)).toBe(true);
    expect(isUnknownRequirementActionOutcome(timeout)).toBe(true);
  });

  it("业务拒绝不算结果未知", () => {
    const conflict = new ApiError("http", "冲突", { code: "state_conflict", status: 409 });
    expect(isUnknownRequirementActionOutcome(conflict)).toBe(false);
  });
});
