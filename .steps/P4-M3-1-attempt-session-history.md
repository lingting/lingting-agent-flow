# P4-M3-1 —— Attempt / Session 历史展示补齐

- **任务类型**：前端可观测性完善（展示层字段补全，不改业务行为）
- **状态**：`completed`
- **实施前 HEAD SHA**：`3cf4ec51be25f34fc7a439303acbcf3f51f3333f`
  （`git fetch origin master` 后 `HEAD` 与 `origin/master` 一致，实测确认交接参考 SHA 未变化）
- **实施后 HEAD SHA**：工作区改动尚未提交（见 §9），提交后需补记
- **依据**：`.steps/P4-M1-api-web-gap-inventory.md` 缺口 **G7**；
  上一轮核验结论见 [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md) §1.1

> **本轮未修改任何后端代码**：`internal/`、`cmd/`、`migrations/` 与所有 HTTP API 均无改动。
> `AttemptResponse` 已完整输出 `timed_out` / `cancelled` / `error_class` / `failure_stage`，
> `SessionResponse` 已完整输出 `archive_status` / `export_status`，缺口全部在前端展示层。

---

## 1. 实施前核实：后端 DTO 与前端字段的真实契约

按当前 `master` 代码逐字段核对，**不依赖历史文档断言**。

### 1.1 `httpapi.AttemptResponse`（`internal/httpapi/executions.go:70`）

| JSON 字段 | Go 类型 | 是否可缺省 | 语义 | 前端 `Attempt`（`src/lib/api/types.ts:644`） |
|---|---|---|---|---|
| `id` | `string` | 否 | 尝试标识 | ✅ `id: string` |
| `task_id` | `string` | 否 | 所属任务 | ✅ `task_id: string` |
| `step_id` | `string` | 否 | 所属步骤 | ✅ `step_id: string` |
| `attempt_number` | `int` | 否 | 尝试序号 | ✅ `attempt_number: number` |
| `status` | `string`（`domain.AttemptStatus`） | 否 | 尝试状态 | ✅ `status: AttemptStatus` |
| `status_terminal` | `bool` | 否 | 是否已结束 | ✅ `status_terminal: boolean` |
| `exit_code` | `int` | 否（未取得为 `-1`） | 主执行退出码 | ✅ `exit_code: number` |
| `timed_out` | `bool` | 否（无 `omitempty`） | **本次尝试因超时结束** | ✅ `timed_out: boolean` |
| `cancelled` | `bool` | 否（无 `omitempty`） | **本次尝试被取消** | ✅ `cancelled: boolean` |
| `error_class` | `string` | 是（`omitempty`） | 失败或重试原因分类 | ✅ `error_class?: string` |
| `failure_stage` | `string` | 是（`omitempty`） | 失败发生的阶段名 | ✅ `failure_stage?: string` |
| `error` | `string` | 是（`omitempty`） | 失败原因摘要（映射 `attempt.FailureSummary`） | ✅ `error?: string` |
| `result_summary` | `string` | 是（`omitempty`） | 结果摘要（映射 `attempt.VerificationSummary`） | ✅ `result_summary?: string` |
| `verification_verdict` | `string` | 是（`omitempty`） | 本次尝试的验证判定 | ✅ `verification_verdict?: string` |
| `retry_reason` | `string` | 是（`omitempty`） | 触发下一次重试的原因 | ✅ `retry_reason?: string` |
| `session_ids` | `[]string` | 否（空数组） | 关联会话标识 | ✅ `session_ids: string[]` |
| `created_at` / `started_at` | `time.Time` | 否 | 时间 | ✅ |
| `finished_at` | `*time.Time` | 是（`omitempty`） | 结束时间 | ✅ `finished_at?: string` |

**结论**：任务要求补齐的 4 个字段（`timed_out`、`cancelled`、`error_class`、`failure_stage`）
后端全部已输出，前端类型全部已声明，**唯一缺口是 `StepDetailPanel.tsx` 的 `AttemptBlock`
没有渲染它们**。调用链 `fetchStepAttempts` → `useStepRecords` → `AttemptBlock` 也完整透传
整个 `Attempt` 对象，无需改动。

### 1.2 `httpapi.SessionResponse`（`internal/httpapi/executions.go:110`）

| JSON 字段 | Go 类型 | 语义 | 前端 `Session`（`src/lib/api/types.ts:681`） |
|---|---|---|---|
| `status` | `string`（`domain.SessionStatus`） | 会话执行状态 | ✅ `status: string` |
| `run_status` | `string`（`domain.SessionRunStatus`） | 本次执行自身的运行结果 | ✅ `run_status?: string` |
| `archive_status` | `string`（`domain.SessionArchiveStatus`） | **原始会话归档状态** | ✅ `archive_status: string` |
| `archive_path` | `string` | 归档后的原始会话位置 | ✅ `archive_path?: string` |
| `export_status` | `string`（`domain.SessionExportStatus`） | **HTML 导出状态** | ✅ `export_status: string` |
| `html_export_path` | `string` | 导出后的 HTML 位置 | ✅ `html_export_path?: string` |
| `agent_role` / `role_id` / `provider` / `model` | `string` | 角色与模型 | ✅ |
| `session_file_path` / `session_id` / `invocation_id` | `string` | 会话文件与调用标识 | ✅ |
| `exit_code` | `*int` | Agent 进程退出码 | ✅ `exit_code?: number` |
| `error` | `string` | 失败原因摘要 | ✅ `error?: string` |

**`archive_status` / `export_status` 的取值来源与边界**（`internal/domain/session_result.go`）：

- `SessionArchiveStatus`：`not_attempted` / `succeeded` / `failed`
- `SessionExportStatus`：`not_attempted` / `succeeded` / `failed`
- **空值归一化在服务端完成**：`httpapi.archiveStatusOf` / `exportStatusOf` 把数据库空字符串
  统一输出为 `not_attempted`，因此前端"缺省"只可能来自代理裁剪或旧服务端。

**结论**：两个字段都是必出字段，前端类型已声明；缺口是 `AttemptBlock` 直接打印原始英文枚举。

---

## 2. 实施范围与取舍

### 2.1 只做展示层，不做任何状态推断

三条硬约束贯穿全部改动：

1. **接口数据是事实来源**：所有文案只由 `Attempt` / `Session` 的真实字段派生。
2. **不从文本猜测分类**：`error_class` / `failure_stage` 的未知取值原样展示，不根据
   `attempt.error` 的文字反推分类。
3. **缺省不推断"成功"或"未发生"**：缺省一律返回中性占位符或不渲染该行。

### 2.2 未修改的部分（有意为之）

- 尝试的准入判定（`attemptAllowsValidation` / `canValidateAttempt` / `canInitializeAttempt`）
- 状态机、验证按钮规则、服务端业务逻辑
- API 请求、记录归属关系、操作按钮可用性
- 页面整体布局与组件结构（未重做任务详情页）
- `attempt.error` 的既有展示方式（新增字段独立成行，不与之重复堆叠）

### 2.3 `completed` 语义未被改写

`ATTEMPT_STATUS_META.completed` 的既有标签仍为"正常结束"，本轮未改动；新代码也没有把
`status === "completed"` 解释为验证通过或任务成功。会话 `status` 的同名标签同样保持
"正常结束"，并在单元测试中断言其不含"通过""成功"字样。

---

## 3. 变更文件清单

### 3.1 修改

| 文件 | 改动与理由 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/steps.ts` | 新增尝试失败事实与三张会话状态映射（详见 §4）；这是既有展示元数据模块，职责匹配，未新建通用框架 |
| `lingting-agent-flow-ui/src/pages/tasks/StepDetailPanel.tsx` | `AttemptBlock` 渲染超时 / 取消标记与错误分类 / 失败阶段；抽出 `SessionLine` 组件承载会话三状态的中文标签，保留会话 ID、角色、模型、执行状态与时间（详见 §5） |
| `lingting-agent-flow-ui/tests/e2e/task-execution.spec.ts` | 新增 `sampleSession` 样本、`stubSessions` 参数化、`尝试与会话历史展示` 用例组（6 个） |

### 3.2 新增

| 文件 | 职责 |
|---|---|
| `lingting-agent-flow-ui/tests/unit/task-attempt-session-history.test.ts` | 30 个单元用例，覆盖任务要求的 12 条验收点 |

### 3.3 未修改

- `internal/`、`cmd/`、`migrations/`：**零改动**
- `src/lib/api/types.ts`：字段原本齐全，无需补充
- `src/lib/api/index.ts`：无新增导出
- `src/pages/tasks/StepListPanel.tsx`、`useExecutionRecords.ts`：调用链已完整透传，无需改动

---

## 4. 新增状态标签与未知值处理策略

### 4.1 尝试失败事实（`steps.ts`）

```text
AttemptFailureFacts = { timedOut?, cancelled?, errorClass?, failureStage?,
                        errorClassKnown, failureStageKnown }
```

`attemptFailureFacts(attempt)` 的规则：

| 输入 | 输出 |
|---|---|
| `timed_out === true` | `timedOut = "已超时"` |
| `timed_out === false` | `timedOut` 缺省（**不渲染**） |
| `cancelled === true` | `cancelled = "已取消"` |
| `cancelled === false` | `cancelled` 缺省（**不渲染**） |
| `error_class` 已确认取值 | 中文说明 + `errorClassKnown = true` |
| `error_class` 未知取值 | 原始文本 + `errorClassKnown = false`，页面追加"（未在当前版本中定义）" |
| `error_class` 缺省 / 空白 | `errorClass` 缺省（**不渲染**） |
| `failure_stage` 同上三行 | 同上 |

**超时与取消是两个独立布尔事实**：`Attempt.Timeout()` 只置 `TimedOut`，`Attempt.Cancel()`
只置 `Cancelled`（`internal/domain/attempt.go:321`、`:335`）；前端也只读这两个布尔，不从
`status` 反推。单元测试固定了这一点（`status: "timed_out"` 但布尔为 `false` 时不显示超时）。

### 4.2 错误分类标签（`ATTEMPT_ERROR_CLASS_LABEL`）

只收录在服务端代码中**确认会写入 `Attempt.ErrorClass` 或属于同一分类枚举**的取值：

| 取值 | 中文说明 | 证据 |
|---|---|---|
| `agent_failure` | Agent 调用失败 | `domain.RetryClassAgentFailure`（`task_config.go:38`），由 `app.errorClassOf` 写入（`step_executor_run.go:781`）、`execution_residue.go:265` 写入 |
| `timeout` | 执行超时 | `domain.RetryClassTimeout`（`task_config.go:42`），由 `app.errorClassOf` 写入（`step_executor_run.go:779`） |
| `verification_failure` | 验证未通过 | `domain.RetryClassVerificationFailure`（`task_config.go:40`） |
| `network` | 网络类失败 | `domain.RetryClassNetwork`（`task_config.go:44`） |

**未收录**（刻意）：`code_fetch_*`、`code_save_*`、`container_*`、`closeout_*`、
`steps_incomplete`、`session_not_persisted` 等取值虽然存在于 `internal/domain/` 的恢复判定中，
但**不经由 `Attempt.ErrorClass` 输出**到 `AttemptResponse`，因此不进入本映射表；若将来
开始写入，会走"未知取值原样展示"分支，语义不会错，只是缺中文说明。

### 4.3 失败阶段标签（`ATTEMPT_FAILURE_STAGE_LABEL`）

只收录服务端实际写入 `Attempt.FailureStage` 的三个阶段名：

| 取值 | 中文说明 | 证据 |
|---|---|---|
| `execute` | 执行阶段 | `app/step_executor_run.go:302` |
| `verify` | 验证阶段 | `app/step_workflow_run.go:328`、`app/step_workflow_repair.go:522`、`:564`（写入 `Step.FailureStage`；与 `Attempt` 共用同一阶段命名空间） |
| `recover` | 服务恢复阶段 | `app/execution_residue.go:264` |

未知阶段（例如将来的 `commit`）原样展示，不猜测语义。

### 4.4 会话三状态（三张独立映射表）

| 表 | 取值 → 标签 | 领域类型 |
|---|---|---|
| `SESSION_ARCHIVE_STATUS_LABEL` | `not_attempted` → 未归档；`succeeded` → 已归档；`failed` → 归档失败 | `domain.SessionArchiveStatus` |
| `SESSION_EXPORT_STATUS_LABEL` | `not_attempted` → 未导出；`succeeded` → 已导出；`failed` → 导出失败 | `domain.SessionExportStatus` |
| `SESSION_STATUS_LABEL` | `starting` → 启动中；`running` → 执行中；`completed` → 正常结束；`failed` → 失败；`cancelled` → 已取消；`interrupted` → 已中断 | `domain.SessionStatus` |

`SESSION_RUN_STATUS_LABEL`（`completed` → 运行正常结束，`failed` → 运行失败，
`cancelled` → 运行已取消，`timed_out` → 运行已超时）对应 `domain.SessionRunStatus`。

### 4.5 未知值与缺省值的统一策略

| 情况 | 归档状态 | 导出状态 | 会话执行状态 | 运行结果 |
|---|---|---|---|---|
| 缺省 / 空字符串 | `未提供`（中性占位符） | `未提供` | `未提供` | 返回 `undefined`，调用方不渲染括号 |
| 已知取值 | 中文标签 | 中文标签 | 中文标签 | 中文标签 |
| 未知取值 | **原样展示原始值** | **原样展示原始值** | **原样展示原始值** | **原样展示原始值** |

**为什么归档与导出不共用映射表**：两表都有 `succeeded` / `failed`，但分别描述"原始会话
已保存到本机持久目录"与"HTML 已生成"，合并会让"归档成功、导出失败"这种真实现场被读成
单一结论。单元测试显式断言两表的同名取值标签必须不同。

**为什么不从会话执行状态推导**：`Session.ApplyArtifacts` 的注释（`internal/domain/session.go:119`）
与 `httpapi.SessionResponse` 的注释都明确三者相互独立，`execution_residue.go:298` 也写明
"三维度保持原值"。单元测试用"`status=completed` 但 `archive_status=failed`、
`export_status=not_attempted`"的现场固定该语义。

---

## 5. 页面呈现

### 5.1 `AttemptBlock`（尝试）

尝试头部行（`Space`）在退出码与开始时间之后追加：

- `已超时`（`type="warning"`，仅 `timed_out === true`）
- `已取消`（`type="warning"`，仅 `cancelled === true`）

在既有 `attempt.error` 与 `attempt.result_summary` 之后，新增一条独立的 `Space`：

- `错误分类 <标签>` + 未知时追加 `（未在当前版本中定义）`
- `失败阶段 <标签>` + 未知时追加 `（未在当前版本中定义）`

`error_class` 与 `failure_stage` **都缺省时整行不渲染**，因此不会出现空白占位。

`attempt.error` 的既有展示未被删除或改写：新增字段表达"哪一类 / 哪个阶段"，`error`
表达"具体出了什么错"，三者语义不同、互不替代，也不重复堆叠同一句话。

### 5.2 `SessionLine`（会话，新抽出的组件）

```text
会话 <id>  <agent_role> / <model>  <执行状态标签>（<运行结果标签>）  归档 <标签> / 导出 <标签>
```

既有信息全部保留：会话 ID、角色、模型、执行状态、运行结果、归档与导出。变化只有把六个
原始英文枚举换成中文标签，并在缺省时使用中性占位符。

---

## 6. 测试清单

### 6.1 单元测试（`tests/unit/task-attempt-session-history.test.ts`，30 个）

对应任务 §四 要求的 12 条验收点：

| # | 验收点 | 用例 |
|---|---|---|
| 1 | `timed_out=true` 展示超时标记 | `timed_out=true 时给出已超时标记` |
| 2 | `timed_out=false` 不显示超时警告 | `timed_out=false 时不产生超时警告` |
| 3 | `cancelled=true` 展示取消标记 | `cancelled=true 时给出已取消标记` |
| 4 | `cancelled=false` 不显示取消警告 | `cancelled=false 时不产生取消警告` |
| 5 | `error_class` 有值时准确展示 | `error_class 有值时准确展示已知分类的中文说明` |
| 6 | `failure_stage` 有值时准确展示 | `failure_stage 有值时准确展示已知阶段的中文说明` |
| 7 | 缺省时不产生虚构值 | `错误分类与失败阶段缺省时不产生虚构值`、`缺失的 error_class / failure_stage 读取函数返回 undefined` |
| 8 | 每个归档状态映射正确 | `覆盖 domain.SessionArchiveStatus 的全部取值`、`每个已核实的归档状态都映射到正确中文标签` |
| 9 | 每个导出状态映射正确 | `覆盖 domain.SessionExportStatus 的全部取值`、`每个已核实的导出状态都映射到正确中文标签` |
| 10 | 未知归档 / 导出状态原样显示 | `未知归档状态原样显示…`、`未知导出状态原样显示` |
| 11 | 缺省状态用中性占位符 | `缺省归档状态使用中性占位符…`、`缺省导出状态使用中性占位符…` |
| 12 | 归档与导出相互独立 | `归档状态与导出状态相互独立` 下的 4 个用例 |

额外补充：超时与取消互不推导、布尔事实不依赖 `status`、未知分类不猜成已知分类、
分类映射表取值审核、会话执行状态不被表述为验收通过、运行结果标签缺省不渲染。

### 6.2 E2E 测试（`tests/e2e/task-execution.spec.ts` → `尝试与会话历史展示`，6 个）

| 用例 | 覆盖 |
|---|---|
| `超时、取消、错误分类与失败阶段按真实字段展示` | 两条尝试：超时（含 `error_class` / `failure_stage`）与取消（两者缺省）。断言"已超时"/"已取消"各出现 2 次（状态标签 + 独立标记），错误分类与失败阶段各只出现 1 次（取消尝试不补造） |
| `布尔事实为 false 时不产生超时与取消警告` | 布尔全 false 时四类文本均 `toHaveCount(0)` |
| `会话归档与导出状态展示中文标签，并保留真实标识` | 断言 `归档 已归档 / 导出 已导出`、会话 ID、`developer / group/free`、执行状态"正常结束"仍在 |
| `未知归档与导出状态原样展示，不误报成功` | `archived_to_object_storage` / `pending_upload` 原样可见，且"已归档""已导出"计数为 0 |
| `归档成功但导出失败时两个事实分别展示` | 归档成功与导出失败同时成立时不被合并；"导出 已导出"计数为 0 |
| `既有执行记录、验证历史与操作入口没有回归` | 执行尝试、验证历史（最新验证 / 流程 已完成）、确定性验证与初始化代码按钮仍可见 |

模拟数据全部与真实 DTO 对齐：`sampleSession` 只含 `SessionResponse` 真实字段，
`sampleAttempt` 只用 `AttemptResponse` 真实字段，没有引入后端不存在的键。

---

## 7. 实际执行命令与真实结果

全部在 `lingting-agent-flow-ui/` 下执行。未执行的项一律标注"未执行"。

| # | 命令 | 结果 |
|---|---|---|
| 1 | `npx vp test run tests/unit/task-attempt-session-history.test.ts --maxWorkers 1` | **通过**：1 文件 / 30 用例 |
| 2 | `npx vp test run --maxWorkers 1`（全量单元测试） | **通过**：19 文件 / **326** 用例（上轮 296，新增 30） |
| 3 | `pnpm test:unit:safe` | **通过**：19 文件 / **326** 用例 |
| 4 | `npx playwright test tests/e2e/task-execution.spec.ts --workers=1` | **通过**：**26/26**（原有 20 + 新增 6） |
| 5 | `pnpm test:e2e:safe`（全量 E2E，`--workers=1`） | **116 通过 / 3 失败**，失败项见 §8 |
| 6 | `pnpm check`（`vp fmt && vp lint --fix && tsc --noEmit`） | **通过**（`Finished in 721ms on 98 files`，无 warn / error） |
| 7 | `pnpm build`（`scripts/build-webui.mjs`） | **通过**；仅有既有的 chunk >500 kB 体积告警 |
| 8 | `go vet ./...` | **未执行**（本轮未修改 Go 代码，也无相关改动需要检查） |
| 9 | 任何 Go 测试 | **未执行**（本轮未修改后端代码） |

### 7.1 测试产物路径

每次运行由 `tests/internal/artifacts.ts` 生成独立随机目录
`<OS 临时目录>/lingting-agent-flow-ui-tests/run-*`，运行结束保留。本轮实际观察到的目录包括：

- 全量单元测试：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-IgZlrC`
  （同批 19 个 worker 各自打印一次 `TEST_ARTIFACT_DIR`）
- 新单元测试单跑：`...\run-DF5z9D`
- `task-execution.spec.ts` 单文件：`...\run-Z6g0D7`、`...\run-8hxRjP`、`...\run-2safIP`（失败中间轮）
- 全量 E2E：`...\run-xsBZZS`（报告与失败产物根目录）
- 基线对照：`...\run-qXV0Lr`、`...\run-DU5I3B`、`...\run-w2Pok9`

`playwright-output/` 保存失败截图与 trace，`playwright-report/` 保存 HTML 报告，两者均未被削弱。

### 7.2 关于并发与资源

三轮 E2E 全部使用 `--workers=1`（`pnpm test:e2e:safe` 的既有语义），没有提高任何 worker
上限，没有直接启动额外无头浏览器，也没有另造并发控制。`pnpm test:unit:safe` 的
`--maxWorkers 1` 同样沿用既有入口。

---

## 8. E2E 基线失败：实际观察与对照证据

### 8.1 本轮观察到的 3 个失败

1. `tests/e2e/layout.spec.ts:43` `基础布局 › /machines 可访问且导航高亮、页面标题一致`
   — `Error: expect(locator).toContainText(expected) failed`
2. `tests/e2e/layout.spec.ts:64` `基础布局 › 刷新后路由与导航状态保持`
   — `Error: expect(locator).toContainText(expected) failed`
3. `tests/e2e/tasks.spec.ts:478` `任务详情 › 详情返回 404 时展示错误而不是伪造任务`
   — `Error: expect(locator).toBeVisible() failed`

### 8.2 基线对照（可复核）

```text
git stash push -u -- lingting-agent-flow-ui     # 移除本轮全部前端改动
npx playwright test tests/e2e/layout.spec.ts tests/e2e/tasks.spec.ts --workers=1
→ 3 failed / 23 passed
git stash pop                                    # 恢复本轮改动
```

基线失败用例与失败断言与本轮**完全相同**（同样是
`layout.spec.ts:43`、`layout.spec.ts:64`、`tasks.spec.ts:478`，同样是
`toContainText` / `toBeVisible` 两类断言）。因此这 3 个失败是本次改动之前就存在的问题，
**不是本轮引入，本轮也未修复**（任务 §七明确要求不顺手修复）。

### 8.3 与历史记录的一致性

3 个失败项与 [`P4-frontend-test-resource-governance.md`](./P4-frontend-test-resource-governance.md)
§5.1 记录的完全一致，也与
[`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md) §6.1 一致。

---

## 9. 完成定义核对与路线图状态

### 9.1 字段缺口是否闭合

| 任务要求 | 状态 | 依据 |
|---|---|---|
| `timed_out` 明确显示"已超时" | ✅ 闭合 | 接口已有 `AttemptResponse.timed_out`（无 `omitempty`），前端已渲染，单元 + E2E 覆盖 |
| `cancelled` 明确显示"已取消" | ✅ 闭合 | 同上 |
| `error_class` 显示错误分类，缺省不归类 | ✅ 闭合 | 接口已有（`omitempty`），缺省时不渲染，未知取值原样 |
| `failure_stage` 显示失败阶段，缺省不虚构 | ✅ 闭合 | 同上 |
| `archive_status` 中文化 | ✅ 闭合 | 三个取值全部映射，未知 / 缺省有明确策略 |
| `export_status` 中文化 | ✅ 闭合 | 同上，且与归档映射完全独立 |

**无剩余字段缺口**：任务要求的 6 个字段全部能从现有接口取得并已如实展示。

### 9.2 M3-1 状态：`completed`

完成定义（"字段级展示补全 + 状态中文化，且不伪造、不推断"）已满足：

1. 四个尝试字段全部基于真实 `Attempt` 对象渲染；
2. 两个会话状态各自独立的映射表，不共用语义；
3. 未知值与缺省值都有明确、可测、不误报成功的处理；
4. 30 个单元用例 + 6 个 E2E 用例；单元测试 326 通过，单文件 E2E 26 通过；
5. 全量 E2E 116 通过 / 3 失败，失败项经 `git stash -u` 基线对照确认为既有问题；
6. `pnpm check` 与 `pnpm build` 均通过；
7. 未修改任何后端代码、未新增或修改任何 HTTP API。

`P4-M3` 整体仍为 `in_progress`：M3-6、M3-7、M3-8 未开始。

---

## 10. 剩余风险与未执行项

### 10.1 剩余风险（均有代码或测试证据支持）

1. **`error_class` / `failure_stage` 的中文说明是白名单，不跟随服务端自动扩展**：
   若服务端新增写入取值，前端会走"未知值原样展示 + （未在当前版本中定义）"分支，语义不会
   错但缺中文说明。这是刻意取舍：宁可展示原始值，也不根据错误文本猜测分类。
2. **`SESSION_STATUS_MISSING_LABEL` 在本项目当前链路上实际不会被触发**：
   `httpapi.archiveStatusOf` / `exportStatusOf` 已把空值归一为 `not_attempted`，
   `status` 也始终存在。该占位符仍被保留并测试，用于代理裁剪字段或旧服务端场景。
3. **归档路径与导出路径未展示**：接口提供 `archive_path` / `html_export_path`，但任务范围
   只要求状态中文化；且路径是服务端本机目录，直接展示给浏览器用户价值有限。若后续需要
   "打开归档"类操作，属独立立项。
4. **E2E 全部在网络层拦截 API**：没有启动真实 Go 服务、SQLite、SSH、Docker 或 Pi，
   真实任务产生的 `timed_out` / `cancelled` / `error_class` / `failure_stage` 取值未被
   在真实数据库上观察。取值映射的依据是服务端代码静态核实（§4.2、§4.3），不是运行时观察。

### 10.2 未执行项

| 项目 | 原因 |
|---|---|
| Go 侧 `go vet ./...` / `go build ./...` / 任何 Go 测试 | 本轮未修改后端代码，无相关改动需要检查 |
| `pnpm test:safe` / `pnpm test:all:safe` 完整串联 | 两者都包含全量 E2E，必然带回 §8 的 3 个既有失败，`&&` 会在此中断；已分别验证各阶段（§7 第 2/3/4/5/6/7 行） |
| 3 个既有 E2E 失败的修复 | 超出本任务范围（任务 §七 明确不做），需单独立项定位 |
| 真实服务 + 真实容器端到端验证 | 本任务为纯展示层补全，且不涉及长请求与远程操作 |

---

## 11. 交接事项

- 工作区改动尚未提交，**实施后 HEAD SHA 仍为 `3cf4ec5`**；提交后需在本文档与
  `.steps/README.md`、`.steps/ROADMAP.md`、`.steps/P4-product-experience-optimization.md`
  四处补记新 SHA。
- 下一步建议推进 **M3-7（进度自动刷新）**，其后再处理需要后端能力的 M3-6（工作流步骤
  起止时间）与 M3-8（事件时间线、修复周期历史）。
- §8 的 3 个既有 E2E 失败需要单独立项（`layout` 两项的导航高亮 / 标题断言，
  `tasks.spec.ts:478` 的 404 错误态可见性）。
