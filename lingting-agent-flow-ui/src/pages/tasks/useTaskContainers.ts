/**
 * 任务容器的查询与显式处置（delete / detach）的唯一状态实现。
 *
 * 三条独立事实分别保存，互不覆盖：
 *   - 容器列表（`list`）：来自 `GET /tasks/{id}/containers`，是操作对象的唯一来源；
 *   - 处置结果（`operation.outcome`）：来自 `POST .../{delete|detach}` 的真实响应，
 *     只有它能说明"本次处置到底发生了什么"；
 *   - 请求 / 刷新失败（`list.error`、`operation.failure`、`operation.refreshError`）：
 *     分别提示，绝不把失败改写成成功，也不因为刷新失败抹掉已取得的结论。
 *
 * 竞态约束：
 *   - 列表请求带「序号 + 任务标识」票据，切换任务后旧响应一律丢弃；
 *   - 处置请求发出时记录任务标识与容器标识，响应回来时若已切换，则不写入当前界面；
 *   - 同一容器同一时刻只允许一个处置请求在途；不同容器可以各自在途；
 *   - 组件卸载后不再通过迟到响应更新状态。
 *
 * 硬约束：
 *   - 没有任何自动重发：失败、超时、连接中断后都由用户显式决定；
 *   - 处置成功后只刷新容器列表与任务详情，不自动重试任务、不自动启动工作流；
 *   - 容器列表不并入 5 秒自动刷新，避免在有副作用的操作旁边产生隐式读取风暴。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  disposeTaskContainer,
  fetchTaskContainers,
  type ContainerDisposalAction,
  type ContainerDisposalResponse,
  type TaskContainerListResponse,
} from "@/lib/api";
import { DISPOSAL_UNKNOWN_OUTCOME_HINT, isUncertainDisposalError } from "@/pages/tasks/containers";
import { acceptResponse, beginRequest, createRequestGate } from "@/pages/tasks/requestGate";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";

/** 容器列表的加载状态 */
export type ContainerListState =
  /** 没有任务标识，未发起查询 */
  | { status: "idle" }
  /** 首次加载中，尚无快照 */
  | { status: "loading" }
  /** 已取得快照；`error` 非空表示刷新失败，展示的仍是上一次成功的结果 */
  | { status: "ready"; data: TaskContainerListResponse; error?: string }
  /** 查询失败且没有可用快照：此时容器列表未知，不得据此操作 */
  | { status: "error"; error: string };

/** 一次处置操作的状态 */
export type ContainerDisposalState = {
  /** 在途的容器标识与动作；没有请求时为空 */
  running?: { containerID: string; action: ContainerDisposalAction };
  /** 最近一次真实响应；刷新失败不影响它 */
  outcome?: {
    containerID: string;
    action: ContainerDisposalAction;
    data: ContainerDisposalResponse;
  };
  /** 请求失败的业务错误说明 */
  failure?: { containerID: string; action: ContainerDisposalAction; message: string };
  /** 请求结果不确定（超时 / 网络中断 / 取消）的明确提示 */
  uncertain?: string;
  /** 操作之后容器列表刷新失败的说明 */
  refreshError?: string;
};

/** 容器 Hook 的输入 */
export type UseTaskContainersOptions = {
  /** 当前打开的任务标识 */
  taskID?: string;
  /** 外部刷新令牌：由调用方在需要重新读取容器列表时递增 */
  token?: number;
  /**
   * 处置结束后的现场刷新（任务详情与工作流进度）。
   *
   * 返回值为刷新失败的项，用于单独提示；本 Hook 自己还会重新读取容器列表。
   */
  refreshTask?: () => Promise<string[]>;
};

/** 容器 Hook 的输出 */
export type TaskContainers = {
  list: ContainerListState;
  /** 重新读取容器列表；不清除已取得的快照 */
  reload: () => void;
  operation: ContainerDisposalState;
  /** 发起一次显式处置；同一容器在途或列表未知时不发送请求 */
  dispose: (containerID: string, action: ContainerDisposalAction) => Promise<void>;
  /** 清除上一次操作结论 */
  clearOutcome: () => void;
};

/**
 * 管理指定任务的容器列表与处置操作。
 *
 * `taskID` 为空表示没有打开任务详情：不查询容器，也不允许发起处置。
 */
export function useTaskContainers(options: UseTaskContainersOptions): TaskContainers {
  const trimmed = options.taskID?.trim() ?? "";
  const token = options.token;
  const refreshTask = options.refreshTask;

  const [list, setList] = useState<ContainerListState>({ status: "idle" });
  const [operation, setOperation] = useState<ContainerDisposalState>({});
  const [nonce, setNonce] = useState(0);

  /** 最近一次成功取得的列表快照及其归属任务 */
  const snapshotRef = useRef<{ taskID: string; data: TaskContainerListResponse } | undefined>(
    undefined,
  );
  /** 当前打开的任务标识：供异步回调判断是否已切换任务 */
  const activeTaskRef = useRef("");
  activeTaskRef.current = trimmed;
  const gateRef = useRef(createRequestGate());
  /** 同步的在途标记：同一容器的连续点击不会让 state 及时更新 */
  const runningRef = useRef<Set<string>>(new Set());
  /** 卸载标记：卸载后不再用迟到响应更新界面 */
  const mountedRef = useRef(true);

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  /**
   * 切换任务时清空处置结论：它描述的是另一个任务的容器。
   *
   * 这里刻意与列表请求解耦：处置结束后的列表刷新同样会重新请求，但那次刷新正是为了
   * 确认处置结果，清空结论会把用户刚看到的真实结论抹掉。
   */
  useEffect(() => {
    setOperation({});
  }, [trimmed]);

  useEffect(() => {
    gateRef.current = createRequestGate();
    if (!trimmed) {
      snapshotRef.current = undefined;
      setList({ status: "idle" });
      return;
    }

    // 任务变化时旧快照立刻失效：它描述的是另一个任务，不能当作当前任务的结论。
    const kept = snapshotRef.current?.taskID === trimmed ? snapshotRef.current : undefined;
    snapshotRef.current = kept;
    setList(kept ? { data: kept.data, status: "ready" } : { status: "loading" });

    const gate = gateRef.current;
    const ticket = beginRequest(gate, trimmed);
    const controller = new AbortController();
    fetchTaskContainers(trimmed, { signal: controller.signal })
      .then(({ data }) => {
        if (!acceptResponse(gate, ticket, activeTaskRef.current, controller.signal.aborted)) {
          return;
        }
        snapshotRef.current = { data, taskID: trimmed };
        setList({ data, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (!acceptResponse(gate, ticket, activeTaskRef.current, controller.signal.aborted)) {
          return;
        }
        const message = describeApiError(cause);
        const previous = snapshotRef.current;
        if (previous && previous.taskID === trimmed) {
          // 刷新失败：保留上一次真实结果，并明确标注它不是最新查询。
          setList({ data: previous.data, error: message, status: "ready" });
          return;
        }
        setList({ error: message, status: "error" });
      });
    return () => controller.abort();
  }, [nonce, token, trimmed]);

  /** 发起一次显式处置：列表未知、同一容器在途或记录已终态时直接返回 */
  const dispose = useCallback(
    async (containerID: string, action: ContainerDisposalAction) => {
      const target = activeTaskRef.current;
      const container = containerID.trim();
      if (!target || !container) return;
      if (runningRef.current.has(container)) return;

      runningRef.current.add(container);
      setOperation({ running: { action, containerID: container } });
      let response: ContainerDisposalResponse | undefined;
      let failure: string | undefined;
      let uncertain: string | undefined;
      try {
        const { data } = await disposeTaskContainer(target, container, action);
        response = data;
      } catch (cause) {
        failure = describeApiError(cause);
        if (cause instanceof ApiError && isUncertainDisposalError(cause.kind)) {
          uncertain = DISPOSAL_UNKNOWN_OUTCOME_HINT;
        }
      } finally {
        runningRef.current.delete(container);
      }

      if (!mountedRef.current || activeTaskRef.current !== target) return;
      if (response) {
        setOperation({ outcome: { action, containerID: container, data: response } });
      } else {
        setOperation({
          failure: { action, containerID: container, message: failure ?? "" },
          uncertain,
        });
      }

      // 无论响应成功还是失败，都以数据库中的真实状态为准刷新一次现场。
      // 刷新失败只记录提示，不覆盖已经取得的操作结论。
      const failures: string[] = [];
      try {
        const refreshed = refreshTask ? await refreshTask() : [];
        failures.push(...refreshed);
      } catch (cause) {
        failures.push(describeApiError(cause));
      }
      if (!mountedRef.current || activeTaskRef.current !== target) return;
      // 重新读取容器列表：处置结果必须由数据库中的真实状态确认，而不是靠本地推断。
      setNonce((value) => value + 1);
      if (failures.length > 0) {
        setOperation((current) => ({
          ...current,
          refreshError: `处置请求已结束，但状态刷新失败：${failures.join("、")}。上方结论仍然有效，请手动刷新确认最新状态。`,
        }));
      }
    },
    [refreshTask],
  );

  const clearOutcome = useCallback(() => setOperation({}), []);

  return { clearOutcome, dispose, list, operation, reload };
}
