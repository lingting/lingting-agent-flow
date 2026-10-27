/**
 * 任务续跑资格（`GET /tasks/{id}/resume-eligibility`）的只读加载。
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
 * 状态迁移被抽成纯函数（`resumeSnapshotForTask`、`resumeInitialState`、
 * `resumeLoaded`、`resumeFailed`），使竞态与保留旧数据的行为可以被直接验证，
 * 而不依赖浏览器环境。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { fetchTaskResumeEligibility, type ResumeEligibilityResponse } from "@/lib/api";
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
 * 加载指定任务的续跑资格。
 *
 * 返回的 `reload` 是用户显式刷新入口；它不会清除已取得的快照，刷新失败时只附加错误。
 */
export function useResumeEligibility(
  taskID?: string,
  token?: number,
): {
  state: ResumeEligibilityState;
  reload: () => void;
} {
  const trimmed = taskID?.trim() ?? "";
  const [state, setState] = useState<ResumeEligibilityState>(() =>
    resumeInitialState(undefined, trimmed),
  );
  const [nonce, setNonce] = useState(0);
  /** 最近一次成功取得的快照；按任务标识归属，跨任务不共享 */
  const snapshotRef = useRef<ResumeSnapshot | undefined>(undefined);
  /** 请求序号：只有最后一次发起的请求结果会被采用 */
  const requestRef = useRef(0);

  const reload = useCallback(() => setNonce((value) => value + 1), []);

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

  return { reload, state };
}
