/**
 * 需求页浏览器测试的受控后端。
 *
 * 只模拟 Go 端需求、进度、任务取消与需求删除接口的响应形状与状态变化：
 * - 需求与任务状态由本模块维护，页面通过真实 HTTP 请求读取；
 * - 取消任务按服务端语义推进（`queued` 直接收敛为 `cancelled`，其余进入 `cancelling`），
 *   真实收敛由用例通过 `setTaskStatus` 模拟；
 * - 删除只把需求标记为已删除，并返回真实任务数量；
 * - `409` 与连接中断可以按用例切换，用于验证页面不伪造成功、不自动重发。
 */
import type { Page, Route } from "@playwright/test";

/** 受控项目 */
export type MockProject = {
  id: string;
  name: string;
};

/** 预置任务 */
export type TaskSeed = {
  id: string;
  /** 任务真实状态：`queued` / `failed` / `cancelling` / `preparing` / 未知取值等 */
  status: string;
  /** 需求内顺序，缺省按数组下标 +1 */
  sequence?: number;
};

/** 预置需求 */
export type RequirementSeed = {
  id: string;
  projectID: string;
  title: string;
  description?: string;
  status?: string;
  tasks: TaskSeed[];
};

/** 删除请求的响应行为 */
export type DeleteMode =
  | { kind: "success" }
  | { kind: "conflict"; message?: string }
  | { kind: "network" };

/** 受控后端状态 */
export type RequirementsBackendState = {
  /** 删除请求次数：用于验证页面不自动重发 */
  deleteCalls: number;
  /** 取消请求携带的任务标识 */
  cancelCalls: string[];
  /** 已被删除的需求标识 */
  deletedRequirements: string[];
};

/** 受控后端句柄 */
export type RequirementsBackend = {
  state: RequirementsBackendState;
  /** 切换删除接口的行为 */
  setDeleteMode: (mode: DeleteMode) => void;
  /** 写入任务状态，用于模拟执行者真实收敛 */
  setTaskStatus: (taskID: string, status: string) => void;
  /** 把需求从列表中移除（模拟另一处并发删除） */
  markDeleted: (requirementID: string) => void;
};

const TIMESTAMP = "2026-11-08T10:00:00Z";

/** 返回统一错误响应 */
async function fulfillError(route: Route, status: number, code: string, message: string) {
  await route.fulfill({
    body: JSON.stringify({ error: { code, message, request_id: "test-request" } }),
    contentType: "application/json",
    status,
  });
}

/** 返回 JSON 响应 */
async function fulfillJSON(route: Route, status: number, body: unknown) {
  await route.fulfill({
    body: JSON.stringify(body),
    contentType: "application/json",
    status,
  });
}

/** 任务计数：与服务端 `ProjectTaskCountsResponse` 形状一致 */
function taskCounts(statuses: string[]) {
  const counts = {
    completed: 0,
    failed: 0,
    paused: 0,
    pending: 0,
    recovery_required: 0,
    running: 0,
    total: statuses.length,
    cancelled: 0,
  };
  for (const status of statuses) {
    switch (status) {
      case "completed":
        counts.completed += 1;
        break;
      case "failed":
        counts.failed += 1;
        break;
      case "cancelled":
        counts.cancelled += 1;
        break;
      case "paused":
        counts.paused += 1;
        break;
      case "recovery_required":
        counts.recovery_required += 1;
        break;
      case "queued":
        counts.pending += 1;
        break;
      default:
        counts.running += 1;
    }
  }
  return counts;
}

/** 安装需求页受控后端 */
export async function installRequirementsBackend(
  page: Page,
  options: {
    projects?: MockProject[];
    requirements: RequirementSeed[];
    deleteMode?: DeleteMode;
  },
): Promise<RequirementsBackend> {
  const projects = options.projects ?? [{ id: "demo-service", name: "示例服务" }];
  const requirements = new Map(
    options.requirements.map((seed) => [seed.id, { ...seed, tasks: [...seed.tasks] }]),
  );
  const deleted = new Set<string>();
  const state: RequirementsBackendState = {
    cancelCalls: [],
    deleteCalls: 0,
    deletedRequirements: [],
  };
  const control: { deleteMode: DeleteMode } = {
    deleteMode: options.deleteMode ?? { kind: "success" },
  };

  const requirementPayload = (seed: RequirementSeed) => ({
    confirmed_at: TIMESTAMP,
    created_at: TIMESTAMP,
    description: seed.description ?? "验证需求删除的完整交互",
    id: seed.id,
    planning_session_id: `${seed.id}-session`,
    priority: "normal",
    project_id: seed.projectID,
    source: "manual",
    status: seed.status ?? "queued",
    title: seed.title,
    updated_at: TIMESTAMP,
  });

  const taskPayload = (seed: RequirementSeed, task: TaskSeed, index: number) => ({
    acceptance_criteria: ["可以独立验收"],
    base_ref: "main",
    created_at: TIMESTAMP,
    goal: `任务 ${index + 1}`,
    host_id: "host-a",
    id: task.id,
    repository_path: "ssh://git.example.com/team/demo",
    requirement_id: seed.id,
    requirement_sequence: task.sequence ?? index + 1,
    status: task.status,
  });

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
    await fulfillJSON(route, 200, {
      records: projects.map((project) => ({
        base_branch: "main",
        created_at: TIMESTAMP,
        default_host_id: "host-a",
        environment: "go",
        id: project.id,
        name: project.name,
        repository_url: "ssh://git.example.com/team/demo",
        updated_at: TIMESTAMP,
      })),
      total: projects.length,
    });
  });

  // 更具体的进度路径必须先注册：否则会被单需求详情路由吃掉。
  await page.route("**/api/v1/requirements/*/progress", async (route) => {
    const requirementID = decodeURIComponent(
      route.request().url().split("/requirements/")[1] ?? "",
    ).replace(/\/progress.*$/, "");
    const seed = requirements.get(requirementID);
    if (!seed || deleted.has(requirementID)) {
      await fulfillError(route, 404, "not_found", "需求不存在或已删除");
      return;
    }
    const statuses = seed.tasks.map((task) => task.status);
    const counts = taskCounts(statuses);
    const blockedTask = seed.tasks.find((task) =>
      ["failed", "cancelled", "paused", "recovery_required"].includes(task.status),
    );
    const running = seed.tasks.some(
      (task) => !["queued", "completed", "failed", "cancelled"].includes(task.status),
    );
    await fulfillJSON(route, 200, {
      blocked: Boolean(blockedTask),
      blocked_reason: blockedTask ? "task_failed" : undefined,
      blocked_task_id: blockedTask?.id,
      counts,
      current: counts.completed,
      current_task_id: running ? seed.tasks[0]?.id : undefined,
      next_task_id: undefined,
      requirement_id: seed.id,
      status: blockedTask ? "blocked" : running ? "executing" : "planned",
      total: statuses.length,
    });
  });

  await page.route("**/api/v1/requirements/*", async (route) => {
    const request = route.request();
    const requirementID = decodeURIComponent(
      request.url().split("/requirements/")[1]?.split("?")[0] ?? "",
    );
    const seed = requirements.get(requirementID);
    if (request.method() === "DELETE") {
      state.deleteCalls += 1;
      if (!seed || deleted.has(requirementID)) {
        await fulfillError(route, 404, "not_found", "需求不存在或已删除");
        return;
      }
      const mode = control.deleteMode;
      if (mode.kind === "network") {
        await route.abort("connectionreset");
        return;
      }
      if (mode.kind === "conflict") {
        await fulfillError(
          route,
          409,
          "state_conflict",
          mode.message ?? "任务仍处于 executing，需要先停止任务并等待真实收敛后才能删除需求",
        );
        return;
      }
      deleted.add(requirementID);
      state.deletedRequirements.push(requirementID);
      await fulfillJSON(route, 200, {
        deleted_at: TIMESTAMP,
        deleted_task_count: seed.tasks.length,
        requirement_id: requirementID,
      });
      return;
    }
    if (request.method() !== "GET") {
      await route.fallback();
      return;
    }
    if (!seed || deleted.has(requirementID)) {
      await fulfillError(route, 404, "not_found", "需求不存在或已删除");
      return;
    }
    await fulfillJSON(route, 200, {
      requirement: requirementPayload(seed),
      tasks: seed.tasks.map((task, index) => taskPayload(seed, task, index)),
    });
  });

  await page.route("**/api/v1/projects/*/requirements", async (route) => {
    const projectID = decodeURIComponent(
      route
        .request()
        .url()
        .split("/projects/")[1]
        ?.replace(/\/requirements.*$/, "") ?? "",
    );
    const records = [...requirements.values()]
      .filter((seed) => seed.projectID === projectID && !deleted.has(seed.id))
      .map(requirementPayload);
    await fulfillJSON(route, 200, { records, total: records.length });
  });

  await page.route("**/api/v1/tasks/*/cancel", async (route) => {
    const taskID = decodeURIComponent(
      route
        .request()
        .url()
        .split("/tasks/")[1]
        ?.replace(/\/cancel.*$/, "") ?? "",
    );
    state.cancelCalls.push(taskID);
    for (const seed of requirements.values()) {
      const task = seed.tasks.find((item) => item.id === taskID);
      if (!task) continue;
      task.status = task.status === "queued" ? "cancelled" : "cancelling";
      await fulfillJSON(route, 200, { id: taskID, status: task.status });
      return;
    }
    await fulfillError(route, 404, "not_found", "任务不存在");
  });

  return {
    markDeleted: (requirementID) => {
      deleted.add(requirementID);
    },
    setDeleteMode: (mode) => {
      control.deleteMode = mode;
    },
    setTaskStatus: (taskID, status) => {
      for (const seed of requirements.values()) {
        const task = seed.tasks.find((item) => item.id === taskID);
        if (task) task.status = status;
      }
    },
    state,
  };
}
