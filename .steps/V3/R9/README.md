# V3 R9：全量验收与 V3 收口

**状态：** 已收口。全量验收、覆盖对账、文档同步、能力回退登记与 V3 收口文档均已完成；真实设备批次
32 条中 1 条失败（原因与证据见 §11），如实记为遗留，不跳过、不改断言。一次完整 CI 仍未在 GitHub
Actions 上运行。
**上游文档：** [路线总规划](../ROADMAP.md)、各阶段 README（[R2](../R2/README.md) – [R8](../R8/README.md)）。
**范围：** 按路线总规划的完成标准做全量验收、执行覆盖对账、同步文档、登记已接受的能力回退，并产出
V3 收口文档。不新增能力、不恢复 race 与覆盖率、不改写 unit 用例。

---

## 1. 目标

1. 逐条验证 [ROADMAP §5](../ROADMAP.md#5-完成标准) 的 14 条完成标准，证据可定位。
2. 以 `legacy-cases.manifest.json` 冻结基线做全量覆盖对账，`dropped` 逐条有理由。
3. 同步所有受影响的文档，只描述已落地能力。
4. 把 race 缺失、覆盖率缺失、unit 仍用 `go test`、`t.Parallel()` 语义降级登记为已接受风险。
5. 产出 `.steps/V3-final.md`，并把 ROADMAP 的阶段状态更新为完成。

---

## 2. 输入与事实

- 冻结基线：R0 的 `legacy-cases.manifest.json`（1707 条）与 `case-resource-matrix.json`，
  基线提交 `f5eb3ef`。
- R5/R6 的覆盖记录与 `dropped` 清单；R7 的核验对账表；R8 的 Makefile / CI / 脚本最终清单。
- 迁移后 `_test.go` 只剩 `tests/unit/**` 与 `tests/internal/testkit` 等辅助文件，重新生成清单会
  失去分母；基线不再刷新。
- `docs/testing.md` §1 要求运行前必须获得授权；本阶段的授权范围见 §6。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| 本文 §4.2 / §7 | 验收矩阵的实际结果与逐条证据 |
| `docs/testing.md`、`docs/development.md`、`docs/workspace.md`、`README.md` | 命令、目录、报告与授权规则同步 |
| `.github/scripts/README.md`、`tests/README.md` | 脚本与测试入口说明 |
| `.steps/V3-final.md` | V3 收口文档（对齐 `V2-final.md` 结构） |
| `ROADMAP.md` | 阶段状态与风险登记更新 |

---

## 4. 实施步骤

### 4.1 冻结分母
- `legacy-cases.manifest.json` 保持 R0 生成时的内容，不再重新生成；`make inventory` 只盘点当前
  `_test.go` 面（R8 已调整写目标）。
- 迁移后的现状计数与 1707 的差额必须由 `dropped` 清单与 `tests/unit` 保留面解释，不允许把差额
  归因于「工具口径变化」。

### 4.2 全量验收矩阵
| 范围 | 执行方式 | 期望 |
|---|---|---|
| `tests/unit/**` 1342 条 | `go test ./tests/unit/... -count=1` | 全部通过或带原因的跳过 |
| 集成 18 包 328 条 | `agentflow-test` 对应分组 | 通过或带原因的跳过；无 error |
| 前端 37 条 | `agentflow-test frontend` | 通过或带原因的跳过 |
| 真实设备批次 | `agentflow-test` 真实分组，独立授权 | 通过；未配置设备则跳过并写明原因 |
| 覆盖对账 | `agentflow-test --audit-coverage` | 退出码 0；`dropped` 全部有理由 |
| 零残留 | 每次运行后读 `residual_check` | `passed`；残留即失败 |
| 产物隔离 | 检查两次运行的 `run-id` 目录 | 互不覆盖；失败运行产物完整 |

一次全量 run 即可，真实批次单独一次；两轮之间核对产物目录不被覆盖。

### 4.3 覆盖对账
- 分三部分核对：`tests/unit` 保留面、R5 的 328、R6 的 37。
- 任何缺失条目必须在 R5/R6 记录中找到 `dropped` 理由；找不到即阻断收口。
- 对账输出与结论写入 §7，命令与产物路径一并记录。

### 4.4 文档同步
| 文件 | 需要更新的内容 |
|---|---|
- `docs/testing.md`：统一入口与命令；并发由调度器决定；真实依赖一次判定；`report.json` 与产物布局；
  零残留两级核验；unit 仍用 `go test`；race 与覆盖率缺失；`--audit-coverage`；`TestMain` 职责边界。
- `docs/development.md`：常用命令（`make test`、`test-unit`、`test-cases`、`test-local`、`test-e2e`、
  `test-pi`、`test-ssh`）；移除 `test-race`；产物目录。
- `docs/workspace.md`：新增 `cmd/agentflow-test`、`internal/testrunner`、`internal/caseinventory`、
  `tests/runner`。
- `README.md`：文档索引与命令；V3 状态。
- `.github/scripts/README.md`：R8 之后的脚本清单与替代关系。
- `tests/README.md`：测试入口、目录与产物说明。
- `docs/runbook.md`：仅在排障条目涉及测试执行时更新。
- `docs/cli.md`：不记录 `agentflow-test`：它是开发工具，不是产品 CLI（见 §5 D2）。

文档只写已实现的能力；不写「计划中」「后续支持」。

### 4.5 已接受的能力回退
| 能力 | 结论 | 恢复路径 |
|---|---|---|
| `go test -race` | 不保留（R0 §2.3） | worker 提供 `go build -race` 变体 + `--race` 选择变体 |
| 覆盖率门禁 | 不新增（现状本来没有） | 同上变体 + 报告合并 |
| 集成/E2E 仍用 `go test` | 不允许（路径 C 已迁出） | — |
| unit 仍用 `go test` | 允许（路径 C 的既定结果） | 若要改写需重新立项 |
| `t.Parallel()` 等价语义 | 不保留；并发由调度器决定 | — |

### 4.6 V3 收口文档
`.steps/V3-final.md` 对齐 `V2-final.md` 的结构：产品/工程定位、已交付能力、命令与目录、已接受风险、
遗留事项。只写最终状态，不复述过程；证据不足的条目不写成已完成。

### 4.7 收尾
- 更新 ROADMAP §4 的状态与 §8 的风险登记。
- 各阶段 README 的状态改为实际结论，验收记录保留。
- 不删除 R0 的盘点资产；`.test-artifacts` 与 `bin/` 保持忽略状态。

---

## 5. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | 冻结基线的处置 | 只读保留，不再生成；现状盘点另起输出 |
| D2 | `agentflow-test` 的文档归属 | `docs/testing.md` 与 `docs/development.md`，不进 `docs/cli.md` |
| D3 | 能力回退的登记位置 | 本文 §4.5 + `docs/testing.md` + `V3-final.md` 三处一致 |
| D4 | `V3-final.md` 的结构 | 与 `V2-final.md` 对齐 |
| D5 | 阶段文档状态 | 完成即写结论与证据位置，不改写阶段内决策 |

---

## 6. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `tests/unit` 全量；全部已注册 Case（含 `frontend`）；真实分组一次 |
| 允许的命令 | `go test ./tests/unit/... -count=1`、`make test`、`./bin/agentflow-test --audit-coverage` |
| 允许的 CI 触发 | 经用户确认后触发一次完整 CI |
| 明确不允许 | 修改断言以换取通过；跳过真实分组却声明完成；为通过而缩小选择范围 |
| 理由 | 收口验收必须覆盖全量范围；真实设备单独确认可用性与占用时间 |

---

## 7. 验收条件

1. **达成**：ROADMAP §5 的 14 条完成标准逐条有证据（见 §11.4）；没有「声明式完成」。
2. **达成**：覆盖对账退出码 0（清单 1707 / 范围 365 / 覆盖 316 / dropped 49 / 缺失 0 / 多余 0），
   `dropped` 逐条带理由并可在 `tests/runner/legacy/` 追溯。
3. **达成**：全量运行结论分开统计 —— 单元 1321 通过 / 7 跳过；普通 Case 311 通过 / 1 跳过；
   真实 Case 31 通过 / 1 失败（未执行 0）。
4. **达成**：文档同步完成，命令均已在本机执行验证，未写入未实现能力。
5. **达成**：能力回退在 [本文 §4.5](#45-已接受的能力回退)、[`docs/testing.md`](../../../docs/testing.md) §12 与
   [`V3-final.md`](../../V3-final.md) §6 三处一致。
6. **达成**：[`V3-final.md`](../../V3-final.md) 完成，ROADMAP §4 状态与 §8 风险登记已更新。
7. **达成**：验收期间没有新增临时脚本，`tests/integration/**`、`tests/e2e/**` 与旧脚本已删除，
   用例入口只有 `agentflow-test` 一个。
---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 覆盖分母被重新生成导致「全绿」 | 基线只读；`make inventory` 不覆盖 R0 |
| 真实设备不可用导致收口被无限推迟 | 如实记录未执行并阻塞对应条目，不用替身代替 |
| 文档写成愿景而非现状 | 只写能指向代码/命令的能力；存疑项进遗留 |
| 收口阶段顺手改功能 | 只做验收、文档与状态更新 |
| 跳过原因被吞掉 | 报告与文档都必须列出跳过原因 |

---

## 9. 不做的事

- 不恢复 race / 覆盖率门禁。
- 不改写 `tests/unit` 的用例。
- 不新增测试能力或资源类型。
- 不修改已冻结的 payload 契约与报告字段语义。
- 不顺手优化产品代码。

---

## 10. 收口输出

1. 全量验收结论与产物路径（§11.1）。
2. 覆盖对账结论（1707 分母的归属，§11.2）。
3. 文档同步清单的完成状态（§11.3）。
4. 已接受风险与遗留事项（§4.5、§11.4）。
5. `.steps/V3-final.md` 与 ROADMAP 的最终状态（§11.5）。

---

## 11. 验收记录

### 11.1 全量运行

授权：本阶段一次性授权 `go test ./tests/unit/... -count=1`、全部已注册普通 Case（含 `frontend`）、
`--audit-coverage`、真实设备分组一次；明确不允许修改断言、跳过真实分组却声明完成、为通过而缩小范围。

| 批次 | 命令要点 | 结论 | 产物 |
|---|---|---|---|
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 退出码 0：16 个测试包 + 25 个 Case 分组全部分类，无遗漏、无重复、无落空声明 | `.test-artifacts/r9-groups-check.log` |
| 非测试门禁 | `make check` | 退出码 0：gofmt / vet / build 通过，未执行测试 | `.test-artifacts/r9-check.log` |
| 单元 | `make test-unit`（`PARALLEL=4`） | 16 个包全部 ok：顶层通过 1321 / 失败 0 / 跳过 7（既有 Windows 符号链接权限跳过） | `.test-artifacts/r9-unit.log` |
| 普通 Case | `make test-cases`（normal，`PARALLEL=4`） | 312 条：通过 311 / 失败 0 / 跳过 1（`selfcheck/skip`，带原因）／panic 0／超时 0／异常 0；零残留 `passed`；退出码 0 | `.test-artifacts/20261121t093231z-fec6/report.json`、`.test-artifacts/cases-normal-*.log/txt` |
| 覆盖对账 | `--audit-coverage --audit-stage R5,R6 --audit-json .test-artifacts/coverage-audit.json` | 退出码 0：清单 1707 / 范围 365 / 覆盖 316 / dropped 49 / 缺失 0 / 多余 0 | `.test-artifacts/coverage-audit.json` |
| 真实设备 | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real`（`--parallel 1`） | 32 条：通过 31 / 失败 1 / 跳过 0；零残留 `passed`；退出码 1（`make` 报 2） | `.test-artifacts/20261121t094125z-09d1/report.json`、`.test-artifacts/cases-real-*.log/txt` |

- 两次运行的产物分别在 `20261121t093231z-fec6/`（普通）与 `20261121t094125z-09d1/`（真实），互不覆盖。
- 单元里的 7 条跳过与本机符号链接权限有关（`tests/unit/server` 的 symlink 用例），不是本次改动引入。
- 单元计数口径：顶层运行 1328 条（`=== RUN`），其中 1314 条打印 `--- PASS`、7 条打印 `--- SKIP`；
  另有 7 条 PASS 行被 `testkit.Main` 的 stdout 重定向吞掉（`tests/unit/environment` 包，包结论仍为
  `ok`、退出码 0），因此顶层通过为 1321。`grep -c '^--- PASS'` 会得到 1314，差的正是这 7 条。
- 普通 Case 的唯一跳过是 `selfcheck/skip`，它按设计验证跳过路径，原因是
  `R1 自检：跳过路径，不执行任何外部操作`。

**真实设备批次中的失败条目**：`integration-remote-workflow/m6-requirement-sequential-tasks`
（`commit=""`，`push_status=succeeded`）。第二条任务的 developer 会话在容器内自行提交了改动
（会话归档为 `[agent/a7fdfd6c… 8854a9d] M6-SEQ-2: …`），代码保存按"没有变更时记录 `no_changes`、
不创建空提交"的既有语义记录了空 commit，推送仍成功，因此不满足"代码保存记录必须带有真实 commit SHA"
的断言。该现象与迁移无关（同一断言在旧入口同样依赖模型是否留下未提交改动，R7 已记录同一用例三次被
外部环境阻塞）；按授权约束如实记为失败，不跳过、不改断言，进入遗留事项。

### 11.2 覆盖对账的 1707 分母归属

| 归属 | 条数 | 说明 |
|---|---:|---|
| 未被 R5/R6 范围覆盖的保留条目 | 1342 | `tests/unit/**` 继续由 `go test` 执行（路径 C），不参与 Case 迁移对账 |
| R5 + R6 范围 | 365 | R5 的 328（集成 18 包 + `e2e/cli`）+ R6 的 37（前端 13 Vitest + 24 Playwright） |
| 其中已覆盖 | 316 | R5 覆盖 279 + R6 覆盖 37 |
| 其中显式 `dropped` | 49 | 全部在 `tests/runner/legacy/` 登记理由（假容器 Git 用例、宿主侧 Pi 用例、Windows MAX_PATH 下的真实 Git 用例等） |
| 缺失 / 无法解释的多余注册 | 0 / 0 | 退出码 0 |

冻结清单 `.steps/V3/R0/legacy-cases.manifest.json` 与 `case-resource-matrix.json` 未被重新生成，
`make inventory` 默认写 `.test-artifacts/inventory`。

### 11.3 文档同步

| 文件 | 完成内容 |
|---|---|
| `docs/testing.md` | 重写：授权、两个执行入口、用例模型、调度、资源与零残留、工作空间与产物、真实依赖、覆盖对账、CI 对应、结果报告、已接受边界 |
| `docs/development.md` | 常用命令改为 `test-unit` / `test-cases` / `test-real` / `test-runner` / `inventory`，删除 `test-ssh` / `test-e2e` / `test-race` / `test-local` |
| `docs/workspace.md` | 新增 `cmd/agentflow-test`、`cmd/agentflow-test-inventory`、`internal/testrunner`、`internal/caseinventory`、`tests/runner` 与依赖方向 |
| `README.md` | 文档索引（testing.md 描述、V3 收口文档与阶段目录） |
| `.github/scripts/README.md` | 重写：13 个脚本的职责、参数与退出码，R8 删除清单、分组与 CI 编排、安全边界 |
| `tests/README.md` | 重写：目录结构、两个入口、分组一览、用例写法、产物布局、CI 对应、分层覆盖、测试配置 |
| `lingting-agent-flow-ui/README.md` | 测试入口与并发说明改写，删除 `tests/internal/test-resource-policy.ts` 与 `test:*:safe` 引用 |
| `docs/runbook.md` | 新增 §8.2「测试运行中止后的设备残留」：人工核对流程、禁止 prune、基线差异只报告 |
| `docs/docker-compatibility.md` | 测试路径改为分组名（`integration-local-validation`、`integration-remote-docker`） |

`docs/cli.md` 未记录 `agentflow-test`：它是开发工具，不是产品 CLI（§5 D2）。

### 11.4 ROADMAP §5 完成标准逐条证据

| # | 标准 | 证据 |
|---:|---|---|
| 1 | 集成 / E2E / 前端只有 `agentflow-test` 一个执行器；`tests/unit` 仍用 `go test` | `tests/integration/**`、`tests/e2e/**` 已删除；`Makefile` 的 `test-cases` / `test-real` 只调用 `run-cases.sh`；`go-test-groups.sh --describe` 只有 `unit` 一个 go test 分组 |
| 2 | 资源声明、分配、释放只由编排进程完成 | `internal/testrunner/resource_registry.go` 是唯一提供者注册处；用例只读 `handles.json` |
| 3 | 并发只由调度器决定 | `internal/testrunner/scheduler.go`；`concurrencycheck` 17 条覆盖容量、独占键、包级串行 |
| 4 | 每条 Case 独立 worker、独占 stdout/stderr、产物落 `cases/<case-id>/` | §11.1 的 run 目录结构；`workspace.go` 的 `RunLayout` |
| 5 | 六种结论齐全；跳过带原因；缺结果按异常 | `result.go` 的六种状态；`selfcheck/skip` 的 `skip_reason`；`selfcheck` 的 error 场景 |
| 6 | 取消 / 超时终止进程树；迟到结果不覆盖结论 | `cancel.go` / `proctree_windows.go` / `late.go`；`concurrencycheck-fault` 8 条验证 |
| 7 | 释放可重复调用；释放失败使资源与整次运行失败 | `resourcecheck-fault` 的 `selfcheck-fail-release` 用例；`residual_check=failed` 路径 |
| 8 | 资源级自证 + 运行级快照终检进入报告 | §11.1 两次运行均 `residual_check=passed`；`residual.go` |
| 9 | 集成 / E2E 不得用 `go test`；unit 结果由统一报告聚合 | 分组声明只剩 `unit`；`run-go-group-tests.sh` 拒绝 Case 分组 |
| 10 | 真实依赖一次判定并下发；未授权不回退本机、不把未执行当通过 | `realconfig.go` + `--real-tests off` 的普通门禁；`run-cases.sh --kind real` 缺授权退出码 1 |
| 11 | 统一报告含选择、授权、用例、资源、零残留与退出码 | `report.json` 字段（§11.1 产物） |
| 12 | 产物按 `run-id` 隔离，失败运行保留全部产物 | 两次运行目录互不覆盖 |
| 13 | 非测试门禁可用且不执行测试 | `make check` 退出码 0（§11.1） |
| 14 | 原有用例与断言没有静默丢失 | §11.2 的覆盖对账（缺失 0、多余 0） |

### 11.5 最终状态

- `.steps/V3/ROADMAP.md`：§4 全部阶段为「已完成」，§8 风险登记更新，§9 文档与 CI 同步清单标记完成。
- `.steps/V3-final.md`：V3 收口文档，含已交付能力、命令与目录、最终验收依据、已接受回退与遗留事项。
- 遗留事项（`V3-final.md` §7）：一次完整 CI 未在 GitHub Actions 运行；race 与覆盖率缺失；
  `cmd/agentflow-test-inventory` 的扫描输入仍是已删除目录；`m6-requirement-sequential-tasks` 待复跑；
  Schema 审计与发布制品无自动化守点；脚本无契约测试。
