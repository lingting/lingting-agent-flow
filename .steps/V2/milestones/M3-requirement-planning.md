# M3：需求工作台与需求分析

> 状态：**completed**（2026-11-08；V2-M3-E1 后端能力与 V2-M3-E2 需求工作台前端均已交付）。
> 前置：M2 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §2、§4.1，
> [`../ARCHITECTURE.md`](../ARCHITECTURE.md) §5、§6，
> 后端边界见 [`../../../docs/architecture.md`](../../../docs/architecture.md)「需求规划分析边界」，
> 接口契约见 [`../../../docs/http-api.md`](../../../docs/http-api.md) §15。

## 1. 目标

把需求提出变成一次可多轮调整的对话：

1. 新增需求工作台页面，强制先选择 Project。
2. 用户消息持久化；同一会话内可连续补充信息。
3. 接入需求分析师（`requirement_analyst`）与方案规划师（`planner`）。
4. 每轮分析结果与实施方案作为 Revision 追加保存，预览可见。
5. 未确认之前不创建正式 Requirement 或 Task。

## 2. 范围

### 2.1 Domain（已交付）

- `RequirementAnalysis`：摘要、目标、项目现状、范围、非目标、约束、风险、验收标准，八个字段全部必填。
- `ImplementationPlan`：摘要 + 任务草案（`id` / 标题 / 描述 / `depends_on` / 验收条件），五个字段全部必填。
- `PlanningMessage` / `PlanningRevision` / `PlanningSession` 类型与状态机
  （`active` / `analyzing` / `preview_ready` / `confirmed` / `abandoned` / `failed`）。
- 单一严格解析入口 `ParseRequirementAnalysis` / `ParseImplementationPlan`：拒绝未知字段、重复 Key、
  必填字段缺失与 `null`、非 JSON 对象、尾随说明文字与第二份 JSON 文档，未闭合或多层代码块围栏不做
  猜测修复；错误类别分为"无法解析"与"不合法"，都满足 `ErrInvalidValue`。
- `PlanningRevision` 快照版本：`PlanningRevisionSnapshotVersion` + `CheckSnapshotVersion`，
  未知版本拒绝解析。

### 2.2 App（已交付）

- `PlanningSessionService`：会话创建与查询、提交消息并启动分析、显式重试、Revision 与角色消息落库、
  失败收敛、确认与放弃；消息序号与 Revision 版本号由存储层在事务内分配。
- `PlanningAnalysisService`：两阶段总编排。数据库级执行权占用
  （`planning_sessions.analysis_claim_id` 条件更新）、分析师 → 规划师顺序调用（规划师输入为分析师的
  完整结构化快照）、两阶段各自结构化校验、成功后单事务写入 Revision 与两条角色消息并推进
  `preview_ready`、失败时以独立事务记录阶段与脱敏原因并释放占用。
- `PlanningAnalysisRecoveryService`：服务端启动时收敛上一次进程中断遗留的分析占用
  （`analyzing` + 仍有占用 → `failed` + 释放占用，同一事务），运行位置在取得进程锁与打开数据库之后、
  HTTP 开始接收请求之前。
- 两个角色复用 `PlanningRuntimeService` 提供的只读容器与各自稳定 Pi Session。

### 2.3 HTTP（已交付）

```text
POST /api/v1/planning/sessions
GET  /api/v1/planning/sessions?project_id=...
GET  /api/v1/planning/sessions/{sessionID}
GET  /api/v1/planning/sessions/{sessionID}/messages
POST /api/v1/planning/sessions/{sessionID}/messages
GET  /api/v1/planning/sessions/{sessionID}/revisions
POST /api/v1/planning/sessions/{sessionID}/analysis
POST /api/v1/planning/sessions/{sessionID}/analysis/retry
POST /api/v1/planning/sessions/{sessionID}/abandon
GET  /api/v1/planning/sessions/{sessionID}/runtime
POST /api/v1/planning/sessions/{sessionID}/runtime/prepare
POST /api/v1/planning/sessions/{sessionID}/runtime/release
```

- `messages` 请求只接受用户文本，不接受 Session 路径、容器、命令或角色覆盖；未知字段返回 `400`。
- 规划运行中的重复请求返回 `409`；执行权由数据库条件更新给出，不靠接口层的状态判断。
- 持久化失败返回 `500`（`execution_not_persisted`），不返回未落库的 Revision。

### 2.4 前端（已交付）

- 需求工作台（`/planning`）：项目选择 → 会话列表 / 新建会话 → 消息列表 → 输入框 → 分析状态 →
  Revision 预览 → 重试 / 放弃入口。
- 预览区展示：需求摘要、目标、项目现状、范围、非目标、约束、风险、验收标准、
  实施方案摘要、任务草案（草案标识、顺序、依赖、验收条件）。
- 未选择 Project 时禁止创建会话与发送；切换 Project 丢弃当前会话上下文；草案任务不得显示为
  正式任务；历史 Revision 只读保留、可切换。
- 执行节点优先使用会话绑定值，其次使用项目 `default_host_id`；都没有时由用户显式选择，
  页面不自动选中列表中的第一个节点。

## 3. 退出条件

| 条件 | 判定方式 | 状态 |
|---|---|---|
| 多轮输入能继续同一规划会话 | 后续消息落到同一 `planning_session_id`，容器与快照不变 | 满足（前端复用同一会话，E2E 覆盖两轮分析） |
| 两个角色职责分离 | 分析结果与方案结果分别由不同角色、不同 Session 产生并分别落库 | 满足（界面按发送方分别展示） |
| 分析结果结构化、可读取 | 字段完整可解析；解析失败按失败处理，不写半成品 | 满足 |
| 规划失败可明确显示 | `failed` + 可读原因（脱敏）；可显式重新运行 | 满足（界面展示失败摘要与显式重试） |
| 未确认前不创建正式实体 | 无 `requirements` / `tasks` 新增记录 | 满足（E2E 断言未调用正式任务接口） |
| 进程中断遗留的执行占用可恢复 | 启动时收敛为 `failed` 并释放占用，重试可继续 | 满足 |

M3 退出条件全部满足，据此标记 `completed`。真实模型输出的稳定性仍属于 §5 的持续风险，
不是本里程碑的退出条件。

## 4. 验收方式（默认不自动执行）

- 阅读代码：消息序号分配、Revision 追加、状态迁移入口、结构化解析失败路径、
  Session 路径生成、并发重复请求的拒绝点、启动恢复的事务边界与运行位置。
- `gofmt`、`go vet ./...`；前端类型检查与构建。
- 若用户明确要求测试，覆盖：多轮会话复用、状态机非法迁移、解析失败、草案不落正式表、
  同一会话并发分析冲突、持久化失败无半成品、失败与重启恢复后的显式重试、
  前端"未选项目不可发送"与错误展示。

## 5. 风险与待决

1. **结构化输出的稳定性**：模型输出必须是可解析 JSON；解析失败即本轮失败并保留脱敏失败摘要，
   不猜字段也不写半成品。已由严格解析与领域校验覆盖，真实模型的输出分布仍需在真实环境观察。
2. **上下文长度**：多轮累积消息 + 项目代码上下文可能超限；当前按最近轮次裁剪并在提示词中显式写出省略
   条数，截断策略仍需随实际使用调整。
3. **分析输入契约**：需求分析师只读代码，仍需明确"读什么范围"（全仓库 vs 关键模块），
   否则大仓库的分析成本不可控。
4. `DATA-MODEL.md` §6 的 Schema 风险必须在结构变化落地时实测并记录结论。

## 6. 明确不做

- 不做确认与任务生成（M4）。
- 不做调度与进度（M5）。
- 不支持已确认需求的修改与重新规划。
- 不允许在规划阶段创建 Step / Attempt / 正式 Task。

## 7. 交付记录

### 7.1 V2-M3-E1 结构化分析与持久化可靠性收口（2026-11-08）

在已交付的结构化契约与两阶段编排之上完成可靠性收口：

1. **领域契约收紧**：必填字段存在性、`null`、重复 Key（含嵌套任务对象）与文档完整性改为在结构化
   解码之前用 JSON Token 校验器判定，解析入口只有一套；任务标识与依赖引用统一要求规范写法
   （不接受首尾空白裁剪后匹配）；未闭合与多层代码块围栏不再被猜测修复。
2. **持久化失败收敛**：Revision / 角色消息 / 状态条件更新 / 占用释放任一步失败后，原事务整体回滚，
   再用独立事务把本轮记为 `failed`（领域状态迁移 + 脱敏摘要 + 释放占用），返回可通过 `errors.Is`
   识别的 `ErrPlanningAnalysisNotPersisted`；失败记录写不成时如实保留原状态，不虚报失败。
3. **重试与执行权**：显式重试在同一个事务内完成状态推进、执行权取得与可选补充消息；同一标识的抢占
   视为执行确认（幂等），其他标识仍冲突。无占用的 `analyzing` 会话可以重试，重复请求不再追加用户消息。
4. **启动恢复**：新增 `PlanningAnalysisRecoveryService`，只处理"`analyzing` 且仍有占用"的会话，
   状态与占用在同一事务内变更，占用已换人时整体回滚；不重建容器、不引入租约机制。
5. **终止运行的收尾**：显式终止一轮运行（`FailAnalysis`）同时按读到的占用标识释放占用，
   避免失败会话带着无人推进的占用无法重试。

### 7.2 V2-M3-E2 需求工作台与 Revision 预览（2026-11-08）

在 M3-E1 的后端能力之上交付前端工作台，Go 领域与应用服务、SQLite Schema 与 HTTP 契约均未改动。

1. **页面与路由**：新增 `src/pages/planning/`（页面、纯函数规则、四个 hook 与七个面板），
   通过 `src/app/navigation.tsx` 的同一份导航元数据注册 `/planning`（标题「需求工作台」），
   菜单标签、页面标题与高亮都由该元数据派生。
2. **Project 选择**：使用真实 `GET /api/v1/projects`；未选择项目时禁止创建会话与提交需求，
   查询失败显示错误而不是空列表，切换项目清空当前会话、消息与 Revision 上下文。
3. **会话列表与恢复**：按项目查询会话（状态 + 最后更新时间），新建会话使用
   `POST /api/v1/planning/sessions`；恢复会话时重新读取会话、消息与方案版本三份服务端事实。
4. **多轮消息**：用户消息经 `POST /messages` 落库后会话进入 `analyzing`，随后显式调用
   `POST /analysis`；两阶段成功后重新读取会话、消息与 Revision，分析师与规划师消息按发送方
   分别展示，不合并成无来源文本。
5. **长请求处理**：`/analysis` 与 `/analysis/retry` 显式使用 `timeoutMs: 0` 且不传调用方取消
   信号，页面卸载不取消在途模型调用；连接中断只标记"结果无法确认"，随后重新读取服务端状态。
6. **状态展示与失败重试**：状态全部来自服务端，未知取值原样展示且只读；`failed` 展示脱敏失败
   摘要并提供显式重试，`409` 展示执行权冲突并要求重新读取状态，`503` 说明规划能力不可用；
   分析中禁止重复提交，服务端仍为 `analyzing` 而无在途请求时只提供显式恢复，不自动重发。
7. **执行节点**：优先使用会话已绑定的节点，其次使用项目 `default_host_id`；两者都没有时必须由
   用户显式选择，不自动选中列表中的第一个节点，已绑定的节点不允许改指。
8. **Revision 预览**：按 `revision_number` 展示历史版本，默认最新版本、新版本生成后自动跟随；
   需求分析八个字段逐一展示（空列表如实说明），实施方案使用 `ExTable` 承载任务草案的顺序、
   草案标识、标题、描述、依赖与验收条件，并明确标注草案不是正式 Task。
9. **测试与验收**：前端单元测试覆盖接口路径与请求体、`409` / `500` / `503` / 网络错误分类、
   长请求越过默认超时不被中断、状态与操作可用性、执行节点判定与 Revision 选择；
   Playwright 用受控后端覆盖完整闭环、失败与重试、进行中重复操作、`409`、网络中断、
   重新打开历史会话、放弃规划、未知 / 已确认状态、空状态与桌面 / 窄屏布局。
   前端单元测试、浏览器测试、类型检查与生产构建通过；Go 普通 CI 分组（unit / local / e2e）
   与 race 测试通过，`go vet` 与静态检查通过。真实外部依赖分组（pi / ssh / remote）在本机缺少
   真实远程环境，未通过，与本阶段改动无关。
