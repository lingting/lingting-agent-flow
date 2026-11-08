# M4：方案确认与任务生成

> 状态：**completed**（2026-11-08；V2-M4-E1 后端确认事务与 V2-M4-E2 前端确认闭环均已交付）。
> 前置：M3 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §3、§5，
> [`../DATA-MODEL.md`](../DATA-MODEL.md) §2.1、§2.2、§3、§4，
> 后端边界见 [`../../../docs/architecture.md`](../../../docs/architecture.md)「方案确认与正式实体生成」，
> 接口契约见 [`../../../docs/http-api.md`](../../../docs/http-api.md) §16。

## 1. 目标

把用户确认的那一版方案变成**正式业务实体**，且不留半成品：

1. 方案结构化校验（至少 1 个任务、顺序连续、每个任务可独立验收）。
2. 确认后在同一事务内创建 1 个正式 Requirement 与 N 个正式 Task。
3. 需求与任务一对多关联；任务在需求内的顺序持久化为 `requirement_sequence`。
4. 需求保存最终分析与最终方案快照；会话收敛为 `confirmed`。
5. 生成任务后保持待执行，不启动 Scheduler。

## 2. 范围

### 2.1 Domain（已交付）

- `ImplementationPlan.ConfirmableTasks()`：在既有结构化校验之上补充"顺序连续且唯一"，顺序由
  任务数组下标 +1 推导（M3 的结构化契约没有独立 `sequence` 字段，数组顺序就是权威顺序），
  因此不需要为一个可确定推导的值改造已落库的 Revision JSON 契约，也不存在第二套方案解析器。
- `RequirementTitleFromAnalysis` / `RequirementDescriptionFromAnalysis`：标题取最终需求分析的摘要，
  描述是"摘要 + 目标 + 范围 + 约束 + 验收标准"的确定性摘要，不引入任何新的模型文本。
- `NewConfirmedRequirement`：确认生成的需求带项目关联、规划会话关联、两份快照与确认时间；
  初始状态沿用需求领域既有约定（`queued`），不新增第二套需求状态机。
- `Requirement.validateConfirmation()`：确认字段的组合一致性（有规划来源就必须完整）在领域层判定。
- `Task.RequirementSequence` 与 `SetRequirementSequence`：需求内顺序必须为正数，只能由确认流程写入。

### 2.2 App（已交付）

- `RequirementService.Confirm`：前置校验 → 单事务写需求 / 任务 / 顺序 / 会话状态。
  失败整体回滚，任何一步失败都不留下部分任务。
- 任务配置从 Project 继承（仓库、分支、环境、版本、缓存、工作目录、执行节点），写入 `config_json` 快照。
- `RequirementService.Get` / `Detail` / `ListByProject`：正式需求的只读查询。

### 2.3 持久化（已交付）

- 迁移脚本 `00003_requirement_confirmation.sql`：`requirements` 增加 `project_id`、
  `planning_session_id`、`final_analysis_json`、`final_plan_json`、`confirmed_at`、`deleted_at`
  与项目需求列表索引；`tasks` 增加 `requirement_sequence` 与部分唯一索引
  `(requirement_id, requirement_sequence) WHERE requirement_sequence IS NOT NULL`。
- 不提供旧库升级，因此没有历史数据回填；枚举式字段仍无取值白名单，不新增外键与触发器。

### 2.4 HTTP（已交付）

```text
POST /api/v1/planning/sessions/{sessionID}/confirm
GET  /api/v1/requirements/{requirementID}
GET  /api/v1/projects/{projectID}/requirements
```

- `confirm` 只接受 `revision_id`（与可选 `host_id`），不接受任务列表、顺序覆盖或任务字段覆盖。
- 响应返回真实创建的需求与任务（含 `requirement_sequence` 与 `queued` 状态），不返回"已受理"式模糊语义。
- 错误按 `errors.Is` 分类：400 参数 / 404 不存在 / 409 状态冲突 / 412 前置条件不满足 / 500 持久化失败。

### 2.5 前端（已交付）

- 复用 `/planning` 工作台，在 Revision 预览区增加方案确认：确认前摘要（版本号、需求标题与描述、
  任务数量与顺序）→ 弹窗二次确认 → 真实需求与任务展示。
- 确认在途禁用重复操作；失败后重新读取会话与方案版本；`409` / `412` / 网络中断分别如实展示；
  已确认会话只读展示采纳结果；页面不提供开始执行入口，也不允许改写服务端确定的任务顺序。

## 3. 退出条件

| 条件 | 判定方式 | 状态 |
|---|---|---|
| 每个正式需求至少关联一个 Task | 空任务列表在结构化校验阶段即被拒绝 | 满足 |
| 无效方案不能创建正式 Task | 缺少必填字段 / 依赖非法 / 空任务列表均被拒绝且不写入实体 | 满足 |
| 事务失败不留部分任务 | 在 7 个写入位置分别注入失败后需求与任务均为 0，会话状态与采纳版本不变 | 满足 |
| Task 继续使用 V1 执行模型 | 生成的任务带完整 `config_json` 快照与执行节点，可被既有启动 / 工作流服务接受 | 满足 |
| 创建完成后不启动 Scheduler | 任务状态为 `queued`，无 Step / Attempt / Session / WorkflowRun / 容器记录 | 满足 |
| 需求内顺序稳定 | `requirement_sequence` 与方案顺序一致，部分唯一索引拒绝重复顺序 | 满足 |

M4 退出条件全部满足，据此标记 `completed`。

## 4. 步骤创建时机的最终结论

V1 的计划生成约定是"**任务启动后由 Planner 在任务容器内生成 Step**"：

- `domain.CheckPlanGenerationStart` 要求任务已有受管理的任务容器，且拒绝覆盖已有计划；
- `domain.BuildPlan` 把 Planner 输出转换为 `steps`（序号 1 为 `plan` 阶段，其余为 `develop`）；
- `TaskWorkflowService.GeneratePlan` 只在任务进入执行流程后调用它。

因此确认事务**只创建 Requirement 与 Task**：不提前创建 Step、不调用 Planner、不改变
`GeneratePlan` 的职责。队列中的 `queued` 任务在开始执行前没有 Step 是既定约定，不是缺失数据。
该结论已同步到 `WORKFLOW.md` §5 与 `ARCHITECTURE.md` §3.1。

## 5. 验收方式（默认不自动执行）

- 阅读代码：确认流程的前置校验点、事务边界、顺序写入、会话状态收敛。
- `git diff` 核对新增迁移脚本只包含本次需要的结构变化；`gofmt`、`go vet ./...`。
- 用户明确要求测试时，覆盖：结构化校验各失败分支、事务回滚（含 7 个注入点）、顺序唯一性、
  并发确认、HTTP 错误分类与前端确认闭环。

## 6. 风险与待决

1. **需求状态与进度**：确认生成的需求沿用既有 `queued / processing / completed / failed / cancelled`
   状态机；V2 设计中的 `planned / executing / blocked` 属于 M5 的进度聚合，本阶段不引入第二套状态机。
2. **旧库处理**：不提供升级，已有开发数据库需要人工备份与重建；确认流程不做历史数据回填。
3. **校验者角色可选**：任务配置中的 `validator_role_id` 是可选的；确认流程只在角色存在、启用且类型
   正确时写入它，否则留空表示"不做辅助评估"。规划与开发角色缺失 / 禁用会让确认失败。
4. **历史手工需求没有项目关联**：`TaskService.CreateTask` 仍按既有约定创建不带项目关联的需求，
   这类需求不会出现在项目需求列表中（空值不是"默认项目"）。

## 7. 明确不做

- 不启动执行、不创建容器、不分配任务租约（M5）。
- 不生成项目级进度聚合接口（M5）。
- 不允许确认后修改需求或重新规划。
- 不实现任务分支串接与自动合并。
- 不实现需求删除（M6）：本阶段只补齐 `deleted_at` 列。

## 8. 交付记录

### 8.1 V2-M4-E1 方案确认与正式实体生成（2026-11-08）

1. **领域契约**：`ImplementationPlan.ConfirmableTasks` 复用既有结构化校验并推导连续顺序；
   需求标题与描述来自最终需求分析的确定性摘要；确认生成需求的字段组合一致性由领域层判定；
   `Task.RequirementSequence` 有正数约束且只能由确认流程写入。
2. **迁移**：`00003_requirement_confirmation.sql` 追加需求的项目关联与规划快照列、`deleted_at`、
   项目需求列表索引，以及任务的需求内顺序列与部分唯一索引；不新增外键、枚举白名单、触发器与回填。
3. **应用服务**：`RequirementService.Confirm` 在单个 `storage.WithTx` 内完成"事务内重新核验会话状态 →
   核验 Revision 归属与快照版本 → 校验项目与会话一致 → 解析执行节点与任务角色 → 构造并校验每个任务的
   配置快照 → 创建 1 个需求与 N 个任务 → 以 `preview_ready` 为前置条件推进会话"。并发确认只允许一个
   成功，落后的一方整体回滚并返回冲突。
4. **配置继承**：仓库、基础分支、环境、环境版本、缓存、工作目录与执行节点全部来自同一个项目快照；
   执行节点按"显式给出 > 项目默认"解析，两者都没有时直接失败，不回退本机执行，也不读规划会话绑定的
   规划节点。角色规则与建任务入口共用同一份校验（`ensureTaskRoles`）。
5. **HTTP**：新增确认与两个只读查询接口；`confirm` 拒绝未知字段，只接受两个标识；响应返回真实需求、
   按顺序排列的任务与被采纳的 Revision。
6. **测试**：领域用例覆盖空任务列表、重复标识、依赖不存在 / 自引用 / 成环、顺序与拓扑、必填字段为空、
   合法单任务与多任务方案、需求标题描述推导与确认字段一致性；存储用例覆盖规划快照读回、手工需求的
   空值语义、需求内顺序唯一索引、更新不丢顺序、项目需求列表筛选与排序；应用用例覆盖 1 需求 + N 任务、
   快照与选定 Revision 一致、顺序一致、全部 `queued`、会话 `confirmed` 与采纳版本、项目配置继承、
   跨项目隔离、旧版本确认、各类前置失败，以及在创建需求 / 第一个任务 / 中间任务 / 最后一个任务 /
   确认快照 / 会话条件更新 / 事务提交 7 个位置注入失败后的数据库状态核验，另有并发确认与重复确认；
   接口用例覆盖正常确认、非法 JSON、未知字段、缺失 `revision_id`、跨会话版本、409、404、412、500、
   未装配 503、并发确认与两个只读查询。

### 8.2 V2-M4-E2 前端确认闭环（2026-11-08）

1. **接口层**：`src/lib/api/planning.ts` 增加确认、需求详情与项目需求列表接口与类型；确认只提交
   `revision_id` 与可选 `host_id`，空白节点不提交。
2. **纯规则**：确认可用性判定（会话 / 版本 / 状态 / 在途动作 / 执行节点）、确认摘要（版本号、需求标题
   与描述、任务数量与顺序）、确认失败分类（网络不确定 / 409 / 412 / 503）、正式任务展示排序与
   "已确认会话的需求匹配"集中在 `src/pages/planning/planning.ts`。
3. **交互**：`PlanningConfirmPanel` 展示确认摘要，弹窗内二次确认（含将生成什么、事务边界与任务保持待执行
   的说明），确认中禁用重复提交；成功后展示真实需求标识、任务标识、需求内顺序与 `queued` 状态，
   不提供开始执行入口；已确认会话只读展示采纳结果。
4. **测试**：前端单元测试覆盖接口路径与请求体、确认可用性、草案摘要、失败分类与任务排序；
   Playwright 受控后端用例覆盖完整确认闭环、选中历史版本时提交旧版本标识、`409`、网络中断不重放、
   `412` 与已确认会话重新打开后的只读展示，并断言正式任务接口从未被调用。
