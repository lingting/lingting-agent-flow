/**
 * 重试资格查询与显式重试操作的唯一状态实现。
 *
 * 三条独立事实分别保存，互不覆盖：
 *   - 资格快照（`eligibility`）：来自 `GET /retry-eligibility`，只有 `can_retry` 能决定
 *     重试入口是否可用；
 *   - 重试请求结果（`outcome`）：来自 `POST /retry` 的真实响应，只有它在决定"是否已重开
 *     任务、工作流是否真的启动"；
 *   - 请求 / 刷新失败（`eligibilityError`、`failure`、`refreshError`）：分别提示，绝不把
 *     失败改写成成功，也不因为刷新失败抹掉已经取得的结论。
 *
 * 竞态约束：
 *   - 资格请求带「序号 + 任务标识」票据，切换任务后旧响应一律丢弃；
 *   - 重试请求发出时记录任务标识，响应回来时若已切换到其他任务，则不写入当前界面；
 *   - 组件卸载后不再通过迟到响应更新状态。
 *
 * 另外两条硬约束：
 *   - 重试没有任何自动重发：请求失败、超时或连接中断后都由用户显式决定；
 *   - 资格查询不并入 5 秒自动刷新：它只在任务切换与用户显式刷新时发起。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  fetchTaskRetryEligibility,
  retryTask,
  type RetryEligibilityResponse,
  type RetryTaskRequest,
  type RetryTaskResponse,
} from "@/lib/api";
import {
  acceptRetryResponse,
  beginRetryRequest,
  createRetryGate,
  isUncertainRetryError,
  RETRY_UNKNOWN_OUTCOME_HINT,
} from "@/pages/tasks/retry";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";

/** 重试资格的加载状态 */
export type RetryEligibilityState =
  /** 没有任务标识，未发起查询 */
  | { status: "idle" }
  /** 首次加载中，尚无快照 */
  | { status: "loading" }
  /** 已取得快照；`error` 非空表示刷新失败，展示的仍是上一次成功的结果 */
  | { status: "ready"; data: RetryEligibilityResponse; error?: string }
  /** 查询失败且没有可用快照：此时资格未知，不得当作"可以重试" */
  | { status: "error"; error: string };

/** 一次重试操作的状态 */
export type RetryOperationState = {
  /** 是否有重试请求在途 */
  running: boolean;
  /** 最近一次真实响应；刷新失败不影响它 */
  outcome?: RetryTaskResponse;
  /** 请求失败的业务错误说明 */
  failure?: string;
  /** 请求结果不确定（超时 / 网络中断 / 取消）的明确提示 */
  uncertain?: string;
  /** 操作之后现场刷新失败的说明 */
  refreshError?: string;
};

/** 重试 Hook 的输入 */
export type UseTaskRetryOptions = {
  /** 当前打开的任务标识 */
  taskID?: string;
  /** 显式刷新令牌：由调用方在需要重新查询资格时递增 */
  token?: number;
  /**
   * 动作结束后的现场刷新：应重新读取任务详情、工作流进度与重试资格。
   * 返回值是刷新失败的项，用于单独提示。
   */
  refresh: () => Promise<string[]>;
};

/** 重试 Hook 的输出 */
export type TaskRetry = {
  eligibility: RetryEligibilityState;
  /** 重新查询资格；不清除已取得的快照 */
  reloadEligibility: () => void;
  operation: RetryOperationState;
  /** 发起一次显式重试；没有资格快照或已在途时不发送请求 */
  retry: (body?: RetryTaskRequest) => Promise<void>;
  /** 清除上一次操作结论 */
  clearOutcome: () => void;
};

/**
 * 管理指定任务的重试资格与重试操作。
 *
 * `taskID` 为空表示没有打开任务详情：不查询资格，也不允许发起重试。
 */
export function useTaskRetry(options: UseTaskRetryOptions): TaskRetry {
  const trimmed = options.taskID?.trim() ?? "";
  const token = options.token;
  const refresh = options.refresh;

  const [eligibility, setEligibility] = useState<RetryEligibilityState>({ status: "idle" });
  const [operation, setOperation] = useState<RetryOperationState>({ running: false });
  const [nonce, setNonce] = useState(0);

  /** 最近一次成功取得的资格快照及其归属任务 */
  const snapshotRef = useRef<{ taskID: string; data: RetryEligibilityResponse } | undefined>(
    undefined,
  );
  /** 当前打开的任务标识：供异步回调判断是否已切换任务 */
  const activeTaskRef = useRef("");
  activeTaskRef.current = trimmed;
  const gateRef = useRef(createRetryGate());
  /** 同步的在途标记：同一帧内的连续点击不会让 state 及时更新 */
  const runningRef = useRef(false);
  /** 卸载标记：卸载后不再用迟到响应更新界面 */
  const mountedRef = useRef(true);

  const reloadEligibility = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    gateRef.current = createRetryGate();
    if (!trimmed) {
      snapshotRef.current = undefined;
      setEligibility({ status: "idle" });
      return;
    }

    // 任务变化时旧快照立刻失效：它描述的是另一个任务，不能当作当前任务的结论。
    const kept = snapshotRef.current?.taskID === trimmed ? snapshotRef.current : undefined;
    snapshotRef.current = kept;
    setEligibility(kept ? { data: kept.data, status: "ready" } : { status: "loading" });

    const ticket = beginRetryRequest(gateRef.current, trimmed);
    const controller = new AbortController();
    fetchTaskRetryEligibility(trimmed, { signal: controller.signal })
      .then(({ data }) => {
        if (
          !acceptRetryResponse(
            gateRef.current,
            ticket,
            activeTaskRef.current,
            controller.signal.aborted,
          )
        ) {
          return;
        }
        snapshotRef.current = { data, taskID: trimmed };
        setEligibility({ data, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (
          !acceptRetryResponse(
            gateRef.current,
            ticket,
            activeTaskRef.current,
            controller.signal.aborted,
          )
        ) {
          return;
        }
        const message = describeApiError(cause);
        const previous = snapshotRef.current;
        if (previous && previous.taskID === trimmed) {
          // 刷新失败：保留上一次真实结论，并明确标注它不是最新查询。
          setEligibility({ data: previous.data, error: message, status: "ready" });
          return;
        }
        setEligibility({ error: message, status: "error" });
      });
    return () => controller.abort();
  }, [nonce, token, trimmed]);

  /** 发起一次显式重试：资格未确认或已在途时直接返回，不发送请求 */
  const retry = useCallback(
    async (body: RetryTaskRequest = {}) => {
      if (runningRef.current) return;
      const target = activeTaskRef.current;
      const snapshot = snapshotRef.current;
      if (!target || !snapshot || snapshot.taskID !== target || !snapshot.data.can_retry) return;

      runningRef.current = true;
      setOperation({ running: true });
      try {
        const { data } = await retryTask(target, body);
        if (!mountedRef.current || activeTaskRef.current !== target) return;
        setOperation({ outcome: data, running: false });
      } catch (cause) {
        if (!mountedRef.current || activeTaskRef.current !== target) return;
        const uncertain =
          cause instanceof ApiError && isUncertainRetryError(cause.kind)
            ? RETRY_UNKNOWN_OUTCOME_HINT
            : undefined;
        setOperation({ failure: describeApiError(cause), running: false, uncertain });
      } finally {
        runningRef.current = false;
      }

      // 无论响应成功还是失败，都以数据库中的真实状态为准刷新一次现场。
      // 刷新失败只记录提示，不覆盖已经取得的操作结论。
      let failures: string[] = [];
      try {
        failures = await refresh();
      } catch (cause) {
        failures = [describeApiError(cause)];
      }
      if (!mountedRef.current || activeTaskRef.current !== target) return;
      setOperation((current) => ({
        ...current,
        refreshError:
          failures.length > 0
            ? `重试请求已结束，但状态刷新失败：${failures.join("、")}。上方结论仍然有效，请手动刷新确认最新状态。`
            : undefined,
      }));
      // 重新查询一次重试资格：任务被重开后服务端结论通常会变化，用户必须看到最新结果。
      setNonce((value) => value + 1);
    },
    [refresh],
  );

  const clearOutcome = useCallback(() => setOperation({ running: false }), []);

  return { clearOutcome, eligibility, operation, reloadEligibility, retry };
}
