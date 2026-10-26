/**
 * 任务创建表单的环境查询。
 *
 * 环境数据全部来自 `GET /api/v1/environments?host_id=…`：
 *   - 执行节点未填写时不发起请求（`host_id` 是唯一节点定位方式，缺省会得到 400）；
 *   - 执行节点变化时重新查询，并清空与旧节点关联的环境与版本选择；
 *   - 加载中、查询失败、空列表是三种不同状态，查询失败不会被伪装成空列表。
 */
import { useEffect, useState } from "react";

import { ApiError, fetchEnvironments, type Environment } from "@/lib/api";

/** 执行节点手工输入时的查询去抖时长（毫秒） */
const HOST_QUERY_DEBOUNCE_MS = 350;

/** 环境查询状态 */
export type EnvironmentQueryState =
  /** 执行节点尚未填写，未发起请求 */
  | { status: "idle"; environments: Environment[] }
  /** 正在查询当前节点上的环境 */
  | { status: "loading"; environments: Environment[] }
  /** 查询成功；列表可能为空，表示该节点还没有任何环境版本 */
  | { status: "ready"; environments: Environment[] }
  /** 查询失败；错误来自接口，不是空列表 */
  | { status: "error"; environments: Environment[]; error: string };

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "环境查询超时，请稍后重试。";
    if (error.kind === "aborted") return "环境查询已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/**
 * 按执行节点查询环境。
 *
 * 返回值的 `status` 是唯一的状态判据：调用方不得把 `error` 当作空列表，也不得在
 * `idle` / `loading` 时提交一个前端猜测的环境配置。
 */
export function useEnvironments(hostID?: string): EnvironmentQueryState {
  const trimmed = hostID?.trim() ?? "";
  const [state, setState] = useState<EnvironmentQueryState>({
    environments: [],
    status: "idle",
  });

  useEffect(() => {
    if (!trimmed) {
      // 没有执行节点就没有可查询的环境：不发请求，也不保留上一个节点的数据。
      setState({ environments: [], status: "idle" });
      return;
    }
    // 执行节点是手工输入的：等输入稳定后再查询，避免为每个中间值发出必然 404 的请求。
    const controller = new AbortController();
    setState({ environments: [], status: "loading" });
    const timer = setTimeout(() => {
      fetchEnvironments(trimmed, { signal: controller.signal })
        .then(({ data }) => {
          if (controller.signal.aborted) return;
          setState({ environments: data.records, status: "ready" });
        })
        .catch((cause: unknown) => {
          if (controller.signal.aborted) return;
          setState({ environments: [], error: describeError(cause), status: "error" });
        });
    }, HOST_QUERY_DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [trimmed]);

  return state;
}
