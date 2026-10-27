# P4-M3-7：任务执行进度自动刷新（G19）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M3`（展示阶段进度、验证结果、失败原因和恢复状态）
- **覆盖缺口**：G19「无需手动点击即可看到进度推进」
- **范围**：仅前端。未修改任何 Go 业务逻辑、HTTP API 或状态机。

---

## 1. 实施前后的 HEAD SHA

| 阶段 | 分支 | HEAD SHA |
|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致，工作区干净） | `37eff6cdee7a4da149458639a44252c05fb85856` |
| 实施后 | `master`（改动未提交，工作区含本项目修改） | `37eff6cdee7a4da149458639a44252c05fb85856` |

开始前执行 `git fetch origin --prune`，`git rev-parse origin/master` 与 `git rev-parse HEAD`
均返回 `37eff6cd`，因此本轮实施基于该提交，未发生上游漂移。本轮改动未提交，所以实施后
HEAD 未变，改动全部位于工作区（见 §4 文件清单）。

---

## 2. 刷新范围与活动状态判定规则

### 2.1 刷新范围

自动刷新只覆盖**当前打开的任务详情及其执行进度**：

| 数据 | 是否自动刷新 | 依据接口 |
|---|---|---|
| 任务状态、开始 / 结束时间、错误摘要、最终业务结论、收尾闭环 | 是（每轮） | `GET /api/v1/tasks/{taskID}` |
| 工作流运行状态、当前步骤、已完成 / 总步数、停止原因、`code_saver_available` | 是（每轮） | `GET /api/v1/tasks/{taskID}/workflow` |
| 步骤列表、步骤状态 | 条件刷新 | `GET .../steps` |
| 当前步骤的 Attempt / Session / 验证历史 | 条件刷新 | `GET .../steps/{stepID}/attempts`、`.../sessions`、`.../validations` |
| 代码保存记录 | 条件刷新 | `GET .../code-saves` |
| 续跑资格与阻塞原因 | 条件刷新 | `GET .../resume-eligibility` |

任务列表（`ExTable`）**不**逐行轮询：列表仍只在用户操作后按需 `reload()`。

后三项不按固定周期无条件请求，理由与依据：

- **步骤相关数据**：只在任务状态、`run.revision`、`run.status`、`run.current_step_id`、
  `run.current_attempt_id`、`run.completed_steps`、`run` 是否存在这七项中**任意一项真的
  变化**时才递增令牌。`revision` 是 `WorkflowRun` 的乐观并发版本号
  （`internal/domain/workflow_run.go`、`httpapi.WorkflowRunResponse.revision`），编排器每次
  推进都会写入新版本，因此它是"步骤现场可能变化"的可靠代理信号。
- **代码保存记录**：只在 `run.stage ∈ {code_save, finalize}` 或 `task.status == saving`
  之一成立、并且该阶段相对上一快照发生变化时刷新一次。这三个值是
  `domain.WorkflowStage` 中与代码保存相关的全部取值。
- **续跑资格**：该查询会 `InspectContainer` 核验远程容器
  （`internal/app/resume_eligibility.go` 的 `containerBlocking`），成本明显高于其余接口，
  因此只在**首次获得「停止自动刷新」结论**时刷新一次——那正是用户最需要看到阻塞原因的
  时刻，而不是执行期间每 5 秒触发一次远程核验。

### 2.2 活动状态判定（附领域代码证据）

判定实现：`lingting-agent-flow-ui/src/pages/tasks/autoRefresh.ts` 的 `decideAutoRefresh`。
它只读取服务端返回值，不推断语义：

| 服务端状态 | 判定 | 领域证据 |
|---|---|---|
| `WorkflowRunStatus = running` | 持续刷新 | `internal/domain/workflow_run.go` `IsActive()` 包含 `running` |
| `WorkflowRunStatus = cancel_requested` | 持续刷新 | 同上 `IsActive()` 包含 `cancel_requested` |
| `WorkflowRunStatus ∈ {completed, cancelled, failed}` | 停止（`workflow_terminal`） | `workflow_runStatusMachine.terminal` 恰好是这三个 |
| `WorkflowRunStatus ∈ {blocked, recovery_required}` | 停止（`awaiting_manual_action`） | 二者**不在** `terminal`，也**不在** `IsActive()`：自动推进已停止、等待人工处置 |
| `WorkflowRunStatus = pending` | 启动窗口（有限轮） | `IsActive()` 不含 `pending`，但它不是"已结束"，不能当作终态 |
| 无执行记录 + `TaskStatus.IsActive()` | 启动窗口（有限轮） | `internal/domain/task.go` `IsActive()` = preparing/planning/planned/developing/verifying/saving/retrying/cancelling |
| 无执行记录 + 其他任务状态 | 停止（`not_started`） | `queued`、`paused`、`recovery_required`、`completed`、`failed`、`cancelled` |
| **未知**任务状态或工作流状态 | 停止（`unknown_*`） | 保守处理：既不当活动态，也不当终态，更不触发自动操作 |

前端 `status.ts` 的 `WORKFLOW_RUN_STATUS_META.terminal` / `.active` 与 `TASK_STATUS_META.active`
是上述领域判断的镜像，两处取值集合一致（`status.ts` 的注释已声明该对齐关系）。

`blocked` / `recovery_required` **不是**终态，因此停止的是"高频轮询"而不是"把任务显示为
结束"：页面照常展示其真实状态与停止原因，并提示可手动刷新；用户手动恢复工作流后，
下一轮读取会得到 `running`，自动刷新自动恢复（有单元测试覆盖）。

---

## 3. 周期、后台行为、并发与清理策略

| 项 | 取值 / 行为 |
|---|---|
| 刷新周期 | `AUTO_REFRESH_INTERVAL_MS = 5000`，集中在 `autoRefresh.ts` 定义 |
| 调度方式 | 递归 `setTimeout`：上一轮结算后才排定下一轮，不使用固定间隔 `setInterval` |
| 启动窗口 | `AUTO_REFRESH_STARTUP_MAX_ROUNDS = 12`（12 × 5 秒）；首次读取失败时按 `AUTO_REFRESH_STARTUP_RETRY_ROUNDS = 3` 重试 |
| 并发 | 任意时刻同一任务最多一轮在途：`useTaskLiveSnapshot` 的在途去重 + 调度器 `refreshing` 标志；手动刷新复用同一入口 |
| 手动刷新 | 清掉待执行定时器后立即读取，结算后用 `scheduleNext()` 接回节奏（单元测试覆盖"手动刷新后不会悄悄停掉"） |
| 页面隐藏 | `visibilitychange` → `setVisible(false)` 取消待执行定时器，暂停自动请求 |
| 恢复前台 | `setVisible(true)` 立即刷新一次，不等待完整周期 |
| 卸载 / 关闭详情 | `useEffect` 清理：移除监听器、`scheduler.stop()`（清定时器 + 中止在途轮 + 作废请求票据）、清空在途引用 |
| 过期响应 | `requestGate.ts` 的「序号 + 任务标识」票据：只有最后一次发起、且仍属于当前详情任务的响应才会被采用；切换任务/关闭详情时序号与任务标识同时失配 |
| 刷新失败 | 保留上一次成功快照，只附加提示；**不重新判定策略**，因此不会被误认为工作流终止，也不会加快请求频率 |

---

## 4. 涉及的文件与数据接口

新增：

| 文件 | 职责 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/autoRefresh.ts` | 刷新周期与启动窗口常量、`decideAutoRefresh` 纯函数判定、停止原因与提示文案、可注入假定时器的 `createRefreshScheduler` |
| `lingting-agent-flow-ui/src/pages/tasks/liveSnapshot.ts` | 一次读取结果的纯变换 `applySnapshotRound`：展示快照、失败项、刷新策略、子面板重载令牌 |
| `lingting-agent-flow-ui/src/pages/tasks/requestGate.ts` | 请求票据（序号 + 任务标识）与刷新失败文案 |
| `lingting-agent-flow-ui/src/pages/tasks/useTaskLiveSnapshot.ts` | 唯一自动刷新 Hook：按任务标识维护快照、串行化读取、`refreshNow` / `refreshTask`、监听 `visibilitychange` |

修改：

| 文件 | 改动 |
|---|---|
| `src/pages/tasks/TasksPage.tsx` | 详情/工作流快照改由 `useTaskLiveSnapshot` 提供；删除页面自建的 `loadWorkflow`、`refreshTaskDetail` 双请求实现；新增自动刷新状态提示、「刷新进度」按钮、「自动刷新失败」提示；详情区域按"已取到任务或正在加载"渲染 |
| `src/pages/tasks/useExecutionRecords.ts` | `useLoader` 增加外部刷新 `token`；`useTaskSteps`、`useStepRecords`、`useTaskCodeSaves` 接受可选 `token` |
| `src/pages/tasks/useResumeEligibility.ts` | 接受可选 `token`（沿用既有快照归属与请求序号机制） |
| `src/pages/tasks/TaskExecutionPanel.tsx` | 新增 `stepsToken` 透传 |
| `src/pages/tasks/CodeSavePanel.tsx` | 新增 `token` 透传 |
| `src/pages/tasks/ResumeEligibilityPanel.tsx` | 新增 `token` 透传 |

数据接口（全部为既有只读 GET，未新增任何 API）：
`GET /api/v1/tasks/{taskID}`、`GET /api/v1/tasks/{taskID}/workflow`、
`GET /api/v1/tasks/{taskID}/steps`、`GET .../steps/{stepID}/attempts`、
`GET .../attempts/{attemptID}/sessions`、`GET .../steps/{stepID}/validations`、
`GET /api/v1/tasks/{taskID}/code-saves`、`GET /api/v1/tasks/{taskID}/resume-eligibility`。

---

## 5. 新增测试覆盖

### 5.1 单元测试（`tests/unit/task-progress-auto-refresh.test.ts`，35 个）

| 要求 | 覆盖用例 |
|---|---|
| 只有真实活动状态才自动刷新 | 「running 与 cancel_requested 是活动态，持续刷新」 |
| 终态 / 等待人工状态停止高频轮询 | 「completed、cancelled、failed 是终态」「blocked 与 recovery_required 等待人工处置」 |
| 未知状态不误判成功、不触发自动操作 | 「未知工作流状态既不算活动态也不算终态」「未知任务状态保守停止刷新」 |
| 同一任务不会有两轮未结束请求 | 「上一轮未结算时不排定下一轮」「手动刷新与自动刷新并发时不产生第二轮请求」 |
| 切换任务后迟到响应不覆盖新任务 | 「只有最后一次发起且仍属于当前任务的响应会被采用」「切换任务后旧任务的响应被丢弃」「被取消的请求即使是最新一次也不采用」 |
| 关闭详情 / 卸载后清理定时器与监听 | 「stop 清理定时器并中止在途一轮」「未开始时不排定任何定时器」（监听器清理由 E2E「关闭详情后自动刷新停止」覆盖） |
| 页面隐藏暂停、恢复前台立即刷新 | 「页面隐藏时暂停排定，恢复可见时立即刷新一次」 |
| 刷新失败保留快照 | 「刷新失败保留上一次成功快照且不改变刷新策略」「工作流进度查询失败不清空任务详情」 |
| 刷新失败不被误认为终止 | 同上（断言 `policy === undefined`、`dirty === {}`） |
| 手动刷新与自动刷新不产生错误覆盖 | 「手动刷新与自动刷新并发时不产生第二轮请求」「手动刷新结算后自动刷新节奏被接回」 |
| 自动刷新只读取、不调用执行接口 | E2E「自动刷新只读取状态，不触发任何执行类接口」（`writes` 必须为空） |
| 操作后既有刷新机制继续有效 | E2E「手动刷新仍然有效」「手动刷新之后自动刷新仍在继续」 |
| 只有真实字段变化才重载子面板 | 「只在步骤相关字段变化时要求重新加载步骤」「进入代码保存阶段时才要求重新加载代码保存记录」「获得停止刷新的结论时才刷新一次续跑资格」 |

时序验证使用注入的假定时器（`TimerApi`）与可手动结算的 Promise，没有真实等待、没有随机 sleep。

### 5.2 E2E 测试（`tests/e2e/task-progress-auto-refresh.spec.ts`，11 个）

| 要求 | 用例 |
|---|---|
| 活动态下后续快照更新当前步骤与进度 | 「工作流推进后自动更新当前步骤与进度」 |
| 终态后不再按活动态周期请求 | 「工作流进入终态后不再按活动态周期请求」（等待两个周期后请求数不再增长） |
| A → B 切换后 A 的迟到响应不覆盖 B | 「从任务 A 切到任务 B 后 A 的迟到响应不覆盖 B」 |
| 刷新失败保留展示并提示 | 「刷新失败保留最后一次成功展示并给出明确提示」 |
| 手动刷新仍然有效 | 「手动刷新仍然有效，并按服务端最新快照更新」+「手动刷新之后自动刷新仍在继续」 |
| 不触发启动 / 恢复 / 执行 / 验证 / 保存 | 「自动刷新只读取状态，不触发任何执行类接口」（拦截全部非 GET 请求） |
| 关闭详情后请求停止 | 「关闭详情后自动刷新停止」 |
| 未知状态不被解释为活动态 / 终态 | 「未知工作流状态不会被当作活动态或终态」 |
| 等待人工处置时停止并提示 | 「blocked 等待人工处置时停止自动刷新并如实提示」 |
| 任务字段随刷新更新 | 「任务开始时间等字段随自动刷新更新」 |

全部用例通过 Playwright 路由模拟真实 DTO，未启动真实 Go 服务、Docker 或模型服务。

---

## 6. 实际执行的命令与结果

全部在 `lingting-agent-flow-ui/` 下执行；worker 上限未提高（单元 `--maxWorkers 1`，
E2E `--workers=1`）。

| 序号 | 命令 | 结果 | 产物目录 |
|---|---|---|---|
| 1 | `npx vitest run tests/unit/task-progress-auto-refresh.test.ts --maxWorkers 1` | **35 通过 / 0 失败** | `C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-dpUnKT` |
| 2 | `pnpm test:unit:safe` | **361 通过 / 0 失败**（20 文件） | 每个 worker 独立 `run-XXXXXX` 目录，最后一轮见 `Test Files 20 passed` |
| 3 | `npx playwright test tests/e2e/task-progress-auto-refresh.spec.ts --workers=1` | **11 通过 / 0 失败** | `run-<随机>` 下的 `playwright-output/` 与 `playwright-report/` |
| 4 | `pnpm test:e2e:safe` | **129 通过 / 0 失败 / 0 跳过**（原 2 个既有失败已按 §7.2 处理） | `C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<随机>` |
| 4b | `npx playwright test tests/e2e/layout.spec.ts --workers=1` | **12 通过 / 0 失败**（§7.2 处理后复核） | `run-<随机>` 下的 `playwright-output/` |
| 5 | `pnpm check`（`vp fmt && vp lint --fix && tsc --noEmit`） | **通过**，无 error / warning | — |
| 6 | `pnpm build` | **通过**，产物写入 `internal/webui/dist`（`index.html` 0.44 kB、CSS 10.03 kB、JS 2014.45 kB） | `internal/webui/dist/` |

未执行项：Go 侧任何测试、`go vet`、`gofmt`——本轮未修改任何 Go 代码。

中途的定向回归（`tasks.spec.ts` + `task-launch.spec.ts` 25 通过、
`task-execution.spec.ts` 相关 49 通过）已并入上表第 4 项的全量结果。

注：第 4 项为 §7.2 处理 layout 用例之后的最终全量结果；处理前的同一命令结果为
128 通过 / 2 失败（两个失败均为 §7.1 记录的既有问题）。

---

## 7. 基线失败对照证据与后续处理

### 7.1 实施期间的既有失败（已对照）

实施前基线（`37eff6cd`，工作区干净）跑出的既有失败共 3 个：

| 用例 | 失败原因 |
|---|---|
| `tests/e2e/layout.spec.ts:43` › `/machines 可访问且导航高亮、页面标题一致` | 断言正文仍是占位文案「执行机器管理将在后续步骤实现。」，但 `src/pages/machines/MachinesPage.tsx` 已是真实的执行节点管理页，该文案已不存在 |
| `tests/e2e/layout.spec.ts:64` › `刷新后路由与导航状态保持` | 同一处占位文案断言 |
| `tests/e2e/tasks.spec.ts:478` › `详情返回 404 时展示错误而不是伪造任务` | 详情加载失败时仍渲染空的详情区域 |

**对照方法**：`git stash -u` 清空本轮全部改动后，用同一入口
`npx playwright test tests/e2e/layout.spec.ts:43 tests/e2e/layout.spec.ts:64 --workers=1`
在未修改的基线上重跑，结果 **2 failed / 5 passed**，与实施期间一致（`git stash pop` 已恢复
改动）。两个 layout 失败与本任务的自动刷新逻辑无关：本轮只改动 `pages/tasks/` 下的文件，
同一份 layout 用例中的 `/tasks` 路由断言是**通过**的。

第三项（`tasks.spec.ts:478`）在实施过程中被本轮"详情首次加载失败不渲染空详情框"的调整
顺带修正，现已通过。

### 7.2 两个 layout 用例的处理（2026-10-27 追加）

两个 layout 失败经确认属于**测试与实现不同步**，与它们要验证的"布局"无关：
`/machines` 已从占位页升级为真实功能页，其正文取决于 `/api/v1/hosts` 的真实响应，而布局
用例不 stub 任何 `/api` 请求（`vite.config.ts` 把 `/api` 代理到未启动的 Go 服务，代理返回
502，页面如实显示"加载执行节点失败 服务返回错误状态 502"）。

按用户决定，两个用例的正文断言在此时点**移除，待后续补齐 machines 页的成功响应 stub 后再
恢复**，处理方式：

- 新增 `ROUTES_PENDING_TEXT = ["/machines"]` 与派生的 `ROUTES_WITH_TEXT`，并在代码注释中写明
  待恢复的条件与 `TODO`；
- 循环用例的**其余断言全部保留**：URL、导航高亮、页面标题、无横向溢出、无脚本错误——因此
  `/machines` 路由仍被完整验证，只是不再断言正文；
- 整条删除的是 `刷新后路由与导航状态保持` 用例（它唯一的独占价值就是该正文断言；URL、高亮
  与标题在其它用例中已覆盖）。

处理后的实测结果：`npx playwright test tests/e2e/layout.spec.ts --workers=1` →
**12 通过 / 0 失败 / 0 跳过**。

### 7.3 需要恢复的内容

后续实现 machines 页时需补：

1. `layout.spec.ts` 的 `ROUTES` 中 `/machines` 的期望正文改为该页真实文案（或改为断言由
   stub 数据渲染出的行内容）；
2. 为该页在布局用例中 stub `GET /api/v1/hosts` 的成功响应；
3. 删除 `ROUTES_PENDING_TEXT` 清单并恢复无条件的正文断言，同时补回"刷新后路由与导航状态
   保持"用例。

这三项**未在本轮执行**，属后续工作；不得把"用例已移除"理解为"布局行为已验证正文"。

---

## 8. 已知限制与剩余风险

1. **长请求期间仍无服务端推送**：`execute-steps`、`execute-and-validate` 等同步接口在一次
   HTTP 请求内完成，客户端在此期间只能看到"操作中"以及每 5 秒的后台快照。真正的流式进度
   需要服务端 SSE，超出本任务范围（P4-M2-7 §6 已记录）。
2. **停止原因依赖状态，不依赖时间**：若编排器已退出但工作流记录仍停在 `running`
   （例如进程崩溃、租约尚未过期），页面会按活动态继续每 5 秒刷新，直到服务端把记录收敛。
   这属于服务端恢复职责，前端不做超时推断——否则会把"仍在推进"误判为"已停止"。
3. **启动窗口是时间有界的**：`pending` 记录或"已进入执行阶段但尚无执行记录"的情形，
   最多自动刷新 12 轮（约 60 秒）。窗口用尽后停止并提示手动刷新，不据此判定任务失败。
   对于从 `queued` 到真正启动耗时更长的现场，用户需要手动刷新一次。
4. **续跑资格不在执行期间自动更新**：为避免每 5 秒触发一次远程容器核验，该面板只在获得
   "停止自动刷新"结论时自动刷新一次；执行期间的变化由「刷新续跑资格」手动入口覆盖。
5. **代码保存记录只在相关阶段刷新**：`saving` / `code_save` / `finalize` 之外的阶段不会自动
   重新读取该列表；手动「刷新代码保存」入口保留。
6. **任务列表不自动刷新**：按任务要求，列表行不建立独立定时器。任务列表的状态/更新时间仍
   依赖用户手动「刷新」或操作后的 `reload()`。
7. **未知状态保守停止**：服务端新增状态取值后，前端会停止自动刷新并提示手动刷新，直到前端
   补齐映射与判定。这是刻意的保守选择，避免把未知状态误判为活动态或终态。

---

## 9. 完成定义检查

| 验收项 | 证据 |
|---|---|
| 工作流推进时页面自动显示真实进度 | E2E「工作流推进后自动更新当前步骤与进度」+「任务开始时间等字段随自动刷新更新」 |
| 终态 / 阻塞后不再高频轮询 | 单元「终态停止」4 例 + E2E「终态后不再按活动态周期请求」「blocked 停止并提示」 |
| 活动状态定义有领域证据 | §2.2 逐状态对照 `IsActive()` / `terminal` / `TaskStatus.IsActive()` |
| 无重复轮询 | 单元「上一轮未结算时不排定下一轮」「手动与自动并发不产生第二轮」 |
| 无旧响应覆盖新任务 | 单元 3 例 + E2E A→B 切换用例 |
| 刷新失败不等于终止 | 单元「刷新失败保留快照且不改变策略」+ E2E 失败提示用例 |
| 只读，不触发自动操作 | E2E 拦截全部非 GET 请求，`writes` 为空 |
| 手动刷新保留并有效 | E2E 两个手动刷新用例 + 单元「节奏被接回」 |
| 关闭详情 / 卸载后清理 | E2E「关闭详情后自动刷新停止」+ 单元「stop 清理定时器」 |
| 测试、检查、构建全部如实记录 | §6；失败项在 §7 说明并附基线对照 |

结论：上表每一项都有可复核的代码与测试证据，因此 **M3-7 标记为 `completed`**。
