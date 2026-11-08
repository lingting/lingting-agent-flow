/**
 * 规划会话的服务端事实：会话记录、消息与方案版本。
 *
 * 页面只把这里读到的内容当作唯一数据源：提交消息、执行分析、重试与放弃之后都重新
 * 读取服务端，而不是用请求返回值就地拼装界面。刷新失败时保留上一次已取得的事实，
 * 并把失败原因单独交给调用方提示。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchPlanningMessages,
  fetchPlanningRevisions,
  fetchPlanningSession,
  type PlanningMessage,
  type PlanningRevision,
  type PlanningSession,
} from "@/lib/api";
import { describePlanningQueryError } from "@/pages/planning/planning";

/** 一个规划会话的完整事实 */
export type PlanningSessionFacts = {
  session: PlanningSession;
  messages: PlanningMessage[];
  revisions: PlanningRevision[];
};

/** 事实查询状态 */
export type PlanningFactsState =
  /** 尚未选择会话 */
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; facts: PlanningSessionFacts }
  | { status: "error"; error: string };

/** 依次读取会话详情、消息与方案版本；任一失败都向上抛出，不返回半份事实 */
async function loadPlanningFacts(
  sessionID: string,
  signal?: AbortSignal,
): Promise<PlanningSessionFacts> {
  const session = await fetchPlanningSession(sessionID, { signal });
  const messages = await fetchPlanningMessages(sessionID, { signal });
  const revisions = await fetchPlanningRevisions(sessionID, { signal });
  return {
    messages: messages.data.records,
    revisions: revisions.data.records,
    session: session.data.session,
  };
}

/**
 * 读取当前会话的事实。
 *
 * `reload()` 返回刷新过程中失败项的说明，供动作结束后单独提示；它不会覆盖已经取得的
 * 事实，也不过滤服务端返回的空消息或空版本列表。
 */
export function usePlanningSessionFacts(sessionID?: string) {
  const trimmedSessionID = sessionID?.trim() ?? "";
  const [state, setState] = useState<PlanningFactsState>(
    trimmedSessionID ? { status: "loading" } : { status: "idle" },
  );
  const requestRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const runLoad = useCallback(
    async (signal?: AbortSignal): Promise<string[]> => {
      if (!trimmedSessionID) {
        setState({ status: "idle" });
        return [];
      }
      const requestID = requestRef.current + 1;
      requestRef.current = requestID;
      const accepted = () => mountedRef.current && requestRef.current === requestID;
      try {
        const facts = await loadPlanningFacts(trimmedSessionID, signal);
        if (accepted()) setState({ facts, status: "ready" });
        return [];
      } catch (cause) {
        if (signal?.aborted) return [];
        const message = describePlanningQueryError(cause);
        if (accepted()) {
          // 刷新失败时保留旧事实：会话、消息与方案版本仍然有效，只是不是最新一份。
          setState((current) =>
            current.status === "ready" ? current : { error: message, status: "error" },
          );
        }
        return [message];
      }
    },
    [trimmedSessionID],
  );

  useEffect(() => {
    const controller = new AbortController();
    if (!trimmedSessionID) {
      requestRef.current += 1;
      setState({ status: "idle" });
      return () => controller.abort();
    }
    setState({ status: "loading" });
    void runLoad(controller.signal);
    return () => controller.abort();
  }, [runLoad, trimmedSessionID]);

  const reload = useCallback(async (): Promise<string[]> => runLoad(), [runLoad]);

  return { reload, state };
}

/** 事实查询 hook 的返回类型 */
export type PlanningFactsReader = ReturnType<typeof usePlanningSessionFacts>;
