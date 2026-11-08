/**
 * 需求工作台浏览器测试的受控后端。
 *
 * 只模拟 Go 端规划接口的响应形状与状态迁移结果，不模拟真实模型调用：
 * - 会话、消息、Revision 与状态变化都由本模块按服务端契约维护，页面通过真实 HTTP 请求读取；
 * - 分析成功时追加 Revision 与两条角色消息，并推进到 `preview_ready`；
 * - 分析失败时把会话收敛为 `failed` 并记录失败摘要；
 * - 冲突、网络中断与"挂起中"三种现场可以按用例切换，用于验证页面不重复提交、不伪造成功。
 */
import type { Page, Route } from "@playwright/test";

/** 受控项目 */
export type MockProject = {
  id: string;
  name: string;
  default_host_id?: string | null;
};

/** 受控执行节点 */
export type MockHost = {
  id: string;
  name: string;
};

/** 分析请求的响应行为 */
export type AnalysisMode =
  | { kind: "success" }
  | { kind: "failure"; message?: string }
  | { kind: "conflict" }
  | { kind: "network" }
  /** 挂起请求，直到用例调用 `release` 指定后续行为 */
  | { kind: "deferred" };

/**
 * 确认请求的响应行为。
 *
 * `success` 生成正式需求与任务并把会话推进为 `confirmed`；`conflict` 模拟并发确认或
 * 状态不允许；`prerequisite` 模拟缺少可用执行节点；`network` 模拟连接中断。
 */
export type ConfirmMode =
  | { kind: "success" }
  | { kind: "conflict" }
  | { kind: "prerequisite" }
  | { kind: "network" };

/** 预置会话：用于"重新打开历史会话"场景 */
export type SessionSeed = {
  id: string;
  projectID: string;
  status: string;
  hostID?: string;
  errorSummary?: string;
  updatedAt?: string;
  messages?: Array<{ sender: string; content: string }>;
  revisions?: number;
};

type SessionRecord = Record<string, unknown> & { id: string };
type MessageRecord = Record<string, unknown> & { id: string };
type RevisionRecord = Record<string, unknown> & {
  id: string;
  revision_number: number;
};

type BackendState = {
  projects: MockProject[];
  hosts: MockHost[];
  sessions: Map<string, SessionRecord>;
  messages: Map<string, MessageRecord[]>;
  revisions: Map<string, RevisionRecord[]>;
  /** 分析请求携带过的 host_id */
  analysisHosts: string[];
  /** 确认请求携带过的 revision_id 与 host_id */
  confirmRequests: Array<{ revisionID: string; hostID: string }>;
  /** 确认生成的正式需求 */
  requirements: Map<string, Record<string, unknown> & { id: string }>;
  /** 确认生成的正式任务：按需求分组，顺序即 requirement_sequence */
  requirementTasks: Map<string, Array<Record<string, unknown> & { id: string }>>;
  counters: {
    analysis: number;
    retry: number;
    message: number;
    abandon: number;
    create: number;
    /** 正式任务接口调用次数：确认流程不得触发它 */
    task: number;
    confirm: number;
  };
};

/** 测试用例可读取与控制的后端 */
export type PlanningBackend = {
  state: BackendState;
  /** 切换下一次分析请求的行为；挂起中的请求会按新行为继续 */
  setAnalysis: (mode: AnalysisMode) => void;
  /** 释放挂起中的分析请求，并指定它后续的行为 */
  release: (mode: AnalysisMode) => void;
  /** 切换下一次确认请求的行为 */
  setConfirm: (mode: ConfirmMode) => void;
};

/** 固定的时间戳，避免断言依赖真实时钟 */
const TIMESTAMP = "2026-11-08T02:00:00Z";

/** 项目响应体，字段与 Go 端 `httpapi.ProjectResponse` 一致 */
function projectPayload(project: MockProject) {
  return {
    base_branch: "main",
    caches: ["go-mod"],
    created_at: TIMESTAMP,
    default_host_id: project.default_host_id ?? null,
    environment: "go",
    environment_version: "v0001",
    id: project.id,
    name: project.name,
    repository_url: `ssh://git.example.com/team/${project.id}`,
    updated_at: TIMESTAMP,
    work_dir: "/workspace/project",
  };
}

/** 执行节点响应体，字段与 Go 端 `httpapi.HostResponse` 一致 */
function hostPayload(host: MockHost) {
  return {
    auth_method: "private_key",
    created_at: TIMESTAMP,
    enabled: true,
    host: `${host.id}.example.com`,
    id: host.id,
    is_default: false,
    last_connect_status: "ok",
    name: host.name,
    port: 22,
    private_key_path: "/home/dev/.ssh/id_ed25519",
    updated_at: TIMESTAMP,
    usable_for_remote_task: true,
    username: "dev",
    validation_generation: 1,
    validation_status: "passed",
    workspace_root: "/workspace",
  };
}

/** 需求分析快照：八个字段全部非空，摘要带上版本号便于区分历史版本 */
function requirementAnalysisPayload(revisionNumber: number) {
  return {
    acceptance_criteria: [`第 ${revisionNumber} 版全部字段可见`, "任务草案不写入正式任务表"],
    constraints: ["只读项目代码", "不新增第三方依赖"],
    current_state: ["已有任务执行链路", "尚无需求工作台"],
    goals: [`交付第 ${revisionNumber} 版需求工作台`, "多轮对话复用同一会话"],
    out_of_scope: ["方案确认与任务生成"],
    risks: ["模型输出需要结构化校验"],
    scope: ["需求工作台页面", "Revision 预览"],
    summary: `第 ${revisionNumber} 版需求摘要：把需求提出变成一次可多轮调整的对话。`,
  };
}

/** 实施方案快照：顺序、草案标识、依赖与验收条件都完整 */
function implementationPlanPayload(revisionNumber: number) {
  return {
    summary: `第 ${revisionNumber} 版方案摘要：先接入接口，再实现工作台与预览。`,
    tasks: [
      {
        acceptance_criteria: ["接口单元测试覆盖超时覆盖与错误分类"],
        depends_on: [],
        description: `第 ${revisionNumber} 版：封装规划会话、消息与版本接口。`,
        id: `task-${revisionNumber}-1`,
        title: "接入规划接口",
      },
      {
        acceptance_criteria: ["页面可直接操作真实接口", "历史版本可切换"],
        depends_on: [`task-${revisionNumber}-1`],
        description: `第 ${revisionNumber} 版：实现工作台与 Revision 预览。`,
        id: `task-${revisionNumber}-2`,
        title: "实现需求工作台",
      },
    ],
  };
}

/** 会话响应体，字段与 Go 端 `httpapi.PlanningSessionResponse` 一致 */
function sessionPayload(input: {
  id: string;
  projectID: string;
  status: string;
  hostID?: string;
  errorSummary?: string;
  updatedAt?: string;
}): SessionRecord {
  const payload: SessionRecord = {
    created_at: TIMESTAMP,
    id: input.id,
    project_id: input.projectID,
    status: input.status,
    updated_at: input.updatedAt ?? TIMESTAMP,
  };
  if (input.hostID) payload.host_id = input.hostID;
  if (input.errorSummary) payload.error_summary = input.errorSummary;
  return payload;
}

/** 消息响应体 */
function messagePayload(input: {
  id: string;
  sessionID: string;
  sequence: number;
  sender: string;
  content: string;
}): MessageRecord {
  const payload: MessageRecord = {
    content: input.content,
    created_at: TIMESTAMP,
    id: input.id,
    planning_session_id: input.sessionID,
    sender_type: input.sender,
    sequence: input.sequence,
  };
  return payload;
}

/** 版本响应体 */
function revisionPayload(sessionID: string, revisionNumber: number): RevisionRecord {
  return {
    created_at: TIMESTAMP,
    id: `${sessionID}-revision-${revisionNumber}`,
    implementation_plan: implementationPlanPayload(revisionNumber),
    planning_session_id: sessionID,
    requirement_analysis: requirementAnalysisPayload(revisionNumber),
    revision_number: revisionNumber,
    snapshot_version: 1,
  };
}

/**
 * 正式需求响应体，字段与 Go 端 `httpapi.RequirementResponse` 一致。
 *
 * 标题与描述按服务端规则由需求分析快照推导：标题取摘要，描述由摘要与目标、范围、
 * 约束、验收标准组成。
 */
function requirementPayload(input: {
  id: string;
  projectID: string;
  sessionID: string;
  analysis: ReturnType<typeof requirementAnalysisPayload>;
}): Record<string, unknown> & { id: string } {
  const blocks = [
    ["目标", input.analysis.goals],
    ["范围", input.analysis.scope],
    ["约束", input.analysis.constraints],
    ["验收标准", input.analysis.acceptance_criteria],
  ] as const;
  const description = [
    input.analysis.summary,
    ...blocks.map(([label, items]) => `${label}：\n${items.map((item) => `- ${item}`).join("\n")}`),
  ].join("\n\n");
  return {
    confirmed_at: TIMESTAMP,
    created_at: TIMESTAMP,
    description,
    id: input.id,
    planning_session_id: input.sessionID,
    priority: "normal",
    project_id: input.projectID,
    source: "manual",
    status: "queued",
    title: input.analysis.summary,
    updated_at: TIMESTAMP,
  };
}

/** 正式任务响应体，字段与 Go 端 `httpapi.RequirementTaskResponse` 一致 */
function requirementTaskPayload(input: {
  id: string;
  requirementID: string;
  sequence: number;
  hostID?: string;
  title: string;
  description: string;
  acceptanceCriteria: string[];
}): Record<string, unknown> & { id: string } {
  const payload: Record<string, unknown> & { id: string } = {
    acceptance_criteria: input.acceptanceCriteria,
    base_ref: "main",
    created_at: TIMESTAMP,
    goal: `${input.title}\n\n${input.description}`,
    id: input.id,
    repository_path: "ssh://git.example.com/team/demo-service",
    requirement_id: input.requirementID,
    requirement_sequence: input.sequence,
    status: "queued",
  };
  if (input.hostID) payload.host_id = input.hostID;
  return payload;
}

/** 返回 JSON 响应 */
async function fulfillJSON(route: Route, status: number, body: unknown) {
  await route.fulfill({
    body: JSON.stringify(body),
    contentType: "application/json",
    status,
  });
}

/** 返回统一错误结构 */
async function fulfillError(route: Route, status: number, code: string, message: string) {
  await fulfillJSON(route, status, { error: { code, message } });
}

/** 读取路径段 */
function segmentsOf(route: Route): string[] {
  return new URL(route.request().url()).pathname.split("/").filter((segment) => segment.length > 0);
}

/**
 * 安装受控后端。
 *
 * 所有规划接口都在浏览器层拦截，因此用例不需要真实 Go 服务、数据库、SSH 或模型服务。
 */
export async function installPlanningBackend(
  page: Page,
  options: {
    projects?: MockProject[];
    hosts?: MockHost[];
    sessions?: SessionSeed[];
    analysis?: AnalysisMode;
    /** 确认请求的行为 */
    confirm?: ConfirmMode;
  } = {},
): Promise<PlanningBackend> {
  const state: BackendState = {
    analysisHosts: [],
    confirmRequests: [],
    counters: {
      abandon: 0,
      analysis: 0,
      confirm: 0,
      create: 0,
      message: 0,
      retry: 0,
      task: 0,
    },
    hosts: options.hosts ?? [],
    messages: new Map(),
    projects: options.projects ?? [{ id: "demo-service", name: "示例服务" }],
    requirementTasks: new Map(),
    requirements: new Map(),
    revisions: new Map(),
    sessions: new Map(),
  };
  const control: {
    mode: AnalysisMode;
    confirmMode: ConfirmMode;
    pending: Array<() => void>;
  } = {
    confirmMode: options.confirm ?? { kind: "success" },
    mode: options.analysis ?? { kind: "success" },
    pending: [],
  };

  // 预置会话与历史数据
  for (const seed of options.sessions ?? []) {
    state.sessions.set(
      seed.id,
      sessionPayload({
        errorSummary: seed.errorSummary,
        hostID: seed.hostID,
        id: seed.id,
        projectID: seed.projectID,
        status: seed.status,
        updatedAt: seed.updatedAt,
      }),
    );
    const messages: MessageRecord[] = (seed.messages ?? []).map((message, index) =>
      messagePayload({
        content: message.content,
        id: `${seed.id}-message-${index + 1}`,
        sender: message.sender,
        sequence: index + 1,
        sessionID: seed.id,
      }),
    );
    state.messages.set(seed.id, messages);
    const revisions: RevisionRecord[] = [];
    for (let number = 1; number <= (seed.revisions ?? 0); number += 1) {
      revisions.push(revisionPayload(seed.id, number));
    }
    state.revisions.set(seed.id, revisions);
  }

  const backend: PlanningBackend = {
    release: (mode) => {
      control.mode = mode;
      const pending = control.pending.splice(0, control.pending.length);
      for (const resolve of pending) resolve();
    },
    setAnalysis: (mode) => {
      control.mode = mode;
    },
    setConfirm: (mode) => {
      control.confirmMode = mode;
    },
    state,
  };

  /** 当前会话记录；不存在时返回 undefined */
  const sessionOf = (sessionID: string) => state.sessions.get(sessionID);

  /** 记录本轮分析请求并返回它的响应 */
  const runAnalysis = async (route: Route, sessionID: string, kind: "analysis" | "retry") => {
    state.counters[kind] += 1;
    const body = route.request().postDataJSON() as { content?: string; host_id?: string } | null;
    const hostID = body?.host_id?.trim();
    state.analysisHosts.push(hostID ?? "");
    const session = sessionOf(sessionID);
    if (!session) {
      await fulfillError(route, 404, "not_found", "规划会话不存在");
      return;
    }
    const content = body?.content?.trim();
    const messages = state.messages.get(sessionID) ?? [];
    if (kind === "retry" && content) {
      messages.push(
        messagePayload({
          content,
          id: `${sessionID}-message-${messages.length + 1}`,
          sender: "user",
          sequence: messages.length + 1,
          sessionID,
        }),
      );
    }
    state.messages.set(sessionID, messages);
    // 重试请求在服务端先推进状态，再执行两阶段分析
    if (kind === "retry") session.status = "analyzing";

    if (control.mode.kind === "deferred") {
      await new Promise<void>((resolve) => control.pending.push(resolve));
    }
    const mode = control.mode;
    if (mode.kind === "network") {
      await route.abort("connectionreset");
      return;
    }
    if (mode.kind === "conflict") {
      await fulfillError(
        route,
        409,
        "state_conflict",
        "该规划会话已有分析在执行，执行权仍被其他请求持有",
      );
      return;
    }
    if (mode.kind === "failure") {
      if (hostID) session.host_id = hostID;
      const summary = mode.message ?? "需求分析师调用超时：本轮分析未产出可用结果";
      session.status = "failed";
      session.error_summary = summary;
      session.updated_at = TIMESTAMP;
      await fulfillError(route, 500, "execution_failed", summary);
      return;
    }

    // 成功：追加版本与两条角色消息，并推进到 preview_ready
    if (hostID) session.host_id = hostID;
    const revisions = state.revisions.get(sessionID) ?? [];
    const revisionNumber = revisions.length + 1;
    const revision = revisionPayload(sessionID, revisionNumber);
    revisions.push(revision);
    state.revisions.set(sessionID, revisions);
    const analystMessage = messagePayload({
      content: `需求分析师输出（第 ${revisionNumber} 轮）：已完成结构化需求分析。`,
      id: `${sessionID}-analyst-${revisionNumber}`,
      sender: "requirement_analyst",
      sequence: messages.length + 1,
      sessionID,
    });
    const plannerMessage = messagePayload({
      content: `方案规划师输出（第 ${revisionNumber} 轮）：已给出任务草案与依赖顺序。`,
      id: `${sessionID}-planner-${revisionNumber}`,
      sender: "planner",
      sequence: messages.length + 2,
      sessionID,
    });
    messages.push(analystMessage, plannerMessage);
    state.messages.set(sessionID, messages);
    session.status = "preview_ready";
    delete session.error_summary;
    session.updated_at = TIMESTAMP;
    await fulfillJSON(route, 200, {
      implementation_plan: revision.implementation_plan,
      messages: [analystMessage, plannerMessage],
      requirement_analysis: revision.requirement_analysis,
      revision,
      session,
    });
  };

  await page.route("**/api/v1/health", (route) =>
    fulfillJSON(route, 200, {
      checks: [
        { name: "http_api", status: "pass" },
        { name: "database", status: "pass" },
      ],
      checked_at: TIMESTAMP,
      status: "ok",
    }),
  );

  await page.route("**/api/v1/projects", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const records = state.projects.map(projectPayload);
    await fulfillJSON(route, 200, { records, total: records.length });
  });

  await page.route("**/api/v1/hosts", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const records = state.hosts.map(hostPayload);
    await fulfillJSON(route, 200, { records, total: records.length });
  });

  // 记录是否有人调用正式任务接口：草案任务不得触发它
  await page.route("**/api/v1/tasks**", async (route) => {
    state.counters.task += 1;
    await fulfillJSON(route, 200, { current: 1, records: [], size: 20, total: 0 });
  });

  await page.route("**/api/v1/planning/**", async (route) => {
    const segments = segmentsOf(route);
    const method = route.request().method();
    // /api/v1/planning/sessions/...
    const rest = segments.slice(3);
    if (rest.length === 0) {
      await fulfillError(route, 404, "not_found", "未知的规划接口路径");
      return;
    }
    if (rest.length === 1 && rest[0] === "sessions") {
      if (method === "POST") {
        state.counters.create += 1;
        const body = route.request().postDataJSON() as { project_id?: string } | null;
        const projectID = body?.project_id ?? "";
        const id = `session-${state.counters.create}`;
        const session = sessionPayload({ id, projectID, status: "active" });
        state.sessions.set(id, session);
        state.messages.set(id, []);
        state.revisions.set(id, []);
        await fulfillJSON(route, 201, { session });
        return;
      }
      const projectID = new URL(route.request().url()).searchParams.get("project_id") ?? "";
      const records = [...state.sessions.values()]
        .filter((session) => session.project_id === projectID)
        .reverse();
      await fulfillJSON(route, 200, { records, total: records.length });
      return;
    }
    const sessionID = rest[1];
    const session = sessionID ? sessionOf(sessionID) : undefined;
    if (rest.length === 2) {
      if (!session) {
        await fulfillError(route, 404, "not_found", "规划会话不存在");
        return;
      }
      await fulfillJSON(route, 200, { session });
      return;
    }
    if (!session) {
      await fulfillError(route, 404, "not_found", "规划会话不存在");
      return;
    }
    const resource = rest[2];
    if (resource === "messages" && method === "POST") {
      state.counters.message += 1;
      const body = route.request().postDataJSON() as { content?: string } | null;
      const content = body?.content ?? "";
      const messages = state.messages.get(sessionID) ?? [];
      const message = messagePayload({
        content,
        id: `${sessionID}-message-${messages.length + 1}`,
        sender: "user",
        sequence: messages.length + 1,
        sessionID,
      });
      messages.push(message);
      state.messages.set(sessionID, messages);
      session.status = "analyzing";
      session.updated_at = TIMESTAMP;
      await fulfillJSON(route, 201, { message, session });
      return;
    }
    if (resource === "messages") {
      const records = state.messages.get(sessionID) ?? [];
      await fulfillJSON(route, 200, { records, total: records.length });
      return;
    }
    if (resource === "revisions") {
      const records = state.revisions.get(sessionID) ?? [];
      await fulfillJSON(route, 200, { records, total: records.length });
      return;
    }
    if (resource === "abandon") {
      state.counters.abandon += 1;
      session.status = "abandoned";
      session.updated_at = TIMESTAMP;
      await fulfillJSON(route, 200, { session });
      return;
    }
    if (resource === "confirm") {
      state.counters.confirm += 1;
      const body = route.request().postDataJSON() as {
        revision_id?: string;
        host_id?: string;
      } | null;
      const revisionID = body?.revision_id?.trim() ?? "";
      const hostID = body?.host_id?.trim() ?? "";
      state.confirmRequests.push({ hostID, revisionID });
      const mode = control.confirmMode;
      if (mode.kind === "network") {
        await route.abort("connectionreset");
        return;
      }
      if (mode.kind === "conflict") {
        await fulfillError(route, 409, "state_conflict", "规划会话状态不允许确认");
        return;
      }
      if (mode.kind === "prerequisite") {
        await fulfillError(
          route,
          412,
          "precondition_failed",
          "项目未设置默认执行节点，确认必须显式指定执行节点",
        );
        return;
      }
      if (session.status !== "preview_ready") {
        await fulfillError(route, 409, "state_conflict", "规划会话状态不允许确认");
        return;
      }
      const revisions = state.revisions.get(sessionID) ?? [];
      const revision = revisions.find((item) => item.id === revisionID);
      if (!revision) {
        await fulfillError(route, 404, "not_found", "方案版本不属于当前会话");
        return;
      }
      const requirementID = `${sessionID}-requirement`;
      const requirement = requirementPayload({
        analysis: requirementAnalysisPayload(revision.revision_number),
        id: requirementID,
        projectID: typeof session.project_id === "string" ? session.project_id : "",
        sessionID,
      });
      const plan = revision.implementation_plan as ReturnType<typeof implementationPlanPayload>;
      const tasks = plan.tasks.map((task, index) =>
        requirementTaskPayload({
          acceptanceCriteria: [...task.acceptance_criteria],
          description: task.description,
          hostID: hostID || undefined,
          id: `${requirementID}-task-${index + 1}`,
          requirementID,
          sequence: index + 1,
          title: task.title,
        }),
      );
      state.requirements.set(requirementID, requirement);
      state.requirementTasks.set(requirementID, tasks);
      session.status = "confirmed";
      session.adopted_revision_id = revision.id;
      session.finished_at = TIMESTAMP;
      session.updated_at = TIMESTAMP;
      await fulfillJSON(route, 201, {
        adopted_revision_id: revision.id,
        requirement,
        tasks,
      });
      return;
    }
    if (resource === "analysis") {
      if (rest[3] === "retry") {
        await runAnalysis(route, sessionID, "retry");
        return;
      }
      await runAnalysis(route, sessionID, "analysis");
      return;
    }
    await fulfillError(route, 404, "not_found", "未知的规划接口路径");
  });

  // 正式需求只读查询：/api/v1/requirements/{id} 与 /api/v1/projects/{id}/requirements
  await page.route("**/api/v1/requirements/**", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const segments = segmentsOf(route);
    const requirementID = segments[3] ?? "";
    const requirement = state.requirements.get(requirementID);
    if (!requirement) {
      await fulfillError(route, 404, "not_found", "需求不存在");
      return;
    }
    await fulfillJSON(route, 200, {
      requirement,
      tasks: state.requirementTasks.get(requirementID) ?? [],
    });
  });

  await page.route("**/api/v1/projects/*/requirements", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const segments = segmentsOf(route);
    const projectID = segments[3] ?? "";
    const records = [...state.requirements.values()].filter(
      (requirement) => requirement.project_id === projectID,
    );
    await fulfillJSON(route, 200, { records, total: records.length });
  });

  return backend;
}
