# P3-M5 多任务并发、冲突与资源回收的系统性验收

> 里程碑 ID：`P3-M5`
> 状态：`completed`（2026-10-25，基于 `master` HEAD `abfdfd3` 之后的工作区核实与实施）
> 前置依赖：P3-M2（有界并发）、P3-M3（执行隔离）、P3-M4（调度器恢复）
> 实施依据：本文档编写前重新核对了 `internal/`、`migrations/`、`tests/` 与
> `tests/config/` 的真实代码与工作区状态，不以已有测试名称或上游里程碑的 `completed`
> 状态作为行为正确的证据。

## 0. 结论摘要

本次验收**不是**"补测试文档"，而是先定位真实缺口、再修复、再以针对性测试证明修复
有效。审计发现 **3 个阻塞正确性的生产缺陷**，均已修复：

| ID | 缺陷 | 影响 | 修复位置 |
|---|---|---|---|
| D1 | `TaskService.CancelTask`（CLI 入口）用无条件整行写入 | 取消会覆盖并发领取的 `lease_owner` / `lease_epoch`，新执行者失去执行权却继续推进现场（**已用探针实测复现**） | `internal/domain/task.go`、`internal/app/task.go`、`internal/storage/sqlite/tasks.go` |
| D2 | `CancelTask` 与 `CancelTaskForAPI` 各自实现一套取消状态语义，且 `queued` 任务停在 `cancelling` | 没有任何执行者的任务只能靠重启恢复才收敛（CLI 实测输出 `状态: cancelling`） | `domain.Task.ApplyCancellationRequest`（唯一落点） |
| D3 | `isContainerMissing` 把 `CodeOwnershipMismatch` 当作"容器不存在" | 归属校验失败时把回收报告为"按删除成功处理"，伪造一次没有发生的清理，并让现场失去诊断线索 | `internal/app/execenv.go` |

另外发现并修复 **1 个并发缺陷**：

| ID | 缺陷 | 影响 | 修复位置 |
|---|---|---|---|
| D4 | `TaskScheduler.Scan` 的名额统计与领取不在同一临界区 | 同一调度器的并发扫描各自基于同一份空名额快照领取，实测**上限 2 时领取 3 个** | `internal/app/task_scheduler.go`（`scanMu`） |

真实远程验收已执行并通过（`docker.remote_enabled: true` + `ssh.enable: true`，
远程 Docker CLI / Engine `29.9.1`、API `1.56`），包括真实容器创建、两个任务并行
回收、`unknown` / 孤儿 / absent 现场注入与短暂不可达后重新核验。

## 1. 现状缺口矩阵（实施前）

| 场景 | 已有测试 | 缺失断言或场景 | 是否阻塞正确性 | 修复位置 | 验收测试 |
|---|---|---|---|---|---|
| 两个调度器同时扫描，是否重复领取同一任务 | `TestTwoSchedulersCannotClaimSameTask` | 已有覆盖：第二个调度器不重复领取 | 否 | — | 复用 |
| 两个调度器竞争不同任务时总量是否越过上限 | 无 | 缺"多调度器共享数据库时名额如何仲裁"的显式断言 | 否（语义需明确） | 文档 §3 | `TestTwoSchedulersClaimDistinctTasksWithinCapacity` |
| 同一调度器并发 `Scan` 是否超额领取 | 无 | **完全缺失**，实测可超限 | **是**（D4） | `task_scheduler.go` | `TestSchedulerDoesNotExceedLimitUnderConcurrentScans` |
| 数据库读取失败是否被当成空队列 | 仅租约残留核验有 `TestLeaseRecoveryFailureBlocksStartup` | 调度器 `Scan` 的读取失败路径无断言 | 否 | — | `TestSchedulerReadFailureDoesNotFakeEmptyQueue` |
| 读到空名额但领取失败时的名额一致性 | 无 | 缺失 | 否 | — | `TestSchedulerKeepsSlotConsistentWhenClaimLosesRace` |
| 启动失败是否只释放自己的名额 | `TestSchedulerReleasesLeaseOnLaunchFailure`（单任务） | 缺"另一个并发任务的租约不被释放" | 否 | — | `TestSchedulerLaunchFailureReleasesOnlyOwnSlot` |
| 取消与任务推进并发时取消意图是否丢失 | `TestCancelDoesNotRequireTaskLease` | 缺"取消不覆盖并发租约"的断言；原用例断言了错误语义（见 §2.2） | **是**（D1/D2） | `domain/task.go` 等 | `TestCancelAndAdvanceRaceKeepsCancelIntent`、`TestCancelDoesNotClobberConcurrentLeaseClaim` |
| 失败重试与调度器竞争同一任务 | `TestRetryConcurrentOnlyOneWins`（两个重试并发） | 缺"重试 vs 调度器"的跨入口竞争 | 否 | — | `TestRetryRacesWithSchedulerForSameTask` |
| 回收任务 A 是否影响任务 B 的记录 | `TestRecoveringOneTaskKeepsAnotherTaskContainer` | 只断言容器状态，未断言任务引用与容器收敛结论 | 否 | — | `TestRecoveringOneTaskKeepsAnotherTaskRecords` |
| 并发容器收敛是否互相覆盖 | `TestContainerRecoveryConflictKeepsNewerRecord`（单连接） | 缺两个独立连接并发收敛 | 否 | — | `TestConcurrentContainerRecoveryKeepsOwnership` |
| 归属不匹配时所有处置入口是否拒绝 | `TestTaskContainerCannotBeReusedByAnotherTask`（纯领域） | 缺"拒绝后没有发生任何破坏性操作"的持久化断言 | 否 | — | `TestOwnershipMismatchBlocksAllDestructivePaths` |
| 孤儿容器是否留下可诊断证据 | `TestOrphanContainerIsDiscoverableButNotAutoHandled`、`TestContainerRecoveryReportsOrphanWithoutDeleting` | 缺"事件里同时含容器标识与孤儿阶段" | 否 | — | `TestOrphanContainerKeepsDiagnosticEvidence` |
| 远程不可达时是否保留 `unknown` 与证据 | `TestContainerRecoveryKeepsUnknownWhenUnreachable` | 缺"事件证据"与"真实远程注入" | 否 | — | `TestUnknownContainerKeepsEvidenceWhenUnreachable`（本地）、`TestRemoteDockerRecoveryKeepsSceneAfterTransientFailure`（真实远程） |
| 回收部分失败后剩余现场是否可识别 | 无 | **完全缺失** | 否 | — | `TestReclaimPartialFailureKeepsRemainingScene` |
| 租约失效是否被当作远程已停止 | `TestLeaseRecoveryRecordsResidueWithoutReleasingLease` | 缺"容器与步骤占用不被清除" | 否 | — | `TestLeaseFencingIsNotPhysicalStopEvidence` |
| 并发工作区初始化 / Session 是否串扰 | `TestConcurrentSessionPathsDoNotCollide`（只算路径） | 缺"文件内容不串扰"与"落在工作空间内" | 否 | — | `TestConcurrentWorkspaceInitDoesNotCrossTasks` |
| 真实远程 `unknown` / 孤儿现场主动注入 | 无（P2-M5 与 P3-M3 均记为交接事项） | **完全缺失** | 否 | — | `TestRemoteDockerUnknownAndOrphanScenes` |
| 真实远程两个任务并行与分别回收 | 无 | **完全缺失** | 否 | — | `TestRemoteDockerTwoTasksConcurrentAndReclaim` |
| 真实 SQLite 多连接竞争 | `TestAcquireTaskLeaseIsExclusiveAcrossConnections`（8 连接） | 已有覆盖 | 否 | — | 复用 |

## 2. 修复的真实缺陷

### 2.1 D1：取消的无条件整行写入覆盖并发领取

`TaskService.CancelTask` 使用 `store.UpdateTask`，SQL 为 `WHERE id = ?`，会写入全部列，
包括 `lease_owner` / `lease_expires_at` / `lease_epoch`。

用探针（读取任务后立即插入一次并发领取，再执行取消）实测：

```text
落库状态=cancelling owner="" epoch=0 active=false
并发领取的租约被取消覆盖: owner="" epoch=0
```

修复方式：新增 `storage.TaskStore.UpdateTaskCancellation`，只更新取消相关列
（`status`、`cancel_requested`、`cancel_requested_at`、`final_result`、
`failure_summary`、`finished_at`、`updated_at`），并带 `status IN (...)` 前置条件。

为什么不是简单地换成 `UpdateTaskIfStatus`：那仍然会写租约列。取消是用户意图而不是
执行权交接，它既不需要持有租约，也绝不能交出或抹掉租约——P3-M3 §2.7 与 §3.5 明确
"取消不需要任务租约"，但"不需要"不等于"可以覆盖"。

### 2.2 D2：两套取消状态语义

CLI（`CancelTask`）与 HTTP（`CancelTaskForAPI`）各自实现一套分支，且 `CancelTask`
对 `queued` 任务只 `RequestCancel` 并因 `!IsActive()` 推进到 `cancelling`，**不**
收敛为 `cancelled`。CLI 实测输出：

```text
状态: cancelling
```

而 `queued` 任务没有任何执行者能确认停止，只能靠 `DecideCancellationRecovery`
在下次启动时收敛。这与 `domain.CancelImmediately` 的既定语义（"只有 queued 允许
直接取消"）矛盾。

修复方式：新增 `domain.Task.ApplyCancellationRequest(at) (repeated bool, err error)`
作为取消语义的唯一落点：

- `queued` → `CancelImmediately`（直接 `cancelled`）；
- `cancelling` → 幂等返回 `repeated=true`，不改写时间戳、不重复写事件；
- 终态 → `ErrInvalidState`；
- 其他未结束状态 → `RequestCancel` + 迁移到 `cancelling`（实际停止由执行者确认）。

`CancelTaskForAPI` 保留自己的 `IsFinished()` 前置判定：`CancelImmediately` 只认
`queued`，而 `failed` 也必须被拒绝，因此该判定不能由领域规则隐式承担。

### 2.3 D3：归属不匹配被当作"容器不存在"

`internal/app/execenv.go` 的 `isContainerMissing` 返回
`code == CodeContainerNotFound || code == CodeOwnershipMismatch`。`releaseDelete` 据此
返回"容器已不存在，按删除成功处理"，`ContainerRemoved` / `TaskResourcesRemoved`
都被置为 `true`。

这与 `domain.DecideContainerRecovery` 对 `ContainerObservedOwnershipMismatch` 的结论
（`NeedsManual=true`、不改写记录）直接矛盾，也与 P2-M5 审计记录的分类缺陷一致。

修复方式：只有 `CodeContainerNotFound` 算"确认不存在"。归属不匹配时释放流程返回
错误，容器记录保持原状。

### 2.4 D4：并发扫描超额领取

`TaskScheduler.Scan` 的名额统计（`countOccupiedSlots`）与逐个领取（`dispatch`）不在
同一临界区。两次并发 `Scan` 会读到同一份"还有空名额"的快照，各自再领取自己的份额。
探针实测（上限 2、4 个并发扫描）：

```text
after Start: launched=2 active=2
immediately: launched=1 active=2   ← 并发扫描在此窗口内又领取了 1 个
later: launched=3 active=3
```

修复方式：新增 `scanMu sync.Mutex`，让"统计名额 → 领取"在同一个临界区内完成。该锁
只保护本进程内的扫描，跨进程仍由任务租约的条件更新拒绝重复领取——与既有的
`dispatched` map"第一道防线"定位一致。

## 3. 并发上限的产品语义（明确记录）

**结论：`scheduler.max_concurrent_tasks` 是"同一份数据库上的名额上限"，不是跨数据库
的全局配额。**

语义精确定义（`TestTwoSchedulersClaimDistinctTasksWithinCapacity` 直接验证）：

1. 名额统计的事实来源是数据库（`domain.DecideTaskSlotState` 对
   `tasks.lease_owner` / `lease_expires_at` / `workflow_runs` 的判定）。
2. 因此**多个调度器共享同一数据库时，总量等于单进程上限**，而不是"进程数 × 上限"：
   第二个调度器看到的占用快照已包含前者的名额，不会再领取。
3. 本进程内还有两道额外防线：`dispatched` map 与 `scanMu`（D4 修复）。
4. **不提供**跨数据库的全局配额：两个进程分别持有独立数据库时，各自的上限互不知情。
   这属于明确的非目标，需要跨库仲裁协议才能改变。

选择理由：

- 当前部署形态是"单个 `agentflow server` 进程 + 一份 SQLite"，跨库全局配额没有真实
  需求证据；
- 全局容量表会引入"领取名额与领取任务之间的部分失败""服务停止但远程执行未确认结束"
  "租约过期后旧执行者仍可能运行"等新故障面，而这些正是本项目反复强调"不得把容量
  过期当作远程已停止证据"的地方；
- 用最小协议满足一个不存在的需求，代价高于收益。

明确非目标（不在本次范围，不得为通过测试而偷偷改变）：

- 跨数据库全局容量仲裁；
- 容量名额的续期 / 释放协议与进程崩溃后的名额回收；
- 分布式调度、优先级抢占。

## 4. 实际修改

### 4.1 生产代码

| 文件 | 改动 |
|---|---|
| `internal/domain/task.go` | 新增 `Task.ApplyCancellationRequest`：取消语义的唯一落点（`queued` 直接取消、`cancelling` 幂等、终态拒绝、其余进入 `cancelling`） |
| `internal/app/task.go` | `CancelTask` 改用 `ApplyCancellationRequest` + `UpdateTaskCancellation` |
| `internal/app/task_management.go` | `CancelTaskForAPI` 改用同一领域规则，保留 `IsFinished()` 前置拒绝 |
| `internal/app/task_scheduler.go` | 新增 `scanMu`，让"统计名额 → 领取"串行化 |
| `internal/app/execenv.go` | `isContainerMissing` 只认 `CodeContainerNotFound`，不再把归属不匹配当"不存在" |
| `internal/storage/interfaces.go` | `TaskStore` 新增 `UpdateTaskCancellation`（只写取消列，带状态前置条件） |
| `internal/storage/sqlite/tasks.go` | 实现 `UpdateTaskCancellation` |

### 4.2 测试

| 文件 | 内容 |
|---|---|
| `tests/integration/local/scheduler/concurrency_acceptance_test.go`（新增） | 8 个用例：多调度器容量、并发扫描不超额、读取失败不伪造空队列、领取失败的名额一致性、启动失败只释放自己名额、取消与推进并发、取消不覆盖并发租约、重试 vs 调度器 |
| `tests/integration/local/scheduler/resource_acceptance_test.go`（新增） | 8 个用例：回收 A 不影响 B、并发容器收敛、归属不匹配阻断破坏性入口、孤儿证据、远程不可达证据、回收部分失败、租约失效不等于物理停止、并发 Session 不串扰 |
| `tests/integration/remote/docker/concurrency_acceptance_test.go`（新增） | 3 个真实远程用例：`unknown` / absent / 孤儿现场注入、两个任务并行与分别回收、短暂不可达后重新核验 |
| `tests/integration/local/scheduler/isolation_test.go` | 新增 `path` 字段与 `openSecondConnection`（独立连接辅助） |
| `tests/integration/local/scheduler/lease_fencing_test.go` | 修正 `TestCancelDoesNotRequireTaskLease` 的错误断言：运行中任务取消应进入 `cancelling`，且不写结论与结束时间 |
| `tests/integration/local/execenv/execenv_test.go` | 新增 `TestReleaseRejectsOwnershipMismatchAsMissing`：归属不匹配时释放必须返回错误，不得报告"按删除成功处理"（D3 的回归测试） |
| `tests/e2e/cli/task_test.go` | 修正 `TestTaskCancelMarksRequest`：`queued` 任务取消应直接输出 `状态: cancelled` |

### 4.3 为什么修改了两个已有测试的断言

这两处不是"为了让测试通过而放宽断言"，而是**原断言把缺陷写成了期望**：

- `TestCancelDoesNotRequireTaskLease` 原断言 `cancelled.Status == developing`，并注释
  "状态保持 developing，由执行路径把它经 cancelling 收敛"。但 `CancelTask` 的代码
  从不把任务推进到 `cancelling`（它只写取消请求），`domain.ApplyCancellationRequest`
  与 `CancelTaskForAPI` 的既有语义都是"立即进入 `cancelling`"。修正后的断言更严格：
  状态必须是 `cancelling`，且 `FinalResult` 必须仍是 `none`、`FinishedAt` 必须为空
  （即"没有伪造已停止"）。
- `TestTaskCancelMarksRequest` 原断言 `queued` 任务输出 `状态: cancelling`，这直接
  暴露 D2。修正后断言 `状态: cancelled`，与 `domain.CancelImmediately` 一致。

## 5. 实际执行的命令与结果

全部命令在仓库根目录执行（Windows 11 + Git Bash，Go 1.24.5）。

### 5.1 静态检查

```bash
gofmt -l internal cmd tests   # 无输出
go vet ./...                  # 无问题
go build ./...                # 成功
```

### 5.2 本地测试（真实 SQLite + 正式迁移 + 多独立连接）

```bash
go test -count=1 ./tests/unit/... ./tests/integration/local/...
```

结果：**2107 passed / 0 failed / 0 skipped，37 个包**。

其中本次新增的用例：

```bash
go test -count=1 ./tests/integration/local/scheduler/...
```

结果：**76 passed / 0 failed**（含原有 60 个 + 新增 16 个）。

### 5.3 CLI / 打包端到端

```bash
go test -count=1 ./tests/e2e/...
```

结果：**58 passed / 0 failed**（`tests/e2e/...` 两个包）。

### 5.4 真实远程验收（已执行）

配置前提（`tests/config/`，本地不入库）：

- `tests/config/ssh.yml`：`ssh.enable: true`，`ssh.host: 192.168.91.20`、`ssh.user: root`、
  `ssh.private_key_path: ${HOME}/.ssh/ssh_id_rsa`、`ssh.known_hosts: []`（复用本机
  OpenSSH `known_hosts`，只读快照）；
- `tests/config/docker.yml`：`docker.remote_enabled: true`、
  `docker.agent_image` / `docker.git_image: agentflow-test-agent:1.0.0`；
- `tests/config/pi.yml`：`pi.enable: true`、`pi.model: group/free`；
- `tests/config/git.yml`：`git.repository` 指向测试仓库，SSH 私钥鉴权。

```bash
go test -count=1 -v ./tests/integration/remote/docker/... ./tests/integration/remote/workflow/...
```

结果：**10 passed / 0 failed / 0 skipped**。逐项：

| 用例 | 结果 | 关键证据 |
|---|---|---|
| `TestRemoteDockerPreflight` | PASS | Docker CLI / Engine `29.9.1`、API `1.56`（≥ 基线 `29.0.0` / `1.52`） |
| `TestRemoteDockerUnknownAndOrphanScenes`（新增） | PASS | `REMOTE_UNKNOWN_SCENE` / `REMOTE_ABSENT_SCENE` / `REMOTE_ORPHAN_SCENE` 三个真实现场，孤儿容器未被自动删除 |
| `TestRemoteDockerTwoTasksConcurrentAndReclaim`（新增） | PASS | `REMOTE_TASK_A` / `REMOTE_TASK_B` 两个真实容器；回收 A 后 B 仍运行且归属不变 |
| `TestRemoteDockerRecoveryKeepsSceneAfterTransientFailure`（新增） | PASS | `REMOTE_FLAKY_SCENE ... final_state=running`：不可达窗口内保持 `unknown` 且真实容器仍在运行，恢复后收敛为 `running` |
| `TestRemoteDockerContainerLifecycle` | PASS | 真实镜像构建、容器创建 / 启动 / 执行 / 日志 / 停止 / 删除 |
| `TestRemoteExecPassesWorkDirAndEnv` | PASS | 真实 `docker exec` 的 `--workdir` / `--env` |
| `TestRemoteWorkflowRequiresHostKeyVerification` | PASS | 主机密钥校验不可关闭 |
| `TestRemoteWorkflowPreconditions` | PASS | — |
| `TestRemoteE2EPreconditions` | PASS | — |
| `TestRemoteE2EFullChain` | PASS（67.89s） | 真实 SSH + 真实 Docker + 容器内真实 Git + 容器内真实 Pi + `group/free` 模型 |

### 5.5 缺陷修复的回归验证（注入法）

"新增测试是否真的能发现缺陷"不能靠推断。对 4 个修复逐项做了**回退验证**：把修复临时
撤销、保留测试、观察测试是否失败，再恢复修复。

| 缺陷 | 回退方式 | 测试结果 | 结论 |
|---|---|---|---|
| D1 | `UpdateTaskCancellation` 的 SQL 重新写入租约列 | `TestCancelDoesNotClobberConcurrentLeaseClaim` 失败：`并发领取的租约被取消覆盖: owner="" epoch=0` | 测试能发现 |
| D2 | `ApplyCancellationRequest` 去掉 `queued` 分支、恢复 `!IsActive()` 判定 | `TestTaskCancelMarksRequest` 失败：`取消输出缺少 "状态: cancelled"`，实际 `状态: cancelling` | 测试能发现 |
| D3 | `isContainerMissing` 重新接受 `CodeOwnershipMismatch` | `TestReleaseRejectsOwnershipMismatchAsMissing` 失败：`归属不匹配时释放必须返回错误，不得报告删除成功` | 测试能发现 |
| D4 | 去掉 `Scan` 的 `scanMu` | `TestSchedulerDoesNotExceedLimitUnderConcurrentScans` 连续 3 次失败：`实际 5` / `实际 6` / `实际 5`（上限 2） | 测试能发现，且可重复 |

D4 的测试原本只靠并发 goroutine 撞窗口，回退验证时**偶发通过**。为此改成确定性构造：
受控存储包装 `scanBarrier` 让第一次名额统计读取停在屏障上，其余扫描在此期间推进。
未串行化时它们会读到同一份空名额快照并各自领取，必然超限；串行化时它们阻塞在临界区外。
修复后连续 3 次通过（每次约 3.5s，含屏障超时兜底），回退后连续 3 次失败。

### 5.6 一次与本里程碑无关的偶发失败（如实记录）

在高并发同时跑全部包时，`tests/unit/app` 的
`TestWorkflowHeartbeatKeepsLeaseAlive` 与 `TestWorkflowStartDoesNotStealLiveLease`
各出现过一次失败（"租约必须被延长"、"有效租约存在时不得抢占，实际 blocked"）。

单独重跑这两个用例连续 3 次通过；随后完整重跑 `./tests/unit/...`、
`./tests/integration/local/...`、`./tests/e2e/...` 连续 2 次全部通过。
这两个用例依赖真实时间窗口（心跳间隔与租约有效期），在数十个包并行压满 CPU 时会
出现时序抖动，与本次改动无调用关系（本次未修改 `task_workflow_lease.go` 或心跳逻辑）。
按"不把未执行/失败报告为通过"的要求，此处如实记录该偶发失败，不掩盖。

### 5.7 未执行项

| 项目 | 原因 | 运行前提 |
|---|---|---|
| `tests/integration/remote/codesaver` | 本次未涉及代码保存路径的改动，且该目录不在 P3-M5 §6 要求的验收范围内 | `docker.git_image` 已配置；`go test -count=1 ./tests/integration/remote/codesaver/...` |
| `tests/integration/remote/agent` | 同上（容器内 Agent 能力已由 `TestRemoteE2EFullChain` 间接覆盖） | `docker.pi.*` 齐全；`go test -count=1 ./tests/integration/remote/agent/...` |
| 前端测试（`pnpm test` / `pnpm lint`） | 本次未修改前端代码 | 在 `lingting-agent-flow-ui/` 下执行 |

**注意**：本地用例**不能**替代真实远程证据。本节所有"PASS"均来自真实远程执行，
未把本地替身用例的结果混入远程结论。

## 6. 测试产物

所有新增测试遵守既有测试架构：入口为 `testkit.Main`，工作空间为
`os.TempDir()/lingting-agent-flow-tests/` 下的随机目录，产物保留不删除。

本次运行的产物位置（`TEST_ARTIFACT_DIR`，取最近一次完整运行）：

```text
C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\scheduler-<随机>\    # 本地调度器与资源验收
C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\docker-<随机>\       # 真实远程 Docker 验收
C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-<随机>\     # 真实远程端到端
```

每个用例目录下包含 `agentflow.db`（真实 SQLite + 正式迁移）、`sessions/`、
`validations/` 与运行日志；本次运行的完整输出写在 `<运行根目录>/test.log`，
基目录下的 `latest-run.txt` 指向该文件。

失败诊断信息（任务 ID、执行者身份、租约 owner/epoch、容器与 Session 归属、
关键状态转换）通过 `t.Logf` 与 `slog` 一并写入 `test.log`；真实远程用例额外输出
`REMOTE_*_SCENE` / `REMOTE_TASK_*` / `REMOTE_FLAKY_SCENE` 行。

远程测试资源位置：每次用例在远程 Home 下使用带随机后缀的独立目录
（`~/.lingting/agentflow-tests/docker/<随机后缀>`，旧路径 `~/agentflow-tests` 已废弃），
容器名带同一后缀，清理只删除本次创建并已核验所有权的容器，并在删除后核验容器确实
已不存在；不执行全局 prune。容器零残留的最终核验机制见
[`remote-test-resource-cleanup.md`](./remote-test-resource-cleanup.md)。

## 7. 完成定义核对

| # | 条件 | 结论 | 证据 |
|---|---|---|---|
| 1 | 多任务并发、领取冲突与执行权 fencing 有可复核的真实 SQLite 测试 | ✅ | `concurrency_acceptance_test.go`（8 个用例，真实 SQLite + 独立连接）、`lease_store_test.go`（8 连接并发领取） |
| 2 | 并发上限语义明确，测试与产品契约一致 | ✅ | §3 语义定义；`TestTwoSchedulersClaimDistinctTasksWithinCapacity`、`TestSchedulerDoesNotExceedLimitUnderConcurrentScans` |
| 3 | 任务取消、失败、重试与资源回收不会破坏其他任务 | ✅ | `TestCancelAndAdvanceRaceKeepsCancelIntent`、`TestCancelDoesNotClobberConcurrentLeaseClaim`、`TestSchedulerLaunchFailureReleasesOnlyOwnSlot`、`TestRecoveringOneTaskKeepsAnotherTaskRecords` |
| 4 | 容器、工作区、Session 与共享缓存的归属与回收边界得到验证 | ✅ | `resource_acceptance_test.go`、`TestSharedCacheVolumesAreNotTaskScoped`（复用）、`TestConcurrentWorkspaceInitDoesNotCrossTasks` |
| 5 | `unknown` 与孤儿容器不会被错误删除或自动重新关联 | ✅ | `TestOrphanContainerKeepsDiagnosticEvidence`、`TestUnknownContainerKeepsEvidenceWhenUnreachable`、`TestRemoteDockerUnknownAndOrphanScenes` |
| 6 | 关键数据库错误与部分失败不会被伪装为成功 | ✅ | `TestSchedulerReadFailureDoesNotFakeEmptyQueue`、`TestReclaimPartialFailureKeepsRemainingScene`、`TestReleaseRejectsOwnershipMismatchAsMissing`；D3 修复关闭"归属不匹配报告为删除成功"；4 个修复均做过回退验证（§5.5） |
| 7 | 已具备条件的真实远程测试有真实执行记录 | ✅ | §5.4（10 passed，含 3 个新增真实远程用例）；不具备条件的项目在 §5.7 明确记录 |
| 8 | 路线图、测试结果与剩余风险一致 | ✅ | `README.md`、`ROADMAP.md`、`P3-multi-task-scheduling.md` 已同步指向本文档 |

## 8. 已知限制、风险与后续入口

### 8.1 明确不在本里程碑范围

- 跨数据库全局容量仲裁（§3 明确非目标）；
- 事件驱动调度、优先级抢占、大规模重构；
- 前端并发状态展示（本次未涉及前端）；
- 分布式多节点调度。

### 8.2 剩余风险

| 风险 | 说明 | 后续入口 |
|---|---|---|
| R1 | 并发上限仍是"同一份数据库"的范围，两个进程各持独立数据库时互不知情 | 需要跨库仲裁协议时单独立项 |
| R2 | 数据库 fencing 只拒绝旧执行者的写入，不证明远程命令已停止 | 需要节点侧可观测能力（P3-M4 已记录，本次以 `TestLeaseFencingIsNotPhysicalStopEvidence` 固化边界） |
| R3 | 真实远程 `unknown` 现场的"注入"用受控观测端口包裹真实连接，不是真的切断网络 | 真实的网络分区注入需要节点侧能力；本次已覆盖"不可达 → 保留 unknown → 恢复后收敛"的完整状态迁移 |
| R4 | `scanMu` 只保护单进程；跨进程超额领取由任务租约的重复领取拒绝兜底，但名额总量仍可能短暂超过单进程上限 | 与 R1 同源；当前部署形态下不构成实际风险 |
| R5 | `UpdateTaskCancellation` 与 `UpdateTaskIfStatus` 是两条写入路径，未来新增取消相关列时必须同步更新前者 | 已在 `internal/storage/interfaces.go` 的注释中写明"只更新取消相关列"，并在 SQL 里显式列出列名 |
| R6 | 真实远程验收依赖本机 `tests/config/*.yml`，这些文件不入库 | 其他环境复现需按 §5.4 准备配置 |

### 8.3 下一步入口

P3 阶段的 5 个里程碑（M1–M5）全部 `completed`。下一步按 `ROADMAP.md` 的优先级
选择规则，进入 **P4 —— 产品操作体验与针对性优化**，入口见
[`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md)。

P4 启动前需先确认的工作（不构成本次范围）：

- 前端尚未实现的重试入口（P3-M4 交接事项）；
- 真实远程 `unknown` 现场的网络分区注入能力；
- 跨进程 / 跨库全局并发配额是否有真实需求。
