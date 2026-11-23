# AgentFlow V4：测试体系收敛、并发额度与批量执行

**文档状态：** 主线规划。R0–R5 已完成（盘点、决策冻结、覆盖对账废弃、残留引用清理与基线复验、并发额度内核与报告 v2、
worker 池与批量执行、目录收敛）与 R6 的 unit 批量迁移 A，R7–R9 未开始。本文是阶段划分与设计口径的唯一来源；每个阶段的执行清单与验收探针在 `.steps/V4/R*/README.md`。
**上游输入：** [V3 收口文档](../V3-final.md)、[测试规范](../../docs/testing.md)、
[R0 盘点与决策冻结](R0/README.md)。
**适用范围：** 只重组测试代码位置、测试执行入口、测试并发模型与 worker 执行模型；不改产品行为、不改
数据库 Schema、不改 HTTP 接口、不改前端功能。

---

## 1. 目标

V3 解决了"集成 / E2E / 前端用例只有 `agentflow-test` 一个入口"，留下四个结构问题：

1. **目录仍然分层两套**：共享辅助在 `tests/internal/testkit`，用例在 `tests/runner/<kind>/...`，多一层
   `runner` 中间目录；`tests/unit/**` 仍由 `go test` 驱动。
2. **unit 用例还在 `go test` 里**：同一条测试体系有两个执行入口、两套并发控制、两套结论统计口径。
3. **并发只有全局上限与资源容量**：无法表达"某个包最多 3 条同时在跑"，也无法表达"多个包共享同一个模型
   供应商"。
4. **一条 Case 一个 worker 进程**：unit 全量迁入后，进程启动开销会成为主要成本。

目标终态（一句话）：**一条 Case 模型、一个执行入口、一套并发额度、一个可复用的 worker 池；
`go test` 不再以任何形式执行用例。**

## 2. 非目标

- 不恢复 `go test -race`、不新增覆盖率门禁（V3 §6 已登记的已接受回退）。
- 不改资源词汇表与作用域语义（`run-shared` / `case-exclusive` 仍由编排进程唯一分配与释放）。
- 不改 `Case` 既有字段语义（`ID` / `Group` / `Timeout` / `Package` / `Parallel` / `Resources`）。
- 不引入第三方调度库或并发库；额度与 worker 池只用 Go 标准库实现。
- 不引入 `go test -c` 过渡适配器（D-09：禁用，且以后也不允许出现任何 `go test` 执行路径）。
- 不为旧目录结构或旧入口保留兼容层：迁移完成后 `tests/runner`、`tests/internal` 整体消失。

## 3. 当前事实基线（HEAD `8a214ad`，R0 实测）

### 3.1 用例面

| 项 | 数量 | 说明 |
|---|---:|---|
| `tests/unit/**` 测试文件 | 157 | 全部 `_test.go`，无普通 `.go` |
| `tests/unit/**` 测试包 | 17 | 含 `8a214ad` 新增的 `tests/unit/testrunner` |
| `func Test*`（含 `TestMain`） | 1352 | `TestMain` 17，用例主体 1335 |
| `func Benchmark*` | 0 | — |
| `tests/runner/**` 普通 Go 文件 | 125 | 零 `_test.go` |
| 已注册 Case / 分组 | 360 / 25 | e2e 64 + local 183 + selfcheck 30 + frontend 37 + real 32 + fault 14 |
| `tests/internal/testkit` 文件 | 16 | 含 `Main`（R9.1 删除） |

### 3.2 `testing` API 调用面

`t.Fatalf` 5981、`t.Fatal(` 844、`t.Parallel()` 1411、`t.Helper()` 389、`t.Cleanup(` 180、`t.Run(` 164、
`t.Errorf` 98、`t.Logf` 6、`t.TempDir(` 3、`t.Skipf` 2、`t.Setenv(` 2、`t.Error(` 1、`testing.Short()` 0。
结构事实：`os.Exit(` 17、`httptest` 121、`go func(` 65、`sync.Once` 3、`testkit.New`/`Dir` 73、`func init()` 0。

### 3.3 包分布与迁移批次

| 阶段 | 包（`func Test*`） | 合计 |
|---|---|---:|
| R5 试点 | `execenv`(21)、`lifecycle`(7) | 28（含 2 个 `TestMain`，迁移后 26 条 Case） |
| R6 批量 A | `testkit`(2)、`testrunner`(4)、`cigate`(10)、`app/planningagent`(10)、`image`(11)、`environment`(12)、`server`(14)、`source`(22)、`app/planninganalysis`(25) | 110 |
| R7 批量 B | `domain`(250)、`container`(30)、`agent`(36) | 316 |
| R8 批量 C | `store`(249)、`httpapi`(242)、`app`(407) | 898 |

合计 1352（含 17 个 `TestMain`），迁移后用例主体 1335 条。数量不是上限：若某个 `t.Run` 子测试需要独立成
Case，数量会上升；**不得**为了数量好看而把子测试合并（会丢失定位）。

### 3.4 门禁现状

- CI #63（run `44911970464`，`head_sha 8a214ad`）全部 Job 成功，`ui-test` 的门禁步骤不再是 `skipped`。
- 覆盖对账已随 D-06 废弃：清单（1707 条）、`--audit-*`、`dropped` 登记、`internal/caseinventory`、
  `cmd/agentflow-test-inventory` 与 CI 覆盖对账步骤全部删除。迁移完整性证据改为"每包迁移清单 + 用例全绿"。

## 4. 目标架构

### 4.1 目录终态

```text
tests/
├── config/            真实外部依赖配置（示例入库，真实配置不入库）
├── testkit/           <- tests/internal/testkit（共享辅助，无 Main）
├── registry/          <- tests/runner/registry（空导入聚合）
├── concurrencycheck/  <- tests/runner/concurrencycheck
├── e2e/               <- tests/runner/e2e
├── frontend/          <- tests/runner/frontend
├── integration/       <- tests/runner/integration
├── pilot/             <- tests/runner/pilot
├── resourcecheck/     <- tests/runner/resourcecheck
├── selfcheck/         <- tests/runner/selfcheck
└── unit/              unit 用例来源包（目录保留，文件在 R9 之前仍由 go test 执行）
```

三条命名规则：`tests/testkit` 是共享辅助、`tests/unit/testkit` 是一个 unit 用例包，两者不得合并；用例包
直接挂在 `tests/` 下，不再有 `runner` 这一层；`tests/unit/**` 保留目录（来源包路径不变），但不再是
`go test` 的范围。

### 4.2 执行入口与 worker 池

```text
agentflow-test（编排进程）
├── 选择器 + 真实依赖授权判定
├── 调度器（唯一并发决策者）
│   ├── 全局 --parallel（worker 槽位上限）
│   ├── 包级额度（--package-limit / Case.Concurrency.PackageLimit）
│   ├── 命名并发池（--pool name=limit / Case.Concurrency.Pools）
│   ├── 资源容量（CapacityOf）与独占键（ExclusiveKeyOf）
│   └── 队列：独占段（DedicatedWorker）优先 + 可复用段
├── 资源管理器（唯一分配与释放者）
├── 统一报告 + 产物目录（含额度与 worker 分节）
└── worker 池：worker 主动拉取 Case，按顺序执行多条；独占用例使用全新 worker 并退出
```

## 5. 冻结决策（D-01 … D-10，R0 定稿）

| 编号 | 决策 | 结论 |
|---|---|---|
| D-01 | 目录映射 | 采用 §4.1；`tests/runner` 与 `tests/internal` 整体消失 |
| D-02 | unit 严格终态 | 采用：`tests/unit/**` 不再有 `_test.go`；删 `TestMain`、`testkit.Main`、`make test-unit`、`go-unit` Job、`unit` 分组；不留空转门禁 |
| D-03 | legacy identity | **取消**：不再按冻结清单对账；`Package` 只服务串行作用域与报告定位 |
| D-04 | 额度声明权威 | **CLI 优先，可临时放大**：运行级值存在时即生效（可大于用例声明），报告记录 `declared_limit` 与生效值并 `WARN`；用例之间的同 key 声明冲突仍在注册期硬失败 |
| D-05 | work-conserving | 采用 I5 / I6 |
| D-06 | 覆盖对账 | **取消**：清单、`--audit-*`、dropped、盘点工具、CI 步骤与产物一并删除 |
| D-07 | 报告 schema | `agentflow-test-report/2`，不保留旧版兼容读路径 |
| D-08 | 报告字段 | package / pool：`limit` / `declared_limit` / `peak` / `completed` / `queued_ms_total` / `queued_ms_max` / `undispatched` + 未派发原因；worker：`workers` / `worker_reuse` / `worker_restarts` / `isolated_cases` |
| D-09 | `go test -c` | **禁用**，且以后也不允许任何 `go test` 执行路径 |
| D-10 | worker 模型 | **批量 worker**：① Case 可声明必须独占 worker；② 一个 worker 顺序执行多条 Case；③ 全部 Case 进队列由 worker 拉取；④ 独占用例优先，且其执行完毕前至少保留一个 worker 消费可复用用例 |

## 6. 阶段总览与依赖

| 阶段 | 状态 | 目标 | 文档 |
|---|---|---|---|
| R0 | 已完成 | 盘点、决策定稿、覆盖对账废弃与删除动作 | [R0](R0/README.md) |
| R1 | 已完成（完整 CI 待补） | 清理残留引用、复验门禁、固定 CI 基线 | [R1](R1/README.md) |
| R2 | 已完成 | 并发额度内核：包级上限 + 命名池 + 报告 v2 | [R2](R2/README.md) |
| R3 | 已完成 | worker 池与批量执行：拉取、复用、独占声明、优先级 | [R3](R3/README.md) |
| R4 | 已完成 | 目录收敛：`tests/testkit` 与 `tests/*` | [R4](R4/README.md) |
| R5 | 已完成 | unit 迁移试点：`execenv` + `lifecycle` 共 26 条 Case；新增 `unit` Case kind | [R5](R5/README.md) |
| R6 | 已完成 | unit 批量迁移 A：9 个中小包 101 条 Case，逐包运行通过 | [R6](R6/README.md) |
| R7 | 未开始 | unit 批量迁移 B：`domain`/`container`/`agent` 316 条 | [R7](R7/README.md) |
| R8 | 未开始 | unit 批量迁移 C：`store`/`httpapi`/`app` 898 条（三个子阶段） | [R8](R8/README.md) |
| R9 | 未开始 | 删除 `go test` 执行路径、门禁 / CI / 文档收口、全量验收 | [R9](R9/README.md) |

```text
R0 ──> R1 ──> R2 ──> R3 ──> R4 ──> R5 ──> R6 ──> R7 ──> R8 ──> R9
                                        │
                                        └── 每包迁移后立刻单包运行 + 登记迁移清单
```

硬依赖与理由：

- **R1 先于 R2。** 删除动作留下的引用与门禁必须先干净，否则 R2 之后无法区分"新失败"与"清理不彻底"。
- **R2 先于 R3。** 额度是纯调度决策（键来自 `Package` 与用例声明），worker 池是执行模型；先定准入条件再改
  执行方式，避免一次改动同时改两类语义。
- **R3 先于 R4。** worker 池会改 `tests/runner/**` 的注册辅助与执行入口；先改语义再搬家，提交可评审。
- **R4 先于 R5。** unit 迁移会新增十几个 `tests/unit/<pkg>` 注册点；目录未定稿就开工会二次搬迁。
- **R5 先于 R6/R7/R8。** 试点必须先证明：写法成立、复用 worker 不串味、失败信息可读；试点不通过不得批量。
- **R9 必须在 R8 之后。** 只要还有一个包是 `_test.go`，删除 `make test-unit` 就会丢失覆盖。

### 6.1 每阶段共同的执行纪律

1. 阶段文档里的决策必须开工时定稿并写回文档，不得边做边改接口。
2. 一个阶段只做本阶段的事：R4 只搬目录；R5–R8 只改用例；R2/R3 只改执行器。
3. 每次迁移一个包就是一次可独立回滚的提交：`git diff` 里不得同时出现"移动"与"语义改写"。
4. 迁移不得静默丢断言：每个包迁移完成立刻单包运行并把结果写进阶段文档。
5. 未获授权不运行测试；允许的静态动作只有 `gofmt`、`go vet`、`go build`、`git diff`、`--list`。

## 7. worker 池与批量执行（D-10）

### 7.1 不变量

| 编号 | 内容 |
|---|---|
| W1 | 并发仍由编排进程决定：worker 的拉取是请求，派发决定只由调度器做出 |
| W2 | 独占用例（`Case.DedicatedWorker`）持有 worker 整个生命周期：不复用、不并发、结束后退出 |
| W3 | 复用 worker 顺序执行，每 Case 前后重置 cwd、环境变量、日志文件、句柄文件 |
| W4 | 独占用例优先派发；独占队列非空时至少保留 1 个 worker 消费可复用用例 |
| W5 | 结果归属：每条 Case 的结果只来自它自己那次执行；worker 崩溃或缺结果文件不影响已定型结果 |
| W6 | 报告完备：每条 Case 一条结论；worker 事件（reuse / restart / exit）进报告 |
| W7 | 确定性：同一输入两次运行的派发顺序一致 |

### 7.2 协议

```text
编排：启动 worker(--exec-worker --worker-id N) → 收到就绪
编排 → worker：stdin 一行 JSON（case_id、句柄文件、工作空间、超时、授权结论）
worker：执行 → 写 cases/<case-id>/result.json → 回报"结束"
编排：派发下一条 / 或让该 worker 退出（独占用例结束后强制退出）
```

超时与取消：编排先投递取消 → 宽限期 → 终止进程树；被终止的 worker 不计入复用。

### 7.3 队列与优先级

- 队列分两段：**独占段**（`DedicatedWorker=true`）与**可复用段**。
- 派发顺序：独占段优先；可复用段按 ID 升序（与现有 `sortedKeys` 一致）。
- 保留策略（W4）：独占段非空时，可复用 worker 数不得降到 0。
- 崩溃处理：worker 退出码非 0 或无结果文件 → 当前 Case 记 `error`/`panic`，worker 计一次 restart，
  队列继续。

## 8. 并发额度

### 8.1 三类额度与两个既有约束

| 约束 | 声明来源 | 语义 | 状态 |
|---|---|---|---|
| 全局并发 | `--parallel` | 同时在飞的 worker 上限 | 已实现 |
| 包级额度 | `--package-limit` + `Case.Concurrency.PackageLimit` | 同一 `Case.Package` 同时在跑的上限 | V4 新增 |
| 命名并发池 | `--pool name=limit` + `Case.Concurrency.Pools` | 跨包共享的同一真实约束 | V4 新增 |
| 资源容量 | `ResourceProfile.Capacity` | 同类资源同时占用份数 | 已实现 |
| 独占键 | `ResourceProfile.ExclusiveKeyKind` | 同键用例不得并行 | 已实现 |
| 包级串行 | `serialPackages` + `Case.Parallel=false` | 整包互斥 | 已实现 |

包级额度与命名池不是同一个概念：前者是**一个包自己的上限**（键自动取自 `Package`），后者是**多包共享的一份
容量**（键由用例显式声明）；一条 Case 可同时受两者约束。

### 8.2 API 与 CLI

```go
type ConcurrencyDecl struct {
    PackageLimit int            // 0 表示不声明；同包非零声明必须一致
    Pools        map[string]int // 0 表示参与该池、容量由别处声明
}

type ConcurrencyPolicy struct {
    Parallel     int
    PackageLimit int            // 运行级上限；0 表示不限
    Pools        map[string]int // 运行级池容量
}
```

```text
--parallel N                全局同时在飞上限（默认 8）
--package-limit N           包级上限；0 表示不限（默认 0）
--pool name=limit           命名池容量；可重复；与用例声明不同时按 D-04 处理
```

生效上限：运行级值存在即生效（可放大），报告记录 `declared_limit` 与生效值并 `WARN` 差异；
运行级未给出时用用例声明；两者都没有而用例引用了该池 → 预检失败。

错判规则：同包/同池两个不同的非零声明 → 注册期失败；池名拼错、负数、非整数 → 退出码 2；
单条所需额度超过生效上限 → 预检失败（不重试、不降级）。

### 8.3 调度不变量

- **I1 全局**：`inFlight ≤ parallel`。
- **I2 包**：对每个包 `inFlight(p) ≤ effectivePackageLimit(p)`。
- **I3 池**：对每个池 `Σ(参与该池的在跑 Case) ≤ effectivePoolLimit(k)`。
- **I4 资源与互斥**：同类资源占用 ≤ 容量；独占键至多被一条在跑用例持有。
- **I5 work-conserving**：存在待派发 Case 且"当前占用 + 该 Case"满足 I1–I4 时，同一轮扫描必须派发它；
  但**不要求填满所有槽位**（队列里全是同池用例时只用 3 个槽位是正确的）。
- **I6 无队头阻塞**：被包额度或池容量阻塞的 Case 不得阻塞其后可派发的 Case。
- **I7 不可满足判定**：`inFlight == 0` 且仍有待派发 Case 时，"仍不可派发"只可能来自声明不可满足，
  必须如实记 `error` 并给出原因，不得永久排队、不得改判跳过。
- **I8 计数守恒**：每次 reserve 恰好一次 release，覆盖 `passed/failed/skipped/panic/timeout/error/
  取消/worker 启动失败/无结果文件` 全部路径，含编排进程 panic 恢复路径。
- **I9 报告完备**：每个出现过的 package / pool 都要有条目；被阻塞过的 Case 计入该额度的排队时间与原因。
- **I10 确定性**：派发顺序只由 ID 升序与当前占用决定，不受 map 迭代影响。

### 8.4 8 / 3 / 5 示例

`--parallel 8`，池 `model-provider:vendor-A` 容量 3，队列 20 条（12 条池内 + 8 条池外）：
稳态 3 条池内 + 5 条池外同时在飞；池内第 4 条排队但不挡池外；池内完成一条立刻补一条；若队列只有池内
Case，则最多 3 条在飞、其余 5 个槽位空闲——这是正确结果，不是调度缺陷。

## 9. unit 用例迁移设计

### 9.1 为什么不能"包一层 `go test`"

`testing.TB` 含未导出方法，外部包无法实现；`*testing.T` 只能由 `go test` 内部构造；`testing.M` 的
`testDeps` 是未导出接口。因此"`agentflow-test` 内部调用 `go test`"只是换了 shell 入口，并发仍由
`-p/-parallel` 决定，报告仍需解析文本。D-09 已明确禁用这条路，也不允许任何形式的 `go test` 执行路径。

### 9.2 逐 API 语义映射

| `testing` 语义 | 迁移后 | 必须保留的行为 |
|---|---|---|
| `t.Fatalf` / `t.Fatal` | `c.Fatalf` / `c.Fatal` | 立刻终止本 Case 主体，结论 `failed`，消息保留 |
| `t.Errorf` / `t.Error` | `c.Errorf` / `c.Error` | 记录失败继续执行；结束时 `c.Failed()` 判失败 |
| `t.Skipf` / `t.Skip` | `c.Skipf` / `c.Skip` | 结论 `skipped`，必须带原因 |
| `t.Run(name, fn)` | `c.Run(name, fn)` | 单层顺序子用例，失败传递到父 Case |
| `t.Parallel()` | `Case.Parallel: true` | 语义不等价（R-02）：并发只由调度器决定 |
| `t.Helper()` | `c.Helper()` | 失败定位指向调用方 |
| `t.Cleanup(fn)` | `c.Cleanup(fn)` | LIFO，结论定型前执行，清理失败可改判失败 |
| `t.TempDir()` | `c.TempDir()` | 用例工作空间下的目录，错误必须显式处理 |
| `t.Logf` | `c.Logf` | 写入用例产物，不改变结论 |
| `t.Setenv(` | 不提供（当前 2 处） | 复用 worker 下必须自带恢复语义，逐条单独决策 |
| `testing.Short()` | 不支持 | 与 V3 结论一致 |

### 9.3 17 个 `TestMain` 的生命周期拆分

现状：全部只有 `os.Exit(testkit.Main(m))`，`testkit.Main` 做三件事——把进程 stdout/stderr 重定向到
`test.log`、写 `latest-run.txt`、默认打开 `-test.v`。迁移后：进程级日志由执行器按 Case 写
`cases/<case-id>/{stdout.log,stderr.log}`；运行根目录由 `run-id` 与 `artifact-root` 提供；
逐用例输出已由执行器打印。因此 R9.1 的删除清单是：17 个 `main_test.go`、`testkit.Main`、运行根目录逻辑、
`make test-unit`、`go-unit` Job、`run-go-group-tests.sh`、`go-test-groups.sh` 的 `unit` 分组。

### 9.4 命名与 `Package`

- 用例主体 `func run<原函数名去掉 Test 前缀>`；`Package` 填来源路径 `tests/unit/<pkg>`。
- 命名不再用于对账（D-03 取消），只作为"这条 Case 来自哪条源用例"的可读约定与评审依据。
- 每包迁移清单必须逐条列出：源函数名、迁移后 Case ID、`DedicatedWorker` 标记与理由、跳过原因。

## 10. 目录迁移同步点（R4）

`tests/internal/testkit` → `tests/testkit`：Go 导入路径 121 个文件（`git grep -l` 枚举）、
`docs/testing.md` / `docs/workspace.md` / `tests/README.md`、包注释。

`tests/runner/*` → `tests/*`：`tests/runner/registry/registry.go` 的 21 行空导入、
`cmd/agentflow-test/main.go` 的 registry 空导入；`Case.Package` 值**不改**；注册分组名**不改**（取 ID
第一段）；用例内相对路径（真实 remote 用例的包内文件、testkit 的远程目录常量）逐个核对。

强制规则：`git mv`；一次提交只搬一个顶层目录；搬完 `go build ./...` 必须通过；搬完立刻 `--list` 确认
360 条 / 25 分组不变。

## 11. 逐阶段任务摘要

每个阶段的完整任务、探针与授权见对应 README；下表只列不可省略的硬门禁。

| 阶段 | 硬门禁 |
|---|---|
| R1 | `make check` 通过；引用清零；`--list` 360/25；一次完整 CI 全绿 |
| R2 | I5/I6 自检通过；`--parallel 8` 下 `max_in_flight` 不被压到 3；`report.json` 有额度分节 |
| R3 | 复用计数、独占计数、优先级、崩溃重启、结果归属五类自检通过；unit 试点开销有数据 |
| R4 | `go build`/`vet`/`gofmt` 干净；`--list` 360/25；`tests/runner` 与 `tests/internal` 不存在 |
| R5 | 26/26 `passed`；两包无 `_test.go`；登记迁移清单与开销数据；`unit` kind 与 `cases-unit` Job 落地 |
| R6 | 9 包无 `_test.go`；每包单独跑全绿；`--list` 530 |
| R7 | 3 包无 `_test.go`；每包单独跑全绿；`--list` 843 |
| R8 | 3 包子阶段全绿；`--list` 1738；`DedicatedWorker` 条目与理由齐备 |
| R9 | `git ls-files '*_test.go'` 为空；无 `go-unit` / `make test-unit`；一次完整 CI 全绿；`V4-final.md` 完成 |

## 12. 风险登记

| 编号 | 风险 | 影响 | 处置 |
|---|---|---|---|
| R-01 | 迁移期同时存在两套入口，容易"只改一半"就宣布完成 | 双重维护、口径不一致 | R9 的 `git ls-files '*_test.go'` 为空是终态证明 |
| R-02 | `t.Parallel()` 1411 处语义降级 | 依赖并行时序的用例可能从"偶尔失败"变成"稳定失败"或掩盖竞争 | 逐包复核有 `go func`/`sync.Once`/固定端口的文件，必要时 `DedicatedWorker` |
| R-03 | worker 复用导致进程内状态串味 | 断言在别的用例留下的状态上成立 | W3 重置 + 自检 Case + 逐包登记包级可变状态 |
| R-04 | 用例 panic / `os.Exit` 杀死复用 worker | 同 worker 的排队结果丢失 | 结果按 Case 落盘；崩溃只影响当前 Case；`restarts` 计入报告 |
| R-05 | 额度计数泄漏 | 运行逐渐降速直到完全不派发 | I8 守恒 + 取消/超时/崩溃路径自检 |
| R-06 | 包额度被误用成"变相串行" | 吞吐下降且难以察觉 | 报告给出每额度 `peak`；长期等于 1 要在评审中指出 |
| R-07 | 声明冲突被静默取最小值 | 真实供应商限流被突破 | 注册期/预检期硬失败 |
| R-08 | 目录搬迁导致相对路径失效 | 真实 remote 用例在 CI 才暴露 | R4 同步点清单 + `--list`；R5 之后第一次跑真实分组复核 |
| R-09 | 取消覆盖对账后"丢用例"没有自动证据 | 静默丢断言 | 每包迁移清单（源数量→Case 数量）+ 逐包运行；评审逐条对照 |
| R-10 | 为通过而放宽断言 | 长期质量下降 | 授权边界明确"不允许"；评审只看 diff 是否等价 |
| R-11 | race 与覆盖率仍无门禁 | 并发缺陷只能靠用例暴露 | 保持 V3 §6 的接受状态；恢复需单独立项 |
| R-12 | 独占用例声明被滥用 | 退化回"一 Case 一进程"，R3 收益消失 | 报告 `isolated_cases` 占比；每条声明需理由 |

## 13. 完成定义（V4 收口标准）

1. `tests/` 下不存在 `runner` 或 `internal` 目录；共享辅助在 `tests/testkit`；用例包直接挂在 `tests/` 下。
2. 仓库内不存在任何 `_test.go`；`go test` 不执行任何用例，也不存在"跑 0 条返回 0"的空门禁。
3. `agentflow-test` 是唯一执行入口，包含 unit 面；报告含六种结论与跳过原因。
4. 并发额度三类齐备（全局 / 包级 / 命名池），I1–I10 有自检 Case 覆盖；`report.json` 记录每额度
   `limit` / `declared_limit` / `peak` / `completed` / 排队时间 / 未派发原因。
5. worker 池按 W1–W7 工作：复用、独占声明、优先级、崩溃重启、结果归属都有自检 Case 与报告字段。
6. 迁移可核对：每个包的迁移清单（源数量 → Case 数量、跳过原因、独占理由）在阶段文档里，且逐包运行通过。
7. 8/3/5 场景可复现：池容量 3、全局 8 时池外用例仍能占满剩余 5 个槽位。
8. 静态门禁保持可用且不执行测试：`fmt`、`fmt-check`、`vet`、`build`、`web-*`、`check`。
9. 一次完整 CI 通过，`ui-test` 门禁步骤与分组核验都成功。
10. 文档只写已实现能力；`docs/testing.md`、`docs/workspace.md`、`docs/development.md`、`tests/README.md`、
    `.github/scripts/README.md` 与新结构一致；`.steps/V4-final.md` 完成。
11. 没有为旧目录 / 旧入口 / 旧执行路径保留任何兼容层或转发包。

## 14. 与 V3 的关系

- V3 的其余不变量（资源只由编排进程分配、六种结论、不伪造结果）**全部保持**；只有"一条 Case 一个 worker
  子进程"被 D-10 改写为"一条 Case 独占一次执行 + 可复用 worker"。
- V3 的覆盖对账与其冻结清单已按 D-06 废弃并删除；`V3-final.md` 保留为历史结论，阶段文档已归档。
- V4 不并入 V3 的阶段编号；收口时新增 `.steps/V4-final.md`，结构与 `V3-final.md` 对齐。
