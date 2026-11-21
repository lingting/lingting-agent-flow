# V4 R3：worker 池与批量执行

**状态：** 未开始。
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
