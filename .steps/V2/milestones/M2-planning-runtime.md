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

- `internal/storage/sqlite/schema.sql` 中的 `planning_sessions` / `planning_messages` /
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
- `internal/storage/sqlite`：`schema.sql` 中的三张规划表与四个索引，以及仓储实现；消息
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

- **持久化**：`internal/storage/sqlite/schema.sql` 的 `projects` 表增加
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

- **持久化**：`internal/storage/sqlite/schema.sql` 新增表 `planning_session_leases`（主键
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
- 进程内协调器解决了"同一会话不会被两个入口同时推进"，但没有解决"远端 Pi 进程是否真的已终止"：取消上下文
  与等待收敛只保证本端调用结束，容器内进程的存续仍未核验（见 §7.9 的 fencing 边界）。
- 规划服务的 HTTP API 与前端需求规划工作台（M3）：三个服务只完成装配与生命周期，没有对外入口。
- 撤销与重跑之外的失败恢复入口：结果不确定的运行时现场只能通过再次 `Prepare` 做只读核验；残留的
  一次性准备容器没有专门清理路径；释放与其他会话操作由进程内协调器串行化（见 §7.13），但残留资源仍只能人工处理。
- `Release` 仍不做"上一次释放结果不确定"的自动收敛：远程删除结果无法确认时保留定位信息并返回带阶段的错误，
  由调用方再次核验，不自动重试。
- 真实环境下的只读验证（文件工具与 `sh -c` 写入是否真的被文件系统拒绝）尚未执行：本阶段只确认参数
  构造正确，**不代表只读规划环境已经可用**。
- 自动化测试守点仍不完整：第七阶段的生命周期用例、第十阶段的规划会话 / 稳定 Session / 只读参数用例，以及
  §7.13 的单 Server 基础设施用例（数据库路径规范化、进程锁身份统一、进程内协调器锁表回收与同键串行 / 异键并行）
  已覆盖准入、取消传播、停止等待、会话状态机与事务边界、状态与两个绑定的条件更新、稳定 Session 首轮归档 /
  多轮恢复 / 归档失败上报、规划只读工作区与 Docker 参数。仍未自动化覆盖的是这些行为在真实环境中的结果：真实
  数据库上的 Schema 初始化与不兼容旧库的失败行为、真实 Docker / SSH 的资源创建与释放，以及真实 Pi 的多轮上下文恢复。
- 未验证的真实外部能力：真实只读环境（文件工具与 `sh -c` 写入是否真的被文件系统拒绝）、真实多轮 Pi Session
  跨容器重建恢复、真实远程容器释放、远端 Pi 进程在取消后是否真的终止，都没有执行，不得报告为通过。
  已删除的 `planning_session_leases` 不再有表结构与测试守点；其余 Schema 结构（项目默认节点、规划会话表）
  只在测试数据库中由首次初始化建表验证，没有自动化审计守点，也没有在真实开发数据库上执行。

### 7.11 验证方式

六个阶段都只做非测试验证：阅读代码核对调用链、`gofmt`、`go vet ./...`、`git diff --check`。第二阶段另加
人工核对 `schema.sql` 中对应的表、索引、字段与非枚举约束；第三阶段人工核对快照卷命名与标签、准备 / 只读复核
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

- **非测试验证**：人工核对 `schema.sql` 中 `projects.default_host_id` 与部分索引的定义、`projectColumns`
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
  增量升级用例 `TestProjectsMigrationDefaultHostKeepsExistingProjects` 已在取消增量迁移时删除。
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

- **非测试验证**：人工核对 `schema.sql` 中 `planning_session_leases` 的表、列、组合 CHECK 与部分索引，
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
