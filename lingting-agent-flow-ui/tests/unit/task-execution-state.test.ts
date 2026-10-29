import { describe, expect, it } from "vitest";

import type {
  Attempt,
  ExecuteAndValidateStepResponse,
  ExecuteStepResponse,
  ExecuteStepsResponse,
  InitializeCodeResponse,
  RepairCycleEligibilityResponse,
  OpenRepairCycleResponse,
  PlanResponse,
  Step,
  StepStatus,
  Task,
  TaskStatus,
  ValidateAttemptResponse,
  ValidationCheckResult,
} from "../../src/lib/api";
import {
  ATTEMPT_STATUS_META,
  attemptStatusMeta,
  canExecuteAndValidate,
  canExecuteStep,
  canGeneratePlan,
  canInitializeAttempt,
  canRunSteps,
  canValidateAttempt,
  describeExecuteAndValidate,
  describeExecuteAndValidateResult,
  describeExecuteStepResult,
  describeExecuteStepsResult,
  describeInitializeResult,
  describeOpenRepairCycleResult,
  describePlanResult,
  describeStepExecution,
  describeValidateResult,
  executionOutcomeLabel,
  executeAndValidateResultFromBody,
  REVIEW_STATUS_LABEL,
  reviewStatusLabel,
  STEP_STATUS_META,
  stepStatusMeta,
  stepTypeLabel,
  taskAllowsPlanning,
  taskAllowsStepExecution,
  taskAllowsValidation,
  isUncertainOperationError,
  OPERATION_UNKNOWN_OUTCOME_HINT,
  validationStateLabel,
  verdictMeta,
} from "../../src/pages/tasks/steps";
import {
  describeRepairCycleAction,
  describeRepairCycleFacts,
  eligibilityBelongsTo,
  repairCycleBlockerLabel,
  type RepairCycleEligibilityState,
} from "../../src/pages/tasks/repairCycleEligibility";
import { describeApiError, isNotFound } from "../../src/pages/tasks/useExecutionRecords";
import { ApiError } from "../../src/lib/api";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 一份任务样本 */
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

/** 一份步骤样本 */
function step(overrides: Partial<Step> = {}): Step {
  return {
    created_at: "2026-10-26T08:00:00Z",
    id: "step-1",
    name: "实现登录接口",
    objective: "实现登录接口",
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
function attempt(overrides: Partial<Attempt> = {}): Attempt {
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

/** 资格判定的归属：默认与样例任务 / 步骤一致 */
function owner(overrides: { taskID?: string; stepID?: string } = {}) {
  return { stepID: "step-1", taskID: "task-1", ...overrides };
}

/** 一份修复周期资格样本（默认：上一周期预算已用尽，服务端允许开启新周期） */
function eligibility(
  overrides: Partial<RepairCycleEligibilityResponse> = {},
): RepairCycleEligibilityResponse {
  return {
    active_cycle: false,
    attempts: 1,
    blocking: [],
    can_open_cycle: true,
    consumed_attempts: 1,
    cycle_sequence: 1,
    max_attempts: 1,
    remaining_attempts: 0,
    step_id: "step-1",
    step_status: "failed",
    summary: "允许开启新的执行周期：上一周期已用 1 次尝试，新周期上限 1",
    task_id: "task-1",
    task_status: "developing",
    ...overrides,
  };
}

/** 一份"服务端已给出结论"的资格状态 */
function readyEligibility(
  overrides: Partial<RepairCycleEligibilityResponse> = {},
): RepairCycleEligibilityState {
  return { data: eligibility(overrides), status: "ready" };
}

/** 一份"服务端明确拒绝"的资格状态 */
function deniedEligibility(
  blocking: string[],
  summary = "当前执行周期仍有剩余预算（已用 1 / 3），不允许通过开启新周期重置预算",
  overrides: Partial<RepairCycleEligibilityResponse> = {},
): RepairCycleEligibilityState {
  return readyEligibility({ blocking, can_open_cycle: false, summary, ...overrides });
}

describe("步骤状态元数据", () => {
  it("覆盖 Go 端 domain.StepStatus 的全部取值", () => {
    const expected: StepStatus[] = [
      "pending",
      "running",
      "developing",
      "verifying",
      "completed",
      "failed",
      "retry_wait",
      "skipped",
      "cancelled",
      "recovery_required",
    ];
    expect(Object.keys(STEP_STATUS_META).sort()).toEqual([...expected].sort());
  });

  it("只有 completed 使用成功色，只有 failed 使用错误色", () => {
    expect(stepStatusMeta("completed").color).toBe("success");
    expect(stepStatusMeta("failed").color).toBe("error");
    for (const status of [
      "pending",
      "running",
      "retry_wait",
      "recovery_required",
    ] as StepStatus[]) {
      const meta = stepStatusMeta(status);
      expect(meta.color).not.toBe("success");
      expect(meta.color).not.toBe("error");
    }
  });

  it("未知步骤状态原样展示，不猜测语义", () => {
    expect(stepStatusMeta("paused_by_operator").label).toBe("paused_by_operator");
    expect(stepStatusMeta("paused_by_operator").active).toBe(false);
  });

  it("步骤类型标签覆盖全部类型，未知类型原样展示", () => {
    for (const type of [
      "prepare",
      "source",
      "plan",
      "develop",
      "verify",
      "review",
      "save",
      "cleanup",
    ]) {
      expect(stepTypeLabel(type)).not.toBe(type);
    }
    expect(stepTypeLabel("unknown_stage")).toBe("unknown_stage");
  });

  it("尝试状态覆盖全部取值，未知取值原样展示", () => {
    for (const status of [
      "running",
      "completed",
      "failed",
      "cancelled",
      "interrupted",
      "timed_out",
      "unknown",
      "recovery_required",
    ]) {
      expect(attemptStatusMeta(status).label).not.toBe(status);
      expect(ATTEMPT_STATUS_META).toHaveProperty(status);
    }
    expect(attemptStatusMeta("mystery").label).toBe("mystery");
  });

  it("验证状态与结论的未知取值不被显示为通过", () => {
    expect(validationStateLabel("unverified")).toBe("尚未验证");
    expect(validationStateLabel(undefined)).toBe("尚未验证");
    expect(validationStateLabel("weird_state")).toBe("weird_state");
    expect(verdictMeta("inconclusive").label).toBe("无法判定");
    expect(verdictMeta(undefined).label).toBe("—");
    expect(verdictMeta("weird_verdict").label).toBe("weird_verdict");
    expect(executionOutcomeLabel(undefined)).toBe("—");
    expect(executionOutcomeLabel("interrupted")).toBe("已中断");
  });
});

describe("任务级准入判定与服务端一致", () => {
  it("只有能迁移到 developing 的状态允许推进步骤", () => {
    for (const status of [
      "preparing",
      "planned",
      "developing",
      "retrying",
      "paused",
      "recovery_required",
    ] as TaskStatus[]) {
      expect(taskAllowsStepExecution(task({ status }))).toBe(true);
    }
    for (const status of [
      "queued",
      "planning",
      "verifying",
      "saving",
      "cancelling",
      "completed",
      "failed",
      "cancelled",
    ] as TaskStatus[]) {
      expect(taskAllowsStepExecution(task({ status }))).toBe(false);
    }
  });

  it("已请求取消的任务不允许推进步骤", () => {
    expect(taskAllowsStepExecution(task({ cancel_requested: true, status: "developing" }))).toBe(
      false,
    );
  });

  it("显式规划集合不含 failed 与 developing", () => {
    for (const status of [
      "queued",
      "preparing",
      "planned",
      "paused",
      "recovery_required",
    ] as TaskStatus[]) {
      expect(taskAllowsPlanning(task({ status }))).toBe(true);
    }
    for (const status of ["failed", "developing", "cancelling", "completed"] as TaskStatus[]) {
      expect(taskAllowsPlanning(task({ status }))).toBe(false);
    }
  });

  it("验证准入只包含执行链上的状态", () => {
    for (const status of [
      "developing",
      "verifying",
      "retrying",
      "paused",
      "recovery_required",
    ] as TaskStatus[]) {
      expect(taskAllowsValidation(task({ status }))).toBe(true);
    }
    for (const status of [
      "queued",
      "planned",
      "cancelling",
      "completed",
      "failed",
    ] as TaskStatus[]) {
      expect(taskAllowsValidation(task({ status }))).toBe(false);
    }
  });
});

describe("步骤级操作可用性", () => {
  it("只有可重新开始的步骤状态显示执行入口", () => {
    for (const status of ["pending", "retry_wait", "failed", "recovery_required"] as StepStatus[]) {
      expect(canExecuteStep(task(), step({ status }))).toBe(true);
    }
    for (const status of [
      "running",
      "developing",
      "verifying",
      "completed",
      "skipped",
      "cancelled",
    ] as StepStatus[]) {
      expect(canExecuteStep(task(), step({ status }))).toBe(false);
    }
  });

  it("被占用或非 Agent 步骤不显示执行入口", () => {
    expect(canExecuteStep(task(), step({ current_attempt_id: "attempt-1" }))).toBe(false);
    expect(canExecuteStep(task(), step({ step_type: "verify" }))).toBe(false);
    expect(canExecuteStep(task({ status: "queued" }), step())).toBe(false);
  });

  it("闭环入口与执行入口的准入一致", () => {
    expect(canExecuteAndValidate(task(), step())).toBe(true);
    expect(canExecuteAndValidate(task({ status: "queued" }), step())).toBe(false);
    expect(canExecuteAndValidate(task(), step({ status: "verifying" }))).toBe(false);
  });

  it("开启修复周期只由服务端资格决定", () => {
    // 服务端允许：放行，且不产生任何前端自己的禁用原因。
    expect(describeRepairCycleAction(readyEligibility(), owner())).toEqual({
      allowed: true,
      reason: "",
    });
    // 服务端拒绝：不放行，原因是服务端给出的真实说明。
    const denied = describeRepairCycleAction(deniedEligibility(["new_cycle_denied"]), owner());
    expect(denied.allowed).toBe(false);
    expect(denied.reason).toContain("仍有剩余预算");
    // 前端不再自行排除"步骤被占用"或"任务状态不允许"：这两类现场由服务端在
    // 资格查询里给出 execution_active / task_status_not_executable 阻塞类别。
    for (const blocking of ["execution_active", "task_status_not_executable"]) {
      expect(describeRepairCycleAction(deniedEligibility([blocking]), owner()).allowed).toBe(false);
    }
  });

  it("资格未知、未归属与刷新失败一律不放行", () => {
    const cases: RepairCycleEligibilityState[] = [
      { status: "idle" },
      { status: "loading" },
      { error: "无法连接 AgentFlow 服务", status: "error" },
      // 快照归属不符：它描述的是另一个任务或步骤。
      readyEligibility({ step_id: "step-2" }),
    ];
    for (const state of cases) {
      const action = describeRepairCycleAction(state, owner());
      expect(action.allowed).toBe(false);
      expect(action.reason.length).toBeGreaterThan(0);
    }
    // 刷新失败：快照仍然展示，但明确它可能已过期且不得据此放行。
    const stale: RepairCycleEligibilityState = {
      data: eligibility(),
      error: "请求超时",
      status: "ready",
    };
    const staleAction = describeRepairCycleAction(stale, owner());
    expect(staleAction.allowed).toBe(false);
    expect(staleAction.reason).toContain("可能已经过期");
  });

  it("顺序执行要求存在未结束的步骤", () => {
    expect(canRunSteps(task(), [step()])).toBe(true);
    expect(canRunSteps(task(), [step({ status: "completed" })])).toBe(false);
    expect(canRunSteps(task(), [step({ status: "skipped" })])).toBe(false);
    expect(canRunSteps(task({ status: "queued" }), [step()])).toBe(false);
  });
  it("重新查询期间保留旧快照但不放行", () => {
    const refreshing: RepairCycleEligibilityState = {
      data: eligibility(),
      refreshing: true,
      status: "ready",
    };

    const action = describeRepairCycleAction(refreshing, owner());

    expect(action.allowed).toBe(false);
    expect(action.reason).toContain("正在重新确认");
    expect(describeRepairCycleFacts(refreshing, owner()).join(" ")).toContain("正在重新查询");
  });

  it("生成计划要求允许规划且没有任何步骤（服务端拒绝覆盖已有计划）", () => {
    expect(canGeneratePlan(task({ status: "planned" }), [])).toBe(true);
    expect(canGeneratePlan(task({ status: "planned" }), [step()])).toBe(false);
    expect(canGeneratePlan(task({ status: "developing" }), [])).toBe(false);
  });

  it("验证可用性同时受任务与尝试状态约束", () => {
    expect(canValidateAttempt(task(), attempt())).toBe(true);
    expect(canValidateAttempt(task(), attempt({ status: "running" }))).toBe(false);
    expect(canValidateAttempt(task(), attempt({ status: "unknown" }))).toBe(false);
    expect(canValidateAttempt(task({ status: "queued" }), attempt())).toBe(false);
    expect(canValidateAttempt(task(), undefined)).toBe(false);
  });

  it("初始化代码的准入与服务端集合一致", () => {
    for (const status of ["running", "failed", "interrupted", "recovery_required"] as const) {
      expect(canInitializeAttempt(task(), attempt({ status }))).toBe(true);
    }
    for (const status of ["completed", "cancelled", "timed_out", "unknown"] as const) {
      expect(canInitializeAttempt(task(), attempt({ status }))).toBe(false);
    }
    expect(canInitializeAttempt(task({ status: "completed" }), attempt({ status: "failed" }))).toBe(
      false,
    );
    expect(
      canInitializeAttempt(task({ cancel_requested: true }), attempt({ status: "failed" })),
    ).toBe(false);
  });
});
describe("步骤级操作的禁用原因", () => {
  it("允许执行时不产生任何禁用原因", () => {
    expect(describeStepExecution(task(), step())).toEqual({ allowed: true, reason: "" });
    expect(describeExecuteAndValidate(task(), step())).toEqual({ allowed: true, reason: "" });
    expect(describeRepairCycleAction(readyEligibility(), owner())).toEqual({
      allowed: true,
      reason: "",
    });
  });

  it("任务状态层的原因是真实状态，不是模糊的不可用", () => {
    const queued = describeStepExecution(task({ status: "queued" }), step());

    expect(queued.allowed).toBe(false);
    expect(queued.reason).toContain("等待执行");
    expect(queued.reason).toContain("环境准备");
    // 与页面状态标签同源：同一个事实不能一处写"等待执行"、一处写"queued"
    expect(queued.reason).not.toContain("queued");
  });

  it("已请求取消时说明取消请求尚未结束", () => {
    const blocked = describeStepExecution(task({ cancel_requested: true }), step());

    expect(blocked.allowed).toBe(false);
    expect(blocked.reason).toContain("取消");
    expect(blocked.reason).toContain("尚未结束");
  });

  it("未知任务状态原样展示，不映射成已知状态", () => {
    const blocked = describeStepExecution(task({ status: "future_status" as TaskStatus }), step());

    expect(blocked.allowed).toBe(false);
    expect(blocked.reason).toContain("future_status");
  });

  it("步骤状态不允许时说明当前真实步骤状态", () => {
    for (const status of [
      "running",
      "developing",
      "verifying",
      "completed",
      "skipped",
      "cancelled",
    ] as StepStatus[]) {
      const blocked = describeStepExecution(task(), step({ status }));

      expect(blocked.allowed).toBe(false);
      expect(blocked.reason).toContain(stepStatusMeta(status).label);
      expect(blocked.reason).not.toContain(status);
    }
  });

  it("步骤仍被尝试占用时说明占用而不是状态不允许", () => {
    const blocked = describeStepExecution(task(), step({ current_attempt_id: "attempt-1" }));

    expect(blocked.allowed).toBe(false);
    expect(blocked.reason).toContain("当前执行尝试占用");
    expect(blocked.reason).not.toContain(stepStatusMeta("pending").label);
  });

  it("非 Agent 步骤说明由服务端直接执行", () => {
    const blocked = describeStepExecution(task(), step({ step_type: "verify" }));

    expect(blocked.allowed).toBe(false);
    expect(blocked.reason).toContain(stepTypeLabel("verify"));
    expect(blocked.reason).toContain("服务端直接执行");
  });

  it("未知步骤状态与未知步骤类型都原样出现在原因里", () => {
    const unknownStatus = describeStepExecution(
      task(),
      step({ status: "future_status" as StepStatus }),
    );
    const unknownType = describeStepExecution(
      task(),
      step({ step_type: "future_type" as Step["step_type"] }),
    );

    expect(unknownStatus.reason).toContain("future_status");
    expect(unknownType.reason).toContain("future_type");
  });

  it("闭环入口与执行入口给出同一份原因", () => {
    for (const blockedTask of [task({ status: "queued" }), task({ cancel_requested: true })]) {
      expect(describeExecuteAndValidate(blockedTask, step())).toEqual(
        describeStepExecution(blockedTask, step()),
      );
    }
  });

  it("修复周期准入只读资格状态，不读任务或步骤对象", () => {
    // 函数只接受资格状态与归属：任务状态、步骤状态与步骤类型都不再参与判定，
    // 因此前端不可能出现"第二套粗判"。执行入口仍按这三项判定（见上方用例）。
    expect(describeRepairCycleAction(readyEligibility(), owner())).toEqual({
      allowed: true,
      reason: "",
    });
    // 服务端对"步骤当前不可开始执行"给出的类别是 execution_active，页面原样展示原因。
    const blocked = describeRepairCycleAction(
      deniedEligibility(["execution_active"], "步骤仍被一次执行占用，不能启动第二个执行者"),
      owner(),
    );
    expect(blocked.allowed).toBe(false);
    expect(blocked.reason).toContain("步骤仍被一次执行占用");
  });

  it("缺少步骤时修复周期给出明确原因而不是静默禁用", () => {
    const blocked = describeRepairCycleAction({ status: "idle" }, { stepID: "", taskID: "task-1" });

    expect(blocked.allowed).toBe(false);
    expect(blocked.reason).toContain("尚未选择步骤");
  });

  it("阻塞类别未知时原样保留，不映射成允许", () => {
    expect(repairCycleBlockerLabel("execution_active")).toBe("步骤仍被当前执行占用");
    expect(repairCycleBlockerLabel("future_blocker")).toBe("future_blocker");

    const action = describeRepairCycleAction(deniedEligibility(["future_blocker"], ""), owner());
    expect(action.allowed).toBe(false);
    expect(action.reason).toContain("future_blocker");
  });

  it("资格事实直接展示，并明确「只登记预算」与首次执行的边界", () => {
    const allowed = describeRepairCycleFacts(readyEligibility(), owner());
    expect(allowed.join(" ")).toContain("只登记预算");
    expect(allowed.join(" ")).toContain("已用 1 / 1 次");

    const firstRun = describeRepairCycleFacts(
      deniedEligibility(["new_cycle_denied"], "该步骤还没有任何执行记录", {
        attempts: 0,
        can_open_cycle: false,
        consumed_attempts: 0,
      }),
      owner(),
    );
    expect(firstRun.join(" ")).toContain("首轮执行本身就是第一个执行周期");

    // 快照归属不符时不展示任何事实，避免把别处的资格读成本步骤的结论。
    expect(describeRepairCycleFacts(readyEligibility({ task_id: "task-2" }), owner())).toEqual([]);
    expect(eligibilityBelongsTo(eligibility(), owner())).toBe(true);
  });

  it("原因与实际可用性结论始终一致，且不允许时必有原因", () => {
    const tasks = [
      task(),
      task({ status: "queued" }),
      task({ status: "planned" }),
      task({ status: "completed" }),
      task({ status: "failed", status_finished: true }),
      task({ cancel_requested: true }),
      task({ status: "future_status" as TaskStatus }),
    ];
    const steps = [
      step(),
      step({ status: "completed" }),
      step({ status: "verifying" }),
      step({ current_attempt_id: "attempt-1" }),
      step({ step_type: "plan" }),
      step({ step_type: "cleanup" }),
    ];

    for (const currentTask of tasks) {
      for (const currentStep of steps) {
        const execution = describeStepExecution(currentTask, currentStep);
        const closure = describeExecuteAndValidate(currentTask, currentStep);

        expect(execution.allowed).toBe(canExecuteStep(currentTask, currentStep));
        expect(closure.allowed).toBe(canExecuteAndValidate(currentTask, currentStep));
        for (const result of [execution, closure]) {
          expect(result.allowed ? result.reason === "" : result.reason.length > 0).toBe(true);
        }
        expect(closure).toEqual(execution);
      }
    }
    // 修复周期的准入不读取任务或步骤状态：它只取决于服务端资格结论。
    const repairCases: RepairCycleEligibilityState[] = [
      readyEligibility(),
      deniedEligibility(["new_cycle_denied"]),
      { status: "loading" },
      { error: "请求超时", status: "error" },
    ];
    for (const state of repairCases) {
      const action = describeRepairCycleAction(state, owner());
      expect(action.allowed).toBe(action.reason === "");
    }
  });
});

describe("执行结论翻译", () => {
  it("计划未持久化时不显示为已完成", () => {
    const result: PlanResponse = {
      failure: "Planner 输出无法解析",
      failure_stage: "plan_parse",
      persisted: false,
      steps: [],
      task_id: "task-1",
      task_status: "recovery_required",
    };
    const outcome = describePlanResult(result);
    expect(outcome.kind).toBe("warning");
    expect(outcome.title).toBe("计划未持久化");
    expect(outcome.details.join(" ")).toContain("未持久化");
    expect(outcome.details.join(" ")).toContain("plan_parse");
  });

  it("计划已持久化时展示真实步骤数", () => {
    const outcome = describePlanResult({
      persisted: true,
      steps: [
        {
          required_checks: ["build"],
          sequence: 1,
          status: "pending",
          step_id: "step-1",
          step_type: "develop",
          title: "实现登录接口",
        },
      ],
      task_id: "task-1",
      task_status: "planned",
    });
    expect(outcome.kind).toBe("success");
    expect(outcome.details.join(" ")).toContain("已持久化 1 个步骤");
  });

  it("单步骤执行成功不表述为任务成功", () => {
    const result: ExecuteStepResponse = {
      archive_status: "archived",
      attempt_id: "attempt-1",
      attempt_number: 1,
      attempt_status: "completed",
      exit_code: 0,
      export_status: "not_attempted",
      finalized: true,
      finished_at: "2026-10-26T08:01:00Z",
      outcome: "completed",
      started_at: "2026-10-26T08:00:00Z",
      step_id: "step-1",
      step_status: "completed",
      success: true,
      task_id: "task-1",
      task_status: "developing",
    };
    const outcome = describeExecuteStepResult(result);
    expect(outcome.kind).toBe("success");
    expect(outcome.title).toBe("步骤执行已结束");
    expect(outcome.details.join(" ")).toContain("不代表任务已通过验收");
  });

  it("执行未成功且状态未写入时给出两类事实", () => {
    const outcome = describeExecuteStepResult({
      archive_status: "not_attempted",
      attempt_id: "attempt-1",
      attempt_number: 1,
      attempt_status: "failed",
      exit_code: 1,
      export_status: "not_attempted",
      finalization_errors: ["写入步骤状态失败"],
      finalized: false,
      finished_at: "2026-10-26T08:01:00Z",
      outcome: "failed",
      started_at: "2026-10-26T08:00:00Z",
      step_id: "step-1",
      step_status: "developing",
      success: false,
      task_id: "task-1",
      task_status: "developing",
    });
    expect(outcome.kind).toBe("warning");
    expect(outcome.details.join(" ")).toContain("写入步骤状态失败");
  });

  it("顺序执行区分 stopped 与 success", () => {
    const result: ExecuteStepsResponse = {
      executed_steps: 2,
      finished_at: "2026-10-26T08:10:00Z",
      started_at: "2026-10-26T08:00:00Z",
      steps: [
        {
          finalized: true,
          outcome: "completed",
          sequence: 1,
          skipped: false,
          step_id: "step-1",
          step_status: "completed",
          title: "第一步",
        },
        {
          finalized: true,
          outcome: "failed",
          sequence: 2,
          skipped: false,
          step_id: "step-2",
          step_status: "failed",
          title: "第二步",
        },
      ],
      stopped: true,
      stop_reason: "步骤执行结论为 failed: 编译失败",
      success: false,
      task_id: "task-1",
      task_status: "developing",
    };
    const outcome = describeExecuteStepsResult(result);
    expect(outcome.title).toBe("顺序执行已停止");
    expect(outcome.kind).toBe("warning");
    expect(outcome.details.join(" ")).toContain("已提前停止");
    expect(outcome.details.join(" ")).toContain("不包含验证、提交、推送与合并");
  });

  it("闭环只有 verified 才是通过", () => {
    const result: ExecuteAndValidateStepResponse = {
      attempt_number: 1,
      attempts: 2,
      developer_finalized: true,
      exit_code: 0,
      feedback_persisted: true,
      finalized: true,
      finished_at: "2026-10-26T08:10:00Z",
      infrastructure_error: false,
      max_attempts: 3,
      outcome: "completed",
      repair_decision: "stop",
      repair_summary: "已达到尝试次数上限",
      review_ran: false,
      round: 2,
      started_at: "2026-10-26T08:00:00Z",
      step_id: "step-1",
      step_status: "verifying",
      task_id: "task-1",
      task_status: "developing",
      validation_failure_summary: "必需检查未通过: build",
      validation_passed: false,
      validation_persisted: true,
      validation_ran: true,
      validation_status: "completed",
      validation_verdict: "failed",
      verification_state: "failed",
      verified: false,
    };
    const outcome = describeExecuteAndValidateResult(result);
    expect(outcome.kind).toBe("warning");
    expect(outcome.title).toBe("步骤未通过验证");
    expect(outcome.details.join(" ")).toContain("验证是否发起：是");
    expect(outcome.details.join(" ")).toContain("必需检查未通过: build");
    expect(outcome.details.join(" ")).toContain("已消耗 2 / 3 次尝试");
    // 失败响应体里的结构化执行事实必须能被读出，且不会被误判为成功。
    const failureBody = {
      error: { code: "execution_failed", message: "步骤未通过验证" },
      operation_result: result,
    };
    expect(executeAndValidateResultFromBody(failureBody)?.verified).toBe(false);
    // 没有真实执行结果的失败（状态冲突等）不得被读成执行详情。
    expect(
      executeAndValidateResultFromBody({ error: { code: "state_conflict", message: "冲突" } }),
    ).toBeUndefined();
  });

  it("闭环未发起验证时如实说明原因", () => {
    const outcome = describeExecuteAndValidateResult({
      attempt_number: 1,
      attempts: 1,
      developer_finalized: true,
      exit_code: 1,
      finalized: true,
      finished_at: "2026-10-26T08:10:00Z",
      infrastructure_error: false,
      max_attempts: 3,
      feedback_persisted: false,
      outcome: "failed",
      repair_decision: "stop",
      review_ran: false,
      round: 1,
      started_at: "2026-10-26T08:00:00Z",
      step_id: "step-1",
      step_status: "failed",
      task_id: "task-1",
      task_status: "developing",
      validation_passed: false,
      validation_persisted: false,
      validation_ran: false,
      validation_skip_reason: "开发阶段未成功",
      verification_state: "unverified",
      verified: false,
    });
    expect(outcome.details.join(" ")).toContain("验证是否发起：否（开发阶段未成功）");
  });

  it("验证结论与持久化分别展示", () => {
    const result: ValidateAttemptResponse = {
      attempt_id: "attempt-1",
      checks: [],
      failed_checks: 0,
      finished_at: "2026-10-26T08:03:00Z",
      passed: true,
      passed_checks: 2,
      persistence_errors: ["写入检查结果失败"],
      persisted: false,
      review_ran: false,
      skipped_checks: 0,
      started_at: "2026-10-26T08:02:00Z",
      status: "completed",
      step_id: "step-1",
      task_id: "task-1",
      validation_run_id: "run-1",
      verdict: "passed",
    };
    const outcome = describeValidateResult(result);
    expect(outcome.title).toBe("验证通过");
    expect(outcome.details.join(" ")).toContain("结果是否可靠写入：否");
    expect(outcome.details.join(" ")).toContain("写入检查结果失败");
  });

  it("代码初始化区分就绪与记录写入", () => {
    const result: InitializeCodeResponse = {
      attempt_id: "attempt-1",
      container_id: "container-1",
      error: "克隆失败: 认证失败",
      error_code: "repository_auth_required",
      finished_at: "2026-10-26T08:04:00Z",
      persistence_errors: ["写入初始化记录失败"],
      persisted: false,
      record_id: "record-1",
      source_type: "git",
      stage: "clone",
      started_at: "2026-10-26T08:00:00Z",
      status: "failed",
      step_id: "step-1",
      succeeded: false,
      task_branch: "agent/task-1",
      task_id: "task-1",
      work_dir: "/workspace/project",
    };
    const outcome = describeInitializeResult(result);
    expect(outcome.kind).toBe("warning");
    expect(outcome.title).toBe("代码初始化未成功");
    expect(outcome.details.join(" ")).toContain("阶段：clone");
    expect(outcome.details.join(" ")).toContain("错误类别：repository_auth_required");
  });

  it("修复周期成功只说明预算登记事实", () => {
    const result: OpenRepairCycleResponse = {
      cycle_id: "cycle-2",
      cycle_sequence: 2,
      max_attempts: 3,
      opened_at: "2026-10-26T09:00:00Z",
      previous_consumed: 3,
      step_id: "step-1",
      task_id: "task-1",
    };
    const outcome = describeOpenRepairCycleResult(result);
    expect(outcome.kind).toBe("success");
    expect(outcome.details.join(" ")).toContain("不会自动执行");
    expect(outcome.details.join(" ")).toContain("上一周期已消耗：3 次");
  });
});
describe("单步诊断动作结论的诚实性", () => {
  /** 一份最简验证响应；只覆盖用例关心的事实 */
  function validationResult(
    overrides: Partial<ValidateAttemptResponse> = {},
  ): ValidateAttemptResponse {
    return {
      attempt_id: "attempt-1",
      checks: [],
      failed_checks: 0,
      finished_at: "2026-10-26T08:03:00Z",
      passed: true,
      passed_checks: 1,
      persisted: true,
      review_ran: false,
      skipped_checks: 0,
      started_at: "2026-10-26T08:02:00Z",
      status: "completed",
      step_id: "step-1",
      task_id: "task-1",
      validation_run_id: "run-1",
      verdict: "passed",
      ...overrides,
    };
  }

  it("只有判定为 passed 才展示验证通过", () => {
    expect(describeValidateResult(validationResult()).title).toBe("验证通过");
    expect(describeValidateResult(validationResult()).kind).toBe("success");
  });

  it("判定 inconclusive 与未知取值都不展示为通过", () => {
    const inconclusive = describeValidateResult(
      validationResult({ passed: false, verdict: "inconclusive" }),
    );
    expect(inconclusive.kind).toBe("warning");
    expect(inconclusive.title).toBe("验证结论无法判定");

    // 服务端新增取值时如实展示原始文本，不回落成"通过"或"未通过"
    const unknown = describeValidateResult(
      validationResult({ passed: false, verdict: "needs_review" as never }),
    );
    expect(unknown.kind).toBe("warning");
    expect(unknown.title).toBe("验证结论无法判定");
    expect(unknown.details.join(" ")).toContain("needs_review");
  });

  it("判定缺失时说明服务端未提供，而不是展示为通过", () => {
    const outcome = describeValidateResult(
      validationResult({ passed: false, verdict: undefined as never }),
    );
    expect(outcome.kind).toBe("warning");
    expect(outcome.title).toBe("验证结论无法判定");
    expect(outcome.details.join(" ")).toContain("验证结论：服务端未提供");
  });

  it("逐项检查结果按真实字段展示，结果不可确认不被当作代码问题", () => {
    const check: ValidationCheckResult = {
      cancelled: false,
      certain: false,
      check_id: "build",
      check_name: "构建",
      duration_ms: 1200,
      exit_code: -1,
      finished_at: "2026-10-26T08:03:00Z",
      output_truncated: true,
      required: true,
      required_passed: false,
      sequence: 1,
      started_at: "2026-10-26T08:02:00Z",
      status: "error",
      stderr_summary: "连接容器超时",
      timed_out: false,
    };
    const outcome = describeValidateResult(
      validationResult({ checks: [check], failed_checks: 1, passed: false, verdict: "failed" }),
    );
    const text = outcome.details.join(" ");
    expect(text).toContain("逐项检查（共 1 项）");
    expect(text).toContain("检查「构建」（必需）");
    expect(text).toContain("状态 执行出错");
    expect(text).toContain("退出码 -1");
    expect(text).toContain("结果不可确认，不能当作代码问题");
    expect(text).toContain("stderr 摘要 连接容器超时");
    expect(text).toContain("输出已截断");
  });

  it("结果未持久化时仍保留真实判定，且不展示为通过", () => {
    const outcome = describeValidateResult(
      validationResult({
        passed: false,
        persistence_errors: ["写入失败"],
        persisted: false,
        verdict: "failed",
      }),
    );
    expect(outcome.title).toBe("验证未通过");
    expect(outcome.details.join(" ")).toContain("结果是否可靠写入：否");
    expect(outcome.details.join(" ")).toContain("写入失败");
  });

  it("语义审查要求人工复核时明确说明不能视为通过", () => {
    const outcome = describeValidateResult(
      validationResult({ assessment: { needs_human_review: true, passed: true } }),
    );
    expect(outcome.details.join(" ")).toContain("要求人工复核");
  });

  it("代码初始化只把就绪与持久化同时成立看作成功", () => {
    const base: InitializeCodeResponse = {
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
    expect(describeInitializeResult(base).kind).toBe("success");

    // persisted 只说明记录写入，不等于初始化成功
    const persistedOnly = describeInitializeResult({ ...base, succeeded: false, status: "failed" });
    expect(persistedOnly.kind).toBe("warning");
    expect(persistedOnly.title).toBe("代码初始化未成功");
  });

  it("代码初始化缺失分支与提交时不补造取值", () => {
    const outcome = describeInitializeResult({
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
      task_branch: "",
      task_id: "task-1",
      work_dir: "/workspace/project",
    });
    const text = outcome.details.join(" ");
    expect(text).toContain("任务分支：服务端未提供");
    expect(text).not.toContain("检出 commit");
  });

  it("初始化结果不确定时要求先核验现场", () => {
    const outcome = describeInitializeResult({
      attempt_id: "attempt-1",
      container_id: "container-1",
      finished_at: "2026-10-26T08:04:00Z",
      persisted: false,
      record_id: "record-1",
      source_type: "git",
      started_at: "2026-10-26T08:00:00Z",
      status: "uncertain",
      step_id: "step-1",
      succeeded: false,
      task_branch: "agent/task-1",
      task_id: "task-1",
      work_dir: "/workspace/project",
    });
    expect(outcome.kind).toBe("warning");
    expect(outcome.details.join(" ")).toContain("请先重新读取现场核验容器内实际状态");
  });

  it("超时、网络中断与取消被判定为结果不确定，服务端拒绝不是", () => {
    expect(isUncertainOperationError("timeout")).toBe(true);
    expect(isUncertainOperationError("network")).toBe(true);
    expect(isUncertainOperationError("aborted")).toBe(true);
    expect(isUncertainOperationError("http")).toBe(false);
    expect(OPERATION_UNKNOWN_OUTCOME_HINT).toContain("不会自动重试");
  });
});

describe("错误描述", () => {
  it("网络、超时、取消与业务错误分别描述", () => {
    expect(describeApiError(new ApiError("network", "无法连接 AgentFlow 服务"))).toContain(
      "无法连接 AgentFlow 服务",
    );
    expect(describeApiError(new ApiError("timeout", "请求超时"))).toContain("请求超时");
    expect(describeApiError(new ApiError("aborted", "请求已取消"))).toContain("已取消");
    expect(describeApiError(new ApiError("http", "任务不存在: task-1", { status: 404 }))).toBe(
      "任务不存在: task-1",
    );
  });

  it("只有 404 被判定为不存在", () => {
    expect(isNotFound(new ApiError("http", "不存在", { status: 404 }))).toBe(true);
    expect(isNotFound(new ApiError("http", "冲突", { status: 409 }))).toBe(false);
    expect(isNotFound(new Error("其他"))).toBe(false);
  });
});

/**
 * 结论文本里的状态取值必须与页面上的状态标签同源。
 *
 * 这些 details 直接展示给用户，同一段文字里既出现"步骤状态：已完成"又出现"任务状态：developing"
 * 会让同一个事实有两种写法，用户无法与页面标签对应；未知取值仍必须原样保留。
 */
describe("结论文本里的状态取值", () => {
  /** 一份单步骤执行响应样本 */
  function executeStep(overrides: Partial<ExecuteStepResponse> = {}): ExecuteStepResponse {
    return {
      archive_status: "not_attempted",
      attempt_id: "attempt-1",
      attempt_number: 1,
      attempt_status: "completed",
      exit_code: 0,
      export_status: "not_attempted",
      finalized: true,
      finished_at: "2026-10-26T08:01:00Z",
      outcome: "completed",
      started_at: "2026-10-26T08:00:00Z",
      step_id: "step-1",
      step_status: "developing",
      success: true,
      task_id: "task-1",
      task_status: "developing",
      ...overrides,
    };
  }

  /** 一份顺序执行响应样本 */
  function executeSteps(overrides: Partial<ExecuteStepsResponse> = {}): ExecuteStepsResponse {
    return {
      executed_steps: 1,
      steps: [],
      stopped: false,
      success: true,
      finished_at: "2026-10-26T08:05:00Z",
      started_at: "2026-10-26T08:00:00Z",
      task_id: "task-1",
      task_status: "developing",
      ...overrides,
    };
  }

  /** 一份代码初始化响应样本 */
  function initializeCode(overrides: Partial<InitializeCodeResponse> = {}): InitializeCodeResponse {
    return {
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
      ...overrides,
    };
  }

  it("任务状态、尝试状态与步骤状态使用同一份中文映射", () => {
    const text = describeExecuteStepResult(
      executeStep({ attempt_status: "completed", step_status: "developing" }),
    ).details.join("；");

    expect(text).toContain("尝试：第 1 次（正常结束）");
    expect(text).toContain("步骤状态：开发中");
    expect(text).toContain("任务状态：开发中");
    expect(text).not.toContain("completed");
    expect(text).not.toContain("developing");
  });

  it("顺序执行与计划生成的任务状态同样中文化", () => {
    expect(describeExecuteStepsResult(executeSteps()).details.join("；")).toContain(
      "任务状态：开发中",
    );
    expect(
      describePlanResult({
        persisted: true,
        steps: [],
        task_id: "task-1",
        task_status: "planned",
      }).details.join("；"),
    ).toContain("任务状态：计划已确认");
  });

  it("代码初始化状态使用中文标签", () => {
    expect(describeInitializeResult(initializeCode()).details.join("；")).toContain(
      "初始化状态：已就绪",
    );
  });

  it("未知状态取值原样保留，不被字典兜底掩盖", () => {
    const text = describeExecuteStepResult(
      executeStep({
        attempt_status: "future_attempt_status",
        task_status: "future_task_status" as TaskStatus,
      }),
    ).details.join("；");
    expect(text).toContain("（future_attempt_status）");
    expect(text).toContain("任务状态：future_task_status");

    expect(
      describeInitializeResult(initializeCode({ status: "future_fetch_status" })).details.join(
        "；",
      ),
    ).toContain("初始化状态：future_fetch_status");
  });
});

/**
 * 语义审查是验证结果面板里的一个独立事实：它描述"审查本身发生了什么"，
 * 与验证结论互不替代。取值来自 Go 端 `domain.ReviewStatus`，必须中文化展示，
 * 未知取值原样保留以便诊断。
 */
describe("语义审查结果标签", () => {
  it("覆盖 domain.ReviewStatus 的全部非零取值", () => {
    expect(Object.keys(REVIEW_STATUS_LABEL).sort()).toEqual(
      [
        "call_failed",
        "cancelled",
        "failed",
        "interrupted",
        "invalid_output",
        "needs_human_review",
        "not_configured",
        "passed",
        "timed_out",
        "unparseable",
      ].sort(),
    );
  });

  it("需要人工复核与输出异常都不被表述为审查通过", () => {
    expect(reviewStatusLabel("needs_human_review")).toBe("需要人工复核");
    expect(reviewStatusLabel("unparseable")).toBe("输出无法解析");
    expect(reviewStatusLabel("invalid_output")).toBe("输出未通过校验");
    expect(reviewStatusLabel("call_failed")).toBe("调用失败");
    // 只有 `passed` 才能读成"审查通过"：其余取值都不得与它同义
    for (const status of ["needs_human_review", "unparseable", "invalid_output", "call_failed"]) {
      expect(reviewStatusLabel(status)).not.toBe(reviewStatusLabel("passed"));
      expect(reviewStatusLabel(status)).not.toBe("审查通过");
    }
  });

  it("零值显示占位符，未知取值原样保留", () => {
    expect(reviewStatusLabel()).toBe("—");
    expect(reviewStatusLabel("")).toBe("—");
    expect(reviewStatusLabel("future_review_status")).toBe("future_review_status");
  });
});
