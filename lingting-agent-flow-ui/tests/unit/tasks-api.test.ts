import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  cancelTask,
  createTask,
  deleteTask,
  fetchTask,
  fetchTasks,
  TASKS_PATH,
  taskCancelPath,
  taskPath,
  type CreateTaskRequest,
  type Task,
} from "../../src/lib/api";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/** 一份任务响应样本，字段与 Go 端一致 */
function sampleTask(overrides: Partial<Task> = {}): Task {
  return {
    cancel_requested: false,
    created_at: "2025-01-01T00:00:00Z",
    description: "接入真实接口",
    id: "task-1",
    name: "实现登录",
    project_id: "",
    status: "queued",
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

/** 一份合法的建任务请求体 */
function sampleCreateRequest(): CreateTaskRequest {
  return {
    base_branch: "main",
    description: "接入真实接口",
    developer_role_id: "developer",
    environment: "go",
    host_id: "host-1",
    name: "实现登录",
    planner_role_id: "planner",
    repository_url: "ssh://git.example.com/team/repo",
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("任务端点路径", () => {
  it("集合路径挂在 /api/v1 下", () => {
    expect(TASKS_PATH).toBe("/api/v1/tasks");
  });

  it("单个任务与取消路径由任务标识派生", () => {
    expect(taskPath("abc")).toBe("/api/v1/tasks/abc");
    expect(taskCancelPath("abc")).toBe("/api/v1/tasks/abc/cancel");
  });

  it("任务标识会被编码，不允许拼出额外路径段", () => {
    expect(taskPath("a b")).toBe("/api/v1/tasks/a%20b");
    expect(() => taskPath("  ")).toThrow();
  });
});

describe("任务列表请求", () => {
  it("分页与状态筛选都编码进查询串", async () => {
    const spy = vi.fn(async () =>
      jsonResponse({ current: 2, records: [sampleTask()], size: 2, total: 3 }),
    );
    vi.stubGlobal("fetch", spy);

    const result = await fetchTasks({ current: 2, size: 2, status: ["queued", "failed"] });

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}?current=2&size=2&status=queued&status=failed`);
    expect(result.data.total).toBe(3);
    expect(result.data.records).toHaveLength(1);
  });

  it("没有查询条件时不追加空查询串", async () => {
    const spy = vi.fn(async () => jsonResponse({ current: 1, records: [], size: 20, total: 0 }));
    vi.stubGlobal("fetch", spy);

    await fetchTasks();

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(TASKS_PATH);
  });
});

describe("任务详情请求", () => {
  it("404 时抛出带状态码的 ApiError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ error: { code: "not_found", message: "任务不存在: abc" } }, { status: 404 }),
      ),
    );

    const error = (await fetchTask("abc").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.kind).toBe("http");
    expect(error.status).toBe(404);
    expect(error.code).toBe("not_found");
    expect(error.message).toContain("任务不存在");
  });
});

describe("创建任务请求", () => {
  it("使用 POST 并发送 JSON 请求体", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleTask(), { status: 201 }));
    vi.stubGlobal("fetch", spy);

    const body = sampleCreateRequest();
    const result = await createTask(body);

    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(TASKS_PATH);
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual(body);
    expect(result.data.id).toBe("task-1");
  });

  it("请求体不包含状态、开始时间或完成时间", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleTask(), { status: 201 }));
    vi.stubGlobal("fetch", spy);

    await createTask(sampleCreateRequest());

    const [, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    const sent = JSON.parse(init.body as string) as Record<string, unknown>;
    for (const forbidden of ["status", "started_at", "finished_at", "created_at"]) {
      expect(sent).not.toHaveProperty(forbidden);
    }
  });

  it("参数错误时保留服务端业务提示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "invalid_request", message: "任务名称不能为空" } },
          { status: 400 },
        ),
      ),
    );

    const error = (await createTask(sampleCreateRequest()).catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.kind).toBe("http");
    expect(error.status).toBe(400);
    expect(error.code).toBe("invalid_request");
    expect(error.message).toBe("任务名称不能为空");
  });
});

describe("取消任务请求", () => {
  it("使用 POST 且不发送请求体", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleTask({ status: "cancelled" })));
    vi.stubGlobal("fetch", spy);

    const result = await cancelTask("abc");

    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(taskCancelPath("abc"));
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(result.data.status).toBe("cancelled");
  });

  it("状态冲突时抛出 409 错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ error: { code: "state_conflict", message: "任务已结束" } }, { status: 409 }),
      ),
    );

    const error = (await cancelTask("abc").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.status).toBe(409);
    expect(error.code).toBe("state_conflict");
  });
});

describe("删除任务请求", () => {
  it("接受 204 空响应体", async () => {
    const spy = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", spy);

    const result = await deleteTask("abc");

    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(taskPath("abc"));
    expect(init.method).toBe("DELETE");
    expect(result.request).toEqual({ method: "DELETE", path: "/api/v1/tasks/abc" });
  });

  it("状态冲突时抛出 409 错误而不是 invalid_response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "state_conflict", message: "执行中的任务不能删除" } },
          { status: 409 },
        ),
      ),
    );

    const error = (await deleteTask("abc").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("http");
    expect(error.status).toBe(409);
    expect(error.code).toBe("state_conflict");
    expect(error.message).toBe("执行中的任务不能删除");
  });

  it("连接失败时报 network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    const error = (await deleteTask("abc").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("network");
  });
});
