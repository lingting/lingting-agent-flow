import { defineConfig, type ReporterDescription } from "@playwright/test";

import { TEST_ARTIFACT_DIR } from "./tests/internal/artifacts";

/**
 * 前端端到端测试配置（V3 R6 起由统一执行器驱动）。
 *
 * - 不再启动 dev server：`webServer` 已移除，dev server 由执行器的 `frontend-dev-server`
 *   资源提供者启动，地址通过 `AGENTFLOW_UI_BASE_URL` 注入；独立运行退回本地默认端口，
 *   此时需要自己先启动 dev server。
 * - 并发固定为 1：文件之间的并发只由执行器调度器决定，这里不再读取任何 worker 环境变量。
 * - 产物（截图、trace、报告）写入注入的用例产物目录，见 tests/internal/artifacts.ts。
 * - `AGENTFLOW_UI_JSON_REPORT` 给出时追加 JSON reporter，供执行器读取机器结论；两种结论
 *   冲突时一律按失败处理。
 */
process.stdout.write(`TEST_ARTIFACT_DIR=${TEST_ARTIFACT_DIR}\n`);

// 执行器注入的 dev server 地址；独立运行退回本地默认端口，此时需要自己先启动 dev server。
const baseURL = (process.env.AGENTFLOW_UI_BASE_URL ?? "http://127.0.0.1:26381").trim();

const jsonReport = (process.env.AGENTFLOW_UI_JSON_REPORT ?? "").trim();

const reporters: ReporterDescription[] = [
  ["list"],
  ["html", { open: "never", outputFolder: `${TEST_ARTIFACT_DIR}/playwright-report` }],
];
if (jsonReport !== "") {
  reporters.push(["json", { outputFile: jsonReport }]);
}

export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: `${TEST_ARTIFACT_DIR}/playwright-output`,
  reporter: reporters,
  // 用例级完全并行会在同一 worker 内再并发起多个页面/上下文，与「一个 worker 一个浏览器」
  // 的资源模型冲突，默认关闭；文件间的并发由执行器调度器决定。
  fullyParallel: false,
  // 单次调用固定一个 worker：执行器一条 Case 一次调用（R6 D4）。
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  use: {
    baseURL,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
  ],
});
