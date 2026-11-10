# AgentFlow V2 最终产品状态

> **文档状态**：V2 收口文档。
> **核实基线**：commit `e98a8a1`（2026-11-10）工作区的实际代码、`internal/storage/sqlite/migrations/`
> 迁移脚本、`internal/httpapi/routes.go` 路由注册与 `tests/` 结构。
> **使用规则**：本文只记录最终状态，不记录逐步开发过程。V2 过程文档（设计基线与 M0–M6 里程碑文件）
> 已按收口决策删除，历史可从 Git 历史追溯。凡写"已交付"的能力，都能在代码中指向具体包、路由或表；
> 凡证据不足或未复核的，一律列入 §11 遗留事项，不写成已完成。

---

## 1. 产品定位

V2 不重写 V1 的执行流程，而是把用户发起工作的入口从「创建一个任务」升级为「提出一项需求」：

```text
选择项目 → 对话提出需求 → AI 分析项目现状 → AI 生成实施方案并拆分为多个任务
→ 多轮对话调整 → 用户确认 → 原子创建 1 个正式 Requirement + N 个正式 Task
→ 用户手动启动 → 复用 V1 工作流执行 → 查看项目与需求进度
```

两个阶段边界固定，不允许互相侵入：

| 阶段 | 对象 | 产物 | 是否创建正式业务实体 |
|---|---|---|---|
| 规划 | `PlanningSession` | 消息历史、需求分析、实施方案、Revision（草案） | 否 |
| 执行 | `Requirement` + `Task` | 正式需求、正式任务、步骤与执行记录 | 是（仅在确认时） |

生成任务**不会**启动执行；执行阶段**不会**重新生成方案。用户可见入口仍是两个：`agentflow server`
（唯一完整服务）与 `agentflow` CLI（其中任务相关命令只是服务端 HTTP 的客户端）。

---

## 2. 已交付能力

### 2.1 设置中心与环境管理

- 设置页（`src/pages/settings/`）承载三块内容：环境管理、固定角色管理、基础配置只读查看。独立的
  环境页面与导航入口已移除，环境管理作为设置中心的子结构存在，没有第二套环境服务。
- 环境写入口（路由见 `internal/httpapi/environments.go`）：初始化工作空间、追加版本、受理后台构建、
  读取 Dockerfile 与构建日志、读取构建结论。写路径全部复用 `app.EnvironmentService`，Handler 只做协议转换。
- 环境版本**只追加**，历史版本与构建元数据不可覆盖；内容与某个已成功构建版本完全一致时由
  `domain.FindReusableVersion` 复用该版本号，不产生重复版本。
- 构建是有副作用的长操作：状态为 `pending` / `building` / `succeeded` / `failed` / `uncertain`；
  同一节点同一环境同时只允许一个构建，重复提交返回 `409`；不提供单独取消接口，取消只发生在服务停止时；
  服务重启时把遗留 `building` 收敛为 `uncertain`（既不报成功也不伪造失败）。后台执行由
  `app.EnvironmentBuildManager`（`lifecycle.Service`）承担。
- 基础配置只读（`GET /api/v1/settings/config`）：返回服务启动时实际生效的四项运行参数（数据库路径、
  最终监听地址、调度并发、Session 根目录），请求期间不重新读取配置文件，不提供编辑、保存、热更新
  与重启入口，也不返回 SSH 路径、凭据或完整配置对象。
- 前端只使用服务端返回的 `usable` / `usable_for_remote_task`，不自行由版本号或镜像引用推断可用性；
  `202`（已受理）不显示为构建成功。

### 2.2 固定四角色与工具策略

- 角色身份固定四个，系统常量定义在 `internal/domain/role.go`，`app.RoleService` 拒绝创建与删除：

| 固定 ID | 名称 | 职责 | 能力上限 | 默认策略 |
|---|---|---|---|---|
| `requirement_analyst` | 需求分析师 | 分析需求与项目现状，产出结构化需求分析 | read | read |
| `planner` | 方案规划师 | 产出项目级实施方案与任务拆分；同时承担 V1 任务内部计划生成 | read | read |
| `developer` | 开发者 | 执行任务的开发步骤 | read / write / execute | 三者全开 |
| `validator` | 验收者 | 确定性验证后的语义审查 | read | read |

- 能力到 Pi 内置工具的映射只在 `internal/domain/role_tools.go` 定义一次：read → `read`/`grep`/`find`/`ls`，
  write → `edit`/`write`，execute → `bash`。`bash` 可执行写文件命令，因此规划角色与验收者都不具备
  execute 能力上限。
- 用户可改：`system_prompt`、`tools_policy_json`（`permissions` / `allow_tools` / `deny_tools`）、`model`、
  `enabled`。不可改：固定 ID、角色数量、`type`、`runner`、系统安全边界。
- 三个接口：`GET /api/v1/roles`、`GET /api/v1/roles/{roleID}`、`PUT /api/v1/roles/{roleID}`。越出能力上限的
  `permissions`、上限外的 `allow_tools`、未知工具名一律 `400` 且不落库。详情响应额外返回服务端计算的
  `capability_ceiling` / `available_tools` / `effective_tools`，前端不重复实现权限判定。
- 策略在真实调用链路上生效（V1 遗留缺口已关闭）：`app.ContainerService.RunAgent` 是唯一把角色类型与
  策略换算成工具列表的位置，`internal/agent/pi/command.go` 构造 `--tools <list>`，列表为空时使用
  `--no-tools`（省略参数会回落到 Pi 默认工具，属越权回退，禁止）。托管调用固定附加 `--no-mcp` 与
  `--no-extensions`，MCP 工具与扩展注册的工具不能绕过策略；该限制只作用于 AgentFlow 发起的 Pi 调用。
- 策略无法换算（未识别角色类型、非法策略）时 `RunAgent` 直接失败，不回退默认权限。
- 每次调用使用**当次读取的配置快照**，`sessions.role_snapshot_json` 保存会话当时的角色快照；配置修改
  只影响后续新会话，不改变已运行或已完成的会话。

### 2.3 规划运行环境（只读代码 + 稳定多轮会话）

规划阶段的"项目代码只读"是**执行环境上的限制**，不是 Prompt 约定，也不是工具白名单的替代品。

- `app.PlanningRuntimeService`：两阶段准备。先用一次性准备容器把项目按**固定 commit SHA** 克隆到命名卷
  `agentflow-plan-<session-id>`，再创建规划容器：代码卷以 `type=volume,...,readonly` 挂载到
  `/workspace/project`、rootfs 使用 `--read-only`、`/tmp` 与会话目录用 `--tmpfs`、不挂 Docker Socket、
  不使用提权。新增的 Docker 参数已同步 `docker.RequiredCLICommands`，节点预检不会漏检。
- 同一 `PlanningSession` 复用同一容器与同一代码快照；不同会话使用独立容器、独立卷、独立 Session 文件。
- 两个规划角色各自维护稳定 Pi Session：角色会话标识成对条件绑定，容器内使用固定 Session 路径，本机
  持久目录保存归档；每轮调用前从本机最后一份完整归档恢复历史 JSONL 并按条件更新可恢复快照，因此
  "容器停止后重启"与"服务重建"都能继续上下文。
- 只读容器内的 Session 出入容器由 `docker exec` + 宿主机重定向完成（`docker cp` 只解析 rootfs，看不到
  tmpfs），命令仍由 `sshclient.ShellCommand` / `ShellQuote` 构造；普通任务容器 `docker cp` 成功时行为不变。
- 现场核验与失败恢复有五个显式入口，全部与 `Prepare` / `RunRole` / `Release` 共用同一个
  `app.PlanningSessionCoordinator`（同一会话严格串行、不同会话并行、排队期间取消不产生远程副作用）：

| 入口 | 职责 |
|---|---|
| `InspectPlanningRuntime` | 只读核验数据库绑定、规划容器、快照卷、准备容器与远端实际 commit |
| `CleanupPlanningSnapshotPreparation` | 只删已退出且所有权核验通过的准备容器；不存在时幂等，运行中拒绝 |
| `RecoverPlanningSnapshot` | 只核验并条件写入已完整存在但未落库的快照，不重新 Clone / checkout |
| `DiscardIncompletePlanningSnapshot` | 仅限未固定 commit、未被容器引用、准备容器已不存在且所有权匹配的快照卷 |
| `Release` | 先核验远端状态再删除，两项资源都确认不存在才清空绑定；结果不确定时保留定位信息且不自动重试 |

- 每份资源的核验结论分为 `present` / `absent` / `unknown` / `mismatch`：SSH、Docker、超时与解析失败只能
  落到 `unknown`，不得映射为 `absent`；标签或归属不成立是 `mismatch`，返回冲突并保留数据库定位信息。
  生产侧只经 `app.PlanningRuntimeRemotePort` 读取事实与执行一次删除，仍然只有一条 SSH / Docker 调用链。
- 规划容器终止采用显式策略：调用方启用 `StopContainerOnUncertain`、给出合法 `PlanningSessionID` 且
  `Started && !Certain` 时，经 `agent.ContainerTerminator` 走持锁内部流程 `stopContainerLocked` 停止本会话
  独占的容器，以停止后的真实 `docker inspect` 判定终止；本次 SSH 会话无法确认时改用独立连接重新读取
  本地记录并核验节点 / 容器 ID / 标签后再停止。已确认停止、已停止但收尾失败、仍然不确定是三种不同事实，
  都如实上报；停止不删除容器、不删除快照卷、不清空绑定，下一次调用按原容器 ID 重启。
- 容器状态判定收敛到 `domain.ClassifyDockerContainerCondition(Status, Running)`：`paused` 与 `restarting`
  同样报告 `Running=true`，因此只由 `Status` 决定不再成立。`IsActivelyRunning` 是唯一允许 `docker exec` /
  Pi 调用的条件，`ConfirmsNotRunning` 是停止完成与残留收敛的肯定结论，`IsStartable` 是唯一的自动启动集合；
  `dead` / `removing` / 未知取值 / 字段矛盾一律不可执行、不可自动启动、不可折叠成 `stopped`。残留观测新增
  `ContainerObservedIndeterminate`，只返回"保持现状 + 需要人工"。
- 规划会话的互斥不是数据库事实：同一数据目录只允许一个 Server 进程，`Prepare` / `RunRole` / `Release`
  由进程内 `app.PlanningSessionCoordinator` 串行化，规划会话**没有**租约表、owner、epoch 与 TTL。
  （M5 的 `project_execution_leases` 属于项目级执行协调，与规划会话互斥无关，两者互不替代。）

### 2.4 需求分析与实施方案

- 领域契约：`domain.RequirementAnalysis`（摘要、目标、项目现状、范围、非目标、业务 / 技术 / 兼容性约束、
  影响范围、风险、待确认事项、验收标准）与 `domain.ImplementationPlan`（摘要、架构调整、涉及模块、任务
  草案数组、执行顺序）。任务草案至少 1 个，顺序从 1 连续唯一，依赖只允许指向更早的任务。
- 单一严格解析入口 `ParseRequirementAnalysis` / `ParseImplementationPlan`：拒绝空输出、非法 JSON、未知
  字段、尾随说明、第二份 JSON 文档、`null`、非对象与非法字段值；必填字段存在性、`null` 与重复 Key
  （含嵌套任务对象）在结构化解码之前判定。解析失败即本轮规划失败，不用自然语言兜底写库。
- `PlanningRevision` 带 `snapshot_version`（迁移 `00002_planning_analysis_claims.sql`），持久化后可由
  `CheckSnapshotVersion` 独立读取解析；Revision 追加写入，旧版本不被覆盖。
- `app.PlanningAnalysisService` 两阶段编排：数据库级占用抢占（`analysis_claim_id` 条件 UPDATE，不依赖
  进程内锁）→ 会话状态校验 → 只读项目上下文 → 分析师调用 → 规划师调用（输入为分析师完整快照，不得覆盖
  约束与非目标）→ 两阶段各自结构化校验 → 成功后单个事务写入 Revision 与两个角色消息并推进
  `preview_ready`。模型调用全程在事务之外。失败时用独立事务把本轮记为 `failed`（领域迁移 + 脱敏摘要 +
  释放占用），不产生半成品 Revision。
- 可靠性守点：持久化事务失败整体回滚并返回 `ErrPlanningAnalysisNotPersisted`（失败记录写不成时不虚报
  失败）；显式重试在同一事务内完成状态推进、执行权取得与可选补充消息，无占用的 `analyzing` 可重试且
  重复请求不重复追加消息；`app.PlanningAnalysisRecoveryService` 在取得进程锁、打开数据库之后、HTTP 接收
  请求之前收敛"`analyzing` 且仍有占用"的遗留会话（占用已换人则回滚，不重建容器、不引入租约）。
- 前端 `/planning` 需求工作台：项目选择 → 新建 / 恢复会话 → 多轮消息 → 显式两阶段分析 → 状态与失败摘要
  → Revision 预览与历史版本切换。状态全部取自服务端，未知取值原样只读展示；执行节点优先会话绑定值、
  其次项目默认节点，都没有时必须显式选择且绑定后不可改指；任务草案明确标注不是正式 Task。

### 2.5 方案确认与原子任务生成

- `ImplementationPlan.ConfirmableTasks` 在既有结构化校验之上补充"顺序连续唯一"，顺序由任务数组下标 +1
  推导（不为可确定推导的值改造已落库的 JSON 契约）。需求标题取最终需求分析摘要，描述是"摘要 + 目标 +
  范围 + 约束 + 验收标准"的确定性摘要；`NewConfirmedRequirement` 记录项目、规划会话、两份快照与确认时间；
  `Task.RequirementSequence` 只由确认流程写入。
- 迁移 `00003_requirement_confirmation.sql`：`requirements` 追加 `project_id`、`planning_session_id`、
  `final_analysis_json`、`final_plan_json`、`confirmed_at`、`deleted_at` 与两个索引；`tasks` 追加
  `requirement_sequence` 与部分唯一索引 `(requirement_id, requirement_sequence) WHERE ... IS NOT NULL`。
  不新增外键、枚举白名单、触发器与数据回填。
- `app.RequirementService.Confirm` 在单个 `storage.WithTx` 内完成：事务内重新核验会话状态（必须
  `preview_ready`）→ 核验 Revision 归属与快照版本 → 校验项目与会话一致 → 解析执行节点与任务角色 →
  逐个构造并校验任务配置快照 → 创建 1 个需求与 N 个任务 → 以 `preview_ready` 为前置条件推进会话。失败整体
  回滚，并发确认只有一个成功、其余返回冲突。
- 配置继承：仓库、分支、环境、版本、缓存、工作目录取自同一份项目快照；执行节点按"显式给出 > 项目默认"
  解析，两者都没有时直接失败，不回退本机执行，也不读规划会话绑定的规划容器节点。
- **确认阶段不创建 Step**：V1 约定是任务进入执行流程后由 Planner 在任务容器内生成计划，`GeneratePlan`
  职责不变，不在两处生成同一组步骤。确认后任务为 `queued`，不调用 `TaskLaunchService`、不进入 Scheduler、
  不创建 Step / Attempt / Session / WorkflowRun / 容器。
- 前端确认闭环：Revision 预览区的确认摘要 + 弹窗二次确认、在途禁用、失败后重新读取服务端事实、
  `409` / `412` / 网络中断分别如实展示；成功后展示真实需求与任务标识及 `queued` 状态，不提供开始执行入口；
  已确认会话只读展示采纳结果。

### 2.6 项目级调度与进度

- 迁移 `00004_project_execution_leases.sql`：
  - `project_execution_leases`：`project_id` 主键（每项目最多一份执行权）、`owner`、单调递增 `epoch`、
    `expires_at`、`acquired_at`、`updated_at`，另有按 `owner` 的索引。取得只允许"没有登记"或"已到期"并
    原子递增 `epoch`；续租 / 释放必须带 `owner + epoch` 条件，0 行按冲突处理；释放只把 `expires_at` 置为
    当前时间，**不删行、不重置 epoch**，旧持有者的迟到写入被拒绝。
  - `project_execution_states`：`project_id` 主键，保存 `state`（无白名单，最后一次决策的投影记录）与
    可空 `stop_requested_at`（用户已请求停止领取的事实）。
  - 两者都不声明外键、不使用枚举白名单、不做数据回填；项目或任务被删除后孤立行允许保留。
  - 本表不用于多实例仲裁，只用于跨请求、跨重启识别执行权归属；单 Server 进程锁仍是唯一的多进程边界。
- 领域纯函数：`ProjectExecutionLease` 的取得 / 续租 / 释放判定；`DecideProjectExecutionPlan`（排序为
  需求创建时间 → `requirement_sequence` → 任务创建时间 → 任务 ID，只有队首 `queued` 可领取，失败 / 待恢复 /
  取消 / 暂停 / 现场未收敛一律阻塞后续任务）；`DecideRequirementProgress` / `DecideProjectExecution`
  （`current = completed`，`cancelled` 不并入 `failed`）。
- `app.ProjectExecutionService`：项目开始 / 停止 / 状态 / 任务列表，并向调度器提供项目闸门
  （`FilterClaims` / `Reconcile`）。`app.ProjectExecutionKeeper` 后台续租与收敛，TTL 2 分钟、周期 40 秒，
  停止时释放没有在途任务的执行权；续租失败只停该项目的领取。
- `TaskScheduler` 只增加 `ProjectGate` 钩子：唯一分派循环不变，全局并发上限
  （`scheduler.max_concurrent_tasks`）不变。`TaskLaunchService.Launch` 增加一次只读的同项目串行核验，
  避免绕过项目执行权并发执行两个任务。
- 项目级开始是显式动作：已有任务在执行 / 队首需人工处置 / 没有可执行任务一律拒绝（`409` / `412`）；
  同一持有者重复开始是幂等的；返回真实项目执行状态而非"可能已开始"。收敛后自动连续领取下一个任务，
  失败 / 待恢复 / 取消即停，不提供"跳过前序任务"或"提前启动后续任务"的入口。
- 项目停止语义固定：先落停止意图（下一次扫描不再领取）→ 有在途任务时调用**既有**任务取消入口，只推进到
  `cancelling`，不直接改写任务状态 → 真实收敛到终态后才释放执行权并记录 `paused` → 结果不确定时保留真实
  状态，不伪造 `paused`；取消不删除任务、不删除执行记录、不回滚 Git、不影响其他项目。重复停止幂等。
- 进度语义：需求进度是**任务完成数量比**，不是语义完成度；规则唯一实现在 `domain`，输入只有关联 Task
  的状态与 `requirement_sequence`。`completed` 要求存在任务且全部 `completed`；存在 `failed` /
  `recovery_required` / `cancelled` / `paused` 时不判完成，阻塞任务取按执行顺序第一个阻止完成判定的任务；
  `cancelled` 单独计数、不并入 `failed`。项目状态是只读投影：`running` > `paused` > `blocked` > `idle`。
- 前端：项目页增加项目执行面板（执行状态、进度统计、开始 / 停止、执行权与当前 / 下一个任务、按项目顺序
  排列的任务列表与阻塞原因）；新增需求页 `/requirements`（支持 `?requirement_id=` 深链接）与任务页
  `?task_id=` 深链接。列表顺序由服务端固定，前端不重排；页面不展示全局并发上限或在跑任务数，避免用推断值
  冒充事实。

### 2.7 停止任务与需求删除

- 停止与删除是两个显式动作，`DELETE` **不隐式取消任务**：存在未收敛任务或执行现场不确定时返回 `409`，
  停止任务仍走既有 `POST /api/v1/tasks/{taskID}/cancel`。
- 领域：`RequirementDeletionTaskStatuses`（`queued` / `completed` / `failed` / `cancelled`，未知失败关闭）与
  `Task.AllowsRequirementDeletion` 是**需求聚合删除专用**白名单，单任务删除接口的状态集合与
  `Task.MarkDeleted` 语义未改动；`Requirement.MarkDeleted` 只写 `deleted_at` 与 `updated_at`，重复删除冲突。
- 存储：`DeleteRequirement` 为条件 UPDATE（`deleted_at IS NULL`），0 行按 `storage.ErrConflict`；任务软删除
  复用既有 `DeleteTask`，由应用层传入需求级状态集合。不新增表、列与迁移。
- 应用：`RequirementService.Delete` 缺少协调器时失败关闭（`ErrRequirementDeleteUnavailable`）；删除前按任务
  标识升序取得全部关联任务的执行权（与 Scheduler / TaskLaunch / 步骤执行同一个 `TaskExecutionCoordinator`），
  任一占用返回 `409` 并释放已取得的执行权；单事务内重读需求与任务、核验任务集合未变化、核验状态与执行现场
  （活动 WorkflowRun、未收敛步骤、结果不明的代码保存副作用）→ 逐个条件软删除任务 → 条件软删除需求 →
  追加 `requirement.deleted` 事件（每个关联任务一条）→ 提交后释放全部执行权。任一步失败整体回滚。
- 执行历史完整保留：Step / Attempt / Session / Event / CodeSave / 容器记录不物理删除，任务原状态、最终结论
  与取消记录不被改写；Git 不回滚，容器不被自动处置（仍走显式 `delete` / `detach`）。
- 两处在真实环境暴露并已修复的缺陷（属于 V2 收口范围）：
  1. 启动阶段失败后任务永久停在 `cancelling`：新增 `TaskLaunchService.SettleCancelledLaunch`，按
     `domain.DecideCancellationRecovery` 收敛（仍在 `cancelling` 且**无**活动执行记录才收敛），在释放执行权
     之前调用；`TaskScheduler.settleFailedDispatch` 经可选端口 `cancellationSettler` 复用同一收口。
  2. 工作流以 `blocked` / `recovery_required` 停止后任务状态不收敛：`stopWithEvent` 增加
     `convergeStoppedTask`，把任务收敛为 `recovery_required`（写 `FailureSummary` 与
     `task.recovery_required` 事件）。选 `recovery_required` 而不是 `failed`，因为 `failed` 会让
     `CheckWorkflowStart`、`DecideTaskExecutionHandover` 与队列准入都拒绝 resume，等于删掉既有恢复入口。

### 2.8 Web 与交付形态

- 页面：概览 `/`、需求工作台 `/planning`、需求 `/requirements`、任务 `/tasks`、项目 `/projects`、
  机器 `/machines`、设置 `/settings`（含环境管理与角色管理）。导航中没有独立环境入口。
- 状态与进度只来自接口真实字段；未知取值原样只读展示；开始 / 停止 / 删除失败后重新读取服务端状态；
  网络中断只提示"结果未知"，不自动重发有副作用的请求；禁用操作必须给出原因。
- 交付形态与 V1 一致：前端产物 `//go:embed` 内嵌为单文件程序，`make release` 先生成前端再编译 Go，
  服务默认只监听 `127.0.0.1`，未实现远程访问认证前不监听所有网卡。

---

## 3. 核心架构

分层与 V1 相同，V2 未引入第二套工作流引擎：

```text
cmd → cli / httpapi → app → agent / process / docker / source / storage / events → domain
```

- `internal/domain`：业务语义、状态迁移、准入判定、进度聚合唯一来源（纯函数、无 IO）。V2 新增
  `planning_session.go`、`planning_message.go`、`planning_revision.go`、`planning_analysis.go`、
  `planning_confirmation.go`、`project_execution.go`、`project_execution_plan.go`、`project_progress.go`、
  `role.go`、`role_defaults.go`、`role_tools.go`、`container_docker_condition.go`。
- `internal/app`：生命周期、编排、事务边界、外部调用。V2 新增 `PlanningSessionService`、
  `PlanningRuntimeService`、`PlanningAgentService`、`PlanningAnalysisService`、
  `PlanningAnalysisRecoveryService`、`PlanningSessionCoordinator`、`RequirementService`、
  `RequirementAnalysisService`、`RequirementPlanningService`、`RoleService`、`ProjectExecutionService`、
  `ProjectExecutionKeeper`、`RequirementDeleteResult` 相关实现、`EnvironmentBuildManager`。
  复用的 V1 服务未重写：`TaskWorkflowService`、`StepWorkflowService`、`StepExecutor`、`ValidationService`、
  `TaskScheduler`、`TaskLaunchService`、`ContainerService`、`ExecutionEnvironmentService`、
  `EnvironmentService`、`ProjectService`。
- `internal/httpapi`：只做协议转换、DTO、错误码映射与 SSE。
- `internal/storage/sqlite/migrations/`：Schema 唯一入口。
- `lingting-agent-flow-ui/`：展示与操作发起，不实现工作流逻辑。

注册到统一 `internal/lifecycle` 的后台服务（按启动顺序、反序停止）：`validation`、`step_executor`、
`validation_engine`、`step_workflow`、`container_recovery`、`execution_residue`、`code_fetch_recovery`、
`planning_analysis_recovery`、`task_workflow`、`task_launch`、`project_execution_keeper`、`task_scheduler`、
`code_save_recovery`、`environment_builds`、`planning_agent`。规划 Agent 的准入与在途登记在同一临界区完成，
停止时拒绝新调用、取消在途调用并等待其连同 Session 归档收敛；SSH / 容器服务缺失时规划能力整体降级，
普通任务路径不受影响。

进程模型：同一数据目录只允许一个 `agentflow server`（`<database_path>.server.lock` 独占文件锁，锁被占用
立即失败）；任务级执行权由进程内协调器登记，规划会话由进程内协调器串行化，项目级执行权落库用于跨请求与
跨重启识别；不存在跨进程租约、执行权接管或多实例仲裁。

---

## 4. 核心业务模型

| 模型 | 职责 | 关键约束 |
|---|---|---|
| `Project` | 项目登记 | `default_host_id` 可空，新引用必须存在且未逻辑删除 |
| `PlanningSession` | 一次需求规划的生命周期 | 六个状态，与 Task 状态机无关，不复用 Task 状态名 |
| `PlanningMessage` | 一轮消息（用户 / 需求分析师 / 方案规划师 / 系统） | 会话内序号唯一，只追加不覆盖 |
| `PlanningRevision` | 一轮完整快照（分析 + 方案 + 校验结果） | 版本号唯一，追加写入，`snapshot_version` 必读 |
| `RequirementAnalysis` / `ImplementationPlan` | 结构化分析与实施方案 | 严格解析，失败即规划失败；任务草案 ≥ 1 |
| `Requirement` | 正式需求 | 由确认产生；`project_id` / `planning_session_id` / 两份快照 / `confirmed_at` / `deleted_at`；逻辑删除 |
| `Task` | 需求的一个执行单元 | 沿用 V1 状态机；`requirement_sequence` 只由确认流程写入；无租约列 |
| `Task → Step → Attempt → Session` | V1 执行记录 | 未新增执行实体 |
| `ProjectExecutionLease` | 项目级执行权 | `project_id` 主键、`owner`、单调 `epoch`、`expires_at`；迟到写入被拒绝 |
| `ProjectExecutionPlan` / `RequirementProgress` / `ProjectProgress` | 项目内顺序、队首分类与进度投影 | 只由 Task 真实状态与执行权计算 |
| `Role` | 固定四角色配置 | ID / 类型固定；能力上限由 domain 计算 |
| `EnvironmentVersion` / `Container` / `CodeSave` / `WorkflowRun` 等 | V1 既有模型 | 结构未因 V2 改动 |

`PlanningSession` 的状态：`active` / `analyzing` / `preview_ready` / `confirmed` / `abandoned` / `failed`，迁移只经
`domain` 的 `Transition` 校验；`confirmed` / `abandoned` / `failed` 为终态，不再接受消息并进入释放流程。

---

## 5. 需求到执行的完整流程

```text
选择项目 → 创建规划会话（active）
  → 提交消息并启动两阶段分析（analyzing；同一会话同时只允许一轮）
  → 分析师产出结构化需求分析 → 规划师在此之上产出实施方案
  → 单个事务写 Revision + 角色消息 → preview_ready（可多轮调整，同容器同快照）
  → 用户确认（RequirementService.Confirm）
       单个事务：requirements(1) + tasks(N, 带 requirement_sequence) + 会话 confirmed
       确认前核验执行节点；不创建 Step、不启动执行
  → 任务 queued（用户手动启动）
  → 项目级"开始执行"：取得项目执行权 → 清除停止意图 → 触发一次调度扫描
  → TaskScheduler：全局名额 → queued 候选 → 既有准入与排序 → 项目闸门（有效执行权 + 未停止 + 队首）
  → 既有 TaskLaunchService.Launch → 既有 TaskWorkflowService（V1 全链路：计划 → 开发 → 确定性验证
    → 语义审查 → 失败修复 → 代码保存 → 会话归档 → 收尾）
  → 收敛：成功则队首前移并自动领取下一个；失败 / 待恢复则项目阻塞并释放执行权
  → 进度：需求进度按任务完成数量比计算，项目状态按 running > paused > blocked > idle 投影
  → 收尾操作：显式停止任务（cancelling → 真实收敛）与显式删除需求（未收敛返回 409，逻辑删除保留历史）
```

停止相关硬规则：执行记录停止必须同时把任务收敛为 `recovery_required`。只停执行记录会让项目闸门永久认为
项目内有任务在执行、执行权不释放、后续任务不领取、需求也无法删除。`blocked` / `recovery_required`
执行记录本身按设计保留，仍属于"未结束记录"；判断自动推进是否停止看任务状态，不看执行记录是否存在。

---

## 6. 安全边界

V1 的边界全部保留：主机密钥校验不可关闭、known_hosts 全程只读；私钥只读、不复制、不入库、不入日志，
加密私钥直接报错；不存储模型凭据与私有仓库凭据（私有仓库认证由用户自定义镜像负责）；不挂载 Docker
Socket、用户 Home、SSH 私钥与远程配置目录；HTTP 默认只监听回环地址。

V2 新增：

- 规划阶段的项目代码只读由**容器文件系统**实施（只读卷挂载 + 只读 rootfs + tmpfs），工具策略是第一道
  限制而非唯一限制。
- 托管 Pi 调用固定 `--no-mcp` 与 `--no-extensions`，空工具列表落到 `--no-tools`，不回退 Pi 默认工具。
- 角色能力上限由服务端固定计算，接口提交越界配置直接 `400`；前端不做权限判定。
- 容器自动启动只发生在 `exited` / `created` 且字段一致的现场；`paused` / `restarting` / `dead` /
  `removing` / 未知取值 / 字段矛盾一律拒绝，且不写 `stopped`。
- 规划运行时的远程删除必须先通过所有权核验；结果不确定时保留现场、不自动重试、不提供批量清理或全局扫描
  删除能力。
- 无用户、用户组、权限管理与操作审计（V2 非目标）。

---

## 7. 数据库与迁移

- `internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本是 Schema 唯一事实来源，由 Goose 在打开数据库时
  按版本执行并记录在 `goose_db_version`；每个脚本在单个事务内执行，失败整体回滚。
- 当前共四个脚本：`00001_initial_schema.sql`（初始最终态）、`00002_planning_analysis_claims.sql`（Revision
  快照版本与分析占用）、`00003_requirement_confirmation.sql`（需求项目关联、规划快照、`deleted_at`、
  `requirement_sequence`）、`00004_project_execution_leases.sql`（项目执行权与执行意图）。
- 约束规则：不声明外键、不用触发器模拟引用完整性、枚举式字段不设 `CHECK` 白名单且未知取值必须能原样
  读回；保留主键、`NOT NULL`、有业务意义的 `UNIQUE`、数值范围、长度、`json_valid` 与跨字段一致性。
  逻辑删除优先，删除不级联清空历史。
- 迁移到版本化脚本时已删除手写版本判断、`schema.sql` 与规划会话跨进程租约表；程序不识别旧的手写 Schema
  数据库、不做数据回填，旧开发库需用户备份后手动重建。
- `verification_results` 仍是冻结遗留表：结构保留在初始迁移，生产代码不读不写，禁止重新读写或在没有可靠
  数据保全方案时删除该表定义。

---

## 8. HTTP 接口面（`/api/v1`）

```text
GET    /health | /system/info | /settings/config
GET    /roles | /roles/{roleID}          PUT /roles/{roleID}

GET    /projects | /projects/{projectID}
POST   /projects/{projectID}/start       POST /projects/{projectID}/cancel
GET    /projects/{projectID}/execution   GET  /projects/{projectID}/tasks
GET    /projects/{projectID}/requirements

GET    /planning/sessions                POST /planning/sessions
GET    /planning/sessions/{id}           GET  /planning/sessions/{id}/messages
POST   /planning/sessions/{id}/messages  GET  /planning/sessions/{id}/revisions
POST   /planning/sessions/{id}/analysis  POST /planning/sessions/{id}/analysis/retry
POST   /planning/sessions/{id}/confirm   POST /planning/sessions/{id}/abandon
GET    /planning/sessions/{id}/runtime   POST /planning/sessions/{id}/runtime/prepare
POST   /planning/sessions/{id}/runtime/release

GET    /requirements/{requirementID}     DELETE /requirements/{requirementID}
GET    /requirements/{requirementID}/progress

GET    /environments                     POST /environments/init
GET    /environments/{type}/{name}       POST /environments/{type}/{name}/versions
POST   /environments/{type}/{name}/build
GET    /environments/{type}/{name}/versions/{v}[/dockerfile|/build-log|/build-status]
```

规则：错误映射沿用既有约定（`400` 参数 / `404` 不存在或归属不符 / `409` 状态冲突 / `412` 前置条件缺失 /
`500` 执行发生但未成功 / `503` 服务未装配或已停止）；路径标识非法沿用 `404`。响应必须区分"规划草案"与
正式 Task，草案不带正式 Task ID。确认接口只接受 `revision_id` 与可选 `host_id`，需求与任务由服务端依据已
采纳的 Revision 计算。项目执行接口不接受请求体，执行权、顺序与结果全部由服务端依据持久化事实计算。

---

## 9. 测试与 CI

- 测试目录结构与 V1 相同（`tests/unit`、`tests/integration/{local,pi,ssh,remote}`、`tests/e2e`、
  `tests/internal/{testkit,fakepi}`、`tests/config`），前端测试在 `lingting-agent-flow-ui/tests/{unit,e2e}`。
- 测试授权制不变：未经用户明确要求，不运行测试、不创建测试文件；里程碑文档中列出的测试项只是"获得授权后
  必须覆盖的范围"。
- `tests/integration/local/database/`（Schema 审计、旧库升级与数据保留）已在测试精简中删除，Schema 没有
  自动化审计守点，新增迁移脚本必须人工核对。
- CI 门禁与分组执行脚本沿 V1：格式与静态检查、测试包分组、Go 单元与本机集成、前端检查与构建、单文件打包
  冒烟、真实远程用例（按配置与授权决定执行或跳过）。真实测试的 CI 授权 fail closed。

---

## 10. 最终验收依据

**验收基线**：`e98a8a1`。以下结论来自 M6 收口时的真实环境运行记录，本轮收口**没有重跑**任何测试，也没有
修改生产代码。

真实环境验收（真实 SSH + 远程 Docker + 容器内 Pi + 真实 SQLite + 生产服务装配），全部 PASS，
每次运行结束的容器零残留核验（`REMOTE_CONTAINER_VERIFY`）均为 PASS：

| 场景 | 结果 |
|---|---|
| A 执行半段：1 需求 2 任务顺序执行 | PASS（321s） |
| A 规划半段：两角色多轮会话复用同一容器 | PASS（33s） |
| B 规划阶段只读 | PASS（27s） |
| C 跨项目并行 + D 同项目失败阻塞 | PASS（264s） |
| E 需求删除 | PASS（69s） |
| V1 全链路回归 | PASS（100s） |

关键证据：

- **A**：任务 1 真实 `completed` 后任务 2 被自动领取并真实 `completed`；每项目在途峰值 1；需求进度
  `completed`（current=2）；项目投影 `idle`；两任务各留下 step / attempt / session / code save 记录且都推送
  成功（真实 commit SHA）。
- **B**：文件工具与 Shell 写入均被只读文件系统拒绝，无提交 / 推送 / 合并记录。
- **C / D**：两项目同时执行且各自严格串行（在途峰值各 1），全局在途不超过配置上限 3；A1 三次尝试后验证
  未通过 → 工作流 `blocked`（`attempts_exhausted`）→ 任务收敛 `recovery_required`；A2 保持 `queued`；项目 A
  显示 `blocked` 且原因为 `task_recovery_required`；项目 B 不受影响，正常 `completed`。
- **E**：运行中删除 → `409` 且无任何部分软删除；停止 → 真实收敛 `cancelled`；删除 → `200`
  （`deleted_task_count=2`）；任务状态未被改写；事件 8 条（含 `requirement.deleted`）；项目任务事实为空；
  容器保留且 `condition=running`（未删除、未停止）。

自动化检查（M6 授权范围内执行并通过）：`gofmt` / `go build` / `go vet` 无输出；Go `domain`、`store`、`app`、
`httpapi` 定向用例与 `-race` 并发守点通过；前端 Vitest 361 例、Playwright 297 例通过；`tsc --noEmit`、
`vp lint`、生产构建通过。

M2 的真实环境验收另取得：只读 rootfs 与只读快照挂载成立、文件工具与 Shell 写入被拒绝、两角色多轮 Session
上下文连续、容器停止与服务重建后同一容器与快照恢复、资源释放零残留、`paused` 真实字段与全部业务拒绝、
Goose 全新文件库初始化与迁移版本表守点。M1 的真实远程端到端回归（`TestRemoteE2EFullChain`）在修正"规划只
规划、开发只验收"的职责边界后于 2026-11-03 通过。

M5 收口时**未执行**测试与真实环境验收（未获授权），退出条件以代码层面证据记录；其项目级调度场景（C 跨项目
并行、D 同项目失败阻塞）由 M6 的真实环境验收覆盖。

---

## 11. 明确遗留事项

1. **`P4-M5`（基于真实使用数据的优化）仍为 `pending`**：属于 V1 遗留，不属于 V2，不因 V2 收口而改变状态，
   也不作为 V2 完成条件。
2. **项目默认环境选择未实现**：`projects` 没有"默认环境版本"列，"空值 = 当前可用版本"还是必填项仍未定稿，
   设置中心里该入口未交付。
3. **同一需求内不允许手动调整任务执行顺序**：顺序在方案确认时由数组下标固定，不提供调整或提前启动入口。
4. **规划会话长时间闲置后的容器处置未定稿**：没有闲置回收策略，只在会话进入 `confirmed` / `abandoned` /
   `failed` 后按释放流程处理。
5. **已确认需求不可修改**：纠正路径只有"停任务 → 逻辑删除需求 → 用户手动回滚 Git → 新建规划会话"。
   删除只影响业务视图与调度，不代表仓库已恢复。
6. **`dead` / `removing` 容器条件在真实环境仍未观测**（`paused` / `restarting` 已观测）；这两类条件的判定
   只有单元用例覆盖。
7. **"代码保存之后结果不明"的删除现场只有应用层用例**：场景 E 的删除发生在代码保存之前，真实环境未构造
   代码保存结果不确定的删除现场。
8. **并发删除、执行权占用与事务回滚只在单元与 `-race` 用例中验证**，真实环境未做并发压测。
9. **两阶段结构化分析在真实模型下的稳定性未单独取证**：解析与编排可靠性由 Fake Agent + 真实 SQLite 覆盖，
   真实规划容器验证覆盖的是多轮会话连续性与只读边界；多轮上下文截断策略与真实模型输出稳定性仍未实测。
10. **无认证、授权与操作审计**：服务仍只监听回环地址，未实现远程访问认证。
11. **Schema 没有自动化审计守点，且不兼容旧开发库**：新增迁移脚本必须人工核对表结构、索引与非枚举约束；
    `00002`–`00004` 之后的结构变化意味着更早创建的开发数据库需备份后重建。
12. **真实远程 `merge` 结果不确定后的核验仍只有本机证据**（V1 遗留）；`merge_after_push` 仍默认关闭，
    项目内任务串行**不等于**解决分支集成。
13. **需求删除不处置容器**：删除后可能留下孤儿容器，仍需显式 `delete` / `detach`；`detached` 容器不被自动
    接管。
14. **设置中心的基础配置只读**：不提供编辑、保存、热更新与重启入口，修改配置仍需用户改文件并重启服务。
15. **真实远程工作流用例只覆盖成功路径**（V1 遗留）；远程失败与恢复路径由注入用例与本地链路级用例覆盖。
16. **代码注释中仍有一处指向已删除过程文档的引用**：`internal/httpapi/execution_feedback.go` 第 14 行引用
    `.steps/P4-M2-5-validation-feedback-api.md`，属 V1 收口遗留引用，未在 V2 清理。
17. **V2 过程文档已删除，索引引用尚未同步**：根目录 `README.md`、`docs/development.md`、`docs/workspace.md`
    仍指向 `.steps/V2/**`，需要用户确认后再更新为本文。
