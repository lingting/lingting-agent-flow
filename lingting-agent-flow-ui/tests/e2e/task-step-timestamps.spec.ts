import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 工作流步骤起止时间的浏览器表现（P4-M3-6）。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型服务。
 * 关注真实业务行为：
 *   - 步骤列表展示服务端返回的真实开始与结束时间；
 *   - 未开始、时间缺失与时间非法时显示中性占位符，不显示伪造时间；
 *   - 仍在执行的步骤不显示完成时间；
 *   - 自动刷新拿到新快照后时间同步更新，刷新失败保留最后一次成功展示。
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
    total_steps: 1,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
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
async function stubTaskList(page: Page) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    json(route, { current: 1, records: [sampleTask()], size: 20, total: 1 }),
  );
}

/** 任务详情：记录每次请求，由 responder 决定响应 */
async function stubTaskDetail(page: Page, responder: (index: number) => unknown) {
  const calls: string[] = [];
  await page.route("**/api/v1/tasks/task-1", async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    calls.push(route.request().url());
    await json(route, responder(calls.length - 1));
  });
  return calls;
}

/** 工作流进度：记录每次请求，由 responder 决定响应 */
async function stubWorkflow(page: Page, responder: (index: number) => unknown) {
  const calls: string[] = [];
  await page.route("**/api/v1/tasks/task-1/workflow", async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    calls.push(route.request().url());
    const body = responder(calls.length - 1);
    if (body instanceof Error) {
      await json(route, { error: { code: "internal_error", message: body.message } }, 500);
      return;
    }
    await json(route, body);
  });
  return calls;
}

/** 步骤列表：记录每次请求，由 responder 决定返回哪些步骤 */
async function stubSteps(page: Page, responder: (index: number) => unknown[]) {
  const calls: string[] = [];
  await page.route("**/api/v1/tasks/task-1/steps?*", async (route: Route) => {
    calls.push(route.request().url());
    const records = responder(calls.length - 1);
    await json(route, { current: 1, records, size: 100, total: records.length });
  });
  return calls;
}

/** 其余执行记录接口返回空数据，避免被自动刷新放大成噪声 */
async function stubEmptyRecords(page: Page) {
  await page.route("**/api/v1/tasks/task-1/steps/step-1/attempts?*", (route: Route) =>
    json(route, { current: 1, records: [], size: 100, total: 0 }),
  );
  await page.route("**/api/v1/tasks/task-1/steps/step-1/validations", (route: Route) =>
    json(route, { records: [], state: "unverified", step_id: "step-1", total: 0 }),
  );
  await page.route("**/api/v1/tasks/task-1/code-saves", (route: Route) =>
    json(route, { records: [], total: 0 }),
  );
  await page.route("**/api/v1/tasks/task-1/resume-eligibility", (route: Route) =>
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

/** 打开任务详情并等待执行操作面板 */
async function openDetail(page: Page) {
  await page.goto("/tasks");
  await page.getByText("实现登录接口").first().click();
  await expect(page.getByText("执行操作")).toBeVisible();
}

/**
 * 时间展示的匹配模式。
 *
 * 断言不依赖具体时区与本地化格式：浏览器与 Node 的默认 locale 可能不同，逐个字符
 * 比对会得到假失败。这里只验证"是否显示了真实时刻"与"是否显示为占位符"这两件
 * 与业务语义相关的事实。
 */
const WITH_START_AND_FINISH = /^开始 \d.* · 结束 \d.*$/;
const WITH_START_ONLY = /^开始 \d.* · 结束 —$/;
const NO_TIME_TEXT = "开始 — · 结束 —";

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("步骤起止时间展示", () => {
  test("展示服务端返回的真实开始与结束时间", async ({ page }) => {
    const started = "2026-10-26T08:00:00Z";
    const finished = "2026-10-26T08:02:30Z";
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask());
    await stubWorkflow(page, () => workflowBody());
    await stubSteps(page, () => [
      sampleStep({
        finished_at: finished,
        started_at: started,
        status: "completed",
        status_terminal: true,
      }),
    ]);
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText("计划步骤")).toBeVisible();
    await expect(page.getByText(WITH_START_AND_FINISH)).toBeVisible();
    await expect(page.getByText(/^.*→.*（耗时 2 分 30 秒）$/)).toBeVisible();
  });

  test("未开始的步骤显示中性占位符而不是伪造时间", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask({ status: "planned" }));
    await stubWorkflow(page, () => workflowBody({ status_active: false, status: "blocked" }));
    await stubSteps(page, () => [sampleStep()]);
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText(NO_TIME_TEXT)).toBeVisible();
    await expect(page.getByText("尚未开始")).toBeVisible();
    // 不得出现任何具体时刻（created_at / updated_at 都不是执行时间）
    await expect(page.getByText(WITH_START_AND_FINISH)).toHaveCount(0);
    await expect(page.getByText(WITH_START_ONLY)).toHaveCount(0);
  });

  test("正在执行的步骤只显示开始时间，不显示完成时间", async ({ page }) => {
    const started = "2026-10-26T08:00:05Z";
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask());
    await stubWorkflow(page, () => workflowBody());
    await stubSteps(page, () => [
      sampleStep({ started_at: started, status: "running", status_terminal: false }),
    ]);
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText(WITH_START_ONLY)).toBeVisible();
    await expect(page.getByText("执行中").first()).toBeVisible();
    // 仍在执行不得显示耗时说明
    await expect(page.getByText(/耗时/)).toHaveCount(0);
  });

  test("时间格式非法时显示占位符，不把原始文本当成时间", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask());
    await stubWorkflow(page, () => workflowBody());
    await stubSteps(page, () => [
      sampleStep({ finished_at: "不是时间", started_at: "2026-99-99T99:99:99Z" }),
    ]);
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText(NO_TIME_TEXT)).toBeVisible();
    await expect(page.getByText("不是时间")).toHaveCount(0);
  });

  test("失败与待核验步骤保留真实状态与时间", async ({ page }) => {
    const started = "2026-10-26T08:00:00Z";
    const finished = "2026-10-26T08:05:00Z";
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask({ status: "recovery_required" }));
    await stubWorkflow(page, () => workflowBody({ status: "blocked", status_active: false }));
    await stubSteps(page, () => [
      sampleStep({
        error: "go vet 未通过",
        finished_at: finished,
        started_at: started,
        status: "failed",
        status_terminal: true,
      }),
      sampleStep({
        finished_at: finished,
        id: "step-2",
        name: "补齐测试",
        sequence: 2,
        started_at: started,
        status: "recovery_required",
      }),
    ]);
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText("失败").first()).toBeVisible();
    await expect(page.getByText("待核验").first()).toBeVisible();
    await expect(page.getByText("go vet 未通过")).toBeVisible();
    await expect(page.getByText(WITH_START_AND_FINISH).first()).toBeVisible();
    // 失败与待核验的步骤不得显示成已完成（进度条文案「已完成 0 / 1 步」不算）
    await expect(page.getByText("已完成", { exact: true })).toHaveCount(0);
  });
});

test.describe("自动刷新与时间同步", () => {
  test("自动刷新拿到新快照后时间同步更新", async ({ page }) => {
    const started = "2026-10-26T08:00:00Z";
    const finished = "2026-10-26T08:02:00Z";
    // 由用例显式推进：只有置位之后服务端才会返回"已结束"的快照，避免把首次加载
    // 之后的第二次请求误当成自动刷新。
    let advanced = false;
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask());
    await stubWorkflow(page, () =>
      advanced ? workflowBody({ current_step_id: "step-1", revision: 2 }) : workflowBody(),
    );
    await stubSteps(page, () =>
      advanced
        ? [
            sampleStep({
              finished_at: finished,
              started_at: started,
              status: "completed",
              status_terminal: true,
            }),
          ]
        : [sampleStep({ started_at: started, status: "running" })],
    );
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText(WITH_START_ONLY)).toBeVisible();
    advanced = true;
    // 自动刷新（5 秒周期）拿到已完成快照后，结束时间出现。
    await expect(page.getByText(WITH_START_AND_FINISH)).toBeVisible({ timeout: 15_000 });
  });

  test("刷新失败保留最后一次成功展示的时间", async ({ page }) => {
    const started = "2026-10-26T08:00:00Z";
    const finished = "2026-10-26T08:02:00Z";
    await stubTaskList(page);
    await stubTaskDetail(page, () => sampleTask());
    await stubWorkflow(page, (index) => (index === 0 ? workflowBody() : new Error("服务内部错误")));
    await stubSteps(page, () => [
      sampleStep({
        finished_at: finished,
        started_at: started,
        status: "completed",
        status_terminal: true,
      }),
    ]);
    await stubEmptyRecords(page);

    await openDetail(page);
    await expect(page.getByText(WITH_START_AND_FINISH)).toBeVisible();
    await expect(page.getByText("自动刷新失败")).toBeVisible({ timeout: 15_000 });
    // 刷新失败不清空已有时间
    await expect(page.getByText(WITH_START_AND_FINISH)).toBeVisible();
  });
});
