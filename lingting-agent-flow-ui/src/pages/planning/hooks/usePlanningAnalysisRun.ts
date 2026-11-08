/**
 * 需求工作台的写操作：提交需求、执行两阶段分析、显式重试与放弃规划。
 *
 * 关键约束：
 * - 消息提交与模型分析是两个独立请求：提交成功只代表服务端追加了用户消息并把会话推进
 *   到 analyzing，必须再显式调用 `/analysis`；只有它成功返回并完成持久化才算出现新版本；
 * - `/analysis` 与 `/analysis/retry` 是长请求，因此不传取消信号：页面卸载不会中断仍在
 *   服务端执行的模型调用，生命周期由服务端阶段超时与请求上下文管理；
 * - 任何失败或结果不确定之后都重新读取服务端事实，不用请求返回值当作数据库最终事实；
 * - 不自动重试：同一时刻只允许一个动作在途，冲突（409）只提示并重新读取状态。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  abandonPlanningSession,
  retryPlanningAnalysis,
  runPlanningAnalysis,
  submitPlanningMessage,
} from "@/lib/api";
import {
  describePlanningActionError,
  isBlankPlanningInput,
  type PlanningErrorFacts,
} from "@/pages/planning/planning";

/** 页面可发起的写操作 */
export type PlanningRunAction = "submit" | "analyze" | "resume" | "abandon";

/** 动作区状态 */
export type PlanningRunState = {
  /** 正在执行的动作；存在时所有写操作按钮都应禁用 */
  running?: PlanningRunAction;
  /** 最近一次动作失败或结果不确定的说明 */
  error?: PlanningErrorFacts;
  /** 动作结束后的现场刷新失败说明；不覆盖上面的结论 */
  refreshError?: string;
};

/** 把刷新失败列表拼成一句提示；没有失败时返回 undefined */
function describeRefreshFailures(failures: string[]): string | undefined {
  if (failures.length === 0) return undefined;
  return `动作已结束，但重新读取服务端状态失败：${failures.join("、")}。页面展示的仍是上一次读取到的事实，请手动刷新确认。`;
}

/**
 * 写操作入口。
 *
 * `refresh` 由调用方提供，负责重新读取会话、消息、方案版本与会话列表。
 */
export function usePlanningAnalysisRun(options: {
  sessionID?: string;
  refresh: () => Promise<string[]>;
}) {
  const { sessionID, refresh } = options;
  const trimmedSessionID = sessionID?.trim() ?? "";
  const [state, setState] = useState<PlanningRunState>({});
  /** 同步的在途标记：同一帧内的连续点击不会让 state 及时更新 */
  const runningRef = useRef(false);
  /** 当前在途动作的票据；只有它仍是最新票据时才允许清除在途标记 */
  const runTicketRef = useRef<number | undefined>(undefined);
  /** 会话切换票据：切换后旧会话的迟到响应一律丢弃 */
  const ticketRef = useRef(0);
  /** 当前打开的会话标识：供异步回调判断是否已切换会话 */
  const activeSessionRef = useRef(trimmedSessionID);
  activeSessionRef.current = trimmedSessionID;
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  /**
   * 切换会话时清空动作结论。
   *
   * 不取消在途请求：服务端可能仍在执行这一轮分析，中断浏览器请求不能证明它已经失败。
   */
  useEffect(() => {
    ticketRef.current += 1;
    runningRef.current = false;
    runTicketRef.current = undefined;
    setState({});
  }, [trimmedSessionID]);

  const runAction = useCallback(
    async (
      action: PlanningRunAction,
      task: (mark: (next: PlanningRunAction) => void) => Promise<string[]>,
      /**
       * 按动作阶段调整失败说明。
       *
       * 提交动作包含"追加消息"与"分析"两个请求：只有消息确实没落库时才应提示
       * "无法确认是否已追加"，分析阶段的中断仍按分析请求的措辞说明。
       */
      describeFailure?: (facts: PlanningErrorFacts) => PlanningErrorFacts,
    ) => {
      if (runningRef.current || !trimmedSessionID) return;
      const startedFor = trimmedSessionID;
      const ticket = ticketRef.current + 1;
      ticketRef.current = ticket;
      const accepted = () =>
        mountedRef.current &&
        ticketRef.current === ticket &&
        activeSessionRef.current === startedFor;
      const mark = (next: PlanningRunAction) => {
        if (accepted()) setState({ running: next });
      };
      runningRef.current = true;
      runTicketRef.current = ticket;
      setState({ running: action });
      try {
        const failures = await task(mark);
        if (accepted()) setState({ refreshError: describeRefreshFailures(failures) });
      } catch (cause) {
        const facts = describePlanningActionError(cause);
        // 失败与结果不确定都必须重新读取服务端事实：请求返回值不是数据库的最终状态。
        let failures: string[] = [];
        try {
          failures = await refresh();
        } catch (refreshCause) {
          failures = [describePlanningActionError(refreshCause).message];
        }
        if (accepted()) {
          setState({
            error: describeFailure ? describeFailure(facts) : facts,
            refreshError: describeRefreshFailures(failures),
          });
        }
      } finally {
        if (runTicketRef.current === ticket) {
          runningRef.current = false;
          runTicketRef.current = undefined;
        }
        if (accepted()) {
          setState((current) => ({ ...current, running: undefined }));
        }
      }
    },
    [refresh, trimmedSessionID],
  );

  /**
   * 提交一条需求并立即发起本轮分析。
   *
   * 返回值表示"用户消息是否已由服务端持久化"：只有它落库后，输入框才可以清空；分析
   * 阶段的失败不会让已落库的消息回到输入框，从而避免用户重复提交同一条需求。
   *
   * 两步之间先刷新一次事实：即便后续分析请求失败，页面也已经展示服务端真实追加的用户
   * 消息与 analyzing 状态，用户可以看到"消息已落库、分析未成功"这个事实。
   */
  const submit = useCallback(
    async (value: string, hostID?: string): Promise<boolean> => {
      if (isBlankPlanningInput(value)) return false;
      let persisted = false;
      await runAction(
        "submit",
        async (mark) => {
          const failures: string[] = [];
          await submitPlanningMessage(trimmedSessionID, { content: value });
          persisted = true;
          failures.push(...(await refresh()));
          mark("analyze");
          const trimmedHostID = hostID?.trim();
          await runPlanningAnalysis(
            trimmedSessionID,
            trimmedHostID ? { host_id: trimmedHostID } : {},
          );
          failures.push(...(await refresh()));
          return failures;
        },
        // 消息已落库后，分析阶段的连接中断不能再说成"无法确认是否已追加消息"。
        (facts) => (persisted ? facts : describeSubmitFailure(facts)),
      );
      return persisted;
    },
    [refresh, runAction, trimmedSessionID],
  );

  /**
   * 显式恢复/重试：用于失败会话，也用于服务端仍为 analyzing 但没有在途请求的会话。
   *
   * 执行权是否真的空闲由服务端数据库条件更新判定，已有占用时返回 409；页面据此提示并
   * 重新读取状态，不会追加同一条用户消息。
   */
  const resumeAnalysis = useCallback(
    (hostID?: string) =>
      runAction("resume", async () => {
        const trimmedHostID = hostID?.trim();
        await retryPlanningAnalysis(
          trimmedSessionID,
          trimmedHostID ? { host_id: trimmedHostID } : {},
        );
        return refresh();
      }),
    [refresh, runAction, trimmedSessionID],
  );

  /** 放弃规划：只调用正式接口，前端不改写状态 */
  const abandon = useCallback(
    () =>
      runAction("abandon", async () => {
        await abandonPlanningSession(trimmedSessionID);
        return refresh();
      }),
    [refresh, runAction, trimmedSessionID],
  );

  const clearError = useCallback(() => {
    setState((current) => ({ ...current, error: undefined, refreshError: undefined }));
  }, []);

  return {
    abandon,
    clearError,
    resumeAnalysis,
    state,
    submit,
  };
}

/** 提交阶段的连接中断说明：此时还不能断言"分析失败" */
function describeSubmitFailure(facts: PlanningErrorFacts): PlanningErrorFacts {
  if (!facts.uncertain) return facts;
  return {
    ...facts,
    message:
      "提交需求时连接中断，无法确认服务端是否已追加这条消息。" +
      "页面不会自动重发，请以重新读取到的会话消息为准。",
  };
}

/** 写操作 hook 的返回类型 */
export type PlanningAnalysisRun = ReturnType<typeof usePlanningAnalysisRun>;
