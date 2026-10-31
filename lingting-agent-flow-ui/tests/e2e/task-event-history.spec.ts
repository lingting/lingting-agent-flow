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
 *   - 实时推送连接只在历史游标真实就绪后建立，连接故障不伪装成历史失败或空历史。
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

/**
 * 带真实游标的事件历史响应。
 *
 * `latest_event_id` 是实时流的起点：没有它前端不会建立连接（"不猜起点"），因此只有实时流
 * 用例提供它，其余历史用例保持缺省，避免意外发起流请求。
 */
function historyBody(latestEventID: number, overrides: Record<string, unknown> = {}) {
  return {
    latest_event_id: latestEventID,
    offset: 0,
    records: [sampleEvent()],
    size: 20,
    task_id: "task-1",
    total: 1,
    ...overrides,
  };
}

/**
 * 一帧实时事件的 `data` 内容，字段与历史查询完全一致（同一个 `httpapi.EventResponse`）。
 *
 * 事件对象必须来自真实 DTO：构造生产代码永远不会返回的字段，只会让页面"碰巧通过"。
 */
function liveEventFrame(id: number, overrides: Record<string, unknown> = {}) {
  return JSON.stringify(
    sampleEvent({
      created_at: "2026-10-26T08:10:00Z",
      event_label: "步骤完成",
      event_type: "step.completed",
      id,
      step_id: "step-1",
      summary: `步骤完成（事件 ${id}）`,
      ...overrides,
    }),
  );
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
async function stubTaskDetail(
  page: Page,
  taskID = "task-1",
  overrides: Record<string, unknown> = {},
) {
  await page.route(`**/api/v1/tasks/${taskID}`, async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await json(route, sampleTask({ id: taskID, ...overrides }));
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
async function stubSteps(page: Page, records: unknown[] = [sampleStep()], taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/steps?*`, (route: Route) =>
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

/**
 * 历史事件查询的路径模式。
 *
 * 必须只匹配历史查询：`/events/stream` 是长连接实时流，若被历史桩捕获，就会用一次性 JSON
 * 应答实时流请求，让"连接失败"与"没有推送"混成同一种现象。这里用锚定正则显式区分，而不是
 * 依赖路由注册顺序或 glob 中 `*` 不跨 `/` 的细节。
 */
function historyEventsPattern(taskID: string) {
  return new RegExp(`/api/v1/tasks/${taskID}/events(\\?.*)?$`);
}

/** 事件时间线：记录每次请求，由 responder 决定响应 */
async function stubEvents(page: Page, responder: StubResponder, taskID = "task-1") {
  const calls: string[] = [];
  await page.route(historyEventsPattern(taskID), async (route: Route) => {
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
async function stubRepairCycles(page: Page, responder: StubResponder, taskID = "task-1") {
  const calls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/steps/*/repair-cycles`, async (route: Route) => {
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
async function stubEmptyRecords(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/steps/*/attempts?*`, (route: Route) =>
    json(route, { current: 1, records: [], size: 100, total: 0 }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/steps/*/validations`, (route: Route) =>
    json(route, { records: [], state: "unverified", step_id: "step-1", total: 0 }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/code-saves`, (route: Route) =>
    json(route, { records: [], total: 0 }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/resume-eligibility`, (route: Route) =>
    json(route, {
      blocking: null,
      can_resume: false,
      code_save_action: "proceed",
      code_save_records: [],
      code_save_summary: "代码保存记录中没有结果不明的外部副作用",
      next_actions: null,
      summary: "工作流仍在推进，无需续跑",
      task_id: taskID,
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

/**
 * 浏览器内 `EventSource` 替身控制面的属性名。
 *
 * 替身只在测试的浏览器上下文里安装，生产代码仍然使用原生 `EventSource`。
 */
const EVENT_SOURCE_CONTROL = "agentflowEventSourceControl";

/**
 * 在页面脚本之前安装可控的 `EventSource` 替身。
 *
 * 为什么需要替身：真实 SSE 连接无法在浏览器里确定性地驱动 `open` / `message` / `error`，
 * 也无法在不等待真实退避的前提下观察重建。替身让这些边界可断言，代价是它**不验证传输层**
 * （响应头、帧格式、心跳、代理缓冲与多批积压），因此这里得到的结论不能当作服务端 SSE 验收。
 *
 * 替身只实现生产 Hook 实际使用的部分：属性回调 `onopen` / `onmessage` / `onerror`、
 * `close()` 与 `readyState`。连接按创建顺序记录，索引即连接代次。
 */
async function installEventSourceStub(page: Page) {
  await page.addInitScript((controlName: string) => {
    type Handler = ((event: unknown) => void) | null;

    const sources: StubEventSource[] = [];

    class StubEventSource {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSED = 2;

      readonly CONNECTING = 0;
      readonly OPEN = 1;
      readonly CLOSED = 2;

      closed = false;
      onerror: Handler = null;
      onmessage: Handler = null;
      onopen: Handler = null;
      readyState = 0;
      url: string;

      constructor(url: string) {
        this.url = String(url);
        sources.push(this);
      }

      close() {
        this.closed = true;
        this.readyState = StubEventSource.CLOSED;
      }
    }

    const control = (operation: string, argument?: unknown): unknown => {
      const payload = (argument ?? {}) as { data?: string; index?: number };
      const source = sources[payload.index ?? 0] as StubEventSource | undefined;
      switch (operation) {
        case "count":
          return sources.length;
        case "url":
          return source?.url;
        case "closed":
          return source?.closed === true;
        case "open":
          if (!source) return false;
          source.readyState = StubEventSource.OPEN;
          source.onopen?.({ type: "open" });
          return true;
        case "message":
          if (!source) return false;
          source.onmessage?.({ data: payload.data, type: "message" });
          return true;
        case "error":
          if (!source) return false;
          source.onerror?.({ type: "error" });
          return true;
        default:
          return undefined;
      }
    };

    const holder = window as unknown as Record<string, unknown>;
    holder.EventSource = StubEventSource;
    holder[controlName] = control;
  }, EVENT_SOURCE_CONTROL);
}

/** 实时流连接的控制指令 */
type EventSourceOperation = "count" | "url" | "closed" | "open" | "message" | "error";

/**
 * 调用浏览器替身的控制面。
 *
 * 必须集中成一个入口：`page.evaluate` 的回调会被序列化到浏览器里单独执行，无法引用本文件的
 * 其它函数，因此页面内只暴露一个受控函数，用例侧只传操作名与参数。
 */
async function callEventSource<T>(
  page: Page,
  operation: EventSourceOperation,
  argument: { data?: string; index?: number } = {},
): Promise<T | undefined> {
  return page.evaluate(
    ({ argument: payload, controlName, operation: name }) => {
      const holder = window as unknown as Record<string, unknown>;
      const control = holder[controlName];
      if (typeof control !== "function") return undefined;
      return (control as (op: string, arg?: unknown) => unknown)(name, payload);
    },
    { argument, controlName: EVENT_SOURCE_CONTROL, operation },
  ) as Promise<T | undefined>;
}

/**
 * 实时流连接的控制接口。
 *
 * 索引对应连接代次：0 是第一条连接，1 是重连或切换到新任务后建立的第二条连接。
 */
function liveStream(page: Page) {
  return {
    /** 已经创建的连接数量 */
    count: async () => (await callEventSource<number>(page, "count")) ?? 0,
    /** 第 index 条连接的请求地址（含 `last_event_id` 查询参数） */
    url: (index: number) => callEventSource<string>(page, "url", { index }),
    /** 第 index 条连接是否已被前端关闭 */
    closed: async (index: number) =>
      (await callEventSource<boolean>(page, "closed", { index })) === true,
    /** 触发第 index 条连接的 `open` */
    open: (index: number) => callEventSource<boolean>(page, "open", { index }),
    /** 向第 index 条连接投递一帧 `data` */
    message: (index: number, data: string) =>
      callEventSource<boolean>(page, "message", { data, index }),
    /** 触发第 index 条连接的 `error` */
    fail: (index: number) => callEventSource<boolean>(page, "error", { index }),
  };
}

/**
 * 记录任务详情与工作流进度的读取次数。
 *
 * 状态确认的断言关心"同一窗口内的多条实时事件是否只合并成一轮读取"，因此要分别统计两类
 * 只读快照请求，而不是只看某个控件文案。
 */
function recordSnapshotReads(page: Page) {
  const reads = { task: 0, workflow: 0 };
  page.on("request", (request) => {
    if (request.method() !== "GET") return;
    const { pathname } = new URL(request.url());
    if (pathname === "/api/v1/tasks/task-1") reads.task += 1;
    else if (pathname === "/api/v1/tasks/task-1/workflow") reads.workflow += 1;
  });
  return reads;
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

/**
 * 步骤流与任务历史的先后顺序。
 *
 * 事件时间线是任务维度的历史（一次 20 条并支持加载更多），不能插在步骤列表与步骤详情之间：
 * 否则用户点开某个步骤后，必须越过整段历史才能看到刚选中的步骤详情与操作入口。
 */
test.describe("步骤详情与事件时间线的顺序", () => {
  test("选中步骤后，步骤详情出现在事件时间线之前", async ({ page }) => {
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
      records: [sampleEvent()],
      size: 20,
      task_id: "task-1",
      total: 1,
    }));

    await openDetail(page);
    await page.getByText("实现登录步骤", { exact: true }).first().click();

    const detail = page.getByText("步骤详情").first();
    const timeline = page.getByText("事件时间线").first();
    await expect(detail).toBeVisible();
    await expect(timeline).toBeVisible();

    const detailBox = await detail.boundingBox();
    const timelineBox = await timeline.boundingBox();
    expect(detailBox).not.toBeNull();
    expect(timelineBox).not.toBeNull();
    // 步骤详情在事件时间线上方：选中步骤后不需要越过任务历史
    expect(detailBox!.y).toBeLessThan(timelineBox!.y);
  });
});

/**
 * 实时事件推送（SSE）在页面上的消费行为。
 *
 * 连接由浏览器内受控的 `EventSource` 替身驱动（见 `installEventSourceStub`），历史查询仍走
 * 真实 HTTP 拦截，两条路径由 `stubEvents` 的锚定正则显式区分。这组用例验证的是"页面如何连接、
 * 怎么显示、收到事件后触发了什么"，**不验证**真实 SSE 传输：帧格式、心跳、15 秒注释帧、代理
 * 缓冲与超过单批 100 条的积压仍由服务端用例与后续专项验收承担。
 *
 * 每个用例使用独立的浏览器上下文：页面销毁时挂起的重连定时器与长连接一并结束，不会把后台
 * 任务留给下一个用例。
 */
test.describe("实时事件推送", () => {
  test("历史返回真实游标后才建立连接，地址携带该游标", async ({ page }) => {
    await installEventSourceStub(page);
    const stream = liveStream(page);
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
    await stubEvents(page, () => historyBody(7));

    await openDetail(page);

    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    // 起点来自历史响应里的 latest_event_id，不是页面猜的 0 或某条事件的下标。
    await expect.poll(() => stream.count()).toBe(1);
    expect(await stream.url(0)).toBe("/api/v1/tasks/task-1/events/stream?last_event_id=7");
    expect(await stream.closed(0)).toBe(false);
    // 连接尚未建立时只能显示"正在连接"，不能提前显示"已连接"。
    await expect(page.getByText("正在连接", { exact: true })).toBeVisible();

    await stream.open(0);

    await expect(page.getByText("已连接", { exact: true })).toBeVisible();
    await expect(
      page.getByText("已连接，暂无新增事件。服务端只推送这条连接建立之后写入的事件。"),
    ).toBeVisible();
    // 历史区域不受连接状态影响。
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
  });

  test("历史加载中、加载失败或没有游标时都不建立连接", async ({ page }) => {
    await installEventSourceStub(page);
    const stream = liveStream(page);
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

    /** 第一次先慢后失败；重试成功但不给 latest_event_id */
    let phase: "no-cursor" | "slow-failure" = "slow-failure";
    await stubEvents(page, async () => {
      if (phase === "slow-failure") {
        await new Promise((resolve) => setTimeout(resolve, 1000));
        return new Error("数据库暂时不可用");
      }
      return { offset: 0, records: [sampleEvent()], size: 20, task_id: "task-1", total: 1 };
    });

    await openDetail(page);

    // 历史仍在加载：没有连接，也没有实时区域，不能假装已经连上。
    await expect(page.getByText("正在加载事件时间线…")).toBeVisible();
    expect(await stream.count()).toBe(0);
    await expect(page.getByText("实时事件")).toHaveCount(0);

    // 历史加载失败：仍然没有连接，失败也不能被写成"暂无事件记录"。
    await expect(page.getByText("加载事件时间线失败")).toBeVisible();
    expect(await stream.count()).toBe(0);
    await expect(page.getByText("暂无事件记录")).toHaveCount(0);

    // 重试成功但响应里没有 latest_event_id：仍然不连接，面板如实显示"未连接"。
    phase = "no-cursor";
    await page.getByText("重试", { exact: true }).click();
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect(page.getByText("未连接", { exact: true })).toBeVisible();
    expect(await stream.count()).toBe(0);
    await expect(page.getByText("已连接", { exact: true })).toHaveCount(0);
  });

  test("实时消息显示真实事件、按标识去重，并丢弃畸形与跨任务事件", async ({ page }) => {
    await installEventSourceStub(page);
    const stream = liveStream(page);
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
    await stubEvents(page, () => historyBody(3));

    await openDetail(page);
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect.poll(() => stream.count()).toBe(1);
    await stream.open(0);
    await expect(page.getByText("已连接", { exact: true })).toBeVisible();

    await stream.message(0, liveEventFrame(4));
    // 同一事件重复送达只有一行：编号 4 再送一次
    await stream.message(0, liveEventFrame(4));
    await stream.message(
      0,
      liveEventFrame(5, {
        created_at: "2026-10-26T08:11:00Z",
        event_label: "步骤开始",
        event_type: "step.started",
        summary: "步骤开始（事件 5）",
      }),
    );
    await expect(page.getByText("连接后新增 2 条")).toBeVisible();

    // 类型、真实时间与经服务端审查的摘要都来自 DTO
    await expect(page.getByText("步骤完成", { exact: true })).toBeVisible();
    await expect(page.getByText("步骤完成（事件 4）")).toHaveCount(1);
    await expect(page.getByText("步骤开始", { exact: true })).toBeVisible();
    const liveTime = await page.evaluate(() =>
      new Date(Date.parse("2026-10-26T08:10:00Z")).toLocaleString(),
    );
    await expect(page.getByText(liveTime, { exact: true })).toBeVisible();

    // 畸形帧、缺必需字段的帧与属于其他任务的事件都不得进入当前页面
    await stream.message(0, "not-json");
    await stream.message(
      0,
      JSON.stringify({ event_type: "step.completed", id: 7, task_id: "task-1" }),
    );
    await stream.message(
      0,
      JSON.stringify(sampleEvent({ event_label: "别的任务事件", id: 8, task_id: "task-2" })),
    );
    // 再送一条有效事件作为"分界"：计数到 3 说明前面三帧确实被处理过且没有新增行
    await stream.message(
      0,
      liveEventFrame(9, {
        created_at: "2026-10-26T08:12:00Z",
        event_label: "验证开始",
        event_type: "verification.started",
        summary: "验证开始（事件 9）",
      }),
    );
    await expect(page.getByText("连接后新增 3 条")).toBeVisible();
    await expect(page.getByText("别的任务事件")).toHaveCount(0);
    await expect(page.getByText("步骤完成（事件 4）")).toHaveCount(1);

    // 实时事件不混进分页历史：历史仍是 1 / 1，实时区域单独计数
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect(page.getByText("实时新增 3 条")).toBeVisible();
    await expect(
      page.getByText(
        "实时区域只包含这条连接之后的事件；更早的历史请看上方分页列表，两者不会合并显示。",
      ),
    ).toBeVisible();
  });

  test("实时事件只合并触发一轮任务状态确认", async ({ page }) => {
    await installEventSourceStub(page);
    const stream = liveStream(page);
    const writes = recordWriteRequests(page);
    const reads = recordSnapshotReads(page);
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
    await stubEvents(page, () => historyBody(3));

    await openDetail(page);
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect.poll(() => stream.count()).toBe(1);
    await stream.open(0);
    await expect.poll(() => reads.task).toBeGreaterThan(0);
    const baseline = { task: reads.task, workflow: reads.workflow };

    // 三条事件落在同一个 800ms 窗口内，其中一条声称任务完成
    await stream.message(
      0,
      liveEventFrame(4, {
        event_label: "任务完成",
        event_type: "task.completed",
        summary: "任务完成（事件 4）",
      }),
    );
    await page.waitForTimeout(80);
    await stream.message(0, liveEventFrame(5));
    await page.waitForTimeout(80);
    await stream.message(0, liveEventFrame(6));
    await expect(page.getByText("连接后新增 3 条")).toBeVisible();

    // 合并后的窗口只确认一次，且确认走既有的串行刷新链路：任务与工作流各读一次
    await expect.poll(() => reads.task, { timeout: 5_000 }).toBe(baseline.task + 1);
    expect(reads.workflow).toBe(baseline.workflow + 1);
    await page.waitForTimeout(600);
    expect(reads.task).toBe(baseline.task + 1);
    expect(reads.workflow).toBe(baseline.workflow + 1);

    // 事件只说明"发生过什么"：任务状态仍来自接口，不因 task.completed 变成已完成
    await expect(page.getByText("任务完成", { exact: true })).toBeVisible();
    await expect(page.getByText("已完成", { exact: true })).toHaveCount(0);
    await expect(page.getByText("开发中").first()).toBeVisible();
    expect(writes).toEqual([]);
  });

  test("切换任务关闭旧连接，新连接使用新任务游标且不混入旧事件", async ({ page }) => {
    await installEventSourceStub(page);
    const stream = liveStream(page);
    await stubTaskList(page, [sampleTask(), sampleTask({ id: "task-2", name: "第二个任务" })]);
    await stubTaskDetail(page);
    await stubTaskDetail(page, "task-2", { name: "第二个任务" });
    await stubWorkflow(page);
    await stubWorkflow(page, "task-2");
    await stubSteps(page);
    await stubSteps(
      page,
      [sampleStep({ id: "step-2", name: "第二个步骤", task_id: "task-2" })],
      "task-2",
    );
    await stubEmptyRecords(page);
    await stubEmptyRecords(page, "task-2");
    await stubRepairCycles(page, () => ({
      records: [],
      step_id: "step-1",
      task_id: "task-1",
      total: 0,
    }));
    await stubRepairCycles(
      page,
      () => ({ records: [], step_id: "step-2", task_id: "task-2", total: 0 }),
      "task-2",
    );
    await stubEvents(page, () => historyBody(3));
    await stubEvents(
      page,
      () => ({
        latest_event_id: 11,
        offset: 0,
        records: [sampleEvent({ event_label: "第二个任务的创建", id: 11, task_id: "task-2" })],
        size: 20,
        task_id: "task-2",
        total: 1,
      }),
      "task-2",
    );

    await openDetail(page);
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect.poll(() => stream.count()).toBe(1);
    expect(await stream.url(0)).toBe("/api/v1/tasks/task-1/events/stream?last_event_id=3");
    await stream.open(0);
    await stream.message(0, liveEventFrame(4));
    await expect(page.getByText("连接后新增 1 条")).toBeVisible();

    // 切换到第二个任务：旧连接必须被关闭
    await page.getByText("第二个任务", { exact: true }).first().click();
    await expect(page.getByText("第二个任务的创建")).toBeVisible();
    await expect.poll(() => stream.count()).toBe(2);
    expect(await stream.closed(0)).toBe(true);
    // 新连接使用新任务自己的历史游标，而不是沿用上一个任务的游标
    expect(await stream.url(1)).toBe("/api/v1/tasks/task-2/events/stream?last_event_id=11");

    await stream.open(1);
    await expect(page.getByText("已连接", { exact: true })).toBeVisible();
    // 旧连接迟到的帧不得进入新任务的实时列表（替身故意在已关闭的连接上继续投递，
    // 模拟网络层迟到；页面必须靠任务归属与连接代次把它丢掉，而不是靠浏览器停止投递）
    await stream.message(0, liveEventFrame(5));
    await stream.message(
      1,
      liveEventFrame(12, {
        event_label: "第二个任务的事件",
        event_type: "step.started",
        summary: "第二个任务的事件（编号 12）",
        task_id: "task-2",
      }),
    );
    await expect(page.getByText("第二个任务的事件（编号 12）")).toBeVisible();
    // 只统计新连接收到的这一条：旧连接的两帧都没有被计入
    await expect(page.getByText("连接后新增 1 条")).toBeVisible();
    await expect(page.getByText("步骤完成（事件 5）")).toHaveCount(0);

    // 关闭详情：连接被关闭，且不会为已关闭的详情重新建立连接
    await page.getByText("关闭", { exact: true }).click();
    await expect(page.getByText("任务详情")).toHaveCount(0);
    expect(await stream.closed(1)).toBe(true);
    await page.waitForTimeout(300);
    expect(await stream.count()).toBe(2);
  });

  test("连接中断显示独立状态，重连保留已收到的事件并推进游标", async ({ page }) => {
    await installEventSourceStub(page);
    const stream = liveStream(page);
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
    await stubEvents(page, () => historyBody(5));

    await openDetail(page);
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect.poll(() => stream.count()).toBe(1);
    await stream.open(0);
    await stream.message(0, liveEventFrame(6));
    await expect(page.getByText("连接后新增 1 条")).toBeVisible();

    await stream.fail(0);

    // 连接故障是独立事实：历史仍是 ready，既不是"加载失败"也不是"没有事件"
    await expect(page.getByText("正在重连", { exact: true })).toBeVisible();
    await expect(page.getByText("事件推送连接中断")).toBeVisible();
    await expect(page.getByText(/事件推送连接已断开，正在重新连接/)).toBeVisible();
    await expect(page.getByText("已显示 1 / 1 条")).toBeVisible();
    await expect(page.getByText("加载事件时间线失败")).toHaveCount(0);
    await expect(page.getByText("暂无事件记录")).toHaveCount(0);
    expect(await stream.closed(0)).toBe(true);
    // 已经收到的有效事件不会因为断线被清空
    await expect(page.getByText("连接后新增 1 条")).toBeVisible();

    // 一次退避后建立新连接：地址带上已收到的最大事件标识
    await expect.poll(() => stream.count(), { timeout: 5_000 }).toBe(2);
    expect(await stream.url(1)).toBe("/api/v1/tasks/task-1/events/stream?last_event_id=6");
    await stream.open(1);
    await expect(page.getByText("已连接", { exact: true })).toBeVisible();
    await expect(page.getByText("连接后新增 1 条")).toBeVisible();
  });
});
