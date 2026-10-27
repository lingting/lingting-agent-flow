# P4-M4-4：基于服务端续跑资格的工作流恢复操作（G11 操作侧）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M4`（支持必要的继续执行、取消、恢复和结果查看操作）
- **覆盖缺口**：G11「续跑阻塞原因不可见」的**操作侧**——资格已可见（M3-4），但恢复入口与
  服务端资格不一致
- **范围**：只补前端操作链路。**本轮未修改任何 Go 生产代码**，未新增 HTTP 接口，
  未改动工作流编排、续跑资格规则与执行前置条件。

---

## 1. 实施前的基线 SHA 与工作区状态

| 阶段 | 分支 | HEAD SHA | 工作区 |
|---|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致） | `baa4f0a8d39585041128801fdad25643f84b3209` | `git status --short` **为空**（无预先修改） |
| 实施后 | `master`（改动未提交） | `baa4f0a8d39585041128801fdad25643f84b3209` | 本轮新增 / 修改的前端与文档文件（见 §6） |

开始前执行 `git fetch origin master`，`origin/master` 与本地 `HEAD` 同为 `baa4f0a`，上游未漂移；
`git status --short` 为空，说明没有需要保留的他人改动。

核对的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`、
`.steps/P4-M4-1-task-retry-web-entry.md`、`.steps/P4-M4-2-code-save-verification-web-action.md`、
`.steps/P4-M4-3-container-delete-detach-web-actions.md`。

---

## 2. 实施前核实到的服务端契约与代码证据

### 2.1 资格查询（只读）返回哪些字段

`GET /api/v1/tasks/{taskID}/resume-eligibility` → `httpapi.ResumeEligibilityResponse`
（`internal/httpapi/code_recovery.go:68`），由 `app.ResumeEligibilityService.Get`
（`internal/app/resume_eligibility.go`）聚合：

| 字段 | 来源 | 说明 |
|---|---|---|
| `task_id` / `task_status` | `storage.GetTask` | 任务业务状态 |
| `run` | `ListWorkflowRunsByTask` 的第一条 | 缺省表示没有执行记录 |
| `can_resume` | `len(Blocking) == 0` | **唯一资格判据** |
| `blocking` | `blockingReasons` | `task_deleted`、`cancel_requested`、`code_save_*`、`container_missing/detached/not_running/unreachable`、`prerequisite_unmet` |
| `code_save_action` / `code_save_summary` | `domain.DecideTaskCodeSaveResume` | 任务级代码保存续跑判定 |
| `code_save_records` | `codeSaveEligibilityViews` | 只含仍有未决副作用 / 记录写入未确认的记录 |
| `next_actions` | `resumeNextActions` | CLI 建议原文 |
| `summary` | `resumeSummary` | 整体可读说明 |

### 2.2 当前恢复入口使用哪些条件启用（实施前）

`TasksPage.tsx` 的「工作流进度」操作条里有一个「恢复工作流」按钮：

```tsx
<Button
  disabled={!canResumeWorkflow(workflow?.run)}
  onClick={() => { const runID = workflow?.run?.run_id; if (runID && selected) void handleResumeWorkflow(selected.id, runID); }}
  text="恢复工作流" type="primary"
/>
```

`canResumeWorkflow`（`src/pages/tasks/status.ts`）是**前端本地的状态白名单**：

```ts
export function canResumeWorkflow(run?: WorkflowRun): boolean {
  if (!run) return false;
  if (run.cancel_requested) return false;
  return run.status === "blocked" || run.status === "recovery_required" || run.status === "pending";
}
```

### 2.3 `can_resume` / `blocking` / `next_actions` 是否已联动（实施前）

- 三者**已联动、但仅在展示层**：`ResumeEligibilityPanel` 如实展示 `can_resume` 与 `blocking`，
  `resumeEligibility.ts` 对未知分类与未知动作原样保留。
- 恢复**按钮**完全**不联动**：它读的是 `workflow.run.status`，与资格快照是两条并行数据源。

### 2.4 恢复 API 在执行时如何重新核验资格

`POST /api/v1/tasks/{taskID}/resume-workflow` → `httpapi.workflowHandler.resume`
（`internal/httpapi/workflow.go:248`）：

1. `claimForWorkflow` 用 `domain.ExplicitWorkflowStatuses()` 领取任务租约（事务 + 状态条件）；
2. `app.TaskWorkflowService.ResumeWorkflow`（`internal/app/task_workflow_run.go:241`）依次核验：
   `run.Status.CanStart()` → `run.CancelRequested` → `verifyPlanUnchanged` →
   `domain.CheckWorkflowStart` → `checkContainerReady` → `checkHostReady` →
   **`checkCodeSaveRecovery`**（`domain.DecideTaskCodeSaveResume`，未决副作用拒绝恢复）→
   `ensureGrant` → `checkTaskLease` → 进程内任务锁 → `beginRunRecord`（先落盘 running）→
   `appendEvent` → `launch`。

结论：服务端在恢复执行时**完整重新核验**全部前置条件，`can_resume` 只是查询时刻的快照，
不是执行授权。前端不需要、也不得复制这些规则。

### 2.5 当前是否存在「资格查询失败仍可发起恢复」的路径

**存在，且是本轮的主要缺口**。恢复按钮的可用性只看 `workflow?.run`：

- 资格查询失败（500 / 503 / 网络中断）时，只要工作流进度查询成功且 `run.status` 属于上述
  白名单，按钮就是**可用**的 —— 用户可以在资格完全未知的情况下发起恢复；
- 资格查询中（`loading`）时同理，按钮可能已可用；
- 资格快照显示 `can_resume: false`（例如 `container_missing`）时，只要 `run.status === "blocked"`，
  按钮仍是**可用**的 —— 展示区说"存在阻塞"，按钮却允许点击，两处结论互相矛盾。

### 2.6 当前是否存在任务切换、迟到响应或资格过期导致的误导

| 风险 | 实施前状态 |
|---|---|
| 旧任务资格污染新任务 | 已防护：`useResumeEligibility` 用 `taskID` + 请求序号票据丢弃迟到响应 |
| 恢复响应迟到污染新任务 | 已防护：`handleResumeWorkflow` 直接 `refreshNow()`，但**没有任务标识票据**，切换到别的任务后迟到的 `setActionError` 仍会写到新任务页面 |
| 资格过期被当成实时授权 | 未处理：面板会提示"是查询时刻的只读快照"，但按钮仍按 `run.status` 独立判断 |
| 刷新失败仍可用旧资格恢复 | 未处理：刷新失败时面板提示"不是最新状态"，按钮状态不受影响 |

---

## 3. 修改前的真实缺口（汇总）

1. **恢复入口与资格完全脱钩**：入口读 `WorkflowRun.status` 白名单，而服务端唯一判据是
   `can_resume`。这是"前端状态白名单复制服务端续跑规则"的典型实例。
2. **资格不可用时仍可恢复**：查询中、查询失败、快照不属于当前任务、刷新失败——四种情况都
   不阻止点击。
3. **恢复结果语义扁平**：`await resumeTaskWorkflow(...)` 后只做 `refreshNow()`，成功时不展示
   真实响应（执行记录、状态、进度），失败时统一塞进页面顶部的 `actionError`；没有区分
   「请求被拒绝」「结果无法确认」「已受理」；超时/中断没有提示且没有禁止自动重发的说明。
4. **恢复操作缺少防重与竞态票据**：同一帧连续点击不会因为 `state` 未及时更新而被拦截。
5. **面板与按钮各维护一套状态**：面板说"存在阻塞"，按钮说"可以恢复"。

---

## 4. 联动设计

### 4.1 单一状态来源

```
TasksPage
  └─ ResumeEligibilityPanel(onRefreshTask=refreshTaskDetail)
       └─ useResumeEligibility({ taskID, token: tokens.resume, refresh: onRefreshTask })
            ├─ state      : ResumeEligibilityState（资格快照 + 加载/错误）
            └─ operation  : ResumeOperationState（running / outcome / failure / uncertain / refreshError）
```

- **删除**了「工作流进度」操作条里的恢复按钮，以及 `status.ts` 的 `canResumeWorkflow`。
- **删除**了 `TasksPage` 的 `handleResumeWorkflow` 与 `resumeTaskWorkflow` 导入。
- 恢复入口**只**存在于 `ResumeEligibilityPanel`，与它自己展示的资格快照同源，结构上不可能
  再出现两套矛盾结论。
- 未新增第二个资格 Hook、未新增第二个自动刷新循环；`tokens.resume` 仍由既有
  `useTaskLiveSnapshot` 按"停止自动刷新"时递增一次。

### 4.2 可提交性判定（纯函数，`resumeAction.ts`）

`resumeSubmitBlockReason` 的判定顺序固定，任一命中即禁用：

| 顺序 | 原因 | 触发条件 |
|---|---|---|
| 1 | `running` | 恢复请求在途 |
| 2 | `eligibility_loading` | 资格查询中 |
| 3 | `eligibility_error` | 资格查询失败且无可用快照 |
| 4 | `no_eligibility` | 从未成功取得快照 |
| 5 | `stale_snapshot` | 快照的 `task_id` ≠ 当前任务 |
| 6 | `stale_snapshot` | 最近一次刷新失败（快照已过期） |
| 7 | `blocked` | `can_resume === false` |

**没有**任何一条读取 `TaskStatus`、`WorkflowRunStatus`、`closeout`、`code_save_action`。
另外还要求资格快照带 `run.run_id`：缺失时禁用入口并在 tooltip 说明"服务端未返回执行记录标识，
无法构造恢复请求"，不猜测标识。

### 4.3 恢复执行

- 请求体只含 `{ run_id }`，`run_id` 来自资格快照，不接受任何客户端拼装。
- 服务端拒绝：展示 `RESUME_FAILED_TITLE` + 服务端原文，**不**依据先前 `can_resume: true` 显示成功。
- 超时 / 网络中断 / 取消：额外展示 `RESUME_UNKNOWN_OUTCOME_HINT`（明确"无法确认、先重新查询、
  页面不会自动重复提交"），**不自动重发**。
- 请求结束后无论成败都调用父组件 `refresh()` 刷新任务详情与工作流进度；刷新失败只追加
  `refreshError` 提示，**不覆盖**已经取得的恢复结论。随后递增 nonce 重新查询资格。

### 4.4 结果呈现（`describeResumeResult`）

按响应 `WorkflowRun.status` 分三种结论，全部依赖真实字段：

| 状态 | 标题 | 含义 |
|---|---|---|
| `running` | 恢复请求已受理，工作流已开始推进 | 明确写出"不代表工作流会成功，也不代表任务已完成" |
| `pending` | 恢复请求已受理，执行记录尚未开始推进 | attention，提示刷新工作流进度 |
| 其他（含未知） | 恢复请求已受理，但执行记录未进入执行中 | attention，提示按真实状态判断、不自动重发 |

明细固定包含 `执行记录`、`执行状态`（未知取值原样展示）、`进度：已完成 N / M 步`；
`started_at` 为缺省 / 零值（`0001-01-01T00:00:00Z`）/ 非法时**不展示开始时间**。

---

## 5. 边界处理

| 场景 | 处理 |
|---|---|
| 任务切换 | Hook 在 `taskID` 变化时重置 `resumeGate`、`resumingRef` 与 `operation`；票据含 `taskID`，`acceptResumeResponse` 拒绝跨任务迟到响应；`mountedRef` 拒绝卸载后写入 |
| 资格刷新失败 | 保留旧快照继续展示，但 `eligibilityRefreshError` 进入禁用判定 → 恢复入口禁用 |
| 恢复结果不确定 | 独立 `uncertain` 字段 + 固定文案，不自动重发 |
| 恢复后刷新失败 | 保留 `outcome`，单独展示 `refreshError` |
| 同一帧连续点击 | `resumingRef`（同步 ref）先拦截，`state.running` 只作展示 |
| 容器处置 / 代码保存核验之后 | 处置沿用 `token`/`onRefreshTask` 机制重新查询服务端资格；页面**不**本地改资格，也不因处置成功而放行恢复 |
| `next_actions` | 仅原文展示 + "页面不会自动运行/不会据此自动执行"说明，不解析为指令 |
| `blocking` 未知分类 | 原样展示原始代码并提示"当前版本未定义" |
| 恢复成功后 | 不重试任务、不处置容器、不执行核验（E2E 有专门断言） |

---

## 6. 新增与修改的文件

**新增**

- `lingting-agent-flow-ui/src/pages/tasks/resumeAction.ts` —— 纯函数与纯数据：可提交性判定、
  票据与竞态判定、结果解释、固定文案。
- `lingting-agent-flow-ui/tests/unit/task-resume-action.test.ts` —— 25 个单元测试。
- `lingting-agent-flow-ui/tests/e2e/task-resume-action.spec.ts` —— 14 个 E2E 用例。
- `.steps/P4-M4-4-resume-eligibility-web-action.md` —— 本文件。

**修改**

- `lingting-agent-flow-ui/src/pages/tasks/useResumeEligibility.ts` —— 参数改为
  `{ taskID, token, refresh }`，新增 `operation` / `resume` / `clearOutcome`，加入防重与票据。
- `lingting-agent-flow-ui/src/pages/tasks/ResumeEligibilityPanel.tsx` —— 新增恢复按钮、
  禁用原因 tooltip、结果 / 失败 / 不确定 / 刷新失败区块；接收 `onRefreshTask`。
- `lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx` —— 移除进度条内的恢复按钮与
  `handleResumeWorkflow`；给面板传 `onRefreshTask`。
- `lingting-agent-flow-ui/src/pages/tasks/status.ts` —— 删除 `canResumeWorkflow`，留注释说明
  恢复判定只能来自服务端资格。

**Go 生产代码：未修改。** 核实的依据是 §2.4：恢复接口已经在应用层重新执行了完整前置校验
（租约 / 计划摘要 / `CheckWorkflowStart` / 容器 / 执行节点 / 代码保存副作用），改动后端属于
无必要扩大范围。

---

## 7. 实际执行的命令与结果

| 命令 | 结果 |
|---|---|
| `npx vp test run tests/unit/task-resume-action.test.ts --maxWorkers 1` | 1 文件 / **25 通过** |
| `npx playwright test tests/e2e/task-resume-action.spec.ts --workers=1` | **14 通过 / 0 失败** |
| `npx vp test run --maxWorkers 1`（= `pnpm test:unit:safe`） | 26 文件 / **547 通过 / 0 失败** |
| `npx playwright test --workers=1`（= `pnpm test:e2e:safe`） | **197 通过 / 0 失败** |
| `npx vp fmt` | 完成（130 文件） |
| `npx vp lint --fix` | 无 error、无 warning |
| `npx tsc --noEmit` | 无错误 |
| `pnpm build` | 通过，产物写入 `internal/webui/dist`（仅有既有的 chunk > 500 kB 提示） |
| `gofmt -l internal/app internal/httpapi` | 无输出 |
| `go vet ./internal/app/... ./internal/httpapi/...` | 无输出 |

`TEST_ARTIFACT_DIR`（每次运行随机生成、保留不清理）示例：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-*`。

测试并发说明：全部命令使用单 worker（`--maxWorkers 1` / `--workers=1`），未提高 worker 数；
这是本机的软性资源限制，不是 CPU 硬配额。

---

## 8. 未执行验证、未覆盖场景与剩余风险

**未执行的验证**

- **Go 测试整体未运行**：本轮未修改 Go 生产代码，因此没有运行 `go test`；只运行了 `go vet`
  与 `gofmt`。
- **未做真实环境验证**：没有连接真实 SSH / Docker / Pi，也没有在真实容器内恢复工作流。
  E2E 全部使用 API mock，只能证明"UI 对给定响应的处理正确"。

**引用的既有 Go 测试证据**（未重新运行，仅作为服务端前置条件的现成证据）

- `tests/unit/httpapi/workflow_test.go`：`TestWorkflowAPIResumeRequiresRunID`（缺少 `run_id`
  → 400）、`TestWorkflowAPIRejectsWrongMethod`（GET → 405）。
- `tests/integration/local/workflow/code_save_recovery_test.go`：
  `TestResumeWorkflowRejectsPendingCodeSave`（未决副作用 → `ErrWorkflowPrerequisite`，
  且不改写执行记录与保存记录）、`TestResumeWorkflowRejectsUnverifiableCodeSave`
  （核验不可确认 → `ErrWorkflowConflict`）、`TestResumeWorkflowAcceptsConvergedCodeSave`
  （已确认未发生 → 允许继续）。
- `tests/unit/app/task_workflow_test.go`：`ResumeWorkflow` 的状态前置条件与冲突路径。
- `tests/integration/local/workflow/code_save_resume_test.go`：续跑资格的阻塞分类与
  `next_actions` 里带真实记录标识。

**未覆盖场景 / 剩余风险**

1. **恢复接口是 202 语义**：服务端在后台推进，响应只代表已受理。页面用 5 秒自动刷新跟踪
   后续状态，但如果工作流立刻进入 `blocked`，最终结论要等下一次刷新才能看到。
2. **没有 SSE**：恢复后的进度仍靠轮询，长任务下用户看到的进度有最长 5 秒延迟。
3. **`next_actions` 中的 CLI 步骤无法在 Web 完成**：`agentflow code verify --record <id>`
   这类建议目前必须去命令行执行；面板已如实标注"页面不会自动运行"，但没有提供 Web 等价入口。
4. **未知 `can_resume` 与未知 `blocking` 组合**：前端保留原始信息并禁用入口，但如果服务端
   将来引入"非阻塞类告警"语义，当前实现会保守地显示为阻塞——需要届时复查映射。
5. **`handleResumeWorkflow` 删除后**：进入任务详情的恢复入口从进度条移到了资格面板下方，
   位置发生变化；没有做面包屑或滚动引导。

---

## 9. 测试覆盖对照（第 6.1 / 6.2 节要求）

| 要求 | 覆盖位置 |
|---|---|
| `can_resume: true` 且查询成功时按钮可用 | 单元「can_resume=true 且查询成功时允许提交」；E2E 用例 1 |
| `can_resume: false` 时禁用并显示阻塞原因 | 单元「can_resume=false 时不可提交」；E2E 用例 2 |
| 查询尚未完成时不可用 | 单元「资格查询尚未完成时不可提交」 |
| 查询失败时不可用 | 单元「资格查询失败时不可提交」；E2E 用例 3 |
| 刷新失败保留旧快照但不冒充当前资格 | 单元「刷新失败保留旧快照但不把它当作当前授权」；E2E 用例 4 |
| 未知阻塞分类 / 未知 `next_actions` 原样展示 | E2E 用例 2（`future_resume_blocker`）；既有 `task-resume-eligibility` 用例 |
| 切换任务后旧响应不影响新任务 | 单元「切换任务后旧任务的迟到响应被丢弃」；E2E 用例 10 |
| 恢复请求在途防重 | 单元「恢复请求在途时不可重复提交」；E2E 用例 12 断言只发一次 |
| 服务端拒绝时展示真实错误 | 单元「服务端拒绝时保留真实业务错误」；E2E 用例 7 |
| 恢复成功与工作流完成严格区分 | 单元「请求已受理不表述为任务成功」「running 时…不代表任务已完成」；E2E 用例 1、6 |
| 超时 / 网络中断提示结果不确定且不自动重发 | 单元「结果不确定只由超时、网络中断与取消触发」；E2E 用例 8 |
| 恢复后刷新失败仍保留请求结果 | E2E 用例 9 |
| 容器处置 / 代码保存核验后必须重新依据服务端资格 | E2E 用例 13、14 |
| 恢复不触发任务重试或其他副作用 | E2E 用例 12 |
| E2E：资格允许 → 点击 → 展示响应 | E2E 用例 1 |
| E2E：资格被阻塞 → 看见原因与建议 → 不能点击 | E2E 用例 2 |
| E2E：资格查询失败 → 保持禁用 | E2E 用例 3 |
| E2E：切换任务 → 迟到响应不污染 | E2E 用例 10 |
| E2E：失败 / 不确定 → 不伪造成功、不自动重发 | E2E 用例 7、8 |
| E2E：成功但刷新失败 → 保留结果 + 单独提示 | E2E 用例 9 |
| E2E：处置 / 核验后重新获取服务端资格 | E2E 用例 13、14 |
