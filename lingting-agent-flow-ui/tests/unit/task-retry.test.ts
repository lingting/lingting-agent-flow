import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  fetchTaskRetryEligibility,
  retryTask,
  TASKS_PATH,
  taskRetryEligibilityPath,
  taskRetryPath,
  type RetryEligibilityResponse,
  type RetryTaskResponse,
} from "../../src/lib/api";
import {
  acceptRetryResponse,
  beginRetryRequest,
  canSubmitRetry,
  createRetryGate,
  describeRetryResult,
  isRealTime,
  isUncertainRetryError,
  RETRY_BLOCKING_META,
  RETRY_BLOCKED_WITHOUT_ELIGIBILITY,
  RETRY_SUMMARY_MISSING,
  RETRY_UNKNOWN_OUTCOME_HINT,
  retryBlockingList,
  retryBlockingMeta,
  retrySummary,
  retryTaskStatusLabel,
  retryVerdict,
} from "../../src/pages/tasks/retry";
import { TASK_STATUS_LABEL } from "../../src/pages/tasks/status";
import { describeApiError } from "../../src/pages/tasks/useExecutionRecords";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/**
 * 一份重试资格响应样本。
 *
 * 字段与 Go 端 `httpapi.RetryEligibilityResponse` 一致；`blocking` 在服务端没有任何阻塞项时
 * 可能序列化为 `null`（Go 端 `append` 到 nil 切片），因此默认值取 `null`。
 */
function eligibility(overrides: Partial<RetryEligibilityResponse> = {}): RetryEligibilityResponse {
  return {
    blocking: null,
    can_retry: true,
    summary: "允许开启一次新的执行周期",
    task_id: "task-1",
    task_status: "failed",
    ...overrides,
  };
}

/** 一份重试响应样本，字段与 Go 端 `httpapi.RetryTaskResponse` 一致 */
function retryResponse(overrides: Partial<RetryTaskResponse> = {}): RetryTaskResponse {
  return {
    lease_epoch: 3,
    lease_owner: "retry-1",
    previous_status: "failed",
    result: undefined,
    task_id: "task-1",
    task_status: "developing",
    workflow_started: true,
    ...overrides,
  } as RetryTaskResponse;
}

/** 一份工作流执行记录样本 */
function sampleRun(overrides: Record<string, unknown> = {}) {
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
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/** 结果展示需要的两个外部字典 */
const DESCRIBE_OPTIONS = {
  stageLabel: (stage: string) => `阶段:${stage}`,
  statusLabels: TASK_STATUS_LABEL as unknown as Record<string, string>,
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("重试路径构造", () => {
  it("由任务路径派生重试与资格查询两条不同路径", () => {
    expect(taskRetryPath("task-1")).toBe(`${TASKS_PATH}/task-1/retry`);
    expect(taskRetryEligibilityPath("task-1")).toBe(`${TASKS_PATH}/task-1/retry-eligibility`);
    expect(taskRetryPath("task 1")).toBe(`${TASKS_PATH}/task%201/retry`);
  });

  it("空任务标识直接拒绝，不拼出无效路径", () => {
    expect(() => taskRetryPath("")).toThrow();
    expect(() => taskRetryEligibilityPath("   ")).toThrow();
  });
});

describe("重试资格请求", () => {
  it("使用 GET 且只发出一次请求", async () => {
    const spy = vi.fn(async () => jsonResponse(eligibility()));
    vi.stubGlobal("fetch", spy);

    const { data, request } = await fetchTaskRetryEligibility("task-1");

    expect(request).toEqual({ method: "GET", path: `${TASKS_PATH}/task-1/retry-eligibility` });
    expect(spy).toHaveBeenCalledTimes(1);
    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(`${TASKS_PATH}/task-1/retry-eligibility`);
    expect(init.method).toBe("GET");
    expect(data.can_retry).toBe(true);
  });

  it("404 表示任务不存在，不能当作可以重试", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "任务不存在: task-1" } },
          { status: 404 },
        ),
      ),
    );

    await fetchTaskRetryEligibility("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(404);
      expect(describeApiError(cause)).toBe("任务不存在: task-1");
    });
  });

  it("503 原样展示服务端说明，不伪造资格结论", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "internal_error", message: "任务重试服务未初始化" } },
          { status: 503 },
        ),
      ),
    );

    await fetchTaskRetryEligibility("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(503);
      expect(describeApiError(cause)).toBe("任务重试服务未初始化");
    });
  });
});

describe("显式重试请求", () => {
  it("使用 POST 且只发送真实接口支持的执行参数", async () => {
    const spy = vi.fn(async () => jsonResponse(retryResponse(), { status: 202 }));
    vi.stubGlobal("fetch", spy);

    const { data, request } = await retryTask("task-1", {
      prompt: "补充说明",
      timeout_seconds: 600,
    });

    expect(request).toEqual({ method: "POST", path: `${TASKS_PATH}/task-1/retry` });
    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(`${TASKS_PATH}/task-1/retry`);
    expect(init.method).toBe("POST");
    const sent = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(sent).toEqual({ prompt: "补充说明", timeout_seconds: 600 });
    // 真实接口没有的字段不会被偷偷加上
    expect(Object.keys(sent)).toEqual(["prompt", "timeout_seconds"]);
    expect(data.workflow_started).toBe(true);
  });

  it("不设置会让同步操作提前失联的客户端超时", async () => {
    // 用假时钟证明：重试请求即使超过 apiRequest 的 10 秒默认超时也不会被中断。
    vi.useFakeTimers();
    try {
      const spy = vi.fn(
        (_target: string, _init: RequestInit) => new Promise<Response>(() => undefined),
      );
      vi.stubGlobal("fetch", spy);

      const pending = retryTask("task-1");
      let settled = false;
      void pending.then(
        () => {
          settled = true;
        },
        () => {
          settled = true;
        },
      );

      await vi.advanceTimersByTimeAsync(60_000);
      expect(settled).toBe(false);
      const [, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
      expect((init.signal as AbortSignal).aborted).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("服务端拒绝时保留真实业务错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "state_conflict", message: "任务不能重试：status_not_retryable" } },
          { status: 409 },
        ),
      ),
    );

    await retryTask("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(409);
      expect(describeApiError(cause)).toBe("任务不能重试：status_not_retryable");
    });
  });

  it("网络中断被识别为结果不确定，不自动再次提交", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        calls += 1;
        throw new TypeError("failed to fetch");
      }),
    );

    await retryTask("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).kind).toBe("network");
      expect(isUncertainRetryError((cause as ApiError).kind)).toBe(true);
      expect(describeApiError(cause)).toBe("无法连接 AgentFlow 服务，请确认服务已启动后重试。");
    });
    // 客户端只发了一次：没有任何自动重试
    expect(calls).toBe(1);
  });

  it("超时被识别为结果不确定", () => {
    expect(isUncertainRetryError("timeout")).toBe(true);
    expect(isUncertainRetryError("aborted")).toBe(true);
    expect(isUncertainRetryError("http")).toBe(false);
    expect(RETRY_UNKNOWN_OUTCOME_HINT).toContain("无法确认");
    expect(RETRY_UNKNOWN_OUTCOME_HINT).toContain("不会自动重复提交");
  });
});

describe("重试资格展示", () => {
  it("can_retry=true 时表述为查询时刻允许重试，并说明重试时仍会重新判定", () => {
    const verdict = retryVerdict(true);

    expect(verdict.title).toBe("当前查询结果允许重试");
    expect(verdict.color).toBe("success");
    expect(verdict.attention).toBe(false);
    expect(verdict.description).toContain("重新判定");
    expect(verdict.description).toContain("不代表任务会成功");
  });

  it("can_retry=false 时不显示为允许重试", () => {
    const verdict = retryVerdict(false);

    expect(verdict.title).toBe("当前存在阻塞，不能重试");
    expect(verdict.attention).toBe(true);
    expect(verdict.title).not.toContain("允许重试");
  });

  it("摘要缺省时使用中性提示，不编造结论", () => {
    expect(retrySummary(eligibility({ summary: "" }))).toBe(RETRY_SUMMARY_MISSING);
    expect(retrySummary(eligibility({ summary: "   " }))).toBe(RETRY_SUMMARY_MISSING);
    expect(retrySummary(eligibility({ summary: "允许重试" }))).toBe("允许重试");
  });

  it("blocking 为 null 时按空列表处理，不当作成功", () => {
    expect(retryBlockingList(eligibility({ blocking: null }))).toEqual([]);
    expect(retryBlockingList(eligibility({ blocking: ["lease_held"] }))).toEqual(["lease_held"]);
  });

  it("覆盖服务端给出的全部阻塞分类", () => {
    expect(Object.keys(RETRY_BLOCKING_META).sort()).toEqual([
      "active_workflow_run",
      "cancel_requested",
      "external_side_effect_pending",
      "lease_held",
      "nothing_to_retry",
      "prerequisite_missing",
      "recovery_residue",
      "status_not_retryable",
      "step_occupied",
      "task_deleted",
    ]);
    for (const code of Object.keys(RETRY_BLOCKING_META)) {
      const meta = retryBlockingMeta(code);
      expect(meta.known).toBe(true);
      expect(meta.description.length).toBeGreaterThan(0);
      expect(meta.label).not.toMatch(/^[a-z_]+$/);
    }
  });

  it("未知阻塞分类原样保留，不省略也不伪装成无阻塞", () => {
    const meta = retryBlockingMeta("future_retry_blocker");

    expect(meta.label).toBe("future_retry_blocker");
    expect(meta.known).toBe(false);
    expect(meta.description).toContain("未定义");
  });

  it("多个阻塞项逐条呈现，不被压缩或丢失", () => {
    const labels = retryBlockingList(
      eligibility({ blocking: ["lease_held", "step_occupied", "future_retry_blocker"] }),
    ).map((code) => retryBlockingMeta(code).label);

    expect(labels).toEqual(["任务执行权未释放", "仍有步骤在执行", "future_retry_blocker"]);
  });

  it("未知任务状态原样保留，不被翻译成已知状态", () => {
    const labels = TASK_STATUS_LABEL as unknown as Record<string, string>;
    expect(retryTaskStatusLabel("failed", labels)).toBe("失败");
    expect(retryTaskStatusLabel("future_state", labels)).toBe("future_state");
  });
});

describe("重试操作可用性", () => {
  it("只有服务端 can_retry=true 才允许提交", () => {
    expect(
      canSubmitRetry({
        eligibility: eligibility({ can_retry: true }),
        eligibilityTaskID: "task-1",
        running: false,
        taskID: "task-1",
      }),
    ).toBe(true);
  });

  it("can_retry=false 时不可提交", () => {
    expect(
      canSubmitRetry({
        eligibility: eligibility({ blocking: ["lease_held"], can_retry: false }),
        eligibilityTaskID: "task-1",
        running: false,
        taskID: "task-1",
      }),
    ).toBe(false);
  });

  it("资格查询失败（没有快照）时不可提交", () => {
    expect(canSubmitRetry({ running: false, taskID: "task-1" })).toBe(false);
    expect(RETRY_BLOCKED_WITHOUT_ELIGIBILITY).toContain("不能发起重试");
  });

  it("资格快照不属于当前任务时不可提交", () => {
    expect(
      canSubmitRetry({
        eligibility: eligibility({ can_retry: true }),
        eligibilityTaskID: "task-1",
        running: false,
        taskID: "task-2",
      }),
    ).toBe(false);
  });

  it("请求在途时不可重复提交", () => {
    expect(
      canSubmitRetry({
        eligibility: eligibility({ can_retry: true }),
        eligibilityTaskID: "task-1",
        running: true,
        taskID: "task-1",
      }),
    ).toBe(false);
  });

  it("不依据任务状态推断资格：failed 但没有服务端结论时仍不可提交", () => {
    // 这里刻意不传任何任务对象：可提交性只由 can_retry 决定，与 status 无关。
    expect(canSubmitRetry({ eligibility: undefined, running: false, taskID: "task-1" })).toBe(
      false,
    );
  });
});

describe("重试结果展示", () => {
  it("workflow_started=true 时展示真实任务状态、执行记录与执行代次", () => {
    const outcome = describeRetryResult(
      retryResponse({
        run: sampleRun() as unknown as RetryTaskResponse["run"],
        workflow_started: true,
      }),
      DESCRIBE_OPTIONS,
    );

    expect(outcome.kind).toBe("started");
    expect(outcome.title).toContain("启动工作流");
    expect(outcome.details.join("｜")).toContain("失败 → 当前 开发中");
    expect(outcome.details.join("｜")).toContain("run-1");
    expect(outcome.details.join("｜")).toContain("执行代次：3");
  });

  it("workflow_started=false 时明确说明工作流没有启动，并给出真实阶段与原因", () => {
    const outcome = describeRetryResult(
      retryResponse({
        failure: "容器准备失败",
        failure_stage: "container",
        task_status: "preparing",
        workflow_started: false,
      }),
      DESCRIBE_OPTIONS,
    );

    expect(outcome.kind).toBe("not_started");
    expect(outcome.title).toBe("任务已重开，但工作流没有启动");
    expect(outcome.title).not.toContain("已启动工作流");
    expect(outcome.details.join("｜")).toContain("阶段:container");
    expect(outcome.details.join("｜")).toContain("容器准备失败");
    expect(outcome.details.join("｜")).toContain("当前 准备环境");
  });

  it("workflow_started=false 且缺少可选字段时使用中性说明，不推断成功", () => {
    const outcome = describeRetryResult(
      retryResponse({ run: undefined, workflow_started: false }),
      DESCRIBE_OPTIONS,
    );

    expect(outcome.kind).toBe("not_started");
    expect(outcome.details.join("｜")).toContain("服务端未提供失败阶段");
    expect(outcome.details.join("｜")).toContain("服务端未提供失败原因");
  });

  it("workflow_started=true 但缺少执行记录时不补造执行记录", () => {
    const outcome = describeRetryResult(
      retryResponse({ run: undefined, workflow_started: true }),
      DESCRIBE_OPTIONS,
    );

    expect(outcome.kind).toBe("started");
    expect(outcome.details.join("｜")).toContain("服务端未返回本次启动的执行记录");
    expect(outcome.details.join("｜")).not.toContain("run-1");
  });

  it("未知任务状态与未知失败阶段原样保留", () => {
    const outcome = describeRetryResult(
      retryResponse({
        failure_stage: "future_stage",
        task_status: "future_status",
        workflow_started: false,
      }),
      DESCRIBE_OPTIONS,
    );

    expect(outcome.details.join("｜")).toContain("future_status");
    expect(outcome.details.join("｜")).toContain("阶段:future_stage");
  });

  it("零值时间不被展示成真实时刻", () => {
    expect(isRealTime(undefined)).toBe(false);
    expect(isRealTime("0001-01-01T00:00:00Z")).toBe(false);
    expect(isRealTime("not-a-time")).toBe(false);
    expect(isRealTime("2026-10-27T08:00:00Z")).toBe(true);

    const outcome = describeRetryResult(
      retryResponse({ started_at: "0001-01-01T00:00:00Z", workflow_started: true }),
      DESCRIBE_OPTIONS,
    );
    expect(outcome.details.join("｜")).not.toContain("开始时间");
  });

  it("请求成功不表述为任务成功", () => {
    const outcome = describeRetryResult(retryResponse(), DESCRIBE_OPTIONS);
    const text = [outcome.title, ...outcome.details].join("｜");

    expect(text).not.toContain("任务成功");
    expect(text).not.toContain("已通过验收");
  });
});

describe("资格请求的竞态判定", () => {
  it("同一任务只有最后一次发起的请求结果会被采用", () => {
    const gate = createRetryGate();
    const first = beginRetryRequest(gate, "task-1");
    const second = beginRetryRequest(gate, "task-1");

    expect(acceptRetryResponse(gate, second, "task-1", false)).toBe(true);
    expect(acceptRetryResponse(gate, first, "task-1", false)).toBe(false);
  });

  it("切换任务后旧任务的迟到响应被丢弃", () => {
    const gate = createRetryGate();
    const ticket = beginRetryRequest(gate, "task-1");

    expect(acceptRetryResponse(gate, ticket, "task-2", false)).toBe(false);
    expect(acceptRetryResponse(gate, ticket, "task-1", false)).toBe(true);
  });

  it("已取消或被卸载的响应不被采用", () => {
    const gate = createRetryGate();
    const ticket = beginRetryRequest(gate, "task-1");

    expect(acceptRetryResponse(gate, ticket, "task-1", true)).toBe(false);
    expect(acceptRetryResponse(gate, ticket, undefined, false)).toBe(false);
  });
});
