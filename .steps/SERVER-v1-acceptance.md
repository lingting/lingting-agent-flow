# AgentFlow 服务端第一版验收记录

> 本文件是对 **服务端第一版（P1 + P2 + P3 的服务端范围）是否真正完成** 的一次独立验收，
> 不是阶段文档的复述。结论只以本轮实际阅读的生产代码与当前基线上实际执行的测试为依据；
> P1 / P2 / P3 中的 `completed` 标记与历史测试记录一律不作为结论依据。
>
> 撰写时间：2026-10-29。验收基线：`origin/master` =
> `9c79a2c30fecc09d1bb15dbb9fe2fd51ee34bfe7`（`git fetch origin` 后确认本地 HEAD 与远端一致，
> 工作区干净、无未提交修改）。

## 0. 结论摘要

- **最终判定：`completed`。**
- **代码层面**：服务端第一版必需链路在基线 `9c79a2c` 与本轮改动之后全部成立，并且有可复核的
  本地证据（真实 SQLite + 正式迁移 + 生产服务装配，只替换 SSH / Docker / 容器内 Pi / 远程 Git
  这些外部依赖）。
- **真实远程**：完整链路（容器内 Git Clone → 计划 → 开发 → 确定性验证 → 语义审查 → 代码保存 →
  会话归档 → 收尾）已在本轮真实跑通（§5）：容器真实创建、仓库 `commit=92f6432` 真实检出、
  2 个步骤全部通过、代码保存 `push=succeeded`、5 个会话全部归档、`workflow_runs.status=completed`、
  `tasks.status=completed` / `result=succeeded`。同一轮的其他真实远程套件也全部通过：docker 6、
  agent 2、codesaver 1、workflow 的预检与主机密钥校验 3。
- **过程记录（保留可追溯）**：本轮上午同一套件曾失败，原因是远程侧出网被阻断/过滤（容器内
  `git clone` 已走镜像内置的 `ssh.github.com:443`，TCP 建连后收不到 SSH banner；宿主机拉镜像也
  超时），**不是端口选择问题、也不是产品缺陷**（实证见 §5.3）。调整远程主机网络配置后重跑即通过。
- **本轮实际修复了 1 处第一版必需缺陷**：验证失败只由 Validator 语义审查给出时，结构化反馈
  不会被写入，修复循环因此在 `feedback_unavailable` 处停止，并被归类为"验证证据未可靠写入"
  （§6）。修复后该路径能进入有上限的修复循环，且停止原因如实（回归结果见 §6.5）。
- **没有任何第一版必需能力被降级为非目标。** 被推迟的项都是既有文档已明确记录的非目标（§2.3），
  以及 3 项不阻塞第一版的后续增强（§2.5）。

## 1. 验收方法与证据分级

本轮把证据分三级，结论只取前两级：

| 级别 | 含义 | 本轮使用方式 |
|---|---|---|
| 一级 | 本轮在当前基线上**实际执行的命令与结果** | 作为验收结论的直接依据（§4、§5） |
| 二级 | 本轮**阅读到的生产调用链** | 用于确认"能力是否真的接在生产路径上"（§3 的代码证据列） |
| 三级 | 历史文档中的 `completed` / `PASS` / 历史测试计数 | 仅用于定位，不作为任何验收结论 |

具体手法：

1. 先按文档索引读 `.steps/README.md`、`ROADMAP.md`、P1、P2、P2-M1、P2-M5、P3、P3-M5、
   `tests/README.md`、`README.md`、`Makefile`，列出文档声称已完成的能力清单。
2. 对每条能力回到生产代码确认"是否真的被接线"：从 `internal/cli/server.go` 的工厂函数
   出发，确认服务构造与 `internal/server/server.go` 的生命周期注册顺序，再看应用层与领域层
   判定是否只有一处定义。
3. 对每条能力定位当前基线下的测试，确认它是"链路级"还是"服务级/纯函数级"，不把后者当作
   前者的证据。
4. 执行本地自动化验证与真实远程验证，逐项记录通过 / 失败 / 跳过 / 未执行 / 无法确认。
5. 发现真实缺口后按"对端到端正确性的影响"排序，本轮只修最高优先级的一处，并补项目级测试。

## 2. 第一版正式范围裁定

### 2.1 第一版必需且已实现（代码 + 测试证据齐备）

| 能力 | 代码落点 | 说明 |
|---|---|---|
| 统一启动（容器准备 → 工作区初始化 → 计划 → 编排） | `app.TaskLaunchService`（`internal/app/task_launch*.go`）、`domain.DecideTaskLaunchNext`（`internal/domain/task_launch.go`） | 阶段顺序与"从哪一阶段继续"只由领域函数决定，Handler / CLI 不重复判定 |
| 任务级工作区初始化 | `app.TaskWorkspaceService`（`internal/app/task_workspace*.go`） | 以"任务 + 容器代次"为幂等边界，早于任何 Step / Attempt；结果落 `task_workspace_initializations` |
| 执行环境准备与所有权 | `app.ExecutionEnvironmentService.PrepareForTask`（`internal/app/execenv*.go`） | 每次全新容器，准备成功后才写回任务；不自动接管旧容器 |
| Planner 生成并持久化计划 | `app.TaskWorkflowService.GeneratePlan`（`task_workflow_plan*.go`）、`domain.BuildPlan`（`domain/plan_generation.go`） | 计划必须落库到 `steps`；`plan` 步骤固定为序号 1；任务级必需检查合并进每一步，Planner 不能省略 |
| Developer / Validator 独立 Session | `app.StepExecutor`、`app.ValidatorReviewer`、`app.SessionService` | 每次调用一次 Session，进程启动前先写库；Validator 挂在开发 Attempt 之下但会话记录独立 |
| 确定性验证与语义审查分离 | `app.ValidationService`（`validation*.go`）、`domain.DecideValidation`（`domain/validation_decision.go`） | 结论只由纯函数依据结构化事实计算；语义审查只能收紧 |
| 有上限的修复循环 | `domain.DecideRepairLoop` / `DecideRepairBudget` / `DecideRepairStart`、`app.StepWorkflowService.runRepairLoop` | 预算来自持久化的 `repair_cycles` + `attempts.budget_scope*`，不是进程内计数 |
| 代码保存（commit / push / 可选 merge） | `internal/codesaver/*`、`app.TaskWorkflowService.saveCode` | 三阶段状态分别持久化；默认不合并；不默认 force push |
| 代码保存结果核验与恢复 | `app.CodeSaveVerificationService`（`code_save_verify.go`）、`domain.DecideCodeSaveResume`、`app.CodeSaveRecoveryService` | 只读观测 + 条件写回；`unknown` 先核验再决定状态 |
| 会话归档与任务收尾 | `app.TaskWorkflowService.finalize/applyCloseout`、`domain.DecideWorkflowCloseout`、`domain.DecideTaskCloseoutView` | 收尾写入顺序"先执行记录、后任务"，执行记录条件写入即栅栏；归档缺失不判成功 |
| 取消、中断、重启恢复 | `app.TaskLaunchService`（启动恢复）、`app.ExecutionResidueService`、`app.ContainerRecoveryService`、`app.CodeFetchRecoveryService`、`app.TaskLeaseRecoveryService`、`domain.DecidePlanGenerationRecovery` / `DecideCancellationRecovery` | 恢复只依据数据库事实，不重放外部副作用；恢复失败阻止启动 |
| 显式重试与续跑资格 | `domain.DecideTaskRetryEligibility` / `ReopenForRetry`、`app.TaskRetryService`、`app.ResumeEligibilityService` | 只接受领域允许的现场；重试复用统一启动，不建第二套流程 |
| 有界并发与隔离 | `app.TaskScheduler`（`task_scheduler.go`）、`domain`（`task_queue.go` / `task_lease.go` / `container_ownership.go`） | 名额上限 + `lease_epoch` fencing + 容器归属校验；跨库全局配额不是第一版要求 |

### 2.2 第一版必需：本轮从"证据不足"变为"已确认"

| 项 | 验收前状态 | 本轮处置与结果 |
|---|---|---|
| 语义审查失败驱动的修复循环 | 原实现只对"未通过的确定性检查"生成结构化反馈，审查失败路径直接停在 `feedback_unavailable`（真实缺口） | **本轮修复**（§6），补 2 个链路级用例，回归通过 |
| 真实远程完整链路成功路径 | 上午因远程侧出网被阻断/过滤无法复核（环境限制） | 远程主机网络调整后本轮真实跑通（§5.3），本地与远程证据齐备 |

### 2.3 第一版明确不包含（不得在本轮被扩大为要求）

| 项 | 依据 |
|---|---|
| 私有仓库 Git 凭据存储 | P1 已记录为架构决策：需要凭据存储设计与安全评审；`GitCredentialProvider` 仅是接口占位 |
| 计划版本化 / 重新规划 | `ROADMAP.md` 暂缓事项：当前单次计划足够支撑第一版 |
| Git Worktree 文件系统隔离 | 第一版隔离边界是"每任务一个容器 + `agent/<task-id>` 分支"，不引入 Worktree |
| 无用户确认的自动重试与退避 | 调度器只领取 `queued`；失败任务必须由用户显式重试 |
| 人工核验后自动继续 | 核验入口仍是显式操作（CLI / HTTP），不自动重放 |
| 跨数据库全局并发配额 | 并发上限语义固定为"同一份数据库上的名额上限"（P3-M5 已裁定） |
| 本机执行环境实现（`local_linux` / `local_darwin` / `local_windows`） | `internal/execenv` 明确返回 `ErrUnsupportedKind`，禁止回退本机执行 |

### 2.4 文档与代码不一致（本轮已裁定并修正）

根 `README.md` 存在两处与当前代码直接矛盾的内容，本轮已修正（详见 §7）：

1. `## 第一版功能范围` 下的"第一版目标（尚未实现）"清单仍把**持久化任务队列与并发上限、
   Validator Agent 环节、验证失败修复循环、远程容器自动编排、Commit/Push/Merge 自动编排、
   可恢复的任务状态、业务 Web 页面**列为未实现。
2. `### 尚未实现的接口` 仍称"项目、环境、容器、角色均没有 HTTP 接口""代码保存没有记录列表
   接口""Web 未接入 `/start`、计划生成、步骤执行、验证历史、失败反馈与重试入口"。

两份清单与 `internal/httpapi` 的 34 条路由、`internal/cli/server.go` 的服务装配、
`internal/app` 的实际实现相反，属于"文档落后于代码"，不是"代码缺失"。

### 2.5 明确的后续增强（不阻塞第一版）

| 项 | 现状 | 为什么不阻塞 |
|---|---|---|
| merge 结果不确定后的真实远程核验 | 判定逻辑（`domain.DecideCodeSaveResume` 等）已由真实 Git + bare 远程的 12 个恢复用例覆盖；真实远程只覆盖了"合并在正常情况下成功" | 判定只消费"观测到的 Git 事实"，远程容器不引入新的判定分支；补该用例属于证据完整性增强 |
| Clone 失败信息中的实际传输目标 | 错误文本使用脱敏后的 `ssh://host/path`，不显示实际连接的 host:port 与传输类型 | 只影响诊断可读性，不影响判定（本轮已用离线实证确定目标为 `ssh.github.com:443`） |
| `TestRemoteE2EFullChain` 对 `blocked` / `recovery_required` 的容忍 | 该用例在"链路跑完并如实收敛"时即通过，不强制成功终态 | 真实模型输出不确定，强制成功会让用例不稳定；成功路径已由本轮一次真实通过 + 本地链路级用例共同证明 |

## 3. 服务端第一版验收矩阵

### 3.1 统一启动（环境 → 工作区 → 计划 → 编排）

- **必需行为**：从 `POST /api/v1/tasks/{taskID}/start` 推进任务；已完成的阶段不重复执行；
  任一阶段失败立即停止且不重放外部操作；部分失败、结果不确定与中断都有明确落库事实。
- **代码证据**：`internal/app/task_launch_run.go:Launch`（阶段分支与 `fallthrough` 顺序固定在
  `container → workspace → plan → workflow`）、`internal/domain/task_launch.go`、
  `internal/app/task_launch_recovery.go`、`internal/httpapi/task_launch.go`、
  `internal/cli/server.go` 的 `TaskLaunchFactory`。
- **测试证据**：`tests/integration/local/workflow/launch_test.go`（9 个，含
  `LaunchDoesNotCreateSecondContainer`、`LaunchInitializesWorkspaceBeforePlan`、
  `LaunchRejectsSecondCallWhileRunActive`、`WorkspaceInterruptedConvergesToUncertain`）、
  `launch_chain_test.go`（4 个，含 `LaunchChainIsIdempotentOnRepeat`、
  `LaunchChainRejectsUncertainWorkspaceWithoutReplay`）、
  `tests/integration/local/workflow/e2e_success_test.go`（5 个，从 `queued` 任务起步）。
- **本轮结果**：通过。
- **备注**：同一条链路在真实远程上本轮也跑通（§5.2）；本地证据使用真实 SQLite 与真实
  `TaskLaunchService`，只替换外部执行端口。

### 3.2 开发与验证（确定性检查 + 语义审查 + 状态收口）

- **必需行为**：Developer 真实执行；必需检查真实运行并逐项落库；Validator Agent 的语义
  审查进入生产决策链；确定性失败不能被模型结论覆盖；未通过不得返回成功。
- **代码证据**：`internal/app/step_executor*.go`、`internal/app/validation_run.go`
  （`reviewSemantics` 在确定性检查全部通过后调用审查器）、`internal/app/validation_review.go`、
  `internal/domain/validation_decision.go:DecideValidation`、
  `internal/domain/step_verification.go:DecideStepCloseout`、
  `internal/app/step_workflow_run.go:closeout`。
- **测试证据**：`tests/unit/domain/validation_decision_test.go`（16 个，含"审查不得放宽"),
  `tests/unit/app/validation_review_test.go`（14 个，含 `TestReviewFailureTightensPassed`、
  `TestReviewCannotOverrideDeterministicFailure`、`TestReviewSessionIsNotReused`）、
  `tests/integration/local/workflow/e2e_success_test.go`（`E2EHappyPathReachesCompleted`、
  `E2EHappyPathPersistsExecutionRecords` 断言 `review_status=passed` 与逐项检查结果）、
  本轮新增 `tests/integration/local/workflow/e2e_review_test.go`（2 个，见 §6.4）。
- **本轮结果**：通过。修复前缺口见 §6（审查失败虽不会伪造成功，但无法驱动修复且停止原因失真）。

### 3.3 修复循环（有上限、有停止条件、保留历史）

- **必需行为**：验证失败后可依据结构化反馈重新开发并重新验证；预算跨进程持久；上限、
  取消、超时、基础设施异常、记录未可靠落库、结果不确定都停止；历史尝试全部保留。
- **代码证据**：`internal/domain/retry.go:DecideRepairLoop`、
  `internal/domain/repair_cycle.go:DecideRepairBudget` / `DecideRepairStart`、
  `internal/app/step_workflow_repair.go:runRepairLoop`、
  `internal/app/step_workflow_run.go:persistFeedback`、
  `internal/app/step_workflow_prompt.go:stepWorkflowPrompt`。
- **测试证据**：`tests/integration/local/workflow/e2e_repair_test.go`（4 个）、
  `e2e_repair_limit_test.go`（5 个）、`repair_budget_test.go`（10 个，含跨重启与并发开新周期）、
  `delivery_chain_test.go`（`DeliveryChainRepairThenCodeSaveArchiveAndCloseout`、
  `DeliveryChainStopsAtRepairLimit`）、`tests/unit/domain/repair_loop_test.go`（12 个）、
  本轮新增 `e2e_review_test.go:TestE2EValidatorReviewFailureFeedsRepairLoop`。
- **本轮结果**：通过（修复后覆盖"失败只来自语义审查"这一分支）。

### 3.4 代码保存（三阶段事实 + 结果不确定可核验）

- **必需行为**：按真实执行结果 commit / push，merge 是可选且默认关闭；三阶段状态分别持久化；
  外部操作成功但记录写入失败必须留下不可信标记；恢复先核验外部现场再决定状态。
- **代码证据**：`internal/codesaver/saver.go` / `merge.go`、`internal/app/code_save.go`、
  `internal/app/code_save_verify.go`、`internal/domain/code_save.go`、
  `internal/domain/code_save_recovery.go`、`internal/app/code_save_recovery.go`、
  `internal/app/task_workflow_exec.go:saveCode`。
- **测试证据**：`tests/integration/local/codesaver/codesaver_test.go`（12 个，含
  `TestPushFailureIsNotReportedAsSuccess`、`TestMergeConflictKeepsInspectableState`、
  `TestCommitSucceededButRecordFailedIsRecoverable`）、
  `tests/integration/local/codesaver/recovery_test.go`（12 个，含
  `TestRecoveryDoesNotAttributeForeignCommit`、`TestRecoveryUnconfirmedMarkerSurvivesWriteFailure`、
  `TestRecoveryMergeBaseAdvancedByOthersStaysUnverifiable`）、
  `tests/integration/local/workflow/e2e_indeterminate_test.go`（6 个）、
  `tests/integration/remote/codesaver/codesaver_test.go`（真实远程容器内 commit + push + merge，
  本轮通过）。
- **本轮结果**：通过。真实远程"merge 结果不确定后的核验"仍未覆盖（§8）。

### 3.5 会话归档与收尾（成功必须有证据）

- **必需行为**：commit / push / merge 与归档事实一致；只有满足交付与归档条件才判成功；
  写入顺序为"执行记录 → 任务"，执行记录条件写入是栅栏；遗留不一致只能被发现，不能被当作成功。
- **代码证据**：`internal/domain/workflow_run.go:DecideWorkflowCloseout`、
  `internal/app/task_workflow_exec.go:finalize` / `applyCloseout` / `sessionsPersisted` /
  `sessionArchived`、`internal/domain/task_closeout.go:DecideTaskCloseoutView` /
  `DecideTaskCloseoutRecovery`、`internal/app/task_workflow_delivery_recovery.go`。
- **测试证据**：`tests/integration/local/workflow/closeout_consistency_test.go`（6 个，含
  `TestCloseoutRunWriteFailsBeforeTaskWrite`、`TestRestartFlagsLegacyCompletedWithRecoveryRun`）、
  `delivery_chain_test.go`（含 `DeliveryChainStopsWhenSessionArchiveIncomplete`、
  `DeliveryChainStopsWhenCodeSaveRecordUnconfirmed`、`DeliveryChainStopsWithoutCodeSaver`）、
  `tests/integration/local/workflow/e2e_success_test.go` 的代码保存记录与会话归档断言。
- **本轮结果**：通过。

### 3.6 恢复与重试（中断不伪成功、不盲目重放）

- **必需行为**：超时 / 取消 / 服务停止 / 进程崩溃的语义可区分；残留 Attempt / Session /
  工作区 / 执行记录 / 代码保存记录可被发现与核验；恢复资格遵守领域规则；失去执行权者不得
  写成功终态；结果不确定不得允许盲目重试。
- **代码证据**：`internal/app/task_launch_recovery.go`、`task_workspace_recovery.go`、
  `execution_residue.go`、`container_recovery.go`、`code_fetch_recovery.go`、
  `code_save_recovery.go`、`task_lease_recovery.go`、`internal/app/resume_eligibility.go`、
  `internal/domain/task_retry.go`、`internal/domain/code_save_recovery.go`、
  `internal/server/server.go:startServices` 的注册顺序（恢复先于调度）。
- **测试证据**：`tests/integration/local/workflow/recovery_test.go`（11 个，含
  `RecoverUnfinishedRunOnRestart`、`RecoverCancellingOnRestart`、
  `ConcurrentRecoveryConvergesOnce`、`PlanGenerationInterruptedByShutdown`）、
  `cancel_intent_test.go`（5 个）、`step_occupancy_test.go`（5 个）、
  `retry_e2e_test.go`（4 个）、`code_save_recovery_test.go`（5 个）、
  `code_save_resume_test.go`（6 个）、`tests/integration/local/scheduler/restart_protocol_test.go`
  （7 个）、`retry_test.go`（11 个）。
- **本轮结果**：通过。

### 3.7 并发与隔离（有界执行、栅栏、资源隔离）

- **必需行为**：同一任务不得被重复领取；失去执行权的旧执行者不得写成功终态；名额上限成立；
  任务间容器与执行现场隔离；回收一个任务不破坏其他任务。
- **代码证据**：`internal/app/task_scheduler.go`（`scanMu`、`countOccupiedSlots`、租约续期）、
  `internal/domain/task_queue.go`、`internal/domain/task_lease.go`、
  `internal/domain/container_ownership.go`、`internal/app/task_container.go:loadOwnedContainer`、
  存储层 `AcquireTaskLease` / `UpdateTaskLeaseGuard`（带 `owner + epoch` 条件）。
- **测试证据**：`tests/integration/local/scheduler/scheduler_test.go`（12 个）、
  `lease_store_test.go`（9 个）、`isolation_test.go`（6 个）、`lease_fencing_test.go`（5 个）、
  `concurrency_acceptance_test.go`（8 个）、`resource_acceptance_test.go`（8 个）、
  `resource_reclaim_test.go`（7 个）、`tests/unit/domain/task_queue_test.go`（30 个）、
  `container_ownership_test.go`（8 个）、`tests/integration/remote/docker/concurrency_acceptance_test.go`
  （3 个真实远程用例，本轮全部通过）。
- **本轮结果**：通过。边界保持：数据库栅栏不等于远程进程已停止，该事实已在
  `P3-M4-scheduler-recovery.md` 固定为"只记录现场、不自动重放"。

## 4. 本轮实际执行的验证

全部命令均在仓库根目录执行（Windows 11 + Git Bash，Go 1.24.5）。

| 命令 | 结果 |
|---|---|
| `gofmt -l cmd internal tests` | 无输出（无未格式化文件） |
| `go vet ./...` | 无问题 |
| `go build ./...` | 通过 |
| `go test -count=1 -p 8 -parallel 8 ./tests/unit/...` | 1811 passed / 0 failed（21 packages） |
| `go test -race -count=1 -p 4 -parallel 4 ./tests/unit/...` | 1811 passed / 0 failed |
| `go test -count=1 -p 8 -parallel 8 ./tests/integration/local/...` | 465 passed / 0 failed（14 packages），无 SKIP |
| `go test -race -count=1 -p 4 -parallel 4 ./tests/integration/local/{scheduler,workflow,codesaver,database,validation}/...` | 252 passed / 0 failed |
| `go test -count=1 ./tests/e2e/...` | 64 passed / 0 failed（2 packages，含 CLI 与单文件打包） |
| `go test -count=1 -v ./tests/integration/pi/...` | 1 passed，1 **SKIP**（`TestRealPiModelRecognition`：Pi 未识别 `group/free` 模型清单，按约定如实跳过） |
| `go test -count=1 -v ./tests/integration/ssh/...` | 6 passed（真实 SSH 握手、主机密钥校验、命令状态、下载上限、超时） |
| `go test -count=1 -timeout 50m -v ./tests/integration/remote/workflow/` | 4 passed / 0 failed（含 `TestRemoteE2EFullChain`，166 s） |
| `go test -count=1 -timeout 40m -v ./tests/integration/remote/{docker,codesaver,agent}/...` | 9 passed / 0 failed（docker 6 + codesaver 1 + agent 2） |

说明：

- 本地集成测试使用真实 SQLite 文件与正式迁移，`TEST_ARTIFACT_DIR` 落在
  `os.TempDir()/lingting-agent-flow-tests/` 下的随机目录并按要求保留。
- `tests/integration/local/validation/` 覆盖的是**执行节点环境预检**（SSH / Docker 版本 / CLI
  帮助 / 镜像元数据），不是任务验证引擎；任务验证引擎的证据在
  `tests/integration/local/workflow/` 与 `tests/unit/app/validation_review_test.go`。
- 未执行：前端任何测试与构建。本轮按任务要求冻结前端，未改动
  `lingting-agent-flow-ui/` 下任何文件。

本轮的测试产物目录（按仓库约定保留，未清理）：

```text
本地集成（最后一次全量执行）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-719306879     （workflow）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\scheduler-2905658771    （scheduler）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\codesaver-1391906499    （本机 codesaver）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\database-2686000819     （迁移）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\validation-2577127303   （环境预检）
  …（其余 9 个包各有独立目录，均在该基目录下；`latest-run.txt` 指向最近一次运行）
真实远程（出网恢复后，均 PASS）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-4019267505    （workflow 4 个，含完整链路）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-1508565171    （完整链路单跑，150 s）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\agent-4097282897        （远程 Clone + Agent）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\codesaver-3619379747    （远程代码保存）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\docker-2786419134       （远程 Docker）
真实远程（出网受阻时，上午的记录，保留用于对照）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-3007334153     （完整链路 FAIL）
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\agent-3989570293        （SourceFetch FAIL）
真实 SSH / Pi
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\ssh-3233523508
  C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\pi-2660748402
```

远程测试资源：本次未创建需长期保留的远程资源；`remote/docker` 与 `remote/codesaver` 用例创建的
容器均带随机后缀，并由用例自行核验所有权后处置，未执行任何全局 prune。

## 5. 真实远程验证

配置来源：`tests/config/{ssh,docker,git,pi}.yml` 均存在且已启用（不打印、不复制任何秘密）。
远程主机实测基线：Docker CLI `29.7.2`、Engine `29.7.2`、Engine API `1.55`
（最低接受 API `1.40`），满足项目基线（CLI/Engine ≥ 29.0.0、API 基准 1.52 ∈ [1.40, 1.55]）。

### 5.1 本轮结果：全部通过

| 套件 | 结果 | 覆盖 |
|---|---|---|
| `./tests/integration/remote/workflow/` | **4 passed**（含 `TestRemoteE2EFullChain` 166 s） | 远程 Docker 预检与基线版本、工作流前置条件、主机密钥校验拒绝，以及**完整链路** |
| `./tests/integration/remote/docker/` | **6 passed** | Docker 预检、容器生命周期、`unknown` / 孤儿 / absent 现场注入、两任务并发与分别回收、短暂不可达后重新核验、`docker exec` 工作目录与环境变量 |
| `./tests/integration/remote/agent/` | **2 passed** | 容器内真实 Git Clone（`TestRemoteSourceFetch`，17 s）、容器内 Pi CLI 1.3.0 真实调用与两次独立 Session 的 JSONL 回传归档 |
| `./tests/integration/remote/codesaver/` | **1 passed** | 真实远程容器内 commit + push + merge，并核对远程基础分支与 `MergeSHA` 一致 |
| `./tests/integration/ssh/` | 6 passed | 真实 SSH 握手、主机密钥校验与拒绝、命令状态、下载上限、超时 |
| `./tests/integration/pi/` | 1 passed + 1 SKIP | `TestRealPiCheck` 通过；`TestRealPiModelRecognition` 因 Pi 未能拉取模型清单而按约定跳过 |

### 5.2 完整链路实际产生的持久化事实

`TestRemoteE2EFullChain` 本轮真实跑通（不是"容忍 blocked 也算过"），日志中的关键事实：

```text
LAUNCH stage=workflow status=planned container=d33463f7… commit=92f64326… steps=2
REMOTE_CONTAINER=d33463f7… DOCKER_ID=66937f0b91c7…
WORKFLOW status=completed stage=finalize class= summary=全部必需步骤已通过验证，代码保存与会话归档已完成
FINAL_TASK status=completed result=succeeded failure=全部必需步骤已通过验证，代码已保存
CODE_SAVE commit=0ad11d6a… push=succeeded branch=agent/4fd30478…
SESSIONS=5 全部已归档
```

对应验收项：容器真实创建并写回任务；容器内真实 Clone 得到 `92f6432` 并准备 `agent/<task-id>` 分支；
Planner 生成 2 个步骤并落库（每步都带任务级必需命令）；Developer 与 Validator 各用独立 Session；
确定性检查真实执行；代码保存 commit `0ad11d6a` 并推送成功；5 个会话全部归档成功；
`workflow_runs` 与 `tasks` 同为完成终态且 `result=succeeded`。

### 5.3 上午失败的根因与排除过程（已定位，不是端口选择问题）

上午同一套件的两次失败不是产品缺陷，而是远程侧出网被阻断/过滤：

| 用例 | 上午结果 | 失败点 |
|---|---|---|
| `TestRemoteE2EFullChain` | FAIL（994 s） | `workspace` 阶段：`无法访问仓库: ssh://github.com/lingting/lingting-agent-flow-test.git \| kex_exchange_identification: read: Connection timed out` |
| `TestRemoteSourceFetch` | FAIL（602 s） | `clone_timeout: git clone: 容器内命令被中断`（连接挂起直到命令超时） |

先排除一个容易被误读的点：**失败信息里的 `ssh://github.com/...` 不是实际连接目标。**

- 生产代码用 `GitRepositoryURL.Raw()` 执行 Clone（原始地址是 scp 形式
  `git@github.com:lingting/lingting-agent-flow-test.git`），错误信息里写的是脱敏后的
  `Display()`，它把 `user@host:path` 重写成 `ssh://host/path` 以隐藏用户名。
- 实证（离线，`GIT_TRACE=1`）：scp 形式与 `ssh://host/path`（无端口）都**不会**给 `ssh` 传
  `-p`，只有 URL 里显式带端口才会；因此镜像内 `Host github.com → Hostname ssh.github.com /
  Port 443` 的 `Port` 生效。
- 实证（离线，`ssh -G -F <镜像内的 SSH 配置>`）：解析结果为 `hostname ssh.github.com`、
  `port 443`；叠加测试注入的 `core.sshCommand`（`-i`、`IdentitiesOnly`、`StrictHostKeyChecking`、
  `UserKnownHostsFile`，均不含 `-p`）后结论不变。
- 现场证据：失败那次运行的日志里有 `CONTAINER_SSH_CONFIG=/root/.ssh/config（由基础镜像内置）`，
  说明 `verifySSHClientConfig` 在该任务容器上读到了 `Host github.com / Hostname ssh.github.com /
  Port 443` 三条配置并通过。

因此容器实际连的是 `ssh.github.com:443`。三条相互独立的证据指向"远程侧出网被阻断/过滤"：

1. 错误是 `kex_exchange_identification: read: Connection timed out`，即 **TCP 握手成功、
   SSH banner 一直没有返回**（连接被直接拒绝或不可达时文本会变成
   `connect to host ... port ...: Connection timed out`）。这与"允许 TCP 建连、但对非 TLS
   流量不做转发（或静默丢弃）"的过滤设备表现一致。
2. 远程宿主机的 `docker run agentflow-test-agent:1.0.0` 需要拉取镜像时报
   `Get "https://registry-1.docker.io/v2/": context deadline exceeded`，说明 HTTPS 出网同样不通。
3. 同一远程会话里，只要不依赖出网就能通过：`remote/docker`、`remote/agent`、`remote/codesaver`、
   `remote/workflow` 的预检与容器内 bare 仓库用例全部通过（含容器内 commit/push/merge）。
   本机对同一仓库 `git ls-remote` **成功**，进一步把问题定位在远程侧而非仓库或凭据。

调整远程主机（WSL）的网络配置后，同一批用例立即全部通过（§5.1），且 Clone 从 602 s 的挂起变为
17 s 完成——这条对照进一步确认根因在远程网络路径。

补充事实：上一轮真实远程记录（P3-M5）记录的 Docker 版本为 `29.9.1` / API `1.56`，而本次实测为
`29.7.2` / `1.55`，说明 `tests/config/ssh.yml`、`docker.yml` 指向的目标主机在 2026-10-28 之后已
被更换。**历史远程记录不能代表当前主机。**

### 5.4 结论

真实远程完整链路：**已确认**（本轮真实跑通并留下 §5.2 的持久化事实）。该用例的设计边界仍需记录：
它在 `blocked` / `recovery_required` 且原因可解释时也会通过，因此单次运行不能独自证明"成功收尾"；
本轮之所以判为"成功路径已验证"，是因为本次运行真的走到了 `completed` / `succeeded`，再叠加 §3.5 的
本地链路级用例。若后续要让该用例自身强制成功终态，见 §2.5 的说明。

## 6. 本轮修复的第一版缺陷

### 6.1 缺口：语义审查失败无法进入修复循环，且停止原因失真

- **触发条件**：确定性必需检查全部通过，Validator Agent 语义审查判定 `passed=false`。
- **修复前行为**（本轮实测，`TestE2EValidatorReviewFailure*` 首轮失败信息）：

  ```text
  status=recovery_required stage=stopped class=verification_unconfirmed
  summary=修复循环停止（feedback_unavailable，共 1 次开发尝试）: 验证失败反馈未能可靠保存，
          缺少下一轮修复的可信依据
  ```

- **根因**：`internal/app/step_workflow_run.go:persistFeedback` 以"存在未通过的确定性检查"
  为写入条件（`len(failedChecksOf(result.Checks)) == 0 → return false`）。审查失败时检查列表
  为空，因此不写反馈；`domain.DecideRepairLoop` 见 `FeedbackPersisted=false` 立即停止并给出
  `RepairClassFeedbackUnavailable`，编排器再把它映射为 `verification_unconfirmed` →
  `recovery_required`。
- **为什么属于第一版必需**：第一版要求"验证失败后，结构化反馈、修复轮次、重新执行与重新验证
  能组成有上限的闭环"。该要求对"验证失败"成立与否不应取决于失败来自确定性检查还是语义审查。
  同时，"一次确定的验证未通过"被写成"验证证据未可靠写入"，违反了"确定性与不确定性必须区分"
  的项目原则。

### 6.2 修复

| 位置 | 改动 |
|---|---|
| `internal/app/step_workflow_run.go:persistFeedback` | 写入条件改为"结论非通过 + 有验证记录 + （存在未通过检查 或 结论摘要非空）"；只有两者都为空时才不写反馈 |
| `internal/app/step_workflow_prompt.go:stepWorkflowPrompt` | 反馈段落不再以"失败检查列表非空"为前提；仅当检查与摘要都为空时省略，避免审查失败时提示词丢光修复依据 |
| `internal/domain/validation_feedback.go:NewValidationFailureFeedback` | 修正函数契约注释：失败可能只来自语义审查，此时检查列表为空、摘要就是唯一可引用的依据 |

未新增第二套执行路径、未新增表与迁移、未改动 `DecideValidation` / `DecideRepairLoop` /
`DecideStepCloseout` 的判定规则，也未放宽任何成功条件。

### 6.3 修复后的行为

- 失败只来自语义审查时：反馈落库（`verdict=failed`、`summary` 来自评估、`checks` 为空，
  不伪造检查条目）→ 修复循环可继续（若重试策略与预算允许）→ 上限处停止为
  `attempts_exhausted`；重试策略关闭时停止为 `verification_failed`（不再被写成"结果不确定"）。
- 修复轮次的提示词包含"上一次验证未通过的问题"段落与上一轮审查结论。

### 6.4 新增测试

`tests/integration/local/workflow/e2e_review_test.go`（链路级，装配与 `e2e_success_test.go`
一致的完整生产服务链）：

1. `TestE2EValidatorReviewFailureBlocksCompletion`：确定性检查全部通过、审查不通过时，
   工作流为 `blocked` / `verification_failed`，任务不得 `completed`，不产生代码保存记录，
   开发步骤 `failed` 且释放占用，验证记录 `review_status=failed`、`verdict=failed`、
   `passed_checks>0`、`failed_checks=0`，审查会话与开发会话是两条不同记录且角色分别为验证与开发。
2. `TestE2EValidatorReviewFailureFeedsRepairLoop`：同样场景但开启修复（上限 2），断言两轮
   各留一次新 Attempt 与一条新验证记录、每轮反馈落库且摘要来自审查、第二轮提示词携带上一轮
   审查结论、上限处停止且不再调用 Agent、任务不得 `completed`。

### 6.5 回归

修复后重新执行：`gofmt`、`go vet ./...`、`go build ./...`、`./tests/unit/...`（1811 passed）、
`./tests/integration/local/...`（465 passed，较修复前 463 增加上述 2 个用例）、
`./tests/e2e/...`（64 passed）、以及 scheduler / workflow / codesaver / database / validation
的 `-race` 组合（252 passed）。无回归。

## 7. 文档同步（本轮实际改动）

> 出网恢复后，本文件的判定从 `blocked` 改为 `completed`；`.steps/README.md`、`.steps/ROADMAP.md`、
> 根 `README.md` 的结论与"前端冻结"表述已同步改为解除冻结。

| 文档 | 改动 |
|---|---|
| `README.md` | 重写"第一版功能范围"的未实现清单与"尚未实现的接口"两节，使其与当前代码一致（§2.4） |
| `.steps/README.md` | 当前阶段段落追加本轮验收结论与指针；文档索引新增本文件 |
| `.steps/ROADMAP.md` | 现状摘要追加本轮验收结论；阶段文档列表新增本文件 |
| `.steps/P2-reliability-recovery.md` | 更新 P2-M5 状态行中"真实远程 merge / 容器残留证据仍缺"的表述：容器残留证据已由 P3-M5 与本轮远程 Docker 套件补齐，仍缺的是 **merge 结果不确定后的真实远程核验** |

未改动：P1、P3、P4 各文档以及前端任何文件。P1 记录的私有仓库凭据架构决策、P3-M5 记录的真实
远程 `unknown` / 孤儿现场结论经本轮复核仍然成立。

## 8. 剩余事项（均不阻塞第一版）

第一版范围内**没有**未处理的阻塞项。以下是不影响判定的后续事项：

1. **merge 结果不确定后的真实远程核验**：真实远程只覆盖"合并成功"；`merge` 结果无法确认后的
   核验判定目前只有真实 Git + bare 远程的本机证据（该判定的全部输入都是观测到的 Git 事实）。
   属于证据完整性增强，见 §2.5。
2. **Clone 失败信息中的实际传输目标**：错误文本用脱敏形式，不显示实际 host:port 与传输类型，
   影响诊断可读性。见 §2.5。
3. **真实远程用例的失败场景覆盖**：`TestRemoteE2EFullChain` 只覆盖成功路径；远程侧的失败与
   恢复路径由 `remote/docker` 的现场注入用例与本地链路级用例覆盖。

## 9. 判定边界

- 本轮**没有**把任何第一版必需能力记为"通过"却没有证据：§3 每一行的测试证据都能在当前
  基线上复现，其中统一启动、开发与验证、代码保存、会话归档与收尾在真实远程上也已跑通（§5.2）。
- 本轮**没有**把未运行的测试算作通过：`TestRealPiModelRecognition` 仍如实记为 SKIP；上午那次
  真实远程失败被完整记录为过程事实与根因分析，未从记录中删除。
- 本轮**没有**为了宣布完成而缩小范围，也没有把明确的非目标扩大为第一版要求（§2.3）。

## 10. 下一项服务端任务建议

1. **解除前端冻结，回到 P4**：服务端第一版的最后一项阻塞（真实远程完整链路）已复核通过，
   `P4-M5`（基于真实数据的优化）仍是 P4 唯一 `pending` 项。下一项服务端任务建议是继续
   `P4-M5`：按既有评估文档补齐"任务状态分布与等待现场"的只读测量，再决定候选 A（G17）与
   候选 C（调度扫描开销）是否立项。
2. **补 merge 结果不确定的真实远程核验用例**（证据完整性）：在
   `tests/integration/remote/codesaver/` 中注入"合并在远程已发生但记录未落盘"的现场，验证核验
   结论为 `confirmed_applied` 且不重放。
3. **给 Clone 失败信息补上实际传输目标**（诊断可读性）：在 `internal/source` 的错误摘要中包含
   解析后的主机与端口（不泄露凭据），避免再次出现"看错误文本推测端口"的情况。
