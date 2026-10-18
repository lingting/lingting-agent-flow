import { expect, test } from "@playwright/test";

/**
 * 连接状态浏览器测试。
 *
 * 这些用例通过拦截 `/api/v1/health` 模拟后端响应，因此不需要真实 Go 服务，
 * 也不需要 Pi、SSH、Docker 或模型服务。
 */
test.describe("API 连接状态", () => {
  test("后端不可用时显示未连接且应用外壳仍可渲染", async ({ page }) => {
    await page.route("**/api/v1/health", (route) => route.abort("failed"));
    await page.goto("/overview");

    // 应用外壳与导航不受连接失败影响
    await expect(page.locator(".ant-menu-item-selected")).toHaveText("概览");
    await expect(page.getByText("未连接", { exact: true })).toBeVisible();
    await expect(page.getByText("无法连接 AgentFlow 服务", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "重新检查连接" })).toBeVisible();
  });

  test("健康检查成功时显示已连接与逐项状态", async ({ page }) => {
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
    await page.goto("/overview");

    await expect(page.getByText("已连接", { exact: true })).toBeVisible();
    await expect(page.getByText("后端连接")).toBeVisible();
    await expect(page.getByText("SQLite 数据库")).toBeVisible();
    await expect(page.getByText("正常").first()).toBeVisible();
  });

  test("后端返回 503 时显示未连接并展示服务端说明", async ({ page }) => {
    await page.route("**/api/v1/health", (route) =>
      route.fulfill({
        body: JSON.stringify({
          checks: [
            { name: "http_api", status: "pass" },
            { message: "数据库不可用", name: "database", status: "fail" },
          ],
          checked_at: "2025-01-01T00:00:00Z",
          status: "degraded",
        }),
        contentType: "application/json",
        status: 503,
      }),
    );
    await page.goto("/overview");

    await expect(page.getByText("未连接", { exact: true })).toBeVisible();
    await expect(page.getByText("数据库不可用").first()).toBeVisible();
  });

  test("重新检查按钮重新发起健康检查", async ({ page }) => {
    let shouldFail = true;
    const okBody = JSON.stringify({
      checks: [
        { name: "http_api", status: "pass" },
        { name: "database", status: "pass" },
      ],
      checked_at: "2025-01-01T00:00:00Z",
      status: "ok",
    });
    await page.route("**/api/v1/health", (route) =>
      shouldFail
        ? route.abort("failed")
        : route.fulfill({ body: okBody, contentType: "application/json", status: 200 }),
    );
    await page.goto("/overview");

    await expect(page.getByText("未连接", { exact: true })).toBeVisible();
    shouldFail = false;
    await page.getByRole("button", { name: "重新检查连接" }).click();

    await expect(page.getByText("已连接", { exact: true })).toBeVisible();
  });

  test("请求使用同源相对路径", async ({ page }) => {
    const targets: string[] = [];
    await page.route("**/api/v1/health", (route) => {
      targets.push(new URL(route.request().url()).pathname);
      return route.fulfill({ body: "{}", contentType: "application/json", status: 500 });
    });
    await page.goto("/overview");

    await expect.poll(() => targets.length).toBeGreaterThan(0);
    expect(targets[0]).toBe("/api/v1/health");
  });
});
