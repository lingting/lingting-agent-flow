import { afterEach, describe, expect, it, vi } from "vitest";

import {
  API_PREFIX,
  ApiError,
  apiPath,
  apiRequest,
  fetchHealth,
  HEALTH_PATH,
  SYSTEM_INFO_PATH,
} from "../../src/lib/api";
import { printArtifactDir } from "../internal/artifacts";
import { readProjectFile } from "../internal/project";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number; requestId?: string } = {}) {
  const headers = new Headers({ "Content-Type": "application/json" });
  if (init.requestId) headers.set("X-Request-Id", init.requestId);
  return new Response(JSON.stringify(body), { headers, status: init.status ?? 200 });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("API 路径构造", () => {
  it("接受同源相对路径", () => {
    expect(apiPath("/api/v1/health")).toBe("/api/v1/health");
  });

  it("拒绝不以斜杠开头的路径", () => {
    expect(() => apiPath("api/v1/health")).toThrow();
  });

  it("拒绝包含协议或主机名的路径", () => {
    expect(() => apiPath("http://127.0.0.1:26380/api/v1/health")).toThrow();
    expect(() => apiPath("//127.0.0.1/api/v1/health")).toThrow();
  });

  it("端点路径都以 API_PREFIX 开头，不含固定主机名与端口", () => {
    for (const path of [HEALTH_PATH, SYSTEM_INFO_PATH]) {
      expect(path.startsWith(`${API_PREFIX}/`)).toBe(true);
      expect(path).not.toContain("http");
      expect(path).not.toContain("localhost");
      expect(path).not.toContain("26380");
    }
  });

  it("源码中不出现硬编码的开发后端地址", () => {
    const client = readProjectFile("src/lib/api/client.ts");
    const endpoints = readProjectFile("src/lib/api/endpoints.ts");

    for (const source of [client, endpoints]) {
      expect(source).not.toContain("http://127.0.0.1");
      expect(source).not.toContain("localhost");
    }
  });
});

describe("API 请求成功路径", () => {
  it("返回解析后的 JSON 与请求描述", async () => {
    const payload = { status: "ok", checks: [], checked_at: "2025-01-01T00:00:00Z" };
    const spy = vi.fn(async () => jsonResponse(payload));
    vi.stubGlobal("fetch", spy);

    const result = await fetchHealth();

    expect(result.data).toEqual(payload);
    expect(result.request).toEqual({ method: "GET", path: HEALTH_PATH });
    expect(spy).toHaveBeenCalledTimes(1);
    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(HEALTH_PATH);
    expect((init.headers as Record<string, string>).Accept).toBe("application/json");
  });
});

describe("API 请求错误分类", () => {
  it("HTTP 非成功响应解析统一错误结构", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "接口不存在", request_id: "abc" } },
          { status: 404, requestId: "abc" },
        ),
      ),
    );

    const error = await fetchHealth().catch((thrown: unknown) => thrown);

    expect(error).toBeInstanceOf(ApiError);
    const apiError = error as ApiError;
    expect(apiError.kind).toBe("http");
    expect(apiError.status).toBe(404);
    expect(apiError.code).toBe("not_found");
    expect(apiError.message).toBe("接口不存在");
    expect(apiError.requestId).toBe("abc");
  });

  it("HTTP 非成功响应且响应体不是统一结构时仍保留状态码", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("bad gateway", { status: 502 })),
    );

    const error = (await fetchHealth().catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("http");
    expect(error.status).toBe(502);
    expect(error.code).toBeUndefined();
  });

  it("成功状态码但内容不是合法 JSON 时报 invalid_response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response("<html>not json</html>", {
            headers: { "Content-Type": "text/html" },
            status: 200,
          }),
      ),
    );

    const error = (await fetchHealth().catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("invalid_response");
    expect(error.message).toContain("JSON");
  });

  it("连接失败时报 network", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    const error = (await fetchHealth().catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("network");
    expect(error.isUnreachable).toBe(true);
  });

  it("超时时报 timeout，而不是业务错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_target: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );

    const error = (await apiRequest(HEALTH_PATH, { timeoutMs: 10 }).catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.kind).toBe("timeout");
    expect(error.isUnreachable).toBe(true);
  });

  it("调用方取消时报 aborted", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_target: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );
    const controller = new AbortController();
    const pending = apiRequest(HEALTH_PATH, { signal: controller.signal, timeoutMs: 0 });

    controller.abort();
    const error = (await pending.catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.kind).toBe("aborted");
    expect(error.isUnreachable).toBe(true);
  });

  it("已经取消的信号在发起请求前即被拒绝", async () => {
    const spy = vi.fn();
    vi.stubGlobal("fetch", spy);
    const controller = new AbortController();
    controller.abort();

    const error = (await apiRequest(HEALTH_PATH, { signal: controller.signal }).catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.kind).toBe("aborted");
    expect(spy).not.toHaveBeenCalled();
  });
});
