# 架构说明

本文件描述各条执行链路的边界、状态规则与数据事实，用于判断「某个行为应该由谁负责」以及
「哪些实现不允许出现」。目录布局、包职责与依赖方向见 [workspace.md](workspace.md)；
HTTP 接口契约见 [http-api.md](http-api.md)；测试规范见 [testing.md](testing.md)。

## 领域模型边界

| 概念 | 含义 | 归属 |
|---|---|---|
| Role | 用户自定义角色，含类型、模型、提示词与工具策略 | domain |
| Requirement | 用户提交的原始需求 | domain |
| Task | 某需求的一次完整执行，持有工作区 | domain |
| Step | 计划中的业务步骤 | domain |
| Attempt | 某个 Step 的一次执行尝试 | domain |
| Session | 一次具体的 Pi 会话 | domain |
| ValidationRun | 确定性验证引擎的一次运行记录（状态与结论分离） | domain |
| ValidationCheckResult | 一项确定性检查的真实执行结果 | domain |
| ExecutionHost | 通过 SSH 管理的远程执行节点（连接目标与工作空间） | domain |

- Step / Attempt / Session 是三个不同层次，不得合并为一个对象。
- 验证结论只有一套模型：`ValidationRun`（流程状态与最终结论分离）与 `ValidationCheckResult`
  （逐项检查结果），结论只能由 `domain.DecideValidation` 计算。历史版本的 `VerificationResult` /
  `NewVerificationResult` / `decideVerdict` 判定模型已删除，其遗留表 `verification_results` 只作为
  已冻结的历史数据保留，运行时不再读写。
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
- 实体字段校验失败返回 `*ValidationError`，可用 `errors.Is(err, ErrInvalidValue)` 判断；非法状态迁移
  返回 `ErrInvalidTransition`。
- `TaskStatus.IsTerminal` 只包含 `completed` 与 `cancelled`（允许显式重试的 `failed` 不是终态）；
  删除判定使用 `TaskStatus.IsFinished`，它额外包含 `failed`。
- 逻辑删除使用 `deleted_at`：读取层默认过滤已删除记录，只有显式 `IncludeDeleted` 才纳入查询；
  历史记录一律保留。
- 四层关联（Task → Step → Attempt → Session）由应用层校验：Attempt 的 Task 必须与其 Step 的 Task
  一致，Session 的 Task / Step / Attempt 必须属于同一条任务链。数据库不声明外键，孤立关联 ID
  可以被写入，但不得被当作合法关系。
- Session 的执行状态、原始 JSONL 归档状态与 HTML 导出状态必须分别记录，不得合并为一个字段。
- 不得伪造执行结果：未调用真实执行器时不得把 Step / Attempt / Session 标记为 `running`、
  `completed` 或 `succeeded`，也不得把测试夹具数据写入真实开发数据库冒充执行历史。

## 配置与领域的关系

- 配置文件（`configs/agentflow.yaml`）只保留运行期启动参数：应用标识与日志级别、HTTP 监听与关闭超时、
  数据库路径、工作区根目录、调度并发与重试上限、SSH 连接层的路径与超时。角色定义随代码内置，角色配置
  持久化在 SQLite 的 `roles` 表并由 `app.RoleService` 读取与更新；工具限制由 `domain.EffectivePiTools`
  在执行层实施，不再依赖配置文件中的声明。
- 配置在启动阶段一次性加载并完成命令行覆盖，之后作为运行期事实传给需要它的组件：数据库、调度器、
  SSH、Session 与验证输出目录都由同一份 `config.Config` 派生。`app.RuntimeConfig` 是这份事实的只读
  快照（数据库路径、最终监听地址、调度并发上限、Session 根目录四项），由
  `GET /api/v1/settings/config` 提供；接口不重新读取配置文件，也不暴露 SSH 路径、凭据或完整配置对象。
- 配置变更只通过修改配置文件并重启服务生效，不提供编辑、保存、热更新或重启入口。

## 数据与状态

- 数据库是唯一真相来源，关键状态迁移必须落库后再继续执行。
- 任务领取使用事务与原子状态迁移，禁止“先查询、后更新”。
- 跨实体的关键状态更新必须在 `storage.Tx` 内完成，不能在应用层分多次调用独立方法后假定原子性。
- 尝试序号由存储层在事务内分配（`storage.AttemptStore.StartAttempt`），禁止"先查最大值再无条件插入"；
  `NextAttemptNumber` 只用于展示与预估。
- 依赖条件的更新必须检查影响行数并返回 `storage.ErrConflict`：迟到的结果不得覆盖已确认的终态，
  重复完成不得重新打开终态记录。
- 完整 Agent 会话历史不能作为唯一业务记录，业务结论必须写入结构化字段。
- 单步骤执行的步骤占用必须在单个事务内完成：以状态为前置条件创建 Attempt，再条件更新 Step 与
  Task。禁止"先查询、后无条件更新"，也不得只依赖进程内互斥锁保证一致性。
- 迁移脚本、schema 与引用完整性的长期约束见 `AGENTS.md` 的「数据库约束规范」；本文件不重复列出。

## Pi Runner 与 Session 生命周期边界

- 所有 Planner、Developer、Validator 调用都必须经由统一 Runner（`app.ContainerService.RunAgent`）
  发起，各自对应一条独立的 Session 记录；禁止在 CLI、HTTP Handler 或工作流代码里自行拼接
  Pi 命令行。本机 Runner 收到容器目标时必须直接失败，不得静默回退到本机执行。
- Pi 只使用非交互调用方式 `pi --print --session <path> --model <id>`，Prompt 通过标准输入写入并
  立即关闭；不使用 RPC 模式、不使用 `--no-session`、不依赖伪终端，也不默认追加
  `--no-extensions` / `--no-skills`（扩展阻塞在交互输入时由超时或取消结束并给出诊断）。
- 必须处理启动失败、非零退出码、输出解析失败、超时、取消、意外退出、输出流关闭与子进程未退出。
  子进程的启动、终止与回收统一由 `process` 负责，其他包不直接管理进程。
- 每次调用使用独立且唯一的 Session 文件路径：由服务端按任务 / 步骤 / 尝试 / 会话 / 调用标识分层
  生成，并校验落在 Session 根目录之内；调用方不得指定任意本地路径。
- Session 记录必须在 Pi 进程启动之前写入数据库并推进到 `running`；启动失败、工作目录不可用等路径
  必须把该记录收敛为 `failed`，不得残留 `running`。
- stdout、stderr 与退出状态分别处理：两个流分别写入 `stdout.log` 与 `stderr.log`，日志有字节上限，
  截断时在文件末尾留下明确标记；数据库只保存路径、状态与摘要。
- 取消与服务关闭必须传播到实际进程，并在宽限期后强制终止仍不退出者；两者必须能区分：调用方取消
  记为 `cancelled`，服务关闭导致的记为 `interrupted`。Server 停止时取消在途调用并等待收敛。
- 原始 JSONL 必须复制到本机持久化数据目录（默认 `<数据库目录>/sessions`），或在清理远程环境前
  完成可靠回传；不得只保存在容器内部、远程配置目录或项目代码目录，也不得写入本机真实项目目录。
- 归档只有在本机确实拿到可读原始文件时才记为成功；HTML 导出只能使用 Pi 官方 `--export`，导出失败
  只降级导出状态，不得删除或覆盖原始 JSONL，也不得把成功的执行改写为失败。
- 会话内容默认只在本机持久化，不打印完整内容到日志或控制台，也不上传到外部存储服务。
- Runner 不负责工作流重试决策，也不决定任务的成功清理策略；Provider 只负责获取代码与初始化任务
  分支，不负责 Push、Merge 或删除容器。
- 容器生命周期状态与任务执行状态分别记录：不得仅凭 Task 状态推断容器一定存在，也不得用容器状态
  推断任务是否成功。

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
- 执行入口固定为 `app.StepExecutor`（`internal/app/step_executor*.go`），按职责拆分为校验与占用、
  提示词构造、执行与终态写入、Git 状态采集、生命周期五部分。它是唯一负责「创建 Attempt → 调用
  Pi → 写终态」的组件，并实现 `lifecycle.Service`，Server 停止时能取消在途执行并等待收敛。
- 任务准入状态固定为可合法迁移到 `developing` 的集合（`preparing`、`planned`、`developing`、
  `retrying`、`paused`、`recovery_required`）；`queued` 必须先完成环境准备与计划，`cancelling`
  与已结束状态一律拒绝。该规则只在 `domain` 中定义一次。
- 超时、取消与中断分别映射为 `timed_out`、`cancelled`、`interrupted`，不得把任何一种当作成功；
  用户取消按状态机把 Step 收敛为 `cancelled`，并把任务经 `cancelling` 收敛为 `cancelled`。
- 单个步骤失败不得覆盖其他步骤的历史结果。
- 终态写入使用未取消的上下文并分别执行：Attempt、Step、Task 三条写入互不阻断，任一失败都要记录到
  结果的 `FinalizationErrors`，接口不得把写入失败报告为成功。
- 迟到结果不得覆盖已确认的终态：所有终态写入都必须带状态前置条件，冲突时读取数据库真实值。
- 执行事件（开始、完成、失败、中断）写入任务时间线；事件写入失败只记录日志，不回滚执行状态。
- `httpapi` 只做协议转换：解析执行参数、把错误类别映射为状态码、序列化结果。接口契约见
  [http-api.md](http-api.md)。

### 开发步骤顺序执行

顺序执行是"单步骤执行"之上的一层薄协调，而不是新的调度器：

```text
HTTP Handler（POST /api/v1/tasks/{taskID}/execute-steps）
  └── app.StepExecutor.ExecuteSteps
        ├── 任务状态准入（domain.TaskStatus.AllowsStepExecution）
        ├── storage.StepStore.ListStepsByTask（按 sequence 排序）
        ├── domain.NextStepToExecute（跳过不可自动推进的阶段、拒绝不确定与已占用步骤）
        ├── domain.CheckStepPrerequisites（前序步骤必须 completed / skipped）
        └── app.StepExecutor.Execute（复用单步骤执行，每次新建 Attempt 与 Session）
```

- 每个开发步骤使用独立 Session，但共享同一任务容器的同一代码工作区；新建 Session 不会
  重新 Clone，也不会覆盖已有代码。
- 执行前必须校验前置步骤：全部序号更小的步骤处于 `completed` 或 `skipped` 才允许执行，
  否则返回 409。该规则只在 `domain` 中定义一次。
- 失败、超时、取消与中断都会立即停止，不自动重试，也不自动执行验证与代码保存。
- 同一任务的顺序执行在进程内以任务为键串行化，跨进程仍由步骤占用的事务条件更新拒绝。
- 入口固定为 `app.StepExecutor.ExecuteSteps`（`internal/app/step_sequence*.go`）：HTTP Handler 与 CLI
  不得自行串联步骤、自行判断"下一步该执行谁"，也不得跳过未完成的步骤。
- 顺序执行按 `steps.sequence` 推进，不依赖数据库默认返回顺序；每次只执行一个 Step。
- `recovery_required` 的步骤与已被占用的步骤（`running` / `developing` / `verifying`）一律阻止自动
  推进，必须先人工核验现场。
- Developer 执行成功只代表本次 Agent 调用按协议正常结束，不代表编译、测试或验证通过；Developer
  不得改写 `verification_verdict` 等由验证阶段写入的字段，也不得通过修改状态宣告验收通过。
- 不允许静默退回宿主机执行 Pi 或 Git：容器缺失、未运行、已 detached 或节点未通过预检时一律返回
  前置条件错误。

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
- 结论规则由 `domain.DecideValidation` 计算，不可被 Agent 结论覆盖；规则至少包含：验证流程必须处于
  `completed`；至少存在一项启用的必需检查；每一项启用的必需检查都有结果且状态为 `passed`；必需检查
  非零退出码、超时、取消、跳过、启动失败与结果缺失都判 `failed`；可选检查失败不影响结论但必须保留
  结果；全部检查被跳过或没有启用的必需检查时不得判通过。

### 当前能力边界

- 验证入口固定为 `app.ValidationService`（`internal/app/validation*.go`）：HTTP Handler、CLI 与工作流
  都不得自行执行检查命令、自行创建 Validation Run 或直接写验证结论。
- 检查执行环境固定为受管理的任务容器，通过 `docker exec` 在容器内工作区执行；容器缺失、未运行、
  已 detached 或节点未通过预检时返回前置条件错误，不回退到本机执行。
- 命令检查以可执行文件 + 参数数组表达，禁止拼接 shell 字符串，也不得把所有命令交给 `sh -c` /
  `bash -c`。
- 检查执行必须设置超时、响应 context 取消、限制输出字节数，并分别捕获标准输出与标准错误；结果必须
  区分"命令以非零码正常结束"与"启动失败、超时、取消、结果无法确认"，不得压缩成单个布尔值。
- 检查结果逐项写入（`storage.ValidationStore.AppendValidationCheckResult`）：部分检查完成后发生错误
  时，已产生的证据仍必须可追溯，不得因最后一项失败而整体丢失。
- 同一 Attempt 同一时刻只允许一个有效验证：占用通过 `storage.ValidationStore.BeginValidationRun`
  在单个事务内以状态为条件完成，冲突返回 `storage.ErrConflict` 并映射为 409；禁止只依赖进程内互斥锁。
- 验证结论写回 Attempt 必须使用带状态前置条件的更新；迟到结果不得覆盖已确认的终态。
- 结论或检查结果写入失败时不得报告验证成功：结果必须携带 `Persisted=false` 与 `PersistenceErrors`，
  HTTP 映射为 `execution_not_persisted`（500）。
- 检查输出落盘失败（`output_archive_error`）与检查判定相互独立：归档失败不得改写退出码或检查状态，
  必须单独记录。
- 检查覆盖配置不得放宽门槛：覆盖后仍要求存在启用的必需检查，并执行同一套结论规则。
- Agent 语义审查只作为附加输入：Agent 声称通过不得覆盖必需检查的失败，也不得替代缺失的必需检查。
- 验证服务作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`：Server 停止时取消在途验证
  并等待收敛，把记录推进到失败终态，不留 `running` 残留。
- 检查输出落在数据库目录下的 `validations/` 子目录，属于本机持久化数据，不写入项目目录。
- 单步骤的开发—验证衔接见"单步骤开发—验证闭环边界"：验证引擎本身只负责执行检查与计算结论，收口
  决策由 `domain.DecideStepCloseout` 承担。接口契约见 [http-api.md](http-api.md)。

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

- 入口固定为 `app.StepWorkflowService.ExecuteAndValidate`（`internal/app/step_workflow*.go`）：HTTP
  Handler、CLI 与工作流不得自行串联开发与验证，也不得自行判断"开发成功了就该验证"。
- 开发阶段必须复用 `app.StepExecutor`，验证阶段必须复用 `app.ValidationService`：禁止新建第二套
  Validator，也禁止把验证逻辑塞进 Handler。
- 只有以下条件全部成立时才启动验证：开发结论为 `completed`、执行记录已完整写入、验证配置存在启用的
  必需检查、任务容器仍受管理且运行中、上下文未被取消。
- 验证配置缺失或无效时必须在开发之前返回明确错误（`ErrStepWorkflowInvalidConfig`），不得跑完开发后
  把配置问题伪装成"验证未通过"。
- Developer 与 Validator 必须针对同一份任务容器工作区：中间不得重新 Clone、不得重置或清理已有代码
  改动，也不得在验证结束前删除容器与必要临时资源。
- 修复循环只能是显式的、有上限的领域流程（`domain` 的修复循环规则），默认停止并等待用户决定：
  不自动删除容器，也不自动开始下一步骤。
- 收口写入必须使用状态前置条件：步骤只在仍处于 `verifying` 时被改写，迟到的结果不得覆盖已确认的
  终态；收口为可再次执行或终态时必须释放 `current_attempt_id`。
- 闭环协调器作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`，注册顺序在步骤执行与验证
  引擎之后，停止顺序因此反过来：先停止新请求入口，再让下层服务收敛。
- 同一 Step 同一时刻只允许一条闭环：进程内以步骤为键拒绝重复请求，跨进程由步骤占用的事务条件更新
  拒绝；重复与并发请求返回 409，不得启动第二个 Pi 进程。
- 未验证通过不得返回 200：响应必须如实区分开发结论、验证是否发起、验证结论与最终状态。接口契约见
  [http-api.md](http-api.md)。

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
- 任务级串联由"任务级工作流编排"负责，见下一节。

## 计划生成边界

计划生成是任务级编排器的一个独立入口，它负责"把需求变成可持久化的计划"，不负责执行。

```text
HTTP Handler（POST /api/v1/tasks/{taskID}/plan → 200 + 真实结果）
  └── app.TaskWorkflowService.GeneratePlan
        ├── domain.CheckPlanGenerationStart（状态 / 容器 / 无已落库步骤 / 已配置规划角色）
        ├── 未结束工作流检查 + 容器核验 + 执行节点预检
        ├── 规划角色与开发角色加载（类型必须匹配且启用）
        ├── domain.TaskStatus.CanStartPlanning → planning（状态条件更新，跨进程闸门）
        ├── app.SessionService.Start（任务级会话，无步骤与尝试归属）
        ├── app.ContainerService.RunAgent（容器内 Planner 调用）
        ├── app.SessionService.Finish（写入产出与归档）
        ├── app.ParsePlanOutput（结构化解析，未知字段拒绝）
        ├── domain.BuildPlan（确定性校验 + 角色绑定 + 任务级必需检查合并）
        └── persistPlan（同一事务）
              ├── CreateSteps（plan 阶段步骤 + develop 步骤）
              ├── UpdateTaskIfStatus（planning → planned）
              └── AppendEvent（plan.created，含摘要）
```

- **计划结构**：序号 1 固定是 `plan` 阶段步骤（绑定规划角色），其后是 `develop` 步骤
  （绑定开发角色）。计划以规划阶段开头，因此任务级编排器可以"从 plan 启动"。
- **Planner 调用成功不等于计划成功**：解析失败、校验失败或持久化失败都不会让任务进入
  `planned`，也不会留下半成品计划。
- **并发**：`planning` 的状态条件更新是跨进程闸门；进程内另有任务级互斥锁作为第一道防线。
- **失败收敛**：调用失败 / 超时 / 输出非法 / 校验失败 / 持久化失败 → `failed`；用户取消 →
  `cancelling` → `cancelled`；服务停止导致的中断 → `recovery_required`。
- **启动恢复**：停留在 `planning` 的任务按持久化事实收敛——已有步骤 → `planned`，
  没有步骤 → `recovery_required`。恢复**不重放 Planner 调用**。
- **不覆盖已有计划**：已有步骤时直接拒绝；重新规划与计划版本化尚未设计。

## 任务级工作流编排边界

编排器是"单步骤闭环"之上的一层薄协调：它决定**执行哪一个 Step、按什么顺序、何时停止**，
但不重新实现任何下层能力。

```text
HTTP Handler（POST /api/v1/tasks/{taskID}/execute-workflow → 202 + 真实初始状态）
  └── app.TaskWorkflowService.StartWorkflow
        ├── domain.CheckWorkflowStart（任务状态 / 容器 / 计划 / 序号 / 阶段类型 / 必需检查 / 现场明确）
        ├── 容器核验（受管理、未 detached、运行中）+ 执行节点预检（EnsureHostUsableForRemoteTask）
        ├── domain.NewWorkflowRun（plan_digest + total_steps）→ workflow_runs 落盘（pending）
        ├── beginRunRecord（pending → running，版本号条件写入）
        └── 后台推进（受 internal/lifecycle 管理）
              └── 循环：
                    ├── domain.NextStepToExecute（稳定顺序 + 前置步骤校验 + 现场明确）
                    ├── SetStage(step_execution, current_step_id) → 落盘
                    ├── app.StepWorkflowService.ExecuteAndValidate（复用闭环）
                    ├── 验证通过 → RecordStepCompleted（游标前进）→ 落盘
                    └── 其它结论 → 停止推进（blocked / recovery_required）
              └── 全部步骤完成 → finalize
                    ├── app.ContainerService.SaveCode（复用 CodeSaver）
                    ├── 会话归档核验
                    └── domain.DecideWorkflowCloseout → 任务状态机 + workflow_runs 终态
```

### 状态分层

| 层次 | 取值来源 | 说明 |
|---|---|---|
| 任务业务状态 | `tasks.status` | 任务当前处在哪个业务阶段 |
| 工作流执行状态 | `workflow_runs.status` | 这一次编排执行是否在推进 |
| 执行阶段 | `workflow_runs.stage` | 中断发生在哪一步（定位用，不参与成功判定） |
| 执行游标 | `current_step_id` / `current_attempt_id` / `last_completed_step_id` / `last_completed_sequence` | 恢复的唯一依据 |
| 执行权 | `worker_id` + `lease_expires_at` | 识别"进程已退出但记录仍在 running" |
| 并发版本 | `workflow_runs.revision` | 迟到结果不得覆盖已确认终态 |

`workflow_runs.status` 与 `tasks.status` 是不同层次的概念，**不得合并为同一个字段**，也不得用
工作流状态推断任务业务状态。

### 持久化与并发

- 编排进度只持久化在 `workflow_runs`（迁移 `0018_workflow_runs.sql`）：任务状态表达业务阶段，工作流
  执行状态表达"这一次编排是否在推进"，两者不得合并为同一个字段。
- 同一任务同一时刻最多一条未结束的执行记录：唯一性由 `idx_workflow_runs_active_task` 部分唯一索引
  保证，进程内以任务为键串行化只是第一道防线，不是一致性保证。
- 状态写入必须以版本号为前置条件（`UpdateWorkflowRun` 的 `revision` 条件）：迟到的结果不得覆盖已
  确认的终态，冲突时读取数据库真实值，而不是用本地对象覆盖。
- 有副作用的阶段之前必须先持久化状态与游标：启动先写 `running`，每个步骤先写 `step_execution` 与
  `current_step_id`，代码保存先写 `code_save`。阶段写入失败时不得执行远程操作。
- 每个开发步骤必须复用 `app.StepWorkflowService.ExecuteAndValidate`：编排器不得自行创建 Attempt、
  不得自行拼接 Pi 命令、不得自行执行检查命令或计算验证结论。
- 只有当前步骤验证通过（`Verified && StepStatus == completed`）才允许推进下一步；其余结论一律停止
  自动推进。
- 编排器不创建、启动、删除或接管容器，也不构造 Docker / Git 命令：容器只被核验与使用，代码保存复用
  `app.ContainerService.SaveCode`。
- 启动与恢复前必须核验容器受管理且运行中、执行节点通过环境预检（`app.EnsureHostUsableForRemoteTask`）；
  不满足时返回前置条件错误，不得绕过 SSHConfig 校验。
- 编排器作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`，注册顺序在步骤闭环之后；不得
  创建不受 Server 管理的长期 goroutine。

### 停止分类

| 触发条件 | 工作流状态 | `error_class` |
|---|---|---|
| 开发阶段失败 | `blocked` | `developer_failed` |
| 开发阶段超时 / 中断 | `recovery_required` | `developer_interrupted` |
| 闭环协调器拒绝执行 | `blocked` | `step_not_executed` |
| 验证未通过 | `blocked` | `verification_failed` |
| 执行记录或验证证据未可靠写入 | `recovery_required` | `verification_unconfirmed` |
| 存在 `recovery_required` / 被占用步骤 | `blocked` | `step_not_executable` |
| 代码保存未成功 | `blocked` | `code_save_failed` |
| 代码保存结果不确定 | `recovery_required` | `code_save_indeterminate` |
| 本次运行无代码保存能力 | `blocked` | `code_saver_unavailable` |
| 会话归档未完成 | `recovery_required` | `session_not_persisted` |
| 服务重启发现残留 | `blocked` | `worker_interrupted` / `not_started` |

规则只在 `domain.DecideWorkflowCloseout` 与编排器的停止分类中定义一次，不得在 Handler 或前端复制。

### 恢复与取消

- **恢复**：`plan_digest` 必须与当前计划一致；已完成的步骤不会重新执行；`recovery_required` 与
  仍被占用的步骤必须先人工处理。恢复只从数据库已确认的进度继续。
- **服务重启**：`running` 记录在租约过期后收敛为 `blocked`；租约仍有效时不得抢占；
  `cancel_requested` 收敛为 `cancelled`；`pending` 记录在租约窗口后收敛为 `blocked`；
  `blocked` 与 `recovery_required` 保持原状等待用户显式恢复。启动恢复**不重放 Pi 调用**。
- **取消**：请求先写入 `cancel_requested`，再向在途执行传播取消信号，最终状态由真实收敛结果
  决定；重复取消幂等。不得因 HTTP 请求成功就把仍在执行的工作流标记为已取消。
- **重启顺序**：服务重启后的处理严格依据数据库事实，顺序固定为：未结束的执行记录 → 计划生成现场 →
  取消现场。取消现场最后收敛，因为"是否还有未结束执行"只有在执行记录收敛完毕后才可靠。
- **计划生成现场**：停留在 `planning` 的任务按 `domain.DecidePlanGenerationRecovery` 收敛——已有步骤 →
  `planned`，没有任何步骤 → `recovery_required`。
- **取消现场**：停留在 `cancelling` 的任务按 `domain.DecideCancellationRecovery` 收敛——存在未结束的
  执行记录 → `recovery_required`；没有取消请求记录 → `recovery_required`；已记录取消请求且没有未结束
  执行 → `cancelled`（收敛前先写入 `TaskResultCancelled` 结论）。
- **不使用尽力而为语义**：任一记录读取、状态迁移或写入失败都必须让启动恢复返回错误，由
  `internal/lifecycle` 逆序清理已启动服务；不得只记日志后继续启动。恢复是幂等的：`UpdateTaskIfStatus`
  返回冲突且数据库状态已等于目标值时视为其他进程已完成同一次收敛，其余冲突仍然报错。
- **中断与取消区分**：`isServiceStop` 必须同时识别 `ErrStepExecutorStopped` 与 `ErrWorkflowStopped`，
  否则计划生成被服务关闭中断会被误判为用户取消并收敛为 `cancelled`。
- **取消幂等**：重复取消不改写 `cancel_requested_at`。

### 与 CodeSaver 的关系

CodeSaver 已真实实现（`internal/codesaver` + `app.ContainerService.SaveCode`），编排器通过
`WorkflowCodeSaverPort` 调用它，不构造任何 Git 命令。判定门槛固定：

- 只有任务分支**推送成功**才算"已安全保存到远程"；
- `merge_after_push` 默认关闭，开启时合并成功才算完成；
- 结果不确定时先核验远程分支，工作流进入 `recovery_required`；
- 记录本身未能可靠写入（`persist_status = unconfirmed`）时同样进入 `recovery_required`，
  错误分类是独立的 `code_save_unconfirmed`。

CodeSaver 缺失时编排器仍会按顺序执行并验证全部步骤，但收尾停留在 `blocked`
（`code_saver_unavailable`），任务不会被标记为 `completed`：**步骤全部通过验证不等于任务成功**。

### CodeSaver 的状态写入失败与恢复边界

远程 Git 操作与本机 SQLite 无法共享事务，因此"Git 做了什么"与"我们把它写进数据库了吗"必须是
两个独立事实。提交、推送与合并成功后写回 `code_saves` 仍可能失败：数据库停留在 `pending`，
而仓库已经是另一个状态。仅凭 `pending` 重新执行 Git 操作会产生重复提交或覆盖远程历史。

**持久化依据。** `0019_code_save_recovery.sql` 增加五个可空列：`baseline_commit_sha`、
`baseline_base_commit_sha`、`persist_status`、`persist_error`、`recovery_status`。基线一律在
副作用之前落盘——写不进去就中止该阶段，不在未知起点上产生新提交或合并。历史记录保持 `NULL`，
按"没有基线 → 转人工核验"处理，不补造基线。

**状态分层。** `PersistStatus`（`persisted` / `unconfirmed`，空值归一化为 `persisted`）与
`RecoveryStatus`（`unverified` / `confirmed_applied` / `confirmed_not_applied` /
`unverifiable`）各自带状态机，与 `CommitStatus` / `PushStatus` / `MergeStatus` 完全分开：
`RequiresManualRecovery` 会把 `unconfirmed` 纳入，`Result.Persisted()` 从记录派生而不是新增
一个默认为 `false` 的布尔字段。

**判定入口。** 结论只由纯函数 `domain.DecideCodeSaveRecovery(record, observation)` 计算，
三个阶段共用一套判据：

| 阶段 | 确认已发生 | 确认未发生 | 其余 |
|---|---|---|---|
| 提交 | 有基线且 HEAD ≠ 基线 | 有基线且 HEAD = 基线 | 无基线或读不到 HEAD → `unverifiable` |
| 推送 | 远程任务分支 = 预期提交 | 远程任务分支不存在 | 指向其他提交 → `unverifiable` |
| 合并 | 远程基础分支 = 唯一候选合并提交 | 远程基础分支 = 基线 | 无法推算或不一致 → `unverifiable` |

唯一候选合并提交要求同时满足：是合并提交、第一父提交等于基线、第二父提交等于任务分支提交。
仅"基础分支发生了变化"不构成证据。只有"确认未发生"才标记 `SafeToRetry`。

**只读核验。** `Saver.Observe` 只执行 `rev-parse`、`ls-remote` 与 `rev-list --first-parent`，
不提交、不推送、不切换分支、不写 `code_saves`；失败时返回 `ReadFailed` 而不是错误，让判定收敛为
`unverifiable`。人工入口是 `agentflow code verify --record <id>`，结论写回 `recovery_status`。

**结论写回。** 核验只更新 `recovery_status`，并且是条件写入：
`storage.CodeSaveStore.UpdateCodeSaveRecovery` 以读取时的恢复结论为条件、只写
`recovery_status` 与 `updated_at`。核验期间记录被其他执行推进时返回 `storage.ErrConflict`，
`VerifyCodeSave` 的 `Persisted` 保持 `false`、`Updated` 回落到数据库中的真实记录——一次只读核验
不得用旧值覆盖新事实，也不得在 SQLite 写入失败时报告恢复闭环完成。收敛判定统一由
`CodeSaveRecord.RecoveryConverged()`（仅 `confirmed_applied` / `confirmed_not_applied`）给出。
本轮不引入自动重试、后台恢复调度与新的工作流功能。

### 当前能力边界

- 只执行已经持久化并通过校验的计划：不生成计划、不补充步骤、不改变顺序。任务没有已持久化
  的步骤时 `domain.CheckWorkflowStart` 直接拒绝启动，接口返回状态冲突。
- 不创建、启动、删除或接管容器：`StartWorkflow` 启动前要求任务已有受管理且运行中的容器，
  缺少时返回 412 前置条件错误。容器由 `agentflow execenv prepare-task`
  （`app.ExecutionEnvironmentService.PrepareForTask`）单独准备，或由统一启动入口
  `agentflow task start` 在需要时自动准备。
- 不自动删除容器、不自动接手下一步骤：修复循环与重试都必须由显式请求触发，失败后等待用户决定。
- 任务之间由调度器按配置的并发上限领取 `queued` 任务；同一任务内的步骤串行推进。
- CLI 的执行入口只是 HTTP 客户端，命令索引与边界见 [cli.md](cli.md)。

## 代码保存（CodeSaver）边界

- 只操作当前受管理容器内、经过核验的任务 Git 工作区；不得把容器内代码复制到宿主机或本机目录后再
  提交。
- 任务分支统一由 `domain.TaskBranchName` 生成并校验，固定为 `agent/<task-id>`；代码获取与代码保存
  不得各自拼接分支名。
- 容器已停止、已删除或已 detached 时直接报错，不得为保存代码而启动或重新接管容器。
- 工作区没有变更时记录 `no_changes`，禁止创建空提交。
- 提交、推送与合并状态分别持久化到 `code_saves`，不得用单一 `saved` 布尔字段代替。
- 禁止默认 Force Push（`--force`、`--force-with-lease`）；远程历史冲突一律作为 `conflict` 返回，
  不得覆盖他人提交。推送结果不确定时必须先核验远程分支实际 commit，再决定状态；状态确认前禁止重试。
- 合并默认关闭（`merge_after_push = false`），只有任务配置或调用参数显式授权时才执行；合并出现冲突
  时不得自动选择 ours 或 theirs，不得伪造成功合并，也不得重置整个仓库。
- Push 成功而 Merge 失败时，任务分支的保存状态仍为成功。
- 禁止把 Git Token、密码、私钥写入日志、SQLite 普通字段、仓库 URL、Dockerfile 或镜像层；凭据只允许
  在单次操作期间以内存形式存在。
- CodeSaver 不负责容器生命周期、任务调度与重试决策，也不包含模型调用。

## 统一任务启动边界

启动入口固定为 `app.TaskLaunchService`（`internal/app/task_launch*.go`）：HTTP Handler、CLI 与
前端都不得自行串联「准备容器 → 初始化工作区 → 生成计划 → 启动编排」，也不得自行判断某一
阶段是否已完成。阶段判定集中在 `domain.DecideTaskLaunchNext`，顺序固定为
`container → workspace → plan → workflow`。

### 阶段判定

阶段顺序固定为 `validate` → `container` → `plan` → `workflow`，是否需要执行某一阶段只由
`domain.DecideTaskLaunchNext` 依据持久化事实计算：

- 任务已删除、已记录取消请求、状态不在可启动集合、存在未结束执行记录、存在
  `recovery_required` 步骤、缺少执行节点、语言环境或角色时一律拒绝启动；
- 任务容器不可用（未登记、已删除、已 detached、未运行）→ 准备容器；
- 容器可用但没有已持久化计划步骤 → 生成计划；
- 其余情况 → 启动任务级工作流。

容器可用性必须来自真实核验（`InspectContainer`），不得凭 `tasks.container_id` 推断：容器可能
已被删除、停止或脱离管理。核验失败按前置条件缺失报告，不得据此创建新容器。

### 复用而非重写

三个有副作用的阶段全部复用既有服务：容器准备用
`app.ExecutionEnvironmentService.PrepareForTask`，计划生成用
`app.TaskWorkflowService.GeneratePlan`，编排启动用 `app.TaskWorkflowService.StartWorkflow`。
启动服务不拼接 Pi 命令、不构造 Docker / Git 命令，也不创建 Attempt 与 Session。

### 已存在容器时不重复创建

已有受管理且运行中的容器时不会创建第二个容器：重复准备只会留下既无法被任何执行链路使用、
也不应被自动删除的孤儿容器。需要重建容器时走显式的容器处置路径，不由启动入口隐式完成。

### 步骤占用启动恢复

进程在 Pi 调用期间退出会留下 `running` / `developing` / `verifying` 的步骤。占用本身会阻止
该步骤再次执行（`domain.NextStepToExecute` 一律拒绝），使任务既无法推进也无法自动收敛。
`app.TaskLaunchService.RecoverStepOccupancy` 在服务启动时按
`domain.DecideStepOccupancyRecovery` 把这类步骤收敛为 `recovery_required`，释放
`current_attempt_id` 并写入事件。该恢复不重放任何 Agent 调用、不删除容器，所属任务仍有未结束
执行记录时不抢占；写入带状态前置条件，冲突按「同一次收敛已完成」处理。

### 不确定现场的处理

- 容器已启动但远程登记清单同步失败时，`Prepare` 仍返回可用的执行环境，把清单同步失败降级为
  阶段说明，避免调用方误判失败后创建第二个容器。
- 容器已创建但未能写回任务关联时，写入 `task.recovery_required` 事件（`stage=attach_container`
  与 `container_id`），使孤儿容器可被查询与人工处置；容器一律保留，不自动删除。

### 接口

`POST /api/v1/tasks/{taskID}/start` 只接受执行参数（补充提示词与三类超时），不接受容器、步骤
列表、Session 路径、工作目录或角色覆盖。响应显式区分 `stage`、`task_status`、
`container_prepared`、`workspace_status`、`workspace_commit`、`plan_persisted` 与
`workflow_started`。错误映射：参数错误 400、目标不存在 404、状态冲突 409（含工作区初始化
结果 `uncertain`）、前置条件缺失 412、启动已执行但未成功 500、结果未落库 500 且错误码为
`execution_not_persisted`、服务已停止 503。

### 任务级工作区初始化边界

启动链路中的工作区阶段固定为 `app.TaskWorkspaceService`（`internal/app/task_workspace*.go`）：
它发生在任何 Step / Attempt 存在之前，因此与尝试级的 `app.CodeInitializeService` 是两个层次。

- 记录模型：`task_workspace_initializations`（迁移 `0020`），以「任务 + 容器代次」为幂等边界。
  `code_fetches` 的 `step_id` / `attempt_id` 保持必填，尝试级契约不变，不得伪造归属。
- 状态与结论分离：`status`（`pending` / `running` / `succeeded` / `skipped` / `failed` /
  `cancelled` / `timed_out` / `uncertain`）与失败阶段 `stage`、错误类别 `error_code` 分别保存。
- `uncertain` 不是终态：它只能经只读核验收敛为确定结论，判定函数
  `domain.DecideTaskWorkspaceNext` 在遇到 `uncertain` 时直接返回状态冲突，禁止重放 Clone。
  部分唯一索引 `idx_task_workspace_init_active` 从数据库层面拒绝同任务同容器的第二条未结束记录。
- 代码获取复用同一条 `source.Fetcher` 路径（`ContainerService.FetchSourceWith`），不存在第二套
  Git 命令；工作目录解析、执行节点准入与错误状态映射由 `internal/app/code_fetch_support.go`
  统一提供，尝试级与任务级共用。
- 容器替换：判定按 `container_id` 过滤，旧容器上的成功记录不适用于新容器，历史记录一律保留。
- 启动恢复：`RecoverInterrupted` 把停留在 `pending` / `running` 的记录收敛为 `uncertain`，
  不重放代码获取，不删除容器与工作区。

### 当前能力边界

- 不提供 `uncertain` 记录的只读核验入口：当前只能由人工在容器内确认后显式处理。
- 不自动重试任一阶段，也不在失败后自动删除容器或重放外部操作。
- 同一任务在进程内以任务为键串行；跨任务并发由调度器按 `scheduler.max_concurrent_tasks` 领取 `queued`
  任务，跨进程仍由各阶段自身的条件更新与 `workflow_runs` 的部分唯一索引拒绝。

## SSH 与执行节点边界

SSH 探测、SSH 连接层（主机密钥校验、私钥认证、远程执行、SFTP、远端套接字）、执行节点环境预检与
远程工作空间规则集中在 [ssh-hosts.md](ssh-hosts.md)。本文件只约定其上下游关系：

- Agent 调用、容器管理与代码获取都经由已认证的 SSH 执行层访问远端 Docker，不开放 Docker TCP 端口，
  也不要求本机访问远端 Docker Socket。
- 任何使用持久化执行节点执行真实远程操作的入口都必须先确认 `validation_status` 为 `normal`；该限制
  由后端强制实施，不能只依赖界面禁用。

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
- Docker 版本与 API 基线、命令清单与容器管理约束集中在 [docker-compatibility.md](docker-compatibility.md)；
  新增 Docker 调用必须同步 `docker.RequiredCLICommands`，否则环境预检会漏检。

### 当前能力边界

- 只实现远程 Linux + Docker 容器执行环境；本机 Windows / macOS / Linux 执行环境尚未实现。
- 项目代码只通过 Git Clone 进入容器（`source.GitFetcher`）；wget、curl、SCP 等获取方式未实现。

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
- 认证范围：AgentFlow 不实现产品级私有仓库 Git 凭据管理（不做凭据存储、注入、轮换与撤销），
  这是已明确的产品边界而不是待实现能力。需要访问私有仓库时，由用户自行编辑并维护执行镜像配置，
  在该镜像内提供 Git 认证环境；该配置不属于 AgentFlow 管理的项目配置或任务配置，AgentFlow 也
  不对其认证方式、凭据安全性或访问成功作任何保证，更不建议把长期凭据明文写入 Dockerfile 或可
  复用的镜像层。仓库地址必须通过 `domain.ParseGitRepositoryURL` 校验，内嵌凭据
  （`user:token@host`）直接拒绝；Clone 输出显示需要认证时返回 `repository_auth_required`，
  不静默降级为匿名访问。
- 失败与中断：Clone 失败、基础分支缺失、分支冲突与工作区核验失败分别映射到不同错误类别；
  超时、取消与结果不确定分别记录为 `timed_out`、`cancelled`、`uncertain`，都不得判定为成功。
- 代码获取不负责 Push、Merge、容器生命周期与重试决策；失败不会删除容器或工作区。
- 后续接入 HTTP 下载、wget、curl 与 SCP 时只需新增 `source.Fetcher` 实现，并在
  `app.newContainerFetcher` 注册；服务层、持久化模型与调用方都不必改动。
- 初始化记录必须先于容器内命令写入：`pending` → `running` → 终态；成功记录必须带可核验的
  `commit_sha`，没有 commit 的"成功"不得落库。
- 归属校验：任务、步骤、尝试、容器与执行节点必须属于同一条任务链，并严格要求容器记录的
  `attempt_id` 与本请求一致；容器未登记尝试归属时返回前置条件错误，由环境准备阶段补写。
- 不新增 `source.GitCredentialProvider`、`codesaver.GitCredentialProvider` 的产品级凭据管理实现；这两个
  接口只是既有空占位，不得作为"待实现能力"的立项依据。
- 接口契约（请求参数与错误映射）见 [http-api.md](http-api.md)。

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
- 远程管理工作空间（默认 `~/.lingting/agentflow`）的解析与创建规则见 [ssh-hosts.md](ssh-hosts.md)。
- Session 文件、检查输出与执行记录一律落在本机持久化目录，容器删除后仍然可读。

## 安全边界

- HTTP 服务默认只监听 `127.0.0.1`，未实现远程访问认证前不得默认监听所有网卡，不开放任意来源 CORS。
- 不存储模型 API 密钥，凭据由 Pi 及其 Provider 管理。
- 角色权限采用最小化原则，实际权限通过 Pi 执行参数与执行环境实施，不能只靠提示词：
  `domain.EffectivePiTools` 按角色能力上限与角色策略计算工具列表，
  `app.ContainerService.RunAgent` 是唯一换算位置，Pi Runner 构造 `--tools <list>`（为空时
  `--no-tools`）并固定附加 `--no-mcp`、`--no-extensions`。规划角色与验收者的能力上限只有 `read`，
  配置无法突破；托管调用不加载 MCP 与扩展，也不修改用户的 Pi 配置与凭据。该限制不提供文件系统
  隔离，只读容器仍是规划阶段代码只读的最终边界。
