# P4：任务详情页面板职责与操作可达性复核（常规功能开发，不属于 P4-M5）

> - **状态**：`completed`（2026-10-28，基于当日 `origin/master` `cbaee84`）
> - **基线**：`origin/master` = `cbaee84eaca709d4202cd64c781000813712b26e`（`创建任务表单的字段收敛`），
>   当前分支 `master`，本轮开始前 `git status --porcelain -uno` 无输出（工作区干净）。
> - **性质**：这是"恢复常规功能开发"后的第五个常规工作项，**不是** P4-M5 性能优化。触发依据是
>   [`P4-task-detail-experience-review.md`](./P4-task-detail-experience-review.md) §8 与
>   [`P4-task-create-form-field-convergence.md`](./P4-task-create-form-field-convergence.md) §7 的
>   同一条建议："任务详情已累积 9 个面板，需要按实际页面结构核实'同一事实多处重复'与'关键动作不可达'"。
> - **P4-M5 保持 `pending`**：本轮没有产生任何真实使用数据，也没有把缺少数据的优化标记为完成。
> - 本文件记录**实际核实出的代码证据与实际实施**；已核实但判定无需修改的项逐条记录，未执行的
>   验证单独列出，不作"通过"表述。

---

## 1. 结论摘要

按当前代码逐面核实后，**面板职责本身是清楚的**，没有发现"前端自造业务规则"或"把任务状态、
执行受理、业务成功与任务收尾混为一谈"的情况；**没有证据支持折叠、标签页或重新排序**。本轮
只修复 4 处能由代码与用例直接证实的缺陷，全部属于"数据归属 / 枚举可读性 / 刷新时机"，不涉及
业务规则、状态机、重试与续跑资格、容器生命周期或远程操作语义：

| 编号 | 缺陷 | 证据位置 |
|---|---|---|
| D7 | 只读数据加载器不按"数据归属"作废缓存：同任务内切换步骤后，新步骤的首次查询失败时会把**上一个步骤**的尝试、会话与验证历史当作"当前步骤的旧数据"长期展示 | `useExecutionRecords.ts` 的 `useLoader`（对比同模块 `useStepRepairCycles` / `useTaskEvents` / `useTaskContainers` / `useResumeEligibility` / `useTaskRetry` 都有自己的归属判定） |
| D8 | 语义审查结论直接输出服务端原始枚举（`needs_human_review` / `unparseable` / `call_failed` …，共 10 个非零取值） | `StepDetailPanel.tsx` `语义审查：{run.review_status ?? "—"}` |
| D9 | 步骤失败阶段直接输出原始枚举 `execute` / `verify`，而同一模块已有中文映射且尝试级早已中文化 | `StepDetailPanel.tsx` `失败阶段 {step.failure_stage}` vs `steps.ts` 的 `ATTEMPT_FAILURE_STAGE_LABEL` |
| D10 | 容器列表的"外部刷新令牌"从未接线：`RefreshTokens` 没有 `containers`，唯一调用点也不传 `token`，因此重试重开任务并准备新容器后，面板仍指向被替换掉的旧容器 | `liveSnapshot.ts` 的 `RefreshTokens`、`TasksPage.tsx` 的 `<TaskContainerPanel …>`、`useTaskContainers.ts` 的 `token` 参数 |

**本轮未改动**：任何 Go 代码、HTTP 契约、`domain` 规则、任务状态机、重试 / 续跑资格、容器生命
周期、验证规则、数据库结构、前端依赖与 UI 框架；也没有新增定时器、没有改变分页、没有移动面板。

---

## 2. 核实方法与基线

1. `git fetch origin` 后确认 `origin/master` 与 `HEAD` 同为
   `cbaee84eaca709d4202cd64c781000813712b26e`，工作区干净，因此不存在"远端已有新提交"的情形。
2. 阅读 `.steps/README.md`、`.steps/ROADMAP.md`、`P4-product-experience-optimization.md`、
   `P4-task-detail-experience-review.md`、`P4-task-create-form-field-convergence.md`，把它们当作
   **上一轮结论**而不是当前代码事实。
3. 从 `TasksPage.tsx` 的真实引用出发，逐个读取任务详情的组件与数据来源（不按旧文档猜路径）：
   `TaskDetail` / `WorkflowPanel`（同文件）、`TaskExecutionPanel`、`StepListPanel`、
   `StepDetailPanel`、`RepairCyclePanel`、`EventTimelinePanel`、`OperationOutcomePanel`、
   `CodeSavePanel` / `CodeSaveVerificationSection`、`ResumeEligibilityPanel`、`TaskContainerPanel`、
   `RetryTaskPanel`，以及 `useTaskLiveSnapshot` / `liveSnapshot` / `autoRefresh` / `requestGate` /
   `useExecutionRecords` / `useExecutionOperations` / `useTaskContainers` / `useResumeEligibility` /
   `useTaskRetry` 与 `status.ts` / `steps.ts` / `codeSaves.ts` / `containers.ts` 等映射模块。
4. 用 Go 侧契约核对"前端是否有权展示某个事实"：`internal/httpapi/workflow.go`
   （`WorkflowRunResponse` / `WorkflowStatusResponse`）、`internal/httpapi/code_recovery.go`
   （`ResumeEligibilityResponse.Run` 复用同一个 `workflowRunResponse`）、
   `internal/domain/validation_decision.go`（`ReviewStatus` 的 11 个取值）、
   `internal/app/step_executor_run.go` / `step_workflow_run.go`（`Step.FailureStage` 实际写入
   `execute` / `verify`）。
5. 回归核对上一轮已修复的 6 项（D1–D6），逐条在当前代码中确认仍然成立（见 §5）。

---

## 3. 任务详情的实际面板结构与职责分类

详情区块自上而下的实际结构（`TasksPage.tsx` → `TaskExecutionPanel.tsx`）：

```text
任务详情（TaskDetail）
工作流进度（WorkflowPanel）
执行操作（按钮组 + OperationOutcomePanel）
计划步骤（StepListPanel）
步骤详情（StepDetailPanel）
  执行尝试 / 会话 → 修复周期历史 → 验证历史（含失败反馈）
事件时间线（EventTimelinePanel）
代码保存记录（CodeSavePanel）
续跑资格与阻塞原因（ResumeEligibilityPanel）
任务容器（TaskContainerPanel）
重试任务（RetryTaskPanel）
```

| 区块 | 数据来源 | 归属 | 刷新触发 |
|---|---|---|---|
| 任务详情 | `GET /tasks/{id}` | 任务级 | 5 秒自动刷新 / 手动刷新（`useTaskLiveSnapshot` 唯一调度器） |
| 工作流进度 | `GET /tasks/{id}/workflow`（同一轮次） | 任务级 | 同上 |
| 执行操作 + 操作结论 | 写接口真实响应（`useExecutionOperations`） | 任务级入口 + 步骤级动作 | 由用户显式点击；任务切换即清空 |
| 计划步骤 | `GET /tasks/{id}/steps?size=100` | 任务级 | `steps` 令牌 |
| 步骤详情（含尝试 / 会话） | `GET /steps/{stepID}/attempts`、`/attempts/{id}/sessions` | **步骤级**（依赖选中步骤） | 选中步骤 + `steps` 令牌 |
| 修复周期历史 | `GET /steps/{stepID}/repair-cycles` | 步骤级 | 步骤变化 + `steps` 令牌 |
| 验证历史 / 失败反馈 | `GET /steps/{stepID}/validations`、`/validations/{runID}/feedback` | 步骤级 | 同尝试 |
| 事件时间线 | `GET /tasks/{id}/events`（20 条分页） | **任务级** | 打开任务 / 手动刷新 / 加载更多 / `steps` 令牌；**不参与周期刷新** |
| 代码保存记录 | `GET /tasks/{id}/code-saves` | 任务级 | `codeSaves` 令牌 |
| 续跑资格与阻塞原因 | `GET /tasks/{id}/resume-eligibility` | 任务级 | `resume` 令牌（自动刷新转为停止后每轮递增） |
| 任务容器 | `GET /tasks/{id}/containers` | 任务级 | 挂载 / 手动刷新 / 处置后 / **本轮新增：任务状态迁移** |
| 重试任务 | `GET /tasks/{id}/retry-eligibility` | 任务级 | 挂载 / 手动刷新 / 重试后 |

**结论（回答问题 1）**：只有「步骤详情」及其内部三块（尝试与会话、修复周期、验证历史）依赖
当前选中的步骤与尝试；其余全部是任务级事实。事件时间线**语义上是任务级**，但当前渲染在
`TaskExecutionPanel` 内部（见 §4.5）。

---

## 4. 逐项复核结论（对应立项问题 2–7）

### 4.1 同一事实是否仍在多处重复展示（问题 2）

| 重复项 | 判定 | 依据 |
|---|---|---|
| 工作流执行记录在「工作流进度」与「续跑资格与阻塞原因」各出现一次（状态、执行记录标识、进度、停止原因、更新时间） | **合理的现有设计** | 两处都来自同一个 `httpapi.WorkflowRunResponse`（`/workflow` 与 `/resume-eligibility` 共用 `workflowRunResponse`），不存在两套取值；续跑资格面板把它作为 `can_resume` 的**现场证据**（与 `code_save_records` 同一性质），并额外给出「阶段」与「服务端未返回执行记录」的显式说明；`resume` 令牌在策略转为停止后每轮递增，两处随同一次刷新同步，不会停留在不同代次 |
| 代码保存记录在「代码保存记录」与「续跑资格」各出现一次 | **合理的现有设计（上一轮已判定）** | 后者只作为 `can_resume=false` 的阻塞证据，并标明"服务端给出的判定范围"，不是第二份代码保存面板 |
| 步骤状态在「计划步骤」与「步骤详情」各出现一次 | **合理的现有设计** | 列表行是"哪一步处于什么状态"的导航信息，详情头部与 `步骤状态` 字段是"我打开的这一步是什么状态"；三处都走同一份 `STEP_STATUS_META`，取值一致，不存在权威冲突 |
| 步骤状态在「步骤详情」头部 `DictTag` 与 `步骤状态` 行同时出现 | **待验证假设**（低收益） | 同一面板内两次展示同一个标签、同一个中文值，不产生矛盾；删除任一处都是纯审美取舍，缺少真实反馈支持，本轮不改 |
| 工作流面板的「当前步骤」是原始 `current_step_id`，而「计划步骤」用步骤名称 | **合理的现有设计** | 前者是工作流游标（可能指向尚未开始的下一步），后者是计划与执行事实；上一轮已把"第二份步骤列表"从工作流面板移除，只留下这个游标字段与一句指路说明 |

### 4.2 关键操作是否难以发现或使用（问题 3）

核实到的操作入口共 12 个：列表行（启动 / 取消 / 删除）、详情（取消工作流）、执行操作（生成计划 /
顺序执行 / 刷新步骤）、步骤详情（执行步骤 / 开发—验证闭环 / 开启新执行周期）、尝试（确定性验证 /
初始化代码）、代码保存（逐记录核验）、复跑（恢复工作流）、容器（删除 / 脱离）、重试（重试任务）。

- **已证实缺陷**：容器列表的刷新时机（D10）。处置入口直接指向真实容器记录，但重试重开任务后
  列表不重新读取，用户看到的是已被替换的旧容器。
- **待验证假设**：`OperationOutcomePanel` 位于「计划步骤」之上，而步骤级动作按钮在「步骤详情」
  里。步骤较多或已展开大量尝试时，点击后的"正在执行 / 失败 / 结果不确定"提示可能落在视口之外。
  这需要真实视口与真实使用证据（当前无法只从代码判断是否真的看不见），本轮不移动面板、不新增
  第二份结论展示（那会造成新的重复展示）。
- **已闭合（2026-10-30）**：步骤详情的三个动作按钮禁用时没有原因提示（`canExecuteStep` 只看任务状态、
  步骤类型与占用），而重试 / 恢复 / 执行操作的按钮都有 tooltip。这与上一轮登记的"任务列表启动按钮无
  禁用原因提示"是同一类一致性问题；两项已在
  [`P4-disabled-action-reasons.md`](./P4-disabled-action-reasons.md) 一并处理（准入条件未变，只是把已
  有的判定结果显式说出来）。

### 4.3 面板顺序是否与操作流程冲突（问题 4）

没有代码证据表明当前顺序违背实际操作流程：任务事实（状态 / 结论 / 收尾）→ 工作流进度 →
执行现场（计划 → 步骤 → 尝试 / 验证）→ 历史（事件）→ 代码保存 → 处置（续跑 / 容器 / 重试）。
上一轮修复 D6（步骤详情移到事件时间线之前）后，步骤级操作入口与其反馈都在历史之前。本轮
**不做任何顺序调整**。

### 4.4 事件时间线是否应提升为任务级面板（问题 5）

核实结论：**本轮不做**，理由是缺少收益证据而不是缺少实现能力。

- 它已经是任务级数据源（`useTaskEvents` 只按任务标识查询，20 条分页、`eventRefs` 展示关联标识），
  与步骤选择**完全解耦**：切换步骤不会重新读取它。
- 它**不参与** 5 秒周期刷新，只由 `steps` 令牌、手动刷新与"加载更多"驱动；物理位置在
  `TaskExecutionPanel` 内部不改变它的读取时机与生命周期。
- 若上移到任务级区块，它会与「代码保存记录 / 续跑资格 / 任务容器 / 重试任务」同级并位于其后，
  即离"刚选中的步骤"更远；这与 D6 的修复目标（缩短从步骤操作到其反馈的距离）方向相反。
- 因此"提升为任务级面板"目前只是分类学上的整齐，不是可测量的收益。要做必须先有真实的使用
  证据（例如"用户需要在不选步骤的情况下查看历史"的报告）。登记为待验证假设。

### 4.5 面板数量是否足以证明需要折叠或分组（问题 6）

不足以。当前 9 个顶层区块（§3）确实较多，但：

- 每个区块都有明确职责与唯一数据来源，没有发现"同一事实两种取值"（§4.1）；
- 关键操作（取消工作流、恢复、重试、容器处置、逐步骤执行）都各自带前置条件说明或 tooltip，
  没有因为数量而被隐藏；
- 本轮没有任何真实使用数据（点击热区、滚动距离、用户报告），按项目约定不得把推测写成已证实
  的用户痛点。
因此**不折叠、不分标签页、不重排**，登记为待验证假设。

### 4.6 是否存在纯前端可解问题（问题 7）

| 发现 | 是否需要服务端改动 |
|---|---|
| D7 归属缓存 | 不需要：纯前端状态派生 |
| D8 / D9 原始枚举 | 不需要：Go 侧枚举取值已存在，缺的只是前端映射 |
| D10 容器刷新令牌 | 不需要：`Token` 参数与 Hook 契约早已存在，只是没接线 |

本轮**没有**需要新增服务端数据或契约才能解决的问题。核实时同时确认了一处**确实**需要契约
能力才能改进的既有缺口（不属于本轮范围）：同步长请求没有流式进度推送（M3-7 已交付轮询，SSE 未
实现），见 `P4-M2-7-web-execution-operations.md` §6。

---

## 5. 上一轮 6 项修复的回归核对

| 上一轮缺陷 | 当前代码 | 结果 |
|---|---|---|
| D1 工作流面板重复展示步骤 | `WorkflowPanel` 只渲染执行记录汇总，并有一句"以「计划步骤」为准"的指路说明 | 未回归 |
| D2 结论文本输出原始枚举 | `steps.ts` / `launch.ts` / `retry.ts` / `ResumeEligibilityPanel` 均已中文化 | 未回归，但**遗漏两处**（本轮 D8 / D9） |
| D3 详情读取失败与操作失败共用标题 | 详情失败为"加载任务详情失败"，操作失败为"操作失败" | 未回归 |
| D4 自动刷新停止后仍显示"自动刷新失败" | 标题按 `autoRefreshOn` 区分 | 未回归 |
| D5 切换任务保留上一个任务的选中步骤 | `selection = { stepID, taskID }`，仅当任务一致时生效 | 未回归 |
| D6 步骤详情被事件时间线隔开 | `StepDetailPanel` 在 `EventTimelinePanel` 之前 | 未回归 |

---

## 6. 已证实缺陷与修复

### D7 只读加载器不按归属作废缓存

**代码证据**

- `useExecutionRecords.ts` 的通用加载器 `useLoader<T>(load, deps, token)` 用 `dataRef` 保存
  "最近一次成功的数据"，刷新失败时保留它并附上错误（这是**正确**的设计）。但它只按 `deps`
  触发重载，**不记录这份缓存属于哪条记录**：`deps` 变化时 `dataRef` 仍然保留上一条记录的数据。
- `useStepRecords` 的 `deps` 是 `[task, step]`，而它被 `TaskExecutionPanel`（常驻）调用；
  `StepDetailPanel` 虽然以 `key={step.id}` 重挂载，**状态却在父组件里**。因此在同一任务内切换
  步骤时，新步骤的第一帧展示的仍是上一个步骤的 `ready` 数据。
- 同模块的其它加载器都不是这样做的：`useStepRepairCycles` 按 `${task}/${step}` 归属清空、
  `useTaskEvents` 在任务变化时清空累计结果、`useTaskContainers` 用 `{taskID,data}` 快照、
  `useResumeEligibility` / `useTaskRetry` 用 `{taskID,data}` 快照。只有被 3 处复用的
  `useLoader` 缺失这一判定。

**影响**：切换步骤后，如果新步骤的执行记录查询失败（容器已删、节点异常、网络中断，都是真实
可能），页面会把**上一个步骤**的尝试、会话与验证历史显示在新步骤下，并只附一句"执行记录刷新
失败"。这是"数据归属错误"，比"没有数据"更容易误导：用户会据此判断当前步骤已经执行过。

**修复**：把加载器的第二个参数从 `deps` 改为显式的 `owner`（缓存归属标识），在 effect 内比较
归属并**立即作废**上一条记录的缓存；归属变化时不再进入"保留旧数据"的分支（旧数据描述的已经
不是当前记录）。三个调用点分别传入任务标识（步骤列表、代码保存记录）与"任务 + 步骤"
（执行记录）。同一记录内的刷新失败行为完全不变。

**副作用核对**：任务切换时 `useTaskLiveSnapshot` 会先把快照置为 `loading`（`task` 为空），
整个详情区块连同这些面板被卸载，所以跨任务展示旧数据当前**不可达**；本修复的作用是让这条
不变式不再依赖"面板恰好被卸载"这一隐式前提（E2E 用例保留为不变式守卫，见 §8）。

### D8 语义审查结论输出原始枚举

**代码证据**：`StepDetailPanel.tsx` 的验证记录块渲染
`语义审查：{run.review_status ?? "—"}`，`review_status` 来自
`domain.ReviewStatus`（`internal/domain/validation_decision.go`），共 10 个非零取值
（`passed` / `failed` / `needs_human_review` / `unparseable` / `invalid_output` / `call_failed` /
`timed_out` / `cancelled` / `interrupted` / `not_configured`）。同一块里 `流程` 走
`validationStatusLabel`、`判定` 走 `verdictMeta`，只有它是一行英文原文。

**影响**：中文界面里出现 `语义审查：needs_human_review`，而这恰好是"不能视为验证通过"的关键
事实；用户既读不懂，也无法把它与 `verdict` 区分开。

**修复**：`steps.ts` 新增 `REVIEW_STATUS_LABEL` + `reviewStatusLabel`（缺省显示 `—`，未知取值
原样展示，与 `codeFetchStatusLabel` 同一写法），面板改用该函数。语义审查与验证结论仍然分开
展示，不互相替代。

### D9 步骤失败阶段输出原始枚举

**代码证据**：`StepDetailPanel.tsx` 的步骤描述表渲染 `失败阶段 {step.failure_stage}`；
`Step.FailureStage` 由服务端写入 `execute` / `verify`（`step_executor_run.go`、
`step_workflow_run.go`、`step_workflow_repair.go`），而同一模块早已有
`ATTEMPT_FAILURE_STAGE_LABEL`（`execute` → 执行阶段、`verify` → 验证阶段、`recover` → 服务恢复
阶段），尝试级也早已通过 `attemptFailureFacts` 使用它。

**影响**：同一个"失败阶段"事实，在尝试行显示"失败阶段 执行阶段"，在步骤行显示 `execute`；
说明这是遗漏而不是设计。

**修复**：步骤描述表改用 `attemptFailureStageLabel(step.failure_stage)`（同一组阶段名共用一份
映射），并在调用处注明取值来源。未知阶段仍原样展示，便于诊断。

### D10 容器列表的刷新令牌从未接线

**代码证据**

- `useTaskContainers.ts` 明确定义了 `token`：「外部刷新令牌：由调用方在需要重新读取容器列表时
  递增」，并在 effect 依赖里使用它。
- `TaskContainerPanel` 声明并透传了 `token`，但 `TasksPage.tsx` 是**唯一**调用点，且
  `<TaskContainerPanel onRefreshTask={…} task={selected} />` 从不传 `token`。
- `liveSnapshot.ts` 的 `RefreshTokens` 只有 `steps` / `codeSaves` / `resume`，
  `applySnapshotRound` 的 `dirty` 也没有 `containers`，因此令牌**永远无法递增**。

**影响**：容器由环境准备与重试阶段创建。用户在详情里点「重试任务」后，服务端重开任务并准备新
容器，而「任务容器」面板继续展示被替换掉的旧容器（列为"运行中 / 未脱离"），处置入口指向的对象
也不再是当前容器。这是"操作后现场不更新"，而不是"容器规则错误"。

**修复**：`RefreshTokens` 增加 `containers`，`applySnapshotRound` 在**任务状态真的迁移**时标记
`containers` 为 dirty，`useTaskLiveSnapshot.bumpTokens` 递增它，`TasksPage` 把它传给
`TaskContainerPanel`。刻意保留两条既有约束：

- **首轮不递增**（`previous.task` 为空时不标记）：面板挂载时已经读过一次，否则打开详情会重复请求；
- 容器列表**不并入按周期的刷新**：只在状态迁移时读取一次，避免紧挨着破坏性操作产生隐式读取风暴。

---

## 7. 修改文件与行为变化

生产代码（全部位于 `lingting-agent-flow-ui/src/pages/tasks/`，未触碰 Go 与 HTTP 契约）：

| 文件 | 行为变化 |
|---|---|
| `useExecutionRecords.ts` | `useLoader` 改为按 `owner` 作废缓存；三个调用点分别传入任务标识与"任务 + 步骤"。同一步骤内的刷新失败仍保留旧数据，行为不变 |
| `steps.ts` | 新增 `REVIEW_STATUS_LABEL` + `reviewStatusLabel`（覆盖 `domain.ReviewStatus` 的 10 个非零取值，未知原样） |
| `StepDetailPanel.tsx` | 语义审查与步骤失败阶段改用中文映射；其余展示不变 |
| `liveSnapshot.ts` | `RefreshTokens` 增加 `containers`；任务状态迁移时标记 dirty（首轮不标记） |
| `useTaskLiveSnapshot.ts` | `bumpTokens` 支持 `containers` |
| `TasksPage.tsx` | `<TaskContainerPanel>` 接收 `token={tokens.containers}`（格式化后为多行） |

未修改：任何 Go 代码、HTTP 契约、`domain` 规则、任务状态机、重试 / 续跑资格、容器生命周期与
远程操作语义、验证规则、数据库结构、前端依赖与 UI 框架；没有新增定时器、没有改变分页、没有
移动或折叠任何面板。

---

## 8. 测试与验证（实际执行）

| 命令 | 实际结果 |
|---|---|
| `pnpm test:unit:safe` | **通过**：28 个测试文件 / 607 个用例（本轮新增 4 个用例，与上一轮文档记录的 603 相加正好一致） |
| `pnpm test:e2e:safe` | **通过**：233 通过 / 0 失败（本轮新增 3 个用例；`--workers=1`，未提高并发）。上一轮文档记录 229，加上本轮新增应为 232，实测为 233，说明上一轮文档的计数比实际少 1，本轮未追溯该差异的来源 |
| `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | **通过** |
| `pnpm build` | **通过**，产物写入 `internal/webui/dist`（仅既有分块体积提示） |
| Go 测试与 `go vet` | **未执行**：本轮未修改任何 Go 文件与后端契约 |

新增用例（全部针对本轮缺陷）：

- 单元 `task-execution-state.test.ts`：语义审查标签覆盖 `domain.ReviewStatus` 全部非零取值；
  `needs_human_review` / `unparseable` / `invalid_output` / `call_failed` 都不被表述为"审查通过"；
  零值显示占位符、未知取值原样保留。
- 单元 `task-progress-auto-refresh.test.ts`：`containers` 令牌在首轮不递增、同状态轮询不递增、
  任务状态迁移时递增一次。
- E2E `task-execution.spec.ts`：切换步骤后不展示上一个步骤的执行记录（新步骤查询失败时显示加载
  失败）；切换任务后不展示上一个任务的计划步骤（不变式守卫）。
- E2E `task-containers.spec.ts`：重试导致任务状态迁移后重新读取容器列表，面板改为展示新容器。

**用例有效性核对**：把两处修复点临时改回修改前行为（`if (false && ownerRef.current !== owner)`、
`containers: false`）后分别运行对应用例，确认它们**失败**（说明不是空转），随后恢复代码并重新
运行全量套件。任务切换那条用例在修改前后都通过，因为该场景由"快照进入 loading → 面板卸载"
保障；它被保留为不变式守卫，不能算作本轮修复的证明。

未执行：Go 侧任何测试（未改 Go 文件）、真实 SSH / Docker / Pi 远程环境验证（本轮改动全部是前端
展示与刷新时机，不产生远程副作用）、浏览器人工走查（用户未提供地址，也未要求访问页面）。

测试产物保留在操作系统临时目录，未删除：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<随机后缀>\`
（含 `TEST_ARTIFACT_DIR`、Playwright 的 `playwright-output/` 与 `playwright-report/`）。

---

## 9. 保留的合理设计、待验证假设与未解决风险

**保留的合理设计**

1. 工作流执行记录与代码保存记录在「续跑资格」面板中的重复展示（§4.1）。
2. 事件时间线留在 `TaskExecutionPanel` 内、步骤详情之后，且不参与周期刷新（§4.4）。
3. 容器列表不并入按周期刷新，只在任务状态迁移、手动刷新与处置后读取（§6 D10）。
4. `useTaskLiveSnapshot` 作为唯一调度器，子面板只吃令牌；请求票据 + 任务归属校验 + 迟到响应
   丢弃的机制未被改动。
5. `StepListPanel` 的 `validationStates` 死参数仍是既有死代码，按项目约定只指出、不主动删除。

**待验证假设（本轮不改）**

1. `OperationOutcomePanel` 的位置可能让步骤级动作的反馈离开视口（需真实视口证据）。
2. 步骤详情三个动作按钮禁用时没有原因提示（与任务列表"启动"按钮同类）。
3. 9 个顶层区块是否需要折叠 / 分组（无真实使用数据）。
4. 事件时间线是否需要独立的任务级位置（无收益证据）。
5. 步骤状态在步骤详情内出现两次（同一取值，纯审美）。

**风险**

1. D10 的令牌在任务状态迁移时读取一次容器列表；重复重试会增加少量只读请求（每次状态迁移一次，
   不进入周期循环）。
2. D7 让归属变化时先进入 `loading`：切换步骤的瞬间列表区会短暂显示"正在加载执行记录…"，
   而不是继续显示上一个步骤的数据（这是期望行为，但与修改前的观感不同）。
3. 归属作废发生在 effect 内，与同模块其它加载器一致：切换后的第一帧仍可能渲染旧数据
   （React 先渲染后执行 effect）。持久化的错误展示已被消除，单帧闪现未处理。

---

## 10. 下一项建议（仅列有代码证据的方向）

1. ~~**`verification_results` 遗留模型清理**~~：**已于 2026-10-29 完成**。审计确认生产链路上唯一
   的读取方是 `app.TaskService.Detail` 的 `TaskDetail.Verifications`（CLI 从不打印该字段），
   写入方为零；领域类型、`storage.VerificationStore` 与其 SQLite 实现、该读取方一并删除，
   遗留表与历史数据保留（未新增删除迁移）。
2. ~~**步骤级动作按钮的禁用原因**~~：**已于 2026-10-30 完成**
   （[`P4-disabled-action-reasons.md`](./P4-disabled-action-reasons.md)）。`canExecuteStep` /
   `canExecuteAndValidate` / `canOpenRepairCycle` 的三个前置条件已在注释里写明，本轮把它们改造成同时
   返回 `allowed` 与 `reason` 的纯函数并显示出来，同时覆盖任务列表的启动按钮。
3. ~~**私有仓库 Git 凭据支持**~~：**已由用户明确决定不纳入产品范围（2026-10-29）**，从候选
   清单中移除；需要私有仓库的用户自行维护执行镜像里的认证环境（见 `AGENTS.md` 的
   "私有仓库认证的产品边界"）。
4. **任务详情页剩余的两项待验证假设**（`OperationOutcomePanel` 的位置、9 个区块是否折叠）
   仍缺少真实使用数据，不构成立项依据。

---

## 11. 与 P4-M5 的关系

P4-M5 要求"优化项有真实使用问题或可测量数据支撑"。本轮 4 处缺陷都是**数据归属、枚举可读性与
刷新时机**问题，由代码与用例直接证实，不涉及性能、并发、存储或架构；本轮没有产生任何可用于
性能立项的数据，因此：

- **`P4-M5` 保持 `pending`**，未标记为完成，也未进行缺少数据的评估；
- 本轮不构成 P4 阶段结束：P4 仍只剩数据依赖型的 `P4-M5`。
