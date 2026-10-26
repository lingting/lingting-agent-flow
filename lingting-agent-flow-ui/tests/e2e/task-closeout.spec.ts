import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 任务最终结论与收尾闭环展示的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型。
 * 用例只验证一件事：页面是否如实呈现服务端给出的三个独立事实
 * （任务状态 / 最终业务结论 / 收尾闭环），并且不会把 `completed`、
 * `succeeded` 与 `confirmed` 混为同一个概念。
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
    status: "completed",
    status_finished: true,
    status_terminal: true,
    updated_at: "2026-10-26T08:00:00Z",
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

/** 拦截任务详情，返回给定任务或给定错误 */
async function stubTaskDetail(page: Page, task: unknown, status = 200) {
  await page.route("**/api/v1/tasks/task-1", (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      body: JSON.stringify(task),
      contentType: "application/json",
      status,
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

test.describe("任务最终结论与收尾闭环", () => {
  test("succeeded + confirmed 展示已确认完成", async ({ page }) => {
    const task = sampleTask({
      closeout: "confirmed",
      closeout_summary: "任务与工作流执行记录一致确认收尾完成",
      final_result: "succeeded",
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("最终业务结论")).toBeVisible();
    await expect(page.getByText("成功", { exact: true })).toBeVisible();
    await expect(page.getByText("已确认收尾")).toBeVisible();
    await expect(page.getByText("任务与工作流执行记录一致确认收尾完成")).toBeVisible();
    // 已确认收尾时不应出现"尚未确认完成"的警示
    await expect(page.getByText("任务尚未确认完成")).toHaveCount(0);
    await expect(page.getByText(/不得当作已通过验收/)).toHaveCount(0);
  });

  test("status=completed 但没有 final_result 时不得显示确定成功", async ({ page }) => {
    const task = sampleTask({ closeout: "unconfirmed", status: "completed" });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("未提供最终结论")).toBeVisible();
    await expect(page.getByText("收尾未闭环")).toBeVisible();
    await expect(page.getByText("任务尚未确认完成")).toBeVisible();
    await expect(page.getByText(/没有提供最终业务结论/)).toBeVisible();
    // 页面不得出现确定成功的结论
    await expect(page.getByText("成功", { exact: true })).toHaveCount(0);
  });

  test("final_result=succeeded 但 closeout=unconfirmed 时明确展示尚未闭环", async ({ page }) => {
    const task = sampleTask({
      closeout: "unconfirmed",
      closeout_summary: "任务结论尚未闭环，需要继续推进或人工核验",
      final_result: "succeeded",
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("成功", { exact: true })).toBeVisible();
    await expect(page.getByText("收尾未闭环")).toBeVisible();
    await expect(page.getByText(/最终结论为成功，但收尾没有确认完成/)).toBeVisible();
    await expect(page.getByText(/尚未闭环/).first()).toBeVisible();
    await expect(page.getByText("任务尚未确认完成")).toBeVisible();
  });

  test("closeout=inconsistent 提示人工核验而不是确定成功", async ({ page }) => {
    const task = sampleTask({
      closeout: "inconsistent",
      closeout_summary:
        "任务被标记为成功，但工作流执行记录没有确认完成；该现场必须人工核验，不得当作确定成功",
      final_result: "succeeded",
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("收尾不一致，需人工核验")).toBeVisible();
    await expect(page.getByText("收尾结论不一致，需要人工核验")).toBeVisible();
    await expect(page.getByText(/必须人工核验/).first()).toBeVisible();
    await expect(page.getByText(/该现场必须人工核验/)).toBeVisible();
    await expect(page.getByText("任务尚未确认完成")).toHaveCount(0);
  });

  test("closeout=pending 展示仍在推进，不表述为已收尾", async ({ page }) => {
    const task = sampleTask({
      closeout: "pending",
      closeout_summary: "工作流仍在推进，收尾尚未结束",
      final_result: "succeeded",
      status: "developing",
      status_finished: false,
      status_terminal: false,
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("收尾进行中")).toBeVisible();
    await expect(page.getByText("工作流仍在推进，收尾尚未结束", { exact: true })).toBeVisible();
    await expect(page.getByText(/收尾尚未结束/).first()).toBeVisible();
    await expect(page.getByText("已确认收尾")).toHaveCount(0);
  });

  test("final_result=failed 展示失败含义", async ({ page }) => {
    const task = sampleTask({
      closeout: "unconfirmed",
      closeout_summary: "任务结论尚未闭环，需要继续推进或人工核验",
      final_result: "failed",
      status: "failed",
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("失败", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("收尾未闭环")).toBeVisible();
    await expect(page.getByText("成功", { exact: true })).toHaveCount(0);
  });

  test("final_result=cancelled 展示取消含义", async ({ page }) => {
    const task = sampleTask({
      closeout: "cancelled",
      closeout_summary: "任务已按取消请求结束",
      final_result: "cancelled",
      status: "cancelled",
      status_terminal: true,
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("已取消", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("已按取消结束")).toBeVisible();
    await expect(page.getByText("任务已按取消请求结束")).toBeVisible();
    await expect(page.getByText("任务尚未确认完成")).toHaveCount(0);
  });

  test("必需字段缺失时页面稳定且不误报成功", async ({ page }) => {
    const task = sampleTask({ final_result: undefined });
    delete (task as Record<string, unknown>).closeout;
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("未提供最终结论")).toBeVisible();
    await expect(page.getByText("未提供收尾结论")).toBeVisible();
    await expect(page.getByText("服务端未提供收尾说明")).toBeVisible();
    await expect(page.getByText("成功", { exact: true })).toHaveCount(0);
  });

  test("未知枚举值原样展示且不猜测为成功", async ({ page }) => {
    const task = sampleTask({
      closeout: "future_closeout",
      closeout_summary: "服务端给出的新结论",
      final_result: "future_result",
    });
    await stubTaskList(page, task);
    await stubTaskDetail(page, task);

    await openDetail(page);

    await expect(page.getByText("future_result", { exact: true })).toBeVisible();
    await expect(page.getByText("future_closeout", { exact: true })).toBeVisible();
    await expect(page.getByText("服务端给出的新结论")).toBeVisible();
    await expect(page.getByText(/未在当前版本中定义/).first()).toBeVisible();
    await expect(page.getByText("成功", { exact: true })).toHaveCount(0);
  });

  test("任务详情加载失败时不伪造默认成功结论", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page, { error: { code: "not_found", message: "任务不存在: task-1" } }, 404);

    await page.goto("/tasks");
    await page.getByText("实现登录接口").first().click();

    await expect(page.getByText("操作失败")).toBeVisible();
    await expect(page.getByText("任务不存在: task-1")).toBeVisible();
    await expect(page.getByText("任务详情")).toHaveCount(0);
    await expect(page.getByText("已确认收尾")).toHaveCount(0);
    await expect(page.getByText("成功", { exact: true })).toHaveCount(0);
  });
});
