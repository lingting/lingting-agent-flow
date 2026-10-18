import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchHealth, type HealthResponse } from "@/lib/api";

/** 连接状态：只描述 AgentFlow HTTP API 的可用性 */
export type ConnectionStatus = "checking" | "connected" | "disconnected";

/** 连接状态快照 */
export type ConnectionState = {
  /** 当前状态；首次检查完成前恒为 checking，不默认显示已连接 */
  status: ConnectionStatus;
  /** 最近一次成功的健康检查结果 */
  health?: HealthResponse;
  /** 失败原因，仅 disconnected 时存在 */
  error?: ApiError;
  /** 最近一次检查完成时间 */
  checkedAt?: Date;
};

/** 可选配置 */
export type UseApiConnectionOptions = {
  /** 轮询间隔（毫秒），0 表示只在挂载与页面恢复时检查 */
  pollIntervalMs?: number;
};

/** 默认轮询间隔：健康检查很轻，30 秒足以反映服务状态变化 */
const DEFAULT_POLL_INTERVAL_MS = 30_000;

/** 首次检查前的状态 */
const INITIAL_STATE: ConnectionState = { status: "checking" };

/** 把未知错误归一化为 ApiError，保证调用方只需要处理一种错误类型 */
function toApiError(error: unknown) {
  if (error instanceof ApiError) {
    return error;
  }
  return new ApiError("network", "无法连接 AgentFlow 服务", { cause: error });
}

/**
 * 维护前端与 AgentFlow HTTP API 的连接状态。
 *
 * - 首次检查完成前状态为 `checking`，不假定已连接。
 * - 挂载、窗口重新获得焦点、页面从 bfcache 恢复时各检查一次。
 * - 可选定时轮询；卸载或重新检查时取消仍在进行的请求，避免状态被过期响应覆盖。
 */
export function useApiConnection(options: UseApiConnectionOptions = {}) {
  const pollIntervalMs = options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS;
  const [state, setState] = useState<ConnectionState>(INITIAL_STATE);
  const controllerRef = useRef<AbortController | null>(null);

  const check = useCallback(async () => {
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    setState((previous) => ({ ...previous, status: "checking" }));
    try {
      const { data } = await fetchHealth({ signal: controller.signal });
      if (controller.signal.aborted) return;
      setState({ checkedAt: new Date(), health: data, status: "connected" });
    } catch (error) {
      if (controller.signal.aborted) return;
      const apiError = toApiError(error);
      if (apiError.kind === "aborted") return;
      setState({ checkedAt: new Date(), error: apiError, status: "disconnected" });
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null;
      }
    }
  }, []);

  useEffect(() => {
    void check();
    const handleVisibility = () => {
      if (document.visibilityState === "visible") {
        void check();
      }
    };
    const handlePageShow = (event: PageTransitionEvent) => {
      if (event.persisted) {
        void check();
      }
    };
    window.addEventListener("focus", handleVisibility);
    document.addEventListener("visibilitychange", handleVisibility);
    window.addEventListener("pageshow", handlePageShow);
    const timer = pollIntervalMs > 0 ? setInterval(() => void check(), pollIntervalMs) : undefined;
    return () => {
      window.removeEventListener("focus", handleVisibility);
      document.removeEventListener("visibilitychange", handleVisibility);
      window.removeEventListener("pageshow", handlePageShow);
      if (timer !== undefined) {
        clearInterval(timer);
      }
      controllerRef.current?.abort();
      controllerRef.current = null;
    };
  }, [check, pollIntervalMs]);

  return { check, state };
}
