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
- **当前里程碑**：`P4-M4` —— 支持必要的继续执行、取消、恢复和结果查看操作，状态
  `completed`（M4-1、M4-2、M4-3、M4-4、M4-5 全部完成）。**P4-M4 的退出条件已重新核对**：
  `failed` / `blocked` / `recovery_required` 三类现场都能在 Web 上被理解并处置（M4-1 重试、
  M4-2 核验、M4-3 容器处置、M4-4 恢复、M4-5 单步诊断共同覆盖），且所有有副作用的操作都需要
  用户显式触发、不自动重放。**P4 整体仍不是 `completed`**：`P4-M5`（基于实测数据的优化）仍为
  `pending`（第一轮测量与决策评估已于 2026-10-28 完成，见
  [`P4-M5-measurement-and-optimization-assessment.md`](./P4-M5-measurement-and-optimization-assessment.md)），
  其余 P4 子任务均已闭合；**G12（项目后端能力缺失）已于 2026-10-28 完成**（见
  [`P4-G12-project-management.md`](./P4-G12-project-management.md)），它是 P4-M1 盘点中最后一项
  未闭合的业务缺口，完成后 P4 只剩 `P4-M5`。子任务 **M4-1（失败任务
  [`P4-M4-1-task-retry-web-entry.md`](./P4-M4-1-task-retry-web-entry.md)：前端补齐
  `RetryEligibilityResponse` / `RetryTaskResponse` / `RetryTaskRequest` 类型与
  `fetchTaskRetryEligibility` / `retryTask`（`GET|POST /api/v1/tasks/{taskID}/retry[-eligibility]`），
  任务详情新增独立面板 `RetryTaskPanel`；可用性只来自服务端 `can_retry`（不出现任何状态
  白名单），阻塞分类逐项展示且未知取值原样保留，查询失败不启用操作、刷新失败保留上一次真实
  快照、切换任务丢弃迟到响应；结果按 `workflow_started` 区分"已启动工作流"与"任务已重开但
  工作流没有启动"，请求失败如实展示服务端错误，超时 / 网络中断提示结果无法确认且**不自动
  重新提交**。**本轮未修改任何 Go 代码，未改动重试领域规则，也未新增第二套工作流**；新增前端
  单元测试 34 个与 E2E 10 个，`pnpm test:unit:safe` 443 通过，`pnpm test:e2e:safe` 155 通过 /
  0 失败，`pnpm check` 与 `pnpm build` 通过。
- 子任务 **M4-2（代码保存核验 Web 操作入口）已于 2026-10-27 完成**，交付物为
  [`P4-M4-2-code-save-verification-web-action.md`](./P4-M4-2-code-save-verification-web-action.md)：
  前端补齐 `CodeSaveVerifyRequest` / `CodeSaveObservation` / `CodeSaveDecision` /
  `CodeSaveRecordView` / `CodeSaveVerifyResponse` 类型与 `verifyCodeSave`
  （`POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save`），代码保存面板为每条
  记录新增显式核验入口（按真实 `record.id` 发起，请求体固定为 `{}`），核验结果按"现场观察 /
  核验判定 / 核验后记录 / 结果持久化"四块分别展示：`read_failed`、`decision.status` + `stage` +
  `summary` + `safe_to_retry`、`record` 与 `updated` 的逐维度前后对照、`persisted` 互不替代；
  `persisted=true` 不被显示为核验通过，`persisted=false` 时仍保留响应中真实的观察与判定；
  未知状态与缺失字段均有中性展示，缺失 commit 不会被读成"没有变化"。核验只由用户点击触发，
  同一记录在途时拒绝重复提交，不同记录状态互不影响，切换任务或记录后丢弃迟到响应，请求失败与
  结果不确定分别提示且**不自动重发**，也不进入 5 秒自动刷新循环。**本轮未修改任何 Go 代码，
  未改动核验领域规则**；新增前端单元测试 42 个与 E2E 10 个，`pnpm test:unit:safe` 485 通过，
  `pnpm test:e2e:safe` 165 通过 / 0 失败，`pnpm check` 与 `pnpm build` 通过。
- 子任务 **M4-3（容器 `delete`/`detach` 的 HTTP API 与 Web 操作入口）已于 2026-10-27 完成**，
  交付物为
  [`P4-M4-3-container-delete-detach-web-actions.md`](./P4-M4-3-container-delete-detach-web-actions.md)：
  G9 的 HTTP 入口与 Web 操作**完全不存在**，本轮补齐三条路由
  （`GET /api/v1/tasks/{taskID}/containers`、
  `POST .../containers/{containerID}/delete`、`POST .../containers/{containerID}/detach`）与
  任务详情中的容器面板。**本轮修改了 Go 生产代码**：新增纯函数
  `domain.DecideContainerDisposal`（处置校验与归属校验是两个不同问题：处置允许历史代次，
  但仍拒绝已脱离管理与已删除后的重复 detach）与 `app.TaskContainerDisposalService`
  （复用 `ContainerService` 的远程所有权核验，不在 Handler 拼 Docker 命令，也不新开远程连接
  路径）。两个动作在路径、请求、响应 `action` 与确认文案上完全分开，界面上没有"清理"按钮；
  删除请求不接受请求体、原因固定为 `manual`，响应不泄露 `docker_id` / `image_id` /
  `host_id` / 工作区与凭据；跨任务与跨节点处置返回 409，记录不存在 404，远程操作失败或结果
  不确定 500 `execution_failed`，从不返回 2xx 伪装成功。前端新增 `TaskContainer` /
  `ContainerDisposalResponse` 等类型、`containers.ts` 纯函数模块、`useTaskContainers` 唯一
  状态 Hook 与 `TaskContainerPanel`；未知状态与未知关联方式原样展示，已脱离管理的容器两个动作
  都禁用，同一容器在途防重、切换任务丢弃迟到响应、刷新失败保留上一次真实结论、
  结果不确定明确提示且不自动重发，处置成功后**不**重试任务、**不**启动工作流。新增 Go 测试
  25 个（domain 9 + httpapi 16）、前端单元测试 37 个与 E2E 18 个；`go test ./tests/unit/...
  ./tests/integration/local/...` 2239 通过、`go test ./tests/e2e/...` 58 通过、
  `pnpm test:unit:safe` 522 通过、`pnpm test:e2e:safe` 183 通过 / 0 失败，`gofmt`、`go vet`、
  `pnpm check` 与 `pnpm build` 通过。
- 子任务 **M4-4（基于服务端续跑资格的工作流恢复操作）已于 2026-10-27 完成**，交付物为
  [`P4-M4-4-resume-eligibility-web-action.md`](./P4-M4-4-resume-eligibility-web-action.md)：
  核实出恢复入口与服务端资格**完全脱钩**——进度条里的「恢复工作流」读的是本地白名单
  `canResumeWorkflow(workflow.run)`（`blocked` / `recovery_required` / `pending`），
  因此在资格查询中、查询失败、快照过期或 `can_resume: false` 时按钮依然可用，与同一页
  上写着"存在阻塞"的资格面板互相矛盾。本轮把恢复入口**唯一化**到 `ResumeEligibilityPanel`
  内：删除进度条按钮、`canResumeWorkflow` 与 `TasksPage.handleResumeWorkflow`，扩展既有
  `useResumeEligibility` 承载恢复操作（未新增第二个 Hook、未新增第二个刷新循环）。可提交性
  由纯函数 `resumeSubmitBlockReason` 决定，判定顺序为"在途 → 查询中 → 查询失败 → 无快照 →
  快照不属于当前任务 → 刷新失败 → `can_resume` 为假"，全部命中即禁用，**不读取任何任务或
  工作流状态**；恢复请求只发送资格快照里的 `run_id`，结果按 `running` / `pending` / 其他三档
  如实展示并明确"请求受理 ≠ 工作流成功 ≠ 任务完成"，服务端拒绝、超时中断、刷新失败分别提示
  且**不自动重发**。**本轮未修改任何 Go 生产代码**：核实到恢复接口已在应用层重新执行租约、
  计划摘要、`CheckWorkflowStart`、容器、执行节点与代码保存副作用的完整校验，`can_resume`
  只是查询时刻快照而非执行授权。新增前端单元测试 25 个与 E2E 14 个；`pnpm test:unit:safe`
  547 通过、`pnpm test:e2e:safe` 197 通过 / 0 失败，`pnpm check` 与 `pnpm build` 通过。
- 子任务 **M4-5（单步诊断动作：初始化代码、确定性验证）已于 2026-10-27 完成**，交付物为
  [`P4-M4-5-single-step-diagnostic-actions.md`](./P4-M4-5-single-step-diagnostic-actions.md)：
  核实出 M2-7 已交付"动作可用"（入口、路径、按钮准入、在途防重均已具备），M4-5 的真实缺口
  在**结果语义与竞态边界**——`describeValidateResult` 用 `passed` 出二元结论，导致
  `inconclusive`/未知判定被显示为"验证未通过"、判定缺失被显示为"验证通过"；响应已提供的
  `checks[]` 前端零读取；`runOperation` 无任务归属判定，切换任务后迟到响应会写进新任务界面
  并触发属于旧任务的刷新；`timeout`/`network`/`aborted` 与 4xx 业务拒绝被压成同一种"失败"
  （同页面其它 4 个操作已区分，唯独执行操作 hook 遗漏）。本轮**只改前端 3 个生产文件与 2 个
  测试文件**：验证结论改由 `verdict` 唯一决定并逐项输出检查事实（含 `certain=false` 不得
  当作代码问题），`describeInitializeResult` 补齐缺失分支占位与 `uncertain` 核验提示，
  `runOperation` 增加"票据 + activeTaskRef + mountedRef"三重判定并把结果不确定单独表达，
  结果面板新增独立的"操作结果无法确认"提示块。**未修改任何 Go 代码与 HTTP 契约，未新增
  接口、Hook 或刷新循环**。新增前端单元测试 10 个与 E2E 8 个；`pnpm test:unit:safe` 557 通过、
  `pnpm test:e2e:safe` 205 通过 / 0 失败，`pnpm check` 与 `pnpm build` 通过。
- **上一里程碑**：`P4-M3` —— 展示阶段进度、验证结果、失败原因和恢复状态，状态
  `completed`（M3-1、M3-3、M3-4、M3-5、M3-6、M3-7、M3-8 已完成，M3-2 已由 M2-7 满足）。
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
  `completed`（全部子项已完成）。2026-10-26 按当前代码核实并实施子任务 **M3-3（任务最终
  结论与收尾闭环展示）**，交付物为
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
  `git stash -u` 基线对照确认为既有问题）。至此 **M3-1 已完成**，字段缺口全部闭合。
  同轮实施子任务 **M3-7（任务执行进度自动刷新）**，交付物为
  [`P4-M3-7-progress-auto-refresh.md`](./P4-M3-7-progress-auto-refresh.md)：先按当前代码核实
  活动状态判定依据（`domain.WorkflowRunStatus.IsActive()` 只含 `running` /
  `cancel_requested`，`terminal` 只含 `completed` / `cancelled` / `failed`，`blocked` 与
  `recovery_required` 两者都不是、需人工处置；`TaskStatus.IsActive()` 与前端
  `TASK_STATUS_META.active` 取值集合一致），再实现唯一自动刷新入口
  `src/pages/tasks/useTaskLiveSnapshot.ts` + `autoRefresh.ts`：5 秒周期、递归 `setTimeout`
  串行调度、上一轮结算后才排下一轮、`pending` 与"已进入执行阶段无执行记录"只在 12 轮启动
  窗口内刷新、页面隐藏暂停 / 恢复前台立即刷新、关闭详情与卸载清理定时器与监听、请求票据按
  「序号 + 任务标识」丢弃迟到响应、刷新失败保留上一次成功快照且**不改变策略**（因此不会被
  误认为工作流终止）。步骤 / 代码保存 / 续跑资格三个子面板不各自建定时器，只在服务端真实
  字段变化时重载；续跑资格因需核验远程容器，只在获得停止刷新结论时刷新一次。修复实施中
  发现的两个真实缺陷：手动刷新清掉定时器后未接回节奏（会让自动刷新悄悄停掉）、详情首次
  加载失败时仍渲染空详情框。**本轮未修改后端代码，未新增或修改任何 HTTP API**；新增前端
  单元测试 35 个（`tests/unit/task-progress-auto-refresh.test.ts`）与 E2E 11 个
  （`tests/e2e/task-progress-auto-refresh.spec.ts`），全量单元测试 361 通过，全量 E2E
  129 通过 / 0 失败（其中 2 个既有失败 `layout.spec.ts:43` 与 `:64` 已按用户决定移除正文
  断言并记录待恢复项，见交付文档 §7.2）。至此 **M3-7 已完成**。
  同轮实施子任务 **M3-6（工作流步骤起止时间补全）**，交付物为
  [`P4-M3-6-workflow-step-timestamps.md`](./P4-M3-6-workflow-step-timestamps.md)：先按当前
  代码逐层核实，确认真实缺口是**三处**而不是只有 G18 描述的一处——① 领域时间语义缺陷
  （`Step.Transition` 只在 `IsTerminal()` 写 `finished_at`，因此 `failed` /
  `recovery_required` / `retry_wait` 永远没有结束时间，重新执行也不清空上一轮的结束时间）；
  ② `httpapi.workflowStatusResponse` 从不映射 `started_at` / `finished_at`；
  ③ 前端 `StepListPanel` 用 `updated_at` 冒充时间、从不读取真实起止时间。据此在
  `internal/domain/step.go` 新增 `StepStatus.BeginsExecution()` / `EndsExecution()` 并以它们
  重写 `Transition` 的时间写入（`started_at` 只写首次真正执行、重新执行清空 `finished_at`、
  六种"周期结束"状态都写 `finished_at`），在 `internal/httpapi/workflow.go` 补齐 DTO 映射
  与 `workflowTimeString`，前端新增纯函数模块 `src/pages/tasks/stepTimes.ts` 并改写步骤行
  展示。**未新增表、未新增迁移脚本、未新增接口、未改动工作流成功判定**（`steps.started_at` /
  `finished_at` 自 `0001_initial.sql` 起即存在且读写完整）。新增 Go 测试 28 个（领域 7、存储 10、
  HTTP 6、工作流集成 5）与前端单元测试 15 个、E2E 7 个；`go test ./tests/...` 2262 通过（46
  包，退出码 0），`pnpm test:unit:safe` 376 通过，`pnpm test:e2e:safe` 136 通过，
  `pnpm check` 与 `pnpm build` 通过。至此 **M3-6 已完成**。
- **M3-8（事件时间线与修复周期历史，2026-10-27 完成）**：先核实出两处真实缺口——①
  `storage.EventStore.ListEventsByTask` 生产代码**零调用者**（只有测试调用），且排序只有
  `ORDER BY id`（不是 `(created_at, id)`，补写的更早事件会排在时间线末尾），`EventFilter`
  缺偏移、类型筛选与总数统计；②`RepairCycleStore.ListRepairCyclesByStep` 有存储能力但
  **没有任何 HTTP 只读入口**，前端只有写入口 `openRepairCycle`。据此新增
  `GET /api/v1/tasks/{taskID}/events`（`type` / `size` / `offset`，排序固定
  `(created_at, id)`，`total` 来自独立 `CountEventsByTask`）与
  `GET /api/v1/tasks/{taskID}/steps/{stepID}/repair-cycles`，应用层由
  `app.HistoryQueryService` 承担归属校验（先任务、后步骤、再比对 `step.TaskID`）。
  **事件 payload 不直接输出**：逐写入点核实 21 处 payload 后发现含 `work_dir`、
  `repository`、`container_id`、`error_summary` 等不可对外字段，因此改为 8 字段白名单摘要
  并经 `domain.ClipFeedbackText` 脱敏截断；未知类型保留原始 `event_type` 与
  `type_known=false`。前端新增 `EventTimelinePanel`（任务维度，独立于步骤选择，不进入
  5 秒刷新循环）与 `RepairCyclePanel`（步骤维度），`closed_at` 缺失一律显示"尚未记录结束
  时间"。**未新增表、未新增迁移、未改动工作流执行与成功判定**。新增 Go 测试 30 个、前端
  单元测试 33 个、E2E 9 个；`go test ./tests/unit/...` 1751 通过，`go test
  ./tests/integration/local/...` 463 通过，`pnpm test:unit:safe` 409 通过，
  `pnpm test:e2e:safe` 145 通过 / 0 失败，`pnpm check` 与 `pnpm build` 通过。
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
- **下一步**：`P4-M4`（支持必要的继续执行、取消、恢复和结果查看操作）状态 `in_progress`。
  **M4-1（失败任务显式重试 Web 入口，2026-10-27 完成）**：前端接入
  `GET|POST /api/v1/tasks/{taskID}/retry[-eligibility]`，可用性只来自服务端 `can_retry`，
  阻塞分类逐项展示、未知取值原样保留，`workflow_started=false` 不被显示为已启动，请求失败
  与结果不确定分别提示且不自动重发；**未修改任何 Go 代码、重试领域规则与工作流编排**。见
  [`P4-M4-1-task-retry-web-entry.md`](./P4-M4-1-task-retry-web-entry.md)。
  **M4-2（代码保存核验 Web 操作入口，2026-10-27 完成）**：前端接入
  `POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save`，每条记录按真实
  `record.id` 提供显式核验入口（请求体固定为 `{}`，容器与工作区由服务端确定），结果按现场
  观察 / 核验判定 / 核验后记录 / 结果持久化四块分别展示，`read_failed`、`decision.*` 与
  `persisted` 互不替代，`persisted=false` 时仍保留真实判定，请求失败与结果不确定分别提示且
  不自动重发；**未修改任何 Go 代码与核验领域规则**。见
  [`P4-M4-2-code-save-verification-web-action.md`](./P4-M4-2-code-save-verification-web-action.md)。
  **M4-3（容器 `delete` / `detach` 的 HTTP API 与 Web 操作入口，2026-10-27 完成）**：G9 的
  HTTP 入口**完全不存在**，本轮补齐
  `GET /api/v1/tasks/{taskID}/containers`（只读登记记录，含 `origin` 区分当前/历史代次）与
  `POST .../containers/{containerID}/{delete|detach}`（两个独立动作、无请求体、不接受远程参数），
  并在任务详情新增 `TaskContainerPanel`。删除与脱离管理在路径、响应 `action`、确认文案与后果
  说明上完全分开；跨任务/跨节点处置 409，记录不存在 404，远程失败或结果不确定 500
  `execution_failed`，从不返回 2xx 伪装成功；已脱离管理的容器两个动作都禁用（终态只能人工处理），
  处置成功后**不**重试任务、**不**启动工作流。**本轮修改了 Go 生产代码**（新增
  `domain.DecideContainerDisposal` 与 `app.TaskContainerDisposalService`，复用既有
  `ContainerService` 的所有权核验与远程会话路径），未改动容器状态机、生命周期策略与工作流编排。
  见 [`P4-M4-3-container-delete-detach-web-actions.md`](./P4-M4-3-container-delete-detach-web-actions.md)。
  **M4-4（基于服务端续跑资格的工作流恢复操作，2026-10-27 完成）**：核实出恢复入口与资格
  **完全脱钩**——进度条里的按钮读本地白名单 `canResumeWorkflow(workflow.run)`，在资格查询中、
  查询失败、快照过期或 `can_resume=false` 时仍可点击。本轮把恢复入口唯一化到
  `ResumeEligibilityPanel` 内，删除进度条按钮、`canResumeWorkflow` 与
  `TasksPage.handleResumeWorkflow`，扩展既有 `useResumeEligibility` 承载恢复操作；可提交性由
  纯函数 `resumeSubmitBlockReason` 判定（在途 / 查询中 / 查询失败 / 无快照 / 快照不属于当前任务 /
  刷新失败 / `can_resume` 为假），**不读取任何任务或工作流状态**；请求只发送资格快照里的
  `run_id`，结果按 `running` / `pending` / 其他三档展示并明确"受理 ≠ 成功 ≠ 完成"，拒绝、
  超时中断与刷新失败分别提示且不自动重发。**未修改任何 Go 生产代码**：恢复接口已在应用层
  重新核验租约、计划摘要、`CheckWorkflowStart`、容器、执行节点与代码保存副作用。见
  [`P4-M4-4-resume-eligibility-web-action.md`](./P4-M4-4-resume-eligibility-web-action.md)。
  **M4-5（单步诊断动作：初始化代码、确定性验证，2026-10-27 完成）**：核实 M2-7 已交付
  "动作可用"，真实缺口在结果语义与竞态边界——验证结论被 `passed` 二元化（`inconclusive` 与
  未知判定显示为"验证未通过"、判定缺失显示为"验证通过"）、响应已提供的 `checks[]` 未被读取、
  切换任务后迟到响应会污染新任务界面并触发属于旧任务的刷新、`timeout`/`network`/`aborted`
  与 4xx 业务拒绝被压成同一种"失败"。本轮只改前端：验证结论改由 `verdict` 唯一决定并逐项
  输出检查事实，初始化结果补齐缺失分支占位与 `uncertain` 核验提示，`runOperation` 增加票据 +
  任务归属 + 卸载三重判定并把结果不确定单独表达。**未修改任何 Go 代码与 HTTP 契约，未新增
  接口、Hook 或刷新循环**。见
  [`P4-M4-5-single-step-diagnostic-actions.md`](./P4-M4-5-single-step-diagnostic-actions.md)。
  环境构建 Web 入口与角色变更入口是否上 Web 仍按产品取舍决定。详见
  [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md)。
- **P4-M3 收尾时的核实记录**（M4 开始前）：`P4-M3`（展示阶段进度、验证结果、失败原因和
  恢复状态）已全部完成；2026-10-26 的核实结论：**M3-3 已完成**（任务最终结论与收尾闭环展示，见
  [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md)）；**M3-5 已完成**
  （代码保存三阶段状态展示，见
  [`P4-M3-5-code-save-status-display.md`](./P4-M3-5-code-save-status-display.md)）；**M3-2 已由
  P4-M2-7 满足**（验证历史、逐项检查与失败反馈视图齐备，无需重复实现）；**M3-1 已完成**
  （Attempt / Session 历史展示补齐，尝试的 `timed_out` / `cancelled` / `error_class` /
  `failure_stage` 与会话归档 / 导出状态的中文标签全部闭合，见
  [`P4-M3-1-attempt-session-history.md`](./P4-M3-1-attempt-session-history.md)）；**M3-4 已完成**
  （续跑资格与阻塞原因展示，见
  [`P4-M3-4-resume-eligibility-display.md`](./P4-M3-4-resume-eligibility-display.md)）；**M3-7 已完成**
  （任务执行进度自动刷新，见
  [`P4-M3-7-progress-auto-refresh.md`](./P4-M3-7-progress-auto-refresh.md)）。
  **M3-6 已完成**（工作流步骤起止时间，见
  [`P4-M3-6-workflow-step-timestamps.md`](./P4-M3-6-workflow-step-timestamps.md)）；
  **M3-8 已完成**（事件时间线与修复周期历史，见
  [`P4-M3-8-event-timeline-repair-cycle-history.md`](./P4-M3-8-event-timeline-repair-cycle-history.md)）。
  至此 **P4-M3 全部子项完成**。
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
- **P4-M5 第一轮核对已完成（2026-10-28，基线 `origin/master` `cf54693`）**：交付物
  [`P4-M5-measurement-and-optimization-assessment.md`](./P4-M5-measurement-and-optimization-assessment.md)。
  本轮先核实观测能力、再建立可复核基线，**未修改任何生产代码 / 前端代码 / 数据库结构 / 测试**：
  - **观测能力核实**：没有访问日志、请求耗时、指标、追踪、pprof 或 `/metrics`；`/api/v1/health`
    只覆盖 `http_api` 与 `database`；`TaskScheduler.Status` 有定义但**零生产调用者**，且
    `Active` 只统计本进程已登记的分派，**不等于**数据库名额占用（真实占用来源是
    `countOccupiedSlots`），直接对外暴露会误导。
  - **候选 A（G17 调度名额可见性）→ 需要继续测量**：无"用户遇到名额等待"的现场，也无队列
    长度/等待时间样本；最小成本测量方式是**只读 SQL**（`tasks.status`、`events` 的
    `task.started` 与 `tasks.created_at`），不需要新接口。
  - **候选 B（任务列表分页）→ 暂缓**：分页是真实的服务端分页（`LIMIT/OFFSET` + 独立
    `COUNT(*)`、默认 20 / 上限 100、超限 400、`ORDER BY created_at DESC, rowid DESC`），
    前端只在页/筛选变化与 4 处显式动作后重载。实测首屏 `size=20` 中位数：1 000 任务 2.8 ms、
    10 000 任务 9.1 ms、50 000 任务 39.0 ms（深偏移 175–251 ms）。现实规模下无可感知问题。
  - **候选 C（本轮新增）→ 需要继续测量**：调度扫描每 1 秒遍历全部非终态任务与全部排队任务
    （量级 `1 + 非终态数 + 1 + 4 × 排队数` 次查询/秒）。实测 50 000 任务库：3 000 个历史
    `failed` → 13.1% 单核空闲占用，402 个排队 → 8.4%；但真实库的 `tasks.status` 分布未知，
    且同等后台负载下任务列表延迟未劣化。
  - **结论**：三个候选都缺"真实使用数据"，P4-M5 **保持 `pending`**；解除待定所需数据与
    下一步动作记录在交付物 §8。
- **G12 项目管理能力已于 2026-10-28 完成（基线 `origin/master` `2cb21ca`）**：交付物为
  [`P4-G12-project-management.md`](./P4-G12-project-management.md)。本轮从"恢复常规功能开发"
  出发，先用代码证据核实 G12 的真实缺口而不是照抄缺口摘要：`domain.ProjectConfig` 与
  `ValidateProjectID` 存在但**零生产调用者**，没有 `projects` 表、store、app 服务、CLI 与
  HTTP；`task.Config.ProjectID` 只是一个受书写规则校验的**自由文本**，它的真实作用是**项目级
  缓存卷的共享范围键**与**远程管理工作目录名**，因此拼错一个字就会让同一项目的任务落在不同
  缓存卷与目录里，而且系统无法自行发现这种分裂。据此把 G12 定义为"项目从自由文本升级为受校验
  的登记记录，并成为任务的必填关联对象"。落地内容：迁移 `0025_projects.sql`、领域实体
  `domain.Project`（复用 `ProjectConfig` 作为环境配置部分，新增仓库地址 / 基础分支 / 逻辑删除）、
  `storage.ProjectStore` 与 sqlite 实现、`app.ProjectService`（创建 / 整体更新 / 逻辑删除 /
  查询，错误语义 `ErrProjectInvalidInput` / `ErrProjectNotFound` / `ErrProjectStateConflict`）、
  四条 HTTP 路由（`GET|POST /api/v1/projects`、`GET|PUT|DELETE /api/v1/projects/{projectID}`）、
  CLI `agentflow project add|list|show|update|remove`、前端项目页（真实 `ExTable` + 新增/修改
  弹窗 + 逻辑删除确认写明后果）与创建任务弹窗的项目选择器（从自由文本输入改为真实项目下拉，
  并按项目预填仓库 / 分支 / 工作目录 / 环境与可用版本）。任务创建改为**必须**关联项目：项目
  标识为空、书写非法、不存在或已逻辑删除一律拒绝；未给出的执行配置从项目继承，显式给出的值
  优先。**P4-M5 不受影响，仍为 `pending`**，本轮没有把它标记为完成。
- **下一步**：P3 阶段已全部完成（M1–M5 均 `completed`），P4-M1、M2-1…M2-7、M3、M4 与 G12
  均已完成（见上方"当前阶段与当前里程碑"）。P4 只剩 `P4-M5`，而它**缺少真实使用数据**，因此
  不再继续推进数据依赖型优化（解除待定所需数据见
  [`P4-M5-measurement-and-optimization-assessment.md`](./P4-M5-measurement-and-optimization-assessment.md) §8）。
  后续入口按 [`ROADMAP.md`](./ROADMAP.md) 的"下一步工作的选择规则"选择**有明确代码证据的常规
  功能缺口**；`P4-M1` 盘点的 P0/P1/P2/P3 缺口已全部闭合，新的候选需要重新盘点而不是照抄旧清单。
  P4-M2-7 的剩余风险见
  [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md) §6：
  同步长请求没有**流式**进度推送（轮询已由 M3-7 交付，SSE 仍未实现）、闭环未通过验证时
  结构化事实只在 500 错误体之外
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
| [`P4-M3-6-workflow-step-timestamps.md`](./P4-M3-6-workflow-step-timestamps.md) | P4-M3-6：工作流步骤起止时间补全（领域时间语义、DTO 映射、步骤列表时间展示，未新增表/迁移/接口） |
| [`P4-M3-7-progress-auto-refresh.md`](./P4-M3-7-progress-auto-refresh.md) | P4-M3-7：任务执行进度自动刷新（活动状态判定的领域证据、5 秒周期与串行调度、启动窗口、后台暂停与恢复、请求票据与迟到响应、子面板按字段变化重载的成本取舍、测试证据、基线失败对照、剩余风险） |
| [`P4-M3-8-event-timeline-repair-cycle-history.md`](./P4-M3-8-event-timeline-repair-cycle-history.md) | P4-M3-8：事件时间线与修复周期历史（事件模型与 payload 安全核查、排序与分页契约、归属校验、白名单摘要、前端两面板的刷新与竞态策略、测试证据、剩余风险） |
| [`P4-M4-1-task-retry-web-entry.md`](./P4-M4-1-task-retry-web-entry.md) | P4-M4-1：失败任务显式重试 Web 入口（真实路由与 DTO 契约、资格唯一判据 `can_retry`、10 个阻塞分类、同步请求与超时语义、操作防重与结果语义、竞态处理、测试证据、剩余风险） |
| [`P4-M4-2-code-save-verification-web-action.md`](./P4-M4-2-code-save-verification-web-action.md) | P4-M4-2：代码保存核验 Web 操作入口（真实路由与双 DTO 差异、请求参数可信来源与"不猜测容器/工作区"依据、五组事实互不替代、`persisted` 与 `safe_to_retry` 语义、竞态与防重、测试证据、剩余风险） |
| [`P4-M4-3-container-delete-detach-web-actions.md`](./P4-M4-3-container-delete-detach-web-actions.md) | P4-M4-3：容器 delete / detach 的 HTTP API 与 Web 操作入口（HTTP 缺口核实、三条新路由与 DTO 契约、处置校验与归属校验的区别、两层所有权核验、delete 与 detach 的边界、结果不确定语义、竞态与防重、测试证据、剩余风险） |
| [`P4-M4-4-resume-eligibility-web-action.md`](./P4-M4-4-resume-eligibility-web-action.md) | P4-M4-4：基于服务端续跑资格的工作流恢复操作（实施前资格/入口/服务端前置条件的代码证据、恢复入口脱钩的四个真实缺口、判定顺序与"不读取任务状态"依据、受理 ≠ 成功 ≠ 完成的语义拆分、任务切换与刷新失败边界、测试证据、剩余风险） |
| [`P4-M4-5-single-step-diagnostic-actions.md`](./P4-M4-5-single-step-diagnostic-actions.md) | P4-M4-5：单步诊断动作（初始化代码、确定性验证）（M2-7 已交付能力与 M4-5 剩余目标的边界、5 处真实缺口的代码证据、判定唯一决定结论的依据、逐项检查事实、结果不确定与服务端拒绝的区分、任务切换的票据判定、未改 Go 代码的理由、测试证据、剩余风险） |
| [`P4-frontend-test-resource-governance.md`](./P4-frontend-test-resource-governance.md) | P4 工程质量：前端测试并发治理（真实资源模型核实、Playwright worker 4 / Vitest worker 4 默认值、非法输入拒绝、资源受限入口、能力边界、验收命令与既有失败隔离） |
| [`P4-M5-measurement-and-optimization-assessment.md`](./P4-M5-measurement-and-optimization-assessment.md) | P4-M5 第一轮：基于真实数据的性能与架构优化评估（观测能力核实、证据分级、可复现测量基线与原始数据、候选 A/B/C 的证据与立项决策、测量局限、解除待定所需数据、实际命令与结果） |
| [`P4-G12-project-management.md`](./P4-G12-project-management.md) | P4-G12：项目管理能力（原始缺口与代码核实、项目与任务 / 缓存 / 远程目录的关系、数据模型与迁移、领域 / 存储 / 应用 / HTTP / CLI / 前端完整调用链、任务必填关联与继承规则、测试证据、剩余风险） |

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
