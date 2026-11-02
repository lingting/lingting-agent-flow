import { describe, expect, it } from "vitest";

import { printArtifactDir } from "../internal/artifacts";
import { readProjectFile } from "../internal/project";

printArtifactDir();

/**
 * 一级路由，与导航配置必须完全一致。
 *
 * 环境管理内嵌化之后移除了独立的环境入口：`/environments` 不再是一级路由，
 * 环境相关的全部操作都在设置页内完成。
 */
const PLANNED_PATHS = ["/overview", "/tasks", "/projects", "/machines", "/settings"] as const;

describe("导航与路由元数据", () => {
  it("导航配置集中定义全部一级路径", () => {
    const navigation = readProjectFile("src/app/navigation.tsx");

    for (const path of PLANNED_PATHS) {
      expect(navigation).toContain(`path: "${path.slice(1)}"`);
    }
  });

  it("独立环境导航入口与页面都已移除", () => {
    const navigation = readProjectFile("src/app/navigation.tsx");

    expect(navigation).not.toContain('path: "environments"');
    expect(navigation).not.toContain("EnvironmentsPage");
    // 页面目录本身也必须移除，避免留下第二套环境功能入口。
    expect(() => readProjectFile("src/pages/environments/EnvironmentsPage.tsx")).toThrow();
  });

  it("设置页承载环境管理", () => {
    const settings = readProjectFile("src/pages/settings/SettingsPage.tsx");

    expect(settings).toContain("EnvironmentManagementPanel");
    expect(settings).toContain("环境管理");
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
