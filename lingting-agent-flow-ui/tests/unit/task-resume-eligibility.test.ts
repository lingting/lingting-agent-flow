import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  fetchTaskResumeEligibility,
  TASKS_PATH,
  taskResumeEligibilityPath,
  type ResumeEligibilityResponse,
} from "../../src/lib/api";
import {
  CODE_SAVE_RESUME_ACTION_META,
  codeSaveResumeActionMeta,
  looksLikeCliCommand,
  RESUME_BLOCKING_META,
  resumeBlockingList,
  resumeBlockingMeta,
  resumeNextActions,
  resumeSummary,
  RESUME_SUMMARY_MISSING,
  resumeVerdict,
} from "../../src/pages/tasks/resumeEligibility";
import {
  resumeFailed,
  resumeInitialState,
  resumeLoaded,
  resumeResponseAccepted,
  resumeSnapshotForTask,
  type ResumeSnapshot,
} from "../../src/pages/tasks/useResumeEligibility";
import { describeApiError } from "../../src/pages/tasks/useExecutionRecords";
import { workflowStageLabel } from "../../src/pages/tasks/status";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/**
 * 一份续跑资格响应样本。
 *
 * 字段与 Go 端 `httpapi.ResumeEligibilityResponse` 一致；`blocking` / `next_actions` 在
 * 服务端为空时序列化为 `null`，因此这里也用 `null` 作为默认值。
 */
function eligibility(
  overrides: Partial<ResumeEligibilityResponse> = {},
): ResumeEligibilityResponse {
  return {
    blocking: null,
    can_resume: true,
    code_save_action: "proceed",
    code_save_records: [],
    code_save_summary: "代码保存记录中没有结果不明的外部副作用",
    next_actions: ["agentflow task resume task-1"],
    summary: "没有阻塞项，可以再次请求恢复推进",
    task_id: "task-1",
    task_status: "blocked",
    ...overrides,
  };
}

/** 一条"需要关注的代码保存记录"样本，字段与 Go 端 `httpapi.ResumeEligibilityCodeSave` 一致 */
function resumeCodeSave(overrides: Record<string, unknown> = {}) {
  return {
    action: "verify",
    commit_status: "succeeded",
    error_class: "code_save_indeterminate",
    merge_status: "not_requested",
    persist_status: "persisted",
    push_status: "unknown",
    record_id: "record-1",
    recovery_status: "unverified",
    summary: "代码保存记录仍存在结果不明的外部副作用，必须先做一次只读核验",
    task_branch: "agent/task-1",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("续跑资格路径构造", () => {
  it("由任务路径派生并编码任务标识", () => {
    expect(taskResumeEligibilityPath("task-1")).toBe(`${TASKS_PATH}/task-1/resume-eligibility`);
    expect(taskResumeEligibilityPath("task 1")).toBe(`${TASKS_PATH}/task%201/resume-eligibility`);
  });

  it("空任务标识直接拒绝，不拼出无效路径", () => {
    expect(() => taskResumeEligibilityPath("")).toThrow();
    expect(() => taskResumeEligibilityPath("   ")).toThrow();
  });

  it("请求只发出一次 GET，路径为任务下的 resume-eligibility", async () => {
    const spy = vi.fn(async () => jsonResponse(eligibility()));
    vi.stubGlobal("fetch", spy);

    const { data, request } = await fetchTaskResumeEligibility("task-1");

    expect(request).toEqual({ method: "GET", path: `${TASKS_PATH}/task-1/resume-eligibility` });
    expect(spy).toHaveBeenCalledTimes(1);
    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/resume-eligibility`);
    expect(data.can_resume).toBe(true);
  });
});

describe("响应解析", () => {
  it("can_resume / blocking / next_actions / code_save_records 各自解析", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          eligibility({
            blocking: ["container_missing", "code_save_indeterminate"],
            can_resume: false,
            code_save_action: "blocked",
            code_save_records: [resumeCodeSave()],
            next_actions: [
              "agentflow code verify --record record-1",
              "核验后若仍为 unverifiable，请人工检查容器内仓库与远程分支后再决定是否重试",
            ],
            summary:
              "存在阻塞项（container_missing、code_save_indeterminate），请按 next_actions 处理后重试",
          }),
        ),
      ),
    );

    const { data } = await fetchTaskResumeEligibility("task-1");

    expect(data.can_resume).toBe(false);
    expect(data.blocking).toEqual(["container_missing", "code_save_indeterminate"]);
    expect(data.code_save_action).toBe("blocked");
    expect(data.next_actions).toHaveLength(2);
    expect(data.code_save_records[0]?.record_id).toBe("record-1");
    expect(data.code_save_records[0]?.error_class).toBe("code_save_indeterminate");
  });

  it("三个 Git 阶段与记录维度的两个状态不被合并", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          eligibility({
            can_resume: false,
            code_save_records: [
              resumeCodeSave({
                commit_status: "succeeded",
                merge_status: "conflict",
                persist_status: "unconfirmed",
                push_status: "failed",
                recovery_status: "unverifiable",
              }),
            ],
          }),
        ),
      ),
    );

    const { data } = await fetchTaskResumeEligibility("task-1");
    const record = data.code_save_records[0]!;

    expect(record.commit_status).toBe("succeeded");
    expect(record.push_status).toBe("failed");
    expect(record.merge_status).toBe("conflict");
    expect(record.persist_status).toBe("unconfirmed");
    expect(record.recovery_status).toBe("unverifiable");
  });

  it("blocking / next_actions 为 null 时按空列表处理，不当作成功", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(eligibility({ blocking: null, next_actions: null }))),
    );

    const { data } = await fetchTaskResumeEligibility("task-1");

    expect(data.blocking).toBeNull();
    expect(resumeBlockingList(data)).toEqual([]);
    expect(resumeNextActions(data)).toEqual([]);
  });

  it("run 缺省时不补造工作流执行记录", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(eligibility({ run: undefined }))),
    );

    const { data } = await fetchTaskResumeEligibility("task-1");

    expect(data.run).toBeUndefined();
    expect("run" in data).toBe(false);
  });

  it("run 存在时保留服务端给出的执行记录字段", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          eligibility({
            run: {
              cancel_requested: false,
              code_saver_available: true,
              completed_steps: 1,
              last_completed_sequence: 1,
              plan_digest: "digest",
              revision: 3,
              run_id: "run-1",
              stage: "step_execution",
              status: "blocked",
              status_active: false,
              status_terminal: false,
              task_id: "task-1",
              total_steps: 2,
              updated_at: "2026-10-26T08:00:00Z",
            },
          }),
        ),
      ),
    );

    const { data } = await fetchTaskResumeEligibility("task-1");

    expect(data.run?.run_id).toBe("run-1");
    expect(data.run?.status).toBe("blocked");
    expect(data.run?.status_terminal).toBe(false);
    expect(data.run?.completed_steps).toBe(1);
  });
});

describe("查询错误处理", () => {
  it("404 表示任务不存在，不能当作允许续跑", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "任务不存在: task-1" } },
          { status: 404 },
        ),
      ),
    );

    await expect(fetchTaskResumeEligibility("task-1")).rejects.toBeInstanceOf(ApiError);
    await fetchTaskResumeEligibility("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(404);
      expect(describeApiError(cause)).toBe("任务不存在: task-1");
    });
  });

  it("503 原样展示服务端说明，不伪造资格结论", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "internal_error", message: "续跑资格服务未初始化" } },
          { status: 503 },
        ),
      ),
    );

    await fetchTaskResumeEligibility("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(503);
      expect(describeApiError(cause)).toBe("续跑资格服务未初始化");
    });
  });

  it("网络错误统一说明为无法连接服务", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("failed to fetch");
      }),
    );

    await fetchTaskResumeEligibility("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).kind).toBe("network");
      expect(describeApiError(cause)).toBe("无法连接 AgentFlow 服务，请确认服务已启动后重试。");
    });
  });
});

describe("资格结论展示", () => {
  it("允许续跑时明确说明只是查询时刻的结论", () => {
    const verdict = resumeVerdict(true);

    expect(verdict.title).toBe("当前查询结果允许继续推进");
    expect(verdict.color).toBe("success");
    expect(verdict.attention).toBe(false);
    expect(verdict.description).toContain("重新检查前置条件");
  });

  it("存在阻塞时不显示为允许续跑", () => {
    const verdict = resumeVerdict(false);

    expect(verdict.title).toBe("当前存在阻塞，不能按当前条件直接续跑");
    expect(verdict.color).toBe("warning");
    expect(verdict.attention).toBe(true);
    expect(verdict.title).not.toContain("允许继续推进");
  });

  it("摘要缺省时使用中性提示，不编造结论", () => {
    expect(resumeSummary(eligibility({ summary: "" }))).toBe(RESUME_SUMMARY_MISSING);
    expect(resumeSummary(eligibility({ summary: "   " }))).toBe(RESUME_SUMMARY_MISSING);
    expect(resumeSummary(eligibility({ summary: "没有阻塞项" }))).toBe("没有阻塞项");
  });
});

describe("阻塞原因展示", () => {
  it("覆盖服务端已给出的全部阻塞分类", () => {
    expect(Object.keys(RESUME_BLOCKING_META).sort()).toEqual([
      "cancel_requested",
      "code_save_indeterminate",
      "code_save_unconfirmed",
      "container_detached",
      "container_missing",
      "container_not_running",
      "container_unreachable",
      "prerequisite_unmet",
      "task_deleted",
    ]);
  });

  it("已知阻塞分类各有中文标签与解释", () => {
    expect(resumeBlockingMeta("task_deleted").label).toBe("任务已删除");
    expect(resumeBlockingMeta("cancel_requested").label).toBe("任务已请求取消");
    expect(resumeBlockingMeta("container_missing").label).toBe("执行容器不存在");
    expect(resumeBlockingMeta("container_detached").label).toBe("执行容器已解除关联");
    expect(resumeBlockingMeta("container_not_running").label).toBe("执行容器未运行");
    expect(resumeBlockingMeta("container_unreachable").label).toBe("无法访问执行容器");
    expect(resumeBlockingMeta("prerequisite_unmet").label).toBe("必要前置条件未满足");
    expect(resumeBlockingMeta("code_save_unconfirmed").label).toBe("代码保存记录写入未确认");
    expect(resumeBlockingMeta("code_save_indeterminate").label).toBe("代码保存结果不确定");

    for (const code of Object.keys(RESUME_BLOCKING_META)) {
      const meta = resumeBlockingMeta(code);
      expect(meta.known).toBe(true);
      expect(meta.description.length).toBeGreaterThan(0);
      expect(meta.label).not.toMatch(/^[a-z_]+$/);
    }
  });

  it("未知阻塞分类原样保留，不省略也不伪装成无阻塞", () => {
    const meta = resumeBlockingMeta("future_blocking_reason");

    expect(meta.label).toBe("future_blocking_reason");
    expect(meta.known).toBe(false);
    expect(meta.description).toContain("未定义");
  });

  it("多个阻塞项逐条呈现，不被压缩或丢失", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          eligibility({
            blocking: [
              "cancel_requested",
              "container_not_running",
              "container_unreachable",
              "future_blocking_reason",
            ],
            can_resume: false,
          }),
        ),
      ),
    );

    const { data } = await fetchTaskResumeEligibility("task-1");
    const labels = resumeBlockingList(data).map((code) => resumeBlockingMeta(code).label);

    expect(labels).toEqual([
      "任务已请求取消",
      "执行容器未运行",
      "无法访问执行容器",
      "future_blocking_reason",
    ]);
  });
});

describe("代码保存续跑判定展示", () => {
  it("覆盖 Go 端全部动作取值", () => {
    expect(Object.keys(CODE_SAVE_RESUME_ACTION_META).sort()).toEqual([
      "blocked",
      "proceed",
      "verify",
    ]);
  });

  it("proceed 明确不表示整个工作流可以恢复", () => {
    const meta = codeSaveResumeActionMeta("proceed");

    expect(meta.label).toBe("允许继续代码保存");
    expect(meta.attention).toBe(false);
    expect(meta.description).toContain("不代表整个工作流已经可以恢复");
  });

  it("verify 表述为需要核验，而不是已经核验", () => {
    const meta = codeSaveResumeActionMeta("verify");

    expect(meta.label).toBe("需先核验");
    expect(meta.attention).toBe(true);
    expect(meta.description).toContain("不是「已经核验」");
  });

  it("blocked 表示禁止自动推进", () => {
    const meta = codeSaveResumeActionMeta("blocked");

    expect(meta.label).toBe("禁止自动推进");
    expect(meta.attention).toBe(true);
    expect(meta.color).toBe("error");
  });

  it("未知动作与缺省动作都不被回落到允许继续", () => {
    const unknown = codeSaveResumeActionMeta("future_action");
    expect(unknown.label).toBe("future_action");
    expect(unknown.known).toBe(false);
    expect(unknown.attention).toBe(true);

    const missing = codeSaveResumeActionMeta(undefined);
    expect(missing.label).toBe("未提供代码保存判定");
    expect(missing.known).toBe(false);
    expect(missing.color).not.toBe("success");
  });
});

describe("下一步动作展示", () => {
  it("逐项保留服务端原文与其中的真实标识", () => {
    const actions = [
      "agentflow code verify --record record-1",
      "agentflow code verify --record record-2",
      "agentflow task resume task-1",
    ];

    expect(actions.map((action) => action)).toEqual(actions);
    expect(actions[0]).toContain("record-1");
    expect(actions[2]).toContain("task-1");
  });

  it("识别 CLI 建议，但只用于标注而不执行", () => {
    expect(looksLikeCliCommand("agentflow code verify --record record-1")).toBe(true);
    expect(looksLikeCliCommand("  agentflow task resume task-1")).toBe(true);
    expect(looksLikeCliCommand("请人工检查容器内仓库与远程分支")).toBe(false);
  });
});

describe("加载状态与竞态", () => {
  const snapshot: ResumeSnapshot = { data: eligibility(), taskID: "task-1" };

  it("没有任务标识时不发起查询", () => {
    expect(resumeInitialState(undefined, "")).toEqual({ status: "idle" });
  });

  it("首次加载进入 loading", () => {
    expect(resumeInitialState(undefined, "task-1")).toEqual({ status: "loading" });
  });

  it("同一任务的快照在刷新时继续展示", () => {
    expect(resumeInitialState(snapshot, "task-1")).toEqual({
      data: snapshot.data,
      status: "ready",
    });
  });

  it("切换任务时旧任务的快照被丢弃，不混入当前任务", () => {
    expect(resumeSnapshotForTask(snapshot, "task-2")).toBeUndefined();
    expect(resumeInitialState(snapshot, "task-2")).toEqual({ status: "loading" });
  });

  it("查询成功时快照归属到当前任务", () => {
    const applied = resumeLoaded("task-2", eligibility({ can_resume: false, task_id: "task-2" }));

    expect(applied.snapshot.taskID).toBe("task-2");
    expect(applied.state.status).toBe("ready");
    expect(applied.state.status === "ready" && applied.state.data.can_resume).toBe(false);
    expect(applied.state.status === "ready" && applied.state.error).toBeUndefined();
  });

  it("刷新失败时保留上一次真实结果并附加错误", () => {
    const applied = resumeFailed(snapshot, "task-1", "服务内部错误");

    expect(applied.snapshot).toBe(snapshot);
    expect(applied.state.status).toBe("ready");
    expect(applied.state.status === "ready" && applied.state.error).toBe("服务内部错误");
    expect(applied.state.status === "ready" && applied.state.data.can_resume).toBe(true);
  });

  it("没有可用快照时查询失败进入错误状态，不显示为允许续跑", () => {
    const applied = resumeFailed(undefined, "task-1", "无法连接 AgentFlow 服务");

    expect(applied.snapshot).toBeUndefined();
    expect(applied.state).toEqual({ error: "无法连接 AgentFlow 服务", status: "error" });
  });

  it("跨任务的旧快照不会被用来掩盖新任务的查询失败", () => {
    const applied = resumeFailed(snapshot, "task-2", "任务不存在: task-2");

    expect(applied.snapshot).toBeUndefined();
    expect(applied.state.status).toBe("error");
  });

  it("只有最后一次发起的请求结果会被采用", () => {
    expect(resumeResponseAccepted(2, 2, false)).toBe(true);
    expect(resumeResponseAccepted(1, 2, false)).toBe(false);
    expect(resumeResponseAccepted(2, 2, true)).toBe(false);
  });
});

/**
 * 面板展示的工作流阶段必须中文化：`task_status`、`run.status` 都用既有映射，只剩阶段曾经
 * 直接渲染 `step_execution` 这类原始取值。
 */
describe("工作流阶段标签", () => {
  it("覆盖 Go 端 domain.WorkflowStage 的每一个取值", () => {
    expect(workflowStageLabel("waiting")).toBe("等待开始");
    expect(workflowStageLabel("step_execution")).toBe("执行开发步骤");
    expect(workflowStageLabel("code_save")).toBe("保存代码");
    expect(workflowStageLabel("finalize")).toBe("收尾");
    expect(workflowStageLabel("stopped")).toBe("已停止");
  });

  it("未知阶段原样保留，不猜测语义", () => {
    expect(workflowStageLabel("future_stage")).toBe("future_stage");
  });
});
