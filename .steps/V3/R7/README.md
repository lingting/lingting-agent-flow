# V3 R7：TestMain 与最终核验收口

**状态：** 已完成。代码收口与验收 1–6 达成；验收 7 中 `integration-remote-workflow` 的
`m6-requirement-sequential-tasks` 两次运行都被外部环境阻塞，已如实记录为未通过，不计为通过。
**上游文档：** [路线总规划](../ROADMAP.md)、[R5 迁移](../R5/README.md)、[R6 前端适配器](../R6/README.md)、
[R2 零残留核验](../R2/README.md)。
**范围：** 收口 `tests/internal/testkit` 的包级最终核验与 `TestMain` 职责，删除迁移后无引用的组件。
不迁移用例（R5/R6）、不改 CI 与文档（R8/R9）。

---

## 1. 目标

R5/R6 删除了已迁移包的 `_test.go` 与 `TestMain`，但 `testkit` 里的核验实现仍在：`finalize.go` 的
`RegisterFinalizer`、`RegisterContainerFinalizer`、远程目录与文件缺失核验，以及旧 `TestMain` 曾
承载的收尾职责。本阶段回答：

1. 每个旧核验在 R2 的资源级自证 / 运行级终检里是否有等价落点；
2. 迁移后哪些 `testkit` 组件已无引用，可以删除；
3. 仍由 `go test` 执行的 unit 包保留哪些入口，职责是否只剩日志与工作空间；
4. 真实设备批次的零残留是否真的由新体系承担，而不是继续靠旧 finalizer。

---

## 2. 输入与事实

- 35 个 `TestMain` 全部经 `testkit.Main`；R0 记录其中 18 个包在 R5 迁出。实际复核：仍由
  `go test` 执行、保留 `TestMain` 的是 **16 个** unit 包（`tests/unit/app` 含 2 个子包），
  全部只有一行 `os.Exit(testkit.Main(m))`，不注册任何资源核验。R7 早期文档写的「17 个」是
  沿用 R0 的口径，按实际注册表更正为 16。
- R5 已把 6 个真实依赖包的包级终检改成用例级终检（R5 §5.4「包级终检的替换方式」）：`guardContainerResiduals` 与
  `planningVolumeRun.Verify` 在用例内注册 `t.Cleanup`，因此包级终检注册表迁移后确实无调用方。
- R2 已实现资源级自证与运行级终检，`residual_check` 进入报告；真实依赖用例按 D9 只声明本地
  独占资源，远程容器由用例级 `ContainerFinalizer` 核验。
- `testkit` 仍承担 unit 侧的运行根目录、日志、配置解析、SQLite 打开与受控容器观测构造，这些
  没有被 R5 替换。
- `docs/testing.md` §4 要求不删除测试工作空间；「零残留」不覆盖工作空间与产物。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `tests/internal/testkit` | 删除无引用组件；保留 unit 侧必要入口并收敛职责 |
| `tests/unit/**` | 仅在有引用变化时更新调用点，不改断言（本次无需改动） |
| 本文 §5 | 旧核验 → 新落点对账表（逐项） |
| `internal/testrunner` | 补齐对账表里缺口，不改已冻结语义（本次无需补缺口；仅按 D6 解除 workflow 整包串行） |

---

## 4. 实施步骤与结果

### 4.1 建立对账表（已完成）

逐项列出旧 `TestMain` / `finalize.go` 承担的职责，每项落到「资源级自证」「运行级终检」
「worker 产物」或「unit 入口保留」四者之一，结论见 §5。

### 4.2 删除迁移后无引用的组件（已完成）

判据是编译 + 全仓引用检索，不凭注释与印象。删除清单：

| 文件 / 符号 | 依据 |
|---|---|
| `finalize.go` 整文件（`RegisterFinalizer`、`RunFinalizers`、`RegisterContainerFinalizer`、`RemoteDockerSession`、`OpenRemoteDockerForFinalize` 等 12 个符号） | 迁移后 0 处调用；包级终检已由用例级 `t.Cleanup` 承担 |
| `realgate.go` 整文件：`RequireRealTests`、`RealTestsAllowed`、`RealTestsGateDecision`、`isCIRun`、`grantReasonText` | 0 处调用；真实依赖授权由 `internal/testrunner` 的 `auth.go` 一次判定，用例通过 `Context.RealTestsAllowed` 读取 |
| `sqlite.go` 的 `FormatSQLite` | 0 处调用 |
| `main.go` 的 `RunLogPath`、`runLogPath` 状态与 `Main` 中的终检执行 | 0 处调用；`Main` 只保留运行根目录与日志职责（D2） |
| `config.go` 的 `repoRoot(t Tester)` | 0 处调用；`repoRootPath` 保留（`configDirPath` 仍在用） |

同时更新了仍在描述旧行为的注释（`main.go` 的 `Main`、`remotecontainer.go` 的
`ContainerRun` / `ContainerVerifyTimeout` / 文件头、`knownhosts.go` 的两处「包级最终核验」）。

保留清单（仍有引用）：`config.go`、`containerinspect.go`、`fileabsence.go`、`gitcredential.go`、
`knownhosts.go`、`logging.go`、`main.go`、`privatekey.go`、`project.go`、`remotecontainer.go`、
`remotedir.go`、`sqlite.go`、`sqlite_error.go`、`tester.go`、`testkit.go`、`workspace.go`。

删除后 `go build ./...`、`go vet ./...`、`gofmt -l` 均无输出；未保留「注释掉待用」。

### 4.3 收敛 unit 侧入口（已完成）

- 16 个 unit 包的 `TestMain` 行为不变，逐个人工复核确认只有 `os.Exit(testkit.Main(m))`，
  不注册真实资源核验。
- `testkit.Main` 的注释改为只描述 go test 侧的运行根目录与日志，并显式写明不再承担包级资源核验；
  集成与 E2E 用例由 `agentflow-test` 的 worker 执行，不经过该入口。
- 两套路径互不覆盖（D3）：unit 用 `<os.TempDir()>/lingting-agent-flow-tests/<包名>-<随机>/test.log`，
  迁移用例用 `.test-artifacts/<run-id>/` 产物目录 + `<os.TempDir()>/lingting-agent-flow-tests/<run-id>/`
  工作空间。

### 4.4 真实批次的零残留复核（已完成，含事故与一项开放项）

运行前后各做一次设备快照（`docker ps -a`、`docker volume ls`、远端测试目录）：

| 时点 | 容器 | 卷 | 结论 |
|---|---|---|---|
| 批次前 | `agentflow-d13c8058797d`（Exited 4 天）、`pgsql-server`（Up） | 全部创建于 2026-10-16 | 基线 |
| 批次后 | 与基线逐条一致 | 与基线一致，无当日新增 | 无残留 |

- 6 个真实包共 12 次 `REMOTE_CONTAINER_VERIFY status=PASS`，0 次 `REMOTE_CONTAINER_RESIDUE`；
  旧 finalizer 已删除的情况下仍能判定残留，说明 R2 的自我核验没有缺口。
- 基线变化的处置：最后一次 `m6-requirement-sequential-tasks` 复跑时，运行前存在的非测试容器
  `agentflow-d13c8058797d`（8 天前创建、已 Exited）在运行后消失，用例级核验按设计输出
  `WARN REMOTE_CONTAINER_BASELINE_GONE`（只报告、不判失败）。已与设备维护者确认：该容器是人工
  删除，不是测试误删；用例日志里也没有针对它的 `REMOTE_CONTAINER_CLEANUP` 记录，与上述结论一致。
  这条记录保留下来，因为它验证了「非测试容器的基线差异只报告、不判失败」的语义按预期工作。
- 事故：一次 `integration-remote-workflow` 长跑被外部中止，编排进程随之退出，worker 已经创建的
  产品容器 `agentflow-d60b263090b4` 留在设备上（`track_task_id` 可在用例 stdout 中追溯）。
  已用 `docker rm -f` 清理并复核设备回到基线。记录该事故是因为它说明「中止运行不会做零残留终检，
  残留需要人工确认」，与 R9 的运行手册描述一致。

### 4.5 失败与跳过的语义复核（已完成）

- 核验失败使退出码非零：`--selfcheck-fail-release local-port resourcecheck-fault/release-failed`
  → 退出码 1、`零残留：failed（运行结束后仍存在 1 条残留资源）`。
- 设备未配置导致的跳过带原因且不计入通过：`selfcheck/skip` 记 SKIPPED 并带原因；
  本地批次 92 条 = 通过 91 + 跳过 1，跳过未计入通过。
- 删除旧 finalizer 后「无法确认即失败」的标准未降低：取消场景中在跑的 2 条记「运行被取消」，
  `cancel-c` 记「运行被取消，用例未派发执行」，退出码 1，无遗留 `agentflow-test.exe`。

---

## 5. 旧核验 → 新落点对账表

| 旧职责 | 原位置 | 新落点 | 状态 |
|---|---|---|---|
| 运行根目录与日志 | `testkit.Main` / `runRootInternal` | unit 侧保留 `testkit.Main`（运行根目录 + `test.log`）；迁移用例用 runner 产物目录与 `<os.TempDir>/lingting-agent-flow-tests/<run-id>/` 工作空间 | 已达成（D2 / D3） |
| 包级终检注册表 | `finalize.go` 的 `RegisterFinalizer` / `RunFinalizers` | 删除；结论由用例级 `t.Cleanup` + 运行级 `residual_check` 承担 | 已达成（D1） |
| 容器零残留 | `RegisterContainerFinalizer` | 用例级 `ContainerFinalizer.Finalize`（读真实 `docker ps -a` 清单） | 已达成（12 次 PASS） |
| 远程目录清理 | `remotedir.go` / finalizer | 远程用例工作空间按用例随机后缀隔离并保留为失败现场（不在零残留口径内）；`remote-workspace` 提供者释放后自证目录不存在 | 已达成 |
| 文件缺失核验 | `fileabsence.go` | 保留 `ClassifyFileAbsence` / `FileAbsenceProbe`，由远程用例断言使用 | 已达成（仍有引用） |
| SSH 会话关闭 | finalizer | 用例级 `ws.Cleanup(client.Close)`，关闭失败使用例失败 | 已达成 |
| 卷/镜像清理 | `remote/planning` 终检 | `requireVolumeAbsent` / `verifyVolumeAbsent` 用例内断言；设备卷快照前后一致 | 已达成 |
| 失败改变退出码 | finalizer 返回值 | 用例 failed 与 `residual_check.failed` 均使退出码非零；注入验证退出码 1 | 已达成 |
| 跳过如实报告 | `WARN [SKIP]` | 报告 `skip_reason`；跳过不计入通过 | 已达成 |

没有「随迁移消失」的核验项，也没有新增第三套核验机制。

容器零残留的判定仍是清单级：`guardContainerResiduals` 在 `t.Cleanup` 里调
`ContainerFinalizer.Finalize`，读整台设备的 `docker ps -a`；未登记但属于 testkit 前缀的容器判
failed，baseline 差异只报告不判失败，核验失败经 `t.Errorf` 使用例 failed 并让退出码非零。

---

## 6. 阶段内冻结的决策

| 编号 | 决策 | 定稿结论 |
|---|---|---|
| D1 | `RegisterFinalizer` 是否保留 | 删除。包级终检注册表、`RemoteDockerSession`、`RegisterContainerFinalizer` 一并删除，不留兼容层；用例级 `t.Cleanup` 是唯一落点 |
| D2 | unit 侧 `testkit.Main` 去留 | 保留，职责限定为运行根目录、`test.log`、stdout/stderr 与 slog 重定向、默认打开 `-test.v`；不再承担任何包级资源核验 |
| D3 | 两套产物路径 | unit 用 `<os.TempDir>/lingting-agent-flow-tests/<包名>-<随机>/`；迁移用例用 `.test-artifacts/<run-id>/` + `<os.TempDir>/lingting-agent-flow-tests/<run-id>/`；互不覆盖 |
| D4 | 核验缺口的补齐位置 | 无缺口需要补。若要补，仍在 R2 语义内（资源级自证 / 运行级终检），不新增第三套机制 |
| D5 | 旧辅助文件删除判据 | 编译 + 全仓引用检索为唯一判据；`internal/caseinventory` 中以字符串出现的符号名（`RequireRealTests`、`RegisterContainerFinalizer` 等）不算引用，它只服务 R0 清单生成工具，扫描输入已在 R5 删除 |
| D6 | `remote/workflow` 是否继续整包串行 | 解除整包串行：删除 `serialPackages` 中该条目，用例改 `Parallel: true`；依据与验证见下文 |

D6 的依据：每条用例的远端工作空间（随机后缀）、数据库、容器名、分支名都按用例隔离；设备级容器
核验只把 `agentflow-test-*` / `agentflow-codesaver-*` 前缀当测试资源，产品容器（`agentflow-<hex>`）
不参与判定，基线差异只报告不判失败；剩余共享面只有同输入的同名镜像构建。`remote/docker` 的包内
串行（R5 D12，`Parallel: false`）不受影响。

D6 的验证：同一组里串行时失败的 `e2e-full-chain` 与
`m6-project-serial-parallel-and-failure-blocking`，在 `--parallel 2` 并发下同时通过，耗时分别从
656s / 1069s 降到 215s / 311s；两次运行都是 12 次 `REMOTE_CONTAINER_VERIFY status=PASS`，
设备前后一致，没有出现「把对方容器判成未登记」的假失败。

---

## 7. 运行授权与执行记录

授权（本阶段一次生效）：全部已注册 Case 的本机集合、`tests/unit` 全量 `go test`、真实设备批次一次
（R5 B3 范围：`integration-ssh`、`remote/docker`、`remote/codesaver`、`remote/agent`、
`remote/planning`、`remote/workflow`）。禁止改断言让结果通过、禁止用本机替身代替真实批次。

| 批次 | 命令要点 | 结论 |
|---|---|---|
| unit 全量 | `go test ./tests/unit/... -count=1` | 16 个包全部 ok，退出码 0；1325 通过 / 7 跳过（Windows 无符号链接权限，既有跳过） |
| 覆盖对账 | `--audit-coverage --audit-stage R5,R6` | 清单 1707 / 范围 365 / 覆盖 316 / dropped 49 / 缺失 0 / 多余 0，退出码 0 |
| 本机绿组 | `--parallel 8 concurrencycheck resourcecheck selfcheck pilot e2e-cli integration-local` | 92 条：通过 91 + 协议跳过 1；零残留 passed；退出码 0 |
| 前端 | `--parallel 4 frontend` | 37 / 37 通过；零残留 passed；退出码 0 |
| 故障注入 | 按 R2/R3 记录的命令逐条执行 `selfcheck-fault/*`、`concurrencycheck-fault/*`、`resourcecheck-fault/release-failed` | 结论逐条一致，退出码 1；取消场景在跑 2 条记「运行被取消」、`cancel-c` 记「未派发」，无遗留进程 |
| 真实设备 | `--real-tests on` 逐包执行 | ssh 6/6；remote/docker 6/6；codesaver 2/2；agent 2/2；planning 8/9（GitHub `Connection reset by peer`，单条重试通过）；workflow 6/7 |
| workflow 并发复核 | `--parallel 2` 跑两条长用例 | `e2e-full-chain` 215s 通过、`m6-project-serial-parallel-and-failure-blocking` 311s 通过（D6 依据） |
| 外部阻塞 | `m6-requirement-sequential-tasks` 三次 | ①两步都通过后代码保存 `CodeSaveIndeterminate` → `recovery_required`；②`node:22.19.0-bookworm-slim` 元数据解析命中华为云镜像站 500；③任务在 `project_task_not_head` 闸门下排队 15 分钟未收敛。三次都不是断言或迁移差异 |

过程事故（如实记录）：一次把 `*-fault` 故障注入组并入全量批次，该批次 106 条按设计出现失败与超时，
不作为全量结论，随后按 R2/R3 的逐条命令重跑；一次长跑被中止，遗留 1 个产品容器，已清理并复核设备
回到基线（见 §4.4）。

---

## 8. 验收条件

1. **达成**：§5 对账表填满，9 项旧职责都有新落点，没有「随迁移消失」的核验。
2. **达成**：无引用的 `testkit` 组件已删除；`go build ./...`、`go vet ./...`、`gofmt -l` 无输出。
3. **达成**：16 个 unit 包 `TestMain` 行为不变；unit 全量退出码 0，跳过 7 条全部带原因。
4. **达成**：真实批次各包运行后 `residual_check` 均为 `passed`，真实设备包本身为 `not-applicable`，
   以用例级 `REMOTE_CONTAINER_VERIFY` 为准（12 次 PASS / 0 次 RESIDUE）。期间一次基线非测试容器
   消失已确认为设备维护者人工删除，不是测试误删。
5. **达成**：注入式释放失败仍使退出码 1 且零残留判 `failed`；跳过带原因且不计入通过。
6. **达成**：`finalize.go` 已删除，零残留核验只剩「用例级 `ContainerFinalizer` + 运行级
   `residual_check`」一套实现。
7. **部分达成**：除 `m6-requirement-sequential-tasks` 外全部已注册 Case 结论与 R5/R6 记录一致；
   该条两次运行分别被 Git 结果不确定与镜像站 500 阻塞，如实记为未通过，需要环境恢复后复跑一次。

---

## 9. 风险与对策

| 风险 | 实际发生 / 对策 |
|---|---|
| 删除组件后才发现 unit 仍依赖 | 未发生：以编译与引用检索为判据，删除后 unit 全量通过 |
| 旧核验能力被悄悄削弱 | 未发生：对账表逐项落点；容器核验仍读真实 `docker ps -a`，失败仍让用例 failed |
| 真实批次不可用 | 部分发生：GitHub HTTPS、镜像站 500、容器内模型 10 分钟超时都是设备侧故障；如实记录，未换本机替身 |
| 两套产物路径混淆 | 未发生：D3 划边界，R9 写入文档 |
| 中止运行不留零残留终检 | 发生一次：中止后编排进程不再做终检，残留容器需人工确认（§4.4），R9 运行手册需写清 |
| workflow 真并行的设备级核验误判 | 未发生：产品容器不匹配 testkit 前缀，baseline 差异只报告；两次并发运行 0 次 RESIDUE |
| 并发同名镜像构建 | 未观察到失败；两次并发运行都完成了基础与语言镜像构建 |

---

## 10. 不做的事

- 不迁移新用例（R5/R6 已完成）。
- 不修改 `Makefile`、CI 与文档（R8 / R9）。
- 不新增第三套核验机制。
- 不改写 unit 断言，只跟随接口变化调整调用点（本次没有调用点变化）。

---

## 11. 输出给 R8 / R9

1. 删除清单与保留清单见 §4.2；`testkit` 现只剩 unit 侧入口与迁移用例仍在用的资源辅助。
2. 对账表的最终结论（§5）供 R9 写入 `docs/testing.md`：零残留由「用例级 `ContainerFinalizer` +
   运行级 `residual_check`」两处承担，包级终检注册表已不存在。
3. 真实批次零残留复核记录（§4.4 / §7）：设备容器与卷前后一致，6 个真实包 12 次 PASS；
   一次中止事故的处置需写进 `docs/runbook.md`。
4. unit 侧入口的最终职责（§4.3 / D2 / D3）：`testkit.Main` 只负责运行根目录与日志，
   与 runner 产物目录互不覆盖。
5. R8 需要知道：`-list` 里 `integration-remote-workflow` 的并行属性已变为 `true`，`--parallel`
   上限即真实并发度；D6 解除了 `serialPackages` 中唯一一条登记（表保留为空表）。
6. R9 需要知道：`m6-requirement-sequential-tasks` 仍需一次复跑，外部故障恢复后按 §7 的真实设备
   命令单条执行即可。
