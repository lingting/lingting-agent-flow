# AgentFlow V3 最终工程状态

> **文档状态**：V3 收口文档。
> **核实基线**：commit `172c6a3`（R8 收口）的工作区代码、`Makefile`、`.github/workflows/ci.yml`、
> `.github/scripts/`、`tests/` 结构与 R9 的全量验收运行产物（`.test-artifacts/`）。
> **补遗（2026-11-21 之后）**：收口提交 `465b8bb` 已推送并触发首次完整 CI，结论为失败（CI #60）；
> 本文 §2.8、§5、§7 的"一次完整 CI 尚未运行"等表述由 [§8 补遗](#8-补遗2026-11-21-之后的验收勘误) 纠正，
> 历史结论原样保留，不重写。
> **使用规则**：本文只记录最终状态，不记录逐步开发过程。凡写"已交付"的能力，都能在代码、命令或
> 报告字段中指向具体位置；证据不足的条目不写成已完成，一律列入 §6 遗留事项。

---

## 1. 定位

V3 不新增产品能力，也不改数据库 Schema 与 HTTP 接口：它把测试的选择、并发、资源生命周期与结果报告
收口到一个执行器，让"跑过的用例"有唯一入口、可信结论与可复现的产物。

```text
agentflow-test（编排进程）
├── 选择器 + 真实依赖授权判定
├── 调度器（唯一并发决策者）
├── 资源管理器（唯一分配与释放者）
├── 统一报告 + 产物目录
└── worker 子进程（同一二进制 --exec-case <id>）
```

四条不变量是这套体系的基础，任何后续改动都不得违反：

1. 并发只由调度器决定；`t.Parallel()` 不再决定并发。
2. 资源只由编排进程分配与释放，用例只消费序列化句柄。
3. 结论只有 passed / failed / skipped / panic / timeout / error；跳过、未执行、无法确认都不是通过。
4. 不伪造结果、不静默回退：容器缺失、未授权真实依赖、资源最终状态不明，都必须如实失败或跳过。

---

## 2. 已交付能力

### 2.1 两个执行入口

| 范围 | 入口 | 命令 |
|---|---|---|
| `tests/unit/**`（16 个包） | `go test` | `make test-unit` |
| 其余全部 Case（25 个分组 / 358 条） | `agentflow-test` | `make test-cases`、`make test-real` |

- `cmd/agentflow-test` 是唯一二进制：编排进程与 worker 是同一个可执行文件，worker 由 `--exec-case`
  重新启动自身（一次启动约 20ms，单用例总开销约 30ms）。`--list`、`--audit-coverage`、
  `--prepare-real-config` 是不执行用例的旁路模式。
- 集成、E2E 与前端浏览器用例只有这一个入口：`tests/integration/**`、`tests/e2e/**` 与它们的
  `go test` 入口已删除，Playwright 的 `webServer` 也已删除（dev server 由提供者启动）。
- `tests/unit` 的 16 个包仍用 `go test`（路径 C 的既定结果），结论按 `go test` 自己的结论行统计。

### 2.2 用例模型与注册表

- 一条 Case 由 `internal/testrunner.Case` 描述：`ID`、`Group`、`Timeout`、`Package`、
  `LegacyName` / `LegacyID`、`Parallel`、`RunnerInternal`、`Resources`、`Run`。
- 单层顺序子测试用 `Context.Run`；跳过必须带原因，失败与超时通过 `Context` 表达。
- 注册表只有一处：`tests/runner/registry/` 的空导入聚合，新增用例包只改这一行。
- 每个 Case 在独立 worker 子进程中执行，独占 stdout/stderr：`panic` 与 `os.Exit` 只影响该条用例。

### 2.3 调度器

- `internal/testrunner/scheduler.go` 是唯一并发决策者：`--parallel`（默认 8）是同时在飞的上限，
  输入只有资源容量、独占键与包级串行（`Case.Package`）三类。
- 9 个调度场景由 `concurrencycheck`（17 条）覆盖：并发上限、`sqlite-db` 容量 4、独占键、
  共享资源复用、包级串行、取消与迟到结果。
- 取消时序为「停止派发 → 宽限 → 终止进程树 → 释放资源 → 零残留终检」；Windows 用
  `taskkill /T /F` 终止进程树，回收失败判 `error`。迟到结果文件不得覆盖已定型结论。

### 2.4 资源提供者与零残留

- 15 类资源（`docker-daemon`、`remote-container`、`remote-workspace`、`ssh-host`、`ssh-session`、
  `local-ssh-server`、`sqlite-db`、`local-port`、`local-http-server`、`child-process`、
  `service-process`、`git-worktree`、`node-worker`、`frontend-dev-server`、`browser`），
  13 类提供者，作用域只有 `run-shared` 与 `case-exclusive`。
- 句柄以 `agentflow-case-handles/1` 外壳写入 `handles.json` 跨进程传递，不用环境变量传参。
- 零残留两级核验：资源级自证消失（`docker ps -a`、远程目录、端口、SSH 会话计数），运行级在退出前
  对全部提供者做一次快照比对，结论写进 `report.json` 的 `residual_check`
  （`passed` / `failed` / `not-applicable`）；残留或无法确认都判失败。

### 2.5 报告与产物

- 报告 `agentflow-test-report/1` 落在 `<artifact-root>/<run-id>/report.json`：选择条件、真实依赖
  授权结论、用例结论（六种状态）、资源结论、零残留结论、并发与取消记录、`exit_code`。
- 产物布局 `<artifact-root>/<run-id>/cases/<case-id>/{stdout.log,stderr.log,result.json,artifacts/}`；
  用例工作空间与资源存放目录在 `os.TempDir()/lingting-agent-flow-tests/<run-id>/` 下，不进制品。
- 运行产物按 `run-id` 隔离，历史运行不被覆盖；失败的运行保留全部产物。

### 2.6 前端适配器

- 13 条 Vitest + 24 条 Playwright，一个文件一条 Case；`playwright.config.ts` 固定 `workers: 1`、
  `fullyParallel: false`、`retries: 0`，并把 `TEST_ARTIFACT_DIR` 下的路径作为产物目录。
- dev server 改为 `run-shared` 的 `frontend-dev-server`：端口由 OS 分配后经 `AGENTFLOW_UI_BASE_URL`
  注入，释放后自证进程、端口与 HTTP 三重消失。`tests/internal/test-resource-policy.ts` 已删除。
- Playwright 与 Vitest 的机器结论通过 `AGENTFLOW_UI_JSON_REPORT` 回传执行器；两种结论冲突按失败处理。

### 2.7 覆盖对账

- 分母是 R0 冻结的 `.steps/V3/R0/legacy-cases.manifest.json`（1707 条），不再重新生成；
  `make inventory` 默认写 `.test-artifacts/inventory`。
- `agentflow-test --audit-coverage --audit-stage R5,R6` 只读清单与注册表：缺失条目、无法解释的多余注册
  与结构性问题都让退出码非零；`dropped` 只在 `tests/runner/legacy/` 显式登记并带理由。
- 结论：范围 365 条（R5 的 328 + R6 的 37），覆盖 316 + dropped 49，缺失 0、多余 0，退出码 0。

### 2.8 门禁、CI 与脚本

- `make test` = `test-unit` + `test-cases`；`make check` 只做 fmt-check / vet / build，不执行测试。
- `test-race`、`test-ssh`、`test-e2e`、`test-local` 目标已删除；`PARALLEL` 是唯一并发变量。
- CI 收敛为 `go-static`、`go-inventory`（分组核验 + 覆盖对账）、`go-unit`、`cases-local`、
  `cases-e2e`、`cases-selfcheck`、`ui-test`、`real-tests-gate`、`real-tests`、`ci-summary`。
- 分组声明只有一个来源 `.github/scripts/go-test-groups.sh`；`.github/scripts` 由 19 个脚本收敛到 13 个，
  `run-cases.sh` 是 Case 执行的唯一入口，真实配置准备收回 `agentflow-test --prepare-real-config`。

---

## 3. 命令与目录

```bash
make check                                           # 非测试门禁
make test                                            # 单元（go test）+ 全部普通 Case
make test-unit                                       # go test ./tests/unit/...
make test-cases                                      # local + e2e + selfcheck + frontend（312 条）
make test-cases TEST_KIND=local|e2e|selfcheck|frontend
AGENTFLOW_REAL_TESTS_GRANTED=true make test-real      # 真实分组（32 条，需授权与真实设备）
./bin/agentflow-test --list                          # 列出 25 个分组 / 358 条 Case
./bin/agentflow-test --audit-coverage --audit-stage R5,R6
./bin/agentflow-test --prepare-real-config           # CI 用：按环境变量生成 tests/config/*.yml
```

| 路径 | 职责 |
|---|---|
| `cmd/agentflow-test/` | 执行器命令行入口（编排、worker、覆盖对账、真实配置准备） |
| `cmd/agentflow-test-inventory/`、`internal/caseinventory/` | R0 盘点工具（`git ls-files` + Go AST） |
| `internal/testrunner/` | 用例模型、调度器、资源提供者、零残留核验、报告 |
| `tests/runner/**` | Case 定义与注册表（`registry.go` 聚合） |
| `tests/unit/**` | 仍由 `go test` 执行的单元测试 |
| `tests/internal/testkit/` | 通用测试辅助（工作空间、日志、known_hosts、容器核验） |
| `.test-artifacts/<run-id>/` | 运行产物（被忽略，不提交） |

---

## 4. 核心流程

```text
选择（分组 / 精确 ID / 前缀...） → 真实依赖一次判定 → 调度（容量 / 独占键 / 包级串行）
→ 资源分配（编排进程） → worker 子进程执行（句柄注入） → 资源释放与自证
→ 运行级快照终检 → report.json（六种结论 + 零残留 + 退出码）
```

---

## 5. 最终验收依据

一次全量本机运行（`make test-unit` + `make test-cases`）、一次覆盖对账、一次真实设备运行：

| 项 | 命令 | 结论 |
|---|---|---|
| 分组核验 | `go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 16 个测试包与 25 个 Case 分组全部分类，无遗漏、无重复、无落空声明 |
| 非测试门禁 | `make check` | gofmt / vet / build 通过，未执行测试 |
| 单元 | `make test-unit` | 16 个包全部 ok：顶层通过 1321 / 失败 0 / 跳过 7（既有 Windows 符号链接权限跳过；`grep -c '^--- PASS'` 只因 `testkit.Main` 的 stdout 重定向少算 7 条，见 [R9 §11.1](V3/R9/README.md#111-全量运行)） |
| 普通 Case | `make test-cases`（normal，`PARALLEL=4`） | 312 条：通过 311 + 跳过 1（`selfcheck/skip`，带原因）；零残留 `passed`；退出码 0 |
| 覆盖对账 | `--audit-coverage --audit-stage R5,R6` | 清单 1707 / 范围 365 / 覆盖 316 / dropped 49 / 缺失 0 / 多余 0；退出码 0 |
| 真实设备 | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real` | 32 条：通过 31 + 失败 1（`m6-requirement-sequential-tasks`，根因见 §5.1）；零残留 `passed` |
| 产物隔离 | 两次运行分别落在 `.test-artifacts/20261121t093231z-fec6/`（普通）与 `.test-artifacts/20261121t094125z-09d1/`（真实） | 互不覆盖；报告与用例日志都在各自 run-id 下 |

### 5.1 真实设备批次

真实设备批次单独一次运行（`tests/config` 的 `ssh.enable`、`docker.remote_enabled`、`pi.enable` 均为
true）：`AGENTFLOW_REAL_TESTS_GRANTED=true make test-real`（`--parallel 1`，run-id
`20261121t094125z-09d1`）。

| 项 | 结论 |
|---|---|
| 用例 | 32 条：通过 31 / 失败 1 / 跳过 0 / panic 0 / 超时 0 / 异常 0 |
| 零残留 | `passed`（全量快照比对通过，容器与卷在运行前后一致） |
| 退出码 | 1（`make` 以 2 报告配方失败，两者都是失败而不是跳过） |
| 失败条目 | `integration-remote-workflow/m6-requirement-sequential-tasks` |

失败根因（证据见 `.test-artifacts/20261121t094125z-09d1/cases/integration-remote-workflow__m6-requirement-sequential-tasks/`）：

- 该用例要求两条串行任务的代码保存记录都带有真实 commit SHA。第一条任务记录
  `commit=699c740b…`，push 成功；第二条任务的 developer 会话在容器内**自行提交**了改动
  （会话归档显示 `[agent/a7fdfd6c… 8854a9d] M6-SEQ-2: …`），退出后工作区干净。
- 代码保存按既有语义"没有变更时记录 `no_changes`、不创建空提交"记录 `commit=""`，随后推送该分支
  仍成功（`push_status=succeeded`），因此记录里没有 commit SHA，用例断言失败。
- 这不是迁移差异：同一断言在旧 `go test` 入口上同样依赖模型是否留下未提交改动，R7 已记录该用例三次
  被外部环境阻塞（需复跑）。本次结论如实记为**失败**，不跳过、不改写断言、不用替身替代，并进入 §7
  遗留事项。

---

## 6. 已接受的能力回退与风险

| 能力 | 结论 | 恢复路径 |
|---|---|---|
| `go test -race` | 不保留（R0 §2.3），目标、脚本与 CI Job 已删除 | worker 提供 `go build -race` 变体 + `--race` 选择变体 |
| 覆盖率门禁 | 不新增（现状本来没有） | 同上变体 + 报告合并 |
| `tests/unit` 仍用 `go test` | 允许（路径 C 的既定结果） | 若要改写需重新立项 |
| 集成 / E2E 仍用 `go test` | 不允许：路径 C 已迁出，源目录已删除 | — |
| `t.Parallel()` 等价语义 | 不保留：1744 处统一映射为调度器属性 | — |
| 中止运行后的零残留终检 | 不做：需人工确认，处置流程写入 `docs/runbook.md` §8.2 | — |

其余登记在 [ROADMAP §8](V3/ROADMAP.md#8-风险登记) 的风险（Windows 进程树终止、包级串行误配、前端
UI 时序抖动、镜像标签被并行构建改指、真实设备外部故障）在阶段内均已按"如实失败或跳过、不伪造通过"
处置。

---

## 7. 遗留事项

1. **一次完整 CI 尚未在 GitHub Actions 上运行**：R9 只在本机逐项验证了与 Job 一一对应的入口。首次
   推送后需要确认 Job 依赖、产物上传与 `ci-summary` 结论。
   **（已被 §8 补遗纠正：首次完整 CI 已运行，即 CI #60，结论为失败。）**
2. **race 与覆盖率能力缺失**：见 §6；恢复需要新立项，不在 V3 范围。
3. **`cmd/agentflow-test-inventory` 的扫描输入仍是已删除的 `tests/integration/**`、`tests/e2e/**`**：
   R0 清单继续按冻结件使用，重新生成清单前必须先改输入。
   **（已被 §8 补遗纠正：扫描框架已是通用的 `git ls-files`，陈旧的是阶段映射，不是扫描输入。）**
4. **`integration-remote-workflow/m6-requirement-sequential-tasks` 仍失败，需复跑**：R9 真实批次里该用例
   因"Agent 在容器内自行提交、工作区干净"导致代码保存记录没有 commit SHA（详见 §5.1）。它是模型输出
   相关的既有不确定性，需要环境与模型稳定后再跑一次；不改断言、不跳过。
5. **真实设备批次依赖外部环境**：`integration-remote-workflow` 的长用例需要设备到 GitHub 的 HTTPS 与
   镜像源可用；外部故障如实记为环境失败，不换本机替身、不放宽断言。
6. **Schema 审计与发布制品没有自动化守点**：迁移脚本的约束与制品布局靠人工核对与发布流程本身校验。
7. **脚本行为没有契约测试**：`.github/scripts/` 的解释器调用方式、文档头部与退出码只能人工核对。

---

## 8. 补遗：2026-11-21 之后的验收勘误

本节只追加事实，不重写 §1–§7 的历史结论。凡与上文冲突处，以本节为准。

### 8.1 首次完整 CI 已经运行，结论为失败

| 项 | 事实 |
|---|---|
| 运行 | CI #60，run id `44895723793`，`event=push`，`run_attempt=1` |
| 提交 | `head_sha 465b8bb64c57bb3bc19f89de638a834727cfb438`（`:memo: v3 全量验收`），与本地 HEAD 一致 |
| 时间 | 2026-11-21T10:43:55Z 起，10:53:30Z 结束，结论 `failure` |
| 通过 | Go 格式与静态检查、测试分组核验与覆盖对账、真实测试授权判断、Go 单元测试、CLI 端到端用例 |
| 跳过 | 真实外部依赖测试（未授权） |
| 失败 | 本机集成用例（job `136821043451`）、前端用例与门禁（`136821043487`）、执行器自检用例（`136821043518`） |
| 产物 | `cases-local-44895723793-1`（14703776654）、`cases-selfcheck-44895723793-1`（14703558834）、`ui-test-44895723793-1`（14704331582）、`coverage-audit-44895723793-1`（14703931751） |
| 取证限制 | 未认证下载 artifact 返回 `401`；本次核对只能读到 job / step 元数据与注解，artifact 内容未取回 |

因此 §2.8 的"一次完整 CI 待推送确认"、§7 第 1 条与 §5 的"未执行"口径都需要按上表理解：
**入口与 Job 依赖已被真实运行验证，但三个 Job 的失败尚未定级。**

### 8.2 三个失败的当前状态

| 失败 | 结论状态 | 说明 |
|---|---|---|
| `integration-local-scheduler/concurrent-delete-and-scheduler-scan-keep-facts` | 未决 | 用例等待 `Scan` / `Delete` 返回，断言"分派结束后不得残留执行权"；产品侧执行权释放发生在 `go s.run(grant)` 协程内，`Scan` 返回不是收敛点。是"用例观察点"还是"产品残留路径"需要 `report.json` 与用例日志定级 |
| `frontend/playwright-task-containers`、`frontend/playwright-task-create-project-inheritance` | 未决 | 失败 trace、截图与 JSON 报告在 `ui-test` artifact 内；产品行为、fixture 污染、UI 时序与 CI 资源压力四类原因都未排除 |
| `pilot/server-shutdown` | 已确诊 | `bin/agentflow` 未构建：`Makefile` 的 `test-cases` 与 CI 的 `cases-selfcheck` 都只构建 `bin/agentflow-test`；资源分配失败被记为 `error` 而不是跳过 |

三项的处置计划、授权边界与验收条件见 [`.steps/V3/R10/README.md`](V3/R10/README.md)。

### 8.3 对 §7 第 3 条的勘误：陈旧的是阶段映射，不是扫描输入

`internal/caseinventory` 的 `scanGoSources` 已经用 `git ls-files` 枚举全部已跟踪 `.go` 文件，
`tests/` 前缀或 `_test.go` 后缀才纳入，不按 `tests/integration/**`、`tests/e2e/**` 过滤。
仍然陈旧的是阶段分类：`stageForGo`（`inventory.go`）与 `isIntegrationOrE2E`（`resources.go`）
按这两个已删除目录的前缀判定 R5。当前 HEAD 重新生成清单会得到"R5 范围 0 条、全部 `keep-go-test`"，
因此冻结清单必须继续只读使用；改动阶段映射属于后续立项，不在 V3 与 R10 内。

### 8.4 不因补遗而改变的结论

- §6 的已接受回退（`go test -race`、覆盖率、unit 仍用 `go test`、`t.Parallel()` 语义降级、
  中止后不做零残留终检）全部保持。
- §5.1 的真实设备失败（`m6-requirement-sequential-tasks`）仍如实记为失败，仍需复跑。
- R0 冻结清单（1707 条）与 R5/R6 的覆盖结论（316 + dropped 49、缺失 0、多余 0）不变。
