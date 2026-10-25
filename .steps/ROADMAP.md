# AgentFlow 项目路线图

> 本文件是路线图总览。具体里程碑的工作项、依赖与验收见各阶段文档。
> 撰写依据：commit `330e7c4`（2026-10-23）工作区的实际代码与测试状态，非旧对话或 README 推断；
> P1-M1 已在 commit `a0ee19b` 之后按实际核实与改动更新；
> P1-M2 已在 HEAD `3219591` 之后按实际接入与改动更新；
> P1-M3 已在 HEAD `2545e74` 之后按实际接入与改动更新；
> P1-M4 已在 HEAD `b7f7e22` 之后按实际核实与改动更新；
> P1-M5 已在 HEAD `b36a9d2` 之后按实际核实与改动更新；
> P2-M1 已在 HEAD `7a55dfe` 之后按实际核实与文档改动更新（结论见
> [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)）；
> P2-M3 已在 HEAD `2a1bb0d` 之后按实际实施更新（结论见
> [`P2-reliability-recovery.md`](./P2-reliability-recovery.md) 的 M3 详情）；
> P2-M4 已在 HEAD `168fd2f` 之后、P2-M5 已在 HEAD `40460d4` 之后按实际实施更新
> （结论见同一文件的 M4 / M5 详情）。

## 项目最终业务目标

AgentFlow 是一个**本地优先的 AI Coding Workflow Orchestrator**：把一条编码需求管理为
持久化任务，在受管理的远程 Linux + Docker 执行环境中完成代码获取、计划生成、分步开发、
确定性验证、验证失败修复、代码保存（提交/推送/可选合并）与任务收尾，并让用户能清楚地
发起、观察、取消、恢复与查看这些任务。

**端到端主流程（目标形态）**：

```text
创建任务
  → 准备执行环境（远程 SSH + Docker 容器，任务级工作区）
  → 获取代码并初始化 agent/<task-id> 分支
  → Planner 生成并持久化计划
  → 按计划顺序逐步执行：
       Developer 开发（独立 Attempt / Session）
       → 确定性验证（容器内必需检查命令）
       → Validator Agent 语义审查（可收紧、不可放宽）
       → 验证失败时进入有上限的修复并重新验证
  → 代码保存（Commit → Push → 可选 Merge）
  → 会话归档核验
  → 任务收尾（completed / blocked / recovery_required / failed / cancelled）
```

## 当前实现状态摘要（基于代码证据）

> **当前阶段：P3 已完成 —— 多任务执行与调度**（P1 全部里程碑于 2026-10-23 完成；
> P2-M1 于 2026-10-23 完成，P2-M2 于 2026-10-24 完成，P2-M3 于 2026-10-24 完成，
> P2-M4 于 2026-10-25 完成，P2-M5 于 2026-10-25 完成；
> **P3-M1 于 2026-10-25 完成，P3-M2 于 2026-10-25 完成，P3-M3 于 2026-10-26 完成
> （文档记录日期；实际实施与本次 P3-M4 同在 2026-10-25，日期不自洽已记录在
> `P3-M4-scheduler-recovery.md` §1.2），P3-M4 于 2026-10-25 完成，P3-M5 于 2026-10-25
> 完成**）。P1 证据见
> `P1-single-task-e2e.md` 的对应"完成记录"；P2-M1 证据见
> `P2-M1-recovery-boundaries.md`；P2-M2 证据见 `P2-M2-interruption-semantics.md`；
> P2-M3 与 P2-M4 证据见 `P2-reliability-recovery.md` 的对应里程碑详情。
>
> P3-M5 的核实结论：本轮以可复核的测试证据为中心，先定位真实缺口再修复，共修复 4 个
> 生产缺陷——D1 `TaskService.CancelTask`（CLI 入口）用无条件整行写入，会覆盖并发领取的
> `lease_owner` / `lease_epoch`（新增 `storage.TaskStore.UpdateTaskCancellation`，只写
> 取消相关列并带状态前置条件）；D2 CLI 与 HTTP 各有一套取消状态语义且 `queued` 任务停在
> `cancelling`（新增 `domain.Task.ApplyCancellationRequest` 作为唯一落点）；D3
> `isContainerMissing` 把 `CodeOwnershipMismatch` 当作"容器不存在"，使回收伪造"按删除
> 成功处理"；D4 `TaskScheduler.Scan` 的名额统计与领取不在同一临界区，并发扫描实测在
> 上限 2 时领取 3 个（新增 `scanMu`）。并发上限语义明确为**"同一份数据库上的名额上限"**：
> 多个调度器共享同一数据库时总量等于单进程上限，跨库全局配额是明确的非目标。
> 测试证据：`tests/integration/local/scheduler/{concurrency,resource}_acceptance_test.go`
> （16 个用例，真实 SQLite + 正式迁移 + 多独立连接）与
> `tests/integration/remote/docker/concurrency_acceptance_test.go`（3 个真实远程用例：
> `unknown` / 孤儿 / absent 现场注入、两个任务并行与分别回收、短暂不可达后重新核验；
> 远程 Docker CLI / Engine `29.9.1`、API `1.56`）。交付物为
> `P3-M5-multi-task-acceptance.md`。
>
> P3-M4 的核实结论：本轮把"调度器重启"落成**安全协议**而不是"自动接管"——过期租约
> 只证明持有者没有续租，不能证明它发出的远程操作已经停止，因此新增
> `app.TaskLeaseRecoveryService`（`internal/app/task_lease_recovery.go`）只把失效事实
> 落成可追溯现场（`task.recovery_required` 事件 + WARN 日志），不释放租约、不改写任务
> 状态、不重放外部操作；`internal/server/server.go` 把它的注册顺序固定在
> `TaskScheduler` 之前，保证"先恢复、后调度"。同时补齐了实施前真实缺失的唯一入口：
> `failed` 任务的显式重试（`internal/domain/task_retry.go` 的
> `DecideTaskRetryEligibility` / `AllowsExplicitRetry` / `ReopenForRetry` 与 10 个稳定
> 拒绝原因，`internal/app/task_retry.go` 的 `TaskRetryService`，
> `POST /api/v1/tasks/{taskID}/retry`、`GET .../retry-eligibility`，CLI
> `agentflow task retry` / `task retry-status`）。重试先按领域规则判定资格（状态、租约
> 归属、未结束执行记录、步骤占用、恢复残留、未核验外部副作用、启动前置条件、是否还有
> 可重试步骤），再以 `failed` 集合领取任务租约、用 `UpdateTaskLeaseGuard` 把任务重开为
> `preparing`，最后交接给既有 `TaskLaunchService.Launch`；历史 Attempt / Session /
> WorkflowRun 全部保留。交付物为 `P3-M4-scheduler-recovery.md`；测试证据为
> `tests/unit/domain/task_retry_test.go`、
> `tests/integration/local/scheduler/{restart_protocol,retry}_test.go`、
> `tests/unit/httpapi/task_retry_test.go`、
> `tests/integration/local/workflow/retry_e2e_test.go`。明确保留的边界：租约残留核验不
> 判断远程进程是否真的停止；重试只接受 `failed`；前端重试入口尚未实现。
>
> P3-M3 的核实结论：本轮核实到 6 个真实隔离缺口（执行链路直接用
> `task.ContainerID` 字符串核验容器、没有容器归属领域概念、单步骤三个入口不领取
> 任务租约、步骤占用与任务终态写入只有状态条件、领取冲突无法区分 404/409、
> 孤儿容器没有归属判据），并全部修复：新增容器归属契约
> （`internal/domain/container_ownership.go` 的 `DecideContainerOwnership` /
> `CheckContainerOwnership`，9 个稳定拒绝原因）与统一加载入口
> （`internal/app/task_container.go` 的 `loadOwnedContainer`），六个执行入口
> （工作区初始化、代码初始化、步骤执行、验证、工作流编排、任务启动）不再直接用
> 字符串核验容器；单步骤三个入口统一领取任务租约，步骤占用与任务终态写入新增
> 租约栅栏（`StepExecutionStartRequest.Lease` +
> `StepExecutionStore.UpdateTaskLeaseGuard`）；领取冲突细分为 404/409
> （`ErrTaskLeaseNotFound` + `classifyClaimConflict`）。交付物为
> `P3-M3-execution-isolation.md`；测试证据为
> `tests/unit/domain/container_ownership_test.go`（8 个用例）与
> `tests/integration/local/scheduler/{isolation,lease_fencing,resource_reclaim}_test.go`
> （真实 SQLite + 正式迁移，覆盖跨任务容器隔离、租约 fencing、孤儿容器与共享缓存边界）。
> 明确保留的边界：租约栅栏只保护数据库写入（远程命令的物理收敛由 P3-M4 定义为
> "只记录现场、不自动重放"）；
> 真实远程依赖用例（`tests/integration/{ssh,pi,remote/**}`）已全部通过；孤儿容器与
> `unknown` 现场的真实远程主动注入验收已在 P3-M5 完成
> （`tests/integration/remote/docker/concurrency_acceptance_test.go`）。并发上限的
> 语义已在 P3-M5 明确为"同一份数据库上的名额上限"，跨库全局配额是明确的非目标。
>
> P3-M2 的核实结论：本轮把 P3-M1 的契约落到运行时——`AcquireTaskLease` 成为生产
> 调用路径（`app.TaskScheduler` 显式传 `queued` 集合）、`ReleaseTaskLease` 补上
> `RowsAffected` 判定并改为按 `owner + epoch` 释放、新增迁移 `0024` 的
> `tasks.lease_epoch` 作为任务级 fencing、任务租约与 `WorkflowRun` 租约合并为同一
> owner（`app.WorkflowExecutionGrant`）、有界并发调度器
> （`app.TaskScheduler`，复用 `cfg.Scheduler.MaxConcurrentTasks`）注册进
> `internal/lifecycle`。交付物为
> `internal/domain/task_lease.go`、`internal/app/{task_lease,task_workflow_lease,
> task_scheduler}.go` 与 `P3-M2-bounded-concurrency.md`；测试证据为
> `tests/unit/domain/task_lease_test.go`、
> `tests/integration/local/scheduler/lease_store_test.go`（9 个用例，多独立连接并发领取）
> 与 `tests/integration/local/scheduler/scheduler_test.go`（12 个用例，真实调度器）。
> 明确保留的边界：并发上限是**进程级**；`StepExecutor` 入口的租约栅栏已在 P3-M3 接入；
> 服务停止保留的租约依赖 TTL 自然过期（P3-M4 已把该现场落成可追溯记录）；
> `failed` 任务的显式重试入口已在 P3-M4 补齐。
>
> P3-M1 的核实结论：本轮把队列语义写成唯一契约，并核实了三处容易被误判为"已有能力"
> 的事实——(1) `TaskStatus.CanStart()` 会为 7 个状态返回 `true`（含 `failed` /
> `paused` / `recovery_required`），**不能**直接作为队列准入规则；(2)
> `Task.LeaseOwner` / `LeaseExpiresAt` 两列与 `AcquireTaskLease` / `ReleaseTaskLease`
> 已实现，但**全仓库无任何生产调用方**，不构成"领取安全"的证据；(3)
> `UpdateTaskIfStatus` 的条件只有 `status IN (...)`，**不含租约条件**，不足以支持原子
> 领取。交付物为 `internal/domain/task_queue.go`（准入 / 排序 / 领取仲裁 / fencing /
> 交接 / 名额归属的纯领域规则）、`tests/unit/domain/task_queue_test.go`（30 个用例）与
> `P3-M1-task-queue-semantics.md`。**未修改任何生产执行流程、存储层或接口**；
> 数据库原子领取、有界并发与 `lease_epoch` 栅栏列为 P3-M2 的验收项 S1–S7，
> 已在 P3-M2 全部落地（见上）。
>
> P2-M5 的核实结论：本轮审查发现并修复了 **3 个阻塞级**与 **4 个高风险**恢复缺陷——
> C1（任务卡在 `verifying`/`saving` 后既不能启动也不能恢复）、C2（收尾先写任务后写
> 执行记录，`completed` 又是终态，导致收尾失败不可恢复）、C3（丢失执行权仍可写任务
> 成功）、H1（`container.state=unknown` 死胡同）、H2（容器不可用即新建第二个容器）、
> H3（`code_fetches` 无活跃唯一约束与收敛路径）、H4（工作区恢复的冲突分支是死代码）。
> 修复方式：收尾写入顺序改为"先执行记录、后任务"并用条件写入做栅栏；新增
> `recoverStalledDelivery` 收敛停滞任务；新增 `app.ContainerRecoveryService` 与
> `app.CodeFetchRecoveryService`；`VerifyCodeSave` 在容器缺失时降级为 `unverifiable`
> 并落盘；任务 API 新增 `final_result` / `closeout` 让"结论存疑"可被看到。
> 证据：`tests/integration/local/{workflow,execenv,fetcher}/` 的新增用例与
> `go test -count=1 ./tests/...` → 1885 passed / 0 failed。
>
> P2-M4 的核实结论：外部副作用的恢复此前有三类真实断点——(1) "记录写入失败"只标记在内存
> 记录上，数据库停留在看似可信的 `pending`，重启后无法区分"没发生"与"已发生但没记下来"；
> (2) 只要 HEAD 变化就判提交成功，他人提交会被归因为本次；(3) 核验结论只写
> `recovery_status`，已确认发生的阶段状态不落盘，数据库永远 `pending`，恢复只能反复要求
> 核验。本次分别用"不可信标记单独落盘"、"第一父等于基线才归因"、"已确认阶段条件写回"
> 修复，并补上"多阶段核验顺序收敛"（此前 `confirmed_applied` 与 `confirmed_not_applied`
> 互斥为终态，导致"提交已确认 + 推送确认未发生"无法落库）。新增
> `domain.DecideCodeSaveResume` / `DecideTaskCodeSaveResume` 统一定义续跑资格，
> `TaskWorkflowService` 在 `StartWorkflow` / `ResumeWorkflow` 前置门控，
> `app.CodeSaveRecoveryService` 在服务启动时列出仍有未决副作用的记录（只读 + 追加事件）。
>
> P2-M3 的核实结论：修复预算此前只有进程内计数（`runRepairLoop` 的 `attempts := 0`），
> 重启后的恢复与重复调用会重新得到"还有余额"的结论。本次把预算落到持久化事实：
> 新增 `repair_cycles`（迁移 `0022_repair_cycles.sql`）与 `attempts.budget_scope` /
> `attempts.budget_scope_id`，预算只统计同一周期的 Attempt；领域层新增
> `domain.DecideRepairBudget` / `domain.DecideRepairStart` 作为唯一准入与预算规则，
> 编排、恢复与重复调用全部引用它。用户显式开启新周期走独立入口
> `POST /api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle`（CLI：`agentflow task
> reopen-step`），只有在上一周期已安全收敛且预算已用尽或策略不再允许修复时才允许；
> 上一周期仍有余额时拒绝，避免用"开新周期"绕过上限。首轮修复依据改为按"当前周期内
> 最近一次验证"精确读取反馈，不再无条件使用步骤级"最近一次反馈"，从而不会把旧周期
> 反馈当成当前依据。
>
> P2-M2 的核实结论：四类中断（用户取消 / 单次超时 / 服务停止 / 进程崩溃）在
> Task / WorkflowRun / Step / Attempt / Session / ValidationRun 六层的收敛目标已成文并
> 落地到领域函数。同时修复了三个阻塞/高风险缺陷——B1（步骤占用与 `blocked` 执行记录
> 互相锁死，判据改为"仍持有有效执行权"）、B5（取消意图跨层丢失，取消入口双写并由启动
> 恢复补齐）、B6（租约只判定一次，改为按租约 1/3 周期续期，续租失败即停止推进且不写
> 终态）。另新增 `app.ExecutionResidueService` 收敛 `attempts` / `sessions` /
> `validation_runs` 的活动态残留（B2/B3），并补齐验证引擎的取消 / 超时 / 服务停止用例。
> 核实中还修复了两处真实生产/测试缺陷：`GetWorkflowStatus` 的读取顺序会导致"任务
> `saving` + 执行记录 `completed`"的撕裂快照；`TestPlanGenerationInterruptedByShutdown`
> 的约 40% 失败源于替身与取消传播的调度竞态。
>
> P1-M4 的核实结论：修复闭环确实在生产调用链内并自然进入交付阶段；核实中修复了两处
> 真实生产断点——(1) Developer 执行从不写入 Session 记录（`callAgent` 只 `Load` 而不
> `Start`/`Finish`，导致归档核验对 Developer 会话完全空转），(2) 收尾终态写入失败时会
> 静默留下 `running` 残留（`completed → recovery_required` 被状态机拒绝后无兜底）。
>
> P1-M5 的核实结论：此前**没有任何用例把统一启动入口与后续工作流执行串起来**
> （`launch_chain_test.go` 用替身替换编排器，`delivery_chain_test.go` 预置 `planned` 任务），
> 因此 M5 在 `tests/integration/local/workflow/e2e_*_test.go` 新建 18 个端到端用例，从
> `queued` 任务出发贯穿启动 → 环境/工作区 → 计划 → 开发 → 确定性验证 → 语义审查 → 修复 →
> 代码保存 → 会话归档 → 收尾，覆盖成功 / 修复 / 修复上限 / 无法确认四类终局；代码保存使用
> 真实 `internal/codesaver` 落在真实 Git 仓库上（真实提交与真实推送）。
>
> **待调查事项（不阻塞 P1 已完成结论）**：
> - ~~`TestPlanGenerationInterruptedByShutdown` 在干净工作区上以约 40% 概率失败~~ ——
>   已在 P2-M2 修复：替身新增 `waitForCancel`，等本次调用的上下文真的被取消后才返回，
>   建立确定的先后关系（`-race` 下连续 15 次全部通过）。
> - 真实远程全路径已自动化并通过：`tests/integration/remote/workflow/e2e_*_test.go` 用真实
>   SSH + 真实 Docker + 真实镜像构建 + 容器内真实 Git Clone + 容器内真实模型调用，逐阶段
>   走通到代码保存。核实中修复了一处真实生产断点：`UpdateContainer` 不写 `docker_id`，
>   导致真实远程链路在容器核验阶段失败（本地用例从不经过"先登记、后拿 ID"的流程）。
> - 真实远程推送成功路径已实测通过（`push=succeeded`，5 个会话全部归档）。早期出现的
>   `repository_auth_failed` 是测试装配缺失造成的错误结论：容器内既没有私钥，也没有
>   github.com 的 443 端口映射。后者已作为**基础镜像默认配置**内置到
>   `internal/docker.DefaultBaseDockerfile`——`ssh.github.com:443` 是 GitHub 官方支持的
>   等价接入点，22 可达与不可达的环境都成立。
> - 生产 `source.GitCredentialProvider` 与 `codesaver.GitCredentialProvider` 仍未实现，
>   私有仓库在真实链路必然失败。这是已记录的架构决策（第一版不支持私有仓库认证），
>   补全需要凭据存储设计与安全评审。

**已实现且有代码证据（可直接复用）**：

- 任务管理、逻辑删除、取消/删除语义与分页列表（`internal/app/task_management.go`、
  `internal/httpapi/tasks.go`、`internal/domain/task.go`）。
- 执行节点管理、SSH 私钥认证、主机密钥校验、环境预检（`internal/app/hosts*.go`、
  `internal/app/host_validation*.go`）。
- 远程 Linux + Docker 执行环境与任务容器生命周期（`internal/app/execenv*.go`、
  `internal/app/containers*.go`、`internal/app/environments*.go`）。
- 代码获取与任务分支初始化（`internal/app/code_initialize.go`、`internal/source/`）。
- 统一任务启动：容器准备 → 工作区初始化 → 计划生成 → 工作流启动
  （`internal/app/task_launch_run.go`、`internal/domain/task_launch.go` 的
  `DecideTaskLaunchNext`）。**P1-M1 已核验**：阶段判定与 `Launch` 一致，容器/工作区/
  计划均有幂等边界与 `uncertain` 停止语义；重复启动不重复建容器、不重复 Clone、不覆盖
  已持久化计划（`tests/integration/local/workflow/launch_chain_test.go`）。
- 计划生成（Planner 调用 + 结构化校验 + 单事务持久化；`domain.BuildPlan` 产出
  序号 1 的 `plan` 步骤 + 后续 `develop` 步骤）（`internal/app/task_workflow_plan.go`、
  `internal/domain/plan_generation.go`）。事务内步骤写入失败时整体回滚（含测试证据）。
- 单步骤执行、顺序执行、单步骤开发—验证闭环（`internal/app/step_executor*.go`、
  `internal/app/step_sequence.go`、`internal/app/step_workflow*.go`）。
- 确定性验证引擎（`internal/app/validation.go`、`internal/domain/validation_decision.go`
  的 `DecideValidation`）。
- **Validator Agent 语义审查的生产调用链**（P1-M2）：必需确定性检查全部通过后，由
  `internal/app/validation_run.go` 的 `reviewSemantics` 调用
  `internal/app/validation_review.go` 的 `ValidatorReviewer`；每次审查创建独立 Session
  （`SessionService.Start` 先于进程启动写库）并通过 `ContainerService.RunAgent` 在同一任务
  容器工作区内调用 Pi。模型输出经 `internal/domain/agent_assessment_parse.go`
  `ParseAgentAssessment` 解析为结构化 `AgentAssessment`（未知字段拒绝、`passed` 必填、领域
  条数上限）。最终结论统一由 `domain.DecideValidation` 依据"确定性结果 + `ReviewStatus` +
  评估"计算：审查只能收紧、不能放宽。审查状态、评估与审查会话写入 `validation_runs`
  （`review_status` / `review_summary` / `assessment_json` / `validator_session_id`，迁移
  `0021_validator_review.sql`）。
- 任务级工作流编排与收尾（`internal/app/task_workflow_exec.go`、
  `internal/app/task_workflow_run.go`、`internal/domain/workflow_run.go` 的
  `CheckWorkflowStart` / `DecideWorkflowCloseout`）。
- 代码保存（Commit/Push/可选 Merge）与保存记录恢复核验（`internal/app/code_save*.go`、
  `internal/domain/code_save*.go`、`internal/cli/code.go` 的 `code verify`）。
- 会话归档核验（`internal/app/task_workflow_exec.go` 的 `sessionsPersisted`）。
- 启动恢复（P2-M1 已核实并成文）：`TaskWorkflowService.Start` 收敛工作流运行记录 /
  计划现场 / 取消现场（`internal/app/task_workflow_run.go`）；`TaskLaunchService.Start`
  收敛工作区初始化残留与步骤执行占用（`internal/app/task_launch.go`、
  `task_workspace_recovery.go`、`task_launch_recovery.go`）；`HostValidationService.Start`
  重新调度 `checking` 节点。真实调用顺序与逐阶段边界见
  [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)。
- Web 任务页：创建/查看/取消/删除、启动/取消/恢复工作流与进度面板
  （`lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx`）。
- **验证失败修复与重新验证闭环**（P1-M3）：`app.StepWorkflowService.ExecuteAndValidate`
  驱动 `runRepairLoop`（`internal/app/step_workflow_repair.go`），每轮复用
  `StepExecutor.Execute(DeferCompletion)` + `ValidationService.Validate`，每轮新建
  Attempt / Session / Validation Run 并保留历史。是否继续修复只由
  `domain.DecideRepairLoop`（`internal/domain/retry.go`）依据持久化事实判定，终态只由
  `domain.DecideRepairCloseout`（`internal/domain/step_verification.go`）计算；达到
  `Step.MaxAttempts`、结论不确定、基础设施异常、记录未可靠落库、反馈不可用、取消与开发失败
  一律停止。每轮写入 `repair.round` 事件，编排器通过 `repairStopMapping`
  （`internal/app/task_workflow_exec.go`）消费停止类别映射工作流状态。
- 验证失败修复依据：`validation_feedbacks` 已持久化并按 `ValidationRunID` 精确引用
  （`internal/app/step_workflow_prompt.go`、`step_workflow_repair.go` 的 `repairPrompt`），
  读取失败或归属不符时停止循环而不是静默降级。
- **修复预算与执行周期**（P2-M3）：预算来自持久化事实而不是进程内计数。
  `repair_cycles`（迁移 `0022_repair_cycles.sql`）登记每个执行周期与开启时的上限快照，
  `attempts.budget_scope` / `budget_scope_id` 记录每次尝试归属哪个周期；预算只统计同一
  周期的 Attempt。`domain.DecideRepairBudget` / `domain.DecideRepairStart` 是唯一预算与
  准入规则，`app.StepWorkflowService`（`step_workflow_cycle.go`、`step_workflow_repair.go`）
  在启动 Agent 之前读取历史并判定，读取失败即拒绝执行。用户显式开启新周期走
  `POST /api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle`（CLI
  `agentflow task reopen-step`），只在上一周期已收敛且预算用尽或策略不再允许修复时成功。
  首轮修复依据改为按"当前周期内最近一次验证"精确读取，不再无条件使用步骤级最近反馈。
- **代码保存三阶段恢复收敛闭环**（P2-M4）：`internal/domain/code_save_recovery.go` 的
  `DecideCodeSaveRecovery` 现在只在"HEAD 的第一父等于基线"时把提交归因为本次操作；
  新增 `DecideCodeSaveResume` / `DecideTaskCodeSaveResume` 把续跑资格定义为
  `proceed` / `verify` / `blocked` 三态并给出稳定 `ErrorClass`；
  `CodeSaveRecord.HasUnresolvedSideEffect` 把"是否还有必须核验才能重试的阶段"与
  "是否需要人工介入"分开，`ApplyRecovery` 只把 `confirmed_applied` 的阶段状态写回。
  `internal/codesaver/saver.go` 的 `persist` 现在会重试一次并把"记录不可信"单独落盘；
  `internal/app/code_save_verify.go` 使用 `storage.CodeSaveStore.UpdateCodeSaveRecoveryResult`
  以 `recovery_status` 为条件写回已确认阶段，核验失败降级为 `unverifiable` 结论而不是
  返回错误。`internal/app/task_workflow_run.go` 新增 `checkCodeSaveRecovery`，在
  `StartWorkflow` / `ResumeWorkflow` 之前拒绝存在未决外部副作用的请求；
  `internal/app/code_save_recovery.go` 的 `CodeSaveRecoveryService` 在服务启动时列出
  仍需人工核验的记录（只读 + 追加 `code_save.recovery_required` 事件），注册进
  `internal/lifecycle`。证据：`tests/integration/local/codesaver/recovery_test.go`
  （11 个用例，真实 SQLite + 正式迁移 + 真实 Git）、
  `tests/integration/local/workflow/code_save_recovery_test.go`（4 个用例）、
  `TestE2EIndeterminateDoesNotResumeWithoutVerification`、
  `tests/unit/domain/code_save_resume_test.go`。
- **单任务端到端验收**（P1-M5）：本地 `tests/integration/local/workflow/e2e_*_test.go`
  （18 个用例）+ 真实远程 `tests/integration/remote/workflow/e2e_*_test.go`。真实 SQLite + 正式迁移、真实统一启动入口（`app.TaskLaunchService`）与真实
  `app.ExecutionEnvironmentService` 四阶段准备、真实编排器/执行器/验证引擎/语义审查/
  修复循环/收尾决策，以及真实 `internal/codesaver` 与真实 Git 仓库；只替身化远程 SSH、
  Docker 与容器内 Pi 进程。四类终局：成功（`completed`，代码真实推送、全部会话已归档）、
  验证失败后修复重验成功、修复达到上限（`blocked`/`attempts_exhausted`，历史全部保留）、
  结果无法确认（`recovery_required`，不重放外部副作用）。
- **任务队列、领取与调度名额的领域契约**（P3-M1）：`internal/domain/task_queue.go` 把
  "谁可以被调度""谁排在前面""谁真正拿到执行权""谁还持有执行权""谁占用一个名额"写成
  纯函数。准入 `DecideTaskQueueAdmission` 只接受 `queued`（`AllowsAutomaticScheduling`），
  并复用 `CheckTaskLaunchStart` 与 `DecideTaskCodeSaveResume` 两个既有门控；排序
  `CompareTaskQueueOrder` 固定为 `Priority.Rank` → 任务创建时间 FIFO → 任务标识，
  并明确任务列表的 `created_at DESC` 不可复用；领取仲裁 `DecideTaskClaim` 与 fencing
  `TaskHoldsExecution` 分离；交接 `DecideTaskExecutionHandover` 要求执行者必须持有任务
  租约；名额 `DecideTaskSlotState` 用六态区分"占名额"与"可自动领取"，把
  `indeterminate`（租约失效但结论不明）与 `awaiting_human`（失败/暂停/待核验）分开，
  避免把结果不确定当作普通失败立即重试。证据：`tests/unit/domain/task_queue_test.go`
  （30 个用例）。**数据库原子领取与并发保证**已由 P3-M2 实现并验证（迁移 `0024` 的
  `lease_epoch` 栅栏 + `tests/integration/local/scheduler/lease_store_test.go`）。
- **调度器重启与失败任务显式重试**（P3-M4）：`internal/app/task_lease_recovery.go` 的
  `TaskLeaseRecoveryService` 在启动时核验过期任务租约残留，只追加
  `task.recovery_required` 事件与 WARN 日志——**不释放租约、不改写任务状态、不重放外部
  操作**，因为"租约过期"不等于"远程操作已停止"。`internal/server/server.go` 把它的注册
  顺序固定在 `TaskScheduler` 之前，保证"恢复先于调度"。
  `internal/domain/task_retry.go` 的 `DecideTaskRetryEligibility` 定义失败任务的重试
  资格（10 个稳定拒绝原因，全部拒绝原因一次收集），`internal/app/task_retry.go` 的
  `TaskRetryService` 先领取 `failed` 集合的任务租约、用 `UpdateTaskLeaseGuard` 把任务
  重开为 `preparing`、再交接给既有 `TaskLaunchService.Launch`；历史 Attempt / Session /
  WorkflowRun 全部保留。证据：`tests/unit/domain/task_retry_test.go`、
  `tests/integration/local/scheduler/{restart_protocol,retry}_test.go`、
  `tests/unit/httpapi/task_retry_test.go`、
  `tests/integration/local/workflow/retry_e2e_test.go`。
- **修复后交付与任务收尾闭环**（P1-M4）：`internal/app/task_workflow_exec.go` 的
  `finalize` → `saveCode` → `sessionsPersisted` → `applyCloseout` 顺序执行，收尾结论只由
  `domain.DecideWorkflowCloseout` 计算。核实中修复两处真实断点：
  (1) `internal/app/step_executor_run.go` 的 `callAgent` 改为
  `SessionService.Start` → `RunAgent` → `SessionService.Finish`，Developer 会话现在真实
  落库并写入归档状态（此前只 `Load`，`attempt.SessionIDs` 恒为空，归档核验对 Developer
  完全空转）；(2) 新增 `stopUnpersistedCloseout`：收尾终态写入失败时重新读取数据库真实
  记录并写入 `recovery_required` / `closeout_failed`，不再静默留下 `running` 残留。
  证据：`tests/integration/local/workflow/delivery_chain_test.go`（13 个用例，真实
  SQLite + 真实编排器/执行器/验证引擎/闭环/会话服务，仅替身化容器内 Pi、命令执行、
  容器核验与外部 Git）。

**尚未实现**：

- **~~跨任务的隔离与回收规则~~**（P3-M3 已完成）：容器归属契约
  （`domain.DecideContainerOwnership`）、统一加载入口（`app.loadOwnedContainer`）、
  单步骤入口的任务租约统一、步骤占用与任务终态写入的租约栅栏、共享缓存与不可变
  环境资源的边界都已在 P3-M3 落地并成文，见
  [`P3-M3-execution-isolation.md`](./P3-M3-execution-isolation.md)。
- **~~调度器重启后的接管策略~~**（P3-M4 已完成）：`TaskLeaseRecoveryService` 只记录
  过期租约残留（不释放租约、不改写状态），并固定"恢复先于调度"的启动顺序；
  `running` 记录的租约过期收敛由既有 `recoverUnfinished` 承担。见
  [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md)。
- **跨数据库的全局容量配额**（P3-M5 已明确语义与非目标）：并发上限的事实来源是
  数据库，因此**多个调度器共享同一数据库时总量等于单进程上限**；两个进程各持独立
  数据库时互不知情。跨库全局配额需要跨库仲裁协议，属明确的非目标，见
  [`P3-M5-multi-task-acceptance.md`](./P3-M5-multi-task-acceptance.md) §3。
- **~~`failed` 任务的用户显式重试入口~~**（P3-M4 已完成）：状态机允许
  `failed → preparing`，现在有经过领域规则约束的生产入口——
  `domain.DecideTaskRetryEligibility` + `app.TaskRetryService` +
  `POST /api/v1/tasks/{taskID}/retry` + `agentflow task retry`。调度器仍只领取
  `queued`，不会隐式重试。见
  [`P3-M4-scheduler-recovery.md`](./P3-M4-scheduler-recovery.md) §3。
- 私有仓库的 Git 凭据支持：`source.GitCredentialProvider` 与 `codesaver.GitCredentialProvider`
  均无实现，真实链路上私有仓库必然失败（已记录的架构决策，需凭据存储设计与安全评审）。
- **~~五类执行残留的收敛路径~~**（P2-M1 记录，见 `P2-M1-recovery-boundaries.md` §3.4）：
  `attempts` / `sessions` / `validation_runs` 已在 P2-M2 收敛；Attempt 级 `code_fetches`
  与 `container.state=unknown`、孤儿容器已在 **P2-M5** 收敛。
- **外部副作用的自动恢复入口**：P2-M4 交付前置门控与启动扫描，P2-M5 补齐了
  HTTP `verify-code-save` 入口与 `resume-eligibility` 查询（CLI `task resume-status`），
  使用户能拿到"哪条记录、下一步做什么"。**仍不自动重放**：核验与恢复推进都需要用户
  显式触发，这是刻意保留的边界。
- **容器残留与 merge 恢复的真实远程证据**：P2-M5 的容器 `unknown` / 孤儿收敛与 merge
  恢复判定只有本地受控端口 + 本机真实 Git 的证据，缺少真实远程 Docker 上的现场注入。
  P2 退出条件据此仍需在真实远程环境复核这一类行为。

**待调查**：

- **P2-M1 记录的阻塞/高风险缺陷**（需在 M2 处理，本阶段未改生产代码）：
  B1 步骤占用残留与 `blocked` 执行记录互相锁死（`workflowRunActivePredicate` 把 `blocked`
  算作未结束，而 `RecoverStepOccupancy` 据此拒绝收敛）；B2 `validation_runs` 残留使
  Attempt 永久无法再验证；B3 `attempts`/`sessions` 残留无收敛；B5 执行记录层取消生效但
  任务层未收敛；B6 租约只判定一次、无续租（10 分钟租约 vs 30 分钟默认步骤超时）；
  B7 `partial` 会话仍被标记 `archive_status=succeeded`。

- `Review` 步骤是否仍需要：P1-M2 已确认语义审查作为"某一步验证的一环"动态调用即可满足
  需求，`domain.BuildPlan` 与编排器都不需要新增步骤类型。是否要为其他用途引入独立审查
  阶段，留待真实需求出现。
- `verification_results` 表与 `domain.NewVerificationResult`（`internal/domain/
  verification.go` 的 `decideVerdict`）是历史遗留的第二套判定模型，生产链路已不再使用
  （唯一生产结论来源是 `DecideValidation`）。清理属 P2 范围。
- `validation_feedbacks` 是否应收录 Validator 的 `findings`：P1-M3 调查后的结论是
  **当前不是自动修复的必需信息**。进入修复的前提是必需确定性检查失败，而
  `DecideValidation` 在确定性检查未通过时不会采用审查结论，`reviewSemantics` 也因此不发起
  审查调用，此时不存在可用 `findings`。审查发现仍完整保存在 `assessment_json` 与
  `review_summary`；是否并入反馈结构（用于"审查判定未通过后继续修复"这类未来场景）留待
  真实需求出现时决定。

## 四阶段总览

| 阶段 | 目标 | 必要性 | 主要依赖 | 退出条件（摘要） |
|---|---|---|---|---|
| P1 单任务完整开发闭环 | 一条真实任务从需求走到代码交付 | 建立可运行的主业务流程，是一切可靠性与调度工作的前提 | 现有环境准备、计划、执行、验证、代码保存能力 | 至少一条真实路径端到端成立；确定性验证与语义审查职责明确；验证失败可受控修复并重验；成功/失败/停止/不确定都有可解释结论；满足必要完成条件才判成功；失败现场不被无条件删除 |
| P2 流程可靠性与恢复 | 超时、取消、重启、持久化失败、外部结果不确定时能正确停止或恢复 | P1 打通后，可靠性决定流程是否真的可用 | P1 退出 | 各阶段持久化与恢复边界清晰；恢复语义明确；重试上限与停止条件明确；commit/push/merge 恢复路径核验；关键恢复场景有集成测试 |
| P3 多任务执行与调度 | 多任务可被管理与调度且不破坏隔离 | 单任务可靠后才有调度价值 | P2 退出 | 队列/领取/调度语义明确；有界并发；隔离规则明确；取消/失败/恢复/重启可处理；并发与资源回收有验证 |
| P4 产品操作体验与优化 | 用户能清楚操作、观察、管理任务，并按真实瓶颈优化 | 主流程可用后再谈体验与优化 | P3 退出 | API/Web 操作缺口补齐；全生命周期操作入口；进度/验证/失败/恢复可见；继续/取消/恢复/查看可用；基于真实数据的优化落地 |

## 推进顺序

固定为 **P1 → P2 → P3 → P4**，不允许跳级：

1. 先在单任务上打通端到端闭环（P1），不提前建设通用调度框架。
2. 再为已打通流程补齐系统性的可靠性与恢复（P2），不把 P1 必须具备的最基本持久化、
   错误处理与失败停止能力推迟到 P2。
3. 单任务闭环可靠后，再建设多任务调度与资源隔离（P3）。
4. 主流程可用后，再做产品操作体验与针对性优化（P4），不与完整流程无关的 UI 美化、
   通用化抽象和推测性性能优化抢跑。

## 明确的设计原则

以下原则在相关里程碑中落实（详见各阶段文档）：

1. 确定性验证与模型语义审查分离，模型不能覆盖确定性检查的失败结果。
2. 不把模型调用成功等同于业务验证通过。
3. 验证失败必须能形成结构化、可追溯的修复依据。
4. 修复循环必须有上限、停止条件和明确的终态。
5. 每次真实执行、修复和验证应保留必要的 Attempt、Session 和结果记录。
6. 状态迁移遵守现有领域规则，不因方便编排而绕过领域状态机。
7. 持久化失败、结果不确定和外部操作冲突不能被伪造成成功。
8. 不确定的 push、merge 等外部副作用必须先核验，不得盲目重放。
9. 失败或中断时默认保留执行环境，除非现有业务规则明确允许清理。
10. 只有满足项目定义的代码保存和会话归档等必要条件后，才能将任务判为完整成功。
11. 优先复用已有的计划生成、工作流编排、验证、代码保存和恢复能力。
12. 不为尚未确定的未来场景预先引入复杂抽象。

## 实现策略与防返工原则

每个里程碑优先投资：

- 稳定的业务行为；
- 明确的领域约束；
- 可靠的持久化事实；
- 完整的端到端调用链；
- 可验证的验收标准；
- 与具体实现解耦的必要接口。

具体约束：

- 对于临时 Provider、临时执行路径、本机专用实现或尚未确定的抽象，必须先确认是否属于
  当前产品架构的正式组成部分，再投入建设。
- 不为方便局部测试或短期开发而恢复已经删除的本机 Pi 路径或相关代码。测试必须针对项目
  正式支持的运行架构（远程 SSH + Docker 容器）；测试替身保持在测试边界内，不得因此引入
  另一条生产执行路径。
- 如果某项优化不阻塞当前里程碑的正确实现，记录为后续事项，不插入当前主流程。

## 暂缓事项与理由

| 事项 | 暂缓理由 | 预计阶段 |
|---|---|---|
| 多任务调度队列、有界并发 | 单任务端到端闭环尚未成立，提前建设会与真实需求脱节 | P3 |
| 通用化抽象 / 插件化框架 | 尚无可支撑抽象的第二条真实实现，避免围绕临时实现提前投入 | 待真实需求出现 |
| 性能、并发、存储优化 | 缺少真实使用数据，属于推测性优化 | P4 |
| 与完整流程无关的 UI 美化 | 不阻塞主流程 | P4 |
| 计划重新生成 / 计划版本化 | 当前单次计划足够支撑 P1，版本化待真实需求 | 待定（不阻塞 P1） |
| 本机 Pi 执行路径 | 已按架构决策移除，测试只针对远程容器执行架构 | 不再恢复 |

## 阶段文档

- [P1-single-task-e2e.md](./P1-single-task-e2e.md)
- [P2-reliability-recovery.md](./P2-reliability-recovery.md)
- [P2-M1-recovery-boundaries.md](./P2-M1-recovery-boundaries.md)
- [P2-M5-closeout-protocol.md](./P2-M5-closeout-protocol.md)
- [P3-multi-task-scheduling.md](./P3-multi-task-scheduling.md)
- [P3-M1-task-queue-semantics.md](./P3-M1-task-queue-semantics.md)
- [P3-M2-bounded-concurrency.md](./P3-M2-bounded-concurrency.md)
- [P3-M3-execution-isolation.md](./P3-M3-execution-isolation.md)
- [P3-M4-scheduler-recovery.md](./P3-M4-scheduler-recovery.md)
- [P3-M5-multi-task-acceptance.md](./P3-M5-multi-task-acceptance.md)
- [P4-product-experience-optimization.md](./P4-product-experience-optimization.md)
