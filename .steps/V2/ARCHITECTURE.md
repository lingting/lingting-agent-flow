# V2 架构

> 状态：V2 设计基线。前置阅读：[`../V1-final.md`](../V1-final.md) §3、`docs/architecture.md`。

## 1. 分层不变

```text
cmd → cli / httpapi → app → agent / process / docker / source / storage / events → domain
```

V2 不引入第二套工作流引擎，也不新增分层。新能力一律按既有规则落位：

- 业务语义、状态迁移、准入判定、进度聚合规则 → `internal/domain`（纯函数，无 IO）。
- 生命周期、编排、事务边界、外部调用 → `internal/app`。
- 协议转换、DTO、错误码映射、SSE → `internal/httpapi`。
- 数据库结构 → `internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本，唯一的 Schema 来源，结构变化只追加新脚本。
- 展示与操作发起 → `lingting-agent-flow-ui/`，不实现工作流逻辑。

## 2. 新增领域类型（`internal/domain`）

| 类型 | 职责 | 关键规则 |
|---|---|---|
| `PlanningSession` | 一次需求规划的生命周期 | 状态迁移用 `Transition` 校验；一个会话对应一个规划容器 |
| `PlanningSessionStatus` | `active` / `analyzing` / `preview_ready` / `confirmed` / `abandoned` / `failed` | 与 Task 状态机无关，不得复用 Task 状态名 |
| `PlanningMessage` | 一轮消息（用户 / 需求分析师 / 方案规划师 / 系统） | 序号在会话内唯一 |
| `PlanningRevision` | 一轮结构化分析 + 方案 + 校验结果 | 追加写入，旧版本不被覆盖 |
| `RequirementAnalysis` | 结构化需求分析 | 字段见 [`WORKFLOW.md`](./WORKFLOW.md) §2；解析失败即规划失败 |
| `ImplementationPlan` | 结构化实施方案 + 任务草案列表 | 至少 1 个任务，校验由 domain 纯函数完成 |
| `ProjectExecutionLease` | 项目级执行权（**M5 已落地**） | `project_id` / `owner` / 单调递增 `epoch` / `expires_at`；取得、续租、释放都是纯判定，写入由存储层条件完成；旧 epoch 的迟到写入被拒绝 |
| `ProjectExecutionPlan` | 项目内执行顺序、队首分类与可领取任务（**M5 已落地**） | 按需求创建时间 → `requirement_sequence` → 任务创建时间 → 任务 ID 排序；只有队首 `queued` 任务可领取 |
| `RequirementProgress` / `ProjectProgress` | 进度聚合结果（**M5 已落地**） | 只由 Task 真实状态、执行权与停止意图计算，不接受模型输入；`current = completed` |

`Requirement` 扩展字段（`ProjectID`、`PlanningSessionID`、最终快照、`ConfirmedAt`、`DeletedAt`）已于
M4 落地；`Task.RequirementSequence` 记录需求内顺序。结构变化见 [`DATA-MODEL.md`](./DATA-MODEL.md)。


## 3. 新增应用服务（`internal/app`）

只新建真实需要的服务，不为名称完整造空壳。

| 服务 | 负责 | 明确不做 |
|---|---|---|
| `PlanningSessionService` | 会话创建、消息追加、Revision 追加、确认/放弃、状态收敛 | 不拼 Pi 命令、不创建容器 |
| `PlanningRuntimeService` | 规划容器准备与复用、只读代码快照、远端现场核验、残留准备容器清理、完整快照恢复、不完整快照废弃、释放 | 不生成方案、不写业务状态结论、不自动重放远程副作用、不自动修复现场 |
| `RequirementAnalysisService` | 调用需求分析师、解析结构化分析、落 Revision | 不创建正式 Requirement |
| `RequirementPlanningService` | 调用方案规划师、解析实施方案、结构化校验 | 不写数据库正式实体 |
| `RequirementService` | 正式需求的创建（含事务）、查询、逻辑删除、进度聚合 | 不调度、不执行 |
| `ProjectExecutionService`（**M5 已落地**） | 项目级开始/停止、项目执行权、项目内顺序与阻塞判定、项目状态与需求进度投影；对调度器提供"项目闸门"（`FilterClaims` / `Reconcile`） | 不实现第二套任务执行流程；不准备容器、不生成计划、不执行步骤；不直接启动 `TaskWorkflowService` |
| `ProjectExecutionKeeper`（**M5 已落地**） | 后台续租与收敛项目执行权（TTL 2 分钟、周期 40 秒），停止时释放没有在途任务的执行权 | 不领取任务、不启动工作流、不接触容器；续租失败只停该项目的领取 |
| `RoleConfigService` | 固定四角色读取与更新、配置快照（`app.RoleService`，V2-M1-E2/E3 已落地） | 不增删角色、不放宽安全边界；工具策略只能收敛在角色能力上限内 |

复用（不得重写）：`TaskWorkflowService`、`StepWorkflowService`、`StepExecutor`、
`ValidationService`、`TaskScheduler`、`TaskLaunchService`、`ContainerService`、
`ExecutionEnvironmentService`、`EnvironmentService`、`ProjectService`。

### 3.1 编排关系

```text
RequirementService.Confirm(planningSessionID, revisionID)   ← M4 已落地
  └─ 单个事务：requirements(1) + tasks(N, 含 requirement_sequence) + 会话 confirmed
     不创建 Step：Step 由 Planner 在任务执行期生成（GeneratePlan 职责不变）

ProjectExecutionService.Start(projectID)                    ← M5 已落地
  └─ 读项目 + 项目内正式任务（requirements.project_id），领域排序与队首判定
  └─ 拒绝：已有任务在执行 / 队首需人工处置 / 没有可执行任务（409 / 412）
  └─ 复用 domain.DecideTaskQueueAdmission 核验队首任务前置条件
  └─ 条件写入取得 project_execution_leases（epoch 递增；同一持有者重复开始幂等）
  └─ 清除停止意图（project_execution_states）并触发一次 TaskScheduler.Scan
  └─ 返回真实项目执行状态（不返回"可能已开始"）

TaskScheduler.Scan                                          ← M5 只增加闸门，不新增调度循环
  └─ 名额统计（scheduler.max_concurrent_tasks）→ queued 候选 → 既有准入与排序
  └─ ProjectExecutionGate.FilterClaims：有效执行权 + 未停止 + 项目队首
  └─ 取得任务执行权 → 既有 TaskLaunchService.Launch → 既有 TaskWorkflowService
  └─ ProjectExecutionGate.Reconcile：真实收敛后释放执行权（成功/阻塞/完成/停止）

TaskLaunchService.Launch                                    ← M5 增加一次只读核验
  └─ 带项目关联的任务：同项目已有在途任务时拒绝启动（避免绕过项目执行权）
```

### 3.2 角色工具策略执行路径（V2-M1-E3 已落地）

```text
roles.tools_policy_json（SQLite）
  └── app.RoleService.Get / 任务配置关联的角色读取
        ├── app.SessionService.Start → sessions.role_snapshot_json（快照）
        └── app.ContainerService.RunAgent
              ├── domain.EffectivePiTools(RoleType, ToolsPolicy)
              │     ├── domain.RoleToolCeiling(RoleType)       角色能力上限（系统固定）
              │     ├── domain.ToolsForPermissions(granted)    能力 → Pi 内置工具
              │     └── ToolsPolicy.AllowsTool(tool)           allow / deny（deny 优先）
              └── agent.RunRequest.Tools → pi.ContainerRunner.execute
                    ├── --tools read,grep,find,ls,…（列表为空时 --no-tools）
                    ├── --no-mcp
                    └── --no-extensions
```

- 策略计算只在 `domain`（`internal/domain/role_tools.go`）定义一次；Handler 与前端都不做权限判定，
  也不存在按调用路径分裂的第二套映射。
- `app.ContainerService.RunAgent` 是唯一把策略换算为工具列表的位置；Pi Runner 只把列表转成命令行参数，
  空列表必须落到 `--no-tools`，不得省略参数回落到 Pi 默认工具。
- 三条真实调用路径（Planner / Developer / Validator）只提交 `role.ToolsPolicy`，与 Session 快照取自
  同一个 `domain.Role` 值。
- 托管调用固定 `--no-mcp` 与 `--no-extensions`：MCP 工具与扩展注册的工具不能绕过策略。该限制只作用于
  AgentFlow 发起的 Pi 调用，不修改用户的 Pi 配置、扩展、MCP 服务器与凭据。
- 策略无法换算（未识别角色类型、非法策略）时 `RunAgent` 直接失败，不允许回退到默认权限。
- 这一层不提供文件系统隔离：规划阶段的项目代码只读仍由 §4 的只读容器实施。

## 4. 规划运行环境（只读代码）实施点

现有能力：`internal/execenv`（`Workspace{ProjectDir, SessionDir, Caches}`、`Provider`/`ExecPort`）
与 `internal/docker`（`docker create/exec`，缓存卷用 `type=volume,...` 挂载）。
**现有代码没有任何只读挂载或只读 rootfs 支持**，这是 M2 必须新增的能力。

推荐方案（A）：两阶段准备 + 只读挂载

1. 准备阶段：用一个可写的一次性准备容器把项目按**固定 commit SHA**克隆到命名卷
   `agentflow-plan-<session-id>`。
2. 规划容器：代码卷以 `type=volume,source=<snapshot>,target=/workspace/project,readonly` 挂载；
   rootfs 用 `--read-only`；`/tmp` 与会话目录用 `--tmpfs` 或独立可写卷；不使用 root 用户
   以外的提权方式；不挂 Docker Socket。
3. 释放阶段：删除规划容器；快照卷按会话复用，会话结束（confirmed / abandoned / failed）后清理。

方案 B（单容器 + 非 root 用户 + `chmod` 只读 + Prompt 约束）**不作为实现方案**：root 仍可
`chmod` 回去，不满足"执行环境上实施限制"的要求。

约束：

- 只读是最终安全边界；工具白名单与 Prompt 是额外限制，两者不可相互替代。工具策略自 V2-M1-E3 起
  在真实调用链路上生效（见 §3.2），但它不能替代 M2 的只读文件系统。
- 新增 Docker 参数（`--read-only`、`--tmpfs`、`--mount ...,readonly`、`--user`）必须同步
  `docker.RequiredCLICommands`，否则节点预检会漏检。
- 规划容器不得进入任何提交/推送/合并路径；规划容器内 Session 与任务执行 Session 是两套模型，
  规划会话不得被当作 Task 执行记录。
- 规划容器必须由 `internal/lifecycle` 管理，服务停止时取消在途调用并等待收敛。

同一 PlanningSession 多轮复用同一容器与同一代码快照；不同 PlanningSession 互相隔离
（独立容器、独立卷、独立 Session 文件）。


### 4.1 现场核验与失败恢复（V2-M2 第十四阶段已落地）

运行时的残留与不确定现场由四个显式入口处置，全部与 `RunRole` / `Prepare` / `Release` 共用同一个
`app.PlanningSessionCoordinator`，因此同一规划会话严格串行、不同会话并行、排队期间取消不产生远程副作用：

```text
InspectPlanningRuntime             只读核验：数据库绑定 + 规划容器 + 快照卷 + 一次性准备容器 + 实际 commit
CleanupPlanningSnapshotPreparation 只清准备容器：不存在幂等、运行中拒绝、退出且所有权核验通过才删
RecoverPlanningSnapshot            已完整快照：只核验 + 条件更新写入 SourceCommit，不重新 Clone / checkout
DiscardIncompletePlanningSnapshot  不完整快照：未固定 commit、未被容器引用、准备容器不存在、所有权匹配才删
Release                            显式重试：先核验再删除，两项资源都确认不存在才清空绑定
```

规则：

- 所有权判定集中在 `PlanningRuntimeService`，远程能力只通过 `app.PlanningRuntimeRemotePort` 读取事实与执行一次删除；
  生产由 `planningRuntimeRemote` 包装 `ContainerService`，仍然只有一条 SSH / Docker 调用链。
- 每份资源的核验结论分为 `present` / `absent` / `unknown` / `mismatch`：SSH、Docker、超时与解析失败只能落到 `unknown`，
  不得映射为 `absent`；标签或归属不成立是 `mismatch`，返回冲突错误并保留数据库定位信息。
- 结果不确定时立即返回并保留现场，服务端**不自动重试**远程删除、不自动重放 Clone / checkout、不自动终止运行中的准备
  容器，也不提供批量清理或全局扫描删除能力。
- 恢复有效快照与废弃不完整快照是两个独立操作，不合并为一次隐式自动修复。

### 4.2 规划容器终止策略（V2-M2 第十六阶段已落地）

取消上下文只保证本端 SSH 会话结束，不保证 Docker 容器内的 Pi 与子进程退出。规划容器的终止因此
采用显式策略，而不是从容器、Session 或角色类型推断：

```text
PlanningAgentService.RunRole
  └─ ContainerAgentRequest{PlanningSessionID, StopContainerOnUncertain: true}
       └─ agent.RunRequest{PlanningSessionID, StopContainerOnUncertain}
            └─ ContainerRunner（Started && !Certain 时）
                 └─ agent.ContainerTerminator（containerSessionExecutor）
                      └─ ContainerService.stopContainerLocked（已持容器锁与远程会话）
                           ├─ 复核本地记录 / 受管理 / Docker 容器 ID / 规划会话标签
                           ├─ docker stop（1 秒宽限期）→ docker inspect
                           └─ 记录 stopped + DockerState + 远程登记清单
```

规则：

- 触发条件是显式事实的组合：调用方启用 `StopContainerOnUncertain`、给出合法 `PlanningSessionID`、
  `ContainerExecResult.Started` 为真且 `Certain` 为假。普通任务调用不设置这些字段，任何路径都
  不会因为一次调用取消就停止可能承载其他执行的容器。
- 终止使用 `context.WithoutCancel` 派生的有界清理上下文；停止成功只由停止后的 `docker inspect`
  判定，`docker stop` 返回错误但容器确实已停止时按已核实的远端事实收敛。
- `ContainerService.RunAgent` 在执行期间已持有容器进程内锁，终止只能走持锁内部流程
  `stopContainerLocked`；公开的 `StopContainer` 会再次取同一把锁并自锁，因此禁止在临界区内调用。
- 结果通过 `agent.RunResult.RemoteTerminationConfirmed` / `RemoteTerminationError` 与
  `agent.ErrRemoteTermination` 表达：已确认停止、已停止但收尾失败、仍然不确定是三种不同事实。
- 停止不删除容器、不删除快照卷、不清空运行时绑定；容器记录为 `stopped` 时下一次 `RunRole` 由
  `PlanningAgentService` 复用 `PlanningRuntimeService.prepareHeld` 按原容器 ID 重新启动，再从本机
  最后一份完整稳定 Session 归档恢复上下文。容器已确认停止时不再从该容器导出或复制 Session。

### 4.3 停止状态对账与中断恢复（V2-M2 第十七阶段已落地）

远端容器已经停止而本地 `Container.State` 仍是 `running` 时（写库失败或 Server 在写库前中断），恢复由
同一套状态对账闭环，规则是"Docker 实际状态优先于本地过期状态"：

```text
PlanningAgentService.RunRole
  └─ observePlanningAgentContainer（节点 / 受管理 / 所有权标签 / 规划会话标签 + docker inspect）
       ├─ 远端 running                    → 直接使用
       ├─ 远端 exited / created           → prepareHeld → reusePlanningContainer → StartContainer
       └─ paused / restarting / dead /
          removing / unknown             → 前置条件错误，不自动启动

ContainerService.StartContainer（持容器锁与远程会话）
  ├─ 远端已在运行 + 本地 running   → 幂等成功，不重复提交启动命令
  ├─ 远端 exited / created         → running / unknown 先 MarkStopped + 落库 + 同步清单，再 docker start
  │                                  收敛写入失败即返回，绝不继续启动
  └─ 其他远端状态                  → 拒绝启动，不改写管理状态
```

- 允许进入启动前收敛的远端事实只有 `exited` 与 `created`；`paused` / `restarting` / `dead` / `removing`
  / `unknown` 与 inspect 失败一律拒绝自动启动，且不写 `stopped`。
- 管理状态只经 `Container.MarkStopped` / `MarkRunning` / `SyncDockerState` 推进；启动前先收敛再启动，
  因此不存在 `running → running` 的非法迁移。启动前的 Docker 容器 ID 核验同样必须通过。
- 收敛顺序固定为"远端已核实 → 管理状态落库 → 远程登记清单收敛 → 启动"；落库失败时不启动，清单失败
  时保留已落库的 `stopped` 并返回清单错误，下一次调用按真实状态继续收敛。
- `RunAgent` 触发过终止流程后，用 `context.WithoutCancel` 派生的 5 秒上限上下文读回本地记录（只读
  Store、不重连远程）：停止成功返回 `stopped`，无法确认返回已落库的 `unknown`；读取失败保留最后一份
  可用记录并由 `ContainerAgentResult.ContainerStateReloadError` 说明，不覆盖原始 Pi 取消 / 超时结论。
- 恢复不创建第二个容器、不改写 `HostID` / `ContainerID` / `SourceCommit`、不重置角色 Session 标识、
  不删除或重新 Clone 快照卷，最后一份完整稳定 Session 归档不被中断调用覆盖。


### 4.4 Docker 容器状态语义统一（V2-M2 第十八阶段已落地）

容器"能否执行 / 能否自动启动 / 是否已经停止"不允许由 `docker inspect` 的 `State.Running` 单独决定：Docker
Engine 中 `paused` 与 `restarting` 容器同样报告 `Running=true`，而 `running` / `exited` / `created` 与
`Running` 不一致就是观测不可信。判定因此收敛到 `internal/domain` 的一处纯函数：

```text
docker.ContainerInspect{State, Running}
  └─ domain.ClassifyDockerContainerCondition(status, running)
       ├─ IsActivelyRunning   running + Running=true            只有它允许 docker exec / Pi 调用
       ├─ IsConfirmedExited   exited + Running=false            已确认退出的唯一口径
       ├─ ConfirmsNotRunning  exited / created 且 Running=false  停止完成与残留收敛的肯定结论
       └─ IsStartable         同上                               唯一的自动启动集合
paused / restarting / dead / removing / 未知取值 / 字段矛盾 → 不可执行、不可自动启动、不可折叠成 stopped
```

规则：

- 业务层不得再直接比较 `Running`：`docker.ContainerInspect` 提供 `RuntimeCondition()`、
  `IsActivelyRunning()`、`IsConfirmedExited()`、`IsStartable()`，所有生产调用方（容器创建后核验、
  `StartContainer`、`Exec`、`RunAgent`、`sessionContainerGuard`、规划运行时复用与规划 Agent 前置核验、
  执行环境与预检、代码初始化、步骤执行、任务启动与工作流、验证、续跑资格、残留观测与恢复）统一使用它们。
- 状态矛盾（`running` + `Running=false`、`exited` + `Running=true`、`created` + `Running=true`）失败关闭：
  既不当作正常运行，也不当作正常退出，不产生任何肯定性观测结论。
- `stopContainerLocked` 用 `ConfirmsNotRunning` 判断"已经停止"，否则照常提交 `docker stop` 并以停止后的实际
  `inspect` 判定；暂停、重启中、dead、removing 与矛盾现场不写 `stopped`，也不伪造停止成功。
- 残留观测新增 `ContainerObservedIndeterminate`：它既不是 `running` 也不是 `stopped`，
  `DecideContainerRecovery` 对它只返回"保持现状 + 需要人工"，不自动启动、删除或接管。
- 测试替身必须同时给出 `State.Status` 与 `State.Running`（`tests/internal/testkit.ContainerInspectFor`），
  不再用"状态等于 running"推导 `Running`；暂停、重启中与字段矛盾的现场直接写字面量构造。


### 4.5 真实环境验收与两处生产缺陷修复（V2-M2 第十九阶段已落地）

真实环境验收（真实 SSH / 远程 Docker / 只读规划容器 / 容器内 Pi）暴露了两处只在真实依赖下成立的缺陷，
修复仍留在原有调用链内，没有新增第二套容器、Session 或 Pi 执行路径：

- **只读容器的 Session 进出容器**：`docker cp` 只解析容器 rootfs，看不到挂在 tmpfs 上的路径，而规划容器
  `--read-only` 的唯一可写目录就是 `/tmp`（tmpfs）。`containerSessionExecutor.CopyFromContainer` 与
  `CopyToContainer` 因此在 `docker cp` 失败时退回宿主机重定向：`docker exec cat <容器路径> > <宿主机路径>`
  与 `docker exec -i sh -c 'cat > <容器路径>' < <宿主机路径>`。数据只经过远程宿主机，不经过本进程；命令
  仍由 `sshclient.ShellCommand` / `ShellQuote` 构造。普通任务容器 `docker cp` 成功时行为不变。
- **SSH 中断后的远端终止**：`TerminateContainer` 原先复用本次调用的 SSH 会话停止容器，连接中断后该会话已
  失效，停止与核验都无从执行。现在本次会话无法确认终止时改用 `ContainerService.
  terminateWithIndependentSession`：新连接 → 重新读取本地容器记录 → 核验执行节点、Docker 容器 ID、管理
  标签与规划会话标签 → 只停止这一个容器 → 以停止后的真实 `inspect` 判定。两条路径都未确认时返回错误，
  不写 `stopped`，容器记录 / 快照卷 / 规划会话绑定全部保留。普通 Task 调用不启用终止策略，不进入该路径。
- **测试入口超时**：`.github/scripts/run-real-tests.sh` 增加 `--timeout`（默认 `90m`）并传给 `go test`，
  使真实套件自带的运行预算（例如 remote/workflow 的 60 分钟）能够成立；超时仍按失败上报。
- 真实验收只使用生产装配：`ContainerService` / `PlanningRuntimeService` / `PlanningAgentService` /
  `PlanningSessionCoordinator` / 当前 `storage.Store` / `RoleService`；测试侧只注入模型配置（写入规划容器
  tmpfs 内的 `PI_CODING_AGENT_DIR`）与真实 SSH 连接的包装器（只改变连接生命周期）。
## 5. HTTP API 方向

仅在既有路由空间内新增（`/api/v1/...`），Handler 只做协议转换。

```text
POST   /api/v1/projects/{projectID}/planning-sessions
GET    /api/v1/planning-sessions/{sessionID}
POST   /api/v1/planning-sessions/{sessionID}/messages
POST   /api/v1/planning/sessions/{sessionID}/confirm          ← M4 已落地
POST   /api/v1/planning/sessions/{sessionID}/abandon

GET    /api/v1/projects/{projectID}/requirements              ← M4 已落地
GET    /api/v1/requirements/{requirementID}                   ← M4 已落地
GET    /api/v1/requirements/{requirementID}/progress           ← M5 已落地
DELETE /api/v1/requirements/{requirementID}                    ← M6

GET    /api/v1/projects/{projectID}/tasks                      ← M5 已落地
GET    /api/v1/projects/{projectID}/execution                  ← M5 已落地
POST   /api/v1/projects/{projectID}/start                      ← M5 已落地
POST   /api/v1/projects/{projectID}/cancel                     ← M5 已落地

GET    /api/v1/roles
GET    /api/v1/roles/{roleID}
PUT    /api/v1/roles/{roleID}
```

规则：

- 错误映射沿用现有约定（400 参数 / 404 不存在或归属不符 / 409 状态冲突 / 412 前置条件缺失 /
  500 执行发生但未成功 / 503 服务未装配或已停止）。
- 响应必须区分"规划草案"与"正式 Task"，草案不得带正式 Task ID。
- 确认接口只接受 `revision_id` 与可选 `host_id`：需求与任务由服务端依据已采纳的 Revision 计算，
  响应返回真实需求与按 `requirement_sequence` 排列的任务，不接受任务列表、顺序或任务字段覆盖。
- 请求不接受 Session 路径、工作目录、命令行参数、步骤列表或角色覆盖。
- 项目执行接口不接受请求体：任务状态、执行权、`owner`、`epoch`、任务顺序与执行结果全部由服务端
  依据持久化事实计算，也不提供"提前启动某个后续任务"或"跳过前序任务"的入口（M5 已落地）。

## 6. 前端结构

导航：概览 / 需求工作台 / 需求 / 任务 / 项目 / 机器 / 设置（环境管理在设置内，无独立环境入口）。

- 需求工作台（`/planning`）是规划阶段的独立页面（项目选择 → 对话 → 预览 → 确认/放弃），不用
  「新增需求」弹窗；未选择 Project 时禁止发送消息；切换 Project 必须丢弃当前会话上下文。
- 需求页（`/requirements`，M5 已落地）按项目浏览正式需求，展示进度统计（已完成 / 执行中 / 待执行 /
  失败 / 待核验 / 已取消 / 已暂停）、当前与下一个任务、阻塞原因与阻塞任务，以及按需求内顺序排列的
  任务；任务可深链接到任务页（`/tasks?task_id=`）。只读：不提供调整顺序、提前启动或删除入口。
- 项目页增加项目执行面板（M5 已落地）：执行状态、进度统计、开始/停止执行、执行权与当前/下一个任务、
  按项目顺序排列的任务列表与阻塞原因；列表顺序由服务端固定，前端不重排。
- 设置页承载环境管理与角色管理（四角色不可增删，只可改 Prompt / 工具策略 / 模型）。
- 状态与进度只来自接口真实字段；未知取值原样只读展示；开始/停止失败后重新读取服务端状态，
  网络中断只提示"结果未知"，不自动重复提交有副作用的请求；禁用操作必须给出原因。
- 项目执行状态未提供全局并发的 HTTP 出口：页面不展示全局并发上限或在跑任务数，避免用推断值冒充事实。
- 所有请求经 `src/lib/api/` 统一客户端；页面不直接 `fetch`。

## 7. 实施依赖与顺序

| 里程碑 | 依赖 | 关键交付 |
|---|---|---|
| M1 | 无（V1 已就绪） | Settings、环境管理 API、固定四角色、执行层工具限制 |
| M2 | M1（角色配置快照） | 只读规划容器、PlanningSession 持久化、多轮 Session |
| M3 | M2 | 需求工作台、分析/方案两个角色、Revision 预览 |
| M4 | M3 | 方案校验、事务化生成 Requirement + Task |
| M5 | M4 | ProjectExecutionLease（`project_execution_leases`）、项目闸门与同项目串行、`Cancel` 语义、需求/项目进度聚合（已完成） |
| M6 | M5 | 停止/删除流程、端到端验收 |

## 8. 已知代码缺口（M0 核实）

| 编号 | 缺口 | 证据 | 归属 |
|---|---|---|---|
| G1 | ~~角色工具策略未在调用层实施~~ **已关闭**（V2-M1-E3） | 现状：`internal/domain/role_tools.go` 计算能力上限与工具列表，`app.ContainerService.RunAgent` 统一换算，`internal/agent/pi/command.go` 构造 `--tools`/`--no-tools` 并固定 `--no-mcp`/`--no-extensions`；Planner / Developer / Validator 三条路径均提交 `role.ToolsPolicy` | M1 |
| G2 | ~~无角色更新 API~~ **已关闭**（V2-M1-E2 / E3） | 现状：`internal/httpapi/roles.go` 提供角色列表、详情与 `PUT /api/v1/roles/{roleID}`；E3 起 `tools_policy` 可更新，越出角色能力上限返回 400 | M1 |
| G3 | ~~环境管理只有查询 API，无创建/修改/构建 HTTP 入口~~ **已关闭**（V2-M1-E1） | 现状：`internal/httpapi/environments.go` 提供环境详情、创建版本、初始化、后台构建与构建状态/日志/Dockerfile 读取入口，写路径全部复用 `app.EnvironmentService` | M1 |
| G4 | `execenv` 无只读挂载与只读 rootfs 能力 | `internal/execenv/workspace.go`、`internal/docker/container.go` | M2 |
| G5 | `Requirement` 无项目关联、无规划快照、无逻辑删除 | `domain/requirement.go`、迁移脚本的 `requirements` 表 | M3/M4 |
| G6 | ~~无 PlanningSession / Message / Revision 持久化~~ **已交付**（M2 第二阶段） | `domain/planning_*.go`、初始迁移的三张规划表 | M2/M3 |
| G7 | ~~无项目级执行租约，`TaskScheduler` 只有全局并发~~ **已关闭**（M5） | 现状：`internal/domain/project_execution.go` 定义 `ProjectExecutionLease` 与取得/续租/释放判定，迁移 `00004_project_execution_leases.sql` 提供 `project_execution_leases`（`project_id` 主键、`owner`、单调 `epoch`、`expires_at`）与 `project_execution_states`，`app.ProjectExecutionService` 负责开始/停止与闸门，`TaskScheduler` 只增加 `ProjectGate` 过滤器；本仓库中没有 `task_leases` 表，任务级执行权仍是进程内协调器 | M5 |
| G8 | ~~无项目/需求聚合查询~~ **已关闭**（M5） | 现状：`GET /api/v1/projects/{projectID}/tasks`、`/execution` 与 `GET /api/v1/requirements/{requirementID}/progress` 由 `app.ProjectExecutionService` 与 `app.RequirementService.Progress` 提供，聚合规则唯一实现在 `domain.DecideProjectExecutionPlan` / `DecideRequirementProgress` / `DecideProjectExecution` | M5 |
| G9 | ~~取消上下文不能证明远端 Pi 进程已终止~~ **代码已关闭**（V2-M2 第十六阶段） | 现状：`PlanningAgentService.RunRole` 显式设置 `PlanningSessionID` 与 `StopContainerOnUncertain`，`ContainerRunner` 在 `Started && !Certain` 时经 `agent.ContainerTerminator` 调用 `ContainerService.stopContainerLocked`（复用已持有的容器锁与远程会话），以停止后的 `docker inspect` 判定终止并写入 `RemoteTerminationConfirmed` / `RemoteTerminationError`；真实 SSH / Docker 环境下的终止仍未验收，见里程碑 §7.10 / §7.16 | M2 |
| G10 | ~~远端容器已停止但本地仍为 `running` 时恢复不闭环~~ **代码已关闭**（V2-M2 第十七阶段） | 现状：`ContainerService.StartContainer` 在启动前 inspect 远端事实，本地 `running` / `unknown` 遇远端 `exited` / `created` 时先经领域状态机收敛为 `stopped` 并落库、同步登记清单后才执行 `docker start`，写入失败即返回；`PlanningAgentService.RunRole` 每次先核验远端状态再决定复用或恢复，`paused` / `restarting` / `dead` / `removing` / `unknown` 不自动启动；`RunAgent` 用 `context.WithoutCancel` 派生的有界上下文读回终止后的记录并以 `ContainerStateReloadError` 报告本地读取失败。真实环境下的收敛与 Server 崩溃恢复仍未验收，见里程碑 §7.17 | M2 |
| G11 | ~~容器状态判定依赖 `State.Running`，`paused` / `restarting` 被当成正常运行~~ **代码已关闭**（V2-M2 第十八阶段） | 现状：`domain.ClassifyDockerContainerCondition` 以 `State.Status` + `State.Running` 统一判定，`docker.ContainerInspect` 暴露 `RuntimeCondition` / `IsActivelyRunning` / `IsConfirmedExited` / `IsStartable`；`paused` / `restarting` 与字段矛盾在 `StartContainer`、`Exec`、`RunAgent`、`sessionContainerGuard`、规划运行时复用、规划 Agent 前置核验、`stopContainerLocked` 与容器残留观测中一律不进入执行、自动启动或 `stopped` 收敛，残留观测新增 `ContainerObservedIndeterminate`。真实 Docker 下的暂停 / 重启现场仍未验收，见里程碑 §7.18 | M2 |

缺口状态说明：标记"已关闭"的条目在对应子任务完成后按当时的代码复核更新，未标记的条目仍按归属
里程碑待实现；关闭单条缺口不代表里程碑退出条件已满足。
