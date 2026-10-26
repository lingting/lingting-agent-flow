# P4-M2-2 —— 环境查询 HTTP API（列表、版本与构建状态）

> 实施依据：`master` 上 `internal/app/environments{,_query,_build}.go`、
> `internal/app/docker_session.go`、`internal/domain/environment.go`、
> `internal/storage/{interfaces.go,sqlite/environment_versions.go}`、
> `internal/httpapi/*`、`internal/httpserver/server.go`、`internal/server/server.go`
> 与 `tests/` 的实际代码核实。对应 P4-M1 缺口 **G3**（见
> [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md) §5、§6）。

## 1. 结论摘要

本轮把**环境只读查询**接入 HTTP，闭合 G3 的后端断链：`/start` 在 `container` 阶段失败
的直接原因（"无 HTTP 入口可读取环境与版本"）已解除。

交付内容：

1. `GET /api/v1/environments?host_id=<host-id>` —— 按执行节点列出环境与全部版本；
2. `GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=<host-id>` —— 单版本查询；
3. 新增只依赖存储的应用层查询服务 `app.EnvironmentQueryService`，使纯读取**不再要求
   SSH / Docker 依赖**，也不会产生任何远程副作用；
4. 完整依赖装配（`httpapi.Deps` → `httpserver.Options` → `internal/server.Server`）；
5. 三层测试（应用层、HTTP 层、服务装配与真实 HTTP 服务集成）。

本轮**未实现**：环境 CRUD、环境构建/重建入口、Dockerfile 修改或读取、环境数据库表或
迁移、前端任务创建弹窗改造、统一任务启动 `/api/v1/tasks/{id}/start` 的前端接入
（归 P4-M2-3）、UI 美化与无关重构。

## 2. API 契约

### 2.1 环境列表

```text
GET /api/v1/environments?host_id=<host-id>
```

| 参数 | 位置 | 必填 | 说明 |
|---|---|---|---|
| `host_id` | 查询参数 | 是 | 执行节点标识；缺失或空白返回 `400` |

只支持 `GET` 与 `HEAD`，其他方法返回 `405` 且 `Allow: GET, HEAD`。

响应（`records` + `total`，与角色、执行节点列表一致；环境数量有限，不分页）：

```json
{
  "records": [
    {
      "type": "language",
      "name": "go",
      "current_version": "v0001",
      "latest_version": "v0002",
      "versions": [
        {
          "version": "v0001",
          "number": 1,
          "build_status": "succeeded",
          "usable": true,
          "image_ref": "agentflow-language-go:v0001-abcdefabcdef",
          "image_id": "sha256:…",
          "parent_version": "v0001",
          "created_at": "2026-10-26T08:00:00Z",
          "built_at": "2026-10-26T08:05:00Z"
        },
        {
          "version": "v0002",
          "number": 2,
          "build_status": "failed",
          "usable": false,
          "image_ref": "agentflow-language-go:v0002-abcdefabcdef",
          "parent_version": "v0001",
          "error_summary": "构建失败: 镜像核实未通过",
          "created_at": "2026-10-26T09:00:00Z"
        }
      ]
    }
  ],
  "total": 1
}
```

### 2.2 单个环境版本

```text
GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=<host-id>
```

| 路径段 / 参数 | 说明 |
|---|---|
| `{type}` | `base` 或 `language`；其他取值返回 `400` |
| `{name}` | 语言环境名称（`^[a-z0-9][a-z0-9-]{0,31}$`）；基础环境必须写 `base`，否则 `400` |
| `{number}` | 版本号整数，范围 `1..9999`；非整数或越界返回 `400` |
| `host_id` | 查询参数，必填；缺失返回 `400` |

响应是单个版本对象，字段与列表中的 `versions[]` 完全一致（同一构造函数生成）。

**路径设计说明**：环境引用由**两个**路径段表达，不使用
`domain.EnvironmentRef.Label()` 的 `type/name` 单段形式。斜杠在 URL 中是路径分隔符，
把 `language/go` 当作单一路径段会依赖隐式转义解码规则，容易与其他接口的段数解析冲突。
两段形式与执行节点、任务的路径风格一致，解析规则显式且唯一。

### 2.3 字段语义

| 字段 | 语义 |
|---|---|
| `type` / `name` | 直接取 `domain.EnvironmentType` / `EnvironmentRef.Name`；基础环境 `name` 固定为 `base` |
| `current_version` | **当前可用版本**：最后一个 `Usable()` 为真的版本标签；没有可用版本时省略。**不是**最新版本 |
| `latest_version` | **最新创建版本**：版本号最大的版本标签，与可用性无关 |
| `versions[]` | 该环境全部版本，按版本号升序（存储层 `ORDER BY … version_number`，不依赖返回顺序） |
| `build_status` | `domain.BuildStatus` 的真实取值：`pending` / `building` / `succeeded` / `failed` |
| `usable` | 服务端按 `domain.EnvironmentVersion.Usable()`（构建成功 **且** 已核实镜像 ID）计算 |
| `image_ref` | 创建版本时固定的镜像引用，供容器创建使用 |
| `image_id` | 构建成功后核实的实际镜像 ID；非成功版本省略 |
| `parent_version` | 语言环境依赖的父基础环境版本标签；基础环境省略 |
| `error_summary` | 构建失败的可诊断摘要（已由领域层脱敏）；成功版本省略 |
| `created_at` / `built_at` | 版本创建时间 / 构建完成时间；未构建成功时 `built_at` 省略 |

**不可由前端推断**：`usable` 与 `current_version` 均由服务端计算。未成功构建的版本
不会因为"存在记录"或"有 image_ref"就被当作可运行版本。前端选择器据此禁用不可用版本，
但服务端 `app.ExecutionEnvironmentService` / `domain` 的准入规则仍是唯一防线，本轮未降低
任何后端校验要求。

### 2.4 错误映射

| 场景 | 状态码 | `error.code` |
|---|---|---|
| 缺少 / 空白 `host_id` | `400` | `invalid_request` |
| 非法环境类型、非法名称、基础环境名称不符、版本号非整数或越界 | `400` | `invalid_request` |
| 执行节点不存在或已删除 | `404` | `not_found` |
| 该节点上该环境没有任何版本 | `404` | `not_found` |
| 该环境不存在指定版本 | `404` | `not_found` |
| 路径形状不属于本阶段资源（如 `…/build`、`…/dockerfile`、单段环境路径） | `404` | `not_found` |
| 不支持的方法 | `405` + `Allow: GET, HEAD` | `method_not_allowed` |
| 环境查询服务未初始化 | `503` | `internal_error` |
| 存储或内部错误 | `500` | `internal_error` |

错误映射依据**错误类别**（`errors.Is`），不匹配错误文本。`500` 的响应消息固定为
`服务内部错误`，原始原因只写日志：不暴露堆栈、存储实现、内部路径或凭据。

## 3. 代码证据

| 关注点 | 位置 |
|---|---|
| 只读查询服务（新增） | `internal/app/environments_query.go` 的 `EnvironmentQueryService`、`NewEnvironmentQueryService`、`List` / `Get` / `GetVersion` / `History` |
| 查询错误类别（新增） | 同文件的 `ErrEnvironmentInvalidInput`、`ErrEnvironmentHostNotFound`、`ErrEnvironmentNotFound`、`ErrEnvironmentVersionNotFound` |
| 环境概览与汇总规则 | `internal/app/environments.go` 的 `EnvironmentSummary`；`summarizeEnvironments`（迁移到查询服务） |
| 有副作用的服务（未改动语义） | `internal/app/environments.go` 的 `EnvironmentService`（`Preflight` / `Init` / `CreateVersion`）；`internal/app/environments_build.go` 的 `Build`；`internal/app/environments_query.go` 的 `ReadDockerfile` / `resolveVersion` |
| 领域取值与可用性 | `internal/domain/environment.go` 的 `EnvironmentRef`、`EnvironmentVersion.Usable`、`BuildStatus`、`VersionNumber` 范围 |
| 存储查询与排序 | `internal/storage/sqlite/environment_versions.go` 的 `GetEnvironmentVersion` / `ListEnvironmentVersions`（`ORDER BY host_id, env_type, env_name, version_number`） |
| HTTP 路径、DTO、处理器 | `internal/httpapi/environments.go`（`PathEnvironments`、`EnvironmentListResponse`、`EnvironmentVersionResponse`、`environmentHandler`） |
| 路由注册 | `internal/httpapi/router.go` 的 `NewHandler`（`PathEnvironments` 与 `environmentPathPrefix` 分别注册）、`router.environmentsHandler` |
| 依赖注入 | `internal/httpapi/router.go` 的 `Deps.Environments`；`internal/httpserver/server.go` 的 `Options.Environments` |
| 服务装配 | `internal/server/server.go` 的 `environmentQueryService` → `startServices` → `startHTTP` |
| CLI 只读命令改用查询服务 | `internal/cli/docker.go` 的 `withEnvironmentQuery`；`internal/cli/docker_env.go` 的 `list` / `show` / `history` |

### 3.1 为什么新增独立的查询服务

`EnvironmentService` 的构造器 `NewEnvironmentService(DockerServiceOptions)` 要求
`SSH != nil`，因为初始化、创建版本、构建与读取 Dockerfile 都必须在远程工作空间内建立
SSH / SFTP / Docker 会话。但环境**元数据**（环境、版本、构建状态、镜像引用、镜像 ID、
父版本、失败摘要）全部持久化在 SQLite 中，读取它们与远程主机无关。

若直接复用 `EnvironmentService`，纯读取接口就必须携带 SSH 依赖：
`agentflow server` 在缺少 SSH 能力时会让环境查询返回 503，而实际上数据库里明明有可用
数据。因此本轮做了**最小的应用层拆分**：

- `EnvironmentQueryService` 只持有 `storage.Store`，构造只需数据库；
- 有副作用的操作全部留在 `EnvironmentService`，其构造方式与语义**未改动**；
- `summarizeEnvironments`（版本归并规则）只有一份实现，两个服务共用，没有复制规则；
- 未引入通用 Repository / Factory 抽象，也未新增第二套环境状态机或构建逻辑。

CLI 的 `docker env list` / `show` / `history` 同步改用只读入口，因此这三个命令不再需要
SSH 连接即可工作；`show --dockerfile` 仍走 `EnvironmentService.ReadDockerfile`，因为
Dockerfile 正文只存在于远程工作空间。

### 3.2 请求上下文与副作用边界

- 查询使用 `r.Context()`，随请求取消而中止；
- 只读路径不建立 SSH / SFTP 连接，不创建远程目录，不写 SQLite，不触发构建；
- 装配位置在 `startServices` 中按既有顺序（角色 → 执行记录 → 环境 → 执行节点 → 后台服务），
  未改变任何后台服务的注册顺序；
- 未初始化时接口返回 `503`，不伪造空列表。

## 4. 测试证据

全部使用真实 SQLite + 正式迁移 + 项目现有 fixture；测试产物保留在
`os.TempDir()/lingting-agent-flow-tests/` 下的随机目录，运行时输出 `TEST_ARTIFACT_DIR`。

### 4.1 应用层与数据语义（`tests/unit/app/environments_query_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestEnvironmentListSummarizesVersions` | 同一节点下多个环境的列表与汇总（基础 + 语言） |
| `TestEnvironmentListDistinguishesCurrentFromLatest` | 当前可用版本（v0001）与最新创建版本（v0003）分别表达 |
| `TestEnvironmentListWithoutUsableVersionKeepsCurrentEmpty` | 只有 `building` 版本时 `current_version` 为空、`latest_version` 仍存在 |
| `TestEnvironmentListIsSortedByVersion` | 乱序写入后连续三次查询都返回 `1,2,3` 升序（排序稳定可复现） |
| `TestEnvironmentListEmptyHostReturnsEmptyList` | 节点存在但没有版本时返回空列表而非错误 |
| `TestEnvironmentListRejectsUnknownHost` | 不存在的节点返回 `ErrEnvironmentHostNotFound` |
| `TestEnvironmentListRejectsEmptyHost` | 空节点标识返回 `ErrEnvironmentInvalidInput` |
| `TestEnvironmentGetVersionReturnsMetadata` | 单版本返回真实构建状态、镜像 ID、构建时间、失败摘要 |
| `TestEnvironmentGetVersionRejectsMissingVersion` | 不存在的版本返回 `ErrEnvironmentVersionNotFound` |
| `TestEnvironmentGetVersionRejectsInvalidInput` | 未知类型 / 非法名称 / 基础环境名称不符 / 版本号 0 / 版本号超上限 |
| `TestEnvironmentGetWithoutVersionsReturnsNotFound` | 没有版本的环境按不存在处理 |
| `TestEnvironmentQueryStorageFailureIsNotAnEmptyList` | 关闭数据库后查询返回错误，不被归类为资源不存在 |
| `TestEnvironmentQueryServiceRequiresStore` | 存储为空时构造失败 |

### 4.2 HTTP 层（`tests/unit/httpapi/environments_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestEnvironmentListReturnsRealRecords` | 真实记录、`records`/`total`、`current_version` 与 `latest_version` 语义、`usable`、`image_ref`、失败摘要 |
| `TestEnvironmentListOmitsInternalFields` | 响应不含 Dockerfile 正文、哈希字段、`private_key`、`password`、`remote_path`、`ssh` |
| `TestEnvironmentVersionReturnsRealBuildStatus` | 成功版本返回镜像 ID 与构建时间；失败版本 `usable=false`、无镜像 ID、有摘要 |
| `TestEnvironmentListRejectsMissingHostID` | 缺少 `host_id` → `400` + `invalid_request` |
| `TestEnvironmentListRejectsUnknownHost` | 节点不存在 → `404` + `not_found` |
| `TestEnvironmentListEmptyHostReturnsEmptyList` | 空列表为 `[]`，`total` 为 `0` |
| `TestEnvironmentVersionRejectsUnknownVersion` | 版本不存在 → `404` |
| `TestEnvironmentVersionRejectsInvalidParameters` | 6 类非法取值 → `400` + `invalid_request` |
| `TestEnvironmentVersionRejectsMissingHostID` | 版本查询同样要求 `host_id` |
| `TestEnvironmentUnknownSubPathsAreNotFound` | `…/build`、`…/dockerfile`、单段与两段环境路径 → `404` |
| `TestEnvironmentRejectsUnsupportedMethods` | 两个接口 × 4 种方法 → `405` + `Allow: GET, HEAD` |
| `TestEnvironmentWithoutServiceReturnsUnavailable` | 依赖缺失 → `503` |
| `TestEnvironmentStorageFailureIsNotAnEmptyList` | 存储错误 → `500`，且响应不含 `sqlite` 字样 |
| `TestEnvironmentQueryServiceIsReadOnly` | 查询前后版本记录条数、状态与镜像 ID 完全不变 |
| `TestEnvironmentVersionNotFoundIsDistinguishable` | 404 映射来自错误类别而非文本匹配 |

### 4.3 服务装配与真实 HTTP 服务

| 用例 | 覆盖行为 |
|---|---|
| `tests/integration/local/httpserver/environments_test.go` 的 `TestEnvironmentListOverRealServer` | 真实 HTTP 服务 + 正式依赖注入：列表返回真实记录与版本语义 |
| 同文件 `TestEnvironmentVersionOverRealServer` | 单版本返回真实构建状态、父版本与失败摘要 |
| 同文件 `TestEnvironmentQueryDoesNotMutateRemoteOrStore` | 两次查询后持久化记录完全不变 |
| 同文件 `TestEnvironmentQueryErrorsOverRealServer` | 真实服务上的 400 / 404 / 405 与统一错误结构、`Allow` 头 |
| 同文件 `TestEnvironmentQueryWithoutServiceOverRealServer` | 不注入服务时真实服务返回 `503` |
| `tests/unit/server/environments_test.go` 的 `TestServerWiresEnvironmentQuery` | `server.New` + `Run` 真实装配：打开真实数据库与迁移，由 `Server` 自行构造查询服务并注入 HTTP，列表与单版本接口均可读 |

### 4.4 本轮实际执行结果

| 命令 | 结果 |
|---|---|
| `go test -count=1 ./tests/unit/...` | 通过（21 个包，1653 个用例） |
| `go test -count=1 ./tests/integration/local/...` | 通过（14 个包，448 个用例） |
| `go test -count=1 ./tests/integration/pi/... ./tests/integration/ssh/...` | 通过（2 个包，7 个用例） |
| `go test -count=1 ./tests/integration/remote/...` | 通过（4 个包，13 个用例） |
| `go test -count=1 ./tests/e2e/...` | 通过（2 个包，58 个用例） |
| `gofmt -l internal cmd tests` | 无输出 |
| `go vet ./...` | 无输出 |
| `go build ./...` | 通过 |

真实远程用例（`tests/integration/remote/**`）本轮实际执行并通过；`tests/config/pi.yml`
与 `tests/config/ssh.yml` 中 `enable` 为 `true`，因此这些用例真实访问了配置的远程目标，
不是跳过（`grep SKIP` 输出为 0）。

**如实记录的波动**：首次执行 `go test -count=1 ./tests/unit/... ./tests/integration/local/...`
时有 2 个与本轮改动无关的用例失败，二者都是时间敏感用例在全量并行负载下的偶发失败：

| 用例 | 首次失败信息 |
|---|---|
| `tests/unit/app` 的 `TestValidationInterruptionKeepsAttemptConsistent` | `sqlite: 读取任务失败: context deadline exceeded` |
| `tests/unit/app` 的 `TestWorkflowHeartbeatKeepsLeaseAlive` | `续租后租约必须仍然有效，实际到期于 …` |

单独运行这两个用例、以及重跑整个 `./tests/unit/app/`（303 个用例）与
`./tests/unit/... ./tests/integration/local/...`（35 个包，2101 个用例）均通过。
两者都未触及本轮改动的代码路径（验证中断与租约续租），因此判定为并行负载下的
既有时序脆弱性，不是本轮引入的回归；但**这一结论未进一步追查根因**，已记入 §6。

## 5. 安全边界

- 响应只包含环境选择与诊断必需字段，**不含** Dockerfile 正文、远程路径、私钥内容、
  口令、令牌或任何认证材料（`TestEnvironmentListOmitsInternalFields` 逐项断言）；
- `500` 一律脱敏为 `服务内部错误`，原始原因只写日志；
- `host_id`、环境类型、名称与版本号全部经领域层校验后才进入存储查询，路径解析显式按
  段数与位置进行，不存在隐式转义解码；
- 只读路径不写数据库、不建远程连接、不改动任何 Docker 资源；
- 本阶段未放宽任何后端准入校验：不可用版本仍会被任务启动与执行环境准备拒绝。

## 6. 剩余风险与未覆盖场景

1. **前端尚未接入**：本轮只做后端。任务创建弹窗仍需手工填写环境名称与版本，且不会展示
   真实构建状态。前端接入归 **P4-M2-3**（与统一启动 `/start` 一起）。
2. **构建入口仍只有 CLI**：`POST …/build` 未实现。用户仍需用
   `agentflow docker init` / `docker env build` 生成并构建版本，之后才能通过本接口看到
   可用版本。是否提供 Web 构建入口取决于后续产品取舍（P4-M1 §5 已标注为"次之"）。
3. **`current_version` 的语义边界**：它表示"该环境最后一个构建成功且已核实镜像 ID 的
   版本"，与 `app.EnvironmentService.currentUsableVersion` 一致；但它不校验镜像在远程
   主机上是否仍然存在（那需要远程访问）。真正的可用性判定仍在容器创建时发生。
4. **无分页**：环境数量有限（每节点通常不超过十余个），与角色、执行节点列表保持一致；
   若未来出现大量环境需要分页，应统一在三个列表接口上处理，而不是只改这一个。
5. **已删除节点**：`GetExecutionHost` 本身过滤 `deleted_at`，因此逻辑删除的节点返回 `404`；
   未新增"包含已删除"的查询入口。
6. **未覆盖场景**：并发查询与并发构建并存时的可观察性未单独验证（读路径不加锁，
   构建写入会更新构建状态，列表读到的是某一时刻的快照）；`unknown` 构建状态不会出现
   （`domain.BuildStatus` 只有四个取值，未知取值会被存储层原样读回但不会由本阶段产生）。
7. **既有的时序脆弱性**：`TestValidationInterruptionKeepsAttemptConsistent` 与
   `TestWorkflowHeartbeatKeepsLeaseAlive` 在全量并行负载下偶发失败（详见 §4.4）。
   二者与本轮改动无关，本轮未修改其代码或断言，也未追查根因；修复应作为独立任务处理。

## 7. 退出条件核对

| 条件 | 状态 |
|---|---|
| 环境列表接口按执行节点返回真实记录（类型、名称、引用、当前/最新版本、版本列表） | 满足（`TestEnvironmentListReturnsRealRecords`、`TestEnvironmentListOverRealServer`） |
| 每个版本给出真实构建状态与 `image_ref` | 满足（`TestEnvironmentVersionReturnsRealBuildStatus`） |
| `current_version` 遵循服务端可用版本判定，`latest_version` 单独表达 | 满足（`TestEnvironmentListDistinguishesCurrentFromLatest`） |
| 版本可用性由服务端计算，未成功构建的版本不被认定为可运行 | 满足（`usable` 来自 `domain.EnvironmentVersion.Usable`，三个层级测试断言） |
| 列表为空时返回合法空列表 | 满足（`TestEnvironmentListEmptyHostReturnsEmptyList`） |
| 版本排序稳定可复现 | 满足（`TestEnvironmentListIsSortedByVersion`） |
| 单版本查询返回真实状态与元数据，且不泄露 Dockerfile / 凭据 | 满足（`TestEnvironmentVersionOverRealServer`、`TestEnvironmentListOmitsInternalFields`） |
| 依赖装配接入 `httpapi.Deps` / `httpserver.Options` / `internal/server.Server` | 满足（`TestServerWiresEnvironmentQuery`） |
| 依赖未初始化返回 `503`，存储错误返回 `500` 且不伪装空列表 | 满足（`TestEnvironmentWithoutServiceReturnsUnavailable`、`TestEnvironmentStorageFailureIsNotAnEmptyList`、`TestEnvironmentQueryWithoutServiceOverRealServer`） |
| 不支持的方法返回 `405` 且设置正确 `Allow` | 满足（`TestEnvironmentRejectsUnsupportedMethods`） |
| 查询不产生远程副作用 | 满足（服务只依赖存储；`TestEnvironmentQueryDoesNotMutateRemoteOrStore`、`TestEnvironmentQueryServiceIsReadOnly`） |
| 相关测试实际执行并通过，证据与文档一致 | 满足（见 §4.4） |

## 8. 下一阶段

**P4-M2-3：前端接入统一任务启动 `POST /api/v1/tasks/{taskID}/start`**（覆盖 G1），
并复用本轮接口展示真实环境与构建状态，替换任务创建弹窗中手填的环境名称与版本。
环境构建/重建入口是否上 Web，在 M2-3 之后再决定。
