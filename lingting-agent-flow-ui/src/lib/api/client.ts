/**
 * AgentFlow HTTP API 的统一请求层。
 *
 * 约定：
 * - 所有请求都使用同源相对路径（`/api/v1/...`），不硬编码主机名、端口或开发地址；
 *   开发环境由 Vite+ 代理转发到 Go 服务，生产环境由同一个 Go 进程提供。
 * - 错误按类别区分：HTTP 业务错误、服务不可达、超时、主动取消、响应无法解析。
 *   不把所有失败压缩成"网络错误"，也不把超时/取消当成服务端业务错误。
 * - 只暴露结构化错误信息，不记录请求体或任何凭据。
 */

/** API 路径前缀，与 Go 端 httpapi.APIPrefix 保持一致 */
export const API_PREFIX = "/api/v1";

/** 请求默认超时（毫秒） */
const DEFAULT_TIMEOUT_MS = 10_000;

/** 错误类别：前端据此选择展示方式与恢复提示 */
export type ApiErrorKind =
  /** 服务返回了非成功状态码 */
  | "http"
  /** 无法建立连接，或连接被中断 */
  | "network"
  /** 超过客户端超时时间 */
  | "timeout"
  /** 调用方主动取消 */
  | "aborted"
  /** 响应内容不是合法的 JSON */
  | "invalid_response";

/** Go 端统一错误结构中的错误描述 */
export type ApiErrorBody = {
  code: string;
  message: string;
  request_id?: string;
};

/** 统一错误结构：`{ "error": { ... } }` */
export type ApiErrorResponse = {
  error: ApiErrorBody;
};

/** 请求层抛出的唯一错误类型，携带足够信息区分失败原因 */
export class ApiError extends Error {
  /** 错误类别 */
  readonly kind: ApiErrorKind;
  /** HTTP 状态码，非 HTTP 错误时为 undefined */
  readonly status?: number;
  /** 服务端返回的错误代码，非统一错误结构时为 undefined */
  readonly code?: string;
  /** 服务端返回的请求标识，便于与日志对应 */
  readonly requestId?: string;

  /** 服务端返回的响应体（已解析），用于展示健康检查等结构化详情 */
  readonly body?: unknown;
  constructor(
    kind: ApiErrorKind,
    message: string,
    options: {
      status?: number;
      code?: string;
      requestId?: string;
      cause?: unknown;
      body?: unknown;
    } = {},
  ) {
    super(message, { cause: options.cause });
    this.name = "ApiError";
    this.kind = kind;
    this.status = options.status;
    this.code = options.code;
    this.requestId = options.requestId;
    this.body = options.body;
  }

  /** 是否为服务不可达类错误（网络失败、超时或取消） */
  get isUnreachable() {
    return this.kind === "network" || this.kind === "timeout" || this.kind === "aborted";
  }
}

/** 单次请求选项 */
export type ApiRequestOptions = {
  /** 请求体，已由调用方序列化为字符串；不传表示没有请求体 */
  body?: string;
  /** 附加的请求头 */
  headers?: Record<string, string>;
  /** 请求方法，默认 GET */
  method?: string;
  /** 调用方取消信号 */
  signal?: AbortSignal;
  /** 覆盖默认超时（毫秒），传 0 表示不设超时 */
  timeoutMs?: number;
};

/** 由 API Client 发出的请求描述，便于测试与诊断 */
export type ApiRequest = {
  method: string;
  path: string;
};

/**
 * 构造 API 路径。`path` 必须以 `/` 开头且不包含协议或主机名，
 * 保证请求始终落在当前页面的来源上。
 */
export function apiPath(path: string): string {
  if (!path.startsWith("/")) {
    throw new Error(`API 路径必须以 / 开头: ${path}`);
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(path) || path.startsWith("//")) {
    throw new Error(`API 路径不能包含协议或主机名: ${path}`);
  }
  return path;
}

/** 判断响应体是否为统一错误结构 */
function isApiErrorResponse(value: unknown): value is ApiErrorResponse {
  if (typeof value !== "object" || value === null || !("error" in value)) {
    return false;
  }
  const body = (value as { error: unknown }).error;
  return (
    typeof body === "object" && body !== null && typeof (body as ApiErrorBody).code === "string"
  );
}

/** 把未知的响应内容解析成 JSON，失败时返回 undefined */
async function readJSON(response: Response): Promise<unknown> {
  const text = await response.text();
  if (text.trim() === "") {
    return undefined;
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}

/**
 * 发起一次 API 请求并返回解析后的 JSON。
 *
 * 失败一律抛出 `ApiError`，调用方不需要再判断 `response.ok`。
 */
export async function apiRequest<T>(
  path: string,
  options: ApiRequestOptions = {},
): Promise<{ data: T; request: ApiRequest }> {
  const target = apiPath(path);
  const method = options.method ?? "GET";
  const request: ApiRequest = { method, path: target };
  const controller = new AbortController();
  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const externalSignal = options.signal;
  let timedOut = false;

  // 调用方取消与超时都要中断 fetch，但两者的错误类别不同，需要分别记录。
  const abortFromCaller = () => controller.abort();
  if (externalSignal) {
    if (externalSignal.aborted) {
      throw new ApiError("aborted", "请求已取消");
    }
    externalSignal.addEventListener("abort", abortFromCaller, { once: true });
  }
  const timer =
    timeoutMs > 0
      ? setTimeout(() => {
          timedOut = true;
          controller.abort();
        }, timeoutMs)
      : undefined;

  try {
    let response: Response;
    try {
      response = await fetch(target, {
        body: options.body,
        headers: { Accept: "application/json", ...options.headers },
        method,
        signal: controller.signal,
      });
    } catch (error) {
      if (timedOut) {
        throw new ApiError("timeout", `请求超时（${timeoutMs} 毫秒）`, { cause: error });
      }
      if (externalSignal?.aborted) {
        throw new ApiError("aborted", "请求已取消", { cause: error });
      }
      throw new ApiError("network", "无法连接 AgentFlow 服务", { cause: error });
    }

    const requestId = response.headers.get("X-Request-Id") ?? undefined;
    const body = await readJSON(response);

    if (!response.ok) {
      const detail = isApiErrorResponse(body) ? body.error : undefined;
      throw new ApiError("http", detail?.message ?? `服务返回错误状态 ${response.status}`, {
        body,
        code: detail?.code,
        requestId: detail?.request_id ?? requestId,
        status: response.status,
      });
    }
    if (body === undefined) {
      throw new ApiError("invalid_response", "服务返回的内容不是合法 JSON", {
        requestId,
        status: response.status,
      });
    }
    return { data: body as T, request };
  } finally {
    if (timer !== undefined) {
      clearTimeout(timer);
    }
    externalSignal?.removeEventListener("abort", abortFromCaller);
  }
}
