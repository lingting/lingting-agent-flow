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

本阶段不包含：`PlanningRuntimeService`、`PlanningSessionService` 业务编排、规划容器与快照卷准备、
Pi Session 路径生成、`internal/lifecycle` 注册、HTTP API 与前端工作台。规划运行时仍不可用。

### 7.3 仍未完成

以下能力尚未实现，M2 状态保持 `pending`：

- `PlanningRuntimeService`：快照卷准备（准备容器按固定 commit SHA 克隆到命名卷）、规划容器启动、
  多轮复用与释放。
- `PlanningSessionService`：会话创建、消息追加、Revision 追加、确认与放弃、状态收敛的业务编排
  （领域类型与 `0026` 持久化已在 §7.2 交付，尚无调用方）。
- 迁移 `0028_projects_default_host.sql`（项目默认执行节点）与后续项目级调度的迁移。
- 两个规划角色的 Pi Session 路径生成与根目录校验、多轮 Pi Session 上下文复用。
- 规划运行时在 `internal/lifecycle` 中的注册与收敛。
- HTTP API 与前端需求规划工作台。
- 真实环境下的只读验证（文件工具与 `sh -c` 写入是否真的被文件系统拒绝）尚未执行：本阶段只确认
  参数构造正确，**不代表只读规划环境已经可用**。

### 7.4 验证方式

第一阶段与第二阶段都只做非测试验证：阅读代码核对调用链、`gofmt`、`go vet ./...`、`git diff --check`；
第二阶段另加人工核对迁移的表、索引、字段、非枚举约束与版本顺序，以及 `dbStore` / `txStore` 的接口
组合。两个阶段都未运行任何测试（含迁移升级与 SQLite 读写测试），未创建测试文件，未连接真实
Docker / SSH / Pi / 远程主机。因此旧库升级后的数据保留、未知枚举值写入与读回、序号分配在并发下的
实际行为均未验证，不得报告为通过。
