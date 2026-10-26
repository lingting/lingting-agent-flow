# P4-M3-4 续跑资格与阻塞原因展示

- **里程碑**：`P4-M3` —— 展示阶段进度、验证结果、失败原因和恢复状态
- **子任务**：`M3-4` —— 续跑资格与阻塞原因展示（覆盖缺口 G11）
- **状态**：`completed`（2026-10-26）
- **本轮改动范围**：仅前端（`lingting-agent-flow-ui/`）。**未修改任何 Go 代码**。
- **覆盖缺口**：G11（"看懂为什么不能继续，以及下一步该做什么"）

---

## 1. 后端查询接口与 DTO 的真实契约

### 1.1 路由与实现

`GET /api/v1/tasks/{taskID}/resume-eligibility` 是本轮唯一使用的接口，它在 P2-M5 就已存在，
本轮只做前端接入。

代码证据：

- 路径段与分发：`internal/httpapi/code_recovery.go` 的 `resumeEligibilitySegment`、
  `serveCodeRecovery`、`isCodeRecoveryPath`、`matchActionPath`。
- 处理器：`internal/httpapi/code_recovery.go` 的 `codeRecoveryHandler.serveEligibility`。
  - 只接受 `GET` / `HEAD`，其他方法返回 `405` + `Allow: GET, HEAD`；
  - 服务未初始化时返回 `503`（`"续跑资格服务未初始化"`），不伪造空结论；
  - `app.ErrWorkflowInvalidInput` / `domain.ErrInvalidValue` → `400`；
    `app.ErrWorkflowNotFound` / `storage.ErrNotFound` → `404`；其余 → `500`。
- 业务实现：`internal/app/resume_eligibility.go` 的 `ResumeEligibilityService.Get`，
  聚合任务、最近一次工作流执行记录、代码保存记录与容器前置条件。

### 1.2 响应字段（`httpapi.ResumeEligibilityResponse`）

| 字段 | 类型 | 语义与来源 |
|---|---|---|
| `task_id` | string | 任务标识 |
| `task_status` | string | `domain.TaskStatus` |
| `run` | 可选对象 | `WorkflowRunResponse`，最近一次执行记录；没有时**整个字段缺省** |
| `can_resume` | bool | 服务端唯一资格结论（`len(Blocking) == 0`） |
| `blocking` | string[] / null | 阻塞分类；`append(nil, …)` 在空时序列化为 `null` |
| `code_save_action` | string | `domain.CodeSaveResumeAction`：`proceed` / `verify` / `blocked` |
| `code_save_summary` | string | `domain.CodeSaveResumeDecision.Summary` |
| `code_save_records` | 对象数组 | 需要关注的代码保存记录（可能为空数组） |
| `next_actions` | string[] / null | 服务端建议文本；空时序列化为 `null` |
| `summary` | string | `resumeSummary` 生成的整体说明 |

`code_save_records[]`（`httpapi.ResumeEligibilityCodeSave`）字段：`record_id`、`task_branch`、
`commit_status`、`push_status`、`merge_status`、`persist_status`、`recovery_status`、
`action`、`error_class`（`omitempty`）、`summary`。

### 1.3 阻塞分类的真实取值

来自 `internal/app/resume_eligibility.go` 的 `blockingReasons` / `containerBlocking` /
`errBlockingClass`：

- `task_deleted`：`task.Deleted()` 为真，直接返回且不再追加其他阻塞项；
- `cancel_requested`：`task.CancelRequested`；
- `container_missing`：`task.ContainerID` 为空，或 `InspectContainer` 返回
  `storage.ErrNotFound`，或容器 `State == removed`；
- `container_detached`：`detail.Container.Detached`；
- `container_not_running`：`detail.Inspect` 为空或 `Running == false`；
- `container_unreachable`：`InspectContainer` 返回其他错误；
- `prerequisite_unmet`：`errBlockingClass` 的兜底分类；
- 代码保存判定给出的 `codeSave.ErrorClass`：`code_save_unconfirmed`（记录写入未确认）
  与 `code_save_indeterminate`（仍有未决副作用或已核验但 `unverifiable`）。

代码证据：`internal/domain/code_save_recovery.go` 的 `DecideCodeSaveResume` 与
`DecideTaskCodeSaveResume`（顺序固定为"写入未确认 → 禁止自动重放 → 需要核验 → 可以继续"），
以及 `domain.CodeSaveResumeProceed` / `CodeSaveResumeVerify` / `CodeSaveResumeBlocked`
三个动作常量。

### 1.4 只读边界

`ResumeEligibilityService` 不写任何状态、不触发恢复、不执行 Git 操作；但
`containerBlocking` 会经 `WorkflowContainerPort.InspectContainer` 核验真实容器，
因此该查询**不是纯本地状态计算**，不适合在每次渲染时反复调用（见 §4）。

---

## 2. 前端新增的类型、API 封装与展示组件

### 2.1 类型（`src/lib/api/types.ts`）

- `ResumeEligibilityResponse`：与 `httpapi.ResumeEligibilityResponse` 逐字段对应。
  - `task_status` 保留 `TaskStatus | (string & {})` 分支；
  - `blocking` 与 `next_actions` 显式声明为 `string[] | null`，因为服务端在空时输出
    `null`（`append(nil, …)`），不能假设一定是数组；
  - `run?: WorkflowRun` 保持可选，缺省即"服务端没有返回执行记录"。
- `ResumeEligibilityCodeSave`：`record_id` / `task_branch` / 三个 Git 阶段 /
  `persist_status` / `recovery_status` / `action` / `error_class?` / `summary`。
- `ResumeBlockingCode`：已核实的 9 个阻塞分类联合类型（仅用于注释与已知值判定，
  消费方仍接受任意字符串）。
- `CodeSaveResumeAction`：`proceed` | `verify` | `blocked`。

所有枚举字段都保留 `(string & {})` 分支，服务端新增取值时界面原样展示。

### 2.2 API 封装（`src/lib/api/endpoints.ts`、`index.ts`）

- `taskResumeEligibilityPath(taskID)`：复用 `taskPath` 的编码与空标识校验，
  空标识直接抛错，不拼出无效路径。
- `fetchTaskResumeEligibility(taskID, options)`：经 `apiRequest` 发出真实 `GET`，
  复用统一错误处理（`ApiError` 的 `http` / `network` / `timeout` / `aborted` /
  `invalid_response` 分类）。
- `index.ts` 导出上述函数与 `ResumeEligibilityResponse` / `ResumeEligibilityCodeSave` /
  `ResumeBlockingCode` / `CodeSaveResumeAction` 类型。

页面不直接调用 `fetch`，也不手工拼接 API 路径。

### 2.3 展示元数据（`src/pages/tasks/resumeEligibility.ts`）

| 导出 | 作用 |
|---|---|
| `RESUME_BLOCKING_META` / `resumeBlockingMeta` | 9 个已核实阻塞分类的中文标签与解释；未知分类原样返回代码并标注"未定义" |
| `CODE_SAVE_RESUME_ACTION_META` / `codeSaveResumeActionMeta` | `proceed` / `verify` / `blocked` 的标签、颜色与含义；未知与缺省不被回落到"允许继续" |
| `resumeVerdict(canResume)` | 把服务端布尔值翻译为结论文案（**只依据服务端结论**） |
| `resumeSummary` / `RESUME_SUMMARY_MISSING` | 摘要缺省时给中性提示，不编造结论 |
| `resumeBlockingList` / `resumeNextActions` | `null` 与缺失统一按空列表处理 |
| `looksLikeCliCommand` | 仅用于把 CLI 建议标注为"需在命令行执行"，不执行任何内容 |
| `resumeRecoveryScope` | 说明记录级核验结论只针对某一个阶段 |

### 2.4 加载状态（`src/pages/tasks/useResumeEligibility.ts`）

状态：`idle` / `loading` / `ready`（可带 `error`，表示刷新失败但保留旧快照）/ `error`。

状态迁移被抽成纯函数，便于直接验证竞态与保留旧数据的行为：

- `resumeSnapshotForTask(snapshot, taskID)`：快照按任务标识归属，跨任务返回 `undefined`；
- `resumeInitialState(snapshot, taskID)`：同任务旧快照继续展示，否则 `loading`；
- `resumeLoaded(taskID, data)`：成功时快照归属当前任务并清除旧错误；
- `resumeFailed(snapshot, taskID, error)`：同任务旧快照保留并附加错误，否则进入 `error`；
- `resumeResponseAccepted(requestID, latestRequestID, aborted)`：只采用最后一次请求的结果。

Hook 只在 `taskID` 变化或用户点击"刷新续跑资格"时发起请求，切换任务时立刻丢弃旧快照，
并用请求序号 + `AbortController` 丢弃迟到响应。

### 2.5 展示组件（`src/pages/tasks/ResumeEligibilityPanel.tsx`）

挂在任务详情下、代码保存面板之后（`TasksPage.tsx`）。由六部分组成：

1. **资格结论**：`Alert`（`success` / `warning`）+ 服务端摘要；固定说明"这是查询时刻的
   只读快照，真正执行恢复时服务端仍会重新检查前置条件"。
2. **任务与工作流状态**：`task_status`（已知走中文标签，未知原样展示）；`run` 存在时展示
   状态标签、`run_id`、阶段、进度计数、停止原因与更新时间；缺省时明确说明服务端未返回
   执行记录，并强调这不代表可以续跑。
3. **阻塞原因**：逐项渲染 `blocking`，每项一条中文标签 + 一句解释；未知分类显示原始代码。
4. **代码保存续跑判定**：`code_save_action` 的标签/颜色/含义 + `code_save_summary`；
   未知动作额外展示原始取值。
5. **需要关注的代码保存记录**：逐条展示 `record_id`、`task_branch`、记录级判定
   （`action` / `summary` / `error_class`），以及提交 / 推送 / 合并 / `persist_status` /
   `recovery_status` 五个独立状态。
6. **下一步动作**：逐项原文展示 `next_actions`，CLI 形态的建议额外标注"需由你在命令行
   显式执行；页面不会自动运行"。

代码保存状态标签复用 M3-5 已建立的 `codeSaves.ts` 映射：为避免两处各写一套渲染逻辑，
把 `CodeSavePanel.tsx` 内的 `StatusTag` 抽成共享组件
`src/pages/tasks/CodeSaveStatusTag.tsx`，两个面板共用同一套"已知走字典、未知原样展示"规则。

---

## 3. `can_resume`、`blocking`、代码保存判定与 `next_actions` 的显示规则

| 服务端事实 | 显示规则 | 不允许的做法 |
|---|---|---|
| `can_resume=true` | "当前查询结果允许继续推进" + "服务端在真正执行恢复前仍会重新检查前置条件" | 不自动恢复；不表述为"任务已完成" |
| `can_resume=false` | "当前存在阻塞，不能按当前条件直接续跑" + 逐项阻塞原因 | 不从任务状态推导资格；不隐藏阻塞 |
| `blocking` 已知分类 | 中文标签 + 具体解释（见 §1.3 的代码依据） | 不合并成一个"失败"标签 |
| `blocking` 未知分类 | 原始代码 + "未在当前版本中定义" | 不省略、不归并为普通失败、不伪装成无阻塞 |
| `code_save_action=proceed` | "允许继续代码保存" + "这不代表整个工作流已经可以恢复" | 不读成任务可恢复 |
| `code_save_action=verify` | "需先核验" + "这是「需要核验」，不是「已经核验」" | 不读成已核验 |
| `code_save_action=blocked` | "禁止自动推进" + 需人工介入 | 不自动重放 |
| `code_save_records[]` | 三个 Git 阶段 + `persist_status` + `recovery_status` 分别展示 | 不把记录级 `action` 当作三阶段都已核验 |
| `next_actions[]` | 原文逐条展示；CLI 形态标注需在命令行执行 | 不解析成自动指令、不自动执行 |
| `next_actions` 为空 / `null` | "服务端没有提供后续动作建议" | 不自行拼造确定性步骤 |

---

## 4. 未知状态、查询失败、旧数据保留与请求竞态

- **未知状态**：`task_status` 未知时展示原始取值并提示未定义（`isKnownTaskStatus`）；
  `run.status` 未知时不用 `DictTag` 兜底，直接展示原始值（`isKnownWorkflowRunStatus`）；
  阻塞分类、代码保存动作、三阶段状态与两个记录维度状态都走同一"未知原样展示"规则。
- **查询失败**：进入 `error` 状态，展示"查询续跑资格失败" + 服务端错误 + 重试按钮；
  **不会**显示"允许续跑"，也不会显示"没有阻塞"。
- **刷新失败**：保留上一次成功快照并单独提示"续跑资格刷新失败 …… 不是最新状态"；
  不清空旧结果、不伪装成"没有阻塞"。
- **请求竞态**：快照按任务标识归属，切换任务时旧快照立即失效；请求序号 + `AbortController`
  保证迟到响应不覆盖新结论；`reload` 不清除快照，因此刷新过程中不会闪成空态。
- **不自动轮询**：本轮不引入自动刷新（属 M3-7），只在打开详情与用户显式刷新时请求；
  资格查询可能核验远程容器，因此也不放在渲染路径上。

---

## 5. 与现有恢复按钮的职责边界

`TasksPage.tsx` 中既有的"恢复工作流"按钮仍由 `canResumeWorkflow(workflow?.run)` 判定，
本轮**未改动**该入口，也未新增第二个恢复按钮或第二条恢复调用链。

- 本轮的职责只是把服务端统一资格结果与阻塞原因展示出来；
- `canResumeWorkflow` 不被当作完整资格判定，页面也不会用资格结果绕过既有后端流程；
- 面板明确说明"查询结果不是后续操作的保证"；
- "恢复按钮可用性完全由 `resume-eligibility` 驱动"仍归属 P4-M4-4（本轮路线图未调整该归属）。

---

## 6. 改动文件

前端源码：

- `lingting-agent-flow-ui/src/lib/api/types.ts`：新增续跑资格类型（含 `null` 与未知取值分支）。
- `lingting-agent-flow-ui/src/lib/api/endpoints.ts`：新增
  `taskResumeEligibilityPath` / `fetchTaskResumeEligibility`。
- `lingting-agent-flow-ui/src/lib/api/index.ts`：导出新增函数与类型。
- `lingting-agent-flow-ui/src/pages/tasks/resumeEligibility.ts`（新增）：阻塞分类、代码保存
  动作、结论与摘要的展示元数据。
- `lingting-agent-flow-ui/src/pages/tasks/useResumeEligibility.ts`（新增）：独立加载状态与
  竞态处理，状态迁移抽成纯函数。
- `lingting-agent-flow-ui/src/pages/tasks/ResumeEligibilityPanel.tsx`（新增）：只读面板。
- `lingting-agent-flow-ui/src/pages/tasks/CodeSaveStatusTag.tsx`（新增）：从 `CodeSavePanel`
  抽出的共享状态标签渲染。
- `lingting-agent-flow-ui/src/pages/tasks/CodeSavePanel.tsx`：改用共享标签组件（行为不变）。
- `lingting-agent-flow-ui/src/pages/tasks/status.ts`：新增 `isKnownTaskStatus` /
  `isKnownWorkflowRunStatus`，用于区分"已知状态"与"需要原样展示的未知取值"。
- `lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx`：任务详情挂载
  `ResumeEligibilityPanel`（仅新增一行渲染，未改动既有操作逻辑）。

测试：

- `lingting-agent-flow-ui/tests/unit/task-resume-eligibility.test.ts`（新增，34 个用例）。
- `lingting-agent-flow-ui/tests/e2e/task-resume-eligibility.spec.ts`（新增，16 个用例）。
- `lingting-agent-flow-ui/tests/e2e/task-code-saves.spec.ts`：新增资格接口拦截桩，并把
  "代码保存记录"标题定位改为 `exact`（因为详情页新增了提到该词的面板说明）。

格式化带来的纯排版变更（`vp fmt`，无行为变化）：

- `lingting-agent-flow-ui/src/pages/tasks/closeout.ts`
- `lingting-agent-flow-ui/tests/unit/task-closeout.test.ts`
- `lingting-agent-flow-ui/tests/e2e/task-closeout.spec.ts`
- `lingting-agent-flow-ui/tests/unit/task-code-saves.test.ts`

---

## 7. 实际执行的测试命令与结果

所有命令在 `lingting-agent-flow-ui/` 下执行。

| 命令 | 结果 |
|---|---|
| `npx vp test run tests/unit/task-resume-eligibility.test.ts` | 通过：1 个文件 / 34 个用例 |
| `npx vp test run`（全量前端单元测试） | 通过：17 个文件 / 282 个用例 |
| `npx playwright test tests/e2e/task-resume-eligibility.spec.ts` | 通过：16/16 |
| `npx playwright test tests/e2e/task-code-saves.spec.ts` | 通过：14/14 |
| `npx playwright test`（全量 E2E） | 110 通过 / 3 失败（失败项经基线对照确认为既有问题，见 §8） |
| `npx tsc --noEmit` | 无错误 |
| `npx vp fmt`（格式化，仅改动本次涉及文件） | 已执行，无报错 |
| `npx vp lint` | 无错误 |
| `npx pnpm build`（即 `pnpm build`，前端生产构建） | 成功，产物写入 `internal/webui/dist/` |

测试产物目录（保留，未清理）：`TEST_ARTIFACT_DIR` 由每个测试入口在
`<OS 临时目录>/lingting-agent-flow-ui-tests/run-<随机名>` 下创建并打印；本次运行的样例路径为
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-PLgjS6`（全量 E2E）。

---

## 8. 未执行项、失败项与剩余风险

**未执行项**

- 未运行任何 Go 测试：本轮未修改任何 Go 代码，因此按仓库约定不触发 Go 单元与集成测试。
- 未做真实远程验证：没有连接真实 SSH / Docker / Pi，也没有在真实远程容器上验证恢复条件。
  E2E 全部在浏览器层拦截 API，只能证明前端对给定响应的处理。
- 未执行"恢复工作流"的真实调用验证：本轮只展示资格，不发起恢复。

**失败项（经基线对照确认与本次改动无关）**

- `tests/e2e/layout.spec.ts` 2 个用例：`/machines` 导航高亮与刷新后路由保持。
  `git stash -u` 后基线同样失败。
- `tests/e2e/tasks.spec.ts:478`"详情返回 404 时展示错误而不是伪造任务"。
  `git stash -u` 后基线同样失败（该用例断言 `getByText("任务不存在: task-1")`，实际页面
  未渲染该文案）。

**剩余风险**

1. `can_resume` 只代表查询时刻的结论。查询与用户点击恢复之间状态可能变化，服务端会在
   恢复前重新检查——这一点已在界面上明确说明，但前端无法消除该时间窗。
2. 资格查询会核验远程容器，失败时可能只是暂时不可达；界面按服务端给出的
   `container_unreachable` 展示，不做重试推断。
3. `blocking` 与 `next_actions` 为 `null` 的契约来自 Go 的 `append(nil, …)` 序列化行为。
   若后续服务端改为输出空数组，前端已同时兼容两种情况（`?? []`）。
4. 恢复按钮可用性仍由 `canResumeWorkflow` 判定，与 `can_resume` 可能不一致（例如
   `can_resume=false` 但工作流记录状态允许点击恢复）。这是 M4-4 的既定范围，本轮按任务
   要求不做改造；界面上没有把两者混为一谈。
5. 代码保存记录在资格响应中缺少 `stage` 字段，因此记录级 `recovery_status` 只能说明
   "只针对某一个阶段"，无法像 M3-5 面板那样指出具体阶段。

---

## 9. M3-4 退出条件逐项检查

| 完成定义 | 结论 | 证据 |
|---|---|---|
| Web 通过正式 API 客户端读取服务端续跑资格 | 满足 | `fetchTaskResumeEligibility` 经 `apiRequest` 发 `GET`；`useResumeEligibility` 调用；E2E 断言真实路径 `/api/v1/tasks/task-1/resume-eligibility` |
| `can_resume`、所有 `blocking`、代码保存判定、需关注记录、`next_actions`、`summary` 正确展示 | 满足 | `ResumeEligibilityPanel.tsx` 六段展示；单测覆盖各字段解析与标签；E2E 覆盖允许、阻塞、多阻塞项、未知分类、空 `next_actions`、空 `run` |
| 不使用前端推断替代服务端资格结论 | 满足 | `resumeVerdict` 只接收服务端 `can_resume`；面板未从 `task_status` 或 `run.status` 推导资格 |
| 未知字段值与未知阻塞代码不被隐藏或伪装 | 满足 | `resumeBlockingMeta` / `codeSaveResumeActionMeta` / `isKnownTaskStatus` / `isKnownWorkflowRunStatus` 均原样展示；单测与 E2E 各有用例 |
| 查询失败不显示默认可续跑；刷新失败不抹除已有结果 | 满足 | `state.status === "error"` 只渲染错误 + 重试；`resumeFailed` 保留同任务快照；E2E 两个用例分别验证 |
| 查询请求不触发恢复、核验或其他副作用 | 满足 | 只有 `GET`；E2E `recordWrites` 断言整个流程零写请求 |
| 新增测试覆盖正常、阻塞、未知、失败与请求竞态，并如实报告结果 | 满足 | 单元 34 + E2E 16；结果见 §7、§8 |
| 路线图、交付文档与实际代码一致 | 满足 | 本文件 + `README.md` / `ROADMAP.md` / `P4-product-experience-optimization.md` 同步更新 |
| P4-M3 整体保持真实状态 | 满足 | `P4-M3` 仍为 `in_progress`：M3-1 部分满足，M3-6 / M3-7 / M3-8 未开始 |

---

## 10. 下一步里程碑建议

按 `.steps/README.md` 的优先级规则与 `P4-product-experience-optimization.md` 的建议顺序：

1. **M3-1 的剩余字段级补全**：尝试的 `timed_out` / `cancelled` / `error_class` /
   `failure_stage` 渲染，会话的 `archive_status` / `export_status` 中文标签。属纯前端，
   依赖的字段已由现有接口返回。
2. **M3-7 进度自动刷新**：在 M3-4 已建立的独立加载状态基础上增加受控轮询，需要先明确
   "哪些请求可以自动轮询"（资格查询会核验远程容器，不应纳入高频轮询）。
3. **M3-6 工作流步骤起止时间**：需要先补后端 `workflowStatusResponse` 的字段赋值。
4. **M3-8 事件时间线与修复周期历史**：需要先补后端 API。
5. **P4-M4-4**：把恢复按钮的可用性改为完全由 `resume-eligibility` 驱动。本轮已把资格结论
   与阻塞原因完整暴露，为该项提供了展示基础，但按任务要求未改动恢复入口。
