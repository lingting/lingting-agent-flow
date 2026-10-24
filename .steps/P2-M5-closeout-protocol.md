# 任务收尾一致性协议

> 撰写依据：`master` HEAD `40460d4` 之后 P2-M5 的实际代码。
> 上游依据：[`P2-M1-recovery-boundaries.md`](./P2-M1-recovery-boundaries.md) §2.13、
> [`P2-reliability-recovery.md`](./P2-reliability-recovery.md) 的 P2-M5 详情。

## 0. 要解决的问题

任务级收尾需要同时完成三件事：执行记录进入终态、任务进入成功终态、写入对外结论。
这三件事分布在 `tasks` 与 `workflow_runs` 两张表，**无法共享一个事务**。此前的实现
按"任务 → 执行记录"顺序写入，一旦第二步失败，就会留下：

```text
tasks.status       = completed          ← 已宣称成功
tasks.final_result = succeeded
workflow_runs.status = recovery_required ← 收尾其实没闭环
```

而 `completed` 是任务状态机的终态（`taskStatusMachine.terminal`），任何恢复都无法修正。
这不是"记录不一致"，而是**伪造成功**：管理接口、任务列表与调用方都会把它当作确定成功。

## 1. 协议

### 1.1 写入顺序（栅栏）

```text
1  依据真实证据计算 decision = domain.DecideWorkflowCloseout(...)
2  若 decision 不是 completed：
       stopWithEvent(blocked | recovery_required)   ← 不改写任务
3  若 decision 是 completed：
   3.1  run.Complete(summary)                       ← 纯领域，仅内存
   3.2  persistRun(run)                             ← 【栅栏】以 revision 为条件
        冲突 / 租约已转移 / 数据库不可用 → 直接返回，**绝不写任务成功终态**
        其它错误                         → stopWithEvent(recovery_required, closeout_failed)
   3.3  completeTask(task)                          ← 栅栏通过后才允许
        失败 → markTaskRecoveryRequired(task, task_completion_failed)
                （saving→recovery_required、verifying→recovery_required 均合法）
   3.4  追加 workflow.completed 事件
```

代码位置：`internal/app/task_workflow_exec.go` 的 `applyCloseout`、
`markTaskRecoveryRequired`、`stopUnpersistedCloseout`。

### 1.2 为什么这样是对的

| 失败点 | 留下的现场 | 是否伪造成功 | 是否可收敛 |
|---|---|---|---|
| 3.2 之前 | `task=saving|verifying`、`run=running` | 否 | 是（启动恢复 `recoverStalledDelivery`） |
| 3.2 冲突（执行权已转移） | 他人写入的版本 | 否 | 不归本次执行者处理 |
| 3.2 其它错误 | `task=saving`、`run=running → stopWithEvent` | 否 | 是 |
| 3.3 失败 | `run=completed`、`task=recovery_required` | 否（明确"未完成"） | 是 |

关键：**栅栏通过后才可能写出任务成功终态，而栅栏使用的是带 `revision` 前置条件的
条件写入。** 条件写入失败意味着"我已经不再拥有这条执行记录"，此时写任务成功正是
C3（丢失执行权仍宣布成功）的成因。

### 1.3 恢复

`TaskWorkflowService.Start` 的顺序固定为：

```text
recoverUnfinished          // 未结束执行记录：cancel_requested→cancelled，
                           // 过期 running→blocked(worker_interrupted)，超窗 pending→blocked
recoverCancellationIntents // 取消意图跨层补齐（B5）
recoverStalledDelivery     // 【本协议】verifying / saving 停滞任务
reportCloseoutInconsistencies // 【本协议】遗留 completed + 未完成执行记录的只读报告
recoverPlanGeneration      // 停留在 planning 的任务
recoverCancelling          // 停留在 cancelling 的任务
```

`recoverStalledDelivery` 的判定由 `domain.DecideTaskCloseoutRecovery` 给出：

| 现场 | 结论 |
|---|---|
| 任务不是 `verifying` / `saving` | 不处理 |
| 执行记录 `running` 且租约有效 | 不处理（仍有执行者） |
| 执行记录 `completed` 且重算证据为通过 | 补齐任务 `completed` + `final_result=succeeded` |
| 执行记录 `completed` 但证据不成立 | `recovery_required` / `closeout_evidence_missing` |
| 其它（`blocked` / `recovery_required` / `failed` / 无记录） | `recovery_required` / `closeout_interrupted` |

"重算证据"复用 `finalize` 的同一组来源（步骤完成情况、已持久化的代码保存记录、
会话归档），但**不执行任何副作用**：不重新提交、不重新推送、不重新调用 Agent。

### 1.4 遗留不一致只报告

`completed` 是终态。本轮**不**允许把它回退，因此对已经存在的
`task=completed` + 执行记录未完成 的现场，只做：

- 追加 `task.recovery_required` 事件（payload `stage=closeout_reconcile`，含
  `task_status`、`final_result`、`run_id`、`run_status`）；
- 写一条 `WARN` 日志；
- 在 API / CLI 派生结论中标记 `closeout=inconsistent`。

## 2. 对外表达

`domain.DecideTaskCloseoutView` 派生只读结论，不写库：

| 任务状态 | 执行记录 | 派生结论 |
|---|---|---|
| 已删除 | 任意 | `unconfirmed` |
| `cancelled` | 任意 | `cancelled` |
| `completed` + `final_result=succeeded` | `completed` | `confirmed` |
| `completed` + `final_result=succeeded` | 无记录 | `inconsistent` |
| `completed` + `final_result=succeeded` | `running` 且租约有效 | `pending` |
| `completed` + `final_result=succeeded` | 其它 | `inconsistent` |
| `completed` 但 `final_result≠succeeded` | 任意 | `unconfirmed` |
| 其它 | `running` 且租约有效 | `pending` |
| 其它 | 其它 | `unconfirmed` |

对外字段（`TaskResponse`）：

```json
{
  "status": "completed",
  "status_terminal": true,
  "status_finished": true,
  "final_result": "succeeded",
  "closeout": "inconsistent",
  "closeout_summary": "任务被标记为成功，但工作流执行记录没有确认完成；该现场必须人工核验，不得当作确定成功"
}
```

调用方判断"任务是否真的成功"只能看 `closeout == "confirmed"`，不能只看 `status`。

## 3. 为什么不引入新状态

备选方案与取舍：

| 方案 | 做法 | 代价 |
|---|---|---|
| **A（采用）** | 顺序 + 栅栏 + 恢复 + 读侧派生 | 无 schema 变更、无状态机变更；遗留数据只能被发现、不能自动修 |
| B | 允许 `completed → recovery_required` | `completed` 不再是终态；必须审计全部读取方（`IsTerminal` / `IsFinished` / `IsActive` / `CanStart` / `AllowsStepExecution`、任务 API、前端状态映射、删除判定）并同时清空 `FinalResult` / `FinishedAt` |
| C | 新增 `tasks.closeout_state` 列 | 引入第二套"收尾状态"，与 `workflow_runs` 语义重复，违反"不把编排语义塞进任务字段" |

若将来采用 B，必须同时提供：状态机新边、`Task.ReopenForRecovery` 之类的方法、
上述全部读取方的审计清单，以及"回退后 `final_result` 与 `finished_at` 的清理规则"。

## 4. 测试

| 场景 | 用例 |
|---|---|
| 写入顺序为"执行记录 → 任务" | `TestCloseoutWritesRunBeforeTask` |
| 栅栏通过、任务写入失败不伪造成功 | `TestCloseoutTaskWriteFailsAfterRunCompleted` |
| 栅栏失败、不写任务成功终态 | `TestCloseoutRunWriteFailsBeforeTaskWrite` |
| 停滞 `saving` 被收敛为可继续处理 | `TestRestartConvergesTaskStuckInSaving` |
| 证据齐备时补齐成功终态 | `TestRestartCompletesTaskFromCompletedRun` |
| 遗留不一致只报告不改写 | `TestRestartFlagsLegacyCompletedWithRecoveryRun` |
| 派生组合矩阵 | `tests/unit/domain/task_closeout_test.go` |
