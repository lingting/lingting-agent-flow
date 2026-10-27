import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 任务执行进度的自动刷新（浏览器行为）。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型服务。
 * 用例关注真实业务行为：
 *   - 工作流处于活动态时，后续服务端快照会更新当前步骤与进度；
 *   - 进入终态后不再按活动态周期持续请求；
 *   - 切换任务后旧任务的迟到响应不会覆盖新任务；
 *   - 刷新失败保留最后一次成功展示并给出明确提示；
 *   - 手动刷新仍然有效，且不会触发任何执行类请求
 *   - 关闭详情后自动刷新请求停止。
 *
 * 自动刷新周期固定为 5 秒，因此断言窗口按秒级等待（不超过一个周期 + 余量），
 * 不使用随机 sleep 掩盖竞态。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    created_at: "2026-10-26T08:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "developing",
    status_finished: false,
    status_terminal: false,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一条工作流执行记录样本 */
function sampleRun(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    code_saver_available: true,
    completed_steps: 0,
    last_completed_sequence: 0,
    plan_digest: "digest",
    revision: 1,
    run_id: "run-1",
    stage: "step_execution",
    status: "running",
    status_active: true,
    status_terminal: false,
    task_id: "task-1",
    total_steps: 2,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一份步骤响应样本，字段与 Go 端 `httpapi.StepResponse` 一致 */
function sampleStep(overrides: Record<string, unknown> = {}) {
  return {
    created_at: "2026-10-26T08:00:00Z",
    id: "step-1",
    name: "实现登录步骤",
    objective: "按验收条件实现登录接口",
    sequence: 1,
    status: "pending",
    status_terminal: false,
    step_type: "develop",
    task_id: "task-1",
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 统一应答：JSON 响应 */
async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status });
}

/** 健康检查，让页面外壳处于已连接状态 */
async function stubHealth(page: Page) {
  await page.route("**/api/v1/health", (route) =>
    json(route, {
      checks: [
        { name: "http_api", status: "pass" },
        { name: "database", status: "pass" },
      ],
      checked_at: "2026-10-26T08:00:00Z",
      status: "ok",
    }),
  );
}

/** 任务列表 */
async function stubTaskList(page: Page, tasks = [sampleTask()]) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    json(route, { current: 1, records: tasks, size: 20, total: tasks.length }),
  );
}

/**
 * 任务详情：记录每次请求，由 responder 决定响应。
 *
 * 返回的 `calls` 可用于断言"自动刷新是否真的重新请求"与请求次数。
 */
async function stubTaskDetail(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const calls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}`, async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    calls.push(route.request().url());
    const result = responder(calls.length - 1);
    await json(route, result.body, result.status ?? 200);
  });
  return calls;
}

/** 工作流进度：记录每次请求，由 responder 决定响应 */
async function stubWorkflow(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const calls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/workflow`, async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    calls.push(route.request().url());
    const result = responder(calls.length - 1);
    await json(route, result.body, result.status ?? 200);
  });
  return calls;
}

/** 工作流进度响应样本 */
function workflowBody(
  runOverrides: Record<string, unknown> = {},
  overrides: Record<string, unknown> = {},
) {
  return {
    has_run: true,
    run: sampleRun(runOverrides),
    steps: [],
    task_id: "task-1",
    task_status: "developing",
    ...overrides,
  };
}

/** 步骤列表与执行记录：返回固定数据，避免被自动刷新放大成噪声 */
async function stubExecutionRecords(page: Page, steps = [sampleStep()]) {
  await page.route("**/api/v1/tasks/task-1/steps?*", (route: Route) =>
    json(route, { current: 1, records: steps, size: 100, total: steps.length }),
  );
  await page.route("**/api/v1/tasks/task-1/steps/step-1/attempts?*", (route: Route) =>
    json(route, { current: 1, records: [], size: 100, total: 0 }),
  );
  await page.route("**/api/v1/tasks/task-1/steps/step-1/validations", (route: Route) =>
    json(route, { records: [], state: "unverified", step_id: "step-1", total: 0 }),
  );
  await page.route("**/api/v1/tasks/task-1/code-saves", (route: Route) =>
    json(route, { records: [], total: 0 }),
  );
  await page.route(`**/api/v1/tasks/task-1/resume-eligibility`, (route: Route) =>
    json(route, {
      blocking: null,
      can_resume: false,
      code_save_action: "proceed",
      code_save_records: [],
      code_save_summary: "代码保存记录中没有结果不明的外部副作用",
      next_actions: null,
      summary: "工作流仍在推进，无需续跑",
      task_id: "task-1",
      task_status: "developing",
    }),
  );
}

/**
 * 记录全部写请求。
 *
 * 自动刷新必须只读：任何 POST / PUT / PATCH / DELETE 都说明它触发了有副作用的操作。
 */
async function recordWrites(page: Page) {
  const writes: string[] = [];
  await page.route("**/api/v1/**", async (route: Route) => {
    const method = route.request().method();
    if (method !== "GET" && method !== "HEAD") {
      writes.push(`${method} ${new URL(route.request().url()).pathname}`);
    }
    return route.fallback();
  });
  return writes;
}

/** 打开任务详情 */
async function openDetail(page: Page, name = "实现登录接口") {
  await page.goto("/tasks");
  await page.getByText(name).first().click();
  await expect(page.getByText("任务详情")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("活动态自动刷新", () => {
  test("工作流推进后自动更新当前步骤与进度", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    // 首个快照：第 1 步执行中；后续快照：第 2 步执行中，进度 1/2
    const workflows = await stubWorkflow(page, (index) => ({
      body:
        index === 0
          ? {
              has_run: true,
              run: sampleRun({ current_step_id: "step-1", completed_steps: 0 }),
              steps: [
                {
                  sequence: 1,
                  status: "developing",
                  step_id: "step-1",
                  step_type: "develop",
                  title: "实现登录步骤",
                },
                {
                  sequence: 2,
                  status: "pending",
                  step_id: "step-2",
                  step_type: "develop",
                  title: "补充单元测试",
                },
              ],
              task_id: "task-1",
              task_status: "developing",
            }
          : {
              has_run: true,
              run: sampleRun({
                completed_steps: 1,
                current_step_id: "step-2",
                revision: 2,
              }),
              steps: [
                {
                  sequence: 1,
                  status: "completed",
                  step_id: "step-1",
                  step_type: "develop",
                  title: "实现登录步骤",
                },
                {
                  sequence: 2,
                  status: "developing",
                  step_id: "step-2",
                  step_type: "develop",
                  title: "补充单元测试",
                },
              ],
              task_id: "task-1",
              task_status: "developing",
            },
    }));
    await stubExecutionRecords(page);

    await openDetail(page);

    // 首个快照：进度 0 / 2，当前步骤为 step-1
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();
    await expect(page.getByText("step-1", { exact: true })).toBeVisible();

    // 不点击任何按钮：下一个周期之后页面自动更新到服务端的新快照
    await expect(page.getByText("已完成 1 / 2 步")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("step-2", { exact: true })).toBeVisible();
    // 确实发出了后续请求，而不是靠本地推断
    expect(workflows.length).toBeGreaterThanOrEqual(2);
  });

  test("自动刷新只读取状态，不触发任何执行类接口", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    const workflows = await stubWorkflow(page, () => ({ body: workflowBody() }));
    await stubExecutionRecords(page);
    const writes = await recordWrites(page);

    await openDetail(page);
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();

    // 至少跨过一个自动刷新周期
    await expect
      .poll(() => workflows.length, { intervals: [500], timeout: 15_000 })
      .toBeGreaterThanOrEqual(2);

    expect(writes).toEqual([]);
    await expect(page.getByText("执行中", { exact: true }).first()).toBeVisible();
  });
});

test.describe("停止刷新的条件", () => {
  test("工作流进入终态后不再按活动态周期请求", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask({ status: "completed" }) }));
    // 首个快照仍是活动态，随后变为 completed
    const workflows = await stubWorkflow(page, (index) => ({
      body:
        index === 0
          ? workflowBody()
          : workflowBody(
              {
                completed_steps: 2,
                status: "completed",
                status_active: false,
                status_terminal: true,
              },
              { task_status: "completed" },
            ),
    }));
    await stubExecutionRecords(page);

    await openDetail(page);
    await expect(
      page.getByText("本次工作流执行已结束，已停止自动刷新；需要时请手动刷新。"),
    ).toBeVisible({
      timeout: 15_000,
    });
    const settled = workflows.length;

    // 再等两个周期：不应继续按活动态周期请求
    await page.waitForTimeout(11_000);
    expect(workflows.length).toBe(settled);
    await expect(page.getByText("自动刷新已停止")).toBeVisible();
  });

  test("blocked 等待人工处置时停止自动刷新并如实提示", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    await stubWorkflow(page, () => ({
      body: workflowBody({ error_summary: "验证未通过", status: "blocked", status_active: false }),
    }));
    await stubExecutionRecords(page);

    await openDetail(page);

    await expect(
      page.getByText("工作流已停止自动推进并等待人工处置，已停止自动刷新；需要时请手动刷新。"),
    ).toBeVisible();
  });

  test("未知工作流状态不会被当作活动态或终态", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    const workflows = await stubWorkflow(page, () => ({
      body: workflowBody({ status: "future_status", status_active: false, status_terminal: false }),
    }));
    await stubExecutionRecords(page);

    await openDetail(page);

    await expect(
      page.getByText("服务端返回了未知的工作流状态，已停止自动刷新；请手动刷新确认最新状态。"),
    ).toBeVisible();
    // 不被解释成已完成或失败
    await expect(
      page.getByText("本次工作流执行已结束，已停止自动刷新；需要时请手动刷新。"),
    ).toHaveCount(0);
    await expect(page.getByText("已完成", { exact: true })).toHaveCount(0);
    const settled = workflows.length;
    await page.waitForTimeout(11_000);
    expect(workflows.length).toBe(settled);
  });
});

test.describe("切换任务与过期响应", () => {
  test("从任务 A 切到任务 B 后 A 的迟到响应不覆盖 B", async ({ page }) => {
    const first = sampleTask();
    const second = sampleTask({ id: "task-2", name: "补充接口测试" });
    await stubTaskList(page, [first, second]);

    // 任务 A 的详情与工作流被显式延迟，返回时详情已经切到 B
    let releaseA: () => void = () => {};
    const gateA = new Promise<void>((resolve) => {
      releaseA = resolve;
    });
    await page.route("**/api/v1/tasks/task-1", async (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      await gateA;
      await json(route, first);
    });
    await page.route("**/api/v1/tasks/task-1/workflow", async (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      await gateA;
      await json(route, workflowBody({ current_step_id: "step-a" }));
    });
    await stubTaskDetail(page, () => ({ body: second }), "task-2");
    await stubWorkflow(
      page,
      () => ({ body: workflowBody({ current_step_id: "step-b" }, { task_id: "task-2" }) }),
      "task-2",
    );
    await stubExecutionRecords(page);

    await page.goto("/tasks");
    // 打开 A：A 的详情与工作流请求被挂起，页面还没有可展示的 A 数据
    await page.getByText("实现登录接口").first().click();
    await expect(page.getByText("任务详情")).toBeVisible();

    // 切换到 B：以 B 的真实数据展示
    await page.getByText("补充接口测试").first().click();
    await expect(page.getByText("step-b", { exact: true })).toBeVisible();

    // 放开 A 的迟到响应：不得覆盖 B 的展示
    releaseA();
    await page.waitForTimeout(1_000);
    await expect(page.getByText("step-b", { exact: true })).toBeVisible();
    await expect(page.getByText("step-a", { exact: true })).toHaveCount(0);
  });

  test("关闭详情后自动刷新停止", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    const workflows = await stubWorkflow(page, () => ({ body: workflowBody() }));
    await stubExecutionRecords(page);

    await openDetail(page);
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();
    await page.getByText("关闭", { exact: true }).click();
    await expect(page.getByText("任务详情")).toHaveCount(0);

    const settled = workflows.length;
    await page.waitForTimeout(11_000);
    expect(workflows.length).toBe(settled);
  });
});

test.describe("刷新失败与手动刷新", () => {
  test("刷新失败保留最后一次成功展示并给出明确提示", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    // 首次成功，之后失败：不得清空已展示的真实数据
    let failing = false;
    await stubWorkflow(page, () =>
      failing
        ? { body: { error: { code: "internal_error", message: "服务内部错误" } }, status: 500 }
        : { body: workflowBody() },
    );
    await stubExecutionRecords(page);

    await openDetail(page);
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();

    failing = true;

    await expect(page.getByText("自动刷新失败")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(/服务内部错误/)).toBeVisible();
    await expect(page.getByText(/上一次成功获取的状态/)).toBeVisible();
    // 上一次成功的工作流进度与任务状态仍然展示，没有被清空
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();
    await expect(page.getByText("开发中", { exact: true }).first()).toBeVisible();
    // 刷新失败不被当作工作流终止
    await expect(
      page.getByText("本次工作流执行已结束，已停止自动刷新；需要时请手动刷新。"),
    ).toHaveCount(0);
  });

  test("手动刷新仍然有效，并按服务端最新快照更新", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    let current = workflowBody();
    const workflows = await stubWorkflow(page, () => ({ body: current }));
    await stubExecutionRecords(page);

    await openDetail(page);
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();

    current = workflowBody({ completed_steps: 2, revision: 7 });
    await page.getByText("刷新进度", { exact: true }).click();

    await expect(page.getByText("已完成 2 / 2 步")).toBeVisible();
    expect(workflows.length).toBeGreaterThanOrEqual(2);
  });

  test("手动刷新之后自动刷新仍在继续", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => ({ body: sampleTask() }));
    // 每次请求返回的进度都比上一次多一步，用于区分"手动刷新"与"自动刷新"
    const workflows = await stubWorkflow(page, (index) => ({
      body: workflowBody({ completed_steps: index, revision: index + 1 }),
    }));
    await stubExecutionRecords(page);

    await openDetail(page);
    await expect(page.getByText("已完成 0 / 2 步")).toBeVisible();

    await page.getByText("刷新进度", { exact: true }).click();
    await expect(page.getByText("已完成 1 / 2 步")).toBeVisible();

    // 手动刷新之后仍然会按周期自动更新
    await expect(page.getByText("已完成 2 / 2 步")).toBeVisible({ timeout: 15_000 });
    expect(workflows.length).toBeGreaterThanOrEqual(3);
  });

  test("任务开始时间等字段随自动刷新更新", async ({ page }) => {
    await stubTaskList(page);
    const details = await stubTaskDetail(page, (index) => ({
      body:
        index === 0
          ? sampleTask()
          : sampleTask({
              error: "验证未通过：go vet 失败",
              started_at: "2026-10-26T08:00:05Z",
            }),
    }));
    await stubWorkflow(page, () => ({ body: workflowBody() }));
    await stubExecutionRecords(page);

    await openDetail(page);
    await expect(page.getByText(/验证未通过：go vet 失败/)).toBeVisible({ timeout: 15_000 });
    expect(details.length).toBeGreaterThanOrEqual(2);
  });
});
