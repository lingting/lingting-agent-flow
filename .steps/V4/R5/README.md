# V4 R5：unit 迁移试点（`execenv` + `lifecycle`，28 条）

**状态：** 未开始。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R0 冻结决策 D-02 / D-09](../R0/README.md)、[R4 目录收敛](../R4/README.md)。
**下游关系：** 试点结论是批量迁移（[R6](../R6/README.md)–[R8](../R8/README.md)）的准入条件；试点不通过不得批量迁移。

---

## 1. 目标

用两个小包验证"unit 用例改成 Case"的写法、成本与隔离假设：

| 包 | 文件 | `func Test*` | 形态 |
|---|---:|---:|---|
| `tests/unit/execenv` | 2 | 21 | 纯函数级，无外部进程 |
| `tests/unit/lifecycle` | 2 | 7 | 有并发启停语义，可验证 `Cleanup` 与失败传播 |

合计 28 条（含 2 个 `TestMain`，迁移后删除）。

## 2. 写法（冻结）

- 每条源测试函数迁移为 `func run<原函数名去掉 Test 前缀>(c *testrunner.Context)`，用
  `testrunner.Register` 登记，`ID` 形如 `unit-execenv/<name>`。
- `Package` 填来源路径 `tests/unit/execenv`（与目录一致），服务串行作用域与报告定位。
- `TestMain` 删除：进程级 fd 重定向、`latest-run.txt`、默认 `-test.v` 都由执行器承担。
- `t.Fatalf` → `c.Fatalf`、`t.Errorf` → `c.Errorf`、`t.Cleanup` → `c.Cleanup`、`t.Helper` → `c.Helper`、
  `t.Run` → `c.Run`；`t.Parallel()` 迁移为 `Case.Parallel: true`。
- **不得**出现 `go test` 执行路径（D-09）；不得引入 `go test -c` 适配器。

## 3. 任务

1. 迁移两个包的用例与断言，逐函数核对语义（`Fatalf` 终止 / `Errorf` 继续）。
2. 每个包一个 `cases.go` 聚合注册，并在 `tests/registry/registry.go`（R4 之后路径）追加两行空导入。
3. 删除两个包的 `main_test.go`，确认包内不再有 `_test.go`。
4. 在本文追加"迁移清单"：包内 `func Test*` 数量、迁移后 Case 数量、逐条差异说明。
5. 记录两个数字：迁移后该批 Case 的总时长与 worker 启动开销，与 `go test` 基线对比；单条失败信息是否
   仍能定位到具体断言。

## 4. 验收探针

| 探针 | 命令 / 产物 | 期望 |
|---|---|---|
| 无测试文件 | `git ls-files 'tests/unit/execenv/**' 'tests/unit/lifecycle/**' '*_test.go'` | 空 |
| Case 已注册 | `./bin/agentflow-test --list` | 新增 28 条，总数 360 + 28 = 388 |
| 用例通过 | `./bin/agentflow-test unit-execenv unit-lifecycle`（需授权） | 28/28 `passed` |
| 隔离 | 同一 worker 复用时断言成立 | 无串味（记录在本文） |
| 成本 | 时长与 worker 开销对比 | 写回本文，供 R6 判断是否需要调整批量粒度 |

## 5. 授权

需要显式授权运行这两个包的迁移用例（一次）。

## 6. 风险

| 风险 | 处置 |
|---|---|
| `t.Parallel()` 语义降级导致时序用例行为变化 | 逐条核对有 `go func` / `sync.Once` / 固定端口的文件 |
| `Cleanup` 顺序与 `Fatalf` 终止语义不一致 | 逐点核对 `Errorf` / `Fatalf` 的迁移映射 |
| 试点只跑通过不看失败信息质量 | 必须构造一次失败，确认行号与断言可读 |

## 7. 不做的事

不改调度器、不改 worker 模型、不搬目录、不迁移其他包、不为试点引入 go test 过渡路径。
