# M5：项目级调度与进度

> 状态：**pending**。
> 前置：M4 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §6、§7，
> [`../DATA-MODEL.md`](../DATA-MODEL.md) `0030`。

## 1. 目标

让多个项目并行、同一项目串行，并把真实执行状态聚合成项目与需求进度：

1. 新增 Project Execution Lease，同一项目同时最多 1 个 Task 在执行。
2. 不同项目可以并行，仍受全局并发上限 `scheduler.max_concurrent_tasks` 约束。
3. 失败 / 待恢复 / 结果不确定时阻止同项目后续任务，其他项目不受影响。
4. 项目任务列表与执行记录聚合查询；需求级任务进度；项目级执行状态。

## 2. 范围

### 2.1 Domain

- `ProjectExecutionLease`：获取 / 续租 / 释放的判定规则（含 `epoch` fencing）。
- `DecideRequirementProgress` / `DecideProjectExecution`：纯函数，输入只有 Task 真实状态。

### 2.2 App

- `ProjectExecutionService.Start(projectID)` / `Cancel(projectID)`：
  获取租约 → 选择下一个 Task → 调用既有 `TaskLaunchService` 与 `TaskWorkflowService`。
- 并发保护：事务条件写入为唯一保证，进程内按项目串行化只是第一道防线。
- 服务重启恢复：过期租约可被重新获取；未过期不得抢占；不重放 Agent 调用。

### 2.3 持久化

- 迁移 `0030_project_execution_leases.sql`。
- 不改动 `task_leases`（任务租约与项目租约并存，互不替代）。

### 2.4 HTTP

```text
POST /api/v1/projects/{projectID}/start
POST /api/v1/projects/{projectID}/cancel
GET  /api/v1/projects/{projectID}/execution
GET  /api/v1/projects/{projectID}/tasks
GET  /api/v1/requirements/{requirementID}/progress
```

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 同项目不能领取第二个执行中的任务 | 并发领取只有一次成功；第二次得到明确的"项目正忙" |
| 不同项目可以同时运行 | 两个项目各自持有租约并有执行中的任务 |
| 全局并发不超限 | 执行中任务数不超过配置上限 |
| 服务重启不造成租约失控 | 过期租约可重新获取；未过期不被抢占 |
| 旧 Worker 不得覆盖新状态 | 迟到写入被条件更新拒绝，冲突时读取数据库真实值 |
| 项目进度来源于真实 Task 状态 | 进度字段与数据库任务状态逐一对应 |
| 一个项目失败不阻塞其他项目 | A 阻塞时 B 仍可执行 |

## 4. 验收方式（默认不自动执行）

- 阅读代码：租约事务边界、`epoch` 条件写入、候选任务选择顺序、失败停止点、
  进度纯函数与 DTO 映射、重启恢复路径。
- `gofmt`、`go vet ./...`。
- 若用户明确要求测试，覆盖：并发领取、跨项目并行、租约过期与抢占、迟到结果拒绝、
  失败阻塞、进度聚合（含 `cancelled` / `recovery_required` 组合）。

## 5. 风险与待决

1. **自动连续领取 vs 每任务人工确认**（`WORKFLOW.md` §10 第 1 条）。默认倾向自动连续领取，
   失败即停；若改为人工确认，接口需要新增显式"执行下一个"动作。
2. **租约续租周期与超时**：必须在 M5 定稿并与任务级租约语义对齐，否则会出现"项目租约过期
   但任务仍在执行"的窗口。
3. **进度展示口径**：`current/total` 与"阻塞原因"是否同屏展示，以及 `cancelled` 是否计入
   `failed`，需固定规则并只在一处实现。
4. **项目停止语义**：`cancel` 是取消在途任务还是仅停止领取下一个任务，必须在实现前固定。

## 6. 明确不做

- 不做需求删除（M6）。
- 不做任务分支自动串接与合并。
- 不做跨项目依赖、任务 DAG 与优先级抢占。
- 不修改 V1 任务级工作流的收敛语义。
