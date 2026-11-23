# V4 R8：unit 批量迁移 C（`store` / `httpapi` / `app`，898 条）

**状态：** 已完成（2026-11-23）。R8.1（`store`）248 条、R8.2（`httpapi`）241 条、R8.3（`app`）406 条用例
全部迁入统一执行器；`store` 与 `app` 已单包运行全部通过（无跳过、无独占 worker），`httpapi` 单包运行待
授权。仓库里已不再有 `_test.go`，静态门禁干净；分组核验在 R8.3 之后因 `unit` go test 分组为空而暂不可用，
由 [R9](../R9/README.md) 收口。见 §7–§11。
**下游关系：** 本阶段完成后仓库里不再有 `_test.go` 的新增面，删除 `go test` 路径在 [R9](../R9/README.md)。

---

## 1. 范围与子阶段

| 子阶段 | 包 | 文件 | `func Test*` | 说明 |
|---|---|---:|---:|---|
| R8.1 | `tests/unit/store` | 26 | 249 | SQLite 仓储；共享 DB 句柄与事务断言 |
| R8.2 | `tests/unit/httpapi` | 25 | 242 | httptest + 路由表；固定端口与监听相关用例 |
| R8.3 | `tests/unit/app` | 49 | 407 | 编排层；65 处 `go func` 与多处时序断言（并发启停、恢复、迟到结果） |

合计 898 条（含 3 个 `TestMain`）。每个子阶段独立验收、独立授权，不得合并成一次"全量跑"。

## 2. 任务（每个子阶段相同）

1. 按包迁移：`func run<Name>(c *Context)` + `Register` + 包内 `cases.go`；删除 `main_test.go`。
2. 逐条核对时序类断言：进程隔离消失后（复用 worker）"并发启停 / 恢复 / 迟到结果"的成立条件是否变化；
   有变化就把该条改为 `DedicatedWorker` 并写明原因，**不得**放宽断言。
3. `store` 的固定数据库文件、`httpapi` 的固定端口、`app` 的固定工作目录：一律走 `c.TempDir()` /
   资源声明，禁止跨 Case 共用同一路径。
4. 追加"迁移清单"：源数量、迁移后数量、跳过条目、`DedicatedWorker` 条目与理由、断言差异说明。
5. 每个子阶段完成后立刻授权运行该包一次，全部 `passed`，跳过条目只有已登记的那几条并有原因。

## 3. 验收探针

| 探针 | 期望 |
|---|---|
| 包内无测试文件 | `git ls-files '<pkg>/*_test.go'` 为空 |
| 注册总数 | R8.1 后 814 + 249 = 1063；R8.2 后 + 242 = 1305；R8.3 后 + 407 = 1712 |
| 用例通过 | 每个子阶段单独授权，全部 `passed` |
| 静态 | `make check`、`gofmt -l`、`go vet ./...` 干净 |
| 隔离 | 报告里 `isolated_cases` 与清单登记的条数一致 |

## 4. 授权

三个子阶段分别授权；`app` 可再按文件组细分授权。

## 5. 风险

| 风险 | 处置 |
|---|---|
| `app` 的时序断言在复用 worker 下变"稳定失败" | 逐条复核；必要时声明 `DedicatedWorker`，不改断言、不加重试 |
| `store` 共享 DB 句柄残留（复用 worker 内单例） | 迁移时改为每 Case 打开/关闭，或用 `c.Cleanup` 显式释放 |
| `httpapi` 固定端口用例并跑冲突 | 端口用例声明 `DedicatedWorker` 或改用 `local-port` 资源 |
| 一次授权变成全量跑 | 每个子阶段单独授权、单独记录 |

## 6. 不做的事

不删除 `make test-unit` 与 CI `go-unit`（R9）、不改调度器、不搬目录、不为通过而放宽断言。

## 7. 迁移清单（R8.1 `store`，2026-11-23）

`tests/unit/store` 的 `func Test*` 共 249 条，其中 1 条是 `TestMain`：删除后迁移出 **248 条 Case**。
分组名把包路径的 `/` 换成 `-`（`tests/unit/store` → `unit-store`），`Package` 仍填来源包路径；写法与
R5–R7 一致：`func run<原函数名去掉 Test 前缀>(t *testrunner.Context)`、包内 `case.go`（分组常量 +
`register` / `registerSerial`）与 `cases.go`（登记清单）。

| 项 | 源 | 迁移后 |
|---|---|---|
| 文件 | 26 个 `*_test.go` | 24 个 `cases_*.go` + `cases_fixture.go`（`fixtures_test.go` 只有辅助函数）+ `case.go` + `cases.go` |
| `func Test*` | 249（含 1 个 `TestMain`） | 248 条 Case |
| 包名 | `sqlite_test` | `sqlite`（正文里的 `sqlite.X` 专指 `internal/storage/sqlite`） |
| 并发属性 | 247 条带 `t.Parallel()`，`migrations-bootstrap` 没有 | 247 条 `Parallel: true`，`migrations-bootstrap` 串行登记 |
| `t.Run` 子测试 | 2 处（`planning_sessions`、`requirement_deletion`） | 保留为 `t.Run`，子测试里的 `t.Parallel()` 删除 |
| 跳过条目 | 0 | 0 |
| `DedicatedWorker` | 0 | 0 |

补充口径：

- 断言等价性：源函数清单与登记清单 1:1 对照（248 ↔ 248，无缺失、无多余），改动只有"函数改名 + 参数
  类型 + 删除 `t.Parallel()`"；12 处 `t.Errorf` 原样映射为 `t.Errorf`，`t.Fatal` / `t.Fatalf` 语义不变。
- 隔离：每条用例在 `testkit.New(t)` 的独占目录里打开自己的数据库文件，连接由 `t.Cleanup` 关闭，因此
  **不声明 `DedicatedWorker`**。逐条核对结果：没有 `os.Chdir` / `t.Setenv` / `t.TempDir` / 固定端口 /
  `sync.Once`；包级可变状态只有 `requirementDeleteAt` 这个固定时间值，复用 worker 下只读。
- 12 处 `go func` 全部保留：并发断言的结果经 channel / 切片回传，由主 goroutine 判定；唯一在 goroutine
  内直接断言的是 `planning-analysis-claim-serializes-concurrent-callers` 的 `t.Errorf`，`Context.Errorf`
  带互斥，语义不变。`concurrency` 三条用例在 goroutine 内调用 `newTestDB(t)` / `testContext(t)`，仍由
  用例独占目录建库，失败路径由 `Context` 的互斥与 panic 传播承担，成功路径断言未变。
- 分组声明：`.github/scripts/go-test-groups.sh` 的 `unit` 段新增 `unit-store`；`tests/registry/registry.go`
  追加空导入。

## 8. 探针结果（R8.1，2026-11-23）

| 探针 | 结果 |
|---|---|
| 包内无测试文件 | `git ls-files 'tests/unit/store/*_test.go'` 为空 |
| 注册总数 | 1089 / 41 个分组（Windows 实测）；Linux 上多 2 条 linux-only 用例 = 1091。新增 1 个分组共 248 条 |
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend,unit` 通过：2 个 go test 测试包、40 个 Case 分组全部分类 |
| 静态 | `make check`（`gofmt -l` / `go vet ./...` / `go build`）干净 |
| 用例通过 | `unit-store` 单包运行：248 条 `passed`、0 条 `skipped`、0 失败，见 §8.1 |
| 断言不丢 | 源 249 条 `func Test*`（含 `TestMain`）→ 248 条 Case，逐条 1:1 对照无缺失、无多余 |
| 隔离 | `isolated_cases` 实测 0：本子阶段没有 `DedicatedWorker` 声明 |

### 8.1 单包运行结果（Windows 11，`--parallel 4`）

| 分组 | 用例 | 通过 | 跳过 | 失败 | 峰值并发 | worker | 复用 | 重启 | 独占 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `unit-store` | 248 | 248 | 0 | 0 | 4 | 4 | 244 | 0 | 0 |

- 额度分节：`parallel_limit 4` / `max_in_flight 4` / 包 `tests/unit/store` 的 `limit 0`、`peak 4`、
  `completed 248`、`undispatched 0`，没有池声明。
- 运行命令把并发参数放在选择条件之前（`--parallel 4 unit-store`）：`flag` 解析遇到第一个位置参数即停止。

口径订正与遗留：

- §3 原写"R8.1 后 814 + 249 = 1063"：基数取自 R6 之前的估算，且把 `TestMain` 算成了用例。按 R7 实测
  （841 / 40 分组，Windows）与 248 条用例主体，期望值是 1089（Windows）/ 1091（Linux）。ROADMAP §11 的
  1738 是 Linux 口径的估算值，收口时按实测更新。
- `docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` 里的分组数量与 `tests/unit` 包数需要
  同步；按文档约定未在代码编辑期改动，留到 R9 统一收口。

## 9. 迁移清单（R8.2 `httpapi`，2026-11-23）

`tests/unit/httpapi` 的 `func Test*` 共 242 条，其中 1 条是 `TestMain`：删除后迁移出 **241 条 Case**。
分组名 `unit-httpapi`，`Package` 仍填来源包路径 `tests/unit/httpapi`，写法与 R5–R8.1 一致：
`func run<原函数名去掉 Test 前缀>(t *testrunner.Context)`、包内 `case.go`（分组常量 + `register` /
`registerSerial`）与 `cases.go`（登记清单），每个源文件迁成同名 `cases_*.go`。

| 项 | 源 | 迁移后 |
|---|---|---|
| 文件 | 26 个 `*_test.go` | 24 个 `cases_*.go` + `case.go` + `cases.go`；`main_test.go` 删除 |
| `func Test*` | 242（含 1 个 `TestMain`） | 241 条 Case |
| 包名 | `httpapi_test` | `httpapi`（正文里的 `httpapi.X` 专指 `internal/httpapi`） |
| 并发属性 | 221 条带 `t.Parallel()`；20 条没有（`planning_analysis_reliability` 4、`planning_analysis` 8、`planning` 8） | 221 条 `Parallel: true`；20 条串行登记 |
| `t.Run` 子测试 | 9 处 | 全部保留在 Case 内，子测试签名改收 `*testrunner.Context` |
| 跳过条目 | 0 | 0 |
| `DedicatedWorker` | 0 | 0 |

补充口径：

- 断言等价性：源函数清单与登记清单 1:1 对照（241 ↔ 241，无缺失、无多余），逐文件核对函数总数在迁移
  前后一致；改动只有"函数改名 + 参数类型 + 删除 `t.Parallel()`"。24 处 `t.Errorf` 原样映射为
  `t.Errorf`，`t.Fatal` / `t.Fatalf` 语义不变，没有任何断言条件被改写或新增跳过。
- 说明行：每条源用例前的 `// TestXxx …` 说明块按 R5–R8.1 既有约定删除（已迁移的 24 个包同样没有
  逐用例说明行），多行说明块一并删除，不留指向已不存在函数名的残留引用。
- 隔离：数据库、密钥与工作目录都由 `testkit.New(t)` / `testkit.Dir(t)` 建在用例独占目录里，连接由
  `t.Cleanup` 关闭；需要真实流式连接的 7 处 `httptest.NewServer` 一律 `127.0.0.1:0` 由系统分配端口，
  不占固定端口。逐条核对结果：没有 `os.Chdir`、`t.Setenv`、`t.TempDir`，包级可变状态只有
  `errPlanningAPIRemoteUnexpected` 这一个不可变错误值，`const` 与固定时间值只读。因此 **不声明**
  `DedicatedWorker`。
- 3 处 `go func` 全部保留：`history` 的请求处理与流读取 goroutine 内不做断言；`planning_analysis` 与
  `requirement_confirm` 的 goroutine 只调用 fixture 的 `send`，其 `Fatalf` 只在 `json.Marshal` 失败时
  可达，两处入参都是 `map[string]string`，属于不可达路径，因此不作为独占理由。
- 分组声明：`.github/scripts/go-test-groups.sh` 的 `unit` 段新增 `unit-httpapi`；
  `tests/registry/registry.go` 追加空导入。

### 9.1 探针结果（R8.2）

| 探针 | 结果 |
|---|---|
| 包内无测试文件 | `git ls-files 'tests/unit/httpapi/*_test.go'` 为空；全仓剩余 41 个 `_test.go`，全部在 `tests/unit/app` |
| 注册总数 | 1330 条 / 42 个分组（Windows 实测），其中 `unit-httpapi` 241 条 |
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend,unit` 通过：1 个 go test 测试包、41 个 Case 分组全部分类 |
| 静态 | `make check`（`gofmt -l` / `go vet ./...` / `go build`）干净 |
| 断言不丢 | 源 242 条 `func Test*`（含 `TestMain`）→ 241 条 Case，逐文件 1:1 对照无缺失、无多余 |
| 隔离 | `DedicatedWorker` 声明 0，故预期 `isolated_cases` 为 0 |
| 用例通过 | 待授权运行 `unit-httpapi`（本子阶段尚未执行） |

口径订正：

- §1 / §3 的 242 与"R8.2 后 + 242 = 1305"把 `TestMain` 算成了用例，且基数是 R6 之前的估算。实测本
  子阶段产出 241 条 Case，R8.2 后总计 1330（Windows）/ 1332（Linux，含 2 条 linux-only 用例）。
- ROADMAP §11 的 1738 同样是 Linux 口径的估算值，R9 收口时按实测重写。
- `docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` 里的分组数量、`tests/unit` 包数与
  go test 测试包数需要同步；按文档约定未在代码编辑期改动，留到 R9 统一收口。

## 10. 迁移清单（R8.3 `app`，2026-11-23）

`tests/unit/app` 的 `func Test*` 共 407 条，其中 1 条是 `TestMain`：删除后迁移出 **406 条 Case**。
分组名 `unit-app`，`Package` 填 `tests/unit/app`，写法与 R5–R8.2 一致：`func run<原函数名去掉 Test 前缀>`
（`t *testrunner.Context`）、包内 `case.go`（分组常量 + `register` / `registerSerial`）与 `cases.go`
（登记清单）。

| 项 | 源 | 迁移后 |
|---|---|---|
| 文件 | 41 个 `*_test.go`（含 `main_test.go`） | 40 个 `cases_*.go` + `case.go` + `cases.go`；`main_test.go` 删除 |
| `func Test*` | 407（含 1 个 `TestMain`） | 406 条 Case |
| 包名 | `app_test` | `app`（正文里的 `app.X` 专指 `internal/app`） |
| 并发属性 | 370 条带 `t.Parallel()`；36 条没有（`planning_runtime` 整个文件） | 370 条 `Parallel: true`，36 条串行登记 |
| `t.Run` 子测试 | 59 处，其中 24 处在子测试内声明 `t.Parallel()` | 全部保留在 Case 内（子测试签名改收 `*testrunner.Context`），子测试内的 `t.Parallel()` 删除 |
| `go func` | 41 处 | 全部保留；goroutine 内没有任何 `t.Fatal*`（只有一处 `t.Error`，见下） |
| 跳过条目 | 0 | 0 |
| `DedicatedWorker` | — | 0 |

补充口径：

- 文件命名：`<name>_test.go` → `cases_<name>.go`，fixture 文件同样 1:1 对应（`container_fixture_test.go`
  → `cases_container_fixture.go`）。这与 R8.1/R8.2 把唯一 fixture 收成 `cases_fixture.go` 的做法不同，
  本子阶段有 5 个 fixture 文件，1:1 对应更便于逐文件核对。
- 断言等价性：逐源文件 1:1 对照 `TestX` ↔ `runX`（406 ↔ 406，无缺失、无多余）。断言调用计数前后一致：
  `t.Fatal` 320 → 320、`t.Fatalf` 2008 → 2008、`t.Cleanup` 96 → 96、`t.Helper` 166 → 166、`t.Run` 59 → 59、
  `go func` 41 → 41；`t.Errorf` 39 → 40，唯一变化是 `cases_planning_recovery.go` 里子测试内 select 分支的
  `t.Error(...)` 改写成 `t.Errorf(...)`——`Context` 没有 `Error`，改写后语义仍是"记录失败后继续执行"，
  没有放宽或改写任何断言条件。
- 说明行：每条源用例前的 `// TestXxx …` 说明块按 R8.2 起的约定删除（不留指向已改名函数的引用）；文件头
  注释保留为文件级注释（`// Package app_test …` 改为 `// 本文件…`），与 `package` 之间留空行，包级文档
  只写在 `case.go`。
- 隔离：数据库由 `testkit.New(t)` 打开、连接由 `t.Cleanup` 关闭；远程节点、Docker、Git 与 Agent 调用全部
  走受控替身，不启动真实外部进程；没有 `os.Chdir`、`t.Setenv`、`t.TempDir`。两处 `sync.Once` 都只属于
  用例内构造的替身类型（`blockingDeleteTx`、`fakeRuntimeRemote.blockOn`），`hosts` 的两处真实监听用
  `127.0.0.1:0` 由系统分配端口。因此 **不声明** `Resources`，也不声明 `DedicatedWorker`。
- 分组声明：`.github/scripts/go-test-groups.sh` 的 `unit` 段新增 `unit-app`；`tests/registry/registry.go`
  追加空导入。

### 10.1 迁移中修复的缺陷：testkit 日志器写坏 worker 控制通道

首次单包运行 406 条里有 21 条判为"异常"（通过 385 / 失败 0 / 跳过 0 / 异常 21），两类表现同源：

- 19 条报 `worker N 控制通道出现无法解析的输出: time=… level=INFO msg=环境构建完成 …`；
- 3 条报 `worker 未写出结果文件（退出码 1）`（同一污染打断了 worker 的收尾回报）。

根因在 `tests/testkit/logging.go` 的 `logWriter()`：`runLogSink` 未初始化（没有走 `testkit.Main`）时它退回
**包初始化时捕获的 `os.Stdout`**。而统一执行器的 worker 把启动瞬间的标准输出当作控制通道
（`workerBaseline.control`），用例执行期间才把 `os.Stdout` 指向该用例的产物日志、结束后指向空设备。于是
`testkit.Slog()` 的日志直接写进了控制通道，worker 侧只能判为"无法解析的输出"。

修复方式是把兜底目标改为动态读取 `os.Stdout`，日志因此跟随用例产物日志与空转目标，不再落到控制通道。
产品代码、worker 协议、分组声明与用例断言都没有改动；同一分组修复后 406 条全部通过（§11.1）。共享测试
辅助的这一缺陷同样影响 R8.1/R8.2 已迁移的包，只是那些用例没有触发日志输出。

## 11. 探针结果（R8.3）

| 探针 | 结果 |
|---|---|
| 包内无测试文件 | 工作树中 `tests/unit/app/*_test.go` 为 0，`tests/unit/**` 下不再有任何 `_test.go` |
| 注册总数 | 1736 条 / 42 个分组（Windows 实测），其中 `unit-app` 406 条 |
| 用例通过 | `unit-app` 单包运行：406 条 `passed`、0 跳过、0 失败、0 panic / 超时 / 异常，见 §11.1 |
| 断言不丢 | 逐源文件 `TestX` ↔ `runX` 1:1（406 ↔ 406），断言调用计数前后一致，差异只有 `t.Error` → `t.Errorf` 一处 |
| 隔离 | `DedicatedWorker` 声明 0，`isolated_cases` 实测 0 |
| 静态 | `make check`（`gofmt -l` / `go vet ./...` / `go build`）干净 |
| 分组核验 | **不再通过**：`go-test-groups.sh --check` 报"没有解析到任何测试包"——`unit` go test 分组在 R8.3 之后已空，核验逻辑在 `package_total = 0` 处短路，属于 R9 的既定收口项 |
| Case 分组归属 | 逐条核对：42 个注册分组全部被声明覆盖，无未声明分组、无落空声明（上游脚本的这项核验被上一条短路，故单独核对） |

### 11.1 单包运行结果（Windows 11，`--parallel 4`）

| 分组 | 用例 | 通过 | 跳过 | 失败 | 峰值并发 | worker | 复用 | 重启 | 独占 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `unit-app` | 406 | 406 | 0 | 0 | 4 | 4 | 402 | 0 | 0 |

- 额度分节：`parallel_limit 4` / `max_in_flight 4` / 包 `tests/unit/app` 的 `limit 0`、`peak 4`、
  `completed 406`、`undispatched 0`，没有池声明；整轮 19.1s（队列合计 3279s、单条最长 19.0s）。
- 零残留：`not-applicable`（本分组没有声明任何资源）。
- 运行命令把并发参数放在选择条件之前（`--parallel 4 --artifact-dir .test-artifacts unit-app`）。

口径订正与遗留：

- §1 / §3 的"R8.3 后 + 407 = 1712"把 `TestMain` 算成了用例。实测本子阶段产出 406 条 Case，R8.3 后总计
  1736（Windows）；Linux 上多 2 条 linux-only 用例 = 1738，与 ROADMAP §11 的估算值一致，R9 收口时按实测
  写定。
- `_test.go` 已全部消失，但 `go-test-groups.sh` 的 `unit` go test 分组、`make test-unit` 与 CI `go-unit`
  仍在，R9 一并删除；在那之前分组核验无法通过。
- `docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` 里的分组数量、`tests/unit` 包数与
  go test 测试包数需要同步，按文档约定留到 R9 统一收口。
- 历史遗留（R9 待定）：R8.1/R8.2 之前的包保留了指向已改名函数的 `// TestXxx …` 说明行（`domain` 245 处、
  `store` 160 处），而 R8.2 起的约定是不保留这类陈旧引用。
