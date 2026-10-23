# P1 —— 单任务完整开发闭环

> 状态与证据撰写依据：commit `330e7c4`（2026-10-23）工作区的实际代码与测试状态。

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

1. **Validator Agent 语义审查没有任何生产调用方**：`domain.DecideValidation` 已经定义了
   `assessment` 参数与"只能收紧、不能放宽"的决策规则，但 `internal/app/validation.go:157`
   明确"不调用 Validator Agent"。确定性检查只能证明"命令通过"，不能替代对需求是否被真正
   满足的语义判断；没有它，验证决策链是残缺的。
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

### 部分完成（已有基础，缺端到端集成）

| 能力 | 已有部分 | 缺口 |
|---|---|---|
| Validator 语义审查 | `domain.StepTypeReview`、`domain.RoleTypeValidator`、`domain.AgentAssessment`、`DecideValidation` 的 `assessment` 分支、`verifications.assessment_json` 列 | 无生产调用方；`Review` 步骤无执行路径；评估结果无写入路径 |
| 验证失败修复依据 | `validation_feedbacks` 持久化 + 下次执行注入提示词 | 无自动"修复→重验"循环 |

### 未实现

- 自动验证失败修复与重新验证闭环（含修复次数上限、停止条件、终态）。
- 真实端到端全路径验收测试。

### 待调查

- `Review`（Validator）步骤应由 Planner 生成、还是由编排器在每个 `develop` 步骤验证后
  动态追加：当前 `domain.BuildPlan` 只产出 `plan` + `develop`，无既定事实。
- Validator 辅助评估的持久化字段与 `validation_runs` / `verifications` 的关系：列已存在，
  但尚无生产写入约定，需在实施 M2 时确认。

## 阶段范围

**做**：

- 打通并核验"任务启动 → 计划生成"前置流程（M1）。
- 把 Validator Agent 接入验证决策链，明确其与确定性验证的职责边界（M2）。
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
| P1-M1 | 核实并打通任务启动至计划生成的前置流程 | `pending` | 无 |
| P1-M2 | 打通 Developer、确定性验证与 Validator Agent 的完整验证决策链 | `pending` | P1-M1 |
| P1-M3 | 实现有上限、可停止的验证失败修复与重新验证闭环 | `pending` | P1-M2 |
| P1-M4 | 将修复闭环与代码保存、会话归档、任务收尾串联 | `pending` | P1-M3 |
| P1-M5 | 完成单任务端到端验收（成功/验证失败/修复失败/无法确认） | `pending` | P1-M4 |

> 状态当前均为 `pending`：M1/M2/M4 的底层能力大多已存在，但尚未按"端到端闭环"的退出
> 条件完成核验与串联，故不标 `completed`。每个里程碑的"当前证据"里会说明哪些部分已就绪。

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

1. 先完成 **P1-M1**：它核实主链路最前端是否真的可用，是后续所有工作的前置，且已有
   代码证据表明大部分已实现，成本最低。
2. 再完成 **P1-M2**：Validator 是 P1 决策链的唯一结构性缺口，M3 修复循环依赖"验证能给出
   可修复的结构化结论"。
3. 再完成 **P1-M3**：修复闭环依赖 M2 的验证结论。
4. 再完成 **P1-M4**：串联收尾依赖 M3。
5. 最后 **P1-M5**：验收依赖前四项全部成立。

---

## 里程碑详情

### P1-M1 核实并打通任务启动至计划生成的前置流程

- **ID**：`P1-M1`
- **名称**：核实并打通任务启动至计划生成的前置流程
- **状态**：`pending`
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
  - 缺口：尚无**真实远程环境**下"创建任务 → `task start` → 得到已持久化计划"的端到端
    验收证据；`tests/integration/remote/workflow/` 目前只有前置条件测试
    （`TestRemoteWorkflowPreconditions` 等），未覆盖真实启动到计划生成。
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
- **完成记录**：（待完成时填写实际改动、测试结果、未解决问题与证据）

---

### P1-M2 打通 Developer、确定性验证与 Validator Agent 的完整验证决策链

- **ID**：`P1-M2`
- **名称**：打通 Developer、确定性验证与 Validator Agent 的完整验证决策链
- **状态**：`pending`
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
- **完成记录**：（待完成时填写实际改动、测试结果、未解决问题与证据）

---

### P1-M3 实现有上限、可停止的验证失败修复与重新验证闭环

- **ID**：`P1-M3`
- **名称**：实现有上限、可停止的验证失败修复与重新验证闭环
- **状态**：`pending`
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
- **完成记录**：（待完成时填写实际改动、测试结果、未解决问题与证据）

---

### P1-M4 将修复闭环与代码保存、会话归档、任务收尾串联

- **ID**：`P1-M4`
- **名称**：将修复闭环与代码保存、会话归档、任务收尾串联
- **状态**：`pending`
- **必要性**：修复闭环只是中间环节，最终要落到"代码被安全保存、会话被归档、任务被正确
  收尾"。现有代码保存与收尾能力已存在，但尚未与修复闭环一起形成完整路径；这一步把它
  们接成一条从需求到交付的链。
- **当前证据**：
  - 已实现：代码保存（`internal/app/code_save.go`）、保存结果恢复核验（
    `internal/app/code_save_verify.go`、`internal/domain/code_save_recovery.go`）。
  - 已实现：会话归档核验（`internal/app/task_workflow_exec.go` `sessionsPersisted`）。
  - 已实现：任务收尾（`internal/domain/workflow_run.go` `DecideWorkflowCloseout`）。
  - 缺口：修复闭环（M3）尚未接入，因此"失败修复后成功"的完整路径（修复→重验→代码保存→
    归档→收尾）没有端到端证据。
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
- **完成记录**：（待完成时填写实际改动、测试结果、未解决问题与证据）

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
