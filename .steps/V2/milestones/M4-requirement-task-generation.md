# M4：方案确认与任务生成

> 状态：**pending**。
> 前置：M3 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §3、§5，
> [`../DATA-MODEL.md`](../DATA-MODEL.md) `0026`、`0029`、§3。

## 1. 目标

把用户确认的那一版方案变成**正式业务实体**，且不留半成品：

1. 方案结构化校验（至少 1 个任务、顺序连续、每个任务可独立验收）。
2. 确认后在同一事务内创建 1 个正式 Requirement 与 N 个正式 Task。
3. 需求与任务一对多关联；任务在需求内的顺序持久化为 `requirement_sequence`。
4. 需求保存最终分析与最终方案快照；会话收敛为 `confirmed`。
5. 生成任务后保持待执行，不启动 Scheduler。

## 2. 范围

### 2.1 Domain

- `ImplementationPlan` 校验：任务数量、`sequence` 连续唯一、必填字段非空、可独立验收判定。
- `RequirementStatus` 初态与状态计算输入约束（不在此阶段实现进度聚合，见 M5）。
- 需求创建规则：必须属于真实存在且未删除的 Project。

### 2.2 App

- `RequirementService.Confirm`：前置校验（`WORKFLOW.md` §5）→ 单事务写入需求 / 任务 / 步骤 →
  更新会话状态。失败整体回滚，任何一步失败不得留下部分任务。
- 任务配置从 Project 继承（仓库、分支、环境、节点、工作目录、缓存），写入 `config_json` 快照。

### 2.3 持久化

- `schema.sql` 的 `requirements` 增加项目关联与规划快照相关列（结构见 `DATA-MODEL.md` §4）。不提供旧库升级，
  因此没有历史数据回填。
- `schema.sql` 的 `tasks` 增加 `requirement_sequence` 与部分唯一索引。

### 2.4 HTTP

```text
POST /api/v1/planning-sessions/{sessionID}/confirm
GET  /api/v1/requirements/{requirementID}
GET  /api/v1/projects/{projectID}/requirements
```

- `confirm` 只接受 `revision_id`（与可选执行节点），不接受任务列表、顺序覆盖或任务字段覆盖。
- 响应必须返回真实创建的需求 ID 与任务 ID 列表，不得返回"已受理"式模糊语义。
- 无效方案、会话状态不符、Project 缺失或已删除 → 400 / 409 / 412，不写入任何记录。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 每个正式需求至少关联一个 Task | 数据库中不存在 0 任务的需求（除历史数据） |
| 无效方案不能创建正式 Task | 缺少必填字段 / 顺序重复 / 空任务列表均被拒绝 |
| 事务失败不留部分任务 | 注入写入失败后不出现孤立需求或孤立任务 |
| Task 继续使用 V1 执行模型 | 生成的任务可被既有 `TaskLaunchService` / `TaskWorkflowService` 接受 |
| 创建完成后不启动 Scheduler | 任务状态为待执行，无执行记录产生 |
| 需求内顺序稳定 | `requirement_sequence` 与方案顺序一致，唯一索引生效 |

## 4. 验收方式（默认不自动执行）

- 阅读代码：确认流程的前置校验点、事务边界、顺序写入、会话状态收敛。
- `git diff` 核对 `schema.sql` 只包含本次需要的结构变化；`gofmt`、`go vet ./...`。
- 若用户明确要求测试，覆盖：结构化校验各失败分支、事务回滚、顺序唯一性。

## 5. 风险与待决

1. **事务内写步骤**：确认阶段是否立即生成任务步骤（plan/develop/verify）取决于 V1 计划生成
   约定；若步骤由 Planner 在任务启动时生成，则确认阶段只创建需求与任务，必须在本里程碑
   开始前确认，不能两处都写。
2. **旧库处理**：因为不提供升级，已有开发数据库需要人工备份与重建；确认流程不做历史数据回填，
   需求的项目关联只对新建需求生效。
3. **需求标题与描述来源**：来自方案摘要还是用户原文，需要在实现前固定（避免同一需求
   在不同入口显示不同文案）。

## 6. 明确不做

- 不启动执行、不创建容器、不分配任务租约（M5）。
- 不生成项目级进度聚合接口（M5）。
- 不允许确认后修改需求或重新规划。
- 不实现任务分支串接与自动合并。
