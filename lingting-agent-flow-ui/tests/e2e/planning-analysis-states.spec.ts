import { expect, test } from "@playwright/test";

import { installPlanningBackend } from "./planning-backend";
import {
  createSession,
  openWorkbench,
  selectHost,
  selectProject,
  submitRequirement,
} from "./planning-ui";

/**
 * 分析阶段的各种现场：失败、进行中、并发冲突、连接中断与执行节点判定。
 *
 * 用例只依赖受控后端，不调用真实模型；断言关注"页面是否如实展示服务端事实"。
 */
test("首轮分析失败显示真实失败摘要并可显式重试", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    analysis: { kind: "failure", message: "需求分析师调用超时：本轮分析未产出可用结果" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);
  await submitRequirement(page, "请实现需求工作台");

  // 失败摘要来自服务端记录，不是前端推断
  await expect(page.getByText("分析失败").first()).toBeVisible();
  await expect(page.getByText(/需求分析师调用超时：本轮分析未产出可用结果/).first()).toBeVisible();
  // 失败会话不允许直接追加需求，只能显式重试
  await expect(
    page.getByText("本轮分析失败：请先显式重试，重试成功后再继续追加需求。"),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();
  await expect(page.getByText("还没有可预览的方案")).toBeVisible();

  backend.setAnalysis({ kind: "success" });
  await page.getByRole("button", { name: "重试分析" }).click();

  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
  await expect(page.getByText("方案已生成").first()).toBeVisible();
  expect(backend.state.counters.retry).toBe(1);
  // 重试没有追加第二条用户消息
  expect(backend.state.counters.message).toBe(1);
});

test("分析进行中禁止重复提交，只发出一次分析请求", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    analysis: { kind: "deferred" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);
  await submitRequirement(page, "请实现需求工作台");

  await expect(page.getByText("正在执行分析")).toBeVisible();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();
  await expect(
    page.getByText("正在执行本轮分析：请等待服务端返回结果，页面不会重复提交同一条需求。"),
  ).toBeVisible();
  // 连点不会产生第二个请求
  await page.getByRole("button", { name: "提交需求并分析" }).click({ force: true });
  await expect.poll(() => backend.state.counters.analysis).toBe(1);
  expect(backend.state.counters.message).toBe(1);

  backend.release({ kind: "success" });
  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
});

test("收到 409 时提示状态冲突且不重复追加用户消息", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    analysis: { kind: "conflict" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);
  await submitRequirement(page, "请实现需求工作台");

  await expect(page.getByText(/服务端返回状态冲突（409）/)).toBeVisible();
  expect(backend.state.counters.message).toBe(1);
  expect(backend.state.counters.analysis).toBe(1);
  // 会话仍为分析中：页面提供显式恢复入口，不自动重复提交
  await expect(page.getByText("分析中").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "恢复分析" })).toBeVisible();

  backend.setAnalysis({ kind: "success" });
  await page.getByRole("button", { name: "恢复分析" }).click();

  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
  expect(backend.state.counters.retry).toBe(1);
  expect(backend.state.counters.message).toBe(1);
});

test("网络中断后不把请求判定为模型执行失败", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    analysis: { kind: "network" },
    projects: [{ default_host_id: "host-a", id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);
  await submitRequirement(page, "请实现需求工作台");

  await expect(page.getByText("分析结果无法确认")).toBeVisible();
  await expect(page.getByText(/无法确认本轮分析是否已在服务端完成/)).toBeVisible();
  // 服务端仍记录 analyzing：页面不显示成功，也不伪造失败摘要
  await expect(page.getByText("分析中").first()).toBeVisible();
  await expect(page.getByText("还没有可预览的方案")).toBeVisible();
  await expect(page.getByRole("button", { name: "恢复分析" })).toBeVisible();

  backend.setAnalysis({ kind: "success" });
  await page.getByRole("button", { name: "恢复分析" }).click();

  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
  expect(backend.state.counters.analysis).toBe(1);
  expect(backend.state.counters.retry).toBe(1);
});

test("没有默认执行节点时必须显式选择，并按所选节点发起分析", async ({ page }) => {
  const backend = await installPlanningBackend(page, {
    hosts: [
      { id: "host-a", name: "执行节点 A" },
      { id: "host-b", name: "执行节点 B" },
    ],
    projects: [{ id: "demo-service", name: "示例服务" }],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await createSession(page);

  // 不自动选中列表中的第一个节点
  await expect(page.getByLabel("选择执行节点")).toBeVisible();
  await expect(
    page.getByText(
      "该项目没有默认执行节点，请先显式选择执行节点后再发起分析；页面不会自动选择节点。",
    ),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "提交需求并分析" })).toBeDisabled();

  await selectHost(page, "执行节点 B");
  await expect(page.getByText(/必须由你选择；页面不会自动选中列表中的第一个节点/)).toBeVisible();
  await submitRequirement(page, "请实现需求工作台");

  await expect(page.getByText(/第 1 版需求摘要/)).toBeVisible();
  expect(backend.state.analysisHosts).toEqual(["host-b"]);
});

test("已绑定执行节点的会话不允许在页面改指", async ({ page }) => {
  await installPlanningBackend(page, {
    hosts: [
      { id: "host-a", name: "执行节点 A" },
      { id: "host-b", name: "执行节点 B" },
    ],
    projects: [{ id: "demo-service", name: "示例服务" }],
    sessions: [
      {
        hostID: "host-b",
        id: "session-bound",
        messages: [{ content: "绑定节点的历史需求", sender: "user" }],
        projectID: "demo-service",
        status: "active",
      },
    ],
  });

  await openWorkbench(page);
  await selectProject(page, "示例服务");
  await page.getByRole("button", { name: "选择会话" }).click();

  await expect(page.getByText("会话已绑定该执行节点，页面不允许改指其他节点。")).toBeVisible();
  await expect(page.getByLabel("选择执行节点")).toHaveCount(0);
});
