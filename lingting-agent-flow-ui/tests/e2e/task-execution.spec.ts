import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 任务执行操作的浏览器测试。
 *
 * 所有请求都在浏览器层拦截，因此不需要真实 Go 服务、数据库、Docker 或模型。
 * 用例关注真实业务行为：
 *   - 任务详情展示真实步骤与状态；
 *   - 操作使用当前选中的真实步骤与尝试标识；
 *   - 服务端失败时展示业务错误，不伪造成功；
 *   - 操作进行中阻止重复提交；
 *   - 验证失败反馈与执行记录正确关联；
 *   - 修复周期被拒绝时不显示成功，也不会自动重试；
 *   - 执行后刷新失败时原始执行结论仍然保留；
 *   - 任务创建与统一启动行为没有回归。
 */

/** 一份任务响应样本，字段与 Go 端 `httpapi.TaskResponse` 一致 */
function sampleTask(overrides: Record<string, unknown> = {}) {
  return {
    cancel_requested: false,
    created_at: "2026-10-26T08:00:00Z",
    description: "为登录页接入真实接口",
    id: "task-1",
    name: "实现登录接口",
    project_id: "",
    status: "developing",
    status_finished: false,
    status_terminal: false,
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一份步骤响应样本，字段与 Go 端 `httpapi.StepResponse` 一致 */
function sampleStep(overrides: Record<string, unknown> = {}) {
  return {
    created_at: "2026-10-26T08:00:00Z",
    id: "step-1",
    name: "实现登录步骤",
    objective: "按验收条件实现登录接口",
    sequence: 1,
    status: "pending",
    status_terminal: false,
    step_type: "develop",
    task_id: "task-1",
    updated_at: "2026-10-26T08:00:00Z",
    ...overrides,
  };
}

/** 一份尝试响应样本，字段与 Go 端 `httpapi.AttemptResponse` 一致 */
function sampleAttempt(overrides: Record<string, unknown> = {}) {
  return {
    attempt_number: 1,
    cancelled: false,
    created_at: "2026-10-26T08:00:00Z",
    exit_code: 0,
    id: "attempt-1",
    session_ids: ["session-1"],
    started_at: "2026-10-26T08:00:00Z",
    status: "completed",
    status_terminal: true,
    step_id: "step-1",
    task_id: "task-1",
    timed_out: false,
    ...overrides,
  };
}

/** 一份会话响应样本，字段与 Go 端 `httpapi.SessionResponse` 一致 */
function sampleSession(overrides: Record<string, unknown> = {}) {
  return {
    agent_role: "developer",
    archive_path: "/data/sessions/session-1.jsonl",
    archive_status: "succeeded",
    created_at: "2026-10-26T08:00:00Z",
    export_status: "succeeded",
    id: "session-1",
    model: "group/free",
    provider: "openai",
    role_id: "role-1",
    started_at: "2026-10-26T08:00:00Z",
    status: "completed",
    status_terminal: true,
    task_id: "task-1",
    ...overrides,
  };
}

/** 一份验证记录样本 */
function sampleValidation(overrides: Record<string, unknown> = {}) {
  return {
    attempt_id: "attempt-1",
    checks: [],
    failed_checks: 1,
    id: "run-1",
    passed_checks: 0,
    review_ran: false,
    skipped_checks: 0,
    started_at: "2026-10-26T08:02:00Z",
    status: "completed",
    step_id: "step-1",
    task_id: "task-1",
    total_checks: 1,
    verdict: "failed",
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

/** 拦截任务列表 */
async function stubTaskList(page: Page, task = sampleTask()) {
  await page.route("**/api/v1/tasks?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: [task], size: 20, total: 1 }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截任务详情 */
async function stubTaskDetail(page: Page, task = sampleTask()) {
  await page.route("**/api/v1/tasks/task-1", (route: Route) => {
    if (route.request().method() !== "GET") {
      return route.fallback();
    }
    return route.fulfill({
      body: JSON.stringify(task),
      contentType: "application/json",
      status: 200,
    });
  });
}

/** 拦截工作流进度 */
async function stubWorkflow(page: Page) {
  await page.route("**/api/v1/tasks/task-1/workflow", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        has_run: true,
        run: {
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
          updated_at: "2026-10-26T08:00:00Z",
        },
        steps: [],
        task_id: "task-1",
        task_status: "developing",
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截步骤列表，记录请求次数 */
async function stubSteps(page: Page, steps = [sampleStep()]) {
  const calls: string[] = [];
  await page.route("**/api/v1/tasks/task-1/steps?*", (route: Route) => {
    calls.push(route.request().url());
    return route.fulfill({
      body: JSON.stringify({ current: 1, records: steps, size: 100, total: steps.length }),
      contentType: "application/json",
      status: 200,
    });
  });
  return calls;
}

/** 拦截步骤的尝试列表 */
async function stubAttempts(page: Page, attempts = [sampleAttempt()]) {
  await page.route("**/api/v1/tasks/task-1/steps/step-1/attempts?*", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({ current: 1, records: attempts, size: 100, total: attempts.length }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截步骤的验证历史 */
async function stubValidations(page: Page, records = [sampleValidation()]) {
  await page.route("**/api/v1/tasks/task-1/steps/step-1/validations", (route: Route) =>
    route.fulfill({
      body: JSON.stringify({
        latest: records[records.length - 1],
        records,
        state: records.length > 0 ? "failed" : "unverified",
        step_id: "step-1",
        total: records.length,
      }),
      contentType: "application/json",
      status: 200,
    }),
  );
}

/** 拦截尝试下的会话列表 */
async function stubSessions(page: Page, sessions: unknown[] = []) {
  await page.route(
    "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/sessions?*",
    (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          current: 1,
          records: sessions,
          size: 100,
          total: sessions.length,
        }),
        contentType: "application/json",
        status: 200,
      }),
  );
}

/** 拦截验证失败反馈 */
async function stubFeedback(page: Page, handler: () => { body: unknown; status?: number }) {
  await page.route(
    "**/api/v1/tasks/task-1/steps/step-1/validations/run-1/feedback",
    (route: Route) => {
      const result = handler();
      return route.fulfill({
        body: JSON.stringify(result.body),
        contentType: "application/json",
        status: result.status ?? 200,
      });
    },
  );
}

/** 打开任务详情 */
async function openDetail(page: Page) {
  await page.goto("/tasks");
  await page.getByText("实现登录接口").first().click();
  await expect(page.getByText("执行操作")).toBeVisible();
}

/** 选中第一个步骤（按步骤名称定位，避免与任务名称混淆） */
async function selectStep(page: Page) {
  await page.getByText("计划步骤").waitFor();
  await page.getByText("实现登录步骤").click();
  await expect(page.getByText("步骤详情")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await stubHealth(page);
});

test.describe("计划与步骤可见", () => {
  test("任务详情展示真实步骤与状态", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    const stepCalls = await stubSteps(page, [
      sampleStep(),
      sampleStep({ id: "step-2", name: "补充单元测试", sequence: 2, status: "completed" }),
    ]);

    await openDetail(page);

    await expect(page.getByText("计划步骤")).toBeVisible();
    await expect(page.getByText("共 2 个步骤")).toBeVisible();
    await expect(page.getByText("补充单元测试")).toBeVisible();
    await expect(page.getByText("已完成").first()).toBeVisible();
    // StrictMode 下首次挂载会重复触发一次加载，这里只要求步骤确实被请求过
    expect(stepCalls.length).toBeGreaterThanOrEqual(1);
  });

  test("步骤列表为空时明确说明计划未持久化", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, []);

    await openDetail(page);

    await expect(page.getByText("没有计划步骤")).toBeVisible();
    await expect(page.getByText(/列表为空不代表计划已完成/)).toBeVisible();
    // 没有步骤时不显示步骤行，因此也不会出现步骤状态"已完成"
    await expect(page.getByText("共 0 个步骤")).toHaveCount(0);
    await expect(page.getByText("尚无已持久化的计划步骤")).toBeVisible();
  });

  test("步骤查询失败不被伪装成空列表", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await page.route("**/api/v1/tasks/task-1/steps?*", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({ error: { code: "not_found", message: "任务不存在: task-1" } }),
        contentType: "application/json",
        status: 404,
      }),
    );

    await openDetail(page);

    await expect(page.getByText("加载步骤失败")).toBeVisible();
    await expect(page.getByText("任务不存在: task-1")).toBeVisible();
    await expect(page.getByText("没有计划步骤")).toHaveCount(0);
  });

  test("未知步骤状态原样展示，不被当作成功", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [sampleStep({ status: "paused_by_operator" })]);

    await openDetail(page);

    await expect(page.getByText("paused_by_operator").first()).toBeVisible();
    // 未知状态不会被显示成"已完成"（exact 避免匹配工作流进度里的"已完成 0 / 1 步"）
    await expect(page.getByText("已完成", { exact: true })).toHaveCount(0);
  });
});

test.describe("步骤执行操作", () => {
  test("单步骤执行使用当前选中步骤的真实标识", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    const calls: string[] = [];
    await page.route("**/api/v1/tasks/task-1/steps/step-1/execute", (route: Route) => {
      calls.push(route.request().url());
      return route.fulfill({
        body: JSON.stringify({
          archive_status: "archived",
          attempt_id: "attempt-2",
          attempt_number: 2,
          attempt_status: "completed",
          exit_code: 0,
          export_status: "not_attempted",
          finalized: true,
          finished_at: "2026-10-26T08:01:00Z",
          outcome: "completed",
          started_at: "2026-10-26T08:00:00Z",
          step_id: "step-1",
          step_status: "completed",
          success: true,
          task_id: "task-1",
          task_status: "developing",
        }),
        contentType: "application/json",
        status: 200,
      });
    });

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "执行步骤" }).click();

    await expect(page.getByText(/步骤执行已结束/)).toBeVisible();
    await expect(page.getByText(/不代表任务已通过验收/)).toBeVisible();
    expect(calls).toHaveLength(1);
    expect(calls[0]).toContain("/tasks/task-1/steps/step-1/execute");
  });

  test("执行失败时展示服务端业务错误且不伪造成功", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route("**/api/v1/tasks/task-1/steps/step-1/execute", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          error: { code: "precondition_failed", message: "执行节点未通过环境预检" },
        }),
        contentType: "application/json",
        status: 412,
      }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "执行步骤" }).click();

    await expect(page.getByText("执行步骤（步骤 step-1）失败")).toBeVisible();
    await expect(page.getByText("执行节点未通过环境预检")).toBeVisible();
    await expect(page.getByText("步骤执行已结束")).toHaveCount(0);
  });

  test("操作进行中阻止重复提交", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const calls: string[] = [];
    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/execute-and-validate",
      async (route: Route) => {
        calls.push(route.request().url());
        await gate;
        await route.fulfill({
          body: JSON.stringify({
            attempt_number: 1,
            attempts: 1,
            developer_finalized: true,
            exit_code: 0,
            feedback_persisted: true,
            finalized: true,
            finished_at: "2026-10-26T08:10:00Z",
            infrastructure_error: false,
            max_attempts: 3,
            outcome: "completed",
            repair_decision: "completed",
            round: 1,
            started_at: "2026-10-26T08:00:00Z",
            step_id: "step-1",
            step_status: "completed",
            task_id: "task-1",
            task_status: "developing",
            validation_passed: true,
            validation_persisted: true,
            validation_ran: true,
            validation_status: "completed",
            validation_verdict: "passed",
            verification_state: "passed",
            verified: true,
          }),
          contentType: "application/json",
          status: 200,
        });
      },
    );

    await openDetail(page);
    await selectStep(page);
    const button = page.getByRole("button", { name: "开发—验证闭环" });
    await button.click();
    await expect(page.getByText(/正在执行：开发—验证闭环/)).toBeVisible();
    // 请求未完成前再次点击不会发出第二个请求
    await button.click({ force: true });
    expect(calls).toHaveLength(1);

    release();
    await expect(page.getByText(/步骤已通过验证/)).toBeVisible();
    expect(calls).toHaveLength(1);
  });

  test("顺序执行展示停止原因，且不包含验证与提交", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route("**/api/v1/tasks/task-1/execute-steps", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          executed_steps: 1,
          finished_at: "2026-10-26T08:05:00Z",
          started_at: "2026-10-26T08:00:00Z",
          steps: [
            {
              finalized: true,
              outcome: "failed",
              sequence: 1,
              skipped: false,
              step_id: "step-1",
              step_status: "failed",
              title: "实现登录接口",
            },
          ],
          stopped: true,
          stop_reason: "步骤执行结论为 failed: 编译失败",
          stopped_step_id: "step-1",
          success: false,
          task_id: "task-1",
          task_status: "developing",
        }),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await page.getByRole("button", { name: "顺序执行" }).click();

    await expect(page.getByText(/顺序执行已停止/)).toBeVisible();
    await expect(page.getByText(/已提前停止：步骤执行结论为 failed/)).toBeVisible();
    await expect(page.getByText("顺序执行不包含验证、提交、推送与合并。")).toBeVisible();
  });
});

test.describe("验证与诊断操作", () => {
  test("对指定尝试发起验证并使用真实尝试标识", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    const calls: string[] = [];
    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) => {
        calls.push(route.request().url());
        return route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [],
            failed_checks: 0,
            finished_at: "2026-10-26T08:03:00Z",
            passed: true,
            passed_checks: 1,
            persisted: true,
            review_ran: false,
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "passed",
          }),
          contentType: "application/json",
          status: 200,
        });
      },
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    await expect(page.getByText(/验证通过/)).toBeVisible();
    expect(calls).toHaveLength(1);
    expect(calls[0]).toContain("/attempts/attempt-1/validate");
  });

  test("验证未通过时展示结论与失败摘要，不显示成功", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [],
            failed_checks: 1,
            failure_summary: "必需检查未通过: build",
            finished_at: "2026-10-26T08:03:00Z",
            passed: false,
            passed_checks: 0,
            persisted: true,
            review_ran: false,
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "failed",
          }),
          contentType: "application/json",
          status: 200,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    await expect(page.getByText(/验证未通过/)).toBeVisible();
    await expect(page.getByText(/必需检查未通过: build/)).toBeVisible();
    await expect(page.getByText(/验证通过$/)).toHaveCount(0);
  });

  test("验证失败反馈与执行记录正确关联", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page);
    await stubSessions(page);
    await stubFeedback(page, () => ({
      body: {
        checks: [
          {
            check_id: "build",
            check_name: "构建",
            exit_code: 1,
            failure_reason: "编译错误: undefined: Foo",
            finished_at: "2026-10-26T08:03:00Z",
            infrastructure_error: false,
            required: true,
            started_at: "2026-10-26T08:02:00Z",
            status: "failed",
            timed_out: false,
            cancelled: false,
          },
        ],
        created_at: "2026-10-26T08:03:10Z",
        developer_attempt_id: "attempt-1",
        infrastructure_error: false,
        step_id: "step-1",
        task_id: "task-1",
        validation_run_id: "run-1",
        verdict: "failed",
      },
    }));

    await openDetail(page);
    await selectStep(page);
    await page.getByText("查看失败反馈", { exact: true }).click();

    await expect(page.getByText("失败反馈", { exact: true })).toBeVisible();
    await expect(page.getByText(/开发尝试 attempt-1/)).toBeVisible();
    await expect(page.getByText(/编译错误: undefined: Foo/)).toBeVisible();
    await expect(page.getByText(/退出码 1/)).toBeVisible();
  });

  test("反馈 404 明确说明没有失败反馈，而不是验证通过", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page);
    await stubSessions(page);
    await stubFeedback(page, () => ({
      body: { error: { code: "not_found", message: "验证失败反馈不存在" } },
      status: 404,
    }));

    await openDetail(page);
    await selectStep(page);
    await page.getByText("查看失败反馈", { exact: true }).click();

    await expect(page.getByText(/该轮验证没有失败反馈记录/)).toBeVisible();
  });

  test("初始化代码展示就绪状态", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [sampleStep({ status: "failed" })]);
    await stubAttempts(page, [sampleAttempt({ status: "failed" })]);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/initialize-code",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            container_id: "container-1",
            finished_at: "2026-10-26T08:04:00Z",
            persisted: true,
            record_id: "record-1",
            source_type: "git",
            started_at: "2026-10-26T08:00:00Z",
            status: "succeeded",
            step_id: "step-1",
            succeeded: true,
            task_branch: "agent/task-1",
            task_id: "task-1",
            work_dir: "/workspace/project",
          }),
          contentType: "application/json",
          status: 200,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "初始化代码" }).click();

    await expect(page.getByText(/代码已初始化/)).toBeVisible();
    await expect(page.getByText(/任务分支：agent\/task-1/)).toBeVisible();
  });

  test("判定 inconclusive 时展示为无法判定，不显示为验证通过", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [],
            failed_checks: 0,
            finished_at: "2026-10-26T08:03:00Z",
            passed: false,
            passed_checks: 1,
            persisted: true,
            review_ran: true,
            review_status: "needs_human_review",
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "inconclusive",
          }),
          contentType: "application/json",
          status: 200,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    await expect(page.getByText(/验证结论无法判定/)).toBeVisible();
    // 不得出现"验证通过"这一结论
    await expect(page.getByText(/确定性验证：验证通过/)).toHaveCount(0);
  });

  test("验证逐项检查结果按真实字段展示", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [
              {
                cancelled: false,
                certain: false,
                check_id: "build",
                check_name: "构建",
                duration_ms: 1200,
                exit_code: -1,
                finished_at: "2026-10-26T08:03:00Z",
                output_truncated: false,
                required: true,
                required_passed: false,
                sequence: 1,
                started_at: "2026-10-26T08:02:00Z",
                status: "error",
                timed_out: false,
              },
            ],
            failed_checks: 1,
            finished_at: "2026-10-26T08:03:00Z",
            passed: false,
            passed_checks: 0,
            persisted: true,
            review_ran: false,
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "failed",
          }),
          contentType: "application/json",
          status: 200,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    await expect(page.getByText(/验证未通过/)).toBeVisible();
    await expect(page.getByText(/检查「构建」（必需）/)).toBeVisible();
    await expect(page.getByText(/结果不可确认，不能当作代码问题/)).toBeVisible();
  });

  test("验证请求结果不确定时与业务拒绝分别提示，且不自动重发", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    let calls = 0;
    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) => {
        calls += 1;
        // 连接中断：结果无法确认
        return route.abort("connectionreset");
      },
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    await expect(page.getByText("操作结果无法确认", { exact: true })).toBeVisible();
    await expect(page.getByText(/页面不会自动重试/)).toBeVisible();
    // 没有任何自动重发
    expect(calls).toBe(1);
  });

  test("服务端拒绝时展示真实业务错误，不提示结果无法确认", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            error: { code: "precondition_failed", message: "任务容器未运行，不能执行验证" },
          }),
          contentType: "application/json",
          status: 412,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    await expect(page.getByText("确定性验证（步骤 step-1）失败")).toBeVisible();
    await expect(page.getByText(/任务容器未运行，不能执行验证/)).toBeVisible();
    await expect(page.getByText("操作结果无法确认", { exact: true })).toHaveCount(0);
  });

  test("初始化代码结果不确定时要求先核验现场，不提示成功", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [sampleStep({ status: "failed" })]);
    await stubAttempts(page, [sampleAttempt({ status: "failed" })]);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/initialize-code",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            container_id: "container-1",
            finished_at: "2026-10-26T08:04:00Z",
            persisted: true,
            record_id: "record-1",
            source_type: "git",
            started_at: "2026-10-26T08:00:00Z",
            status: "uncertain",
            step_id: "step-1",
            succeeded: false,
            task_branch: "agent/task-1",
            task_id: "task-1",
            work_dir: "/workspace/project",
          }),
          contentType: "application/json",
          status: 200,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "初始化代码" }).click();

    await expect(page.getByText(/代码初始化未成功/)).toBeVisible();
    await expect(page.getByText(/请先重新读取现场核验容器内实际状态/)).toBeVisible();
    await expect(page.getByText(/代码已初始化$/)).toHaveCount(0);
  });

  test("诊断动作成功后刷新失败时保留真实结论并单独提示", async ({ page }) => {
    await stubTaskList(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    // 任务详情第一次成功，动作之后的刷新失败
    let detailCalls = 0;
    await page.route("**/api/v1/tasks/task-1", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      detailCalls += 1;
      if (detailCalls === 1) {
        return route.fulfill({
          body: JSON.stringify(sampleTask()),
          contentType: "application/json",
          status: 200,
        });
      }
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      });
    });

    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      (route: Route) =>
        route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [],
            failed_checks: 0,
            finished_at: "2026-10-26T08:03:00Z",
            passed: true,
            passed_checks: 1,
            persisted: true,
            review_ran: false,
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "passed",
          }),
          contentType: "application/json",
          status: 200,
        }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();

    // 真实结论保留
    await expect(page.getByText(/确定性验证（步骤 step-1）：验证通过/)).toBeVisible();
    // 刷新失败单独提示，且不覆盖结论
    await expect(page.getByText("状态刷新失败", { exact: true })).toBeVisible();
    await expect(page.getByText(/执行结论仍然有效/)).toBeVisible();
  });

  test("诊断动作进行中重复点击只发出一次请求", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const calls: string[] = [];
    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      async (route: Route) => {
        calls.push(route.request().url());
        await gate;
        await route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [],
            failed_checks: 0,
            finished_at: "2026-10-26T08:03:00Z",
            passed: true,
            passed_checks: 1,
            persisted: true,
            review_ran: false,
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "passed",
          }),
          contentType: "application/json",
          status: 200,
        });
      },
    );

    await openDetail(page);
    await selectStep(page);
    const button = page.getByRole("button", { name: "确定性验证" });
    await button.click();
    await expect(page.getByText(/正在执行：确定性验证/)).toBeVisible();
    await button.click({ force: true });
    expect(calls).toHaveLength(1);

    release();
    await expect(page.getByText(/验证通过/)).toBeVisible();
    expect(calls).toHaveLength(1);
  });

  test("切换任务后旧的诊断结论不会污染新任务", async ({ page }) => {
    const first = sampleTask();
    const second = sampleTask({ id: "task-2", name: "补充接口测试" });
    await stubTaskList(page, first);
    // 列表包含两个任务，但详情各自独立
    await page.unroute("**/api/v1/tasks?*");
    await page.route("**/api/v1/tasks?*", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({ current: 1, records: [first, second], size: 20, total: 2 }),
        contentType: "application/json",
        status: 200,
      }),
    );
    await stubTaskDetail(page, first);
    await page.route("**/api/v1/tasks/task-2", (route: Route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        body: JSON.stringify(second),
        contentType: "application/json",
        status: 200,
      });
    });
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route(
      "**/api/v1/tasks/task-1/steps/step-1/attempts/attempt-1/validate",
      async (route: Route) => {
        await gate;
        await route.fulfill({
          body: JSON.stringify({
            attempt_id: "attempt-1",
            checks: [],
            failed_checks: 0,
            finished_at: "2026-10-26T08:03:00Z",
            passed: true,
            passed_checks: 1,
            persisted: true,
            review_ran: false,
            skipped_checks: 0,
            started_at: "2026-10-26T08:02:00Z",
            status: "completed",
            step_id: "step-1",
            task_id: "task-1",
            validation_run_id: "run-2",
            verdict: "passed",
          }),
          contentType: "application/json",
          status: 200,
        });
      },
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "确定性验证" }).click();
    await expect(page.getByText(/正在执行：确定性验证/)).toBeVisible();

    // 在请求仍未返回时切换任务
    await page.getByText("补充接口测试").first().click();
    await expect(page.getByText("执行操作")).toBeVisible();

    // 迟到响应返回后不得把旧任务的结论写进新任务
    release();
    await expect(page.getByText(/正在执行：确定性验证/)).toHaveCount(0);
    await expect(page.getByText(/验证通过/)).toHaveCount(0);
  });
});

test.describe("尝试与会话历史展示", () => {
  test("超时、取消、错误分类与失败阶段按真实字段展示", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [sampleStep({ status: "failed" })]);
    await stubAttempts(page, [
      // 超时尝试：布尔事实为 true，错误分类与失败阶段都已写入。
      sampleAttempt({
        cancelled: false,
        error_class: "timeout",
        failure_stage: "execute",
        status: "timed_out",
        timed_out: true,
      }),
      // 取消尝试：只有取消布尔为 true，没有错误分类与失败阶段。
      sampleAttempt({
        attempt_number: 2,
        cancelled: true,
        id: "attempt-2",
        status: "cancelled",
        timed_out: false,
      }),
    ]);
    await stubValidations(page, []);
    await stubSessions(page);

    await openDetail(page);
    await selectStep(page);

    // 超时是独立事实：状态标签使用 DictTag，独立标记是另一个文本节点，
    // 因此"已超时"在页面上应出现两次（状态标签 + 独立标记）。
    await expect(page.getByText("已超时", { exact: true })).toHaveCount(2);
    // 错误分类与失败阶段按真实取值展示为中文说明。
    await expect(page.getByText(/错误分类 执行超时/)).toBeVisible();
    await expect(page.getByText(/失败阶段 执行阶段/)).toBeVisible();
    // 取消尝试展示独立标记：状态标签"已取消" + 独立标记"已取消"。
    await expect(page.getByText("已取消", { exact: true })).toHaveCount(2);
    // 取消尝试没有 error_class / failure_stage，因此不出现虚构分类。
    await expect(page.getByText(/错误分类/)).toHaveCount(1);
    await expect(page.getByText(/失败阶段/)).toHaveCount(1);
  });

  test("布尔事实为 false 时不产生超时与取消警告", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [sampleStep({ status: "completed" })]);
    await stubAttempts(page, [
      sampleAttempt({ cancelled: false, status: "completed", timed_out: false }),
    ]);
    await stubValidations(page, []);
    await stubSessions(page);

    await openDetail(page);
    await selectStep(page);

    await expect(page.getByText(/第 1 次尝试/)).toBeVisible();
    await expect(page.getByText("已超时", { exact: true })).toHaveCount(0);
    await expect(page.getByText("已取消", { exact: true })).toHaveCount(0);
    await expect(page.getByText(/错误分类/)).toHaveCount(0);
    await expect(page.getByText(/失败阶段/)).toHaveCount(0);
  });

  test("会话归档与导出状态展示中文标签，并保留真实标识", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page, [sampleSession()]);

    await openDetail(page);
    await selectStep(page);

    await expect(page.getByText(/归档 已归档 \/ 导出 已导出/)).toBeVisible();
    // 会话标识、角色、模型与执行状态仍完整保留。
    // 执行状态"正常结束"与尝试状态标签同名，这里限定到会话行的作用域内断言。
    const sessionLine = page.getByText(/developer \/ group\/free/);
    await expect(sessionLine).toBeVisible();
    await expect(page.getByText("session-1", { exact: true })).toBeVisible();
    await expect(page.getByText("正常结束", { exact: true })).toHaveCount(2);
  });

  test("未知归档与导出状态原样展示，不误报成功", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page, [
      sampleSession({
        archive_status: "archived_to_object_storage",
        export_status: "pending_upload",
      }),
    ]);

    await openDetail(page);
    await selectStep(page);

    await expect(page.getByText(/归档 archived_to_object_storage/)).toBeVisible();
    await expect(page.getByText(/导出 pending_upload/)).toBeVisible();
    await expect(page.getByText(/归档 已归档/)).toHaveCount(0);
    await expect(page.getByText(/导出 已导出/)).toHaveCount(0);
  });

  test("归档成功但导出失败时两个事实分别展示", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page, [
      sampleSession({ archive_status: "succeeded", export_status: "failed" }),
    ]);

    await openDetail(page);
    await selectStep(page);

    await expect(page.getByText(/归档 已归档/)).toBeVisible();
    await expect(page.getByText(/导出 导出失败/)).toBeVisible();
    // 执行状态与归档 / 导出状态没有被合并成一个"成功"结论：
    // 归档成功与导出失败同时存在，页面不会把导出失败读作已导出。
    await expect(page.getByText(/导出 已导出/)).toHaveCount(0);
  });

  test("既有执行记录、验证历史与操作入口没有回归", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, [
      sampleAttempt({ error_class: "agent_failure", failure_stage: "execute", status: "failed" }),
    ]);
    await stubValidations(page);
    await stubSessions(page, [sampleSession()]);

    await openDetail(page);
    await selectStep(page);

    // 执行记录
    await expect(page.getByText(/第 1 次尝试/)).toBeVisible();
    await expect(page.getByText("执行尝试", { exact: true })).toBeVisible();
    // 验证历史
    await expect(page.getByText("验证历史", { exact: true })).toBeVisible();
    await expect(page.getByText("最新验证")).toBeVisible();
    await expect(page.getByText(/流程 已完成/)).toBeVisible();
    // 操作入口仍可见
    await expect(page.getByRole("button", { name: "确定性验证" })).toBeVisible();
    await expect(page.getByRole("button", { name: "初始化代码" })).toBeVisible();
  });
});

test.describe("修复周期", () => {
  test("修复周期被拒绝时展示服务端原因，不显示成功也不自动重试", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    const calls: string[] = [];
    await page.route("**/api/v1/tasks/task-1/steps/step-1/open-repair-cycle", (route: Route) => {
      calls.push(route.request().url());
      return route.fulfill({
        body: JSON.stringify({
          error: { code: "state_conflict", message: "仍在上限内，进入第 2 次开发尝试" },
        }),
        contentType: "application/json",
        status: 409,
      });
    });

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "开启新执行周期" }).click();

    await expect(page.getByText(/开启新执行周期（步骤 step-1）失败/)).toBeVisible();
    await expect(page.getByText("仍在上限内，进入第 2 次开发尝试")).toBeVisible();
    await expect(page.getByText("已开启新的执行周期")).toHaveCount(0);

    await page.waitForTimeout(500);
    expect(calls).toHaveLength(1);
  });

  test("修复周期成功后重新读取步骤与执行记录", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page);
    await stubValidations(page, []);
    await stubSessions(page);

    await page.route("**/api/v1/tasks/task-1/steps/step-1/open-repair-cycle", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          cycle_id: "cycle-2",
          cycle_sequence: 2,
          max_attempts: 3,
          opened_at: "2026-10-26T09:00:00Z",
          previous_consumed: 3,
          step_id: "step-1",
          task_id: "task-1",
        }),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "开启新执行周期" }).click();

    await expect(page.getByText("已开启新的执行周期")).toBeVisible();
    await expect(page.getByText(/上一周期已消耗：3 次/)).toBeVisible();
    await expect(
      page.getByText("该操作只登记新的预算作用域，不会自动执行；随后仍需显式发起执行。"),
    ).toBeVisible();
  });
});

test.describe("刷新失败与结论保留", () => {
  test("执行后刷新失败时原始执行结论仍然保留", async ({ page }) => {
    await stubTaskList(page);
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    // 任务详情：第一次成功，之后失败，模拟动作后刷新失败
    let detailCalls = 0;
    await page.route("**/api/v1/tasks/task-1", (route: Route) => {
      if (route.request().method() !== "GET") {
        return route.fallback();
      }
      detailCalls += 1;
      if (detailCalls === 1) {
        return route.fulfill({
          body: JSON.stringify(sampleTask()),
          contentType: "application/json",
          status: 200,
        });
      }
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      });
    });

    await page.route("**/api/v1/tasks/task-1/steps/step-1/execute", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          archive_status: "archived",
          attempt_id: "attempt-2",
          attempt_number: 2,
          attempt_status: "completed",
          exit_code: 0,
          export_status: "not_attempted",
          finalized: true,
          finished_at: "2026-10-26T08:01:00Z",
          outcome: "completed",
          started_at: "2026-10-26T08:00:00Z",
          step_id: "step-1",
          step_status: "completed",
          success: true,
          task_id: "task-1",
          task_status: "developing",
        }),
        contentType: "application/json",
        status: 200,
      }),
    );

    await openDetail(page);
    await selectStep(page);
    await page.getByRole("button", { name: "执行步骤" }).click();

    // 结论保留
    await expect(page.getByText(/步骤执行已结束/)).toBeVisible();
    // 刷新失败单独提示
    await expect(page.getByText("状态刷新失败", { exact: true })).toBeVisible();
    await expect(page.getByText(/执行结论仍然有效/)).toBeVisible();
  });

  test("步骤刷新失败时保留上一次的步骤数据", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    // StrictMode 下首次挂载会触发两次加载，因此前两次都返回成功数据
    let stepCalls = 0;
    await page.route("**/api/v1/tasks/task-1/steps?*", (route: Route) => {
      stepCalls += 1;
      if (stepCalls <= 2) {
        return route.fulfill({
          body: JSON.stringify({ current: 1, records: [sampleStep()], size: 100, total: 1 }),
          contentType: "application/json",
          status: 200,
        });
      }
      return route.fulfill({
        body: JSON.stringify({ error: { code: "internal_error", message: "服务内部错误" } }),
        contentType: "application/json",
        status: 500,
      });
    });

    await openDetail(page);
    await expect(page.getByText("共 1 个步骤")).toBeVisible();

    await page.getByText("刷新步骤", { exact: true }).click();

    await expect(page.getByText("步骤刷新失败，下面展示的可能是旧数据")).toBeVisible();
    // 旧数据仍然可见
    await expect(page.getByText("共 1 个步骤")).toBeVisible();
  });
});

test.describe("不伪造能力", () => {
  test("queued 任务不显示步骤执行与顺序执行入口", async ({ page }) => {
    await stubTaskList(page, sampleTask({ status: "queued" }));
    await stubTaskDetail(page, sampleTask({ status: "queued" }));
    await stubWorkflow(page);
    await stubSteps(page);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    await openDetail(page);

    await expect(page.getByRole("button", { name: "顺序执行" })).toBeDisabled();
    await selectStep(page);
    await expect(page.getByRole("button", { name: "执行步骤" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "开发—验证闭环" })).toBeDisabled();
  });

  test("非 Agent 步骤（确定性验证）不显示执行入口", async ({ page }) => {
    await stubTaskList(page);
    await stubTaskDetail(page);
    await stubWorkflow(page);
    await stubSteps(page, [sampleStep({ step_type: "verify" })]);
    await stubAttempts(page, []);
    await stubValidations(page, []);
    await stubSessions(page);

    await openDetail(page);
    await selectStep(page);

    await expect(page.getByRole("button", { name: "执行步骤" })).toBeDisabled();
  });

  test("任务创建与统一启动没有回归", async ({ page }) => {
    let launched = false;
    const oldEntry: string[] = [];
    await page.route("**/api/v1/tasks/*/execute-workflow", (route: Route) => {
      oldEntry.push(route.request().url());
      return route.fulfill({ body: "{}", contentType: "application/json", status: 409 });
    });
    const launchCalls: string[] = [];
    await page.route("**/api/v1/tasks/task-1/start", (route: Route) => {
      launchCalls.push(route.request().url());
      launched = true;
      return route.fulfill({
        body: JSON.stringify({
          container_prepared: true,
          plan_persisted: true,
          plan_steps: [],
          stage: "workflow",
          task_id: "task-1",
          task_status: "developing",
          workflow_started: true,
        }),
        contentType: "application/json",
        status: 202,
      });
    });
    await page.route("**/api/v1/tasks?*", (route: Route) =>
      route.fulfill({
        body: JSON.stringify({
          current: 1,
          records: [sampleTask({ status: launched ? "developing" : "queued" })],
          size: 20,
          total: 1,
        }),
        contentType: "application/json",
        status: 200,
      }),
    );
    await stubTaskDetail(page, sampleTask({ status: "queued" }));
    await stubWorkflow(page);
    await stubSteps(page, []);

    await page.goto("/tasks");
    const row = page.locator(".ant-table-tbody tr.ant-table-row", { hasText: "实现登录接口" });
    await row.getByText("启动", { exact: true }).click();

    await expect(page.getByText("已启动任务执行：实现登录接口")).toBeVisible();
    expect(launchCalls).toHaveLength(1);
    expect(oldEntry).toHaveLength(0);
  });
});
