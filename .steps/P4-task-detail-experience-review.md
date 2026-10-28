# P4：任务详情页信息结构与操作可达性复核（常规功能开发，不属于 P4-M5）

> - **状态**：`completed`（2026-10-28，基于当日 `origin/master` `d6a6a19`）
> - **基线**：`origin/master` = `d6a6a1982827f5c78aaa3896b22afdd33146620d`（`管理管理支持`），
>   当前分支 `master`，本轮开始前工作区干净（`git status --porcelain -uno` 无输出）。
> - **性质**：这是"恢复常规功能开发"后的第二个工作项，**不是** P4-M1 缺口清单中的 G 项（G1–G21
>   已全部闭合），也**不是** P4-M5 性能优化。触发依据是
>   [`P4-G12-project-management.md`](./P4-G12-project-management.md) §13 的第 1 条建议：
>   "任务详情已累积 9 个面板，是否存在'同一事实在多处重复展示'或'用户找不到关键动作'的真实问题，
>   需要按实际页面结构核实，而不是凭感觉重排"。
> - **P4-M5 保持 `pending`**：本轮没有产生任何真实使用数据，也没有把缺少数据的优化标记为完成。
> - 本文件记录的是**实际核实出的代码证据与实际实施**；已核实但判定无需修改的项也逐条记录，
>   未执行的测试与未解决的问题单独列出，不作"通过"表述。

---

## 1. 结论摘要

按当前代码核实出 **6 处**能由代码直接证实的缺陷，全部属于"读起来自相矛盾"或"关键动作被隔开"
这一类，不涉及业务规则。已全部按最小改动修复，未新增接口、未改 Go 代码、未改数据库结构：

| 编号 | 缺陷 | 证据位置 |
|---|---|---|
| D1 | 步骤事实在工作流面板与计划步骤中重复展示，且两处状态取值一处中文一处原始英文 | `TasksPage.tsx` `WorkflowPanel` vs `StepListPanel.tsx` |
| D2 | 结论文本里任务状态 / 尝试状态 / 工作流状态 / 工作流阶段 / 代码初始化状态直接输出服务端原始枚举 | `launch.ts`、`steps.ts`、`retry.ts`、`ResumeEligibilityPanel.tsx` |
| D3 | "详情读取失败"与"有副作用的操作失败"共用同一个提示标题"操作失败" | `TasksPage.tsx` 两处 `Alert` |
| D4 | 自动刷新已停止后，手动刷新失败仍显示"自动刷新失败"，与同一行的"自动刷新已停止"矛盾 | `TasksPage.tsx` + `liveSnapshot.ts` |
| D5 | 切换任务后组件仍保留上一个任务的选中步骤，并按旧步骤标识向新任务发起一次必然失败的查询 | `TaskExecutionPanel.tsx` |
| D6 | 选中步骤后，步骤详情被整段任务事件时间线隔开，操作入口需要越过历史才能看到 | `TaskExecutionPanel.tsx` 渲染顺序 |

**未修改的候选**（核实后判定当前实现合理，理由见 §6）：`validationStates` 死参数、
`OperationOutcomePanel` 不显示尝试标识、任务列表"启动"按钮无禁用原因提示。

---

## 2. 核实方法与基线

1. `git fetch origin` 后确认 `origin/master` 与 `HEAD` 同为
   `d6a6a1982827f5c78aaa3896b22afdd33146620d`，工作区干净，因此不存在"远端已有新提交"的情形。
2. 逐文件阅读任务详情的真实组成与数据来源（不依赖旧文档复述）：
   `TasksPage.tsx`（列表 + `TaskDetail` + `WorkflowPanel` + 详情区块）、
   `TaskExecutionPanel.tsx`、`StepListPanel.tsx`、`StepDetailPanel.tsx`、
   `CodeSavePanel.tsx`、`ResumeEligibilityPanel.tsx`、`TaskContainerPanel.tsx`、
   `RetryTaskPanel.tsx`、`EventTimelinePanel.tsx`、`RepairCyclePanel.tsx`、
   `OperationOutcomePanel.tsx`、`status.ts`、`steps.ts`、`closeout.ts`、`launch.ts`、
   `retry.ts`、`resumeAction.ts`、`autoRefresh.ts`、`liveSnapshot.ts`、`requestGate.ts`、
   `useTaskLiveSnapshot.ts`、`useExecutionOperations.ts`、`useExecutionRecords.ts`、
   `useTaskContainers.ts`。
3. 用 Go 侧契约核对前端是否有权展示某个事实：
   `internal/httpapi/workflow.go`（`workflowStatusResponse` 的 `Steps` 映射）、
   `internal/httpapi/executions.go:562`（`Name: step.Title`）、
   `internal/app/task_workflow_run.go`（`workflowStepViews`）、
   `internal/domain/workflow_run.go`（`WorkflowStage` 五个取值）、
   `internal/domain/code_fetch.go`（`CodeFetchStatus` 八个取值）。
4. 追踪刷新与切换链路：`useTaskLiveSnapshot`（唯一调度器）→ `liveSnapshot.applySnapshotRound`
   → `requestGate.acceptResponse`；子面板（步骤 / 代码保存 / 续跑资格 / 容器 / 重试）各自
   只在令牌或任务变化时重载，均不建第二个定时器。

---

## 3. 当前任务详情的实际结构（修改前）

详情区块自上而下由 1 个详情表 + 1 个工作流面板 + 4 个独立面板 + 1 个执行现场复合面板组成，
每个事实的唯一数据来源如下：

| 区块 | 数据来源 | 说明 |
|---|---|---|
| 任务详情 | `GET /tasks/{id}`（快照） | 任务状态、最终业务结论、收尾闭环三行独立事实 |
| 工作流进度 | `GET /tasks/{id}/workflow`（同一快照轮次） | 执行记录、进度、当前步骤、停止原因 |
| 执行操作 / 计划步骤 / 事件时间线 / 步骤详情 | `GET /tasks/{id}/steps` 等 | 复合面板 `TaskExecutionPanel` |
| 代码保存 | `GET /tasks/{id}/code-saves`（令牌驱动） | 提交 / 推送 / 合并 / 记录写入 / 核验五组事实 |
| 续跑资格 | `GET /tasks/{id}/resume-eligibility`（令牌驱动） | `can_resume` 是唯一判据 |
| 容器处置 | `GET /tasks/{id}/containers` | 两个动作独立确认 |
| 重试任务 | `GET /tasks/{id}/retry-eligibility` | `can_retry` 是唯一判据 |

**核实结论**：面板职责本身是清楚的，没有发现"前端自造业务规则"的情况；问题集中在两处——
**同一事实被两个区块各渲染一次**（D1），以及**纯文本结论把服务端枚举直接吐给用户**（D2）。

---

## 4. 缺陷清单与修复

### D1 步骤事实重复展示，且两处状态取值不一致

**代码证据**

- `TasksPage.tsx` 的 `WorkflowPanel` 在修改前渲染了第二个步骤列表，状态直接输出原始取值：
  `<Typography.Text code>{step.status}</Typography.Text>`。
- `StepListPanel.tsx` 渲染同一批步骤时走统一映射：`<DictTag dict={STEP_STATUS_DICT} value={step.status} />`，
  未知取值另有 `isKnownStepStatus` 分支原样展示。
- 两处数据确实来自同一批行：`internal/httpapi/workflow.go` 的 `workflowStatusResponse` 由
  `app.WorkflowStatusResult.Steps` 映射，而该视图由 `internal/app/task_workflow_run.go` 的
  `workflowStepViews` 从 `[]domain.Step` 生成；步骤标题在 `/workflow` 里是 `Title`，
  在 `/steps` 里由 `internal/httpapi/executions.go:562` 映射为 `Name: step.Title`，同源同名。
- 修改前，概览行不可点击，无法从概览跳到步骤详情；而详情列表可以。

**影响**：同一个步骤状态在同一个屏幕上同时显示为 `developing` 与 `开发中`，用户无法判断哪一处
是权威信息；概览还破坏中文界面的一致性，并且是"看起来像调试输出"的展示。

**修复**：只保留**单一权威位置**（下方「计划步骤」），工作流面板改为一行说明
"步骤顺序、状态与失败原因以「计划步骤」为准；本面板的数字只汇总执行记录。"。
不新增第二个列表、不改变读取时机。E2E 断言"工作流响应独有的第 2 步不再被渲染"。

### D2 结论文本里直接输出服务端原始枚举

**代码证据（修改前）**

- `steps.ts` `describeExecuteStepResult` 的 `details` 同时包含
  `步骤状态：${stepStatusMeta(...).label}`（中文）与
  `尝试：第 N 次（${result.attempt_status}）`、`任务状态：${result.task_status}`（原始英文）。
- `steps.ts` `describePlanResult`、`describeExecuteStepsResult` 的 `任务状态：${result.task_status}`。
- `steps.ts` `describeInitializeResult` 的 `初始化状态：${result.status}`（`domain.CodeFetchStatus`）。
- `launch.ts` `describeLaunchResult` 的 `任务状态：${result.task_status}`。
- `retry.ts` `describeRetryResult` 的 `执行记录：（${run.status}…）`。
- `ResumeEligibilityPanel.tsx` 的 `阶段：{run.stage}`（`domain.WorkflowStage`）。
- 对照：`resumeAction.ts` 与 `retry.ts` 的任务状态早已通过字典中文化，
  `steps.ts` 的步骤状态也早已中文化 —— 说明这是遗漏而不是设计。

**影响**：同一次操作的结果面板里，一行中文、一行英文；页面上写着"开发中"的任务，
结论里写着 `developing`，用户无法把结论与页面状态对应起来。

**修复**：全部复用既有映射，不新增状态名、不新增第二套取值：
- `status.ts` 新增 `taskStatusLabel(status)`（复用 `TASK_STATUS_LABEL`，未知取值原样返回，
  并用 `Object.hasOwn` 防原型键误命中）；
- `status.ts` 新增 `WORKFLOW_STAGE_LABEL` + `workflowStageLabel`（覆盖 `domain.WorkflowStage`
  的 5 个取值，未知原样）；
- `steps.ts` 新增 `CODE_FETCH_STATUS_LABEL` + `codeFetchStatusLabel`（覆盖
  `domain.CodeFetchStatus` 的 8 个取值，未知原样），并按项目既有风格（对照
  `SESSION_ARCHIVE_STATUS_LABEL`）独立成表，不与工作区初始化状态共用；
- `steps.ts` 尝试状态改用同文件的 `attemptStatusMeta(...).label`；
- `retry.ts` 执行记录改用 `workflowRunStatusMeta(run.status).label`（不改函数签名，
  `statusLabels` 选项语义保持不变）。

### D3 详情读取失败与操作失败共用标题"操作失败"

**代码证据**：`TasksPage.tsx` 中两处 `Alert` 的 `title` 都是 `操作失败` ——
一处是 `actionError`（启动 / 取消 / 删除等有副作用的动作），一处是 `snapshot.error && !selected`
（只读详情首次加载失败）。既有 E2E 也以 `操作失败` 断言详情 404
（`tests/e2e/tasks.spec.ts`、`tests/e2e/task-closeout.spec.ts`），说明该文案已被当作可接受文案固化。

**影响**：用户在看到"操作失败"时无法判断是"我点了某个动作但失败了"还是"我只是打开了一条记录
但读取失败"，前者意味着可能产生过远程副作用。

**修复**：详情读取失败的标题改为"加载任务详情失败"；操作失败的标题保持不变。
同时把三处断言 `getByText("任务详情")).toHaveCount(0)` 收紧为 `{ exact: true }` ——
新标题包含"任务详情"子串，原来的子串匹配会把提示标题误当成详情区块。

### D4 自动刷新已停止后仍显示"自动刷新失败"

**代码证据**：`liveSnapshot.applySnapshotRound` 把失败说明写进 `snapshot.refreshError`，
而该字段同时被自动刷新轮次与 `refreshNow()`（手动刷新、动作后刷新）共用
（`useTaskLiveSnapshot.ts`）；`TasksPage.tsx` 渲染该提示时标题固定为 `自动刷新失败`，
而同一区块下一行已经在按真实策略显示"执行期间每 5 秒自动刷新 / 自动刷新已停止"。
在 `blocked` / `recovery_required` / 终态场景，`decideAutoRefresh` 一定返回
`{ mode: "off" }`，此时页面会同时显示"自动刷新已停止"与"自动刷新失败"。

**影响**：自相矛盾的提示；用户无法判断页面是否仍在轮询，可能误以为失败会自动恢复。

**修复**：标题按真实的刷新机制区分——`autoRefreshOn ? "自动刷新失败" : "刷新失败"`。
失败说明文本（`describeRefreshFailures`）保持原样，仍然说明"展示的是上一次成功获取的状态"。

### D5 切换任务后保留上一个任务的选中步骤

**代码证据**：`TaskExecutionPanel` 用 `useState<string>()` 保存 `selectedStepID`，
父组件 `<TaskExecutionPanel …>` 没有 `key`，因此切换任务时组件不卸载、状态不重置；
`useStepRecords(task.id, selectedStepID, …)` 的第一帧就会用**新任务标识 + 旧步骤标识**
发起一次必然 404 的查询（且该错误因为 `selectedStep` 为空而不展示，属于静默失败）。
对照同模块的其它状态实现：`useExecutionOperations` 在 `trimmedTaskID` 变化时递增票据并清空状态，
`useTaskContainers`、`useResumeEligibility`、`useTaskLiveSnapshot` 都按"序号 + 任务标识"丢弃迟到响应
——只有步骤选中态没有做任务归属判定。

**影响**：每次切换任务产生一次无意义且静默失败的请求；选中态短暂属于上一个任务，
与"切换任务后旧状态不得污染当前详情"的既有约束不一致。

**修复**：选中态改为 `{ stepID, taskID }`，只有 `taskID === task.id` 时才视为选中，
派生出的 `selectedStepID` 因此在新任务上立即为 `undefined`，请求不会再发出。
没有引入 `useEffect` 重置（那样仍会先发出一次请求），也没有给父组件加 `key`
（那会顺带改变无关子面板的挂载行为）。

### D6 步骤详情被任务事件时间线隔开

**代码证据**：修改前 `TaskExecutionPanel` 的渲染顺序是
`StepListPanel` → `EventTimelinePanel` → `StepDetailPanel`。事件时间线是**任务维度**的历史
（`useTaskEvents`，一次 20 条并支持"加载更多"），与"查看刚选中的步骤的尝试 / 验证 / 操作"无关。

**影响**：点开一个步骤后，必须滚动越过整段任务事件历史才能看到刚选中的步骤详情与全部操作入口
（执行、闭环、开启新执行周期、验证、初始化代码）。

**修复**：把 `EventTimelinePanel` 移到步骤详情之后。这是纯 JSX 顺序调整，可逆；
读取时机、分页、是否参与 5 秒刷新全部不变（仍由 `stepsToken` 驱动，仍不进入刷新循环）。
E2E 用 `boundingBox()` 断言步骤详情位于事件时间线上方。

---

## 5. 修改文件与行为变化

生产代码（全部位于 `lingting-agent-flow-ui/src/pages/tasks/`，未触碰 Go 与 HTTP 契约）：

| 文件 | 行为变化 |
|---|---|
| `TasksPage.tsx` | 工作流面板不再渲染第二份步骤列表，改为指向「计划步骤」的说明；详情读取失败标题改为"加载任务详情失败"；刷新失败标题按 `autoRefreshOn` 区分 |
| `TaskExecutionPanel.tsx` | 选中步骤与任务标识绑定；事件时间线移到步骤详情之后 |
| `status.ts` | 新增 `taskStatusLabel`、`WORKFLOW_STAGE_LABEL`、`workflowStageLabel` |
| `steps.ts` | 新增 `CODE_FETCH_STATUS_LABEL`、`codeFetchStatusLabel`；四处结论文本改用中文标签 |
| `launch.ts` | 启动结论的任务状态改用中文标签 |
| `retry.ts` | 重试结论里的执行记录状态改用中文标签 |
| `ResumeEligibilityPanel.tsx` | 工作流阶段改用中文标签 |

未修改：任何 Go 代码、HTTP 契约、`domain` 规则、任务状态机、恢复 / 重试资格、容器生命周期、
验证规则、数据库结构、前端依赖与 UI 框架。

---

## 6. 已核实但判定为合理、未修改的项

1. **代码保存记录在 `CodeSavePanel` 与 `ResumeEligibilityPanel` 各出现一次**：后者只展示
   `code_save_records` 作为 `can_resume=false` 的**阻塞证据**（与 `code_save_action` /
   `code_save_summary` 一起解释"为什么不能续跑"），并带"服务端给出的判定范围"说明；
   它不是第二份代码保存面板，删除会让阻塞原因失去证据。保留。
2. **`StepListPanel` 的 `validationStates` prop 从未被父组件传入**：属于既有死参数，
   按"原有死代码仅指出、不主动删除"的约定保留。若后续要让步骤列表直接显示
   "验证：通过 / 未通过"，需要先补数据来源，属于新功能而不是修缺陷。
3. **`OperationOutcomePanel` 的标题只显示步骤、不显示尝试标识**：`OperationTarget` 里已有
   `attemptID`，但结果面板每次只保留最近一次动作结论，不会同时出现两条同步骤结论，
   当前不构成误导；登记为后续候选。
4. **任务列表行内"启动"按钮禁用时没有原因提示**（`canStartTask` 只看
   `cancel_requested` / `status_finished` / 状态集合），而重试面板的禁用按钮有 tooltip。
   属于一致性问题而非错误信息问题，且列表行的可用文案空间有限；登记为后续候选。
5. **`refreshHint` 与操作结果四类提示**：自动刷新停止的 7 种原因各自有明确文案，
   操作结果按"受理 / 服务端拒绝 / 结果不确定 / 刷新失败"分别呈现，未发现需要调整的地方。

---

## 7. 测试与验证（实际执行）

| 命令 | 结果 |
|---|---|
| `pnpm test:unit:safe` | **通过**：27 个文件 / 589 个用例通过（本轮新增 7 个单元用例） |
| `pnpm test:e2e:safe` | **通过**：218 通过 / 0 失败（本轮新增 4 个 E2E 用例；`--workers=1`，遵守低资源约束） |
| `pnpm check` | **通过**（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） |
| `pnpm build` | **通过**（产物写入 `internal/webui/dist/`） |

未执行：Go 侧任何测试与 `go vet`（本轮未修改任何 Go 文件）；真实 SSH / Docker / Pi 环境验证
（本轮改动全部是前端展示与页面结构，不产生远程副作用）。

测试产物保留在操作系统临时目录，未在结束时删除：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\`（每次运行一个 `run-XXXXXX`
子目录，内含 `TEST_ARTIFACT_DIR` 与 Playwright 的 `playwright-output/`）。

新增 / 修改的断言，全部针对本轮缺陷：

- 单元：`task-execution-state.test.ts`（结论文本状态取值 4 例，含未知取值原样保留）、
  `task-launch-state.test.ts`（启动结论状态标签 + 未知取值 1 例）、
  `task-resume-eligibility.test.ts`（工作流阶段标签 2 例）、
  `task-retry.test.ts`（执行记录状态中文化，并入既有用例）。
- E2E：`task-progress-auto-refresh.spec.ts`（新增"自动刷新已停止时手动刷新失败"、
  "工作流面板不再重复列出步骤" 2 例）、`task-execution.spec.ts`（新增"切换任务后不再用上一个
  任务的步骤标识发起查询"）、`task-event-history.spec.ts`（新增"步骤详情出现在事件时间线之前"）；
  并同步了 5 处因标题/文案变化而需要更新的既有断言
  （`tasks.spec.ts`、`task-closeout.spec.ts`、`task-resume-eligibility.spec.ts`、
  `task-containers.spec.ts`、`task-launch-state` 与 `task-execution-state` 的单元断言）。

---

## 8. 未解决问题与后续候选

1. **`EventTimelinePanel` 仍在 `TaskExecutionPanel` 内部**：语义上它是任务级面板，与
   代码保存 / 续跑资格 / 容器 / 重试同级。本轮只做了"不阻断步骤流"的最小调整；是否上移到
   任务级区块需要同时决定"哪些面板属于任务级、哪些属于步骤级"，属于布局重构，
   不在本轮范围。
2. **步骤列表不显示验证状态**（`validationStates` 死参数）：需要先补数据来源。
3. **操作结果面板不区分尝试**：若将来允许并行诊断多个尝试，需要把 `attemptID` 纳入标题。
4. **重试 / 恢复 / 容器处置仍在详情区块最底部**：核实后认为它们属于"失败现场处置"，
   位于执行现场之后是合理的（用户先看现场再决定处置），但面板数量已达 9 个，
   是否需要折叠分组需要真实使用反馈，本轮不做推测性重排。
5. **创建任务表单字段收敛**、**私有仓库 Git 凭据**、**`verification_results` 遗留模型清理**：
   仍是 [`P4-G12-project-management.md`](./P4-G12-project-management.md) §13 记录的候选，
   本轮未触碰。

---

## 9. 与 P4-M5 的关系

P4-M5 要求"优化项有真实使用问题或可测量数据支撑"。本轮的 6 处缺陷全部是**可读性与结构**
问题，由代码本身即可证实（自相矛盾的提示、同一事实两种取值、被隔开的操作入口），
不涉及性能、并发、存储或架构；本轮没有产生任何可用于性能立项的数据，因此：

- **`P4-M5` 保持 `pending`**，未标记为完成，也未进行缺少数据的评估；
- 本轮不构成 P4 阶段结束：P4 仍只剩数据依赖型的 `P4-M5`。
