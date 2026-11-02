# V2 角色设计

> 状态：V2 设计基线。前置阅读：[`ARCHITECTURE.md`](./ARCHITECTURE.md) §4、§8。
> 角色身份固定四个，不可增删；可改的只有配置内容，不可改变职责与安全边界。

## 1. 固定四角色

| 固定 ID | 名称 | 职责 | 参与阶段 |
|---|---|---|---|
| `requirement_analyst` | 需求分析师 | 分析用户需求与项目现状，产出结构化需求分析 | 规划 |
| `planner` | 方案规划师 | 产出项目级实施方案与任务拆分；同时承担 V1 任务内部计划生成 | 规划 + 任务计划 |
| `developer` | 开发者 | 执行任务的开发步骤 | 任务执行 |
| `validator` | 验收者 | 执行确定性验证与语义审查 | 任务执行 |

- `planner` 的两个使用场景（项目级方案 vs 任务内部计划）必须有**各自明确的 Prompt 与输入契约**，
  不新增第五个角色，也不共用同一套 Prompt 模板。
- `RoleRunnerPi`（`pi`）仍是唯一运行时；V2 不引入第二个 Agent 运行时。
- 角色 ID 是系统常量（`domain` 中定义固定集合），`RoleConfigService` 拒绝创建与删除。

## 2. 权限矩阵

| 角色 | 读取项目代码 | 写入项目代码 | 执行命令 | 规划容器内可用 |
|---|---:|---:|---:|---|
| `requirement_analyst` | 是 | 否 | 受限 | 只读 |
| `planner` | 是 | 否 | 受限 | 只读 |
| `developer` | 是 | 是 | 是 | — |
| `validator` | 是 | 否 | 是（仅验证命令） | — |

规划阶段始终只读，**修改角色配置不能突破项目代码只读限制**：只读由容器文件系统实施
（见 [`ARCHITECTURE.md`](./ARCHITECTURE.md) §4），不是由角色配置授予或撤销的。

## 3. 用户可改 / 不可改

可改（持久化到 SQLite `roles`）：

- `system_prompt`
- `tools_policy_json`（`permissions`、`allow_tools`、`deny_tools`）
- `model`
- `enabled`（仅影响是否参与新的调用，历史 Session 不受影响）

不可改（服务端拒绝或直接忽略）：

- 固定 Role ID 与角色数量
- 角色 `type`（职责类别）
- 系统强制安全边界（只读挂载、无 Docker Socket、无提权）
- 角色不能承担的工作流职责（例如让 `developer` 生成正式需求方案）
- `runner` 不得改成未实现的运行时

## 4. 配置持久化与快照

- 现状已具备：`roles` 表含 `system_prompt`、`tools_policy_json`、`model`、`enabled`、`deleted_at`；
  `sessions.role_snapshot_json` 已按会话保存角色快照。**V2 无需为快照新增迁移。**
- 每次规划或执行调用使用**当次读取的配置快照**；配置后续修改不影响已运行或已完成的会话。
- 角色初始化只补齐缺失角色，不覆盖用户已修改的配置。
- 默认 Prompt 随代码内置（不放在可被覆盖的配置目录），用户修改后以数据库记录为准。

## 5. 工具限制必须在执行层实施（M1 核心）

当前事实（M0 核实，缺口 G1）：`internal/agent/pi/command.go` 只传
`--print / --session / --model / --append-system-prompt`，`tools_policy_json` **只落库、不生效**。

M1 必须做到：

1. 调用 Pi 时按快照把允许/拒绝列表转换为 Pi 支持的参数（若 Pi 无对应参数，则该限制必须由
   执行环境实施，不允许退化成"只在 Prompt 里写一句"）。
2. 规划角色的写权限限制由**容器只读挂载**兜底（M2），工具限制是第一道限制而不是唯一限制。
3. 拒绝列表优先于允许列表；未识别的工具名不静默放行。
4. 每次实际生效的策略写入 Session 快照，便于事后核对；日志不得打印完整 Prompt 内容中的凭据。

## 6. 角色管理与 API

```text
GET /api/v1/roles            列表（四个固定角色；缺失时初始化补齐）
GET /api/v1/roles/{roleID}   单角色配置
PUT /api/v1/roles/{roleID}   更新 system_prompt / tools_policy / model / enabled
```

- 只允许更新上述字段；携带 `id`、`type`、`runner` 或未知字段一律 400。
- 更新后立即对**新调用**生效，不影响进行中的会话。
- 不提供 `POST /roles` 与 `DELETE /roles/{roleID}`：固定角色的增删不是 V2 能力。

## 7. 与规划会话的关系

```text
PlanningSession
  ├─ analyst_session_id ──> requirement_analyst 的 Pi Session（只读）
  └─ planner_session_id ──> planner 的 Pi Session（只读）
```

- 两个角色各自维护 Session 上下文，互不写入对方 Session 文件。
- 规划会话**不**使用 Task 执行记录模型（不创建 Step / Attempt），只在 `planning_sessions`
  记录 Session 路径与状态，避免用执行记录冒充规划。
- 规划会话的 Session 归档沿用 V1 归档能力，路径在本机持久目录，容器删除后仍可读。

## 8. 待决问题

1. 规划角色"受限执行命令"的精确边界（是否允许 `git log` / `rg` 之外的命令），M2 定稿，
   默认倾向：只允许只读查询类命令，且由容器只读文件系统兜底。
2. 是否允许用户为四个角色分别配置不同模型。默认倾向：允许，但真实模型调用必须显式传入
   模型标识，不得回退到 Pi 配置中的默认（可能付费的）模型。
