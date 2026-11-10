# V3 R4.5：Go 集成测试单子目录试点

**状态：** 完成。`integration-local-scheduler` 41 条用例连续三次运行全部通过、零残留 `passed`；
同范围 `go test` 对照 41 条全部通过，逐条一致；`go vet ./...` 与 `gofmt -l` 无新增问题。
结论：Case API 够用，迁移成本可控，R5 可以进入批量迁移（附 §9 的一项未覆盖项与 §10 的批次建议）。
**上游文档：** [路线总规划](../ROADMAP.md)、[R4 真实服务进程试点](../R4/README.md)。
**范围：** 只迁移 `tests/integration/local/scheduler`（8 个文件，其中 6 个含用例，共 41 个用例）。
原有 8 个 `_test.go` 在对账运行完成后按试点结论删除，该目录不再有 go test 用例。
不迁移其他包（R5）、不实现前端（R6）、不改 `Makefile` / CI / `docs/`。

---

## 1. 目标

R4 只证明机制可用。本阶段用**既有集成用例**验证四件事，并据此决定 R5 是否进入批量迁移：

1. Case API 是否够用：迁移 41 个用例会暴露缺什么（清理栈、子测试、辅助函数签名）。
2. 迁移成本是否可接受：人工改写的工作量、语义风险与返工比例。
3. 结果是否等价：新执行器与 `go test` 在同一范围内的通过/跳过逐条对齐。
4. 资源声明是否准确：R2 判定 `local/scheduler` 为共享、只需 `sqlite-db`，试点是第一次校验。

试点结论不通过时停止，向用户反馈成本与阻塞项，不擅自扩大或缩小迁移面。

---

## 2. 输入与事实

- 目标包：`tests/integration/local/scheduler`，8 个文件（`main_test.go`、`helpers_test.go` 与 6 个用例文件），
  41 个 `func Test*`，资源只有 SQLite，无真实依赖（R0 §4.1）。
- R0 冻结清单中该包有 41 条用例，是本次试点覆盖对账的分母。
- 该包初判可共享；R0 标记 `concurrency_acceptance_test.go` 含 `sync.Once`，迁移后需人工判定是否依赖时序。
- 现有测试辅助全部以 `*testing.T` 为参数：`testkit.Dir`、`testkit.ConfigPath`、`testkit.Load*Config`、
  `testkit.OpenSQLite` 等，且 `testkit.Main` 承担日志与工作空间初始化。
- R1 的 `Context` 目前没有 `Cleanup`、没有子测试、没有 `Helper`；这三个缺口会在本阶段暴露。
- `t.Parallel()` 在新体系中只映射为调度属性，不再产生父子同步语义。

### 2.1 试点实测事实（本阶段实测）

- **方案 B 成立，且比预估更省。** `Context` 补齐 `Helper`/`Logf`/`Errorf`/`Fatalf`/`Fatal`/`Skipf`/`Skip`/`Cleanup`
  后，把用例主体的唯一参数命名为 `t`，41 个用例体就只需要三类改动：函数头（`func Test*` → `func run*`
  且参数类型改为 `*testrunner.Context`）、删掉 `t.Parallel()`、fixture 构造改为从注入句柄取路径。
  断言、注释、表驱动循环、并发 goroutine 一行都不用改。
- **子测试缺口在试点范围内没有暴露。** 6 个源文件都没有 `t.Run`：表驱动写法是普通 `for` 循环加顺序断言，
  按 D2 原样保留为 Case 内的顺序块。因此试点**没有**新增子测试 API，避免为未出现的能力先写实现。
- **`testkit` 的运行根目录不能带进 worker。** `RunRoot` 是进程级 `sync.Once`，worker 里既没有 `TestMain`
  也没有它需要的语义；新增 `NewAt(t, root)` 与 `PrivateKeyIn(t, dir)` 两个按路径构造的入口后，
  用例工作空间统一取 `Context.WorkspaceDir()`，unit 侧行为不变。
- **"用例自己拼数据库路径"这个假设可以完全去掉。** 38 条用到数据库的用例各自从 `sqlite-db` 句柄的
  `path` 取值，报告里 38 份资源全部 `released` / `verified`，终检 `passed`。
- **并发结论。** 41 条全部 `Parallel: true`，实测峰值 7 / 上限 8、无排队死锁；R0 标记的
  `concurrency_acceptance_test.go` 的 `sync.Once` 是 `scanBarrier` 的字段（每条用例各自新建），
  不是包级状态，迁移后并行安全。
- **唯一需要人工判断的语义差异：** go test 里这 41 条共享同一进程、同一 `testkit` 运行根目录与
  同一个 `test.log`；新体系里每条用例独占 worker 进程与工作空间。逐条核对后没有发现依赖跨用例共享
  状态的断言，因此这是"更严格的隔离"，不是行为变化。

---

## 3. 交付物（已完成）

| 位置 | 内容 |
|---|---|
| `tests/runner/integration/local/scheduler` | 41 条 Case（`cases*.go`）、调度器与隔离现场（`helpers.go`）、登记入口与资源声明（`case.go`）、迁移写法（`doc.go`） |
| `internal/testrunner/context.go` | `Cleanup`（后进先出、清理失败判失败、panic 不吞其余清理）、`Helper` 与失败调用点定位、`Fatal` / `Skip` |
| `internal/testrunner/worker.go` | 用例主体结束（含 panic / `Fatalf` / `Skipf`）后先执行清理，再定型结论 |
| `internal/testrunner/case.go` | `Case.Package` 的语义补充：沿用来源 go test 包路径 |
| `tests/internal/testkit` | 全部辅助改为接收 `Tester` 接口；新增 `NewAt` / `PrivateKeyIn`；`*testing.T` 与 `*testrunner.Context` 共用同一套实现 |
| `tests/runner/registry` | 追加一行导入 |
| 本文 §2.1 / §4 / §6 / §7 / §9 / §10 / §11 | 试点事实、步骤结论、执行记录、验收结果、未覆盖项、R5 输入、用例对账 |

试点报告写在本文件，不另建文件：成本、阻塞项、与 `go test` 的差异都能直接读到。

---

## 4. 实施步骤与结论

### 4.1 补齐 Context 缺口（已完成）

- `Cleanup(func())`：按注册顺序的相反顺序执行；清理失败记为用例失败；清理函数本身 panic 只记为该
  次清理失败，不影响其余清理；用例以失败、跳过或 panic 结束时同样执行。
- `Helper()`：按函数名记录辅助函数，失败定位跳过辅助函数与 `testrunner` 自身栈帧，`Errorf` / `Fatalf`
  输出的消息前缀因此是真正的断言行位置。
- 子测试：试点范围内不需要，见 §2.1，本轮未引入 API。
- 超时路径的边界如实记录：用例超过 `Timeout` 时 worker 交出超时结论退出，编排进程按宽限期终止
  worker 进程树；已注册的清理在"主体仍卡住"时无法安全执行（会与卡住的主体竞争资源），
  这一点由 §9 明示，不由代码假装覆盖。

### 4.2 冻结辅助函数的复用策略（已完成，选 B）

`testkit` 全部辅助改为接收 `Tester` 接口（`Helper` / `Logf` / `Errorf` / `Fatalf` / `Cleanup` / `Skip`）。
`*testing.T` 天然满足，`*testrunner.Context` 也满足，因此 unit 侧的调用点与行为零改动，
集成侧的 41 个用例也不必改写断言。方案 A（全量改签名）与方案 C（另写一套 `casekit`）都没有采用。

### 4.3 迁移顺序（已完成）

1. `main_test.go`：`TestMain` 的职责拆分完毕——日志落盘由 worker 的 stdout/stderr 产物承担，
   最终核验由运行级终检（`residual_check`）承担，用例包里不再有 `TestMain`。
2. `helpers_test.go`：3 个现场辅助在接口方案下直接复用，只改参数类型。
3. 6 个用例文件：按「一个既有 `func Test*` = 一条 Case」逐条迁移，表驱动子测试保留为 Case 内顺序块。

### 4.4 资源声明（已完成，按 §5 D6）

每条用到数据库的用例声明一条 `sqlite-db`（`case-exclusive`），数据库路径只从注入句柄读取；
3 条不使用数据库的用例（并发 Session 路径、归档不跨任务、并发工作区初始化）不声明资源。
没有复用同一个数据库文件，也没有依赖 `testkit` 的运行根目录全局状态。

### 4.5 对照运行（已完成）

同一范围跑了一次 `go test ./tests/integration/local/scheduler/... -count=1` 作为基线：
41 条通过、0 跳过、0 失败。新执行器同集合 41 条通过、0 跳过。逐条比较结论与计数完全一致，
没有需要解释的差异（跳过原因无从比较：两侧都没有跳过）。

### 4.6 成本与阻塞项（已完成）

- 迁移改造点只有 3 类（函数头、`t.Parallel()`、fixture 构造），41 条用例体全部保持原样；
  辅助代码（约 40 个 fixture 方法）只改参数类型。
- 返工 1 次：`register` 的资源参数改为变参后，声明处需要展开（编译期暴露，改一行正则即可）。
- 补 API 2 批：`Context` 的 5 个能力 + 失败定位；`testkit` 的 2 个按路径构造入口。除此之外没有
  为了迁移而改接口。
- 阻塞项：无。
- 语义问题类型只有一类：进程内共享状态（`testkit` 运行根目录、`TestMain`）必须换成编排进程下发
  的工作空间，见 §2.1。

---

## 5. 阶段内冻结的决策

| 编号 | 决策 | 结论 |
|---|---|---|
| D1 | 辅助函数复用策略 | 方案 B：`testkit.Tester` 最小接口 + `Context` 实现；41 条用例验证成立 |
| D2 | 用例与函数的粒度 | 一个既有 `func Test*` = 一条 Case；子测试为 Case 内顺序块（本包无 `t.Run`，不新增子测试 API） |
| D3 | `TestMain` 的替代 | 运行根目录与日志交给 worker 产物；最终核验交给终检；用例包不再有 `TestMain` |
| D4 | 用例包位置 | `tests/runner/integration/local/scheduler`，包名 `scheduler`，非 `_test.go` |
| D5 | 共享结论复核 | 复核结论：该包确实可共享（资源 case-exclusive、无跨用例共享状态）；R2 §7 无需回写 |
| D6 | 数据库资源与路径 | 用 `sqlite-db` 句柄路径，不用 `c.TempDir()` 自建库；不使用数据库的 3 条用例不声明资源 |
| D7 | 用例 ID 与分组 | 分组 `integration-local-scheduler`，ID 为 `<分组>/<原用例名 kebab-case>`；`Case.Package` 沿用来源包路径 `tests/integration/local/scheduler` |
| D8 | 主体参数命名 | 用例主体与辅助函数统一收 `*testrunner.Context`，参数名保持 `t`，使主体与源文件逐行对应 |

---

## 6. 运行授权与执行记录

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `pilot`、`integration-local-scheduler`（新执行器） |
| 允许的一次性对照 | `go test ./tests/integration/local/scheduler/... -count=1`（仅此包，仅此一次，已在删除源文件前完成） |
| 允许的命令 | `go build`、`./bin/agentflow-test --artifact-dir .test-artifacts <集合>`、`go vet`、`gofmt` |
| 明确不允许 | 其他 `tests/integration/**`、`tests/e2e/**`、`tests/unit/**` 的 `go test`；真实 Pi / SSH / 远程 Docker；前端命令 |
| 理由 | 试点验收需要一次真实对照，范围严格限定在目标包；其他用例在 R5 按批次授权 |

```bash
go build -o bin/agentflow.exe ./cmd/agentflow
go build -o bin/agentflow-test.exe ./cmd/agentflow-test
./bin/agentflow-test.exe --artifact-dir .test-artifacts integration-local-scheduler   # 连续 3 次
go test ./tests/integration/local/scheduler/... -count=1
./bin/agentflow-test.exe --artifact-dir .test-artifacts pilot
go vet ./... && gofmt -l cmd internal tests
```

### 6.1 执行记录（Windows 11 + Go 1.25.7）

| 命令 | 退出码 | 观测 |
|---|---|---|
| `integration-local-scheduler`（第 1 次） | 0 | 41 通过 / 0 跳过；峰值 7 / 上限 8；排队合计 16923ms（最长 1008ms）；38 份 `sqlite-db` 全部 `released` + `verified`；零残留 `passed` |
| 同上（连续第 2 次） | 0 | 41 通过；峰值 7；排队合计 15857ms（最长 948ms）；资源与终检同第 1 次 |
| 同上（连续第 3 次） | 0 | 41 通过；峰值 7；排队合计 16013ms（最长 958ms）；资源与终检同第 1 次 |
| `go test ./tests/integration/local/scheduler/... -count=1` | 0 | 41 通过、0 跳过、0 失败（新执行器同集合也是 41 通过、0 跳过） |
| `pilot` | 0 | 通过 1；用例 265ms；零残留 `passed`（回归：本轮改了 `Context` 与 worker 结论定型） |
| `go vet ./...`、`gofmt -l cmd internal tests` | 0 / 空 | 无新增问题 |

三次报告分别在 `.test-artifacts/20261110t144928z-9b76`、`…t144935z-b75c`、`…t144939z-d76a`；
对照基线在新用例包之外完成，随后按试点结论删除 `tests/integration/local/scheduler` 的 8 个 `_test.go`。
本阶段未运行 `selfcheck` / `resourcecheck` / `pilot-fault`（不在 §6 集合内），未触碰真实 Pi / SSH /
远程 Docker，未运行前端命令。

---

## 7. 验收条件与结果

| 条件 | 结果 |
|---|---|
| 1. 41 条用例全部注册，与 R0 清单逐条对齐；不存在无主条目 | 达成：`--list` 显示 41 条，与源文件 41 个 `func Test*` 一一对应（§11） |
| 2. 新执行器与 `go test` 对照结果一致；差异全部有解释与处置 | 达成：两侧都是 41 通过、0 跳过，无差异 |
| 3. 同一集合连续三次运行结论一致，无偶发失败 | 达成：三次全部 `passed`，逐条结论一致 |
| 4. `Context.Cleanup`、子测试记录、失败定位在试点中验证通过；辅助函数复用方案成立 | 部分达成：`Cleanup` 有实测证据（38 份数据库文件被提供者删除并自证，Windows 下必须先关句柄才能删）；辅助函数复用方案成立；子测试记录因本包无 `t.Run` 未引入（§2.1）；失败定位本轮没有触发点（41 条全通过），见 §9 |
| 5. 资源声明准确：无额外共享、无跨用例数据库复用；终检 `passed` | 达成：38 条声明的 `sqlite-db` 全部 case-exclusive，38 份 `released` + `verified`，零残留 `passed` |
| 6. 试点结论文档化：迁移写法、成本、阻塞项、对 R5 的批次建议 | 达成：§2.1、§4、§10、§11 |
| 7. `go vet ./...` 与 `gofmt -l` 无新增问题（除 §6 授权的对照运行外不运行测试） | 达成 |

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| `sync.Once` 与包级状态在单进程内失效 | 已复核：`scanBarrier.once` 是每条用例新建的字段；`testkit` 的运行根目录换成编排进程下发的工作空间 |
| 子测试语义变化导致结论漂移 | 本包没有 `t.Run`，表驱动循环按原顺序执行；出现 `t.Run` 的包在 R5 单独处理 |
| 辅助函数接口方案不成立 | 已验证成立：41 条用例的辅助调用一行未改，unit 侧行为不变 |
| 试点成本被低估 | 实测只改 3 类改造点，无返工（除一次变参展开） |
| 试点通过被误读为「R5 一定可行」 | 结论只覆盖 1 个包 41 条用例；R5 按批次继续验证 |
| 用例失败时无法定位 | 失败定位按辅助函数栈帧跳过；本轮无失败，未实测（§9） |

---

## 9. 已知差异与未覆盖面

1. **失败定位没有实测样本。** 41 条用例全部通过，`Context.Fatalf` 的调用点输出没有触发点。
   机制已实现（`Helper` 记录辅助函数名，`recordFailure` 跳过辅助函数与 `testrunner` 栈帧），
   R5 迁移的失败用例会覆盖它；如需提前验证，跑一条刻意失败的用例即可（不在本轮授权集合内）。
2. **超时路径不执行已注册清理。** 用例超过 `Timeout` 时 worker 交出超时结论退出，清理只能由编排进程
   终止进程树兜底；在主体仍卡住时并发执行清理会与主体争用资源，因此不做。已在 §4.1 明示。
3. **子测试 API 未引入。** 本包无 `t.Run`，按 D2 保持顺序块；出现 `t.Run` 的包在 R5 评估是否需要
   `Context.Run` 与子测试条目对账。
4. **旧用例已删除。** `tests/integration/local/scheduler` 不再有 go test 用例，若要复跑基线只能借助版本库
   历史；这是"试点通过后删除"的决定带来的正常代价。
5. **`selfcheck` / `resourcecheck` / `pilot-fault` 本轮未复跑。** 不在 §6 授权集合内；其中
   `selfcheck-fault` 的用例是本轮 `Context` 结论定型逻辑的直接覆盖点，如需补齐应单独授权。

文档同步待办（R4.5 不改 `docs/`，留给 R9）：`docs/testing.md` §4 与 R2 §4 描述的资源目录已变为
`<工作空间根>/shard_<type>_<id>/`，用例工作空间根移到 `os.TempDir()` 下；`docs/testing.md` 需要补
"现有辅助函数的参数类型是 `testkit.Tester`，不是 `*testing.T`"。

---

## 10. 输出给 R5

1. **冻结的迁移写法**（`tests/runner/integration/local/scheduler/doc.go`）：`register(name, run, decls...)`
   登记、`func run*(t *testrunner.Context)` 主体、`t.Parallel()` 换成 `Case.Parallel`、
   `Case.Package` 沿用来源包路径、资源只声明真正使用的类型。
2. **辅助函数复用方案**：`testkit.Tester` 是唯一接口；需要"按路径构造"时用 `testkit.NewAt` /
   `testkit.PrivateKeyIn`，不要退回 `RunRoot` / `Dir`。
3. **成本与返工**：单包 41 条、约 40 个辅助方法，改造点 3 类，返工 1 次，补 API 2 批；阻塞项为 0。
4. **批次建议**：按 R0 §4.1 的 18 包逐包推进，每包仍按「一个包一次对照」的方式验证；先做
   无真实依赖的 `shared-candidate` 包（`local/codesaver`、`local/source`、`local/ssh`、
   `local/validation`、`tests/e2e/cli`），再做 `exclusive-required` 与真实依赖包。
5. **需要提前补的能力**：`t.Run` 子测试（出现时再补，不预置）；失败定位在真实失败用例上复核一次；
   `--audit-coverage` 命令（R5 落地，本轮只用人工对账）。
6. **`Case.Package` 的口径**：沿用来源包路径，R2 冻结的包级串行表因此在 R5 里不需要改键；若后续改成
   迁移后路径，必须同步改表，不要两套并存。

---

## 11. 用例清单与对账

41 条用例与源文件逐条对应：`func TestXxx` → `runXxx`（主体）→ `<分组>/<kebab-case>`（Case ID）。

```text
scheduler_test.go（11）
  TestSchedulerDispatchesInQueueOrder              → scheduler-dispatches-in-queue-order
  TestSchedulerRespectsConcurrencyLimit            → scheduler-respects-concurrency-limit
  TestSchedulerResumesAfterDispatchCompletes       → scheduler-resumes-after-dispatch-completes
  TestSchedulerReleasesExecutionOnLaunchFailure    → scheduler-releases-execution-on-launch-failure
  TestSchedulerStopsWithoutDispatching             → scheduler-stops-without-dispatching
  TestSchedulerSkipsFailedAndPausedTasks           → scheduler-skips-failed-and-paused-tasks
  TestSchedulerClaimEventIsTraceable               → scheduler-claim-event-is-traceable
  TestSchedulerCountsExistingActiveTasks           → scheduler-counts-existing-active-tasks
  TestSchedulerSkipsTaskWithActiveWorkflowRun      → scheduler-skips-task-with-active-workflow-run
  TestSchedulerRequiresPositiveLimit               → scheduler-requires-positive-limit
  TestSchedulerStatusReportsLimit                  → scheduler-status-reports-limit

requirement_delete_test.go（3）
  TestSchedulerSkipsTasksOfDeletedRequirement      → scheduler-skips-tasks-of-deleted-requirement
  TestDeleteRejectsWhileSchedulerDispatchInFlight  → delete-rejects-while-scheduler-dispatch-in-flight
  TestConcurrentDeleteAndSchedulerScanKeepFacts    → concurrent-delete-and-scheduler-scan-keep-facts

retry_test.go（9）
  TestRetryFailedTaskStartsNewCycle                → retry-failed-task-starts-new-cycle
  TestRetryRejectsNonFailedTask                    → retry-rejects-non-failed-task
  TestRetryRejectsPendingSideEffect                → retry-rejects-pending-side-effect
  TestRetryRejectsRecoveryResidue                  → retry-rejects-recovery-residue
  TestRetryReleasesExecutionOnLaunchFailure        → retry-releases-execution-on-launch-failure
  TestRetryConcurrentOnlyOneWins                   → retry-concurrent-only-one-wins
  TestRetryEligibilityIsReadOnly                   → retry-eligibility-is-read-only
  TestRetryKeepsHistoricalRecords                  → retry-keeps-historical-records
  TestRetryDoesNotFakeSuccessOnDatabaseError       → retry-does-not-fake-success-on-database-error

concurrency_acceptance_test.go（5）
  TestSchedulerDoesNotExceedLimitUnderConcurrentScans → scheduler-does-not-exceed-limit-under-concurrent-scans
  TestSchedulerReadFailureDoesNotFakeEmptyQueue    → scheduler-read-failure-does-not-fake-empty-queue
  TestSchedulerLaunchFailureReleasesOnlyOwnExecution → scheduler-launch-failure-releases-only-own-execution
  TestCancelAndAdvanceRaceKeepsCancelIntent        → cancel-and-advance-race-keeps-cancel-intent
  TestRetryRacesWithSchedulerForSameTask           → retry-races-with-scheduler-for-same-task

isolation_test.go（6）
  TestTwoTasksUseIndependentContainers             → two-tasks-use-independent-containers
  TestTaskContainerCannotBeReusedByAnotherTask     → task-container-cannot-be-reused-by-another-task
  TestWorkspaceInitRecordsDoNotCrossTasks          → workspace-init-records-do-not-cross-tasks
  TestConcurrentSessionPathsDoNotCollide           → concurrent-session-paths-do-not-collide（无资源）
  TestArchivingOneTaskKeepsAnotherTaskFiles        → archiving-one-task-keeps-another-task-files（无资源）
  TestSessionScopeRejectsInconsistentOwnership     → session-scope-rejects-inconsistent-ownership

resource_acceptance_test.go（7）
  TestRecoveringOneTaskKeepsAnotherTaskRecords     → recovering-one-task-keeps-another-task-records
  TestConcurrentContainerRecoveryKeepsOwnership    → concurrent-container-recovery-keeps-ownership
  TestOwnershipMismatchBlocksAllDestructivePaths   → ownership-mismatch-blocks-all-destructive-paths
  TestOrphanContainerKeepsDiagnosticEvidence       → orphan-container-keeps-diagnostic-evidence
  TestUnknownContainerKeepsEvidenceWhenUnreachable → unknown-container-keeps-evidence-when-unreachable
  TestReclaimPartialFailureKeepsRemainingScene     → reclaim-partial-failure-keeps-remaining-scene
  TestConcurrentWorkspaceInitDoesNotCrossTasks     → concurrent-workspace-init-does-not-cross-tasks（无资源）
```

---

## 12. 不做的事

- 不迁移 `local/scheduler` 以外的任何包（R5）。
- 不删除 `testkit` 的入口与最终核验（R7）。
- 不实现前端适配器（R6）。
- 不修改 `Makefile`、CI 与文档（R8 / R9）。
- 不实现 `--audit-coverage` 命令，只用一次性人工对账，命令在 R5 落地。
