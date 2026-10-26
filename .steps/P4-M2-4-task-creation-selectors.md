# P4-M2-4 —— 任务创建表单接入执行节点与角色选择器

> 实施依据：`master` 上 `internal/httpapi/{roles,hosts,tasks}.go`、
> `internal/app/{role_query,task,docker_session}.go`、`internal/domain/role.go`
> 与 `lingting-agent-flow-ui/src/**`、`lingting-agent-flow-ui/tests/**` 的实际代码核实。
> 对应 P4-M1 缺口 **G4**（执行节点未复用已有接口）与 **G2 的前端部分**（角色选择器）。

## 1. 结论摘要

本轮把创建任务弹窗中手填的三类外部标识全部改为真实接口驱动的选择器：

1. **执行节点**：复用既有 `fetchHosts()` 与 `HostListResponse`，移除手填 ID；
2. **角色**：新增 `fetchRoles()` 与 `Role` / `RoleListResponse` / `RoleType` 类型，
   按 `type` 分类、按 `enabled` 过滤；
3. **环境与版本**：沿用 M2-3 的环境查询，但查询触发点从"手填节点文本"改为
   "选择器选中节点"，并去掉不再需要的 350ms 去抖。

交付内容：

1. API 层新增 `ROLES_PATH`、`fetchRoles()`，类型新增 `RoleType`、`Role`、
   `RoleListResponse`（字段严格对齐 `httpapi.RoleResponse`）；
2. 新增 `src/pages/tasks/selectors.ts`：节点与角色的选项构造、可用性判定、
   提交前校验（纯函数）；
3. 新增 `src/pages/tasks/useHosts.ts` 与 `src/pages/tasks/useRoles.ts`：
   `loading` / `ready` / `error` 三态查询，失败不伪装成空列表，带重试入口；
4. `CreateTaskModal` 改为三个选择器 + 加载/空/失败状态 + 完整提交门控；
5. 三层测试：API 层单元测试、选择器与表单逻辑单元测试、真实浏览器 E2E。

本轮**未实现**：环境构建/重建 Web 入口、环境 CRUD、角色 CRUD 与角色同步、
任务进度自动刷新、验证历史与执行记录视图、任何后端改动。

## 2. 后端契约（只读核实，未修改）

### 2.1 角色列表

```text
GET /api/v1/roles
```

响应 `httpapi.RoleListResponse`：

| 字段 | 语义 |
|---|---|
| `records[].id` | 角色真实标识，提交到 `*_role_id` 的值 |
| `records[].name` | 角色名称，选择器展示用 |
| `records[].type` | `planner` / `developer` / `validator`（`domain.RoleType`） |
| `records[].runner` | 执行器标识，仅作辅助信息 |
| `records[].model` | 模型标识，仅作辅助信息 |
| `records[].enabled` | 是否启用；**已禁用角色如实返回 `false`** |
| `total` | 角色总数 |

`system_prompt`、工具策略与时间戳不在响应中（`internal/httpapi/roles.go` 明确只暴露
选择所需的最小字段），前端也不请求、不展示。服务未初始化时返回 503，存储错误不伪装成
空列表。

### 2.2 执行节点列表

```text
GET /api/v1/hosts
```

复用既有 `httpapi.HostResponse`。选择器只用其中的 `id`、`name`、`host`、`port`、
`username`、`is_default`、`enabled`、`validation_status`、`validation_message`、
`usable_for_remote_task`；`private_key_path`、指纹等与"选哪个节点"无关的字段不展示。

`usable_for_remote_task` 由后端 `app.EnsureHostUsableForRemoteTask` 计算
（`internal/app/docker_session.go`：`enabled` 且 `validation_status == normal`）。
前端不复制该规则，只消费服务端结论。

### 2.3 创建任务的准入规则（前端复刻的唯一依据）

`internal/app/task.go`：

- `resolveHost`：节点必须存在且 `enabled`；
- `ensureRoles`：规划 / 开发角色必须存在、`type` 匹配、`enabled`；验证角色仅在非空时校验。

因此前端门控与后端一致：节点必须 `enabled && usable_for_remote_task`，
角色必须 `enabled` 且 `type` 匹配。前端禁用只是提示，服务端仍是最终权威。

`CreateTaskRequest` 中与本轮相关的字段未变：`host_id`、`planner_role_id`、
`developer_role_id`、可选 `validator_role_id`、`environment`、`environment_version`。

## 3. 代码证据

| 关注点 | 位置 |
|---|---|
| 角色接口封装 | `src/lib/api/endpoints.ts` 的 `ROLES_PATH`、`fetchRoles` |
| 角色类型 | `src/lib/api/types.ts` 的 `RoleType`、`Role`、`RoleListResponse` |
| 选择规则与校验 | `src/pages/tasks/selectors.ts` 的 `ROLE_TYPE_META`、`enabledRolesOfType`、`isEnabledRoleOfType`、`roleSelectOptions`、`hostUnavailableReason`、`hostSelectable`、`hostSelectOptions`、`findHost`、`resolveRoleSelection`、`resolveHostSelection` |
| 节点查询状态机 | `src/pages/tasks/useHosts.ts` 的 `useHosts`（`loading` / `ready` / `error` + `reload`） |
| 角色查询状态机 | `src/pages/tasks/useRoles.ts` 的 `useRoles`（同上） |
| 环境查询 | `src/pages/tasks/useEnvironments.ts`（去掉手填去抖，节点来自选择器） |
| 创建表单 | `src/pages/tasks/CreateTaskModal.tsx` 的三个选择器、状态提示与 `handleOk` 门控 |

### 3.1 提交流程

```text
handleOk
  → form.validateFields()                       表单必填校验
  → resolveHostSelection({ hosts, loaded, hostID })     节点已加载 + 已选 + 可用
  → resolveRoleSelection({ roles, loaded, … })          角色已加载 + 类型匹配 + 启用
  → resolveEnvironmentSelection({ environments, loaded, … })   环境属于当前节点且版本可用
  → toCreateTaskRequest(…选择器返回的真实标识…)
  → createTask()                                 POST /api/v1/tasks
```

任一步失败都给出可读原因并阻止提交，不发出创建请求。展示值与提交值分离：
下拉展示名称，请求体提交真实 `id` / 环境名称 / 版本标签。

### 3.2 三种查询的状态语义

| 状态 | 节点 | 角色 | 环境 |
|---|---|---|---|
| 加载中 | "正在加载执行节点…" | "正在加载角色…" | "正在查询该执行节点上的环境…" |
| 失败 | "执行节点查询失败" + `ApiError` 提示 + 重试 | "角色查询失败" + 提示 + 重试 | "环境查询失败" + 提示 |
| 空结果 | "没有可用的执行节点" | "没有可用的角色" | "没有可用的环境" |
| 类别缺失 | 全部节点不可用时下拉整体禁用 | "缺少启用的规划角色 / 开发角色"；验证角色缺失提示为 info | "没有可用的环境版本" |

请求失败与空列表是不同状态，互不伪装。节点切换时清空 `environment` 与
`environment_version`，并针对新节点重新查询（`useEnvironments` 以 `host_id` 为依赖）。

### 3.3 角色与节点的展示规则

- 角色选项：启用角色展示"名称（模型 · 执行器）"；禁用角色同样展示但追加"已禁用"并被
  `disabled`，让用户能看出"角色存在但被禁用"。
- 节点选项：展示"名称 · 用户@主机:端口 · 默认节点（若有）· 不可用原因（若有）"；
  不可用节点被 `disabled`，`title` 给出原因。
- 未选择任何角色：不静默填入默认角色 ID，用户必须显式选择。

## 4. 测试证据

### 4.1 前端单元测试（`pnpm test` → `vp test run`）

新增 `tests/unit/task-selectors-api.test.ts`（12 个用例）：

- `fetchRoles()` 使用 `GET /api/v1/roles`，无请求体；
- 解析 `records` / `total` / 每个角色的 `enabled`，断言响应中不含 `system_prompt`；
- 空列表是合法结果；
- 503 保留业务提示、网络错误为 `network`、调用方取消为 `aborted`；
- `fetchHosts()` 返回的 `id` / `name` / `host` / `port` / `enabled` /
  `usable_for_remote_task` / `validation_status` / `validation_message` 可用于选择器；
- 节点空列表合法、500 保留业务提示。

新增 `tests/unit/task-selectors.test.ts`（21 个用例）：

- 角色按 `type` 分类，`enabled=false` 不进入可用集合，仍作为禁用选项展示；
- 提交值是角色真实 `id` 而非名称；
- 节点启用状态与 `usable_for_remote_task` 决定选择资格，`checking` 不显示为可用；
- 选项不包含私钥路径；
- `resolveHostSelection` 的 6 条拒绝路径与 1 条成功映射；
- `resolveRoleSelection` 的 8 条拒绝路径与 2 条成功映射；
- 验证角色未选择时不产生 `validator_role_id` 键（不发送空字符串）。

### 4.2 浏览器 E2E（`pnpm exec playwright test`）

新增 `tests/e2e/task-create-selectors.spec.ts`（10 个用例）：

| 用例 | 覆盖行为 |
|---|---|
| 节点与角色从真实接口加载，用户可以完成选择并提交真实标识 | `/hosts`、`/roles` 被请求；payload 的 `host_id`、三个角色 ID、环境与版本均为选择器返回的真实值 |
| 禁用角色不能选中 | 禁用选项带 `ant-select-item-option-disabled`，点击不写入选择值 |
| 不可用节点不能用于提交 | 唯一节点未通过预检时节点下拉整体禁用，提交被阻止且无创建请求 |
| 切换执行节点清除已有环境与版本选择，并重新查询新节点 | `host_id` 由 host-1 变为 host-2，旧选择被清除 |
| 节点接口返回错误时显示可理解的错误状态而不是空列表 | 显示"执行节点查询失败"+ 服务端提示，不出现"没有可用的执行节点" |
| 角色接口返回错误时显示可理解的错误状态而不是没有角色 | 显示"角色查询失败"+ 提示，不出现"没有可用的角色" |
| 没有可用节点时用户不能创建任务 | 提交被阻止，无创建请求 |
| 缺少启用的必需角色时不能创建任务 | 显示"缺少启用的规划角色 / 开发角色"，无创建请求 |
| 创建请求失败时保留真实错误且不提示创建成功 | 400 提示保留，弹窗保持打开，未出现新任务 |
| 创建成功后只刷新任务列表，不自动启动任务 | 列表重新请求；`/start` 零调用 |

同时更新既有测试：

- `tests/e2e/task-launch.spec.ts` 的"创建任务的环境选择"4 个用例改为通过节点与角色
  选择器操作（新增 `sampleHost` / `sampleRole` / `stubHosts` / `stubRoles` /
  `selectHost` / `selectRole` 辅助函数）；
- `tests/e2e/tasks.spec.ts` 的"创建任务后刷新真实列表"同步改为选择器操作。

### 4.3 本轮实际执行结果

| 命令 | 结果 |
|---|---|
| `npx vitest run`（12 个测试文件） | 通过，140 个用例 |
| `npx playwright test tests/e2e/task-create-selectors.spec.ts` | 通过，10 个用例 |
| `npx playwright test`（全部 E2E） | 50 通过 / 3 失败；3 个失败在**未改动的 `master` 工作区同样失败**（见 §6.1） |
| `npx tsc --noEmit` | 无错误 |
| `npx vp lint --fix` | 无告警 |
| `npx vp fmt` | 通过（71 个文件） |
| `pnpm build` | 通过，产物写入 `internal/webui/dist` 并补回 `.gitkeep` |

测试产物保留在 `os.TempDir()/lingting-agent-flow-ui-tests/<run-*>` 下，未自动清理；
每次运行输出 `TEST_ARTIFACT_DIR`。

**后端未修改**，因此未运行后端测试；本轮所有后端结论来自静态代码核实。

## 5. 安全与边界

- 前端不请求、不展示角色 `system_prompt`、工具策略与凭据，也不展示节点私钥路径；
- 前端不新增节点或角色的可用性判定规则，只消费 `enabled` 与
  `usable_for_remote_task`；最终裁决仍在服务端；
- 不提交 `status`、`started_at`、`finished_at` 等执行期字段，创建不触发执行；
- 创建成功后只刷新列表，不自动调用 `/start`，也不调用旧的 `execute-workflow`；
- 未新增后端能力、未新增迁移、未改动角色或节点的业务契约。

## 6. 剩余风险与未覆盖场景

### 6.1 与既有失败用例的关系（如实记录）

`npx playwright test` 全量运行时有 3 个用例失败，且在**把本轮改动全部 stash 后的
`master` 工作区同样失败**，因此不是本轮引入：

| 失败用例 | 说明 |
|---|---|
| `tests/e2e/layout.spec.ts` 的 `/machines 可访问且导航高亮、页面标题一致` | `toContainText` 失败，与任务创建无关；已用 stash 对照确认 |
| `tests/e2e/layout.spec.ts` 的 `刷新后路由与导航状态保持` | 同上 |
| `tests/e2e/tasks.spec.ts` 的 `详情返回 404 时展示错误而不是伪造任务` | `任务不存在: task-1` 未出现；已用 stash 对照确认在未改动工作区同样失败 |

三者与 M2-3 §6.1 记录的失败完全一致，本轮未修改其代码或断言，根因未进一步追查，
应作为独立任务处理。

### 6.2 功能边界

1. **环境构建入口仍只有 CLI**：`POST …/build` 未实现，全新部署仍须先执行
   `agentflow docker init` 与 `docker env build`，否则环境选择器显示"没有可用的环境"。
2. **节点列表不分页**：`GET /api/v1/hosts` 返回全部节点，节点数量极大时下拉会变长；
   当前部署形态下不构成阻塞。
3. **角色变更入口仍缺失**：没有角色 CRUD 或显式同步，禁用角色只能通过数据库或后续
   里程碑补齐的入口处理；本轮的职责只是如实过滤。
4. **`usable_for_remote_task` 不保证镜像仍存在**：它表示节点环境预检通过，
   容器创建仍可能在 `/start` 的 `container` 阶段失败，此时页面按"未启动/被阻塞"展示。
5. **未覆盖场景**：并发打开多个弹窗同时创建未验证（服务端仍会校验）；
   节点或角色列表在弹窗打开期间发生变化（如角色被禁用）只会在提交时被服务端拒绝，
   前端不做轮询。
6. **未做视觉重构**：本轮只保证业务链路正确，未调整弹窗布局与样式。

## 7. 退出条件核对

| 条件 | 状态 |
|---|---|
| 角色列表 API 封装复用 `apiRequest` 与 `ApiError`，未新增请求框架 | 满足 |
| 角色类型按实际响应定义（`id`/`name`/`type`/`runner`/`model`/`enabled`） | 满足 |
| 列表响应使用真实 `records` 与 `total` | 满足（API 单测） |
| 不请求或展示 `system_prompt`、凭据 | 满足（API 单测断言 + 后端契约核实） |
| 移除手填执行节点 ID，改用 `fetchHosts()` 选择器 | 满足（`CreateTaskModal` 无 `执行节点标识` 输入） |
| 选择器展示名称、地址与端口，不展示私钥路径 | 满足（选择器单测） |
| 禁用节点与未通过预检的节点不可选，并给出真实原因 | 满足（单测 + E2E） |
| 节点变化清除旧环境与版本并重新查询 | 满足（E2E `切换执行节点…`） |
| 规划 / 开发 / 验证角色按 `type` 分类选择 | 满足（单测 + E2E） |
| `enabled=false` 的角色不进入可选项 | 满足（单测 + E2E） |
| 角色加载中 / 失败 / 空列表 / 某类无启用角色分别呈现 | 满足（E2E 两个错误用例 + 缺少必需角色用例） |
| 必需角色缺失时不能提交 | 满足（单测 + E2E） |
| 查询失败不伪装为空列表，不使用硬编码节点或角色 | 满足（E2E 两个错误用例） |
| 加载、成功、空结果、失败四种状态明确区分 | 满足（§3.2 表格与对应 E2E） |
| 查询失败提供重试入口 | 满足（`useHosts.reload` / `useRoles.reload` + Alert action） |
| 提交请求使用真实 `host_id` 与角色 `id` | 满足（E2E payload 断言） |
| 验证角色未选择时省略该字段 | 满足（单测 + `toCreateTaskRequest`） |
| 创建请求不含状态、开始时间等执行期字段 | 满足（既有单测 + E2E） |
| 创建与启动仍是两个显式动作 | 满足（E2E `/start` 零调用） |
| 前端禁用不作为唯一防线 | 满足（服务端 `resolveHost` / `ensureRoles` 仍校验） |
| 测试与检查命令真实执行并记录 | 满足（§4.3，含未通过项的如实说明） |

结论：M2-4 标记为 **`completed`**。未完成项（环境构建 Web 入口、角色 CRUD、
进度自动刷新、既有 3 个基线失败用例）已明确归属后续里程碑或独立任务。

## 8. 下一阶段

**P4-M2-5：验证失败反馈查询 API**（存储层 `validation_feedbacks` 已具备，缺 HTTP 查询
入口），覆盖 P4-M1 缺口 **G6 的 API 部分**。环境构建/重建入口是否上 Web 仍在 M2 之后
按产品取舍决定。
