# P3-M2 —— 实现有界并发和任务生命周期协调

> 撰写依据：`master` HEAD `d8a0fdd`（P3-M1 之后）的工作区真实代码与测试状态。
> 上游依据：[`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md)、
> [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md)、
> [`P2-M5-closeout-protocol.md`](./P2-M5-closeout-protocol.md)、
> [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)。

## 0. 里程碑标识

| 项 | 值 |
|---|---|
| ID | `P3-M2` |
| 名称 | 实现有界并发和任务生命周期协调 |
| 状态 | `completed`（2026-10-25） |
| 前置依赖 | P3-M1（`completed`） |
| 必要性 | 无界并发会耗尽执行环境与模型额度；两套所有权会让旧执行者覆盖新领取者的状态 |
| 本里程碑不做的 | 跨任务隔离规则（P3-M3）、调度器重启接管（P3-M4）、多任务验收（P3-M5）、分布式配额 |

## 1. 交付概览

P3-M1 只交付领域契约：`AcquireTaskLease` / `ReleaseTaskLease` 无任何生产调用方，
`UpdateTaskIfStatus` 不含租约条件，任务租约与 `WorkflowRun` 租约的 owner 完全无关。
P3-M2 把这三件事全部落地：

1. **存储层 fencing**：新增 `tasks.lease_epoch`（迁移 `0024`），领取时原子递增，
   续期 / 释放 / 执行期状态写入都以 `owner + epoch` 为条件。
2. **统一所有权**：任务租约是唯一令牌，`WorkflowRun.WorkerID` 取自任务租约 owner，
   心跳同时续租两者，任一失效立即停止推进。
3. **有界并发调度器**：`app.TaskScheduler` 作为 `internal/lifecycle` 后台服务，
   复用领域准入 / 排序 / 名额规则，只调用既有 `TaskLaunchService.Launch`。
4. **显式入口审计**：HTTP 启动 / 计划 / 工作流启动 / 恢复四个入口都先领取任务租约，
   不再存在绕过唯一所有权的执行路径。

## 2. M2-1：任务租约与存储层 fencing

### 2.1 领取代次（`lease_epoch`）

`tasks.lease_epoch` 是**内部栅栏**，不是用户可控参数：

- 迁移 `0024_task_lease_epoch.sql`：`ALTER TABLE tasks ADD COLUMN lease_epoch
  INTEGER NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0)`。默认值 `0` 表示"从未被领取"，
  真正的领取从 `1` 开始，因此 `0` 永远不会被误认为一次有效领取。
- 只使用 `ALTER TABLE ADD COLUMN`，不重建 `tasks` 表：既有数据、索引与触发器原样保留。
- 不声明枚举白名单 `CHECK`；保留的是非枚举性质的数值范围约束。

### 2.2 原子领取

`AcquireTaskLease` 的语义（`internal/storage/sqlite/tasks.go`）：

```sql
UPDATE tasks
   SET lease_owner = ?, lease_expires_at = ?, lease_epoch = lease_epoch + 1, updated_at = ?
 WHERE id = ? AND deleted_at IS NULL AND status IN (...)
   AND (lease_owner IS NULL
        OR lease_expires_at IS NULL
        OR lease_expires_at <= ?
        OR lease_owner = ?)
```

关键点：

- 条件同时覆盖**删除状态、任务状态、租约可用性**，由单条 UPDATE 判定，因此并发
  领取最多只有一个成功；其余返回 `storage.ErrConflict`。
- `Statuses` 为空时返回 `storage.ErrInvalidData`：**不再回退到包含
  `failed` / `paused` / `recovery_required` 的默认集合**。这是 P3-M1 验收项 S7 的落地。
- 领取成功后必须回读代次（`readLeaseEpoch`）：代次由数据库在同一语句内递增，调用方
  无法预先知道结果。回读条件同时覆盖 owner 与有效期，若在读取前被接管则按失败处理。
- 返回的 `domain.TaskLeaseHandle{TaskID, Owner, Epoch}` 是数据库确认过的所有权身份，
  调用方不得自行构造。

### 2.3 续期与释放

| 操作 | SQL 条件 | 失败语义 |
|---|---|---|
| `RenewTaskLease` | `id` + `deleted_at IS NULL` + `lease_owner` + `lease_epoch` + `lease_expires_at > now` | `storage.ErrConflict` |
| `ReleaseTaskLease` | `id` + `lease_owner` + `lease_epoch` | 返回 `false` + `storage.ErrConflict` |
| `UpdateTaskLeaseGuard` | `id` + `status IN (...)` + `lease_owner` + `lease_epoch` | `storage.ErrConflict` |

三条硬性语义：

1. **过期后不得通过迟到续期恢复所有权**：续期条件包含 `lease_expires_at > now`，
   租约一旦过期，旧持有者的续期必然 0 行受影响。
2. **旧代次不得释放新持有者的租约**：释放条件包含 `lease_epoch`，代次不匹配时不命中
   任何记录，因此不会清除新持有者的租约。
3. **"没有释放任何东西"不是成功**：`ReleaseTaskLease` 返回 `(bool, error)`，
   `domain.CheckTaskLeaseRelease` 把"未命中"翻译成 `TaskLeaseReleaseConflict` + 错误，
   上层不得据此认为名额已经空出。

### 2.4 任务写入路径审计

| 调用方 | 写入接口 | 是否带租约条件 | 理由 |
|---|---|---|---|
| `TaskWorkflowService.completeTask` | `UpdateTaskLeaseGuard` | ✅ | 成功终态必须由仍持有执行权者写入 |
| `TaskWorkflowService.beginPlanning` | `UpdateTaskLeaseGuard` | ✅ | 计划生成会真实调用 Planner |
| `TaskWorkflowService.completeStalledTask` | `UpdateTaskIfStatus`（`grant = nil`） | ❌ 有意 | 执行记录已是终态且 `WorkerID` 已清空，没有在途执行者；只按状态条件补齐终态 |
| `TaskService.CancelTask` | `UpdateTask` | ❌ 有意 | 用户取消必须能持久化取消意图，不能要求用户持有执行权 |
| `StepExecutor` 终态写入 | `UpdateTaskIfStatus` | ❌ 待 P3-M3/M4 | 步骤执行器不是任务领取者；其写入依赖状态条件，见 §7 剩余风险 |
| 启动恢复（`recover*`） | `UpdateTaskIfStatus` | ❌ 有意 | 恢复按持久化事实收敛，不重放外部操作 |
| `TaskService` 管理接口 | `UpdateTaskIfStatus` / `DeleteTask` | ❌ 有意 | 管理操作不是执行期写入 |

**取消意图的保护**：用户取消走 `UpdateTask`（无条件），而执行者的成功终态写入走
`UpdateTaskLeaseGuard`（带 owner + epoch）。因此"旧执行者把取消意图覆盖掉"不会发生：
旧代次的写入在租约条件上就被拒绝，而取消请求写的是最新记录。

### 2.5 可追溯性

新增两个事件类型：`task.claimed`（`EventTypeTaskClaimed`）与
`task.lease_released`（`EventTypeTaskReleased`）。

- 领取事件与租约写入由 `TaskLeaseService.Claim` 在领取成功后立即追加；
  `AppendTaskLeaseEvent` 在单个事务内写入事件，避免"有效租约但没有审计痕迹"。
- 事件写入失败只记录日志、不回滚租约：状态是唯一真相来源，审计失败不应让一次
  真实领取被伪装成"没领取"。
- payload 包含 `owner`、`epoch`、`takeover`、`claimed_at`、`expires_at`，
  释放事件额外包含 `reason` 与 `release_kind`。

## 3. M2-2：统一任务租约与 WorkflowRun 执行权

### 3.1 唯一令牌

`app.WorkflowExecutionGrant` 是任务执行权的**内部凭据**：

```go
type WorkflowExecutionGrant struct {
    Lease domain.TaskLeaseHandle
    TTL   time.Duration
}
```

- 它由 `TaskLeaseService.Claim` 产生，只在应用层内部传递；HTTP / CLI 请求结构体
  不解析 `owner` / `epoch`，因此不存在"调用方可指定所有权"的信任漏洞。
- `WorkflowRun.WorkerID` 直接取自 `grant.Lease.Owner`（`beginRunRecord` 调用
  `run.Start(grant.workflowOwner(), ...)`），这就是"两套所有权合并为一套"的落点。

### 3.2 交接校验

`StartWorkflow` / `ResumeWorkflow` 在执行前调用 `checkTaskLease`，它复用
`domain.DecideTaskExecutionHandover`：

1. 任务已删除 → 拒绝；
2. 已请求取消 → 拒绝；
3. 不持有有效任务租约（owner + epoch + 有效期）→ 拒绝；
4. 存在由其他执行者持有有效执行权的工作流记录 → 拒绝；
5. 状态不允许推进步骤 → 拒绝。

`GeneratePlan` 与 `TaskLaunchService.Launch` 从 `queued` 开始，因此使用
`checkTaskOwnership`：只校验删除、取消与执行权，不要求任务已处于可推进步骤的状态。

### 3.3 双租约心跳

`TaskWorkflowService.startLeaseHeartbeat` 每个周期**先续任务租约、再续工作流租约**：

- 任务租约续期失败 → 记录日志、`cancel(errWorkflowLeaseLost)`、停止推进；
- 工作流租约续期失败 → 同样停止推进。

只续其中一方会让"任务已被接管、执行记录仍显示有效"的两套所有权重新出现，因此
两者必须同时维持。`stopForInterruption` 对 `errWorkflowLeaseLost` 不写任何终态：
数据库里的记录已属于别的执行者，本次推进只停止。

### 3.4 收尾栅栏与租约的关系

P2-M5 的收尾协议不变：**执行记录的条件写入（`revision`）是栅栏，通过后才允许写
任务成功终态**。P3-M2 在任务侧追加了第二道栅栏：

```text
run.Complete + persistRun（revision 条件）
  → completeTask（每步 UpdateTaskLeaseGuard：status + owner + epoch）
```

两道栅栏条件不同但目标一致：任一失效都说明"我已经不再拥有这条记录"，此时写任务成功
正是"丢失执行权仍宣布成功"的成因。因此 P3-M2 没有削弱 P2-M5 的版本条件，而是叠加了
任务级所有权条件。

### 3.5 旧执行者迟到结果的处理

| 迟到动作 | 结果 |
|---|---|
| `RenewTaskLease` | 0 行 → `ErrConflict` → 停止推进 |
| `ReleaseTaskLease` | 0 行 → 冲突 → 不清除新持有者租约 |
| `UpdateTaskLeaseGuard`（写 `completed`） | 0 行 → `ErrConflict` → 不写成功终态 |
| `UpdateWorkflowRun` | `revision` 不匹配 → `ErrConflict` → 不覆盖新记录 |
| commit / push / merge | 不会发生：副作用只在 `advance` 循环内、且循环在心跳失败时被取消 |

**明确边界**：新代次写入数据库不等于旧进程中的远程操作已被物理取消。P3-M2 通过
取消信号与心跳停止**阻止继续推进**，但已经发出的 `docker exec` / Git 命令是否结束
取决于下层执行器；该现场由 P2-M2 的中断语义与 P3-M4 的接管策略处理。

## 4. M2-3：有界并发调度器

### 4.1 结构

`internal/app/task_scheduler.go` 的 `TaskScheduler`：

```text
Scan
  ├─ countOccupiedSlots   ← domain.DecideTaskSlotState（数据库事实）
  ├─ loadCandidates       ← queued + DecideTaskQueueAdmission + SortTaskQueueOrderKeys
  └─ dispatch（逐个候选，直到名额耗尽）
        ├─ leases.Claim（Statuses = AutomaticSchedulingStatuses()）
        ├─ DecideTaskClaim 复检（沿用候选加载的事实）
        ├─ register + task.claimed 事件
        └─ go run → Launch（带 grant）+ 启动期心跳
```

职责边界：调度器**不**准备容器、不初始化工作区、不生成计划、不执行步骤、不保存代码、
不创建或接管容器。它只加载事实、调用领域规则、调用既有启动流程。

### 4.2 候选与排序

- 候选集合固定为 `domain.AutomaticSchedulingStatuses()`（只有 `queued`）。
- 逐条调用 `domain.DecideTaskQueueAdmission`，输入是真实加载的需求、步骤、执行记录与
  代码保存记录；读取失败时**跳过该候选并记录日志**，不伪造"可调度"，也不阻塞队列。
- 排序使用 `domain.NewTaskQueueOrderKey` + `domain.SortTaskQueueOrderKeys`
  （优先级 → 创建时间 → 任务 ID）。测试证据见 §7.3 第 1 项。

### 4.3 名额

`countOccupiedSlots` 的事实来源是数据库：

- 租约有效 → `TaskSlotClaimed` / `TaskSlotFinalizing`（占名额）；
- 执行记录租约有效 → `TaskSlotExecuting`（占名额）；
- `paused` / `failed` / `recovery_required` / `blocked` → `TaskSlotAwaitingHuman`（不占）；
- 中间状态但租约失效 → `TaskSlotIndeterminate`（不占，但也不允许自动领取）。

本进程已登记但尚未写回租约的分派也计入，防止并发扫描超额领取。

**并发上限的级别**：这是**基于数据库事实的进程级上限**。多个服务进程共享同一数据库时，
任务租约能防止同一任务被重复领取，但**不提供跨进程的全局容量仲裁**——两个进程可能
各自看到同一份占用快照后各领一个任务，使总量短暂超过上限。全局配额需要数据库仲裁，
属于后续里程碑（不在 P3-M2 范围）。

### 4.4 分派与失败处理

| 场景 | 租约处置 | 状态写入 |
|---|---|---|
| 领取冲突 | 无 | 无（正常竞争，不记录警告） |
| 领取后领域复检拒绝 | 立即释放 | 无 |
| 登记分派失败（已停止 / 已有分派） | 立即释放 | 无 |
| 启动失败（非停止类） | 释放 | 追加 `task.failed` 事件，**不推进任务状态** |
| 启动成功且工作流已启动 | 由工作流继续持有 | 由工作流负责 |
| 启动成功但未建立工作流 | 释放 | 无 |
| 服务停止导致取消 | **保留到自然过期** | 追加 `task.recovery_required` 事件 |

"服务停止不释放"是有意的：无法确认远程操作是否已经发生，盲目释放会让其他执行者
立刻在未知现场上重放。租约按有效期自然过期，由 P2 的恢复语义处理。

## 5. M2-4：生命周期集成

`internal/server/server.go` 的注册顺序（启动顺序）：

```text
执行节点预检 → StepExecutor → ValidationService → StepWorkflow
  → ContainerRecovery → Residue → CodeFetchRecovery
  → TaskWorkflow → TaskLaunch → TaskScheduler → CodeSaveRecovery
```

- 任务租约服务在数据库打开后、`TaskLaunchService` 之前构造：它是启动、工作流与
  调度器的共同依赖。
- 调度器注册在 `TaskLaunchService` 之后：它的 `Start` 在恢复与前置收敛全部完成后才被
  调用，因此第一次扫描不会看到尚未收敛的残留。
- 停止顺序自动反转：先停止调度器（不再分派新任务），再停止启动服务与工作流编排器，
  让下层服务收敛在途工作。
- `Stop` 取消扫描上下文、等待在途分派与扫描 goroutine 退出，并返回超时错误而不是
  静默遗留 goroutine。
- 因服务停止中断的分派**不被当作失败**（`isServiceStop` 识别
  `ErrTaskLaunchStopped` / `ErrWorkflowStopped` / `ErrStepExecutorStopped` /
  `context.Canceled` / `ErrSchedulerStopped`），因此不会触发自动重试。

## 6. M2-5：显式重试入口的决策

**审计结论**：`failed` 任务在生产代码中仍然没有任何显式重试入口。

- `domain.TaskStatus` 的状态机允许 `failed → preparing` / `planning`。
- HTTP / CLI 没有任何"重试失败任务"的接口；`POST .../start` 使用的是
  `domain.ExplicitLaunchStatuses()`，它**明确排除 `failed`**。
- 因此 `failed` 任务当前的合法操作是：删除，或人工修改数据库后重新入队。

**本次决定**：**不新增显式重试入口**，理由如下：

1. 安全重试需要独立的语义设计：必须核验容器现场、未决 Git 副作用、步骤残留与
   工作流租约，并在核验通过后才允许重置任务。这与 P2 的恢复门控是同一类问题，
   塞进调度器会退化为"隐式自动重试"。
2. 把 `failed` 直接改回 `queued` 让调度器重放，会在未经核验的现场上重新 Clone、
   调用 Agent 与执行 Git 操作，违反 P2 "先核验、后处理"的原则。
3. 已有能力覆盖了大部分真实需求：`paused` / `recovery_required` 可以通过
   `POST .../start` 与 `resume-workflow` 显式推进（它们的前置门控由
   `CheckTaskLaunchStart` / `CheckWorkflowStart` / `checkCodeSaveRecovery` 实施）。

**下一步**：显式重试入口作为独立工作项记录在
[`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md) 的"尚未实现"与
`ROADMAP.md` 中；它需要自己的里程碑与接口契约，而不是作为调度器的隐式行为。

## 7. 测试与验收

### 7.1 领域单元测试

`tests/unit/domain/task_lease_test.go`：

| 覆盖项 | 用例 |
|---|---|
| 租约身份必填项 | `TestTaskLeaseHandleValidate` |
| owner / 代次 / 有效期三者同时成立才算持有 | `TestTaskHoldsLease` |
| 失效原因可区分（接管 / 过期 / 释放） | `TestDecideTaskLeaseLoss` |
| 续期拒绝接管与过期 | `TestDecideTaskLeaseRenewal` |
| "未释放任何记录"不是成功 | `TestCheckTaskLeaseRelease` |
| 自动调度集合严格小于显式入口集合且都不含 `failed` | `TestTaskStatusExplicitSchedulingSets` |

### 7.2 存储层真实 SQLite 测试

`tests/integration/local/scheduler/lease_store_test.go`（共享同一数据库文件、
各自打开独立连接）：

| 覆盖项 | 用例 |
|---|---|
| 多连接并发领取只有一个成功（S6） | `TestAcquireTaskLeaseIsExclusiveAcrossConnections` |
| 空状态集合被拒绝（S7） | `TestAcquireTaskLeaseRequiresExplicitStatuses` |
| `failed` / `paused` / `recovery_required` 不能借默认集合领取 | `TestAcquireTaskLeaseRejectsNonQueuedStatuses` |
| 旧代次的续期 / 释放 / 写入全部被拒绝（S5） | `TestTaskLeaseEpochIsFenced` |
| 代次在重启后正确读取并继续递增（S3） | `TestTaskLeaseEpochSurvivesRestart` |
| 过期租约不能被迟到续期恢复 | `TestRenewTaskLeaseRejectsExpiredLease` |
| 释放检查受影响行数（S4） | `TestReleaseTaskLeaseChecksAffectedRows` |
| 领取事件可追溯（S3） | `TestTaskLeaseEventIsTraceable` |
| 已删除任务不能被领取 | `TestAcquireTaskLeaseRejectsDeletedTask` |

### 7.3 调度器集成测试

`tests/integration/local/scheduler/scheduler_test.go`（真实 SQLite + 正式迁移 +
真实 `app.TaskScheduler` + 真实 `app.TaskLeaseService`，只把 `Launch` 端口替换为替身）：

| 覆盖项 | 用例 |
|---|---|
| 按优先级 → 创建时间 → 任务 ID 顺序领取 | `TestSchedulerDispatchesInQueueOrder` |
| 并发上限生效且领取窗口占名额 | `TestSchedulerRespectsConcurrencyLimit` |
| 名额释放后继续处理队列 | `TestSchedulerResumesAfterSlotReleased` |
| 启动失败释放自己的租约并留下失败事件 | `TestSchedulerReleasesLeaseOnLaunchFailure` |
| 服务停止保留无法确认的租约且不再分派 | `TestSchedulerKeepsLeaseOnStop` |
| `failed` / `paused` 不被自动领取 | `TestSchedulerSkipsFailedAndPausedTasks` |
| 两个调度器实例不会重复领取同一任务 | `TestTwoSchedulersCannotClaimSameTask` |
| 领取事件可追溯 | `TestSchedulerLeaseEventIsTraceable` |
| 已有活跃任务占用名额 | `TestSchedulerCountsExistingActiveTasks` |
| 存在未结束执行记录时不领取 | `TestSchedulerSkipsTaskWithActiveWorkflowRun` |
| 并发上限必须为正 | `TestSchedulerRequiresPositiveLimit` |
| 状态快照与服务名 | `TestSchedulerStatusReportsLimit` |

### 7.4 实际执行的命令与结果

```text
$ go build ./...
（无输出，成功）

$ go vet ./...
（无输出，无新增问题）

$ gofmt -l internal cmd tests
（无输出，格式正确）

$ go test -count=1 ./tests/unit/domain/
ok  github.com/lingting/agentflow/tests/unit/domain

$ go test -count=1 ./tests/integration/local/scheduler/
ok  github.com/lingting/agentflow/tests/integration/local/scheduler

$ go test -count=1 ./tests/...
ok  github.com/lingting/agentflow/tests/...（全部 43 个测试包通过，退出码 0）
```

- **通过**：上述全部用例。
- **失败**：无（最终状态）。
- **跳过**：真实远程测试包在未启用对应配置时按既有约定输出 `WARN [SKIP]` 并跳过；
  本次运行中 `tests/integration/remote/**` 与 `tests/integration/pi` 均为 `ok`。
- **未执行**：前端测试（`lingting-agent-flow-ui/`），本里程碑不涉及前端改动。
- **无法确认**：无。

`TEST_ARTIFACT_DIR` 由 `testkit.Main` 在每次运行时创建于
`os.TempDir()/lingting-agent-flow-tests/` 下并输出绝对路径，测试产物按仓库约定保留。
本次调度器测试包的产物目录为
`C:/Users/lingting/AppData/Local/Temp/lingting-agent-flow-tests/scheduler-2440293909`
（含 `test.log` 与逐用例结论）。

### 7.5 本里程碑修改的文件

| 文件 | 变更 |
|---|---|
| `internal/domain/task_lease.go` | **新增**：租约身份、持有判定、失效原因、续期判定、释放结论 |
| `internal/domain/task.go` | 新增 `LeaseEpoch` 字段与校验 |
| `internal/domain/task_queue.go` | 新增显式入口状态集合（`ExplicitLaunchStatuses` 等） |
| `internal/domain/event.go` | 新增 `task.claimed` / `task.lease_released` 事件类型 |
| `internal/storage/interfaces.go` | `AcquireTaskLease` 返回句柄；新增 `RenewTaskLease`、`UpdateTaskLeaseGuard`、`AppendTaskLeaseEvent`；`ReleaseTaskLease` 改为按句柄并返回布尔 |
| `internal/storage/sqlite/tasks.go` | 领取原子递增代次并回读；续期 / 释放 / 栅栏写入；租约事件事务写入；读写 `lease_epoch` |
| `internal/storage/sqlite/migrations/0024_task_lease_epoch.sql` | **新增**：`tasks.lease_epoch` |
| `internal/app/task_lease.go` | **新增**：`TaskLeaseService`（领取 / 续期 / 释放 / 栅栏写入 / 事件） |
| `internal/app/task_workflow_lease.go` | **新增**：`WorkflowExecutionGrant`、交接校验、双租约续期、租约释放 |
| `internal/app/task_scheduler.go` | **新增**：有界并发调度器（扫描 / 排序 / 领取 / 分派 / 收尾） |
| `internal/app/task_workflow.go` | 存储接口增加租约方法；执行记录 owner 取自任务租约 |
| `internal/app/task_workflow_run.go` | `StartWorkflow` / `ResumeWorkflow` 接收 grant、校验执行权、使用统一 owner |
| `internal/app/task_workflow_exec.go` | 心跳同时续租两者；收尾改用租约栅栏写入；结束后释放租约 |
| `internal/app/task_workflow_plan.go` | `GeneratePlan` 接收 grant、校验执行权、`beginPlanning` 使用栅栏写入 |
| `internal/app/task_workflow_delivery_recovery.go` | 收尾补齐显式使用"无执行者"写入路径 |
| `internal/app/task_launch.go` / `task_launch_run.go` | 启动流程接收 grant、自领取回退、启动期心跳 |
| `internal/httpapi/router.go` / `task_launch.go` / `workflow.go` / `plan.go` | 四个显式入口先领取租约，成功后交接给工作流 |
| `internal/httpserver/server.go` / `internal/server/server.go` / `internal/cli/server.go` | 装配租约服务与调度器；注册到统一生命周期 |
| `tests/unit/domain/task_lease_test.go` | **新增** |
| `tests/integration/local/scheduler/*.go` | **新增**（`main_test.go`、`lease_store_test.go`、`scheduler_test.go`） |
| `tests/unit/store/migrate_test.go`、`tests/integration/local/database/workflow_test.go` | 迁移版本更新为 24 |
| `tests/unit/app/task_launch_test.go`、`tests/unit/httpapi/{task_launch,workflow,plan}_test.go`、`tests/integration/local/workflow/{delivery_chain,launch_chain}_test.go` | 适配新的租约依赖与栅栏写入接口 |

## 8. 完成定义核对

| 条件 | 状态 | 证据 |
|---|---|---|
| `AcquireTaskLease` 被生产调度调用且只接受 queued | ✅ | `TaskLeaseService.Claim` ← `TaskScheduler.dispatch`（`AutomaticSchedulingStatuses()`） |
| `ReleaseTaskLease` 与续租具备明确的成功 / 冲突语义 | ✅ | §2.3；`TestReleaseTaskLeaseChecksAffectedRows` |
| 执行权具备数据库条件保护的 fencing | ✅ | `lease_epoch` + `UpdateTaskLeaseGuard`；`TestTaskLeaseEpochIsFenced` |
| 任务与 WorkflowRun 执行权绑定 | ✅ | `beginRunRecord` 使用 `grant.workflowOwner()`；§3.1 |
| 自动调度按领域准入与排序规则选取候选 | ✅ | `loadCandidates`；`TestSchedulerDispatchesInQueueOrder` |
| 并发上限使用现有配置并覆盖领取窗口 | ✅ | `cfg.Scheduler.MaxConcurrentTasks`；`TestSchedulerRespectsConcurrencyLimit` |
| 生命周期 / 取消 / 失败 / 停止不留错误领取结论 | ✅ | §4.4、§5；`TestSchedulerKeepsLeaseOnStop` |
| 关键存储与调度并发语义有真实 SQLite 证据 | ✅ | §7.2、§7.3 |
| 所有现存显式执行入口经过审计 | ✅ | §2.4、§3.2 与 `P3-multi-task-scheduling.md` 的入口审计表 |
| 路线图状态一致且测试结果如实记录 | ✅ | §7.4 与本目录三份路线图 |

## 9. 未完成事项与剩余风险

### 9.1 明确不在本里程碑范围

- 跨任务容器 / 工作区 / Session / 缓存隔离规则（P3-M3）。
- 调度器重启后的完整接管策略（P3-M4）。
- 多任务并发与资源回收的系统性验收（P3-M5）。
- 分布式调度、跨机器配额、优先级抢占。
- `failed` 任务的用户显式重试入口（见 §6）。
- UI 进度展示。

### 9.2 剩余风险

| 编号 | 风险 | 影响 | 缓解 / 下一步 |
|---|---|---|---|
| R1 | 并发上限是**进程级**，不是全局配额 | 多进程共享同一数据库时总量可能短暂超限 | 任务租约仍防重复领取；全局配额需要数据库仲裁（后续里程碑） |
| R2 | `StepExecutor` 的任务终态写入仍是状态条件写（无租约栅栏） | 若步骤执行与任务领取同时发生，旧执行者理论上可写入终态 | 当前步骤执行入口不经过调度器（调度器只调用 `Launch` → 工作流），且步骤执行以事务占用为前提；P3-M3/M4 需统一该入口 |
| R3 | 服务停止保留的租约依赖 TTL 自然过期 | 停止后该任务在 TTL 内不可被重新领取 | 有意的取舍（无法确认远程现场）；TTL 默认 10 分钟 |
| R4 | 新代次写入不等于旧进程远程操作已物理取消 | 旧容器内可能有仍在运行的命令 | 心跳失败即停止推进且不再写状态；物理收敛属 P3-M4 |
| R5 | 调度扫描是轮询（默认 1s） | 空扫描开销与最坏 1s 的入队延迟 | 可观测、无"事件丢失即永不扫描"的故障面；后续可改为事件驱动 |
| R6 | `failed` 任务没有用户显式重试入口 | 失败任务只能删除或人工干预 | 已记录为独立工作项（§6） |
| R7 | 领取事件写入失败只记录日志 | 极端情况下存在"有效租约但无审计事件" | 状态是唯一真相来源；事件失败不回滚租约是有意取舍 |

## 10. 下一步

P3-M3 —— 明确执行环境、工作区、Session 和资源之间的隔离规则：

- 容器、工作区、Session 与共享缓存在多任务并发下的隔离边界；
- 名额上限与单节点容器数量的对应关系；
- 缓存卷与镜像争用的规则。

`TaskScheduler` 的进程级上限、`lease_epoch` 栅栏与双租约心跳是 P3-M3 的前提，不需要
在 P3-M3 重新设计。
