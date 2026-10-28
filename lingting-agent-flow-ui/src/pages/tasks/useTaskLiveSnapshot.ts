/**
 * 任务详情与执行进度的自动刷新（唯一实现）。
 *
 * 职责边界：
 *   - 这里只负责"多久读一次、读了之后展示什么"，不发起任何有副作用的操作；
 *   - 是否继续刷新由 `decideAutoRefresh` 依据服务端返回的任务状态与工作流执行状态决定；
 *   - 快照按任务标识归属：切换任务、关闭详情与卸载页面都会让旧请求的响应失效；
 *   - 刷新失败只附加提示并保留上一次成功快照，且不会改变刷新节奏或推断任务已终止。
 *
 * 子面板（步骤、尝试与验证、代码保存、续跑资格）不各自建定时器：本 Hook 在检测到
 * 相关服务端字段发生变化时递增对应的 `tokens`，由面板在令牌变化时重新加载。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { fetchTask, fetchTaskWorkflow } from "@/lib/api";
import {
  AUTO_REFRESH_INTERVAL_MS,
  AUTO_REFRESH_STARTUP_RETRY_ROUNDS,
  AUTO_REFRESH_STOPPED_HINT,
  createRefreshScheduler,
  type RefreshScheduler,
} from "@/pages/tasks/autoRefresh";
import {
  applySnapshotRound,
  INITIAL_TOKENS,
  refreshHintFor,
  type LiveSnapshot,
  type RefreshTokens,
} from "@/pages/tasks/liveSnapshot";
import { acceptResponse, beginRequest, createRequestGate } from "@/pages/tasks/requestGate";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";

export type { LiveSnapshot, RefreshTokens } from "@/pages/tasks/liveSnapshot";

/** 页面是否可见：只在浏览器环境判断，其他环境视为可见 */
function pageVisible(): boolean {
  if (typeof document === "undefined") return true;
  return document.visibilityState !== "hidden";
}

/**
 * 按任务标识维护详情快照并自动刷新。
 *
 * `taskID` 为空表示没有打开任务详情：此时不加载、不排定任何定时器。
 */
export function useTaskLiveSnapshot(taskID?: string): {
  snapshot: LiveSnapshot;
  tokens: RefreshTokens;
  /** 自动刷新是否仍在进行（false 时只有手动刷新会更新快照） */
  autoRefreshOn: boolean;
  /** 自动刷新停止的原因说明；自动刷新进行中或缺省时为空 */
  refreshHint?: string;
  /** 立即读取一次（手动刷新与动作后刷新共用），返回刷新失败的项 */
  refreshNow: () => Promise<string[]>;
  /**
   * 读取指定任务的最新状态，返回刷新失败的项。
   *
   * 与 `refreshNow` 的区别只在于目标任务：动作可能作用于列表行而不是当前打开的详情。
   * 目标不是当前详情任务时只做只读查询，不把结果写进快照，避免把别的任务显示在详情里。
   */
  refreshTask: (taskID: string) => Promise<string[]>;
} {
  const trimmed = taskID?.trim() ?? "";
  const [snapshot, setSnapshot] = useState<LiveSnapshot>({ loading: false });
  const [tokens, setTokens] = useState<RefreshTokens>(INITIAL_TOKENS);
  const [refreshHint, setRefreshHint] = useState<string>();
  const [autoRefreshOn, setAutoRefreshOn] = useState(false);

  /** 最新快照与当前任务标识：供稳定回调读取，避免闭包读到旧值 */
  const snapshotRef = useRef<LiveSnapshot>({ loading: false });
  snapshotRef.current = snapshot;
  const activeTaskIDRef = useRef("");
  activeTaskIDRef.current = trimmed;
  const gateRef = useRef(createRequestGate());
  /** 在途的一轮：同一任务同一时刻最多一轮 */
  const inflightRef = useRef<{ taskID: string; promise: Promise<string[]> } | undefined>(undefined);
  const schedulerRef = useRef<RefreshScheduler | undefined>(undefined);

  const bumpTokens = useCallback((dirty: Partial<Record<keyof RefreshTokens, boolean>>) => {
    if (!dirty.steps && !dirty.codeSaves && !dirty.resume && !dirty.containers) return;
    setTokens((current) => ({
      codeSaves: current.codeSaves + (dirty.codeSaves ? 1 : 0),
      containers: current.containers + (dirty.containers ? 1 : 0),
      resume: current.resume + (dirty.resume ? 1 : 0),
      steps: current.steps + (dirty.steps ? 1 : 0),
    }));
  }, []);

  /** 读取一轮快照，返回刷新失败的项 */
  const loadRound = useCallback(
    async (target: string): Promise<string[]> => {
      const gate = gateRef.current;
      const ticket = beginRequest(gate, target);
      const controller = new AbortController();
      const previous = snapshotRef.current;
      const previousForTask: LiveSnapshot =
        previous.taskID === target ? previous : { loading: false };

      const [taskResult, workflowResult] = await Promise.allSettled([
        fetchTask(target, { signal: controller.signal }),
        fetchTaskWorkflow(target, { signal: controller.signal }),
      ]);

      // 任务已切换、详情已关闭或请求被取消：迟到的响应一律丢弃。
      if (!acceptResponse(gate, ticket, activeTaskIDRef.current, controller.signal.aborted)) {
        return [];
      }

      const applied = applySnapshotRound({
        previous: previousForTask,
        target,
        task: taskResult.status === "fulfilled" ? taskResult.value.data : previousForTask.task,
        taskError:
          taskResult.status === "rejected" ? describeApiError(taskResult.reason) : undefined,
        workflow:
          workflowResult.status === "fulfilled"
            ? workflowResult.value.data
            : previousForTask.workflow,
        workflowError:
          workflowResult.status === "rejected"
            ? describeApiError(workflowResult.reason)
            : undefined,
      });

      setSnapshot(applied.snapshot);
      bumpTokens(applied.dirty);
      if (applied.policy) {
        schedulerRef.current?.setPolicy(applied.policy);
        setAutoRefreshOn(applied.policy.mode !== "off");
        setRefreshHint(refreshHintFor(applied.policy));
      }
      return applied.failures;
    },
    [bumpTokens],
  );

  /** 串行入口：同一任务在途时复用同一 Promise，绝不并发发起第二轮 */
  const runOnce = useCallback(
    (target: string): Promise<string[]> => {
      const inflight = inflightRef.current;
      if (inflight && inflight.taskID === target) return inflight.promise;
      const promise = loadRound(target).finally(() => {
        if (inflightRef.current?.promise === promise) inflightRef.current = undefined;
      });
      inflightRef.current = { promise, taskID: target };
      return promise;
    },
    [loadRound],
  );

  /** 手动刷新与动作后刷新：立即读取一次，返回刷新失败的项 */
  const refreshNow = useCallback(async (): Promise<string[]> => {
    const target = activeTaskIDRef.current;
    if (!target) return [];
    const scheduler = schedulerRef.current;
    scheduler?.cancelScheduled();
    const failures = await runOnce(target);
    // 手动刷新结算后接回节奏：否则自动刷新会悄悄停掉。
    scheduler?.scheduleNext();
    return failures;
  }, [runOnce]);

  /** 按任务标识读取最新状态；目标不是当前详情任务时只报告失败项 */
  const refreshTask = useCallback(
    async (taskID: string): Promise<string[]> => {
      const target = taskID.trim();
      if (!target) return [];
      if (target === activeTaskIDRef.current) return refreshNow();
      const failures: string[] = [];
      const results = await Promise.allSettled([fetchTask(target), fetchTaskWorkflow(target)]);
      if (results[0].status === "rejected") {
        failures.push(`任务详情（${describeApiError(results[0].reason)}）`);
      }
      if (results[1].status === "rejected") {
        failures.push(`工作流进度（${describeApiError(results[1].reason)}）`);
      }
      return failures;
    },
    [refreshNow],
  );

  useEffect(() => {
    gateRef.current = createRequestGate();
    inflightRef.current = undefined;
    schedulerRef.current = undefined;

    if (!trimmed) {
      setSnapshot({ loading: false });
      setTokens(INITIAL_TOKENS);
      setRefreshHint(undefined);
      setAutoRefreshOn(false);
      return;
    }

    setSnapshot({ loading: true, taskID: trimmed });
    setRefreshHint(undefined);
    setAutoRefreshOn(true);

    const scheduler = createRefreshScheduler({
      intervalMs: AUTO_REFRESH_INTERVAL_MS,
      isVisible: pageVisible,
      onExhausted: () => {
        setAutoRefreshOn(false);
        setRefreshHint(AUTO_REFRESH_STOPPED_HINT.startup_window_elapsed);
      },
      // 首轮结果尚未取得时只给有限的启动窗口：取得真实状态后会立即被重新判定。
      policy: { mode: "bounded", maxRounds: AUTO_REFRESH_STARTUP_RETRY_ROUNDS },
      run: () => runOnce(trimmed).then(() => undefined),
    });
    schedulerRef.current = scheduler;
    scheduler.start();

    const handleVisibility = () => scheduler.setVisible(pageVisible());
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", handleVisibility);
    }

    // 打开详情时立即加载，不需要等到第一个周期。
    void runOnce(trimmed);

    return () => {
      if (typeof document !== "undefined") {
        document.removeEventListener("visibilitychange", handleVisibility);
      }
      scheduler.stop();
      if (schedulerRef.current === scheduler) schedulerRef.current = undefined;
      inflightRef.current = undefined;
    };
  }, [runOnce, trimmed]);

  return { autoRefreshOn, refreshHint, refreshNow, refreshTask, snapshot, tokens };
}
