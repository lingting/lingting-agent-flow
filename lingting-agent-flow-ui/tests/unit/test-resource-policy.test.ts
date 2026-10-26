import { describe, expect, it } from "vitest";

import { printArtifactDir } from "../internal/artifacts";
import { readProjectFile } from "../internal/project";
import {
  DEFAULT_PLAYWRIGHT_WORKERS,
  DEFAULT_VITEST_MAX_WORKERS,
  MAX_WORKERS,
  PLAYWRIGHT_WORKERS_ENV,
  VITEST_WORKERS_ENV,
  parseWorkerCount,
  resolvePlaywrightWorkers,
  resolveVitestWorkers,
} from "../internal/test-resource-policy";

printArtifactDir();

describe("测试并发策略：默认值", () => {
  it("Playwright 与 Vitest 默认并发都是 4", () => {
    expect(DEFAULT_PLAYWRIGHT_WORKERS).toBe(4);
    expect(DEFAULT_VITEST_MAX_WORKERS).toBe(4);
    expect(resolvePlaywrightWorkers({})).toBe(4);
    expect(resolveVitestWorkers({})).toBe(4);
  });

  it("环境变量可以显式调整并发，上限为 MAX_WORKERS", () => {
    expect(resolvePlaywrightWorkers({ [PLAYWRIGHT_WORKERS_ENV]: "1" })).toBe(1);
    expect(resolveVitestWorkers({ [VITEST_WORKERS_ENV]: "2" })).toBe(2);
    expect(resolvePlaywrightWorkers({ [PLAYWRIGHT_WORKERS_ENV]: String(MAX_WORKERS) })).toBe(
      MAX_WORKERS,
    );
  });
});

describe("测试并发策略：非法输入必须报错", () => {
  const rejected: ReadonlyArray<readonly [string, string]> = [
    ["0", "下界外"],
    [String(MAX_WORKERS + 1), "超过上限"],
    ["-1", "负数"],
    ["1.5", "小数"],
    ["50%", "百分比"],
    ["abc", "非数字"],
  ];

  for (const [raw, label] of rejected) {
    it(`${label}：${JSON.stringify(raw)} 被拒绝`, () => {
      expect(() =>
        parseWorkerCount({ raw, fallback: 1, envName: PLAYWRIGHT_WORKERS_ENV }),
      ).toThrowError(new RegExp(PLAYWRIGHT_WORKERS_ENV));
    });
  }

  it("非法取值不静默回退到默认值或更高并发", () => {
    expect(() =>
      parseWorkerCount({ raw: "99", fallback: 4, envName: VITEST_WORKERS_ENV }),
    ).toThrowError(new RegExp(`必须在 1-${MAX_WORKERS} 之间`));
    expect(() =>
      parseWorkerCount({ raw: "4.0", fallback: 4, envName: VITEST_WORKERS_ENV }),
    ).toThrowError(/十进制整数/);
  });

  it("空值使用默认值，前后空白被忽略", () => {
    expect(parseWorkerCount({ raw: "", fallback: 4, envName: VITEST_WORKERS_ENV })).toBe(4);
    expect(parseWorkerCount({ raw: "  2 ", fallback: 4, envName: VITEST_WORKERS_ENV })).toBe(2);
  });
});

describe("测试并发策略：项目配置确实接入", () => {
  it("Playwright 使用策略默认值，关闭用例级完全并行，保留失败诊断", async () => {
    const config = (await import("../../playwright.config")).default;
    expect(config.workers).toBe(DEFAULT_PLAYWRIGHT_WORKERS);
    expect(config.fullyParallel).toBe(false);
    expect(config.retries).toBe(0);
    // 资源治理不得削弱失败诊断能力
    expect(config.use?.screenshot).toBe("only-on-failure");
    expect(config.use?.trace).toBe("retain-on-failure");
    expect(JSON.stringify(config.reporter)).toContain('"html"');
    // 只有 chromium 单项目，且只有一个 dev server
    expect(config.projects).toHaveLength(1);
    const webServer = Array.isArray(config.webServer) ? config.webServer[0] : config.webServer;
    expect(webServer?.url).toBe("http://127.0.0.1:26390");
  });

  it("Vitest 通过同一策略限制 worker，且不同时设置冲突参数", () => {
    const viteConfig = readProjectFile("vite.config.ts");
    expect(viteConfig).toContain("resolveVitestWorkers()");
    // 只设置 maxWorkers：minWorkers / fileParallelism 会与命令行并发上限互相冲突
    expect(viteConfig).not.toMatch(/^\s*minWorkers\s*:/m);
    expect(viteConfig).not.toMatch(/fileParallelism\s*:/);
    // 不得为降低 CPU 而关闭测试隔离
    expect(viteConfig).not.toMatch(/isolate\s*:\s*false/);
  });

  it("测试产物目录约定未被改动", () => {
    const playwrightConfig = readProjectFile("playwright.config.ts");
    expect(playwrightConfig).toContain("TEST_ARTIFACT_DIR");
    expect(readProjectFile("tests/internal/artifacts.ts")).toContain(
      "lingting-agent-flow-ui-tests",
    );
  });

  it("package.json 提供资源受限入口，且保留原有脚本", () => {
    const manifest = JSON.parse(readProjectFile("package.json")) as {
      scripts: Record<string, string>;
    };
    // 原有脚本语义不变
    expect(manifest.scripts.test).toBe("vp test run");
    expect(manifest.scripts.lint).toBe("vp fmt && vp lint --fix && tsc --noEmit");
    expect(manifest.scripts.build).toBe("node scripts/build-webui.mjs");
    // 新增的安全入口把并发压到 1，并且是顺序执行
    expect(manifest.scripts["test:unit:safe"]).toContain("--maxWorkers 1");
    expect(manifest.scripts["test:e2e:safe"]).toContain("--workers=1");
    expect(manifest.scripts["test:safe"]).toContain("&&");
    expect(manifest.scripts["test:all:safe"]).toContain("&&");
  });
});
