import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  defaultUsableVersion,
  ENVIRONMENTS_PATH,
  environmentVersionPath,
  fetchEnvironmentVersion,
  fetchEnvironments,
  startTask,
  TASKS_PATH,
  taskStartPath,
  usableEnvironmentVersions,
  type Environment,
  type EnvironmentVersion,
  type StartTaskResponse,
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

/** 一份统一启动响应样本，字段与 Go 端 `httpapi.StartTaskResponse` 一致 */
function sampleLaunch(overrides: Partial<StartTaskResponse> = {}): StartTaskResponse {
  return {
    container_prepared: true,
    plan_persisted: true,
    plan_steps: [
      {
        required_checks: ["build"],
        role_id: "developer",
        sequence: 1,
        status: "pending",
        step_id: "step-1",
        step_type: "develop",
        title: "实现登录接口",
      },
    ],
    stage: "workflow",
    task_id: "task-1",
    task_status: "developing",
    workflow_started: true,
    ...overrides,
  };
}

/** 一份环境版本样本 */
function sampleVersion(overrides: Partial<EnvironmentVersion> = {}): EnvironmentVersion {
  return {
    build_status: "succeeded",
    created_at: "2026-10-26T08:00:00Z",
    image_ref: "agentflow-language-go:v0001-abcdefabcdef",
    number: 1,
    usable: true,
    version: "v0001",
    ...overrides,
  };
}

/** 一份环境样本 */
function sampleEnvironment(overrides: Partial<Environment> = {}): Environment {
  return {
    latest_version: "v0001",
    name: "go",
    type: "language",
    versions: [sampleVersion()],
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("统一任务启动请求", () => {
  it("使用 POST /api/v1/tasks/{id}/start 并发送执行参数", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleLaunch(), { status: 202 }));
    vi.stubGlobal("fetch", spy);

    const result = await startTask("task-1", { prompt: "补充说明", timeout_seconds: 600 });

    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(`${TASKS_PATH}/task-1/start`);
    expect(target).toBe(taskStartPath("task-1"));
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual({ prompt: "补充说明", timeout_seconds: 600 });
    expect(result.data.workflow_started).toBe(true);
  });

  it("不发送容器、工作目录、步骤列表或角色覆盖", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleLaunch(), { status: 202 }));
    vi.stubGlobal("fetch", spy);

    await startTask("task-1");

    const [, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    const sent = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(sent).toEqual({});
    for (const forbidden of [
      "container_id",
      "work_dir",
      "steps",
      "role_id",
      "planner_role_id",
      "developer_role_id",
      "session_path",
    ]) {
      expect(sent).not.toHaveProperty(forbidden);
    }
  });

  it("把 202 当作成功响应而不是失败", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(sampleLaunch(), { status: 202 })),
    );

    const result = await startTask("task-1");

    expect(result.request.method).toBe("POST");
    expect(result.data.stage).toBe("workflow");
  });

  it("启动未完成时如实返回 workflow_started=false", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          sampleLaunch({
            container_prepared: false,
            failure: "远程不可达",
            failure_stage: "container",
            plan_persisted: false,
            plan_steps: [],
            stage: "container",
            task_status: "queued",
            workflow_started: false,
          }),
        ),
      ),
    );

    const { data } = await startTask("task-1");

    expect(data.workflow_started).toBe(false);
    expect(data.stage).toBe("container");
    expect(data.failure_stage).toBe("container");
    expect(data.failure).toBe("远程不可达");
    expect(data.run).toBeUndefined();
  });

  it("可选字段缺失时按真实类型解析而不崩溃", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          container_prepared: false,
          plan_persisted: false,
          plan_steps: [],
          stage: "validate",
          task_id: "task-1",
          task_status: "queued",
          workflow_started: false,
        }),
      ),
    );

    const { data } = await startTask("task-1");

    expect(data.container_id).toBeUndefined();
    expect(data.workspace_status).toBeUndefined();
    expect(data.workspace_commit).toBeUndefined();
    expect(data.run).toBeUndefined();
    expect(data.plan_steps).toEqual([]);
  });

  it("结构化错误响应被转换为 ApiError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            error: { code: "state_conflict", message: "任务已有未结束的工作流执行，不能重复启动" },
          },
          { status: 409 },
        ),
      ),
    );

    const error = (await startTask("task-1").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.kind).toBe("http");
    expect(error.status).toBe(409);
    expect(error.code).toBe("state_conflict");
    expect(error.message).toBe("任务已有未结束的工作流执行，不能重复启动");
  });

  it("前置条件缺失映射为 412 且保留服务端提示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "precondition_failed", message: "执行节点未通过环境预检" } },
          { status: 412 },
        ),
      ),
    );

    const error = (await startTask("task-1").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.status).toBe(412);
    expect(error.code).toBe("precondition_failed");
    expect(error.message).toBe("执行节点未通过环境预检");
  });

  it("网络错误报告为 network，不伪造成功", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    const error = (await startTask("task-1").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("network");
  });

  it("调用方超时会报告为 timeout", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_target: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            init.signal?.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );

    const error = (await startTask("task-1", {}, { timeoutMs: 10 }).catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.kind).toBe("timeout");
  });

  it("启动路径由任务标识派生并编码，不允许拼出额外路径段", () => {
    expect(taskStartPath("a b")).toBe(`${TASKS_PATH}/a%20b/start`);
    expect(() => taskStartPath("  ")).toThrow();
  });
});

describe("环境查询请求", () => {
  it("列表请求把 host_id 编码进查询串", async () => {
    const spy = vi.fn(async () => jsonResponse({ records: [sampleEnvironment()], total: 1 }));
    vi.stubGlobal("fetch", spy);

    const result = await fetchEnvironments("host 1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${ENVIRONMENTS_PATH}?host_id=host+1`);
    expect(result.data.total).toBe(1);
    expect(result.data.records[0]?.current_version).toBeUndefined();
  });

  it("执行节点为空时不发起请求", () => {
    const spy = vi.fn();
    vi.stubGlobal("fetch", spy);

    expect(() => fetchEnvironments("  ")).toThrow();
    expect(spy).not.toHaveBeenCalled();
  });

  it("单版本请求使用 type/name/versions/{number} 两段路径并带上 host_id", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleVersion()));
    vi.stubGlobal("fetch", spy);

    await fetchEnvironmentVersion("host-1", "language", "go", 2);

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${ENVIRONMENTS_PATH}/language/go/versions/2?host_id=host-1`);
    expect(target).toBe(`${environmentVersionPath("language", "go", 2)}?host_id=host-1`);
  });

  it("环境类型与名称按路径段编码，非法版本号直接拒绝", () => {
    expect(environmentVersionPath("base", "base", 1)).toBe(
      `${ENVIRONMENTS_PATH}/base/base/versions/1`,
    );
    expect(() => environmentVersionPath("language", "  ", 1)).toThrow();
    expect(() => environmentVersionPath("language", "go", 0)).toThrow();
    expect(() => environmentVersionPath("language", "go", 1.5)).toThrow();
  });

  it("环境查询错误保留状态码与业务提示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "执行节点不存在: host-1" } },
          { status: 404 },
        ),
      ),
    );

    const error = (await fetchEnvironments("host-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.status).toBe(404);
    expect(error.code).toBe("not_found");
    expect(error.message).toBe("执行节点不存在: host-1");
  });

  it("列表为空是合法结果，不是错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ records: [], total: 0 })),
    );

    const { data } = await fetchEnvironments("host-1");

    expect(data.records).toEqual([]);
    expect(data.total).toBe(0);
  });

  it("版本记录缺省可选字段时仍可解析", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          records: [
            {
              latest_version: "v0002",
              name: "go",
              type: "language",
              versions: [
                {
                  build_status: "failed",
                  created_at: "2026-10-26T09:00:00Z",
                  error_summary: "构建失败: 镜像核实未通过",
                  image_ref: "agentflow-language-go:v0002-abcdefabcdef",
                  number: 2,
                  usable: false,
                  version: "v0002",
                },
              ],
            },
          ],
          total: 1,
        }),
      ),
    );

    const { data } = await fetchEnvironments("host-1");
    const version = data.records[0]?.versions[0];

    expect(version?.usable).toBe(false);
    expect(version?.image_id).toBeUndefined();
    expect(version?.built_at).toBeUndefined();
    expect(version?.parent_version).toBeUndefined();
    expect(version?.error_summary).toBe("构建失败: 镜像核实未通过");
  });
});

describe("环境可用版本选择", () => {
  it("只返回 usable=true 的版本", () => {
    const environment = sampleEnvironment({
      versions: [
        sampleVersion({ number: 1, usable: true, version: "v0001" }),
        sampleVersion({ build_status: "failed", number: 2, usable: false, version: "v0002" }),
        sampleVersion({ build_status: "building", number: 3, usable: false, version: "v0003" }),
      ],
    });

    expect(usableEnvironmentVersions(environment).map((item) => item.version)).toEqual(["v0001"]);
  });

  it("current_version 优先，且不会退回到最新创建版本", () => {
    const environment = sampleEnvironment({
      current_version: "v0001",
      latest_version: "v0003",
      versions: [
        sampleVersion({ number: 1, usable: true, version: "v0001" }),
        sampleVersion({ build_status: "building", number: 3, usable: false, version: "v0003" }),
      ],
    });

    expect(defaultUsableVersion(environment)?.version).toBe("v0001");
  });

  it("没有可用版本时返回 undefined", () => {
    const environment = sampleEnvironment({
      latest_version: "v0002",
      versions: [
        sampleVersion({ build_status: "building", number: 2, usable: false, version: "v0002" }),
      ],
    });

    expect(defaultUsableVersion(environment)).toBeUndefined();
  });
});
