# P3-M4 —— 打通任务取消、失败、恢复与调度器重启

> 撰写依据：`master` HEAD `b61b7e4`（P3-M3 之后）的工作区真实代码、迁移与测试状态，
> 逐项核对 `internal/app/`、`internal/domain/`、`internal/httpapi/`、`internal/cli/`、
> `internal/server/` 与 `tests/` 得出。实施完成日期 **2026-10-25**。
>
> 上游依据：[`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md)、
> [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md)、
> [`P3-M2-bounded-concurrency.md`](./P3-M2-bounded-concurrency.md)、
> [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md)、
> [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)、
> [`P2-M5-closeout-protocol.md`](./P2-M5-closeout-protocol.md)。

## 0. 里程碑标识

| 项 | 值 |
|---|---|
| ID | `P3-M4` |
| 名称 | 处理任务取消、失败、恢复和调度器重启 |
| 状态 | `completed`（2026-10-25） |
| 前置依赖 | P3-M2（`completed`）、P3-M3（`completed`） |
| 必要性 | 调度器重启或多任务取消时，若不能正确收敛，会卡死或重复执行外部副作用 |
| 本里程碑不做的 | 跨进程全局并发配额、分布式调度、优先级抢占、孤儿容器自动重新关联、结果不确定的外部操作自动重放 |

## 1. 现状审计（实施前）

审计对象是真实代码与调用关系，不是路线图描述。

| 场景 | 当前持久化状态 | 已有处理入口 | 实施前的实际行为 | 缺失条件 | 拟修复位置 | 对应验收测试 |
|---|---|---|---|---|---|---|
| 调度器正常关闭 | `tasks.lease_owner` / `lease_expires_at` 保留 | `TaskScheduler.Stop` | 取消扫描与在途分派，**不释放租约**（有意：无法确认远程操作已停止） | 无（行为正确） | — | `TestSchedulerRestartAfterGracefulStop` |
| 调度器重启 | 同上 | `Server.startServices` 注册顺序 | 调度器注册在恢复服务之后，第一次扫描前已收敛 | 缺"过期租约残留"的可追溯现场 | `internal/app/task_lease_recovery.go` | `TestLeaseRecoveryRecordsResidueWithoutReleasingLease` |
| 进程崩溃 + 租约过期 | `lease_owner` 残留、`lease_expires_at <= now` | 无专门入口 | 租约按 TTL 自然过期；`recoverUnfinished` 只收敛**执行记录**，不看任务租约 | 过期租约的失效事实没有落成现场记录，用户与调度器都看不到 | 新增 `TaskLeaseRecoveryService` | `TestLeaseRecoveryRecordsResidueWithoutReleasingLease`、`TestLeaseRecoverySkipsLiveLease`、`TestLeaseRecoverySkipsActiveRun` |
| 租约仍有效时第二个执行者 | `lease_owner` 有效 | `AcquireTaskLease` 条件 UPDATE | 领取必然冲突（`storage.ErrConflict`） | 无（行为正确） | — | `TestSchedulerRestartAfterGracefulStop`、既有 `TestAcquireTaskLeaseIsExclusiveAcrossConnections` |
| 旧代次尝试续租 / 写终态 / 推进步骤 | `lease_epoch` 已递增 | `RenewTaskLease` / `UpdateTaskLeaseGuard` / `StepExecutionStartRequest.Lease` | 三者都以 `owner + epoch` 为条件，0 行 → 冲突 | 无（P3-M2/M3 已落地） | — | 既有 `TestTaskLeaseEpochIsFenced`、`TestStaleLeaseCannotOccupyStep`、`TestStaleLeaseCannotWriteTaskStatus` |
| 恢复未成功持久化 | 记录保持原状 | `TaskWorkflowService.Start`、`ExecutionResidueService.Start` | 任一记录收敛失败即让 `Start` 返回错误，`lifecycle.Manager` 逆序清理 | 无（行为正确） | — | 既有 `TestRecoverUnfinishedRunFailureBlocksStartup`、`TestRecoverCancelIntentFailureBlocksStartup` |
| 任务处于 `cancelling` | `cancel_requested = 1` + `status = cancelling` | `recoverCancelling` + `domain.DecideCancellationRecovery` | 有未结束执行 → `recovery_required`；无取消记录 → `recovery_required`；其余 → `cancelled` | 无（行为正确） | — | 既有 `TestRecoverCancellingOnRestart` |
| 任务处于 `recovery_required` / `blocked` | 任务层 + 执行记录层 | 无自动入口 | 自动调度不领取（准入只接受 `queued`）；启动恢复保持原状 | 无（有意：不得因租约过期自动重放） | — | `TestSchedulerDoesNotReclaimExpiredLeaseWithUncertainState` |
| 任务处于 `failed` | `final_result = failed`、`status = failed` | **无生产入口** | `POST .../start` 的集合 `ExplicitLaunchStatuses` 明确排除 `failed`；调度器只领取 `queued`；`AllowsStepExecution` 不含 `failed` | 缺一个经过领域规则约束的显式重试入口 | 新增 `domain.DecideTaskRetryEligibility` + `app.TaskRetryService` + `POST .../retry` + `agentflow task retry` | `TestRetryFailedTaskStartsNewCycle`、`TestE2ERetryAfterFailureStartsNewCycle` |
| 取消意图已落盘但执行者仍在跑 | 执行记录 `cancel_requested = 1` + `status = cancel_requested`，`worker_id` 仍有效 | `CancelWorkflow` + `cancelInFlight` | 意图先落执行记录、再落任务层；信号传播到在途上下文；`recoverRun` 的 `cancel_requested` 分支只在租约过期后才收敛 | 无（行为正确） | — | 既有 `TestRecoverCancelIntentConvergesTask`、`TestE2ECancelPersistsIntentBeforeStop` |
| 服务关闭 vs 用户取消 | `attempts.status = interrupted` vs `cancelled` | `isServiceStop` / `isServiceStopCause` / `context.Cause` | 两者可区分：服务停止收敛为 `recovery_required` / `interrupted` | 无（行为正确） | — | 既有 `TestPlanGenerationInterruptedByShutdown`、`TestE2ERestartConvergesRunningRunToBlocked` |
| 任务租约 / WorkflowRun 租约 / 执行记录所有权矛盾 | `tasks.lease_epoch` + `workflow_runs.worker_id` / `lease_expires_at` | `WorkflowExecutionGrant` + 双租约心跳 | `WorkerID` 取自任务租约 owner，心跳同时续租两者，任一失效即停止推进 | 无（P3-M2 已统一） | — | 既有 `TestSingleStepAndWorkflowCompeteForSameLease` |

### 1.1 对六个审计问题的回答

1. **调度器停止时在途 `Launch`、任务租约、工作流执行记录如何收敛？**
   `Stop` 取消扫描上下文与每个在途分派的 `cancel`，等待 `runs`/`scanning` 退出，
   超时返回明确错误而不是静默遗留 goroutine。分派的 `Launch` 收到取消后按
   `settleFailedDispatch` 处理：`isServiceStop` 命中时**保留租约**（按 TTL 自然过期）
   并追加 `task.recovery_required` 事件；其余失败释放租约并追加 `task.failed` 事件。
   工作流执行记录由 `TaskWorkflowService.Stop` 收敛（`stopForInterruption` →
   `recovery_required` / `worker_interrupted`）。

2. **进程崩溃后租约过期、远程命令仍可能运行时，如何防止新执行者与旧执行者同时改工作区？**
   三道约束叠加：任务租约的 `owner + epoch` 条件写（旧代次续租/写终态/占步骤全部被拒）、
   `workflow_runs` 的 `revision` 条件写、步骤占用的事务条件更新。但**数据库 fencing
   不等于远程进程已停止**——因此 `TaskLeaseRecoveryService` 只记录现场、不释放租约、
   不改写状态，调度器也不会把过期租约解释为"可以安全重跑"。

3. **任务处于 `cancelling` / `recovery_required` / `blocked` / `failed` 时，启动恢复与自动调度分别做什么？**
   - `cancelling`：启动恢复按 `DecideCancellationRecovery` 收敛为 `cancelled` 或 `recovery_required`；自动调度不领取（状态不在 `queued`）。
   - `recovery_required`：启动恢复保持原状（等待用户显式恢复）；自动调度不领取。
   - `blocked`：这是**执行记录**状态，任务层通常是 `developing`；启动恢复保持原状；自动调度不领取。
   - `failed`：启动恢复保持原状；自动调度不领取；只有新的显式重试入口（本里程碑）能重开它。

4. **五个服务的实际启动顺序是否保证先恢复、后调度？**
   `server.startServices` 的注册顺序（= 启动顺序）为：执行节点预检 → StepExecutor →
   ValidationService → StepWorkflow → ContainerRecovery → Residue →
   CodeFetchRecovery → TaskWorkflow → TaskLaunch → **TaskLeaseRecovery** → TaskScheduler →
   CodeSaveRecovery。`TaskLeaseRecovery` 与 `TaskScheduler` 的先后是本里程碑新增的显式
   约束，并有注释说明理由；`lifecycle.Manager` 保证停止顺序反转。

5. **取消意图已持久化但旧执行者仍在执行时，如何等待或核验远程操作的真实结果？**
   取消只落意图（`cancel_requested` + `cancelling`），不宣称已停止；信号经
   `cancelInFlight` 传播到在途上下文；最终状态由真实收敛结果决定。若进程在传播中退出，
   重启后 `recoverCancelling` 依据"是否还有未结束执行记录"区分：有 → `recovery_required`
   （现场未知，等待人工核验）；无 → `cancelled`。

6. **用户能否从 `failed` 任务显式发起新一轮执行？状态机是否支持安全转换？**
   实施前**不能**。状态机本身支持 `failed → preparing` / `planning`，但没有任何生产
   入口使用它。本里程碑新增领域规则与入口（见 §3）。

7. **任务租约、`WorkflowRun` 租约和执行记录是否可能出现相互矛盾的所有权事实？**
   不会出现"两套所有权"：`WorkflowRun.WorkerID` 直接取自任务租约 owner，
   `TaskWorkflowService.startLeaseHeartbeat` 先续任务租约再续工作流租约，任一失败即
   `cancel(errWorkflowLeaseLost)`。仍存在的是"旧执行者的远程进程可能还在运行"这一
   **物理**事实与数据库事实的差异，这是本里程碑明确保留的边界（见 §6）。

### 1.2 与路线图的不一致（已纠正）

- `P3-multi-task-scheduling.md` 与 `ROADMAP.md` 曾把"调度器重启接管"整体列为 P3-M4 的
  目标，措辞容易被读成"过期租约即可安全接管"。实际代码与 P2-M2 语义要求的是
  **先核验、后处理**：本里程碑因此把交付物定义为"恢复资格 + 现场记录 + 显式重试入口"，
  而不是"自动重放"。
- 文档中的 P3-M3 完成日期写作 `2026-10-26`，而本次实施发生在 `2026-10-25`。
  该日期与工作区实际时间不一致，本里程碑如实记录而不追溯修改历史里程碑的表述；
  后续如需统一，应由用户在单独一次文档整理中决定基准。

## 2. 统一恢复资格（复用既有领域规则）

本里程碑**没有新增状态抽象**。恢复资格完全由既有判定组合而成：

| 现场 | 判定依据（唯一来源） | 结论 |
|---|---|---|
| 仍有有效执行权 | `domain.TaskHoldsLease`、`workflowRunOwnsExecution`（`task_launch.go`）、`taskQueueRunHoldsExecution`（`task_queue.go`） | 不接管、不并发推进 |
| 执行权已失效且结果可确定 | `domain.DecideCancellationRecovery`、`domain.DecidePlanGenerationRecovery`、`domain.DecideTaskCloseoutRecovery`、`domain.DecideStepOccupancyRecovery` | 按既有状态机收敛 |
| 外部操作结果不确定 | `domain.DecideTaskCodeSaveResume`、`domain.DecideCodeSaveResume` | 进入或保持 `recovery_required`，等待核验 |
| 已请求取消但执行可能未停止 | `domain.DecideCancellationIntentRecovery` + `ActiveRunID` | 保留取消意图，不提前宣告已取消 |
| 已完成 | `TaskStatus.IsTerminal()`（`completed` / `cancelled`） | 不得因恢复重新执行 |
| `blocked` / `recovery_required` | `AllowsAutomaticScheduling()` 只含 `queued` | 不得仅因租约过期自动重放 |
| `failed` 且满足重试资格 | `domain.DecideTaskRetryEligibility`（**本里程碑新增**） | 允许用户显式开启新执行周期 |

自动调度、用户显式恢复与启动恢复三者共用同一批判定函数：调度器通过
`DecideTaskQueueAdmission`，启动恢复通过各自的 `Decide*Recovery`，重试通过
`DecideTaskRetryEligibility`。**没有任何一处只在 HTTP 层或调度器内临时写条件。**

## 3. 用户显式重试失败任务

### 3.1 领域规则

`internal/domain/task_retry.go`：

- `TaskStatus.AllowsExplicitRetry()` 只接受 `failed`。
- `DecideTaskRetryEligibility(TaskRetryInput) TaskRetryDecision` 按固定顺序收集**全部**
  拒绝原因（不短路）：
  1. `task_deleted`；
  2. `cancel_requested`；
  3. `status_not_retryable`；
  4. `lease_held`（有效租约由**他人**持有）；
  5. `active_workflow_run`；
  6. `step_occupied`（`running` / `developing` / `verifying`）；
  7. `recovery_residue`（`recovery_required`）；
  8. `external_side_effect_pending`（复用 `pendingSideEffect` → `DecideTaskCodeSaveResume`）；
  9. `prerequisite_missing`（复用 `checkTaskLaunchPrerequisites`，与 `CheckTaskLaunchStart` 同一实现）；
  10. `nothing_to_retry`（计划中已无可推进步骤）。
- `Task.ReopenForRetry(at)` 走状态机 `failed → preparing`，并清空上一周期的
  `final_result`、`failure_summary`、`current_step_id`、`current_attempt_id`。
  它**不改变任务标识**，也不删除任何历史记录。

### 3.2 应用层

`internal/app/task_retry.go` 的 `TaskRetryService.Retry` 顺序固定为：

```text
1  读取任务 / 步骤 / 未结束执行记录 / 代码保存记录 → DecideTaskRetryEligibility
2  TaskLeaseService.Claim(Statuses = {failed})           ← 执行权必须先失效才可能成功
3  用领取后的任务快照重新判定（Owner = 本次 owner）
4  UpdateTaskLeaseGuard 写入 failed → preparing          ← 租约栅栏
5  task.retried 事件（失败只记日志）
6  TaskLaunchService.Launch(Grant = 本次租约)            ← 复用唯一启动流程
```

失败语义：

| 失败点 | 结果 |
|---|---|
| 资格判定拒绝 | `ErrTaskRetryConflict` / `ErrTaskRetryPrerequisite` / `ErrTaskRetryNotFound`，**不领取租约** |
| 领取冲突 | `ErrTaskRetryConflict`（执行权已被他人持有） |
| 重开写入失败 | `ErrTaskRetryConflict`（状态或执行权已变更），释放租约，不继续启动 |
| 启动失败（非服务停止） | `ErrTaskRetryFailed`，释放租约，任务留在 `preparing` |
| 启动失败（服务停止） | `ErrTaskRetryFailed`，**保留租约**（现场无法确认） |

### 3.3 接口与 CLI

- `POST /api/v1/tasks/{taskID}/retry` → 202 + 真实初始状态；错误映射
  400 / 404 / 409 / 412 / 500（`execution_failed`）/ 503。
- `GET /api/v1/tasks/{taskID}/retry-eligibility` → 200，只读，回显 `can_retry` /
  `blocking` / `summary` / `task_status`。
- `agentflow task retry <task-id>`（含 `--wait` 轮询）与
  `agentflow task retry-status <task-id>`。

两个入口都先于任务管理服务可用性判定，因此任务服务未初始化时不会返回误导性错误。

## 4. 调度器重启协议

### 4.1 启动顺序（代码事实）

`internal/server/server.go` 的 `startServices` 注册顺序即启动顺序：

```text
执行节点预检 → StepExecutor → ValidationService → StepWorkflow
  → ContainerRecovery → Residue → CodeFetchRecovery
  → TaskWorkflow → TaskLaunch → TaskLeaseRecovery → TaskScheduler → CodeSaveRecovery
```

`TaskLeaseRecovery` 排在 `TaskScheduler` **之前**：调度器的第一次扫描不得在"租约过期但
现场未核验"的任务上做任何判断。停止顺序自动反转，先停调度器（不再分派新任务）。

### 4.2 接管规则

1. 有效租约不被抢占（`AcquireTaskLease` 条件 UPDATE）。
2. 过期租约的接管必须同时满足：任务状态属于调用方显式给出的集合、无未结束执行记录、
   无取消意图、无未确认的外部副作用（由各入口的领域判定保证）。
3. 旧执行者受 `owner + epoch` fencing 约束：不能续租、不能占用步骤、不能写入成功终态、
   不能继续启动新的 Agent 调用（心跳失败即 `cancel(errWorkflowLeaseLost)`）。
4. **数据库 fencing 不是远程进程已停止的证据**：`TaskLeaseRecoveryService` 只追加
   `task.recovery_required` 事件（`stage=recover_lease`）与 WARN 日志，字段包含
   `lease_owner` / `lease_epoch` / `expires_at` / `task_status`。
5. 不通过无条件清空租约或直接把任务改为 `queued` 来恢复吞吐量。
6. 启动恢复的必要持久化操作失败时 `Start` 返回错误，`lifecycle.Manager` 逆序清理，
   服务不会带着未收敛现场进入自动调度。

### 4.3 `TaskLeaseRecoveryService` 的判定顺序

```text
1 任务 lease_owner 为空            → 无现场，跳过
2 存在未结束执行记录               → 执行者可能仍在推进，跳过
3 租约仍有效                       → 不得抢占，跳过
4 其余（过期租约 + 无未结束执行）   → 追加 task.recovery_required 事件（recovery_auto=false）
```

读取失败（`ListTasks` / `GetActiveWorkflowRun`）让启动失败：无法判断是否存在残留时
继续启动，会让调度器在未知现场上做判断。事件写入失败只记日志（它是诊断信息，不是
状态迁移）。

## 5. 取消与失败收敛（复用既有能力，无新增状态机边）

本里程碑**没有**新增任何取消或失败相关的状态迁移。核实并补齐的边界：

| 边界 | 结论 | 依据 |
|---|---|---|
| 用户取消 vs 服务正常关闭 | 必须区分 | `isServiceStop`（`ErrStepExecutorStopped` / `ErrWorkflowStopped` / `ErrTaskLaunchStopped` / `ErrSchedulerStopped`）、`context.Cause` |
| 取消请求的持久化不依赖当前执行者持有租约 | 成立 | `TaskService.CancelTaskForAPI` 走 `UpdateTaskIfStatus`，无租约条件 |
| 取消请求成功 ≠ 远程操作已停止 | 成立 | 只写意图 + `cancelling`，`cancelled` 由真实收敛写入 |
| 超时 / 进程中断 / 租约丢失不得推导为业务成功或确定失败 | 成立 | `recovery_required` / `interrupted` / `timed_out` 三类独立结论 |
| 执行记录、任务状态与最终结论不得互相矛盾 | 成立 | P2-M5 收尾协议 + `domain.DecideTaskCloseoutView` |
| 无法确认外部操作结果时不释放资源、不重建容器、不启动重复操作 | 成立 | `ContainerRecoveryService` 只报告；`CodeSaveRecoveryService` 只报告 |
| 旧执行者迟到写入不得覆盖新执行者 | 成立 | `UpdateTaskLeaseGuard` / `UpdateWorkflowRun`（revision）/ `RenewTaskLease` |

## 6. 与 P2 恢复语义及外部副作用核验的边界

- **不做**的：把租约过期解释为可以安全重跑；对结果不确定的 commit / push / merge 自动
  重放；自动清理孤儿容器或自动重新关联；自动把 `failed` / `blocked` /
  `recovery_required` 改回 `queued`。
- **做**的：把上述现场变成可追溯、可查询、可由用户显式推进的状态。
- 未决外部副作用的唯一出口仍是既有协议：`agentflow code verify --record <id>` 核验后，
  `domain.DecideTaskCodeSaveResume` 才允许继续；`TaskRetryService` 直接复用该判定，
  因此"重试"不可能绕过核验。

## 7. 实际修改的文件及理由

| 文件 | 变更 | 理由 |
|---|---|---|
| `internal/domain/task_retry.go` | **新增** | 重试资格的唯一领域定义（`DecideTaskRetryEligibility`、`AllowsExplicitRetry`、`ReopenForRetry`、10 个稳定拒绝原因） |
| `internal/domain/task_launch.go` | 抽出 `checkTaskLaunchPrerequisites` | 启动与重试共用同一份"前置条件缺失"判定，避免出现第二份实现 |
| `internal/domain/event.go` | 新增 `EventTypeTaskRetried`（`task.retried`） | 重试是用户显式决定，必须留下审计记录 |
| `internal/app/task_retry.go` | **新增** | `TaskRetryService`：领域判定 → 领取执行权 → 租约栅栏重开 → 交接统一启动流程；错误类别区分 404 / 409 / 412 / 500 / 503 |
| `internal/app/task_lease_recovery.go` | **新增** | 启动时核验过期租约残留：只记录现场，不释放租约、不改写状态 |
| `internal/httpapi/task_retry.go` | **新增** | `POST .../retry` 与 `GET .../retry-eligibility` 的协议转换与状态码映射 |
| `internal/httpapi/router.go` | 新增 `Deps.TaskRetry`、`router.taskRetry`、`isTaskRetryPath` 分发 | 重试入口先于任务服务可用性判定 |
| `internal/httpserver/server.go` | `Options.TaskRetry` → `httpapi.Deps.TaskRetry` | 装配 |
| `internal/server/server.go` | 构造并注册 `TaskRetryService` 与 `TaskLeaseRecoveryService`；`TaskLeaseRecovery` 排在 `TaskScheduler` 之前 | 保证"先恢复、后调度"的启动顺序 |
| `internal/cli/task_retry.go` | **新增** | `agentflow task retry` 与 `agentflow task retry-status` |
| `internal/cli/task_workflow_client.go` | 新增 `retry` / `retryEligibility` 客户端方法与输出函数 | 命令层只做 HTTP 客户端 |
| `internal/cli/task.go` | 注册两个子命令、更新 `Short` | 入口可见 |
| `tests/unit/domain/task_retry_test.go` | **新增** | 领域规则矩阵（13 个状态 × `AllowsExplicitRetry`、10 类阻塞事实、重开语义） |
| `tests/integration/local/scheduler/restart_protocol_test.go` | **新增** | 调度器重启、过期租约、租约残留核验、双连接一致性 |
| `tests/integration/local/scheduler/retry_test.go` | **新增** | 重试准入、执行权竞争、并发、写入失败、历史保留 |
| `tests/unit/httpapi/task_retry_test.go` | **新增** | 协议与状态码映射 |
| `tests/integration/local/workflow/retry_e2e_test.go` | **新增** | 端到端重试、取消、重启收敛 |
| `tests/integration/local/workflow/e2e_fixture_test.go` | 装配 `TaskRetryService` 与 `TaskLeaseService` | 端到端现场需要真实重试服务 |

**未新增**：数据库迁移、新的任务 / 步骤 / 执行记录状态、第二套启动流程、第二套所有权。

## 8. 测试

### 8.1 新增用例

| 文件 | 覆盖 |
|---|---|
| `tests/unit/domain/task_retry_test.go` | 只有 `failed` 允许重试；10 类阻塞事实逐项拒绝；一次给出全部原因；`ReopenForRetry` 清空结论与占用且不改变任务身份；拒绝非失败 / 已取消 / 已删除 |
| `tests/integration/local/scheduler/restart_protocol_test.go` | 正常关闭后重启不抢占有效租约；过期租约 + 结论不明不被自动重放；租约残留只记录不释放；有效租约与未结束执行记录不产生残留结论；读取失败让启动失败；双连接看到同一份租约事实 |
| `tests/integration/local/scheduler/retry_test.go` | 失败任务经重试进入新周期（新代次、清空结论、审计事件）；非失败 / 有效租约 / 未决副作用 / 恢复残留逐项拒绝且不调用启动；启动失败释放租约；服务停止保留租约；并发只有一个成功；资格查询只读；历史执行记录保留；数据库写入失败不伪造成功 |
| `tests/unit/httpapi/task_retry_test.go` | 202 + 响应字段；405 + `Allow`；非失败 409；不存在 404；未装配 503；资格查询只读 |
| `tests/integration/local/workflow/retry_e2e_test.go` | 端到端重试进入新周期并保留历史；执行记录未结束时拒绝重试；已结束任务拒绝取消；进程崩溃后 `running` 记录收敛为 `blocked` / `worker_interrupted` 且不重放 Agent |

### 8.2 实际执行的命令与结果

```text
$ go build ./...
（无输出，成功）

$ go vet ./...
（无输出，无新增问题）

$ gofmt -l internal cmd tests
（无输出，格式正确）

$ go test -count=1 ./tests/unit/... ./tests/integration/local/...
Go test: 2032 passed in 35 packages

$ go test -count=1 ./tests/...
Go test: 2107 passed in 46 packages
```

- **通过**：`./tests/...` 全部 46 个包、2107 个用例（含真实远程依赖用例，`tests/config`
  中 `ssh.enable` / `docker.remote_enabled` / `pi.enable` 均为 `true`）。
- **失败**：无（最终状态）。
- **跳过**：无。
- **未执行**：前端测试（`lingting-agent-flow-ui/`），本里程碑不涉及前端改动。
- **无法确认**：无。

`TEST_ARTIFACT_DIR` 由 `testkit.Main` 在每次运行时创建于
`os.TempDir()/lingting-agent-flow-tests/` 下并输出绝对路径，产物按仓库约定保留。
本次涉及的测试包产物目录：

- `C:/Users/lingting/AppData/Local/Temp/lingting-agent-flow-tests/scheduler-4122913590`
  （`tests/integration/local/scheduler`，57 个用例全部通过）
- `C:/Users/lingting/AppData/Local/Temp/lingting-agent-flow-tests/workflow-3649218110`
  （`tests/integration/local/workflow`）

## 9. 完成定义核对

| 条件 | 状态 | 证据 |
|---|---|---|
| 调度器启动顺序与恢复前置条件有代码与测试证据 | ✅ | §4.1；`server.startServices` 注册顺序；`TestSchedulerRestartAfterGracefulStop`、`TestSchedulerAndLeaseRecoveryShareDatabase` |
| 租约过期、执行权接管与旧执行者 fencing 行为正确 | ✅ | §4.2；新增 `TestLeaseRecovery*` 4 个用例 + 既有 `TestTaskLeaseEpochIsFenced` / `TestStaleLeaseCannotOccupyStep` / `TestStaleLeaseCannotWriteTaskStatus` |
| 取消、服务关闭、进程崩溃与结果不确定能被区分并正确收敛 | ✅ | §5；`TestE2ECancelPersistsIntentBeforeStop`、`TestE2ERestartConvergesRunningRunToBlocked` + 既有 P2-M2 用例 |
| `failed` 任务有经领域规则约束的显式重试入口 | ✅ | §3；`DecideTaskRetryEligibility` + `TaskRetryService` + HTTP + CLI；14 个重试用例 |
| 关键恢复路径不产生未经确认的重复外部副作用 | ✅ | §6；重试复用 `DecideTaskCodeSaveResume`，未决副作用直接拒绝；`TestRetryRejectsPendingSideEffect` |
| 多任务执行、取消与恢复不互相串扰 | ✅ | 既有 `TestTwoTasksAdvanceConcurrentlyUnderOwnLeases`、`TestRecoveringOneTaskKeepsAnotherTaskContainer`；新增用例全部按任务隔离断言租约与记录 |
| 单元 / 集成 / 端到端测试有真实执行记录 | ✅ | §8.2 |
| 路线图、实现状态、测试结果与剩余风险一致 | ✅ | 本文件与 `.steps/README.md`、`ROADMAP.md`、`P3-multi-task-scheduling.md` |

## 10. 未解决风险与明确非目标

### 10.1 明确不在本里程碑范围

- 跨进程全局并发配额（需要数据库仲裁）。
- 分布式调度与跨机器抢占。
- 优先级抢占、事件驱动调度。
- 大规模重构现有单任务工作流。
- 对不确定的外部操作自动重放。
- 孤儿容器自动重新关联或自动删除。
- 前端重试入口（本次只交付 API 与 CLI；UI 需要单独确认交互）。

### 10.2 剩余风险

| 编号 | 风险 | 影响 | 缓解 / 下一步 |
|---|---|---|---|
| R1 | 租约残留只记录、不释放 | 过期租约在 TTL 内仍占名额 | 有意的取舍：清空会让旧执行者的远程操作与新执行者并发；TTL 默认 10 分钟 |
| R2 | `TaskLeaseRecoveryService` 不判断远程进程是否真的停止 | 数据库与物理事实可能不同步 | 这正是"不把 fencing 当作远程已停止"的要求；物理收敛需要节点侧可观测能力（后续里程碑） |
| R3 | 重试后任务停在 `preparing` 但启动失败 | 用户需要再次发起重试 | 结果与失败阶段如实返回；租约已释放，可再次重试 |
| R4 | 重试入口只接受 `failed` | `blocked` / `recovery_required` 仍需走 `resume-workflow` | 有意：这两类现场尚未被判定为失败，语义是"继续推进"而不是"新周期" |
| R5 | 服务停止时保留的租约 | 任务在 TTL 内不可被重新领取 | 有意（P3-M2 已确立）；`TestRetryKeepsLeaseOnServiceStop` 固定该行为 |
| R6 | 调度扫描仍是轮询（默认 1s） | 空扫描开销与最坏 1s 的入队延迟 | 可观测、无"事件丢失即永不扫描"的故障面 |
| R7 | `P3-M3` 文档的完成日期（2026-10-26）晚于本次实际实施时间（2026-10-25） | 文档时间线不自洽 | 已记录于 §1.2；是否需要统一由用户决定 |

## 11. 下一步

P3-M5 —— 验证多任务并发、冲突和资源回收行为：

- 多任务并发下的名额、容器、缓存与 Session 争用的系统性验收；
- 真实远程环境主动注入 `unknown` / 孤儿容器现场；
- 跨进程全局配额的数据库仲裁方案（如确有需求）。

本里程碑交付的恢复资格判定、租约残留核验与显式重试入口是 P3-M5 的前提，不需要重新设计。
