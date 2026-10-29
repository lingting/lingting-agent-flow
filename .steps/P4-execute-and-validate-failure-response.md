# P4 常规开发 —— 开发—验证闭环失败时保留结构化执行结果

> 基线核实：`git fetch origin` 后 `origin/master` = `HEAD` = `630ea421b5f78467d4ee76ad5b677b6462a210dd`，
> 当前分支 `master`，`git status -uno` 无输出（工作区干净）。实施依据为该基线的实际代码核实，
> 不沿用任何历史结论。本轮不属于 P4-M1 的 G 项，也不属于 P4-M5。

## 1. 原始问题与代码证据

闭环的结构化结果类型早已存在，但失败响应没有利用它，调用方只能拿到一条错误消息。

### 1.1 后端证据（修改前）

- `internal/httpapi/step_workflow.go` 的 `serveExecuteAndValidate`：`h.workflow.ExecuteAndValidate`
  返回 `(result, err)` 后，`err != nil` 直接 `h.writeWorkflowError(w, r, result, err)`；该函数只用
  `result` 生成说明文本（`workflowNotPersistedMessage`），**从不把 `result` 放进响应体**。
- 同一函数的两个业务失败分支（`ValidationRan && !ValidationPersisted`、
  `!result.Verified`）虽然已经构造了 `response := executeAndValidateResponse(result)`，却只调用
  统一 `writeError(...)` 输出状态码 / 错误代码 / 消息，`response` 仅用于 200 分支。
- `internal/httpapi/response.go` 的 `ErrorResponse` 只有 `Error ErrorBody` 一个字段，
  `writeError` 也没有承载额外结构化事实的参数。

于是"已真实执行、已产生逐项检查与修复轮次"的失败，在响应体里与"前置条件不满足"的失败完全同形。

### 1.2 前端证据（修改前）

- `src/lib/api/client.ts` 的 `ApiError` 已保存解析后的响应体 `body`，能力本身存在；
- `useExecutionOperations.ts` 的失败分支只保留 `describeApiError(cause)` 生成的一条消息
  （`failure: { ...target, message }`），没有读取 `cause.body`；
- `OperationOutcomePanel.tsx` 的 `failure` 提示只渲染 `failure.message`，因此即使服务端给了
  结构化事实，界面也不会展示；
- `steps.ts` 已有 `describeExecuteAndValidateResult`，可以从 `ExecuteAndValidateStepResponse`
  翻译出逐条事实，但只在 200 路径被调用。

## 2. 失败响应契约的最终选择与兼容性依据

### 2.1 选择

在统一错误结构外层增加一个**可选**字段，承载既有闭环响应 DTO：

```json
{
  "error": { "code": "execution_failed", "message": "...", "request_id": "..." },
  "operation_result": { "...": "与 200 响应完全相同的 ExecuteAndValidateStepResponse" }
}
```

- Go：`httpapi.ErrorResponse` 增加
  `OperationResult *ExecuteAndValidateStepResponse \`json:"operation_result,omitempty"\``；
  新增 `writeErrorWithOperationResult(...)`，`operationResult` 为 `nil` 时与 `writeError` 等价。
- 复用同一 DTO 与同一映射函数 `executeAndValidateResponse(result)`，失败路径不维护第二套字段翻译。

### 2.2 兼容性依据

- `error.code` / `error.message` / `error.request_id` 与 HTTP 状态码**全部不变**；
  `!result.Verified` 仍是 `500 execution_failed`，`ValidationRan && !ValidationPersisted` 仍是
  `500 execution_not_persisted`，业务失败没有被改写成 200，也没有新增成功语义。
- 字段带 `omitempty`：没有执行事实的响应仍然是旧形状，其他接口的 `ErrorResponse`
  消费者（多处测试按结构体反序列化）不受影响。
- 选择放在错误结构外层而不是 `error` 内部：`ErrorBody` 的语义是"错误描述"，
  而执行事实不是错误的一部分，它是与错误并列的独立事实。

### 2.3 出现条件（安全边界）

只有同时满足以下条件才附加 `operation_result`：

1. 本次请求确实创建并执行过一次开发尝试，即 `app.ExecuteAndValidateResult.AttemptID` 非空；
2. 该失败不是被隐藏的"未知内部错误"（`code != internal_error`）。

因此以下情况**不附加**，只返回原有错误响应：

- 参数非法（400）、目标不存在或归属不符（404）、状态冲突（409）、前置条件缺失（412）；
- 验证配置无效（`ErrStepWorkflowInvalidConfig`，发生在开发之前）；
- 修复预算 / 现场判定拒绝（`ErrStepWorkflowCycleDenied`）；
- `code == internal_error` 的未知异常：此时无法确认结构化事实是否可信，而且结构化字段里可能
  带出底层错误原文，既有内部错误隐藏策略必须继续生效。

### 2.4 保留与省略的执行事实

保留（全部来自既有 DTO，字段值与 200 路径同源，不含原始 stdout / stderr）：

- 开发结论、退出码、开发摘要与失败原因、`developer_finalized` 与写入失败原因；
- `validation_ran` / `validation_skip_reason` / `validation_run_id` / `validation_status` /
  `validation_verdict` / `validation_passed` / `validation_summary` /
  `validation_failure_summary`；
- 逐项检查摘要（`checks`：状态、退出码、超时 / 取消 / 确定性与输出摘要，不含完整输出）；
- `validation_persisted` 与持久化失败原因、`infrastructure_error`、`feedback_persisted`；
- 语义审查（`review_ran` / `review_status` / `review_summary` / `assessment`）；
- `verification_state` / `step_status` / `task_status` / `verified`；
- 修复事实（`round` / `attempts` / `max_attempts` / `repair_decision` / `repair_class` /
  `repair_summary`）、`finalized` 与收口失败原因、起止时间。

省略：任何未产生的事实都保持为空或 `false`（例如验证未发起时 `validation_ran=false`、
`checks` 为空、`validation_status` 为空），不做默认值推断；不新增原始输出、容器日志、
调用栈、凭据或任意路径字段；不改动 DTO 本身的字段集合。

## 3. 前端消费与语义边界

1. `src/pages/tasks/steps.ts` 新增 `executeAndValidateResultFromBody(body: unknown)`：对
   `operation_result` 做结构校验（`task_id` / `step_id` / `validation_ran` /
   `validation_persisted` / `verified` / `step_status` / `task_status` / `repair_decision`
   的类型检查），不合格即返回 `undefined`，不做任何强制类型转换。
2. `useExecutionOperations.ts`：失败分支用 `failureFactsOf(cause)` 读取 `ApiError.body` 并交给
   既有 `describeExecuteAndValidateResult` 翻译成逐条事实，存入 `failure.facts`；其他动作的
   错误响应没有该字段，`facts` 为 `undefined`，显示方式不变。防重、任务票据、迟到响应丢弃与
   刷新逻辑均未改动。
3. `OperationOutcomePanel.tsx`：`failure` 提示仍然只使用 `type="error"`；标题在有结构化事实时
   变为"…未通过"，否则仍是"…失败"；描述先显示服务端错误消息，再以
   "服务端本次执行详情（不代表操作成功）"分组列出逐条事实。
4. **业务失败与结果不确定继续分开**：只有拿到服务端响应（`ApiError.kind === "http"`）才可能有
   `operation_result`；超时 / 网络中断 / 主动取消仍只由 `uncertain` 提示"结果无法确认"，
   `facts` 为空，且不自动重发。`verified=false` 的响应不会被渲染成成功样式。

## 4. 修改文件

生产代码：

| 文件 | 改动 |
|---|---|
| `internal/httpapi/response.go` | `ErrorResponse` 新增可选 `OperationResult`；新增 `writeErrorWithOperationResult` |
| `internal/httpapi/step_workflow.go` | 两个业务失败分支改用 `writeErrorWithOperationResult`；`writeWorkflowError` 挂载执行事实（`internal_error` 除外）；新增 `workflowOperationResult` 作为唯一附加判定 |
| `lingting-agent-flow-ui/src/pages/tasks/steps.ts` | 新增失败响应体的结构守卫 `executeAndValidateResultFromBody` |
| `lingting-agent-flow-ui/src/pages/tasks/useExecutionOperations.ts` | 新增 `OperationFailure`（含 `facts`）与 `failureFactsOf`，失败分支保留结构化事实 |
| `lingting-agent-flow-ui/src/pages/tasks/OperationOutcomePanel.tsx` | 失败提示展示服务端执行详情，标题区分"未通过" |

既有测试文件（只更新契约与断言，未新建测试文件）：

| 文件 | 改动 |
|---|---|
| `tests/unit/httpapi/step_workflow_test.go` | 验证失败 / 开发失败的 500 响应必须携带 `operation_result` 且 `verified=false`；404 / 409 / 400 不得携带 |
| `lingting-agent-flow-ui/tests/unit/task-execution-api.test.ts` | 闭环 500 的 mock 增加 `operation_result`，断言 `error.body` 保留该事实 |
| `lingting-agent-flow-ui/tests/unit/task-execution-state.test.ts` | 断言守卫能读出失败响应体中的事实，且无 `operation_result` 的错误响应返回 `undefined` |

## 5. 实际执行的非测试检查

| 检查 | 结果 |
|---|---|
| `gofmt -l internal cmd tests` | 无输出 |
| `go vet ./...` | 无输出 |
| `go build ./...` | 通过 |
| `tsc --noEmit`（`lingting-agent-flow-ui`） | 通过 |
| `vp lint`（5 个改动文件） | 无输出 |
| `vp fmt --check`（5 个改动文件） | 本次改动文件格式正确（`useExecutionOperations.ts` 由 `vp fmt --write` 只对该文件格式化一次） |
| `git diff --check` | 无输出 |

**未执行任何测试**（Go 与前端）：本轮未获得测试授权。前端 `pnpm test:unit:safe` /
`pnpm test:e2e:safe`、Go 的 `go test ./...` 均未运行，因此新增 / 更新的断言是否正确**尚未验证**。

剩余风险：

1. Go 侧新断言的细节依赖真实执行链路：`validation_verdict=failed`、`outcome=failed` 与
   `checks` 非空均由既有引擎行为推出，未运行测试确认。
2. 前端失败面板的标题变化（"未通过"）只在服务端返回 `operation_result` 时出现；现有 E2E 用例
   的 mock 未包含该字段，因此标题仍为"失败"，不会与既有断言冲突——这条判断同样未通过运行验证。
3. `finalization_errors` / `validation_persistence_errors` 会随结构化结果出现。这些文本在
   `execution_not_persisted` 的既有错误消息里本来就会出现，本轮没有扩大暴露面；但若未来把
   `operation_result` 附加到 `internal_error`，就会越过内部错误隐藏边界（当前已显式排除）。
4. 失败响应仍不包含原始 stdout / stderr 与完整容器输出，用户需要完整输出时仍要查执行记录。

## 6. 与 P4-M5 的关系

本轮不涉及性能测量、并发调整或任何数据依赖型优化，`P4-M5` **仍为 `pending`**。
