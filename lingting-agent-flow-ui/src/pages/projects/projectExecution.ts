/**
 * 项目执行与需求进度的展示元数据。
 *
 * 取值全部来自 Go 端 `domain.ProjectExecutionStatus` / `domain.RequirementProgressStatus`
 * 与稳定的阻塞原因常量，这里只补充中文标签与颜色，不新增状态、不改变语义，也不把
 * 未知取值折叠成"成功"或"失败"。
 */
import type { ProjectExecutionStatus, ProjectTaskCounts } from "@/lib/api";
import { taskStatusMeta } from "@/pages/tasks/status";

/** 项目执行状态的展示信息 */
type ProjectExecutionStatusMeta = {
  /** 中文标签 */
  label: string;
  /** 标签颜色 */
  color: string;
  /** 说明文字：解释这个状态对用户可以做的操作意味着什么 */
  description: string;
};

/** 已知的项目执行状态；未列出的取值按只读方式原样展示 */
const PROJECT_EXECUTION_STATUS_META: Record<string, ProjectExecutionStatusMeta> = {
  idle: {
    color: "default",
    description: "项目当前没有执行中的任务。开始执行后按项目内顺序逐个领取任务。",
    label: "空闲",
  },
  running: {
    color: "processing",
    description: "项目持有执行权：同一项目同时最多只有一个任务在执行，不同项目可以并行。",
    label: "执行中",
  },
  blocked: {
    color: "warning",
    description: "队首任务需要人工处置，后续任务不会被自动领取；其他项目不受影响。",
    label: "阻塞",
  },
  paused: {
    color: "warning",
    description: "用户已请求停止领取新任务。当前任务真实收敛后才算停止完成。",
    label: "已停止",
  },
};

/** 读取项目执行状态的展示信息；未知状态原样展示，不猜测语义 */
export function projectExecutionStatusMeta(
  status: ProjectExecutionStatus,
): ProjectExecutionStatusMeta {
  return (
    PROJECT_EXECUTION_STATUS_META[status] ?? {
      color: "default",
      description: "服务端返回了未知的项目执行状态，页面按原值展示并保持只读。",
      label: String(status),
    }
  );
}

/** 需求进度状态的展示信息 */
type RequirementProgressStatusMeta = {
  label: string;
  color: string;
};

/** 已知的需求进度状态 */
const REQUIREMENT_PROGRESS_STATUS_META: Record<string, RequirementProgressStatusMeta> = {
  planned: { color: "default", label: "待执行" },
  executing: { color: "processing", label: "执行中" },
  blocked: { color: "warning", label: "阻塞" },
  completed: { color: "success", label: "已完成" },
};

/** 读取需求进度状态的展示信息；未知状态原样展示 */
export function requirementProgressStatusMeta(status: string): RequirementProgressStatusMeta {
  return REQUIREMENT_PROGRESS_STATUS_META[status] ?? { color: "default", label: String(status) };
}

/**
 * 阻塞原因的中文说明。
 *
 * 服务端返回稳定的原因码，这里只做文案映射；未知原因码原样展示，便于在服务端新增原因时
 * 仍能看到真实事实，而不是显示"未知错误"。
 */
const BLOCKING_REASON_LABELS: Record<string, string> = {
  task_failed: "前序任务已失败",
  task_recovery_required: "前序任务结果不确定，需要先核验",
  task_cancelled: "前序任务已被取消，不再视为完成",
  task_paused: "前序任务处于人工暂停",
  task_in_flight: "前序任务停在中间状态但没有执行记录",
  cancel_requested: "前序任务已记录取消请求，等待真实收敛",
  project_execution_not_held: "项目没有执行权",
  project_execution_expired: "项目执行权已过期",
  project_execution_lost: "项目执行权已属于其他持有者",
  project_execution_stopped: "用户已请求停止领取",
  project_task_not_head: "该任务不是项目内下一个该执行的任务",
  project_task_blocked: "项目被前序任务阻塞",
  project_task_running: "项目内已有任务在执行",
  project_completed: "项目内任务已经全部完成",
  project_execution_unreadable: "项目事实读取失败，无法判定领取资格",
};

/** 把阻塞原因码转换成可读说明；未知原因码原样展示 */
export function describeBlockingReason(reason?: string): string | undefined {
  if (!reason) return undefined;
  return BLOCKING_REASON_LABELS[reason] ?? reason;
}

/** 任务状态的标签与颜色；未知状态原样展示 */
export function projectTaskStatusMeta(status: string): { label: string; color: string } {
  const meta = taskStatusMeta(status as never);
  return { color: meta.color, label: meta.label };
}

/**
 * 进度摘要。
 *
 * 只按已完成任务数量表达进度：不使用模型生成的完成度，也不以时间戳推断。任务总数为 0 时
 * 返回空串，避免出现 "0/0" 这种看似有进展的展示。
 */
export function progressSummary(counts: ProjectTaskCounts): string {
  if (counts.total <= 0) return "项目内没有正式任务";
  return `已完成 ${counts.completed} / 共 ${counts.total} 个任务`;
}

/** 任务数量统计的可读行，只列出非零项 */
export function describeCounts(counts: ProjectTaskCounts): string {
  const parts: string[] = [];
  if (counts.running > 0) parts.push(`执行中 ${counts.running}`);
  if (counts.pending > 0) parts.push(`待执行 ${counts.pending}`);
  if (counts.failed > 0) parts.push(`失败 ${counts.failed}`);
  if (counts.recovery_required > 0) parts.push(`待核验 ${counts.recovery_required}`);
  if (counts.cancelled > 0) parts.push(`已取消 ${counts.cancelled}`);
  if (counts.paused > 0) parts.push(`已暂停 ${counts.paused}`);
  if (parts.length === 0) return "没有需要展示的中间状态任务";
  return parts.join(" · ");
}
