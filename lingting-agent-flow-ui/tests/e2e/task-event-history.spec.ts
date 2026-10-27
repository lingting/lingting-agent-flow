import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 事件时间线与修复周期历史的浏览器表现（P4-M3-8）。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH、Git
 * 或模型服务。用例关注真实业务行为：
 *   - 任务详情展示服务端返回的真实事件时间线；
 *   - 事件查询失败后可以重试，失败不被伪装成空历史；
 *   - 未知事件类型仍能显示，并说明该类型未在当前版本定义；
 *   - 选择步骤后展示该步骤的修复周期历史；
 *   - 没有周期与未关闭周期使用中性文案，不推断成功或失败；
 *   - 切换任务或步骤后旧响应不会覆盖新数据；
 *   - 5 秒自动刷新不会无条件重复加载事件历史；
 *   - 浏览与刷新流程不触发执行、验证、重试、恢复或代码保存。
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

/** 一条事件响应样本，字段与 Go 端 `httpapi.EventResponse` 一致 */
function sampleEvent(overrides: Record<string, unknown> = {}) {
  return {
    created_at: "2026-10-26T08:00:00Z",
    event_label: "任务创建",
    event_type: "task.created",
    id: 1,
    task_id: "task-1",
    type_known: true,
    ...overrides,
  };
}

/** 一个修复周期响应样本，字段与 Go 端 `httpapi.RepairCycleResponse` 一致 */
function sampleCycle(overrides: Record<string, unknown> = {}) {
  return {
    id: "cycle-1",
    max_attempts: 3,
    opened_at: "2026-10-26T08:00:00Z",
    opened_by: "user_reopen",
    sequence: 1,
    step_id: "step-1",
    task_id: "task-1",
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

/** 任务详情 */
async function stubTaskDetail(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}`, async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await json(route, sampleTask({ id: taskID }));
  });
}

/** 工作流进度 */
async function stubWorkflow(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/workflow`, async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await json(route, workflowBody({ task_id: taskID }, { task_id: taskID }));
  });
}

/** 步骤列表 */
async function stubSteps(page: Page, records: unknown[] = [sampleStep()]) {
  await page.route("**/api/v1/tasks/task-1/steps?*", (route: Route) =>
    json(route, { current: 1, records, size: 100, total: records.length }),
  );
}

/**
 * 拦截应答器：返回值即响应体，也可以是延迟的 Promise。
 *
 * 竞态用例需要旧请求晚于新请求返回，因此必须允许异步返回值；同步用例直接返回
 * 响应体即可，两者共用同一签名。
 */
type StubResponder = (index: number, url: string) => unknown;

/** 事件时间线：记录每次请求，由 responder 决定响应 */
async function stubEvents(page: Page, responder: StubResponder) {
  const calls: string[] = [];
  await page.route("**/api/v1/tasks/task-1/events*", async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const url = route.request().url();
    calls.push(url);
    const body = await responder(calls.length - 1, url);
    if (body instanceof Error) {
      await json(route, { error: { code: "internal_error", message: body.message } }, 500);
      return;
    }
    await json(route, body);
  });
  return calls;
}

/** 修复周期历史：记录每次请求，由 responder 决定响应 */
async function stubRepairCycles(page: Page, responder: StubResponder) {
  const calls: string[] = [];
  await page.route("**/api/v1/tasks/task-1/steps/*/repair-cycles", async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const url = route.request().url();
    calls.push(url);
    const body = await responder(calls.length - 1, url);
    if (body instanceof Error) {
      await json(route, { error: { code: "internal_error", message: body.message } }, 500);
      return;
    }
    await json(route, body);
  });
  return calls;
}

/** 其余执行记录接口返回空数据 */
async function stubEmptyRecords(page: Page) {
  await page.route("**/api/v1/tasks/task-1/steps/*/attempts?*", (route: Route) =>
    json(route, { current: 1, records: [], size: 100, total: 0 }),
  );
  await page.route("**/api/v1/tasks/task-1/steps/*/validations", (route: Route) =>
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
 * 记录全部写请求。
 *
 * 历史查询必须是只读的：任何 POST / PUT / PATCH / DELETE 都说明浏览行为触发了
 * 有副作用的动作。
 */
function recordWriteRequests(page: Page): string[] {
  const writes: string[] = [];
  page.on("request", (request) => {
    const method = request.method();
    const url = request.url();
    if (!url.includes("/api/v1/")) return;
    if (method === "GET" || method === "HEAD" || method === "OPTIONS") return;
    writes.push(`${method} ${url}`);
  });
  return writes;
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("事件时间线", () => {
  test("任务详情展示真实事件时间线与中文说明", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));
    await stubEvents(page, () => ({
      offset: 0,
      records: [
        sampleEvent(),
        sampleEvent({
          attempt_id: "attempt-1",
          created_at: "2026-10-26T08:01:00Z",
          event_label: "执行尝试开始",
          event_type: "attempt.started",
          id: 2,
          step_id: "step-1",
          summary: "结论 started",
        }),
      ],
      size: 20,
      task_id: "task-1",
      total: 2,
    }));

    await openDetail(page);

    await expect(page.getByText("事件时间线")).toBeVisible();
    await expect(page.getByText("已显示 2 / 2 条")).toBeVisible();
    await expect(page.getByText("任务创建")).toBeVisible();
    await expect(page.getByText("执行尝试开始")).toBeVisible();
    await expect(page.getByText("结论 started")).toBeVisible();
    await expect(page.getByText("尝试 attempt-1")).toBeVisible();
    await expect(page.getByText("步骤 step-1")).toBeVisible();
  });

  test("事件查询失败后可重试，失败不伪装成空历史", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));
    // 先让全部事件请求失败：自动刷新可能带来第二次请求，两者都必须失败，
    // 否则"失败"这一状态会被一次偶然成功的首屏请求掩盖。
    let failing = true;
    const calls = await stubEvents(page, () =>
      failing
        ? new Error("数据库暂时不可用")
        : {
            offset: 0,
            records: [sampleEvent()],
            size: 20,
            task_id: "task-1",
            total: 1,
          },
    );

    await openDetail(page);

    await expect(page.getByText("加载事件时间线失败")).toBeVisible();
    await expect(page.getByText("暂无事件记录")).toHaveCount(0);

    failing = false;
    await page.getByText("重试", { exact: true }).click();
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect(page.getByText("任务创建")).toBeVisible();
    expect(calls.length).toBeGreaterThanOrEqual(2);
  });

  test("未知事件类型仍能显示，并说明类型未在当前版本定义", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));
    await stubEvents(page, () => ({
      offset: 0,
      records: [
        sampleEvent({
          created_at: "2026-10-26T08:05:00Z",
          event_label: undefined,
          event_type: "legacy.removed",
          id: 9,
          type_known: false,
        }),
      ],
      size: 20,
      task_id: "task-1",
      total: 1,
    }));

    await openDetail(page);

    await expect(page.getByText("legacy.removed", { exact: true })).toBeVisible();
    await expect(page.getByText(/该类型未在当前版本定义/)).toBeVisible();
    await expect(page.getByText("无可展示的摘要")).toBeVisible();
  });

  test("事件为空时明确说明不代表任务从未启动", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));
    await stubEvents(page, () => ({
      offset: 0,
      records: [],
      size: 20,
      task_id: "task-1",
      total: 0,
    }));

    await openDetail(page);

    await expect(page.getByText("暂无事件记录").first()).toBeVisible();
    await expect(page.getByText(/列表为空不代表任务从未启动/)).toBeVisible();
  });
});

test.describe("修复周期历史", () => {
  test("选择步骤后显示该步骤的修复周期历史", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubEvents(page, () => ({
      offset: 0,
      records: [],
      size: 20,
      task_id: "task-1",
      total: 0,
    }));
    await stubRepairCycles(page, () => ({
      records: [
        sampleCycle({ closed_at: "2026-10-26T09:00:00Z" }),
        sampleCycle({
          id: "cycle-2",
          max_attempts: 5,
          opened_at: "2026-10-26T09:30:00Z",
          sequence: 2,
        }),
      ],
      step_id: "step-1",
      task_id: "task-1",
      total: 2,
    }));

    await openDetail(page);
    await page.getByText("实现登录步骤", { exact: true }).first().click();
    await expect(page.getByText("步骤详情")).toBeVisible();

    await expect(page.getByText("修复周期历史")).toBeVisible();
    await expect(page.getByText("共 2 个周期")).toBeVisible();
    await expect(page.getByText("第 1 个周期")).toBeVisible();
    await expect(page.getByText("第 2 个周期")).toBeVisible();
    await expect(page.getByText("来源 user_reopen").first()).toBeVisible();
    await expect(page.getByText("尝试上限 3 次")).toBeVisible();
    await expect(page.getByText("尝试上限 5 次")).toBeVisible();
    // 未关闭的周期必须显示中性说明，而不是"进行中"或"成功"。
    await expect(page.getByText(/尚未记录结束时间/)).toBeVisible();
  });

  test("没有周期与未关闭周期都使用中性文案", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubEvents(page, () => ({
      offset: 0,
      records: [],
      size: 20,
      task_id: "task-1",
      total: 0,
    }));
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));

    await openDetail(page);
    await page.getByText("实现登录步骤", { exact: true }).first().click();
    await expect(page.getByText("步骤详情")).toBeVisible();

    await expect(page.getByText("暂无修复周期记录")).toBeVisible();
    await expect(page.getByText(/列表为空不代表该步骤从未执行/)).toBeVisible();
    // 不得出现"仍在执行""成功"这类由前端推断出来的周期结论。
    await expect(page.getByText("第 1 个周期")).toHaveCount(0);
    await expect(page.getByText(/尚未记录结束时间/)).toHaveCount(0);
  });

  test("切换步骤后旧响应不覆盖新数据", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [
      sampleStep(),
      sampleStep({ id: "step-2", name: "第二个步骤", sequence: 2 }),
    ]);
    await stubEmptyRecords(page);
    await stubEvents(page, () => ({
      offset: 0,
      records: [],
      size: 20,
      task_id: "task-1",
      total: 0,
    }));
    // 第一个步骤的响应故意延迟，确保它在切换之后才到达。
    await stubRepairCycles(page, async (_index, url) => {
      if (url.includes("/step-1/")) {
        await new Promise((resolve) => setTimeout(resolve, 800));
        return {
          records: [sampleCycle({ id: "cycle-old", sequence: 7 })],
          step_id: "step-1",
          task_id: "task-1",
          total: 1,
        };
      }
      return {
        records: [sampleCycle({ id: "cycle-new", sequence: 2 })],
        step_id: "step-2",
        task_id: "task-1",
        total: 1,
      };
    });

    await openDetail(page);
    await page.getByText("实现登录步骤", { exact: true }).first().click();
    await expect(page.getByText("步骤详情")).toBeVisible();
    // 立刻切到第二个步骤，第一个步骤的响应仍在途中。
    await page.getByText("第二个步骤", { exact: true }).first().click();
    await expect(page.getByText("第 2 个周期")).toBeVisible();
    // 迟到的旧步骤响应不得覆盖当前步骤的周期列表。
    await page.waitForTimeout(1200);
    await expect(page.getByText("第 2 个周期")).toBeVisible();
    await expect(page.getByText("第 7 个周期")).toHaveCount(0);
  });
});

test.describe("自动刷新与副作用", () => {
  test("自动刷新不会无条件重复加载事件历史", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));
    const calls = await stubEvents(page, () => ({
      offset: 0,
      records: [sampleEvent()],
      size: 20,
      task_id: "task-1",
      total: 1,
    }));

    await openDetail(page);
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    const initial = calls.length;

    // 等待超过一个自动刷新周期（5 秒）：任务与工作流字段没有变化，事件历史不应被重复请求。
    await page.waitForTimeout(6200);
    expect(calls.length).toBe(initial);
  });

  test("浏览与刷新历史不触发任何执行类请求", async ({ page }) => {
    const writes = recordWriteRequests(page);
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubEmptyRecords(page);
    await stubRepairCycles(page, () => ({
      records: [sampleCycle()],
      step_id: "step-1",
      task_id: "task-1",
      total: 1,
    }));
    await stubEvents(page, () => ({
      offset: 0,
      records: [sampleEvent()],
      size: 20,
      task_id: "task-1",
      total: 1,
    }));

    await openDetail(page);
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await page.getByText("实现登录步骤", { exact: true }).first().click();
    await expect(page.getByText("修复周期历史")).toBeVisible();
    await page.getByText("刷新", { exact: true }).first().click();
    await page.waitForTimeout(6200);

    expect(writes).toEqual([]);
  });
});
