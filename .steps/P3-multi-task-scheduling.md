# P3 —— 多任务执行与调度

> 撰写依据：commit `330e7c4`（2026-10-23）工作区的实际代码与测试状态。
> P3 的细节以 P2 完成后补充为准，本文档只明确工作边界、依赖、交付目标与验收条件，
> 不在缺少实现证据时编造细节。

## 阶段目标

在单任务闭环可靠运行之后，让多个任务能够被管理和调度，而不破坏资源隔离与状态正确性。
阶段结束时，用户实际能完成：

- 多个任务进入统一队列，按明确语义被领取与调度。
- 并发有界，任务生命周期协调正确，不出现同一任务被重复推进或资源被互相踩踏。
- 执行环境、工作区、Session 与资源之间的隔离规则明确且被遵守。
- 任务的取消、失败、恢复与调度器重启都能正确处理。
- 多任务并发、冲突与资源回收行为有验证证据。

## 必要性

单任务闭环（P1）与可靠性（P2）成立后，才能真正评估"多个任务同时推进"对资源、状态与
恢复的冲击。过早建设通用调度框架会与真实需求脱节；但在单任务稳定后，没有调度能力，
AgentFlow 只能一次处理一个任务，无法成为可用的编排器。

## 前置条件

- P2 退出（单任务在超时/取消/重启/持久化失败下正确停止或恢复）。
- 已有能力：任务管理、任务级工作流编排（`workflow_runs` 的租约与唯一性索引）、
  执行环境生命周期、`internal/lifecycle` 后台服务契约。

## 当前状态（摘要，基于代码证据）

**已完成（可复用基础）**：

- 单任务的启动、计划、执行、验证、代码保存、收尾与恢复（P1/P2 范围）。
- `workflow_runs` 的 `idx_workflow_runs_active_task` 部分唯一索引（迁移 `0018`）与租约
  （`WorkerID` / `LeaseExpiresAt`）——这是未来调度一致性的基础。
- 后台服务通过 `internal/lifecycle` 统一注册、启动、停止。
- **P3-M1**：任务队列、领取、调度名额与执行权交接的领域契约
  （`internal/domain/task_queue.go`）。详见
  [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md)。
- **P3-M2**：任务租约的持久化 fencing（`tasks.lease_epoch`，迁移 `0024`）、统一所有权
  （`app.WorkflowExecutionGrant`）、有界并发调度器（`app.TaskScheduler`）与生命周期
  集成。详见 [`P3-M2-bounded-concurrency.md`](./P3-M2-bounded-concurrency.md)。

**尚未实现**：

- 跨数据库的全局容量配额（P3-M5 已明确语义与明确的非目标，见下文）。
- 前端重试入口（P3-M4 交接事项，尚未实现）。

已完成（P3-M3 / P3-M4）：

- 跨任务的资源隔离与回收规则（P3-M3，见
  [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md)）。
- 调度器重启后的恢复协议：启动恢复先于调度、过期租约残留只记录不释放、旧执行者受
  `owner + epoch` fencing 约束（P3-M4，见
  [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md)）。
- `failed` 任务的用户显式重试入口（P3-M4：
  `domain.DecideTaskRetryEligibility` + `app.TaskRetryService` +
  `POST /api/v1/tasks/{taskID}/retry` + `agentflow task retry`）。

## 阶段范围

**做**：

- 明确任务队列、领取和调度的业务语义（M1）。
- 实现有界并发和任务生命周期协调（M2）。
- 明确执行环境、工作区、Session 和资源之间的隔离规则（M3）。
- 处理任务取消、失败、恢复和调度器重启（M4）。
- 验证多任务并发、冲突和资源回收行为（M5）。

**不做**：

- 不重复实现单任务闭环；调度只调用既有 `app.TaskWorkflowService` / `app.TaskLaunchService`。
- 不提前做调度策略优化、优先级抢占、跨机器分布式调度等未确认需求。
- 不引入与真实需求脱节的通用任务框架。

## 里程碑清单与依赖

| ID | 名称 | 状态 | 依赖 |
|---|---|---|---|
| P3-M1 | 明确任务队列、领取和调度的业务语义 | `completed` | P2 退出 |
| P3-M2 | 实现有界并发和任务生命周期协调 | `completed` | P3-M1 |
| P3-M3 | 落实执行环境、工作区、Session 与资源隔离 | `completed` | P3-M1 |
| P3-M4 | 处理任务取消、失败、恢复和调度器重启 | `completed` | P3-M2、P3-M3 |
| P3-M5 | 验证多任务并发、冲突和资源回收行为 | `completed` | P3-M2、P3-M3、P3-M4 |

## 阶段退出条件

1. ✅ 多任务可被入队、领取并按明确语义调度，且并发有界（P3-M2：`app.TaskScheduler`
   + `cfg.Scheduler.MaxConcurrentTasks`）。
2. ✅ 同一任务不会被两个执行者同时推进（P3-M2：`lease_epoch` 栅栏 +
   `workflow_runs` 部分唯一索引；`TestAcquireTaskLeaseIsExclusiveAcrossConnections`）。
3. ✅ 执行环境、工作区、Session 与资源之间互不串扰，且规则成文并落地（P3-M3：
   `DecideContainerOwnership` 容器归属契约 + 单步骤入口统一领取任务租约 +
   资源回收所有权校验；`tests/integration/local/scheduler/isolation_test.go`）。
4. ✅ 任务取消、失败、恢复与调度器重启的语义明确，无 `running` 残留与重复副作用
   （P3-M4：`TaskLeaseRecoveryService` + `TaskRetryService` + 既有恢复入口；
   见 [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md)）。
5. ✅ 多任务并发、冲突与资源回收有测试证据（P3-M5：
   `tests/integration/local/scheduler/{concurrency,resource}_acceptance_test.go` 与
   `tests/integration/remote/docker/concurrency_acceptance_test.go`；并修复 4 个生产缺陷
   D1–D4，见 [`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md)）。

## 风险与边界

- **资源泄漏**：调度器重启或多任务并发时，容器/Session/租约若不能正确收敛，会泄漏资源。
- **状态冲突**：并发领取同一任务会破坏单任务闭环；必须沿用事务 + 条件更新 + 唯一索引。
- **重复副作用**：调度重放外部操作（push/merge）会重复副作用，必须复用 P2 的核验语义。
- **无效投入**：不为"集群/多节点/优先级抢占"等未确认场景预先设计，先从单进程有界并发做起。

## 下一步选择规则

**下一步入口：P4（产品操作体验与针对性优化）。**

顺序固定：M1/M2/M3/M4/M5 全部已完成 → **P4**。

P3-M5 已落地的前提与交接事项见
[`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md)：并发上限语义为
"同一份数据库上的名额上限"（跨库全局配额是明确的非目标）；数据库 fencing 不等于远程
命令已停止；真实远程 `unknown` 现场的"注入"用受控观测端口包裹真实连接；前端重试入口
仍未实现。

P3-M4 已落地的前提（P3-M5 不需要重新设计）：

1. 启动顺序固定为"恢复 → 调度"：`TaskLeaseRecovery` 注册在 `TaskScheduler` 之前。
2. 过期租约残留只记录现场（`task.recovery_required` 事件 + WARN 日志），不释放租约、
   不改写任务状态、不重放外部操作。
3. `failed` 任务只能经 `domain.DecideTaskRetryEligibility` 判定通过后显式重试，
   重试复用 `TaskLaunchService.Launch` 与 `DecideTaskCodeSaveResume`。

**P3-M4 的交接事项**：

- 租约残留核验不判断远程进程是否真的停止（物理收敛需要节点侧可观测能力）。
- 重试入口只接受 `failed`：`blocked` / `recovery_required` 仍走 `resume-workflow`。
- 前端重试入口尚未实现（只交付 API 与 CLI）。
- 跨进程全局并发配额仍需数据库仲裁。

P3-M3 已落地的前提（P3-M4 不需要重新设计）：

1. 容器归属契约（`domain.DecideContainerOwnership`）与统一加载入口
   （`app.loadOwnedContainer`）：所有执行入口读取容器登记记录并核验
   `task_id` / `host_id` / 生命周期状态 / `docker_id`，不再直接用字符串。
2. 单步骤三个入口（`execute` / `execute-steps` / `execute-and-validate`）统一领取
   任务租约，租约覆盖整个调用周期。
3. 步骤占用与任务终态写入具备租约栅栏（`StepExecutionStartRequest.Lease` +
   `StepExecutionStore.UpdateTaskLeaseGuard`）。
4. 领取冲突细分为 404 / 409（`ErrTaskLeaseNotFound` + `classifyClaimConflict`）。
5. 共享资源边界成文：缓存卷名不含任务标识，镜像与缓存从不自动删除。

**P3-M3 的交接事项**：

- 租约栅栏只保护数据库写入；已发出的远程命令是否停止取决于下层执行器。P3-M4 已把
  该边界落成显式协议：`TaskLeaseRecoveryService` 只记录现场，不把 fencing 当作
  "远程进程已停止"的证据。
- 真实远程依赖用例（`tests/integration/{ssh,pi,remote/**}`）已全部通过；孤儿容器与
  `unknown` 现场的真实远程主动注入验收已在 P3-M5 完成
  （`tests/integration/remote/docker/concurrency_acceptance_test.go`）。
- 跨进程全局并发配额需要数据库仲裁。
- （P3-M4 已解决）`failed` 任务现在有经领域规则约束的显式重试入口。

P3-M2 已落地的前提（P3-M3/M4 不需要重新设计）：

1. `AcquireTaskLease` 由 `app.TaskScheduler` 生产调用，显式传
   `Statuses: domain.AutomaticSchedulingStatuses()`（只有 `queued`）。
2. `ReleaseTaskLease` 返回 `(bool, error)` 并检查受影响行数；
   `RenewTaskLease` 以 `owner + epoch` 为条件。
3. 任务级 fencing 落地为 `tasks.lease_epoch`（迁移 `0024`）与
   `UpdateTaskLeaseGuard`（status + owner + epoch）。
4. 任务租约是唯一令牌，`WorkflowRun.WorkerID` 取自任务租约 owner，心跳同时续租两者。
5. 有界并发复用 `cfg.Scheduler.MaxConcurrentTasks`，名额统计基于
   `domain.DecideTaskSlotState` 的数据库事实。
6. `app.TaskScheduler` 注册进 `internal/lifecycle`，停止顺序在启动服务之前。
7. 真实 SQLite + 并发 goroutine 已验证"只有一个领取成功"（P3-M1 验收项 S6）。

**P3-M2 的交接事项**：

- 并发上限是**进程级**：跨进程全局配额需要数据库仲裁，属后续里程碑。
- 服务停止保留的租约依赖 TTL 自然过期；P3-M4 已把该现场落成可追溯记录。
- （P3-M4 已解决）`failed` 任务现在有经领域规则约束的显式重试入口。
- 轮询扫描（默认 1s）是可接受的取舍；事件驱动留待有证据的需求。
- （P3-M3 已解决）`StepExecutor` 的任务终态写入现在是租约栅栏写，不再是纯状态条件写。

**入口审计结论（P3-M3 完成后）**：

| 入口 | 是否经过唯一所有权 |
|---|---|
| `POST /api/v1/tasks/{id}/start` | ✅ 先 `TaskLeaseService.Claim`，成功后交接给工作流 |
| `POST /api/v1/tasks/{id}/plan` | ✅ 同上；计划生成是同步操作，返回时释放 |
| `POST /api/v1/tasks/{id}/execute-workflow` | ✅ 同上 |
| `POST /api/v1/tasks/{id}/resume-workflow` | ✅ 同上 |
| `POST /api/v1/tasks/{id}/execute-steps`（单步骤顺序执行） | ✅ P3-M3 统一：领取任务租约并覆盖整个周期 |
| `POST .../steps/{id}/execute` | ✅ P3-M3 统一：同上 |
| `POST .../steps/{id}/execute-and-validate` | ✅ P3-M3 统一：同上 |
| `POST .../steps/{id}/open-repair-cycle` | ❌ 不需要：只登记预算作用域，不启动 Agent |
| 自动调度（`app.TaskScheduler`） | ✅ 显式 `queued` 集合领取 |
| 用户取消（`POST .../cancel`） | ❌ 有意：取消意图必须先落盘，不受租约阻塞 |
| 用户显式重试（`POST .../retry`） | ✅ P3-M4：先按领域规则判定资格，再以 `failed` 集合领取任务租约，成功后交接给统一启动流程 |

---

## 里程碑详情（骨架）

> 以下里程碑在 P2 完成后、具备真实调度需求证据时补充工作项细节。当前只给出边界，避免
> 在缺少实现证据时编造细节。

### P3-M1 明确任务队列、领取和调度的业务语义

- **ID**：`P3-M1`
- **状态**：`completed`（2026-10-25，基于 `master` HEAD `6e6d8b2` 之后的工作区核实与实施）
- **必要性**：没有明确语义，调度会退化为"随意并发"，破坏状态正确性。
- **当前证据（核实结论）**：
  - `TaskStatus.CanStart()` 会为 7 个状态返回 `true`（含 `failed` / `paused` /
    `recovery_required`），**不能**直接当作队列准入规则。
  - `Task.LeaseOwner` / `LeaseExpiresAt` 两列与 `AcquireTaskLease` /
    `ReleaseTaskLease` 已实现，但**全仓库无任何生产调用方**——不是"已有领取安全"的证据。
  - `UpdateTaskIfStatus` 的条件只有 `status IN (...)`，**不含租约条件**，不足以支持原子领取。
  - `ReleaseTaskLease` 不检查 `RowsAffected`，无法区分"释放成功"与"没释放任何东西"。
  - 任务租约与 `WorkflowRun` 租约的 owner 完全无关（后者由
    `TaskWorkflowService.owner` 生成），两套所有权互不约束。
- **前置依赖**：P2 退出。
- **目标结果**：成文的任务入队、领取、调度、并发上限语义。
- **完成定义**：语义在领域层定义，调度与恢复都引用它。
- **交付物**：
  - [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md)（完整契约文档）
  - `internal/domain/task_queue.go`：`AllowsAutomaticScheduling`、
    `DecideTaskQueueAdmission`、`NewTaskQueueOrderKey` / `CompareTaskQueueOrder` /
    `SortTaskQueueOrderKeys`、`DecideTaskClaim`、`TaskHoldsExecution`、
    `DecideTaskExecutionHandover`、`DecideTaskSlotState` 与 `TaskQueueRejection`
  - `tests/unit/domain/task_queue_test.go`（30 个用例）
- **明确未做**：调度循环、有界并发执行器、容量控制器、调度器重启接管；未修改任何
  生产执行流程、存储层或 HTTP / CLI 接口。
- **移交 P3-M2**：存储层原子领取验收项 S1–S7（见契约文档 §4.5）。

### P3-M2 实现有界并发和任务生命周期协调

- **ID**：`P3-M2`
- **状态**：`completed`（2026-10-25）
- **必要性**：无界并发会耗尽执行环境与模型额度；两套所有权会让旧执行者覆盖新领取者。
- **前置依赖**：P3-M1。
- **目标结果**：有界并发与任务生命周期协调实现，且不破坏单任务闭环。
- **完成定义**：并发上限可配置，同一任务不被重复推进。
- **交付物**：
  - [`P3-M2-bounded-concurrency.md`](./P3-M2-bounded-concurrency.md)（完整契约与证据）
  - `internal/domain/task_lease.go`：租约身份、持有判定、失效原因、续期判定、释放结论
  - `internal/app/task_lease.go`：`TaskLeaseService`（领取 / 续期 / 释放 / 栅栏写入 / 事件）
  - `internal/app/task_workflow_lease.go`：`WorkflowExecutionGrant` 与双租约协调
  - `internal/app/task_scheduler.go`：`TaskScheduler`（扫描 / 排序 / 领取 / 分派 / 收尾）
  - 迁移 `0024_task_lease_epoch.sql`：`tasks.lease_epoch`
  - 测试：`tests/unit/domain/task_lease_test.go`、
    `tests/integration/local/scheduler/{lease_store,scheduler}_test.go`
- **明确未做**：跨任务隔离规则（M3）、调度器重启接管（M4）、多任务验收（M5）、
  分布式配额、`failed` 任务的显式重试入口。
- **移交 P3-M3/M4**：进程级并发上限、`StepExecutor` 入口的租约栅栏、服务停止后租约的
  物理收敛、`failed` 显式重试入口。

### P3-M3 落实执行环境、工作区、Session 与资源隔离

- **ID**：`P3-M3`
- **状态**：`completed`（2026-10-26，基于 `master` HEAD `7e950e7` 之后的工作区核实与实施）
- **必要性**：多任务并发下，容器/Session/工作区串扰会导致错误结果。
- **本次核实到的真实缺口**：执行链路直接用 `task.ContainerID` 字符串核验容器（该列无外键）、
  没有"容器归属"领域概念、单步骤三个入口不领取任务租约、步骤占用与任务终态写入只有
  状态条件、领取冲突无法区分 404/409。
- **前置依赖**：P3-M1（`completed`）。
- **目标结果**：跨任务隔离规则成文并落地。
- **完成定义**：每个任务的容器、工作区、Session 互不串扰。
- **交付物**：
  - [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md)（完整契约与证据）
  - `internal/domain/container_ownership.go`：`DecideContainerOwnership` /
    `CheckContainerOwnership` 与 9 个稳定拒绝原因
  - `internal/app/task_container.go`：`loadOwnedContainer`（统一加载并核验归属）
  - 六个执行入口改为经 `loadOwnedContainer`（工作区初始化、代码初始化、步骤执行、
    验证、工作流编排、任务启动）
  - 单步骤三个入口统一领取任务租约；`StepExecutionStartRequest.Lease` +
    `StepExecutionStore.UpdateTaskLeaseGuard` 提供租约栅栏
  - `ErrTaskLeaseNotFound` + `classifyClaimConflict` 把领取冲突细分为 404 / 409
  - 测试：`tests/unit/domain/container_ownership_test.go`、
    `tests/integration/local/scheduler/{isolation,lease_fencing,resource_reclaim}_test.go`
- **明确未做**：调度器重启接管（M4）、多任务验收（M5）、分布式配额、
  真实远程环境下的孤儿容器主动注入验收、`failed` 显式重试入口。
- **移交 P3-M4**：租约栅栏只保护数据库写入，已发出的远程命令的物理收敛属 P3-M4；
  真实远程依赖用例已全部通过；孤儿容器与 `unknown` 现场的真实远程主动注入验收
  已在 P3-M5 完成。

### P3-M4 处理任务取消、失败、恢复和调度器重启

- **ID**：`P3-M4`
- **状态**：`completed`（2026-10-25，基于 `master` HEAD `b61b7e4` 之后的工作区核实与实施）
- **必要性**：调度器重启或多任务取消时，若不能正确收敛，会卡死或重复执行外部副作用。
- **本次核实到的真实缺口**：过期租约的失效事实没有落成可追溯现场；`failed` 任务在
  生产代码中没有任何显式重试入口（状态机允许 `failed → preparing`，但
  `ExplicitLaunchStatuses` 明确排除 `failed`，调度器只领取 `queued`）。
- **前置依赖**：P3-M2、P3-M3。
- **目标结果**：任务取消 / 失败 / 恢复与调度器重启语义明确且正确，用户有显式重试入口。
- **完成定义**：重启后无 `running` 残留，无未经确认的重复副作用；失败任务只能经领域
  规则约束的显式入口进入新执行周期。
- **交付物**：
  - [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md)（审计表、协议与证据）
  - `internal/domain/task_retry.go`：`DecideTaskRetryEligibility`、`AllowsExplicitRetry`、
    `ReopenForRetry`、10 个稳定拒绝原因
  - `internal/domain/task_launch.go`：抽出 `checkTaskLaunchPrerequisites` 供启动与重试共用
  - `internal/app/task_retry.go`：`TaskRetryService`（领域判定 → 领取执行权 → 租约栅栏
    重开 → 交接统一启动流程）
  - `internal/app/task_lease_recovery.go`：`TaskLeaseRecoveryService`（过期租约残留只记录
    现场，不释放租约、不改写状态）
  - `internal/httpapi/task_retry.go`：`POST .../retry` 与 `GET .../retry-eligibility`
  - `internal/cli/task_retry.go`：`agentflow task retry` 与 `agentflow task retry-status`
  - `internal/server/server.go`：`TaskLeaseRecovery` 注册在 `TaskScheduler` 之前
  - 测试：`tests/unit/domain/task_retry_test.go`、
    `tests/integration/local/scheduler/{restart_protocol,retry}_test.go`、
    `tests/unit/httpapi/task_retry_test.go`、
    `tests/integration/local/workflow/retry_e2e_test.go`
- **明确未做**：跨进程全局配额、分布式调度、优先级抢占、孤儿容器自动重新关联、
  结果不确定的外部操作自动重放、前端重试入口。
- **移交 P3-M5**：多任务并发与资源回收的系统性验收；真实远程环境主动注入 `unknown` /
  孤儿容器现场；跨进程全局配额的数据库仲裁方案。

### P3-M5 验证多任务并发、冲突和资源回收行为

- **ID**：`P3-M5`
- **状态**：`completed`（2026-10-25，基于 `master` HEAD `abfdfd3` 之后的工作区核实与实施）
- **必要性**：并发与资源回收必须靠真实测试证明。
- **本次核实到的真实缺口**：取消入口用无条件整行写入会覆盖并发领取的租约（D1）；
  CLI 与 HTTP 各有一套取消状态语义且 `queued` 任务停在 `cancelling`（D2）；
  `isContainerMissing` 把归属不匹配当作"容器不存在"（D3）；`TaskScheduler.Scan` 的
  名额统计与领取不在同一临界区，并发扫描实测超额领取（D4）。
- **前置依赖**：P3-M2、P3-M3、P3-M4。
- **目标结果**：并发、冲突、资源回收的测试证据。
- **完成定义**：关键并发与回收场景有可复核的测试产物与结论。
- **交付物**：
  - [`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md)（缺口矩阵、
    并发上限语义、4 个缺陷的修复与证据、真实远程结果）
  - `internal/domain/task.go`：`Task.ApplyCancellationRequest`（取消语义唯一落点）
  - `internal/storage/interfaces.go` + `internal/storage/sqlite/tasks.go`：
    `UpdateTaskCancellation`（只写取消列，带状态前置条件）
  - `internal/app/task_scheduler.go`：`scanMu`（名额统计与领取同临界区）
  - `internal/app/execenv.go`：`isContainerMissing` 只认 `CodeContainerNotFound`
  - 测试：`tests/integration/local/scheduler/{concurrency,resource}_acceptance_test.go`
    （16 个用例）、`tests/integration/remote/docker/concurrency_acceptance_test.go`
    （3 个真实远程用例）
- **明确未做**：跨数据库全局容量仲裁、事件驱动调度、优先级抢占、前端并发状态展示。
- **移交 P4**：并发上限语义已明确（跨库全局配额是明确的非目标）；数据库 fencing
  不等于远程命令已停止；真实远程 `unknown` 现场的"注入"用受控观测端口包裹真实连接；
  前端重试入口仍未实现。
