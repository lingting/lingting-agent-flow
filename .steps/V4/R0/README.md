# V4 R0：现状盘点与口径冻结

**状态：** 未开始。本文件既是阶段计划，也是 R0 交付物的骨架；执行前不得把 §4 的决策写成"已定稿"。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[V3 收口文档](../../V3-final.md)、
[V3/R10 遗留治理](../../V3/R10/README.md)。
**范围：** 只读盘点当前 HEAD 的用例面、目录引用面与工具口径，冻结 V4 的 10 项决策（D-01…D-10），
产出"每个包的迁移清单"和"每个阶段的验收探针"。不搬目录、不改代码、不改用例。

---

## 1. 目标

1. 用**当前 HEAD** 重新盘点，而不是复制 2026-11-10 审阅意见里的历史数字
   （那份基线是 246 个测试文件 / 1705 个 `Test*`，与现在不同）。
2. 把 V4 需要冻结的接口与口径全部定稿：目录映射、unit 终态、legacy identity、额度语义、
   覆盖对账口径、报告 schema。
3. 给每个阶段留下**可执行的验收探针**（命令 + 期望结论），使后续阶段不必重新讨论"怎样算通过"。
4. 记录盘点时发现的、与 V3 文档不一致的事实，作为勘误输入（不改 `.steps/V3/R0/`）。

## 2. 盘点方法（只读）

允许的命令与输出位置：

| 用途 | 命令要点 | 输出位置 |
|---|---|---|
| 文件面 | `git ls-files 'tests/**'`、`git ls-files '*_test.go'` | 终端（不落盘） |
| `Test*` 与 `testing` API 统计 | `git grep -h -o -E '<pattern>' -- 'tests/unit' \| wc -l` | 终端 |
| 包归属 | `git grep -l 'tests/internal/testkit' -- '*.go' \| sed 's#/[^/]*$##' \| sort \| uniq -c` | 终端 |
| 注册表基线 | `go run ./cmd/agentflow-test --list`（只列出，不执行） | 终端 |
| 冻结清单对账 | `go run ./cmd/agentflow-test --audit-coverage --audit-stage R5,R6 --audit-json <path>` | `.test-artifacts/v4-r0/audit.json`（被忽略） |
| 重新盘点（可选） | `make inventory INVENTORY_DIR=.test-artifacts/v4-r0/inventory` | `.test-artifacts/v4-r0/inventory`（被忽略） |

硬约束：

- **不写 `.steps/V3/R0/`**：冻结清单与资源矩阵是覆盖分母，重新生成会把 365 条迁移面清零。
- 不执行任何用例；`--list` 与 `--audit-coverage` 是只读旁路模式，不属于执行用例。
- 不创建临时脚本；需要统计时用一次性命令并只打印到终端。

## 3. 当前盘点结果（HEAD `465b8bb`，2026-11-21）

### 3.1 用例面

| 项 | 数量 | 备注 |
|---|---:|---|
| `tests/unit/**` 文件 | 154 | 全部 `_test.go`，无普通 `.go` |
| `tests/unit/**` 包 | 16 | 见 §3.3 分布 |
| `func Test*`（含 `TestMain`） | 1348 | `TestMain` 16，实际测试函数 1332 |
| `func Benchmark*` | 0 | — |
| `tests/runner/**` 普通 Go 文件 | 124 | 零 `_test.go` |
| `tests/runner/**` 目录 | 23 | 含 `registry`、`legacy` |
| 已注册 Case / 分组 | 358 / 25 | 普通 312 + real 32 + fault 14 |
| `tests/internal/testkit` 文件 | 16 | 含 `Main` |
| 引用 `tests/internal/testkit` 的 Go 文件 | 120 | unit 84 / runner 34 / 注释 2 |
> 口径来源：HEAD `465b8bb` 相对 R8 收口提交 `172c6a3` 只改文档（`tests/` 无变更），
> 因此 R8/R9 记录的 358 条 Case / 25 个分组对 HEAD 仍然成立；文件数、`Test*` 与 `testing` API
> 计数由 HEAD 实测得出。R0 执行时若 Head 已前进，必须重新统计并更新本表。

### 3.2 `testing` API 调用面（迁移成本）

| API | 调用点 |
|---|---:|
| `t.Fatalf` | 5969 |
| `t.Fatal` | 844 |
| `t.Parallel()` | 1411 |
| `t.Helper()` | 387 |
| `t.Cleanup(...)` | 180 |
| `t.Run(...)` | 158 |
| `t.Errorf` | 98 |
| `t.Logf` | 6 |
| `t.Error` | 1 |
| `t.Skipf` | 1 |
| `t.TempDir()` | 1 |
| `t.Setenv` / `t.Skip` / `t.SkipNow` / `t.Deadline` / `t.Name` / `t.Failed` / `testing.Short()` | 0 |

其它结构事实：`sync.Once` 3 处（均在测试内 fixture 结构体）、`func init()` 0 处、包级 `var` 10 个文件、
`go func(` 65 处、`httptest` 121 处、`testkit.New/Dir` 73 处、`os.Exit` 16 处（全部随 `TestMain` 删除）。

### 3.3 包分布与迁移批次

| 批次 | 包 | `func Test*` | 文件 |
|---|---|---:|---:|
| R3 | `execenv` | 21 | 2 |
| R3 | `lifecycle` | 7 | 2 |
| R4.1 | `testkit` | 2 | 2 |
| R4.1 | `cigate` | 10 | 2 |
| R4.1 | `app/planningagent` | 10 | 3 |
| R4.1 | `image` | 11 | 2 |
| R4.1 | `environment` | 12 | 3 |
| R4.1 | `server` | 14 | 2 |
| R4.1 | `source` | 22 | 3 |
| R4.1 | `app/planninganalysis` | 25 | 5 |
| R4.2 | `container` | 30 | 4 |
| R4.2 | `agent` | 36 | 4 |
| R4.2 | `domain` | 250 | 28 |
| R5.1 | `store` | 249 | 26 |
| R5.2 | `httpapi` | 242 | 25 |
| R5.3 | `app` | 407 | 49 |

合计 1348（含 16 个 `TestMain`）。R0 必须逐包确认"没有包被漏掉"：`tests/unit` 的 16 个目录与上表的
16 个包一一对应，缺一即失败。

### 3.4 工具口径现状

- `internal/caseinventory`：`git ls-files` 通用扫描（不按旧目录过滤）；
  但 `stageForGo` 与 `isIntegrationOrE2E` 仍按 `tests/integration/`、`tests/e2e/` 前缀分类，
  这两个目录已删除，因此**重新生成清单会得到 R5 范围 0 条**。冻结清单必须继续只读使用。
- `internal/testrunner/coverage.go`：按 `Package + LegacyName`（go 条目）或 `LegacyID`（前端条目）
  匹配，天然支持"目录已搬家但 `Package` 保留来源路径"的写法。
- `.github/scripts/go-test-groups.sh`：go test 分组只有 `unit`（`./tests/unit/...`），
  `--check` 要求 `--declared-groups` 非空；R6 删除 unit 分组后必须同步调整这一契约。
- `.github/workflows/ci.yml`：`go-unit` Job 目前是必需 Job，`ci-summary` 依赖它。

## 4. 需要冻结的决策（D-01 … D-10）

逐条给出"建议 / 结论 / 决策人 / 结论日期"。R0 结束时全部必须填满，未填满不得进入 R1。

| 编号 | 决策 | 建议 | 结论 | 决策人 | 日期 |
|---|---|---|---|---|---|
| D-01 | 目录映射（§4.1） | 采用 `tests/testkit` + `tests/*`，删除 `runner` 与 `internal` 两层；unit 目录保留 | 待定稿 | — | — |
| D-02 | unit 严格终态 | 删除 `go test` 执行路径、`TestMain`、`testkit.Main`、`make test-unit`、`go-unit`；不留空转门禁 | 待定稿 | — | — |
| D-03 | legacy identity | `Package` 保留 `tests/unit/<pkg>`，函数名 `run<Name>`，`Register` 自动推导 `LegacyName` | 待定稿 | — | — |
| D-04 | 额度声明权威 | 用例声明为准；CLI 只能收紧；同 key 冲突在注册/预检期硬失败 | 待定稿 | — | — |
| D-05 | work-conserving | I5/I6 必须成立：可派发就派发、被阻塞不挡后面 | 待定稿 | — | — |
| D-06 | 覆盖对账 | 不重新生成清单；新增 unit 审计阶段 + `--audit-package` 包过滤 | 待定稿 | — | — |
| D-07 | 报告 schema | `agentflow-test-report/2`，不保留旧版兼容读路径 | 待定稿 | — | — |
| D-08 | 报告字段 | `limit` / `peak` / `completed` / `queued_ms_total` / `queued_ms_max` / `undispatched` | 待定稿 | — | — |
| D-09 | `go test -c` 过渡 adapter | 默认不用；若用需 owner 批准并写明删除条件（R6 完成即删） | 待定稿 | — | — |
| D-10 | worker 模型 | 一条 Case 一个 worker；开销超基线 2 倍才提批量 worker（需 owner 批准） | 待定稿 | — | — |

## 5. 阶段探针（R0 交付，后续阶段直接复用）
> 注意：下表中 `--package-limit`、`--pool`、`--audit-package` 与 `--audit-stage V4` **当前都不存在**，
> 它们是 R1 / R7 要实现的参数。表中把它们写成探针，说明的是"该阶段以这个命令作为验收探针"，
> 不代表现在可以执行；R0 只能用已有的 `--list` 与 `--audit-coverage --audit-stage R5,R6` 做基线。

| 阶段 | 探针 | 期望 |
|---|---|---|
| R1 | `--list`（含新增自检分组）+ `concurrencycheck` 分组运行 | 358 + 新增条数；17 条既有自检全绿；新额度自检全绿 |
| R1 | `report.json` 的 `concurrency.quotas` | 至少 1 个 package 条目与 1 个 pool 条目，字段齐全 |
| R2 | `go build ./...`、`go vet ./...`、`gofmt -l` | 干净 |
| R2 | `--list`、`--audit-coverage --audit-stage R5,R6` | 25 分组 / 358 条不变；退出码 0（316 + 49、缺失 0、多余 0） |
| R3 | `--audit-coverage --audit-package tests/unit/execenv --audit-package tests/unit/lifecycle` | 28/28，缺失 0 |
| R4 | 每个包一次 `--audit-package tests/unit/<pkg>` | 覆盖 100%，缺失 0 |
| R5 | 每个子阶段一次 `--audit-package tests/unit/{store,httpapi,app}` | 同上 |
| R6 | `git ls-files '*_test.go'` | 空 |
| R6 | `make test-unit` / CI `go-unit` 是否存在 | 都不存在 |
| R7 | `--audit-coverage --audit-stage V4` | 1332 条全覆盖、缺失 0、多余 0、`dropped` 未增加 |
| R8 | `make check`、`make test-cases`、完整 CI | 全绿；`residual_check=passed` |

## 6. 风险与停机点

| 风险 | 处置 |
|---|---|
| 复制历史数字当现状 | 本文 §3 的数字必须由 R0 重新执行的命令产出；与本文不一致时以新命令为准，并更新文档 |
| 手滑重新生成 R0 清单 | `INVENTORY_DIR` 必须显式指向被忽略路径；盘点后核对 `git status` 不含 `.steps/V3/R0` |
| 决策含混 | 每条决策必须有可判定的结论（例如"采用/不采用"），"倾向于""建议后续讨论"不算定稿 |
| worker 开销被忽略 | R3 的测量数字必须写进文档，否则 D-10 无法判定 |
| R0 变成实现阶段 | R0 只出文档与命令结论；遇到需要改代码才能回答的问题，记录为"R1 输入"并停止 |

## 7. 验收条件

1. §3 的数字全部由 R0 自己执行的只读命令产出，且与本文一致（不一致处已在文档中更正）。
2. §4 的 10 条决策全部定稿，且每条有决策人与日期。
3. §5 的探针全部可执行（命令能直接复制运行），且每条给出期望输出。
4. 盘点过程中没有修改 `.steps/V3/R0/`、没有执行任何用例、没有创建临时脚本。
5. 若发现 V3 文档与新事实不符（例如盘点工具口径），已在本文记录并指向 R10 的勘误范围，
   而不是直接改 V3 的历史章节。

## 8. 不做的事

- 不搬目录、不改 import、不改调度器（R1/R2 才做）。
- 不改任何用例，包括不"顺手"把 `t.Fatalf` 换成 `c.Fatalf`。
- 不重新生成冻结清单，不把清单条目改成 `dropped`。
- 不运行测试，不创建测试文件或临时验证脚本。
