# AgentFlow 阶段式开发路线图

本目录是 AgentFlow 项目**唯一的开发路线图真相来源**。它记录项目当前处于哪个阶段、
当前推进哪个里程碑、每一步为什么先做、以及如何判断"真正完成"。

任何后续开发都必须以本目录为依据，并在实施前后同步维护这些文档。路线图描述的是
**规划与现状**，不是产品文档，也不替代 `README.md` 或 `docs/` 中的设计说明。

## 用途

1. 让任何一个 Coding Agent（或人）在没有聊天历史的情况下，理解项目当前状态与下一步。
2. 约束开发范围：每个里程碑只做既定工作，不顺手扩大范围。
3. 记录"哪些能力已实现、哪些只部分实现、哪些待调查"，并附代码证据。
4. 记录每个里程碑的退出条件与验收标准，避免把"能跑起来"误判为"已完成"。

## 维护规则

- **代码与路线图必须一致**：开发过程中发现路线图与代码事实冲突时，先更新本目录的
  状态与证据，再确定实施方案；不要带着过时的结论继续写代码。
- **不把未实现写成已实现**：任何"已实现/部分实现"的判断都必须能指向文件、函数或测试。
- **不把设想写成既定事实**：未经确认的设计选择一律归入"风险与待决问题"或"待调查"。
- **完成定义是硬门槛**：只有满足里程碑的完成定义且证据充分，才能标 `completed`；
  只完成部分就保持 `in_progress` 并写明剩余条件。
- **验证结果如实区分**：通过、失败、跳过、未执行、无法确认必须分别记录，不得混写。

## 当前阶段与当前里程碑

- **当前阶段**：P2 进行中 —— 流程可靠性与恢复。
- **当前里程碑**：`P2-M4` —— 核验提交、推送、合并等外部副作用的恢复路径（`pending`）。
- **上一个里程碑**：`P2-M3` —— 明确修复次数上限、停止条件、重试资格和结果不确定处理
  规则，状态 `completed`（2026-10-24，基于 `master` HEAD `2a1bb0d` 实施）。交付内容：
  预算落到持久化事实（`repair_cycles` + `attempts.budget_scope*`，迁移
  `0022_repair_cycles.sql`）；重试资格与预算由 `domain.DecideRepairStart` /
  `domain.DecideRepairBudget` 统一定义，编排与恢复都引用它；用户显式新周期走
  `POST .../steps/{id}/open-repair-cycle`（CLI `agentflow task reopen-step`），只在上一
  周期已收敛且预算用尽或策略不再允许修复时成功；反馈写入失败由
  `RepairClassFeedbackUnavailable` 明确停止，首轮依据只取当前周期内最近一次验证。
  测试证据：`tests/unit/domain/repair_cycle_test.go`、
  `tests/integration/local/workflow/repair_budget_test.go`（含重启与并发）、
  `e2e_repair_limit_test.go` 的 `TestE2ERepairLimitSurvivesRestart`。
- **更早里程碑**：`P2-M2` —— 明确超时、取消、中断、进程重启后的恢复语义，状态
  `completed`（2026-10-24，基于 `master` HEAD `2a1bb0d` 实施与核实）。交付物为
  [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)：四类中断的
  判定依据、六层状态 × 四类中断的收敛矩阵、跨层一致性规则。本次同时修复了 B1（步骤占用
  与 `blocked` 执行记录互相锁死）、B5（取消意图跨层丢失）、B6（租约无续租），并新增
  `app.ExecutionResidueService` 收敛 `attempts` / `sessions` / `validation_runs` 残留。
- **更早里程碑（续）**：`P2-M1` —— 梳理所有阶段的状态持久化与恢复边界，状态 `completed`
  （2026-10-23，基于 `master` HEAD `7a55dfe` 核实）。交付物为
  [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)：14 个阶段的完整恢复边界
  矩阵、服务启动恢复的真实调用链（`HostValidationService` → `StepExecutor`/`Validation`/
  `StepWorkflow`（无恢复）→ `TaskWorkflowService` → `TaskLaunchService`）、
  自动恢复/用户触发/只读核验/只能安全停止/完全缺失五级分类、G1–G14 未覆盖故障窗口清单，
  以及 6 个阻塞或高风险缺陷（B1–B6）与 1 个语义不一致（B7）。该里程碑**未修改生产代码**。
  `P1-M5` —— 完成单任务端到端验收，状态 `completed`（2026-10-23），本地
  `tests/integration/local/workflow/e2e_*_test.go`（18 个用例）+ 真实远程
  `tests/integration/remote/workflow/e2e_*_test.go`，覆盖成功 / 修复 / 修复上限 / 无法确认
  四类终局。`P1-M4` —— 将修复闭环与代码保存、会话归档、任务收尾串联（核实中修复了
  Developer 会话从未写入数据库、收尾终态写入失败留下 `running` 残留两处生产断点）。
  `P1-M3` —— 有上限、可停止的验证失败修复与重新验证闭环。`P1-M2` —— Developer、
  确定性验证与 Validator Agent 的完整验证决策链。`P1-M1` —— 任务启动至计划生成的前置流程。
- **下一步**：按"下一步工作的选择规则"进入 P2-M4（commit / push / merge 的恢复核验）。
  P2-M3 的交接事项：`interrupted` / `unknown` / `recovery_required` 尝试一律阻止自动重放
  与开启新周期，只保留人工核验；`code_fetches` 与 `container.state=unknown` 两类残留仍
  没有收敛路径（B4/G14，属 M4/M5）；"任务结论存疑"（`tasks.completed` 与
  `workflow_runs.recovery_required` 同时成立）仍没有专门表达。
  P1 遗留的真实缺口是私有仓库 Git 凭据支持
  （`source.GitCredentialProvider` / `codesaver.GitCredentialProvider` 无实现），
  记录在 `ROADMAP.md` 的"尚未实现"。

> 阶段与里程碑的状态可能随开发推进而变化，以上仅为撰写本路线图时的快照；最新状态
> 以 `ROADMAP.md` 的"当前实现状态摘要"、`P1-single-task-e2e.md` 的里程碑表与
> `P2-reliability-recovery.md` 的里程碑清单为准。

## 文档索引

| 文档 | 内容 |
|---|---|
| [`ROADMAP.md`](./ROADMAP.md) | 项目最终业务目标、端到端主流程概览、四阶段总览、推进顺序、现状摘要、暂缓事项 |
| [`P1-single-task-e2e.md`](./P1-single-task-e2e.md) | P1：单任务完整开发闭环（当前优先，最详细） |
| [`P2-reliability-recovery.md`](./P2-reliability-recovery.md) | P2：流程可靠性与恢复 |
| [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md) | P2-M1：全链路状态持久化与恢复边界清单（14 阶段矩阵、启动恢复调用链、缺陷与测试缺口） |
| [`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md) | P2-M2：四类中断 × 六层状态的收敛语义矩阵与跨层一致性规则 |
| [`P3-multi-task-scheduling.md`](./P3-multi-task-scheduling.md) | P3：多任务执行与调度 |
| [`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md) | P4：产品操作体验与针对性优化 |

## 下一步工作的选择规则

按以下优先级选取尚未完成的里程碑（顺序即权重，不可因为"更容易"而倒置）：

1. 阻塞完整端到端流程的缺失能力。
2. 当前主流程必须具备的基础正确性与必要测试。
3. 打通阶段内剩余的业务环节。
4. 阶段验收和端到端验证。
5. 已经影响流程可靠性的恢复与安全问题。
6. 后续阶段的扩展能力。
7. 不阻塞流程的局部优化与重构。

在一个里程碑内部有多个工作项时，优先选择**能解除最多后续依赖、让真实业务流程向前
推进**的工作项，而不是代码量最小、最容易测试或最容易抽象的工作项。

## 每次开发前

1. 读取本文件、`ROADMAP.md` 与当前阶段文档。
2. 检查最新代码、工作区状态与相关测试，不能只依赖上次结论。
3. 确认当前阶段、当前里程碑、前置条件与尚未解决的问题。
4. 判断该工作是推进当前主流程，还是仅属于可延期优化。
5. 将本次计划限定在当前里程碑范围内。
6. 发现路线图与代码事实不一致时，先更新规划状态和证据，再确定实施方案。

## 每次开发中

1. 不擅自扩大工作范围。
2. 发现当前设计无法支撑端到端流程时，记录具体阻塞及证据。
3. 只有确实影响主流程或基础正确性时，才调整里程碑依赖。
4. 不把临时解决方案默认为永久架构。
5. 不因局部优化而偏离当前阶段目标。

## 每次开发后

1. 根据实际代码差异更新当前里程碑状态。
2. 记录实际修改的文件、功能结果、测试命令与真实测试结果。
3. 明确区分通过、失败、未执行和无法确认的验证结果。
4. 更新已完成工作、剩余工作、阻塞项、风险与后续任务。
5. 只有满足完成定义且证据充分时，才把里程碑标记为 `completed`。
6. 只完成部分就保持 `in_progress`，明确剩余条件。
7. 实现过程中发现路线图有误时，更新路线图并说明修改理由。
8. 更新本文件中的当前阶段与下一步入口，确保后来者不需要依赖聊天历史。
