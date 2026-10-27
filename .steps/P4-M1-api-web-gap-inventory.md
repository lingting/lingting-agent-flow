# P4-M1：API 与 Web 业务操作能力缺口盘点

> 本文档是 P4 后续里程碑（M2 / M3 / M4）的实施依据。它只记录**经代码核实的事实**，
> 不沿用历史文档的状态结论，也不把"接口文件存在"等同于"能力可用"。

## 1. 核对口径

### 1.1 核对时的代码版本

| 项 | 值 |
|---|---|
| 分支 | `master` |
| HEAD | `70f82b1`（`:zap: 系统性验收多任务并发、冲突与资源回收`，2026-10-26 01:18:58 +0800） |
| 工作区 | 干净（`git status --short` 无输出） |
| 后端 | Go（`internal/httpapi`、`internal/app`、`internal/domain`、`internal/storage`） |
| 前端 | `lingting-agent-flow-ui/`（Vite+ / React / `lingting-react-ui`） |

### 1.2 判定口径

- **后端业务能力**：只看 `internal/app`、`internal/domain`、`internal/storage` 是否存在真实可调用的
  服务方法、领域规则与持久化能力。存在接口类型但没有生产调用者，判为"部分实现"。
- **HTTP API**：只看 `internal/httpapi` 是否注册了真实路由并返回真实数据。注释里写到的路径不算。
- **前端客户端**：只看 `lingting-agent-flow-ui/src/lib/api/endpoints.ts` 与 `types.ts` 是否封装了
  对应请求与类型。
- **Web 页面**：只看 `src/pages/` 下是否存在真实交互入口，占位页（`PagePlaceholder`）判为"缺失"。
- **业务阻塞**：判断标准是"用户能否仅通过 Web 完成该业务动作"，而不是"有没有一个按钮"。

### 1.3 本次核对的检查方式

- 阅读代码、`grep` 路由注册与调用链、核对 `internal/httpapi/router.go` 的分发顺序。
- 未运行任何测试（本次未获测试授权）。全部结论均为静态代码核实结果，第 9 节如实记录。

## 2. 现状快照（一句话结论）

后端执行链路已经相当完整：统一任务启动、计划生成、单步骤执行、顺序执行、开发—验证闭环、
确定性验证、修复周期、任务级工作流编排、显式重试、续跑资格、代码保存核验、执行记录查询
全部有真实服务与 HTTP 入口。

**但 Web 只接通了其中的任务 CRUD、工作流启动/取消/恢复与执行节点管理。** 用户无法仅通过
Web 完成"新建任务 → 准备环境 → 获取代码 → 生成计划 → 执行 → 查看验证结果 → 处置失败"
这条链路，也无法在首次部署后创建出任何可执行任务。

## 3. 文档与代码冲突（需要纠正的地方）

核对中发现以下历史文档结论与当前代码不一致。本节是后续修订的依据，本次已按第 3 列处理。

| 位置 | 文档原结论 | 代码事实 | 本次处理 |
|---|---|---|---|
| `README.md:25` | "自动重试、多任务并发调度仍未实现" | 有界并发调度器已实现并注册进生命周期（`internal/app/task_scheduler.go`、`internal/server/server.go:585-588`）；P3-M2/P3-M5 已验收 | 在 P4 文档中登记，README 需纠正（本次仅做最小必要修改，见 §7） |
| `README.md:1141` | "后台服务…任务调度器与自动 Retry 编排尚未接入" | `TaskScheduler` 与 `TaskRetryService` 均已构造并注册（`server.go:477-490`、`585-588`） | 同上 |
| `README.md:1131` | "项目、环境、容器、代码保存与 Agent 调度均**没有** HTTP 接口" | 代码保存有 `POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save`；调度确实没有 HTTP 接口 | 需改为"代码保存只有核验入口，没有列表入口" |
| `README.md:1134` | "任务页面…只能创建、查看、取消与删除任务记录，尚未接入步骤执行入口" | `TasksPage.tsx` 已接入工作流启动/取消/恢复与进度面板；仍无步骤执行入口 | 需区分"工作流入口已接入"与"步骤级入口未接入" |
| `README.md:1772-1790` | "多步骤自动编排与 Planner → Developer → Validator 自动串联""自动重试"未实现 | 任务级编排器 `TaskWorkflowService` 已按计划顺序串联开发与验证；自动重试（无需用户确认的重试）确实未实现 | 需拆分"编排已实现"与"自动重试未实现" |
| `.steps/P4-product-experience-optimization.md` | "项目、环境、容器、代码保存与 Agent 调度均无 HTTP 接口" | 代码保存核验有 HTTP 入口 | 本次已在 P4 文档中按事实拆分 |
| `.steps/P4-product-experience-optimization.md` | "机器页已接入执行节点接口" | 正确，`MachinesPage.tsx` 完整接入 CRUD + 重新校验 | 保留 |

**新发现（历史文档完全未记录）**：角色初始化（`app.RoleSeeds` + `storage.RoleSeeder`）在
**生产代码中没有任何调用者**。`agentflow server` 启动流程（`internal/server/server.go` 的
`openDatabase`）不导入角色，CLI 也没有角色管理命令。这意味着全新数据库上 `roles` 表为空，
`TaskService.CreateTask` → `ensureRoles` → `store.GetRole` 直接失败，**任何任务都无法创建**。
详见 G2。

## 4. 完整 API 清单（核对结果）

### 4.1 已存在的 HTTP 路由

以下路由全部经 `internal/httpapi/router.go` 的真实分发逻辑核实。

| 方法 | 路径 | 处理器 | 前端已封装 |
|---|---|---|---|
| GET | `/api/v1/health` | `router.healthHandler` | 是 |
| GET | `/api/v1/system/info` | `router.systemInfoHandler` | 是 |
| GET | `/api/v1/roles` | `roleHandler.list`（P4-M2-1 新增） | **否**（M2-4 接入） |
| GET | `/api/v1/environments` | `environmentHandler.serveList`（P4-M2-2 新增） | **否**（M2-3 接入） |
| GET | `/api/v1/environments/{type}/{name}/versions/{number}` | `environmentHandler.serveVersion`（P4-M2-2 新增） | **否**（M2-3 接入） |
| POST | `/api/v1/tasks` | `taskHandler.create` | 是 |
| GET | `/api/v1/tasks` | `taskHandler.list` | 是 |
| GET | `/api/v1/tasks/{taskID}` | `taskHandler.detail` | 是 |
| DELETE | `/api/v1/tasks/{taskID}` | `taskHandler.remove` | 是 |
| POST | `/api/v1/tasks/{taskID}/cancel` | `taskHandler.cancel` | 是 |
| POST | `/api/v1/tasks/{taskID}/start` | `serveTaskLaunch` | **否** |
| POST | `/api/v1/tasks/{taskID}/plan` | `servePlan` | **否** |
| POST | `/api/v1/tasks/{taskID}/execute-workflow` | `workflowHandler.start` | 是 |
| POST | `/api/v1/tasks/{taskID}/cancel-workflow` | `workflowHandler.cancel` | 是 |
| POST | `/api/v1/tasks/{taskID}/resume-workflow` | `workflowHandler.resume` | 是 |
| GET | `/api/v1/tasks/{taskID}/workflow` | `workflowHandler.status` | 是 |
| POST | `/api/v1/tasks/{taskID}/execute-steps` | `stepExecutionHandler.serveSteps` | **否** |
| POST | `/api/v1/tasks/{taskID}/steps/{stepID}/execute` | `stepExecutionHandler.serve` | **否** |
| POST | `/api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate` | `serveExecuteAndValidate` | **否** |
| POST | `/api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle` | `serveOpenRepairCycle` | **否** |
| POST | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate` | `validationHandler.serve` | **否** |
| POST | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code` | `codeInitializeHandler.serve` | **否** |
| GET | `/api/v1/tasks/{taskID}/steps` | `executionHandler.listSteps` | **否** |
| GET | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts` | `executionHandler.listAttempts` | **否** |
| GET | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/sessions` | `executionHandler.listSessions` | **否** |
| GET | `/api/v1/tasks/{taskID}/steps/{stepID}/validations` | `executionHandler.listValidations` | **否** |
| GET | `/api/v1/tasks/{taskID}/sessions/{sessionID}` | `executionHandler.getSession` | **否** |
| POST | `/api/v1/tasks/{taskID}/retry` | `taskRetryHandler.retry` | **否** |
| GET | `/api/v1/tasks/{taskID}/retry-eligibility` | `taskRetryHandler.eligibility` | **否** |
| POST | `/api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save` | `codeRecoveryHandler.verify` | **否** |
| GET | `/api/v1/tasks/{taskID}/resume-eligibility` | `codeRecoveryHandler.serveEligibility` | **否** |
| GET | `/api/v1/hosts` | `hostHandler.list` | 是 |
| POST | `/api/v1/hosts` | `hostHandler.create` | 是 |
| GET | `/api/v1/hosts/{hostID}` | `hostHandler.detail` | 是 |
| PATCH | `/api/v1/hosts/{hostID}` | `hostHandler.update` | 是 |
| DELETE | `/api/v1/hosts/{hostID}` | `hostHandler.remove` | 是 |
| POST | `/api/v1/hosts/{hostID}/validate` | `hostHandler.validate` | 是 |

### 4.2 后端有服务方法、但完全没有 HTTP 入口的能力

| 能力 | 应用层入口 | 说明 |
|---|---|---|
| 角色管理 | `storage.RoleStore`（`CreateRole`/`UpdateRole`/`DeleteRole`/`GetRole`/`ListRoles`） | **没有 app 服务层**，只有存储接口；无 HTTP、无 CLI |
| 角色初始化 | `app.RoleSeeds`（`internal/app/roles.go:15`）、`storage.RoleSeeder.SeedRoles` | **生产代码零调用者**（仅测试调用） |
| 环境定义与版本 | `app.EnvironmentService`（`CreateVersion` / `Build` / `ReadDockerfile` / `DownloadBuildLog` / `Preflight` / `Init`）；查询部分已由 P4-M2-2 的 `app.EnvironmentQueryService`（`List` / `Get` / `GetVersion` / `History`）覆盖 | 查询已有 HTTP（P4-M2-2）；创建版本、构建、Dockerfile 读取仍只有 CLI（`agentflow docker env …`、`agentflow docker init`） |
| 容器管理 | `app.ContainerService.ListContainers` / `InspectContainer` / `StartContainer` / `StopContainer` / `DeleteContainer` / `DetachContainer` / `ApplyLifecycle` / `RestartContainer` / `ContainerLogs`（`internal/app/containers_ops.go`） | 只有 CLI（`agentflow docker container …`） |
| 代码保存记录查询 | `app.ContainerService.CodeSaveStatus`（`internal/app/code_save.go:145`） | 只有 CLI（`agentflow code status`）；**HTTP 只有按 recordID 的核验，没有列表** |
| 执行环境准备 | `app.ExecutionEnvironmentService.PrepareForTask`（`internal/app/execenv_task.go:35`） | 只有 CLI（`agentflow execenv prepare-task`）；`/start` 内部会调用 |
| 任务级工作区初始化 | `app.TaskWorkspaceService.InitializeTaskWorkspace`（`internal/app/task_workspace.go:204`） | 无独立 HTTP；`/start` 内部会调用 |
| 验证失败反馈 | `storage.ValidationFeedbackStore.GetValidationFailureFeedback` / `LatestValidationFailureFeedbackByStep`（`internal/storage/interfaces.go:610-629`） | 无 HTTP、无 CLI 查询入口 |
| 修复周期历史 | `storage.RepairCycleStore.ListRepairCyclesByStep`（`internal/storage/interfaces.go:383`） | 无 HTTP；只有 `open-repair-cycle` 写入口 |
| 任务事件时间线 | `storage.EventStore.ListEventsByTask`（`internal/storage/interfaces.go:644`） | 无 HTTP、无 CLI 查询入口 |
| 调度器状态 | `app.TaskScheduler.Status()`（`internal/app/task_scheduler.go:833`） | 无 HTTP、无 CLI |
| 项目配置 | `domain.ProjectConfig` / `docker.Layout.ProjectConfigPath`（`internal/domain/project.go:183`、`internal/docker/layout.go:143`） | **无存储表、无 app 服务、无 HTTP、无 CLI**；`project_id` 仅是任务配置里的受校验字符串，用于缓存共享范围 |

### 4.3 前端 `src/lib/api` 实际封装的端点

`endpoints.ts` 只导出以下请求：`fetchHealth`、`fetchSystemInfo`、`fetchTasks`、`fetchTask`、
`createTask`、`cancelTask`、`deleteTask`、`fetchTaskWorkflow`、`startTaskWorkflow`、
`cancelTaskWorkflow`、`resumeTaskWorkflow`、`fetchHosts`、`fetchHost`、`createHost`、
`updateHost`、`validateHost`、`deleteHost`。

**未封装**：`/start`、`/plan`、`/execute-steps`、`/steps/{id}/execute`、`/execute-and-validate`、
`/open-repair-cycle`、`/validate`、`/initialize-code`、`/steps`、`/attempts`、`/sessions`、
`/validations`、`/retry`、`/retry-eligibility`、`/verify-code-save`、`/resume-eligibility`。

### 4.4 前端页面实际接通情况

| 页面 | 文件 | 状态 |
|---|---|---|
| 概览 | `src/pages/overview/ConnectionPanel.tsx` | 已接通（健康检查，明确声明不代表 SSH/Docker/Pi 已就绪） |
| 任务 | `src/pages/tasks/TasksPage.tsx`、`CreateTaskModal.tsx`、`status.ts` | 部分接通（CRUD + 工作流启动/取消/恢复 + 进度面板；无步骤级入口、无验证结果、无重试、无收尾结论） |
| 机器 | `src/pages/machines/MachinesPage.tsx`、`HostFormModal.tsx`、`HostValidationPanel.tsx`、`status.ts` | 已接通（CRUD + 重新校验 + 预检详情） |
| 项目 | `src/pages/projects/ProjectsPage.tsx` | 占位（`PagePlaceholder`） |
| 环境 | `src/pages/environments/EnvironmentsPage.tsx` | 占位（`PagePlaceholder`） |
| 设置 | `src/pages/settings/SettingsPage.tsx` | 占位（`PagePlaceholder`） |

## 5. 主矩阵

> 优先级定义：
> **P0** 端到端阻塞（用户无法通过预期产品入口完成核心业务路径）；
> **P1** 业务决策阻塞（看不懂验证结果/失败原因/恢复条件，或无法执行必要操作）；
> **P2** 管理能力缺失（须按真实业务依赖排序）；
> **P3** 非阻塞体验优化。

| ID | 用户目标 | 后端业务能力 | HTTP API | 前端客户端 | Web 页面 | 业务阻塞 | 代码证据 | 后续里程碑 | 优先级 |
|---|---|---|---|---|---|---|---|---|---|
| **G1** | 新建任务后一键开始执行（准备容器 → 初始化工作区 → 生成计划 → 启动编排） | 已实现（`TaskLaunchService.Launch`） | `POST /tasks/{id}/start`（202，含 stage / task_status / plan_persisted / workflow_started） | **缺失** | **缺失**：任务页只有"执行工作流"，直接调 `execute-workflow` | **是**：新建任务为 `queued`、无容器、无步骤，`CheckWorkflowStart` 必然返回 409；用户无法从 Web 启动任何新任务 | `internal/app/task_launch_run.go:36`；`internal/domain/workflow_run.go:608`（要求 `task.ContainerID != ""` 且 `len(steps) > 0`）；`lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx:231` | P4-M2 | **P0** |
| **G2** | 首次部署后能创建出可执行任务（角色存在且可选） | **已由 P4-M2-1 闭合后端部分**（启动期导入 + `GET /api/v1/roles`；本行保留 P4-M1 盘点时的现场）；原状态：**部分实现且断链**：`storage.RoleStore` CRUD 存在，`app.RoleSeeds` 与 `storage.RoleSeeder.SeedRoles` 存在，但**生产代码无任何调用者**（`agentflow server` 启动不导入角色，无 CLI、无 app 服务） | **不存在**（无 `/roles`） | 缺失 | 缺失（创建任务需手填角色 ID） | **是**：全新数据库 `roles` 为空 → `CreateTask` → `ensureRoles` → `GetRole` 失败 → 任务无法创建 | `internal/app/roles.go:15`（仅测试调用）；`internal/storage/interfaces.go:651`；`internal/app/task.go:292`；`internal/server/server.go:333`（`openDatabase` 不导入角色） | P4-M2 | **P0** |
| **G3** | 选择/创建语言环境与可用环境版本（含镜像构建） | **查询已由 P4-M2-2 闭合**（新增只依赖存储的 `app.EnvironmentQueryService`；初始化/创建版本/构建仍由 `EnvironmentService` 承担）；本行保留 P4-M1 盘点时的现场 | **查询已实现**：`GET /api/v1/environments?host_id=…`、`GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=…`；构建/CRUD 仍**不存在** | 缺失 | 占位页 | **查询部分不再是阻塞**：环境与版本的构建状态、`image_ref`、服务端计算的可用性可从 HTTP 读取；剩余阻塞是前端接入（M2-3）与环境构建 Web 入口（未排期），CLI `agentflow docker init` / `docker env build` 仍是生成版本的唯一入口 | `internal/app/environments_query.go`（`EnvironmentQueryService`）；`internal/httpapi/environments.go`；`internal/app/execenv_prepare.go:145-180`；`internal/app/environments_build.go:47` | P4-M2（查询部分已完成，见 [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)） | **P0** |
| **G4** | 从可用执行节点中选择节点（而非手抄 ID） | 已实现（`ExecutionHostService`） | `GET /hosts` 已存在 | 已封装 `fetchHosts` | **部分接通**：`CreateTaskModal` 仍是自由文本 `Input`，未复用 `fetchHosts` | **否**（可手填，但易错且与已有能力脱节） | `lingting-agent-flow-ui/src/pages/tasks/CreateTaskModal.tsx:280-290`；`endpoints.ts` 的 `fetchHosts` | P4-M2 | **P1** |
| **G5** | 失败任务显式重试（先看资格再决定） | 已实现（`TaskRetryService.Retry` / `Eligibility`，10 个稳定拒绝原因） | `POST /tasks/{id}/retry`、`GET /tasks/{id}/retry-eligibility` | **缺失** | **缺失** | **是（业务决策）**：`failed` 任务在 Web 上无任何后续动作；前端也不得自行推断可重试 | `internal/httpapi/task_retry.go`；`internal/domain/task_retry.go:22-49`；`lingting-agent-flow-ui/src/pages/tasks/status.ts`（无重试判定） | P4-M4 | **P1** |
| **G6** | 看懂验证结果与失败原因（哪些检查失败、退出码、输出摘要、语义审查结论） | 部分实现：验证历史与检查结果可查；失败反馈查询 API 已补齐（P4-M2-5），前端视图仍缺 | `GET /tasks/{id}/steps/{sid}/validations`；`GET …/validations/{runID}/feedback`（P4-M2-5） | **缺失** | **缺失** | **是（业务决策）**：用户无法判断"为什么没通过"，只能看到 `error_summary` | `internal/httpapi/executions.go`、`internal/httpapi/execution_feedback.go`（P4-M2-5）、`internal/app/execution_feedback.go`（P4-M2-5） | P4-M3（M3-2 前端视图） | **P1** |
| **G7** | 查看步骤 / Attempt / Session 历史（含归档与导出状态） | 已实现（`ExecutionRecordService`） | `GET /tasks/{id}/steps`、`.../attempts`、`.../sessions`、`/tasks/{id}/sessions/{sid}` | **缺失** | **缺失** | **是（业务决策）**：无法查看历史尝试与失败记录 | `internal/httpapi/executions.go:237-256`、`:258-330`、`:419` | P4-M3 | **P1** |
| **G8** | 查看代码保存（提交/推送/合并）记录并核验 | 部分实现：记录查询与核验都在，但**没有列表 API**，`recordID` 在 Web 上不可获得 | `POST .../code-saves/{recordID}/verify-code-save`、`GET .../resume-eligibility`（其中含未决记录） | **缺失** | **缺失** | **是（业务决策）**：用户无法确认代码是否真的推送到远端 | `internal/app/code_save.go:145`（仅 CLI）；`internal/httpapi/code_recovery.go:145`、`:227` | P4-M3（列表 API）+ P4-M4（核验操作） | **P1** |
| **G9** | 处置旧容器（`delete` / `detach`）以便重新开始 | 已实现（`ContainerService` 全套） | **不存在** | 缺失 | 缺失 | **是**：重新开始/重试的错误信息直接要求用户执行 `agentflow docker container inspect/delete/detach`，Web 无对应入口 | `internal/app/execenv_task.go:272-283`（错误文本）；`internal/app/containers_ops.go:29-401` | P4-M4（+ 需补 HTTP） | **P1** |
| **G10** | 看懂任务最终结论与收尾闭环状态 | 已实现（`DecideTaskCloseoutView`） | 任务响应已含 `final_result` / `closeout` / `closeout_summary` | **类型缺失**：`types.ts` 的 `Task` 无这三个字段 | **缺失**：详情面板不展示 | **是（业务决策）**：无法区分"步骤全通过但收尾未确认"与真正成功 | `internal/httpapi/tasks.go:52-66`、`:381-405`；`lingting-agent-flow-ui/src/lib/api/types.ts:60-95`（缺字段）；`TasksPage.tsx` 的 `TaskDetail` | P4-M3 | **P1** |
| **G11** | 看懂"为什么不能继续"以及下一步该做什么 | 已实现（`ResumeEligibilityService`，含 `blocking` / `next_actions` / 逐记录动作） | `GET /tasks/{id}/resume-eligibility` | **缺失** | **缺失** | **是（业务决策）**：`blocked` / `recovery_required` 下用户无任何指引 | `internal/app/resume_eligibility.go:98-136`、`:227-266`；`internal/httpapi/code_recovery.go:227` | P4-M3 | **P1** |
| **G12** | 管理项目（环境、版本、缓存、工作目录） | **缺失**：只有 `domain.ProjectConfig` 校验与 `docker.Layout.ProjectConfigPath`，**无存储表、无 app 服务** | 不存在 | 缺失 | 占位页 | 否（`project_id` 可选，缺失只影响缓存共享与目录命名） | `internal/domain/project.go:183-283`；`internal/docker/layout.go:143`；`internal/app/task_management.go:117`（仅校验书写） | P4-M2（后端能力需先立项） | **P2** |
| **G13** | 单独生成/重生成计划 | 已实现（`GeneratePlan`） | `POST /tasks/{id}/plan` | 缺失 | 缺失 | 否（`/start` 会覆盖该阶段） | `internal/httpapi/plan.go`；`internal/app/task_workflow_plan.go` | P4-M2（随 G1 一起） | **P2** |
| **G14** | 单步骤执行 / 顺序执行 / 开发—验证闭环 / 开启修复周期 | 已实现 | `/execute`、`/execute-steps`、`/execute-and-validate`、`/open-repair-cycle` | 缺失 | 缺失 | 否（任务级编排已覆盖常规路径；这些是诊断与干预手段） | `internal/httpapi/step_execution.go`、`step_sequence.go`、`step_workflow.go`、`step_cycle.go` | P4-M2 | **P2** |
| **G15** | 查看任务事件时间线 | 已实现（`ListEventsByTask`） | **不存在** | 缺失 | 缺失 | 否 | `internal/storage/interfaces.go:644`；`internal/storage/sqlite/events.go:61` | P4-M3 | **P2** |
| **G16** | 查看修复周期历史与预算消耗 | 已实现（`ListRepairCyclesByStep`） | **不存在** | 缺失 | 缺失 | 部分（`execute-and-validate` 响应含 `round` / `attempts` / `max_attempts`，但历史不可查） | `internal/storage/interfaces.go:370-383`；`internal/httpapi/step_workflow.go:100-110` | P4-M3 | **P2** |
| **G17** | 查看队列与调度名额（并发上限、当前占用） | 已实现（`TaskScheduler.Status`） | **不存在** | 缺失 | 缺失 | 否 | `internal/app/task_scheduler.go:833` | P4-M5 前置（先测量） | **P3** |
| **G18** | 查看步骤起止时间 | 部分实现：`WorkflowStepView` 有 `StartedAt`/`FinishedAt`，但响应构造**从不填充** | `GET /tasks/{id}/workflow` 的 `steps[].started_at` / `finished_at` **恒为空** | 类型已声明 | 未展示 | 部分（影响"真实执行进度"判断） | `internal/app/task_workflow_run.go:107-121`（有字段）；`internal/httpapi/workflow.go:456-468`（未赋值） | P4-M3 | **P2** |
| **G19** | 无需手动点击即可看到进度推进 | 不涉及 | 不涉及 | 不涉及 | **缺失**：进度面板只有"刷新"按钮，无轮询 | 否 | `lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx`（`loadWorkflow` 仅在打开详情/手动刷新/操作后调用） | P4-M3 | **P3** |
| **G20** | 从 Web 发起"初始化代码 / 确定性验证"等单步诊断动作 | 已实现 | `/initialize-code`、`/validate` | 缺失 | 缺失 | 否（常规路径由编排覆盖） | `internal/httpapi/code_initialize.go`、`validation.go` | P4-M4 | **P3** |
| **G21** | 管理执行节点与环境预检 | 已实现 | `/hosts` 全套 | 已封装 | 已接通 | 否 | `MachinesPage.tsx`、`HostValidationPanel.tsx` | —（无需后续工作） | — |

## 6. 缺口详情与验收标准

### G1 统一任务启动未接入 Web（P0）

**事实**：后端把"准备容器 → 初始化工作区 → 生成计划 → 启动编排"收敛为
`POST /api/v1/tasks/{taskID}/start`（`internal/httpapi/task_launch.go`，返回 202 与
`stage` / `task_status` / `container_prepared` / `workspace_status` / `plan_persisted` /
`workflow_started`）。前端 `TasksPage.tsx:231` 的"执行工作流"直接调用
`startTaskWorkflow` → `POST /execute-workflow`。

**为什么必然失败**：`domain.CheckWorkflowStart`（`internal/domain/workflow_run.go:608`）
要求 `task.ContainerID` 非空且已持久化计划步骤非空。新建任务（`queued`）两项都不满足，
因此该按钮对新建任务一定返回 409，且错误信息只是"状态不允许"。

**注意**：不得把"准备容器 + 生成计划 + 启动编排"在前端拼成第二套工作流。正确做法是接入
既有 `/start`，由服务端决定从哪个阶段继续（`domain.DecideTaskLaunchNext`）。

**验收标准**

1. 任务详情中存在一个以 `/start` 为后端的启动入口；点击后前端展示服务端返回的真实
   `stage` 与 `workflow_started`，不自行推断"已开始执行"。
2. 对 `queued` 任务点击启动，`workflow_started=true` 且任务状态离开 `queued`；响应中
   `container_prepared`、`plan_persisted` 与数据库事实一致。
3. `/start` 返回 `workflow_started=false` 时，页面不得显示为"执行中"或"已完成"。
4. 前端不新增任何阶段拼接逻辑：`grep` 前端代码不存在对 `/execenv prepare-task`、
   `/plan` 的顺序编排调用。

### G2 角色初始化与角色管理没有生产入口（P0）

> **状态更新（2026-10-26）**：本节描述的是 P4-M1 盘点时的现场。**P4-M2-1 已闭合后端
> 部分**：`agentflow server` 启动时在数据库打开后导入初始角色（只在空表时），并新增只读
> `GET /api/v1/roles`。仍缺：角色变更入口（CRUD 或显式同步）与前端角色选择器（M2-4）。
> 契约与证据见 [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md)。

**事实**：

- `storage.RoleStore` 提供 `CreateRole` / `UpdateRole` / `DeleteRole` / `GetRole` / `ListRoles`。
- `app.RoleSeeds`（`internal/app/roles.go:15`）把 `config.Roles` 转成 `storage.RoleSeed`。
- `storage.RoleSeeder.SeedRoles`（`internal/storage/sqlite/roles.go:158`）在表为空时导入。
- **全仓库生产代码中 `RoleSeeds` / `SeedRoles` / `CreateRole` 零调用者**（仅 `tests/` 调用）。
- `agentflow server` 的 `openDatabase`（`internal/server/server.go:333`）只打开数据库与
  Session 目录，不导入角色；CLI 没有 `agentflow role` 命令组。
- `TaskService.CreateTask` → `prepareTaskRequest` → `ensureRoles`（`internal/app/task.go:292`）
  对每个角色执行 `store.GetRole`，失败即返回错误。

**后果**：全新部署（`configs/example.yaml` 里配置了 roles）不会把角色写进数据库，
`POST /api/v1/tasks` 必然失败。这是比"前端缺下拉框"严重得多的后端断链。

**需要补齐的服务端能力**（不得只加前端下拉框）：

1. 启动时（或显式命令）把配置角色导入数据库，且只在表为空时导入（复用 `SeedRoles` 语义，
   不覆盖已有角色）。
2. 角色查询接口（至少 `GET /api/v1/roles`），供创建任务时选择。
3. 角色增删改接口或明确 CLI 入口，用于角色变更（当前 `RoleStore` 已有能力但无入口）。

**验收标准**

1. 全新数据库启动 `agentflow server` 后，`roles` 表包含配置声明的角色；重复启动不重复导入、
   不覆盖已有角色。
2. `GET /api/v1/roles` 返回真实角色记录（`id` / `name` / `type` / `enabled`）。
3. 创建任务弹窗中的角色选择来自该接口，不再手填 ID；非法角色类型仍在服务端被拒绝。
4. 已有测试 `tests/unit/app/roles_test.go`、`tests/unit/store/roles_test.go` 的语义不被破坏。

### G3 环境（语言环境 / 版本 / 镜像）无 HTTP 入口（P0）

> **状态更新（P4-M2-2，2026-10-26）**：查询部分已闭合。新增只依赖存储的
> `app.EnvironmentQueryService`（`List` / `Get` / `GetVersion` / `History`）与两条只读路由
> `GET /api/v1/environments?host_id=…`、`GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=…`，
> 返回真实构建状态、`image_ref`、服务端计算的可用性（`usable`）、当前可用版本
> （`current_version`）与最新创建版本（`latest_version`）。CLI 的 `docker env list/show/history`
> 同步改用该只读入口。构建、CRUD 与 Dockerfile 读取仍无 HTTP 入口，构建仍只有 CLI。
> 契约与测试证据见 [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)。
> 以下为 P4-M1 盘点时的现场记录。

**事实**：`app.EnvironmentService` 已有 `Preflight` / `Init` / `CreateVersion` / `Build` /
`List` / `Get` / `History` / `ReadDockerfile` / `DownloadBuildLog`。全部只经 CLI 暴露
（`internal/cli/docker.go`、`docker_env.go`、`docker_env_edit.go`）。

`ExecutionEnvironmentService.validatePrepare`（`internal/app/execenv_prepare.go:145-180`）
在准备容器前必须 `ResolveVersion` 成功，否则返回"环境尚未创建版本；请先执行
agentflow docker init"。

**后果**：Web 无法准备执行环境 → `/start` 会在 `container` 阶段失败 → 完整链路断在第一步。

**需要补齐的服务端能力**：`GET /api/v1/environments?host_id=…`（列表 + 版本 + 构建状态）、
`GET /api/v1/environments/{ref}/versions/{n}`、`POST …/build`（可选，取决于产品取舍）。

**验收标准**

1. 前端能按执行节点列出环境与其版本，并显示真实 `status` / `image_ref` / 可用性。
2. 不可用（未构建成功）的环境版本在界面上不可选，且服务端仍会拒绝。
3. 不新增第二套构建流程：构建仍经 `EnvironmentService.Build`。

### G4 执行节点选择未复用已有接口（P1）

**事实**：`fetchHosts` 已封装、`MachinesPage` 已在用；`CreateTaskModal` 的 `host_id` 仍是
`<Input placeholder="执行节点 ID" />`，并在文案里写"执行节点与角色目前没有查询接口，
需要手工填写标识"——这句话对执行节点已经不成立。

**验收标准**

1. 创建任务弹窗的执行节点字段是下拉选择，数据来自 `fetchHosts`。
2. 只列出 `enabled` 节点；`usable_for_remote_task=false` 的节点给出明确提示。
3. 页面文案不再声称执行节点没有查询接口。

### G5 失败任务重试无 Web 入口（P1）

**事实**：`POST /tasks/{id}/retry` 与 `GET /tasks/{id}/retry-eligibility` 已实现
（`internal/httpapi/task_retry.go`），拒绝原因稳定（`internal/domain/task_retry.go:22-49`，
含 `status_not_retryable` / `lease_held` / `active_workflow_run` / `step_occupied` /
`recovery_residue` / `external_side_effect_pending` / `prerequisite_missing` /
`nothing_to_retry`）。前端零封装、零入口。

**约束**：前端不得自行推断"能不能重试"，必须调用 `retry-eligibility` 并展示 `blocking`。

**验收标准**

1. `failed` 任务展示"重试"入口，其可用性来自 `retry-eligibility` 的 `can_retry`。
2. 不可重试时展示 `blocking` 分类与 `summary`，并给出下一步动作。
3. 重试请求成功后展示服务端返回的 `previous_status` / `task_status` / `workflow_started`，
   不把 202 当作"已完成"。
4. 前端不出现硬编码的状态白名单。

### G6 验证结果与失败反馈不可见（P1）

> **状态更新（P4-M2-5，2026-10-26）**：失败反馈查询的 **API 部分已闭合**。新增只读
> `GET /api/v1/tasks/{taskID}/steps/{stepID}/validations/{runID}/feedback`，由
> `app.ExecutionRecordService.GetValidationFailureFeedback` 校验任务 → 步骤 → 验证记录 →
> 反馈的完整归属链后返回已持久化并已脱敏的逐项失败检查；没有反馈时返回 `404`，不构造空
> 反馈，也不把缺失当作验证通过。步骤级"最近一次反馈"入口经核实由验证历史列表覆盖，本轮
> 有意不新增。前端验证历史与逐项检查视图仍归 P4-M3（M3-2）。契约与测试证据见
> [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md)。

**事实**：

- 有：`GET /tasks/{id}/steps/{sid}/validations` 返回 `state` / `latest` / `records`，
  每条记录含 `status`、`verdict`、`checks[]`（`check_name`、`exit_code`、`timed_out`、
  `certain`、`stdout_summary`、`stderr_summary`、`failure_reason`）、`review_*` 语义审查结论。
- 无：`validation_feedbacks` 的查询接口（存储层 `GetValidationFailureFeedback` /
  `LatestValidationFailureFeedbackByStep` 存在，`internal/storage/interfaces.go:610-629`，
  但无 HTTP、无 CLI）。

**后果**：用户看不到"哪条必需检查失败、退出码是多少、输出是什么"，也无法拿到
"上一次验证未通过的问题"用于判断下一步。

**验收标准**

1. 步骤详情可展示验证历史，逐项检查的 `status` / `exit_code` / 失败原因 / 输出摘要可见。
2. 明确区分 `status`（流程状态）与 `verdict`（结论），不把 `inconclusive` 显示为通过。
3. 失败反馈可查（新增服务端查询能力），且展示前已脱敏（复用 `domain.RedactSensitiveText`）。

### G7 步骤 / Attempt / Session 历史不可见（P1）

**事实**：`executions.go` 提供 5 条只读查询路由；`SessionResponse` 已分别输出
`status` / `archive_status` / `export_status`（`internal/httpapi/executions.go:105-160`）。
前端零封装。

**验收标准**

1. 可按步骤查看尝试列表（`attempt_number`、`status`、`exit_code`、`timed_out`、
   `cancelled`、`error_class`、`failure_stage`、`verification_verdict`）。
2. 可按尝试查看会话，归档状态与导出状态分列展示，不合并为一个字段。
3. 不展示 Session 内容正文；只展示服务端返回的路径、状态与摘要。

### G8 代码保存记录不可见（P1）

**事实**：`CodeSaveStatus`（`internal/app/code_save.go:145`）只有 CLI（`agentflow code status`）。
HTTP 只有 `POST .../code-saves/{recordID}/verify-code-save`，而 `recordID` 在 Web 上无法获得；
`GET .../resume-eligibility` 会返回**未决**记录（`codeSaveEligibilityViews` 过滤掉已收敛的），
但不能当作完整历史列表。

**验收标准**

1. 任务详情可查看代码保存记录：`task_branch`、`commit_status` + `commit_sha`、
   `push_status` + `remote_commit_sha`、`merge_status` + `merge_sha`、`persist_status`、
   `recovery_status`、`error_summary`。
2. 三个阶段分别展示，不用单一"已保存"替代。
3. `recordID` 从列表接口获得，不要求用户手抄。

### G9 容器管理无 HTTP 入口（P1）

**事实**：容器全套操作只在 CLI（`internal/cli/docker_container.go`、
`docker_container_lifecycle.go`）。而重新开始/重试的阻塞信息直接要求用户执行 CLI：

```text
任务 %s 仍有未处置的容器记录 %s（状态 %s），请先执行 agentflow docker container inspect %s 核验现场，
再显式 delete 或 detach 后再重新准备执行环境
```
（`internal/app/execenv_task.go:272-283`）

**验收标准**

1. 任务详情能看到该任务的容器记录与状态（`unknown` / 停止 / detached / 已删除）。
2. 提供显式 `delete` 与 `detach` 动作，且动作前展示真实状态，不做"自动接管"。
3. detached 容器不出现在可操作列表里，也不被自动启动、停止或删除。

### G10 任务最终结果与收尾结论未展示（P1）

**事实**：`TaskResponse` 已输出 `final_result` / `closeout` / `closeout_summary`
（`internal/httpapi/tasks.go:52-66`），`closeout` 取值 `confirmed` / `pending` /
`unconfirmed` / `inconsistent` / `cancelled`（`domain.TaskCloseoutState`）。前端 `Task` 类型
（`types.ts:60-95`）**没有这三个字段**，`TaskDetail` 也不展示。

**后果**：`completed` + `closeout=pending`（执行记录收尾尚未确认）会被用户当成成功。

**验收标准**

1. `types.ts` 补齐三个字段并与 Go 端一致。
2. 任务详情展示"最终结论"与"收尾闭环结论"，`closeout != confirmed` 时给出明确警示。
3. 不把 `status=completed` 单独显示为"任务成功"。

### G11 恢复阻塞原因与下一步动作不可见（P1）

**事实**：`ResumeEligibility` 返回 `can_resume`、`blocking[]`、`code_save_action`、
`code_save_records[]`、`next_actions[]`、`summary`（`internal/app/resume_eligibility.go:36-58`）。
前端零封装。

**验收标准**

1. `blocked` / `recovery_required` 的任务展示 `blocking` 与 `next_actions`。
2. `next_actions` 里指向 CLI 的动作如实标注为"需在 CLI 执行"，不伪造 Web 按钮。

### G12 项目能力在后端缺失（P2）

**事实**：`domain.ProjectConfig` 与 `ValidateProjectID` 存在，但**没有 `projects` 表**
（`internal/storage/sqlite/migrations/` 中无该表）、没有 store、没有 app 服务、
没有 CLI、没有 HTTP。`task.Config.ProjectID` 只是一个受校验的字符串，进入
`code_saves.project_id` / `code_fetches.project_id` 与缓存共享范围键
（`internal/app/containers.go:244-250`）。

**结论**：这不是"缺前端页面"，而是"后端业务能力本身不存在"。补齐需要新表 + 迁移 +
服务 + 接口，属独立立项。

**验收标准**（若立项）

1. 存在 `projects` 表与迁移，记录 id / name / 环境 / 版本 / 缓存 / 工作目录。
2. 创建任务时项目来自真实记录，且 `ValidateProjectID` 规则不被绕过。
3. 未立项时，项目页继续是占位页，且不出现"已可用"的假象。

### G18 工作流步骤起止时间恒为空（P2）

> **已于 P4-M3-6 闭合**（2026-10-27）。实施时核实出三处缺口而不只是下面记录的
> `workflowStatusResponse` 未赋值一处：领域 `Step.Transition` 只在 `IsTerminal()` 写
> `finished_at`（导致 `failed` / `recovery_required` / `retry_wait` 永远没有结束时间）、
> HTTP 转换未映射、前端用 `updated_at` 冒充。证据与验收结果见
> [`P4-M3-6-workflow-step-timestamps.md`](./P4-M3-6-workflow-step-timestamps.md)。以下为该
> 盘点时的现场记录，保留不改。

**事实**：`WorkflowStepView` 定义了 `StartedAt` / `FinishedAt`
（`internal/app/task_workflow_run.go:118-120`），`WorkflowStepResponse` 也声明了
`started_at` / `finished_at`（`internal/httpapi/workflow.go:103-104`），但
`workflowStatusResponse`（`:456-468`）**从不给这两个字段赋值**，因此 JSON 里永远缺省。

**验收标准**

1. 步骤真实执行后，`GET /tasks/{id}/workflow` 的 `steps[].started_at` 非空。
2. 未开始的步骤两个字段保持缺省，不用零值冒充。

## 7. 后续里程碑工作项与依赖

### P4-M2：补齐核心操作入口（优先打通完整任务生命周期）

依赖：P4-M1（本文档）。

| 工作项 | 覆盖缺口 | 前置 | 备注 |
|---|---|---|---|
| M2-1 角色初始化接入启动流程 + `GET /api/v1/roles` | G2 | 无 | **已完成（2026-10-26）**：见 [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md)；剩余角色变更入口与前端选择器（M2-4） |
| M2-2 环境查询 HTTP 接口 | G3 | 无 | 只读列表 + 版本 + 构建状态优先；构建入口次之。**已完成**（查询部分，见 [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)） |
| M2-3 前端接入统一启动 `/start` | G1 | M2-1、M2-2 | **已完成（2026-10-26）**：任务页"启动"走 `POST /tasks/{id}/start`，`execute-workflow` 不再用于新任务；环境选择接入 M2-2 接口。见 [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md) |
| M2-4 创建任务弹窗复用 `fetchHosts` 与角色接口 | G4、G2 | M2-1 | 纯前端 + 已有接口。**已完成（2026-10-26）**：见 [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md) |
| M2-5 验证失败反馈查询 API | G6（部分） | 无 | 存储层已具备，只缺 HTTP。**已完成（2026-10-26）**：见 [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md) |
| M2-6 代码保存记录列表 API | G8（部分） | 无 | 让 `recordID` 可获取 |
| M2-7 单步骤 / 顺序执行 / 闭环 / 修复周期入口（诊断用） | G14、G13 | M2-3 | 优先级低于 M2-1~M2-4 |

**M2 退出条件**：全新部署 → Web 创建任务 → 启动 → 工作流真实推进，全程无需 CLI。

### P4-M3：让用户能观察真实进度、验证结果、失败原因与恢复状态

依赖：P4-M2。

| 工作项 | 覆盖缺口 |
|---|---|
| M3-1 步骤 / Attempt / Session 历史视图 | G7 |
| M3-2 验证历史与逐项检查结果视图 | G6 |
| M3-3 任务最终结论与收尾闭环展示（含 `types.ts` 补字段） | G10 |
| M3-4 续跑资格与阻塞原因展示 | G11 |
| M3-5 代码保存三阶段状态展示 | G8 |
| M3-6 工作流步骤起止时间补全（服务端） | G18 |
| M3-7 进度自动刷新 | G19 |
| M3-8 事件时间线与修复周期历史（需先补 API） | G15、G16 |

**M3 退出条件**：任一失败或阻塞的任务，用户能从 Web 独立判断"卡在哪、为什么、下一步做什么"，
且所有展示字段都能追溯到具体接口。

### P4-M4：完善继续、取消、重试、恢复、核验与历史查看操作

依赖：P4-M2、P4-M3。

| 工作项 | 覆盖缺口 |
|---|---|
| M4-1 重试入口（资格前置，禁止前端推断） | G5 |
| M4-2 代码保存核验操作入口 | G8 |
| M4-3 容器记录查看与显式 `delete` / `detach`（需先补 HTTP） | G9 |
| M4-4 继续执行 / 恢复工作流的可用性来自 `resume-eligibility` | G11 |
| M4-5 单步诊断动作（初始化代码、确定性验证） | G20 |

**M4 退出条件**：`failed` / `blocked` / `recovery_required` 三类现场都能在 Web 上被理解并处置，
且所有有副作用的操作都需要用户显式触发（不自动重放）。

### P4-M5 继续保持后置

本次核对**没有发现**性能、存储或架构问题正在阻塞主流程。唯一与"规模"相关的现象是并发上限
为"同一份数据库上的名额上限"（P3-M5 §3 已明确为非目标），当前部署形态下不构成阻塞。
因此 M5 维持后置，先由 M2/M3/M4 打通业务链路；若 M2 之后出现实测数据（例如任务列表分页在
真实数据量下变慢），再以测量结果决定优化项。

## 8. 优先级依据（为什么不按数量排序）

1. **G2 优先于一切**：角色表为空时，`POST /api/v1/tasks` 直接失败，任何后续入口都没有对象可
   操作。它同时阻塞 G1、G4、G5、G14 等所有依赖"存在任务"的工作项。
2. **G3 与 G1 构成第二条必经链路**：即使有任务，没有环境版本也无法准备容器，`/start` 会在
   `container` 阶段失败。这两项解除后，"新建 → 执行"才第一次真正可行。
3. **G5 / G6 / G7 / G8 / G10 / G11 属于"业务决策阻塞"**：它们不影响任务跑起来，但决定用户能
   否在失败后做出正确判断。缺这些，用户只能看到 `failed` 而无法行动。
4. **G12 / G13 / G14 / G15 / G16 / G18 属管理或诊断能力**：有替代路径（CLI、`/start`、
   `execute-and-validate` 的响应字段），因此排在 P2。
5. **G17 / G19 / G20 属体验优化**：不阻塞任何业务动作。

## 9. 实际执行的检查与测试

| 项目 | 状态 | 说明 |
|---|---|---|
| 代码阅读与路由核实 | **已执行** | `internal/httpapi/*.go` 全量路由与分发；`internal/app`、`internal/domain`、`internal/storage` 关键服务与规则 |
| 前端源码核实 | **已执行** | `src/lib/api/{endpoints,types,index}.ts`、`src/pages/**`、`src/app/navigation.tsx` |
| 生产调用者追溯 | **已执行** | `grep` `RoleSeeds` / `SeedRoles` / `CreateRole` 全仓库，确认无生产调用者 |
| 历史文档对照 | **已执行** | `.steps/README.md`、`ROADMAP.md`、`P3-multi-task-scheduling.md`、`P3-M5-multi-task-acceptance.md`、`P4-product-experience-optimization.md` |
| Go 测试（`go test`） | **未执行** | 本次未获测试授权；本文档结论全部来自静态核实 |
| `gofmt` / `go vet` | **未执行** | 本次未修改任何 Go 代码 |
| 前端 `pnpm lint` / `pnpm test` | **未执行** | 本次未修改任何前端代码 |
| 浏览器验证 | **未执行** | 用户未提供地址，也未要求访问页面 |

## 10. 已知风险与待调查

| 项 | 说明 |
|---|---|
| R1 | G2 的结论依赖"生产代码无角色导入调用者"。若存在未纳入本仓库的部署脚本（例如手工 `sqlite3` 插入角色），实际影响会降低；但这本身说明产品缺少正式入口，仍应补齐 |
| R2 | G3 的具体产品取舍（Web 是否应支持"创建环境版本 + 构建镜像"）尚未确定。只读查询是必要条件，写操作是否进 Web 需要用户确认 |
| R3 | G12（项目）需要新表与迁移，属后端立项，不能在 M2 里"顺手加个页面" |
| R4 | G9 的容器 `delete` 属破坏性操作，是否进 Web 需与"未推送代码不得丢失"的既有约束一起评审 |
| R5 | 前端 `Task` 类型缺 `final_result` / `closeout` 属类型漂移，说明前后端字段同步缺少检查手段；是否引入类型生成需评估 |
| R6 | `WorkflowStepResponse.started_at` / `finished_at` 长期为空，可能已有其他读取方假设它为空，补全前需确认（**P4-M3-6 已补全**：P4-M3-6 交付文档 §7 记录了补全后的语义与风险） |
| R7 | 本次未执行任何测试，G1 的"必然 409"结论来自 `CheckWorkflowStart` 的静态判定；若存在绕过路径（例如任务在创建时即带容器），需在 M2 实施时复核 |

## 11. 待调查事项（保持未决，不写成结论）

1. 是否存在本仓库之外的部署流程会向 `roles` 表写入角色（影响 G2 的严重度分级，不影响"缺少
   正式入口"的结论）。
2. Web 是否需要"创建环境版本 / 触发镜像构建"的写入口，还是只读选择即可（G3 范围）。
3. 项目（G12）是否真的需要独立实体，还是继续以"受校验的标识 + 缓存范围"存在即可。
4. 容器 `delete` / `detach` 是否应进 Web，以及需要哪些额外确认（G9）。
5. 修复周期历史（G16）与事件时间线（G15）在 M3 中是否同批交付。
