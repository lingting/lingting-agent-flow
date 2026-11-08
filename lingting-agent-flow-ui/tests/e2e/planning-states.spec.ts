import { expect, test } from "@playwright/test";

import { installPlanningBackend } from "./planning-backend";
import { createSession, openWorkbench, selectProject, submitRequirement } from "./planning-ui";

/**
 * 需求工作台的前置条件、空状态、只读状态与响应式布局。
 *
 * 这些用例覆盖"页面是否拒绝不该发生的操作"：未选项目、切换项目、未知状态与已确认会话。
 */
test("未选择项目时不能创建会话或提交需求", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);

  await expect(
    page.getByText("请先选择项目：规划会话与需求消息都必须归属于一个真实项目。"),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "新建会话" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();
  await expect(page.getByLabel("需求输入")).toBeDisabled();
  await expect(page.getByText("先选择项目，才能创建或打开规划会话。")).toBeVisible();
});

test("切换项目会清除当前会话上下文", async ({ page }) => {
  await installPlanningBackend(page, {
    hosts: [{ id: "host-a", name: "执行节点 A" }],
    projects: [
      { default_host_id: "host-a", id: "demo-service", name: "示例服务" },
      { default_host_id: "host-a", id: "web-app", name: "前端应用" },
    ],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);
  await submitRequirement(page, "示例服务的需求");
  await expect(page.getByTestId("planning-message")).toHaveCount(3);
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();

  await selectProject(page, "前端应用");

  // 上一个项目的会话、消息与方案版本全部离开页面上下文
  await expect(page.getByText("该项目还没有规划会话").first()).toBeVisible();
  await expect(page.getByTestId("planning-message")).toHaveCount(0);
  await expect(page.getByText("session-1")).toHaveCount(0);
  await expect(page.getByText(/需求摘要/)).toHaveCount(0);

  // 切回原项目：会话列表重新出现，但上下文需要重新选择会话
  await selectProject(page, "示例服务");
  await expect(page.getByText("session-1").first()).toBeVisible();
  await expect(page.getByTestId("planning-message")).toHaveCount(0);
});

test("没有项目、没有会话与没有方案时都显示可读的空状态", async ({ page }) => {
  await installPlanningBackend(page, { projects: [] });

  await openWorkbench(page);

  await expect(page.getByText("没有可用项目")).toBeVisible();
  await expect(page.getByRole("button", { name: "新建会话" })).toBeDisabled();
});

test("当前项目没有会话时提示创建，创建后提示没有方案版本", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");

  await expect(page.getByText(/该项目还没有规划会话/).first()).toBeVisible();

  await createSession(page);

  await expect(page.getByText("该会话还没有消息记录。", { exact: false })).toBeVisible();
  await expect(page.getByText("还没有可预览的方案")).toBeVisible();
});

test("未知状态原样展示且不开放写操作", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
    sessions: [
      {
        hostID: "host-a",
        id: "session-unknown",
        messages: [{ content: "未知状态下的历史消息", sender: "user" }],
        projectID: "demo-service",
        status: "paused_unknown",
      },
    ],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await page.getByRole("button", { name: "选择会话" }).click();

  // 原样显示服务端取值，不映射成成功或失败
  await expect(page.getByText("paused_unknown").first()).toBeVisible();
  await expect(page.getByText(/不在当前页面已知取值中/).first()).toBeVisible();
  await expect(
    page.getByText("未知状态不开放写操作：页面不会把它当作成功、失败或可继续编辑的会话。"),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "恢复分析" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "重试分析" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "放弃规划" })).toHaveCount(0);
});

test("已确认会话只读，不提供普通消息编辑", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
    sessions: [
      {
        hostID: "host-a",
        id: "session-confirmed",
        messages: [{ content: "已确认会话的历史消息", sender: "user" }],
        projectID: "demo-service",
        revisions: 1,
        status: "confirmed",
      },
    ],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await page.getByRole("button", { name: "选择会话" }).click();

  await expect(page.getByText("已确认").first()).toBeVisible();
  await expect(page.getByText("当前状态不开放普通消息编辑：本阶段只提供只读展示。")).toBeVisible();
  await expect(page.getByText("会话方案已确认，本阶段只读展示。").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "放弃规划" })).toHaveCount(0);
  // 历史方案仍然可读
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
});

test("放弃规划调用正式接口并转为只读", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
    sessions: [
      {
        hostID: "host-a",
        id: "session-abandon",
        messages: [{ content: "准备放弃的需求", sender: "user" }],
        projectID: "demo-service",
        status: "active",
      },
    ],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await page.getByRole("button", { name: "选择会话" }).click();
  await expect(page.getByText("待输入需求").first()).toBeVisible();

  await page.getByRole("button", { name: "放弃规划" }).click();
  await page.getByRole("button", { name: "确认放弃" }).click();

  await expect(page.getByText("已放弃").first()).toBeVisible();
  await expect(page.getByText("会话已放弃，本阶段只读展示。").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "放弃规划" })).toHaveCount(0);
  // 放弃走正式接口，前端不改写状态
  expect(backend.state.counters.abandon).toBe(1);
  // 历史消息仍然保留
  await expect(page.getByText("准备放弃的需求", { exact: true })).toBeVisible();
});

/** 断言页面没有水平溢出 */
async function expectNoHorizontalOverflow(page: import("@playwright/test").Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
}

/** 打开预置的预览会话 */
async function openSeededSession(page: import("@playwright/test").Page) {
  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await page.getByRole("button", { name: "选择会话" }).click();
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
}

/** 预置一个带方案版本的会话 */
function layoutSessionSeed() {
  return {
    hostID: "host-a",
    id: "session-layout",
    messages: [
      { content: "布局检查需求", sender: "user" },
      { content: "布局检查分析", sender: "requirement_analyst" },
      { content: "布局检查方案", sender: "planner" },
    ],
    projectID: "demo-service",
    revisions: 1,
    status: "preview_ready",
  };
}

test("桌面布局完整展示方案预览且不产生水平溢出", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
    sessions: [layoutSessionSeed()],
  });

  await page.setViewportSize({ height: 900, width: 1440 });
  await openSeededSession(page);

  await expect(page.locator(".sidebar-layout__sidebar")).toBeVisible();
  await expect(page.getByText(/第 1 版方案摘要/)).toBeVisible();
  await expectNoHorizontalOverflow(page);
});

test("窄屏布局完整展示方案预览且不产生水平溢出", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
    sessions: [layoutSessionSeed()],
  });

  await page.setViewportSize({ height: 820, width: 420 });
  await openSeededSession(page);

  // 窄视口下侧栏切换为抽屉，内容区独占宽度
  await expect(page.locator(".sidebar-layout__sidebar")).toHaveCount(0);
  await expect(page.getByText(/第 1 版方案摘要/)).toBeVisible();
  await expectNoHorizontalOverflow(page);
});
