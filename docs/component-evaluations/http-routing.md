# 组件评估：HTTP 服务、API 路由与静态资源服务

> **状态：审计结论，未实施。**
> 本轮没有修改任何生产代码、前端代码、`go.mod` / `go.sum` 或现有测试，没有引入 Chi 或任何其他
> 第三方组件，没有运行任何测试。本文只是替换前的可行性审计记录，结论见第 13 节。

- 审计时间：2026-11-05
- 仓库 HEAD：`b00c8b71aaf1a51dfcfa4b5e72a4b0caa8c0625b`（`:fire: 移除预见dockerfile源的操作`）
- 审计对象：`internal/httpapi`、`internal/httpserver`、`internal/webui`、`internal/server`（HTTP 装配部分）、
  `internal/lifecycle`（仅职责边界）
- Go 版本：`go.mod` 声明 `go 1.24.5`，本机工具链 `go1.24.5 windows/amd64`
- 审计方式：逐文件阅读本仓库实现；阅读本机 `go1.24.5` 标准库源码（`net/http/server.go`、
  `net/http/pattern.go`、`net/http/fs.go`、`embed/embed.go`）；通过 Go 模块代理取得
  `github.com/go-chi/chi/v5@v5.5.0` 的完整源码并逐文件阅读（未改动本仓库的 `go.mod` / `go.sum`）。
  `github.com` 与 `proxy.golang.org` 在本机解析到内网地址，抓取工具按策略拒绝，因此上游结论以模块
  源码为准，下文每条行为结论都能对应到具体源文件。
- 相关文档：[http-api.md](../http-api.md)（接口契约）、[frontend.md](../frontend.md)（请求分发与静态资源规则）、
  [workspace.md](../workspace.md)（包职责）、[architecture.md](../architecture.md)（执行链路边界）

## 0. 一条影响审计结论的外部指令

本次审计按用户指令采用以下前提，与 [frontend.md](../frontend.md) 现有的 SSE 描述存在冲突，冲突时以该
指令为准：

> 允许破坏 SSE 的更新，长连接可以改用 WebSocket。

这意味着 **SSE 不再作为硬约束**：

- 不要求候选方案保留 `http.Flusher` 的可用性，也不禁止会替换 `http.ResponseWriter` 的中间件包装。
- 现有 SSE 协议（帧格式、`Last-Event-ID` 游标、心跳注释帧）可以在后续改造中替换为 WebSocket。
- 但本轮审计只评估「路由 / 静态资源 / Server 生命周期 / 中间件」的替换可行性；SSE → WebSocket 是独立的
  协议迁移，涉及前端改动与新增依赖，不在本轮实施范围内（见 8.10、12.3）。

本文其余部分描述的是**当前代码的真实行为**（仍然是 SSE）。凡结论依赖「SSE 可破坏」的地方都单独标注。

## 1. 结论摘要

1. **HTTP Server 继续使用 Go 标准库**。`internal/httpserver/server.go` 中没有重复实现标准库已经提供的
   能力：它只做「监听器 + `http.Server` 装配 + Shutdown 编排 + 关闭信号」，核心仍然是
   `net.Listen` / `http.Server.Serve` / `http.Server.Shutdown` / `RegisterOnShutdown`。
2. **API 路由建议统一到标准库 `http.ServeMux`（方案 B）**。这是本轮唯一有实质收益的替换：可以把
   `tasksHandler` 里按顺序逐段试匹配的 20 个形状判定，以及第 3.3 节列出的全部
   `parse*Path` / `is*Path` / `match*Path` 辅助函数，替换为一张声明式路由表 + `Request.PathValue`。
   前提是必须同时给出 404 / 405 的 JSON 适配器，不能让 `ServeMux` 自带的纯文本 404 / 405 直接对外。
3. **不建议引入 Chi（方案 C）**。Chi v5.5.0 零依赖、MIT、活跃维护，但它相对标准库多出的能力（Group /
   Route / Mount / 中间件栈）本项目一个都不需要；而它自带的 404 / 405 同样不是项目统一 JSON 协议，
   `HEAD` 需要额外注册或 `middleware.GetHead`，路径参数不做百分号解码。换掉自研路由的净收益不足以
   抵消新增依赖。
4. **WebUI 文件服务保留项目自定义 Handler（方案 A）**，只在需要 Range / 条件请求时才考虑用
   `http.ServeContent` 替换 `writeContent`（方案 D 的局部用法）。`http.FileServer` / `http.FileServerFS`
   无法表达项目必须保留的 6 条产品规则（`/api` 保留、目录禁止列出、缺失 `.js` 必须 404、SPA 回退、
   分级缓存、路径清理语义），强行替换会引入包装层，反而更复杂。
5. **Request ID 保留自研**。Chi 的 `middleware.RequestID` 会直接信任调用方传入的 `X-Request-Id`，
   与项目「不接受外部传入值」的规则冲突；自研实现只有 42 行且用 128 位加密随机数。
6. **最终结论：部分值得替换。** 只有「API 路由分发」值得替换为纯标准库方案；Server、静态资源服务、
   Request ID 保持现状。

## 2. 当前 HTTP 服务架构

### 2.1 三层的实际分工

```text
internal/server.Server            ← 进程级生命周期：配置 → 数据库 → 后台服务 → HTTP → 逆序清理
└── internal/httpserver.Server    ← HTTP 生命周期：监听、http.Server 装配、Shutdown、关闭信号
    └── routes(api, web)          ← 单一分叉点：IsAPIPath ? api : web
        ├── httpapi.NewHandler    ← withRequestID(ServeMux)  → 全部 /api 路由
        └── webui.Handler         ← fs.FS 读取 + SPA 回退 + 缓存头
```

请求进入后的完整分叉只有一处，位于 `internal/httpserver/routes.go`：

```go
if httpapi.IsAPIPath(r.URL.Path) { api.ServeHTTP(w, r); return }
web.ServeHTTP(w, r)
```

`httpapi.IsAPIPath`（`internal/httpapi/path.go`）按路径段边界判定：
`path == "/api" || strings.HasPrefix(path, "/api/")`，因此 `/apiary`、`/apix` 落到 web 侧。

### 2.2 自研部分与标准库部分的职责边界

| 能力 | 当前实现 | 归属 |
|---|---|---|
| TCP 监听、连接 accept、keep-alive、请求解析 | `net.Listen` + `http.Server.Serve` | 标准库 |
| 慢请求头防护 | `http.Server.ReadHeaderTimeout = 10s` | 标准库（配置项） |
| 优雅关闭、等待在途请求 | `http.Server.Shutdown(timeoutCtx)` | 标准库 |
| 关闭时通知长连接 | `http.Server.RegisterOnShutdown` + `close(streamShutdown)` | 标准库钩子 + 自研业务信号 |
| 关闭超时到期后强制断开 | `http.Server.Close()` | 标准库 |
| 进程生命周期与清理顺序 | `internal/server.Server`（`startup` / `wait` / `shutdown` / `waitServe`） | 自研 |
| 后台服务生命周期 | `internal/lifecycle.Manager` | 自研 |
| `/api` 与页面的路径分叉 | `internal/httpserver/routes.go` + `IsAPIPath` | 自研（1 个函数） |
| API 路径匹配 | `http.ServeMux`（仅前缀式注册）+ 自研顺序形状判定 | 混合，见第 3 节 |
| API 404 / 405 | 自研 `notFoundHandler` + 各 Handler 的方法校验 | 自研 |
| 统一 JSON 错误协议 | `writeError` / `writeJSON`（`response.go`） | 自研（必须保留） |
| 请求标识 | `withRequestID`（`requestid.go`） | 自研（必须保留） |
| 静态资源读取 | `fs.Stat` + `fs.ReadFile` + 手写 `WriteHeader`/`Write` | 自研 |
| 静态资源 MIME | `webui/mime.go` 显式表 + `mime.TypeByExtension` 兜底 | 自研（Windows 确定性，必须保留） |
| 嵌入产物 | `//go:embed all:dist` + `fs.Sub` | 标准库 |

结论：「当前 HTTP Server 是完全自研的」这一判断不成立。**HTTP 协议层面全部由标准库承担**，自研部分是
生命周期编排、业务信号与错误协议。

## 3. 全部路由分发与路径解析调用链

### 3.1 注册在 `http.ServeMux` 上的模式（现状）

`httpapi.NewHandler`（`internal/httpapi/router.go`）注册了 14 个模式（13 个 API 模式 + 1 个 `/` 兜底），全部
**不带方法**，且 API 侧都是以 `/` 结尾的前缀式子模式：

| 注册模式 | 处理入口 |
|---|---|
| `PathRoles` = `/api/v1/roles` | `router.rolesHandler` |
| `rolePathPrefix` = `/api/v1/roles/` | `router.rolesHandler` |
| `PathProjects` = `/api/v1/projects` | `router.projectsHandler` |
| `projectPathPrefix` = `/api/v1/projects/` | `router.projectsHandler` |
| `PathEnvironments` = `/api/v1/environments` | `router.environmentsHandler` |
| `environmentPathPrefix` = `/api/v1/environments/` | `router.environmentsHandler` |
| `PathHealth` = `/api/v1/health` | `router.healthHandler` |
| `PathSystemInfo` = `/api/v1/system/info` | `router.systemInfoHandler` |
| `PathSettingsConfig` = `/api/v1/settings/config` | `router.settingsConfigHandler` |
| `PathTasks` = `/api/v1/tasks` | `router.tasksHandler` |
| `taskPathPrefix` = `/api/v1/tasks/` | `router.tasksHandler` |
| `PathHosts` = `/api/v1/hosts` | `router.hostsHandler` |
| `hostPathPrefix` = `/api/v1/hosts/` | `router.hostsHandler` |
| `/` | `router.notFoundHandler`（统一 JSON 404） |

也就是说：**一级资源名称由 ServeMux 前缀匹配，二级以下的形状、顺序、方法与错误全部由自研代码决定。**

### 3.2 请求进入 `tasksHandler` 后的分发顺序（现状 20 个判定 + 兜底）

顺序本身就是语义，注释里多处明确写出「必须先判定」，改动顺序会改变 404 / 405 / 503 的归属：

| # | 判定 | 处理入口 | 说明 |
|---|---|---|---|
| 1 | `isTaskLaunchPath` | `router.serveTaskLaunch` | `{taskID}/start`，先于任务服务可用性判定 |
| 2 | `isTaskRetryPath` | `router.serveTaskRetry` | `{taskID}/retry`、`{taskID}/retry-eligibility` |
| 3 | `isTaskContainerPath` | `router.serveTaskContainers` | `{taskID}/containers`、`{taskID}/containers/{id}/{delete\|detach}` |
| 4 | `isCodeSaveListPath` | `router.serveCodeSaveList` | `{taskID}/code-saves`（只依赖存储） |
| 5 | `parseTaskEventStreamPath` | `router.serveTaskEventStream` | `{taskID}/events/stream` |
| 6 | `parseTaskEventsPath` | `router.serveTaskEvents` | `{taskID}/events` |
| 7 | `parseRepairCyclesPath` | `router.serveStepRepairCycles` | `{taskID}/steps/{stepID}/repair-cycles` |
| 8 | `rt.tasks == nil` → 503 | — | 任务服务可用性判定 |
| 9 | `r.URL.Path == PathTasks` | `tasks.list` / `tasks.create` | 集合路径 + 方法分支 |
| 10 | `parseRepairCycleEligibilityPath` | `stepExec.serveRepairCycleEligibility` | 必须早于第 17 步 |
| 11 | `parseOpenRepairCyclePath` | `stepExec.serveOpenRepairCycle` | |
| 12 | `parseExecuteAndValidatePath` | `stepExec.serveExecuteAndValidate` | |
| 13 | `parseStepExecutePath` | `stepExec.serve` | |
| 14 | `isCodeRecoveryPath` | `router.serveCodeRecovery` | `resume-eligibility` / `code-saves/{id}/verify-code-save` |
| 15 | `matchActionPath(planSegment)` | `router.servePlan` | `{taskID}/plan` |
| 16 | `isWorkflowPath` | `router.serveWorkflow` | 4 个 workflow 动作 + 状态查询 |
| 17 | `parseExecuteStepsPath` | `stepExec.serveSteps` | `{taskID}/execute-steps` |
| 18 | `parseValidatePath` | `validation.serve` | `.../attempts/{attemptID}/validate` |
| 19 | `parseInitializeCodePath` | `codeInit.serve` | `.../attempts/{attemptID}/initialize-code` |
| 20 | `isExecutionRecordPath` | `executions.serve` | 第二段是 `steps` 或 `sessions` |
| 21 | 兜底 | `tasks.serve` | 单任务详情 / 取消 / 删除；其余 404 |

### 3.3 路径解析辅助函数清单（全部为自研）

| 函数 | 文件 | 解析形状 / 职责 |
|---|---|---|
| `splitPathSegments` | `executions.go` | 按 `/` 切分并丢弃空段（顺序判定与执行记录分派的公共底座） |
| `actionTaskID` / `matchActionPath` | `workflow.go` | `{taskID}/{action}`，2 段 |
| `statusTaskID` / `matchStatusPath` | `workflow.go` | `{taskID}/workflow`，2 段 |
| `isWorkflowPath` | `router.go` | 组合上面 3 个动作 + 状态查询 |
| `isExecutionRecordPath` | `router.go` | `{taskID}/{steps\|sessions}/…` |
| `parseTaskEventsPath` | `events.go` | `{taskID}/events` |
| `parseRepairCyclesPath` | `events.go` | `{taskID}/steps/{stepID}/repair-cycles` |
| `parseTaskEventStreamPath` | `event_stream.go` | `{taskID}/events/stream` |
| `parseOpenRepairCyclePath` | `step_cycle.go` | `{taskID}/steps/{stepID}/open-repair-cycle` |
| `parseRepairCycleEligibilityPath` | `step_cycle.go` | `{taskID}/steps/{stepID}/repair-cycle-eligibility` |
| `parseStepExecutePath` | `step_execution.go` | `{taskID}/steps/{stepID}/execute` |
| `parseExecuteAndValidatePath` | `step_workflow.go` | `{taskID}/steps/{stepID}/execute-and-validate` |
| `parseExecuteStepsPath` | `step_sequence.go` | `{taskID}/execute-steps` |
| `parseValidatePath` | `validation.go` | `{taskID}/steps/{stepID}/attempts/{attemptID}/validate` |
| `parseInitializeCodePath` | `code_initialize.go` | `…/attempts/{attemptID}/initialize-code` |
| `isCodeRecoveryPath` / `matchSubtasksPath` / `nestedPathID` | `code_recovery.go` | `resume-eligibility`、`code-saves/{recordID}/verify-code-save` |
| `isCodeSaveListPath` | `code_saves.go` | `{taskID}/code-saves` |
| `isTaskContainerPath` / `parseTaskContainerListPath` / `parseContainerDisposalPath` | `task_containers.go` | 2 段与 4 段形状 |
| `isTaskLaunchPath` | `task_launch.go` | `{taskID}/start` |
| `isTaskRetryPath` | `task_retry.go` | `{taskID}/retry[-eligibility]` |
| `parseEnvironmentPath` | `environments.go` | `{type}/{name}[/versions\|/build][/{number}[/dockerfile\|/build-log\|/build-status]]` |
| `roleIDFromPath` | `roles.go` | `/api/v1/roles/{roleID}`，1 段 |
| `validTaskPathID` / `validProjectPathID` / `validHostPathID` | `tasks.go` / `projects.go` / `hosts.go` | 标识非空且不含 `/`（三个同义实现） |

此外每个资源 Handler 内部还有一层手写分派，同样依赖 `strings.TrimPrefix` / `strings.HasSuffix`：

| 分派函数 | 手写分派方式 |
|---|---|
| `taskHandler.serve` | `TrimPrefix(taskPathPrefix)` + `HasSuffix("/cancel")` + `TrimSuffix` |
| `hostHandler.serve` | `TrimPrefix(hostPathPrefix)` + `HasSuffix("/validate")` + `TrimSuffix` |
| `projectHandler.serve` | `r.URL.Path == PathProjects` + `TrimPrefix(projectPathPrefix)` |
| `roleHandler.serve` | `r.URL.Path == PathRoles` + `roleIDFromPath` |
| `environmentHandler.serve` | `r.URL.Path == PathEnvironments` + `== environmentInitPath` + `parseEnvironmentPath` |
| `executionHandler.serve` | `splitPathSegments` 后按段数与段名 `switch`（6 种形状） |
| `containerHandler.serve` | `parseTaskContainerListPath` 后 `parseContainerDisposalPath` 二次尝试 |

### 3.4 404 / 405 / 503 的实际产出位置

| 语义 | 产出方式 | 结果 |
|---|---|---|
| 未知 API 路径 | `router.notFoundHandler` | 404 `not_found`，`接口不存在: <path>` |
| 形状命中但值非法（环境类型/名称/版本号） | `environmentHandler.serve` → `target.err` | 400 `invalid_request` |
| 路径存在但方法不支持 | 各 Handler 自己的 `allowGet` / `allowMethod` / `allowMethods` / `if r.Method != …` | 405 `method_not_allowed` + `Allow` 头 |
| 服务未装配 | 各 Handler 自己的 `== nil` 判定 | 503 `internal_error` |

三点必须记录的事实：

1. **405 的 `Allow` 头字符串与错误消息不是同一套规则生成的。** `Allow` 取值有 9 种
   （`GET, HEAD`、`GET, HEAD, POST`、`GET, HEAD, PUT`、`GET, HEAD, PUT, DELETE`、
   `GET, HEAD, PATCH, PUT, DELETE`、`GET, HEAD, DELETE`、`GET`、`POST`）；消息文案也不统一
   ——`tasks` / `roles` / `projects` / `hosts` 用「该接口只支持 GET 与 POST 请求」形式，
   环境接口直接拼 `Allow` 原文（「该接口只支持 GET, HEAD 请求」），其余多为「该接口只支持 GET 请求」。
   任何替换方案若想保持响应字节不变，就必须把 `Allow` 与 `message` **逐条抄进路由表**，不能靠一个
   通用格式化函数推导。
2. **503 的位置编码了依赖优先级。** 例如 `{taskID}/code-saves` 必须在任务服务可用性判定之前，
   否则依赖缺失会得到误导性的「任务服务未初始化」；`{taskID}/start` 同理。
3. **503 与 405 的先后顺序并不统一**，迁移时必须逐条核对，不能统一处理。已核实的两类：
   - **先 503、后 405**：`roles`（`roleHandler.serve`）、`projects`（`projectHandler.serve`）、
     `hosts`（`router.hostsHandler`）、任务集合（`router.tasksHandler` 第 8 步）、执行记录
     （`isExecutionRecordPath` 分支）、`code-saves` 与 `events` 等只读子资源，以及 `start` / `retry`
     （`serveTaskLaunch` / `serveTaskRetry` 在进入处理器前判服务）。服务缺失时任何方法都得到 503。
   - **先 405、后 503**：`environments` 的全部接口（`allowMethods` 先于 `requireQueries`），以及由
     `router` 直接分发、没有 router 级服务判定的 POST-only 动作——步骤执行、`open-repair-cycle`、
     `execute-and-validate`、`execute-steps`、`validate`、`initialize-code`、`plan`、workflow 动作、
     容器处置（这些处理器的第一段就是 `if r.Method != http.MethodPost`）。
   - 因此路由表除了 `allow` 与 `message`，还要记录「方法校验与服务校验的先后」，否则依赖缺失时的状态码会改变。

## 4. 静态资源服务调用链

```text
http.Server.Handler = routes(api, web)
└── webui.NewHandler(fs.FS, logger)        ← 启动时构造，缺 index.html 时返回 ErrNoIndex
    └── (*Handler).ServeHTTP
        ├── 方法校验：非 GET/HEAD → Allow: GET, HEAD + 405（text/plain）
        ├── X-Content-Type-Options: nosniff
        ├── name = strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
        ├── isAPIPath(name)                    → 404 text/plain「接口不存在」
        ├── name == "" || "."                   → serveIndex
        ├── fs.Stat(files, name)
        │   ├── 其他错误                         → 500 + 日志
        │   ├── info.IsDir()                    → 404「资源不存在」（禁止目录列表）
        │   ├── 存在 → fs.ReadFile → serveFile  → Cache-Control + Content-Type + Content-Length
        │   └── 不存在 → isAssetPath(name) ? 404 : serveIndex
        └── writeContent：WriteHeader → HEAD 直接 return → w.Write
```

关键实现点（`internal/webui/handler.go`、`mime.go`、`embed.go`、`diagnostic.go`）：

| 规则 | 实现位置 | 具体行为 |
|---|---|---|
| `/api` 保留 | `Handler.ServeHTTP` + `isAPIPath` | 404 text/plain，绝不回退 index.html |
| 根路径与客户端路由回退 | `serveIndex` | `name == ""` 或缺失且非资源路径 → `index.html` |
| 缺失的 `.js` / `.css` 必须 404 | `isAssetPath` | `path.Ext(name) != "" \|\| strings.HasPrefix(name, "assets/")` |
| 禁止目录列表 | `info.IsDir()` 分支 | 直接 404，与 `http.FileServer` 行为相反 |
| `index.html` 缓存 | `indexCacheControl = "no-cache"` | |
| 哈希产物缓存 | `hashedCacheControl = "public, max-age=31536000, immutable"`，判据是 `assets/` 前缀 | |
| 其他静态资源缓存 | `defaultCacheControl = "public, max-age=3600"` | |
| MIME 确定性 | `webMimeTypes` 显式表优先，`mime.TypeByExtension` 兜底 | 避免 Windows 注册表差异，`.js` 强制 `text/javascript` |
| `nosniff` | `ServeHTTP` 统一设置 | 含 404 / 500 响应 |
| HEAD 不返回响应体 | `writeContent` 显式判断 | 但照常写出 `Content-Length` |
| 无 ETag / Last-Modified / Range | `writeContent` 注释明确「不提供」 | 嵌入资源 modtime 不可靠，Range 对构建产物无意义 |
| 路径清理 | `path.Clean("/"+r.URL.Path)` | `/assets//app.js`、`/a/../assets/app.js` 会被规范化后照常服务（不重定向） |
| 产物缺失诊断页 | `DiagnosticHandler` | 503 + `no-store` + HTML 说明 + 转义后的失败原因 |
| 构建强校验 | `httpserver.buildWebHandler` + `RequireWebUI` + `make web-verify` | 发布构建缺 `index.html` 直接失败 |

`internal/webui/embed.go` 只做 `//go:embed all:dist` + `fs.Sub` + `HasIndex`，没有重复实现标准库能力。

## 5. SSE 与 Request ID

### 5.1 SSE（当前实现，`internal/httpapi/event_stream.go`）

| 关注点 | 现状 | 结论 |
|---|---|---|
| 路径 | `GET /api/v1/tasks/{taskID}/events/stream` | 由 `parseTaskEventStreamPath` 判定（3 段） |
| `http.Flusher` | 显式 `w.(http.Flusher)` 断言，失败返回 500「当前连接不支持流式响应」 | 依赖原始 `ResponseWriter` |
| 响应头 | `text/event-stream`、`no-cache, no-transform`、`Connection: keep-alive`、`X-Accel-Buffering: no` | 已在写头前全部确定 |
| 写头时机 | 方法、游标、任务归属、起点全部校验通过后才 `WriteHeader(200)` + `Flush` | 流开始后错误只记录日志 |
| 游标 | 优先 `Last-Event-ID` 头，其次 `last_event_id` 查询参数；缺省取 `LatestTaskEventID` | |
| 心跳 | 15s 一次 `: ping\n\n` 注释帧 + `Flush` | |
| 轮询 | 1s 周期 + 建立连接后立即补发，按批连续读，批未满即停 | |
| 慢客户端 | 无写超时；写失败即结束连接并保留原游标 | 不阻塞其它任务，由独立 goroutine 各自推 |
| 取消 | `<-ctx.Done()` → 记录 `client_closed` | |
| 服务关闭 | `<-h.shutdown`（来自 `http.Server.RegisterOnShutdown`）→ 记录 `server_shutdown` | 没有该信号时 Shutdown 会等到连接级超时 |
| `ResponseWriter` 包装 | `withRequestID` 不包装 `ResponseWriter`，只改 `Request` | 当前不存在破坏 `Flusher` 的中间件 |

### 5.2 Request ID（`internal/httpapi/requestid.go`）

| 关注点 | 现状 |
|---|---|
| 生成 | `crypto/rand` 16 字节 → 32 字符十六进制；随机源失败返回空字符串，不让请求失败 |
| 信任外部 | 不读取请求头，每次请求重新生成 |
| 注入 Context | `context.WithValue(r.Context(), requestIDKey{}, id)` |
| 响应头 | `X-Request-Id` |
| 错误响应 | `writeError` → `errorResponse(code, message, requestIDFrom(...))` → `error.request_id` |
| 覆盖范围 | 只包住 API 处理器；**Web 页面响应没有 `X-Request-Id`** |

## 6. 候选组件上游核查

### 6.1 Go 1.24.5 标准库 `http.ServeMux`

来源：本机 `go1.24.5` 的 `D:\librarys\go\src\net\http\server.go`（`findHandler`、`matchOrRedirect`、
`matchingMethods`、`registerErr`）与 `pattern.go`（`comparePaths`、`conflictsWith`、`compareMethods`）。

| 能力 | 结论 |
|---|---|
| 方法模式（`GET /path`） | 支持。**`GET` 模式同时匹配 `GET` 与 `HEAD`**（`server.go` 文档原文） |
| `{name}` 单段通配 | 支持，通配符必须是完整路径段 |
| `{name...}` 剩余通配 | 支持，只能出现在末尾 |
| 尾部斜杠作为匿名 `{...}` | 支持（`A trailing slash in a path acts as an anonymous "..." wildcard`） |
| `{$}` 精确匹配结尾 | 支持 |
| `Request.PathValue` | 支持，值来自按段解码后的匹配结果 |
| 优先级 | 更具体的模式胜出；互不包含则**注册时 panic** |
| 冲突检测 | 注册时 panic，错误信息含双方注册位置 |
| 未知路径 | `404 page not found`，`text/plain`，非 JSON |
| 方法不支持 | `405 Method Not Allowed`，`Allow` 为**排序后的逗号列表**（例如只注册 `GET` 时是 `GET`，不是 `GET, HEAD`），正文 `text/plain` |
| 路径清理 | `cleanPath` 后若与请求路径不同 → **301 重定向**（`//`、`/./`、`/../`） |
| 尾部斜杠重定向 | 仅在「路径不以 `/` 结尾且加 `/` 后能精确匹配」时 301 |
| 百分号解码 | **模式与请求路径都按段解码后再匹配**；`/a%2Fb` 被视为 1 段 `a/b`，`PathValue` 返回 `a/b` |
| 组合方式 | `http.Handler`，可与任意中间件、任意子 Handler 组合 |

两个直接后果：

1. `ServeMux` 自带的 404 / 405 **不符合** AgentFlow 的 JSON 错误协议，也不能生成现有中文文案，
   因此不能直接依赖。
2. 今天 `httpapi.NewHandler` 已经在用 `ServeMux`，所以 **「重复斜杠 301」与「`.`/`..` 清理」已经是
   现状行为**，不是换成 `ServeMux` 才出现的新行为。真正新增的差异只有百分号解码语义（见 8.8）。

### 6.2 `github.com/go-chi/chi/v5`

来源：模块代理取得的 `github.com/go-chi/chi/v5@v5.5.0` 完整源码（`mux.go`、`tree.go`、`chi.go`、
`context.go`、`chain.go`、`middleware/`）。

| 项目 | 结论 |
|---|---|
| 最新稳定版 | `v5.5.0`（模块代理 `@latest`；本次核查时最高版本） |
| 发布时间 | `v5.5.0` = 2026-10-14T13:20:04Z；`v5.4.0` = 2026-10-12T11:22:20Z；`v5.2.3` = 2025-08-26 |
| Go 最低版本 | `go 1.24`（`go.mod` 指令；注释写明「支持最近四个 Go 大版本」） |
| 许可证 | MIT（`LICENSE`：「Copyright (c) 2015-present Peter Kieltyka … Permission is hereby granted…」） |
| 依赖闭包 | `go.mod` 中**没有任何 `require`**，只依赖标准库 |
| 维护活跃度 | 2026-10 连续两个版本发布，仓库仍活跃；但 `CHANGELOG.md` 只更新到 `v5.0.12`（2024-02-16），5.1+ 没有变更记录 |
| 公开 API | `NewRouter`/`NewMux`、`Router`/`Routes` 接口、`Handle`/`HandleFunc`/`Method`/`Get`/`Post`/`Put`/`Patch`/`Delete`/`Head`/`Options`、`With`/`Group`/`Route`/`Mount`、`NotFound`/`MethodNotAllowed`、`Use`、`URLParam`/`RouteContext`、`middleware.*` |
| 路由参数 | `chi.URLParam` / `RouteContext().URLParam`；v5.5.0 同时 `r.SetPathValue`，可复用标准库读取方式 |
| 方法匹配 | 每种方法独立注册；**`GET` 不自动匹配 `HEAD`**，需要显式 `Head` 或 `middleware.GetHead` |
| 路由分组 | `Route` / `Group` / `Mount` 子路由 |
| 中间件 | `Use`（全局栈）+ `With`（内联）+ `middleware` 包 |
| 404 / 405 | 可用 `NotFound` / `MethodNotAllowed` 覆盖；默认 405 通过 `Allow` 头逐条 `Add`（多值头）+ 空响应体 |
| 路径编码语义 | 优先用 `r.URL.RawPath` 匹配（`mux.go`），**路径参数不做百分号解码**，与标准库按段解码不同 |
| SSE | 与标准库一致（`http.Handler`），不影响 `Flusher`；但中间件若包装 `ResponseWriter` 需自行规避 |
| Context | 用 `context.WithValue(RouteCtxKey, *Context)` + `sync.Pool`，每请求 2 次分配（源码注释自述） |
| 与 `http.Handler` 组合 | 完全兼容（`Mux` 实现 `http.Handler`） |
| 默认 405 的 `Allow` 形态 | `w.Header().Add("Allow", …)` 多条头，不同于项目单一逗号字符串 |
| `middleware.RequestID` | **优先使用调用方传入的 `X-Request-Id`**，为空才生成 `<hostname>/<base62>-<序号>` |

## 7. A / B / C / D 方案对照

| 指标 | A 保留现状 | B 统一 ServeMux | C 引入 Chi | D 标准文件服务 |
|---|---|---|---|---|
| 自研代码删除量 | 0 | 可删除第 3.2 节的顺序分发表、第 3.3 节列出的全部路径解析与形状判定函数、7 个 Handler 内部手写分派、以及一批重复的方法校验 | 同 B（Chi 也需要删掉同样这批代码），额外引入 Router 概念 | `writeContent` 的 40 行可被 `http.ServeContent` 取代；`Handler` 分发骨架无法删除 |
| 依赖成本 | 0 | 0（已在用） | +1 直接依赖（零间接依赖），需自行跟进 5.x 版本与语义演进 | 0 |
| 功能完整性 | 完整 | 完整（配 404/405 JSON 适配器） | 完整（需自行处理 HEAD 与默认 405 头形态） | 完整；但 `http.FileServer` 无法表达 SPA 回退/禁止目录/`/api` 保留 |
| 行为变化 | 无 | 百分号解码语义变化（8.8）、未知形状 503→404（8.9）、新增路径清理 301 覆盖面 | 同 B，另加参数不解码 → 与 B 不同 | 新增 Range / `Accept-Ranges` / 条件请求 |
| 可维护性 | 新增接口需要同时改 3 处（注册表、分派顺序、parse 函数），顺序即为隐式契约 | 新增接口 = 路由表加一行；顺序依赖消失 | 同 B，但多一层 Chi 概念（Context/Group/中间件栈） | 分发骨架仍需手写 |
| 性能 | 每请求最多 ~20 次 `TrimPrefix` + 若干 `Split` 分配 | 前缀树匹配，无逐段分配；参数直接切片 | 与 B 同量级（`sync.Pool` + 2 次 context 分配） | 无差异 |
| Go 版本 | 符合 | 符合（1.22+ 特性，1.24.5 可用） | 符合（要求 ≥1.24） | 符合（`FileServerFS` 自 1.22） |
| 供应链 | 无 | 无 | MIT、零依赖、活跃；但 `CHANGELOG` 停滞、5.x 曾在 2025-08 → 2026-10 之间无发布 | 无 |

性能结论：三条路线在本项目的量级（本地单用户工具、SSE 每秒一次轮询）上都没有可测量的实际差异，
性能不构成任何选择依据。

## 8. 行为差异与风险清单

以下每一条都是「换成 ServeMux + 路由表」时需要显式确认的点。

### 8.1 未知路径 404
- 现状：`router.notFoundHandler` → JSON `not_found` + `接口不存在: <path>`。
- B 方案：注册 `/api` 与 `/api/` 两个兜底模式，指向同一个 `notFoundHandler`；`/api/v1/nope` 命中的是
  `/api/` 子树，因此仍是 JSON 404。**不能**让 `ServeMux` 默认 404 生效（它是 `text/plain`）。
- 风险：低。必须同时注册 `/api` 与 `/api/`（两者在 `pattern.go` 中被判定为 disjoint，可以共存）。

### 8.2 已知路径但方法不支持 → 405
- 现状：JSON `method_not_allowed` + 现有 `Allow` 字符串 + 现有中文文案。
- B 方案：路由表每条记录带 `pattern`、`allowHeader`、`methods`、`message`，注册 `methods` 各一条方法模式，
  再用**同 pattern 的无方法模式**做兜底写 405。Go 的方法模式比无方法模式更具体，二者可共存不冲突。
- **禁止**直接依赖 `ServeMux` 的自动 405：它的 `Allow` 是排序逗号列表（只注册 `GET` 时给 `GET`，缺 `HEAD`），
  正文是纯文本，测试 `TestUnsupportedMethodReturnsJSONError` 会失败。

### 8.3 错误消息与 `Allow` 字符串
- 必须逐条抄录：`Allow` 有 9 种取值，消息文案至少 4 种风格（含环境接口的逗号形式）。
- 用一个通用格式化函数推导会改变文案，违反「不修改错误消息」。

### 8.4 GET / HEAD
- 现状：Handler 内部把 `HEAD` 与 `GET` 等同处理（`allowGet`、`allowMethod`、`allowMethods` 都接受 HEAD）。
- B 方案：只注册 `GET`，`ServeMux` 自动把 `HEAD` 交给同一处理器；`net/http` 对 `HEAD` 丢弃响应体。
  行为等价，并可删除所有显式 HEAD 分支。
- 风险：极低。`Allow` 头仍写 `GET, HEAD`，与现状一致。

### 8.5 尾部斜杠
- 现状：`/api/v1/tasks/` → `taskHandler.serve` 的 `remainder == ""` 分支 → JSON 404。
- B 方案：`/api/v1/tasks/` 不匹配 `/api/v1/tasks` 也不匹配 `/api/v1/tasks/{taskID}`（通配段不匹配空串），
  落到 `/api/` 兜底 → JSON 404 `接口不存在: /api/v1/tasks/`。状态码与错误码一致，文案一致。
- 风险：低。`TestTrailingSlashIsNotFound` 保护。

### 8.6 重复斜杠
- 现状：API 侧已经是 `ServeMux`，`/api/v1//tasks` 已返回 301。
- B 方案：不变。
- 唯一新增：`//api/v1/tasks` 今天落到 Web 侧（`IsAPIPath("//api/...") == false`）→ text/plain 404；
  若把整套 Handler 变成单一 `ServeMux`，它会先被 `cleanPath` 成 `/api/v1/tasks` 再 301。
  这是行为变化，需在实施时明确接受或保留外层 `routes` 分叉不变（推荐保留外层分叉，见 14.2）。

### 8.7 路由注册冲突
- B 方案：冲突会在**启动阶段 panic**（`ServeMux.Handle` 的设计）。
- 影响：把今天「顺序错了就静默走错分支」的隐式契约，换成「启动即失败」。这是收益，但需要在实施说明里
  写明：路由表冲突属于编程错误，panic 可接受；不得用 `recover` 吞掉。
- 已核查：第 3.2 节列出的每个形状都能用 `ServeMux` 模式清晰表达且**互不冲突**（段数不同，或末段是字面量，或被方法模式细分，都不构成冲突）。

### 8.8 `%2F` 编码路径段（唯一真正需要注意的语义差异）
- 现状：用 `r.URL.Path`（已解码）切分，`/api/v1/tasks/a%2Fb` 变成三段 `tasks/a/b` →
  `validTaskPathID` 判定失败 → 404。
- B 方案：按段解码后匹配，`a%2Fb` 是**一段**，`{taskID}` 命中，`PathValue("taskID") == "a/b"`。
- 后果：若不额外校验，`"a/b"` 会进入应用层（任务不存在 → 404，或参数校验 → 400），语义与现在不同。
- 处理：保留一个合并后的 `validPathID(value)`（非空且不含 `/`）在取 `PathValue` 后校验，必要时回 404。
  这样既保留现状语义，又能删掉三份重复实现。

### 8.9 形状不命中时的状态码归属
- 现状：`rt.tasks == nil` 时，`/api/v1/tasks/{id}/unknown` 返回 503「任务服务未初始化」而不是 404
  （任务服务可用性判定先于 `tasks.serve`）。
- B 方案：每条路由自带依赖检查，未知形状直接落到 `/api/` 兜底 → 404。
- 结论：这是修正，但属于可观察变化，需要在实施说明中列出。

### 8.10 SSE
- 现状：`withRequestID` 不包装 `ResponseWriter`，`Flusher` 可用。
- B / C 方案都不包装 `ResponseWriter`，SSE 不受影响。
- 按第 0 节的指令，即使将来引入会包装 `ResponseWriter` 的中间件，也不构成阻塞；但那会**立即破坏当前 SSE**，
  必须与 SSE → WebSocket 迁移一起做，不能单独上线。
- 若迁移到 WebSocket：`http.Server.Shutdown` **不会**关闭已 hijack 的连接，现有
  `RegisterOnShutdown` + `streamShutdown` 通道必须保留（并升级为「主动关闭 socket」），这条不能随 Router
  替换一起丢弃。

### 8.11 统一错误协议
- 所有方案都必须继续通过 `writeError` / `writeJSON` 输出，`Cache-Control: no-store` 与
  `Content-Type: application/json; charset=utf-8` 不变。
- 风险点：新增的路由表兜底处理器必须复用 `writeError`，不能自己 `http.Error`。

### 8.12 服务依赖检查语义
- 现状：顺序即优先级（3.4 第 2、3 点）。
- B 方案：每条路由处理器的第一段逻辑就是自己的依赖检查，表内顺序依赖消失，语义更容易保证；但必须逐条核对
  3.2 表中标注了「先于…判定」的项目，确保迁移后仍然得到同样的错误归属。
- 额外陷阱：405 与 503 的先后顺序本来就是两类（3.4 第 3 点）。若统一按「先查服务再查方法」处理，
  `environments` 与 POST-only 动作类的 405 会在服务缺失时变成 503；反过来统一按「先查方法」处理，
  资源类接口的 503 会变成 405。两种方向都是可观察的行为变化。

## 9. 可删除代码清单（仅在接受 B 方案时）

**1. 分发表**

- `internal/httpapi/router.go`：`(*router).tasksHandler` 的 20 个顺序判定整体删除；`isWorkflowPath`、
  `isExecutionRecordPath`、`(*router).environmentsHandler`、`(*router).hostsHandler`、
  `(*router).rolesHandler`、`(*router).projectsHandler` 这些纯转发包装一并删除（改为路由表直连各处理器）。

**2. 路径解析与形状判定（第 3.3 节全表）**

`splitPathSegments`、`actionTaskID`、`matchActionPath`、`statusTaskID`、`matchStatusPath`、
`parseTaskEventsPath`、`parseRepairCyclesPath`、`parseTaskEventStreamPath`、`parseOpenRepairCyclePath`、
`parseRepairCycleEligibilityPath`、`parseStepExecutePath`、`parseExecuteAndValidatePath`、
`parseExecuteStepsPath`、`parseValidatePath`、`parseInitializeCodePath`、`isCodeRecoveryPath`、
`matchSubtasksPath`、`nestedPathID`、`isCodeSaveListPath`、`isTaskContainerPath`、
`parseTaskContainerListPath`、`parseContainerDisposalPath`、`isTaskLaunchPath`、`isTaskRetryPath`、
`roleIDFromPath`、`parseEnvironmentPath` 及 `environmentPathKind` 常量组与 `environmentPath` 结构体、
`validTaskPathID` / `validProjectPathID` / `validHostPathID`（合并为 1 个 `validPathID`）。

**必须保留的同族函数**：`parseEnvironmentVersionNumber`、`environmentRefFromSegments`
（它们做的是「值校验 → 400」，不是形状判定）。

**3. Handler 内部手写分派与方法校验**

- `taskHandler.serve`、`hostHandler.serve`、`projectHandler.serve`、`roleHandler.serve`、
  `environmentHandler.serve` 的路径分支部分（保留 `serveList`/`serveDefinition`/`serveVersion`/
  `serveInit`/`serveCreateVersion`/`serveBuild`/`serveDockerfile`/`serveBuildLog`/`serveBuildStatus`
  等真正的业务入口）。
- `executionHandler.serve` 的 6 分支 `switch`（改为 6 条路由直连 `listSteps` / `listAttempts` /
  `listSessions` / `getSession` / `listValidations` / `getValidationFeedback`）。
- `containerHandler` 的二次形状尝试（改为两条路由）。
- `(*router).allowGet`、`(*router).tasks.allowMethod`、`(*router).executionHandler.allowGet`、
  `(*hosts).allowGet` 一类重复实现、`(*environmentHandler).allowMethods`、以及各 Handler 里
  `if r.Method != http.MethodPost` / `!= http.MethodGet` 的方法分支。
- `writeErrorWithOperationResult` 之外的 405 分支里手写的 `Allow` 与消息（移入路由表）。

**4. Web 侧**

- `webui.Handler.ServeHTTP` 的方法校验（由 `GET /` 模式 + `/` 兜底承担）——可选，属防御性代码，
  也可保留。

**不删除、不建议替换的部分**

- `internal/httpserver/server.go` 全部（监听、`ReadHeaderTimeout`、`Shutdown`、`RegisterOnShutdown`）。
- `internal/server/server.go` 的生命周期编排。
- `internal/webui` 的分发骨架、缓存策略、MIME 表、诊断页、`embed.go`。
- `internal/httpapi/response.go`、`requestid.go`、`path.go`（`IsAPIPath` 仍需给 `routes` 用）。
- `internal/httpapi/event_stream.go`（除非同期做 WebSocket 迁移）。

## 10. 必须保留的业务策略（任何方案都不得改变）

1. `/api` 与 `/api/` 属于 API 保留路径；`/api/v1/...` 未注册必须 JSON 404；`/apiary`、`/apix` 不是 API 路径；
   未知 API 路径不得落到 SPA 回退。
2. 逐路由的状态码、错误码、`Allow` 头与中文文案（见 8.1–8.3）。
3. 每类资源缺失时返回真实 503，不伪造空列表（角色、项目、任务、执行节点、环境、执行记录、历史、
   代码保存、租约、容器……）。
4. 依赖优先级：只读子资源（`code-saves`、`events`、`repair-cycles`）与独立动作（`start`、`retry`）
   不得因为任务管理服务缺失而报「任务服务未初始化」。
5. 静态资源 6 条产品规则（`/api` 保留、根与客户端路由回退、缺失带扩展名资源必须 404、禁止目录列表、
   分级缓存、`nosniff`），以及「缺产物时 503 诊断页而不是空白页」。
6. 监听地址、`ReadHeaderTimeout = 10s`、`DefaultShutdownTimeout = 10s`、Shutdown 失败后 `Close()`、
   `RegisterOnShutdown` 关闭信号、`internal/server` 的逆序清理与 `waitServe` 超时。
7. 统一 JSON 错误协议与 `request_id` 关联；Request ID 不接受外部输入。
8. 发布构建的前端产物强校验（`RequireWebUI` + `make web-verify`）。

## 11. 现有测试审计（只读，未运行）

范围：`tests/unit/httpapi/`（20 个测试文件，204 个 `Test*` 函数）、`tests/integration/local/`、
`tests/e2e/cli/`。`tests/` 中**没有**任何引用 `internal/webui`、`internal/httpserver`、
`internal/httpapi/event_stream.go` 文件名的测试，也就是说静态资源服务与 HTTP Server 生命周期没有自动化
覆盖。

| 行为 | 是否有测试覆盖 | 证据 |
|---|---|---|
| 未知 API 路径 JSON 404 | 有 | `TestUnknownAPIPathReturnsJSONNotFound`、`TestAPIRootReturnsJSONError`、`TestTrailingSlashIsNotFound` |
| `/api` 边界（`/apiary`、`/apix`） | 有 | `TestAPIPathBoundary` |
| 405 + `Allow` + 错误码 | 有（十余条路由） | 见下方 A 组 |
| 形状不互相吞掉（顺序即契约） | 有 | 见下方 B 组 |
| `X-Request-Id` 与错误体 `request_id` | 部分 | `TestHealthSetsJSONContentType`（响应头非空）、`TestUnknownAPIPathReturnsJSONNotFound`（`error.request_id` 非空） |
| 405 文案与 `Allow` 的**字节级**一致性 | 部分 | 多处只断言 `Allow`，未断言 message 文本 |
| SSE：方法/游标/未知任务/推送/心跳/分批/写失败/取消/慢客户端 | 有（9 个） | `TestTaskEventStream*` |
| SSE：不支持 `Flusher` 时的 500 | **无** | 需人工阅读 |
| 静态资源：缓存头、MIME、目录、SPA 回退、HEAD、`/api` 保留 | **无** | 需人工阅读 `internal/webui` + `docs/frontend.md` |
| Server 生命周期：`ReadHeaderTimeout`、Shutdown、`RegisterOnShutdown` 信号 | **无** | 需人工阅读 `internal/httpserver`、`internal/server` |
| 请求 ID 不信任外部传入 | **无** | 需人工阅读 `requestid.go` |


A 组（405 与 `Allow`）：`TestUnsupportedMethodReturnsJSONError`、`TestProjectAPIRejectsUnsupportedMethods`、
`TestTaskPathsRejectUnsupportedMethods`、`TestEnvironmentRejectsUnsupportedMethods`、
`TestStartAPIMethodNotAllowed`、`TestRetryAPIRejectsWrongMethod`、`TestValidateAttemptAPIMethodNotAllowed`、
`TestExecuteStepAPIMethodNotAllowed`、`TestExecuteAndValidateAPIMethodNotAllowed`、
`TestRepairCycleEligibilityAPIMethodNotAllowed`、`TestTaskContainerDisposalMethodNotAllowed`、
`TestPlanAPIMethodNotAllowed`。

B 组（形状隔离）：`TestPlanPathNotSwallowedByTaskHandler`、`TestUnknownTaskSubPathReturnsJSONNotFound`、
`TestUnknownHostSubPathReturnsNotFound`、`TestEnvironmentUnknownSubPathsAreNotFound`、
`TestProjectAPIPathShapeRejectsNestedSegments`、`TestExecuteStepAPINotFound`、
`TestRepairCycleEligibilityAPINotFound`、`TestExecuteStepsAPINotFound`、
`TestExecuteAndValidateAPINotFound`、`TestValidateAttemptAPIUnknownPathIsNotFound`、
`TestInitializeCodeUnknownPathIsNotFound`、`TestWorkflowAPIRejectsUnknownPath`。
结论：路由语义的自动化覆盖已经相当充分（204 个用例里有 30 余个专门针对路径形状与 405），因此 B 方案
迁移的风险主要落在「文案与 `Allow` 字节一致性」「`%2F` 语义」「503→404」这三处未被测试覆盖的缝隙上；
静态资源与 Server 生命周期则完全没有回归网，任何改动都必须靠人工源码核对。

## 12. 最终方案比较（逐项结论）

### 12.1 HTTP Server 是否应该继续使用 Go 标准库
**是。** 现状已经是标准库主导（2.2 节），自研部分只有生命周期编排与关闭信号，不可替换也不该替换。
`ReadHeaderTimeout`、`Shutdown`/`Close` 的用法正确，`RegisterOnShutdown` 是标准库为此提供的唯一钩子。
唯一可优化项是 SSE 关闭信号的必要性：若将来迁移到 WebSocket，该信号仍需保留（8.10）。

### 12.2 API 路由是否应该统一使用标准库 ServeMux
**应该（方案 B）。** Go 1.24.5 的 `ServeMux` 已具备本项目需要的全部匹配能力，且项目已经在用它做一级
分派。收益是删掉 20 余个形状解析函数与一张顺序敏感的 20 项分派表，新增接口退化为「路由表加一行」。
**前提条件是必须自带 404 / 405 JSON 适配器**，否则会破坏统一错误协议与现有 30 余个测试。

### 12.3 是否有必要引入 Chi
**没有必要。** Chi v5.5.0 的零依赖与 MIT 许可都很干净，但它相对标准库多出的能力（Group / Route /
Mount / 中间件栈 / RouteContext）本项目一个都不使用；它相同的地方（404/405 需要自己接管、参数需要自己
校验）与标准库完全等价；它不同的地方（`GET` 不匹配 `HEAD`、参数不解码、`Allow` 多值头）反而要求额外的
适配与测试改写。为同一个能力引入外部依赖，违反「新增依赖必须实际减少维护成本」。

### 12.4 WebUI 文件服务是否需要保留项目自定义 Handler
**需要保留（方案 A）。** `http.FileServer` / `http.FileServerFS` 无法表达：`/api` 拒绝、禁止目录列表、
「缺失带扩展名资源必须 404」、SPA 回退、分级缓存、按 `assets/` 前缀判定长缓存。要表达这些就得包一层
自己的分发逻辑，等于把今天的 `ServeHTTP` 换个写法。**唯一值得吸收的标准库能力是
`http.ServeContent`**：用它替换 `writeContent`，可以免费获得 Range、`Accept-Ranges`、`If-None-Match` /
`If-Modified-Since`、HEAD 处理与一致的 `Content-Length`。

需要注意的事实（已核对 `net/http/fs.go`、`embed/embed.go`）：
- `embed.FS` 的 `ModTime()` 恒为零值，`ServeContent` 在 `modtime.IsZero()` 时不输出 `Last-Modified`、
  也不处理 `If-Modified-Since`，因此**缓存策略不会被动改变**。
- `ServeContent` 从不自行计算 `ETag`，只认调用方预置的 `Etag` 头，因此**不会凭空出现 ETag**。
- Range 与 `Accept-Ranges: bytes` 是新增能力，与今天的注释「Range 请求对构建产物没有意义，这里不实现」
  冲突，是一项需要显式接受的行为变化（不影响安全边界，但会让 `/assets/*.js` 支持断点续传）。

结论：这一项**收益很小**（约省掉 40 行手写写响应逻辑），如果不需要 Range，保留现状更划算；建议列为
「可选、低优先级」，不作为本轮改造内容。

### 12.5 哪些中间件值得保留自研
- **Request ID：保留自研。** 只有 42 行，且「不信任外部传入 + 128 位加密随机」正是 Chi
  `middleware.RequestID` 明确不满足的（它优先取调用方头）。换成 Chi 会打开请求标识伪造口子。
- **Recoverer：可选新增。** 目前 `net/http` 自带 recover（连接级），进程不会因 handler panic 崩溃，
  但不会返回统一 JSON。若将来要统一 500，自研 20 行即可，不需要 Chi。
- **日志 / 压缩 / CORS / RealIP：不需要。** 本地单用户工具，无反向代理、无跨域场景。

### 12.6 如果推荐改造，具体建议删除什么
见第 9 节的可删除清单。核心是：`router.go` 的顺序分发表 + 第 3.3 节列出的全部路径解析函数 + 7 个 Handler 内部手写
分派 + 一批重复的方法校验，替换为 `internal/httpapi/routes.go` 里的路由表与两个适配处理器
（JSON 404、JSON 405）。

## 13. 最终结论

**部分值得替换。**

| 审计方向 | 结论 |
|---|---|
| HTTP Server 生命周期 | 不值得替换：已是标准库 + 必要的业务信号，保留 |
| API 路由与路径匹配 | **值得替换**：改为标准库 `ServeMux` 方法模式 + 声明式路由表 |
| 第三方 Router（Chi） | 不值得引入 |
| 静态资源服务 | 保留自定义 Handler（`http.FileServer` 无法表达产品规则）；`ServeContent` 只是可选项 |
| Request ID 中间件 | 保留自研 |

判断依据不是代码行数，而是三件事：路由顺序依赖是当前唯一「改一处必须同时改三处、顺序错了静默走错分支」
的结构性维护成本；这块能力标准库已经完全具备且无新增依赖；其余三块要么必须保留业务规则，要么本来就
不是自研（Server），要么替换会破坏安全边界（Request ID）。

## 14. 如果实施，最小范围

仅作为后续立项的输入，本轮未实施。

### 14.1 新增

`internal/httpapi/routes.go`：一张路由表，每条记录形如

```go
type route struct {
    pattern string   // "/api/v1/tasks/{taskID}/steps/{stepID}/execute"
    methods []string // 需要注册的方法；GET 已隐含 HEAD，不要重复注册 HEAD
    allow   string   // 现有 Allow 头原文，如 "GET, HEAD, DELETE"
    message string   // 现有 405 文案原文
    // serviceFirst 表示先判服务可用性再判方法：资源类接口与 start/retry 为 true，
    // environments 与 POST-only 动作为 false，以保持 503 与 405 的现有先后顺序（3.4 第 3 点）。
    serviceFirst bool
    handle  func(http.ResponseWriter, *http.Request)
}
```

注册逻辑：对每条记录逐个注册 `method + " " + pattern`，再用**同 pattern 的无方法模式**注册
`methodNotAllowed(route)`（写统一 JSON 405）。另外注册 `/api`、`/api/` 两个兜底 → `notFoundHandler`。
外层 `internal/httpserver/routes.go` 的 `IsAPIPath` 分叉**保持不变**，这样 `//api/...` 的现有归属不变。

### 14.2 实施顺序（每步都可独立验证）

1. 路由表 + 404/405 适配器，先迁移基础接口（`health`、`system/info`、`settings/config`）与
   `roles` / `projects` / `hosts`，删除对应的 `serve` 分派与 `roleIDFromPath`、`valid*PathID` 重复实现。
2. 迁移 `environments`（保留 `environmentRefFromSegments` 与 `parseEnvironmentVersionNumber` 的 400 语义）。
3. 迁移 `tasks` 子树（分派表整体替换），同时把 `PathValue` 统一过 `validPathID`（8.8）。
4. 删除第 9 节剩余函数，确认 `splitPathSegments` 已无引用。

### 14.3 验证方式（不运行测试的前提下）

- 逐条对照第 8 节的 12 项差异，检查路由表里的 `allow` / `message` 与改动前的字面量是否逐字一致。
- `go vet ./...`、`gofmt`。
- 人工阅读 `tests/unit/httpapi` 中与路由形状、405 相关的 30 余个用例，确认新表覆盖同样的路径集合。
- 若用户明确授权，再运行 `go test ./tests/unit/httpapi/...`。

### 14.4 明确不做

- 不改 `internal/httpserver`、`internal/server`、`internal/webui`。
- 不改任何路径、方法、状态码、错误码、错误文案、缓存策略、SPA 回退规则。
- 不引入任何新依赖。
- 不在本轮做 SSE → WebSocket。

## 15. 后续重新评估的触发条件

1. 新增 API 资源/子资源时，如果发现需要再次修改 `tasksHandler` 式的顺序判定（说明现状的结构性成本
   已经开始实际付费），立即启动 B 方案。
2. 迁移 SSE → WebSocket 时：需要重新评估中间件与 `ResponseWriter` 包装策略，并确认 `Shutdown` 对
   hijack 连接的关闭语义（8.10）；同时评估是否引入 WebSocket 库（标准库不提供实现）。
3. 出现「静态资源需要 Range / 断点续传 / 强缓存校验」的真实需求时，再评估 `http.ServeContent`（12.4）。
4. 出现反向代理、多实例或跨域部署时，才需要重新考虑中间件（压缩、CORS、RealIP）与 Router 选型。
5. Chi 若在未来版本提供「默认 JSON 404/405 + 与标准库一致的按段解码语义 + `GET` 自动匹配 `HEAD`」，
   重新评估 C 方案；当前 v5.5.0 不满足其中任意一项。
6. Go 标准库若新增可定制的 `ServeMux` NotFound / MethodNotAllowed 钩子，B 方案的适配器可以进一步收缩。
