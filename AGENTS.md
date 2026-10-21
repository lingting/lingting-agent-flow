# AGENTS.md

本文件约束在本仓库内进行开发时的行为约定。通用开发规范（语言、权限、测试授权）由全局配置提供，此处只补充 AgentFlow 项目自身的约定。

## 项目定位

AgentFlow 是本地优先的 AI Coding Workflow Orchestrator。编排器由 Go 实现，负责角色配置、任务队列、并发调度、开发步骤、验证、失败回退与执行记录；Pi 只作为 Agent 执行引擎，通过独立子进程与 JSONL RPC 通信。

## 分层与依赖方向

依赖只能自上向下，不得反向：

```text
cmd → cli / httpapi → app → scheduler / workflow → agent / process / gitworktree / storage / events
                                                                    ↘ domain
```

- `internal/domain/` 是业务语义的唯一来源，只定义实体、状态与业务规则，禁止依赖数据库、HTTP、Agent 运行时、外部进程或任何第三方包。
- `internal/app/` 是应用服务层，是 CLI 与 HTTP 的共同入口。CLI 与 Web UI 不得各自实现工作流或状态迁移。
- `internal/workflow/` 负责 Plan / Develop / Verify / Retry 的状态迁移；`internal/scheduler/` 负责任务领取、并发控制与恢复。
- `internal/httpapi/` 只做协议转换与 SSE 推送，不承载业务规则。
- `web/ui/` 只负责展示与发起操作，不实现独立工作流逻辑。

## 领域模型约定

- `Requirement`、`Task`、`Step`、`Attempt`、`Session`、`VerificationResult` 是不同层次的概念，不得合并。
- Step 是业务对象，Attempt 是一次执行尝试，Session 是一次 Pi 会话；每次重试必须创建新的 Attempt 与新的 Session，并保留历史记录。
- 状态迁移统一在 `domain` 内通过 `Transition` 方法校验，非法迁移返回 `ErrInvalidTransition`。禁止在业务层直接赋值状态字段绕过校验。
- 实体字段校验失败返回 `*ValidationError`，可通过 `errors.Is(err, ErrInvalidValue)` 判断。
- `TaskStatus.IsTerminal` 只包含 `completed` 与 `cancelled`（允许显式重试的 `failed` 不是终态）；
  管理接口的删除判定使用 `TaskStatus.IsFinished`，它额外包含 `failed`。
- 逻辑删除使用 `deleted_at` 标记，读取层默认过滤已删除记录，只有显式 `IncludeDeleted`
  时才纳入查询；历史记录一律保留。

### 执行记录四层关系（Task → Step → Attempt → Session）

- 层级固定为 `Task → Step → Attempt → Session`：Task 是用户提交的整体任务，Step 是逻辑执行
  阶段（`plan` / `develop` / `verify` / `review` 等），Attempt 是同一步骤的一次执行尝试，
  Session 是一次独立的 Agent 调用会话。
- 每一次 Planner、Developer、Validator 调用都必须对应一个独立 Session，不得复用同一个会话
  记录或同一个外部会话标识。
- 每一次重试都必须创建新的 Attempt 与新 Session：不复用旧 Attempt、不覆盖旧尝试的错误与
  耗时、不复用旧容器、不复用旧 Agent Session，完整保留历史尝试。
- 执行状态只能由 Go 业务服务经 `domain` 的状态机方法推进；不得由 HTTP Handler、前端或
  Agent 直接赋值状态字段。创建 Step / Attempt 不会自动把 Task 推进到运行状态。
- 尝试序号由存储层在事务内分配（`storage.AttemptStore.StartAttempt`），禁止"先查最大值再
  无保护插入"。`NextAttemptNumber` 只用于展示与预估。
- 依赖条件的更新必须检查影响行数并返回 `storage.ErrConflict`：旧 Attempt 的迟到结果不得
  覆盖新状态，重复完成不得重新打开终态记录。
- 四层关联由应用层校验：Attempt 的 Task 必须与其 Step 的 Task 一致，Session 的 Task / Step /
  Attempt 必须属于同一条任务链。数据库不声明外键，孤立关联 ID 允许存在但不得被当作合法关系。
- Session 的执行状态、原始 JSONL 归档状态（`archive_status` / `archive_path`）与 HTML 导出
  状态（`export_status` / `session_html_path`）必须分别记录，不得合并为一个字段。
- Session 归档与远程容器生命周期分离：归档路径由服务端生成并保存到本机持久目录，容器删除
  后原始会话与归档仍必须可读；容器清理成败都不得删除 Session 归档。
- 客户端不得传入任意 Session 文件路径；路径一律由服务端生成与管理。
- 不得伪造执行结果：未调用真实执行器时不得把 Step / Attempt / Session 标记为 `running`、
  `completed` 或 `succeeded`，也不得把测试夹具数据写入真实开发数据库冒充执行历史。

## 架构硬性约束

- 数据库是唯一真相来源。关键状态迁移（任务领取、步骤开始、Session 启动、验证失败、重试、任务完成）都必须持久化，内存对象只能作为执行期缓存。
- 任务领取必须基于事务与原子状态迁移，禁止“先查询、后更新”的无保护流程。
- 不得假设 Pi 正常结束，必须处理启动失败、非零退出、RPC 解析失败、超时、取消、意外退出、输出流关闭与子进程未退出。
- 每个 Task 使用独立 Git Worktree；未经明确配置不得删除包含未提交代码的工作区。
- 角色权限采用最小化原则，实际权限通过 Agent 配置与执行环境实施，不能只靠提示词。
- HTTP 服务默认只监听 `127.0.0.1`，不存储模型凭据。
- 优先使用 Go 标准库；新增第三方依赖必须有明确用途。

## 代码约定

- 所有输出、注释与提交信息使用简体中文。
- 每个文件保持单一清晰职责；职责扩散时拆分，不按固定行数机械拆分。
- 项目未声明行宽时以每行不超过 120 字符为目标。
- 同一方法在调用链中连续出现两次及以上时，从第一次重复调用开始逐段换行。
- 完成修改后对改动的 Go 文件执行 `gofmt`，并确认 `go vet ./...` 无新增问题。

## 验证与测试

- 默认只做非测试验证：阅读代码、检查 `git diff`、`gofmt`、`go vet`、类型与引用检查。
- 未经用户明确要求，不运行测试、不创建测试文件、不编写临时验证脚本，也不通过“测试后删除”的方式绕过该限制。
- 数据库 schema 变更必须同时提供 `migrations/` 下的迁移脚本。

## 测试规范

以下规则是长期项目规范，任何新增或修改测试的工作都必须遵守。

### 目录

- 所有测试代码与测试入口必须放在根目录 `tests/` 下，按 `unit/`、`integration/local/`、
  `integration/pi/`、`integration/ssh/`、`integration/remote/`、`e2e/` 分层。
- 禁止在生产源码目录（`internal/`、`cmd/`）新增或保留测试文件。
- `tests/internal/testkit/` 只存放通用测试辅助代码，不存放产品实现，也不存放需要额外执行的验证脚本。
- `tests/internal/workflowfake/` 只提供工作流下层组件（容器、代码获取、Agent、命令、代码保存）
  的受控替身，不得在其中复制工作流决策逻辑；重试、判定与资源处置必须由真实工作流引擎执行。

### 独立入口与并发

- 每个测试目录都是独立的 Go 测试包，可用 `go test` 直接执行；成功判定与退出码由 Go
  测试框架负责，不得把结果判断委托给额外的 Shell、Python 或日志解析脚本。
- 每个测试入口必须自行完成初始化、执行被测操作、自动断言，并用非零退出码报告失败。
- 新测试默认必须支持并发：无共享状态的用例使用 `t.Parallel()`，不得依赖固定执行顺序，
  不得依赖其他测试预先创建的数据。
- 并发度由 `-p` 与 `-parallel` 控制，不得在测试代码中把全部用例强制串行化。

### 测试工作空间

- 每次测试入口启动时在 `os.TempDir()/lingting-agent-flow-tests/` 下创建随机根目录，
  每个用例使用独立子目录，并输出绝对路径 `TEST_ARTIFACT_DIR`。
- 数据库、WAL/SHM、Session、日志、配置与生成文件必须全部落在该工作空间内，不得写入
  真实项目目录、用户默认数据目录或其他用例的工作空间。
- 使用 `tests/internal/testkit` 提供的 `testkit.New(t)` 与 `testkit.Dir(t)`，
  不要用 `t.TempDir()`，也不要用 `t.Setenv` 切换全局数据目录；路径通过结构体、
  构造参数或子进程独立环境变量传入。
- 配置文件中的数据库与工作区路径必须是工作空间内的绝对路径。相对路径会按进程工作
  目录解析（`go test` 下即包目录），把 SQLite 文件写进仓库；配置模板中的路径一律使用
  占位符，由统一的辅助函数替换为绝对路径，并保留一个断言绝对路径的回归用例。
- 仓库忽略任意层级的 `.agentflow/` 目录，作为误用相对路径时的兜底；不得把运行期
  数据文件提交进版本库。
- 测试结束禁止删除工作空间与测试产物：不调用 `os.RemoveAll` 删除测试根目录，不使用会在
  结束时自动清理目录的临时目录 API，也不在 `t.Cleanup` 中删除目录。允许关闭数据库连接、
  终止测试启动的子进程、关闭监听端口等运行时资源。

### 真实外部依赖

- 本机没有 `pi` 时，只有依赖真实 Pi CLI 的用例输出 `WARN [SKIP]` 并跳过；Pi Runner
  的单元测试使用替身，必须继续执行。
- 真实 Pi 测试由 `tests/config/pi.yml` 的 `pi.enable` 显式开启，默认模型为 `group/free`；
  每次真实调用都必须显式传入模型，不得回退到 Pi CLI 配置中的默认付费模型。
- 真实 SSH 测试的目标统一来自 `tests/config/ssh.yml`（`ssh.host`、`ssh.user`、`ssh.port`、
  `ssh.private_key_path`、`ssh.known_hosts`），并由 `ssh.enable` 显式开启；未启用时输出
  警告并跳过，启用但必需目标缺失时输出警告并跳过，目标配置完整但连接、认证或主机密钥校验
  失败时必须让测试失败。测试配置禁止再通过 `AGENTFLOW_TEST_SSH_*` 等环境变量逐项读取。
- `ssh.known_hosts` 留空时，真实 SSH 测试必须复用本机用户 OpenSSH 的 `~/.ssh/known_hosts`
  与 `known_hosts2`，与产品默认行为一致；显式填写但文件不可读时直接让测试失败，不得静默
  回落到空信任库。来源解析统一由 `tests/internal/testkit` 的 `KnownHostsSources` 提供，
  各远程测试目录不得各自实现。
- 真实 SSH 测试写入的信任记录只允许落在测试工作空间，不得修改用户真实 known_hosts。
- 主机密钥未知的失败信息必须列出实际读取的 known_hosts 路径、可用的配置项与目标信任
  记录路径，使"配置写错"与"连接层缺陷"可以区分。
- 禁止关闭 SSH 主机密钥验证来规避测试失败，禁止自动接受未知主机密钥。
- 真实外部测试的远程操作只允许在随机生成的远程临时目录内进行。
- SSH 连接层的单元测试放在 `tests/unit/ssh/`，本机协议集成测试放在
  `tests/integration/local/ssh/`；两者都不得访问外网或依赖本机已安装的 SSH 服务。
- 本机集成测试的 SSH/SFTP 服务端只能由测试代码在进程内启动，不得要求用户预先启动服务，
  也不得用"配置解析正确"代替真实握手、认证与传输验证。
- 远程命令的取消与超时测试必须验证会话确实被关闭，不能只验证本地停止等待。
- 真实远程 Docker 集成测试必须把 `tests/config/docker.yml` 的 `docker.remote_enabled` 设为
  `true` 并同时启用 `ssh.enable`；未启用时输出 `WARN [SKIP]`，普通测试不得修改真实远程
  Docker。
- 真实远程 Docker 测试放在 `tests/integration/remote/docker/`，目标机器由
  `tests/config/ssh.yml` 提供；开关已启用但目标缺失时说明缺少的配置项并跳过，目标完整但
  认证失败、Docker 不可用或断言不成立时必须让测试失败。
- 真实远程 Docker 测试只允许操作带随机后缀、属于本次测试的镜像标签、容器与远程目录；
  禁止执行全局 `docker system prune`、`docker image prune`、`docker volume prune`。
- 真实远程 Agent 测试放在 `tests/integration/remote/agent/`，同样要求 `docker.remote_enabled`
  与 `ssh.enable` 都已启用；容器内 Pi CLI 是否可用必须通过容器内实际检查确认，不能依据本机
  是否安装 Pi 判断。测试镜像由该目录下的 `Dockerfile` 构建，基础镜像与 Pi CLI 版本都固定到
  具体版本。
- 真实远程工作流测试放在 `tests/integration/remote/workflow/`，同样要求
  `docker.remote_enabled` 与 `ssh.enable` 都已启用；只操作带随机后缀、属于本次测试的容器与
  远程目录。

### SSH 连接层约束

- 主机密钥校验不可关闭：`sshclient.NewDialer` 未收到 `HostKeyStore` 时直接失败，任何位置
  都不得引入 `InsecureIgnoreHostKey` 等价实现。
- 主机密钥类型协商必须按 known_hosts 中该主机已记录的类型逐个尝试握手，取第一个校验
  通过的结果；某个类型校验失败或服务端不提供该类型时换下一个。不得只把全部已记录类型
  一次性交给库协商：服务端会选中第一个共同算法，使"旧记录已失效、新记录仍有效"的主机
  直接失败。没有记录时才交给库默认协商。
- 逐类型重试只针对主机密钥类失败（未知、已变化、已吊销、无共同算法）；TCP 不可达、
  超时、取消与认证失败必须立即返回，不得继续换类型掩盖真实原因。
- 未知主机必须返回 `CodeHostKeyUnknown` 并给出算法与 SHA-256 指纹；密钥变化必须返回
  `CodeHostKeyMismatch` 并给出已信任记录，不得静默接受或自动覆盖。
- 信任记录只写入 AgentFlow 自己的 known_hosts 文件，用户 OpenSSH 的 known_hosts 只读。
- SSH 认证方式固定为无口令私钥：`domain.AuthMethod` 与 `sshclient.AuthMethod` 只有
  `private_key` 一个取值，节点配置必须给出 `private_key_path`。不得重新引入 SSH Agent、
  密码认证或私钥口令（passphrase）支持，也不得注册 `--password-stdin`、
  `--passphrase-stdin`、`--ask-pass` 等凭据输入参数。
- 私钥文件只被读取：不得复制到工作空间、写入数据库或日志，也不得修改其权限或内容。
  加密私钥必须返回明确的不支持错误，而不是挂起等待口令输入。
- 存量数据库中 `auth_method` 为 `agent` 或 `password` 的记录保留原值，不写迁移改写；
  这类节点在校验时因认证方式不受支持而判为 `abnormal`，由用户显式改成私钥。

### 执行节点环境预检约束

- 执行节点（`execution_hosts`）的校验状态只有 `checking`、`normal`、`abnormal` 三种，
  定义在 `domain.ValidationStatus`；单项结果使用 `domain.CheckStatus`，不得新增第二套
  总体状态名，也不得把 `checking` 显示为成功。
- 新建节点一律由服务端置为 `checking` 并提交后台校验，客户端不能指定
  `validation_status`、`validation_generation` 等校验字段。
- 任何影响连接目标、认证方式、私钥路径、远程工作空间或启用状态的修改都必须调用
  `InvalidateValidation`：状态回到 `checking`、清空旧摘要与详情、代次加一。
- 校验结果必须通过 `UpdateExecutionHostValidation` 以代次为条件写入；代次不一致时
  丢弃结果，不得覆盖新配置。`RecordValidationResult` 与 `FailValidation` 同样校验代次。
- 四项必需检查按顺序执行：SSH 连接 → Docker 版本与 API 兼容性 → Docker CLI 帮助信息 →
  基础 Dockerfile 的外部 `FROM` 镜像元数据。前一项失败时后续检查记为 `not_run`，不得伪装成已验证。
- 只有当前代次全部必需检查都通过才允许写入 `normal`；`skipped` 与 `not_run` 一律不算通过。
- 无法开始校验（调度失败、服务未运行等）时不得永久停留在 `checking`：要么保持可恢复的
  待校验状态并由启动恢复重新调度，要么收敛为 `abnormal` 并给出可读原因。
- `normal` 的含义仅限本方案实际执行的检查：Docker 兼容性基于实际 CLI / Engine / API 版本与
  CLI 帮助信息，不验证 Engine 的实际运行契约，因此不得把 `normal` 表述为"容器创建与运行已验证"。
- Docker CLI 帮助检查只允许执行 `docker <command> --help`：禁止在该检查中执行
  `create`、`run`、`start`、`pull` 等任何创建容器、启动工作负载或下载镜像层的命令。
  版本与 API 兼容性检查由独立的 `docker_version` 检查项通过只读的 `docker version` 与
  `/v1.52/version` 完成，帮助检查本身不得执行其他 Docker 操作。
- 帮助检查的命令与参数清单必须与 `internal/docker` 中真实的命令构造保持一致
  （`docker.RequiredCLICommands`）；新增 Docker 调用必须同步更新清单。
- 镜像预检只允许查询 `FROM` 镜像元数据（Docker Engine 的 Distribution Inspect），
  禁止 `docker pull`、完整 `docker build`、创建临时容器或运行镜像内程序，也不得改变
  远端镜像缓存、容器与卷状态。查询经由已认证 SSH 连接直接访问远端 Docker Engine 的
  Unix 套接字，不要求远端安装 curl 等额外工具，也不得用 `docker pull` 作为回退手段。
- 无法解析的 `FROM` 动态引用（例如依赖构建期参数的变量）必须判为失败并记录原因，
  不得跳过并判为成功；`scratch` 与内部阶段别名不产生网络查询。
- 校验超时集中定义在 `internal/docker/validation.go`：单步 SSH 检查、单镜像查询与整体
  时限都不得分散写入 Handler 或业务方法，且整体时限必须大于单步时限。
- 后台校验作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`：由统一 Server
  启动与停止，不得在 HTTP Handler 内创建不受管理的长期 goroutine。停止时必须取消在途
  校验并等待其释放 SSH 连接；启动时必须重新调度停留在 `checking` 的记录。
- 任何使用持久化执行节点执行真实远程操作的入口都必须经过
  `app.EnsureHostUsableForRemoteTask`（`withRemoteSession` 已统一调用）；不得只依赖
  Web UI 禁用按钮。只有校验本身与只读预检允许在节点未通过校验时执行。
- 校验错误与日志必须截断并脱敏：不得写入密码、令牌、私钥内容、认证头或完整环境变量。

### 远程执行与 Session 约束

- Pi 运行在任务容器内部，不得静默回退到本机执行；本机 Runner 收到容器目标时必须直接失败。
- 每次 Agent 调用使用独立的 invocation ID 与独立的 Session 文件，禁止复用同一个 Session。
- 原始 JSONL 必须复制到本机 AgentFlow 数据目录并独立于容器生命周期；容器删除后仍可读。
- 不得把 Session 仅保存在容器内部、远程 AgentFlow 配置目录或远程项目代码目录。
- 不得把 Session 内容写入本机真实项目目录，也不得把完整 Session 打印到日志或控制台。
- HTML 导出失败只降级导出状态，不得删除或覆盖已经保存的原始 JSONL。
- 项目代码只能克隆到容器内部工作目录，不得克隆到远程宿主机项目配置目录，也不得通过
  SCP/SFTP 上传到宿主机后再 bind mount 进容器。
- SourceProvider 只负责获取代码与初始化任务分支，不负责 Push、Merge 或删除容器。
- Runner 不负责工作流重试决策，也不决定整个任务的成功清理策略。
- 所有项目命令（代码拉取、Agent 开发、验证检查）都必须经由统一执行环境接口
  （`execenv.Provider` / `execenv.ExecPort`）在容器内工作区执行；不得在宿主机当前目录
  隐式执行任务代码，也不得在容器不可用时退化到本机。
- 容器生命周期状态与任务执行状态分别记录：不得仅凭 Task 状态推断 Docker 容器一定存在，
  也不得用容器状态推断任务是否成功。

### 统一 Pi Runner 与 Session 生命周期约束

- 所有 Planner、Developer、Validator 调用都必须经由 `app.AgentRunnerService`（本机）或
  `ContainerService.RunAgent`（容器）发起，并各自对应一条独立的 Session 记录；禁止在
  CLI、HTTP Handler 或工作流代码里自行拼接 Pi 命令行。
- Pi CLI 只使用经过本机实测支持的非交互调用方式：`pi --print --session <path> --model <id>`，
  Prompt 通过标准输入写入并在写入后立即关闭。不得启用 RPC 模式，也不得依赖伪终端。
- 每次调用使用明确且唯一的 Session 文件路径；路径由服务端按任务 / 步骤 / 尝试 / 会话 /
  调用标识分层生成，并校验落在 Session 根目录之内。调用方不得指定任意本地路径。
- 不得使用 `--no-session` 关闭会话保存；每个角色、每次 Attempt 都使用新的 Session 文件。
- Session 记录必须在 Pi 进程启动之前写入数据库并推进到 `running`；进程启动失败、工作目录
  不可用等路径必须把该记录收敛为 `failed`，不得残留 `running`。
- stdout、stderr 与进程退出状态必须分别处理：两个流分别写入 `stdout.log` 与 `stderr.log`，
  日志有字节上限，截断时必须在文件末尾留下明确标记；数据库只保存路径、状态与摘要。
- 超时与取消必须传播到实际执行进程，并在宽限期后强制终止仍不退出者；两者与"Server 关闭
  导致的中断"必须能区分。Server 停止时取消在途调用并等待收敛，不留不受管理的子进程。
- 原始 JSONL 归档状态（`archive_status` / `archive_path`）、HTML 导出状态（`export_status` /
  `session_html_path`）与执行状态（`status` / `run_status`）必须分别持久化，不得合并。
- 归档只有在本机确实拿到可读原始文件时才记为成功；不得把不存在的文件标记为已归档。
  HTML 导出只能使用 Pi 官方 `--export`，导出失败不得删除原始 JSONL，也不得把成功的执行
  改写为失败。
- Session 文件必须保存在 AgentFlow 本机持久化数据目录（默认 `<数据库目录>/sessions`），
  或在清理远程执行环境之前完成可靠回传；容器删除后归档仍必须可读。
- 不默认禁用用户扩展：不得为了规避非交互兼容问题而自动追加 `--no-extensions`、
  `--no-skills` 等参数。扩展阻塞在交互输入时，由超时或取消结束并给出明确诊断。
- 会话内容默认只在本机持久化，不上传到任何外部存储服务。
- 真实模型测试必须显式传入模型：默认 `group/free`，只有显式配置后才允许使用其他模型；
  不得回退到 Pi CLI 配置中的默认（可能付费的）模型。
- 测试产物（数据库、Session、日志、模拟可执行文件）必须留在随机 OS 临时目录的工作空间中，
  不得写入真实项目目录与其他用例的工作空间，也不在测试结束时删除。


### 统一执行环境约束

- 执行环境契约固定在 `internal/execenv`（`Kind`、`Workspace`、`ManagementWorkspace`、
  `Environment`、`ExecSpec`、`ExecResult`、`PrepareSpec`、`Prepared`、`ReleaseRequest`、
  `ReleaseResult`、`Provider`、`ExecPort`）。该包只放纯类型与纯函数，不得依赖 Docker、
  SSH、SQLite 或外部进程。
- 远程 Linux + Docker 容器实现固定为 `app.ExecutionEnvironmentService`
  （`internal/app/execenv*.go`）。CLI、HTTP Handler、Pi Runner 与验证引擎都不得自行
  拼接 Docker 命令，也不得自行创建或释放容器。
- 未实现的执行环境类型（`local_linux`、`local_darwin`、`local_windows`）必须返回
  `execenv.ErrUnsupportedKind`，不得静默回退到本机执行，也不得在宿主机当前目录执行
  任务代码。
- 工作区边界必须显式区分：容器内项目代码目录固定为 `/workspace/project`
  （`execenv.DefaultProjectDir`），容器内临时 Session 目录默认为
  `/tmp/agentflow/sessions`；远程管理工作区（默认 `~/.lingting/agentflow`）只存放
  项目配置、Dockerfile 与镜像构建资源，不得存放项目源代码。
- Session 目录不得位于项目代码目录之内；缓存卷挂载不得落在 `/workspace` 之下。
- 容器准备固定分四个阶段：校验机器与项目配置 → 准备镜像 → 创建任务容器 →
  验证容器可用性。失败结果必须保留已执行阶段的记录，使失败原因可定位。
- 每次准备都创建全新的容器，不复用旧容器；容器创建失败时不得在数据库中留下
  "已登记但实际不存在"的可用容器引用。
- 命令执行一律使用可执行文件 + 参数数组，禁止拼接 shell 字符串；工作目录必须是
  容器内 `/workspace` 之下的安全绝对路径；环境变量只能显式声明，执行层不继承
  宿主机或服务进程环境。
- 执行结果必须分别表达退出码、标准输出、标准错误、结束状态与"是否可确认"：
  超时、取消、执行通道不可用与结果无法确认都不得判定为成功。
- 容器生命周期与任务状态分离：`execenv.Retention`（keep / delete / detach）只表达
  资源处置，处置结论由 `execenv.DecideRetention` 依据既有
  `domain.DecideContainerLifecycle` 计算，不得在别处复制第二套规则。
- 已脱离管理的容器不得被自动删除、启动、停止或重新接管；容器已不存在时删除按
  成功处理。
- 任务与执行环境的关联规则集中在 `ExecutionEnvironmentService.PrepareForTask`：
  执行节点、语言环境、环境版本、缓存与工作目录全部来自任务记录，尝试标识必须
  属于该任务；准备成功后才把容器登记记录写回任务。
- Pi Runner 的工作目录与容器内 Session 目录必须来自执行环境描述
  （`ContainerAgentRequest.WorkDir` / `SessionDir`），不得在 Runner 内硬编码另一套路径。
- 验证引擎必须经由 `execenv.ExecPort` 在对应 Attempt 的容器工作区执行检查命令；
  容器缺失、未运行或节点未通过预检时返回前置条件错误，不得回退到本机执行。
- Session 文件、检查输出与执行记录一律落在本机持久化目录，不依赖容器继续存在；
  容器删除不得导致已要求保留的 Session 与执行记录丢失。
- 所有 Docker 命令与参数仍必须遵守既定兼容性基线（CLI 29.0.0、Engine 29.0.0、
  API 1.52）；新增 `docker exec` 参数（例如 `--env`）必须同步更新
  `docker.RequiredCLICommands`，否则环境预检会漏检。
### 工作流引擎约束

- Task、Step、Attempt 的状态迁移只能通过 `domain` 的状态机方法完成；禁止在业务层直接
  赋值状态字段绕过校验，条件更新必须使用 `UpdateTaskIfStatus` 等带状态前置条件的方法。
- 只有 `internal/workflow/` 可以依据持久化状态、验证结果与代码保存结果把 Task 推进到最终
  状态；Pi 不更新任务状态，也不决定是否重试。
- 每次 Planner、Developer、Validator 调用必须创建新的 Attempt（开发步骤）与独立的
  Invocation / Session；重试不得复用旧记录。
- 必需确定性验证命令必须先执行，再按需调用 Validator Agent；Agent 返回 `passed: true`
  不能覆盖必需检查的失败或未执行。
- 失败任务重新开始时必须创建新容器，旧容器只能由用户显式选择 `delete` 或 `detach`；
  不得迁移旧容器中的未推送修改。
- 已 detached 的容器不得被自动启动、停止、删除或重新接管。
- 成功清理只删除受管理的任务容器与任务级临时资源；共享镜像、共享缓存卷、远程项目配置
  目录与 Session 一律保留。
- Commit、Push 与 Merge 状态必须分别持久化；`merge_after_push` 默认关闭，Push 结果未知时
  必须先核验远程分支再决定状态。

### 单步骤执行协调器约束

- 单步骤执行入口固定为 `app.StepExecutor`（`internal/app/step_executor*.go`）：HTTP Handler、
  CLI 与工作流都不得自行拼接 Pi 命令、自行创建 Attempt 或直接写执行状态。
- 执行是同步的：只有 Attempt 进入终态后才返回结果；不得返回"已受理但仍在后台"的语义，
  也不得在 Handler 内启动不受生命周期管理的 goroutine 或子进程。
- 执行前必须校验任务、步骤、角色的归属与状态，并核验任务容器受管理、未 detached 且处于
  运行中；容器缺失或节点未通过预检时返回明确的前置条件错误，不得静默回退到本机执行，
  也不得在执行路径上创建、启动或接管容器。
- 任务的准入状态固定为可合法迁移到 `developing` 的集合（`preparing`、`planned`、
  `developing`、`retrying`、`paused`、`recovery_required`）；`queued` 必须先完成环境准备与
  计划，`cancelling` 与已结束状态一律拒绝。该规则只在 `domain` 中定义，不得在别处复制。
- 步骤占用必须通过 `storage.StepExecutionStore.BeginStepExecution` 在单个事务内完成：先以
  状态为条件写入 Attempt，再条件更新 Step 与 Task。禁止"先查询、后无条件更新"，也不得只
  依赖进程内互斥锁保证一致性；冲突一律返回 `storage.ErrConflict` 并映射为 409。
- 同一步骤同一时刻只允许一个有效执行占用：并发请求、重复请求与客户端超时后的重试都必须
  返回状态冲突，不得启动第二个 Pi 进程。
- 每次执行必须创建新的 Attempt 与独立的 Session；重试不得复用旧记录。Session 文件路径由
  服务端生成，调用方不得指定。
- 执行结论只由真实结果决定：`completed` 要求进程真实运行并正常退出，缺少可核验运行痕迹时
  按失败处理；超时、取消与中断分别映射为 `timed_out`、`cancelled`、`interrupted`，不得把
  任何一种当作成功。
- 取消与超时必须传播到实际 Pi 进程；用户取消按状态机把 Step 收敛为 `cancelled`，并把任务
  经 `cancelling` 收敛为 `cancelled`；服务关闭导致的中断映射为 `interrupted`，与用户取消
  区分记录。
- 单个步骤成功不得推进任务到 `completed`：任务保持 `developing`，终态由后续确定性验证与
  代码保存阶段决定。单个步骤失败不得覆盖其他步骤的历史结果。
- 最终状态写入必须使用未取消的上下文并分别执行：Attempt、Step、Task 三条写入互不阻断，
  任一失败都要记录到结果的 `FinalizationErrors`，接口不得把"写入失败"报告为成功。
- 迟到结果不得覆盖已确认的终态：所有终态写入都必须带状态前置条件，冲突时读取数据库真实值。
- 执行事件（开始、完成、失败、中断）写入任务时间线；事件写入失败只记录日志，不回滚执行
  状态，也不删除已持久化的执行记录。
- 步骤执行接口固定为 `POST /api/v1/tasks/{taskID}/steps/{stepID}/execute`，只接受执行参数
  （可选补充提示词与超时），不接受任意 Session 路径、工作目录、命令行参数或角色覆盖。
  错误映射：参数错误 400、目标不存在或归属不符 404、状态冲突 409、前置条件缺失 412、
  执行发生但未成功 500。
- 本阶段不实现多步骤自动编排、自动重试、远程容器创建、Git 操作与执行队列；相关能力缺失
  时必须返回明确错误，不得伪装成已可用。

### 确定性验证引擎（Validator）约束

- 验证入口固定为 `app.ValidationService`（`internal/app/validation*.go`）：HTTP Handler、CLI 与
  工作流都不得自行执行检查命令、自行创建 Validation Run 或直接写验证结论。
- 验证流程状态（`validation_runs.status`：`pending` / `running` / `completed` / `failed` /
  `cancelled` / `timed_out`）与最终结论（`validation_runs.verdict`：`passed` / `failed` /
  `inconclusive`）必须分别持久化，不得合并为一个字段，也不得由状态推导结论。
- 最终结论只能由 `domain.DecideValidation` 这一纯函数依据结构化检查结果计算：禁止由 Pi Agent、
  HTTP Handler 或前端决定结论。规则至少包含：
  - 验证流程必须处于 `completed`，取消、超时与流程失败一律不判通过；
  - 至少存在一项启用的必需检查；
  - 每一项启用的必需检查都必须有结果且状态为 `passed`；
  - 必需检查非零退出码、超时、取消、跳过、启动失败与结果缺失都判 `failed`；
  - 可选检查失败不影响结论，但必须保留结果；
  - 全部检查被跳过或没有启用的必需检查时不得判定通过。
- Agent 辅助评估只能收紧结论：Agent 声称通过不得覆盖必需检查的失败，也不得替代缺失的必需
  检查。本阶段不发起 Validator Session 调用，结论计算函数已按该约束实现并可被单独测试。
- 命令检查必须以可执行文件 + 参数数组表达，禁止拼接 shell 字符串，也不得把所有命令交给
  `sh -c` / `bash -c`。检查命令只在受管理的任务容器内、容器内工作区（`/workspace` 之下）执行；
  容器缺失、未运行、已 detached 或节点未通过预检时返回明确的前置条件错误，不得回退到服务进程
  的当前目录执行。
- 检查执行必须设置超时、响应 context 取消、限制输出字节数，并分别捕获标准输出与标准错误。
  执行结果必须区分"命令以非零码正常结束"与"启动失败、超时、取消、结果无法确认"，不得压缩成
  单个布尔值。
- 检查结果逐项写入（`storage.ValidationStore.AppendValidationCheckResult`）：部分检查完成后
  发生错误时，已产生的证据必须仍可追溯，不得因最后一项失败而整体丢失。
- 同一 Attempt 同一时刻只允许一个有效验证：占用必须通过
  `storage.ValidationStore.BeginValidationRun` 在单个事务内以状态为条件完成，冲突返回
  `storage.ErrConflict` 并映射为 409；禁止只依赖进程内互斥锁。
- 验证结论写回 Attempt 必须使用带状态前置条件的更新；迟到结果不得覆盖已确认的终态。
- 结论或检查结果写入失败时，接口不得报告验证成功：结果必须携带 `Persisted=false` 与
  `PersistenceErrors`，HTTP 映射为 `execution_not_persisted`（500）。
- 检查输出落盘失败（`output_archive_error`）与检查判定相互独立：归档失败不得改写退出码或
  检查状态，必须单独记录。
- 验证服务作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`：Server 停止时取消
  在途验证并等待收敛，把记录推进到失败终态，不留 `running` 残留。
- 验证接口固定为 `POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate`，
  只接受验证参数（可选超时与检查覆盖配置），不接受工作目录、任意命令行或 Session 路径。
  错误映射：参数错误 400、目标不存在或归属不符 404、状态冲突 409、前置条件缺失 412。
- 检查覆盖配置不得放宽门槛：覆盖后仍要求存在启用的必需检查，并执行同一套结论规则。
- 本阶段不实现完整 Planner → Developer → Validator 编排、自动重试、任务级调度队列与
  Docker 镜像构建；相关能力缺失时必须返回明确错误，不得伪装成已可用。

### 任务管理 API 约束

- 任务状态只能由业务层状态机修改：`internal/domain` 的 `Transition`、`RequestCancel`、
  `CancelImmediately`、`MarkDeleted` 是唯一入口；HTTP Handler 不得直接赋值 `status`、
  `deleted_at`、`started_at`、`finished_at`，也不得接受客户端指定这些字段。
- 任务状态取值完全复用 `domain.TaskStatus`，不新增第二套对外状态名，也不把中间状态压平为
  `succeeded` / `failed`；管理接口只做数据管理，不代表任务已经执行。
- 取消语义按状态区分：`queued` 任务可直接收敛为 `cancelled`；其他未结束状态只记录取消请求
  并进入 `cancelling`，实际停止由执行者确认，不得伪造"运行中任务已停止"。
- 取消请求必须幂等：重复请求返回当前状态，不重复改写 `cancel_requested_at`。
- 删除采用逻辑删除：只写 `tasks.deleted_at`，不物理删除任务行，不级联删除需求、步骤、
  尝试、会话与事件；只有 `completed`、`failed`、`cancelled` 允许删除。
- 状态更新必须使用带状态前置条件的更新：`UpdateTaskIfStatus`、`DeleteTask` 以读取到的
  原始状态作为条件，禁止"先查询、后无条件更新"。
- 任务列表必须分页：默认页大小 20，上限 100，超过上限返回参数错误而不是读取全表；
  响应固定返回 `records` / `total` / `current` / `size`。
- 任务接口路径固定为 `/api/v1/tasks`，错误响应使用统一结构
  `{"error":{"code","message","request_id"}}`；参数错误 400、不存在 404、状态冲突 409，
  不得一律返回 200。

### 代码保存（CodeSaver）约束

- CodeSaver 只操作当前受管理容器内、经过核验的任务 Git 工作区；不得把容器内代码复制
  到宿主机或本机目录后再提交。
- 任务分支统一由 `domain.TaskBranchName` 生成并校验，固定为 `agent/<task-id>`；
  SourceProvider 与 CodeSaver 不得各自拼接分支名。
- 容器已停止、已删除或已 detached 时直接报错，不得为保存代码而启动或重新接管容器。
- 工作区没有变更时记录 `no_changes`，禁止创建空提交。
- 提交、推送与合并状态必须分别持久化到 `code_saves`，不得用单一 `saved` 布尔字段代替。
- 禁止默认 Force Push（`--force`、`--force-with-lease`）；远程历史冲突一律作为
  `conflict` 返回，不得覆盖他人提交。
- 推送结果不确定时必须先核验远程分支实际 commit，再决定状态；状态确认前禁止重试。
- 合并默认关闭（`merge_after_push = false`），只有任务配置或调用参数显式授权时才执行；
  开启代码保存本身不代表同意合并主分支。
- 合并出现冲突时不得自动选择 ours 或 theirs，不得伪造成功合并，也不得重置整个仓库。
- Push 成功而 Merge 失败时，任务分支的保存状态仍为成功；只有任务分支推送成功才算
  "已安全保存到远程"。
- 禁止把 Git Token、密码、私钥写入日志、SQLite 普通字段、仓库 URL、Dockerfile 或镜像层；
  凭据只允许在单次操作期间以内存形式存在。
- CodeSaver 不负责容器生命周期、任务调度与重试决策，也不包含模型调用。

### 代码获取（CodeFetcher）约束

- 代码获取必须通过统一抽象 `source.Fetcher`（`internal/source/fetcher.go`）：第一版只注册
  Git Clone 实现（`GitFetcher`）。新增 HTTP 下载、wget、curl、SCP 等方式只能新增实现并在
  `app.newContainerFetcher` 注册，不得在服务层、Handler 或 CLI 中另行拼接获取命令。
- 任务代码只允许存放于对应 Attempt 的任务容器工作区（`/workspace/project`，由
  `domain.DefaultContainerWorkDir` 与任务配置决定）；不得拉取到 AgentFlow 管理工作区、
  本机数据目录或宿主机目录，也不得在容器不可用时回退到本机执行。
- 任务分支统一为 `agent/<task-id>`，由 `domain.TaskBranchName` 生成与校验；代码获取与
  代码保存不得各自拼接分支名。分支已存在时只检出，不强制重置或覆盖。
- 不允许初始化时覆盖已有代码：目标目录非空即失败，同一 Attempt 已经初始化成功时写入
  `skipped` 记录并复用原 commit，不得重新 Clone。
- Clone、基础分支识别与分支切换失败必须记录真实原因：失败阶段（`stage`）、错误类别
  （`error_code`）与脱敏摘要（`error_summary`）分别持久化到 `code_fetches`，不得只记录
  "失败"。
- 初始化记录必须先于容器内命令写入：`pending` → `running` → 终态；成功记录必须带可核验的
  `commit_sha`，没有 commit 的"成功"不得落库。
- 代码初始化必须校验任务、步骤、尝试、容器与执行节点归属，并严格要求容器记录的
  `attempt_id` 与本请求一致；容器未登记尝试归属时返回前置条件错误，由环境准备阶段补写。
- 凭据不得泄露到 Dockerfile、Git URL、普通日志、数据库普通字段或测试产物；仓库地址必须
  经 `domain.ParseGitRepositoryURL` 脱敏，内嵌凭据的地址直接拒绝。第一版不支持私有仓库
  认证，需要认证时返回 `repository_auth_required`，不得静默降级为匿名访问。
- 取消与超时必须传播到实际 Git 进程并分别记录为 `cancelled` / `timed_out`；执行结果无法
  确认时记为 `uncertain`，不得判定为成功。
- 代码获取不负责 Push、Merge、容器生命周期与重试决策；失败不得自动删除容器，也不得自动
  无限重试。
- 所有 Docker 命令与参数仍必须遵守既定兼容性基线（CLI 29.0.0、Engine 29.0.0、API 1.52）。
### 结果报告

- 修改测试相关基础设施后，必须运行与修改范围相关的测试，并如实报告通过、失败、跳过与
  未执行的测试；不得把未执行、被跳过或失败的测试报告为通过。
- 测试产物路径（`TEST_ARTIFACT_DIR`）与远程测试资源位置必须在最终报告中列出。

## 数据库约束规范

以下规则是长期约束，任何新增或修改表结构、迁移脚本、数据访问层的工作都必须遵守。

- 业务数据库中的枚举式字段不得通过 `CHECK` 等数据库约束限定固定取值集合；新增取值
  必须能在不修改 Schema 的情况下写入。
- 禁止在业务 Schema 中声明外键；不得新增列级 `REFERENCES` 或表级 `FOREIGN KEY`，
  也不得使用 `ON DELETE` / `ON UPDATE` 级联行为。
- 不得通过触发器模拟外键引用完整性，也不得用触发器重新实施枚举值白名单。
- 普通关联 ID（`task_id`、`step_id`、`attempt_id`、`session_id`、`host_id`、
  `container_id` 等）仍作为普通文本保存，并保留索引以加速关联查询；允许出现指向已
  不存在记录的孤立 ID。
- 主键、`NOT NULL`、有明确业务意义的 `UNIQUE`、非枚举性质的 `CHECK`（数值范围、
  长度、JSON 结构、跨字段组合一致性）与合理默认值仍需逐项审核后保留，不得机械删除。
- 引用完整性、状态转换与生命周期规则由应用层显式承担，不得重新引入数据库外键或枚举
  白名单；数据读取层必须保留未知枚举值，不得自动映射为默认状态。
- 逻辑删除优先：`roles`、`execution_hosts` 与 `tasks` 使用 `deleted_at` 标记删除，
  删除操作不得级联删除历史记录。
- SQLite 变更必须使用增量迁移：已发布脚本不得修改，schema 变更只能追加更高版本的脚本，
  且优先使用 `ALTER TABLE` 等非破坏性语句，不重建无关表。
- 迁移已有数据库必须保留数据，禁止删除数据库重建；表重建必须保留必要的索引、触发器、
  视图与自增主键行为。
- 数据库连接显式关闭外键检查（`foreign_keys(0)`），业务 Schema 本身不得依赖该开关。
- 迁移必须在事务内完成，失败时回滚且不得写入版本记录；并发打开同一数据库只允许执行
  一次迁移。
- 执行记录表沿用同一套约束：`steps`、`attempts`、`sessions` 不声明外键，`step_type`、
  `status`、`run_status`、`completeness`、`export_status`、`archive_status` 等枚举式字段
  不得设置取值白名单，未知取值必须能写入并原样读回。
- 新增列的迁移必须可重复执行且只读取既有表：`attempts.task_id` 的历史回填通过
  `steps.task_id` 完成，步骤已不存在的孤立记录保持空值，不得因此失败或删除记录。
- 数据库相关测试集中在 `tests/`，必须覆盖 Schema 审计、旧库升级、数据保留、未知枚举
  值与并发访问，且不得把未执行、失败或跳过的测试报告为通过。

## Docker 与容器管理约束

- 远程 Docker 操作只能通过已验证的 SSH 执行层进行，不开放 Docker TCP 端口，不创建
  反向隧道，也不要求本机访问远程 Docker Socket。
- 禁止把 Docker Socket、用户 Home、SSH 私钥或远程项目配置目录挂载到任务容器。
- 项目代码只存在于任务容器内部（`/workspace/project`），不得绑定挂载到宿主机目录，也不
  得写入远程项目配置目录。
- 环境 Dockerfile 的历史版本不得覆盖；每次保存只能生成新的递增版本号。
- 删除任务容器不得删除共享镜像、共享缓存卷或远程项目配置目录；默认禁止全局 prune。
- 失败任务与默认中断任务必须保留容器；重新开始必须创建新容器，旧容器只能显式
  `delete` 或 `detach`。
- Detached 容器不得被 AgentFlow 自动重新接管，也不得被自动启动、停止或删除。
- 只有构建成功并核实到实际镜像 ID 的版本才能用于创建容器；构建失败不得标记为可用。

### Docker 兼容性基线（强制规则）

1. Docker CLI 最低支持版本为 29.0.0。
2. Docker Engine 最低支持版本为 29.0.0。
3. AgentFlow 使用的 Docker Engine API 基准版本固定为 1.52。
4. 所有新增 Docker 命令、子命令和参数，都必须以 Docker CLI 29.0.0 为兼容性基准。
5. 所有新增 Docker Engine API 路由、请求字段、响应字段和行为，都必须以 API 1.52 为兼容性基准。
6. 不得因开发环境安装了较新 Docker 而依赖更高版本才支持的功能。
7. 高版本 Docker 可以使用，但 AgentFlow 的实际命令和 API 使用必须保持兼容最低基线。
8. 所有 Docker 调用必须经过项目统一的命令执行或 API 访问入口；禁止在业务代码中随意拼接并执行未经过兼容性审查的 Docker 命令。
9. AgentFlow 发起的 Docker CLI 操作必须统一使用 API 基准 1.52；版本发现和兼容性探测阶段除外，且必须明确控制 DOCKER_API_VERSION 环境变量。
10. 直接调用 Docker Engine API 时，必须使用 /v1.52/ 路径，不得擅自使用更高 API 版本。
11. SSHConfig 的 Docker 兼容性预检必须同时检查实际使用的 CLI 版本、Engine 版本和 API 版本信息。
12. Docker CLI --help 只能用于辅助检查命令和参数，不得作为 Engine 实际运行能力的证明。
13. Docker 兼容性预检第一版禁止执行 docker create、docker run、docker pull 等会创建容器、启动工作负载或下载完整镜像层的操作。
14. 基础 FROM 镜像检查只查询镜像元数据，不下载镜像层。
15. 所有 Docker 命令新增、修改和移除都必须检查对最低基线的兼容性，并通过对应测试。
16. 不得为了让单个测试通过而私自提高最低版本要求或依赖高版本专属参数。
17. 修改最低支持版本或 API 基准必须经过明确的架构决策，并同步更新规则、代码、测试和文档。
18. 未真实运行的 Docker 测试不得报告为通过；跳过测试必须说明原因。

#### 基线的落地位置

- 基线取值只在 `internal/docker/compatibility.go` 中定义（`MinimumCLIVersion`、
  `MinimumEngineVersion`、`EngineAPIBaseVersion`），规则文件、代码、测试与文档必须完全一致。
- 版本发现与业务调用的环境变量策略必须分离：探测阶段用 `env -u DOCKER_API_VERSION` 隔离遗留变量，
  业务调用统一显式设置 `DOCKER_API_VERSION=1.52`；不得修改远端用户 Shell 配置来实现固定版本。
- 固定 API 版本与 Engine 不兼容时必须返回明确错误，不得静默降级到旧 API 或升级到新 API。
- 区分 Engine 声明的最高 API 版本与最低接受版本：基准必须落在 `[MinAPIVersion, APIVersion]` 区间内，
  不得把最低接受版本与基准做"大于等于"比较。
- 不得在业务代码中直接调用 `docker` 命令：`internal/docker` 之外禁止构造 Docker 命令行，
  `internal/docker` 内部也必须经由 `Runner` 的统一执行入口。

## 前端工程约束

- 前端根目录固定为仓库根目录下的 `lingting-agent-flow-ui/`，与 Go 代码不共享模块。
- UI 组件库固定为 `lingting-react-ui@2026.10.0`，必须精确锁定版本；不得升级、降级或替换为
  其他版本，也不得绕开该库另建一套 UI 设计体系。
- 依赖管理与构建工具固定为 pnpm 与 Vite+（`vp dev` / `vp build` / `vp lint` / `vp test`）。
- 组件选择优先级为 `lingting-react-ui` → Ant Design Pro Components → Ant Design。Ant Design
  组件必须在确认前两级没有等效能力后才直接使用。
- 参考项目为 `lingting-ai-gateway`（本地 `D:\code\lingting\lingting-ai-gateway\lingting-ai-gateway-ui`）。
  重要布局、路由与导航实现必须先阅读其真实源码，再决定借鉴点；不得只按文档推测，也不得复制
  只对其自身生效的别名、SDK workspace、代理端口与构建模式。
- 导航、页面标题与当前路由高亮必须由同一份路由元数据派生，禁止在多处重复定义路径与标题。
- 前端不直接访问 SQLite，不执行 SSH、Docker、Git 或 Pi 命令，也不承载工作流状态迁移。
- 不得使用虚构数据伪装真实业务状态，不得渲染会使用户误以为业务功能已可用的操作入口。
- 任务页面的状态标签必须由 `src/pages/tasks/status.ts` 的单一映射派生，取值与 Go 端
  `domain.TaskStatus` 一一对应；禁止新增前端状态名，也禁止把 `queued` 或中间状态显示为
  成功或失败。
- 任务页面的操作可用性必须依据接口返回的 `status_finished` 判定：已停止执行的任务
  （`completed`、`failed`、`cancelled`）不可取消但可以删除，其余状态可以取消但不可删除；
  `status_terminal` 只表达"是否终态"（`completed`、`cancelled`），不得用它代替删除判定。
  不得在前端伪造状态或跳过服务端校验。
- 任务列表必须使用 `lingting-react-ui` 的 `ExTable` 承载分页、筛选与加载状态，不得用
  原生 `<table>` 或手写分页重新实现一套表格。
- 所有前端测试位于 `lingting-agent-flow-ui/tests/`，测试产物写入操作系统临时目录下的随机
  唯一目录并保留，不得自动清理。
- 不得声称未运行的测试已经通过。

## Web 连接层与单文件交付约束

- 开发模式保留两个进程：Vite+ 开发服务器负责页面热更新，Go HTTP 服务负责 API；Vite+ 的
  `/api` 代理只作用于开发环境，代理目标通过 `AGENTFLOW_DEV_API_TARGET` 或开发配置设置。
- 生产模式只交付一个 Go 可执行文件：API 与前端静态资源由同一进程提供，前端构建产物通过
  `//go:embed` 嵌入，禁止要求用户在发布后手工复制 HTML、CSS 或 JavaScript。
- 前端源码固定使用同源相对 API 路径（例如 `/api/v1/health`），禁止在生产构建中硬编码
  `localhost`、开发端口、局域网 IP 或后端完整 URL。
- 所有前端 API 请求必须经由 `lingting-agent-flow-ui/src/lib/api/` 的统一客户端发出，页面不得
  直接调用 `fetch`，也不得各自解析 JSON 或错误。
- 前端构建输出目录固定为 `internal/webui/dist/`；该目录必须保留 `.gitkeep`，使全新检出的
  仓库仍满足 `go:embed` 的编译要求。缺少真实构建产物时不得静默返回空白页面。
- 仓库根目录 `Makefile` 的 `release` 是唯一推荐的单文件生产构建入口，必须先构建前端再编译
  Go；禁止先编译 Go 再构建前端，否则二进制中会残留旧 UI。
- 前端连接状态只能来自真实健康检查响应，禁止使用固定假数据；首次检查完成前不得显示已连接。
  连接指示器只表示 AgentFlow HTTP API 的可用性，不得表达为 SSH、Docker、远程机器或 Pi Agent
  已连接。
- `/api` 下的未知路径必须返回统一 JSON 错误，不得回退到前端入口页面；缺失的 `.js`、`.css`
  等静态资源必须返回 404，不得伪装成成功的 HTML。
- Go HTTP 服务默认只监听 `127.0.0.1`，未实现远程访问认证前不得默认监听所有网卡。

## 统一服务入口与请求分发约束

- `agentflow server` 是 Web 服务与后台服务的唯一启动入口：同一个 Go 进程内依次初始化配置、
  数据库与后台服务，再启动 HTTP 服务，并由同一处生命周期代码负责启动失败清理与优雅关闭。
- 后台服务通过 `internal/lifecycle` 的 `Service` 契约（`Name`/`Start`/`Stop`）注册，启动顺序
  与注册顺序一致，停止顺序相反；禁止在 HTTP Handler 内创建长期运行的后台服务。
- 统一 Pi Runner（`app.AgentRunnerService`）作为后台服务注册进同一生命周期：Server 停止时
  取消在途调用并等待其收敛。它依赖已打开的数据库与 Session 目录，因此由工厂在打开数据库后
  构造，不能在构造 Server 时创建。
- 数据库是必需组件：初始化失败时服务必须返回错误并停止启动，不得带着未初始化的存储在
  后台服务或 HTTP 层继续运行。
- 启动顺序固定为：配置 → 日志与公共依赖 → 数据库 → 后台服务 → HTTP 服务；关闭顺序相反。
  任一必需组件失败或关键后台服务异常退出时，已启动组件必须按逆序清理，不留监听端口与
  goroutine。
- 所有后端 HTTP 接口必须使用 `/api` 前缀，当前版本接口挂在 `/api/v1` 下；新增接口不得
  使用 `/api` 之外的路径空间。
- `/api` 与 `/api/...` 是保留路径空间，永远不能回退为 `index.html`；`/api` 本身返回 API
  风格的错误响应。
- API 路径判定必须按路径段边界进行（`/api`、`/api/...`），禁止用 `strings.HasPrefix(path, "/api")`
  之类的判断，否则 `/apiary`、`/apix` 会被误判为 API。
- 请求分发顺序固定为：API 保留路径 → 真实存在的静态资源 → 缺失静态资源 404 → 只有符合
  页面规则的 GET/HEAD 请求才允许 SPA 回退到 `index.html`。非 GET/HEAD 的页面请求返回 405，
  不得回退为 HTML。
- 静态资源以文件系统真实存在为准；`assets/` 目录下的路径即使没有扩展名也视为资源，缺失时
  必须返回 404。带内容哈希的产物使用长期缓存，`index.html` 使用重新验证策略。
- 正式构建必须先生成前端产物再编译 Go；缺少 `index.html` 时发布构建必须失败，不得产出
  空白页面或仅含占位资源的可执行文件。
- `agentflow server` 默认开启 `RequireWebUI`，使缺少前端产物的二进制在启动阶段直接失败；
  只有开发模式（页面由 Vite+ 提供）才允许显式 `--require-web-ui=false`。

## 文档同步

- 代码编辑期间不修改 `README.md` 及其他文档。
- 只有用户明确要求时才同步文档。文档不得描述尚未实现的命令或能力。

## 常用命令

```bash
gofmt -l internal cmd tests  # 检查格式
gofmt -w internal cmd tests  # 格式化
go vet ./...                 # 静态检查
go build ./...               # 编译
```

前端命令在 `lingting-agent-flow-ui/` 下执行：

```bash
pnpm dev                  # 开发服务器（vp dev），/api 代理到 Go 服务
pnpm build                # 生产构建，产物写入 internal/webui/dist（含占位文件补回）
pnpm build:assets         # 仅执行 vp build，不补回占位文件
pnpm lint                 # 格式、lint 与类型检查
pnpm test                 # 单元测试（vp test run）
pnpm exec playwright test # 基础布局与连接状态浏览器测试
```

Go 侧服务相关命令在仓库根目录执行：
```bash
go run ./cmd/agentflow server                        # 启动完整服务（API + 内嵌页面 + 后台服务）
go run ./cmd/agentflow server --address 127.0.0.1:26380
make serve                                           # 开发模式后端（--require-web-ui=false）
make web-build                                       # 前端生产构建（按锁文件安装依赖）
make release                                         # 单文件生产构建：前端 + Go
go test -count=1 ./tests/e2e/packaging/...           # 单文件打包冒烟测试
```
