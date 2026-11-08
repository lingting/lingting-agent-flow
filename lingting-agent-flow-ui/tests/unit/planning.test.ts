import { afterEach, describe, expect, it, vi } from "vitest";

import {
  abandonPlanningSession,
  ApiError,
  createPlanningSession,
  fetchPlanningMessages,
  fetchPlanningRevisions,
  fetchPlanningSession,
  fetchPlanningSessions,
  PLANNING_SESSIONS_PATH,
  planningAbandonPath,
  planningAnalysisPath,
  planningAnalysisRetryPath,
  planningMessagesPath,
  planningRevisionsPath,
  planningSessionPath,
  retryPlanningAnalysis,
  runPlanningAnalysis,
  submitPlanningMessage,
  type ImplementationPlan,
  type PlanningAnalysisResponse,
  type PlanningMessage,
  type PlanningRevision,
  type PlanningSession,
  type RequirementAnalysis,
} from "../../src/lib/api";
import {
  describePlanningActionError,
  describePlanningComposerState,
  describePlanningQueryError,
  describePlanningStage,
  implementationPlanTaskRows,
  isBlankPlanningInput,
  isKnownPlanningStatus,
  latestPlanningRevision,
  planningCapabilities,
  planningRevisionLabel,
  planningSenderColor,
  planningSenderLabel,
  planningStatusColor,
  planningStatusLabel,
  planningTaskDependencyText,
  requirementAnalysisSections,
  resolvePlanningHost,
  resolvePlanningRevisionSelection,
  sortPlanningMessages,
  sortPlanningRevisions,
} from "../../src/pages/planning/planning";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/** 需求分析快照 */
function sampleAnalysis(overrides: Partial<RequirementAnalysis> = {}): RequirementAnalysis {
  return {
    acceptance_criteria: ["全部字段可见"],
    constraints: ["只读项目代码"],
    current_state: ["已有任务执行链路"],
    goals: ["交付需求工作台"],
    out_of_scope: ["确认与任务生成"],
    risks: ["模型输出需要结构化校验"],
    scope: ["工作台页面"],
    summary: "把需求提出变成一次可多轮调整的对话。",
    ...overrides,
  };
}

/** 实施方案快照 */
function samplePlan(overrides: Partial<ImplementationPlan> = {}): ImplementationPlan {
  return {
    summary: "先接入接口，再实现工作台。",
    tasks: [
      {
        acceptance_criteria: ["接口可用"],
        depends_on: [],
        description: "封装规划接口",
        id: "task-1",
        title: "接入接口",
      },
      {
        acceptance_criteria: ["版本可切换"],
        depends_on: ["task-1"],
        description: "实现工作台与预览",
        id: "task-2",
        title: "实现工作台",
      },
    ],
    ...overrides,
  };
}

/** 方案版本快照 */
function sampleRevision(overrides: Partial<PlanningRevision> = {}): PlanningRevision {
  return {
    created_at: "2026-11-08T02:00:00Z",
    id: "revision-1",
    implementation_plan: samplePlan(),
    planning_session_id: "session-1",
    requirement_analysis: sampleAnalysis(),
    revision_number: 1,
    snapshot_version: 1,
    ...overrides,
  };
}

/** 规划会话 */
function sampleSession(overrides: Partial<PlanningSession> = {}): PlanningSession {
  return {
    created_at: "2026-11-08T02:00:00Z",
    id: "session-1",
    project_id: "demo-service",
    status: "preview_ready",
    updated_at: "2026-11-08T02:10:00Z",
    ...overrides,
  };
}

/** 规划消息 */
function sampleMessage(overrides: Partial<PlanningMessage> = {}): PlanningMessage {
  return {
    content: "请实现需求工作台",
    created_at: "2026-11-08T02:00:00Z",
    id: "message-1",
    planning_session_id: "session-1",
    sender_type: "user",
    sequence: 1,
    ...overrides,
  };
}

/** 一轮分析结果 */
function sampleAnalysisResponse(): PlanningAnalysisResponse {
  return {
    implementation_plan: samplePlan(),
    messages: [sampleMessage({ id: "message-2", sender_type: "requirement_analyst", sequence: 2 })],
    requirement_analysis: sampleAnalysis(),
    revision: sampleRevision(),
    session: sampleSession(),
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("规划接口路径", () => {
  it("集合路径挂在 /api/v1/planning/sessions 下", () => {
    expect(PLANNING_SESSIONS_PATH).toBe("/api/v1/planning/sessions");
  });

  it("子资源路径按段编码，空标识直接报错", () => {
    expect(planningSessionPath("session-1")).toBe("/api/v1/planning/sessions/session-1");
    expect(planningSessionPath("a b")).toBe("/api/v1/planning/sessions/a%20b");
    expect(planningMessagesPath("session-1")).toBe("/api/v1/planning/sessions/session-1/messages");
    expect(planningRevisionsPath("session-1")).toBe(
      "/api/v1/planning/sessions/session-1/revisions",
    );
    expect(planningAnalysisPath("session-1")).toBe("/api/v1/planning/sessions/session-1/analysis");
    expect(planningAnalysisRetryPath("session-1")).toBe(
      "/api/v1/planning/sessions/session-1/analysis/retry",
    );
    expect(planningAbandonPath("session-1")).toBe("/api/v1/planning/sessions/session-1/abandon");
    expect(() => planningSessionPath("  ")).toThrow("规划会话标识不能为空");
  });
});

describe("规划接口请求", () => {
  it("创建会话使用 POST 并只提交项目标识", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ session: sampleSession() }, { status: 201 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await createPlanningSession({ project_id: "demo-service" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(PLANNING_SESSIONS_PATH);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ project_id: "demo-service" });
    expect(data.session.id).toBe("session-1");
  });

  it("按项目列出会话时携带 project_id 查询参数", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ records: [sampleSession()], total: 1 }));
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await fetchPlanningSessions("demo service");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`${PLANNING_SESSIONS_PATH}?project_id=demo%20service`);
    expect(init.method ?? "GET").toBe("GET");
    expect(data.records).toHaveLength(1);
    expect(() => fetchPlanningSessions("  ")).toThrow("查询规划会话必须指定项目标识");
  });

  it("查询会话详情、消息与方案版本使用各自的 GET 路径", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await fetchPlanningSession("session-1");
    await fetchPlanningMessages("session-1");
    await fetchPlanningRevisions("session-1");

    const requests = fetchMock.mock.calls.map((call) => call as unknown as [string, RequestInit]);
    expect(requests.map((request) => request[0])).toEqual([
      "/api/v1/planning/sessions/session-1",
      "/api/v1/planning/sessions/session-1/messages",
      "/api/v1/planning/sessions/session-1/revisions",
    ]);
    expect(requests.map((request) => request[1].method ?? "GET")).toEqual(["GET", "GET", "GET"]);
  });

  it("提交用户消息使用 POST 且只发送正文", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        { message: sampleMessage(), session: sampleSession({ status: "analyzing" }) },
        {
          status: 201,
        },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await submitPlanningMessage("session-1", { content: "请实现需求工作台" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/planning/sessions/session-1/messages");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ content: "请实现需求工作台" });
    expect(data.session.status).toBe("analyzing");
  });

  it("分析请求携带可选执行节点并返回持久化后的版本", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleAnalysisResponse()));
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await runPlanningAnalysis("session-1", { host_id: "host-a" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/planning/sessions/session-1/analysis");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ host_id: "host-a" });
    expect(data.revision.revision_number).toBe(1);
  });

  it("没有可用执行节点时不发送 host_id", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleAnalysisResponse()));
    vi.stubGlobal("fetch", fetchMock);

    await runPlanningAnalysis("session-1");

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ host_id: "" });
  });

  it("显式重试只发送非空字段", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleAnalysisResponse()));
    vi.stubGlobal("fetch", fetchMock);

    await retryPlanningAnalysis("session-1", {});
    await retryPlanningAnalysis("session-1", { content: "补充一条约束", host_id: " host-a " });

    const first = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(first[0]).toBe("/api/v1/planning/sessions/session-1/analysis/retry");
    expect(first[1].method).toBe("POST");
    expect(JSON.parse(first[1].body as string)).toEqual({});

    const second = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    const secondInit = second[1];
    expect(JSON.parse(secondInit.body as string)).toEqual({
      content: "补充一条约束",
      host_id: "host-a",
    });
  });

  it("放弃规划使用 POST 且不带请求体", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ session: sampleSession({ status: "abandoned" }) }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await abandonPlanningSession("session-1");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/planning/sessions/session-1/abandon");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(data.session.status).toBe("abandoned");
  });
});

describe("长时间分析请求", () => {
  /**
   * 统一请求层的默认超时是 10 秒，而两阶段分析可能持续数十分钟。
   * 这里推进 30 分钟虚拟时间，验证请求没有被客户端超时中断。
   */
  it("分析请求越过默认超时后仍未被中断", async () => {
    vi.useFakeTimers();
    let captured: RequestInit | undefined;
    let settle: ((response: Response) => void) | undefined;
    const fetchMock = vi.fn((_url: string, init: RequestInit) => {
      captured = init;
      return new Promise<Response>((resolve) => {
        settle = resolve;
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const pending = runPlanningAnalysis("session-1", { host_id: "host-a" });
    await vi.advanceTimersByTimeAsync(30 * 60 * 1000);

    const signal = captured?.signal as AbortSignal | undefined;
    expect(signal?.aborted).toBe(false);

    settle?.(jsonResponse(sampleAnalysisResponse()));
    const { data } = await pending;
    expect(data.session.status).toBe("preview_ready");
  });

  it("重试请求越过默认超时后仍未被中断", async () => {
    vi.useFakeTimers();
    let captured: RequestInit | undefined;
    let settle: ((response: Response) => void) | undefined;
    const fetchMock = vi.fn((_url: string, init: RequestInit) => {
      captured = init;
      return new Promise<Response>((resolve) => {
        settle = resolve;
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const pending = retryPlanningAnalysis("session-1");
    await vi.advanceTimersByTimeAsync(30 * 60 * 1000);

    const signal = captured?.signal as AbortSignal | undefined;
    expect(signal?.aborted).toBe(false);

    settle?.(jsonResponse(sampleAnalysisResponse()));
    await expect(pending).resolves.toBeDefined();
  });
});

describe("规划接口错误分类", () => {
  it("409 保留为状态冲突而不是内部错误", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        { error: { code: "state_conflict", message: "该会话已有分析在执行" } },
        { status: 409 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const error = await runPlanningAnalysis("session-1").catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).kind).toBe("http");
    expect((error as ApiError).status).toBe(409);
    expect((error as ApiError).code).toBe("state_conflict");
  });

  it("500 与 503 保留服务端错误代码", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse(
          { error: { code: "execution_not_persisted", message: "结果未落库" } },
          { status: 500 },
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          { error: { code: "internal_error", message: "规划服务未装配" } },
          {
            status: 503,
          },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    const serverError = await fetchPlanningSession("session-1").catch((cause: unknown) => cause);
    const unavailable = await fetchPlanningSession("session-1").catch((cause: unknown) => cause);

    expect((serverError as ApiError).status).toBe(500);
    expect((serverError as ApiError).code).toBe("execution_not_persisted");
    expect((unavailable as ApiError).status).toBe(503);
    expect((unavailable as ApiError).code).toBe("internal_error");
  });

  it("连接失败归类为 network，不当作服务端业务失败", async () => {
    const fetchMock = vi.fn(async () => {
      throw new TypeError("fetch failed");
    });
    vi.stubGlobal("fetch", fetchMock);

    const error = await fetchPlanningSessions("demo-service").catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).kind).toBe("network");
    expect((error as ApiError).status).toBeUndefined();
  });
});

describe("会话状态展示", () => {
  it("已知状态映射为中文，未知状态原样保留", () => {
    expect(planningStatusLabel("preview_ready")).toBe("方案已生成");
    expect(planningStatusLabel("analyzing")).toBe("分析中");
    expect(planningStatusLabel("paused_unknown")).toBe("paused_unknown");
    expect(planningStatusColor("failed")).toBe("error");
    expect(planningStatusColor("paused_unknown")).toBe("default");
    expect(isKnownPlanningStatus("preview_ready")).toBe(true);
    expect(isKnownPlanningStatus("paused_unknown")).toBe(false);
  });

  it("操作可用性遵循服务端状态机的对外行为", () => {
    expect(planningCapabilities({ running: false, status: "active" })).toMatchObject({
      canAbandon: true,
      canAcceptMessage: true,
      canResumeAnalysis: false,
      writable: true,
    });
    expect(planningCapabilities({ running: false, status: "preview_ready" })).toMatchObject({
      canAbandon: true,
      canAcceptMessage: true,
      canResumeAnalysis: false,
    });
    expect(planningCapabilities({ running: false, status: "analyzing" })).toMatchObject({
      canAbandon: false,
      canAcceptMessage: false,
      canResumeAnalysis: true,
    });
    expect(planningCapabilities({ running: false, status: "failed" })).toMatchObject({
      canAbandon: false,
      canAcceptMessage: false,
      canResumeAnalysis: true,
    });
    for (const status of ["confirmed", "abandoned"]) {
      expect(planningCapabilities({ running: false, status })).toMatchObject({
        canAbandon: false,
        canAcceptMessage: false,
        canResumeAnalysis: false,
        writable: false,
      });
    }
    expect(planningCapabilities({ running: false, status: "paused_unknown" })).toMatchObject({
      canAcceptMessage: false,
      canResumeAnalysis: false,
      known: false,
      writable: false,
    });
    // 本页面持有在途请求时，所有动作都让位给这次请求
    expect(planningCapabilities({ running: true, status: "active" })).toMatchObject({
      canAbandon: false,
      canAcceptMessage: false,
    });
  });

  it("阶段说明区分在途请求与服务端 analyzing", () => {
    expect(describePlanningStage({ running: true, status: "analyzing" }).key).toBe("running");
    expect(describePlanningStage({ running: false, status: "analyzing" }).key).toBe(
      "server_analyzing",
    );
    expect(describePlanningStage({ running: false, status: "active" }).key).toBe("active");
    expect(describePlanningStage({ running: false, status: "preview_ready" }).key).toBe(
      "preview_ready",
    );
    const failed = describePlanningStage({
      errorSummary: "需求分析师调用超时",
      running: false,
      status: "failed",
    });
    expect(failed.key).toBe("failed");
    expect(failed.detail).toContain("需求分析师调用超时");
    const unknown = describePlanningStage({ running: false, status: "paused_unknown" });
    expect(unknown.key).toBe("unknown");
    expect(unknown.label).toBe("paused_unknown");
    expect(unknown.detail).toContain("paused_unknown");
  });
});
describe("动作错误说明", () => {
  it("网络中断与超时标记为结果不确定", () => {
    const network = describePlanningActionError(new ApiError("network", "无法连接 AgentFlow 服务"));
    expect(network.uncertain).toBe(true);
    expect(network.message).toContain("无法确认本轮分析是否已在服务端完成");
    expect(describePlanningActionError(new ApiError("timeout", "超时")).uncertain).toBe(true);
    expect(describePlanningActionError(new ApiError("aborted", "取消")).uncertain).toBe(true);
  });

  it("409 与 503 分别标记为冲突与服务不可用", () => {
    const conflict = describePlanningActionError(
      new ApiError("http", "该会话已有分析在执行", { code: "state_conflict", status: 409 }),
    );
    expect(conflict.conflict).toBe(true);
    expect(conflict.uncertain).toBe(false);
    expect(conflict.message).toContain("409");

    const unavailable = describePlanningActionError(
      new ApiError("http", "规划服务未装配", { code: "internal_error", status: 503 }),
    );
    expect(unavailable.unavailable).toBe(true);
    expect(unavailable.message).toContain("503");
  });

  it("其他服务端错误保留原始提示", () => {
    const facts = describePlanningActionError(
      new ApiError("http", "需求分析师输出非法", { code: "execution_failed", status: 500 }),
    );
    expect(facts).toEqual({
      conflict: false,
      message: "需求分析师输出非法",
      uncertain: false,
      unavailable: false,
    });
  });

  it("查询错误按类别给出可读说明", () => {
    expect(describePlanningQueryError(new ApiError("network", "x"))).toContain("无法连接");
    expect(describePlanningQueryError(new ApiError("timeout", "x"))).toContain("超时");
    expect(describePlanningQueryError(new Error("服务端拒绝"))).toBe("服务端拒绝");
  });
});

describe("消息展示", () => {
  it("发送方标签区分两个角色，未知取值原样保留", () => {
    expect(planningSenderLabel("requirement_analyst")).toBe("需求分析师");
    expect(planningSenderLabel("planner")).toBe("方案规划师");
    expect(planningSenderLabel("auditor")).toBe("auditor");
    expect(planningSenderColor("planner")).toBe("purple");
    expect(planningSenderColor("auditor")).toBe("default");
  });

  it("按序号排序，空白输入被识别", () => {
    const messages = [
      sampleMessage({ id: "b", sequence: 2 }),
      sampleMessage({ id: "a", sequence: 1 }),
    ];
    expect(sortPlanningMessages(messages).map((message) => message.id)).toEqual(["a", "b"]);
    expect(isBlankPlanningInput("   \n ")).toBe(true);
    expect(isBlankPlanningInput("需求")).toBe(false);
  });
});

describe("版本历史", () => {
  const revisions = [
    sampleRevision({ id: "revision-2", revision_number: 2 }),
    sampleRevision({ id: "revision-1", revision_number: 1 }),
  ];

  it("按版本号排序并给出最新版本", () => {
    expect(sortPlanningRevisions(revisions).map((revision) => revision.id)).toEqual([
      "revision-1",
      "revision-2",
    ]);
    expect(latestPlanningRevision(revisions)?.id).toBe("revision-2");
    expect(latestPlanningRevision([])).toBeUndefined();
    expect(planningRevisionLabel(revisions[0])).toBe("第 2 版");
  });

  it("默认选中最新版本，新版本出现时自动跟随", () => {
    expect(resolvePlanningRevisionSelection({ revisions, selectedID: undefined })).toBe(
      "revision-2",
    );
    expect(resolvePlanningRevisionSelection({ revisions, selectedID: "revision-1" })).toBe(
      "revision-1",
    );
    expect(
      resolvePlanningRevisionSelection({
        previousLatestID: "revision-1",
        revisions,
        selectedID: "revision-1",
      }),
    ).toBe("revision-2");
    expect(resolvePlanningRevisionSelection({ revisions, selectedID: "removed" })).toBe(
      "revision-2",
    );
    expect(resolvePlanningRevisionSelection({ revisions: [] })).toBeUndefined();
  });
});

describe("需求分析预览字段", () => {
  it("八个字段按固定顺序展示，空列表如实保留", () => {
    const sections = requirementAnalysisSections(sampleAnalysis({ goals: [] }));
    expect(sections.map((section) => section.title)).toEqual([
      "需求摘要",
      "目标",
      "项目现状",
      "本轮范围",
      "非目标",
      "约束条件",
      "风险",
      "验收标准",
    ]);
    expect(sections[0]).toMatchObject({
      kind: "text",
      text: "把需求提出变成一次可多轮调整的对话。",
    });
    expect(sections[1].items).toEqual([]);
    expect(sections[7].items).toEqual(["全部字段可见"]);
  });

  it("没有快照时不展示任何区块", () => {
    expect(requirementAnalysisSections(undefined)).toEqual([]);
    expect(requirementAnalysisSections(null)).toEqual([]);
  });
});

describe("实施方案预览字段", () => {
  it("草案任务按原始顺序生成表格行", () => {
    const rows = implementationPlanTaskRows(samplePlan());
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      acceptance_criteria: ["接口可用"],
      depends_on: [],
      order: 1,
      task_id: "task-1",
      title: "接入接口",
    });
    expect(rows[1].depends_on).toEqual(["task-1"]);
    expect(rows[1].order).toBe(2);
  });

  it("缺少方案或依赖为空时给出中性说明", () => {
    expect(implementationPlanTaskRows(undefined)).toEqual([]);
    expect(implementationPlanTaskRows(null)).toEqual([]);
    expect(planningTaskDependencyText([])).toBe("无前置依赖");
    expect(planningTaskDependencyText(["task-1", "task-2"])).toBe("task-1 → task-2");
  });
});

describe("执行节点判定", () => {
  it("会话已绑定的节点优先，不允许改指", () => {
    expect(
      resolvePlanningHost({
        hostsStatus: "ready",
        projectDefaultHostID: "host-default",
        selectedHostID: "host-selected",
        sessionHostID: "host-bound",
      }),
    ).toEqual({ hostID: "host-bound", requiresSelection: false, source: "session" });
  });

  it("没有会话绑定时优先使用项目默认节点", () => {
    expect(
      resolvePlanningHost({
        hostsStatus: "ready",
        projectDefaultHostID: "host-default",
        selectedHostID: "host-selected",
      }),
    ).toEqual({ hostID: "host-default", requiresSelection: false, source: "project_default" });
  });

  it("项目没有默认节点时只使用用户显式选择", () => {
    expect(resolvePlanningHost({ hostsStatus: "ready", selectedHostID: "host-selected" })).toEqual({
      hostID: "host-selected",
      requiresSelection: false,
      source: "selected",
    });
    const missing = resolvePlanningHost({ hostsStatus: "ready" });
    expect(missing.source).toBe("missing");
    expect(missing.requiresSelection).toBe(true);
    expect(missing.hostID).toBeUndefined();
    expect(missing.blockedReason).toContain("不会自动选择节点");
  });

  it("节点查询中或失败时给出不同原因", () => {
    expect(resolvePlanningHost({ hostsStatus: "loading" }).blockedReason).toContain("正在查询");
    expect(resolvePlanningHost({ hostsStatus: "error" }).blockedReason).toContain("查询失败");
  });
});

describe("需求输入区可用性", () => {
  it("未选择项目或会话时禁止提交", () => {
    expect(
      describePlanningComposerState({
        hasProject: false,
        hasSession: false,
        running: false,
        status: "",
      }),
    ).toMatchObject({
      disabled: true,
      reason: "请先选择项目：规划会话与需求消息都必须归属于一个真实项目。",
    });
    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: false,
        running: false,
        status: "",
      }),
    ).toMatchObject({ disabled: true, reason: "请先创建或选择一个规划会话，再提交需求。" });
  });

  it("分析中、失败与终态分别给出原因", () => {
    const running = describePlanningComposerState({
      hasProject: true,
      hasSession: true,
      running: true,
      status: "analyzing",
    });
    expect(running.disabled).toBe(true);
    expect(running.reason).toContain("正在执行本轮分析");

    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: true,
        running: false,
        status: "analyzing",
      }).reason,
    ).toContain("本轮收敛之前不接受新的需求消息");
    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: true,
        running: false,
        status: "failed",
      }).reason,
    ).toContain("请先显式重试");
    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: true,
        running: false,
        status: "confirmed",
      }).reason,
    ).toContain("只读展示");
    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: true,
        running: false,
        status: "paused_unknown",
      }).reason,
    ).toContain("paused_unknown");
  });

  it("执行节点未就绪时阻止提交，就绪后允许提交", () => {
    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: true,
        hostBlockedReason: "该项目没有默认执行节点",
        running: false,
        status: "active",
      }),
    ).toEqual({
      disabled: true,
      reason: "尚未确定本轮分析使用的执行节点：请先按上方提示选择或配置执行节点。",
    });
    expect(
      describePlanningComposerState({
        hasProject: true,
        hasSession: true,
        running: false,
        status: "preview_ready",
      }),
    ).toEqual({ disabled: false });
  });
});
