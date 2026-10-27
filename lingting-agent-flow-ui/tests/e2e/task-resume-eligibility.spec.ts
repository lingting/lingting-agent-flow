import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 续跑资格与阻塞原因的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker、SSH 或 Git。
 * 用例只验证一件事：页面是否如实呈现服务端给出的资格结论、阻塞项、代码保存判定与
 * 下一步建议，并且不会把它们合并、隐藏或自动执行。
 *
 * 这些用例不能证明真实远程容器的恢复条件，只能证明 UI 对给定响应的处理。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    closeout: "unconfirmed",
    closeout_summary: "任务结论尚未闭环，仍需继续推进或人工核验",
    created_at: "2026-10-26T08:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "recovery_required",
    status_finished: false,
    status_terminal: false,
    updated_at: "2026-10-26T08:00:00Z",
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
    updated_at: "2026-10-26T08:00:00Z",
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
    run: sampleRun({ run_id: "run-eligibility-1" }),
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

/** 一条 `GET /tasks/{id}/code-saves` 记录样本，字段与 M3-5 面板一致 */
function sampleCodeSaveRecord(overrides: Record<string, unknown> = {}) {
  return {
    base_branch: "main",
    commit_sha: "1".repeat(40),
    commit_status: "succeeded",
    container_id: "container-1",
    created_at: "2026-10-26T08:00:00Z",
    id: "record-1",
    merge_enabled: false,
    merge_status: "not_requested",
    persist_status: "persisted",
    push_status: "unknown",
    recovery_status: "unverified",
    remote_name: "origin",
    stage: "push",
    task_branch: "agent/task-1",
    task_id: "task-1",
    updated_at: "2026-10-26T08:00:12Z",
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
        checked_at: "2026-10-26T08:00:00Z",
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
async function stubWorkflow(page: Page, taskID = "task-1") {
  await page.route(`**/api/v1/tasks/${taskID}/workflow`, (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        has_run: true,
        run: sampleRun({ task_id: taskID }),
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

/** 拦截代码保存记录列表 */
async function stubCodeSaves(page: Page, records = [sampleCodeSaveRecord()]) {
  await page.route("**/api/v1/tasks/task-1/code-saves", (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      body: JSON.stringify({ records, total: records.length }),
      contentType: "application/json",
      status: 200,
    });
  });
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

/**
 * 记录所有写请求，用于证明只读资格查询不会触发恢复、核验或其他副作用。
 */
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

test.describe("续跑资格读取与结论展示", () => {
  test("打开任务详情时读取真实形状的 resume-eligibility", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    const urls = await stubEligibility(page, () => ({ body: sampleEligibility() }));

    await openDetail(page);

    await expect(page.getByText("续跑资格与阻塞原因")).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    expect(urls.length).toBeGreaterThanOrEqual(1);
    expect(urls[0]).toContain("/api/v1/tasks/task-1/resume-eligibility");
  });

  test("can_resume=true 时展示资格结论与服务端摘要，并说明是查询时快照", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({ body: sampleEligibility() }));

    await openDetail(page);

    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    await expect(page.getByText("没有阻塞项，可以再次请求恢复推进")).toBeVisible();
    // 明确说明这是快照，恢复时服务端会重新检查
    await expect(page.getByText(/真正执行恢复时服务端仍会重新检查前置条件/)).toBeVisible();
    await expect(page.getByText(/重新检查前置条件/).first()).toBeVisible();
    // 允许续跑不等于自动恢复
    await expect(page.getByText("当前存在阻塞，不能按当前条件直接续跑")).toHaveCount(0);
  });

  test("can_resume=false 且存在多个阻塞项时逐项展示", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["cancel_requested", "container_not_running", "prerequisite_unmet"],
        can_resume: false,
        code_save_action: "blocked",
        code_save_summary: "已核验但无法取得可信事实",
        next_actions: [
          "任务已请求取消：如需继续推进请先确认取消是否仍然适用，再重新启动任务",
          "agentflow code verify --record record-1",
        ],
        summary:
          "存在阻塞项（cancel_requested、container_not_running、prerequisite_unmet），请按 next_actions 处理后重试",
      }),
    }));

    await openDetail(page);

    await expect(
      page.getByText("当前存在阻塞，不能按当前条件直接续跑", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("任务已请求取消", { exact: true })).toBeVisible();
    await expect(page.getByText("执行容器未运行", { exact: true })).toBeVisible();
    await expect(page.getByText("必要前置条件未满足", { exact: true })).toBeVisible();
    await expect(
      page.getByText(/存在阻塞项（cancel_requested、container_not_running/),
    ).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toHaveCount(0);
  });

  test("容器缺失或不可达时展示准确阻塞原因，不显示允许续跑", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["container_missing", "container_unreachable"],
        can_resume: false,
        summary: "存在阻塞项（container_missing、container_unreachable）",
      }),
    }));

    await openDetail(page);

    await expect(page.getByText("执行容器不存在", { exact: true })).toBeVisible();
    await expect(page.getByText(/任务的执行容器不存在或已被移除/)).toBeVisible();
    await expect(page.getByText("无法访问执行容器", { exact: true })).toBeVisible();
    await expect(page.getByText(/无法访问执行容器（连接或核验失败）/)).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toHaveCount(0);
    await expect(
      page.getByText("当前存在阻塞，不能按当前条件直接续跑", { exact: true }),
    ).toBeVisible();
  });

  test("未知阻塞分类原样展示，不省略也不伪装成无阻塞", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["future_blocking_reason"],
        can_resume: false,
        next_actions: null,
        summary: "存在阻塞项（future_blocking_reason）",
      }),
    }));

    await openDetail(page);

    // 标签与原始代码都展示：未知分类既不被替换成中文，也不被隐藏
    await expect(page.getByText("future_blocking_reason").first()).toBeVisible();
    await expect(page.getByText(/未在当前版本中定义|未定义/).first()).toBeVisible();
    // 未知分类仍然算作阻塞，不能显示允许续跑
    await expect(page.getByText("当前查询结果允许继续推进")).toHaveCount(0);
  });

  test("run 缺省时明确说明未返回执行记录，不补造进度", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({ run: undefined, summary: "没有阻塞项" }),
    }));

    await openDetail(page);

    await expect(page.getByText(/服务端未返回工作流执行记录/)).toBeVisible();
    await expect(page.getByText("run-eligibility-1")).toHaveCount(0);
  });
});

test.describe("代码保存判定与需要关注的记录", () => {
  test("代码保存有未决副作用时展示记录、原因与后续动作", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, [sampleCodeSaveRecord()]);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["code_save_indeterminate"],
        can_resume: false,
        code_save_action: "verify",
        code_save_records: [sampleResumeCodeSave()],
        code_save_summary: "代码保存记录仍存在结果不明的外部副作用，必须先做一次只读核验",
        next_actions: ["agentflow code verify --record record-1"],
        summary: "存在阻塞项（code_save_indeterminate）",
      }),
    }));

    await openDetail(page);

    // 任务级判定
    await expect(page.getByText("需先核验").first()).toBeVisible();
    await expect(page.getByText(/这是「需要核验」，不是「已经核验」/).first()).toBeVisible();
    // 记录标识与分支
    await expect(page.getByText("agent/task-1").first()).toBeVisible();
    await expect(page.getByText("record-1").first()).toBeVisible();
    // 记录级判定与原因
    await expect(page.getByText(/必须先做一次只读核验/).first()).toBeVisible();
    await expect(page.getByText("code_save_indeterminate").first()).toBeVisible();
    // 后续动作原文
    await expect(page.getByText("agentflow code verify --record record-1")).toBeVisible();
    await expect(page.getByText(/需由你在命令行显式执行/)).toBeVisible();
  });

  test("三个 Git 阶段与记录维度两个状态分别展示，不被合并", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, [sampleCodeSaveRecord({ push_status: "unknown" })]);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        can_resume: false,
        code_save_action: "verify",
        code_save_records: [
          sampleResumeCodeSave({
            commit_status: "succeeded",
            merge_status: "conflict",
            persist_status: "unconfirmed",
            push_status: "unknown",
            recovery_status: "unverifiable",
          }),
        ],
      }),
    }));

    await openDetail(page);

    // 提交 / 推送 / 合并三个独立标签（"推送结果不确定，需核验" 在 M3-5 面板也会出现）
    await expect(page.getByText("提交成功").first()).toBeVisible();
    await expect(page.getByText("推送结果不确定，需核验").first()).toBeVisible();
    await expect(page.getByText("合并冲突").first()).toBeVisible();
    // 记录写入状态与核验结论是另外两个事实
    await expect(page.getByText("记录写入未确认").first()).toBeVisible();
    await expect(page.getByText("无法取得可信结论").first()).toBeVisible();
    // 不出现把三阶段合并的总体状态
    await expect(page.getByText("代码保存成功")).toHaveCount(0);
    await expect(page.getByText("代码保存失败")).toHaveCount(0);
  });

  test("已有代码保存记录与 M3-5 面板中的真实记录标识保持一致", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, [sampleCodeSaveRecord({ id: "record-42" })]);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        can_resume: false,
        code_save_action: "verify",
        code_save_records: [sampleResumeCodeSave({ record_id: "record-42" })],
      }),
    }));

    await openDetail(page);

    await expect(page.getByText("代码保存记录", { exact: true })).toBeVisible();
    await expect(page.getByText("续跑资格与阻塞原因")).toBeVisible();
    // 两个面板使用同一个真实记录标识
    await expect(page.getByText("record-42")).toHaveCount(2);
  });

  test("next_actions 逐项显示服务端原文，没有自动执行其中任何指令", async ({ page }) => {
    const writes = await recordWrites(page);
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        can_resume: false,
        next_actions: [
          "agentflow code verify --record record-1",
          "agentflow code verify --record record-2",
          "核验后若仍为 unverifiable，请人工检查容器内仓库与远程分支后再决定是否重试",
        ],
        summary: "存在阻塞项",
      }),
    }));

    await openDetail(page);

    await expect(page.getByText("agentflow code verify --record record-1")).toBeVisible();
    await expect(page.getByText("agentflow code verify --record record-2")).toBeVisible();
    await expect(page.getByText(/核验后若仍为 unverifiable/)).toBeVisible();
    await expect(page.getByText(/页面不会自动运行/).first()).toBeVisible();

    // 页面没有发出任何写请求
    expect(writes).toEqual([]);
  });

  test("next_actions 为空时明确说明服务端没有给出建议", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({ can_resume: false, next_actions: null }),
    }));

    await openDetail(page);

    await expect(page.getByText(/服务端没有提供后续动作建议/)).toBeVisible();
    await expect(page.getByText(/agentflow task resume/)).toHaveCount(0);
  });
});

test.describe("失败、刷新与竞态", () => {
  test("查询失败显示错误与重试入口，而非默认显示允许续跑", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: { error: { code: "not_found", message: "任务不存在: task-1" } },
      status: 404,
    }));

    await openDetail(page);

    await expect(page.getByText("查询续跑资格失败")).toBeVisible();
    await expect(page.getByText("任务不存在: task-1")).toBeVisible();
    // 页面上有多个"重试"按钮（续跑资格与重试资格各自的重试入口），这里断言第一个可见即可。
    await expect(page.getByRole("button", { name: /^重\s*试$/ }).first()).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toHaveCount(0);
    await expect(page.getByText("当前存在阻塞，不能按当前条件直接续跑")).toHaveCount(0);
  });

  test("刷新失败时保留上一次真实结果并明确提示不是最新查询", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    // 首次成功；测试在结论可见后切换为失败，避免 StrictMode 的重复请求让索引不确定。
    let failing = false;
    const urls = await stubEligibility(page, () =>
      failing
        ? {
            body: { error: { code: "internal_error", message: "服务内部错误" } },
            status: 500,
          }
        : { body: sampleEligibility({ summary: "没有阻塞项，可以再次请求恢复推进" }) },
    );

    await openDetail(page);
    await expect(page.getByText("没有阻塞项，可以再次请求恢复推进")).toBeVisible();

    failing = true;
    await page.getByText("刷新续跑资格").click();

    await expect(page.getByText("续跑资格刷新失败")).toBeVisible();
    await expect(page.getByText(/服务内部错误/)).toBeVisible();
    await expect(page.getByText(/不是最新状态/)).toBeVisible();
    // 旧结论仍然可见，不被清空成"没有阻塞"或"允许续跑"
    await expect(page.getByText("没有阻塞项，可以再次请求恢复推进")).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    expect(urls.length).toBeGreaterThanOrEqual(2);
  });

  test("切换任务不会把上一任务的资格结果混入当前任务", async ({ page }) => {
    const first = sampleTask();
    const second = sampleTask({ id: "task-2", name: "补充接口测试", status: "developing" });
    await stubTaskList(page, [first, second]);
    await stubTaskDetail(page, first);
    await stubTaskDetail(page, second);
    await stubWorkflow(page, "task-1");
    await stubWorkflow(page, "task-2");
    await stubStepsEmpty(page, "task-1");
    await stubStepsEmpty(page, "task-2");
    await stubCodeSaves(page, []);

    // 第一个任务：明确阻塞；第二个任务：允许续跑
    await stubEligibility(
      page,
      () => ({
        body: sampleEligibility({
          blocking: ["container_missing"],
          can_resume: false,
          next_actions: null,
          summary: "第一个任务的阻塞结论",
          task_id: "task-1",
        }),
      }),
      "task-1",
    );
    await stubEligibility(
      page,
      () => ({
        body: sampleEligibility({
          blocking: null,
          can_resume: true,
          next_actions: ["agentflow task resume task-2"],
          summary: "第二个任务可以续跑",
          task_id: "task-2",
        }),
      }),
      "task-2",
    );

    await openDetail(page);
    await expect(page.getByText("第一个任务的阻塞结论")).toBeVisible();
    await expect(page.getByText("执行容器不存在", { exact: true })).toBeVisible();

    // 切换到第二个任务
    await page.getByText("补充接口测试").first().click();
    await expect(page.getByText("第二个任务可以续跑")).toBeVisible();
    await expect(page.getByText("当前查询结果允许继续推进")).toBeVisible();
    // 上一任务的结论不再出现
    await expect(page.getByText("第一个任务的阻塞结论")).toHaveCount(0);
    await expect(page.getByText("执行容器不存在", { exact: true })).toHaveCount(0);
  });

  test("查询过程不会自动调用恢复、核验或其他写接口", async ({ page }) => {
    const writes = await recordWrites(page);
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubStepsEmpty(page);
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({
        blocking: ["code_save_indeterminate"],
        can_resume: false,
        code_save_action: "verify",
        code_save_records: [sampleResumeCodeSave()],
        next_actions: ["agentflow code verify --record record-1"],
      }),
    }));

    await openDetail(page);
    await expect(page.getByText("续跑资格与阻塞原因")).toBeVisible();
    await page.getByText("刷新续跑资格").click();
    await expect(page.getByText("续跑资格与阻塞原因")).toBeVisible();

    expect(writes.filter((item) => item.includes("resume-workflow"))).toEqual([]);
    expect(writes.filter((item) => item.includes("verify-code-save"))).toEqual([]);
    expect(writes.filter((item) => item.includes("execute"))).toEqual([]);
    expect(writes).toEqual([]);
  });

  test("任务详情加载失败时不展示前一个任务的资格结果", async ({ page }) => {
    const first = sampleTask();
    const second = sampleTask({ id: "task-2", name: "补充接口测试" });
    await stubTaskList(page, [first, second]);
    await stubTaskDetail(page, first);
    await stubWorkflow(page, "task-1");
    await stubStepsEmpty(page, "task-1");
    await stubCodeSaves(page, []);
    await stubEligibility(page, () => ({
      body: sampleEligibility({ summary: "第一个任务可以续跑" }),
    }));

    // 第二个任务的详情请求失败
    await page.route("**/api/v1/tasks/task-2", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      });
    });

    await openDetail(page);
    await expect(page.getByText("第一个任务可以续跑")).toBeVisible();

    await page.getByText("补充接口测试").first().click();
    await expect(page.getByText("任务详情")).toHaveCount(0);
    await expect(page.getByText("第一个任务可以续跑")).toHaveCount(0);
    await expect(page.getByText("续跑资格与阻塞原因")).toHaveCount(0);
  });
});
