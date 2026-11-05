# 组件评估：`golang.org/x/sync/errgroup`

> **状态：不引入（维持仅使用标准库）。** 本轮为纯审计，未修改任何生产代码、测试、`go.mod` / `go.sum`，
> 未引入依赖，未运行 `go test`、构建、`vet` 或其他验证命令。落地动作是"什么都不做"。

- 审计时间：2026-11-05
- 审计基线：`422470249be10b81f2d5b868b55ef4360c1985c8`（`:art: 路由使用 ServeMux 路由表`）
  - 审计时 `git status --short -uno` 无输出（工作区干净），报告中的行号与结论对应该 SHA
- 审计对象：仓库全部 Go 代码（`internal/`、`cmd/`、`tests/`），重点为并发编排、后台服务生命周期与
  取消传播路径；不评估前端与文档
- Go 版本：`go.mod` 声明 `go 1.24.5`，本机工具链 `go1.24.5 windows/amd64`
- 审计方式：
  1. 全仓库检索 `errgroup`、`sync`、`go func`、`context.With*` 等并发原语（含 `go <method>` 形式）；
  2. 逐文件阅读命中点的完整调用链（准入 → 取消 → 等待 → 收敛 → 落库），而不是只看关键词数量；
  3. 阅读本机模块缓存中的 `golang.org/x/sync@v0.17.0/errgroup/errgroup.go` 源码（151 行），
     本文所有组件语义结论都能对应到该文件的具体行为，而不是凭印象复述文档。
- 相关文档：[architecture.md](../architecture.md)（执行链路边界与状态分层）、[workspace.md](../workspace.md)
  （包职责）、[development.md](../development.md)（依赖与改动约定）

## 1. 结论摘要

1. **仓库当前没有使用 `errgroup`**，也没有任何 `golang.org/x/sync` 的包导入。
   `go.sum` 中出现的 `golang.org/x/sync` 记录来自传递依赖（`golang.org/x/exp`、`modernc.org/libc`），
   与本仓库代码无关。
2. **仓库里不存在任务书第 4.1 节所述意义下的 `errgroup` 目标场景**：全仓库没有任何"一组短期、同质、需要把错误汇总
   到本次调用返回值"的并行批次。所有 `sync.WaitGroup` 都是**在途请求计数**（admission + 停止时等待收敛），
   所有 `go func()` 都是**长期循环、取消桥接或单请求流水线**，没有一处是"并行子任务 + 错误聚合"。
3. `errgroup` 能提供的三项净能力中，两项在本仓库是**负收益或有冲突**：
   - 错误汇聚：`Group.Wait` 只返回**第一个**错误（`errOnce`），其余错误被丢弃；而本仓库的错误处理方式是
     **逐条写库 + 逐条日志**（`TaskScheduler.settleFailedDispatch`、`EnvironmentBuildManager.execute`、
     `domain.EnvironmentVersion.MarkUncertain`），并不汇聚成一个返回值。
   - 协同取消：`WithContext` 的派生 ctx 会在"第一个子任务返回错误"和"`Wait` 返回"两个时刻被取消；
     本仓库的取消原因是**业务语义**（`ErrWorkflowStopped` / `errWorkflowCancelRequested` /
     `errWorkflowLeaseLost` / `ErrStepExecutorStopped`），由 `context.Cause` 在 8 处读取并据此决定
     收敛路径。把原因换成"某个子任务的错误"会改变业务判定。
   - 等待：`Wait` 没有 ctx 版本。本仓库每个 `Stop` 都需要 `select { <-done; <-ctx.Done(); <-time.After(timeout) }`，
     即使换成 `errgroup` 也必须保留"把等待放进 goroutine + `close(done)`"的写法，代码量不减。
4. 唯一有相对收益的能力是 `SetLimit` / `TryGo`（进程内并发上限），但本项目的并发上限是**跨进程语义**：
   `TaskScheduler.countOccupiedSlots` + `domain.DecideTaskSlotState` 按数据库中的真实占用与任务租约计算，
   `errgroup.SetLimit` 只是进程内信号量，替换会削弱约束（详见 6.1）。
5. **最终建议：不引入。** 现存并发结构保持不变；`golang.org/x/sync` 不作为直接依赖写入 `go.mod`。
6. 未来重新评估的触发条件见第 10 节（典型信号：出现同一请求内多目标并行探测 / 多仓库并行拉取 /
   批量环境初始化这类"任一失败即放弃其余"的同质批次）。

## 2. 组件简介与适用范围

`golang.org/x/sync/errgroup` 是 Go 官方维护的扩展库，官方定位是"`sync.WaitGroup` 的衍生：为**同一个总体任务**
的子任务组增加错误处理"。`Group` 的文档明确写了两条前提，直接决定了它的适用边界：

- `A Group should not be reused for different tasks.`（一次性、不可复用）
- `The first call to Go must happen before a Wait.`（`Wait` 开始后不得再 `Go`）

### 2.1 能力与对应问题（v0.17.0 源码行为）

| 能力 | 源码行为 | 解决的问题 | 代价 / 约束 |
|---|---|---|---|
| `Go(f func() error)` | `sem <- token` 后 `wg.Add(1)` 起 goroutine；`f` 返回非 nil 时通过 `errOnce` 记录**第一个**错误并 `cancel(err)` | 并行子任务 + 把子任务错误带回调用方 | 只有第一个错误可见；`panics` 不被捕获（源码注释明确拒绝 recover，见 `errgroup.go` 内的 `#53757/#74275/...` 说明）；`Go` 必须在 `Wait` 之前 |
| `Wait() error` | `wg.Wait()` 后 `g.cancel(g.err)`，返回 `g.err` | 等待组内全部完成 | 无 ctx 参数，无法响应取消与超时；返回时**无条件取消**派生 ctx |
| `WithContext(ctx)` | `context.WithCancelCause(ctx)`，`cancel` 由 `Group` 持有 | 首个错误自动取消兄弟任务 | 取消原因只有一个（首个错误）；`Wait` 返回也会取消，与"子任务自行收敛"冲突 |
| `SetLimit(n)` | `n<0` 取消限制；`len(g.sem)!=0` 时 **panic**；`n=0` 阻塞后续所有 `Go` | 进程内并发上限 | 有活动 goroutine 时不可修改；`n=0` 与 `Go` 搭配会死锁；限额只在本进程内有效 |
| `TryGo(f) bool` | 尝试占用 `sem`，占不到直接返回 `false` | 非阻塞提交（丢弃/降级） | 与 `Go` 混用时语义要自己保证；同样只在本进程内有效 |

### 2.2 适用场景

`errgroup` 的匹配面很窄且清晰：**一次调用内、生命周期短、彼此同质、结果只服务于本次调用** 的并行子任务，
典型如"并行请求 N 个 URL，任一失败就放弃整体"。它不处理：

- 长生命周期、可复用、需要"拒绝新任务 → 取消在途 → 等待收敛"三段式停止的服务；
- 结果必须落库、且失败要按条隔离的业务执行（任务、步骤、构建、校验）；
- 有租约 / epoch fencing / 不确定结果收敛要求的执行链路；
- 有明确依赖顺序的启动与关闭。

## 3. 当前依赖情况

| 检查项 | 结论 | 依据 |
|---|---|---|
| `go.mod` 是否声明 `golang.org/x/sync` | **否** | `go.mod` 直接依赖只有 `pkg/sftp`、`spf13/cobra`、`x/crypto`、`x/term`、`yaml.v3`、`modernc.org/sqlite` |
| 代码是否导入 `golang.org/x/sync/*` | **否** | 全仓库检索 `errgroup` 与 `golang.org/x/sync` 只命中 `go.sum`（第 65–73 行） |
| `go.sum` 中的记录从哪来 | 传递依赖 | `golang.org/x/sync v0.17.0` 带 `h1:` 校验和（第 72 行）；`go mod graph` 显示 `golang.org/x/exp@v0.0.0-20251023183803-a4bb9ffd2546 → golang.org/x/sync@v0.17.0`、`modernc.org/libc@v1.67.6 → golang.org/x/sync@v0.17.0`，其中 `x/exp` 是 `go.mod` 里声明的间接依赖 |
| 构建图中的实际选中版本 | `golang.org/x/sync v0.17.0` | `GOPROXY=off go list -m all` 输出包含该行（离线读取模块图，未联网） |
| 若引入，是否需要新增下载 | 不需要新增模块下载，但仍需新增直接依赖 | 模块版本已在构建图与 `go.sum` 中；`errgroup.go` 源码已在本机模块缓存中可直接阅读。正式引入仍需在 `go.mod` 增加 `require` 行（`go mod tidy` 会补），属于"新增一个直接依赖" |
| 引入的版本依据 | 目前唯一在图中且通过校验的版本是 `v0.17.0` | 与 `go.mod` 的 `go 1.24.5` 兼容；`errgroup` API 自 2016 年起稳定，升级风险低——但**风险低不等于有必要** |

一句话：技术上是"零下载成本的引入"，政策上是"新增直接依赖"，而收益评估（第 6、7 节）为负。

## 4. 并发需求的语义分层

审计必须先拆开六类互不相同的需求，否则会把"能并行"误读成"该用 errgroup"。

| # | 需求 | 本仓库是否存在 | 仓库中的实际实现 | `errgroup` 是否匹配 |
|---|---|---|---|---|
| 1 | 短期并行，任一失败取消其余 | **不存在** | 无并行批次；`HostValidator.runChecks` 是**串行且有依赖**（SSH → Docker 版本 → CLI 帮助，前项失败后项记 `not_run`） | 匹配，但无对象 |
| 2 | 短期并行，等待全部并收集多个错误 | **不存在**（现有聚合是**顺序**执行 + `errors.Join`） | `lifecycle.Manager.stopServices`、`Server.shutdown`、`planningLease.Settle` | 匹配度低：`Wait` 只返回首个错误，收集多错仍需自己写 |
| 3 | 多个独立后台业务任务，各自失败互不影响 | **存在** | `TaskScheduler.Scan` → `go s.run(claim)`；`EnvironmentBuildManager.Submit` → `go m.execute(...)`；`HostValidationService` worker + `inflight` 去重 | **不匹配**：`WithContext` 会把单点失败变成"取消兄弟任务"，直接违反红线 |
| 4 | 长期后台循环，需要明确停止 / 取消 / 等待 | **存在（主要形态）** | `TaskScheduler.loop`、`HostValidationService.worker`、`httpserver.Server.Serve`、4 处租约心跳、`sshclient` keepAlive、`docker` transport watcher、`lifecycle.Manager.watch` | **不匹配**：`Group` 不可复用，`Wait` 后不能再 `Go` |
| 5 | 服务生命周期启动与逆序关闭 | **存在** | `Server.startup` / `Server.shutdown` / `lifecycle.Manager.Start` / `stopServices` | **不匹配**：这是顺序 + 逆序，不是并行；并行化会破坏依赖顺序 |
| 6 | 持久化状态、租约、epoch fencing、不确定结果收敛 | **存在（核心执行链路）** | `TaskWorkflowService`、`TaskLaunchService`、`StepWorkflowService`、`StepExecutor`、`ValidationService`、`PlanningAgentService`、`planningLease`、`TaskScheduler` 租约 | **不匹配**：错误不是"返回值"，而是状态机推进 + 落库；取消原因有业务含义 |

**结论**：仓库的并发压力集中在第 3、4、6 类，恰好是 `errgroup` 最不擅长或明确不适用的一类。

## 5. 仓库内并发实现清单

按实现机制归类（行号对应审计基线）。同一机制在多个服务中重复出现，说明它们共享同一套设计意图。

### 5.1 服务生命周期骨架（6 份同构实现）

`mu + started/stopped + stopSignal(context.WithCancel(WithoutCancel(parent))) + stopOnce + runs(sync.WaitGroup) +
active(int)`，配合 `beginXxx` 准入函数与"停止期间拒绝新请求"的三段式停止：

| 服务 | Stop | 准入函数 | 取消原因 |
|---|---|---|---|
| `TaskWorkflowService` | `task_workflow.go:409` | `beginRun` `task_workflow.go:447` | `ErrWorkflowStopped` |
| `StepWorkflowService` | `step_workflow.go:326` | `beginRun` `step_workflow.go:364` | `ErrStepExecutorStopped` |
| `StepExecutor` | `step_executor_lifecycle.go:67` | `beginRun` `step_executor_lifecycle.go:105` | `ErrStepExecutorStopped` |
| `TaskLaunchService` | `task_launch.go:319` | `beginLaunch` `task_launch.go:362` | `ErrTaskLaunchStopped` |
| `ValidationService` | `validation_lifecycle.go:66` | `beginRun` `validation_lifecycle.go:104` | `ErrValidationEngineStopped` |
| `PlanningAgentService` | `planning_agent_lifecycle.go:64` | `beginCall` `planning_agent_lifecycle.go:115` | `ErrPlanningAgentStopped` |

每个准入函数的固定结构：同一把锁内完成"已停止判定 → `active++` → `runs.Add(1)` → `context.WithCancelCause(ctx)`
→ 起一个 `select { <-stop.Done(); <-runCtx.Done() }` 的取消桥协程。停止时先在同一把锁内置 `stopped`，
再 `stopOnce()`，最后 `go func(){ runs.Wait(); close(done) }()` + `select` 等 `done` / `ctx.Done()` / 超时。

### 5.2 后台循环服务

- `TaskScheduler`（`internal/app/task_scheduler.go`）：`Start:191` 起 `loop`（`268`），`runScan`（`284`）用
  `scanning` WaitGroup 计数，`Scan`（`304`）用 `scanMu` 串行化"统计名额 → 领取 → 登记"，`dispatch`（`516`）
  成功后 `runs.Add(1)` + `go s.run(claim)`（`579-580`）。
- `HostValidationService`（`internal/app/host_validation_service.go`）：`Start` 起 N 个 `go s.worker(serviceCtx)`
  （`131`），`queue chan validationKey` + `inflight` map 去重，`Stop:140` 取消并等待 worker 退出。
- `httpserver.Server.Serve`（`internal/httpserver/server.go`）：`go func(){ serveErr <- s.http.Serve(listener) }()`
  （`209`），`ctx.Done()` 后 `Shutdown` + 再来一次 `<-serveErr`。
- 租约心跳 4 处：`TaskScheduler.startLeaseHeartbeat`（`task_scheduler.go:748`）、
  `TaskWorkflowService.startLeaseHeartbeat`（`task_workflow_exec.go:234`）、
  `TaskLaunchService.startLeaseHeartbeat`（`task_launch_run.go:563` 附近）、
  `planningLease.renewLoop`（`planning_lease.go:253`）。统一形态是
  `stop chan struct{} + done chan struct{} + ticker`，返回的停止函数会 `close(stop)` 并 `<-done` 等待退出。
- `sshclient.Session` keepAlive（`session.go:43`）、`docker.engineTransport` 响应 watcher（`engine_transport.go:47`）、
  `lifecycle.Manager.watch`（`manager.go:227`）故障转发。

### 5.3 取消桥接（ctx → 不支持 ctx 的阻塞调用）

统一形态：起一个只做 `select { case <-ctx.Done(): close(conn) }` 的观察协程，调用结束再通知它退出。

- `sshclient.Dialer`（`dialer.go:365`、`372`）：`ssh.NewClientConn` 不接受 ctx，用关闭连接响应取消。
- `sshclient.dialUnixSocket`（`unixsocket.go:53`、`70`）：`c.sshClient.Dial` 不可中断，超时后另起协程回收连接。
- `sshprobe.Prober.readIdentifier`（`prober.go:157`）：取消时 `conn.Close()` 打断阻塞读。
- `docker.engineTransport.exchange`（`engine_transport.go:47`）：SSH 通道不支持 `SetDeadline`，只能靠关闭通道生效。

### 5.4 单请求内的流水线协作

- `process.Supervisor`（`supervisor.go:130-132`）：`pumpStdout` / `pumpStderr` / `reap` 三个协程，`reap` 先等
  两个 pump 结束再 `cmd.Wait()`，`exit` 通过 `done` 通道发布。
- `pi.runOnce`（`agent/pi/command.go:117`）：`collectStdout` 协程 + `select { <-collected; <-runCtx.Done() }`。
- `sshclient.Session.Run`（`session.go:182-241`）：`session.Run` 协程 + `writeStdin` 协程，`select` 二选一，
  取消时 `terminateSession` 尽力终止并在宽限期内等待。

### 5.5 装配与错误聚合（顺序执行，不是并行）

- `Server.Run`（`server.go:344`）→ `startup`（`369`）→ `startServices`（`609`）/ `startHTTP`（`1014`），
  `wait`（`1081`）在 `ctx.Done()` / `runCtx.Done()` / `serveErr` / `manager.Faults()` 四路事件上等待，
  每条返回路径都 `errors.Join(..., s.Stop(context.WithoutCancel(ctx)))`；`Stop`（`1110`）由 `stopOnce` 保证幂等，
  `shutdown`（`1118`）按"HTTP → 后台服务 → 数据库"逆序清理。
- `lifecycle.Manager.Start`（`manager.go:150`）按注册顺序启动，失败时 `errors.Join(startErr, m.shutdown(ctx))`；
  `stopServices`（`208`）逆序停止并在同一 `stopCtx`（`context.WithTimeout(WithoutCancel(ctx), m.stopTimeout)`）
  预算内 `errors.Join` 汇总失败。

### 5.6 其他协调原语（与 errgroup 无关，但构成"现有生命周期管理"的一部分）

- 按标识互斥：`keyedTaskMutex`（`task_workflow.go` / `task_workflow_exec.go:124` `lockWithin`）、
  `environments.go:42` 的 `map[string]*sync.Mutex`。
- 取消注册表：`cancelRegistry`（`task_workflow_exec.go:34-72`），按 taskID 登记 `context.CancelCauseFunc`，
  用 `sameCancel`（`%p` 比较）避免误删后来者的登记。
- `context.WithoutCancel` 在 `internal/app` 内出现数十次，全部服务于同一目的：**取消之后仍要把真实结论落库**。
- `context.Cause` 在 8 处被读取以区分停止 / 用户取消 / 失去执行权：`step_executor_lifecycle.go:148`、
  `step_executor_run.go:813`、`step_workflow_repair.go:464`、`task_workflow_exec.go:527`、`583`、`911`、`914`、
  `validation_run.go:438`。

## 6. 候选迁移点逐项分析

每项按"当前实现 → 并发模型 → ctx 来源与取消传播 → 错误处理 → 停止等待 → 持久化/副作用 →
errgroup 收益 → 行为变化 → 结论"展开。

### 6.1 `TaskScheduler`（`internal/app/task_scheduler.go`）

- **当前实现**：`Scan`（`304`）在 `scanMu` 内 `countOccupiedSlots`（`466`）统计真实占用，按 `limit` 计算空名额，
  逐条候选做领域准入，`dispatch`（`516`）领取租约成功后登记 `dispatched`、`runs.Add(1)` 并 `go s.run(claim)`。
- **并发模型**：**任务之间完全独立**，扫描本身串行（防"同一份空名额快照被两次扫描各自领取"，注释已记录实测
  上限 2 却领到 3 的事故）。`Stop`（`216`）取消扫描与每个在途分派的 `cancel`，然后用一个协程同时
  `runs.Wait()` + `scanning.Wait()`，`select` 等 `done` / `ctx.Done()` / `timeout`。
- **ctx 来源与取消传播**：`Start` 里 `stopSignal, stop := context.WithCancel(context.WithoutCancel(ctx))`，
  `run`（`630`）再派生 `runCtx` 并登记到 `dispatched[taskID].cancel`，使 `Stop` 能逐个取消在途分派。
- **错误处理**：`dispatch` 的 `claimErr` 只做 `continue` + 日志（`ErrTaskLeaseConflict` 静默）；`run` 里
  `launchErr` 交给 `settleFailedDispatch`（`687`），按"服务停止 / 真实失败"分流：前者**保留租约等自然过期**
  并写 `EventTypeTaskRecoveryRequired`，后者写 `EventTypeTaskFailed` 后按 owner+epoch 释放租约。
- **停止等待**：见上，`runs` 与 `scanning` 两个 WaitGroup + 超时；超时时返回
  `"调度器停止超时: 仍有 %d 个任务在分派中"`。
- **持久化 / 副作用**：任务租约（owner + epoch 条件更新）、事件追加、容器与 Pi 启动；`isServiceStop`（`739`）
  把"停止"与"用户取消"区分开，避免停止触发自动重试。
- **errgroup 收益**：**零**。`Scan` 是串行决策循环，不是并行批次；即便把候选改成并行领取，`errgroup.WithContext`
  会让"某一个任务领取失败"取消其他任务的启动——这正是任务说明中点明的红线。
- **行为变化风险**：`SetLimit` 是进程内信号量，而当前名额来自数据库真实占用 + 租约，跨进程生效。用它替代会让
  多实例部署时同时运行的任务数超出配置上限（正是 `scanMu` 注释记录过的那类 bug 的放大版）。
- **结论**：**不迁移**。这是"独立业务执行 + 持久化所有权"的典型场景，`errgroup` 的语义（首个错误取消全组、
  一次性组）与需求相反。

### 6.2 `EnvironmentBuildManager`（`internal/app/environments_build_manager.go`）

- **当前实现**：`Submit`（`186`）在 `m.mu` 内做"启动状态检查 → 同环境去重（`ErrEnvironmentBuildInProgress`）→
  `m.running[key]` 登记 → `m.wg.Add(1)`"，然后 `go m.execute(...)`（`262`）；`execute`（`299`）用 `defer` 完成
  `wg.Done`、`close(run.done)`、从 `running` 删除。
- **并发模型**：不同环境并行，同环境串行（拒绝而非排队）；`Wait`（`284`）只为测试与诊断提供单次等待。
- **ctx 来源**：`Start`（`138`）里 `context.WithCancel(ctx)`，`Submit` 取 `m.ctx` 作为构建 ctx；`Stop`（`156`）
  `cancel()` 后用协程 `m.wg.Wait()` + `select` 等 `finished` / `ctx.Done()`。
- **错误处理**：`execute` 内部**只记录**日志，不返回值；真实结论由 `EnvironmentService.Build`
  （`environments_build.go:145-190`）落库：被中断时 `MarkUncertain`（不能记 failed，镜像可能已生成），
  记录写回强制用 `context.WithoutCancel`。
- **停止等待**：`wg.Wait` + 停止超时由上层 `lifecycle.Manager` 表达；`Start` 先 `convergeUnmanagedBuilds`
  （`337`）把遗留 `building` 收敛为 `uncertain`。
- **持久化 / 副作用**：远程 Docker 构建、构建日志与镜像清单写入；"构建是否仍在进行"由数据库证据决定，
  不能由 goroutine 存在与否推断。
- **errgroup 收益**：**零**。`WithContext` 在这里是最危险的能力：一个环境构建失败会取消其他环境的构建，
  而产品规则要求"不同环境的构建彼此独立"。
- **结论**：**不迁移**。

### 6.3 `PlanningAgentService`（`internal/app/planning_agent_lifecycle.go`）

- **当前实现**：`Start`（`36`）不启动长期协程，只建立 `stopSignal`；`beginCall`（`115`）在同一把锁内完成
  "已停止/未启动拒绝 → `active++` → `runs.Add(1)` → `context.WithCancelCause(ctx)`"并起取消桥协程；
  `Stop`（`64`）置 `stopped`、取消、协程 `runs.Wait()`，`select` 三个分支（`done` / `ctx.Done()` /
  独立 timer，且当调用方 deadline 更早时不启 timer，避免"取消与超时随机判定"）。
- **错误处理**：错误不汇聚，而是由 Runner 完成 Session 提取与归档后写库；`Stop` 注释明确"等待覆盖到
  `RunRole` 完整返回，而不只是 Pi 主进程退出"。
- **持久化 / 副作用**：规划会话租约（`planningLease`，见 6.8）、容器内 Pi 执行、Session 归档。
- **errgroup 收益**：**零**，且会丢失"准入拒绝"能力：`errgroup.Group` 没有"当前已停止，拒绝新任务"的概念，
  `Go` 在 `Wait` 开始后即属误用，无法表达"长期可复用服务 + 停止时 drain"。
- **结论**：**不迁移**。

### 6.4 `TaskWorkflowService`（`internal/app/task_workflow.go`、`task_workflow_exec.go`）

- **当前实现**：`drive`（`task_workflow_exec.go:164`）在 `beginRun` 之后 `context.WithCancelCause(ctx)`、
  向 `cancelRegistry` 登记取消函数（`register:40`），起 `startLeaseHeartbeat`（`234`），调用 `advance`（`341`），
  结束后用 `context.WithoutCancel(ctx)` 释放任务租约（`releaseTaskLease`），并按 `context.Cause` 判定
  `errWorkflowCancelRequested`（`527`/`583`/`911`）与 `errWorkflowLeaseLost`（`914`）的分支。
- **并发模型**：同一任务串行（`keyedTaskMutex`），不同任务并行；每条在途执行有独立的续租协程与独立取消原因。
- **错误处理**：`errors.Join` 只出现在租约释放等收尾处；业务失败进入状态机（`blocked` /
  `recovery_required` / `failed`）并落库，**不以返回值形式向调用方汇总**。
- **持久化 / 副作用**：执行租约、epoch fencing、步骤与尝试记录、事件追加、容器与 Git 副作用；
  迟到结果不得覆盖已确认终态由领域层条件更新保证。
- **errgroup 收益**：**零**。`WithContext` 会把"第一个子任务错误"作为 `context.Cause`，与现有的三档业务原因
  冲突；`Wait` 只返回一个错误，无法表达"每条工作流各自的结论"。
- **行为变化风险**：若用 `errgroup` 包住"续租协程 + 执行协程"，`Wait` 返回时的无条件 `cancel(nil)` 会与
  `finish()` 里的显式 `cancelRun(nil)` 叠加，`context.Cause` 从"业务原因"变成"nil 或首个错误"，直接影响
  `stopForInterruption` 等收敛分支。
- **结论**：**不迁移**。

### 6.5 `lifecycle.Manager` 与 `Server` 装配（`internal/lifecycle/manager.go`、`internal/server/server.go`）

- **当前实现**：`Manager.Start`（`150`）串行启动，失败即 `errors.Join(startErr, m.shutdown(ctx))`；
  `stopServices`（`208`）**逆序**逐个 `Stop`，共享一个 `stopCtx`（`WithTimeout(WithoutCancel(ctx), 10s)`），
  失败用 `errors.Join` 汇总。`Server.Run`（`344`）串行 `startup`（`369`）→ `openDatabase` / `startServices` /
  `startHTTP`，`wait`（`1081`）四路 `select`，`shutdown`（`1118`）按 HTTP → 后台服务 → 数据库逆序。
- **并发模型**：**故意串行**：顺序即依赖顺序（角色导入先于业务服务、数据库最后关闭、`manager.Stop` 先于
  `store.Close`）。唯一的协程是 `serveErr` 的投递与 `Manager.watch` 的故障转发。
- **errgroup 收益**：**零（负）**。把启动或停止并行化会破坏依赖顺序；`errgroup` 也无法表达"逆序"。
  此处已经是标准库 `errors.Join` + 顺序执行的最简形态。
- **结论**：**不迁移**（且不得为了并发化而改动顺序）。

### 6.6 `StepExecutor` / `StepWorkflowService` / `ValidationService`

- **当前实现**：与 5.1 表格中的六份骨架完全同构，均为"请求驱动、无长期协程、admission + runs + stopSignal"。
- **errgroup 收益**：**零**。这些服务的 `runs` 是**计数器**，不是"本次调用的子任务组"：一组生命周期内会经历
  成百上千次 `Add`/`Done`，而 `errgroup.Group` 明确不可复用。
- **附带观察**：这 6 份骨架的重复是真实的可维护性负担，但**消除它的手段是抽公共生命周期辅助类型，与
  `errgroup` 无关**。本轮只做审计，不提出实施。
- **结论**：**不迁移**。

### 6.7 `HostValidationService`（`internal/app/host_validation_service.go`）

- **当前实现**：固定 worker 数（`Start:131` 循环 `go s.worker`），`queue` 带缓冲，`inflight` 去重，
  `SubmitHost` 非阻塞入队（满则 `ErrValidationQueueFull`），`run`（`242`）在 `defer` 中清理 `inflight`。
- **并发模型**：进程内工作池 + 队列；这是"生产/消费"，不是"同一任务的并行子任务"。
- **错误处理**：`execute` 对校验失败调用 `FailWithoutChecks` 把状态收敛为 `abnormal`（写库），
  `Faults()` 通道当前不写任何值。
- **errgroup 对照**：`TryGo` + `SetLimit` 能表达"限额提交、满了返回 false"，看起来最接近本场景的一处。
  但仍有两点不匹配：(1) 现有去重键是 `(hostID, generation)` 且需要 `InFlight` 供外部查询，`Group` 无此状态；
  (2) `Stop` 需要"取消 → worker 全部退出"的等待，`errgroup.Wait` 无 ctx、且不能与后续提交共存。
- **结论**：**不迁移**。若未来需要"限额提交 + 汇总首个错误"的队列，可重新评估（见第 10 节）。

### 6.8 租约心跳（4 处 `startLeaseHeartbeat` / `renewLoop`）

- **当前实现**：`stop chan` + `done chan` + `ticker`，返回的停止函数 `close(stop)` 后 `<-done`；
  `TaskWorkflowService` 版本在续租失败时 `cancel(errWorkflowLeaseLost)` 并返回（`task_workflow_exec.go:246-280`）。
- **errgroup 收益**：**零**。这是"长期循环 + 由调用方在业务结束时停止"，`errgroup` 的 `Go`/`Wait` 无法表达
  "先跑业务、业务结束再停后台循环"，且 `Wait` 会阻塞直到心跳退出——顺序必须由业务侧控制。
- **结论**：**不迁移**。

### 6.9 单请求内流水线与取消桥接（5.3 / 5.4）

- **当前实现**：`select` 二选一或三选一，`done`/`watch`/`closed` 通道成对关闭，取消通过关闭连接实现。
- **errgroup 收益**：**接近零**。这些协程多数**不返回错误**（`pumpStdout`/`pumpStderr`、`writeStdin`、
  `collectStdout` 通过通道传值），或错误需要按"起止状态"分类（`CommandResult.Status`、
  `StdinStatusUnknown`、`TerminationConfirmed=false`），`Group.Wait` 的单一 error 承载不了。
- **特别说明**：`process.Supervisor` 的 `reap` 依赖"先等两个 pump 结束再 `cmd.Wait()`"，是**有顺序的等待**；
  `errgroup.Wait` 只保证"全部结束"，不区分谁先结束，无法替代 `<-stdoutDone; <-stderrDone`。
- **结论**：**不迁移**。

### 6.10 `HostValidator.runChecks`（`internal/app/host_validation.go`）

这是全仓库**形式上最像 errgroup 用例**的一处（"三项检查"），但语义不允许：

- 顺序与依赖是刻意的：`runChecks` 注释写明"SSH 连接 → Docker 版本与 API 兼容性 → Docker CLI 帮助信息，
  前一项失败时后续检查记为 `not_run`"，理由是"SSH 不可用时无法执行任何远程检查，版本不兼容时命令帮助检查
  也没有意义"。
- 结果 `domain.ValidationDetails` 需要逐项记录状态（通过 / 失败 / 未执行），不是"首个错误"。
- 外层 `Validate`（`host_validation.go:94`）已在 `totalCtx`（`WithTimeout`）内执行，错误路径用
  `context.WithoutCancel` 写回，避免取消导致状态永久停在 `checking`。

**结论**：**不迁移**。这是一处"看起来能并行、实际必须串行"的典型，也是本轮审计要防止的误判。

### 6.11 `Server` 的 `serveErr` 与 `Manager.Faults`（`server.go:1014-1143`）

- **当前实现**：`startHTTP` 起一个协程把 `web.Serve` 结果送进 `serveErr`（容量 1），`wait` 在四路事件上 `select`，
  `waitServe` 用 `time.After(s.shutdownTimeout)` 兜底。
- **errgroup 收益**：**零**。`wait` 需要"任一事件到达即返回并保持其它组件状态可查询"（`s.serveErr = nil`、
  `s.web = nil` 等清理），`Group.Wait` 是"等全部完成"，语义相反。
- **结论**：**不迁移**。

## 7. `errgroup` 与标准库的语义对比

| 维度 | `errgroup.Group` | `sync.WaitGroup`（现状用法） | `context` 家族（现状用法） | `errors.Join`（现状用法） |
|---|---|---|---|---|
| 错误可见性 | 只保留**第一个**错误（`errOnce`），其余丢弃 | 不处理错误 | 不处理错误 | 保留**全部**错误，`errors.Is/As` 可穿透 |
| 错误是否需要"返回值" | 是（`Wait` 返回） | 否 | 否 | 是（作为聚合结果） |
| 取消触发 | 首个错误自动取消 + `Wait` 返回时取消 | 无 | 显式 `cancel(cause)` / deadline | 无 |
| 取消原因 | 唯一（首个错误） | 无 | `WithCancelCause` 可携带业务原因，`context.Cause` 可判定 | 无 |
| 可复用 | **不可以**（`Group` 一次性） | 可以（计数归零后可复用） | 每个派生 ctx 一次性 | 无状态 |
| 等待是否响应取消 | **不响应**（`Wait` 无 ctx） | 不响应 | — | — |
| 并发上限 | `SetLimit` / `TryGo`，仅进程内 | 自己实现 | 无 | 无 |
| panic 处理 | 不 recover（直接崩） | 不 recover | 无 | 无 |
| 依赖 | 需 `golang.org/x/sync` | 标准库 | 标准库 | 标准库（Go 1.20+） |

对照本仓库的 6 类需求：

- 需求 3（独立任务）与需求 6（持久化执行）需要的是**错误隔离 + 逐条落库**，`errors.Join` 或"根本不聚合"
  才是正确形态；`errgroup` 的"首个错误 + 自动取消"是反模式。
- 需求 4（长期循环）需要"可停止 + 可等待 + 可重复提交"，`WaitGroup` + `chan struct{}` 才能表达。
- 需求 5（生命周期）需要顺序与逆序，`errors.Join` 已足够。
- 需求 1、2 是本仓库唯一没有的场景；即使出现，`WaitGroup` + `errors.Join`（多错误）或
  `WithCancelCause` + 首个错误（协同取消）也能在 20 行内实现，且不引入依赖。

**净结论**：在本仓库现有形态下，`errgroup` 提供的能力要么重复标准库，要么与业务语义冲突，没有第三种情况。

## 8. 引入后可能造成的行为变化与风险

即使"只在某处局部引入"，也存在可识别的行为漂移：

1. **兄弟任务被误取消**：只要用了 `WithContext`，一个子任务的错误会取消同组其它任务。在 `TaskScheduler`
   分派、`EnvironmentBuildManager` 构建、`HostValidationService` 校验这三处都是**明确禁止**的行为。
2. **错误信息丢失**：`errOnce` 只保留第一个错误。当前 `settleFailedDispatch` / `execute` /
   `FailWithoutChecks` 是**每个失败都有自己的库内记录**；改成 `Wait` 返回值后，第二个及以后的错误不会被
   `Wait` 返回，若同时删掉原有逐条日志，就会出现"失败无记录"的审计缺口。
3. **取消原因被污染**：8 处 `context.Cause` 判定依赖 `ErrWorkflowStopped` / `errWorkflowCancelRequested` /
   `errWorkflowLeaseLost` 等语义值。`errgroup` 把首个错误作为 cause，会让"失去执行权"与"用户取消"混为一谈，
   直接影响 `stopForInterruption`、`isServiceStop` 的分流（后者决定是否写 `recovery_required`、是否释放租约）。
4. **停止语义被破坏**：`Wait` 不响应 ctx。把现有 `select { done / ctx.Done() / timeout }` 换成 `g.Wait()`
   会让 `Manager.stopTimeout`（10s）与各服务的 30s 上限失效，进程关闭可能无限挂起。
5. **准入能力缺失**：`Group` 无"已停止"状态，`Wait` 之后不得再 `Go`。当前六个服务都要求"停止后新请求返回
   `Err*Stopped`"，并把 `runs.Add` 与 `stopped = true` 放在同一临界区以避免竞态；`errgroup` 无法复现该保证。
6. **限额语义被削弱**：`SetLimit` 修改在有活动协程时 panic，且只在本进程生效；`SetLimit(0)` 配合 `Go` 会死锁。
   用它替代数据库名额统计会让多实例部署的并发上限失效。
7. **panic 不会被兜底**：`errgroup` 明确不 recover（源码注释）。不要误以为引入它能提升崩溃隔离性。
8. **依赖与版本面**：`golang.org/x/sync` 会成为直接依赖；升级 `x/exp` / `modernc.org/libc` 时可能连带升级
   `x/sync`。`errgroup` API 稳定，实际风险低，但这是"没有收益的依赖增加"，与
   [AGENTS.md](../../AGENTS.md) 中"优先标准库、新增依赖必须有明确功能收益"的约定相悖。

## 9. 最终建议

**不引入 `golang.org/x/sync/errgroup`，也不做任何局部使用。**

理由按权重排序：

1. **没有目标场景**：全仓库没有"一组短期并行子任务，需要把错误汇总到本次调用返回值"的代码
   （第 5、6 节逐项核过，包括形式上最像的 `HostValidator.runChecks`）。
2. **现存并发形态与 `errgroup` 的三条硬约束正面冲突**：`Group` 一次性、`Wait` 之后不得 `Go`、
   `Wait` 不可取消。而本仓库的核心并发是"长期可复用服务 + 三段式停止 + 落库收敛"。
3. **两条会伤害业务正确性的能力**：`WithContext` 的兄弟取消违反"独立任务互不影响"红线；
   `Wait` 的单错误语义会与"任务结论只能来自持久化状态"的红线冲突。
4. **唯一可能受益的 `SetLimit` 与项目并发上限语义不同**（跨进程租约 vs 进程内信号量）。
5. 标准库组合已经覆盖全部需求：`sync.WaitGroup`（在途计数与停止等待）、`context.WithCancel` /
   `WithCancelCause` / `WithoutCancel`（取消传播与落库保护）、`errors.Join`（顺序聚合）、
   `sync.Mutex` + 条件更新（名额与租约）。

同时明确三条**不因此改变**的边界（防止后续被误改）：

- 不因为"上 errgroup 更容易并发"而把 `lifecycle.Manager` 的启动 / 停止并行化；
- 不因为"统一并发原语"而把 `TaskScheduler` 的分派改成 `WithContext` 组；
- 不为了消除六份同构生命周期骨架而引入第三方并发库（该重复应由内部公共类型解决，属独立议题）。

## 10. 未来重新评估的触发条件

出现以下任一情形时，本文结论应被重新评估（每条都可直接对应到本仓库的既有结构）：

1. **出现同一请求内的同质并行批次，且要求"任一失败即放弃其余"**。例如：
   - 一次校验请求并行探测多台候选执行节点（当前 `HostValidator` 只针对单节点，且检查项有依赖）；
   - 一次任务启动并行拉取多个代码仓库 / 并行准备多个环境镜像；
   - 一次请求并行生成多份计划候选并取第一个成功者。
   判断标准：这些子任务**没有各自的持久化结论**，失败只需整体重试或整体失败。
2. **需要进程内并发限额，且限额与数据库租约无关**。例如 HTTP 批量接口的本地并发保护（当前所有限额都
   来自租约与真实占用）。
3. **出现"多错误聚合"的短期批次**，此时先评估 `sync.WaitGroup` + `errors.Join`（标准库、零依赖）；
   只有当"首个错误取消其余 + 限额 + 提交"三件事同时需要时，`errgroup` 才具备相对优势。
4. **标准库出现等价能力**（例如把并发组纳入 `sync` 或 `context`），此时优先采用标准库，不引入扩展库。
5. 若某决策必须引入 `errgroup`，需同时给出：`WithContext` 不用于独立业务任务的证明、
   `Wait` 单错误与落库结论不冲突的说明、以及限额语义不跨进程的适用边界。

## 11. 审计边界说明

- 本文只读代码与模块缓存源码，**没有运行任何** `go test`、构建、`vet`、`gofmt` 或其他验证命令；
- 除 `go.mod` / `go.sum` / 依赖图元数据（`GOPROXY=off go list -m all`、`go mod graph`，只读）外，
  没有执行影响仓库状态的命令；
- 没有创建测试或临时脚本，没有修改生产代码、测试、依赖文件与其它文档；
- 本报告只提供结论，不包含任何实施动作；若后续决定引入组件，应另立任务并重新走评估流程。
