/**
 * 任务详情快照的纯计算部分。
 *
 * 与 React 无关：把"一次读取的结果"变换成"页面上应该展示什么、刷新策略是什么、
 * 哪些子面板需要重新加载"。这样竞态、刷新失败与策略判定都可以被直接断言，
 * 而不依赖浏览器或真实等待。
 */
import type { Task, WorkflowStatusResponse } from "@/lib/api";
import {
  AUTO_REFRESH_STOPPED_HINT,
  decideAutoRefresh,
  type AutoRefreshPolicy,
} from "@/pages/tasks/autoRefresh";
import { describeRefreshFailures } from "@/pages/tasks/requestGate";

/** 自动刷新维护的快照 */
export type LiveSnapshot = {
  /** 快照归属的任务标识 */
  taskID?: string;
  /** 任务详情；首次加载失败或缺省时为空 */
  task?: Task;
  /** 工作流进度；任务从未启动工作流时缺省 */
  workflow?: WorkflowStatusResponse;
  /** 首次加载中，还没有任何可用快照 */
  loading: boolean;
  /** 首次加载失败（没有任何可用快照），此时不展示任务详情 */
  error?: string;
  /** 刷新失败说明：展示的仍是上一次成功快照，不代表任务失败或工作流终止 */
  refreshError?: string;
};

/** 子面板的重新加载令牌：只在相关服务端字段变化时递增，不按固定周期无条件请求 */
export type RefreshTokens = {
  /** 步骤、尝试、验证历史与会话 */
  steps: number;
  /** 代码保存记录 */
  codeSaves: number;
  /** 续跑资格与阻塞原因 */
  resume: number;
};

/** 初始令牌：面板首次挂载时自行加载一次 */
export const INITIAL_TOKENS: RefreshTokens = { codeSaves: 0, resume: 0, steps: 0 };

/** 一次读取的原始结果 */
export type SnapshotRoundInput = {
  /** 本次读取的目标任务标识 */
  target: string;
  /** 该任务上一次成功取得的快照；跨任务时调用方必须传空 */
  previous: LiveSnapshot;
  /** 本次取得的任务详情，失败或缺省时为空 */
  task?: Task;
  /** 任务详情请求的错误说明 */
  taskError?: string;
  /** 本次取得的工作流进度，失败或缺省时为空 */
  workflow?: WorkflowStatusResponse;
  /** 工作流进度请求的错误说明 */
  workflowError?: string;
};

/** 一次读取的处理结论 */
export type SnapshotRoundResult = {
  /** 应当展示的快照 */
  snapshot: LiveSnapshot;
  /** 刷新失败的项；为空表示两个请求都成功 */
  failures: string[];
  /** 本次得出的刷新策略；刷新失败时为 undefined（不改变已有节奏） */
  policy?: AutoRefreshPolicy;
  /** 子面板是否需要重新加载 */
  dirty: Partial<Record<keyof RefreshTokens, boolean>>;
};

/**
 * 把一次读取结果变换成展示快照。
 *
 * 三条硬约束：
 *   1. 刷新失败保留上一次成功快照：任务对象与工作流进度都不会被清空，只附加失败提示；
 *   2. 刷新失败不重新判定策略，因此不会被误认为工作流终止，也不会加快请求频率；
 *   3. 子面板只在真实字段变化时才重新加载，不按固定周期无条件请求全部接口。
 */
export function applySnapshotRound(input: SnapshotRoundInput): SnapshotRoundResult {
  const { target, previous, taskError, workflowError } = input;
  const failures: string[] = [];
  if (taskError !== undefined) failures.push(`任务详情（${taskError}）`);
  if (workflowError !== undefined) failures.push(`工作流进度（${workflowError}）`);

  // 没有取得新数据时回退到上一次成功取得的同一份对象，避免把"刷新失败"显示成"数据消失"。
  // 服务端真的没有执行记录时会给出 `has_run: false` 的响应对象，而不是 undefined。
  const task = input.task ?? previous.task;
  const workflow = input.workflow ?? previous.workflow;

  if (!task) {
    // 没有任何可用快照：只报告本次失败，不伪造任务对象，也不清空已有执行结论。
    return {
      dirty: {},
      failures,
      snapshot: { error: taskError, loading: false, taskID: target },
    };
  }

  const snapshot: LiveSnapshot = {
    loading: false,
    refreshError: describeRefreshFailures(failures),
    task,
    taskID: target,
    workflow,
  };
  if (failures.length > 0) return { dirty: {}, failures, snapshot };

  const policy = decideAutoRefresh({ task, workflow });
  const run = workflow?.run;
  const previousRun = previous.workflow?.run;
  const stage = run?.stage;
  return {
    dirty: onlyDirty({
      // 代码保存记录只在代码保存相关阶段发生变化时重新读取。
      codeSaves:
        (stage === "code_save" || stage === "finalize" || task.status === "saving") &&
        (stage !== previousRun?.stage || task.status !== previous.task?.status),
      // 续跑资格要核验远程容器：只在获得"停止自动刷新"的结论时刷新一次。
      resume:
        policy.mode === "off" &&
        (previousRun !== undefined || run !== undefined || previous.task === undefined),
      // 步骤、尝试、验证历史与会话：任务状态或工作流游标真的变化才重新读取。
      steps:
        task.status !== previous.task?.status ||
        run?.revision !== previousRun?.revision ||
        run?.status !== previousRun?.status ||
        run?.current_step_id !== previousRun?.current_step_id ||
        run?.current_attempt_id !== previousRun?.current_attempt_id ||
        run?.completed_steps !== previousRun?.completed_steps ||
        (run !== undefined) !== (previousRun !== undefined),
    }),
    failures,
    policy,
    snapshot,
  };
}

/** 只保留真正需要重新加载的子面板，避免产生无意义的令牌递增 */
function onlyDirty(
  candidates: Record<keyof RefreshTokens, boolean>,
): Partial<Record<keyof RefreshTokens, boolean>> {
  const dirty: Partial<Record<keyof RefreshTokens, boolean>> = {};
  for (const [key, value] of Object.entries(candidates)) {
    if (value) dirty[key as keyof RefreshTokens] = true;
  }
  return dirty;
}

/** 停止自动刷新的提示文案；策略仍在刷新时返回空 */
export function refreshHintFor(policy: AutoRefreshPolicy): string | undefined {
  return policy.mode === "off" ? AUTO_REFRESH_STOPPED_HINT[policy.reason] : undefined;
}
