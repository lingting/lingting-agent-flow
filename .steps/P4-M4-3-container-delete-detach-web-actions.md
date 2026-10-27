# P4-M4-3：容器 delete / detach 的 HTTP API 与 Web 操作入口（G9）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M4`（支持必要的继续执行、取消、恢复和结果查看操作）
- **覆盖缺口**：G9「处置旧容器（`delete` / `detach`）以便重新开始」
- **范围**：补齐两个缺失的 HTTP 接口与任务详情中的显式操作入口。**本轮修改了 Go 生产代码**：
  新增只读查询 + 显式处置的应用层服务与两个 HTTP 路由，并新增一个纯函数领域判定。
  没有重写容器生命周期、没有新增第二套任务重启工作流。

---

## 1. 实施前后的基线 SHA 与工作区状态

| 阶段 | 分支 | HEAD SHA | 工作区 |
|---|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致） | `074943663408b6b61cf00558f809a05a0c2a7c20` | `git status --short` **为空**（无预先修改） |
| 实施后 | `master`（改动未提交） | `074943663408b6b61cf00558f809a05a0c2a7c20` | 本轮新增 / 修改的后端与前端文件（见 §6） |

开始前执行 `git fetch origin master`，`origin/master` 与本地 `HEAD` 同为 `0749436`，上游未漂移；
`git status --short` 为空，说明没有需要保留的他人改动。

核对的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`（§G9、
§7 M4 工作项表、§R4 风险）、`.steps/P4-M4-1-task-retry-web-entry.md`、
`.steps/P4-M4-2-code-save-verification-web-action.md`。

---

## 2. 实施前核实到的真实缺口（全部结论来自代码）

### 2.1 业务能力齐全，HTTP 完全没有

| 事实 | 代码位置 |
|---|---|
| 删除/脱离管理已完整实现 | `app.ContainerService.DeleteContainer` / `DetachContainer`（`internal/app/containers_ops.go:283`、`:349`） |
| 二者都先核验远程所有权 | `deleteLocked` 先 `observe`（内部 `docker.VerifyOwnership`），`DetachContainer` 同样先 `observe` |
| 已脱离管理拒绝一切自动操作 | `ensureManaged`（`internal/app/containers_ops.go:165`）；`domain.Container.MarkRemoved` / `Detach` 再次拒绝 |
| 重复删除是幂等成功 | `deleteLocked` 在 `State == removed` 时只同步远程清单并返回成功 |
| 生命周期策略与旧容器处置方式 | `domain.DecideContainerLifecycle`、`domain.PlanContainerRestart`（`internal/domain/container_lifecycle.go`） |
| CLI 已有两条命令 | `agentflow docker container delete` / `detach`（`internal/cli/docker_container.go:348` 起） |
| **HTTP 完全没有容器接口** | 实施前 `grep -rn "containers" internal/httpapi/*.go` 只命中 `code_recovery.go` 与 `router.go` 的 `Deps.Containers`（代码保存核验用），没有容器路由 |
| **前端完全没有容器类型与操作** | 实施前 `src/lib/api/types.ts` 关于 `container` 只有各响应里的 `container_id` 字符串；无 `TaskContainer`、无删除/脱离管理入口 |

### 2.2 为什么必须在应用层补一个服务

用户报错文本直接指向 CLI 处置旧容器（`internal/app/execenv_task.go:272-283`），但 HTTP 层没有
任何可用入口。同时存在两个真实约束：

1. **不能直接用 `loadOwnedContainer`**：`internal/app/task_container.go` 的归属校验
   （`domain.DecideContainerOwnership`）要求"容器记录正好是任务记录里的 `container_id`"、
   "未脱离"、"未删除"、"有 Docker ID"。**处置旧容器的典型现场恰恰是历史代次**：重新开始前的
   旧容器还可能被 `detach`，也可能已被删除。直接复用会把正常处置路径全部拒绝。
2. **不能在前端或 Handler 里放宽校验**：跨任务操作必须被拒绝，且已脱离管理是终态。

因此新增 `domain.DecideContainerDisposal`（纯函数）与
`app.TaskContainerDisposalService`（应用层），二者分工：

- `domain` 只判定"这条记录能不能被这个任务处置"；
- `app` 负责读取任务与容器、调用 `ContainerService`、重读真实记录并归类错误；
- `httpapi` 只做协议转换与状态码映射。

### 2.3 依赖装配现状

- `server.Server` 在 `startServices` 中通过 `containerObservation()` 构造 `*app.ContainerService`
  （要求 `store` 与 `ssh` 同时存在），并把它注入 `httpapi.Deps.Containers`（供代码保存核验使用）。
- 该实例**本来就必须存在**才能构建代码保存核验接口，因此本轮直接复用：没有为容器操作新开一条
  绕过既有凭据与主机配置的远程连接路径，也没有让只读接口强制要求 SSH。

---

## 3. 新增 HTTP API 契约

### 3.1 路由与分发

| 接口 | 方法 | 语义 | 错误 |
|---|---|---|---|
| `/api/v1/tasks/{taskID}/containers` | `GET` / `HEAD` | 只读：返回该任务关联的容器登记记录 | 400 参数、404 任务不存在、503 服务未装配 |
| `/api/v1/tasks/{taskID}/containers/{containerID}/delete` | `POST` | 显式删除容器 | 400 / 404 / 409 / 500 |
| `/api/v1/tasks/{taskID}/containers/{containerID}/detach` | `POST` | 显式让容器脱离管理 | 400 / 404 / 409 / 500 |

- 路径形状由 `parseTaskContainerListPath`（2 段）与 `parseContainerDisposalPath`（4 段，
  末段必须是 `delete` 或 `detach`）按**路径段边界**判定（`internal/httpapi/task_containers.go`）。
  其他形状（例如 `.../containers/{id}/stop`）返回 404，不会落到删除或脱离管理。
- 分发位置在 `router.tasksHandler` 中，**先于任务管理服务的可用性判定**：容器查询只依赖存储，
  不应因为任务服务未装配而得到误导性的 503。
- 方法不匹配返回 405 并带 `Allow` 头（列表 `GET, HEAD`，动作 `POST`）。

### 3.2 请求 DTO

两个动作**没有请求体**，也**不接受任何请求体**（不调用 `decodeOptionalJSONBody`，也不解析
`container_id`、`work_dir`、`reason` 之类字段）。调用方只能通过路径给出：

- `taskID`：任务标识，来自路径；
- `containerID`：**真实的容器登记记录 ID**，来自路径；
- `action`：由路径末段决定。

删除的 `Reason` 由服务端固定为 `domain.ContainerEndReasonManual`（人工处置），调用方无法把手工
删除伪装成"任务成功清理"。

### 3.3 响应 DTO

列表 `TaskContainerListResponse`：

```json
{
  "records": [
    {
      "id": "container-...",
      "task_id": "task-...",
      "name": "agentflow-...",
      "state": "running",
      "docker_state": "running",
      "detached": false,
      "detached_at": "2026-10-27T09:00:00Z",
      "end_reason": "",
      "origin": "current",
      "created_at": "2026-10-27T08:00:00Z",
      "finished_at": "2026-10-27T08:30:00Z",
      "updated_at": "2026-10-27T08:30:00Z"
    }
  ],
  "total": 1
}
```

处置 `ContainerDisposalResponse`：

```json
{
  "task_id": "task-...",
  "action": "delete",
  "outcome": "completed",
  "detail": "容器 container-... 已删除；共享镜像、缓存卷与远程项目配置目录未受影响",
  "container": { "…同 TaskContainerResponse，处置后重新读取的真实记录…" }
}
```

字段语义（逐字段来自 Go DTO，前端不虚构字段）：

| 字段 | 来源 | 不能推出的结论 |
|---|---|---|
| `state` | `domain.Container.State` | 不等于 Docker 实际状态 |
| `docker_state` | `domain.Container.DockerState` | 不等于 AgentFlow 管理状态，也不代表可操作 |
| `detached` / `detached_at` | `domain.Container.Detached` / `DetachedAt` | 不代表容器已停止 |
| `end_reason` | `domain.Container.EndReason` | 未结束时为空字符串，不代表失败 |
| `origin` | 派生：等于任务 `container_id` 则为 `current`，否则 `history` | 不代表历史容器一定可删；已脱离管理仍被拒绝 |
| `outcome` | `app.ContainerDisposalOutcome`（`completed` / `failed` / `uncertain`） | `completed` 仍须看同响应的 `container` |
| `action` | 路径动作 | 使响应自证语义，不与"停止"混淆 |

**不泄露的字段**：`docker_id`、`image_id`、`build_config_sha256`、`workspace`、`host_id`、
远程登记清单路径与任何凭据。这一点由
`TestTaskContainerResponseHidesInternalDetails` 直接断言。

### 3.4 错误映射

`disposalStatus`（`internal/httpapi/task_containers.go`）按错误类别映射，不解析错误文本：

| 情况 | 状态 | 错误代码 |
|---|---|---|
| 容器标识为空 / 动作未知 | 400 | `invalid_request` |
| 任务不存在或已逻辑删除 | 404 | `not_found` |
| 容器登记记录不存在 | 404 | `not_found` |
| 跨任务 / 跨节点（归属校验失败） | 409 | `state_conflict` |
| 记录已脱离管理、已删除仍请求 detach | 409 | `state_conflict` |
| 容器服务因既有自动管理约束拒绝 | 409 | `state_conflict` |
| 远程操作失败、记录持久化失败 | 500 | `execution_failed` |
| 远程结果无法确认 | 500 | `execution_failed` |
| 服务未装配 | 503 | `internal_error` |

失败一律使用统一错误结构，**不返回 2xx**，也不把部分完成伪装成成功。

---

## 4. 归属校验、所有权核验与生命周期语义

### 4.1 两层校验（不可互相替代）

```text
HTTP Handler  只做路径解析与协议转换
      ↓
app.TaskContainerDisposalService   读取任务 + 容器记录，调用 domain 判定
      ↓
domain.CheckContainerDisposal      纯函数：任务归属、节点一致性、终态约束
      ↓
app.ContainerService.Delete/Detach 远程所有权核验（docker.VerifyOwnership）+ 远程副作用
```

- **第一层（新增）**：`domain.DecideContainerDisposal`（`internal/domain/container_ownership.go`）。
  规则顺序固定：
  1. 记录未登记任务归属 → 拒绝 `container_unassigned`；
  2. 记录登记的是另一个任务 → 拒绝 `container_task_mismatch`（跨任务操作的唯一可核验防线）；
  3. 记录所属执行节点与任务不一致 → 拒绝 `container_host_mismatch`（否则远程操作会打在错误的机器上）；
  4. `state=detached` 但 `detached=false` → 拒绝（记录自相矛盾，必须先人工核验）；
  5. 已脱离管理 → 拒绝（`delete` 与 `detach` 都拒绝，终态只能人工处理）；
  6. 已删除仍请求 `detach` → 拒绝（不能把删除报告为成功脱离）。

  **与 `DecideContainerOwnership` 的关键差异**：处置校验**允许历史代次**（登记了任务归属但不是
  任务当前 `container_id` 引用的那条），这正是 G9 的核心现场；已删除请求 `delete` 仍然允许，
  由容器服务按幂等成功处理（不产生新的远程副作用）。两者都有专门的测试断言其差异
  （`TestDecideContainerDisposalAllowsHistoryGeneration`）。

- **第二层（既有，未改动）**：`ContainerService.deleteLocked` / `DetachContainer` 在操作前
  `observe` 远程容器并执行 `docker.VerifyOwnership`（标签核验）。标签不一致时直接失败，
  **不执行任何破坏性操作**。

### 4.2 delete 与 detach 的边界

| | `delete` | `detach` |
|---|---|---|
| 远程动作 | `docker rm --force` | 无远程删除；只读取观测状态 |
| 本地记录 | `MarkRemoved(manual)` → `state=removed`、`finished_at` | `Detach()` → `state=detached`、`detached=true`、`detached_at`、`end_reason=detached` |
| 远程登记清单 | 更新 | 更新 |
| 共享镜像 / 缓存卷 / 远程项目配置目录 | **不删除** | 不涉及 |
| 已脱离管理的容器 | **拒绝**（沿用 `ensureManaged`） | **拒绝**（终态，不重复） |
| 已删除的容器 | 幂等成功（只同步清单） | **拒绝** |
| 可撤销 | 否 | 否（Docker 标签无法原地修改） |

### 4.3 结果不确定的如实表达

- 远程删除返回 `docker.CodeStateUncertain` 或 context 超时时，应用层把错误归入
  `app.ErrContainerOperationUncertain` → HTTP 500 `execution_failed`。
- 用户取消（`context.Canceled`）不属于"不确定"：它不会命中 `DeadlineExceeded` 分支，
  按失败返回（`TestTaskContainerDisposalFailures` 与 `isContainerOperationUncertain` 的注释）。
- 处置成功但重新读取记录失败时，返回错误而不是成功；日志保留完整原因
  （`Disposal` 的 `重新读取容器记录失败` 分支）。
- `containerDisposalOutcome` 校验执行后的记录：既没有 `removed` 也没有 `detached` 时返回
  `uncertain`，**不得报告为 `completed`**（`TestTaskContainerDisposalUncertainOutcome`）。

---

## 5. Web 入口与结果状态设计

### 5.1 入口位置

任务详情面板新增 `TaskContainerPanel`（位于 `TasksPage` 详情区，排在续跑资格面板之后）。
它展示**该任务真实关联的容器记录**（`GET /tasks/{id}/containers`），不从任务状态、`container_id`
或记录顺序猜测归属：

- 每条记录显示：完整容器标识 + 名称、与任务的关系（`任务当前容器` / `历史容器`）、AgentFlow
  管理状态、Docker 观测状态、是否已脱离（含时间）、结束原因、创建与结束时间；
- 未知状态、未知关联方式**原样展示**并标注"服务端返回的取值未在当前版本中定义"，
  不被 `DictTag` 兜底吞掉；
- 列表为空时明确说明"容器由环境准备阶段创建，本面板不会创建容器"；
- 列表读取失败时**不提供任何操作入口**（没有可信记录就没有操作对象）；
- 列表刷新失败时保留上一次成功结果并单独提示"可能不是最新状态"。

### 5.2 两个独立动作

| | 删除容器 | 脱离管理 |
|---|---|---|
| 按钮 | `删除容器`（危险色） | `脱离管理` |
| 确认标题 | `确认删除该容器？` | `确认让该容器脱离 AgentFlow 管理？` |
| 确认描述要点 | 删除容器与服务端规则允许清理的任务级临时资源；**共享镜像、共享缓存卷与远程项目配置目录不会被删除**；不可简单撤销；已脱离管理的容器不会被删除 | **保留容器现场**；AgentFlow 不再自动启停删除；**这不是删除**，**也不代表容器已经停止**；不可撤销 |
| 确认框信息 | 真实容器标识、容器名称、当前管理状态、所属任务 | 同左 |

两个动作在文案、路径、请求方法与响应 `action` 上都是分开的，界面上没有任何"清理"按钮
（`Test 两个动作的文案都不出现模糊的「清理」按钮语义`、E2E `两个动作的确认文案互不相同`）。

### 5.3 展示层防重与状态机

`containerActionBlocked` 只做**展示层的重复提交防护**，不制造第二套资格规则：

- `detached` / `state=detached`：两个动作都禁用，并说明"不再接受自动删除或重复脱离"；
- `state=removed`：`detach` 禁用，`delete` 仍可点击（服务端按幂等处理）；
- 其余状态（`creating` / `created` / `running` / `stopped` / `unknown` / 未知取值）：
  前端**不做白名单**，交给服务端判定（服务端还可能因节点预检失败而拒绝）。

### 5.4 状态与竞态（`useTaskContainers`）

三条独立事实分别保存、互不覆盖：

| 事实 | 来源 | 不被覆盖的情况 |
|---|---|---|
| 容器列表 | `GET .../containers` | 临时刷新失败保留上一次成功快照 + `error` 提示 |
| 处置结果 | `POST .../{delete|detach}` 响应 | 后续列表刷新失败不清除结论 |
| 请求 / 刷新失败 | `list.error` / `operation.failure` / `operation.uncertain` / `operation.refreshError` | 分别提示，不改写成成功 |

竞态约束：

| 场景 | 处理 |
|---|---|
| 任务切换 | 列表请求带「序号 + 任务标识」票据；处置结论在 `taskID` 变化时清空 |
| 处置后的列表刷新 | **不清空处置结论**（专门修复：初次实现把 `setOperation({})` 放进了列表 effect，导致处置结论被立即抹掉，由 E2E 发现并改正） |
| 同一容器在途 | `runningRef: Set<containerID>` 同步占位，同帧连续点击不会发第二个请求；两个动作同时禁用 |
| 不同容器 | 各自独立在途，互不影响 |
| 组件卸载 | `mountedRef` 拦截，不写入任何状态 |
| 迟到响应 | 任务标识不符则丢弃（E2E `处置期间切换到任务 B，A 的迟到响应不出现在 B 的详情里`） |
| 客户端超时 | 处置请求 `timeoutMs: 0`（不设超时），避免客户端提前断开把"已完成"变成"未知" |
| 自动重发 | **没有任何重发逻辑**；结果不确定时提示"先刷新容器列表核验真实状态" |
| 处置成功后的后续动作 | 只刷新容器列表 + 任务详情/工作流；**不调用重试接口、不启动工作流**（E2E 断言写请求中无 `/retry`、`/start`、`/execute-workflow`） |

### 5.5 与 M4-1 重试入口的关系

处置结果提示里固定追加一句：

> 处置容器不会重开任务，也不会启动工作流。如需重新开始，请在确认容器状态后使用任务重试入口。

页面不把两个动作复合化：处置成功后用户仍需显式使用既有的 `RetryTaskPanel`，并由服务端按
`can_retry` 重新判定资格。

---

## 6. 新增与修改的文件

### 6.1 Go 生产代码（本轮修改了生产代码，原因见 §2.2）

| 文件 | 改动 | 必要性 |
|---|---|---|
| `internal/domain/container_ownership.go` | **新增** `DecideContainerDisposal` / `CheckContainerDisposal` | 处置校验与归属校验是不同问题，必须各有一条唯一实现；放宽"历史代次"的同时保留终态约束 |
| `internal/app/task_container_disposal.go`（新增） | 新增 `TaskContainerDisposalService`：`TaskContainers` 只读查询 + `Disposal` 显式处置；5 个错误类别；错误归类辅助函数 | HTTP 需要一个既做归属校验、又不自己拼 Docker 命令的应用层入口 |
| `internal/httpapi/task_containers.go`（新增） | 路径解析、三个 Handler、两个 DTO、错误映射 | 协议转换，唯一职责 |
| `internal/httpapi/router.go` | `Deps.TaskContainers`、`router.taskContainers`、分发点 | 路由注册 |
| `internal/httpserver/server.go` | `Options.TaskContainers` 透传 | 依赖装配 |
| `internal/server/server.go` | 构造 `TaskContainerDisposalService`（`Store` + `observation` 容器服务）并注入 HTTP | 与既有 `ContainerService` 共用同一实例，不新开远程连接路径 |

**未改动**：`ContainerService` 的任何方法、`domain.Container` 状态机、
`domain.DecideContainerLifecycle`、`domain.PlanContainerRestart`、`internal/docker`、
迁移脚本、`app.TaskRetryService`、`app.TaskWorkflowService`。

### 6.2 前端

| 文件 | 改动 |
|---|---|
| `src/lib/api/types.ts` | 新增 `TaskContainer`、`TaskContainerListResponse`、`ContainerDisposalAction`、`ContainerDisposalOutcome`、`ContainerDisposalResponse` |
| `src/lib/api/endpoints.ts` | 新增 `taskContainersPath`、`taskContainerDisposalPath`、`fetchTaskContainers`、`disposeTaskContainer` |
| `src/lib/api/index.ts` | 导出上述类型与函数 |
| `src/pages/tasks/containers.ts`（新增） | 纯数据与纯函数：状态/关联方式/结束原因元数据、两个动作的文案、展示层防重、处置结论解释、不确定提示 |
| `src/pages/tasks/useTaskContainers.ts`（新增） | 唯一状态实现：列表快照、票据、在途防重、处置结果、刷新失败 |
| `src/pages/tasks/TaskContainerPanel.tsx`（新增） | 面板：记录展示 + 两个动作 + 确认弹窗 + 结果展示 |
| `src/pages/tasks/TasksPage.tsx` | 详情区接入 `<TaskContainerPanel>` |

**未改动**：`useTaskLiveSnapshot`、`liveSnapshot`、`autoRefresh`、`requestGate`、
`RetryTaskPanel`、`useTaskRetry`、`CodeSavePanel`、`useExecutionRecords`。

### 6.3 测试

| 文件 | 内容 |
|---|---|
| `tests/unit/httpapi/task_containers_test.go`（新增） | 16 个协议用例 |
| `tests/unit/domain/container_disposal_test.go`（新增） | 9 个领域判定用例 |
| `lingting-agent-flow-ui/tests/unit/task-containers.test.ts`（新增） | 37 个前端单元用例 |
| `lingting-agent-flow-ui/tests/e2e/task-containers.spec.ts`（新增） | 18 个浏览器用例 |

---

## 7. 测试与验证的实际执行结果

Go 命令在仓库根目录执行；前端命令在 `lingting-agent-flow-ui/` 下执行。测试授权来自任务书 §6
的明确要求。

| # | 命令 | 是否执行 | 结果 |
|---|---|---|---|
| 1 | `gofmt -l internal cmd tests` | 已执行 | 无输出（无未格式化文件） |
| 2 | `go vet ./...` | 已执行 | 无输出（无告警） |
| 3 | `go build ./...` | 已执行 | 通过 |
| 4 | `go test ./tests/unit/domain/ -run TestDecideContainerDisposal -count=1` | 已执行 | **9 passed** |
| 5 | `go test ./tests/unit/httpapi/ -run TestTaskContainer -count=1` | 已执行 | **16 passed** |
| 6 | `go test ./tests/unit/... ./tests/integration/local/... -count=1` | 已执行 | **2239 passed**（35 包） |
| 7 | `go test ./tests/e2e/... -count=1` | 已执行 | **58 passed**（2 包） |
| 8 | `npx vitest run tests/unit/task-containers.test.ts` | 已执行 | **37 passed** |
| 9 | `pnpm test:unit:safe` | 已执行 | 25 文件通过，**522 passed** |
| 10 | `npx playwright test tests/e2e/task-containers.spec.ts --workers=1` | 已执行 | **18 passed** |
| 11 | `pnpm test:e2e:safe` | 已执行 | **183 passed / 0 failed**（7.5m） |
| 12 | `pnpm check`（`vp fmt && vp lint --fix && tsc --noEmit`） | 已执行 | 通过（127 files），无 warning |
| 13 | `pnpm build` | 已执行 | 通过，产物写入 `internal/webui/dist` |

测试产物目录（`TEST_ARTIFACT_DIR`，保留未清理）示例：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<random>`。
`pnpm test:e2e:safe` 的完整报告位于其自身 `run-*` 目录下的 `playwright-report/`。

### 7.1 过程中的失败与处理（如实记录）

| 失败 | 原因 | 是否本轮回归 | 处理 |
|---|---|---|---|
| `TestTaskContainerDisposalFailures` 两例：错误代码期望 `execution_failed` 实际为空 | 失败路径返回了带响应体的 2xx 风格 DTO，测试解析的是 `ErrorResponse` | 是（本轮新增代码） | 删除 `writeDisposalFailure`，失败一律走统一错误结构；重跑通过 |
| E2E 14 例 `expect(locator)` 超时（无处置结果） | Playwright 的 `*` 不跨 `/`，`.../containers/*` 无法匹配 `.../containers/{id}/delete` | 是（测试自身缺陷） | 改用 `**/api/v1/tasks/{id}/containers/**` |
| E2E `container-1` strict mode violation（2 个元素） | 容器名称 `agentflow-container-1` 也包含该子串 | 是（测试断言不够精确） | 断言改为 `getByText("container-1", { exact: true })` |
| E2E 确认按钮点不到 | 行内按钮与弹窗确认按钮同名可匹配 | 是（测试自身缺陷） | 行内按钮用 `exact: true`、弹窗按钮限定 `.ant-modal` 作用域，确认按钮文案改为 `确认删除容器` / `确认脱离管理` |
| E2E `处置已完成` 不出现（**真实产品缺陷**） | `useTaskContainers` 在列表请求 effect 里 `setOperation({})`，处置后的列表刷新立即抹掉结论 | **是（本轮新增代码的真实缺陷）** | 把清空逻辑拆到只依赖 `trimmed` 的独立 effect；E2E 与单元测试同时转为通过 |
| E2E `删除容器` 按钮未禁用（在途） | 按钮在途时文案变为 `删除中`，旧断言按 `删除容器` 查找 | 是（测试断言不准确） | 断言改为 `删除中` 且 `disabled`，并用可控挂起请求固定"在途"窗口 |
| E2E `容器列表刷新失败` 不出现 | React StrictMode 重复执行 effect，按调用次数切换失败不可靠 | 是（测试设计缺陷） | 改用显式 `fail` 开关控制失败时机 |
| E2E `状态刷新失败` strict mode violation（2 处） | 容器面板与页面顶部各有一处提示，本应同时存在 | 是（测试断言不够精确） | 改为断言容器面板的"上方结论仍然有效" + 页面顶部提示之一 |
| lint 首轮 `Modal` 不支持 `description` 属性 | antd v6 `Modal` 无该 prop | 是（本轮新增代码） | 改为 children 渲染描述内容 |
| 未知容器状态在 UI 中不可见 | `DictTag` 对未知取值使用兜底，吞掉原始值 | 是（本轮新增代码） | 未知状态改用 `Typography.Text code` 原样展示 + 明确说明；关联方式同样处理 |

---

## 8. 未完成项、未覆盖场景与剩余风险

| 项目 | 状态 | 说明 |
|---|---|---|
| 真实后端联调（真实 SQLite + 真实容器 + SSH + Docker） | **未执行** | 用户未要求；E2E 全部为浏览器层拦截，Go 侧 HTTP 测试使用受控替身，都不能证明真实远程删除/脱离在容器内可用 |
| 真实 SSH / Docker 集成测试 | **未执行** | 同上；`tests/integration/remote/` 需要 `ssh.enable` 与 `docker.remote_enabled`，本轮未启用 |
| 浏览器人工验收（真实地址访问） | **未执行** | 用户未提供地址，也未要求访问页面 |
| 容器列表接口的 `Sync` 观测 | **未做** | 列表只读数据库，不触发远程 `docker inspect`（避免只读列表需要 SSH）。因此 `docker_state` / `state` 是**最后一次写入时**的事实；需要最新观测时仍应使用既有 CLI `docker container show`。这是刻意的取舍，不是缺陷未修 |
| 处置前的远程预检（提前展示"可操作"结论） | **未做** | 服务端在处置时核验所有权并可能因节点预检失败返回 412/409；前端不提前查询，避免为"看一眼能不能删"建立远程会话 |
| 已脱离管理容器的清理入口 | **无** | 按既有约束（`AGENTS.md`：不允许通过 Web 删除或重新接管已脱离管理的容器），界面禁用并提示"如需清理请人工处理" |
| 容器 `start` / `stop` 的 Web 入口 | **不做** | 不属于 G9，任务书明确禁止顺手实现 |
| 与 `resume-eligibility` 的 `next_actions` 联动 | **未做** | 若续跑资格给出"需在 CLI 处置容器"的动作，界面仍按 M4-4 处理；本轮未改动该面板 |
| 并发多标签页 | 依赖服务端 | 同一容器两个标签页同时点击时，服务端 `withContainerLock` 与状态机条件写入保证只有一个真实生效，另一个按失败或幂等返回；前端不做跨标签页协调 |

---

## 9. 本轮范围声明

- **修改了 Go 生产代码**：原因是 G9 的 HTTP 入口完全不存在，且既有的归属校验
  （`DecideContainerOwnership`）语义上不允许处置历史代次与终态容器，必须在应用层补一个既做
  归属判定、又复用 `ContainerService` 的最小服务。没有其它可行路径能在不放宽安全约束的前提下
  补齐 G9。
- **未重写容器生命周期**：`ContainerService`、`domain.Container` 状态机、
  `domain.DecideContainerLifecycle`、`domain.PlanContainerRestart` 全部未改。
- **未新增第二套任务重启/工作流**：处置成功后不重试、不启动工作流、不推进任务状态。
- **未新增表或迁移**：容器记录结构未变。
- **未新增绕过凭据或主机配置的远程连接路径**：复用 `server.Server` 已构造的
  `*app.ContainerService`，其远程会话仍经 `withRemoteSession` → `EnsureHostUsableForRemoteTask`。
