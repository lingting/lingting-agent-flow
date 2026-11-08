/**
 * 项目执行状态查询与开始 / 停止动作。
 *
 * 约定：
 * - 状态、任务顺序与阻塞原因全部来自服务端；页面不使用按钮本地状态推断"已启动"或
 *   "已停止"；
 * - 开始 / 停止提交后一律重新读取服务端状态，即使请求本身失败；
 * - 网络中断时显示"结果未知"并重新查询，不自动重复提交有副作用的请求；
 * - `409`、`412` 与服务不可用分别展示，不折叠成同一句"操作失败"。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  cancelProjectExecution,
  fetchProjectExecution,
  startProjectExecution,
  type ProjectExecutionResponse,
  type ProjectExecutionSnapshot,
  type ProjectExecutionTask,
} from "@/lib/api";

/** 项目执行查询状态 */
export type ProjectExecutionQueryState =
  /** 尚未选择项目，或正在加载 */
  | { status: "loading"; snapshot?: undefined; tasks: ProjectExecutionTask[] }
  /** 查询成功；snapshot 是服务端的真实投影 */
  | { status: "ready"; snapshot: ProjectExecutionSnapshot; tasks: ProjectExecutionTask[] }
  /** 查询失败；错误来自接口，不是"没有任务" */
  | { status: "error"; tasks: ProjectExecutionTask[]; error: string };

/** 项目执行动作的展示状态 */
export type ProjectExecutionAction = {
  /** 是否有请求在途 */
  submitting: boolean;
  /** 动作失败的说明 */
  error?: string;
  /** 结果未知提示：请求未能确认成功或失败，页面已重新读取服务端状态 */
  unknown?: string;
};

/** 把未知错误转成可读说明，区分网络中断、超时与业务错误 */
export function describeProjectExecutionError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "请求超时，服务端可能仍在处理，请重新读取状态确认。";
    if (error.kind === "aborted") return "请求已取消。";
    if (error.status === 409) return `操作被拒绝：${error.message}`;
    if (error.status === 412) return `前置条件不满足：${error.message}`;
    if (error.status === 503) return "项目执行服务不可用，请稍后重试或检查服务端状态。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 判断错误是否意味着"结果未知"：连接中断或超时都不代表服务端没有执行 */
function isUnknownOutcome(error: unknown): boolean {
  return (
    error instanceof ApiError && (error.kind === "network" || error.kind === "timeout")
  );
}

/**
 * 读取并操作指定项目的执行状态。
 *
 * `projectID` 为空时不发请求：未选择项目与"项目没有任务"是两种事实。
 */
export function useProjectExecution(projectID?: string) {
  const [state, setState] = useState<ProjectExecutionQueryState>({
    status: "loading",
    tasks: [],
  });
  const [action, setAction] = useState<ProjectExecutionAction>({ submitting: false });
  /** 只接受最近一次请求的结果，避免重试后旧响应覆盖新状态 */
  const requestRef = useRef(0);

  const load = useCallback(async (): Promise<void> => {
    if (!projectID) {
      setState({ status: "loading", tasks: [] });
      return;
    }
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    setState({ status: "loading", tasks: [] });
    try {
      const { data } = await fetchProjectExecution(projectID);
      if (requestRef.current !== requestID) return;
      setState(readState(data));
    } catch (cause) {
      if (requestRef.current !== requestID) return;
      setState({
        error: describeProjectExecutionError(cause),
        status: "error",
        tasks: [],
      });
    }
  }, [projectID]);

  /**
   * 执行一次有副作用的动作，并无论结果如何都重新读取服务端状态。
   *
   * 副作用请求不自动重试：连接中断时无法确认服务端是否已经执行过，页面只展示"结果未知"
   * 并给出真实状态。
   */
  const runAction = useCallback(
    async (invoke: (id: string) => Promise<{ data: ProjectExecutionResponse }>) => {
      if (!projectID) {
        setAction({ error: "请先选择项目", submitting: false });
        return;
      }
      setAction({ submitting: true });
      try {
        const { data } = await invoke(projectID);
        setState(readState(data));
        setAction({ submitting: false });
      } catch (cause) {
        setAction({
          error: describeProjectExecutionError(cause),
          submitting: false,
          unknown: isUnknownOutcome(cause) ? "这次操作的结果未知，已重新读取服务端状态。" : undefined,
        });
        await load();
      }
    },
    [load, projectID],
  );

  const start = useCallback(() => runAction(startProjectExecution), [runAction]);
  const cancel = useCallback(() => runAction(cancelProjectExecution), [runAction]);

  useEffect(() => {
    void load();
    return () => {
      // 卸载后不再接受在途请求的结果。
      requestRef.current += 1;
    };
  }, [load]);

  return { ...state, action, reload: load, start, cancel };
}

/** 把服务端响应转换为查询状态 */
function readState(data: ProjectExecutionResponse): ProjectExecutionQueryState {
  return { snapshot: data.execution, status: "ready", tasks: data.tasks };
}
