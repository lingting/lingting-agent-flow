/**
 * 需求工作台浏览器测试的交互辅助。
 *
 * 只封装"用户怎么点"这一层：选择项目 / 执行节点、创建会话、提交需求与切换方案版本。
 * 断言仍留在各 spec 中，避免把业务事实藏进工具函数。
 */
import { expect, type Page } from "@playwright/test";

/** 打开下拉并选择带指定文本的选项 */
export async function selectOption(page: Page, ariaLabel: string, text: string) {
  await page.getByLabel(ariaLabel).click();
  const dropdown = page.locator(".ant-select-dropdown:visible");
  const option = dropdown.locator(".ant-select-item-option").filter({ hasText: text }).first();
  await option.click();
}

/** 选择项目 */
export async function selectProject(page: Page, projectName: string) {
  await selectOption(page, "选择项目", projectName);
}

/** 选择执行节点 */
export async function selectHost(page: Page, hostName: string) {
  await selectOption(page, "选择执行节点", hostName);
}

/** 新建规划会话，并返回会话标识文本 */
export async function createSession(page: Page) {
  await page.getByRole("button", { name: "新建会话" }).click();
}

/** 提交一条需求 */
export async function submitRequirement(page: Page, content: string) {
  await page.getByLabel("需求输入").fill(content);
  await page.getByRole("button", { name: "提交需求并分析" }).click();
}

/** 切换方案版本 */
export async function selectRevision(page: Page, revisionNumber: number) {
  await page
    .locator("label.ant-radio-button-wrapper")
    .filter({ hasText: `第 ${revisionNumber} 版` })
    .click();
}

/** 打开工作台并等待项目列表就绪 */
export async function openWorkbench(page: Page) {
  await page.goto("/planning");
  await expect(page.getByLabel("选择项目")).toBeVisible();
}
