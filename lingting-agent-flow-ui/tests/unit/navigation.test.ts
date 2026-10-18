import { describe, expect, it } from "vitest";

import { printArtifactDir } from "../internal/artifacts";
import { readProjectFile } from "../internal/project";

printArtifactDir();

/** 规划中的六个一级路由，与导航配置必须完全一致 */
const PLANNED_PATHS = [
  "/overview",
  "/tasks",
  "/projects",
  "/machines",
  "/environments",
  "/settings",
] as const;

describe("导航与路由元数据", () => {
  it("导航配置集中定义六个规划路径", () => {
    const navigation = readProjectFile("src/app/navigation.tsx");

    for (const path of PLANNED_PATHS) {
      expect(navigation).toContain(`path: "${path.slice(1)}"`);
    }
  });

  it("根路径由布局重定向到概览页", () => {
    const navigation = readProjectFile("src/app/navigation.tsx");

    expect(navigation).toContain('export const ROOT_REDIRECT_TO = "/overview";');
  });

  it("路由定义不注册根路径，避免与布局的根重定向冲突", () => {
    const navigation = readProjectFile("src/app/navigation.tsx");

    expect(navigation).not.toContain('path: "/"');
    expect(navigation).not.toContain('path: ""');
  });

  it("应用外壳把导航配置、重定向目标与连接状态交给 LRI 布局", () => {
    const shell = readProjectFile("src/app/AppShell.tsx");

    expect(shell).toContain("menuRoutes={navigationRoutes}");
    expect(shell).toContain("rootRedirectTo={ROOT_REDIRECT_TO}");
    expect(shell).toContain("notFoundComponent={NotFoundPage}");
    expect(shell).toContain("ApiConnectionProvider");
    expect(shell).toContain("headerRightItems");
  });

  it("入口只挂载应用外壳，不在入口重复定义路由", () => {
    const main = readProjectFile("src/main.tsx");

    expect(main).toContain("<AppShell />");
    expect(main).not.toContain("menuRoutes");
  });
});
