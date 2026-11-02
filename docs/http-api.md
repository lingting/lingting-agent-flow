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

## 5. 统一任务启动

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/start` | 准备容器 → 初始化工作区 → 生成计划 → 启动编排 |

- 只接受执行参数（补充提示词与三类超时）。
- 响应显式区分 `stage`、`task_status`、`container_prepared`、`workspace_status`、`workspace_commit`、
  `plan_persisted` 与 `workflow_started`；任一为 false 都不得当作任务已开始执行。
- 阶段判定依据数据库事实，不由客户端指定从哪个阶段继续。工作区初始化结果为 `uncertain` 时返回 409，
  必须先在容器内核验现场。

## 6. 单步骤执行

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/execute` | 对指定 Step 发起一次真实 Agent 调用 |
| `POST` | `/api/v1/tasks/{taskID}/execute-steps` | 按 `sequence` 顺序执行开发步骤 |

- 单步骤执行只接受可选补充提示词与超时；执行同步完成，Attempt 进入终态后才返回。同一步骤同一时刻
  只有一个有效占用，重复与并发请求返回 409。
- 顺序执行响应必须逐步骤返回 Attempt、Session、执行结论与停止原因；遇到失败、超时、取消或中断立即
  停止。

## 7. 确定性验证

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate` | 对指定 Attempt 执行确定性检查 |

- 只接受验证参数（可选超时与检查覆盖配置），不接受工作目录、任意命令行或 Session 路径。
- 错误映射：参数错误 400、目标不存在或归属不符 404、状态冲突 409、前置条件缺失 412。
- 结论或检查结果写入失败时不得报告验证成功：结果携带 `Persisted=false` 与 `PersistenceErrors`，
  映射为 `execution_not_persisted`（500）。

## 8. 单步骤开发—验证闭环

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate` | 开发后接确定性验证并按规则收口 |

- 只接受执行参数（可选补充提示词、开发超时、验证超时）。
- 响应必须如实区分开发结论、验证是否发起、验证结论与最终状态；未验证通过不得返回 `200`。
- 验证配置缺失或无效时在开发之前返回明确错误，不把配置问题伪装成"验证未通过"。

## 9. 计划生成

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/plan` | 在任务容器内调用 Planner 并持久化计划 |

- 成功返回 `200` 与真实结果。已有步骤时直接拒绝，不覆盖已有计划。
- 失败收敛：调用失败 / 超时 / 输出非法 / 校验失败 / 持久化失败 → `failed`；取消 → `cancelling` →
  `cancelled`；服务中断 → `recovery_required`。

## 10. 任务级工作流编排

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

## 11. 代码获取

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code` | 在对应 Attempt 的容器内获取代码并准备任务分支 |

- 只接受执行参数（可选目标目录与超时）。
- 该入口是尝试级的：要求 `step_id` / `attempt_id`，并核验容器登记的 `attempt_id` 与本请求一致。
- 错误映射：参数错误 400、目标不存在或归属不符 404、状态冲突 409、前置条件缺失 412、执行未成功 500。
  需要认证的私有仓库返回明确的认证要求（`repository_auth_required`），不静默降级为匿名访问。
