# V4 R1：清理收尾与基线复验

**状态：** 已完成（2026-11-21）。静态门禁、引用清零、分组核验与注册表基线均按 §4 探针通过；完整
CI 基线仍待推送授权后补一次运行（见 §8）。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R0 盘点与决策冻结](../R0/README.md)。
**下游关系：** 只收尾 R0 的删除动作留下的引用与门禁，不改执行器语义；额度内核在 [R2](../R2/README.md)。

---

## 1. 目标

1. 让"覆盖对账已废弃"这件事在仓库里没有残留：代码注释、文档、README 索引都不再指向已删除的
   `--audit-coverage`、`.steps/V3/**`、`caseinventory`。
2. 复验删除后的门禁仍然可用：`make check`、分组核验、`--list` 基线。
3. 用一次完整 CI 固定基线，供 R2 之后判定"新失败是本次改动引起的"。

## 2. 范围

| 类别 | 文件 |
|---|---|
| 用例注释 | `tests/runner/e2e/cli/case.go`、`tests/runner/integration/local/{codesaver,environment,execenv,session,source,ssh,validation,workflow}/case.go` |
| 执行器注释 | `internal/testrunner/report.go`、`internal/testrunner/resource_contract.go`、`tests/runner/concurrencycheck/doc.go` |
| 其余 `.steps/V3` 引用 | `tests/runner/e2e/cli/cases_cli.go`、`tests/runner/integration/{remote/agent,remote/codesaver,remote/docker,remote/planning,remote/workflow,ssh}/case.go` |
| 文档 | `README.md`、`docs/testing.md`、`tests/README.md`、`.steps/V3-final.md` |
| 门禁 | `.github/workflows/ci.yml`、`.github/scripts/{go-test-groups.sh,summarize-ci-gate.sh}` |

## 3. 任务

1. 全仓引用扫描：`git grep -n 'audit-coverage\|caseinventory\|test-inventory\|RunnerInternal\|steps/V3'`，
   把结果逐项处理到"只剩 Git 历史"。
2. 用例注释改成说明用途（来源包路径只服务串行作用域与报告定位），不引入新概念。
3. `README.md` 的索引表：删除 `.steps/V3/` 行，`V3-final.md` 行注明"阶段文档已归档删除"。
4. `.steps/V3-final.md`：保留正文（V1/V2 同样只留 final 文档），把指向 `.steps/V3/**` 的链接改为纯文本，
   并在文首加一行说明"V3 阶段文档与 R0 冻结件已随 `.steps/V4/R0` 的删除动作移除"。
5. `tests/README.md` 与 `docs/testing.md` 的目录树、Job 表与本机入口逐条核对一遍（不重写结构）。

## 4. 验收探针

| 探针 | 命令 | 期望 |
|---|---|---|
| 静态检查 | `make check` | 通过，且不执行任何测试 |
| 引用清零 | `git grep -n 'audit-coverage\|caseinventory\|test-inventory\|RunnerInternal' -- ':!*.md'` | 无输出 |
| 分组核验 | `bash .github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 退出码 0 |
| 注册表基线 | `./bin/agentflow-test --list` | 360 条 / 25 个分组 |
| 完整 CI | 推送后由 GitHub Actions 执行 | 全部 Job `success`（`真实外部依赖测试` 可跳过） |

## 5. 授权

- 只读/静态动作可直接执行：`gofmt -l`、`go vet`、`go build`、`git grep`、`go-test-groups.sh --check`、`--list`。
- 完整 CI 需要单独授权推送。

## 6. 风险

| 风险 | 处置 |
|---|---|
| 顺手改动用例语义 | 只改注释与文档；评审只看 diff 是否等价 |
| 删链接时误删 V3 结论内容 | `V3-final.md` 只改链接形态，不改任何结论、数字与日期 |
| CI 只跑部分 Job 就宣告基线绿 | 必须一次完整 workflow |

## 7. 不做的事

不搬目录、不改执行器、不改 worker 模型、不迁移任何 unit 用例、不改 CI 的 Job 结构（除已删除的覆盖对账步骤）。

---

## 8. 验收记录（2026-11-21）

探针结果（工作区代码，未推送）：

| 探针 | 命令 | 结果 |
|---|---|---|
| 静态检查 | `make check` | 通过；`gofmt -l` 无输出，`go vet ./...` 与 `go build` 无告警，未执行测试 |
| 引用清零（代码） | `git grep -n 'audit-coverage\|caseinventory\|test-inventory\|RunnerInternal' -- ':!*.md' ':!.steps/**'` | 无输出 |
| 引用清零（`.steps/V3` 路径） | `git grep -n 'steps/V3' -- ':!*.md' ':!.steps/**'` | 无输出；Markdown 侧只剩 `V3-final.md` 正文的历史引用与 V4 阶段文档的记录 |
| 分组核验 | `bash .github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 退出码 0：17 个 go test 测试包、25 个 Case 分组全部分类 |
| 注册表基线 | `make test-runner && ./bin/agentflow-test --list` | 360 条 Case / 25 个分组 |
| 完整 CI | 推送后由 GitHub Actions 执行 | 未执行：需要推送授权 |

本轮实际改动：

1. 9 个用例包的 `register` 注释：删除 `--audit-coverage` 说法，改为"命名只服务可读对照，`Package`
   只服务串行作用域与报告定位"。
2. 执行器注释：`report.go` 的"覆盖对账的唯一输入"改为"运行结果的唯一事实来源"；
   `resource_contract.go` 与 `concurrencycheck/doc.go` 的 `.steps/V3/**` 路径改为纯文本说明。
3. 8 处用例包注释里的 `.steps/V3/R5/README.md` 死链改为 `V3 R5` 纯文本（含 `remote/workflow/cases_m6.go`）。
4. 文档：`README.md` 删除 `.steps/V3/` 索引行并给 `V3-final.md` 行加"阶段文档已归档删除"；
   `docs/testing.md` 的执行器指向改为 `internal/testrunner/` 与 `.steps/V3-final.md`；
   `tests/README.md` 与 `docs/testing.md` 的目录树删除已不存在的 `runner/legacy/`，并订正实测数字：
   17 个 go test 测试包、1352 条顶层 `func Test*`、25 个分组 / 360 条 Case、普通门禁 314 条。
5. `.steps/V3-final.md`：文首加"阶段文档与 R0 冻结件已移除"的说明，6 处阶段文档链接改为纯文本，
   结论、数字与日期未改。
6. 范围表之外的同类残留（经 owner 确认一并清理）：`e2e/cli`、`frontend`、`integration/local/{codesaver,environment,
   execenv,scheduler,session,source,validation,workflow}` 的用例清单注释里"与 R0 冻结清单逐条对账"改为
   "便于按包核对迁移数量"，`frontend/case.go` 的取值来源改为"去掉 `.spec.ts` / `.test.ts` 后缀的文件名"，
   `workflow/cases.go` 去掉对已删除 `tests/runner/legacy` 的指向。
7. `internal/testrunner/resource.go` 与 `resource_contract.go` 里 `case-resource-matrix.json` / "R0 词汇表"
   的说法改为指向仓库内的类型常量表；`.github/scripts/summarize-ci-gate.sh` 的"测试或对账步骤失败"改为
   "测试步骤失败"。
8. 两处与覆盖对账无关、但同样指向不存在文档的残留（经 owner 确认移除）：`internal/httpapi/execution_feedback.go`
   的"判定依据见 `.steps/P4-M2-5-…` §3"整句删除（上一段已说明同一判定理由，属于 V2 收口时登记的
   V1 遗留引用）；`docs/workspace.md` 的 `.steps/` 说明由"V1 收口、V2 收口、V3 规划与收口"改为
   "V1–V3 收口、V4 阶段规划"。

遗留给 R9 收口的数字口径：`unit` 包数与用例数会随 R5–R8 的迁移继续变化，届时按实测重写。
