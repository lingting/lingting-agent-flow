# AgentFlow 项目路线图

> 本文件是路线图总览。具体里程碑的工作项、依赖与验收见各阶段文档。
> 撰写依据：commit `330e7c4`（2026-10-23）工作区的实际代码与测试状态，非旧对话或 README 推断；
> P1-M1 已在 commit `a0ee19b` 之后按实际核实与改动更新。

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

> **当前里程碑：P1-M2**（P1-M1 已于 2026-10-23 完成，证据见
> `P1-single-task-e2e.md` 的 P1-M1"完成记录"）。
>
> **待调查事项（不影响 M1 已完成结论）**：
> - `TestPlanGenerationInterruptedByShutdown` 在干净工作区上以约 40% 概率失败（测试内
>   `duringCall` 用 goroutine 触发 Stop，与取消传播存在竞态）。属既有测试缺陷，
>   待 P2（可靠性与恢复）阶段处理。
> - `tests/integration/remote/workflow/` 尚未覆盖真实远程"启动到计划"，留待 P1-M5 端到端验收。

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

**已有部分实现、尚未端到端集成**：

- Validator Agent 语义审查：领域类型与决策参数已就绪（`StepTypeReview`、
  `RoleTypeValidator`、`AgentAssessment`、`DecideValidation` 的 `assessment` 分支），
  但**没有生产调用方**——`internal/app/validation.go:157` 明确"不调用 Validator Agent"。
- 验证失败修复依据：`validation_feedbacks` 已持久化，且下次显式执行时被注入提示词
  （`internal/app/step_workflow_prompt.go`），但**没有自动修复循环**。

**尚未实现**：

- 自动验证失败修复与重新验证闭环（当前验证失败即停止为 `blocked`，等待用户显式
  `resume-workflow` 或重新执行）。
- 多任务调度队列、有界并发与任务领取。
- 真实端到端全路径验收测试（成功 / 验证失败 / 修复失败 / 无法确认结果）。

**待调查**：

- 计划生成是否能产出 `review`（Validator）步骤、以及 `Review` 步骤在编排器中的执行
  语义是否已有隐含设计——当前 `domain.BuildPlan` 只产出 `plan` + `develop`，编排器
  `NextStepToExecute` / `executableSteps` 也只处理这两类，`review` 尚无执行路径。
- Validator 辅助评估结果应如何持久化：`verifications` 表已有 `assessment_json` 列
  （迁移 `0008`/`0009`），但尚无生产写入路径。

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
