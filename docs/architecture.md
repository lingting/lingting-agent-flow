# 架构说明

本文件描述 AgentFlow 的架构边界与依赖方向，用于判断“新代码应该放在哪里”以及“哪些依赖不允许出现”。

> 下文的架构图为**目标架构**，仅说明分层意图。当前代码实现了 CLI 骨架、配置加载与校验、
> 日志封装、SQLite 数据层、执行节点管理与环境预检、SSH 服务探测与 SSH 连接层（主机密钥校验、
> 私钥认证、远程命令执行、SFTP 传输与远端 Unix 套接字通道）、统一 Pi Runner（本机与容器内）、
> **单步骤执行协调器与开发步骤顺序执行**、**确定性验证引擎**、**统一执行环境（远程 Linux +
> Docker）**、**代码获取（CodeFetcher）与代码初始化协调器**与任务管理 HTTP 接口。
> 图中调度、多步骤工作流编排与 UI 的完整形态尚未实现。

## 目标架构

```text
CLI / Web UI
      |
Application Services
      |
Scheduler / Workflow Engine
      |
Agent Runner / Process Manager
      |
Pi RPC / Git Worktree
      |
SQLite
```

- **CLI / Web UI**：只负责发起操作与展示结果。
- **Application Services**：CLI 与 HTTP 的共同入口，编排各领域能力。
- **Scheduler / Workflow Engine**：任务领取、并发控制与 Plan / Develop / Verify / Retry 状态迁移；
  当前只接入单步骤执行与显式顺序执行，自动 Plan / Verify / Retry 编排仍未接入 HTTP。
- **Agent Runner / Process Manager**：以独立子进程方式驱动 Pi，并管理进程生命周期。
- **Pi CLI / 任务容器**：非交互 `pi --print --session` 调用与任务工作区隔离。
- **SQLite**：唯一真相来源。

## 目录地图

```text
cmd/agentflow/            CLI 入口，只做构建信息注入与启动
internal/app/             应用服务，CLI 与 HTTP 的共同调用入口；
                          含单步骤执行协调器 step_executor*.go（校验、占用、调用与终态写入）
                          与执行环境实现 execenv*.go（准备、状态、执行、释放）
internal/cli/             Cobra 命令实现
internal/config/          配置结构与 YAML 加载、校验
internal/logging/         结构化日志封装
internal/domain/          核心实体、状态与业务规则（含单步骤执行准入规则）
internal/source/          代码获取抽象（Fetcher）与 Git Clone 实现：容器内 Clone、基础分支识别、
                          任务分支初始化与错误分类
internal/execenv/         统一执行环境契约：Kind、Workspace、Environment、ExecSpec/Result、
                          PrepareSpec/Prepared、ReleaseRequest/Result、Provider 与 ExecPort
internal/scheduler/       任务领取、并发控制、Worker 与恢复（未实现）
internal/workflow/        Plan / Develop / Verify / Retry 状态迁移（引擎已实现，未接入 HTTP）
internal/agent/           统一 Agent 接口：Runner、RunRequest、RunResult、Event
internal/agent/pi/        Pi CLI 适配器：本机 Runner、容器 Runner、Session 路径与日志落盘
internal/process/         外部进程生命周期管理：启动、输出读取、超时、取消与回收
internal/gitworktree/     任务工作区创建、检查与清理（未实现）
internal/storage/         存储接口与统一存储错误（含步骤占用的原子事务接口）
internal/storage/sqlite/  数据库连接、迁移与仓储实现
internal/sshprobe/        SSH 端点探测：TCP 可达性与协议标识校验
internal/sshclient/        SSH 连接层：主机密钥校验与算法协商、身份认证、远程执行与 SFTP 传输
internal/events/          内部事件定义与事件总线（未实现）
internal/lifecycle/       后台服务注册、启动、故障传播与逆序停止
internal/httpapi/         HTTP 路由与处理器（SSE 未实现）
lingting-agent-flow-ui/   React + TypeScript 前端（独立 pnpm 工程）
configs/                  示例配置
migrations/               迁移说明（实际迁移脚本在 internal/storage/sqlite/migrations/）
docs/                     架构与开发文档
tests/unit/               快速单元测试（不依赖真实 Pi、SSH 或网络）
tests/unit/ssh/           SSH 连接层单元测试（主机密钥与类型记录、认证配置、路径与结果状态）
tests/unit/app/           应用服务单元测试（会话、Runner 生命周期、单步骤执行编排）
tests/integration/local/  本机集成测试（数据库全链路、进程内 SSH/SFTP 服务端、TCP 探测、
                          执行环境生命周期与工作区隔离）
tests/integration/pi/     需要本机真实 Pi CLI 的集成测试
tests/integration/ssh/    需要真实远程机器的集成测试
tests/e2e/                端到端测试（CLI 完整操作路径）
tests/internal/testkit/   测试工作空间与通用辅助组件
tests/internal/fakepi/    以测试二进制充当 Pi CLI 的可控替身
```

标为“未实现”的目录在对应能力落地前不创建，避免留下只有接口没有使用场景的抽象。

测试代码不放在生产源码目录，全部集中在根目录 `tests/` 下，按测试类型与领域分层，
每个子目录都是独立的 Go 测试包。`tests/internal/testkit` 提供统一的测试工作空间：
在系统临时目录的 `lingting-agent-flow-tests` 下创建随机根目录，为每个用例分配独立子目录，
并保证测试产物不被自动删除。`tests/internal/fakepi` 让测试二进制充当 Pi CLI 替身，
使 Pi Runner 的单元测试不依赖本机是否安装 Pi。

## 依赖方向

```text
cmd
 └── cli / httpapi
      └── app
           ├── scheduler
           ├── workflow
           ├── storage
           └── events
                └── agent / process / gitworktree / config
                     └── domain
```

约束：

- `domain` 处于最底层，只依赖 Go 标准库，禁止 import 本项目其他内部包。
- 单步骤执行准入规则（任务/步骤状态、角色类型、占用检查）只在 `domain` 中定义一次，
  `app` 只调用它，`httpapi` 与前端不得复制第二份判断。
- `domain` 不得感知数据库、HTTP、JSONL RPC、子进程与文件系统。
- `config` 可以把角色类型映射到 `domain.RoleType` 以复用取值校验，但配置结构自身保持独立，不把 YAML 细节泄漏进 `domain`。
- `app` 之上的层不得直接访问 SQLite；所有持久化经由仓储接口。
- `scheduler` 与 `workflow` 不得直接创建 Pi 进程，只能通过 `agent` 接口。
- `httpapi` 与 `cli` 不得包含状态迁移逻辑，只调用 `app`。
- `httpapi` 不得自行拼接 Pi 命令、创建 Attempt 或直接赋值执行状态；单步骤执行只能调用
  `app.StepExecutor`。
- `app.StepExecutor` 只通过容器执行端口发起 Pi 调用，不直接构造命令行，也不创建或接管容器。
- 前端不得复制工作流规则，只消费 HTTP API。

## 当前实现的分层

```text
cmd/agentflow → cli / httpapi / server → app → config / logging / storage / sshclient / docker
                                              ↘ workflow / lifecycle
                                                   ↘ domain
```

- `main.go` 只注入构建信息、处理信号并返回退出码。
- `cli` 只做参数解析、结果输出与退出码转换，不包含校验规则。SSH 认证只使用节点配置中的
  本机私钥文件，命令层不收集任何凭据，也不存在隐藏输入或标准输入读取路径。
- `app` 组装配置与日志，并把配置校验编排成一次应用服务调用；同时负责把配置中的角色转换为存储层的初始化定义，以及把节点配置转换为 SSH 连接目标与认证配置。
- `config` 负责 YAML 解析、未知字段拒绝与字段级校验。
- `logging` 封装 `log/slog`，日志统一输出到标准错误流。
- `storage` 只定义接口与存储错误，不依赖具体驱动；`storage/sqlite` 实现连接、迁移与各实体仓储。
- `sshprobe` 只做网络与协议标识探测，不进行身份认证，不依赖 `cli` 或 `storage`。
- `sshclient` 提供 SSH 连接层，内部按职责分为：私钥认证（`AuthConfig` 与
  `buildAuthMethods`）、主机密钥信任（`HostKeyStore` / `KnownHostsStore`，含按 known_hosts
  记录提供的主机密钥类型 `HostKeyTypeProvider`）、连接管理（`Dialer` / `Client`）、
  远程执行（`Run` 与 `CommandResult`）、SFTP 传输（`FileTransfer`）、远程工作空间
  （`ResolveHome` / `ResolveWorkspace` / `EnsureWorkspace`）与远端 Unix 套接字通道
  （`DialUnixSocket`）。它不依赖 `cli`、`storage` 或 `app`，业务代码通过接口注入依赖，
  不在各处直接创建 SSH 客户端。
- `dockerfile` 只做 Dockerfile 静态解析（多阶段、`AS` 别名、`ARG` 替换、`--platform`），
  不访问网络也不调用 Docker；`docker` 负责远程 Docker 命令构造、帮助信息核对与镜像元数据
  查询，并集中定义校验超时。
- `app` 中的 `HostValidator` 编排三项必需检查，`HostValidationService` 作为后台服务接入
  `internal/lifecycle`，两者都不在 HTTP Handler 内创建长期 goroutine。
- `app.StepExecutor` 是单步骤执行协调器（`internal/app/step_executor*.go`），按职责拆分为
  校验与占用、提示词构造、执行与终态写入、Git 状态采集、生命周期五部分。它是所有执行入口里
  唯一负责"创建 Attempt → 调用 Pi → 写终态"的组件，同时实现 `lifecycle.Service`，因此
  Server 停止时能取消在途执行并等待收敛。
- `app.StepExecutor.ExecuteSteps`（`internal/app/step_sequence.go`）是显式的顺序执行入口：
  它按 `steps.sequence` 逐个调用 `Execute`，不生成计划、不自动重试、不串联验证与代码保存，
  遇到失败、超时、取消或中断立即停止。顺序执行不新增调度器，也不复制状态迁移规则。
- Git 工作区状态采集经由 `StepExecutionGitPort`，默认实现是
  `ContainerService.InspectWorktree`（`internal/app/code_inspect.go`），它复用
  `codesaver.Inspector` 的只读核验：不提交、不推送、不合并，也不写入 `code_saves`。
- `httpapi` 只做协议转换：解析执行参数、把错误类别映射为状态码、序列化结果。它不判断任务
  是否可执行，也不读数据库。
- `workflow` 引擎（多步骤编排）已经存在并可在 CLI 下使用，但 `agentflow server` 构造的任务
  服务不含工作流引擎，因此 HTTP 侧不会自动推进任务。

## 领域模型边界

| 概念 | 含义 | 归属 |
|---|---|---|
| Role | 用户自定义角色，含类型、模型、提示词与工具策略 | domain |
| Requirement | 用户提交的原始需求 | domain |
| Task | 某需求的一次完整执行，持有工作区 | domain |
| Step | 计划中的业务步骤 | domain |
| Attempt | 某个 Step 的一次执行尝试 | domain |
| Session | 一次具体的 Pi 会话 | domain |
| VerificationResult | 工作流引擎一次验证的结构化结果（含 Agent 辅助评估） | domain |
| ValidationRun | 确定性验证引擎的一次运行记录（状态与结论分离） | domain |
| ValidationCheckResult | 一项确定性检查的真实执行结果 | domain |
| ExecutionHost | 通过 SSH 管理的远程执行节点（连接目标与工作空间） | domain |

- Step / Attempt / Session 是三个不同层次，不得合并为一个对象。
- `ValidationRun` 与 `VerificationResult` 是两个不同模型：前者是确定性验证引擎的运行记录
  （状态、结论、逐项检查结果分别持久化），后者是工作流引擎的验证快照（含 Agent 辅助评估，
  结果整体以 JSON 保存）。两者不得合并，也不得互相替代。
- 重试必须产生新的 Attempt 与新的 Session，历史记录只追加不覆盖。
- 状态只能通过 `Transition`（或实体自身的结束方法）变更，不允许直接赋值绕过校验。
- 角色名称由用户自由命名，工作流只依据角色类型识别职责。
- `ExecutionHost` 只保存连接目标、本机私钥文件路径、远程工作空间、主机密钥信任信息与
  环境预检状态，**不保存任何凭据材料**。
- `AuthMethod` 只有 `private_key` 一个取值：SSH Agent、密码认证与加密私钥均已移除，
  不得重新引入，也不得回落到其他认证方式。
- `ValidationStatus`（`checking` / `normal` / `abnormal`）表达总体预检状态，
  `CheckStatus`（`passed` / `failed` / `timed_out` / `skipped` / `not_run`）只用于单项结果；
  两者不得合并，也不得新增第二套总体状态名。

## 配置与领域的关系

配置中的 `roles[].type` 复用 `domain.RoleType` 的取值与校验，因此角色类型只在一处定义。配置结构自身定义 `runner`、`model`、`system_prompt` 与 `tools_policy.mode`，这些是部署期参数，不属于领域实体。

第一版只定义 `tools_policy` 的结构并校验其取值，不据此实施实际权限控制。配置中出现权限声明，并不意味着 Pi 已经执行了对应限制。

## 数据与状态

- 数据库是唯一真相来源，关键状态迁移必须落库后再继续执行。
- 任务领取使用事务与原子状态迁移，禁止“先查询、后更新”。
- schema 变更必须在 `internal/storage/sqlite/migrations/` 追加新迁移脚本，不修改已发布的迁移。
- 迁移脚本通过 `embed` 嵌入程序，运行时按版本顺序自动执行；已应用版本记录在 `schema_migrations`。
- 跨实体的关键状态更新必须在 `storage.Tx` 内完成，不能在应用层分多次调用独立方法后假定原子性。
- 外键统一为 `ON DELETE RESTRICT`：角色、需求、任务、步骤等被历史记录引用时不可删除，避免历史丢失。
- 完整 Agent 会话历史不能作为唯一业务记录，业务结论必须写入结构化字段。
- 单步骤执行的步骤占用必须在单个事务内完成：以状态为前置条件创建 Attempt，再条件更新
  Step 与 Task。禁止"先查询、后无条件更新"，也不得只依赖进程内互斥锁保证一致性。
- `ON DELETE RESTRICT` 是早期设计；当前 Schema 不声明外键，引用完整性由应用层承担（见
  `AGENTS.md` 的数据库约束规范）。

## 外部进程边界

- Pi 只使用非交互调用方式 `pi --print --session <path> --model <id>`，Prompt 通过标准输入写入
  并立即关闭；不使用 RPC 模式、不使用 `--no-session`、不依赖伪终端。
- 必须处理启动失败、非零退出码、超时、取消、意外退出、输出流关闭与子进程未退出。
- 子进程的终止与回收统一由 `process` 负责，其他包不直接管理进程。
- 取消与服务关闭必须传播到实际 Pi 进程；两者必须能区分：调用方取消记为 `cancelled`，
  服务关闭导致的记为 `interrupted`。

## 单步骤执行边界

单步骤执行是 Task Service 与 Pi Runner 之间的中间层，边界如下：

```text
HTTP Handler
  └── app.StepExecutor.Execute
        ├── domain.CheckStepExecutionStart（准入规则）
        ├── 执行节点准入 + 容器核验（只读，不创建/不启动）
        ├── storage.StepExecutionStore.BeginStepExecution（单事务占用 + 创建 Attempt）
        ├── ContainerService.RunAgent（统一 Pi Runner，独立 Session）
        └── 终态写入：Attempt → Step → Task（各自条件更新）
```

- 一次请求只执行一个已存在的 Step；不生成计划、不串联步骤、不自动重试。
- 执行是同步的：Attempt 进入终态后才返回响应，不存在"已受理但仍在后台"的语义。
- 同一 Step 同一时刻只有一个有效占用，重复与并发请求返回 409。
- 每次执行创建新的 Attempt 与独立的 Session 文件，不复用旧记录。
- `completed` 只代表本次 Agent 调用正常结束，不代表任务验收通过；任务保持 `developing`。
- 失败与超时把任务收敛为 `recovery_required` 而不是终态 `failed`：是否重试需要完整工作流判断。
- 终态写入失败必须如实报告（`Finalized=false` / `execution_not_persisted`），不得因响应
  写入失败而跳过 Session 保存与状态更新。
- 本层不创建、启动、停止或接管容器，也不执行 Commit / Push / Merge。

### 开发步骤顺序执行

顺序执行是"单步骤执行"之上的一层薄协调，而不是新的调度器：

```text
HTTP Handler（POST /api/v1/tasks/{taskID}/execute-steps）
  └── app.StepExecutor.ExecuteSteps
        ├── 任务状态准入（domain.TaskStatus.AllowsStepExecution）
        ├── storage.StepStore.ListStepsByTask（按 sequence 排序）
        ├── domain.NextStepToExecute（跳过非开发步骤、拒绝不确定与已占用步骤）
        ├── domain.CheckStepPrerequisites（前序步骤必须 completed / skipped）
        └── app.StepExecutor.Execute（复用单步骤执行，每次新建 Attempt 与 Session）
```

- 每个开发步骤使用独立 Session，但共享同一任务容器的同一代码工作区；新建 Session 不会
  重新 Clone，也不会覆盖已有代码。
- 执行前必须校验前置步骤：全部序号更小的步骤处于 `completed` 或 `skipped` 才允许执行，
  否则返回 409。该规则只在 `domain` 中定义一次。
- 失败、超时、取消与中断都会立即停止，不自动重试，也不自动执行验证与代码保存。
- 同一任务的顺序执行在进程内以任务为键串行化，跨进程仍由步骤占用的事务条件更新拒绝。

### 代码状态记录

- 每次执行在占用步骤之后采集一次"执行前"快照，在 Pi 结束后采集一次"执行后"快照，记录到
  对应 Attempt（`git_branch` / `git_before_json` / `git_after_json`）。
- 快照只包含分支、HEAD、是否存在未提交变更与变更条目计数：不保存完整 diff，也不写入
  `code_saves`。
- 采集失败必须记录为 `status_read = false` 与原因，不能被当作"工作区干净"，也不能把成功的
  执行改写为失败。
- 采集是只读的：`codesaver.Inspector` 复用代码保存的前置核验，但只执行 `git status` 与
  `git rev-parse`。

## 确定性验证引擎边界

验证引擎是"执行结果"与"验收结论"之间的独立一层，边界如下：

```text
HTTP Handler（POST .../attempts/{attemptID}/validate）
  └── app.ValidationService.Validate
        ├── 归属与状态校验（Task → Step → Attempt）
        ├── 执行节点准入 + 容器核验（只读，不创建/不启动）
        ├── storage.ValidationStore.BeginValidationRun（单事务占用 + 创建 Validation Run）
        ├── CheckExecutor.Exec（按配置顺序执行，逐项 AppendValidationCheckResult）
        ├── domain.DecideValidation（纯函数计算结论）
        └── 终态写入：Validation Run 结论 + Attempt 判定（各自条件更新）
```

### Validation Run 与 Check Result

| 概念 | 含义 | 存储 |
|---|---|---|
| Validation Run | 一次验证的完整记录 | `validation_runs` |
| Check Definition | 一项确定性检查的配置（随 Run 保存快照） | `validation_runs.spec_json` / `validation_check_results.definition_json` |
| Check Result | 一项检查的真实执行结果 | `validation_check_results` |

- 每次验证创建独立的 Validation Run，历史记录只追加不覆盖。
- Check Result 独立成表而不是塞进单个 JSON 字段：需要按检查标识、状态与验证记录查询与审计。
- 完整输出只保存落盘位置与摘要（`stdout_path` / `stderr_path` + `stdout_summary` /
  `stderr_summary`），大输出由文件承载，避免撑爆 SQLite 普通字段。

### 状态与结论的区别

- `validation_runs.status`（`pending` / `running` / `completed` / `failed` / `cancelled` /
  `timed_out`）描述**验证流程本身发生了什么**：是否跑完、是否被取消、是否整体超时。
- `validation_runs.verdict`（`passed` / `failed` / `inconclusive`）描述**现有证据能否支持
  通过**。两者严格分离：`completed` 不代表 `passed`，`failed` 状态也不必然对应
  `failed` 结论，不得互相推导。
- 单项检查状态（`validation_check_results.status`：`pending` / `running` / `passed` /
  `failed` / `skipped` / `timed_out` / `cancelled` / `error`）必须区分"命令以非零码正常结束"
  与"命令没能正常执行"，否则失败证据会被压平成一个布尔值。
- 结论规则由 `domain.DecideValidation` 计算，不可被 Agent 结论覆盖（见 `AGENTS.md`）。

### 当前能力边界

- 检查执行环境固定为受管理的任务容器，通过 `docker exec` 在容器内工作区执行；容器缺失、
  未运行或节点未通过预检时返回前置条件错误，不回退到本机执行。
- 只支持命令型检查（`command`）：可执行文件 + 参数数组，不经过 shell。
- 不发起 Validator Agent 辅助评估：结论函数已支持 `AgentAssessment` 输入并按"只能收紧、
  不能放宽"实现，但本阶段不创建 Validator Session。
- 检查输出落在数据库目录下的 `validations/` 子目录，属于本机持久化数据，不写入项目目录。
- 单步骤的开发—验证衔接见"单步骤开发—验证闭环边界"：验证引擎本身仍只负责执行检查与计算
  结论，收口决策由 `domain.DecideStepCloseout` 承担。

## 单步骤开发—验证闭环边界

闭环是"单步骤执行"与"确定性验证"之上的一层薄协调，而不是新的调度器：

```text
HTTP Handler（POST /api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate）
  └── app.StepWorkflowService.ExecuteAndValidate
        ├── 归属 / 前置步骤 / 验证配置校验（domain.CheckStepPrerequisites、必需检查存在）
        ├── app.StepExecutor.Execute（DeferCompletion=true，新建 Attempt 与独立 Session）
        │     └── 开发成功时步骤进入 verifying 并保持被当前尝试占用
        ├── app.ValidationService.Validate（同一容器、同一工作区）
        ├── domain.DeriveStepVerificationState（由 Run 状态 / 结论 / 检查结果推导验证状态）
        ├── domain.DecideStepCloseout（开发结论 + 验证状态 → 步骤收口状态）
        └── 收口写入：步骤（条件更新）→ 任务（条件更新）→ 事件
```

### 状态分层

| 层次 | 取值来源 | 说明 |
|---|---|---|
| 开发执行状态 | `attempts.status` / `StepExecutionOutcome` | `completed` 只表示 Agent 调用正常结束 |
| 验证流程状态 | `validation_runs.status` | 流程是否跑完、是否被取消或超时 |
| 验证结论 | `validation_runs.verdict` | 现有证据能否支持通过 |
| 步骤验证状态 | `domain.StepVerificationState`（派生，不落库） | 供收口决策与展示使用 |
| 最终步骤状态 | `steps.status` | 由 `domain.DecideStepCloseout` 计算后写入 |

`StepVerificationState` 是从 Validation Run 派生出来的**决策视图**，不写入任何表：数据库仍只
保存 `validation_runs.status` 与 `validation_runs.verdict`，两者严格分离的约束不会因为多了一个
聚合视图而被抹平。

### 收口规则

| 开发结论 | 验证情况 | 步骤状态 |
|---|---|---|
| 失败 / 超时 | 未执行 | `failed` |
| 取消 | 未执行 | `cancelled` |
| 中断 | 未执行 | `recovery_required` |
| 成功 | 未发起验证 | `recovery_required` |
| 成功 | 验证通过 | `completed`（已验证完成） |
| 成功 | 必要检查失败 | `failed` |
| 成功 | 验证被取消 | `cancelled` |
| 成功 | 验证基础设施异常 | `recovery_required` |

- 规则只在 `domain.DecideStepCloseout` 中定义一次，不得在 Handler 或前端复制。
- 单个步骤通过验证**不会**把任务推进到 `completed`：任务保持 `developing`，终态由后续代码
  保存与任务级编排决定。只有"需要人工核验"的结论才把任务推进到 `recovery_required`。

### 执行失败、验证失败与基础设施异常的区别

三者必须可区分，任何一项都不能替代其他项：

- **执行失败**：开发阶段结论为 `failed` / `timed_out` / `cancelled` / `interrupted`。此时没有
  待验证的完成品，不启动验证。
- **验证失败**：验证流程跑完，但至少一项启用的必需检查未通过（退出码不在允许范围内、输出
  未匹配预期、检查超时或取消）。这是代码问题，步骤收敛为 `failed`。
- **基础设施异常**：验证流程本身失败或整体超时，或任一启用的必需检查状态为 `error`
  （命令无法启动、结果无法确认）。这**不是**代码问题：既不伪装成验证失败，也不判为通过，
  步骤收敛为 `recovery_required`。

### 失败反馈

- 验证未通过时写入 `validation_feedbacks`：Task / Step / Developer Attempt / Validation Run
  引用、失败检查标识与名称、实际退出码、失败原因、有长度限制的输出摘要、检查起止时间、
  超时/取消/基础设施标记，以及开发会话引用。
- 反馈与原始 Validation Run 一起保留，不替代它；同一轮验证至多一条反馈，历史反馈不被覆盖。
- 输出摘要与原因在 `domain.RedactSensitiveText` / `domain.ClipFeedbackText` 中统一脱敏与
  裁剪：令牌、口令、私钥与 URL 内嵌凭据一律过滤，长度受 `domain.MaxFeedback*` 限制。
- 用户显式重新执行时，提示词会引用上一轮失败反馈（"上一次验证未通过的问题"），但反馈**不是**
  当前验证结论，新的执行仍然必须创建新的 Attempt 与独立 Session 并通过新的验证。

### 当前能力边界

- 闭环只覆盖单个 Step：不生成计划、不串联多个步骤、不自动重试、不自动修复代码。
- 同一 Step 同一时刻只允许一条闭环：进程内以步骤为键拒绝重复请求，跨进程由步骤占用的事务
  条件更新拒绝。
- 不实现任务级调度队列与多任务并发编排；下一步才是按计划推进多个 Step 并在验证失败时停下。

## SSH 边界

- SSH 探测（`sshprobe`）只验证 TCP 可达性与 SSH 协议标识，属于网络层检查。
- SSH 认证与远程操作（`sshclient`）属于另一个边界：只有完成认证后才能执行远程命令、
  传输文件或管理远程 Docker。
- 两者不得合并：探测成功不得被当作认证成功，也不得据此开放远程操作路径。
- 探测结果中的 `Authenticated` 恒为 `false`，用于阻止调用方误判。
- 主机密钥校验不得被关闭：`NewDialer` 未收到 `HostKeyStore` 时直接拒绝构造，不存在
  "跳过校验"的开关，也不允许用 `ssh.InsecureIgnoreHostKey()` 作为产品默认策略。
- 主机密钥的读取来源（用户 `known_hosts`）与写入目标（AgentFlow 自己的记录文件）分离：
  用户既有文件只读，新记录以独占锁 + 临时文件 + 原子重命名写入，避免并发写坏记录。
- 凭据边界：只支持无口令私钥，连接过程不接收任何凭据输入。私钥文件只被读取，其路径
  可以进入节点记录，但私钥原文、密码与口令一律不得进入数据库、配置文件、命令行参数或日志。
- 私钥边界：不得把私钥复制到工作空间或远程机器，也不得修改其权限或内容；遇到加密私钥
  必须返回明确的不支持错误，而不是要求交互输入口令。
- 远程命令执行以结构化命令描述（程序名 + 参数数组）构造，参数统一经过 POSIX Shell 转义，
  禁止把用户输入直接拼接进命令字符串。
- 超时与取消只关闭对应的远程会话，不关闭整条连接，避免一个命令影响同连接上的其他操作。
- 日志中不得出现私钥内容、认证头或其他凭据材料。
- 环境预检边界：Docker 兼容性只基于 CLI 帮助信息，不执行容器创建、启动或镜像层下载；
  镜像预检只查询 `FROM` 元数据。`normal` 不得被表述为"远端 Docker Engine 已验证可用"。
- 执行节点准入边界：任何使用持久化执行节点执行真实远程操作的入口都必须先确认
  `validation_status` 为 `normal`；该限制由后端强制实施，不能只依赖界面禁用。

## 执行环境边界

统一执行环境契约位于 `internal/execenv`，远程 Linux + Docker 实现位于
`internal/app/execenv*.go`（`app.ExecutionEnvironmentService`）。

```text
Task / Pi Runner / Validator
        │  只依赖 execenv.Provider / execenv.ExecPort
        ▼
Execution Environment（execenv）
        ├── Prepare（校验 → 镜像 → 创建容器 → 核验可用性）
        ├── Status（只读核验）
        ├── Exec（参数数组 + 工作目录 + 显式环境变量）
        └── Release（keep / delete / detach）
                │
                ▼
      app.ExecutionEnvironmentService
                │
     ┌──────────┼───────────┐
     ▼          ▼           ▼
Environment  Container   docker.Runner
 Service      Service     （SSH）
```

- 只有 `execenv.KindRemoteContainer` 已实现；`local_linux`、`local_darwin`、
  `local_windows` 返回 `ErrUnsupportedKind`，不会静默回退到本机执行。
- 准备阶段固定为 `validate` → `image` → `create` → `start` → `verify`，失败结果保留
  已执行阶段，使失败可定位。每次准备创建全新容器，不复用旧容器。
- `Exec` 只接受可执行文件 + 参数数组，工作目录必须是 `/workspace` 之下的安全绝对路径；
  环境变量只能显式声明，执行层不继承宿主机或服务进程环境。
- 执行结果分别表达退出码、stdout、stderr、结束状态与"是否可确认"：超时、取消、
  执行通道不可用与结果无法确认都不判定为成功。
- `Release` 的处置结论由 `execenv.DecideRetention` 复用
  `domain.DecideContainerLifecycle`：成功删除、失败保留、中断默认保留。已脱离管理的
  容器不会被自动删除或重新接管；容器已不存在时删除按成功处理。
- 任务与执行环境的关联集中在 `ExecutionEnvironmentService.PrepareForTask`：执行节点、
  语言环境、版本、缓存与工作目录全部来自任务记录，准备成功后才把容器写回任务。
- Pi Runner 的工作目录与容器内 Session 目录来自执行环境描述
  （`ContainerAgentRequest.WorkDir` / `SessionDir`），不在 Runner 内硬编码第二套路径。
- 验证引擎通过 `execenv.ExecPort` 在对应 Attempt 的容器工作区执行检查命令，验证记录中的
  `execution_env` 为 `remote_container`。

### 当前能力边界

- 只实现远程 Linux + Docker 容器执行环境；本机 Windows / macOS / Linux 执行环境尚未实现。
- 项目代码只通过 Git Clone 进入容器（`source.GitFetcher`）；wget、curl、SCP 等获取方式未实现。
- 不实现多步骤自动编排、自动重试与任务级调度队列。
- 不实现多步骤自动编排、自动重试与任务级调度队列。

## 代码获取边界

代码获取由统一抽象 `source.Fetcher` 表达，第一版只有 Git Clone 实现
（`source.GitFetcher`），远程执行环境内的服务入口是 `app.CodeInitializeService`。

```text
HTTP Handler / CLI
  └── app.CodeInitializeService.Initialize
        ├── 归属与状态校验（Task → Step → Attempt → Container → Host）
        ├── 幂等判定（同一 Attempt 已成功 → 写 skipped，不重新 Clone）
        ├── storage.CodeFetchStore（pending → running → 终态）
        ├── ContainerService.FetchSourceWith（远程会话内按来源类型构造 Fetcher）
        │     └── source.GitFetcher.Fetch
        │           ├── git --version / 目标目录检查 / mkdir -p
        │           ├── git clone --no-hardlinks -- <url> <workdir>
        │           ├── 基础分支识别（origin/HEAD → main → master，或显式指定）
        │           ├── git checkout --no-force -b agent/<task-id>（或检出已存在分支）
        │           └── git rev-parse HEAD / --abbrev-ref HEAD（工作区核验）
        └── 事件追加（source.fetched）与结果记录
```

- 命令一律以可执行文件 + 参数数组表达，通过统一执行环境在受管理容器内执行；
  文件系统检查类命令使用固定的 `sh -c` 模板，模板本身固定，唯一变量是已经过
  `domain.ValidateContainerWorkDir` 校验的容器内绝对路径。
- 目标目录固定为容器内 `/workspace/project`（或任务配置的 `/workspace` 之下路径），
  不会拉取到 AgentFlow 管理工作区或宿主机目录；容器不可用时直接返回前置条件错误。
- 任务分支固定为 `agent/<task-id>`，由 `domain.TaskBranchName` 生成与校验；分支已存在时
  只检出（远程优先、其次本地），从不强制重置或覆盖。
- 初始化结果持久化到 `code_fetches`：来源类型、脱敏仓库地址、目标目录、容器标识、
  基础分支与 commit、任务分支与来源、实际 HEAD、状态、失败阶段、错误类别与摘要、
  起止时间。状态（`status`）、失败阶段（`stage`）与错误类别（`error_code`）分别保存。
- 幂等边界：同一 Attempt 在同一容器上已有成功记录时写入 `skipped` 记录并复用原 commit；
  目标目录非空时直接失败，不会为了重试自动清空目录。
- 认证范围：第一版不支持私有仓库认证。仓库地址必须通过 `domain.ParseGitRepositoryURL`
  校验，内嵌凭据（`user:token@host`）直接拒绝；Clone 输出显示需要认证时返回
  `repository_auth_required`，不静默降级为匿名访问。凭据由环境镜像自行注入。
- 失败与中断：Clone 失败、基础分支缺失、分支冲突与工作区核验失败分别映射到不同错误类别；
  超时、取消与结果不确定分别记录为 `timed_out`、`cancelled`、`uncertain`，都不得判定为成功。
- 代码获取不负责 Push、Merge、容器生命周期与重试决策；失败不会删除容器或工作区。
- 后续接入 HTTP 下载、wget、curl 与 SCP 时只需新增 `source.Fetcher` 实现，并在
  `app.newContainerFetcher` 注册；服务层、持久化模型与调用方都不必改动。
- 接口固定为 `POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code`，
  只接受执行参数（可选目标目录与超时）。错误映射：参数错误 400、目标不存在或归属不符 404、
  状态冲突 409、前置条件缺失 412、执行未成功 500。
- 代码初始化成功不等于 Agent 开发流程已打通：Developer 逐步执行仍由后续阶段实现。

## 工作区边界

- 三种目录必须显式区分，不得互相替代：
  - 容器内项目代码目录：`/workspace/project`（`execenv.DefaultProjectDir`），代码拉取、
    Agent 开发与验证检查都在这里；不绑定挂载到远程宿主机。
  - 容器内临时 Session 目录：默认 `/tmp/agentflow/sessions`，容器删除后消失，因此必须先
    回传本机；不得位于项目代码目录之内。
  - 远程 AgentFlow 管理工作区：默认 `~/.lingting/agentflow`，只存放项目配置、Dockerfile
    与镜像构建资源，不得存放项目源代码。
- 依赖缓存使用带 AgentFlow 标签的 Docker 命名卷，不得落在 `/workspace` 之下，避免项目
  代码被共享缓存覆盖，也避免缓存成为项目代码的唯一保存位置。
- 目标形态下每个 Task 拥有独立 Git Worktree，同任务多步骤共享该工作区；当前实现的远程任务
  使用容器内工作目录，本机 Worktree 尚未接入。
- 单步骤执行不会克隆代码、不会初始化仓库，也不会修改工作区路径：它只在已就绪的容器内
  执行 Agent 调用。
- 未经明确配置，不得自动删除包含未提交代码的工作区。
- 执行节点的远程工作空间默认 `~/.lingting/agentflow`，`~` 指远程 SSH 登录用户的 Home；
  路径遵循 POSIX 语义。SFTP 不会展开 `~`，因此必须先通过已认证连接取得远程 Home，再把
  配置解析为远程绝对路径；目录创建只在解析出的工作空间路径内进行，且必须显式授权。
- 容器生命周期状态与任务执行状态分别记录：不得仅凭 Task 状态推断 Docker 容器一定存在。
- Session 文件、检查输出与执行记录一律落在本机持久化目录，容器删除后仍然可读。

## 安全边界

- HTTP 服务默认只监听 `127.0.0.1`，不开放任意来源 CORS。
- 不存储模型 API 密钥，凭据由 Pi 及其 Provider 管理。
- 角色权限采用最小化原则，实际权限通过 Agent 配置与执行环境实施。

## 文档边界

- 文档只描述已实现的能力，不提前声明可用命令或接口。
- 代码编辑期间不同步文档，仅在明确要求时更新。
