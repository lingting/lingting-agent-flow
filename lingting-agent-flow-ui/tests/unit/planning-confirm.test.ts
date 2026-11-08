import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  confirmPlanningSession,
  fetchProjectRequirements,
  fetchRequirement,
  planningConfirmPath,
  projectRequirementsPath,
  REQUIREMENTS_PATH,
  requirementPath,
  type ConfirmPlanningSessionResponse,
  type ConfirmedRequirement,
  type ConfirmedTask,
  type ImplementationPlan,
  type PlanningRevision,
  type RequirementAnalysis,
} from "../../src/lib/api";
import {
  confirmedTaskRows,
  describePlanningConfirmAvailability,
  describePlanningConfirmError,
  describePlanningConfirmSummary,
  draftRequirementFromAnalysis,
  findSessionRequirement,
  resolveConfirmHost,
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
    acceptance_criteria: ["正式需求可见"],
    constraints: ["同一事务内写入"],
    current_state: ["已有规划会话"],
    goals: ["生成正式需求与任务"],
    out_of_scope: ["不做调度"],
    risks: ["事务边界容易写错"],
    scope: ["internal/app"],
    summary: "补齐方案确认",
    ...overrides,
  };
}

/** 实施方案快照 */
function samplePlan(count = 2): ImplementationPlan {
  const tasks = Array.from({ length: count }, (_value, index) => ({
    acceptance_criteria: [`任务 ${index + 1} 可以独立验收`],
    depends_on: index === 0 ? [] : [`task-${index}`],
    description: `实现第 ${index + 1} 部分`,
    id: `task-${index + 1}`,
    title: `任务 ${index + 1}`,
  }));
  return { summary: "按阶段推进", tasks };
}

/** 方案版本 */
function sampleRevision(overrides: Partial<PlanningRevision> = {}): PlanningRevision {
  return {
    created_at: "2026-11-08T02:00:00Z",
    id: "revision-2",
    implementation_plan: samplePlan(),
    planning_session_id: "session-1",
    requirement_analysis: sampleAnalysis(),
    revision_number: 2,
    snapshot_version: 1,
    ...overrides,
  };
}

/** 正式需求 */
function sampleRequirement(overrides: Partial<ConfirmedRequirement> = {}): ConfirmedRequirement {
  return {
    created_at: "2026-11-08T02:20:00Z",
    description: "补齐方案确认",
    id: "requirement-1",
    planning_session_id: "session-1",
    priority: "normal",
    project_id: "demo-project",
    source: "manual",
    status: "queued",
    title: "补齐方案确认",
    updated_at: "2026-11-08T02:20:00Z",
    ...overrides,
  };
}

/** 正式任务 */
function sampleTask(sequence: number, overrides: Partial<ConfirmedTask> = {}): ConfirmedTask {
  return {
    acceptance_criteria: ["可以独立验收"],
    base_ref: "main",
    created_at: `2026-11-08T02:2${sequence}:00Z`,
    goal: `任务 ${sequence}\n\n实现第 ${sequence} 部分`,
    id: `task-id-${sequence}`,
    repository_path: "ssh://git.example.com/team/repo",
    requirement_id: "requirement-1",
    requirement_sequence: sequence,
    status: "queued",
    ...overrides,
  };
}

/** 一份确认结果 */
function sampleConfirmResponse(): ConfirmPlanningSessionResponse {
  return {
    adopted_revision_id: "revision-2",
    requirement: sampleRequirement(),
    tasks: [sampleTask(1), sampleTask(2)],
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("确认接口路径", () => {
  it("确认挂在会话子资源，需求接口使用独立路径", () => {
    expect(planningConfirmPath("session-1")).toBe("/api/v1/planning/sessions/session-1/confirm");
    expect(REQUIREMENTS_PATH).toBe("/api/v1/requirements");
    expect(requirementPath("requirement-1")).toBe("/api/v1/requirements/requirement-1");
    expect(propertyPath()).toBe("/api/v1/projects/demo-project/requirements");
    expect(() => requirementPath("  ")).toThrow("需求标识不能为空");
  });
});

/** 项目需求列表路径（便于断言）。 */
function propertyPath() {
  return projectRequirementsPath("demo-project");
}

describe("确认接口请求", () => {
  it("只提交 revision_id 与可选 host_id", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleConfirmResponse(), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    const { data } = await confirmPlanningSession("session-1", { revision_id: "revision-2" });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/planning/sessions/session-1/confirm");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ revision_id: "revision-2" });
    expect(data.tasks).toHaveLength(2);
    expect(data.requirement.status).toBe("queued");
  });

  it("显式给出执行节点时一并提交，空白节点不提交", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleConfirmResponse(), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await confirmPlanningSession("session-1", { host_id: " host-1 ", revision_id: "revision-2" });
    const [, withHost] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(withHost.body as string)).toEqual({
      host_id: "host-1",
      revision_id: "revision-2",
    });

    await confirmPlanningSession("session-1", { host_id: "  ", revision_id: "revision-2" });
    const [, withoutHost] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(JSON.parse(withoutHost.body as string)).toEqual({ revision_id: "revision-2" });
  });

  it("需求详情与项目需求列表使用只读 GET", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ requirement: sampleRequirement(), tasks: [sampleTask(1)] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const detail = await fetchRequirement("requirement-1");
    expect(detail.data.requirement.id).toBe("requirement-1");
    expect(detail.data.tasks[0]?.requirement_sequence).toBe(1);

    fetchMock.mockResolvedValueOnce(jsonResponse({ records: [sampleRequirement()], total: 1 }));
    const list = await fetchProjectRequirements("demo-project");
    expect(list.data.records[0]?.planning_session_id).toBe("session-1");
    const [url, init] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/projects/demo-project/requirements");
    expect(init.method ?? "GET").toBe("GET");
  });
});

describe("确认前的草案摘要", () => {
  it("标题取分析摘要，描述由目标、范围、约束与验收标准组成", () => {
    const draft = draftRequirementFromAnalysis(sampleAnalysis());
    expect(draft.title).toBe("补齐方案确认");
    expect(draft.description).toContain("目标：");
    expect(draft.description).toContain("- 生成正式需求与任务");
    expect(draft.description).toContain("范围：");
    expect(draft.description).toContain("- internal/app");
    expect(draft.description).toContain("约束：");
    expect(draft.description).toContain("- 同一事务内写入");
    expect(draft.description).toContain("验收标准：");
    expect(draft.description).toContain("- 正式需求可见");
  });

  it("确认摘要展示版本号、任务数量与顺序", () => {
    const summary = describePlanningConfirmSummary(
      sampleRevision({ implementation_plan: samplePlan(3) }),
    );
    expect(summary).toBeDefined();
    expect(summary?.revisionLabel).toBe("第 2 版");
    expect(summary?.revisionID).toBe("revision-2");
    expect(summary?.taskCount).toBe(3);
    expect(summary?.tasks.map((task) => task.order)).toEqual([1, 2, 3]);
    expect(summary?.tasks[1]?.depends_on).toEqual(["task-1"]);
  });

  it("没有选中版本时没有摘要", () => {
    expect(describePlanningConfirmSummary(undefined)).toBeUndefined();
  });
});

describe("确认可用性", () => {
  const base = {
    confirming: false,
    hasSession: true,
    running: false,
    selectedRevisionID: "revision-2",
    status: "preview_ready",
  };

  it("未选会话或未选版本时不允许确认", () => {
    expect(describePlanningConfirmAvailability({ ...base, hasSession: false })).toEqual({
      available: false,
      reason: "请先创建或选择一个规划会话。",
    });
    expect(describePlanningConfirmAvailability({ ...base, selectedRevisionID: "  " })).toEqual({
      available: false,
      reason: "请先在方案预览中选择要采纳的方案版本。",
    });
  });

  it("在途确认与分析时不允许重复操作", () => {
    expect(describePlanningConfirmAvailability({ ...base, confirming: true }).available).toBe(
      false,
    );
    expect(describePlanningConfirmAvailability({ ...base, running: true }).available).toBe(false);
  });

  it("只有 preview_ready 允许确认，其他状态给出原因", () => {
    expect(describePlanningConfirmAvailability(base)).toEqual({ available: true });
    for (const status of ["active", "analyzing", "confirmed", "abandoned", "failed"]) {
      const result = describePlanningConfirmAvailability({ ...base, status });
      expect(result.available).toBe(false);
      expect(result.reason).toBeTruthy();
    }
    expect(
      describePlanningConfirmAvailability({ ...base, status: "unknown_status" }).reason,
    ).toContain("unknown_status");
  });

  it("执行节点未就绪时阻止确认", () => {
    expect(
      describePlanningConfirmAvailability({ ...base, hostBlockedReason: "该项目没有默认执行节点" }),
    ).toEqual({ available: false, reason: "该项目没有默认执行节点" });
  });
});

describe("确认使用的执行节点", () => {
  it("显式选择优先，其次项目默认节点", () => {
    expect(
      resolveConfirmHost({
        hostsStatus: "ready",
        projectDefaultHostID: "host-default",
        selectedHostID: "host-selected",
      }).hostID,
    ).toBe("host-selected");
    expect(
      resolveConfirmHost({ hostsStatus: "ready", projectDefaultHostID: "host-default" }),
    ).toEqual({});
  });

  it("两者都没有时不自动选择节点", () => {
    const missing = resolveConfirmHost({ hostsStatus: "ready" });
    expect(missing.hostID).toBeUndefined();
    expect(missing.blockedReason).toContain("不会自动选择节点");
    expect(resolveConfirmHost({ hostsStatus: "loading" }).blockedReason).toContain(
      "正在查询执行节点",
    );
    expect(resolveConfirmHost({ hostsStatus: "error" }).blockedReason).toContain(
      "执行节点查询失败",
    );
  });
});

describe("确认失败说明", () => {
  it("409 表示状态冲突，412 表示前置条件不满足", () => {
    const conflict = describePlanningConfirmError(new ApiError("http", "冲突", { status: 409 }));
    expect(conflict.conflict).toBe(true);
    expect(conflict.uncertain).toBe(false);
    expect(conflict.message).toContain("不要重复确认");

    const prerequisite = describePlanningConfirmError(
      new ApiError("http", "前置条件", { status: 412 }),
    );
    expect(prerequisite.conflict).toBe(false);
    expect(prerequisite.message).toContain("没有生成任何正式实体");
  });

  it("网络中断时结果不确定，不当作业务失败", () => {
    const uncertain = describePlanningConfirmError(new ApiError("network", "连接中断"));
    expect(uncertain.uncertain).toBe(true);
    expect(uncertain.message).toContain("不会自动重放确认");
  });

  it("503 表示服务不可用", () => {
    const unavailable = describePlanningConfirmError(
      new ApiError("http", "不可用", { status: 503 }),
    );
    expect(unavailable.unavailable).toBe(true);
  });
});

describe("正式任务展示", () => {
  it("按 requirement_sequence 升序排列，状态保持服务端原值", () => {
    const rows = confirmedTaskRows([sampleTask(2), sampleTask(1)]);
    expect(rows.map((row) => row.order)).toEqual([1, 2]);
    expect(rows.every((row) => row.status === "queued")).toBe(true);
    expect(rows[0]?.title).toBe("任务 1");
  });

  it("没有顺序的历史任务排在后面", () => {
    const rows = confirmedTaskRows([
      sampleTask(1, {
        created_at: "2026-11-08T02:25:00Z",
        id: "task-id-9",
        requirement_sequence: undefined,
      }),
      sampleTask(1),
    ]);
    expect(rows.map((row) => row.task_id)).toEqual(["task-id-1", "task-id-9"]);
  });
});

describe("已确认会话的采纳结果", () => {
  it("按 planning_session_id 匹配需求", () => {
    const requirement = sampleRequirement();
    expect(findSessionRequirement([requirement], "session-1")).toBe(requirement);
    expect(findSessionRequirement([requirement], "session-2")).toBeUndefined();
    expect(findSessionRequirement([requirement], undefined)).toBeUndefined();
  });
});
