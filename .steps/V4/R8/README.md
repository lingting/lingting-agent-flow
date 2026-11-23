# V4 R8：unit 批量迁移 C（`store` / `httpapi` / `app`，898 条）

**状态：** 进行中。R8.1（`store`）已完成（2026-11-23）：248 条用例迁入统一执行器，单包运行全部通过
（无跳过、无独占 worker），静态门禁与分组核验干净。R8.2（`httpapi`）/ R8.3（`app`）未开始。见 §7、§8。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R7 批量 B](../R7/README.md)。
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
