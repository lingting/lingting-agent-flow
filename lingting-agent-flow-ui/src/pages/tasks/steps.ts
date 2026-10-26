/**
 * 步骤、尝试与验证的展示元数据，以及执行操作的可用性判定。
 *
 * 取值全部来自 Go 端领域类型，这里只补充中文标签、颜色与"是否可点击"的提示。
 * 前端判定只是用户体验保护：真正的准入由服务端状态机、任务租约与容器核验决定，
 * 因此任何一处判定都不代表"服务端一定会接受"，也不代表"服务端拒绝就是 bug"。
 */
import type { DictItem } from "lingting-react-ui";

import type {
  Attempt,
  AttemptStatus,
  ExecuteAndValidateStepResponse,
  ExecuteStepResponse,
  ExecuteStepsResponse,
  InitializeCodeResponse,
  OpenRepairCycleResponse,
  PlanResponse,
  Step,
  StepExecutionOutcome,
  StepStatus,
  Task,
  ValidateAttemptResponse,
  VerificationVerdict,
} from "@/lib/api";

/** 一次操作的展示结论 */
export type OperationOutcome = {
  /** 标题：说明"哪个动作、什么结果" */
  title: string;
  /** `success` 只用于服务端明确成功的情况 */
  kind: "success" | "warning" | "error";
  /** 逐条事实说明，全部来自服务端响应字段 */
  details: string[];
};

/** 步骤状态展示信息 */
export type StepStatusMeta = {
  label: string;
  color: string;
  /** 是否表示步骤正被执行占用 */
  active: boolean;
};

/** 全部步骤状态，覆盖 Go 端 `domain.StepStatus` 的每一个取值 */
export const STEP_STATUS_META: Record<StepStatus, StepStatusMeta> = {
  pending: { active: false, color: "default", label: "待执行" },
  running: { active: true, color: "processing", label: "执行中" },
  developing: { active: true, color: "processing", label: "开发中" },
  verifying: { active: true, color: "processing", label: "验证中" },
  completed: { active: false, color: "success", label: "已完成" },
  failed: { active: false, color: "error", label: "失败" },
  retry_wait: { active: false, color: "warning", label: "等待重试" },
  skipped: { active: false, color: "default", label: "已跳过" },
  cancelled: { active: false, color: "default", label: "已取消" },
  recovery_required: { active: false, color: "warning", label: "待核验" },
};

/**
 * 判断步骤状态是否属于已知取值。
 *
 * 未知取值必须原样展示：`DictTag` 遇到字典里没有的值会渲染为空，从而把
 * "服务端新增了一个状态"变成"什么都没有"，这比展示原始值更容易误导。
 */
export function isKnownStepStatus(status: string): status is StepStatus {
  return Object.hasOwn(STEP_STATUS_META, status);
}

/** 步骤状态字典，供 DictTag / DictSelect 使用 */
export const STEP_STATUS_DICT: DictItem<string>[] = Object.entries(STEP_STATUS_META).map(
  ([value, meta]) => ({ label: meta.label, tagColor: meta.color, value }),
);

/** 读取步骤状态元数据；未知状态原样展示，不猜测语义 */
export function stepStatusMeta(status: string): StepStatusMeta {
  return (
    STEP_STATUS_META[status as StepStatus] ?? { active: false, color: "default", label: status }
  );
}

/** 步骤类型中文标签，取值来自 Go 端 `domain.StepType` */
export const STEP_TYPE_LABEL: Record<string, string> = {
  prepare: "准备环境",
  source: "获取代码",
  plan: "规划",
  develop: "开发",
  verify: "确定性验证",
  review: "语义审查",
  save: "保存代码",
  cleanup: "清理",
};

/** 读取步骤类型标签；未知类型原样展示 */
export function stepTypeLabel(type: string): string {
  return STEP_TYPE_LABEL[type] ?? type;
}

/**
 * 需要 Agent 调用才能执行的步骤类型，与 `domain.StepType.AgentRole()` 一致。
 *
 * 其他类型（prepare / source / verify / save / cleanup）由 Go 直接执行，服务端会
 * 明确拒绝通过执行接口启动它们，因此这里也不显示执行入口。
 */
const AGENT_STEP_TYPES = new Set(["plan", "develop", "review"]);

/** 尝试状态展示信息 */
export type AttemptStatusMeta = { label: string; color: string };

/** 全部尝试状态，覆盖 Go 端 `domain.AttemptStatus` 的每一个取值 */
export const ATTEMPT_STATUS_META: Record<AttemptStatus, AttemptStatusMeta> = {
  running: { color: "processing", label: "执行中" },
  completed: { color: "success", label: "正常结束" },
  failed: { color: "error", label: "失败" },
  cancelled: { color: "default", label: "已取消" },
  interrupted: { color: "warning", label: "已中断" },
  timed_out: { color: "warning", label: "已超时" },
  unknown: { color: "warning", label: "结果未知" },
  recovery_required: { color: "warning", label: "待核验" },
};

/** 读取尝试状态元数据；未知状态原样展示 */
export function attemptStatusMeta(status: string): AttemptStatusMeta {
  return ATTEMPT_STATUS_META[status as AttemptStatus] ?? { color: "default", label: status };
}

/** 单步骤执行结论的中文标签，取值来自 Go 端 `domain.StepExecutionOutcome` */
export const EXECUTION_OUTCOME_LABEL: Record<StepExecutionOutcome, string> = {
  completed: "正常结束",
  failed: "失败",
  timed_out: "已超时",
  cancelled: "已取消",
  interrupted: "已中断",
};

/** 读取执行结论标签；未知取值原样展示 */
export function executionOutcomeLabel(outcome?: string): string {
  if (!outcome) return "—";
  return EXECUTION_OUTCOME_LABEL[outcome as StepExecutionOutcome] ?? outcome;
}

/** 验证流程状态标签，取值来自 Go 端 `domain.ValidationRunStatus` */
export const VALIDATION_STATUS_LABEL: Record<string, string> = {
  pending: "待执行",
  running: "执行中",
  completed: "已完成",
  failed: "流程失败",
  cancelled: "已取消",
  timed_out: "已超时",
};

/** 读取验证流程状态标签；未知取值原样展示 */
export function validationStatusLabel(status?: string): string {
  if (!status) return "—";
  return VALIDATION_STATUS_LABEL[status] ?? status;
}

/**
 * 步骤级验证状态标签，取值来自服务端派生的 `ValidationHistoryResponse.state`。
 *
 * `unverified` 与 `verifying` 都不是通过；`infrastructure_error` 表示没拿到可用证据。
 */
export const VALIDATION_STATE_LABEL: Record<string, string> = {
  unverified: "尚未验证",
  verifying: "验证中",
  passed: "验证通过",
  failed: "验证未通过",
  cancelled: "验证已取消",
  infrastructure_error: "验证未取得证据",
};

/** 读取验证状态标签；未知取值原样展示 */
export function validationStateLabel(state?: string): string {
  if (!state) return "尚未验证";
  return VALIDATION_STATE_LABEL[state] ?? state;
}

/** 验证结论展示信息 */
export type VerdictMeta = { label: string; color: string };

/** 全部验证结论，覆盖 Go 端 `domain.VerificationVerdict` */
export const VERDICT_META: Record<VerificationVerdict, VerdictMeta> = {
  passed: { color: "success", label: "通过" },
  failed: { color: "error", label: "未通过" },
  inconclusive: { color: "warning", label: "无法判定" },
};

/** 读取验证结论元数据；未知取值原样展示，不猜测为通过 */
export function verdictMeta(verdict?: string): VerdictMeta {
  if (!verdict) return { color: "default", label: "—" };
  return VERDICT_META[verdict as VerificationVerdict] ?? { color: "default", label: verdict };
}

/** 单项检查状态标签 */
export const CHECK_STATUS_LABEL: Record<string, string> = {
  passed: "通过",
  failed: "未通过",
  timed_out: "已超时",
  cancelled: "已取消",
  skipped: "已跳过",
  error: "执行出错",
};

/** 读取单项检查状态标签；未知取值原样展示 */
export function checkStatusLabel(status?: string): string {
  if (!status) return "—";
  return CHECK_STATUS_LABEL[status] ?? status;
}

/** 修复循环下一步动作标签，取值来自服务端 `repair_decision` */
export const REPAIR_DECISION_LABEL: Record<string, string> = {
  retry: "继续修复",
  completed: "已收口",
  stop: "已停止",
};

/** 读取修复动作标签；未知取值原样展示 */
export function repairDecisionLabel(decision?: string): string {
  if (!decision) return "—";
  return REPAIR_DECISION_LABEL[decision] ?? decision;
}

/**
 * 任务状态是否允许推进步骤。
 *
 * 与服务端 `domain.TaskStatus.AllowsStepExecution` 一致：`preparing`、`planned`、
 * `developing`、`retrying`、`paused`、`recovery_required`。`queued` 必须先完成环境
 * 准备与计划，`cancelling` 与已结束状态一律不允许。
 */
export function taskAllowsStepExecution(task: Task): boolean {
  if (task.cancel_requested) return false;
  switch (task.status) {
    case "preparing":
    case "planned":
    case "developing":
    case "retrying":
    case "paused":
    case "recovery_required":
      return true;
    default:
      return false;
  }
}

/**
 * 任务状态是否允许生成计划。
 *
 * 与服务端 `domain.TaskStatus.CanStartPlanning` 派生的显式规划集合一致：
 * `queued`、`preparing`、`planned`、`paused`、`recovery_required`（不含 `failed`）。
 * 服务端还会额外要求任务已有受管理容器、已配置规划角色且没有任何步骤，这些条件
 * 前端拿不到，因此这里只做状态层提示。
 */
export function taskAllowsPlanning(task: Task): boolean {
  if (task.cancel_requested) return false;
  switch (task.status) {
    case "queued":
    case "preparing":
    case "planned":
    case "paused":
    case "recovery_required":
      return true;
    default:
      return false;
  }
}

/** 步骤状态是否允许开始一次新的执行，与服务端 `StepStatus.CanStartExecution` 一致 */
export function stepAllowsExecution(step: Step): boolean {
  switch (step.status) {
    case "pending":
    case "retry_wait":
    case "failed":
    case "recovery_required":
      return true;
    default:
      return false;
  }
}

/** 步骤是否可被单步骤执行入口启动 */
export function canExecuteStep(task: Task, step: Step): boolean {
  if (!taskAllowsStepExecution(task)) return false;
  if (!stepAllowsExecution(step)) return false;
  if (step.current_attempt_id) return false;
  return AGENT_STEP_TYPES.has(step.step_type);
}

/**
 * 步骤是否可发起开发—验证闭环。
 *
 * 与服务端 `ExecuteAndValidate` 的准入一致：它复用 `CheckStepExecutionStart`，
 * 但还会在开发之前校验验证配置与修复预算，这两项前端不可见，因此拒绝原因必须
 * 原样展示给用户。
 */
export function canExecuteAndValidate(task: Task, step: Step): boolean {
  return canExecuteStep(task, step);
}

/**
 * 步骤是否可显式开启新的修复周期。
 *
 * 服务端只在上一周期已收敛、且预算用尽或策略不再允许修复时才允许开启；前端无法
 * 判断预算，因此这里只排除"明确不可能成功"的现场（任务不允许推进、步骤被占用）。
 */
export function canOpenRepairCycle(task: Task, step?: Step): boolean {
  if (!step) return false;
  if (!taskAllowsStepExecution(task)) return false;
  return !step.current_attempt_id;
}

/** 是否可以顺序执行：任务允许推进步骤，且存在未结束的步骤 */
export function canRunSteps(task: Task, steps: Step[]): boolean {
  if (!taskAllowsStepExecution(task)) return false;
  return steps.some((step) => step.status !== "completed" && step.status !== "skipped");
}

/**
 * 是否可以生成计划：任务允许规划，且当前没有任何步骤。
 *
 * 服务端拒绝覆盖已有计划（计划是执行依据），因此有步骤时不再显示该入口。
 */
export function canGeneratePlan(task: Task, steps: Step[]): boolean {
  if (!taskAllowsPlanning(task)) return false;
  return steps.length === 0;
}

/** 尝试状态是否允许验证，与服务端 `domain.CanValidateAttempt` 一致 */
export function attemptAllowsValidation(attempt: Attempt): boolean {
  switch (attempt.status) {
    case "completed":
    case "failed":
    case "cancelled":
    case "timed_out":
    case "interrupted":
      return true;
    default:
      return false;
  }
}

/**
 * 任务状态是否允许验证，与服务端 `domain.ValidationTaskStatus` 一致。
 *
 * `queued` 尚未准备环境，`cancelling` 与已结束状态一律拒绝。
 */
export function taskAllowsValidation(task: Task): boolean {
  if (task.cancel_requested) return false;
  switch (task.status) {
    case "developing":
    case "verifying":
    case "retrying":
    case "paused":
    case "recovery_required":
      return true;
    default:
      return false;
  }
}

/** 尝试是否可发起确定性验证 */
export function canValidateAttempt(task: Task, attempt?: Attempt): boolean {
  if (!attempt) return false;
  if (!taskAllowsValidation(task)) return false;
  return attemptAllowsValidation(attempt);
}

/**
 * 尝试是否可初始化代码。
 *
 * 与服务端 `checkCodeInitAllowed` 一致：任务处于可初始化集合，尝试处于
 * `running` / `failed` / `interrupted` / `recovery_required`。服务端还会校验容器
 * 归属与工作区就绪状态。
 */
export function canInitializeAttempt(task: Task, attempt?: Attempt): boolean {
  if (!attempt) return false;
  if (task.cancel_requested) return false;
  switch (task.status) {
    case "queued":
    case "preparing":
    case "planning":
    case "planned":
    case "developing":
    case "retrying":
    case "paused":
    case "recovery_required":
      break;
    default:
      return false;
  }
  switch (attempt.status) {
    case "running":
    case "failed":
    case "interrupted":
    case "recovery_required":
      return true;
    default:
      return false;
  }
}

/** 把计划生成响应翻译为用户可读结论：只有 `persisted` 才代表计划已落库 */
export function describePlanResult(result: PlanResponse): OperationOutcome {
  const details = [
    `任务状态：${result.task_status}`,
    `计划：${result.persisted ? `已持久化 ${result.steps.length} 个步骤` : "未持久化"}`,
  ];
  if (result.summary) details.push(`摘要：${result.summary}`);
  if (result.failure_stage) details.push(`失败阶段：${result.failure_stage}`);
  if (result.failure) details.push(`失败原因：${result.failure}`);
  return {
    details,
    kind: result.persisted ? "success" : "warning",
    title: result.persisted ? "计划已生成并持久化" : "计划未持久化",
  };
}

/** 把单步骤执行响应翻译为用户可读结论：`success` 不表示任务成功 */
export function describeExecuteStepResult(result: ExecuteStepResponse): OperationOutcome {
  const details = [
    `执行结论：${executionOutcomeLabel(result.outcome)}`,
    `尝试：第 ${result.attempt_number} 次（${result.attempt_status}）`,
    `步骤状态：${stepStatusMeta(result.step_status).label}`,
    `任务状态：${result.task_status}`,
  ];
  if (result.summary) details.push(`摘要：${result.summary}`);
  if (result.error) details.push(`原因：${result.error}`);
  if (!result.finalized) {
    details.push(
      `最终状态未完整写入：${(result.finalization_errors ?? []).join("；") || "服务端未给出原因"}`,
    );
  }
  details.push("本次调用正常结束不代表任务已通过验收，仍需验证与代码保存。");
  return {
    details,
    kind: result.success ? "success" : "warning",
    title: result.success ? "步骤执行已结束" : "步骤执行未成功",
  };
}

/** 把顺序执行响应翻译为用户可读结论：`stopped` 与 `success` 分别表达 */
export function describeExecuteStepsResult(result: ExecuteStepsResponse): OperationOutcome {
  const details = [`已执行步骤：${result.executed_steps} 个`, `任务状态：${result.task_status}`];
  for (const step of result.steps) {
    const summary = step.skipped
      ? `跳过（${step.skip_reason ?? "未执行"}）`
      : `${executionOutcomeLabel(step.outcome)}，步骤状态 ${stepStatusMeta(step.step_status ?? "").label}`;
    details.push(`第 ${step.sequence} 步「${step.title}」：${summary}`);
  }
  if (result.stopped) {
    details.push(`已提前停止：${result.stop_reason ?? "服务端未给出原因"}`);
  }
  details.push("顺序执行不包含验证、提交、推送与合并。");
  return {
    details,
    kind: result.success ? "success" : result.stopped ? "warning" : "warning",
    title: result.success ? "顺序执行已完成" : "顺序执行已停止",
  };
}

/** 把开发—验证闭环响应翻译为用户可读结论：只有 `verified` 才算通过 */
export function describeExecuteAndValidateResult(
  result: ExecuteAndValidateStepResponse,
): OperationOutcome {
  const details = [
    `开发结论：${executionOutcomeLabel(result.outcome)}`,
    `验证是否发起：${result.validation_ran ? "是" : `否（${result.validation_skip_reason ?? "未给出原因"}）`}`,
  ];
  if (result.validation_ran) {
    details.push(`验证流程状态：${validationStatusLabel(result.validation_status)}`);
    details.push(`验证结论：${verdictMeta(result.validation_verdict).label}`);
    details.push(
      `检查统计：通过 ${result.checks?.filter((check) => check.status === "passed").length ?? 0} 项`,
    );
  }
  if (result.validation_failure_summary) {
    details.push(`验证失败摘要：${result.validation_failure_summary}`);
  }
  if (result.infrastructure_error) {
    details.push("验证因基础设施异常没有拿到可用证据，不能当作代码验证失败。");
  }
  if (!result.validation_persisted && result.validation_ran) {
    details.push(
      `验证结果未可靠写入：${(result.validation_persistence_errors ?? []).join("；") || "服务端未给出原因"}`,
    );
  }
  details.push(`修复动作：${repairDecisionLabel(result.repair_decision)}`);
  if (result.repair_class) details.push(`停止类别：${result.repair_class}`);
  if (result.repair_summary) details.push(`说明：${result.repair_summary}`);
  details.push(
    `轮次：第 ${result.round} 轮，已消耗 ${result.attempts} / ${result.max_attempts} 次尝试`,
  );
  details.push(`步骤状态：${stepStatusMeta(result.step_status).label}`);
  return {
    details,
    kind: result.verified ? "success" : "warning",
    title: result.verified ? "步骤已通过验证" : "步骤未通过验证",
  };
}

/** 把验证响应翻译为用户可读结论：流程状态、结论与持久化分别说明 */
export function describeValidateResult(result: ValidateAttemptResponse): OperationOutcome {
  const details = [
    `验证流程状态：${validationStatusLabel(result.status)}`,
    `验证结论：${verdictMeta(result.verdict).label}`,
    `检查统计：通过 ${result.passed_checks} 项、未通过 ${result.failed_checks} 项、跳过 ${result.skipped_checks} 项`,
    `结果是否可靠写入：${result.persisted ? "是" : "否"}`,
  ];
  if (result.summary) details.push(`摘要：${result.summary}`);
  if (result.failure_summary) details.push(`失败摘要：${result.failure_summary}`);
  if (!result.persisted) {
    details.push(
      `写入失败原因：${(result.persistence_errors ?? []).join("；") || "服务端未给出原因"}`,
    );
  }
  return {
    details,
    kind: result.passed ? "success" : "warning",
    title: result.passed ? "验证通过" : "验证未通过",
  };
}

/** 把代码初始化响应翻译为用户可读结论：`succeeded` 与 `persisted` 分别说明 */
export function describeInitializeResult(result: InitializeCodeResponse): OperationOutcome {
  const details = [
    `初始化状态：${result.status}`,
    `代码是否就绪：${result.succeeded ? "是" : "否"}`,
    `工作目录：${result.work_dir}`,
    `任务分支：${result.task_branch}`,
  ];
  if (result.commit) details.push(`检出 commit：${result.commit}`);
  if (result.stage) details.push(`阶段：${result.stage}`);
  if (result.error_code) details.push(`错误类别：${result.error_code}`);
  if (result.error) details.push(`失败原因：${result.error}`);
  if (!result.persisted) {
    details.push(
      `记录未完整写入：${(result.persistence_errors ?? []).join("；") || "服务端未给出原因"}`,
    );
  }
  return {
    details,
    kind: result.succeeded && result.persisted ? "success" : "warning",
    title: result.succeeded ? "代码已初始化" : "代码初始化未成功",
  };
}

/** 把修复周期响应翻译为用户可读结论：只说明预算登记事实，不表示执行成功 */
export function describeOpenRepairCycleResult(result: OpenRepairCycleResponse): OperationOutcome {
  return {
    details: [
      `新周期序号：第 ${result.cycle_sequence} 个`,
      `尝试次数上限：${result.max_attempts} 次`,
      `上一周期已消耗：${result.previous_consumed} 次`,
      "该操作只登记新的预算作用域，不会自动执行；随后仍需显式发起执行。",
    ],
    kind: "success",
    title: "已开启新的执行周期",
  };
}
