/**
 * 执行现场的只读数据加载。
 *
 * 三件事必须始终可区分：
 *   - `loading`：请求进行中，还没有任何结论；
 *   - `ready`：拿到了服务端返回的数据（数组可能为空，空是合法结果）；
 *   - `error`：请求失败，错误来自接口，不能被伪装成空列表。
 *
 * 刷新失败时保留上一次成功的数据：调用方可以在 `ready` 的同时看到 `error`，
 * 从而不把"刷新失败"误解为"现场变空了"。执行结论由页面单独保存，不会被这里
 * 的刷新失败覆盖。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { mergeEventPages } from "@/pages/tasks/history";

import {
  ApiError,
  fetchAttemptSessions,
  fetchStepAttempts,
  fetchStepRepairCycles,
  fetchStepValidations,
  fetchTaskCodeSaves,
  fetchTaskEvents,
  fetchTaskSteps,
  fetchValidationFeedback,
  type Attempt,
  type CodeSaveRecord,
  type RepairCycle,
  type Session,
  type Step,
  type TaskEvent,
  type ValidationFailureFeedback,
  type ValidationHistoryResponse,
} from "@/lib/api";

/** 一次只读加载的状态 */
export type LoadState<T> =
  /** 没有发起请求：缺少必要的标识 */
  | { status: "idle" }
  /** 首次加载中，还没有任何数据 */
  | { status: "loading" }
  /** 已拿到服务端数据；`error` 非空表示后续刷新失败，数据可能不是最新 */
  | { status: "ready"; data: T; error?: string }
  /** 请求失败且没有可用数据 */
  | { status: "error"; error: string };

/** 把未知错误转成可读说明，保留服务端返回的业务提示 */
export function describeApiError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动后重试。";
    if (error.kind === "timeout") return "请求超时，请稍后重试。";
    if (error.kind === "aborted") return "请求已取消。";
    if (error.kind === "invalid_response") return "服务返回的内容无法解析，请查看服务端日志。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 判断错误是否为"资源不存在"，用于把反馈 404 与真正的失败区分开 */
export function isNotFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

/**
 * 一次请求的通用加载器：忽略过期响应，支持手动刷新与外部刷新令牌。
 *
 * `owner` 是这批数据归属的记录标识（任务标识，或"任务 + 步骤"）。它变化意味着这是另一条
 * 记录，上一条记录的缓存必须立即失效：同一任务内切换步骤不会卸载步骤详情，若缓存保留，
 * 新步骤的首次查询失败时会把上一个步骤的尝试与验证历史当作"当前步骤的旧数据"长期展示。
 * 同一记录内的刷新失败仍然保留旧数据。
 *
 * `token` 由自动刷新提供：值变化时重新读取一次（同一任务、同一份服务端状态确实变了），
 * 调用方不需要知道定时器的存在。
 */
function useLoader<T>(
  load: ((signal: AbortSignal) => Promise<T>) | undefined,
  owner: string,
  token?: number,
): { state: LoadState<T>; reload: () => void } {
  const [state, setState] = useState<LoadState<T>>({ status: "idle" });
  const [nonce, setNonce] = useState(0);
  // 保存最近一次成功的数据：刷新失败时不清空，避免把"刷新失败"显示成"没有数据"。
  const dataRef = useRef<T | undefined>(undefined);
  /** 缓存归属的记录标识：与 `owner` 不一致时缓存必须作废 */
  const ownerRef = useRef("");
  const loadRef = useRef(load);
  loadRef.current = load;

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    // 归属变化：立刻丢弃上一条记录的缓存，避免跨任务 / 跨步骤展示别处的数据。
    // 这里不做"保留旧数据"的降级，因为旧数据描述的已经不是当前记录。
    if (ownerRef.current !== owner) {
      ownerRef.current = owner;
      dataRef.current = undefined;
    }
    const loader = loadRef.current;
    if (!loader) {
      dataRef.current = undefined;
      setState({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    if (dataRef.current === undefined) {
      setState({ status: "loading" });
    }
    loader(controller.signal)
      .then((data) => {
        if (controller.signal.aborted) return;
        dataRef.current = data;
        setState({ status: "ready", data });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        const message = describeApiError(cause);
        const previous = dataRef.current;
        if (previous === undefined) {
          setState({ error: message, status: "error" });
          return;
        }
        // 刷新失败：保留上一次的数据并附上错误，由页面同时展示。
        setState({ data: previous, error: message, status: "ready" });
      });
    return () => controller.abort();
    // owner 与 token 只用于触发重新加载；nonce 供手动刷新使用。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [owner, nonce, token]);

  return { reload, state };
}

/** 加载任务的全部步骤；任务不存在时返回错误而不是空列表 */
export function useTaskSteps(
  taskID?: string,
  token?: number,
): {
  state: LoadState<Step[]>;
  reload: () => void;
} {
  const trimmed = taskID?.trim() ?? "";
  const { state, reload } = useLoader<Step[]>(
    trimmed
      ? (signal) =>
          fetchTaskSteps(trimmed, { size: 100 }, { signal }).then(({ data }) => data.records)
      : undefined,
    trimmed,
    token,
  );
  return { reload, state };
}

/**
 * 加载任务的代码保存记录。
 *
 * 记录属于任务维度（不是某个步骤），因此这里只按任务标识查询。返回顺序完全采用
 * 服务端顺序（创建时间倒序），前端不重新排序。
 *
 * 空数组是合法结果（该任务还没有保存过代码），与查询失败、任务不存在必须区分：
 * 后两者走 `error` 分支，页面不得显示为"暂无记录"。
 */
export function useTaskCodeSaves(
  taskID?: string,
  token?: number,
): {
  state: LoadState<CodeSaveRecord[]>;
  reload: () => void;
} {
  const trimmed = taskID?.trim() ?? "";
  const { state, reload } = useLoader<CodeSaveRecord[]>(
    trimmed
      ? (signal) => fetchTaskCodeSaves(trimmed, { signal }).then(({ data }) => data.records)
      : undefined,
    trimmed,
    token,
  );
  return { reload, state };
}

/** 某步骤的执行现场：尝试列表、验证历史，以及每个尝试的会话 */
export type StepRecords = {
  attempts: Attempt[];
  validations?: ValidationHistoryResponse;
  /** 每个尝试关联的会话，按尝试标识索引 */
  sessions: Record<string, Session[]>;
};

/**
 * 加载某步骤的尝试、验证历史与会话。
 *
 * 会话按尝试逐个查询：执行记录接口只在尝试维度提供会话列表，没有"步骤下全部会话"
 * 的入口。任何一个尝试的会话查询失败都不会让整份现场变成错误，只是该尝试缺少会话。
 *
 * 缓存归属"任务 + 步骤"：换任务、换步骤都会作废上一次的尝试与验证历史，不能把上一个
 * 步骤的现场当作新步骤的旧数据展示。
 */
export function useStepRecords(
  taskID?: string,
  stepID?: string,
  token?: number,
): { state: LoadState<StepRecords>; reload: () => void } {
  const task = taskID?.trim() ?? "";
  const step = stepID?.trim() ?? "";
  const { state, reload } = useLoader<StepRecords>(
    task && step
      ? async (signal) => {
          const [attemptsResult, validationsResult] = await Promise.all([
            fetchStepAttempts(task, step, { size: 100 }, { signal }),
            fetchStepValidations(task, step, { signal }),
          ]);
          const attempts = attemptsResult.data.records;
          const sessions: Record<string, Session[]> = {};
          await Promise.all(
            attempts.map(async (attempt) => {
              try {
                const { data } = await fetchAttemptSessions(
                  task,
                  step,
                  attempt.id,
                  { size: 100 },
                  { signal },
                );
                sessions[attempt.id] = data.records;
              } catch {
                // 会话缺失不影响尝试与验证结论；该尝试的会话列表保持为空。
                sessions[attempt.id] = [];
              }
            }),
          );
          return { attempts, sessions, validations: validationsResult.data };
        }
      : undefined,
    `${task}/${step}`,
    token,
  );
  return { reload, state };
}

/** 一次验证失败反馈的加载结果：`not_found` 表示该轮验证没有失败反馈 */
export type FeedbackState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; feedback: ValidationFailureFeedback }
  | { status: "not_found" }
  | { status: "error"; error: string };

/**
 * 加载指定验证记录的失败反馈。
 *
 * 服务端在没有反馈时返回 404，这里单独表达为 `not_found`：它既不是错误，也不是
 * "验证通过"，而是"这一轮验证没有失败反馈记录"。
 */
export function useValidationFeedback(
  taskID?: string,
  stepID?: string,
  runID?: string,
): FeedbackState {
  const task = taskID?.trim() ?? "";
  const step = stepID?.trim() ?? "";
  const run = runID?.trim() ?? "";
  const [state, setState] = useState<FeedbackState>({ status: "idle" });

  useEffect(() => {
    if (!task || !step || !run) {
      setState({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    setState({ status: "loading" });
    fetchValidationFeedback(task, step, run, { signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted) return;
        setState({ feedback: data, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isNotFound(cause)) {
          setState({ status: "not_found" });
          return;
        }
        setState({ error: describeApiError(cause), status: "error" });
      });
    return () => controller.abort();
  }, [task, step, run]);

  return state;
}

/**
 * 事件时间线的分页大小。
 *
 * 与服务端默认值一致：历史记录一次不需要太多，用户按需"加载更多"即可。这个值
 * 只在用户显式打开或点击"加载更多"时才会被请求用到，不参与自动刷新周期。
 */
export const EVENT_PAGE_SIZE = 20;

/** 事件时间线的加载状态 */
export type EventTimelineState =
  | { status: "idle" }
  | { status: "loading" }
  | {
      status: "ready";
      events: TaskEvent[];
      /** 服务端给出的真实总数 */
      total: number;
      /** 是否还有后续记录 */
      hasMore: boolean;
      /** 加载更多进行中 */
      loadingMore: boolean;
      /** 刷新或加载更多失败；展示的仍是上一次成功取得的事件 */
      error?: string;
    }
  | { status: "error"; error: string };

/**
 * 加载任务事件时间线。
 *
 * 与步骤 / 尝试 / 验证的加载器刻意分开，原因有三：
 *   1. 事件历史是任务维度的，不随选中步骤变化；
 *   2. 它不参与 5 秒自动刷新：事件只在用户显式刷新、打开任务或"加载更多"时读取，
 *      避免把整个历史列表无条件放进刷新循环；
 *   3. 它需要分页累积，而不是每次覆盖。
 *
 * `token` 由父组件在服务端执行相关事实变化时递增，用来在"确实发生了新事件"时
 * 重新读取第一页；`taskID` 变化会重置累积结果，旧任务的迟到响应一律丢弃。
 */
export function useTaskEvents(
  taskID?: string,
  token?: number,
): {
  state: EventTimelineState;
  /** 重新读取第一页（手动刷新与失败重试共用） */
  reload: () => void;
  /** 追加下一页；没有后续记录或正在加载时不发起请求 */
  loadMore: () => void;
} {
  const trimmed = taskID?.trim() ?? "";
  const [state, setState] = useState<EventTimelineState>({ status: "idle" });
  const [nonce, setNonce] = useState(0);
  const [moreNonce, setMoreNonce] = useState(0);
  // 累积结果与归属任务：读取回调需要它们的最新值，不能依赖闭包快照。
  const eventsRef = useRef<TaskEvent[]>([]);
  const totalRef = useRef(0);
  const taskRef = useRef("");
  const loadingMoreRef = useRef(false);

  const reload = useCallback(() => setNonce((value) => value + 1), []);
  const loadMore = useCallback(() => {
    if (loadingMoreRef.current) return;
    if (eventsRef.current.length >= totalRef.current) return;
    loadingMoreRef.current = true;
    setState((current) =>
      current.status === "ready" ? { ...current, loadingMore: true } : current,
    );
    setMoreNonce((value) => value + 1);
  }, []);

  useEffect(() => {
    // 切换任务或卸载时清空累积结果，防止把上一个任务的事件拼进新任务列表。
    if (taskRef.current !== trimmed) {
      taskRef.current = trimmed;
      eventsRef.current = [];
      totalRef.current = 0;
      loadingMoreRef.current = false;
    }
    if (!trimmed) {
      eventsRef.current = [];
      totalRef.current = 0;
      setState({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    if (eventsRef.current.length === 0) {
      setState({ status: "loading" });
    }
    fetchTaskEvents(trimmed, { offset: 0, size: EVENT_PAGE_SIZE }, { signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted) return;
        eventsRef.current = data.records;
        totalRef.current = data.total;
        loadingMoreRef.current = false;
        setState({
          events: data.records,
          hasMore: data.records.length < data.total,
          loadingMore: false,
          status: "ready",
          total: data.total,
        });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        loadingMoreRef.current = false;
        const message = describeApiError(cause);
        const previous = eventsRef.current;
        if (previous.length === 0) {
          setState({ error: message, status: "error" });
          return;
        }
        // 刷新失败：保留上一次成功取得的事件，并单独提示失败，不伪装成空历史。
        setState({
          error: message,
          events: previous,
          hasMore: previous.length < totalRef.current,
          loadingMore: false,
          status: "ready",
          total: totalRef.current,
        });
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [trimmed, nonce, token]);

  useEffect(() => {
    if (moreNonce === 0) return;
    if (!trimmed) return;
    const controller = new AbortController();
    fetchTaskEvents(
      trimmed,
      { offset: eventsRef.current.length, size: EVENT_PAGE_SIZE },
      { signal: controller.signal },
    )
      .then(({ data }) => {
        if (controller.signal.aborted) return;
        const merged = mergeEventPages(eventsRef.current, data.records);
        eventsRef.current = merged;
        totalRef.current = data.total;
        loadingMoreRef.current = false;
        setState({
          events: merged,
          hasMore: merged.length < data.total,
          loadingMore: false,
          status: "ready",
          total: data.total,
        });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        loadingMoreRef.current = false;
        setState((current) =>
          current.status === "ready"
            ? { ...current, error: describeApiError(cause), loadingMore: false }
            : current,
        );
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [moreNonce, trimmed]);

  return { loadMore, reload, state };
}

/**
 * 加载某步骤的修复周期历史。
 *
 * 只读：不开启新周期、不关闭活动周期、不创建执行尝试。
 *
 * 与通用加载器的两点差异都是刻意的：
 *   - 步骤或任务变化时清空累积结果，避免把上一个步骤的周期显示在新步骤下；
 *   - 刷新失败保留上一次成功取得的记录并单独提示，不把失败显示成"暂无周期"。
 *
 * 旧步骤的迟到响应由 `AbortController` 丢弃，因此快速切换步骤不会被覆盖。
 */
export function useStepRepairCycles(
  taskID?: string,
  stepID?: string,
  token?: number,
): { state: LoadState<RepairCycle[]>; reload: () => void } {
  const task = taskID?.trim() ?? "";
  const step = stepID?.trim() ?? "";
  const [state, setState] = useState<LoadState<RepairCycle[]>>({ status: "idle" });
  const [nonce, setNonce] = useState(0);
  const dataRef = useRef<RepairCycle[] | undefined>(undefined);
  const ownerRef = useRef("");

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    const owner = `${task}/${step}`;
    if (ownerRef.current !== owner) {
      ownerRef.current = owner;
      dataRef.current = undefined;
    }
    if (!task || !step) {
      dataRef.current = undefined;
      setState({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    if (dataRef.current === undefined) {
      setState({ status: "loading" });
    }
    fetchStepRepairCycles(task, step, { signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted) return;
        dataRef.current = data.records;
        setState({ data: data.records, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        const message = describeApiError(cause);
        const previous = dataRef.current;
        if (previous === undefined) {
          setState({ error: message, status: "error" });
          return;
        }
        setState({ data: previous, error: message, status: "ready" });
      });
    return () => controller.abort();
  }, [nonce, step, task, token]);

  return { reload, state };
}
