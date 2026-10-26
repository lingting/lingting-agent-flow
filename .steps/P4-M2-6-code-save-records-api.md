# P4-M2-6 —— 代码保存记录列表 API

> 实施依据：`master` 上 `internal/domain/code_save.go`、`internal/app/{code_save.go,
> code_save_query.go, docker_session.go, task_management.go, execution_records.go}`、
> `internal/storage/interfaces.go`、`internal/storage/sqlite/code_saves.go`、
> `internal/httpapi/{code_saves.go, code_recovery.go, router.go, tasks.go, executions.go}`、
> `internal/httpserver/server.go`、`internal/server/server.go` 与 `tests/` 的实际代码核实。
> 对应 P4-M1 缺口 **G8** 的 API 部分（见
> [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md) §5、§6）。

## 1. 真实业务需求

代码保存记录（`code_saves`）自 CodeSaver 落地起就在写入：每条记录分别保存提交、推送与
合并三个 Git 阶段的结果，以及记录自身的写入可信度（`persist_status`）与只读核验结论
（`recovery_status`）。但本轮之前，这些已落盘的数据只有两种消费方式：

- CLI：`agentflow code status <taskID>` 调用 `app.ContainerService.CodeSaveStatus`；
- HTTP：只有 `POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save`，
  需要先知道 `recordID`。

Web 上没有任何入口能列出记录，因此拿不到 `recordID`，核验接口在真实使用中不可达；用户
也无法判断"代码到底有没有推到远端、推到哪一步失败"（G8 的原始描述）。本轮把**只读列表**
暴露为 HTTP 接口，作为后续 M3-5（三阶段状态展示）与 M4-2（核验操作入口）的数据入口。

## 2. API 契约

### 2.1 路径

```text
GET /api/v1/tasks/{taskID}/code-saves
```

只支持 `GET` 与 `HEAD`；其他方法返回 `405` 且 `Allow: GET, HEAD`。

路径与既有代码保存核验共享 `code-saves` 前缀，但形状不同、互不抢占：

| 形状 | 段数 | 归属 |
|---|---|---|
| `{taskID}/code-saves` | 2 | 本轮新增的只读列表 |
| `{taskID}/code-saves/{recordID}` | 3 | 有意不提供：没有单条记录查询端点（见 §2.5） |
| `{taskID}/code-saves/{recordID}/verify-code-save` | 4 | 既有核验动作，行为不变 |

判定按路径段进行（`isCodeSaveListPath` → `actionTaskID`，只有两段且第二段为 `code-saves`
才命中），因此不会把 `code-save`（拼错）、`code-saves/extra`、核验路径或多段路径误当成列表。

### 2.2 响应结构

```json
{
  "records": [
    {
      "id": "01J9Z0T7R6V0Y3X8W2K4M5N6P7",
      "task_id": "01J9Z0T7R6V0Y3X8W2K4M5N6P0",
      "project_id": "project-1",
      "container_id": "01J9Z0T7R6V0Y3X8W2K4M5N6P1",
      "docker_id": "9d0f2b1c8e4a",
      "attempt_id": "01J9Z0T7R6V0Y3X8W2K4M5N6P2",
      "remote_name": "origin",
      "repository": "https://git.example.com/group/repo.git",
      "task_branch": "agent/01J9Z0T7R6V0Y3X8W2K4M5N6P0",
      "base_branch": "main",
      "commit_status": "succeeded",
      "commit_sha": "1111111111111111111111111111111111111111",
      "commit_message": "提交一",
      "push_status": "unknown",
      "merge_enabled": false,
      "merge_status": "not_requested",
      "stage": "push",
      "error_code": "push_unconfirmed",
      "error_summary": "推送结果无法确认",
      "created_at": "2026-10-26T08:00:00Z",
      "started_at": "2026-10-26T08:00:01Z",
      "finished_at": "2026-10-26T08:00:12Z",
      "updated_at": "2026-10-26T08:00:12Z",
      "baseline_commit_sha": "0000000000000000000000000000000000000000",
      "persist_status": "persisted",
      "recovery_status": "unverified"
    }
  ],
  "total": 1
}
```

`records` 永远输出数组（空时为 `[]`，不是 `null`）。`total` 即本次返回的记录数：代码保存
记录按任务归属、数量有限，因此不引入分页；如需分页应在此接口上统一处理，而不是新增第二个
入口。

### 2.3 字段语义

| 字段 | 语义 |
|---|---|
| `id` | 记录标识。**本轮的核心交付**：Web 据此发起只读核验 |
| `task_id` | 记录所属任务；与路径 `{taskID}` 一致 |
| `project_id` / `attempt_id` / `docker_id` | 可选关联标识，缺失时省略 |
| `container_id` | 容器登记记录标识 |
| `remote_name` / `repository` | Git 远程名称与**已脱敏**的仓库地址（脱敏在写入前完成） |
| `task_branch` / `base_branch` | 任务分支（`agent/<task-id>`）与识别出的基础分支 |
| `commit_status` / `commit_sha` / `commit_message` | 提交事实、产生的 commit 与单行摘要 |
| `push_status` / `remote_commit_sha` | 任务分支的推送结果与核验到的远程 commit |
| `merge_enabled` | 是否请求了推送后合并（默认 false，开启保存不等于同意合并主分支） |
| `merge_status` / `merge_sha` | 合并结果与合并后基础分支的 commit |
| `stage` | 最后一个进入的操作阶段，用于中断后定位 |
| `error_code` / `error_summary` | 可诊断的错误分类与**写入时已脱敏**的失败摘要 |
| `created_at` / `started_at` / `finished_at` / `updated_at` | 记录时间线 |
| `baseline_commit_sha` / `baseline_base_commit_sha` | 提交前 HEAD 与合并前远程基础分支；缺失表示无法判定 |
| `persist_status` | 记录自身是否被可靠写入；**空值已归一化为 `persisted`** |
| `persist_error` | 最后一次写入失败的脱敏摘要 |
| `recovery_status` | 只读核验结论；**空值已归一化为 `unverified`** |

所有字段直接映射 `domain.CodeSaveRecord`，HTTP 层不推断任何 Git 结果，也不拼接新的错误
信息：`error_summary` 与 `persist_error` 就是持久化时的内容。

### 2.4 为什么归一化 `persist_status` / `recovery_status`

两者在领域模型里都有"空值"的历史语义：

- `PersistStatus.Normalized()`：空值等价于 `persisted`（历史记录写入时还没有该字段，不能
  因此被判为不可信）；
- `RecoveryStatus.Normalized()`：空值等价于 `unverified`（"还没有核验"，不是第三种结论）。

DTO 对两者都调用领域的归一化方法后输出，因此响应里不会出现空字符串。这不是"改写状态"，
而是遵守领域模型已定义的兼容规则；`unconfirmed`、`unverifiable`、`confirmed_applied`、
`confirmed_not_applied` 都按真实值原样输出。

本轮把 `RecoveryStatus.normalized` 提升为导出的 `Normalized`（唯一调用点原本全在
`internal/domain` 内部），使 HTTP 层复用同一份规则而不是复制一份。这是本轮唯一的领域层
改动，不改变任何行为。

### 2.5 有意不提供的入口

- **单条记录查询**（`GET …/code-saves/{recordID}`）：列表已返回全部诊断字段，再开一个
  单条入口会让同一语义出现两条路径；核验动作需要的是 `recordID`，列表已提供。用例
  `TestCodeSaveListRejectsUnknownShapes` 锁定该形状返回 `404`。
- **分页**：见 §2.2。
- **任何写入口**：本阶段不提供保存触发、核验触发或恢复入口。

### 2.6 错误映射

| 场景 | 状态码 | `error.code` |
|---|---|---|
| 任务不存在 | `404` | `not_found` |
| 任务已逻辑删除 | `404` | `not_found` |
| 路径标识非法（空、含 `/`） | `404` | `not_found` |
| 路径形状不属于本接口 | `404` | `not_found` |
| 不支持的方法 | `405` + `Allow: GET, HEAD` | `method_not_allowed` |
| 查询服务未初始化 | `503` | `internal_error` |
| 存储或内部错误 | `500` | `internal_error` |

映射依据**错误类别**（`errors.Is(err, app.ErrTaskNotFound)` 等），不解析错误文本。`500` 的
响应消息固定为 `服务内部错误`，原始原因只写服务端日志。

"任务不存在"与"任务下没有记录"是两件不同的事实：前者 `404`，后者 `200` + 空 `records`。
用例 `TestCodeSaveListDistinguishesMissingTaskFromEmptyList` 在真实服务上锁定该区别。

非法任务标识按未找到处理而不是 `400`：两者都不能通过状态码差异暴露"哪些标识形状被服务端
识别"，这与既有执行记录接口的规则一致。

## 3. 应用层依赖边界（本轮的关键取舍）

### 3.1 为什么不能复用 `ContainerService.CodeSaveStatus`

`app.ContainerService.CodeSaveStatus(ctx, taskID)` 确实能列出记录，但它挂在
`ContainerService` 上，而该服务的构造链是：

```text
app.NewContainerService (internal/app/containers.go:29)
  → newRemoteDockerService (internal/app/docker_session.go:73)
      → 要求 options.Store != nil
      → 要求 options.SSH != nil     ← 强制依赖
```

在 `internal/server/server.go` 里，它由 `containerObservation()` 构造，且该函数在
`store == nil || ssh == nil` 时直接返回 `nil`。也就是说，把列表查询绑在它上面，会让
"仅为了读一张表"的请求被 SSH 可用性阻塞，并在测试里引入与业务无关的远程依赖。

因此本轮新增 `app.CodeSaveQueryService`（`internal/app/code_save_query.go`），它只依赖：

```go
type CodeSaveQueryStore interface {
    GetTask(ctx context.Context, id domain.ID) (*domain.Task, error)
    ListCodeSaveRecordsByTask(ctx context.Context, taskID string) ([]domain.CodeSaveRecord, error)
}
```

接口只声明两个读取方法，**编译期就不存在**写入、核验或建立远程会话的可能。查询路径上不会：
建立 SSH / Docker 连接、检查 / 启动 / 删除容器、调用 Git 命令、修改代码保存记录、推进任务
状态、执行核验或恢复。

### 3.2 归属校验顺序

| 顺序 | 校验 | 失败结果 |
|---|---|---|
| 1 | 校验 `taskID` 形状（空、含 `/` 均视为未找到） | `ErrTaskNotFound` |
| 2 | 读取任务，不存在映射为未找到 | `ErrTaskNotFound` |
| 3 | 任务已逻辑删除 | `ErrTaskNotFound` |
| 4 | 按任务列出记录，存储失败向上返回 | 原始错误（不折叠为空列表） |

HTTP 层不做业务校验，只做协议转换与状态码映射。

### 3.3 路由分发位置

`internal/httpapi/router.go` 的 `tasksHandler` 中，列表判定插在：

```text
isTaskLaunchPath → isTaskRetryPath → isCodeSaveListPath → 任务服务可用性 → …
```

位置的两点理由：

1. 放在任务服务可用性判定**之前**：列表只依赖查询服务，任务管理服务未初始化时应当得到
   "代码保存记录查询服务未初始化"，而不是误导性的"任务服务未初始化"。
2. 放在 `isCodeRecoveryPath` 之前但形状互斥：`isCodeSaveListPath` 只匹配 2 段，核验是 4 段，
   因此核验、续跑资格、任务详情、取消、重试、启动、计划生成、工作流的行为都不变。

`isExecutionRecordPath` 只认第二段为 `steps` / `sessions`，因此 `code-saves` 不会被它接走。

## 4. 修改文件与代码证据

| 关注点 | 位置 |
|---|---|
| 应用层查询与归属校验（新增） | `internal/app/code_save_query.go` 的 `CodeSaveQueryStore`、`CodeSaveQueryService.ListByTask`、`parseCodeSaveQueryTaskID` |
| HTTP DTO、处理器与路径判定（新增） | `internal/httpapi/code_saves.go` 的 `codeSavesSegment`、`CodeSaveRecordListEntryResponse`、`CodeSaveRecordListResponse`、`codeSaveListHandler`、`serveCodeSaveList`、`isCodeSaveListPath` |
| 路由分发（修改） | `internal/httpapi/router.go`：`Deps.CodeSaveQueries`、`router.codeSaves`、`tasksHandler` 中的 `isCodeSaveListPath` 分支 |
| 依赖装配（修改） | `internal/httpserver/server.go` 的 `Options.CodeSaveQueries` → `httpapi.Deps`；`internal/server/server.go` 的 `codeSaveQueries` 字段、`startServices` 构造与 `startHTTP` 注入 |
| 服务构造（新增） | `internal/server/server.go` 的 `codeSaveQueryService(store)`：只传存储，未传 SSH |
| 领域归一化导出（修改） | `internal/domain/code_save.go` 的 `RecoveryStatus.normalized` → `Normalized`，并更新 `MarkRecovery`、`HasUnresolvedSideEffect`、`code_save_recovery.go` 的两处调用 |
| 存储读取（复用，未修改） | `internal/storage/sqlite/code_saves.go` 的 `ListCodeSaveRecordsByTask`（`ORDER BY created_at DESC, id DESC`） |

**未修改**：CodeSaver 的执行逻辑（`internal/codesaver/`）、核验与恢复逻辑
（`internal/app/code_save_verify.go`、`code_save_recovery.go` 的判定规则）、
`ContainerService.CodeSaveStatus`（CLI 仍在用）、数据库 Schema（**无新增迁移**）、
前端代码、任务 / 步骤状态机。

排序沿用存储层的真实语义 `created_at DESC, id DESC`：次级键 `id` 保证同一毫秒创建的记录
顺序稳定，应用层不重新排序。

## 5. 测试证据

全部使用真实 SQLite + 正式迁移 + 项目现有 fixture；测试产物保留在
`os.TempDir()/lingting-agent-flow-tests/` 下的随机目录，运行时输出 `TEST_ARTIFACT_DIR`，
不清理其他测试运行留下的产物。

### 5.1 应用层（`tests/unit/app/code_save_query_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestCodeSaveQueryReturnsTaskRecords` | 正常返回某任务的多条记录：只含本任务、`id` 非空、三阶段状态独立保留 |
| `TestCodeSaveQueryOrdersNewestFirst` | 最新记录在前，且重复查询顺序一致（不依赖隐式返回顺序） |
| `TestCodeSaveQueryRejectsDeletedTask` | 任务逻辑删除后返回 `ErrTaskNotFound` |
| `TestCodeSaveQueryRejectsUnknownTask` | 任务不存在返回 `ErrTaskNotFound` |
| `TestCodeSaveQueryRejectsInvalidTaskID` | 空、纯空白、含 `/` 的标识按未找到处理 |
| `TestCodeSaveQueryReturnsEmptyList` | 没有记录时返回合法空列表（不是错误） |
| `TestCodeSaveQueryStorageFailureIsNotNotFound` | 关闭数据库后返回错误，且不归类为未找到 / 记录不存在 |
| `TestCodeSaveQueryIsReadOnly` | 两次查询后记录五类状态、时间戳与任务状态、时间戳均不变，记录条数不变 |
| `TestCodeSaveQueryRequiresStore` | 存储为空时构造失败 |
| `TestCodeSaveQueryStoreHasNoRemoteDependency` | 真实 SQLite 满足只含两个读取方法的依赖契约（无远程依赖） |

### 5.2 HTTP 层（`tests/unit/httpapi/code_saves_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestCodeSaveListReturnsTaskRecords` | 成功返回、真实 `id`、`records` / `total` 与落盘数据一致 |
| `TestCodeSaveListOrdersNewestFirst` | 多条记录按创建时间倒序，首条为最晚创建的未确认记录 |
| `TestCodeSaveListKeepsStagesIndependent` | 提交 / 推送 / 合并三阶段独立返回；`unknown` 推送不伪造远程 commit；`stage` / `error_code` / `error_summary` 如实输出 |
| `TestCodeSaveListKeepsPersistAndRecoverySemantics` | `unconfirmed` 如实返回并带 `persist_error`；空 `recovery_status` 归一化为 `unverified`；空 `persist_status` 归一化为 `persisted` |
| `TestCodeSaveListReturnsEmptyList` | 空任务返回 `200`，`records` 是空数组而非 `null` |
| `TestCodeSaveListRejectsUnknownTasks` | 任务不存在与已逻辑删除都返回 `404` + `not_found` |
| `TestCodeSaveListRequiresService` | 依赖缺失返回 `503`，不是 `404` 或空列表 |
| `TestCodeSaveListStorageFailureIsNotNotFound` | 存储错误返回 `500` + `internal_error`，消息脱敏为 `服务内部错误` |
| `TestCodeSaveListRejectsUnsupportedMethods` | `POST` / `PUT` / `PATCH` / `DELETE` 返回 `405` + `Allow: GET, HEAD`，且记录条数不变 |
| `TestCodeSaveListRejectsUnknownShapes` | `code-saves/extra`、`code-save`、单条记录路径、多段路径均返回 JSON `404` |
| `TestCodeSaveListQueryIsReadOnly` | 两次查询后记录状态、时间戳、任务状态与时间戳完全不变 |

### 5.3 真实 SQLite + 真实 HTTP 服务（`tests/integration/local/httpserver/code_saves_test.go`）

两条互不相关的任务（第一个 3 条记录、第二个 1 条记录、另有一个空任务），依赖注入走正式路径
（`app.NewCodeSaveQueryService` → `httpserver.Options`），**故意不注入任何容器 / SSH 能力**。

| 用例 | 覆盖行为 |
|---|---|
| `TestCodeSaveListOverRealServer` | 响应与真实落盘数据逐条逐字段对照（标识、归属、分支、容器、三阶段状态、SHA、写入状态、恢复结论、错误摘要），不使用写死 JSON |
| `TestCodeSaveListDoesNotCrossTasksOverRealServer` | 两次独立查询，双方都不含对方的记录 |
| `TestCodeSaveListDistinguishesMissingTaskFromEmptyList` | 空任务 `200` + 空列表；不存在的任务 `404` + `not_found` |
| `TestCodeSaveListOverRealServerIsReadOnly` | 两次查询后记录状态、时间戳、顺序与任务状态、时间戳均不变 |
| `TestCodeSaveListWithoutServiceOverRealServer` | 未注入服务时真实服务返回 `503` |

### 5.4 统一服务装配（`tests/unit/server/code_saves_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestServerWiresCodeSaveQuery` | `server.New` + `Run` 打开真实数据库与迁移，由 `Server` 自行构造查询服务并注入 HTTP；列表返回真实数据（含 `unverified` 归一化），不存在的任务返回 `404`。该用例未提供 SSH 能力 |

### 5.5 实际执行结果

| 命令 | 结果 |
|---|---|
| `go test -count=1 ./tests/unit/app/ -run TestCodeSaveQuery` | 通过（10 个用例） |
| `go test -count=1 ./tests/unit/httpapi/ -run TestCodeSaveList` | 通过（11 个用例） |
| `go test -count=1 ./tests/integration/local/httpserver/ -run TestCodeSaveList` | 通过（5 个用例） |
| `go test -count=1 ./tests/unit/server/ -run TestServerWiresCodeSaveQuery` | 通过（1 个用例） |
| `go test -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/httpserver/` | 通过（3 个包，531 个用例） |
| `go test -count=1 ./tests/unit/...` | 通过（21 个包，1698 个用例） |
| `go test -count=1 ./tests/integration/local/...` | 通过（14 个包，458 个用例） |
| `go test -count=1 ./tests/integration/pi/... ./tests/integration/ssh/...` | 通过（2 个包，7 个用例，真实远程目标已启用，未跳过） |
| `go test -count=1 ./tests/integration/remote/...` | 通过（4 个包，13 个用例，真实远程 Docker 已启用，未跳过） |
| `go test -count=1 ./tests/e2e/...` | 通过（2 个包，58 个用例） |
| `go test -count=1 ./...` | 通过（69 个包，2234 个用例） |
| `gofmt -l internal cmd tests` | 无输出 |
| `go vet ./...` | 无输出 |
| `go build ./...` | 通过 |

**本轮没有失败用例**，也没有被跳过的用例：`tests/config/ssh.yml` 的 `ssh.enable` 与
`tests/config/docker.yml` 的 `docker.remote_enabled` 均为 `true`，远程用例真实执行。

## 6. 安全边界与剩余风险

### 6.1 安全边界

- **只读**：不创建记录、不提交 / 推送 / 合并、不核验、不恢复、不修改任务状态（四个用例
  分别在应用层、HTTP 层、真实服务层断言）；
- **无远程副作用**：查询服务的依赖契约只有两个存储读取方法，HTTP 装配故意传入"没有 SSH"
  的服务端，用例真实通过；
- **不跨任务泄露**：任务归属由路径与记录 `task_id` 双重约束，真实服务用例逐条对照；
- **不模糊语义**：提交 / 推送 / 合并 / 记录可信度 / 核验结论五个维度分别输出；
  `unknown`、`pending`、`unconfirmed`、`unverifiable` 都按真实值输出，不归并为失败或成功；
- **不拼接错误**：`error_summary` 与 `persist_error` 直接来自持久化内容，写入时已脱敏；
- **`500` 一律脱敏**，原始原因只写服务端日志；
- 未新增数据库迁移，未修改脱敏算法与长度上限。

### 6.2 剩余风险

1. **前端尚未接入**：本轮只做后端 API。三阶段状态展示归 **P4-M3（M3-5）**，核验操作
   入口归 **P4-M4（M4-2）**。
2. **无分页**：记录数由"一次任务执行产生的保存次数"决定，量级很小。若未来出现一个任务
   积累大量记录的现场，应在此接口上统一加 `current` / `size`，而不是新增第二个入口。
3. **`recovery_status` 是记录级字段**：一条记录里提交、推送、合并依次推进，但恢复结论只有
   一个字段，语义上属于"第一个仍待确认的阶段"（见 `internal/domain/code_save_recovery.go`
   的注释）。因此该字段必须与 `stage` 一起读，不能单独解释为"整条记录都已核验"。本轮
   DTO 如实按字段输出，不做推断。
4. **`DockerID` 与 `Repository` 对外暴露**：二者在库中已是脱敏 / 无害值（`repository` 经
   `domain.ParseGitRepositoryURL` 脱敏，`docker_id` 是容器 ID）。若后续要求更严格的收敛，
   应在 DTO 层统一裁剪，而不是改存储。
5. **路径标识非法返回 404 而非 400**：与既有执行记录接口一致，但意味着前端无法区分
   "路径写错"与"任务不存在"。这是有意的一致性取舍，不是遗漏。

## 7. 退出条件核对

| 条件 | 状态 |
|---|---|
| 存在只读的代码保存记录列表接口，路径与现有路由风格一致 | 满足（`GET /api/v1/tasks/{taskID}/code-saves`） |
| 响应以 `domain.CodeSaveRecord` 为唯一数据来源，字段真实存在、未改名、未补造 | 满足（`codeSaveRecordListEntry` 逐字段映射；真实服务用例逐字段对照落盘数据） |
| 记录列表返回真实 `id`，可供后续核验使用 | 满足（`TestCodeSaveListReturnsTaskRecords`、`TestCodeSaveListOverRealServer`） |
| 提交、推送、合并三阶段状态独立返回，未被压缩为单一结论 | 满足（`TestCodeSaveListKeepsStagesIndependent`） |
| `persist_status` 空值等价于 `persisted`、`recovery_status` 空值归一化为 `unverified` | 满足（`TestCodeSaveListKeepsPersistAndRecoverySemantics`、`TestCodeSaveListOverRealServer`） |
| `unknown` / `pending` / `unconfirmed` / `unverifiable` 按真实值输出 | 满足（同上两条用例） |
| `error_summary` / `persist_error` 按领域与存储契约输出，不拼接新错误 | 满足（DTO 直接映射；用例断言具体内容） |
| 查询只读、无远程副作用（SSH / Docker / Git / 状态写入 / 核验） | 满足（`CodeSaveQueryStore` 只含读取方法；`TestCodeSaveQueryIsReadOnly`、`TestCodeSaveListQueryIsReadOnly`、`TestCodeSaveListOverRealServerIsReadOnly`） |
| 应用层校验任务存在且未被逻辑删除；不存在与已删除返回未找到 | 满足（`TestCodeSaveQueryRejectsDeletedTask`、`TestCodeSaveQueryRejectsUnknownTask`、`TestCodeSaveListRejectsUnknownTasks`） |
| 空记录列表与任务不存在可区分 | 满足（`TestCodeSaveQueryReturnsEmptyList`、`TestCodeSaveListReturnsEmptyList`、`TestCodeSaveListDistinguishesMissingTaskFromEmptyList`） |
| 存储错误不被伪装为空列表 | 满足（`TestCodeSaveQueryStorageFailureIsNotNotFound`、`TestCodeSaveListStorageFailureIsNotNotFound`） |
| 依赖缺失返回 `503`，不被伪装成 `404` 或空列表 | 满足（`TestCodeSaveListRequiresService`、`TestCodeSaveListWithoutServiceOverRealServer`） |
| 不支持的方法返回 `405` 且 `Allow` 正确，且无副作用 | 满足（`TestCodeSaveListRejectsUnsupportedMethods`） |
| 未知路径不误命中本接口，现有核验 / 续跑 / 执行记录接口行为不变 | 满足（`TestCodeSaveListRejectsUnknownShapes`；路由形状按段数互斥，既有测试包全部通过） |
| 路由分发顺序正确，不被任务详情处理器提前接收 | 满足（`isCodeSaveListPath` 位于任务处理器之前、`isCodeRecoveryPath` 之前但形状互斥） |
| 未新增无关的单条记录查询或写入端点 | 满足（§2.5，并由 `TestCodeSaveListRejectsUnknownShapes` 锁定） |
| 应用层、HTTP 层、真实 SQLite 集成、统一服务装配四层测试实际执行并通过 | 满足（§5.5，`go test ./...` 2234 个用例全部通过） |
| 未修改 CodeSaver 执行逻辑、核验与恢复规则、容器服务与数据库 Schema | 满足（§4"未修改"清单；`git status` 无迁移文件变更） |
| 路线图与交付文档已更新 | 满足（`README.md`、`ROADMAP.md`、`P4-product-experience-optimization.md` 与本文件） |

## 8. 下一阶段

**P4-M2-7：单步骤执行、顺序执行、开发—验证闭环、修复周期与计划生成的 Web/API 操作入口**
——覆盖 G14（单步诊断入口）与 G13（计划生成）。

该阶段应先盘点现有后端路由与前端调用的缺口，再按"端到端主流程"优先级排序实施，不要一次性
把所有入口堆进 UI。当前已知的后端能力（均有 HTTP 入口，缺少 Web 操作）：`…/steps/{stepID}/
execute`、`…/execute-steps`、`…/execute-and-validate`、`…/open-repair-cycle`、
`…/steps/{stepID}/attempts/{attemptID}/validate`、`…/plan`、`…/code-initialize`。

Web 端的验证历史页面（M3-2）与代码保存三阶段状态展示（M3-5）归 P4-M3，核验操作入口
（M4-2）归 P4-M4，均不在本轮范围。
