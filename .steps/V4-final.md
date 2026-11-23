# AgentFlow V4 最终工程状态

> **文档状态**：V4 收口文档（R0 盘点与决策冻结 → R9 门禁、CI 与文档收口）。
> **阶段文档状态**：V4 阶段文档（`.steps/V4/**`，含 `ROADMAP.md` 与 R0–R9 批次记录）已随 V4 收口移除；
> 本文保留正文与结论，原文中指向阶段文档的链接已改为纯文本，需要细节时用 Git 历史（被删提交之前可查）。
> **核实基线**：R9 收口工作区（R8 批次记录之后）的代码、`Makefile`、`.github/workflows/ci.yml`、
> `.github/scripts/`、`tests/` 结构与 R0–R8 的阶段记录。
> **使用规则**：本文只记录最终状态，不记录逐步开发过程。凡写"已交付"的能力，都能在代码、命令或报告
> 字段中指向具体位置；证据不足或本次未获授权执行的条目一律列入 §7 遗留事项。

---

## 1. 定位

V4 不新增产品能力，也不改数据库 Schema、HTTP 接口与前端功能：它把测试的**目录形状、执行入口、
并发额度与 worker 执行模型**收敛到终态，让"一条 Case、一个入口、一套额度、一个可复用 worker 池"
成为可核对的仓库事实。

```text
agentflow-test（编排进程，唯一执行入口）
├── 选择器 + 真实依赖授权判定
├── 调度器（唯一并发决策者）
│   ├── 全局 --parallel（worker 槽位上限）
│   ├── 包级额度（--package-limit / Case.Concurrency.PackageLimit）
│   ├── 命名并发池（--pool name=limit / Case.Concurrency.Pools）
│   ├── 资源容量（CapacityOf）与独占键（ExclusiveKeyOf）
│   └── 队列：独占段优先 + 可复用段
├── 资源管理器（唯一分配与释放者）
├── 统一报告（agentflow-test-report/2，含额度与 worker 分节）
└── worker 池：worker 主动拉取 Case，按顺序执行多条；独占用例用全新 worker 并退出
```

三条终态事实（R9 收口后成立）：

1. 仓库里没有任何 `_test.go`；`go test` 不执行任何用例，也不存在"跑 0 条返回 0"的空门禁。
2. `agentflow-test` 是包含 unit 面在内的唯一执行入口；`make test` 就是 `make test-cases`。
3. 并发额度三类齐备、worker 池按 W1–W7 工作，两者都有自检 Case 与报告字段。

---

## 2. 已交付能力

### 2.1 目录形状

```text
tests/
├── testkit/            通用测试辅助（工作空间、日志、known_hosts 快照、容器核验），不含测试入口
├── unit/               unit 用例的来源包路径（17 个包，已无 `_test.go`）
├── registry/           用例包聚合入口（空导入）
├── integration/local/  本机集成用例（9 个分组）
├── integration/ssh/    真实 SSH 主机用例
├── integration/remote/ 真实远程 Docker / Agent / 代码保存 / 规划 / 工作流用例
├── e2e/cli/            CLI 端到端用例
├── frontend/           Vitest 与 Playwright 适配用例
├── selfcheck/          执行器自检：跳过、超时、迟到结果、取消
├── resourcecheck/      资源提供者自检：分配、释放、自证、故障注入
├── concurrencycheck/   调度场景与 worker 池：额度、复用、独占、优先级、崩溃重启
├── pilot/              试点用例（真实服务进程）
└── config/             真实外部依赖配置（示例文件入库，真实配置不入库）
```

- `tests/runner` 与 `tests/internal` 两层中间目录已不存在；用例包直接挂在 `tests/` 下。
- `tests/testkit` 的 `Main`、运行根目录日志重定向、`latest-run.txt` 与默认 `-test.v` 已随 R9 删除：
  日志辅助只写当前标准输出（`testkit.Logger` / `testkit.Slog`），由执行器按用例落盘。
- 命名规则保持：`tests/testkit` 是共享辅助，`tests/unit/testkit` 是一个 unit 用例包，两者不合并。

### 2.2 唯一执行入口

| 项 | 事实 | 证据 |
|---|---|---|
| 仓库中的 `_test.go` | 0 个 | `git ls-files '*_test.go'` 为空 |
| `go test` | 不是门禁：没有测试文件可执行，因此不产生任何结论 | 同上；`go-inventory` 把任何残留测试包判失败 |
| 本地入口 | `make test` = `make test-cases`；`make test-cases TEST_KIND=...`；`make test-real` | `Makefile` |
| `make test-unit` | 不存在（`make` 报 No rule to make target） | `Makefile` 无该目标 |
| CI Job | 没有 `go-unit`；`ci-summary` 不引用它 | `.github/workflows/ci.yml` |
| 注册表规模 | 1736 条 Case / 42 个分组；普通门禁 1688 条 | `./bin/agentflow-test --list` |
| unit 面 | 1333 条 Case / 17 个来源包 | 同上（`unit-*` 分组） |

- unit 面的 `func Test*` 全部迁为 Case（R5–R8）；迁移写法固定为
  `func run<原函数名去掉 Test 前缀>`，`Case.Package` 填来源路径 `tests/unit/<pkg>`。
- 用例的失败、跳过与超时仍只通过 `Context` 表达；跳过必须带原因，不计入通过。
- 门禁口径只有一处：`.github/scripts/go-test-groups.sh`。`--check` 在"无 go test 分组"语义下仍会
  失败于四类漂移：残留 go test 测试包、`Makefile` 出现 `go test` 调用或 `test-unit` 目标、Case 分组
  未归类 / 重复归属 / 声明落空、工作流声明的普通 kind 与清单不一致。

### 2.3 并发额度

- 三类额度：全局 `--parallel`（默认 8）、包级 `--package-limit` + `Case.Concurrency.PackageLimit`、
  命名池 `--pool name=limit` + `Case.Concurrency.Pools`；与既有资源容量、独占键、包级串行共同决策。
- D-04 生效规则：运行级值存在即生效（可放大），报告记录 `declared_limit` 与生效值并 `WARN` 差异；
  用例之间的同 key 声明冲突在注册期硬失败。
- 不变量 I1–I10 由 `tests/concurrencycheck/`（58 条）覆盖：全局与包级上限、池容量、work-conserving
  派发、无队头阻塞、不可满足判定、计数守恒、报告完备与派发确定性；`quota.go` / `limit.go` 为实现位置。
- 8/3/5 场景可复现：池容量 3、`--parallel 8` 时池内 3 条在飞、池外占满剩余 5 个槽位；队列只有池内
  Case 时只用 3 个槽位是正确结果。

### 2.4 worker 池（D-10）

- 复用：一个 worker 顺序执行多条 Case，用例之间重置 cwd、环境变量与日志文件；`--parallel` 同时是槽位
  上限。
- 独占：`Case.DedicatedWorker` 使用全新一次性 worker，执行完即退出、不复用（当前 2 条声明，均在
  `concurrencycheck`）。
- 优先级与保留：独占段优先派发，独占队列非空时可复用 worker 数不得降到 0。
- 崩溃处理：worker 退出码非 0 或无结果文件 → 当前 Case 记 `error` / `panic`，worker 计一次 restart，
  队列继续；`os.Exit`、控制通道污染、派发前退出都归入该类。
- 结果归属：每条 Case 的结果只来自它自己那次执行并落盘 `cases/<case-id>/result.json`。
- 报告字段：`workers.total` / `workers.reuse` / `workers.restarts` / `workers.isolated_cases`，每条用例
  带 `worker_id`。

### 2.5 报告与产物

- 报告版本 `agentflow-test-report/2`（`internal/testrunner/report.go` 的 `ReportSchemaVersion`），不保留
  旧版读路径。
- 额度分节：`concurrency.package` 与 `concurrency.quotas` 各条目含 `limit` / `declared_limit` / `peak` /
  `completed` / `queued_ms_total` / `queued_ms_max` / `undispatched` / `undispatched_reason`；运行级另有
  `parallel_limit` / `max_in_flight` / `queued_ms_total` / `queued_ms_max` / `warnings`。
- worker 分节：见 §2.4；`worker_launches` 与 `workers.total` 一致。
- 产物布局不变：`<artifact-root>/<run-id>/report.json`、
  `cases/<case-id>/{stdout.log,stderr.log,result.json,artifacts/}`、`workers/<n>/stderr.log`；用例工作空间
  仍在 `os.TempDir()/lingting-agent-flow-tests/` 下，不进制品。
- 结论仍是六种：`passed` / `failed` / `skipped` / `panic` / `timeout` / `error`。

### 2.6 迁移可核对性

- 分支阶段的逐包迁移清单（源 `func Test*` 数量 → Case 数量、跳过原因、独占理由）写在 R5（`execenv`
  + `lifecycle`）、R6（9 个中小包）、R7（`domain` / `container` / `agent`）、R8（`store` / `httpapi` /
  `app`）的批次记录里；这些阶段文档已随 V4 收口移除，需要时用 Git 历史查。
- 覆盖对账（清单、`--audit-*`、dropped 登记、盘点工具与 CI 步骤）已按 D-06 删除；迁移完整性证据改为
  "每包清单 + 逐包运行"，终态防丢用例的证据是"仓库里没有 `_test.go`"加上 `--list` 的 1736 条注册项。

### 2.7 门禁、CI 与脚本

- 本地入口：`make check`（`fmt-check` + `vet` + `build`，不执行测试）、`make test`（全部普通 Case）、
  `make test-cases TEST_KIND=normal|local|e2e|selfcheck|frontend|unit`、`make test-real`、
  `make test-runner`。
- CI 必需 Job：`go-static`、`go-inventory`、`cases-unit`、`cases-local`、`cases-e2e`、`cases-selfcheck`、
  `ui-test`、`real-tests-gate`、`real-tests`、`ci-summary`；`go-unit` 与 `run-go-group-tests.sh` 已删除，
  `summarize-ci-gate.sh` 不再有 `GO_UNIT_*` 输入。
- `go-inventory` 只做静态核验（`go list` 的测试包必须为空、执行器 `--list` 的注册表、Makefile 配方、
  消费脚本与工作流声明），不执行任何用例。
- `.github/scripts/` 的脚本清单以 [`.github/scripts/README.md`](../.github/scripts/README.md) 为准。

---

## 3. 命令与目录

```bash
make check                                    # 非测试门禁：fmt-check + vet + build
make test                                     # 全部普通 Case（= make test-cases TEST_KIND=normal）
make test-cases TEST_KIND=local|e2e|selfcheck|frontend|unit
AGENTFLOW_REAL_TESTS_GRANTED=true make test-real   # 真实分组（32 条，需授权与真实设备）
make test-runner                              # 只构建 bin/agentflow-test
./bin/agentflow-test --list                   # 列出 1736 条 Case / 42 个分组（不执行）
bash .github/scripts/go-test-groups.sh --check --declared-case-kinds local,e2e,selfcheck,frontend,unit
```

| 路径 | 职责 |
|---|---|
| `cmd/agentflow-test/` | 执行器命令行入口（编排进程、worker、真实配置准备、`--list`） |
| `internal/testrunner/` | 用例模型、调度器、并发额度、worker 池、资源提供者、零残留核验、报告 |
| `tests/registry/` | 用例包聚合（空导入，一次注册全部 Case） |
| `tests/<kind>/` | 用例包（见 §2.1），`tests/unit/**` 只作为来源包路径 |
| `tests/testkit/` | 通用测试辅助（无 `Main`、无测试入口） |
| `.github/scripts/go-test-groups.sh` | 分组声明的唯一来源与静态门禁 |
| `.test-artifacts/<run-id>/` | 运行产物（被忽略，不提交） |

---

## 4. 核心流程

```text
选择（分组 / 精确 ID / 前缀...） → 真实依赖一次判定
→ 调度（全局 / 包级 / 命名池 / 资源容量 / 独占键 / 包级串行）
→ 资源分配（编排进程） → worker 拉取与执行（句柄注入，复用或独占）
→ 资源释放与自证 → 运行级终检 → report.json（六种结论 + 额度 + worker + 零残留 + 退出码）
```

---

## 5. 最终验收依据

R9 只执行了不需要测试授权的静态动作；测试运行与 CI 需要单独授权，逐项列在 §7。

| 项 | 命令 | 结论 |
|---|---|---|
| 仓库无 `_test.go` | `git ls-files '*_test.go'` | 空 |
| 静态门禁 | `make check`（`gofmt -l` / `go vet ./...` / `go build ./...`） | 通过，未执行测试 |
| 分组核验 | `go-test-groups.sh --check --declared-case-kinds local,e2e,selfcheck,frontend,unit` | 退出码 0：残留 go test 测试包 0 个、42 个 Case 分组全部分类，无遗漏、无重复、无落空声明 |
| 反证：残留分组声明 | 同上并追加 `--declared-groups unit` | 退出码 1（清单已无 go test 分组），说明门禁不会空转 |
| 注册表规模 | `./bin/agentflow-test --list` | 1736 条 / 42 个分组；普通门禁 1688 条、真实 32 条、故障注入 16 条 |
| `make test-unit` | `make -n test-unit` | No rule to make target（目标不存在） |
| 普通 Case 全绿 | `make test` | **未执行**（未获授权），见 §7 |
| unit 面全绿且 `totals` 与迁移前对账 | `make test-cases TEST_KIND=unit` | **未执行**（未获授权），见 §7 |
| 真实设备批次 | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real` | **未执行**（未获授权），见 §7 |
| 一次完整 CI | push 后 `ci.yml` | **未运行**（需推送授权），见 §7 |

---

## 6. 已接受的能力回退与风险

| 能力 | 结论 | 恢复路径 / 理由 |
|---|---|---|
| `go test -race` | 不保留（V3 R0 §2.3 起接受） | worker 的 `-race` 变体 + `--race` 选项，未实现 |
| 覆盖率门禁 | 不新增 | 同上 |
| 覆盖对账清单 | 已删除（D-06） | 不为旧分母保留兼容层；证据改为每包迁移清单 + 逐包运行 |
| `t.Parallel()` 等价语义 | 不保留：声明映射为调度器属性，并发只由调度器决定 | 需要并行时序的用例必须显式声明额度或独占 |
| 中止运行后的零残留终检 | 不做：编排进程被中止时残留需人工确认 | 处置流程见 `docs/runbook.md` |
| `go test` 的"空门禁" | 不存在：仓库无测试文件，`go test` 不产生结论 | `git ls-files '*_test.go'` 为空是终态证据 |
| 独占用例声明 | 允许但需理由；报告 `isolated_cases` 占比 | 占比上升意味着退化为"一 Case 一进程"，评审要指出 |

---

## 7. 遗留事项

1. **普通 Case、unit 面与真实设备批次的全量运行未获授权**：R9 只完成静态核验与门禁收敛。
   需要用户明确授权后分别执行 `make test`、`make test-cases TEST_KIND=unit`、`make test-real`，
   并核对 `report.json` 的 `totals` 与迁移前逐包结论。
2. **一次完整 CI 尚未运行**：`go-unit` 删除、`ci-summary` 依赖与产物上传都只在本机核验过脚本行为，
   需要推送授权后在 GitHub Actions 上确认 Job 依赖、`go-inventory` 与 `ci-summary` 结论。
3. **`go-test-groups.sh` 的"零测试包"分支没有负向用例**：验证它需要临时创建 `_test.go`，与
   "未授权不创建测试文件"冲突；当前只通过"声明残留分组"反证门禁会失败。
4. **真实设备批次仍依赖外部环境**（沿用 V3 §7）：远程 Docker、容器内真实 Pi 与到 GitHub 的 HTTPS
   可用性不足时如实记为环境失败，不换本机替身、不放宽断言。
5. **race 与覆盖率仍无门禁**：见 §6，恢复需单独立项。
6. **`.github/scripts/` 没有契约测试**：解释器调用、文档头部、参数与退出码只能人工核对。
7. **数据库 Schema 审计没有自动化守点**：迁移脚本只能人工核对（见 `AGENTS.md` 的数据库约束规范）。

---

## 8. R9 收口记录（2026-11-23）

- 删除 `go test` 执行路径：`tests/testkit/main.go`（`Main`、运行根目录日志重定向、`latest-run.txt`）、
  `Makefile` 的 `test-unit`、`.github/scripts/run-go-group-tests.sh`、`ci.yml` 的 `go-unit` Job 与
  `ci-summary` 对它的依赖、`summarize-ci-gate.sh` 的 `GO_UNIT_*`。
- `go-test-groups.sh`：`go_test_groups` 为空、`group_packages` / `group_make_target` 恒失败；`--check`
  改为核验"零 go test 测试包 + Makefile 无 `go test` / `test-unit` + 无 go test 分组声明 + Case kind
  改为核验"零 go test 测试包 + Makefile 无 `go test` / `test-unit` + 无 go test 分组声明 + Case kind
  与清单一致"。
- 文档按终态重写：`docs/testing.md`、`docs/workspace.md`、`docs/development.md`、`tests/README.md`、
  `.github/scripts/README.md`、`README.md`、`docs/release.md` 的跳过原因产物描述。
- 未执行任何测试；静态门禁、分组核验与 `--list` 结论见 §5。
