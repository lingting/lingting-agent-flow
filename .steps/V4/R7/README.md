# V4 R7：unit 批量迁移 B（`domain` / `container` / `agent`，316 条）

**状态：** 已完成（2026-11-23）：3 个包共 313 条用例迁入统一执行器，逐包单跑全部通过（无跳过、
无独占 worker），静态门禁与分组核验干净。见 §7、§8。
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

## 7. 迁移清单（2026-11-23）

3 个包的 `func Test*` 共 316 条，其中 3 条是 `TestMain`：删除后迁移出 **313 条 Case**。分组名把包
路径的 `/` 换成 `-`（`tests/unit/domain` → `unit-domain`），`Package` 仍填来源包路径。写法与 R5/R6
一致：`func run<原函数名去掉 Test 前缀>(t *testrunner.Context)`、包内 `case.go`（分组常量 +
`register`）与 `cases.go`（登记清单），每个源文件迁成同名 `cases_*.go`。

| 包（分组） | 源 `func Test*` | Case | 逐条差异说明 |
|---|---:|---:|---|
| `tests/unit/domain`（`unit-domain`） | 250（含 1 个 `TestMain`） | 249 | 28 个文件 → 27 个 `cases_*.go`（`main_test.go` 删除）；249 条全部带 `t.Parallel()`，按可并行登记；57 个 `t.Run` 子测试全部保留在 Case 内；测试现场辅助函数（39 处 `t.Helper()` 所在函数）改收 `*testrunner.Context`；包名 `domain_test` → `domain`，正文里的 `domain.X` 专指 `internal/domain` |
| `tests/unit/container`（`unit-container`） | 29 + 1 个 `TestMain` | 29 | 4 个文件 → 3 个 `cases_*.go`；29 条全部可并行；2 个 `t.Run` 子测试保留；Docker 命令以参数切片核对，不执行 `docker` |
| `tests/unit/agent`（`unit-agent`） | 35 + 1 个 `TestMain` | 35 | 4 个文件 → 3 个 `cases_*.go`；35 条全部可并行；2 个 `t.Run` 子测试保留；Pi 与容器执行器仍是受控替身，只有 `cases_agent.go` 与 `cases_container_runner.go` 用到 `tests/testkit`（辅助函数收 `testkit.Tester`，`*testrunner.Context` 天然满足） |

补充口径：

- `TestMain` 3 条全部删除：进程级日志、结论与运行根目录由执行器承担。
- 子测试一律保留在 Case 内（`c.Run`），既没有为了数量好看拆成独立 Case，也没有合并。
- 跳过条目：**0 条**。三个包都没有 `t.Skipf`/`t.Skip`，也没有新增跳过条件。
- `DedicatedWorker`：本批 **0 条**。逐包核对结果：没有 `os.Exit`、没有 `t.Setenv`、没有 `t.TempDir`、
  没有 `os.Chdir`、没有固定端口、没有 `go func`、没有 `sync.Once`；包级 `var` 只有 `queueAt`、
  `retryAt`、`workflowRunAt`、`planningBaseTime` 四个 `time.Time` 常量值，其余是 `const`，因此在复用
  worker 下只读，不需要独占进程。
- `t.Errorf` 7 处（全在 `domain`）保持"记录失败后继续执行"的语义，与 `c.Errorf` 一一对应；`t.Fatal`
  / `t.Fatalf` 直接映射为 `c.Fatal` / `c.Fatalf`，没有改写断言条件。
- 文件头注释：`domain` 的 12 个源文件头注释保留为文件级注释（`// Package domain_test …` 改为
  `// 本文件…`），与 `package` 之间留空行；包级文档只写在 `case.go`，避免 27 个文件重复声明 package
  doc。
- 分组声明：`.github/scripts/go-test-groups.sh` 的 `unit` 段新增 `unit-agent` / `unit-container` /
  `unit-domain` 三行。

## 8. 探针结果（2026-11-23）

| 探针 | 结果 |
|---|---|
| 包内无测试文件 | 3 个包的 `git ls-files '<pkg>/*_test.go'` 均为空 |
| 注册总数 | 841 / 40 个分组（Windows 实测）；Linux 上多 2 条 linux-only 用例 = 843。新增 3 个分组共 313 条 |
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend,unit` 通过：3 个 go test 测试包、39 个 Case 分组全部分类 |
| 静态 | `make check`（`gofmt -l` / `go vet ./...` / `go build`）干净 |
| 用例通过 | 3 个分组逐包单跑：313 条 `passed`、0 条 `skipped`、0 失败，见 §8.1 |
| 断言不丢 | 逐源文件对照：`domain` 249/249、`container` 29/29、`agent` 35/35，源函数清单与登记清单 1:1，差异只有"子测试保留为 `c.Run`" |
| 隔离 | `isolated_cases` 实测 0：本批没有 `DedicatedWorker` 声明 |

### 8.1 逐包运行结果（Windows 11，`--parallel 4`）

| 分组 | 用例 | 通过 | 跳过 | 失败 | 峰值并发 | worker | 复用 |
|---|---:|---:|---:|---:|---:|---:|---:|
| `unit-domain` | 249 | 249 | 0 | 0 | 4 | 4 | 245 |
| `unit-container` | 29 | 29 | 0 | 0 | 4 | 4 | 25 |
| `unit-agent` | 35 | 35 | 0 | 0 | 4 | 4 | 31 |

- 三个分组都是 `worker 重启 0 / 独占 0`，`--parallel 4` 下峰值 4、复用正常。
- 运行命令把并发参数放在选择条件之前（`--parallel 4 unit-domain`）：`flag` 解析遇到第一个位置参数
  即停止，写在选择条件之后的 `--parallel` 会被当成选择条件而不是并发上限。

口径订正与遗留：

- §3 原写"498（R6 之后）+ 316 = 814"：基数取自 R6 之前的估算，且把 3 个 `TestMain` 算成了用例。按 R6
  实测（528 / 37 分组，Windows）与 313 条用例主体，期望值是 841（Windows）/ 843（Linux）；ROADMAP
  §11 的 843 是 Linux 口径。
- `docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` 里的分组数量与 `tests/unit` 包数需要
  同步；按文档约定未在代码编辑期改动，留到 R9 统一收口。
