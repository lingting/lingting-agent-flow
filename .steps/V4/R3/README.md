# V4 R3：worker 池与批量执行

**状态：** 已完成（2026-11-23）。worker 池、拉取式派发、独占用例、优先级与报告 `workers` 分节均已落地，
验收记录见 §9。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R0 冻结决策 D-10](../R0/README.md)、[R2 额度内核](../R2/README.md)。
**下游关系：** 改变"一条 Case 一个 worker 进程"的模型，是 unit 迁移（R5–R7）的性能前提；目录收敛在
[R4](../R4/README.md)。

---

## 1. 目标

把执行模型从"一条 Case 一个 worker 子进程"改为**批量 worker**，以消除 unit 全量迁移后的进程启动开销，
同时不牺牲隔离性：

1. Case 可以声明**必须独占 worker**（有副作用的用例：关闭服务端、占用固定端口、改全局环境等）。
2. 一个 worker 进程按顺序执行多条 Case。
3. 全部 Case 进入队列，由 worker 主动拉取。
4. 独占用例优先派发；在其执行完毕前，至少保留一个 worker 继续消费可复用用例。

## 2. 冻结口径

**V3 不变量 4 改写**：一条 Case 独占"一次执行"；worker 可在用例之间复用，**声明独占用例必须使用全新
worker，并在该用例结束后退出**。

**新增 worker 不变量**：

| 编号 | 内容 |
|---|---|
| W1 | 并发仍由编排进程决定：worker 的"拉取"是请求，派发决定只由调度器做出；worker 不得自行挑选用例 |
| W2 | 独占用例持有 worker 的整个生命周期：不复用、不并发、结束后 worker 退出 |
| W3 | 复用 worker 顺序执行，每 Case 前后重置：工作目录、环境变量快照、日志文件、句柄文件 |
| W4 | 优先级：独占用例先派发；独占队列非空时至少保留 1 个 worker 消费可复用用例（不得全部占给独占用例而让可复用队列空转） |
| W5 | 结果归属：每条 Case 的结果只能来自它自己那次执行；worker 崩溃或缺结果文件不得影响已定型结果 |
| W6 | 报告完备：每条 Case 仍是一条结论；worker 事件（`reused` / `restarted` / `exited`）进报告 |
| W7 | 确定性：同一输入两次运行的派发顺序一致；拉取顺序不依赖 map 迭代 |

## 3. 协议草案（编排 ↔ worker）

```text
编排进程                                    worker 进程
   | 启动（--exec-worker --worker-id N）  -->  |
   | <-- 就绪                                |
   | 派发（stdin 一行 JSON：case_id + 句柄）--> |
   | <-- 结果文件 + 完成消息（exit_code）      |
   | 派发下一条 / 或对该 worker 发送 shutdown --> |
```

- 派发消息携带：`case_id`、资源句柄文件路径、工作空间目录、超时、真实测试授权结论。
- 结果仍写 `cases/<case-id>/result.json`（沿用现有格式），worker 只回报"本 Case 结束"。
- 编排对每个 worker 维护：已执行条数、复用次数、最近一次退出原因。
- 超时/取消：编排先投递 context 取消 → 宽限期 → 终止进程树；复用 worker 被终止后不计入复用。

## 4. 实现落点

| 文件 | 改动 |
|---|---|
| `internal/testrunner/case.go` | 新增 `Case.DedicatedWorker bool`（默认 false）+ `Validate` 说明 |
| `internal/testrunner/worker.go` / `workerproc.go` | 常驻 worker 循环：领取 → 执行 → 回报 → 再领取；每 Case 重置 cwd / env / 日志 |
| `internal/testrunner/scheduler.go` | 队列按"独占优先 + 可复用"两段；worker 槽位与 Case 队列解耦；W4 保留策略 |
| `internal/testrunner/orchestrator.go` | worker 池生命周期、崩溃重启、结果归属、取消/超时路径 |
| `internal/testrunner/report.go` | v2 追加 `workers` 分节：`total` / `reuse` / `restarts` / `isolated_cases` / 每条 Case 的 worker 号 |
| `cmd/agentflow-test/main.go` | `--exec-worker`（worker 模式）与 `--workers`（可选，默认沿用 `--parallel`）；`--exec-case` 保留为单条调试入口或删除（R3 决定并记录） |
| `tests/runner/concurrencycheck/` | 新增 worker 池自检 Case（见 §5） |

## 5. 验收探针

| 探针 | 期望 |
|---|---|
| 复用：N 条无副作用 Case | 实际启动的 worker 数远小于 N（`workers.total` < N，`reuse` > 0） |
| 独占：声明 `DedicatedWorker` 的 Case | 每条都使用新 worker，执行完该 worker 退出，`isolated_cases` 等于条数 |
| 优先级：独占 + 可复用混合队列 | 独占用例先派发；独占执行期间仍有 ≥1 个 worker 消费可复用用例 |
| 隔离：独占用例关闭端口 / 服务 | 后续可复用用例不受影响（复用 worker 未被污染） |
| 崩溃：一条 Case `panic` / `os.Exit` | 该条结论为 `panic`/`error`，其余 Case 继续，`restarts` += 1 |
| 无结果文件 | 该条记 `error` 并给出原因，不得计为通过 |
| 计数守恒 | `workers.total`、`reuse`、`restarts` 与报告条目自洽（W5/W6） |
| 时长 | 记录 unit 试点（R5）的进程启动开销对比数据，写回 R5 文档 |

## 6. 授权

- 静态：`gofmt`、`go vet`、`go build`、`--list`。
- 运行：`concurrencycheck` 与 `selfcheck` 分组；unit 试点在 R5 单独授权。

## 7. 风险

| 风险 | 处置 |
|---|---|
| 复用 worker 内的全局状态串味（包级 var、`os.Chdir`、环境变量、fd） | W3 强制重置 + 自检 Case 显式验证；迁移时逐包核对包级可变状态（R5–R7 每包登记） |
| 用例 panic / `os.Exit` 杀死复用 worker，连带丢失同 worker 后续结果 | 结果按 Case 落盘、每 Case 独立结果文件；worker 崩溃只影响当前 Case |
| 独占声明被滥用（全部声明独占 → 退回旧模型） | 报告 `isolated_cases` 占比；迁移文档要求逐条说明为什么必须独占 |
| 取消/超时路径漏 `release`（额度与 worker 槽位） | I8 守恒自检覆盖取消、超时、worker 启动失败路径 |

## 8. 不做的事

不改资源词汇表与作用域语义、不改 `Case` 既有字段语义、不引入第三方调度/并发库、不搬目录、不迁移 unit 用例。

---

## 9. 验收记录（2026-11-23）

### 9.1 落点

| 文件 | 改动 |
|---|---|
| `internal/testrunner/workerprotocol.go`（新增） | 控制协议：stdin 一行指令（`run` / `shutdown`），stdout 一行事件（`ready` / `started` / `done` / `shutdown` / `invalid`） |
| `internal/testrunner/workerloop.go`（新增） | worker 侧：顺序领取用例；每 Case 重置工作目录、环境变量与标准流（W3）；结论仍只来自结果文件（W5） |
| `internal/testrunner/workersession.go`（新增） | 编排侧的一条常驻 worker：启动与就绪、派发与等待、超时/取消终止、协议污染判定、自行退出 |
| `internal/testrunner/workerpool.go`（新增） | 槽位与 worker 池：复用选择、独占用例使用的一次性 worker、序号分配、收尾 |
| `internal/testrunner/dispatch.go`（新增） | 派发循环：独占段 + 可复用段、W4 保留槽位、派发与完成记账、取消与未派发收口 |
| `internal/testrunner/scheduler.go` | 移除"一 Case 一进程"的 `startCase` 与旧 `runSchedule`；保留额度状态、`planCase` 与报告条目 |
| `internal/testrunner/orchestrator.go` | `runCase` 经 worker 池执行并回传 worker 观测事实；`OnCaseStart` 取代 spawn 回调；时长改用用例自身区间 |
| `internal/testrunner/workerproc.go` | 一次性 worker 保留（独占用例与单条调试）；新增 `Spawned` 观测与 `--worker-id` 透传 |
| `internal/testrunner/case.go` | 新增 `Case.DedicatedWorker` 与字段说明；包注释改为 worker 池口径 |
| `internal/testrunner/context.go`、`worker.go` | `Context.WorkerID()` 与 `WorkerRequest.WorkerID` |
| `internal/testrunner/report.go`、`result.go` | 顶层 `workers` 分节（`total` / `reuse` / `restarts` / `isolated_cases`）与每条 Case 的 `worker_id`；终端摘要增加 worker 池行 |
| `internal/testrunner/workspace.go` | `<run-id>/workers/` 目录与 `WorkerLogRoot` |
| `cmd/agentflow-test/main.go` | `--exec-worker` / `--worker-id`；`--list` 新增 `DEDICATED` 列 |
| `tests/runner/concurrencycheck/worker.go`（新增） | 复用 6 条、独占 2 条、状态隔离 2 条 |
| `tests/runner/concurrencycheck/fault.go` | worker 崩溃与伴随用例（场景 17） |
| `docs/testing.md`、`tests/README.md` | 执行入口、用例模型、worker 池、产物布局与报告字段同步；分组条数按实测订正 |

### 9.2 实现决定

- **`--exec-case` 保留**：它是独占用例的执行方式，也是单条调试入口，不是"过渡适配器"，不引入任何
  `go test` 路径；常驻 worker 用新增的 `--exec-worker`。
- **不引入 `--workers`**：槽位数等于 `--parallel`，进程只在第一次派发时启动，之后在用例之间保留复用。
  独立的池大小会让"并发上限"与"进程数"两套口径并存，收益不足。
- **独占用例用一次性 worker**：执行完即退出、永不复用（W2）；槽位上原有的常驻进程保持空闲，继续服务
  下一条可复用用例。
- **报告顶层 `workers` 分节**：`total + reuse` 等于进入过 worker 进程的用例条数；
  `concurrency.worker_launches` 保留并与 `workers.total` 一致。
- **`restarts` 口径**：只统计没有按协议收尾的进程（用例内 `os.Exit`、控制通道被污染、派发前已退出）。
  编排进程主动终止的超时与取消不计入——它们已经由该用例的 `timeout` / `error` 结论表达。
- **W4 保留槽位**：可复用队列非空且已有用例在跑时，独占派发不得拿走最后一个空闲槽位；`inFlight == 0`
  时不受该限制，避免 `--parallel 1` 下把本可派发的用例判成不可满足（I7）。
- **取消没有反向通道**：worker 只在用例之间读指令，"先投递取消 → 宽限 → 终止进程树"仍按宽限 + 终止
  实现；被终止的 worker 不复用，也不计 `restarts`。
- **协议污染不猜测**：控制通道出现无法解析的一行即判该 worker 失效（当前用例记 `error`）。用例输出
  正常不会进入控制通道：worker 启动后标准流指向空设备，每条用例再指向自己的日志文件，迟到的日志同样
  落在空设备上。

### 9.3 探针结果

| 探针 | 命令 | 结果 |
|---|---|---|
| 复用（W1/W6） | `./bin/agentflow-test --parallel 2 --artifact-dir .test-artifacts/r3-reuse concurrencycheck/worker-reuse-...` | 6/6 `passed`；`workers` = `total 2` / `reuse 4` / `restarts 0` / `isolated_cases 0`；`worker_id` 只取 1、2；每条 `duration_ms` 为 250（不是进程存活时长） |
| 独占 + 优先级（W2/W4） | `./bin/agentflow-test --parallel 4 --artifact-dir .test-artifacts/r3-dedicated concurrencycheck/worker-dedicated-... concurrencycheck/worker-reuse-...` | 8/8 `passed`；`workers` = `6 / 2 / 0 / 2`；按 `dispatch_at` 排序，两条独占用例最先派发且 `worker_id` 各只出现一次；同一轮还有 2 条可复用用例在跑 |
| 状态隔离（W3） | `./bin/agentflow-test --parallel 1 --artifact-dir .test-artifacts/r3-bleed concurrencycheck/worker-bleed-...` | 2/2 `passed`；`workers` = `1 / 1 / 0 / 0`；两条用例 `worker_id` 都是 1（同一进程），后一条确认环境变量与工作目录已被重置 |
| 崩溃与结果归属（W5/W6） | `./bin/agentflow-test --parallel 2 --artifact-dir .test-artifacts/r3-crash concurrencycheck-fault/worker-crash concurrencycheck-fault/worker-crash-companion` | 崩溃用例 `error`（`worker 未写出结果文件（退出码 3）`），伴随用例 `passed`；`workers` = `2 / 0 / 1 / 0`；运行退出码 1 |
| 整组回归（含 R2 额度） | `./bin/agentflow-test --parallel 8 --artifact-dir .test-artifacts/r3-concurrency concurrencycheck` | 58/58 `passed`；`max_in_flight` 8；`workers` = `10 / 48 / 0 / 2`；包额度 `limit 2 / peak 2`、两个池 `limit 3 / peak 3` 与 R2 一致；零残留 `passed` |
| selfcheck 分组 | `./bin/agentflow-test --parallel 4 --artifact-dir .test-artifacts/r3-selfcheck selfcheck` | 3 条：2 `passed` + 1 `skipped`（带原因）；`workers` = `1 / 2 / 0 / 0`；零残留 `passed` |
| 超时 / panic / 迟到结果 / 崩溃 | `./bin/agentflow-test --parallel 4 --artifact-dir .test-artifacts/r3-fault2 --selfcheck-late-result concurrencycheck-fault/panic concurrencycheck-fault/panic-companion concurrencycheck-fault/timeout concurrencycheck-fault/timeout-companion concurrencycheck-fault/late-result concurrencycheck-fault/worker-crash concurrencycheck-fault/worker-crash-companion` | panic 1 / timeout 2 / error 1 / passed 3；迟到结果没有改写已定型的 `timeout` 结论（只进用例 `warnings`）；`workers` = `4 / 3 / 1 / 0`；退出码 1；无残留进程 |
| 取消注入（I8） | `./bin/agentflow-test --parallel 4 --artifact-dir .test-artifacts/r3-fault --selfcheck-cancel-on concurrencycheck-fault/cancel-a --selfcheck-late-result concurrencycheck-fault concurrencycheck-fault/worker-crash` | 取消记录（在跑 4 / 未派发 6），其余 6 条如实记 `error` 并注明未派发；退出码 1；零残留 `passed` |
| 确定性（W7） | `./bin/agentflow-test --parallel 1 --artifact-dir .test-artifacts/r3-seq1 concurrencycheck/worker-reuse-... concurrencycheck/worker-dedicated-... concurrencycheck/worker-bleed-...`（连跑两次） | 两次运行派发顺序完全一致：独占段（`worker-dedicated-a`、`worker-dedicated-b`）先派发，可复用段按 ID 升序（`worker-bleed-a`、`worker-bleed-b`、`worker-reuse-01…06`）；`worker_id` 分配同样一致（独占用例各得一个新进程，其余 8 条共用 1 个常驻进程） |
| 静态门禁 | `make check`、`go vet ./...`、`go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend`、`./bin/agentflow-test --list` | 全部通过；`--list` 403 条 Case / 25 个分组 |

### 9.4 口径与遗留

- `--list` 由 391 条增至 403 条（本阶段新增 12 条自检）。R5–R8 登记的 `--list` 目标（498 / 814 / 1712）
  是在 360 基线上算出的，收口时按实测顺延，不作为本阶段的偏差。
- unit 试点的进程启动开销对比数据按 §5 在 R5 采集并写回 R5 文档；本阶段给出可对比的基线：同一
  `concurrencycheck` 分组 58 条用例只启动 10 个进程、`reuse 48`（`total + reuse` 等于用例条数），
  R2 记录里的 `worker_launches 48`（48 条用例 48 个进程）是旧模型口径。
- 复用 worker 中的"包级可变状态"不在 W3 重置范围内（与 `go test` 同包共用进程的情况一致），由 R5–R8
  逐包登记；用例需要独占时可以声明 `DedicatedWorker`。
- worker 自身的标准错误落在 `<run-id>/workers/<worker-序号>/stderr.log`；用例日志仍在各自产物目录，两者
  不混写。
- 本阶段未运行的授权外分组：`resourcecheck`、`pilot`、`real`（按 R3 §6 不在授权范围），R5 试点时按 R5
  的授权运行。
- 待办（需推送授权）：一次完整 CI 复核，确认 `cases-*` 与 `ui-test` Job 在 worker 池下全绿。
