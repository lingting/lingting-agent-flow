import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 创建任务表单的执行节点与角色选择器浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型；
 * 返回体形状与 Go 端 DTO 一致（`HostListResponse`、`RoleListResponse`、
 * `EnvironmentListResponse`、`TaskResponse`）。
 *
 * 用例关注的是业务断言：页面是否走了正确接口、是否只允许真实可用的节点与角色、
 * 切换节点是否清除旧环境，以及提交请求是否使用选择器返回的真实标识。
 */

/** 一份执行节点样本，字段与 Go 端 `httpapi.HostResponse` 一致 */
function sampleHost(overrides: Record<string, unknown> = {}) {
  return {
    auth_method: "private_key",
    created_at: "2026-10-26T08:00:00Z",
    enabled: true,
    host: "10.0.0.11",
    id: "host-1",
    is_default: true,
    last_connect_status: "ok",
    name: "构建机",
    port: 22,
    private_key_path: "/home/dev/.ssh/id_ed25519",
    updated_at: "2026-10-26T08:00:00Z",
    usable_for_remote_task: true,
    username: "dev",
    validation_generation: 1,
    validation_status: "normal",
    workspace_root: "/home/dev/agentflow",
    ...overrides,
  };
}

/** 一份角色样本，字段与 Go 端 `httpapi.RoleResponse` 一致 */
function sampleRole(overrides: Record<string, unknown> = {}) {
  return {
    enabled: true,
    id: "planner-1",
    model: "group/free",
    name: "默认规划角色",
    runner: "pi",
    type: "planner",
    ...overrides,
  };
}

/** 一份环境版本样本 */
function sampleVersion(overrides: Record<string, unknown> = {}) {
  return {
    build_status: "succeeded",
    created_at: "2026-10-26T08:00:00Z",
    image_ref: "agentflow-language-go:v0001-abcdefabcdef",
    number: 1,
    usable: true,
    version: "v0001",
    ...overrides,
  };
}

/** 一份环境样本 */
function sampleEnvironment(overrides: Record<string, unknown> = {}) {
  return {
    current_version: "v0001",
    latest_version: "v0001",
    name: "go",
    type: "language",
    versions: [sampleVersion()],
    ...overrides,
  };
}

/** 一份任务响应样本 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    created_at: "2025-01-01T00:00:00Z",
    description: "描述内容",
    id: "task-new",
    name: "新建任务",
    project_id: "",
    status: "queued",
    status_finished: false,
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
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

/** 记录并应答任务列表请求 */
async function stubTaskList(page: Page) {
  const requests: URL[] = [];
  await page.route("**/api/v1/tasks?*", async (route: Route) => {
    requests.push(new URL(route.request().url()));
    await route.fulfill({
      body: JSON.stringify({ current: 1, records: [], size: 20, total: 0 }),
      contentType: "application/json",
      status: 200,
    });
  });
  return requests;
}

/** 拦截执行节点列表接口，记录调用次数 */
async function stubHosts(page: Page, handler: () => { body: unknown; status?: number }) {
  const calls: string[] = [];
  await page.route("**/api/v1/hosts", async (route: Route) => {
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

/** 拦截角色列表接口，记录调用次数 */
async function stubRoles(page: Page, handler: () => { body: unknown; status?: number }) {
  const calls: string[] = [];
  await page.route("**/api/v1/roles", async (route: Route) => {
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

/** 拦截环境查询接口，记录 host_id 参数 */
async function stubEnvironments(
  page: Page,
  handler: (hostID: string) => { body: unknown; status?: number },
) {
  const hosts: string[] = [];
  await page.route("**/api/v1/environments?*", async (route: Route) => {
    const hostID = new URL(route.request().url()).searchParams.get("host_id") ?? "";
    hosts.push(hostID);
    const result = handler(hostID);
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return hosts;
}

/** 拦截创建任务接口，记录请求体 */
async function stubCreateTask(page: Page, handler: () => { body: unknown; status?: number }) {
  const created: Record<string, unknown>[] = [];
  await page.route("**/api/v1/tasks", async (route: Route) => {
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

/** 打开创建任务弹窗 */
async function openCreateModal(page: Page) {
  await page.goto("/tasks");
  await page.getByRole("button", { name: "创建任务" }).click();
}

/** 选择执行节点 */
async function selectHost(page: Page, label: RegExp | string) {
  await page.getByLabel("执行节点").click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(label).click();
}

/** 选择角色（按 aria-label 定位下拉） */
async function selectRole(page: Page, ariaLabel: string, label: RegExp | string) {
  await page.getByLabel(ariaLabel).click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(label).click();
}

/** 打开环境下拉并选择指定环境 */
async function selectEnvironment(page: Page, name: string) {
  await page.getByLabel("语言环境").click();
  await page
    .locator(".ant-select-dropdown:visible")
    .getByTitle(new RegExp(`^${name}`))
    .click();
}

/** 打开版本下拉并选择指定版本 */
async function selectVersion(page: Page, version: string) {
  await page.getByLabel("环境版本").click();
  await page
    .locator(".ant-select-dropdown:visible")
    .getByTitle(new RegExp(`^${version}`))
    .click();
}

/** 填写除节点、环境与角色之外的必填字段 */
async function fillCommonFields(page: Page) {
  await page.getByLabel("任务名称").fill("新建任务");
  await page.getByLabel("任务描述").fill("描述内容");
  await page.getByLabel("仓库地址").fill("ssh://git.example.com/team/repo");
  await page.getByRole("textbox", { name: "* 基础分支" }).fill("main");
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("创建任务的节点与角色选择", () => {
  test("节点与角色从真实接口加载，用户可以完成选择并提交真实标识", async ({ page }) => {
    const hostCalls = await stubHosts(page, () => ({
      body: { records: [sampleHost()], total: 1 },
    }));
    const roleCalls = await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-1", name: "规划角色" }),
          sampleRole({ id: "dev-1", name: "开发角色", type: "developer" }),
          sampleRole({ id: "val-1", name: "验证角色", type: "validator" }),
        ],
        total: 3,
      },
    }));
    const envHosts = await stubEnvironments(page, () => ({
      body: { records: [sampleEnvironment()], total: 1 },
    }));
    await stubTaskList(page);
    const created = await stubCreateTask(page, () => ({ body: sampleTask() }));

    await openCreateModal(page);
    await expect.poll(() => hostCalls.length).toBeGreaterThan(0);
    await expect.poll(() => roleCalls.length).toBeGreaterThan(0);

    // 节点展示名称与地址，而不是要求用户手抄 ID
    await selectHost(page, /构建机/);
    await expect.poll(() => envHosts.at(-1)).toBe("host-1");

    await selectEnvironment(page, "go");
    await selectVersion(page, "v0001");
    await selectRole(page, "规划角色", /规划角色/);
    await selectRole(page, "开发角色", /开发角色/);
    await selectRole(page, "验证角色", /验证角色/);
    await fillCommonFields(page);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect.poll(() => created.length).toBe(1);
    expect(created[0]?.host_id).toBe("host-1");
    expect(created[0]?.planner_role_id).toBe("planner-1");
    expect(created[0]?.developer_role_id).toBe("dev-1");
    expect(created[0]?.validator_role_id).toBe("val-1");
    expect(created[0]?.environment).toBe("go");
    expect(created[0]?.environment_version).toBe("v0001");
  });

  test("禁用角色不能选中", async ({ page }) => {
    await stubHosts(page, () => ({ body: { records: [sampleHost()], total: 1 } }));
    await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-on", name: "规划启用" }),
          sampleRole({ enabled: false, id: "planner-off", name: "规划禁用" }),
          sampleRole({ id: "dev-on", name: "开发启用", type: "developer" }),
        ],
        total: 3,
      },
    }));
    await stubEnvironments(page, () => ({ body: { records: [], total: 0 } }));
    await stubTaskList(page);

    await openCreateModal(page);
    await page.getByLabel("规划角色").click();
    const dropdown = page.locator(".ant-select-dropdown:visible");
    const disabledOption = dropdown.getByTitle(/规划禁用/);
    await expect(disabledOption).toBeVisible();
    await expect(disabledOption).toHaveClass(/ant-select-item-option-disabled/);
    // 禁用选项点击不会写入选择值，选择框仍是占位提示
    await disabledOption.click({ force: true });
    await expect(page.getByText("选择规划角色", { exact: true })).toBeVisible();
  });

  test("不可用节点不能用于提交", async ({ page }) => {
    await stubHosts(page, () => ({
      body: {
        records: [
          sampleHost({
            id: "host-bad",
            name: "未通过预检",
            usable_for_remote_task: false,
            validation_message: "环境预检未通过",
            validation_status: "abnormal",
          }),
        ],
        total: 1,
      },
    }));
    await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-1", name: "规划角色" }),
          sampleRole({ id: "dev-1", name: "开发角色", type: "developer" }),
        ],
        total: 2,
      },
    }));
    await stubEnvironments(page, () => ({ body: { records: [], total: 0 } }));
    await stubTaskList(page);
    const created = await stubCreateTask(page, () => ({ body: sampleTask() }));

    await openCreateModal(page);
    // 唯一节点未通过预检：下拉整体禁用，用户无法选出不可用节点
    const hostSelect = page.getByLabel("执行节点");
    await expect(hostSelect).toBeDisabled();
    // 未选中节点时不会发起环境查询，页面明确提示尚未选择节点
    await expect(page.getByText("尚未选择执行节点")).toBeVisible();

    await fillCommonFields(page);
    await page.getByRole("button", { name: "创 建" }).click();

    // 没有可用节点时提交被阻止，且没有发出创建请求
    await expect(page.getByText("请选择执行节点")).toBeVisible();
    expect(created).toHaveLength(0);
  });

  test("切换执行节点清除已有环境与版本选择，并重新查询新节点", async ({ page }) => {
    await stubHosts(page, () => ({
      body: {
        records: [
          sampleHost(),
          sampleHost({ host: "10.0.0.12", id: "host-2", is_default: false, name: "第二台" }),
        ],
        total: 2,
      },
    }));
    await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-1", name: "规划角色" }),
          sampleRole({ id: "dev-1", name: "开发角色", type: "developer" }),
        ],
        total: 2,
      },
    }));
    const envHosts = await stubEnvironments(page, (hostID) => ({
      body: {
        records: [sampleEnvironment({ name: hostID === "host-2" ? "node" : "go" })],
        total: 1,
      },
    }));
    await stubTaskList(page);

    await openCreateModal(page);
    await selectHost(page, /构建机/);
    await expect.poll(() => envHosts.at(-1)).toBe("host-1");
    await selectEnvironment(page, "go");
    await selectVersion(page, "v0001");
    await expect(page.getByLabel("环境版本")).toBeVisible();

    // 切换节点：环境与版本被清空，并针对新节点重新查询
    await selectHost(page, /第二台/);
    await expect.poll(() => envHosts.at(-1)).toBe("host-2");
    await expect(page.getByText("选择真实环境")).toBeVisible();
    await expect(page.getByText("请先选择环境")).toBeVisible();
  });

  test("节点接口返回错误时显示可理解的错误状态而不是空列表", async ({ page }) => {
    await stubHosts(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));
    await stubRoles(page, () => ({ body: { records: [], total: 0 } }));
    await stubEnvironments(page, () => ({ body: { records: [], total: 0 } }));
    await stubTaskList(page);

    await openCreateModal(page);

    await expect(page.getByText("执行节点查询失败")).toBeVisible();
    await expect(page.getByText("服务内部错误")).toBeVisible();
    await expect(page.getByText("没有可用的执行节点")).toHaveCount(0);
  });

  test("角色接口返回错误时显示可理解的错误状态而不是没有角色", async ({ page }) => {
    await stubHosts(page, () => ({ body: { records: [sampleHost()], total: 1 } }));
    await stubRoles(page, () => ({
      body: { error: { code: "internal_error", message: "角色服务未初始化" } },
      status: 503,
    }));
    await stubEnvironments(page, () => ({ body: { records: [], total: 0 } }));
    await stubTaskList(page);

    await openCreateModal(page);

    await expect(page.getByText("角色查询失败")).toBeVisible();
    await expect(page.getByText("角色服务未初始化")).toBeVisible();
    await expect(page.getByText("没有可用的角色")).toHaveCount(0);
  });

  test("没有可用节点时用户不能创建任务", async ({ page }) => {
    await stubHosts(page, () => ({
      body: {
        records: [
          sampleHost({
            enabled: false,
            id: "host-off",
            name: "停用节点",
            usable_for_remote_task: false,
          }),
        ],
        total: 1,
      },
    }));
    await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-1", name: "规划角色" }),
          sampleRole({ id: "dev-1", name: "开发角色", type: "developer" }),
        ],
        total: 2,
      },
    }));
    await stubEnvironments(page, () => ({ body: { records: [], total: 0 } }));
    await stubTaskList(page);
    const created = await stubCreateTask(page, () => ({ body: sampleTask() }));

    await openCreateModal(page);
    await fillCommonFields(page);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText("请选择执行节点")).toBeVisible();
    expect(created).toHaveLength(0);
  });

  test("缺少启用的必需角色时不能创建任务", async ({ page }) => {
    await stubHosts(page, () => ({ body: { records: [sampleHost()], total: 1 } }));
    await stubRoles(page, () => ({
      body: {
        records: [sampleRole({ enabled: false, id: "planner-off", name: "规划禁用" })],
        total: 1,
      },
    }));
    await stubEnvironments(page, () => ({ body: { records: [sampleEnvironment()], total: 1 } }));
    await stubTaskList(page);
    const created = await stubCreateTask(page, () => ({ body: sampleTask() }));

    await openCreateModal(page);
    await selectHost(page, /构建机/);
    await selectEnvironment(page, "go");
    await selectVersion(page, "v0001");
    await fillCommonFields(page);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText("缺少启用的规划角色")).toBeVisible();
    await expect(page.getByText("缺少启用的开发角色")).toBeVisible();
    expect(created).toHaveLength(0);
  });

  test("创建请求失败时保留真实错误且不提示创建成功", async ({ page }) => {
    await stubHosts(page, () => ({ body: { records: [sampleHost()], total: 1 } }));
    await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-1", name: "规划角色" }),
          sampleRole({ id: "dev-1", name: "开发角色", type: "developer" }),
        ],
        total: 2,
      },
    }));
    await stubEnvironments(page, () => ({ body: { records: [sampleEnvironment()], total: 1 } }));
    await stubTaskList(page);
    await stubCreateTask(page, () => ({
      body: { error: { code: "invalid_request", message: "执行节点已被禁用" } },
      status: 400,
    }));

    await openCreateModal(page);
    await selectHost(page, /构建机/);
    await selectEnvironment(page, "go");
    await selectVersion(page, "v0001");
    await selectRole(page, "规划角色", /规划角色/);
    await selectRole(page, "开发角色", /开发角色/);
    await fillCommonFields(page);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText("执行节点已被禁用")).toBeVisible();
    await expect(page.getByText("新建任务")).toHaveCount(0);
    // 弹窗保持打开，用户可以修正后重试
    await expect(page.getByRole("button", { name: "创 建" })).toBeVisible();
  });

  test("创建成功后只刷新任务列表，不自动启动任务", async ({ page }) => {
    await stubHosts(page, () => ({ body: { records: [sampleHost()], total: 1 } }));
    await stubRoles(page, () => ({
      body: {
        records: [
          sampleRole({ id: "planner-1", name: "规划角色" }),
          sampleRole({ id: "dev-1", name: "开发角色", type: "developer" }),
        ],
        total: 2,
      },
    }));
    await stubEnvironments(page, () => ({ body: { records: [sampleEnvironment()], total: 1 } }));
    const listCalls = await stubTaskList(page);
    const created = await stubCreateTask(page, () => ({ body: sampleTask() }));
    const launchCalls: string[] = [];
    await page.route("**/api/v1/tasks/*/start", async (route: Route) => {
      launchCalls.push(route.request().url());
      await route.fulfill({ body: "{}", contentType: "application/json", status: 202 });
    });

    await openCreateModal(page);
    await selectHost(page, /构建机/);
    await selectEnvironment(page, "go");
    await selectVersion(page, "v0001");
    await selectRole(page, "规划角色", /规划角色/);
    await selectRole(page, "开发角色", /开发角色/);
    await fillCommonFields(page);
    const before = listCalls.length;
    await page.getByRole("button", { name: "创 建" }).click();

    await expect.poll(() => created.length).toBe(1);
    // 创建后重新拉取列表
    await expect.poll(() => listCalls.length).toBeGreaterThan(before);
    // 但不自动启动任务
    expect(launchCalls).toHaveLength(0);
  });
});
