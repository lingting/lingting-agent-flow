# V3 R8：门禁、CI 与脚本收敛

**状态：** 待执行。
**上游文档：** [路线总规划](../ROADMAP.md)、[R5 迁移](../R5/README.md)、[R6 前端适配器](../R6/README.md)、
[R7 核验收口](../R7/README.md)。
**范围：** 收口 `Makefile` 测试目标、`.github/workflows/ci.yml` 的测试 Job 与 `.github/scripts/` 的逐脚本
归属。不改文档（R9）、不改产品代码。

---

## 1. 目标

1. 开发者入口从 `go test` 切换为统一执行器，单元测试保持 `go test`。
2. 删除 race 门禁（R0 §2.3）与不再有职责的脚本，不保留兼容层。
3. 非测试门禁 `fmt`、`fmt-check`、`vet`、`build`、`web-*`、`check` 保持可用且不触发测试。
4. CI 的测试 Job 与本地命令一一对应，产物与跳过统计仍可汇总。
5. 覆盖对账进入门禁：未覆盖即有理由的 `dropped` 时 CI 失败。

---

## 2. 输入与事实

- 现有 `Makefile`：`test`、`test-unit`、`test-local`、`test-pi`、`test-ssh`、`test-e2e`、`test-race`，
  统一用 `-p $(PARALLEL) -parallel $(PARALLEL)`，`PARALLEL ?= 8`；`check: fmt-check vet build`。
- 现有 CI 测试相关 Job：Go 格式与静态检查、Go 测试包清单与分组核验、Go 单元测试、Go 本机集成测试、
  Go 端到端测试、Go 竞态检测、前端单元与浏览器测试、真实测试授权判断、真实外部依赖测试、CI 汇总。
- `go-test-groups.sh` 是测试分组声明的唯一来源，`--check` 是静态门禁；`run-go-group-tests.sh` 按组执行。
- `make inventory` 目前直接写入 `.steps/V3/R0`；R5/R6 迁出用例后继续这样写会覆盖冻结基线。
- R0 §5 已给出脚本归属初稿，但漏列 `run-real-tests.sh`；本阶段按实际文件清单核对。
- R5 已落地 `--audit-coverage`；R5/R6 的用例包已注册；R7 已删除无引用的 `testkit` 组件。
- 文档同步不在本阶段：`.github/scripts/README.md`、`docs/**`、`README.md` 由 R9 统一更新。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `Makefile` | 测试目标改为统一执行器；删除 `test-race`；保留非测试门禁 |
| `.github/workflows/ci.yml` | 测试 Job 重排；删除竞态 Job；新增覆盖对账与统一产物上传 |
| `.github/scripts/` | 删除/保留/合并后的脚本清单（§5） |
| `go-test-groups.sh` | 表达已迁出包与保留组；`--check` 与 Makefile、CI 保持一致 |

---

## 4. 实施步骤

### 4.1 `Makefile` 目标定稿
| 目标 | 新行为 |
|---|---|
| `test` | `test-unit` + `test-cases`（单元仍 `go test`，其余走统一执行器） |
| `test-unit` | 不变：`go test ./tests/unit/...` |
| `test-cases` | 新增：构建并运行全部已注册 Case |
| `test-local` / `test-e2e` / `test-pi` / `test-ssh` | 改为统一执行器的对应分组；真实分组保留授权门禁 |
| `test-race` | 删除 |
| `check` | 不变：`fmt-check vet build`（不执行测试） |
| `inventory` | 不再覆盖 `legacy-cases.manifest.json`：默认输出到临时目录，R0 冻结基线只读 |

`PARALLEL` 保留为唯一并发旋钮，同时传给 `go test` 与 `agentflow-test --parallel`；不新增第二个变量。

### 4.2 CI Job 重排
| 现有 Job | 处置 |
|---|---|
| Go 格式与静态检查 | 保留，仍运行 `check-go-static.sh` |
| Go 测试包清单与分组核验 | 保留并更新分组声明；新增 `--audit-coverage` 静态核对 |
| Go 单元测试 | 保留，仍走 `run-go-group-tests.sh --group unit` 与跳过汇总 |
| Go 本机集成测试 | 改为统一执行器 `integration/local/...` |
| Go 端到端测试 | 改为统一执行器 `e2e/cli` |
| Go 竞态检测 | 删除（连同 `run-go-race-tests.sh`） |
| 前端单元与浏览器测试 | 改为统一执行器 `frontend` 组 |
| 真实测试授权判断 | 保留 Job，但判定规则只来自 `internal/cigate`，脚本只做转发 |
| 真实外部依赖测试 | 改为统一执行器真实分组，未授权直接失败 |
| CI 汇总 | 更新输入 Job 列表与失败条件 |

### 4.3 产物与汇总
- 统一上传 `<artifact-root>/<run-id>/`，不再由 `collect-test-artifacts.sh` 拼装多个目录。
- 跳过统计改为读取 `report.json` 的 `totals.skipped` 与 `skip_reason`；不再解析 `-v` 文本。
- `summarize-ci-gate.sh` 的输入 Job 名与状态随 §4.2 更新；失败条件不得放宽。

### 4.4 脚本逐文件归属
| 脚本 | 处置 | 责任阶段 |
|---|---|---|
| `go-test-groups.sh` | 保留：分组声明唯一来源；`--check` 继续作为静态门禁，需表达已迁出包 | R8 |
| `run-go-group-tests.sh` | 收敛为只跑 `unit` 组 | R8 |
| `run-real-tests.sh` | 删除：真实分组由统一执行器承载 | R8 |
| `run-go-race-tests.sh` | 删除（race 显式放弃） | R8 |
| `check-real-test-authorization.sh` | 保留为转发入口，规则仍只在 `internal/cigate` | R8 |
| `prepare-real-tests-config.py` | 合并进统一执行器的配置准备入口 | R8 |
| `summarize-real-tests.sh` | 删除：结论来自 `report.json` | R8 |
| `summarize-test-skips.sh` | 删除或改为读取 `report.json`；不保留文本解析 | R8 |
| `collect-test-artifacts.sh` | 删除：统一产物目录 | R8 |
| `run-frontend-tests.sh` | 删除：改由 `frontend` 组调用 | R8 |
| `check-go-static.sh` | 保留（非测试门禁） | — |
| `check-frontend-clean.sh` | 保留（非测试门禁） | — |
| `summarize-ci-gate.sh` | 保留，更新输入 | R8 |
| 发布链路脚本 | 保留，不受本重构影响（清单见本节末） | — |

发布链路脚本（不属于测试门禁，本次不修改）：`build-release-dist.sh`、`github-release.sh`、
`resolve-release-version.sh`、`release-artifacts-summary.py`、`summarize-release-stages.sh`、
`verify-release-assets.sh`。

### 4.5 门禁语义核对
- 覆盖对账 Job 只读文件与注册表，不执行用例；失败条件见 R5 §4.1。
- 真实依赖 Job 未获授权时必须直接失败，不得显示为「通过」或「跳过成功」。
- 单元 Job 与统一执行器 Job 的跳过都进入同一份汇总口径，不得各算一套。

---

## 5. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | `make test` 的组成 | `test-unit` + `test-cases`，保持「一条命令跑全部」 |
| D2 | `PARALLEL` 语义 | 单一变量，同时作用于 `go test` 与调度器 |
| D3 | 真实授权判定来源 | 只保留 `internal/cigate`，脚本只转发 |
| D4 | 跳过统计来源 | 只读 `report.json`，不解析日志文本 |
| D5 | `go-test-groups.sh` 边界 | 继续声明分组；不承担用例注册表职责 |
| D6 | 删除脚本的时机 | 同一次变更内删除，避免两套入口并存 |
| D7 | `make inventory` 的写目标 | 默认写临时目录；覆盖 R0 冻结基线与 manifest 必须显式指定路径 |

---

## 6. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `test-unit`（1342 条）、全部已注册 Case（含 `frontend`）；真实分组单独确认 |
| 允许的命令 | `make test`、`make test-unit`、`make check`、`./bin/agentflow-test --audit-coverage` |
| 允许的 CI 触发 | 经用户确认后推送分支触发一次完整 CI |
| 明确不允许 | 为了让 Job 变绿而放宽断言、跳过对账、跳过真实分组 |
| 理由 | 门禁收敛必须验证本地与 CI 一致；真实设备占用单独授权 |

---

## 7. 验收条件

1. `make test` 覆盖单元与全部已注册 Case；`make check` 不执行任何测试。
2. `test-race` 目标与竞态 Job 已删除；仓库中无 `run-go-race-tests.sh` 残留引用。
3. 覆盖对账作为门禁生效：删除一条注册即失败，登记 `dropped` 后可解释通过。
4. CI 的测试 Job 与本地分组一一对应；跳过统计来自 `report.json`。
5. 真实依赖 Job 未授权时失败，不出现「跳过即通过」。
6. 脚本清单与 §4.4 一致，无孤儿脚本、无第二套产物收集。
7. 一次完整 CI 通过；失败时能定位到具体 Case 与产物。

8. `make inventory` 不再覆盖 `.steps/V3/R0/legacy-cases.manifest.json`；冻结基线可被对账命令读取且保持原样。

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 分组声明与注册表再次漂移 | 静态对账 Job 同时校验分组与注册表 |
| 删除脚本导致某条汇总链路断 | 先替换数据来源，再删除脚本；同一次变更内完成 |
| CI 时长上升 | 监控各 Job 耗时；容量与并行度由 R3 决定，不靠降低覆盖换取速度 |
| 真实 Job 授权判定出现两套 | D3 只保留 `internal/cigate` |
| 本地与 CI 结果不一致 | 本地与 CI 使用同一二进制与同一产物布局 |

---

## 9. 不做的事

- 不修改 `docs/**`、`README.md`、`.github/scripts/README.md`（R9）。
- 不修改产品代码与用例断言。
- 不恢复 race 与覆盖率门禁。
- 不新增第二套测试入口或平行脚本。

---

## 10. 输出给 R9

1. 最终 Makefile 目标与 CI Job 列表。
2. 脚本删除/保留清单与替代关系。
3. 授权判定与跳过统计的唯一来源说明。
4. 一次完整 CI 的结论与产物路径。
