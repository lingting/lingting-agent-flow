# P2-M2 —— 四类中断 × 六层状态的收敛语义

> 撰写依据：`master` HEAD `2a1bb0d`（"核实代码"）的真实代码与测试状态，逐项核对
> `internal/app/`、`internal/domain/`、`internal/storage/sqlite/` 与 `tests/` 得出。
> 本文档只描述**本次实施后**的语义与**实施前**的代码事实差异，不描述尚未实现的设想。
>
> 上游依据：[`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md) 的
> B1/B2/B3/B5/B6 与 §2.14、§4.1。

## 0. 阅读约定

- **中断**：一次执行没有按协议正常结束。四类中断互相独立，不能互相推导。
- **收敛**：把一条持久化记录依据**已确认的证据**推进到一个合法且可解释的状态。
- **不确定性**：无法从持久化事实判断外部操作是否真的发生过。此时状态必须
  保留不确定性，不得判成功，也不得判"确定失败"，更不得盲目重放。
- **执行权（ownership）**：`workflow_runs.worker_id` + `lease_expires_at` 共同表达
  "当前哪个进程有权推进这条执行记录"。它是跨进程并发保护的唯一依据。
- **未结束（unfinished）**：`workflow_runs.status` 不属于
  `{completed, cancelled, failed}`。它**不等于**"有执行者正在推进"。

> **"中断语义已定义" ≠ "自动重试资格已定义"。** 本文只定义"中断后各层收敛到
> 什么状态、保留哪些证据、允许哪些后续动作"。是否允许自动重跑、重跑上限与
> 修复轮次计数属于 P2-M3，本文不做实现也不做承诺。
> （P2-M3 已完成：重试资格与预算现由 `domain.DecideRepairStart` /
> `domain.DecideRepairBudget` 统一定义，见 §4 末尾的"M3 之后需注意的事实变化"。）

---

## 1. 四类中断的定义与判定依据

判定依据只能是持久化事实与只读外部观测，不能是"进程内还记得什么"。

| 类别 | 触发来源 | 判定依据（按优先级） | 不等于 |
|---|---|---|---|
| **用户取消** | `Task.CancelRequested` / `WorkflowRun.CancelRequested` / `POST .../cancel` / `CancelWorkflow` | 持久化的 `cancel_requested = 1` + `cancel_requested_at` | 不等于"进程已停止"；不等于"外部副作用没发生" |
| **单次调用超时** | `context.WithTimeoutCause(..., DeadlineExceeded)`（Agent 调用、单条检查、整次验证） | `attempts.status = timed_out` / `validation_runs.status = timed_out` / `agent.RunTimedOut` | 不等于"服务关闭"；不等于"外部操作没发生" |
| **服务正常关闭** | `lifecycle.Manager` 逆序 `Stop`，取消原因为 `ErrStepExecutorStopped` / `ErrWorkflowStopped` / `ErrValidationEngineStopped` | `attempts.status = interrupted` / `validation_runs.status = failed` + `error_detail` 含服务停止；`context.Cause` 命中上述三类错误 | 不等于"用户取消"；不等于"可以标记为成功" |
| **进程崩溃 / 执行者异常退出** | 无进程内入口（`kill -9`、断电、容器被回收） | 只有持久化事实 + **已过期租约**（`lease_expires_at <= now`）或明确的"从未开始"（`pending` 且超过租约窗口） | 不等于"执行失败"；不等于"执行成功" |

**关键区分点**：

1. **取消 vs 服务关闭**：两者都表现为 `context.Canceled`，但 `context.Cause`
   不同。`isServiceStop` 识别 `ErrStepExecutorStopped` / `ErrWorkflowStopped`，
   `stopStateOf` 识别 `ErrValidationEngineStopped`。误判会把服务关闭写成用户取消，
   从而掩盖"现场未确认"的事实。
2. **超时 vs 取消**：`DeadlineExceeded` 与 `Canceled` 必须分开判定
   （`classifyMissingResult`、`stopStateOf`）。
3. **进程崩溃 vs 仍在运行**：唯一可靠判据是**租约**。单看 `status = running`
   或单看"启动时间已超过某个固定时长"都不能证明进程已退出（见 §5 续租）。

---

## 2. 六层状态的中断收敛矩阵

层与层之间**不共用同一套枚举**：Task 表达业务阶段，WorkflowRun 表达"这一次编排
是否在推进"，Step 表达逻辑阶段的结论，Attempt 表达一次执行尝试，Session 表达
一次 Agent 调用，ValidationRun 表达一次验证流程。跨层映射关系在每格中写明。

### 2.1 Task（`tasks.status` / `final_result` / `cancel_requested`）

| 中断类别 | 目标状态 | 原因与诊断 | 能否确认业务完成 | 证据不足时 | 允许的后续操作 |
|---|---|---|---|---|---|
| 用户取消 | `cancelling` → `cancelled`（`final_result = cancelled`）；`queued` 可直接 `cancelled` | `failure_summary` 写入"取消请求已记录"或恢复摘要 | 否 | 存在未结束执行记录时停在 `recovery_required`，不写 `cancelled` | 重复取消幂等；不可再 `StartWorkflow` / `Launch` |
| 单次超时 | `recovery_required`（`finishTask` 的 `TimedOut`/`Failed` 分支） | `failure_summary` = 失败摘要 | 否（外部副作用未知） | 保持 `recovery_required`，等待显式核验 | 显式重新执行步骤 / `Launch` / `ResumeWorkflow` |
| 服务关闭 | `recovery_required`（`Interrupted` 分支，`requireTaskRecovery`） | `failure_summary` = 中断摘要 | 否 | 同上 | 同上；**不得**写成 `cancelled` |
| 进程崩溃 | `recovery_required`（计划生成现场无步骤时）或保持原状等待收敛 | 启动恢复摘要 | 否 | 见 §3 B1：步骤占用收敛后由用户显式恢复 | `Launch` / `ResumeWorkflow` / 步骤重新执行 |

**Task 层约束**：

- `completed` 只能由 `TaskWorkflowService.applyCloseout` 在
  `domain.DecideWorkflowCloseout` 判定完成后写入；任何中断路径都不得写 `completed`。
- `cancelled` 是终态，写入前必须有 `cancel_requested = 1`（`convergeCancellingTask`
  显式校验）。
- 单个步骤的成功/失败不决定 Task 终态。

### 2.2 WorkflowRun（`workflow_runs.status` / `stage` / 游标 / `worker_id` + 租约 / `revision`）

| 中断类别 | 目标状态 | 原因与诊断 | 能否确认业务完成 | 证据不足时 | 允许的后续操作 |
|---|---|---|---|---|---|
| 用户取消 | `cancel_requested` → `cancelled` | `error_summary` = 取消摘要；`error_class` 清空 | 否 | 状态机拒绝 `cancel_requested → cancelled` 时收敛为 `recovery_required` / `cancel_not_applicable` | 幂等重复取消；不可 `ResumeWorkflow`（`CancelRequested` 拒绝） |
| 单次超时 | `recovery_required` / `developer_interrupted` | `error_class`、`error_summary` | 否 | 同上 | 显式 `ResumeWorkflow`（要求 `CanStart()`、无取消请求、`plan_digest` 一致） |
| 服务关闭 | `recovery_required` / `developer_interrupted` | 同上 | 否 | 同上 | 同上 |
| 进程崩溃 | `blocked` / `worker_interrupted`（租约过期后）；`blocked` / `not_started`（`pending` 超窗） | `error_class` + `error_summary`；`stage` 保留中断发生的位置 | 否 | 租约仍有效时**不动** | 显式 `ResumeWorkflow` |

**WorkflowRun 层约束**：

- 终态只有 `completed` / `cancelled` / `failed`。`blocked` 与 `recovery_required`
  是"已停止但未终结"，需要用户介入。
- **执行权**：`Start()` 写 `worker_id` + `lease_expires_at`；`Stop()` / `Fail()` /
  `Cancel()` / `Complete()` 清空两者。续租只能延长 `lease_expires_at`，不得改变
  `status`、不得让终态记录重新获得租约。
- **`blocked` 与"仍有执行者"是两件事**（B1 的根因）：`blocked` 记录已经被执行者
  主动停止，它的 `worker_id` 与租约都已清空，因此**不能**被当作"仍有执行在推进"。

### 2.3 Step（`steps.status` / `current_attempt_id` / `failure_summary`）

| 中断类别 | 目标状态 | 原因与诊断 | 能否确认业务完成 | 证据不足时 | 允许的后续操作 |
|---|---|---|---|---|---|
| 用户取消 | `cancelled`（`stepStatusOfOutcome` / `DecideStepCloseout`） | `failure_summary` = 取消摘要；释放 `current_attempt_id` | 否 | 步骤停在 `verifying` 且无有效执行者时由启动恢复收敛为 `recovery_required` | 不可自动重跑 |
| 单次超时 | `failed`（`stepStatusOfOutcome` 默认分支；`DecideStepCloseout` 的 `Interrupted` 除外） | `failure_stage = execute`、`failure_summary` | 否 | 同上 | `CanStartExecution()` 允许显式重新执行 |
| 服务关闭 | `recovery_required`（`DecideStepCloseout` 的 `Interrupted` 分支） | 同上 | 否 | 同上 | 人工核验后显式重新执行 |
| 进程崩溃 | `recovery_required`（启动恢复 `DecideStepOccupancyRecovery`） | `StepOccupancyRecoverySummary()` | 否 | 有**有效执行权**的记录存在时不抢占 | 人工核验后显式重新执行 |

**Step 层约束**：

- `running` / `developing` / `verifying` 表示"被占用"，不是终态。它们本身就会
  阻止再次执行（`NextStepToExecute`、`CheckStepExecutionStart`）。
- 收敛为 `recovery_required` 时必须释放 `current_attempt_id`，否则用户显式重新
  执行会被"已被执行尝试占用"挡住。
- 任何中断路径都不得把步骤写成 `completed`。

### 2.4 Attempt（`attempts.status` / `failure_stage` / `error_class` / Git 快照）

| 中断类别 | 目标状态 | 原因与诊断 | 能否确认业务完成 | 证据不足时 | 允许的后续操作 |
|---|---|---|---|---|---|
| 用户取消 | `cancelled`（`Attempt.Cancel`，`cancelled = 1`） | `failure_stage = execute`、`failure_summary` | 否 | — | 不可重开（`cancelled` 是终态） |
| 单次超时 | `timed_out`（`Attempt.Timeout`，`timed_out = 1`） | 同上 | 否 | — | 不可重开 |
| 服务关闭 | `interrupted`（`Attempt.Interrupt`） | 同上 | 否 | — | 不可重开 |
| 进程崩溃 | `interrupted`（启动恢复收敛残留 `running`） | `failure_stage = recover`、`failure_summary` 说明由启动恢复收敛 | 否 | 只有确认原执行者已失去执行权时才收敛 | 不可重开；历史保留 |

**Attempt 层约束**：

- `running` 是唯一的活动状态，且必须能被中断收敛。**收敛目标固定为
  `interrupted`**：它表达的正是"编排器或宿主进程异常结束，未能给出正常结论"，
  不需要新增枚举。
- 绝不把残留 `running` 改回 `running`，也绝不改成 `completed`。
- 收敛必须带状态前置条件（`UpdateAttemptIfStatus` 期望 `running`），迟到结果
  不得覆盖已确认终态。

### 2.5 Session（`sessions.status` + `run_status` / `completeness` / `export_status` / `archive_status`）

| 中断类别 | 目标状态 | 原因与诊断 | 能否确认业务完成 | 证据不足时 | 允许的后续操作 |
|---|---|---|---|---|---|
| 用户取消 | `cancelled` + `run_status = cancelled` | `failure_summary` | 否 | — | 只读 |
| 单次超时 | `failed` + `run_status = timed_out`（`SessionService.Finish` → `statusForRun`） | 同上 | 否 | — | 只读 |
| 服务关闭 | `failed` + `run_status = failed`（Runner 返回后由 `Finish` 收敛） | 同上 | 否 | 启动恢复收敛残留 `starting`/`running` 时写 `interrupted` | 只读 |
| 进程崩溃 | `interrupted`（启动恢复收敛残留） | `failure_summary` 说明由启动恢复收敛；`completeness = missing`、`export_status = not_attempted`、`archive_status = failed` | 否 | 只有确认原执行者已失去执行权时才收敛 | 只读；原始文件（若有）一律保留 |

**Session 层约束**：

- 三个独立维度（`completeness` / `export_status` / `archive_status`）**不得**因为
  收敛执行状态而被清空或改写：归档成功仍然是归档成功。
- 收敛残留 `starting`/`running` 时**不得**删除已有 `jsonl_path`、`html_path`、
  `stdout_path`、`stderr_path`、`process_id`、`exit_code`、`archive_path`：
  它们是唯一的现场。
- `SessionService.Fail` 是"没有 Runner 结果"的正式收敛入口（进程启动失败、
  工作目录不可用、启动恢复），它写入 `failed` + 明确的三维度取值，不留空。

### 2.6 ValidationRun（`validation_runs.status` / `verdict` / `error_detail`）

| 中断类别 | 目标状态 | 原因与诊断 | 能否确认业务完成 | 证据不足时 | 允许的后续操作 |
|---|---|---|---|---|---|
| 用户取消 | `cancelled` + `verdict = failed`（`DecideValidation`：流程未 `completed` 一律不判通过） | `error_detail` = "调用方取消验证" | 否 | — | 换新 Attempt 重新验证 |
| 单次超时 | `timed_out` + `verdict = failed` | `error_detail` = "验证整体超时" | 否 | — | 同上 |
| 服务关闭 | `failed` + `verdict = failed` | `error_detail` = "服务停止导致验证中断" | 否 | — | 同上 |
| 进程崩溃 | `failed` + `verdict = failed`（启动恢复收敛残留 `pending`/`running`） | `error_detail` = 启动恢复摘要 | 否 | 只有确认原执行者已失去执行权时才收敛 | 同上；**该 Attempt 不再被旧记录永久阻塞** |

**ValidationRun 层约束**：

- 流程状态与最终结论**分别持久化**，不得互相推导。
- **"执行中断导致无法验证" ≠ "确定性检查已经失败"**：
  - 中断 → `status ∈ {cancelled, timed_out, failed}` + `verdict = failed` +
    `error_detail` 说明中断来源；`failure_summary` 用 `describeUnfinishedRun` 的
    文案，绝不出现"必需检查未通过"这类暗示代码有问题的措辞。
  - 确定性检查失败 → `status = completed` + `verdict = failed` +
    `failure_summary` 列出失败检查。
- 中断的验证**绝不可能** `passed`（`DecideValidation` 第一条规则）。
- 残留 `pending`/`running` 会永久阻塞该 Attempt 的新验证（`BeginValidationRun`
  的活跃验证查询），因此必须有启动恢复入口。

---

## 3. 跨层一致性规则

1. **上层状态不得用于推断下层状态**。反例已在测试中固定：
   `tasks.status = completed` 与 `workflow_runs.status = recovery_required` 可以
   同时成立（`TestDeliveryChainKeepsTaskCompletedWhenCloseoutFails`）。
2. **下层状态不得单独决定上层终态**。单个步骤 `completed` 不推进 Task 到
   `completed`；单个验证 `passed` 也不推进。
3. **取消意图必须在持久化后立即对所有层可见**。执行记录层的取消请求必须在
   重启后仍然收敛任务层（B5）。
4. **执行权是唯一的活动判据**。`blocked` / `recovery_required` / `pending` 记录
   都不持有执行权，不能阻止其他恢复流程收敛其残留。
5. **不确定性保留**：任何"结果无法确认"的路径都必须留下可解释的 `error_class` /
   `failure_summary`，并且**不得**触发自动重放。

---

## 4. 与 P2-M3 的边界

本文定义并实现：

- 四类中断的判定与收敛目标（六层）；
- B1（步骤占用 × blocked 执行记录锁死）、B5（取消意图跨层丢失）、B6（租约误判）；
- `attempts` / `sessions` / `validation_runs` 残留的启动收敛；
- 验证引擎的取消、超时、服务停止测试；
- `TestPlanGenerationInterruptedByShutdown` 的确定性。

本文**不**实现（移交 P2-M3，已在 M3 落地）：

- 跨进程持久化的修复轮次计数 → `repair_cycles`（迁移 `0022_repair_cycles.sql`）+
  `attempts.budget_scope` / `budget_scope_id`；
- 统一的 retry eligibility 框架 → `domain.DecideRepairStart`（唯一准入规则）；
- 自动重新执行所有中断的 Attempt → **仍然禁止**：`interrupted` / `unknown` /
  `recovery_required` 一律阻止自动重放与开启新周期，必须先人工核验；
- `validation_feedbacks` 写入失败后的补偿路径 → 不补偿，由
  `RepairClassFeedbackUnavailable` 明确停止。

任何未确认执行结果的记录，都不得仅因"已达到某个状态"被自动重放。

> **M3 之后需注意的事实变化**：本文描述的残留收敛只把记录推进到合法终态，
> **不授予重试资格**。收敛完成后仍必须由 `domain.DecideRepairStart` 依据周期登记、
> 已消耗预算与历史尝试状态判定；判定为 `cycle_unresolved` 时保持阻塞。
