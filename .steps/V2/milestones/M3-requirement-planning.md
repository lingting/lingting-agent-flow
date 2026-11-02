# M3：需求工作台与需求分析

> 状态：**pending**。
> 前置：M2 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §2、§4.1，
> [`../ARCHITECTURE.md`](../ARCHITECTURE.md) §5、§6。

## 1. 目标

把需求提出变成一次可多轮调整的对话：

1. 新增需求工作台页面，强制先选择 Project。
2. 用户消息持久化；同一会话内可连续补充信息。
3. 接入需求分析师（`requirement_analyst`）与方案规划师（`planner`）。
4. 每轮分析结果与实施方案作为 Revision 追加保存，预览可见。
5. 未确认之前不创建正式 Requirement 或 Task。

## 2. 范围

### 2.1 Domain

- `PlanningMessage` / `PlanningRevision` / `RequirementAnalysis` / `ImplementationPlan` 类型与校验。
- `PlanningSession` 状态机（`active` / `analyzing` / `preview_ready` / `confirmed` / `abandoned` / `failed`）。

### 2.2 App

- `RequirementAnalysisService`：构造分析输入（需求原文 + 历史消息 + 项目只读代码上下文），
  调用需求分析师，解析结构化结果，失败即本轮失败。
- `RequirementPlanningService`：以分析结果 + 用户补充为输入调用方案规划师，解析实施方案，
  写入 Revision（含结构化校验结果的原始记录）。
- 两个服务共用 `PlanningRuntimeService` 提供的容器与各自 Session。

### 2.3 HTTP

```text
POST /api/v1/projects/{projectID}/planning-sessions
GET  /api/v1/planning-sessions/{sessionID}
POST /api/v1/planning-sessions/{sessionID}/messages
POST /api/v1/planning-sessions/{sessionID}/abandon
```

- `messages` 请求只接受用户文本（可选补充参数），不接受 Session 路径、容器、命令或角色覆盖。
- 规划运行中的重复请求返回 409，不启动第二个 Pi 进程。

### 2.4 前端

- 需求工作台：项目选择 → 消息列表 → 输入框 → 运行状态 → 预览区 → 重跑 / 放弃入口。
- 预览区展示：需求摘要、目标、项目现状、范围、非目标、约束、风险、验收标准、
  实施方案、任务列表与顺序。
- 未选择 Project 时禁止发送；切换 Project 丢弃当前会话上下文；草案任务不得显示为正式任务。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 多轮输入能继续同一规划会话 | 后续消息落到同一 `planning_session_id`，容器与快照不变 |
| 两个角色职责分离 | 分析结果与方案结果分别由不同角色、不同 Session 产生并分别落库 |
| 分析结果结构化、可读取 | 字段完整可解析；解析失败按失败处理，不写半成品 |
| 规划失败可明确显示 | `failed` + 可读原因（脱敏）；可显式重新运行 |
| 未确认前不创建正式实体 | 无 `requirements` / `tasks` 新增记录 |

## 4. 验收方式（默认不自动执行）

- 阅读代码：消息序号分配、Revision 追加、状态迁移入口、结构化解析失败路径、
  Session 路径生成、并发重复请求的拒绝点。
- `gofmt`、`go vet ./...`；前端类型检查与构建。
- 若用户明确要求测试，覆盖：多轮会话复用、状态机非法迁移、解析失败、草案不落正式表、
  前端"未选项目不可发送"与错误展示。

## 5. 风险与待决

1. **结构化输出的稳定性**：模型输出必须是可解析 JSON；解析失败必须是本轮失败并保留原始
   失败摘要，不能猜字段或写入半成品。
2. **上下文长度**：多轮累积消息 + 项目代码上下文可能超限，M3 需要明确的截断策略（以
   最近轮次与结构化摘要为主），且截断事实要记录，不能静默丢弃。
3. **分析输入契约**：需求分析师只读代码，仍需明确"读什么范围"（全仓库 vs 关键模块），
   否则大仓库的分析成本不可控。
4. `DATA-MODEL.md` §5 的迁移风险必须在 `0026`/`0027` 落地时实测并记录结论。

## 6. 明确不做

- 不做确认与任务生成（M4）。
- 不做调度与进度（M5）。
- 不支持已确认需求的修改与重新规划。
- 不允许在规划阶段创建 Step / Attempt / 正式 Task。
