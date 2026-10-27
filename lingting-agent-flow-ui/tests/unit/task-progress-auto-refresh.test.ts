import { afterEach, describe, expect, it, vi } from "vitest";

import type { Task, WorkflowRun, WorkflowStatusResponse } from "../../src/lib/api";
import {
  AUTO_REFRESH_INTERVAL_MS,
  AUTO_REFRESH_STARTUP_MAX_ROUNDS,
  AUTO_REFRESH_STARTUP_RETRY_ROUNDS,
  AUTO_REFRESH_STOPPED_HINT,
  createRefreshScheduler,
  decideAutoRefresh,
  type AutoRefreshPolicy,
  type TimerApi,
} from "../../src/pages/tasks/autoRefresh";
import { applySnapshotRound, refreshHintFor } from "../../src/pages/tasks/liveSnapshot";
import {
  acceptResponse,
  beginRequest,
  createRequestGate,
  describeRefreshFailures,
} from "../../src/pages/tasks/requestGate";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份任务样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function task(overrides: Partial<Task> = {}): Task {
  return {
    cancel_requested: false,
    created_at: "2026-10-26T08:00:00Z",
    description: "描述",
    id: "task-1",
    name: "任务",
    project_id: "",
    status: "developing",
    status_finished: false,
    status_terminal: false,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一条工作流执行记录样本，字段与 Go 端 `httpapi.WorkflowRunResponse` 一致 */
function run(overrides: Partial<WorkflowRun> = {}): WorkflowRun {
  return {
    cancel_requested: false,
    code_saver_available: true,
    completed_steps: 0,
    last_completed_sequence: 0,
    plan_digest: "digest",
    revision: 1,
    run_id: "run-1",
    stage: "step_execution",
    status: "running",
    status_active: true,
    status_terminal: false,
    task_id: "task-1",
    total_steps: 2,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一份工作流状态响应样本 */
function workflow(overrides: Partial<WorkflowStatusResponse> = {}): WorkflowStatusResponse {
  return {
    has_run: true,
    run: run(),
    steps: [],
    task_id: "task-1",
    task_status: "developing",
    ...overrides,
  };
}

/** 可控的假定时器：不依赖真实等待，能精确断言排定与清理 */
function fakeTimer() {
  const pending = new Map<number, { handler: () => void; ms: number }>();
  let nextHandle = 1;
  const api: TimerApi = {
    clearTimeout: (handle) => {
      pending.delete(handle as number);
    },
    setTimeout: (handler, ms) => {
      const handle = nextHandle++;
      pending.set(handle, { handler, ms });
      return handle;
    },
  };
  return {
    api,
    clearCount: () => pending.size,
    /**
     * 触发最早排定的定时器一次。
     *
     * 返回定时器排队等待的毫秒数，便于断言周期。
     */
    async fire(): Promise<number | undefined> {
      const entry = [...pending.entries()][0];
      if (!entry) return undefined;
      pending.delete(entry[0]);
      entry[1].handler();
      // 让本轮异步回调结算，之后才会排定下一轮。
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      return entry[1].ms;
    },
    pendingCount: () => pending.size,
    /** 排定过的周期，按触发顺序 */
    msList: () => [...pending.values()].map((item) => item.ms),
  };
}

/** 可手动结算的一轮刷新 */
function deferredRound() {
  let release: () => void = () => {};
  let reject: (cause: unknown) => void = () => {};
  let started = 0;
  const promise = () =>
    new Promise<void>((resolve, fail) => {
      started += 1;
      release = resolve;
      reject = fail;
    });
  return {
    promise,
    reject: (cause: unknown) => reject(cause),
    release: () => release(),
    started: () => started,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("自动刷新周期与契约常量", () => {
  it("周期约 5 秒，集中在同一处定义", () => {
    expect(AUTO_REFRESH_INTERVAL_MS).toBe(5_000);
  });

  it("启动窗口是有限轮数，不是永久高频轮询", () => {
    expect(AUTO_REFRESH_STARTUP_MAX_ROUNDS).toBeGreaterThan(0);
    expect(AUTO_REFRESH_STARTUP_MAX_ROUNDS).toBeLessThanOrEqual(30);
    expect(AUTO_REFRESH_STARTUP_RETRY_ROUNDS).toBeGreaterThan(0);
    expect(AUTO_REFRESH_STARTUP_RETRY_ROUNDS).toBeLessThanOrEqual(AUTO_REFRESH_STARTUP_MAX_ROUNDS);
  });

  it("每种停止原因都有只描述刷新行为的提示文案", () => {
    for (const [reason, hint] of Object.entries(AUTO_REFRESH_STOPPED_HINT)) {
      expect(hint.length).toBeGreaterThan(0);
      // 提示不得把停止刷新表达成任务成功或失败
      expect(hint).not.toContain("任务成功");
      expect(hint).not.toContain("任务失败");
      if (reason !== "no_task") expect(hint).toContain("手动刷新");
    }
  });
});

describe("活动状态判定（对齐 Go 端领域规则）", () => {
  it("没有打开任务详情时不刷新", () => {
    expect(decideAutoRefresh({})).toEqual({ mode: "off", reason: "no_task" });
  });

  it("running 与 cancel_requested 是活动态，持续刷新", () => {
    for (const status of ["running", "cancel_requested"] as const) {
      expect(
        decideAutoRefresh({ task: task(), workflow: workflow({ run: run({ status }) }) }),
      ).toEqual({ mode: "continuous" });
    }
  });

  it("completed、cancelled、failed 是终态，停止高频轮询", () => {
    for (const status of ["completed", "cancelled", "failed"] as const) {
      expect(
        decideAutoRefresh({ task: task(), workflow: workflow({ run: run({ status }) }) }),
      ).toEqual({ mode: "off", reason: "workflow_terminal" });
    }
  });

  it("blocked 与 recovery_required 等待人工处置，停止高频轮询", () => {
    for (const status of ["blocked", "recovery_required"] as const) {
      expect(
        decideAutoRefresh({ task: task(), workflow: workflow({ run: run({ status }) }) }),
      ).toEqual({ mode: "off", reason: "awaiting_manual_action" });
    }
  });

  it("pending 只在启动窗口内刷新，不永久高频轮询", () => {
    const policy = decideAutoRefresh({
      task: task({ status: "preparing" }),
      workflow: workflow({ run: run({ status: "pending", status_active: false }) }),
    });
    expect(policy).toEqual({ mode: "bounded", maxRounds: AUTO_REFRESH_STARTUP_MAX_ROUNDS });
  });

  it("未知工作流状态既不算活动态也不算终态，且不触发任何自动操作", () => {
    const policy = decideAutoRefresh({
      task: task(),
      // 服务端新增状态：前端不得猜测语义
      workflow: workflow({ run: run({ status: "future_status" as WorkflowRun["status"] }) }),
    });
    expect(policy).toEqual({ mode: "off", reason: "unknown_workflow_status" });
    expect(refreshHintFor(policy)).toBe(AUTO_REFRESH_STOPPED_HINT.unknown_workflow_status);
  });

  it("未知任务状态保守停止刷新", () => {
    expect(
      decideAutoRefresh({ task: task({ status: "future_status" as Task["status"] }) }),
    ).toEqual({ mode: "off", reason: "unknown_task_status" });
  });

  it("尚未启动工作流且任务不在执行阶段时不刷新", () => {
    for (const status of ["queued", "paused", "completed", "failed", "cancelled"] as const) {
      expect(
        decideAutoRefresh({
          task: task({ status }),
          workflow: { has_run: false, steps: [], task_id: "task-1", task_status: status },
        }),
      ).toEqual({ mode: "off", reason: "not_started" });
    }
  });

  it("任务已进入执行阶段但还没有执行记录时使用启动窗口", () => {
    for (const status of ["preparing", "planning", "planned", "developing", "verifying"] as const) {
      expect(
        decideAutoRefresh({
          task: task({ status }),
          workflow: { has_run: false, steps: [], task_id: "task-1", task_status: status },
        }),
      ).toEqual({ mode: "bounded", maxRounds: AUTO_REFRESH_STARTUP_MAX_ROUNDS });
    }
  });
});

describe("刷新调度器", () => {
  it("首轮在完整周期之后才发生，周期为 5 秒", async () => {
    const timer = fakeTimer();
    const runs: number[] = [];
    const scheduler = createRefreshScheduler({
      run: async () => {
        runs.push(Date.now());
      },
      timer: timer.api,
    });
    scheduler.start();

    expect(runs).toHaveLength(0);
    expect(timer.pendingCount()).toBe(1);
    const ms = await timer.fire();
    expect(ms).toBe(AUTO_REFRESH_INTERVAL_MS);
    expect(runs).toHaveLength(1);
    scheduler.stop();
  });

  it("上一轮未结算时不排定下一轮（不会并发轮询）", async () => {
    const timer = fakeTimer();
    const round = deferredRound();
    const scheduler = createRefreshScheduler({ run: round.promise, timer: timer.api });
    scheduler.start();

    void timer.fire();
    await Promise.resolve();
    expect(scheduler.isRefreshing()).toBe(true);
    // 在途期间既没有新定时器，也不会因为 refreshNow 再发起一轮
    expect(timer.pendingCount()).toBe(0);
    scheduler.refreshNow();
    expect(round.started()).toBe(1);

    round.release();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(scheduler.isRefreshing()).toBe(false);
    expect(timer.pendingCount()).toBe(1);
    expect(scheduler.completedRounds()).toBe(1);
    scheduler.stop();
  });

  it("手动刷新与自动刷新并发时不产生第二轮请求", async () => {
    const timer = fakeTimer();
    const round = deferredRound();
    const scheduler = createRefreshScheduler({ run: round.promise, timer: timer.api });
    scheduler.start();

    scheduler.refreshNow();
    expect(round.started()).toBe(1);
    void timer.fire();
    scheduler.refreshNow();
    expect(round.started()).toBe(1);

    round.release();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    scheduler.stop();
  });

  it("手动刷新结算后自动刷新节奏被接回，不会悄悄停止", async () => {
    const timer = fakeTimer();
    const round = deferredRound();
    const scheduler = createRefreshScheduler({ run: round.promise, timer: timer.api });
    scheduler.start();

    // 手动刷新：清掉待执行定时器并发起一轮
    scheduler.cancelScheduled();
    scheduler.refreshNow();
    expect(round.started()).toBe(1);
    round.release();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // 结算后仍有一轮排定，说明自动刷新没有因为手动刷新而停掉
    expect(timer.pendingCount()).toBe(1);
    expect(timer.msList()).toEqual([AUTO_REFRESH_INTERVAL_MS]);
    scheduler.stop();
  });

  it("单轮失败既不加快也不放慢节奏", async () => {
    const timer = fakeTimer();
    let calls = 0;
    const scheduler = createRefreshScheduler({
      run: async () => {
        calls += 1;
        throw new Error("刷新失败");
      },
      timer: timer.api,
    });
    scheduler.start();
    await timer.fire();
    await Promise.resolve();
    expect(calls).toBe(1);
    expect(timer.msList()).toEqual([AUTO_REFRESH_INTERVAL_MS]);
    scheduler.stop();
  });

  it("页面隐藏时暂停排定，恢复可见时立即刷新一次", async () => {
    const timer = fakeTimer();
    let visible = true;
    const round = deferredRound();
    const scheduler = createRefreshScheduler({
      isVisible: () => visible,
      run: round.promise,
      timer: timer.api,
    });
    scheduler.start();
    expect(timer.pendingCount()).toBe(1);

    visible = false;
    scheduler.setVisible(false);
    expect(timer.pendingCount()).toBe(0);

    visible = true;
    scheduler.setVisible(true);
    // 恢复前台立即刷新一次，不需要等待完整周期
    expect(round.started()).toBe(1);
    expect(timer.pendingCount()).toBe(0);

    round.release();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(timer.pendingCount()).toBe(1);
    scheduler.stop();
  });

  it("策略为 off 时清空待执行定时器且不再排定", async () => {
    const timer = fakeTimer();
    const round = deferredRound();
    const scheduler = createRefreshScheduler({ run: round.promise, timer: timer.api });
    scheduler.start();
    expect(timer.pendingCount()).toBe(1);

    scheduler.setPolicy({ mode: "off", reason: "workflow_terminal" });
    expect(timer.pendingCount()).toBe(0);
    expect(scheduler.isOff()).toBe(true);
    await timer.fire();
    expect(round.started()).toBe(0);
    scheduler.stop();
  });

  it("策略从 off 回到 continuous 时恢复排定（例如用户手动恢复工作流）", async () => {
    const timer = fakeTimer();
    let calls = 0;
    const scheduler = createRefreshScheduler({
      run: async () => {
        calls += 1;
      },
      timer: timer.api,
    });
    scheduler.setPolicy({ mode: "off", reason: "awaiting_manual_action" });
    scheduler.start();
    expect(timer.pendingCount()).toBe(0);

    scheduler.setPolicy({ mode: "continuous" });
    expect(timer.pendingCount()).toBe(1);
    await timer.fire();
    expect(calls).toBe(1);
    scheduler.stop();
  });

  it("启动窗口用尽后停止刷新并回调提示", async () => {
    const timer = fakeTimer();
    const exhausted = vi.fn();
    const scheduler = createRefreshScheduler({
      onExhausted: exhausted,
      policy: { mode: "bounded", maxRounds: 2 },
      run: async () => undefined,
      timer: timer.api,
    });
    scheduler.start();

    await timer.fire();
    await Promise.resolve();
    expect(timer.pendingCount()).toBe(1);
    await timer.fire();
    await Promise.resolve();
    expect(exhausted).toHaveBeenCalledTimes(1);
    expect(timer.pendingCount()).toBe(0);
    expect(scheduler.completedRounds()).toBe(2);
    scheduler.stop();
  });

  it("stop 清理定时器并中止在途一轮", async () => {
    const timer = fakeTimer();
    const round = deferredRound();
    const scheduler = createRefreshScheduler({ run: round.promise, timer: timer.api });
    scheduler.start();
    void timer.fire();
    await Promise.resolve();

    scheduler.stop();
    expect(timer.pendingCount()).toBe(0);
    expect(scheduler.hasStopped()).toBe(true);

    round.release();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(timer.pendingCount()).toBe(0);
    expect(scheduler.completedRounds()).toBe(0);
  });

  it("未开始时不排定任何定时器", () => {
    const timer = fakeTimer();
    const scheduler = createRefreshScheduler({ run: async () => undefined, timer: timer.api });
    scheduler.refreshNow();
    scheduler.setVisible(true);
    expect(timer.pendingCount()).toBe(0);
  });
});

describe("请求票据与迟到响应", () => {
  it("只有最后一次发起且仍属于当前任务的响应会被采用", () => {
    const gate = createRequestGate();
    const first = beginRequest(gate, "task-1");
    const second = beginRequest(gate, "task-1");

    expect(acceptResponse(gate, first, "task-1", false)).toBe(false);
    expect(acceptResponse(gate, second, "task-1", false)).toBe(true);
  });

  it("切换任务后旧任务的响应被丢弃", () => {
    const gate = createRequestGate();
    const ticket = beginRequest(gate, "task-1");

    expect(acceptResponse(gate, ticket, "task-2", false)).toBe(false);
    expect(acceptResponse(gate, ticket, undefined, false)).toBe(false);
  });

  it("被取消的请求即使是最新一次也不采用", () => {
    const gate = createRequestGate();
    const ticket = beginRequest(gate, "task-1");

    expect(acceptResponse(gate, ticket, "task-1", true)).toBe(false);
  });

  it("刷新失败提示不把失败表达成任务失败或工作流终止", () => {
    expect(describeRefreshFailures([])).toBeUndefined();
    const text = describeRefreshFailures(["任务详情（服务内部错误）"]);
    expect(text).toContain("刷新失败");
    expect(text).toContain("服务内部错误");
    expect(text).toContain("上一次成功获取");
    expect(text).not.toContain("任务已失败");
    expect(text).not.toContain("工作流已终止");
  });
});

describe("一轮快照的变换", () => {
  it("成功读取活动态：进入持续刷新，不再给启动窗口", () => {
    const applied = applySnapshotRound({
      previous: { loading: false },
      target: "task-1",
      task: task(),
      workflow: workflow(),
    });

    expect(applied.failures).toEqual([]);
    expect(applied.policy).toEqual({ mode: "continuous" });
    expect(applied.snapshot.task?.id).toBe("task-1");
    expect(applied.snapshot.refreshError).toBeUndefined();
  });

  it("刷新失败保留上一次成功快照且不改变刷新策略", () => {
    const previous = {
      loading: false,
      task: task(),
      taskID: "task-1",
      workflow: workflow(),
    };
    const applied = applySnapshotRound({
      previous,
      target: "task-1",
      task: previous.task,
      taskError: "服务内部错误",
    });

    expect(applied.snapshot.task).toBe(previous.task);
    expect(applied.snapshot.workflow).toBe(previous.workflow);
    expect(applied.snapshot.refreshError).toContain("服务内部错误");
    // 策略未重新判定：刷新失败不会被当作工作流终止，也不会改变节奏
    expect(applied.policy).toBeUndefined();
    expect(applied.dirty).toEqual({});
  });

  it("首次加载失败时不伪造任务对象", () => {
    const applied = applySnapshotRound({
      previous: { loading: true },
      target: "task-1",
      taskError: "任务不存在: task-1",
    });

    expect(applied.snapshot.task).toBeUndefined();
    expect(applied.snapshot.error).toBe("任务不存在: task-1");
    expect(applied.policy).toBeUndefined();
    expect(applied.failures).toEqual(["任务详情（任务不存在: task-1）"]);
  });

  it("工作流进度查询失败不清空任务详情", () => {
    const previous = { loading: false, task: task(), taskID: "task-1" };
    const applied = applySnapshotRound({
      previous,
      target: "task-1",
      task: previous.task,
      workflowError: "无法连接 AgentFlow 服务",
    });

    expect(applied.snapshot.task).toBe(previous.task);
    expect(applied.snapshot.workflow).toBeUndefined();
    expect(applied.snapshot.refreshError).toContain("工作流进度");
    expect(applied.policy).toBeUndefined();
  });

  it("只在步骤相关字段变化时要求重新加载步骤", () => {
    const previous = {
      loading: false,
      task: task(),
      taskID: "task-1",
      workflow: workflow(),
    };
    // 完全相同的一份数据：不需要重新请求子面板
    expect(
      applySnapshotRound({ previous, target: "task-1", task: task(), workflow: workflow() }).dirty,
    ).toEqual({});

    // 当前步骤推进：需要重新加载步骤与执行记录
    expect(
      applySnapshotRound({
        previous,
        target: "task-1",
        task: task(),
        workflow: workflow({ run: run({ current_step_id: "step-2", revision: 2 }) }),
      }).dirty.steps,
    ).toBe(true);
  });

  it("进入代码保存阶段时才要求重新加载代码保存记录", () => {
    const previous = {
      loading: false,
      task: task(),
      taskID: "task-1",
      workflow: workflow(),
    };
    const saving = applySnapshotRound({
      previous,
      target: "task-1",
      task: task({ status: "saving" }),
      workflow: workflow({ run: run({ stage: "code_save", status: "running" }) }),
    });
    expect(saving.dirty.codeSaves).toBe(true);

    // 同一阶段内的普通轮询不会重复请求代码保存记录
    const sameStage = applySnapshotRound({
      previous: {
        ...previous,
        task: task({ status: "saving" }),
        workflow: workflow({ run: run({ stage: "code_save", revision: 2 }) }),
      },
      target: "task-1",
      task: task({ status: "saving" }),
      workflow: workflow({ run: run({ stage: "code_save", revision: 2 }) }),
    });
    expect(sameStage.dirty.codeSaves).toBeFalsy();
  });

  it("获得停止刷新的结论时才刷新一次续跑资格", () => {
    const previous = {
      loading: false,
      task: task(),
      taskID: "task-1",
      workflow: workflow(),
    };
    const blocked = applySnapshotRound({
      previous,
      target: "task-1",
      task: task(),
      workflow: workflow({ run: run({ status: "blocked", status_active: false }) }),
    });
    expect(blocked.policy).toEqual({ mode: "off", reason: "awaiting_manual_action" });
    expect(blocked.dirty.resume).toBe(true);

    const running = applySnapshotRound({
      previous,
      target: "task-1",
      task: task(),
      workflow: workflow({ run: run({ revision: 2 }) }),
    });
    expect(running.dirty.resume).toBeFalsy();
  });
});

describe("策略与提示的对应关系", () => {
  it("只有停止刷新的策略才有提示文案", () => {
    const continuous: AutoRefreshPolicy = { mode: "continuous" };
    const bounded: AutoRefreshPolicy = { mode: "bounded", maxRounds: 3 };
    expect(refreshHintFor(continuous)).toBeUndefined();
    expect(refreshHintFor(bounded)).toBeUndefined();
    expect(refreshHintFor({ mode: "off", reason: "workflow_terminal" })).toContain("手动刷新");
  });
});
