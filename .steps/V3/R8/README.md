# V3 R8：门禁、CI 与脚本收敛

**状态：** 已收口。本机验收 1–6 与 8 达成；验收 7 的「一次完整 CI 通过」需要推送分支触发，
本阶段只在本机逐项验证了 Job 对应的本地入口，CI 实际运行待推送后确认。
**上游文档：** [路线总规划](../ROADMAP.md)、[R5 迁移](../R5/README.md)、[R6 前端适配器](../R6/README.md)、
[R7 核验收口](../R7/README.md)。
**范围：** 收口 `Makefile` 测试目标、`.github/workflows/ci.yml` 的测试 Job 与 `.github/scripts/` 的逐脚本
归属。不改文档（R9）、不改产品代码行为。

---

## 1. 目标

1. 开发者入口从 `go test` 切换为统一执行器，单元测试保持 `go test`。
2. 删除 race 门禁（R0 §2.3）与不再有职责的脚本，不保留兼容层。
3. 非测试门禁 `fmt`、`fmt-check`、`vet`、`build`、`web-*`、`check` 保持可用且不触发测试。
4. CI 的测试 Job 与本地命令一一对应，产物与跳过统计仍可汇总。
5. 覆盖对账进入门禁：未覆盖即有理由的 `dropped` 时 CI 失败。

---

## 2. 输入与事实

- 开工时的 `Makefile`：`test`、`test-unit`、`test-ssh`（指向 R5 已删除的 `tests/integration/ssh`）、
  `test-e2e`（指向已删除的 `tests/e2e`）、`test-race`，统一用 `-p $(PARALLEL) -parallel $(PARALLEL)`。
- 开工时的 CI：Go 格式与静态检查、测试包清单与分组核验、单元测试、端到端测试、竞态检测、
  前端 `pnpm test:all:safe`、真实测试授权判断、真实外部依赖测试、CI 汇总。`go-test-groups.sh`
  只声明 `unit` 与 `e2e` 两个普通分组，`tests/integration/local/**` 在 CI 里没有执行者。
- R5 删除 `tests/integration/**`、`tests/e2e/**` 后，`make test-ssh`、`make test-e2e` 与
  `run-go-race-tests.sh` 的三条 `go test` 命令全部指向不存在的目录；`run-frontend-tests.sh`
  依赖 R6 已移除的 Playwright `webServer` 自启 dev server，同样跑不起来。
- 执行器现有能力（R1–R7 冻结）：`--list`、`--parallel`、`--timeout`、`--real-tests auto|on|off`、
  `--audit-coverage`（含 `--audit-stage`、`--audit-json`），选择条件只有分组名、精确 ID 与
  `前缀...` 三种写法，没有排除语法。
- 已注册 Case 分成 25 个分组：`integration-local-*`（9）、`e2e-cli`、`frontend`、
  执行器自检 4 个（`selfcheck`/`resourcecheck`/`concurrencycheck`/`pilot`）、
  真实设备 6 个（`integration-ssh`、`integration-remote-*`）、故障注入 4 个（`*-fault`）。
- `make inventory` 原先直接写 `.steps/V3/R0`，而 `legacy-cases.manifest.json` 是冻结基线。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `Makefile` | 测试目标改为统一执行器；删除 race 与两个失效目标；`inventory` 默认不再覆盖冻结基线 |
| `.github/workflows/ci.yml` | 测试 Job 重排；删除竞态 Job；覆盖对账进 go-inventory；产物按 Job 目录上传 |
| `.github/scripts/` | 脚本由 19 个收敛到 13 个（含 6 个发布链路脚本），清单见 §4.5 |
| `go-test-groups.sh` | 唯一的分组声明来源：go test 分组 + Case kind，核验直接对注册表与 Makefile |
| `run-cases.sh` | Case 执行的唯一入口（Makefile 与全部 Case Job 共用） |
| `cmd/agentflow-test`、`internal/testrunner/realconfig.go` | 真实测试配置准备入口（`--prepare-real-config`） |

---

## 4. 实施步骤与结果

### 4.1 `Makefile` 目标（已完成）

| 目标 | 行为 |
|---|---|
| `test` | `test-unit` + `test-cases` |
| `test-unit` | 不变：`go test ./tests/unit/...`，并行度仍由 `PARALLEL` 决定 |
| `test-runner` | 构建 `bin/agentflow-test`；worker 由该二进制重新启动自身，必须是最新构建 |
| `test-cases` | `run-cases.sh --kind $(TEST_KIND)`，`TEST_KIND` 默认 `normal`，可取 `local`/`e2e`/`selfcheck`/`frontend` |
| `test-real` | `run-cases.sh --kind real --parallel 1`，未授权时脚本直接失败 |
| `test-ssh` / `test-e2e` / `test-race` | 删除：前两者指向 R5 已删除的目录，`test-race` 与 R0 §3.3 的显式放弃一致 |
| `inventory` | 默认写 `INVENTORY_DIR`（`.test-artifacts/inventory`）；覆盖 R0 冻结基线必须显式传 `INVENTORY_DIR=.steps/V3/R0` |
| `check` | 不变：`fmt-check vet build`，不执行任何测试 |

`PARALLEL` 仍是唯一的并发旋钮，同时作用于 `go test` 与执行器 `--parallel`；没有新增第二个变量。

### 4.2 CI Job（已完成）

| Job | 内容 | 对应的本地入口 |
|---|---|---|
| `go-static` | `check-go-static.sh`（gofmt 门禁、`go vet`、`go build`） | `make check` |
| `go-inventory` | ① `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend`；② `agentflow-test --audit-coverage --audit-stage R5,R6` | 两条命令原样执行 |
| `go-unit` | `run-go-group-tests.sh --group unit`（仍是 `go test`） | `make test-unit` |
| `cases-local` | `run-cases.sh --kind local --parallel 4` | `make test-cases TEST_KIND=local` |
| `cases-e2e` | `run-cases.sh --kind e2e --parallel 4` | `make test-cases TEST_KIND=e2e` |
| `cases-selfcheck` | `run-cases.sh --kind selfcheck --parallel 4` | `make test-cases TEST_KIND=selfcheck` |
| `ui-test` | `run-cases.sh --kind frontend` + `pnpm check` / `pnpm build` / `check-frontend-clean.sh` / `make web-verify` | `make test-cases TEST_KIND=frontend` + 四个非测试门禁 |
| `real-tests-gate` | `check-real-test-authorization.sh`（规则只在 `internal/cigate`） | 不适用 |
| `real-tests` | `agentflow-test --prepare-real-config` → `run-cases.sh --kind real --parallel 1` | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real` |

删除的 Job：Go 竞态检测（`run-go-race-tests.sh` 一并删除）。故障注入分组（`*-fault`）不进绿色门禁：
它们按设计注入失败并以非零退出码结束，需要时按 R2/R3 记录的逐条命令单独执行。

### 4.3 产物与汇总（已完成）

- 每个 Case Job 只上传自己的产物根目录 `<workspace>/.test-artifacts/<job>/`，其中只有
  `<run-id>/`（`report.json`、worker 日志、`cases/<case>/`）。测试工作空间仍由执行器放在系统
  临时目录（R7 D3），因此上传内容里没有数据库、Session 与临时私钥。
- `go-unit` 只上传 `*-output.log` 与 `*-summary.txt`：它的测试工作空间在 `TMPDIR` 下，按
  `docs/testing.md` §4 保留在工作区，不进制品。
- 跳过统计：Case Job 只读 `report.json` 的 `totals.skipped` 与每条 `skip_reason`（D4），
  由 `run-cases.sh` 渲染；单元组的跳过数仍来自 `go test` 的顶层 `--- SKIP:` 行，两者写进同一份
  Job 摘要，口径各自标注。
- `summarize-ci-gate.sh` 的输入 Job 列表随 §4.2 更新，失败条件没有放宽；前端非测试门禁作为
  独立一行进入汇总。

### 4.4 真实依赖授权与配置（已完成）

- 授权判定不变：唯一变量 `AGENTFLOW_REAL_TESTS`，规则只在 `internal/cigate`，
  `check-real-test-authorization.sh` 只做转发。
- `run-cases.sh` 对 `--kind real` 强制要求 `AGENTFLOW_REAL_TESTS_GRANTED=true`，缺失时以退出码 1
  失败（不是跳过、不是成功）；普通 kind 一律传 `--real-tests off`，普通门禁不可能选中或执行
  真实分组。
- 配置准备从 `.github/scripts/prepare-real-tests-config.py` 收回执行器：
  `agentflow-test --prepare-real-config` 按同一组环境变量生成 `tests/config/{ssh,pi,docker,git}.yml`
  与 `RUNNER_TEMP/agentflow-real-tests/` 下的私钥与 known_hosts（0600），逐行输出
  `::add-mask::`，把 `configured=true|false` 写进 `GITHUB_OUTPUT`，结论写进 Job 摘要；缺少必需
  Secret 时不写任何文件、退出码 0，由工作流把用例步骤标为跳过。生成内容的引号、默认镜像
  （`agentflow-test-agent:1.0.0`）与默认模型（`group/free`）与原脚本一致。

### 4.5 脚本逐文件归属（已完成）

| 脚本 | 处置 |
|---|---|
| `go-test-groups.sh` | 保留并重写：go test 分组（只有 `unit`）+ Case kind 声明与核验 |
| `run-cases.sh` | 新增：Case 执行的唯一入口 |
| `run-go-group-tests.sh` | 收敛为只跑 `unit` 组，不再认识 Case 分组 |
| `check-real-test-authorization.sh` | 保留：只转发到 `cmd/agentflow-ci` |
| `summarize-ci-gate.sh` | 保留并按 §4.2 更新输入 |
| `check-go-static.sh`、`check-frontend-clean.sh` | 保留（非测试门禁） |
| `run-real-tests.sh` | 删除：真实分组由 `run-cases.sh --kind real` 承担 |
| `run-go-race-tests.sh` | 删除（race 显式放弃，R0 §2.3） |
| `run-frontend-tests.sh` | 删除：前端用例改由 `run-cases.sh --kind frontend` 执行 |
| `summarize-real-tests.sh` | 删除：授权模式与跳过原因来自 `report.json` |
| `summarize-test-skips.sh` | 删除：不再解析 `test.log` 文本 |
| `collect-test-artifacts.sh` | 删除：产物目录本身可上传，没有第二套收集 |
| `prepare-real-tests-config.py` | 删除：合并进 `agentflow-test --prepare-real-config` |
| 发布链路脚本（`build-release-dist.sh`、`github-release.sh`、`resolve-release-version.sh`、`release-artifacts-summary.py`、`summarize-release-stages.sh`、`verify-release-assets.sh`） | 不动 |

前端入口：`lingting-agent-flow-ui/package.json` 删除 `test:e2e:safe`、`test:safe` 与 `test:all:safe`
（三者都依赖 R6 已移除的 Playwright `webServer`，保留只会留下第二条跑不通的入口），`test` 与
`test:unit:safe` 作为开发者本机单测入口保留，CI 不再调用它们。

### 4.6 门禁语义核对（已完成）

- `go-test-groups.sh --check` 直接对真实清单核验：`go list` 的 16 个测试包必须全部落在 `unit`；
  `agentflow-test --list` 的 25 个 Case 分组必须各命中一条声明；声明必须命中至少一个分组；
  工作流声明的 go test 分组与普通 kind 必须与清单完全一致；`test-unit` 的包模式、`test-cases`
  与 `test-real` 的配方（必须调用 `run-cases.sh`、不得硬编码分组名）一并核验。
- 覆盖对账只读冻结清单与注册表：缺失条目、无法解释的多余注册与结构性问题都让 Job 失败，
  显式声明 `dropped` 的条目按理由通过。
- 真实依赖 Job 缺授权时 `run-cases.sh` 直接失败；`configured=false` 时用例步骤被跳过并在摘要
  与汇总里标注「不计为通过」。

---

## 5. 阶段内冻结的决策

| 编号 | 决策 | 定稿结论 |
|---|---|---|
| D1 | `make test` 的组成 | `test-unit` + `test-cases`；`test-cases` 覆盖全部普通 Case（local + e2e + selfcheck + frontend）。真实分组与故障注入分组不在内：前者需单独授权，后者按设计必定非零退出 |
| D2 | `PARALLEL` 语义 | 单一变量，同时作用于 `go test` 与执行器 `--parallel`；不新增第二个并发变量 |
| D3 | 真实授权判定来源 | 只保留 `internal/cigate`；`run-cases.sh` 只做"缺少授权即失败"的守卫 |
| D4 | 跳过统计来源 | Case Job 只读 `report.json`（`totals.skipped` 与 `skip_reason`）；`go test` 单元组的跳过数只能来自它自己的结论行，两者分开标注 |
| D5 | `go-test-groups.sh` 边界 | 继续做分组声明与静态核验；不承担用例注册表职责（注册表仍以 `--list` 为准） |
| D6 | 删除脚本的时机 | 同一次变更内删除；新入口（`run-cases.sh`、`--prepare-real-config`）先落地，再删旧脚本 |
| D7 | `make inventory` 的写目标 | 默认写 `.test-artifacts/inventory`；覆盖 R0 冻结基线与 manifest 必须显式传 `INVENTORY_DIR` |
| D8 | `prepare-real-tests-config.py` 的去向 | 合并进执行器（`--prepare-real-config`）并删除脚本：配置准备与执行使用同一份契约；这是本阶段唯一新增的代码路径，行为与原脚本逐项对齐 |
| D9 | 故障注入分组的位置 | `*-fault` 单独归类为 `fault` kind，不进绿色门禁也不进 `make test`；核验脚本拒绝把 `real`/`fault` 声明为普通门禁执行 |

---

## 6. 运行授权与执行记录

授权（本阶段一次生效）：`make test-unit`、全部已注册的普通 Case（含 `frontend`）、
`--audit-coverage`、`make check`。真实设备分组未运行；未授权改断言让结果通过。

| 批次 | 命令要点 | 结论 |
|---|---|---|
| 静态门禁 | `make check` | gofmt / vet / build 全部通过，无测试执行 |
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 16 个测试包 + 25 个 Case 分组全部分类，无遗漏、无重复、无落空声明 |
| 分组核验反例 | 少声明 `selfcheck,frontend`；把 `real` 当普通 kind 声明 | 两次都以退出码 1 失败并指出不一致 |
| 单元 | `make test-unit` | 16 个包全部 ok：顶层通过 1322 / 失败 0 / 跳过 7（既有 Windows 符号链接权限跳过） |
| 自检用例 | `make test-cases TEST_KIND=selfcheck` | 28 条：通过 27 + 跳过 1（`selfcheck/skip`）；零残留 passed |
| E2E | `make test-cases TEST_KIND=e2e` | 64 条全部通过；零残留 passed |
| 本机集成 | `make test-cases TEST_KIND=local` | 183 条全部通过；零残留 passed |
| 前端 | `PARALLEL=4 make test-cases TEST_KIND=frontend` | 37 条全部通过；零残留 passed |
| 普通全量 | `make test-cases`（`normal`：local + e2e + selfcheck + frontend） | 312 条：通过 311 + 跳过 1（`selfcheck/skip`）；零残留 passed |
| 覆盖对账 | `agentflow-test --audit-coverage --audit-stage R5,R6 --audit-json .test-artifacts/coverage/audit.json` | 清单 1707 / 范围 365 / 覆盖 316 / dropped 49 / 缺失 0 / 多余 0，退出码 0 |
| 真实授权守卫 | `run-cases.sh --kind real`（不带授权标记） | 退出码 1，两条 `::error::` 说明拒绝执行；未连接任何设备 |
| 配置准备 | `--prepare-real-config` 无 Secret / 带测试用假 Secret | 无 Secret：`SKIP` 且不写文件，退出码 0；有 Secret：写出 4 个 yml + 3 个临时凭据文件，`configured=true`，`::add-mask::` 逐行输出 |
| 完整 CI | 未执行 | 需要推送分支触发；本阶段仅验证了与 Job 一一对应的本地入口 |

---

## 7. 验收条件

1. **达成**：`make test` = `test-unit` + `test-cases`（普通 Case 全包）；`make check` 只做 fmt-check / vet / build，不触发测试。
2. **达成**：`test-race` 目标与竞态 Job 已删除，`run-go-race-tests.sh` 删除且全仓无残留引用（`docs/`、`tests/README.md` 的历史描述归 R9）。
3. **达成**：覆盖对账进入 `go-inventory`，退出码 0 的现行结论为「缺失 0 / 多余 0」；`CoverageReport.Failed()` 在缺失、无法解释的多余注册或结构性问题时退出码非零，`dropped` 只能显式声明并带理由。
4. **达成**：CI 五个测试 Job（`go-unit`、`cases-local`、`cases-e2e`、`cases-selfcheck`、`ui-test`）与本地 `make test-unit` / `make test-cases TEST_KIND=...` 一一对应；跳过统计来自 `report.json`（单元组为其 `go test` 结论行）。
5. **达成**：`run-cases.sh --kind real` 缺授权立即以退出码 1 失败；`configured=false` 时用例步骤跳过并在汇总里标注不计为通过。
6. **达成**：脚本清单与 §4.5 一致（13 个，含 6 个发布链路脚本），没有孤儿脚本，也没有第二套产物收集。
7. **未达成（待推送）**：一次完整 CI 通过；失败时能定位到具体 Case 与产物。本机逐项结果见 §6。
8. **达成**：`make inventory` 默认写 `.test-artifacts/inventory`，不再覆盖 `.steps/V3/R0/legacy-cases.manifest.json`。

---

## 8. 风险与对策

| 风险 | 实际发生 / 对策 |
|---|---|
| 分组声明与注册表再次漂移 | 已闭环：`--check` 直接对 `--list` 的注册表与 `go list` 的包清单核验，反例已验证会失败 |
| 删除脚本导致某条汇总链路断 | 每条数据来源先替换后删脚本（D6）；`summarize-ci-gate.sh` 的输入项与 Job 一一对应 |
| CI 时长上升 | `cases-local` 183 条与 `cases-e2e` 64 条拆成不同 Job 并行；后端真实分组固定 `--parallel 1` |
| 真实 Job 授权判定出现两套 | 未发生：判定仍只在 `internal/cigate`（D3） |
| 本地与 CI 结果不一致 | 同一二进制、同一脚本、同一产物布局；本机 `report.json` 与 CI 上传的是同一种结构 |
| `report.json` 摘要依赖 python | CI 用 `python3`；本机没有时只降级为"不打印摘要"并保留报告路径，不影响退出码 |
| 本机没有真实设备 | 真实分组未运行；授权守卫与配置准备已在本机验证，真实批次仍按阶段单独授权 |

---

## 9. 不做的事

- 不修改 `docs/**`、`README.md`、`.github/scripts/README.md`、`tests/README.md`（R9）。
- 不修改产品行为与用例断言。
- 不恢复 race 与覆盖率门禁。
- 不新增第二套测试入口或平行脚本。

---

## 10. 输出给 R9

1. 最终门禁清单：`go-static`、`go-inventory`（分组核验 + 覆盖对账）、`go-unit`、`cases-local`、
   `cases-e2e`、`cases-selfcheck`、`ui-test`、`real-tests-gate`、`real-tests`、`ci-summary`。
2. 脚本清单：§4.5 的 5 个测试相关脚本 + 2 个静态门禁脚本 + 6 个发布链路脚本；`.github/scripts/README.md`
   需要按此重写。
3. 唯一来源说明：授权判定在 `internal/cigate`；跳过与零残留结论在 `report.json`；分组声明在
   `go-test-groups.sh`；真实配置生成在 `agentflow-test --prepare-real-config`。
4. 需要同步的文档引用：`docs/testing.md`、`docs/development.md`、`docs/workspace.md`、`tests/README.md`、
   `README.md`、`lingting-agent-flow-ui/README.md` 里仍引用 `run-real-tests.sh`、
   `collect-test-artifacts.sh`、`prepare-real-tests-config.py`、`pnpm test:e2e:safe`、
   `tests/integration/**` 与 `tests/e2e/**`，需要改写为 R8 之后的入口。
5. `docs/testing.md` 的「§6 脚本与入口」需要写清新口径：普通门禁一律 `--real-tests off`，
   真实分组只由 `real-tests` Job 或显式授权的 `make test-real` 触发。
6. 遗留：`cmd/agentflow-test-inventory` 的扫描输入仍是已删除的 `tests/integration/**`、`tests/e2e/**`，
   R0 清单继续按冻结件使用（与 R7 记录一致）。
