import { afterEach, describe, expect, it, vi } from "vitest";

import {
  resumeTaskWorkflow,
  TASKS_PATH,
  taskResumeWorkflowPath,
  type ResumeEligibilityResponse,
  type WorkflowRun,
} from "../../src/lib/api";
import {
  acceptResumeResponse,
  beginResumeRequest,
  canSubmitResume,
  createResumeGate,
  describeResumeResult,
  formatResumeTime,
  isRealTime,
  isUncertainResumeError,
  RESUME_BLOCKED_WITHOUT_ELIGIBILITY,
  RESUME_FAILED_TITLE,
  RESUME_SUBMIT_BLOCK_TEXT,
  RESUME_UNCERTAIN_TITLE,
  RESUME_UNKNOWN_OUTCOME_HINT,
  resumeRunStatusLabel,
  resumeSubmitBlockReason,
  resumeSubmitBlockText,
  type ResumeSubmitInput,
} from "../../src/pages/tasks/resumeAction";
import { WORKFLOW_RUN_STATUS_META } from "../../src/pages/tasks/status";
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
 * 一份续跑资格响应样本，字段与 Go 端 `httpapi.ResumeEligibilityResponse` 一致。
 *
 * `blocking` 与 `next_actions` 在服务端为空时序列化为 `null`，因此默认值取 `null`。
 */
function eligibility(
  overrides: Partial<ResumeEligibilityResponse> = {},
): ResumeEligibilityResponse {
  return {
    blocking: null,
    can_resume: true,
    code_save_action: "proceed",
    code_save_records: [],
    code_save_summary: "代码保存记录中没有结果不明的外部副作用",
    next_actions: ["agentflow task resume task-1"],
    summary: "没有阻塞项，可以再次请求恢复推进",
    task_id: "task-1",
    task_status: "recovery_required",
    ...overrides,
  };
}

/** 一份工作流执行记录样本，字段与 Go 端 `httpapi.WorkflowRunResponse` 一致 */
function sampleRun(overrides: Partial<WorkflowRun> = {}): WorkflowRun {
  return {
    cancel_requested: false,
    code_saver_available: true,
    completed_steps: 1,
    last_completed_sequence: 1,
    plan_digest: "digest",
    revision: 4,
    run_id: "run-1",
    stage: "step_execution",
    status: "running",
    status_active: true,
    status_terminal: false,
    task_id: "task-1",
    total_steps: 3,
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/** 由状态字典派生的标签映射，与面板使用同一份来源 */
const RUN_STATUS_LABELS: Record<string, string> = Object.fromEntries(
  Object.entries(WORKFLOW_RUN_STATUS_META).map(([value, meta]) => [value, meta.label]),
);

/** 一份"资格已就绪且允许恢复"的提交输入 */
function submitInput(overrides: Partial<ResumeSubmitInput> = {}): ResumeSubmitInput {
  const snapshot = eligibility();
  return {
    eligibility: snapshot,
    eligibilityRefreshError: undefined,
    eligibilityStatus: "ready",
    eligibilityTaskID: snapshot.task_id,
    running: false,
    taskID: "task-1",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("恢复路径构造", () => {
  it("由任务路径派生恢复工作流路径", () => {
    expect(taskResumeWorkflowPath("task-1")).toBe(`${TASKS_PATH}/task-1/resume-workflow`);
  });

  it("空任务标识直接拒绝，不拼出无效路径", () => {
    expect(() => taskResumeWorkflowPath("")).toThrow();
    expect(() => taskResumeWorkflowPath("   ")).toThrow();
  });
});

describe("恢复请求", () => {
  it("使用 POST 且只发送服务端支持的参数", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleRun()));
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await resumeTaskWorkflow("task-1", { run_id: "run-1" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`${TASKS_PATH}/task-1/resume-workflow`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ run_id: "run-1" });
    expect(data.run_id).toBe("run-1");
  });

  it("服务端拒绝时保留真实业务错误，不伪装成已受理", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "state_conflict", message: "工作流执行当前状态 running 不允许恢复" } },
          { status: 409 },
        ),
      ),
    );

    await expect(resumeTaskWorkflow("task-1", { run_id: "run-1" })).rejects.toThrow(
      "工作流执行当前状态 running 不允许恢复",
    );
  });

  it("结果不确定只由超时、网络中断与取消触发", () => {
    expect(isUncertainResumeError("timeout")).toBe(true);
    expect(isUncertainResumeError("network")).toBe(true);
    expect(isUncertainResumeError("aborted")).toBe(true);
    expect(isUncertainResumeError("http")).toBe(false);
    expect(isUncertainResumeError("invalid_response")).toBe(false);
    expect(RESUME_UNKNOWN_OUTCOME_HINT).toContain("不会自动重复提交");
  });
});

describe("恢复入口可用性", () => {
  it("can_resume=true 且查询成功时允许提交", () => {
    expect(canSubmitResume(submitInput())).toBe(true);
    expect(resumeSubmitBlockReason(submitInput())).toBe("");
  });

  it("can_resume=false 时不可提交，并给出阻塞说明", () => {
    const input = submitInput({
      eligibility: eligibility({ blocking: ["container_missing"], can_resume: false }),
    });

    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("blocked");
    expect(resumeSubmitBlockText(input)).toBe(RESUME_SUBMIT_BLOCK_TEXT.blocked);
  });

  it("资格查询尚未完成时不可提交", () => {
    const input = submitInput({ eligibility: undefined, eligibilityStatus: "loading" });

    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("eligibility_loading");
  });

  it("资格查询失败时不可提交", () => {
    const input = submitInput({ eligibility: undefined, eligibilityStatus: "error" });

    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("eligibility_error");
  });

  it("从未取得过资格快照时不可提交，并使用固定说明", () => {
    const input = submitInput({ eligibility: undefined, eligibilityStatus: "ready" });

    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("no_eligibility");
    expect(resumeSubmitBlockText(input)).toBe(RESUME_BLOCKED_WITHOUT_ELIGIBILITY);
  });

  it("刷新失败保留旧快照但不把它当作当前授权", () => {
    const input = submitInput({ eligibilityRefreshError: "服务内部错误" });

    // 快照本身仍写着允许恢复，但刷新失败后它已过期。
    expect(input.eligibility?.can_resume).toBe(true);
    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("stale_snapshot");
  });

  it("资格快照不属于当前任务时不可提交", () => {
    const input = submitInput({ taskID: "task-2" });

    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("stale_snapshot");
  });

  it("恢复请求在途时不可重复提交", () => {
    const input = submitInput({ running: true });

    expect(canSubmitResume(input)).toBe(false);
    expect(resumeSubmitBlockReason(input)).toBe("running");
  });

  it("不依据任务或工作流状态推断资格：没有服务端结论时一律不可提交", () => {
    // 这里刻意不传任务对象与执行记录：可提交性只由 can_resume 与资格可用性决定。
    expect(canSubmitResume({ eligibilityStatus: "idle", running: false })).toBe(false);
    expect(
      canSubmitResume({
        eligibility: eligibility({ can_resume: false }),
        eligibilityStatus: "ready",
        eligibilityTaskID: "task-1",
        running: false,
        taskID: "task-1",
      }),
    ).toBe(false);
  });
});

describe("恢复请求的竞态判定", () => {
  it("同一任务只有最后一次发起的请求结果会被采用", () => {
    const gate = createResumeGate();
    const first = beginResumeRequest(gate, "task-1");
    const second = beginResumeRequest(gate, "task-1");

    expect(acceptResumeResponse(gate, second, "task-1", false)).toBe(true);
    expect(acceptResumeResponse(gate, first, "task-1", false)).toBe(false);
  });

  it("切换任务后旧任务的迟到响应被丢弃", () => {
    const gate = createResumeGate();
    const ticket = beginResumeRequest(gate, "task-1");

    expect(acceptResumeResponse(gate, ticket, "task-2", false)).toBe(false);
    expect(acceptResumeResponse(gate, ticket, "task-1", false)).toBe(true);
  });

  it("被取消或已卸载的响应不被采用", () => {
    const gate = createResumeGate();
    const ticket = beginResumeRequest(gate, "task-1");

    expect(acceptResumeResponse(gate, ticket, "task-1", true)).toBe(false);
    expect(acceptResumeResponse(gate, ticket, undefined, false)).toBe(false);
  });
});

describe("恢复结果展示", () => {
  it("running 时说明请求已受理且不代表任务已完成", () => {
    const outcome = describeResumeResult(sampleRun({ status: "running" }), {
      statusLabels: RUN_STATUS_LABELS,
    });

    expect(outcome.title).toContain("已受理");
    expect(outcome.attention).toBe(false);
    expect(outcome.details.join("｜")).toContain("执行记录：run-1");
    expect(outcome.details.join("｜")).toContain("执行状态：执行中");
    expect(outcome.details.join("｜")).toContain("已完成 1 / 3 步");
    expect(outcome.description).toContain("不代表工作流会成功");
    expect(outcome.description).toContain("不代表任务已完成");
  });

  it("pending 时不把受理读成已推进", () => {
    const outcome = describeResumeResult(sampleRun({ status: "pending" }), {
      statusLabels: RUN_STATUS_LABELS,
    });

    expect(outcome.attention).toBe(true);
    expect(outcome.title).toContain("尚未开始推进");
    expect(outcome.title).not.toContain("已开始推进，");
  });

  it("其他状态如实说明执行记录没有回到执行中", () => {
    const outcome = describeResumeResult(sampleRun({ status: "blocked" }), {
      statusLabels: RUN_STATUS_LABELS,
    });

    expect(outcome.attention).toBe(true);
    expect(outcome.title).toContain("未进入执行中");
    expect(outcome.details.join("｜")).toContain("执行状态：已停止推进");
  });

  it("未知执行状态原样保留，不被翻译成已知状态", () => {
    const outcome = describeResumeResult(sampleRun({ status: "future_status" as never }), {
      statusLabels: RUN_STATUS_LABELS,
    });

    expect(outcome.details.join("｜")).toContain("执行状态：future_status");
    expect(outcome.title).toContain("未进入执行中");
  });

  it("请求已受理不表述为任务成功", () => {
    const outcome = describeResumeResult(sampleRun(), { statusLabels: RUN_STATUS_LABELS });
    const text = [outcome.title, outcome.description, ...outcome.details].join("｜");

    expect(text).not.toContain("任务成功");
    expect(text).not.toContain("已通过验收");
    expect(text).not.toContain("已完成任务");
  });

  it("零值时间不被展示成真实时刻", () => {
    expect(isRealTime(undefined)).toBe(false);
    expect(isRealTime("0001-01-01T00:00:00Z")).toBe(false);
    expect(isRealTime("not-a-time")).toBe(false);
    expect(isRealTime("2026-10-27T08:00:00Z")).toBe(true);
    expect(formatResumeTime(undefined)).toBe("服务端未记录");

    const outcome = describeResumeResult(sampleRun({ started_at: "0001-01-01T00:00:00Z" }), {
      statusLabels: RUN_STATUS_LABELS,
    });
    expect(outcome.details.join("｜")).not.toContain("开始时间");
  });

  it("状态标签缺省时原样返回服务端取值", () => {
    expect(resumeRunStatusLabel("running", RUN_STATUS_LABELS)).toBe("执行中");
    expect(resumeRunStatusLabel("future_status", RUN_STATUS_LABELS)).toBe("future_status");
  });

  it("请求失败与结果不确定使用不同的固定标题", () => {
    expect(RESUME_FAILED_TITLE).toBe("恢复请求被服务端拒绝");
    expect(RESUME_UNCERTAIN_TITLE).toBe("恢复请求结果无法确认");
    expect(RESUME_FAILED_TITLE).not.toBe(RESUME_UNCERTAIN_TITLE);
  });
});
