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
  ValidationCheckResult,
  VerificationVerdict,
} from "@/lib/api";
import { taskStatusLabel } from "@/pages/tasks/status";

/** 一次操作的展示结论 */
export type OperationOutcome = {
  /** 标题：说明"哪个动作、什么结果" */
  title: string;
  /** `success` 只用于服务端明确成功的情况 */
  kind: "success" | "warning" | "error";
  /** 逐条事实说明，全部来自服务端响应字段 */
  details: string[];
};

/**
 * 一次显式操作的准入结论：是否可用，以及不可用时面向用户的原因。
 *
 * `reason` 只允许描述调用方**确实检查过**的条件，不能把服务端的未知判断写成前端结论；
 * 允许操作时 `reason` 为空字符串，界面据此不展示任何禁用说明。
 */
export type ActionEligibility = {
  allowed: boolean;
  reason: string;
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

/**
 * 尝试级别的独立事实标记：超时、取消、错误分类与失败阶段。
 *
 * 这四个字段各自表达一件真实的事，前端只负责如实展示：
 *   - `timed_out` / `cancelled` 是尝试记录上的独立布尔事实，不由 `status` 推断；
 *   - `error_class` / `failure_stage` 的取值来自服务端，未知取值原样保留。
 *
 * 缺省一律返回 `undefined`，调用方据此不渲染该行，绝不补出"未发生"或"成功"。
 */
export type AttemptFailureFacts = {
  /** 已超时标记；仅在服务端 `timed_out=true` 时存在 */
  timedOut?: string;
  /** 已取消标记；仅在服务端 `cancelled=true` 时存在 */
  cancelled?: string;
  /** 错误分类；缺省时不出现 */
  errorClass?: string;
  /** 失败阶段标签；缺省时不出现 */
  failureStage?: string;
  /** `error_class` 为前端已知取值，可附加中文说明 */
  errorClassKnown: boolean;
  /** `failure_stage` 为前端已知取值，可附加中文说明 */
  failureStageKnown: boolean;
};

/**
 * 错误分类的中文说明，取值来自 Go 端 `domain.RetryErrorClass` 与恢复流程实际写入的分类。
 *
 * 只收录已在服务端代码中确认会写入 `Attempt.ErrorClass` 或同一分类枚举的取值；
 * 未收录的取值按原始文本展示，不根据错误文本猜测分类。
 */
export const ATTEMPT_ERROR_CLASS_LABEL: Record<string, string> = {
  agent_failure: "Agent 调用失败",
  timeout: "执行超时",
  verification_failure: "验证未通过",
  network: "网络类失败",
};

/** 失败阶段的中文说明，取值来自服务端实际写入 `Attempt.FailureStage` 的阶段名 */
export const ATTEMPT_FAILURE_STAGE_LABEL: Record<string, string> = {
  execute: "执行阶段",
  verify: "验证阶段",
  recover: "服务恢复阶段",
};

/** 超时与取消的固定标记文案；两者不互相替代 */
export const ATTEMPT_TIMED_OUT_LABEL = "已超时";
export const ATTEMPT_CANCELLED_LABEL = "已取消";

/** 读取错误分类说明；未知取值没有说明，返回 undefined 而不是猜测 */
export function attemptErrorClassLabel(errorClass?: string): string | undefined {
  if (!errorClass) return undefined;
  return ATTEMPT_ERROR_CLASS_LABEL[errorClass];
}

/** 读取失败阶段标签；未知取值原样返回，缺省时返回 undefined */
export function attemptFailureStageLabel(stage?: string): string | undefined {
  if (!stage) return undefined;
  return ATTEMPT_FAILURE_STAGE_LABEL[stage] ?? stage;
}

/**
 * 汇总一次尝试的失败事实。
 *
 * 只读取接口返回的真实字段：布尔值为 `true` 时才产生标记，`false` 不产生任何警告；
 * `error_class` / `failure_stage` 缺省时不出现在结果里，也就不会被页面渲染。
 */
export function attemptFailureFacts(attempt: Attempt): AttemptFailureFacts {
  const errorClass = attempt.error_class?.trim();
  const failureStage = attempt.failure_stage?.trim();
  const facts: AttemptFailureFacts = {
    errorClassKnown: false,
    failureStageKnown: false,
  };
  if (attempt.timed_out) facts.timedOut = ATTEMPT_TIMED_OUT_LABEL;
  if (attempt.cancelled) facts.cancelled = ATTEMPT_CANCELLED_LABEL;
  if (errorClass) {
    facts.errorClass = ATTEMPT_ERROR_CLASS_LABEL[errorClass] ?? errorClass;
    facts.errorClassKnown = Object.hasOwn(ATTEMPT_ERROR_CLASS_LABEL, errorClass);
  }
  if (failureStage) {
    facts.failureStage = ATTEMPT_FAILURE_STAGE_LABEL[failureStage] ?? failureStage;
    facts.failureStageKnown = Object.hasOwn(ATTEMPT_FAILURE_STAGE_LABEL, failureStage);
  }
  return facts;
}

/**
 * 会话归档状态标签，取值来自 Go 端 `domain.SessionArchiveStatus`。
 *
 * 归档指把容器内的原始会话文件复制到 AgentFlow 本机持久目录，它与会话执行状态、
 * HTML 导出状态是三个独立事实，因此三张映射表互不复用。
 */
export const SESSION_ARCHIVE_STATUS_LABEL: Record<string, string> = {
  not_attempted: "未归档",
  succeeded: "已归档",
  failed: "归档失败",
};

/** 会话 HTML 导出状态标签，取值来自 Go 端 `domain.SessionExportStatus` */
export const SESSION_EXPORT_STATUS_LABEL: Record<string, string> = {
  not_attempted: "未导出",
  succeeded: "已导出",
  failed: "导出失败",
};

/** 状态缺省时的中性占位符：既不表示成功，也不表示失败 */
export const SESSION_STATUS_MISSING_LABEL = "未提供";

/**
 * 会话执行状态标签，取值来自 Go 端 `domain.SessionStatus`。
 *
 * 它只表达"这次 Agent 调用如何结束"，不代表步骤验收通过，也不代表归档或导出成功。
 */
export const SESSION_STATUS_LABEL: Record<string, string> = {
  starting: "启动中",
  running: "执行中",
  completed: "正常结束",
  failed: "失败",
  cancelled: "已取消",
  interrupted: "已中断",
};

/** 会话运行结果标签，取值来自 Go 端 `domain.SessionRunStatus` */
export const SESSION_RUN_STATUS_LABEL: Record<string, string> = {
  completed: "运行正常结束",
  failed: "运行失败",
  cancelled: "运行已取消",
  timed_out: "运行已超时",
};

/**
 * 三个读取函数共用同一条边界规则，因此逐个显式写出，不做额外抽象：
 * 缺省 → 中性占位符；已知 → 中文标签；未知 → 服务端原始值。
 */
/** 读取归档状态标签；未知取值原样展示，不因字典缺项隐藏真实取值 */
export function sessionArchiveStatusLabel(status?: string): string {
  if (!status) return SESSION_STATUS_MISSING_LABEL;
  return SESSION_ARCHIVE_STATUS_LABEL[status] ?? status;
}

/** 读取导出状态标签；与归档状态各自独立，不由归档结果推导 */
export function sessionExportStatusLabel(status?: string): string {
  if (!status) return SESSION_STATUS_MISSING_LABEL;
  return SESSION_EXPORT_STATUS_LABEL[status] ?? status;
}

/** 读取会话执行状态标签；未知取值原样展示 */
export function sessionStatusLabel(status?: string): string {
  if (!status) return SESSION_STATUS_MISSING_LABEL;
  return SESSION_STATUS_LABEL[status] ?? status;
}

/** 读取会话运行结果标签；缺省返回 undefined，由调用方决定不渲染 */
export function sessionRunStatusLabel(runStatus?: string): string | undefined {
  if (!runStatus) return undefined;
  return SESSION_RUN_STATUS_LABEL[runStatus] ?? runStatus;
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

/**
 * 代码初始化状态标签，取值来自 Go 端 `domain.CodeFetchStatus`。
 *
 * 它只表达"这一次代码初始化如何结束"，不代表记录已可靠写入（见 `persisted`），也不代表
 * 步骤验收通过；与工作区初始化状态是不同的事实，因此单独一张映射表。
 */
export const CODE_FETCH_STATUS_LABEL: Record<string, string> = {
  pending: "待执行",
  running: "执行中",
  succeeded: "已就绪",
  failed: "失败",
  cancelled: "已取消",
  timed_out: "已超时",
  skipped: "已就绪（未改动）",
  uncertain: "结果不确定",
};

/**
 * 语义审查结果标签，取值来自 Go 端 `domain.ReviewStatus`
 * （`internal/domain/validation_decision.go`）。
 *
 * 它描述"语义审查本身发生了什么"，与验证结论（`verdict`）是两件独立的事：
 * `needs_human_review` 不等于通过，`unparseable` / `invalid_output` / `call_failed`
 * 也不等于代码有问题。零值表示本次没有要求语义审查。
 */
export const REVIEW_STATUS_LABEL: Record<string, string> = {
  passed: "审查通过",
  failed: "审查未通过",
  needs_human_review: "需要人工复核",
  unparseable: "输出无法解析",
  invalid_output: "输出未通过校验",
  call_failed: "调用失败",
  timed_out: "已超时",
  cancelled: "已取消",
  interrupted: "已中断",
  not_configured: "未配置验证角色",
};

/** 读取语义审查结果标签；缺省显示占位符，未知取值原样展示 */
export function reviewStatusLabel(status?: string): string {
  if (!status) return "—";
  return REVIEW_STATUS_LABEL[status] ?? status;
}

/** 读取代码初始化状态标签；未知取值原样展示 */
export function codeFetchStatusLabel(status?: string): string {
  if (!status) return "—";
  return CODE_FETCH_STATUS_LABEL[status] ?? status;
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

/**
 * 读取验证判定标签。
 *
 * 缺省说明服务端没有提供判定（不能读作通过，也不能读作未通过）；未知取值原样展示。
 */
export function validationVerdictLabel(verdict?: string): string {
  if (!verdict) return "服务端未提供";
  return VERDICT_META[verdict as VerificationVerdict]?.label ?? verdict;
}

/**
 * 验证判定到结论标题与语义类型的映射。
 *
 * 判定是结论的唯一来源：`passed` 只是便捷布尔值，`inconclusive`、未知取值与缺省
 * 一律是"无法判定"，既不是通过也不是"验证未通过"。
 */
function verdictOutcome(verdict?: string): Pick<OperationOutcome, "kind" | "title"> {
  if (verdict === "passed") return { kind: "success", title: "验证通过" };
  if (verdict === "failed") return { kind: "warning", title: "验证未通过" };
  return { kind: "warning", title: "验证结论无法判定" };
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
/**
 * 单条检查结果的一行事实说明。
 *
 * 状态、退出码、超时、取消、结果是否可信与输出摘要是各自独立的事实：任一项都不能由
 * 其他项推导，`certain=false` 更不能被当作代码问题。
 */
function describeCheckFact(check: ValidationCheckResult): string {
  const name = check.check_name || check.check_id;
  const facts = [
    `检查「${name}」（${check.required ? "必需" : "可选"}）`,
    `状态 ${checkStatusLabel(check.status)}`,
    `退出码 ${check.exit_code}`,
  ];
  if (check.timed_out) facts.push("已超时");
  if (check.cancelled) facts.push("已取消");
  if (check.certain === false) facts.push("结果不可确认，不能当作代码问题");
  if (check.failure_reason) facts.push(`失败原因 ${check.failure_reason}`);
  if (check.stderr_summary) facts.push(`stderr 摘要 ${check.stderr_summary}`);
  if (check.output_truncated) facts.push("输出已截断");
  return facts.join("，");
}

/**
 * 有副作用的操作结果无法确认时的固定提示。
 *
 * 客户端超时、网络中断与调用方取消都意味着"服务端可能已经执行了这次有副作用的操作"，
 * 因此必须与明确的服务端拒绝区分，并要求先核验现场、不自动重发。
 */
export const OPERATION_UNKNOWN_OUTCOME_HINT =
  "无法确认服务端是否已完成本次操作：连接中断、超时或被取消，请求可能已经生效，也可能没有。" +
  "页面不会自动重试，请先重新读取现场核验真实状态，再由你决定是否重新发起。";

/** 判断错误类别是否属于"结果无法确认"（超时、网络中断或调用方取消） */
export function isUncertainOperationError(kind: string): boolean {
  return kind === "timeout" || kind === "network" || kind === "aborted";
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

/** 允许执行：可用且没有禁用原因 */
const ACTION_ALLOWED: ActionEligibility = { allowed: true, reason: "" };

/** 构造"不可执行"的结论 */
function blockedAction(reason: string): ActionEligibility {
  return { allowed: false, reason };
}

/**
 * 任务状态层的步骤推进准入结论与原因。
 *
 * 只表达 `taskAllowsStepExecution` 已检查的条件：取消请求与任务状态。服务端的容器、
 * 执行节点预检与步骤归属校验前端拿不到，不在这里编造。
 */
function describeStepTaskGate(task: Task): ActionEligibility {
  if (taskAllowsStepExecution(task)) return ACTION_ALLOWED;
  if (task.cancel_requested) {
    return blockedAction("任务已请求取消：取消请求尚未结束，此时不能推进任何步骤。");
  }
  return blockedAction(
    `当前任务状态（${taskStatusLabel(task.status)}）不允许推进步骤：` +
      "新任务需要先完成环境准备与计划，取消中与已结束的任务不能再执行步骤。",
  );
}

/**
 * 单步骤执行的准入结论与原因。
 *
 * 判定条件与原来的 `canExecuteStep` 逐条一致，顺序也一致：任务状态 → 步骤状态 →
 * 步骤占用 → 步骤类型。原因只描述这四项前端确实检查过的事实。
 */
export function describeStepExecution(task: Task, step: Step): ActionEligibility {
  const taskGate = describeStepTaskGate(task);
  if (!taskGate.allowed) return taskGate;
  if (!stepAllowsExecution(step)) {
    return blockedAction(
      `步骤状态为「${stepStatusMeta(step.status).label}」，` +
        "只有待执行、等待重试、失败或待核验的步骤可以开始新的执行。",
    );
  }
  if (step.current_attempt_id) {
    return blockedAction("该步骤仍被当前执行尝试占用，需要等这次尝试结束后才能重新执行。");
  }
  if (!AGENT_STEP_TYPES.has(step.step_type)) {
    return blockedAction(
      `步骤类型为「${stepTypeLabel(step.step_type)}」，由服务端直接执行，` +
        "不通过单步骤执行入口发起。",
    );
  }
  return ACTION_ALLOWED;
}

/** 步骤是否可被单步骤执行入口启动，条件与 `describeStepExecution` 完全一致 */
export function canExecuteStep(task: Task, step: Step): boolean {
  return describeStepExecution(task, step).allowed;
}

/**
 * 开发—验证闭环的准入结论与原因。
 *
 * 与服务端 `ExecuteAndValidate` 的准入一致：它复用 `CheckStepExecutionStart`，
 * 但还会在开发之前校验验证配置与修复预算，这两项前端不可见，因此拒绝原因必须
 * 原样展示给用户。
 *
 * 前端可见的前置条件与单步骤执行完全相同，因此原因直接复用同一份判定，避免两处漂移。
 */
export function describeExecuteAndValidate(task: Task, step: Step): ActionEligibility {
  return describeStepExecution(task, step);
}

/** 步骤是否可发起开发—验证闭环，条件与 `describeExecuteAndValidate` 完全一致 */
export function canExecuteAndValidate(task: Task, step: Step): boolean {
  return describeExecuteAndValidate(task, step).allowed;
}

/**
 * 开启新修复周期的准入结论与原因。
 *
 * 服务端只在上一周期已收敛、且预算用尽或策略不再允许修复时才允许开启；前端无法
 * 判断预算，因此这里只排除"明确不可能成功"的现场（任务不允许推进、步骤被占用）。
 */
export function describeRepairCycle(task: Task, step?: Step): ActionEligibility {
  if (!step) return blockedAction("尚未选择步骤，无法开启新的执行周期。");
  const taskGate = describeStepTaskGate(task);
  if (!taskGate.allowed) return taskGate;
  if (step.current_attempt_id) {
    return blockedAction("该步骤仍被当前执行尝试占用，需要等当前周期收敛后才能开启新的执行周期。");
  }
  return ACTION_ALLOWED;
}

/** 步骤是否可显式开启新的修复周期，条件与 `describeRepairCycle` 完全一致 */
export function canOpenRepairCycle(task: Task, step?: Step): boolean {
  return describeRepairCycle(task, step).allowed;
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
    `任务状态：${taskStatusLabel(result.task_status)}`,
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
    `尝试：第 ${result.attempt_number} 次（${attemptStatusMeta(result.attempt_status).label}）`,
    `步骤状态：${stepStatusMeta(result.step_status).label}`,
    `任务状态：${taskStatusLabel(result.task_status)}`,
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
  const details = [
    `已执行步骤：${result.executed_steps} 个`,
    `任务状态：${taskStatusLabel(result.task_status)}`,
  ];
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

/**
 * 从失败响应体中取出开发—验证闭环的结构化执行事实。
 *
 * 服务端只在本次请求确实产生了执行结果时才附加 `operation_result`；前置条件失败、
 * 状态冲突、目标不存在与其他接口的错误响应都不会有该字段，此时返回 `undefined`，
 * 界面维持原来的错误展示。
 *
 * 这里只做结构校验，不推断业务结论：字段取值照实交给 `describeExecuteAndValidateResult`
 * 翻译，`verified` 是否成立仍由服务端响应决定。
 */
export function executeAndValidateResultFromBody(
  body: unknown,
): ExecuteAndValidateStepResponse | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const candidate = (body as { operation_result?: unknown }).operation_result;
  if (typeof candidate !== "object" || candidate === null) return undefined;
  const facts = candidate as Partial<ExecuteAndValidateStepResponse>;
  if (
    typeof facts.task_id !== "string" ||
    typeof facts.step_id !== "string" ||
    typeof facts.validation_ran !== "boolean" ||
    typeof facts.validation_persisted !== "boolean" ||
    typeof facts.verified !== "boolean" ||
    typeof facts.step_status !== "string" ||
    typeof facts.task_status !== "string" ||
    typeof facts.repair_decision !== "string"
  ) {
    return undefined;
  }
  return facts as ExecuteAndValidateStepResponse;
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

/**
 * 把验证响应翻译为用户可读结论。
 *
 * 只陈述响应里真实存在的字段：流程状态、判定、检查统计、逐项检查结果与持久化结果互不
 * 替代。判定是结论的唯一来源——`inconclusive`、未知取值与缺省都不得被展示为"验证通过"，
 * HTTP 请求成功也不等于验证通过。
 */
export function describeValidateResult(result: ValidateAttemptResponse): OperationOutcome {
  const details = [
    `验证流程状态：${validationStatusLabel(result.status)}`,
    `验证结论：${validationVerdictLabel(result.verdict)}`,
    `检查统计：通过 ${result.passed_checks} 项、未通过 ${result.failed_checks} 项、跳过 ${result.skipped_checks} 项`,
    `结果是否可靠写入：${result.persisted ? "是" : "否"}`,
  ];
  if (result.summary) details.push(`摘要：${result.summary}`);
  if (result.failure_summary) details.push(`失败摘要：${result.failure_summary}`);
  if (result.assessment?.needs_human_review) {
    details.push("语义审查要求人工复核：该轮验证不能视为已通过。");
  }
  const checks = result.checks ?? [];
  if (checks.length > 0) {
    details.push(`逐项检查（共 ${checks.length} 项）：`);
    for (const check of checks) details.push(describeCheckFact(check));
  }
  if (!result.persisted) {
    details.push(
      `写入失败原因：${(result.persistence_errors ?? []).join("；") || "服务端未给出原因"}`,
    );
  }
  return { details, ...verdictOutcome(result.verdict) };
}

/**
 * 把代码初始化响应翻译为用户可读结论。
 *
 * `status`、`succeeded`、`task_branch`、`commit` 与 `persisted` 是各自独立的事实：
 * `persisted=true` 只说明记录写入了数据库，不说明初始化成功；`succeeded=true` 也不代表
 * 记录已可靠写入。缺失的分支与提交不补造，`uncertain` 一律要求先核验现场。
 */
export function describeInitializeResult(result: InitializeCodeResponse): OperationOutcome {
  const details = [
    `初始化状态：${codeFetchStatusLabel(result.status)}`,
    `代码是否就绪：${result.succeeded ? "是" : "否"}`,
    `工作目录：${result.work_dir}`,
    `任务分支：${result.task_branch || "服务端未提供"}`,
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
  if (result.status === "uncertain") {
    details.push(
      "服务端没有确认初始化结果：请先重新读取现场核验容器内实际状态，再决定是否再次操作。",
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
