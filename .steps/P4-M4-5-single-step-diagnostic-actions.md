# P4-M4-5 —— 单步诊断动作（初始化代码、确定性验证）

> 实施依据：`origin/master` HEAD `c95b754f2433eafb6d3692fe2a3d549a5fb3dd8d`
> （`:zap: 基于服务端续跑资格的工作流恢复操作`）。实施前后均确认本地 `HEAD` 与该 SHA 相同，
> 且工作区无未提交改动，未覆盖或丢弃任何既有修改。
>
> **本轮只修改前端 3 个生产文件与 2 个测试文件，未修改任何 Go 代码、HTTP 契约、Attempt 状态机、
> 验证规则、容器生命周期与代码初始化业务规则，未新增任何 HTTP API。**

## 1. 开发前核对

| 项目 | 结果 |
|---|---|
| 当前分支 | `master` |
| 工作区状态 | 干净（实施前无未提交改动） |
| `origin/master` SHA | `c95b754f2433eafb6d3692fe2a3d549a5fb3dd8d` |
| 本地 `HEAD` | 同 `origin/master` |
| `git fetch origin` | 已执行，无新提交 |

阅读的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`
（G20 定义与验收标准）、`.steps/P4-M2-7-web-execution-operations.md`、`.steps/P4-M4-1`、
`.steps/P4-M4-2`、`.steps/P4-M4-3`、`.steps/P4-M4-4` 四个交付文档。

阅读的实际代码：`TaskExecutionPanel.tsx`、`StepDetailPanel.tsx`、`useExecutionOperations.ts`、
`steps.ts`、`useExecutionRecords.ts`、`useTaskContainers.ts`（作为竞态与防重的既有范式）、
`resumeAction.ts` + `useResumeEligibility.ts`（作为"结果不确定"的既有范式）、
`lib/api/{execution,endpoints,types,client}.ts`、`tests/unit/task-execution{,-state}.test.ts`、
`tests/e2e/task-execution.spec.ts`，以及 Go 端 `internal/httpapi/{code_initialize,validation}.go`、
`internal/app/{code_initialize,validation,validation_run}.go`、
`internal/domain/{validation,validation_decision,code_fetch,step_execution}.go`。

## 2. 实施前的真实现状（代码证据）

### 2.1 M2-7 已完成的能力

`useExecutionOperations.ts:12-16` 已声明 7 个执行类动作，其中包含本轮的两个目标
（`validate`、`initialize-code`）；`StepDetailPanel.tsx` 的 `AttemptBlock`（`attempt` 维度）
已为每个真实 `Attempt` 渲染「确定性验证」与「初始化代码」两个按钮，
点击时把 `step` 与 `attempt` 对象整体传给 hook，路径由
`lib/api/execution.ts` 的 `attemptValidatePath` / `attemptInitializeCodePath` 逐段编码构造，
**前端没有任何手填或拼装任务 / 步骤 / 尝试标识的入口**。

`steps.ts` 已提供与服务端一致的准入提示：`canValidateAttempt`（对齐
`domain.ValidationTaskStatus` + `domain.CanValidateAttempt`）、
`canInitializeAttempt`（对齐 `app.checkCodeInitAllowed`），
且两个按钮都带 `disabled={busy || ...}`（`busy = operations.state.running !== undefined`）
与同步 `runningRef` 在途锁，因此重复点击不会产生第二个请求。

`types.ts:1095-1160` 已与 Go 端 DTO 一一对应地声明了 `ValidateAttemptResponse`
（含 `status` / `verdict` / `passed` / 统计 / `persisted` / `checks[]`）与
`InitializeCodeResponse`（含 `status` / `succeeded` / `task_branch` / `commit` / `persisted`）。

### 2.2 结论：M2-7 与 M4-5 的关系

M2-7 交付的是**"动作可用"**——入口存在、请求路径正确、按钮按服务端契约禁用；
M4-5 的目标是**"用户能安全地显式触发并理解真实结果"**——结果诚实性、结果不确定与服务端
拒绝的区分、迟到响应归属、刷新失败与执行结论的分离。因此 M4-5 **不是**重建入口，
而是审计结果语义与竞态边界。

## 3. 审计发现的真实缺口（4 处）

### 缺口 1：验证结论被压缩成二元，`inconclusive` 与未知判定被显示为"验证未通过"，判定缺失被显示为"验证通过"

**证据**：`steps.ts` 的 `describeValidateResult` 用
`kind: result.passed ? "success" : "warning"` 与
`title: result.passed ? "验证通过" : "验证未通过"` 决定结论。
而 Go 端 `domain.VerificationVerdict` 有三个取值（`passed` / `failed` / `inconclusive`，
见 `internal/domain/validation_decision.go:113-130`），且 `inconclusive`
（语义审查失败、评估缺失、要求人工复核）会被压成"验证未通过"。
更严重的是：`passed` 是 Go 端的便捷布尔值（`httpapi/validation.go:74` 注释明确
"不表示流程一定正常结束"）；响应若缺少 `verdict`，前端会展示 **"验证通过"**。

**影响**：直接违反 M4-5「未知状态或结果持久化成功不得被解释为验证通过」。

### 缺口 2：验证响应已提供的逐项检查结果完全没有展示

**证据**：`ValidateAttemptResponse.checks: ValidationCheckResult[]` 在
`types.ts:1118` 已声明、Go 端 `validateAttemptResponse` 已逐项输出
（`check_id` / `check_name` / `required` / `status` / `exit_code` / `timed_out` /
`cancelled` / `certain` / `failure_reason` / `stderr_summary` / `output_truncated`），
但 `describeValidateResult` 只输出通过 / 未通过 / 跳过的**统计数字**，
`checks` 字段在前端零读取。用户点了按钮却看不到"哪一项检查、退出码多少、为什么失败"。

其中 `certain`（结果是否可信）尤其关键：`certain=false` 表示基础设施异常，
不能当作代码问题——而这一语义在现有实现里完全丢失。

### 缺口 3：动作请求的"结果不确定"与服务端拒绝被混为一谈

**证据**：`useExecutionOperations.ts` 的 `runOperation` 在 `catch` 中只做
`setState({ failure: { ...target, message: describeApiError(cause) } })`，
而 `describeApiError`（`useExecutionRecords.ts:49-58`）把 `timeout` / `network` / `aborted`
统一翻译成"请求超时，请稍后重试"之类的文案。

**影响**：这两个诊断动作有真实副作用（容器内 Git Clone/checkout、容器内执行检查命令、
写 `code_fetches` / `validation_runs`）。客户端超时或连接中断意味着**服务端可能已经执行**，
把它与 4xx 业务拒绝展示成同一种"失败"，并配上一句"请稍后重试"，会诱导用户重复触发
有副作用的操作。M2-7 之后的 M4-1 / M4-2 / M4-3 / M4-4 都已建立这一区分
（`retry.ts isUncertainRetryError`、`codeSaveVerification.ts isUncertainVerifyError`、
`containers.ts isUncertainDisposalError`、`resumeAction.ts isUncertainResumeError`），
**唯独执行操作 hook 没有**，形成同一页面内的语义不一致。

### 缺口 4：切换任务后迟到的动作响应会写进新任务的界面，并触发一次属于旧任务的现场刷新

**证据**：`runOperation` 只有同步 `runningRef` 防重，**没有任务归属判定**。
`TaskExecutionPanel.refreshAll` 的依赖是 `[onRefreshTask, records.reload, stepsState.reload]`，
`useExecutionOperations` 的 `runOperation` 依赖是 `[refresh, taskID]`；
`tasksState.reload` / `records.reload` 都是 `useCallback(() => setNonce(...), [])`（稳定），
因此**切换任务时 `refresh` 引用不变、`runOperation` 不重建、在途 Promise 继续存活**。
其 `finally` 块会无条件 `setState({ refreshError, running: undefined })` 并对新任务的
`stepsState.reload()` / `records.reload()` / `onRefreshTask()` 发起刷新。
结果是：旧任务的动作结论与"正在执行：..."会出现在新任务页面顶部，
且新任务被一次与它无关的刷新波及。

同一模块内的 `useTaskContainers`、`useTaskLiveSnapshot`、`useResumeEligibility` 都用
"票据 + activeTaskRef + mountedRef"解决了该问题，执行操作 hook 是唯一遗漏点。

### 缺口 5（措辞层，同一处代码）：`persisted` 与 `succeeded` 的说明不足

`describeInitializeResult` 已分别输出 `succeeded` 与 `persisted`（合规），但
`task_branch` 为空时会渲染成 `任务分支：`（空值被读成"没有分支"而不是"服务端未提供"），
且 Go 端 `domain.CodeFetchStatusUncertain`（`code_fetch.go:92-94`："执行结果无法确认，
必须先核验容器内实际状态"）在结果面板里没有任何提示。

## 4. 实施方案与修改文件

**全部改动都在前端，均为上述缺口的直接修复；未重写面板结构，未新增 Hook、未新增 API 封装、
未新增刷新循环、未触碰任何 Go 代码。**

| 文件 | 改动 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/steps.ts` | 新增 `validationVerdictLabel`、`verdictOutcome`（判定是结论唯一来源：`passed` → 通过，`failed` → 未通过，`inconclusive` / 未知 / 缺省 → 无法判定）、`describeCheckFact`（逐项检查事实：名称、必需性、状态、退出码、超时、取消、`certain=false`、失败原因、stderr 摘要、截断标记）、`OPERATION_UNKNOWN_OUTCOME_HINT`、`isUncertainOperationError`；重写 `describeValidateResult` 改由判定决定标题并输出逐项检查与人工复核提示；`describeInitializeResult` 补齐分支缺失占位与 `uncertain` 提示 |
| `lingting-agent-flow-ui/src/pages/tasks/useExecutionOperations.ts` | `ExecutionOperationsState` 新增 `uncertain` 字段；`runOperation` 新增任务票据 + `activeTaskRef` + `mountedRef` 三重判定（`accepted()`），迟到响应与迟到刷新一律丢弃；`finally` 不再用 `return` 提前跳出（消除 `no-unsafe-finally` 告警），并先释放 `runningRef` 再判定；`ApiError` 的 `timeout` / `network` / `aborted` 归类为结果不确定并附固定提示；`clear()` 在动作在途时不清空在途标记 |
| `lingting-agent-flow-ui/src/pages/tasks/OperationOutcomePanel.tsx` | 新增独立的"操作结果无法确认"告警块，与服务端拒绝的 `failure` 块、动作后刷新失败的 `refreshError` 块三者分别呈现 |
| `lingting-agent-flow-ui/tests/unit/task-execution-state.test.ts` | 新增 `describe("单步诊断动作结论的诚实性")` 共 10 个用例 |
| `lingting-agent-flow-ui/tests/e2e/task-execution.spec.ts` | 新增 8 个浏览器用例 |

### 4.1 关键取舍

1. **`passed` 不再参与结论判定**。`verdict` 是唯一来源；`passed` 仅作为响应中的一个普通字段
   存在。这样 `verdict` 缺失、未知取值与 `inconclusive` 都不会被读成通过。
2. **不新增第二套拒绝分类**。`uncertain` 与 `failure` 同时存在，前者是"请求可能已生效"的
   明确提示，后者仍原样展示服务端 message，因此 4xx 业务拒绝仍逐字可见。
3. **不让 `clear()` 打断在途动作**。步骤切换会调用 `operations.clear()`；在途时清空会抹掉
   `running` 标记从而让按钮重新可用，故改为在途时直接返回。动作结束后的 `setState` 用函数式
   更新合并到 `current`，不会复活已清空的旧结论。
4. **不把刷新失败写进 `uncertain`**。刷新失败仍是 `refreshError`，与动作本身的执行结论分离。

## 5. 验收标准与证据

| 验收点 | 实现位置 | 证据 |
|---|---|---|
| 只作用于当前选定步骤下的真实 Attempt，不用手填标识 | 既有 `AttemptBlock` 传 `step` + `attempt`，路径由 `attemptValidatePath` / `attemptInitializeCodePath` 逐段编码 | 既有用例「对指定尝试发起验证并使用真实尝试标识」「初始化代码展示就绪状态」 |
| 展示真实验证状态、判定、统计、逐项结果、持久化 | `describeValidateResult` | 单测「逐项检查结果按真实字段展示」「结果未持久化时仍保留真实判定」；E2E「验证逐项检查结果按真实字段展示」 |
| HTTP 成功 ≠ 验证通过；未知 / 缺省判定不读作通过 | `verdictOutcome` + `validationVerdictLabel` | 单测「判定 inconclusive 与未知取值都不展示为通过」「判定缺失时说明服务端未提供」；E2E「判定 inconclusive 时展示为无法判定」 |
| 验证失败保留服务端业务错误，不压平为"操作失败" | 既有 `failure` 块 + 服务端 message | E2E「服务端拒绝时展示真实业务错误，不提示结果无法确认」 |
| 初始化代码展示 `status` / `succeeded` / `task_branch` / `commit` / `persisted` | `describeInitializeResult` | 单测「代码初始化只把就绪与持久化同时成立看作成功」「缺失分支与提交时不补造取值」 |
| `persisted` 不等于初始化成功 | `kind` 需 `succeeded && persisted` | 同上 |
| 结果不确定时提示先核验现场、不自动重试 | `uncertain` + `OPERATION_UNKNOWN_OUTCOME_HINT` | 单测「超时、网络中断与取消被判定为结果不确定」；E2E「验证请求结果不确定时与业务拒绝分别提示，且不自动重发」「初始化代码结果不确定时要求先核验现场」 |
| 在途防重 | `runningRef` + `disabled={busy}` | E2E「诊断动作进行中重复点击只发出一次请求」（断言请求次数为 1） |
| 任务切换后迟到响应不污染当前页面 | `accepted()` 三重判定 | E2E「切换任务后旧的诊断结论不会污染新任务」 |
| 动作后刷新失败与执行结论分离 | `refreshError` 独立块 | E2E「诊断动作成功后刷新失败时保留真实结论并单独提示」 |
| 准入提示只是提示，服务端仍是否最终权威 | 既有 `canValidateAttempt` / `canInitializeAttempt` 注释与服务端错误原样展示 | E2E「queued 任务不显示步骤执行与顺序执行入口」等既有用例未回归 |
| 不自动触发任何动作 | 两个动作只在按钮 `onClick` 中调用 | 单测 / E2E 均无自动触发路径；`clear()` 与刷新都不调用 `validate` / `initializeCode` |

### 5.1 未修改 Go 代码的理由

核实到的 4 处缺口都在前端：结论二元化、`checks` 未读取、错误语义未区分、迟到响应无归属判定。
服务端契约（`ValidateAttemptResponse` / `InitializeCodeResponse` / 各错误码与状态码）已经
提供修复所需的全部字段与语义，无需修改生产 Go 代码。M4-5 的硬约束是不在证据不足时改动
Go 契约，本轮不满足改动条件。

## 6. 实际执行的验证命令与真实结果

全部在 `lingting-agent-flow-ui/` 下执行，未提高任何 worker 数。

| 命令 | 结果 |
|---|---|
| `npx vp test run --maxWorkers 1 tests/unit/task-execution-state.test.ts` | **通过**：40 / 40（原 30 + 新增 10） |
| `pnpm test:unit:safe` | **通过**：26 个文件 / 557 个用例 |
| `npx playwright test tests/e2e/task-execution.spec.ts --workers=1` | **通过**：34 / 34（原 26 + 新增 8） |
| `pnpm test:e2e:safe` | **通过**：205 / 205，0 失败（7.2 分钟；含本轮新增 8 个）。首次全量运行时为 204 / 204（当时新增用例为 7 个），补齐"动作后刷新失败与结论分离"用例后重跑为 205 |
| `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | **通过**，无输出。曾出现 1 条 `no-unsafe-finally` 告警，已通过重排 `finally` 结构修复 |
| `pnpm build` | **通过**；仅有既有的 chunk 体积告警 |

`TEST_ARTIFACT_DIR`：由 `tests/internal/artifacts.ts` 生成独立随机目录
（`<OS 临时目录>/lingting-agent-flow-ui-tests/run-*`），运行结束保留。
本轮主要运行目录：`run-xUgLX7`（单文件单测）、`run-OsPZ23` 等（全量单测）。
全量 E2E 运行目录见该次运行的 `TEST_ARTIFACT_DIR` 输出。

换行符：本次修改的 5 个文件均为 LF，已逐个核验不含 `\r`。

## 7. 未执行项与原因

- **未运行任何 Go 测试**：本轮未修改任何 Go 文件（`git status` 仅有 5 个前端文件），
  且未获得测试授权。
- **未做真实环境验证**：全部浏览器测试都在网络层拦截 API。没有启动 `agentflow server` +
  真实 SSH / Docker / Pi，因此"真实容器内 `git clone` 与检查命令执行"的行为本轮未被验证；
  用例只能证明 UI 对给定响应的处理。
- **未修改文档中的 G20 行**：`P4-M1-api-web-gap-inventory.md` 是盘点快照，按既有惯例
  （G9 完成时保留原行并注明闭合来源）不改写其历史结论。

## 8. 剩余风险

1. **同步长请求仍无进度**：两个动作都是同步的，验证最长可达 21600 秒。页面只能显示
   "正在执行：确定性验证（步骤 …）"，没有进度推送（属 P4-M3 M3-7 之外的后续工作）。
   用户刷新页面会丢失本地结论（服务端记录仍在）。
2. **结果不确定无法自动收敛**：`uncertain` 只提示用户先核验现场，前端不会重读该 Attempt 的
   最新验证 / 初始化记录来"自动确认"。这是因为验证记录接口是步骤维度
   （`GET {taskID}/steps/{stepID}/validations`），没有"按尝试取最近一次"的入口，
   前端聚合会制造 N+1 且仍无法确定"本次请求是否就是那条记录"。
3. **逐项检查输出仍只有摘要**：`stdout_summary` / `stderr_summary` 是服务端裁剪后的文本，
   完整输出只在 `stdout_path` / `stderr_path` 指向的落盘文件里，前端没有读取通道，
   因此"为什么失败"的可读深度受服务端摘要限制。
4. **`closeout` 不受影响**：本轮不改变任何成功判定，步骤通过验证仍不等于任务完成；
   任务终态仍由 `TaskWorkflowService` 依据确定性验证与代码保存决定。
