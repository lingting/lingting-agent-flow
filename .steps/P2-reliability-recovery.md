# P2 —— 流程可靠性与恢复

> 撰写依据：`master` HEAD `7a55dfe` 的真实代码与测试状态；P2-M1 已按实际核实更新。
> 完整恢复边界矩阵见 [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)，
> 任务收尾一致性协议见 [`P2-M5-closeout-protocol.md`](./P2-M5-closeout-protocol.md)，
> 本文档只保留阶段目标、里程碑边界与移交结论。

## 阶段目标

让已经打通的业务流程在超时、取消、进程重启、持久化失败及外部操作结果不确定等情况下，
仍然能够正确停止或恢复。阶段结束时，用户实际能完成：

- 任务在任意阶段中断后，能依据持久化事实确定停在哪里、下一步如何继续。
- 超时、取消、服务停止导致的三种中断能被区分，并各自收敛到正确状态。
- 修复次数上限、停止条件、重试资格、结果不确定时的处理规则明确且一致。
- commit / push / merge 等外部副作用的恢复路径先核验、后处理，不盲目重放。
- 关键恢复场景有集成测试与恢复验收证据。

## 必要性

P1 只保证"主路径能跑通"，但真实运行必然遇到超时、取消、进程退出、SQLite 写入失败、
远程 Git 结果不确定等问题。若这些场景会把任务卡死在 `running` / `pending` / `cancelling`，
或把"结果不确定"伪装成成功/失败，主流程就不可信。P2 把恢复能力系统化，使闭环在异常下
仍然正确。

注意：**P1 不允许以牺牲基础正确性为代价**。P1 已经具备的最基本持久化、错误处理与失败
停止能力（如 `execution_not_persisted` 不得报告成功、失败默认保留现场）不得被推迟到 P2；
P2 主要补齐更系统的恢复语义与恢复验收。

## 前置条件

- P1 退出（单任务端到端闭环成立）。
- 已有能力：启动恢复（`internal/app/task_workflow_run.go` 的收敛顺序、
  `internal/app/task_launch_recovery.go`）、代码保存恢复核验
  （`internal/domain/code_save_recovery.go`、`internal/cli/code.go` 的 `code verify`）、
  租约机制（`WorkflowRun` 的 `WorkerID` / `LeaseExpiresAt`）。

## 当前状态（摘要，基于代码证据）

> P2-M1 调查已完成，完整清单见 [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)。
> 本节只保留结论摘要；任何与清单冲突的表述以清单为准。

**已完成**：

- 工作流运行记录的启动恢复（`running` 租约过期→`blocked`/`worker_interrupted`；
  `cancel_requested`→`cancelled`；`pending` 超窗→`blocked`/`not_started`）。
- 计划现场恢复（`planning` 有步骤→`planned`，无步骤→`recovery_required`）。
- 取消现场恢复（`cancelling` 有未结束执行→`recovery_required`，否则→`cancelled`）。
- 工作区初始化残留恢复（`pending`/`running`→`uncertain`，不重放代码获取）。
- 步骤执行占用残留恢复（`running`/`developing`/`verifying`→`recovery_required`）。
- 执行节点预检残留恢复（`checking` 重新入队，失败只记日志不阻止启动）。
- 代码保存三阶段（提交/推送/合并）的基线、`persist_status`、`recovery_status` 与只读核验
  （迁移 `0019_code_save_recovery.sql`、`domain.DecideCodeSaveRecovery`）。
- 超时/取消/服务关闭在单次 Agent 调用层面的区分（`isServiceStop`、
  `classifyStepExecutionRun`）。
- **P2-M2 新增**：四类中断 × 六层状态的收敛语义成文并落地
  （[`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)）。
- **P2-M2 新增**：步骤占用收敛改为按"有效执行权"判定，解除与 `blocked`
  执行记录的互相锁死（B1）。
- **P2-M2 新增**：取消意图在任务层与执行记录层双写，并由启动恢复跨层补齐（B5）。
- **P2-M2 新增**：`workflow_runs` 租约周期续期；续租失败即停止推进且不写终态（B6）。
- **P2-M2 新增**：`attempts` / `sessions` / `validation_runs` 残留的启动收敛
  （`app.ExecutionResidueService`，B2/B3）。
- **P2-M2 新增**：验证引擎的取消 / 超时 / 服务停止独立用例与持久化断言。
- **P2-M4 新增**：代码保存三阶段的恢复收敛闭环——不可信标记落盘、HEAD 归因依靠第一父、
  已确认阶段状态条件写回、多阶段核验顺序收敛、恢复入口前置门控、启动扫描可发现未决记录
  （`app.CodeSaveRecoveryService`）。

**已确认缺失（详见清单 §3.4、§5）**：

- ~~Attempt 级 `code_fetches`、`container.state=unknown` 两类残留仍**没有收敛路径**
  （B4 与 G14）。~~ 仍未收敛，转入 P2-M5 范围。
- ~~代码保存恢复核验（`code verify`）**只有 CLI 手工入口**，不属于自动恢复流程（M4）。~~
  P2-M4 已交付恢复入口的前置门控与启动扫描可发现性；"核验并继续"的自动编排入口仍未实现，
  属 P2-M5 范围。
- 收尾阶段 `tasks.status=completed` 与 `workflow_runs.status=recovery_required` 可以同时成立，
  没有"任务结论存疑"的表达（M5）。

**待补**：

- ~~修复次数上限、重试资格与结果不确定处理规则在 P1-M3 引入修复循环后，需要重新审视
  其与工作流恢复的衔接，避免两套规则冲突（M3）。~~ 已在 P2-M3 完成：预算落到
  `repair_cycles` + `attempts.budget_scope*`，准入统一由 `domain.DecideRepairStart`
  判定，编排与恢复都引用同一规则。
- 关键恢复场景的测试当前分散在 `tests/integration/local/workflow/recovery_test.go`
  与 `tests/integration/local/codesaver/`；清单 §6.2 已列出 G1–G14 共 14 个未覆盖窗口。

## 阶段范围

**做**：

- 梳理所有阶段的状态持久化与恢复边界（M1）。
- 明确超时、取消、中断、进程重启后的恢复语义（M2）。
- 明确修复次数上限、停止条件、重试资格与结果不确定处理规则（M3）。
- 核验提交、推送、合并等外部副作用的恢复路径（M4）。
- 补齐关键恢复场景的集成测试与恢复验收（M5）。

**不做**：

- 不建设多任务调度（P3）。
- 不做产品体验与 UI 优化（P4）。
- 不重复实现 P1 已经具备的基础持久化与失败停止能力，只做系统性补齐。

## 里程碑清单与依赖

| ID | 名称 | 状态 | 依赖 |
|---|---|---|---|
| P2-M1 | 梳理所有阶段的状态持久化和恢复边界 | `completed` | P1 退出 |
| P2-M2 | 明确超时、取消、中断、进程重启后的恢复语义 | `completed` | P2-M1 |
| P2-M3 | 明确修复次数上限、停止条件、重试资格和结果不确定处理规则 | `completed` | P2-M1 |
| P2-M4 | 核验提交、推送、合并等外部副作用的恢复路径 | `completed` | P2-M2 |
| P2-M5 | 补齐关键恢复场景的集成测试与恢复验收 | `completed` | P2-M2、P2-M3、P2-M4 |

## 阶段退出条件

1. 每个阶段（容器准备、工作区初始化、计划生成、逐步执行、验证、代码保存、收尾）都有
   明确的持久化事实与恢复边界，且成文。
2. 超时、用户取消、服务停止导致的中断有统一且可区分的语义，并各自收敛到正确状态。
3. 修复循环的重试资格、上限、停止条件与"结果不确定"处理规则在领域层定义且无二义。
4. 提交/推送/合并的恢复遵循"先核验、后处理、不可确认即 `unverifiable`"，不盲目重放。
5. 关键恢复场景有集成测试证据，且通过/失败/跳过/未执行如实记录。

## 风险与边界

- **重复副作用**：恢复若直接重放 commit/push/merge，可能产生重复提交或覆盖他人提交；
  必须复用 `DecideCodeSaveRecovery` 的只读核验语义。
- **卡死状态**：任何异常路径都不得让 `workflow_runs` / `attempts` / `sessions` 永久停留
  在 `running` / `pending` / `cancelling`。
- **服务停止 vs 用户取消混淆**：恢复必须区分二者，否则服务重启会把正常取消误判为中断。
- **无效投入**：不预先为"尚未出现的异常"设计通用恢复框架，只针对 P1 已打通的真实阶段。

## 下一步选择规则

按 P1 完成后的真实缺口排序：先补"恢复边界清单"（M1，解除后续依赖），再明确语义（M2/M3），
再核验外部副作用（M4），最后做恢复验收（M5）。

---

## 里程碑详情（骨架）

> 以下里程碑在 P1 完成后、具备真实恢复缺口证据时补充工作项细节。当前只给出边界，避免
> 在缺少实现证据时编造细节。

### P2-M1 梳理所有阶段的状态持久化和恢复边界

- **ID**：`P2-M1`
- **状态**：`completed`（2026-10-23，基于 `master` HEAD `7a55dfe` 核实）
- **必要性**：没有统一清单，恢复工作会碎片化，容易遗漏某个阶段的 `running` 残留。
- **目标结果**：成文的"阶段 × 持久化事实 × 恢复动作 × 收敛状态"清单。
- **交付物**：[`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)（14 个阶段的完整
  矩阵 + 恢复责任分级 + 启动恢复真实调用链 + 测试覆盖矩阵 + 阻塞性缺陷清单）。
- **完成记录**：
  - 覆盖 14 个阶段：任务创建与启动前置检查、容器创建/关联/可用性核验、工作区初始化与
    代码获取、Planner 与计划持久化、WorkflowRun 创建与启动、Developer 执行与
    Attempt/Session、确定性验证与 Validator、修复与重验、commit、push、merge、会话归档、
    Task/WorkflowRun 收尾、取消/超时/服务停止/进程重启。
  - 核实服务启动恢复的**真实调用顺序**：`HostValidationService.Start` →
    `StepExecutor.Start`（无恢复）→ `ValidationService.Start`（无恢复）→
    `StepWorkflowService.Start`（无恢复）→ `TaskWorkflowService.Start`（3 类收敛）→
    `TaskLaunchService.Start`（2 类收敛）。核实中发现 `blocked` 执行记录与步骤占用残留
    互相锁死（B1），以及 `recoverUnfinished` 先于 `RecoverStepOccupancy` 的顺序副作用。
  - 区分了自动恢复（8 项）、用户触发恢复（7 项）、只能安全停止（7 项）、缺少恢复能力
    （10 项）。
  - 记录 6 个阻塞/高风险缺陷（B1–B6）与 1 个语义不一致（B7），并给出建议修复边界；
    本里程碑**未修改任何生产代码**。
  - 列出 G1–G14 共 14 个未覆盖故障窗口，并按"解除最多后续依赖"给出优先级。
  - 修正了本文件与 `ROADMAP.md` 中与代码事实不符的 4 处表述（见清单 §8）。
- **完成定义**：每个阶段都有明确恢复边界，且与代码事实一致 —— 已满足。
- **非目标**：未新增恢复代码，未重构服务装配，未提前实现 M2/M3/M4。
- **遗留问题（移交 M2）**：B1–B7 均需要生产代码改动，已在清单 §5 与 §7 标注依赖关系；
  `TestPlanGenerationInterruptedByShutdown` 的既有竞态需在 M5 前修复。

### P2-M2 明确超时、取消、中断、进程重启后的恢复语义

- **ID**：`P2-M2`
- **状态**：`completed`（2026-10-24，基于 `master` HEAD `2a1bb0d` 实施与核实）
- **必要性**：四种中断若语义混淆，会误判用户意图或覆盖真实结果。
- **交付物**：[`P2-M2-interruption-semantics.md`](./P2-M2-interruption-semantics.md)
  （四类中断的判定依据 + 六层状态 × 四类中断的完整收敛矩阵 + 跨层一致性规则）。
- **前置依赖**：P2-M1（已完成）。
- **M1 移交问题的处理结果**：
  1. **B1（已修复）**：步骤占用收敛的判据从"存在未结束执行记录"改为
     "存在**仍持有有效执行权**的执行记录"。`domain.DecideStepOccupancyRecovery`
     改为接收执行记录本身与当前时刻，由 `workflowRunOwnsExecution` 判定
     （`running` 且租约有效）。`blocked` / `recovery_required` / 过期租约的
     `running` 都不再阻止其残留步骤被收敛。跨进程保护未放宽：部分唯一索引与
     版本号条件更新保持原样。
  2. **B5（已修复）**：`CancelWorkflow` 在写执行记录之后立即写任务层取消意图
     （`recordTaskCancelIntent`），把崩溃窗口压到最小；启动恢复新增
     `recoverCancellationIntents`，依据 `domain.DecideCancellationIntentRecovery`
     把"执行记录已取消、任务层未同步"的现场补齐到 `cancelled` 或
     `recovery_required`。取消意图因此可以跨进程重启保留。
  3. **B6（已修复）**：新增 `domain.WorkflowRun.RenewLease` 与
     `storage.WorkflowRunLeaseStore.RenewWorkflowRunLease`（只改
     `lease_expires_at`，不推进 `revision`），`TaskWorkflowService` 在推进期间
     按租约的 1/3 周期续租。续租失败视为失去执行权：取消本次推进，且**不写任何
     终态**（避免用过期执行者的本地对象覆盖真实记录）。
  4. **六层语义（已完成）**：矩阵已成文并落地到领域函数
     （`DecideStepOccupancyRecovery`、`DecideCancellationRecovery`、
     `DecideCancellationIntentRecovery`、`DecideValidation`、`DecideStepCloseout`）。
- **B2/B3 的处理（本次范围内）**：新增 `app.ExecutionResidueService`，在服务启动时
  收敛残留的 `attempts`（→ `interrupted`）、`sessions`（→ `interrupted` +
  `run_status = failed`，产物路径与归档事实原样保留）、`validation_runs`
  （→ `failed` + `verdict = failed`，`error_detail` 说明中断来源）。判据是
  "任务是否仍有持有有效租约的执行者"，只有确定没有执行者时才收敛。
- **验证引擎中断语义（已补齐）**：新增取消 / 超时 / 服务停止的独立用例，断言流程
  状态、结论与持久化记录，并区分"执行中断导致无法验证"与"确定性检查失败"。
- **已修复的测试竞态**：`TestPlanGenerationInterruptedByShutdown` 的失败源于替身
  在 `Stop` 传播到在途调用之前就返回结果，结论取决于 goroutine 调度顺序。修复方式
  是给替身增加 `waitForCancel`（等到本次调用的上下文真的被取消后才返回），建立确定
  的先后关系；`-race` 下连续 15 次全部通过。
- **顺带修复的真实生产缺陷**：`GetWorkflowStatus` 原先先读任务状态、后读执行记录，
  一次正在完成的收尾会被读成"任务仍在 `saving`、执行记录已 `completed`"这种不可能
  同时成立的组合（`TestWorkflowAPILifecycle` 间歇失败）。读取顺序改为
  "执行记录 → 任务 → 步骤"。
- **完成定义**：四种中断在文档与代码中一致，且有测试区分 —— 已满足。
- **不在本次范围（移交 P2-M3，已在 M3 处理）**：跨进程持久化的修复轮次计数（→
  `repair_cycles` + `attempts.budget_scope*`）、统一的 retry eligibility 框架（→
  `domain.DecideRepairStart`）、自动重放中断的 Attempt（→ 明确禁止，需人工核验）、
  `validation_feedbacks` 写入失败补偿（→ 由 `RepairClassFeedbackUnavailable` 停止）。

### P2-M3 明确修复次数上限、停止条件、重试资格和结果不确定处理规则

- **ID**：`P2-M3`
- **状态**：`completed`（2026-10-24，基于 `master` HEAD `2a1bb0d` 实施）
- **必要性**：修复循环（P1-M3）引入后，重试与恢复规则必须统一，否则会出现两套判断。
- **前置依赖**：P2-M1（已完成）。
- **M1 移交问题的处理结果**：
  1. **残留 `running` 的收敛（B2/B3）**：已由 P2-M2 的 `app.ExecutionResidueService`
     完成（`attempts` → `interrupted`、`sessions` → `interrupted` + `run_status=failed`、
     `validation_runs` → `failed` + `verdict=failed`）。P2-M3 补齐了**收敛之后的
     重新执行资格**：收敛成功不等于自动获得重试资格，仍需依据持久化历史、验证结果、
     反馈与预算作出判定。
  2. **跨进程预算**：`runRepairLoop` 的 `attempts := 0` 不再承担预算判定。预算改由
     `domain.DecideRepairBudget` 依据 `repair_cycles` 与 `attempts.budget_scope*` 计算，
     进程重启后重新读取历史得到相同结论。
  3. **反馈写入失败**：`persistFeedback` 返回 `false` 时由 `domain.DecideRepairLoop`
     判定为 `RepairClassFeedbackUnavailable` 并停止，不再"只记日志后继续"。首轮修复
     依据改为按当前周期内最近一次验证精确读取，旧周期反馈不会被误用。
- **正式定义（本次成文）**：
  - **Attempt**：一次真实的开发执行尝试，必须有独立、持久化的记录。
  - **修复轮次**：一次开发执行与其对应验证、必要反馈及收口组成的业务周期。
  - **MaxAttempts**：一个明确执行预算内允许的开发尝试总数，包含首轮。
  - **执行周期**：由 `repair_cycles` 登记的一次逻辑开发闭环；进程重启不能自动创建新的
    预算。
  - **预算作用域**：`attempts.budget_scope` / `budget_scope_id`。同一逻辑周期的恢复
    （进程重启、HTTP 重试、重复调用、workflow resume）复用同一个标识；只有用户显式
    入口才写入新标识。
- **预算计算规则（`domain.DecideRepairBudget`）**：
  - 有活动周期时按活动周期统计，否则按最近一个已结束周期统计（否则"用尽后开启新周期"
    永远无法成立）；
  - 只有 `BudgetScopeID` 等于该周期的 Attempt 计入；其他周期不消耗本周期预算；
  - 所有状态都消耗预算（含 `running` / `interrupted` / `unknown` / `recovery_required`），
    唯一例外是 `cancelled`：用户取消不应挤占后续修复预算；
  - 上限取周期开启时的快照（`repair_cycles.max_attempts`），事后修改步骤配置不会让已
    开启的周期获得更多预算；
  - 读取失败一律返回错误，结论是"不能判定是否可重试"，而不是默认还有余额。
- **统一准入规则（`domain.DecideRepairStart`）**，顺序固定：
  1. 步骤仍被一次执行占用 → `execution_active`；
  2. 存在 `unknown` / `recovery_required` / `interrupted` 尝试 → `cycle_unresolved`；
  3. 上一周期已结束且未显式要求新周期 → `cycle_closed`；
  4. 有尝试引用了未登记的周期 → `cycle_unresolved`；
  5. 显式要求新周期：没有任何历史 → `new_cycle_denied`；上一周期仍有余额且策略仍允许
     修复 → `new_cycle_denied`；否则允许并 `OpenCycle = true`；
  6. 未要求新周期：当前周期仍有剩余预算 → 允许；否则 `attempts_exhausted`。
- **正式调用方**（无第二套判定）：
  - `app.StepWorkflowService.ExecuteAndValidate`（`step_workflow_repair.go`）在启动
    Agent 之前调用它；
  - `app.StepWorkflowService.runRepairLoop` 每轮结束后重新读取预算并交给
    `domain.DecideRepairLoop`；
  - `app.StepWorkflowService.OpenRepairCycle`（`step_workflow_cycle.go`）判定新周期资格；
  - `app.TaskWorkflowService.drive` 消费 `ExecuteAndValidateResult.Repair` 的停止类别，
    不重新推导。
- **不确定结果的处理**：`interrupted` / `unknown` / `recovery_required` 尝试一律阻止
  自动重放与开启新周期；`running` 由步骤占用（`CurrentAttemptID` + 租约）表达，第二个
  执行者会被占用准入拒绝。恢复服务只收敛残留状态，不授予重试资格。
- **新增迁移**：`0022_repair_cycles.sql`（`repair_cycles` 表 + `attempts.budget_scope` /
  `budget_scope_id` + 索引）。旧历史尝试的两列为 `NULL`，表示周期归属无法确定：迁移
  不回填、不改写历史；这类记录在全部历史都无归属时按单一隐式周期统计，在混有已登记
  周期时判定为 `cycle_unresolved` 并要求人工核验。
- **新增接口**：`POST /api/v1/tasks/{taskID}/steps/{stepID}/open-repair-cycle`（CLI
  `agentflow task reopen-step`）。只登记预算作用域，不执行任何 Agent 调用；只有上一周期
  已安全收敛且预算已用尽或策略不再允许修复时才成功。
- **测试证据**：
  - `tests/unit/domain/repair_cycle_test.go`：预算边界、各状态是否消耗预算、
    `DecideRepairStart` 的全部停止类别、新周期准入、上限快照、字段校验（表驱动）。
  - `tests/integration/local/workflow/repair_budget_test.go`（真实 SQLite + 正式迁移 +
    真实 `app.StepWorkflowService`）：重启后预算一致、达到上限后重启不能创建新 Attempt、
    重复调用不重置预算、显式新周期获得新预算、仍有余额时拒绝新周期、不确定尝试阻止
    重放、反馈写入/读取失败停止、旧周期反馈不被冒充、并发开启新周期只有一个成功。
  - `tests/integration/local/workflow/e2e_repair_limit_test.go` 新增
    `TestE2ERepairLimitSurvivesRestart`：达到上限后重启并重新推进任务，Agent 与检查调用
    为 0，Attempt 与周期登记数量与标识全部不变。
- **完成定义**：规则在领域层定义一次，编排与恢复都引用它 —— 已满足。
- **不在本次范围**：自动重试框架（自动决定"下一次由谁重试"）、`code_fetches` 与
  `container.state=unknown` 两类残留的收敛（B4/G14，属 M4/M5）、任务结论存疑的表达。
- **真实执行的测试命令与结果（2026-10-24）**：
  - `go test -count=1 ./tests/...` → 退出码 0，1751 passed / 0 failed（45 packages）。
  - `go test -count=1 ./tests/unit/domain/ ./tests/unit/app/ ./tests/unit/httpapi/
    ./tests/unit/store/ ./tests/integration/local/workflow/
    ./tests/integration/local/database/` → 退出码 0，1132 passed。
  - `go test -count=1 -v -run "TestRepairBudget|TestOpenRepairCycle|TestRepairStops|
    TestRepairIgnores|TestE2ERepairLimit" ./tests/integration/local/workflow/` → 退出码 0，
    14 个用例全部 PASS（含 `TestE2ERepairLimitSurvivesRestart`）。
  - `gofmt -l internal cmd tests` → 无输出；`go vet ./...` → 无问题。
  - `TEST_ARTIFACT_DIR=C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-1680917061`
    （测试产物按仓库约定保留，未清理）。
  - 真实 Pi / SSH / Docker 用例未在本机执行：`tests/config/pi.yml`、`ssh.yml`、
    `docker.yml` 未启用，相关用例按约定输出 `WARN [SKIP]`；本次没有把跳过用例报告为通过。

### P2-M4 核验提交、推送、合并等外部副作用的恢复路径

- **ID**：`P2-M4`
- **状态**：`completed`（2026-10-25，基于 `master` HEAD `168fd2f` 实施）
- **必要性**：外部副作用不可重放，恢复必须先核验。
- **前置依赖**：P2-M2（已完成）。

#### 审计发现的真实故障窗口（均已修复或明确登记）

| 窗口 | 位置 | 修复前行为 | 处置 |
|---|---|---|---|
| W1 提交已成功、记录写不进数据库 | `internal/codesaver/saver.go` `persist` | 只在内存记录上标记 `unconfirmed`，**不落盘**；数据库永远停在看似可信的 `pending`，重启后恢复无法区分"没发生"与"已发生但没记下来" | 改为"重试一次 → 仍失败则单独落盘不可信标记（`flagUnconfirmed`，独立 5s 超时上下文）" |
| W2 HEAD 变化即判提交成功 | `internal/domain/code_save_recovery.go` `decideCommitRecovery` | 只要 HEAD ≠ 基线就判 `confirmed_applied`，他人提交也会被归因为本次成功 | 新增 `CodeSaveObservation.HeadParentCommit`（`git rev-list --parents -n 1`）：仅当第一父等于基线才判已发生，否则 `unverifiable` |
| W3 核验结论只写 `recovery_status` | `internal/app/code_save_verify.go` `persistCodeSaveRecovery` | 已确认发生的阶段状态与 SHA 不落盘，数据库仍 `pending`，恢复反复要求核验同一现场 | 新增 `storage.CodeSaveStore.UpdateCodeSaveRecoveryResult`（窄范围、以 `recovery_status` 为条件），配合 `domain.CodeSaveRecord.ApplyRecovery` 写回已确认阶段 |
| W4 多阶段核验无法收敛 | `internal/domain/code_save.go` `recoveryStatusMachine` | `confirmed_applied` / `confirmed_not_applied` 是终态，禁止互相迁移；"提交已确认发生 + 推送确认未发生"无法落库，`push_status` 永久 `pending` | 允许已收敛结论之间迁移（阶段归属由 `CodeSaveRecoveryDecision.Stage` 表达），并补两个阶段的顺序收敛用例 |
| W5 阶段状态不确定即等于整体不确定 | `internal/app/task_workflow_exec.go` `codeSaveIndeterminate` | 用 `RequiresManualRecovery`，导致已核验确认未发生的记录仍被判不确定，恢复无法收敛 | 改用 `domain.CodeSaveRecord.HasUnresolvedSideEffect`；`confirmed_not_applied` 视为已覆盖后续阶段（不提交就不会推送） |
| W6 恢复请求无视未决副作用 | `internal/app/task_workflow_run.go` | `ResumeWorkflow` 只核验容器与执行节点，直接重新执行代码保存，可能重放 commit / push / merge | 新增 `checkCodeSaveRecovery`：读全部记录 → `domain.DecideTaskCodeSaveResume` → 拒绝或放行；`StartWorkflow` 同样前置校验 |
| W7 核验失败伪装成"操作没发生" | `internal/app/code_save_verify.go` `VerifyCodeSave` | 节点不可达 / Docker 不可访问 / Git 查询失败直接返回错误，数据库得不到 `unverifiable` | 降级为 `ReadFailed` 观察值 → `unverifiable` 结论并落盘，原始错误进日志 |
| W8 重启后无人知晓存在未决副作用 | 无 | 进程重启后没有任何入口列出"仍需人工核验的代码保存记录" | 新增 `app.CodeSaveRecoveryService`（启动扫描，只读 + 追加 `code_save.recovery_required` 事件），注册进 `internal/lifecycle` |

#### 判据与重试资格的变化

- **`domain.DecideCodeSaveRecovery`**：`SafeToRetry` 只在"确认未发生"且记录可信时为真；记录不可信时保留 Git 事实但一律不给重试资格。
- **`domain.DecideCodeSaveResume`（新增）**：`proceed` / `verify` / `blocked` 三态。顺序为"写入未确认 → 已核验无法确认 → 确认未发生 → 仍有未决副作用 → 可以继续"，并输出稳定 `ErrorClass`（`code_save_unconfirmed` / `code_save_indeterminate`）。
- **`domain.DecideTaskCodeSaveResume`（新增）**：按任务全部记录判定，越保守的结论优先。
- **`domain.CodeSaveRecord.HasUnresolvedSideEffect`（新增）**：只回答"是否还有必须核验才能重试的阶段"，与"是否需要人工介入"分离。
- **`domain.CodeSaveRecord.ApplyRecovery`（新增）**：`confirmed_applied` 才推进阶段状态；`confirmed_not_applied` / `unverifiable` 只写恢复结论，不臆造 SHA。

#### M1 移交问题的处理结果

1. **核验入口仍然只有 CLI**：本阶段不做"核验并继续"的自动编排入口（需要满足"核验结论已落盘 + 任务容器仍可达 + 工作流可合法推进"三项，属于编排策略）。已交付的是**恢复入口的前置门控**（W6）与**可发现性**（W8），人工核验入口保持 `agentflow code verify --record <id>`（现同时输出续跑资格与四个独立事实）。
2. **`push_status=unknown` 之后的再次核验路径**：已打通——同一记录可重复核验，且第二个阶段的结论现在能落盘（W3/W4）。
3. **容器已删除时**：仍然只能得到 `unverifiable`（核验需要容器内 Git）。这是有意的：不伪造确定结论。
4. **"核验过程中再次退出"**：核验是纯只读查询，退出后记录保持原状，重新核验幂等；以 W3 的条件写入 + 冲突重读覆盖"核验期间记录被推进"（`TestRecoveryConflictKeepsNewerRecord`）。
5. **merge 恢复的真实远程证据**：仍无真实远程 Docker 用例（`docker.remote_docker_enabled` 未启用），本次用真实本机 Git 仓库（bare 远程）覆盖 merge 已发生 / 基础分支被他人推进两类判定。

#### 测试证据（真实 SQLite + 正式迁移 + 真实 Git）

- `tests/integration/local/codesaver/recovery_test.go`（11 个用例）：提交/推送/合并已发生但记录未落盘、HEAD 变化不可归因、远程被他人推进、缺少基线、并发冲突、确认未发生后重新执行、远程不可达、基础分支被他人推进、不可信标记落盘、多阶段顺序收敛。
- `tests/integration/local/workflow/code_save_recovery_test.go`（4 个用例）：恢复请求被门控拒绝（4 种未决状态）、`unverifiable` 直接拒绝、已收敛放行、启动扫描可发现并只追加事件、已结束任务不被扫描。
- `tests/integration/local/workflow/e2e_indeterminate_test.go` 新增 `TestE2EIndeterminateDoesNotResumeWithoutVerification`：端到端断言"结果不确定不会被一次恢复请求悄悄变成重放"。
- `tests/unit/domain/code_save_resume_test.go`（5 个用例）：续跑资格矩阵、按任务判定优先级、跨阶段写回、幂等。
- `tests/unit/domain/code_save_recovery_test.go`：补充"HEAD 变化但第一父不匹配 / 读不到第一父"与"记录不可信仍保留 Git 事实"。
- `tests/unit/store/code_saves_test.go`：新增 `UpdateCodeSaveRecoveryResult` 的落盘、冲突拒绝、无关字段保持。

#### 真实执行的测试命令与结果（2026-10-25）

- `go test -count=1 ./tests/...` → 退出码 0，**1800 passed / 0 failed**（45 packages）。
- `go test -count=1 ./tests/unit/domain/ ./tests/unit/app/ ./tests/unit/httpapi/
  ./tests/unit/store/ ./tests/unit/source/codesaver/ ./tests/integration/local/codesaver/
  ./tests/integration/local/workflow/ ./tests/integration/local/database/` → 退出码 0，1238 passed。
- `go test -count=1 -run "TestRecovery|TestDecideCodeSaveResume|TestDecideTaskCodeSaveResume|
  TestApplyRecovery|TestResumeWorkflowRejects|TestResumeWorkflowAccepts|
  TestCodeSaveRecoveryService|TestE2EIndeterminateDoesNotResume|TestCodeSaveRecovery"
  ./tests/integration/local/codesaver/ ./tests/integration/local/workflow/
  ./tests/unit/domain/ ./tests/unit/store/` → 退出码 0，54 passed。
- `gofmt -l internal cmd tests` → 无输出；`go vet ./...` → 无问题。
- `TEST_ARTIFACT_DIR=C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\codesaver-1801282134`
  （测试产物按仓库约定保留，未清理）。
- 未执行：真实远程 Docker / SSH / Pi 用例（`tests/config/docker.yml`、`ssh.yml`、`pi.yml`
  均未启用），按约定输出 `WARN [SKIP]`；本次没有把跳过用例报告为通过。

#### 剩余风险与移交

- **merge 的真实远程证据**仍缺（依赖 `docker.remote_enabled`），属 M5 范围。
- **`code verify` 仍为人工入口**：没有"核验并继续"的编排入口；恢复需要用户显式执行核验后再次请求恢复。这是本次刻意的范围边界（不新增恢复框架）。
- **容器已删除时只能得到 `unverifiable`**：需要人工判断，无自动出路（`container.state=unknown` 的完整恢复体系属 M5）。
- **启动扫描只覆盖未结束任务**：已结束任务的记录保持原样供查阅，不产生新事件。
- **完成定义**：三阶段恢复判据有测试证据且不盲目重放 —— 已满足。

### P2-M5 补齐关键恢复场景的集成测试与恢复验收

- **ID**：`P2-M5`
- **状态**：`completed`（2026-10-25，基于 `master` HEAD `40460d4` 实施与核实；
  真实远程 merge / 容器残留证据仍缺，见文末"剩余风险"）
- **必要性**：恢复语义必须靠真实持久化事实验证，不能只靠方法返回值。
- **当前证据**：`tests/integration/local/workflow/recovery_test.go`（11 个用例）、
  `tests/integration/local/codesaver/`（4 个恢复用例，真实 Git）、
  `tests/unit/app/task_workflow_test.go` / `task_launch_test.go` /
  `step_executor_test.go` 的恢复与迟到结果用例。
- **前置依赖**：P2-M2、P2-M3、P2-M4。
- **M1 移交的具体问题**：清单 §6.2 已列出 G1–G14 共 14 个未覆盖窗口。按优先级：
  1. **阻塞级**：G10（`attempts`/`sessions`/`validation_runs` 残留，且实现也缺失）、
     G2（外部操作已成功但记录未写入，容器孤儿场景）、G4（恢复写入失败，
     步骤占用与工作区收敛无覆盖）；
  2. **高风险**：G5（恢复执行期间再次退出）、G9（Clone / 容器创建无副作用计数器断言）、
     G12（任务层取消未收敛）、G13（租约过期误判）、G14（容器 `unknown` 收敛）；
  3. **中风险**：G1、G3、G6、G8、G11。
  先修复 `TestPlanGenerationInterruptedByShutdown` 的既有竞态（约 40% 概率失败，
  见 `ROADMAP.md`），否则"服务关闭中断"这一窗口的测试证据不可靠。
- **目标结果**：覆盖矩阵与集成测试证据，结果如实区分通过/失败/跳过/未执行。
- **完成定义**：关键恢复场景均有可复核的测试产物与结论。

#### 实施前审查发现的生产缺陷（均已修复）

审查基于 `master` HEAD `40460d4` 的真实代码，逐项核对 `internal/app`、`internal/domain`、
`internal/storage/sqlite`、`internal/server/server.go` 与 `tests/`。发现 **3 个阻塞级**与
**4 个高风险**缺陷，其中 C1、C3、H2、H4 此前未被任何文档记录。

| ID | 级别 | 缺陷 | 证据（修复前） |
|---|---|---|---|
| C1 | 阻塞 | 任务停在 `verifying` / `saving` 后**永久无法继续**：这两个状态既不在 `CheckTaskLaunchStart` 的准入集合，也不在 `TaskStatus.AllowsStepExecution`，而 `recoverUnfinished` 只收敛执行记录、不改写任务 | `internal/domain/task_launch.go`、`internal/domain/step_execution.go`、`internal/app/task_workflow_run.go` |
| C2 | 阻塞 | 收尾顺序先写任务后写执行记录，且 `completed` 是终态 → 收尾失败**不可恢复**，留下"任务成功 + 执行记录需核验" | `internal/app/task_workflow_exec.go` `applyCloseout` |
| C3 | 阻塞 | 丢失执行权的执行者仍可写 `tasks.completed`：`completeTask` 使用 `context.WithoutCancel`，写入前不再检查租约 | 同上 |
| H1 | 高 | `container.state=unknown` 是死胡同：`markUnknown` 写入后无任何调用方推进 | `internal/app/containers.go`、`internal/domain/container.go` |
| H2 | 高 | 容器不可用即无条件新建第二个容器（停止 / 未知 / 孤儿都触发） | `internal/app/task_launch_run.go`、`internal/app/execenv_task.go` |
| H3 | 高 | `code_fetches` 无活跃唯一约束、无收敛路径、无状态前置条件更新 | `migrations/0015_code_fetches.sql`、`internal/storage/sqlite/code_fetches.go` |
| H4 | 高 | `UpdateTaskWorkspaceInit` 是 `WHERE id = ?`，使恢复里的 `errors.Is(err, storage.ErrConflict)` 分支成为死代码 | `internal/storage/sqlite/task_workspace_inits.go`、`internal/app/task_workspace_recovery.go` |

另有一处分类缺陷：`internal/app/execenv.go` 的 `isContainerMissing` 把
`docker.CodeOwnershipMismatch` 也算"容器不存在"，`releaseDelete` 据此返回"按删除成功处理"。

#### M5-1 收尾一致性

- **收尾协议**（`internal/app/task_workflow_exec.go` `applyCloseout`）：写入顺序改为
  **先执行记录、后任务**，执行记录的条件写入（`revision` 前置条件）作为栅栏：
  - 栅栏失败（版本冲突 / 租约已转移 / 数据库不可用）→ **不再写任务成功终态**，
    直接收敛执行记录并返回，C3 由此消除；
  - 栅栏通过后任务写入失败 → 只可能留下 `run=completed` + `task=recovery_required`
    （"未完成"），由新增的 `markTaskRecoveryRequired` 与启动恢复补齐，绝不伪造成功。
- **`stopUnpersistedCloseout`** 语义收窄为"仅处理执行记录终态写入失败"，
  不再承担"任务已被写成 completed 的善后"这一不可能的职责。
- **启动恢复新增 `recoverStalledDelivery`**（`internal/app/task_workflow_delivery_recovery.go`）：
  读取 `tasks.status ∈ {verifying, saving}`，按 `domain.DecideTaskCloseoutRecovery`
  收敛——执行记录已完成且证据重算通过则补齐成功终态，否则写 `recovery_required`
  （`closeout_evidence_missing` / `closeout_interrupted`）。C1 由此消除。
- **只读报告 `reportCloseoutInconsistencies`**：对 `completed` 任务与最近执行记录做
  `domain.DecideTaskCloseoutView` 判定，不一致时追加 `task.recovery_required` 事件
  （`stage=closeout_reconcile`）并告警，**不改写终态**（`completed` 仍是终态）。
- **对外表达**：`domain.DecideTaskCloseoutView` 派生 `confirmed` / `pending` /
  `unconfirmed` / `inconsistent` / `cancelled`；`TaskResponse` 新增 `final_result`、
  `closeout`、`closeout_summary`，任务列表、详情、创建与取消响应统一输出。
  此前 API 根本不输出 `final_result`，调用方无法判断"结论存疑"。

#### M5-2 尝试级代码获取残留

- 迁移 `0023_code_fetch_active_unique.sql`：`code_fetches` 新增部分唯一索引
  `idx_code_fetch_active_attempt`（同一 Attempt 最多一条活动记录），与
  `task_workspace_initializations` 同构。脚本内写明升级前排重 SQL 与处置原则。
- `domain.DecideCodeFetchRecovery` / `DecideCodeFetchResume` / `DecideCodeFetchVerification`
  统一定义"何时收敛为 uncertain、何时禁止重放、何时可收敛为终态"。
- `app.CodeFetchRecoveryService`（启动扫描，注册在 `ExecutionResidue` 之后、
  `TaskWorkflow` 之前）：把无执行者的 `pending` / `running` 收敛为 `uncertain`，
  写入带状态前置条件，冲突视为他人已收敛。
- `app.CodeFetchVerifyService` + CLI `agentflow code fetch-verify --record <id>`：
  **只读**探测（目录是否存在、是否为 Git 仓库、当前分支、HEAD），事实充分时才收敛；
  绝不 Clone / checkout / reset。domain 新增 `CodeFetchRecord.MarkVerifiedSucceeded`，
  避免核验路径臆造基础分支信息。
- `CodeInitializeService` 新增两条前置门控：
  - `checkResumeEligible`：该 Attempt 存在 `uncertain` 记录时返回
    `ErrCodeInitRecoveryRequired`（412），存在活动记录时返回 `ErrCodeInitConflict`（409）；
  - `requireTaskWorkspaceReady`：尝试级与任务级共用 `/workspace/project`，因此尝试级
    必须排在任务级之后，没有成功记录时如实拒绝而不是猜测可以安全初始化。
- `liveExecutionTasks` 从 `ExecutionResidueService.liveTasks` 中抽出，供残留收敛与
  代码获取收敛共用同一份"是否仍有执行者"判定。

#### M5-3 容器 `unknown` 与孤儿容器

- `domain.ContainerObservationClass`（running / stopped / absent / ownership_mismatch /
  unreachable / incomplete）与 `domain.DecideContainerRecovery` 定义收敛规则；
  `app.ClassifyContainerObservation` 只用 `docker.ErrorCodeOf` 映射，不解析错误文本。
- `app.ContainerRecoveryService`（注册在 `HostValidation` 之后）：
  - `creating` / `unknown` → 只读观测后条件写入（running / stopped / removed）；
  - 所有权不匹配、无法访问、记录不完整 → 保持 `unknown` 并标记需要人工介入
    （**不删除**，节点离线不让启动失败，与 `HostValidationService` 口径一致）；
  - 孤儿容器（`ContainerFilter.Orphans`，实现为"登记了任务关联但不是该任务当前容器"）
    只追加 `recover_container_orphan` 事件，不删除、不重新关联；
  - 写入使用新增的 `UpdateContainerIfState`，冲突保留他人结论。
- 阻止第二个容器：`TaskLaunchService.ensureNoPendingContainer` 与
  `ExecutionEnvironmentService.ensureNoPendingContainer` 在创建新容器前检查该任务是否
  还有未处置的容器（非 `removed` / `detached`），有则返回前置条件错误并给出可执行动作。
  H2 由此消除。
- `ContainerFilter` 新增 `TaskID` / `Orphans`；CLI `docker container list` 新增
  `--task` / `--orphans`。

#### M5-4 代码保存核验后的衔接

- 修复 F2：`VerifyCodeSave` 在**容器登记记录缺失**（`storage.ErrNotFound` 之外）、
  已删除、已脱离管理时，降级为 `unverifiable` 并**落盘**（`degradeCodeSaveUnverifiable`），
  不再直接返回错误。此前该路径使记录永远停在 `pending`，恢复无出路。
- 新增 `app.ResumeEligibilityService` + HTTP `GET /api/v1/tasks/{id}/resume-eligibility`
  + CLI `agentflow task resume-status <task-id>`：只读回答"能否继续 / 为什么不能 /
  下一步做什么"，`next_actions` 直接给出 `agentflow code verify --record <id>`；
  判定复用 `domain.DecideTaskCodeSaveResume` 与既有容器前置校验，不新增第二套规则。
- 新增 HTTP `POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save`：
  与 CLI `code verify` 完全同语义（只读、幂等），五个事实分别输出
  （record / observation / decision / persisted / updated）。
- 不做自动核验、不自动 resume、不重放 commit / push / merge。

#### 数据库与装配变更

- 迁移 `0023_code_fetch_active_unique.sql`（schema 版本 22 → 23）。
- `storage`：`CodeFetchStore` 新增 `UpdateCodeFetchRecordIfStatus` /
  `ListInterruptedCodeFetchRecords`；`TaskWorkspaceInitStore` 新增
  `UpdateTaskWorkspaceInitIfStatus`；`ContainerStore` 新增 `UpdateContainerIfState`。
- 服务注册顺序（`internal/server/server.go`）：
  `HostValidation → ContainerRecovery → StepExecutor → Validation → StepWorkflow →
  ExecutionResidue → CodeFetchRecovery → TaskWorkflow → TaskLaunch → CodeSaveRecovery`；
  停止顺序自动反向。

#### 测试证据

新增用例（全部使用真实 SQLite、正式迁移与生产入口）：

- `tests/unit/domain/code_fetch_recovery_test.go`（8 个用例）：收敛目标、终态不改写、
  有执行者不抢占、续跑资格三态与优先级、只读核验的收敛边界（表驱动）。
- `tests/unit/domain/task_closeout_test.go`（5 个用例）：收尾闭环派生组合（8 种）、
  停滞收敛（5 种，含"仍有执行权不抢占"）、非收尾状态不处理、不一致说明可读。
- `tests/unit/container/container_recovery_test.go`（5 个用例）：已确认观测的三种收敛、
  高风险观测必须人工、终态不触碰、非法观测值/非法目标被拒绝。
- `tests/unit/store/recovery_conditional_test.go`（6 个用例）：三类条件更新的冲突拒绝与
  冲突不改库、中断查询只返回活动记录、`0023` 唯一约束生效、按任务/孤儿筛选。
- `tests/integration/local/workflow/closeout_consistency_test.go`（6 个用例）：
  写入顺序为"执行记录 → 任务"、任务写入失败不伪造成功、执行记录写入失败不写任务成功、
  重启收敛 `saving` 停滞、证据齐备补齐成功终态、遗留不一致只报告不改写。
- `tests/integration/local/workflow/code_save_resume_test.go`（6 个用例）：
  `unverifiable` 阻止推进、续跑资格给出记录标识、已收敛放行、容器不可运行分类、
  容器缺失降级为 `unverifiable` 并落盘、记录不存在仍返回参数错误。
- `tests/integration/local/execenv/container_recovery_test.go`（9 个用例）：
  unknown→running / absent→removed / 不可达保留 unknown / 所有权不匹配不处置 /
  detached 不被触碰 / 重复收敛幂等且只写一条事件 / 孤儿只报告不删除 /
  写入失败阻止启动 / 并发冲突保留新结论。
- `tests/integration/local/fetcher/code_fetch_recovery_test.go`（9 个用例）：
  pending / running 收敛为 uncertain / 有租约不抢占 / 重复收敛幂等 /
  写入失败阻止启动 / 并发保留成功结论 / 不确定现场阻止重新初始化 /
  只读核验的四个收敛边界（并断言不执行 clone/checkout/reset/rm/clean）。

同时按新协议修正了既有用例的等待条件（收尾"先执行记录、后任务"是合法中间状态）：
`delivery_chain_test.go` 的 `settle`、`e2e_fixture_test.go` 的 `settle`、
`tests/unit/app/task_workflow_test.go` 的 `waitTaskRunStatus`、
`tests/unit/httpapi/workflow_test.go` 的 `waitSettled`。

#### 真实执行的测试命令与结果（2026-10-25）

- `gofmt -l internal cmd tests` → 无输出；`go vet ./...` → 无问题；`go build ./...` → 通过。
- `go test -count=1 ./tests/...` → 退出码 0，**1885 passed / 0 failed / 0 skipped**
  （45 packages）。本机 `tests/config/{ssh,docker,pi}.yml` 早已启用，因此该命令包含真实
  SSH / Docker / Pi 用例且全部通过。
- `go test -count=1 -race ./tests/integration/local/workflow/
  ./tests/integration/local/fetcher/ ./tests/integration/local/execenv/` → 退出码 0，
  170 passed。
- `go test -count=1 -race ./tests/unit/domain/ ./tests/unit/container/
  ./tests/unit/store/ ./tests/unit/app/ ./tests/unit/httpapi/` → 退出码 0，1131 passed。
- 测试产物保留在 `os.TempDir()/lingting-agent-flow-tests/` 下的随机目录，
  例如 `C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-3751921440`；
  `testkit.Main` 同时在基目录写入 `latest-run.txt`。

#### 剩余风险与移交

- **真实远程 Docker 的容器残留证据仍缺**：容器 `unknown` 收敛、孤儿发现、
  `merge` 恢复的判定都只在本地受控端口上验证过。真实远程用例需
  `docker.remote_enabled` + `ssh.enable`，且必须有构造出 `unknown` / 孤儿现场的时机，
  当前测试没有这样的注入点。**不得把本地用例当作真实远程验收。**
- **遗留 `completed` + `recovery_required` 无法自动修复**：`completed` 是终态，
  本轮只做发现与报告（事件 + 告警 + 派生 `inconsistent`）。若要自动修复，必须先决策
  "允许 completed 回退"并审计全部读取方（`IsTerminal` / `IsFinished` / `IsActive` /
  `CanStart` / `AllowsStepExecution`、任务 API、前端状态映射、删除判定）。
- **`0023` 迁移在已有重复活动 `code_fetches` 的库上会失败**：脚本内已给出排查 SQL 与
  处置原则（不得为通过迁移删除历史）。
- **孤儿容器是否允许自动补关联**：本轮决定**不自动**（只报告），因为其工作区状态未知。
- **`HostValidationService.recoverPending` 仍只记日志**，与工作流恢复"失败即阻止启动"
  口径不同；本轮未扩大范围，保留该记录。
- **完成定义**：收尾一致性、代码获取残留、容器 unknown / 孤儿、核验后续跑四条主线均有
  代码、真实调用链与集成测试证据；P2 退出条件第 5 条由不成立转为成立。
  **P2 达到退出条件**，可进入 P3（多任务调度）。
