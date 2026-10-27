/**
 * 任务状态的展示元数据。
 *
 * 状态取值直接来自 Go 端 `domain.TaskStatus`，这里只补充中文标签与颜色，
 * 不新增状态、不改变语义，也不把中间状态压平为成功或失败。
 */
import type { DictItem } from "lingting-react-ui";

import type { Task, TaskStatus, WorkflowRun, WorkflowRunStatus } from "@/lib/api";

/** 状态展示信息 */
export type TaskStatusMeta = {
  /** 中文标签 */
  label: string;
  /** 标签颜色，与 Ant Design 的语义色一致 */
  color: string;
  /** 是否属于执行过程中的中间状态 */
  active: boolean;
};

/** 全部任务状态的展示元数据，覆盖 Go 端的每一个取值 */
export const TASK_STATUS_META: Record<TaskStatus, TaskStatusMeta> = {
  queued: { active: false, color: "default", label: "等待执行" },
  preparing: { active: true, color: "processing", label: "准备环境" },
  planning: { active: true, color: "processing", label: "规划中" },
  planned: { active: true, color: "processing", label: "计划已确认" },
  developing: { active: true, color: "processing", label: "开发中" },
  verifying: { active: true, color: "processing", label: "验证中" },
  saving: { active: true, color: "processing", label: "保存代码" },
  cancelling: { active: true, color: "warning", label: "取消中" },
  retrying: { active: true, color: "processing", label: "准备重试" },
  paused: { active: false, color: "warning", label: "已暂停" },
  recovery_required: { active: false, color: "warning", label: "待核验" },
  completed: { active: false, color: "success", label: "已完成" },
  failed: { active: false, color: "error", label: "失败" },
  cancelled: { active: false, color: "default", label: "已取消" },
};

/** 状态筛选下拉的选项，顺序与状态定义一致 */
export const TASK_STATUS_OPTIONS = Object.entries(TASK_STATUS_META).map(([value, meta]) => ({
  label: meta.label,
  value: value as TaskStatus,
}));

/**
 * "状态取值 → 中文标签"的投影。
 *
 * 它只是 `TASK_STATUS_META` 的另一种形状，用于需要按字符串查标签的场景（例如服务端返回的
 * 状态值可能不在联合类型内）。不新增状态名，也不改变语义。
 */
export const TASK_STATUS_LABEL: Record<TaskStatus, string> = Object.fromEntries(
  Object.entries(TASK_STATUS_META).map(([value, meta]) => [value, meta.label]),
) as Record<TaskStatus, string>;

/** 读取状态元数据；未知状态原样展示，不猜测语义 */
export function taskStatusMeta(status: TaskStatus): TaskStatusMeta {
  return TASK_STATUS_META[status] ?? { active: false, color: "default", label: String(status) };
}

/**
 * 判断某个任务状态取值是否为当前版本已知的取值。
 *
 * 只用于决定界面是否需要额外展示服务端原始取值：未知状态必须原样可见，不能被
 * `taskStatusMeta` 的兜底文案掩盖成"看起来正常"的标签。
 */
export function isKnownTaskStatus(status: string): status is TaskStatus {
  return Object.hasOwn(TASK_STATUS_META, status);
}

/**
 * 是否允许请求取消。
 *
 * 已停止执行的任务（含 `failed`）不能取消，否则服务端会返回 409；
 * 已处于 `cancelling` 时请求已经记录，不需要重复发起。
 */
export function canCancelTask(task: Task): boolean {
  return !task.status_finished && task.status !== "cancelling";
}

/** 是否允许删除：只有已停止执行的任务可以删除，与服务端删除规则一致 */
export function canDeleteTask(task: Task): boolean {
  return task.status_finished;
}

/**
 * 工作流执行状态的展示元数据。
 *
 * 取值直接来自 Go 端 `domain.WorkflowRunStatus`，这里只补充中文标签与颜色。
 * `blocked` 与 `recovery_required` 不是终态：它们表示自动推进已停止，需要用户显式
 * 恢复，页面不能把它们显示为成功或失败。
 */
export type WorkflowRunStatusMeta = {
  /** 中文标签 */
  label: string;
  /** 标签颜色，与 Ant Design 的语义色一致 */
  color: string;
  /** 是否正在推进 */
  active: boolean;
  /** 是否已结束 */
  terminal: boolean;
};

/** 全部工作流执行状态的展示元数据，覆盖 Go 端的每一个取值 */
export const WORKFLOW_RUN_STATUS_META: Record<WorkflowRunStatus, WorkflowRunStatusMeta> = {
  pending: { active: false, color: "default", label: "等待启动", terminal: false },
  running: { active: true, color: "processing", label: "执行中", terminal: false },
  blocked: { active: false, color: "warning", label: "已停止推进", terminal: false },
  cancel_requested: { active: true, color: "warning", label: "取消中", terminal: false },
  cancelled: { active: false, color: "default", label: "已取消", terminal: true },
  completed: { active: false, color: "success", label: "已完成", terminal: true },
  failed: { active: false, color: "error", label: "失败", terminal: true },
  recovery_required: { active: false, color: "warning", label: "待核验", terminal: false },
};

/** 工作流状态的字典项，供 DictTag 使用 */
export const WORKFLOW_RUN_STATUS_DICT: DictItem<string>[] = Object.entries(
  WORKFLOW_RUN_STATUS_META,
).map(([value, meta]) => ({ label: meta.label, tagColor: meta.color, value }));

/** 读取工作流状态元数据；未知状态原样展示，不猜测语义 */
export function workflowRunStatusMeta(status: WorkflowRunStatus): WorkflowRunStatusMeta {
  return (
    WORKFLOW_RUN_STATUS_META[status] ?? {
      active: false,
      color: "default",
      label: String(status),
      terminal: false,
    }
  );
}

/**
 * 判断某个工作流执行状态取值是否为当前版本已知的取值。
 *
 * 与 `isKnownTaskStatus` 同义：未知取值必须原样展示，不能靠字典兜底文案掩盖。
 */
export function isKnownWorkflowRunStatus(status: string): status is WorkflowRunStatus {
  return Object.hasOwn(WORKFLOW_RUN_STATUS_META, status);
}

/**
 * 是否允许启动工作流。
 *
 * 判定依据任务状态：只有能合法迁移到 `developing` 的状态才可以启动，与服务端
 * `domain.TaskStatus.AllowsStepExecution` 保持一致；已请求取消或已结束的任务不允许。
 */
export function canStartWorkflow(task: Task): boolean {
  if (task.cancel_requested || task.status_finished) return false;
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

/** 是否允许请求取消工作流：只有正在推进的执行可以取消 */
export function canCancelWorkflow(run?: WorkflowRun): boolean {
  if (!run) return false;
  if (run.status_terminal || run.cancel_requested) return false;
  return run.status === "running" || run.status === "pending";
}

/**
 * 是否允许恢复工作流。
 *
 * 只有 `blocked`、`recovery_required` 与 `pending` 允许恢复：终态记录不会被恢复流程
 * 重新打开，`cancel_requested` 必须先完成取消。
 */
export function canResumeWorkflow(run?: WorkflowRun): boolean {
  if (!run) return false;
  if (run.cancel_requested) return false;
  return run.status === "blocked" || run.status === "recovery_required" || run.status === "pending";
}
