import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 统一任务启动与环境选择的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型。
 * 用例关注的是"页面是否走对了接口、是否如实展示服务端事实"：
 *   - 新任务必须走 `POST /api/v1/tasks/{id}/start`，不得回落到 `execute-workflow`；
 *   - `workflow_started=false` 不得显示为启动成功；
 *   - 启动失败不得伪造 running 状态；
 *   - 环境与版本必须来自真实查询，不可用版本不得被选中。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    created_at: "2025-01-01T00:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "queued",
    status_finished: false,
    status_terminal: false,
    updated_at: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

/** 一份统一启动响应样本，字段与 Go 端 `httpapi.StartTaskResponse` 一致 */
function sampleLaunch(overrides: Record<string, unknown> = {}) {
  return {
    container_prepared: true,
    plan_persisted: true,
    plan_steps: [
      {
        required_checks: ["build"],
        role_id: "developer",
        sequence: 1,
        status: "pending",
        step_id: "step-1",
        step_type: "develop",
        title: "实现登录接口",
      },
    ],
    stage: "workflow",
    task_id: "task-1",
    task_status: "developing",
    workflow_started: true,
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
async function stubTaskList(page: Page, handler: () => { body: unknown; status?: number }) {
  await page.route("**/api/v1/tasks?*", async (route: Route) => {
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/** 拦截统一启动接口，记录调用次数与请求体 */
async function stubLaunch(
  page: Page,
  handler: (body: unknown) => { body: unknown; status?: number },
) {
  const calls: unknown[] = [];
  await page.route("**/api/v1/tasks/*/start", async (route: Route) => {
    calls.push(JSON.parse(route.request().postData() ?? "{}") as unknown);
    const result = handler(calls[calls.length - 1]);
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 202,
    });
  });
  return calls;
}

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

/** 一份项目样本，字段与 Go 端 `httpapi.ProjectResponse` 一致 */
function sampleProject(overrides: Record<string, unknown> = {}) {
  return {
    base_branch: "main",
    caches: ["go-mod"],
    created_at: "2026-10-28T02:00:00Z",
    environment: "go",
    id: "demo-service",
    name: "示例服务",
    repository_url: "ssh://git.example.com/team/repo",
    updated_at: "2026-10-28T02:00:00Z",
    work_dir: "/workspace/project",
    ...overrides,
  };
}

/** 拦截项目列表接口，默认返回一条真实项目记录 */
async function stubProjects(
  page: Page,
  handler: () => { body: unknown; status?: number } = () => ({
    body: { records: [sampleProject()], total: 1 },
  }),
) {
  await page.route("**/api/v1/projects", async (route: Route) => {
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/** 通过项目选择器选中指定项目（按名称匹配） */
async function selectProject(page: Page, label: RegExp | string) {
  await page.getByLabel("关联项目").click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(label).click();
}

/** 一份角色样本，字段与 Go 端 `httpapi.RoleResponse` 一致 */
function sampleRole(overrides: Record<string, unknown> = {}) {
  return {
    enabled: true,
    id: "role-1",
    model: "group/free",
    name: "角色",
    runner: "pi",
    type: "planner",
    ...overrides,
  };
}

/** 拦截执行节点列表接口，默认返回一个可用节点 */
async function stubHosts(
  page: Page,
  handler: () => { body: unknown; status?: number } = () => ({
    body: { records: [sampleHost()], total: 1 },
  }),
) {
  await page.route("**/api/v1/hosts", async (route: Route) => {
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/** 拦截角色列表接口，默认返回一个规划角色与一个开发角色 */
async function stubRoles(
  page: Page,
  handler: () => { body: unknown; status?: number } = () => ({
    body: {
      records: [
        sampleRole({ id: "planner", name: "规划角色" }),
        sampleRole({ id: "developer", name: "开发角色", type: "developer" }),
      ],
      total: 2,
    },
  }),
) {
  await page.route("**/api/v1/roles", async (route: Route) => {
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/** 通过执行节点选择器选中指定节点（按名称匹配） */
async function selectHost(page: Page, label: RegExp | string) {
  await page.getByLabel("执行节点").click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(label).click();
}

/** 通过角色选择器选中指定角色（按 aria-label 与名称匹配） */
async function selectRole(page: Page, ariaLabel: string, label: RegExp | string) {
  await page.getByLabel(ariaLabel).click();
  await page.locator(".ant-select-dropdown:visible").getByTitle(label).click();
}

/** 拦截环境查询接口，记录 host_id 参数 */
async function stubEnvironments(
  page: Page,
  handler: (hostID: string) => { body: unknown; status?: number },
) {
  const hosts: string[] = [];
  await page.route("**/api/v1/environments?*", async (route: Route) => {
    const url = new URL(route.request().url());
    const hostID = url.searchParams.get("host_id") ?? "";
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

/** 拦截工作流状态查询 */
async function stubWorkflow(page: Page, handler: () => { body: unknown; status?: number }) {
  await page.route("**/api/v1/tasks/*/workflow", async (route: Route) => {
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/**
 * 点击任务列表中某一行的启动按钮。
 *
 * 按钮在启动期间变为"启动中"并禁用，因此定位使用行内文本而不是全局唯一匹配。
 */
async function clickStart(page: Page, rowText: string) {
  const row = page.locator(".ant-table-tbody tr.ant-table-row", { hasText: rowText });
  await row.getByText("启动", { exact: true }).click();
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

/**
 * 展开"项目默认执行配置"：仓库地址、基础分支与容器内工作目录默认收起并继承关联项目，
 * 要为本次任务覆盖它们必须先展开。
 */
async function expandProjectDefaults(page: Page) {
  const button = page.getByRole("button", { name: /展开修改仓库地址、基础分支与工作目录/ });
  if (await button.isVisible()) await button.click();
}

/** 拦截任务详情 */
async function stubTaskDetail(page: Page, handler: () => { body: unknown; status?: number }) {
  await page.route("**/api/v1/tasks/task-1", async (route: Route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
  // 任务必须关联项目：默认提供一条真实项目记录，创建任务弹窗才有可选项。
  await stubProjects(page);
});

test.describe("统一任务启动", () => {
  test("新任务通过 /start 启动，并刷新任务与工作流状态", async ({ page }) => {
    let launched = false;
    const oldEntry: string[] = [];
    await page.route("**/api/v1/tasks/*/execute-workflow", async (route: Route) => {
      oldEntry.push(route.request().url());
      await route.fulfill({ body: "{}", contentType: "application/json", status: 409 });
    });
    const calls = await stubLaunch(page, () => {
      launched = true;
      return { body: sampleLaunch() };
    });
    await stubTaskList(page, () => ({
      body: {
        current: 1,
        records: [sampleTask(launched ? { status: "developing" } : {})],
        size: 20,
        total: 1,
      },
    }));
    await stubTaskDetail(page, () => ({
      body: sampleTask(launched ? { status: "developing" } : {}),
    }));
    await stubWorkflow(page, () => ({
      body: {
        has_run: launched,
        run: launched
          ? {
              cancel_requested: false,
              code_saver_available: true,
              completed_steps: 0,
              last_completed_sequence: 0,
              plan_digest: "digest",
              revision: 1,
              run_id: "run-1",
              stage: "step_execution",
              status: "running",
              status_active: true,
              status_terminal: false,
              task_id: "task-1",
              total_steps: 1,
              updated_at: "2025-01-01T00:00:00Z",
            }
          : undefined,
        steps: [],
        task_id: "task-1",
        task_status: launched ? "developing" : "queued",
      },
    }));

    await page.goto("/tasks");
    await clickStart(page, "实现登录接口");

    await expect(page.getByText("已启动任务执行：实现登录接口")).toBeVisible();
    expect(calls).toHaveLength(1);
    // 不再调用旧的 execute-workflow 入口
    expect(oldEntry).toHaveLength(0);
    // 启动后展示服务端返回的真实工作流状态
    await page.locator(".ant-table-tbody tr.ant-table-row").first().getByText("详情").click();
    await expect(page.getByText("工作流进度")).toBeVisible();
    await expect(page.getByText("执行中").first()).toBeVisible();
  });

  test("启动过程中重复点击被阻止", async ({ page }) => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const calls: unknown[] = [];
    await page.route("**/api/v1/tasks/*/start", async (route: Route) => {
      calls.push(JSON.parse(route.request().postData() ?? "{}") as unknown);
      await gate;
      await route.fulfill({
        body: JSON.stringify(sampleLaunch()),
        contentType: "application/json",
        status: 202,
      });
    });
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));
    await stubWorkflow(page, () => ({
      body: { has_run: false, steps: [], task_id: "task-1", task_status: "queued" },
    }));

    await page.goto("/tasks");
    const button = page
      .getByText("启动中", { exact: true })
      .or(page.getByText("启动", { exact: true }));
    await button.click();
    // 请求未完成前按钮处于"启动中"且不可再次点击
    await expect(page.getByText("启动中", { exact: true })).toBeVisible();
    await page.getByText("启动中", { exact: true }).click({ force: true });
    expect(calls).toHaveLength(1);

    release();
    await expect(page.getByText("已启动任务执行：实现登录接口")).toBeVisible();
    expect(calls).toHaveLength(1);
  });

  test("workflow_started=false 不显示为启动成功", async ({ page }) => {
    await stubLaunch(page, () => ({
      body: sampleLaunch({
        container_prepared: false,
        failure: "执行节点未通过环境预检",
        failure_stage: "validate",
        plan_persisted: false,
        plan_steps: [],
        stage: "validate",
        task_status: "queued",
        workflow_started: false,
      }),
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));
    await stubWorkflow(page, () => ({
      body: { has_run: false, steps: [], task_id: "task-1", task_status: "queued" },
    }));

    await page.goto("/tasks");
    await clickStart(page, "实现登录接口");

    await expect(page.getByText("启动流程未完成，工作流未启动：实现登录接口")).toBeVisible();
    await expect(page.getByText(/执行节点未通过环境预检/)).toBeVisible();
    // 不显示为成功
    await expect(page.getByText("已启动任务执行：实现登录接口")).toHaveCount(0);
  });

  test("启动请求失败时展示服务端错误且不伪造 running 状态", async ({ page }) => {
    await stubLaunch(page, () => ({
      body: {
        error: { code: "state_conflict", message: "任务已有未结束的工作流执行，不能重复启动" },
      },
      status: 409,
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));
    await stubWorkflow(page, () => ({
      body: { has_run: false, steps: [], task_id: "task-1", task_status: "queued" },
    }));

    await page.goto("/tasks");
    await clickStart(page, "实现登录接口");

    await expect(page.getByText("操作失败")).toBeVisible();
    await expect(page.getByText("任务已有未结束的工作流执行，不能重复启动")).toBeVisible();
    // 任务仍是等待执行，没有被乐观改成执行中
    await expect(page.getByText("等待执行").first()).toBeVisible();
    await expect(page.getByText("已启动任务执行：实现登录接口")).toHaveCount(0);
  });

  test("状态刷新失败不会抹除已收到的启动结论", async ({ page }) => {
    await stubLaunch(page, () => ({ body: sampleLaunch() }));
    // 列表保持等待执行：启动按钮必须可用，才能验证启动后的刷新失败行为
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));
    // 任务详情与工作流查询都失败
    await stubTaskDetail(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));
    await stubWorkflow(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));

    await page.goto("/tasks");
    await clickStart(page, "实现登录接口");

    await expect(page.getByText("状态刷新失败", { exact: true })).toBeVisible();
    await expect(page.getByText(/启动请求已结束，但状态刷新失败/)).toBeVisible();
    // 已得到的启动结论仍然展示
    await expect(page.getByText("已启动任务执行：实现登录接口")).toBeVisible();
  });

  test("不自动重试启动请求", async ({ page }) => {
    const calls = await stubLaunch(page, () => ({
      body: { error: { code: "execution_failed", message: "容器准备失败" } },
      status: 500,
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [sampleTask()], size: 20, total: 1 },
    }));
    await stubWorkflow(page, () => ({
      body: { has_run: false, steps: [], task_id: "task-1", task_status: "queued" },
    }));

    await page.goto("/tasks");
    await clickStart(page, "实现登录接口");

    await expect(page.getByText("操作失败")).toBeVisible();
    await page.waitForTimeout(500);
    expect(calls).toHaveLength(1);
  });

  test("failed 任务不显示启动入口", async ({ page }) => {
    await stubTaskList(page, () => ({
      body: {
        current: 1,
        records: [
          sampleTask({
            id: "task-failed",
            name: "失败任务",
            status: "failed",
            status_finished: true,
          }),
        ],
        size: 20,
        total: 1,
      },
    }));

    await page.goto("/tasks");
    await expect(page.getByText("失败任务")).toBeVisible();
    await expect(page.getByText("启动", { exact: true })).toHaveClass(/ant-typography-disabled/);
  });
});

test.describe("创建任务的环境选择", () => {
  test("执行节点变化时重新查询环境并清空旧选择", async ({ page }) => {
    const hosts = await stubEnvironments(page, (hostID) => ({
      body: {
        records: [
          {
            current_version: "v0001",
            latest_version: "v0001",
            name: hostID === "host-2" ? "node" : "go",
            type: "language",
            versions: [sampleVersion()],
          },
        ],
        total: 1,
      },
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [], size: 20, total: 0 },
    }));

    await stubHosts(page, () => ({
      body: {
        records: [
          sampleHost(),
          sampleHost({ host: "10.0.0.12", id: "host-2", is_default: false, name: "第二台" }),
        ],
        total: 2,
      },
    }));
    await stubRoles(page);
    await page.goto("/tasks");
    await page.getByRole("button", { name: "创建任务" }).click();
    await selectHost(page, /构建机/);

    await expect.poll(() => hosts.at(-1)).toBe("host-1");
    // 选择环境与版本
    await selectEnvironment(page, "go");
    await selectVersion(page, "v0001");

    // 切换执行节点后，旧的环境与版本被清除，并重新查询新节点
    await selectHost(page, /第二台/);
    await expect.poll(() => hosts.at(-1)).toBe("host-2");
    await expect(page.getByText("选择真实环境")).toBeVisible();
    await expect(page.getByText("请先选择环境")).toBeVisible();
  });

  test("环境查询失败不被伪装成空列表", async ({ page }) => {
    await stubEnvironments(page, () => ({
      body: { error: { code: "not_found", message: "执行节点不存在: host-1" } },
      status: 404,
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [], size: 20, total: 0 },
    }));

    await stubHosts(page);
    await stubRoles(page);
    await page.goto("/tasks");
    await page.getByRole("button", { name: "创建任务" }).click();
    await selectHost(page, /构建机/);
    await expect(page.getByText("环境查询失败")).toBeVisible();
    await expect(page.getByText("执行节点不存在: host-1")).toBeVisible();
    await expect(page.getByText("没有可用的环境")).toHaveCount(0);
  });

  test("空环境列表显示明确的空状态", async ({ page }) => {
    await stubEnvironments(page, () => ({ body: { records: [], total: 0 } }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [], size: 20, total: 0 },
    }));

    await stubHosts(page);
    await stubRoles(page);
    await page.goto("/tasks");
    await page.getByRole("button", { name: "创建任务" }).click();
    await selectHost(page, /构建机/);
    await expect(page.getByText("没有可用的环境")).toBeVisible();
  });

  test("没有可用版本的环境被禁用且不能提交", async ({ page }) => {
    await stubEnvironments(page, () => ({
      body: {
        records: [
          {
            latest_version: "v0002",
            name: "go",
            type: "language",
            versions: [
              sampleVersion({
                build_status: "failed",
                error_summary: "构建失败: 镜像核实未通过",
                number: 2,
                usable: false,
                version: "v0002",
              }),
            ],
          },
        ],
        total: 1,
      },
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [], size: 20, total: 0 },
    }));

    await stubHosts(page);
    await stubRoles(page);
    await page.goto("/tasks");
    await page.getByRole("button", { name: "创建任务" }).click();
    await selectHost(page, /构建机/);
    await expect(page.getByText("正在查询该执行节点上的环境…")).toBeHidden({ timeout: 5000 });

    await expect(page.getByText("没有可用的环境版本")).toBeVisible();
    await expect(page.getByText("暂不可用于任务启动", { exact: false }).first()).toBeVisible();

    // 填完必填项后提交：环境未选中，必须被阻止
    await selectProject(page, /示例服务/);
    await page.getByLabel("任务名称").fill("新建任务");
    await page.getByLabel("任务描述").fill("描述内容");
    await expandProjectDefaults(page);
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/team/repo");
    await page.getByRole("textbox", { name: "* 基础分支" }).fill("main");
    await selectRole(page, "规划角色", /规划角色/);
    await selectRole(page, "开发角色", /开发角色/);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect(page.getByText("请选择语言环境")).toBeVisible();
  });

  test("提交请求使用实际选中的环境与版本", async ({ page }) => {
    await stubEnvironments(page, () => ({
      body: {
        records: [
          {
            current_version: "v0002",
            latest_version: "v0003",
            name: "go",
            type: "language",
            versions: [
              sampleVersion({ number: 1, usable: false, version: "v0001" }),
              sampleVersion({ number: 2, version: "v0002" }),
              sampleVersion({
                build_status: "building",
                number: 3,
                usable: false,
                version: "v0003",
              }),
            ],
          },
        ],
        total: 1,
      },
    }));
    await stubTaskList(page, () => ({
      body: { current: 1, records: [], size: 20, total: 0 },
    }));
    let created: Record<string, unknown> | undefined;
    await page.route("**/api/v1/tasks", async (route: Route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      created = JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>;
      await route.fulfill({
        body: JSON.stringify(sampleTask({ id: "task-new", name: "新建任务" })),
        contentType: "application/json",
        status: 201,
      });
    });

    await stubHosts(page);
    await stubRoles(page);
    await page.goto("/tasks");
    await page.getByRole("button", { name: "创建任务" }).click();
    await selectHost(page, /构建机/);
    await expect(page.getByText("选择真实环境")).toBeVisible({ timeout: 5000 });

    await selectEnvironment(page, "go");
    await selectVersion(page, "v0002");

    await selectProject(page, /示例服务/);
    await page.getByLabel("任务名称").fill("新建任务");
    await page.getByLabel("任务描述").fill("描述内容");
    await expandProjectDefaults(page);
    await page.getByLabel("仓库地址").fill("ssh://git.example.com/team/repo");
    await page.getByRole("textbox", { name: "* 基础分支" }).fill("main");
    await selectRole(page, "规划角色", /规划角色/);
    await selectRole(page, "开发角色", /开发角色/);
    await page.getByRole("button", { name: "创 建" }).click();

    await expect.poll(() => created?.environment).toBe("go");
    expect(created?.environment_version).toBe("v0002");
    // 不提交容器、步骤或角色覆盖以外的执行期字段
    expect(created).not.toHaveProperty("status");
    expect(created).not.toHaveProperty("started_at");
  });
});
