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
| [`DATA-MODEL.md`](./DATA-MODEL.md) | 新增哪些实体、迁移怎么排、历史数据怎么处理 | 写迁移与存储层前必读 |
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
| [M2](./milestones/M2-planning-runtime.md) | 规划运行环境（只读容器 + 多轮会话） | pending（第三阶段已交付规划运行时服务：固定 commit 快照卷、只读规划容器、复用与释放；第四阶段已交付规划会话业务编排 `PlanningSessionService`：会话创建与查询、提交消息并启动分析、显式重跑、Revision 与角色消息落库、失败收敛、确认与放弃；第五阶段修复运行时绑定的并发覆盖风险：绑定只写执行节点 / 规划容器 / 固定 commit / 更新时间，并以读取到的旧绑定做条件更新。两个角色的 Pi 调用方、多轮 Pi Session、生命周期注册、HTTP 与前端未完成，见里程碑 §7.3 / §7.4 / §7.5） |
| [M3](./milestones/M3-requirement-planning.md) | 需求工作台与需求分析 | pending |
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
  项目级执行租约、固定四角色的配置与运行时限制。
- **升级**：`Requirement` 从「只有标题描述的记录」变成「项目级、由规划确认产生的正式需求」。

## 5. 文档维护规则

1. 代码与文档必须一致。发现冲突时先修文档结论，再确定实现方案。
2. 不把未实现写成已实现；不把"能跑起来"当成"已完成"。
3. 未确认的设计选择归入对应文档的「待决问题」，不写成既定事实。
4. 里程碑状态只按退出条件判定，退出条件不满足就保持 `pending` / `in_progress`。
5. 历史迁移脚本、冻结遗留表、V1 执行记录一律不动。
