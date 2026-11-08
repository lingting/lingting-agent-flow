import { expect, test, type Page } from "@playwright/test";

import { installPlanningBackend } from "./planning-backend";
import {
  createSession,
  openWorkbench,
  selectProject,
  selectRevision,
  submitRequirement,
} from "./planning-ui";

/**
 * 方案确认的浏览器闭环。
 *
 * 所有接口都由受控后端返回：确认只提交选定的 Revision，正式需求与任务来自服务端响应，
 * 页面不提供开始执行入口，也不改写服务端给出的任务顺序。
 */

/** 打开工作台并准备一个已有方案版本的会话 */
async function preparePreviewSession(page: Page) {
  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);
  await submitRequirement(page, "请实现方案确认与正式任务生成");
  await expect(page.getByText("方案已生成").first()).toBeVisible();
}

test("确认闭环：二次确认后展示真实需求与 queued 任务", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await preparePreviewSession(page);

  // 确认前展示将要生成什么：版本号、需求标题、任务数量与顺序
  await expect(page.getByText("方案确认", { exact: true })).toBeVisible();
  await expect(page.getByText(/将生成 1 条正式需求与 2 个正式任务/)).toBeVisible();
  await expect(page.getByRole("button", { name: "确认采纳该版方案" })).toBeEnabled();

  // 二次确认必须是一次显式操作，而不是普通导航按钮
  await page.getByRole("button", { name: "确认采纳该版方案" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("确认采纳方案并生成正式实体")).toBeVisible();
  await expect(dialog.getByText(/将采纳 第 1 版/)).toBeVisible();
  await expect(dialog.getByText(/任务数量：2 个/)).toBeVisible();
  await expect(dialog.getByText(/确认后生成正式实体/)).toBeVisible();
  expect(backend.state.counters.confirm).toBe(0);

  await dialog.getByRole("button", { name: "确认生成正式需求" }).click();

  // 真实结果：需求标识、任务标识、顺序与服务端状态
  await expect(page.getByText("已生成正式需求与任务")).toBeVisible();
  await expect(page.getByText("session-1-requirement").first()).toBeVisible();
  await expect(page.getByText("session-1-requirement-task-1")).toBeVisible();
  await expect(page.getByText("session-1-requirement-task-2")).toBeVisible();
  await expect(page.getByText("任务保持待执行", { exact: true })).toBeVisible();
  await expect(page.getByText("已确认").first()).toBeVisible();

  // 顺序由服务端写定，页面只读展示
  expect(backend.state.confirmRequests).toEqual([
    { hostID: "", revisionID: "session-1-revision-1" },
  ]);
  expect(backend.state.counters.confirm).toBe(1);
  // 确认不启动执行：正式任务接口从未被调用
  expect(backend.state.counters.task).toBe(0);
  await expect(page.getByRole("button", { name: /开始执行/ })).toHaveCount(0);
});

test("选中历史版本时提交的是该版本标识", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await preparePreviewSession(page);
  await submitRequirement(page, "再补充一条约束：不要新增第三方依赖");
  await expect(page.getByText(/第 2 版需求摘要/).first()).toBeVisible();

  await selectRevision(page, 1);
  await expect(page.getByText(/第 1 版需求摘要/).first()).toBeVisible();
  await page.getByRole("button", { name: "确认采纳该版方案" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "确认生成正式需求" }).click();

  await expect(page.getByText("已生成正式需求与任务")).toBeVisible();
  expect(backend.state.confirmRequests).toEqual([
    { hostID: "", revisionID: "session-1-revision-1" },
  ]);
});

test("409 状态冲突：如实展示并重新读取服务端事实", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    confirm: { kind: "conflict" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await preparePreviewSession(page);
  await page.getByRole("button", { name: "确认采纳该版方案" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "确认生成正式需求" }).click();

  await expect(page.getByText("确认被拒绝（状态冲突）")).toBeVisible();
  await expect(page.getByText(/不要重复确认/)).toBeVisible();
  // 失败后重新读取服务端状态：会话仍是方案已生成
  await expect(page.getByText("方案已生成").first()).toBeVisible();
  expect(backend.state.counters.confirm).toBe(1);
  expect(backend.state.counters.task).toBe(0);
});

test("网络中断：不自动重放确认，提示结果不确定", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    confirm: { kind: "network" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await preparePreviewSession(page);
  await page.getByRole("button", { name: "确认采纳该版方案" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "确认生成正式需求" }).click();

  await expect(page.getByText("确认未成功")).toBeVisible();
  await expect(page.getByText(/不会自动重放确认/)).toBeVisible();
  // 只发出一次确认请求：页面不盲目重试
  expect(backend.state.counters.confirm).toBe(1);
});

test("412 前置条件不满足：说明没有生成任何正式实体", async ({ page }) => {
  await installPlanningBackend(page, {
    confirm: { kind: "prerequisite" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await preparePreviewSession(page);
  await page.getByRole("button", { name: "确认采纳该版方案" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "确认生成正式需求" }).click();

  await expect(page.getByText("确认未成功")).toBeVisible();
  await expect(page.getByText(/没有生成任何正式实体/)).toBeVisible();
});

test("已确认会话重新打开后只读展示采纳结果", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await preparePreviewSession(page);
  await page.getByRole("button", { name: "确认采纳该版方案" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "确认生成正式需求" }).click();
  await expect(page.getByText("已生成正式需求与任务")).toBeVisible();

  // 重新打开页面并恢复已确认会话：结果来自服务端，页面不再提供确认入口
  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await page.getByRole("button", { name: "选择会话" }).click();

  await expect(page.getByText("session-1-requirement").first()).toBeVisible();
  await expect(page.getByText("session-1-requirement-task-1")).toBeVisible();
  await expect(page.getByText("任务保持待执行", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "确认采纳该版方案" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /开始执行/ })).toHaveCount(0);
});
