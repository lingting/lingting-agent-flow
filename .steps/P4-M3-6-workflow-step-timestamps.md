# P4-M3-6：工作流步骤起止时间补全（G18）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M3`（展示阶段进度、验证结果、失败原因和恢复状态）
- **覆盖缺口**：G18「查看步骤起止时间」
- **范围**：领域时间语义 → SQLite 持久化 → HTTP DTO → 前端步骤列表，四层全部打通。
  未新增表、未新增迁移脚本、未新增接口、未改动工作流成功判定。

---

## 1. 实施前后的 HEAD SHA

| 阶段 | 分支 | HEAD SHA |
|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致，工作区干净） | `0fc652f4d2eb6878b1ba88199491da508873696d` |
| 实施后 | `master`（改动未提交，工作区含本项目修改） | `0fc652f4d2eb6878b1ba88199491da508873696d` |

开始前执行 `git fetch origin`，`git log -1 --oneline origin/master` 返回
`0fc652f 任务执行进度自动刷新`，与任务书给出的 SHA 一致，上游未漂移。本轮改动未提交，
因此实施后 HEAD 未变，改动全部位于工作区（见 §5）。

核对过的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`、
`.steps/P4-M3-7-progress-auto-refresh.md`。

---

## 2. 时间字段的真实数据流与原始缺口

### 2.1 数据流（实施后）

```
domain.Step.Transition(next, at)          领域状态机：唯一写时间的落点
  ├── next.BeginsExecution() → started_at（仅首次，且清空 finished_at）
  └── next.EndsExecution()   → finished_at
        ↓
storage.UpdateStepIfStatus / BeginStepExecution   整行写回 steps 表（带状态前置条件）
        ↓
steps.started_at / steps.finished_at（INTEGER，UTC Unix 毫秒，可空）
        ↓
sqlite.scanStep → domain.Step.StartedAt / FinishedAt
        ↓
app.workflowStepViews → app.WorkflowStepView.StartedAt / FinishedAt
        ↓
httpapi.workflowStatusResponse → WorkflowStepResponse.started_at / finished_at（RFC3339）
        ↓
前端 WorkflowStep.started_at / finished_at（类型已存在）
        ↓
StepListPanel → stepTimes.ts → "开始 X · 结束 Y"
```

步骤列表接口（`GET /tasks/{id}/steps`）走另一条链路
（`httpapi.stepResponse` → `StepResponse.started_at` / `finished_at`，类型为
`*time.Time`），但读取的是同一条 `steps` 记录，因此两个端点不会给出矛盾时间。

### 2.2 原始缺口（按层逐项核实）

| 层 | 实施前状态 | 结论 |
|---|---|---|
| `domain.Step` 结构体 | 已含 `StartedAt` / `FinishedAt` | 字段存在 |
| `domain.Step.Transition` | `if StartedAt == nil && next != pending` → 写开始时间；`if next.IsTerminal()` → 写结束时间 | **语义缺陷**：`failed` / `recovery_required` / `retry_wait` 不是终态，却会结束当次执行周期 → 这三个状态下 `finished_at` 恒为空；重新执行时也不清空上一轮的 `finished_at` |
| SQLite schema | `steps.started_at` / `finished_at` 自 `0001_initial.sql` 起即存在，`0009_relax_constraints.sql` 重建表时原样保留 | **列已存在，无需迁移** |
| SQLite 读写 | `insertStep`、`updateStep`、`scanStep` 全部覆盖两列，`toMillisPtr` / `fromMillisPtr` 用 UTC 毫秒 | 完整，无缺口 |
| `app.WorkflowStepView` | 已含 `StartedAt` / `FinishedAt`，`workflowStepViews` 已赋值 | 完整，无缺口 |
| `httpapi.WorkflowStepResponse` | 声明了 `started_at` / `finished_at`（`string` + `omitempty`），但 `workflowStatusResponse` **从不赋值** | **确认缺口**（G18 原始描述） |
| 前端 `WorkflowStep` 类型 | 已声明 `started_at?` / `finished_at?` | 类型齐备 |
| 前端步骤列表 | `StepListPanel` 展示的是 `step.updated_at`，**从不读取** `started_at` / `finished_at` | **确认缺口** |
| 前端步骤详情 | `StepDetailPanel` 已展示起止时间，但因上游写入缺陷，失败 / 待核验状态下为空 | 受 §2.2 第二行缺陷影响 |

因此本轮的真实缺口是**三处**，而不是只有 HTTP 转换一处：

1. **领域时间语义**：结束时间只覆盖 `IsTerminal()`（`completed` / `skipped` / `cancelled`），
   `failed`、`recovery_required`、`retry_wait` 三种"执行周期已结束但允许重新执行"的状态
   不写结束时间；重新执行也不清空上一轮的结束时间。
2. **HTTP 转换**：`workflowStatusResponse` 不映射两个时间字段（G18 原始描述）。
3. **前端展示**：步骤列表用 `updated_at` 冒充时间，从未读取真实起止时间。

---

## 3. 时间语义定义

### 3.1 两个字段的语义

| 字段 | 语义 | 写入时机 |
|---|---|---|
| `started_at` | 该步骤**首次真正进入执行**的时间 | 迁移到 `running` / `developing` / `verifying` 时写入，且**只在为 nil 时写入一次** |
| `finished_at` | 该步骤**当前执行周期结束**的时间 | 迁移到 `completed` / `skipped` / `cancelled` / `failed` / `recovery_required` / `retry_wait` 时写入；重新进入执行时清空 |

显式不成立的语义：

- `started_at` **不是**计划创建时间（`created_at`），**不是**任务开始时间（`domain.Task.StartedAt`），
  **不是**工作流执行记录的开始时间（`domain.WorkflowRun.StartedAt`）。
- 从未执行的 `pending` 步骤两个字段都为空。
- 从 `pending` 直接 `skipped` / `cancelled` 的步骤**不写** `started_at`：它们表达"这一步没有
  执行过"，写入开始时间会让界面误报"曾经开始执行"。

### 3.2 状态与执行边界（`domain.StepStatus` 上的两个新方法）

| 方法 | 返回 true 的状态 | 说明 |
|---|---|---|
| `BeginsExecution()` | `running`、`developing`、`verifying` | 已经真正开始执行；`verifying` 是开发阶段结束后的验证阶段，不刷新开始时间 |
| `EndsExecution()` | `IsTerminal()`（`completed` / `skipped` / `cancelled`）+ `retry_wait` / `failed` / `recovery_required` | 当前执行周期结束 |

两个方法覆盖 `domain.StepStatus` 的全部 10 个取值，`pending` 与空值两者都返回 false。
规则只在 `internal/domain/step.go` 定义一次，未在应用层、HTTP 层或前端复制。

### 3.3 重试与恢复的语义

| 场景 | 行为 |
|---|---|
| 首次进入执行 | `started_at = T1`，`finished_at = NULL` |
| 执行失败 | `finished_at = T2`（`failed` 也写结束时间） |
| 显式重新执行 | `started_at` 保持 `T1`（首次开始时间不被覆盖），`finished_at` 置回 `NULL`（新周期尚无结束时间） |
| 再次失败 / 完成 | `finished_at = T3`（当前周期的结束时间） |
| 服务重启后占用恢复（`recovery_required`） | 写入 `finished_at`（该周期真的结束了），`started_at` 保留 |
| 同一状态重复迁移 | **被状态机拒绝**（`ErrInvalidTransition`），因此不会刷新时间 |

这等价于「`started_at` = 首次执行时间，`finished_at` = 当前执行周期结束时间」的一对字段
表达，而不是"每个执行周期一行"。选择该语义的理由：

- 现有模型一个 `Step` 只有一行，无法容纳多周期时间序列；把周期历史塞进两个字段只会得到
  无法区分含义的值。
- 周期级历史已由既有 `repair_cycles`（`opened_at` / `closed_at`）与每次重试独立的
  `attempts.started_at` / `attempts.finished_at` 承载，不需要在 `steps` 上重复。
- 完整事件时间线属于 M3-8（需先补 API），本轮不扩展。

### 3.4 时区与精度

统一复用既有约定：`domain.normalizeTime` 截断到毫秒并转 UTC；SQLite 存 UTC Unix 毫秒
（`toMillis` / `fromMillis`）；HTTP 以 `time.RFC3339Nano` 输出 UTC 文本。前端按浏览器本地
时区格式化。

---

## 4. 并发与执行权约束的保持

未新增任何无条件整行写入：

- 步骤时间随 `steps` 行一起写回，所有生产写入路径本来就带状态前置条件
  （`UpdateStepIfStatus`）。
- 开始时间的跨进程落点仍是 `storage.StepExecutionStore.BeginStepExecution`：它在**单个事务**
  内先条件更新步骤状态为 `running`（此时 `Transition` 写入 `started_at`），再条件更新任务，
  保持既有的乐观并发与租约栅栏（`updateTaskForStepExecution` 带 owner + epoch）。
- 时间字段不会因为迟到结果而覆盖已确认的终态：条件更新冲突时调用方读取数据库真实值。
- 有测试专门验证：第二次占用同一步骤返回 `storage.ErrConflict`，第一次写入的 `started_at`
  不被改写。

---

## 5. 修改文件及职责

修改：

| 文件 | 职责 / 改动 |
|---|---|
| `internal/domain/step.go` | 新增 `StepStatus.BeginsExecution()` / `EndsExecution()`；重写 `Step.Transition` 的时间写入规则（开始时间只在真正执行时写、重新执行清空结束时间、`failed` / `recovery_required` / `retry_wait` 写结束时间） |
| `internal/httpapi/workflow.go` | `WorkflowStepResponse` 补注释与字段语义；`workflowStatusResponse` 映射 `started_at` / `finished_at`；新增 `workflowTimeString`（nil → 空字符串，由 `omitempty` 省略字段） |
| `lingting-agent-flow-ui/src/pages/tasks/StepListPanel.tsx` | 步骤行改为展示"开始 … · 结束 …"与摘要行；删除用 `updated_at` 冒充时间的实现；移除本地 `formatTime` |

新增：

| 文件 | 职责 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/stepTimes.ts` | 纯函数时间展示层：`parseStepTime`、`formatStepTime`、`formatStepDuration`、`stepTimeDisplay`、`stepTimeSummary`；缺失 / 非法 / 仍在执行 / 结束早于开始四种情况分别有确定结论 |

未改动：SQLite schema 与迁移脚本（列早已存在且读写完整）、`app` 层的
`workflowStepViews`（本就赋值）、`StepDetailPanel`（已展示真实字段）、任何状态机迁移表、
任何接口路径与错误映射。

---

## 6. 新增与修改的测试

### 6.1 Go

新增 `tests/unit/domain/step_timestamps_test.go`（7 个用例）：新建步骤无时间、只在真实执行时
写开始时间、`pending → skipped` / `cancelled` 不写开始时间、执行周期结束写结束时间、重新执行
保留首次开始并清空结束、同状态迁移被拒绝、两个方法的状态映射全覆盖。

新增 `tests/unit/store/steps_timestamps_test.go`（10 个用例）：使用真实 SQLite 与正式迁移，
覆盖新建为空、`started_at` 落库、`finished_at` 落库、`failed` / `recovery_required` 落库、
重新执行清空结束时间、只改说明字段不清空时间、历史未知状态不伪造时间、`BeginStepExecution`
在事务内写开始时间、重复占用返回冲突且不改写时间、关闭连接后同路径重新打开历史数据保留。

新增 `tests/unit/httpapi/workflow_timestamps_test.go`（6 个用例）：`pending` 步骤不带时间字段、
执行中步骤返回与数据库一致的 RFC3339 开始时间且无结束时间、终态同时返回两个时间、步骤时间
不取任务时间、失败 / 待核验不被序列化成 `completed` 且保留失败摘要与结束时间、步骤列表端点与
工作流端点的起止时间互相一致。

新增 `tests/integration/local/workflow/step_timestamps_test.go`（5 个用例）：真实 SQLite + 真实
`app.TaskWorkflowService` + 真实迁移，覆盖成功路径全部步骤有起止时间且结束不早于开始、
`GetWorkflowStatus` 快照时间与数据库一致、失败路径保留失败摘要与结束时间且工作流不被判为
完成、未执行步骤时间保持为空、时间字段为 UTC 且精度不高于毫秒。

### 6.2 前端

新增 `lingting-agent-flow-ui/tests/unit/task-step-timestamps.test.ts`（15 个用例）：时间解析与
本地时区格式化、时长格式化、未开始/执行中/已结束三种展示、结束早于开始不算耗时、只有结束
时间不补开始时间、缺失时间不显示任何时刻、不用 `created_at` / `updated_at` 冒充、自动刷新
新快照后展示同步更新、刷新失败保留最后快照、未知状态不影响时间结论、失败与待核验照常展示。

新增 `lingting-agent-flow-ui/tests/e2e/task-step-timestamps.spec.ts`（7 个用例，Playwright，
全部用真实 DTO 形状的 API mock，不依赖模型服务与 Docker）：真实起止时间与耗时展示、未开始
显示占位符且不出现任何具体时刻、执行中只显示开始时间、非法时间显示占位符而不显示原始文本、
失败与待核验保留状态与时间且不显示为已完成、自动刷新后结束时间出现、刷新失败保留已展示时间。

### 6.3 实际执行的命令与结果

Go（仓库根目录）：

```
gofmt -l internal cmd tests              → 无输出
go vet ./...                             → 无输出
go build ./...                           → 无输出
go test ./tests/unit/... -count=1        → 1721 passed / 21 packages
go test ./tests/integration/local/... -count=1 → 463 passed / 14 packages
go test ./tests/... -count=1             → 2262 passed / 46 packages
```

前端（`lingting-agent-flow-ui/`）：

```
pnpm test:unit:safe    → 21 files / 376 tests passed
pnpm test:e2e:safe     → 136 passed（workers=1，含本轮新增 7 个）
pnpm check             → vp fmt / vp lint --fix / tsc --noEmit 全部通过
pnpm build             → 构建成功，产物写入 internal/webui/dist
```

本轮另外单独运行了真实外部依赖用例。`TestRemoteE2EFullChain` 在两次独立运行中失败过一次、
通过过一次，失败原因是远程仓库网络不可达，如实记录如下：

```
go test ./tests/integration/pi/... ./tests/integration/ssh/... ./tests/integration/remote/...
  第 1 次 → 19 passed, 1 failed, 1 skipped
    唯一失败：tests/integration/remote/workflow 的 TestRemoteE2EFullChain
      Clone 失败: ssh://github.com/lingting/lingting-agent-flow-test.git |
      Connection closed by 172.20.0.11 port 443
      （失败发生在工作区初始化 clone 阶段，任务仍为 queued，从未进入步骤执行；
        与本次改动的时间字段无关，属远程网络不可达）
  第 2 次（仅该包）→ 3 passed, 1 failed（同一条 clone 网络失败）

go test ./tests/... -count=1 -timeout 45m
  → 退出码 0，46 个包全部 ok，其中
    tests/integration/remote/workflow 144.3s ok（本轮第三次运行，网络恢复后通过）
```

即：`TestRemoteE2EFullChain` 属于网络依赖型用例，本轮先后运行三次，两次失败一次通过，
失败均停在 clone 阶段、早于任何步骤执行，因此不构成本轮回归；最终的全量运行（退出码 0）
是报告中 `2262 passed / 46 packages` 的来源。测试产物目录由 `TEST_ARTIFACT_DIR` 输出，位于
操作系统临时目录下的随机目录并保留：

```
Go：C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-<随机>（含 test.log）
前端单元：C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<随机>
前端 E2E：同上（playwright-output / playwright-report 子目录）
```

---

## 7. 未解决问题、风险与后续工作

1. **`finished_at` 表达"当前周期"而非"首次结束"**：历史文档与部分调用方若曾假设
   `finished_at` 一旦写入就不变，重新执行后该字段会被清空。本轮已把语义写入领域注释与
   交付文档；前端不依赖该字段做终态判断（终态判断始终来自 `status` / `status_terminal`）。
2. **没有回填历史数据**：`failed` / `recovery_required` / `retry_wait` 状态的历史步骤
   `finished_at` 仍为空。不伪造是刻意选择——`updated_at` 不是结束时间，回填会把错误数据
   写成事实。这些步骤在页面上显示"结束 —"，符合"历史缺失"的真实情况。
3. **周期级时间历史仍需 M3-8**：单步骤多周期的完整时间线需要事件 API，本轮不扩展。
4. **步骤详情面板与步骤列表的时间来源不同（但同源数据）**：详情用 `GET /steps` 的
   `*time.Time`，列表用同一端点的同一字段；工作流端点另有一份 `string` 形式的相同值。
   三处都由同一条 `steps` 记录派生，测试已锁定一致性；未来若新增第三个端点，同样必须
   读取 `steps` 而非重新推算。
