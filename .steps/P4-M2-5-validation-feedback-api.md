# P4-M2-5 —— 验证失败反馈查询 API

> 实施依据：`master` 上 `internal/domain/validation_feedback.go`、
> `internal/storage/interfaces.go`（`ValidationFeedbackStore` / `ValidationStore`）、
> `internal/storage/sqlite/validation_feedbacks.go`、`internal/storage/sqlite/validations.go`、
> `internal/app/execution_records.go`、`internal/httpapi/{executions.go,router.go}`、
> `internal/httpserver/server.go`、`internal/server/server.go` 与 `tests/` 的实际代码核实。
> 对应 P4-M1 缺口 **G6** 的 API 部分（见
> [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md) §5、§6）。

## 1. 真实业务需求

验证失败反馈（`validation_feedbacks`）自迁移 `0017_validation_feedbacks.sql` 起就在写入：
`app.StepWorkflowService` 在每次验证未通过时调用 `domain.NewValidationFailureFeedback`
生成结构化反馈并持久化，目的是让"下一次 Developer 执行"能拿到"上一轮到底失败了哪些检查"。

但在本轮之前，这些已落盘、已脱敏、已裁剪的数据**只有内部消费者**：

- 修复循环内部按 `ValidationRunID` 精确读取（`step_workflow_repair.go:351`）；
- 首轮执行按"当前周期内最近一次验证"读取（`step_workflow.go:504`）。

没有任何 HTTP 或 CLI 入口能读到它。用户因此只能看到
`GET …/validations` 返回的 `status` / `verdict` / `failure_summary`，看不到
"哪条检查失败、退出码多少、错误摘要是什么"，无法判断下一步该修什么（G6 的原始描述）。

本轮把该能力以**只读 HTTP 接口**暴露，作为后续 M3 验证历史页面（M3-2）的数据来源。

## 2. API 契约

### 2.1 按验证运行 ID 查询反馈

```text
GET /api/v1/tasks/{taskID}/steps/{stepID}/validations/{runID}/feedback
```

只支持 `GET` 与 `HEAD`；其他方法返回 `405` 且 `Allow: GET, HEAD`。

路径与既有验证历史层级一致：`…/steps/{stepID}/validations` 列出该步骤的全部验证记录，
在其后追加 `{runID}/feedback` 即定位到"某一条验证记录产生的失败反馈"。该形状与步骤执行
（`…/execute`）、顺序执行（`…/execute-steps`）、闭环（`…/execute-and-validate`）、
修复周期（`…/open-repair-cycle`）、验证动作（`…/attempts/{attemptID}/validate`）都不
冲突：反馈路径是 6 段，且第 4 段是 `validations`、第 6 段是 `feedback`。

响应示例（数据来自真实持久化反馈）：

```json
{
  "task_id": "01J9Z0T7R6V0Y3X8W2K4M5N6P7",
  "step_id": "01J9Z0T7R6V0Y3X8W2K4M5N6P8",
  "developer_attempt_id": "01J9Z0T7R6V0Y3X8W2K4M5N6P9",
  "validation_run_id": "01J9Z0T7R6V0Y3X8W2K4M5N6PA",
  "developer_session_ids": [],
  "verdict": "failed",
  "summary": "必需检查未通过: build",
  "infrastructure_error": false,
  "checks": [
    {
      "check_id": "build",
      "check_name": "构建",
      "required": true,
      "status": "failed",
      "exit_code": 1,
      "failure_reason": "退出码 1 不在允许范围内",
      "stderr_summary": "编译错误: main.go:10",
      "stdout_summary": "构建开始",
      "timed_out": false,
      "cancelled": false,
      "infrastructure_error": false,
      "started_at": "2026-10-26T08:00:00Z",
      "finished_at": "2026-10-26T08:00:12Z"
    },
    {
      "check_id": "lint",
      "check_name": "静态检查",
      "required": false,
      "status": "timed_out",
      "exit_code": -1,
      "failure_reason": "检查超时",
      "timed_out": true,
      "cancelled": false,
      "infrastructure_error": false,
      "started_at": "2026-10-26T08:00:12Z",
      "finished_at": "2026-10-26T08:01:12Z"
    }
  ],
  "created_at": "2026-10-26T08:01:12Z"
}
```

### 2.2 字段语义

| 字段 | 语义 |
|---|---|
| `task_id` / `step_id` | 反馈所属任务与步骤，取自持久化记录 |
| `developer_attempt_id` | 产生待验证代码的那次开发尝试 |
| `validation_run_id` | 产生该反馈的验证记录 |
| `developer_session_ids` | 开发尝试关联的会话标识。领域对象有该字段，但迁移 `0017` 没有对应列，因此当前**恒为空数组**；接口如实返回空列表，不补造数据（见 §6.1） |
| `verdict` | **上一轮**验证的结论（`failed` / `inconclusive`），不是当前结论 |
| `summary` | 上一轮验证的结论摘要（已脱敏并裁剪到 `domain.MaxFeedbackSummary`） |
| `infrastructure_error` | 上一轮验证是否因基础设施异常没拿到可用证据；它与"代码检查失败"语义不同 |
| `checks[]` | 未通过的检查，最多 `domain.MaxFeedbackChecks`（20）条 |
| `checks[].required` | 该检查是否属于必需检查 |
| `checks[].status` | `domain.ValidationCheckStatus` 的真实取值：`failed` / `timed_out` / `cancelled` / `skipped` / `error` |
| `checks[].exit_code` | 实际退出码，未能取得时为 `-1` |
| `checks[].failure_reason` / `error_detail` | 判定原因与执行层错误摘要（已脱敏并裁剪到 `domain.MaxFeedbackReason`） |
| `checks[].stdout_summary` / `stderr_summary` | 输出摘要（已脱敏并裁剪到 `domain.MaxFeedbackOutputSummary`） |
| `checks[].timed_out` / `cancelled` | 分别表达超时与取消，二者互不替代 |
| `checks[].infrastructure_error` | 该检查没有拿到可用证据（对应 `status=error`） |
| `checks[].started_at` / `finished_at` | 检查的真实执行时间 |

`checks` 为空数组时输出 `[]`，不省略；`summary` 等可选字段为空时按 DTO 的
`omitempty` 约定省略。

### 2.3 为什么不再新增"最近一次反馈"入口

存储层的 `LatestValidationFailureFeedbackByStep` 已经存在，但它被现有 HTTP 接口
**间接覆盖**，因此本轮**不新增**
`GET …/validation-feedback/latest`：

1. `GET …/steps/{stepID}/validations` 已返回该步骤的全部验证记录（按创建时间升序），
   最后一条即最新验证；再对该条记录的 `id` 调用本轮接口就得到"最近一次反馈"。
   新增步骤级入口等于为同一语义提供两条路径，违反"避免同一语义存在多个入口"。
2. `LatestValidationFailureFeedbackByStep` 的真实用途是**显式重试时引用上一轮问题**，
   而且 `app.StepWorkflowService.initialFeedback` 已经不再无条件使用它——P2-M3 之后
   首轮依据必须来自"当前执行周期内最近一次验证"，步骤级"最近一次"会把其他周期的旧
   反馈当成当前依据（`internal/app/step_workflow.go` 的 `initialFeedback` 注释）。
   把它直接暴露成 HTTP 语义会复刻一个已被判定为不正确的读取方式。
3. 产品侧（M3-2 验证历史视图）需要的正是"按验证记录逐条查看"，与 `validations` 列表
   一一对应；"最近一次"可由前端在已取到的列表上选取，不需要额外接口。

该决策已由 `tests/unit/httpapi/execution_feedback_test.go` 的
`TestValidationFeedbackRejectsUnknownShapes` 覆盖：`…/validation-feedback/latest` 这类
形状返回统一 JSON `404`，不会被误当作已实现能力。

### 2.4 错误映射

| 场景 | 状态码 | `error.code` |
|---|---|---|
| 任务不存在或已逻辑删除 | `404` | `not_found` |
| 步骤不存在或不属于该任务 | `404` | `not_found` |
| 验证记录不存在或不属于该任务 / 该步骤 | `404` | `not_found` |
| 该验证记录没有失败反馈 | `404` | `not_found` |
| 路径标识非法（空、含 `/`） | `404` | `not_found` |
| 路径形状不属于本阶段资源 | `404` | `not_found` |
| 不支持的方法 | `405` + `Allow: GET, HEAD` | `method_not_allowed` |
| 执行记录服务未初始化 | `503` | `internal_error` |
| 存储或内部错误 | `500` | `internal_error` |

错误映射依据**错误类别**（`errors.Is` 判断 `app.ErrExecutionRecordNotFound` 等），
不解析错误文本。`500` 的响应消息固定为 `服务内部错误`，原始原因只写服务端日志。

"验证记录存在但没有反馈"与"验证通过"**不可混淆**：前者返回 `404`，后者本接口同样返回
`404`，结论必须从 `GET …/validations` 的 `verdict` 读取。反馈缺失绝不代表验证通过。

## 3. 应用层归属校验

新增 `app.ExecutionRecordService.GetValidationFailureFeedback`（`internal/app/execution_feedback.go`）。
它**不是**直接 `store.GetValidationFailureFeedback(runID)` 的透传，而是逐层校验归属：

| 顺序 | 校验 | 失败结果 |
|---|---|---|
| 1 | 解析并校验 `taskID`，读取任务，拒绝逻辑删除 | `ErrExecutionRecordNotFound` |
| 2 | 解析并校验 `stepID`，读取步骤，要求 `step.TaskID == taskID` | `ErrExecutionRecordNotFound` |
| 3 | 解析并校验 `runID`，读取验证记录，要求 `run.StepID == step.ID` 且 `run.TaskID == step.TaskID` | `ErrExecutionRecordNotFound` |
| 4 | 按 `run.ID` 读取反馈，`storage.ErrNotFound` 映射为"该验证记录没有失败反馈" | `ErrExecutionRecordNotFound` |
| 5 | 复核 `feedback.ValidationRunID / StepID / TaskID` 与验证记录一致 | 内部错误（记录错配，不伪装成未找到） |

复用而非复制：步骤与任务校验直接调用既有的 `requireStep` / `requireTask`，
新增的只有 `requireValidationRun`。HTTP handler 不做任何业务校验，只做协议转换。

查询不产生副作用：不创建反馈、不发起 Agent 调用、不执行命令、不修改任务 / 步骤 /
验证状态、不创建新 Attempt。用例 `TestValidationFeedbackQueryIsReadOnly` 断言两次查询后
验证记录的 `status` / `verdict` 与反馈内容完全不变。

## 4. 修改文件与代码证据

| 关注点 | 位置 |
|---|---|
| 应用层查询与归属校验（新增） | `internal/app/execution_feedback.go` 的 `GetValidationFailureFeedback`、`requireValidationRun` |
| HTTP DTO、处理器与路径段（新增） | `internal/httpapi/execution_feedback.go` 的 `feedbackSegment`、`ValidationFailureFeedbackResponse`、`ValidationCheckFeedbackResponse`、`getValidationFeedback`、`validationFailureFeedbackResponse` |
| 路由分发（修改） | `internal/httpapi/executions.go` 的 `executionHandler.serve`：新增 6 段 `…/validations/{runID}/feedback` 分支，并更新支持的路径清单 |
| 服务可用性判定 | 既有 `internal/httpapi/router.go` 的 `isExecutionRecordPath`（第二段为 `steps`）与 `rt.executions == nil` 检查，未改动 |
| 依赖装配 | 既有 `httpapi.Deps.ExecutionRecords` → `httpserver.Options.ExecutionRecords` → `internal/server/server.go` 的 `s.executionRecords`，本轮**未新增依赖** |
| 领域数据来源 | `internal/domain/validation_feedback.go` 的 `ValidationFailureFeedback`、`ValidationCheckFeedback`、`RedactSensitiveText`、`ClipFeedbackText` |
| 存储读取 | `internal/storage/sqlite/validation_feedbacks.go` 的 `GetValidationFailureFeedback`；`internal/storage/sqlite/validations.go` 的 `GetValidationRun` |

**未修改**：验证反馈生成规则（`domain.NewValidationFailureFeedback` / `checkFeedbackOf`）、
脱敏与裁剪算法（`RedactSensitiveText` / `ClipFeedbackText`）、数据库 Schema（无新增迁移）、
任务 / 步骤 / 验证状态机、前端代码。

## 5. 测试证据

全部使用真实 SQLite + 正式迁移 + 项目现有 fixture；测试产物保留在
`os.TempDir()/lingting-agent-flow-tests/` 下的随机目录，运行时输出 `TEST_ARTIFACT_DIR`，
不清理其他测试运行留下的产物。

### 5.1 应用层（`tests/unit/app/execution_feedback_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestGetValidationFailureFeedbackReturnsPersistedFeedback` | 正常读取指定验证运行的反馈：归属、结论、检查条目、退出码、原因、输出摘要 |
| `TestGetValidationFailureFeedbackRejectsDeletedTask` | 任务逻辑删除后返回 `ErrExecutionRecordNotFound` |
| `TestGetValidationFailureFeedbackRejectsUnknownStep` | 步骤不存在返回未找到 |
| `TestGetValidationFailureFeedbackRejectsForeignStep` | 步骤属于其他任务返回未找到 |
| `TestGetValidationFailureFeedbackRejectsUnknownRun` | 验证记录不存在返回未找到 |
| `TestGetValidationFailureFeedbackRejectsForeignRun` | 验证记录属于其他任务链、或路径任务与步骤不匹配，两种情形都返回未找到 |
| `TestGetValidationFailureFeedbackRejectsRunWithoutFeedback` | 验证记录存在但没有反馈 → 未找到（不是空反馈，也不是"通过"） |
| `TestGetValidationFailureFeedbackRejectsInvalidIDs` | 空标识与含 `/` 标识按未找到处理 |
| `TestGetValidationFailureFeedbackStorageFailureIsNotNotFound` | 关闭数据库后返回错误且**不**归类为未找到 |
| `TestExecutionRecordServiceRequiresStore` | 存储为空时构造失败 |

### 5.2 HTTP 层（`tests/unit/httpapi/execution_feedback_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestValidationFeedbackReturnsStructuredChecks` | 成功返回结构化反馈：全部归属字段、结论、摘要、多条检查的退出码 / 原因 / 输出摘要 / 超时标记 / 执行时间 |
| `TestValidationFeedbackRejectsForeignScope` | 跨任务验证记录、跨任务步骤、跨任务路径三种混用都返回 `404` + `not_found` |
| `TestValidationFeedbackRejectsMissingRecords` | 任务 / 步骤 / 验证记录不存在返回 `404` |
| `TestValidationFeedbackRejectsRunWithoutFeedback` | 没有反馈返回 `404`，响应不是空对象 |
| `TestValidationFeedbackRejectsUnsupportedMethods` | `POST` / `PUT` / `PATCH` / `DELETE` 返回 `405` + `Allow: GET, HEAD` + `method_not_allowed` |
| `TestValidationFeedbackRejectsUnknownShapes` | 缺少 `feedback` 段、多余段、未知动作段、`validation` 拼错、`…/validation-feedback/latest` 均返回 JSON `404` |
| `TestValidationFeedbackRequiresService` | 依赖缺失返回 `503`（不是 `404`，也不是空反馈） |
| `TestValidationFeedbackStorageFailureIsNotNotFound` | 存储错误返回 `500` + `internal_error`，消息脱敏为 `服务内部错误` |
| `TestValidationFeedbackQueryIsReadOnly` | 两次查询后验证记录状态、结论与反馈内容完全不变 |
| `TestValidationFeedbackKeepsRunsIndependent` | 同一步骤的两条验证记录各自返回自己的反馈与开发尝试，不串用 |
| `TestValidationFeedbackOmitsUndefinedFields` | 响应不含 `spec` / `spec_snapshot` / `stdout_path` / `stderr_path` / `checks_json` / `prompt` 等未定义字段 |
| `TestValidationFeedbackErrorIsCategoryBased` | 错误类别相互独立，映射不依赖文本匹配 |

### 5.3 真实 SQLite + 真实 HTTP 服务（`tests/integration/local/httpserver/execution_feedback_test.go`）

两条互不相关的任务链（各自含任务、步骤、尝试、已结束的失败验证记录与失败反馈），
依赖注入走正式路径（`app.NewExecutionRecordService` → `httpserver.Options`）。

| 用例 | 覆盖行为 |
|---|---|
| `TestValidationFeedbackOverRealServer` | 查询结果与真实落盘数据逐字段对照（归属、结论、检查数量、检查标识 / 退出码 / 原因 / 输出摘要 / 必需标记），不使用写死 JSON |
| `TestValidationFeedbackDoesNotCrossTasksOverRealServer` | 三种替换路径 ID 的尝试都返回 `404` + `not_found` |
| `TestValidationFeedbackKeepsRunsSeparateOverRealServer` | 同一步骤追加第二条验证记录后，两条记录的反馈互不串用，且各自关联正确的开发尝试 |
| `TestValidationFeedbackNotFoundOverRealServer` | 验证记录存在但没有反馈 → `404` + `not_found` |
| `TestValidationFeedbackWithoutServiceOverRealServer` | 未注入服务时真实服务返回 `503` |

### 5.4 统一服务装配（`tests/unit/server/execution_feedback_test.go`）

| 用例 | 覆盖行为 |
|---|---|
| `TestServerWiresValidationFeedbackQuery` | `server.New` + `Run` 打开真实数据库与迁移，由 `Server` 自行构造执行记录服务并注入 HTTP；反馈查询返回真实数据，不存在的验证记录返回 `404` |

### 5.5 实际执行结果

| 命令 | 结果 |
|---|---|
| `go test -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/unit/server/ ./tests/integration/local/httpserver/` | 通过（4 个包，522 个用例） |
| `go test -count=1 ./tests/unit/...` | 通过（21 个包，1676 个用例） |
| `go test -count=1 ./tests/integration/local/...` | 通过（14 个包，453 个用例） |
| `go test -count=1 ./tests/integration/pi/... ./tests/integration/ssh/...` | 通过（2 个包，7 个用例，真实远程目标已启用，未跳过） |
| `go test -count=1 ./tests/integration/remote/...` | 通过（4 个包，13 个用例，真实远程 Docker 已启用，未跳过） |
| `go test -count=1 ./tests/e2e/...` | 通过（2 个包，58 个用例） |
| `go test -count=1 ./...` | 通过（69 个包，2207 个用例） |
| `gofmt -l internal cmd tests` | 无输出 |
| `go vet ./...` | 无输出 |
| `go build ./...` | 通过 |

**本轮没有失败用例**，也没有被跳过的用例：`tests/config/ssh.yml` 与
`tests/config/docker.yml` 中的 `enable` / `remote_enabled` 均为 `true`，远程用例真实执行。

## 6. 安全边界与剩余风险

### 6.1 `developer_session_ids` 恒为空（既有存储契约缺陷）

`domain.ValidationFailureFeedback.DeveloperSessionIDs` 在领域层存在，但迁移
`0017_validation_feedbacks.sql` 没有为它建列，`validationFeedbackStore` 的写入与读取都
不涉及该字段。因此：

- 写入时传入的 `DeveloperSessionIDs` 会被静默丢弃；
- 读取时该字段恒为零值，接口输出空数组 `[]`。

本轮**不补造数据**、不新增迁移、不修改领域对象：这属于既有的存储契约缺口，修复需要
`ALTER TABLE` 新增列并回填历史记录（历史记录已无法回填），超出"暴露已持久化反馈"的
范围。已由 `TestValidationFeedbackReturnsStructuredChecks` 显式断言"不补造"，并记入
本节。后续若要支持"从反馈直接跳到开发会话"，需要独立的 Schema 变更里程碑。

### 6.2 其余安全边界

- 只读：不创建反馈、不触发 Agent、不执行命令、不修改任何状态（用例断言）；
- 不跨任务泄露：任务、步骤、验证记录三层归属都校验，"不属于"与"不存在"返回同一语义，
  无法用状态码差异探测其他任务是否存在验证记录；
- 不把验证记录的存在误认为反馈必然存在，也不把反馈缺失报告为验证通过；
- `infrastructure_error` 与代码检查失败分别表达，字段语义在 DTO 注释中写明；
- 响应只含持久化快照，不含 `spec` / `spec_snapshot` / 完整输出路径 / 提示词；
- `500` 一律脱敏，原始原因只写服务端日志；
- 未新增数据库迁移，未修改脱敏算法与长度上限。

### 6.3 剩余风险

1. **前端尚未接入**：本轮只做后端 API。Web 的验证历史与逐项检查视图归 **P4-M3（M3-2）**。
2. **无分页**：反馈是"单条验证记录的一条反馈"，检查条目上限由领域层
   （`domain.MaxFeedbackChecks = 20`）约束，不需要分页；若未来放开上限，应在此接口上
   统一处理而不是新增第二个入口。
3. **`…/validations` 列表未返回反馈标识**：前端需要先取列表、再逐条请求反馈。若 M3-2
   实测请求次数过多，可考虑在验证记录上增加 `has_feedback` 布尔字段（服务端计算，
   不改表结构），但那属于 M3 的性能取舍，本轮不提前优化。
4. **历史数据**：`0017` 之前产生的验证失败没有反馈记录，这类验证记录调用本接口返回
   `404`。这是正确语义（"当时没有写入反馈"），不是缺陷。

## 7. 退出条件核对

| 条件 | 状态 |
|---|---|
| 按验证运行 ID 查询反馈的只读接口存在，且与现有验证历史路径层级一致 | 满足（`GET …/validations/{runID}/feedback`，`TestValidationFeedbackReturnsStructuredChecks`） |
| 响应以 `domain.ValidationFailureFeedback` 为唯一数据来源，字段真实存在、未改名、未补造 | 满足（`validationFailureFeedbackResponse` 逐字段映射；`TestValidationFeedbackOmitsUndefinedFields`） |
| `checks` 保留检查标识、名称、必需标记、状态、退出码、失败原因、错误摘要、输出摘要、超时 / 取消标记与执行时间 | 满足（`TestValidationFeedbackReturnsStructuredChecks` 逐项断言） |
| 应用层校验任务 → 步骤 → 验证记录 → 反馈的完整归属链，不只凭 `runID` 读取 | 满足（`requireValidationRun` + 反馈归属复核；`TestGetValidationFailureFeedbackRejectsForeignRun`、`TestValidationFeedbackRejectsForeignScope`） |
| 不跨任务泄露验证记录或反馈 | 满足（应用层与 HTTP 层、真实服务三层用例） |
| 验证记录存在但没有反馈时返回明确未找到，不构造空反馈 | 满足（`TestValidationFeedbackRejectsRunWithoutFeedback`、`TestValidationFeedbackNotFoundOverRealServer`） |
| 任务 / 步骤 / 验证记录 / 反馈不存在均返回一致的未找到语义 | 满足（`TestValidationFeedbackRejectsMissingRecords`） |
| 路径标识非法遵循既有执行记录规则（按未找到处理） | 满足（`TestGetValidationFailureFeedbackRejectsInvalidIDs`、`TestValidationFeedbackRejectsUnknownShapes`） |
| 存储或内部错误返回统一内部错误并记录日志 | 满足（`TestValidationFeedbackStorageFailureIsNotNotFound`） |
| 依赖未初始化返回 `503`，不伪装成空反馈 | 满足（`TestValidationFeedbackRequiresService`、`TestValidationFeedbackWithoutServiceOverRealServer`） |
| 不支持的方法返回 `405` 且设置正确 `Allow` | 满足（`TestValidationFeedbackRejectsUnsupportedMethods`） |
| 查询是只读操作，不创建反馈、不触发 Agent、不执行命令、不修改状态 | 满足（`TestValidationFeedbackQueryIsReadOnly`） |
| 最近一次反馈接口的范围决策已记录依据 | 满足（§2.3，并有用例锁定该形状返回 `404`） |
| 应用层、HTTP 层、真实 SQLite 集成三层测试实际执行并通过 | 满足（§5.5，`go test ./...` 2207 个用例全部通过） |
| 未修改反馈生成规则、脱敏算法与数据库 Schema | 满足（§4"未修改"清单；`git status` 无迁移文件变更） |
| 路线图与交付文档已更新 | 满足（`README.md`、`ROADMAP.md`、`P4-product-experience-optimization.md` 与本文件） |

## 8. 下一阶段

**P4-M2-6：代码保存记录列表 API**（覆盖 G8 的 API 部分）——让 `recordID` 可从 Web 获取，
使 `POST …/code-saves/{recordID}/verify-code-save` 与续跑资格接口能被真实使用。

Web 端的验证历史页面（M3-2）归 P4-M3，不在本轮范围。
