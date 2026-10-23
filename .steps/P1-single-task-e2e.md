# P1 —— 单任务完整开发闭环

> 状态与证据撰写依据：commit `330e7c4`（2026-10-23）的代码状态；P1-M1 部分已按
> commit `a0ee19b`（2026-10-23）之后的实际核实与改动更新。

## 阶段目标

让一条任务能够从用户需求出发，经过环境准备、规划、开发、验证、必要的修复，最终完成
代码交付。阶段结束时，用户实际能完成：

- 创建任务后，用一条统一入口把它推进到"代码已提交/推送（可选合并）且任务收尾"。
- 确定性验证与 Validator 语义审查各司其职，模型结论不能覆盖确定性检查失败。
- 验证失败时进入**有上限、可停止**的修复流程，修复后重新验证，直至通过或明确终止。
- 成功、失败、停止、结果不确定四种结局都有真实、可解释的结论与持久化证据。
- 任务只有满足"代码保存 + 会话归档"等必要完成条件后才被判为 `completed`。
- 失败现场与必要记录不会被无条件删除。

## 必要性

当前主链路（启动 → 容器 → 工作区 → 计划 → 逐步开发+确定性验证 → 代码保存 → 归档核验 →
收尾）的绝大部分能力已经存在，但**缺口恰好卡在"真正交付"这一环**：

1. **Validator Agent 语义审查没有生产调用方（已由 P1-M2 补齐）**：`domain.DecideValidation`
   早已定义 `assessment` 参数与"只能收紧、不能放宽"的规则，但接入前没有任何调用方。确定性
   检查只能证明"命令通过"，不能替代对需求是否被真正满足的语义判断；没有它，验证决策链
   是残缺的。（现状见"已完成"表格中的 Validator 条目。）
2. **验证失败后没有自动修复闭环**：失败只停止为 `blocked`，等待用户显式 `resume-workflow`
   或重新执行。虽然 `validation_feedbacks` 已经持久化并能在下次执行时注入提示词，但
   "修复→重新验证"的循环没有编排入口与终止条件。
3. **没有端到端全路径验收**：现有测试覆盖了大量单元与本地集成路径，但没有一条真实
   "成功 / 验证失败 / 修复失败 / 无法确认结果"的完整验收，无法证明端到端闭环真的成立。

不做 P1，就无法宣称"AgentFlow 能完整跑通一个任务"，后续可靠性、调度与产品体验都建立在
一个未闭环的主流程之上。

## 前置条件

以下能力已存在，P1 直接复用，不重复实现：

- 任务管理、执行节点与环境预检、远程 Docker 执行环境与容器生命周期。
- 代码获取与 `agent/<task-id>` 分支初始化。
- 统一任务启动（`DecideTaskLaunchNext` 分阶段推进）。
- 计划生成与结构化持久化（`domain.BuildPlan`）。
- 单步骤执行、单步骤开发—验证闭环、确定性验证引擎。
- 任务级工作流编排、代码保存、会话归档核验、任务收尾（`DecideWorkflowCloseout`）。
- 启动恢复与代码保存恢复核验。

## 当前状态（按能力区分）

### 已完成（有代码证据）

| 能力 | 证据 |
|---|---|
| 统一任务启动（容器→工作区→计划→工作流） | `internal/app/task_launch_run.go`、`internal/domain/task_launch.go` `DecideTaskLaunchNext`；集成测试 `tests/integration/local/workflow/launch_test.go` |
| 计划生成与持久化 | `internal/app/task_workflow_plan.go`、`internal/domain/plan_generation.go` `BuildPlan`；单测 `tests/unit/app/plan_generation_test.go` |
| 确定性验证引擎 | `internal/app/validation.go`、`internal/domain/validation_decision.go` `DecideValidation`；单测 `tests/unit/app/validation_test.go` |
| 单步骤开发—验证闭环 | `internal/app/step_workflow_run.go` `ExecuteAndValidate`、`internal/domain/step_verification.go` `DecideStepCloseout`；单测 `tests/unit/app/step_workflow_test.go` |
| 任务级工作流编排与收尾 | `internal/app/task_workflow_exec.go`、`internal/domain/workflow_run.go` `CheckWorkflowStart`/`DecideWorkflowCloseout`；单测 `tests/unit/app/task_workflow_test.go` |
| 代码保存与恢复核验 | `internal/app/code_save*.go`、`internal/domain/code_save*.go`；集成测试 `tests/integration/local/codesaver/` |
| 验证失败结构化反馈 | `internal/domain/validation_feedback.go`、`internal/app/step_workflow_run.go` `persistFeedback`、`internal/app/step_workflow_prompt.go` |
| Validator 语义审查（生产调用链 + 评估持久化） | `internal/app/validation_review.go` `ValidatorReviewer`、`internal/app/validation_run.go` `reviewSemantics`、`internal/domain/agent_assessment_parse.go`、`internal/domain/validation_decision.go` `ReviewStatus`；迁移 `0021_validator_review.sql`；测试 `tests/unit/domain/agent_assessment_test.go`、`tests/unit/app/validation_review_test.go` |
| Developer 执行会话持久化 | `internal/app/step_executor_run.go` `callAgent`（`SessionService.Start` → `RunAgent` → `Finish`）；集成测试 `tests/integration/local/workflow/delivery_chain_test.go` |
| 修复后交付链路（修复→代码保存→归档→收尾） | `internal/app/task_workflow_exec.go` `finalize`/`saveCode`/`sessionsPersisted`/`stopUnpersistedCloseout`、`internal/domain/workflow_run.go` `DecideWorkflowCloseout`；集成测试 `tests/integration/local/workflow/delivery_chain_test.go` |
| 验证失败修复与重新验证闭环 | `internal/app/step_workflow_repair.go` `runRepairLoop`、`internal/domain/retry.go` `DecideRepairLoop`、`internal/domain/step_verification.go` `DecideRepairCloseout`；单测 `tests/unit/app/step_workflow_repair_test.go`、`tests/unit/app/task_workflow_repair_test.go` |

### 部分完成（已有基础，缺端到端集成）

| 能力 | 已有部分 | 缺口 |
|---|---|---|
| 真实远程端到端验收 | 本地集成测试已覆盖完整链路（受控替身边界内） | `tests/integration/remote/*` 仅覆盖前置条件，真实 SSH + Docker + 容器内 Pi 的全路径属 M5 |

### 未实现

- 真实端到端全路径验收测试（P1-M5）。

### 待调查

- Validator 评估应写入 `validation_runs` 还是 `verification_results`：M2 已确认生产链路
  只写 `validation_runs`，评估落 `assessment_json`；`verification_results` 与
  `domain.NewVerificationResult` 只有测试在用，属待清理的历史遗留双判定模型（P2 事项）。
- `validation_feedbacks` 是否应收录 Validator 的 `findings`：M2 未扩大该结构，审查发现完整
  保留在 `assessment_json` 中；是否纳入修复提示词由 M3 决定。

## 阶段范围

**做**：

- 打通并核验"任务启动 → 计划生成"前置流程（M1）。
- 把 Validator Agent 接入验证决策链，明确其与确定性验证的职责边界（M2，已完成）。
- 实现有上限、可停止的验证失败修复与重新验证闭环（M3）。
- 把修复闭环与代码保存、会话归档、任务收尾串联成完整端到端路径（M4）。
- 用真实运行路径完成成功 / 验证失败 / 修复失败 / 无法确认结果的验收（M5）。

**不做**：

- 不建设多任务调度队列、有界并发与任务领取（P3）。
- 不系统性补齐超时/取消/重启/持久化失败的恢复语义（P2；但 P1 已具备的最基本持久化、
  错误处理与失败停止能力不得退化）。
- 不做与主流程无关的 UI 美化、通用化抽象与推测性性能优化。
- 不恢复已删除的本机 Pi 执行路径；测试只针对远程容器执行架构，替身保持在测试边界内。
- 不重写已经存在的环境准备、计划、确定性验证、代码保存与收尾能力。

## 里程碑清单与依赖

```text
P1-M1（核实启动→计划前置流程）──► P1-M2（Developer+确定性验证+Validator 决策链）
                                          │
                                          ▼
P1-M4（串联修复闭环与代码保存/归档/收尾） ◄── P1-M3（有上限的修复与重验闭环）
                                          │
                                          ▼
                                   P1-M5（端到端全路径验收）
```

| ID | 名称 | 状态 | 依赖 |
|---|---|---|---|
| P1-M1 | 核实并打通任务启动至计划生成的前置流程 | `completed` | 无 |
| P1-M2 | 打通 Developer、确定性验证与 Validator Agent 的完整验证决策链 | `completed` | P1-M1 ✅ |
| P1-M3 | 实现有上限、可停止的验证失败修复与重新验证闭环 | `completed` | P1-M2 ✅ |
| P1-M4 | 将修复闭环与代码保存、会话归档、任务收尾串联 | `completed` | P1-M3 ✅ |
| P1-M5 | 完成单任务端到端验收（成功/验证失败/修复失败/无法确认） | `pending` | P1-M4 |

> P1-M1 已完成（证据见该里程碑"完成记录"）：启动链路已核实并修补，成功/幂等/失败/
> uncertain 四类路径均有真实数据库上的测试证据。
> P1-M2 已完成（证据见该里程碑"完成记录"）：Validator Agent 已接入真实生产调用链，
> 评估写入 `validation_runs`，最终结论统一由 `domain.DecideValidation` 计算。
> P1-M3 已完成（证据见该里程碑"完成记录"）：修复闭环已接入真实生产调用链，轮次上限、
> 停止条件与终态映射统一由 `domain.DecideRepairLoop` 与 `domain.DecideRepairCloseout`
> 计算，编排器只消费领域判定。
> P1-M4 已完成（证据见该里程碑"完成记录"）：修复通过的步骤会自然进入代码保存、
> 会话归档核验与收尾；`DecideWorkflowCloseout` 的必要条件在真实路径中被执行，
> 保存失败/不确定/未确认、归档缺失与会话查询失败、收尾记录写入失败都会停在
> `blocked` / `recovery_required`，不会伪装成 `completed`。核实中修复了两处真实
> 生产断点（Developer Session 从不落库、收尾终态写入失败留下 `running` 残留）。
> M5 仍为 `pending`；当前里程碑为 **P1-M5**。

## 阶段退出条件（可验证的客观标准）

1. 至少一条真实单任务路径能从需求走到代码交付（`completed` 且 `code_saves` 记录提交/推送
   成功、会话归档成功）。
2. 确定性验证与 Validator 语义审查的职责在代码与文档中都明确分离，且模型结论不能覆盖
   确定性检查的失败结果。
3. 验证失败能进入受控修复流程并重新验证；修复有次数上限、停止条件与明确终态。
4. 成功、失败、停止、结果不确定四种结局都有真实且可解释的结论与持久化记录。
5. 任务只有满足 `DecideWorkflowCloseout` 定义的必要完成条件（步骤全通过 + 代码保存 + 会话
   归档）才被判为 `completed`。
6. 失败或中断现场与必要记录不被无条件删除（默认保留执行环境）。
7. 上述路径有对应的自动化测试证据（真实远程运行路径，或在测试边界内用受控替身覆盖），
   且结果如实区分通过 / 失败 / 跳过 / 未执行。

## 风险与边界

- **错误状态**：把"模型调用成功"当成"验证通过"、把"步骤全通过"当成"任务完成"，都会绕过
  既有的 `DecideValidation` / `DecideWorkflowCloseout` 规则，必须在 M2/M4 中显式防止。
- **重复副作用**：修复循环若直接重放外部操作（push/merge），可能产生重复提交或重复合并；
  必须复用既有"先核验、后重放"的代码保存恢复语义。
- **数据丢失**：修复循环不得覆盖旧 Attempt / Session / 验证记录；每次修复与重验都要新建
  Attempt 与 Session，历史保留。
- **无效投入**：在 Validator 语义审查尚未确定接入方式时，过早为其设计独立步骤类型、独立
  调度或通用评估框架，可能随真实接入方式变化而返工。先确认最小可用的接入点，再决定是否
  需要新步骤类型。
- **绕过领域状态机**：修复循环若在业务层直接赋值步骤/任务状态，会绕过 `Transition` 校验，
  属于禁止项。

## 下一步选择规则

在 P1 内部按"解除最多下游依赖"原则选择：

1. **P1-M1 已完成**（启动→计划前置流程已核实并修补）。
2. **P1-M2 已完成**（Validator 接入真实生产调用链，评估持久化，结论统一由
   `DecideValidation` 计算）。
3. **P1-M3 已完成**（有上限的修复与重验闭环已接入生产调用链，领域判定统一由
   `DecideRepairLoop` / `DecideRepairCloseout` 计算）。
4. 当前应推进 **P1-M4**：串联收尾依赖 M3 已成立的"步骤终态与停止原因可解释"结论。
5. 最后 **P1-M5**：验收依赖前四项全部成立。

---

## 里程碑详情

### P1-M1 核实并打通任务启动至计划生成的前置流程

- **ID**：`P1-M1`
- **名称**：核实并打通任务启动至计划生成的前置流程
- **状态**：`completed`（2026-10-23 核实并修补，证据见下方"完成记录"）
- **必要性**：P1 的入口是"从需求到可执行计划"。启动链路（容器→工作区→计划）若存在
  隐蔽断点（如任务启动阶段判定、工作区初始化结果不确定、计划持久化失败），后续所有
  里程碑都建立在不可靠的地基上。该里程碑以"核实 + 修补最小缺口"为主，不重复建设已
  存在的能力。
- **当前证据**：
  - 已实现：`internal/app/task_launch_run.go`（`Launch` 分阶段推进）、
    `internal/domain/task_launch.go`（`DecideTaskLaunchNext` 返回 container/workspace/
    plan/workflow）、`internal/app/task_launch_recovery.go`。
  - 已实现：工作区初始化（`internal/app/code_initialize.go`、迁移 `0020_task_workspace_initializations.sql`）。
  - 已实现：计划生成（`internal/app/task_workflow_plan.go`、`internal/domain/plan_generation.go`）。
  - 测试：`tests/integration/local/workflow/launch_test.go`（跨阶段持久化、不重复建容器、
    工作区 uncertain 停止、重启后恢复）；`tests/unit/app/plan_generation_test.go`。
  - 测试：`tests/integration/local/workflow/launch_chain_test.go`（真实 SQLite + 真实
    `TaskLaunchService`/`TaskWorkspaceService`/`TaskWorkflowService`，只把容器内 Git 与
    Pi 调用替换为受控替身：启动到 `planned`、重复启动幂等、工作区失败停止、uncertain
    不重放）；`tests/integration/local/workflow/launch_test.go`；`tests/unit/app/
    plan_generation_test.go`（含事务内步骤写入失败的整体回滚）。
  - 剩余缺口（不阻塞 M1）：`tests/integration/remote/workflow/` 仍只有前置条件测试。
    该目录的真实远程启动用例需要在容器内运行真实 Planner，属于 M5 端到端验收范围；
    M1 的完成定义允许使用"测试边界内受控替身"覆盖，因此不阻塞本里程碑。
- **前置依赖**：无。
- **目标结果**：用户执行统一启动入口后，能可靠地到达"任务为 `planned` 且计划步骤已落库"
  的状态；启动各阶段的失败能被区分定位（容器 / 工作区 / 计划），工作区结果不确定时按
  现有语义停止并等待核验，不重放 Clone。
- **基本工作项**（按逻辑顺序）：
  1. 对照 `DecideTaskLaunchNext` 与 `Launch` 阅读启动链，确认每一阶段的前置条件与失败
     映射是否有缺口。
  2. 核实工作区初始化 `uncertain` 时接口返回 409、不重放 Clone 的语义是否完整。
  3. 核实计划生成"单事务写入全部步骤 + 推进 `planned`"的失败回滚行为。
  4. 若发现断点，做最小修补（不重构、不扩大范围）。
  5. 补一条真实远程环境（或测试边界内受控替身）的"启动到计划"端到端用例，如实记录
     通过/跳过/未执行。
- **涉及范围**：`internal/app/task_launch*.go`、`internal/app/code_initialize.go`、
  `internal/app/task_workflow_plan.go`、`internal/domain/task_launch.go`、
  `internal/domain/plan_generation.go`、`tests/integration/remote/workflow/`、
  `tests/integration/local/workflow/`。
- **状态与数据约束**：启动阶段只能通过 `domain` 状态机推进；工作区初始化结果 `uncertain`
  必须持久化且阻止自动继续；计划步骤必须全部落库后任务才推进 `planned`；任何失败不得
  伪装成"已就绪"。
- **测试与验收**：
  - 成功路径：任务从 `queued/preparing` 经容器、工作区、计划到达 `planned`，步骤完整。
  - 失败路径：容器准备失败、工作区 uncertain、计划输出非法、计划持久化失败各自收敛到
    正确状态并保留证据。
  - 边界：重复启动不创建第二个容器；`recovery_required` 步骤阻止启动。
- **完成定义**：真实远程（或受控替身）环境下一次启动能到达 `planned`；各失败路径有测试
  证据且状态正确；无新增绕过状态机的代码。
- **非目标**：不新增容器自动修复、不重做计划生成逻辑、不引入任务调度。
- **风险与待决问题**：真实远程环境的测试依赖 SSH/Docker/Pi 配置，可能在 CI 中跳过；需
  区分"未配置导致跳过"与"配置完整但失败"。
- **完成记录**（2026-10-23，分支 `master`，改动前 HEAD `a0ee19b`）：

  核实结论：主链路（创建任务 → 准备容器 → 初始化工作区 → Planner 生成计划 → 持久化 →
  `planned`）在代码中成立，`DecideTaskLaunchNext` 与 `Launch` 的阶段判定一致，容器/工作区
  /计划各自具备幂等边界与 uncertain 停止语义。核实中发现 2 处证据缺口，均已最小修补。

  实际改动（2 项，均为必要修补）：

  1. `internal/app/task_launch_run.go`（+4 行）：修复报告缺陷。计划早已持久化时启动跳过
     plan 阶段，旧实现只在 plan 分支内赋值 `PlanPersisted`，导致重复启动返回
     `plan_persisted=false` 却同时 `task_status=planned` 的自相矛盾结论，违反该字段
     "计划是否已完整写入数据库"的契约（HTTP 与 CLI 都据此展示）。改为先按
     `len(input.Steps)==0` 给出持久化事实，只在真正执行计划生成时用生成结果覆盖。
     判定规则仍在 `domain`，本改动不新增第二套业务语义。
  2. `tests/unit/app/plan_generation_test.go`（改 1 个用例 + 注入点）：原
     `TestGeneratePlanRollsBackOnPersistFailure` 通过 `planStore.updateTaskErr` 注入失败，
     但该失败发生在 `beginPlanning` 阶段——即事务之前，用例实际只覆盖"事务外被拒绝"，
     未覆盖回滚。新增 `planStore.WithTx` + `failingStepTx`，把失败注入真实事务内的
     `CreateSteps`，并断言 Planner 已真实调用、步骤零落库、任务未进入 `planned`。
     已用变异测试确认：让 `failingStepTx` 先真实写入步骤再返回错误，用例仍通过，
     证明事务回滚确实撤销了写入。

  新增测试：`tests/integration/local/workflow/launch_chain_test.go`（4 个用例）
  - `TestLaunchChainReachesPlannedWithPersistedPlan`：成功路径。1 次启动经工作区初始化与
    Planner 生成到达 `planned`，断言 3 个步骤（序号 1 为 `plan`、其余 `develop`）、任务级
    必需命令已合并、工作区成功记录带 `commit_sha` 且关联当前容器、代码获取 1 次、
    Planner 1 次、编排启动 1 次。
  - `TestLaunchChainIsIdempotentOnRepeat`：幂等路径。重复启动不重复 Clone、不重复调用
    Planner、不覆盖已有计划（逐步骤比对步骤 ID），且如实报告计划已持久化。
  - `TestLaunchChainStopsOnWorkspaceFailure`：工作区阶段失败（`repository_access_failed`）
    后不调用 Planner、不启动编排、不留步骤、任务不进入 `planned`。
  - `TestLaunchChainRejectsUncertainWorkspaceWithoutReplay`：工作区 `uncertain` 时返回
    `ErrTaskLaunchConflict`，不重放代码获取，记录原样保留。

  测试结果（如实区分）：
  - 通过：`go test -count=1 -p 1 ./tests/...` → 45 个包全部通过（1528 个用例，含新增 4 个）。
  - 通过（真实远程）：`go test -count=1 ./tests/integration/remote/...` → 8 个用例全部真实
    执行并通过（SSH 到 `192.168.91.20`，Docker CLI/Engine 29.9.1、API 1.56 落在 1.52 基线
    区间内），无跳过。
  - 未执行：`tests/integration/remote/workflow/` 的真实"启动到计划"用例——M1 尚未新增该
    用例（原因见上"剩余缺口"），因此**没有**也未声称该路径的真实远程证据。
  - 已知既有问题（非本次改动引入）：`tests/integration/local/workflow` 的
    `TestPlanGenerationInterruptedByShutdown` 在**未改动的干净工作区**上以约 40% 概率失败
    （`恢复测试` 中 `duringCall` 在 goroutine 内触发 Stop，与实际取消传播存在竞态）。已用
    `git stash` 回到改动前状态复现 8 次，失败 4 次，与本里程碑改动无关；按"不修改 M1 范围
    外代码"的要求未修改，记录为待后续处理。

  检查：`gofmt -l internal cmd tests` 无输出；`go vet ./...` 无输出。

  未解决问题：上述 `TestPlanGenerationInterruptedByShutdown` 的既有竞态；真实远程
  "启动到计划"验收用例留待 M5。

---

### P1-M2 打通 Developer、确定性验证与 Validator Agent 的完整验证决策链

- **ID**：`P1-M2`
- **名称**：打通 Developer、确定性验证与 Validator Agent 的完整验证决策链
- **状态**：`completed`（2026-10-23 接入生产调用链，证据见下方"完成记录"）
- **必要性**：这是 P1 的结构性缺口。确定性检查只能证明"命令通过"，不能证明"需求被真正
  满足"；`domain.DecideValidation` 已经为 Validator 语义审查预留了"只能收紧、不能放宽"
  的位置，但没有生产调用方。不补齐它，"开发→验证"就缺了语义审查这一环，后续修复闭环也
  缺少高质量的修复依据。
- **当前证据**：
  - 已实现：确定性验证（`internal/app/validation.go`）、`DecideValidation` 的确定性规则
    （`internal/domain/validation_decision.go`）。
  - 已实现：`domain.StepTypeReview`、`domain.RoleTypeValidator`、`domain.AgentAssessment`
    （`internal/domain/step.go`、`internal/domain/role.go`、`internal/domain/verification.go`）。
  - 已实现：`verifications.assessment_json` 列（迁移 `0008`/`0009`）。
  - 缺口：`internal/app/validation.go:157` 明确"不调用 Validator Agent"；`Review` 步骤无
    执行路径（`domain.BuildPlan` 只产出 `plan` + `develop`，`NextStepToExecute` /
    `executableSteps` 只处理这两类）；评估结果无生产写入路径。
- **前置依赖**：`P1-M1`。
- **目标结果**：在每个开发步骤的确定性验证之后，能（按既定策略）调用 Validator Agent 做
  语义审查，把结构化评估写入持久化记录，并让 `DecideValidation` 依据"确定性检查 + 评估"
  得出最终结论；模型结论不能覆盖确定性检查失败。
- **基本工作项**（按逻辑顺序）：
  1. 确定 Validator 的接入方式：是作为每个 `develop` 步骤验证后的一次调用，还是引入
     `Review` 步骤（先读现有 `StepTypeReview` 语义与 `CheckWorkflowStart` 对步骤类型的
     限制，确认最小可用接入点，避免为此重写编排器）。
  2. 复用 `app.ContainerService.RunAgent`（或等价统一入口）发起 Validator 调用，保证独立
     Session、独立 Attempt/Invocation，遵守既有 Session 生命周期约束。
  3. 定义 Validator 输出协议与 `AgentAssessment` 的映射，把评估写入 `verifications`
     （`assessment_json`）或相应持久化字段。
  4. 把评估传入 `DecideValidation`，确认"确定性失败不可被评估覆盖、评估只能收紧"的
     规则被真实执行。
  5. 在步骤收口（`DecideStepCloseout`）与任务收尾中保持结论一致，不重复实现判断。
  6. 为验证失败时的评估结果建立可追溯、可脱敏的修复依据（复用 `validation_feedbacks`）。
- **涉及范围**：`internal/app/validation.go`、`internal/app/step_workflow_run.go`、
  `internal/app/agent_container.go`、`internal/domain/validation_decision.go`、
  `internal/domain/verification.go`、`internal/storage/sqlite/verifications.go`、
  `internal/app/step_workflow_prompt.go`、`tests/unit/app/validation_test.go` 等。
- **状态与数据约束**：Validator 调用是真实 Agent 调用，必须独立 Session/Attempt 并持久化；
  评估写入失败不得把验证判为通过；确定性检查失败时评估结果不得改写结论；评估要求人工复核
  时结论为 `inconclusive`。
- **测试与验收**：
  - 成功路径：确定性通过 + 评估通过 → 结论 `passed`。
  - 失败路径：确定性失败 + 评估通过 → 仍 `failed`；确定性通过 + 评估未通过 → `failed`；
    评估不可解释或要求复核 → `inconclusive`。
  - 边界：评估持久化失败不得判通过；Validator 会话失败/超时/取消的结论映射正确。
- **完成定义**：存在生产调用路径把 Validator 评估接入 `DecideValidation`；`AgentAssessment`
  有持久化写入与读取；单元测试覆盖"评估只能收紧"的核心规则；无绕过确定性规则的代码。
- **非目标**：不实现自动修复循环（M3）；不做独立调度队列；不重写 `DecideValidation` 的
  确定性规则。
- **风险与待决问题**：`Review` 步骤 vs 动态调用尚未定案，需要先确认最小编排改动；Validator
  输出协议需与真实模型行为磨合，避免过度结构化导致调用频繁失败。
- **完成记录**（2026-10-23，分支 `master`，改动前 HEAD `3219591`）：

  调查结论（先调查后改码）：生产链路的唯一验证结论来源是
  `domain.DecideValidation`（`internal/app/validation_run.go` 调用）；`verification_results`
  表、`domain.NewVerificationResult`、`domains/verification.go` 的 `decideVerdict` 只有
  测试在写，**不是**生产写入路径。因此没有把评估写进 `verification_results`，而是写进
  `validation_runs`（生产链路真实落库的那张表），避免同时维护两套判定模型。

  实际断点（改动前）：
  1. `ValidationService` 没有任何 Validator 调用方（注释明确写"不调用 Validator Agent"）。
  2. `DecideValidation` 的 `Assessment` 参数在生产链路永远为 nil。
  3. `Review` 步骤（`StepTypeReview`）没有任何执行路径：`BuildPlan` 只产 `plan`+`develop`，
     `CheckWorkflowStart` 拒绝非 `plan`/`develop` 步骤。
  4. `validation_runs` 只有 `validator_session_id` 一个预留列，没有审查状态与评估列。

  接入方式选择：**动态调用，不引入 `Review` 步骤**。理由是可证伪的最小方案：
  `CheckWorkflowStart` 会直接拒绝 `review` 步骤、`NextStepToExecute` 也不处理它，引入步骤
  类型就必须改动计划生成、步骤排序与编排器三处，而语义审查在语义上就属于"某一步的验证"，
  不是独立执行阶段。因此接入点固定在 `ValidationService.runValidation`：确定性检查跑完后、
  计算结论之前调用。

  生产调用链（`internal/app/validation_run.go` → `internal/app/validation_review.go`）：
  ```text
  executeChecks（逐项真实执行 + 逐项落库）
    → reviewSemantics：三条门控全成立才调用 Validator
        ① 服务装配了语义审查能力（`ValidationOptions.Reviewer`）
        ② 确定性流程正常跑完（completed，非 cancelled/timed_out/failed）
        ③ 仅凭确定性检查即可判通过（必需检查全过）
    → ValidatorReviewer.Review
        loadValidatorRole（类型/启用/模型校验，缺失 → ErrReviewNotConfigured）
        → SessionService.Start（进程启动前写库，独立 Session + InvocationID）
        → ContainerService.RunAgent（真实容器内 Pi 调用，工作目录 = 任务工作区）
        → SessionService.Finish（未取消上下文，写产出与归档）
        → domain.ParseAgentAssessment（结构化解析 + 领域校验）
    → domain.DecideValidation{RunStatus, Results, ReviewStatus, Assessment}
    → finalizeValidation（结论 + 审查状态 + 审查会话 + 评估一次写入）
  ```

  输出协议与校验（`internal/domain/agent_assessment_parse.go`）：
  - 只接受一个完整 JSON 对象，允许围栏与前后说明文字；
  - `DisallowUnknownFields`，`passed` 必须显式出现；
  - 解析后再走 `AgentAssessment.Validate`（findings/evidence 条数上限复用领域常量）；
  - 错误分两类：`ErrAssessmentUnparseable`（不是可解析结构）与 `ErrAssessmentInvalid`
    （结构合法但违反领域约束），分别映射 `unparseable` / `invalid_output`；
  - 解析失败、调用失败、超时、取消、中断、未配置一律不产出评估，因此不可能通过。

  统一决策（`internal/domain/validation_decision.go`）：
  - 新增 `ReviewStatus` 与 `ValidationDecisionInput.ReviewStatus`；
  - `decideWithReviewStatus` 只做"证据门槛"：未要求审查不阻塞；审查完成且可解释不阻塞；
    其余状态（含"声称通过但评估缺失"）一律 `inconclusive`；
  - 顺序固定在必需检查判定之后，因此必需检查失败时审查结论无效（无覆盖可能）。

  持久化（迁移 `0021_validator_review.sql`，只加列不重建表）：
  - `validation_runs.review_status`（审查状态）
  - `validation_runs.review_summary`（脱敏裁剪后的说明）
  - `validation_runs.assessment_json`（结构化 `AgentAssessment`，`json_valid` 约束）
  - `validator_session_id` 补齐生产写入（审查会话）
  - 枚举不设取值白名单，未知状态可写入并原样读回（有存储测试覆盖）

  收口一致性（`internal/domain/step_verification.go`）：
  - 新增 `StepVerificationInconclusive`；此前 `inconclusive` 被压平成 `failed`，会把"要求
    人工复核"误读成"代码有问题"；
  - `DecideStepCloseout` 新增该分支 → `recovery_required`（不判通过，也不写成验证失败）；
  - `validation_run.go` 的 `InfrastructureError` 与预期相反的问题被修正：结论写入失败
    （`ValidationPersisted=false`）现在按基础设施异常收口（`recovery_required`），不会把
    一次未落库的"通过"写成已验证完成。

  实际改动（新增 4 个文件 + 修改 14 个文件）：
  - 新增 `internal/app/validation_review.go`（`ValidatorReviewer`、`ReviewPort`，
    复用 `ContainerService.RunAgent` 与 `SessionService`）
  - 新增 `internal/app/validation_prompt.go`（审查提示词，输出摘要复用
    `domain.ClipFeedbackText` 脱敏）
  - 新增 `internal/domain/agent_assessment_parse.go`（解析 + 共享 `ExtractJSONObject`；
    `task_workflow_plan_parse.go` 改为复用它，删除重复的括号配平扫描）
  - 新增 `internal/storage/sqlite/migrations/0021_validator_review.sql`
  - 修改 `validation.go` / `validation_run.go` / `validation_decision.go` /
    `step_verification.go` / `validation.go`(domain) / `step_workflow*.go` /
    `storage/interfaces.go` / `sqlite/validations.go` / `cli/server.go` /
    `httpapi/{validation,step_workflow,executions}.go`

  测试结果（如实区分）：
  - 通过：`go test -count=1 -p 1 ./tests/...` → 45 个包全部通过（1568 个用例）。
  - 新增领域单测 `tests/unit/domain/agent_assessment_test.go`（10 个用例）：结构化解析、
    7 类不可解释输出、审查不得覆盖必需检查失败、审查状态门槛、通过需评估、收紧、人工复核、
    未完成流程审查无效。
  - 新增应用集成 `tests/unit/app/validation_review_test.go`（12 个用例，真实 SQLite +
    真实 `StepExecutor`/`ValidationService`/`StepWorkflowService`/`ValidatorReviewer`，
    只把容器内 Agent 调用与命令执行替换为替身）：确定性通过+审查通过、确定性失败+审查声称
    通过仍失败（且不发起审查调用）、审查未通过收紧为失败、人工复核不通过、输出非法不可解析、
    调用失败、超时、评估持久化失败不报成功、验证角色不可用、任务未配置验证角色、服务未装配
    审查能力、历史只追加（两轮独立记录且第一轮结论未被覆盖）、审查会话不复用 Developer 会话、
    提示词携带确定性证据。
  - 新增存储测试 2 个：审查状态/说明/会话/评估一起落库并可读回；未知审查状态原样读回。
  - 未执行：`tests/integration/remote/*` 的真实远程 Validator 调用——本里程碑的完成定义
    允许在测试边界内用受控替身覆盖（计划/审查/容器入口都在生产代码路径上，只替换了进程
    调用本身）。真实远程全链路验收属于 P1-M5。
  - 无跳过用例。

  检查：`gofmt -l internal cmd tests` 无输出；`go vet ./...` 无输出。

  未解决问题（不阻塞 M2 完成定义）：
  - 审查提示词与真实模型行为尚未磨合：结构化协议在受控替身下验证完备，真实模型遵循度需
    在 P1-M5 的真实远程路径上观察。
  - `validation_feedbacks` 目前只收录失败检查，不收录审查发现（`findings`）。M2 未扩大
    该结构：审查发现通过 `assessment_json` 完整保留，M3 设计修复提示词时再决定是否需要
    把 findings 纳入反馈结构。
  - `verification_results` 表与 `domain.NewVerificationResult` 仍是历史遗留的双判定模型，
    生产链路不再使用。本轮按"不扩大范围"的要求未删除，记录为 P2 待清理事项。

---

### P1-M3 实现有上限、可停止的验证失败修复与重新验证闭环

- **ID**：`P1-M3`
- **名称**：实现有上限、可停止的验证失败修复与重新验证闭环
- **状态**：`completed`
- **必要性**：验证失败后只停止为 `blocked` 意味着"任务自己不会向交付前进"，需要人工反复
  干预。受控的修复闭环是 P1 端到端交付的核心：让失败能转化为下一次修复的输入，同时用
  上限与停止条件防止无限循环和额度浪费。
- **当前证据**：
  - 已实现：`validation_feedbacks` 持久化与下次执行注入提示词
    （`internal/app/step_workflow_run.go` `persistFeedback`、`internal/app/step_workflow_prompt.go`）。
  - 已实现：重试策略类型（`internal/domain/retry.go`：默认上限 3，配置上限 10）与
    `Step.MaxAttempts`、`StepStatusRetryWait` 状态（`internal/domain/step.go`）。
  - 缺口：无自动"修复→重新验证"的编排入口；`RetryPolicy.CanRetry` / `NextAttemptNumber`
    只有类型与纯函数，未被编排器消费；验证失败后工作流直接停止为 `blocked`。
- **前置依赖**：`P1-M2`（需要结构化、可解释的验证结论作为修复输入）。
- **目标结果**：验证失败时，编排器按步骤重试策略进入修复（新 Attempt + 新 Session + 注入
  上一轮失败反馈），修复后重新验证；达到上限或满足停止条件时收敛到明确终态，不无限循环。
- **基本工作项**（按逻辑顺序）：
  1. 定义修复循环的领域规则（纯函数）：何时可重试、达到上限后收敛为何种终态、哪些失败
     （基础设施异常、结果不确定）不得自动重试。
  2. 在步骤级闭环（或任务级编排器的步骤循环）中接入"失败 → 修复 → 重验"的循环，复用
     `ExecuteAndValidate`，每次新建 Attempt 与 Session。
  3. 每次修复前注入上一轮 `validation_feedbacks`（已有提示词构造能力，确认被循环复用）。
  4. 实现停止条件：达到 `MaxAttempts`、连续失败、用户取消、上下文取消、结果不确定等，
     分别映射到 `failed` / `cancelled` / `recovery_required` 等终态。
  5. 保持历史记录：每轮 Attempt / Session / Validation Run 都保留，不覆盖。
  6. 补充单元测试覆盖重试资格、上限、停止条件与终态映射。
- **涉及范围**：`internal/domain/retry.go`、`internal/domain/step.go`、
  `internal/app/step_workflow_run.go`、`internal/app/task_workflow_exec.go`、
  `internal/app/step_workflow_prompt.go`、`tests/unit/app/step_workflow_test.go`、
  `tests/unit/app/task_workflow_test.go`。
- **状态与数据约束**：每轮修复都是真实执行，必须新 Attempt + 新 Session；重试次数判定
  只能由领域规则给出；达到上限或基础设施异常不得伪装成"验证通过"或"代码失败"；任何一轮
  的持久化失败都阻止继续推进并记录。
- **测试与验收**：
  - 成功路径：第一轮失败 → 注入反馈修复 → 第二轮验证通过 → 步骤 `completed`。
  - 失败路径：达到 `MaxAttempts` 后收敛为 `failed`/相应终态，不继续调用。
  - 边界：基础设施异常不自动重试；取消/超时立即停止；每次重试都有新 Session 且历史保留。
- **完成定义**：存在可执行、有上限、可停止的修复循环；达到上限或停止条件后有明确终态；
  每轮记录可追溯；相关单元测试通过且无绕过状态机的实现。
- **非目标**：不实现跨任务重试调度、退避策略（如需要可记录为后续事项）、不把修复循环
  做成通用重试框架。
- **风险与待决问题**：修复循环是否会消耗大量模型额度，需明确默认上限与用户可配置边界；
  停止条件的语义（"连续失败"还是"累计失败"）需在领域规则中确认。
- **完成记录**（2026-10-24）：

  **真实断点（改动前的代码证据）**：
  - `ExecuteAndValidate` 一次性执行"开发 + 验证 + 收口"就返回，验证失败即收口为 `failed`；
    工作流随后停止为 `blocked`（`stopForStepResult` 的 `verification_failed` 分支）。
  - `domain.RetryPolicy.CanRetry` / `NextAttemptNumber` 与 `Step.MaxAttempts` 已持久化
    （`steps.max_attempts`、`steps.retry_json`），但**没有任何生产调用方**——领域规则与
    编排器之间缺少连接点。
  - 失败反馈已持久化并按步骤注入首轮提示词（`persistFeedback` / `stepWorkflowPrompt`），
    但读取失败被静默降级为"没有反馈"，无法支撑"必须拿到可信修复依据才重放调用"。
  - `StepStatus.retry_wait` 已在状态机中，但编排器从未使用：失败后不需要中间状态即可回到
    `running`。

  **领域规则（`internal/domain/retry.go`、`internal/domain/step_verification.go`）**：
  - 新增 `RepairFailureClass`（14 个停止/结束类别，可 `Validate`）、`RepairDecision` 与
    纯函数 `DecideRepairLoop`：按固定顺序判定取消 → 开发结论 → 是否发起验证 → 是否通过 →
    是否可靠落库/基础设施异常 → 结论不确定/取消 → 收口未持久化 → 反馈不可用 → 步骤类型 →
    修复开关 → 尝试上限。
  - 新增 `DecideRepairCloseout`：把停止类别映射为步骤终态（通过 → `completed`；取消 →
    `cancelled`；记录不可确认/结论不确定/步骤不适用/开发中断 → `recovery_required`；
    达到上限与其他真实失败 → `failed`）。`RepairCloseoutDecision` 同时记录生效上限。
  - 尝试次数口径：**`Step.MaxAttempts` 表示本次闭环调用允许的 Developer 执行总次数（含首轮）**。
    缺省 `domain.DefaultMaxAttempts = 3`，硬上限 `MaxTaskMaxAttempts = 10`，由
    `RetryConfig.Limit()` 在计划生成时写入 `steps.max_attempts`；计数只累加本轮真实创建并
    落库的 Attempt。用户显式发起的下一次执行是新决定，从新预算开始计数。

  **生产调用链（`internal/app/step_workflow_repair.go`）**：
  - `app.StepWorkflowService.ExecuteAndValidate` 改为驱动 `runRepairLoop`：每轮调用既有的
    `StepExecutor.Execute(DeferCompletion)` + `ValidationService.Validate` + 既有单轮收口，
    每轮新建 Attempt / Session / Validation Run，历史一律保留。
  - 首轮依据由 `initialFeedback` 读取（读取失败或归属不符直接拒绝执行，不再静默降级）；
    后续轮次由 `repairPrompt` 按上一轮 `ValidationRunID` 精确读取反馈并校验 Task/Step 归属。
  - 循环每轮消费 `domain.DecideRepairLoop`；停止时由 `closeRepairLoop` 依据
    `domain.DecideRepairCloseout` 写入终态（带状态前置条件，释放执行占用），并把需要人工核验
    的结论同步推进任务到 `recovery_required`。
  - `internal/app/task_workflow_exec.go` 的 `stopForStepResult` 新增
    `result.Repair.Class != ""` 分支，由 `repairStopMapping` 把领域类别映射为工作流状态与
    错误分类（`attempts_exhausted` / `verification_inconclusive` /
    `verification_unconfirmed` / `step_not_repairable` 等），不在编排层重新推导规则。
  - `internal/domain/event.go` 新增 `EventTypeRepairRound`（`repair.round`）：每轮记录轮次、
    上限、Attempt / Session / Validation Run、继续或停止原因；写入失败只记日志，不改业务状态。
  - `ExecuteAndValidateResult` 新增 `Round` / `Attempts` / `Repair` / `RepairCloseout`；
    HTTP 响应新增 `round` / `attempts` / `max_attempts` / `repair_decision` / `repair_class`
    / `repair_summary`。
  - 未新增任何状态迁移：`failed` / `recovery_required` / `retry_wait` → `running` 的现有迁移
    已足够支撑循环，`retry_wait` 未被使用（循环在同一调用内推进，不需要中间状态）。

  **测试结果（如实区分）**：
  - 通过：`go test -count=1 -p 1 ./tests/...` → 45 个包、1639 个用例全部通过。
  - 新增 `tests/unit/app/step_workflow_repair_test.go`（14 个用例，真实 SQLite + 真实
    `StepExecutor`/`ValidationService`/`StepWorkflowService`，只替换容器内 Agent 调用与
    命令执行）：两轮修复后通过（新 Attempt/Session/Validation Run、提示词引用上一轮反馈）、
    达到上限立即停止（不再产生 Attempt、检查与 Agent 调用）、显式上限 3、未开启修复即停止、
    结论不确定不修复、基础设施异常不修复、反馈写入失败不重放调用、开发失败/超时不修复、
    规划步骤不修复、轮次之间取消不再调用 Agent、每轮事件与历史可追溯、达到上限后可显式重新执行。
  - 新增 `tests/unit/domain/repair_loop_test.go`（44 个用例，含子用例）：默认上限与显式上限、
    上限边界（1/2/3/4、零值走默认）、14 类停止类别、通过优先、取消优先、未知类别拒绝、
    终态映射全表、上限摘要、`AttemptsLimitOr` 回退、`RetryConfig` 修复开关语义。
  - 新增 `tests/unit/app/task_workflow_repair_test.go`（11 个用例）：编排器消费修复停止类别，
    逐类断言工作流状态与错误分类，且停止后不推进下一步骤、不执行代码保存、不记入已完成进度。
  - 无跳过用例；`tests/integration/remote/*` 的真实远程修复循环未执行（该路径需要真实
    SSH + Docker + 模型，属于 P1-M5 端到端验收范围）。
  - 检查：`gofmt -l internal cmd tests` 无输出；`go vet ./...` 无输出。

  **未解决问题（不阻塞 M3 完成定义）**：
  - 尝试上限按"单次闭环调用"计数：用户显式重新执行会重新获得完整预算，因此理论上可以靠
    人工反复重试无限消耗额度。累计上限属于任务级策略，留待 P2 评估。
  - `validation_feedbacks` 仍不收录 Validator 的 `findings`。本轮经调查确认**不是自动修复
    的必需信息**：进入修复的前提是必需确定性检查全部失败（`DecideValidation` 在确定性检查
    未通过时不会调用审查，`reviewSemantics` 也因此不发起调用），此时不存在可用 `findings`；
    唯一带 `findings` 的失败形态是"审查判定未通过 → 结论 `failed`"，其结构化原因已通过
    `validation_runs.review_summary` / `assessment_json` 持久化，但当前修复提示词只引用
    `validation_feedbacks` 的检查条目。是否把审查发现并入反馈结构留待真实需求出现时决定。
  - 契约变更：`StepWorkflowStore` 新增 `GetValidationFailureFeedback`，`ExecuteAndValidate`
    的语义从"单轮"变为"有上限的循环"；外部调用方（HTTP/CLI/编排器）无需改动，但若有人
    依赖"单次调用只产生一次 Attempt"，需要按新语义调整预期。
  - 未修改 `internal/httpapi` 之外的接口层：CLI 仍通过 `POST execute-workflow` 触发，
    `agentflow task run` 的展示未增加修复轮次信息。

---

### P1-M4 将修复闭环与代码保存、会话归档、任务收尾串联

- **ID**：`P1-M4`
- **名称**：将修复闭环与代码保存、会话归档、任务收尾串联
- **状态**：`completed`
- **必要性**：修复闭环只是中间环节，最终要落到"代码被安全保存、会话被归档、任务被正确
  收尾"。现有代码保存与收尾能力已存在，但尚未与修复闭环一起形成完整路径；这一步把它
  们接成一条从需求到交付的链。
- **当前证据（核实后）**：
  - 已实现：代码保存（`internal/app/code_save.go`）、保存结果恢复核验（
    `internal/app/code_save_verify.go`、`internal/domain/code_save_recovery.go`）。
  - 已实现：会话归档核验（`internal/app/task_workflow_exec.go` `sessionsPersisted`）。
  - 已实现：任务收尾（`internal/domain/workflow_run.go` `DecideWorkflowCloseout`）。
  - 已实现：修复闭环（M3）已在生产调用链内，修复通过后步骤进入 `completed`
    （`internal/app/step_workflow_repair.go`），编排器随即推进游标。
  - **核实前文档中的"缺口：修复闭环（M3）尚未接入"已失效**：M3 在 commit `b7f7e22`
    完成接入，此处属于旧描述。核实中发现的真实断点见下方"完成记录"。
- **前置依赖**：`P1-M3`。
- **目标结果**：一条任务在修复闭环成功后，能继续完成代码保存、会话归档核验与任务收尾；
  任一步骤的持久化失败或结果不确定都按 `DecideWorkflowCloseout` 收敛为正确状态，不把
  "步骤全通过"当成任务成功。
- **基本工作项**（按逻辑顺序）：
  1. 确认任务级编排器在步骤全部通过后进入代码保存、会话归档核验、收尾的路径完整
     （`task_workflow_exec.go` 的 `finalize` / `saveCode` / `sessionsPersisted`）。
  2. 确认修复闭环收敛到"步骤 `completed`"后能自然进入收尾，不需要额外状态。
  3. 核验代码保存 `unconfirmed` / `indeterminate` 时任务进入 `recovery_required` 且不判
     成功；归档缺失时同样进入 `recovery_required`。
  4. 补一条本地集成测试（受控替身）覆盖"开发失败修复后成功 → 代码保存 → 归档 → 收尾"。
  5. 记录真实远程路径下该链路的验证结果（或明确标记跳过/未执行）。
- **涉及范围**：`internal/app/task_workflow_exec.go`、`internal/domain/workflow_run.go`、
  `internal/app/code_save*.go`、`tests/unit/app/task_workflow_test.go`、
  `tests/integration/local/workflow/`。
- **状态与数据约束**：任务 `completed` 必须同时满足步骤全通过 + 代码保存成功 + 会话归档
  成功；代码保存结果不确定/记录未确认时不得判成功；失败现场默认保留。
- **测试与验收**：
  - 成功路径：修复成功 → 步骤完成 → 代码保存 → 归档 → 任务 `completed`。
  - 失败路径：代码保存失败/不确定、归档缺失分别进入 `blocked` / `recovery_required`，
    不判成功。
  - 边界：`CodeSaver` 缺失时任务停留 `blocked`（`code_saver_unavailable`）。
- **完成定义**：存在端到端证据证明"修复成功后能走到正确的任务收尾"；`DecideWorkflowCloseout`
  的必要完成条件在真实路径中被执行；无绕过收尾规则直接写 `completed` 的代码。
- **非目标**：不重构代码保存/收尾逻辑；不新增合并策略；不把收尾做成独立调度。
- **风险与待决问题**：代码保存与任务收尾涉及真实 Git 远程副作用，测试需注意不污染真实
  远程仓库（使用随机后缀、隔离目录）。
- **完成记录**（2026-10-24）：

  **核实起点**：分支 `master`，HEAD `b7f7e22`（`:zap: 实现有上限、可停止的验证失败修复与
  重新验证闭环`），工作区干净。

  **路线图冲突（已修正）**：`P1-single-task-e2e.md` 的 M4 章节仍写"M3 尚未接入，因此
  '失败修复后成功'的完整路径没有端到端证据"。事实是该描述已过时：M3 的 `runRepairLoop`
  与 `repairStopMapping` 已经在生产调用链内。已按代码证据改写"当前证据"。

  **核实中发现的真实生产断点（两条）**：

  1. **Developer 执行从不写入 Session 记录**（真实断点）。
     `internal/app/step_executor_run.go` 的 `callAgent` 只调用 `SessionService.Load(sessionID)`，
     而 `ContainerService.RunAgent` 从不写数据库——全仓库 `CreateSession` 的唯一生产调用方
     是 `SessionService.Start`，它只被 Planner（`task_workflow_plan.go`）与 Validator
     （`validation_review.go`）使用。后果链条：
     `attempt.SessionIDs` 恒为空 → `finalize` 里 `sessionsPersisted` 对 Developer 会话
     完全空转 → 只要任务里存在任何非 Developer 会话（Planner），归档核验就永远只覆盖
     Planner，Developer 会话完全没被核验过；若任务无 Planner 会话则直接判"无会话"。
     内部测试未暴露该缺陷，因为三个测试替身（`step_executor_fixture_test.go`、
     `step_workflow_test.go`、`validation_review_test.go`）都在 `RunAgent` 里自行
     `CreateSession`，把"谁负责持久化会话"的职责错误地放进了替身。

  2. **收尾终态写入失败会留下 `running` 残留**（真实断点）。
     `applyCloseout` 先用 `run.Complete()` 把内存副本推进到终态，再 `persistRun`；写入失败时
     调用 `stopRun(recovery_required)`，但 `WorkflowRun` 状态机不允许 `completed → recovery_required`，
     `stopWithEvent` 退化为 `run.Fail()` 同样被拒绝，最终**静默返回内存对象**：
     数据库里的记录永久停在 `running`，接口看起来"已停止"，重启后也没有租约收敛路径。

  **实际改动**：

  - `internal/app/step_executor_run.go`：`callAgent` 改为
    `SessionService.Start`（先写库）→ 用该 SessionID 调用 `RunAgent` → `SessionService.Finish`
    （写入产出、导出与归档状态），与 Planner / Validator 完全同构。执行端口若返回不同的
    SessionID 只记录告警不覆盖。会话登记失败时不启动进程（避免一次没有任何记录的真实执行）。
  - `internal/app/task_workflow_exec.go`：新增 `stopUnpersistedCloseout`。收尾终态写入失败时
    重新读取数据库真实记录，在其之上写 `recovery_required` / `closeout_failed`，不写
    "工作流已完成"事件；连重新读取都失败时保留原始现场并记录 Error 日志。

  **新增测试**：`tests/integration/local/workflow/delivery_chain_test.go`（13 个用例）。
  装配真实 `TaskWorkflowService` + `StepExecutor` + `ValidationService` +
  `StepWorkflowService` + `SessionService` 与真实 SQLite、真实迁移；只把容器内 Pi 调用、
  容器内命令执行、容器核验与外部 Git 保存替换为受控替身。

  覆盖场景：
  - A 修复后成功交付：首轮检查失败 → 修复重验通过 → 第 2 步通过 → 代码保存 → 全部会话
    已归档 → `completed`；并断言 Attempt 递增且不覆盖、每次尝试 1 条 Session 且
    `ArchiveStatus=succeeded`、每次尝试 1 条 Validation Run 且归属正确、`attempt.SessionIDs`
    全部指向真实会话、修复轮次事件 ≥2、代码保存只调用 1 次、事件链完整。
  - B 修复达到上限：`attempts_exhausted` → `blocked`，不再产生第 3 次 Developer/检查调用，
    不执行代码保存，任务不 completed，2 次尝试/2 条会话/2 条验证记录均可查询。
  - C 代码保存：明确失败（`code_save_failed`/`blocked`）、推送结果未知
    （`code_save_indeterminate`/`recovery_required`）、记录未确认
    （`code_save_unconfirmed`/`recovery_required`）、能力缺失
    （`code_saver_unavailable`/`blocked`）。
  - D 会话归档：归档失败、归档位置缺失、完整性 partial、完整性 missing、会话查询失败
    （均 `session_not_persisted`/`recovery_required`），以及收尾记录写入失败
    （`closeout_failed`/`recovery_required`，数据库不得出现 `completed`）。
  - E 状态推进边界：上述用例同时断言"步骤已通过验证但保存失败时任务不 completed"。

  **为使替身不再掩盖断点而调整的既有测试辅助代码**（`tests/unit/app/`）：
  `step_executor_fixture_test.go`、`step_workflow_test.go`、`validation_review_test.go` 的
  容器替身删除 `RunAgent` 内自行 `CreateSession` 的逻辑，以及随之失去引用的
  `fakeStepSession` / `reviewSessionOf` 两个辅助函数。会话改由产品代码的
  `StepExecutor.callAgent`（Developer）与 `ValidatorReviewer.Review`（Validator）登记。

  **测试结果（如实区分）**：
  - 通过：`go test -count=1 ./tests/...` → 45 个包、1652 个用例全部通过。
  - 通过：`go test -count=1 -p 1 ./tests/...` → 同上（串行执行，排除并发偶发掩盖）。
  - 通过：`go test -count=1 -run TestDeliveryChain ./tests/integration/local/workflow/` → 13 个用例。
  - 跳过：`tests/integration/remote/*` 的真实远程路径未执行（需要真实 SSH + Docker + 模型，
    属 P1-M5 端到端验收范围）。因此**真实远程交付链路本轮无法确认**。
  - 检查：`gofmt -l internal cmd tests` 无输出；`go vet ./...` 无输出；`go build ./...` 通过。
  - 测试产物：`TEST_ARTIFACT_DIR=C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-2198399842`。

  **未解决问题（不阻塞 M4 完成定义）**：
  - 远程路径未验证：本轮的端到端证据全部来自受控替身边界内的本地集成测试，
    真实 SSH + Docker + 容器内 Pi 的同类场景仍需 P1-M5 覆盖。
  - `sessionsPersisted` 仍要求任务下**全部** Session 都满足"完整性可用 + 归档成功 + 归档位置非空"。
    本次核实确认这是有意为之的保守口径（执行失败会话同样必须留下可读归档），
    但它意味着 Validator 审查失败会让收尾停在 `recovery_required`。该语义已在用例
    `TestDeliveryChainStopsWhenSessionArchiveIncomplete` 中被固化。
  - `tests/unit/app/task_workflow_test.go` 的 `taskWorkflowStore.sessions` 仍以"整体替换会话
    查询结果"的方式注入，无法表达"部分会话归档成功、部分失败"。若后续需要覆盖
    "开发已归档但 Validator 未归档"的混合现场，需要把该注入点改为按会话逐条控制。

---

### P1-M5 完成单任务端到端验收（成功/验证失败/修复失败/无法确认）

- **ID**：`P1-M5`
- **名称**：完成单任务端到端验收（成功/验证失败/修复失败/无法确认）
- **状态**：`pending`
- **必要性**：前四个里程碑各自成立后，需要一条真实路径证明它们**连起来**是对的，并覆盖
  关键终局，否则无法宣称 P1 退出条件满足。
- **当前证据**：
  - 已有大量单元/本地集成测试（`tests/unit/app/task_workflow_test.go` 等），但无真实
    端到端全路径验收；`tests/integration/remote/workflow/workflow_test.go` 目前只有前置
    条件测试（`TestRemoteWorkflowPreconditions`、`TestRemoteWorkflowRequiresHostKeyVerification`）。
- **前置依赖**：`P1-M4`。
- **目标结果**：至少一条真实单任务路径从需求走到代码交付，并覆盖成功、验证失败、
  修复失败、结果无法确认四种关键路径，且每条路径的结论与持久化记录都真实可解释。
- **基本工作项**（按逻辑顺序）：
  1. 设计端到端验收场景与数据准备（真实远程环境，或按测试规范在受控替身边界内执行）。
  2. 覆盖成功路径：需求 → 计划 → 开发 → 确定性验证 → Validator 评估 → 代码保存 → 归档 →
     任务 `completed`。
  3. 覆盖验证失败路径：验证失败 → 修复 → 重验通过（或达到上限停止）。
  4. 覆盖修复失败路径：达到上限后收敛为明确终态，历史 Attempt/Session/Validation 保留。
  5. 覆盖无法确认结果路径：代码保存/验证结果不确定 → `recovery_required`，不判成功。
  6. 记录每条路径的通过/失败/跳过/未执行，并列出测试产物路径。
- **涉及范围**：`tests/integration/remote/workflow/`、`tests/integration/local/workflow/`、
  `tests/config/`（SSH/Docker/Pi 配置）。
- **状态与数据约束**：验收结论只能来自真实执行结果，不能伪造；跳过必须说明原因；无法
  确认的路径不得被报告为通过。
- **测试与验收**：上述四条路径均有测试证据，成功路径确实到达 `completed` 且满足
  `DecideWorkflowCloseout` 的全部必要条件；失败/不确定路径结论正确且记录保留。
- **完成定义**：P1 阶段退出条件的第 1–7 条全部满足，且有可复核的测试产物与结论记录。
- **非目标**：不覆盖多任务并发、不覆盖系统性的重启恢复矩阵（P2）、不做性能压测。
- **风险与待决问题**：真实远程端到端依赖外部环境与模型可用性，可能需要与 CI 区分
  "跳过（未配置）"与"失败（配置完整）"。
- **完成记录**：（待完成时填写实际改动、测试结果、未解决问题与证据）
