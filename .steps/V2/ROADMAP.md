# V2 路线图

> 状态：V2 设计基线。里程碑顺序固定，不跳级；每个里程碑的退出条件见 `milestones/`。

## 1. 当前状态

| 里程碑 | 状态 | 说明 |
|---|---|---|
| M0 V1 收口与 V2 设计 | **completed**（2026-11-02） | `V1-final.md` + V2 文档体系；未修改生产代码 |
| M1 设置中心、环境管理、固定四角色 | **completed**（2026-11-03） | 子任务 V2-M1-E1（环境管理内嵌化）、V2-M1-E2（固定四角色与角色配置管理）、V2-M1-E3（角色工具策略执行与 Settings 编辑）与 V2-M1-E4（基础配置只读查看）已完成。2026-11-02 真实远程端到端运行曾失败于规划阶段步骤（任务级必需验证命令被合并进序号 1 的 `plan` 步骤，见里程碑 §7）；修正为「规划只规划、开发只验收」后，2026-11-03 真实远程端到端运行 `TestRemoteE2EFullChain` 通过：规划步骤真实完成、开发步骤执行 `notes-exists`（退出码 0）并通过语义审查，工作流 `completed`、推送成功（里程碑 §8） |
| M2 规划运行环境 | pending | 依赖 M1 的角色配置快照。第三阶段已交付 `PlanningRuntimeService`（固定 commit 快照卷、只读规划容器、同一会话复用与精确释放，见里程碑 §7.3）；第四阶段已交付 `PlanningSessionService`（会话创建与查询、提交消息并启动分析、显式重跑分析、Revision 与角色消息落库、失败收敛、确认与放弃，事务与条件更新保证并发正确性，见里程碑 §7.4）；第五阶段把运行时绑定写入改为只写运行时列的比较并交换更新，运行时字段不再覆盖业务状态、采纳的 Revision、失败摘要与角色会话标识（见里程碑 §7.5）；第六阶段为两个规划角色建立稳定 Pi Session：角色会话标识成对条件绑定、容器内固定路径与本机持久归档、每轮调用前从本机归档恢复历史 JSONL 并按条件更新可恢复快照，普通任务 Runner 行为不变（见里程碑 §7.6）；第七阶段完成服务装配与生命周期管理：`PlanningSessionService` / `PlanningRuntimeService` / `PlanningAgentService` 由 `agentflow server` 用当前存储、容器服务、角色服务与配置 Session 根目录构造，规划 Agent 实现 `lifecycle.Service`，准入与在途登记同临界区完成，停止时拒绝新调用、取消在途调用并等待其连同 Session 归档收敛，SSH / 容器服务缺失时整体降级且普通任务路径不变（见里程碑 §7.7）；第八阶段交付项目默认执行节点配置：迁移脚本的 `projects` 表增加可空的 `default_host_id` 与部分索引，领域层统一空白归一，应用层在引入新引用时校验节点存在且未逻辑删除（节点下线后保留历史引用、允许清除），既有项目 HTTP 接口与现有项目管理界面读写该字段并以 JSON `null` 表达未设置（见里程碑 §7.8）；第九阶段（历史记录：已随单 Server 架构删除）曾交付规划会话跨进程执行租约与运行时释放保护：迁移脚本新增按规划会话的租约表（owner、operation、原子递增的 epoch、到期时间，部分索引，无外键、无枚举白名单、释放保留 epoch），`PlanningSessionLeaseService` 负责领取、后台续租与条件释放，`RunRole` 在读取会话之前取得租约并覆盖容器准备、角色会话绑定、Pi 执行与 Session 归档，续租失败取消在途调用并以 `ErrPlanningAgentLeaseLost` 上报后等待收敛，`Prepare` / `Release` 与 `RunRole` 竞争同一把租约且内部准备不重复领取，`Release` 在租约被占用时不进入任何远程删除（见里程碑 §7.9）；第十阶段为 M2 已实现的业务补齐定向自动化测试守点：规划会话状态机、存储层消息与版本序号分配及并发追加、状态条件更新与运行时绑定 / 角色绑定比较并交换、`PlanningSessionService` 的事务业务流程与回滚、稳定 Pi Session 的首轮归档 / 多轮恢复 / 失败保护 / 归档失败上报，以及规划只读工作区与 Docker 创建参数（见里程碑 §7.12）；第十三阶段修复单 Server 基础设施：数据库路径符号链接规范化、进程锁与 SQLite 共用同一条规范化路径、`PlanningSessionCoordinator` 与 `EnvironmentBuildLocks` 改用带引用计数的 keyed mutex 自动回收（见里程碑 §7.13），同时删除第九阶段的 `planning_session_leases` 表与 owner / epoch / TTL / 续租机制；第十四阶段补齐规划运行时的现场核验与失败恢复：`InspectPlanningRuntime` 只读核验规划容器、快照卷、一次性准备容器与远端实际 commit（结论分为存在 / 不存在 / 无法确认 / 归属不匹配），`CleanupPlanningSnapshotPreparation` 在所有权核验通过后按容器 ID 删除已退出的准备容器，`RecoverPlanningSnapshot` 只恢复已完整存在且未落库的快照（不重新 Clone / checkout），`DiscardIncompletePlanningSnapshot` 严格限制为未固定 commit、未被容器引用、准备容器已不存在且所有权匹配的快照卷，`Release` 改为先核验远端状态再删除、以既有条件更新清空绑定、结果不确定时保留定位信息且不自动重试；释放专用端口被 `app.PlanningRuntimeRemotePort` 取代，所有权与状态判定回到应用层，仍只有一条 SSH / Docker 调用链（见里程碑 §7.14）；第十五阶段把 Schema 初始化改为版本化 SQL 迁移：`internal/storage/sqlite/migrations/` 下的脚本由 Goose 按版本执行并记录在 `goose_db_version`，手写版本判断、`schema.sql` 与 `storage.ErrUnsupportedSchema` 一并删除（见里程碑 §7.15）；第十七阶段补齐规划容器的停止状态对账与中断恢复：远端容器已停止但本地记录仍为 `running` 时（停止写入失败或 Server 在写库前中断），`ContainerService.StartContainer` 启动前先 inspect 远端事实，本地 `running` / `unknown` 遇远端 `exited` / `created` 时经领域状态机收敛为 `stopped` 并落库、同步远程登记清单后才执行 `docker start`，落库失败即返回且不启动；`PlanningAgentService.RunRole` 不再只凭本地状态判断可用性，远端 `exited` / `created` 才按原容器 ID 恢复并从本机最后一份完整归档恢复历史 Session，`paused` / `restarting` / `dead` / `removing` / `unknown` 与 inspect 失败一律不自动启动；`ContainerService.RunAgent` 触发终止后用 `context.WithoutCancel` 派生的 5 秒上限上下文读回已落库的 `stopped` 或 `unknown`，本地读取失败由 `ContainerAgentResult.ContainerStateReloadError` 单独表达而不覆盖原始取消结论（见里程碑 §7.17）。第十六阶段关闭"取消上下文不能证明远端 Pi 已终止"的代码缺口：规划调用显式启用容器终止策略，结果不确定时经持有容器锁的内部流程停止独占规划容器并以停止后的 `docker inspect` 判定终止，无法确认时如实返回 `ErrRemoteTermination` 并保留绑定，停止后按原容器 ID 重启并从本机最后一份完整归档恢复历史 Session，普通 Task 容器行为不变（见里程碑 §7.16）；第十八阶段统一 Docker 容器状态判定：`paused` 与 `restarting` 容器同样报告 `State.Running=true`，因此判定改由 `domain.ClassifyDockerContainerCondition` 同时考虑 `State.Status` 与 `State.Running`，`docker.ContainerInspect` 暴露 `RuntimeCondition` / `IsActivelyRunning` / `IsConfirmedExited` / `IsStartable`，暂停、重启中、dead、removing 与字段矛盾不再被当作正常运行或已确认退出；`StartContainer`、`Exec`、`RunAgent`、`sessionContainerGuard`、规划运行时复用、规划 Agent 前置核验与 `stopContainerLocked` 全部改用统一语义，残留观测新增 `ContainerObservedIndeterminate` 并要求人工核验，受控替身改为分别设置两个字段（见里程碑 §7.18）。规划轮次业务编排与结构化输出解析、规划 HTTP 与需求工作台，以及真实环境下的只读、多轮恢复与远端 Pi 终止验证已在第十九阶段完成并修复两处生产缺陷：只读规划容器的 Session 提取与恢复改由 `docker exec` 加宿主机重定向完成（`docker cp` 看不到 tmpfs），SSH 中断后的远端 Pi 终止改由独立连接重新读取本地记录、核验节点 / 容器 ID / 标签后停止并以真实 `inspect` 判定；真实环境确认只读 rootfs 与只读快照挂载、文件工具与 Shell 写入被文件系统拒绝、两角色多轮 Session 上下文连续、容器停止与服务重建后同一容器与快照恢复、资源释放零残留、`paused` 真实字段与全部业务拒绝，`restarting` 已观测而 `dead` / `removing` 未观测，Goose 全新文件库初始化与迁移版本表守点已补齐（见里程碑 §7.19）。规划轮次业务编排与结构化输出解析、规划 HTTP 与需求工作台尚未交付（见里程碑 §7.10 / §7.19） |
| M4 方案确认与任务生成 | pending | 依赖 M3 |
| M5 项目级调度与进度 | pending | 依赖 M4 |
| M6 删除流程与端到端验收 | pending | 依赖 M5 |

V1 遗留的 `P4-M5`（基于真实使用数据的优化）保持 `pending`，不属于 V2 里程碑，不因 V2 开工
而改变状态，也不作为 V2 完成条件。

## 2. 里程碑与依赖

```text
M1 ──> M2 ──> M3 ──> M4 ──> M5 ──> M6
```

| 里程碑 | 主要交付 | 阻塞风险 |
|---|---|---|
| M1 | Settings 可用、环境写入口、四角色可配置、工具限制真正生效、基础配置只读（E1–E4 已交付）；真实执行链路回归已确认为通过（2026-11-03） | 规划阶段的项目代码只读边界依赖 M2 容器只读挂载（工具层限制已生效，文件系统层仍属 M2） |
| M2 | 只读规划容器、代码快照、多轮 Session、容器复用与释放 | 只读挂载与只读 rootfs 需新增 Docker 参数并同步 `RequiredCLICommands` |
| M3 | 需求工作台、需求分析师与方案规划师接入、Revision 预览 | 结构化输出解析稳定性；规划失败的可诊断性 |
| M4 | 方案校验、事务化生成 Requirement + Task、顺序持久化 | 事务内同时写需求/任务/步骤的一致性 |
| M5 | 项目级执行协调、同项目串行、跨项目并行、进度聚合（单 Server 架构下的具体承载方式实施时确定） | 执行权丢失、进程重启恢复与迟到结果的边界 |
| M6 | 停止与逻辑删除、历史保留、端到端验收 A–E | 真实收敛判定不得被伪装 |

## 3. 每个里程碑的通用要求

1. **Schema 以 `internal/storage/sqlite/migrations/` 下的版本化脚本为准**；结构变更只追加更高版本的迁移
   脚本，由 Goose 执行与记录版本，不手写迁移引擎，不提供旧库升级。
2. **不新增第二套执行引擎或验证规则**；执行路径一律复用 V1 服务。
3. **状态只由 `domain` 迁移**；Handler、前端、Agent 都不得直接赋值状态。
4. **失败与不确定不得伪装成成功**；接口如实返回，前端如实展示。
5. **测试授权制**：未经用户明确要求，不运行测试、不创建测试文件；里程碑文档中列出的
   测试项属于"该里程碑在获得授权后必须覆盖的范围"，不是自动执行指令。
6. **文档同步**：代码改动完成后，若发现文档与实现不一致，先报告，由用户决定是否同步。

## 4. 里程碑之间的"不许提前做"

- M1 不做规划容器、不做 PlanningSession（即使前端页面先出现，也只能是禁用入口）。
- M2 不做需求工作台业务逻辑（只提供运行能力与持久化）。
- M3 不创建正式 Requirement / Task（只产生 Revision 草案）。
- M4 不启动执行（确认后任务保持待执行）。
- M5 不做需求删除（删除流程属于 M6）。
- 任何里程碑都不顺手实现 V2 非目标清单中的内容。

## 5. 完成定义

V2 完成 = 以下闭环在真实环境中成立且有可复核证据：

```text
提出需求 → AI 分析项目 → AI 生成方案 → 多轮调整 → 用户确认
→ 生成 1 个 Requirement 与多个 Task → 用户手动启动
→ 多项目并行、项目内串行执行 → 查看项目与需求进度 → 保留完整执行历史
```

并且：

- 不重复建设 V1 工作流，不新增自定义角色框架，不实现用户权限管理与审计；
- 不支持正式需求变更，不自动合并任务分支，不让模型决定真实执行状态；
- 不用复杂抽象代替真正的业务功能。

## 6. 记录规则

- 每个里程碑完成后更新本文 §1 与本目录 `README.md` 的状态表，并写明结论依据。
- 未满足退出条件时保持 `pending` / `in_progress`，不得以"基本可用"判定完成。
- 测试结果如实区分通过 / 失败 / 跳过 / 未执行。
