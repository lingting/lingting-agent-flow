# P4-M2-3 —— Web 接入统一任务启动与环境选择

> 实施依据：`master` 上 `internal/httpapi/{task_launch,environments,router,tasks}.go`、
> `internal/app/{task_launch,task_launch_run,environments_query}.go`、
> `internal/domain/{task_launch,task_queue,task,step_execution,environment}.go`
> 与 `lingting-agent-flow-ui/src/**`、`lingting-agent-flow-ui/tests/**` 的实际代码核实。
> 对应 P4-M1 缺口 **G1**（统一启动未接入 Web）与 **G3 的前端部分**（环境查询未接入）。

## 1. 结论摘要

本轮闭合 G1：Web 的**新任务启动入口**从旧的 `POST /tasks/{id}/execute-workflow`
切换为统一启动 `POST /tasks/{id}/start`，并在任务创建弹窗中接入 M2-2 的环境查询接口，
用服务端真实数据替换手填的环境名称与版本。

交付内容：

1. API 层新增 `startTask`（`POST /api/v1/tasks/{id}/start`，接受 `StartTaskRequest`
   真实契约，不发送容器 / 工作目录 / 步骤 / 角色覆盖），以及
   `fetchEnvironments` / `fetchEnvironmentVersion`；
2. 任务页"启动"按钮改走统一启动，`execute-workflow` 不再作为新任务启动入口；
3. 启动结果按 `workflow_started` 区分"已启动"与"未启动/被阻塞"，失败不伪造运行状态；
4. 任务创建弹窗的语言环境与版本改为服务端数据驱动的选择器；
5. 三层测试（API 层单元测试、页面逻辑单元测试、真实浏览器 E2E）。

本轮**未实现**：角色与执行节点的正式选择器（M2-4）、环境构建 Web 入口、环境 CRUD、
后端任何启动语义或租约改动、UI 视觉重构。

## 2. 后端契约（只读核实，未修改）

### 2.1 统一启动

```text
POST /api/v1/tasks/{taskID}/start
```

请求体 `httpapi.StartTaskRequest`：`prompt`、`timeout_seconds`、
`validation_timeout_seconds`、`plan_timeout_seconds`。**不接受**容器 ID、工作目录、
步骤列表、Session 路径或角色覆盖。服务端先经 `TaskLeaseService.Claim` 领取任务租约
（`domain.ExplicitLaunchStatuses()`），再执行 `app.TaskLaunchService.Launch`。

成功时返回 `202 Accepted` + `httpapi.StartTaskResponse`：

| 字段 | 语义 |
|---|---|
| `stage` | 本次启动最后进入的阶段：`validate` / `container` / `workspace` / `plan` / `workflow` |
| `task_status` | 收尾后读取到的任务真实状态（来自数据库，不由前端推断） |
| `container_id` / `container_prepared` | 本次使用的容器记录与"是否真的创建了容器" |
| `workspace_status` / `workspace_commit` | 任务级工作区初始化状态与核验到的 commit；只有 `succeeded` / `skipped` 表示代码就绪 |
| `plan_persisted` / `plan_steps` | 计划是否已完整落库，以及已持久化的步骤 |
| `workflow_started` / `run` | **工作流是否真的启动**，以及启动后的执行记录 |
| `failure_stage` / `failure` | 失败阶段与脱敏原因摘要 |

失败时服务端返回非 2xx 与统一错误结构（400 / 404 / 409 / 412 / 500 / 503），
因此客户端不会拿到"伪造的成功响应"。`202` 表示"已受理并得出真实结论"，不表示任务完成。

### 2.2 环境查询

```text
GET /api/v1/environments?host_id=<host-id>
GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=<host-id>
```

契约与字段语义见 [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)。
本轮只消费：`type`、`name`、`current_version`、`latest_version`、`versions[]` 的
`version` / `number` / `build_status` / `usable` / `image_ref` / `error_summary`。

**`usable` 与 `current_version` 都由服务端计算**，前端不从版本号、`image_ref` 或构建
时间推断可用性，也不把 `latest_version` 当成可用版本。

### 2.3 准入规则（前端复刻的唯一依据）

`domain.TaskStatus.AllowsExplicitLaunch` = `CanStart() && status != failed`，即
`queued`、`preparing`、`planned`、`retrying`、`paused`、`recovery_required`。
`failed` 的重新开始属于独立的显式重试入口，不由统一启动隐式承担。

前端 `canStartTask` 与该集合逐项对齐，另加 `cancel_requested` / `status_finished`
两项排除。前端禁用只是提示：真正的并发与准入保护仍在服务端任务租约与状态机。

## 3. 代码证据

| 关注点 | 位置 |
|---|---|
| 统一启动请求封装 | `lingting-agent-flow-ui/src/lib/api/endpoints.ts` 的 `taskStartPath` / `startTask` |
| 环境查询封装与可用版本选择 | 同文件的 `ENVIRONMENTS_PATH`、`environmentVersionPath`、`fetchEnvironments`、`fetchEnvironmentVersion`、`usableEnvironmentVersions`、`defaultUsableVersion` |
| 响应类型 | `src/lib/api/types.ts` 的 `TaskLaunchStage`、`TaskWorkspaceInitStatus`、`PlanStep`、`StartTaskRequest`、`StartTaskResponse`、`EnvironmentType`、`BuildStatus`、`EnvironmentVersion`、`Environment`、`EnvironmentListResponse` |
| 启动阶段与结论判定 | `src/pages/tasks/launch.ts` 的 `TASK_LAUNCH_STAGE_META`、`launchStageMeta`、`workspaceStatusLabel`、`workspaceReady`、`canStartTask`、`describeLaunchResult` |
| 环境选择与提交校验 | `src/pages/tasks/environments.ts` 的 `taskEnvironments`、`usableVersions`、`environmentSelectOptions`、`versionSelectOptions`、`resolveEnvironmentSelection` |
| 环境查询状态机 | `src/pages/tasks/useEnvironments.ts` 的 `useEnvironments`（`idle` / `loading` / `ready` / `error`） |
| 环境选择器组件 | `src/pages/tasks/EnvironmentSelector.tsx` |
| 任务页启动入口 | `src/pages/tasks/TasksPage.tsx` 的 `handleStartTask`、`refreshAfterLaunch`、启动按钮与结果提示 |
| 创建弹窗环境选择 | `src/pages/tasks/CreateTaskModal.tsx` 的 `handleValuesChange`、`handleEnvironmentChange`、`resolveEnvironmentSelection` 校验 |

### 3.1 启动调用链

```text
TasksPage "启动" 按钮
  → canStartTask(record)                 （与服务端 AllowsExplicitLaunch 对齐的可用性）
  → handleStartTask
      → startTask(taskID)                POST /api/v1/tasks/{taskID}/start
      → describeLaunchResult(response)   按 workflow_started 区分已启动 / 未启动
      → refreshAfterLaunch(taskID)
          → fetchTask                    GET /api/v1/tasks/{taskID}
          → ExTable reload               GET /api/v1/tasks?current=…&size=…
          → fetchTaskWorkflow            GET /api/v1/tasks/{taskID}/workflow
```

`startTaskWorkflow`（`POST /tasks/{id}/execute-workflow`）仍保留在 API 层，因为它
继续推进"已有容器与已持久化步骤"的既有现场时仍是真实接口；但它**不再被任务页的
新任务启动流程调用**。E2E 用例 `新任务通过 /start 启动，并刷新任务与工作流状态`
显式拦截 `/execute-workflow` 并断言零调用。

### 3.2 启动结果的三态处理

| 情形 | 判定依据 | UI 行为 |
|---|---|---|
| 启动成功 | `workflow_started=true` | 成功提示，展示服务端返回的阶段、任务状态、容器、工作区、计划与"工作流已启动" |
| 未完成或被阻塞 | 请求成功但 `workflow_started=false` | 警告提示，展示阶段、真实任务状态、`failure_stage` 与 `failure`，明确写"工作流未启动" |
| HTTP 请求失败 | 非 2xx 的 `ApiError` | 展示 `ApiError.message`（服务端业务提示）与错误代码对应的说明，不根据前端预期推断状态 |
| 状态刷新失败 | `fetchTask` / `fetchTaskWorkflow` 失败 | 单独提示"状态刷新失败"，**不抹除**已经得到的启动结论 |

`202` 由 `apiRequest` 正常按 `response.ok` 处理，因此不会因为"不是 200"而误报失败。
启动请求不设客户端超时（`timeoutMs: options.timeoutMs ?? 0`）：该流程包含远程容器
准备与 Planner 调用，客户端提前断开只会留下无法确认的现场；超时与取消由服务端流程
自身管理。

### 3.3 环境选择行为

| 场景 | 行为 |
|---|---|
| 执行节点未填写 | 不发起请求（`host_id` 缺失会得到 400），显示"尚未填写执行节点" |
| 执行节点填写 / 变化 | 去抖 350ms 后查询；**先清空**环境与版本选择，避免跨节点误选 |
| 查询中 | 显示"正在查询该执行节点上的环境…" |
| 查询失败 | 显示 `ApiError` 的业务提示，**不伪装成空列表** |
| 空列表 | 显示"没有可用的环境"并提示先用 CLI 生成版本 |
| 只有基础环境 / 全部版本不可用 | 分别显示对应警告，明确"暂不可用于任务启动" |
| 多个环境与多个版本 | 环境选项展示"当前可用 vXXXX · 最新创建 vYYYY"；版本选项展示版本、构建状态与可用性 |
| 不可用版本 | 展示但 `disabled`，`title` 给出 `error_summary` |
| 提交前校验 | `resolveEnvironmentSelection` 逐项拒绝：未加载、未选环境、环境不在当前节点、无可用版本、未选版本、版本不属于该环境、版本不可用 |

环境 `type` / `name` 与版本号映射到 `CreateTaskRequest` 的 `environment` /
`environment_version`（后端 `CreateTaskRequest` 只接受语言环境名称与版本标签，
`app.TaskService` 用 `domain.EnvironmentTypeLanguage` 构造引用）。基础环境
（`type=base`）不进入任务环境选择。

### 3.4 创建与启动的边界

- 点击"创建"只调用 `POST /api/v1/tasks`，不触发执行；创建成功后关闭弹窗并刷新列表。
- 启动只由用户在任务列表显式点击"启动"触发。
- 启动请求进行中按钮变为"启动中"并禁用；`launchingRef` 做同帧防重复提交。
- 启动失败不自动重试、不自动重新创建任务、不发出 `execute-workflow` 请求。
- 前端不写 `status` / `started_at` 等执行期字段，也不绕过后端租约。

## 4. 测试证据

### 4.1 前端单元测试（`pnpm test` → `vp test run`）

`tests/unit/task-launch-api.test.ts`（20 个用例）：统一启动的路径 / 方法 / 请求体、
不发送容器与角色覆盖、`202` 视为成功、正常 JSON 解析、可选字段缺失不崩溃、
409 / 412 结构化错误转 `ApiError`、网络错误与调用方超时、启动路径编码；
环境列表 `host_id` 编码、空节点不请求、单版本路径与参数编码、非法版本号拒绝、
404 保留业务提示、空列表是合法结果、缺省可选字段解析、
`usable` 过滤与 `current_version` 优先且不退回到最新创建版本。

`tests/unit/task-launch-state.test.ts`（14 个用例）：`canStartTask` 与
`AllowsExplicitLaunch` 逐项对齐（含 `failed` 不可启动、已请求取消不可启动）、
阶段元数据覆盖全部取值与未知阶段原样展示、工作区只有 `succeeded` / `skipped` 就绪、
`workflow_started=true` 才算启动成功、`workflow_started=false` 不显示为成功、
计划已持久化与容器已准备都不能替代 `workflow_started`、失败摘要缺失时的明确说明、
展示真实任务状态而不从阶段推断成功。

`tests/unit/task-environments.test.ts`（14 个用例）：只取语言环境、多环境多版本、
`current_version` 与 `latest_version` 区分、无可用版本时禁用并给出原因、
构建失败 / 构建中的真实状态与诊断摘要、未知构建状态原样展示、按名称查找；
提交前校验的 7 条拒绝路径与 1 条成功映射。

### 4.2 浏览器 E2E（`pnpm exec playwright test`）

`tests/e2e/task-launch.spec.ts`（12 个用例）：

| 用例 | 覆盖行为 |
|---|---|
| 新任务通过 `/start` 启动，并刷新任务与工作流状态 | 走 `/start`；`/execute-workflow` 零调用；启动后展示服务端返回的 `running` |
| 启动过程中重复点击被阻止 | 请求未完成时按钮为"启动中"且只发出一次请求 |
| `workflow_started=false` 不显示为启动成功 | 显示"启动流程未完成，工作流未启动"与服务端失败摘要 |
| 启动请求失败时展示服务端错误且不伪造 running 状态 | 409 展示业务提示，任务仍为"等待执行" |
| 状态刷新失败不会抹除已收到的启动结论 | 详情与工作流查询均 500 时，启动结论仍在，另给"状态刷新失败"提示 |
| 不自动重试启动请求 | 500 后 500ms 内仍只有一次请求 |
| `failed` 任务不显示启动入口 | 启动按钮为 disabled |
| 执行节点变化时重新查询环境并清空旧选择 | `host_id` 从 host-1 变为 host-2，旧选择被清除 |
| 环境查询失败不被伪装成空列表 | 404 展示"环境查询失败"与服务端提示，不出现"没有可用的环境" |
| 空环境列表显示明确的空状态 | 显示"没有可用的环境" |
| 没有可用版本的环境被禁用且不能提交 | 显示"没有可用的环境版本"，提交被 `resolveEnvironmentSelection` 阻止 |
| 提交请求使用实际选中的环境与版本 | `POST /tasks` 的 `environment=go`、`environment_version=v0002`，不含 `status` / `started_at` |

`tests/e2e/tasks.spec.ts` 的"创建任务后刷新真实列表"已同步改为通过环境选择器选择
环境与版本（原先手填 `语言环境`）。

### 4.3 本轮实际执行结果

| 命令 | 结果 |
|---|---|
| `npx vitest run`（10 个测试文件） | 通过，107 个用例 |
| `npx playwright test`（全部 E2E） | 40 通过 / 3 失败；3 个失败在**未改动的 `master` 工作区同样失败**（见 §6.1） |
| `npx playwright test tests/e2e/task-launch.spec.ts` | 通过，12 个用例 |
| `npx tsc --noEmit` | 无错误 |
| `npx vp lint`（等价于 `pnpm lint` 的 `vp lint --fix` + 类型检查） | 无告警 |
| `npx vp fmt` | 通过（65 个文件） |
| `npx vp build` / `npm run build` | 通过，产物写入 `internal/webui/dist` |

测试产物保留在 `os.TempDir()/lingting-agent-flow-ui-tests/<run-*>` 与
`os.TempDir()/lingting-agent-flow-tests/` 下，未自动清理；每次运行输出
`TEST_ARTIFACT_DIR`。

**后端未修改**，因此未重新运行后端测试；本轮所有后端结论来自静态代码核实。

## 5. 安全与边界

- 前端不提交容器 ID、工作目录、步骤列表、Session 路径或角色覆盖；请求体只含
  `StartTaskRequest` 声明的执行参数。
- 不绕过后端任务租约：`/start` 的租约领取与释放完全由服务端 `taskLaunchHandler` 完成。
- 环境查询是只读路径：不建立 SSH / Docker 连接，不产生远程副作用。
- 前端不写状态字段，不做乐观状态更新，不把 `202`、计划持久化或容器创建当作"已完成"。
- 未新增后端能力、未新增环境表或迁移、未改动启动服务与调度器语义。

## 6. 剩余风险与未覆盖场景

### 6.1 与既有失败用例的关系（如实记录）

`npx playwright test` 全量运行时有 3 个用例失败，且在**把本轮改动全部 stash 后的
`master` 工作区同样失败**，因此不是本轮引入：

| 失败用例 | 说明 |
|---|---|
| `tests/e2e/layout.spec.ts` 的 `/machines 可访问且导航高亮、页面标题一致` | 断言 `toContainText` 失败，与本轮任务页改动无关 |
| `tests/e2e/layout.spec.ts` 的 `刷新后路由与导航状态保持` | 同上 |
| `tests/e2e/tasks.spec.ts` 的 `详情返回 404 时展示错误而不是伪造任务` | 断言 `任务不存在: task-1` 可见失败 |

第三个用例与本轮改动的任务页相关（同一文件），但已用 stash 对照确认在未改动的工作区
同样失败；根因未进一步追查，应作为独立任务处理。本轮未修改其代码或断言。

### 6.2 功能边界

1. **执行节点与角色仍是手填标识**：`fetchHosts` 与 `GET /api/v1/roles` 已具备，但正式
   选择器与禁用角色过滤归 **P4-M2-4**，本轮未做。
2. **环境构建入口仍只有 CLI**：`POST …/build` 未实现。全新部署必须先执行
   `agentflow docker init` 与 `docker env build`，否则环境选择器会显示"没有可用的环境"。
   这是真实状态，不是前端缺陷。
3. **`current_version` 不保证镜像仍在远程存在**：它表示"最后一个构建成功且已核实镜像
   ID 的版本"，真正可用性仍在容器创建时判定。因此环境选择通过后 `/start` 仍可能在
   `container` 阶段失败，此时页面按"未启动/被阻塞"展示。
4. **执行节点输入使用去抖 350ms**：手工输入时中间值不发起请求；若用户输入过快，
   最后一个值仍会被查询。该去抖只影响请求数量，不影响正确性。
5. **启动请求无客户端超时**：长时间启动（容器准备 + Planner）期间页面只有"启动中"
   反馈，没有进度百分比。真实进度由 `GET /tasks/{id}/workflow` 提供，自动轮询归 M3-7。
6. **未覆盖场景**：`workspace_status=uncertain` 与 `recovery_required` 的端到端
   UI 流程未单独构造 E2E；`timeout` 类错误只做 API 层单元测试，未在浏览器层模拟；
   并发多标签页同时启动同一任务未验证（服务端租约是最终防线，前端只防同页重复提交）。
7. **未做视觉重构**：本轮只保证业务链路正确，未调整页面布局与样式。

## 7. 退出条件核对

| 条件 | 状态 |
|---|---|
| 新任务可通过 Web 显式启动（`POST /tasks/{id}/start`） | 满足（`新任务通过 /start 启动…`） |
| `execute-workflow` 不再是新任务启动入口 | 满足（E2E 断言零调用） |
| 启动状态不被伪造：`workflow_started=false` 不显示为成功 | 满足（`workflow_started=false 不显示为启动成功`、`describeLaunchResult` 单测） |
| 启动失败保留真实任务状态、不乐观改为运行中 | 满足（`启动请求失败时…不伪造 running 状态`） |
| 刷新失败不抹除已得到的启动结果 | 满足（`状态刷新失败不会抹除已收到的启动结论`） |
| 启动期间阻止重复提交 | 满足（`启动过程中重复点击被阻止`） |
| 不自动重试启动请求 | 满足（`不自动重试启动请求`） |
| 环境选择使用真实后端数据，节点变化重新加载并清除旧选择 | 满足（`执行节点变化时重新查询环境并清空旧选择`） |
| 加载中 / 失败 / 空列表分别展示，错误不伪装为空列表 | 满足（`环境查询失败不被伪装成空列表`、`空环境列表显示明确的空状态`） |
| 只有 `usable=true` 的版本可被选择，不可用版本展示状态但禁用 | 满足（`没有可用版本的环境被禁用且不能提交`、环境单测） |
| `current_version` 与 `latest_version` 语义不混用 | 满足（环境单测 `current_version 与 latest_version 不同时…`） |
| 无合法可用版本时不能提交伪造配置 | 满足（`resolveEnvironmentSelection` 单测 + E2E 提交被阻止） |
| 创建与启动是两个明确动作，创建不自动启动 | 满足（`CreateTaskModal` 只调 `createTask`） |
| 前端不提交容器 / 工作目录 / 步骤 / 角色覆盖 | 满足（API 单测断言请求体只有执行参数） |
| 测试与检查命令真实执行并记录 | 满足（§4.3，含未通过项的如实说明） |

结论：M2-3 标记为 **`completed`**。未完成项（角色与节点选择器、环境构建入口、
进度自动刷新）已明确归属 M2-4 与 M3，不属于本阶段退出条件。

## 8. 下一阶段

**P4-M2-4：创建任务弹窗复用 `fetchHosts` 与 `GET /api/v1/roles`，移除手填的执行节点
与角色标识**（覆盖 G4 与 G2 的前端部分），包括执行节点可用性展示与禁用角色过滤。
环境构建/重建入口是否上 Web，在 M2-4 之后按产品取舍决定。
