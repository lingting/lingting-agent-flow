# P4-M3-3 —— 任务最终结论与收尾闭环展示

> 实施依据：`master` HEAD `79aa4ad`（`:sparkles: Web 接入计划生成、步骤执行与验证修复操作`）
> 的实际代码核实与实施。
> 核实范围：`internal/httpapi/tasks.go`、`internal/domain/{task.go, task_closeout.go}`、
> `internal/app/task_closeout_view.go`；前端 `lingting-agent-flow-ui/src/lib/api/{types.ts,
> index.ts}`、`src/pages/tasks/{TasksPage.tsx, status.ts, steps.ts, StepDetailPanel.tsx,
> StepListPanel.tsx, TaskExecutionPanel.tsx}`、`tests/unit/`、`tests/e2e/`。
> 覆盖 P4-M1 缺口 **G10**（任务最终结论与收尾闭环状态）。
>
> **本轮未修改任何后端代码**：`TaskResponse` 的 `final_result` / `closeout` /
> `closeout_summary` 与 `domain.DecideTaskCloseoutView` 的派生规则已完整实现，缺口全部在前端。

## 1. M3-1 / M3-2 核验结论

任务要求先核验 M3-1、M3-2 是否已被 P4-M2-7 满足，避免重复实现。结论如下。

### 1.1 M3-1（步骤 / Attempt / Session 历史，含归档与导出状态）：**部分满足**

已有证据（`src/pages/tasks/StepDetailPanel.tsx`、`StepListPanel.tsx`）：

- 步骤列表来自 `GET /tasks/{id}/steps`，展示顺序、名称、类型、状态、验证状态、
  当前占用尝试与失败摘要（`StepRow`）。
- 尝试列表来自 `GET .../steps/{sid}/attempts`，`AttemptBlock` 展示
  `attempt_number`、`status`、`id`、`exit_code`、`started_at`、`verification_verdict`、
  `error`、`result_summary`。
- 会话列表来自 `GET .../attempts/{aid}/sessions`，`AttemptBlock` 展示会话标识、
  `agent_role` / `model`、`status`（含 `run_status`）与
  `归档 {archive_status} / 导出 {export_status}`，三者分列，不合并、不展示会话正文。
- 加载 / 失败（带重试）/ 空 / 有数据四态齐备；刷新失败保留旧数据并单独提示。

缺口（`P4-M1` §6 G7 验收标准第 1 条的字段级对照）：

1. 尝试的 `timed_out`、`cancelled` 未展示。接口 `AttemptResponse` 已输出这两个字段
   （`internal/httpapi/executions.go`），前端类型 `Attempt` 也已声明，但 `AttemptBlock`
   没有渲染。
2. 尝试的 `error_class` 与 `failure_stage` 未展示，同样已在类型中声明。
3. 会话的 `archive_status` / `export_status` 以服务端原始英文枚举值展示，
   没有与 `steps.ts` 中其他状态一致的字典化中文标签。

以上三项属字段级展示补全，不涉及接口或业务规则；按"不借本任务扩展范围"的约束，
本轮**只记录、不实现**，作为 M3-1 的剩余工作。

### 1.2 M3-2（验证历史、逐项检查结果与失败反馈）：**满足**

已有证据（`StepDetailPanel.tsx` 的 `ValidationRunBlock` / `FeedbackPanel`）：

- 验证历史来自 `GET .../steps/{sid}/validations`，展示流程状态、结论标签、通过 / 未通过 /
  跳过统计、尝试标识、开始时间、`failure_summary`、语义审查状态与摘要，默认只看最近两次、
  可展开全部。
- 逐项检查来自 `records[].checks[]`：`check_name`、必需 / 可选、状态标签、`exit_code`、
  `failure_reason`。`ValidationCheckResult` 类型同时覆盖 `certain`、`stdout_summary`、
  `stderr_summary`、`output_truncated`、`output_archive_error`。
- 失败反馈来自 `GET .../validations/{runID}/feedback`（P4-M2-5），展示逐项失败检查的
  必需标记、状态、退出码、超时 / 取消标记、`failure_reason`、`error_detail`、
  `stderr_summary`；`404` 明确呈现为"该轮验证没有失败反馈记录（验证通过，或反馈未写入）"，
  既不当作错误也不当作通过。
- 流程状态与结论分别展示：`validationStatusLabel(run.status)` 与 `DictTag` 的
  `verdict`，`inconclusive` 映射为警告色"无法判定"，不会被读成通过。

因此 M3-2 无需重复实现，其前端视图已由 P4-M2-7 交付。

## 2. M3-3 实现

### 2.1 真实契约核实

`internal/httpapi/tasks.go` 的 `TaskResponse`：

| 字段 | JSON | Go 类型 | 是否可缺省 | 语义 |
|---|---|---|---|---|
| `FinalResult` | `final_result,omitempty` | `string`（`task.FinalResult`，即 `domain.TaskResult`） | 是（空字符串不输出） | 任务最终业务结论，空 = 未提供 |
| `Closeout` | `closeout` | `string`（`domain.TaskCloseoutState`） | 否（无 `omitempty`） | 派生的收尾闭环结论 |
| `CloseoutSummary` | `closeout_summary,omitempty` | `string`（`closeout.Summary()`） | 是 | 收尾结论的可读说明 |

派生规则唯一来源为 `domain.DecideTaskCloseoutView`（`internal/domain/task_closeout.go`），
前端不复制任何规则，也不从 `status` 推断结论。

### 2.2 前端类型（`src/lib/api/types.ts`）

新增两个枚举类型并在 `Task` 上补齐三个字段：

- `TaskResult = "succeeded" | "failed" | "cancelled"`（对应 `domain.TaskResult` 的非空取值）。
- `TaskCloseoutState = "confirmed" | "pending" | "unconfirmed" | "inconsistent" | "cancelled"`
  （对应 `domain.TaskCloseoutState`）。
- `final_result?: TaskResult | (string & {})`：可缺省（服务端 `omitempty`）；保留 `string`
  分支，使服务端新增取值时仍能原样展示，不因类型假设隐藏真实数据。
- `closeout?: TaskCloseoutState | (string & {})`：正常情况下总是存在，但按可选处理，
  避免旧服务端或代理裁剪字段时崩溃。
- `closeout_summary?: string`：可缺省。

`src/lib/api/index.ts` 导出 `TaskResult` 与 `TaskCloseoutState`。

### 2.3 展示层（`src/pages/tasks/closeout.ts`，新增）

单一映射来源，只做"服务端事实 → 中文标签"，不含任何推断：

| 入口 | 职责 |
|---|---|
| `TASK_RESULT_META` | `succeeded` 成功（success）、`failed` 失败（error）、`cancelled` 已取消（default） |
| `TASK_CLOSEOUT_META` | `confirmed` 已确认收尾、`pending` 收尾进行中、`unconfirmed` 收尾未闭环、`inconsistent` 收尾不一致需人工核验、`cancelled` 已按取消结束 |
| `taskResultDisplay(result, closeout)` | 缺省 → "未提供最终结论"；未知 → 原样返回原始值；`succeeded` 且 `closeout !== confirmed` → 降级为警告色 |
| `closeoutDisplay(closeout)` | 缺省 → "未提供收尾结论"；未知 → 原样；`pending` 标记为需关注 |
| `closeoutSummary(task)` | 优先服务端 `closeout_summary`，缺省返回中性提示"服务端未提供收尾说明" |
| `conclusionNeedsAttention` / `conclusionWarning` | 只在需要关注时生成警示文案，逐条陈述事实，末尾固定"该任务不得当作已通过验收" |

`isKnownTaskResult` / `isKnownCloseout` 用 `Object.hasOwn` 判定，未知取值一律走"原始值"
分支，避免 `DictTag` 因字典缺项渲染为空。

### 2.4 任务详情（`src/pages/tasks/TasksPage.tsx`）

`TaskDetail` 由单个 `Descriptions` 扩展为"状态 + 结论 + 收尾"三行独立事实，并在下方按需
给出 `Alert` 警示：

| 行 | 内容 |
|---|---|
| 任务状态 | `DictTag` 的状态标签 + 固定说明"状态只表达业务阶段，不代表最终结论" |
| 最终业务结论 | `taskResultDisplay` 的中文标签（含颜色）；未知取值同时展示"服务端原始取值：…" |
| 收尾闭环 | `closeoutDisplay` 的中文标签（含颜色）+ 第二行 `closeoutSummary(task)` |
| 警示 | `conclusionWarning(task)`；`inconsistent` 用 error 级标题"收尾结论不一致，需要人工核验"，其余用 warning 级"任务尚未确认完成" |

其余字段（描述、关联项目、各时间、取消请求、错误摘要）保持原样，未重排布局、未改列表。

任务列表未新增概览列：现有列已含状态与时间，`closeout` 属于进入详情才能判断的结论，
按"不顺手重做表格"的约束保持现状。

## 3. 三个概念没有被混同的证据

| 场景 | 展示结果 | 覆盖用例 |
|---|---|---|
| `completed` + `succeeded` + `confirmed` | "成功"（success）+ "已确认收尾"（success），无警示 | 单元 + E2E 用例 1 |
| `completed` + 无 `final_result` + `unconfirmed` | "未提供最终结论"（secondary）+ "收尾未闭环"（warning）+ 警示含"没有提供最终业务结论" | 单元 + E2E 用例 2 |
| `completed` + `succeeded` + `unconfirmed` | "成功"但降级为 warning 色 + "收尾未闭环" + 警示含"收尾没有确认完成""尚未闭环" | 单元 + E2E 用例 3 |
| `completed` + `succeeded` + `inconsistent` | "收尾不一致，需人工核验"（error）+ error 级警示"必须人工核验" | 单元 + E2E 用例 4 |
| `succeeded` + `pending` | "收尾进行中"（processing）+ 警示含"收尾尚未结束" | 单元 + E2E 用例 5 |
| `failed` / `cancelled` | "失败"（error）/ "已取消"（default），不出现"成功"文案 | 单元 + E2E 用例 6、7 |
| 字段缺失 / 未知枚举 | "未提供最终结论"、"未提供收尾结论"、"服务端未提供收尾说明" / 原始值 + "未在当前版本中定义" | 单元 + E2E 用例 8、9 |
| 详情接口 404 | 只展示"操作失败"+ 服务端 message，`任务详情` 不出现 | E2E 用例 10 |

## 4. 改动文件

前端修改：

| 文件 | 改动 |
|---|---|
| `lingting-agent-flow-ui/src/lib/api/types.ts` | 新增 `TaskResult`、`TaskCloseoutState`；`Task` 补 `final_result` / `closeout` / `closeout_summary` |
| `lingting-agent-flow-ui/src/lib/api/index.ts` | 导出两个新类型 |
| `lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx` | `TaskDetail` 拆成状态 / 结论 / 收尾三行并加警示 `Alert` |

前端新增：

| 文件 | 职责 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/closeout.ts` | 结论与收尾的展示元数据、缺省与未知取值处理、警示文案 |
| `lingting-agent-flow-ui/tests/unit/task-closeout.test.ts` | 27 个单元用例 |
| `lingting-agent-flow-ui/tests/e2e/task-closeout.spec.ts` | 10 个浏览器用例 |

后端：**无改动**。

## 5. 测试结果（实际执行）

| 命令 | 结果 |
|---|---|
| `npx vp test run`（全量前端单元测试） | **通过**：15 个文件 / 218 个用例（其中 `task-closeout.test.ts` 27 个） |
| `npx playwright test tests/e2e/task-closeout.spec.ts` | **通过**：10/10 |
| `npx playwright test`（全量前端 E2E） | **80 通过 / 3 失败**，失败项见 §6 |
| `npx tsc --noEmit` | 通过（无输出） |
| `npx vp lint`（含 `vp fmt`、`vp lint --fix`、`tsc --noEmit`） | 通过（无输出） |
| `pnpm build`（`scripts/build-webui.mjs`） | 通过；仅有既有的 chunk 体积告警 |

`TEST_ARTIFACT_DIR`：每次运行由 `tests/internal/artifacts.ts` 生成独立随机目录
（`<OS 临时目录>/lingting-agent-flow-ui-tests/run-*`），运行结束后保留。

## 6. 失败项、未执行项与剩余风险

### 6.1 全量 E2E 的 3 个失败项（既有问题，已基线对照）

1. `tests/e2e/layout.spec.ts:43` `/machines 可访问且导航高亮、页面标题一致`
2. `tests/e2e/layout.spec.ts:64` `刷新后路由与导航状态保持`
3. `tests/e2e/tasks.spec.ts:478` `详情返回 404 时展示错误而不是伪造任务`

基线对照：`git stash push -u -- lingting-agent-flow-ui` 移除本轮全部前端改动后，
执行 `npx playwright test tests/e2e/layout.spec.ts tests/e2e/tasks.spec.ts`
→ **3 failed / 23 passed**，失败用例与失败断言与本轮完全相同；随后 `git stash pop` 恢复。
本轮未修改这三个用例涉及的代码路径（`layout`、详情 404 分支的错误提示链路），也未修复它们。

### 6.2 未执行项

- 未运行任何 Go 测试（本轮未修改后端代码）。
- 未做真实环境验证：没有启动 `agentflow server` + 真实 SQLite / SSH / Docker / Pi，
  全部浏览器测试都在网络层拦截 API。真实任务产生的 `closeout` 派生结论未在本轮被观察。
- 未做真实远程 Docker / SSH 验证（与本任务无关）。

### 6.3 剩余风险

1. **`closeout` 由服务端派生，前端只展示**：若服务端派生规则变更（例如新增取值），
   前端会走"未知取值原样展示"分支，语义不会错，但中文标签缺失，需要同步
   `TASK_CLOSEOUT_META`。这是刻意取舍：宁可展示原始值，也不猜测为已确认。
2. **任务列表没有结论概览**：`closeout != confirmed` 的任务在列表中仍只显示任务状态，
   用户必须进入详情才能发现"状态已完成但收尾未闭环"。是否增加列表列属产品取舍，
   本轮按范围约束未做。
3. **M3-1 的字段级缺口仍在**（`timed_out`、`cancelled`、`error_class`、`failure_stage`
   未渲染，会话归档 / 导出状态未字典化），见 §1.1。
4. **详情接口失败时不保留上次结论**：`openDetail` 失败会清空 `selected`，页面只显示错误。
   这符合"不伪造结论"的要求，但用户会失去上一次已加载的结论展示。

## 7. 下一步

`P4-M3` 整体**未完成**。已完成的子项：

- **M3-2** 由 P4-M2-7 交付，本轮核实为满足（见 §1.2）。
- **M3-3** 本轮完成（见 §2、§3、§5）。

M3-1 仍为部分满足（见 §1.1），剩余工作是把尝试级的 `timed_out` / `cancelled` /
`error_class` / `failure_stage` 与会话归档 / 导出状态的中文标签补齐。

其余子项按
[`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md) 推进：

- **M3-4** 续跑资格与阻塞原因展示（`resume-eligibility`，覆盖 G11）。
- **M3-5** 代码保存三阶段状态展示（`GET /tasks/{taskID}/code-saves`，覆盖 G8）。
- **M3-6** 工作流步骤起止时间补全（需先修 `workflowStatusResponse` 未赋值，覆盖 G18）。
- **M3-7** 进度自动刷新（覆盖 G19）。
- **M3-8** 事件时间线与修复周期历史（需先补 API，覆盖 G15、G16）。

M3-6、M3-8 需要先补后端能力，其余为纯前端展示。建议先做 M3-5（接口已就绪、只读、
不涉及长请求），再做 M3-4，最后处理需要后端改动的 M3-6 / M3-8。
