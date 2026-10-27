/**
 * 任务执行进度自动刷新的契约与调度器。
 *
 * 三条设计约束：
 *   1. 是否"仍在推进"只依据服务端返回的事实，未知取值一律不当作活动态，也不当作终态；
 *   2. 页面只有一个调度器：周期、启动窗口与停止提示都在这里定义，页面与子面板不再各自建定时器；
 *   3. 调度采用"上一轮结算后才排下一轮"的递归定时器，页面隐藏时暂停，恢复可见时立即刷新一次。
 *
 * 领域依据（Go 端）：
 *   - `domain.WorkflowRunStatus.IsActive()` 只包含 `running` 与 `cancel_requested`
 *     （`internal/domain/workflow_run.go`）；
 *   - `domain.WorkflowRunStatus.IsTerminal()` 只包含 `completed`、`cancelled`、`failed`；
 *   - `blocked` 与 `recovery_required` 既不是活动态也不是终态：自动推进已停止，等待人工处置；
 *   - `pending` 表示已登记但尚未开始推进，只在启动窗口内有限次刷新；
 *   - `TASK_STATUS_META` 的 `active` 标记与 `domain.TaskStatus.IsActive()` 的取值集合一致
 *     （`internal/domain/task.go`），因此"任务处于执行阶段但还没有执行记录"同样只在窗口内刷新。
 *
 * 前端不新增状态名，也不把未知取值解释为成功、终态或活动态。
 */
import type { Task, WorkflowStatusResponse } from "@/lib/api";
import {
  isKnownTaskStatus,
  isKnownWorkflowRunStatus,
  TASK_STATUS_META,
  WORKFLOW_RUN_STATUS_META,
} from "@/pages/tasks/status";

/** 自动刷新周期（毫秒）：任务详情与执行进度统一使用这一取值 */
export const AUTO_REFRESH_INTERVAL_MS = 5_000;

/**
 * 启动窗口内允许的最大自动刷新轮数。
 *
 * `pending` 的执行记录，以及"任务已进入执行阶段但还没有执行记录"，都属于短暂过渡状态：
 * 它们需要刷新才能及时看到推进，但不能永久高频请求，因此限定窗口（12 × 5 秒）。
 * 窗口结束只停止自动刷新并提示手动刷新，不据此推断任务已停止或已完成。
 */
export const AUTO_REFRESH_STARTUP_MAX_ROUNDS = 12;

/**
 * 首轮读取失败时排定的重试轮数。
 *
 * 首次加载失败不会让详情永久停在"无法获取"：在有限轮内按同一周期重试；重试也不会
 * 把失败解释成任务结束或工作流终止。
 */
export const AUTO_REFRESH_STARTUP_RETRY_ROUNDS = 3;

/** 停止自动刷新的原因：只描述"为什么不再按时刷新"，不表达任何业务结论 */
export type AutoRefreshOffReason =
  /** 没有打开任务详情 */
  | "no_task"
  /** 服务端返回了未知的任务状态 */
  | "unknown_task_status"
  /** 服务端返回了未知的工作流执行状态 */
  | "unknown_workflow_status"
  /** 任务尚未进入执行阶段（等待执行、已暂停、待核验、已结束） */
  | "not_started"
  /** 工作流已停止自动推进，等待人工处置（blocked / recovery_required） */
  | "awaiting_manual_action"
  /** 本次工作流执行已经结束（completed / cancelled / failed） */
  | "workflow_terminal"
  /** 启动窗口的自动刷新轮数已经用尽 */
  | "startup_window_elapsed";

/** 自动刷新策略：持续刷新、只在启动窗口内刷新、或停止刷新 */
export type AutoRefreshPolicy =
  | { mode: "continuous" }
  | { mode: "bounded"; maxRounds: number }
  | { mode: "off"; reason: AutoRefreshOffReason };

/** 停止自动刷新的提示文案（只说明刷新行为，不描述任务成功或失败） */
export type AutoRefreshStopHintKey = AutoRefreshOffReason;

export const AUTO_REFRESH_STOPPED_HINT: Record<AutoRefreshStopHintKey, string> = {
  awaiting_manual_action: "工作流已停止自动推进并等待人工处置，已停止自动刷新；需要时请手动刷新。",
  no_task: "未打开任务详情，未进行自动刷新。",
  not_started: "任务当前不在执行阶段，已停止自动刷新；需要时请手动刷新。",
  startup_window_elapsed:
    "启动阶段的自动刷新窗口已结束，仍未取得推进记录；已停止自动刷新，请手动刷新确认最新状态。",
  unknown_task_status: "服务端返回了未知的任务状态，已停止自动刷新；请手动刷新确认最新状态。",
  unknown_workflow_status: "服务端返回了未知的工作流状态，已停止自动刷新；请手动刷新确认最新状态。",
  workflow_terminal: "本次工作流执行已结束，已停止自动刷新；需要时请手动刷新。",
};

/**
 * 依据服务端返回的任务状态与工作流执行状态决定自动刷新策略。
 *
 * 判定顺序：
 *   1. 没有任务 → 不刷新；
 *   2. 任务状态未知 → 不刷新（保守处理，不猜测语义）；
 *   3. 有执行记录时以执行记录为准：活动态 → 持续刷新；终态 → 停止；`pending` → 启动窗口；
 *      `blocked` / `recovery_required` → 停止并等待人工；状态未知 → 停止；
 *   4. 没有执行记录时看任务状态：处于执行阶段 → 启动窗口；否则停止。
 *
 * 这里只回答"是否继续刷新"，不会读取或写入任何业务状态。
 */
export function decideAutoRefresh(input: {
  task?: Task;
  workflow?: WorkflowStatusResponse;
}): AutoRefreshPolicy {
  const { task, workflow } = input;
  if (!task) return { mode: "off", reason: "no_task" };
  if (!isKnownTaskStatus(task.status)) return { mode: "off", reason: "unknown_task_status" };

  const run = workflow?.run;
  if (run) {
    if (!isKnownWorkflowRunStatus(run.status)) {
      return { mode: "off", reason: "unknown_workflow_status" };
    }
    const meta = WORKFLOW_RUN_STATUS_META[run.status];
    if (meta.terminal) return { mode: "off", reason: "workflow_terminal" };
    if (meta.active) return { mode: "continuous" };
    if (run.status === "pending") {
      return { mode: "bounded", maxRounds: AUTO_REFRESH_STARTUP_MAX_ROUNDS };
    }
    return { mode: "off", reason: "awaiting_manual_action" };
  }

  if (TASK_STATUS_META[task.status].active) {
    return { mode: "bounded", maxRounds: AUTO_REFRESH_STARTUP_MAX_ROUNDS };
  }
  return { mode: "off", reason: "not_started" };
}

/** 定时器契约：允许测试注入可控的假定时器，不依赖真实等待 */
export type TimerApi = {
  setTimeout(handler: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
};

/** 调度器选项 */
export type RefreshSchedulerOptions = {
  /** 一轮刷新：只读取状态；抛出的错误不会改变节奏 */
  run: () => Promise<void>;
  /** 刷新周期，缺省使用 `AUTO_REFRESH_INTERVAL_MS` */
  intervalMs?: number;
  /** 初始策略，缺省为持续刷新；真实状态取得后会由 `setPolicy` 覆盖 */
  policy?: AutoRefreshPolicy;
  /** 页面是否可见；不可见时暂停排定下一轮 */
  isVisible?: () => boolean;
  /** 定时器实现，缺省使用浏览器 / Node 的全局定时器 */
  timer?: TimerApi;
  /** 启动窗口用尽时回调，用于给出"已停止自动刷新"的提示 */
  onExhausted?: () => void;
};
/**
 * 调度器对外能力。
 *
 * 策略（是否继续刷新）与节奏（多久一轮）分离：策略由调用方依据服务端真实状态传入，
 * 调度器本身不判断业务语义。
 */
export type RefreshScheduler = {
  /** 开始调度：第一轮在完整周期之后才会发生 */
  start(): void;
  /** 停止调度、清空待执行定时器并中止在途一轮 */
  stop(): void;
  /** 更新策略：`off` 立即停表；`bounded` 重新计数窗口；`continuous` 不设上限 */
  setPolicy(policy: AutoRefreshPolicy): void;
  /** 页面可见性变化：隐藏时暂停，恢复可见时立即刷新一次 */
  setVisible(visible: boolean): void;
  /** 立即刷新一次（手动刷新复用同一入口，不会与在途一轮并发） */
  refreshNow(): void;
  /** 放弃待执行的定时器，但不打断可能在进行的一轮 */
  cancelScheduled(): void;
  /**
   * 重新排定下一轮（手动刷新之后调用）。
   *
   * 手动刷新会清掉待执行定时器以避免同一时刻两轮请求；它结算后必须把节奏接回来，
   * 否则自动刷新会在没有任何提示的情况下永久停止。
   */
  scheduleNext(): void;
  /** 本轮已经被打开的执行记录选为"不再刷新" */
  hasStopped(): boolean;
  /** 策略是否为持续刷新 */
  isContinuous(): boolean;
  /** 已完成的刷新轮数（含手动与恢复前台触发的轮次） */
  completedRounds(): number;
  /** 当前策略是否为 `off` */
  isOff(): boolean;
  /** 当前是否有一轮在途 */
  isRefreshing(): boolean;
};

const DEFAULT_TIMER: TimerApi = {
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
  setTimeout: (handler, ms) => setTimeout(handler, ms),
};

/**
 * 创建一个串行的刷新调度器。
 *
 * 并发规则：任意时刻最多一轮在途，上一轮结算后才排下一轮；在途期间的 `refreshNow`
 * 不会发起第二轮（在途一轮已经会带来最新数据）。策略为 `off` 时立即清空待执行定时器，
 * 但不会中断已经发出的一轮，也不会删除它带来的真实数据。
 */
export function createRefreshScheduler(options: RefreshSchedulerOptions): RefreshScheduler {
  const intervalMs = options.intervalMs ?? AUTO_REFRESH_INTERVAL_MS;
  const timer = options.timer ?? DEFAULT_TIMER;
  const isVisible = options.isVisible ?? (() => true);

  let started = false;
  let stopped = false;
  let exhausted = false;
  let refreshing = false;
  let completed = 0;
  let handle: unknown;
  let controller: AbortController | undefined;
  let policy: AutoRefreshPolicy = options.policy ?? { mode: "continuous" };

  const cancelTimer = () => {
    if (handle === undefined) return;
    timer.clearTimeout(handle);
    handle = undefined;
  };

  function schedule() {
    if (!started || stopped || exhausted || refreshing) return;
    if (policy.mode === "off") return;
    if (handle !== undefined) return;
    if (!isVisible()) return;
    handle = timer.setTimeout(() => {
      handle = undefined;
      void refreshRound();
    }, intervalMs);
  }

  const finish = () => {
    refreshing = false;
    controller = undefined;
    if (stopped) return;
    completed += 1;
    if (policy.mode === "bounded" && completed >= policy.maxRounds) {
      exhausted = true;
      policy = { mode: "off", reason: "startup_window_elapsed" };
      options.onExhausted?.();
      return;
    }
    schedule();
  };

  async function refreshRound() {
    if (stopped || refreshing) return;
    refreshing = true;
    controller = new AbortController();
    try {
      await options.run();
    } catch {
      // 单轮失败既不加快也不放慢节奏：周期只由配置决定，失败提示由刷新实现自己给出。
    } finally {
      finish();
    }
  }

  return {
    cancelScheduled: cancelTimer,
    completedRounds: () => completed,
    hasStopped: () => stopped,
    isContinuous: () => policy.mode === "continuous",
    isOff: () => policy.mode === "off",
    isRefreshing: () => refreshing,
    refreshNow: () => {
      if (stopped || !isVisible()) return;
      cancelTimer();
      void refreshRound();
    },
    scheduleNext: schedule,
    setPolicy: (next) => {
      policy = next;
      if (next.mode === "off") {
        // 停止排定下一轮：已经发出的一轮仍会结算，其真实数据不会被丢弃。
        exhausted = false;
        cancelTimer();
        return;
      }
      if (started && !stopped) {
        exhausted = false;
        schedule();
      }
    },
    setVisible: (visible) => {
      if (!visible) {
        cancelTimer();
        return;
      }
      if (stopped) return;
      // 从后台恢复：立即刷新一次，不等待完整的下一个周期。
      cancelTimer();
      void refreshRound();
    },
    start: () => {
      if (started || stopped) return;
      started = true;
      schedule();
    },
    stop: () => {
      stopped = true;
      started = false;
      cancelTimer();
      controller?.abort();
      controller = undefined;
    },
  };
}
