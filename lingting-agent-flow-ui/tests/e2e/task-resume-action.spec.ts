import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 恢复工作流操作的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH 或 Git。
 * 用例只验证一件事：恢复入口是否只由服务端 `resume-eligibility` 的 `can_resume` 决定，
 * 是否按真实响应区分"请求已受理 / 被拒绝 / 结果无法确认 / 刷新失败"，并且从不自动
 * 重发恢复请求、也不顺带触发任务重试或容器处置。
 *
 * 这些用例不能证明真实恢复流程在容器内可用，只能证明 UI 对给定响应的处理。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    closeout: "unconfirmed",
    closeout_summary: "任务结论尚未闭环，仍需继续推进或人工核验",
    created_at: "2026-10-27T08:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "recovery_required",
    status_finished: false,
    status_terminal: false,
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/** 一份工作流执行记录样本，字段与 Go 端 `httpapi.WorkflowRunResponse` 一致 */
function sampleRun(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    code_saver_available: true,
    completed_steps: 1,
    last_completed_sequence: 1,
    plan_digest: "digest",
    revision: 3,
    run_id: "run-1",
    stage: "step_execution",
    status: "blocked",
    status_active: false,
    status_terminal: false,
    task_id: "task-1",
    total_steps: 2,
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}

/**
 * 一份续跑资格响应样本，字段与 Go 端 `httpapi.ResumeEligibilityResponse` 一致。
 *
 * `blocking` 与 `next_actions` 在服务端为空时序列化为 `null`，这里保持一致。
 */
function sampleEligibility(overrides: Record<string, unknown> = {}) {
  return {
    blocking: null,
    can_resume: true,
    code_save_action: "proceed",
    code_save_records: [],
    code_save_summary: "代码保存记录中没有结果不明的外部副作用",
    next_actions: ["agentflow task resume task-1"],
    run: sampleRun(),
    summary: "没有阻塞项，可以再次请求恢复推进",
    task_id: "task-1",
    task_status: "recovery_required",
    ...overrides,
  };
}

/** 一条"需要关注的代码保存记录"样本 */
function sampleResumeCodeSave(overrides: Record<string, unknown> = {}) {
  return {
    action: "verify",
    commit_status: "succeeded",
    error_class: "code_save_indeterminate",
    merge_status: "not_requested",
    persist_status: "persisted",
    push_status: "unknown",
    record_id: "record-1",
    recovery_status: "unverified",
    summary: "代码保存记录仍存在结果不明的外部副作用，必须先做一次只读核验",
    task_branch: "agent/task-1",
    ...overrides,
  };
}

/** 健康检查响应，使页面外壳处于已连接状态 */
async function stubHealth(page: Page) {
  await page.route("**/api/v1/health", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        checks: [
          { name: "http_api", status: "pass" },
          { name: "database", status: "pass" },
        ],
        checked_at: "2026-10-27T08:00:00Z",
        status: "ok",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截任务列表；可传入多个任务用于验证切换任务的行为 */
async function stubTaskList(page: Page, tasks = [sampleTask()]) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: tasks, size: 20, total: tasks.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截指定任务的详情 */
async function stubTaskDetail(page: Page, task = sampleTask()) {
  await page.route(`**/api/v1/tasks/${task.id as string}`, (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      body: JSON.stringify(task),
      contentType: "application/json",
      status: 200,
    });
  });
}

/** 拦截工作流进度 */
async function stubWorkflow(page: Page, taskID = "task-1", run?: Record<string, unknown>) {
  await page.route(`**/api/v1/tasks/${taskID}/workflow`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        has_run: true,
        run: sampleRun({ task_id: taskID, ...run }),
        steps: [],
        task_id: taskID,
        task_status: "recovery_required",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截步骤列表：空计划 */
async function stubStepsEmpty(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/steps?*`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [], size: 100, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截代码保存记录列表、容器列表与事件时间线（本页其余只读面板） */
async function stubOtherPanels(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/code-saves`, (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      body: JSON.stringify({ records: [], total: 0 }),
      contentType: "application/json",
      status: 200,
    });
  });
  await page.route(`**/api/v1/tasks/${taskID}/containers`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ records: [], task_id: taskID, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/events?*`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ offset: 0, records: [], size: 20, task_id: taskID, total: 0 }),
      contentType: "application/json",
      status: 200,
    }),
  );
  await page.route(`**/api/v1/tasks/${taskID}/retry-eligibility`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        blocking: ["status_not_retryable"],
        can_retry: false,
        summary: "任务状态不允许重试",
        task_id: taskID,
        task_status: "recovery_required",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/**
 * 拦截续跑资格查询。
 *
 * `responder` 收到的调用次数可用于验证"刷新"是否真的重新请求；返回值决定响应。
 * 返回的 `urls` 用于断言请求路径与真实形状。
 */
async function stubEligibility(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const urls: string[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/resume-eligibility`, (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    urls.push(route.request().url());
    const result = responder(urls.length - 1);
    return route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 200,
    });
  });
  return urls;
}

/** 记录恢复写请求的调用次数与请求体 */
async function stubResume(
  page: Page,
  responder: (callIndex: number) => { body: unknown; status?: number },
  taskID = "task-1",
) {
  const calls: { body: unknown; url: string }[] = [];
  await page.route(`**/api/v1/tasks/${taskID}/resume-workflow`, async (route: Route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const raw = route.request().postData();
    calls.push({
      body: raw ? (JSON.parse(raw) as unknown) : undefined,
      url: route.request().url(),
    });
    const result = responder(calls.length - 1);
    return route.fulfill({
      body: JSON.stringify(result.body),
      contentType: "application/json",
      status: result.status ?? 202,
    });
  });
  return calls;
}

/** 记录所有写请求，用于证明只读查询与禁用状态不会触发任何副作用 */
async function recordWrites(page: Page) {
  const writes: string[] = [];
  await page.route("**/api/v1/**", (route: Route) => {
    const method = route.request().method();
    if (method !== "GET" && method !== "HEAD") {
      writes.push(`${method} ${new URL(route.request().url()).pathname}`);
    }
    return route.fallback();
  });
  return writes;
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

/** 打开任务详情 */
async function openDetail(page: Page, name = "实现登录接口") {
  await page.goto("/tasks");
  await page.getByText(name).first().click();
  await expect(page.getByText("任务详情")).toBeVisible();
}

/** 恢复入口：只在服务端资格允许且快照属于当前任务时可用 */
function resumeButton(page: Page) {
  return page.getByRole("button", { name: /恢复工作流|恢\s*复\s*工\s*作\s*流/ });
}

/** 公共前置：任务列表、详情、工作流、步骤与其它面板 */
async function stubPage(page: Page, tasks = [sampleTask()]) {
  await stubTaskList(page, tasks);
  for (const task of tasks) {
    const taskID = task.id as string;
    await stubTaskDetail(page, task);
    await stubWorkflow(page, taskID);
    await stubStepsEmpty(page, taskID);
    await stubOtherPanels(page, taskID);
  }
}

test.describe("恢复入口与资格联动", () => {
  test("资格允许时用户显式点击恢复，页面按真实响应展示结果", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    const calls = await stubResume(page, () => ({
      body: sampleRun({
        status: "running",
        status_active: true,
        status_terminal: false,
        started_at: "2026-10-27T08:00:00Z",
      }),
    }));

    await openDetail(page);

    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await expect(resumeButton(page)).toBeEnabled();

    await resumeButton(page).click();

    await expect(page.getByText("恢复请求已受理，工作流已开始推进")).toBeVisible();
    await expect(page.getByText(/执行记录：run-1/)).toBeVisible();
    await expect(page.getByText(/执行状态：执行中/)).toBeVisible();
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toContain("/api/v1/tasks/task-1/resume-workflow");
    // 只发送服务端支持的参数：执行记录标识来自资格快照
    expect(calls[0]?.body).toEqual({ run_id: "run-1" });
    // 请求已受理不被表述为任务完成
    await expect(page.getByText(/不代表任务已完成/).first()).toBeVisible();
  });

  test("资格被阻塞时展示阻塞原因与下一步建议，且不能点击恢复", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["code_save_indeterminate", "future_resume_blocker"],
        can_resume: false,
        code_save_action: "verify",
        code_save_records: [sampleResumeCodeSave()],
        next_actions: ["agentflow code verify --record record-1"],
        summary: "存在阻塞项（code_save_indeterminate）",
      }),
    }));
    const calls = await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);

    await expect(
      page.getByText("当前存在阻塞，不能按当前条件直接续跑", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("代码保存结果不确定", { exact: true })).toBeVisible();
    // 未知阻塞分类原样展示
    await expect(page.getByText("future_resume_blocker").first()).toBeVisible();
    await expect(page.getByText(/未定义/).first()).toBeVisible();
    // next_actions 原文展示且不自动执行
    await expect(page.getByText("agentflow code verify --record record-1")).toBeVisible();
    await expect(page.getByText(/页面不会自动运行/).first()).toBeVisible();

    await expect(resumeButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  test("资格查询失败时恢复入口保持禁用", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({
      body: { error: { code: "internal_error", message: "续跑资格服务未初始化" } },
      status: 503,
    }));
    const calls = await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);

    await expect(page.getByText("查询续跑资格失败")).toBeVisible();
    await expect(page.getByText(/续跑资格服务未初始化/)).toBeVisible();
    await expect(page.getByText(/续跑资格当前无法确认/)).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toHaveCount(0);
    await expect(resumeButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  test("资格刷新失败时保留旧快照但恢复入口不可用", async ({ page }) => {
    await stubPage(page);
    let failing = false;
    await stubEligibility(page, () =>
      failing
        ? { body: { error: { code: "internal_error", message: "服务内部错误" } }, status: 500 }
        : { body: sampleEligibility({ can_resume: true }) },
    );
    const calls = await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await expect(resumeButton(page)).toBeEnabled();

    failing = true;
    await page.getByText("刷新续跑资格").click();

    await expect(page.getByText("续跑资格刷新失败")).toBeVisible();
    await expect(page.getByText(/不是最新状态/)).toBeVisible();
    // 旧快照仍展示供参考，但不再作为恢复授权
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await expect(resumeButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  test("服务端未返回执行记录时恢复入口禁用，不猜测标识", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({
      body: sampleEligibility({ can_resume: true, run: undefined }),
    }));
    const calls = await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);

    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await expect(resumeButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });
});

test.describe("恢复结果语义", () => {
  test("pending 时不把受理读成已推进", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    await stubResume(page, () => ({
      body: sampleRun({ status: "pending", status_active: false }),
    }));

    await openDetail(page);
    await resumeButton(page).click();

    await expect(page.getByText("恢复请求已受理，执行记录尚未开始推进")).toBeVisible();
    await expect(page.getByText("恢复请求已受理，工作流已开始推进")).toHaveCount(0);
  });

  test("服务端拒绝恢复时展示真实错误，不按先前资格显示成功", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    const calls = await stubResume(page, () => ({
      body: {
        error: { code: "state_conflict", message: "工作流执行已记录取消请求，不能恢复" },
      },
      status: 409,
    }));

    await openDetail(page);
    await resumeButton(page).click();

    await expect(page.getByText("恢复请求被服务端拒绝")).toBeVisible();
    await expect(page.getByText("工作流执行已记录取消请求，不能恢复")).toBeVisible();
    await expect(page.getByText("恢复请求已受理，工作流已开始推进")).toHaveCount(0);
    await page.waitForTimeout(500);
    expect(calls).toHaveLength(1);
  });

  test("请求结果无法确认时明确提示，且不自动重复提交", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    const calls = await stubResume(page, () => ({
      body: { error: { code: "internal_error", message: "服务内部错误" } },
      status: 500,
    }));

    await openDetail(page);
    await resumeButton(page).click();

    await expect(page.getByText("恢复请求被服务端拒绝")).toBeVisible();
    await expect(page.getByText("服务内部错误")).toBeVisible();
    await page.waitForTimeout(600);
    expect(calls).toHaveLength(1);
  });

  test("恢复请求成功但后续刷新失败时保留请求结果并单独提示刷新问题", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    await stubResume(page, () => ({
      body: sampleRun({ status: "running", status_active: true }),
    }));

    await openDetail(page);

    // 恢复之后让任务详情与工作流进度的刷新失败：已取得的恢复结论必须仍然可见。
    await page.route("**/api/v1/tasks/task-1", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      });
    });
    await page.route("**/api/v1/tasks/task-1/workflow", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      }),
    );

    await resumeButton(page).click();

    await expect(page.getByText("恢复请求已受理，工作流已开始推进")).toBeVisible();
    await expect(page.getByText("状态刷新失败").first()).toBeVisible();
    await expect(page.getByText(/上方结论仍然有效/)).toBeVisible();
    // 恢复结论没有被刷新失败抹掉
    await expect(page.getByText(/执行记录：run-1/)).toBeVisible();
  });
});

test.describe("竞态与边界", () => {
  test("切换任务后旧任务的迟到资格响应不污染当前页面", async ({ page }) => {
    const first = sampleTask();
    const second = sampleTask({ id: "task-2", name: "补充接口测试" });
    await stubPage(page, [first, second]);
    await stubEligibility(
      page,
      () => ({ body: sampleEligibility({ can_resume: true, summary: "第一个任务允许恢复" }) }),
      "task-1",
    );
    // 第二个任务故意延迟：先让第一个任务的结论渲染出来，再切换任务。
    await stubEligibility(
      page,
      () => ({
        body: sampleEligibility({
          blocking: ["container_not_running"],
          can_resume: false,
          next_actions: null,
          summary: "第二个任务存在阻塞",
          task_id: "task-2",
        }),
      }),
      "task-2",
    );
    const calls = await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);
    await expect(page.getByText("第一个任务允许恢复")).toBeVisible();

    await page.getByText("补充接口测试").first().click();
    await expect(page.getByText("第二个任务存在阻塞")).toBeVisible();
    await expect(page.getByText("第一个任务允许恢复")).toHaveCount(0);
    await expect(page.getByText("执行容器未运行", { exact: true })).toBeVisible();
    await expect(resumeButton(page)).toBeDisabled();
    expect(calls).toHaveLength(0);
  });

  test("打开详情与点击刷新都不会自动发起恢复请求", async ({ page }) => {
    const writes = await recordWrites(page);
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await page.getByText("刷新续跑资格").click();
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await page.waitForTimeout(300);

    expect(writes).toEqual([]);
  });

  test("恢复操作不会触发任务重试、容器处置或代码保存核验", async ({ page }) => {
    const writes = await recordWrites(page);
    await stubPage(page);
    await stubEligibility(page, () => ({ body: sampleEligibility({ can_resume: true }) }));
    const calls = await stubResume(page, () => ({
      body: sampleRun({ status: "running", status_active: true }),
    }));

    await openDetail(page);
    await resumeButton(page).click();
    await expect(page.getByText("恢复请求已受理，工作流已开始推进")).toBeVisible();
    await page.waitForTimeout(400);

    // 恢复是唯一发出的写请求，且只发一次
    expect(calls).toHaveLength(1);
    expect(writes.filter((item) => item.includes("/retry"))).toEqual([]);
    expect(writes.filter((item) => item.includes("/containers/"))).toEqual([]);
    expect(writes.filter((item) => item.includes("verify-code-save"))).toEqual([]);
  });
});

test.describe("处置与核验之后的资格复核", () => {
  test("容器处置成功后不本地推断可恢复，必须重新查询服务端资格", async ({ page }) => {
    await stubPage(page);
    // 处置前：容器缺失导致阻塞；处置后：服务端才会给出允许恢复。
    let disposed = false;
    const urls = await stubEligibility(page, () =>
      disposed
        ? { body: sampleEligibility({ can_resume: true, summary: "旧容器已处置，可以恢复" }) }
        : {
            body: sampleEligibility({
              blocking: ["container_missing"],
              can_resume: false,
              next_actions: null,
              summary: "执行容器不存在",
            }),
          },
    );
    const calls = await stubResume(page, () => ({ body: sampleRun() }));
    await page.route("**/api/v1/tasks/task-1/containers", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          records: [sampleContainer()],
          task_id: "task-1",
          total: 1,
        }),
        contentType: "application/json",
        status: 200,
      }),
    );
    await page.route("**/api/v1/tasks/task-1/containers/container-1/detach", (route: Route) => {
      disposed = true;
      return route.fulfill({
        body: JSON.stringify({
          action: "detach",
          container: sampleContainer({ detached: true, state: "detached" }),
          detail: "容器 container-1 已脱离 AgentFlow 自动管理",
          outcome: "completed",
          task_id: "task-1",
        }),
        contentType: "application/json",
        status: 200,
      });
    });

    await openDetail(page);
    await expect(page.getByText("执行容器不存在", { exact: true }).first()).toBeVisible();
    await expect(resumeButton(page)).toBeDisabled();

    // 处置动作本身不改变续跑资格：入口是否可用仍只能来自服务端结论。
    const before = urls.length;
    await page
      .getByRole("button", { name: /脱离管理|脱\s*离\s*管\s*理/ })
      .first()
      .click();
    await page.getByRole("button", { name: /确认脱离管理|确认脱离/ }).click();
    // 处置结束后必须重新查询服务端资格，新结论生效后才可能启用入口。
    await expect
      .poll(() => urls.length, { message: "处置后应重新查询续跑资格" })
      .toBeGreaterThan(before);
    await expect(page.getByText("旧容器已处置，可以恢复")).toBeVisible();
    await expect(resumeButton(page)).toBeEnabled();
    // 处置动作本身不会触发恢复请求
    expect(calls).toHaveLength(0);
  });

  test("代码保存核验完成后不本地放行恢复，仍需服务端资格结论", async ({ page }) => {
    await stubPage(page);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["code_save_indeterminate"],
        can_resume: false,
        code_save_action: "verify",
        code_save_records: [sampleResumeCodeSave()],
        next_actions: ["agentflow code verify --record record-1"],
        summary: "代码保存结果不确定",
      }),
    }));
    const calls = await stubResume(page, () => ({ body: sampleRun() }));

    await openDetail(page);
    await expect(page.getByText("代码保存结果不确定", { exact: true }).first()).toBeVisible();
    await expect(resumeButton(page)).toBeDisabled();
    // 页面没有因为展示 next_actions 就自动执行核验
    const writes = await recordWrites(page);
    await page.waitForTimeout(300);
    expect(writes).toEqual([]);
    expect(calls).toHaveLength(0);
  });
});

/** 一条容器记录样本，字段与 Go 端 `httpapi.TaskContainerResponse` 一致 */
function sampleContainer(overrides: Record<string, unknown> = {}) {
  return {
    created_at: "2026-10-27T08:00:00Z",
    detached: false,
    docker_state: "running",
    end_reason: "",
    id: "container-1",
    name: "agentflow-container-1",
    origin: "current",
    state: "running",
    task_id: "task-1",
    updated_at: "2026-10-27T08:00:00Z",
    ...overrides,
  };
}
