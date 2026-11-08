/**
 * 当前项目下的规划会话列表与新建会话。
 *
 * 设计约束：
 * - 列表、加载中、查询失败与空列表是四种不同状态，失败不会被伪装成空列表；
 * - 切换项目时旧项目的会话立即失效，绝不把上一个项目的会话带入新项目；
 * - 新建会话只提交项目标识，会话标识由服务端生成，前端不预填。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  createPlanningSession,
  fetchPlanningSessions,
  type PlanningSession,
} from "@/lib/api";
import { describePlanningQueryError } from "@/pages/planning/planning";

/** 会话列表状态 */
export type PlanningSessionListState =
  /** 尚未选择项目 */
  | { status: "idle"; sessions: PlanningSession[] }
  /** 正在查询当前项目的会话 */
  | { status: "loading"; sessions: PlanningSession[] }
  /** 查询成功；列表可能为空，表示该项目还没有规划会话 */
  | { status: "ready"; sessions: PlanningSession[] }
  /** 查询失败；错误来自接口，不是空列表 */
  | { status: "error"; sessions: PlanningSession[]; error: string };

/** 新建会话失败说明：保留服务端返回的业务提示 */
function describeCreateError(error: unknown): string {
  if (error instanceof ApiError && error.kind === "network") {
    return "无法连接 AgentFlow 服务，请确认服务已启动。";
  }
  return describePlanningQueryError(error);
}

/**
 * 查询某个项目下的规划会话，并提供新建入口。
 *
 * `projectID` 变化时上一次查询会被取消，迟到的响应也会被丢弃。
 */
export function usePlanningSessions(projectID?: string) {
  const trimmedProjectID = projectID?.trim() ?? "";
  const [state, setState] = useState<PlanningSessionListState>({
    sessions: [],
    status: "idle",
  });
  const [createError, setCreateError] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const requestRef = useRef(0);
  const creatingRef = useRef(false);

  /** 重新查询当前项目的会话列表；失败原因由 state.error 单独表达 */
  const reload = useCallback(() => {
    setAttempt((value) => value + 1);
  }, []);

  useEffect(() => {
    if (!trimmedProjectID) {
      requestRef.current += 1;
      setState({ sessions: [], status: "idle" });
      return;
    }
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    setState({ sessions: [], status: "loading" });
    fetchPlanningSessions(trimmedProjectID, { signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ sessions: data.records, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({
          error: describePlanningQueryError(cause),
          sessions: [],
          status: "error",
        });
      });
    return () => controller.abort();
  }, [attempt, trimmedProjectID]);

  /**
   * 新建规划会话。
   *
   * 成功时刷新列表并返回服务端生成的会话记录；失败时记录可读原因并返回 undefined。
   * 重复点击不会发出第二个请求。
   */
  const create = useCallback(async (): Promise<PlanningSession | undefined> => {
    if (!trimmedProjectID || creatingRef.current) return undefined;
    creatingRef.current = true;
    setCreating(true);
    setCreateError(undefined);
    try {
      const { data } = await createPlanningSession({ project_id: trimmedProjectID });
      setAttempt((value) => value + 1);
      return data.session;
    } catch (cause) {
      setCreateError(describeCreateError(cause));
      return undefined;
    } finally {
      creatingRef.current = false;
      setCreating(false);
    }
  }, [trimmedProjectID]);

  const clearCreateError = useCallback(() => {
    setCreateError(undefined);
  }, []);

  return { clearCreateError, create, createError, creating, reload, state };
}

/** 会话列表 hook 的返回类型 */
export type PlanningSessionsState = ReturnType<typeof usePlanningSessions>;
