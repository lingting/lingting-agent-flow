# V4 R0：现状盘点与口径冻结

**状态：** 已完成（2026-11-21）。盘点数字由当前 HEAD 实测；D-01…D-10 全部定稿；本轮同时落地了"废弃
覆盖对账"的全部删除动作（记录见 §3），残留引用清理留给 [R1](../R1/README.md)。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[V3 收口文档](../../V3-final.md)、[测试规范](../../../docs/testing.md)。
**范围：** 只读盘点 + 决策冻结 + 删除"覆盖对账"链路。不搬目录、不改调度器、不迁移用例。

---

## 1. 盘点结果（HEAD `8a214ad`，实测）

| 项 | 数量 | 口径 |
|---|---:|---|
| `tests/unit/**` 测试文件 | 157 | 全部 `_test.go`，无普通 `.go` |
| `tests/unit/**` 测试包 | 17 | 新增 `tests/unit/testrunner`（`8a214ad`） |
| `func Test*`（含 `TestMain`） | 1352 | `TestMain` 17，实际测试函数 1335 |
| `func Benchmark*` | 0 | — |
| `tests/runner/**` 普通 Go 文件 | 125 | 零 `_test.go` |
| `tests/runner/**` 目录 | 23 | `registry` 空导入 21 行（`legacy` 已删） |
| 已注册 Case / 分组 | 360 / 25 | `--list`：e2e 64 + local 183 + selfcheck 30 + frontend 37 + real 32 + fault 14 |
| `tests/internal/testkit` 文件 | 16 | 含 `Main`（R8.1 删除） |
| 引用 `tests/internal/testkit` 的 Go 文件 | 121 | unit 85 / runner 34 / 注释 2 |

包分布（`func Test*` 含本包 1 个 `TestMain`）：

| 包 | 文件 | `func Test*` |
|---|---:|---:|
| `agent` | 4 | 36 |
| `app` | 49 | 407 |
| `app/planningagent` | 3 | 10 |
| `app/planninganalysis` | 5 | 25 |
| `cigate` | 2 | 10 |
| `container` | 4 | 30 |
| `domain` | 28 | 250 |
| `environment` | 3 | 12 |
| `execenv` | 2 | 21 |
| `httpapi` | 25 | 242 |
| `image` | 2 | 11 |
| `lifecycle` | 2 | 7 |
| `server` | 2 | 14 |
| `source` | 3 | 22 |
| `store` | 26 | 249 |
| `testkit` | 2 | 2 |
| `testrunner` | 3 | 4 |

`testing` API 调用面（`git grep -o` 实测）：

| API | 调用点 | 迁移方式 |
|---|---:|---|
| `t.Fatalf` | 5981 | `c.Fatalf` |
| `t.Fatal(` | 844 | `c.Fatal` |
| `t.Parallel()` | 1411 | `Case.Parallel`（语义不等价，见 ROADMAP R-02） |
| `t.Helper()` | 389 | `c.Helper` |
| `t.Cleanup(` | 180 | `c.Cleanup` |
| `t.Run(` | 164 | `c.Run` |
| `t.Errorf` | 98 | `c.Errorf` + `c.Failed()` |
| `t.Logf` | 6 | `c.Logf` |
| `t.TempDir(` | 3 | `c.TempDir`（返回 error） |
| `t.Skipf` | 2 | `c.Skipf`（带原因） |
| `t.Setenv(` | 2 | 不提供（worker 独占进程，需要时单独决策） |
| `t.Error(` | 1 | `c.Error` |
| `testing.Short()` | 0 | 不支持 |

结构事实：`os.Exit(` 17（全部随 `TestMain` 删除）、`httptest` 121、`go func(` 65、`sync.Once` 3、
`testkit.New`/`testkit.Dir` 73、`func init()` 0。

**废弃前的覆盖对账基线（已随清单删除）**：清单 1707 条；范围 `R5,R6` 共 365 条，覆盖 316 / dropped 49 /
缺失 0 / 多余 0，退出码 0。保留此数字只为说明"删除前状态是干净的"，不再作为任何门禁。

---

## 2. 冻结决策（D-01 … D-10）

决策人：lingting；日期：2026-11-21。

| 编号 | 决策 | 结论 |
|---|---|---|
| D-01 | 目录映射 | **采用**：`tests/testkit` + `tests/*`；删除 `runner` 与 `internal` 两层；`tests/unit/**` 目录保留（只改执行方式） |
| D-02 | unit 严格终态 | **采用**：`tests/unit/**` 不再有 `_test.go`；删 `TestMain`、`testkit.Main`、`make test-unit`、CI `go-unit` Job、`go-test-groups.sh` 的 `unit` 分组；不留"跑 0 条返回 0"的空门禁 |
| D-03 | legacy identity | **取消**：覆盖对账废弃后不再按清单条目对账；`Package` 仍保留来源路径，只服务串行作用域与可读定位，不再推导 `LegacyName` |
| D-04 | 额度声明权威 | **CLI 优先，可临时放大**：运行级 `--package-limit` / `--pool` 存在时以运行级值为生效值（可以大于用例声明），并在报告与日志中 `WARN` 记录"声明值 vs 生效值"；用例之间的同 key 声明冲突仍在注册期硬失败；池名拼错是参数错误；负数 / 非整数退出码 2 |
| D-05 | work-conserving | **采用**：I5 无闲置、I6 无队头阻塞（被额度阻塞的 Case 不得挡住后面可派发的 Case） |
| D-06 | 覆盖对账口径 | **取消**：清单、`--audit-*`、`dropped` 登记、`internal/caseinventory`、`cmd/agentflow-test-inventory`、CI 覆盖对账步骤与 `coverage-audit` 产物一并删除；迁移的完整性证据改为"每包迁移清单 + 用例全绿" |
| D-07 | 报告 schema | **采用**：`schema_version` 升到 `agentflow-test-report/2`，不保留旧版兼容读路径 |
| D-08 | 报告字段 | **采用**：每个 package / pool 记录 `limit`（生效值）、`declared_limit`、`peak`、`completed`、`queued_ms_total`、`queued_ms_max`、`undispatched` 与未派发原因；v2 另加 worker 池字段（`workers`、`worker_reuse`、`worker_restarts`、`isolated_cases`） |
| D-09 | `go test -c` 过渡 adapter | **禁用**，且**以后也不允许出现任何 `go test` 执行路径**（不只是过渡期）；终态 `go test` 只剩"仓库里没有测试文件"这一事实 |
| D-10 | worker 模型 | **批量 worker**（取代"一条 Case 一个 worker"）：① Case 可声明必须独占 worker（例如会关闭服务端 / 端口的用例）；② 一个 worker 进程按顺序执行多条 Case；③ 全部 Case 进队列，由 worker 主动拉取；④ 独占用例优先派发，且在其执行完毕前至少保留一个 worker 继续消费可复用用例 |

D-10 的影响：V3 不变量 4（"一条 Case 一个 worker 子进程"）改写为"一条 Case 独占一次执行；worker 可在
用例之间复用，镜像独占用例必须使用全新 worker 并在用例结束后退出"。新增 worker 不变量 W1–W7 见
[ROADMAP §7](../ROADMAP.md#7-worker-池与批量执行)。

---

## 3. 本轮已落地的改动（D-06 的删除动作）

删除的文件 / 目录：

| 路径 | 原因 |
|---|---|
| `.steps/V3/**` | 按 owner 决定移除 V3 阶段文档与 R0 冻结件（清单、资源矩阵） |
| `internal/testrunner/coverage.go` | 覆盖对账实现（清单解析、dropped 注册、对账报告） |
| `internal/caseinventory/**` | 冻结清单与资源矩阵的生产者 |
| `cmd/agentflow-test-inventory/**` | 盘点工具入口 |
| `tests/runner/legacy/**` | dropped 登记（只为对账存在） |

代码改动：

| 位置 | 改动 |
|---|---|
| `internal/testrunner/case.go` | 删除 `LegacyName` / `LegacyID` / `RunnerInternal` 字段、`legacyNameOf` 与两个正则、注册期校验、`Register` 的自动推导；去掉 `reflect` / `runtime` 导入；`Package` 注释改为只说明串行作用域 |
| `cmd/agentflow-test/main.go` | 删除 `--audit-coverage` / `--audit-manifest` / `--audit-stage` / `--audit-json` 与 `runAuditCoverage` / `parseAuditStages` / `auditArgs` |
| `tests/runner/frontend/case.go` | 删除 `LegacyID`（`playwright:` / `vitest:`）与相关注释 |
| `tests/runner/concurrencycheck/serial.go` | 删除 `RunnerInternal: true` 与注释 |
| `tests/runner/registry/registry.go` | 删除 `tests/runner/legacy` 空导入 |

构建与门禁改动：

| 位置 | 改动 |
|---|---|
| `Makefile` | 删除 `inventory` 目标、`INVENTORY_DIR` 与 `.PHONY` 中对应项 |
| `.github/workflows/ci.yml` | `go-inventory` Job 更名为"测试分组核验"；删除覆盖对账步骤、`coverage-audit` 产物上传、`coverage` 输出与 `GO_INVENTORY_AUDIT_STEP` |
| `.github/scripts/go-test-groups.sh` | 不再写 `coverage=passed` 输出，注释同步 |
| `.github/scripts/summarize-ci-gate.sh` | 删除 `GO_INVENTORY_AUDIT_STEP` 的输入、行字段与报告小节 |
| `.github/scripts/README.md` | 同步上述变化 |

文档改动：`docs/testing.md`（Case 字段表、§9 改为"已移除"、命令与 Job 表）、`docs/workspace.md`
（删盘点工具条目与依赖行）、`docs/development.md`（删 `make inventory`）、`tests/README.md`（删对账命令、
`LegacyName` 说明与 Job 表项）、`README.md`（索引行去掉覆盖对账）。

已做的检查（不含测试）：`gofmt -w cmd internal tests`、`go vet ./...`、`go build ./...` 全部通过；
`grep` 确认 `--audit-coverage` / `caseinventory` / `RunnerInternal` 已无代码引用。

---

## 4. 残留待办（已由 [R1](../R1/README.md) 收尾，此表保留为处理口径）

| 残留 | 位置 | 处理 |
|---|---|---|
| `--audit-coverage` 注释 | `tests/runner/{e2e/cli,integration/local/*}/case.go` 共 9 处 | 改成"来源包路径只服务串行作用域与可读定位" |
| `.steps/V3` 死链 | `README.md`、`docs/testing.md`、`internal/testrunner/resource_contract.go`、`tests/runner/concurrencycheck/doc.go`、`tests/runner/e2e/cli/cases_cli.go`、`tests/runner/integration/{remote/*,ssh}/case.go` | 删除链接或改为纯文本；不再指向已删除目录 |
| `V3-final.md` 内的阶段链接 | `.steps/V3-final.md` | 保留文档本体，把指向 `.steps/V3/**` 的链接改为纯文本并注明"阶段文档已归档删除" |
| `internal/testrunner/report.go` 注释 | "覆盖对账的唯一输入" | 改为"运行结果的唯一事实来源" |
| CI / 脚本一致性 | `ci.yml`、`summarize-ci-gate.sh`、`go-test-groups.sh` | 一次完整 CI 验证（需要推送授权） |

---

## 5. 阶段探针（当前可执行）

| 用途 | 命令 | 期望 |
|---|---|---|
| 注册表基线 | `go build -o bin/agentflow-test ./cmd/agentflow-test && ./bin/agentflow-test --list` | 360 条 Case / 25 个分组 |
| 分组核验 | `bash .github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 退出码 0 |
| 静态检查 | `make check`（fmt + vet + build） | 通过且不执行测试 |
| 仓库无 `_test.go` | `git ls-files '*_test.go'` | R8.1 之后为空 |

`--package-limit` / `--pool` / `--audit-package` 等参数中，只有前者属于 V4 R2 的新增；`--audit-*` 已随
D-06 删除，不再出现在探针里。

---

## 6. 风险与不做的事

| 风险 | 处置 |
|---|---|
| 删除分母后"迁移丢用例"不再有自动证据 | 每个包迁移时在阶段文档登记"包内 `func Test*` 数量 → 迁移后 Case 数量"，并逐包跑该包用例 |
| 历史阶段文档删除后无法回溯 V3 决策 | `V3-final.md` 保留；需要细节时用 Git 历史（`.steps/V3` 在被删提交之前可查） |
| 删代码时漏掉引用 | R0 已用 `go build` / `vet` / `grep` 核对；R1 再做一次全仓引用扫描 |

不做的事：不搬目录、不改调度器、不迁移任何用例、不改 worker 模型（R3 才做）、不运行测试。
