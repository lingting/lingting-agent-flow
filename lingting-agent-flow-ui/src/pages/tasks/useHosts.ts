/**
 * 创建任务表单的执行节点查询。
 *
 * 数据来自 `GET /api/v1/hosts`：加载中、查询失败、空列表是三种不同状态，
 * 查询失败不会被伪装成空列表，也不会使用硬编码节点继续创建任务。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchHosts, type Host } from "@/lib/api";

/** 执行节点查询状态 */
export type HostQueryState =
  /** 首次加载尚未开始或正在加载 */
  | { status: "loading"; hosts: Host[] }
  /** 查询成功；列表可能为空，表示服务端还没有登记任何节点 */
  | { status: "ready"; hosts: Host[] }
  /** 查询失败；错误来自接口，不是空列表 */
  | { status: "error"; hosts: Host[]; error: string };

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
export function describeHostQueryError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "执行节点查询超时，请稍后重试。";
    if (error.kind === "aborted") return "执行节点查询已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/**
 * 查询全部执行节点。
 *
 * 返回值的 `status` 是唯一的状态判据：调用方不得把 `error` 当作空列表，
 * 也不得在 `loading` / `error` 时提交一个前端猜测的节点。
 */
export function useHosts(): HostQueryState & { reload: () => void } {
  const [state, setState] = useState<HostQueryState>({ hosts: [], status: "loading" });
  const [attempt, setAttempt] = useState(0);
  /** 只接受最近一次请求的结果，避免重试后旧响应覆盖新状态 */
  const requestRef = useRef(0);

  const reload = useCallback(() => {
    setState({ hosts: [], status: "loading" });
    setAttempt((value) => value + 1);
  }, []);

  useEffect(() => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    fetchHosts({ signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ hosts: data.records, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ error: describeHostQueryError(cause), hosts: [], status: "error" });
      });
    return () => controller.abort();
  }, [attempt]);

  return { ...state, reload };
}
