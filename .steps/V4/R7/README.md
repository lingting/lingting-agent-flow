# V4 R7：unit 批量迁移 B（`domain` / `container` / `agent`，316 条）

**状态：** 未开始。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R6 批量 A](../R6/README.md)。
**下游关系：** 批量 C 在 [R8](../R8/README.md)；删除 `go test` 路径在 [R9](../R9/README.md)。

---

## 1. 范围

| 包 | 文件 | `func Test*` | 形态 |
|---|---:|---:|---|
| `tests/unit/domain` | 28 | 250 | 纯领域规则，无外部依赖；最适合大批量验证写法 |
| `tests/unit/container` | 4 | 30 | 外部进程替身（Docker 命令构造），验证 `Cleanup` 与子进程收尾 |
| `tests/unit/agent` | 4 | 36 | 外部进程替身 + 会话/工作空间断言 |

合计 316 条（含 3 个 `TestMain`）。本阶段的关键价值是证明"大包一次迁移"不会丢断言。

## 2. 任务

1. `domain` 先做：它是纯函数包，可用它验证批量改写的机械性与一致性（一次一个文件组，逐组提交）。
2. `container` / `agent` 后做：这两包有进程替身，逐条核对 `t.Cleanup` 的收尾顺序与子进程终止。
3. 每包迁移后：删除 `main_test.go`；`git ls-files '<pkg>/*_test.go'` 为空；`--list` 数量递增；
   该包用例单独授权运行一次。
4. 追加"迁移清单"：包、源数量、迁移后数量、被跳过条目、需 `DedicatedWorker` 的条目及理由、
   子测试处理方式（保留 `c.Run` 或独立成 Case）。
5. `domain` 的包级表/常量（10 个文件里的包级 `var`）在复用 worker 下必须只读；若存在可变包级状态，
   记录处理方式。

## 3. 验收探针

| 探针 | 期望 |
|---|---|
| 包内无测试文件 | 3 个包的 `*_test.go` 为空 |
| 注册总数 | 498（R6 之后）+ 316 = 814 |
| 用例通过 | 每个包单独授权运行一次，全部 `passed` |
| 断言不丢 | 每包迁移前后 `func Test*` 清单逐条对照，差异只有"子测试升为 Case" |
| 静态 | `make check`、`gofmt -l`、`go vet ./...` 干净 |

## 4. 授权

按包授权；`domain` 可一次授权整包，`container` / `agent` 建议按文件组授权。

## 5. 风险

| 风险 | 处置 |
|---|---|
| `domain` 数量大，机械改写引入 `Errorf`/`Fatalf` 语义错位 | 逐文件核对映射；批量脚本不生成语义判断 |
| `container` / `agent` 的进程替身在复用 worker 下端口/临时目录冲突 | 需要的条目声明 `DedicatedWorker`，并登记理由 |
| 一次提交过大难以评审 | 一个文件组一个提交，`git diff` 只看等价性 |

## 6. 不做的事

不动 `app` / `httpapi` / `store`（R8）、不改调度器、不搬目录、不删除 `make test-unit`（R9）。
