import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  TASKS_PATH,
  disposeTaskContainer,
  fetchTaskContainers,
  taskContainerDisposalPath,
  taskContainersPath,
  type ContainerDisposalResponse,
  type TaskContainer,
} from "../../src/lib/api";
import {
  CONTAINER_STATE_META,
  DISPOSAL_NOT_RETRY_HINT,
  DISPOSAL_UNKNOWN_OUTCOME_HINT,
  containerActionBlocked,
  containerActionBlockedReason,
  containerEndReasonLabel,
  containerOriginMeta,
  containerRemoved,
  containerStateMeta,
  describeDisposalOutcome,
  disposalActionMeta,
  disposalOutcomeMeta,
  isKnownContainerState,
  isUncertainDisposalError,
} from "../../src/pages/tasks/containers";
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

/** 一条容器记录样本，字段与 Go 端 `httpapi.TaskContainerResponse` 一致 */
function container(overrides: Partial<TaskContainer> = {}): TaskContainer {
  return {
    created_at: "2026-10-27T08:00:00Z",
    detached: false,
    docker_state: "running",
    end_reason: "",
    id: "container-1",
    name: "agentflow-container-1",
    origin: "current",
    state: "running",
    task_id: "task-1",
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/** 一次处置响应样本，字段与 Go 端 `httpapi.ContainerDisposalResponse` 一致 */
function disposalResponse(
  overrides: Partial<ContainerDisposalResponse> = {},
): ContainerDisposalResponse {
  return {
    action: "delete",
    container: container({ end_reason: "manual", state: "removed" }),
    outcome: "completed",
    task_id: "task-1",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("容器接口路径构造", () => {
  it("由任务路径派生容器集合路径", () => {
    expect(taskContainersPath("task-1")).toBe(`${TASKS_PATH}/task-1/containers`);
  });

  it("处置路径包含真实容器标识与动作", () => {
    expect(taskContainerDisposalPath("task-1", "container-1", "delete")).toBe(
      `${TASKS_PATH}/task-1/containers/container-1/delete`,
    );
    expect(taskContainerDisposalPath("task-1", "container-1", "detach")).toBe(
      `${TASKS_PATH}/task-1/containers/container-1/detach`,
    );
  });

  it("任务与容器标识都被 URL 编码", () => {
    expect(taskContainerDisposalPath("task 1", "container/1", "delete")).toBe(
      `${TASKS_PATH}/task%201/containers/container%2F1/delete`,
    );
  });

  it("空标识与未知动作直接拒绝，不拼出会命中其他资源的路径", () => {
    expect(() => taskContainersPath("")).toThrow();
    expect(() => taskContainerDisposalPath("task-1", "", "delete")).toThrow();
    expect(() => taskContainerDisposalPath("task-1", "container-1", "stop" as never)).toThrow();
  });
});

describe("容器查询请求", () => {
  it("只发出一次 GET，路径指向该任务的容器集合", async () => {
    const spy = vi.fn(async () => jsonResponse({ records: [container()], total: 1 }));
    vi.stubGlobal("fetch", spy);

    const { data, request } = await fetchTaskContainers("task-1");

    expect(request).toEqual({ method: "GET", path: `${TASKS_PATH}/task-1/containers` });
    expect(spy).toHaveBeenCalledTimes(1);
    expect(data.total).toBe(1);
    expect(data.records[0]?.id).toBe("container-1");
  });

  it("404 保留服务端业务说明", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({ error: { code: "not_found", message: "任务不存在" } }, { status: 404 }),
      ),
    );

    await fetchTaskContainers("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(404);
      expect(describeApiError(cause)).toBe("任务不存在");
    });
  });

  it("空响应体不会被当成成功的查询结果", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("", { status: 200 })),
    );
    await fetchTaskContainers("task-1").catch((cause: unknown) => {
      expect((cause as ApiError).kind).toBe("invalid_response");
    });
  });
});

describe("容器处置请求", () => {
  it("delete 与 detach 使用不同的方法与路径，且都只发一次 POST", async () => {
    const spy = vi.fn(async () => jsonResponse(disposalResponse()));
    vi.stubGlobal("fetch", spy);

    await disposeTaskContainer("task-1", "container-1", "delete");
    await disposeTaskContainer("task-1", "container-1", "detach");

    expect(spy).toHaveBeenCalledTimes(2);
    const calls = spy.mock.calls as unknown as [string, RequestInit][];
    expect(calls[0]?.[0]).toBe(`${TASKS_PATH}/task-1/containers/container-1/delete`);
    expect(calls[0]?.[1].method).toBe("POST");
    expect(calls[1]?.[0]).toBe(`${TASKS_PATH}/task-1/containers/container-1/detach`);
    expect(calls[1]?.[1].method).toBe("POST");
  });

  it("处置请求不设客户端超时，避免提前断开留下未知状态", async () => {
    const spy = vi.fn(async () => jsonResponse(disposalResponse()));
    vi.stubGlobal("fetch", spy);
    vi.useFakeTimers();
    try {
      await disposeTaskContainer("task-1", "container-1", "delete");
      vi.advanceTimersByTime(120_000);
    } finally {
      vi.useRealTimers();
    }
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("409 保留服务端归属拒绝的真实原因", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          {
            error: {
              code: "state_conflict",
              message: "容器归属校验未通过: 容器登记的是任务 task-2，不是任务 task-1",
            },
          },
          { status: 409 },
        ),
      ),
    );

    await disposeTaskContainer("task-1", "container-1", "delete").catch((cause: unknown) => {
      expect((cause as ApiError).status).toBe(409);
      expect((cause as ApiError).kind).toBe("http");
      expect(describeApiError(cause)).toContain("归属校验未通过");
    });
  });

  it("网络中断只发出一次请求，不自动重发", async () => {
    const spy = vi.fn(async () => {
      throw new TypeError("failed to fetch");
    });
    vi.stubGlobal("fetch", spy);

    await disposeTaskContainer("task-1", "container-1", "delete").catch((cause: unknown) => {
      expect((cause as ApiError).kind).toBe("network");
    });

    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("结果不确定的错误类别可被识别，业务错误不算", () => {
    expect(isUncertainDisposalError("network")).toBe(true);
    expect(isUncertainDisposalError("timeout")).toBe(true);
    expect(isUncertainDisposalError("aborted")).toBe(true);
    expect(isUncertainDisposalError("http")).toBe(false);
    expect(isUncertainDisposalError("invalid_response")).toBe(false);
  });
});

describe("容器状态展示", () => {
  it("已知管理状态有中文标签，终态只有 detached 与 removed", () => {
    expect(containerStateMeta("running").label).toBe("运行中");
    expect(containerStateMeta("detached").terminal).toBe(true);
    expect(containerStateMeta("removed").terminal).toBe(true);
    expect(containerStateMeta("running").terminal).toBe(false);
    expect(isKnownContainerState("running")).toBe(true);
  });

  it("未知管理状态原样展示，且不视为终态", () => {
    const meta = containerStateMeta("future_state");
    expect(meta.label).toBe("future_state");
    expect(meta.terminal).toBe(false);
    expect(meta.color).toBe("default");
    expect(isKnownContainerState("future_state")).toBe(false);
  });

  it("缺失管理状态显示中性占位符而不是伪造状态", () => {
    expect(containerStateMeta("").label).toBe("未提供");
  });

  it("关联方式区分当前代次与历史代次", () => {
    expect(containerOriginMeta("current").label).toBe("任务当前容器");
    expect(containerOriginMeta("history").label).toBe("历史容器");
    expect(containerOriginMeta("future").label).toBe("future");
    expect(containerOriginMeta("").label).toBe("未提供");
  });

  it("结束原因有中文标签，空值显示未记录，未知值原样保留", () => {
    expect(containerEndReasonLabel("manual")).toBe("人工处置");
    expect(containerEndReasonLabel("detached")).toBe("人工脱离管理");
    expect(containerEndReasonLabel("")).toBe("未记录");
    expect(containerEndReasonLabel("   ")).toBe("未记录");
    expect(containerEndReasonLabel("future_reason")).toBe("future_reason");
  });

  it("处置结论未知取值原样展示且不使用成功色", () => {
    expect(disposalOutcomeMeta("completed").color).toBe("success");
    expect(disposalOutcomeMeta("failed").color).toBe("error");
    expect(disposalOutcomeMeta("uncertain").color).toBe("warning");
    const unknown = disposalOutcomeMeta("future_outcome");
    expect(unknown.label).toBe("future_outcome");
    expect(unknown.color).toBe("default");
  });

  it("已删除判定不依赖状态元数据的颜色", () => {
    expect(containerRemoved(container({ state: "removed" }))).toBe(true);
    expect(containerRemoved(container({ state: "running" }))).toBe(false);
  });
});

describe("动作可用性（展示层防重）", () => {
  it("已脱离管理的容器两个动作都不可用", () => {
    const detached = container({ detached: true, state: "detached" });
    expect(containerActionBlocked(detached, "delete")).toBe(true);
    expect(containerActionBlocked(detached, "detach")).toBe(true);
    expect(containerActionBlockedReason(detached)).toContain("不再接受自动删除");
  });

  it("已删除的容器不能脱离管理，但删除仍可交给服务端判幂等", () => {
    const removed = container({ state: "removed" });
    expect(containerActionBlocked(removed, "detach")).toBe(true);
    expect(containerActionBlocked(removed, "delete")).toBe(false);
    expect(containerActionBlockedReason(removed)).toContain("已被删除");
  });

  it("受管理容器没有前端白名单：状态不由前端判定可操作", () => {
    for (const state of ["creating", "created", "running", "stopped", "unknown"]) {
      const record = container({ state });
      expect(containerActionBlocked(record, "delete")).toBe(false);
      expect(containerActionBlocked(record, "detach")).toBe(false);
      expect(containerActionBlockedReason(record)).toBeUndefined();
    }
  });

  it("状态是 detached 但缺少脱离标记时同样阻止操作", () => {
    const broken = container({ detached: false, state: "detached" });
    expect(containerActionBlocked(broken, "delete")).toBe(true);
  });

  it("未知状态不被当作可操作终态，也不被当作已成功", () => {
    const unknown = container({ state: "future_state" });
    expect(containerActionBlocked(unknown, "delete")).toBe(false);
    expect(containerRemoved(unknown)).toBe(false);
    expect(containerStateMeta(unknown.state).terminal).toBe(false);
  });
});

describe("动作文案互不混淆", () => {
  it("delete 说明会删除容器且不可撤销，确认按钮为危险色", () => {
    const meta = disposalActionMeta("delete");
    expect(meta.buttonLabel).toBe("删除容器");
    expect(meta.danger).toBe(true);
    expect(meta.confirmDescription).toContain("删除该容器");
    expect(meta.confirmDescription).toContain("共享镜像");
    expect(meta.confirmDescription).toContain("已脱离管理的容器不会被删除");
  });

  it("detach 说明保留现场且不是删除、不等于已停止", () => {
    const meta = disposalActionMeta("detach");
    expect(meta.buttonLabel).toBe("脱离管理");
    expect(meta.danger).toBe(false);
    expect(meta.confirmDescription).toContain("保留容器现场");
    expect(meta.confirmDescription).toContain("不是删除");
    expect(meta.confirmDescription).toContain("不代表容器已经停止");
  });

  it("两个动作的按钮文案与确认标题互不相同", () => {
    const del = disposalActionMeta("delete");
    const det = disposalActionMeta("detach");
    expect(del.buttonLabel).not.toBe(det.buttonLabel);
    expect(del.confirmTitle).not.toBe(det.confirmTitle);
    expect(del.confirmDescription).not.toBe(det.confirmDescription);
  });

  it("两个动作的文案都不出现模糊的「清理」按钮语义", () => {
    expect(disposalActionMeta("delete").buttonLabel).not.toContain("清理");
    expect(disposalActionMeta("detach").buttonLabel).not.toContain("清理");
  });
});

describe("处置结论展示", () => {
  it("delete 完成时以响应中的真实记录描述已删除", () => {
    const view = describeDisposalOutcome({
      action: "delete",
      container: container({ state: "removed", end_reason: "manual" }),
      detail: "容器 container-1 已删除",
      outcome: "completed",
    });
    expect(view.title).toBe("处置已完成");
    expect(view.kind).toBe("success");
    expect(view.details.join(" ")).toContain("已标记为已删除");
    expect(view.details.join(" ")).toContain("容器 container-1 已删除");
  });

  it("detach 完成时说明已标记脱离且现场保留", () => {
    const view = describeDisposalOutcome({
      action: "detach",
      container: container({ detached: true, state: "detached" }),
      outcome: "completed",
    });
    expect(view.details.join(" ")).toContain("已标记为脱离管理，容器现场保留");
  });

  it("completed 但记录未体现结果时不说「已删除」", () => {
    const view = describeDisposalOutcome({
      action: "delete",
      container: container({ state: "running" }),
      outcome: "completed",
    });
    expect(view.details.join(" ")).toContain("没有体现该结果");
    expect(view.details.join(" ")).not.toContain("已标记为已删除");
  });

  it("uncertain 不用成功色，且要求先核验", () => {
    const view = describeDisposalOutcome({
      action: "delete",
      container: container(),
      outcome: "uncertain",
    });
    expect(view.kind).toBe("warning");
    expect(view.details.join(" ")).toContain("没有确认删除结果");
    expect(view.details.join(" ")).toContain("不要假定操作已经生效");
  });

  it("failed 使用错误色并保留服务端原因", () => {
    const view = describeDisposalOutcome({
      action: "detach",
      container: container(),
      detail: "脱离管理前核验容器失败: 容器在远程机器上已不存在",
      outcome: "failed",
    });
    expect(view.kind).toBe("error");
    expect(view.details.join(" ")).toContain("服务端说明：脱离管理前核验容器失败");
  });

  it("未知结论原样展示且不使用成功色", () => {
    const view = describeDisposalOutcome({
      action: "delete",
      container: container(),
      outcome: "future_outcome",
    });
    expect(view.title).toBe("future_outcome");
    expect(view.kind).toBe("default");
  });

  it("所有结论都提示处置不会重开任务", () => {
    for (const outcome of ["completed", "failed", "uncertain"]) {
      const view = describeDisposalOutcome({ action: "delete", container: container(), outcome });
      expect(view.details).toContain(DISPOSAL_NOT_RETRY_HINT);
    }
  });

  it("不确定提示明确说明不自动重发并要求先核验", () => {
    expect(DISPOSAL_UNKNOWN_OUTCOME_HINT).toContain("无法确认");
    expect(DISPOSAL_UNKNOWN_OUTCOME_HINT).toContain("不会自动重发");
    expect(DISPOSAL_UNKNOWN_OUTCOME_HINT).toContain("刷新容器列表");
  });
});

describe("状态元数据完整性", () => {
  it("覆盖 Go 端 domain.ContainerState 的全部取值", () => {
    expect(Object.keys(CONTAINER_STATE_META).sort()).toEqual([
      "created",
      "creating",
      "detached",
      "removed",
      "running",
      "stopped",
      "unknown",
    ]);
  });
});
