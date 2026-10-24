# P2 —— 流程可靠性与恢复

> 撰写依据：`master` HEAD `7a55dfe` 的真实代码与测试状态；P2-M1 已按实际核实更新。
> 完整恢复边界矩阵见 [`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md)，
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

**已确认缺失（详见清单 §3.4、§5）**：

- Attempt 级 `code_fetches`、`container.state=unknown` 两类残留仍**没有收敛路径**
  （B4 与 G14，属于 P2-M4/M5 范围）。
- 代码保存恢复核验（`code verify`）**只有 CLI 手工入口**，不属于自动恢复流程（M4）。
- 收尾阶段 `tasks.status=completed` 与 `workflow_runs.status=recovery_required` 可以同时成立，
  没有"任务结论存疑"的表达（M3/M4）。

**待补**：

- 修复次数上限、重试资格与结果不确定处理规则在 P1-M3 引入修复循环后，需要重新审视
  其与工作流恢复的衔接，避免两套规则冲突（M3）。
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
| P2-M3 | 明确修复次数上限、停止条件、重试资格和结果不确定处理规则 | `pending` | P2-M1 |
| P2-M4 | 核验提交、推送、合并等外部副作用的恢复路径 | `pending` | P2-M2 |
| P2-M5 | 补齐关键恢复场景的集成测试与恢复验收 | `pending` | P2-M2、P2-M3、P2-M4 |

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
- **不在本次范围（移交 P2-M3）**：跨进程持久化的修复轮次计数、统一的 retry
  eligibility 框架、自动重放中断的 Attempt、`validation_feedbacks` 写入失败补偿。

### P2-M3 明确修复次数上限、停止条件、重试资格和结果不确定处理规则

- **ID**：`P2-M3`
- **状态**：`pending`
- **必要性**：修复循环（P1-M3）引入后，重试与恢复规则必须统一，否则会出现两套判断。
- **当前证据**：`domain.RetryPolicy`（默认 3、上限 10）、`StepStatusRetryWait` 已存在；
  `domain.DecideRepairLoop` / `DecideRepairCloseout` 已定义修复循环停止规则。
- **前置依赖**：P2-M1（已完成）。
- **M1 移交的具体问题**：
  1. `validation_runs` / `attempts` / `sessions` 的残留 `running` 没有收敛规则（清单 B2、B3），
     "能否重试"无从判定；
  2. 修复循环的 `attempts` 是进程内计数，跨进程中断后"已消耗轮次"丢失，与持久化的
     `MaxAttempts` 语义未打通（清单 §2.8）；
  3. `validation_feedbacks` 写入失败只记日志，下一次执行会静默失去修复依据，
     而 `RepairClassFeedbackUnavailable` 只覆盖"读取失败"（清单 §2.8）。
- **目标结果**：领域层单一规则定义重试资格/上限/停止/不确定处理。
- **完成定义**：规则在领域层定义一次，编排与恢复都引用它。

### P2-M4 核验提交、推送、合并等外部副作用的恢复路径

- **ID**：`P2-M4`
- **状态**：`pending`
- **必要性**：外部副作用不可重放，恢复必须先核验。
- **当前证据**：`DecideCodeSaveRecovery`、`code verify`、`persist_status`/`recovery_status`
  已实现；commit/push/merge 三阶段的基线与核验判据在
  `tests/integration/local/codesaver/codesaver_test.go` 与
  `tests/unit/domain/code_save_recovery_test.go` 已有真实证据。
- **前置依赖**：P2-M2。
- **M1 移交的具体问题**：
  1. `VerifyCodeSave` **只有 CLI 手工入口**（`agentflow code verify`），不属于自动恢复流程；
     收尾进入 `recovery_required` 后没有"核验并继续"的编排入口（清单 §2.9、§3.2）；
  2. `push_status=unknown` / `merge=unverifiable` 之后没有"再次核验"的自动路径
     （清单 §2.10、§2.11）；
  3. 容器已删除时核验只能得到 `unverifiable`，缺少可操作出路（清单 §2.9）；
  4. 没有"核验过程中再次退出"的用例（清单 G5）；
  5. `merge_after_push` 默认关闭，merge 恢复**没有真实远程证据**（清单 §2.11）。
- **目标结果**：提交/推送/合并的恢复路径全部"先核验、后处理"，不可确认即 `unverifiable`。
- **完成定义**：三阶段恢复判据有测试证据，且不盲目重放。

### P2-M5 补齐关键恢复场景的集成测试与恢复验收

- **ID**：`P2-M5`
- **状态**：`pending`
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
