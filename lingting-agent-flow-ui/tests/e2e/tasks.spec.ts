import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 任务页面的浏览器测试。
 *
 * 所有用例都通过拦截 `/api/v1/tasks` 相关请求模拟后端响应，因此不需要真实 Go 服务、
 * 数据库或执行器；页面行为（加载、空状态、错误、创建、取消、删除、分页、筛选、详情 404）
 * 都在真实浏览器中验证。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    created_at: "2025-01-01T00:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "queued",
    status_finished: false,
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

/** 健康检查响应，使页面外壳处于已连接状态 */
async function stubHealth(page: Page) {
  await page.route("**/api/v1/health", (route) =>
    route.fulfill({
      body: JSON.stringify({
        checks: [
          { name: "http_api", status: "pass" },
          { name: "database", status: "pass" },
        ],
        checked_at: "2025-01-01T00:00:00Z",
        status: "ok",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 记录并应答任务列表请求 */
async function stubTaskList(
  page: Page,
  handler: (url: URL) => { body: unknown; status?: number } | "abort",
) {
  const requests: URL[] = [];
  await page.route("**/api/v1/tasks?*", async (route: Route) => {
    const url = new URL(route.request().url());
    requests.push(url);
    const result = handler(url);
    if (result === "abort") {
      await route.abort("failed");
      return;
    }
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return requests;
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("任务列表", () => {
  test("加载成功后展示真实任务数据", async ({ page }) => {
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));

    await page.goto("/tasks");

    await expect(page.getByText("实现登录接口")).toBeVisible();
    await expect(page.getByText("为登录页接入真实接口")).toBeVisible();
    await expect(page.getByText("等待执行").first()).toBeVisible();
    // 共 1 条由接口 total 派生，不是硬编码
    await expect(page.getByText(/共\s*1\s*条/)).toBeVisible();
  });

  test("空列表显示明确的空状态", async ({ page }) => {
    await stubTaskList(page, () => ({
      body: { current: 1, records: [], size: 20, total: 0 },
    }));

    await page.goto("/tasks");

    await expect(page.getByText("没有符合条件的任务")).toBeVisible();
  });

  test("接口失败时展示错误与重试入口，且不显示伪造数据", async ({ page }) => {
    await stubTaskList(page, () => "abort");

    await page.goto("/tasks");

    await expect(page.getByText("加载任务失败")).toBeVisible();
    await expect(page.getByText(/无法连接 AgentFlow 服务/)).toBeVisible();
    await expect(page.getByRole("button", { name: "重 试" })).toBeVisible();
    await expect(page.getByText("实现登录接口")).toHaveCount(0);
  });

  test("后端返回 500 时展示服务端错误说明", async ({ page }) => {
    await stubTaskList(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));

    await page.goto("/tasks");

    await expect(page.getByText("加载任务失败")).toBeVisible();
    await expect(page.getByText("服务内部错误")).toBeVisible();
  });

  test("分页参数来自表格分页控件", async ({ page }) => {
    const requests = await stubTaskList(page, (url) => {
      const current = url.searchParams.get("current") ?? "1";
      return {
        body: {
          current: Number(current),
          records: [sampleTask({ id: `task-${current}`, name: `第 ${current} 页任务` })],
          size: 10,
          total: 30,
        },
      };
    });

    await page.goto("/tasks");

    await expect(page.getByText("第 1 页任务")).toBeVisible();
    expect(requests[0]?.searchParams.get("size")).toBe("20");

    await page.getByTitle("2", { exact: true }).click();

    await expect(page.getByText("第 2 页任务")).toBeVisible();
    expect(requests.at(-1)?.searchParams.get("current")).toBe("2");
  });

  test("状态筛选把 status 传给接口", async ({ page }) => {
    const requests = await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask({ status: "queued" })], size: 20, total: 1 },
    }));

    await page.goto("/tasks");
    await expect.poll(() => requests.length).toBeGreaterThan(0);
    expect(requests.at(-1)?.searchParams.get("status")).toBeNull();

    // 工具栏的状态筛选：选择状态后列表自动按该状态重新请求
    const filter = page.locator(".ant-pro-table-list-toolbar .ant-select").first();
    const combobox = filter.getByRole("combobox");
    await combobox.click();
    // 下拉是虚拟列表，选项节点不可见，用键盘选中第一个状态 queued
    await combobox.press("ArrowDown");
    await combobox.press("Enter");

    // 键盘选中了第二个状态 preparing，接口必须收到同一个取值
    await expect.poll(() => requests.at(-1)?.searchParams.get("status")).toBe("preparing");
  });
});

test.describe("任务操作", () => {
  test("创建任务后刷新真实列表", async ({ page }) => {
    let created = false;
    await page.route("**/api/v1/tasks", async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      created = true;
      await route.fulfill({
        body: JSON.stringify(sampleTask({ id: "task-new", name: "新建任务" })),
        contentType: "application/json",
        status: 201,
      });
    });
    await stubTaskList(page, () => ({
      body: {
        current: 1,
        records: created ? [sampleTask({ id: "task-new", name: "新建任务" })] : [],
        size: 20,
        total: created ? 1 : 0,
      },
    }));
    // 语言环境与版本来自真实环境查询：不可用版本不会被选中，因此这里返回一个可用版本
    await page.route("**/api/v1/environments?*", (route) =>
      route.fulfill({
        body: JSON.stringify({
          records: [
            {
              current_version: "v0001",
              latest_version: "v0001",
              name: "go",
              type: "language",
              versions: [
                {
                  build_status: "succeeded",
                  created_at: "2026-10-26T08:00:00Z",
                  image_ref: "agentflow-language-go:v0001-abcdefabcdef",
                  number: 1,
                  usable: true,
                  version: "v0001",
                },
              ],
            },
          ],
          total: 1,
        }),
        contentType: "application/json",
        status: 200,
      }),
    );

    await page.goto("/tasks");
    await page.getByRole("button", { name: "创建任务" }).click();

    await page.getByLabel("任务名称").fill("新建任务");
    await page.getByLabel("任务描述").fill("描述内容");
    await page.getByLabel("执行节点标识").fill("host-1");
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/team/repo");
    await page.getByRole("textbox", { name: "* 基础分支" }).fill("main");
    await page.getByLabel("语言环境").click();
    await page.locator(".ant-select-dropdown:visible").getByTitle(/^go/).click();
    await page.getByLabel("环境版本").click();
    await page
      .locator(".ant-select-dropdown:visible")
      .getByTitle(/^v0001/)
      .click();
    await page.getByLabel("规划角色标识").fill("planner");
    await page.getByLabel("开发角色标识").fill("developer");

    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText("新建任务")).toBeVisible();
  });

  test("取消按钮对已停止的任务不可用，对等待中的任务可用", async ({ page }) => {
    await stubTaskList(page, () => ({
      body: {
        current: 1,
        records: [
          sampleTask({ id: "queued", name: "等待任务" }),
          sampleTask({
            id: "failed",
            name: "失败任务",
            status: "failed",
            status_finished: true,
          }),
        ],
        size: 20,
        total: 2,
      },
    }));

    await page.goto("/tasks");

    const rows = page.locator(".ant-table-tbody tr.ant-table-row");
    await expect(rows).toHaveCount(2);
    // 等待中的任务可以取消
    await expect(rows.nth(0).getByText("取消")).toBeEnabled();
    // failed 已停止执行，取消不可用但删除可用
    await expect(rows.nth(1).getByText("取消")).toHaveClass(/ant-typography-disabled/);
    await expect(rows.nth(1).getByText("删除")).toBeEnabled();
  });

  test("取消等待中的任务后重新拉取列表", async ({ page }) => {
    let cancelled = false;
    await page.route("**/api/v1/tasks/*/cancel", async (route) => {
      cancelled = true;
      await route.fulfill({
        body: JSON.stringify(
          sampleTask({
            cancel_requested: true,
            status: "cancelled",
            status_finished: true,
            status_terminal: true,
          }),
        ),
        contentType: "application/json",
        status: 200,
      });
    });
    await stubTaskList(page, () => ({
      body: {
        current: 1,
        records: [
          sampleTask(
            cancelled
              ? {
                  cancel_requested: true,
                  status: "cancelled",
                  status_finished: true,
                  status_terminal: true,
                }
              : {},
          ),
        ],
        size: 20,
        total: 1,
      },
    }));

    await page.goto("/tasks");
    await expect(page.getByText("等待执行").first()).toBeVisible();

    await page.getByText("取消", { exact: true }).click();

    await expect(page.getByText("已取消").first()).toBeVisible();
  });

  test("删除已停止的任务后从列表移除", async ({ page }) => {
    let deleted = false;
    await page.route("**/api/v1/tasks/task-1", async (route) => {
      if (route.request().method() !== "DELETE") {
        await route.fallback();
        return;
      }
      deleted = true;
      await route.fulfill({ body: "", status: 204 });
    });
    await stubTaskList(page, () => ({
      body: {
        current: 1,
        records: deleted
          ? []
          : [
              sampleTask({
                status: "completed",
                status_finished: true,
                status_terminal: true,
              }),
            ],
        size: 20,
        total: deleted ? 0 : 1,
      },
    }));

    await page.goto("/tasks");
    await expect(page.getByText("已完成").first()).toBeVisible();

    await page.getByText("删除", { exact: true }).click();
    await page.getByRole("button", { name: "确 定" }).click();

    await expect(page.getByText("没有符合条件的任务")).toBeVisible();
  });

  test("操作失败时展示服务端业务提示", async ({ page }) => {
    await page.route("**/api/v1/tasks/*/cancel", (route) =>
      route.fulfill({
        body: JSON.stringify({
          error: { code: "state_conflict", message: "任务已结束，状态为 completed" },
        }),
        contentType: "application/json",
        status: 409,
      }),
    );
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));

    await page.goto("/tasks");
    await page.getByText("取消", { exact: true }).click();

    await expect(page.getByText("操作失败")).toBeVisible();
    await expect(page.getByText("任务已结束，状态为 completed")).toBeVisible();
  });
});

test.describe("任务详情", () => {
  test("点击任务名称展示接口返回的详情字段", async ({ page }) => {
    await page.route("**/api/v1/tasks/task-1", (route) =>
      route.fulfill({
        body: JSON.stringify(
          sampleTask({
            project_id: "demo-service",
            started_at: "2025-01-01T00:00:10Z",
          }),
        ),
        contentType: "application/json",
        status: 200,
      }),
    );
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));

    await page.goto("/tasks");
    await page.getByText("实现登录接口").first().click();

    await expect(page.getByText("任务详情")).toBeVisible();
    await expect(page.getByText("demo-service")).toBeVisible();
    await expect(page.getByText("task-1")).toBeVisible();
  });

  test("详情返回 404 时展示错误而不是伪造任务", async ({ page }) => {
    await page.route("**/api/v1/tasks/task-1", (route) =>
      route.fulfill({
        body: JSON.stringify({ error: { code: "not_found", message: "任务不存在: task-1" } }),
        contentType: "application/json",
        status: 404,
      }),
    );
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));

    await page.goto("/tasks");
    await page.getByText("实现登录接口").first().click();

    await expect(page.getByText("操作失败")).toBeVisible();
    await expect(page.getByText("任务不存在: task-1")).toBeVisible();
    await expect(page.getByText("任务详情")).toHaveCount(0);
  });
});
