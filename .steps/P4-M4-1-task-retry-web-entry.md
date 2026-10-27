# P4-M4-1：失败任务显式重试 Web 入口（G5）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M4`（支持必要的继续执行、取消、恢复和结果查看操作）
- **覆盖缺口**：G5「失败任务重试无 Web 入口」
- **范围**：只接入前端。`POST /api/v1/tasks/{taskID}/retry` 与
  `GET /api/v1/tasks/{taskID}/retry-eligibility` 均已存在，本轮**没有修改任何 Go 生产代码、
  领域规则、状态机、迁移脚本或第二套工作流**。

---

## 1. 实施前后的 HEAD SHA 与工作区状态

| 阶段 | 分支 | HEAD SHA | 工作区 |
|---|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致） | `716b0236ff3700514af7568070b98804d2483355` | `git status --short` **为空**（无预先修改） |
| 实施后 | `master`（改动未提交） | `716b0236ff3700514af7568070b98804d2483355` | 本轮新增 / 修改的前端文件（见 §5） |

开始前执行 `git fetch origin master`，`origin/master` 与 `HEAD` 同为 `716b023`
（`:zap:  事件时间线与修复周期历史`），上游未漂移；`git status --short` 为空，说明没有需要
保留的他人改动。交付前再次 `git fetch origin master`，`origin/master` 仍是 `716b023`，
上游在本轮实施期间没有新提交。

核对的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`（§G5 与
§7 M4 工作项表）、`.steps/P4-M2-7-web-execution-operations.md`、
`.steps/P4-M3-4-resume-eligibility-display.md`、`.steps/P4-M3-7-progress-auto-refresh.md`、
`.steps/P4-M3-8-event-timeline-repair-cycle-history.md`。

---

## 2. 实施前核实到的真实契约（结论全部来自代码）

### 2.1 路由与分发

| 接口 | 方法 | 分发位置 | 语义 |
|---|---|---|---|
| `/api/v1/tasks/{taskID}/retry` | `POST` | `internal/httpapi/router.go:181` → `serveTaskRetry` → `taskRetryHandler.retry` | 同步的显式重试，成功返回 **202** |
| `/api/v1/tasks/{taskID}/retry-eligibility` | `GET` / `HEAD` | 同上 → `taskRetryHandler.eligibility` | 只读资格查询，返回 **200** |

路径判定为 `internal/httpapi/task_retry.go` 的 `isTaskRetryPath`（按路径段形状，`{taskID}/retry`
与 `{taskID}/retry-eligibility` 两段）；其余形状仍由任务处理器处理，保留原有 404 / 405 语义。

### 2.2 请求 DTO

`httpapi.RetryTaskRequest`（`internal/httpapi/task_retry.go`）只有四个可选执行参数：

| 字段 | 含义 | 缺省 |
|---|---|---|
| `prompt` | 追加在计划与每一步骤提示词之后的补充说明 | 空 |
| `timeout_seconds` | 单个步骤开发阶段超时秒数 | 0 → 任务配置 / 服务端默认 |
| `validation_timeout_seconds` | 单个步骤验证阶段超时秒数 | 0 → 服务端默认 |
| `plan_timeout_seconds` | 本次规划调用超时秒数 | 0 → 任务配置 / 服务端默认 |

**没有**步骤列表、Session 路径、工作目录、执行代次或角色覆盖字段；请求体可为空
（`decodeOptionalJSONBody`）。

### 2.3 响应 DTO

`httpapi.RetryTaskResponse`：

- `task_id`、`previous_status`、`task_status`；
- `lease_epoch`、`lease_owner`（新执行周期由新的任务租取代次表达）；
- `workflow_started`（本次是否真的启动了编排）；
- 可选 `run`（`WorkflowRunResponse`，**只有** `workflow_started=true` 时才填充）；
- 可选 `failure_stage`、`failure`（启动流程失败时的阶段与原因摘要）；
- `started_at` / `finished_at`（**无 `omitempty`**，零值时序列化为
  `0001-01-01T00:00:00Z`；前端必须识别零值，不能当作真实时刻展示）。

`httpapi.RetryEligibilityResponse`：`task_id`、`task_status`、`can_retry`、`blocking`、`summary`。
`blocking` 由 `decision.Rejections` 逐个转换；没有阻塞项时 Go 端 `append` 到 nil 切片，
序列化为 `null`，前端按"没有给出阻塞项"处理。

### 2.4 资格判定（唯一来源）

`domain.DecideTaskRetryEligibility`（`internal/domain/task_retry.go`）是纯函数，按固定顺序收集
**全部**拒绝原因（不短路）：`task_deleted`、`cancel_requested`、`status_not_retryable`、
`lease_held`、`active_workflow_run`、`step_occupied`、`recovery_residue`、
`external_side_effect_pending`、`prerequisite_missing`、`nothing_to_retry`，共 10 个稳定分类。
`AllowsExplicitRetry()` 当前只接受 `failed`。

**结论：`can_retry` 是唯一判据。** 本轮前端没有复制这套规则，也没有任何
`status === "failed"` / `status_terminal` / `status_finished` 判断参与重试可用性。

### 2.5 执行语义（`app.TaskRetryService.Retry`）

顺序固定为：判定资格 → 以 `failed` 为条件原子领取任务租约 → 重新读取并再次判定资格 →
用租约栅栏把任务重开为 `preparing`（`domain.Task.ReopenForRetry`）→ 通过
`TaskLaunchService.Launch` 交接给既有统一启动流程。

因此**任务可能先被重开为 `preparing`，之后才发现启动流程失败**：此时响应里的
`task_status` 是 `preparing`（或后续真实状态）、`workflow_started=false`，错误类别为
`ErrTaskRetryFailed`。前端必须分别展示，不能显示为"重试成功"。

### 2.6 错误映射（`writeRetryError`）

| 错误类别 | HTTP | `code` |
|---|---|---|
| `ErrTaskRetryConflict` / `ErrTaskLeaseConflict` / `storage.ErrConflict` | 409 | `conflict` |
| `ErrTaskRetryNotFound` / `ErrTaskLeaseNotFound` / `storage.ErrNotFound` | 404 | `not_found` |
| `ErrTaskRetryPrerequisite` / `ErrTaskLeaseInvalid` | 412 | `precondition_failed` |
| `ErrTaskRetryFailed` | 500 | `execution_failed`（**重试已真实开始但未成功**，保留原始 message） |
| `ErrTaskLaunchStopped` / `ErrWorkflowStopped` | 503 | `internal_error` |
| 其他 | 500 | `internal_error`（message 被替换为"服务内部错误"） |

注意 500 有两类：`execution_failed` 会保留真实原因，因此前端不得只看状态码推断"什么都没发生"。

### 2.7 请求是否同步、客户端超时

`taskRetryHandler.retry` 是**同步**的：只有任务被重开、启动流程得出真实结论后才写响应；
工作流本身在其后的后台推进。启动流程包含容器准备与 Planner 调用，耗时远超普通请求。

既有惯例：`startTask`（`src/lib/api/endpoints.ts`）对同类同步写操作使用
`timeoutMs: options.timeoutMs ?? 0`（不设客户端超时），注释明确说明"避免客户端提前断开后
留下未知状态的现场"。本轮 `retryTask` 沿用同一惯例。

### 2.8 与文档的差异

`.steps/P4-M1-api-web-gap-inventory.md` §G5 列出的拒绝原因有 8 项，实际领域常量有 10 项
（多出 `task_deleted` 与 `cancel_requested`）。本轮以代码为准：前端阻塞分类元数据覆盖全部
10 项，并在遇到未知分类时原样展示。文档该处未修改（文档同步需用户明确要求）。

---

## 3. 前端改动与必要性

| 文件 | 改动 | 必要性 |
|---|---|---|
| `src/lib/api/types.ts` | 新增 `RetryTaskRequest`、`RetryTaskResponse`、`RetryEligibilityResponse` | 按真实 Go DTO 声明类型；`started_at` / `finished_at` 的零值语义写进注释 |
| `src/lib/api/endpoints.ts` | 新增 `taskRetryPath`、`taskRetryEligibilityPath`、`fetchTaskRetryEligibility`、`retryTask` | 页面不得直接 `fetch`；重试走同步写操作惯例（不设客户端超时） |
| `src/lib/api/index.ts` | 导出上述类型与函数 | 保持统一出口 |
| `src/pages/tasks/retry.ts`（新增） | 阻塞分类元数据、资格结论文案、`canSubmitRetry`、请求票据、结果解释、零值时间判定、结果不确定提示 | 纯函数，使"资格只来自 `can_retry`""任务切换竞态""`workflow_started` 语义""未知取值保留"都可被直接断言 |
| `src/pages/tasks/useTaskRetry.ts`（新增） | 资格查询（票据 + 保留上次成功快照）+ 显式重试（防重、无自动重发、结果不确定提示、操作后刷新） | 唯一状态实现；避免在页面里堆竞态与刷新逻辑 |
| `src/pages/tasks/RetryTaskPanel.tsx`（新增） | 资格面板 + 重试入口 + 结果面板 | 详情页独立面板（用户确认的形态） |
| `src/pages/tasks/status.ts` | 新增 `TASK_STATUS_LABEL`（`TASK_STATUS_META` 的投影） | 需要按字符串查中文标签而不重复定义状态名；不由它推断任何业务资格 |
| `src/pages/tasks/TasksPage.tsx` | 详情内加入 `<RetryTaskPanel onRefreshTask={refreshTaskDetail} task={selected} />` | 接入点；刷新复用既有 `refreshNow`，不新建轮询 |
| `tests/e2e/task-code-saves.spec.ts`、`tests/e2e/task-resume-eligibility.spec.ts` | 把 `getByRole("button", { name: /重\s*试/ })` 收紧为 `/^重\s*试$/` + `.first()` | 页面新增了一个"重试任务"按钮，原正则会同时命中两个按钮导致 strict mode 冲突；语义断言不变 |

**未改动**：任何 Go 文件、迁移脚本、`internal/domain`、`app.TaskRetryService`、
`TaskLaunchService`、`useTaskLiveSnapshot`、`liveSnapshot`、`autoRefresh`、`requestGate`。

---

## 4. 展示规则、防重、竞态与结果语义

### 4.1 资格面板

| 状态 | 展示 |
|---|---|
| `loading` | "正在查询重试资格…" |
| `ready`（`can_retry=true`） | 绿色结论"当前查询结果允许重试" + 服务端 `summary` + 真实 `task_status` |
| `ready`（`can_retry=false`） | 橙色结论"当前存在阻塞，不能重试" + 服务端 `summary` + 全部 `blocking` 逐条展示 |
| `ready` + `error`（刷新失败） | 保留上次成功快照，另附"重试资格刷新失败"并明确"不是最新状态" + 重试按钮 |
| `error`（无可用快照） | "查询重试资格失败" + 服务端错误原文 + "重试资格当前未知，不能据此发起重试" + 重试按钮 |
| `blocking` 为空 / `null` | "服务端本次没有返回阻塞项；资格结论仍以服务端给出的 can_retry 为准" |
| 未知阻塞分类 | 中文标签位置使用原始代码，并额外以 `code` 展示 + "未定义"说明；不丢整条响应 |
| 未知 `task_status` | 原样展示服务端取值 + "服务端返回的状态取值未在当前版本中定义" |
| 快照不属于当前任务 | 额外提示"该资格结论描述的不是当前打开的任务，已经失效" |

资格查询**不进入** 5 秒自动刷新循环：`useTaskRetry` 的依赖只有 `taskID`、显式刷新令牌与
用户点击；`liveSnapshot.applySnapshotRound` 未改动，未新增 token。

### 4.2 操作防重与前置条件

- 唯一判据：`canSubmitRetry` 要求"最近一次成功取得的快照存在 + 快照归属当前任务 +
  `can_retry=true` + 没有在途请求"；
- 资格未查询、查询失败、快照过期（不属于当前任务）、`can_retry=false` 一律禁用按钮，
  tooltip 固定为"尚未取得允许重试的服务端结论，不能发起重试"；
- 双重防重：`operation.running` 驱动按钮禁用与 `loading`，`runningRef` 做同帧同步拦截；
- 面板未使用 `next_actions`（该接口不返回），也不自动执行任何命令。

### 4.3 请求与结果语义

- 只有用户点击才调用 `POST /retry`，默认请求体为 `{}`（服务端默认参数）；
- 不自动重发：`retry()` 内没有任何重试循环；失败、超时、网络中断后由用户显式决定；
- 结果按响应字段分两类：
  - `workflow_started=true` → "重试已重开任务并启动工作流"，列出真实 `task_status`、
    `lease_epoch`、真实时间（非零值）与 `run` 的 `run_id` / 状态 / 进度；
  - `workflow_started=false` → "任务已重开，但工作流没有启动"，列出真实 `task_status`、
    `failure_stage`（经 `launchStageMeta` 中文化，未知阶段原样）与 `failure`（缺失时为
    "服务端未提供失败阶段 / 原因"）；
- 无论哪一类都附一句"请求成功只说明服务端完成了本次操作；任务是否成功仍要以最终业务结论
  与收尾闭环为准"，**不出现**"任务成功""已通过验收"字样；
- 请求失败：展示服务端 `message`（保留真实业务错误）；若 `ApiError.kind` 为
  `timeout` / `network` / `aborted`，额外显示"无法确认服务端是否已完成本次重试操作……
  页面不会自动重复提交"；
- 操作后：调用父组件 `refreshTaskDetail`（既有 `useTaskLiveSnapshot.refreshNow`，只读任务详情
  + 工作流进度）+ 重新查询重试资格（`setNonce`）。刷新失败只追加
  "重试请求已结束，但状态刷新失败：……上方结论仍然有效"。

### 4.4 竞态处理

| 场景 | 处理 |
|---|---|
| 同一任务并发刷新资格 | `requestID` 递增，只有最后一次发起的结果被采用 |
| 从任务 A 切到任务 B | 资格票据带 `taskID`，`activeTaskID !== ticket.taskID` 时丢弃响应；`snapshotRef` 同步失效 |
| 组件卸载 | `mountedRef` 拦截；资格请求的 `AbortController` 在 effect 清理时中止 |
| 重试响应迟到 | 发出时记录 `target`，响应 / 刷新回来时若 `activeTaskRef.current !== target` 则直接返回，不写入界面 |
| 资格查询失败但已有快照 | 保留快照并标注刷新失败，不伪造"没有阻塞" |

---

## 5. 测试与检查的实际执行结果

全部命令在 `lingting-agent-flow-ui/` 下执行。测试授权来自本次任务书的明确要求
（用户确认"授权全部"）。

| # | 命令 | 是否执行 | 退出码 | 结果 |
|---|---|---|---|---|
| 1 | `npx vitest run tests/unit/task-retry.test.ts` | 已执行 | 0 | 1 个文件通过，**34 passed** |
| 2 | `npx playwright test tests/e2e/task-retry.spec.ts --workers=1 --reporter=line` | 已执行 | 0 | **10 passed**（20.6s） |
| 3 | `pnpm test:unit:safe` | 已执行 | 0 | 23 个文件通过，**443 passed** |
| 4 | `pnpm test:e2e:safe` | 已执行 | 0 | **155 passed / 0 failed**（5.8m） |
| 5 | `pnpm check`（`vp fmt && vp lint --fix && tsc --noEmit`） | 已执行 | 0 | 通过（117 files），无 warning |
| 6 | `pnpm build` | 已执行 | 0 | 通过，产物写入 `internal/webui/dist` |

测试产物目录（`TEST_ARTIFACT_DIR`，保留未清理，例如）：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-jfIe4a`
（单元测试）、`...\run-Ane4NO`（单文件 E2E 诊断运行）。
`pnpm test:e2e:safe` 的完整运行报告位于其自身输出的 `run-*` 目录下
`playwright-report/`。

### 5.1 过程中出现的失败与处理（如实记录）

| 失败 | 原因 | 是否本轮回归 | 处理 |
|---|---|---|---|
| 首轮 `pnpm test:e2e:safe`：`task-code-saves.spec.ts:436`、`task-resume-eligibility.spec.ts:525` 失败 | 页面新增"重试任务"按钮后，旧断言 `getByRole("button", { name: /重\s*试/ })` 同时命中两个按钮，Playwright strict mode 报"resolved to 3 elements" | **是**（由本轮新增按钮引起） | 收紧为正则 `/^重\s*试$/` + `.first()`，断言语义（"失败时展示重试入口"）不变；重跑两个文件 30 passed，随后全量 155 passed |
| 首轮自写 E2E 出现 `TypeError: Cannot read properties of undefined (reading 'length')`（`useExecutionRecords.ts:380`） | 我的 E2E fixture 把 `/events` 返回体写成 `{ events, has_more, total }`，与真实 DTO（`{ task_id, records, total, size, offset }`）不一致 | 测试自身缺陷 | 修正 fixture 为真实 DTO；此为既有代码在"响应缺字段"时的行为，**未修改该生产代码**，也不属于本轮范围 |

### 5.2 定向用例覆盖（对应任务书 §6.1 的每一项）

单元测试（`tests/unit/task-retry.test.ts`，34 项）：

- 路径构造（`/retry` 与 `/retry-eligibility` 是两条不同路径，空标识直接拒绝）；
- 资格请求方法 / 路径 / 单次调用；404、503 原样展示服务端说明；
- 重试请求方法 / 路径 / 请求体只含真实字段；60 秒假时钟推进后请求仍未被中断
  （证明没有客户端超时）；409 保留服务端业务错误；网络中断只发一次、不自动重发；
- `can_retry=true` / `false` 的结论文案；摘要缺省的中性提示；`blocking=null`；
  10 个已知分类各有用中文标签与说明；未知分类原样保留；多个阻塞项逐条呈现；
  未知 `task_status` 原样保留；
- `canSubmitRetry` 的四种禁用条件（无快照 / `can_retry=false` / 快照属于其他任务 /
  在途）与"不依据任务状态推断"；
- `workflow_started=true` 与 `false` 的结论差异、缺 `run` 时不补造、缺 `failure_stage` /
  `failure` 的中性文案、未知阶段与未知状态原样保留、零值时间不被展示、结论不含
  "任务成功"；
- 票据竞态：同一任务只有最后一次生效、切换任务丢弃、取消与卸载后不接受。

E2E（`tests/e2e/task-retry.spec.ts`，10 项）：可重试并真实触发后展示结果、`can_retry=false`
展示全部阻塞且按钮禁用且不发请求、资格查询失败禁用并提供重新查询、`workflow_started=false`
不显示为已启动、请求失败展示服务端错误且不自动重发、刷新失败不覆盖操作结论、重试后资格
按新响应刷新、切换任务后旧任务结论与结果都不出现、打开详情与刷新资格不发出任何写请求。

### 5.3 未执行项

| 项目 | 状态 | 说明 |
|---|---|---|
| Go 测试（`go test`）与 `gofmt` / `go vet` | **未执行** | 本轮未修改任何 Go 代码 |
| 真实后端联调（真实 SQLite + 容器 + Pi） | **未执行** | 用户未要求；E2E 全部为浏览器层拦截 |
| 浏览器人工验收（真实地址访问） | **未执行** | 用户未提供地址，也未要求访问页面 |

---

## 6. 未解决风险与后续工作

1. **`started_at` / `finished_at` 的零值**：Go 端这两个字段没有 `omitempty`，成功路径下
   通常为真实时间，但 `workflow_started=false` 时可能是零值。前端用 `isRealTime` 过滤零值，
   这是**展示层防御**；是否给 Go 端加 `omitempty` 属于后端 DTO 契约决策，本轮未改。
2. **重试结果不确定的用户路径**：客户端超时 / 网络中断时页面只提示"无法确认"并允许手动刷新，
   不回查服务端是否已经重开任务。若要自动核对，需要新增只读的"最近一次重试结果"能力，
   属于新范围，本轮未做。
3. **列表行没有重试入口**：按用户确认的形态，只在任务详情提供；列表行若要重试需要额外的
   资格查询成本与竞态处理。
4. **阻塞分类文案是前端投影**：服务端新增分类时按钮仍按 `can_retry` 正确工作，但中文标签
   会退化为原始代码（有"未定义"提示），需要随后端更新补充文案。
5. **本轮不涉及**：M4-2（代码保存核验入口）、M4-3（容器 `delete` / `detach`）、M4-4
   （`resume-eligibility` 驱动的恢复入口）、M4-5（单步诊断动作）均未实施。

---

## 7. 与任务书要求的一致性说明

- **未改变重试领域规则**：`domain.DecideTaskRetryEligibility`、`TaskStatus.AllowsExplicitRetry`、
  `Task.ReopenForRetry`、`app.TaskRetryService` 一行未改；前端不复制资格判定。
- **没有新增第二套工作流**：重试请求只调用既有 `POST /retry`，服务端内部仍走
  `TaskLeaseService.Claim` + `TaskLaunchService.Launch`；前端不拼接容器准备、工作区初始化、
  计划生成或工作流启动。
- **没有自动重放任何操作**：请求不自动重发；资格查询与详情刷新都是只读；`next_actions`
  （本接口不返回）不会被自动执行。
- **未把以下事实混为一谈**：HTTP 请求成功、任务状态已改变、工作流已启动、最终业务结论
  与收尾闭环在界面上是分别展示的独立事实。
- **未把资格查询并入 5 秒自动刷新**：`autoRefresh` / `liveSnapshot` 未改动。
