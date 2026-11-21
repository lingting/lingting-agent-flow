# AgentFlow V3：统一测试执行器路线总规划

**文档状态：** 主线规划。R0–R9 已完成，V3 已收口（最终状态见 [../V3-final.md](../V3-final.md)）。
**上游输入：** [审阅意见](TEST-RUNNER-REFACTOR-REVIEW.md)（P0/P1/P2 结论）、
[R0 盘点与决策](R0/README.md)、[R1 接口与可运行骨架](R1/README.md)、
[.plan/mian.md](../../.plan/main.md) 的「测试调整」。
**适用范围：** 只重组测试执行体系，不改产品行为、不做界面改版、不改数据库 Schema。

---

## 1. 目标与不变量

把测试的选择、并发、资源生命周期与报告收口到一个执行器：

```text
agentflow-test（编排进程）
├── 选择器 + 真实依赖授权判定
├── 调度器（唯一并发决策者）
├── 资源管理器（唯一分配与释放者）
├── 统一报告 + 产物目录
└── worker 子进程（同一二进制 --exec-case <id>）
```

四条不变量，任何阶段都不得违反：

1. 并发只由调度器决定；`t.Parallel()` 不再决定并发。
2. 资源只由编排进程分配与释放，用例只消费序列化句柄。
3. 结论只有 passed / failed / skipped / panic / timeout / error；跳过、未执行、无法确认都不是通过。
4. 不伪造结果、不静默回退：容器缺失、未授权真实依赖、资源最终状态不明，都必须如实失败或跳过。

---

## 2. 基线（R0 盘点 + R1 落地）

R0 冻结的规模基线（AST 口径）。清单按冻结件使用：`make inventory` 的扫描输入是 R5 已删除的
`tests/integration/**` 与 `tests/e2e/**`，重新生成清单前必须先改它的输入。

| 项 | 数量 |
|---|---:|
| Go 测试文件 / 测试包 | 246 / 35 |
| `func Test*` / `TestMain` | 1670 / 35 |
| `t.Run` / `t.Parallel` / `t.Cleanup` | 167 / 1744 / 245 |
| 前端 Vitest / Playwright 文件 | 13 / 24 |
| 冻结清单条目 `legacy-cases.manifest.json` | 1707 |

迁移面按路径 C（见 §3.1）划分：

| 阶段 | 用例 | 范围 |
|---|---:|---|
| 保持 `go test` | 1342 | `tests/unit/**`，17 个包 |
| R5 | 328 | `tests/integration/**` 17 个包 + `tests/e2e/cli`（已迁完，源目录删除） |
| R6 | 37 | 前端 13 个 Vitest + 24 个 Playwright 文件 |

R1 已落地并冻结：`cmd/agentflow-test`、`internal/testrunner`（Case 注册表、上下文、句柄、worker、
报告）、16 类资源词汇表、`agentflow-case-handles/1` 句柄外壳、`agentflow-test-report/1` 报告、
真实依赖一次判定、Windows 进程树终止。一次 worker 启动约 20ms，单用例总开销约 30ms。

R1 明确留下的缺口（后续阶段逐项补齐）：资源提供者只有 `local-port`、串行执行、无取消、无零残留核验、
无 `go test` 适配、无覆盖对账、无前端适配。

R2 已落地并冻结：16 类资源的 payload / Spec 契约、13 类提供者（8 类本机含 `local-port` + 5 类远程）、`Provider` 新增
`Verify` 与 `Snapshot`、资源级自证与运行级基线比对终检、容量 / 独占键 / 包级串行三类调度输入，以及
`tests/runner/resourcecheck` 的 8 条本机资源自检用例。远程提供者本阶段只做编译与静态检查，未在真实
Pi / SSH / 远程 Docker 上运行；`node-worker` / `frontend-dev-server` / `browser` 由 R6 补齐（已完成）。
R3 已落地并冻结：`internal/testrunner/scheduler.go` 是唯一并发决策者（`--parallel` 默认 8、容量、独占键、
`Case.Package` 串行作用域与包级串行），`cancel.go` 给出「停止派发 → 宽限 → 终止进程树 → 释放资源 →
零残留终检」的取消时序，`late.go` 让已定型结论不被迟到结果文件改写；报告新增 `concurrency` 与
`cancellation`，用例 `timings` 新增 `queue_enter_at` / `dispatch_at`。9 个调度场景由
`tests/runner/concurrencycheck` 覆盖（`sqlite-db` 容量在 R3 收紧为 4）。

R4 / R4.5 已落地并冻结：`service-process` 真实服务进程的启动、就绪、取消与零残留（R4），以及
`local/scheduler` 41 条集成用例的迁移试点，冻结了迁移写法（`func run<原函数名>`、`Context.Run` 子测试、
按用例初始化资源）与用例包位置（R4.5）。

R5 已收口：328 条集成与 E2E 用例全部迁到 Case 模型并注册，`--audit-coverage` 对 R5 范围退出码 0
（覆盖 279 + dropped 49，缺失 0）。`tests/e2e`、`tests/integration` 目录连同 `TestMain` 已整体删除，
集成与 E2E 现在只有 `agentflow-test` 一个入口。B3 的 6 个真实依赖包在真实设备（WSL2 主机 + Docker
29.7.2、容器内真实 Pi 与 Git）上跑过：`ssh` / `docker` / `codesaver` / `agent` / `workflow` 连续三次
通过，`planning` 9 条中 8 条稳定通过，剩余 1 条受测试设备到 GitHub 的 HTTPS 间歇故障阻塞（旧
`go test` 同一时间点同样失败，迁移差异 0）。执行器能力新增 `Context.Run`（单层顺序子测试，D6）与
`Case.RunnerInternal`（自检用例不参与对账，D11）。阶段内冻结 D9–D13：真实依赖用例只声明本地独占
资源（D9）、禁止宿主侧 Pi 探测（D10）、自检用例对账口径（D11）、`remote/docker` 包内串行（D12）、
既有竞态的只读等待处置（D13）。

R5 遗留（按归属交给后续阶段，不在 R5 内处理）：

- `Makefile` 的 `test-ssh` / `test-e2e` 目标仍指向已删除的 `tests/integration/ssh`、`tests/e2e` 目录，
  与 §7 的替代关系一起交给 R8 收敛。
- `cmd/agentflow-test-inventory` 的扫描输入仍是 `tests/integration/**` 与 `tests/e2e/**`；R0 清单在
  重新生成前按冻结件使用，改输入属于 R0 工具的后续维护。
- `testkit.Main` 与包级终检仍被 `tests/unit`、`tests/runner` 自检包使用，收口归 R7。
- `docs/` 里仍有大量 `tests/integration/...` 路径描述，文档同步归 R9。

R6 已收口：13 个 Vitest 文件与 24 个 Playwright spec 迁到 Case 模型（一文件一条 Case），
`--audit-coverage --audit-stage R6` 退出码 0（37 / 37 覆盖、缺失 0、多余 0），R5 范围复核仍为
328 = 279 + 49、退出码 0。Vite dev server 从 Playwright 的 `webServer` 改为 `run-shared` 的
`frontend-dev-server` 提供者（端口由 OS 分配后注入，释放后自证进程、端口与 HTTP 三重消失），
`browser` / `node-worker` 只分配会话目录并核验用例登记的 CLI 进程；配置侧删除
`tests/internal/test-resource-policy.ts`，`workers` / `maxWorkers` 固定 1，并发只由调度器决定。
七次全量运行的终检都是 `passed`（其中五次 37 / 37 全通过），Playwright 297 条与 Vitest 361 条与迁移前逐条一致、
跳过 0；其中两次并行运行出现 5s `expect` / 30s 用例超时，而旧入口在同一台机器上以 `--workers=4`
对照时同样出现同类失败，判定为前端用例既有的 UI 时序抖动，不是迁移差异。阶段内冻结 D2（一文件一次
CLI 调用）、D5（冷启动阈值）、D6（删除并发策略文件）、D7（产物统一到 `cases/<case>/artifacts/`）、
D8（会话类资源的进程登记与 `node-worker` 契约微调）、D9（前端组建议 `--parallel 4`）。

R6 遗留（按归属交给后续阶段）：

- `node-worker` 的 payload 契约调整（`pid` 改为可选、新增必填 `session_dir` 与 `version`）需要回写
  R2 §7，与 `resourcecheck-fault/unimplemented` 用例随提供者补齐而删除的记录一起。
- `Makefile` 与 `.github/workflows/ci.yml` 仍走 `pnpm test:all:safe`，而 `playwright.config.ts` 已删除
  `webServer`：前端 E2E 必须由 `agentflow-test` 启动 dev server 才能运行，切换归 R8。
- `lingting-agent-flow-ui/README.md` 仍引用已删除的 `tests/internal/test-resource-policy.ts`，
  `test:e2e:safe` / `test:all:safe` 的说明需随 R8 一起更新。

R7 已收口：`tests/internal/testkit` 的无引用组件（包级终检注册表 `finalize.go`、真实依赖旧门禁
`realgate.go`、`FormatSQLite`、`RunLogPath`、`repoRoot`）已删除，`go build` / `go vet` / `gofmt` 干净；
`testkit.Main` 收敛为只服务仍由 `go test` 执行的 16 个 unit 包（运行根目录 + `test.log`）。零残留只剩
「用例级 `ContainerFinalizer` + 运行级 `residual_check`」一套实现，真实设备批次前后容器与卷一致，
6 个真实包 12 次 `REMOTE_CONTAINER_VERIFY` PASS。阶段内冻结 D1–D6，其中 D6 解除 `remote/workflow`
整包串行：远端工作空间、数据库、容器名与分支本就按用例隔离，`serialPackages` 现在为空表；同组内
并发跑两条长用例均通过（656s→215s、1069s→311s），未出现设备级核验假失败。`remote/docker` 的包内
串行（R5 D12）保持不变。

R7 遗留（按归属交给后续阶段）：

- `integration-remote-workflow/m6-requirement-sequential-tasks` 三次运行都被外部环境阻塞（代码保存
  Git 结果不确定、`node:22.19.0-bookworm-slim` 元数据解析命中华为云镜像站 500、任务在
  `project_task_not_head` 闸门下排队 15 分钟未收敛），需在环境恢复后复跑一次；这不是断言或迁移差异。
- 已处置：最后一次复跑时消失的非测试容器 `agentflow-d13c8058797d`（8 天前创建、已 Exited）经确认是
  设备维护者人工删除；用例级核验当时按设计只输出 `WARN REMOTE_CONTAINER_BASELINE_GONE`，不判失败，
  语义按预期工作。R9 只需在运行手册里写清「基线差异只报告」这一点。
- 一次长跑被中止后留下一只产品容器，说明「中止运行不会做零残留终检」，处置流程需写进
  `docs/runbook.md`（R9）。
- `cmd/agentflow-test-inventory` 的扫描输入仍是已删除的 `tests/integration/**`、`tests/e2e/**`；
  R0 清单继续按冻结件使用。

R8 已收口：`Makefile` 的测试目标收敛为 `test-unit`（仍是 `go test`）与 `test-cases`（统一执行器，
`TEST_KIND` 取 `normal`/`local`/`e2e`/`selfcheck`/`frontend`），真实分组走 `test-real` 且要求显式
授权；`test` 改为 `test-unit` + `test-cases`，`test-ssh` / `test-e2e` / `test-race` 与 CI 的竞态
Job 一并删除。分组声明只剩一个来源：`go-test-groups.sh` 同时声明 go test 分组（只有 `unit`）与
25 个 Case 分组的 kind（local / e2e / selfcheck / frontend / real / fault），`--check` 直接对
`go list` 的 16 个测试包、执行器 `--list` 的注册表、Makefile 入口与消费脚本核验。CI 收敛为
`go-static`、`go-inventory`（分组核验 + `--audit-coverage --audit-stage R5,R6`）、`go-unit`、
`cases-local`、`cases-e2e`、`cases-selfcheck`、`ui-test`、`real-tests-gate`、`real-tests`、`ci-summary`；
Case Job 只上传自己的 `<artifact-root>/<job>/<run-id>/`，跳过与零残留结论只读 `report.json`。
`.github/scripts` 从 19 个脚本收敛到 13 个：新增 `run-cases.sh` 作为 Case 唯一入口，删除
`run-real-tests.sh` / `run-go-race-tests.sh` / `run-frontend-tests.sh` / `summarize-real-tests.sh` /
`summarize-test-skips.sh` / `collect-test-artifacts.sh` / `prepare-real-tests-config.py`，最后一项的
职责收回到执行器的 `--prepare-real-config`。本机验收：`make check` 通过；单元 16 包 1322 通过 /
7 跳过；selfcheck 28 条（27 通过 + 1 跳过）、e2e-cli 64 条、本机集成 183 条、frontend 37 条全部
通过且零残留 `passed`；覆盖对账 316 覆盖 + 49 dropped、缺失 0、多余 0。`make inventory` 默认写
`.test-artifacts/inventory`，不再覆盖冻结基线。阶段内冻结 D1–D9。

R8 遗留（按归属交给后续阶段）：

- 一次完整 CI 尚未运行：需要推送分支触发（本阶段只在本机逐项验证了与 Job 一一对应的入口）。
- `docs/testing.md`、`tests/README.md`、`docs/development.md`、`README.md`、
  `lingting-agent-flow-ui/README.md` 与 `.github/scripts/README.md` 仍引用 R8 删除的脚本、
  `pnpm test:e2e:safe` / `test:all:safe` 与 `tests/integration/**`、`tests/e2e/**` 路径，文档同步归 R9。
- `cmd/agentflow-test-inventory` 的扫描输入仍是已删除的 `tests/integration/**`、`tests/e2e/**`
  （与 R7 记录一致，R0 清单继续按冻结件使用）。

R9 已收口：全量验收一次跑完（单元 16 包 1321 通过 / 7 跳过；普通 Case 312 条中 311 通过 + 1 跳过；
真实设备 32 条中 31 通过 + 1 失败；两次运行零残留均 `passed`，产物分别在
`.test-artifacts/20261121t093231z-fec6/` 与 `.test-artifacts/20261121t094125z-09d1/` 下互不覆盖）。
覆盖对账以冻结清单 1707 为分母：范围 365、覆盖 316 + dropped 49、缺失 0、多余 0、退出码 0。
文档同步完成（`docs/testing.md`、`docs/development.md`、`docs/workspace.md`、`README.md`、
`.github/scripts/README.md`、`tests/README.md`、`lingting-agent-flow-ui/README.md`、`docs/runbook.md`、
`docs/docker-compatibility.md`），产出口径以 [../V3-final.md](../V3-final.md) 为准。真实批次唯一失败
`integration-remote-workflow/m6-requirement-sequential-tasks` 的根因（Agent 在容器内自行提交、代码保存
记录 `no_changes` 而推送成功）与证据记入 [R9 §11.1](R9/README.md#111-全量运行)，按遗留事项待复跑，
不改断言、不跳过。一次完整 CI 仍未在 GitHub Actions 上运行。

---

## 3. 已冻结决策

### 3.1 P0-1：路径 C，分层迁移
`tests/unit/**` 的 1342 个用例继续由 `go test` 执行，不改签名、不替换断言；`tests/integration/**` 与
`tests/e2e/cli` 的 328 个用例迁移到统一 Case 模型；前端 37 个文件接适配器。全量重写 1670 个测试函数
不在本次范围。

### 3.2 P0-2：编排进程 + worker 子进程
用例主体只在 worker 里执行：独占 stdout/stderr、包级状态隔离、panic / `os.Exit` 只影响单条用例、
可按需构建 `-race` 变体。句柄通过 `handles.json` 跨进程传递，不使用环境变量传参。

### 3.3 P0-3：race 检测显式放弃
本次重构不保留 `go test -race`，也不新增覆盖率门禁。R8 删除 `test-race` 目标、`run-go-race-tests.sh`
与 CI 的「Go 竞态检测」Job；worker 的 `-race` 变体只作为恢复路径记录，不在本次实现。R9 把该能力回退
登记为已接受风险。

### 3.4 P0-4：零残留两级核验
资源级：每个资源释放后自证消失（`docker ps -a`、远程目录、SSH 会话计数等）。
运行级：编排进程退出前对全部资源做一次快照比对，失败时退出码非零并保留此前所有产物。
跳过不等于通过；一次正常运行结束后 `residual_check.status` 不允许停留在 `not-run`。

### 3.5 P0-5：按阶段授权运行
每个阶段文档写明「本阶段验收允许执行的 Case 集合」。未获授权的阶段只做静态检查（阅读代码、`git diff`、
`gofmt`、`go vet`、`make check` 中不触发测试的部分），不得以「验证」为名运行测试。

### 3.6 文档与 CI 冻结
R2–R7 只改代码、用例资产与本目录内的阶段文档；`Makefile`、`.github/**`、`docs/**`、`README.md` 的修改
集中在 R8（门禁与工作流）与 R9（文档同步）。

---

## 4. 阶段总览

| 阶段 | 状态 | 目标 | 文档 |
|---|---|---|---|
| R0 | 已完成 | 用例与资源盘点、四项 P0 决策冻结 | [R0](R0/README.md) |
| R1 | 已完成 | 进程模型与 Case / Resource / Report 接口、可运行骨架 | [R1](R1/README.md) |
| R2 | 已完成 | 资源契约与提供者、共享/独享落位、零残留两级核验 | [R2](R2/README.md) |
| R3 | 已完成 | 并发调度、容量与互斥、取消与进程树终止、迟到结果防护 | [R3](R3/README.md) |
| R4 | 已完成 | 试点：真实服务进程的启动、就绪、取消与零残留 | [R4](R4/README.md) |
| R4.5 | 已完成 | 试点：`local/scheduler` 41 条集成用例迁移，验证 Case API 与迁移成本 | [R4.5](R4.5/README.md) |
| R5 | 已完成 | 迁移 18 个包 / 328 个集成与 E2E 用例，覆盖对账可执行，真实依赖包在真实设备上跑完 | [R5](R5/README.md) |
| R6 | 已完成 | 前端适配器：Vitest 13 + Playwright 24 接入执行器，dev server 改为共享资源，覆盖对账退出码 0 | [R6](R6/README.md) |
| R7 | 已完成 | TestMain 与 `testkit` 最终核验收口、删除迁移遗留、真实设备零残留复核；1 条用例被外部环境阻塞已记录 | [R7](R7/README.md) |
| R8 | 已完成 | Makefile / CI / 脚本收敛，非测试门禁保持可用；一次完整 CI 待推送确认 | [R8](R8/README.md) |
| R9 | 已完成 | 全量验收（单元 + 312 条普通 Case + 32 条真实 Case）、覆盖对账退出码 0、文档同步与 V3 收口；一次完整 CI 待推送确认 | [R9](R9/README.md) |

依赖与硬门禁：

```text
R2 ──> R3 ──> R4 ──> R4.5 ──> R5 ──> R7 ──> R9
                     └──────> R6 ──┘
                     R5 + R6 ──> R8 ──> R9
```

- R2 的 payload 契约与结果字段已冻结（R2 §5、§8），R3 直接读取 `schedule.go` 的容量、独占键与
  包级串行声明。
- R3 的 9 个调度场景未通过，不得进入 R4。
- R4.5 试点结论未确认，不得开始 R5 的批量迁移。
- R5、R6、R7 与 R8 已完成，可以进入 R9（R8 的门禁收敛跑在 R5 迁移后的用例包、R6 的前端用例包与
  收口后的入口上；R8 遗留的一次完整 CI 运行在 R9 的验收里推送确认）。

---

## 5. 完成标准

编号 9 与 14 采用 R0 第 4 节的改写口径，其余条目由审阅意见与各阶段出口合并而来。

1. `agentflow-test` 是集成测试、E2E 测试与前端测试的唯一执行器；`tests/unit` 仍由 `go test` 执行。
2. 资源声明、分配、释放只由编排进程完成；用例不得自行创建或清理共享资源。
3. 并发只由调度器决定；用例无法通过代码自行扩大并发。
4. 每条 Case 在独立 worker 子进程中执行，独占 stdout/stderr，产物落在 `cases/<case-id>/`。
5. 结论六种状态齐全；跳过带原因且不计入通过；缺失结果文件按异常处理。
6. 取消或超时会终止 worker 进程树，不留子进程；迟到结果不得覆盖已确认结论。
7. 资源释放可重复调用；释放失败使该资源与整次运行都失败。
8. 每个资源释放后自证消失，运行结束做全量快照终检，结论进入报告。
9. 集成与 E2E 用例不得再以 `go test` 作为执行器；`tests/unit` 仍由 `go test` 执行，其结果由统一报告
   聚合（R0 §4 改写）。
10. 真实依赖只有一次判定并下发给 worker；未授权时不得回退到本机执行，也不得把未执行报告为通过。
11. 统一报告包含选择条件、授权结论、用例结论、资源结论、零残留结论与退出码，路径可复现。
12. 运行产物按 `run-id` 隔离，历史目录不被覆盖；失败的运行保留全部产物。
13. 非测试门禁保持可用且不执行测试：`fmt`、`fmt-check`、`vet`、`build`、`web-install`、`web-build`、
    `web-verify`、`check`。
14. 原有测试场景与断言没有静默丢失：以 `legacy-cases.manifest.json` 的全量覆盖与 `counts` 对账为准，
    条目只允许显式标记 `dropped: <理由>`，消失即失败（R0 §4 改写）。

---

## 6. 运行授权模型

- 授权按阶段给出，范围是该阶段文档「运行授权申请」里的 Case 集合与命令。
- 一次授权只对当前阶段有效；进入下一阶段需要新的授权。
- 真实设备（真实 Pi、SSH 主机、远程 Docker）单独列出，不与本机用例混在同一次授权里。
- 未获授权时允许的动作只有静态检查；不得运行 `go test`、`agentflow-test`、前端测试命令或 CI 工作流。
- 授权范围内的运行必须如实报告通过、失败、跳过与未执行，并列出产物路径；不得只报告通过数。

---

## 7. 现有命令的替代关系（R8 落地）

| 现有用法 | 新体系 |
|---|---|
| `go test ./tests/...` | `agentflow-test`（全部已注册 Case）；`tests/unit` 仍用 `go test` |
| `go test -run 'TestFoo' ./tests/unit/store/...` | 单元层不变；集成层用 `agentflow-test <group>/<name>` 或分组前缀 |
| `make test-unit` | 保留，仍是 `go test ./tests/unit/...` |
| `make test-e2e` / `test-ssh`（当前目标指向 R5 已删除的目录） | 改为 `agentflow-test` 对应分组，真实依赖分组保留授权门禁；`test-local` / `test-pi` 目标已不存在 |
| `pnpm test:e2e:safe` / `pnpm test:all:safe`（Playwright 依赖 `webServer` 自启 dev server，R6 已移除该能力） | 前端 E2E 改为 `agentflow-test --parallel 4 frontend`（dev server 由 `frontend-dev-server` 提供者启动）；Vitest 单测仍可用 `pnpm test`（`vp test run`） |
| `run-frontend-tests.sh`（CI 的 `ui-test` Job） | 同上入口；脚本在 R8 删除，`ui-test` Job 改为调用 `agentflow-test` 的 `frontend` 组 |
| `-count=1` | Case 默认不复用结果，不提供 `--count`；重复运行即重新执行 |
| `-short` | 不支持：现状 0 处 `testing.Short()` |
| `-timeout` | `--timeout`（默认 10m），Case 可用 `Timeout` 覆盖 |
| `-p` / `-parallel` | 合并为一个 `--parallel`，由调度器统一换算 |
| `-race` | 本次不提供（§3.3） |

---

## 8. 风险登记

| 风险 | 处置 | 归属 |
|---|---|---|
| 无 `-race` 门禁 | 显式接受，已登记到 [testing.md](../../docs/testing.md) §12 与 [V3-final.md](../V3-final.md) §6；恢复路径见 §3.3 | R0/R9（已完成） |
| `tests/unit` 仍依赖 `go test` | 路径 C 的既定结果；报告层聚合，不改写断言 | R0 |
| `t.Parallel()` 语义降级 | 1744 处统一映射为调度器属性；6 个 `sync.Once` 文件已逐个判定：R5 范围内 3 处均为 Case 内局部状态，另 3 处在 `tests/unit`（路径 C），结论见 R5 §5.4.4 | R5（已完成） |
| Windows 进程树终止不可靠 | `taskkill /T /F` + 回收失败判 error，不用「已杀」冒充通过 | R1/R3 |
| 取消只能通过终止 worker 进程树生效 | worker 与编排进程之间不新增取消 IPC；宽限期后强制终止，回收失败判 error | R3 |
| 包级串行 / 独占键声明误配 | 声明只来自用例与 R2 冻结表，冲突表现为排队而非静默并行；R4.5 / R5 逐包复核 | R3/R5 |
| 前端冷启动开销 | R6 先测基线再决定调用方式：基线全量 4m03，适配层 4m09–4m48，未超「慢一倍」红线，按一文件一次调用落地 | R6（已完成） |
| 共享 dev server 的写污染 | R6 逐 spec 审计：18 个直接 `page.route`、5 个经 helper、1 个纯渲染，均无服务端写入与固定端口依赖，共享成立 | R6（已完成） |
| 并发下前端 UI 时序抖动（既有） | 两套入口同形态复现；前端组建议 `--parallel 4`，不加重试、不放宽断言；如需消除须单独决策 | R6（已完成）/R7 |
| 镜像构建属写入语义 | 构建产物走独立缓存通道，不与 `Destructive + Shared` 混用 | R2 |
| 测试工作空间是保留产物而非泄漏 | 零残留核验排除 `TEST_ARTIFACT_DIR` 与 `.test-artifacts` | R2 |
| 真实设备占用与残留 | 真实批次单独授权；运行前后各做一次快照。B3 已在真实设备跑完，容器与卷在运行前后一致，远程 per-case 目录按迁移前口径保留 | R5（已完成）/R7 |
| 覆盖清单与实际注册漂移 | `--audit-coverage` 作为 R5/R9 的硬门禁；R9 复核：清单 1707 / 范围 365 / 覆盖 316 + dropped 49 / 缺失 0 / 多余 0 / 退出码 0 | R5（已完成）/R9（已完成） |
| `service-process` 端口存在「探测空闲 → 服务监听」竞争窗口 | 就绪判定在超时内重试，窗口期失败判「没有就绪」 | R2 |
| 远程用例漏声明 `ssh-host` 会让连接在终检里成为残留 | 有意保留：终检判 failed 并指出残留条目，强制声明依赖 | R2 |
| 容器零残留核验是设备级的，同包并发会把别的用例的容器判成未登记 | 真实设备容器包整包串行（`remote/docker` 按 R5 D12 改为 `Parallel: false`），核验语义保持清单级；R7 D6 复核后 `remote/workflow` 解除串行——产品容器不匹配 testkit 测试前缀，不参与未登记判定 | R5（已完成）/R7 |
| 需求删除后立即断言「拒绝重新开始」与后台收敛释放执行权竞态 | R5 D13：先只读等待执行权释放（上限 5 分钟）再断言，不放宽断言 | R5（已完成） |
| 真实设备外部故障（GitHub HTTPS、镜像站 500、容器内模型 10 分钟超时） | 如实记为环境失败，不换本机替身、不放宽断言；R9 真实批次 31/32 通过，`m6-requirement-sequential-tasks` 仍失败（Agent 自行提交导致代码保存记录无 commit SHA），已登记为遗留待复跑 | R7（已完成）/R9（已登记） |
| 中止运行不做零残留终检 | 编排进程被中止后残留需人工确认（R7 §4.4 事故记录），处置流程已写入 [runbook.md](../../docs/runbook.md) §8.2（含"基线差异只报告"） | R9（已完成） |

---

## 9. 文档与 CI 同步清单（R8 / R9，均已完成）

- R8（已完成）：`Makefile` 测试目标（`test-ssh` / `test-e2e` 与 §7 的替代关系一起收敛）、
  `.github/workflows/ci.yml` 测试相关 Job、`.github/scripts/` 逐脚本归属；`ui-test` Job 改为
  `agentflow-test --parallel 4 frontend`（R6 已移除 Playwright 的 `webServer`，脚本不能再独立起 dev server）；
  `lingting-agent-flow-ui/package.json` 的 `test:e2e:safe` / `test:all:safe` 同步删除。
- R9（已完成）：`docs/testing.md`、`docs/development.md`（常用命令）、`docs/workspace.md`（新增
  `cmd/agentflow-test`、`internal/testrunner`、`tests/runner` 与已删除的 `tests/integration/**`、
  `tests/e2e/**` 路径）、`README.md`（索引与命令）、`.github/scripts/README.md`、`docs/runbook.md`
  （§8.2 测试运行中止后的设备残留）、`tests/README.md`（入口与目录说明）、
  `lingting-agent-flow-ui/README.md`（改写并发与入口说明，删除 `tests/internal/test-resource-policy.ts`
  引用）、`docs/docker-compatibility.md`（测试路径改为分组名）。
- 文档只描述已落地能力：R9 同步后不再存在指向已删除目录、脚本或目标的现行描述。

---

## 10. 与 V2 的关系

V2 已收口（[../V2-final.md](../V2-final.md)）。V3 是 V2 之后的独立里程碑，R0 是它的第一个阶段；
不并入 V2 的阶段编号，也不替换 `../../.plan/main.md` 的产品需求清单。V3 收口时新增 `.steps/V3-final.md`，
格式与 `V2-final.md` 对齐。

---

## 11. 阶段执行约定

1. 开始一个阶段前先读：本文 → 该阶段 README → 上游阶段 README。
2. 阶段文档里的「阶段内需要冻结的决策」必须在开工时定稿并写回该文档，不得边做边改接口。
3. 发现需求与冻结决策冲突时停止实现，向用户反馈后等待回复，不自行扩大范围。
4. 阶段完成后：更新本文 §4 状态、该阶段 README 的状态与验收记录，并把新事实补进 §2 与 §8。
5. 任一阶段不得跨阶段顺手清理其他阶段的遗留；R7 与 R8 是唯一的集中清理点。
