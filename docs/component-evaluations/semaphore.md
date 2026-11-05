# 组件评估：`golang.org/x/sync/semaphore`

> **状态：不引入（维持仅使用标准库与既有机制）。** 本轮为纯审计：未修改生产代码、测试、`go.mod` /
> `go.sum`，未引入依赖，未改动配置与 Schema，未运行 `go test`、构建、`vet`、`gofmt` 或其他验证命令，
> 也未提交、推送或重写 Git 历史。审计的唯一落地动作是新增本文件。

- 审计时间：2026-11-05
- 审计基线：`802eb722112a3edf3bd607e610e6685f76e5e98e`（`:memo: errgroup 组件替换报告`）
  - 审计前执行 `git fetch origin master`，`origin/master` 与本地 `HEAD` 均为该 SHA，基线未发生漂移
  - 审计开始时 `git status --short -uno` 无输出（工作区干净）
- 审计对象：仓库全部 Go 代码（`internal/`、`cmd/`、`tests/`），重点是并发准入、资源互斥、连接池与
  后台任务容量；不评估前端与文档
- Go 版本：`go.mod` 声明 `go 1.24.5`，本机工具链 `go1.24.5 windows/amd64`
- 审计方式：
  1. 全仓库检索 `semaphore`、`Weighted`、`Acquire`、`TryAcquire`、`Release`、`MaxConcurrent`、
     `MaxWorkers`、`QueueSize`、`SetMaxOpenConns`、`inflight`、`slots`、`limit`、`capacity`、
     `worker`、`keyedMutex`、`sync.Mutex`、有界 channel 等模式；
  2. 逐文件阅读命中点的完整调用链（准入 → 排队 → 执行 → 收尾 → 落库），不以关键词计数下结论；
  3. 阅读本机模块缓存中的 `golang.org/x/sync@v0.17.0/semaphore/semaphore.go`（160 行）与
     `@v0.20.0`、`@v0.22.0` 同名文件做版本比对；本文所有组件语义结论都对应到源码的具体分支，
     不是复述文档；
  4. 用 `go mod graph`、`go mod why -m`、`go list -m all` 离线核对依赖来源与选中版本，未联网、
     未改动依赖文件。
- 相关文档：[architecture.md](../architecture.md)（执行链路边界与状态分层）、
  [workspace.md](../workspace.md)（包职责）、[docker-compatibility.md](../docker-compatibility.md)
  （Docker 版本基线与命令入口）、[errgroup.md](errgroup.md)（同一模块的另一组件，结论可交叉印证）

## 1. 结论摘要

1. **仓库当前没有任何信号量实现**：全仓库 `golang.org/x/sync/*` 导入为 0 处，`semaphore`、`Weighted`、
   `TryAcquire` 检索结果为空，也没有手写的 `Acquire/Release` 令牌计数。
2. **`go.sum` 里的 `golang.org/x/sync v0.17.0` 与本仓库代码无关**：`go.mod` 未声明该模块（连 `// indirect`
   都没有），`go mod why -m` 给出的链路是
   `internal/storage/sqlite → modernc.org/sqlite → modernc.org/libc → modernc.org/libc.test →
   golang.org/x/tools/go/packages → golang.org/x/sync/errgroup`，
   即 **测试依赖链**，不在本仓库的构建导入图中。
3. 仓库的资源控制手段集中在五类，**没有一类是"按权重分配容量"**：
   - 任务并发上限：`TaskScheduler` 的**数据库事实 + 进程内在途分派**计数（进程级上限，不是部署级配额）；
   - 同资源串行：`keyedMutex`（按节点、按节点+环境、按容器、按规划会话、按任务）；
   - 固定 worker + 有界队列：仅 `HostValidationService` 一处，且入队满时**立即拒绝**而不是阻塞；
   - 连接池：只有 `database/sql` 自身的 `SetMaxOpenConns(4)` / `SetMaxIdleConns(4)`；
   - 跨进程执行权：数据库条件更新 + 租约 + epoch fencing（任务租约、步骤占用、规划会话租约）。
4. **四个重点候选位置都不需要加权语义**：
   - `TaskScheduler`：容量来自数据库占用快照，跨进程已由租约防护；`semaphore.Weighted` 是进程内计数器，
     替换会让"名额 = 数据库里真实占用的任务"退化为"名额 = 本进程内存里减到几"，**削弱**约束。
   - `HostValidationService`：所有校验任务成本相同（一次 SSH 连接 + 两条 Docker 命令），已有
     worker + 有界队列 + 非阻塞拒绝，换成加权信号量是把 worker pool 换一种写法，没有新增能力，
     还会丢掉"队列满即拒绝"的语义（`Acquire` 只能阻塞或由调用方用 `TryAcquire` 轮询）。
   - `EnvironmentBuildManager`：按 (节点, 环境) 去重、无队列、无全局上限——**这是仓库里唯一真实存在
     容量控制缺口的地方**（第 8 节），但缺的是"每节点构建并发数"这种**同权计数**，不是权重模型；
     `semaphore.Weighted` 不是它的正确形态，且进程内计数器无法覆盖多实例。
   - 容器创建 / 规划会话互斥：是**单一资源互斥**（同一节点、同一会话），由 `keyedMutex` + 数据库租约
     承担；互斥不是容量，换成权重信号量（`NewWeighted(1)`）只会得到一把更慢、更难取消、更难观测的锁。
5. **`semaphore.Weighted` 的若干行为与仓库现有约定直接冲突**：`Acquire(n > size)` 不报错而是**阻塞到
   ctx 结束**（`context.Background()` 下永久挂起）；没有容量查询、没有 `SetLimit`、没有持有者归属；
   v0.17.0 对负权重**不做校验**（v0.22.0 才加入 `panic`）。仓库要求"错误必须保留上下文、不使用 `panic`
   处理普通错误"，这条冲突是决定性的。
6. **最终建议：不引入。** `golang.org/x/sync` 不作为直接依赖写入 `go.mod`；不新增信号量抽象。构建容量
   缺口按第 8 节的建议形态单独评估（每节点同权上限 + 跨进程租约），而不是引入加权信号量。
7. 未来重新评估的触发条件见第 11 节（典型信号：出现**同一资源上成本相差一个数量级**的真实任务，
   且权重可由产品配置或实测数据确定）。

## 2. 审计状态与 Git 基线

| 检查项 | 结论 | 依据 |
|---|---|---|
| 最新 `master` SHA | `802eb722112a3edf3bd607e610e6685f76e5e98e` | `git fetch origin master` 后 `git rev-parse origin/master` 与 `git rev-parse HEAD` 完全一致 |
| 审计期间基线是否漂移 | 未漂移 | 上述两个 SHA 相等，且报告中的行号对应该 SHA |
| 工作区状态 | 干净 | `git status --short -uno` 无输出 |
| 审计期间的生产代码变更 | 无 | 本轮只新增本文件 |

本报告引用的全部行号均为上述 SHA 下的行号。

## 3. 组件定位与能力

`golang.org/x/sync/semaphore` 是 Go 官方维护的扩展库中的**加权信号量**：调用方以"权重"为单位申请
访问权，总量不超过容量。它的定位是"一把可以一次占用多份配额的锁"，而不是任务调度器、队列或限流器。

### 3.1 API 表面（v0.17.0 的全部导出符号）

| 符号 | 语义 |
|---|---|
| `NewWeighted(n int64) *Weighted` | 创建容量为 `n` 的加权信号量；`n` **不做校验** |
| `(*Weighted).Acquire(ctx context.Context, n int64) error` | 申请权重 `n`；成功返回 `nil`，失败返回 `ctx.Err()` 且**信号量状态不变** |
| `(*Weighted).TryAcquire(n int64) bool` | 非阻塞申请；失败返回 `false` 且状态不变 |
| `(*Weighted).Release(n int64)` | 归还权重 `n`；累计持有量变为负数时 `panic` |

没有任何容量查询、当前使用量查询、`SetLimit`、持有者标识或统计接口。这一点直接决定了它在
"需要把容量暴露给运维/接口"的项目里的可用性——本仓库的 `TaskScheduler.Limit()`
（`internal/app/task_scheduler.go:183`）与 `ActiveDispatches()`（`internal/app/task_scheduler.go:261`）、
`HostValidationService.InFlight()`（`internal/app/host_validation_service.go:212`）都是已对外暴露的观测点，
换成 `Weighted` 后这些观测能力需要另建结构维护。

### 3.2 源码行为逐条（对应 `x/sync@v0.17.0/semaphore/semaphore.go`）

以下每条都能在该文件的具体分支上找到依据，行号指该文件。

**(1) 权重总量如何计算、如何不超过容量。**
`Weighted{size, cur, mu, waiters list.List}`（第 28–33 行）：`size` 是构造时固定的容量，`cur` 是已分配权重。
两个放行路径的判断条件完全相同：`s.size - s.cur >= n && s.waiters.Len() == 0`（`Acquire` 第 55 行、
`TryAcquire` 第 117 行）。放行后 `s.cur += n`，`Release` 时 `s.cur -= n`（第 125 行）。
因此"不超过容量"由**单点互斥的算术不变式**保证：`0 <= cur <= size`，没有周期性校验，也没有后台回收。

**(2) `Acquire` 阻塞等待时 Context 取消的行为。**
进入等待时先 `PushBack` 一个 `waiter{n, ready}`（第 79–82 行），然后 `select` 两个分支（第 84 行起）：

- `<-done`（ctx 结束）时再 `select` 一次 `<-ready`：若此时已被放行，则 `s.cur -= n` 把令牌退回并
  `notifyWaiters()`（第 88–92 行）；否则把自身上下文从队列里摘掉，且若自己原本在队首且
  `size > cur`，调用 `notifyWaiters()` 让后面的等待者继续（第 93–100 行）。**令牌不会泄漏。**
- `<-ready`（被放行）后再检查一次 `done`：若 ctx 已结束，则 `s.Release(n)` 退回并返回 `ctx.Err()`
  （第 105–112 行）。**令牌不会泄漏，但会出现"放行后立即退回"的短暂占用。**

结论：取消路径是健全的，这也是该组件最值得肯定的部分。但注意它把"取消"与"申请失败"合并成同一个
返回值——调用方无法区分"我取消了"和"容量不够"。

**(3) 获取成功后 `Release` 的责任完全在调用方。**
结构体里没有任何持有者记录，`Release` 不校验调用者是否真的持有过对应权重（第 123–133 行）。
两个 goroutine 互相 `Release` 对方申请的权重，只要总量不为负就不会被发现。程序正确性依赖
"`defer sem.Release(n)` 与 `Acquire` 成对出现在同一作用域"这一**编码约定**，而不是任何运行时事实。

**(4) 错误释放、重复释放、负权重等参数错误的后果。**

| 情形 | v0.17.0 行为 | 后果 |
|---|---|---|
| `Release` 总量超过已持有 | `s.cur -= n` 后 `cur < 0` → `panic("semaphore: released more than held")`（第 127–131 行） | 进程崩溃；不是 `error` 返回 |
| 重复 `Release`（总量仍非负） | **静默通过**，`cur` 变小 | 凭空多出容量，并发上限被突破而无人察觉 |
| `Release(-n)`（负权重） | `cur` 变大（`cur -= -n`） | **静默超额分配**，容量不变式被破坏 |
| `Acquire(ctx, -n)` / `TryAcquire(-n)` | 无校验。`Acquire` 走快路径或队列；`TryAcquire` 直接 `cur += -n` | 静默破坏容量 |
| `NewWeighted(-1)` | 无校验 | 之后所有 `Acquire`（含权重 0）都会阻塞到 ctx 结束 |

版本差异（本机模块缓存实测）：`v0.17.0` 与 `v0.20.0` **没有** `n < 0` 校验；`v0.22.0` 在
`Acquire`、`TryAcquire`、`Release` 三个入口各加了 `if n < 0 { panic("semaphore: n < 0") }`。
也就是说，**仓库构建图里选中的 v0.17.0 恰好是"负权重静默破坏容量"的版本**。这条与
`AGENTS.md` 的"不使用 `panic` 处理普通的配置或用户输入错误"直接冲突：参数校验只能由调用方自己做，
组件本身在出错时选择崩溃。

**(5) 申请权重大于总容量时的实际行为。**
`Acquire` 在 `n > s.size` 时**不入队**，而是解锁后 `<-done` 阻塞、返回 `ctx.Err()`（第 68–72 行）：

```go
if n > s.size {
    // Don't make other Acquire calls block on one that's doomed to fail.
    s.mu.Unlock()
    <-done
    return ctx.Err()
}
```

这是最容易踩的坑：`Acquire(context.Background(), n)` 在 `n > size` 时**永久挂起**，既不报错也不占用
容量；用 `context.WithTimeout` 时返回的是 `context.DeadlineExceeded`，与"排队超时"无法区分。
正确的用法必须在调用前自检 `n <= size`，而这正说明"容量与权重的一致性"必须由调用方自己维护。
`TryAcquire(n > size)` 则立即返回 `false`（第 117 行）——两种入口对同一个错误配置给出完全不同的表现。

**(6) 零权重申请的实际行为。**
`Acquire(ctx, 0)`：快路径 `size-cur >= 0` 恒真，但还需 `waiters.Len() == 0`。因此
"队列为空时立即成功、`cur` 不变"；"队列非空时排到队尾，按 FIFO 顺序被放行"。
`TryAcquire(0)` 同理（有等待者时返回 `false`）。`Release(0)` 是空操作。
上游测试 `TestWeighted` 就用 `HammerWeighted(sem, int64(i))` 覆盖了 `i == 0` 的情形。
结论：零权重**不是**"无条件通过"，它会受队首阻塞影响；把它当"永远成功的探测"是错的。

**(7) 等待队列的顺序、公平性与 head-of-line blocking。**
`waiters` 是 `container/list` 的 FIFO 队列，`notifyWaiters`（第 136–164 行）**只从队首依次放行**，
遇到第一个装不下的等待者就 `break`。同时快路径要求 `waiters.Len() == 0`，即**新来的调用不能插队**。
两者共同构成严格 FIFO + 无插队（no barging）：公平性好，代价是队首阻塞——一个大队列头会卡住后面所有
小请求，即使当前剩余容量足够满足它们。

**(8) 大权重请求是否影响小权重请求的吞吐。**
会，而且是设计选择。源码注释明确写了这是为反饥饿（第 141–153 行的读写锁类比：若允许读者插队，
写者永远饿死）。因此"用加权信号量提升小请求吞吐"是反的：**加权信号量会牺牲小请求吞吐来换取
大请求的确定性**。若目标场景是"小请求应当优先"，该组件帮不上忙，需要优先级队列或分级容量。

**(9) 容量是否支持动态调整，以及调整时的并发问题。**
不支持。`size` 只在 `NewWeighted` 中写入一次，之后没有任何写路径。配置热更新只能"新建一个
`Weighted` 再替换指针"，而：

- 旧实例上仍有 goroutine 持有令牌并在之后 `Release` → 退还到旧实例；
- 新实例从 `cur = 0` 起算 → **同一份物理资源被两套计数器各算一遍**，实际并发可以翻倍；
- 旧实例上的等待者会继续按旧容量放行。

也就是说，"动态容量"需要调用方自己设计"排空 → 切换 → 重新进入"的交接协议，组件不提供任何帮助。
对比 `errgroup.SetLimit` 至少有"有活动 goroutine 时 `panic`"这条显式保护（见 `errgroup.md` 第 2.1 节），
`Weighted` 连这层保护都没有。

**(10) `Acquire` 与 `TryAcquire` 在阻塞、排队和拒绝语义上的差异。**

| 维度 | `Acquire` | `TryAcquire` |
|---|---|---|
| 容量不足 | 入队等待（FIFO） | 立即 `false`，不入队 |
| `n > size` | **阻塞到 ctx 结束**，返回 `ctx.Err()` | 立即 `false` |
| 存在等待者 | 自己也要入队（不许插队） | 立即 `false`（不许插队） |
| 可取消 | 是（ctx） | 不涉及 |
| 失败表达 | `error`（只有 ctx 的原因） | `bool`（无原因） |
| 典型误用后果 | 忘记设超时就永久挂起 | 在高并发下持续拒绝，调用方需要自己退避重试 |

两者都不是"排队并且能报告队列长度"的语义，也都不能替代"有界队列 + 满即拒绝"——
本仓库 `HostValidationService` 现有的 `ErrValidationQueueFull`（`internal/app/host_validation_service.go:19`）
语义，`Acquire` 无法表达，`TryAcquire` 能表达但会丢掉"已入队"和"排队中"的可观测状态。

### 3.3 适用边界

**匹配**：同一进程内、同一资源的**多单位配额**，且不同请求需要的单位数不同——
典型是"一个读操作占 1 个权重、一个写/整理操作占 N 个权重"，或"每 GB 内存算 1 个权重"。

**不匹配**（本仓库全部落在这里）：

- 单一资源互斥 → `sync.Mutex` / `keyedMutex` 更直接；
- 等成本任务的数量上限 → `chan struct{}` 令牌或固定 worker pool 更直接，且能表达"队列满即拒绝"；
- 跨进程容量 → 需要数据库仲裁，进程内计数器无论如何都无法承担；
- 连接数准入 → `database/sql` 自身已实现；
- 输出字节、命令长度、超时 → 是数据/时间限制，不是并发容量。

## 4. 版本与依赖情况

| 检查项 | 结论 | 依据 |
|---|---|---|
| `go.mod` 是否声明 `golang.org/x/sync` | **否**（连 `// indirect` 都没有） | `go.mod` 直接依赖仅 `pkg/sftp`、`spf13/cobra`、`x/crypto`、`x/term`、`yaml.v3`、`modernc.org/sqlite`；`x/sync` 未出现在任何 `require` 块 |
| 代码是否导入 `golang.org/x/sync/*` | **否** | `grep -rn "golang.org/x/sync" --include=*.go .` 无输出 |
| `go.sum` 中的记录 | 存在 `v0.17.0` 的 `h1:` 与 `/go.mod` 两条记录（第 72–73 行），另有 7 条更早版本的 `/go.mod` 记录（第 65–71 行） | `go.sum:65-73` |
| 这些记录从哪来 | 传递依赖的**测试链** | `go mod why -m golang.org/x/sync` 输出：`internal/storage/sqlite → modernc.org/sqlite → modernc.org/libc → modernc.org/libc.test → golang.org/x/tools/go/packages → golang.org/x/sync/errgroup` |
| `go mod graph` 中的边 | `golang.org/x/exp@v0.0.0-20251023183803-a4bb9ffd2546 → v0.17.0`、`modernc.org/libc@v1.67.6 → v0.17.0`（其余边指向更旧版本，被 MVS 提升） | `go mod graph \| grep golang.org/x/sync` |
| 构建图实际选中版本 | `golang.org/x/sync v0.17.0` | `go list -m all \| grep sync` |
| 该版本发布时间 | 2025-08-13 | 本机代理缓存 `@v/v0.17.0.info` |
| 官方已发布的最新版本 | 至少 `v0.24.0`（2026-09-23） | 本机代理缓存 `@v/` 目录中存在 `v0.20.0`、`v0.22.0`、`v0.23.0`、`v0.24.0` 的 `.info`，其中 `v0.22.0` 有完整 `.zip`。**无法联网复核**：`proxy.golang.org` 与 `raw.githubusercontent.com` 在本机解析到内网地址，抓取工具按策略拒绝；按任务约束不为此下载新版本 |
| 若引入，是否要新增下载 | 源码已在缓存中（`v0.17.0` 与 `v0.22.0` 均已解压），但引入仍需在 `go.mod` 增加 `require` | 属"新增直接依赖"这一政策动作，不是技术障碍 |
| 版本层面的额外风险 | **构建图里选中的 v0.17.0 缺少负权重校验**（v0.22.0 才加入 `panic`），若真要用加权语义，`v0.17.0` 是最差的选择 | 三个版本源码逐行 diff |

一句话：`go.sum` 里有 `x/sync` 是**传递依赖的产物，不是本仓库使用它的证据**；把它当作"项目已经在用、
只差一个 `require`"来论证引入，是错误推理。

## 5. 仓库资源限制与并发准入实现清单

### 5.1 需求语义分层

审计必须先拆开六类互不相同的问题，否则会把"要限制并发"一律读成"该用信号量"。

| # | 语义 | 本仓库是否存在 | 实际实现 | 加权信号量是否匹配 |
|---|---|---|---|---|
| 1 | 同一进程内的**多单位配额**（不同请求成本不同） | **不存在** | 无 | 匹配，但无对象 |
| 2 | 等成本任务的**数量上限** | **存在** | `TaskScheduler` 名额统计（数据库事实 + 进程内在途）、`HostValidationService` 固定 worker | 不匹配：这是一个**计数**，权重恒为 1，加权是多余维度 |
| 3 | **单一资源互斥** | **存在（主要形态）** | `keyedMutex` 5 处（节点、节点+环境、容器、规划会话、任务），+ 数据库租约 | 不匹配：`NewWeighted(1)` 是"更慢的 `Mutex`" |
| 4 | **跨进程执行权 / 容量仲裁** | **存在（核心）** | 任务租约（owner/epoch 条件更新）、步骤占用、`planning_session_leases`、`workflow_runs` 部分唯一索引 | 不匹配：进程内计数器无法实现 |
| 5 | **连接池容量** | **存在** | `database/sql` 的 `SetMaxOpenConns(4)` / `SetMaxIdleConns(4)`；SSH 无池（每次操作新建连接） | 不匹配：重复实现 `database/sql` 已提供的能力 |
| 6 | **数据/时间边界** | **存在** | `sshclient.DefaultMaxOutputBytes = 1MB`、`docker.MaxContextFileBytes = 4MB`、各类超时 | 不匹配：不是并发容量 |

**结论**：仓库的容量压力全部落在第 2、3、4 类，恰好是加权信号量最不匹配的三类。

### 5.2 清单（按机制，行号对应审计基线）

**A. 任务级并发上限（唯一的"数量上限"）**

- `internal/app/config/config.go:71` `SchedulerConfig.MaxConcurrentTasks`；校验在 `config.go:205`（必须 > 0）
- `internal/app/runtime_config.go:22-23`、`48`：作为运行期快照对外暴露
- `internal/app/task_scheduler.go:73-76`、`104`、`183-185`：`MaxConcurrentTasks` → `limit` → `Limit()`
- `internal/app/task_scheduler.go:304` `Scan`：`scanMu`（`119-127`）串行化"统计名额 → 领取 → 登记"
- `internal/app/task_scheduler.go:466` `countOccupiedSlots`：按数据库事实计数，注释第 462–465 行明确写
  "**基于数据库事实的进程级上限**……不提供跨进程的全局容量仲裁……全局配额需要数据库仲裁，属于后续里程碑"
- `internal/app/task_scheduler.go:516` `dispatch` → `588` `register` → `630` `run` → `687` `settleFailedDispatch`
- 判据在领域层：`internal/domain/task_queue.go:509` `DecideTaskSlotState`

**B. 固定 worker + 有界队列（唯一一处 worker pool）**

- `internal/app/host_validation_service.go:25-32`：`DefaultValidationQueueSize = 128`、`DefaultValidationWorkers = 2`、
  `validationStopTimeout = 20s`
- `internal/app/host_validation_service.go:117` `Start`：`make(chan validationKey, s.queueSize)`（第 126 行）+ 起 `workers` 个 `go s.worker`
- `internal/app/host_validation_service.go:182` `SubmitHost`：`inflight` 去重（192–196），入队用
  `select { case queue <- key: ... default: ... return ErrValidationQueueFull }`（200–208）→ **满即拒绝，不阻塞**
- `internal/app/host_validation_service.go:229` `worker`、`242` `run`、`252` `execute`
- `internal/app/host_validation_service.go:140` `Stop`：`cancel()` → `go wg.Wait()` → `select` 三种结局
  （完成 / 20s 超时 / ctx 取消）
- 构造处 `internal/server/server.go:982-989` **不传** `Workers` / `QueueSize`，即生产固定为 2 / 128，
  配置文件中没有对应开关

**C. 按标识互斥（`keyedMutex`）**

- `internal/app/environments.go:40-60` `keyedMutex`（`map[string]*sync.Mutex`，按需创建，**从不回收**）
- `internal/app/environments.go:354`：按 `lockKey(host, ref)` 串行"创建环境版本"
- `internal/app/environments_build.go:71`：按同一 `lockKey` 串行"构建镜像"
- `internal/app/containers.go:95`：按 `host.ID` 串行容器创建（`createLocks`）；`containers.go:24-25` 注释说明其目的是
  "避免并发创建时名称冲突"
- `internal/app/planning_runtime.go:265`（`Prepare`）、`523`（`Release`）、`internal/app/planning_agent.go:224`
  （`RunRole`）：按 `sessionID` 串行，紧接数据库租约 `leases.Acquire`
- 任务级 `keyedTaskMutex`（`internal/app/task_workflow_exec.go:124` `lockWithin`）

**D. 跨进程执行权（数据库仲裁）**

- 任务租约：`TaskSchedulerLeasePort`（`internal/app/task_scheduler.go:57-62`）的 `Claim` / `Renew` / `Release`，
  owner + epoch 条件更新；`dispatch` 拿到租约后还要用 `domain.DecideTaskClaim` 二次仲裁（第 548 行）
- 规划会话租约：`internal/app/planning_lease.go:125` `Acquire`，三种错误类别
  （`errPlanningLeaseConflict` / `errPlanningLeaseLost` / `errPlanningLeaseUnavailable`，
  `planning_lease.go:24-33`），后台 `renewLoop`（`planning_lease.go:163`）
- 步骤占用与 `workflow_runs` 部分唯一索引（见 [architecture.md](../architecture.md) 第 316、347、652 行附近）

**E. 连接池**

- `internal/storage/sqlite/db.go:31-33`：`defaultMaxOpenConns = 4`、`defaultMaxIdleConns = 4`
- `internal/storage/sqlite/db.go:187-189`：`SetMaxOpenConns(maxOpenConns)`、`SetMaxIdleConns(min(4, maxOpenConns))`、
  `SetConnMaxLifetime(0)`
- DSN 侧：`_pragma=busy_timeout(5000)`、`foreign_keys(0)`、`journal_mode(WAL)`、`synchronous(NORMAL)`、
  `_txlock=immediate`（`db.go:194-212`）
- **SSH 没有连接池**：`internal/app/docker_session.go:135` 每次远程操作都 `s.ssh.Connect(ctx, hostID)`，
  `defer session.close`（第 144–146 行）

**F. 数据/时间边界（不是信号量场景，列出以证明"已排查"）**

- `internal/sshclient/client.go:157-158` `DefaultMaxOutputBytes = 1MB`；`internal/sshclient/session.go:169-170` 有界 writer
- `internal/docker/context.go:19-20` `MaxContextFileBytes = 4MB`
- `internal/process/supervisor.go:122` 行通道容量 64（防止慢消费者阻塞管道，不是并发上限）
- `internal/httpserver/server.go:173-176`：`http.Server` 只设 `ReadHeaderTimeout`，**没有**连接数/请求数上限

**G. 不存在的机制（明确记录，避免误判）**

- 没有限速器（`rate.Limiter`、令牌桶、漏桶）
- 没有指标系统（全仓库无 `prometheus` / `metrics`）
- 没有任务队列表之外的排队设施（构建无队列、步骤无队列）
- 没有 `semaphore` 或任何 `Acquire/Release` 令牌计数

## 6. 候选位置逐项分析

### 6.1 `TaskScheduler`（`internal/app/task_scheduler.go`、`internal/domain/task_queue.go`）

**当前实现。** `Scan`（第 304 行）在 `scanMu` 内做三步：`countOccupiedSlots`（第 466 行）读数据库算占用 →
`available = limit - occupied`（第 321 行）→ 按领域排序依次 `dispatch`（第 516 行）。`dispatch` 领取租约
成功后 `register` 登记 `dispatched`（第 588 行）、`runs.Add(1)`、`go s.run(claim)`。

**`scheduler.max_concurrent_tasks` 的实际含义。** 它是**"同一份数据库上真实占用名额的任务数不得再增加"
的阈值**，占用判定由 `domain.DecideTaskSlotState`（`internal/domain/task_queue.go:509`）给出：
任务租约有效 → `Claimed`（`verifying`/`saving` 时为 `Finalizing`）；执行记录持有执行权 → `Executing`；
两者都没有 → `Idle` / `AwaitingHuman` / `Indeterminate`。它与"本进程启动了几个 goroutine"无关。

**`countOccupiedSlots` 的数据库状态读取。** 读 `ListTasks(ExcludeStatuses=[completed, cancelled])`（第 467 行），
对每个任务：租约有效则直接判定（省一次查询），否则读 `GetActiveWorkflowRun`。最后再叠加
**本进程已登记但租约尚未写回的分派**（第 487–503 行），这段是"消除 `dispatch` 与 `run` 之间的竞态窗口"。

**`scanMu` 的作用。** 它把"读快照 → 算空名额 → 领取"放进同一个临界区。注释（第 119–127 行）记录了实测事故：
"上限 2 时并发扫描可领取 3 个"。集成测试 `TestSchedulerDoesNotExceedLimitUnderConcurrentScans`
（`tests/integration/local/scheduler/concurrency_acceptance_test.go:216`）用 `scanBarrier` 把这个窗口
确定性触发。**注意：`scanMu` 是互斥锁，不是容量限制**——它保证"一次只算一份名额"，本身不表达上限。

**`dispatched` 的作用。** 进程内在途分派登记表，注释（第 109–111 行）明确"是第一道防线而不是一致性保证：
跨进程仍由任务租约的条件更新拒绝重复领取"。它在 `run` 结束时由 `unregister`（第 617 行）清理，
同时调用 `cancel()`，使 `Stop` 能逐个取消在途分派。

**租约 / owner / epoch 与并发领取的关系。** `Claim` 生成 `Owner = "scheduler-" + 新 ID`，条件更新把
`lease_epoch` 递增。拿到租约后 `dispatch` **还要**用 `domain.DecideTaskClaim(input, handle.Owner)` 二次仲裁
（第 548 行）：领域判定拒绝就立刻 `Release` 自己刚拿到的租约（第 549–558 行）。这层"存储成功 ≠ 业务允许"
的复核是无法用信号量表达的。

**多实例竞争时的真实容量语义。** 必须准确表述：

- 任务**不会**被两个实例重复领取——跨进程互斥来自数据库条件更新；
- 但**并发上限是进程级的**：`countOccupiedSlots` 的注释（第 462–465 行）写明"两个进程可能各自看到同一份
  占用快照后各领一个任务，使总量短暂超过上限"；
- 集成测试 `TestTwoSchedulersClaimDistinctTasksWithinCapacity`（同文件第 129 行）文件的契约说明
  （第 121–128 行）同时记录了两件事：断言"总量不超过进程数 × 上限"，并明确**不断言**"总量不超过单进程上限"，
  因为"跨进程全局配额需要数据库仲裁（P3-M5 §3 记为后续决策）"；该用例具体时序下第二个调度器因读到已占用
  快照而未领取（第 190–199 行的断言），但这属于该时序的结果，不是普遍保证。

**绝不能把当前限制称为部署级全局配额。** 它是"数据库事实 + 本进程在途"的进程级阈值。

**`semaphore.Weighted` 是否有实际收益：没有，且为负收益。**

| 维度 | 当前实现 | 换成 `NewWeighted(limit)` |
|---|---|---|
| 容量来源 | 数据库真实占用（含其他进程已领取的任务） | 本进程内存计数，看不到其他进程 |
| 上限是否跨进程 | 部分（互斥跨进程，容量不跨进程） | 完全不跨进程，多实例部署时上限被放大为 N 倍 |
| 容量与业务状态一致性 | 与任务状态机强绑定（`DecideTaskSlotState`） | 与业务状态无关，只是数字 |
| 观测 | `Limit()`、`ActiveDispatches()`、`Status()` | 需要另建结构维护 |
| 取消 / 停止 | `scanMu` + `runs`/`scanning` WaitGroup + 超时 | `Acquire` 需额外 ctx，仍需自己的 WaitGroup |
| 名额回收时机 | 由任务真实状态决定（可能数十分钟） | 由 `Release` 调用决定，容易与状态脱节 |

更关键的是**权重无意义**：每个任务占 1 个名额，没有"这个任务占 2 个名额"的产品语义。引入加权维度只是
把 `available--` 换成 `Acquire(ctx, 1)`，代码不减反增（要处理 ctx 取消、要区分 `n > size` 的挂起、
要自己保证 `Release` 与 `unregister` 的配对）。

**结论：不适用。**

### 6.2 `HostValidationService`（`internal/app/host_validation_service.go`、`host_validation.go`）

**固定 worker 数量。** 默认 2（第 29 行），`Start` 里起 `workers` 个 `go s.worker(serviceCtx)`（第 129–132 行）。
构造处 `internal/server/server.go:982` 不传 `Workers`，所以生产恒为 2。

**有界队列长度。** `queue = make(chan validationKey, 128)`（第 126 行）。

**`inflight` 去重语义。** `map[validationKey]struct{}`，键是 `(hostID, generation)`（第 53–54 行）。
`SubmitHost` 在 `s.mu` 内先查 `inflight`，命中直接返回 `nil`（**幂等，不是错误**，第 192–195 行）；
未命中则登记并入队。`run` 结束（成功、失败、取消）由 `defer` 删除（第 243–247 行）。
因此 `inflight` 同时承担"去重"和"排队+执行中"两态记录，`InFlight()`（第 212 行）可直接观测。

**入队时队列已满的返回行为。** `select` 的 `default` 分支：从 `inflight` 里回滚，返回
`ErrValidationQueueFull`（第 203–208 行）。**非阻塞拒绝**，HTTP 层可以把它翻译成一个明确的 4xx/5xx，
而不是让请求挂住。

**Stop 语义。** 置 `stopped` 后 `cancel()`；`go wg.Wait(); close(done)`；`select { <-done; <-time.After(20s);
<-ctx.Done() }`（第 140–168 行）。三个结局都有明确错误文本。

**现有 worker pool 是否所有任务同权？** 是。`HostValidator.runChecks`（`internal/app/host_validation.go:191`）
是**串行三步**：SSH 连接检查 → Docker 版本检查 → Docker CLI 帮助检查，前一步失败时后两步记 `not_run`。
每个校验任务消耗：1 条 SSH 连接 + 2 条远程 Docker 命令，成本可视为常数。**没有任何可量化的权重差异。**

**三种方案比较。**

| 方案 | 解决的问题 | 缺陷 | 在本场景的净收益 |
|---|---|---|---|
| 当前 worker + 有界队列 | 并发固定、队列有界、满即拒绝、去重、可停止 | 容量不可配置（仅代码内默认值） | 基准 |
| channel 作为简单信号量（`sem <- struct{}{}`） | 并发上限 | 丢失去重、丢失"满即拒绝"、丢失 worker 生命周期 | 负 |
| `semaphore.Weighted` | 并发上限（加权） | 无双队列、无去重、`Acquire` 阻塞或需 `TryAcquire` 自旋；无容量观测 | 负 |

**特别判断**：所有校验任务成本相同 → 加权机制只会把已有的 worker pool 换成另一种写法。
真正能改进的点是"`Workers` / `QueueSize` 目前写死在代码里、配置文件中没有开关"
（`internal/config/config.go` 的 `Config` 只有 `App`/`Server`/`Storage`/`Workspace`/`Scheduler`/`SSH`），
这是**配置可见性**问题，与信号量无关。

**结论：不适用。**

### 6.3 `EnvironmentBuildManager`（`internal/app/environments_build_manager.go`、`environments_build.go`）

**同一节点、同一环境的构建去重。** 键是 `lockKey(host.ID, request.Ref)`（`environments.go:378`，
即 `hostID + "|" + ref.Label()`），`Submit` 在 `m.mu` 内检查 `m.running[key]`，命中返回
`ErrEnvironmentBuildInProgress`（第 239–247 行）。注意键里**不含版本号**：同一环境的 v0001 与 v0002
在 `Submit` 层面互斥。`EnvironmentService.Build` 内还有一把同样键的 `keyedMutex`（`environments_build.go:71`），
所以"同节点同环境同时只有一个构建"在进程内是成立的。

**不同环境构建是否可以无限并行。** 可以。`m.running` 只做键去重，没有容量上限，也没有队列；
`Submit` 成功即 `m.wg.Add(1); go m.execute(...)`（第 258–262 行），与已有多少构建在跑无关。

**构建任务的实际资源消耗是否存在可量化权重。** **不存在。** 仓库里：
- 没有 CPU/内存配额概念（`grep` 全仓库无 cgroup、无 `--memory`/`--cpus` 构建参数）；
- 没有构建耗时或产物大小的记录用于派生权重；
- 没有指标系统；
- 产品配置里没有任何构建容量字段（`SchedulerConfig` 只有 `MaxConcurrentTasks` / `MaxRetriesPerStep`）。

因此"给构建分配权重"只能靠**自行发明** CPU/内存/镜像层数的数字，这正是任务书禁止的做法。

**当前是否有队列、全局并发上限或跨实例资源限制。** 三个都没有：

- 无队列：`Submit` 只受理或拒绝，不受理"排队稍后执行"；
- 无全局上限：`m.running` 无容量；
- 无跨实例限制：`UpdateEnvironmentVersion`（`environments_build.go:145-149`）是**无条件写入**
  `MarkBuilding`，没有"状态仍为 pending/expected 才写"的条件更新，因此**数据库层也没有构建闸门**；
  两个进程可以对同一 (节点, 环境) 同时进入构建，各自上传上下文、各自执行 `docker build`，最后互相覆盖记录。

另有一条容易漏掉的构建入口：**任务启动时的镜像准备**。
`internal/app/execenv_prepare.go:220` `prepareImage` → `s.images.Build` → `EnvironmentService.Build`，
它不经过 `EnvironmentBuildManager`，因此**不受 `m.running` 去重保护**（但受同一把 `keyedMutex` 串行化）。
第三条入口是 CLI：`internal/cli/docker_env.go:220`，同步单次构建。

**构建取消、停止、uncertain 状态与远程副作用如何收敛。**

- `Stop`（第 156 行）：`cancel()` → `go wg.Wait()` → `select { <-finished; <-ctx.Done() }`，超时由上层生命周期表达；
- 构建被中断时 `environments_build.go:153-159` 记 `MarkUncertain`（"镜像可能已经生成，也可能没有"），
  并且用 `context.WithoutCancel` 落库（`environments_build.go:163-169`），避免记录永久停在 `building`；
- 进程重启后 `convergeUnmanagedBuilds`（`environments_build_manager.go:337`）把所有遗留 `building` 收敛为 `uncertain`，
  `UpdateEnvironmentVersionIfStatus(..., [Building])` 是**条件**更新，`ErrConflict` 时跳过，不覆盖他人结论。

**这是重点候选吗？** 是——它是仓库里唯一存在真实容量控制缺口的地方。详见第 8 节。

**结论：存在缺口，但缺口不是"权重"，`semaphore.Weighted` 不是正确形态。**

### 6.4 容器创建与规划会话互斥（`containers.go`、`environments.go`、`planning_runtime.go`、`planning_agent.go`）

**keyed mutex 的作用。**

- `ContainerService.createLocks.Lock(session.host.ID.String())`（`containers.go:95`）：按执行节点串行化
  **容器创建**，目的是"避免并发创建时名称冲突"（第 24 行注释）。它只覆盖创建，不覆盖后续启动/停止/删除。
- `EnvironmentService.locks.Lock(lockKey(host, ref))`（`environments.go:354`、`environments_build.go:71`）：
  按"节点 + 环境"串行化版本创建与镜像构建。键里不含版本号，故同环境的多个版本也互斥。
- `PlanningRuntimeService.locks.Lock(sessionID)`（`planning_runtime.go:265` `Prepare`、`523` `Release`）、
  `PlanningAgentService.locks.Lock(sessionID)`（`planning_agent.go:224` `RunRole`）：按规划会话串行化。
  三处都在拿锁之后**立刻** `ctx.Err()` 自检（如 `planning_agent.go:226-229`），再进入数据库租约。

**与数据库持久化租约的职责边界。** 很清晰，注释也写明了（`planning_agent.go:222-224`）：
"进程内先排队，再参与跨进程竞争：同进程的第二个调用不会在第一个调用持租期间无谓地拿到一个
'正被占用'的错误"。即 `keyedMutex` 负责**同进程排队**，数据库租约负责**跨进程互斥**，
两者不是替代关系。相比之下 `EnvironmentService` 与 `ContainerService` 只有进程内互斥，**没有数据库租约**，
因此多实例下同一 (节点, 环境) 仍可能被两个进程同时构建——这是"缺租约"，不是"缺信号量"。

**区分互斥、并发容量限制和加权资源限制。** 这三者在本仓库的分工是：

- 互斥（`keyedMutex`）：同一标识同时只允许一个操作；
- 并发容量（`TaskScheduler` 名额、`HostValidationService` worker）：全局或进程内允许 N 个操作；
- 加权资源：**不存在**。

**不能仅因存在 `sync.Mutex` 就认为应换成 Semaphore。** 用 `NewWeighted(1)` 表达互斥的净效果是：
多了一层 `cur` 计数与等待者队列，失去 `defer unlock()` 的简洁，失去 `TryLock` 语义（Go 1.18+ 的
`sync.Mutex.TryLock` 存在，`Weighted` 的等价物是 `TryAcquire(1)`），并引入 `Acquire` 的 ctx 取消与
`n > size` 挂起陷阱。**纯负收益。**

**结论：不适用。**

### 6.5 SQLite 数据库连接池（`internal/storage/sqlite/db.go`、`store.go` 及相关装配）

**现有连接池控制。** `openSQL`（`db.go:172`）在 `sql.Open` 之后设 `SetMaxOpenConns(4)`、
`SetMaxIdleConns(min(4, maxOpenConns))`、`SetConnMaxLifetime(0)`（第 187–189 行）。`MaxOpenConns` 可由
`Options.MaxOpenConns` 覆盖（第 177–181 行）。配套的 SQLite 侧等待机制是 `busy_timeout(5000)` 与
`_txlock=immediate`（第 194–212 行），另有初始化阶段的 `retryBusy`（`db.go:94`，调用点 `db.go:80` / `83`）。

**是否存在重复的应用层连接限制。** 没有。全仓库没有任何"取连接令牌"的代码；`storage.Store` 的方法
直接落到 `sqlPool`（构造 `store.go:87`、定义 `store.go:151-152`）上，由 `database/sql` 自身排队与限流。

**`semaphore.Weighted` 能否提供连接池没有的能力。** 不能。`database/sql` 已经实现：
等待可用连接、`ctx` 取消时退出等待、连接复用与空闲回收、连接超时。
`semaphore.Weighted` 能表达的额外维度只有"按权重占用连接"，而本仓库只有 SQLite 一种数据库、
所有查询同权——**没有权重的来源**。

**实际上还需要注意**：`*sql.Tx` 会占用连接直到提交或回滚，若在事务内做远程调用（SSH/Docker），
4 个连接会被长时间占住。这是"不要在事务里做远程 I/O"的编码约束问题，不是信号量问题。

**结论：不适用。**

### 6.6 其他远程调用与资源边界（SSH、Docker、HTTP、Agent、Session、验证）

按第 5.1 节的六类语义逐项对照：

| 调用链 | 并发任务数量 | 单一资源互斥 | 速率限制 | 连接池 | 可计量资源配额 | 数据/时间限制 |
|---|---|---|---|---|---|---|
| SSH 连接（`internal/sshclient/dialer.go`） | 无上限（每操作新建连接） | 无 | 无 | **无池** | 无 | `ConnectTimeout`、`DefaultMaxOutputBytes` |
| 远程 Docker（`docker/runner.go`、`engine_transport.go`） | 由调用方决定 | 由 `keyedMutex` 在上层施加 | 无 | 传输层复用 `sshclient.Session`（单操作内） | 无 | 各类超时、`MaxOutputBytes` |
| HTTP Server（`internal/httpserver/server.go`） | 无上限（`net/http` 每连接一 goroutine） | `closeStreams sync.Once` | 无 | 无 | 无 | `ReadHeaderTimeout`、`shutdownTimeout` |
| Agent / Pi（`internal/agent/pi/command.go`） | 每 Step 一个进程，Step 之间串行 | 步骤占用（数据库） | 无 | n/a | 无 | `MaxOutputBytes`、执行超时 |
| Session / 归档 | 每任务一个 | 任务级 `keyedTaskMutex` | 无 | n/a | 无 | 归档超时 |
| 校验（`HostValidationService`） | **2（唯一有界）** | `inflight` 去重 | 无 | 无 | 无 | 20s 停止超时 |
| 环境构建（`EnvironmentBuildManager`） | **无上限（缺口）** | `keyedMutex` + `m.running` 键去重 | 无 | 无 | 无 | 构建超时、停止超时 |

**必须明确区分**：

- `DefaultMaxOutputBytes`、`MaxContextFileBytes`、命令长度、超时——**不是**并发容量，不能当信号量场景；
- `http.Server` 没有连接数上限，是**已知的产品取舍**（`DefaultServerHost = 127.0.0.1`，默认只监听回环，
  未实现远程访问认证前不对外开放网卡，见 `internal/config/config.go:20-21` 与 `ServerConfig` 注释），
  在当前部署形态下不构成资源控制缺口；
- SSH 无连接池是**吞吐/延迟**问题（每次操作一次握手），不是并发容量问题；若要优化，正确做法是连接复用，
  不是信号量。

## 7. 替代方案比较

### 7.1 六方案能力对照

| 能力 | `sync.Mutex` / `RWMutex` | 有缓冲 channel 令牌 | worker pool + 有界队列 | 数据库条件更新 / 租约 / epoch | `database/sql` 连接池 | `semaphore.Weighted` |
|---|---|---|---|---|---|---|
| 单一资源互斥 | ✅ 最直接 | ✅（容量 1） | 笨重 | ✅ 跨进程 | ✗ | ✅ 但更慢 |
| 等成本并发上限 | ✗ | ✅ 最直接 | ✅ | ✗（要自建计数） | ✗ | ✅ 但多余维度 |
| 队列满即拒绝 | ✗ | ✅ `select default` | ✅ | ✗ | ✗ | ⚠️ 仅 `TryAcquire`，丢排队态 |
| 排队且有界 | ✗ | ✗ | ✅ | ✗ | ✅（隐式） | ✅（队列不可观测） |
| **加权容量** | ✗ | ⚠️ 需自建 | ✗ | ✗ | ✗ | ✅ **唯一优势** |
| 跨进程生效 | ✗ | ✗ | ✗ | ✅ | 部分 | ✗ |
| 容量可观测 | ✗ | ✅ `len`/`cap` | ✅ | ✅ | ✅ `Stats()` | ✗ |
| 容量可动态调整 | n/a | ✗ | 需重启 | ✅ | ✅ | ✗ |
| ctx 取消 | ✗（`sync.Mutex` 不可取消） | 需自建 `select` | ✅ `select` | ✅ | ✅ | ✅ |
| 参数出错时的表现 | 行为确定 | 确定 | 确定 | 确定（条件更新返回 `ErrConflict`） | 确定 | ⚠️ `panic` 或静默破坏 |
| 与仓库现有错误约定一致 | ✅ | ✅ | ✅ | ✅ | ✅ | ❌（`panic` / 只有 `ctx.Err()`） |

### 7.2 逐项回答任务书要求的九个比较维度

对每个候选场景（`TaskScheduler` 名额、校验 worker、构建容量、容器创建、规划会话、SQLite 连接）：

1. **当前方案解决了什么问题**：见第 5、6 节逐项说明；核心事实是"容量来自数据库状态机"或"单一资源互斥"。
2. **当前方案是否存在缺陷或资源瓶颈**：唯一实质缺陷是**环境构建无并发上限、无跨进程闸门**（第 8 节）；
   其余为可配置性与可观测量问题（校验 worker 数写死、HTTP 无连接上限），不属并发原语缺陷。
3. **使用加权信号量后是否减少真实复杂度**：不会。所有场景都需要保留"数据库状态判定 + 停止收敛 +
   错误分类"这三块代码，信号量只能替换其中的 `available--` 或一把锁。
4. **是否需要真正的权重模型**：不需要。仓库没有任何权重来源（无 CPU/内存配额、无指标、无产品配置项）。
5. **是阻塞等待、非阻塞拒绝还是排队**：
   - `TaskScheduler`：非阻塞（本轮没有名额就返回，等下一次 tick）；
   - `HostValidationService`：**非阻塞拒绝 + 有界队列**；
   - 构建：非阻塞拒绝（`ErrEnvironmentBuildInProgress`）或直接受理，无排队；
   - `keyedMutex`：阻塞排队（含挂起风险，但已有 `ctx.Err()` 自检兜底）；
   - 数据库租约：非阻塞拒绝（`ErrConflict`）。
   加权信号量只提供"阻塞"与"非阻塞拒绝"两种，无法表达"有界队列 + 拒绝"。
6. **Context 取消后如何保证令牌不泄漏**：`Acquire` 的取消路径本身是健全的（第 3.2 节第 (2) 条），
   但仓库自己的机制在这方面更强——`TaskScheduler` 用"数据库事实"回收名额，**进程崩溃也不会漏名额**；
   信号量在进程崩溃时直接丢失全部状态（这对进程内计数器是可接受的，但恰好说明它不能承担
   "名额必须持久"的语义）。
7. **业务停止时如何等待收敛**：仓库每个服务都是"拒绝新请求 → 取消在途 → 等待 `WaitGroup` → 超时"三段式
   （`host_validation_service.go:140`、`task_scheduler.go:216`、`environments_build_manager.go:165`）。
   信号量不参与这套骨架，引入后仍需保留全部骨架代码。
8. **是否会影响不同任务之间的隔离**：会。严格 FIFO + head-of-line blocking 意味着一个大构建会阻塞
   队列里所有小校验/小任务。当前的分机制设计（任务名额、校验 worker、构建键去重各自独立）天然隔离；
   合并到一个 `Weighted` 上会**破坏**现有隔离。
9. **是否需要跨进程一致性**：`TaskScheduler` 与构建都需要（多实例共享同一数据库/主机）；
   `keyedMutex` 与校验 worker 不需要（纯进程内核）。凡需要跨进程者，进程内信号量都不合格。

## 8. 环境构建容量缺口的单独结论

这是本次审计唯一发现的真实资源控制缺口，按要求逐项说明。

**缺口是什么。** 环境镜像构建**没有并发上限**：`EnvironmentBuildManager.Submit` 只按 (节点, 环境) 去重，
不同环境可以无限并行（第 6.3 节）；任务启动路径的镜像准备（`execenv_prepare.go:220`）绕过了 `m.running`，
只受调度器名额间接约束；数据库层没有构建状态的条件更新闸门，所以多实例可以对同一目标同时构建。

**影响范围。**

- 单节点影响：同一执行节点上，`docker build` 并发数 = 当前在途的不同环境构建数（HTTP 触发）
  + 被调度任务数（任务触发，上限 `max_concurrent_tasks`）。没有上界的是前者。
- 跨实例影响：同一 (节点, 环境) 可能在两个进程上同时构建，导致远程构建目录与镜像清单互相覆盖
  （`environments_build.go:200-217` 的 `Upsert` 与清单回写是"后写覆盖"），并把版本状态写成彼此矛盾的结论。
- 不影响的：构建**互斥**本身在进程内是成立的（`keyedMutex`），任务调度名额也独立成立。

**是否确实需要权重。** 不需要。缺的是"每节点允许几个构建"这种**同权计数**。仓库没有任何依据可以把
"构建 A 占 2 份、构建 B 占 1 份"说清楚：没有 CPU/内存配额、没有镜像规模记录、没有指标系统、没有配置项。
自行引入权重数字会违反任务书"不得自行引入 CPU、内存或 Docker 资源权重"的约束。

**`semaphore.Weighted` 是否足够。** 不够，而且形态不对：

1. 它是进程内计数器，多实例部署下每节点实际并发会被放大为 N 倍，与"每节点容量"的诉求相反；
2. 它的容量不能动态调整（第 3.2 节第 (9) 条），而"每节点构建上限"显然需要可配置；
3. 它没有容量/占用观测，无法在 `Status`/接口里暴露（当前的 `Status()`、`Wait()` 已对外提供在途信息）；
4. 它的严格 FIFO 会让一个大构建阻塞整台节点的所有小构建，与"按节点隔离、互不干扰"的目标冲突；
5. 它无法表达"队列满即拒绝"，而当前 `Submit` 的错误契约是同步返回 `ErrEnvironmentBuildInProgress`。

**是否需要数据库或其他跨进程机制。** 需要。要做到"每节点构建上限在部署范围内成立"，只能由数据库仲裁，
形态与仓库已有机制一致：

- 一张 `(host_id, slot)` 或计数型租约记录，用条件更新领取、带 TTL 与 owner/epoch（复刻
  `planning_session_leases` / 任务租约的既有模式）；
- 或者把"构建占用"并入任务租约体系，作为 `TaskSlotState` 的一个新状态。

同进程内的快速路径仍然是一把 `keyedMutex` 或一个带缓冲 channel 令牌（容量 = 每节点构建上限）。
**这两层各自负责一件事，都不需要加权。**

**为什么现有机制无法合理承担。** `keyedMutex` 只能表达"同一键互斥"，无法表达"同一节点上 N 个不同键
共享 N 份容量"；`m.running` 是一个无界 map，没有容量语义；任务调度器的名额统计只覆盖"占名额的任务"，
不覆盖纯 HTTP 触发的构建。因此需要**新增一个等权容量机制**，而不是把现有机制换成加权信号量。

**建议（供后续单独任务评估，本轮不实施）**：如果产品确认需要每节点构建上限，按
"配置项（例如 `scheduler` 或新的 `builds` 段）+ 进程内等权令牌（channel 或 `keyed` 计数）
+ 数据库跨进程租约"三件套落地；**不引入 `semaphore.Weighted`**。

## 9. 可能的行为变化与风险

若不顾本节以上结论而引入 `semaphore.Weighted`，可预期的行为变化与风险：

1. **`Acquire(n > size)` 永久挂起**：容量参数来自配置（`max_concurrent_tasks` 等），一旦配置变小而代码里
   权重未同步，`Acquire(context.Background(), n)` 会永久阻塞且不报错。这类故障在生产上难以定位。
2. **参数错误导致进程崩溃**：重复释放到负数、负权重（v0.22.0+）都 `panic`，与仓库"错误保留上下文、
   不使用 `panic` 处理普通错误"的约定冲突；`internal/app` 现有服务全部以 `error` 返回，没有任何一处
   依赖 `recover` 兜底。
3. **v0.17.0 的静默破坏**：构建图里选中的版本没有负权重校验，负权重会让 `cur` 变成负数/超额，
   并发上限被**静默**突破——比 `panic` 更危险，因为它不留痕迹。
4. **多实例语义退化**：`TaskScheduler` 若改为进程内信号量，多实例部署的实际并发从"数据库事实仲裁"
   退化为"N × limit"，直接削弱现有约束（这一点与 `errgroup.md` 第 6.1 节的结论一致）。
5. **观测能力丢失**：`Limit()`、`ActiveDispatches()`、`InFlight()`、`Status()` 需要另建结构维护，
   否则接口层与运维看不到容量与占用。
6. **隔离性下降**：把互不相干的容量合并到一个 `Weighted` 上，会让某一类慢操作通过 head-of-line blocking
   影响其他类别。
7. **新增直接依赖**：`go.mod` 增加 `golang.org/x/sync`，`go.mod` 的依赖集从"6 个直接依赖"扩大；
   按项目政策"新增第三方依赖必须有明确功能收益"，本次评估的功能收益为**零或负**。

## 10. 最终结论

**不引入 `golang.org/x/sync/semaphore`，也不新增任何自研信号量抽象。**

判定依据汇总：

1. 组件唯一的净能力是"**按权重**分配进程内容量"，而仓库**没有任何权重来源**（无 CPU/内存配额、
   无指标、无配置项），所有真实容量诉求都是等权计数或单一资源互斥。
2. 仓库真实的容量来源是**数据库状态机**（`domain.DecideTaskSlotState`、任务租约、规划会话租约），
   进程内计数器无法替代；用它替代会**削弱**跨进程约束。
3. 四个重点候选位置（`TaskScheduler`、`HostValidationService`、`EnvironmentBuildManager`、
   `keyedMutex` 互斥组）逐项分析后收益均为零或负。
4. SQLite 连接池已由 `database/sql` 承担，重复实现是负收益。
5. 组件自身行为与仓库约定冲突：`panic` 式参数错误、只有 `ctx.Err()` 的错误表达、无容量观测、
   容量不可调整、`n > size` 静默挂起、严格 FIFO 破坏隔离。

**发现的唯一真实缺口是环境构建容量**（第 8 节）：缺的是"每节点构建并发上限 + 跨进程闸门"，
正确形态是**等权令牌 + 数据库租约**，不是加权信号量。该缺口应作为独立任务评估，本轮不实施。

## 11. 未来重新评估的触发条件

只有同时满足以下条件时，才应重新评估加权信号量：

1. **出现真实的按权重资源**：例如产品明确"一次环境构建按镜像层数/CPU 请求数占 N 份节点构建容量"，
   或"一次 Pi 调用按模型上下文长度占 N 份并发额度"，且权重**能由现有产品配置或实测数据确定**，
   不需要自行发明数字。
2. **该资源只需在单进程内约束**：如果容量必须跨实例成立，应优先走数据库租约（与
   `planning_session_leases`、任务租约保持一致），信号量在任何情况下都不是候选。
3. **现有可能方案被证明不够用**：需要证明"等权计数 + 分级队列"无法表达该需求，例如同一资源上
   大请求与小请求必须共享一个容量池，同时又不能互相饿死——这才是 `notifyWaiters` 的
   FIFO + 反饥饿设计真正对口的场景。
4. **版本前提**：至少使用含负权重校验的版本（`v0.22.0` 及以后），并接受"参数错误即 `panic`"
   或在所有调用点先自检。
5. **观测前提**：已经另行设计好容量与占用的观测路径（组件本身不提供）。

典型信号（出现任一即触发评估）：

- 引入按节点/按租户的**分级配额**（例如"任务容器占 1 份、规划容器占 2 份、构建占 4 份"）；
- 引入**可配置的**节点级资源预算（CPU/内存单位），且需要在一个进程内按请求规模分配；
- 出现"同一资源上小请求被大请求长期挤占"的可观测故障，且等权 FIFO 队列无法解决。
