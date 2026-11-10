# AgentFlow V3：统一测试执行器路线总规划

**文档状态：** 主线规划。R0、R1 已完成，R2 起待执行。
**上游输入：** [审阅意见](TEST-RUNNER-REFACTOR-REVIEW.md)（P0/P1/P2 结论）、
[R0 盘点与决策](R0/README.md)、[R1 接口与可运行骨架](R1/README.md)、
[.plan/mian.md](../../.plan/mian.md) 的「测试调整」。
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

R0 冻结的规模基线（AST 口径，可用 `make inventory` 复现）：

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
| R5 | 328 | `tests/integration/**` 17 个包 + `tests/e2e/cli` |
| R6 | 37 | 前端 13 个 Vitest + 24 个 Playwright 文件 |

R1 已落地并冻结：`cmd/agentflow-test`、`internal/testrunner`（Case 注册表、上下文、句柄、worker、
报告）、16 类资源词汇表、`agentflow-case-handles/1` 句柄外壳、`agentflow-test-report/1` 报告、
真实依赖一次判定、Windows 进程树终止。一次 worker 启动约 20ms，单用例总开销约 30ms。

R1 明确留下的缺口（后续阶段逐项补齐）：资源提供者只有 `local-port`、串行执行、无取消、无零残留核验、
无 `go test` 适配、无覆盖对账、无前端适配。

R2 已落地并冻结：16 类资源的 payload / Spec 契约、13 类提供者（8 类本机含 `local-port` + 5 类远程）、`Provider` 新增
`Verify` 与 `Snapshot`、资源级自证与运行级基线比对终检、容量 / 独占键 / 包级串行三类调度输入，以及
`tests/runner/resourcecheck` 的 8 条本机资源自检用例。远程提供者本阶段只做编译与静态检查，未在真实
Pi / SSH / 远程 Docker 上运行；`node-worker` / `frontend-dev-server` / `browser` 仍由 R6 补齐。
R3 已落地并冻结：`internal/testrunner/scheduler.go` 是唯一并发决策者（`--parallel` 默认 8、容量、独占键、
`Case.Package` 串行作用域与包级串行），`cancel.go` 给出「停止派发 → 宽限 → 终止进程树 → 释放资源 →
零残留终检」的取消时序，`late.go` 让已定型结论不被迟到结果文件改写；报告新增 `concurrency` 与
`cancellation`，用例 `timings` 新增 `queue_enter_at` / `dispatch_at`。9 个调度场景由
`tests/runner/concurrencycheck` 覆盖（`sqlite-db` 容量在 R3 收紧为 4）。

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
| R5 | 执行中 | 迁移 18 个包 / 328 个集成与 E2E 用例，覆盖对账可执行 | [R5](R5/README.md) |
| R6 | 待执行 | 前端适配器：Vitest 13 + Playwright 24，一文件一 Case | [R6](R6/README.md) |
| R7 | 待执行 | TestMain 与 `testkit` 最终核验收口，删除迁移遗留 | [R7](R7/README.md) |
| R8 | 待执行 | Makefile / CI / 脚本收敛，非测试门禁保持可用 | [R8](R8/README.md) |
| R9 | 待执行 | 全量验收、覆盖对账、文档同步与 V3 收口 | [R9](R9/README.md) |

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
- R5 / R6 未完成，不得进入 R7 / R8。
- R8 未完成，不得进入 R9（R9 的验收必须跑在收敛后的门禁上）。

---

## 5. 完成标准

编号 9 与 14 采用 R0 第 4 节的改写口径，其余条目由审阅意见与各阶段出口合并而来。

1. `agentflow-test` 是集成测试与 E2E 测试的唯一执行器；`tests/unit` 仍由 `go test` 执行。
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
| `make test-local` / `test-e2e` / `test-pi` / `test-ssh` | 改为 `agentflow-test` 对应分组，真实依赖分组保留授权门禁 |
| `-count=1` | Case 默认不复用结果，不提供 `--count`；重复运行即重新执行 |
| `-short` | 不支持：现状 0 处 `testing.Short()` |
| `-timeout` | `--timeout`（默认 10m），Case 可用 `Timeout` 覆盖 |
| `-p` / `-parallel` | 合并为一个 `--parallel`，由调度器统一换算 |
| `-race` | 本次不提供（§3.3） |

---

## 8. 风险登记

| 风险 | 处置 | 归属 |
|---|---|---|
| 无 `-race` 门禁 | 显式接受，R9 写入文档与风险清单；恢复路径见 §3.3 | R0/R9 |
| `tests/unit` 仍依赖 `go test` | 路径 C 的既定结果；报告层聚合，不改写断言 | R0 |
| `t.Parallel()` 语义降级 | 1744 处统一映射为调度器属性；6 个 `sync.Once` 文件逐个人工判定 | R5 |
| Windows 进程树终止不可靠 | `taskkill /T /F` + 回收失败判 error，不用「已杀」冒充通过 | R1/R3 |
| 取消只能通过终止 worker 进程树生效 | worker 与编排进程之间不新增取消 IPC；宽限期后强制终止，回收失败判 error | R3 |
| 包级串行 / 独占键声明误配 | 声明只来自用例与 R2 冻结表，冲突表现为排队而非静默并行；R4.5 / R5 逐包复核 | R3/R5 |
| 前端冷启动开销 | R6 先测基线，再由用户确认可接受阈值后才批量迁移 | R6 |
| 共享 dev server 的写污染 | 写入必须可命名空间化，否则改独享后端 | R6 |
| 镜像构建属写入语义 | 构建产物走独立缓存通道，不与 `Destructive + Shared` 混用 | R2 |
| 测试工作空间是保留产物而非泄漏 | 零残留核验排除 `TEST_ARTIFACT_DIR` 与 `.test-artifacts` | R2 |
| 真实设备占用与残留 | 真实批次单独授权；运行前后各做一次快照 | R5/R7 |
| 覆盖清单与实际注册漂移 | `--audit-coverage` 作为 R5/R9 的硬门禁 | R5/R9 |
| `service-process` 端口存在「探测空闲 → 服务监听」竞争窗口 | 就绪判定在超时内重试，窗口期失败判「没有就绪」 | R2 |
| 远程用例漏声明 `ssh-host` 会让连接在终检里成为残留 | 有意保留：终检判 failed 并指出残留条目，强制声明依赖 | R2 |

---

## 9. 文档与 CI 同步清单（R8 / R9）

- R8：`Makefile` 测试目标、`.github/workflows/ci.yml` 测试相关 Job、`.github/scripts/` 逐脚本归属。
- R9：`docs/testing.md`、`docs/development.md`（常用命令）、`docs/workspace.md`（新增目录）、
  `README.md`（索引与命令）、`.github/scripts/README.md`、`docs/runbook.md`（若涉及测试排障）、
  `tests/README.md`（入口与目录说明）。
- 文档只描述已落地能力；R9 之前不得提前写入未实现命令。

---

## 10. 与 V2 的关系

V2 已收口（[../V2-final.md](../V2-final.md)）。V3 是 V2 之后的独立里程碑，R0 是它的第一个阶段；
不并入 V2 的阶段编号，也不替换 `.plan/mian.md` 的产品需求清单。V3 收口时新增 `.steps/V3-final.md`，
格式与 `V2-final.md` 对齐。

---

## 11. 阶段执行约定

1. 开始一个阶段前先读：本文 → 该阶段 README → 上游阶段 README。
2. 阶段文档里的「阶段内需要冻结的决策」必须在开工时定稿并写回该文档，不得边做边改接口。
3. 发现需求与冻结决策冲突时停止实现，向用户反馈后等待回复，不自行扩大范围。
4. 阶段完成后：更新本文 §4 状态、该阶段 README 的状态与验收记录，并把新事实补进 §2 与 §8。
5. 任一阶段不得跨阶段顺手清理其他阶段的遗留；R7 与 R8 是唯一的集中清理点。
