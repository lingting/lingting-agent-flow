# AgentFlow 项目路线图

> 本文件是路线图总览。具体里程碑的工作项、依赖与验收见各阶段文档。
> 撰写依据：commit `330e7c4`（2026-10-23）工作区的实际代码与测试状态，非旧对话或 README 推断；
> P1-M1 已在 commit `a0ee19b` 之后按实际核实与改动更新；
> P1-M2 已在 HEAD `3219591` 之后按实际接入与改动更新；
> P1-M3 已在 HEAD `2545e74` 之后按实际接入与改动更新；
> P1-M4 已在 HEAD `b7f7e22` 之后按实际核实与改动更新；
> P1-M5 已在 HEAD `b36a9d2` 之后按实际核实与改动更新。

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

> **当前阶段：P1 已完成**（P1-M1、P1-M2 于 2026-10-23 完成，P1-M3、P1-M4 于 2026-10-23
> 完成，P1-M5 于 2026-10-23 核实并完成；证据见 `P1-single-task-e2e.md` 的对应"完成记录"）。
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
> - `TestPlanGenerationInterruptedByShutdown` 在干净工作区上以约 40% 概率失败（测试内
>   `duringCall` 用 goroutine 触发 Stop，与取消传播存在竞态）。属既有测试缺陷，
>   待 P2（可靠性与恢复）阶段处理。
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
- 启动恢复（工作流运行记录 / 计划现场 / 取消现场收敛，
  `internal/app/task_workflow_run.go`、`internal/app/task_launch_recovery.go`）。
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
- **单任务端到端验收**（P1-M5）：本地 `tests/integration/local/workflow/e2e_*_test.go`
  （18 个用例）+ 真实远程 `tests/integration/remote/workflow/e2e_*_test.go`。真实 SQLite + 正式迁移、真实统一启动入口（`app.TaskLaunchService`）与真实
  `app.ExecutionEnvironmentService` 四阶段准备、真实编排器/执行器/验证引擎/语义审查/
  修复循环/收尾决策，以及真实 `internal/codesaver` 与真实 Git 仓库；只替身化远程 SSH、
  Docker 与容器内 Pi 进程。四类终局：成功（`completed`，代码真实推送、全部会话已归档）、
  验证失败后修复重验成功、修复达到上限（`blocked`/`attempts_exhausted`，历史全部保留）、
  结果无法确认（`recovery_required`，不重放外部副作用）。
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

- 多任务调度队列、有界并发与任务领取。
- 私有仓库的 Git 凭据支持：`source.GitCredentialProvider` 与 `codesaver.GitCredentialProvider`
  均无实现，真实链路上私有仓库必然失败（已记录的架构决策，需凭据存储设计与安全评审）。

**待调查**：

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
- [P3-multi-task-scheduling.md](./P3-multi-task-scheduling.md)
- [P4-product-experience-optimization.md](./P4-product-experience-optimization.md)
