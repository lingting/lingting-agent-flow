import { describe, expect, it } from "vitest";

import { printArtifactDir } from "../internal/artifacts";
import { installedVersion, readProjectFile } from "../internal/project";

printArtifactDir();

const REQUIRED_UI_LIBRARY_VERSION = "2026.10.0";

describe("工程配置", () => {
  it("lingting-react-ui 固定为 2026.10.0", () => {
    expect(installedVersion("lingting-react-ui")).toBe(REQUIRED_UI_LIBRARY_VERSION);

    const manifest = JSON.parse(readProjectFile("package.json")) as {
      dependencies: Record<string, string>;
    };

    expect(manifest.dependencies["lingting-react-ui"]).toBe(REQUIRED_UI_LIBRARY_VERSION);
  });

  it("构建去重列表完整覆盖 lingting-react-ui 的 peerDependencies", () => {
    const peerDependencies = (
      JSON.parse(readProjectFile("node_modules/lingting-react-ui/package.json")) as {
        peerDependencies: Record<string, string>;
      }
    ).peerDependencies;
    const viteConfig = readProjectFile("vite.config.ts");

    // 去重列表由 peerDependencies 推导，配置里必须保留该推导，避免手工列表与依赖脱节
    expect(viteConfig).toContain("Object.keys(peerDependencies)");
    expect(Object.keys(peerDependencies).length).toBeGreaterThan(0);
  });

  it("脚本使用 pnpm 与 Vite+ 命令", () => {
    const manifest = JSON.parse(readProjectFile("package.json")) as {
      scripts: Record<string, string>;
    };

    expect(manifest.scripts.dev).toBe("vp dev");
    expect(manifest.scripts.build).toBe("node scripts/build-webui.mjs");
    expect(manifest.scripts["build:assets"]).toBe("vp build");
    expect(manifest.scripts.test).toBe("vp test run");
  });
});
