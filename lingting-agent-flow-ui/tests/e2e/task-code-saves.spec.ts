import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 代码保存记录展示的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH 或 Git。
 * 用例只验证一件事：页面是否如实呈现服务端给出的五组独立事实（提交 / 推送 / 合并 /
 * 记录写入状态 / 核验结论），并且不会把它们合并成一个"代码保存成功"标签。
 *
 * 这些用例不能证明真实 Git 行为，只能证明 UI 对给定响应的处理。
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
    commit_status: "succeeded",
    container_id: "container-1",
    created_at: "2026-10-26T08:00:00Z",
    finished_at: "2026-10-26T08:00:12Z",
    id: "record-1",
    merge_enabled: false,
    merge_status: "not_requested",
    persist_status: "persisted",
    push_status: "succeeded",
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

/** 拦截任务列表 */
async function stubTaskList(page: Page, task = sampleTask()) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [task], size: 20, total: 1 }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截任务详情 */
async function stubTaskDetail(page: Page, task = sampleTask()) {
  await page.route("**/api/v1/tasks/task-1", (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      body: JSON.stringify(task),
      contentType: "application/json",
      status: 200,
    });
  });
}

/** 拦截工作流进度：没有执行记录 */
async function stubWorkflowEmpty(page: Page) {
  await page.route("**/api/v1/tasks/task-1/workflow", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        has_run: false,
        steps: [],
        task_id: "task-1",
        task_status: "completed",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截步骤列表：空计划 */
async function stubStepsEmpty(page: Page) {
  await page.route("**/api/v1/tasks/task-1/steps?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [], size: 100, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/**
 * 拦截代码保存记录请求。
 *
 * `responder` 收到的调用次数可用于验证"刷新"是否真的重新请求；返回值决定响应。
 */
async function stubCodeSaves(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
) {
  const urls: string[] = [];
  await page.route("**/api/v1/tasks/task-1/code-saves", (route: Route) => {
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

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
  await stubWorkflowEmpty(page);
  await stubStepsEmpty(page);
});

/** 打开任务详情 */
async function openDetail(page: Page) {
  await page.goto("/tasks");
  await page.getByText("实现登录接口").first().click();
  await expect(page.getByText("任务详情")).toBeVisible();
}

test.describe("代码保存记录展示", () => {
  test("请求真实形状的 code-saves 并展示三阶段独立状态", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    const urls = await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({
            commit_status: "succeeded",
            merge_enabled: true,
            merge_status: "succeeded",
            merge_sha: "3".repeat(40),
            push_status: "succeeded",
            remote_commit_sha: "2".repeat(40),
          }),
        ],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("代码保存记录")).toBeVisible();
    expect(urls[0]).toContain("/api/v1/tasks/task-1/code-saves");
    // 三个阶段各自有独立标签，不出现"代码保存成功"这类总体状态
    await expect(page.getByText("提交成功")).toBeVisible();
    await expect(page.getByText("推送成功")).toBeVisible();
    await expect(page.getByText("合并成功")).toBeVisible();
    await expect(page.getByText("已请求合并基础分支")).toBeVisible();
    await expect(page.getByText("代码保存成功")).toHaveCount(0);
    await expect(page.getByText("代码保存失败")).toHaveCount(0);
  });

  test("提交成功不表示推送成功", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [sampleRecord({ commit_status: "succeeded", push_status: "failed" })],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("提交成功")).toBeVisible();
    await expect(page.getByText("推送失败", { exact: true })).toBeVisible();
    await expect(page.getByText(/推送失败，任务分支没有确认到达远端/)).toBeVisible();
  });

  test("推送或合并为 unknown 时显示结果尚不确定", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({
            commit_status: "succeeded",
            merge_enabled: true,
            merge_status: "unknown",
            push_status: "unknown",
          }),
        ],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("推送结果不确定，需核验")).toBeVisible();
    await expect(page.getByText("合并结果不确定，需核验")).toBeVisible();
    await expect(page.getByText(/推送结果尚不确定/)).toBeVisible();
    await expect(page.getByText(/推送结果尚不确定/)).toContainText("不能直接重试");
    await expect(page.getByText(/合并结果尚不确定/)).toBeVisible();
    // 不确定不是失败，也不是成功
    await expect(page.getByText("推送失败")).toHaveCount(0);
    await expect(page.getByText("推送成功")).toHaveCount(0);
    await expect(page.getByText("合并成功")).toHaveCount(0);
  });

  test("持久化未确认时给出明确警告，且与 Git 结果分开", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({
            commit_status: "succeeded",
            persist_error: "写入时连接中断",
            persist_status: "unconfirmed",
            push_status: "succeeded",
          }),
        ],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("记录写入未确认", { exact: true })).toBeVisible();
    await expect(page.getByText("写入时连接中断")).toBeVisible();
    await expect(page.getByText(/可能落后于真实 Git 现场/)).toBeVisible();
    // Git 阶段的事实仍然照常展示，不被记录可信度覆盖
    await expect(page.getByText("提交成功")).toBeVisible();
    await expect(page.getByText("推送成功")).toBeVisible();
  });

  test("recovery_status 与 stage 同时呈现，不被当作所有阶段的总体结论", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({ recovery_status: "confirmed_not_applied", stage: "push" }),
        ],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("已确认操作未发生")).toBeVisible();
    await expect(page.getByText("阶段：推送阶段")).toBeVisible();
    await expect(
      page.getByText(/核验结论只针对推送阶段；它不代表提交、推送、合并三个阶段都已核验/),
    ).toBeVisible();
  });

  test("unverifiable 与 unverified 的说明不同", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({ id: "record-a", recovery_status: "unverifiable", stage: "merge" }),
          sampleRecord({ id: "record-b", recovery_status: "unverified", stage: "commit" }),
        ],
        total: 2,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("无法取得可信结论", { exact: true })).toBeVisible();
    await expect(page.getByText(/核验无法取得可信事实/)).toBeVisible();
    await expect(page.getByText("尚未核验", { exact: true })).toBeVisible();
    await expect(page.getByText(/该记录尚未核验/)).toBeVisible();
  });

  test("非成功状态（pending / not_attempted / no_changes / skipped）如实展示", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({
            commit_status: "no_changes",
            merge_enabled: true,
            merge_status: "skipped",
            push_status: "not_attempted",
          }),
        ],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("没有可提交的变更")).toBeVisible();
    await expect(page.getByText("未尝试推送")).toBeVisible();
    await expect(page.getByText("未执行合并")).toBeVisible();
    await expect(page.getByText("提交成功")).toHaveCount(0);
    await expect(page.getByText("推送成功")).toHaveCount(0);
    await expect(page.getByText("合并成功")).toHaveCount(0);
  });

  test("merge_enabled=false 时不暗示已授权合并", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [sampleRecord({ merge_enabled: false, merge_status: "not_requested" })],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("未请求合并基础分支")).toBeVisible();
    await expect(page.getByText("未请求合并", { exact: true })).toBeVisible();
    await expect(page.getByText(/本次保存没有请求合并基础分支/)).toBeVisible();
    await expect(page.getByText("已请求合并基础分支")).toHaveCount(0);
  });

  test("未知状态原样展示，不误报为成功", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({
            commit_status: "future_commit",
            merge_status: "future_merge",
            persist_status: "future_persist",
            push_status: "future_push",
            recovery_status: "future_recovery",
          }),
        ],
        total: 1,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("future_commit", { exact: true })).toBeVisible();
    await expect(page.getByText("future_push", { exact: true })).toBeVisible();
    await expect(page.getByText("future_merge", { exact: true })).toBeVisible();
    await expect(page.getByText("future_persist", { exact: true })).toBeVisible();
    await expect(page.getByText("future_recovery", { exact: true })).toBeVisible();
    await expect(page.getByText(/未在当前版本中定义/).first()).toBeVisible();
    await expect(page.getByText("提交成功")).toHaveCount(0);
    await expect(page.getByText("推送成功")).toHaveCount(0);
  });

  test("空记录显示为空状态，而不是任务成功", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({ body: { records: [], total: 0 } }));

    await openDetail(page);

    await expect(page.getByText(/该任务当前没有代码保存记录/)).toBeVisible();
    await expect(page.getByText(/不代表任务成功、代码无须/)).toBeVisible();
    await expect(page.getByText(/共 \d+ 条记录/)).toHaveCount(0);
  });

  test("请求失败显示错误与重试入口，不伪装为空列表", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: { error: { code: "not_found", message: "任务不存在: task-1" } },
      status: 404,
    }));

    await openDetail(page);

    await expect(page.getByText("加载代码保存记录失败")).toBeVisible();
    await expect(page.getByText("任务不存在: task-1")).toBeVisible();
    await expect(page.getByRole("button", { name: /重\s*试/ })).toBeVisible();
    await expect(page.getByText(/该任务当前没有代码保存记录/)).toHaveCount(0);
  });

  test("刷新失败时保留已有记录并单独提示", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    // 首次加载成功；由测试在记录可见后切换为失败，避免 React 严格模式下的重复请求
    // 让"第一次请求"无法确定对应哪一次真实调用。
    let failing = false;
    const urls = await stubCodeSaves(page, () =>
      failing
        ? {
            body: { error: { code: "internal_error", message: "服务内部错误" } },
            status: 500,
          }
        : {
            body: {
              records: [sampleRecord({ push_status: "succeeded" })],
              total: 1,
            },
          },
    );

    await openDetail(page);
    await expect(page.getByText("推送成功")).toBeVisible();

    failing = true;
    await page.getByText("刷新代码保存").click();

    await expect(page.getByText("代码保存记录刷新失败")).toBeVisible();
    await expect(page.getByText(/服务内部错误/)).toBeVisible();
    // 旧记录仍然可见，不被清空成"暂无记录"
    await expect(page.getByText("推送成功", { exact: true })).toBeVisible();
    await expect(page.getByText(/该任务当前没有代码保存记录/)).toHaveCount(0);
    expect(urls.length).toBeGreaterThanOrEqual(2);
  });

  test("多条记录各自独立展示并保留服务端顺序", async ({ page }) => {
    const task = sampleTask();
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({
      body: {
        records: [
          sampleRecord({
            created_at: "2026-10-26T09:00:00Z",
            id: "record-new",
            persist_status: "unconfirmed",
            push_status: "unknown",
          }),
          sampleRecord({
            commit_status: "no_changes",
            created_at: "2026-10-26T08:00:00Z",
            id: "record-old",
            push_status: "not_attempted",
          }),
        ],
        total: 2,
      },
    }));

    await openDetail(page);

    await expect(page.getByText("共 2 条记录")).toBeVisible();
    await expect(page.getByText("record-new")).toBeVisible();
    await expect(page.getByText("record-old")).toBeVisible();
    await expect(page.getByText("推送结果不确定，需核验")).toBeVisible();
    await expect(page.getByText("未尝试推送")).toBeVisible();
    await expect(page.getByText("没有可提交的变更")).toBeVisible();
    // 记录的顺序与服务端一致：最新的记录在前
    const newest = await page.getByText("record-new").boundingBox();
    const oldest = await page.getByText("record-old").boundingBox();
    expect(newest!.y).toBeLessThan(oldest!.y);
  });

  test("任务状态为 completed 不推导代码保存完整成功", async ({ page }) => {
    const task = sampleTask({ status: "completed" });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);
    await stubCodeSaves(page, () => ({ body: { records: [], total: 0 } }));

    await openDetail(page);

    await expect(page.getByText("已确认收尾")).toBeVisible();
    await expect(page.getByText(/该任务当前没有代码保存记录/)).toBeVisible();
    await expect(page.getByText(/不代表任务成功/)).toBeVisible();
  });
});
