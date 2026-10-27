/**
 * 任务续跑资格（`GET /tasks/{id}/resume-eligibility`）的加载与恢复工作流操作。
 *
 * 该查询与任务详情、工作流进度是三条独立的加载链路，因此必须有独立的加载状态：
 *   - `loading`：首次加载中，还没有任何结论；
 *   - `ready`：拿到了服务端返回的资格快照；`error` 非空表示后续刷新失败，展示的仍是
 *     上一次成功取得的快照；
 *   - `error`：请求失败且没有可用快照，此时不得显示"允许续跑"或"没有阻塞"。
 *
 * 三条额外约束：
 *   1. 资格查询可能核验远程容器，因此只在任务标识变化或用户显式刷新时发起，不在每次
 *      渲染或无关状态变化时重复请求；同一任务同时最多一个在途请求。
 *   2. 切换任务时旧任务的迟到响应必须被丢弃：快照按任务标识归属，标识不一致一律不展示。
 *   3. 刷新失败只附加错误，不清空上一次真实快照。
 *
 * 恢复工作流操作（`POST /tasks/{id}/resume-workflow`）复用同一份资格快照，但结果单独保存：
 *   - 资格快照只决定入口是否可用，它不是执行授权，服务端仍会重新核验前置条件；
 *   - 请求结果（`outcome`）与请求失败 / 结果不确定分别保存，互不覆盖；
 *   - 恢复请求没有自动重发：失败、超时与中断之后都由用户显式决定。
 *
 * 状态迁移被抽成纯函数（`resumeSnapshotForTask`、`resumeInitialState`、
 * `resumeLoaded`、`resumeFailed`），使竞态与保留旧数据的行为可以被直接验证，
 * 而不依赖浏览器环境。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  fetchTaskResumeEligibility,
  resumeTaskWorkflow,
  type ResumeEligibilityResponse,
  type ResumeWorkflowRequest,
  type WorkflowRun,
} from "@/lib/api";
import {
  acceptResumeResponse,
  beginResumeRequest,
  createResumeGate,
  isUncertainResumeError,
  RESUME_UNKNOWN_OUTCOME_HINT,
} from "@/pages/tasks/resumeAction";
import { describeApiError } from "@/pages/tasks/useExecutionRecords";
/** 续跑资格的加载状态 */
export type ResumeEligibilityState =
  /** 没有任务标识，未发起查询 */
  | { status: "idle" }
  /** 首次加载中，尚无快照 */
  | { status: "loading" }
  /** 已取得快照；`error` 非空表示刷新失败，快照可能不是最新 */
  | { status: "ready"; data: ResumeEligibilityResponse; error?: string }
  /** 查询失败且没有可用快照 */
  | { status: "error"; error: string };

/** 已取得的快照及其归属任务 */
export type ResumeSnapshot = {
  /** 该快照描述的任务标识；用于丢弃跨任务的迟到响应 */
  taskID: string;
  data: ResumeEligibilityResponse;
};

/**
 * 取出属于指定任务的快照。
 *
 * 快照描述的是某一个任务：任务切换后旧快照立刻失效，不能被当作当前任务的结论展示。
 */
export function resumeSnapshotForTask(
  snapshot: ResumeSnapshot | undefined,
  taskID: string,
): ResumeSnapshot | undefined {
  if (!snapshot || !taskID) return undefined;
  return snapshot.taskID === taskID ? snapshot : undefined;
}

/** 进入一次查询时的状态：已有同任务快照时保留展示，否则进入首次加载 */
export function resumeInitialState(
  snapshot: ResumeSnapshot | undefined,
  taskID: string,
): ResumeEligibilityState {
  if (!taskID) return { status: "idle" };
  const kept = resumeSnapshotForTask(snapshot, taskID);
  return kept ? { data: kept.data, status: "ready" } : { status: "loading" };
}

/** 查询成功：替换快照并清除上一次的刷新错误 */
export function resumeLoaded(
  taskID: string,
  data: ResumeEligibilityResponse,
): { snapshot: ResumeSnapshot; state: ResumeEligibilityState } {
  return { snapshot: { data, taskID }, state: { data, status: "ready" } };
}

/**
 * 查询失败。
 *
 * 只有属于同一任务的旧快照会被保留（并附上错误提示）；其他情况一律进入错误状态，
 * 绝不因为"上一次看起来可以续跑"而继续展示旧结论。
 */
export function resumeFailed(
  snapshot: ResumeSnapshot | undefined,
  taskID: string,
  error: string,
): { snapshot?: ResumeSnapshot; state: ResumeEligibilityState } {
  const kept = resumeSnapshotForTask(snapshot, taskID);
  if (kept) {
    return { snapshot: kept, state: { data: kept.data, error, status: "ready" } };
  }
  return { snapshot: undefined, state: { error, status: "error" } };
}

/**
 * 判断一次响应是否仍然有效。
 *
 * 同一任务可能因为显式刷新而并发多个请求，只有最后一次发起的请求结果会被采用；
 * 被取消或已过期的响应必须丢弃，不能覆盖更新的结论。
 */
export function resumeResponseAccepted(
  requestID: number,
  latestRequestID: number,
  aborted: boolean,
): boolean {
  return !aborted && requestID === latestRequestID;
}

/**
/** 一次恢复操作的状态 */
export type ResumeOperationState = {
  /** 是否有恢复请求在途 */
  running: boolean;
  /** 最近一次被服务端受理的真实响应；刷新失败不影响它 */
  outcome?: WorkflowRun;
  /** 请求被服务端拒绝的业务错误说明 */
  failure?: string;
  /** 请求结果不确定（超时 / 网络中断 / 取消）的明确提示 */
  uncertain?: string;
  /** 操作之后现场刷新失败的说明 */
  refreshError?: string;
};

/** Hook 的输入 */
export type UseResumeEligibilityOptions = {
  /** 当前打开的任务标识；为空时不查询资格，也不允许发起恢复 */
  taskID?: string;
  /** 显式刷新令牌：由调用方在需要重新查询资格时递增 */
  token?: number;
  /**
   * 恢复请求结束后的现场刷新：应重新读取任务详情与工作流进度。
   * 返回值是刷新失败的项，用于单独提示；刷新失败不覆盖已经取得的恢复结论。
   */
  refresh?: () => Promise<string[]>;
};

/** Hook 的输出 */
export type ResumeEligibility = {
  state: ResumeEligibilityState;
  /** 重新查询资格；不清除已取得的快照 */
  reload: () => void;
  operation: ResumeOperationState;
  /** 发起一次显式恢复；资格未确认、不属于当前任务或已在途时不发送请求 */
  resume: (body: Omit<ResumeWorkflowRequest, "run_id"> & { run_id: string }) => Promise<void>;
  /** 清除上一次操作结论 */
  clearOutcome: () => void;
};

/**
 * 加载指定任务的续跑资格，并管理恢复工作流操作。
 *
 * 返回的 `reload` 是用户显式刷新入口；它不会清除已取得的快照，刷新失败时只附加错误。
 * `taskID` 为空表示没有打开任务详情：不查询资格，也不允许发起恢复。
 */
export function useResumeEligibility(options: UseResumeEligibilityOptions): ResumeEligibility {
  const trimmed = options.taskID?.trim() ?? "";
  const token = options.token;
  const refresh = options.refresh;

  const [state, setState] = useState<ResumeEligibilityState>(() =>
    resumeInitialState(undefined, trimmed),
  );
  const [nonce, setNonce] = useState(0);
  const [operation, setOperation] = useState<ResumeOperationState>({ running: false });
  /** 最近一次成功取得的快照；按任务标识归属，跨任务不共享 */
  const snapshotRef = useRef<ResumeSnapshot | undefined>(undefined);
  /** 请求序号：只有最后一次发起的请求结果会被采用 */
  const requestRef = useRef(0);
  /** 当前打开的任务标识：供异步回调判断是否已切换任务 */
  const activeTaskRef = useRef("");
  activeTaskRef.current = trimmed;
  /** 恢复请求的票据与同步在途标记（同一帧内的连续点击不会让 state 及时更新） */
  const resumeGateRef = useRef(createResumeGate());
  const resumingRef = useRef(false);
  /** 卸载标记：卸载后不再用迟到响应更新界面 */
  const mountedRef = useRef(true);

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // 任务变化（含关闭详情）时，上一个任务的恢复结论立刻失效：它描述的是另一条执行记录。
  useEffect(() => {
    resumeGateRef.current = createResumeGate();
    resumingRef.current = false;
    setOperation({ running: false });
  }, [trimmed]);

  useEffect(() => {
    if (!trimmed) {
      snapshotRef.current = undefined;
      setState({ status: "idle" });
      return;
    }
    // 任务变化时旧快照立刻失效：它描述的是另一个任务，不能作为当前任务的结论。
    snapshotRef.current = resumeSnapshotForTask(snapshotRef.current, trimmed);
    setState(resumeInitialState(snapshotRef.current, trimmed));

    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    fetchTaskResumeEligibility(trimmed, { signal: controller.signal })
      .then(({ data }) => {
        if (!resumeResponseAccepted(requestID, requestRef.current, controller.signal.aborted))
          return;
        const applied = resumeLoaded(trimmed, data);
        snapshotRef.current = applied.snapshot;
        setState(applied.state);
      })
      .catch((cause: unknown) => {
        if (!resumeResponseAccepted(requestID, requestRef.current, controller.signal.aborted))
          return;
        const applied = resumeFailed(snapshotRef.current, trimmed, describeApiError(cause));
        snapshotRef.current = applied.snapshot;
        setState(applied.state);
      });
    return () => controller.abort();
  }, [trimmed, nonce, token]);

  /** 发起一次显式恢复：服务端仍会重新核验全部前置条件，前端只提交请求并如实展示结果 */
  const resume = useCallback(
    async (body: Omit<ResumeWorkflowRequest, "run_id"> & { run_id: string }) => {
      const target = activeTaskRef.current;
      if (!target || resumingRef.current) return;

      resumingRef.current = true;
      const ticket = beginResumeRequest(resumeGateRef.current, target);
      // 新的恢复一开始，上一次的结论就不再适用：只保留在途标记。
      setOperation({ running: true });

      const accepted = () =>
        mountedRef.current &&
        acceptResumeResponse(resumeGateRef.current, ticket, activeTaskRef.current, false);

      try {
        const { data } = await resumeTaskWorkflow(target, body);
        if (accepted()) setOperation({ outcome: data, running: false });
      } catch (cause) {
        if (accepted()) {
          const uncertain =
            cause instanceof ApiError && isUncertainResumeError(cause.kind)
              ? RESUME_UNKNOWN_OUTCOME_HINT
              : undefined;
          setOperation({ failure: describeApiError(cause), running: false, uncertain });
        }
      } finally {
        resumingRef.current = false;
      }

      // 无论响应成功还是失败，都以数据库中的真实状态为准刷新一次现场。
      // 刷新失败只记录提示，不覆盖已经取得的恢复结论。
      let failures: string[] = [];
      try {
        failures = refresh ? await refresh() : [];
      } catch (cause) {
        failures = [describeApiError(cause)];
      }
      if (!mountedRef.current || activeTaskRef.current !== target) return;
      setOperation((current) => ({
        ...current,
        refreshError:
          failures.length > 0
            ? `恢复请求已结束，但状态刷新失败：${failures.join("、")}。上方结论仍然有效，请手动刷新确认最新状态。`
            : undefined,
      }));
      // 重新查询一次续跑资格：恢复之后服务端结论通常已变化，用户必须看到最新结果。
      setNonce((value) => value + 1);
    },
    [refresh],
  );

  const clearOutcome = useCallback(() => setOperation({ running: false }), []);

  return { clearOutcome, operation, reload, resume, state };
}
