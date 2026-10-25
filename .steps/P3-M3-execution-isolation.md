# P3-M3 —— 落实执行环境、工作区、Session 与资源隔离

> 撰写依据：`master` HEAD `7e950e7`（P3-M2 之后）的工作区真实代码与测试状态。
> 上游依据：[`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md)、
> [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md)、
> [`P3-M2-bounded-concurrency.md`](./P3-M2-bounded-concurrency.md)、
> [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)、
> [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)、
> [`P2-M5-closeout-protocol.md`](./P2-M5-closeout-protocol.md)。

## 0. 里程碑标识

| 项 | 值 |
|---|---|
| ID | `P3-M3` |
| 名称 | 落实执行环境、工作区、Session 与资源隔离 |
| 状态 | `completed`（2026-10-26） |
| 前置依赖 | P3-M1（`completed`）、P3-M2（`completed`） |
| 必要性 | 多任务并发下，容器 / 工作区 / Session 串扰会导致错误结果与数据覆盖 |
| 本里程碑不做的 | 调度器重启接管（P3-M4）、多任务并发验收（P3-M5）、分布式配额、缓存优化 |

## 1. 本次核实到的真实隔离缺口

以下缺口都是**读代码确认**的，不是推测。它们在 P3-M2 结束时仍然存在，且都会造成
跨任务串扰、错误执行或数据覆盖。

| 编号 | 缺口 | 代码位置 | 后果 |
|---|---|---|---|
| G1 | 执行链路直接使用 `task.ContainerID` 字符串核验容器，从不读取容器登记记录 | `task_workspace.go`、`code_initialize.go`、`step_executor.go`、`validation.go`、`task_workflow_run.go`、`task_launch_run.go` | 任务记录的 `container_id` 是普通文本列、无外键：指向其他任务的容器、未登记归属的容器或已删除的容器都会被当作可用容器，命令进入别人的现场 |
| G2 | 没有"容器归属"这一领域概念 | `internal/domain/`（缺失） | 归属判定散落在各处（只判断 `Detached` / `Removed` / `Running`），没有任何一处校验"这条容器记录登记的是本任务" |
| G3 | 单步骤执行入口（`execute`、`execute-steps`、`execute-and-validate`）不领取任务租约 | `internal/httpapi/step_execution.go`、`step_sequence.go`、`step_workflow.go` | 与任务级工作流并发时，两套所有权同时修改同一份工作区、容器与 Session |
| G4 | 步骤占用与任务终态写入只有状态条件，没有租约栅栏 | `storage/sqlite/step_execution.go`、`app/step_executor_run.go` | 旧代次的执行者可以在任务被接管后占用步骤、把成功或失败终态写到新执行者的记录上 |
| G5 | 领取冲突无法区分"任务不存在"与"执行权被占用" | `app/task_lease.go` | 接口把 404 伪装成 409，调用方会以为"任务还在、只是被占用" |
| G6 | 容器创建成功、任务关联失败时的孤儿容器没有任何自动处置，也没有归属判据 | `execenv_task.go`（已有 `recordOrphanContainer`，但无归属判据） | 孤儿容器与"当前容器"只能靠字符串比较区分，容易被误认 |

## 2. M3-1：唯一隔离契约

### 2.1 归属关系（Task → Container → Workspace → Attempt → Session）

```text
Task (tasks)
  └─ container_id ──► Container (containers)
  │                     ├─ task_id        归属判据
  │                     ├─ host_id        执行节点判据
  │                     ├─ docker_id      远程身份判据
  │                     └─ state/detached 生命周期判据
  ├─ Step (steps)
  │    └─ current_attempt_id ──► Attempt (attempts)
  │                                 ├─ task_id / step_id  归属链
  │                                 ├─ container_id       代次判据
  │                                 └─ Session (sessions)
  │                                       ├─ task_id / step_id / attempt_id
  │                                       └─ container_id / docker_id
  └─ TaskWorkspaceInitRecord (task_workspace_initializations)
        └─ task_id + container_id   代次判据（容器代次）
```

关键结论：

1. **容器归属的唯一判据是容器登记记录本身**，不是容器名称，也不是"容器里有一个
   工作区"。`containers.task_id` 与 `containers.host_id` 共同证明归属。
2. **容器代次由 `container_id` 表达**：任务重新开始时创建新容器，旧容器上的成功
   记录（工作区初始化、代码获取）不适用于新容器。
3. **Attempt 的 `container_id` / `docker_id` 是执行代次的现场快照**：它记录"这次
   尝试实际用了哪个容器"，即使任务之后换容器也不会改变。
4. **Session 的归属链由存储层校验**：`sessions.attempt_id` 所属的 `step_id` 必须与
   `sessions.step_id` 一致，否则拒绝写入（`storage.ErrInvalidData`）。

### 2.2 资源的唯一标识与生命周期

| 资源 | 唯一标识 | 生命周期起点 | 生命周期终点 | 复用规则 |
|---|---|---|---|---|
| Task | `tasks.id` | `CreateTask` | 逻辑删除（`deleted_at`） | 不物理删除 |
| Container | `containers.id`（业务）+ `docker_id`（远程） | `CreateContainer` 登记 | `MarkRemoved` / `Detach` | 同一任务同一时刻最多一个活动容器 |
| Workspace（容器内） | 容器内绝对路径（默认 `/workspace/project`） | 任务级工作区初始化成功 | 随容器删除消失 | 同一容器内跨 Attempt 复用 |
| TaskWorkspaceInit | `task_workspace_initializations.id` | `CreateTaskWorkspaceInit`（pending） | 终态（succeeded / skipped / failed / cancelled / timed_out） | 只追加，不覆盖 |
| Attempt | `attempts.id` + 序号 | `BeginStepExecution` | 终态 | 每次执行都新建，不复用 |
| Session | `sessions.id` + `invocation_id` | `SessionService.Start` | 终态 | 每次调用都新建，不复用 |
| Session 文件 | `<SessionRoot>/<task>/<step>/<attempt>/<session>/<invocation>/session.jsonl` | `SessionLayout.Paths` | 不删除（容器删除后仍可读） | 路径分层保证唯一 |
| 环境镜像 | 镜像 ID（`sha256:...`）+ 版本标签 | 构建成功 | 不自动删除 | 允许共享（不可变） |
| 缓存卷 | 节点 + 缓存类型 + 共享范围 + ABI + 共享键 | `EnsureVolume` | 不自动删除 | 允许共享（见 §2.5） |

### 2.3 不同 Task 之间禁止共享的可写资源

| 资源 | 判据 | 违反时的行为 |
|---|---|---|
| 容器登记记录 | `containers.task_id == task.id` | `DecideContainerOwnership` 返回 `container_task_mismatch` |
| 容器内项目工作区 | 容器代次（`container_id`）+ 任务分支 `agent/<task-id>` | 工作区初始化按 `(task_id, container_id)` 判定，跨任务不可复用 |
| Git 分支操作目标 | `domain.TaskBranchName(task.id)` | 分支名由任务标识派生，不同任务必然不同 |
| Session 文件与归档 | `SessionLayout`（任务 / 步骤 / 尝试 / 会话 / 调用） | 路径分层，不同任务必然不同目录 |
| Session 数据库记录 | `sessions.task_id` / `step_id` / `attempt_id` | 存储层校验归属链自洽 |
| 工作区初始化记录 | `task_workspace_initializations.(task_id, container_id)` | 按任务查询，跨任务不可见 |
| 容器内临时 Session 目录 | `/tmp/agentflow/sessions/<invocation>` | 按调用标识分层 |
| Attempt 相关临时资源 | `attempts.id` | 每次执行新建 |

### 2.4 同一 Task 内允许跨 Attempt 复用的资源

| 资源 | 复用条件 | 代码依据 |
|---|---|---|
| 任务容器 | 记录仍属于该任务、受管理、未 detached、运行中、Docker ID 一致 | `DecideContainerOwnership` + `checkContainerRunning` |
| 容器内项目工作区 | 同一容器代次上任务级工作区已就绪 | `DecideTaskWorkspaceNext` |
| 任务分支 | 分支名固定 `agent/<task-id>` | `domain.TaskBranchName` |
| 已推送的提交 | 由 CodeSaver 的恢复核验判定 | `DecideCodeSaveResume` |

**明确不复用**：Attempt、Session、Validation Run、修复轮次预算。每次重试都必须创建
新的记录（P1/P2 已确立，本里程碑不改变）。

### 2.5 共享缓存与不可变环境资源的边界

**允许共享**：

- **环境镜像**：不可变，按镜像 ID 与版本标签固定；`docker.VerifyOwnership` 通过标签
  核验归属。删除任务容器不删除镜像。
- **依赖缓存卷**：使用 Docker Named Volume，卷名由
  `agentflow-<node>-<cacheType>-<scope>-<abi>[-<scopeKey>]` 决定
  （`docker.CacheVolumeName`）。卷名**不包含任务标识**，因此它是共享资源。
  - `machine` 范围：同一节点上所有项目共享（例如 `go-mod`、`node-npm`）；
  - `project` 范围：同一节点同一项目共享（例如 `go-build`），`scopeKey` 为项目标识。
- **远程项目配置目录**：`~/.lingting/agentflow/projects/<project-id>`，只存放项目配置
  与 Dockerfile，不存放源代码。

**必须任务隔离**：

- Git 工作区（容器内 `/workspace/project`）与分支操作目标；
- 可写的构建产物、临时文件、任务级目录；
- Session 文件及其归档；
- 容器内任务状态、Attempt 相关临时资源。

**并发安全依据**：缓存卷只挂载为依赖下载缓存目录（`/var/cache/agentflow/*`），
`CacheMount.Validate` 明确拒绝落在 `/workspace` 之下或使用保留路径，因此缓存卷
**不可能与项目代码目录重叠**。共享的是"缓存"，不是"整个可写项目目录"。

**删除边界**：`execenv.ReleaseResult.TaskResourcesRemoved` 的注释明确写着"该标记永远
不包含共享镜像、共享缓存卷与远程项目配置目录"；`DecideContainerLifecycle` 也只
返回容器级处置，不产生任何 prune 动作。仓库内不存在 `docker system prune` /
`docker volume prune` 调用。

### 2.6 任务租约失效后必须立即拒绝的操作

| 操作 | 拒绝依据 | 错误类别 |
|---|---|---|
| 续租 | `lease_owner` + `lease_epoch` + `lease_expires_at > now` | `ErrTaskLeaseLost` |
| 释放 | `lease_owner` + `lease_epoch` | `ErrTaskLeaseConflict` |
| 占用步骤 | `UpdateTaskLeaseGuard`（status + owner + epoch） | `storage.ErrConflict` |
| 写任务终态 | `UpdateTaskLeaseGuard` | `storage.ErrConflict` |
| 写工作流执行记录 | `UpdateWorkflowRun`（`revision` 条件） | `storage.ErrConflict` |
| 释放容器资源 | 归属校验（`DecideContainerOwnership`） | `ErrTaskContainerOwnership` |

**明确不被租约阻止的操作**：

- 用户取消：`TaskService.CancelTask` 走 `UpdateTask`（无条件），因为取消意图必须能被
  持久化——否则异常执行者占满名额时用户无法停止任务。
- 启动恢复：按持久化事实收敛，不重放外部操作（`UpdateTaskIfStatus`）。
- 管理操作：`TaskService` 的读取与删除。

### 2.7 容器重建、任务重试、取消与恢复的代次规则

| 场景 | 容器代次 | 工作区 | Session |
|---|---|---|---|
| 任务重试 / 重新开始 | 创建新容器（`RestartContainer`），旧容器只能显式 `delete` / `detach` | 新容器上重新初始化 | 新 Attempt、新 Session |
| 单步骤重新执行 | 复用同一容器 | 复用同一工作区 | 新 Attempt、新 Session |
| 修复循环下一轮 | 复用同一容器 | 复用同一工作区 | 新 Attempt、新 Session |
| 用户取消 | 保留（等待真实停止） | 保留 | 只读 |
| 服务中断 | 保留（现场未知） | 保留 | 只读；原始文件不删除 |
| 任务成功 | 按策略删除任务级资源 | 随容器删除 | 归档后只读 |

### 2.8 资源删除、脱离管理、保留与核验的准入条件

| 动作 | 准入条件 | 禁止条件 |
|---|---|---|
| 删除容器 | 归属成立、受管理、远程所有权标签核验通过 | 已 detached、归属不成立、所有权标签不一致、状态未知 |
| 脱离管理 | 归属成立、未删除、远程所有权核验通过 | 已删除 |
| 保留容器 | 任何状态 | —（保留永远安全） |
| 孤儿容器 | 只报告 | 不自动删除、不自动重新关联 |
| 状态未知容器 | 保留 `unknown` 并记录原因 | 不收敛为任何确定状态、不删除、不重建 |
| 共享缓存 / 镜像 | 从不自动删除 | 任何情况下都不得因单任务收尾而删除 |

### 2.9 数据库约束、应用层检查与测试证据

| 层 | 承担什么 | 位置 |
|---|---|---|
| 数据库 | 主键、非空、有业务意义的唯一索引、非枚举 CHECK | `migrations/` |
| 领域 | 归属判定、生命周期策略、恢复结论、租约 fencing 判定 | `internal/domain/container_ownership.go`、`container_lifecycle.go`、`container_recovery.go`、`task_lease.go` |
| 应用 | 加载并核验归属、条件写入、拒绝跨代次操作 | `internal/app/task_container.go` 及六个执行入口 |
| 存储 | 单条条件 UPDATE、`RowsAffected` 判定、归属链自洽校验 | `internal/storage/sqlite/` |
| 测试 | 领域矩阵 + 真实 SQLite 集成 | 见 §4 |

**没有新增 ownership 字段**：现有 `containers.task_id`、`containers.host_id`、
`tasks.container_id`、`attempts.container_id`、`sessions.container_id` 已经足以表达
归属关系。本里程碑只增加**条件校验与调用链保护**，不新增重复字段，也不改 Schema。

## 3. M3-2：统一执行入口与任务所有权边界

### 3.1 入口审计结果（本次完成）

| 入口 | 领取任务租约 | 租约覆盖周期 | 备注 |
|---|---|---|---|
| `POST /api/v1/tasks/{id}/start` | ✅ | 整个启动流程，成功后交接给工作流 | P3-M2 |
| `POST /api/v1/tasks/{id}/plan` | ✅ | 整个计划生成 | P3-M2 |
| `POST /api/v1/tasks/{id}/execute-workflow` | ✅ | 整个工作流推进（心跳续租） | P3-M2 |
| `POST /api/v1/tasks/{id}/resume-workflow` | ✅ | 整个工作流推进（心跳续租） | P3-M2 |
| `POST /api/v1/tasks/{id}/execute-steps` | ✅ | 整个顺序执行周期 | **本次新增** |
| `POST .../steps/{id}/execute` | ✅ | 整个单步骤执行 | **本次新增** |
| `POST .../steps/{id}/execute-and-validate` | ✅ | 整个开发—验证闭环 | **本次新增** |
| `POST .../steps/{id}/open-repair-cycle` | ❌（不需要） | — | 只登记预算作用域，不启动 Agent |
| 自动调度（`app.TaskScheduler`） | ✅ | 整个启动流程 | P3-M2 |
| 用户取消（`POST .../cancel`） | ❌（有意） | — | 取消意图必须先落盘 |

### 3.2 职责划分（不能互相替代）

```text
任务租约（tasks.lease_owner + lease_epoch）   ← 保护整个 Task 的执行权
  └─ 步骤占用（steps.current_attempt_id + attempts 事务）  ← 保护单次 Step 的执行登记
```

- **任务租约**回答"这一次由谁推进这个任务"：跨步骤、跨 Attempt，覆盖整个执行周期。
- **步骤占用**回答"这一次由谁执行这个步骤"：单次执行登记，事务内条件写入。
- 两者**都不足以单独保证隔离**：
  - 只有步骤占用 → 单步骤入口与任务级工作流可以同时推进同一任务的不同步骤，
    从而并发修改同一份工作区。
  - 只有任务租约 → 无法表达"同一步骤的重复执行请求"，也无法阻止两次 Attempt
    在同一份现场上交错写入。

### 3.3 执行期状态写入的栅栏

本次把租约栅栏落到三个新位置：

1. **`storage.StepExecutionStartRequest.Lease`**：非空时事务内的任务写入使用
   `UpdateTaskLeaseGuard`（status + owner + epoch），旧代次无法占用步骤。
2. **`StepExecutionStore.UpdateTaskLeaseGuard`**：步骤执行协调器的任务终态写入
   （`finishTask`、`persistCancelledTask`、`persistTaskStatus`）在持有凭据时使用
   租约栅栏写入。
3. **`ExecuteAndValidateRequest.Grant` / `ExecuteStepsRequest.Grant` /
   `StepExecuteRequest.Grant`**：执行权凭据随请求传入，逐层传递到步骤协调器。

### 3.4 旧执行路径的禁止动作

租约失效（被接管 / 过期 / 释放）后，旧执行路径**不得**：

- 启动新的 Agent 调用：`TaskLeaseService.Hold` / `domain.TaskHoldsLease` 判定失败即停止；
- 修改工作区：步骤占用的事务条件更新被拒绝；
- 写入成功终态：`UpdateTaskLeaseGuard` 返回 `storage.ErrConflict`；
- 继续推进工作流：心跳失败即 `cancel(errWorkflowLeaseLost)`。

**明确边界**：新代次写入数据库不等于旧进程中的远程操作已被物理取消。已发出的
`docker exec` / Git 命令是否结束取决于下层执行器，该现场由 P2-M2 的中断语义与
P3-M4 的接管策略处理。

### 3.5 取消请求不被租约阻塞

`TaskService.CancelTask` 使用 `UpdateTask`（无条件）：取消意图必须能被持久化，否则
在执行者异常占满名额时用户将无法停止任务。租约栅栏保护的是**执行期写入**，不是
"用户意图"。

### 3.6 不允许绕过的直接调用

所有执行入口都必须经过 `TaskLeaseService.Claim`；CLI 的 `agentflow task run` 只调用
HTTP 接口并轮询，不在 CLI 进程内装配执行能力。`app.StepExecutor` / `app.StepWorkflowService`
接受的是内部凭据（`*WorkflowExecutionGrant`），HTTP 请求体不解析 `owner` / `epoch`，
因此不存在"调用方可指定所有权"的信任漏洞。

### 3.7 领取冲突的细分

`TaskLeaseService.classifyClaimConflict` 在冲突时做一次只读查询，把结论分为：

- `ErrTaskLeaseNotFound`（任务不存在或已删除）→ HTTP 404；
- `ErrTaskLeaseConflict`（执行权被占用 / 状态不允许）→ HTTP 409。

读取失败时不掩盖原始冲突：无法确认现场时按冲突返回，调用方不会因此认为"任务
不存在"而删除或重建任何东西。

## 4. M3-3：资源回收必须校验所有权

### 4.1 回收场景与本次结论

| 场景 | 结论 | 依据 |
|---|---|---|
| 任务成功 | 删除任务容器与任务级临时资源 | `DecideContainerLifecycle` → `RetentionDelete` |
| 任务失败 | 保留容器与现场 | 同上 → `RetentionKeep` |
| 用户取消 | 等待真实执行停止后按策略处理 | 取消不直接删除容器 |
| 进程中断 | 保留无法确认状态的资源 | 同上 → `RetentionKeep`（默认） |
| 任务重新执行 | 创建新容器；旧容器只能显式 `delete` / `detach` | `PlanContainerRestart` |
| 容器状态未知 | 保留 `unknown`，记录原因，要求人工核验 | `DecideContainerRecovery` |
| 容器已创建但任务关联失败 | 可被发现（`Orphans` 过滤），不自动删除、不自动重新关联 | `ContainerRecoveryService.reportOrphans` |
| Session 已执行但归档记录写入失败 | 保留原始文件；归档状态如实记为失败 | `SessionService.applyArchive` |
| 工作区初始化 pending / running | 启动恢复收敛为 `uncertain`，不重放 Clone | `DecideTaskWorkspaceRecovery` |
| 旧执行者租约失效但远程命令仍在运行 | 停止推进，保留现场，物理收敛属 P3-M4 | P2-M2 §1、§3.4 |

### 4.2 强制原则的落地

1. **先核实身份与归属，再删除或修改**：所有执行入口经 `loadOwnedContainer`；删除经
   `ensureManaged` + `observe`（远程所有权标签核验）。
2. **不能仅凭 `task.ContainerID` 删除远程容器**：必须先读取容器登记记录并核验
   `task_id` / `host_id` / 生命周期状态 / `docker_id`。
3. **不自动删除孤儿容器、不自动重新关联**：`reportOrphans` 只追加事件与日志。
4. **结果不确定时不重试删除、不重建、不重新 Clone**：`uncertain` 必须先只读核验。
5. **不因租约失效声称远程操作已停止**：`blocked` / `recovery_required` 记录不持有
   执行权，也不代表外部操作没发生。
6. **不因一个任务成功删除共享资源**：删除只作用于容器级资源，卷名不含任务标识。
7. **回收失败保留现场与真实错误**：`ReleaseResult.Err` 与 `Detail` 分别表达失败与
   实际处置；`DeleteContainer` 只在容器已不存在时按幂等成功处理。
8. **新增自动回收动作必须有所有权检查与幂等语义**：本次未新增任何自动回收动作。

## 5. 测试与验收

### 5.1 新增测试

| 文件 | 覆盖 |
|---|---|
| `tests/unit/domain/container_ownership_test.go` | 归属判定的 8 个结论：成立 / 跨任务 / 未登记 / 记录不一致 / 无引用 / 节点不一致 / 已脱离与已删除 / 缺 Docker ID |
| `tests/integration/local/scheduler/isolation_test.go` | 两个任务独立容器、跨任务复用被拒绝、工作区记录不跨任务、并发 Session 路径不冲突、归档不影响其他任务、会话归属链不一致被持久化拒绝 |
| `tests/integration/local/scheduler/lease_fencing_test.go` | 单步骤与工作流竞争同一租约、旧代次不能占用步骤、旧代次不能写任务状态、两个任务并发推进、取消不需要租约 |
| `tests/integration/local/scheduler/resource_reclaim_test.go` | 孤儿容器只报告不处置、未知状态不删除、回收一个任务不影响另一个、共享缓存不随任务变化、删除幂等与脱离管理拒绝、失败保留现场、容器状态条件写入 |

这些用例全部使用真实 SQLite 与正式迁移，断言对象是数据库里的真实记录。

### 5.2 既有测试的适配

容器归属核验把"任务只填一个 container_id 字符串"的测试夹具从"可执行现场"变成
"记录缺失"。因此新增共享夹具辅助函数并在各测试包内补齐容器登记记录：

- `tests/unit/app/container_fixture_test.go`（`createTaskContainer`、`seedOwnedContainer`、`containerHostID`）
- `tests/unit/httpapi/container_fixture_test.go`（`seedOwnedContainer`、`containerHostID`）
- `tests/integration/local/workflow/container_fixture_test.go`（`seedOwnedContainer`、`fixtureContainerHostID`）
- `tests/integration/local/fetcher/container_fixture_test.go`（`seedOwnedContainer`、`fixtureContainerHostID`）

这些辅助函数只写入真实容器记录，不改动任何业务断言。

## 6. 实际执行的命令与结果

```text
$ go build ./...
（无输出，成功）

$ go vet ./...
（无输出，无新增问题）

$ gofmt -l internal cmd tests
（无输出，格式正确）

$ go test -count=1 ./tests/unit/...
Go test: 1563 passed in 21 packages

$ go test -count=1 ./tests/integration/local/...
Go test: 383 passed in 14 packages

$ go test -count=1 ./tests/integration/local/scheduler/
Go test: 42 passed in 1 packages

# 真实远程依赖用例（tests/config 中 ssh.enable / docker.remote_enabled / pi.enable 均为 true）
$ go test -count=1 -v ./tests/integration/ssh/
Go test: 6 passed in 1 packages

$ go test -count=1 ./tests/integration/pi/
Go test: 1 passed in 1 packages

$ go test -count=1 ./tests/integration/remote/docker/
Go test: 3 passed in 1 packages

$ go test -count=1 ./tests/integration/remote/codesaver/
Go test: 1 passed in 1 packages

$ go test -count=1 ./tests/integration/remote/agent/
Go test: 2 passed in 1 packages

$ go test -count=1 ./tests/integration/remote/workflow/
Go test: 4 passed in 1 packages

$ go test -count=1 ./tests/...
Go test: 2047 passed in 46 packages
```

- **通过**：`./tests/...` 全部 46 个包、2047 个用例，包含真实远程依赖用例
  （`tests/integration/ssh`、`tests/integration/pi`、
  `tests/integration/remote/{docker,codesaver,agent,workflow}`）。
- **失败**：无（最终状态）。首次运行时 `tests/integration/remote/**` 因远程主机
  `192.168.91.20:22` 不可达而失败；远程服务启动后重新执行，全部通过。
- **跳过**：无（真实 Pi 与真实远程 Docker 均已启用并实际执行）。
- **未执行**：前端测试（`lingting-agent-flow-ui/`），本里程碑不涉及前端改动。
- **无法确认**：无。

`TEST_ARTIFACT_DIR` 由 `testkit.Main` 在每次运行时创建于
`os.TempDir()/lingting-agent-flow-tests/` 下并输出绝对路径，测试产物按仓库约定保留。

## 7. 本里程碑修改的文件

| 文件 | 变更 |
|---|---|
| `internal/domain/container_ownership.go` | **新增**：容器归属契约（`DecideContainerOwnership` / `CheckContainerOwnership` 与 9 个拒绝原因） |
| `internal/domain/task_queue.go` | 新增 `ExplicitStepExecutionStatuses`（单步骤入口与工作流共用准入集合） |
| `internal/app/task_container.go` | **新增**：`loadOwnedContainer`（读取容器记录并核验归属）、`ErrTaskContainerMissing` / `ErrTaskContainerOwnership` |
| `internal/app/task_workspace.go` | `requireContainer` 改为先核验归属，并校验观测到的 Docker ID 与登记一致 |
| `internal/app/code_initialize.go` | `requireContainer` 改为先核验归属 |
| `internal/app/step_executor.go` | `requireContainer` 改为先核验归属；`StepExecuteRequest` 新增 `Grant`；`beginExecution` 传租约；`StepExecutionStore` 新增 `UpdateTaskLeaseGuard` |
| `internal/app/step_executor_run.go` | 任务终态写入改为 `updateTaskGuarded`（持有凭据时附带租约栅栏） |
| `internal/app/validation.go` | `requireContainer` 改为先核验归属；`ValidationStore` 新增 `GetContainer` |
| `internal/app/task_workflow_run.go` | `checkContainerReady` 改为先核验归属 |
| `internal/app/task_workflow.go` | `WorkflowTaskRepository` 新增 `GetContainer` |
| `internal/app/task_workflow_lease.go` | 新增 `leaseOf` / `holdsGrant` / `updateTaskGuarded` |
| `internal/app/task_workflow_exec.go` | 步骤闭环调用传入 `Grant` |
| `internal/app/task_launch_run.go` | `containerUsable` 改为按任务核验归属；归属不成立时返回前置条件错误 |
| `internal/app/task_launch.go` | `TaskLaunchStore` 新增 `GetContainer` |
| `internal/app/task_lease.go` | 新增 `ErrTaskLeaseNotFound` 与 `classifyClaimConflict`（区分 404 与 409） |
| `internal/app/step_sequence.go` | `ExecuteStepsRequest` 新增 `Grant` 并传给每一步 |
| `internal/app/step_workflow.go` | `ExecuteAndValidateRequest` 新增 `Grant` |
| `internal/app/step_workflow_repair.go` | 开发阶段调用传入 `Grant` |
| `internal/storage/step_execution.go` | `StepExecutionStartRequest` 新增 `Lease` |
| `internal/storage/sqlite/step_execution.go` | 任务写入按是否持有租约选择 `UpdateTaskLeaseGuard` / `UpdateTaskIfStatus` |
| `internal/httpapi/step_execution.go` | 新增 `leases` 依赖与 `claimForStepExecution`；`serve` 领取租约；错误映射区分 404 / 409 |
| `internal/httpapi/step_sequence.go` | `serveSteps` 领取租约并传入 `Grant` |
| `internal/httpapi/step_workflow.go` | `serveExecuteAndValidate` 领取租约并传入 `Grant` |
| `internal/httpapi/router.go` | 传入 `deps.TaskLeases` |
| `internal/httpapi/task_launch.go` / `workflow.go` | 错误映射补充 `ErrTaskLeaseNotFound` / `ErrTaskLeaseConflict` |
| `tests/unit/domain/container_ownership_test.go` | **新增** |
| `tests/integration/local/scheduler/isolation_test.go` | **新增** |
| `tests/integration/local/scheduler/lease_fencing_test.go` | **新增** |
| `tests/integration/local/scheduler/resource_reclaim_test.go` | **新增** |
| `tests/unit/app/container_fixture_test.go`、`tests/unit/httpapi/container_fixture_test.go`、`tests/integration/local/{workflow,fetcher}/container_fixture_test.go` | **新增**：容器登记记录夹具辅助 |
| 既有测试夹具 | 补齐容器登记记录（不改动任何业务断言） |

## 8. 完成定义核对

| 条件 | 状态 | 证据 |
|---|---|---|
| Task / Container / Workspace / Attempt / Session 归属关系明确且一致 | ✅ | §2.1、§2.2；`DecideContainerOwnership`；`TestDecideContainerOwnership*`（8 个用例） |
| 跨任务可写资源隔离有代码与测试证明 | ✅ | §2.3；`TestTwoTasksUseIndependentContainers`、`TestTaskContainerCannotBeReusedByAnotherTask`、`TestWorkspaceInitRecordsDoNotCrossTasks` |
| 共享资源边界明确，任务回收不误删共享资源 | ✅ | §2.5；`TestSharedCacheVolumesAreNotTaskScoped`、`TestRecoveringOneTaskKeepsAnotherTaskContainer` |
| 单步骤入口不再绕过任务级执行权保护 | ✅ | §3.1、§3.3；`TestSingleStepAndWorkflowCompeteForSameLease` |
| 旧租约执行者不能继续推进或修改新代次状态 | ✅ | §3.3、§3.4；`TestStaleLeaseCannotOccupyStep`、`TestStaleLeaseCannotWriteTaskStatus` |
| 容器未知状态、孤儿容器、工作区中断与 Session 归档失败遵守安全恢复规则 | ✅ | §4.1；`TestOrphanContainerIsDiscoverableButNotAutoHandled`、`TestUnknownContainerStateIsNotDeleted`、`TestContainerUpdateUsesStateCondition` |
| 真实 SQLite 集成测试覆盖关键隔离与并发场景 | ✅ | §5.1（4 个新增文件、15 个新增集成用例 + 8 个领域用例） |
| 路线图与代码事实一致，未解决风险均有归属 | ✅ | 本文件与 `.steps/README.md`、`ROADMAP.md`、`P3-multi-task-scheduling.md` |
| 测试结果如实记录 | ✅ | §6 |

## 9. 未完成事项与剩余风险

### 9.1 明确不在本里程碑范围

- 调度器重启后的过期租约接管（P3-M4）。
- 多任务并发与资源回收的系统性验收（P3-M5）。
- 跨进程全局并发配额（需要数据库仲裁）。
- `failed` 任务的用户显式重试入口。
- 真实远程 Docker 上的孤儿容器与 `unknown` 现场**主动注入**验收（正常路径已由
  `tests/integration/remote/**` 覆盖）。
- 缓存优化、镜像优化、调度性能重构。

### 9.2 剩余风险

| 编号 | 风险 | 影响 | 缓解 / 下一步 |
|---|---|---|---|
| R1 | 归属校验依赖数据库里的容器记录 | 记录被人工改成别的任务时，校验会拒绝执行而不是纠正 | 拒绝是正确行为：现场必须人工核验（§4.2 第 1 条） |
| R2 | `loadOwnedContainer` 在无存储能力时无法核验 | 测试替身必须实现 `GetContainer` | 生产代码始终使用真实存储；替身已在夹具中补齐 |
| R3 | 单步骤入口领取的是任务租约，与工作流共用同一份令牌 | 同一任务的单步骤与工作流不能并行 | 这是有意的：两者修改同一份工作区，并行会互相覆盖 |
| R4 | 租约栅栏只保护数据库写入 | 已发出的远程命令是否停止取决于下层执行器 | 心跳失败即停止推进；物理收敛属 P3-M4 |
| R5 | 真实远程环境已跑通（`remote/{docker,codesaver,agent,workflow}` 全部通过），但孤儿容器与 `unknown` 现场仍未在真实远程环境主动注入 | 远程侧处置逻辑在"正常路径"上已验证，"异常注入"路径仅由本机真实 SQLite 用例覆盖 | 注入式远程验收归 P3-M5 |
| R6 | 缓存卷的并发写入安全依赖"缓存工具自身支持并发" | 同一节点上两个任务的依赖下载可能竞争同一缓存 | 缓存卷只挂载依赖缓存目录，不与代码目录重叠；共享范围由 ABI 约束 |

## 10. 下一步

P3-M4 —— 处理任务取消、失败、恢复和调度器重启：

- 过期租约的接管策略与物理收敛；
- 调度器重启后的残留收敛；
- 取消 / 失败 / 恢复的端到端语义；
- `failed` 任务的显式重试入口（独立契约）。

本里程碑交付的容器归属契约、租约栅栏与共享资源边界是 P3-M4 的前提，不需要重新设计。
