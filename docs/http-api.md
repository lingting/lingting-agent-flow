# HTTP 接口契约

本文件描述 AgentFlow 对外 HTTP 接口的路径、请求边界与错误语义。各条执行链路的内部状态规则见
[architecture.md](architecture.md)；前端对接口的消费方式见 [frontend.md](frontend.md)。

## 1. 通用约定

- 所有后端接口都挂在 `/api/v1` 下；`/api` 与 `/api/...` 是保留路径空间，永远不能回退为页面。
- 请求与响应只使用 JSON。错误响应统一为：

```json
{"error": {"code": "…", "message": "…", "request_id": "…"}}
```

- 状态码语义固定：参数错误 `400`、目标不存在或归属不符 `404`、状态冲突 `409`、前置条件缺失 `412`、
  执行发生但未成功 `500`、结果未落库 `500`（`execution_not_persisted`）、服务已停止 `503`。不得一律
  返回 `200`。
- 接口只接受执行参数（补充提示词、各类超时、可选的检查覆盖配置）。不接受任意 Session 文件路径、
  工作目录、命令行参数、步骤列表、顺序覆盖或角色覆盖。
- 任务状态取值完全复用 `domain.TaskStatus`，不新增对外状态名，也不把中间状态压平为 `succeeded` /
  `failed`。响应中 `status_terminal` 只表达"是否终态"（`completed`、`cancelled`），`status_finished`
  表达"是否已停止执行"（`completed`、`failed`、`cancelled`），删除判定使用后者。
- 写接口不得把"请求成功"当作"执行已成功"：响应必须如实区分发起、推进与最终结论。

## 2. 基础接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/health` | 报告 HTTP API 与 SQLite 可用性 |
| `GET` | `/api/v1/system/info` | 构建信息（版本、提交、构建时间） |
| `GET` | `/api/v1/settings/config` | 基础运行配置只读快照（四项运行参数） |

基础运行配置接口只读，返回服务启动时实际生效的取值，不与构建信息混合：

```json
{
  "database_path": ".agentflow/agentflow.db",
  "listen_address": "127.0.0.1:26380",
  "max_concurrent_tasks": 3,
  "session_root": "D:\\code\\example\\.agentflow\\sessions"
}
```

- 响应只有这四个字段：`database_path` 是数据库实际使用的路径表达式（相对路径按进程工作目录解析，
  与打开数据库时一致）；`listen_address` 是最终生效的 `host:port`，已包含 `--address` 覆盖；
  `max_concurrent_tasks` 是任务调度的并发上限；`session_root` 是 Session 文件实际写入的绝对目录
  （由数据库目录派生的 `sessions` 目录，与 `workspace.worktree_root` 无关）。
- 取值来自启动阶段已加载并完成覆盖的配置：请求期间不重新读取配置文件，也不触发磁盘扫描、远程连接、
  Docker 操作或配置重载。
- 不返回 SSH known_hosts 路径、凭据、私钥、模型凭据、仓库凭据或完整配置对象，也不提供编辑、保存、
  热更新与重启入口。
- 运行配置快照不可用时返回 `503`（`internal_error`），不返回默认配置或猜测值。

## 3. 任务管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/tasks` | 分页列表 |
| `POST` | `/api/v1/tasks` | 创建任务 |
| `GET` | `/api/v1/tasks/{taskID}` | 任务详情 |
| `POST` | `/api/v1/tasks/{taskID}/cancel` | 请求取消 |
| `DELETE` | `/api/v1/tasks/{taskID}` | 逻辑删除 |
| `GET` | `/api/v1/tasks/{taskID}/retry-eligibility` | 查询是否允许显式重试 |
| `POST` | `/api/v1/tasks/{taskID}/retry` | 显式重试（创建新尝试与新容器） |

约束：

- 任务状态只能由业务层状态机修改：Handler 不得直接赋值 `status`、`deleted_at`、`started_at`、
  `finished_at`，也不得接受客户端指定这些字段。
- 管理接口只做数据管理，不代表任务已经执行。
- 取消语义按状态区分：`queued` 可直接收敛为 `cancelled`；其他未结束状态只记录取消请求并进入
  `cancelling`，实际停止由执行者确认。取消请求幂等，重复请求返回当前状态且不改写 `cancel_requested_at`。
- 删除为逻辑删除：只写 `tasks.deleted_at`，不物理删除，也不级联删除需求、步骤、尝试、会话与事件；
  只有 `completed`、`failed`、`cancelled` 允许删除。
- 状态更新使用带状态前置条件的更新（`UpdateTaskIfStatus` / `DeleteTask`），禁止"先查询、后无条件
  更新"。
- 列表必须分页：默认页大小 20，上限 100，超过上限返回参数错误；响应固定返回 `records` / `total` /
  `current` / `size`。

## 4. 执行节点

执行节点接口挂在 `/api/v1/hosts` 下。客户端不能指定 `validation_status`、`validation_generation` 等
校验字段：新建与影响连接的修改由服务端置为 `checking` 并提交后台校验。错误映射与任务接口一致，
节点不存在返回 404，预检未通过导致的真实远程操作拒绝返回 412。预检语义见 [ssh-hosts.md](ssh-hosts.md)。

## 5. 环境管理

环境接口挂在 `/api/v1/environments` 下，分为只读查询与受控写入口两类。环境属于具体执行节点，
因此所有接口都必须带 `host_id`（查询用查询参数，写接口用请求体），服务端不会回退到默认节点。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/environments` | 列出该节点上的环境与全部版本（含服务端计算的 `usable`） |
| `GET` | `/api/v1/environments/{type}/{name}` | 环境详情：当前可用版本、最新创建版本与版本列表 |
| `GET` | `/api/v1/environments/{type}/{name}/versions/{version}` | 单版本元数据 |
| `GET` | `/api/v1/environments/{type}/{name}/versions/{version}/dockerfile` | 该版本 Dockerfile 正文 |
| `GET` | `/api/v1/environments/{type}/{name}/versions/{version}/build-log` | 该版本构建日志正文 |
| `GET` | `/api/v1/environments/{type}/{name}/versions/{version}/build-status` | 该版本构建结论 |
| `POST` | `/api/v1/environments/init` | 初始化远程环境工作空间（预检 → 目录 → 默认基础/语言环境版本） |
| `POST` | `/api/v1/environments/{type}/{name}/versions` | 追加一个不可覆盖的新版本（内容与既有成功版本一致时复用该版本，返回 `201` 与其元数据） |
| `POST` | `/api/v1/environments/{type}/{name}/build` | 受理一次后台构建 |

约束：

- **版本只追加**：写入口只能新增版本号，历史版本的 Dockerfile、哈希与构建结果不可改写。提交内容与
  某个已成功构建的版本完全一致时，服务端复用该版本号，不产生重复版本。
- 请求体只接受 `host_id`、`dockerfile`、`parent_version`、`extra_files`、`version`、`no_cache` 与
  `with_languages`；不接受 Session 路径、本机/远程工作目录、日志路径、命令行参数或任何凭据。
  Dockerfile 与构建上下文（额外文件的相对路径、权限位、大小）由服务端二次校验，请求体上限 8 MiB。
- 语言环境版本必须记录已构建成功的父基础环境版本；父版本不可用时拒绝创建。
- 构建是长操作：`POST .../build` 只返回 `202` 与 `in_progress`，表示服务端已受理并在后台执行，
  **不代表构建成功**。同一节点同一环境同时只允许一个构建，重复请求返回 409。构建结论只能由
  `GET .../build-status` 读取真实记录得出。
- 构建日志与 Dockerfile 的路径由服务端从已验证的节点、环境与版本记录推导，文件缺失返回 404，
  不会被伪造成空内容。

错误语义：

| 情形 | 状态码 | `error.code` |
|---|---|---|
| 环境类型/名称/版本号非法、Dockerfile 或上下文不合法、缺少 `host_id` | 400 | `invalid_request` |
| 执行节点不存在、环境不存在、版本不存在、构建日志缺失 | 404 | `not_found` |
| 版本写入冲突、状态不允许该操作 | 409 | `state_conflict` |
| 该环境已有构建进行中 | 409 | `environment_build_in_progress` |
| 执行节点未通过环境预检 | 412 | `precondition_failed` |
| 构建已执行且确定失败 | 500 | `environment_build_failed` |
| 构建被中断、结果无法确认 | 500 | `environment_build_uncertain` |
| 环境管理或后台构建服务未装配/已停止 | 503 | `environment_management_unavailable` |

构建中与成功由状态码区分：`GET .../build-status` 在 `pending` / `building` 时返回 `202`（`in_progress`
表示本服务是否有在途构建），`succeeded` 返回 `200`；`failed` 与 `uncertain` 不合并为同一个结论。

## 6. 统一任务启动

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/start` | 准备容器 → 初始化工作区 → 生成计划 → 启动编排 |

- 只接受执行参数（补充提示词与三类超时）。
- 响应显式区分 `stage`、`task_status`、`container_prepared`、`workspace_status`、`workspace_commit`、
  `plan_persisted` 与 `workflow_started`；任一为 false 都不得当作任务已开始执行。
- 阶段判定依据数据库事实，不由客户端指定从哪个阶段继续。工作区初始化结果为 `uncertain` 时返回 409，
  必须先在容器内核验现场。

## 7. 单步骤执行

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/execute` | 对指定 Step 发起一次真实 Agent 调用 |
| `POST` | `/api/v1/tasks/{taskID}/execute-steps` | 按 `sequence` 顺序执行开发步骤 |

- 单步骤执行只接受可选补充提示词与超时；执行同步完成，Attempt 进入终态后才返回。同一步骤同一时刻
  只有一个有效占用，重复与并发请求返回 409。
- 顺序执行响应必须逐步骤返回 Attempt、Session、执行结论与停止原因；遇到失败、超时、取消或中断立即
  停止。

## 8. 确定性验证

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate` | 对指定 Attempt 执行确定性检查 |

- 只接受验证参数（可选超时与检查覆盖配置），不接受工作目录、任意命令行或 Session 路径。
- 错误映射：参数错误 400、目标不存在或归属不符 404、状态冲突 409、前置条件缺失 412。
- 结论或检查结果写入失败时不得报告验证成功：结果携带 `Persisted=false` 与 `PersistenceErrors`，
  映射为 `execution_not_persisted`（500）。

## 9. 单步骤开发—验证闭环

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate` | 开发后接确定性验证并按规则收口 |

- 只接受执行参数（可选补充提示词、开发超时、验证超时）。
- 响应必须如实区分开发结论、验证是否发起、验证结论与最终状态；未验证通过不得返回 `200`。
- 验证配置缺失或无效时在开发之前返回明确错误，不把配置问题伪装成"验证未通过"。

## 10. 计划生成

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/plan` | 在任务容器内调用 Planner 并持久化计划 |

- 成功返回 `200` 与真实结果。已有步骤时直接拒绝，不覆盖已有计划。
- 失败收敛：调用失败 / 超时 / 输出非法 / 校验失败 / 持久化失败 → `failed`；取消 → `cancelling` →
  `cancelled`；服务中断 → `recovery_required`。

## 11. 任务级工作流编排

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/execute-workflow` | 启动一次任务级编排 |
| `GET` | `/api/v1/tasks/{taskID}/workflow` | 查询真实执行进度 |
| `POST` | `/api/v1/tasks/{taskID}/cancel-workflow` | 请求取消在途编排 |
| `POST` | `/api/v1/tasks/{taskID}/resume-workflow` | 从已确认进度恢复编排 |

- 启动返回 `202` 与真实初始状态；只接受执行参数。
- 取消接口只写入取消请求并传播取消信号，最终状态由真实收敛结果决定。
- 恢复只在计划摘要与当前计划一致、现场明确时允许；`recovery_required` 与仍被占用的步骤必须先人工
  处理，禁止在未知现场上重放调用。
- 任务没有已持久化的步骤时返回状态冲突，编排器不会临时生成计划。

## 12. 代码获取

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code` | 在对应 Attempt 的容器内获取代码并准备任务分支 |

- 只接受执行参数（可选目标目录与超时）。
- 该入口是尝试级的：要求 `step_id` / `attempt_id`，并核验容器登记的 `attempt_id` 与本请求一致。
- 错误映射：参数错误 400、目标不存在或归属不符 404、状态冲突 409、前置条件缺失 412、执行未成功 500。
  需要认证的私有仓库返回明确的认证要求（`repository_auth_required`），不静默降级为匿名访问。

## 13. 角色配置

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/roles` | 四个固定角色的列表（保留旧字段） |
| `GET` | `/api/v1/roles/{roleID}` | 单个角色的完整配置与工具信息 |
| `PUT` | `/api/v1/roles/{roleID}` | 更新 `system_prompt` / `tools_policy` / `model` / `enabled` |

- 角色集合固定为四个；`POST /api/v1/roles` 与 `DELETE /api/v1/roles/{roleID}` 返回 `405`。
- `PUT` 只接受上述四个字段：携带 `id`、`name`、`type`、`runner` 或未知字段返回 `400`；未提交的字段
  保持数据库中的值，响应取重新读取的真实保存结果。
- `tools_policy` 是整体替换：`permissions` 取值 `read` / `write` / `execute`，`allow_tools` 与
  `deny_tools` 取 Pi 内置工具名（`read` / `grep` / `find` / `ls` / `edit` / `write` / `bash`）。
  `allow_tools` 为空表示不按名称限制已授权的工具，`deny_tools` 始终优先。
- 工具策略只能在角色的系统能力上限内收紧：规划角色（`requirement_analyst`、`planner`）与
  `validator` 的上限只有 `read`，提交 `write` / `execute`、上限外的 `allow_tools` 或未知工具名一律
  `400`，不落库。
- 详情响应在保存的配置之外返回三项服务端计算的只读字段：

```json
{
  "tools_policy": {"permissions": ["read"], "deny_tools": ["ls"]},
  "capability_ceiling": ["read"],
  "available_tools": ["read", "grep", "find", "ls"],
  "effective_tools": ["read", "grep", "find"]
}
```

- `effective_tools` 就是该角色下一次 Pi 调用实际收到的工具列表；为空表示以 `--no-tools` 调用。
  客户端不得自行推算这三个字段。
- 工具策略无法换算（例如数据库出现不支持的角色类型）时返回 `500`，不返回"没有工具限制"的响应。
- 配置修改只影响后续新会话；已创建会话的 `role_snapshot_json` 不随配置变化。

## 14. 项目登记

项目接口挂在 `/api/v1/projects` 下，任务必须关联一个真实存在的项目。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/projects` | 全部未删除项目（数量有限，不分页，固定 `records` / `total`） |
| `POST` | `/api/v1/projects` | 创建项目（`201`） |
| `GET` | `/api/v1/projects/{projectID}` | 项目详情 |
| `PUT` | `/api/v1/projects/{projectID}` | 整体更新项目配置 |
| `DELETE` | `/api/v1/projects/{projectID}` | 逻辑删除（`204`，无响应体） |

请求与响应字段：

```json
{
  "id": "demo-service",
  "name": "示例服务",
  "repository_url": "ssh://git.example.com/team/repo",
  "base_branch": "main",
  "environment": "go",
  "environment_version": "v0001",
  "caches": ["go-mod"],
  "work_dir": "/workspace/project",
  "default_host_id": null,
  "created_at": "2026-10-28T02:00:00Z",
  "updated_at": "2026-10-28T02:00:00Z"
}
```

- `default_host_id` 是项目的默认执行节点标识。响应用 JSON `null` 表达"没有默认执行节点"，
  不返回空字符串，也不会替换成任意节点标识；请求省略该字段或传空字符串都表示未设置。
  该字段是配置偏好，**不代表**节点当前可用或已通过环境预检，规划运行时仍须执行自己的
  节点与环境前置条件检查。
- 只有引入新的非空 `default_host_id` 时才要求该节点存在且未被逻辑删除；引用不成立返回 `400`
  （`invalid_request`），且不产生项目配置的部分写入。项目已保存的历史引用在节点下线后仍可原样
  提交（项目仍可编辑其它配置），也可以传空值清除。清除在数据库中落为 `NULL`。
- `PUT` 是整体替换：未提交的配置字段表示清除，不提供"省略表示保留原值"的部分更新语义；项目标识
  不可修改。
- 客户端不能提交 `created_at`、`updated_at`、`deleted_at` 等服务端字段，携带未知字段返回 `400`。
- 删除是逻辑删除：只写 `deleted_at`，不级联删除历史任务、代码获取与代码保存记录。

错误语义：

| 情形 | 状态码 | `error.code` |
|---|---|---|
| 标识/名称/仓库/分支/环境非法，默认执行节点不存在或已删除 | 400 | `invalid_request` |
| 项目不存在或已逻辑删除 | 404 | `not_found` |
| 项目标识已存在 | 409 | `state_conflict` |
| 项目服务未装配或已停止 | 503 | `internal_error` |

## 15. 规划会话与两阶段分析

规划会话接口挂在 `/api/v1/planning/sessions` 下。规划阶段只产生 `PlanningSession` /
`PlanningMessage` / `PlanningRevision`，不创建正式 Requirement / Task / Step / Attempt。

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/planning/sessions` | 创建规划会话（`201`），只接受 `project_id` |
| `GET` | `/api/v1/planning/sessions?project_id=…` | 按项目列出会话（最近在前） |
| `GET` | `/api/v1/planning/sessions/{sessionID}` | 会话详情 |
| `GET` | `/api/v1/planning/sessions/{sessionID}/messages` | 消息列表（按序号升序） |
| `POST` | `/api/v1/planning/sessions/{sessionID}/messages` | 追加用户消息并把会话推进到 `analyzing`（`201`） |
| `GET` | `/api/v1/planning/sessions/{sessionID}/revisions` | 方案版本列表（按版本号升序） |
| `POST` | `/api/v1/planning/sessions/{sessionID}/analysis` | 同步执行一轮两阶段分析 |
| `POST` | `/api/v1/planning/sessions/{sessionID}/analysis/retry` | 显式重试（可选附带补充消息） |
| `POST` | `/api/v1/planning/sessions/{sessionID}/abandon` | 放弃规划 |
| `GET` | `/api/v1/planning/sessions/{sessionID}/runtime` | 只读核验规划运行现场 |
| `POST` | `/api/v1/planning/sessions/{sessionID}/runtime/prepare` | 准备规划运行环境（复用生产路径） |
| `POST` | `/api/v1/planning/sessions/{sessionID}/runtime/release` | 释放规划运行环境（复用生产路径） |

请求边界：

- 分析请求体可整体省略，或只携带 `host_id`；重试请求体可携带 `content`（补充消息）与 `host_id`。
- 不接受角色覆盖、模型覆盖、提示词覆盖、Session 路径、容器标识或命令参数；携带未知字段返回 `400`
  （`invalid_request`）。角色、模型、容器与会话路径只由服务端按当前配置与记录决定。

分析结果：

```json
{
  "session": {"id": "…", "status": "preview_ready", "…": "…"},
  "revision": {"id": "…", "revision_number": 1, "snapshot_version": 1,
               "requirement_analysis": {}, "implementation_plan": {}, "created_at": "…"},
  "messages": [{"sender_type": "requirement_analyst"}, {"sender_type": "planner"}],
  "requirement_analysis": {"summary": "…", "goals": [], "current_state": [], "scope": [],
                           "out_of_scope": [], "constraints": [], "risks": [],
                           "acceptance_criteria": []},
  "implementation_plan": {"summary": "…", "tasks": [
    {"id": "task-1", "title": "…", "description": "…", "depends_on": [],
     "acceptance_criteria": []}]}
}
```

- 两个阶段顺序固定：`requirement_analyst` 先执行，`planner` 后执行，两者使用各自独立的稳定 Pi
  Session。规划师的输入是需求分析师的完整结构化快照，不是摘要。
- Revision 与两条角色消息、`preview_ready` 状态、本轮分析占用在同一个事务内提交：任何一步失败都整体
  回滚，不会返回未持久化的 Revision。
- 只有成功持久化才返回 `200`：持久化失败返回 `500`（`execution_not_persisted`），响应中的说明是脱敏
  裁剪后的摘要，会话此时已收敛为 `failed` 并记录同一份摘要。
- 同一会话并发分析返回 `409`（`state_conflict`）：执行权由数据库条件更新给出，而不是接口层的状态判断。
  不同会话之间互不影响。
- 分析失败（调用失败、超时、输出不可解析、输出违反领域约束）返回 `500`（状态码按错误类别映射），
  会话收敛为 `failed`，失败摘要记录阶段并可读；随后可用 `analysis/retry` 显式重试。
- `analysis/retry` 接受 `failed` 以及"没有执行占用"的 `analyzing` 会话：后者是提交消息之后、抢占执行权
  之前进程退出的现场。重试与执行权在同一个事务内完成，因此重复的重试请求不会重复追加用户消息，也
  不会启动第二轮模型调用。已有执行占用的会话返回 `409`。
- 服务端启动时会把上一次进程中断遗留的"`analyzing` + 仍有占用"会话收敛为 `failed`（摘要说明是进程
  中断）并释放占用；随后用户通过 `analysis/retry` 继续。只在 `analyzing` 但没有占用的会话不做推断。
- 未装配规划会话服务返回 `503`；未装配两阶段分析服务时 `analysis` 与 `analysis/retry` 返回 `503`。

错误语义：

| 情形 | 状态码 | `error.code` |
|---|---|---|
| 标识非法、消息为空、请求携带未知字段 | 400 | `invalid_request` |
| 会话或所属项目不存在、已删除 | 404 | `not_found` |
| 状态不允许分析、已被其他执行占用、重复重试 | 409 | `state_conflict` |
| 角色调用未产出可用结果、输出非法 | 500 | `execution_failed` |
| 结果未持久化（含 Revision / 角色消息 / 状态 / 占用任一步失败） | 500 | `execution_not_persisted` |
| 规划服务未装配或已停止 | 503 | `internal_error` |

## 16. 方案确认与正式需求

确认接口把用户指定的那一版 Revision 变成正式 Requirement / Task，是唯一写入正式实体的入口。
生成后的任务保持 `queued`：接口不启动执行，也不创建 Step / Attempt / Session / 容器。

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/planning/sessions/{sessionID}/confirm` | 确认指定 Revision 并生成正式实体（`201`） |
| `GET` | `/api/v1/requirements/{requirementID}` | 正式需求详情与按 `requirement_sequence` 排列的任务 |
| `GET` | `/api/v1/projects/{projectID}/requirements` | 项目正式需求列表（排序确定性） |

请求边界：

```json
{"revision_id": "…", "host_id": "…"}
```

- `revision_id` 必填；`host_id` 可选，为空时使用项目的 `default_host_id`。两者都没有可用节点时
  返回 `412`，不回退本机执行，也不使用规划会话绑定的规划容器节点。
- 不接受需求标识、任务列表、任务顺序、任务标题 / 描述覆盖、`TaskConfig`、仓库地址覆盖、任务状态、
  Step 或 Attempt；携带未知字段返回 `400`（`invalid_request`）。

响应（`201`）：

```json
{
  "requirement": {"id": "…", "title": "…", "description": "…", "priority": "normal",
                  "status": "queued", "source": "manual", "project_id": "…",
                  "planning_session_id": "…", "confirmed_at": "…", "created_at": "…",
                  "updated_at": "…"},
  "tasks": [{"id": "…", "requirement_id": "…", "status": "queued", "requirement_sequence": 1,
             "host_id": "…", "repository_path": "…", "base_ref": "main", "goal": "…",
             "acceptance_criteria": ["…"], "created_at": "…"}],
  "adopted_revision_id": "…"
}
```

- `tasks` 按 `requirement_sequence` 升序排列，顺序与方案任务数组一致；任务状态固定为 `queued`。
- 需求标题取最终需求分析的摘要，描述是"摘要 + 目标 + 范围 + 约束 + 验收标准"的确定性摘要；
  需求初始状态沿用需求领域既有约定（`queued`）。
- 需求、任务、任务顺序与会话状态（`confirmed` + `adopted_revision_id` + `finished_at`）在同一个事务内
  写入：任一步失败整体回滚，不留下半成品实体，也不会返回未落库的结果。
- 同一会话只允许确认成功一次：并发确认只有一个返回 `201`，其余返回 `409`；重复确认同样返回 `409`。
- 未装配需求服务返回 `503`。

错误语义：

| 情形 | 状态码 | `error.code` |
|---|---|---|
| 请求体非法、未知字段、`revision_id` 缺失、方案结构不合法 | 400 | `invalid_request` |
| 会话、项目或 Revision 不存在，或 Revision 不属于该会话 | 404 | `not_found` |
| 会话状态不是 `preview_ready`、并发确认、重复确认 | 409 | `state_conflict` |
| 项目已逻辑删除、缺少可用执行节点、任务角色不可用 | 412 | `precondition_failed` |
| 事务或存储失败 | 500 | `internal_error` / `execution_not_persisted` |
| 需求服务未装配或已停止 | 503 | `internal_error` |

## 17. 项目执行与进度

项目执行是**显式动作**：`queued` 任务不会自动开始执行，只有项目取得执行权后才由既有调度器按项目内
顺序领取。开始与停止**不接受请求体**——任务状态、执行权 `owner` / `epoch`、任务顺序与执行结果全部
由服务端依据持久化事实计算，也不提供"提前启动某个后续任务"或"跳过前序任务"的入口。

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/projects/{projectID}/start` | 开始项目执行：核验现场、取得执行权并触发一次扫描 |
| `POST` | `/api/v1/projects/{projectID}/cancel` | 请求停止项目执行（记录停止意图并调用既有取消入口） |
| `GET` | `/api/v1/projects/{projectID}/execution` | 项目执行状态（只读，无副作用） |
| `GET` | `/api/v1/projects/{projectID}/tasks` | 项目正式任务列表（按项目内执行顺序） |
| `GET` | `/api/v1/requirements/{requirementID}/progress` | 需求进度（只读） |

响应（`/start`、`/cancel`、`/execution`）：

```json
{
  "project_id": "demo",
  "execution": {
    "project_id": "demo",
    "status": "running",
    "counts": {"total": 3, "completed": 1, "running": 0, "pending": 2,
               "failed": 0, "recovery_required": 0, "cancelled": 0, "paused": 0},
    "requirements": 1,
    "blocked": false,
    "current_task_id": "…",
    "next_task_id": "…",
    "lease_active": true,
    "lease_epoch": 2,
    "lease_expires_at": "…",
    "stop_requested": false,
    "stopping": false
  },
  "tasks": [{"id": "…", "requirement_id": "…", "requirement_title": "…",
             "requirement_sequence": 1, "position": 1, "status": "completed",
             "cancel_requested": false, "blocking": false, "created_at": "…"}]
}
```

- `status` 是**投影**，取值 `idle` / `running` / `blocked` / `paused`，只由任务真实状态、执行权与
  停止意图计算；`project_execution_states` 中的记录只表达停止意图，不作为状态机读取。
- `counts` 中 `cancelled` 单独统计，**不并入** `failed`；`current = completed`。
- 阻塞时给出 `blocked`、`blocked_task_id` 与稳定的 `blocked_reason`（如 `task_failed`、
  `task_recovery_required`、`task_cancelled`、`task_paused`、`task_in_flight`、`cancel_requested`）。
- `tasks` 顺序由服务端固定：需求创建时间 → `requirement_sequence` → 任务创建时间 → 任务标识；
  `blocking` 标记当前阻塞项目的任务。历史任务没有 `requirement_sequence` 时为 `null`，排在后面。
- `stop_requested` 表示用户已请求停止领取；`stopping` 表示请求已记录但当前任务仍在收敛。
  `status` 在收敛前仍是 `running`，**不**显示为已停止。

需求进度响应：

```json
{"requirement_id": "…", "status": "blocked",
 "counts": {"total": 2, "completed": 1, "running": 0, "pending": 0,
            "failed": 1, "recovery_required": 0, "cancelled": 0, "paused": 0},
 "current": 1, "total": 2, "blocked": true,
 "blocked_task_id": "…", "blocked_reason": "task_failed",
 "current_task_id": "…", "next_task_id": "…"}
```

- `status` 是进度投影：`planned` / `executing` / `blocked` / `completed`，与持久化的
  `requirements.status`（`queued` / `processing` / …）是两个不同口径。
- `completed` 只在全部关联任务真实 `completed` 时成立；阻塞任务取按执行顺序第一个阻止完成判定的任务。

错误语义：

| 情形 | 状态码 | `error.code` |
|---|---|---|
| 项目标识或需求标识非法 | 400 | `invalid_request` |
| 项目不存在或已逻辑删除；需求不存在或已删除 | 404 | `not_found` |
| 项目已在执行（并发开始或另一执行者持有执行权） | 409 | `state_conflict` |
| 项目当前没有执行（停止请求没有对象）、项目没有待执行任务 | 409 | `state_conflict` |
| 队首任务需要人工处置（失败 / 待核验 / 取消 / 暂停）、队首任务启动前置条件不满足 | 412 | `precondition_failed` |
| 存储失败 | 500 | `internal_error` / `execution_not_persisted` |
| 项目执行服务或调度器未装配 | 503 | `internal_error` |

只读约束：`/execution`、`/tasks` 与需求进度查询不触发启动、续租、释放或其他写操作；它们不做任何
状态迁移，重复查询结果只随时间与真实任务状态变化。
