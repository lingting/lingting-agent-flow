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
  `integration/pi/`、`integration/ssh/`、`e2e/` 分层。
- 禁止在生产源码目录（`internal/`、`cmd/`）新增或保留测试文件。
- `tests/internal/testkit/` 只存放通用测试辅助代码，不存放产品实现，也不存放需要额外执行的验证脚本。

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

### 结果报告

- 修改测试相关基础设施后，必须运行与修改范围相关的测试，并如实报告通过、失败、跳过与
  未执行的测试；不得把未执行、被跳过或失败的测试报告为通过。

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
