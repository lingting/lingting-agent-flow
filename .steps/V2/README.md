# AgentFlow V2 文档体系

> **文档状态**：V2 设计基线（V2 M0 交付物）。
> **基线**：V1 最终产品状态见 [`../V1-final.md`](../V1-final.md)。
> **范围**：V2 产品范围、架构、数据模型、工作流、角色、环境管理与里程碑。
> 具体实现细节、DTO 与错误码在对应里程碑实现前按当时代码再核实，不在本文预设。

## 1. 一句话定位

V2 把用户发起工作的入口从「创建一个任务」升级为「提出一项需求」：以**需求**组织任务，
以**项目**组织执行，执行能力整体复用 V1。

## 2. 文档职责

| 文档 | 回答的问题 | 读者用途 |
|---|---|---|
| [`PRODUCT-SCOPE.md`](./PRODUCT-SCOPE.md) | V2 做什么、不做什么、边界在哪 | 判断需求是否属于 V2 |
| [`ARCHITECTURE.md`](./ARCHITECTURE.md) | 分层、服务职责、接口与前端结构 | 判断代码放哪里、谁不能做什么 |
| [`DATA-MODEL.md`](./DATA-MODEL.md) | 当前最终 Schema 结构、后续里程碑需要的结构变化 | 改迁移脚本与存储层前必读 |
| [`WORKFLOW.md`](./WORKFLOW.md) | 需求规划、确认、调度、进度、删除的状态与流程 | 写编排与状态机前必读 |
| [`ROLE-DESIGN.md`](./ROLE-DESIGN.md) | 四个固定角色、权限实施方式、可改范围 | 写角色配置与执行层限制前必读 |
| [`ENVIRONMENT-MANAGEMENT.md`](./ENVIRONMENT-MANAGEMENT.md) | 环境管理内嵌化、配置迁移边界 | 改 Settings 与配置前必读 |
| [`ROADMAP.md`](./ROADMAP.md) | 里程碑顺序、依赖、当前状态 | 决定下一步做什么 |
| [`milestones/`](./milestones) | 每个里程碑的目标、交付物、退出条件 | 开工与验收时逐条核对 |

## 3. 里程碑一览

| 里程碑 | 主题 | 状态 |
|---|---|---|
| [M0](./milestones/M0-v1-closeout-and-v2-design.md) | V1 收口与 V2 设计 | completed（2026-11-02） |
| [M1](./milestones/M1-settings-and-fixed-roles.md) | 设置中心、环境管理、固定四角色 | completed（2026-11-03；2026-11-02 真实远程端到端运行曾失败于规划阶段步骤，修正「规划只规划、开发只验收」的职责边界后于 2026-11-03 真实运行通过，详见里程碑 §7 / §8） |
| [M2](./milestones/M2-planning-runtime.md) | 规划运行环境（只读容器 + 多轮会话） | completed（第三阶段已交付规划运行时服务：固定 commit 快照卷、只读规划容器、复用与释放；第四阶段已交付规划会话业务编排 `PlanningSessionService`：会话创建与查询、提交消息并启动分析、显式重跑、Revision 与角色消息落库、失败收敛、确认与放弃；第五阶段修复运行时绑定的并发覆盖风险：绑定只写执行节点 / 规划容器 / 固定 commit / 更新时间，并以读取到的旧绑定做条件更新；第六阶段为需求分析师与方案规划师建立稳定 Pi Session：角色会话标识成对条件绑定、容器内固定 Session 路径与本机持久归档、每轮调用前从本机归档恢复历史 JSONL 并按条件更新可恢复快照，普通任务 Runner 默认路径不变；第七阶段把三个规划服务接入 `agentflow server` 的装配入口并实现规划 Agent 的生命周期边界：Start 之后才接受调用，Stop 时拒绝新调用、取消在途调用并等待其连同 Session 归档一起收敛，服务不可用时整体降级且不影响普通任务路径，并新增本阶段授权的生命周期定向测试；第八阶段把项目默认执行节点落成项目配置：迁移脚本的 `projects` 表增加 `default_host_id`（可空、部分索引），领域与应用层在写入前校验节点引用（新引用必须存在且未删除，节点下线后历史引用仍可保留、可清除），既有项目 HTTP 接口以 JSON `null` 表达未设置，现有项目管理界面可查看、选择、回填与清除默认节点，并新增本阶段授权的定向测试；第九阶段（历史记录：已随单 Server 架构删除）曾为规划会话增加数据库级跨进程执行租约：迁移脚本新增 `planning_session_leases`（主键规划会话、owner、operation、原子递增的 epoch、到期时间，无外键、无枚举白名单、释放保留 epoch），`Prepare` / `RunRole` / `Release` 竞争同一把会话锁，租约覆盖容器准备、角色会话绑定、Pi 执行与 Session 归档，执行期间后台续租，续租失败即取消在途调用并等待其收敛，`Release` 在租约被占用时不进入任何远程删除，并新增本阶段授权的定向测试；第十阶段为 M2 已实现的业务补齐定向自动化测试守点：规划会话状态机、存储层消息与版本序号分配及并发追加、状态条件更新与运行时绑定 / 角色绑定比较并交换、`PlanningSessionService` 的事务业务流程与回滚、稳定 Pi Session 的首轮归档 / 多轮恢复 / 失败保护 / 归档失败上报，以及规划只读工作区与 Docker 创建参数（见里程碑 §7.12）；第十三阶段修复单 Server 基础设施：数据库路径符号链接规范化、进程锁与 SQLite 共用同一条规范化路径、`PlanningSessionCoordinator` 与 `EnvironmentBuildLocks` 改用带引用计数的 keyed mutex 自动回收（见里程碑 §7.13），第九阶段的 `planning_session_leases` 与 owner / epoch / TTL / 续租机制已整体删除。第十四阶段补齐规划运行时的现场核验与失败恢复：新增 `InspectPlanningRuntime`（只读核验规划容器 / 快照卷 / 一次性准备容器 / 实际 commit，结论分为存在、不存在、无法确认与归属不匹配）、`CleanupPlanningSnapshotPreparation`（所有权核验通过才按容器 ID 删除已退出的准备容器）、`RecoverPlanningSnapshot`（只恢复已完整存在且未落库的快照，不重新 Clone / checkout）与 `DiscardIncompletePlanningSnapshot`（未固定 commit、未被容器引用、准备容器已不存在且所有权匹配才废弃），`Release` 改为先核验远端状态再删除、以既有条件更新清空绑定、不确定时保留定位信息且不自动重试；释放专用端口被 `app.PlanningRuntimeRemotePort` 取代，所有权与状态判定回到应用层，仍只有一条 SSH / Docker 调用链（见里程碑 §7.14）。第十五阶段把 Schema 初始化改为版本化 SQL 迁移：`internal/storage/sqlite/migrations/` 下的脚本由 Goose 执行并记录版本，手写版本判断与 `schema.sql` 一并删除（见里程碑 §7.15）。第十七阶段补齐规划容器的停止状态对账与中断恢复：远端容器已停止但本地仍为 `running` 时（停止写入失败或 Server 在写库前中断），`ContainerService.StartContainer` 先经领域状态机收敛为 `stopped` 并落库、同步登记清单后才执行 `docker start`，本地写入失败不会继续启动；`PlanningAgentService.RunRole` 每次调用先 inspect 远端事实，`exited` / `created` 才进入按原容器 ID 的恢复流程，`paused` / `restarting` / `dead` / `removing` / `unknown` 一律不自动启动；`RunAgent` 触发终止后用 `context.WithoutCancel` 派生的有界上下文读回已落库的 `stopped` 或 `unknown`，本地读取失败由 `ContainerStateReloadError` 单独表达（见里程碑 §7.17）。第十六阶段交付规划 Pi 执行结果不确定时的受控容器终止：`PlanningAgentService` 显式声明 `PlanningSessionID` 与 `StopContainerOnUncertain`，`ContainerRunner` 在命令已启动但结果不确定时经持锁内部流程停止本会话独占的规划容器并以停止后的实际 `docker inspect` 判定终止，本地记录与远程登记清单同步为 `stopped`，无法确认时返回 `ErrRemoteTermination` 并保留绑定；停止后不再从该容器提取 Session，下一次调用按原容器 ID 重启并从最后一份完整归档恢复历史 Session，普通 Task 容器行为不变（见里程碑 §7.16）。第十八阶段统一 Docker 容器状态判定：`paused` 与 `restarting` 容器同样报告 `State.Running=true`，判定因此改由 `domain.ClassifyDockerContainerCondition` 同时考虑 `State.Status` 与 `State.Running`，`docker.ContainerInspect` 提供 `RuntimeCondition` / `IsActivelyRunning` / `IsConfirmedExited` / `IsStartable`，暂停、重启中、dead、removing 与字段矛盾不再被当作正常运行或已确认退出；`StartContainer`、`Exec`、`RunAgent`、`sessionContainerGuard`、规划运行时复用、规划 Agent 前置核验与 `stopContainerLocked` 全部改用统一语义，容器残留观测新增 `ContainerObservedIndeterminate` 并要求人工核验，受控测试替身改为分别设置两个字段（见里程碑 §7.18）。第十九阶段在真实环境（真实 SSH / 远程 Docker / 只读规划容器 / 容器内 Pi）完成验收并修复两处生产缺陷：只读规划容器的 Session 提取与恢复改由 `docker exec` 加宿主机重定向完成（`docker cp` 只解析 rootfs，看不到 tmpfs），SSH 中断后的远端 Pi 终止改由独立连接重新读取本地记录、核验节点 / 容器 ID / 标签后停止并以真实 `inspect` 判定；真实环境已确认只读 rootfs 与只读快照挂载、文件工具与 Shell 写入被文件系统拒绝、两角色多轮 Session 上下文连续、容器停止与服务重建后同一容器与快照恢复、资源释放零残留、`paused` 真实字段与全部业务拒绝，`restarting` 已观测而 `dead` / `removing` 未观测，Goose 全新文件库初始化与迁移版本表守点已补齐（见里程碑 §7.19）。第二十一阶段新增规划会话与规划运行时 HTTP 入口（会话查询、现场核验、准备与释放，1:1 映射生产服务），用真实 Server 子进程验收进程强制终止后的恢复：全新进程使用同一数据库读到原会话与绑定、远程容器与快照卷归属可核验、未创建替代容器、Session 归档摘要不变、资源由生产 Release 路径释放；同时把「同一规划会话多轮复用同一容器」从退出条件移除并把多轮规划业务编排归入 M3，M2 据此标记 completed，见里程碑 §7.21 与 §7.3 / §7.4 / §7.5 / §7.6 / §7.7 / §7.8 / §7.9 / §7.10 / §7.14 / §7.16 / §7.17 / §7.18 / §7.19） |
| [M3](./milestones/M3-requirement-planning.md) | 需求工作台与需求分析 | in_progress（V2-M3-E1 已交付：`RequirementAnalysis` / `ImplementationPlan` 结构化领域契约与严格解析、`PlanningRevision` 快照版本、`PlanningAnalysisService` 两阶段编排（数据库级占用、分析师→规划师顺序调用、成功单事务写 Revision 与角色消息、失败按阶段记录并可显式重试）、`/api/v1/planning/sessions` 业务与 `analysis` / `analysis/retry` 接口；未交付：需求工作台前端、Revision 预览、确认生成正式任务（属 M4），详见 `ROADMAP.md` §1） |
| [M4](./milestones/M4-requirement-task-generation.md) | 方案确认与任务生成 | pending |
| [M5](./milestones/M5-project-scheduling-and-progress.md) | 项目级调度与进度 | pending |
| [M6](./milestones/M6-deletion-and-acceptance.md) | 删除流程与端到端验收 | pending |

顺序固定，不跳级：M0 → M1 → M2 → M3 → M4 → M5 → M6。

## 4. 与 V1 的关系

- **复用**：`TaskWorkflowService`、`StepWorkflowService`、`StepExecutor`、`ValidationService`、
  `TaskScheduler`、`TaskLaunchService`、`ContainerService`、`ExecutionEnvironmentService`、
  `EnvironmentService`、`ProjectService`、领域状态机与全部执行记录模型。
- **不新建**：第二套工作流引擎、第二套任务执行实体、第二套验证判定规则。
- **新增**：需求级规划模型（PlanningSession / Message / Revision）、正式需求管理、
  项目级执行协调（具体承载方式在 M5 按单 Server 架构确定）、固定四角色的配置与运行时限制。
- **升级**：`Requirement` 从「只有标题描述的记录」变成「项目级、由规划确认产生的正式需求」。

## 5. 文档维护规则

1. 代码与文档必须一致。发现冲突时先修文档结论，再确定实现方案。
2. 不把未实现写成已实现；不把"能跑起来"当成"已完成"。
3. 未确认的设计选择归入对应文档的「待决问题」，不写成既定事实。
4. 里程碑状态只按退出条件判定，退出条件不满足就保持 `pending` / `in_progress`。
5. Schema 只以 `internal/storage/sqlite/migrations/` 下的版本化脚本为准；冻结遗留表与 V1 执行记录一律不动。
