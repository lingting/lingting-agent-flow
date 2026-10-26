import { describe, expect, it } from "vitest";

import type {
  Attempt,
  ExecuteAndValidateStepResponse,
  ExecuteStepResponse,
  ExecuteStepsResponse,
  InitializeCodeResponse,
  OpenRepairCycleResponse,
  PlanResponse,
  Step,
  StepStatus,
  Task,
  TaskStatus,
  ValidateAttemptResponse,
} from "../../src/lib/api";
import {
  ATTEMPT_STATUS_META,
  attemptStatusMeta,
  canExecuteAndValidate,
  canExecuteStep,
  canGeneratePlan,
  canInitializeAttempt,
  canOpenRepairCycle,
  canRunSteps,
  canValidateAttempt,
  describeExecuteAndValidateResult,
  describeExecuteStepResult,
  describeExecuteStepsResult,
  describeInitializeResult,
  describeOpenRepairCycleResult,
  describePlanResult,
  describeValidateResult,
  executionOutcomeLabel,
  STEP_STATUS_META,
  stepStatusMeta,
  stepTypeLabel,
  taskAllowsPlanning,
  taskAllowsStepExecution,
  taskAllowsValidation,
  validationStateLabel,
  verdictMeta,
} from "../../src/pages/tasks/steps";
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

  it("开启修复周期只排除明确不可能的现场", () => {
    expect(canOpenRepairCycle(task(), step())).toBe(true);
    expect(canOpenRepairCycle(task(), step({ current_attempt_id: "attempt-1" }))).toBe(false);
    expect(canOpenRepairCycle(task({ status: "queued" }), step())).toBe(false);
    expect(canOpenRepairCycle(task(), undefined)).toBe(false);
  });

  it("顺序执行要求存在未结束的步骤", () => {
    expect(canRunSteps(task(), [step()])).toBe(true);
    expect(canRunSteps(task(), [step({ status: "completed" })])).toBe(false);
    expect(canRunSteps(task(), [step({ status: "skipped" })])).toBe(false);
    expect(canRunSteps(task({ status: "queued" }), [step()])).toBe(false);
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
