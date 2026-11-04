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

## 2. 能力上限与工具映射

角色身份决定系统能力上限，用户配置只能在上限内收紧，不能突破（`domain.RoleToolCeiling`）：

| 角色 | 能力上限 | 默认策略 | Pi 内置工具 |
|---|---|---|---|
| `requirement_analyst` | read | read | `read`、`grep`、`find`、`ls` |
| `planner` | read | read | `read`、`grep`、`find`、`ls` |
| `developer` | read、write、execute | read、write、execute | `read`、`grep`、`find`、`ls`、`edit`、`write`、`bash` |
| `validator` | read | read | `read`、`grep`、`find`、`ls` |

- 能力到工具的映射只在 `internal/domain/role_tools.go` 定义一次：read → `read`/`grep`/`find`/`ls`，
  write → `edit`/`write`，execute → `bash`。
- `bash` 可以执行写文件命令，因此**不是**只读工具：规划角色与验收者都不具备 execute 能力上限。
- 验收者的模型调用保持只读：确定性验证命令由 `app.ValidationService` 在容器内执行，验收者 Agent
  （`app.ValidatorReviewer`）读取代码与检查结果后做语义审查。因此验收者默认策略不含 execute，
  也不通过 `execute` 获得任意 Shell 权限。
- `allow_tools` 为空表示不按名称限制已授权的工具；非空时只保留「能力上限内 ∩ 已授权能力 ∩
  allow_tools」；`deny_tools` 始终优先；不认识的名字一律不允许。
- 最终工具列表由 `domain.EffectivePiTools(roleType, policy)` 计算一次，
  `app.ContainerService.RunAgent` 用它把角色类型与策略换算成工具名称；Pi Runner 只把结果转成
  `--tools <list>`，列表为空时使用 `--no-tools`。
- 规划阶段的项目代码只读最终边界由 M2 的容器只读文件系统实施；工具策略是第一道限制，不是唯一限制。

## 3. 用户可改 / 不可改

可改（持久化到 SQLite `roles`）：

- `system_prompt`
- `tools_policy_json`（`permissions`、`allow_tools`、`deny_tools`）
- `model`
- `enabled`（仅影响是否参与新的调用，历史 Session 不受影响）

不可改（服务端拒绝或直接忽略）：

- 固定 Role ID 与角色数量
- 角色 `type`（职责类别）
- 系统强制安全边界（能力上限、只读挂载、无 Docker Socket、无提权）：`PUT /api/v1/roles/{roleID}`
  提交超出能力上限的 `permissions`、上限外的 `allow_tools` 或未知工具名一律 400，不落库
- 角色不能承担的工作流职责（例如让 `developer` 生成正式需求方案）
- `runner` 不得改成未实现的运行时

## 4. 配置持久化与快照

- 现状已具备：`roles` 表含 `system_prompt`、`tools_policy_json`、`model`、`enabled`、`deleted_at`；
  `sessions.role_snapshot_json` 已按会话保存角色快照。**V2 无需为快照新增结构。**
- 每次规划或执行调用使用**当次读取的配置快照**；配置后续修改不影响已运行或已完成的会话。
- 角色初始化只补齐缺失角色，不覆盖用户已修改的配置。
- 默认 Prompt 随代码内置（不放在可被覆盖的配置目录），用户修改后以数据库记录为准。

## 5. 工具限制在执行层实施（已落地，V2-M1-E3）

实际执行路径（缺口 G1 已关闭）：

```text
SQLite roles.tools_policy_json
  └── app.RoleService.Get / 任务配置关联的角色读取
        ├── app.SessionService.Start（写入 sessions.role_snapshot_json）
        └── app.ContainerService.RunAgent
              ├── domain.EffectivePiTools(roleType, ToolsPolicy)
              │     ├── domain.RoleToolCeiling(roleType)      能力上限
              │     ├── ToolsForPermissions(granted)          能力 → 内置工具
              │     └── ToolsPolicy.AllowsTool(tool)          allow / deny（deny 优先）
              └── agent.RunRequest.Tools
                    └── pi.ContainerRunner.execute
                          ├── --tools read,grep,find,ls,…（为空时 --no-tools）
                          ├── --no-mcp
                          └── --no-extensions
```

- 工具名单与能力上限只在 `internal/domain/role_tools.go` 定义一次；三条真实调用路径
  （`TaskWorkflowService` 的 Planner、`StepExecutor` 的 Developer、`ValidatorReviewer` 的 Validator）
  都只提交 `role.ToolsPolicy`，不各自映射工具，也不各自拼接命令行。
- 策略无法换算（未识别的角色类型、非法策略）时 `RunAgent` 直接失败，不回退到 Pi 默认工具列表，
  也不会退化为"只在 Prompt 里写一句"。
- 工具列表为空时使用 `--no-tools`：省略参数会让 Pi 加载默认工具列表（`read` / `bash` / `edit` / `write`），
  属于越权回退，禁止。
- 托管调用固定附加 `--no-mcp` 与 `--no-extensions`：MCP 工具与扩展注册的自定义工具不能绕过工具策略，
  扩展也无法覆盖内置工具。这只限制 AgentFlow 发起的 Pi 调用，不修改用户的 Pi 配置、扩展、MCP 服务器
  与凭据。
- 规划阶段的项目代码只读边界仍由 M2 的容器只读文件系统兜底；工具限制是第一道限制。
- 实际生效的工具与 `sessions.role_snapshot_json` 中的策略取自同一个 `domain.Role` 值，因此快照可以
  用于事后核对；配置修改只影响后续新会话。

## 6. 角色管理与 API

```text
GET /api/v1/roles            列表（四个固定角色；缺失时初始化补齐）
GET /api/v1/roles/{roleID}   单角色配置
PUT /api/v1/roles/{roleID}   更新 system_prompt / tools_policy / model / enabled
```

- 只允许更新上述字段；携带 `id`、`type`、`runner` 或未知字段一律 400。
- `tools_policy` 提交即整体替换已保存的策略：超出角色能力上限的 `permissions`、能力上限外的
  `allow_tools`、未知工具名一律 400，不落库；未提交的字段保持数据库中的值。
- 详情响应额外返回服务端计算的三项只读信息：`capability_ceiling`（能力上限）、`available_tools`
  （上限内可选工具）与 `effective_tools`（本次配置实际交给 Pi 的工具）。设置页只展示这些值，
  不在前端重复实现权限判定。
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

1. ~~规划角色"受限执行命令"的精确边界~~ **已定稿（V2-M1-E3）**：规划角色与验收者没有 execute 能力
   上限，Pi 调用只收到 `read`/`grep`/`find`/`ls`，不存在"受限命令"这一档；规划阶段的项目代码只读
   边界由 M2 的容器只读文件系统实施。
2. 是否允许用户为四个角色分别配置不同模型。默认倾向：允许，但真实模型调用必须显式传入
   模型标识，不得回退到 Pi 配置中的默认（可能付费的）模型。
