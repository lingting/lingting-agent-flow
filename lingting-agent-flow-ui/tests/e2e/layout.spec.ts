import { expect, test, type Page } from "@playwright/test";

/** 规划中的六个一级路由与对应的导航标签、页面标题 */
const ROUTES = [
  {
    path: "/overview",
    label: "概览",
    text: "AgentFlow 的总览入口，用于汇总需求、任务与执行状态。",
  },
  { path: "/tasks", label: "任务", text: "创建任务" },
  { path: "/projects", label: "项目", text: "项目管理将在后续步骤实现。" },
  { path: "/machines", label: "机器", text: "执行机器管理将在后续步骤实现。" },
  { path: "/environments", label: "环境", text: "运行环境管理将在后续步骤实现。" },
  { path: "/settings", label: "设置", text: "设置项将在后续步骤实现。" },
] as const;

/**
 * 暂不从布局用例断言正文的路由。
 *
 * `/machines` 已升级为真实的执行节点管理页（`src/pages/machines/MachinesPage.tsx`），
 * 原来那句「执行机器管理将在后续步骤实现。」的占位文案已不存在；该页正文取决于
 * `/api/v1/hosts` 的真实响应，需要在补齐成功响应 stub 之后再恢复正文断言。
 *
 * TODO：补齐 machines 页的正文断言后，把该路由从本清单移除。
 */
const ROUTES_PENDING_TEXT: readonly string[] = ["/machines"];

/** 参与正文断言的路由：与 `ROUTES` 的差异只来自上面的待补清单 */
const ROUTES_WITH_TEXT = ROUTES.filter((route) => !ROUTES_PENDING_TEXT.includes(route.path));

/** 收集页面渲染期间的脚本错误，用于断言不存在 React 渲染错误 */
function collectPageErrors(page: Page) {
  const errors: string[] = [];

  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const text = message.text();
    // 资源加载失败与 React 渲染错误无关，这里只关心脚本级错误
    if (text.includes("Failed to load resource")) return;
    errors.push(text);
  });

  return errors;
}

async function expectNoHorizontalOverflow(page: Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );

  expect(overflow).toBeLessThanOrEqual(0);
}

test.describe("基础布局", () => {
  for (const route of ROUTES) {
    test(`${route.path} 可访问且导航高亮、页面标题一致`, async ({ page }) => {
      const errors = collectPageErrors(page);

      await page.goto(route.path);

      await expect(page).toHaveURL(new RegExp(`${route.path}$`));
      // 正文断言只覆盖仍在断言占位文案的路由；/machines 的正文等待补齐 stub 后再断言。
      if (ROUTES_WITH_TEXT.includes(route)) {
        await expect(page.locator("main")).toContainText(route.text);
      }
      await expect(page.locator(".ant-menu-item-selected")).toHaveText(route.label);
      await expect(page).toHaveTitle(`AgentFlow - ${route.label}`);
      await expectNoHorizontalOverflow(page);
      expect(errors).toEqual([]);
    });
  }

  test("根路径重定向到概览页", async ({ page }) => {
    await page.goto("/");

    await expect(page).toHaveURL(/\/overview$/);
    await expect(page).toHaveTitle("AgentFlow - 概览");
  });

  test("点击导航切换页面并同步高亮", async ({ page }) => {
    await page.goto("/overview");

    await page.getByRole("menuitem", { name: "项目" }).click();

    await expect(page).toHaveURL(/\/projects$/);
    await expect(page.locator(".ant-menu-item-selected")).toHaveText("项目");
    await expect(page.locator("main")).toContainText("项目管理将在后续步骤实现。");
  });

  test("未知路径显示 404 页面", async ({ page }) => {
    const errors = collectPageErrors(page);

    await page.goto("/no-such-page");

    await expect(page.getByText("页面不存在")).toBeVisible();
    await expect(page.getByRole("button", { name: /返回首页/ })).toBeVisible();
    await expectNoHorizontalOverflow(page);
    expect(errors).toEqual([]);
  });

  test("404 页面可以返回概览页", async ({ page }) => {
    await page.goto("/no-such-page");

    await page.getByRole("button", { name: /返回首页/ }).click();

    await expect(page).toHaveURL(/\/overview$/);
    await expect(page.locator(".ant-menu-item-selected")).toHaveText("概览");
  });
});

test.describe("响应式布局", () => {
  test("窄视口下侧栏切换为抽屉且不覆盖内容", async ({ page }) => {
    await page.setViewportSize({ width: 420, height: 820 });
    await page.goto("/tasks");

    // 抽屉展示方式下侧栏默认隐藏，内容区独占宽度
    await expect(page.locator(".sidebar-layout__sidebar")).toHaveCount(0);
    await expect(page.locator("main")).toContainText("创建任务");
    await expectNoHorizontalOverflow(page);

    await page.getByRole("button", { name: "切换侧边栏" }).click();

    await expect(page.locator(".ant-drawer")).toBeVisible();
    await expect(page.locator(".ant-drawer").getByRole("menuitem", { name: "项目" })).toBeVisible();
    await expectNoHorizontalOverflow(page);
  });

  test("桌面视口显示完整侧栏与导航", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/environments");

    await expect(page.locator(".sidebar-layout__sidebar")).toBeVisible();
    await expect(page.getByRole("menuitem")).toHaveCount(ROUTES.length);
    await expectNoHorizontalOverflow(page);
  });
});
