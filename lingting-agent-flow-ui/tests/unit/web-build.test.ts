import { describe, expect, it } from "vitest";

import { printArtifactDir } from "../internal/artifacts";
import { readProjectFile } from "../internal/project";

printArtifactDir();

describe("前端构建与 Go 嵌入", () => {
  it("Vite 构建输出指向 Go 的嵌入包目录", () => {
    const viteConfig = readProjectFile("vite.config.ts");

    expect(viteConfig).toContain("WEBUI_DIST_DIR");
    expect(viteConfig).toContain("internal");
    expect(viteConfig).toContain("webui");
    expect(viteConfig).toContain("outDir: WEBUI_DIST_DIR");
    // 构建会清空嵌入目录，因此必须开启 emptyOutDir 并随后补回占位文件
    expect(viteConfig).toContain("emptyOutDir: true");
  });

  it("构建脚本在产物写入后补回占位文件", () => {
    const script = readProjectFile("scripts/build-webui.mjs");

    expect(script).toContain(`"internal", "webui", "dist"`);
    expect(script).toContain(".gitkeep");
    expect(script).toContain("index.html");
    expect(script).toContain("assets");
  });

  it("开发代理只在开发环境生效并指向 Go 服务", () => {
    const viteConfig = readProjectFile("vite.config.ts");

    expect(viteConfig).toContain("proxy");
    expect(viteConfig).toContain('"/api"');
    expect(viteConfig).toContain("DEV_API_TARGET");
  });

  it("go:embed 指向 dist 目录且源码检出保留占位文件", () => {
    const embed = readProjectFile("../internal/webui/embed.go");
    const placeholder = readProjectFile("../internal/webui/dist/.gitkeep");

    expect(embed).toContain("//go:embed all:dist");
    expect(placeholder).toContain("占位");
  });
});
