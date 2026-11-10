# V3 R3：并发调度与取消

**状态：** 完成。调度器、取消路径、并发观测与 9 个调度场景自检已落地并冻结。
**上游文档：** [路线总规划](../ROADMAP.md)、[R2 资源管理器](../R2/README.md)。
**范围：** 让编排进程成为唯一并发决策者，并补齐取消、容量、互斥与迟到结果处理。未迁移既有用例
（R4.5 / R5）、未实现前端适配器（R6）、未改 `Makefile` / CI / `docs/`。

---

## 1. 目标

R1 是严格串行执行，`Case.Parallel` 只冻结了字段。本阶段交付：

1. 调度器：在同一个运行内决定并行度、排队顺序与资源互斥。
2. 容量：按资源类型限制同时在飞的用例数，超出即排队，不允许靠失败重试掩盖。
3. 取消：运行被中断时停止派发、终止在跑 worker 的进程树、回收资源、如实给出结论。
4. 迟到结果防护：已被判定终态的用例不接受后续结果文件覆盖。

R3 完成后，`t.Parallel()` 在新体系里没有任何作用，并发完全由调度器决定。

---

## 2. 输入（已冻结）

- 结论只有六种：passed / failed / skipped / panic / timeout / error（R1 §5、ROADMAP §1）。
- worker 是一次启动约 20ms 的子进程；单用例总开销约 30ms（R1 §8）。并发预算按此估算。
- 等待与终止：用例超时后先由 worker 的 deadline 接管，编排进程再用
  `taskkill /T /F /PID`（Windows）或 `kill(-pid, SIGKILL)`（非 Windows）兜底（R1 §7）。
- 资源分配只由编排进程完成；共享资源整次运行只分配一次（R1 + R2）。
- 现有并发基线是 `-p 8 -parallel 8`（`Makefile` 的 `PARALLEL ?= 8`）。
- `tests/integration/remote/workflow` 必须整包串行（R0 §6）；SQLite 迁移与连接在高并发下会超时
  （`Makefile` 的 `test-race` 注释记录了这一点）。

---

## 3. 交付物（已完成）

| 位置 | 内容 |
|---|---|
| `internal/testrunner/scheduler.go` | 队列、并行度、独占键、包级串行、容量控制、并发观测 |
| `internal/testrunner/cancel.go` | 信号上下文、取消传播、宽限期等待与进程树回收 |
| `internal/testrunner/late.go` | 迟到结果复核与自检注入（结论定型后只留痕不改写） |
| `internal/testrunner/report.go` | 运行级并发观测（`concurrency`）与取消结论（`cancellation`） |
| `internal/testrunner/orchestrator.go` | 按调度结果汇总、清理阶段使用与取消解耦的上下文、共享资源串行化 |
| `internal/testrunner/workerproc.go` | worker 启动回调、超时/取消两条终止路径 |
| `internal/testrunner/schedule.go` | `sqlite-db` 容量收紧为 4（R3 唯一改动的调度属性） |
| `tests/runner/concurrencycheck` | 9 个调度场景的自检组：`concurrencycheck` + `concurrencycheck-fault` |
| `cmd/agentflow-test` | `--parallel`、`--grace` 语义定稿（替换 `-p` / `-parallel` 双旋钮） |
| 本文 §4 / §5 / §6 / §7 / §8 | 实现结论、场景自检、冻结决策、执行记录与验收结果 |

---

## 4. 实施步骤

### 4.1 调度输入模型（已完成）

`Case` 补齐 `Package`，`Parallel` 语义定稿：输入只来自用例声明与 R2 的冻结表。

- `Case.Package`：所属 Go 测试包目录；同时是串行作用域（非空时取包，否则退化为 `Group`）与包级
  串行声明（`SerialPackageReason`）的输入。
- `Case.Parallel`：`false` 表示该用例在串行作用域内独占执行，对齐 `go test` 里未标记 `t.Parallel`
  的用例；用例无法用代码扩大或缩小并发。
- 独占键与容量都从 `Case.Resources` 推导（`ExclusiveKeyOf` / `CapacityOf`），调度器里没有任何针对
  具体包的硬编码例外。

### 4.2 调度器（已完成）

`runSchedule` 是单线程的确定性扫描：每一步按 ID 升序检查待派发用例，满足全部约束就立即派发。

- 同时运行需要同时满足：在飞数未达 `--parallel`、独占键不冲突、串行作用域允许、容量可用。
- 占用在派发前一次性预留（`reserve`），结论回传后释放（`release`）；运行级共享资源的容量与独占键
  整次运行只占一次，其余用例复用同一句柄。
- 共享资源获取在 `sharedResources` 的互斥锁内整体串行化（D4）：并发获取不会产生两份句柄。
- 容量永远无法满足的声明在派发前判错（`impossible`），不会留在队列里等一个不存在的空位。
- 结论按选择顺序（ID 升序）写回 `report.cases`，与完成顺序无关。

### 4.3 容量与背压（已完成）

- 每类资源声明容量（`ResourceProfiles`）；超出容量的用例排队，等待时长进入报告的 `queued_ms_*`。
- 并发上限与资源容量取更严格的一方；`--parallel` 默认 8，不取 `runtime.NumCPU()`。
- 本阶段唯一改动的容量：`sqlite-db` 从"不限"收紧为 4（对应 §2 的 SQLite 高并发超时），其余 11 类
  本机资源保持 R2 的"0 表示不限"。

### 4.4 取消（已完成）

- 第一次中断（`os.Interrupt` / `SIGTERM`）：停止派发新用例，在跑 worker 进入宽限期（`--grace`）。
- 宽限期后仍未退出：终止 worker 进程树，再释放该用例已分配的资源。
- 取消不伪造结论：在跑用例记 `error` + 「运行被取消」，从未派发的用例记 `error` + 「运行被取消，用例
  未派发执行」；运行级取消原因写入 `report.cancellation`，不新增第七种状态。
- 清理阶段使用 `context.WithoutCancel`：释放、自证与零残留终检不因取消而跳过或被取消打断。
- 自检注入 `--selfcheck-cancel-on <case-id>` 在该用例的 worker 真正启动后触发取消，使这条路径可稳定
  复现；与真实信号走同一个取消函数。

### 4.5 迟到结果防护（已完成）

worker 进程树被终止后仍可能写出 `result.json`。编排进程只在结论未定型时读取结果文件；结论定型为
`timeout` / `error` 后，写报告之前再做一次复核：磁盘文件声称另一种状态时只记录用例警告
（`结论已定型为 timeout，忽略迟到的结果文件（声称 passed）`），结论不变。
自检注入 `--selfcheck-late-result` 主动写出这样一份文件，使该路径可稳定复现。

### 4.6 报告扩展（已完成）

- 运行级 `concurrency{parallel_limit, max_in_flight, worker_launches, queued_ms_total, queued_ms_max}`。
- 运行级 `cancellation{cancelled, reason, at, cancelled_cases, undispatched_cases}`。
- 用例级 `timings` 新增 `queue_enter_at` / `dispatch_at`，与 R1 的四个时间点并存。
- 终端摘要追加「并发」与「取消」两行，其余格式不变。

### 4.7 9 个调度场景自检（已完成）

`concurrencycheck`（场景 1–5，全部通过）与 `concurrencycheck-fault`（场景 6–9，故意失败）共 17 条
用例，覆盖 §5 全部场景；判定依据是报告字段与用例日志，不依赖固定等待时长。逐场景命令与观测见 §7.1。

---

## 5. 9 个调度场景

| 编号 | 场景 | 判定 |
|---|---|---|
| 1 | 无资源用例并行 | 在飞数量达到上限，总耗时小于串行 |
| 2 | 同一独占键互斥 | 两条例程不重叠，报告显示等待 |
| 3 | 运行共享资源只分配一次 | 两条用例拿到同一句柄，分配计数为 1 |
| 4 | 包级串行 | 同组用例不重叠，跨组可并行 |
| 5 | 资源容量上限 | 超出容量的用例排队，不失败 |
| 6 | 用例超时 | 超时用例判 timeout，其余用例照常完成 |
| 7 | panic 隔离 | panic 只影响自身，运行继续并给出非零退出码 |
| 8 | 取消 | 不再派发、在跑被终止、进程树无残留、资源已释放 |
| 9 | 迟到结果 | 终止后写出的结果文件不得覆盖已判定的 timeout / error |

| 场景 | 自检用例 |
|---|---|
| 1 | `concurrencycheck/parallel-a..d`（无资源、允许并行、同样占用窗口） |
| 2 | `concurrencycheck/exclusive-a/b`（同一 `git-worktree` 独占键） |
| 3 | `concurrencycheck/shared-a/b`（同名 `run-shared` 端口） |
| 4 | `concurrencycheck/serial-a/b`（声明整包串行的 `tests/integration/remote/workflow`）+ `serial-c`（普通包） |
| 5 | `concurrencycheck/capacity-1..6`（各声明一个独享 `sqlite-db`） |
| 6 | `concurrencycheck-fault/timeout` + `timeout-companion` |
| 7 | `concurrencycheck-fault/panic` + `panic-companion` |
| 8 | `concurrencycheck-fault/cancel-a/b`（占用端口后一直阻塞）+ `cancel-c`（不得被派发） |
| 9 | `concurrencycheck-fault/late-result`（超时后注入迟到结果） |

---

## 6. 阶段内需要冻结的决策（已定稿）

| 编号 | 决策 | 结论 |
|---|---|---|
| D1 | `--parallel` 语义 | 同时运行的 worker 上限，默认 8；资源容量、独占键与串行作用域可进一步压低 |
| D2 | 取消的结论表达 | 用例 `error` + 「运行被取消」，未派发用例 `error` + 「未派发执行」；运行级记录取消原因；不新增第七种状态 |
| D3 | 排队顺序 | 按 ID 升序扫描、就绪即派发；报告顺序按 ID 升序，与完成顺序无关 |
| D4 | 共享资源并发获取 | 同一运行内整体串行化（互斥锁内完成分配），不允许并发重复分配 |
| D5 | 默认资源容量 | 沿用 R2 表与「0 表示不限」语义；R3 只把 `sqlite-db` 收紧为 4（本机唯一显式受限类型，用于场景 5） |
| D6 | 串行作用域 | `Case.Package` 非空时取包路径，否则取 `Group`；`Parallel: false` 与包级串行都在该作用域内互斥 |
| D7 | 取消时序 | 停止派发 → 宽限期 → 终止进程树 → 释放资源（与取消解耦的上下文）→ 零残留终检 → 报告 |
| D8 | 迟到结果防护 | 结论定型后只留痕不改写；写报告前复核一次并写入用例警告 |
| D9 | 报告新增字段 | `concurrency`、`cancellation`、`timings.queue_enter_at` / `dispatch_at`；报告版本仍为 `agentflow-test-report/1`（只做加法） |

---

## 7. 运行授权与执行记录

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `selfcheck`、`selfcheck-fault`、`concurrencycheck`、`concurrencycheck-fault` |
| 允许的命令 | `./bin/agentflow-test --parallel <N> --artifact-dir .test-artifacts <集合>`；`tasklist` / `ps` 只读查看进程 |
| 明确不允许 | `tests/unit`、`tests/integration`、`tests/e2e` 的 `go test`；真实 Pi / SSH / 远程 Docker；前端命令 |
| 理由 | 调度语义可在自检组内完整覆盖；真实用例的并发正确性在 R4 / R4.5 验证 |

场景 8 需要核对进程残留，只允许只读列举进程，不允许用终止命令清理非本运行创建的进程。

### 7.1 执行记录（2026-11-10，Windows 11 + Go 1.25.7）

| 命令 | 退出码 | 观测 |
|---|---|---|
| `--parallel 4 selfcheck` | 0 | 通过 2 / 跳过 1；峰值 1（同组用例串行） |
| `--parallel 1 selfcheck` 与 `--parallel 8 selfcheck` | 0 / 0 | 逐条结论完全一致（§8.1） |
| `--parallel 2 selfcheck-fault` | 1 | 失败 1 / panic 1 / 超时 1，均如实记录 |
| `--parallel 8 concurrencycheck` | 0 | 17 条全部通过；峰值 8；排队合计 4024ms（最长 1078ms）；worker 启动 17；零残留 passed |
| `--parallel 1 concurrencycheck` | 0 | 17 条结论与并行运行一致；峰值 1；排队合计 52614ms |
| `--parallel 4 'concurrencycheck/parallel...'` | 0 | 场景 1：峰值 4（达到上限），4 条用例耗时 289–291ms，总 298ms，远小于串行累加 |
| `--parallel 4 'concurrencycheck/exclusive...'` | 0 | 场景 2：峰值 1；后一条排队 1004ms；两条区间不重叠，独占键生效 |
| `--parallel 4 'concurrencycheck/shared...'` | 0 | 场景 3：峰值 2；两条用例日志中的 address 都是 `127.0.0.1:59063`；`resources[]` 只有一条 `shared-port` 记录，`released` / `verified` 均为 true |
| `--parallel 4 'concurrencycheck/serial...'` | 0 | 场景 4：峰值 2；`serial-a` 00.582→00.864、`serial-b` 00.874→01.150（不重叠），`serial-c` 00.582→00.866 与 a 重叠 |
| `--parallel 8 'concurrencycheck/capacity...'` | 0 | 场景 5：峰值 4（容量 4 生效）、排队合计 638ms（最长 319ms）、6 条全部通过 |
| `--parallel 2 'concurrencycheck-fault/timeout...'` | 1 | 场景 6：`timeout` 判 timeout，伴随用例 passed |
| `--parallel 2 'concurrencycheck-fault/panic...'` | 1 | 场景 7：`panic` 只影响自身，伴随用例 passed，运行照常出报告 |
| `--parallel 2 --grace 300ms --selfcheck-cancel-on concurrencycheck-fault/cancel-b 'concurrencycheck-fault/cancel...'` | 1 | 场景 8：`cancelled=true`、原因写明注入来源；在跑 2 条记「运行被取消」，`cancel-c` 记「未派发执行」；两条用例的端口 `released` / `verified` 均为 true；`tasklist` 复查无遗留 `agentflow-test.exe` |
| `--parallel 1 --selfcheck-late-result 'concurrencycheck-fault/late-result'` | 1 | 场景 9：结论保持 timeout，警告为「结论已定型为 timeout，忽略迟到的结果文件（声称 passed）」 |
| `go vet ./...`、`gofmt -l cmd internal tests` | 0 / 空 | 无新增问题 |

自检暴露的一个真实缺陷：取消时用例资源释放原本复用被取消的上下文，释放报「上下文已结束」，端口
留在系统里。修复方式是用 `context.WithoutCancel` 派生出清理上下文，释放与自证不再被取消打断；修复后
场景 8 的端口逐条 `released` / `verified` 为 true。

本阶段未运行 `tests/unit` / `tests/integration` / `tests/e2e` 的任何 `go test`，未触碰真实 Pi / SSH /
远程 Docker，未运行前端命令。

---

## 8. 验收条件与结果

| 条件 | 结果 |
|---|---|
| 1. 同一集合在串行与并行下的结论完全一致 | 达成：`selfcheck` 与 `concurrencycheck` 分别在 `--parallel 1` / `8` 下逐条结论一致 |
| 2. §5 的 9 个场景全部通过，且不依赖固定等待时间 | 达成：判定依据是报告字段（峰值在飞数、排队时长、区间、取消结论、警告）与用例日志，占用窗口只用于制造并发而非断言 |
| 3. 取消后：无遗留 worker、无遗留子进程、资源全部释放、终检执行、退出码非零 | 达成：在跑用例 `released` / `verified` 为 true、`residual_check=passed`、退出码 1、`tasklist` 无遗留进程 |
| 4. 迟到结果无法覆盖已定型的 timeout / error 结论 | 达成：注入的 `passed` 结果只进入警告，结论保持 timeout |
| 5. 报告包含并发上限、峰值在飞数、排队时长；同一输入两次运行的用例顺序一致 | 达成：`concurrency` 五项字段齐全；`cases` 始终按 ID 升序，与完成顺序无关 |
| 6. 共享资源在并发下只分配一次；独占键冲突被调度器阻止而不是事后报错 | 达成：场景 2、3 的观测；容量不足的声明在派发前判错 |
| 7. `go vet ./...` 与 `gofmt -l` 无新增问题（不运行测试） | 达成 |

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| SQLite 与端口在高并发下超时 | 默认并行度 8；`sqlite-db` 容量 4；排队而不是靠重试掩盖阻塞 |
| Windows 进程树终止不可靠 | 回收失败判 `error`；宽限期内未退出即终止进程树；场景 8 必须通过才进入 R4 |
| 场景自检本身易 flaky | 用启停事件、计数器与阻塞点确认状态，不用 `sleep` 猜时序；失败必须能稳定复现 |
| 调度器内置包级例外 | 例外只能来自声明；R2 的 `serialPackages` 表是唯一来源 |
| 取消路径上的资源泄漏 | 清理阶段用与取消解耦的上下文；取消后仍执行零残留终检 |
| worker 与编排进程之间没有取消通道 | 取消只能通过终止进程树生效；worker 侧不响应取消时由宽限期兜底，不在 worker 里新增 IPC |
| `resourcecheck` 组未在 R3 授权范围内复跑 | 调度只改变派发顺序，资源语义未变；R4.5 / R5 的真实用例批次会重新覆盖资源路径 |

---

## 10. 不做的事

- 不迁移任何既有 `_test.go` 用例（R4 / R4.5 / R5）。
- 不实现真实资源提供者（R2 已交付）。
- 不实现前端适配器与前端并发（R6）。
- 不修改 `Makefile`、CI 与文档（R8 / R9）。
- 不新增 `go test` 适配层。

---

## 11. 输出给 R4

1. `--parallel`、容量、独占键与包级串行的最终语义（§6 D1、D5、D6）。
2. 取消路径的完整时序：停止派发 → 宽限期 → 终止进程树 → 释放资源（`context.WithoutCancel`）→
   零残留终检 → 写报告（§4.4、§6 D7）。
3. 9 个场景的自检结果与可复现命令（§7.1）；`concurrencycheck` 运行退出码 0，
   `concurrencycheck-fault` 各场景命令退出码 1。
4. 报告新增字段的最终形态：`concurrency{...}`、`cancellation{...}`、
   `timings.queue_enter_at` / `timings.dispatch_at`（§4.6、§6 D9）。
5. R4 可以直接复用的入口：`testrunner.RunOptions.Parallel`、`testrunner.SignalContext()`；
   用例声明侧只需 `Case.Parallel` 与 `Case.Package`，不需要了解调度器实现。
