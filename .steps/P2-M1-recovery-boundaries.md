# P2-M1 —— 全链路状态持久化与恢复边界清单

> 撰写依据：`master` HEAD `7a55dfe`（"修复执行容器部分网络环境无法正常推送代码"）的真实代码与
> 测试状态，逐项核对 `internal/`、`internal/storage/sqlite/migrations/` 与 `tests/` 得出。
> 本文档只描述**当前已存在**的机制与**已核实**的缺口，不描述计划中的实现。
> 每条结论都指向具体文件与函数；无法指向代码的推测一律标注为"待确认"。
>
> **P2-M2 更新（2026-10-24，基于 `master` HEAD `2a1bb0d`）**：B1、B2、B3、B5、B6 与
> §5.8 的测试竞态已在 P2-M2 修复，相关段落标注为"P2-M2 修复（已完成）"，§3.4 与 §6.2
> 的缺口表已按实际状态更新。
>
> **P2-M5 更新（2026-10-25，基于 `master` HEAD `40460d4`）**：B4（容器 `unknown` / 孤儿）
> 与 G1–G9 / G14 已在 P2-M5 修复并有集成测试证据，新增 G15–G17 三个窗口（任务卡在
> `verifying`/`saving`、收尾跨表不一致、丢失执行权仍写任务成功）同样已修复。§2.3 补齐
> 了 Attempt 级 `code_fetches` 的恢复边界，§3.4 与 §6.2 已按实际状态重写，§5.4 标为已修复。
> 本文其余描述（§1–§2 的机制与矩阵、§4 的边界说明）仍然成立。

## 0. 阅读约定

- **自动恢复**：有正式调用入口，依据持久化事实执行边界明确的收敛动作，且由服务生命周期触发。
- **用户触发恢复**：能力已存在，但必须由用户通过 CLI / HTTP 显式调用。
- **只读核验**：能读取真实外部状态，但不修改业务状态（或只写核验结论字段）。
- **只能安全停止**：外部副作用或执行结果无法确认，系统不应自动重放。
- **缺少恢复能力**：没有完整处理路径，或只有局部逻辑，不能保证状态正确收敛。

"有方法存在"不等于"运行时一定会调用它"。本文档所有"当前恢复入口"都标注了**真实调用者**，
仅被 CLI 调用或仅被测试调用的方法会显式说明。

---

## 1. 恢复机制整体结构

### 1.1 服务启动时的真实调用顺序

装配点：`internal/server/server.go` 的 `startup` → `startServices`。后台服务注册顺序
（`startServices`，`services` 依次 append）决定启动顺序，`internal/lifecycle/manager.go` 的
`Manager.Start` 按注册顺序启动、`stopServices` 逆序停止。

```text
agentflow server（internal/cli/server.go:runServer）
  → Server.Run
      → Server.startup
          → openDatabase                      # 打开 SQLite、执行迁移、构造 SessionService
          → startServices                     # 构造执行节点/编排/启动服务并注册
              → lifecycle.Manager.Start（按注册顺序）
                  1. 注入的 services（server.Options.Services，默认为空）
                  2. app.HostValidationService.Start
                        → recoverPending           # checking 节点重新入队（失败只记日志）
                  3. app.StepExecutor.Start        # 只设置生命周期标志，无任何恢复
                  4. app.ValidationService.Start   # 只设置生命周期标志，无任何恢复
                  5. app.StepWorkflowService.Start # 只设置生命周期标志，无任何恢复
                  6. app.ExecutionResidueService.Start  # P2-M2 新增
                        → 收敛残留 validation_runs（→ failed + verdict=failed）
                        → 收敛残留 attempts（→ interrupted）
                        → 收敛残留 sessions（→ interrupted，产物路径保留）
                  7. app.TaskWorkflowService.Start
                        → recoverUnfinished           # 未结束的 workflow_runs
                        → recoverCancellationIntents  # 取消意图跨层补齐（P2-M2 新增）
                        → recoverPlanGeneration       # 停留在 planning 的任务
                        → recoverCancelling           # 停留在 cancelling 的任务
                  8. app.TaskLaunchService.Start
                        → recoverWorkspace            # 停留在 pending/running 的工作区初始化
                        → RecoverStepOccupancy        # 停留在 running/developing/verifying 的步骤
          → startHTTP                         # 之后才开始接受新请求
```

关键事实：

- **恢复入口是否必然执行**：上述 2、6、7、8 号服务的恢复是 `Start` 的一部分，必然执行；
  但 3、4、5 号服务**没有任何启动恢复**，它们只在停止时收敛在途调用。
- **失败传播**：`Manager.Start` 遇到任一服务 `Start` 返回错误即中止，并逆序 `Stop` 已启动的
  服务；`Server.startup` 返回错误，`Server.Run` 用 `errors.Join(ErrStartup, err, Stop(...))`
  收尾。因此 `TaskWorkflowService` / `TaskLaunchService` 的恢复失败会**阻止服务启动**。
- **例外**：`HostValidationService.recoverPending` 的失败只写警告日志，不阻止启动
  （`internal/app/host_validation_service.go:135`）。与工作流恢复的"失败即停止启动"语义不一致。
- **顺序副作用（P2-M2 已解除）**：`TaskWorkflowService` 的恢复先于
  `TaskLaunchService`，所以 `recoverUnfinished` 会先把 `running` 执行记录收敛为
  `blocked`，之后 `RecoverStepOccupancy` 再判断"任务是否仍有**有效执行权**"。
  M1 当时记录的问题是 `workflowRunActivePredicate`
  （`internal/storage/sqlite/workflow_runs.go`）把 `blocked` 也算作未结束，导致步骤占用
  永远得不到收敛（§5.1 B1）。P2-M2 的修复**没有改动该谓词**（它仍正确表达"未结束记录"，
  并被 `GetActiveWorkflowRun` / 部分唯一索引复用），而是把步骤占用收敛的判据改为
  `domain.workflowRunOwnsExecution`（`running` + 租约有效）。两个概念从此分开：
  "未结束记录"用于并发保护，"有效执行权"用于抢占判定。

### 1.2 状态分层与真相来源

| 层次 | 状态字段 | 取值来源 | 唯一真相来源 |
|---|---|---|---|
| Task | `tasks.status` / `final_result` / `cancel_requested` | `domain.TaskStatus`（`internal/domain/task.go:26`） | 数据库 `tasks` 行 |
| WorkflowRun | `workflow_runs.status` / `stage` / 游标 / `worker_id` + `lease_expires_at` / `revision` | `domain.WorkflowRunStatus`（`internal/domain/workflow_run.go:20`） | 数据库 `workflow_runs` 行 |
| Step | `steps.status` / `current_attempt_id` / `failure_summary` | `domain.StepStatus`（`internal/domain/step.go:97`） | 数据库 `steps` 行 |
| Attempt | `attempts.status` / `git_before_json` / `git_after_json` / `verification_id` | `domain.AttemptStatus`（`internal/domain/attempt.go`） | 数据库 `attempts` 行 |
| Session | `sessions.status` / `run_status` / `completeness` / `export_status` / `archive_status` / `archive_path` | `internal/domain/session.go` | 数据库 `sessions` 行 + 本机文件 |
| ValidationRun | `validation_runs.status` / `verdict` / `review_status` | `internal/domain/validation.go:20` | 数据库 `validation_runs` 行 |
| 工作区初始化 | `task_workspace_initializations.status` / `stage` / `commit_sha` | `internal/domain/task_workspace.go` | 数据库行 |
| 代码获取（Attempt 级） | `code_fetches.status` / `stage` / `commit_sha` | `internal/domain/code_fetch.go:73` | 数据库行 |
| 代码保存 | `code_saves.commit_status` / `push_status` / `merge_status` / `persist_status` / `recovery_status` | `internal/domain/code_save.go` | 数据库行 |
| 容器 | `containers.state` / `docker_state` / `docker_id` | `internal/domain/container.go:58` | 数据库行 + 远程 Docker 观测 |

**领域层最终判定函数**（规则只在这些函数中定义一次，调用方不得复制）：

| 判定 | 函数 | 位置 |
|---|---|---|
| 启动阶段选择 | `domain.DecideTaskLaunchNext` | `internal/domain/task_launch.go:141` |
| 工作区是否需初始化 | `domain.DecideTaskWorkspaceNext` | `internal/domain/task_workspace.go:436` |
| 工作区中断收敛 | `domain.DecideTaskWorkspaceRecovery` | `internal/domain/task_workspace.go:486` |
| 步骤占用中断收敛 | `domain.DecideStepOccupancyRecovery` | `internal/domain/task_launch.go:197` |
| 计划生成现场恢复 | `domain.DecidePlanGenerationRecovery` | `internal/domain/plan_generation.go:303` |
| 取消现场恢复 | `domain.DecideCancellationRecovery` | `internal/domain/plan_generation.go:333` |
| 下一步可执行步骤 | `domain.NextStepToExecute` | `internal/domain/git_state.go:137` |
| 验证结论 | `domain.DecideValidation` | `internal/domain/validation_decision.go` |
| 步骤收口 | `domain.DecideStepCloseout` | `internal/domain/step_verification.go` |
| 修复循环停止 | `domain.DecideRepairLoop` / `DecideRepairCloseout` | `internal/domain/retry.go`、`step_verification.go` |
| 代码保存恢复 | `domain.DecideCodeSaveRecovery` | `internal/domain/code_save_recovery.go:67` |
| 任务收尾 | `domain.DecideWorkflowCloseout` | `internal/domain/workflow_run.go:717` |
| 容器生命周期 | `domain.DecideContainerLifecycle` | `internal/domain/container_lifecycle.go:92` |

**哪些数据只是事件 / 日志 / 提示，不是恢复依据**：

- `events` 表（`domain.EventType*`）：时间线，写入失败只记日志、不回滚状态。
  恢复判定**从不**读取事件。
- 进程内登记表：`keyedTaskMutex`（`task_workflow_exec.go`）、`stepExecutionRegistry`
  （`step_executor.go`）、`cancelRegistry`：只做第一道防线，重启后全部丢失。
- `code_saves.persist_error` / `error_summary`、`attempts.failure_summary`、
  `validation_feedbacks` 摘要：面向人的诊断信息，不参与状态迁移。
- `logs`（`sessions.stdout.log` / `stderr.log`）：仅诊断，不参与判定。

### 1.3 并发与重复恢复的保护

| 机制 | 位置 | 保护范围 |
|---|---|---|
| `UpdateTaskIfStatus` | `internal/storage/sqlite/tasks.go` | 任务状态写入带状态前置条件 |
| `UpdateStepIfStatus` | `internal/storage/sqlite/steps.go:89` | 步骤状态写入带状态前置条件 |
| `UpdateAttemptIfStatus` | `internal/storage/sqlite/attempts.go` | 尝试终态写入带状态前置条件 |
| `UpdateWorkflowRun` 的 `revision` 条件 | `internal/storage/sqlite/workflow_runs.go` | 执行记录乐观并发 |
| `idx_workflow_runs_active_task` 部分唯一索引 | `migrations/0018_workflow_runs.sql` | 同一任务最多一条未结束执行 |
| `BeginStepExecution` 单事务 | `internal/storage/sqlite/step_execution.go:27` | 步骤占用原子性 |
| `BeginValidationRun` 活跃验证查询 | `internal/storage/sqlite/validations.go:141` | 同一 Attempt 最多一条 pending/running 验证 |
| `writeRecoveredTask` 冲突后重读 | `internal/app/task_workflow_run.go:733` | 恢复幂等（状态已等于目标值视为已完成） |
| 进程内 `tryLock` / `acquire` | 见上 | 只防同进程重复，**不是**一致性保证 |

`tests/integration/local/workflow/recovery_test.go` 的 `TestConcurrentRecoveryConvergesOnce`
用两个共享同一数据库的实例同时启动，验证"只收敛一次、只写一条恢复事件"。

---

## 2. 恢复边界矩阵

每阶段字段固定为：阶段与入口 / 前置条件 / 副作用 / 执行前记录 / 成功证据 / 失败与中断窗口 /
当前恢复入口 / 当前收敛行为 / 重试安全性 / 证据与测试 / 剩余缺口。

### 2.1 任务创建与启动前置检查

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `app.TaskService.CreateTaskFromAPI`（`internal/app/task_management.go:150`）；`app.TaskLaunchService.Launch`（`internal/app/task_launch_run.go:29`），HTTP `POST /api/v1/tasks/{id}/start`，CLI `agentflow task start`（`internal/cli/task.go:365`） |
| 前置条件 | 任务已持久化且未删除；`domain.CheckTaskLaunchStart`（`internal/domain/task_launch.go:74`）要求状态 ∈ {queued, preparing, planned, retrying, paused, recovery_required}、无未结束执行记录、无 `recovery_required` 步骤、已绑定 host/环境/规划角色/开发角色 |
| 副作用 | 无写副作用；`loadLaunchInput` 会调用 `ContainerService.InspectContainer`（远程只读） |
| 执行前记录 | 无新增记录；读取 `steps`、`GetActiveWorkflowRun`、容器可用性、`task_workspace_initializations` |
| 成功证据 | 无（本阶段只产出判定） |
| 失败与中断窗口 | 读取失败 → 原样返回错误，状态不变；容器核验失败 → `ErrTaskLaunchPrerequisite`，**不会**因此新建容器（`task_launch_run.go:196-205`） |
| 当前恢复入口 | 不需要（无副作用） |
| 当前收敛行为 | 无状态变化 |
| 重试安全性 | 只读，可安全重复 |
| 证据与测试 | `tests/unit/app/task_launch_test.go`（`TestLaunchRejectsUnknownTask`、`TestLaunchRejectsActiveWorkflowRun`、`TestLaunchRejectsNegativeTimeout`）、`tests/unit/domain/task_launch_test.go` |
| 剩余缺口 | `containerUsable` 只对 `Detached` / `Removed` / 非 running 返回 false，`InspectContainer` 出错时直接上抛——"容器已删除"与"节点不可达"在**调用方**层面仅靠错误文本区分，没有稳定错误类别 |

### 2.2 容器创建、关联与可用性核验

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `ExecutionEnvironmentService.PrepareForTask`（`internal/app/execenv_task.go:38`）→ `Prepare`（`internal/app/execenv_prepare.go:41`，四阶段）→ `ContainerService.CreateContainer`（`internal/app/containers.go:74`）→ `createLocked`（`internal/app/containers.go:143`） |
| 前置条件 | 任务绑定 host + 语言环境；节点预检为 `normal`（`requireHost`）；环境版本可解析 |
| 副作用 | 镜像构建/拉取、`docker create`、`docker start`、远程登记清单写入 |
| 执行前记录 | `s.store.CreateContainer(container)` 在 `docker create` **之前**写入，`state=creating`，并携带 `task_id` / `attempt_id`（`containers.go:155`） |
| 成功证据 | `containers.state=running` + `docker_id` 非空 + `docker.VerifyOwnership` 通过 + 远程清单同步成功；随后 `attachContainerToTask` 把容器 ID 写回 `tasks.container_id` |
| 失败与中断窗口 | (a) 记录已写、`docker create` 失败 → `recoverByName` 按名字核验，失败则 `markUnknown`；(b) 启动失败/核验失败 → `markUnknown`；(c) 容器已运行但 `syncRegistry` 失败 → 返回 detail + error，`prepareContainer` 降级为阶段说明、继续使用该容器；(d) 容器已运行但 `attachContainerToTask` 条件更新冲突 → 写 `task_recovery_required` 事件并返回 `ErrStateUncertain`，容器成为"已登记但未关联任务"的孤儿 |
| 当前恢复入口 | **无自动恢复**。`TaskLaunchService.loadLaunchInput` 只读 `tasks.container_id`，孤儿容器永远不会被重新关联 |
| 当前收敛行为 | 孤儿容器保留在 `containers` 表中（`task_id` 有值、`state=running`），任务 `container_id` 为空；下次 `Launch` 会**再创建一个容器** |
| 重试安全性 | 创建容器**不可**安全重放（会产生重复容器）；当前依赖"先判定可用容器"避免重复，但孤儿场景绕过了该保护 |
| 证据与测试 | `tests/integration/local/workflow/launch_test.go`（`TestLaunchDoesNotCreateSecondContainer`、`TestLaunchPersistsContainerAndPlanAcrossStages`）；`recoverByName` / `markUnknown` **无测试** |
| 剩余缺口 | 1) 孤儿容器无核验/复用入口；2) `ContainerStateUnknown` 无自动收敛路径（`domain` 允许 unknown→created/running/stopped，但没有任何调用方推进）；3) 不存在"按任务查找孤儿容器"的存储查询 |

### 2.3 工作区初始化与代码获取

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `TaskWorkspaceService.InitializeTaskWorkspace`（`internal/app/task_workspace.go:203`），由 `TaskLaunchService.initializeWorkspace`（`internal/app/task_launch_run.go:290`）调用；Attempt 级为 `CodeInitializeService.Initialize`（`internal/app/code_initialize.go:208`），HTTP `POST .../initialize-code` |
| 前置条件 | 容器受管理、未 detached、运行中（`TaskWorkspaceService.requireContainer`）；节点预检通过；任务级不要求 attempt 绑定 |
| 副作用 | 容器内 `git clone`、基础分支识别、任务分支创建/检出 |
| 执行前记录 | `beginRecord` 先写 `pending` 记录再 `MarkRunning(probe)` 落库（`task_workspace.go:488-524`），记录以 `task_id + container_id` 为幂等边界 |
| 成功证据 | `status ∈ {succeeded, skipped}` 且 `commit_sha` 非空（`TestTaskWorkspaceSucceededRequiresCommit`） |
| 失败与中断窗口 | 进程在 clone 期间退出 → 记录停在 `pending`/`running`；结果未落库 → `ErrTaskWorkspaceNotPersisted`（`task_workspace.go:350`）；目标目录非空 → 拒绝并记录 `stage`/`error_code` |
| 当前恢复入口 | `TaskWorkspaceService.RecoverInterrupted`（`internal/app/task_workspace_recovery.go:25`），真实调用者：`TaskLaunchService.Start` → `recoverWorkspace`（`internal/app/task_launch.go:379`） |
| 当前收敛行为 | `pending`/`running` → `uncertain` + 固定摘要，**不重放**任何代码获取；随后 `DecideTaskWorkspaceNext` 对 `uncertain` 返回错误，`Launch` 停在 workspace 阶段 |
| 重试安全性 | 重放 clone 会覆盖可能已就绪的工作区，**禁止**自动重试（已正确实现） |
| 证据与测试 | `tests/integration/local/workflow/launch_test.go`（`TestWorkspaceInterruptedConvergesToUncertain`、`TestWorkspaceInitializationPersistsAcrossRestart`、`TestLaunchStopsAtWorkspaceStageWhenUncertain`）、`launch_chain_test.go`（`TestLaunchChainRejectsUncertainWorkspaceWithoutReplay`）、`tests/unit/domain/task_workspace_test.go`（`TestDecideTaskWorkspaceRecovery`）、`tests/unit/app/task_workspace_test.go`（`TestRecoverInterruptedConvergesToUncertain`、`TestRecoverInterruptedKeepsTerminalRecords`） |
| 剩余缺口 | **P2-M5 更新**：1) Attempt 级 `code_fetches` 已有收敛路径（`app.CodeFetchRecoveryService`，把无执行者的 `pending`/`running` 收敛为 `uncertain`）与只读核验入口（`code fetch-verify --record <id>`，`app.CodeFetchVerifyService`）；2) `0023_code_fetch_active_unique.sql` 为 `code_fetches` 加了"同一 Attempt 最多一条活动记录"的部分唯一索引；3) 尝试级初始化新增 `requireTaskWorkspaceReady` 前置门控，避免与任务级初始化争抢同一容器内目录；4) 任务级 `uncertain` 仍**没有**只读核验入口（`code fetch-verify` 只覆盖 Attempt 级），任务级只能人工进容器确认——这是本轮刻意保留的边界 |

### 2.4 Planner 调用与计划持久化

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `TaskWorkflowService.GeneratePlan`（`internal/app/task_workflow_plan.go:79`）；HTTP `POST /api/v1/tasks/{id}/plan`；CLI `agentflow task plan`；由 `TaskLaunchService.planTask` 调用 |
| 前置条件 | `domain.CheckPlanGenerationStart`：状态可迁移到 `planning`、容器存在、**没有已持久化步骤**、已配置规划角色；无未结束执行记录 |
| 副作用 | 容器内 Pi Planner 调用（一次独立 Session） |
| 执行前记录 | `beginPlanning` 用 `UpdateTaskIfStatus` 把任务推进到 `planning` 并落库（跨进程闸门）；`callPlanner` 在进程启动前 `SessionService.Start` 写会话记录（`sessions.go:88`） |
| 成功证据 | 步骤行 + 任务 `planned` + `plan_created` 事件在**同一事务**写入（`persistPlan`，`task_workflow_plan.go:499`） |
| 失败与中断窗口 | 进程在 Planner 调用期间退出 → 任务停在 `planning`；事务未提交 → 没有步骤；事务已提交 → 有步骤且任务 `planned` |
| 当前恢复入口 | `TaskWorkflowService.recoverPlanGeneration`（`internal/app/task_workflow_run.go:599`），真实调用者：`TaskWorkflowService.Start` |
| 当前收敛行为 | 有步骤 → `planned`；无步骤 → `recovery_required` + 固定摘要（`planRecoverySummary`）；**绝不重放 Planner** |
| 重试安全性 | 不重放；用户可在 `recovery_required` 上显式重新 `Launch`（`CheckTaskLaunchStart` 允许） |
| 证据与测试 | `tests/integration/local/workflow/recovery_test.go`（`TestRecoverPlanGenerationOnRestart`、`TestRecoverPlanGenerationFailureBlocksStartup`、`TestPlanGenerationPersistsAcrossRestart`、`TestConcurrentGeneratePlanRejectsSecond`）、`tests/unit/domain/plan_generation_test.go` |
| 剩余缺口 | 1) Planner 会话若停在 `running`（见 2.6）不会被收敛，`sessionsPersisted` 会因此判 false；2) `TestPlanGenerationInterruptedByShutdown` 已知约 40% 概率失败（`.steps/ROADMAP.md` 已记录），"关闭时中断"这一窗口的测试证据不可靠 |

### 2.5 WorkflowRun 创建与启动

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `TaskWorkflowService.StartWorkflow`（`internal/app/task_workflow_run.go:124`）、`ResumeWorkflow`（`:206`）；HTTP `POST .../execute-workflow`、`POST .../resume-workflow` |
| 前置条件 | `domain.CheckWorkflowStart`（`internal/domain/workflow_run.go:550`）：任务未删除、未请求取消、`AllowsStepExecution`、有容器、有计划、序号唯一、无 `recovery_required`/占用步骤、步骤类型 ∈ {plan, develop}、每步有必需检查；容器核验 + 节点预检 |
| 副作用 | 启动后台 goroutine（`launch` → `drive`），随后产生 Pi 调用 |
| 执行前记录 | `CreateWorkflowRun`（`pending`）→ `beginRunRecord` 调 `run.Start(owner, lease, now)` 写 `running` + `worker_id` + `lease_expires_at` 并落库；`UpdateWorkflowRun` 以 `revision` 为条件 |
| 成功证据 | `workflow_runs.status=running` 且租约有效；`workflow_started` 事件 |
| 失败与中断窗口 | `beginRunRecord` 写入失败 → 记录停在 `pending`，接口返回错误且**不启动**后台推进；进程在推进中退出 → 记录停在 `running` + 租约 |
| 当前恢复入口 | `recoverUnfinished`（`:829`）→ `recoverRun`（`:849`） |
| 当前收敛行为 | `cancel_requested` → `cancelled`；`running` 且租约过期 → `blocked` / `worker_interrupted`；`pending` 且超过租约窗口 → `blocked` / `not_started`；租约有效 → 不动；`blocked` / `recovery_required` → 保持原状 |
| 重试安全性 | 不自动重启；只能由用户显式 `ResumeWorkflow`（要求 `run.Status.CanStart()`、无取消请求、`plan_digest` 一致、`CheckWorkflowStart` 通过） |
| 证据与测试 | `tests/integration/local/workflow/recovery_test.go`（`TestRecoverUnfinishedRunOnRestart`、`TestRecoverUnfinishedRunFailureBlocksStartup`）、`tests/unit/app/task_workflow_test.go`（`TestWorkflowRecoveryOnServerRestart`、`TestWorkflowRecoveryKeepsLiveLease`、`TestWorkflowRecoveryCancelsRequested`、`TestWorkflowResumeSkipsCompletedSteps`、`TestWorkflowResumeRejectsPlanChange`）、`tests/unit/domain/workflow_run_test.go`（`TestWorkflowRunLeaseExpiry`） |
| 剩余缺口 | 1) 租约只在启动时判定一次，进程内**没有续租/看门狗**（`grep time.NewTicker` 在 `internal/app` 无命中）：长时间运行的步骤（默认超时 30 分钟）会超过 10 分钟租约（`defaultWorkflowRunLease`，`task_workflow.go:51`），使"另一进程"在租约过期后可以抢占仍在推进的记录；2) 租约过期收敛依赖"重启"，没有周期性收敛 |

### 2.6 Developer 执行、Attempt 与 Session

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `StepExecutor.Execute`（`internal/app/step_executor.go:284`），由 `StepWorkflowService.executeRepairRound`（`internal/app/step_workflow_repair.go:197`）与 `TaskWorkflowService.runStep` 间接调用；单步接口 `POST .../steps/{id}/execute` |
| 前置条件 | `domain.CheckStepExecutionStart`：任务未取消、`AllowsStepExecution`、步骤 `CanStartExecution`、`current_attempt_id` 为空、步骤类型有 Agent 角色、已配置角色；前序步骤全部 completed/skipped（`domain.CheckStepPrerequisites`）；容器受管理运行中；节点预检通过；角色启用且配置模型 |
| 副作用 | 容器内 Pi 进程（一次独立 Session）；Git 状态只读采集 |
| 执行前记录 | `BeginStepExecution` 单事务：写 `attempts`（`running`，序号在事务内分配）→ 条件更新 `steps`（`running` + `current_attempt_id`）→ 条件更新 `tasks`（`developing` + 游标）（`internal/storage/sqlite/step_execution.go:73-163`）；随后 `SessionService.Start` 先写会话（`starting`→`running`）再启动进程 |
| 成功证据 | `attempts.status` 终态 + `sessions.status`/`run_status`/`archive_status` + `steps.status ∈ {completed, verifying}`；`attempt.session_ids` 含本次会话 |
| 失败与中断窗口 | 进程在 Pi 调用期间退出 → `attempts.status=running`、`sessions.status=running`、`steps.status ∈ {running, developing}`、`tasks.status=developing`；收尾写入失败 → `Finalized=false` + `FinalizationErrors`（不伪造成功） |
| 当前恢复入口 | 仅 `TaskLaunchService.RecoverStepOccupancy`（`internal/app/task_launch_recovery.go:24`）收敛**步骤**；Attempt 与 Session **没有**任何收敛入口 |
| 当前收敛行为 | 步骤 `running`/`developing`/`verifying` 且任务无未结束执行 → `recovery_required` + 释放 `current_attempt_id` + 固定摘要；Attempt / Session 保持 `running` |
| 重试安全性 | 步骤收敛为 `recovery_required` 后 `CanStartExecution()` 为 true，用户可显式重跑；但在未知现场上重跑会覆盖真实执行结果，属于"需要人工核验后再决定" |
| 证据与测试 | `tests/integration/local/workflow/launch_test.go`（`TestRecoverStepOccupancyPersistsRecoveryRequired`、`TestRecoverStepOccupancyKeepsActiveRunOccupancy`）、`tests/unit/app/task_launch_test.go`（`TestRecoverStepOccupancyConvergesIdleOccupancy`、`TestRecoverStepOccupancySkipsActiveRun`）、`tests/unit/app/step_executor_test.go`（`TestExecuteStepFinalizationFailureIsReported`、`TestExecutorStopCancelsInFlightRun`、`TestExecuteStepLateResultDoesNotOverrideTerminal`） |
| 剩余缺口 | 1) **`attempts` 停在 `running` 无收敛路径**；2) **`sessions` 停在 `running`/`starting` 无收敛路径**，`SessionService.Fail`（`internal/app/sessions.go:234`）在生产代码中**没有任何调用者**（`grep` 仅命中定义）；3) 恢复时无法区分"Agent 从未启动"与"Agent 已执行但结果未落库"，因为 `sessions` 不记录进程启动证据；4) `recovery_required` 步骤没有任何只读核验入口 |

### 2.7 确定性验证、Validator 评估与结果持久化

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `ValidationService.Validate` → `runValidation`（`internal/app/validation_run.go:36`），由 `StepWorkflowService.validateAndCloseout`（`internal/app/step_workflow_run.go:71`）调用；接口 `POST .../attempts/{id}/validate` |
| 前置条件 | 任务/步骤/尝试归属一致；尝试状态允许；**该 Attempt 无 pending/running 验证**（`internal/storage/sqlite/validations.go:141`）；容器受管理运行中；节点预检通过 |
| 副作用 | 容器内确定性检查命令；语义审查时一次独立 Validator Session（`reviewSemantics`，`validation_run.go:111`） |
| 执行前记录 | `BeginValidationRun` 在同一事务内校验归属、检查活跃验证、构造并**直接以 `running` 写入** `validation_runs`（`validations.go:174`），同时条件更新 `steps`/`tasks` |
| 成功证据 | `validation_runs.status` 终态 + `verdict` + 逐项 `validation_check_results` + `attempts.verification_id`；`Persisted=true` |
| 失败与中断窗口 | 逐项检查结果**逐项落盘**（`AppendValidationCheckResult`），最后一项失败不丢前面的证据；结论写入失败 → `Persisted=false` + `ErrValidationNotPersisted`（`validation_run.go` 的 `finalizeValidation`）；进程在验证期间退出 → `validation_runs.status=running` 且无终态 |
| 当前恢复入口 | **无**。没有任何启动恢复处理 `validation_runs` |
| 当前收敛行为 | 无收敛。残留 `running` 验证会让该 Attempt 的**后续验证永久返回 409**（活跃验证查询），`domain.DecideValidation` 对 `running` 也只返回"不能判定通过" |
| 重试安全性 | 对该 Attempt 不可重试；只能通过新建 Attempt 绕过（但旧记录仍残留 `running`） |
| 证据与测试 | `tests/unit/app/validation_test.go`、`tests/unit/app/validation_review_test.go`（`TestReviewPersistenceFailureIsNotSuccess`）、`tests/unit/domain/validation*`、`tests/integration/local/validation/`；**没有"验证期间进程退出"的用例** |
| 剩余缺口 | 1) `validation_runs` 停在 `pending`/`running` 无收敛路径；2) `validation_check_results` 与流程状态无法区分"检查未执行"与"检查已执行但结论未落库"；3) Validator Session 停在 `running` 同样无收敛路径 |

### 2.8 验证失败后的修复与重验

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `StepWorkflowService.ExecuteAndValidate`（`internal/app/step_workflow_repair.go:40`）→ `runRepairLoop`（`:105`）；接口 `POST .../steps/{id}/execute-and-validate` |
| 前置条件 | `CheckStepExecutionStart`；前序步骤完成；`requireStepVerificationSpec`（至少一项启用必需检查）；`domain.DecideRepairStart` 允许开始（P2-M3）；`ensureRepairCycle` 拿到周期标识；`initialFeedback` 按当前周期精确读取成功 |
| 副作用 | 每轮一次 Developer 调用 + 一次验证 |
| 执行前记录 | 每轮新建 Attempt（`BeginStepExecution`）→ 新 Session（`SessionService.Start`）→ 新 Validation Run（`BeginValidationRun`）；每轮写 `repair.round` 事件 |
| 成功证据 | 轮次决策由 `domain.DecideRepairLoop` 依据持久化事实判定；终态由 `domain.DecideRepairCloseout` 计算 |
| 失败与中断窗口 | 轮次之间进程退出 → 步骤停在 `failed` / `recovery_required` / `retry_wait`；某轮 Attempt 停在 `running` → 同 2.6 |
| 当前恢复入口 | 无（重启后不续跑修复循环） |
| 当前收敛行为 | 保持现场；用户可显式再次调用（`repairStopMapping` 决定工作流状态：`blocked` 或 `recovery_required`） |
| 重试安全性 | **P2-M3 更新**：预算来自 `repair_cycles` + `attempts.budget_scope*`，同一周期跨重启保持一致；上一周期结束后必须由用户显式调用 `open-repair-cycle` 才能获得新预算；自动重试仍被禁止 |
| 证据与测试 | `tests/unit/app/step_workflow_repair_test.go`、`tests/unit/app/task_workflow_repair_test.go`、`tests/unit/domain/repair_loop_test.go`、`tests/integration/local/workflow/e2e_repair_test.go`、`e2e_repair_limit_test.go`、`delivery_chain_test.go`（`TestDeliveryChainStopsAtRepairLimit`） |
| 剩余缺口 | 已由 P2-M3 关闭：1) `persistFeedback` 返回 `false` 时由 `RepairClassFeedbackUnavailable` 明确停止，首轮依据改为按当前周期内最近一次验证精确读取；2) 已消耗轮次改为持久化事实（`repair_cycles` + `attempts.budget_scope*`）。仍需注意：`interrupted` / `unknown` / `recovery_required` 尝试一律阻止重放与开启新周期，只能人工核验 |

### 2.9 commit

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `codesaver.Session.commit`（`internal/codesaver/commit.go:24`）→ `runCommit`（`:160`）；由 `ContainerService.SaveCode`（`internal/app/code_save.go:57`）→ `TaskWorkflowService.saveCode`（`internal/app/task_workflow_exec.go:771`）触发 |
| 前置条件 | 容器受管理、未 detached、`docker_id` 与记录一致；容器内 Git 可用；工作区是仓库根目录；无未解决冲突；`record.PersistStatus` 可信 |
| 副作用 | `git add --all`、`git commit --no-verify` |
| 执行前记录 | `Prepare` 在**任何 Git 操作之前**写入 `code_saves` 记录（`saver.go:createRecord`，`status` 全为 `pending`，`persist_status` 由 `MarkPersisted` 决定）；`runCommit` 在 `git commit` **之前**写 `baseline_commit_sha` 并落库（`commit.go:276`） |
| 成功证据 | 提交后重新读取 HEAD；`commit_status=succeeded` + `commit_sha` + `persist_status=persisted` |
| 失败与中断窗口 | (a) 提交成功但写入失败 → `MarkPersistFailure` → `persist_status=unconfirmed`、DB 停在 `commit_status=pending`，错误类别 `CodeStateUnconfirmed`（`saver.go:persist`）；(b) 提交后 HEAD 不可读 → `commit_status=unknown` + `CodeStateUncertain`；(c) 进程在提交后崩溃 → DB 停在 `pending`，基线可用 |
| 当前恢复入口 | **用户触发**：`agentflow code verify --record <id>`（`internal/cli/code.go:283`）→ `ContainerService.VerifyCodeSave`（`internal/app/code_save_verify.go:50`）→ `codesaver.Saver.Observe` → `domain.DecideCodeSaveRecovery`。**没有任何自动恢复调用它**（`grep VerifyCodeSave` 只命中 CLI 与定义） |
| 当前收敛行为 | **P2-M4 更新**：核验结论连同已确认阶段状态条件写回（`UpdateCodeSaveRecoveryResult` + `ApplyRecovery`）：`confirmed_applied` 才推进阶段状态，`confirmed_not_applied` / `unverifiable` 只写结论。`SafeToRetry` 只在"确认未发生"且记录可信时为 true；续跑资格统一由 `domain.DecideCodeSaveResume` 判定（`proceed` / `verify` / `blocked`） |
| 重试安全性 | **P2-M4 更新**：只有 `confirmed_not_applied` 可重试；`unconfirmed` 记录禁止仅凭 `pending` 重放；`TaskWorkflowService.checkCodeSaveRecovery` 在启动与恢复之前拒绝存在未决副作用的请求 |
| 证据与测试 | `tests/integration/local/codesaver/recovery_test.go`（11 个用例，真实 SQLite + 真实 Git）、`tests/integration/local/codesaver/codesaver_test.go`、`tests/unit/source/codesaver/codesaver_test.go`、`tests/unit/domain/code_save_recovery_test.go`、`tests/unit/domain/code_save_resume_test.go`、`tests/unit/store/code_saves_test.go` |
| 剩余缺口 | **P2-M4 更新**：1) 核验仍是**人工**动作，收尾进入 `recovery_required` 后没有"核验并继续"的自动编排入口（恢复入口门控与启动扫描已落地）；2) `VerifyCodeSave` 需要容器仍然存在且可达，容器已删除时只能得到 `unverifiable`；3) 已用"条件写入 + 冲突重读"覆盖"核验期间记录被推进"，"核验过程中再次退出"由只读幂等性覆盖 |

### 2.10 push

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `codesaver.Session.push` / `pushCommit`（`internal/codesaver/push.go:41`） |
| 前置条件 | `commit_status ∈ {succeeded, no_changes}`；`persist_status` 可信 |
| 副作用 | `git push --porcelain <remote> <branch>:refs/heads/<branch>`（**无** `--force`） |
| 执行前记录 | `push_status=pending` 已在 `Prepare` 阶段落库；推送前 `Stage="push"` |
| 成功证据 | 推送后重新读取远程引用并确认等于本地 commit（`push.go:73-88`）；`push_status=succeeded` + `commit_sha` |
| 失败与中断窗口 | 超时/中断/状态不确定 → 先核验远程引用：等于 commit → succeeded；不等于且非空 → `conflict`；读不到 → `unknown`（`resolvePushFailure`，`push.go:95`） |
| 当前恢复入口 | 同 2.9（`code verify` + `DecideCodeSaveRecovery` 的 `decidePushRecovery`） |
| 当前收敛行为 | 远程等于预期 commit → `confirmed_applied`；远程不存在 → `confirmed_not_applied` + `SafeToRetry`；远程指向其他 commit → `unverifiable`（禁止强推） |
| 重试安全性 | `unknown` / `conflict` 一律禁止自动重放；`conflict` 明确拒绝 force push |
| 证据与测试 | `tests/unit/source/codesaver/codesaver_test.go`（`TestPushTimeoutButRemoteAccepted`、`TestPushUnknownWhenRemoteUnverifiable`、`TestPushRemoteMismatchIsConflict`、`TestPushNonFastForwardIsConflict`）、`tests/integration/local/codesaver/codesaver_test.go`（`TestObserveDetectsRemoteMismatch`、`TestObserveReportsUnreachableRemote`）、`tests/integration/local/workflow/e2e_indeterminate_test.go`（`TestE2EIndeterminateCodeSavePushUnknown` 断言 `gitPushCount()==1`） |
| 剩余缺口 | 1) `push_status=unknown` 之后没有"再次核验"的自动路径；2) 远程分支被他人推进（`conflict`）时的解决流程未定义，只能人工处理 |

### 2.11 可选 merge

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `codesaver.Session.merge`（`internal/codesaver/merge.go`）；由 `TaskConfig.MergeAfterPush` 决定是否请求 |
| 前置条件 | `checkMergePreconditions`（`merge.go:86`）：`commit_status ∈ {succeeded, no_changes}`、`push_status=succeeded`、基础分支名可识别、无冲突 |
| 副作用 | 拉取并合并基础分支、推送合并结果 |
| 执行前记录 | `recordBaselineBaseCommit` 在合并**之前**写 `baseline_base_commit_sha` 并落库（`merge.go:151`、`:385`） |
| 成功证据 | `merge_status=succeeded` + `merge_sha` + `persist_status=persisted` |
| 失败与中断窗口 | 合并冲突 → `merge_status=conflict`；结果不可读 → `unknown`；写入失败 → `unconfirmed` |
| 当前恢复入口 | 同 2.9（`decideMergeRecovery`） |
| 当前收敛行为 | 远程基础分支等于基线 → `confirmed_not_applied` + `SafeToRetry`；等于预期合并提交 → `confirmed_applied`；基础分支变了但无法唯一确定合并提交 → `unverifiable` |
| 重试安全性 | 只有 `confirmed_not_applied` 可重试；冲突时**不得**自动选 ours/theirs |
| 证据与测试 | `tests/integration/local/codesaver/codesaver_test.go`（`TestObserveConfirmsMergeApplied`、`TestMergeConflictKeepsInspectableState`）、`tests/unit/domain/code_save_recovery_test.go`（`TestDecideCodeSaveRecoveryForMerge`、`TestDecideCodeSaveRecoveryMergeParentMismatch`）、`tests/unit/source/codesaver/codesaver_test.go`（`TestMergeRequiresSuccessfulPush`、`TestMergeNotRequestedByDefault`） |
| 剩余缺口 | 1) `merge_after_push` 默认关闭，生产链路中 merge 恢复证据只有单测与本地集成，**没有真实远程用例**；2) 合并成功后若写入失败，恢复依赖基线 + 合并父提交推断，推断失败即 `unverifiable`（无进一步自动手段） |

### 2.12 会话归档

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `SessionService.Finish`（`internal/app/sessions.go:186`）→ `applyArchive`（`:272`）；归档结论由 `archiveOf`（`:290`）依据 `agent.RunResult.Session.JSONLPath` 生成；收尾核验为 `TaskWorkflowService.sessionsPersisted`（`internal/app/task_workflow_exec.go:821`） |
| 前置条件 | 会话记录已存在且非终态；原始 JSONL 已由 `pi.ContainerRunner` 从容器回传到本机（`internal/agent/pi/container_runner.go:414` `copySession`） |
| 副作用 | 容器内 `pi --export`（HTML）、SFTP 回传原始 JSONL |
| 执行前记录 | `SessionService.Start` 在进程启动前写 `sessions` 行（`starting`→`running`）；`Finish` 写 `run_status` / `completeness` / `export_status` / `archive_status` / `archive_path` |
| 成功证据 | `completeness ∈ {complete, export_failed}` 且 `archive_status=succeeded` 且 `archive_path` 非空（`sessionArchived`，`task_workflow_exec.go:847`） |
| 失败与中断窗口 | 进程在回传前退出 → 会话停在 `running`，`archive_status` 为空；回传 0 字节 → `archive_status=failed` + 错误摘要；HTML 导出失败 → `completeness=export_failed`（**不算**归档失败）；JSONL 不完整 → `completeness=partial` + 仍写 `JSONLPath` 与 `archive_status=succeeded`（记录层语义不一致，见缺口） |
| 当前恢复入口 | **无**。没有重新归档或补导出的入口 |
| 当前收敛行为 | `sessionsPersisted` 返回 false → `DecideWorkflowCloseout` 返回 `recovery_required` / `session_not_persisted`；查询失败同样返回 false（**不**当作"没有会话"） |
| 重试安全性 | 归档本身可重做（只读容器内文件），但当前没有入口 |
| 证据与测试 | `tests/integration/local/workflow/delivery_chain_test.go`（`TestDeliveryChainStopsWhenSessionArchiveIncomplete`、`TestDeliveryChainStopsWhenSessionQueryFails`）、`tests/unit/app/task_workflow_test.go`（`TestWorkflowCloseoutRequiresSessionArchive`）、`tests/integration/local/session/` |
| 剩余缺口 | 1) 会话停在 `running` 无收敛路径（同 2.6）；2) `partial` 会话仍被标记 `archive_status=succeeded`，与"归档成功"的语义冲突；3) 没有"重新归档"入口，`recovery_required` / `session_not_persisted` 之后任务无出路；4) 归档结论不校验文件可读性，只依据路径非空 |

### 2.13 Task 与 WorkflowRun 最终收尾

| 字段 | 内容 |
|---|---|
| 阶段与入口 | `TaskWorkflowService.finalize`（`internal/app/task_workflow_exec.go:405`）→ `saveCode`（`:771`）→ `sessionsPersisted`（`:821`）→ `domain.DecideWorkflowCloseout` → `applyCloseout`（`:449`） |
| 前置条件 | `advance` 中 `domain.NextStepToExecute` 返回 `ok=false`（没有可推进步骤）；`allExecutableStepsCompleted` 为真 |
| 副作用 | 代码保存（commit/push/可选 merge）；无其他写外部操作 |
| 执行前记录 | `SetStage(finalize)` 落库；`saveCode` 之前 `SetStage(code_save)` 落库（写入失败即不执行远程操作，`task_workflow_exec.go:saveCode`） |
| 成功证据 | `tasks.status=completed` + `final_result=succeeded` + `workflow_runs.status=completed`（两者分别写入） |
| 失败与中断窗口 | (a) `completeTask` 失败 → `recovery_required` / `task_completion_failed`；(b) `run.Complete` 状态机拒绝 → `recovery_required` / `closeout_failed`；(c) `run.Complete` 成功但 `persistRun` 失败 → `stopUnpersistedCloseout` 重读数据库并写 `recovery_required` / `closeout_failed`；若该写入也失败 → 保留 `running`，由下次启动按租约收敛 |
| 当前恢复入口 | `recoverUnfinished`（启动时）；`stopUnpersistedCloseout`（进程内兜底） |
| 当前收敛行为 | 收尾结论只由 `domain.DecideWorkflowCloseout` 计算；顺序固定为 steps → code_save_unconfirmed → code_save_indeterminate → code_save_failed → session_not_persisted → completed |
| 重试安全性 | 收尾可重入（`ResumeWorkflow`），但 `blocked`/`recovery_required` 的恢复需要用户显式发起 |
| 证据与测试 | `tests/integration/local/workflow/delivery_chain_test.go`（`TestDeliveryChainKeepsTaskCompletedWhenCloseoutFails`、`TestDeliveryChainStopsWithoutCodeSaver`）、`e2e_indeterminate_test.go`（`TestE2EIndeterminateCloseoutWriteFails`）、`tests/unit/domain/workflow_run_test.go`（`TestDecideWorkflowCloseout`）、`tests/unit/app/task_workflow_test.go`（`TestWorkflowDoesNotReportSuccessWithoutCodeSaver`） |
| 剩余缺口 | **P2-M5 更新**：1) 收尾写入顺序改为"**先执行记录、后任务**"，执行记录的条件写入作为栅栏：栅栏失败绝不写任务成功终态，栅栏通过后任务写入失败只会留下 `run=completed` + `task=recovery_required`（"未完成"）；2) 新增 `domain.DecideTaskCloseoutView` 派生 `confirmed`/`pending`/`unconfirmed`/`inconsistent`/`cancelled`，任务 API 新增 `final_result` / `closeout` / `closeout_summary`，管理接口不再把结论存疑的任务当作确定成功；3) 新增 `recoverStalledDelivery` 收敛卡在 `verifying`/`saving` 的任务（G15）；4) `reportCloseoutInconsistencies` 只读报告遗留的 `completed` + `recovery_required` 现场（不改写终态）；5) 仍未变：`blocked` / `recovery_required` 执行记录永久保留，恢复入口仍是 `resume-workflow` |

### 2.14 取消、超时、服务停止与进程重启跨越的中断场景

| 字段 | 内容 |
|---|---|
| 阶段与入口 | 取消：`TaskWorkflowService.CancelWorkflow`（`task_workflow_run.go:298`）与 `TaskService.CancelTaskForAPI`（`task_management.go:223`）；超时：`StepExecutor.executionTimeout` / `ValidationService` 的整体 deadline；服务停止：`lifecycle.Manager.shutdown` 逆序 `Stop`；进程崩溃：无入口 |
| 前置条件 | 取消要求存在未结束执行（否则幂等返回或 404）；停止要求服务已注册 |
| 副作用 | 取消传播到 Pi 子进程；停止取消在途调用并等待收敛 |
| 执行前记录 | 取消：`RequestCancel` 写 `cancel_requested` + `cancel_requested_at` 并落库，随后 `Transition(cancel_requested)`，**先落盘再传播**（`CancelWorkflow`）；超时：`context.WithTimeoutCause(..., DeadlineExceeded)`；停止：`beginRun` 的 stopSignal 以 `ErrWorkflowStopped` / `ErrStepExecutorStopped` 作为 cause |
| 成功证据 | `workflow_runs.status ∈ {cancelled, blocked, recovery_required}`；`attempts.status ∈ {cancelled, timed_out, interrupted}`；`tasks.status ∈ {cancelled, recovery_required}` |
| 失败与中断窗口 | (a) 取消请求落库后进程退出 → 执行记录停在 `cancel_requested`；(b) 任务层还没收敛（仍在 `developing`）时进程退出 → **任务状态不会收敛**（见缺口）；(c) 服务停止时在途调用无法在超时内收敛 → `Stop` 返回超时错误，记录可能停在 `running`；(d) 进程被 kill -9 → 无任何进程内处理，只能靠启动恢复 |
| 当前恢复入口 | 执行记录层：`recoverRun` 的 `cancel_requested → cancelled`；任务层：`recoverCancelling`（仅处理 `tasks.status=cancelling`）；超时/中断在进程内的分类见 `classifyStepExecutionRun` 与 `isServiceStop`（`internal/app/step_executor_run.go`） |
| 当前收敛行为 | 用户取消 → 记录 `cancelled`，任务 `cancelled`；Agent 超时 → 尝试 `timed_out`、步骤 `failed`、任务 `recovery_required`、执行记录 `recovery_required` / `developer_interrupted`；服务停止 → 尝试 `interrupted`、任务 `recovery_required`、执行记录 `recovery_required`；进程崩溃 → 启动恢复按租约收敛为 `blocked` |
| 重试安全性 | 取消幂等（不改写时间戳）；超时/中断不自动重试 |
| 证据与测试 | `tests/unit/app/task_workflow_test.go`（`TestWorkflowCancelStopsAdvance`、`TestWorkflowCancelIsIdempotent`）、`tests/unit/app/step_executor_test.go`（`TestExecuteStepTimeout`、`TestExecuteStepCancelled`、`TestExecutorStopCancelsInFlightRun`）、`tests/unit/app/step_workflow_test.go`（`TestWorkflowStopCancelsInFlightRun`）、`tests/integration/local/workflow/recovery_test.go`（`TestRecoverCancellingOnRestart`、`TestRecoverCancellingFailureBlocksStartup`、`TestRecoverCancellingActiveRunReadFailure`）、`tests/unit/domain/workflow_run_test.go`（`TestWorkflowRunRequestCancelIsIdempotent`） |
| 剩余缺口 | 1) **执行记录取消生效但任务层未收敛**：`recoverRun` 把 `cancel_requested → cancelled` 后不处理任务；若崩溃时任务仍在 `developing`，`recoverCancelling` 不会处理它（只查 `cancelling`），用户取消意图丢失，任务之后还能被 `StartWorkflow` 重新推进；2) 服务停止超时后没有"再次尝试收敛"的入口；3) `cancel_requested` 期间跨进程崩溃后，`attempts`/`sessions` 残留 `running`；4) 没有"服务停止 + 在途验证"的集成用例 |

---

## 3. 恢复责任分级

### 3.1 自动恢复（有正式调用入口，由服务生命周期触发）

| 能力 | 入口 | 触发 | 依据的持久化事实 | 收敛结果 |
|---|---|---|---|---|
| 未结束执行记录收敛 | `TaskWorkflowService.recoverUnfinished` | `TaskWorkflowService.Start` | `workflow_runs.status` + `lease_expires_at` + `cancel_requested` | `cancel_requested→cancelled`；租约过期的 `running→blocked(worker_interrupted)`；超窗 `pending→blocked(not_started)` |
| 计划现场收敛 | `recoverPlanGeneration` | 同上 | `tasks.status=planning` + `steps` 是否存在 | `planned` 或 `recovery_required` |
| 取消现场收敛 | `recoverCancelling` | 同上 | `tasks.status=cancelling` + `cancel_requested` + 是否有未结束执行 | `cancelled` 或 `recovery_required` |
| 工作区初始化残留 | `TaskWorkspaceService.RecoverInterrupted` | `TaskLaunchService.Start` → `recoverWorkspace` | `task_workspace_initializations.status ∈ {pending, running}` | `uncertain` |
| 步骤占用残留 | `TaskLaunchService.RecoverStepOccupancy` | `TaskLaunchService.Start` | `steps.status ∈ {running, developing, verifying}` + 是否有未结束执行 | `recovery_required` + 释放占用 |
| 执行节点预检残留 | `HostValidationService.recoverPending` | `HostValidationService.Start` | `execution_hosts.validation_status=checking` | 重新入队，或 `abnormal`（失败只记日志） |
| 收尾写入失败兜底 | `stopUnpersistedCloseout` | 进程内 `applyCloseout` | 重读 `workflow_runs` 真实值 | `recovery_required(closeout_failed)`；再失败则保留 `running` |
| 幂等收敛 | `writeRecoveredTask` | 上述恢复内部 | `UpdateTaskIfStatus` 冲突后重读 | 状态已等于目标值 → 视为已完成，不重复写事件 |

### 3.2 用户触发的核验或恢复（能力存在，但需显式调用）

| 能力 | 入口 | 说明 |
|---|---|---|
| 恢复工作流推进 | `POST /api/v1/tasks/{id}/resume-workflow`（`TaskWorkflowService.ResumeWorkflow`）、CLI `agentflow task` 系列 | 要求 `run.Status.CanStart()`、无取消请求、`plan_digest` 一致、`CheckWorkflowStart` 通过 |
| 重新启动任务 | `POST /api/v1/tasks/{id}/start`（`TaskLaunchService.Launch`）、CLI `agentflow task start` | 依据 `DecideTaskLaunchNext` 从最早未完成阶段继续；不重复建容器、不重复 Clone |
| 代码保存只读核验 | `agentflow code verify --record <id>`（`ContainerService.VerifyCodeSave`） | 唯一能区分"记录 pending"与"Git 实际做了什么"的入口；结论写 `recovery_status` |
| 容器生命周期处置 | `agentflow docker container start/stop/delete/detach`、`ContainerService.ApplyLifecycle` | 重新开始失败任务时必须显式 `delete` 或 `detach` 旧容器 |
| 步骤重新执行 | `POST .../steps/{id}/execute`、`.../execute-and-validate`、`.../execute-steps` | `recovery_required` 步骤可通过 `CanStartExecution()` 重新执行 |
| 任务取消 | `POST /api/v1/tasks/{id}/cancel`、`POST .../cancel-workflow` | 先落盘再传播 |
| 工作流状态查询 | `GET /api/v1/tasks/{id}/workflow`、`GET /api/v1/tasks/{id}/steps/...` | 只读；`WorkflowRunView` 暴露 `ErrorClass` / `ErrorSummary` / 游标 |

### 3.3 只能安全停止（结果或副作用无法确认，不应自动重放）

| 场景 | 现场证据 | 为什么不能自动重放 |
|---|---|---|
| 工作区初始化 `uncertain` | `task_workspace_initializations.status=uncertain` + `stage` | 重放 clone 会覆盖可能已就绪的工作区 |
| 计划生成无步骤 | `tasks.status=planning` 且 `steps` 为空 | 无法判断 Planner 是否真的被调用过；重放会重复消耗模型并可能产生第二份计划 |
| 代码保存 `persist_status=unconfirmed` | DB 停在 `pending` + `baseline_commit_sha` | 记录可能落后于真实 Git 状态 |
| push `unknown` / `conflict` | `push_status ∈ {unknown, conflict}` + `error_summary` | 远程可能已接受；强推会覆盖他人提交 |
| merge `unverifiable` | 基础分支已变但无法唯一确定合并提交 | 无法证明是本次合并造成的 |
| 步骤 `recovery_required` | `steps.status=recovery_required` + `failure_summary` | 在未知现场重放 Pi 调用会覆盖真实执行结果 |
| 执行记录 `blocked` / `recovery_required` | `workflow_runs.status` + `error_class` | 停止原因可能是外部副作用不确定 |

### 3.4 缺少恢复能力（无完整处理路径）

> **P2-M2 更新（2026-10-24）**：下表中 `attempts` / `sessions` / `validation_runs`
> 三类残留与"任务层取消意图丢失"已在 P2-M2 补齐收敛路径，实现为
> `app.ExecutionResidueService`（启动时按"任务是否仍有持有有效租约的执行者"判定）
> 与 `TaskWorkflowService.recoverCancellationIntents`。本表其余条目仍然成立，
> 属于 P2-M4/M5 范围。逐条结论见
> [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md) §2.4–§2.6。

| 缺口 | 证据 | 影响 | P2-M2 状态 |
|---|---|---|---|
| `attempts` 停在 `running` | 无任何收敛代码（`grep AttemptStatusRunning` 仅命中写入路径与 `finishAttempt` 的"非 running 则跳过"） | 尝试历史无法解释；无法判断"Agent 是否执行过" | **已修复**：收敛为 `interrupted`，`failure_stage = recover` |
| `sessions` 停在 `running`/`starting` | `SessionService.Fail`（`sessions.go:234`）**无生产调用者** | 收尾时 `sessionsPersisted` 恒为 false → 任务永久 `recovery_required` | **已修复**：收敛为 `interrupted` + `run_status = failed`，产物路径与归档事实原样保留 |
| `validation_runs` 停在 `pending`/`running` | 无收敛代码；`BeginValidationRun` 的活跃验证查询（`validations.go:141`）会永久拒绝该 Attempt 的新验证 | 该 Attempt 永久无法再验证，只能换 Attempt | **已修复**：收敛为 `failed` + `verdict = failed`，`error_detail` 说明中断来源 |
| Attempt 级 `code_fetches` 残留 | ~~`ListInterruptedTaskWorkspaceInits` 只查 `task_workspace_initializations`~~ | 中断记录永久停留，无收敛 | **P2-M5 已修复**：`app.CodeFetchRecoveryService` 收敛为 `uncertain`（有执行者时不抢占），并提供只读核验入口 |
| `container.state=unknown` | ~~`domain` 允许 unknown→running，但无调用方推进~~ | 容器状态不可信，且 `Launch` 会另建容器 | **P2-M5 已修复**：`app.ContainerRecoveryService` 按 `domain.DecideContainerRecovery` 条件收敛；`TaskLaunchService` / `ExecutionEnvironmentService` 拒绝在未处置旧容器时新建 |
| 孤儿容器（创建成功、关联失败） | ~~`recordOrphanContainer` 只写事件~~ | 下次 `Launch` 再建容器；孤儿永不回收 | **P2-M5 已修复**：`ContainerFilter.Orphans` + `recover_container_orphan` 事件使孤儿可发现；只报告、不删除、不自动重新关联 |
| `uncertain` 工作区无只读核验入口 | ~~只有 `code verify` 覆盖代码保存~~ | 用户只能人工进容器判断 | **P2-M5 部分修复**：Attempt 级有 `code fetch-verify`；任务级仍是人工核验（刻意保留） |
| 归档失败无重做入口 | `sessionsPersisted` 只读 | `recovery_required` / `session_not_persisted` 后无出路 |
| 步骤 `recovery_required` 无只读核验入口 | 无 | 用户只能直接重跑（有覆盖风险） |
| ~~任务层取消意图丢失~~ | ~~`recoverRun` 只收敛执行记录~~ | **P2-M2 已修复**：取消入口双写任务层意图，`recoverCancellationIntents` 跨层补齐（B5） |

---

## 4. 恢复顺序与状态边界说明

### 4.1 层次边界

- **Task 层状态**：业务阶段（`queued` … `completed` / `cancelled` / `failed` / `recovery_required`）。
  终态只有 `completed` 与 `cancelled`（`TaskStatus.IsTerminal`），删除判定用 `IsFinished`
  （额外含 `failed`）。
- **WorkflowRun 层状态**：这一次编排是否在推进（`pending` / `running` / `blocked` /
  `cancel_requested` / `cancelled` / `completed` / `failed` / `recovery_required`）。
  终态只有 `completed` / `cancelled` / `failed`；`blocked` 与 `recovery_required` 需要用户介入。
- **Step 层状态**：`pending` / `running` / `developing` / `verifying` / `completed` / `failed` /
  `retry_wait` / `skipped` / `cancelled` / `recovery_required`。终态 `completed` / `skipped` / `cancelled`。
- **Attempt 层状态**：一次执行的尝试结论（`running` / `completed` / `failed` / `timed_out` /
  `cancelled` / `interrupted` / `recovery_required` 等）。
- **Session 层状态**：一次 Pi 会话（`starting` / `running` / 终态）+ 三个**独立**维度：
  `completeness`（原始文件完整性）、`export_status`（HTML 导出）、`archive_status` + `archive_path`（归档）。
- **ValidationRun 层**：流程状态（`pending` / `running` / `completed` / `failed` / `cancelled` /
  `timed_out`）与最终结论（`verdict`）**分别持久化**，不得互相推导。

**核心约束**：单个实体状态正确**不**代表整条任务链正确。反例已在测试中固定：
`TestDeliveryChainKeepsTaskCompletedWhenCloseoutFails` 显示 `tasks.status=completed` 而
`workflow_runs.status=recovery_required`。恢复判定必须按层读取，不能用上层状态推断下层。

### 4.2 恢复写入失败时的行为

| 恢复入口 | 写入失败时的行为 | 是否阻止启动 |
|---|---|---|
| `recoverUnfinished` / `recoverPlanGeneration` / `recoverCancelling` | 立即返回错误，**不继续处理剩余记录** | 是 |
| `RecoverStepOccupancy` | 立即返回错误 | 是 |
| `TaskWorkspaceService.RecoverInterrupted` | 立即返回错误 | 是 |
| `HostValidationService.recoverPending` | 只记警告日志，继续启动 | 否 |
| `stopUnpersistedCloseout` | 再读数据库失败 → 保留 `running` 现场 + 错误日志 | 否（进程内） |
| `writeRecoveredTask` | 冲突且数据库状态已等于目标值 → 视为已完成；否则返回错误 | 是（经上层） |

**不一致点**：工作流/启动恢复采用"失败即阻止启动"，节点预检恢复采用"失败仅记日志"。
两套语义都没有错，但需要在 M2 明确归类，避免同一类残留出现两种处理口径。

### 4.3 一次恢复不会做的事

- 不重放 Pi 调用（`recoverPlanGeneration`、`RecoverStepOccupancy` 均明确禁止）。
- 不重放代码获取 / Clone（`RecoverInterrupted` 只收敛为 `uncertain`）。
- 不重放 commit / push / merge（`DecideCodeSaveRecovery` 只回答"是否已经发生"）。
- 不删除容器、不启动容器、不接管 detached 容器。
- 不覆盖已确认终态（所有写入带状态/版本前置条件）。

---

## 5. 阻塞性缺陷与高风险缺口

按"是否伪造成功"与"是否导致状态无法收敛"分类。**未发现把未确认结果判为成功的缺陷**；
以下缺陷都属于"状态无法收敛"或"语义不一致"，但对 P1 的可用性构成实质阻塞。

### 5.1 B1（阻塞）步骤占用残留与 blocked 执行记录互相锁死

- **证据**：`TaskLaunchService.recoverTaskStepOccupancy`（`internal/app/task_launch_recovery.go:56`）
  用 `taskHasActiveRun` → `GetActiveWorkflowRun` 判定；`workflowRunActivePredicate`
  （`internal/storage/sqlite/workflow_runs.go:21`）为 `status NOT IN ('completed','cancelled','failed')`，
  因此 `blocked` **算作**未结束。
- **复现条件**：进程在 Pi 调用期间退出（步骤停在 `running`/`developing`/`verifying`，
  执行记录停在 `running`）→ 重启 → `recoverUnfinished` 把执行记录收敛为 `blocked`
  → `RecoverStepOccupancy` 发现"仍有未结束执行" → **不收敛步骤**。
- **后果**：`Launch` 因 `HasActiveWorkflowRun` 被拒；`ResumeWorkflow` 因
  `checkWorkflowStepState` 拒绝占用步骤；`StartWorkflow` 同样被拒。任务永久卡死，
  只能手工改数据库。
- **建议修复边界**（留给 M2，不在 M1 实施）：`DecideStepOccupancyRecovery` 的输入语义应区分
  "正在推进的执行"与"已停止但未终结的执行"，或恢复顺序改为"先步骤、后执行记录"。
- **P2-M2 修复（已完成）**：采用第一种方案。`domain.TaskLaunchStepOccupancyInput` 改为
  携带执行记录本身与当前时刻，新增 `workflowRunOwnsExecution`：只有 `running` **且租约
  有效**的记录才算"有执行者可能在推进"。`blocked` / `recovery_required` / 过期租约的
  `running` / 超窗的 `pending` 都不再阻止步骤收敛。恢复顺序未改动，跨进程保护未放宽
  （部分唯一索引与版本号条件更新保持原样）。证据：
  `tests/integration/local/workflow/step_occupancy_test.go` 的
  `TestStepOccupancyConvergesAfterRunBlocked`（完整锁死场景）、
  `TestStepOccupancyKeepsLiveLeaseOwner`、`TestStepOccupancyConvergedTaskCanLaunch`
  （收敛后正式入口可用）、`TestStepOccupancyConvergesOnceAcrossRestarts`、
  `TestStepOccupancyReadFailureDoesNotConverge`。

### 5.2 B2（阻塞）验证流程残留使 Attempt 永久无法再验证

- **证据**：`BeginValidationRun` 的活跃验证查询只认 `pending`/`running`
  （`internal/storage/sqlite/validations.go:141-160`）；没有任何代码收敛 `validation_runs`。
- **复现条件**：进程在验证期间退出。
- **后果**：该 Attempt 之后每次 `validate` 都返回 409；`domain.DecideValidation` 对
  `running` 只给"不能判定通过"。
- **建议修复边界**：M2 增加"启动时把残留 `pending`/`running` 验证收敛为
  `failed`/`cancelled` + `verdict=inconclusive`"的正式入口，并复用
  `domain.DecideValidation` 的"流程未完成不得通过"规则。
- **P2-M2 修复（已完成）**：实现为 `app.ExecutionResidueService.recoverValidationRuns`，
  收敛为 `failed` + `verdict = failed`（`DecideValidation` 对非 `completed` 流程一律判
  `failed`，因此不采用 `inconclusive`），`error_detail` 说明"进程退出时仍在活动状态"，
  措辞刻意不使用任何暗示"检查未通过"的表述。证据：
  `tests/unit/app/execution_residue_test.go` 的
  `TestResidueConvergesValidationRunToFailed`（同时断言收敛后该 Attempt 可以重新验证）。

### 5.3 B3（阻塞）Attempt 与 Session 的 `running` 残留无收敛路径

- **证据**：`grep` 全仓 `AttemptStatusRunning` 只出现在写入与 `finishAttempt` 的"非 running
  则跳过"分支；`SessionService.Fail` 无生产调用者。
- **后果**：收尾时 `sessionsPersisted` 恒为 false → 任务永久 `recovery_required`；
  执行记录无法解释"Agent 是否执行过"。
- **建议修复边界**：M2 明确"残留 `running` 会话/尝试在启动时收敛为什么状态"，并补
  `SessionService.Fail` 的调用点（进程启动失败、工作目录不可用等路径）。
- **P2-M2 修复（已完成）**：实现为 `app.ExecutionResidueService.recoverAttempts` /
  `recoverSessions`。Attempt 收敛为 `interrupted`（`failure_stage = recover`），Session
  收敛为 `interrupted` + `run_status = failed`，且**不删除**任何已有产物路径与归档事实。
  判据是"任务是否仍有持有有效租约的执行者"，因此一个仍在运行的其他实例不会被抢占。
  证据：`tests/unit/app/execution_residue_test.go` 的
  `TestResidueConvergesAttemptToInterrupted`、
  `TestResidueConvergesSessionToInterrupted`（断言路径与归档事实原样保留）、
  `TestResidueKeepsLiveLeaseResidue`、`TestResidueConvergesResidueWithoutRun`、
  `TestResidueKeepsTerminalRecords`、`TestResidueIsIdempotent`。

### 5.4 B4（高）容器孤儿与 `unknown` 状态无收敛（P2-M5 修复）

- **证据**：`attachContainerToTask` 冲突时只写事件（`internal/app/execenv_task.go:recordOrphanContainer`）；
  `ContainerStateUnknown` 无调用方推进。
- **后果**：下次 `Launch` 创建第二个容器；孤儿容器永不回收。
- **P2-M5 修复（已完成）**：新增 `app.ContainerRecoveryService` 与
  `domain.DecideContainerRecovery`，把 `creating` / `unknown` 按只读观测条件收敛为
  running / stopped / removed，所有权不匹配与不可达一律保留 `unknown` 并要求人工介入。
  孤儿容器通过 `domain.ContainerFilter.Orphans`（登记了任务关联但不是该任务当前容器）
  被发现，只追加事件、不删除、不自动重新关联。`TaskLaunchService.ensureNoPendingContainer`
  与 `ExecutionEnvironmentService.ensureNoPendingContainer` 使"仍有未处置容器时不得新建"
  成为前置条件，从而阻止第二个容器。证据：
  `tests/integration/local/execenv/container_recovery_test.go`（9 个用例）、
  `tests/unit/container/container_recovery_test.go`（5 个用例）、
  `tests/unit/store/recovery_conditional_test.go` 中的 `TestContainerFilterByTaskAndOrphans`。

### 5.5 B5（高）执行记录层取消生效但任务层未收敛

- **证据**：`recoverRun` 对 `cancel_requested` 只做 `run.Cancel(...)`
  （`internal/app/task_workflow_run.go:857`），不触碰 `tasks`；`recoverCancelling` 只处理
  `tasks.status=cancelling`。
- **后果**：崩溃点落在"取消请求已落库、任务尚未进入 `cancelling`"时，重启后任务仍是
  `developing`，可被 `StartWorkflow` 重新推进——用户取消意图丢失。
- **P2-M2 修复（已完成）**：`CancelWorkflow` 在写执行记录之后立即调用
  `recordTaskCancelIntent` 写任务层意图（缩小崩溃窗口）；启动恢复新增
  `recoverCancellationIntents`，依据 `domain.DecideCancellationIntentRecovery` 与
  `domain.DecideCancellationRecovery` 把"执行记录已取消、任务层未同步"的现场补齐。
  不假设所有 `WorkflowRun.CancelRequested` 都必须把任务标为 `cancelled`：执行记录仍未
  结束时不在此处改写任务状态。证据：
  `tests/integration/local/workflow/cancel_intent_test.go` 的
  `TestRecoverCancelIntentConvergesTask`、`TestRecoverCancelIntentBlocksNewWorkflow`、
  `TestRecoverCancelIntentIsIdempotent`、`TestRecoverCancelIntentKeepsTerminalTask`、
  `TestRecoverCancelIntentFailureBlocksStartup`。

### 5.6 B6（高）`workflow_runs` 租约只判定一次且无续租

- **证据**：`run.Start(owner, lease, now)` 只在 `beginRunRecord` 时写一次
  （`internal/app/task_workflow_run.go:407`）；`internal/app` 中无 `time.NewTicker` /
  周期性续租；默认租约 10 分钟（`task_workflow.go:51`），单步默认超时 30 分钟
  （`step_executor.go:defaultStepExecutionTimeout`）。
- **后果**：单步执行超过 10 分钟后，若另一进程启动，`recoverRun` 会认为租约过期并把仍在
  推进的记录收敛为 `blocked`——用 `revision` 条件只能阻止覆盖，不能阻止误判。
- **P2-M2 修复（已完成）**：新增 `domain.WorkflowRun.RenewLease`（只允许 `running` +
  同一 `worker_id` + 已有租约时延长）与 `storage.WorkflowRunLeaseStore.RenewWorkflowRunLease`
  （只更新 `lease_expires_at` / `updated_at`，**不推进 `revision`**，避免与正常推进的写入
  互相覆盖）。`TaskWorkflowService` 在推进期间按租约的 1/3 周期续租；续租失败视为失去
  执行权，立即取消本次推进且**不写任何终态**（避免用过期执行者的本地对象覆盖真实记录）。
  证据：`tests/unit/app/task_workflow_lease_test.go` 的
  `TestWorkflowHeartbeatKeepsLeaseAlive`（单次调用跨越原始租约仍持有执行权）、
  `TestWorkflowHeartbeatDoesNotReviveTerminalRun`、
  `TestWorkflowRecoveryKeepsRenewedLease`（另一实例不抢占）、
  `TestWorkflowExpiredLeaseAllowsRecovery`（真正退出的进程仍可被收敛）。

### 5.7 B7（中）归档语义不一致

- **证据**：`archiveOf`（`internal/app/sessions.go:290`）只要 `JSONLPath` 非空就记
  `archive_status=succeeded`，即使 `completeness=partial`（`container_runner.go:426`）。
- **后果**：DB 声明"归档成功"但原始文件不完整；`sessionsPersisted` 通过 `completeness`
  兜住了收尾判定，但记录本身的语义与"归档成功"冲突。

### 5.8 已知测试缺陷

- ~~`TestPlanGenerationInterruptedByShutdown`（`tests/integration/local/workflow/recovery_test.go:791`）
  在干净工作区上约 40% 概率失败~~ —— **P2-M2 已修复**。根因是替身 `duringCall` 用独立
  goroutine 触发 `Stop`，而替身在取消传播到本次调用之前就可能返回结果，结论因此取决于
  goroutine 调度顺序。修复方式：替身新增 `waitForCancel`，等本次调用的上下文真的被取消
  后才返回，建立确定的先后关系（`-race` 下连续 15 次全部通过）。
- 另发现并修复一处真实生产缺陷：`GetWorkflowStatus` 原先先读任务状态、后读执行记录，
  一次正在完成的收尾会被读成"任务仍在 `saving`、执行记录已 `completed`"这种不可能同时
  成立的组合（`tests/unit/httpapi` 的 `TestWorkflowAPILifecycle` 间歇失败）。读取顺序
  改为"执行记录 → 任务 → 步骤"。

---

## 6. 测试覆盖与证据缺口

### 6.1 已覆盖的恢复场景（实现 + 行为均有证据）

| 场景 | 测试 |
|---|---|
| 未结束执行记录 → `blocked` | `recovery_test.go:TestRecoverUnfinishedRunOnRestart`、`task_workflow_test.go:TestWorkflowRecoveryOnServerRestart` |
| 有效租约不被抢占 | `task_workflow_test.go:TestWorkflowRecoveryKeepsLiveLease` |
| `cancel_requested` → `cancelled` | `recovery_test.go:TestRecoverCancellingOnRestart`、`task_workflow_test.go:TestWorkflowRecoveryCancelsRequested` |
| 计划现场（有/无步骤） | `recovery_test.go:TestRecoverPlanGenerationOnRestart` |
| 恢复读取失败阻止启动 | `recovery_test.go:TestRecoverPlanGenerationFailureBlocksStartup` |
| 恢复写入失败阻止启动 | `recovery_test.go:TestRecoverUnfinishedRunFailureBlocksStartup`、`TestRecoverCancellingFailureBlocksStartup` |
| 恢复读取失败不误判取消生效 | `recovery_test.go:TestRecoverCancellingActiveRunReadFailure` |
| 并发恢复只收敛一次 | `recovery_test.go:TestConcurrentRecoveryConvergesOnce` |
| 工作区中断 → `uncertain` | `launch_test.go:TestWorkspaceInterruptedConvergesToUncertain`、`task_workspace_test.go:TestRecoverInterruptedConvergesToUncertain`、`TestRecoverInterruptedKeepsTerminalRecords` |
| `uncertain` 工作区不重放 | `launch_chain_test.go:TestLaunchChainRejectsUncertainWorkspaceWithoutReplay` |
| 步骤占用 → `recovery_required` | `launch_test.go:TestRecoverStepOccupancyPersistsRecoveryRequired`、`task_launch_test.go:TestRecoverStepOccupancyConvergesIdleOccupancy`、`TestRecoverStepOccupancySkipsActiveRun` |
| 有未结束执行时不抢占占用 | `launch_test.go:TestRecoverStepOccupancyKeepsActiveRunOccupancy` |
| 重复启动幂等（不重复建容器/Clone） | `launch_chain_test.go:TestLaunchChainIsIdempotentOnRepeat`、`launch_test.go:TestLaunchDoesNotCreateSecondContainer` |
| 提交已成功但记录未落盘 → 可核验 | `codesaver_test.go:TestCommitSucceededButRecordFailedIsRecoverable`（真实 Git） |
| push 超时但远程已接受 | `codesaver_test.go:TestPushTimeoutButRemoteAccepted` |
| push 无法核验 → unknown 且不重放 | `codesaver_test.go:TestPushUnknownWhenRemoteUnverifiable`、`e2e_indeterminate_test.go:TestE2EIndeterminateCodeSavePushUnknown` |
| 远程与预期不一致 → unverifiable | `codesaver_test.go:TestObserveDetectsRemoteMismatch` |
| 远程不可达 → 保留不确定性 | `codesaver_test.go:TestObserveReportsUnreachableRemote` |
| 合并已发生可被确认 | `codesaver_test.go:TestObserveConfirmsMergeApplied` |
| 收尾记录写入失败 → `recovery_required` | `delivery_chain_test.go:TestDeliveryChainKeepsTaskCompletedWhenCloseoutFails`、`e2e_indeterminate_test.go:TestE2EIndeterminateCloseoutWriteFails` |
| 会话归档缺失 / 查询失败 | `delivery_chain_test.go:TestDeliveryChainStopsWhenSessionArchiveIncomplete`、`TestDeliveryChainStopsWhenSessionQueryFails` |
| 迟到结果不覆盖终态 | `step_executor_test.go:TestExecuteStepLateResultDoesNotOverrideTerminal` |
| 取消幂等 | `task_workflow_test.go:TestWorkflowCancelIsIdempotent` |
| 恢复不重放已完成步骤 / 计划变更拒绝恢复 | `task_workflow_test.go:TestWorkflowResumeSkipsCompletedSteps`、`TestWorkflowResumeRejectsPlanChange` |
| 无代码保存能力不判成功 | `task_workflow_test.go:TestWorkflowDoesNotReportSuccessWithoutCodeSaver`、`e2e_indeterminate_test.go:TestE2EIndeterminateWithoutCodeSaver` |

**P2-M2 新增覆盖（2026-10-24）**：

| 场景 | 测试 |
|---|---|
| 步骤占用 × `blocked` 记录锁死被解除（B1） | `step_occupancy_test.go:TestStepOccupancyConvergesAfterRunBlocked` |
| 有效执行权存在时不抢占（B1） | `step_occupancy_test.go:TestStepOccupancyKeepsLiveLeaseOwner` |
| 收敛后正式入口可用（B1） | `step_occupancy_test.go:TestStepOccupancyConvergedTaskCanLaunch` |
| 重复收敛只写一条事件（B1） | `step_occupancy_test.go:TestStepOccupancyConvergesOnceAcrossRestarts` |
| 读取执行记录失败时不抢占（B1） | `step_occupancy_test.go:TestStepOccupancyReadFailureDoesNotConverge` |
| 取消意图跨重启补齐任务层（B5） | `cancel_intent_test.go:TestRecoverCancelIntentConvergesTask` |
| 收敛后拒绝启动新工作流（B5） | `cancel_intent_test.go:TestRecoverCancelIntentBlocksNewWorkflow` |
| 重复恢复幂等（B5） | `cancel_intent_test.go:TestRecoverCancelIntentIsIdempotent` |
| 已结束任务不被重新打开（B5） | `cancel_intent_test.go:TestRecoverCancelIntentKeepsTerminalTask` |
| 补齐写入失败阻止启动（B5） | `cancel_intent_test.go:TestRecoverCancelIntentFailureBlocksStartup` |
| 长调用跨越原始租约不丢执行权（B6） | `task_workflow_lease_test.go:TestWorkflowHeartbeatKeepsLeaseAlive` |
| 终态记录不被续租复活（B6） | `task_workflow_lease_test.go:TestWorkflowHeartbeatDoesNotReviveTerminalRun` |
| 续租后另一实例不抢占（B6） | `task_workflow_lease_test.go:TestWorkflowRecoveryKeepsRenewedLease` |
| 真正退出的进程仍可被收敛（B6） | `task_workflow_lease_test.go:TestWorkflowExpiredLeaseAllowsRecovery` |
| 残留 Attempt → `interrupted`（B3） | `execution_residue_test.go:TestResidueConvergesAttemptToInterrupted` |
| 残留 Session → `interrupted` 且现场保留（B3） | `execution_residue_test.go:TestResidueConvergesSessionToInterrupted` |
| 残留 ValidationRun → `failed` 且可重验（B2） | `execution_residue_test.go:TestResidueConvergesValidationRunToFailed` |
| 有效执行权时不收敛残留 | `execution_residue_test.go:TestResidueKeepsLiveLeaseResidue` |
| 没有执行记录的残留同样被收敛 | `execution_residue_test.go:TestResidueConvergesResidueWithoutRun` |
| 已终结记录不被改写 | `execution_residue_test.go:TestResidueKeepsTerminalRecords` |
| 重复收敛幂等 | `execution_residue_test.go:TestResidueIsIdempotent` |
| 验证取消不判通过 | `validation_interruption_test.go:TestValidationCancelIsNotPassed` |
| 验证超时不判通过 | `validation_interruption_test.go:TestValidationTimeoutIsNotPassed` |
| 验证服务停止 ≠ 用户取消 | `validation_interruption_test.go:TestValidationServiceStopIsNotUserCancel` |
| 中断后检查记为 skipped | `validation_interruption_test.go:TestValidationInterruptedChecksAreSkipped` |
| 中断时不发起语义审查 | `validation_interruption_test.go:TestValidationInterruptionDoesNotCallValidator` |
| 中断后尝试判定与验证结论一致 | `validation_interruption_test.go:TestValidationInterruptionKeepsAttemptConsistent` |

### 6.2 未覆盖的故障窗口（清单）

| # | 故障窗口 | 现状 | 优先级 |
|---|---|---|---|
| G1 | 外部操作执行前退出（容器创建前、Clone 前、commit 前） | **P2-M5 已覆盖**：`TestCloseoutRunWriteFailsBeforeTaskWrite`（收尾栅栏前失败）、`TestContainerRecoveryWriteFailureBlocksStartup`、`TestCodeFetchRecoveryFailureBlocksStartup`、"写入失败阻止启动"三条 | 已关闭 |
| G2 | 外部操作已成功但结果尚未写入（容器创建成功/关联失败） | **P2-M5 已覆盖**：容器侧由 `TestContainerRecoveryReportsOrphanWithoutDeleting` 与 `TestContainerRecoveryConvergesUnknownToRunning` 覆盖；代码保存侧由 M4 的真实 Git 用例覆盖 | 已关闭 |
| G3 | 恢复读取失败 | **P2-M5 已覆盖**：`TestContainerRecoveryWriteFailureBlocksStartup`、`TestCodeFetchRecoveryFailureBlocksStartup`；工作区与步骤占用沿用 M2 用例 | 已关闭 |
| G4 | 恢复写入失败 | **P2-M5 已覆盖**：`TestUpdateCodeFetchRecordIfStatusRejectsStaleStatus`、`TestUpdateTaskWorkspaceInitIfStatusRejectsStaleStatus`、`TestUpdateContainerIfStateRejectsStaleState`（三类条件更新的冲突拒绝），使恢复里的 `ErrConflict` 分支不再是死代码 | 已关闭 |
| G5 | 恢复执行期间再次退出 | **P2-M5 已覆盖**：`TestContainerRecoveryIsIdempotentAcrossRestarts`、`TestCodeFetchRecoveryIsIdempotentAcrossRestart`（连续两次启动收敛，第二次扫描不到活动记录，事件不重复） | 已关闭 |
| G6 | 恢复过程并发/重复执行 | **P2-M5 已覆盖**：`TestContainerRecoveryConflictKeepsNewerRecord`、`TestCodeFetchRecoveryConcurrentInstancesKeepNewerRecord`（并发写入的结论被保留，不覆盖成 `unknown` / `uncertain`） | 已关闭 |
| G7 | 外部状态不可达、观测结果无法确认 | **P2-M5 已覆盖**：`TestContainerRecoveryKeepsUnknownWhenUnreachable`、`TestContainerRecoveryOwnershipMismatchNeedsManual`、`TestCodeFetchVerifyConvergesOnlyWithConclusiveFacts`（核验失败保持不确定）、`TestVerifyCodeSaveDegradesWhenContainerMissing` | 已关闭 |
| G8 | 已确认成功的外部结果不被错误改写 | **P2-M5 补充**：`TestContainerRecoveryKeepsDetachedUntouched`、`TestRestartCompletesTaskFromCompletedRun`（执行记录不被重复改写）；归档侧沿用 M1 记录的遗留缺口 | 已关闭（归档除外） |
| G9 | 恢复后不重复执行已成功副作用 | **P2-M5 已覆盖**：`TestCodeFetchInitRejectedWhileRecordUncertain`（拒绝时记录数不变）、`TestCodeFetchVerifyConvergesOnlyWithConclusiveFacts` 断言核验不执行 clone/checkout/reset/rm/clean；容器侧由 `TestLaunchRefusesSecondContainerWhenExistingNotUsable` 系列的 `ensureNoPendingContainer` 前置门控保证 | 已关闭 |
| G10 | `attempts` / `sessions` / `validation_runs` 的 `running` 残留 | **P2-M2 已实现并覆盖**（`execution_residue_test.go`） | 已关闭 |
| G11 | 服务停止时在途验证/修复循环 | **P2-M2 已补**（`validation_interruption_test.go`） | 已关闭 |
| G12 | 任务层取消未收敛（B5） | **P2-M2 已实现并覆盖**（`cancel_intent_test.go`，5 个用例） | 已关闭 |
| G13 | 租约过期误判（B6） | **P2-M2 已实现周期续租并覆盖**（`task_workflow_lease_test.go`，4 个用例） | 已关闭 |
| G14 | 容器 `unknown` 状态收敛 | **P2-M5 已实现并覆盖**：`app.ContainerRecoveryService` + `domain.DecideContainerRecovery`；`TestContainerRecoveryConvergesUnknownToRunning` / `...ConvergesAbsentToRemoved` / `...KeepsUnknownWhenUnreachable` / `...OwnershipMismatchNeedsManual` | 已关闭 |
| G15（新增） | 任务卡在 `verifying` / `saving` 后永久无法继续（既不能 Launch 也不能 Resume） | **P2-M5 已实现并覆盖**：`app.recoverStalledDelivery` + `domain.DecideTaskCloseoutRecovery`；`TestRestartConvergesTaskStuckInSaving`（收敛为 `recovery_required` 后 `StartWorkflow` 不再被状态拒绝）、`TestRestartCompletesTaskFromCompletedRun`（证据齐备时补齐成功终态） | 已关闭 |
| G16（新增） | 收尾跨表不一致（任务 `completed` + 执行记录 `recovery_required`） | **P2-M5 已修复协议并覆盖**：写入顺序改为"先执行记录、后任务"；`TestCloseoutWritesRunBeforeTask`（顺序断言）、`TestCloseoutTaskWriteFailsAfterRunCompleted`（任务写入失败不伪造成功）、`TestRestartFlagsLegacyCompletedWithRecoveryRun`（遗留现场只报告不改写） | 已关闭 |
| G17（新增） | 丢失执行权的执行者仍写任务成功终态 | **P2-M5 已修复并覆盖**：执行记录的条件写入（`revision` 前置条件）作为栅栏，冲突即放弃写任务；`TestCloseoutRunWriteFailsBeforeTaskWrite`、`tests/unit/app/task_workflow_lease_test.go` 的续租失败用例 | 已关闭 |

### 6.3 证据强度说明

- `tests/integration/local/workflow/e2e_*_test.go`（18 个用例）使用真实 SQLite + 真实统一启动
  入口 + 真实编排链路 + 真实 Git，只替身化远程 SSH/Docker/容器内 Pi。这是当前最强的
  本地恢复证据来源。
- `tests/integration/remote/workflow/e2e_*_test.go` 有真实远程证据，但只覆盖成功路径与
  代码保存，**不覆盖任何中断/恢复场景**。
- **P2-M5 更新**：G1–G9、G14 与新增的 G15–G17 已有真实 SQLite + 生产入口的集成测试证据，
  分别落在 `tests/integration/local/{workflow,execenv,fetcher}/`。仍未闭环的是**真实远程
  Docker** 上的容器 `unknown` / 孤儿现场与 `merge` 恢复：本地用例使用受控端口，不能替代
  真实远程验收。

---

## 7. 对 M2–M5 的依赖与调查优先级

> **P2-M2 / P2-M3 完成后的移交状态（2026-10-24）**：本表 P2-M2 行的 4 项问题已全部
> 落地（见 §5.1/§5.2/§5.3/§5.5/§5.6 的"P2-M2 修复"段落与
> [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)）。P2-M3 行的
> 3 项问题已全部落地：
>
> 1. **重试资格**由 `domain.DecideRepairStart` 统一定义：残留收敛为 `interrupted` 只是
>    把记录推进到合法终态，**不授予**重试资格；存在 `interrupted` / `unknown` /
>    `recovery_required` 尝试时一律判定为 `cycle_unresolved` 并保持阻塞。
> 2. 残留唯一收敛规则由 `app.ExecutionResidueService` 提供（M2 已完成）。
> 3. 修复轮次计数改为持久化事实：`repair_cycles`（迁移 `0022_repair_cycles.sql`）+
>    `attempts.budget_scope` / `budget_scope_id`，预算由 `domain.DecideRepairBudget`
>    依据同一周期的 Attempt 计算。
>
> 同时修正了本文 §2.8 记录的"`validation_feedbacks` 写入失败只记日志"缺口：现在由
> `RepairClassFeedbackUnavailable` 明确停止，且首轮修复依据只按当前周期内最近一次验证
> 精确读取，不再无条件使用步骤级"最近一次反馈"。

| 里程碑 | 依赖 M1 的结论 | 首要问题 | P2-M2 后状态 |
|---|---|---|---|
| P2-M2（中断语义） | §2.14、§4.1、B1、B5、B6 | 1) 统一"用户取消 / 超时 / 服务停止 / 进程崩溃"在**每一层**（Task / WorkflowRun / Step / Attempt / Session / ValidationRun）的收敛目标；2) 修复 B1 的锁死；3) 让执行记录层取消生效时任务层必须同步收敛（B5）；4) 决定租约续期或改用心跳（B6） | **已完成** |
| P2-M3（重试资格） | §2.6–2.8、§3.3、B2、B3 | 1) 定义"残留 `running`"的重试资格；2) 让 `validation_runs` / `attempts` / `sessions` 的残留有唯一收敛规则；3) 把修复循环的轮次计数从进程内计数改为持久化事实 | **已完成**（1 → `domain.DecideRepairStart`；2 → `ExecutionResidueService`；3 → `repair_cycles` + `attempts.budget_scope*`） |
| P2-M4（外部副作用） | §2.9–2.11、§3.2、§3.3 | 1) `VerifyCodeSave` 从"CLI 手工"升级为可编排的核验步骤（但仍需显式触发）；2) `unknown`/`conflict` 之后的用户决策路径；3) 补充"核验期间再次退出"的用例 |
| P2-M5（恢复验收） | §6.1、§6.2 | 按 G1–G17 补齐用例，并修复审查中发现的 C1/C2/C3 与 H1–H4 | **已完成**（见 `P2-reliability-recovery.md` 的 M5 详情） |

**调查优先级（按"解除最多后续依赖"排序）**：

1. ~~B1（步骤占用 × blocked 执行记录锁死）~~ —— **P2-M2 已修复**。
2. ~~B2/B3（`validation_runs` / `attempts` / `sessions` 残留）~~ —— **P2-M2 已补齐收敛
   路径**，**P2-M3 已补齐收敛之后的重试资格判定**。
3. ~~B5（任务层取消未收敛）~~ —— **P2-M2 已修复**。
4. ~~B6（租约）~~ —— **P2-M2 已修复**（周期续租）。
5. B4（容器孤儿/unknown）——影响环境准备阶段的可恢复性，仍待处理（M4/M5）。
6. B7 与 §2.12 的归档语义——影响收尾判定的一致性。

---

## 8. 与既有文档的一致性修正

调查中发现以下文档表述需要按代码事实修正（已在本文件与
`P2-reliability-recovery.md`、`ROADMAP.md` 同步）：

1. `P2-reliability-recovery.md` 原文把"代码保存恢复核验"列为**已有能力**，但未说明它
   **只在 CLI 手工调用时执行**，不属于自动恢复流程。已在本文件 §3.2 明确区分。
2. 原文"启动恢复（工作流运行记录 / 计划现场 / 取消现场收敛）"未提及
   `TaskLaunchService.Start` 还收敛**工作区初始化**与**步骤占用**两类残留。已在 §1.1 补全。
3. 原文称"超时/取消/服务关闭在单次 Agent 调用与验证引擎层面的区分"已完成——代码层面成立
   （`isServiceStop`、`classifyStepExecutionRun`），但**验证引擎侧没有对应的中断用例**，
   也没有跨层统一语义。已在 §6.2 G11 记录。
4. `ROADMAP.md` 的"已实现"列表把"启动恢复"整体列为已实现，实际只覆盖 5 类残留中的 5 个
   具体对象，`attempts` / `sessions` / `validation_runs` / `code_fetches` / 容器 `unknown`
   均无收敛路径。已在 §3.4 列出。
