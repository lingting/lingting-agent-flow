/**
 * 任务容器处置（delete / detach）的展示与判定元数据。
 *
 * 本模块只放纯数据与纯函数，不发起请求、不读 React 状态：
 *   - 两个动作的文案与后果说明；
 *   - 管理状态 / Docker 状态 / 结束原因 / 关联方式的中文标签；
 *   - "该记录是否允许在界面发起动作"的展示层判断（服务端仍会重新校验）；
 *   - 处置结论的解释与"结果不确定"提示。
 *
 * 硬约束：
 *   - 不把 delete 与 detach 混成同一个语义，两者文案与后果必须分开；
 *   - 不把"未知状态"当作"可操作"或"已成功"；
 *   - 不由任务状态、`container_id` 或记录顺序猜出容器归属与可操作性。
 */
import type {
  ApiErrorKind,
  ContainerDisposalAction,
  ContainerDisposalOutcome,
  TaskContainer,
} from "@/lib/api";

/** 展示元数据 */
export type Meta = {
  /** 中文标签 */
  label: string;
  /** 语义色，与 Ant Design 的语义色一致 */
  color: "success" | "warning" | "error" | "processing" | "default";
  /** 是否属于终态（不再接受自动操作） */
  terminal: boolean;
};

/** 容器管理状态（Go 端 `domain.ContainerState`）的展示元数据 */
export const CONTAINER_STATE_META: Record<string, Meta> = {
  creating: { color: "processing", label: "创建中", terminal: false },
  created: { color: "processing", label: "已创建，未启动", terminal: false },
  running: { color: "success", label: "运行中", terminal: false },
  stopped: { color: "warning", label: "已停止", terminal: false },
  unknown: { color: "warning", label: "状态不确定", terminal: false },
  detached: { color: "default", label: "已脱离管理", terminal: true },
  removed: { color: "default", label: "已删除", terminal: true },
};

/** 读取容器管理状态的展示元数据；未知取值原样展示，且不视为终态 */
export function containerStateMeta(state: string): Meta {
  const known = CONTAINER_STATE_META[state];
  if (known) return known;
  return { color: "default", label: state || "未提供", terminal: false };
}

/** 判断容器管理状态是否属于当前版本已知的取值 */
export function isKnownContainerState(state: string): boolean {
  return Object.hasOwn(CONTAINER_STATE_META, state);
}

/** 容器与任务关联方式（Go 端 `app.ContainerOrigin`）的展示元数据 */
export const CONTAINER_ORIGIN_META: Record<string, Meta> = {
  current: { color: "processing", label: "任务当前容器", terminal: false },
  history: { color: "default", label: "历史容器", terminal: false },
};

/** 读取关联方式的展示元数据；未知取值原样展示 */
export function containerOriginMeta(origin: string): Meta {
  const known = CONTAINER_ORIGIN_META[origin];
  if (known) return known;
  return { color: "default", label: origin || "未提供", terminal: false };
}

/** 判断关联方式是否属于当前版本已知的取值 */
export function isKnownContainerOrigin(origin: string): boolean {
  return Object.hasOwn(CONTAINER_ORIGIN_META, origin);
}

/** 生命周期结束原因（Go 端 `domain.ContainerEndReason`）的中文标签 */
export const CONTAINER_END_REASON_LABEL: Record<string, string> = {
  task_succeeded: "任务成功后清理",
  task_failed: "任务失败后保留现场",
  interrupted: "任务中断",
  detached: "人工脱离管理",
  manual: "人工处置",
};

/** 读取结束原因标签；空值返回"未记录"，未知取值原样展示 */
export function containerEndReasonLabel(reason: string): string {
  const trimmed = reason.trim();
  if (!trimmed) return "未记录";
  return CONTAINER_END_REASON_LABEL[trimmed] ?? trimmed;
}

/** 处置动作的展示元数据 */
export type DisposalActionMeta = {
  /** 按钮文案 */
  buttonLabel: string;
  /** 确认标题 */
  confirmTitle: string;
  /** 确认描述：必须写清操作对象与后果 */
  confirmDescription: string;
  /** 危险操作，确认按钮使用危险色 */
  danger: boolean;
};

/**
 * 两个动作的文案。
 *
 * 文案差异是产品语义的一部分：`delete` 不可撤销且会删除容器；`detach` 保留现场但放弃
 * 自动管理权，并且**不等于容器已经停止**。任何把两者合并成"清理"的写法都会让用户
 * 误判后果，因此这里刻意重复说明。
 */
export const DISPOSAL_ACTION_META: Record<ContainerDisposalAction, DisposalActionMeta> = {
  delete: {
    buttonLabel: "删除容器",
    confirmTitle: "确认删除该容器？",
    confirmDescription:
      "将删除该容器以及服务端规则允许清理的任务级临时资源。共享镜像、共享缓存卷与远程项目配置目录不会被删除。" +
      "删除不可简单撤销；已脱离管理的容器不会被删除。",
    danger: true,
  },
  detach: {
    buttonLabel: "脱离管理",
    confirmTitle: "确认让该容器脱离 AgentFlow 管理？",
    confirmDescription:
      "将保留容器现场，但 AgentFlow 不再自动启动、停止或删除它。这不是删除，" +
      "也不代表容器已经停止；Docker 标签无法原地修改，因此该操作不可撤销。",
    danger: false,
  },
};

/** 读取动作文案 */
export function disposalActionMeta(action: ContainerDisposalAction): DisposalActionMeta {
  return DISPOSAL_ACTION_META[action];
}

/** 处置结论（Go 端 `app.ContainerDisposalOutcome`）的展示元数据 */
export const DISPOSAL_OUTCOME_META: Record<ContainerDisposalOutcome, Meta> = {
  completed: { color: "success", label: "处置已完成", terminal: true },
  failed: { color: "error", label: "处置未成功", terminal: true },
  uncertain: { color: "warning", label: "处置结果不确定", terminal: false },
};

/** 读取处置结论的展示元数据；未知取值原样展示且不使用成功色 */
export function disposalOutcomeMeta(outcome: string): Meta {
  const known = DISPOSAL_OUTCOME_META[outcome as ContainerDisposalOutcome];
  if (known) return known;
  return { color: "default", label: outcome || "未提供", terminal: false };
}

/**
 * 判断一条容器记录当前是否已处于"动作已生效"的终态。
 *
 * 这里只做展示层的重复提交防护：
 *   - `removed` 的容器不能再删除，也不能脱离管理（已删除无法被报告为成功脱离）；
 *   - `detached` 的容器既不能再删除，也不能重复脱离（脱离管理是终态）。
 *
 * 真正的拒绝仍由服务端 `domain.DecideContainerDisposal` 与容器服务给出，前端不得据此
 * 推断"其他状态一定可操作"——服务端还可能因节点预检失败而拒绝。
 */
export function containerActionBlocked(
  container: TaskContainer,
  action: ContainerDisposalAction,
): boolean {
  if (container.detached || container.state === "detached") return true;
  if (container.state === "removed") return action === "detach";
  return false;
}

/** 判断容器是否已删除（用于列表中的中性展示） */
export function containerRemoved(container: TaskContainer): boolean {
  return container.state === "removed";
}

/**
 * 说明为什么两个动作当前都不可用。
 *
 * 只为"记录本身已处于终态"给出解释；其余情况返回 `undefined`，由服务端决定能否操作，
 * 不在前端制造第二套资格规则。
 */
export function containerActionBlockedReason(container: TaskContainer): string | undefined {
  if (container.detached || container.state === "detached") {
    return "该容器已脱离 AgentFlow 自动管理，不再接受自动删除或重复脱离；如需清理请人工处理。";
  }
  if (container.state === "removed") {
    return "该容器已被删除，删除与脱离管理都不再适用。";
  }
  return undefined;
}

/** 处置结果的不确定提示 */
export const DISPOSAL_UNKNOWN_OUTCOME_HINT =
  "本次请求的结果无法确认：连接中断、超时或被取消，服务端可能已经完成处置，也可能没有。" +
  "页面不会自动重发请求。请先刷新容器列表核验真实状态，再由你决定下一步。";

/** 处置完成后的提示：不宣称任务已重开 */
export const DISPOSAL_NOT_RETRY_HINT =
  "处置容器不会重开任务，也不会启动工作流。如需重新开始，请在确认容器状态后使用任务重试入口。";

/** 判断错误类别是否属于"结果无法确认" */
export function isUncertainDisposalError(kind: ApiErrorKind): boolean {
  return kind === "network" || kind === "timeout" || kind === "aborted";
}

/** 一次处置结果的可展示说明 */
export type DisposalOutcomeView = {
  /** 标题 */
  title: string;
  /** 语义类型 */
  kind: Meta["color"];
  /** 明细行 */
  details: string[];
};

/**
 * 把真实响应转换为可展示结论。
 *
 * 关键的诚实性要求：
 *   - `outcome=completed` 时仍以响应中的 `container` 为准描述结果（已删除 / 已脱离），
 *     不用"成功"概括一切；
 *   - `outcome=uncertain` 时明确说明需要核验，不显示为完成；
 *   - `detail` 是服务端原文，逐字展示。
 */
export function describeDisposalOutcome(input: {
  action: ContainerDisposalAction;
  outcome: string;
  detail?: string;
  container: TaskContainer;
}): DisposalOutcomeView {
  const meta = disposalOutcomeMeta(input.outcome);
  const details: string[] = [];
  const actionLabel = input.action === "delete" ? "删除" : "脱离管理";

  if (input.outcome === "completed") {
    if (input.action === "delete" && containerRemoved(input.container)) {
      details.push("容器记录已标记为已删除。");
    } else if (input.action === "detach" && input.container.detached) {
      details.push("容器记录已标记为脱离管理，容器现场保留。");
    } else {
      details.push(
        `服务端报告${actionLabel}已完成，但本次返回的容器记录没有体现该结果，请以刷新后的真实状态为准。`,
      );
    }
  } else if (input.outcome === "uncertain") {
    details.push(
      `服务端没有确认${actionLabel}结果。请刷新容器列表核验真实状态，不要假定操作已经生效。`,
    );
  } else {
    details.push(`${actionLabel}未成功；下方是服务端返回的真实原因与当前容器状态。`);
  }

  if (input.detail && input.detail.trim()) {
    details.push(`服务端说明：${input.detail.trim()}`);
  }
  details.push(DISPOSAL_NOT_RETRY_HINT);

  return { details, kind: meta.color, title: meta.label };
}
