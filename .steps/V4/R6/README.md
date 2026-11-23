# V4 R6：unit 批量迁移 A（9 个中小包，110 条）

**状态：** 已完成（2026-11-23）：9 个包共 101 条用例迁入统一执行器，逐包单跑全部通过（7 条 Windows
符号链接用例按源用例已有条件跳过），静态门禁干净。见 §7、§8。
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
| 注册总数 | 429（R5 之后，含 26 条 unit）+ 101 = 530（Linux）；Windows 上少 2 条 linux-only 用例，见 §8 |
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

## 7. 迁移清单（2026-11-23）

9 个包的 `func Test*` 共 110 条，其中 9 条是 `TestMain`：删除后迁移出 **101 条 Case**。分组名把
包路径的 `/` 换成 `-`（`tests/unit/app/planningagent` → `unit-app-planningagent`），`Package` 仍填
来源包路径。写法与 R5 一致：`func run<原函数名去掉 Test 前缀>(t *testrunner.Context)`、包内
`case.go`（分组常量 + `register`）与 `cases.go`（登记清单），文件统一改名 `cases_*.go`。

| 包（分组） | 源 `func Test*` | Case | 逐条差异说明 |
|---|---:|---:|---|
| `tests/unit/testkit`（`unit-testkit`） | 2（含 1 个 `TestMain`） | 1 | 4 个 `t.Run` 保留为 `t.Run` 子测试；源用例没有 `t.Parallel()`，按串行条目登记；共享辅助导入用 `sharedkit` 别名，避免与包名 `testkit` 混淆 |
| `tests/unit/testrunner`（`unit-testrunner`） | 4（含 1 个 `TestMain`） | 3 | 3 条都没有 `t.Parallel()`，全部串行；`listener_fallback_linux_*` 对应的 2 条只在 Linux 出现，登记写在 `cases_linux.go`；`t.Setenv` 改为 `setPath`（`os.Setenv` + `t.Cleanup` 恢复，第二个子测试基于原始 `PATH`）；`t.TempDir` 改为 `tempDir` 并显式处理错误；`t.Skipf` 原样保留 |
| `tests/unit/cigate`（`unit-cigate`） | 10（含 1 个 `TestMain`） | 9 | 9 条全部 `t.Parallel()`，按可并行登记；13 个 `t.Run` 子测试保留，子测试里的 `t.Parallel()` 删除 |
| `tests/unit/app/planningagent`（`unit-app-planningagent`） | 10（含 1 个 `TestMain`） | 9 | 9 条全部可并行；`cases_fixture.go` 无用例，只把 7 个接受 `*testing.T` 的辅助函数换成 `*testrunner.Context`；现场里的 `sync.Once` 是函数内局部变量，包级没有 `var`，不需要独占 worker |
| `tests/unit/image`（`unit-image`） | 11（含 1 个 `TestMain`） | 10 | 10 条全部可并行；`newBaseVersion` 换成 `*testrunner.Context`；`testkit.Dir(t)` 原样保留（`*testrunner.Context` 满足 `testkit.Tester`） |
| `tests/unit/environment`（`unit-environment`） | 12（含 1 个 `TestMain`） | 11 | 11 条全部可并行；两个源文件分别迁成 `cases_environment.go` 与 `cases_reusable_version.go` |
| `tests/unit/server`（`unit-server`） | 14（含 1 个写在 `process_lock_test.go` 里的 `TestMain`） | 13 | 12 条可并行；`server-lock-path-matches-relative-path` 源用例用 `t.Chdir`，改为 `chdir` 辅助（`os.Chdir` + 切回）并按串行登记；HTTP 监听一律 `127.0.0.1:0`，没有固定端口 |
| `tests/unit/source`（`unit-source`） | 22（含 1 个 `TestMain`） | 21 | 21 条全部可并行；6 个 `t.Run` 子测试保留；包名 `source_unit` 改为 `source`；`status_test.go` 没有用例、只有状态替身，改名 `helpers.go` |
| `tests/unit/app/planninganalysis`（`unit-app-planninganalysis`） | 25（含 1 个 `TestMain`） | 24 | 24 条全部可并行；`cases_fixture.go` 无用例，只换 11 个辅助函数签名；2 个 `t.Run` 子测试保留；`t.TempDir` 显式处理错误 |

补充口径：

- `TestMain` 9 条全部删除：进程级日志、结论与运行根目录由执行器承担。
- 子测试一律保留在 Case 内（`t.Run`），没有为了数量好看拆成独立 Case，也没有合并。
- 跳过条目：只有 `unit-testrunner/listener-pids-with-ss`（容器内没有 `ss` 时 `t.Skipf`）沿用源用例的
  跳过条件，没有新增跳过。
- `DedicatedWorker`：本批 **0 条**。`server` 只切工作目录、`testrunner` 只改 `PATH`，两者都在用例内
  恢复，且 worker 在用例之间按 W3 重置工作目录与环境变量；数据库、锁文件、符号链接夹具与临时目录都
  建在用例独占目录里；没有固定端口、`os.Exit` 与包级 `var`。
- 命名边界：`tests/testkit` 是共享辅助，`tests/unit/testkit` 是一个 unit 用例包；后者正文用
  `sharedkit` 前缀引用前者，两者不合并、不互相空导入。

## 8. 探针结果（2026-11-23）

| 探针 | 结果 |
|---|---|
| 包内无测试文件 | 9 个包的 `git ls-files '<pkg>/*_test.go'` 均为空 |
| 注册总数 | 528 / 37 个分组（Windows 实测）；Linux 上多 2 条 linux-only 用例 = 530。新增 9 个分组共 99（Linux 101）条 |
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend,unit` 通过：6 个 go test 测试包、36 个 Case 分组全部分类 |
| 静态 | `make check`（`gofmt -l` / `go vet ./...` / `go build`）干净；`GOOS=linux go vet ./tests/unit/testrunner` 通过（覆盖 linux-only 文件） |
| 用例通过 | 9 个分组逐包单跑：92 条 `passed`、7 条 `skipped`、0 失败，见 §8.1 |
| 隔离 | `isolated_cases` 实测 0：本批没有 `DedicatedWorker` 声明 |

### 8.1 逐包运行结果（Windows 11，`--parallel 4`）

| 分组 | 用例 | 通过 | 跳过 | 失败 | 耗时合计 |
|---|---:|---:|---:|---:|---:|
| `unit-testkit` | 1 | 1 | 0 | 0 | 0ms |
| `unit-testrunner` | 1 | 1 | 0 | 0 | 0ms（2 条 linux-only 用例本机不存在） |
| `unit-cigate` | 9 | 9 | 0 | 0 | 0ms |
| `unit-app-planningagent` | 9 | 9 | 0 | 0 | 663ms |
| `unit-image` | 10 | 10 | 0 | 0 | 3ms |
| `unit-environment` | 11 | 11 | 0 | 0 | 0ms |
| `unit-server` | 13 | 6 | 7 | 0 | 75ms |
| `unit-source` | 21 | 21 | 0 | 0 | 0ms |
| `unit-app-planninganalysis` | 24 | 24 | 0 | 0 | 约 1.2s |

- `unit-server` 的 7 条跳过全部来自源用例已有的 `createPathAlias` 跳过路径：本机未开启开发者模式，
  `os.Symlink` 返回 1314（`A required privilege is not held by the client.`），与迁移前 `go test` 在本机
  的表现一致；开启开发者模式的机器与 CI 上会实际执行。没有新增跳过条件。
- `unit-testrunner` 本机只有 1 条：`listener-pids-with-ss` 与 `listener-pids-fallback` 只在 Linux 构建里
  注册（`cases_linux.go`），本机 `--list` 见不到，CI 上应显示 3 条。
- 9 个分组全部 `worker 重启 0 / 独占 0`，`--parallel 4` 下峰值 4、复用正常。

口径订正与遗留：

- §3 原写"388（R5 之后）+ 110 = 498"：基数取自 R5 之前的估算，且把 9 个 `TestMain` 算成了用例。按 R5
  实测（429，含 26 条 unit）与 101 条用例主体，期望值是 530（Linux）。
- 逐包运行在本机（Windows 未开启开发者模式）上完成：`unit-server` 的 7 条符号链接用例按源用例已有条件
  跳过，`unit-testrunner` 的 2 条 linux-only 用例本机没有注册。这两处的实际执行要在 Linux CI 上补齐，
  §8.1 已把差异写明。
- `docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` 的分组数量与 `tests/unit` 包数需要
  同步；按文档约定未在代码编辑期改动，留到 R9 统一收口。
