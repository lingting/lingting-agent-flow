# V3 R10：V3 收口后的遗留治理

**状态：** 未开始（本文件是计划，不是已完成记录）。
**上游文档：** [路线总规划](../ROADMAP.md)、[V3 收口文档](../../V3-final.md)、[R9 验收记录](../R9/README.md)。
**下游关系：** R10 只清理 V3 收口后暴露的缺陷与环境准备缺口，不改测试体系结构；测试目录收敛、
unit 迁移到统一入口与并发额度属于 [V4](../../V4/ROADMAP.md)，不在本阶段内做。

---

## 1. 目标

1. 把 CI #60 的三个失败从"只知道 job 名与断言位置"推进到"结论已定级、证据已归档"。
2. 修掉已确诊的 CI 环境准备缺口（`pilot/server-shutdown` 缺 `bin/agentflow`），不通过跳过用例解决。
3. 对两条并发/时序类失败分别给出**产品缺陷**或**用例观察点错误**的判定，并在有证据的前提下收敛。
4. 让 `.steps/V3-final.md` 与 `V3/ROADMAP.md` 的"一次完整 CI 尚未运行"等过期结论回到当前事实。
5. 不改写 V3 已冻结的历史记录（R0–R9 的状态、验收数字、决策一律保持原样，只追加勘误）。

## 2. 非目标

- 不恢复 `go test -race`，不新增覆盖率门禁（V3 已登记的已接受回退，恢复路径见 ROADMAP §3.3）。
- 不迁移 `tests/unit`、不改目录结构、不改调度器额度模型（属于 V4）。
- 不放宽断言、不增加 `sleep`/重试次数、不把失败改判为跳过。
- 不复跑单条失败 Job 就宣告完成：最终结论只来自一次完整 CI 运行。
- 不修改 `.steps/V3/R0/` 下的冻结 manifest 与矩阵。

## 3. 已核实的事实

### 3.1 本地与远端基线

| 项 | 事实 |
|---|---|
| 本地 HEAD | `465b8bb64c57bb3bc19f89de638a834727cfb438`（`:memo: v3 全量验收`，2026-11-21T18:42:42+08:00） |
| 工作区 | `## master...origin/master`，无未提交改动 |
| GitHub HEAD | 与本地一致：CI run 的 `head_sha` 就是 `465b8bb…` |
| 最新 CI run | #60 / `44895723793`，`event=push`，`run_attempt=1`，`2026-11-21T10:43:55Z → 10:53:30Z`，结论 `failure` |
| 历史 CI #59 | `44887452267`（`217b94a`，`:fire: 精简测试相关`，2026-11-21T09:00:01Z）结论 `failure`，但 Job 结构是 R8 之前的旧结构（含「Go 竞态检测」「Go 端到端测试」「前端单元与浏览器测试」），**不得与 #60 的失败混为一谈** |

### 3.2 CI #60 的 Job 结论

| Job ID | Job | 结论 | 时间窗 |
|---|---|---|---|
| 136820831909 | 测试分组核验与覆盖对账 | success | 10:43:59–10:44:51 |
| 136820831917 | 真实测试授权判断 | success | 10:43:58–10:44:26 |
| 136820831956 | Go 格式与静态检查 | success | 10:43:58–10:44:47 |
| 136820949005 | 真实外部依赖测试 | skipped | 10:44:26 |
| **136821043451** | **本机集成用例** | **failure** | 10:44:53–10:45:31（38s） |
| 136821043481 | Go 单元测试 | success | 10:44:54–10:46:31 |
| 136821043483 | CLI 端到端用例 | success | 10:44:53–10:45:42 |
| **136821043487** | **前端用例与门禁** | **failure** | 10:44:53–10:53:20（8m27s） |
| **136821043518** | **执行器自检用例** | **failure** | 10:44:53–10:45:45（52s） |
| 136823031264 | CI 汇总 | failure | 10:53:23–10:53:29 |

三个失败 Job 的步骤级结论完全一致：「执行…用例」步骤 `failure`，「上传用例产物」步骤 `success`；
`ui-test` 的「前端格式、lint、类型检查与生产构建」步骤因前一步失败而 `skipped`，因此**该门禁在 #60 中没有结论**。

### 3.3 失败 Job 的注解（未登录可见的唯一结构化输出）

每个失败 Job 都带 2 条错误注解，内容为「步骤退出码 1」加上下面这条：

| Job | 注解给出的日志与报告路径（Runner 内绝对路径） |
|---|---|
| 本机集成用例 | `.test-artifacts/local/cases-local-output.log`、`.test-artifacts/local/20261121t104524z-7d18/report.json` |
| 执行器自检用例 | `.test-artifacts/selfcheck/cases-selfcheck-output.log`、`.test-artifacts/selfcheck/20261121t104539z-4fb0/report.json` |
| 前端用例与门禁 | `.test-artifacts/frontend/cases-frontend-output.log`、`.test-artifacts/frontend/20261121t104552z-fe35/report.json` |

### 3.4 产物与取证能力

run 级 artifacts 共 4 个（`expired=false`）：

| Artifact ID | 名称 | 大小 |
|---|---|---|
| 14703776654 | `cases-local-44895723793-1` | 305,983 B |
| 14703558834 | `cases-selfcheck-44895723793-1` | 41,953 B |
| 14704331582 | `ui-test-44895723793-1` | 15,476,874 B |
| 14703931751 | `coverage-audit-44895723793-1` | 2,551 B |

取证限制（本次已实测）：

- 未认证访问 `archive_download_url` 返回 `401 Requires authentication`；本机没有 `gh`，也没有
  `GITHUB_TOKEN`，因此**本次无法直接读取 #60 的日志与 artifact 内容**，只能读到 job/step 元数据与注解。
- 未认证访问 GitHub Actions 的 job 日志页面显示「Sign in to view logs」。
- 因此下表把"已由项目 owner 提供的内容"与"必须重新取证的内容"分开标注，不把转述当成本机核实。

### 3.5 三个失败的已知内容与来源

| 失败 | 已知内容 | 来源 |
|---|---|---|
| 本机集成用例 | 183 条中 182 通过、1 失败：`integration-local-scheduler/concurrent-delete-and-scheduler-scan-keep-facts`（task `2e68b1ee360446b3d1812bd656c5afaf`），失败断言「分派结束后不得残留执行权」 | owner 提供的 #60 报告内容（需按 §5 A1 归档） |
| 前端用例与门禁 | 2 条 Playwright Case 失败：`frontend/playwright-task-containers`（处置成功但任务刷新失败时，处置结论仍可见）、`frontend/playwright-task-create-project-inheritance`（先选节点与环境、再选项目时，已选环境不应被项目默认值覆盖） | 同上 |
| 执行器自检用例 | `pilot/server-shutdown` 分配资源 `service` 失败：可执行文件 `bin/agentflow` 不可用（`no such file or directory`） | 同上 |

**结论状态：** 三个失败都还没有达到"根因已定级"。§4 分别给出已核实的代码契约、候选解释与所需证据。

## 4. 逐项分析与未决点

### 4.1 `integration-local-scheduler/concurrent-delete-and-scheduler-scan-keep-facts`

**用例位置：** `tests/runner/integration/local/scheduler/cases_requirement_delete.go`
（注册在 `cases.go`；迁移来源 `tests/integration/local/scheduler/requirement_delete_test.go`）。

**用例形状（已读源码）：** 10 轮，每轮新建一个 queued 任务，然后同时启动 `scheduler.Scan(ctx)` 与
`service.Delete(ctx, requirementID)` 两个 goroutine，用 `done` 通道等这两个 goroutine 返回；每轮断言
"需求删除状态与任务删除状态一致、删除未改写任务状态、任务仍可见时删除只以
`ErrRequirementStateConflict` 失败"；全部轮次结束后再断言两条不变量：

1. 调度器不得领取已被软删除的任务（`recorder.deletedClaims()` 为空）；
2. 对每一个被领取过的任务，`coordinator.Held(task)` 必须为 false。

失败的是第 2 条。

**已核实的执行权合同（产品侧）：**

| 环节 | 事实（源码位置） |
|---|---|
| 唯一登记处 | `app.TaskExecutionCoordinator`：`internal/app/task_execution.go` 的 `locks` 按 task id 计数，`Held` 读 `entry.refs > 0` |
| 调度器领取 | `TaskScheduler.dispatch`（`internal/app/task_scheduler.go`）先 `TryAcquire`，再用数据库事实复核任务未删除、领域准入仍成立，最后 `register` 并 `go s.run(grant)` |
| 分派失败/过期候选 | 三条提前返回路径都调用 `coordination.Release(grant)` |
| 启动返回后 | `run` 中若 `result.WorkflowStarted == false`，由调度器 `Release`；否则执行权转交工作流租约，工作流结束时释放 |
| 启动失败 | `settleFailedDispatch` 用 `defer coordination.Release(grant)`，并区分服务停止与用户取消 |
| 需求删除 | `RequirementService.Delete`（`internal/app/requirement_delete.go`）先 `lockRequirementTasks` 逐个 `TryAcquire`，任一失败即释放已取得的并返回 `ErrRequirementStateConflict`；成功后 `defer releaseTaskGrants` |
| 观察点 | `Scan` 在 `go s.run(grant)` **之后**立即返回；`Scan` 返回不代表分派协程已经 `Release` |

**关于用例侧的已核实事实：** 该用例的启动替身 `deleteAwareRecorder.Launch` 返回
`TaskLaunchResult{TaskID, Stage, TaskStatus}`，**没有设置 `WorkflowStarted`**，因此调度器会在 `run` 中走
"未启动工作流 → 立即释放执行权"的分支。用例等待的是 `Scan` 与 `Delete` 两个 goroutine 返回，
不是"分派协程已经释放执行权"。

**候选解释（均未确认，不得写成根因）：**

- (A) **用例观察点与收敛点不一致**：`Held` 断言在 `Scan` 返回后立刻检查，而释放发生在其后的
  `run` 协程中；`Launch` 里有一次 `db.GetTask` 读库，CI Runner 的 I/O 抖动会放大该窗口。
  若成立 → 修的是用例等待条件（轮询 `Held` 收敛，不 sleep、不放宽断言）；产品侧无缺陷。
- (B) **产品存在未释放路径**：某个分支取得执行权后既没有转交工作流也没有 `Release`
  （例如 `settleFailedDispatch` 之外的错误返回）。若成立 → 产品缺陷，必须在证据指出的分支上修。
- (C) **删除与领取的真实一致性缺陷**：删除成功返回后仍有分派路径持权并继续推进已删除任务。
  若成立 → 优先级最高的产品缺陷（违反"删除后不得残留执行权"的不变量）。

判定需要的最低证据（A1 阶段取回）：`report.json` 中该 Case 的 `message`（完整断言文本与实际 task id）、
`cases/<case-id>/stdout.log`、以及 `report.json` 的 `concurrency.max_in_flight` 与 `timings`。
**当前状态：未决（证据不足）。** 不允许按 (A) 直接改用例，也不允许按 (B)(C) 直接改产品。

### 4.2 `frontend/playwright-task-containers` 与 `frontend/playwright-task-create-project-inheritance`

**用例位置：** `tests/runner/frontend/case.go` 按文件登记（`playwright-<spec 名>` → 一条 Case），
spec 文件分别是 `lingting-agent-flow-ui/tests/e2e/task-containers.spec.ts` 与
`task-create-project-inheritance.spec.ts`。

**已核实的取证通道（`lingting-agent-flow-ui/playwright.config.ts`）：** `screenshot: only-on-failure`、
`trace: retain-on-failure`、`workers: 1`、`fullyParallel: false`、`retries: 0`、`outputDir` 与
`playwright-report` 都写进 `TEST_ARTIFACT_DIR`；执行器另外注入 `AGENTFLOW_UI_JSON_REPORT`。
因此 `ui-test-…` artifact 里应当同时有 trace zip、失败截图、html 报告与 JSON 报告 —— 这是唯一能定级的证据。

**必须区分的四类原因（当前都不能排除）：**

1. 产品缺陷：UI 行为确实违反 spec 描述（处置结论在刷新失败时应当仍可见；已选环境不应被项目默认值覆盖）。
2. fixture 污染：共享 `frontend-dev-server`（`run-shared`、容量 1）上的服务端状态在文件之间串味。
3. UI 异步时序：断言与请求/渲染完成的先后顺序竞争（V3 R6 曾记录两套入口同形态复现的既有抖动）。
4. CI 资源压力：4 vCPU Runner 上 `--parallel 4` + Chromium 冷启动导致的超时。

**已知的间接信号（不作为结论）：** `ui-test` Job 耗时 8m27s，明显长于"37 条 Case 的机器结论"所需时间，
与"个别用例跑满超时"一致；但 #60 的报告未取回，无法区分超时与断言失败。

**当前状态：未决（无 trace/截图/JSON 报告）。** 不得归因为"环境抖动"了事，也不得先加重试。

### 4.3 `pilot/server-shutdown`：CI 缺少 `bin/agentflow`

**已确诊（本机可直接核实，不依赖 artifact）：**

| 事实 | 位置 |
|---|---|
| 用例把 `serverBinaryPath()` 当作 `service-process` 的 `binary` 声明 | `tests/runner/pilot/server_shutdown.go` |
| `serverBinaryPath()` = `<repo>/bin/<serverBinaryNameOn(GOOS)>`，注释写明"构建由授权命令在仓库根目录完成：`go build -o bin/agentflow ./cmd/agentflow`" | `tests/runner/pilot/workspace.go` |
| 提供者用 `resolveExecutable` 校验路径：`os.Stat` 失败即返回"可执行文件 … 不可用" | `internal/testrunner/resource_process.go` |
| 资源分配失败在编排层是 **`error` 状态**（不是 skip）：`runCase` 把 `acquireCaseResources` 的错误写进 `CaseResult.Message` | `internal/testrunner/orchestrator.go` |
| `Makefile` 的 `test-cases` 只依赖 `test-runner`（构建 `bin/agentflow-test`），**不构建 `bin/agentflow`** | `Makefile` |
| CI 的 `cases-selfcheck` Job 只有「检出代码 / 安装 Go / 执行执行器自检用例 / 上传用例产物」，`run-cases.sh` 只构建 `bin/agentflow-test` | `.github/workflows/ci.yml`、`.github/scripts/run-cases.sh` |
| `check-go-static.sh` 只执行 `go build ./...`，不产出 `bin/agentflow` | `.github/scripts/check-go-static.sh` |

**因此这是确定的 CI 环境准备缺口，不是用例缺陷。** 但"最小修复"有决策点（见 D2），并且**不允许**
把 `pilot/server-shutdown` 改成跳过或按二进制缺失自动 skip —— 那会把"环境没准备"伪装成"无需执行"。

### 4.4 inventory 旧路径结论的纠正

V3 文档里有两处关于盘点的表述需要纠正，且**都不是"扫描输入还是旧目录"**：

- `internal/caseinventory/inventory.go` 的 `scanGoSources` 已经是通用扫描：遍历 `git ls-files` 的
  **全部已跟踪 `.go` 文件**，`tests/` 前缀或 `_test.go` 后缀才纳入；不按 `tests/integration/**`、
  `tests/e2e/**` 过滤。
- 真正陈旧的是**阶段映射**：`stageForGo` 仍按 `tests/integration/`、`tests/e2e/` 前缀判定 R5，
  `resources.go` 的 `isIntegrationOrE2E` 同样按这两个已删除目录判定。当前 HEAD 上重新生成清单会得到
  "全部 `keep-go-test`、R5 范围 0 条"，等于把 365 条的迁移分母清零。

因此正确表述是：**扫描框架已经通用，阶段分类仍是旧路径口径；R0 清单必须继续按冻结件使用，
重新生成清单前必须先改阶段映射（并重新立项冻结新分母）。** R10 只做文档勘误（D5），改代码属于后续立项。
### 4.5 证据归档（A1 完成后填写）

本节只记录"证据在哪里"，不写推断。A1 完成后必须填满；未填满时不得进入 C1 / D1 的定级。

| 失败 | artifact | 归档文件 | SHA256 |
|---|---|---|---|
| 本机集成用例 | `cases-local-44895723793-1`（14703776654） | 待归档 | 待归档 |
| 执行器自检用例 | `cases-selfcheck-44895723793-1`（14703558834） | 待归档 | 待归档 |
| 前端用例与门禁 | `ui-test-44895723793-1`（14704331582） | 待归档（report.json、trace、截图、JSON 报告） | 待归档 |

同时记录：run id、job id、`head_sha`、归档时间、归档后被忽略路径（`.test-artifacts/v3-r10-ci60/`）。

## 5. 实施步骤

### A1 取证与归档（不需要运行任何测试）

- 由项目 owner 在本机以已登录会话导出三个失败 Job 的 artifact（`gh run download 44895723793` 或网页下载），
  解压后把下列文件复制进**被忽略**的 `.test-artifacts/v3-r10-ci60/`（不得提交）：
  `cases-local/20261121t104524z-7d18/report.json`、`cases-local/cases-local-output.log`、
  `cases-selfcheck/20261121t104539z-4fb0/report.json`、
  `frontend/cases-frontend-output.log`、`frontend/20261121t104552z-fe35/report.json`、
  `frontend/*/cases/frontend__playwright-task-containers/artifacts/`（trace、截图、JSON 报告）。
- 在本文 §4.5 填好证据归档表：run id、job id、artifact id、文件清单与 SHA256（不写内容摘要以外的推断）。
- 产出：`#60` 的证据索引 + 每个失败的"已确认事实 / 仍未知"两栏表。

### B1 修 `pilot/server-shutdown` 的前置条件（已确诊，优先）

- 决策 D2 定稿后改：
  - `Makefile`：给需要被测二进制的入口加显式前置（`test-cases` 在 `TEST_KIND` 含 `selfcheck` 时先
    `build`，或新增 `test-cases-selfcheck: build test-runner`），或
  - `.github/workflows/ci.yml` 的 `cases-selfcheck` Job 增加一步 `make build`，或
  - `run-cases.sh` 增加 `--require-binary` 前置校验并给出可执行的错误信息。
- **不得**改用例断言、不得把缺失二进制转成 skip。
- 验收：`make test-cases TEST_KIND=selfcheck` 在本机（授权后）与 CI 上都通过；`pilot/server-shutdown`
  的报告状态为 `passed`，且 `resources[].name == "service"` 的记录出现。

### C1 处理 `concurrent-delete-and-scheduler-scan-keep-facts`

- 先完成 A1；按 §4.1 的候选解释定级，并把结论写进 §4.1 的"结论状态"与 §4.5 的证据表。
- 若为 (A)：把"等待 `Held` 收敛"改成等待条件（`waitFor` 轮询 + 明确的失败信息），
  不改断言语义、不 sleep 固定时长。
- 若为 (B)/(C)：先补一条**最小复现合同**（在 R10 内只允许修产品缺陷本身），修复点必须是证据指出的分支。
- 验收：该 Case 在 `--parallel 4` 下连续 3 次通过（每次独立 run-id），且失败信息在人为构造的
  残留现场下仍能准确报错（新增的等待条件不得掩盖真实残留）。

### D1 处理两条 Playwright 失败

- 先完成 A1；按 trace/截图/console/network 与 JSON 报告定级（产品 / fixture / 时序 / 资源压力）。
- 若为产品行为：修 UI 与对应断言（断言语义按 spec 描述，不放宽）。
- 若为 fixture 污染：改为 `case-exclusive` 的 dev server 或加可验证的命名空间；资源成本变化写进风险。
- 若为时序：只允许"等待可观察条件"的改造（如等待请求完成、等待元素稳定），**不允许**加 `retries`。
- 若为资源压力：记录证据并在 D6 决定是否调整 `--parallel`，不改断言。
- 验收：`make test-cases TEST_KIND=frontend` 单次运行 37/37 通过，且两次独立运行结论一致。

### E1 完整 CI 复跑

- 需要用户明确授权：推送提交并触发一次完整 CI；**不得**只重跑三个失败 Job。
- 记录新 run 的 run id、`head_sha`、全部 Job 结论、artifact 清单；与 #60 对比。
- 验收：`ci-summary` 通过，`residual_check` 全部 `passed`，`ui-test` 的门禁步骤不再是 `skipped`。

### F1 文档勘误

| 文件 | 改动 |
|---|---|
| `.steps/V3/ROADMAP.md` | 新增 §12 R10 章节；§4 表格追加 R10 行（状态为进行中/已完成按实际）；§2 增加一条"盘点输入口径勘误"注记 |
| `.steps/V3-final.md` | 追加「补遗：2026-11-21 之后的验收勘误」，把"一次完整 CI 尚未运行"改为 CI #60 已运行且失败的准确事实；纠正 §7 第 3 条的表述 |
| `docs/testing.md` | 只在 D2 改变了构建前置条件时更新对应命令说明；不写未实现能力 |

## 6. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 | 状态 |
|---|---|---|---|
| D1 | CI #60 三个失败的定级口径 | 每条必须先给出"已确认事实 / 仍未知 / 需要什么证据"，定级只允许四类：产品缺陷、用例观察点错误、环境准备缺口、未决 | 待定稿 |
| D2 | `pilot` 被测二进制的最小 CI 前置 | 在 Case Job 里显式构建（`make build`），而不是让 `run-cases.sh` 猜测哪些 kind 需要它；本地入口同步加依赖，保证 CI 与本机前置一致 | 待定稿 |
| D3 | 并发用例的等待条件写法 | 只允许"轮询可观察状态收敛"，禁止固定 `sleep`；等待失败时必须打印现场（`Held` 的任务集合） | 待定稿 |
| D4 | Playwright 失败的处置边界 | 时序问题只用条件等待；`retries` 保持 0；若确需提高稳定性上限，单独决策并记录证据 | 待定稿 |
| D5 | inventory 陈旧结论的勘误位置 | 写在 `V3-final.md` 补遗 + `ROADMAP` §2 注记，**不改动** R0 冻结件；代码修复另立项 | 待定稿 |
| D6 | `--parallel` 是否调整 | 只在 D1 证据指向资源压力时才调整，并同时记录对其它 kind 的影响 | 待定稿 |

## 7. 运行授权申请

R10 的默认状态是**只做静态核查**（读代码、`git diff`、`gofmt -l`、`go vet`、`make check` 的不触发测试部分）。
需要运行测试的场合必须逐项授权：

| 步骤 | 申请执行的集合 | 申请命令 | 说明 |
|---|---|---|---|
| C1 | 单条 Case | `./bin/agentflow-test integration-local-scheduler/concurrent-delete-and-scheduler-scan-keep-facts`（`--parallel 4`，重复 3 次） | 只跑该条，不跑整个 local 分组 |
| C1 | 该分组回归 | `make test-cases TEST_KIND=local` | 确认改动不影响 183 条里的其它用例 |
| D1 | 两条 Case | `./bin/agentflow-test frontend/playwright-task-containers frontend/playwright-task-create-project-inheritance` | 先单条复现，再整体 |
| D1 | 前端分组回归 | `make test-cases TEST_KIND=frontend` | 37 条 |
| B1 | 自检分组 | `make test-cases TEST_KIND=selfcheck`（先 `make build`） | 覆盖 `pilot/server-shutdown` 与 `pilot-fault/server-not-ready` 两条 |
| E1 | 完整 CI | 推送后由 GitHub Actions 执行 | 需要单独授权推送 |

明确不允许：修改断言换取通过；把失败改判为跳过；只重跑失败 Job 就宣告完成；跳过真实设备分组却声明完成。

## 8. 风险与停机点

| 风险 | 处置 |
|---|---|
| 把用例观察点问题误判成产品缺陷，做出无收益的"修复" | 定级必须有 artifact 证据；证据不足时状态写"未决"并停下来等 owner 决策 |
| 用等待轮询掩盖真实残留 | 新增等待必须同时提供一个"真实残留仍会失败"的反向验证场景 |
| CI 环境修复被扩大成"给所有 kind 预构建被测二进制" | D2 只针对需要 `bin/agentflow` 的入口，其余入口不变 |
| 前端失败被当成抖动长期挂账 | D4 明确 `retries` 不变；每次复跑都要给出与上次不同的证据或同一证据的稳定复现 |
| 修改 R0 冻结件导致分母漂移 | R0 目录只读；盘点结论只写进文档 |
| 只重跑失败 Job 造成"绿了但没验证" | E1 强制完整 CI，对比全部 Job 结论 |

## 9. 验收条件

1. **证据归档完成**：三个失败的 artifact 已被引用，文件清单与 SHA256 写入本文 §4.5，且都存放在被忽略路径下。
2. **定级完成**：三个失败各自给出四类定级之一；仍为"未决"的必须写清缺什么证据与谁来决定。
3. **环境准备修复完成**：CI 的 `cases-selfcheck` Job 能构建到 `bin/agentflow`，`pilot/server-shutdown`
   报告为 `passed`；本机 `make test-cases TEST_KIND=selfcheck` 走同一条前置。
4. **并发用例收敛**：`concurrent-delete-and-scheduler-scan-keep-facts` 连续 3 次通过，且反向验证仍会失败。
5. **前端收敛**：`frontend` 分组 37/37 通过，两次独立运行结论一致；不做重试掩盖。
6. **一次完整 CI 通过**：`ci-summary` 成功，所有 Job 有结论（`ui-test` 门禁步骤不再 `skipped`）。
7. **文档勘误完成**：`V3-final.md` 与 `ROADMAP` 的过期结论已按 D5 纠正，R0–R9 历史记录保持原样。
8. **没有范围外改动**：未改 `tests/unit`、未改目录结构、未改调度额度模型、未恢复 race/覆盖率。

## 10. 不做的事

- 不做 V4 的目录收敛与 unit 迁移（见 [V4](../../V4/ROADMAP.md)）。
- 不写新的测试框架能力（额度、池、批量 worker 都属于 V4）。
- 不删除 `tests/internal/testkit`，不改 `testkit.Main`（unit 迁移时才会动）。
- 不重新生成 `.steps/V3/R0/` 的任何冻结件。
- 不为"看起来更稳"而增加重试、放宽断言或扩大超时。

## 11. 与 V4 的边界

R10 结束的判定是"CI #60 的三个失败有结论、环境缺口已修、完整 CI 通过"。
它**不**产生下列变化，这些全部留给 V4：

- `tests/internal/testkit` → `tests/testkit`；
- `tests/runner/*` → `tests/*`；
- unit 用例从 `go test` 迁到 `agentflow-test`；
- 包级并发上限与跨包命名并发池。

V4 的第一阶段（[V4/R0](../../V4/R0/README.md)）会重新盘点当前 HEAD 的用例面；R10 的结论与证据
作为 V4 R0 的输入之一，但 V4 不依赖 R10 的具体实现细节。
