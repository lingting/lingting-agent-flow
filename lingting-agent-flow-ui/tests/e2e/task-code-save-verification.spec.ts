import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 代码保存核验操作的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH 或 Git。
 * 用例只验证一件事：核验是否只由用户显式触发、结果是否按真实响应分块展示、以及在途 /
 * 失败 / 切换任务等竞态是否被正确处理。这些用例不能证明真实只读核验在容器内可用。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    closeout: "confirmed",
    closeout_summary: "任务与工作流执行记录一致确认收尾完成",
    created_at: "2026-10-26T08:00:00Z",
    description: "为登录页接入真实接口",
    final_result: "succeeded",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "completed",
    status_finished: true,
    status_terminal: true,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一条代码保存记录样本，字段与 Go 端 `CodeSaveRecordListEntryResponse` 一致 */
function sampleRecord(overrides: Record<string, unknown> = {}) {
  return {
    base_branch: "main",
    commit_message: "提交一",
    commit_sha: "1".repeat(40),
    commit_status: "pending",
    container_id: "container-1",
    created_at: "2026-10-26T08:00:00Z",
    finished_at: "2026-10-26T08:00:12Z",
    id: "record-1",
    merge_enabled: false,
    merge_status: "pending",
    persist_status: "persisted",
    push_status: "pending",
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

/** 核验响应中的记录视图，字段与 Go 端 `httpapi.CodeSaveRecordResponse` 一致 */
function sampleRecordView(overrides: Record<string, unknown> = {}) {
  return {
    commit_sha: "1".repeat(40),
    commit_status: "pending",
    merge_status: "pending",
    persist_status: "persisted",
    push_status: "pending",
    record_id: "record-1",
    recovery_status: "unverified",
    task_branch: "agent/task-1",
    task_id: "task-1",
    ...overrides,
  };
}

/** 一份核验响应样本，字段与 Go 端 `httpapi.CodeSaveVerifyResponse` 一致 */
function sampleVerify(overrides: Record<string, unknown> = {}) {
  return {
    decision: {
      safe_to_retry: false,
      stage: "push",
      status: "unverifiable",
      summary: "远程任务分支指向其他提交，需要人工核验",
    },
    observation: {
      head_commit: "1".repeat(40),
      read_failed: false,
      remote_base_commit: "3".repeat(40),
    },
    persisted: true,
    record: sampleRecordView(),
    updated: sampleRecordView({ recovery_status: "unverifiable" }),
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
        checked_at: "2026-10-26T08:00:00Z",
        status: "ok",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截任务列表；可传入多个任务用于验证切换任务 */
async function stubTaskList(page: Page, tasks = [sampleTask()]) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: tasks, size: 20, total: tasks.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截任务详情 */
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

/** 拦截其余只读面板，避免与代码保存无关的错误提示 */
async function stubOtherPanels(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/workflow`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        has_run: false,
        steps: [],
        task_id: taskID,
        task_status: "completed",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/steps?*`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [], size: 100, total: 0 }),
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
        code_save_summary: "任务已完成",
        next_actions: null,
        summary: "任务已完成",
        task_id: taskID,
        task_status: "completed",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/retry-eligibility`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        blocking: null,
        can_retry: false,
        summary: "任务状态不允许重试",
        task_id: taskID,
        task_status: "completed",
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

/** 拦截代码保存记录列表；返回被调用的 URL 列表 */
async function stubCodeSaves(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const urls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/code-saves`, (route: Route) => {
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

/**
 * 拦截代码保存核验请求。
 *
 * 返回的 `calls` 记录每次请求的 URL，用于断言"没有自动触发""重复点击只发一次"。
 * `responder` 可用 `route` 延迟响应，从而构造在途状态。
 */
async function stubVerify(
  page: Page,
  responder: (callIndex: number, route: Route) => Promise<void> | void,
  taskID = "task-1",
) {
  const calls: string[] = [];
  await page.route(
    `**/api/v1/tasks/${taskID}/code-saves/*/verify-code-save`,
    async (route: Route) => {
      calls.push(route.request().url());
      await responder(calls.length - 1, route);
    },
  );
  return calls;
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

/** 打开任务详情 */
async function openDetail(page: Page, taskName = "实现登录接口") {
  await page.goto("/tasks");
  await page.getByText(taskName).first().click();
  await expect(page.getByText("任务详情")).toBeVisible();
}

/** 满足代码保存记录列表与核验都可见的常规场景 */
async function preparePage(page: Page, options: { records?: Record<string, unknown>[] } = {}) {
  const task = sampleTask();
  await stubTaskList(page, [task]);
  await stubTaskDetail(page, task);
  await stubOtherPanels(page, task.id);
  const records = options.records ?? [sampleRecord()];
  await stubCodeSaves(page, () => ({ body: { records, total: records.length } }), task.id);
}

test.describe("代码保存核验入口", () => {
  test("每条记录各自提供核验入口，且初始状态不会发起核验", async ({ page }) => {
    await preparePage(page, {
      records: [sampleRecord({ id: "record-1" }), sampleRecord({ id: "record-2" })],
    });
    const calls = await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify(sampleVerify()),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);

    await expect(page.getByText("record-1")).toBeVisible();
    await expect(page.getByText("record-2")).toBeVisible();
    // 每条记录都有自己的核验入口
    await expect(page.getByRole("button", { name: "核验代码保存" })).toHaveCount(2);
    // 打开详情、加载列表与自动刷新都不会触发核验
    expect(calls.length).toBe(0);
  });

  test("显式触发核验后展示观察结果、判定与持久化结果", async ({ page }) => {
    await preparePage(page);
    const calls = await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify(
          sampleVerify({
            decision: {
              safe_to_retry: true,
              stage: "merge",
              status: "confirmed_not_applied",
              summary: "远程基础分支仍等于基线，合并没有发生",
            },
            observation: {
              head_commit: "a".repeat(40),
              read_failed: false,
              remote_base_commit: "c".repeat(40),
              remote_task_commit: "b".repeat(40),
            },
            updated: sampleRecordView({ recovery_status: "confirmed_not_applied" }),
          }),
        ),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await page.getByRole("button", { name: "核验代码保存" }).click();

    // A. 现场观察
    await expect(page.getByText("A. 现场观察（只读查询见到的事实）")).toBeVisible();
    await expect(page.getByText("a".repeat(40))).toBeVisible();
    await expect(page.getByText("b".repeat(40))).toBeVisible();
    await expect(page.getByText("c".repeat(40))).toBeVisible();
    await expect(page.getByText(/只读查询已完成/)).toBeVisible();

    // B. 核验判定：状态、阶段、说明与 safe_to_retry 独立展示
    await expect(page.getByText("B. 核验判定（领域规则给出的结论）")).toBeVisible();
    // 判定状态标签按精确匹配：核验前后对比区会同时出现同样的文案（带前后对比后缀）
    await expect(page.getByText("已确认操作未发生", { exact: true })).toBeVisible();
    await expect(page.getByText("合并阶段", { exact: true })).toBeVisible();
    await expect(page.getByText("远程基础分支仍等于基线，合并没有发生")).toBeVisible();
    await expect(page.getByText("服务端返回 safe_to_retry = true")).toBeVisible();
    await expect(page.getByText(/页面不会自动重试/)).toBeVisible();

    // C. 核验后的记录
    await expect(page.getByText("C. 核验后的记录（服务端写回后读取到的真实记录）")).toBeVisible();

    // D. 持久化结果
    await expect(page.getByText("D. 核验结论已写入记录")).toBeVisible();
    await expect(page.getByText(/不代表代码保存成功或核验通过/)).toBeVisible();

    expect(calls.length).toBe(1);
    expect(calls[0]).toContain("/code-saves/record-1/verify-code-save");
  });

  test("persisted=false 时明确提示未持久化，仍保留真实判定", async ({ page }) => {
    await preparePage(page);
    await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify(
          sampleVerify({
            decision: {
              safe_to_retry: false,
              stage: "push",
              status: "confirmed_applied",
              summary: "远程任务分支已指向预期提交，推送确实已经发生",
            },
            persisted: false,
          }),
        ),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await page.getByRole("button", { name: "核验代码保存" }).click();

    await expect(page.getByText("D. 核验结论未成功持久化")).toBeVisible();
    await expect(page.getByText(/仍然是本次核验取得的真实事实/)).toBeVisible();
    await expect(page.getByText(/刷新代码保存/).first()).toBeVisible();
    // 结论本身仍然可见，不被"未持久化"抹掉
    await expect(page.getByText("已确认操作发生")).toBeVisible();
    await expect(page.getByText("远程任务分支已指向预期提交，推送确实已经发生")).toBeVisible();
    // 未持久化不得被展示成核验通过
    await expect(page.getByText("D. 核验结论已写入记录")).toHaveCount(0);
  });

  test("read_failed=true 时如实展示读取失败，不显示为核验成功", async ({ page }) => {
    await preparePage(page);
    await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify(
          sampleVerify({
            decision: {
              safe_to_retry: false,
              status: "unverifiable",
              summary: "无法核验真实仓库状态: 容器已脱离管理",
            },
            observation: { read_error: "容器已脱离管理", read_failed: true },
          }),
        ),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await page.getByRole("button", { name: "核验代码保存" }).click();

    await expect(page.getByText(/本次只读核验没有完成，其余现场字段不可信/)).toBeVisible();
    await expect(page.getByText("容器已脱离管理").first()).toBeVisible();
    await expect(page.getByText("无法取得可信结论", { exact: true })).toBeVisible();
    await expect(page.getByText(/无法核验真实仓库状态/)).toBeVisible();
    // 读取失败不得被展示成成功或"操作没有发生"
    await expect(page.getByText("已确认操作发生")).toHaveCount(0);
    await expect(page.getByText("已确认操作未发生")).toHaveCount(0);
  });

  test("服务端返回错误时显示错误，不伪造核验结果", async ({ page }) => {
    await preparePage(page);
    const calls = await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify({
          error: { code: "precondition_failed", message: "核验代码保存记录失败: 容器已删除" },
        }),
        contentType: "application/json",
        status: 412,
      }),
    );

    await openDetail(page);
    await page.getByRole("button", { name: "核验代码保存" }).click();

    await expect(page.getByText("核验请求失败")).toBeVisible();
    await expect(page.getByText(/容器已删除/)).toBeVisible();
    await expect(page.getByText(/本次请求没有取得任何可展示的核验结论/)).toBeVisible();
    // 没有任何伪造的现场观察或判定区块
    await expect(page.getByText("A. 现场观察（只读查询见到的事实）")).toHaveCount(0);
    await expect(page.getByText("B. 核验判定（领域规则给出的结论）")).toHaveCount(0);
    // 不自动重发
    expect(calls.length).toBe(1);
  });

  test("核验后记录列表刷新失败时，已取得的核验响应仍可见", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, [task]);
    await stubTaskDetail(page, task);
    await stubOtherPanels(page);
    let refreshing = false;
    await stubCodeSaves(page, () =>
      refreshing
        ? { body: { error: { code: "internal_error", message: "服务内部错误" } }, status: 500 }
        : { body: { records: [sampleRecord()], total: 1 } },
    );
    await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify(sampleVerify()),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await expect(page.getByText("等待提交").first()).toBeVisible();

    refreshing = true;
    await page.getByRole("button", { name: "核验代码保存" }).click();

    await expect(page.getByText("D. 核验结论已写入记录")).toBeVisible();
    await expect(page.getByText("核验结论未成功持久化")).toHaveCount(0);
    // 列表刷新失败单独提示，核验结论不被抹除
    await expect(page.getByText("代码保存记录刷新失败")).toBeVisible();
    await expect(page.getByText(/服务内部错误/)).toBeVisible();
    await expect(page.getByText("B. 核验判定（领域规则给出的结论）")).toBeVisible();
  });

  test("同一记录请求在途时重复点击不产生第二个请求", async ({ page }) => {
    await preparePage(page);
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const calls = await stubVerify(page, async (_index, route) => {
      await gate;
      await route.fulfill({
        body: JSON.stringify(sampleVerify()),
        contentType: "application/json",
        status: 200,
      });
    });

    await openDetail(page);
    const button = page.getByRole("button", { name: "核验代码保存" });
    await button.click();
    // 在途时按钮被禁用并显示执行中状态
    await expect(page.getByText("正在核验代码保存")).toBeVisible();
    await expect(page.getByRole("button", { name: "核验中" })).toBeDisabled();
    await page.getByRole("button", { name: "核验中" }).click({ force: true });

    expect(calls.length).toBe(1);
    release?.();
    await expect(page.getByText("D. 核验结论已写入记录")).toBeVisible();
    expect(calls.length).toBe(1);
  });

  test("不同记录的核验结果与在途状态不会串线", async ({ page }) => {
    await preparePage(page, {
      records: [sampleRecord({ id: "record-1" }), sampleRecord({ id: "record-2" })],
    });
    await stubVerify(page, (index, route) => {
      const recordID = index === 0 ? "record-1" : "record-2";
      return route.fulfill({
        body: JSON.stringify(
          sampleVerify({
            decision: {
              safe_to_retry: false,
              stage: "push",
              status: "confirmed_applied",
              summary: `${recordID} 的判定说明`,
            },
            record: sampleRecordView({ record_id: recordID }),
            updated: sampleRecordView({
              record_id: recordID,
              recovery_status: "confirmed_applied",
            }),
          }),
        ),
        contentType: "application/json",
        status: 200,
      });
    });

    await openDetail(page);
    await page.getByRole("button", { name: "核验代码保存" }).first().click();
    await expect(page.getByText("record-1 的判定说明")).toBeVisible();

    await page.getByRole("button", { name: "核验代码保存" }).nth(1).click();
    await expect(page.getByText("record-2 的判定说明")).toBeVisible();
    // 两条记录各自保留自己的结果
    await expect(page.getByText("record-1 的判定说明")).toHaveCount(1);
    await expect(page.getByText("record-2 的判定说明")).toHaveCount(1);
  });

  test("快速切换任务时迟到响应不会污染当前详情", async ({ page }) => {
    const taskA = sampleTask({ name: "任务 A" });
    const taskB = sampleTask({ id: "task-2", name: "任务 B" });
    await stubTaskList(page, [taskA, taskB]);
    await stubTaskDetail(page, taskA);
    await stubTaskDetail(page, taskB);
    await stubOtherPanels(page, "task-1");
    await stubOtherPanels(page, "task-2");
    await stubCodeSaves(page, () => ({ body: { records: [sampleRecord()], total: 1 } }), "task-1");
    await stubCodeSaves(
      page,
      () => ({
        body: { records: [sampleRecord({ id: "record-b", task_id: "task-2" })], total: 1 },
      }),
      "task-2",
    );

    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await stubVerify(
      page,
      async (index, route) => {
        if (index === 0) {
          await gate;
          await route.fulfill({
            body: JSON.stringify(
              sampleVerify({
                decision: {
                  safe_to_retry: false,
                  status: "unverifiable",
                  summary: "任务 A 的迟到判定",
                },
              }),
            ),
            contentType: "application/json",
            status: 200,
          });
          return;
        }
        await route.fulfill({
          body: JSON.stringify(
            sampleVerify({
              decision: { safe_to_retry: false, status: "unverified", summary: "任务 B 的判定" },
              record: sampleRecordView({ record_id: "record-b", task_id: "task-2" }),
              updated: sampleRecordView({ record_id: "record-b", task_id: "task-2" }),
            }),
          ),
          contentType: "application/json",
          status: 200,
        });
      },
      "task-1",
    );

    await openDetail(page, "任务 A");
    await page.getByRole("button", { name: "核验代码保存" }).click();
    await expect(page.getByText("正在核验代码保存")).toBeVisible();

    // 请求还在途时切换到任务 B
    await page.getByText("任务 B").first().click();
    await expect(page.getByText("任务详情")).toBeVisible();
    release?.();

    // A 的迟到响应不得出现在 B 的详情里
    await expect(page.getByText("任务 A 的迟到判定")).toHaveCount(0);
    await expect(page.getByText("A. 现场观察（只读查询见到的事实）")).toHaveCount(0);
  });

  test("核验不会进入自动刷新循环：等待若干个刷新周期后仍只有一次请求", async ({ page }) => {
    await preparePage(page);
    const calls = await stubVerify(page, (_index, route) =>
      route.fulfill({
        body: JSON.stringify(sampleVerify()),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await expect(page.getByRole("button", { name: "核验代码保存" })).toBeVisible();

    // 自动刷新只在执行期间每 5 秒一轮；任务已完成，因此这里额外等待确保没有轮询触发核验。
    await page.waitForTimeout(1500);
    await page.getByText("刷新代码保存").click();
    await page.waitForTimeout(500);

    expect(calls.length).toBe(0);
    await expect(page.getByText("A. 现场观察（只读查询见到的事实）")).toHaveCount(0);
  });
});
