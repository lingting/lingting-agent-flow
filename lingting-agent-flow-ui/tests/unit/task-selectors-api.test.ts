import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  fetchHosts,
  fetchRoles,
  HOSTS_PATH,
  ROLES_PATH,
  type Host,
  type Role,
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

/** 一份角色样本，字段与 Go 端 `httpapi.RoleResponse` 一致 */
function sampleRole(overrides: Partial<Role> = {}): Role {
  return {
    enabled: true,
    id: "planner-default",
    model: "group/free",
    name: "默认规划角色",
    runner: "pi",
    type: "planner",
    ...overrides,
  };
}

/** 一份执行节点样本，字段与 Go 端 `httpapi.HostResponse` 一致 */
function sampleHost(overrides: Partial<Host> = {}): Host {
  return {
    auth_method: "private_key",
    created_at: "2026-10-26T08:00:00Z",
    enabled: true,
    host: "10.0.0.11",
    host_key_algorithm: "ssh-ed25519",
    host_key_fingerprint: "SHA256:abc",
    id: "host-1",
    is_default: true,
    last_connect_status: "ok",
    name: "构建机",
    port: 22,
    private_key_path: "/home/user/.ssh/id_ed25519",
    updated_at: "2026-10-26T08:00:00Z",
    usable_for_remote_task: true,
    username: "dev",
    validation_generation: 1,
    validation_status: "normal",
    workspace_root: "/home/dev/agentflow",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("角色列表请求", () => {
  it("使用 GET /api/v1/roles", async () => {
    const spy = vi.fn(async () => jsonResponse({ records: [sampleRole()], total: 1 }));
    vi.stubGlobal("fetch", spy);

    const result = await fetchRoles();

    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(`${ROLES_PATH}`);
    expect(target).toBe("/api/v1/roles");
    expect(init.method ?? "GET").toBe("GET");
    expect(init.body).toBeUndefined();
    expect(result.request).toEqual({ method: "GET", path: "/api/v1/roles" });
  });

  it("解析 records、total 与每个角色的 enabled", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          records: [
            sampleRole({ id: "planner-1", name: "规划 A" }),
            sampleRole({ enabled: false, id: "planner-2", name: "规划 B" }),
            sampleRole({ id: "dev-1", name: "开发 A", type: "developer" }),
            sampleRole({ id: "val-1", name: "验证 A", type: "validator" }),
          ],
          total: 4,
        }),
      ),
    );

    const { data } = await fetchRoles();

    expect(data.total).toBe(4);
    expect(data.records).toHaveLength(4);
    expect(data.records.map((role) => role.type)).toEqual([
      "planner",
      "planner",
      "developer",
      "validator",
    ]);
    expect(data.records[0]?.enabled).toBe(true);
    expect(data.records[1]?.enabled).toBe(false);
    // 角色接口不公开的字段不应出现在类型与数据里
    expect(data.records[0]).not.toHaveProperty("system_prompt");
  });

  it("空列表是合法结果，不是错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ records: [], total: 0 })),
    );

    const { data } = await fetchRoles();

    expect(data.records).toEqual([]);
    expect(data.total).toBe(0);
  });

  it("服务端 503 保留业务提示而不是伪装成空列表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "internal_error", message: "角色服务未初始化" } },
          { status: 503 },
        ),
      ),
    );

    const error = (await fetchRoles().catch((thrown: unknown) => thrown)) as ApiError;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.kind).toBe("http");
    expect(error.status).toBe(503);
    expect(error.message).toBe("角色服务未初始化");
  });

  it("网络错误报告为 network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    const error = (await fetchRoles().catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("network");
  });

  it("调用方取消报告为 aborted", async () => {
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

    const controller = new AbortController();
    const pending = fetchRoles({ signal: controller.signal }).catch(
      (thrown: unknown) => thrown as ApiError,
    );
    controller.abort();
    const error = (await pending) as ApiError;

    expect(error.kind).toBe("aborted");
  });
});

describe("执行节点列表请求", () => {
  it("使用 GET /api/v1/hosts 并返回可用于选择器的真实字段", async () => {
    const spy = vi.fn(async () =>
      jsonResponse({
        records: [
          sampleHost(),
          sampleHost({
            enabled: false,
            id: "host-2",
            name: "停用节点",
            usable_for_remote_task: false,
          }),
          sampleHost({
            id: "host-3",
            name: "待校验节点",
            usable_for_remote_task: false,
            validation_message: "环境预检未通过",
            validation_status: "abnormal",
          }),
        ],
        total: 3,
      }),
    );
    vi.stubGlobal("fetch", spy);

    const { data } = await fetchHosts();

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(HOSTS_PATH);
    expect(data.total).toBe(3);
    const [first, second, third] = data.records;
    expect(first?.id).toBe("host-1");
    expect(first?.name).toBe("构建机");
    expect(first?.host).toBe("10.0.0.11");
    expect(first?.port).toBe(22);
    expect(first?.enabled).toBe(true);
    expect(first?.usable_for_remote_task).toBe(true);
    expect(second?.enabled).toBe(false);
    expect(second?.usable_for_remote_task).toBe(false);
    expect(third?.validation_status).toBe("abnormal");
    expect(third?.validation_message).toBe("环境预检未通过");
  });

  it("空列表是合法结果", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ records: [], total: 0 })),
    );

    const { data } = await fetchHosts();

    expect(data.records).toEqual([]);
    expect(data.total).toBe(0);
  });

  it("服务端 500 保留业务提示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "internal_error", message: "服务内部错误" } },
          { status: 500 },
        ),
      ),
    );

    const error = (await fetchHosts().catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("http");
    expect(error.status).toBe(500);
    expect(error.message).toBe("服务内部错误");
  });
});
