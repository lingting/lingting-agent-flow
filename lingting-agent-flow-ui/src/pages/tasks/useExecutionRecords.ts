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

import {
  ApiError,
  fetchAttemptSessions,
  fetchStepAttempts,
  fetchStepValidations,
  fetchTaskCodeSaves,
  fetchTaskSteps,
  fetchValidationFeedback,
  type Attempt,
  type CodeSaveRecord,
  type Session,
  type Step,
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

/** 一次请求的通用加载器：忽略过期响应，支持手动刷新 */
function useLoader<T>(
  load: ((signal: AbortSignal) => Promise<T>) | undefined,
  deps: readonly unknown[],
): { state: LoadState<T>; reload: () => void } {
  const [state, setState] = useState<LoadState<T>>({ status: "idle" });
  const [nonce, setNonce] = useState(0);
  // 保存最近一次成功的数据：刷新失败时不清空，避免把"刷新失败"显示成"没有数据"。
  const dataRef = useRef<T | undefined>(undefined);
  const loadRef = useRef(load);
  loadRef.current = load;

  const reload = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
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
    // deps 由调用方给出，nonce 只用于触发手动刷新。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  return { reload, state };
}

/** 加载任务的全部步骤；任务不存在时返回错误而不是空列表 */
export function useTaskSteps(taskID?: string): {
  state: LoadState<Step[]>;
  reload: () => void;
} {
  const trimmed = taskID?.trim() ?? "";
  const { state, reload } = useLoader<Step[]>(
    trimmed
      ? (signal) =>
          fetchTaskSteps(trimmed, { size: 100 }, { signal }).then(({ data }) => data.records)
      : undefined,
    [trimmed],
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
export function useTaskCodeSaves(taskID?: string): {
  state: LoadState<CodeSaveRecord[]>;
  reload: () => void;
} {
  const trimmed = taskID?.trim() ?? "";
  const { state, reload } = useLoader<CodeSaveRecord[]>(
    trimmed
      ? (signal) => fetchTaskCodeSaves(trimmed, { signal }).then(({ data }) => data.records)
      : undefined,
    [trimmed],
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
 */
export function useStepRecords(
  taskID?: string,
  stepID?: string,
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
    [task, step],
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
