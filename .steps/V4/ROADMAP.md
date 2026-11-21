# AgentFlow V4：测试体系收敛与并发额度总规划

**文档状态：** 主线规划（尚未开始任何阶段）。本文是 V4 的唯一阶段划分来源；R0 的盘点结果与
冻结决策以各阶段 README 为准。
**上游输入：** [V3 收口文档](../V3-final.md)、[V3/R10 遗留治理](../V3/R10/README.md)、
[V3 路线总规划](../V3/ROADMAP.md)（R0–R9 已冻结的历史决策）、[测试规范](../../docs/testing.md)。
**适用范围：** 只重组测试代码位置、测试执行入口与测试并发模型；不改产品行为、不改数据库
Schema、不改 HTTP 接口、不改前端功能。

---

## 1. 目标

V3 已经解决"集成 / E2E / 前端用例只有 `agentflow-test` 一个入口"。V4 解决它留下的三个结构问题：

1. **目录仍然分层两套。** 共享辅助在 `tests/internal/testkit`，用例在 `tests/runner/<kind>/...`，
   多一层 `runner` 中间目录，而 `tests/unit/**` 仍由 `go test` 驱动。目录形状对读者无法自解释。
2. **unit 用例还在 `go test` 里。** 同一条测试体系有两个执行入口、两套并发控制（`-p/-parallel` 与
   `--parallel`）、两套结论统计口径。路径 C 是 V3 的既定结果，V4 把它收口。
3. **并发只有全局上限与资源容量。** 现状无法表达"某个包最多 3 条同时在跑"，也无法表达
   "多个包共享同一个模型供应商，真实供应商限流是跨包约束"。没有命名池时，模型调用只能靠分组
   串行或人工降低 `--parallel`，代价是把不相关的用例一起压慢。

目标终态（一句话）：**一条 Case 模型、一个执行入口、一套并发额度；`go test` 不再执行任何用例。**

## 2. 非目标

- 不恢复 `go test -race`、不新增覆盖率门禁（V3 §6 已登记的已接受回退；恢复需要在 V4 之后单独立项，
  见 §11 R-04）。
- 不改 `internal/testrunner` 的进程模型：**一条 Case 仍在独立 worker 子进程中执行**（V3 不变量 4）。
  批量 worker 只在 §8.5 的测量门槛被突破时才作为**需 owner 批准**的变更提出。
- 不改资源词汇表与作用域语义（`run-shared` / `case-exclusive` 仍由编排进程唯一分配与释放）。
- 不改 `Case` 的既有字段语义（`ID` / `Package` / `LegacyName` / `LegacyID` / `Parallel` / `Resources`）。
- 不引入第三方调度库或并发库；额度只用 Go 标准库实现。
- 不重新生成 `.steps/V3/R0/` 的冻结清单与矩阵（覆盖分母保持不变）。
- 不为尚未发布的旧目录结构保留兼容层：迁移完成后 `tests/runner`、`tests/internal/testkit` 必须
  整体消失，不得两个目录并存同名 package。

## 3. 当前事实基线（HEAD `465b8bb`，只读盘点）

### 3.1 用例面

| 项 | 数量 | 说明 |
|---|---:|---|
| `tests/unit/**` 测试文件 | 154 | 全部是 `_test.go`，没有普通 `.go` 文件 |
| `tests/unit/**` 测试包 | 16 | `agent`、`app`、`app/planningagent`、`app/planninganalysis`、`cigate`、`container`、`domain`、`environment`、`execenv`、`httpapi`、`image`、`lifecycle`、`server`、`source`、`store`、`testkit` |
| `func Test*`（含 `TestMain`） | 1348 | 其中 `TestMain` 16 个，实际测试函数 1332 |
| `func Benchmark*` | 0 | 不需要为基准设计 API |
| `tests/runner/**` 普通 Go 文件 | 124 | **零** `_test.go`：Case 已完全脱离 `go test` |
| 已注册 Case / 分组 | 358 / 25 | `--list` 口径：普通 312（local 183 + e2e 64 + selfcheck 28 + frontend 37）+ real 32 + fault 14 |
| `tests/runner/registry` 空导入 | 22 | 新增用例包只需一行 |
| `tests/internal/testkit` 文件 | 16 | 含 `testkit.Main`（进程级 fd 重定向写 `test.log`） |
| 引用 `tests/internal/testkit` 的 Go 文件 | 120 | `tests/unit/**` 84 个、`tests/runner/**` 34 个，另 2 处是 `internal/testrunner` 的注释引用 |

> 口径来源：HEAD `465b8bb` 相对 R8 收口提交 `172c6a3` **只改文档**（12 个文件，`tests/` 无变更），
> 因此 R8/R9 记录的用例数量与分组数对 HEAD 仍然成立；本节的 `testing` API 计数与包分布是 HEAD 实测。

### 3.2 unit 用例对 `testing` 的依赖（迁移成本基线）

| API | 调用点 | 迁移方式 |
|---|---:|---|
| `t.Fatalf` | 5969 | `c.Fatalf`（终结当前 Case） |
| `t.Fatal` | 844 | `c.Fatal` |
| `t.Parallel()` | 1411 | 迁移为 `Case.Parallel: true`（并发由调度器决定，语义不等价，见 R-02） |
| `t.Helper()` | 387 | `c.Helper()`（已存在） |
| `t.Cleanup(...)` | 180 | `c.Cleanup(...)`（已存在，LIFO，在结论定型前执行） |
| `t.Run(...)` | 158 | `c.Run(...)`（单层顺序子用例，已存在） |
| `t.Errorf` | 98 | `c.Errorf` + 结束时 `c.Failed()` 判失败（语义已具备） |
| `t.Logf` | 6 | `c.Logf` |
| `t.Error` | 1 | `c.Error` 等价写法 |
| `t.Skipf` | 1（`tests/unit/server` 符号链接权限） | `c.Skipf`（带原因） |
| `t.TempDir()` | 1 | `c.TempDir()`（返回 error，需显式处理） |
| `t.Setenv` / `t.Skip` / `t.SkipNow` / `t.Deadline` / `t.Name` / `t.Failed` | 0 | 不需要新增 API |
| `testing.Short()` | 0 | 不支持（沿用 V3 §7 的结论） |
| `testing.M`（`TestMain`） | 16 | 生命周期拆分，见 §8.3 |

结构风险面（已逐个核对，不构成阻塞）：

- `sync.Once` 3 处，全部在测试内部的 fixture 结构体里（`tests/unit/app/planning_runtime_fixture_test.go`、
  `tests/unit/app/planningagent/planning_agent_fixture_test.go`、`tests/unit/app/requirement_delete_test.go`），
  没有包级单例。
- `func init()` 0 处；包级 `var` 出现在 10 个文件中，均按包内常量/表使用。
- `go func(` 65 处：并发行为测试，迁移后每条 Case 独占进程，隔离性只增不减。
- `httptest` 121 处、`testkit.New/Dir` 73 处：进程内 HTTP 与工作空间，不需要真实外部依赖。
- `os.Exit` 16 处：全部是 `os.Exit(testkit.Main(m))`，随 `TestMain` 一起删除。

### 3.3 盘点工具的当前口径（纠正 V3 文档的过期结论）

- `internal/caseinventory` 的扫描框架**已经是通用的**：`git ls-files` 枚举全部已跟踪文件，
  只按 `tests/` 前缀或 `_test.go` 后缀纳入，不按 `tests/integration/**`、`tests/e2e/**` 过滤。
- 仍然陈旧的是**阶段分类**：`stageForGo`（`inventory.go`）与 `isIntegrationOrE2E`（`resources.go`）
  按 `tests/integration/`、`tests/e2e/` 前缀判定 R5。这两个目录已在 R5 删除，因此当前 HEAD 上重新
  生成清单会得到"R5 范围 0 条、全部 `keep-go-test`"。
- 结论：冻结清单继续只读使用；**unit 面要进入覆盖对账，必须新增阶段口径（见 §5 D-06），
  而不是重新生成清单；unit 审计阶段与 `--audit-package` 属于 V4-R1/R7 的实现范围。**

### 3.4 已经跑过的门禁（V3 R9 本机 + CI #60）

- V3 R9 本机全量：单元 16 包 1321 通过 / 7 跳过；普通 Case 312 条（311 通过 + 1 跳过）；真实 32 条
  （31 通过 + 1 失败）；覆盖对账退出码 0。
- CI #60（`44895723793`）首次完整运行：3 个 Job 失败（本机集成 1 条、前端 2 条 Playwright、自检
  `pilot/server-shutdown` 缺 `bin/agentflow`）。详见 [V3/R10](../V3/R10/README.md)。
- **V4 的阶段验收不能建立在"CI 已经是绿的"这个假设上**：R10-E1 的完整 CI 通过是 V4 最后一阶段
  完整验收的前置条件（不阻塞 R0–R6 的局部验收）。

## 4. 目标架构

### 4.1 目录终态

```text
tests/
├── doc.go                      测试代码位置的唯一说明
├── config/                     真实外部依赖配置（示例入库，真实配置不入库）
├── testkit/                    <- tests/internal/testkit（共享辅助，无 TestMain）
├── registry/                   <- tests/runner/registry（空导入聚合）
├── concurrencycheck/           <- tests/runner/concurrencycheck
├── e2e/                        <- tests/runner/e2e            （e2e/cli 仍在下一级）
├── frontend/                   <- tests/runner/frontend
├── integration/                <- tests/runner/integration   （local/ ssh/ remote/ 仍在下一级）
├── legacy/                     <- tests/runner/legacy        （R0 清单的 dropped 登记）
├── pilot/                      <- tests/runner/pilot
├── resourcecheck/              <- tests/runner/resourcecheck
├── selfcheck/                  <- tests/runner/selfcheck
└── unit/                       unit 用例来源包（目录保留，文件不再由 go test 执行）
    ├── agent/ app/ app/planningagent/ app/planninganalysis/ cigate/ container/
    ├── domain/ environment/ execenv/ httpapi/ image/ lifecycle/ server/ source/
    ├── store/ testkit/
    └── ...
```

三条命名规则（V4 冻结）：

- `tests/testkit` 是共享辅助；`tests/unit/testkit` 是**一个 unit 用例包**（测的正是 testkit 自身），
  两者不得合并、不得互称。
- 用例包一律直接挂在 `tests/` 下，不再有 `runner` 这一层；kind 与目录不再由 `runner` 表达。
- `tests/unit/**` 保留目录（来源包路径不变，覆盖对账的分母路径不变），但它不再是 `go test` 的范围。

### 4.2 执行入口终态

```text
agentflow-test（编排进程）
├── 选择器 + 真实依赖授权判定
├── 调度器（唯一并发决策者）
│   ├── 全局 --parallel
│   ├── 包级额度（--package-limit / Case.PackageLimit）
│   ├── 命名并发池（--pool name=limit / Case.Concurrency.Pools）
│   ├── 资源容量（CapacityOf）
│   └── 独占键（ExclusiveKeyOf）
├── 资源管理器（唯一分配与释放者）
├── 统一报告 + 产物目录（含额度报告）
└── worker 子进程（同一二进制 --exec-case <id>，每 Case 一个）
```

`go test` 在终态只保留一个用途：作为**编译与静态检查**（`go build` / `go vet`），不执行任何用例。

## 5. 冻结决策（D-01 … D-10）

| 编号 | 决策 | 内容 | 状态 |
|---|---|---|---|
| D-01 | 目录映射 | §4.1 的映射是唯一方案；迁移完成后 `tests/runner/` 与 `tests/internal/` 必须整体消失，禁止两个目录并存同名 package | 待 R0 定稿 |
| D-02 | unit 严格终态 | `tests/unit/**` 的用例改为普通 Go 包 + `agentflow-test` 注册；`make test-unit`、CI `go-unit`、`go-test-groups.sh` 的 `unit` 分组、16 个 `TestMain`、`testkit.Main` 全部删除；**不得**留下"`go test` 跑 0 条测试但退出码 0"的空转门禁 | 待 R0 定稿 |
| D-03 | legacy identity | 迁移后的 Case 继续用 `Package` + `LegacyName` 对上 R0 清单：`Package` 保持来源路径 `tests/unit/<pkg>`，用例主体命名为 `func run<原函数名去掉 Test 前缀>`（V3 R5 冻结写法） | 待 R0 定稿 |
| D-04 | 额度声明位置 | 用例声明是权威来源；运行级 `--package-limit` / `--pool` 只能收紧（大于声明值时取声明值并 `WARN`，不报错）；池名拼写错误是参数错误；**同一 key 在用例之间的不同声明必须在注册/预检期报错**，不取最小值、不静默 | 待 R0 定稿 |
| D-05 | work-conserving | 包额度与池额度都不得造成空闲槽位或队头阻塞（不变量 I5–I6，见 §7.3） | 待 R0 定稿 |
| D-06 | 覆盖对账口径 | 不重新生成 R0 清单；新增"unit 面"审计阶段（`--audit-stage` 的新取值映射到清单里 `keep-go-test` 的条目），并新增按包过滤（`--audit-package`）让每个迁移批次可独立作为门禁 | 待 R0 定稿 |
| D-07 | 报告 schema | 额度报告进入 `report.json`，`schema_version` 升到 `agentflow-test-report/2`；旧版本不保留兼容读路径（项目未发布，见 `AGENTS.md` 兼容性政策） | 待 R0 定稿 |
| D-08 | 报告字段 | 每个 package/pool 记录 `limit` / `peak` / `completed` / `queued_ms_total` / `queued_ms_max` / `undispatched`，并记录未能派发原因分类 | 待 R0 定稿 |
| D-09 | `go test -c` 过渡 adapter | **默认不使用。** 只有在分批迁移期间 owner 明确批准时才能引入，且必须写明删除条件（V4-R6 完成即删除，R6 之后不得存在任何 `go test` 执行路径） | 待 owner 决定 |
| D-10 | worker 模型不变 | 一条 Case 一个 worker 子进程；只有在 R3 试点测出 worker 启动开销超过冻结阈值时，才允许提出批量 worker 变更，并需要 owner 批准 | 待 R0 定稿 |

## 6. 阶段总览与依赖

| 阶段 | 状态 | 目标 | 文档 |
|---|---|---|---|
| R0 | 未开始 | 现状盘点、冻结口径、决策定稿、审计工具盘点 | [R0](R0/README.md) |
| R1 | 未开始 | 并发额度内核：包级上限 + 命名池 + 报告 + 自检 Case | R1/README.md（R0 之后创建） |
| R2 | 未开始 | 目录收敛：`tests/testkit` 与 `tests/*`，全量同步点一次改完 | R2/README.md |
| R3 | 未开始 | unit 迁移试点：`execenv`（21）+ `lifecycle`（7）共 28 条，验证成本与语义 | R3/README.md |
| R4 | 未开始 | unit 批量迁移 A：11 个中小包共 422 条（R4.1 小包 106 + R4.2 `domain`/`container`/`agent` 316） | R4/README.md |
| R5 | 未开始 | unit 批量迁移 B：`app`（407）+ `httpapi`（242）+ `store`（249）共 898 条 | R5/README.md |
| R6 | 未开始 | 删除 `go test` 执行路径、`TestMain`、`testkit.Main`、`make test-unit`、`go-unit` Job | R6/README.md |
| R7 | 未开始 | unit 面覆盖对账与 legacy identity 全量核验 | R7/README.md |
| R8 | 未开始 | 门禁 / CI / 脚本 / 文档收口，全量验收，产出 V4 收口文档 | R8/README.md |

```text
R0 ──> R1 ──> R2 ──> R3 ──> R4 ──> R5 ──> R6 ──> R7 ──> R8
                        │                              ▲
                        └── 每批迁移都必须 --audit-package 通过 ┘
```

硬依赖与理由：

- **R1 先于 R2。** R1 要给 `Case` 增加并发声明字段并改 `tests/runner/**/case.go` 的注册辅助；
  R2 要整体移动这些文件。先加字段再搬家，避免一次改动同时改语义与路径（不可评审）。
- **R2 先于 R3。** unit 迁移会新增十几个 `tests/unit/<pkg>` 注册点；目录未定稿就开工会导致二次搬迁。
- **R3 先于 R4/R5。** 试点必须先证明：迁移写法成立、worker 启动开销可接受、覆盖对账能按包通过。
  试点不通过不得进入批量迁移。
- **R6 必须在 R5 之后。** 只要还有一个包是 `_test.go`，删除 `make test-unit` 就会丢失覆盖。
- **R7 在 R6 之后、R8 之前。** R6 删掉 `go test` 路径后，覆盖对账才有"全部 unit 条目已覆盖"的
  完整分母；R8 的完整验收要求 R7 退出码 0。
- **R8 的完整 CI 验收依赖 R10-E1。** 若 CI 基线仍是失败状态，新失败无法归因；R0–R6 的局部验收
  不依赖这一点。

### 6.0 每阶段共同的执行纪律

1. 阶段文档里的决策必须开工时定稿并写回文档，不得边做边改接口。
2. 一个阶段只做本阶段的事：R2 只搬目录，不改任何用例语义；R3/R4/R5 只改用例，不搬目录。
3. 每次迁移一个包就是一次可独立回滚的提交：`git diff` 里不得同时出现"移动"与"语义改写"。
4. 迁移不得静默丢断言：每个包迁移完成后立刻跑该包的 `--audit-package` 与既有用例，缺失即失败。
5. 未获授权不运行测试；允许的静态动作只有 `gofmt`、`go vet`、`go build`、`git diff`、`--list`、
   `--audit-coverage`（只读清单与注册表，不执行用例）。

## 7. 并发额度设计

### 7.1 三类额度与两个既有约束的分工

| 约束 | 声明来源 | 语义 | 现状 |
|---|---|---|---|
| 全局并发 | `--parallel`（默认 8） | 同时在飞的 worker 上限 | 已实现 |
| **包级额度** | `--package-limit` + `Case.Concurrency.PackageLimit` | 同一 `Case.Package` 同时在跑的上限 | **V4 新增** |
| **命名并发池** | `--pool name=limit` + `Case.Concurrency.Pools` | 跨包共享的同一真实约束（例如同一模型供应商） | **V4 新增** |
| 资源容量 | `ResourceProfile.Capacity` | 同类资源同时占用份数（`sqlite-db` 4、`browser` 4 等） | 已实现 |
| 独占键 | `ResourceProfile.ExclusiveKeyKind` | 同键用例不得并行（镜像标签、远程目录等） | 已实现 |
| 包级串行 | `serialPackages` + `Case.Parallel=false` | 整包互斥 | 已实现 |

**包级额度与命名池不是同一个概念**：

- 包级额度是**一个包自己的并发上限**，键由 `Case.Package` 自动得到，不需要额外声明；
- 命名池是**多个包共享的一份容量**，键由用例显式声明，同一池的用例可以来自任意包；
- 一个 Case 可同时受两者约束（例如 `tests/unit/app` 的某条用例既受 `app` 包额度限制，又参与
  `model-provider:vendor-A` 池）。

### 7.2 API 草案（需在 R1 冻结）

```go
// ConcurrencyDecl 是一条用例的并发额度声明。
//
// 零值表示"没有任何额外约束"：包额度跟随运行级 --package-limit，不参与任何命名池。
type ConcurrencyDecl struct {
	// PackageLimit 是该用例所在包的并发上限；0 表示跟随运行级 PackageLimit。
	// 同一个 Package 的全部非零声明必须相等，否则注册失败。
	PackageLimit int
	// Pools 是参与的命名池 → 该池容量。
	// 值为 0 表示"参与该池，容量由别处（同一池的其它用例或运行级 --pool）声明"。
	Pools map[string]int
}

type Case struct {
	// ... 既有字段不变
	// Concurrency 是该用例的并发额度声明（V4 R1 新增）。
	Concurrency ConcurrencyDecl
}

// ConcurrencyPolicy 是一次运行的额度策略：用例声明解析后的生效上限。
type ConcurrencyPolicy struct {
	Parallel     int
	PackageLimit int            // 运行级默认包上限，0 表示不限
	Pools        map[string]int // 命名池 → 容量
}
```

CLI：

```text
--parallel N                全局同时在飞上限（默认 8）
--package-limit N           包级默认上限；0 表示不限（默认 0）
--pool name=limit           命名池容量；可重复；大于用例声明时以声明为准并 WARN
```

校验规则（注册期 + 预检期，两类都要有明确错误码与信息）：

| 场景 | 时机 | 结果 |
|---|---|---|
| 同一 `Package` 出现两个不同的非零 `PackageLimit` | 注册期（`Cases()` 汇总注册错误） | 失败：`额度声明冲突：包 <pkg> 同时声明 3 与 5` |
| 同一 pool 出现两个不同的非零容量 | 注册期 | 失败：`额度声明冲突：池 <name> 同时声明 3 与 4` |
| 用例引用某 pool，但该 pool 既无声明也无 `--pool` | 预检期（`Run` 开始前） | 失败：`池 <name> 没有容量声明`（不做隐式 1） |
| 运行级 `--pool` 的值大于用例声明的容量 | 预检期 | 取声明值（运行级不能放宽真实约束），并输出 `WARN` 说明运行级值被忽略；不报错、不静默改小声明 |
| 运行级 `--package-limit` 大于某包的 `Case.PackageLimit` | 预检期 | 同上：该包取用例声明值并 `WARN`，其余包仍用运行级值 |
| `--pool` 指定的池名在注册表里不存在 | 预检期 | 参数错误（拼写错误必须暴露，不得静默创建空池） |
| `--package-limit` / `--pool` 出现负数或 0 以外的非整数 | 参数解析期 | 退出码 2（用法错误） |
| 某用例独占需求（单条所需额度）超过生效上限 | 预检期 | 失败，而不是让它永久排队 |

### 7.3 调度不变量

调度器（`internal/testrunner/scheduler.go`）必须始终满足：

- **I1 全局**：`inFlight ≤ parallel`。
- **I2 包**：对每个包 `p`：`inFlight(p) ≤ effectivePackageLimit(p)`，其中
  `effectivePackageLimit(p) = min(--package-limit, Case.PackageLimit)`（两者都为 0 时视为不限）。
- **I3 池**：对每个池 `k`：`Σ_{在跑且参与 k 的 Case} 1 ≤ limit(k)`。
- **I4 资源与互斥**：同类资源占用 ≤ 容量；独占键至多被一条在跑用例持有。
- **I5 work-conserving（无闲置）**：若存在待派发 Case，且"当前占用 + 该 Case"满足 I1–I4，
  且 `inFlight < parallel`，则派发扫描必须在同一轮内派发它。**语义边界**：本不变量约束的是
  "可派发却不派发"，不是"必须填满所有槽位"——队列里全是同一池的用例时，8 个槽位只用 3 个是正确的。
- **I6 无队头阻塞**：某条 Case 因包额度或池容量被阻塞时，不得阻塞其后仍可派发的 Case；
  调度器必须跳过它继续扫描。
- **I7 不可满足的判定**：`inFlight == 0` 且仍有待派发 Case 时，任何额度都不处于占满状态，
  因此"仍不可派发"只可能来自声明本身不可满足（例如单条需求超过容量、独占键自冲突），
  此时必须如实记 `error` 并给出原因，不得让它永久排队，也不得改判为跳过。
- **I8 计数守恒**：每次 `reserve` 都必须有且只有一次 `release`；release 必须覆盖
  `passed / failed / skipped / panic / timeout / error / 取消 / worker 启动失败 / 无结果文件`
  全部路径，并且在编排进程侧 panic 恢复路径上同样执行（对齐现有 `startCase` 的 `recover`）。
- **I9 报告完备**：每个出现过的 package/pool 都要有额度报告条目；被某额度阻塞过的 Case 要计入
  该额度的排队时间与"未能派发原因"。
- **I10 确定性**：派发顺序只由 ID 升序与当前占用决定，不受 map 迭代顺序影响（沿用现有
  `sortedKeys` 写法）；同一输入两次运行的派发序列必须一致。

### 7.4 8 / 3 / 5 示例的精确语义

参数：`--parallel 8`，池 `model-provider:vendor-A` 容量 3。

- 队列里有 20 条 Case，其中 12 条属于该池（真实调用模型），8 条不参与该池（例如纯领域用例）。
- 稳态可以同时有：3 条池内 Case + 5 条池外 Case = 8 条在飞。
- 池内第 4 条 Case 处于**排队**状态，但**不得**阻塞池外第 6 条 Case 的派发（I5/I6）。
- 池内 Case 完成一个，立刻补派下一条池内 Case（池容量从 3 释放到 2 → 又回到 3）。
- 若队列里只有池内 Case（12 条，无池外 Case），则最多 3 条在飞，剩余 5 个全局槽位空闲 —— 这是
  work-conserving 的正确结果，不是调度缺陷，也不得为了让 `max_in_flight` 好看而突破池容量。
- 报告里应能读出：`parallel_limit=8`、`pool[model-provider:vendor-A].limit=3`、
  `pool[…].peak=3`、`max_in_flight=8`（有池外 Case 时）、两条池内 Case 的排队时长。

### 7.5 实现落点

| 文件 | 改动 |
|---|---|
| `internal/testrunner/limit.go`（新增） | `ConcurrencyDecl`、`ConcurrencyPolicy`、生效上限解析、声明冲突校验（纯函数 + 纯结构，无 goroutine） |
| `internal/testrunner/case.go` | `Case` 增加 `Concurrency` 字段；`Validate` 校验非负与池名合法性（池名沿用 ID 的字符集约束） |
| `internal/testrunner/scheduler.go` | `caseRequirement` 增加 `pkg` / `pkgLimit` / `pools`；`scheduleState` 增加 `pkgInFlight`、`poolInFlight`；`admits`/`fits`/`reserve`/`release` 扩展；`reservation` 记录本次占用的 package 与 pools |
| `internal/testrunner/orchestrator.go` | `RunOptions` 增加 `Policy ConcurrencyPolicy`；`Run` 预检（I7 与 §7.2 的预检错误） |
| `internal/testrunner/report.go` | `ConcurrencyReport` 增加 `package_limit`、`quotas []QuotaReport`；`ReportSchemaVersion` 升版（D-07） |
| `cmd/agentflow-test/main.go` | 新增 `--package-limit`、`--pool`（可重复），解析与错误信息 |
| `.github/scripts/run-cases.sh` | 透传 `--package-limit` / `--pool`（可选参数，不引入分组知识） |
| `Makefile` | `PACKAGE_LIMIT` / `POOLS` 变量透传，CI 与本地共用同一入口 |
| `tests/runner/concurrencycheck/`（R2 之后为 `tests/concurrencycheck/`） | 新增自检 Case（见 R1 验收） |

## 8. unit 用例迁移设计

### 8.1 为什么不能"包一层 `go test`"

Go 的硬约束（V3 P0-1 已记录）：`testing.TB` 含未导出方法，外部包无法实现；`*testing.T` 只能由
`go test` 框架内部构造；`testing.M` 的 `testDeps` 是未导出接口。因此：

- **不允许**把"`agentflow-test` 内部调用 `go test`"包装成"unit 已经迁移"。那只是换了一个 shell 入口，
  并发仍由 `-p/-parallel` 决定，报告仍要从文本里解析。
- **严格终态**（默认规划）是：`tests/unit/**` 下不再有 `_test.go`，用例主体是接受
  `*testrunner.Context` 的普通函数，由 `agentflow-test` 直接驱动。
- 过渡方案（D-09，需 owner 批准）是 `go test -c` 编出测试二进制 + 适配器调用；它**必须**写明
  "R6 完成即删除"，且不得在 R6 之后的任何文档里被描述为终态。

### 8.2 逐 API 语义映射

| `testing` 语义 | 迁移后 | 必须保留的行为 |
|---|---|---|
| `t.Fatalf` / `t.Fatal` | `c.Fatalf` / `c.Fatal` | 立刻终止本 Case 主体，后续语句不执行；结论 `failed`，消息保留 |
| `t.Errorf` / `t.Error` | `c.Errorf` / `c.Error` | 记录失败并继续执行；Case 结束时因 `c.Failed()` 判 `failed`（现有 `runCaseBody` 已实现） |
| `t.Skipf` / `t.Skip` | `c.Skipf` / `c.Skip` | 结论 `skipped`，必须带原因；不得计为通过 |
| `t.Run(name, fn)` | `c.Run(name, fn)` | 单层顺序子用例；子用例失败要传递到父 Case（现有实现） |
| `t.Parallel()` | `Case.Parallel: true` | **语义不等价**（见 R-02）：并发只由调度器决定，同包内不再有 `t.Parallel` 的父子同步语义 |
| `t.Helper()` | `c.Helper()` | 失败定位指向调用方（保持可读的失败行号） |
| `t.Cleanup(fn)` | `c.Cleanup(fn)` | LIFO；在结论定型前执行；清理失败必须能把 Case 改判为失败 |
| `t.TempDir()` | `c.TempDir()` | 返回用例工作空间下的目录；错误必须显式处理（不得 `_` 丢弃） |
| `t.Logf` | `c.Logf` | 写入用例产物；不改变结论 |
| `t.Setenv` | 不提供（当前 0 处） | worker 进程独占，如未来需要必须自带恢复语义并单独决策 |
| `testing.Short()` | 不支持 | 与 V3 §7 一致 |
| `TestMain` | 见 §8.3 | 运行级职责改由执行器承担 |
| `testing.M` | 删除 | `testkit.Main` 一并删除 |

### 8.3 16 个 `TestMain` 的生命周期拆分

现状（已逐个核对）：16 个 `TestMain` 全部只有 `os.Exit(testkit.Main(m))`，没有其它初始化逻辑。
`testkit.Main` 做三件事：把进程 stdout/stderr 重定向到 `<run>/test.log`、写 `latest-run.txt`、
默认打开 `-test.v`。

迁移后职责归属：

| 原职责 | 新归属 |
|---|---|
| 进程级 `test.log` | 执行器已按 Case 写 `cases/<case-id>/{stdout.log,stderr.log}`，**删除** fd 重定向 |
| `latest-run.txt` / 运行根目录 | 执行器的 `run-id` 与 `artifact-root` 已提供，**删除** |
| `-test.v` 默认开启 | 执行器输出已逐用例打印，**不需要** |
| 包级初始化（当前为空） | 若某包确实需要，改成用例内的显式 setup，或在 `Case.Run` 开头做（不得引入包级单例） |

因此 R6 的删除清单是：16 个 `main_test.go`、`testkit.Main`、`testkit` 中仅为 `TestMain` 服务的
运行根目录逻辑、`make test-unit`、CI `go-unit` Job、`run-go-group-tests.sh`、`go-test-groups.sh` 的
`unit` 分组与包模式校验。

### 8.4 legacy identity 与覆盖对账

- 每个迁移函数保留 `func run<原函数名去掉 Test 前缀>` 的命名，`Register` 会自动推出 `LegacyName`，
  与 V3 R5 的写法一致（`internal/testrunner/case.go` 的 `legacyNameOf`）。
- `Case.Package` 填**来源包路径** `tests/unit/<pkg>`（与目录一致，因此不需要别名），
  `--audit-coverage` 用 `Package + LegacyName` 匹配 R0 清单（`internal/testrunner/coverage.go`
  的 `auditKey`）。
- 清单条目的 `stage` 是 `keep-go-test`。要把它纳入门禁而不重新生成清单，需要：
  - 新增审计阶段取值（例如 `V4`）在代码里映射到 `keep-go-test` 条目；
  - 新增按包过滤（`--audit-package tests/unit/<pkg>`，可重复），使每个迁移批次能独立判定
    "本包已全覆盖、缺失 0"；
  - 最终 R7 用不带包过滤的全量 unit 审计作为门禁。
- **不允许**为了让审计变绿而把清单条目改成 `dropped`：`dropped` 只用于"显式放弃某个用例"，
  必须逐条给理由，且由 owner 决定。

### 8.5 成本估算与分包顺序

机械改写面（HEAD 实测）：1332 个测试函数、6813 处 `Fatal/Fatalf`、98 处 `Errorf`、387 处 `Helper`、
194 处 `Run`（其中 `t.Run` 158）、180 处 `Cleanup`、1411 处 `t.Parallel`、1 处 `TempDir`、
1 处 `Skipf`。按包分布：

| 批次 | 包 | 测试函数 | 说明 |
|---|---|---:|---|
| R3 试点 | `execenv`（21）、`lifecycle`（7） | 28 | 纯函数级、无外部进程、`lifecycle` 有并发启停语义，可验证 `Cleanup` 与失败传播 |
| R4.1 批量 A1 | `testkit`（2）、`cigate`（10）、`app/planningagent`（10）、`image`（11）、`environment`（12）、`server`（14）、`source`（22）、`app/planninganalysis`（25） | 106 | 规模可控，覆盖"有 httptest / 有子进程 / 有 fixture"三类形态 |
| R4.2 批量 A2 | `domain`（250）、`container`（30）、`agent`（36） | 316 | `domain` 是纯领域规则、最适合大批量验证写法；`container` / `agent` 有外部进程替身，用来验证 `Cleanup` 与子进程收尾 |
| R5 批量 B | `store`（249）、`httpapi`（242）、`app`（407） | 898 | 大包，一个包一个子阶段（R5.1 / R5.2 / R5.3），每个子阶段单独验收 |

数量口径说明：包内数字是 `func Test*` 计数（**含**该包的 1 个 `TestMain`），因此四批合计
28 + 106 + 316 + 898 = 1348 = 全部 `func Test*`；减去 16 个 `TestMain` 后实际用例主体是 1332 条。
这不是"改完就一劳永逸"的上限——迁移过程中若发现一个 `t.Run` 子测试需要独立成 Case，数量会上升；
不得为了数量好看而把子测试合并成一条 Case（那会丢失 legacy identity）。

R3 必须测出的两个数字（写回 R3 文档）：

1. 迁移后该批 Case 的 worker 启动开销与总时长，与 `go test` 基线对比；
2. 单条 Case 的失败信息可读性（是否还能看到具体断言行）。

**门槛（D-10）**：若固定开销导致 unit 全量运行时长超过迁移前基线的 2 倍，允许提出"批量 worker"
变更；否则保持一条 Case 一个 worker。

## 9. 目录迁移的同步点清单（R2 交付）

`tests/internal/testkit` → `tests/testkit`：

| 同步点 | 数量/位置 |
|---|---|
| Go 导入路径 | 120 处（`tests/unit/**` 84 个文件、`tests/runner/**` 34 个文件、`internal/testrunner` 2 处注释）；由 `goimports`/`gofmt` 之外的人工替换完成，替换后必须 `go build ./...` |
| 文档路径 | `docs/testing.md`（§2/§7/§8 三处）、`docs/workspace.md`（依赖方向表）、`tests/README.md`（目录树与 known_hosts 段落）、`.steps/V3-final.md`（只追加勘误，不改历史叙述） |
| 包注释 | `testkit` 内引用自身路径的注释 |
| `.steps` 历史文档 | **不修改**（历史记录原样保留） |

`tests/runner/*` → `tests/*`：

| 同步点 | 数量/位置 |
|---|---|
| Go 导入路径 | `tests/runner/registry/registry.go` 的 22 行空导入；`cmd/agentflow-test/main.go` 的 1 行 registry 空导入；包内互相引用（如有） |
| `Case.Package` 值 | **不改**：它保留来源路径（`tests/integration/...`、`tests/e2e/...`），是覆盖对账的分母，与文件位置无关 |
| 注册顺序与 group 名 | 不改：group 取 `ID` 第一段，与目录无关 |
| 用例内部的文件查找 | `tests/runner/**` 中按相对路径读固定文件的地方（例如真实 remote 用例读取包内 `Dockerfile`、`testkit` 的远程目录常量）；搬目录后必须逐个核对 |
| 产物路径 | `ArtifactPaths` 由 `run-id` 与 `case-id` 组成，不含源码路径；仅需核对用例内自建的相对路径 |
| 覆盖对账 | `internal/testrunner/coverage.go` 不含目录字面量，无需改；但 `--audit-*` 的说明文字要改 |
| `internal/caseinventory` | 无需改（通用扫描）；阶段映射问题另立项（§3.3） |
| 分组脚本 | `.github/scripts/go-test-groups.sh` 不含目录路径（只含分组名），无需改；`--check` 的注释文字要改 |
| `Makefile` | 只引用 `./tests/unit/...`（R2 不改，R6 删除）；`inventory` 目标不变 |
| CI | `.github/workflows/ci.yml` 不含测试目录路径，无需改 |
| 前端 | `lingting-agent-flow-ui` 侧不引用 `tests/runner`；`tests/runner/frontend/case.go` 的 `uiProjectDir` 是相对仓库根的路径，搬目录后仍需正确解析（必须验证） |

搬目录的强制规则：

- 用 `git mv` 而不是"复制 + 删除"，保证 Git 能把移动识别为重命名（评审与 `git blame` 可读）。
- 一次提交只搬一个顶层目录（`testkit`、`registry`、`concurrencycheck`、…），每个提交都必须
  `go build ./...` 通过。
- 搬完后 `tests/runner` 与 `tests/internal` 必须为空目录并删除；**禁止**保留 `runner` 转发包。
- 搬完后立刻跑 `--list`（不执行用例）确认 358 条 Case 与 25 个分组数量不变，且
  `--audit-coverage --audit-stage R5,R6` 仍退出码 0。

## 10. 逐阶段任务、范围与验收

### R0：现状盘点与冻结（文档产出为主）

- 范围：`.steps/V4/R0/README.md`、`.steps/V4/ROADMAP.md`（§3/§5/§6 定稿）；
  可把 `make inventory` 的输出写进被忽略路径 `.test-artifacts/v4-roadmap-inventory/`。
- 任务：重跑盘点（只读）、核对 §3 的数字、冻结 D-01…D-10、列出每个包的迁移清单与负责人。
- 验收：本文 §3 的数字与当前 HEAD 一致；D-01…D-10 全部有"建议 + 结论 + 决策人"；
  `--list` 与 `--audit-coverage` 的当前结论被记录为基线。
- 授权：只读盘点，不执行任何用例。

### R1：并发额度内核

- 范围：`internal/testrunner/{limit.go,case.go,scheduler.go,orchestrator.go,report.go}`、
  `cmd/agentflow-test/main.go`、`.github/scripts/run-cases.sh`、`Makefile`、
  `tests/runner/concurrencycheck/`（自检 Case，R2 之后随目录改名）、`docs/testing.md` 的调度章节
  （R8 统一收口时再写也可，
  但代码与用例必须在 R1 完成）。
- 任务：按 §7 实现额度；注册期与预检期校验；报告字段；CLI 参数。
- 验收（需授权运行）：
  1. 新增自检 Case 覆盖：包额度生效、跨包池生效、I5 无闲置（池内 3 + 池外 5 同时在飞）、
     I6 无队头阻塞、声明冲突在预检期失败、`--pool` 放大被拒、release 幂等（含 panic / 超时 /
     取消 / 无结果四条路径）。
  2. 现有 `concurrencycheck`（17 条）全部仍通过，`--parallel 8` 下 `max_in_flight` 不被压到 3。
  3. `report.json` 的额度分节可读，`schema_version` 已升版。
- 风险：额度计数泄漏会让运行"越来越慢"直到完全不派发 —— 必须用 I8 的守恒自检守住。

### R2：目录收敛

- 范围：`tests/**`（纯移动）、`tests/testkit` 的包注释、`tests/registry/registry.go`、
  `cmd/agentflow-test/main.go`、`docs/testing.md`、`docs/workspace.md`、`tests/README.md`。
- 任务：按 §9 的同步点清单一次改完；`git mv` 分提交。
- 验收（静态为主 + 一次只读核对）：
  1. `go build ./...`、`go vet ./...`、`gofmt -l` 干净；
  2. `./bin/agentflow-test --list` 仍是 25 个分组 / 358 条 Case；
  3. `--audit-coverage --audit-stage R5,R6` 退出码 0（316 覆盖 + 49 dropped、缺失 0、多余 0）；
  4. `tests/runner`、`tests/internal` 不存在；仓库内没有残留的旧导入路径
     （`git grep 'tests/runner\|tests/internal'` 只剩 `.steps/` 下的历史文档）；
  5. 文档中的目录树与新结构一致。
- 授权：只读核对，不执行用例；若需执行，只授权一次 `--list` 与一次覆盖对账。

### R3：unit 迁移试点（28 条）

- 范围：`tests/unit/execenv/**`、`tests/unit/lifecycle/**`、`tests/registry/registry.go`（两行）、
  `internal/testrunner/coverage.go`（新增 unit 审计阶段与 `--audit-package`，若 R0 未提前做）。
- 任务：按 §8.2/§8.3 迁移；删掉这两个包的 `main_test.go`；写 `cases.go` 聚合注册。
- 验收：
  1. 两个包不再有 `_test.go`，`go test ./tests/unit/execenv/... ./tests/unit/lifecycle/...` 不再
     执行任何用例（必须显式核对：不是"跑 0 条返回 0"就当成功——该结果在 R6 会被删除）；
  2. `--audit-coverage --audit-package tests/unit/execenv --audit-package tests/unit/lifecycle`
     退出码 0（28/28 覆盖、缺失 0）；
  3. 28 条 Case 全绿；失败信息能定位到具体断言；
  4. 记录 worker 启动开销与总时长，与 `go test` 基线对比（写回 R3 文档）。
- 授权：需要显式授权运行这两个包的迁移用例与一次覆盖对账。

### R4：unit 批量迁移 A（422 条，11 个包，两个子批次）

- 范围：§8.5 的 R4.1（8 个小包 106 条）与 R4.2（`domain` 250 + `container` 30 + `agent` 36 = 316 条），
  一个包一次提交。
- 任务：同 R3 的写法；`tests/unit/testkit` 包（2 条）要先确认它与 `tests/testkit` 的命名边界
  （前者是用例、后者是共享辅助，不得合并）。
- 验收：每个包迁移完成后 `--audit-package` 覆盖 100%、缺失 0；该包 Case 全绿；
  全部 11 个包完成后，`git ls-files 'tests/unit/*_test.go'` 的剩余文件只来自
  `app`（49 个文件）、`httpapi`（25 个）、`store`（26 个）。
- 授权：按包分批授权运行（一次一个包或两个包），避免一次授权变成"全量跑"。

### R5：unit 批量迁移 B（898 条，3 个大包）

- 子阶段：R5.1 `store`（249）、R5.2 `httpapi`（242）、R5.3 `app`（407）。
- 每个子阶段一个独立验收：`--audit-package` 100%、用例全绿、worker 开销与时长记录。
- 风险：`app` 有 65 处 `go func` 与多处时序断言（并发启停、恢复、迟到结果），迁移时必须逐条
  确认"进程隔离是否改变了断言的成立条件"（V3 R5 的 D13 类问题）。
- 授权：每个子阶段单独授权。

### R6：删除 `go test` 执行路径

- 范围：16 个 `main_test.go`（应已随包迁移删除，此处核对）、`tests/testkit/**`（删除
  `Main`、运行根目录逻辑）、`.github/scripts/run-go-group-tests.sh`（删除）、
  `.github/scripts/go-test-groups.sh`（`go_test_groups` / `group_packages` / `group_make_target`
  改为"没有 go test 分组"并调整 `--check` 的 `--declared-groups` 必填语义）、`Makefile`
  （删除 `test-unit`，`test` 目标改为只跑 Case）、`.github/workflows/ci.yml`（删除 `go-unit` Job
  与 `ci-summary` 对它的依赖）、`docs/testing.md`、`docs/development.md`、`tests/README.md`。
- 验收（静态）：
  1. `git ls-files '*_test.go'` 为空（仓库不再有任何 Go 测试文件）；
  2. `go test ./...` 不执行任何用例（必须能在文档里说明：这不是门禁，而是"已无测试文件"的必然结果）；
  3. `make test-unit` 不存在；CI 没有 `go-unit` Job；`go-test-groups.sh --check` 在
     "无 go test 分组"的新语义下通过；
  4. `make check` 仍只做 fmt / vet / build；
  5. 一次 `--list` 与一次 `--audit-coverage --audit-stage R5,R6` 退出码 0（确认删除动作没有
     影响已注册 Case）。
- 授权：静态检查为主；如需运行，只授权 `--list`、覆盖对账与一次 `make test-cases`。

### R7：unit 面覆盖对账与 legacy identity

- 范围：`internal/testrunner/coverage.go`（若 R3 已实现则只核对）、`.steps/V4/R7` 文档。
- 验收：
  1. `--audit-coverage --audit-stage V4`（unit 面）覆盖 1332 条中的全部已迁移条目，缺失 0、
     多余 0、退出码 0；
  2. 每个 unit 包的分组统计与 R0 清单的包分布一致（可用 `--audit-json` 产物对比）；
  3. `dropped` 条目数不增加（V4 不放弃任何 unit 用例）；
  4. 覆盖分母仍是 R0 的 1707 条（未重新生成）。
- 授权：只读审计（不执行用例）。

### R8：门禁 / CI / 文档收口与全量验收

- 范围：`Makefile`、`.github/workflows/ci.yml`、`.github/scripts/*`、`docs/**`、`README.md`、
  `tests/README.md`、`.steps/V4-final.md`。
- 验收（需完整授权）：
  1. `make check` 通过且不执行测试；
  2. `make test-cases`（全部 312 条普通 Case）通过，`residual_check=passed`；
  3. unit 用例（现已是 Case）全部通过，报告里的 `totals` 与迁移前逐包对得上（每条用例都有结论，
     跳过仍只有已登记的那几条并有原因）；
  4. 真实设备分组按授权单独一次运行；
  5. 覆盖对账（R5/R6 与 unit 面）退出码 0；
  6. 一次完整 CI 通过（前置：R10-E1 已完成）；
  7. 文档只描述已落地能力：`docs/testing.md` 不再出现"unit 仍由 go test 执行"、
     `tests/README.md` 的目录树与新结构一致、`docs/workspace.md` 的依赖方向已更新；
  8. `.steps/V4-final.md` 完成，`ROADMAP` 阶段状态全部为已完成。
- 授权：全量 + 真实设备 + CI 推送，三项分别授权。

## 11. 风险登记

| 编号 | 风险 | 影响 | 处置 |
|---|---|---|---|
| R-01 | 迁移期同时存在两套入口，容易"只改一半"就宣布完成 | 双重维护、口径不一致 | 阶段硬门禁：R6 的验收条件 1（仓库无 `_test.go`）是终态证明，不是"看起来差不多了" |
| R-02 | `t.Parallel()` 1411 处语义降级 | 依赖并行时序的用例可能从"偶尔失败"变成"稳定失败"或掩盖竞争 | 已在 V3 P1-4 记录；V4 逐包迁移时对有 `go func`/`sync.Once`/固定端口的文件单独复核，结论写进批次文档 |
| R-03 | worker 启动开销（一条 Case 一个进程） | unit 全量时长可能翻倍 | R3 测量；超过 2 倍才提批量 worker 变更（D-10），并需要 owner 批准 |
| R-04 | race 与覆盖率仍无门禁 | 并发缺陷只能靠用例暴露 | 保持 V3 §6 的接受状态；恢复需要在 V4 之后单独立项（worker `-race` 变体 + `--race`），不在 V4 范围 |
| R-05 | 额度计数泄漏 | 运行逐渐降速直到完全不派发 | I8 守恒 + 自检 Case（panic/timeout/cancel/no result 四条路径） |
| R-06 | 包额度被误用成"变相串行" | 吞吐下降且难以察觉 | 报告必须给出 `max_in_flight` 与每额度 `peak`；`peak` 长期等于 1 要在评审中指出 |
| R-07 | 声明冲突被静默取最小值 | 真实供应商限流被突破 | 注册期/预检期硬失败（§7.2），不提供"取较小值"的降级 |
| R-08 | 目录搬迁导致相对路径失效（用例内读包内文件） | 真实 remote 用例在 CI 上才暴露 | R2 的同步点清单 + 一次 `--list`；R2 之后第一个跑真实分组的阶段必须复核 |
| R-09 | 覆盖对账把 unit 面排除在外 | "全绿"但 unit 断言静默丢失 | §8.4 的 unit 审计阶段 + 每批 `--audit-package` 门禁 |
| R-10 | 为通过而放宽断言 | 长期质量下降 | 阶段文档的授权边界明确"不允许"；评审只看 diff 是否等价 |
| R-11 | CI 基线仍是红的（#60） | 新失败无法归因 | R8 的全量验收依赖 R10-E1；R0–R6 的局部验收不依赖 |
| R-12 | 迁移改写引入 `t.Errorf` → `c.Errorf` 的语义差 | 原本"继续执行到末尾"的用例提前终止 | 逐点核对：`Errorf` 对应 `c.Errorf`（不终止），只有 `Fatalf` 对应终止 |

## 12. 完成定义（V4 收口标准）

1. `tests/` 下不存在 `runner` 或 `internal` 目录；共享辅助在 `tests/testkit`；用例包直接挂在
   `tests/` 下；`tests/unit/**` 只剩普通 Go 文件。
2. 仓库内不存在任何 `_test.go`；`go test` 不再是任何用例的执行入口，也不存在"跑 0 条返回 0"的空门禁。
3. `agentflow-test` 是唯一执行入口，包含 unit 面；报告含 unit 用例的六种结论与跳过原因。
4. 并发额度三类齐备：`--parallel`、包级上限、命名池；全部不变量 I1–I10 有自检 Case 覆盖；
   `report.json` 记录每个额度的 limit / peak / 排队时间 / 完成数 / 未能派发原因。
5. 8/3/5 场景可复现：池容量 3、全局 8 时，池外用例仍能占满剩余 5 个槽位；池内第 4 条不乱序。
6. 覆盖对账：R0 清单 1707 条仍是唯一分母，R5/R6 范围与 unit 范围都退出码 0，`dropped` 不增加。
7. 静态门禁保持可用且不执行测试：`fmt`、`fmt-check`、`vet`、`build`、`web-*`、`check`。
8. 一次完整 CI 通过（前置 R10-E1），三个失败 Job 的教训（环境前置、观察点、前端时序）没有复发。
9. 文档只写已实现能力；`docs/testing.md`、`docs/workspace.md`、`docs/development.md`、
   `tests/README.md`、`.github/scripts/README.md` 与新结构一致。
10. 没有为旧目录/旧入口保留任何兼容层或转发包。

## 13. 与 V3 / R10 的关系

- V3 已冻结的不变量（一条 Case 一个 worker、资源只由编排进程分配、六种结论、不伪造结果）
  在 V4 中**全部保持**；V4 只增加额度这一层约束，并收口执行入口。
- V3 的覆盖分母、R0 冻结件、`dropped` 清单**不改**。
- R10 处理 CI #60 的三个失败与文档勘误；V4 不重复处理，但 R8 的完整 CI 验收以 R10-E1 完成为前置。
- V4 不并入 V3 的阶段编号；V4 R0 是独立里程碑的第一个阶段，收口时新增 `.steps/V4-final.md`，
  结构与 `V3-final.md` 对齐。
