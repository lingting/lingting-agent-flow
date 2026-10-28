import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 创建任务时项目配置继承与显式覆盖的浏览器测试。
 *
 * 背景：项目（`GET /api/v1/projects`）承载仓库地址、基础分支、容器内工作目录、语言环境
 * 与版本，创建任务表单会按项目预填。这里断言的不是"有没有预填"，而是预填与用户显式
 * 设置的**优先级**是否稳定：
 *   - 用户显式设置过的字段不会被切换项目悄悄覆盖；
 *   - 项目锁定的环境在当前节点不可用时不会被伪造；
 *   - 环境被换掉时，旧环境的版本不能残留在表单里；
 *   - 字段取值来源（项目默认值 / 用户显式设置）必须可辨识，且可恢复为项目默认值。
 *
 * 全部请求在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型；响应体形状
 * 与 Go 端 DTO 一致（`ProjectListResponse`、`HostListResponse`、`RoleListResponse`、
 * `EnvironmentListResponse`、`TaskResponse`）。
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

/** 一份语言环境版本样本 */
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

/** 一份语言环境样本 */
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

/** 项目 A：Go 服务，锁定 go 的 v0001 */
function projectA(overrides: Record<string, unknown> = {}) {
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

/** 项目 B：Node 服务，锁定的版本在该节点不可用（用于验证"不猜版本"） */
function projectB(overrides: Record<string, unknown> = {}) {
  return {
    base_branch: "release/1.x",
    caches: ["node-npm"],
    created_at: "2026-10-28T03:00:00Z",
    environment: "node",
    environment_version: "v0009",
    id: "other-service",
    name: "其他服务",
    repository_url: "ssh://git.example.com/team/other.git",
    updated_at: "2026-10-28T03:00:00Z",
    work_dir: "/workspace/other",
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
    project_id: "demo-service",
    status: "queued",
    status_finished: false,
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

/** 应答健康检查，使页面外壳处于已连接状态 */
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

/** 应答任务列表 */
async function stubTaskList(page: Page) {
  await page.route("**/api/v1/tasks?*", (route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [], size: 20, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截项目列表接口 */
async function stubProjects(page: Page, projects: Record<string, unknown>[]) {
  await page.route("**/api/v1/projects", (route) =>
    route.fulfill({
      body: JSON.stringify({ records: projects, total: projects.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截执行节点列表接口 */
async function stubHosts(page: Page, hosts: Record<string, unknown>[]) {
  await page.route("**/api/v1/hosts", (route) =>
    route.fulfill({
      body: JSON.stringify({ records: hosts, total: hosts.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截角色列表接口 */
async function stubRoles(page: Page) {
  const records = [
    {
      enabled: true,
      id: "planner-1",
      model: "group/free",
      name: "规划角色",
      runner: "pi",
      type: "planner",
    },
    {
      enabled: true,
      id: "dev-1",
      model: "group/free",
      name: "开发角色",
      runner: "pi",
      type: "developer",
    },
  ];
  await page.route("**/api/v1/roles", (route) =>
    route.fulfill({
      body: JSON.stringify({ records, total: records.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截环境查询接口 */
async function stubEnvironments(page: Page, handler: (hostID: string) => unknown[]) {
  const calls: string[] = [];
  await page.route("**/api/v1/environments?*", async (route: Route) => {
    const hostID = new URL(route.request().url()).searchParams.get("host_id") ?? "";
    calls.push(hostID);
    const records = handler(hostID);
    await route.fulfill({
      body: JSON.stringify({ records, total: records.length }),
      contentType: "application/json",
      status: 200,
    });
  });
  return calls;
}

/** 拦截创建任务接口，记录请求体 */
async function stubCreateTask(page: Page) {
  const created: Record<string, unknown>[] = [];
  await page.route("**/api/v1/tasks", async (route: Route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    created.push(JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>);
    await route.fulfill({
      body: JSON.stringify(sampleTask()),
      contentType: "application/json",
      status: 201,
    });
  });
  return created;
}

/** 打开创建任务弹窗 */
async function openCreateModal(page: Page) {
  await page.goto("/tasks");
  await page.getByRole("button", { name: "创建任务" }).click();
}

/** 选择关联项目（选项标题为「名称（标识）」） */
async function selectProject(page: Page, name: RegExp | string) {
  await page.getByLabel("关联项目").click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(name).click();
}

/** 选择执行节点 */
async function selectHost(page: Page) {
  await page.getByLabel("执行节点").click();
  await page
    .locator(".ant-select-dropdown:visible")
    .getByTitle(/构建机/)
    .click();
}

/** 打开环境下拉并选择指定环境 */
async function selectEnvironment(page: Page, name: string) {
  await page.getByLabel("语言环境").click();
  await page
    .locator(".ant-select-dropdown:visible")
    .getByTitle(new RegExp(`^${name} ·`))
    .click();
}

/** 打开版本下拉并选择指定版本 */
async function selectVersion(page: Page, version: string) {
  await page.getByLabel("环境版本").click();
  await page
    .locator(".ant-select-dropdown:visible")
    .getByTitle(new RegExp(`^${version} ·`))
    .click();
}

/**
 * 当前选中的环境下拉取值。
 *
 * antd 6 的选中项没有稳定类名，且会同时渲染一个视觉隐藏的 `aria-live` 播报节点，
 * 因此按可见文本断言，并只保留可见元素（下拉关闭后其选项仍在 DOM 中但不可见）。
 */
function selectedEnvironment(page: Page, name: string) {
  return page.getByText(new RegExp(`^${name} · `)).filter({ visible: true });
}

/** 当前选中的版本下拉取值 */
function selectedVersion(page: Page, version: string) {
  return page.getByText(new RegExp(`^${version} · `)).filter({ visible: true });
}

/** 某个表单项的额外说明 */
function fieldExtra(page: Page, label: string) {
  return page.locator(".ant-form-item").filter({ hasText: label }).locator(".ant-form-item-extra");
}

/** 收起摘要中的某一行（仓库地址 / 基础分支 / 容器内工作目录） */
function summaryRow(page: Page, label: string) {
  return page.locator(".ant-descriptions-item").filter({ hasText: label });
}

/** 展开"项目默认执行配置"：三个可继承字段默认收起 */
async function expandProjectDefaults(page: Page) {
  await page.getByRole("button", { name: /^展开修改仓库地址、基础分支与工作目录$/ }).click();
}

/** 收起"项目默认执行配置" */
async function collapseProjectDefaults(page: Page) {
  await page.getByRole("button", { name: /^收起仓库地址、基础分支与工作目录$/ }).click();
}

/** 选择角色 */
async function selectRole(page: Page, ariaLabel: string, label: RegExp) {
  await page.getByLabel(ariaLabel).click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(label).click();
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
  await stubTaskList(page);
  await stubRoles(page);
  await stubHosts(page, [sampleHost()]);
  // 当前节点上：go 有可用版本 v0001，node 有可用版本 v0002（不含项目 B 锁定的 v0009），
  // python 有可用版本 v0003——它不属于任何项目，用于验证"用户显式选择"的优先级。
  await stubEnvironments(page, () => [
    sampleEnvironment(),
    sampleEnvironment({
      current_version: "v0002",
      latest_version: "v0002",
      name: "node",
      versions: [
        sampleVersion({
          image_ref: "agentflow-language-node:v0002-abcdef",
          number: 2,
          version: "v0002",
        }),
      ],
    }),
    sampleEnvironment({
      current_version: "v0003",
      latest_version: "v0003",
      name: "python",
      versions: [
        sampleVersion({
          image_ref: "agentflow-language-python:v0003-abcdef",
          number: 3,
          version: "v0003",
        }),
      ],
    }),
  ]);
});

test.describe("创建任务时的项目配置继承与显式覆盖", () => {
  test("默认收起时只展示摘要，摘要取值与展开后的表单一致", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);

    // 三个可继承字段默认收起，只展示当前有效取值与来源。
    await expect(page.getByLabel("仓库地址")).toBeHidden();
    await expect(page.getByRole("textbox", { name: "* 基础分支" })).toBeHidden();
    await expect(summaryRow(page, "仓库地址")).toContainText("ssh://git.example.com/team/repo");
    await expect(summaryRow(page, "基础分支")).toContainText("main");
    await expect(summaryRow(page, "容器内工作目录")).toContainText("/workspace/project");
    await expect(summaryRow(page, "仓库地址")).toContainText(/来自项目「示例服务」的默认配置/);

    // 语言环境与版本必须始终可见：它取决于当前执行节点，不能藏进收起区域。
    await expect(selectedEnvironment(page, "go").first()).toBeVisible();
    await expect(selectedVersion(page, "v0001").first()).toBeVisible();
    await expect(page.getByText(/来自项目「示例服务」的默认配置/).first()).toBeVisible();

    // 展开入口是可聚焦按钮：键盘回车即可展开，展开后字段值与摘要一致。
    await page.getByRole("button", { name: /^展开修改/ }).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("button", { name: /^收起仓库地址/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    await expect(page.getByLabel("仓库地址")).toHaveValue("ssh://git.example.com/team/repo");
    await expect(page.getByRole("textbox", { name: "* 基础分支" })).toHaveValue("main");
    await expect(page.getByLabel("容器内工作目录（可选）")).toHaveValue("/workspace/project");
  });

  test("用户覆盖后收起：摘要显示覆盖值，再展开不丢失输入与来源", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);
    await expandProjectDefaults(page);
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/fork/repo");

    await collapseProjectDefaults(page);
    // 摘要必须与表单取值一致：把项目原始地址显示成任务配置会误导用户。
    await expect(summaryRow(page, "仓库地址")).toContainText("ssh://git.example.com/fork/repo");
    await expect(summaryRow(page, "仓库地址")).toContainText(/已手动设置/);
    await expect(summaryRow(page, "仓库地址")).not.toContainText("ssh://git.example.com/team/repo");
    await expect(summaryRow(page, "基础分支")).toContainText(/来自项目「示例服务」的默认配置/);

    await expandProjectDefaults(page);
    await expect(page.getByLabel("仓库地址")).toHaveValue("ssh://git.example.com/fork/repo");
    await expect(fieldExtra(page, "仓库地址").getByText(/已手动设置/)).toBeVisible();
  });

  test("收起状态下必填字段没有取值时，提交会先展开该分组再显示字段错误", async ({ page }) => {
    // 项目记录缺少仓库地址时界面不能伪造一个地址，也不能把错误藏在收起区域里。
    await stubProjects(page, [projectA({ repository_url: "" })]);
    const created = await stubCreateTask(page);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);

    await expect(summaryRow(page, "仓库地址")).toContainText(/未填写/);
    await expect(page.getByLabel("仓库地址")).toBeHidden();

    await page.getByLabel("任务名称").fill("新建任务");
    await page.getByLabel("任务描述").fill("描述内容");
    await selectRole(page, "规划角色", /规划角色/);
    await selectRole(page, "开发角色", /开发角色/);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText("请输入仓库地址")).toBeVisible();
    await expect(page.getByLabel("仓库地址")).toBeVisible();
    expect(created).toHaveLength(0);
  });

  test("项目列表查询失败时摘要不显示来自项目，也不把空值当成继承配置", async ({ page }) => {
    await page.route("**/api/v1/projects", (route) =>
      route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "项目查询不可用" } }),
        contentType: "application/json",
        status: 500,
      }),
    );

    await openCreateModal(page);
    await selectHost(page);

    await expect(page.getByText("项目查询失败")).toBeVisible();
    await expect(page.getByText(/尚未选择关联项目/)).toBeVisible();
    await expect(summaryRow(page, "仓库地址")).toContainText(/未填写/);
    await expect(summaryRow(page, "基础分支")).toContainText(/未填写/);
    await expect(page.getByText(/来自项目「/)).toHaveCount(0);
  });

  test("环境查询失败时不显示虚假的环境与版本", async ({ page }) => {
    await stubProjects(page, [projectA()]);
    await page.route("**/api/v1/environments?*", (route) =>
      route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "环境查询不可用" } }),
        contentType: "application/json",
        status: 500,
      }),
    );

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);

    await expect(page.getByText("环境查询失败")).toBeVisible();
    // 项目锁定的环境没有被预填：查询失败不等于"项目环境可用"。
    await expect(selectedEnvironment(page, "go")).toHaveCount(0);
    await expect(selectedVersion(page, "v0001")).toHaveCount(0);
  });

  test("手动修改仓库地址后切换项目：修改过的字段保留，其余字段跟随新项目", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);
    await expandProjectDefaults(page);
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/fork/repo");

    await selectProject(page, /其他服务/);

    await expect(page.getByLabel("仓库地址")).toHaveValue("ssh://git.example.com/fork/repo");
    await expect(page.getByRole("textbox", { name: "* 基础分支" })).toHaveValue("release/1.x");
    await expect(page.getByLabel("容器内工作目录（可选）")).toHaveValue("/workspace/other");
    await expect(fieldExtra(page, "仓库地址").getByText(/已手动设置/)).toBeVisible();

    // 显式覆盖必须可恢复为项目默认值，否则用户只能手抄 URL。
    await page
      .locator(".ant-form-item")
      .filter({ hasText: "仓库地址" })
      .getByText("恢复项目默认值")
      .click();
    await expect(page.getByLabel("仓库地址")).toHaveValue("ssh://git.example.com/team/other.git");
  });

  test("项目切换把环境换成新项目的环境时，旧环境的版本不会残留", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);
    await expect(selectedVersion(page, "v0001").first()).toBeVisible();

    // 项目 B 锁定 node 的 v0009，该版本在当前节点不存在：只预填环境，不猜版本。
    await selectProject(page, /其他服务/);

    await expect(selectedEnvironment(page, "node").first()).toBeVisible();
    // 旧环境 go 的 v0001 不能留在"node"环境下。
    await expect(selectedVersion(page, "v0001")).toHaveCount(0);
    await expect(page.getByText("选择可用版本")).toBeVisible();
    // 项目环境可用但版本不可用时，不得伪造一个版本。
    await expect(selectedVersion(page, "v0009")).toHaveCount(0);
  });

  test("先选项目、后选节点：环境列表就绪后补齐项目默认值", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    // 没有执行节点就没有环境查询：此时只能预填与节点无关的字段。
    await selectProject(page, /示例服务/);
    await expect(summaryRow(page, "仓库地址")).toContainText("ssh://git.example.com/team/repo");
    await expect(page.getByText("尚未选择执行节点")).toBeVisible();

    await selectHost(page);

    await expect(selectedEnvironment(page, "go").first()).toBeVisible();
    await expect(selectedVersion(page, "v0001").first()).toBeVisible();
    await expect(page.getByText(/来自项目「示例服务」的默认配置/).first()).toBeVisible();
  });

  test("先选节点与环境、后选项目：用户已选的环境不会被项目默认值覆盖", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    await selectHost(page);
    await selectEnvironment(page, "python");
    await selectVersion(page, "v0003");

    await selectProject(page, /示例服务/);

    await expect(selectedEnvironment(page, "python").first()).toBeVisible();
    await expect(selectedVersion(page, "v0003").first()).toBeVisible();
    // 项目默认值只填进未设置过的字段。
    await expect(summaryRow(page, "基础分支")).toContainText("main");
    await expect(page.getByText(/已手动设置/).first()).toBeVisible();
  });

  test("用户手动选择的环境与版本不会被项目切换覆盖", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);
    await expect(selectedEnvironment(page, "go").first()).toBeVisible();

    // 用户显式选择项目默认值之外的环境与版本（python 不属于任何项目）。
    await selectEnvironment(page, "python");
    await selectVersion(page, "v0003");
    await expect(selectedVersion(page, "v0003").first()).toBeVisible();

    // 项目 B 的环境是 node：用户已经手动选过 python，环境与版本都不应被改写。
    await selectProject(page, /其他服务/);

    await expect(selectedEnvironment(page, "python").first()).toBeVisible();
    await expect(selectedVersion(page, "v0003").first()).toBeVisible();
    await expect(page.getByText(/已手动设置/).first()).toBeVisible();
  });

  test("提交体遵守后端继承规则：项目标识来自记录，未设置的字段不提交", async ({ page }) => {
    await stubProjects(page, [projectA(), projectB()]);
    const created = await stubCreateTask(page);

    await openCreateModal(page);
    await selectHost(page);
    await selectProject(page, /示例服务/);
    await page.getByLabel("任务名称").fill("新建任务");
    await page.getByLabel("任务描述").fill("描述内容");
    // 清空工作目录：空值表示"未覆盖"，由服务端继承项目配置。
    await expandProjectDefaults(page);
    await page.getByLabel("容器内工作目录（可选）").fill("");
    await page.getByLabel("规划角色").click();
    await page
      .locator(".ant-select-dropdown:visible")
      .getByTitle(/规划角色/)
      .click();
    await page.getByLabel("开发角色").click();
    await page
      .locator(".ant-select-dropdown:visible")
      .getByTitle(/开发角色/)
      .click();

    await page.getByRole("button", { name: "创 建" }).click();
    await expect.poll(() => created.length).toBe(1);

    const body = created[0] ?? {};
    expect(body.project_id).toBe("demo-service");
    expect(body.host_id).toBe("host-1");
    expect(body.repository_url).toBe("ssh://git.example.com/team/repo");
    expect(body.base_branch).toBe("main");
    expect(body.environment).toBe("go");
    expect(body.environment_version).toBe("v0001");
    // 表单没有缓存类型入口，缓存类型只能由服务端从项目继承，不能出现在请求体里。
    expect(body).not.toHaveProperty("caches");
    expect(body).not.toHaveProperty("work_dir");
  });
});
