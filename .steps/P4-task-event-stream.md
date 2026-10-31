# P4 常规开发 —— 任务事件实时推送（SSE）与 Web 实时展示

> 基线：`origin/master` = `1550a55d70dac81ce64c012c0e21c21a01fc66fd`（上一提交
> `b09adaf5372edb9a419d877e0a1d286fff1fa86a`）。开工前执行过 `git fetch origin`，
> 远端未前进，当前分支 `master`、`HEAD` 与 `origin/master` 一致，工作区干净（无未提交改动）。
>
> 本轮不是 P4-M5 的性能优化，也没有把它标记为完成：`P4-M5` 仍为 `pending`。

---

## 1. 实施前的代码证据

结论先行：**既有事件已经是完整、可安全对外、按自增标识单调追加的持久化事实；缺的是
"把它们在写入后送达浏览器"的只读通道**。因此本轮只补通道，不改事件语义、不改状态机、
不新增事件类型。

### 1.1 服务端既有能力（逐项核实）

| 事实 | 代码位置 | 核实结果 |
|---|---|---|
| 事件只追加、不修改删除 | `internal/storage/sqlite/events.go`（`AppendEvent`） | 成立：只有 INSERT 与 SELECT |
| 事件标识是自增主键 | `migrations/0001_initial.sql`：`id INTEGER PRIMARY KEY AUTOINCREMENT` | 成立：写入顺序即 `id` 顺序，任务内单调递增 |
| 已有按任务的索引 | 同迁移：`CREATE INDEX idx_events_task ON events (task_id, id)` | 成立：`(task_id, id)` 同时覆盖过滤与 `id` 范围扫描 |
| 历史查询排序 | `sqlite.ListEventsByTask`：`ORDER BY created_at, id` | 成立 |
| 历史查询分页接口 | `GET /api/v1/tasks/{taskID}/events`（`httpapi/events.go`、`app.ListTaskEvents`） | 成立：`size` / `offset` / `type`，`total` 为真实总数 |
| 对外安全摘要 | `app.describeEvent` / `eventPayloadSummary` / `eventResponse` | 成立：payload 白名单 + `ClipFeedbackText` + 脱敏，原始 `payload` 从不出现在响应里 |
| 归属校验 | `app.requireHistoryTask`（不存在 / 已逻辑删除 → `ErrHistoryNotFound`） | 成立 |
| 事件写入点 | `task_workflow_exec.go`、`task_workflow_run.go`、`step_executor_run.go`、`step_workflow_run.go` 等 20 余处，统一 `domain.NewEvent(..., now())` | 成立：`created_at` 取自业务时钟，`id` 取自数据库 |

### 1.2 前端既有行为（逐项核实）

| 事实 | 代码位置 | 核实结果 |
|---|---|---|
| 事件历史不参与 5 秒自动刷新 | `useExecutionRecords.useTaskEvents` 的注释与实际加载时机 | 成立：只在打开任务、手动刷新、「加载更多」与 `stepsToken` 变化时读取 |
| 自动刷新只更新任务与工作流快照 | `useTaskLiveSnapshot.loadRound` 只调用 `fetchTask` + `fetchTaskWorkflow` | 成立 |
| 历史从最早的记录开始分页 | `useTaskEvents` 首页 `offset: 0, size: 20`，服务端 `ORDER BY created_at, id` | 成立：首页是最早的 20 条，`hasMore` 由 `total` 决定 |
| 事件时间线独立于步骤选择 | `TaskExecutionPanel` 中 `useTaskEvents(task.id, stepsToken)` | 成立 |

### 1.3 由此确定的边界

- 事件历史按 `offset` 从最早处开始分页，**已加载的历史与"最新事件"之间本来就可能存在
  未加载的中间段**。因此实时事件不能混进历史列表，否则用户会把"最新几条"误认为连续的
  完整时间线。
- 事件 `created_at` 来自业务时钟，`id` 来自数据库自增序列。两者在正常运行时同序，但
  排序契约是 `(created_at, id)`，而游标必须是单调域。轮子上的取舍见 §3。
- 不新增事件类型、不改事件写入、不引入消息中间件：一个只读轮询型 SSE + 单列索引查询
  已能满足"阶段变化在 1 秒内可见"，且不需要任何跨进程订阅设施。

---

## 2. 新增接口契约

```text
GET /api/v1/tasks/{taskID}/events/stream
```

- 只接受 `GET`；其它方法 405（`Allow: GET`）。
- 只推送**该任务已经持久化**的事件，字段与历史查询完全一致（`httpapi.EventResponse`）：
  `id`、`task_id`、`step_id` / `attempt_id` / `session_id`、`event_type`、`event_label`、
  `type_known`、`summary`、`created_at`。原始 `payload` 仍然不出现在响应里。
- 响应头：`Content-Type: text/event-stream; charset=utf-8`、
  `Cache-Control: no-cache, no-transform`、`Connection: keep-alive`、
  `X-Accel-Buffering: no`（避免代理缓冲增量帧）。
- 帧格式：`id: <事件标识>` + `data: <事件 JSON>` + 空行。**不使用 `event:` 字段**，
  客户端按默认 `message` 监听，避免为推送另造一套事件类型名。
- 心跳：每 15 秒写一帧 SSE 注释 `: ping`，不携带业务数据。
- 增量读取周期 1 秒；每批最多 100 条（`app.DefaultEventStreamBatchSize`），读到满批时
  连续读完，积压不会按周期被切慢。

### 2.1 错误语义（都在写响应头之前判定）

| 情况 | 结果 |
|---|---|
| 任务不存在 / 已逻辑删除 | 404 `not_found`（复用 `ErrHistoryNotFound`） |
| 游标非法（非整数 / 负数） | 400 `invalid_request` |
| 游标大于该任务当前最新事件标识 | 400 `invalid_request`，不静默等待 |
| 连接不支持 `http.Flusher` | 500 `internal_error` |
| 历史查询服务未装配 | 503 `internal_error` |
| 流开始后的读取/写失败 | 不写 JSON，只结束连接并记录服务端诊断日志 |

### 2.2 历史查询新增字段

`GET .../events` 的响应新增 `latest_event_id`：本次读取时刻该任务最新的已持久化事件标识
（无事件时 0）。它在同一次请求内由 `LatestEventIDByTask`（`COALESCE(MAX(id), 0)`）取得，
用来消除"历史查询 ↔ 流连接"之间的时间窗口。既有字段与语义未变。

---

## 3. 游标与断线恢复

### 3.1 游标取值

游标是事件自增标识 `id`，理由：

- 它是 `INTEGER PRIMARY KEY AUTOINCREMENT`，写入顺序单调递增，任务内严格唯一；
- 增量读取 `WHERE task_id = ? AND id > ? ORDER BY id` 直接命中既有
  `idx_events_task (task_id, id)`，不统计总数、不使用 `OFFSET`，成本不随历史长度增长；
- 断线重连的语义是"已送到哪一条"，天然与写入顺序同域。

**刻意不使用 `(created_at, id)` 作为流内排序**：若一边按 `(created_at, id)` 排序、一边按
`id` 截断，一批结果里的最大 `id` 可能不是游标之后的最大 `id`，推进游标就会静默漏行。
流内按 `id` 升序，展示顺序仍按 `(created_at, id)`（见 §4.3），两者各自与其契约一致。

### 3.2 协议

- 每次连接携带 `last_event_id` 查询参数；服务端同时接受标准 `Last-Event-ID` 请求头
  （请求头优先）。两者都缺省表示"只推送连接建立之后写入的事件"，不重放整份历史。
- 服务端在每帧里发送真实事件 `id`，浏览器自动重连时会回传；本项目由前端自己管理重连
  （见 §4.2），因此以查询参数为准，头部能力保留以便与任何 SSE 客户端互通。
- 游标只在整批事件**全部写出成功**后推进；写失败时保留原游标并结束连接，未送达的事件
  由下一次连接按游标补发。
- 重复不丢失：重连与历史查询竞争可能让同一事件出现两次，客户端按真实事件 `id` 去重。

### 3.3 无丢事件的窗口处理

```text
t0: GET .../events            → records（最早的 20 条）、latest_event_id = L
t1: GET .../events/stream?last_event_id=L
      → 服务端立即推送 (L, now] 区间内的全部事件，再按 1 秒周期继续
```

`latest_event_id` 与分页记录在同一次读取中取得，因此 (t0, t1) 之间写入的事件不会落空。
`EventTimelinePanel` 只在历史状态为 `ready` 后才建立连接；历史加载失败时先显示错误与重试，
不猜一个起点。

### 3.4 服务端连接生命周期

- 客户端断开 / 请求上下文取消 → 推送循环立即返回；
- `agentflow server` 关闭 → `http.Server.RegisterOnShutdown` 关闭
  `httpapi.Deps.Shutdown` 通道，推送循环据此结束，避免优雅关闭一直等到连接级超时；
- 均为单 goroutine 的 `select` 循环（`ctx.Done` / `shutdown` / 心跳 / 轮询），不派生
  不可回收的后台 goroutine，也不在 Handler 里创建长期服务。

---

## 4. 前端实现

### 4.1 连接管理（`useTaskEventStream`）

- 只在有明确任务标识、且历史游标已就绪（`enabled && initialCursor !== undefined`）时连接。
- 任务切换 / 卸载：连接代次自增 + 关闭 `EventSource` + 清空累积 + 重置游标；旧连接的
  任何回调因代次不符被丢弃，且收到的帧还要校验 `task_id` 与当前任务一致。
- `initialCursor` 只在建立第一条连接时使用，之后游标只由收到的帧推进：历史刷新会让该值
  变大，若跟着跳过去，已推送但未处理的中间事件就会被跳过。
- 断线：主动 `close()` 后按 1s / 2s / 4s / 8s 退避重连（`eventStreamReconnectDelay`，封顶
  15s），连续失败达到 5 次（`EVENT_STREAM_MAX_FAILURES`，判定由纯函数
  `eventStreamReconnectExhausted` 给出）即停止自动重连并显示「重新连接」按钮。**这不是数据轮询**：每次重连都是一条
  带游标的长连接，中间不存在固定周期的数据请求。
- 环境不支持 `EventSource` 时明确报错，而不是显示成"暂无事件"。

### 4.2 状态区分

`TaskEventStreamState.status` 取 `idle` / `connecting` / `live` / `reconnecting` / `error`，
与 `EventTimelineState`（`idle` / `loading` / `ready` / `error`）完全分开：历史为空、历史加载
失败、推送连接断开是三种不同事实，面板分别表述。

### 4.3 与历史分页共存

- 历史列表契约不变：`offset` 分页、「加载更多」、手动刷新、「已显示 N / M 条」全部保留。
- 新增独立的「实时事件」区域，只显示**这条连接建立之后**收到的事件，按 `(created_at, id)`
  升序、按真实 `id` 去重（`mergeLiveEvents`），并显式说明"更早的历史请看上方分页列表，
  两者不会合并显示"。因此不存在"看起来像完整连续时间线"的误导。
- 实时更新不清空历史；刷新失败仍保留上一次成功的数据（沿用既有 `useTaskEvents` 行为）。
- 表头给出未读提示「实时新增 K 条（见下方实时事件）」：实时区域位于整段历史之后，没有该
  提示时用户不会知道下方还有新的阶段事件。提示只统计收到的事件条数，不表达任何任务状态。

### 4.4 与任务状态刷新协作

- 收到新事件后，在 800ms 窗口内合并触发**一次** `onRefreshTask`（复用父组件
  `useTaskLiveSnapshot` 的串行刷新链路，同一时刻最多一轮）。
- 不从事件类型推断成功、失败、恢复资格或操作可用性；任务、工作流、步骤与修复周期的展示
  仍来自各自已有的真实接口。
- 既有约束未动：任务切换票据、迟到响应丢弃、刷新失败保留快照、同步 POST 的语义与在途防重。
  SSE 只重连只读连接，绝不会重发执行 / 验证 / 重试 / 恢复 / 取消 / 代码保存请求。

---

## 5. 文件清单与关键调用链

新增：

- `internal/httpapi/event_stream.go` —— SSE 处理器、路径解析、游标解析、推送循环。
- `lingting-agent-flow-ui/src/pages/tasks/useTaskEventStream.ts` —— 前端连接生命周期。

修改：

- `internal/storage/interfaces.go`、`internal/storage/sqlite/events.go` —— `EventStore` 增加
  `ListEventsByTaskAfterID` 与 `LatestEventIDByTask`（均为最小必要的只读能力，无迁移）。
- `internal/app/history_query.go` —— `ListTaskEventsAfter`、`LatestTaskEventID`、
  `TaskEventStreamRequest` / `TaskEventStreamBatch`，`EventTimelinePage.LatestEventID`。
- `internal/httpapi/events.go` —— `EventTimelineResponse.LatestEventID`。
- `internal/httpapi/router.go` —— `Deps.Shutdown`、`router.eventStream`、`{taskID}/events/stream` 分发。
- `internal/httpserver/server.go` —— 创建关闭信号并 `RegisterOnShutdown`。
- `lingting-agent-flow-ui/src/lib/api/types.ts` / `execution.ts` / `index.ts` ——
  `EventTimelineResponse.latest_event_id`、`taskEventStreamPath` / `taskEventStreamUrl`。
- `lingting-agent-flow-ui/src/pages/tasks/useExecutionRecords.ts` —— 事件历史状态新增
  `latestEventID`（失败时保留上一次的值）。
- `lingting-agent-flow-ui/src/pages/tasks/history.ts` —— `mergeLiveEvents` 与展示排序。
- `lingting-agent-flow-ui/src/pages/tasks/EventTimelinePanel.tsx` —— 实时区域与连接状态展示。
- `lingting-agent-flow-ui/src/pages/tasks/TaskExecutionPanel.tsx` —— 接线、合并状态确认。

关键调用链：

```text
浏览器 EventSource
  → GET /api/v1/tasks/{taskID}/events/stream?last_event_id=L
  → router.tasksHandler → parseTaskEventStreamPath → router.serveTaskEventStream
  → eventStreamHandler.streamTaskEvents（方法/Flusher/游标/归属校验）
  → app.HistoryQueryService.LatestTaskEventID / ListTaskEventsAfter
  → storage.EventStore.LatestEventIDByTask / ListEventsByTaskAfterID
  → describeEvent（payload 白名单 + 脱敏）→ eventResponse → SSE 帧
  → useTaskEventStream（去重、排序、游标推进）→ EventTimelinePanel 实时区域
  → 合并后的 onRefreshTask → useTaskLiveSnapshot（只读快照刷新）
```

---

## 6. 本轮实际执行的非测试检查

| 检查 | 命令 | 结果 |
|---|---|---|
| Go 格式 | `gofmt -l internal cmd tests` | 无输出（无文件需要格式化） |
| Go 编译 | `go build ./...` | 通过 |
| Go 静态检查 | `go vet ./...` | 无输出 |
| 前端类型检查 | `pnpm exec tsc --noEmit` | `TypeScript: No errors found` |
| 前端格式 | `pnpm exec vp fmt` | 通过（未改动本次以外的文件） |
| 前端 lint | `pnpm exec vp lint` | 退出码 0 |
| 行尾/空白 | `git diff --check` + `grep -c $'\r'` | 无空白错误，无 CR，全部 LF |
| 完整 diff 审阅 | `git diff`（逐文件） | 已审阅 |

## 7. 未执行项

- **未运行任何测试**：`go test ./...`、`pnpm test:unit:safe`、`pnpm test:e2e:safe` 均未执行
  （用户本轮未授权测试）。因此**不能声称**任何 SSE 行为、游标恢复、前端连接或既有用例
  已经通过测试验证。
- 未执行 `pnpm build` / `make release`：仅为生产构建入口，本轮不改构建配置。
- 未用浏览器实测 SSE 推送：本轮没有明确的 URL 验证授权。
- 未修改任何测试文件；`pnpm test:unit:safe --maxWorkers 4`、`pnpm test:e2e:safe --workers=4`
  的并发设置未动。

## 8. 剩余风险

1. **既有 E2E 桩不含 `latest_event_id`**：`tests/e2e/task-event-history.spec.ts` 的事件桩
   响应没有该字段，前端因此在桩环境下不会建立连接（`initialCursor` 缺省即不连接，这是刻意
   的"不猜起点"）。这意味着既有 E2E 不会意外产生流请求，也意味着**浏览器侧仍没有真实推送
   链路的覆盖**；服务端推送与游标推进已由 §10.3 的 `tests/unit/httpapi/history_test.go`
   用例覆盖（本轮授权范围内只更新断言，未执行）。
2. **推送延迟上限 1 秒**：轮询型 SSE 的固有取舍。事件量很大时会话内仍是分批读取（每批 100），
   未做背压控制：如果某任务的事件可以在极短时间内超过这个速率，客户端会看到连续多帧而不是
   被合并。
3. **长连接与中间层**：只有一条本机连接（`127.0.0.1`）时不存在代理缓冲问题；若将来把服务
   放到反向代理之后，仍需确认代理不缓冲 `text/event-stream`（已发送 `X-Accel-Buffering: no`）。
4. **`latest_event_id` 只消除"查询 ↔ 连接"窗口**，不补齐历史分页中间的缺口：若用户从未点击
   「加载更多」，实时区域与已加载历史之间仍可能有未加载的中间事件。这是既有分页语义，
   面板用「已显示 N / M 条」如实表达，本轮按要求未改变历史查询契约。
5. **未做数字进度或 token 流**：本轮不提供百分比、剩余时间、Agent token、stdout/stderr
   或容器日志流；任何"进度"仍只是阶段事件，不是完成度。
6. **游标超出最新标识时返回 400**：正常的客户端不会触发；若将来允许跨数据库迁移（自增序列
   重置），需要重新评估该判定。

## 9. 下一步候选

- 为推送链路补受控用例：服务端 HTTP 契约用例已补入 `tests/unit/httpapi/history_test.go` 与前端
  纯函数断言（§10.3），但**未执行**，仍需用户明确授权测试后才能运行；浏览器侧 E2E 与真实
  `EventSource` 连接仍未覆盖。
- 若真实使用中出现"历史中间段未加载"的困惑，再评估是否把历史分页改为"最新在前"（属于
  历史查询契约变更，需单独立项）。
- `P4-M5` 仍为 `pending`：本轮没有任何来自真实使用数据的测量。

---

## 10. 重连失败上限修复与既有契约覆盖（2026-10-29）

> 基线：`origin/master` = `cdb1356e4032cef28bae6ffd9ed8445338dc7463`（即本文件 §1–§9 那个
> 提交）。开工前执行了 `git fetch origin`：远端未前进，当前分支 `master`、`HEAD` 与
> `origin/master` 一致，工作区干净（`git status` 无输出）。本轮是范围受限的正确性修复，
> **不是** `P4-M5` 性能优化，`P4-M5` 仍为 `pending`。

> 日期更正（§11 实施时核对）：本小节对应提交 `5c72da75fa4ffe340759c36909eed18d1c56a9bb`
> 的实际提交时间为 `2026-10-29T22:49:06+08:00`（`2026-10-29T14:49:06Z`），此前把该工作记为
> `2026-10-30`，已按提交时间改为 `2026-10-29`。其余历史日期缺少同等证据，未一并改动。

### 10.1 缺陷证据（修复前 → 修复后）

| 项目 | 修复前 | 修复后 |
|---|---|---|
| 判定 | `failuresRef.current > EVENT_STREAM_MAX_FAILURES` | `eventStreamReconnectExhausted(failuresRef.current)`，即 `failures >= EVENT_STREAM_MAX_FAILURES` |
| 实际行为 | 第 5 次失败仍排一次重连（间隔被 15s 封顶），第 6 次失败才停止 | 第 5 次失败即停止自动重连 |
| 与常量注释 / 界面语义 | 注释写"允许的连续失败次数 = 5"，实现等效于 6 | 一致 |

因此自动重连节奏为：失败 1 次 → 1s 后重连；2 次 → 2s；3 次 → 4s；4 次 → 8s；第 5 次失败 →
停止并显示「重新连接」。退避函数本身未改（1s 起、每次翻倍、封顶 15s），只是封顶那一档在当前
阈值下不会再被用到。

### 10.2 修复方式

- 生产代码 `lingting-agent-flow-ui/src/pages/tasks/useTaskEventStream.ts`：新增纯函数
  `eventStreamReconnectExhausted(failures)` 承载"达到即停止"的边界，`source.onerror` 调用
  同一个函数，不再内联比较表达式；常量与退避函数的注释改为与实现一致的表述。
- 未引入新定时器、轮询机制或第二套连接状态；连接代次、任务切换清理、游标推进、事件去重、
  历史分页、手动重连保留已收到事件等既有行为均未改动。

### 10.3 既有测试文件的补充（未新建测试文件）

| 文件 | 补充内容 |
|---|---|
| `lingting-agent-flow-ui/tests/unit/task-event-history.test.ts` | 新增 `describe("重连退避与失败上限")`：失败 0 / 4 不停止、达到 5 停止、6 仍停止；退避 1s / 2s / 4s / 8s 与 15s 封顶，均直接断言 Hook 运行时使用的同一批纯函数 |
| `tests/unit/httpapi/history_test.go` | fixture 增加流的关闭信号（并保证既有用例行为不变）；新增实时流 HTTP 契约用例：非 GET → 405 + `Allow: GET`、非法游标（非整数 / 负数 / 大于最新标识）→ 400 `invalid_request`、不存在的任务 → 404 `not_found`、服务未装配 → 503（并入既有 503 用例）、已持久化事件的实际推送（响应头、帧 `id:`、JSON 字段、不出现 `payload`）、流建立后新写入事件的增量推送与游标推进、关闭信号下以 EOF 收敛、用已送达游标重连不重放 |

上述断言**尚未执行**（见 §10.6），因此不能据此声称任何行为已通过测试验证。

### 10.4 SSE 仍未覆盖的行为

- 浏览器侧真实 `EventSource` 连接：既有 E2E 桩不含 `latest_event_id`，前端因此不建立连接，
  `tests/e2e/task-event-history.spec.ts` 未增加流用例。
- 前端连接生命周期本身：退避定时器实际触发、`reconnecting` → `error` 状态迁移、「重新连接」
  按钮点击、任务切换丢帧只在纯函数层面被断言，没有组件级或浏览器级验证。
- 心跳帧（15 秒注释帧）、超过单批 100 条的背压、`shutdown` 对多条在途连接的影响、
  `Last-Event-ID` 请求头优先于查询参数的端到端行为都未验证。
- 后续专项验收的前置条件：需要用户明确授权测试，并需要一个可在用例内可控启停的流式夹具；
  本轮已有的 `httptest.Server` + 关闭信号是最小可行起点。

### 10.5 本轮实际执行的非测试检查

| 检查 | 命令 | 结果 |
|---|---|---|
| Go 格式 | `gofmt -l internal cmd tests` | 无输出 |
| Go 静态检查 | `go vet ./...` | 无输出（同时确认新增测试文件可编译，未执行） |
| Go 编译 | `go build ./...` | 通过 |
| 前端类型检查 | `pnpm exec tsc --noEmit` | 退出码 0（含 `tests/**/*.ts`） |
| 前端格式 | `pnpm exec vp fmt --check <两个改动文件>` | 通过 |
| 前端 lint / 类型感知 | `pnpm exec vp lint` | 退出码 0 |
| 空白与行尾 | `git diff --check`、逐文件统计 CR | 无空白错误；改动文件 CR 计数为 0（全部 LF） |
| 完整 diff 审阅 | `git diff` | 已逐文件审阅，无无关改动 |

### 10.6 未执行的测试

- `go test ./...`（含 `tests/unit/httpapi`）、`pnpm test:unit:safe`、`pnpm test:e2e:safe` 全部
  **未执行**：本轮未获测试授权。
- 未创建任何测试文件、临时脚本或一次性验证代码；未改动 `--maxWorkers 4` / `--workers=4` 设置。
- 因此本轮结论只包含"缺陷证据 + 修复方式 + 已补断言"，**不包含测试通过结论**。

---

## 11. 浏览器端 SSE 消费路径的 E2E 断言（2026-10-29）

> 基线：`origin/master` = `5c72da75fa4ffe340759c36909eed18d1c56a9bb`（即 §10 那个提交）。
> 开工前执行了 `git fetch origin`：远端未前进，当前分支 `master`、`HEAD` 与 `origin/master`
> 一致，工作区干净（`git status` 无输出）。本轮只补浏览器端 E2E 断言，**未运行任何测试**，
> 未改动 SSE 服务端协议、事件写入、游标设计、事件持久化与任务状态机。`P4-M5` 仍为 `pending`。

### 11.1 实施前的证据与拦截器缺陷

| 项目 | 核实结果 |
|---|---|
| 历史查询路径 | `/api/v1/tasks/{taskID}/events`（`size` / `offset` / `type`） |
| 实时流路径 | `/api/v1/tasks/{taskID}/events/stream` |
| 既有 E2E 拦截器 | `stubEvents` 用 glob `**/api/v1/tasks/task-1/events*`；Playwright 的 `*` 不跨 `/`，实际不会命中 `/events/stream`，但区分靠的是 glob 细节，而不是显式规则 |
| 历史响应游标字段 | `httpapi.EventTimelineResponse.latest_event_id`（`src/lib/api/types.ts`） |
| 前端接线 | `TaskExecutionPanel` 把 `events.state.latestEventID` 传给 `useTaskEventStream.initialCursor`，且只在历史 `status === "ready"` 时 `enabled: true` |
| 既有 E2E 桩 | 不含 `latest_event_id`，因此既有用例不会建立流连接（"不猜起点"） |
| 既有覆盖 | 服务端 HTTP 契约（`tests/unit/httpapi/history_test.go`）与重连纯函数（`tests/unit/task-event-history.test.ts`）已补断言但未执行；浏览器侧完全没有推送链路覆盖 |

拦截器改为锚定正则 `historyEventsPattern(taskID)` = `/api/v1/tasks/{taskID}/events(\?.*)?$`：
历史桩只处理历史查询，实时流由浏览器内受控的 `EventSource` 替身单独驱动，两者不再依赖路由
注册顺序或 glob 细节。为使任务切换场景可用，`stubTaskDetail`（新增 `overrides`）、`stubSteps`、
`stubEmptyRecords`、`stubRepairCycles`、`stubEvents` 增加可选的 `taskID` 参数；**既有调用点
签名不变**，所有旧用例行为未被改动。

### 11.2 新增的浏览器用例（只在既有文件内）

文件：`lingting-agent-flow-ui/tests/e2e/task-event-history.spec.ts`（未新建测试文件）。

| 用例 | 真正验证的行为 |
|---|---|
| 历史返回真实游标后才建立连接，地址携带该游标 | 历史 `latest_event_id = 7` → 建立第一条连接且 URL 为 `.../events/stream?last_event_id=7`；`open` 之前只显示「正在连接」，`open` 之后才是「已连接」，历史条数不受连接状态影响 |
| 历史加载中、加载失败或没有游标时都不建立连接 | 加载中与加载失败时连接数为 0、不渲染实时区域，失败不写成「暂无事件记录」；重试成功但响应没有 `latest_event_id` 时仍不连接，面板如实显示「未连接」 |
| 实时消息显示真实事件、按标识去重，并丢弃畸形与跨任务事件 | 真实 DTO 事件的时间（按浏览器本地时区格式化）、类型与服务端安全摘要展示在独立实时区域；同一 `id` 重复送达只有一行；`not-json`、缺 `created_at`、`task_id` 属于其他任务的三帧都被丢弃（之后送一条有效事件作为"分界"，计数到 3 说明前三帧确实被处理且没有新增行）；分页历史仍是 `1 / 1` |
| 实时事件只合并触发一轮任务状态确认 | 三条事件落在同一个 800ms 窗口内时，任务详情与工作流进度各只多读一次（既有串行刷新链路），随后 600ms 内不再增加；事件中即使出现 `task.completed`，任务状态仍来自接口（「开发中」不变、「已完成」不出现）；全过程零写请求 |
| 切换任务关闭旧连接，新连接使用新任务游标且不混入旧事件 | 切到第二个任务后旧连接被 `close()`；新连接地址使用第二个任务自己的 `latest_event_id=11`；旧连接迟到的帧不进入新任务实时列表，实时计数只统计新连接收到的那一条；关闭详情后连接被关闭且不再建立新连接 |
| 连接中断显示独立状态，重连保留已收到的事件并推进游标 | `error` 后显示「正在重连」与独立提示，历史仍为 ready（既不是加载失败，也不是空历史）；旧连接被关闭且已收到的事件保留；一次退避后新连接 URL 带上已收到的最大事件标识（`last_event_id=6`） |

### 11.3 替身的限制（不得当作服务端 SSE 验收）

- 替身只在测试的浏览器上下文里注入（`page.addInitScript`），生产代码仍使用原生 `EventSource`。
- 它只实现生产 Hook 实际使用的部分：属性回调 `onopen` / `onmessage` / `onerror`、`close()`、
  `readyState` 与静态常量；不实现 `addEventListener`、CORS 语义与浏览器自带重连。
- 因此下列行为**仍未验证**：真实传输层的响应头与帧格式、15 秒 `: ping` 注释帧、
  `X-Accel-Buffering` 之后的代理行为、超过单批 100 条的积压与背压、真实网络中断、浏览器原生
  `Last-Event-ID` 回传、服务端 `shutdown` 下多条在途连接的收敛。
- 自动重连只验证"一次失败 → 一次重建"；`EVENT_STREAM_MAX_FAILURES` 的"达到即停止"与退避封顶
  仍只由 `tests/unit/task-event-history.test.ts` 的纯函数断言覆盖（该断言同样未执行）。
  **「已停止」状态与手动「重新连接」按钮的浏览器路径仍未覆盖**：走完 5 次退避需要真实等待
  1s + 2s + 4s + 8s，本轮按要求不复制阈值逻辑、也不替换全局计时器（会影响 5 秒自动刷新等
  其他组件）。
- 每个用例使用独立浏览器上下文，页面销毁时挂起的重连定时器与长连接一并结束；用例内不替换
  全局计时器，也不留下跨用例的后台任务。

### 11.4 本轮实际执行的非测试检查

| 检查 | 命令 | 结果 |
|---|---|---|
| 前端类型检查 | `pnpm exec tsc --noEmit` | 退出码 0（含 `tests/**/*.ts`） |
| 前端格式 | `pnpm exec vp fmt --check lingting-agent-flow-ui/tests/e2e/task-event-history.spec.ts` | 通过（`vp fmt` 只作用于该文件） |
| 前端 lint | `pnpm exec vp lint` | 退出码 0，无输出 |
| 空白与行尾 | `git diff --check`、逐文件统计 CR | 无空白错误；改动文件 CR 计数为 0（LF） |
| 完整 diff 审阅 | `git diff` | 已逐文件审阅，仅一个既有 E2E 文件与两份 `.steps` 文档 |

### 11.5 未执行的测试

- `go test ./...`、`pnpm test:unit:safe`、`pnpm test:e2e:safe` 全部**未执行**（本轮未获测试
  授权）。因此本轮结论是"补充了浏览器端 E2E 断言，尚未运行测试"，**不能声称**实时推送链路、
  连接状态管理或任务切换清理已经通过测试验证。
- 未新建测试文件、未创建临时脚本或一次性验证代码；`--maxWorkers 4` / `--workers=4` 未改动。
- 服务端心跳、多批背压、原生浏览器连接与真实部署环境仍未验收，也不能用本轮 E2E 替代。

## 12. 全量测试与 SSE 专项验收执行记录（2026-10-31）

> 基线：`origin/master` = `488321cad31a18285c2e7340b4653892bfbc4f44`（提交标题
> `:zap: 补充了浏览器端 E2E 断言`，即 §11 那个提交）。`git fetch origin` 后 `HEAD` 与
> `origin/master` 一致，工作区干净。

本轮获得用户明确授权执行仓库全部测试。`tests/config/` 未做任何改动：`pi.enable`、`ssh.enable`、
`docker.remote_enabled` 沿用仓库现有真实配置（三项均为 `true`），因此真实 Pi、真实 SSH 与真实远程
Docker 用例按配置真实执行，未启用项由测试自身输出 `WARN [SKIP]`。

### 12.1 命令与退出码

| 类别 | 命令 | 退出码 | 结果 |
|---|---|---|---|
| Go 全量 | `mingw32-make test`（等价 `go test -count=1 -v -p 8 -parallel 8 ./tests/...`） | 2 | 38 包 ok / 5 包 FAIL，用例 17 FAIL、2 SKIP |
| Go 竞态 | `mingw32-make test-race` | 0 | 21 个 unit 包 + `integration/local/ssh` 全部 ok |
| 数据库竞态 | `go test -race -count=1 ./tests/integration/local/database/` | 1 | 1 用例 FAIL |
| 工作流竞态 | `go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/` | 0 | 3 包全部 ok |
| 前端单元 | `pnpm test:unit:safe`（`--maxWorkers 4` 未改） | 0 | 28 文件 / 635 用例通过，无跳过 |
| 浏览器 E2E | `pnpm test:e2e:safe`（`--workers=4` 未改） | 非 0（Playwright `5 failed`，`[ELIFECYCLE] exit code 1`；捕获的 shell 码 126） | 254 用例 / 249 通过 / 5 失败 |

本机 Git Bash 没有 `make`，改用 `mingw32-make` 执行同一份 `Makefile`，`PARALLEL` 仍为 8，未拆分
并行、未提高并发。真实 Pi 用例 `TestRealPiCheck` 通过（显式模型 `group/free`）。

### 12.2 6 个 SSE 浏览器用例的逐项结果

全部 6 个用例都被 Playwright 实际发现并执行，结果如下：

| # | 用例 | 结果 |
|---|---|---|
| 1 | 历史返回真实游标后才建立连接，地址携带该游标 | **FAIL** |
| 2 | 历史加载中、加载失败或没有游标时都不建立连接 | **FAIL** |
| 3 | 实时消息显示真实事件、按标识去重，并丢弃畸形与跨任务事件 | **FAIL** |
| 4 | 实时事件只合并触发一轮任务状态确认 | PASS |
| 5 | 切换任务关闭旧连接，新连接使用新任务游标且不混入旧事件 | **FAIL** |
| 6 | 连接中断显示独立状态，重连保留已收到的事件并推进游标 | **FAIL** |

失败原因同一个，且**可稳定复现**（单文件重跑：11 通过 / 5 失败）：这些用例用页面级定位器
`page.getByText("已连接", { exact: true })` 断言推送状态，而该 spec 文件顶部的
`test.beforeEach` 会 `stubHealth(page)`，使头部 `ApiConnectionStatus` 也渲染「已连接」，于是
`getByText` 命中 2 个元素：Playwright 严格模式报 strict mode violation，`toHaveCount(0)`
断言则因计数为 1 而失败。第 4 个用例不读取该文案，因此通过。

- 性质判断：**测试定位器歧义**（非产品状态机或 SSE 协议缺陷）。同一页面出现两处「已连接」也
  反映出「API 连接」与「推送连接」的文案不够可区分，属可记录的 UX 歧义，但现有实现满足
  「连接指示器只表示 HTTP API 可用性、推送状态单独展示」的既有约束。
- 影响范围：仅 §11.2 新增的 6 个用例；既有 `事件时间线` / `修复周期历史` / `自动刷新与副作用`
  等 10 个用例在本文件内全部通过。
- 建议修复点：把推送状态的断言收敛到实时区域作用域（或为该状态加稳定 `data-testid`），
  不改动任何行为断言。本轮按授权范围**未修改测试与产品代码**。

### 12.3 Go 侧失败与竞态失败

| 失败 | 根因 | 性质 |
|---|---|---|
| `integration/ssh`、`integration/remote/{agent,codesaver,docker,workflow}` 共 5 包 / 17 用例 | `tests/config/ssh.yml` 指向的 SSH 目标 `127.0.0.1:22` TCP 连接被拒绝（`connectex: actively refused`），未进入 SSH 握手 | 环境缺失：本机没有监听该端口的 SSH 服务。测试按规范如实失败，未降级为跳过 |
| `TestLegacyDatabaseConcurrentOpenMigratesOnce`（仅 `-race`） | 4 个并发打开者对历史库执行 25 个迁移，`BeginTx` 取写锁超过 `defaultBusyTimeout`（5s）的 `retryBusy` 窗口后返回 `SQLITE_BUSY` | 产品侧并发鲁棒性缺口：非 race 全量测试下该包通过，`-race` 下整包运行 2/2 复现，单用例隔离运行 5/5 通过 |

远程包的包级 `FINALIZER`（容器零残留核验）同样因同一 SSH 不可达而 `status=FAIL`，已按规则作为
测试失败记录，未降级为警告。远程资源零残留在本轮**无法核验**。

### 12.4 跳过项

仅 2 个用例 SKIP，均为测试自身机制：`TestRealPiModelRecognition`（本机 Pi 的模型清单不可用，
不识别 `group/free`）、`TestEmbeddedFSHasNoIndexInSourceCheckout`（源码检出下无嵌入产物，属设计内
跳过）。无其他跳过。

### 12.5 测试产物

- Go：每个包的 `TEST_ARTIFACT_DIR=<绝对路径>` 与 `test.log` 全部保留在
  `%TEMP%\lingting-agent-flow-tests\`（43 个包目录），基目录 `latest-run.txt` 指向最近一次运行。
- 前端：`TEST_ARTIFACT_DIR=%TEMP%\lingting-agent-flow-ui-tests\run-*\`；全量 E2E 的
  `playwright-report` 与 `playwright-output`（失败截图、`trace.zip`、`error-context.md`）在
  `run-CLGmsr`，SSE 单文件重跑的在 `run-Wo1Oyz`。均未删除。

### 12.6 仍未覆盖

浏览器侧仍使用受控 `EventSource` 替身（§11.3），因此**真实 SSE 传输层未验收**：帧格式与响应头、
15 秒 `: ping` 注释帧、`X-Accel-Buffering` 之后的代理缓冲、超过单批 100 条的积压与背压、真实网络
中断、浏览器原生 `Last-Event-ID` 回传、`shutdown` 下多条在途连接收敛、「已停止 + 手动重新连接」
的完整浏览器路径仍未执行。本轮结果不得用于声称实时推送链路已通过传输层验收。

---

## 13. SSE 全链路验收闭环（2026-10-31）

> 基线：当前本地 checkout `d74a504`（`:memo:  测试验收文档`）。按任务要求**未执行 `git fetch`**，
> 也不与远端比较 SHA；开工前 `git status --short` 无输出，收工时只有 3 个文件被修改。本轮获授权
> 执行全部必要测试，因此本节的结论均来自实际运行结果。

### 13.1 修复：推送状态定位器歧义（§12.2 的 5 个失败）

| 项目 | 内容 |
|---|---|
| 缺陷性质 | 测试定位器歧义（非产品状态机、非 SSE 协议缺陷） |
| 证据 | 页面头部 `ApiConnectionStatus` 与实时区域都渲染「已连接」；该 spec 的 `beforeEach` 会 `stubHealth`，因此页面级 `page.getByText("已连接", { exact: true })` 命中 2 个元素 → strict mode violation，`toHaveCount(0)` 断言也因计数不符失败 |
| 修复方式 | 产品侧给实时区域根节点加 `data-testid="task-event-live"`（`EventTimelinePanel.tsx`，纯测试钩子，不改变渲染内容与状态语义）；测试侧新增 `liveRegion(page)` / `liveStatus(page)` 两个作用域定位器，把 8 处推送状态断言（「正在连接」1 处、「已连接」4 处、「未连接」2 处、「正在重连」1 处）全部收敛到实时区域 |
| 结果 | `tests/e2e/task-event-history.spec.ts` 全文件 19 个用例全部通过（此前 16 个用例中 5 个失败） |

### 13.2 服务端真实 SSE 传输测试

全部补在既有文件 `tests/unit/httpapi/history_test.go`（未新建测试文件），断言基于真实
`httptest.Server` 连接、真实响应字节与真实持久化事件：

| 新增用例 | 真正验证的行为 |
|---|---|
| `TestTaskEventStreamSendsHeartbeatComments` | 空闲连接按既定 15 秒周期收到心跳注释帧，首帧到达时间落在 14–25 秒（吸收调度抖动），两次心跳间隔同样落在该区间；心跳块必须**恰好**是 `: ping` 一行，不含 `id:` / `data:`，因此不会被当成业务事件、也不会推进 `Last-Event-ID` 游标；心跳之后的真实事件照常作为消息帧送达（两者在同一条连接上交替出现），且消息帧内不含注释行；`shutdown` 关闭后心跳循环立即停止，流以 EOF 收尾，收尾前不再出现业务事件帧 |
| `TestTaskEventStreamDeliversEventsAcrossBatches` | 持久化 205 条事件（单批上限 100）后，从游标 0 建立连接，前 120 条在**同一轮推送**内跨批取完（耗时 < 2 秒；若按"每轮询周期只推一批"则光等待就 ≥ 2 秒）；事件标识与持久化顺序逐条一致（不漏发、不重复、不乱序）；穿插写入另一个任务的 5 条事件全部未出现，证明按任务隔离而不是按自增序列连续推送；客户端断开后处理器立即退出（按真实请求计数断言，不依赖超时）；用最后确认的事件标识重连补齐剩余 85 条，不重放已送达部分；`shutdown` 收尾时不再出现任何业务事件帧 —— 说明历史查询的 `offset` 分页语义没有被误用到实时游标查询 |
| `TestTaskEventStreamStopsOnWriteFailure` | 用实现 `http.Flusher` 的写失败响应器模拟"客户端已断开"：写出 360 字节后报错，处理器立即返回（不进入下一轮轮询）；失败前已完整写出的帧按持久化顺序构成前缀，末尾被截断的半帧不计为已送达；写响应头先于失败完成且确实发生过 flush；用最后确认的游标重连，剩余事件补齐、已送达的 20 条中已确认部分不重发 |
| `TestTaskEventStreamStopsOnRequestCancel` | 请求取消后处理器返回（连接处理的 goroutine 收敛），不留下只在写失败后才结束的循环 |
| `TestTaskEventStreamSlowClientDoesNotBlockOtherTasks` | 慢客户端故意不读响应体：一个任务积压 205 条大摘要事件（总量远超本机 socket 缓冲区，服务端写入会阻塞在该连接上），另一个任务的事件仍在 10 秒内送达，说明慢客户端只占用自己的连接处理；取消两条连接后处理全部释放 |

同时新增了若干测试辅助（`readStreamBlocks` / `streamBlock` / `collectStreamEventIDs` /
`requireStreamEventIDs` / `openEventStream` / `trackStreamHandlers` / `failingStreamWriter` /
`streamEventIDsFromBytes`）。心跳用例是唯一耗时显著的一个（约 30 秒），因为它是**真实等待**两个
15 秒心跳周期；生产路径的周期常量未改动，也没有引入可配置间隔或第三方时钟依赖。

### 13.3 浏览器端重连耗尽与手动重连

补在既有文件 `lingting-agent-flow-ui/tests/e2e/task-event-history.spec.ts`（未新建测试文件），
复用既有受控 `EventSource` 替身与生产 Hook 的纯函数边界，退避时长由 Playwright 页面时钟
（`page.clock`）在**建立连接与收首帧之后**才接管，因此不替换全局计时器实现、也不影响既有自动
刷新逻辑。

| 新增用例 | 真正验证的行为 |
|---|---|
| 自动重连连续失败达到上限后停止，不再安排新的连接 | 第 1 次失败后 1 秒、第 2 次后 2 秒、第 3 次后 4 秒、第 4 次后 8 秒各重建一次（每一步都先推进到"还差 100ms"断言此刻**没有**新连接，再推进到该到的时间点断言连接已建立，因此同时验证间隔与"不提前重连"）；第 5 次连续失败后进入「已停止」，显示「事件推送已停止」与"已停止自动重连"提示，随后把时钟推进 60 秒，连接总数仍为 5 —— 不安排第 6 条连接，与 `EVENT_STREAM_MAX_FAILURES = 5` 一致；反复失败不清空已收到的实时事件、不伪造事件、零写请求 |
| 停止后手动重新连接使用已收到的最大游标，并保留已收到的事件 | 达到停止条件后实时区域出现「重新连接」入口；点击后立即创建新连接，地址为 `.../events/stream?last_event_id=6`（已收到的最大事件标识），已收到的事件仍在；新连接 `open` 后状态回到「已连接」；手动重连后再次连续失败仍按同一上限收敛（再从 1s/2s/4s/8s 走到「已停止」，连接总数停在 10，不进入无限重连）；全程零写请求，未触发任何执行、验证、重试、恢复、取消或代码保存 |

### 13.4 原生 `EventSource` 验证与实测发现

新增一个用例（同一 spec 文件内），不注入替身，由测试自己在进程内启动受控 SSE 服务端
（`node:http`，随机端口，逐帧写出并保持连接打开），再用 `page.route` + `route.continue` 把页面
自己的同源请求改写到该服务端：

| 覆盖点 | 结果 |
|---|---|
| `text/event-stream` 被浏览器识别 | 页面里 `String(window.EventSource)` 含 `[native code]`（确认没有替身），且原生连接确实收到了消息 |
| 标准 `data:` 帧解析并触发 `message` 回调 | 响应体由 1 条注释心跳 + 2 帧真实 DTO 事件 + 1 条畸形帧组成：两帧真实事件都进入既有实时事件列表，计数为「连接后新增 2 条」 |
| 畸形数据不污染页面 | 中间的 `data: not-json` 帧被丢弃：不显示、不影响后续帧、不改变计数 |
| 连接地址来自真实游标 | 服务端收到的首个请求为 `/stream?last_event_id=5`（由页面按历史 `latest_event_id` 拼出） |
| 页面卸载后连接终止 | 关闭详情后服务端观察到连接被关闭（`response` 的 `close` 事件），随后 500ms 内没有新的流请求 |

实测发现（测试基础设施限制，不是产品缺陷）：`route.fulfill` 会一次性写完响应体并立刻结束连接，
浏览器按任务队列派发消息时会与连接关闭竞争——一段含 3 帧的响应体只能稳定收到**前 2 帧**（用 3 帧
全合法的事件体实测复现：请求发生了 4 次、页面只渲染 2 条）。因此原生验证改用真实本地 SSE 服务端。
这也说明：**不能用 `route.fulfill` 验证"多帧连续推送"这类传输层行为**。

**边界（不得扩大解读）**：本轮原生验证只证明"浏览器原生 SSE 解析 + 页面消费 + 卸载时断开"这条
链路可用，**不等于**真实反向代理/代理缓冲、TLS、负载均衡、移动网络或生产部署环境的验收。

### 13.5 本轮发现但未修复的问题（范围外，附证据）

本轮**没有发现 SSE 相关的生产缺陷**：心跳周期、多批推进、写失败退出、请求取消退出、慢客户端隔离、
重连上限、手动重连游标与事件保留、任务切换关闭旧连接，全部按既有设计工作。以下两项与 SSE 无关，
按"范围外且需单独决策"记录，未做任何重构：

| 问题 | 证据 | 性质 |
|---|---|---|
| `integration/remote/{agent,codesaver}` 的包级 `FINALIZER`（容器零残留核验）在全量并行运行时 FAIL | 日志显示 `remote/agent` 只看见 `agentflow-test-73468784b5ac8343-run`（属 `remote/docker`，该包随后 `REMOTE_CONTAINER_CLEANUP ... result=absent` 已删除）；`remote/codesaver` 自己只创建并删除了 `agentflow-codesaver-369e3c85ede6`，却把 `remote/docker`/`remote/agent` 当时正在使用的容器记为"未登记残留"。两个包单独串行复跑（`go test -count=1 -p 1 ./tests/integration/remote/agent/ ./tests/integration/remote/codesaver/`）退出码 0 | 测试基础设施缺陷：零残留核验**全局列举** `agentflow-test-*`，与并行跑的其他远程包在途容器冲突（跨包误判）。修复需要按"包归属/运行标识"过滤，属远程资源治理范围 |
| `go test -race` 全量单元运行时两个用例超时失败 | `tests/unit/httpapi` 的 `TestExecuteStepAPINotFound/步骤不属于任务` 报 409（同一 fixture 的三个 `t.Parallel()` 子用例共享同一个任务，租约互斥导致 409 先于 404）；`tests/unit/store` 的 `TestMigrateUpgradesExistingDatabase` 报 `context deadline exceeded`；`tests/integration/local/database` 的 `TestLegacyDatabaseConcurrentOpenMigratesOnce` 报 `SQLITE_BUSY`（§12.3 已记录）。三者单独运行均通过 | 高负载下的超时/隔离缺陷：前两个是测试自身隔离问题（并行子用例共享任务、超时预算偏紧），第三个是并发迁移的忙等待窗口，均与 SSE 无关 |

另有一个真实外部依赖的随机失败：`integration/remote/workflow` 的 `TestRemoteE2EFullChain` 在第二轮
全量中失败（`停在交付阶段时必须留下代码保存记录，实际分类 verification_inconclusive`）。原因是
容器内真实 Pi（`group/free`）本轮返回的语义审查输出无法解析为结构化评估，产品按设计收敛为
`recovery_required` / `verification_inconclusive`（没有伪造通过），而该用例的"未完成"分支只接受
"停在交付阶段"这一种终局。同一份被测代码下该用例第一轮全量通过、随后单独复跑（
`go test -count=1 -run TestRemoteE2EFullChain ./tests/integration/remote/workflow/`）退出码 0
（耗时 168s）。属外部模型输出随机性 + 用例断言过窄，需单独任务决定断言策略，本轮未改动。

### 13.6 命令与退出码（最终代码）

| 类别 | 命令 | 退出码 | 结果 |
|---|---|---|---|
| Go 全量 | `mingw32-make test`（= `go test -count=1 -v -p 8 -parallel 8 ./tests/...`；本机 Git Bash 无 `make`，用 `mingw32-make` 执行同一份 `Makefile`） | 2 | 40 包 ok / 3 包 FAIL / 3 包 no test files；用例 1916 PASS / 1 FAIL / 2 SKIP（详见 §13.5） |
| Go 竞态 | `mingw32-make test-race` | 2 | 19 包 ok，2 包 FAIL（`tests/unit/httpapi`、`tests/unit/store`，见 §13.5）；因 make 在该步失败后停止，`integration/local/ssh` 未在该次执行 |
| Go 竞态（SSH 部分） | `go test -race -count=1 ./tests/integration/local/ssh/` | 0 | ok |
| Go 竞态 A | `go test -race -count=1 ./tests/unit/source/codesaver/ ./tests/unit/store/` | 0 | 2 包 ok |
| Go 竞态 B | `go test -race -count=1 ./tests/integration/local/database/` | 1 | `TestLegacyDatabaseConcurrentOpenMigratesOnce` FAIL（`SQLITE_BUSY`） |
| Go 竞态 C | `go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/` | 0 | 3 包 ok（含本轮新增的 5 个 SSE 用例） |
| 失败用例隔离复跑 | `go test -race -count=1 -run <单个用例> <包>` | 0 | 三个失败用例单独运行均通过 |
| SSE 包单独复跑 | `go test -count=1 -run 'TestTaskEventStream' ./tests/unit/httpapi/` | 0 | 9 个用例全部通过（4 个既有 + 5 个新增） |
| 前端全量 | `pnpm test:all:safe`（`--maxWorkers 4` / `--workers=4` 未改动） | 0 | 单元 28 文件 / 635 用例通过；浏览器 257 用例通过；`vp fmt` + `vp lint --fix` + `tsc --noEmit` 通过；生产构建成功 |
| SSE 浏览器用例单文件 | `pnpm exec playwright test tests/e2e/task-event-history.spec.ts --workers=4` | 0 | 19 用例全部通过（16 既有 + 3 新增） |

### 13.7 用例统计

- Go：1916 PASS / 1 FAIL / 2 SKIP。两个 SKIP 均为测试自身机制：`TestRealPiModelRecognition`
  （本机 Pi 的模型清单不可用，不识别 `group/free`）、`TestEmbeddedFSHasNoIndexInSourceCheckout`
  （源码检出下无嵌入产物）。真实 Pi、SSH、远程 Docker 用例按 `tests/config/` 的显式启用状态
  真实执行，未为了增加数量启用任何外部服务，也未修改任何配置。
- 前端：单元 635 PASS / 0 FAIL / 0 SKIP；浏览器 257 PASS / 0 FAIL / 0 SKIP。
- SSE 专项：服务端 9 个实时流用例全部通过；浏览器 8 个实时推送用例（6 既有 + 2 重连）+ 1 个原生
  `EventSource` 用例全部通过。

### 13.8 测试产物

- Go：每个包的 `TEST_ARTIFACT_DIR=<绝对路径>` 与 `test.log` 保留在
  `C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\`，基目录 `latest-run.txt` 指向
  最近一次运行的 `test.log`；未删除任何工作空间。
- 前端：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-imm1Jz\`
  （本轮最终 `pnpm test:all:safe` 的 `playwright-report` 与 `playwright-output`）；
  单文件重跑与诊断的运行目录同样保留，未删除。

### 13.9 仍未覆盖

1. 真实反向代理（nginx 等）与代理缓冲、CDN、TLS 终止、负载均衡下的长连接行为：`X-Accel-Buffering: no`
   只是声明，未经真实代理验证。
2. 真实网络中断（拔网线、NAT 超时、中间层 RST）下的重连行为；本轮的重连由替身与本地服务端驱动。
3. 浏览器原生 `Last-Event-ID` 请求头回传：`parseEventStreamCursor` 的头优先逻辑仍只有代码证据，
   没有端到端用例（前端始终使用 `last_event_id` 查询参数，见 §3.2）。
4. 服务端 `shutdown` 下多条在途连接的**同时**收敛：单连接收敛已被覆盖，多连接并发收敛未单独断言。
5. 生产部署下的慢客户端背压策略：当前实现按连接独立、逐批写并阻塞在该连接上（有 socket 缓冲区
   作为事实上限），没有跨连接的队列、限流或丢弃策略；本轮只验证"互不阻塞"。
6. `integration/remote/workflow` 的真实模型输出稳定性（见 §13.5），与远程容器零残留核验的跨包
   并行误判。

### 13.10 P4-M5

`P4-M5` 仍为 `pending`。本轮只补齐验收覆盖与测试证据，没有采集任何真实使用数据，也没有据此做任何
性能优化；本轮结果不得用于替代真实使用数据。

### 13.11 收工检查

| 检查 | 命令 | 结果 |
|---|---|---|
| 完整 diff 审阅 | `git diff`（逐文件） | 仅 `EventTimelinePanel.tsx`（1 处测试钩子）、`task-event-history.spec.ts`（定位器收敛 + 3 个新用例 + 辅助）、`history_test.go`（5 个新用例 + 辅助） |
| 空白错误 | `git diff --check` | 无输出 |
| 行尾 | 三个改动文件逐行统计 CR | 均为 0（全部 LF） |
| Go 格式 | `gofmt -l tests/unit/httpapi` | 无输出 |
| 前端格式/lint/类型 | `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | 退出码 0 |
| 工作区 | `git status --short` | 只有上述 3 个文件被修改，未纳入测试产物、真实配置或用户原有文件 |
