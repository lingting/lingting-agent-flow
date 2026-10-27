# AgentFlow 阶段式开发路线图

本目录是 AgentFlow 项目**唯一的开发路线图真相来源**。它记录项目当前处于哪个阶段、
当前推进哪个里程碑、每一步为什么先做、以及如何判断"真正完成"。

任何后续开发都必须以本目录为依据，并在实施前后同步维护这些文档。路线图描述的是
**规划与现状**，不是产品文档，也不替代 `README.md` 或 `docs/` 中的设计说明。

## 用途

1. 让任何一个 Coding Agent（或人）在没有聊天历史的情况下，理解项目当前状态与下一步。
2. 约束开发范围：每个里程碑只做既定工作，不顺手扩大范围。
3. 记录"哪些能力已实现、哪些只部分实现、哪些待调查"，并附代码证据。
4. 记录每个里程碑的退出条件与验收标准，避免把"能跑起来"误判为"已完成"。

## 维护规则

- **代码与路线图必须一致**：开发过程中发现路线图与代码事实冲突时，先更新本目录的
  状态与证据，再确定实施方案；不要带着过时的结论继续写代码。
- **不把未实现写成已实现**：任何"已实现/部分实现"的判断都必须能指向文件、函数或测试。
- **不把设想写成既定事实**：未经确认的设计选择一律归入"风险与待决问题"或"待调查"。
- **完成定义是硬门槛**：只有满足里程碑的完成定义且证据充分，才能标 `completed`；
  只完成部分就保持 `in_progress` 并写明剩余条件。
- **验证结果如实区分**：通过、失败、跳过、未执行、无法确认必须分别记录，不得混写。

## 当前阶段与当前里程碑

- **当前阶段**：P4 —— 产品操作体验与针对性优化。
- **当前里程碑**：`P4-M3` —— 展示阶段进度、验证结果、失败原因和恢复状态，状态
  `in_progress`（M3-1、M3-3、M3-4、M3-5 已完成、M3-2 已由 M2-7 满足，M3-6 至 M3-8 未开始）。
  详见下方"当前里程碑"详述与
  [`P4-M3-1-attempt-session-history.md`](./P4-M3-1-attempt-session-history.md)。
- **上一里程碑**：`P4-M2` —— 提供完整任务生命周期的必要操作入口，状态 `completed`
  （M2-1 至 M2-7 全部完成）。
  子任务 **M2-1（角色初始化 + `GET /api/v1/roles`）已于 2026-10-26 完成**，交付物为
  [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md)：角色初始化
  成为 `agentflow server` 启动协议的一部分（数据库打开后、业务服务之前，复用
  `app.RoleSeeds` + `storage.RoleSeeder.SeedRoles`，只在空表时导入，失败即启动失败），
  并新增只读角色列表接口（最小字段集、禁用角色如实返回、依赖缺失 503、存储错误不伪装成
  空列表）。子任务 **M2-2（环境查询 HTTP API）已于 2026-10-26 完成**，交付物为
  [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)：新增只依赖
  存储的 `app.EnvironmentQueryService` 与两条只读路由
  （`GET /api/v1/environments?host_id=…`、
  `GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=…`），返回真实构建
  状态、`image_ref`、服务端计算的 `usable`、`current_version` 与 `latest_version`；
  纯读取不再要求 SSH / Docker 依赖，也不产生远程副作用。子任务 **M2-3（Web 接入统一任务
  启动 + 环境选择）已于 2026-10-26 完成**，交付物为
  [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md)：任务页"启动"改走
  `POST /api/v1/tasks/{id}/start`（`execute-workflow` 已从新任务启动流程移除），启动结果
  按 `workflow_started` 区分"已启动"与"未启动/被阻塞"，失败不伪造运行状态，刷新失败不
  抹除已得到的结论；任务创建弹窗的语言环境与版本改由环境查询接口驱动，只有 `usable=true`
  的版本可选。子任务 **M2-4（创建任务表单接入执行节点与角色选择器）已于 2026-10-26
  完成**，交付物为
  [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md)：创建弹窗
  复用 `fetchHosts()` 并新增 `fetchRoles()`，手填的执行节点与角色标识全部改为真实接口
  驱动的选择器；节点只允许已启用且 `usable_for_remote_task` 的记录，角色按 `type` 分类
  并排除 `enabled=false`，节点切换清空旧环境与版本并重新查询；加载中 / 失败 / 空列表 /
  某类无启用角色分别呈现，失败不伪装为空列表且带重试入口；提交仍只用
  `CreateTaskRequest` 的真实 `host_id` 与角色 `id`，创建不自动启动任务。子任务
  **M2-5（验证失败反馈查询 API）已于 2026-10-26 完成**，交付物为
  [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md)：新增只读
  `GET /api/v1/tasks/{taskID}/steps/{stepID}/validations/{runID}/feedback`，由
  `app.ExecutionRecordService.GetValidationFailureFeedback` 校验任务 → 步骤 → 验证记录 →
  反馈的完整归属链后返回已持久化的 `domain.ValidationFailureFeedback`（含逐项失败检查的
  标识、必需标记、状态、退出码、原因与已脱敏输出摘要），不构造空反馈、不跨任务泄露、
  不触发任何执行或状态写入；步骤级"最近一次反馈"入口经核实由验证历史列表覆盖，本轮
  有意不新增并记录依据。子任务 **M2-6（代码保存记录列表 API）已于 2026-10-26 完成**，
  交付物为
  [`P4-M2-6-code-save-records-api.md`](./P4-M2-6-code-save-records-api.md)：新增只读
  `GET /api/v1/tasks/{taskID}/code-saves`，让 Web 能取得真实 `recordID`；响应逐字段映射
  `domain.CodeSaveRecord`，提交 / 推送 / 合并是三个独立事实，`persist_status` 与
  `recovery_status` 分别表达"记录是否可信"与"核验结论"（空值按领域规则归一化为
  `persisted` / `unverified`）；查询由新增的 `app.CodeSaveQueryService` 承担，它只依赖存储，
  不复用强制要求 SSH 的 `ContainerService`，因此"看记录"不再需要远程能力。剩余工作项
  不复用强制要求 SSH 的 `ContainerService`，因此"看记录"不再需要远程能力。子任务
  **M2-7（Web 接入计划生成、步骤执行与验证修复操作）已于 2026-10-26 完成**，交付物为
  [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md)：先按真实
  路由核实 8 项操作的请求 DTO、前置条件、成功响应与失败映射（含"闭环未通过验证返回 500"
  "顺序执行 200 但可能 `success=false`"两条易误判契约），再补齐前端 API 封装与操作界面——
  任务详情 → 计划步骤 → 步骤详情 → 尝试与验证结果 → 明确后续动作；接入计划生成、单步骤
  执行、顺序执行、开发—验证闭环、指定 Attempt 确定性验证、代码初始化与显式开启修复周期，
  全部动作只使用当前真实记录的标识、动作在途时禁止重复提交、失败原样展示服务端原因、
  刷新失败不覆盖已取得的执行结论、从不自动重试。**本轮未修改任何后端代码**；全量前端
  单元测试 191 通过，新增 E2E 20/20 通过，全量 E2E 70 通过 / 3 失败（失败项经
  `git stash -u` 基线对照确认为既有问题）。P4-M2 的 7 个子任务至此全部完成。详见
  [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md)。
- **当前里程碑**：`P4-M3` —— 展示阶段进度、验证结果、失败原因和恢复状态，状态
  `in_progress`。2026-10-26 按当前代码核实并实施子任务 **M3-3（任务最终结论与收尾闭环
  展示）**，交付物为
  [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md)：先核实
  `TaskResponse` 已输出 `final_result` / `closeout` / `closeout_summary`（`omitempty` 与
  派生规则见 `domain.DecideTaskCloseoutView`），再补齐前端 `Task` 类型（新增 `TaskResult` /
  `TaskCloseoutState`，未知取值保留原样展示分支），并把任务详情拆成"任务状态 / 最终业务
  结论 / 收尾闭环"三行独立事实 + 按需警示；`completed`、`succeeded` 与 `confirmed` 未被
  混同，`pending` / `unconfirmed` / `inconsistent` / 缺失值与未知值均不误报成功。
  **本轮未修改任何后端代码**；新增前端单元测试 27 个与 E2E 10 个，全量单元测试 218 通过，
  全量 E2E 80 通过 / 3 失败（失败项经 `git stash -u` 基线对照确认为既有问题）。
  同轮核验：**M3-2 已由 P4-M2-7 满足**（验证历史、逐项检查与失败反馈视图齐备）；
  **M3-1 当时部分满足**，仍缺尝试的 `timed_out` / `cancelled` / `error_class` /
  `failure_stage` 与会话归档 / 导出状态的中文标签，**已于后续 M3-1 交付闭合**。
  同轮实施子任务 **M3-5（代码保存三阶段状态展示）**，交付物为
  [`P4-M3-5-code-save-status-display.md`](./P4-M3-5-code-save-status-display.md)：前端补齐
  `CodeSaveRecord` / `CodeSaveRecordListResponse` 类型与 `fetchTaskCodeSaves`
  （`GET /api/v1/tasks/{taskID}/code-saves`），任务详情新增只读面板 `CodeSavePanel`，
  提交 / 推送 / 合并 / 记录写入状态 / 核验结论五组事实分别展示，`unknown` / `pending` /
  `not_requested` / `skipped` / `unconfirmed` / `unverifiable` 与未知取值均不误报成功，
  核验结论与 `stage` 一起呈现，空列表只说明没有记录。**本轮未修改任何后端代码**；新增前端
  单元测试 30 个与 E2E 14 个，全量单元测试 248 通过，全量 E2E 94 通过 / 3 失败（失败项经
  `git stash -u` 基线对照确认为既有问题）。同轮实施子任务 **M3-4（续跑资格与阻塞原因展示）**，
  交付物为
  [`P4-M3-4-resume-eligibility-display.md`](./P4-M3-4-resume-eligibility-display.md)：前端补齐
  `ResumeEligibilityResponse` / `ResumeEligibilityCodeSave` 类型与 `fetchTaskResumeEligibility`
  （`GET /api/v1/tasks/{taskID}/resume-eligibility`），任务详情新增只读面板
  `ResumeEligibilityPanel`，把 `can_resume`、全部 `blocking`、`code_save_action` /
  `code_save_summary`、`code_save_records` 与 `next_actions` 依据服务端响应如实展示；未知
  阻塞分类与未知动作原样保留，查询失败不显示为允许续跑，刷新失败保留上一次真实快照，
  切换任务丢弃迟到响应，`next_actions` 只展示原文而不自动执行。**本轮未修改任何后端代码**；
  新增前端单元测试 34 个与 E2E 16 个，全量单元测试 282 通过，全量 E2E 110 通过 / 3 失败
  （失败项经 `git stash -u` 基线对照确认为既有问题）。
  同轮实施子任务 **M3-1（Attempt / Session 历史展示补齐）**，交付物为
  [`P4-M3-1-attempt-session-history.md`](./P4-M3-1-attempt-session-history.md)：先按当前代码
  逐字段核实 `httpapi.AttemptResponse` 已输出 `timed_out` / `cancelled` / `error_class` /
  `failure_stage`、`SessionResponse` 已输出 `archive_status` / `export_status`，再把
  `AttemptBlock` 的四个尝试字段补齐（超时与取消只读独立布尔，不从 `status` 反推；
  `error_class` / `failure_stage` 缺省时不渲染、未知取值原样保留），并抽出 `SessionLine`
  用三张**互不复用**的映射表把归档 / 导出 / 执行状态中文化（未知值原样、缺省用中性占位符
  `未提供`）。**本轮未修改任何后端代码，也未新增或修改任何 HTTP API**；新增前端单元测试
  30 个与 E2E 6 个，全量单元测试 326 通过，全量 E2E 116 通过 / 3 失败（失败项经
  `git stash -u` 基线对照确认为既有问题）。至此 **M3-1 已完成**，字段缺口全部闭合，
  M3-6 至 M3-8 未开始。
- **上一里程碑**：`P4-M2` —— 提供完整任务生命周期的必要操作入口，状态 `completed`
  （M2-1 至 M2-7 全部完成）。详见
  [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md)。
- **更早里程碑**：`P4-M1` —— 盘点 API 与 Web 业务操作能力的缺口，状态 `completed`
  （2026-10-26，基于 `master` HEAD `70f82b1` 的实际代码核实）。交付物为
  [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md)。
  本次**未修改任何生产代码或前端功能代码**，只建立缺口清单与后续实施依据。核对结论：
  后端执行链路已完整（统一启动、计划生成、单步骤/顺序执行、开发—验证闭环、修复周期、
  确定性验证、任务级编排、显式重试、续跑资格、代码保存核验、执行记录查询均有服务与
  HTTP 入口），但 Web 只接通任务 CRUD、工作流启停恢复与执行节点管理。
  三条 P0 端到端断链：
  G1 `POST /api/v1/tasks/{taskID}/start` 未接入 Web（任务页直接调 `execute-workflow`，
  对 `queued` 新任务必然 409，因为 `domain.CheckWorkflowStart` 要求已有容器与已持久化
  步骤）——**已于 P4-M2-3 闭合**；G2 角色初始化（`app.RoleSeeds` + `storage.RoleSeeder.SeedRoles`）**生产代码零
  调用者**，`agentflow server` 启动不导入角色、CLI 无角色命令、无 `/api/v1/roles`，全新
  数据库 `roles` 为空导致 `CreateTask` → `ensureRoles` 失败（历史文档从未记录该断链）；
  G3 环境（语言环境/版本/镜像）只有 CLI，无 HTTP，`/start` 会在 `container` 阶段失败。
  **其中 G2 已在 P4-M2-1 闭合后端部分**（启动期导入初始角色 + `GET /api/v1/roles`），
  **G3 的查询部分已在 P4-M2-2 闭合**（`GET /api/v1/environments` 与单版本查询；构建与
  CRUD 仍只有 CLI），**G1 与 G3 的前端接入已在 P4-M2-3 闭合**（任务页走 `/start`，
  创建弹窗的环境与版本来自环境查询接口），上述描述保留为 P4-M1 盘点时的现场记录。
  另有 7 项 P1 业务决策阻塞（重试、验证失败反馈、执行记录历史、代码保存列表、容器处置、
  收尾结论、续跑阻塞原因）、6 项 P2、3 项 P3，共 21 项，逐项附代码证据、验收标准与
  里程碑归属。本次未运行任何测试，全部结论为静态代码核实，已在文档 §9 如实记录。
  另外记录了 7 处历史文档与代码不符之处（README 仍称并发调度未实现、任务调度器未接入、
  项目/环境/容器/代码保存均无 HTTP 接口、任务页尚未接入步骤执行入口等）。
- **下一步**：继续 `P4-M3`（展示阶段进度、验证结果、失败原因和恢复状态）的剩余子项。
  2026-10-26 的核实结论：**M3-3 已完成**（任务最终结论与收尾闭环展示，见
  [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md)）；**M3-5 已完成**
  （代码保存三阶段状态展示，见
  [`P4-M3-5-code-save-status-display.md`](./P4-M3-5-code-save-status-display.md)）；**M3-2 已由
  P4-M2-7 满足**（验证历史、逐项检查与失败反馈视图齐备，无需重复实现）；**M3-1 已完成**
  （Attempt / Session 历史展示补齐，尝试的 `timed_out` / `cancelled` / `error_class` /
  `failure_stage` 与会话归档 / 导出状态的中文标签全部闭合，见
  [`P4-M3-1-attempt-session-history.md`](./P4-M3-1-attempt-session-history.md)）；**M3-4 已完成**
  （续跑资格与阻塞原因展示，见
  [`P4-M3-4-resume-eligibility-display.md`](./P4-M3-4-resume-eligibility-display.md)）。
  M3-6 至 M3-8 未开始，其中 M3-6
  （工作流步骤起止时间）与 M3-8（事件时间线、修复周期历史）需要先补后端能力。
  建议顺序：M3-7（进度自动刷新）→ M3-6 / M3-8。
  环境构建 Web 入口与角色变更入口是否上 Web 仍按产品取舍决定。详见
  [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md)。
- **更早里程碑**：`P3-M5` —— 验证多任务并发、冲突和资源回收行为，状态
  `completed`（2026-10-25，基于 `master` HEAD `abfdfd3` 之后的工作区核实与实施）。
  交付物为 [`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md)。
  本次以可复核的测试证据为中心，先定位真实缺口再修复，共修复 4 个生产缺陷：
  D1 取消的无条件整行写入会覆盖并发领取的租约（新增
  `storage.TaskStore.UpdateTaskCancellation`，只写取消列并带状态前置条件）；
  D2 CLI 与 HTTP 各有一套取消状态语义、`queued` 任务停在 `cancelling`
  （新增 `domain.Task.ApplyCancellationRequest` 作为唯一落点）；
  D3 `isContainerMissing` 把归属不匹配当作"容器不存在"，使回收伪造"按删除成功处理"；
  D4 `TaskScheduler.Scan` 的名额统计与领取不在同一临界区，并发扫描实测可超额领取
  （新增 `scanMu`）。测试证据：
  `tests/integration/local/scheduler/{concurrency,resource}_acceptance_test.go`
  （16 个用例，真实 SQLite + 正式迁移 + 多独立连接）与
  `tests/integration/remote/docker/concurrency_acceptance_test.go`
  （3 个真实远程用例：`unknown` / 孤儿 / absent 现场注入、两个任务并行与分别回收、
  短暂不可达后重新核验）。并发上限语义已在文档 §3 明确为"同一份数据库上的名额
  上限"，跨库全局配额是明确的非目标。
- **更早里程碑**：`P3-M4` —— 打通任务取消、失败、恢复与调度器重启，状态
  `completed`（2026-10-25，基于 `master` HEAD `b61b7e4` 之后的工作区核实与实施）。
  交付物为 [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md)。核心改动：
  过期任务租约的残留核验（`internal/app/task_lease_recovery.go` 的
  `TaskLeaseRecoveryService`，只记录现场、不释放租约、不改写任务状态）与启动顺序
  约束（`internal/server/server.go` 中 `TaskLeaseRecovery` 注册在 `TaskScheduler` 之前）；
  失败任务的显式重试入口（`internal/domain/task_retry.go` 的
  `DecideTaskRetryEligibility` / `AllowsExplicitRetry` / `ReopenForRetry` 与 10 个稳定
  拒绝原因，`internal/app/task_retry.go` 的 `TaskRetryService`，
  `POST /api/v1/tasks/{taskID}/retry`、`GET .../retry-eligibility`，CLI
  `agentflow task retry` / `task retry-status`）。测试证据：
  `tests/unit/domain/task_retry_test.go`、
  `tests/integration/local/scheduler/{restart_protocol,retry}_test.go`、
  `tests/unit/httpapi/task_retry_test.go`、
  `tests/integration/local/workflow/retry_e2e_test.go`。
- **更早里程碑**：`P3-M3` —— 落实执行环境、工作区、Session 与资源隔离，状态
  `completed`（文档记录日期 2026-10-26；本次实施发生在 2026-10-25，日期不自洽已在
  `P3-M4-scheduler-recovery.md` §1.2 记录，未追溯修改）。
  交付物为 [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md)。核心改动：
  容器归属契约（`internal/domain/container_ownership.go` 的 `DecideContainerOwnership` /
  `CheckContainerOwnership`，9 个稳定拒绝原因）与统一加载入口
  （`internal/app/task_container.go` 的 `loadOwnedContainer`），六个执行入口
  （工作区初始化、代码初始化、步骤执行、验证、计划生成、工作流编排、任务启动）
  不再直接用 `task.ContainerID` 字符串核验容器；单步骤三个入口
  （`execute` / `execute-steps` / `execute-and-validate`）统一领取任务租约，
  步骤占用与任务终态写入新增租约栅栏（`StepExecutionStartRequest.Lease`、
  `StepExecutionStore.UpdateTaskLeaseGuard`）；领取冲突细分为 404 / 409
  （`ErrTaskLeaseNotFound` + `classifyClaimConflict`）。
  测试证据：`tests/unit/domain/container_ownership_test.go`（8 个用例）、
  `tests/integration/local/scheduler/{isolation,lease_fencing,resource_reclaim}_test.go`
  （真实 SQLite + 正式迁移，覆盖跨任务容器隔离、租约 fencing、孤儿容器与共享缓存边界）。
- **更早里程碑**：`P3-M2` —— 实现有界并发和任务生命周期协调，状态 `completed`
  （2026-10-25，基于 `master` HEAD `d8a0fdd` 之后的工作区核实与实施）。交付物为
  [`P3-M2-bounded-concurrency.md`](./P3-M2-bounded-concurrency.md)。核心改动：
  任务租约的持久化 fencing（`tasks.lease_epoch`，迁移 `0024_task_lease_epoch.sql`；
  `AcquireTaskLease` 原子递增代次并回读、`RenewTaskLease` / `ReleaseTaskLease` /
  `UpdateTaskLeaseGuard` 以 `owner + epoch` 为条件）；统一所有权
  （`app.WorkflowExecutionGrant`，`WorkflowRun.WorkerID` 取自任务租约 owner，心跳同时
  续租两者）；有界并发调度器 `app.TaskScheduler`（复用 `domain.DecideTaskQueueAdmission` /
  `SortTaskQueueOrderKeys` / `DecideTaskSlotState`，只调用既有 `TaskLaunchService.Launch`，
  复用 `cfg.Scheduler.MaxConcurrentTasks`）；四个显式执行入口（`start` / `plan` /
  `execute-workflow` / `resume-workflow`）统一先领取任务租约。
  测试证据：`tests/unit/domain/task_lease_test.go`、
  `tests/integration/local/scheduler/lease_store_test.go`（9 个用例，真实 SQLite +
  多独立连接并发领取）、`tests/integration/local/scheduler/scheduler_test.go`
  （12 个用例，真实 SQLite + 正式迁移 + 真实调度器）。
- **更早里程碑**：`P3-M1` —— 明确任务队列、领取和调度的业务语义，状态 `completed`
  （2026-10-25，基于 `master` HEAD `6e6d8b2` 之后的工作区核实与实施）。交付物为
  [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md) 与
  `internal/domain/task_queue.go`（队列准入、排序、领取仲裁、执行权 fencing、交接、
  调度名额归属的纯领域规则）+ `tests/unit/domain/task_queue_test.go`（30 个用例）。
- **更早里程碑**：`P2-M5` —— 补齐关键恢复场景的集成测试与恢复验收，状态
  `completed`（2026-10-25，基于 `master` HEAD `40460d4` 实施与核实）。
- **更早里程碑**：`P2-M4` —— 核验提交、推送、合并等外部副作用的恢复路径，状态
  `completed`（2026-10-25，基于 `master` HEAD `168fd2f` 实施）。交付内容：修复 8 个
  真实故障窗口——不可信标记落盘（`codesaver.Session.flagUnconfirmed`）、HEAD 归因改为
  依据第一父（`CodeSaveObservation.HeadParentCommit`）、已确认阶段状态条件写回
  （`storage.CodeSaveStore.UpdateCodeSaveRecoveryResult` + `domain.CodeSaveRecord.ApplyRecovery`）、
  多阶段核验顺序收敛（`recoveryStatusMachine` 允许已收敛结论间迁移）、
  恢复入口前置门控（`TaskWorkflowService.checkCodeSaveRecovery`）、核验失败降级为
  `unverifiable`、启动扫描可发现未决记录（`app.CodeSaveRecoveryService`）。
  新增 `domain.DecideCodeSaveResume` / `domain.DecideTaskCodeSaveResume` /
  `domain.CodeSaveRecord.HasUnresolvedSideEffect` 统一定义续跑资格。
  测试证据：`tests/integration/local/codesaver/recovery_test.go`（11 个用例，真实 SQLite +
  真实 Git）、`tests/integration/local/workflow/code_save_recovery_test.go`（4 个用例）、
  `TestE2EIndeterminateDoesNotResumeWithoutVerification`、`tests/unit/domain/code_save_resume_test.go`。
- **更早里程碑**：`P2-M3` —— 明确修复次数上限、停止条件、重试资格和结果不确定处理
  规则，状态 `completed`（2026-10-24，基于 `master` HEAD `2a1bb0d` 实施）。交付内容：
  预算落到持久化事实（`repair_cycles` + `attempts.budget_scope*`，迁移
  `0022_repair_cycles.sql`）；重试资格与预算由 `domain.DecideRepairStart` /
  `domain.DecideRepairBudget` 统一定义，编排与恢复都引用它；用户显式新周期走
  `POST .../steps/{id}/open-repair-cycle`（CLI `agentflow task reopen-step`），只在上一
  周期已收敛且预算用尽或策略不再允许修复时成功；反馈写入失败由
  `RepairClassFeedbackUnavailable` 明确停止，首轮依据只取当前周期内最近一次验证。
  测试证据：`tests/unit/domain/repair_cycle_test.go`、
  `tests/integration/local/workflow/repair_budget_test.go`（含重启与并发）、
  `e2e_repair_limit_test.go` 的 `TestE2ERepairLimitSurvivesRestart`。
- **更早里程碑**：`P2-M2` —— 明确超时、取消、中断、进程重启后的恢复语义，状态
  `completed`（2026-10-24，基于 `master` HEAD `2a1bb0d` 实施与核实）。交付物为
  [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)：四类中断的
  判定依据、六层状态 × 四类中断的收敛矩阵、跨层一致性规则。本次同时修复了 B1（步骤占用
  与 `blocked` 执行记录互相锁死）、B5（取消意图跨层丢失）、B6（租约无续租），并新增
  `app.ExecutionResidueService` 收敛 `attempts` / `sessions` / `validation_runs` 残留。
- **更早里程碑（续）**：`P2-M1` —— 梳理所有阶段的状态持久化与恢复边界，状态 `completed`
  （2026-10-23，基于 `master` HEAD `7a55dfe` 核实）。交付物为
  [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)：14 个阶段的完整恢复边界
  矩阵、服务启动恢复的真实调用链（`HostValidationService` → `StepExecutor`/`Validation`/
  `StepWorkflow`（无恢复）→ `TaskWorkflowService` → `TaskLaunchService`）、
  自动恢复/用户触发/只读核验/只能安全停止/完全缺失五级分类、G1–G14 未覆盖故障窗口清单，
  以及 6 个阻塞或高风险缺陷（B1–B6）与 1 个语义不一致（B7）。该里程碑**未修改生产代码**。
  `P1-M5` —— 完成单任务端到端验收，状态 `completed`（2026-10-23），本地
  `tests/integration/local/workflow/e2e_*_test.go`（18 个用例）+ 真实远程
  `tests/integration/remote/workflow/e2e_*_test.go`，覆盖成功 / 修复 / 修复上限 / 无法确认
  四类终局。`P1-M4` —— 将修复闭环与代码保存、会话归档、任务收尾串联（核实中修复了
  Developer 会话从未写入数据库、收尾终态写入失败留下 `running` 残留两处生产断点）。
  `P1-M3` —— 有上限、可停止的验证失败修复与重新验证闭环。`P1-M2` —— Developer、
  确定性验证与 Validator Agent 的完整验证决策链。`P1-M1` —— 任务启动至计划生成的前置流程。
- **P2-M5 交付内容**：修复 3 个阻塞级 + 4 个高风险恢复缺陷——收尾写入顺序改为
  "先执行记录、后任务"并用条件写入做栅栏（`applyCloseout`）、启动恢复新增
  `recoverStalledDelivery` 收敛停滞在 `verifying`/`saving` 的任务、新增
  `domain.DecideTaskCloseoutView` 让任务 API 输出 `final_result` / `closeout`
  （`confirmed`/`pending`/`unconfirmed`/`inconsistent`）、新增
  `app.ContainerRecoveryService`（容器 `unknown` 与孤儿容器收敛 / 报告）与
  `app.CodeFetchRecoveryService`（Attempt 级 `code_fetches` 残留收敛）、迁移
  `0023_code_fetch_active_unique.sql`、`VerifyCodeSave` 在容器缺失时降级为
  `unverifiable` 并落盘、新增 HTTP `verify-code-save` 与 `resume-eligibility`
  （CLI `task resume-status`）、`ensureNoPendingContainer` 阻止创建第二个容器。
- **测试基础设施（独立工程质量项，不属于 P4-M3）**：前端测试并发治理已于 2026-10-27 完成，
  交付物为
  [`P4-frontend-test-resource-governance.md`](./P4-frontend-test-resource-governance.md)。
  本轮核实了真实资源模型：Playwright 的 `webServer` 一次运行只启动一个 `vp dev`，全部 worker
  共享同一 `baseURL`，因此 CPU 开销等于「无头浏览器实例数 = worker 数」，限制 worker 即限制
  CPU 峰值，不需要进程级 CPU 配额（最初实施的 Windows Job Object 硬配额方案已验证有效但
  维护成本过高，已全部删除）。落地内容：`playwright.config.ts` 关闭 `fullyParallel` 并把
  `workers` 默认设为 4，`vite.config.ts` 的 `test.maxWorkers` 默认 4，两处共用新增的
  `tests/internal/test-resource-policy.ts`（只接受 1–8 的十进制整数，非法值直接报错、不静默
  回退），`package.json` 新增 `check` / `test:unit:safe` / `test:e2e:safe` / `test:safe` /
  `test:all:safe` 且原有脚本语义不变。真实执行记录：全量单元测试 296 通过（原 282 + 新增 14，
  未减少覆盖）；全量 E2E 110 通过 / 3 失败，`Running 113 tests using 4 workers`、耗时 1.3 分钟
  （单 worker 时为 4.1 分钟），`[WebServer]` 只启动一次；`pnpm check` 与 `pnpm build` 通过。
  3 个失败项已用 `git stash -u` 在未修改的基线上逐个复现，确认为既有问题、非本次引入，
  本次未修复（涉及业务逻辑判断，需单独立项）。本轮**未修改任何前置业务代码与测试断言**，
  也未提供任何 CPU 硬上限——降低 worker 属于软限制，不得表述为 CPU 百分比上限。
- **下一步**：P3 阶段已全部完成（M1–M5 均 `completed`），P4-M1 已完成，P4-M2-1 至
  M2-7 均已完成（见上方"当前阶段与当前里程碑"）。按
  [`ROADMAP.md`](./ROADMAP.md) 的优先级规则，下一步是 **P4-M3 —— 展示阶段进度、验证结果、
  失败原因和恢复状态**，入口见
  [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md)；
  缺口依据见
  [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md)。
  P4-M2-7 的剩余风险见
  [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md) §6：
  同步长请求没有进度推送（M3-7 补）、闭环未通过验证时结构化事实只在 500 错误体之外
  （需后端调整契约才能展示完整结论）、修复周期可用性只能粗判（预算由服务端判定）、
  计划不可覆盖、会话查询为按尝试的 N+1 请求。
  P3-M5 的交接事项见
  [`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md) §8：并发上限是
  "同一份数据库上的名额上限"（跨库全局配额是明确的非目标）；数据库 fencing 不等于
  远程命令已停止；真实远程 `unknown` 现场的"注入"用受控观测端口包裹真实连接，不是
  真的切断网络；前端重试入口仍未实现。
  P3-M3 已落地的前提见
  [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md) §2 与 §3。
  P3-M3 的交接事项：容器归属契约（`DecideContainerOwnership`）与统一加载入口
  （`loadOwnedContainer`）已落地，所有执行入口不再直接用字符串核验容器；
  单步骤三个入口已统一领取任务租约，步骤占用与任务终态写入具备租约栅栏；
  租约栅栏只保护数据库写入，已发出的远程命令的物理收敛由 P3-M4 定义为"只记录现场、
  不自动重放"；
  真实远程依赖用例（`tests/integration/{ssh,pi,remote/**}`）已全部通过；孤儿容器与
  `unknown` 现场的真实远程主动注入验收已在 P3-M5 完成；
  跨库全局并发配额是明确的非目标（P3-M5 §3）。
  P3-M2 的交接事项：并发上限的事实来源是数据库（P3-M5 已明确语义：多个调度器共享
  同一数据库时总量等于单进程上限，跨库全局配额是非目标）；
  服务停止保留的租约依赖 TTL 自然过期（P3-M4 已把该现场落成可追溯记录）；
  轮询扫描（默认 1s）是可接受的取舍。
  P2-M5 的交接事项：真实远程 Docker 上的容器残留与 merge 恢复仍缺证据（需在真实远程
  环境注入 `unknown` / 孤儿现场）；遗留的 `completed` + `recovery_required` 只被报告、
  不被自动修复（`completed` 是终态，若要回退需先审计全部读取方）；`0023` 迁移在已有
  重复活动 `code_fetches` 的库上会失败并有排查说明；孤儿容器不自动补关联。
  P1 遗留的真实缺口是私有仓库 Git 凭据支持
  （`source.GitCredentialProvider` / `codesaver.GitCredentialProvider` 无实现），
  记录在 `ROADMAP.md` 的"尚未实现"。

> 阶段与里程碑的状态可能随开发推进而变化，以上仅为撰写本路线图时的快照；最新状态
> 以 `ROADMAP.md` 的"当前实现状态摘要"、`P1-single-task-e2e.md` 的里程碑表与
> `P2-reliability-recovery.md` 的里程碑清单为准。

## 文档索引

| 文档 | 内容 |
|---|---|
| [`ROADMAP.md`](./ROADMAP.md) | 项目最终业务目标、端到端主流程概览、四阶段总览、推进顺序、现状摘要、暂缓事项 |
| [`P1-single-task-e2e.md`](./P1-single-task-e2e.md) | P1：单任务完整开发闭环（当前优先，最详细） |
| [`P2-reliability-recovery.md`](./P2-reliability-recovery.md) | P2：流程可靠性与恢复 |
| [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md) | P2-M1：全链路状态持久化与恢复边界清单（14 阶段矩阵、启动恢复调用链、缺陷与测试缺口） |
| [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md) | P2-M2：四类中断 × 六层状态的收敛语义矩阵与跨层一致性规则 |
| [`P2-M5-closeout-protocol.md`](./P2-M5-closeout-protocol.md) | P2-M5：任务收尾一致性协议（写入顺序、栅栏、恢复入口、遗留不一致的只读报告） |
| [`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md) | P3：多任务执行与调度 |
| [`P3-M1-task-queue-semantics.md`](./P3-M1-task-queue-semantics.md) | P3-M1：任务队列、领取、调度名额与执行权交接契约（准入/排序/仲裁/fencing/名额，含存储层验收项 S1–S7） |
| [`P3-M2-bounded-concurrency.md`](./P3-M2-bounded-concurrency.md) | P3-M2：任务租约 fencing（`lease_epoch`）、统一所有权、有界并发调度器与生命周期集成 |
| [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md) | P3-M3：容器 / 工作区 / Session / 资源隔离契约、统一执行入口所有权、资源回收所有权校验 |
| [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md) | P3-M4：调度器重启协议、过期租约残留核验、失败任务显式重试入口 |
| [`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md) | P3-M5：多任务并发、冲突与资源回收的系统性验收（缺口矩阵、并发上限语义、4 个生产缺陷修复、真实远程证据） |
| [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md) | P4：产品操作体验与针对性优化（M1–M5 工作项与依赖） |
| [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md) | P4-M1：API 与 Web 业务操作能力缺口盘点（完整路由清单、21 项缺口矩阵与优先级、逐项验收标准、文档与代码冲突清单） |
| [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md) | P4-M2-1：角色初始化接入启动协议与 `GET /api/v1/roles` 契约（调用位置、空表语义、失败处理、响应字段、测试证据、剩余风险） |
| [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md) | P4-M2-2：环境查询 HTTP API 契约（列表与单版本路由、字段语义、错误映射、代码证据、测试证据、剩余风险） |
| [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md) | P4-M2-3：Web 接入统一任务启动与环境选择（调用链、启动结果三态、环境选择行为、测试证据、剩余风险） |
| [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md) | P4-M2-4：任务创建表单接入执行节点与角色选择器（接口契约、选择与校验规则、四态展示、测试证据、剩余风险） |
| [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md) | P4-M2-5：验证失败反馈查询 API 契约（路由、字段语义、完整归属校验、错误映射、最近反馈入口的范围决策、测试证据、剩余风险） |
| [`P4-M2-6-code-save-records-api.md`](./P4-M2-6-code-save-records-api.md) | P4-M2-6：代码保存记录列表 API 契约（路由与 DTO、三阶段与记录可信度语义、应用层依赖边界、错误映射、只读边界、测试证据、剩余风险） |
| [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md) | P4-M2-7：Web 接入计划生成、步骤执行与验证修复操作（8 项操作矩阵与代码证据、实施顺序、前端 API 与操作界面结构、测试证据、未通过项与剩余风险） |
| [`P4-M3-1-attempt-session-history.md`](./P4-M3-1-attempt-session-history.md) | P4-M3-1：Attempt / Session 历史展示补齐（后端 DTO 逐字段核实、尝试超时与取消独立布尔事实、错误分类与失败阶段标签来源、归档 / 导出 / 执行状态三张独立映射表与未知值策略、测试证据、E2E 基线对照、剩余风险） |
| [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md) | P4-M3-3：任务最终结论与收尾闭环展示（真实 DTO 契约、M3-1 / M3-2 核验结论、三行独立事实与警示、测试证据、未执行项与剩余风险） |
| [`P4-M3-5-code-save-status-display.md`](./P4-M3-5-code-save-status-display.md) | P4-M3-5：代码保存三阶段状态展示（后端 DTO 字段契约、前端类型与查询封装、五组独立事实与警示、测试证据、未执行项与剩余风险） |
| [`P4-M3-4-resume-eligibility-display.md`](./P4-M3-4-resume-eligibility-display.md) | P4-M3-4：续跑资格与阻塞原因展示（后端查询契约与阻塞分类来源、前端类型与查询封装、资格结论与阻塞项展示规则、加载失败与竞态处理、测试证据、退出条件检查、剩余风险） |
| [`P4-frontend-test-resource-governance.md`](./P4-frontend-test-resource-governance.md) | P4 工程质量：前端测试并发治理（真实资源模型核实、Playwright worker 4 / Vitest worker 4 默认值、非法输入拒绝、资源受限入口、能力边界、验收命令与既有失败隔离） |

## 下一步工作的选择规则

按以下优先级选取尚未完成的里程碑（顺序即权重，不可因为"更容易"而倒置）：

1. 阻塞完整端到端流程的缺失能力。
2. 当前主流程必须具备的基础正确性与必要测试。
3. 打通阶段内剩余的业务环节。
4. 阶段验收和端到端验证。
5. 已经影响流程可靠性的恢复与安全问题。
6. 后续阶段的扩展能力。
7. 不阻塞流程的局部优化与重构。

在一个里程碑内部有多个工作项时，优先选择**能解除最多后续依赖、让真实业务流程向前
推进**的工作项，而不是代码量最小、最容易测试或最容易抽象的工作项。

## 每次开发前

1. 读取本文件、`ROADMAP.md` 与当前阶段文档。
2. 检查最新代码、工作区状态与相关测试，不能只依赖上次结论。
3. 确认当前阶段、当前里程碑、前置条件与尚未解决的问题。
4. 判断该工作是推进当前主流程，还是仅属于可延期优化。
5. 将本次计划限定在当前里程碑范围内。
6. 发现路线图与代码事实不一致时，先更新规划状态和证据，再确定实施方案。

## 每次开发中

1. 不擅自扩大工作范围。
2. 发现当前设计无法支撑端到端流程时，记录具体阻塞及证据。
3. 只有确实影响主流程或基础正确性时，才调整里程碑依赖。
4. 不把临时解决方案默认为永久架构。
5. 不因局部优化而偏离当前阶段目标。

## 每次开发后

1. 根据实际代码差异更新当前里程碑状态。
2. 记录实际修改的文件、功能结果、测试命令与真实测试结果。
3. 明确区分通过、失败、未执行和无法确认的验证结果。
4. 更新已完成工作、剩余工作、阻塞项、风险与后续任务。
5. 只有满足完成定义且证据充分时，才把里程碑标记为 `completed`。
6. 只完成部分就保持 `in_progress`，明确剩余条件。
7. 实现过程中发现路线图有误时，更新路线图并说明修改理由。
8. 更新本文件中的当前阶段与下一步入口，确保后来者不需要依赖聊天历史。
