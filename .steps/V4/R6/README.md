# V4 R6：unit 批量迁移 A（9 个中小包，110 条）

**状态：** 未开始。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R5 试点结论](../R5/README.md)。
**下游关系：** 批量 B 在 [R7](../R7/README.md)、批量 C 在 [R8](../R8/README.md)。

---

## 1. 范围

| 包 | 文件 | `func Test*` | 形态 |
|---|---:|---:|---|
| `tests/unit/testkit` | 2 | 2 | 被测对象是共享辅助本身（与 `tests/testkit` 不是同一个包） |
| `tests/unit/testrunner` | 3 | 4 | 执行器自身（监听进程发现，`8a214ad` 新增） |
| `tests/unit/cigate` | 2 | 10 | 纯逻辑 + 文件系统 |
| `tests/unit/app/planningagent` | 3 | 10 | fixture 结构体 + `sync.Once` |
| `tests/unit/image` | 2 | 11 | 纯函数 + 字典表 |
| `tests/unit/environment` | 3 | 12 | httptest + 文件系统 |
| `tests/unit/server` | 2 | 14 | httptest + 生命周期 |
| `tests/unit/source` | 3 | 22 | httptest + 临时目录 + 符号链接权限跳过 |
| `tests/unit/app/planninganalysis` | 5 | 25 | fixture + 状态机断言 |

合计 110 条（含 9 个 `TestMain`），迁移后用例主体 101 条。

## 2. 任务

1. 一个包一个提交，写法与 R5 完全一致（`func run<Name>(c *Context)` + `Register` + 包内 `cases.go`）。
2. 每包迁移后立刻：删除该包 `main_test.go`；`git ls-files '<pkg>/*_test.go'` 为空；
   `./bin/agentflow-test --list` 数量按本文表格递增。
3. 在本文追加"迁移清单"表：包、源 `func Test*` 数量、迁移后 Case 数量、差异说明（子测试是否升为独立 Case、
   被跳过条目及原因、需要独占 worker 的条目及理由）。
4. 逐包核对包级可变状态（`sync.Once`、包级 `var`、`os.Chdir`、环境变量写入）：需要进 `DedicatedWorker`
   的条目必须写明原因。
5. `tests/unit/testkit` 与 `tests/testkit` 的命名边界写进本文：前者是一个 unit 用例包，后者是共享辅助，
   不得合并、不得互称。

## 3. 验收探针

| 探针 | 期望 |
|---|---|
| 包内无测试文件 | 9 个包的 `*_test.go` 均为空 |
| 注册总数 | 388（R5 之后）+ 110 = 498 |
| 用例通过 | 每个包单独授权运行一次，全部 `passed`；跳过条目只有已登记的那几条并有原因 |
| 隔离 | `DedicatedWorker` 条目在报告中可数，理由齐全 |
| 静态 | `make check`、`gofmt -l`、`go vet ./...` 干净 |

## 4. 授权

按包分批授权运行（一次一个包），不一次性跑全量。

## 5. 风险

| 风险 | 处置 |
|---|---|
| `app/planningagent` 的 `sync.Once` fixture 在复用 worker 下状态残留 | 迁移时确认 `Once` 的作用域在 fixture 实例内；必要时改为每 Case 新建 |
| `server` / `source` / `httpapi` 类包固定端口或临时目录 | 固定端口用例声明 `DedicatedWorker`；临时目录用 `c.TempDir()` |
| 子测试被合并成一条 Case 导致定位变差 | 允许 `c.Run` 保留子测试，不强制拆分 |

## 6. 不做的事

不改调度器与 worker 模型、不搬目录、不动 `app` / `httpapi` / `store` / `domain` / `container` / `agent`。
