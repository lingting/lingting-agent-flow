import { expect, test } from "@playwright/test";

import { installPlanningBackend } from "./planning-backend";
import {
  createSession,
  openWorkbench,
  selectProject,
  selectRevision,
  submitRequirement,
} from "./planning-ui";

/**
 * 需求工作台的主流程：选择项目 → 创建会话 → 多轮分析 → 版本切换。
 *
 * 所有接口都由受控后端返回，不调用真实模型；断言只针对页面展示的服务端事实。
 */
test("完整闭环：创建会话、两轮分析并切换历史版本", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    hosts: [{ id: "host-a", name: "执行节点 A" }],
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);

  // 会话标识与状态来自创建响应
  await expect(page.getByText("session-1").first()).toBeVisible();
  await expect(page.getByText("待输入需求").first()).toBeVisible();

  await submitRequirement(page, "请实现需求工作台的多轮对话与方案预览");

  // 用户消息 + 需求分析师 + 方案规划师：三条消息分别展示
  await expect(page.getByTestId("planning-message")).toHaveCount(3);
  await expect(page.getByText("需求分析师", { exact: true })).toBeVisible();
  await expect(page.getByText("方案规划师", { exact: true })).toBeVisible();
  await expect(
    page.getByText("需求分析师输出（第 1 轮）：已完成结构化需求分析。", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("方案规划师输出（第 1 轮）：已给出任务草案与依赖顺序。", { exact: true }),
  ).toBeVisible();

  // 分析与方案版本
  await expect(page.getByText("方案已生成").first()).toBeVisible();
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
  await expect(page.getByText("接入规划接口")).toBeVisible();
  await expect(page.getByText("task-1-2").first()).toBeVisible();
  await expect(page.getByText("第 1 版全部字段可见")).toBeVisible();
  await expect(page.getByText(/不是已创建的正式任务/)).toBeVisible();
  await expect(page.getByText(/共 1 个版本/)).toBeVisible();

  // 第二轮：同一会话继续追加需求，生成第二个版本并自动跟随最新
  await submitRequirement(page, "再补充一条约束：不要新增第三方依赖");
  await expect(page.getByTestId("planning-message")).toHaveCount(6);
  await expect(page.getByText(/第 2 版需求摘要/)).toBeVisible();
  await expect(page.getByText(/共 2 个版本/)).toBeVisible();

  // 历史版本可切换，旧版本没有被覆盖
  await selectRevision(page, 1);
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
  await expect(page.getByText("task-1-1").first()).toBeVisible();
  await expect(page.getByText(/第 2 版需求摘要/)).toHaveCount(0);

  await selectRevision(page, 2);
  await expect(page.getByText(/第 2 版需求摘要/)).toBeVisible();
  await expect(page.getByText("task-2-1").first()).toBeVisible();

  // 每轮只提交一条消息、只发起一次分析；草案任务不触发正式任务接口
  expect(backend.state.counters.message).toBe(2);
  expect(backend.state.counters.analysis).toBe(2);
  expect(backend.state.counters.retry).toBe(0);
  expect(backend.state.counters.task).toBe(0);
  // 两轮都使用同一个执行节点：第一轮取项目默认节点，第二轮沿用会话已绑定的节点
  expect(backend.state.analysisHosts).toEqual(["host-a", "host-a"]);
});

test("重新打开历史会话时重新读取服务端事实", async ({ page }) => {
  await installPlanningBackend(page, {
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
    sessions: [
      {
        hostID: "host-a",
        id: "session-history",
        messages: [
          { content: "历史需求：实现需求工作台", sender: "user" },
          { content: "历史分析：已完成结构化需求分析。", sender: "requirement_analyst" },
          { content: "历史方案：已给出任务草案。", sender: "planner" },
        ],
        projectID: "demo-service",
        revisions: 1,
        status: "preview_ready",
      },
    ],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  // 会话列表展示真实状态与最后更新时间
  await expect(page.getByText("session-history").first()).toBeVisible();
  await expect(page.getByText("方案已生成").first()).toBeVisible();
  await page.getByRole("button", { name: "选择会话" }).click();

  await expect(page.getByTestId("planning-message")).toHaveCount(3);
  await expect(page.getByText("历史需求：实现需求工作台", { exact: true })).toBeVisible();
  await expect(page.getByText("历史分析：已完成结构化需求分析。", { exact: true })).toBeVisible();
  await expect(page.getByText("历史方案：已给出任务草案。", { exact: true })).toBeVisible();
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
});
