/**
 * 方案确认与已采纳结果的读取。
 *
 * 关键约束：
 * - 确认只提交当前选中的 Revision 与执行节点，正式需求与任务由服务端计算；
 * - 同一时刻只允许一个确认请求在途：重复点击不会发出第二个 POST；
 * - 任何失败或结果不确定之后都重新读取服务端事实，不盲目重放确认；
 * - 确认成功后不提供"开始执行"入口：任务保持服务端返回的 queued 状态。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  confirmPlanningSession,
  fetchProjectRequirements,
  fetchRequirement,
  type ConfirmPlanningSessionResponse,
  type ConfirmedRequirement,
  type ConfirmedTask,
} from "@/lib/api";
import {
  describePlanningConfirmError,
  findSessionRequirement,
  type PlanningErrorFacts,
} from "@/pages/planning/planning";

/** 确认动作的状态 */
export type PlanningConfirmState =
  | { status: "idle" }
  | { status: "confirming" }
  | { status: "succeeded"; result: ConfirmPlanningSessionResponse; refreshError?: string }
  | { status: "failed"; error: PlanningErrorFacts; refreshError?: string };

/** 已采纳结果的读取状态（用于恢复已确认会话） */
export type PlanningAdoptedState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; requirement: ConfirmedRequirement; tasks: ConfirmedTask[] }
  | { status: "error"; error: string };

/** 把刷新失败列表拼成一句提示；没有失败时返回 undefined */
function describeRefreshFailures(failures: string[]): string | undefined {
  if (failures.length === 0) return undefined;
  return `确认请求已返回，但重新读取服务端状态失败：${failures.join("、")}。请手动刷新确认结果。`;
}

/** 已采纳结果读取失败时的说明 */
function describeAdoptedFailure(message: string): string {
  return `无法读取该会话采纳生成的正式需求：${message}。页面不补造结果，请稍后重试。`;
}

/**
 * 按项目需求列表定位当前会话采纳生成的需求，再读取它的详情（含任务顺序）。
 *
 * 已确认会话本身不保存需求标识，因此只能按 `planning_session_id` 匹配；匹配不到时说
 * 明"读不到"，不显示成"没有生成正式实体"。
 */
async function loadAdoptedRequirement(
  projectID: string,
  sessionID: string,
): Promise<{ requirement: ConfirmedRequirement; tasks: ConfirmedTask[] }> {
  const list = await fetchProjectRequirements(projectID);
  const requirement = findSessionRequirement(list.data.records, sessionID);
  if (!requirement) {
    throw new Error("该项目需求列表中没有与该会话关联的需求");
  }
  const detail = await fetchRequirement(requirement.id);
  return { requirement: detail.data.requirement, tasks: detail.data.tasks };
}

/**
 * 确认入口与已采纳结果。
 *
 * `refresh` 由调用方提供，负责重新读取会话、消息与方案版本；确认失败后必须重新读取，
 * 因为请求返回值不是数据库的最终状态。
 */
export function usePlanningConfirm(options: {
  sessionID?: string;
  projectID?: string;
  status: string;
  refresh: () => Promise<string[]>;
}) {
  const { refresh, sessionID, projectID, status } = options;
  const trimmedSessionID = sessionID?.trim() ?? "";
  const trimmedProjectID = projectID?.trim() ?? "";
  const [state, setState] = useState<PlanningConfirmState>({ status: "idle" });
  const [adopted, setAdopted] = useState<PlanningAdoptedState>({ status: "idle" });
  /** 同步的在途标记：同一帧内的连续点击不会让 state 及时更新 */
  const runningRef = useRef(false);
  /** 会话票据：切换会话后旧会话的迟到响应一律丢弃 */
  const ticketRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  /** 切换会话时清空确认结论与已采纳结果 */
  useEffect(() => {
    ticketRef.current += 1;
    runningRef.current = false;
    setState({ status: "idle" });
    setAdopted({ status: "idle" });
  }, [trimmedSessionID]);

  useEffect(() => {
    if (status !== "confirmed" || !trimmedSessionID || !trimmedProjectID) {
      setAdopted({ status: "idle" });
      return;
    }
    let cancelled = false;
    setAdopted({ status: "loading" });
    void loadAdoptedRequirement(trimmedProjectID, trimmedSessionID)
      .then((loaded) => {
        if (cancelled || !mountedRef.current) return;
        setAdopted({ requirement: loaded.requirement, status: "ready", tasks: loaded.tasks });
      })
      .catch((cause: unknown) => {
        if (cancelled || !mountedRef.current) return;
        const message = cause instanceof Error ? cause.message : "未知错误";
        setAdopted({ error: describeAdoptedFailure(message), status: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [status, trimmedProjectID, trimmedSessionID]);

  const confirm = useCallback(
    async (revisionID: string, hostID?: string) => {
      if (runningRef.current || !trimmedSessionID) return;
      const ticket = ticketRef.current;
      const accepted = () => mountedRef.current && ticketRef.current === ticket;
      runningRef.current = true;
      setState({ status: "confirming" });
      try {
        const response = await confirmPlanningSession(trimmedSessionID, {
          host_id: hostID?.trim() || undefined,
          revision_id: revisionID,
        });
        // 成功之后重新读取会话与方案版本：状态、采纳版本与需求都以服务端为准。
        const failures = await refresh();
        if (accepted()) {
          setState({
            refreshError: describeRefreshFailures(failures),
            result: response.data,
            status: "succeeded",
          });
        }
      } catch (cause) {
        const facts = describePlanningConfirmError(cause);
        let failures: string[] = [];
        try {
          failures = await refresh();
        } catch (refreshCause) {
          failures = [describePlanningConfirmError(refreshCause).message];
        }
        if (accepted()) {
          setState({
            error: facts,
            refreshError: describeRefreshFailures(failures),
            status: "failed",
          });
        }
      } finally {
        runningRef.current = false;
      }
    },
    [refresh, trimmedSessionID],
  );

  const clearError = useCallback(() => {
    setState((current) => (current.status === "failed" ? { status: "idle" } : current));
  }, []);

  return { adopted, clearError, confirm, state };
}
