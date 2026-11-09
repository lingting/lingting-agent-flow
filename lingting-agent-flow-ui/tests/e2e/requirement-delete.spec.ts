import { expect, test, type Page } from "@playwright/test";

import { installRequirementsBackend, type RequirementSeed } from "./requirements-backend";

/**
 * 需求页停止与删除的浏览器闭环。
 *
 * 所有接口都由受控后端返回：页面状态全部来自服务端响应，删除是显式二次确认动作，
 * `409` 与连接中断分别如实展示并重新读取服务端事实。
 */

/** 页面主体区域：把按钮查询限定在内容区，避免与弹窗中的同名按钮冲突 */
function panel(page: Page) {
  return page.getByRole("main");
}

/** 打开需求页并选中指定需求 */
async function openRequirement(page: Page, requirementID: string) {
  await page.goto(`/requirements?requirement_id=${encodeURIComponent(requirementID)}`);
  await expect(page.getByText("需求与真实进度")).toBeVisible();
}

/** 一个包含三个任务的需求 */
function runningSeed(): RequirementSeed[] {
  return [
    {
      id: "req-running",
      projectID: "demo-service",
      tasks: [
        { id: "task-1", status: "completed" },
        { id: "task-2", status: "developing" },
        { id: "task-3", status: "queued" },
      ],
      title: "运行中的需求",
    },
  ];
}

test("执行中的任务先停止再删除", async ({ page }) => {
  const backend = await installRequirementsBackend(page, { requirements: runningSeed() });
  await openRequirement(page, "req-running");

  // 执行中：删除禁用并给出原因，停止可用
  const deleteButton = panel(page).getByRole("button", { name: "删除需求" });
  const stopButton = panel(page).getByRole("button", { name: "停止任务" });
  await expect(deleteButton).toBeDisabled();
  await expect(page.getByText(/任务 task-2 仍在执行（developing）/)).toBeVisible();
  await expect(stopButton).toBeEnabled();

  // 停止：复用任务取消接口，页面立即重新读取真实状态
  await stopButton.click();
  await expect(page.getByText("任务正在停止中")).toBeVisible();
  await expect(page.getByText(/task-2 的停止请求已记录/)).toBeVisible();
  await expect(deleteButton).toBeDisabled();
  await expect(stopButton).toBeDisabled();
  expect(backend.state.cancelCalls).toEqual(["task-2"]);

  // 真实收敛后删除才可用
  backend.setTaskStatus("task-2", "cancelled");
  await panel(page).getByRole("button", { name: "刷新" }).click();
  await expect(page.getByText("任务正在停止中")).toHaveCount(0);
  await expect(deleteButton).toBeEnabled();

  // 二次确认必须展示需求、任务数量、状态与历史保留说明
  await deleteButton.click();
  const dialog = page.getByRole("dialog").last();
  // 标题与确认按钮同名，按文本定位只取标题本身
  await expect(dialog.getByText("确认删除需求", { exact: true }).first()).toBeVisible();
  await expect(dialog.getByText("需求标题：运行中的需求")).toBeVisible();
  await expect(dialog.getByText(/关联任务：3 个/)).toBeVisible();
  await expect(dialog.getByText(/task-2（cancelled）/)).toBeVisible();
  await expect(
    dialog.getByText(/Step、Attempt、Session、Event、CodeSave 与容器记录全部保留/),
  ).toBeVisible();
  await expect(dialog.getByText(/Git 仓库不会自动回滚/)).toBeVisible();
  expect(backend.state.deleteCalls).toBe(0);

  await dialog.getByRole("button", { name: "确认删除需求" }).click();

  // 成功后清空选择并从默认列表移除
  expect(backend.state.deleteCalls).toBe(1);
  expect(backend.state.deletedRequirements).toEqual(["req-running"]);
  await expect(page.getByText("需求详情", { exact: true })).toHaveCount(0);
  await expect(page.getByText("运行中的需求")).toHaveCount(0);
  await expect(page.getByText("该项目还没有正式需求：需求在方案确认后生成")).toBeVisible();
});

test("删除被拒绝后重新读取服务端事实", async ({ page }) => {
  const backend = await installRequirementsBackend(page, {
    deleteMode: { kind: "conflict" },
    requirements: [
      {
        id: "req-conflict",
        projectID: "demo-service",
        tasks: [{ id: "task-1", status: "failed" }],
        title: "冲突需求",
      },
    ],
  });
  await openRequirement(page, "req-conflict");

  // 前端认为可以删除（任务已失败），但服务端在事务内发现现场未收敛
  const deleteButton = panel(page).getByRole("button", { name: "删除需求" });
  await expect(deleteButton).toBeEnabled();
  backend.setTaskStatus("task-1", "cancelling");
  await deleteButton.click();
  await page.getByRole("dialog").last().getByRole("button", { name: "确认删除需求" }).click();

  await expect(page.getByText("操作未成功")).toBeVisible();
  await expect(page.getByText(/操作被拒绝：/)).toBeVisible();
  expect(backend.state.deleteCalls).toBe(1);

  // 重新读取后按真实状态禁用删除，不残留"可以删除"的按钮状态
  await expect(deleteButton).toBeDisabled();
  await expect(page.getByText(/任务 task-1 正在停止（cancelling）/)).toBeVisible();
});

test("网络中断不自动重发删除请求", async ({ page }) => {
  const backend = await installRequirementsBackend(page, {
    deleteMode: { kind: "network" },
    requirements: [
      {
        id: "req-network",
        projectID: "demo-service",
        tasks: [{ id: "task-1", status: "failed" }],
        title: "中断需求",
      },
    ],
  });
  await openRequirement(page, "req-network");
  await panel(page).getByRole("button", { name: "删除需求" }).click();
  await page.getByRole("dialog").last().getByRole("button", { name: "确认删除需求" }).click();

  // 结果未知：明确提示并重新查询，但不重复提交有副作用的请求
  await expect(page.getByText("结果需要确认")).toBeVisible();
  await expect(page.getByText("操作结果未知")).toBeVisible();
  await expect(page.getByText(/结果未知/).first()).toBeVisible();
  expect(backend.state.deleteCalls).toBe(1);
  await page.waitForTimeout(300);
  expect(backend.state.deleteCalls).toBe(1);
  await expect(panel(page).getByRole("button", { name: "删除需求" })).toBeVisible();
});

test("未知任务状态只读展示并阻止删除", async ({ page }) => {
  await installRequirementsBackend(page, {
    requirements: [
      {
        id: "req-unknown",
        projectID: "demo-service",
        tasks: [{ id: "task-1", status: "paused" }],
        title: "待核验需求",
      },
    ],
  });
  await openRequirement(page, "req-unknown");

  // 未知 / 待核验状态原样展示，不显示成成功或失败，也不提供可点击但必然失败的操作
  await expect(page.getByText("paused").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "删除需求" })).toBeDisabled();
  await expect(
    page.getByText(/任务 task-1 处于 paused，需要人工核验现场后才能删除需求/),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "停止任务" })).toBeDisabled();
});

test("切换需求不残留旧的停止与删除状态", async ({ page }) => {
  await installRequirementsBackend(page, {
    deleteMode: { kind: "conflict" },
    requirements: [
      {
        id: "req-a",
        projectID: "demo-service",
        tasks: [{ id: "task-a", status: "failed" }],
        title: "需求 A",
      },
      {
        id: "req-b",
        projectID: "demo-service",
        tasks: [{ id: "task-b", status: "completed" }],
        title: "需求 B",
      },
    ],
  });
  await openRequirement(page, "req-a");
  await panel(page).getByRole("button", { name: "删除需求" }).click();
  await page.getByRole("dialog").last().getByRole("button", { name: "确认删除需求" }).click();
  await expect(page.getByText("操作未成功")).toBeVisible();

  // 切换到另一条需求：上一条需求的错误说明与禁用原因都不得残留
  await page.getByText("需求 B").click();
  await expect(page.getByText("操作未成功")).toHaveCount(0);
  await expect(panel(page).getByRole("button", { name: "删除需求" })).toBeEnabled();
  await expect(page.getByText(/任务 task-a/)).toHaveCount(0);
});

test("窄屏下停止与删除仍然可用且不重叠", async ({ page }) => {
  await page.setViewportSize({ height: 720, width: 480 });
  await installRequirementsBackend(page, {
    requirements: [
      {
        id: "req-narrow",
        projectID: "demo-service",
        tasks: [{ id: "task-1", status: "developing" }],
        title: "窄屏需求",
      },
    ],
  });
  await openRequirement(page, "req-narrow");

  const stopButton = panel(page).getByRole("button", { name: "停止任务" });
  const deleteButton = panel(page).getByRole("button", { name: "删除需求" });
  await expect(stopButton).toBeVisible();
  await expect(deleteButton).toBeVisible();
  const stopBox = await stopButton.boundingBox();
  const deleteBox = await deleteButton.boundingBox();
  expect(stopBox).not.toBeNull();
  expect(deleteBox).not.toBeNull();
  if (!stopBox || !deleteBox) return;
  // 两个按钮不得互相覆盖
  const overlap =
    stopBox.x < deleteBox.x + deleteBox.width &&
    deleteBox.x < stopBox.x + stopBox.width &&
    stopBox.y < deleteBox.y + deleteBox.height &&
    deleteBox.y < stopBox.y + stopBox.height;
  expect(overlap).toBe(false);
});
