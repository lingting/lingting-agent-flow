# M5：项目级调度与进度

> 状态：**completed**（2026-11-08）。
> 前置：M4 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §4.2 / §4.3 / §6 / §7，
> [`../DATA-MODEL.md`](../DATA-MODEL.md) §3，[`../../../docs/architecture.md`](../../../docs/architecture.md)
> 「项目级执行协调边界」，[`../../../docs/http-api.md`](../../../docs/http-api.md) §17。

## 1. 目标

让多个项目并行、同一项目串行，并把真实执行状态聚合成项目与需求进度：

1. 新增 Project Execution Lease，同一项目同时最多 1 个 Task 在执行。
2. 不同项目可以并行，仍受全局并发上限 `scheduler.max_concurrent_tasks` 约束。
3. 失败 / 待恢复 / 结果不确定时阻止同项目后续任务，其他项目不受影响。
4. 项目任务列表与执行记录聚合查询；需求级任务进度；项目级执行状态。

## 2. 范围（已交付）

### 2.1 Domain

- `ProjectExecutionLease` 与 `DecideProjectLeaseAcquire` / `DecideProjectLeaseRenew` /
  `DecideProjectLeaseRelease`：纯判定，不读数据库、不取时钟。规则：没有登记 → epoch 1；已到期 →
  epoch + 1；未到期 → `ErrProjectLeaseHeld`；续租 / 释放必须 `owner + epoch` 匹配。
- `ProjectExecutionPlan` / `DecideProjectExecutionPlan` / `DecideProjectExecutionClaim`：项目内排序、
  队首分类、可领取任务与拒绝原因（稳定字符串）。
- `RequirementProgress` / `ProjectProgress` 与 `DecideRequirementProgress` / `DecideProjectExecution`：
  需求与项目进度聚合。

### 2.2 App

- `ProjectExecutionService`：`Start` / `Cancel` / `Execution` / `ListTasks`、项目闸门
  （`FilterClaims` / `Reconcile`）与 `RenewLeases` / `ReleaseOnStop`。
- `ProjectExecutionKeeper`：后台续租与收敛（TTL 2 分钟、周期 40 秒），注册进 `internal/lifecycle`。
- `TaskScheduler` 只增加 `ProjectGate` 钩子：扫描前 `Reconcile`，分派前 `FilterClaims`；不新增第二个
  调度循环。
- `TaskLaunchService.Launch` 增加一次只读核验：带项目关联的任务在同项目已有在途任务时拒绝启动，
  防止显式入口绕过项目执行权并发执行两个任务。
- `RequirementService.Progress`：需求进度只读查询，复用同一份领域判定。

### 2.3 持久化

- 迁移 `00004_project_execution_leases.sql`：
  - `project_execution_leases`（`project_id` 主键、`owner`、单调 `epoch`、`expires_at`、`acquired_at`、
    `updated_at`，按 `owner` 的索引）；
  - `project_execution_states`（`project_id` 主键、`state` 无白名单、可空 `stop_requested_at`、
    `updated_at`）。
- 无外键、无枚举白名单、无触发器、无回填。不修改既有迁移脚本。

### 2.4 HTTP

```text
POST /api/v1/projects/{projectID}/start
POST /api/v1/projects/{projectID}/cancel
GET  /api/v1/projects/{projectID}/execution
GET  /api/v1/projects/{projectID}/tasks
GET  /api/v1/requirements/{requirementID}/progress
```

开始 / 停止不接受请求体；查询无副作用。

### 2.5 前端

- 项目页新增执行面板（状态、进度统计、开始/停止、执行权、当前/下一个任务、按顺序的任务与阻塞原因）。
- 新增需求页 `/requirements`（项目 → 需求 → 进度与任务顺序），支持 `?requirement_id=` 深链接。
- 任务页支持 `?task_id=` 深链接，使"需求 → 关联任务"落在真实记录上。

## 3. 退出条件

| 条件 | 判定方式（代码层面） |
|---|---|
| 同项目不能领取第二个执行中的任务 | 闸门只放行 `ProjectExecutionPlan.Claimable`（队首 queued）；显式启动入口另有同项目在途核验；并发开始由租约条件写入保证只有一个成功 |
| 不同项目可以同时运行 | 租约按 `project_id` 隔离，闸门按项目独立判定；一个项目的拒绝不影响其他项目 |
| 全局并发不超限 | 仍走既有 `countOccupiedSlots` 与 `scheduler.max_concurrent_tasks`，项目串行未改变名额统计 |
| 项目内任务严格按顺序推进 | `DecideProjectExecutionPlan` 的排序与"第一个非 completed 任务"规则；仅 `completed` 可被跳过 |
| 失败 / 取消 / 待恢复 / 不确定阻止继续领取 | 队首分类为 blocked 时 `FilterClaims` 拒绝；`Reconcile` 释放执行权并把意图记为 blocked |
| 一个项目阻塞不影响其他项目 | 闸门按项目缓存判定，读取失败只拒绝该项目自己的候选 |
| 租约过期 / 续租 / 释放 / epoch fencing | 条件 SQL：取得仅允许到期行、续租与释放带 `owner + epoch`；旧 epoch 写入影响行数为 0 |
| 服务重启不重复发起不确定操作 | 新进程 owner 不同，不会自动领取项目任务；不重放远程调用，只由既有恢复路径按数据库事实收敛 |
| 项目与需求进度只来源于真实 Task 状态 | 唯一实现为 `domain.DecideRequirementProgress` / `DecideProjectExecution`；`current = completed` |
| HTTP 与前端展示与服务端事实一致 | 五个接口只做协议转换；前端开始/停止后重新读取状态，未知取值原样展示 |

## 4. 验收方式与执行结果

已执行（静态检查，不涉及测试）：

- `go build ./...`、`go vet ./...`、`gofmt -l internal/`：无输出。
- 前端 `tsc --noEmit`：无错误；`node scripts/build-webui.mjs` 生产构建成功。
- `git diff` 自查：未改动 V1 工作流的状态机、验证、重试、代码保存与收敛语义（唯一新增的是
  `TaskLaunchService.Launch` 的一次只读项目串行核验）。

**未执行**：所有自动化测试、真实环境（真实 SSH / Docker / 容器内 Pi）验收、并发与竞态验证。
本里程碑未获测试授权，因此没有创建或运行任何测试文件与临时脚本。

## 5. 风险与待决（已定稿）

1. **自动连续领取**：已定稿为自动连续领取，失败 / 待恢复 / 取消即停；不提供逐任务确认与"跳过前序
   任务"的入口。
2. **租约续租周期与超时**：TTL 2 分钟、续租与收敛周期 40 秒（续租周期必须小于 TTL，构造时校验）。
3. **进度展示口径**：`counts` 含 `cancelled` 与 `paused`，`cancelled` **不并入** `failed`；
   `current = completed`；阻塞原因以稳定原因码表达并在前端映射文案。
4. **项目停止语义**：先落库停止意图 → 调用既有取消入口 → 任务真实收敛后释放执行权；不确定现场保留
   真实状态（详见 [`../WORKFLOW.md`](../WORKFLOW.md) §6.3）。

## 6. 明确不做

- 不做需求删除（M6）。
- 不做任务分支自动串接与合并。
- 不做跨项目依赖、任务 DAG 与优先级抢占。
- 不修改 V1 任务级工作流的收敛语义。
- 不提供全局并发上限 / 在跑任务数的 HTTP 出口（项目执行状态接口不返回这类数字）。

## 7. 交付细节与关键取舍

1. **调度入口唯一**：保留 `TaskScheduler` 作为唯一分派循环，项目执行权以"闸门"形式约束候选资格；
   `ProjectExecutionService` 不调用 `TaskLaunchService`，也不新增后台项目扫描器。
2. **执行权载体**：按方案书落库（`project_execution_leases`），而不是进程内 map；它不用于多实例
   仲裁，只用于跨请求、跨重启识别执行权归属与停止意图（单 Server 进程锁不变）。
3. **停止意图载体**：独立的 `project_execution_states`，与执行权生命周期分离：租约释放不影响
   "用户已请求停止"这一事实的展示。
4. **需求状态双口径**：`requirements.status`（V1 枚举，未改动）与 `RequirementProgress`（M5 投影）
   分开；接口只返回投影状态，不修改持久化状态。
5. **需求详情页**：仓库此前没有需求详情页，M5 新增 `/requirements` 页面；任务详情沿用任务页的
   页内选择并以 `?task_id=` 深链接进入。

## 8. 已知边界与后续事项

1. 显式 `resume-workflow` 与单步骤执行入口属于 V1 恢复语义，M5 未加项目串行核验；只有统一启动路径
   （`TaskLaunchService.Launch`，含重试入口）受约束。
2. `Start` 只复用领域启动准入，不做 SSH / Docker 远程环境预检；远程前置条件仍由统一启动流程在真实
   执行时判定，失败按阻塞处理。
3. 取消与首次分派之间存在极短窗口：已被领取的任务会真实运行后再被停止请求取消，项目不会伪造
   "已停止"。
4. 文档同步已完成（`WORKFLOW.md` §4.2 / §4.3 / §6 / §7、`DATA-MODEL.md` §2.2 / §3 / §4 / §6、
   `ARCHITECTURE.md` §2 / §3 / §3.1 / §5 / §6 / §7 / §8、`ROADMAP.md` 与 `.steps/V2/README.md` 的状态表，
   以及 `docs/architecture.md`、`docs/http-api.md` §17、`docs/frontend.md` §9、`docs/development.md`）。
   另外修正两处与实现不符的历史描述：`AGENTS.md` 补充说明 `project_execution_leases` 不是多实例仲裁，
   `.steps/V1-final.md` 标注 `task_leases` 已随单 Server 架构删除。根目录 `README.md` 不需要改动：
   它不含里程碑状态、接口清单或数据库重建说明（后者在 `docs/development.md`，已存在）。
