# M6：删除流程与端到端验收

> 状态：**completed**（2026-11-09）。停止与需求聚合逻辑删除、HTTP 接口、前端交互与自动化用例已交付；
> 真实环境验收 A–E 全部取得可复核证据，V1 全链路回归通过，详见 §7 / §8。
> 前置：M5 completed。参考：[`../WORKFLOW.md`](../WORKFLOW.md) §8、§9。

## 1. 目标

补齐需求的收尾操作，并用真实链路完成 V2 验收：

1. 支持停止需求下正在运行的任务。
2. 支持需求逻辑删除与关联任务逻辑删除，完整保留执行历史。
3. 未收敛的任务不能被删除，也不能被伪装成已停止。
4. 端到端验证场景 A–E 在真实环境中成立。

## 2. 范围

### 2.1 App

- `RequirementService.Delete`：准入校验（无运行中、无 `cancelling`、无 `recovery_required`、
  无结果不确定）→ 逻辑删除需求与关联任务 → 记录事件；不删除任何执行历史。
- 停止入口复用既有任务取消语义（`cancel_requested` → `cancelling` → 真实收敛）。
- `ProjectExecutionService` 在需求删除后不得再领取该需求的任务。

### 2.2 HTTP

```text
DELETE /api/v1/requirements/{requirementID}
POST   /api/v1/projects/{projectID}/cancel      （已有，M5 交付）
```

- 删除请求在存在执行中任务时返回 409（并说明需要先停止），不自动取消。
- 删除成功返回真实结果（删除需求 ID 与任务数量），不返回"可能已删除"。

### 2.3 前端

- 需求详情提供"停止任务"与"删除需求"，禁用态必须有原因。
- 删除前明确提示：不自动回滚 Git，历史记录仍保留但不再出现在默认视图。
- 删除后相关页面不残留可点击但必然失败的操作。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 运行中的任务不能被直接删除 | 存在执行中任务时删除返回 409 |
| 不确定状态不能被伪装为已停止 | `recovery_required` / 结果不确定同样拒绝删除 |
| 删除不物理清理历史 | Step / Attempt / Session / Event / CodeSave 记录仍可查询 |
| Git 回滚仍由用户执行 | 删除路径不调用任何 Reset / Revert / Force Push |
| 完整需求到多任务链路通过验收 | 场景 A–E 全部成立并有可复核证据 |

## 4. 验收方式

场景 A–E 必须**真实执行**（不是代码阅读）：

| 场景 | 关键证据 |
|---|---|
| A 单需求多任务 | 会话多轮复用同一容器；确认后 1 需求 + N 任务；顺序执行；进度与数据库一致 |
| B 规划阶段只读 | 文件工具与 Shell 写入均被拒绝；无提交 / 推送 / 合并记录 |
| C 不同项目并行 | 两项目同时执行且各自串行；全局并发不超限 |
| D 同项目失败阻塞 | A2 未自动开始；A 显示阻塞；B 正常执行 |
| E 需求删除 | 未收敛拒绝；收敛后逻辑删除；历史仍可查询 |

- 测试与真实环境验证默认需要用户明确授权后才能执行。
- 未执行、跳过、失败的场景必须如实报告，不得写成通过。

## 4.1 已交付范围

### 4.1.1 Domain

- `domain.Requirement.MarkDeleted(at)` / `Deleted()`：只写 `deleted_at` 与 `updated_at`，重复删除返回
  `ErrInvalidState`，业务状态、确认快照与规划来源不变。
- `domain.RequirementDeletionTaskStatuses()`（`queued` / `completed` / `failed` / `cancelled`）与
  `Task.AllowsRequirementDeletion()`：**需求聚合删除专用**白名单，未知状态失败关闭；`Task.MarkDeleted`
  与单任务删除接口的状态集合未改动。
- `StepStatus.RequiresManualVerification()`：`running` / `developing` / `verifying` / `recovery_required`
  视为现场未收敛；`TaskQueueRejection` 的占用判定改为复用 `StepStatus.BeginsExecution()`（行为不变）。
- `EventTypeRequirementDeleted`（`requirement.deleted`）：需求删除写进每个关联 Task 的时间线。

### 4.1.2 Storage

- `RequirementStore.DeleteRequirement(ctx, id, at)`：条件 UPDATE（`deleted_at IS NULL`），0 行按
  `storage.ErrConflict` 返回；不新增表、不新增列、不新增迁移。
- 任务软删除复用 `TaskStore.DeleteTask`，由应用层传入需求级状态集合；`deleted_at IS NULL` 与状态集合共同
  保证并发只有一个成功。

### 4.1.3 App

- `RequirementService.Delete`：`RequirementServiceOptions.Coordination` 缺失时返回
  `ErrRequirementDeleteUnavailable`（失败关闭）；删除前按任务标识升序取得全部关联任务的执行权（同一个
  `TaskExecutionCoordinator`），任一占用 → 409 并释放已取得执行权；单事务内重读需求与任务、核验任务集合
  未变化、核验状态与执行现场（活动 WorkflowRun / 未收敛步骤 / `DecideTaskCodeSaveResume` 判定为结果不明
  的外部副作用）、逐个条件软删除任务、条件软删除需求、追加事件；任一步失败整体回滚，事务结束释放执行权。
- 返回真实结果 `RequirementDeleteResult`（需求、软删除任务数量、删除时间）。
- 读取路径收口：`Detail` / `Progress` 对已删除需求返回 `ErrRequirementNotFound`。

### 4.1.4 HTTP 与前端

- `DELETE /api/v1/requirements/{requirementID}`：`200` + `{requirement_id, deleted_task_count, deleted_at}`；
  404（不存在/已删除/重复删除）、409（未收敛或现场不确定）、500（事务、SQL、事件失败）、503（删除能力未装配）；
  路径标识非法沿用共享 `pathIDGuard` 的 404，与其余路径标识接口一致。
- 需求页新增「停止任务」（复用 `POST /api/v1/tasks/{id}/cancel`，只作用于该需求的任务）与「删除需求」
  （二次确认弹窗含任务数量、各任务状态与历史保留 / Git 不回滚 / 容器不处置说明）；不安全状态禁用并给出
  原因；`409` 与网络中断后重新读取服务端事实且不自动重发；切换需求清除旧状态。

## 5. 风险与待决

1. ~~删除范围：已结束任务是否随需求一起逻辑删除~~ **已定稿**：已结束任务与尚未启动的 `queued` 任务
   一并逻辑删除（`PRODUCT-SCOPE.md` §7 第 1 条），历史记录保留并可按任务继续查询。
2. **收敛等待时长**：前端不引入自动重试与超时判定，`cancelling` 期间只能展示"停止中"并允许用户手动
   刷新；服务端不做等待，直接以 409 拒绝未收敛的删除请求。
3. **孤儿容器**：删除需求不自动删除容器；容器处置仍走既有显式 `delete` / `detach`。

## 6. 明确不做

- 不自动回滚 Git 代码。
- 不物理删除执行历史、Session 归档或代码保存记录。
- 不实现正式需求的修改与重新规划（删除重建是唯一纠正路径）。
- 不实现用户权限与操作审计。

## 7. 已执行检查与测试

授权范围：本次任务获得用户明确的测试授权（创建并执行 Go 与前端测试）与真实环境验收授权。

已执行并通过：

- `gofmt -l internal tests`、`go build ./...`、`go vet ./...`：无输出。
- `go test ./tests/unit/domain/ ./tests/unit/store/`：通过。新增 `tests/unit/domain/requirement_deletion_test.go`
  与 `tests/unit/store/requirement_deletion_test.go`。
- `go test ./tests/unit/app/ ./tests/unit/httpapi/`：通过。新增 `tests/unit/app/requirement_delete_test.go`
  与 `tests/unit/httpapi/requirement_delete_test.go`。
- `go test -race ./tests/unit/app -run 'TestDelete|TestConcurrentRequirementDelete'` 与
  `go test -race ./tests/unit/store -run 'Delet|Concurrent'`：通过。
- 前端 `npx vp test run`（361 个用例，含新增 `tests/unit/requirement-actions.test.ts`）：通过。
- 前端 `npx playwright test --workers=4`（297 个用例，含新增 `tests/e2e/requirement-delete.spec.ts` 与
  `tests/e2e/requirements-backend.ts`）：通过。
- 前端 `tsc --noEmit`、`vp lint`、`node scripts/build-webui.mjs`（生产构建）：通过。

覆盖的守点（摘录）：

- 领域：`MarkDeleted` 只写删除时间且保留快照 / 确认时间 / 规划来源，重复删除冲突；需求级白名单只含
  四个安全状态，全部活动状态、`paused`、`recovery_required` 与未知状态被拒绝；`Task.MarkDeleted`
  仍拒绝 `queued`。
- 存储：条件软删除成功/重复冲突、按需求级集合软删除 `queued`、集合外状态冲突、默认列表过滤、
  项目视图过滤、事务回滚（事件写入失败、需求条件写入失败、任务软删除失败）、并发删除只有一个成功、
  Step / Attempt / Session / Event 保留、未知状态原样读回。
- 应用：成功删除的真实任务数量与字段不被改写、需求详情与进度按不存在处理、每个任务一条
  `requirement.deleted` 事件、十种不安全状态与未知状态全部拒绝且无部分删除、活动 WorkflowRun、
  未收敛步骤、结果不明代码保存副作用拒绝删除、已占用执行权拒绝删除并释放此前取得的执行权、删除成功后
  全部执行权释放、删除事务期间其他入口无法取得执行权（确定性屏障）、并发删除只有一个成功、删除一个
  需求不影响其他需求的执行占用、删除后项目调度视图不再包含该需求任务。
- HTTP：`200` + 真实任务数量与删除时间、404（不存在 / 重复删除）、409（`preparing` / `developing` /
  `cancelling` / `recovery_required` / 未知状态）、503（未装配服务或缺少协调器）、405 + Allow、
  事务失败与事件失败返回 500 且整体回滚。
- 前端：删除准入与停止范围纯函数、错误分类、`DELETE` 路径与方法、执行中 → 停止 → `cancelling` →
  收敛后可删除、二次确认内容、成功后清空选择与列表、`409` 后重新读取、网络中断不自动重发、
  未知 / 待核验状态只读展示、切换需求不残留旧状态、窄屏布局。

顺带修正的既有测试漂移：`tests/e2e/layout.spec.ts` 的一级路由清单缺 M5 新增的 `/requirements` 导航项，
导致「桌面视口显示完整侧栏与导航」在 M6 之前即失败；本次补齐该表项（不改产品代码）。

### 7.1 真实环境验收结果（2026-11-09）

授权范围：用户明确授权在本任务中执行真实环境验收（SSH `127.0.0.1:22` + 远程 Docker + 容器内 Pi 模型
`group/free`）。

| 场景 | 用例 | 结果 |
|---|---|---|
| A 执行半段 | `TestRemoteM6RequirementSequentialTasks` | PASS（321s） |
| C 跨项目并行 + D 同项目失败阻塞 | `TestRemoteM6ProjectSerialParallelAndFailureBlocking` | PASS（264s） |
| E 需求删除 | `TestRemoteM6DeleteRequirementWhileRunning` | PASS（69s） |
| B 规划阶段只读 | `TestRemotePlanningReadOnlyEnvironment` | PASS（27s） |
| A 规划半段（多轮会话复用同一容器） | `TestRemotePlanningMultiRoundSessions` | PASS（33s） |
| V1 全链路回归 | `TestRemoteE2EFullChain` | PASS（100s） |

每次运行结束的 `REMOTE_CONTAINER_VERIFY` 均为 PASS，远端只保留基线容器 `pgsql-server`。

真实证据摘录：

- **A 执行半段**：1 个项目 → 1 个需求 → 2 个带 `sh -c "test -f <file>"` 确定性验证命令的任务；任务 1 真实
  `completed` 后任务 2 被自动领取并真实 `completed`；每项目在途峰值 1；需求进度 `completed`（current=2）；
  项目投影 `idle`；两任务各留下 step / attempt / session / code save 记录，且都推送成功（真实 commit SHA）。
- **A 规划半段**：两角色多轮会话在同一规划容器内连续，上下文与归档一致（既有真实用例，本轮重新取证）。
- **B 规划只读**：文件工具与 Shell 写入均被只读文件系统拒绝，无提交 / 推送 / 合并记录。
- **C 跨项目并行**：两个项目同时执行，各自严格串行（在途峰值各 1），全局在途不超过配置上限 3。
- **D 同项目失败阻塞**：A1 三次尝试后验证未通过 → 工作流 `blocked`（`attempts_exhausted`，带 `error_class`）→
  任务收敛 `recovery_required`；A2 保持 `queued`；项目 A 显示 `blocked` 且原因为 `task_recovery_required`；
  项目 B 不受影响，正常 `completed`。
- **E 需求删除**：运行中删除 → 409 且无任何部分软删除；停止 → 真实收敛 `cancelled`；删除 → 200
  （`deleted_task_count=2`）；任务状态未被改写（`cancelled` / `queued`）；事件 8 条（含 `requirement.deleted`）；
  项目任务事实为空；容器保留且 `condition=running`（未删除、未停止）。

### 7.2 本轮修复的缺陷（均由真实环境暴露）

1. **启动阶段失败后任务永久停在 `cancelling`**：启动阶段失败后释放执行权时没有任何执行者收口取消现场，
   任务永久停在 `cancelling`，需求无法删除、项目也无法重新领取。修复：新增
   `internal/app/task_launch_cancellation.go` 的 `TaskLaunchService.SettleCancelledLaunch`，按
   `domain.DecideCancellationRecovery` 收敛（仍在 `cancelling` 且**无**活动执行记录才收敛）；`Launch` 失败且
   工作流未接手时在释放执行权之前调用，`TaskScheduler.settleFailedDispatch` 经可选端口 `cancellationSettler`
   调用同一收口（该路径持有执行权，`Launch` 内不再重复收敛）。
2. **工作流以 `blocked` / `recovery_required` 停止后任务状态不收敛**：验证未通过或尝试耗尽时执行记录
   `blocked`，任务仍停在 `developing`，项目闸门永久"项目内已有任务在执行"，执行权不释放、后续任务永不
   领取、需求不可删除。修复：`internal/app/task_workflow_exec.go` 的 `stopWithEvent` 增加
   `convergeStoppedTask`，把任务收敛为 `recovery_required`（写 `FailureSummary` 与 `task.recovery_required`
   事件）。选 `recovery_required` 而不是 `failed` 见 §9 第 7 条。
3. **场景 C/D 用例自身的死锁**（测试缺陷，不是产品缺陷）：并发采样器把"取峰值"与"停止监视"拆成两步，调用
   顺序反了导致断言前永久阻塞，上一次运行的"未取得完成证据"由此而来。修复：采样函数改为"停止 + 返回峰值"
   一次完成，并注册 `t.Cleanup` 兜底。

## 8. 未执行项与已知边界

- 真实环境验收 A–E 与 V1 全链路回归均已执行并取得可复核证据（§7.1）：没有跳过、没有放宽断言，也没有
  用"结果不确定"当通过。
- 已知边界（这些是范围与环境的限制，不是通过证据）：
  - 场景 E 的删除发生在代码保存之前（任务停在 `planning` 后被取消），"删除发生在代码保存之后且结果不明"
    的现场只用应用层单元用例覆盖，未在真实环境构造；
  - `dead` / `removing` 容器条件在真实环境仍未观测（M2 已记录），M6 未涉及；
  - 并发删除、执行权占用与事务回滚等并发守点只在单元与 `-race` 用例中验证，真实环境未做并发压测；
  - 真实环境用例依赖本机 WSL 中的 SSH 目标与远端 Docker 处于运行状态；目标不可达时用例直接失败（不跳过、
    不伪造结果）。
- 环境前提（缺一不可）：可 SSH 的 Linux 节点（`tests/config/ssh.yml`）、该节点上的 Docker 与
  `tests/config/docker.yml` 的镜像、`tests/config/git.yml` 的可访问仓库与凭据、容器内 Pi 的模型接入
  （`tests/config/docker.yml` 的 `docker.pi` 或镜像内配置）。

## 9. 交付细节与关键取舍

1. **停止与删除彻底分开**：`DELETE` 不隐式取消任务，未收敛返回 409；早期 `WORKFLOW.md` §8 的
   「删除时先发起取消」写法已按 M6 §2 定稿替换。
2. **需求级删除白名单独立**：`queued` 允许随需求删除，避免"前序失败阻塞后续任务"时用户必须伪造任务
   状态；单任务删除接口的状态集合与 `Task.MarkDeleted` 语义均未改动。
3. **执行现场失败关闭**：除任务状态外还核验活动 WorkflowRun、未收敛步骤与 `DecideTaskCodeSaveResume`
   判定的结果不明副作用；任一读取失败按无法确认拒绝。
4. **并发保护复用既有协调器**：删除使用 Server 装配的同一 `TaskExecutionCoordinator`，按任务标识升序
   取得全部执行权，失败与成功后都释放；不新建第二把任务锁，也不等待在途执行完成。
5. **事件不新开需求时间线**：需求删除写进每个关联 Task 的时间线并带 `requirement_id`，与软删除同事务。
6. **路径标识非法沿用 404**：与任务、项目等所有路径标识接口一致，由共享 `pathIDGuard` 给出「接口不
   存在」；400 仅用于请求体与协议级参数错误。
7. **停止后收敛为 `recovery_required` 而不是 `failed`**：`failed` 会让 `CheckWorkflowStart`、
   `DecideTaskExecutionHandover` 与队列准入都拒绝 resume，等于删掉既有恢复入口；选 `recovery_required`
   的代价是项目阻塞原因显示为 `task_recovery_required`，且需求在人工处置前不可删除。
8. **需求删除准入包含"代码保存结果不明的外部副作用"**：比"只看任务状态"更严格，宁可让用户先人工核验，
   也不在 Git 结果不确定时删除需求记录。
