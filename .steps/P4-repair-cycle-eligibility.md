# P4 常规开发 —— 修复周期开启资格查询与服务端阻塞原因展示

> **状态**：`completed`（2026-10-29，基于 `origin/master` `b09adaf5372edb9a419d877e0a1d286fff1fa86a`）
> **基线**：`git fetch origin` 后 `origin/master` = `HEAD` = `b09adaf`，当前分支 `master`，
> `git status --porcelain` 无输出（工作区干净）。
> **性质**：这是"恢复常规功能开发"后的第八个常规工作项，**不是** P4-M5 性能优化，
> 也不是 P4-M1 的 G 项。触发依据是 [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md)
> §6 第 3 条登记的同一缺口："修复周期可用性只能粗判，按钮常亮但可能被 409 拒绝"。
> **P4-M5 保持 `pending`**：本轮没有产生任何真实使用数据，也没有把缺少数据的优化标记为完成。
> 本文件记录**实际核实的代码事实、实际实施方式、实际执行的非测试检查**；未执行的测试单独列出。

---

## 1. 实施前核实：真实缺口与代码证据

### 1.1 前端只能粗判

实施前（`b09adaf`）`lingting-agent-flow-ui/src/pages/tasks/steps.ts` 中的
`describeRepairCycle(task, step)` 只检查两件事：

1. 任务层：`cancel_requested` 与 `taskAllowsStepExecution(task)`；
2. 步骤占用：`step.current_attempt_id` 是否为空。

`StepDetailPanel.tsx` 用它的 `.allowed` 决定「开启新执行周期」按钮是否可用，用 `.reason`
作为 `DisabledActionHint` 的说明。**它没有、也无法检查**：

- 当前执行周期还有多少次可用尝试（预算是否耗尽）；
- 上一周期是否已收敛（是否有 `unknown` / `recovery_required` / `interrupted` 的历史尝试）；
- 历史尝试引用的周期标识是否真实存在（周期归属是否可解释）；
- 从未执行过的步骤是否应当被拒绝（首轮即第一个周期）。

### 1.2 服务端早已有完整判定

- `internal/domain/repair_cycle.go` 的 `DecideRepairStart` 是唯一判定入口，规则顺序固定：
  步骤占用 → 历史结果不确定 → 上一周期已结束（需显式开新周期）→ 周期归属不明 →
  新周期分支（首轮拒绝 / 预算未耗尽拒绝 / 允许）→ 预算耗尽拒绝与允许重试。
- `internal/app/step_workflow_cycle.go` 的 `loadRepairBudgetFacts` + `decideRepairStart`
  负责把持久化的周期登记、全部历史尝试、步骤配置转换成领域输入；预算由
  `domain.DecideRepairBudget` 依据 `attempts.budget_scope_id` 计算。
- `OpenRepairCycle`（`POST .../open-repair-cycle`）在写之前完整执行任务准入、
  `checkPrerequisites` 与 `decideRepairStart`，只在允许时结束旧周期并登记新周期。

也就是说：**判定早已存在，缺的只是把它的结论按只读方式暴露出来**。任何在前端或 HTTP
层重新实现预算算法的做法都会制造第二套规则，因此本轮没有这么做。

### 1.3 审计中发现的口径差异（已记录，未修改规则）

`decideRepairStart` 的输入 `StepOccupied` 由
`step.CurrentAttemptID != nil || !step.Status.CanStartExecution()` 计算，而
`StepStatus.CanStartExecution()` 只包含 `pending` / `retry_wait` / `failed` /
`recovery_required`。因此 `completed`、`cancelled`、`skipped` 这类"不可开始执行"的步骤
会被**服务端**判为 `execution_active`（"步骤仍被一次执行占用"）。

这与上一轮前端 `describeRepairCycle` 的假设（"已完成的步骤仍可开启新周期"）相反：
真实服务端会用 409 拒绝该请求，前端当时属于乐观误判。本轮按服务端结论展示
（已完成步骤的按钮禁用，原因是 `execution_active` 的说明），**没有修改
`domain.DecideRepairStart`**：该行为是否应在未来区分"真正被占用"与"状态不可开始"
属于独立的规则问题，需要单独的证据与决策，不在本轮范围。

---

## 2. 新增只读资格查询 API

### 2.1 路由与语义

```text
GET /api/v1/tasks/{taskID}/steps/{stepID}/repair-cycle-eligibility
```

- 路径形状固定为 4 段：`{taskID}/steps/{stepID}/repair-cycle-eligibility`
  （`parseRepairCycleEligibilityPath`，与 `parseOpenRepairCyclePath` / `parseRepairCyclesPath`
  一样按路径段判定，而不是字符串前缀）。
- 只支持 `GET` 与 `HEAD`；其他方法返回 `405` `method_not_allowed` 并带 `Allow: GET, HEAD`。
- 未知路径与不匹配的形状仍走原有 `404` 语义：新判定在 `router.go` 中**先于**
  `isExecutionRecordPath` 与 `parseOpenRepairCyclePath` 执行，否则 `steps/{stepID}/...`
  会被当成执行记录资源返回 404。
- 依赖未装配（`StepWorkflow` 为空）返回 `503` `internal_error`，与 `open-repair-cycle` 一致。
- **只读**：不登记周期、不结束周期、不写事件、不改任务或步骤状态、不领取任务租约、
  不启动 Agent、不建立远程会话。

### 2.2 响应字段与来源

`httpapi.RepairCycleEligibilityResponse`：

| 字段 | 来源 |
|---|---|
| `task_id` / `step_id` | 经过归属校验的任务与步骤记录 |
| `task_status` / `step_status` | 读取时刻的真实状态（`domain.Task.Status` / `domain.Step.Status`） |
| `can_open_cycle` | `app.RepairCycleEligibilityResult.CanOpenCycle`，即 `decision.Allowed && decision.OpenCycle` |
| `blocking` | 稳定阻塞类别列表；允许时为空数组 |
| `summary` | 任务层/前置层由应用层生成，周期层直接使用 `DecideRepairStart` 的 `Summary` |
| `active_cycle` | `facts.HasCycle`（是否存在活动周期登记） |
| `cycle_sequence` / `max_attempts` / `consumed_attempts` / `remaining_attempts` | `domain.DecideRepairBudget` 的计算结果 |
| `attempts` | 该步骤的历史尝试总数（`len(facts.Attempts)`） |

字段来源全部是服务端持久化事实与既有纯函数结果，没有一个是前端可推断的量。

### 2.3 稳定阻塞类别

周期的拒绝类别**直接复用** `domain.RepairFailureClass` 的字符串取值
（`execution_active` / `cycle_unresolved` / `cycle_closed` / `new_cycle_denied` /
`attempts_exhausted` 等），不新增第二套分类。只有发生在周期判定**之前**的两条入口条件
与一处数据不一致，由应用层补充：

| 类别 | 含义 | 与既有分类的关系 |
|---|---|---|
| `cancel_requested` | 任务已记录取消请求 | 与显式重试的 `cancel_requested` 是同一业务事实，沿用同一取值 |
| `task_status_not_executable` | 任务状态不在 `AllowsStepExecution` 内 | 新增：对应步骤推进的状态白名单，重试资格里没有等价类别 |
| `prerequisite_incomplete` | `domain.CheckStepPrerequisites` 未通过 | 新增：对应前序步骤规则 |
| `cycle_ownership_unconfirmed` | 读到的周期登记不属于该步骤 | 新增：与 `cycle_unresolved`（尝试的**结果或归属无法解释**）是不同问题，因此不合并 |

### 2.4 错误语义

| 情形 | 结果 |
|---|---|
| 任务不存在 / 已逻辑删除、步骤不存在、步骤不属于该任务 | `404` `not_found`（沿用信息隐藏语义） |
| 标识非法（空、含 `/`） | `400` `invalid_request` |
| 类别不允许开启 | `200` + `can_open_cycle=false` + `blocking` + `summary` |
| 周期登记归属不一致 | `200` + `blocking=["cycle_ownership_unconfirmed"]`（数据不一致必须拒绝，但不能伪装成"预算未耗尽"） |
| 读取数据库失败、存储故障、判定内部不一致 | 如实返回错误（`500` `internal_error`），**不伪装成 `can_open_cycle=false`** |

---

## 3. 复用既有预算规则，写操作保持独立校验

`app.StepWorkflowService.RepairCycleEligibility`（`internal/app/step_workflow_eligibility.go`）
的判定顺序与写入口一致，且**只调用既有实现**：

```text
parseWorkflowID → loadTask → loadStep（归属校验）
  → repairCycleTaskGate（cancel_requested / AllowsStepExecution）
  → checkPrerequisites（domain.CheckStepPrerequisites）
  → decideRepairStart(ctx, step, true)（domain.DecideRepairStart + DecideRepairBudget）
```

- 没有新增任何预算或准入函数；`decideRepairStart` 是写入口也在用的同一个方法。
- 只有 `decision.Allowed && decision.OpenCycle` 才给出 `can_open_cycle=true`；
  "当前没有运行中的 Attempt"不作为允许依据（`cycle_closed` 与 `new_cycle_denied`
  都由领域判定给出）。
- 首次执行的边界：`decideNewCycle` 在 `len(input.Attempts) == 0` 时返回
  `new_cycle_denied`（"首轮执行本身就是第一个执行周期"），本查询原样返回该结论。
- 预算与不确定现场：全部来自 `domain.DecideRepairBudget` 与
  `RepairAttemptFacts.Unresolved` / `hasUnknownCycleOwnership`，没有前端或 Handler 的二次判断。

**查询与写操作的一致性边界**：

- 写入口 `OpenRepairCycle` 未做任何改动，仍会重新读取现场并完整校验；
- 查询结论只是读取时刻的快照：并发执行、另一客户端修改、历史数据变化都会让它过期；
- 因此写请求返回 `409` 时前端原样展示服务端原因，并**重新查询**资格，不自动重发，
  也不因为"查询说允许"就跳过校验。

本轮没有引入租约、锁或缓存协议：只读查询不需要它们，跨进程一致性仍由写入口的事务与
状态条件更新保证。

---

## 4. 前端接入

### 4.1 API 与类型

- `src/lib/api/types.ts`：新增 `RepairCycleEligibilityResponse`（与 Go DTO 逐字段对应，
  注释显式写明"`can_open_cycle=true` 只表示预算作用域可以登记"）。
- `src/lib/api/execution.ts`：新增 `stepRepairCycleEligibilityPath` 与
  `fetchStepRepairCycleEligibility`，复用 `apiRequest` / `ApiError`，只发 `GET`，不设写请求超时。
- `src/lib/api/index.ts`：导出上述路径、函数与类型。

### 4.2 唯一可信的数据源

新增 `src/pages/tasks/repairCycleEligibility.ts`（纯函数）与
`src/pages/tasks/useRepairCycleEligibility.ts`（唯一状态实现）：

- 资格状态区分 `idle` / `loading` / `ready`（可带 `error` 或 `refreshing`）/ `error`；
- **只有归属当前任务与当前步骤、且没有任何刷新失败或刷新进行中的 `ready` 快照**，
  才可能返回 `allowed=true`；其余一律不放行，原因分别为"尚未选择步骤 / 正在查询 /
  无法确认（资格未知）/ 快照不属于当前对象 / 刷新失败（可能已过期）/ 正在重新确认"；
- 阻塞原因使用服务端 `summary` 与 `blocking`；未知类别原样保留
  （`repairCycleBlockerLabel` 只翻译已知取值，未知值直接返回原字符串），绝不映射成允许；
- `describeRepairCycleFacts` 把服务端说明、阻塞类别与预算数字直接渲染在按钮下方
  （不依赖悬停），并明确"只登记预算，不会执行开发"与首次执行的边界。

`steps.ts` 中原有的 `describeRepairCycle` / `canOpenRepairCycle`（只做任务状态与步骤占用的
粗判）**已删除**，并留下注释指向新的权威入口：同时保留两套判定会让同一个按钮出现两种结论。

### 4.3 加载、刷新与竞态

- 挂载即查询；`TaskExecutionPanel` 用当前 `task.id` + `selectedStepID` 调用，切换任一标识
  都会作废旧快照（归属不符时纯函数也不放行，形成双层保护）。
- 复用既有 `requestGate.ts` 的"序号 + 归属"票据与卸载标记：迟到的旧任务/旧步骤响应被丢弃。
- 刷新时机：`stepsToken`（服务端执行相关事实变化）变化、动作结束后（`refreshAll` 中显式
  `reload`）。**不并入每 5 秒的任务快照循环**，也不自建定时器。
- 刷新失败保留上一次快照并明确标注可能已过期，但按钮不放行；查询失败（无快照）显示
  "资格未知"而不是"预算耗尽"。
- 动作进行中（`busy`）不把"操作进行中"写成业务阻塞：禁用原因的传递方式与上一轮一致。

### 4.4 按钮行为（保持不变的既有契约）

- 点击仍只调用 `openRepairCycle`（`POST .../open-repair-cycle`），不自动执行开发；
- `busy` 期间的重复提交防护、结果面板的"已开启新的执行周期 / 上一周期已消耗 N 次"
  展示、服务端 `409` 原因原样呈现都没有改动；
- 成功后按真实响应展示周期登记事实，不把"登记预算"描述成"任务即将成功"。

---

## 5. 修改文件

后端：

| 文件 | 改动 |
|---|---|
| `internal/app/step_workflow_eligibility.go`（新增） | `RepairCycleEligibilityBlocker` 及四个补充类别、请求/结果类型、`RepairCycleEligibility`（复用 `loadTask` / `loadStep` / `checkPrerequisites` / `decideRepairStart`）、`repairCycleTaskGate` |
| `internal/httpapi/step_cycle.go` | 新增 `repairCycleEligibilitySegment`、`RepairCycleEligibilityResponse`、`serveRepairCycleEligibility`、`parseRepairCycleEligibilityPath`；路径段常量合并为同一个 `const` 块 |
| `internal/httpapi/router.go` | 在 `tasksHandler` 中把资格查询判定放在执行周期写入口与执行记录判定之前 |

前端：

| 文件 | 改动 |
|---|---|
| `src/pages/tasks/repairCycleEligibility.ts`（新增） | 资格状态类型、`eligibilityBelongsTo`、`repairCycleBlockerLabel`、`describeRepairCycleAction`、`describeRepairCycleFacts` |
| `src/pages/tasks/useRepairCycleEligibility.ts`（新增） | 唯一状态实现：票据、归属作废、刷新中/失败保留快照、手动 `reload` |
| `src/pages/tasks/StepDetailPanel.tsx` | 新增 `repairEligibility` 属性；按钮可用性改由服务端资格决定；按钮下方直接展示资格事实 |
| `src/pages/tasks/TaskExecutionPanel.tsx` | 承载资格 Hook（任务 + 选中步骤 + `stepsToken`），把 `reload` 接入动作后的统一刷新并向下传递状态 |
| `src/pages/tasks/steps.ts` | 删除 `describeRepairCycle` / `canOpenRepairCycle`，留下指向新入口的说明 |
| `src/lib/api/{types.ts,execution.ts,index.ts}` | 新类型、路径、请求封装与导出 |

既有测试文件（只更新契约与断言，未新建测试文件）：

| 文件 | 改动 |
|---|---|
| `tests/unit/httpapi/step_workflow_test.go` | 新增 4 个用例：从未执行时拒绝且不产生执行副作用、预算用尽后允许且查询不写入新周期、非 GET 返回 405、任务/步骤不存在返回 404 |
| `lingting-agent-flow-ui/tests/unit/task-execution-state.test.ts` | 删除旧粗判断言；新增"只由服务端资格决定""资格未知/未归属/刷新失败不放行""重新查询期间不放行""未知阻塞类别原样保留""资格事实展示"等用例，并改写矩阵用例 |
| `lingting-agent-flow-ui/tests/unit/task-execution-api.test.ts` | 新增资格查询的路径、方法、字段解析与空标识拒绝用例 |
| `lingting-agent-flow-ui/tests/e2e/task-execution.spec.ts` | 新增资格查询替身（默认"缺少执行记录"、可覆盖为允许）与 3 个浏览器用例（服务端阻塞、查询失败、首次执行前）；改写原有修复周期与禁用原因用例以符合新契约 |

---

## 6. 实际执行的非测试检查

| 检查 | 结果 |
|---|---|
| `gofmt -l internal cmd tests` | 无输出 |
| `go vet ./...` | 无输出 |
| `go build ./...` | 通过 |
| `npx tsc --noEmit`（`lingting-agent-flow-ui`） | 通过（`TypeScript: No errors found`） |
| `npx vp fmt --check`（本次改动的 11 个前端文件） | 全部通过；先对 3 个文件执行过一次 `vp fmt`（仅这几个文件） |
| `npx vp lint`（`lingting-agent-flow-ui`） | 无输出 |
| `git diff --check` | 无输出 |
| 行尾检查 | 新增与修改的文本文件均为 LF，无 CRLF |

**未执行任何测试**（Go 与前端）：本轮未获得测试授权。`go test ./...`、
`pnpm test:unit:safe`、`pnpm test:e2e:safe` 均未运行，因此**新增与更新的断言尚未被验证**，
不能表述为"测试通过"。

---

## 7. 剩余风险与边界

1. **断言未经运行验证**：Go 侧 `TestRepairCycleEligibilityAPIBudgetExhaustedAllows` 依赖
   "闭环一次失败后预算恰好用尽"（步骤 `RetryConfig.MaxAttempts=1`、首轮 `first_run` 周期
   快照上限为 1）；前端单元与 E2E 用例依赖新的资格替身与文案。这些都要在获得授权后实跑确认。
2. **E2E 依赖资格替身的默认值**：`stubRepairCycleEligibilityDefault` 使用通配步骤路径，
   需要"允许开启"的用例在用例体内重新注册路由覆盖。Playwright 后注册的路由优先，但若未来
   改成 `route.fallback` 链式写法，需要重新确认覆盖关系。
3. **资格 ≠ 可执行**：容器受管理、执行节点预检、验证配置不在资格判定内——它们决定"能否
   真实执行"，而资格只回答"能否登记预算作用域"。因此 `can_open_cycle=true` 之后
   `execute-and-validate` 仍可能因 412 被拒，这是刻意保持的边界。
4. **只读查询的并发窗口**：查询与写操作之间现场可能变化（另一客户端、并发执行），写入口
   仍会拒绝；本轮不引入租约或缓存，也不承诺"查询即预授权"。
5. **阻塞列表只描述第一个拦路条件**：判定按阶段短路，因此不会一次列出全部可能阻塞项；
   这与写入口的失败顺序一致，避免出现"查询列出 3 项、写入只报 1 项"的矛盾。
6. **周期归属不一致返回 200**：`cycle_ownership_unconfirmed` 是"数据不一致但可以如实描述"
   的现场，选择 200 + 稳定类别而不是 4xx，是为了让客户端能区分"资格不允许"与"资格无法确认"；
   若未来把它定义成 500，需要同步调整前端与用例。
7. **`completed` 步骤被归类为 `execution_active`**（见 §1.3）：这是既有领域行为的直接结果，
   本轮按服务端结论展示而没有改写规则。如果后续确认应当区分"真正被占用"与"状态不可开始"，
   需要单独决策并同步资格类别、写入口与用例。
8. **不做的事**：没有修改 `domain.DecideRepairStart` 的判定逻辑（只复用它的结论）、修复上限、
   自动修复策略、周期关闭语义、任务状态机、工作流编排、租约协议；没有引入轮询循环、
   SSE/WebSocket、计划版本化、会话查询优化、日志采集改动、私有仓库凭据管理；
   也没有调整前端测试 worker=4 的配置。`P4-M5` 仍为 `pending`。

---

## 8. 文档同步说明

- 本轮同时纠正了文档中无证据支持的未来日期：`git log` 显示
  `042a33d`（步骤禁用原因）与 `b09adaf`（闭环失败响应）的提交时间都是 **2026-10-29**，
  因此 `.steps/README.md`、`.steps/ROADMAP.md`、
  `.steps/P4-M2-7-web-execution-operations.md`、`.steps/P4-disabled-action-reasons.md` 与
  `.steps/P4-task-detail-panel-organization.md` 中写作 `2026-10-30` / `2026-10-31` 的记录，
  已全部按真实提交时间改为 2026-10-29（没有追溯到任何一个未来日期）。
- `P4-M2-7` §6 第 3 条已从"只能粗判"更新为本轮实际处理结果，并保留"写请求仍会重新核验"
  与剩余边界。
- `P4-disabled-action-reasons.md` 增加了"后续变更"说明：它记录的那版粗判函数已被服务端
  资格取代。
