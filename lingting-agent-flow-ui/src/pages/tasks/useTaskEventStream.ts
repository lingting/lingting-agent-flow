/**
 * 任务事件实时流（SSE）的连接生命周期。
 *
 * 职责边界：
 *   - 只在有明确任务标识、且历史游标已经就绪时建立连接；没有目标就不连接；
 *   - 连接只读：不提交任何执行、验证、重试、恢复、取消或代码保存请求；
 *   - 切换任务、关闭详情与卸载页面都会关闭旧连接，旧任务的迟到帧不会进入新任务；
 *   - 断线按 SSE 的恢复语义重连（新连接带真实事件游标），不是按固定周期轮询数据；
 *   - 连接状态与事件加载状态分开：这里只表达"推送通道怎样了"，不表达历史是否为空。
 *
 * 与历史分页的关系：实时列表只累积"连接建立之后"收到的事件，服务端从游标之后开始
 * 推送，因此不会与历史分页重复。历史分页是否连续由面板自己如实展示，这里不做任何
 * 补全，也不把实时事件混进历史列表。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { taskEventStreamUrl, type TaskEvent } from "@/lib/api";
import { mergeLiveEvents } from "@/pages/tasks/history";

/** 实时推送通道的状态；与事件加载状态是两件事 */
export type EventStreamStatus =
  /** 没有任务目标或历史游标未就绪：不建立连接 */
  | "idle"
  /** 首次连接建立中 */
  | "connecting"
  /** 连接已建立；此时可能还没有收到任何事件 */
  | "live"
  /** 连接中断，正在按退避重新连接 */
  | "reconnecting"
  /** 已停止自动重连，等待用户手动重连 */
  | "error";

/** 实时推送通道的对外状态 */
export type TaskEventStreamState = {
  status: EventStreamStatus;
  /** 连接建立后收到的事件，按 `(created_at, id)` 升序、按真实事件 `id` 去重 */
  events: TaskEvent[];
  /**
   * 连接状态说明。
   *
   * `live` 时缺省；`reconnecting` 与 `error` 时给出可读原因，页面必须显示它，
   * 不能把"连接断了"显示成"暂无事件"。
   */
  hint?: string;
};

/** 重连退避的起始间隔（毫秒） */
export const EVENT_STREAM_RECONNECT_BASE_MS = 1_000;
/** 重连退避的最大间隔（毫秒）：封顶后仍会重试，不会退化成高频请求 */
export const EVENT_STREAM_RECONNECT_MAX_MS = 15_000;
/**
 * 连续失败多少次后停止自动重连。
 *
 * 停止而不是无限重试：服务可能已经停止或被删除任务，无限后台重连只会持续制造无用
 * 请求。停止后由用户点「重新连接」显式恢复，历史查询不受影响。
 */
export const EVENT_STREAM_MAX_FAILURES = 5;

/** 按失败次数计算下一次重连的等待时间 */
export function eventStreamReconnectDelay(failures: number): number {
  if (failures <= 1) return EVENT_STREAM_RECONNECT_BASE_MS;
  const grown = EVENT_STREAM_RECONNECT_BASE_MS * 2 ** (failures - 1);
  return Math.min(grown, EVENT_STREAM_RECONNECT_MAX_MS);
}

/** 把一帧数据解析成事件；形状不符时返回 undefined，不把畸形帧当作事件 */
function parseStreamEvent(data: string): TaskEvent | undefined {
  if (!data) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(data) as unknown;
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) return undefined;
  const candidate = parsed as Partial<TaskEvent>;
  if (typeof candidate.id !== "number" || !Number.isFinite(candidate.id)) return undefined;
  if (typeof candidate.task_id !== "string" || !candidate.task_id) return undefined;
  if (typeof candidate.event_type !== "string" || !candidate.event_type) return undefined;
  if (typeof candidate.created_at !== "string" || !candidate.created_at) return undefined;
  return {
    ...candidate,
    id: candidate.id,
    task_id: candidate.task_id,
    event_type: candidate.event_type,
    created_at: candidate.created_at,
    type_known: candidate.type_known === true,
  } as TaskEvent;
}

/** 初始状态 */
const IDLE_STATE: TaskEventStreamState = { events: [], status: "idle" };

/**
 * 建立并维护任务事件的实时推送连接。
 *
 * `initialCursor` 必须来自历史读取时刻的最新事件标识：它在建立第一条连接时使用且只用
 * 一次，之后就由收到的帧推进。故意不把后来的 `initialCursor` 当成新游标——历史刷新
 * 会让该值变大，若跟着跳过去，已经推送但尚未处理的中间事件就被跳过了。
 */
export function useTaskEventStream(options: {
  taskID?: string;
  initialCursor?: number;
  enabled?: boolean;
}): {
  state: TaskEventStreamState;
  /** 手动重新连接（自动重连停止后使用） */
  reconnect: () => void;
} {
  const trimmed = options.taskID?.trim() ?? "";
  const enabled = options.enabled === true;
  const initialCursor = options.initialCursor;

  const [state, setState] = useState<TaskEventStreamState>(IDLE_STATE);

  /** 当前连接归属的任务：回调只用它，避免闭包读到上一次的取值 */
  const taskRef = useRef("");
  /** 连接代次：任务切换后自增，旧连接的任何回调都会因代次不符而被丢弃 */
  const generationRef = useRef(0);
  /** 已送达的最新事件标识，是重连时使用的真实游标 */
  const cursorRef = useRef<number | undefined>(undefined);
  /** 是否为当前任务建立过第一条连接 */
  const startedRef = useRef(false);
  /** 连续失败次数 */
  const failuresRef = useRef(0);
  const sourceRef = useRef<EventSource | undefined>(undefined);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const clearTimer = useCallback(() => {
    if (timerRef.current === undefined) return;
    clearTimeout(timerRef.current);
    timerRef.current = undefined;
  }, []);

  const closeSource = useCallback(() => {
    const source = sourceRef.current;
    sourceRef.current = undefined;
    source?.close();
  }, []);

  const connectRef = useRef<() => void>(() => {});

  const connect = useCallback(() => {
    const target = taskRef.current;
    if (!target) return;
    closeSource();
    if (typeof EventSource === "undefined") {
      setState({
        events: [],
        hint: "当前环境不支持事件推送（EventSource 不可用），历史事件仍可正常查询。",
        status: "error",
      });
      return;
    }
    const generation = generationRef.current;
    const source = new EventSource(taskEventStreamUrl(target, cursorRef.current));
    sourceRef.current = source;
    setState((current) => ({
      ...current,
      hint: undefined,
      status: current.status === "reconnecting" ? "reconnecting" : "connecting",
    }));

    source.onopen = () => {
      if (generation !== generationRef.current) return;
      failuresRef.current = 0;
      setState((current) => ({ ...current, hint: undefined, status: "live" }));
    };

    source.onmessage = (message: MessageEvent<string>) => {
      if (generation !== generationRef.current) return;
      const event = parseStreamEvent(message.data);
      // 畸形帧与跨任务事件一律丢弃：宁可少一行，也不把别的任务的事件显示在这里。
      if (!event || event.task_id !== target) return;
      const previous = cursorRef.current ?? 0;
      if (event.id > previous) cursorRef.current = event.id;
      failuresRef.current = 0;
      setState((current) => ({
        events: mergeLiveEvents(current.events, [event]),
        hint: undefined,
        status: "live",
      }));
    };

    source.onerror = () => {
      // 主动关闭而不是交给浏览器自动重连：浏览器重连时不带查询参数，游标只能依赖
      // 它自己回传的 Last-Event-ID，而本项目希望由已确认收到的事件标识决定游标。
      source.close();
      if (sourceRef.current === source) sourceRef.current = undefined;
      if (generation !== generationRef.current) return;
      failuresRef.current += 1;
      const exhausted = failuresRef.current > EVENT_STREAM_MAX_FAILURES;
      if (exhausted) {
        setState((current) => ({
          ...current,
          hint: "事件推送连接多次重连失败，已停止自动重连；可以手动重新连接，历史事件仍可正常查询。",
          status: "error",
        }));
        return;
      }
      const delay = eventStreamReconnectDelay(failuresRef.current);
      setState((current) => ({
        ...current,
        hint: `事件推送连接已断开，正在重新连接…（约 ${Math.round(delay / 1000)} 秒后重试）`,
        status: "reconnecting",
      }));
      clearTimer();
      timerRef.current = setTimeout(() => {
        timerRef.current = undefined;
        if (generation !== generationRef.current) return;
        connectRef.current();
      }, delay);
    };
  }, [clearTimer, closeSource]);

  connectRef.current = connect;

  // 任务归属变化与卸载：关闭旧连接、清空累积、重置游标。旧连接的回调因代次变化失效。
  useEffect(() => {
    generationRef.current += 1;
    startedRef.current = false;
    cursorRef.current = undefined;
    failuresRef.current = 0;
    clearTimer();
    closeSource();
    taskRef.current = trimmed;
    setState(IDLE_STATE);
    return () => {
      generationRef.current += 1;
      clearTimer();
      closeSource();
    };
  }, [clearTimer, closeSource, trimmed]);

  // 历史游标就绪后建立第一条连接。之后的游标只由收到的帧推进，不再重读该参数。
  useEffect(() => {
    if (!trimmed || !enabled) return;
    if (initialCursor === undefined) return;
    if (startedRef.current) return;
    startedRef.current = true;
    cursorRef.current = initialCursor;
    connect();
  }, [connect, enabled, initialCursor, trimmed]);

  /** 手动重连：重置失败计数后立即重连一次 */
  const reconnect = useCallback(() => {
    if (!taskRef.current) return;
    clearTimer();
    failuresRef.current = 0;
    startedRef.current = true;
    connectRef.current();
  }, [clearTimer]);

  return { reconnect, state };
}
