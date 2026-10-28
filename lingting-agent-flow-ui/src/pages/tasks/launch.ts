/**
 * 统一任务启动的展示元数据与结论判定。
 *
 * 这里的取值全部来自 Go 端领域类型（`domain.TaskLaunchStage`、`domain.TaskWorkspaceInitStatus`），
 * 只补充中文标签，不新增阶段名，也不把任何中间阶段显示为成功。
 */
import type { StartTaskResponse, Task, TaskLaunchStage, TaskWorkspaceInitStatus } from "@/lib/api";
import { taskStatusLabel } from "@/pages/tasks/status";

/** 启动阶段的中文标签与说明 */
export type TaskLaunchStageMeta = {
  label: string;
  /** 该阶段在做什么，用于向用户解释失败发生在哪一步 */
  description: string;
};

/** 全部启动阶段，顺序与 Go 端流程一致 */
export const TASK_LAUNCH_STAGE_META: Record<TaskLaunchStage, TaskLaunchStageMeta> = {
  validate: { description: "校验任务记录与执行前置条件", label: "校验任务" },
  container: { description: "准备任务容器", label: "准备容器" },
  workspace: { description: "在容器内初始化代码工作区", label: "初始化工作区" },
  plan: { description: "在容器内生成并持久化计划", label: "生成计划" },
  workflow: { description: "启动任务级工作流执行", label: "启动工作流" },
};

/** 读取阶段元数据；未知阶段原样展示，不猜测语义 */
export function launchStageMeta(stage: string): TaskLaunchStageMeta {
  const known = TASK_LAUNCH_STAGE_META[stage as TaskLaunchStage];
  return known ?? { description: "未知阶段", label: stage };
}

/** 任务级工作区初始化状态的中文标签 */
export const TASK_WORKSPACE_STATUS_LABEL: Record<TaskWorkspaceInitStatus, string> = {
  cancelled: "已取消",
  failed: "失败",
  pending: "待执行",
  running: "执行中",
  skipped: "已就绪（未改动）",
  succeeded: "已就绪",
  timed_out: "已超时",
  uncertain: "结果不确定",
};

/** 读取工作区状态标签；未知取值原样展示 */
export function workspaceStatusLabel(status?: string): string {
  if (!status) return "—";
  return TASK_WORKSPACE_STATUS_LABEL[status as TaskWorkspaceInitStatus] ?? status;
}

/** 工作区是否已就绪：只有 succeeded 与 skipped 算就绪 */
export function workspaceReady(status?: string): boolean {
  return status === "succeeded" || status === "skipped";
}

/**
 * 是否允许对任务发起统一启动。
 *
 * 判定与服务端 `domain.TaskStatus.AllowsExplicitLaunch` 一致：它是
 * `CanStart() && status !== "failed"`，即 `queued`、`preparing`、`planned`、`retrying`、
 * `paused`、`recovery_required`。`failed` 的重新开始属于独立的显式重试入口，不由
 * 统一启动隐式承担；已请求取消的任务也不能启动。
 *
 * 前端禁用只是提示：真正的并发与准入保护仍在服务端任务租约与状态机。
 */
export function canStartTask(task: Task): boolean {
  if (task.cancel_requested || task.status_finished) return false;
  switch (task.status) {
    case "queued":
    case "preparing":
    case "planned":
    case "retrying":
    case "paused":
    case "recovery_required":
      return true;
    default:
      return false;
  }
}

/** 一次启动的结果分类：只有 workflow_started 为真才算启动成功 */
export type TaskLaunchOutcome = {
  /** `started` 表示工作流真的启动了；`blocked` 表示流程结束但没有启动工作流 */
  kind: "started" | "blocked";
  /** 面向用户的标题 */
  title: string;
  /** 面向用户的详细说明，全部来自服务端返回的事实 */
  description: string;
};

/**
 * 把统一启动响应翻译为用户可读的结论。
 *
 * 规则固定：
 *   - `workflow_started=true` 才算启动成功，此时展示服务端返回的真实阶段与进度；
 *   - 否则一律按"未完成/被阻塞"展示阶段、真实任务状态与失败摘要，绝不显示为已启动；
 *   - 计划已持久化、容器已准备这些事实只能作为过程说明，不能替代 `workflow_started`。
 */
export function describeLaunchResult(result: StartTaskResponse): TaskLaunchOutcome {
  const stage = launchStageMeta(result.stage);
  const parts = [
    `阶段：${stage.label}（${stage.description}）`,
    `任务状态：${taskStatusLabel(result.task_status)}`,
    `容器：${result.container_prepared ? "本次已创建" : result.container_id ? "复用既有容器" : "未准备"}`,
    `工作区：${workspaceStatusLabel(result.workspace_status)}`,
    `计划：${result.plan_persisted ? `已持久化 ${result.plan_steps.length} 个步骤` : "未持久化"}`,
  ];
  if (result.workflow_started) {
    return {
      description: [...parts, "工作流已启动，进度以服务端执行记录为准。"].join("；"),
      kind: "started",
      title: "已启动任务执行",
    };
  }
  const failure = result.failure ?? result.run?.error_summary ?? "服务端未启动工作流";
  const failureStage = result.failure_stage
    ? launchStageMeta(result.failure_stage).label
    : stage.label;
  return {
    description: [...parts, `未启动原因（${failureStage}）：${failure}`].join("；"),
    kind: "blocked",
    title: "启动流程未完成，工作流未启动",
  };
}
