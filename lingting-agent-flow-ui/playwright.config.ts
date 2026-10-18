import { defineConfig } from "@playwright/test";

import { TEST_ARTIFACT_DIR } from "./tests/internal/artifacts";

/**
 * 前端布局测试配置。
 *
 * - 只启动前端开发服务器，不依赖 Go 后端、Docker、SSH、Pi CLI 或模型服务。
 * - 所有产物（截图、追踪、报告）写入本次运行的随机测试工作空间，测试结束后保留。
 */
process.stdout.write(`TEST_ARTIFACT_DIR=${TEST_ARTIFACT_DIR}\n`);

export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: `${TEST_ARTIFACT_DIR}/playwright-output`,
  reporter: [
    ["list"],
    ["html", { open: "never", outputFolder: `${TEST_ARTIFACT_DIR}/playwright-report` }],
  ],
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  use: {
    baseURL: "http://127.0.0.1:26390",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
  ],
  webServer: {
    command: "vp dev",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    url: "http://127.0.0.1:26390",
  },
});
