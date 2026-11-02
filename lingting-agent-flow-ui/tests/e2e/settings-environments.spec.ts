import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 设置中心「环境管理」浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、SSH 或 Docker。返回体
 * 形状与 Go 端 DTO 保持一致（`HostListResponse`、`EnvironmentListResponse`、
 * `EnvironmentVersionResponse`、`EnvironmentBuildAcceptedResponse` 等）。
 *
 * 用例关注业务断言：环境可用性只由服务端字段决定、构建失败与结果未知分别如实展示、
 * 日志读取失败不显示成空日志、在途构建期间禁止重复提交、切换节点后丢弃迟到响应，
 * 以及独立的环境导航入口已经被移除。
 */

/** 一个可用于环境操作的执行节点 */
function managedHost(overrides: Record<string, unknown> = {}) {
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
    validation_message: "",
    validation_status: "normal",
    workspace_root: "/home/dev/agentflow",
    ...overrides,
  };
}

/** 一个不可用于远程操作的执行节点 */
function unusableHost(overrides: Record<string, unknown> = {}) {
  return managedHost({
    host: "10.0.0.12",
    id: "host-2",
    is_default: false,
    name: "备用机",
    usable_for_remote_task: false,
    validation_message: "SSH 连接失败",
    validation_status: "abnormal",
    ...overrides,
  });
}

/** 一个环境版本 */
function version(overrides: Record<string, unknown> = {}) {
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

/** 一个语言环境 */
function languageEnvironment(overrides: Record<string, unknown> = {}) {
  return {
    current_version: "v0002",
    latest_version: "v0002",
    name: "go",
    type: "language",
    versions: [
      version({ number: 1, version: "v0001" }),
      version({
        image_ref: "agentflow-language-go:v0002-fedcba987654",
        number: 2,
        version: "v0002",
      }),
    ],
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
        checked_at: "2026-10-26T08:00:00Z",
        status: "ok",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截执行节点列表 */
async function stubHosts(page: Page, hosts: () => unknown[]) {
  await page.route("**/api/v1/hosts", async (route: Route) => {
    await route.fulfill({
      body: JSON.stringify({ records: hosts(), total: hosts().length }),
      contentType: "application/json",
      status: 200,
    });
  });
}

/** 拦截环境列表；按 host_id 返回不同响应，并可注入延迟以模拟迟到响应 */
async function stubEnvironmentList(
  page: Page,
  handler: (hostID: string) => { body: unknown; delayMs?: number },
) {
  const hostIDs: string[] = [];
  await page.route("**/api/v1/environments?*", async (route: Route) => {
    const hostID = new URL(route.request().url()).searchParams.get("host_id") ?? "";
    hostIDs.push(hostID);
    const result = handler(hostID);
    if (result.delayMs) {
      await new Promise((resolve) => setTimeout(resolve, result.delayMs));
    }
    try {
      await route.fulfill({
        body: JSON.stringify(result.body),
        contentType: "application/json",
        status: 200,
      });
    } catch {
      // 请求已被调用方取消（例如切换节点、组件卸载），无需应答。
    }
  });
  return hostIDs;
}

/** 拦截单版本查询 */
async function stubVersion(
  page: Page,
  handler: (number: string) => { body: unknown; status?: number },
) {
  await page.route("**/api/v1/environments/*/*/versions/*?*", async (route: Route) => {
    const number = new URL(route.request().url()).pathname.split("/").pop() ?? "";
    const result = handler(number);
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/** 拦截构建状态查询 */
async function stubBuildStatus(
  page: Page,
  handler: (number: string) => { body: unknown; status?: number },
) {
  const calls: string[] = [];
  await page.route("**/api/v1/environments/*/*/versions/*/build-status?*", async (route: Route) => {
    const segments = new URL(route.request().url()).pathname.split("/");
    const number = segments[segments.length - 2] ?? "";
    calls.push(number);
    const result = handler(number);
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return calls;
}

/** 拦截构建触发接口，记录请求体 */
async function stubBuild(page: Page, handler: () => { body: unknown; status?: number }) {
  const created: Record<string, unknown>[] = [];
  await page.route("**/api/v1/environments/*/*/build", async (route: Route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    created.push(JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>);
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 202,
    });
  });
  return created;
}

/** 拦截 Dockerfile 读取 */
async function stubDockerfile(page: Page, content: string) {
  await page.route("**/api/v1/environments/*/*/versions/*/dockerfile?*", (route) =>
    route.fulfill({
      body: JSON.stringify({
        content,
        name: "go",
        remote_path: "/home/dev/agentflow/environments/languages/go/versions/v0001/Dockerfile",
        type: "language",
        version: "v0001",
        version_number: 1,
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截构建日志读取 */
async function stubBuildLog(page: Page, handler: () => { body: unknown; status?: number }) {
  await page.route("**/api/v1/environments/*/*/versions/*/build-log?*", async (route: Route) => {
    const result = handler();
    await route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
}

/** 打开设置页 */
async function openSettings(page: Page) {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { name: "环境管理" })).toBeVisible();
}

/** 选择执行节点 */
async function selectHost(page: Page, label: RegExp) {
  await page.getByLabel("执行节点").click();
  // 下拉选项的 title 属性是"是否可用于环境操作"的说明，因此按可见选项文本定位，
  // 而不是按 title 或可访问名（后者由 aria 层渲染，可能处于隐藏状态）。
  await page.locator(".ant-select-item-option").filter({ hasText: label }).click();
}

/** 在环境列表中选择指定环境进入管理 */
async function manageEnvironment(page: Page, name: string) {
  await page
    .getByRole("row", { name: new RegExp(name) })
    // 两个汉字的按钮会被 antd 插入空格（"管 理"），因此按正则可选地匹配空白。
    .getByRole("button", { name: /管\s*理/ })
    .click();
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test("设置页承载环境管理且独立环境导航入口已移除", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await openSettings(page);

  // 独立入口必须消失：菜单里只有设置，没有"环境"。
  await expect(page.getByRole("menuitem", { name: "环境" })).toHaveCount(0);
  await expect(page.getByRole("menuitem", { name: "设置" })).toHaveCount(1);
  // 环境管理确实在设置页内，而不是跳转到另一个页面。
  await expect(page.getByText("环境按执行节点隔离")).toBeVisible();
});

test("执行节点选择展示真实可用性，且不自动选中默认节点", async ({ page }) => {
  await stubHosts(page, () => [managedHost(), unusableHost()]);
  await stubEnvironmentList(page, () => ({ body: { records: [], total: 0 } }));
  await openSettings(page);

  // 尚未选择节点时不发起环境查询：环境属于具体节点，不猜默认值。
  await expect(page.getByText("选择执行节点后会查询该节点上真实存在的环境与版本。")).toBeVisible();

  await page.getByLabel("执行节点").click();
  const options = page.locator(".ant-select-item-option");
  await expect(
    options.filter({ hasText: /构建机（10\.0\.0\.11:22）· 可用于环境操作/ }),
  ).toBeVisible();
  await expect(
    options.filter({ hasText: /备用机（10\.0\.0\.12:22）· 不可用于环境操作/ }),
  ).toBeVisible();
});

test("环境列表与版本详情只展示服务端给出的状态", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await stubEnvironmentList(page, () => ({
    body: { records: [languageEnvironment()], total: 1 },
  }));
  await openSettings(page);
  await selectHost(page, /构建机/);

  // 列表：当前可用版本来自服务端 current_version。
  const row = page.getByRole("row", { name: /go/ });
  await expect(row).toContainText("v0002");
  await expect(row).toContainText("2 个（可用 2，失败/未知 0）");

  await manageEnvironment(page, "go");
  // 详情：当前可用版本与最新版本分开展示。
  // 详情：当前可用版本与最新创建版本分开展示（列表表头也含同名字样，因此限定在详情区）。
  const detailLabels = page.locator(".ant-descriptions-item-label");
  await expect(detailLabels.filter({ hasText: "当前可用版本" })).toBeVisible();
  await expect(detailLabels.filter({ hasText: "最新创建版本" })).toBeVisible();
});

test("构建失败展示服务端原因，成功才显示成功", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await stubEnvironmentList(page, () => ({
    body: {
      records: [
        languageEnvironment({
          current_version: undefined,
          latest_version: "v0002",
          versions: [
            version({
              build_status: "pending",
              image_ref: "agentflow-language-go:v0001-pending0001",
              number: 1,
              usable: false,
              version: "v0001",
            }),
            version({
              build_status: "pending",
              image_ref: "agentflow-language-go:v0002-pending0002",
              number: 2,
              usable: false,
              version: "v0002",
            }),
          ],
        }),
      ],
      total: 1,
    },
  }));
  await stubVersion(page, () => ({ body: version({ build_status: "pending", usable: false }) }));
  await stubBuild(page, () => ({
    body: {
      accepted_at: "2026-10-26T08:01:00Z",
      in_progress: true,
      version: version({ build_status: "pending", usable: false }),
      version_number: 1,
    },
  }));
  await stubBuildStatus(page, (number) =>
    number === "1"
      ? {
          body: {
            error: { code: "environment_build_failed", message: "构建失败: 退出码 1" },
          },
          status: 500,
        }
      : { body: {} },
  );
  await openSettings(page);
  await selectHost(page, /构建机/);
  await manageEnvironment(page, "go");

  await page
    .getByRole("row", { name: /v0001/ })
    .getByRole("button", { name: /构\s*建/ })
    .click();

  // 受理后必须如实展示服务端失败原因，而不是显示构建成功。
  await expect(page.getByText("版本 1 构建失败")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("构建失败: 退出码 1")).toBeVisible();
  await expect(page.getByText("构建成功")).toHaveCount(0);
});

test("构建结果不确定时单独提示", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await stubEnvironmentList(page, () => ({
    body: {
      records: [
        languageEnvironment({
          current_version: undefined,
          latest_version: "v0001",
          versions: [
            version({ build_status: "uncertain", number: 1, usable: false, version: "v0001" }),
          ],
        }),
      ],
      total: 1,
    },
  }));
  await stubVersion(page, () => ({ body: version({ build_status: "uncertain", usable: false }) }));
  await stubBuild(page, () => ({
    body: {
      accepted_at: "2026-10-26T08:01:00Z",
      in_progress: true,
      version: version({ build_status: "uncertain", usable: false }),
      version_number: 1,
    },
  }));
  await stubBuildStatus(page, () => ({
    body: { error: { code: "environment_build_uncertain", message: "构建被中断，结果无法确认" } },
    status: 500,
  }));
  await openSettings(page);
  await selectHost(page, /构建机/);
  await manageEnvironment(page, "go");

  await page
    .getByRole("row", { name: /v0001/ })
    .getByRole("button", { name: /构\s*建/ })
    .click();

  await expect(page.getByText("版本 1 构建结果无法确认")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText("构建失败")).toHaveCount(0);
});

test("在途构建期间禁止重复提交", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await stubEnvironmentList(page, () => ({
    body: {
      records: [
        languageEnvironment({
          current_version: undefined,
          latest_version: "v0001",
          versions: [
            version({ build_status: "pending", number: 1, usable: false, version: "v0001" }),
          ],
        }),
      ],
      total: 1,
    },
  }));
  await stubVersion(page, () => ({ body: version({ build_status: "pending", usable: false }) }));
  const builds = await stubBuild(page, () => ({
    body: {
      accepted_at: "2026-10-26T08:01:00Z",
      in_progress: true,
      version: version({ build_status: "pending", usable: false }),
      version_number: 1,
    },
  }));
  // 构建状态一直停留在"构建中"，模拟真实的长操作。
  await stubBuildStatus(page, () => ({
    body: {
      in_progress: true,
      version: version({ build_status: "building", number: 1, usable: false, version: "v0001" }),
    },
    status: 202,
  }));
  await openSettings(page);
  await selectHost(page, /构建机/);
  await manageEnvironment(page, "go");

  const buildButton = page
    .getByRole("row", { name: /v0001/ })
    .getByRole("button", { name: /构\s*建/ });
  await buildButton.click();
  await expect(page.getByText("构建进行中")).toBeVisible();

  // 在途期间按钮必须禁用：不允许把同一个环境重复提交给后台。
  await expect(
    page.getByRole("row", { name: /v0001/ }).getByRole("button", { name: /构建中/ }),
  ).toBeDisabled();
  expect(builds).toHaveLength(1);
});

test("构建日志读取失败不会显示为空日志", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await stubEnvironmentList(page, () => ({
    body: { records: [languageEnvironment()], total: 1 },
  }));
  await stubBuildLog(page, () => ({
    body: { error: { code: "not_found", message: "构建日志不存在" } },
    status: 404,
  }));
  await openSettings(page);
  await selectHost(page, /构建机/);
  await manageEnvironment(page, "go");

  await page.getByRole("button", { name: "查看构建日志" }).click();
  await expect(page.getByText("读取构建日志失败")).toBeVisible();
  await expect(page.getByText("构建日志不存在")).toBeVisible();
});

test("Dockerfile 查看与编辑为新版本", async ({ page }) => {
  await stubHosts(page, () => [managedHost()]);
  await stubEnvironmentList(page, () => ({
    body: { records: [languageEnvironment()], total: 1 },
  }));
  await stubDockerfile(page, "FROM debian:12.7-slim\nRUN true\n");
  await openSettings(page);
  await selectHost(page, /构建机/);
  await manageEnvironment(page, "go");

  await page.getByRole("button", { name: "查看 Dockerfile" }).click();
  await expect(page.getByText("FROM debian:12.7-slim")).toBeVisible();

  await page
    .getByRole("row", { name: /v0002/ })
    .getByRole("button", { name: "编辑为新版本" })
    .click();
  // 编辑对话框展示的是"新版本"语义：历史版本不可改写。
  await expect(page.getByText("版本只追加")).toBeVisible();
  // 对话框标题也含 "Dockerfile"，因此按 textbox 角色精确定位输入框。
  await expect(page.getByRole("textbox", { name: "Dockerfile" })).toHaveValue(
    "FROM debian:12.7-slim\nRUN true\n",
  );
});

test("切换执行节点后丢弃旧节点的迟到响应", async ({ page }) => {
  await stubHosts(page, () => [
    managedHost(),
    managedHost({ host: "10.0.0.13", id: "host-3", is_default: false, name: "第三台" }),
  ]);
  await stubEnvironmentList(page, (hostID) => {
    if (hostID === "host-1") {
      // 旧节点的响应故意迟到，模拟切换后旧请求才返回。
      return {
        body: {
          records: [languageEnvironment({ name: "old-lang" })],
          total: 1,
        },
        delayMs: 1500,
      };
    }
    return { body: { records: [languageEnvironment({ name: "new-lang" })], total: 1 } };
  });
  await openSettings(page);

  await selectHost(page, /构建机（10\.0\.0\.11/);
  await selectHost(page, /第三台（10\.0\.0\.13/);

  // 新节点的数据必须最终生效，旧节点迟到的响应不得覆盖它。
  await expect(page.getByRole("row", { name: /new-lang/ })).toBeVisible();
  await page.waitForTimeout(2000);
  await expect(page.getByRole("row", { name: /old-lang/ })).toHaveCount(0);
  await expect(page.getByRole("row", { name: /new-lang/ })).toBeVisible();
});

test("不可用节点上的写操作被禁用但环境仍可查看", async ({ page }) => {
  await stubHosts(page, () => [unusableHost()]);
  await stubEnvironmentList(page, () => ({
    body: { records: [languageEnvironment()], total: 1 },
  }));
  await openSettings(page);
  await selectHost(page, /备用机/);

  // 只读查询仍然可用：不可用节点也可能已有历史环境记录。
  await expect(page.getByRole("row", { name: /go/ })).toBeVisible();
  await expect(page.getByText(/该执行节点不可用（abnormal）：SSH 连接失败/)).toBeVisible();
  await expect(page.getByRole("button", { name: "初始化环境工作空间" })).toBeDisabled();
});
