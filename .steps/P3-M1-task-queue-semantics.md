# P3-M1 —— 明确任务队列、领取和调度的业务语义

> 撰写依据：`master` HEAD `6e6d8b2` 之后工作区的真实代码（`internal/domain/task_queue.go`、
> `internal/storage/sqlite/tasks.go`、`internal/storage/sqlite/workflow_runs.go`、
> `internal/app/task_launch*.go`、`internal/app/task_workflow*.go`）与
> `tests/unit/domain/task_queue_test.go`。
> 上游依据：[`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md)、
> [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)、
> [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)、
> [`P2-M5-closeout-protocol.md`](./P2-M5-closeout-protocol.md)。

## 0. 里程碑标识

| 项 | 值 |
|---|---|
| ID | `P3-M1` |
| 名称 | 明确任务队列、领取和调度的业务语义 |
| 状态 | `completed`（2026-10-25） |
| 前置依赖 | P2 退出（P2-M1…M5 全部 `completed`） |
| 必要性 | 没有明确语义，调度会退化为"随意并发"，破坏状态正确性与 P2 的恢复门控 |
| 本里程碑不做的 | 队列扫描循环、有界并发执行器、容量控制器、调度器重启接管服务 |

**必要性展开**：当前代码没有任何队列或领取能力，但已经存在三个容易误判为"可复用"的
事实：`TaskStatus.CanStart()` 会返回 `true`、`Task.LeaseOwner` / `LeaseExpiresAt` 有列，
`storage.TaskStore.AcquireTaskLease` 已实现但**无任何生产调用方**。若不先把业务语义
写成唯一契约，P3-M2 极可能直接把 `CanStart` 当准入规则、把"字段存在"当"领取安全"，
从而在 `failed` / `recovery_required` / 结果不确定的任务上自动重放 Agent 与外部副作用。

## 1. 当前实现证据（真实代码核实）

### 1.1 已存在且可复用

| 能力 | 代码位置 | 核实结论 |
|---|---|---|
| 任务状态机 | `internal/domain/task.go` 的 `taskStatusMachine` | `completed` / `cancelled` 为终态；`failed` 允许回到 `preparing` / `planning` |
| `TaskStatus.CanStart` | `internal/domain/task.go` | 返回 `true` 的状态有 7 个：`queued`、`failed`、`paused`、`recovery_required`、`preparing`、`planned`、`retrying`。**它不是队列准入规则** |
| `TaskStatus.AllowsStepExecution` | `internal/domain/step_execution.go` | 返回 `true`：`preparing`、`planned`、`developing`、`retrying`、`paused`、`recovery_required` |
| 优先级 | `internal/domain/requirement.go` 的 `Priority.Rank()` | `high=0`、`normal=1`、`low=2`，是排序映射的唯一来源；`ParsePriorityRank` 反向还原 |
| 需求排序 | `internal/domain/requirement.go` 的 `Requirement.Less` | 优先级 → `CreatedAt` 升序 → `ID` 升序。**目前无生产调用方** |
| 需求表排序 | `internal/storage/sqlite/requirements.go` | `ORDER BY priority, created_at, rowid`，与 `Priority.Rank` 落库值一致 |
| 任务表排序 | `internal/storage/sqlite/tasks.go` | `ORDER BY created_at DESC, rowid DESC`（最新优先，**与队列语义相反**） |
| 任务条件更新 | `UpdateTaskIfStatus` | 以 `status IN (...)` 为条件；**不含租约条件** |
| 任务租约存储 | `internal/storage/sqlite/tasks.go` 的 `AcquireTaskLease` / `ReleaseTaskLease` | 已实现原子条件更新（见 1.2） |
| 工作流执行租约 | `internal/storage/sqlite/workflow_runs.go` 的 `RenewWorkflowRunLease` | 条件为 `status='running' AND worker_id=? AND lease_expires_at IS NOT NULL`；只写租约，不推进 `revision` |
| 执行记录版本栅栏 | `UpdateWorkflowRun` | `WHERE id = ? AND revision = ?`，影响行数为 0 一律 `ErrConflict` |
| 执行记录唯一性 | 迁移 `0018_workflow_runs.sql` 的 `idx_workflow_runs_active_task` | 部分唯一索引，`status NOT IN ('completed','cancelled','failed')` |
| 后台服务契约 | `internal/lifecycle` | `Name` / `Start` / `Stop`，注册顺序启动、逆序停止 |
| 统一启动入口 | `internal/app/task_launch.go`、`task_launch_run.go` | `Launch` 是唯一入口；`domain.DecideTaskLaunchNext` 决定下一阶段 |
| 启动前置校验 | `internal/domain/task_launch.go` 的 `CheckTaskLaunchStart` | 校验删除、取消、状态集合、未结束执行记录、`recovery_required` 步骤、执行节点、环境、规划/开发角色 |
| 工作流启动校验 | `internal/domain/workflow_run.go` 的 `CheckWorkflowStart` | 额外校验容器、步骤序号唯一、步骤类型、必需检查 |

### 1.2 `Task.LeaseOwner` / `LeaseExpiresAt` 的真实调用链（核实结论）

**字段与持久化**：

- 定义在 `internal/domain/task.go`；`Validate` 强制"有 owner 必须有 expires_at"。
- 迁移 `0009_relax_constraints.sql` 有同义 `CHECK`，`lease_expires_at` 必须是正整数毫秒。
- `internal/storage/sqlite/tasks.go` 的 `taskInsertArgs` / `UpdateTask` / `UpdateTaskIfStatus`
  都会写入这两列。

**读取**：`Task.LeaseActive(at)` 在 `internal/domain/task.go`，但**全仓库无生产调用方**，
只有 `tests/unit/app/task_launch_test.go` 与 `tests/unit/domain/task_launch_test.go`
构造记录时赋值。

**写入**：`AcquireTaskLease` 与 `ReleaseTaskLease` 定义在
`internal/storage/interfaces.go`（`TaskLeaseRequest` 含 `TaskID` / `Owner` / `TTL` /
`Statuses` / `At`）与 `internal/storage/sqlite/tasks.go`，但**全仓库无任何调用方**——
生产代码不领取、不释放、不续租任务租约。`AcquireTaskLease` 的默认状态集合是
`queued, preparing, planned, retrying, failed, paused, recovery_required`，注释明确写着
"与 `domain.TaskStatus.CanStart` 保持一致"。

**结论（必须记录的限制）**：

1. `Task` 租约目前是**已建好基础设施但完全未接线**的能力，不是"已有领取安全"的证据。
2. `AcquireTaskLease` 的 SQL 条件本身是正确的原子条件更新（`status IN (...) AND
   (lease_owner IS NULL OR lease_expires_at IS NULL OR lease_expires_at <= ? OR
   lease_owner = ?)`），可以在 P3-M2 直接复用；但它的**默认状态集合不能作为队列准入
   规则**——它包含 `failed` / `paused` / `recovery_required`，会自动重放未经核验的现场。
   P3-M2 必须显式传入 `Statuses`，不得依赖默认值。
3. `UpdateTaskIfStatus` **不足以支持原子领取**：它的条件只有 `status IN (...)`，不含
   `lease_owner` / `lease_expires_at`。若用它写租约，两个执行者读同一份快照后都能写入
   成功（后者覆盖前者），这正是"字段存在就代表领取安全"的错误假设。
4. `AcquireTaskLease` 无 fencing 版本号：它成功之后，旧持有者的任何后续
   `UpdateTaskIfStatus` 仍会成功。任务级 fencing 必须由调用方在每次推进前重新读取并
   用 `TaskHoldsExecution` 判定，P3-M2 需要评估是否补充 `lease_epoch` 或等效栅栏。
5. `ReleaseTaskLease` 的 SQL 无影响行数检查（`_, err := ExecContext`），无法区分"释放了
   自己的租约"与"什么都没释放"。P3-M2 必须补上 `RowsAffected` 判定，否则"释放失败"
   会被当作"已释放"。

### 1.3 工作流租约与任务租约的现状关系（核实结论）

- `WorkflowRun.WorkerID` 在生产中由 `TaskWorkflowService.owner` 赋值，取值是构造服务时
  生成的 `"workflow-" + domain.NewID()`（`internal/app/task_workflow.go`），**与任务租约
  的 owner 完全无关**。
- `StartWorkflow` / `ResumeWorkflow` 的前置校验链是 `CheckWorkflowStart` →
  `checkContainerReady` → `checkHostReady` → `checkCodeSaveRecovery`，**没有任何任务租约
  校验**。
- 因此当前存在两套互不相干的所有权：任务租约（未接线）与执行记录租约（已接线）。
  **本契约明确要求 P3-M2 把任务租约定义为唯一令牌，执行记录租约是它在执行阶段的
  续期视图**，否则"任务领取"无法约束"工作流推进"。

## 2. 队列候选资格（M1-1）

### 2.1 新建任务如何成为候选

`TaskService.CreateTaskFromAPI` → `CreateTask` 在一个事务内写入 `requirements` +
`tasks`（`status=queued`）+ `task_created` 事件，**不做任何执行动作**（代码注释明确：
"任务只落到 queued，是否执行由调用方另行决定"）。

因此"新建任务成为自动调度候选"= 落库即为候选，无需额外入队操作。候选集合由扫描
`status = queued` 得到，不需要新表、不需要队列记录。

### 2.2 自动调度与用户显式启动的职责边界

| 维度 | 自动调度（P3-M2 实现） | 用户显式 `TaskLaunchService.Launch` |
|---|---|---|
| 入口 | 调度器扫描 + 领取 | `POST /api/v1/tasks/{taskID}/start`（CLI `task start`） |
| 候选状态 | **仅 `queued`** | `queued`、`preparing`、`planned`、`retrying`、`paused`、`recovery_required` |
| 用户意图 | 无（系统自主） | 明确请求推进 |
| 失败后的行为 | 停止，不自动重试 | 停止，返回真实阶段与原因 |
| 是否可越过 P2 门控 | **否** | 否（`CheckTaskLaunchStart` 同样拦截） |

关键结论：**自动调度的准入集合严格小于显式启动的准入集合**。`preparing` / `planned` /
`retrying` 允许用户显式重新推进（进程中断后的残留现场），但它们绝不能被调度器自动
接管——用户没有表达"重试"意图时，自动推进等价于替用户重放。

### 2.3 各状态是否可被自动领取

| 状态 | 可自动领取 | 理由 |
|---|---|---|
| `queued` | ✅ | 从未开始执行的新任务，唯一候选 |
| `preparing` | ❌ | 环境准备阶段中断的残留；必须先核验容器现场（可能是"已创建但未写回"的孤儿容器） |
| `planning` | ❌ | Planner 调用可能正在进行或已产生未落库输出 |
| `planned` | ❌ | 计划已持久化但执行未开始；重新推进属于用户决定 |
| `developing` / `verifying` / `saving` | ❌ | 步骤占用或外部副作用可能正在进行 |
| `retrying` | ❌ | 重试意图由用户或修复循环决定，调度器不得代替 |
| `failed` | ❌ | 存在已确认失败；用户显式重试入口尚未定义（见 §8 未决问题） |
| `paused` | ❌ | 人工暂停，恢复必须显式 |
| `recovery_required` | ❌ | 结果不确定，必须先人工核验（P2 门控） |
| `cancelling` | ❌ | 取消意图已落盘，调度不得抢先推进 |
| `completed` / `cancelled` | ❌ | 终态 |

领域表达：`TaskStatus.AllowsAutomaticScheduling()` 只对 `queued` 返回 `true`。

### 2.4 用户显式重试、人工恢复与新任务调度的区分

三者用**不同入口 + 不同状态集合**区分，不需要新增状态：

1. **新任务自动调度**：`queued` → 调度器领取 → `Launch`。
2. **用户显式重试**（`failed` 任务）：目前**没有入口**。`failed` 允许迁移到 `preparing` /
   `planning`，但没有任何生产代码触发它，HTTP / CLI 也没有对应接口。P3-M2 需要决定
   是否新增（见 §8）。
3. **人工恢复**（`paused` / `recovery_required` / `blocked` 执行记录）：走
   `TaskLaunchService.Launch` 或 `ResumeWorkflow`；前置门控由
   `CheckTaskLaunchStart` / `CheckWorkflowStart` / `checkCodeSaveRecovery` 与
   `ResumeEligibilityService` 共同实施。

### 2.5 已删除、已取消、已持有租约、存在未结束执行记录

全部排除，拒绝原因使用稳定字符串（`TaskQueueRejection`）：

| 情况 | 拒绝原因 |
|---|---|
| `deleted_at` 非空 | `task_deleted` |
| `cancel_requested = true` | `cancel_requested` |
| 状态不可自动调度 | `status_not_queueable` |
| 存在未结束执行记录 | `active_workflow_run` |
| 存在 `running` / `developing` / `verifying` 步骤 | `step_occupied` |
| 存在 `recovery_required` 步骤 | `recovery_residue` |
| 代码保存仍有未核验副作用 | `external_side_effect_pending` |
| 需求优先级无法解析 | `priority_unknown` |
| `CheckTaskLaunchStart` 失败 | `prerequisite_missing` |

**租约归属不参与准入**（`lease_held` 只出现在领取仲裁）：准入回答"是否属于候选池"，
仲裁回答"这一次谁拿到执行权"。混在一起会让"同一执行者重复领取"无法表达。

### 2.6 前置检查失败时如何避免错误判定为已领取或永久占名额

规则：

1. 准入失败发生在**任何远程副作用之前**，不产生租约、不产生执行记录、不推进状态。
2. 领取成功后若启动前置检查失败（容器不可达、节点未通过预检等），必须**显式释放租约**，
   否则任务会被自己的租约挡在候选池外，直到租约自然过期（TTL 窗口内的永久占名额）。
3. 释放本身失败时不得假装成功：任务会停留在"已领取但无执行"的状态，
   `DecideTaskSlotState` 将其判为 `claimed`（占名额）。P3-M2 必须把"释放失败"记为
   独立错误并让名额窗口受 TTL 约束，不得靠重试掩盖。
4. 名额归属（§5）与准入判定是两套规则：占名额不代表通过准入，通过准入也不代表占名额。

## 3. 调度顺序（M1-2）

### 3.1 规则（三级全序）

```text
1. Priority.Rank() 升序        high(0) → normal(1) → low(2)
2. Task.CreatedAt 升序         同优先级先到先服务（FIFO）
3. Task.ID 字典序升序          时间戳相同时的确定性最终键
```

- **优先级映射唯一来源**：`Priority.Rank()`。领域层不另建映射，存储层 `requirements`
  表已用同一映射落库（`ORDER BY priority`）。
- **不新增优先级种类**：仍是 `low` / `normal` / `high`。
- **不实现抢占**：已持有执行权的任务不因新来高优先级任务而中断。
- **第三级是稳定性保证而非业务偏好**：时间戳只有毫秒精度，同批创建必然同时间；
  没有最终键时排序结果取决于输入顺序，同一队列两次扫描会得到不同次序。
- **任务列表排序不可复用**：`ORDER BY created_at DESC, rowid DESC` 是"最新优先"，
  与"先到先服务"相反。调度队列必须使用 `CompareTaskQueueOrder`。
- **需求表排序不完全等价**：`ORDER BY priority, created_at, rowid` 的第二级是需求
  创建时间，而队列按**任务**创建时间排序（同一需求可以有多条任务）。领域规则以任务
  时间为准。

### 3.2 被排除任务是否参与排序

不参与。排序输入是**已经通过准入的候选集合**：取消、已领取、不符合准入条件的任务在
准入阶段被排除，不占用排序位次，也不会因为"排到前面但拿不到"而阻塞后面的候选。

### 3.3 领域表达

- `NewTaskQueueOrderKey(input)`：从判定输入构造排序键，优先级无法解析时返回错误。
- `CompareTaskQueueOrder(a, b)`：三级比较，`< 0` 表示 `a` 先于 `b`。
- `SortTaskQueueOrderKeys(keys)`：使用稳定排序（比较函数已是全序，稳定排序是防御）。

## 4. 领取与租约契约（M1-3）

### 4.1 四层职责划分

| 层 | 回答的问题 | 领域/代码落点 |
|---|---|---|
| **业务准入** | 该任务是否可以进入调度 | `DecideTaskQueueAdmission` |
| **领取仲裁** | 谁真正获得任务的执行权 | `DecideTaskClaim`（领域）+ `AcquireTaskLease`（存储，原子） |
| **执行推进** | 如何执行 | 既有 `TaskLaunchService` / `TaskWorkflowService`，不修改 |
| **恢复接管** | 原持有者失效后何时允许新执行者 | `TaskHoldsExecution` + `DecideTaskExecutionHandover` |

### 4.2 领取前置条件与结果

前置条件（全部满足才允许尝试）：

1. 业务准入通过。
2. 执行者标识非空（空标识返回 `*ValidationError`）。
3. 租约满足以下之一：为空 / 已过期 / 已由本人持有。

结果分类：

| 情况 | `Allowed` | `Takeover` | `Rejection` |
|---|---|---|---|
| 准入不通过 | `false` | `false` | 第一个准入拒绝原因 |
| 租约空闲 | `true` | `false` | 空 |
| 租约已过期（有前持有者） | `true` | `true` | 空 |
| 租约由本人有效持有 | `true` | `false` | 空 |
| 租约由他人有效持有 | `false` | `false` | `lease_held` |

### 4.3 八条硬性语义

1. **同一任务只能被一个有效领取者持有**：由 `AcquireTaskLease` 的单个条件 UPDATE 保证。
   领域层的 `Allowed` 只是"可以尝试"，不构成所有权。
2. **并发领取只有一个成功**：`AcquireTaskLease` 的 `WHERE` 含租约有效性条件，两个并发
   UPDATE 中只有一个的 `RowsAffected` 为 1。**这条保证目前未被验证**（无调用方、无测试），
   列为 P3-M2 的存储层验收项。
3. **领取失败不推进状态、不启动工作流**：`DecideTaskClaim` 是纯函数，不写任何状态；
   P3-M2 的领取流程必须在 `AcquireTaskLease` 成功之后才调用 `Launch`。
4. **过期后旧持有者不得继续推进**：每次推进前必须重新读取任务记录并调用
   `TaskHoldsExecution(task, owner, now)`；返回 `false` 即必须停止。
5. **执行者能识别自己是否已失去执行权**：`TaskHoldsExecution` 同时校验 owner 与到期时间。
   与 `WorkflowRun.HoldsExecution` 语义一致（后者已用于工作流续租）。
6. **持久化失败、条件冲突、读取失败都不是成功领取**：`AcquireTaskLease` 返回
   `storage.ErrConflict`（`RowsAffected == 0`）或其它错误时，调用方必须按失败处理；
   `ErrConflict` 与"数据库不可用"必须能区分（前者不重试，后者可重试）。
7. **各终止场景的租约处理**：

   | 场景 | 租约处理 |
   |---|---|
   | 正常结束（`completed` / `cancelled` / `failed`） | 显式释放；`ReleaseTaskLease` 以 owner 为条件 |
   | 用户取消 | 释放由收敛逻辑负责；`cancel_requested` 一旦落盘，准入即永久拒绝 |
   | 失败 | 释放；任务进入 `failed`（`IsFinished` 为真，可删除） |
   | 服务关闭 | 释放；在途启动由 `beginLaunch` 取消并等待收敛 |
   | 进程崩溃 | 无法释放，租约自然过期；`DecideTaskSlotState` 判为 `indeterminate`，要求人工核验 |

8. **任务租约与 `WorkflowRun` 租约的边界**：任务租约是**唯一令牌**，`WorkflowRun` 租约
   是它在执行阶段的续期视图。`DecideTaskExecutionHandover` 明确规定：不持有任务租约
   的执行者不得建立或恢复工作流执行。

### 4.4 交接契约（任务领取 → 工作流执行）

`DecideTaskExecutionHandover(task, activeRun, owner, at)` 的拒绝顺序：

1. 任务已删除 → `task_deleted`。
2. 已请求取消 → `cancel_requested`。
3. 不持有有效任务租约 → `lease_held`。
4. 存在**其他执行者**持有有效执行权的执行记录 → `active_workflow_run`。
5. 状态不允许推进步骤（`AllowsStepExecution` 为 `false`）→ `status_not_queueable`。

第 4 条使用 `taskQueueRunHoldsExecution`：`running` 与 `cancel_requested` 且租约有效都算
持有执行权。这与 `internal/domain/task_launch.go` 的 `workflowRunOwnsExecution`
（只认 `running`）有意不同——取消请求已落盘但停止未确认时，抢占同一任务会与取消收敛
互相覆盖。

### 4.5 存储层必须满足的原子性要求（P3-M2 验收项）

本里程碑只定义契约，以下**未被验证**，必须在 P3-M2 实现并测试：

| 编号 | 要求 | 现状 |
|---|---|---|
| S1 | 领取必须在单个条件 UPDATE 内完成，条件包含状态集合 + 租约有效性 | `AcquireTaskLease` 已实现，无调用方、无测试 |
| S2 | 领取失败返回可区分错误（`ErrConflict` vs 数据库错误） | `AcquireTaskLease` 已区分，未验证 |
| S3 | 领取成功后必须能在同一事务或紧邻步骤内写入"已领取"痕迹 | **缺失**：无领取事件、无 `lease_epoch` |
| S4 | 释放必须检查 `RowsAffected`，区分"释放成功"与"没释放任何东西" | **缺失**：`ReleaseTaskLease` 无检查 |
| S5 | 任务租约需有 fencing 机制（`lease_epoch` 或等效），使旧持有者的迟到写入被拒绝 | **缺失**：`UpdateTaskIfStatus` 无条件租约检查 |
| S6 | 并发领取只有一个成功（真实 SQLite + 并发 goroutine） | **未验证** |
| S7 | `AcquireTaskLease` 必须显式传 `Statuses`，不得依赖默认值 | **待约束**：默认值含 `failed` / `paused` / `recovery_required` |

## 5. 调度名额的业务定义（M1-4）

### 5.1 名额状态

`TaskSlotState` 六态：

| 状态 | 占名额 | 可自动领取 | 含义 |
|---|---|---|---|
| `idle` | ❌ | ✅ | 新任务或已结束任务 |
| `claimed` | ✅ | ❌ | 已领取，尚未建立工作流执行记录（令牌 = 任务租约） |
| `executing` | ✅ | ❌ | 执行记录持有有效执行权 |
| `finalizing` | ✅ | ❌ | `verifying` / `saving` 且仍持有执行权 |
| `awaiting_human` | ❌ | ❌ | `paused` / `failed` / `recovery_required`，或 `blocked` / `recovery_required` 执行记录 |
| `indeterminate` | ❌ | ❌ | 租约已失效但仍停在中间状态：结论不明 |

### 5.2 关键规则

1. **哪些阶段计入名额**：只有"仍持有执行权"的阶段计入。`claimed` 是"领取成功但
   `WorkflowRun` 尚未建立"的名额表达——这正是任务租约存在的意义，否则这个窗口
   完全不占名额，调度器会超额领取。
2. **领取后、执行记录建立前的名额归属**：由任务租约表达（`claimed`）。
3. **进入执行阶段后的协调**：任务租约继续持有（用于 fencing），执行记录租约接管续期；
   两者必须由同一 owner 持有（`DecideTaskExecutionHandover` 第 3、4 条）。
4. **完成 / 失败 / 取消 / `recovery_required` 后的名额**：
   - `completed` / `cancelled` → `idle`（不占名额）；
   - `failed` / `paused` / `recovery_required` → `awaiting_human`（不占名额，但也不得自动领取）。
5. **为什么结果不确定不能当作普通失败立即重试**：`indeterminate` 与 `awaiting_human`
   分离。前者表示"租约失效 + 状态未收敛"，可能对应仍在运行的远程副作用；把它归入
   `failed` 会立刻触发重放，违反 P2 的"先核验、后处理"。因此
   `TaskSlotState.AllowsAutomaticClaim()` 只对 `idle` 返回 `true`。
6. **不占名额不等于可以自动领取**：`awaiting_human` 与 `indeterminate` 都不占名额，
   但都必须人工介入。

### 5.3 留给 P3-M3 的容量规则

以下**不在本里程碑决定**：

- 多执行节点之间的容量分配与配额。
- 单个节点上的并发上限与容器数量上限的对应关系。
- 共享镜像 / 缓存卷的争用规则。
- 跨任务的资源隔离（CPU / 内存 / 磁盘）。

本里程碑只定义"名额归属"，不定义"名额上限是多少"。

## 6. 异常、取消、过期租约与恢复边界

### 6.1 取消

- 取消请求通过 `Task.RequestCancel` + `Transition(cancelling)` 落盘（`queued` 任务走
  `CancelImmediately`）。
- 落盘后 `cancel_requested = true`，**准入永久拒绝**（`cancel_requested`），
  `DecideTaskExecutionHandover` 同样拒绝。
- 取消不是"立即停止"：最终状态由真实收敛结果决定（P2-M2 已定义）。

### 6.2 过期租约

- `Task.LeaseActive(at)` 用严格 `After`：到期时刻本身即视为失效。
- 过期后：`DecideTaskClaim` 允许接管（`Takeover = true`）；`TaskHoldsExecution` 返回
  `false`；`DecideTaskQueueAdmission` 不受影响（准入不看租约）。
- 旧持有者必须自行停止：每次推进前重新读取并判定，不允许用本地对象自检。

### 6.3 服务关闭

- `TaskLaunchService.Stop` 取消在途启动并等待收敛（`beginLaunch` 的 `stopSignal`）。
- `TaskWorkflowService.Stop` 取消在途工作流并把记录推进到失败终态。
- 两者都会释放自己持有的租约（P3-M2 需确保领取层同样在 `Stop` 中释放）。

### 6.4 进程崩溃

- 租约无法释放，自然过期。
- `DecideTaskSlotState` 在租约失效后：
  - 若执行记录仍持有有效执行权 → `executing`（不抢占）；
  - 若执行记录为 `blocked` / `recovery_required` → `awaiting_human`；
  - 若任务为 `paused` / `failed` / `recovery_required` → `awaiting_human`；
  - 其余（停在 `preparing` / `planned` / `developing` 等中间状态）→ `indeterminate`。
- **`indeterminate` 不得自动重试**，必须人工核验。

### 6.5 与 P2 门控的衔接

队列准入复用（不重新实现）以下既有规则：

| 门控 | 复用点 |
|---|---|
| 启动前置条件 | `CheckTaskLaunchStart` |
| 外部副作用未决 | `DecideTaskCodeSaveResume`（`Action != Proceed` 即拒绝） |
| 步骤结果不确定 | `StepStatusRecoveryRequired` |
| 执行记录持有权 | `WorkflowRun.LeaseActive` |
| 任务执行权 | `Task.LeaseActive` |

## 7. 领域规则与调用方职责

### 7.1 领域层（`internal/domain/task_queue.go`）

| 规则 | 函数 | 类型 |
|---|---|---|
| 自动调度候选集合 | `TaskStatus.AllowsAutomaticScheduling()` | 纯函数 |
| 业务准入 | `DecideTaskQueueAdmission(input) TaskQueueAdmission` | 纯函数 |
| 排序键构造 | `NewTaskQueueOrderKey(input) (TaskQueueOrderKey, error)` | 纯函数 |
| 排序比较 | `CompareTaskQueueOrder(a, b) int` | 纯函数 |
| 排序执行 | `SortTaskQueueOrderKeys(keys)` | 纯函数 |
| 领取仲裁 | `DecideTaskClaim(input, owner) (TaskClaimDecision, error)` | 纯函数 |
| 执行权判定 | `TaskHoldsExecution(task, owner, at) bool` | 纯函数 |
| 领取 → 执行交接 | `DecideTaskExecutionHandover(task, run, owner, at)` | 纯函数 |
| 名额归属 | `DecideTaskSlotState(input) TaskSlotState` | 纯函数 |
| 名额语义 | `TaskSlotState.OccupiesSlot()` / `AllowsAutomaticClaim()` | 纯函数 |
| 拒绝原因 | `TaskQueueRejection`（10 个稳定取值） | 纯类型 |

领域层**不含**：数据库查询、goroutine、HTTP 参数、后台服务生命周期、SQL、重试循环。

### 7.2 与既有领域规则的关系（不重复实现）

| 既有规则 | 关系 |
|---|---|
| `TaskStatus.CanStart()` | 不改、不复用为队列准入。`AllowsAutomaticScheduling` 是它的真子集（有测试断言） |
| `CheckTaskLaunchStart` | 准入直接调用它，不重新实现前置条件判定 |
| `TaskStatus.AllowsStepExecution()` | 交接判定直接调用它 |
| `WorkflowRun.LeaseActive()` / `HoldsExecution()` | 交接与名额判定直接调用 |
| `DecideTaskCodeSaveResume` | 准入直接调用，不重新实现副作用核验 |
| `workflowRunOwnsExecution`（`task_launch.go`） | 语义不同的独立函数（只认 `running`），不合并 |

### 7.3 调用方职责（P3-M2 实现）

- **调度器**：扫描 `queued` → 加载需求 / 步骤 / 执行记录 / 代码保存记录 →
  `DecideTaskQueueAdmission` → 按 `CompareTaskQueueOrder` 排序 → `DecideTaskClaim` →
  `AcquireTaskLease` → 校验名额 → `TaskLaunchService.Launch`。
- **领取层**：`AcquireTaskLease` 必须显式传 `Statuses: []{TaskStatusQueued}`。
- **执行层**：每次推进前重新读取任务并 `TaskHoldsExecution`；失败即停止。
- **释放层**：终止场景显式 `ReleaseTaskLease` 并检查 `RowsAffected`。
- **绝不**在 Handler、CLI 或前端复制上述任一判定。

## 8. 单元测试矩阵与完成定义

### 8.1 测试矩阵（`tests/unit/domain/task_queue_test.go`）

| 覆盖项 | 用例 |
|---|---|
| `queued` 与其他状态的可调度资格区别 | `TestTaskStatusAllowsAutomaticScheduling`（14 个状态） |
| 准入集合严于 `CanStart` | `TestTaskStatusAllowsAutomaticSchedulingIsStricterThanCanStart` |
| 新任务通过准入 | `TestDecideTaskQueueAdmissionAcceptsQueuedTask` |
| 非 `queued` 状态排除 | `TestDecideTaskQueueAdmissionRejectsNonQueuedStatus`（9 个状态） |
| 取消与逻辑删除排除 | `TestDecideTaskQueueAdmissionRejectsDeletedAndCancelled` |
| 租约不参与准入 | `TestDecideTaskQueueAdmissionIgnoresLeaseOwnership` |
| 执行现场残留排除 | `TestDecideTaskQueueAdmissionRejectsActiveRunAndResidue`（执行记录 / 不确定步骤 / 占用步骤） |
| 未核验副作用排除 | `TestDecideTaskQueueAdmissionRejectsPendingSideEffect` |
| 前置条件缺失排除 | `TestDecideTaskQueueAdmissionRejectsMissingPrerequisite`（2 个） |
| 优先级无法解析排除 | `TestDecideTaskQueueAdmissionRejectsUnknownPriority`（2 个） |
| 一次收集全部拒绝原因 | `TestDecideTaskQueueAdmissionCollectsAllReasons` |
| 优先级顺序 | `TestCompareTaskQueueOrderByPriority` |
| 同优先级 FIFO | `TestCompareTaskQueueOrderIsFIFOWithinPriority` |
| 时间相同的稳定次序 | `TestCompareTaskQueueOrderIsStableOnTiedTime` |
| 排序与输入顺序无关 | `TestSortTaskQueueOrderKeysIsDeterministic`（3 种输入顺序） |
| 排序键不能凭空构造 | `TestNewTaskQueueOrderKeyRejectsUnknownPriority` |
| 空执行者标识拒绝 | `TestDecideTaskClaimRejectsInvalidOwner` |
| 空闲 / 过期租约可领取 | `TestDecideTaskClaimAllowsFreeAndExpiredLease` |
| 他人有效租约拒绝 | `TestDecideTaskClaimRejectsLiveLeaseOfOtherOwner` |
| 同一执行者重复领取幂等 | `TestDecideTaskClaimIsIdempotentForSameOwner` |
| 准入不通过不得领取 | `TestDecideTaskClaimRejectsInadmissibleTask` |
| 两个竞争者不能都持有 | `TestDecideTaskClaimCompetingOwnersCannotBothHold` |
| 空 / 有效 / 过期 / 不匹配租约 | `TestTaskHoldsExecution`（5 个） |
| 交接判定 | `TestDecideTaskExecutionHandover`（7 个） |
| 状态与执行权不一致 | `TestDecideTaskExecutionHandoverRejectsStatusWithoutStepExecution` |
| 名额归属 | `TestTaskSlotStateOccupancy`（7 个） |
| 名额语义 | `TestTaskSlotStateOnlyIdleAllowsAutomaticClaim` |
| 结论不明不退化为失败 | `TestDecideTaskSlotStateDoesNotTreatIndeterminateAsFailure` |
| 名额由执行记录租约决定 | `TestDecideTaskSlotStateUsesWorkflowRunLease` |
| 阻塞执行记录等待人工 | `TestDecideTaskSlotStateBlockedRunAwaitsHuman` |

### 8.2 未由本里程碑验证、必须列为 P3-M2 验收项

| 场景 | 为什么纯领域测试不能证明 |
|---|---|
| 两个并发 `AcquireTaskLease` 只有一个成功 | 需要真实 SQLite + 并发 goroutine |
| `ErrConflict` 与数据库不可用可区分 | 需要真实存储错误 |
| 领取失败不产生任何持久化副作用 | 需要事务边界验证 |
| 释放失败（`RowsAffected == 0`）能被识别 | 需要存储层实现补齐后验证 |
| 旧持有者的迟到写入被 fencing 拒绝 | 需要 `lease_epoch` 或等效机制先落地 |
| 名额不超发（N 个名额只领取 N 个） | 需要真实调度循环 |

**不得**用纯领域单元测试声称以上并发保证已经落地。

### 8.3 完成定义

全部满足才可标记 `completed`：

1. ✅ 队列候选规则、排序规则、领取契约和租约边界都有明确且互不矛盾的定义（§2–§5）。
2. ✅ `Task` 租约的现有用途与缺口已被真实代码核实（§1.2）。
3. ✅ 领域层具有最小且统一的调度准入与排序语义，不重复实现状态机（§7）。
4. ✅ 核心领域规则有测试证据；未验证的数据库并发保证被明确列为 P3-M2 验收项（§8.2）。
5. ✅ 任务取消、恢复和结果不确定语义没有被调度规则绕过（§6）。
6. ✅ 三份路线图文档一致，且为 P3-M2 留下清楚的实现边界（§9）。
7. ✅ 实际测试结果如实记录（§10）。

## 9. 已确认风险、未决问题与移交 P3-M2 的接口契约

### 9.1 已确认风险

| 编号 | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R1 | `AcquireTaskLease` 默认状态集合含 `failed` / `paused` / `recovery_required` | 依赖默认值会重放未经核验的现场 | P3-M2 必须显式传 `Statuses: []{queued}` |
| R2 | `UpdateTaskIfStatus` 无租约条件 | 旧持有者的迟到写入会成功 | P3-M2 需 `lease_epoch` 或等效栅栏（S5） |
| R3 | `ReleaseTaskLease` 无 `RowsAffected` 检查 | 释放失败被当作已释放，名额被占满 | P3-M2 必须补上判定（S4） |
| R4 | 任务租约与工作流租约 owner 无关 | 两套所有权互不约束 | 本契约要求统一（§4.3 第 8 条） |
| R5 | 领取后启动前置检查失败时不释放租约 | 任务被自己的租约挡在候选池外 | P3-M2 必须显式释放并记录失败 |
| R6 | 无领取事件 | 无法事后追溯"谁在何时领取" | P3-M2 建议追加事件（S3） |
| R7 | `failed` 无用户显式重试入口 | 失败任务只能删除或人工干预 | P3-M2 决定是否新增 |

### 9.2 未决问题

1. **`failed` 的用户显式重试是否需要一个新接口**（例如 `POST .../retry`）？
   当前状态机允许 `failed → preparing` / `planning`，但无任何入口。P3-M2 必须先决定
   入口形态，否则 `failed` 任务在 UI 上是"不可操作但不可删除之外的死路"。
2. **任务租约是否需要 `lease_epoch` 列**？若不新增列，fencing 只能靠"每次推进前重读 +
   `TaskHoldsExecution`"，存在读取与写入之间的窗口。需要评估窗口是否可接受。
3. **名额上限如何配置**（配置项 vs 数据库 vs 启动参数）？本里程碑不决定。
4. **调度扫描的触发方式**（定时轮询 vs 事件驱动 vs 领取失败后重试）？本里程碑不决定。
5. **是否需要"已领取但未执行"的显式超时回收**？当前只能靠租约 TTL 自然过期。

### 9.3 移交 P3-M2 的接口契约

P3-M2 **必须**基于以下接口实现，不得另建一套：

```go
// 1. 候选筛选：对每条 queued 任务构造输入并判定
domain.DecideTaskQueueAdmission(domain.TaskQueueInput{...}) domain.TaskQueueAdmission

// 2. 排序：按准入通过的候选构造键并排序
key, err := domain.NewTaskQueueOrderKey(input)
domain.SortTaskQueueOrderKeys(keys)

// 3. 领取仲裁（领域判定）
decision, err := domain.DecideTaskClaim(input, owner)

// 4. 领取仲裁（存储原子写入）——必须显式传 Statuses
store.AcquireTaskLease(ctx, storage.TaskLeaseRequest{
    TaskID:   task.ID,
    Owner:    owner,
    TTL:      leaseTTL,
    Statuses: []domain.TaskStatus{domain.TaskStatusQueued},
    At:       now,
})

// 5. 名额判定
domain.DecideTaskSlotState(domain.TaskSlotInput{...}) domain.TaskSlotState

// 6. 执行前 fencing
if !domain.TaskHoldsExecution(task, owner, now) { /* 立即停止推进 */ }

// 7. 交接判定
domain.DecideTaskExecutionHandover(task, activeRun, owner, now)

// 8. 释放
store.ReleaseTaskLease(ctx, taskID, owner, now) // P3-M2 必须补 RowsAffected 检查
```

P3-M2 的存储层验收要求（S1–S7，见 §4.5）必须逐条实现并测试，不得用纯领域测试替代。

## 10. 实际验证结果

### 10.1 执行的命令与真实结果

```text
$ go build ./...
（无输出，成功）

$ go vet ./...
（无输出，无新增问题）

$ gofmt -l internal cmd tests
（无输出，格式正确）

$ go test -count=1 ./tests/unit/...
Go test: 1542 passed in 21 packages

$ go test -count=1 ./tests/unit/domain/
Go test: 535 passed in 1 packages

$ go test -count=1 -run 'TestTaskQueue|TestDecideTaskQueue|TestDecideTaskClaim|TestTaskHoldsExecution|TestDecideTaskExecutionHandover|TestTaskSlotState|TestDecideTaskSlotState|TestCompareTaskQueueOrder|TestSortTaskQueueOrderKeys|TestNewTaskQueueOrderKey|TestTaskStatusAllowsAutomaticScheduling' ./tests/unit/domain/
Go test: 91 passed in 1 packages
```

- **通过**：上述全部用例（含新增 30 个队列用例与既有 505 个 domain 用例）。
- **失败**：无。
- **跳过**：无。
- **未执行**：本里程碑不涉及 `tests/integration/**`、`tests/e2e/**` 与前端测试；
  未运行任何集成测试、真实远程测试或前端测试。
- **无法确认**：无。

`TEST_ARTIFACT_DIR=C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\domain-2213097560`
（由 `testkit.Main` 创建，含 `test.log` 与 `latest-run.txt`；测试产物按仓库约定保留，
未写入仓库目录）。

### 10.2 本里程碑修改的文件

| 文件 | 变更 |
|---|---|
| `internal/domain/task_queue.go` | **新增**：队列准入、排序、领取仲裁、执行权 fencing、交接、名额归属的纯领域规则 |
| `tests/unit/domain/task_queue_test.go` | **新增**：30 个单元测试覆盖 §8.1 全部项 |
| `.steps/P3-M1-task-queue-semantics.md` | **新增**：本文件 |
| `.steps/README.md` | 更新当前阶段与里程碑 |
| `.steps/ROADMAP.md` | 更新当前实现状态摘要 |
| `.steps/P3-multi-task-scheduling.md` | 更新里程碑状态与 M1 详情 |

**未修改**：`TaskLaunchService`、`TaskWorkflowService`、`StepWorkflowService`、
`ValidationService`、任何存储层实现、任何 HTTP / CLI 入口、任何迁移脚本。
本里程碑不修改生产执行流程。

### 10.3 下一步（P3-M2）

P3-M2 必须实现：

1. **存储层原子领取**：`AcquireTaskLease` 显式传 `Statuses`；补 `RowsAffected` 检查到
   `ReleaseTaskLease`；评估并落地 `lease_epoch` 栅栏（S3–S5）。
2. **有界并发**：可配置的名额上限 + 基于 `DecideTaskSlotState` 的名额占用统计。
3. **任务生命周期协调**：领取 → `Launch` → 交接 → 释放的完整链路，包含失败释放与
   服务关闭释放。
4. **调度器后台服务**：注册进 `internal/lifecycle`，`Stop` 时取消在途领取并释放租约。
5. **并发测试**：真实 SQLite + 并发 goroutine 验证 S6（只有一个领取成功）。

**不得**在 P3-M2 中：修改 P2 的恢复门控、绕过 `CheckTaskLaunchStart`、把
`failed` / `recovery_required` 纳入自动调度、用进程内互斥锁冒充跨进程安全。
