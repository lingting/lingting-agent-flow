import { afterEach, describe, expect, it, vi } from "vitest";

import {
  createProject,
  deleteProject,
  fetchProjects,
  PROJECTS_PATH,
  projectPath,
  updateProject,
  type Environment,
  type Project,
} from "../../src/lib/api";
import {
  describeProjectCaches,
  describeProjectEnvironment,
  findProject,
  joinCacheTypes,
  PROJECT_CACHES_FALLBACK,
  PROJECT_VERSION_FALLBACK,
  projectSelectOptions,
  resolveProjectSelection,
  splitCacheTypes,
  toCreateProjectRequest,
  toProjectFormValues,
  toUpdateProjectRequest,
} from "../../src/pages/projects/projects";
import { projectTaskPrefill } from "../../src/pages/tasks/projectPrefill";
import { printArtifactDir } from "../internal/artifacts";

printArtifactDir();

/** 构造一个 JSON 响应 */
function jsonResponse(body: unknown, init: { status?: number } = {}) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    status: init.status ?? 200,
  });
}

/** 一份项目样本，字段与 Go 端 `httpapi.ProjectResponse` 一致 */
function sampleProject(overrides: Partial<Project> = {}): Project {
  return {
    base_branch: "main",
    caches: ["go-mod"],
    created_at: "2026-10-28T02:00:00Z",
    environment: "go",
    environment_version: "v0001",
    id: "demo-service",
    name: "示例服务",
    repository_url: "ssh://git.example.com/team/repo",
    updated_at: "2026-10-28T02:00:00Z",
    work_dir: "/workspace/project",
    ...overrides,
  };
}

/** 一份语言环境样本，字段与 Go 端 `httpapi.EnvironmentResponse` 一致 */
function sampleEnvironment(overrides: Partial<Environment> = {}): Environment {
  return {
    current_version: "v0002",
    latest_version: "v0002",
    name: "go",
    type: "language",
    versions: [
      {
        build_status: "succeeded",
        created_at: "2026-10-28T02:00:00Z",
        usable: true,
        version: "v0002",
      },
    ],
    ...overrides,
  } as Environment;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("项目接口路径", () => {
  it("集合路径挂在 /api/v1/projects 下", () => {
    expect(PROJECTS_PATH).toBe("/api/v1/projects");
  });

  it("单项目路径按段编码，空标识直接报错", () => {
    expect(projectPath("demo-service")).toBe("/api/v1/projects/demo-service");
    expect(projectPath("a b")).toBe("/api/v1/projects/a%20b");
    expect(() => projectPath("  ")).toThrow("项目标识不能为空");
  });
});

describe("项目接口请求", () => {
  it("列表请求返回真实 records / total", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ records: [sampleProject()], total: 1 }));
    vi.stubGlobal("fetch", fetchMock);
    const { data } = await fetchProjects();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/projects");
    expect(init.method ?? "GET").toBe("GET");
    expect(data.records).toHaveLength(1);
    expect(data.total).toBe(1);
  });

  it("新增请求使用 POST 并发送完整请求体", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleProject(), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    const { data } = await createProject({
      base_branch: "main",
      caches: ["go-mod"],
      environment: "go",
      environment_version: "v0001",
      id: "demo-service",
      name: "示例服务",
      repository_url: "ssh://git.example.com/team/repo",
      work_dir: "/workspace/project",
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(PROJECTS_PATH);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toMatchObject({ id: "demo-service" });
    expect(data.id).toBe("demo-service");
  });

  it("更新请求使用 PUT，标识只出现在路径里", async () => {
    const fetchMock = vi.fn(async () => jsonResponse(sampleProject({ name: "改名" })));
    vi.stubGlobal("fetch", fetchMock);
    await updateProject("demo-service", {
      base_branch: "main",
      environment: "go",
      name: "改名",
      repository_url: "ssh://git.example.com/team/repo",
    });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/projects/demo-service");
    expect(init.method).toBe("PUT");
    const body = JSON.parse(init.body as string) as Record<string, unknown>;
    expect(body).not.toHaveProperty("id");
    expect(body.name).toBe("改名");
  });

  it("删除请求使用 DELETE 且不需要响应体", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await deleteProject("demo-service");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/projects/demo-service");
    expect(init.method).toBe("DELETE");
  });
});

describe("项目展示", () => {
  it("环境版本缺失时显示中性说明，不伪装成具体版本", () => {
    expect(describeProjectEnvironment(sampleProject())).toBe("go · v0001");
    expect(describeProjectEnvironment(sampleProject({ environment_version: undefined }))).toBe(
      `go · ${PROJECT_VERSION_FALLBACK}`,
    );
    expect(describeProjectEnvironment(sampleProject({ environment_version: "  " }))).toBe(
      `go · ${PROJECT_VERSION_FALLBACK}`,
    );
  });

  it("缓存类型为空时显示未声明", () => {
    expect(describeProjectCaches(sampleProject())).toBe("go-mod");
    expect(describeProjectCaches(sampleProject({ caches: [] }))).toBe(PROJECT_CACHES_FALLBACK);
  });

  it("下拉选项使用真实标识作为取值", () => {
    const options = projectSelectOptions([sampleProject({ id: "a", name: "甲" })]);
    expect(options).toEqual([{ label: "甲（a）", value: "a" }]);
  });

  it("按标识查找项目，空标识不匹配任何记录", () => {
    const projects = [sampleProject()];
    expect(findProject(projects, "demo-service")?.id).toBe("demo-service");
    expect(findProject(projects, " DEMO-SERVICE ")).toBeUndefined();
    expect(findProject(projects, "  ")).toBeUndefined();
    expect(findProject(projects, undefined)).toBeUndefined();
  });
});

describe("项目表单转换", () => {
  it("缓存类型支持换行与逗号分隔，并去空去重前的空白", () => {
    expect(splitCacheTypes("go-mod\nnode-npm, maven ")).toEqual(["go-mod", "node-npm", "maven"]);
    expect(splitCacheTypes("   ")).toBeUndefined();
    expect(splitCacheTypes(undefined)).toBeUndefined();
    expect(joinCacheTypes(["go-mod", "node-npm"])).toBe("go-mod\nnode-npm");
    expect(joinCacheTypes([])).toBeUndefined();
  });

  it("新增请求省略空的可选字段", () => {
    const request = toCreateProjectRequest({
      base_branch: " main ",
      caches: "  ",
      environment: " go ",
      environment_version: "  ",
      id: " demo-service ",
      name: " 示例服务 ",
      repository_url: " ssh://git.example.com/team/repo ",
      work_dir: "",
    });
    expect(request).toEqual({
      base_branch: "main",
      environment: "go",
      id: "demo-service",
      name: "示例服务",
      repository_url: "ssh://git.example.com/team/repo",
    });
    expect(request).not.toHaveProperty("caches");
    expect(request).not.toHaveProperty("environment_version");
    expect(request).not.toHaveProperty("work_dir");
  });

  it("更新请求不包含项目标识", () => {
    const request = toUpdateProjectRequest({
      base_branch: "release/1.x",
      caches: "go-mod",
      environment: "go",
      environment_version: "v0002",
      id: "demo-service",
      name: "改名",
      repository_url: "https://git.example.com/team/other.git",
      work_dir: "/workspace/other",
    });
    expect(request).not.toHaveProperty("id");
    expect(request).toMatchObject({
      base_branch: "release/1.x",
      caches: ["go-mod"],
      environment_version: "v0002",
      name: "改名",
      work_dir: "/workspace/other",
    });
  });

  it("项目记录可以回填为表单取值", () => {
    expect(toProjectFormValues(sampleProject())).toEqual({
      base_branch: "main",
      caches: "go-mod",
      environment: "go",
      environment_version: "v0001",
      id: "demo-service",
      name: "示例服务",
      repository_url: "ssh://git.example.com/team/repo",
      work_dir: "/workspace/project",
    });
    expect(toProjectFormValues(sampleProject({ caches: [] })).caches).toBeUndefined();
  });
});

describe("创建任务时的项目选择", () => {
  it("项目列表未加载完成时拒绝提交", () => {
    const result = resolveProjectSelection({ loaded: false, projectID: "a", projects: [] });
    expect(result.ok).toBe(false);
  });

  it("空项目标识被拒绝", () => {
    const result = resolveProjectSelection({
      loaded: true,
      projectID: "  ",
      projects: [sampleProject()],
    });
    expect(result).toEqual({ error: "请选择项目：任务必须关联一个真实存在的项目", ok: false });
  });

  it("不在列表中的项目被拒绝，不猜测默认项目", () => {
    const result = resolveProjectSelection({
      loaded: true,
      projectID: "missing",
      projects: [sampleProject()],
    });
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("missing");
  });

  it("真实项目被接受并返回完整记录", () => {
    const result = resolveProjectSelection({
      loaded: true,
      projectID: " demo-service ",
      projects: [sampleProject()],
    });
    expect(result.ok).toBe(true);
    if (result.ok) expect(result.project.id).toBe("demo-service");
  });
});

describe("项目预填任务表单", () => {
  it("未选择项目时不改动任何字段", () => {
    expect(
      projectTaskPrefill({ environments: [], environmentsLoaded: true, project: undefined }),
    ).toEqual({});
  });

  it("环境未加载时只预填仓库、分支与工作目录", () => {
    const prefill = projectTaskPrefill({
      environments: [],
      environmentsLoaded: false,
      project: sampleProject(),
    });
    expect(prefill).toEqual({
      base_branch: "main",
      repository_url: "ssh://git.example.com/team/repo",
      work_dir: "/workspace/project",
    });
  });

  it("环境存在且版本可用时一并预填环境与版本", () => {
    const prefill = projectTaskPrefill({
      environments: [sampleEnvironment()],
      environmentsLoaded: true,
      project: sampleProject({ environment_version: "v0002" }),
    });
    expect(prefill.environment).toBe("go");
    expect(prefill.environment_version).toBe("v0002");
  });

  it("项目版本不在可用版本中时只预填环境，不猜版本", () => {
    const prefill = projectTaskPrefill({
      environments: [sampleEnvironment()],
      environmentsLoaded: true,
      project: sampleProject({ environment_version: "v0001" }),
    });
    expect(prefill.environment).toBe("go");
    expect(prefill.environment_version).toBeUndefined();
  });

  it("当前节点没有该项目环境时不预填环境", () => {
    const prefill = projectTaskPrefill({
      environments: [sampleEnvironment({ name: "node" })],
      environmentsLoaded: true,
      project: sampleProject(),
    });
    expect(prefill.environment).toBeUndefined();
    expect(prefill.repository_url).toBe("ssh://git.example.com/team/repo");
  });

  it("环境存在但没有可用版本时不预填环境", () => {
    // 预填一个没有可用版本的环境会立刻被提交校验拒绝，等于制造必然失败的配置。
    const prefill = projectTaskPrefill({
      environments: [
        sampleEnvironment({
          versions: [
            {
              build_status: "failed",
              created_at: "2026-10-28T02:00:00Z",
              image_ref: "agentflow-language-go:v0001-abcdefabcdef",
              number: 1,
              usable: false,
              version: "v0001",
            },
          ],
        }),
      ],
      environmentsLoaded: true,
      project: sampleProject({ environment_version: "v0001" }),
    });
    expect(prefill.environment).toBeUndefined();
    expect(prefill.environment_version).toBeUndefined();
    expect(prefill.base_branch).toBe("main");
  });
});
