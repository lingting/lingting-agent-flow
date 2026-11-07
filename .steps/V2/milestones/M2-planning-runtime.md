# M2：规划运行环境

> 状态：**pending**。
> 前置：M1 completed。参考：[`../ARCHITECTURE.md`](../ARCHITECTURE.md) §4、
> [`../DATA-MODEL.md`](../DATA-MODEL.md) §4、[`../ROLE-DESIGN.md`](../ROLE-DESIGN.md) §7。

## 1. 目标

为需求规划提供**可复用、只读、可恢复**的运行环境：

1. 一个 PlanningSession 对应一个独立规划容器。
2. 项目代码以固定 commit SHA 的只读快照进入容器。
3. 代码目录只读、rootfs 只读、唯一可写目录是 Session / 临时目录。
4. 多轮规划复用同一容器、同一快照，两个角色各自保留 Pi Session 上下文。
5. 会话确认 / 放弃 / 失败后释放容器与快照卷，保留消息、分析、方案历史。

## 2. 范围

### 2.1 执行环境能力（`internal/execenv` + `internal/docker`）

- `execenv` 增加只读工作区语义（代码目录只读、可写目录显式声明），未支持的类型继续返回
  `ErrUnsupportedKind`。
- `internal/docker` 增加 `--read-only`、`--tmpfs`、`--mount ...,readonly` 的构造能力。
- 同步 `docker.RequiredCLICommands`，保证节点预检覆盖新增参数。

### 2.2 App

- `PlanningRuntimeService`：准备（准备容器克隆到快照卷）→ 启动规划容器 → 复用 → 释放。
- `PlanningSessionService`：会话创建、状态推进、容器/Session 关联、结束与清理。
- 两个角色各一条 Pi Session 路径，由服务端生成并校验落在 Session 根目录内。

### 2.3 持久化

- 初始迁移（`internal/storage/sqlite/migrations/00001_initial_schema.sql`）中的 `planning_sessions` /
  `planning_messages` /
  `planning_revisions`：已实现（M2 第二阶段），表结构、索引与序号/版本号唯一约束见
  [`../DATA-MODEL.md`](../DATA-MODEL.md) §2。
- `projects.default_host_id`（项目默认执行节点）：已实现（M2 第八阶段），字段、部分索引与 NULL 语义见
  [`../DATA-MODEL.md`](../DATA-MODEL.md) §2。
- 规划会话的互斥不是数据库事实：`planning_session_leases`（M2 第九阶段引入）已随单 Server 架构删除，
  `RunRole` / `Prepare` / `Release` 由进程内 `app.PlanningSessionCoordinator` 串行化，见
  [`../DATA-MODEL.md`](../DATA-MODEL.md) §2 与 [architecture.md](../../docs/architecture.md)。

### 2.4 生命周期

- 规划运行时作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`；
  服务停止时取消在途调用并等待收敛，不留不受管理的容器与子进程。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 同一规划会话多轮复用同一容器 | 多轮请求后 `planning_sessions.container_id` 不变，快照卷不变 |
| 不同规划会话互相隔离 | 会话之间容器、卷、Session 文件均不同 |
| AI 能读取真实项目代码 | 规划调用中读到实际仓库文件内容 |
| 所有代码写入尝试被运行环境拒绝 | 文件工具与 Shell 两条路径写入均失败，且无提交记录 |
| Session 与容器生命周期可持久化恢复 | 重启后会话状态与容器关联仍可读，过期会话可被收敛 |
| 规划容器不进入提交流程 | 规划路径不调用任何 code save / push / merge 服务 |

## 4. 验收方式（默认不自动执行）

- 阅读代码：只读挂载参数构造、`--read-only` 与 tmpfs 的构造点、`RequiredCLICommands` 同步、
  Session 路径生成与根目录校验、释放路径、生命周期注册顺序。
- 实际写入尝试（若用户授权在真实环境验证）：文件写入与 `sh -c 'echo > 文件'` 都必须失败。
- 若用户明确要求测试，覆盖：只读参数构造、会话隔离、容器复用、释放与收敛、失败诊断。

## 5. 风险与待决

1. **只读方案的实现成本**：方案 A（准备容器 + 只读卷）需要两阶段准备与快照卷管理；
   必须在 M2 开始时确认真实 Docker 29.0.0 支持所用的参数组合。
2. **克隆到卷的失败恢复**：准备容器失败时不得留下"已登记但不存在"的容器或卷引用。
3. **长会话资源占用**：闲置规划会话的容器是否回收（保留到显式放弃 vs 超时回收），M2 定稿。
4. **Session 归档**：规划 Session 归档沿用 V1 能力，需确认归档路径不落在项目代码目录内。

## 6. 明确不做

- 不做需求分析、方案生成业务（M3）。
- 不创建正式 Requirement / Task（M4）。
- 不实现"用 Prompt 让模型别写代码"作为唯一限制。
- 不把规划会话写进 Task 的 Step / Attempt 执行记录。

## 7. 阶段进展

### 7.1 已交付：只读容器底层能力（M2 第一阶段）

只读执行环境的**底层表达与 Docker 创建参数**已落地，暂无业务调用方：

- `execenv`（`internal/execenv/workspace.go`）：项目代码访问模式 `ProjectAccess`
  （`read_write` / `read_only`）、只读工作区构造函数、显式可写目录与路径关系校验。
- `docker`（`internal/docker/container.go`）：`CreateContainerSpec.ReadOnlyRootFS`
  （`--read-only`）、`CodeVolumes`（`--mount type=volume,...,readonly`）、`Tmpfs`（`--tmpfs`）
  与对应的参数校验。
- `app`（`internal/app/containers.go`、`internal/app/containers_planning.go`）：
  `CreateContainerRequest.Planning` 只读规划容器参数，把只读工作区语义转换为 Docker 创建参数。
- 预检（`internal/docker/clihelp.go`）：`docker create` 必需参数清单新增 `--read-only` 与 `--tmpfs`。

约束与语义：

- 项目代码目录只读由结构化字段表达，不依赖 Prompt 或 Agent 工具白名单。
- 只读工作区只允许显式声明的可写目录，且该目录不得与项目代码目录互相包含；Session 目录必须
  由声明的可写目录覆盖，否则校验失败。
- 只读工作区不接受缓存卷；只读容器创建参数同样拒绝可写缓存卷，因此规划容器无法通过缓存挂载
  获得项目代码写权限。
- 只读参数只在 `CreateContainerRequest.Planning` 非空时生效：普通任务容器仍是可写 rootfs、可写
  项目目录与既有缓存卷参数，命令行与 HTTP 接口都不暴露该字段。
- 未引入 privileged、Docker Socket、host network、host PID、宿主机目录挂载与额外提权参数；
  Docker CLI / Engine / API 基线未变（CLI 29.0.0 / Engine 29.0.0 / API 1.52）。

### 7.2 已交付：规划会话领域模型与持久化（M2 第二阶段）

规划会话的三类对象及其存储能力已落地，**暂无业务调用方**：

- `internal/domain/planning_session.go`：`PlanningSessionStatus`（`active` / `analyzing` /
  `preview_ready` / `confirmed` / `abandoned` / `failed`）与状态机、`PlanningSession` 实体、
  `Transition` 与语义迁移方法（`BeginAnalysis` / `RetryAnalysis` / `MarkPreviewReady` /
  `Confirm` / `Abandon` / `Fail`）、运行期绑定（执行节点、容器、固定代码 commit、两个角色的
  Pi 会话标识）。
- `internal/domain/planning_message.go`：`PlanningSenderType`（`user` / `requirement_analyst` /
  `planner` / `system`）与 `PlanningMessage`。
- `internal/domain/planning_revision.go`：`PlanningRevision`；分析与方案以 JSON 快照保存，只做结构
  校验，不解析内部字段，也不定义任务拆分规则。
- `internal/storage`：`PlanningSessionStore`（创建、更新、按 ID 查询、按项目列表、追加与查询消息、
  追加与查询方案版本），已并入 `Tx` 与 `Store`。
- `internal/storage/sqlite`：初始迁移中的三张规划表与四个索引，以及仓储实现；消息
  序号与方案版本号在写入事务内分配。

约束与语义：

- 状态迁移只在 `domain` 定义：`confirmed` / `abandoned` 不可重新打开，`failed` 只能经显式
  `RetryAnalysis` 回到 `analyzing`，追加消息不得隐式重置失败结论。
- 规划会话独立于 Task / Step / Attempt / Session 执行记录：不写执行记录表，也不进入代码保存路径。
- 表不声明外键、不设枚举取值白名单；`status` 与 `sender_type` 的未知取值必须能写入并原样读回。
- 消息序号与方案版本号分别由会话 ID + 序号唯一索引兜底；序号分配与插入在同一事务内完成，不
  "先查最大值再插入"。
- 会话记录保存执行节点、规划容器、固定代码 commit 与两个角色的 Pi 会话标识，供后续运行时恢复。

本阶段只交付持久化模型：当时的规划运行时、会话业务编排、Pi Session 路径与 HTTP API 均未实现，后续
阶段的交付见 §7.3。

### 7.3 已交付：规划运行时服务（M2 第三阶段）

`app.PlanningRuntimeService` 已把规划会话与只读 Docker 底座连接起来，提供 `Prepare` 与 `Release` 两个操作。
它仍没有 HTTP / CLI 入口，也不执行任何 AI 对话：

- **固定 commit 快照**：按 `Project.RepositoryURL` 与 `BaseBranch` 在一次性准备容器内 `git clone`，把
  基础分支解析为完整 commit SHA，`checkout --detach` 到该 SHA，并在可写卷上核验 `rev-parse HEAD`、
  detached HEAD 与干净工作区；持久化的是核验后的 SHA，不是分支名、标签或调用方输入。
- **快照卷**：名称由服务端按 `agentflow-plan-<规划会话 ID>-<执行节点 ID>` 生成，挂载到项目工作目录。
  卷带 `agentflow.managed`、`agentflow.host_id`、`agentflow.planning_session_id` 标签；复用与删除前
  都必须核验名称与这三个标签，不共享不同会话的卷，也不删除名称相似但不属于本会话的卷。
- **一次性容器**：准备容器以可写方式挂载快照卷，按需启动、执行、删除，不登记为受管理容器，也不写入
  `planning_sessions.container_id`；只读复核使用 `docker run --rm` 的一次性容器，只读挂载同一个卷。
- **正式规划容器**：经 `ContainerService.CreateContainer` 与 `PlanningContainerSpec` 创建，使用
  `--read-only`、代码卷只读挂载、`/tmp` 作为唯一显式可写 tmpfs（覆盖 `/tmp/agentflow/sessions`），
  不挂缓存卷、不挂 Docker Socket、不使用特权 / host PID / host network 参数；容器额外带规划会话标签，
  复用时核验标签与登记记录。
- **持久化顺序**：产生远程副作用前先写入 `HostID`；快照核验通过后写入 `SourceCommit`；容器启动并通过
  所有权核验后才写入 `ContainerID`。任何一步失败都不会写入假的容器 ID，也不改变规划会话状态。
- **复用与现场一致性**：已记录 `SourceCommit` 的会话只使用同一 SHA；快照卷缺失、实际 SHA 不一致或
  登记的容器在远程已不存在时返回明确冲突，不重新 Clone、不创建第二个容器覆盖记录；只有在容器已停止且
  仍受管理时才重新启动。
- **释放**：先删除并确认规划容器，再按标签删除本会话快照卷，最后清空运行时绑定。容器已删除、卷已不
  存在时重复释放是幂等的；删除失败或归属不成立时保留 `HostID` / `ContainerID` / `SourceCommit`。
- **不确定结果**：远程命令超时、取消或连接中断时保留快照卷与准备容器现场，返回带阶段的
  `ErrPlanningRuntimeUncertain`，不自动重试，也不盲目删除可能仍在使用的资源。
- **Docker 能力**：`internal/docker` 新增 `docker run --detach` 一次性容器、`docker run --rm` 一次性
  执行、`docker volume inspect` / `docker volume rm` 与规划会话标签，并同步 `RequiredCLICommands`。

第三阶段仍未提供 `PlanningSessionService` 业务编排（已在 §7.4 交付）、两个角色的 Pi Session 路径与多轮
上下文、生命周期注册、HTTP API、前端工作台与需求 / 方案结构化解析；真实 Docker / SSH 环境下的资源创建、
只读挂载与释放均未执行。

### 7.4 已交付：规划会话业务编排（M2 第四阶段）

`app.PlanningSessionService`（`internal/app/planning_session*.go`）把 §7.2 的领域状态机与
SQLite 持久化连接成统一入口。它仍**没有 Pi 调用方、HTTP 入口与 CLI 入口**，也不接触远程资源：

- **会话创建**（`Create`）：按领域书写规则校验并小写归一项目标识，读取项目并要求未被逻辑删除，
  再用 `domain.NewPlanningSession` 创建 `active` 会话。会话标识、初始状态、创建时间与运行期字段
  （执行节点、容器、固定 commit、两个角色的 Pi 会话标识）全部由服务端生成，调用方无法指定。
- **会话查询**（`Get` / `ListByProject` / `Messages` / `Revisions`）：读取路径不调用领域校验、不做
  状态或发送方映射，数据库中的未知取值原样返回；会话不存在返回 `ErrPlanningSessionNotFound` 而不是
  空列表。按项目列表前先确认项目存在且未删除，避免把已下线项目的会话当作可用结果。
- **提交消息与启动分析**（`SubmitMessage`）：单个事务内依次读取会话 → `CanAcceptMessage` 准入
  （只有 `active` 与 `preview_ready`）→ 追加 `user` 消息 → `BeginAnalysis` → 以读到的状态为前置
  条件写回。消息与状态同事务，任一步失败都整体回滚；`failed` 会话不会被普通消息隐式重置。
- **显式重跑分析**（`RetryAnalysis`）：`failed` 的唯一出口，调用 `domain.RetryAnalysis`；可选的
  用户补充消息与状态迁移在同一事务内完成。
- **分析完成**（`CompleteAnalysis`）：单个事务内确认 `analyzing` → 追加 Revision（版本号由存储层
  分配）→ 追加本轮角色消息 → `MarkPreviewReady` → 以 `analyzing` 为前置条件写回。角色消息的发送方
  只允许 `requirement_analyst` 与 `planner`（判定在 `domain.PlanningSenderType.IsPlanningRole`），
  用户与系统消息不能在这里伪造；`user` 消息只能经提交入口写入。
- **分析失败**（`FailAnalysis`）：确认 `analyzing` → `domain.Fail`。摘要先经
  `domain.ClipFeedbackText` 脱敏并按 `domain.MaxFeedbackReason` 裁剪，不保存凭据或完整日志；失败
  不写 Revision、不写成功消息，状态保留为 `failed`。
- **确认方案**（`Confirm`）：校验目标 Revision 确实属于该会话（按会话读取版本列表，跨会话与不存在
  返回同一错误）→ `domain.Confirm` → 以 `preview_ready` 为前置条件写入状态与 `adopted_revision_id`。
  确认只表示采纳该版规划：不创建 Requirement，也不创建 Task。
- **放弃会话**（`Abandon`）：`domain.Abandon` + 以读到的状态为前置条件写入；消息、Revision 与运行期
  绑定全部保留，不删除任何记录，`analyzing` 期间是否允许放弃完全由领域状态机决定。

并发与事务边界：

- 全部写操作都在 `storage.Store.WithTx` 内完成，读写共享同一事务；DSN 的 `_txlock=immediate`
  让写事务串行，条件更新使用 `UpdatePlanningSessionIfStatus`。影响行数为 0 即 `storage.ErrConflict`，
  映射为 `ErrPlanningSessionStateConflict`，不会被报告为成功。
- 因此同一会话不能同时启动两轮分析，两个并发完成请求不会互相覆盖状态，两个并发确认请求不可能采纳
  不同 Revision，已确认或已放弃的会话也不会被迟到结果重新打开。
- 没有引入进程内互斥锁、队列或第二套任务引擎；消息序号与方案版本号仍由存储层在写入事务内分配，本层
  不传序号与版本号。
- 领域只补了一个纯判定 `domain.PlanningSenderType.IsPlanningRole`；结构化分析与实施方案仍只做 JSON
  结构校验，内部字段语义（`WORKFLOW.md` §2 / §3）与任务拆分规则属于 M3 / M4，本阶段不解析、不建列。

边界：本阶段不启动 Pi、SSH 或 Docker，不准备、复用或释放规划容器与快照卷，也不把远程 Docker 操作
放进数据库事务。终态会话与远程资源释放之间的组合仍不在本服务内，服务操作不会被报告为"资源已释放"。

### 7.5 已交付：运行时绑定并发覆盖修复（M2 第五阶段）

`PlanningRuntimeService` 保存运行时绑定不再使用整行更新，运行时字段与会话业务状态因此可以安全并存：

- **专用存储能力**：`storage.PlanningSessionStore.UpdatePlanningSessionRuntimeBinding`
  （`internal/storage/sqlite/planning_sessions.go`）只更新 `host_id`、`container_id`、`source_commit`
  与 `updated_at`。`status`、`finished_at`、`adopted_revision_id`、`error_summary`、
  `analyst_session_id`、`planner_session_id` 与 `project_id` 完全不在 `SET` 子句中：运行时写入因此
  不可能把 `confirmed` 改回 `active`，也不会清空已保存的 Revision、两个角色的 Pi 会话标识或失败摘要。
- **比较并交换**：接口输入"调用方读到的预期绑定"（`storage.PlanningSessionRuntimeBinding`）与"准备
  写入的新绑定"。`WHERE` 同时校验会话 ID 与三个绑定列，并用 `COALESCE(..., '')` 做 NULL 安全比较——空列
  与空值都归一为空字符串，未绑定状态才能与读到的空值正确匹配。影响行数为 0 时，会话不存在返回
  `storage.ErrNotFound`，绑定已被其他操作改写返回 `storage.ErrConflict`（附带数据库中的当前绑定），
  不会退化成一条无条件的部分字段覆盖。
- **`persistRuntime` 的新形式**：`Prepare` 的三个持久化阶段（执行节点、固定 commit、容器登记）与
  `Release` 的清空绑定都走同一个入口。它在 `storage.Store.WithTx` 内先按传入的 `storage.Tx` 重新读取
  会话，用这份最新副本调用 `domain.BindRuntime`，再以调用方读到的旧绑定为条件提交更新；返回的会话对象
  因此带着数据库当前的业务状态与最新运行时字段，不会把并发推进的状态、方案版本或角色会话标识丢掉。
  清空绑定不受会话是否结束的限制（已确认或已放弃的会话同样需要释放运行时资源），绑定新资源则拒绝已结束
  的会话：确认或放弃之后不得再把运行时资源绑回去；`failed` 也不会被顺手改成其他状态。
- **错误映射**：绑定冲突按 `ErrPlanningRuntimeConflict` 返回，并保留失败阶段（`persist_host` /
  `persist_snapshot` / `persist_container` / `clear_binding`）。`storage.ErrConflict` 属于"不能用旧快照
  覆盖新事实"，不再被报告为 `ErrPlanningRuntimeNotPersisted`；会话在写入过程中消失时仍是
  `ErrPlanningRuntimeNotFound`。
- 新能力同时由 `dbStore` 与 `txStore` 提供（两者组合同一个 `planningSessionStore`）；没有新增 Schema
  变更，规划会话表结构未改动，`app` 层不含 SQL。`PlanningSessionService` 的业务行为、领域状态机
  与 `UpdatePlanningSessionIfStatus` 的条件语义都未改动。

本阶段解决的只是"运行时字段与业务字段相互覆盖"。`PlanningRuntimeService` 的 `keyedMutex` 仍只保证当前
进程内的同会话串行；不同进程对同一规划会话发起 `Prepare` / `Release` 时，运行时绑定由数据库条件更新拒绝
过期写入，但远程资源操作本身仍没有跨进程租约，`Release` 也不检查在途规划调用。（单 Server 架构确认后，
进程内串行就是唯一的协调机制，上述"仍没有跨进程租约"不再是缺口，见 §7.13。）

### 7.6 已交付：规划角色 Pi Session 持久化与多轮上下文复用（M2 第六阶段）

两个固定规划角色现在各自拥有独立、稳定、可复用的 Pi Session，且上下文恢复不再依赖规划容器 tmpfs 是否
存活。本阶段只打通运行基础设施，**没有**实现需求语义解析、任务拆分与方案业务规则：

- **角色会话身份**：`app.PlanningAgentService`（`internal/app/planning_agent*.go`）在首次准备角色
  会话时用 `domain.NewID` 为 `requirement_analyst` 与 `planner` 各生成一个标识，通过
  `domain.PlanningSession.BindRoleSessions` 成对写入；`domain` 拒绝两个角色得到相同标识。后续多轮
  只复用已绑定的值，重新初始化、重新准备运行环境或角色配置变化都不会覆盖它们。标识由服务端生成，
  调用方无法指定。
- **安全持久化**：新增 `storage.PlanningSessionStore.UpdatePlanningSessionRoleBindings`
  （`internal/storage/sqlite/planning_session_roles.go`），只写 `analyst_session_id`、
  `planner_session_id` 与 `updated_at`，`WHERE` 用 `COALESCE(..., '')` 对两个列做 NULL 安全比较；
  会话不存在返回 `storage.ErrNotFound`，绑定已被改写返回 `storage.ErrConflict`。写入在事务内先重新
  读取会话再条件更新：并发初始化只有一个写入获胜，另一个重新读取并复用获胜结果，不会退化成无条件覆盖，
  也不会改写状态、结束时间、采纳的 Revision、失败摘要与运行时绑定。`dbStore` 与 `txStore` 都提供该
  能力；没有新增 Schema 变更，规划会话表结构未改动。
- **稳定路径**：容器内固定文件为 `/tmp/agentflow/sessions/<规划会话标识>/<角色会话标识>/session.jsonl`
  （POSIX `path` 生成，位于规划容器的 Session 根目录下）；本机持久归档为
  `<SessionRoot>/planning/<规划会话标识>/<角色会话标识>/session.jsonl`（`filepath` 生成，落在配置
  的数据目录下，不进入项目代码目录）。两个角色互相隔离，不同规划会话互相隔离，同一角色的多轮调用共用
  同一个文件；路径片段拒绝空值、`.`、`..` 与不安全字符。
- **Runner 扩展**：`agent.RunRequest.StableSession`（`ContainerPath` + `ArchiveDir`）表达稳定位置，
  `pi.ContainerRunner` 校验容器路径位于容器 Session 根目录内且已规范化、本机归档目录位于 Session 根
  目录内，并保证稳定文件名为 `session.jsonl`。不提供该字段时行为与之前完全一致：普通任务仍按
  `InvocationID` 分层得到独立 Session 文件，V1 的独立调用语义不变。每轮仍生成独立 `InvocationID`
  用于事件、日志与单轮归档，稳定 Session 标识与调用标识不混用；规划调用不使用 `--no-session`，模型、
  工具白名单、`--no-mcp`、`--no-extensions` 等受管参数保持原有策略。
- **恢复流程**：每轮调用 Pi 之前检查本机归档。归档不存在表示首轮，允许 Pi 新建 Session；存在则先用现有
  `validateSessionFile` 做结构校验，再经"本机 → 远程宿主机
  `/tmp/agentflow/sessions/<容器 ID>/<调用标识>/restore/` → 容器内固定路径"三段传输恢复
  （复用 `sshclient.FileTransfer.Upload` 与新增的 `docker.Runner.CopyToContainer`）。归档损坏、
  超过大小上限、传输失败或执行器不支持写入时返回明确错误，本轮不启动 Pi，也不会静默丢弃历史上下文。
- **归档判定**：Pi 结束后仍按原顺序导出 HTML 并把原始 JSONL 复制到按调用标识分层的单轮归档；只有"本轮
  执行正常完成 + 单轮归档通过结构校验"才用同目录临时文件加原子重命名更新可恢复快照。失败、超时、取消或
  残缺的记录只保留单轮归档，不覆盖最后一份完整历史；归档写入后再次校验，未确认落盘即视为归档失败。归档
  失败不改变 Pi 的执行状态，而是通过 `agent.ErrSessionArchive` 与 `RunResult.StableSession.Error`
  单独上报，不伪装成 Session 保存成功。远程中转副本只在本机文件可靠落盘（恢复时为本机归档已校验通过）
  之后清理。
- **文件传输能力**：`agent.ContainerFileWriter` 是把本机文件写入容器内的可选能力（上传 + `docker cp`
  入容器），由 `containerSessionExecutor` 实现；`internal/docker` 新增 `CopyToContainer`，命令仍经
  `dockerCommand` 构造，`docker cp` 已在 `RequiredCLICommands` 中，无需新增预检参数。远程中转目录固定
  在 `/tmp/agentflow/sessions/` 下按容器与调用标识隔离；不把 Docker Socket、宿主机项目目录或私钥带入
  规划容器。
- **应用服务**：`PlanningAgentService.RunRole` 是单角色调用入口，只接受规划会话、角色类型
  （`requirement_analyst` / `planner`）、提示词与可选的执行节点，不接受 Session 路径、Pi 参数、工具
  列表、模型或容器标识。它要求会话处于 `analyzing`、项目未删除、角色配置来自 `RoleService` 且已启用、
  类型匹配、模型非空；容器必须是本会话登记的规划容器、属于会话的执行节点、带本会话的
  `agentflow.planning_session_id` 标签且正在运行，未准备时只复用 `PlanningRuntimeService.Prepare`
  （不创建第二个容器，也不接管 detached 容器）。同一规划会话的规划调用在本进程内串行，避免两个调用
  同时读写同一份 Pi JSONL。返回真实 `agent.RunResult`，不伪造成功。
- **工具策略**：规划角色只提交角色类型与 `ToolsPolicy`，由 `domain.EffectivePiTools` 计算实际工具，
  `RoleToolCeiling` 保证两个角色只读；不使用开发者权限兜底，也不因配置缺失、禁用或模型为空回退到其他
  角色或 Pi 默认模型。

### 7.7 已交付：规划服务装配与生命周期管理（M2 第七阶段）

三个规划服务现在由 `agentflow server` 的组合入口构造，规划 Agent 调用有了完整的生命周期边界。本阶段
只做装配与生命周期，**没有**实现 M3 的规划轮次编排：

- **构造位置与依赖关系**（`internal/server/server.go` 的 `planningServices`，在 `startServices`
  中、角色服务就绪之后调用一次）：
  - `app.PlanningSessionService`：`app.NewPlanningSessionService` 传入当前 Server 已打开的
    `storage.Store`；不另开数据库连接，也不新建第二套会话存储。
  - `app.PlanningRuntimeService`：`app.NewPlanningRuntimeService(containerService)`，复用当前
    Server 的 `ContainerService`（由 `containerObservation` 建立）；不在 Server 中复制 `Prepare` /
    `Release`，也不另建 Docker / SSH / SFTP 调用链。
  - `app.PlanningAgentService`：`app.NewPlanningAgentService` 的 `Store`、`Roles`、`Containers`、
    `Runtime` 全部来自当前 Server 的既有实例，`SessionRoot` 取 `app.SessionRoot(s.cfg)`
    （与 `SessionService` 同一来源的本次启动生效配置，不从请求、默认值或重新读取的配置文件生成）。
  - 三个实例保存在 `Server` 上供 M3 装配复用；本阶段不新增 HTTP 路由，也不把服务暴露给 Handler。
- **降级边界**：SSH 或 `ContainerService` 不可用时只构造 `PlanningSessionService`，规划运行时与规划
  Agent 保持为空——规划能力整体不可用，不伪造运行环境，也不回退到本机执行；原有任务执行路径与服务
  装配不受影响（普通任务容器参数、Step / Workflow / Scheduler 注册顺序均未改动）。
- **生命周期状态**：`PlanningAgentService` 直接实现 `lifecycle.Service`（`internal/app` 不反向依赖
  `internal/lifecycle`），状态区分"尚未启动 / 正常运行 / 停止中 / 已停止"。`Start` 校验传入上下文、
  拒绝重复启动（不覆盖已建立的取消信号），不启动任何后台循环；`Stop` 在同一临界区内切到"已停止"后
  取消生命周期上下文并等待收敛，重复调用安全。
- **准入与取消传播**：`RunRole` 先执行 `beginCall`，准入判断与 `runs.Add` 在同一把锁内完成，因此
  `Stop` 的 `Wait` 开始之后不可能再有新的 `Add`，停止边界之后不会启动任何新的 Pi 调用。每次准入生成
  一个 `context.WithCancelCause(调用方 ctx)`，生命周期取消通过它传播到 `ContainerService.RunAgent`
  的 `ctx`，最终到达 Pi 执行与 Session 归档路径；`defer finish()` 保证所有返回路径都释放在途计数
  并 `Done`。按规划会话的 `keyedMutex` 串行化保持不变（仍是单进程内串行，不是跨进程锁）。
- **Stop 的等待范围**：等待的是 `RunRole` 完整返回，包含 Runner 的 Session 提取与归档，而不只是 Pi
  主进程退出；等待同时响应传入 `Stop` 的取消与 deadline（deadline 早于默认 30s 时只用它自己的取消
  信号，避免两个几乎同时到期的计时器把"取消"随机报告成"超时"）。停止超时或未收敛时返回真实错误，
  不报告成功。`Stop` 不释放规划容器、不删除快照卷、不删除 Session 归档。
- **Resource / 恢复边界**：Server 停止与重启都不删除需要恢复的规划容器、快照卷、角色 Pi Session 标识
  与本机归档；启动不重新 Clone，停止不无条件调用 `PlanningRuntimeService.Release`，单次请求取消也不
  清理远程资源。
- **生命周期注册**：`planningAgent` 在后台服务列表中最后注册，因此停止顺序反转为最先执行——先在停止
  边界上拒绝新的规划调用并收敛在途调用，再停止其他后台服务；它依赖的存储、容器服务与角色服务都不是
  lifecycle 服务，数据库在 `manager.Stop` 之后才关闭。整体关闭顺序仍是 HTTP → 后台服务 → 数据库，
  启动失败继续由 Manager 逆序清理；`lifecycle.Manager` 本身未改动。
- **测试缝隙**：新增窄端口 `app.PlanningAgentContainerPort`（`InspectContainer` + `RunAgent`），由
  `*app.ContainerService` 满足，`PlanningAgentServiceOptions.Containers` 改为该端口，与
  `app.StepExecutionContainerPort` 等既有写法一致；没有引入新的依赖注入框架，也没有改变运行时语义。
- **本阶段测试**：新增 `tests/unit/app/planningagent`，覆盖"未启动拒绝 / 启动后可进入执行 / 停止后
  拒绝 / 在途调用收到取消并收敛 / 停止等待收敛不提前返回"五条业务场景，以及"准入与停止竞争 /
  多调用并发停止 / 并发 Stop 安全 / 停止超时返回真实错误"四条并发场景。取消传播用例断言的是传给
  `RunAgent` 的 `ctx` 确实被取消，而不只是生命周期字段变化。实跑命令与结果见 §7.11。

### 7.8 已交付：项目默认执行节点配置（M2 第八阶段）

项目现在把"默认执行节点"作为持久化配置保存，并从既有项目管理入口完成创建、读取、修改与清除。本阶段
不涉及任何规划轮次编排，也没有新增规划工作台：

- **持久化**：`internal/storage/sqlite/migrations/00001_initial_schema.sql` 的 `projects` 表增加
  `default_host_id TEXT`（可空）与部分索引 `idx_projects_default_host (default_host_id)
  WHERE default_host_id IS NOT NULL`。可空表示"未设置"，写入时由存储层把空值落成 SQL NULL；
  不为 NULL 行建立索引项，也不声明外键或枚举白名单。历史任务继续使用自己的 `tasks.host_id` 与
  `config_json` 快照，项目字段变化不改写任何历史事实。
- **领域**：`domain.Project` / `domain.ProjectInput` 增加 `DefaultHostID`，创建与更新走同一个
  `domain.NewProject`，因此归一化规则一致：只做空白裁剪，空白即未设置，不生成隐式默认节点，也不
  因为该字段缺失而拒绝历史项目。领域层不做跨实体查询。
- **应用**：`ProjectService.Create` / `Update` 在写入前用现有存储能力 `GetExecutionHost` 确认节点
  存在且未被逻辑删除；不存在或已删除时按既有错误类别 `ErrProjectInvalidInput` 拒绝，不解析错误
  文本。只有引入**新的**非空标识时才校验引用，与项目当前已保存取值相同则原样保留——节点之后被
  逻辑删除时，项目仍可打开、仍可编辑其它配置、也可以清空该历史引用；清除写入 NULL 而不是空串。
  该确认只看引用是否成立，不把节点当前的预检状态当作项目配置合法性：规划运行时仍须执行自己的
  节点与环境前置条件检查。
- **存储**：`projectColumns` 增加 `default_host_id`（列数 12 → 13），`CreateProject` /
  `UpdateProject` 写入该列（空值落 NULL），`scanProject` 用 `sql.NullString` 还原 NULL；更新语句
  仍只写项目配置字段与 `updated_at`，不触碰 `created_at`、`deleted_at` 与历史任务记录。
- **HTTP**：既有 `POST/GET/PUT /api/v1/projects` 与列表接口增加 `default_host_id`：
  请求省略或传空都表示未设置；响应使用 JSON `null` 表达"没有默认节点"，不返回空字符串或任意节点
  标识；列表、详情、创建、更新的字段语义一致。无效节点引用返回 `400 invalid_request`，不产生项目
  配置的部分写入。客户端仍不能提交创建时间、更新时间或删除标记（未知字段被拒绝）。
- **前端**：`ProjectFormModal` 增加"默认执行节点（可选）"下拉，候选来自既有
  `GET /api/v1/hosts`（复用任务表单已有的 `useHosts` 查询，不新增重复 API），支持"不设置默认执行
  节点"，并区分加载中、查询失败与空列表：查询失败只给出重试入口，不伪装成"没有节点"，此时不提供
  除了"不设置"以外的选项。编辑时回显现有 `default_host_id`；节点已不在列表中时给出提示并保留旧
  引用，用户可原样保存、重新选择或清空。项目列表新增"默认执行节点"列，未设置显示中性占位符。前端
  不判断节点是否可执行，也不因为选为默认值就宣称它已通过远程环境预检。

本阶段不做：M3 需求工作台与规划 HTTP API、`RequirementAnalysisService` / `RequirementPlanningService`、
需求分析师与方案规划师的调用顺序与结构化解析、`requirements` 项目关联（M3 结构变更）的业务实现、任务顺序
字段与项目执行租约（后续阶段的结构变更）、跨进程规划调用协调、真实 Pi / SSH / Docker 规划环境验证，也
不修改普通任务的执行节点选择与任务配置快照。

定向测试覆盖本阶段业务场景（命令、结果与产物路径见 §7.11）：Domain 的归一化与空值语义；App 的
"不设置 / 设置有效节点 / 更新切换 / 清空落 NULL / 拒绝不存在与已删除节点 / 节点删除后仍可读取与编辑"；
Store 的 NULL 与非 NULL 往返、更新默认节点不影响其它项目字段、项目默认节点变化不改写任务
`host_id` 与 `config_json` 快照；HTTP 的创建、列表、详情、更新读写与 null 语义、无效节点返回 `400` 且不留
部分写入；前端表单取值转换、清除语义与"节点不在列表中"的展示规则。

### 7.9 已被单 Server 架构替代：规划会话跨进程执行租约与运行时释放保护（M2 第九阶段）

> **本阶段已被单 Server 架构替代，以下内容只作为历史记录保留，不描述当前实现。**
>
> 服务端进程锁确认"同一份数据目录只有一个 Server 进程"之后，跨进程仲裁不再成立：`planning_session_leases`
> 表、owner、operation、epoch、`lease_expires_at`、续租与租约丢失上报全部删除，`Prepare`、`RunRole` 与
> `Release` 的互斥改由进程内 `app.PlanningSessionCoordinator` 承担（见 §7.13）。规划会话的业务状态机、
> Revision、角色会话标识与固定快照没有变化。

规划调用当时由数据库级跨进程互斥保护：`Prepare`、`RunRole` 与 `Release` 竞争同一把会话锁，租约覆盖容器准备、
角色会话绑定、Pi 执行与 Session 归档的完整过程；租约丢失会取消在途调用并等待其收敛。M2 已有业务状态机、
Revision、角色会话标识、固定快照与普通任务执行行为均未改变。

- **持久化**：迁移脚本新增表 `planning_session_leases`（主键
  `planning_session_id`、`owner`、`operation`、`epoch`、`lease_expires_at`、`updated_at`）与部分索引
  `idx_planning_session_leases_expiry`。字段、组合约束、epoch 与释放语义见
  [`../DATA-MODEL.md`](../DATA-MODEL.md) §2：不声明外键、不为 `operation` 设枚举白名单、释放不删行且
  保留 epoch；规划会话表与项目表结构均未改动。
- **领域与存储**：`domain.PlanningSessionLeaseHandle`（规划会话 + owner + epoch）与
  `domain.PlanningLeaseOperation`（`run_role` / `prepare` / `release`，只是诊断分类）。存储层新增
  `AcquirePlanningSessionLease` / `RenewPlanningSessionLease` / `ReleasePlanningSessionLease`，全部为单条
  条件语句：领取用原子 UPSERT 递增 epoch（无记录从 1 开始，已释放或已过期可接管，未到期返回
  `storage.ErrConflict`）；续期与释放都要求 owner + epoch 匹配且仍未过期；释放只清空 owner / operation /
  到期时间并保留 epoch。影响行数为 0 一律返回 `storage.ErrConflict`（释放同时返回 false），没有任何"先
  SELECT 判断空闲再无条件 UPDATE"的两步路径。owner 由服务端生成且每次领取唯一，epoch 只由存储层分配：
  App 不计算代次，外部接口不接受 owner、epoch、TTL 与到期时间。
- **应用层租约服务**（`internal/app/planning_lease.go`）：`PlanningSessionLeaseService` 集中定义 TTL（默认
  2 分钟）、续租间隔（TTL/3，下限 50ms）与单次读写超时（5s），可注入时钟与较短 TTL。领取成功后立即启动
  后台续租；续租失败即记录原因并取消业务上下文。续租使用独立于业务请求的上下文（`context.WithoutCancel`
  加单次超时），因此请求取消或 Server 停止都不会停止续租——直到调用方显式 `Settle`。`Settle` 先关闭停止
  信号并等待续租 goroutine 退出，再以 owner + epoch 释放租约；返回错误同时保留"租约丢失"与"释放失败"。
- **RunRole 覆盖范围**：`PlanningAgentService.runRole` 在进程内排队之后、读取会话之前取得租约，之后的会话
  校验、项目校验、角色配置、容器准备、角色会话绑定、Session 恢复、Pi 执行与归档都在租约上下文内执行；
  租约收尾通过命名返回值合并进结果错误，释放失败或租约被接管不会被吞掉。Pi 进程退出不是结束边界，
  归档完成后才 `Settle`。`Stop` 的等待范围因此仍包含租约收尾与归档。
- **Prepare / Release 与 RunRole 的互斥关系**：`PlanningRuntimeService.Prepare` 与 `Release` 先取得同一
  租约再动手；`Release` 冲突时在进入任何容器删除、快照卷删除或绑定清空之前返回
  `ErrPlanningRuntimeBusy`，成功路径上租约持续到三类资源操作全部结束。`RunRole` 内部的准备走
  `prepareHeld`，复用已持有的租约，不存在嵌套领取、自锁或提前释放。进程内 `keyedMutex` 保留为排队优化，
  跨进程互斥由租约承担：同进程的第二个操作先排队，跨进程的第二个操作直接得到"正被占用"。
- **错误语义**：规划 Agent 新增 `ErrPlanningAgentBusy` / `ErrPlanningAgentLeaseLost` /
  `ErrPlanningAgentLeaseUnavailable`；规划运行时新增 `ErrPlanningRuntimeBusy` /
  `ErrPlanningRuntimeLeaseLost` / `ErrPlanningRuntimeLeaseUnavailable` 与阶段 `lease`。竞争、丢失与数据库
  不可用可区分，调用方不解析错误文本；M3 的 HTTP 尚未实现，本阶段不新增路由。
- **释放路径测试缝隙**：新增窄端口 `app.PlanningRuntimeReleasePort`（读取执行节点、读取容器记录、删除容器、
  删除本会话快照卷）与 `PlanningRuntimeServiceOptions.ReleasePort`。生产装配始终留空，由
  `planningRuntimeContainerRelease` 包装 `ContainerService`：快照卷删除代码只是从 `PlanningRuntimeService`
  移到该适配器，调用顺序、标签核验与错误映射未变，因此没有第二条容器或 SSH 调用链。
  该端口已在第十四阶段（§7.14）被 `app.PlanningRuntimeRemotePort` 取代：所有权与状态判定重新回到应用层，
  适配器只读取远端事实与执行单次删除。

fencing 边界（必须保留的风险）：

- 数据库 epoch 只能阻止旧持有者继续续租与释放新租约，**不能**阻止容器内的 Pi 进程继续写文件：容器文件系统
  不认识数据库代次。旧持有者失去租约后通过"取消上下文 → 等待 Runner 与归档收敛"尽快停止，这是收敛手段，
  不等于对远程文件写入提供了绝对 fencing。
- 现有 SSH / Docker exec 取消路径只保证本端命令与其会话结束；远端 Pi 进程是否随之终止没有在本阶段验证
  （真实 SSH / Docker / Pi 环境未执行），因此"跨进程 Pi Session 写入已绝对受保护"不成立。
- 若远端进程在取消后仍继续运行，新持有者的 Session 恢复可能与旧写入交叉。本阶段不通过缩短 TTL、忽略归档
  错误或静默接管来掩盖该风险，也不另建远程侧互斥或进程终止核验链。

### 7.10 仍未完成

以下能力尚未实现，M2 状态保持 `pending`：

- 规划轮次的业务编排：`PlanningAgentService` 只提供单角色调用入口，当前没有任何代码调用它；两个角色的
  调用顺序、提示词组装、结构化输出解析与 `CompleteAnalysis` / `FailAnalysis` 的自动收口仍属 M3。
- 后续项目级调度需要的结构变化（`requirements` 项目关联的业务实现、任务顺序、项目执行租约）：项目
  默认执行节点已在第八阶段落地，但调度侧仍未使用它。
- ~~进程内协调器没有解决"远端 Pi 进程是否真的已终止"~~ **代码缺口已关闭**（第十六阶段）：规划调用显式启用
  `StopContainerOnUncertain` 并在 Pi 主命令已启动但结果不确定时停止本会话独占的规划容器，以停止后的实际
  `docker inspect` 判定终止，本地记录与远程登记清单同步为 `stopped`，未确认时如实返回 `ErrRemoteTermination`
  并保留绑定。**真实 SSH / Docker 环境下的终止仍未验收**（见本条与 §7.16、§7.9 的 fencing 边界）。
- 规划服务的 HTTP API 与前端需求规划工作台（M3）：三个服务只完成装配与生命周期，没有对外入口。
- ~~撤销与重跑之外的失败恢复入口~~ **已关闭**（第十四阶段）：运行时长具备 `InspectPlanningRuntime`
  （只读现场核验）、`CleanupPlanningSnapshotPreparation`（一次性准备容器显式清理）、
  `RecoverPlanningSnapshot`（完整快照显式恢复）、`DiscardIncompletePlanningSnapshot`（不完整快照显式废弃），
  并以 `Release` 的显式重试收敛不确定结果；所有权与不确定判定集中在应用层，资源标签不一致时拒绝删除。
  仍属于未完成的是真实环境验收（见下一条与 §7.14）。
- ~~`Release` 不做"上一次释放结果不确定"的自动收敛~~ **语义已按此定稿**（第十四阶段）：远程删除结果无法确认时
  保留定位信息并立即返回带阶段的错误，调用方先用 `InspectPlanningRuntime` 取得远端真实现场，再显式发起下一次
  `Release`；服务端仍然**不自动重试**远程删除，也不在一次 `Release` 内部循环重试。
- 真实环境下的只读验证（文件工具与 `sh -c` 写入是否真的被文件系统拒绝）尚未执行：本阶段只确认参数
  构造正确，**不代表只读规划环境已经可用**。
- 自动化测试守点仍不完整：第七阶段的生命周期用例、第十阶段的规划会话 / 稳定 Session / 只读参数用例、§7.13 的
  单 Server 基础设施用例（数据库路径规范化、进程锁身份统一、进程内协调器锁表回收与同键串行 / 异键并行），以及第十四
  阶段的运行时现场核验 / 准备容器清理 / 快照恢复 / 快照废弃 / 释放恢复用例（受控远程替身注入存在、不存在、标签不一致、
  归属不成立与结果不确定五类现场），以及第十六阶段的规划容器终止与恢复用例（结果不确定时终止、独立有界清理上下文、
  受管理 / 标签 / Docker ID 核验、停止后 inspect 判定、不确定不虚构 stopped、同容器重启与历史 Session 恢复）已覆盖
  准入、取消传播、停止等待、会话状态机与事务边界、状态与两个绑定的条件更新、
  稳定 Session 首轮归档 / 多轮恢复 / 归档失败上报、规划只读工作区与 Docker 参数、运行时现场处置的所有权与幂等语义，
  以及容器停止与远端终止结论的表达。
  仍未自动化覆盖的是这些行为在真实环境中的结果：真实数据库上的 Schema 初始化与不兼容旧库的失败行为、真实 Docker / SSH
  的资源创建与释放，以及真实 Pi 的多轮上下文恢复。
- 未验证的真实外部能力：真实只读环境（文件工具与 `sh -c` 写入是否真的被文件系统拒绝）、真实多轮 Pi Session
  跨容器重建恢复、真实远程容器释放、**真实 SSH 中断与 Docker 环境下取消后的容器停止确认与历史 Session 恢复**
  （第十六阶段只交付了代码路径与受控替身测试），都没有执行，不得报告为通过。
  已删除的 `planning_session_leases` 不再有表结构与测试守点；其余 Schema 结构（项目默认节点、规划会话表）
  只在测试数据库中由首次初始化建表验证，没有自动化审计守点，也没有在真实开发数据库上执行。

### 7.11 验证方式

六个阶段都只做非测试验证：阅读代码核对调用链、`gofmt`、`go vet ./...`、`git diff --check`。第二阶段另加
人工核对迁移脚本中对应的表、索引、字段与非枚举约束；第三阶段人工核对快照卷命名与标签、准备 / 只读复核
容器、`--read-only` 与只读代码卷、tmpfs 可写目录、容器所有权校验、释放顺序与 `RequiredCLICommands`
新增参数，并确认普通任务容器的创建参数未改变；第四阶段人工核对七个业务操作的事务边界、领域状态机调用点、
条件更新的前置状态、角色消息发送方限制与失败摘要脱敏；第五阶段人工核对运行时绑定更新的实际 `SET` 列、
NULL 安全的比较并交换条件、`persistRuntime` 的事务内重读与错误映射，并确认 `Prepare` 的三个持久化阶段与
`Release` 都走同一入口；第六阶段人工核对角色会话绑定的 `SET` 列与比较条件、两个角色路径的生成与根目录
包含校验、恢复与归档的调用顺序、归档只有在本轮正常完成且校验通过时才替换、归档失败与执行状态的分离上报，
并确认普通任务不传稳定 Session 时的路径与参数未改变。

六个阶段都未运行任何测试（含数据库 Schema 初始化与 SQLite 读写测试），未创建测试文件，未连接真实 Docker / SSH / Pi /
远程主机。因此不兼容旧库的失败行为、未知枚举值写入与读回、序号分配与条件更新在并发下的实际行为、真实快照
克隆、只读挂载与容器释放均未验证，不得报告为通过；第四阶段的并发结论来自事务与条件更新的代码路径核对，
第五阶段的运行时绑定冲突结论同样只来自 SQL 条件与调用链核对，没有真实并发执行与跨进程竞争的证据。
第六阶段的多轮上下文恢复也没有真实环境证据：把容器内 Session 复制回本机、再从本机归档恢复到容器内这条链路
只在代码层面核对过，容器仍在运行时复用同一份 tmpfs 文件**不等于**容器停止、重建或 Docker 环境恢复后仍能
恢复上下文；`docker cp` 恢复、`mkdir -p` 建立容器目录、SSH 上传与归档原子替换的实际行为，以及 Pi 对已存在
Session 文件的续写语义，都需要在真实只读规划环境中验证后才能报告为可用。

第七阶段的验证方式与前六个阶段不同：本任务明确授权针对**本阶段实际实现的生命周期业务行为**编写并运行
必要测试，因此既有非测试验证，也有定向测试结果。

- **非测试验证**：人工核对三个服务的构造位置与参数来源（`Store` / `Roles` / `Containers` / `Runtime`
  全部来自当前 Server 实例，`SessionRoot` 来自 `app.SessionRoot(s.cfg)`）、SSH 与容器服务缺失时的降级
  分支、`planningAgent` 在服务列表中的注册位置与逆序关闭关系、`beginCall` 准入与 `Stop` 的临界区顺序、
  `Wait` 之前不再发生 `Add`、取消信号从 `RunRole` 到 `RunAgent` 的传播路径，并确认普通任务执行路径
  （Step / Workflow / Scheduler / 容器创建参数）未被改动。`gofmt`、`go vet ./...`、`git diff --check`
  均无输出。
- **已运行的定向测试**（`tests/unit/app/planningagent`，只跑本阶段用例）：

  ```bash
  go test -count=1 ./tests/unit/app/planningagent -run '^(TestPlanningAgentService)'
  go test -count=1 -race ./tests/unit/app/planningagent -run '^(TestPlanningAgentService)'
  ```

  两条命令均通过（9 个用例全部 PASS，`-race` 无数据竞争报告）；`-count=2` 的重复运行同样全部通过，
  用例使用 `TestMain` + `testkit` 随机工作空间，产物保留在 `TEST_ARTIFACT_DIR`
  （`%TEMP%\lingting-agent-flow-tests\planningagent-*`），不写入仓库与用户数据目录。
- **未运行的测试**：`go test ./...`、`go test ./tests/...`、`make test`、`make test-race`、其他测试包
  （含 `tests/unit/store` 的仓储用例）、真实 Pi / SSH / Docker / 远程主机测试均未执行；未设置或
  修改 `AGENTFLOW_REAL_TESTS` 等真实测试开关，未关闭 SSH 主机密钥校验，未启动真实 AgentFlow Server
  连接真实设备。因此第七阶段只证明生命周期准入、取消传播与停止收敛在该测试缝隙下成立，不能推断真实
  远程环境、真实 Pi 归档或多轮恢复已经可用。

第八阶段同样获得定向测试授权，验证范围收敛在"项目默认执行节点"这一配置链路上，不执行完整测试套件。

- **非测试验证**：人工核对初始迁移中 `projects.default_host_id` 与部分索引的定义、`projectColumns`
  与占位列数一致、`scanProject` 的 NULL 还原、`UpdateProject`
  的 `SET` 列不含 `created_at` / `deleted_at`、应用层的引用校验只在引入新标识时发生、HTTP 响应以 JSON
  `null` 表达未设置，并确认普通任务执行路径（`tasks.host_id`、`config_json` 快照与执行节点选择）未被改动。
  `gofmt`、`go vet ./...`、`git diff --check` 均无输出，所有改动文件行尾均为 LF。
- **已运行的定向测试**（每条命令只跑本阶段用例或本阶段涉及的既有项目主流程）：

  ```bash
  go test -race -count=1 ./tests/unit/domain -run '^TestProject.*DefaultHost'
  go test -race -count=1 ./tests/unit/app -run '^TestProjectService.*DefaultHost'
  go test -race -count=1 ./tests/unit/store -run '^TestProject.*DefaultHost'
  go test -race -count=1 ./tests/unit/httpapi -run '^TestProjectAPI.*DefaultHost'

  # 既有项目主流程回归（精确名称）
  go test -race -count=1 ./tests/unit/domain -run '^(TestNewProject|TestProject|TestRequireProjectID)'
  go test -race -count=1 ./tests/unit/app -run '^(TestProjectService|TestTaskCreationRequiresExistingProject)'
  go test -race -count=1 ./tests/unit/store -run '^(TestProject|TestCreateAndGetProject|TestGetProjectNotFound|TestUpdateProject|TestDeleteProjectIsLogical|TestListProjectsFiltersDeleted)'
  go test -race -count=1 ./tests/unit/httpapi -run '^TestProjectAPI'
  ```

  八条命令全部通过（`-race` 无数据竞争报告），覆盖本阶段新增的默认节点用例与既有项目主流程用例；其中旧库
  增量升级用例 `TestProjectsMigrationDefaultHostKeepsExistingProjects` 已在取消手写增量升级时删除。
  用例使用 `TestMain` + `testkit` 随机工作空间，产物保留在 `TEST_ARTIFACT_DIR`
  （`%TEMP%\lingting-agent-flow-tests\{domain,app,store,httpapi}-*`），不写入仓库与用户数据目录。
- **前端**（`lingting-agent-flow-ui`）：

  ```bash
  npx vitest run tests/unit/projects.test.ts
  npx tsc --noEmit
  npx vp fmt <本次修改的前端文件>
  npx vp lint
  npm run build
  ```

  单元测试 41 个用例全部通过（含本阶段新增的表单取值转换、清除语义与"节点不在列表中"展示规则），类型
  检查无输出，生产构建成功（仅保留既有的 chunk 体积告警）；产物留在
  `%TEMP%\lingting-agent-flow-ui-tests\run-*`，构建写入 `internal/webui/dist/`（不提交）。
- **未运行的测试**：`go test ./...`、`go test ./tests/...`、`make test`、`make test-race`、其他测试包与
  前端全量/E2E 测试均未执行；未设置或修改 `AGENTFLOW_REAL_TESTS` 等真实测试开关，未连接真实 Docker /
  SSH / Pi / 远程主机。因此第八阶段只证明"项目默认执行节点"在测试数据库与测试替身下从数据库结构到界面的
  配置链路成立：不代表真实开发数据库已按当前 Schema 重建，也不代表任何节点当前可用或已通过远程环境预检。

第九阶段同样获得定向测试授权，验证范围收敛在"规划会话跨进程租约与运行时释放保护"，不执行完整测试套件。

- **非测试验证**：人工核对迁移脚本中 `planning_session_leases` 的表、列、组合 CHECK 与部分索引，
  核对领取 UPSERT 的原子性、回读代次的条件、续期与释放的 owner + epoch + 未过期三个
  条件、释放保留 epoch、受影响行数 0 一律按冲突返回；核对 `RunRole` 的租约取得点（读取会话之前）、
  `Settle` 的调用点（Runner 与归档返回之后）、`prepareHeld` 不重复领取、`Release` 的租约先于任何远程删除、
  释放路径的端口适配器与原实现的调用顺序 / 标签核验 / 错误映射一致，并确认 `planning_sessions` 表结构、
  会话业务状态机、Revision、角色会话标识与普通任务执行路径（TaskLease、Step / Workflow）未被改动。
  `gofmt`、`go vet ./...`、`git diff --check` 均无输出，本次修改文件行尾均为 LF。
- **已运行的定向测试**（`-race`，`-count=1`）：

  ```bash
  go test -race -count=1 ./tests/unit/store -run '^TestPlanningSessionLease'
  go test -race -count=1 ./tests/unit/app/planningagent \
    -run '^(TestPlanningAgentServiceCrossInstanceLease|TestPlanningAgentServiceLeaseRenewal|TestPlanningAgentServiceLeaseLoss|TestPlanningAgentServiceLeaseTakeoverKeepsNewLease)$'
  go test -race -count=1 ./tests/unit/app/planningagent -run '^TestPlanningRuntimeServiceLeaseGuard'
  go test -race -count=1 ./tests/unit/app/planningagent -run '^TestPlanningAgentService'
  ```

  四条命令全部通过（`-race` 无数据竞争报告）：Store 侧 11 个用例（首次领取代次 1、第二领取者冲突、两个独立
  数据库连接竞争只有一个成功、续期后不被接管、过期租约不得续期、新代次接管后旧持有者不得续期或释放、释放
  保留代次、重新领取代次递增、数据库故障不同于冲突、非法请求按数据错误拒绝）；规划 Agent 侧 4 个用例
  （两个独立服务实例共享同一数据库时第二个实例被租约拒绝且不进入自己的 `RunAgent`、不改写角色会话绑定；
  长时间调用期间租约持续续期且不被接管、返回后可重新领取；续租失败取消 `RunAgent` 上下文、等待归档收尾、
  以 `ErrPlanningAgentLeaseLost` 上报并在收敛后释放租约；旧持有者不得清空新代次租约）；规划运行时侧 5 个
  用例（`RunRole` 持租期间 `Release` 冲突且未调用任何远程能力、绑定未被清空；`RunRole` 返回后 `Release`
  正常完成并清空绑定；`Release` 期间 `RunRole` 与 `Prepare` 均被拒绝；远程删除失败返回
  `ErrPlanningRuntimeFailed` 且保留定位信息、不报告资源已释放；释放期间租约被接管时返回
  `ErrPlanningRuntimeLeaseLost` 而不报告成功）；既有规划 Agent 用例（含生命周期准入、取消传播、停止等待）
  共 13 个用例全部通过。用例使用 `TestMain` + `testkit` 随机工作空间，产物保留在 `TEST_ARTIFACT_DIR`：
  `%TEMP%\lingting-agent-flow-tests\store-2518212759`（Store 租约）、
  `%TEMP%\lingting-agent-flow-tests\planningagent-4101371743`（跨实例 / 续期 / 丢失 / 接管）、
  `%TEMP%\lingting-agent-flow-tests\planningagent-3040705929`（释放保护）、
  `%TEMP%\lingting-agent-flow-tests\planningagent-102712429`（既有 `TestPlanningAgentService` 回归）。
- **未运行的测试**：`go test ./...`、`go test ./tests/...`、`make test`、`make test-race`、其他测试包均未
  执行；未设置或修改 `AGENTFLOW_REAL_TESTS` 等真实测试开关，未连接真实 Docker / SSH / Pi / 远程主机，未启动
  真实 AgentFlow Server。因此第九阶段只证明租约在 SQLite 与受控替身下成立：不代表真实多 Server 部署已验证，
  也不代表远端 Pi 进程会在租约丢失后真正终止（见 §7.9 的 fencing 边界）。

### 7.12 已交付：规划会话与稳定 Session 自动化测试守点（M2 第十阶段）

第十阶段只做一件事：为 M2 已经实现的业务补齐可重复的定向自动化测试守点，范围收敛在规划会话状态机、存储层事务
与条件更新、应用层业务流程、稳定 Pi Session 的恢复与归档，以及规划只读工作区与 Docker 创建参数。本阶段不新增
生产代码能力，也没有发现需要修复的本阶段缺陷；除 `tests/` 下的测试文件外未改动其他文件。

- **Domain（`tests/unit/domain/planning_session_test.go`）**：全部断言调用真实领域方法，不复制状态机规则。覆盖
  `active → analyzing → preview_ready` 与 `preview_ready → analyzing` 多轮继续分析、`analyzing → failed` 后
  普通入口不得重置结论且 `RetryAnalysis` 清空失败摘要与结束时间、非失败状态拒绝重试、确认必须关联有效版本且
  状态与结束时间/采纳版本一致、未确认会话不得带采纳版本、`active` / `preview_ready` 的放弃、`analyzing` 期间
  放弃被状态机拒绝、`confirmed` / `abandoned` 不能被 `BeginAnalysis` / `RetryAnalysis` / `Transition` 重新打开，
  以及两个角色会话标识不能相同、重复绑定相同取值幂等且不改变业务状态。
- **Store（`tests/unit/store/planning_sessions_test.go`）**：全部使用真实 SQLite，并用独立连接读取原始列或重新
  读取记录断言持久化事实。覆盖会话字段完整往返与可空字段的 NULL 语义（未绑定列的原始值为 NULL，而非空串）、
  按项目列出会话的过滤与倒序、消息序号与版本号从 1 开始且不同会话使用独立序号空间、非法消息 / 非法 JSON / 不
  存在的会话不留下半成品记录与记录数不变、两个独立数据库连接并发追加后序号与版本号连续且不重复、状态条件更新
  的成功、返回 `storage.ErrConflict` 与冲突后数据库保留最新状态（含迟到结果不能覆盖 `confirmed` / `abandoned` /
  `failed` 结论）、运行时绑定首次写入 NULL 绑定、旧 expected 冲突、成功时只更新运行时四列且 `status` /
  `finished_at` / `adopted_revision_id` / `error_summary` / 角色会话标识不变、会话不存在返回 `storage.ErrNotFound`，
  以及角色绑定成对写入、旧 expected 冲突、两个角色标识相同或单边写入按 `storage.ErrInvalidData` 拒绝、角色绑定
  不改变业务状态与运行时绑定、两个独立连接基于同一 expected 并发写入恰好一成功一冲突。
- **App（`tests/unit/app/planning_session_test.go`）**：使用真实 SQLite、真实 `PlanningSessionService` 与
  `testkit`。覆盖创建只允许关联存在且未逻辑删除的项目、初始状态与时间/运行期字段由服务端生成、消息与版本查询
  返回真实持久化记录、项目逻辑删除后创建与列表返回 `ErrPlanningSessionNotFound`；提交消息的准入（`active` 与
  `preview_ready` 允许，`analyzing` / `failed` / `confirmed` / `abandoned` 拒绝且不留下消息）、并发提交恰好一成功
  一冲突且数据库只有 1 条消息；完成分析追加版本与两条角色消息并进入 `preview_ready`、第二轮版本号为 2、非规划
  角色与非法 JSON 分别返回 `ErrPlanningSessionInvalidInput` 且不留下版本、角色消息或成功状态、非 `analyzing` 状态
  返回冲突；失败只允许 `analyzing`、失败摘要经既有脱敏与裁剪（凭据替换为占位符、超长摘要带截断标记）、失败不写
  版本与角色成功消息、`failed` 必须经显式 `RetryAnalysis` 才能回到 `analyzing` 且补充消息与状态迁移同事务落库、
  无补充消息时不追加消息；跨会话版本确认返回 `ErrPlanningSessionRevisionNotFound`、确认后 `adopted_revision_id` 与
  `confirmed` 同时落库、重复确认返回冲突且采纳版本不变、`analyzing` 放弃返回冲突、`preview_ready` 放弃保留消息 /
  版本 / 运行时绑定、确认规划不创建正式 Requirement 与 Task。
- **稳定 Pi Session（`tests/unit/agent/container_runner_test.go`）**：复用既有 `fakeExecutor`，只按真实接口需要
  为它补上 `agent.ContainerFileWriter` 能力（`UploadFromLocal` / `CopyToContainer`）与按发生顺序记录的操作日志，
  没有第二套容器执行器模拟框架。覆盖首轮无归档时不恢复、Pi 使用服务端固定容器 Session 路径、完整 Session 被提升
  为本机稳定归档且 `Archived` / 路径 / 字节数反映真实结果；第二轮在启动 Pi 之前按"本机文件 → 远程宿主机临时文件 →
  容器固定路径"顺序恢复（用实际调用记录断言恢复先于 `--print` 主调用）、恢复失败不启动 Pi、两轮复用同一容器路径
  而 `InvocationID` 不同、第二轮成功后新快照替换旧归档；执行失败时保留最后一次完整归档、单轮 Session 仍按原规则
  保存且不报告已归档；本机归档损坏时恢复明确失败、不启动 Pi、不删除或改写原归档；受控文件系统故障（归档临时路径
  被非空目录占据）导致归档失败时返回 `agent.ErrSessionArchive`、`RunResult.Status` 仍是 `RunCompleted`、
  `StableSession.Error` 说明归档失败且已有完整归档不被破坏；执行器不实现 `ContainerFileWriter` 时明确失败、不
  静默忽略历史上下文、不启动 Pi、不删除历史归档。
- **只读工作区与 Docker 参数**：`tests/unit/execenv/execenv_test.go` 覆盖 `NewReadOnlyWorkspace` 设置
  `ProjectAccessReadOnly`、可写目录按声明顺序保留、Session 目录必须由显式可写目录覆盖（空白声明等同未声明）、
  可写目录与项目代码目录互相包含或相等时拒绝、只读工作区不接受缓存卷，以及 `NewWorkspace` 仍保持可写语义并拒绝
  在可写模式声明可写目录；`tests/unit/container/docker_args_test.go` 覆盖规划容器创建参数包含 `--read-only`、
  项目代码快照卷以 `type=volume,...,readonly` 挂载、`--tmpfs` 取值与显式声明的可写目录逐项一致、不出现 `--privileged` /
  `--pid=host` / `--network=host` / `type=bind` / Docker Socket / 可写缓存卷、普通任务容器参数不因只读能力改变
  （无 `--read-only`、无 `--tmpfs`、缓存卷保持可写）、非法组合（缺只读代码卷、缺可写临时目录、挂可写缓存卷、
  临时目录与代码卷重叠、临时目录使用保留路径、非只读模式声明只读挂载）在创建前被拒绝，且 `RequiredCLICommands`
  的 `create` 清单包含 `--read-only`、`--tmpfs` 与 `--mount`。
- **已运行的定向测试**（全部 `-race -count=1`，只跑本阶段用例）：

  ```bash
  go test -race -count=1 ./tests/unit/domain -run '^TestPlanningSession'
  go test -race -count=1 ./tests/unit/store -run '^TestPlanningSession(Store|Message|Revision|RuntimeBinding|RoleBinding)'
  go test -race -count=1 ./tests/unit/app -run '^TestPlanningSessionService'
  go test -race -count=1 ./tests/unit/agent -run '^TestStableSession'
  go test -race -count=1 ./tests/unit/execenv -run '^TestReadOnlyWorkspace'
  go test -race -count=1 ./tests/unit/container -run '^TestPlanningContainerCreateArgs'
  ```

  六条命令全部通过，`-race` 无数据竞争报告。用例使用各包既有 `TestMain` + `testkit` 随机工作空间，产物保留在
  `TEST_ARTIFACT_DIR`（`%TEMP%\lingting-agent-flow-tests\{domain,store,app,agent,execenv,container}-*`），不写入
  仓库或用户数据目录。另外对本阶段改动涉及的既有容器 Runner 用例（`fakeExecutor` 增加了操作顺序记录与两个文件
  传输方法）按精确测试名做了回归：`TestCommandConstruction`、`TestSessionPathUniquePerInvocation`、
  `TestSessionSavedLocally`、`TestExportFailureKeepsJSONL`、`TestMissingSessionMarksFailure`、
  `TestPartialSessionRecorded`、`TestDownloadFailureKeepsRemoteCopy`、`TestNonZeroExitClassified`、
  `TestTimeoutAndCancelClassified`、`TestUncertainProcessReported`、`TestPiUnavailableInContainer`、
  `TestDetachedContainerRejected`、`TestDockerIDMismatchRejected` 全部通过。
- **非测试验证**：`gofmt`（仅本次修改的测试文件）、`go vet ./...`、`git diff --check` 均无输出，新增与修改文件
  行尾均为 LF。
- **未运行的测试与未验证的能力**：`go test ./...`、`go test ./tests/...`、`make test`、`make test-race` 与其他
  测试包均未执行；未设置或修改 `AGENTFLOW_REAL_TESTS` 等真实测试开关，未连接真实 Docker / SSH / Pi / 远程主机，
  未启动真实 AgentFlow Server。因此本阶段证明的是"代码路径已实现且自动化测试在受控替身下通过"，以下事项仍属于
  未完成：真实规划容器中项目代码是否确实不可写、真实 Pi 是否可以从归档恢复并继续同一上下文、Docker 容器停止 /
  重建后真实 Session 的恢复能力、`docker cp` / `mkdir -p` / SSH 上传与归档原子替换的实际行为、真实 SSH / Docker
  取消后远端 Pi 进程是否确实终止、一次性准备容器的残留与远程释放不确定结果的恢复入口，以及旧数据库被
  明确拒绝打开的行为（未在真实旧库上验证）。M2 状态继续为 `pending`。

### 7.13 已交付：单 Server 文件锁身份统一与进程内协调器锁对象回收（M2 基础设施修复）

本阶段修复单 Server 架构的两处基础设施缺陷，并校正历史阶段与路线图的描述。它不新增业务能力，M2 状态仍为
`pending`。

- **数据库路径规范化**（`internal/server/process_lock.go`）：`normalizeDatabasePath` 先用 `filepath.Abs` 与
  `filepath.Clean` 收敛相对路径与冗余段，再用 `filepath.EvalSymlinks` 解析最近一个已存在的祖先（覆盖数据库
  文件本身是符号链接、父目录是符号链接两种情形），把尚未创建的路径段按原顺序拼回。悬空符号链接与解析过程中
  的其他文件系统错误一律返回错误并保留原始错误链，启动失败关闭，不把悬空链接当成"尚未创建的新数据库"。
- **锁身份与 SQLite 路径统一**：`ProcessLock` 保存规范化数据库路径、`<database_path>.server.lock` 与 OS 文件锁
  对象；`Server.Run` 取得锁之后把配置中的数据库路径替换为规范化路径，因此 `StoreOpener` 打开的就是与锁同一条
  路径。锁被占用时不打开数据库、不执行 Schema 初始化、不构造后台服务、不启动 HTTP；锁的生命周期
  （HTTP 关闭 → 后台收敛 → 数据库关闭 → 释放文件锁）与"不删除锁文件"的语义不变。
- **keyed mutex 引用计数回收**（`internal/app/coordination.go`）：`PlanningSessionCoordinator` 与
  `EnvironmentBuildLocks` 共用同一个带引用计数的 `keyedMutex`：引用计数在等待实际互斥锁之前增加，覆盖持锁者与
  等待者，只有最后一个引用退出时才删除锁表条目，因此锁表不会随节点 / 环境 / 会话数量增长。`EnvironmentBuildLocks`
  仍以 `lockKey(hostID, ref)` 生成锁键，HTTP 后台构建与任务启动的环境准备继续共用 Server 装配的同一份实例。
  `TaskExecutionCoordinator`（`keyedTaskMutex`）及其执行权语义未改动。
- **已知边界**：本阶段只处理符号链接别名。Go 1.23 起 `os.Lstat` 不再把目录联接（junction）标记为符号链接，
  `filepath.EvalSymlinks` 不解析它，因此 Windows 目录联接形式的路径别名不在本次范围内。

本次测试范围与实际结果（本任务明确授权测试）：

- 基线（改动生产代码之前）：`go test -count=1 ./tests/unit/server ./tests/unit/app` → 339 passed。
- `go test -race -count=20 ./tests/unit/app -run '^(TestPlanningSessionCoordinator|TestEnvironmentBuildLocks)'`
  → 120 passed，`-race` 无数据竞争报告。
- `go test -race -count=1 ./tests/unit/server ./tests/unit/app` → 346 passed。
- `go vet ./...` 与 `git diff --check` 无输出。
- 本机 Windows 未开启开发模式，`os.Symlink` 返回 `ERROR_PRIVILEGE_NOT_HELD`，因此 7 个依赖符号链接的用例
  （数据库文件别名、父目录别名、悬空链接、Server 使用规范化路径打开数据库、别名锁冲突）在本机 `SKIP` 并记录
  原因，只有 5 个不依赖符号链接的用例实际执行；这些用例在支持符号链接的环境（CI 的 `ubuntu-latest`）会真实执行。
- 未运行的验收：未连接真实 Docker / SSH / Pi / 远程主机，未设置或修改 `AGENTFLOW_REAL_TESTS` 等真实测试开关，
  未启动真实 AgentFlow Server。基础设施修复与单元测试通过不代表真实环境验收完成。

### 7.14 已交付：规划运行时现场核验、失败恢复与残留资源处置（M2 第十四阶段）

本阶段为规划运行时补齐"先核验远端事实、再显式选择动作"的恢复能力，解决三类残留现场：快照准备容器因 SSH、
Docker 或远程命令结果不确定而残留；快照卷已创建但 Clone / checkout / 进程中断导致快照未完成；释放时远程删除结果
不确定。生产代码只在 `internal/app` 与既有 `ContainerService` / `remoteDocker` / Docker Runner 能力上扩展，
没有新增第二套 SSH 或 Docker 调用链，也没有引入数据库租约、owner、epoch、TTL 或跨进程仲裁。

**新增只读现场核验 `PlanningRuntimeService.InspectPlanningRuntime`**

- 覆盖数据库运行时绑定、已登记规划容器的远端实际状态、快照卷存在性、快照卷的管理 / 节点 / 会话标签、一次性准备
  容器的存在与运行状态，以及远端快照实际 commit 与 `SourceCommit` 的一致性。
- 每份资源给出四态结论 `PlanningRuntimePresencePresent` / `Absent` / `Unknown` / `Mismatch`：SSH 失败、Docker
  inspect 失败、超时与解析失败一律落到 `Unknown`，**不会**被映射为资源不存在；标签或归属不成立落到 `Mismatch`。
- 标签不一致、归属不成立或固定 commit 不一致返回 `ErrPlanningRuntimeConflict`；无法确认的探测返回
  `ErrPlanningRuntimeUncertain`。错误与结果同时返回：已确认的事实不因另一个资源的失败而丢失。
- 核验不创建、启动、停止或删除规划资源，不写数据库。快照实际 commit 由既有的一次性只读核验容器（前台 `--rm`、
  快照卷只读挂载、只读取 HEAD 与当前引用）取得，结束后容器自动删除，不留下需要清理的远程资源。
- 会话未绑定执行节点时不建立连接：准备流程在产生任何远程副作用之前先落盘执行节点，因此此时三份资源按数据库事实
  报告为 `Absent`，而不是把探测失败当成不存在。

**新增一次性准备容器清理 `CleanupPlanningSnapshotPreparation`**

- 容器名称继续由 `planningPrepContainerName(sessionID, hostID)` 生成，调用方只能提交规划会话标识，无法指定任意
  Docker 容器名称或 ID。
- 清理前重新 inspect，并同时核验：实际容器名称、AgentFlow 管理标签、`ContainerLabelKey` **不存在**、节点标签、
  规划会话标签与 Docker 容器 ID 非空。任一不满足返回冲突且不调用删除，因此不会删除正式规划容器、任务容器或
  其他会话 / 其他节点的资源。
- 规则：容器不存在按已清理处理；仍在运行拒绝自动停止与删除（返回冲突）；状态无法确认（读取失败或 Docker 状态不是
  `exited` / `dead`）拒绝操作；只有已退出且所有权全部核验通过才按 Docker 容器 ID 删除；删除结果不确定立即返回并
  保留现场、不自动重试；删除成功后再次按名称核验，确认容器确实不存在才报告已清理。清理不删除快照卷。

**新增完整快照恢复 `RecoverPlanningSnapshot`**

- 顺序固定：取得会话锁 → 读取会话与绑定 → 核验一次性准备容器（**只要仍存在就拒绝**，即使已退出，必须先显式清理）
  → 通过既有只读核验逻辑检查快照卷所有权、worktree、HEAD 与实际 commit → 通过既有条件更新写入 `SourceCommit`。
- `SourceCommit` 为空时写入实际 commit；已记录时远端实际 commit 必须完全一致，否则返回冲突；远程状态不确定时保留
  原有绑定。恢复只接受已经完整存在的快照，**不重新执行 Clone 或 checkout**，也不把不完整快照标记为成功。
- 写入走 `persistRuntime` 的条件更新：绑定被并发改写时返回冲突并保留数据库最新值，不会用旧对象覆盖新事实。

**新增不完整快照废弃 `DiscardIncompletePlanningSnapshot`**

- 破坏性操作，只有同时满足 `SourceCommit` 为空、`ContainerID` 为空、一次性准备容器已确认不存在、快照卷存在且
  卷名 / 管理标签 / 节点标签 / 会话标签全部匹配时才允许删除。
- 顺序固定：取得会话锁 → 读取绑定 → 确认准备容器不存在 → inspect 快照卷 → 核验完整所有权 → 删除 → 再次 inspect
  确认卷不存在 → 返回废弃结果。删除结果不确定时保留数据库绑定，不通过第二次删除命令猜测结果。
- 不清空 `SourceCommit`、不修改会话业务状态、不重新准备快照；恢复有效快照与废弃不完整快照是两个独立操作，不合并为
  一次隐式自动修复。

**`Release` 的不确定结果核验与显式重试**

- 顺序固定：按会话串行 → 核验规划容器远端状态 → 删除容器 → 确认已删除或已不存在 → 核验快照卷远端状态 → 删除卷 →
  再次核验不存在 → 清空运行时绑定。任一步不确定都保留数据库定位信息并在本次调用内立即返回。
- 再次显式 `Release` 时重新核验远端状态：已确认不存在的资源按幂等完成处理（不重复执行有副作用的删除），仍存在且
  所有权正确的资源才继续后续步骤；标签不匹配或状态无法确认时立即停止。只有两项资源都确认不存在才通过
  `persistRuntime` 的条件更新清空绑定。`Release` 内部不循环重试，也不新增批量清理或全局扫描删除能力。

**端口与调用链**

- `PlanningRuntimeServiceOptions.ReleasePort` / `app.PlanningRuntimeReleasePort` 被 `RemotePort` /
  `app.PlanningRuntimeRemotePort` 取代：端口只提供"读取远端事实"与"执行一次删除"（`InspectManagedContainer`、
  `DeleteManagedContainer`、`InspectPrepContainer`、`RemovePrepContainer`、`InspectSnapshotVolume`、
  `RemoveSnapshotVolume`、`VerifySnapshotCommit`），所有权与状态判定集中在 `PlanningRuntimeService`，测试因此能观察
  "标签不一致不得删除"这类边界。生产装配仍由 `planningRuntimeRemote` 包装 `ContainerService`，调用继续经过
  `ContainerService.withRemoteSession` 与同一个 `docker.Runner`。
- 新增失败阶段 `inspect` / `cleanup_preparation` / `recover_snapshot` / `discard_snapshot`，调用方据此定位失败位置，
  不解析错误文本。
- Inspect / Recover / Cleanup / Discard / Release 与 `RunRole`、`Prepare` 共用同一个
  `app.PlanningSessionCoordinator`：同一会话严格串行，不同会话继续并行，排队期间上下文取消不执行任何远程副作用。

**测试范围与实际结果（本任务明确授权测试）**

- 基线（改动生产代码之前）：`go test -count=1 ./tests/unit/app ./tests/unit/app/planningagent ./tests/unit/server`
  → 355 passed。
- 新增 `tests/unit/app/planning_runtime_test.go` 与 `tests/unit/app/planning_runtime_fixture_test.go`：36 个顶层用例
  （含子用例共 62 个），使用真实 SQLite、真实 `ContainerService` 外壳与 `app.PlanningRuntimeRemotePort` 受控替身，
  替身未登记即视为不存在、失败必须显式注入。覆盖：现场核验的三态结论（存在 / 不存在 / 未知）与三类归属不匹配、
  固定 commit 不一致、未绑定节点时不访问远程；准备容器清理的幂等、运行中拒绝、状态不确定拒绝、其他会话 / 节点 /
  正式容器拒绝、删除不确定保留现场、删除后复核；快照恢复的写入、相同 commit 幂等、不同 commit 拒绝、准备容器存在
  拒绝、不完整 / 不确定不更新绑定、条件更新冲突保留最新值；不完整快照废弃的全部前置条件、删除不确定保留绑定、
  删除后复核与卷已不存在的幂等；`Release` 的容器删除不确定即停止、卷删除不确定保留绑定、已确认不存在按幂等完成、
  归属不成立 / 状态不确定不清空绑定、清空走条件更新；以及同会话串行（含与 `RunRole` 互斥）、异会话并行、排队期间
  取消不产生远程动作。
- `go test -race -count=20 ./tests/unit/app -run '^TestPlanningRuntime'` → 1240 passed，`-race` 无数据竞争报告。
- `go test -race -count=1 ./tests/unit/app ./tests/unit/app/planningagent ./tests/unit/server` → 417 passed。
- `go vet ./...`、`git diff --check` 无输出；本次新增与修改文件行尾均为 LF。
- `.github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e` 通过（31 个测试包全部分类，无遗漏、
  无重复归属；本阶段未新增测试包）。

**仍未完成的真实环境验收（不得报告为通过）**

- 真实文件工具与 Shell 写入是否确实被文件系统拒绝。
- 真实 Pi 多轮上下文恢复（跨容器重建）。
- 容器停止、重建后的真实 Session 恢复。
- 真实远程容器与快照卷的创建、删除与释放结果。
- 远端 Pi 进程在取消后是否真正终止。
- 真实开发数据库的 Schema 初始化与不兼容旧库的失败关闭行为。

以上验收需要真实 Docker / SSH / Pi / 远程主机才能成立，本阶段的受控替身测试**不构成**真实环境验收。M2 状态继续为
`pending`：仍阻塞 M2 的退出条件是"所有代码写入尝试被运行环境拒绝"与"Session 与容器生命周期可持久化恢复"两项在真实
环境中的证据，以及规划轮次业务编排（M3）接入后在真实环境中复用同一容器与快照的多轮验证；本次阶段只补齐运行时的
现场核验、失败恢复与残留资源处置能力，不改变 M2 的完成判定。

### 7.15 已交付：Schema 初始化改为版本化 SQL 迁移（Goose，M2 基础设施变更）

本阶段把 SQLite 的 Schema 初始化从手写的"完整最终态脚本 + `PRAGMA user_version` 判断"改为版本化 SQL 迁移：
`internal/storage/sqlite/migrations/00001_initial_schema.sql` 整体承接原最终态结构，`sqlite.Open` 在确认
PRAGMA 之后由 Goose 执行所有待执行的迁移，已执行的版本记录在 `goose_db_version` 表中。

- **迁移组件**：使用 `github.com/pressly/goose/v3` 的 `goose.NewProvider`（关闭全局注册表、禁止乱序迁移），
  脚本通过 `//go:embed` 编译进程序，不依赖工作目录，也不引入 CLI 外部可执行文件。
- **执行顺序**：创建父目录 → 打开连接 → 确认 PRAGMA → 构造 Provider → `Up` 执行待执行迁移；任一步失败都
  关闭刚建立的连接，不返回半初始化的 DB。已有数据库的迁移版本由 Goose 自己识别，重复打开不会重复执行。
- **删除的手写实现**：`currentSchemaVersion`、`readSchemaState`、`initializeSchema`、`createSchema`、
  `unsupportedSchemaError`、`schemaScript` 与 `schema.sql` 一并删除，`storage.ErrUnsupportedSchema` 不再
  有使用方也随之删除；不再用 `user_version` 管理版本，也不再保留第二份完整 Schema 定义与旧初始化 fallback。
- **约束与边界**：保留 `modernc.org/sqlite` 纯 Go 驱动与单 Server 文件锁；迁移仍在取得
  `<database_path>.server.lock` 之后执行，未新增跨进程租约或额外仲裁。不兼容旧的手写 Schema 数据库，
  不做旧库识别、数据回填或 `user_version` 转换。
- **工具链**：Go 最低版本提升到 `1.25.7`（Goose v3.27.2 的要求），`modernc.org/sqlite` 随之升级到
  `v1.53.0`，仍是纯 Go 驱动。
- **验证（本任务明确授权测试）**：`go test -count=1 ./tests/unit/store` → 226 passed；
  `go test -count=1 ./tests/unit/app ./tests/unit/server` → 408 passed；
  `go test -race -count=1 ./tests/unit/store ./tests/unit/app ./tests/unit/server` → 634 passed；
  `go vet ./...`、`git diff --check` 无输出；`go-test-groups.sh --check --declared-groups unit,local,e2e`
  通过（31 个测试包，本阶段未新增测试包）。新增的 `tests/unit/store/migrations_test.go` 只确认全新数据库
  能完成初始迁移、核心表可读、迁移记录已写入，且重复打开不会重复执行。

M2 状态继续为 `pending`：本阶段只替换 Schema 初始化基础设施，不涉及规划轮次编排，也不改变 M2 的退出条件
与完成判定。

### 7.16 已交付：规划 Pi 执行结果不确定时的受控容器终止（M2 第十六阶段）

本阶段关闭"取消上下文不能证明远端 Pi 进程已经终止"这一代码缺口。规划调用执行的是远程宿主机上的
`docker exec`，本端 SSH 会话结束不等于 Docker 容器内的 Pi 与子进程退出；因此规划容器在结果不确定时
被显式停止并以实际检查结果核验，而普通 Task 的容器执行、超时与取消行为保持不变。

**规划专用容器停止策略（显式事实，不做推断）**

- `ContainerAgentRequest` / `agent.RunRequest` 新增 `PlanningSessionID` 与 `StopContainerOnUncertain`；
  只有 `PlanningAgentService.RunRole` 设置它们，其余调用方（Step / Workflow / Validation）保持零值。
- `RunRequest.Validate` 要求启用策略时必须给出容器目标与合法规划会话标识，缺失即拒绝调用。
- 触发条件全部来自显式事实：`ContainerExecResult.Started` 为真、`Certain` 为假、策略已启用、规划会话
  标识非空。命令未启动、执行前已取消、已明确正常退出或明确非零退出都不触发终止。
- `agent.ContainerExecResult` 增加 `Started`，由 `docker.CommandResult.Started` 透传，使"命令根本没跑
  起来"与"命令跑过但结果不确定"可区分。

**停止流程与确认顺序**

```text
ContainerRunner.Run（Started && !Certain）
  └─ 独立有界清理上下文 context.WithoutCancel + 90s
       └─ containerSessionExecutor.TerminateContainer
            └─ ContainerService.stopContainerLocked（已持容器锁与远程会话）
                 1. 重新读取本地容器记录
                 2. 核验受管理（detached / removed 拒绝）
                 3. 核验远程容器 ID 与本地 DockerID 一致
                 4. 核验 Docker 标签所有权（含规划会话标签）
                 5. 已停止 → 直接按核验结果收敛，不重复提交停止命令
                 6. docker stop（1 秒宽限期，结束后由 Docker 强制终止容器内全部进程）
                 7. docker inspect（唯一判定依据）
                 8. 记录 stopped + 真实 DockerState + 远程登记清单
```

- 停止成功只由第 7 步的实际检查结果判定；`docker stop` 返回错误但容器确实已停止时按已核实的远端事实收敛，
  同时照常更新本地记录与远程清单。
- 停止后仍确认在运行：返回失败，容器保持原管理状态，不虚构 `stopped`。
- 停止后仍无法核验：容器记录标记 `unknown`，错误带失败阶段说明；不清空 `HostID` / `ContainerID` /
  `SourceCommit`，不自动重复发送停止命令。
- 停止不删除容器与快照卷，也不释放规划会话；它只中断本轮执行。

**避免在临界区内重复取同一把锁**

`ContainerService.RunAgent` 执行期间已持有 `ContainerService.locks` 的 per-container keyed lock。公开的
`StopContainer` 会再次获取同一把锁并自锁，因此本阶段把停止流程抽成持锁内部方法 `stopContainerLocked`：
公开入口与 `RunAgent` 终止路径共用它，终止路径不取锁、不新建远程会话。`RunAgent` 路径的停止因此不会
产生自锁、锁泄漏或死锁。

**终止结论的表达**

- `agent.RunResult` 增加 `RemoteTerminationConfirmed` 与 `RemoteTerminationError`，并新增
  `agent.ErrRemoteTermination`：执行状态（`cancelled` / `timed_out` / `failed`）与远端终止结论是两件事实，
  三种情况可区分：未触发终止 / 已确认停止 / 仍然不确定。
- 已确认停止时不再从已停止的容器导出 HTML 或复制 Session：本轮单轮 Session 记为缺失，本机最后一份
  完整稳定 Session 归档保持不变（`promoteStableSession` 在缺少本轮 Session 时不覆盖快照），既不删除也不
  被本轮中断覆盖。
- 无法确认停止时如实返回终止错误，不停止第二次、不启动第二次 Pi 调用。

**同一容器重新启动与历史 Session 恢复**

- 容器记录为 `stopped` 时，`PlanningAgentService.ensurePlanningAgentContainer` 复用
  `PlanningRuntimeService.prepareHeld`：`reusePlanningContainer` 发现容器已停止后按原容器 ID 调用既有
  `ContainerService.StartContainer` 并核验运行状态，随后仍从本机最后一份完整 Archive 恢复到容器内固定
  Session 路径，再启动 Pi。
- 不创建第二个规划容器，不新建 PlanningSession，不改写角色会话标识；`HostID` / `ContainerID` /
  `SourceCommit` 保持不变。状态为 `unknown` 的容器不在此自动重启，调用方需先用 `InspectPlanningRuntime`
  核验远端事实。

**测试缝隙**

- `app.SSHOptions.Connector` 是唯一新增的测试缝隙：生产装配始终留空，`SSHService.Connect` 仍走
  `sshclient.Dialer`（主机密钥校验、私钥认证、known_hosts）；只有单元测试注入受控连接替身，其余远程操作
  仍经过同一个 `sshclient.Client` 与 `docker.Runner`，不存在第二条 SSH / Docker 调用链。
- 未新增 PID 文件、PID 表、进程租约、owner / epoch / TTL、远端进程注册表或后台监控进程；终止边界是
  "停止本会话独占的容器"，不通过进程名匹配或 PID 猜测结束远端进程。

**测试范围与实际结果（本任务明确授权测试）**

- 新增 `tests/unit/agent/container_termination_test.go`（9 个顶层用例，取消 / 超时两个子用例）：正常完成不终止、普通 Task 取消与超时不终止、
  结果不确定只终止一次、终止使用独立且有上限的清理上下文、命令未启动不终止、明确退出不终止、无法确认
  停止时如实上报、停止后不覆盖最后一份完整归档。
- 新增 `tests/unit/app/container_remote_fixture_test.go`（受控远程现场：真实 `ContainerService` +
  真实 `docker.Runner` + 替身 `sshclient.Client` / `FileTransfer`，远程文件映射到测试工作空间）与
  `tests/unit/app/container_stop_test.go`（11 个用例）：停止后按核验结果更新本地记录与远程清单、已停止幂等
  收敛、`docker stop` 报错但实际已停止时收敛、停止后仍在运行不标记 stopped、无法确认时标记 unknown 且保留
  规划会话绑定、detached / 记录缺失拒绝停止、`RunAgent` 内终止不死锁、标签不一致与 Docker ID 不一致拒绝
  停止、普通任务调用不停止容器。
- 新增 `tests/unit/app/planning_recovery_test.go`（3 个用例）：`Prepare` 按原容器 ID 重新启动已停止容器、中断后重启并恢复历史 Session
  （逐轮断言 Pi 主调用前容器内已有 Session）、容器记录与快照卷只保留一份、绑定与归档在重启前后一致、
  终止不确定时只调用一次 Pi 且另一个规划会话的容器不受影响。
- 已运行：

  ```bash
  go test -count=1 ./tests/unit/agent ./tests/unit/app ./tests/unit/app/planningagent
  go test -race -count=20 ./tests/unit/agent -run 'ContainerRunner'
  go test -race -count=1 ./tests/unit/agent ./tests/unit/app ./tests/unit/app/planningagent ./tests/unit/server
  go test -count=1 ./tests/unit/...
  go test -count=1 ./tests/integration/local/...
  go vet ./...
  git diff --check
  .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e
  ```

  结果：前三组分别为 465 / 220 / 470 passed（`-race` 无数据竞争报告）；`./tests/unit/...` 1598 passed；
  `./tests/integration/local/...` 242 passed；`go vet`、`git diff --check` 无输出；测试分组核验通过
  （31 个测试包全部分类，本阶段未新增测试包）。用例使用 `TestMain` + `testkit` 随机工作空间，产物保留在
  `TEST_ARTIFACT_DIR`，不写入仓库与用户数据目录。

**仍未完成的真实环境验收（不得报告为通过）**

- 真实 Docker 环境下取消后的容器停止确认（`docker stop` → `docker inspect` → 记录同步的真实结果）。
- 真实 Pi 多轮上下文恢复，以及容器停止、重新启动后恢复历史 Session 的真实行为。
- 真实 SSH 断开与远端停止的结果收敛（包括停止命令报错但容器实际已停止的场景）。
- 上述验收需要真实 Docker / SSH / Pi / 远程主机才能成立，本阶段的受控替身测试**不构成**真实环境验收；
  本阶段也没有连接真实远程环境、没有设置真实测试授权开关、没有关闭 SSH 主机密钥校验。

M2 状态继续为 `pending`：退出条件仍缺"真实环境中的只读写入拒绝、Session 与容器生命周期可持久化恢复"
的证据，以及 M3 编排接入后在真实环境复用同一容器与快照的多轮验证。本阶段只补齐代码侧终止能力、
受控测试与文档，不改变 M2 的完成判定。

### 7.17 已交付：规划容器停止状态对账与中断恢复（M2 第十七阶段）

第十六阶段让规划容器在 Pi 结果不确定时被受控停止，但仍有一个恢复边界没有闭环：远端容器已经停止，
本地数据库里的 `Container.State` 可能仍是 `running`。典型现场是"`docker stop` 成功、`docker inspect`
确认容器已退出，但本地 `UpdateContainer` 失败或 Server 在写库之前崩溃"，此时下一次 `RunRole` 会按
本地记录以为容器还在运行，直到前置核验失败而停在错误上；即使进入 `StartContainer`，`running → running`
也是非法迁移。

本阶段把这条恢复链补齐，规则只有一条：**Docker 实际状态是"容器是否仍在运行"的最终依据，本地管理
状态必须先与之一致才能执行启动**。生产代码只修改 `ContainerService` 与 `PlanningAgentService` 的既有
生命周期方法，新增一个只承载状态对账的文件 `internal/app/container_state_reconcile.go`，没有新增容器
状态字段、缓存表、迁移脚本、租约、后台恢复进程或第二套容器生命周期服务。

**允许收敛的远端事实（正例与反例都显式列出）**

- 只有 `exited` 与 `created` 属于"容器确实没有在运行且可以安全再次启动"，才允许进入启动前的状态收敛。
- `paused` / `restarting` / `dead` / `removing` / `unknown` 一律拒绝自动启动：它们既不表示容器已经正常
  退出，也不保证再次启动能拿到可用的运行环境。拒绝时不改写管理状态、不提交 `docker start`，错误文本
  说明远端状态不在可安全启动的退出状态。
- `docker inspect` 失败、SSH 断开或状态无法确认时同样不写 `stopped`、不启动、不调用 Pi，保留现有记录
  并返回明确错误。

**启动前的状态对账（`ContainerService.StartContainer`）**

```text
StartContainer（持有容器锁与远程会话）
  1. 重新读取本地记录 → 核验受管理（detached / removed 拒绝）
  2. docker inspect 取得真实状态 → 核验 Docker 容器 ID 与本地记录一致
  3. 远端已在运行 + 本地 running → 幂等成功，不重复提交启动命令
  4. 远端未运行 + 远端状态 ∈ {exited, created}：
       · stopped / created → 无需收敛
       · running / unknown → MarkStopped + SyncDockerState → 落库 → 同步远程登记清单
       · 其他管理状态     → 拒绝启动（不猜测、不覆盖）
  5. 收敛成功后才执行 docker start → 再次 inspect 确认运行 → MarkRunning → 落库 → 同步清单
```

- 管理状态迁移全部经 `Container.MarkStopped` / `Container.MarkRunning` / `Container.SyncDockerState`，
  没有任何直接赋值 `container.State = stopped`。
- 收敛失败（本地写入失败或清单同步失败）立即返回：**不会**执行 `docker start`，也不会报告为已恢复。
  写入失败时数据库保持原样，清单同步失败时保留已落库的 `stopped`（不回退成 `running`）并返回清单
  错误，下一次调用按真实状态继续收敛。
- 同一容器并发启动由既有 per-container 键控锁串行化：第一个调用完成收敛与启动，第二个观察到
  "远端 running + 本地 running"直接幂等返回，因此只提交一次启动命令。
- `reconcileBeforeStart` / `recordStartedContainer` 都是持锁内部方法，不在容器锁内再次调用公开入口，
  也不会重复打开远程连接。

**规划调用的恢复路径（`PlanningAgentService`）**

```text
PlanningAgentService.RunRole
  → PlanningSessionCoordinator（整段持锁）
  → 读取 PlanningSession 与原容器记录
  → observePlanningAgentContainer：节点 / 受管理 / Docker 所有权 / 规划会话标签 + docker inspect
  → 远端 running            → 直接使用（一次 inspect）
  → 远端 exited / created   → restartPlanningAgentContainer
       → PlanningRuntimeService.prepareHeld → reusePlanningContainer
       → ContainerService.StartContainer（先对账为 stopped，再 start，再核验 running）
       → 复用原容器 ID，从本机最后一份完整 StableSession Archive 恢复，再调用 Pi
  → 其他远端状态            → 前置条件错误，不自动启动
```

- 分支不再由本地 `Container.State` 决定：`stopped`、`running`、`unknown` 与远端不一致时都走同一条
  对账路径，因此"停止已生效但记录没落库"和"Server 中断"共用同一套恢复逻辑。
- 不创建第二个容器、不新建 PlanningSession、不改写 `HostID` / `ContainerID` / `SourceCommit`、不重置
  两个角色的 Session 标识、不删除或重新 Clone 快照卷、不把中断的调用提升为新的完整归档。
- 归属核验先于状态判断：执行节点、受管理状态、Docker 所有权标签与规划会话标签任一不成立都拒绝继续，
  "远端恰好已退出"不构成绕过所有权检查的理由。

**终止后的容器记录读取（`ContainerService.RunAgent`）**

- 只要本轮触发过远端终止流程（`RemoteTerminationConfirmed` 为真或 `RemoteTerminationError` 非空），
  就重新读取本地记录：停止成功时返回 `stopped`，无法确认时返回已落库的 `unknown`，不再返回调用前的
  内存副本。
- 读取使用 `context.WithoutCancel` 派生的独立上下文并带 5 秒上限（`containerStateReloadTimeout`），
  因此用户取消、Server 停止或调用超时都不会让读取立即失败；读取只访问本地 Store，不重新建立 SSH 连接。
- 读取失败时保留最后一份可用记录，并通过新的结构化字段 `ContainerAgentResult.ContainerStateReloadError`
  给出可诊断说明；它不覆盖原始 Pi 取消 / 超时结论，也不把本地读取失败写成 `RemoteTerminationError`
  （那会误报成远端进程终止失败）。
- 三种事实保持独立表达：远端容器已停止（`RemoteTerminationConfirmed`）、本地管理状态已持久化
  （记录的 `State` 与 `ContainerStateReloadError`）、远程登记清单已同步（终止流程返回的错误）。

**测试范围与实际结果（本任务明确授权测试）**

- 扩展 `tests/unit/app/container_remote_fixture_test.go`：新增受控存储故障替身 `faultStore`（包装真实
  SQLite，只在显式启用时让 `UpdateContainer` 失败）、远端状态覆盖 `setDockerState`、
  `setInspectUncertain`、`setRegistryWriteFailure`、`setPiPrintHook` 与 `volumeCount`，并让
  `newRemoteContainerFixtureWithStore` / `newPlanningRecoveryFixtureWithStore` 支持注入存储故障；
  没有第二套测试夹具。
- 新增 `tests/unit/app/container_start_state_test.go`（10 个顶层用例，远端不可启动状态含 4 个子用例）：
  本地 running 远端 exited 收敛后启动、本地 unknown 远端 exited、远端 created、已在运行时的幂等、
  paused / restarting / dead / removing 拒绝启动、inspect 失败不改状态、本地写入失败不启动且恢复写入
  条件后可继续收敛、清单同步失败保留已落库 `stopped` 且下一轮可继续、Docker 容器 ID 不一致拒绝启动、
  并发启动只提交一次启动命令。
- 扩展 `tests/unit/app/container_stop_test.go`（2 个用例）：Pi 主命令执行期间取消调用方上下文后仍读回
  已落库的 `stopped`；无法确认停止时返回已落库的 `unknown` 而不是旧的 `running`。
- 扩展 `tests/unit/app/planning_recovery_test.go`（新增 6 个顶层用例，含 5 个子用例）：数据库仍为 running 但
  远端已 exited 时沿用原容器 ID 与 Docker ID 恢复、本机归档恢复先于 Pi 主调用、容器与快照卷各只有一份、
  归档内容不变；停止已确认但本地写入失败时如实报告且下一轮重新收敛（不重复停止）；Server 重启（服务
  实例与协调器全部重建）后通过同一套核验与收敛路径恢复；标签不一致 / Docker 容器 ID 不一致 / 已脱离
  管理 / 远端 paused / 远端状态无法确认时一律拒绝恢复且不提交启动命令；恢复与 Pi 调用期间同会话的
  另一处操作必须排队，说明 `PlanningSessionCoordinator` 仍然覆盖整段恢复过程。
- 已运行（本任务范围内全部通过）：

  ```bash
  go test -count=1 ./tests/unit/app ./tests/unit/app/planningagent ./tests/unit/agent
  go test -race -count=20 -timeout 40m ./tests/unit/app -run 'Container|PlanningRole|PlanningRuntime'
  go test -race -count=1 ./tests/unit/app ./tests/unit/app/planningagent ./tests/unit/agent ./tests/unit/server
  go test -count=1 ./tests/unit/...
  go vet ./...
  .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e
  git diff --check
  go test -count=1 ./tests/integration/local/...
  ```

  结果：492 / 2560 / 497 / 1625 passed（`-race` 无数据竞争报告）；`go vet` 与 `git diff --check`
  无输出；测试分组核验通过（31 个测试包全部分类，本阶段未新增测试包）；
  `./tests/integration/local/...` 242 passed。`-race -count=20` 需显式提高 `-timeout`：默认 10 分钟
  包超时会在重复运行中触发测试超时，这不是用例失败。
- 用例使用 `TestMain` + `testkit` 随机工作空间，不写仓库与用户数据目录；未连接真实 Docker、SSH、Pi 或
  远程主机，未设置真实测试授权开关，未关闭 SSH 主机密钥校验，未修改 Goose 迁移，也未操作本地开发数据库。

**仍未完成的真实环境验收（不得报告为通过）**

- 真实 Docker 下"停止成功但本地写库失败"后的重启收敛（`docker stop` → `docker inspect` → 写库失败 →
  再次调用的真实结果）。
- 真实 Server 崩溃后同一规划容器的收敛与历史 Session 恢复。
- 真实远程环境下 `paused` / `restarting` / `dead` 容器被拒绝启动的实际行为。
- 真实取消后 `RunAgent` 返回记录的读取（独立上下文的真实数据库行为）。

上述验收需要真实 Docker / SSH / Pi / 远程主机才能成立，本阶段的受控替身测试**不构成**真实环境验收。

M2 状态继续为 `pending`：退出条件仍缺"真实环境中的只读写入拒绝、Session 与容器生命周期可持久化恢复"
的证据，以及 M3 编排接入后在真实环境复用同一容器与快照的多轮验证。本阶段只补齐代码侧的状态对账与
中断恢复能力、受控测试与文档，不改变 M2 的完成判定。

### 7.18 已交付：统一 Docker 容器状态判定（M2 第十八阶段）

生产代码在判断"容器是否可以执行 / 是否可以恢复 / 是否已经停止"时依赖 `State.Running` 布尔值。Docker
Engine 的状态模型里 `State.Status` 与 `State.Running` **不是互斥的同一个事实**：暂停与重启中的容器同样
报告 `Running=true`，因此只凭 `Running` 会把 `paused` 容器当成正常运行，进而允许继续执行 `docker exec`、
发起 Pi 调用或进入规划 Agent 操作。本阶段把判定收敛到一处，并让所有生产调用方改用统一语义。

**Docker 状态语义（本阶段的事实基线）**

```text
paused       {"Status": "paused",     "Running": true,  "Paused": true}
restarting   {"Status": "restarting", "Running": true}
exited       {"Status": "exited",     "Running": false}
```

- `paused`：容器内的进程仍然存在但被挂起，**既不能执行命令，也不代表容器已经退出**。把它当作运行中会
  允许 Pi 调用；把它当作已停止会写入错误的 `stopped` 并掩盖现场。
- `restarting`：容器正在重启，状态不稳定，既不可执行也不可自动启动；自动启动只会与 Docker 自己的重启
  策略竞争。
- 字段矛盾（`running` + `Running=false`、`exited` + `Running=true`、`created` + `Running=true`）说明
  观测本身不可信，一律失败关闭，不得降级解释为正常运行或正常退出。

**统一判定（`internal/domain/container_docker_condition.go`）**

| Docker Status | Running | 判定 | 可执行 | 可自动启动 |
|---|---|---|---|---|
| running | true | `running` | 是 | 否（已在运行） |
| running | false | `indeterminate` | 否 | 否 |
| exited | false | `exited` | 否 | 是 |
| exited | true | `indeterminate` | 否 | 否 |
| created | false | `created` | 否 | 是 |
| created | true | `indeterminate` | 否 | 否 |
| paused | 任意 | `paused` | 否 | 否 |
| restarting | 任意 | `restarting` | 否 | 否 |
| dead | 任意 | `dead` | 否 | 否 |
| removing | 任意 | `removing` | 否 | 否 |
| unknown / 无法识别 | 任意 | `indeterminate` | 否 | 否 |

- `domain.ClassifyDockerContainerCondition(status, running)` 是唯一判定入口；`docker.ContainerInspect` 在
  它之上提供 `RuntimeCondition()`、`IsActivelyRunning()`、`IsConfirmedExited()`、`IsStartable()`，业务
  层不得再直接比较 `Running`。
- `IsActivelyRunning` 只对 `running` 且 `Running=true` 为真；`IsConfirmedExited` 只对 `exited` 且
  `Running=false` 为真；`ConfirmsNotRunning` / `IsStartable` 只接受 `exited` 与 `created`（且
  `Running=false`）。暂停、重启中、dead、removing 与不确定状态既不属"未在运行"，也不可自动启动。
- `docker inspect` 解析保持 Docker 原样：`State.Status` 未知时解析为 `DockerStateUnknown`，不推断成
  `running` 或 `exited`；`State.Running` 原样保留供诊断与一致性判定。

**受影响的普通容器调用路径**

- 创建后运行核验（`ContainerService.createLocked`）、`StartContainer`、`Exec`、
  `sessionContainerGuard.EnsureExecutable`、`RunAgent` 执行前核验：只有 `IsActivelyRunning` 为真才继续。
- `StartContainer` 启动前对账（`reconcileBeforeStart`）：可启动集合由 `IsStartable` 给出，`paused` /
  `restarting` / `dead` / `removing` / 不确定状态与字段矛盾一律拒绝，不改写管理状态、不提交 `docker start`。
- `stopContainerLocked`：不再用 `!Running` 认定"已经停止"。只有 `ConfirmsNotRunning`（已确认退出或从未
  启动）才直接收敛；否则照常提交 `docker stop`，并以停止后的实际 `inspect` 判定，未确认退出时不写
  `stopped`、不伪造停止成功（`paused` / `restarting` / 字段矛盾分别给出可诊断错误）。
- `TerminateContainer`：只有已确认未在运行才报告终止完成；`paused` 停止后仍暂停时不报告终止确认。
- `ExecutionEnvironmentService` 状态判定、环境预检、代码初始化、步骤执行、步骤工作区、任务启动、任务工作流、
  验证与续跑资格：全部改用 `IsActivelyRunning`，暂停容器不再被当成可执行环境。
- CLI `docker inspect` 输出新增"观测判定"，"观测运行中"按 `IsActivelyRunning()` 显示。

**受影响的规划调用路径**

- `PlanningRuntimeService.reusePlanningContainer`：只有已确认运行中才直接复用；`exited` / `created` 才进入
  既有重启流程；暂停、重启中、dead、removing 与字段矛盾返回 `ErrPlanningRuntimeConflict` 且不改写管理状态。
- `PlanningAgentService.ensurePlanningAgentContainer` / `verifyPlanningAgentContainer`：可复用与可恢复集合
  分别由 `IsActivelyRunning` 与 `IsStartable` 给出；其余状态返回前置条件错误，不自动启动、不调用 Pi。
- `ContainerService.RunAgent` 追加执行前核验：`observe` 的结果不是 `IsActivelyRunning` 时直接失败，Pi 主
  调用不会被提交（`sessionContainerGuard` 仍然是容器内命令前的第二道核验）。
- `PlanningRuntimeContainerInspection.Running` 改由 `IsActivelyRunning()` 填充，"Running" 字段不再可能把
  暂停容器报告为运行中。
- 一次性准备容器核验与快照流程改用同一判定：仍在运行的准备容器继续拒绝删除，未确认退出的状态继续按
  "无法确认"拒绝操作。

**容器残留观测与恢复**

- 新增观测结论 `domain.ContainerObservedIndeterminate`：暂停、重启中、dead、removing、未知状态与字段矛盾
  的容器既不是"正在运行"也不是"已确认退出"。
- `ClassifyContainerObservation` 只对 `IsActivelyRunning`（→ running）与 `ConfirmsNotRunning`（→ stopped）
  给出肯定结论，其余统一落到 `indeterminate`，不再把暂停折叠成 `stopped`。
- `DecideContainerRecovery` 对 `indeterminate` 返回"保持现状 + 需要人工"（`needs_manual`，不产生收敛目标）：
  残留收敛不会自动启动、删除或接管这些容器，也不会清空诊断摘要。

**测试替身修复**

- `tests/unit/app/container_remote_fixture_test.go` 的 `setDockerState` 改为
  `setDockerState(dockerID, status, running)`：`State.Status` 与 `State.Running` 必须分别给出，替身不再用
  "state == running" 推导 `Running`，因此可以构造 `paused + Running=true`、`restarting + Running=true` 与
  各类矛盾组合；`docker inspect` 输出同时给出 `Paused` / `Restarting` 辅助标志，与真实输出结构一致。
- 新增 `tests/internal/testkit` 的 `ContainerInspectFor(dockerID, running)`：只关心"运行中 / 未运行"的既有
  替身用它同时给出自洽的 `State.Status` 与 `State.Running`，22 处替身不再只设置 `Running`。

**测试范围与实际结果（本任务明确授权测试）**

- 新增 `tests/unit/container/docker_status_test.go`：`docker inspect` 状态矩阵（running / exited / created /
  paused / restarting / dead / removing / 未知取值 × `Running` 真假共 14 个用例）与判定函数全覆盖校验。
- 扩展 `tests/unit/app/container_start_state_test.go`：拒绝启动的状态扩展到 paused（两个 `Running` 取值）、
  restarting（两个取值）、dead、removing、未知状态与三种字段矛盾，并断言不提交 `docker start`、不提交
  `docker stop`、不触发容器内执行、管理状态与观测状态都不被改写。
- 新增 `tests/unit/app/container_exec_state_test.go`：`Exec` 在 paused / restarting / 字段矛盾 / 未知状态下
  拒绝且不提交 `docker exec`；`RunAgent` 在同样状态下不发起 Pi 主调用；确认运行中的对照用例正常执行一次
  `docker exec`；`docker inspect` 无法确认时不产生任何容器内执行；并发 `Exec` 与 `Stop` 在同一把
  per-container 键控锁上串行、有界返回，且只提交一次停止命令。
- 新增 `tests/unit/app/container_recovery_state_test.go`：观测结论矩阵、`indeterminate` 不产生收敛目标，
  以及 `ContainerRecoveryService` 对 paused / restarting / dead / removing / 字段矛盾保持原记录且不启动、
  不停止、不删除；已确认退出的对照用例仍收敛为 `stopped`。
- 扩展 `tests/unit/app/container_stop_test.go`：暂停容器必须真正提交一次停止命令（不得被当作已停止）；停止后
  仍暂停 / 仍在重启 / 状态矛盾时不写 `stopped`；并发停止同一容器只提交一次停止命令。
- 扩展 `tests/unit/app/planning_recovery_test.go`：规划调用在远端 paused（两个取值）、restarting、dead、
  removing、字段矛盾时拒绝恢复且不提交启动、不发起 Pi 调用、不创建第二个容器、绑定不变；`Prepare` 对
  paused / restarting / 字段矛盾同样拒绝并保留绑定。
- 已运行（本任务范围内全部通过）：

  ```bash
  go test -count=1 ./tests/unit/container                       # 49 passed
  go test -count=1 ./tests/unit/app                             # 499 passed
  go test -count=1 ./tests/unit/...                             # 1696 passed / 14 packages
  go test -count=1 ./tests/integration/local/...                # 242 passed / 10 packages
  go test -count=1 ./tests/e2e/...                              # 65 passed
  go test -race -count=1 ./tests/unit/...                       # 1696 passed
  go test -race -count=1 ./tests/integration/local/ssh/... \
       ./tests/integration/local/workflow/...                   # 114 passed
  go test -race -count=20 ./tests/unit/app -run 'Container|Planning'   # 4220 passed
  bash .github/scripts/check-go-static.sh                       # gofmt / go vet / go build 通过
  bash .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e  # 31 个测试包全部分类
  ```

  前端（本阶段未修改前端代码）：`pnpm test:unit:safe` → 291 passed；`pnpm check`（vp fmt / vp lint --fix /
  tsc --noEmit）通过；`pnpm build` 通过。Playwright 浏览器 E2E 未在本机执行：本机未安装 Chromium，属环境限制，
  不计为通过。

**本次失败的测试与修复记录**

- `internal/app/container_observation.go` 首次改动后多出一个右花括号，`go vet` 报语法错误 → 修正函数闭合位置。
- `TestClassifyContainerObservationStatusMatrix` 的 `created + Running=false` 等用例漏填期望值，导致期望为
  空字符串 → 补全期望结论。
- `TestContainerServiceRunAgentRequiresActivelyRunning` 断言 "不可执行"，实际错误文本是 "未处于可执行状态"
  → 修正断言。
- `TestContainerServiceStopRefusesToRecordStoppedForUnconfirmedStates` 断言读取远程登记清单，但该现场从未
  写过清单文件 → 改为断言未提交 `docker start`。
- 既有多处测试替身只设置 `docker.ContainerInspect{Running: ...}` 而没有 `State`，在新语义下被判为未知状态，
  导致 `TestWorkflowCloseoutRequiresSessionArchive`、`TestGeneratePlanRejectsInvalidOutput` 与若干步骤执行
  超时用例失败 → 统一改用 `testkit.ContainerInspectFor(dockerID, running)` 让替身同时给出自洽的两个字段。

**仍未完成的真实环境验收（不得报告为通过）**

- 真实 Docker 下暂停容器的真实字段组合（`docker pause` 后 `docker inspect`）与拒绝执行 / 拒绝启动的实际行为。
- 真实 `docker stop` 打在暂停容器上的实际结果（报错 / 生效），以及本阶段"未确认退出即不记录 stopped"的
  真实收敛表现。
- 真实 `restarting` 容器（重启策略触发的窗口）与 `dead` / `removing` 现场下的拒绝路径。
- 真实环境下的规划容器恢复、Session 归档恢复与远端 Pi 终止。

上述验收需要真实 Docker / SSH / Pi / 远程主机才能成立，本阶段的受控替身测试**不构成**真实环境验收。

M2 状态继续为 `pending`：退出条件仍缺"真实环境中的只读写入拒绝、Session 与容器生命周期可持久化恢复"的
证据，以及 M3 编排接入后在真实环境复用同一容器与快照的多轮验证。本阶段只统一容器状态语义、补齐受控测试与
文档，不改变 M2 的完成判定，也不把剩余工作转入 M3。

### 7.19 已交付：真实环境验收与必要缺陷修复（M2 第十九阶段）

第十九阶段在**真实环境**中验收规划运行时：真实文件型 SQLite、真实 SSH（主机密钥校验开启）、真实远程
Docker Engine、真实只读规划容器、真实固定 commit 快照卷、真实容器内 Pi CLI 与真实模型。验收过程发现
两处**生产缺陷**并已在本阶段修复，另发现一处**测试基础设施缺陷**（真实测试执行入口的 `go test` 超时
与套件预算不匹配）并修复。M2 状态仍为 `pending`，理由见本节末尾。

#### 7.19.1 真实环境事实（本次实测）

| 项目 | 实测值 |
|---|---|
| Docker CLI / Engine | 29.7.2 / 29.7.2 |
| Docker API / 最低 API | 1.55 / 1.40 |
| 测试镜像 | `agentflow-test-planning:1.0.0`（`sha256:6efc22d7…2d6d64`），基础为 `agentflow-test-agent:1.0.0` |
| 容器内 Pi CLI | 1.3.0（`pi --version`，容器内实测） |
| 容器内 Git | 2.39.5 |
| 模型标识（不含凭据） | `group/free`，思考等级 `low` |
| 测试项目固定 commit | `92f6432678902c7532b27c713dbbd026cbbed5a0`（`main`） |
| 远程测试资源目录 | `~/.lingting/agentflow-tests/planning/<随机后缀>` |
| 本机测试产物 | `%TEMP%\lingting-agent-flow-tests\planning-*`（保留，不删除） |
| 真实测试产物目录 | `.test-artifacts/real-m2/`（`TEST_ARTIFACT_DIR` 见 `real-tests-output.log`） |

#### 7.19.2 生产缺陷与修复

**缺陷一：只读规划容器的 Session 提取与恢复不可用。**

`docker cp` 只解析容器 rootfs 层，看不到挂在 tmpfs 上的路径。规划容器 `--read-only` 且唯一可写目录是
`/tmp`（tmpfs），因此：

- `docker cp <容器>:/tmp/agentflow/sessions/...` 报"容器内不存在该文件"，`persistSession` 判定"未能取得
  Session 文件"，规划角色调用在**真实成功执行 Pi**之后仍被判定失败；
- 反方向的 `docker cp <宿主机文件> <容器>:/tmp/...` 在只读 rootfs 上直接失败，历史 Session 无法恢复到
  容器内固定路径，多轮上下文恢复不可能成立。

修复（`internal/app/agent_container.go`）：`containerSessionExecutor.CopyFromContainer` /
`CopyToContainer` 在 `docker cp` 失败时退回宿主机重定向方式——`docker exec cat <容器路径> > <宿主机路径>`
与 `docker exec -i sh -c 'cat > <容器路径>' < <宿主机路径>`。文件内容仍由远程宿主机落盘/读取，不经过本
进程；命令由固定子命令与 `sshclient.ShellCommand` / `ShellQuote` 转义后的参数构造。普通任务容器 `docker cp`
成功时行为完全不变。

**缺陷二：SSH 中断后的远端 Pi 终止依赖已失效的连接。**

第十六阶段的终止流程复用本次调用使用的 SSH 会话执行停止。真实 SSH 中断后该会话已失效，`inspect` 与
`docker stop` 都无从执行，容器与容器内 Pi 可能仍在运行，而本地只能记录 `unknown`。这不满足"中断后必须
通过独立连接核验并终止"的要求。

修复（`internal/app/agent_container.go`）：`containerSessionExecutor.TerminateContainer` 在本次会话无法
确认终止时，改为调用新增的 `ContainerService.terminateWithIndependentSession`，用一条**独立连接**重新
读取本地容器记录、核验执行节点 / Docker 容器 ID（必须与本次调用一致）、管理标签与规划会话标签后，只对
该容器提交停止，并以停止后的真实 `inspect` 判定结果；两条路径都未确认时如实返回错误，不写 `stopped`。
容器记录、快照卷与规划会话绑定全部保留，下一次调用按原容器 ID 重启。普通 Task 调用不启用终止策略，
不会进入该路径。

**测试基础设施修复：真实测试入口的 `go test` 超时。**

`.github/scripts/run-real-tests.sh` 原先使用 `go test` 默认的 10 分钟超时，而 `tests/integration/remote/
workflow` 的端到端用例自己声明了 60 分钟运行预算（`e2eRunTimeout`），真实运行需要数分钟到数十分钟，
因此该套件必然被默认超时打断（本次实测在执行到步骤执行阶段后 601.8 秒被 `panic: test timed out` 终止）。
修复：脚本新增 `--timeout DURATION`（默认 `90m`）并传给 `go test -timeout`；超时仍按失败上报，不放宽
任何断言。

#### 7.19.3 真实只读环境验收结果

- 真实 `docker inspect` 取值：`HostConfig.ReadonlyRootfs=true`；项目快照以 `type=volume` 只读挂载到
  `/workspace/project`（`Mounts[].RW=false`，卷名与 `Prepare` 返回的 `CodeVolumeName` 一致）；
  `HostConfig.Tmpfs` 只含 `/tmp`；`Binds` 为空、无 `bind` 挂载、无 Docker Socket、非特权、非 `--pid=host`、
  非 `--network=host`。
- 角色工具策略：`requirement_analyst` 与 `planner` 的实际工具均为 `[read grep find ls]`，不含
  `edit` / `write` / `bash`。
- 真实文件工具拒绝：以 `--tools write` 的独立诊断调用尝试写入 `/workspace/project/...`，Pi 报告
  "目标路径所在的文件系统为只读（EROFS），写入工具无法在此处创建文件"，目标文件不存在。
- 真实 Shell 拒绝：`sh -c 'printf probe > /workspace/project/...'` 退出码 2，
  `cannot create …: Read-only file system`，目标文件不存在。
- 两次写入尝试之后，容器内 `git rev-parse HEAD` 仍为 `92f6432…`，`git status --porcelain` 为空。

#### 7.19.4 真实多轮 Session 结果

- 需求分析师两轮真实调用：角色会话标识、容器内路径 `/tmp/agentflow/sessions/<会话>/<角色>/session.jsonl`
  与本机归档路径全程不变；第二轮 `StableSession.Restored=true`，输出包含第一轮的唯一标记，归档字节数
  由 6774 增长到 7383 之后继续增长。
- 方案规划师独立会话：两个角色的 Pi 会话标识、容器内 Session 文件与本机归档文件互不相同；调用方案规划师
  不改变需求分析师的会话标识。
- 取消的调用：在真实 Pi 开始执行后取消，调用返回错误、容器按终止策略停止，最后一份完整归档字节内容
  **未发生变化**。

#### 7.19.5 真实容器停止、应用服务重建与恢复结果

- 生产 `ContainerService.StopContainer` 停止容器后，独立控制连接确认 `State.Status=exited`。
- 关闭 SQLite 连接与全部应用服务，然后用同一数据库文件、同一 Session 根目录重建服务对象后：容器记录 ID、
  Docker 容器 ID、执行节点、固定 commit、角色会话标识全部不变；快照卷仍存在。
- 用生产 `Prepare` 重新启动同一个容器（记录 ID 与 Docker 容器 ID 均未变化，快照卷与 commit 未变化），
  再执行第二轮真实 Pi 调用：`StableSession.Restored=true`，输出包含停止前记录的标记，归档继续增长
  （6537 → 7867 字节）。
- 范围说明：本项重建的是**应用服务对象与数据库连接**，不是 Server 进程崩溃重启；本阶段没有以子进程
  方式重启 Server 进程，因此不得把本项表述为进程级崩溃验收。

#### 7.19.6 真实 SSH 中断与远端 Pi 终止结果

- 测试用连接包装器只包装**真实认证后的客户端**（主机密钥校验与私钥认证与生产一致），在独立控制连接确认
  容器内 `pi` 进程已启动后关闭该连接（`SSH_CONNECTIONS_BROKEN count=1`）。
- 中断后的调用返回
  `规划角色调用失败: pi: 容器内执行失败（退出码 -1）: 远程 Docker 操作失败(ssh_unavailable): docker exec: 远程命令未能确认结束`，
  且 `RemoteTerminationConfirmed=true`。
- 独立控制连接核验：容器 `State.Status=exited`，容器内已无 `pi` 进程（`REMOTE_PI_ABSENT
  reason=container_not_running`）；本地记录 `state=stopped / docker_state=exited`，规划会话的
  执行节点 / 容器 / 固定 commit 绑定不变；最后一份完整归档未被覆盖。
- 中断后仍可复用同一容器：`Prepare` 重启后第二轮真实调用恢复历史上下文，角色会话标识与本机归档路径不变。
- 本项的终止动作由**独立连接**完成（缺陷二的修复），不再依赖已经中断的会话。

#### 7.19.7 真实资源释放结果

- `PlanningRuntimeService.Release` 返回 `ContainerReleased=true` / `SnapshotReleased=true`；独立控制连接
  确认容器与快照卷都不存在，一次性准备容器也不存在；规划会话的 `ContainerID`、`HostID`、`SourceCommit`
  已清空，规划消息仍然保留。
- 绑定已清空时再次 `Release` 返回成功且不谎称删除过资源（`container_released=false`
  `snapshot_released=false`），远端资源仍然不存在，绑定没有被重新写回。
- 测试包结束时按真实 `docker ps -a` 与命名卷核验：`REMOTE_CONTAINER_VERIFY status=PASS`、
  `REMOTE_VOLUME_VERIFY status=absent`，远程只保留运行前后既有的非测试容器（`pgsql-server`）。

#### 7.19.8 真实 Docker 状态结果

- `paused`：真实 `docker pause` 后 `docker inspect` 给出 `State.Status=paused`、`State.Running=true`、
  `State.Paused=true`。业务侧：`StartContainer` 拒绝（"不在可安全启动的退出状态"，且容器仍为 `paused`）、
  `Exec` 拒绝（"当前未运行，无法执行命令"）、`RunRole` 拒绝且不发起 Pi 调用、`Prepare` 返回
  `ErrPlanningRuntimeConflict` 且绑定与容器状态不变、`ClassifyContainerObservation` 给出
  `indeterminate`、本地记录没有被写成 `stopped`（`state=running docker_state=paused`）。解除暂停后容器
  恢复 `running`。
- `restarting`：用本次测试专用的一次性容器（`--restart=always` 立即退出）捕获到
  `RESTARTING_OBSERVED status=restarting running=true condition=restarting`。
- `dead` 与 `removing`：本次未制造对应真实现场，如实记录为**未观测**，未编造状态。受控测试中的状态矩阵
  （`tests/unit/container/docker_status_test.go` 等）保持不变并通过。

#### 7.19.9 全新数据库的 Goose 初始化结果

`tests/unit/store/migrations_test.go` 通过生产 `sqlite.Open` 在测试工作空间的全新文件上验证：

- 迁移成功执行，`goose_db_version` 含 Goose 基线记录（`version=0, is_applied=1`）与初始脚本记录
  （`version=1, is_applied=1`），没有未执行记录；
- `planning_sessions` / `planning_messages` / `planning_revisions` 的必需字段与
  `idx_planning_sessions_project` / `idx_planning_sessions_status` / `idx_planning_messages_sequence` /
  `idx_planning_revisions_number` 存在；`projects.default_host_id` 与 `idx_projects_default_host` 存在；
- 关闭后用同一文件重新打开：迁移记录数不变、结构与版本记录一致，没有重复执行迁移。
- 未对任何现存开发数据库执行迁移，也未引入旧库兼容、回填或升级逻辑。

#### 7.19.10 新增与调整的测试

- 新增 `tests/integration/remote/planning/`（真实依赖验收入口，自动归入 `remote` 分组）：
  `TestRemotePlanningReadOnlyEnvironment`（只读容器 / 工具策略 / 文件工具与 Shell 写入拒绝 / 释放与
  重复释放）、`TestRemotePlanningMultiRoundSessions`（两角色多轮上下文、角色隔离、取消不覆盖归档）、
  `TestRemotePlanningContainerRestartRecovery`（停止 + 服务与数据库重建 + 同一容器恢复）、
  `TestRemotePlanningSSHInterruptionTermination`（真实 SSH 中断、独立连接核验与终止、恢复）、
  `TestRemotePlanningPausedState`（真实 paused 字段与全部业务拒绝）、
  `TestRemotePlanningRestartingObservation`（restarting 观测，dead/removing 如实记未观测）、
  `TestRemotePlanningPreconditions`（Docker 版本、镜像、容器内 Pi 与 Git 版本、模型、仓库与分支）。
  该包只用真实依赖并复用生产服务装配（`ContainerService` / `PlanningRuntimeService` /
  `PlanningAgentService` / `PlanningSessionCoordinator` / 当前 `storage.Store` / `RoleService`），
  没有第二套容器管理、Pi Runner 或 Session 管理实现。
- 新增 `tests/integration/remote/planning/Dockerfile`（`agentflow-test-planning:1.0.0`）：在通用测试镜像
  之上只设置 `PI_CODING_AGENT_DIR=/tmp/pi-agent`，使只读 rootfs 规划容器仍有可写的 Pi 配置目录；不含任何
  凭据，模型配置由测试在运行期写入容器内 tmpfs。
- `tests/internal/testkit/remotedir.go` 登记 `RemoteTestPackagePlanning`（远程测试子目录白名单的唯一
  来源）。
- 扩展 `tests/unit/store/migrations_test.go`：`goose_db_version` 记录、M2 规划表字段与索引、
  `projects.default_host_id` 与索引、重复打开不重复迁移。
- 修复 `.github/scripts/run-real-tests.sh`：新增 `--timeout`（默认 `90m`）并传给 `go test`。

#### 7.19.11 实际测试执行结果（本次）

```bash
# 定向受控回归
go test -count=1 ./tests/unit/...                        # 1696 passed / 14 packages
go test -count=1 ./tests/integration/local/...           # 242 passed / 10 packages
go test -count=1 ./tests/e2e/...                         # 65 passed / 1 package
go test -race -count=20 -timeout 45m ./tests/unit/app -run 'Container|Planning'   # 4220 passed
bash .github/scripts/run-go-race-tests.sh --artifact-dir .test-artifacts/go-race --parallel 4  # 全部通过
bash .github/scripts/check-go-static.sh                  # gofmt / go vet / go build 通过
bash .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e   # 32 个测试包全部分类

# 真实外部依赖测试（授权后执行，包之间串行）
AGENTFLOW_REAL_TESTS_GRANTED=true bash .github/scripts/run-real-tests.sh \
  --artifact-dir "$PWD/.test-artifacts/real-m2" --parallel 1 --timeout 45m
# pi 4.0s / ssh 6.1s / remote/agent 24.1s / remote/codesaver 14.1s / remote/docker 71.9s
# remote/planning 131.9s / remote/workflow 196.5s —— 全部 ok
```

跳过项（不计为通过）：`tests/integration/pi` 的 `TestRealPiModelRecognition` 类用例输出
`WARN [SKIP] 当前 Pi 未识别模型 group/free`（本机 Pi 1.4.0 的模型清单不含该标识）。规划运行时真实验收
只以容器内 Pi（1.3.0）的实际调用为准。

#### 7.19.12 仍未完成与 M2 状态

- 规划轮次业务编排、结构化输出解析与 Revision 生成仍属 M3，不在本阶段。
- 容器 `dead` 与 `removing` 真实现场本次未制造，如实记为未观测；其拒绝路径由受控测试覆盖。
- Server **进程**级崩溃重启验收未执行：本次只重建应用服务对象与数据库连接，报告时已如实区分。
- M2 状态继续保持 `pending`。第十九阶段补齐了"真实环境只读写入拒绝、多轮 Session 恢复、容器与快照
  生命周期可持久化恢复、SSH 中断后远端 Pi 终止"的证据，但 M2 的正式退出条件还要求 M3 编排接入后在真实
  环境复用同一容器与快照完成多轮规划，因此本阶段不把 M2 标记为 completed，也不把剩余工作转入 M3。

### 7.20 已交付：真实源码读取验收与文件不存在断言加固（M2 第二十阶段）

第二十阶段补齐 §3 退出条件中「AI 能读取真实项目代码」的第一条直接证据：真实规划角色必须在
只读规划容器内**实际读取固定 commit 的源码**并准确回报源码里的取值，而不是仅仅"声称读过"。
本阶段同时修复了一处测试断言缺陷（文件不存在判定可能把"探针自身失败"当成"文件不存在"）。
M2 状态仍为 `pending`，理由见 §7.20.6。

#### 7.20.1 读取验收样本仓库

读取验收要求快照中存在 `internal/` 或 `cmd/` 下的 Go 源文件，而原有测试仓库
`lingting/lingting-agent-flow-test` 的 `main` 固定 commit `92f6432…` 只含 `README.md`，无法建立样本
（按规则不得 Skip 或伪造通过）。本阶段在测试仓库补齐了样本源码并推送：

| 项目 | 值 |
|---|---|
| 仓库 | `https://github.com/lingting/lingting-agent-flow-test.git`（匿名 HTTPS 可访问） |
| 固定 commit | `afe8e9c9b56fca55c48b0c3b4e6a357317987d97`（`main`） |
| 样本文件 | `internal/oracle/constants.go`、`internal/oracle/endpoints.go`、`cmd/oracle-probe/main.go` |
| 样本常量 | 27 个带随机后缀的字符串常量（`>= 16` 字符、含数字后缀） |

样本仓库只提供取值样本，不参与产品逻辑；产品仓库不引入任何测试仓库内容。

#### 7.20.2 新增真实读取验收用例

`tests/integration/remote/planning/source_read_test.go` 新增
`TestRemotePlanningReadsPinnedProjectContent`，全部依赖真实环境与生产装配：真实文件型 SQLite、
真实 SSH（主机密钥校验开启）、真实远程 Docker Engine、真实只读规划容器、生产
`PlanningRuntimeService` / `PlanningAgentService` / `PlanningSessionCoordinator` / `RoleService`、
容器内真实 Pi CLI 与 `docker.pi` 配置模型。没有 Fake Pi、Mock Runner、假文件内容，也没有绕过生产
规划 Agent 直接调用 Pi CLI。

独立 oracle 的构造方式：

1. 生产 `Prepare` 建立固定 commit 的只读快照（本阶段实测 `afe8e9c9…`）；
2. 测试侧通过**独立控制连接**执行只读 git 命令：`git -C /workspace/project ls-tree -r --name-only HEAD`
   取文件清单，`git -C /workspace/project cat-file blob HEAD:<path>` 取真实文件内容；
3. 用 Go AST（`go/parser` + `go/ast`）读取 `const` 声明的字符串字面量，按"长度 `>= 16`、只含
   `[A-Za-z0-9._-]`、含数字"过滤后得到候选池，再用 `crypto/rand` 随机打乱文件顺序并随机取一项；
4. 提示词只包含**源码路径与常量标识符**，不包含取值；用例内另有断言确认提示词中没有出现期望值。

断言：

- 真实 Pi 的 `FinalText` 必须包含独立读取得到的准确取值（本轮实测模型回答 `"oracle-stage-read-1b8f"`）；
- 规划角色有效工具策略仍为 `[read grep find ls]`，不含 `edit` / `write` / `bash`；
- 调用后快照 `HEAD` 仍为固定 commit，`git status --porcelain` 为空；
- 容器记录 ID / Docker 容器 ID、快照卷（只读挂载名）、容器内 Session 路径
  `/tmp/agentflow/sessions/<规划会话>/<角色会话>/session.jsonl`、本机归档
  `<SessionRoot>/planning/<规划会话>/<角色会话>/session.jsonl` 均与固定规则一致，归档结构可解析。

样本缺失或候选池为空时用例直接失败并说明"无法建立读取验收样本"，不 Skip、不伪造通过。

#### 7.20.3 文件不存在断言加固

`tests/integration/remote/planning/readonly_test.go` 的 `assertFileAbsentInContainer` 原先在
"Docker Exec 返回非零但结果不确定"时也可能判为文件不存在。现在改为：

- 探针命令必须在容器内确认执行（`Started`）、确认结束（`Certain`）且按退出码返回；
- 只有 `test -e <path>` 的**精确退出码 1** 才认定文件不存在；
- 退出码 0 表示目标文件存在，用例失败；
- 退出码 2、127、-1 及其他取值均按测试失败上报；
- 连接中断、超时、取消或状态不确定一律不得报告文件不存在；
- 失败信息打印退出码、`started/certain/status`、错误与服务前置校验结果，不包含任何凭据。

退出码语义集中在 `tests/internal/testkit/fileabsence.go`（`ClassifyFileAbsence`），并由新增的
`tests/unit/testkit/fileabsence_test.go` 覆盖：退出码 1 → 不存在、0 → 存在、2/127/-1/3/255 →
失败，`Started` / `Certain` / `Determined` 任一为假时同样返回错误。本阶段真实运行中两次探针均为
`FILE_ABSENT … exit=1`，即加固后仍然成立，且不再存在"探针失败被当成不存在"的通道。

#### 7.20.4 真实环境事实（本次实测）

| 项目 | 实测值 |
|---|---|
| Docker CLI / Engine | 29.7.2 / 29.7.2（API 1.55，最低 1.40） |
| 测试镜像 | `agentflow-test-planning:1.0.0`（`sha256:6efc22d7…2d6d64`） |
| 容器内 Pi CLI | 1.3.0；模型 `group/free`，思考等级 `low` |
| SSH 目标 | `root@127.0.0.1:22`（专用测试设备，主机密钥校验开启） |
| 快照固定 commit | `afe8e9c9b56fca55c48b0c3b4e6a357317987d97`（`main`） |
| 远程测试资源目录 | `/root/.lingting/agentflow-tests/planning/<随机后缀>`（每次用例独立） |
| 本机产物目录 | `TEST_ARTIFACT_DIR=%TEMP%\lingting-agent-flow-tests\planning-*`（保留，不删除） |
| 真实测试产物目录 | `.test-artifacts/real-m2-read-verification/`、`.test-artifacts/real-m2-read-verification-rerun/` |

#### 7.20.5 实际测试执行结果（本次）

```bash
gofmt -w tests/integration/remote/planning
bash .github/scripts/check-go-static.sh                       # 通过
bash .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e   # 33 个测试包全部分类
go test -count=1 ./tests/unit/...                              # 1701 passed / 15 packages
go test -count=1 ./tests/integration/local/...                 # 242 passed / 10 packages

AGENTFLOW_REAL_TESTS_GRANTED=true bash .github/scripts/run-real-tests.sh \
  --artifact-dir "$PWD/.test-artifacts/real-m2-read-verification" --parallel 1 --timeout 45m
AGENTFLOW_REAL_TESTS_GRANTED=true bash .github/scripts/run-real-tests.sh \
  --artifact-dir "$PWD/.test-artifacts/real-m2-read-verification-rerun" --parallel 1 --timeout 45m
```

- 定向运行（仅新用例）：`TestRemotePlanningReadsPinnedProjectContent` 通过，两次运行的随机样本
  不同（`DiagnosticStageRead` / `StepTypeRequirementAnalysis`），说明样本确实随机。
- 第一次全量真实运行：`pi`、`ssh`、`remote/agent`、`remote/codesaver`、`remote/docker`、
  `remote/planning` 全部通过；`remote/workflow` 的 `TestRemoteE2EFullChain` **失败**——
  规划步骤在容器内 Pi 执行 600 秒后 `run.timed_out`（上限 10m0s），任务按设计进入
  `recovery_required`，日志见 `.test-artifacts/real-m2-read-verification/real-tests-output.log`。
  该失败点在本次改动之外（真实模型接入点单次调用停滞），未通过放宽断言或调大套件预算掩盖。
- 复跑：`remote/workflow` 单包复跑通过（4 个用例），随后完整真实套件写入
  `.test-artifacts/real-m2-read-verification-rerun/real-tests-output.log`，7 个真实测试包全部 `ok`
  （`pi` 4.1s / `ssh` 5.9s / `remote/agent` 24.5s / `remote/codesaver` 13.9s /
  `remote/docker` 72.1s / `remote/planning` 156.4s / `remote/workflow` 129.0s）。
- 读取验收证据（复跑原始日志）：`SOURCE_SAMPLE file=internal/oracle/endpoints.go
  const=DiagnosticStageRead value=oracle-stage-read-1b8f commit=afe8e9c9…`，
  `SOURCE_READ_ANSWER "oracle-stage-read-1b8f"`，`SNAPSHOT_CLEAN commit=afe8e9c9…`，
  `ROLE_TOOLS tools=[read grep find ls]`，`SOURCE_READ_BINDING … volume=agentflow-plan-…`。
- 跳过项（不计为通过）：`tests/integration/pi` 的 `TestRealPiModelRecognition` 输出
  `WARN [SKIP] 当前 Pi 未识别模型 group/free`（本机 Pi 1.4.0 的模型清单不含该标识），与第十九阶段
  一致；读取验收只以容器内 Pi 1.3.0 的真实调用为准。
- 未执行项：Server **进程**级崩溃重启验收、容器 `dead` / `removing` 真实现场（与第十九阶段相同，
  本阶段未扩大范围）。

#### 7.20.6 仍未完成与 M2 状态

- 本阶段只补齐读取证据与断言缺陷，不重构规划服务、不扩展 HTTP API、不引入 M3 业务编排。
- 真实模型接入点存在偶发的单次调用停滞（第一次全量运行的 `remote/workflow` 超时即属此类），
  已如实记录为环境侧不稳定，未通过放宽断言处理。
- M2 状态保持 `pending`：§3 的"AI 能读取真实项目代码"已获得直接证据，但正式退出条件还要求
  M3 编排接入后在真实环境复用同一容器与快照完成多轮规划，本阶段不提前标记 `completed`。
