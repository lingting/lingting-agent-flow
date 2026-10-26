import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  attemptInitializeCodePath,
  attemptPath,
  attemptSessionsPath,
  attemptValidatePath,
  executeAndValidateStep,
  executeStep,
  executeSteps,
  fetchStepAttempts,
  fetchStepValidations,
  fetchTaskSteps,
  fetchValidationFeedback,
  generatePlan,
  initializeAttemptCode,
  openRepairCycle,
  stepExecuteAndValidatePath,
  stepExecutePath,
  stepOpenRepairCyclePath,
  stepValidationsPath,
  TASKS_PATH,
  taskExecuteStepsPath,
  taskPlanPath,
  taskStepsPath,
  validateAttempt,
  type Attempt,
  type ExecuteAndValidateStepResponse,
  type ExecuteStepResponse,
  type ExecuteStepsResponse,
  type InitializeCodeResponse,
  type OpenRepairCycleResponse,
  type PlanResponse,
  type Step,
  type ValidateAttemptResponse,
  type ValidationHistoryResponse,
} from "../../src/lib/api";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/** 一份步骤样本，字段与 Go 端 `httpapi.StepResponse` 一致 */
function sampleStep(overrides: Partial<Step> = {}): Step {
  return {
    created_at: "2026-10-26T08:00:00Z",
    id: "step-1",
    name: "实现登录接口",
    objective: "按验收条件实现登录接口",
    sequence: 1,
    status: "pending",
    status_terminal: false,
    step_type: "develop",
    task_id: "task-1",
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一份尝试样本 */
function sampleAttempt(overrides: Partial<Attempt> = {}): Attempt {
  return {
    attempt_number: 1,
    cancelled: false,
    created_at: "2026-10-26T08:00:00Z",
    exit_code: 0,
    id: "attempt-1",
    session_ids: [],
    started_at: "2026-10-26T08:00:00Z",
    status: "completed",
    status_terminal: true,
    step_id: "step-1",
    task_id: "task-1",
    timed_out: false,
    ...overrides,
  };
}

/** 一份单步骤执行响应样本 */
function sampleExecuteStep(overrides: Partial<ExecuteStepResponse> = {}): ExecuteStepResponse {
  return {
    archive_status: "archived",
    attempt_id: "attempt-1",
    attempt_number: 1,
    attempt_status: "completed",
    exit_code: 0,
    export_status: "not_attempted",
    finalized: true,
    finished_at: "2026-10-26T08:01:00Z",
    outcome: "completed",
    session_id: "session-1",
    started_at: "2026-10-26T08:00:00Z",
    step_id: "step-1",
    step_status: "completed",
    success: true,
    task_id: "task-1",
    task_status: "developing",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("执行现场路径构造", () => {
  it("步骤与尝试路径由真实标识派生并逐段编码", () => {
    expect(taskStepsPath("task-1")).toBe(`${TASKS_PATH}/task-1/steps`);
    expect(stepExecutePath("task-1", "step-1")).toBe(`${TASKS_PATH}/task-1/steps/step-1/execute`);
    expect(stepExecuteAndValidatePath("task 1", "step 1")).toBe(
      `${TASKS_PATH}/task%201/steps/step%201/execute-and-validate`,
    );
    expect(stepOpenRepairCyclePath("task-1", "step-1")).toBe(
      `${TASKS_PATH}/task-1/steps/step-1/open-repair-cycle`,
    );
    expect(taskPlanPath("task-1")).toBe(`${TASKS_PATH}/task-1/plan`);
    expect(taskExecuteStepsPath("task-1")).toBe(`${TASKS_PATH}/task-1/execute-steps`);
    expect(attemptPath("task-1", "step-1", "attempt-1")).toBe(
      `${TASKS_PATH}/task-1/steps/step-1/attempts/attempt-1`,
    );
    expect(attemptValidatePath("task-1", "step-1", "attempt-1")).toBe(
      `${TASKS_PATH}/task-1/steps/step-1/attempts/attempt-1/validate`,
    );
    expect(attemptInitializeCodePath("task-1", "step-1", "attempt-1")).toBe(
      `${TASKS_PATH}/task-1/steps/step-1/attempts/attempt-1/initialize-code`,
    );
    expect(attemptSessionsPath("task-1", "step-1", "attempt-1")).toBe(
      `${TASKS_PATH}/task-1/steps/step-1/attempts/attempt-1/sessions`,
    );
    expect(stepValidationsPath("task-1", "step-1")).toBe(
      `${TASKS_PATH}/task-1/steps/step-1/validations`,
    );
  });

  it("空的步骤或尝试标识直接拒绝，不拼出无效路径", () => {
    expect(() => stepExecutePath("task-1", "  ")).toThrow();
    expect(() => attemptValidatePath("task-1", "step-1", "")).toThrow();
  });
});

describe("步骤与执行记录查询", () => {
  it("步骤列表请求带分页参数并解析 records / total", async () => {
    const spy = vi.fn(async () =>
      jsonResponse({ current: 1, records: [sampleStep()], size: 100, total: 1 }),
    );
    vi.stubGlobal("fetch", spy);

    const { data } = await fetchTaskSteps("task-1", { size: 100 });

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/steps?size=100`);
    expect(data.total).toBe(1);
    expect(data.records[0]?.status).toBe("pending");
  });

  it("尝试列表请求使用步骤路径并保留分页回显", async () => {
    const spy = vi.fn(async () =>
      jsonResponse({ current: 1, records: [sampleAttempt()], size: 20, total: 1 }),
    );
    vi.stubGlobal("fetch", spy);

    const { data } = await fetchStepAttempts("task-1", "step-1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/steps/step-1/attempts`);
    expect(data.records[0]?.attempt_number).toBe(1);
    expect(data.records[0]?.session_ids).toEqual([]);
  });

  it("验证历史解析 latest 与逐项检查结果", async () => {
    const history: ValidationHistoryResponse = {
      latest: {
        attempt_id: "attempt-1",
        failed_checks: 1,
        id: "run-2",
        passed_checks: 0,
        review_ran: false,
        skipped_checks: 0,
        started_at: "2026-10-26T08:02:00Z",
        status: "completed",
        step_id: "step-1",
        task_id: "task-1",
        total_checks: 1,
        verdict: "failed",
      },
      records: [],
      state: "failed",
      step_id: "step-1",
      total: 0,
    };
    history.records = [history.latest!];
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(history)),
    );

    const { data } = await fetchStepValidations("task-1", "step-1");

    expect(data.state).toBe("failed");
    expect(data.latest?.verdict).toBe("failed");
    expect(data.records).toHaveLength(1);
  });

  it("验证历史为空是合法结果，不是错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ records: [], state: "unverified", step_id: "step-1", total: 0 }),
      ),
    );

    const { data } = await fetchStepValidations("task-1", "step-1");

    expect(data.records).toEqual([]);
    expect(data.latest).toBeUndefined();
    expect(data.state).toBe("unverified");
  });

  it("反馈 404 表示该轮验证没有失败反馈", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "验证失败反馈不存在" } },
          { status: 404 },
        ),
      ),
    );

    const error = (await fetchValidationFeedback("task-1", "step-1", "run-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error).toBeInstanceOf(ApiError);
    expect(error.status).toBe(404);
    expect(error.code).toBe("not_found");
  });

  it("步骤查询 404 不被伪装成空列表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "任务不存在: task-1" } },
          { status: 404 },
        ),
      ),
    );

    const error = (await fetchTaskSteps("task-1").catch((thrown: unknown) => thrown)) as ApiError;

    expect(error.status).toBe(404);
    expect(error.message).toBe("任务不存在: task-1");
  });
});

describe("执行类写操作", () => {
  it("单步骤执行使用 POST 与空请求体，并解析真实结论字段", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleExecuteStep()));
    vi.stubGlobal("fetch", spy);

    const { data } = await executeStep("task-1", "step-1");

    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    expect(target).toBe(`${TASKS_PATH}/task-1/steps/step-1/execute`);
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual({});
    expect(data.outcome).toBe("completed");
    expect(data.success).toBe(true);
    expect(data.finalized).toBe(true);
  });

  it("执行请求只发送执行参数，不发送步骤列表、Session 路径或角色覆盖", async () => {
    const spy = vi.fn(async () => jsonResponse(sampleExecuteStep()));
    vi.stubGlobal("fetch", spy);

    await executeStep("task-1", "step-1", { prompt: "补充说明", timeout_seconds: 600 });

    const [, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    const sent = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(sent).toEqual({ prompt: "补充说明", timeout_seconds: 600 });
    for (const forbidden of ["session_path", "work_dir", "role_id", "steps", "container_id"]) {
      expect(sent).not.toHaveProperty(forbidden);
    }
  });

  it("顺序执行使用任务级路径并解析停止原因", async () => {
    const response: ExecuteStepsResponse = {
      executed_steps: 1,
      finished_at: "2026-10-26T08:05:00Z",
      started_at: "2026-10-26T08:00:00Z",
      steps: [
        {
          finalized: true,
          outcome: "failed",
          sequence: 1,
          skipped: false,
          step_id: "step-1",
          step_status: "failed",
          title: "实现登录接口",
        },
      ],
      stopped: true,
      stop_reason: "步骤执行结论为 failed: 编译失败",
      stopped_step_id: "step-1",
      success: false,
      task_id: "task-1",
      task_status: "developing",
    };
    const spy = vi.fn(async () => jsonResponse(response));
    vi.stubGlobal("fetch", spy);

    const { data } = await executeSteps("task-1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/execute-steps`);
    expect(data.stopped).toBe(true);
    expect(data.success).toBe(false);
    expect(data.stop_reason).toBe("步骤执行结论为 failed: 编译失败");
    expect(data.steps[0]?.step_status).toBe("failed");
  });

  it("闭环响应区分开发结论、验证是否发起与最终是否通过", async () => {
    const response: ExecuteAndValidateStepResponse = {
      attempt_number: 1,
      attempts: 1,
      developer_finalized: true,
      exit_code: 0,
      feedback_persisted: true,
      finalized: true,
      finished_at: "2026-10-26T08:10:00Z",
      infrastructure_error: false,
      max_attempts: 3,
      outcome: "completed",
      repair_decision: "stop",
      review_ran: false,
      round: 1,
      started_at: "2026-10-26T08:00:00Z",
      step_id: "step-1",
      step_status: "verifying",
      task_id: "task-1",
      task_status: "developing",
      validation_passed: false,
      validation_persisted: true,
      validation_ran: true,
      validation_status: "completed",
      validation_verdict: "failed",
      verification_state: "failed",
      verified: false,
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(response)),
    );

    const { data } = await executeAndValidateStep("task-1", "step-1");

    expect(data.outcome).toBe("completed");
    expect(data.validation_ran).toBe(true);
    expect(data.validation_verdict).toBe("failed");
    expect(data.verified).toBe(false);
  });

  it("闭环业务失败（未通过验证）返回 500 与 execution_failed，不被当作成功", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            error: {
              code: "execution_failed",
              message: "步骤未通过验证（failed）: 必需检查未通过: build",
            },
          },
          { status: 500 },
        ),
      ),
    );

    const error = (await executeAndValidateStep("task-1", "step-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.status).toBe(500);
    expect(error.code).toBe("execution_failed");
    expect(error.message).toContain("步骤未通过验证");
  });

  it("验证请求解析流程状态、结论与持久化结果", async () => {
    const response: ValidateAttemptResponse = {
      attempt_id: "attempt-1",
      checks: [],
      failed_checks: 1,
      finished_at: "2026-10-26T08:03:00Z",
      passed: false,
      passed_checks: 0,
      persisted: true,
      review_ran: false,
      skipped_checks: 0,
      started_at: "2026-10-26T08:02:00Z",
      status: "completed",
      step_id: "step-1",
      task_id: "task-1",
      validation_run_id: "run-1",
      verdict: "failed",
    };
    const spy = vi.fn(async () => jsonResponse(response));
    vi.stubGlobal("fetch", spy);

    const { data } = await validateAttempt("task-1", "step-1", "attempt-1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/steps/step-1/attempts/attempt-1/validate`);
    expect(data.status).toBe("completed");
    expect(data.verdict).toBe("failed");
    expect(data.persisted).toBe(true);
  });

  it("验证结果未能持久化时返回 execution_not_persisted", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            error: {
              code: "execution_not_persisted",
              message: "验证已执行，但结果未能可靠写入数据库",
            },
          },
          { status: 500 },
        ),
      ),
    );

    const error = (await validateAttempt("task-1", "step-1", "attempt-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.code).toBe("execution_not_persisted");
  });

  it("代码初始化解析就绪状态与持久化状态", async () => {
    const response: InitializeCodeResponse = {
      attempt_id: "attempt-1",
      container_id: "container-1",
      finished_at: "2026-10-26T08:04:00Z",
      persisted: true,
      record_id: "record-1",
      source_type: "git",
      started_at: "2026-10-26T08:00:00Z",
      status: "succeeded",
      step_id: "step-1",
      succeeded: true,
      task_branch: "agent/task-1",
      task_id: "task-1",
      work_dir: "/workspace/project",
    };
    const spy = vi.fn(async () => jsonResponse(response));
    vi.stubGlobal("fetch", spy);

    const { data } = await initializeAttemptCode("task-1", "step-1", "attempt-1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/steps/step-1/attempts/attempt-1/initialize-code`);
    expect(data.succeeded).toBe(true);
    expect(data.task_branch).toBe("agent/task-1");
  });

  it("修复周期拒绝原因原样返回", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            error: {
              code: "state_conflict",
              message: "仍在上限内，进入第 2 次开发尝试",
            },
          },
          { status: 409 },
        ),
      ),
    );

    const error = (await openRepairCycle("task-1", "step-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.status).toBe(409);
    expect(error.code).toBe("state_conflict");
    expect(error.message).toBe("仍在上限内，进入第 2 次开发尝试");
  });

  it("修复周期成功响应只描述预算登记事实", async () => {
    const response: OpenRepairCycleResponse = {
      cycle_id: "cycle-2",
      cycle_sequence: 2,
      max_attempts: 3,
      opened_at: "2026-10-26T09:00:00Z",
      previous_consumed: 3,
      step_id: "step-1",
      task_id: "task-1",
    };
    const spy = vi.fn(async () => jsonResponse(response));
    vi.stubGlobal("fetch", spy);

    const { data } = await openRepairCycle("task-1", "step-1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/steps/step-1/open-repair-cycle`);
    expect(data.cycle_sequence).toBe(2);
    expect(data.previous_consumed).toBe(3);
  });

  it("计划生成解析 persisted 与失败阶段", async () => {
    const response: PlanResponse = {
      failure: "Planner 输出无法解析为计划",
      failure_stage: "plan_parse",
      persisted: false,
      steps: [],
      task_id: "task-1",
      task_status: "recovery_required",
    };
    const spy = vi.fn(async () => jsonResponse(response));
    vi.stubGlobal("fetch", spy);

    const { data } = await generatePlan("task-1");

    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/plan`);
    expect(data.persisted).toBe(false);
    expect(data.failure_stage).toBe("plan_parse");
  });

  it("网络错误报告为 network，不伪造成功", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    const error = (await executeStep("task-1", "step-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.kind).toBe("network");
  });

  it("前置条件缺失映射为 412 并保留服务端提示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "precondition_failed", message: "执行节点未通过环境预检" } },
          { status: 412 },
        ),
      ),
    );

    const error = (await executeStep("task-1", "step-1").catch(
      (thrown: unknown) => thrown,
    )) as ApiError;

    expect(error.status).toBe(412);
    expect(error.code).toBe("precondition_failed");
    expect(error.message).toBe("执行节点未通过环境预检");
  });
});
