# V4 R8：unit 批量迁移 C（`store` / `httpapi` / `app`，898 条）

**状态：** 未开始。
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
