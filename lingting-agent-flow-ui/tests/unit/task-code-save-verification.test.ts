import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  TASKS_PATH,
  taskCodeSaveVerifyPath,
  verifyCodeSave,
  type CodeSaveRecordView,
  type CodeSaveVerifyResponse,
} from "../../src/lib/api";
import {
  acceptVerifyResponse,
  beginVerifyRequest,
  canSubmitVerify,
  compareRecordSnapshots,
  createVerifyGate,
  DECISION_SUMMARY_MISSING,
  decisionView,
  isUncertainVerifyError,
  OBSERVATION_MISSING_HEAD,
  OBSERVATION_MISSING_REMOTE_BASE,
  OBSERVATION_MISSING_REMOTE_TASK,
  observationRows,
  responseBelongsTo,
  safeToRetryView,
  VERIFY_UNKNOWN_OUTCOME_HINT,
  verifyPersistView,
  verifyTicketKey,
} from "../../src/pages/tasks/codeSaveVerification";
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

/** 核验响应中的记录视图样本，字段与 Go 端 `httpapi.CodeSaveRecordResponse` 一致 */
function recordView(overrides: Partial<CodeSaveRecordView> = {}): CodeSaveRecordView {
  return {
    commit_sha: "1".repeat(40),
    commit_status: "pending",
    merge_sha: undefined,
    merge_status: "pending",
    persist_status: "persisted",
    push_status: "pending",
    record_id: "record-1",
    recovery_status: "unverified",
    remote_commit_sha: undefined,
    task_branch: "agent/task-1",
    task_id: "task-1",
    ...overrides,
  };
}

/** 完整核验响应样本，字段与 Go 端 `httpapi.CodeSaveVerifyResponse` 一致 */
function verifyResponse(overrides: Partial<CodeSaveVerifyResponse> = {}): CodeSaveVerifyResponse {
  return {
    decision: {
      safe_to_retry: false,
      stage: "push",
      status: "unverifiable",
      summary: "远程任务分支不存在或无法读取",
    },
    observation: {
      head_commit: "1".repeat(40),
      read_failed: false,
    },
    persisted: true,
    record: recordView(),
    updated: recordView({ recovery_status: "unverifiable" }),
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("核验路径构造", () => {
  it("由任务代码保存路径派生核验动作路径", () => {
    expect(taskCodeSaveVerifyPath("task-1", "record-1")).toBe(
      `${TASKS_PATH}/task-1/code-saves/record-1/verify-code-save`,
    );
  });

  it("任务标识与记录标识都被 URL 编码", () => {
    expect(taskCodeSaveVerifyPath("task 1", "record/1")).toBe(
      `${TASKS_PATH}/task%201/code-saves/record%2F1/verify-code-save`,
    );
  });

  it("空标识直接拒绝，不拼出会命中其他资源的路径", () => {
    expect(() => taskCodeSaveVerifyPath("", "record-1")).toThrow();
    expect(() => taskCodeSaveVerifyPath("task-1", "")).toThrow();
    expect(() => taskCodeSaveVerifyPath("task-1", "   ")).toThrow();
  });
});

describe("核验请求", () => {
  it("只发出一次 POST，请求体为空对象，路径指向该记录", async () => {
    const spy = vi.fn(async () => jsonResponse(verifyResponse()));
    vi.stubGlobal("fetch", spy);

    const { data, request } = await verifyCodeSave("task-1", "record-1");

    expect(request).toEqual({
      method: "POST",
      path: `${TASKS_PATH}/task-1/code-saves/record-1/verify-code-save`,
    });
    expect(spy).toHaveBeenCalledTimes(1);
    const [target, init] = spy.mock.calls[0] as unknown as [string, RequestInit];
    // 容器与工作区由服务端确定：前端不猜测、不拼造，请求体固定为空对象。
    expect(target).toBe(`${TASKS_PATH}/task-1/code-saves/record-1/verify-code-save`);
    expect(init.body).toBe("{}");
    expect(init.method).toBe("POST");
    expect(data.persisted).toBe(true);
  });

  it("核验请求不设客户端超时，避免提前断开留下未知写回状态", async () => {
    const spy = vi.fn(async () => jsonResponse(verifyResponse()));
    vi.stubGlobal("fetch", spy);
    vi.useFakeTimers();
    try {
      await verifyCodeSave("task-1", "record-1");
      vi.advanceTimersByTime(120_000);
    } finally {
      vi.useRealTimers();
    }
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("404 保留服务端业务错误说明", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "not_found", message: "代码保存记录不存在: record-1" } },
          { status: 404 },
        ),
      ),
    );

    await expect(verifyCodeSave("task-1", "record-1")).rejects.toBeInstanceOf(ApiError);
    await verifyCodeSave("task-1", "record-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(404);
      expect(describeApiError(cause)).toBe("代码保存记录不存在: record-1");
    });
  });

  it("412 保留核验无法开始的真实原因", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { error: { code: "precondition_failed", message: "核验代码保存记录失败: 容器已删除" } },
          { status: 412 },
        ),
      ),
    );

    await verifyCodeSave("task-1", "record-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(412);
      expect((cause as ApiError).kind).toBe("http");
      expect(describeApiError(cause)).toContain("容器已删除");
    });
  });

  it("网络中断只发出一次请求，不自动重发", async () => {
    const spy = vi.fn(async () => {
      throw new TypeError("failed to fetch");
    });
    vi.stubGlobal("fetch", spy);

    await verifyCodeSave("task-1", "record-1").catch((cause: unknown) => {
      expect((cause as ApiError).kind).toBe("network");
    });

    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("空响应体不会被当成成功的核验结果", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("", { status: 200 })),
    );

    await verifyCodeSave("task-1", "record-1").catch((cause: unknown) => {
      expect((cause as ApiError).kind).toBe("invalid_response");
    });
  });
});

describe("现场观察展示", () => {
  it("read_failed=false 时明确说明只读查询完成不等于操作发生", () => {
    const rows = observationRows({ read_failed: false });
    const readFailed = rows.find((row) => row.key === "read_failed")!;
    expect(readFailed.value).toContain("否");
    expect(readFailed.value).toContain("不代表操作已经发生");
  });

  it("read_failed=true 时如实展示读取失败，并说明其余字段不可信", () => {
    const rows = observationRows({
      read_error: "容器已脱离管理",
      read_failed: true,
    });
    const readFailed = rows.find((row) => row.key === "read_failed")!;
    expect(readFailed.value).toContain("没有完成");
    expect(readFailed.value).toContain("其余现场字段不可信");
    expect(rows.find((row) => row.key === "read_error")?.value).toBe("容器已脱离管理");
  });

  it("read_error 缺失时不渲染读取失败原因行", () => {
    const rows = observationRows({ read_failed: false });
    expect(rows.some((row) => row.key === "read_error")).toBe(false);
  });

  it("三个 commit 字段各自独立，缺失时给中性占位符而不是“未变化”", () => {
    const rows = observationRows({
      head_commit: "a".repeat(40),
      read_failed: false,
      remote_base_commit: "c".repeat(40),
      remote_task_commit: undefined,
    });
    expect(rows.find((row) => row.key === "head_commit")?.value).toBe("a".repeat(40));
    const taskRow = rows.find((row) => row.key === "remote_task_commit")!;
    expect(taskRow.missing).toBe(true);
    expect(taskRow.value).toBe(OBSERVATION_MISSING_REMOTE_TASK);
    expect(taskRow.value).toContain("缺失不代表没有变化");
    expect(rows.find((row) => row.key === "remote_base_commit")?.value).toBe("c".repeat(40));
  });

  it("全部 commit 缺失时不推断“远程与本地一致”", () => {
    const rows = observationRows({ read_failed: false });
    const values = rows.map((row) => row.value).join(" ");
    expect(values).toContain(OBSERVATION_MISSING_HEAD);
    expect(values).toContain(OBSERVATION_MISSING_REMOTE_BASE);
    expect(values).not.toContain("一致");
    expect(values).not.toContain("未发生变化");
  });

  it("空字符串 commit 与缺失同等处理", () => {
    const rows = observationRows({ head_commit: "   ", read_failed: false });
    expect(rows.find((row) => row.key === "head_commit")?.missing).toBe(true);
  });
});

describe("核验判定展示", () => {
  it("判定状态、阶段、说明与 safe_to_retry 分别展示", () => {
    const view = decisionView({
      safe_to_retry: true,
      stage: "push",
      status: "confirmed_not_applied",
      summary: "远程任务分支不存在，推送没有发生",
    });
    expect(view.status.label).toBe("已确认操作未发生");
    expect(view.stage).toBe("推送阶段");
    expect(view.summary).toBe("远程任务分支不存在，推送没有发生");
    expect(view.safeToRetry.raw).toBe("true");
  });

  it("未知判定状态原样展示且不是成功色", () => {
    const view = decisionView({ safe_to_retry: false, status: "future_state", summary: "x" });
    expect(view.status.label).toBe("future_state");
    expect(view.status.known).toBe(false);
    expect(view.status.color).not.toBe("success");
  });

  it("缺失阶段与缺失说明都给出中性文案", () => {
    const view = decisionView({ safe_to_retry: false, status: "unverifiable", summary: "   " });
    expect(view.stage).toBe("未记录阶段");
    expect(view.summary).toBe(DECISION_SUMMARY_MISSING);
  });

  it("未知阶段原样展示", () => {
    const view = decisionView({
      safe_to_retry: false,
      stage: "cleanup",
      status: "unverified",
      summary: "x",
    });
    expect(view.stage).toBe("cleanup");
  });

  it("safe_to_retry=true 只是判定字段，不构成自动重试许可", () => {
    const view = safeToRetryView(true);
    expect(view.attention).toBe(false);
    expect(view.description).toContain("页面不会自动重试");
    expect(view.description).toContain("显式发起");
  });

  it("safe_to_retry=false 需要用户注意并说明风险", () => {
    const view = safeToRetryView(false);
    expect(view.attention).toBe(true);
    expect(view.description).toContain("重复提交");
    expect(view.description).toContain("人工核验");
  });

  it("safe_to_retry 缺失时不推断可以重试", () => {
    const view = safeToRetryView(undefined);
    expect(view.attention).toBe(true);
    expect(view.raw).toBe("undefined");
    expect(view.description).toContain("不得据此判断可以安全重试");
  });
});

describe("持久化状态语义", () => {
  it("persisted=true 只说明结论已写回，不等于核验通过", () => {
    const view = verifyPersistView(true);
    expect(view.persisted).toBe(true);
    expect(view.type).toBe("success");
    expect(view.title).toBe("核验结论已写入记录");
    expect(view.description).toContain("不代表代码保存成功或核验通过");
  });

  it("persisted=false 明确提示未持久化，并保留响应中已经取得的结论", () => {
    const view = verifyPersistView(false);
    expect(view.persisted).toBe(false);
    expect(view.type).toBe("warning");
    expect(view.title).toBe("核验结论未成功持久化");
    expect(view.description).toContain("仍然是本次核验取得的真实事实");
    expect(view.description).toContain("刷新代码保存");
  });

  it("read_failed、decision.status 与 persisted 是三个独立事实", () => {
    const response = verifyResponse({
      decision: { safe_to_retry: false, status: "unverifiable", summary: "无法核验" },
      observation: { read_error: "容器已删除", read_failed: true },
      persisted: false,
    });
    expect(response.observation.read_failed).toBe(true);
    expect(response.decision.status).toBe("unverifiable");
    expect(response.persisted).toBe(false);
    // 三者可以同时出现不同取值，页面分别展示，不合并成一个布尔值。
    expect(
      new Set([
        String(response.observation.read_failed),
        response.decision.status,
        String(response.persisted),
      ]).size,
    ).toBe(3);
  });
});

describe("核验前后记录对比", () => {
  it("逐维度对比，不把 record 与 updated 混用", () => {
    const before = recordView();
    const updated = recordView({
      commit_status: "succeeded",
      recovery_status: "confirmed_applied",
    });
    const rows = compareRecordSnapshots(before, updated);

    const commit = rows.find((row) => row.key === "commit")!;
    expect(commit.before).toContain("等待提交");
    expect(commit.after).toContain("提交成功");
    expect(commit.changed).toBe(true);

    const recovery = rows.find((row) => row.key === "recovery")!;
    expect(recovery.changed).toBe(true);

    const push = rows.find((row) => row.key === "push")!;
    expect(push.changed).toBe(false);
  });

  it("两个视图完全相同时所有维度都标记为未变化", () => {
    const rows = compareRecordSnapshots(recordView(), recordView());
    expect(rows.every((row) => !row.changed)).toBe(true);
  });

  it("commit SHA 变化也会被识别", () => {
    const rows = compareRecordSnapshots(
      recordView({ commit_sha: "1".repeat(40) }),
      recordView({ commit_sha: "2".repeat(40) }),
    );
    expect(rows.find((row) => row.key === "commit")?.changed).toBe(true);
    expect(rows.find((row) => row.key === "commit")?.after).toContain("2".repeat(40));
  });

  it("字段缺失不会被展示成状态被清除", () => {
    const rows = compareRecordSnapshots(
      recordView({ commit_sha: undefined }),
      recordView({ commit_sha: undefined }),
    );
    expect(rows.find((row) => row.key === "commit")?.after).toBe("等待提交");
  });
});

describe("核验响应的记录归属", () => {
  it("record_id 与本次核验的记录一致时判定为属于该记录", () => {
    expect(responseBelongsTo(verifyResponse(), "record-1")).toBe(true);
  });

  it("响应记录属于其他记录时不被挂到当前记录上", () => {
    const response = verifyResponse({
      record: recordView({ record_id: "record-2" }),
      updated: recordView({ record_id: "record-2" }),
    });
    expect(responseBelongsTo(response, "record-1")).toBe(false);
  });
});

describe("在途与防重", () => {
  it("没有 entry 时可以发起核验", () => {
    expect(canSubmitVerify(undefined)).toBe(true);
  });

  it("在途时禁止重复提交", () => {
    expect(canSubmitVerify({ running: true })).toBe(false);
  });

  it("请求结束后可以再次核验", () => {
    expect(canSubmitVerify({ response: verifyResponse(), running: false })).toBe(true);
  });
});

describe("票据与迟到响应", () => {
  it("同一记录的多次核验只有最后一次生效", () => {
    const gate = createVerifyGate();
    const first = beginVerifyRequest(gate, "task-1", "record-1");
    const second = beginVerifyRequest(gate, "task-1", "record-1");
    expect(acceptVerifyResponse(gate, first, "task-1", false)).toBe(false);
    expect(acceptVerifyResponse(gate, second, "task-1", false)).toBe(true);
  });

  it("不同记录的序号互相独立", () => {
    const gate = createVerifyGate();
    const a = beginVerifyRequest(gate, "task-1", "record-a");
    const b = beginVerifyRequest(gate, "task-1", "record-b");
    expect(a.requestID).toBe(1);
    expect(b.requestID).toBe(1);
    expect(acceptVerifyResponse(gate, a, "task-1", false)).toBe(true);
    expect(acceptVerifyResponse(gate, b, "task-1", false)).toBe(true);
  });

  it("切换任务后旧任务的响应被丢弃", () => {
    const gate = createVerifyGate();
    const ticket = beginVerifyRequest(gate, "task-a", "record-1");
    expect(acceptVerifyResponse(gate, ticket, "task-b", false)).toBe(false);
    expect(acceptVerifyResponse(gate, ticket, undefined, false)).toBe(false);
    expect(acceptVerifyResponse(gate, ticket, "task-a", false)).toBe(true);
  });

  it("被取消的请求不被接受", () => {
    const gate = createVerifyGate();
    const ticket = beginVerifyRequest(gate, "task-1", "record-1");
    expect(acceptVerifyResponse(gate, ticket, "task-1", true)).toBe(false);
  });

  it("票据键同时包含任务与记录", () => {
    expect(verifyTicketKey("task-1", "record-1")).not.toBe(verifyTicketKey("task-2", "record-1"));
    expect(verifyTicketKey("task-1", "record-1")).not.toBe(verifyTicketKey("task-1", "record-2"));
  });
});

describe("结果不确定的判定", () => {
  it("超时、网络中断与取消都属于结果不确定", () => {
    expect(isUncertainVerifyError("timeout")).toBe(true);
    expect(isUncertainVerifyError("network")).toBe(true);
    expect(isUncertainVerifyError("aborted")).toBe(true);
  });

  it("服务端业务错误不属于结果不确定", () => {
    expect(isUncertainVerifyError("http")).toBe(false);
    expect(isUncertainVerifyError("invalid_response")).toBe(false);
  });

  it("不确定提示明确要求用户确认现场且不自动重发", () => {
    expect(VERIFY_UNKNOWN_OUTCOME_HINT).toContain("无法确认");
    expect(VERIFY_UNKNOWN_OUTCOME_HINT).toContain("不会自动重复提交");
    expect(VERIFY_UNKNOWN_OUTCOME_HINT).toContain("刷新代码保存");
  });
});
