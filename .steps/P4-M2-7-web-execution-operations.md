# P4-M2-7 —— Web 接入计划生成、步骤执行与验证修复操作

> 实施依据：`master` HEAD `7138c49`（`:sparkles: 代码保存记录列表 API`）的实际代码核实与
> 实施。核实范围：`internal/httpapi/{router.go, plan.go, executions.go, step_execution.go,
> step_sequence.go, step_workflow.go, step_cycle.go, validation.go, code_initialize.go,
> execution_feedback.go}`、`internal/app/{step_executor.go, step_sequence.go, step_workflow*,
> validation.go, code_initialize.go}`、`internal/domain/{task.go, task_queue.go, step.go,
> step_execution.go, attempt.go, validation.go, plan_generation.go, git_state.go}`；
> 前端 `lingting-agent-flow-ui/src/lib/api/*`、`src/pages/tasks/*`、`tests/`。
> 覆盖 P4-M1 缺口 **G13**（计划生成）与 **G14**（单步骤 / 顺序执行 / 开发—验证闭环 /
> 修复周期），并顺带接入 G20 的两项单步诊断动作（初始化代码、确定性验证）。
>
> **本轮未修改任何后端代码**：所有接口在前端接入前先按代码核实契约，未发现阻塞级缺陷。

## 1. 操作矩阵（按真实路由核实）

路径前缀统一为 `/api/v1/tasks/{taskID}`。所有写操作都是**同步**的：服务端只在真实执行
收敛后才返回，因此前端对这些请求不设客户端超时（`timeoutMs: 0`），避免服务端仍在执行
时客户端提前断开，留下"前端以为失败、后端仍在跑"的现场。

| 操作 | 真实接口 | 前置条件（代码证据） | 成功响应 | 失败语义 | 副作用 |
|---|---|---|---|---|---|
| 生成计划 | `POST {taskID}/plan` | `domain.CheckPlanGenerationStart`：任务未删除、未请求取消、`CanStartPlanning()`、`container_id` 非空、**没有任何步骤**（拒绝覆盖已有计划）、已配置 `planner_role_id`；此外 `GeneratePlan` 还要求没有未结束的 `workflow_run`、执行节点可用、容器受管理（`plan.go:151`、`task_workflow_plan.go:84-144`、`plan_generation.go:71-110`） | 200 + `PlanResponse`：`task_status`、`persisted`、`summary`、`steps[]`、`session_id`、`failure_stage`/`failure` | 400 `invalid_request`（超时越界、Planner 输出无法构成计划 `ErrPlanInvalidOutput`）、404（任务/租约不存在）、409（状态冲突、已有未结束工作流、已有步骤）、412（节点/容器前置条件）、503（服务停止） | 有：真实调用 Planner、写 `steps`、推进任务状态 |
| 查询步骤 | `GET {taskID}/steps` | 任务存在且未逻辑删除（`ExecutionRecordService.requireTask`） | 200 + `StepListResponse`（`records`/`total`/`current`/`size`） | 404 不存在；400 分页参数非法 | 只读 |
| 单步骤执行 | `POST {taskID}/steps/{stepID}/execute` | `domain.CheckStepExecutionStart`：任务未删除、步骤属于该任务、未请求取消、`AllowsStepExecution()`、步骤状态可开始、`current_attempt_id` 为空、步骤类型需要 Agent 角色、已锁定角色；`checkPrerequisites` 要求全部前序步骤 `completed`/`skipped`；节点预检与容器受管理（`step_executor.go:311-348`、`step_execution.go:104-142`、`git_state.go:103-126`） | 200 + `ExecuteStepResponse`：`outcome`、`success`、`attempt_id`/`attempt_number`、`session_id`、`step_status`/`task_status`、`finalized` | 400、404、409（占用/状态/租约冲突）、412（前置条件）、500 `execution_failed`（执行未成功）、500 `execution_not_persisted`（状态未完整写入） | 有：真实 Pi 调用、新建 Attempt + Session、Git 快照 |
| 顺序执行 | `POST {taskID}/execute-steps` | 同上任务级准入 + `domain.NextStepToExecute` 逐个取步骤；进程内按任务串行（`step_sequence.go:87-127`） | 200 + `ExecuteStepsResponse`：`steps[]`、`executed_steps`、`stopped`、`success`、`stop_reason`、`stopped_step_id`、`task_status` | 同上；步骤执行失败时返回 200 + `stopped=true`（已执行但提前停止） | 有：连续多次 Pi 调用 |
| 开发—验证闭环 | `POST {taskID}/steps/{stepID}/execute-and-validate` | `CheckStepExecutionStart` + `requireStepVerificationSpec`（必须存在启用的必需检查）+ `domain.DecideRepairStart`（预算/现场）+ 前序步骤检查（`step_workflow_repair.go:40-113`） | 200 + `ExecuteAndValidateStepResponse`：`outcome`、`validation_ran`、`validation_status`/`validation_verdict`、`verified`、`round`/`attempts`/`max_attempts`、`repair_decision`/`repair_class` | 400（含 `ErrStepWorkflowInvalidConfig`）、404、409（`ErrStepWorkflowCycleDenied`/占用）、412、500 `execution_failed`（未通过验证）、500 `execution_not_persisted` | 有：多轮 Pi 调用 + 容器内检查命令 + 反馈落盘 |
| 单次验证 | `POST {taskID}/steps/{stepID}/attempts/{attemptID}/validate` | `domain.ValidationRun.CheckScope`（任务/步骤/尝试归属）+ `ValidationTaskStatus`（developing/verifying/retrying/paused/recovery_required）+ `CanValidateAttempt`（尝试已结束且结果确定）+ 未请求取消 + 容器受管理（`validation.go:504-537`） | 200 + `ValidateAttemptResponse`：`status`、`verdict`、`passed`、检查统计、`persisted`、`checks[]` | 400、404、409（归属/状态/占用）、412（容器/节点）、500 `execution_not_persisted` | 有：容器内执行检查命令、写 `validation_runs` |
| 代码初始化 | `POST {taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code` | `checkCodeInitAllowed`：任务未请求取消且状态在 {queued, preparing, planning, planned, developing, retrying, paused, recovery_required}，尝试状态在 {running, failed, interrupted, recovery_required}；容器必须登记该尝试归属且工作区就绪（`code_initialize.go:891-927`、`:221-280`） | 200 + `InitializeCodeResponse`：`status`、`succeeded`、`task_branch`、`commit`、`persisted` | 400、404、409、412、500 `execution_failed`、500 `execution_not_persisted` | 有：容器内 Git Clone / checkout |
| 开启修复周期 | `POST {taskID}/steps/{stepID}/open-repair-cycle` | `domain.DecideRepairStart(openNewCycle=true)`：任务允许推进、前序步骤完成、步骤未被占用、上一周期已收敛且预算用尽或策略不再允许修复；从未执行过的步骤拒绝（`step_workflow_cycle.go:301-360`、`repair_cycle.go:370-430`） | 200 + `OpenRepairCycleResponse`：`cycle_id`、`cycle_sequence`、`max_attempts`、`previous_consumed`、`opened_at` | 409 `ErrStepWorkflowCycleDenied`（拒绝原因原样返回）、400、404、412 | 有：写 `repair_cycles`、事件；**不启动任何 Agent 调用** |

补充只读接口（本轮一并接入，用于展示关联关系）：

| 接口 | 用途 |
|---|---|
| `GET {taskID}/steps/{stepID}/attempts` | 步骤的执行尝试历史 |
| `GET {taskID}/steps/{stepID}/attempts/{attemptID}/sessions` | 尝试关联的 Session（执行/归档/导出三个独立状态） |
| `GET {taskID}/steps/{stepID}/validations` | 验证历史 + 服务端派生的 `state` + `latest` |
| `GET {taskID}/steps/{stepID}/validations/{runID}/feedback` | 验证失败反馈（404 = 该轮没有反馈，不是通过） |

### 1.1 关键契约结论

1. **统一启动不可被替代**：`/start` 包含环境准备 + 计划生成 + 工作流启动；新任务
   （`queued`、无容器、无步骤）走任何步骤接口都会因 `AllowsStepExecution()` 与
   `CheckPlanGenerationStart` 的容器要求被拒。因此执行面板明确提示"新任务必须先通过
   任务列表的「启动」"，且不把 `plan` / `execute-steps` 当作新任务入口。
2. **`plan` 不创建容器**：`CheckPlanGenerationStart` 要求 `task.ContainerID` 非空，因此
   该入口只在容器已准备的现场可用；它同时拒绝覆盖已有计划。
3. **顺序执行返回 200 但可能 `success=false`**：`ExecuteSteps` 在"已执行但提前停止"时
   返回 `(result, nil)`，只有"没有产生真实执行"（状态冲突、前置条件缺失）才返回错误。
   前端因此必须同时看 `stopped` / `success` / `stop_reason`，不能只看 HTTP 状态码。
4. **闭环未通过验证返回 500**：`serveExecuteAndValidate` 在 `!result.Verified` 时写
   `CodeExecutionFailed`（500），响应体是统一错误结构，`verified=false` 的真实状态只在
   服务端日志与数据库中。前端因此把 500 + `execution_failed` 展示为"未通过验证"并保留
   服务端 message，不伪造成功。
5. **反馈 404 的语义**：`GetValidationFailureFeedback` 在没有反馈时返回 404；前端把它表达
   为"该轮验证没有失败反馈记录（验证通过，或反馈未写入）"，既不是错误也不是通过。
6. **修复周期只登记预算**：`OpenRepairCycle` 不发起任何 Agent 调用，成功后仍需显式执行；
   前端结果面板明确写出这一点。

## 2. 实施顺序

1. **M2-7A 计划与步骤可见**：先补 API 类型与封装，再在任务详情中展示真实步骤
   （名称 / 顺序 / 类型 / 状态 / 错误摘要 / 验证状态 / 当前占用尝试）。
2. **M2-7B 执行操作**：步骤详情接入单步骤执行、开发—验证闭环、顺序执行，以及尝试级的
   确定性验证与代码初始化；计划生成放在任务级操作区。
3. **M2-7C 修复周期**：接入显式开启修复周期，并把服务端拒绝原因原样呈现。

## 3. 改动文件与关键行为

前端新增：

| 文件 | 职责 |
|---|---|
| `src/lib/api/execution.ts` | 执行现场全部接口的路径构造与请求封装（计划、步骤、尝试、会话、验证、反馈、执行、闭环、初始化、修复周期），全部复用 `apiRequest` / `ApiError`，写请求不设客户端超时 |
| `src/lib/api/types.ts` | 与 Go DTO 一一对应的类型：`Step`/`StepStatus`、`Attempt`/`AttemptStatus`、`Session`、`ValidationRun`/`ValidationHistoryResponse`/`ValidationCheckResult`、`ValidationFailureFeedback`、`ExecuteStepResponse`、`ExecuteStepsResponse`、`ExecuteAndValidateStepResponse`、`ValidateAttemptResponse`、`InitializeCodeResponse`、`OpenRepairCycleResponse`、`PlanResponse` |
| `src/pages/tasks/steps.ts` | 步骤/尝试/验证状态元数据与中文标签、操作可用性判定（对齐 `AllowsStepExecution`、`CanStartPlanning`、`CanStartExecution`、`CanValidateAttempt`、`checkCodeInitAllowed`）、以及把每个响应翻译成"事实清单"的函数 |
| `src/pages/tasks/useExecutionRecords.ts` | 只读加载器：`useTaskSteps`、`useStepRecords`（尝试 + 验证历史 + 每个尝试的会话）、`useValidationFeedback`（404 → `not_found`）；刷新失败保留上一次数据 |
| `src/pages/tasks/useExecutionOperations.ts` | 全部写操作的统一调用：单动作在途锁（同步 ref + state）、成功只来自响应字段、动作后统一刷新、刷新失败单独记录、从不自动重试 |
| `src/pages/tasks/OperationOutcomePanel.tsx` | 执行结论 / 业务错误 / 刷新失败三类提示分别呈现 |
| `src/pages/tasks/StepListPanel.tsx` | 步骤列表：加载 / 失败（带重试）/ 空（明确"计划未持久化"）/ 有数据四态；未知状态原样展示 |
| `src/pages/tasks/StepDetailPanel.tsx` | 步骤详情：描述字段、三个步骤级动作、尝试列表（含 Session 与"验证 / 初始化代码"）、验证历史（含逐项检查与可展开的失败反馈） |
| `src/pages/tasks/TaskExecutionPanel.tsx` | 任务级操作区（生成计划 / 顺序执行）+ 步骤列表 + 步骤详情的组合，动作后刷新任务与工作流状态 |

前端修改：

- `src/lib/api/index.ts`：导出新增的接口、路径函数与类型。
- `src/pages/tasks/TasksPage.tsx`：任务详情中挂载 `TaskExecutionPanel`；用 `selectedRef`
  保存最新选中任务，供动作后的刷新回调读取；关闭详情时一并清空 ref。

测试新增：

- `tests/unit/task-execution-api.test.ts`（24 个用例）：路径构造与逐段编码、空标识拒绝、
  步骤/尝试/验证历史查询解析、反馈 404、全部写请求的方法与请求体（只发送执行参数，
  不含 `session_path` / `work_dir` / `role_id` / `steps` / `container_id`）、
  业务失败与网络失败、`execution_not_persisted`、修复周期拒绝原因、计划 `persisted=false`。
- `tests/unit/task-execution-state.test.ts`（27 个用例）：状态元数据覆盖 Go 全部取值、
  未知取值原样展示、任务/步骤/尝试级准入判定逐状态对照、每个响应到"事实清单"的翻译
  （计划未持久化不显示完成、执行成功不表述为任务成功、闭环只有 `verified` 才通过、
  验证结论与持久化分别展示、修复周期只说明预算登记）。
- `tests/e2e/task-execution.spec.ts`（20 个用例）：真实步骤与状态展示、空计划明确说明、
  查询失败不伪装成空列表、未知状态原样展示、单步骤执行使用真实步骤标识、执行失败展示
  业务错误、闭环进行中阻止重复提交、顺序执行停止原因、按真实 `attemptID` 发起验证、
  验证未通过不显示成功、失败反馈与执行记录关联、反馈 404 语义、代码初始化结果、
  修复周期拒绝与成功、执行后刷新失败保留结论、步骤刷新失败保留旧数据、`queued` 任务
  禁用执行入口、非 Agent 步骤禁用执行入口、任务创建与统一启动无回归。

## 4. 测试结果（实际执行）

| 命令 | 结果 |
|---|---|
| `npx vp test run`（全量前端单元测试） | **通过**：14 个文件 / 191 个用例 |
| `npx playwright test tests/e2e/task-execution.spec.ts` | **通过**：20/20 |
| `npx playwright test`（全量前端 E2E） | **70 通过 / 3 失败**，失败项见 §5 |
| `npx tsc --noEmit` | 通过（无输出） |
| `npx vp lint`（含 `vp fmt`、`vp lint --fix`、`tsc --noEmit`） | 通过（无输出） |
| `npx vp build` | 通过；仅有既有的 chunk 体积告警 |

`TEST_ARTIFACT_DIR`：每次运行由 `tests/internal/artifacts.ts` 生成独立随机目录
（`<OS 临时目录>/lingting-agent-flow-ui-tests/run-*`），运行结束后保留。本轮最后三次运行的
目录分别为 `run-19S2DC`（全量 E2E）、`run-W31GCc` 等（单元测试）。

## 5. 未通过项与未执行项

**全量 E2E 的 3 个失败项，均已通过基线对照确认为既有问题**（在 `git stash -u` 移除本轮
全部改动后，用同一命令复现出相同的失败用例与失败断言）：

1. `tests/e2e/layout.spec.ts:43` `/machines 可访问且导航高亮、页面标题一致`
2. `tests/e2e/layout.spec.ts:64` `刷新后路由与导航状态保持`
3. `tests/e2e/tasks.spec.ts:478` `详情返回 404 时展示错误而不是伪造任务`

基线对照命令与结果：`npx playwright test tests/e2e/layout.spec.ts
tests/e2e/task-create-selectors.spec.ts` → 2 failed / 21 passed（与本轮相同的两个 layout
用例）；`npx playwright test tests/e2e/tasks.spec.ts` → 1 failed / 12 passed（与本轮相同的
404 用例）。本轮未修改这些用例涉及的代码路径，也未修复它们。

另有一次全量运行中 `tests/e2e/task-create-selectors.spec.ts:463` 失败，单独重跑该文件时
10/10 通过，判定为并发执行下的偶发时序问题，非本轮改动引入。

**未执行项**：

- 未运行任何 Go 测试（本轮未修改后端代码，且未获得测试授权）。
- 未做真实环境验证：没有启动 `agentflow server` + 真实 SSH / Docker / Pi，全部浏览器
  测试都在网络层拦截 API。真实容器内的执行、验证与修复周期行为未在本轮被验证。

## 6. 剩余风险

1. **同步长请求与浏览器连接**：单步骤执行、顺序执行、闭环、验证、代码初始化都是同步的，
   最长可达数小时（超时上限 21600 秒）。前端不设客户端超时，因此一次长执行期间页面只能
   显示"正在执行"，没有进度推送（进度轮询归 P4-M3 M3-7）。用户刷新页面会丢失本地结论
   （服务端记录仍在）。
2. **闭环 500 的语义损失 —— 已由后续工作处理（2026-10-31）**：本条原始结论是"未通过验证
   时服务端返回统一错误结构，`verified`、`round`、`attempts`、`checks` 等结构化事实不在响应体
   内，要展示完整结构需要后端调整契约"。该契约调整已实施：`httpapi.ErrorResponse` 增加可选
   `operation_result`（复用 `ExecuteAndValidateStepResponse`），只在确实产生开发尝试且不是
   未知内部错误时附加；前端在失败面板中显示真实执行详情并明确"不代表操作成功"。详见
   [`P4-execute-and-validate-failure-response.md`](./P4-execute-and-validate-failure-response.md)。
   仍未处理的部分：失败响应不含原始 stdout / stderr 与完整容器输出（需要完整输出时仍要查
   执行记录）。
3. **修复周期可用性只能粗判**：前端只能排除"任务不允许推进"与"步骤被占用"两类现场，
   预算是否用尽、上一周期是否收敛只能由服务端判定，因此按钮常亮但可能被 409 拒绝。
   这是有意取舍：拒绝原因原样展示，不伪造可用性。
4. **计划覆盖不可用**：服务端拒绝覆盖已有计划，前端因此在有步骤时不显示生成入口；
   真正的"重新规划"需要计划版本化能力，未立项。
5. **会话查询是 N+1**：`useStepRecords` 对每个尝试单独查询会话列表（执行记录接口只在
   尝试维度提供该入口）。步骤尝试数上限受计划与预算约束，当前不构成问题；如后续需要
   一次拿全，应在后端新增步骤级会话查询而不是在前端聚合更多请求。
6. **无自动刷新**：动作后的刷新是一次性的，外部（调度器、其他客户端）推进的状态不会
   自动反映到页面，需手动刷新（归 P4-M3 M3-7）。

## 7. 下一步

P4-M2 的 7 个子任务至此全部完成，可将 `P4-M2` 标记为 `completed`。下一步按
[`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md) 进入
**P4-M3 展示阶段进度、验证结果、失败原因和恢复状态**，其中 M3-2（验证历史视图）与
M3-5（代码保存三阶段）已由本轮的只读接口与展示骨架部分铺垫。
