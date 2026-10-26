import { defineConfig } from "@playwright/test";

import { TEST_ARTIFACT_DIR } from "./tests/internal/artifacts";
import { resolvePlaywrightWorkers } from "./tests/internal/test-resource-policy";

/**
 * 前端布局测试配置。
 *
 * - 只启动前端开发服务器，不依赖 Go 后端、Docker、SSH、Pi CLI 或模型服务。
 * - 所有产物（截图、追踪、报告）写入本次运行的随机测试工作空间，测试结束后保留。
 * - 资源模型：`webServer` 只启动一个 Vite dev server（Playwright 原生行为，全部 worker
 *   共享同一个 baseURL），CPU 开销来自每个 worker 一个无头浏览器实例，因此并发度由
 *   `workers` 唯一决定。默认值与合法区间定义在 tests/internal/test-resource-policy.ts，
 *   非法取值直接抛错中断，不会静默回退到更高并发。
 */
process.stdout.write(`TEST_ARTIFACT_DIR=${TEST_ARTIFACT_DIR}\n`);

const workers = resolvePlaywrightWorkers();
process.stdout.write(`PLAYWRIGHT_WORKERS=${workers}\n`);

export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: `${TEST_ARTIFACT_DIR}/playwright-output`,
  reporter: [
    ["list"],
    ["html", { open: "never", outputFolder: `${TEST_ARTIFACT_DIR}/playwright-report` }],
  ],
  // 用例级完全并行会在同一 worker 内再并发起多个页面/上下文，与「一个 worker 一个浏览器」
  // 的资源模型冲突，默认关闭；文件级并行仍由 workers 控制。
  fullyParallel: false,
  // 显式声明并发上限：命令行 --workers 仍可覆盖，用于显式诊断。
  workers,
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
  // 单个 dev server，全部 worker 共用；不随 worker 数增加而重复启动。
  webServer: {
    command: "vp dev",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    url: "http://127.0.0.1:26390",
  },
});
