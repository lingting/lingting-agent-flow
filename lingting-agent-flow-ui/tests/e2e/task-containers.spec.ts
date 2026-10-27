import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 任务容器处置入口的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH 或 Pi。
 * 用例只验证一件事：页面是否只用真实的容器登记记录提供两个**独立**的显式动作，
 * 是否按真实响应区分"已处置 / 未成功 / 结果不确定"，并且从不自动重发有副作用的
 * 请求、也不因处置成功而重试任务。
 *
 * 这些用例不能证明容器处置在真实远程环境可用，只能证明 UI 对给定响应的处理。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    closeout: "unconfirmed",
    closeout_summary: "任务结论尚未闭环，仍需继续推进或人工核验",
    created_at: "2026-10-27T08:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "failed",
    status_finished: true,
    status_terminal: false,
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/** 一条容器记录样本，字段与 Go 端 `httpapi.TaskContainerResponse` 一致 */
function sampleContainer(overrides: Record<string, unknown> = {}) {
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

/** 一次删除处置的响应样本 */
function deleteResponse(overrides: Record<string, unknown> = {}) {
  return {
    action: "delete",
    container: sampleContainer({ end_reason: "manual", state: "removed" }),
    detail: "容器 container-1 已删除；共享镜像、缓存卷与远程项目配置目录未受影响",
    outcome: "completed",
    task_id: "task-1",
    ...overrides,
  };
}

/** 一次脱离管理处置的响应样本 */
function detachResponse(overrides: Record<string, unknown> = {}) {
  return {
    action: "detach",
    container: sampleContainer({
      detached: true,
      detached_at: "2026-10-27T09:00:00Z",
      end_reason: "detached",
      state: "detached",
    }),
    detail: "容器 container-1 已脱离 AgentFlow 自动管理，容器现场保留；它不再接受自动启停与删除",
    outcome: "completed",
    task_id: "task-1",
    ...overrides,
  };
}

/** 健康检查响应，使页面外壳处于已连接状态 */
async function stubHealth(page: Page) {
  await page.route("**/api/v1/health", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        checks: [
          { name: "http_api", status: "pass" },
          { name: "database", status: "pass" },
        ],
        checked_at: "2026-10-27T08:00:00Z",
        status: "ok",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截任务列表 */
async function stubTaskList(page: Page, tasks = [sampleTask()]) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: tasks, size: 20, total: tasks.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截指定任务的详情 */
async function stubTaskDetail(page: Page, task = sampleTask()) {
  await page.route(`**/api/v1/tasks/${task.id as string}`, (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      body: JSON.stringify(task),
      contentType: "application/json",
      status: 200,
    });
  });
}

/** 拦截工作流进度 */
async function stubWorkflow(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/workflow`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ has_run: false, steps: [], task_id: taskID, task_status: "failed" }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截步骤列表：空计划 */
async function stubStepsEmpty(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/steps?*`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [], size: 100, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截其它只读面板（代码保存 / 续跑资格 / 事件 / 重试资格） */
async function stubOtherPanels(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/code-saves`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ records: [], total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/resume-eligibility`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        blocking: null,
        can_resume: false,
        code_save_action: "blocked",
        code_save_records: [],
        code_save_summary: "任务未处于可续跑状态",
        next_actions: null,
        summary: "任务状态不允许续跑",
        task_id: taskID,
        task_status: "failed",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/events?*`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ offset: 0, records: [], size: 20, task_id: taskID, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/retry-eligibility`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        blocking: ["status_not_retryable"],
        can_retry: false,
        summary: "任务不能重试",
        task_id: taskID,
        task_status: "failed",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截容器列表；返回被调用的次数计数 */
async function stubContainers(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const calls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/containers`, (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    calls.push(route.request().url());
    const result = responder(calls.length - 1);
    return route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return calls;
}

/** 记录处置写请求（delete / detach），返回调用记录 */
async function stubDisposal(
  page: Page,
  responder: (action: string, callIndex: number) => { body: unknown; status?: number },
  options: { delayMs?: number; taskID?: string } = {},
) {
  const calls: { action: string; url: string }[] = [];
  const taskID = options.taskID ?? "task-1";
  await page.route(`**/api/v1/tasks/${taskID}/containers/**`, async (route: Route) => {
    const method = route.request().method();
    if (method !== "POST") return route.fallback();
    const pathname = new URL(route.request().url()).pathname;
    const action = pathname.slice(pathname.lastIndexOf("/") + 1);
    calls.push({ action, url: route.request().url() });
    if (options.delayMs) {
      await new Promise((resolve) => setTimeout(resolve, options.delayMs));
    }
    const result = responder(action, calls.length - 1);
    return route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return calls;
}

/** 记录所有写请求，用于证明只读查询与禁用状态不会触发任何副作用 */
async function recordWrites(page: Page) {
  const writes: string[] = [];
  await page.route("**/api/v1/**", (route: Route) => {
    const method = route.request().method();
    if (method !== "GET" && method !== "HEAD") {
      writes.push(`${method} ${new URL(route.request().url()).pathname}`);
    }
    return route.fallback();
  });
  return writes;
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

/** 打开任务详情 */
async function openDetail(page: Page, name = "实现登录接口") {
  await page.goto("/tasks");
  await page.getByText(name).first().click();
  await expect(page.getByText("任务详情")).toBeVisible();
}

/** 确认弹窗里的动作按钮 */
function confirmDisposal(page: Page, action: "delete" | "detach") {
  const name = action === "delete" ? "确认删除容器" : "确认脱离管理";
  return page.locator(".ant-modal").getByRole("button", { name, exact: true });
}

/** 公共前置：任务列表、详情、工作流、步骤、其它面板 */
async function stubPage(page: Page, tasks = [sampleTask()]) {
  await stubTaskList(page, tasks);
  for (const task of tasks) {
    await stubTaskDetail(page, task);
    await stubWorkflow(page, task.id as string);
    await stubStepsEmpty(page, task.id as string);
    await stubOtherPanels(page, task.id as string);
  }
}

/**
 * 行内「删除容器」按钮。
 *
 * 必须用精确名称：弹窗的确认按钮是「确认删除容器」，宽匹配会同时命中两者。
 */
function deleteContainerButton(page: Page) {
  return page.getByRole("button", { name: "删除容器", exact: true });
}

/** 行内「脱离管理」按钮，同样使用精确名称 */
function detachContainerButton(page: Page) {
  return page.getByRole("button", { name: "脱离管理", exact: true });
}

test.describe("容器记录展示", () => {
  test("打开详情后展示真实容器记录，且不发出任何处置请求", async ({ page }) => {
    await stubPage(page);
    const writes = await recordWrites(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls = await stubDisposal(page, () => ({ body: deleteResponse() }));

    await openDetail(page);

    await expect(page.getByText("任务容器", { exact: true })).toBeVisible();
    await expect(page.getByText("container-1", { exact: true })).toBeVisible();
    await expect(page.getByText("运行中", { exact: true })).toBeVisible();
    await expect(page.getByText("任务当前容器", { exact: true })).toBeVisible();
    await expect(page.getByText(/未脱离（仍受 AgentFlow 自动管理）/)).toBeVisible();
    // 两个动作都可见且各自独立
    await expect(deleteContainerButton(page)).toBeEnabled();
    await expect(detachContainerButton(page)).toBeEnabled();
    expect(calls).toHaveLength(0);
    expect(writes.filter((item) => item.includes("/containers/"))).toHaveLength(0);
  });

  test("历史容器标注为历史，不与当前容器混淆", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: {
        records: [
          sampleContainer({ id: "container-old", origin: "history", state: "stopped" }),
          sampleContainer(),
        ],
        total: 2,
      },
    }));
    await stubDisposal(page, () => ({ body: deleteResponse() }));

    await openDetail(page);

    await expect(page.getByText("历史容器", { exact: true })).toBeVisible();
    await expect(page.getByText(/这是该任务的历史容器/)).toBeVisible();
    await expect(page.getByText("已停止", { exact: true })).toBeVisible();
  });

  test("容器列表为空时说明不会创建容器", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({ body: { records: [], total: 0 } }));

    await openDetail(page);

    await expect(page.getByText(/该任务当前没有容器登记记录/)).toBeVisible();
    await expect(page.getByText(/本面板不会创建容器/)).toBeVisible();
    await expect(deleteContainerButton(page)).toHaveCount(0);
  });

  test("容器列表读取失败时不提供操作入口", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { error: { code: "not_found", message: "任务不存在" } },
      status: 404,
    }));
    const calls = await stubDisposal(page, () => ({ body: deleteResponse() }));

    await openDetail(page);

    await expect(page.getByText("读取任务容器失败")).toBeVisible();
    await expect(page.getByText(/当前没有可信的容器记录/)).toBeVisible();
    await expect(deleteContainerButton(page)).toHaveCount(0);
    expect(calls).toHaveLength(0);
  });

  test("已脱离管理的容器两个动作都禁用并说明原因", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: {
        records: [sampleContainer({ detached: true, state: "detached" })],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText(/不再接受自动删除或重复脱离/)).toBeVisible();
    await expect(deleteContainerButton(page)).toBeDisabled();
    await expect(detachContainerButton(page)).toBeDisabled();
  });

  test("未知状态原样展示，不被当作已成功或已删除", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: {
        records: [sampleContainer({ docker_state: "", state: "future_state" })],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("future_state")).toBeVisible();
    await expect(page.getByText(/未在当前版本中定义/)).toBeVisible();
    await expect(page.getByText(/服务端未提供/)).toBeVisible();
    await expect(page.getByText("已删除", { exact: true })).toHaveCount(0);
  });
});

test.describe("确认交互", () => {
  test("删除容器需要确认，取消确认不会发出请求", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls = await stubDisposal(page, () => ({ body: deleteResponse() }));

    await openDetail(page);
    await deleteContainerButton(page).click();

    await expect(page.getByText("确认删除该容器？")).toBeVisible();
    await expect(page.getByText(/共享镜像、共享缓存卷与远程项目配置目录不会被删除/)).toBeVisible();
    await expect(page.getByText("container-1", { exact: true }).last()).toBeVisible();

    await page
      .locator(".ant-modal")
      .getByRole("button", { name: /^取\s*消$/ })
      .click();
    await expect(page.getByText("确认删除该容器？")).toHaveCount(0);
    expect(calls).toHaveLength(0);
  });

  test("脱离管理需要确认，且文案说明不是删除、不等于已停止", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls = await stubDisposal(page, () => ({ body: detachResponse() }));

    await openDetail(page);
    await detachContainerButton(page).click();

    await expect(page.getByText("确认让该容器脱离 AgentFlow 管理？")).toBeVisible();
    await expect(page.getByText(/这不是删除/)).toBeVisible();
    await expect(page.getByText(/也不代表容器已经停止/)).toBeVisible();

    await page
      .locator(".ant-modal")
      .getByRole("button", { name: /^取\s*消$/ })
      .click();
    expect(calls).toHaveLength(0);
  });

  test("两个动作的确认文案互不相同", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    await stubDisposal(page, (action) => ({
      body: action === "delete" ? deleteResponse() : detachResponse(),
    }));

    await openDetail(page);

    await deleteContainerButton(page).click();
    await expect(page.getByText("确认删除该容器？")).toBeVisible();
    await page
      .locator(".ant-modal")
      .getByRole("button", { name: /^取\s*消$/ })
      .click();

    await detachContainerButton(page).click();
    await expect(page.getByText("确认让该容器脱离 AgentFlow 管理？")).toBeVisible();
    await expect(page.getByText("确认删除该容器？")).toHaveCount(0);
    await page
      .locator(".ant-modal")
      .getByRole("button", { name: /^取\s*消$/ })
      .click();
  });
});

test.describe("处置结果展示", () => {
  test("确认删除后按真实响应展示结果，只发一次 POST，路径与动作正确", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls = await stubDisposal(page, () => ({ body: deleteResponse() }));

    await openDetail(page);
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();

    await expect(page.getByText(/处置已完成：删除容器/)).toBeVisible();
    await expect(page.getByText(/容器记录已标记为已删除/)).toBeVisible();
    await expect(page.getByText(/处置容器不会重开任务/)).toBeVisible();

    expect(calls).toHaveLength(1);
    expect(calls[0]?.action).toBe("delete");
    expect(calls[0]?.url).toContain("/api/v1/tasks/task-1/containers/container-1/delete");
  });

  test("确认脱离管理后展示已脱离，且不调用删除接口", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls = await stubDisposal(page, () => ({ body: detachResponse() }));

    await openDetail(page);
    await detachContainerButton(page).click();
    await confirmDisposal(page, "detach").click();

    await expect(page.getByText(/处置已完成：脱离管理/)).toBeVisible();
    await expect(page.getByText(/容器记录已标记为脱离管理，容器现场保留/)).toBeVisible();

    expect(calls).toHaveLength(1);
    expect(calls[0]?.action).toBe("detach");
  });

  test("服务端拒绝（409 归属不匹配）时展示真实原因，不展示成功", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls = await stubDisposal(page, () => ({
      body: {
        error: {
          code: "state_conflict",
          message: "容器归属校验未通过: 容器登记的是任务 task-2，不是任务 task-1",
        },
      },
      status: 409,
    }));

    await openDetail(page);
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();

    await expect(page.getByText("删除容器请求失败")).toBeVisible();
    await expect(page.getByText(/归属校验未通过/)).toBeVisible();
    await expect(page.getByText(/本次请求没有取得可展示的处置结论/)).toBeVisible();
    await expect(page.getByText(/处置已完成/)).toHaveCount(0);
    // 不自动重发
    expect(calls).toHaveLength(1);
  });

  test("结果不确定（网络中断）时明确提示，且不自动重发", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    const calls: string[] = [];
    await page.route("**/api/v1/tasks/task-1/containers/**", (route: Route) => {
      if (route.request().method() !== "POST") return route.fallback();
      calls.push(route.request().url());
      return route.abort("failed");
    });

    await openDetail(page);
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();

    await expect(page.getByText("请求结果无法确认")).toBeVisible();
    await expect(page.getByText(/页面不会自动重发请求/)).toBeVisible();
    await expect(page.getByText(/请先刷新容器列表核验真实状态/)).toBeVisible();
    await expect(page.getByText(/处置已完成/)).toHaveCount(0);
    expect(calls).toHaveLength(1);
  });

  test("处置成功但任务刷新失败时，处置结论仍可见", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    await stubDisposal(page, () => ({ body: deleteResponse() }));

    await openDetail(page);
    // 处置之后才让任务详情刷新失败：验证"刷新失败不抹除已取得的处置结论"。
    await page.unroute("**/api/v1/tasks/task-1");
    await page.route("**/api/v1/tasks/task-1", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "读取任务失败" } }),
        contentType: "application/json",
        status: 500,
      });
    });
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();

    // 处置结论保留：详情刷新失败只附加提示，不抹除已取得的结论
    await expect(page.getByText(/处置已完成：删除容器/)).toBeVisible();
    await expect(page.getByText(/上方结论仍然有效/)).toBeVisible();
    // 页面顶部同时给出任务详情刷新失败提示（与容器面板的提示是两处独立提示）
    await expect(page.getByText(/自动刷新失败|操作失败/).first()).toBeVisible();
  });
});

test.describe("在途防重与竞态隔离", () => {
  test("在途时按钮禁用且强制重复点击只发出一次请求", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    // 用可控的挂起请求固定"在途"窗口，避免固定等待掩盖竞态。
    const calls: string[] = [];
    let release = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route("**/api/v1/tasks/task-1/containers/**", async (route: Route) => {
      if (route.request().method() !== "POST") return route.fallback();
      calls.push(route.request().url());
      await gate;
      return route.fulfill({
        body: JSON.stringify(deleteResponse()),
        contentType: "application/json",
        status: 200,
      });
    });

    await openDetail(page);
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();

    // 在途状态：动作按钮切换为进行中文案并禁用，同时给出进行中说明
    await expect(page.getByText(/正在请求服务端删除该容器/)).toBeVisible();
    const runningDelete = page.getByRole("button", { name: /删\s*除\s*中/ });
    await expect(runningDelete).toBeDisabled();
    await expect(detachContainerButton(page)).toBeDisabled();
    // 强制再点一次已禁用的按钮不会发送第二个请求
    await runningDelete.click({ force: true }).catch(() => undefined);
    await detachContainerButton(page)
      .click({ force: true })
      .catch(() => undefined);
    expect(calls).toHaveLength(1);

    // 放行响应后才出现结论
    release();
    await expect(page.getByText(/处置已完成：删除容器/)).toBeVisible();
    expect(calls).toHaveLength(1);
  });

  test("处置期间切换到任务 B，A 的迟到响应不出现在 B 的详情里", async ({ page }) => {
    const taskA = sampleTask({ id: "task-a", name: "任务 A" });
    const taskB = sampleTask({ id: "task-b", name: "任务 B" });
    await stubPage(page, [taskA, taskB]);
    await stubContainers(
      page,
      () => ({
        body: { records: [sampleContainer({ id: "container-a", task_id: "task-a" })], total: 1 },
      }),
      "task-a",
    );
    await stubContainers(
      page,
      () => ({
        body: { records: [sampleContainer({ id: "container-b", task_id: "task-b" })], total: 1 },
      }),
      "task-b",
    );
    let release = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route("**/api/v1/tasks/task-a/containers/**", async (route: Route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await gate;
      return route.fulfill({
        body: JSON.stringify(deleteResponse()),
        contentType: "application/json",
        status: 200,
      });
    });

    await openDetail(page, "任务 A");
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();
    // 确认弹窗关闭后立即切换任务：请求仍在途，响应会迟到。
    await expect(page.getByText(/正在请求服务端删除该容器/)).toBeVisible();
    await page.locator("#root").getByRole("button", { name: "关闭", exact: true }).click();
    await page.getByText("任务 B").first().click();
    await expect(page.getByText("container-b", { exact: true })).toBeVisible();

    // 放行 A 的迟到响应：它不得污染任务 B 的详情
    release();
    await expect(page.getByText(/处置已完成：删除容器/)).toHaveCount(0);
    await page.waitForTimeout(300);
    await expect(page.getByText(/处置已完成：删除容器/)).toHaveCount(0);
    await expect(page.getByText("container-b", { exact: true })).toBeVisible();
  });

  test("处置成功后不自动调用重试接口", async ({ page }) => {
    await stubPage(page);
    await stubContainers(page, () => ({
      body: { records: [sampleContainer()], total: 1 },
    }));
    await stubDisposal(page, () => ({ body: deleteResponse() }));
    const writes = await recordWrites(page);

    await openDetail(page);
    await deleteContainerButton(page).click();
    await confirmDisposal(page, "delete").click();
    await expect(page.getByText(/处置已完成：删除容器/)).toBeVisible();

    // 等一段时间确认没有任何自动后续动作
    await page.waitForTimeout(800);
    expect(writes.some((item) => item.includes("/retry"))).toBe(false);
    expect(writes.some((item) => item.includes("/execute-workflow"))).toBe(false);
    expect(writes.some((item) => item.includes("/start"))).toBe(false);
  });

  test("容器列表刷新失败时保留上一次成功结果并提示", async ({ page }) => {
    await stubPage(page);
    // 用显式开关控制失败时机：这里验证的是"手动刷新失败保留上一次成功结果"，
    // 与 React StrictMode 重复执行 effect 的读取次数无关。
    const calls: string[] = [];
    let fail = false;
    await page.route("**/api/v1/tasks/task-1/containers", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      calls.push(route.request().url());
      if (!fail) {
        return route.fulfill({
          body: JSON.stringify({ records: [sampleContainer()], total: 1 }),
          contentType: "application/json",
          status: 200,
        });
      }
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "读取容器记录失败" } }),
        contentType: "application/json",
        status: 500,
      });
    });

    await openDetail(page);
    await expect(page.getByText("container-1", { exact: true })).toBeVisible();

    fail = true;
    await page.getByRole("button", { name: /刷新容器/ }).click();

    await expect(page.getByText("容器列表刷新失败")).toBeVisible();
    await expect(page.getByText(/上一次成功读取的结果/)).toBeVisible();
    // 旧记录仍然可见，不被抹掉
    await expect(page.getByText("container-1", { exact: true })).toBeVisible();
    expect(calls.length).toBeGreaterThanOrEqual(2);
    // 操作入口仍然可用：刷新失败不等于容器不可操作
    await expect(deleteContainerButton(page)).toBeEnabled();
  });
});
