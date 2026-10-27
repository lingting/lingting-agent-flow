# P4-M3-8：事件时间线与修复周期历史（G15、G16）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M3`（展示阶段进度、验证结果、失败原因和恢复状态）
- **覆盖缺口**：G15「查看任务事件时间线」、G16「查看修复周期历史与预算消耗」的前端部分
- **范围**：存储查询扩展 → 应用层只读服务 → HTTP 只读接口 → 前端 DTO / 请求封装 /
  两个历史面板，四层全部打通。**未改动工作流执行、重试、修复预算、状态迁移与任何外部
  副作用**，也未新增表或迁移脚本。

---

## 1. 实施前后的 HEAD SHA

| 阶段 | 分支 | HEAD SHA |
|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致，工作区干净） | `1dbb37c8e9bfe411bf8e790f79eaa422a30bb5cf` |
| 实施后 | `master`（改动未提交，工作区含本项目修改） | `1dbb37c8e9bfe411bf8e790f79eaa422a30bb5cf` |

开始前执行 `git fetch origin master`，`git log -1 --oneline origin/master` 返回
`1dbb37c 工作流步骤起止时间补全`，与任务书给出的基线一致，上游未漂移；`git status
--short -uno` 为空。本轮改动未提交，因此实施后 HEAD 未变，全部改动位于工作区（见 §9）。

核对过的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`、
`.steps/P4-M3-6-workflow-step-timestamps.md`、`.steps/P4-M3-7-progress-auto-refresh.md`、
`.steps/P4-M3-1-attempt-session-history.md`。

---

## 2. 实施前的真实缺口（逐项核实结论）

### 2.1 事件模型与存储

| 层 | 实施前状态 | 结论 |
|---|---|---|
| `internal/domain/event.go` | `Event` 含 `ID` / `TaskID` / 可选 `StepID`、`AttemptID`、`SessionID` / `Type` / `Payload` / `CreatedAt`；`NewEvent` 与 `Validate` 只接受 `knownEventTypes` 中的 43 个类型 | 模型完整 |
| `internal/storage/interfaces.go` `EventFilter` | 只有 `StepID` 与 `Limit` | **缺分页偏移、缺类型筛选、缺总数** |
| `internal/storage/sqlite/events.go` `ListEventsByTask` | `ORDER BY id` | **排序只按写入顺序，不是 `(created_at, id)`** |
| HTTP 入口 | 无 | **`ListEventsByTask` 生产代码零调用者**（只有测试调用），G15 完全缺失 |
| 前端 | 无 DTO、无请求封装、无面板 | 缺失 |

排序问题的实际影响：事件时间取自业务时钟（`domain.NewEvent` 的 `at` 参数由各服务用
`s.now()` 给出），`id` 只反映写入先后。只按 `id` 排序会让"补写的更早事件"出现在时间线
末尾，与用户看到的时刻顺序相反。

### 2.2 事件 payload 的真实结构（安全核查）

逐写入点核实生产代码中的 `domain.NewEvent` 调用（21 处），payload 由
`marshalStepExecutionPayload(map[string]any{...})` 构造，实际出现过的键共 60 余个。
其中包含**不可对外输出**的字段：

- `work_dir`（`/workspace/project` 等容器内路径）——`code_initialize.go`、`task_workspace.go`、
  `validation_run.go`；
- `repository`（仓库地址，`domain.ParseGitRepositoryURL` 会拒绝内嵌凭据，但仍属内部配置）；
- `container_id`、`record_id`、`attempt_id` 等内部登记标识；
- `error_summary`、`summary`（可能含命令输出片段或凭据形态文本）。

因此**不直接把 `payload_json` 输出到 Web**，改为白名单摘要（见 §4.1）。

### 2.3 事件写入的真实性核查

- 事件只追加、不修改、不删除（表无 `UPDATE`/`DELETE` 路径）；
- 事件写入失败**只记录日志，不回滚状态**（`task.go`、`task_workflow.go`、`step_executor_run.go`、
  `validation_run.go` 等处的注释与代码一致），因此**事件缺失不等于业务未发生**，
  接口契约必须显式说明这一点；
- `TaskService.CreateTask` 的 `task.created` 事件与任务写入在**同一事务**内
  （`tx.AppendEvent`），不会出现"有任务没创建事件"；
- 未发现同一事实重复写入的非幂等路径；`workflow.blocked` 在启动恢复时可能对同一条记录
  重复追加（每次启动各一条），这是**真实的多轮恢复事实**，不是重复写入错误；
- 事件时间来自业务时钟而非数据库时间，与 `created_at` 等字段可能相差毫秒级；排序采用
  `(created_at, id)` 而非单纯 `id` 正是为此。

### 2.4 修复周期模型与存储

| 层 | 实施前状态 | 结论 |
|---|---|---|
| `internal/domain/repair_cycle.go` | `RepairCycle` 字段完整，`Active()` = `ClosedAt == nil` | 模型完整 |
| `internal/storage/sqlite/repair_cycles.go` `ListRepairCyclesByStep` | `WHERE step_id = ? ORDER BY sequence` | 排序稳定，但**没有任何 HTTP 入口** |
| HTTP 入口 | 只有写入口 `POST .../open-repair-cycle` | **只读历史缺失** |
| 前端 | 只有 `openRepairCycle`（写），无历史查询 | 缺失 |

关键事实核实：

1. **归属**：`ListRepairCyclesByStep` 只按 `step_id` 过滤，不做任务校验；归属必须由应用层
   承担（`step_workflow_cycle.go` 的 `loadRepairBudgetFacts` 已经是这样做的）。
2. **活动周期唯一性**：`0022_repair_cycles.sql` 的
   `idx_repair_cycles_active_step ... WHERE closed_at IS NULL` 部分唯一索引保证同一步骤
   同一时刻最多一个活动周期；`(step_id, sequence)` 唯一索引保证序号唯一。
3. **`ClosedAt == nil` 的语义**：只表示"该周期还没有记录结束时间"，**不表示成功、失败或
   仍在执行**。关闭有两个真实入口：`StepWorkflowService.OpenRepairCycle`（开新周期前
   关闭旧周期）与 `step_workflow_repair.go` 的修复循环收尾。因此不能从"尝试数"推断
   周期是否结束。
4. **`Sequence` 与 `Attempt.BudgetScopeID`**：周期 `ID` 同时是 `Attempt.BudgetScopeID` 的取值，
   `domain.DecideRepairBudget` 只统计 `BudgetScopeID == 当前周期 ID` 的尝试。历史尝试的
   `budget_scope` 为 `NULL`（迁移 0022 明确不回填），表示周期归属无法从持久化事实确定。
5. **旧数据兼容**：`budget_scope` / `budget_scope_id` 为 `NULL` 的历史尝试不会出现在任何
   周期的统计里，但 `repair_cycles` 表本身的历史记录是完整可读的。
6. **无需额外关联 Attempt**：周期列表自身已经能解释"开过几次周期、每次上限多少、何时开
   何时关"；尝试归属由 `attempts.budget_scope_id` 表达，属于执行记录查询的范围，本阶段
   不重复在周期面板里展示。

---

## 3. 两个只读 API 的契约

路径沿用仓库既有风格（全部挂在 `/api/v1/tasks/{taskID}` 之下，资源名用复数）：

```
GET /api/v1/tasks/{taskID}/events
GET /api/v1/tasks/{taskID}/steps/{stepID}/repair-cycles
```

### 3.1 事件时间线

查询参数（只支持经过核实的三项；时间范围、按会话筛选等当前没有真实场景，不引入）：

| 参数 | 说明 |
|---|---|
| `type` | 事件类型筛选，支持重复参数与逗号分隔；**不校验是否为当前版本定义的类型**，历史类型必须仍可查询 |
| `size` | 页大小，默认 20，上限 100；超过上限返回 400（不静默截断） |
| `offset` | 跳过的条数，默认 0；负数返回 400 |

排序固定为 `(created_at, id)` 升序，是**唯一稳定的定义**：时间来自业务时钟、`id`
单调递增且唯一，因此这个键既符合时间线语义，又保证同一毫秒内多条事件有确定先后，
分页时不漏行、不重复行。

响应：

```json
{
  "task_id": "…",
  "records": [
    {
      "id": 42,
      "task_id": "…",
      "step_id": "…",           // 仅当事件真实关联步骤
      "attempt_id": "…",        // 同上
      "session_id": "…",        // 同上
      "event_type": "task.created",
      "event_label": "任务创建",   // 未知类型缺省
      "type_known": true,
      "summary": "…",            // 经审查、脱敏、截断的摘要；无可公开内容时缺省
      "created_at": "2026-10-27T…Z"
    }
  ],
  "total": 120,
  "size": 20,
  "offset": 0
}
```

`payload` **不出现在响应中**（字段根本不存在，不是空值）。

错误语义：

| 情况 | 状态码 | `error.code` |
|---|---|---|
| 任务不存在或已逻辑删除 | 404 | `not_found` |
| `size` 非正整数 / 超过上限 / `offset` 非法 | 400 | `invalid_request` |
| 非 GET/HEAD | 405 | `method_not_allowed` |
| 历史查询服务未装配 | 503 | `internal_error` |
| 存储错误 | 500 | `internal_error`（只写日志，不返回底层细节） |

契约明确说明：**空列表只表示该任务没有匹配的事件记录，不能据此推断任务从未执行**——
事件写入失败只记日志，不回滚业务状态。

### 3.2 修复周期历史

无查询参数，返回全部已登记周期（按 `sequence` 升序），不分页：周期只由显式开启动作
产生，数量天然很小。

```json
{
  "task_id": "…",
  "step_id": "…",
  "records": [
    {
      "id": "…",                 // 同时是 Attempt.BudgetScopeID 的取值
      "task_id": "…",
      "step_id": "…",
      "sequence": 1,
      "max_attempts": 3,         // 周期开启时的上限快照
      "opened_by": "user_reopen",
      "opened_at": "2026-10-27T…Z",
      "closed_at": "2026-10-27T…Z"   // 缺省表示尚未记录
    }
  ],
  "total": 1
}
```

错误语义：任务不存在 / 步骤不存在 / **步骤不属于该任务** 一律 404（不区分，避免用状态码
差异探测其他任务）；非 GET/HEAD 405；服务未装配 503；存储错误 500。

`closed_at` 缺省而非 `null`：前端据此显示中性占位符，**不推断成功、失败或仍在执行**。

---

## 4. 数据安全、归属校验与排序策略

### 4.1 事件摘要白名单（`internal/app/history_query.go`）

原始 payload 一律不输出。摘要只从 8 个经审查的字段按固定优先级提取，最多 3 段：

```
summary, repair_summary, reason, failure_stage, error_class, status, outcome, verdict
```

- 这些字段在全部 21 个写入点中**均不含路径、仓库地址、容器标识、命令输出或凭据**；
- 值经 `domain.ClipFeedbackText` 处理（先脱敏、再按 512 字符截断），凭据形态文本被替换
  为 `***已隐藏***`；
- 只接受字符串 / 布尔 / 数字；`nil`、对象与数组丢弃（嵌套结构无法逐字段审查）；
- 解析前先按 `MaxEventPayloadBytes = 4096` 截断，避免为一条摘要载入超长历史负载；
- 白名单之外的字段（`work_dir`、`repository`、`container_id`、`error_summary`、`record_id` 等）
  **默认不可见**——上游"顺手多写一个字段"不会自动变成对外泄露。

未知事件类型：保留原始 `event_type`、`type_known=false`、`event_label` 缺省，摘要按同一
白名单提取。这样历史类型仍可被区分与统计，不是被压成统一的"未知事件"。

### 4.2 归属校验（`internal/app/history_query.go`）

两处都是"先任务、后步骤、再比对归属"，与 `ExecutionRecordService` 的既有约定一致：

- 事件查询：`requireHistoryTask`（`GetTask` → `ErrNotFound` 或 `Deleted()` 都映射为
  `ErrHistoryNotFound`）；
- 周期查询：`requireHistoryStep`（`requireHistoryTask` → `GetStep` → `step.TaskID != taskID`
  返回 `ErrHistoryNotFound`）。

只校验 `taskID` 与 `stepID` 各自存在是不够的：那会让 A 任务的路径读到 B 任务的步骤历史。
`storage.ErrNotFound` 与"归属不符"返回**同一错误类别**，避免探测。

### 4.3 分页归一化

`normalizeHistoryPaging` 复用 `DefaultTaskPageSize`（20）与 `MaxTaskPageSize`（100），
超上限返回 `ErrHistoryInvalidInput` → 400，而不是静默截断或读全表。`total` 由独立的
`CountEventsByTask` 给出，前端据它判断"是否还有下一页"，不用当前页条数推算。

### 4.4 只读性

两个接口都不追加事件、不开启/关闭周期、不创建 Attempt/Session、不建立 SSH 或 Docker
会话、不执行 Git 操作、不推进任何状态。`TestHistoryQueriesDoNotMutateState`（应用层）与
`TestHistoryEndpointsAreReadOnly`（HTTP 层）逐项断言任务状态、`updated_at`、步骤状态、
周期记录、尝试数量与事件数量在重复查询后完全不变。

---

## 5. 前端接入范围

| 文件 | 变更 |
|---|---|
| `src/lib/api/types.ts` | 新增 `TaskEvent`、`EventTimelineResponse`、`RepairCycle`、`RepairCycleHistoryResponse` |
| `src/lib/api/execution.ts` | 新增 `taskEventsPath`、`stepRepairCyclesPath`、`EventTimelineQuery`、`fetchTaskEvents`、`fetchStepRepairCycles` |
| `src/lib/api/index.ts` | 导出上述类型与函数 |
| `src/pages/tasks/history.ts`（新增） | 纯函数：事件文案映射、时间格式化、摘要占位符、关联标识提取、分页合并去重、`hasMoreEvents`、`repairCycleDisplay` |
| `src/pages/tasks/useExecutionRecords.ts` | 新增 `useTaskEvents`（分页累积 + 陈旧响应丢弃）与 `useStepRepairCycles`（步骤切换清空 + 失败保留快照） |
| `src/pages/tasks/EventTimelinePanel.tsx`（新增） | 时间线面板：四种状态、手动刷新、加载更多、未知类型提示、中性摘要占位符 |
| `src/pages/tasks/RepairCyclePanel.tsx`（新增） | 周期面板：序号、起止时间、来源、上限、标识，未关闭显示中性说明 |
| `src/pages/tasks/TaskExecutionPanel.tsx` | 挂载 `EventTimelinePanel`（任务维度，独立于步骤选择） |
| `src/pages/tasks/StepDetailPanel.tsx` | 挂载 `RepairCyclePanel`（步骤维度，`recordsToken` 变化时重读） |

关键设计与约束：

- **事件时间线不进入 5 秒刷新循环**：`useTaskEvents` 只在挂载、手动刷新、"加载更多"与
  父组件在服务端事实真的变化时给出的 `stepsToken` 递增时读取。E2E 用例在等待超过一个
  完整刷新周期（6.2 秒）后断言请求次数不变；
- **陈旧响应丢弃**：`useTaskEvents` 用 `AbortController` + 任务标识比对，
  `useStepRepairCycles` 在 `task/step` 变化时清空累积结果并 abort 旧请求。E2E 用例让旧步骤
  的响应延迟 800ms 返回，断言它不覆盖新步骤的周期列表；
- **失败不伪装成空历史**：首次失败进入 `error` 状态并给出"重试"入口；已有数据时保留
  上一次成功快照并单独提示"刷新失败，下面展示的可能是旧数据"；
- **空列表文案明确边界**：事件为空说明"列表为空不代表任务从未启动"；周期为空说明
  "列表为空不代表该步骤从未执行"；
- **不根据历史推断状态**：`closed_at` 缺失只显示"尚未记录结束时间"，不出现"进行中"
  "成功""失败"等由前端推断的周期结论。

---

## 6. 修改文件清单

新增：

- `internal/app/history_query.go`
- `internal/httpapi/events.go`
- `tests/unit/app/history_query_test.go`
- `tests/unit/httpapi/history_test.go`
- `lingting-agent-flow-ui/src/pages/tasks/history.ts`
- `lingting-agent-flow-ui/src/pages/tasks/EventTimelinePanel.tsx`
- `lingting-agent-flow-ui/src/pages/tasks/RepairCyclePanel.tsx`
- `lingting-agent-flow-ui/tests/unit/task-event-history.test.ts`
- `lingting-agent-flow-ui/tests/e2e/task-event-history.spec.ts`
- `.steps/P4-M3-8-event-timeline-repair-cycle-history.md`（本文档）

修改：

- `internal/domain/event.go`（新增 `EventType.Known()`）
- `internal/storage/interfaces.go`（`EventFilter` 增加 `Types` / `Offset`；`EventStore` 增加
  `CountEventsByTask`；`ListEventsByTask` 排序契约写入注释）
- `internal/storage/sqlite/events.go`（排序改 `(created_at, id)`、类型筛选、偏移分页、
  总数统计）
- `internal/httpapi/router.go`（装配 `History` 依赖与两个只读路由）
- `internal/httpserver/server.go`、`internal/server/server.go`（依赖组装）
- `lingting-agent-flow-ui/src/lib/api/{types.ts,execution.ts,index.ts}`
- `lingting-agent-flow-ui/src/pages/tasks/{useExecutionRecords.ts,TaskExecutionPanel.tsx,StepDetailPanel.tsx}`
- `tests/unit/httpapi/executions_test.go`（新增 `handlerWithHistory` 辅助）
- `tests/unit/store/events_test.go`（新增 3 个存储用例）
- `lingting-agent-flow-ui/tests/unit/task-execution-api.test.ts`（新增 7 个请求层用例）

**未新增表、未新增迁移脚本**：`events` 与 `repair_cycles` 表结构自 `0001_initial.sql` 与
`0022_repair_cycles.sql` 起已完整，本轮只补查询与展示。

---

## 7. 测试命令及实际结果

### 7.1 Go

| 命令 | 结果 |
|---|---|
| `go test ./tests/unit/app/ -run 'TestListTaskEvents\|TestListStepRepairCycles\|TestHistoryQueriesDoNotMutateState\|TestEventTypeLabel' -count=1` | 通过（15 个用例） |
| `go test ./tests/unit/httpapi/ -run 'TestListEvents\|TestListRepairCycles\|TestHistoryEndpoints' -count=1` | 通过（12 个用例） |
| `go test ./tests/unit/store/ -run 'TestListEvents\|TestListEventsByTypeAndOffset\|TestListEventsKeepsUnknownEventType' -count=1` | 通过（5 个用例） |
| `go test ./tests/unit/... -count=1` | **通过：1751 通过，21 个包** |
| `go test ./tests/integration/local/... -count=1` | **通过：463 通过，14 个包** |
| `go vet ./...` | 无问题 |
| `go build ./...` | 成功 |
| `gofmt -l internal cmd tests` | 无输出（全部已格式化） |

本轮新增 Go 测试用例：应用层 15 个、HTTP 层 12 个、存储层 3 个。

覆盖点与本任务要求逐项对应：

- 事件：正常查询、`(created_at, id)` 稳定排序（含同一时间戳）、分页不重不漏、
  `size` 超上限拒绝、类型筛选（含历史未知类型）、跨任务隔离、任务不存在、
  空/不可公开负载、敏感字段不泄露（断言 `supersecret`、`/workspace/project`、
  `user:pass` 均不出现且出现 `***已隐藏***`）、未知类型安全展示；
- 周期：无历史、单个已关闭、多周期稳定顺序、`closed_at == nil` 真实表示、
  跨任务归属拒绝、步骤不存在、查询后任务/步骤/周期/尝试/事件数量全部不变。

### 7.2 前端

| 命令 | 结果 |
|---|---|
| `pnpm exec vp test run --maxWorkers 1 tests/unit/task-event-history.test.ts` | 通过（26 个用例） |
| `pnpm exec vp test run --maxWorkers 1 tests/unit/task-execution-api.test.ts` | 通过（28 个用例，含新增 7 个） |
| `pnpm test:unit:safe` | **通过：409 通过，22 个文件** |
| `pnpm exec playwright test tests/e2e/task-event-history.spec.ts --workers=1` | 通过（9 个用例） |
| `pnpm test:e2e:safe` | **通过：145 通过，0 失败**（`--workers=1`） |
| `pnpm check`（fmt + lint --fix + tsc --noEmit） | 通过 |
| `pnpm build` | 成功，产物写入 `internal/webui/dist` |

E2E 逐条对应任务书 §5.4 的八项要求：

1. 任务详情显示事件时间线（`任务详情展示真实事件时间线与中文说明`）；
2. 事件查询失败后可重试（`事件查询失败后可重试，失败不伪装成空历史`，断言失败状态可见、
   空历史文案不出现、重试后拿到真实数据）；
3. 未知事件类型仍能显示（`未知事件类型仍能显示，并说明类型未在当前版本定义`）；
4. 选择步骤后显示对应修复周期历史（`选择步骤后显示该步骤的修复周期历史`）；
5. 无历史周期与未关闭周期的中性展示（`没有周期与未关闭周期都使用中性文案` +
   第一条用例中的"尚未记录结束时间"断言）；
6. 切换步骤后旧响应不覆盖新数据（`切换步骤后旧响应不覆盖新数据`，旧响应延迟 800ms）；
7. 自动刷新不会导致事件历史被无条件重复加载（`自动刷新不会无条件重复加载事件历史`，
   等待 6.2 秒后请求次数不变）；
8. 浏览与刷新流程不触发任何副作用（`浏览与刷新历史不触发任何执行类请求`，拦截全部
   `/api/v1/` 非 GET 请求并断言为空）。

### 7.3 测试产物路径（`TEST_ARTIFACT_DIR`）

本轮运行的产物全部保留在 OS 临时目录，未被删除：

- Go：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\`（每个测试包一个
  随机子目录，含 `test.log` 与 `latest-run.txt`）；
- 前端单元测试：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-*\`
  （每次运行一个随机目录）；
- Playwright：同上目录下的 `playwright-output/`、`playwright-report/`（含失败用例的
  截图、trace 与 `error-context.md`）。

本轮**未执行**真实 Pi、真实 SSH、真实远程 Docker 与真实模型相关测试（`tests/integration/pi/`、
`tests/integration/ssh/`、`tests/integration/remote/`、`tests/e2e/` 之外的远端用例）：
本任务不涉及这些依赖，`.steps/P4-M3-8` 的验收也不需要它们。这些测试**保持未执行状态，
不视为通过**。

---

## 8. 完成定义核对

| 条件 | 结论 | 证据 |
|---|---|---|
| 事件时间线能从真实持久化事件完整、安全地查询与展示 | 满足 | 事件全部来自 `events` 表；`payload` 不输出，只有白名单摘要；§7.1 的安全断言 |
| 修复周期历史能从真实持久化记录查询与展示 | 满足 | `ListRepairCyclesByStep` 原样返回；`closed_at` 缺失如实表达 |
| API 正确校验任务和步骤归属 | 满足 | `requireHistoryTask` / `requireHistoryStep`；跨任务用例断言 404 |
| 前端能正确区分空历史、加载失败和真实记录 | 满足 | 四种状态分别渲染；失败态有重试、就绪态可带"刷新失败"提示 |
| 自动刷新与页面切换没有产生竞态或无谓的历史查询负载 | 满足 | 6.2 秒无重复请求；800ms 延迟响应不覆盖新步骤 |
| 测试证据足以支持完成结论 | 满足 | §7 全部命令与真实结果 |

**M3-8 判定为 `completed`。**

---

## 9. 尚未解决的风险与限制

1. **事件缺失不可见**：事件写入失败只记日志、不回滚状态，因此时间线可能存在"业务发生过
   但没有事件"的缺口。当前契约只在文案上说明这一点，没有服务端侧的缺口检测指标。若要
   在页面上区分"没有事件"与"事件丢失"，需要在写入侧引入失败计数（属后续阶段）。
2. **摘要字段是硬编码白名单**：新增事件或新增 payload 字段时，如果该字段应该进入时间线，
   必须同步更新 `eventSummaryFields`。白名单本身不会报错，只会让新字段静默不可见——这是
   有意的安全默认，但需要维护者知道。
3. **`repair_cycles` 历史记录的 `opened_by` 未做取值枚举**：数据库不设枚举约束是项目既定
   规则，接口原样返回标签，前端也原样显示。若未来出现非 `user_reopen` 的来源标签，页面
   会直接显示原始标签而不是伪造中文说明。
4. **周期面板不展示尝试归属**：`Attempt.BudgetScopeID` 与周期的对应关系已经可从执行记录
   查询推断，但本面板刻意不重复展示，避免与步骤详情的尝试列表重复。真正需要"这一轮消耗
   了哪些尝试"时可以再增补。
5. **`count` 与 `list` 之间的事件写入**：两个查询分开执行，若在两次查询之间新增事件，
   `total` 与当前页内容可能短暂不一致（既有多数列表接口都有同样特性）。前端按 `id`
   去重合并，因此不会出现重复行。
6. **前端未做事件类型筛选 UI**：接口支持 `type` 筛选，页面当前不暴露入口。这是有意的范围
   控制（任务书要求"仅允许经过核实的筛选参数"，未要求筛选 UI）；需要时可直接用现成的
   `fetchTaskEvents({ type: [...] })`。
7. **E2E 用例数继续增长**：本轮新增 9 个 E2E 用例（136 → 145），全量 E2E 已需约 5.7 分钟。
   若继续增长，应按 `.steps/P4-frontend-test-resource-governance.md` 的观测结论评估分组或
   按需运行策略，不得通过提高并发度掩盖耗时。
