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

  it("入口把导航配置与重定向目标交给 LRI 布局", () => {
    const main = readProjectFile("src/main.tsx");

    expect(main).toContain("menuRoutes={navigationRoutes}");
    expect(main).toContain("rootRedirectTo={ROOT_REDIRECT_TO}");
    expect(main).toContain("notFoundComponent={NotFoundPage}");
  });
});
