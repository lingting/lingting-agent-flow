# M2：规划运行环境

> 状态：**pending**。
> 前置：M1 completed。参考：[`../ARCHITECTURE.md`](../ARCHITECTURE.md) §4、
> [`../DATA-MODEL.md`](../DATA-MODEL.md) `0027`、[`../ROLE-DESIGN.md`](../ROLE-DESIGN.md) §7。

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

- 迁移 `0026_planning_sessions.sql`（`planning_sessions` / `planning_messages` / `planning_revisions`）：
  已实现（M2 第二阶段），实际表结构与索引见 [`../DATA-MODEL.md`](../DATA-MODEL.md) §2。
- 迁移 `0028_projects_default_host.sql`（项目默认执行节点）。

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
- `internal/storage/sqlite`：迁移 `0026_planning_sessions.sql`（三张表与四个索引）与仓储实现；消息
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
- 新能力同时由 `dbStore` 与 `txStore` 提供（两者组合同一个 `planningSessionStore`）；没有新增迁移，
  `0026_planning_sessions.sql` 未改动，`app` 层不含 SQL。`PlanningSessionService` 的业务行为、领域状态机
  与 `UpdatePlanningSessionIfStatus` 的条件语义都未改动。

本阶段解决的只是"运行时字段与业务字段相互覆盖"。`PlanningRuntimeService` 的 `keyedMutex` 仍只保证当前
进程内的同会话串行；不同进程对同一规划会话发起 `Prepare` / `Release` 时，运行时绑定由数据库条件更新拒绝
过期写入，但远程资源操作本身仍没有跨进程租约，`Release` 也不检查在途规划调用。

### 7.6 仍未完成

以下能力尚未实现，M2 状态保持 `pending`：

- 两个规划角色（需求分析师、方案规划师）的 Pi 调用方：`PlanningSessionService` 已提供
  `CompleteAnalysis` / `FailAnalysis` 等收敛入口，但当前没有任何代码调用它们，也没有结构化输出解析。
- 迁移 `0028_projects_default_host.sql`（项目默认执行节点）与后续项目级调度的迁移。
- 两个规划角色的 Pi Session 路径生成与根目录校验、多轮 Pi Session 上下文复用。
- 规划运行时在 `internal/lifecycle` 中的注册与收敛，以及 `agentflow server` 的服务装配
  （`PlanningRuntimeService` 与 `PlanningSessionService` 目前都未被任何装配入口构造）。
- HTTP API 与前端需求规划工作台。
- 撤销与重跑之外的失败恢复入口：结果不确定的运行时现场只能通过再次 `Prepare` 做只读核验；残留的
  一次性准备容器没有专门清理路径，也没有跨进程会话锁。
- `Release` 不检查是否存在在途规划调用（运行时绑定写入用旧对象整行覆盖业务状态的风险已在 §7.5 解决，
  该处改为只写运行时列的条件更新）。
- 真实环境下的只读验证（文件工具与 `sh -c` 写入是否真的被文件系统拒绝）尚未执行：本阶段只确认参数
  构造正确，**不代表只读规划环境已经可用**。
- 本阶段新增的会话事务、条件更新与错误映射没有任何自动化测试守点，也未在真实数据库上验证并发行为。

### 7.7 验证方式

五个阶段都只做非测试验证：阅读代码核对调用链、`gofmt`、`go vet ./...`、`git diff --check`。第二阶段另加
人工核对迁移的表、索引、字段、非枚举约束与版本顺序；第三阶段人工核对快照卷命名与标签、准备 / 只读复核
容器、`--read-only` 与只读代码卷、tmpfs 可写目录、容器所有权校验、释放顺序与 `RequiredCLICommands`
新增参数，并确认普通任务容器的创建参数未改变；第四阶段人工核对七个业务操作的事务边界、领域状态机调用点、
条件更新的前置状态、角色消息发送方限制与失败摘要脱敏；第五阶段人工核对运行时绑定更新的实际 `SET` 列、
NULL 安全的比较并交换条件、`persistRuntime` 的事务内重读与错误映射，并确认 `Prepare` 的三个持久化阶段与
`Release` 都走同一入口。

五个阶段都未运行任何测试（含迁移升级与 SQLite 读写测试），未创建测试文件，未连接真实 Docker / SSH / Pi /
远程主机。因此旧库升级后的数据保留、未知枚举值写入与读回、序号分配与条件更新在并发下的实际行为、真实快照
克隆、只读挂载与容器释放均未验证，不得报告为通过；第四阶段的并发结论来自事务与条件更新的代码路径核对，
第五阶段的运行时绑定冲突结论同样只来自 SQL 条件与调用链核对，没有真实并发执行与跨进程竞争的证据。
