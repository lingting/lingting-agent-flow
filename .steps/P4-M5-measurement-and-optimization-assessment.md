# P4-M5：基于真实数据的性能与架构优化评估（第一轮）

> 本文件是 **P4-M5 的第一步**：核实真实运行数据是否支持某项优化、建立可复核的测量基线、
> 并给出是否立项的明确结论。
>
> **本轮没有修改任何生产代码、前端代码、数据库结构或测试代码。** 结论是：现有证据不足以
> 支持任何优化立项，P4-M5 保持 `pending`，并记录"解除待定所需的数据"。
>
> 编写时间：2026-10-28；核实基线：`origin/master`。测量环境为本机单进程 + 本地 SQLite，
> 全部数据见 §4 与 §7，**不是生产环境数据**。

## 1. 基线与环境

| 项 | 取值 |
|---|---|
| 远端基线 | `git fetch origin` 后 `origin/master` = `cf546939b0db87a648602a03c0bfdda298eed467`（`:zap: 单步诊断动作确认`，2026-10-27 23:09:18 +0800） |
| 本地分支 | `master`，与 `origin/master` 同 SHA，工作区干净（`git status` 无改动） |
| 操作系统 | Windows 11 |
| CPU / 内存 | Intel Core Ultra 7 251HX，18 逻辑核 / 50 GB |
| Go | `go1.24.5 windows/amd64` |
| SQLite 驱动 | `modernc.org/sqlite v1.45.0`（纯 Go） |
| 交叉核对用 CLI | `sqlite3 3.44.3`（仅用于 `EXPLAIN QUERY PLAN` 与量级核对，**不是**生产驱动） |
| 数据库 | 单文件 SQLite + WAL，单 `agentflow server` 进程 |
| 部署形态假设 | 本地单用户；多实例 / 多数据库部署**未测量**，结论不得外推 |

生产数据：**未获取**。本机不存在任何真实部署的数据库或日志
（`~/.lingting/agentflow` 为空，仓库内无 `.agentflow/` 数据库），也没有生产访问权限。
因此本文件中的所有数字都属于 §3 的「可重复实验数据」或「静态代码证据」，
**不能作为生产性能结论**。

## 2. 当前观测能力（核实结果）

| 能力 | 是否存在 | 证据 |
|---|---|---|
| 结构化日志 | 有 | `internal/logging/logging.go`（`slog` 文本输出，级别可配）；调度器关键动作有日志（`internal/app/task_scheduler.go` 的 `run` / `settleFailedDispatch`） |
| HTTP 请求耗时 / 访问日志 | **没有** | `internal/httpserver/server.go`、`internal/httpapi/router.go` 只有 `http.NewServeMux()` 分发；`internal/httpapi` 内 `time.Since` 仅出现在 `validation.go`，且只用于**验证检查项**的 `duration_ms` |
| 指标（Prometheus / expvar / pprof / `/metrics`） | **没有** | `go.mod` 无相关依赖；`grep -rln "prometheus\|opentelemetry\|expvar\|pprof\|/metrics" internal/ cmd/` 无命中 |
| 分布式追踪 | **没有** | 同上 |
| 健康检查 | 有（范围明确） | `GET /api/v1/health` 只报告 `http_api` 与 `database` 两项（`internal/app/health.go`）；**不表达** SSH / Docker / Pi 是否可用 |
| 调度器状态 | 有字段、**无任何生产调用者** | `internal/app/task_scheduler.go:824` 的 `SchedulerStatus{Limit, Active}` / `Status()`；全仓库唯一调用者是 `tests/integration/local/scheduler/scheduler_test.go:789` |
| 数据库诊断 | 部分 | 任务时间线事件（`events` 表，`idx_events_task (task_id, id)`）、验证运行与检查结果、`attempts.git_before_json/git_after_json`；**没有**慢查询日志、查询计数、执行计划记录 |
| 任务队列 / 名额的可查询事实 | 有（在数据库里） | 队列长度 = `tasks.status='queued'`；名额占用由 `domain.DecideTaskSlotState`（租约或未结束的 `workflow_runs`）决定；等待时间可由 `events` 的 `task.started` 与 `tasks.created_at` 之差得出（见 §5.1） |

结论：**当前没有任何请求级延迟、数据库耗时或名额利用率的直接观测数据**。
所有与本任务相关的数字都必须通过 §4 的受控实验获得。

## 3. 证据分级（本文件的使用规则）

| 级别 | 本轮来源 | 可否支撑优化立项 |
|---|---|---|
| 1 真实运行数据 | **无**（无生产库、无生产日志） | — |
| 2 可重复实验数据 | §4 的本地测量（固定环境、固定数据规模、多次采样、原始样本保留） | 只能证明"该规模、该条件下的行为" |
| 3 静态代码证据 | §5 的代码路径、SQL、索引、调用者核实 | 只能提出假设，不能单独作为性能结论 |
| 4 推断 / 未知 | §8 列出的缺口 | 不得据此优化 |

## 4. 测量环境、方法与结果

### 4.1 方法

测量工件全部放在**仓库之外**的随机临时目录（保留未删除）：
`C:\Users\lingting\AppData\Local\Temp\p4m5-1793159954`

| 步骤 | 做法 |
|---|---|
| 构建被测二进制 | `go build -o <WS>/agentflow.exe ./cmd/agentflow`（构建自上述 SHA，无本地修改） |
| 启动被测服务 | `<WS>/agentflow.exe server --config <WS>/cfg.yaml --require-web-ui=false`，监听 `127.0.0.1:26391` |
| 数据集 | SQLite 中直接写入 N 条 `completed` 任务（每个任务 1 条 `requirements`，80% 带 1 条 `completed` 的 `workflow_runs`），`created_at` 按 1 分钟间隔展开，规模 0 / 1 000 / 10 000 / 50 000 各一份独立数据库 |
| 采样 | 每个 URL **30 次**，**交错**发起（每轮依次请求所有 URL，避免单 URL 连续采样受缓存与顺序影响） |
| 统计 | 排序后取中位数、第 95 百分位（`p95 = sorted[floor(0.95n)]`）、最小值与最大值；原始样本逐行落盘（`<WS>/s-*.txt`） |
| 延迟测量 | `curl -s -o /dev/null -w "%{time_total}\n"`，含 loopback TCP 与 Go HTTP 栈开销 |
| 数据库核对 | `sqlite3 <db> "EXPLAIN QUERY PLAN …"` 与 `.timer on`（CLI 3.44.3，仅作量级参照） |
| 服务 CPU | Windows 进程 `TotalProcessorTime` 在 20 秒空闲窗口内的增量：`powershell -NoProfile -Command "(Get-Process -Id <pid>).CPU"`，取值两次求差除以窗口 |

**规模自检**：每个规模开始测量前，先请求 `GET /api/v1/tasks?size=1` 核对 `total` 是否等于
期望规模。这一检查是必需的——第一次尝试时端口被上一轮遗留的监听进程占用，导致全部数据落到
错误的数据集上，该轮结果已整体丢弃、未用于本文件。

### 4.2 任务列表端到端延迟（服务端分页）

数据集：全部为 `completed` 任务（无排队、无在途执行，排除调度器干扰），服务刚启动、缓存已热。

单位毫秒，`n=30`/格（中位数）：

| 任务总数 | `size=1` | `size=20` | `size=100` | `size=20` @25% 偏移 | `size=20` @末尾 | `size=20` 单状态过滤 |
|---|---|---|---|---|---|---|
| 0 | 1.2 | 1.2 | 1.2 | — | — | 1.3 |
| 1 000 | 2.0 | 2.8 | 7.0 | 3.6 | 3.6 | 2.3 |
| 10 000 | 8.1 | 9.1 | 19.7 | 20.2 | 26.6 | 2.9 |
| 50 000 | 34.5 | 39.0 | 77.6 | 250.9 | 174.8 | 6.7 |

第二轮在 50 000 规模、且同一库中另有 3 000 条非终态任务（调度器占用约 13% 单核）时重测：

| 偏移 | 中位数 | p95 | 最大值 |
|---|---|---|---|
| 1 | 36.0 | 37.3 | 37.4 |
| 100 | 66.6 | 77.0 | 87.0 |
| 1 000 | 75.7 | 89.9 | 92.4 |
| 12 500 | 180.9 | 213.9 | 216.0 |
| 49 980 | 157.4 | 162.2 | 166.4 |

**直接观察**：50 000 规模下同样 20 条记录的相邻偏移之间差异可达 2–7 倍，且
`size=1`（1 条记录）就要 34.5 ms。这说明成本主要不来自"返回多少条记录"，而来自
"为了排出前 20 条要处理整张表"（见 §5.2 的执行计划）。

**接口契约核对**（在这些库上真实执行）：

| 请求 | 真实结果 |
|---|---|
| `GET /api/v1/tasks` | `current=1`、`size=20`、`records=20` |
| `GET /api/v1/tasks?size=101` | `400 invalid_request`，`页大小不能超过 100` |
| `GET /api/v1/tasks?current=0` | `400 invalid_request`，`分页参数 current 必须是大于 0 的整数` |
| 10 000 库 `?current=501&size=20` | `{"records":[],"total":10000,"current":501,"size":20}` |

### 4.3 调度扫描的空闲 CPU 成本

`TaskScheduler` 默认扫描间隔为 **1 秒**（`internal/app/task_scheduler.go:28`
`defaultSchedulerScanInterval = time.Second`）。在 50 000 任务库上，用"无计划步骤的
`queued` 任务"（`domain.DecideTaskQueueAdmission` 会静默拒绝，永远不被领取，因此队列
深度稳定）与"历史 `failed` 任务"两种现场测量空闲 CPU：

| 现场 | 非终态任务数 | 20 秒窗口 CPU | 折算单核占用 |
|---|---|---|---|
| 空队列、无历史失败 | 0 | 0.000 s | 0.0% |
| +500 历史 `failed` | 500 | 0.469 s | 2.3% |
| +1 500 历史 `failed` | 1 500 | 1.328 s | 6.6% |
| +3 000 历史 `failed` | 3 000 | 2.625 s | 13.1% |
| 101 个排队（无步骤） | 101 | 0.609 s | 3.0% |
| 402 个排队（无步骤） | 402 | 1.672 s | 8.4% |

另一轮（50 000 任务库、混合现场）测得：200 排队 → 3.2%，902 排队 → 20.3%。

**直接观察**：空闲 CPU 与「非终态任务数」「排队任务数」近似线性——约 **每 100 个非终态任务
0.09% 单核**，约 **每个排队任务每次扫描 0.2–0.3 ms CPU**。零排队且无非终态任务时为 0.000 s，
说明成本确实来自这两类记录的数量，而不是扫描循环本身。

同时测量同一现场下的任务列表延迟：`queued=0` 时中位数 52.9 ms，`queued=101/402` 时
41.3 / 41.6 ms —— **没有出现可归因于调度扫描的延迟劣化**（差异落在 §4.4 的波动范围内）。

### 4.4 测量的局限与误差

| 项 | 说明 |
|---|---|
| 数据集是合成的 | 需求标题/描述为固定模板、`config_json` 为 `{}`；真实任务的 JSON 体积、步骤数、执行记录数不同，因此 **序列化体积与每记录查询成本可能与真实库不同** |
| 无真实用户行为 | 没有"真实点击路径"数据（打开详情、翻页深度、页大小偏好），无法判断真实偏移分布 |
| 单机单进程 | 18 核机器上后台占用 13% 单核对 HTTP 延迟几乎无影响；低核数机器或多实例部署**未测量** |
| 运行时波动明显 | 同一现场相邻两轮在 50 000 规模的 `size=20` 有 39.0 ms 与 36.0 ms，深偏移有 250.9 ms 与 180.9 ms 的差异（约 ±35%）。因此**只比较数量级，不比较几个 ms 的差别** |
| SQLite 实现差异 | `EXPLAIN QUERY PLAN` 与 `.timer` 来自 CLI（C 实现 3.44.3）；生产使用 `modernc.org/sqlite`（纯 Go）。CLI 计时只用于确认"同一数量级"，权威数字以 HTTP 端到端为准 |
| 统计量 | 只给中位数与 p95，未做置信区间；样本量 30，尾部（p99）不可靠 |
| 未测量项 | 浏览器端到端体验（含 React 渲染与网络栈）、`size=100` 在真实 UI 下的实际使用率、并发请求下的排队行为、WAL 增长与数据库文件膨胀、多实例共享同一数据库时的表现 |

## 5. 候选问题、代码证据与决策

### 5.1 候选 A：调度名额可见性（G17）

**业务问题（假设，未证实）**：用户看到任务长期停在 `queued`，无法判断是"名额已满在等待"、
"缺少前置条件"还是"调度器没在工作"。

**代码证据**

| 事实 | 证据 |
|---|---|
| 已有状态快照，但**没有生产调用者** | `internal/app/task_scheduler.go:824-843`；唯一调用者是 `tests/integration/local/scheduler/scheduler_test.go:789` |
| 快照语义**不是**"名额占用" | `SchedulerStatus{Limit, Active}` 的 `Active = len(s.dispatched)`，只统计**本进程已登记的分派**；真实占用的事实来源是 `countOccupiedSlots`（数据库：租约 + 未结束 `workflow_runs`） |
| 因此直接暴露该字段会误导 | 存在"另一个进程持有名额"或"租约有效但本进程未登记"时，`Active < 真实占用`；界面会显示"有名额"却领不到任务 |
| 准入拒绝原因不落库 | `loadCandidates` 中 `!admission.Admitted` 只是 `continue`（静默跳过）；只有 `loadQueueInput` 报错才写 `logger.Warn`。队列里"为什么没被领走"没有任何持久化记录 |
| 失败原因分散在别处 | `failed` 任务的 `failure_summary` 与 `events(task.failed)` 只表达**启动后**的失败，不表达"排队中" |

**可直接用数据库回答的部分**（无需新增接口）：

```sql
-- 队列长度
SELECT count(*) FROM tasks WHERE deleted_at IS NULL AND status = 'queued';
-- 排队等待时间（task.started 在启动流程开始处写入，见 internal/app/task_launch_run.go:133）
SELECT e.created_at - t.created_at AS wait_ms
FROM events e JOIN tasks t ON t.id = e.task_id
WHERE e.event_type = 'task.started';
-- 名额占用的近似（正式判定仍在 domain.DecideTaskSlotState）
SELECT count(*) FROM tasks t
WHERE t.deleted_at IS NULL AND t.status NOT IN ('completed','cancelled')
  AND (t.lease_expires_at > :now
       OR EXISTS (SELECT 1 FROM workflow_runs r
                  WHERE r.task_id = t.id AND r.status IN ('running','cancel_requested')));
```

**数据来源**：只有静态代码证据 + §4.3 的空闲成本。**没有任何一条运行记录显示用户因名额
等待而受阻**（没有生产库、没有等待时间样本、没有排队深度样本）。

**若立项的收益目标与风险（未立项，仅备查）**

- 收益目标：让"排队等待 vs 前置条件缺失"可区分，减少误判为"卡住"的人工排查。
- 风险：名额占用需要按 `DecideTaskSlotState` 语义计算，而该计算要遍历全部非终态任务
  （§4.3 表明这条路径的成本与历史规模线性相关）。若把它做成"每 1 秒轮询一次的接口"，
  等于把后台成本转成用户可见接口成本。任何名额接口都必须先定义**语义**（是本进程分派数、
  还是数据库占用数）、**成本**（是否接受 O(非终态任务)）与**权限边界**（本地单用户可为
  只读；不暴露 `lease_owner`、`worker_id` 等内部标识）。
- 维护成本：新增一个接口 = 新增一份需要与 `domain.DecideTaskSlotState` 长期保持一致的
  展示逻辑；语义漂移会让用户看到与实际不符的名额。

**决策：需要继续测量**（不立项，不写代码）。问题方向合理，但既没有"用户遇到等待"的证据，
也没有"利用率/排队长度"的样本。**最小成本测量方式不是新增接口，而是一次只读 SQL**
（如上三条），并把结果记录下来。解除待定至少需要：

1. 一份真实（或真实使用过一段时间的本地）数据库上，上述三条查询的实际结果；
2. 至少一次"用户报告任务长时间停在 `queued`"的现场，能拿到该任务在队列中的时间与
   当时的非终态任务数；
3. 确认现有 Web 是否已经能表达"排队中"这一状态（`TASK_STATUS_META` 已区分 `queued`），
   即用户缺的到底是"状态名"还是"等待原因"。

### 5.2 候选 B：真实数据量下的任务列表分页

**业务问题（假设，未证实）**：任务变多后，任务列表首页/翻页变慢或产生重复请求。

**服务端行为（已核实，全部为真实代码事实）**

| 检查项 | 结论 | 证据 |
|---|---|---|
| 是否真的在数据库分页 | **是**。`LIMIT ?` + `OFFSET ?`，另有一条独立的 `COUNT(*)` | `internal/storage/sqlite/tasks.go:746-800`（`buildTaskQuery` / `buildTaskCountQuery` / `appendTaskPaging`） |
| 默认页大小 / 上限 | 20 / 100；超过上限返回 **参数错误**，不退化全表 | `internal/app/task_management.go:32-35,181-220` |
| 边界参数 | `current<=0`、`size<=0` 返回 400；越界页返回空 `records` 与正确 `total` | `internal/httpapi/tasks.go:479-520`；§4.2 的真实响应 |
| 排序稳定性 | `ORDER BY created_at DESC, rowid DESC`（同一毫秒内按写入顺序），分页不重复 | `internal/storage/sqlite/tasks.go:749-759` |
| 逻辑删除 | 默认 `deleted_at IS NULL`；部分索引 `idx_tasks_active (status, created_at) WHERE deleted_at IS NULL` | `0009_relax_constraints.sql:381-385`、`0010_task_soft_delete.sql:16` |
| 每页的额外查询 | **每记录 2 次额外查询**：需求标题/描述（`taskNameAndDescription`）+ 该任务的 `workflow_runs`（`TaskCloseoutViews`）。`size=20` → 42 次查询；`size=100` → 202 次 | `internal/httpapi/tasks.go:344-368`；`internal/app/task_closeout_view.go:18-44` |

**执行计划（真实输出）**

```text
-- 无筛选（分页首屏/任意偏移，均为同一计划）
|--SCAN tasks USING INDEX idx_tasks_active
`--USE TEMP B-TREE FOR ORDER BY

-- 带单个 status 筛选
`--SEARCH tasks USING INDEX idx_tasks_active (status=?)

-- COUNT(*)
`--SCAN tasks USING INDEX idx_tasks_active

-- 调度器 countOccupiedSlots 使用的非终态列表（status NOT IN (...)）
|--SCAN tasks USING INDEX idx_tasks_active
`--USE TEMP B-TREE FOR ORDER BY

-- 每页的两次额外查询
`--SEARCH workflow_runs USING INDEX idx_workflow_runs_task (task_id=?)
`--SEARCH requirements USING INDEX sqlite_autoindex_requirements_1 (id=?)
```

**解释**：现有索引都以 `status` 为前导列，因此"不带状态筛选、按 `created_at` 倒序"无法
被索引顺序满足，必须整表扫一遍索引再排序——这解释了 §4.2 中 `size=1` 也要 34.5 ms、
以及"偏移越深越慢"（排序后还要跳过 OFFSET 行）。带单个状态筛选时走
`SEARCH … (status=?)`，同一规模只要 6.7 ms 且**与偏移无关**。

**前端行为（已核实）**

| 检查项 | 结论 | 证据 |
|---|---|---|
| 请求方式 | `ExTable` 的 `request` 透传 `current` / `size` / `status`，服务端分页 | `lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx:497-516,634-643` |
| 页大小选项 | 10 / 20 / 50 / 100，默认 20（与服务端上限一致） | `TasksPage.tsx:50,638` |
| 列表是否被轮询 | **否**。列表只在页/筛选变化与 4 处显式动作后重载；5 秒自动刷新只作用于任务详情与执行进度 | `TasksPage.tsx:298,331,396,416,673`；`useTaskLiveSnapshot` / `autoRefresh.ts` |
| 重复请求控制 | 有请求票据（丢弃迟到响应）；详情与工作流刷新共用同一调度器 | `lingting-agent-flow-ui/src/pages/tasks/requestGate.ts`；`P4-M3-7-progress-auto-refresh.md` |

**数据来源**：§4.2 的可重复实验数据 + 上述静态证据。仍然**没有生产数据**。

**决策：暂缓（不立项）**，理由：

1. 分页本身是正确的服务端分页，参数有上限、排序稳定、边界语义明确；§4.2 也验证了
   越界页、超限页大小与默认参数的真实行为。**不存在"先加载大量记录再内存分页"**。
2. 在 1 000–10 000 任务规模（单用户工具的现实区间）首屏 2.8–9.1 ms、深偏移 3.6–26.6 ms，
   与空库的 1.2 ms 同数量级，**没有任何证据显示用户会感知到慢**。
3. 只有到 50 000 规模才出现 39 ms 首屏 / 175–251 ms 深偏移——但既没有真实库达到该规模，
   也没有证据显示用户会翻到第 1 000 页以后。按 P4-M5 的规则，这属于"没有证据支持该优化"。
4. 前端也不存在"重复请求"或"列表轮询"问题（§4.2 之上已逐项核实）。

**收益目标与风险（若将来证据支持）**：可复现的对照基线已具备——同规模下
`（首屏 size=20，深偏移 size=20，size=100）` 三组数字即可作为优化前后对照；可行的方向
（**均未实施**）是给"按 `created_at` 倒序"提供可被索引满足的排序（或改为键集分页），
以及把每页的 `需求 + workflow_runs` 两次查询改成批量查询。风险：索引会增加写入成本与
迁移需求，键集分页会改变 `current/size` 契约并影响前端与 CLI 的调用点；
批量查询必须保持"读取失败不得冒充空值"的既有语义（`TaskCloseoutViews` 目前遇到读取
失败直接返回错误，改成批量后同样不能吞掉错误）。

### 5.3 候选 C（本轮新增）：调度扫描随历史规模线性增长

**这是 §4.3 测出来的、先前文档没有记录的机制**，本节只登记为"待测量"，不立项。

**代码证据**（`Scan` 每 1 秒一次）：

1. `countOccupiedSlots`：`ListTasks(ExcludeStatuses=[completed,cancelled])` —— 取出**全部
   非终态任务**（`buildTaskQuery` 始终带 `ORDER BY created_at DESC, rowid DESC`，因此还额外
   排序一次），再对"租约无效"的任务逐个 `GetActiveWorkflowRun`。
2. `loadCandidates`：`ListTasks(Statuses=[queued])`，然后对**每个排队任务**调用
   `loadQueueInput`，后者固定发出 4 条查询（`GetRequirement`、`ListStepsByTask`、
   `GetActiveWorkflowRun`、`ListCodeSaveRecordsByTask`）。

因此每秒查询次数的量级是 `1 + 非终态任务数 + 1 + 4 × 排队任务数`。

**测量结果**：见 §4.3（3 000 非终态任务 + 空队列 → 13.1% 单核；402 排队 → 8.4% 单核）。

**为什么只到"需要继续测量"**：

- 这条成本是**结构性**的：`failed` 任务允许显式重试，因此不是终态，会长期保留；
  逻辑删除（`deleted_at`）可以把它们从 `idx_tasks_active` 中摘除，属于用户已有的手段。
- 但在真实部署里到底有多少非终态任务、多少排队任务，**没有任何数据**。
  这决定 13% 是"永不发生"还是"常态"。
- 更重要的是：§4.3 同时显示，即使后台占用 13% 单核，任务列表延迟也没有劣化。
  **资源占用本身不是用户体验问题**，只有它导致延迟、吞吐或电耗时才算问题。

**解除待定所需数据**（只读，无代码改动）：

```sql
SELECT status, count(*) FROM tasks WHERE deleted_at IS NULL GROUP BY status;
```

若非终态任务长期在数百以上，再评估"只统计未过期租约 + 未结束执行记录"的替代实现
（例如改用索引可满足的 `lease_expires_at > now` 条件，或引入 `status` 集合
`('queued','preparing','planned','developing','verifying','retrying','pausing')` 的正向筛选，
后者可以走 `idx_tasks_active (status, created_at)` 的 `IN` 前缀搜索）。这些都属于**后续**
的、需要单独定义范围的实施任务。

## 6. 决策汇总

| 候选 | 业务问题是否可确认真实存在 | 证据强度 | 决策 | 下一步 |
|---|---|---|---|---|
| A 调度名额可见性（G17） | 否（无等待现场、无队列样本） | 静态证据强、运行数据缺 | **需要继续测量** | 用 §5.1 的只读 SQL 采集真实队列长度与等待时间；确认用户缺的是"状态"还是"原因" |
| B 任务列表分页性能 | 否（现实规模 3–9 ms） | 静态 + 可复现实验 | **暂缓** | 若出现真实大库（≥3 万任务）或用户报告卡顿，直接用 §4.2 的同规模基线做对照 |
| C 调度扫描随历史规模线性增长 | 否（机制已确认，频率未知） | 静态 + 可复现实验 | **需要继续测量** | 采集真实库的 `status` 分布；据此决定是否需要收窄扫描集合 |

**本轮不立项的理由总结**：三个候选都缺"真实使用数据"这一必要条件。B 与 C 的
**机制**已经被测量确认，但机制本身不等于问题；A 连机制层面都还没有可用的观测数据。
按 P4-M5 的规则（"如果没有真实使用数据或足够的可重复证据，应如实记录证据缺口，保持待定"），
本轮不修改生产代码。

## 7. 实际执行的命令与真实结果

以下命令均在 `D:\code\lingting\lingting-agent-flow` 下执行（`<WS>` = 上述临时测量目录）。

| 命令 | 真实结果 |
|---|---|
| `git status` / `git branch --show-current` / `git rev-parse HEAD` | 工作区干净；`master`；`cf546939b0db87a648602a03c0bfdda298eed467` |
| `git fetch origin` + `git rev-parse origin/master` | `cf546939b0db87a648602a03c0bfdda298eed467`（与本地同 SHA） |
| `go build -o <WS>/agentflow.exe ./cmd/agentflow` | 成功（无输出） |
| `<WS>/agentflow.exe server --config <WS>/cfg.yaml --require-web-ui=false` | `已从配置导入 3 个角色` / `AgentFlow 服务已启动: http://127.0.0.1:26391` |
| `curl -s http://127.0.0.1:26391/api/v1/health` | `{"status":"ok","checks":[{"name":"http_api","status":"pass"},{"name":"database","status":"pass"}]}` |
| `sqlite3 <db> "EXPLAIN QUERY PLAN …"` | 见 §5.2 的四个计划输出 |
| `sqlite3 <db>` + `.timer on`（50 000 库、3 000 非终态） | `COUNT(*)` 26 ms；`offset 0` 32 ms；`offset 12500` 57 ms（CLI 3.44.3，仅量级参照） |
| `curl -w "%{time_total}"` × 30/格（交错） | 见 §4.2 两张表，原始样本 `<WS>/s-*.txt`、`<WS>/m3-*.txt` |
| `powershell "(Get-Process -Id <pid>).CPU"` 差 / 窗口 | 见 §4.3 |
| 接口契约探测（`size=101`、`current=0`、越界页、默认参数） | 见 §4.2 的四个真实响应 |
| `go vet ./...`、`gofmt`、`go test`、`pnpm lint` / `pnpm test` / `pnpm build` | **未执行**：本轮未修改任何 Go / 前端源码，也无测试授权 |
| 前端 / 浏览器验证 | **未执行**：用户未提供地址，也未要求访问页面 |

## 8. 未获取的数据、尚未解决的问题与后续行动

**未获取**

1. 任何真实部署的数据库、日志与用户请求记录（本机无部署）。
2. 真实任务的队列等待时间分布、排队深度分布、非终态任务数量分布。
3. 真实任务列表的翻页深度与页大小偏好（决定"深偏移"是否值得优化）。
4. 浏览器端到端体验（React 渲染 + 网络栈）与并发请求下的表现。
5. 多实例 / 多数据库部署下的名额占用与扫描成本。

**尚未解决**

1. `SchedulerStatus` 的语义与"名额占用"不一致（§5.1）。若将来要做 G17，必须先定义
   对外语义并说明成本，不能直接返回该结构。
2. 准入拒绝原因完全没有持久化（§5.1），导致"为什么这个任务没被领走"无法事后回答。
   这一点与 G17 的诉求重叠，但属于**新的观测能力**，需要按"最小成本、可只读复盘"的原则
   单独定义，不得顺手引入指标系统。
3. 不限状态的任务列表没有可被索引满足的排序（§5.2）。这是机制事实，但是否值得付出
   索引/迁移/契约成本取决于真实规模。

**下一项具体行动（按顺序）**

1. 在任意一台已经真实跑过任务的机器上执行 §5.1 与 §5.3 的只读 SQL，把
   `tasks.status` 分布、队列长度、等待时间分位数（中位数 / p95 / 最大值）记录进本文件。
   这一步不需要任何代码改动，是解除 A 与 C 待定状态的前置数据。
2. 若第 1 步显示非终态任务长期在数百以上，或排队等待 p95 明显偏高，再就"收窄扫描集合"
   （§5.3 末尾）单独立项，并复用本文件的空闲 CPU 基线作为对照。
3. 若出现 ≥3 万任务的真实库或明确的"列表卡顿"报告，再评估 §5.2 的索引 / 键集分页 / 批量
   查询方向，并以 §4.2 的同规模数字为优化前基线；三者必须分开评估，不得一次改动多项。
4. G17（名额可见性）**不因本文件而进入实施**：在拿到第 1 步数据之前，它仍是
   "缺少证据的体验优化"。

## 9. P4-M5 状态判定

- 状态：**保持 `pending`**。
- 理由：本轮完成了"测量基线 + 可行性核实"，但**没有确认任何真实的、用户可感知的问题**，
  因此不满足 P4-M5 的完成定义（"每项优化有可测量的收益与回归证据"），也不足以进入实施。
- 已在测量工件中保留的对照基线（任何后续优化都必须以它为基准）：
  任务总数 0 / 1 000 / 10 000 / 50 000 的 `size=1|20|100`、深偏移与单状态过滤延迟；
  非终态任务 0 / 500 / 1 500 / 3 000 与排队 0 / 101 / 402 的空闲 CPU 占用。
- 测量工件路径（**保留，未删除**）：`C:\Users\lingting\AppData\Local\Temp\p4m5-1793159954`
  （含 `dbs/*.db`、原始延迟样本 `s-*.txt` / `m3-*.txt`、测量脚本 `*.sh`、服务日志 `srv-*.log`）。
