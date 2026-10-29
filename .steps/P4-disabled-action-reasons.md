# P4：禁用操作的原因提示（常规功能开发，不属于 P4-M5）

> - **状态**：`completed`（2026-10-30，基于 `origin/master` `d76f656`）
> - **基线**：`origin/master` = `d76f656eb18827a39d22fd5d63996a74d9ddc81a`（`远程真实测试资源治理`），
>   当前分支 `master`，本轮开始前 `git status --porcelain -uno` 无输出（工作区干净）。
> - **性质**：这是"恢复常规功能开发"后的第六个常规工作项，**不是** P4-M5 性能优化。触发依据是
>   [`P4-task-detail-panel-organization.md`](./P4-task-detail-panel-organization.md) §4.2 与 §10 第 2 条
>   登记的同一个缺陷："步骤详情的三个动作按钮禁用时没有原因提示，与任务列表「启动」按钮属同一类
>   一致性问题"。
> - **P4-M5 保持 `pending`**：本轮没有产生任何真实使用数据，也没有把缺少数据的优化标记为完成。
> - 本文件记录**实际核实的 DOM 行为、实际实施的判定与展示方式、实际执行的测试**；未执行的动作单独列出，
>   不作"通过"表述。

---

## 1. 结论摘要

四类操作在禁用时现在都能说明**真实原因**，原因全部来自调用前的准入判定，没有新增第二套业务规则：

| 操作 | 入口 | 原因来源（既有判定） | 禁用时可读到的说明（示例） |
|---|---|---|---|
| 启动 | 任务列表行内「启动」 | `launch.ts` 的 `canStartTask`（与服务端 `domain.TaskStatus.AllowsExplicitLaunch` 一致） | "任务已失败：统一启动不会隐式重试失败任务，请在任务详情查看服务端给出的重试资格，并按需显式重试。" |
| 执行步骤 | 步骤详情「执行步骤」 | `steps.ts` 的 `canExecuteStep`（任务状态 → 步骤状态 → 步骤占用 → 步骤类型） | "当前任务状态（等待执行）不允许推进步骤：新任务需要先完成环境准备与计划……" |
| 执行并验证 | 步骤详情「开发—验证闭环」 | `canExecuteAndValidate`（前端可见条件与执行步骤完全相同） | 同上（服务端另有验证配置与修复预算校验，前端不伪造这部分结论） |
| 开启新的修复周期 | 步骤详情「开启新执行周期」 | `canOpenRepairCycle`（任务状态 + 步骤占用） | "该步骤仍被当前执行尝试占用，需要等当前周期收敛后才能开启新的执行周期。" |

**规则本身没有任何变化**：`canStartTask` / `canExecuteStep` / `canExecuteAndValidate` /
`canOpenRepairCycle` 的返回值与修改前逐条一致（新增用例用矩阵逐组合断言），只是改为返回
`{ allowed, reason }` 的纯函数后再取 `.allowed`，因此不存在"判定一套、文案另一套"的漂移空间。

**本轮未改动**：任何 Go 代码、HTTP API、请求/响应 DTO、`domain` 规则、任务状态机、步骤占用规则、
重试 / 续跑资格、容器生命周期、验证规则与数据库结构；没有新增接口、Hook、定时器或自动重试。

---

## 2. 核实方法与基线

1. `git fetch origin` 后确认 `origin/master` 与 `HEAD` 同为 `d76f656`，工作区干净。
2. 阅读 `.steps/README.md`、`ROADMAP.md`、`P4-product-experience-optimization.md`、
   `P4-task-detail-panel-organization.md`、`P4-task-detail-experience-review.md`、
   `P4-M2-3-web-task-launch.md`、`SERVER-v1-acceptance.md`、`remote-test-resource-cleanup.md`，
   把它们当作**上一轮结论**而不是当前代码事实。
3. 从现行代码重新确认实际缺口（不按旧文档猜路径）：
   `TasksPage.tsx` 的操作列、`StepDetailPanel.tsx` 的动作区、`launch.ts` 的 `canStartTask`、
   `steps.ts` 的 `taskAllowsStepExecution` / `stepAllowsExecution` / `canExecuteStep` /
   `canExecuteAndValidate` / `canOpenRepairCycle`，以及 `useExecutionOperations.ts` 的 `busy`。
4. 核实底层组件的真实 DOM 行为（不是按组件名推断）：
   - `lingting-react-ui` 的 `LinkButton` 渲染 antd `Typography.Link`（即 `<a>`），`disabled` 只加
     `ant-typography-disabled` 样式并把 `onClick` 置空，**没有** `disabled` 属性、`aria-disabled`
     或 `tabIndex`，因此禁用后既不可聚焦，也不是可靠的悬停目标；
   - `Button`（antd）的 `disabled` 是真正的 DOM 禁用属性，浏览器**不会在禁用控件上派发鼠标事件**，
     直接挂在按钮上的 Tooltip 永远不会打开；
   - antd `Tooltip` 的默认 `trigger` 只有 `hover`（`mergedTrigger = trigger || 'hover'`），
     所以"按钮禁用 + 只写 `tooltip`"既拿不到鼠标事件、也拿不到键盘事件。
5. 上述三点共同决定了展示方案（见 §5），而不是"给按钮加一个 `tooltip` 属性"。

---

## 3. 任务列表「启动」按钮

`launch.ts` 原有的 `canStartTask` 只有布尔结论。本轮把它改为：

```ts
export type ActionEligibility = { allowed: boolean; reason: string };

export function describeTaskStart(task: Task): ActionEligibility;
export function canStartTask(task: Task): boolean; // = describeTaskStart(task).allowed
```

分支顺序与修改前逐条对应（先取消请求，再失败任务，再已结束，再状态白名单），原因只描述**这些分支
真实检查过的事实**：

| 现场 | 原因 |
|---|---|
| `cancel_requested` | 已请求取消：取消请求尚未结束，此时不能通过统一启动重新开始任务。 |
| `status === "failed"` | 任务已失败：统一启动不会隐式重试失败任务，请在任务详情查看服务端给出的重试资格，并按需显式重试。 |
| `status_finished`（`completed` / `cancelled`） | 任务已结束（已完成／已取消），统一启动不会重开已停止执行的任务。 |
| 执行中或其它不允许的状态（`planning` / `developing` / `verifying` / `saving` / `cancelling`） | 当前任务状态（开发中……）不支持统一启动：正在执行或正在取消的任务不会重复启动。 |
| 未知状态 | 同上句式，且**原样保留**服务端取值（`current_task_status` 不在联合类型内时显示原始字符串）。 |

两点刻意保留：

- **失败任务单独一条**：`status_finished` 包含 `failed`，但"失败"不能用"任务已结束"一句带过——重试资格
  是另一个服务端判定，必须把用户指向任务详情的显式重试入口。
- **启动中不算准入失败**：`startingTaskID === record.id` 时按钮仍按原条件禁用，但原因传空字符串，
  页面不渲染任何禁用说明；"启动中"是流程状态，不是"该任务不允许启动"。

---

## 4. 步骤级三个操作

`steps.ts` 新增三个返回 `ActionEligibility` 的纯函数，并把原有布尔函数改成投影：

```ts
describeStepExecution(task, step)     -> canExecuteStep
describeExecuteAndValidate(task, step) -> canExecuteAndValidate
describeRepairCycle(task, step?)       -> canOpenRepairCycle
```

原因来源逐条对齐既有判定，顺序也一致：

| 判定层 | 适用操作 | 原因 |
|---|---|---|
| `task.cancel_requested` | 三个都是 | 任务已请求取消：取消请求尚未结束，此时不能推进任何步骤。 |
| 任务状态不在 `AllowsStepExecution` 内 | 三个都是 | 当前任务状态（X）不允许推进步骤：新任务需要先完成环境准备与计划，取消中与已结束的任务不能再执行步骤。 |
| `!stepAllowsExecution(step)` | 执行步骤 / 执行并验证 | 步骤状态为「已完成」，只有待执行、等待重试、失败或待核验的步骤可以开始新的执行。 |
| `step.current_attempt_id` | 三个都是 | 该步骤仍被当前执行尝试占用，需要等这次尝试结束后才能重新执行（修复周期为"等当前周期收敛"）。 |
| 步骤类型不属于 Agent 步骤 | 执行步骤 / 执行并验证 | 步骤类型为「确定性验证」，由服务端直接执行，不通过单步骤执行入口发起。 |
| 未选中步骤 | 开启新的修复周期 | 尚未选择步骤，无法开启新的执行周期。 |

**差异是真实的，不是文案差异**：已完成步骤仍可开启新执行周期（修复周期的前置条件不检查步骤状态与
步骤类型），被占用与任务状态层则对三个操作同时成立；新增用例对这三条差异逐一断言。执行并验证的
前端可见条件与执行步骤完全相同（服务端另有验证配置与修复预算校验，前端拿不到），因此它复用同一份
判定与同一份原因，而不是另写一段。

动作进行中（`operations.state.running !== undefined`）与启动按钮一样处理：按钮仍禁用，但不展示业务
原因。

---

## 5. 交互与可访问性

新增展示组件 `DisabledActionHint`（`src/pages/tasks/DisabledActionHint.tsx` + 同名 `.css`）：

- `reason` 为空 → 直接渲染子节点，**不产生任何包装、提示或 aria 属性**（可用时不会出现误导性说明）；
- `reason` 非空 → 用一层 `<span class="disabled-action-hint" tabIndex={0} aria-label="{操作}不可用：{原因}">`
  承载 antd `Tooltip`，并显式声明 `trigger={["hover", "focus"]}`；
- CSS 只做两件事：包装层 `inline-flex` + `cursor: not-allowed`，以及对直接子元素
  `pointer-events: none`——被禁用控件不派发鼠标事件，指针事件因此落到可聚焦的包装层上，
  否则"悬停看原因"在真实浏览器里不会生效。

由此满足四项要求：

| 要求 | 实现 |
|---|---|
| 鼠标悬停可读 | Tooltip 由包装层的 hover 打开（E2E 用 `hover()` 断言浮层文本） |
| 键盘可读 | 包装层 `tabIndex=0` + `trigger` 含 `focus`；E2E 只用 `Tab` 前进直到聚焦包装层，再断言浮层 |
| 读屏可读 | `aria-label` 为"操作 + 原因"，聚焦即被朗读 |
| 不残留旧对象原因 | 原因在每次渲染时按当前任务/步骤重新计算，不使用缓存；切换任务或步骤后包装层与浮层都不再对应旧对象（E2E 断言旧浮层最终为 0） |

不改变的既有行为：按钮名称、点击行为、请求内容、API 路径、操作顺序、`busy` 期间的重复提交防护。

---

## 6. 修改文件与行为变化

生产代码（全部位于 `lingting-agent-flow-ui/src/pages/tasks/`，未触碰 Go 与 HTTP 契约）：

| 文件 | 行为变化 |
|---|---|
| `steps.ts` | 新增 `ActionEligibility` 类型与 `describeStepExecution` / `describeExecuteAndValidate` / `describeRepairCycle`；`can*` 三个函数改为其 `.allowed` 投影（结论逐条不变） |
| `launch.ts` | 新增 `describeTaskStart`；`canStartTask` 改为其 `.allowed` 投影（结论逐条不变） |
| `DisabledActionHint.tsx` / `.css`（新增） | 禁用时的可聚焦原因包装层；可用时不渲染任何包装 |
| `StepDetailPanel.tsx` | 三个动作按钮包在 `DisabledActionHint` 中，`busy` 时不传原因 |
| `TasksPage.tsx` | 行内「启动」按钮包在 `DisabledActionHint` 中，正在启动时不传原因 |

测试代码：`tests/unit/task-launch-state.test.ts`、`tests/unit/task-execution-state.test.ts`（新增原因
用例）、`tests/e2e/task-launch.spec.ts`、`tests/e2e/task-execution.spec.ts`（新增 12 个浏览器用例）。

一处**与本轮功能无关但必须说明**的测试维护：`tests/unit/test-resource-policy.test.ts` 原先断言
`package.json` 的安全入口是 `--maxWorkers 1` / `--workers=1`，而仓库当前（用户已确认的）设置为 4，
该断言在本轮基线 `d76f656` 上就是失败的。本轮**没有改动 worker 数**，只把断言改为从
`tests/internal/test-resource-policy.ts` 的默认值取数（`DEFAULT_VITEST_MAX_WORKERS` /
`DEFAULT_PLAYWRIGHT_WORKERS`），使测试与"唯一策略来源"重新一致。

---

## 7. 测试与验证（实际执行）

| 命令 | 实际结果 |
|---|---|
| `pnpm test:unit:safe` | **通过**：28 个测试文件 / 626 个用例（`--maxWorkers 4` 未改动） |
| `pnpm test:e2e:safe` | **通过**：245 通过 / 0 失败（`--workers=4` 未改动） |
| `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | **通过** |
| `pnpm build` | **通过**，产物写入 `internal/webui/dist`（仅既有分块体积提示） |
| Go 测试与 `go vet` | **未执行**：本轮未修改任何 Go 文件与后端契约 |

新增用例覆盖：

- 单元（启动）：允许启动时原因为空、已请求取消、失败任务、`completed` / `cancelled` 已结束、
  执行中的 5 个状态、未知状态原样保留、以及"原因与 `canStartTask` 结论一致且不允许时必有原因"的矩阵。
- 单元（步骤）：允许时原因为空、任务状态层、取消请求、未知任务状态、6 个不允许的步骤状态、步骤占用、
  非 Agent 步骤、未知步骤状态与未知步骤类型、闭环与执行原因相同、修复周期与执行入口的前置条件差异、
  未选中步骤，以及 7 任务 × 6 步骤的矩阵（与三个 `can*` 逐组合比对）。
- E2E（任务列表）：失败任务的启动按钮禁用且悬停可读到"重试 / 任务详情"、点击不发出 `/start` 请求、
  键盘 `Tab` 可聚焦并打开同一原因、允许启动时不存在任何禁用包装且仍调用既有 `/start`、
  "启动中"不出现业务原因、两行之间切换不残留旧原因。
- E2E（步骤）：任务状态不许推进时原因含真实状态且点击不发出 `/execute`、步骤状态不许时原因含
  "已完成"而同一现场仍可开启新执行周期、步骤占用原因、非 Agent 步骤原因、允许执行时无包装且按原契约
  调用 `/tasks/task-1/steps/step-1/execute`、键盘沿按钮顺序获得原因、切换步骤后旧原因与旧浮层都不残留。

测试产物保留在操作系统临时目录，未删除：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<随机后缀>\`
（含 `TEST_ARTIFACT_DIR`、Playwright 的 `playwright-output/` 与 `playwright-report/`）。

未执行：Go 侧任何测试（未改 Go 文件）、真实 SSH / Docker / Pi 远程验证（本轮只改前端展示逻辑，
不产生远程副作用）、人工浏览器走查（用户未提供地址，也未要求访问页面）。

---

## 8. 保留的设计、风险与未覆盖项

**保留的合理设计**

1. 重试 / 续跑 / 执行操作的既有 `tooltip` 说明保持原状，未替换为本轮组件（本轮只补齐"禁用却无原因"
   的四类操作）。
2. 前端只解释自己检查过的条件；服务端的容器、执行节点预检、验证配置与修复预算等判定**不写入原因**，
   避免把"前端猜测"展示成后端结论。
3. `can*` 布尔函数继续导出：它们仍是页面真实使用的判定入口，也是既有单元用例的契约。

**风险**

1. 包装层 `pointer-events: none` 作用在子元素上：这依赖"禁用控件本身不响应指针事件"这一前提，
   对当前的 `Button` / `LinkButton` 成立；若将来某个子控件在禁用态仍需独立悬停行为，需要单独处理。
2. `tabIndex=0` 的包装层会给页面增加 1 个键盘焦点位（每个被禁用操作一个）。这是"键盘可读原因"的
   代价，也是当前唯一可行的原生方案（禁用控件不可聚焦）。
3. 浮层的退场动画会让"上一个对象的说明"在约 0.2 s 内仍可见；E2E 因此断言"最终不残留"而不是
   "瞬时不存在"，真实用户读到的仍是同一份状态绑定关系。
4. 只有这四类操作有统一的原因提示。任务列表「取消 / 删除」、尝试级「确定性验证 / 初始化代码」等
   入口本次未纳入，仍靠现有面板说明。

**未覆盖（明确不在本轮范围）**

- 不修改任何服务端准入规则、API 与 DTO；按钮可用不等于服务端一定接受请求。
- 不改动重试资格（`retry-eligibility`）、续跑资格（`can_resume`）与容器处置的展示与判定。
- 不新增弹窗、Hook、刷新循环、API 请求或自动重试。

---

## 9. 与 P4-M5 的关系

P4-M5 要求"优化项有真实使用问题或可测量数据支撑"。本轮是**可解释性缺陷**（禁用却无原因），
由组件真实 DOM 行为与 E2E 直接证实，不涉及性能、并发、存储或架构，也没有产生任何可用于性能立项的
数据，因此：

- **`P4-M5` 保持 `pending`**，未标记完成，也未进行缺少数据的评估；
- 本轮不构成 P4 阶段结束：P4 仍只剩数据依赖型的 `P4-M5`。
