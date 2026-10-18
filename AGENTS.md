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
- 测试结束禁止删除工作空间与测试产物：不调用 `os.RemoveAll` 删除测试根目录，不使用会在
  结束时自动清理目录的临时目录 API，也不在 `t.Cleanup` 中删除目录。允许关闭数据库连接、
  终止测试启动的子进程、关闭监听端口等运行时资源。

### 真实外部依赖

- 本机没有 `pi` 时，只有依赖真实 Pi CLI 的用例输出 `WARN [SKIP]` 并跳过；Pi Runner
  的单元测试使用替身，必须继续执行。
- 真实 Pi 测试默认模型为 `group/free`，可用 `AGENTFLOW_TEST_PI_MODEL` 覆盖；每次真实
  调用都必须显式传入模型，不得回退到 Pi CLI 配置中的默认付费模型。
- 真实 SSH 测试必须通过 `AGENTFLOW_TEST_SSH_HOST`、`AGENTFLOW_TEST_SSH_USER`、
  `AGENTFLOW_TEST_SSH_PORT`、`AGENTFLOW_TEST_SSH_KEY_PATH`、
  `AGENTFLOW_TEST_SSH_KNOWN_HOSTS` 获取目标；必需目标缺失时输出警告并跳过，目标配置
  完整但连接、认证或主机密钥校验失败时必须让测试失败。
- 禁止关闭 SSH 主机密钥验证来规避测试失败，禁止自动接受未知主机密钥。
- 真实外部测试的远程操作只允许在随机生成的远程临时目录内进行。
- SSH 连接层的单元测试放在 `tests/unit/ssh/`，本机协议集成测试放在
  `tests/integration/local/ssh/`；两者都不得访问外网或依赖本机已安装的 SSH 服务。
- 本机集成测试的 SSH/SFTP 服务端只能由测试代码在进程内启动，不得要求用户预先启动服务，
  也不得用"配置解析正确"代替真实握手、认证与传输验证。
- 远程命令的取消与超时测试必须验证会话确实被关闭，不能只验证本地停止等待。
- 真实远程 Docker 集成测试必须显式设置 `AGENTFLOW_TEST_REMOTE_DOCKER=1`；未设置时输出
  `WARN [SKIP]`，普通测试不得修改真实远程 Docker。
- 真实远程 Docker 测试放在 `tests/integration/remote/docker/`，目标机器通过
  `AGENTFLOW_TEST_SSH_*` 获取；开关已启用但目标缺失时说明缺少的变量并跳过，目标完整
  但认证失败、Docker 不可用或断言不成立时必须让测试失败。
- 真实远程 Docker 测试只允许操作带随机后缀、属于本次测试的镜像标签、容器与远程目录；
  禁止执行全局 `docker system prune`、`docker image prune`、`docker volume prune`。
- 真实远程 Agent 测试放在 `tests/integration/remote/agent/`，同样必须显式设置
  `AGENTFLOW_TEST_REMOTE_DOCKER=1`；容器内 Pi CLI 是否可用必须通过容器内实际检查确认，
  不能依据本机是否安装 Pi 判断。测试镜像由该目录下的 `Dockerfile` 构建，基础镜像与
  Pi CLI 版本都固定到具体版本。
- 真实远程工作流测试放在 `tests/integration/remote/workflow/`，同样必须显式设置
  `AGENTFLOW_TEST_REMOTE_DOCKER=1`；只操作带随机后缀、属于本次测试的容器与远程目录。

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
- 逻辑删除优先：`roles` 与 `execution_hosts` 使用 `deleted_at` 标记删除，删除操作不得
  级联删除历史记录。
- 迁移已有数据库必须保留数据，禁止删除数据库重建；表重建必须保留必要的索引、触发器、
  视图与自增主键行为。
- 数据库连接显式关闭外键检查（`foreign_keys(0)`），业务 Schema 本身不得依赖该开关。
- 迁移必须在事务内完成，失败时回滚且不得写入版本记录；并发打开同一数据库只允许执行
  一次迁移。
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
