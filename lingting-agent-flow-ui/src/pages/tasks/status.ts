/**
 * 任务状态的展示元数据。
 *
 * 状态取值直接来自 Go 端 `domain.TaskStatus`，这里只补充中文标签与颜色，
 * 不新增状态、不改变语义，也不把中间状态压平为成功或失败。
 */
import type { Task, TaskStatus } from "@/lib/api";

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

/** 读取状态元数据；未知状态原样展示，不猜测语义 */
export function taskStatusMeta(status: TaskStatus): TaskStatusMeta {
  return TASK_STATUS_META[status] ?? { active: false, color: "default", label: String(status) };
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
