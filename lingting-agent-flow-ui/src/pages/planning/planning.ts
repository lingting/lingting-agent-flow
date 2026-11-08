/**
 * 需求工作台的展示与可用性规则。
 *
 * 这里只做纯函数转换，事实全部来自服务端：
 * - 会话状态、消息、Revision 与失败摘要一律原样取用，未知取值不做映射；
 * - 操作可用性只表达页面对该状态的处理方式（是否提供输入、重试、放弃入口），状态迁移
 *   仍然只由服务端领域状态机判定，前端不复制迁移规则；
 * - 错误分类保留"服务端拒绝"与"结果无法确认"的区别，不把网络中断显示成业务失败。
 */
import type {
  ConfirmedRequirement,
  ConfirmedTask,
  ImplementationPlan,
  PlanningRevision,
  RequirementAnalysis,
} from "@/lib/api";

/** 已知的规划会话状态；与服务端 `domain.PlanningSessionStatus` 取值一一对应 */
export type KnownPlanningSessionStatus =
  | "active"
  | "analyzing"
  | "preview_ready"
  | "confirmed"
  | "abandoned"
  | "failed";

/** 已知状态的中文说明；不新增服务端不存在状态名 */
export const PLANNING_STATUS_LABEL: Record<KnownPlanningSessionStatus, string> = {
  abandoned: "已放弃",
  active: "待输入需求",
  analyzing: "分析中",
  confirmed: "已确认",
  failed: "分析失败",
  preview_ready: "方案已生成",
};

/** 状态标签颜色，只影响展示 */
export const PLANNING_STATUS_COLOR: Record<KnownPlanningSessionStatus, string> = {
  abandoned: "default",
  active: "default",
  analyzing: "processing",
  confirmed: "success",
  failed: "error",
  preview_ready: "success",
};

/** 判断状态是否属于当前已知取值 */
export function isKnownPlanningStatus(status: string): status is KnownPlanningSessionStatus {
  return Object.hasOwn(PLANNING_STATUS_LABEL, status);
}

/** 状态中文说明；未知取值原样返回，不映射成默认状态 */
export function planningStatusLabel(status: string): string {
  return isKnownPlanningStatus(status) ? PLANNING_STATUS_LABEL[status] : status;
}

/** 状态标签颜色；未知取值为中性色 */
export function planningStatusColor(status: string): string {
  return isKnownPlanningStatus(status) ? PLANNING_STATUS_COLOR[status] : "default";
}

/** 页面对当前会话开放的操作入口 */
export type PlanningCapabilities = {
  /** 状态是否是当前已知取值；未知状态一律只读 */
  known: boolean;
  /** 是否允许继续追加需求（服务端 `CanAcceptMessage`：active / preview_ready） */
  canAcceptMessage: boolean;
  /**
   * 是否提供显式重试 / 恢复入口。
   *
   * 覆盖两类现场：失败会话（`failed`），以及服务端仍为 `analyzing` 但本页面没有在途
   * 请求的会话。后者的执行权是否真的空闲由服务端判定，冲突时返回 409。
   */
  canResumeAnalysis: boolean;
  /** 是否允许放弃规划（领域状态机只允许 active / preview_ready → abandoned） */
  canAbandon: boolean;
  /** 是否允许发起本轮分析（提交消息之后或恢复之后） */
  writable: boolean;
};

/**
 * 计算页面上的操作可用性。
 *
 * 依据服务端状态机对外可见的行为：`analyzing` 不接受新消息，`confirmed` / `abandoned`
 * 是终态，`failed` 只能经显式重试重新运行。`running` 表示本页面正持有一次在途分析请求，
 * 此时所有动作都要让位给这次请求，避免重复提交。
 */
export function planningCapabilities(input: {
  status: string;
  running: boolean;
}): PlanningCapabilities {
  const known = isKnownPlanningStatus(input.status);
  const canAcceptMessage =
    !input.running && (input.status === "active" || input.status === "preview_ready");
  const canResumeAnalysis =
    !input.running && (input.status === "failed" || input.status === "analyzing");
  const canAbandon =
    !input.running && (input.status === "active" || input.status === "preview_ready");
  return {
    canAbandon,
    canAcceptMessage,
    canResumeAnalysis,
    known,
    writable: known && (canAcceptMessage || canResumeAnalysis || canAbandon),
  };
}

/** 页面状态说明的分类 */
export type PlanningStageKey =
  | "active"
  | "running"
  | "server_analyzing"
  | "preview_ready"
  | "failed"
  | "confirmed"
  | "abandoned"
  | "unknown";

/** 一段状态说明：标题 + 服务端事实说明 */
export type PlanningStageDescription = {
  key: PlanningStageKey;
  label: string;
  detail: string;
};

/**
 * 描述当前会话发生了什么。
 *
 * `running` 与会话状态必须分开表达：只有本页面持有在途请求时才能说"正在执行"，否则
 * 服务端 `analyzing` 只能说明记录仍处于分析态，执行权可能已无人持有。
 */
export function describePlanningStage(input: {
  status: string;
  running: boolean;
  errorSummary?: string;
}): PlanningStageDescription {
  if (input.running) {
    return {
      detail:
        "本页面正在等待服务端完成两阶段分析：需求分析师先执行，方案规划师随后执行。" +
        "只有服务端返回成功并完成持久化，才会出现新的方案版本；页面不会自动重试。",
      key: "running",
      label: "正在执行分析",
    };
  }
  switch (input.status) {
    case "active":
      return {
        detail: "会话已建立，等待输入需求。提交需求后服务端会把会话推进到 analyzing。",
        key: "active",
        label: PLANNING_STATUS_LABEL.active,
      };
    case "analyzing":
      return {
        detail:
          "服务端记录仍为 analyzing，但本页面没有在途请求：本轮分析可能仍在其他位置执行，" +
          "也可能上一次请求中断。请先点「重新读取」确认状态；需要继续时由你显式发起恢复，页面不自动重发。",
        key: "server_analyzing",
        label: PLANNING_STATUS_LABEL.analyzing,
      };
    case "preview_ready":
      return {
        detail: "本轮方案已生成并落库，可以查看需求分析与实施方案，也可以继续追加需求生成新版本。",
        key: "preview_ready",
        label: PLANNING_STATUS_LABEL.preview_ready,
      };
    case "failed":
      return {
        detail: input.errorSummary
          ? `本轮分析失败，失败摘要来自服务端记录：${input.errorSummary}`
          : "本轮分析失败，服务端未记录可读摘要。重试会开启新一轮两阶段分析。",
        key: "failed",
        label: PLANNING_STATUS_LABEL.failed,
      };
    case "confirmed":
      return {
        detail:
          "会话方案已确认：服务端已采纳一版 Revision 并生成正式需求与任务。" +
          "采纳结果只读展示，任务保持待执行；本阶段不提供开始执行入口。",
        key: "confirmed",
        label: PLANNING_STATUS_LABEL.confirmed,
      };
    case "abandoned":
      return {
        detail: "会话已放弃，消息与历史方案版本保留，本阶段为只读展示。",
        key: "abandoned",
        label: PLANNING_STATUS_LABEL.abandoned,
      };
    default:
      return {
        detail: `服务端返回的状态「${input.status}」不在当前页面已知取值中，页面保持只读，不猜测它的含义。`,
        key: "unknown",
        label: input.status,
      };
  }
}

/** 一次动作失败的展示信息 */
export type PlanningErrorFacts = {
  message: string;
  /** 无法确认服务端是否已完成本次操作（网络中断、超时、取消） */
  uncertain: boolean;
  /** 服务端以状态冲突拒绝：可能仍有其他执行者持有执行权 */
  conflict: boolean;
  /** 规划能力未装配或已停止（503），请求没有被执行 */
  unavailable: boolean;
};

/**
 * 把动作错误翻译成可读说明。
 *
 * 三类不能混：
 * - 网络中断 / 超时 / 取消：结果不确定，不得当作模型执行失败；
 * - 409：执行权或状态冲突，需要重新读取服务端状态；
 * - 503：规划能力不可用，请求根本没有执行业务逻辑。
 */
export function describePlanningActionError(error: unknown): PlanningErrorFacts {
  const fallback = error instanceof Error ? error.message : "未知错误";
  const status = (error as { status?: unknown }).status;
  const kind = (error as { kind?: unknown }).kind;
  if (kind === "network" || kind === "timeout" || kind === "aborted") {
    return {
      conflict: false,
      message:
        "与分析请求的连接中断，无法确认本轮分析是否已在服务端完成。" +
        "页面不会自动重试，请以重新读取到的会话、消息与方案版本为准。",
      uncertain: true,
      unavailable: false,
    };
  }
  if (status === 409) {
    return {
      conflict: true,
      message:
        "服务端返回状态冲突（409）：该会话可能仍被其他执行占用，或当前状态不允许本次操作。" +
        "已重新读取服务端状态，请勿重复提交同一条消息。",
      uncertain: false,
      unavailable: false,
    };
  }
  if (status === 503) {
    return {
      conflict: false,
      message: "规划服务当前不可用（503），本次请求没有被执行，请稍后再试。",
      uncertain: false,
      unavailable: true,
    };
  }
  return { conflict: false, message: fallback, uncertain: false, unavailable: false };
}

/** 已知的规划消息发送方 */
export const PLANNING_SENDER_LABEL: Record<string, string> = {
  planner: "方案规划师",
  requirement_analyst: "需求分析师",
  system: "系统",
  user: "我",
};

/** 发送方标签颜色 */
const PLANNING_SENDER_COLOR: Record<string, string> = {
  planner: "purple",
  requirement_analyst: "blue",
  system: "default",
  user: "geekblue",
};

/** 发送方说明；未知取值原样返回 */
export function planningSenderLabel(sender: string): string {
  return PLANNING_SENDER_LABEL[sender] ?? sender;
}

/** 发送方标签颜色；未知取值为中性色 */
export function planningSenderColor(sender: string): string {
  return PLANNING_SENDER_COLOR[sender] ?? "default";
}

/** 输入校验：只拒绝空白输入，不裁剪用户原始文本 */
export function isBlankPlanningInput(value: string): boolean {
  return value.trim().length === 0;
}

/** 按序号升序排列；服务端已排序，这里只保证渲染顺序稳定 */
export function sortPlanningMessages<T extends { sequence: number }>(messages: T[]): T[] {
  return [...messages].sort((left, right) => left.sequence - right.sequence);
}

/** 按版本号升序排列 */
export function sortPlanningRevisions(revisions: PlanningRevision[]): PlanningRevision[] {
  return [...revisions].sort((left, right) => left.revision_number - right.revision_number);
}

/** 最新一版；没有版本时返回 undefined */
export function latestPlanningRevision(
  revisions: PlanningRevision[],
): PlanningRevision | undefined {
  return sortPlanningRevisions(revisions).at(-1);
}

/** 版本展示标签 */
export function planningRevisionLabel(revision: PlanningRevision): string {
  return `第 ${revision.revision_number} 版`;
}

/**
 * 解析当前应该展示哪一版。
 *
 * 规则：
 * - 没有选中或选中的版本已不存在 → 最新一版；
 * - 最新一版刚刚变化（新一轮分析追加了版本）→ 自动跟随最新一版；
 * - 否则保留用户的选择，历史版本始终可切换。
 */
export function resolvePlanningRevisionSelection(input: {
  revisions: PlanningRevision[];
  selectedID?: string;
  /** 上一次渲染时最新一版的标识，用于识别"新版本刚刚生成" */
  previousLatestID?: string;
}): string | undefined {
  const latest = latestPlanningRevision(input.revisions);
  if (!latest) return undefined;
  const selectedStillExists = input.revisions.some((revision) => revision.id === input.selectedID);
  if (!selectedStillExists) return latest.id;
  if (input.previousLatestID && input.previousLatestID !== latest.id) return latest.id;
  return input.selectedID;
}

/** 需求分析展示区块 */
export type PlanningAnalysisSection = {
  key: keyof RequirementAnalysis;
  title: string;
  kind: "text" | "list";
  text?: string;
  items?: string[];
};

/** 列表字段的原值；缺失时返回空数组，不补造内容 */
function analysisItems(value?: string[]): string[] {
  return Array.isArray(value) ? value : [];
}

/**
 * 需求分析八个字段的展示顺序。
 *
 * 只做字段到标题的映射，不判断内容是否合理，也不隐藏服务端返回的空列表。
 */
export function requirementAnalysisSections(
  analysis?: RequirementAnalysis | null,
): PlanningAnalysisSection[] {
  if (!analysis) return [];
  return [
    { key: "summary", kind: "text", text: analysis.summary, title: "需求摘要" },
    { items: analysisItems(analysis.goals), key: "goals", kind: "list", title: "目标" },
    {
      items: analysisItems(analysis.current_state),
      key: "current_state",
      kind: "list",
      title: "项目现状",
    },
    { items: analysisItems(analysis.scope), key: "scope", kind: "list", title: "本轮范围" },
    {
      items: analysisItems(analysis.out_of_scope),
      key: "out_of_scope",
      kind: "list",
      title: "非目标",
    },
    {
      items: analysisItems(analysis.constraints),
      key: "constraints",
      kind: "list",
      title: "约束条件",
    },
    { items: analysisItems(analysis.risks), key: "risks", kind: "list", title: "风险" },
    {
      items: analysisItems(analysis.acceptance_criteria),
      key: "acceptance_criteria",
      kind: "list",
      title: "验收标准",
    },
  ];
}

/** 实施方案任务列表的一行：草案标识、顺序与依赖都是原值 */
export type PlanningPlanTaskRow = Record<string, unknown> & {
  key: string;
  order: number;
  task_id: string;
  title: string;
  description: string;
  depends_on: string[];
  acceptance_criteria: string[];
};

/**
 * 把任务草案转换成表格行。
 *
 * 顺序取方案中的原始位置（从 1 开始），草案标识只用于表达依赖，不是正式任务标识。
 */
export function implementationPlanTaskRows(
  plan?: ImplementationPlan | null,
): PlanningPlanTaskRow[] {
  const tasks = Array.isArray(plan?.tasks) ? plan.tasks : [];
  return tasks.map((task, index) => ({
    acceptance_criteria: Array.isArray(task.acceptance_criteria) ? task.acceptance_criteria : [],
    depends_on: Array.isArray(task.depends_on) ? task.depends_on : [],
    description: task.description,
    key: task.id,
    order: index + 1,
    task_id: task.id,
    title: task.title,
  }));
}

/** 依赖展示文本；无依赖时给出中性说明 */
export function planningTaskDependencyText(dependsOn: string[]): string {
  return dependsOn.length > 0 ? dependsOn.join(" → ") : "无前置依赖";
}

/** 时间展示：使用本地时区，缺省显示占位符 */
export function formatPlanningTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/** 查询类请求（会话列表、会话详情、消息、方案版本）的可读错误说明 */
export function describePlanningQueryError(error: unknown): string {
  const kind = (error as { kind?: unknown }).kind;
  if (kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
  if (kind === "timeout") return "查询超时，请稍后重试。";
  if (kind === "aborted") return "查询已取消。";
  if (kind === "invalid_response") return "服务返回的内容无法解析，请查看服务端日志。";
  return error instanceof Error ? error.message : "未知错误";
}

/** 执行节点的来源：已绑定会话优先，其次是项目默认节点，最后才是用户显式选择 */
export type PlanningHostSource = "session" | "project_default" | "selected" | "missing";

/** 本轮分析使用的执行节点判定结果 */
export type PlanningHostReadiness = {
  source: PlanningHostSource;
  /** 分析请求携带的 host_id；已绑定、有默认节点或已选择时才有值 */
  hostID?: string;
  /** 需要用户显式选择执行节点 */
  requiresSelection: boolean;
  /** 无法发起分析时的可读原因；可以发起时为 undefined */
  blockedReason?: string;
};

/**
 * 解析本轮分析要使用的执行节点。
 *
 * 规则来自服务端契约：分析请求的 `host_id` 只在运行环境尚未准备时使用，已绑定的会话
 * 不允许在这里改指其他节点；项目默认节点只是配置偏好，不代表预检已通过。没有默认节点
 * 时不自动取列表里的第一个节点，必须由用户显式选择。
 */
export function resolvePlanningHost(input: {
  /** 当前会话已绑定的执行节点 */
  sessionHostID?: string;
  /** 项目的默认执行节点（服务端以 null 表示未设置） */
  projectDefaultHostID?: string | null;
  /** 用户显式选择的执行节点 */
  selectedHostID?: string;
  /** 执行节点查询状态 */
  hostsStatus: "loading" | "ready" | "error";
}): PlanningHostReadiness {
  const sessionHost = input.sessionHostID?.trim();
  if (sessionHost) {
    return { hostID: sessionHost, requiresSelection: false, source: "session" };
  }
  const defaultHost = input.projectDefaultHostID?.trim();
  if (defaultHost) {
    return { hostID: defaultHost, requiresSelection: false, source: "project_default" };
  }
  const selected = input.selectedHostID?.trim();
  if (selected) {
    return { hostID: selected, requiresSelection: false, source: "selected" };
  }
  const blockedReason =
    input.hostsStatus === "loading"
      ? "正在查询执行节点，请等待查询完成后再发起分析。"
      : input.hostsStatus === "error"
        ? "执行节点查询失败，无法确认可用的执行节点，请先重试查询。"
        : "该项目没有默认执行节点，请先显式选择执行节点后再发起分析；页面不会自动选择节点。";
  return { blockedReason, requiresSelection: true, source: "missing" };
}

/** 需求输入区的可用性判定结果 */
export type PlanningComposerState = {
  disabled: boolean;
  /** 不允许提交时的可读原因；可以提交时为 undefined */
  reason?: string;
};

/**
 * 判定需求输入区是否可以提交。
 *
 * 判定顺序固定为"先选项目 → 再选会话 → 再看会话状态 → 最后看执行节点"，与用户实际
 * 操作顺序一致；任何一个前置条件不成立都不会发出请求。
 */
export function describePlanningComposerState(input: {
  hasProject: boolean;
  hasSession: boolean;
  status: string;
  running: boolean;
  /** 执行节点前置条件未满足时的原因 */
  hostBlockedReason?: string;
}): PlanningComposerState {
  if (!input.hasProject) {
    return {
      disabled: true,
      reason: "请先选择项目：规划会话与需求消息都必须归属于一个真实项目。",
    };
  }
  if (!input.hasSession) {
    return { disabled: true, reason: "请先创建或选择一个规划会话，再提交需求。" };
  }
  if (input.running) {
    return {
      disabled: true,
      reason: "正在执行本轮分析：请等待服务端返回结果，页面不会重复提交同一条需求。",
    };
  }
  const capabilities = planningCapabilities({ running: false, status: input.status });
  if (!capabilities.canAcceptMessage) {
    return {
      disabled: true,
      reason: describeMessageBlocker(input.status),
    };
  }
  if (input.hostBlockedReason) {
    // 具体原因由执行节点区说明，这里只提示"还没确定节点"，避免同一句话在页面上出现两次。
    return {
      disabled: true,
      reason: "尚未确定本轮分析使用的执行节点：请先按上方提示选择或配置执行节点。",
    };
  }
  return { disabled: false };
}

/** 当前状态不允许追加需求的原因 */
function describeMessageBlocker(status: string): string {
  switch (status) {
    case "analyzing":
      return "会话处于分析中：本轮收敛之前不接受新的需求消息。";
    case "failed":
      return "本轮分析失败：请先显式重试，重试成功后再继续追加需求。";
    case "confirmed":
      return "会话方案已确认，本阶段只读展示。";
    case "abandoned":
      return "会话已放弃，本阶段只读展示。";
    default:
      return `服务端状态「${status}」不在当前页面已知取值中，不开放写操作。`;
  }
}

/**
 * 页面用于展示的"将要生成的需求草案"。
 *
 * 与服务端 `domain.RequirementTitleFromAnalysis` / `RequirementDescriptionFromAnalysis`
 * 使用同一条规则：标题取需求分析摘要，描述由摘要与目标、范围、约束、验收标准逐条组成。
 * 这里只用于确认前的预览，实际落库内容以服务端返回的需求为准。
 */
export type PlanningRequirementDraft = {
  title: string;
  description: string;
};

/** 由需求分析快照推导确认后将生成的需求草案 */
export function draftRequirementFromAnalysis(
  analysis?: RequirementAnalysis | null,
): PlanningRequirementDraft {
  if (!analysis) return { description: "", title: "" };
  const title = (analysis.summary ?? "").trim();
  const sections: Array<[string, string[] | undefined]> = [
    ["目标", analysis.goals],
    ["范围", analysis.scope],
    ["约束", analysis.constraints],
    ["验收标准", analysis.acceptance_criteria],
  ];
  const blocks = sections.map(([label, items]) => {
    const lines = analysisItems(items).map((item) => `- ${item.trim()}`);
    return `${label}：\n${lines.join("\n")}`;
  });
  return { description: [title, ...blocks].join("\n\n"), title };
}

/** 确认前摘要中的一行任务草案 */
export type PlanningConfirmTaskRow = {
  order: number;
  task_id: string;
  title: string;
  depends_on: string[];
};

/** 确认前摘要：采纳哪一版、生成什么 */
export type PlanningConfirmSummary = {
  revisionID: string;
  revisionLabel: string;
  requirement: PlanningRequirementDraft;
  tasks: PlanningConfirmTaskRow[];
  taskCount: number;
};

/**
 * 构造确认摘要。
 *
 * 全部内容来自选定 Revision 的快照：版本号说明采纳的是哪一版，需求标题与描述说明将生成
 * 什么，任务行说明会生成多少个正式任务以及它们的顺序。任务顺序取数组位置，与服务端写入
 * `requirement_sequence` 的规则一致。
 */
export function describePlanningConfirmSummary(
  revision?: PlanningRevision | null,
): PlanningConfirmSummary | undefined {
  if (!revision) return undefined;
  const rows = implementationPlanTaskRows(revision.implementation_plan);
  return {
    requirement: draftRequirementFromAnalysis(revision.requirement_analysis),
    revisionID: revision.id,
    revisionLabel: planningRevisionLabel(revision),
    taskCount: rows.length,
    tasks: rows.map((row) => ({
      depends_on: row.depends_on,
      order: row.order,
      task_id: row.task_id,
      title: row.title,
    })),
  };
}

/** 确认操作的可用性判定结果 */
export type PlanningConfirmAvailability = {
  available: boolean;
  /** 不可确认时的可读原因；可以确认时为 undefined */
  reason?: string;
};

/**
 * 判定当前是否允许确认选定的 Revision。
 *
 * 判定顺序固定为"选会话 → 选版本 → 会话状态 → 是否已有在途动作"，与用户实际可见的
 * 前置条件一致；任何一项不成立都不会发出确认请求。并发安全仍由服务端条件更新保证，
 * 这里的判定只用于禁用按钮并说明原因。
 */
export function describePlanningConfirmAvailability(input: {
  hasSession: boolean;
  status: string;
  selectedRevisionID?: string;
  running: boolean;
  confirming: boolean;
  /** 执行节点前置条件未满足时的原因 */
  hostBlockedReason?: string;
}): PlanningConfirmAvailability {
  if (!input.hasSession) {
    return { available: false, reason: "请先创建或选择一个规划会话。" };
  }
  if (!input.selectedRevisionID?.trim()) {
    return { available: false, reason: "请先在方案预览中选择要采纳的方案版本。" };
  }
  if (input.confirming) {
    return {
      available: false,
      reason: "确认请求正在处理中：请等待服务端返回结果，页面不会重复提交。",
    };
  }
  if (input.running) {
    return { available: false, reason: "本轮分析仍在进行：请等待分析收敛后再确认方案。" };
  }
  if (input.status !== "preview_ready") {
    return {
      available: false,
      reason: describeConfirmBlocker(input.status),
    };
  }
  if (input.hostBlockedReason) {
    return { available: false, reason: input.hostBlockedReason };
  }
  return { available: true };
}

/**
 * 解析确认生成正式任务时使用的执行节点。
 *
 * 与服务端规则一致：请求里的 `host_id` 优先，为空时由服务端使用项目默认节点。规划会话
 * 已绑定的节点只描述规划容器，不能当作任务默认配置，因此这里不读它；两者都没有时不
 * 自动选择列表中的第一个节点。
 */
export function resolveConfirmHost(input: {
  projectDefaultHostID?: string | null;
  selectedHostID?: string;
  hostsStatus: "loading" | "ready" | "error";
}): { hostID?: string; blockedReason?: string } {
  const selected = input.selectedHostID?.trim();
  if (selected) {
    return { hostID: selected };
  }
  if (input.projectDefaultHostID?.trim()) {
    return {};
  }
  const blockedReason =
    input.hostsStatus === "loading"
      ? "正在查询执行节点，请等待查询完成后再确认方案。"
      : input.hostsStatus === "error"
        ? "执行节点查询失败，无法确认可用的执行节点，请先重试查询。"
        : "该项目没有默认执行节点，请先显式选择执行节点后再确认；页面不会自动选择节点。";
  return { blockedReason };
}

/** 状态不允许确认的原因 */
function describeConfirmBlocker(status: string): string {
  switch (status) {
    case "active":
      return "会话还没有生成方案版本：确认的前提是至少存在一版已落库的 Revision。";
    case "analyzing":
      return "会话处于分析中：本轮收敛之前无法确认方案。";
    case "confirmed":
      return "会话方案已确认：已采纳的方案版本与生成的正式实体是只读事实。";
    case "abandoned":
      return "会话已放弃：不再接受确认。";
    case "failed":
      return "本轮分析失败：请先显式重试并生成可用的方案版本。";
    default:
      return `服务端状态「${status}」不在当前页面已知取值中，不开放确认入口。`;
  }
}

/**
 * 把确认失败翻译成可读说明。
 *
 * 三类不能混：网络中断时结果不确定（可能已生成正式实体）、409 是状态冲突（并发确认或
 * 会话已被推进）、412 是前置条件不满足（项目已删除、缺少可用执行节点、角色不可用）。
 */
export function describePlanningConfirmError(error: unknown): PlanningErrorFacts {
  const fallback = error instanceof Error ? error.message : "未知错误";
  const status = (error as { status?: unknown }).status;
  const kind = (error as { kind?: unknown }).kind;
  if (kind === "network" || kind === "timeout" || kind === "aborted") {
    return {
      conflict: false,
      message:
        "确认请求的连接中断，无法确认服务端是否已经生成正式需求与任务。" +
        "页面不会自动重放确认，请以重新读取到的会话状态与需求列表为准。",
      uncertain: true,
      unavailable: false,
    };
  }
  if (status === 409) {
    return {
      conflict: true,
      message:
        "服务端返回状态冲突（409）：该会话可能已被其他确认请求采纳，或当前状态不允许确认。" +
        "已重新读取会话与方案版本，请以服务端事实为准，不要重复确认。",
      uncertain: false,
      unavailable: false,
    };
  }
  if (status === 412) {
    return {
      conflict: false,
      message:
        "确认前置条件不满足（412）：项目可能已下线、缺少可用执行节点，或任务角色配置不可用。" +
        "请先修复前置条件，本次没有生成任何正式实体。",
      uncertain: false,
      unavailable: false,
    };
  }
  if (status === 503) {
    return {
      conflict: false,
      message: "需求服务当前不可用（503），本次确认没有被执行，请稍后再试。",
      uncertain: false,
      unavailable: true,
    };
  }
  return { conflict: false, message: fallback, uncertain: false, unavailable: false };
}

/** 确认结果中的一行任务：顺序与状态都是服务端原值 */
export type ConfirmedTaskRow = {
  key: string;
  order?: number;
  task_id: string;
  title: string;
  status: string;
};

/** 把正式任务整理成展示行：按 requirement_sequence 升序，缺失顺序的排在后面 */
export function confirmedTaskRows(tasks: ReadonlyArray<ConfirmedTask>): ConfirmedTaskRow[] {
  const sorted = [...tasks].sort((left, right) => {
    const leftOrder = left.requirement_sequence;
    const rightOrder = right.requirement_sequence;
    if (leftOrder !== undefined && rightOrder !== undefined) return leftOrder - rightOrder;
    if (leftOrder !== undefined) return -1;
    if (rightOrder !== undefined) return 1;
    return left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id);
  });
  return sorted.map((task) => ({
    key: task.id,
    order: task.requirement_sequence,
    status: task.status,
    task_id: task.id,
    title: task.goal.split("\n")[0] ?? "",
  }));
}

/**
 * 从项目需求列表中找出当前会话采纳生成的需求。
 *
 * 已确认的会话本身不保存需求标识，因此恢复一个已确认会话时按 `planning_session_id`
 * 匹配；没有任何匹配时返回 undefined，页面如实说明"尚未读取到正式需求"，不补造结果。
 */
export function findSessionRequirement(
  requirements: ReadonlyArray<ConfirmedRequirement>,
  sessionID?: string,
): ConfirmedRequirement | undefined {
  const trimmed = sessionID?.trim();
  if (!trimmed) return undefined;
  return requirements.find((requirement) => requirement.planning_session_id === trimmed);
}
