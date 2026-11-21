# V4 R9：删除 `go test` 执行路径与收口验收

**状态：** 未开始。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R8 批量 C](../R8/README.md)。
**下游关系：** 本阶段结束后 V4 收口，产出 [`.steps/V4-final.md`](../../V4-final.md)。

---

## 1. 目标

终态三条：仓库内不再有任何 `_test.go`；`go test` 不再是任何用例的执行入口（也不再以"跑 0 条返回 0"的
形式存在）；`agentflow-test` 是包含 unit 面在内的唯一执行入口。

## 2. R9.1 删除 `go test` 执行路径

| 对象 | 处置 |
|---|---|
| 17 个 `main_test.go` | 应已随各包迁移删除；本阶段只核对 `git ls-files '*_test.go'` 为空 |
| `tests/testkit` 的 `Main` | 删除 `Main`、运行根目录逻辑与 `latest-run.txt` 写入 |
| `Makefile` | 删除 `test-unit` 目标；`test` 目标改为只跑 Case；`.PHONY` 同步 |
| `go-test-groups.sh` | 删除 `unit` 分组：`go_test_groups` / `group_packages` / `group_make_target` 改为"没有 go test 分组"；`--check` 在"无 go test 分组"语义下仍然要求分组声明与 CI / Makefile 一致 |
| `run-go-group-tests.sh` | 删除该脚本及其在 `.github/scripts/README.md` 的条目 |
| `ci.yml` | 删除 `go-unit` Job 与 `ci-summary` 对它的依赖、`GO_UNIT_*` 环境变量 |
| `summarize-ci-gate.sh` | 删除 `GO_UNIT_RESULT` / `GO_UNIT_STEP` 的输入、行与报告小节 |
| `check-go-static.sh` | 确认只做 fmt / vet / build，不隐式跑测试 |

## 3. R9.2 门禁 / CI / 脚本收敛

1. 本地入口：`make test`（全部普通 Case）、`make test-cases TEST_KIND=...`、`make test-real`、
   `make check`。删除不再存在的入口与文档描述。
2. 分组核验：`--declared-groups` 与 `--declared-case-kinds` 的语义按新分组清单校正；分组遗漏仍必须失败。
3. 报告与产物：`report.json` 的 `schema_version` 为 `agentflow-test-report/2`；额度分节与 worker 分节都在
   CI 产物里可见。
4. 完整 CI 一次通过（需要授权推送）。

## 4. R9.3 文档与全量验收

| 文档 | 要求 |
|---|---|
| `docs/testing.md` | 只描述一个执行入口；删除"unit 仍由 go test 执行"、`TestMain`、`testkit.Main` 的描述；§9 覆盖对账保持"已移除" |
| `docs/workspace.md` | 目录树与依赖方向与新结构一致 |
| `docs/development.md` | 常用命令与实际 Makefile 目标一致 |
| `tests/README.md` | 入口、目录树、Job 对照表与新结构一致 |
| `.github/scripts/README.md` | 脚本清单与实际脚本一致 |
| `README.md` | 索引与命令一致 |
| `.steps/V4-final.md` | 按 `V3-final.md` 结构写最终状态 |

全量验收（逐项授权）：普通 Case 全绿；unit 面（现已是 Case）全绿且报告 `totals` 与迁移前逐包对得上；
真实设备分组按授权单独一次；一次完整 CI 通过；`make check` 通过且不执行测试。

## 5. 验收探针

| 探针 | 期望 |
|---|---|
| `git ls-files '*_test.go'` | 空 |
| `go test ./...` | 不执行任何用例（"仓库里没有测试文件"的必然结果，不是门禁） |
| `make test-unit` | 不存在（`make` 报无此目标） |
| CI | 没有 `go-unit` Job；`ci-summary` 不引用它 |
| `./bin/agentflow-test --list` | 1712 条（含 unit 面）/ 分组数与 R8 一致 |
| `report.json` | `schema_version=agentflow-test-report/2`，含 quota 与 worker 分节 |

## 6. 风险

| 风险 | 处置 |
|---|---|
| 删除 `go-unit` 后 unit 面短期无人执行 | 必须在 R8 全部完成后再删，并在删之前确认真实运行过全部 unit Case |
| "跑 0 条返回 0"的空门禁 | 终态验收明确写出"`go test` 不是门禁"，并以 `git ls-files '*_test.go'` 为空作为证据 |
| 文档仍描述旧入口 | R9.3 逐文件核对；文档只写已落地能力 |

## 7. 不做的事

不恢复 race/覆盖率、不改调度与 worker 模型、不为旧目录或旧入口保留兼容层。
