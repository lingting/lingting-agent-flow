import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  fetchTaskCodeSaves,
  TASKS_PATH,
  taskCodeSavesPath,
  type CodeSaveRecord,
  type CodeSaveRecordListResponse,
} from "../../src/lib/api";
import {
  CODE_SAVE_COMMIT_META,
  CODE_SAVE_MERGE_META,
  CODE_SAVE_PERSIST_META,
  CODE_SAVE_PUSH_META,
  CODE_SAVE_RECOVERY_META,
  CODE_SAVE_STAGE_MISSING_LABEL,
  commitStatusMeta,
  mergeRequested,
  mergeStatusMeta,
  mergeSummary,
  mergeWarning,
  persistStatusMeta,
  pushStatusMeta,
  pushSummary,
  recordNeedsAttention,
  recordWarnings,
  recoveryScope,
  recoveryStatusMeta,
  stageLabel,
} from "../../src/pages/tasks/codeSaves";
import { describeApiError } from "../../src/pages/tasks/useExecutionRecords";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/** 一条代码保存记录样本，字段与 Go 端 `httpapi.CodeSaveRecordListEntryResponse` 一致 */
function record(overrides: Partial<CodeSaveRecord> = {}): CodeSaveRecord {
  return {
    base_branch: "main",
    commit_message: "提交一",
    commit_sha: "1".repeat(40),
    commit_status: "succeeded",
    container_id: "container-1",
    created_at: "2026-10-26T08:00:00Z",
    finished_at: "2026-10-26T08:00:12Z",
    id: "record-1",
    merge_enabled: false,
    merge_status: "not_requested",
    persist_status: "persisted",
    push_status: "succeeded",
    recovery_status: "unverified",
    remote_name: "origin",
    stage: "push",
    started_at: "2026-10-26T08:00:01Z",
    task_branch: "agent/task-1",
    task_id: "task-1",
    updated_at: "2026-10-26T08:00:12Z",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("代码保存记录路径构造", () => {
  it("由任务路径派生并编码任务标识", () => {
    expect(taskCodeSavesPath("task-1")).toBe(`${TASKS_PATH}/task-1/code-saves`);
    expect(taskCodeSavesPath("task 1")).toBe(`${TASKS_PATH}/task%201/code-saves`);
  });

  it("空任务标识直接拒绝，不拼出无效路径", () => {
    expect(() => taskCodeSavesPath("")).toThrow();
    expect(() => taskCodeSavesPath("   ")).toThrow();
  });

  it("请求只发出一次 GET，路径为任务下的 code-saves", async () => {
    const payload: CodeSaveRecordListResponse = { records: [record()], total: 1 };
    const spy = vi.fn(async () => jsonResponse(payload));
    vi.stubGlobal("fetch", spy);

    const { data, request } = await fetchTaskCodeSaves("task-1");

    expect(request).toEqual({ method: "GET", path: `${TASKS_PATH}/task-1/code-saves` });
    expect(spy).toHaveBeenCalledTimes(1);
    const [target] = spy.mock.calls[0] as unknown as [string];
    expect(target).toBe(`${TASKS_PATH}/task-1/code-saves`);
    expect(data.total).toBe(1);
    expect(data.records[0]?.id).toBe("record-1");
  });
});

describe("列表响应解析", () => {
  it("保留服务端顺序，不重新排序", async () => {
    const records = [
      record({ created_at: "2026-10-26T09:00:00Z", id: "record-new" }),
      record({ created_at: "2026-10-26T08:00:00Z", id: "record-old" }),
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ records, total: 2 })),
    );

    const { data } = await fetchTaskCodeSaves("task-1");

    expect(data.records.map((item) => item.id)).toEqual(["record-new", "record-old"]);
  });

  it("records: [] 是合法结果，不是错误", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ records: [], total: 0 })),
    );

    const { data } = await fetchTaskCodeSaves("task-1");

    expect(data.records).toEqual([]);
    expect(data.total).toBe(0);
  });

  it("三阶段与记录可信度字段各自解析，不被合并", async () => {
    const payload = {
      records: [
        record({
          commit_status: "succeeded",
          merge_enabled: true,
          merge_status: "failed",
          persist_status: "unconfirmed",
          push_status: "unknown",
          recovery_status: "unverifiable",
        }),
      ],
      total: 1,
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(payload)),
    );

    const { data } = await fetchTaskCodeSaves("task-1");
    const item = data.records[0]!;

    expect(item.commit_status).toBe("succeeded");
    expect(item.push_status).toBe("unknown");
    expect(item.merge_status).toBe("failed");
    expect(item.persist_status).toBe("unconfirmed");
    expect(item.recovery_status).toBe("unverifiable");
  });
});

describe("查询错误处理", () => {
  it("404 表示任务不存在，不能当作空列表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "任务不存在: task-1" } },
          { status: 404 },
        ),
      ),
    );

    await expect(fetchTaskCodeSaves("task-1")).rejects.toBeInstanceOf(ApiError);
    await fetchTaskCodeSaves("task-1").catch((cause: unknown) => {
      expect(cause).toBeInstanceOf(ApiError);
      expect((cause as ApiError).status).toBe(404);
      expect(describeApiError(cause)).toBe("任务不存在: task-1");
    });
  });

  it("503 映射为服务端错误说明，不伪造空记录", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "internal_error", message: "代码保存记录查询服务未初始化" } },
          { status: 503 },
        ),
      ),
    );

    await fetchTaskCodeSaves("task-1").catch((cause: unknown) => {
      expect(cause).toBeInstanceOf(ApiError);
      expect((cause as ApiError).status).toBe(503);
      expect(describeApiError(cause)).toBe("代码保存记录查询服务未初始化");
    });
  });

  it("网络错误统一说明为无法连接服务", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("failed to fetch");
      }),
    );

    await fetchTaskCodeSaves("task-1").catch((cause: unknown) => {
      expect(cause).toBeInstanceOf(ApiError);
      expect((cause as ApiError).kind).toBe("network");
      expect(describeApiError(cause)).toBe("无法连接 AgentFlow 服务，请确认服务已启动后重试。");
    });
  });
});

describe("状态标签覆盖", () => {
  it("提交状态覆盖 Go 端全部取值并给出中文标签", () => {
    expect(Object.keys(CODE_SAVE_COMMIT_META).sort()).toEqual([
      "failed",
      "no_changes",
      "not_attempted",
      "pending",
      "succeeded",
      "unknown",
    ]);
    expect(commitStatusMeta("pending").label).toBe("等待提交");
    expect(commitStatusMeta("not_attempted").label).toBe("未尝试提交");
    expect(commitStatusMeta("no_changes").label).toBe("没有可提交的变更");
    expect(commitStatusMeta("succeeded").label).toBe("提交成功");
    expect(commitStatusMeta("failed").label).toBe("提交失败");
    expect(commitStatusMeta("unknown").label).toBe("提交结果不确定，需核验");
  });

  it("推送状态覆盖 Go 端全部取值并给出中文标签", () => {
    expect(Object.keys(CODE_SAVE_PUSH_META).sort()).toEqual([
      "conflict",
      "failed",
      "not_attempted",
      "pending",
      "succeeded",
      "unknown",
    ]);
    expect(pushStatusMeta("pending").label).toBe("等待推送");
    expect(pushStatusMeta("not_attempted").label).toBe("未尝试推送");
    expect(pushStatusMeta("conflict").label).toBe("远程存在冲突");
    expect(pushStatusMeta("unknown").label).toBe("推送结果不确定，需核验");
  });

  it("合并状态覆盖 Go 端全部取值并给出中文标签", () => {
    expect(Object.keys(CODE_SAVE_MERGE_META).sort()).toEqual([
      "conflict",
      "failed",
      "not_requested",
      "pending",
      "skipped",
      "succeeded",
      "unknown",
    ]);
    expect(mergeStatusMeta("not_requested").label).toBe("未请求合并");
    expect(mergeStatusMeta("skipped").label).toBe("未执行合并");
    expect(mergeStatusMeta("conflict").label).toBe("合并冲突");
  });

  it("记录写入状态与核验结论各自独立", () => {
    expect(Object.keys(CODE_SAVE_PERSIST_META).sort()).toEqual(["persisted", "unconfirmed"]);
    expect(Object.keys(CODE_SAVE_RECOVERY_META).sort()).toEqual([
      "confirmed_applied",
      "confirmed_not_applied",
      "unverifiable",
      "unverified",
    ]);
    expect(persistStatusMeta("unconfirmed").label).toBe("记录写入未确认");
    expect(recoveryStatusMeta("unverified").label).toBe("尚未核验");
    expect(recoveryStatusMeta("unverifiable").label).toBe("无法取得可信结论");
    expect(recoveryStatusMeta("confirmed_applied").label).toBe("已确认操作发生");
    expect(recoveryStatusMeta("confirmed_not_applied").label).toBe("已确认操作未发生");
  });

  it("非成功状态一律不被标成成功色", () => {
    for (const value of ["pending", "not_attempted", "no_changes"] as const) {
      expect(commitStatusMeta(value).color).not.toBe("success");
    }
    for (const value of ["pending", "not_attempted"] as const) {
      expect(pushStatusMeta(value).color).not.toBe("success");
    }
    for (const value of ["pending", "not_requested", "skipped"] as const) {
      expect(mergeStatusMeta(value).color).not.toBe("success");
    }
  });

  it("unknown / conflict / failed 都标记为需要关注", () => {
    for (const value of ["unknown", "failed"] as const) {
      expect(commitStatusMeta(value).attention).toBe(true);
    }
    for (const value of ["unknown", "failed", "conflict"] as const) {
      expect(pushStatusMeta(value).attention).toBe(true);
      expect(mergeStatusMeta(value).attention).toBe(true);
    }
  });
});

describe("未知与缺失取值", () => {
  it("未知状态原样展示且不被误报为成功", () => {
    const unknown = commitStatusMeta("future_commit_state");
    expect(unknown.label).toBe("future_commit_state");
    expect(unknown.known).toBe(false);
    expect(unknown.color).not.toBe("success");

    expect(pushStatusMeta("future_push_state").label).toBe("future_push_state");
    expect(mergeStatusMeta("future_merge_state").label).toBe("future_merge_state");
    expect(persistStatusMeta("future_persist_state").label).toBe("future_persist_state");
    expect(recoveryStatusMeta("future_recovery_state").label).toBe("future_recovery_state");
  });

  it("缺失字段给出明确文案而不是成功", () => {
    expect(commitStatusMeta(undefined).label).toBe("未提供提交状态");
    expect(pushStatusMeta("").label).toBe("未提供推送状态");
    expect(mergeStatusMeta(undefined).label).toBe("未提供合并状态");
    expect(persistStatusMeta(undefined).label).toBe("未提供记录写入状态");
    expect(recoveryStatusMeta(undefined).label).toBe("未提供核验结论");
    expect(stageLabel(undefined)).toBe(CODE_SAVE_STAGE_MISSING_LABEL);
  });

  it("未知阶段原样展示", () => {
    expect(stageLabel("cleanup")).toBe("cleanup");
    expect(stageLabel("push")).toBe("推送阶段");
  });
});

describe("三个 Git 阶段与记录维度的语义区别", () => {
  it("提交成功不表示推送成功", () => {
    const item = record({ commit_status: "succeeded", push_status: "failed" });
    expect(commitStatusMeta(item.commit_status).label).toBe("提交成功");
    expect(pushStatusMeta(item.push_status).label).toBe("推送失败");
    expect(pushSummary(item)).toBe("推送失败，任务分支没有确认到达远端。");
  });

  it("推送成功不表示合并成功", () => {
    const item = record({
      merge_enabled: true,
      merge_status: "conflict",
      push_status: "succeeded",
    });
    expect(pushStatusMeta(item.push_status).label).toBe("推送成功");
    expect(mergeStatusMeta(item.merge_status).label).toBe("合并冲突");
    expect(mergeSummary(item)).toContain("冲突");
  });

  it("merge_status=not_requested 既不是失败也不是已合并", () => {
    const item = record({ merge_enabled: false, merge_status: "not_requested" });
    const meta = mergeStatusMeta(item.merge_status);
    expect(meta.label).toBe("未请求合并");
    expect(meta.color).not.toBe("error");
    expect(meta.color).not.toBe("success");
    expect(mergeRequested(item)).toBe(false);
    expect(mergeSummary(item)).toContain("没有请求合并基础分支");
    expect(mergeSummary(item)).toContain("不表示合并失败");
  });

  it("merge_enabled=false 时不暗示已授权合并", () => {
    const item = record({ merge_enabled: false, merge_status: "skipped" });
    expect(mergeRequested(item)).toBe(false);
    expect(mergeSummary(item)).toContain("没有请求合并基础分支");
  });

  it("push/merge unknown 明确说明结果尚不确定，而不是可直接重试", () => {
    const item = record({ merge_enabled: true, merge_status: "unknown", push_status: "unknown" });
    expect(pushSummary(item)).toContain("尚不确定");
    expect(pushSummary(item)).toContain("不能直接重试");
    expect(mergeWarning(item)).toContain("尚不确定");
    expect(recordNeedsAttention(item)).toBe(true);
  });

  it("persist_status=unconfirmed 明确说明记录可能落后于真实现场", () => {
    const item = record({ persist_status: "unconfirmed" });
    const warnings = recordWarnings(item);
    expect(warnings.some((warning) => warning.includes("可能落后于真实 Git 现场"))).toBe(true);
    expect(recordNeedsAttention(item)).toBe(true);
  });

  it("persist_status=persisted 且核验已确认时不产生警示", () => {
    const item = record({ recovery_status: "confirmed_applied" });
    expect(recordWarnings(item)).toEqual([]);
    expect(recordNeedsAttention(item)).toBe(false);
  });

  it("unverified 与 unverifiable 的说明不同", () => {
    const unverified = recordWarnings(record({ recovery_status: "unverified" }));
    const unverifiable = recordWarnings(record({ recovery_status: "unverifiable" }));
    expect(unverified.join("")).toContain("尚未核验");
    expect(unverifiable.join("")).toContain("无法取得可信事实");
    expect(unverified.join("")).not.toBe(unverifiable.join(""));
  });

  it("核验结论与阶段同时呈现，且不表述为三个阶段都已核验", () => {
    const item = record({ recovery_status: "confirmed_not_applied", stage: "merge" });
    const scope = recoveryScope(item);
    expect(scope).toContain("合并阶段");
    expect(scope).toContain("不代表提交、推送、合并三个阶段都已核验");
    expect(stageLabel(item.stage)).toBe("合并阶段");
  });

  it("未知 persist / recovery 取值产生未定义提示", () => {
    const warnings = recordWarnings(
      record({ persist_status: "future_persist", recovery_status: "future_recovery" }),
    );
    expect(warnings.some((warning) => warning.includes("future_persist"))).toBe(true);
    expect(warnings.some((warning) => warning.includes("future_recovery"))).toBe(true);
  });
});

describe("多条记录与时间信息", () => {
  it("保留多条记录的服务端顺序并各自独立判定", async () => {
    const records = [
      record({ id: "record-2", push_status: "unknown", recovery_status: "unverifiable" }),
      record({ id: "record-1", push_status: "succeeded", recovery_status: "confirmed_applied" }),
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ records, total: 2 })),
    );

    const { data } = await fetchTaskCodeSaves("task-1");

    expect(data.records.map((item) => item.id)).toEqual(["record-2", "record-1"]);
    expect(recordNeedsAttention(data.records[0]!)).toBe(true);
    expect(recordNeedsAttention(data.records[1]!)).toBe(false);
  });

  it("时间字段缺失时不会被当作有效时间", () => {
    const item = record({ finished_at: undefined, started_at: undefined });
    expect(item.started_at).toBeUndefined();
    expect(item.finished_at).toBeUndefined();
    expect(item.created_at).toBe("2026-10-26T08:00:00Z");
  });
});
