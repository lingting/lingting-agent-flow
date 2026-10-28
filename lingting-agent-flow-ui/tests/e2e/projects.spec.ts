import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 项目管理页的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库或远程执行环境；
 * 返回体形状与 Go 端 `httpapi.ProjectResponse` / `ProjectListResponse` 一致。
 *
 * 用例关注业务事实：项目列表是否来自真实接口、新增/修改/删除是否走正确方法与
 * 正确请求体、查询失败是否被伪装成空列表、删除是否明确说明"只标记下线"。
 */

/** 一份项目样本，字段与 Go 端 `httpapi.ProjectResponse` 一致 */
function sampleProject(overrides: Record<string, unknown> = {}) {
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

/** 健康检查响应，使页面外壳处于已连接状态 */
async function stubHealth(page: Page) {
  await page.route("**/api/v1/health", (route) =>
    route.fulfill({
      body: JSON.stringify({
        checks: [
          { name: "http_api", status: "pass" },
          { name: "database", status: "pass" },
        ],
        checked_at: "2025-01-01T00:00:00Z",
        status: "ok",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截项目列表接口并记录调用 */
async function stubProjectList(page: Page, handler: () => { body: unknown; status?: number }) {
  const calls: string[] = [];
  await page.route("**/api/v1/projects", async (route: Route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    calls.push(route.request().url());
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return calls;
}

/** 拦截项目新增接口，记录请求体 */
async function stubCreateProject(page: Page, handler: () => { body: unknown; status?: number }) {
  const created: Record<string, unknown>[] = [];
  await page.route("**/api/v1/projects", async (route: Route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    created.push(JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>);
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 201,
    });
  });
  return created;
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("项目列表", () => {
  test("展示真实项目字段，环境版本缺失时给出中性说明", async ({ page }) => {
    await stubProjectList(page, () => ({
      body: {
        records: [
          sampleProject(),
          sampleProject({
            base_branch: "develop",
            caches: [],
            environment: "node",
            environment_version: undefined,
            id: "web-app",
            name: "前端应用",
          }),
        ],
        total: 2,
      },
    }));

    await page.goto("/projects");

    await expect(page.getByText("示例服务")).toBeVisible();
    await expect(page.getByText("demo-service")).toBeVisible();
    await expect(page.getByText("前端应用")).toBeVisible();
    // 环境版本缺失时显示"当前可用版本"，不伪装成具体版本
    await expect(page.getByText("node · 当前可用版本")).toBeVisible();
    // 缓存类型为空时显示中性说明（按行定位，避免与页面说明文案里的同一措辞混淆）
    await expect(page.getByRole("row", { name: /前端应用/ }).getByText("未声明")).toBeVisible();
  });

  test("空列表显示可执行的空状态而不是加载失败", async ({ page }) => {
    await stubProjectList(page, () => ({ body: { records: [], total: 0 } }));

    await page.goto("/projects");

    await expect(page.getByText(/尚未登记项目/).first()).toBeVisible();
    await expect(page.getByText("加载项目失败")).toHaveCount(0);
  });

  test("查询失败不被伪装成空列表", async ({ page }) => {
    await stubProjectList(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));

    await page.goto("/projects");

    await expect(page.getByText("加载项目失败")).toBeVisible();
    await expect(page.getByText("服务内部错误")).toBeVisible();
    await expect(page.getByText(/尚未登记项目/)).toHaveCount(0);
  });
});

test.describe("项目新增", () => {
  test("新增项目使用 POST 与真实字段，成功后重新拉取列表", async ({ page }) => {
    const listCalls = await stubProjectList(page, () => ({
      body: { records: [], total: 0 },
    }));
    const created = await stubCreateProject(page, () => ({ body: sampleProject() }));

    await page.goto("/projects");
    await expect.poll(() => listCalls.length).toBeGreaterThan(0);
    const before = listCalls.length;

    await page.getByRole("button", { name: "新增项目" }).click();
    await page.getByLabel("项目标识").fill("demo-service");
    await page.getByLabel("项目名称").fill("示例服务");
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/team/repo");
    await page.getByLabel("基础分支").fill("main");
    await page.getByLabel("语言环境").fill("go");
    await page.getByLabel("环境版本（可选）").fill("v0001");
    await page.getByLabel("缓存类型（可选）").fill("go-mod\nnode-npm");
    await page.getByLabel("容器内工作目录（可选）").fill("/workspace/project");
    await page.getByRole("button", { name: "创 建" }).click();

    await expect.poll(() => created.length).toBe(1);
    expect(created[0]).toMatchObject({
      base_branch: "main",
      caches: ["go-mod", "node-npm"],
      environment: "go",
      environment_version: "v0001",
      id: "demo-service",
      name: "示例服务",
      repository_url: "ssh://git.example.com/team/repo",
      work_dir: "/workspace/project",
    });
    // 创建后刷新真实列表
    await expect.poll(() => listCalls.length).toBeGreaterThan(before);
  });

  test("标识冲突时展示服务端真实错误并保持弹窗打开", async ({ page }) => {
    await stubProjectList(page, () => ({ body: { records: [sampleProject()], total: 1 } }));
    await stubCreateProject(page, () => ({
      body: {
        error: {
          code: "state_conflict",
          message: "app: 项目状态不允许该操作: 项目标识 demo-service 已存在",
        },
      },
      status: 409,
    }));

    await page.goto("/projects");
    await page.getByRole("button", { name: "新增项目" }).click();
    await page.getByLabel("项目标识").fill("demo-service");
    await page.getByLabel("项目名称").fill("示例服务");
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/team/repo");
    await page.getByLabel("基础分支").fill("main");
    await page.getByLabel("语言环境").fill("go");
    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText(/项目标识 demo-service 已存在/)).toBeVisible();
    await expect(page.getByRole("button", { name: "创 建" })).toBeVisible();
  });
});

test.describe("项目修改与删除", () => {
  test("修改使用 PUT，标识不可编辑且不出现在请求体里", async ({ page }) => {
    await stubProjectList(page, () => ({ body: { records: [sampleProject()], total: 1 } }));
    const updated: Record<string, unknown>[] = [];
    await page.route("**/api/v1/projects/demo-service", async (route: Route) => {
      if (route.request().method() !== "PUT") {
        await route.fallback();
        return;
      }
      updated.push(JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>);
      await route.fulfill({
        body: JSON.stringify(sampleProject({ name: "改名后的项目" })),
        contentType: "application/json",
        status: 200,
      });
    });

    await page.goto("/projects");
    // 行内操作是文字按钮，按精确文本定位，避免与说明文案里的同名词混淆。
    await page.getByText("修改", { exact: true }).click();
    await expect(page.getByLabel("项目标识")).toBeDisabled();
    await page.getByLabel("项目名称").fill("改名后的项目");
    await page.getByRole("button", { name: "保 存" }).click();

    await expect.poll(() => updated.length).toBe(1);
    expect(updated[0]).not.toHaveProperty("id");
    expect(updated[0]).toMatchObject({ name: "改名后的项目", environment: "go" });
  });

  test("删除需要显式确认并说明只标记下线", async ({ page }) => {
    await stubProjectList(page, () => ({ body: { records: [sampleProject()], total: 1 } }));
    const deleted: string[] = [];
    await page.route("**/api/v1/projects/demo-service", async (route: Route) => {
      if (route.request().method() !== "DELETE") {
        await route.fallback();
        return;
      }
      deleted.push(route.request().url());
      await route.fulfill({ body: "", status: 204 });
    });

    await page.goto("/projects");
    await page.getByText("删除", { exact: true }).click();
    // 确认框必须写明后果：只标记下线，历史记录仍保留该标识
    await expect(page.getByText(/删除只标记项目下线/)).toBeVisible();
    await page.getByRole("button", { name: "确认删除" }).click();

    await expect.poll(() => deleted.length).toBe(1);
  });
});
