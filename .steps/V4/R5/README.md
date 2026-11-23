# V4 R5：unit 迁移试点（`execenv` + `lifecycle`，28 条）

**状态：** 已完成（2026-11-23）。两个包的 26 条用例已迁入统一执行器并登记，静态探针与授权执行
探针见 §8。
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
| Case 已注册 | `./bin/agentflow-test --list` | 新增 26 条，总数 403 + 26 = 429（本文原写 360 + 28：既忽略了 R2/R3 新增自检，也把 2 个 `TestMain` 算成了用例） |
| 用例通过 | `./bin/agentflow-test unit-execenv unit-lifecycle`（需授权） | 26/26 `passed` |
| 隔离 | 同一 worker 复用时断言成立 | 无串味（记录在本文） |
| 成本 | 时长与 worker 开销对比 | 写回本文，供 R6 判断是否需要调整批量粒度 |
| 分组归类 | `bash .github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend,unit` | 新增 `unit` Case kind 覆盖两组，27 个分组全部分类 |
| 门禁入口 | `.github/workflows/ci.yml` 的 `cases-unit` Job | `--kind unit` 有对应 Job，`ci-summary` 汇总该 Job |

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

## 8. 验收记录（2026-11-23）

### 8.1 落点

| 位置 | 改动 |
|---|---|
| `tests/unit/execenv/execenv_test.go` → `cases_execenv.go` | `git mv`；包名 `execenv_test` → `execenv`，20 条主体改 `run*` |
| `tests/unit/lifecycle/manager_test.go` → `cases_manager.go` | 同上；包名 → `lifecycle`，6 条主体改 `run*` |
| `tests/unit/{execenv,lifecycle}/main_test.go` | 删除：进程级日志、`latest-run.txt` 与默认 `-test.v` 由执行器承担 |
| `tests/unit/{execenv,lifecycle}/case.go` | 包注释、`caseGroup` / `packagePath` 常量与 `register` 辅助 |
| `tests/unit/{execenv,lifecycle}/cases.go` | 26 条用例的注册清单，顺序与源文件一致 |
| `tests/registry/registry.go` | 追加两个空导入 |
| `.github/scripts/go-test-groups.sh` | 新增 `unit` Case kind、两组声明，`normal_case_kinds` 加入 `unit` |
| `.github/workflows/ci.yml` | 新增 `cases-unit` Job、`--declared-case-kinds ...unit`、`ci-summary` 的 needs 与产物变量 |
| `.github/scripts/summarize-ci-gate.sh` | 汇总表新增 `cases-unit` 行 |
| `.github/scripts/run-cases.sh`、`Makefile` | 只更新 kind 列表的说明文字（新增 `unit`） |

### 8.2 迁移清单

`func Test*` 共 20 + 6 = 26 条，加上两个 `TestMain` 才是 28 条源函数；`TestMain` 迁移后删除，因此
迁移后 Case 数量是 26。Case ID 统一为 `unit-execenv/<名称>` 与 `unit-lifecycle/<名称>`。

| 源函数 | Case ID 名称 | DedicatedWorker | 跳过原因 |
|---|---|---|---|
| `TestEnvironmentKindMustBeSupported` | `environment-kind-must-be-supported` | 否 | 无 |
| `TestWorkspaceRejectsSourceInSessionDir` | `workspace-rejects-source-in-session-dir` | 否 | 无 |
| `TestManagementWorkspaceDoesNotContainSource` | `management-workspace-does-not-contain-source` | 否 | 无 |
| `TestEnsureWorkDirRejectsEscapingPaths` | `ensure-work-dir-rejects-escaping-paths` | 否 | 无 |
| `TestExecSpecResolveDefaults` | `exec-spec-resolve-defaults` | 否 | 无 |
| `TestExecSpecRejectsInvalidInput` | `exec-spec-rejects-invalid-input` | 否 | 无 |
| `TestExecResultDistinguishesOutcomes` | `exec-result-distinguishes-outcomes` | 否 | 无 |
| `TestDecideRetentionFollowsLifecyclePolicy` | `decide-retention-follows-lifecycle-policy` | 否 | 无 |
| `TestPreparedRequiresCompleteEnvironment` | `prepared-requires-complete-environment` | 否 | 无 |
| `TestProviderRejectsUnsupportedKind` | `provider-rejects-unsupported-kind` | 否 | 无 |
| `TestPrepareSpecValidation` | `prepare-spec-validation` | 否 | 无 |
| `TestProviderContractIsImplementable` | `provider-contract-is-implementable` | 否 | 无 |
| `TestEnvironmentLabelAndAccessors` | `environment-label-and-accessors` | 否 | 无 |
| `TestContainerRefValidation` | `container-ref-validation` | 否 | 无 |
| `TestPreparedFailedStep` | `prepared-failed-step` | 否 | 无 |
| `TestReadOnlyWorkspaceSetsReadOnlyProjectAccess` | `read-only-workspace-sets-read-only-project-access` | 否 | 无 |
| `TestReadOnlyWorkspaceRequiresWritableSessionDir` | `read-only-workspace-requires-writable-session-dir` | 否 | 无 |
| `TestReadOnlyWorkspaceRejectsOverlappingProjectDir` | `read-only-workspace-rejects-overlapping-project-dir` | 否 | 无 |
| `TestReadOnlyWorkspaceRejectsCaches` | `read-only-workspace-rejects-caches` | 否 | 无 |
| `TestReadOnlyWorkspaceKeepsWorkspaceWritable` | `read-only-workspace-keeps-workspace-writable` | 否 | 无 |
| `TestStopRunsInReverseStartOrder` | `stop-runs-in-reverse-start-order` | 否 | 无 |
| `TestStartFailureStopsOnlyStartedServicesInReverse` | `start-failure-stops-only-started-services-in-reverse` | 否 | 无 |
| `TestStopIsIdempotentAndReportsFailures` | `stop-is-idempotent-and-reports-failures` | 否 | 无 |
| `TestStopTimeoutIsBounded` | `stop-timeout-is-bounded` | 否 | 无 |
| `TestRuntimeFaultIsForwarded` | `runtime-fault-is-forwarded` | 否 | 无 |
| `TestRegisterRejectsInvalidServices` | `register-rejects-invalid-services` | 否 | 无 |

逐条差异说明：

- 26 条全部只改函数头（`run*`）与参数类型（`*testrunner.Context`），`t.Fatalf` / `t.Fatal` 与注释
  逐行保留，`t.Helper` 原样迁移（`newEnvironment` 收 `*testrunner.Context`）。
- 26 处 `t.Parallel()` 全部换成 `Case.Parallel: true`；两包无 `go func`、`sync.Once` 与固定端口，
  并发降级不改变被断言的行为。
- 只有 `read-only-workspace-rejects-overlapping-project-dir` 含 `t.Run`：迁成 `c.Run`，并删除闭包里
  已无意义的 `testCase := testCase` 副本（子测试改为同步执行）。其余表驱动循环保持 Case 内顺序块。
- lifecycle 的 6 条用 `t.Context()`：`testrunner.Context` 提供同名方法，调用点不用改写。
- 没有 `t.Cleanup` / `t.Skipf` / `t.TempDir` / `t.Setenv`，因此本批不验证这些映射（留给 R6–R8）。
- 26 条均不声明 `DedicatedWorker`：用例只碰纯类型、受控替身与本 Case 自己的栈上变量，没有进程级
  可变状态；`isolated_cases` 为 0。

### 8.3 探针结果

| 探针 | 结果 |
|---|---|
| `git ls-files 'tests/unit/{execenv,lifecycle}/*_test.go'` | 空 |
| `./bin/agentflow-test --list` | 429 条 / 27 个分组；`unit-*` 26 条 / 2 个分组 |
| `bash .github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend,unit` | 通过：15 个 go test 测试包、27 个 Case 分组全部分类 |
| `./bin/agentflow-test --parallel 8 unit-execenv unit-lifecycle` | 26/26 `passed`，退出码 0 |
| 失败信息定位 | 在 `runPreparedFailedStep` 临时插入一条 `t.Fatalf` 后运行单条 Case，输出 `tests/unit/execenv/cases_execenv.go:421: R5 失败探针…`，退出码 1；还原并重建二进制后重新跑整批 26/26 |
| 隔离 | 8 个 worker 进程、复用 18 次、重启 0、独占 0，复用后 26 条仍全绿 |
| `gofmt -l tests cmd internal`、`go vet ./...`、`go build ./...` | 干净 |

### 8.4 成本

| 项 | 数值 |
|---|---|
| 26 条总时长（`--parallel 8`） | 293 ms（复跑 311 ms） |
| 用例主体合计 | 51 ms |
| worker 启动 / 复用 | 8 个进程 / 复用 18 次（平均一次启动摊到 3.25 条） |
| 排队 | 合计 3900 ms，最长 230 ms |
| 资源 | 无声明，零残留终检 `not-applicable` |

`go test` 基线未采集（按本次决定）。按本批数据，进程启动开销已被复用稀释，单条 Case 主体耗时在毫秒
级；R6 可以按"一个包一次提交"的粒度继续批量迁移，不需要为 worker 启动开销调整颗粒度。

### 8.5 口径与遗留

- §4 的期望值按实测订正：`--list` 从 403 + 26 = 429（原写 360 + 28），用例数 26 而非 28（28 里有 2 个
  `TestMain`）。
- 新增 `unit` Case kind 与 `cases-unit` Job 超出了 §7"不改 CI 结构"的字面范围，经确认后实施：不新增
  kind 时 `go-test-groups.sh --check`（CI 的 `go-inventory`）会因为两个新分组未归类直接失败；这也是
  R6–R8 批量迁移的前置。
- `docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` 的分组数量、`tests/unit` 包数与
  kind 列表需要同步；按文档约定未在代码编辑期改动。
- `go-unit` / `make test-unit` 仍在跑 `./tests/unit/...`，两个已迁移包显示 `no test files`（不是空转
  门禁，剩余包仍有测试）；R9 统一删除该路径。
- `real`、`resourcecheck`、`pilot` 等分组本阶段未运行（授权只覆盖两个 unit 分组），R-08 的"第一个跑
  真实分组的阶段复核相对路径"继续顺延到 R6。
