import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 显式重试入口的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH 或 Pi。
 * 用例只验证一件事：页面是否只用服务端 `can_retry` 决定重试入口是否可用，是否按真实响应
 * 区分"任务已重开 / 工作流是否启动"，并且从不自动重发有副作用的请求。
 *
 * 这些用例不能证明真实重试流程在容器内可用，只能证明 UI 对给定响应的处理。
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

/** 一份工作流执行记录样本，字段与 Go 端 `httpapi.WorkflowRunResponse` 一致 */
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
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/**
 * 一份重试资格响应样本，字段与 Go 端 `httpapi.RetryEligibilityResponse` 一致。
 *
 * `blocking` 在服务端没有任何阻塞项时可能序列化为 `null`，这里保持一致。
 */
function sampleEligibility(overrides: Record<string, unknown> = {}) {
  return {
    blocking: null,
    can_retry: true,
    summary: "允许开启一次新的执行周期",
    task_id: "task-1",
    task_status: "failed",
    ...overrides,
  };
}

/** 一份重试响应样本，字段与 Go 端 `httpapi.RetryTaskResponse` 一致 */
function sampleRetry(overrides: Record<string, unknown> = {}) {
  return {
    lease_epoch: 3,
    lease_owner: "retry-1",
    previous_status: "failed",
    run: sampleRun({ run_id: "run-retry-1" }),
    task_id: "task-1",
    task_status: "developing",
    workflow_started: true,
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

/** 拦截任务列表；可传入多个任务用于验证切换任务的行为 */
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

/** 拦截代码保存记录列表与续跑资格（本页其余只读面板，返回空结论） */
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
}

/** 拦截重试资格查询；返回被调用的 URL 列表，用于断言刷新行为 */
async function stubEligibility(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const urls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/retry-eligibility`, (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    urls.push(route.request().url());
    const result = responder(urls.length - 1);
    return route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return urls;
}

/** 记录重试写请求的调用次数与请求体 */
async function stubRetry(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
) {
  const calls: { body: unknown; url: string }[] = [];
  await page.route("**/api/v1/tasks/task-1/retry", async (route: Route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const raw = route.request().postData();
    calls.push({
      body: raw ? (JSON.parse(raw) as unknown) : undefined,
      url: route.request().url(),
    });
    const result = responder(calls.length - 1);
    return route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 202,
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

/** 重试按钮：只在服务端允许重试且资格快照属于当前任务时可用 */
function retryButton(page: Page) {
  return page.getByRole("button", { name: /重试任务|重\s*试\s*任\s*务/ });
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

test.describe("重试资格与入口可用性", () => {
  test("can_retry=true 时允许发起重试，并在点击后用真实响应展示结果", async ({ page }) => {
    await stubPage(page);
    const eligibilityURLs = await stubEligibility(page, () => ({
      body: sampleEligibility({ can_retry: true }),
    }));
    const calls = await stubRetry(page, () => ({ body: sampleRetry() }));

    await openDetail(page);

    await expect(page.getByText("重试任务", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("当前查询结果允许重试")).toBeVisible();
    await expect(page.getByText("允许开启一次新的执行周期")).toBeVisible();
    expect(eligibilityURLs.length).toBeGreaterThanOrEqual(1);
    expect(eligibilityURLs[0]).toContain("/api/v1/tasks/task-1/retry-eligibility");
    // 只有拿到资格才可能点击
    await expect(retryButton(page)).toBeEnabled();

    await retryButton(page).click();

    // 真实响应：任务已被重开且工作流已启动
    await expect(page.getByText("重试已重开任务并启动工作流")).toBeVisible();
    await expect(page.getByText(/run-retry-1/)).toBeVisible();
    await expect(page.getByText(/执行代次：3/)).toBeVisible();
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toContain("/api/v1/tasks/task-1/retry");
    // 只发送服务端支持的执行参数：默认不附加任何额外字段
    expect(calls[0]?.body).toEqual({});
  });

  test("can_retry=false 时展示全部阻塞原因且操作不可用", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["status_not_retryable", "lease_held", "future_retry_blocker"],
        can_retry: false,
        summary: "任务不能重试：status_not_retryable、lease_held",
      }),
    }));
    const calls = await stubRetry(page, () => ({ body: sampleRetry() }));

    await openDetail(page);

    await expect(page.getByText("当前存在阻塞，不能重试")).toBeVisible();
    await expect(page.getByText("任务状态不允许重试", { exact: true })).toBeVisible();
    await expect(page.getByText("任务执行权未释放", { exact: true })).toBeVisible();
    // 未知分类原样保留，不被吞掉
    await expect(page.getByText("future_retry_blocker").first()).toBeVisible();
    await expect(page.getByText(/未定义/).first()).toBeVisible();
    await expect(page.getByText("任务不能重试：status_not_retryable、lease_held")).toBeVisible();
    await expect(page.getByText("当前查询结果允许重试")).toHaveCount(0);

    // 操作不可用：点击也不会发出请求
    await expect(retryButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  test("资格查询失败时不启用重试，并提供重新查询入口", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({
      body: { error: { code: "internal_error", message: "任务重试服务未初始化" } },
      status: 503,
    }));
    const calls = await stubRetry(page, () => ({ body: sampleRetry() }));

    await openDetail(page);

    await expect(page.getByText("查询重试资格失败")).toBeVisible();
    await expect(page.getByText(/任务重试服务未初始化/)).toBeVisible();
    await expect(page.getByText(/重试资格当前未知/)).toBeVisible();
    await expect(page.getByText("当前查询结果允许重试")).toHaveCount(0);
    await expect(retryButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });
});

test.describe("重试结果语义", () => {
  test("workflow_started=false 时明确说明工作流没有启动", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_retry: true }) }));
    await stubRetry(page, () => ({
      body: sampleRetry({
        failure: "容器准备失败：镜像不可用",
        failure_stage: "container",
        run: undefined,
        task_status: "preparing",
        workflow_started: false,
      }),
    }));

    await openDetail(page);
    await retryButton(page).click();

    await expect(page.getByText("任务已重开，但工作流没有启动")).toBeVisible();
    await expect(page.getByText(/失败阶段：准备容器/)).toBeVisible();
    await expect(page.getByText(/容器准备失败：镜像不可用/)).toBeVisible();
    // 真实任务状态如实展示，不被说成执行中
    await expect(page.getByText(/重试前 失败 → 当前 准备环境/)).toBeVisible();
    // 绝不出现"已启动"的结论
    await expect(page.getByText("重试已重开任务并启动工作流")).toHaveCount(0);
  });

  test("重试请求失败时展示服务端错误，且不自动再次提交", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_retry: true }) }));
    const calls = await stubRetry(page, () => ({
      body: { error: { code: "state_conflict", message: "任务的状态或执行权已变更，重试未生效" } },
      status: 409,
    }));

    await openDetail(page);
    await retryButton(page).click();

    await expect(page.getByText("重试请求失败")).toBeVisible();
    await expect(page.getByText("任务的状态或执行权已变更，重试未生效")).toBeVisible();
    await expect(page.getByText("重试已重开任务并启动工作流")).toHaveCount(0);

    // 等待一段时间，确认没有任何自动重发
    await page.waitForTimeout(600);
    expect(calls).toHaveLength(1);
  });

  test("请求结果无法确认时明确提示，且不自动重复提交", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_retry: true }) }));
    const calls = await stubRetry(page, () => {
      return { body: { error: { code: "internal_error", message: "服务内部错误" } }, status: 500 };
    });

    await openDetail(page);
    await retryButton(page).click();

    await expect(page.getByText("重试请求失败")).toBeVisible();
    await expect(page.getByText("服务内部错误")).toBeVisible();
    await page.waitForTimeout(400);
    expect(calls).toHaveLength(1);
  });
});

test.describe("刷新与竞态", () => {
  test("重试后资格与任务快照按真实响应刷新", async ({ page }) => {
    await stubPage(page);
    let retried = false;
    const eligibilityURLs = await stubEligibility(page, () =>
      retried
        ? {
            body: sampleEligibility({
              blocking: ["status_not_retryable"],
              can_retry: false,
              summary: "任务状态不允许重试：当前已是 developing",
              task_status: "developing",
            }),
          }
        : { body: sampleEligibility({ can_retry: true }) },
    );
    await stubRetry(page, () => {
      retried = true;
      return { body: sampleRetry() };
    });

    await openDetail(page);
    await expect(page.getByText("当前查询结果允许重试")).toBeVisible();
    const before = eligibilityURLs.length;

    await retryButton(page).click();
    await expect(page.getByText("重试已重开任务并启动工作流")).toBeVisible();

    // 重新查询资格：服务端结论变化后如实展示
    await expect(page.getByText("当前存在阻塞，不能重试")).toBeVisible();
    await expect(page.getByText("任务状态不允许重试", { exact: true })).toBeVisible();
    expect(eligibilityURLs.length).toBeGreaterThan(before);
  });

  test("重试后刷新失败不覆盖已经取得的操作结论", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_retry: true }) }));
    await stubRetry(page, () => ({ body: sampleRetry() }));

    await openDetail(page);

    // 重试之后让任务详情与资格查询失败：已取得的操作结论必须仍然可见。
    await page.route("**/api/v1/tasks/task-1", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      });
    });
    await stubEligibility(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));

    await retryButton(page).click();

    await expect(page.getByText("重试已重开任务并启动工作流")).toBeVisible();
    await expect(page.getByText("状态刷新失败").first()).toBeVisible();
    await expect(page.getByText(/上方结论仍然有效/)).toBeVisible();
  });

  test("切换任务后旧任务的资格与重试结果不会污染新任务", async ({ page }) => {
    const first = sampleTask();
    const second = sampleTask({ id: "task-2", name: "补充接口测试", status: "failed" });
    await stubPage(page, [first, second]);
    await stubEligibility(
      page,
      () => ({ body: sampleEligibility({ can_retry: true, summary: "第一个任务允许重试" }) }),
      "task-1",
    );
    await stubEligibility(
      page,
      () => ({
        body: sampleEligibility({
          blocking: ["lease_held"],
          can_retry: false,
          summary: "第二个任务存在阻塞",
          task_id: "task-2",
        }),
      }),
      "task-2",
    );
    await stubRetry(page, () => ({ body: sampleRetry({ task_id: "task-1" }) }));

    await openDetail(page);
    await expect(page.getByText("第一个任务允许重试")).toBeVisible();
    await retryButton(page).click();
    await expect(page.getByText("重试已重开任务并启动工作流")).toBeVisible();

    // 切换到第二个任务
    await page.getByText("补充接口测试").first().click();
    await expect(page.getByText("第二个任务存在阻塞")).toBeVisible();
    // 第一个任务的结论与操作结果都不再出现
    await expect(page.getByText("第一个任务允许重试")).toHaveCount(0);
    await expect(page.getByText("重试已重开任务并启动工作流")).toHaveCount(0);
    await expect(retryButton(page)).toBeDisabled();
  });

  test("打开详情只读取资格，不发起任何写请求", async ({ page }) => {
    const writes = await recordWrites(page);
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_retry: true }) }));

    await openDetail(page);
    await expect(page.getByText("当前查询结果允许重试")).toBeVisible();
    await page.getByText("刷新重试资格").click();
    await expect(page.getByText("当前查询结果允许重试")).toBeVisible();

    expect(writes).toEqual([]);
  });
});
