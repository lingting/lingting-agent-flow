/**
 * 设置中心「环境管理」的执行节点查询。
 *
 * 数据来自 `GET /api/v1/hosts`：节点是环境写操作的唯一目标来源，因此这里不猜默认节点、
 * 不在加载失败时给出空列表。是否允许发起写操作由服务端计算的 `usable_for_remote_task`
 * 决定，前端只用于提示。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { fetchHosts, type Host } from "@/lib/api";
import { describeEnvironmentError } from "@/pages/settings/settings-environments";

/** 执行节点查询状态 */
export type HostQueryState =
  /** 首次加载或重新加载中 */
  | { status: "loading"; hosts: Host[] }
  /** 查询成功；列表可能为空，表示服务端还没有登记任何执行节点 */
  | { status: "ready"; hosts: Host[] }
  /** 查询失败；错误来自接口，不是空列表 */
  | { status: "error"; hosts: Host[]; error: string };

/** 查询全部执行节点，并提供重新加载入口 */
export function useEnvironmentHosts(): HostQueryState & { reload: () => void } {
  const [state, setState] = useState<HostQueryState>({ hosts: [], status: "loading" });
  const [attempt, setAttempt] = useState(0);
  /** 只接受最近一次请求的结果，避免重试后旧响应覆盖新状态 */
  const requestRef = useRef(0);

  const reload = useCallback(() => {
    setAttempt((value) => value + 1);
  }, []);

  useEffect(() => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    // 刷新时保留上一份真实节点数据：清空会让已选中节点的详情面板被卸载，
    // 从而丢失刚收到的构建结论。
    setState((previous) => ({ hosts: previous.hosts, status: "loading" }));
    fetchHosts({ signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ hosts: data.records, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        // 失败时保留上一份真实数据，由界面展示错误：刷新失败不等于"没有执行节点"。
        setState((previous) => ({
          error: describeEnvironmentError(cause),
          hosts: previous.hosts,
          status: "error",
        }));
      });
    return () => controller.abort();
  }, [attempt]);

  return { ...state, reload };
}
