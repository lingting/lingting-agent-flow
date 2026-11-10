# 测试规范

本文件是 AgentFlow 的测试规则：目录分层、独立入口、测试工作空间、真实外部依赖与结果报告。测试目录
在仓库中的位置见 [workspace.md](workspace.md)；前端测试目录见 [frontend.md](frontend.md)；各测试套件的
目录与操作细节见 [tests/README.md](../tests/README.md)。

## 1. 授权与默认验证

- 默认只做非测试验证：阅读代码、检查 `git diff`、`gofmt`、`go vet`、类型与引用检查。
- 未经用户明确要求，不运行测试、不创建测试文件、不编写临时验证脚本，也不通过"测试后删除"的方式绕过
  该限制。
- 数据库结构变更只追加 `internal/storage/sqlite/migrations/` 下的版本化脚本，不手写版本判断，也不为
  Goose 自身的迁移调度、版本表或 SQLite 事务编写重复实现的测试（见 `AGENTS.md` 的「数据库约束规范」）。

## 2. 目录

- 所有测试代码与测试入口必须放在根目录 `tests/` 下，按 `unit/`、`integration/local/`、
  `integration/ssh/`、`integration/remote/`、`e2e/` 分层。
- 禁止在生产源码目录（`internal/`、`cmd/`）新增或保留测试文件。
- `tests/integration/local/workflow/` 是应用层编排的集成测试目录：使用真实 SQLite 文件与真实
  `app.TaskWorkflowService`，只把外部依赖（Agent 执行、容器核验、单步骤闭环）替换为受控替身。恢复类
  用例必须"关闭连接后用同一路径重新打开"来模拟进程退出，并只断言数据库里的持久化事实（任务状态、
  步骤、执行记录、会话、事件），不得以方法返回值代替持久化断言。
- `tests/internal/testkit/` 只存放通用测试辅助代码，不存放产品实现，也不存放需要额外执行的验证脚本。

## 3. 独立入口与并发

- 每个测试目录都是独立的 Go 测试包，可用 `go test` 直接执行；成功判定与退出码由 Go 测试框架负责，
  不得把结果判断委托给额外的 Shell、Python 或日志解析脚本。
- 每个测试入口必须自行完成初始化、执行被测操作、自动断言，并用非零退出码报告失败。
- 新测试默认必须支持并发：无共享状态的用例使用 `t.Parallel()`，不得依赖固定执行顺序，不得依赖其他
  测试预先创建的数据。
- 例外：共用同一台真实设备的测试包必须保持包内串行（不使用 `t.Parallel()`），并在包注释写明原因。
  典型情形是 `tests/integration/remote/workflow/`：它的用例共用同一台远程 Docker 与环境镜像标签，并行
  运行时后完成的构建会把同一标签改指到另一个镜像，使先完成用例记录的镜像 ID 失效，随后"复用已有构建"
  失败、任务启动反复失败并长期停在 `queued`。串行执行时同一次运行只构建一次，标签与记录始终一致。
- 除上述例外，并发度由 `-p` 与 `-parallel` 控制，不得把无共享状态的用例强制串行化。

## 4. 测试工作空间

- 每次测试入口启动时在 `os.TempDir()/lingting-agent-flow-tests/` 下创建随机根目录，每个用例使用独立
  子目录，并输出绝对路径 `TEST_ARTIFACT_DIR`。
- 数据库、WAL/SHM、Session、日志、配置与生成文件必须全部落在该工作空间内，不得写入真实项目目录、
  用户默认数据目录或其他用例的工作空间。
- 使用 `tests/internal/testkit` 提供的 `testkit.New(t)` 与 `testkit.Dir(t)`，不要用 `t.TempDir()`，
  也不要用 `t.Setenv` 切换全局数据目录；路径通过结构体、构造参数或子进程独立环境变量传入。
- 配置文件中的数据库与工作区路径必须是工作空间内的绝对路径。相对路径会按进程工作目录解析（`go test`
  下即包目录），把 SQLite 文件写进仓库；配置模板中的路径一律使用占位符，由统一的辅助函数替换为绝对
  路径，并保留一个断言绝对路径的回归用例。
- 仓库忽略任意层级的 `.agentflow/` 目录，作为误用相对路径时的兜底；不得把运行期数据文件提交进版本库。
- 测试结束禁止删除工作空间与测试产物：不调用 `os.RemoveAll` 删除测试根目录，不使用会在结束时自动清理
  目录的临时目录 API，也不在 `t.Cleanup` 中删除目录。允许关闭数据库连接、终止测试启动的子进程、关闭
  监听端口等运行时资源。
- 每个测试包必须用 `testkit.Main` 作为统一入口（`os.Exit(testkit.Main(m))`），由它把本次运行的
  完整输出写入 `<TEST_ARTIFACT_DIR>/test.log` 并在基目录记录
  `latest-run.txt`，同时默认打开 `-test.v`。不得各包自行丢弃测试日志，应用服务日志统一用
  `testkit.Logger` / `testkit.Slog` 构造。

## 5. 真实外部依赖

真实测试的启用是**两层门禁**，两层都必须通过：

1. **CI 授权门禁（只在 GitHub Actions 生效）**：`GITHUB_ACTIONS=true` 时，用例还要求
   `AGENTFLOW_REAL_TESTS_GRANTED=true`（由授权判定 Job 依据 GitHub Repository Variable
   `AGENTFLOW_REAL_TESTS` 与事件身份字段产出，规则只实现一次于 `internal/cigate`）。未获授权时以
   `WARN [SKIP]` 跳过。该门禁由 `tests/internal/testkit` 的 `RequireRealTests` 统一实施，测试代码不读取
   `AGENTFLOW_REAL_TESTS` 本身。
2. **本机配置开关（任何环境都生效）**：`tests/config/*.yml` 中的 `pi.enable`、`ssh.enable` 与
   `docker.remote_enabled`；文件缺失、开关为 false 或目标字段不全时跳过并说明缺少哪一项。

本机开发环境（没有 `GITHUB_ACTIONS` 标记）不经过第 1 层：`make test`（`go test ./tests/...`）在
`tests/config` 已启用真实依赖时会真实连接测试设备；`make test-unit` / `make test-race` /
`make test-local` 只跑不需要真实设备的分组。跳过只能来自"未获授权"或"未提供配置"，配置完整但连接、
认证或主机密钥校验失败时必须让测试失败。

- 本机不执行 Pi：所有 Agent 调用都在容器内，测试不检查本机是否安装 `pi`，也不读取本机 Pi
  可执行文件路径；容器内 Pi 的可用性由 `tests/integration/remote/agent/` 在容器内执行
  `pi --version` 与 `pi --list-models` 确认。
- 真实模型调用由 `tests/config/pi.yml` 的 `pi.enable` 显式开启（当前只作用于
  `tests/integration/remote/workflow`），默认模型为 `group/free`；每次真实调用都必须显式传入
  模型，不得回退到 Pi CLI 配置中的默认付费模型。模型在清单里挂在不含模型前缀的 provider 下时，
  用同文件的 `pi.provider` 补充 provider 前缀（由 `testkit.PiConfig.ResolvedModel` 统一组装，
  留空时原样使用模型标识）；容器内 Pi 的模型来自 `docker.yml` 的 `pi.*`。
- 真实 SSH 测试的目标统一来自 `tests/config/ssh.yml`（`ssh.host`、`ssh.user`、`ssh.port`、
  `ssh.private_key_path`、`ssh.known_hosts`），并由 `ssh.enable` 显式开启；未启用时输出警告并跳过，
  启用但必需目标缺失时输出警告并跳过，目标配置完整但连接、认证或主机密钥校验失败时必须让测试失败。
  测试配置禁止再通过 `AGENTFLOW_TEST_SSH_*` 等环境变量逐项读取。
- `ssh.known_hosts` 留空时，真实 SSH 测试必须复用本机用户 OpenSSH 的 `~/.ssh/known_hosts` 与
  `known_hosts2`，与产品默认行为一致；显式填写但文件不可读时直接让测试失败，不得静默回落到空信任库。
  来源解析统一由 `tests/internal/testkit` 的 `KnownHostsSources` 提供，各远程测试目录不得各自实现。
- 解析出的 known_hosts 来源必须先快照复制进当前用例工作空间，校验只读快照：产品与测试都不得写入用户
  真实 known_hosts。需要信任某主机时直接构造 known_hosts 文件，不得调用任何写入接口（产品侧已不再
  提供）。
- 主机密钥未知的失败信息必须列出实际读取的 known_hosts 路径与可用配置项，并提示用 OpenSSH 追加已核验
  的密钥，使"配置写错"与"连接层缺陷"可以区分。
- 禁止关闭 SSH 主机密钥验证来规避测试失败，禁止自动接受未知主机密钥。
- 真实外部测试的远程操作只允许在随机生成的远程临时目录内进行。
- SSH 连接层的测试放在 `tests/integration/local/ssh/`（进程内启动服务端、走真实握手与认证），不得访问外网
  或依赖本机已安装的 SSH 服务。
- 本机集成测试的 SSH/SFTP 服务端只能由测试代码在进程内启动，不得要求用户预先启动服务，也不得用
  "配置解析正确"代替真实握手、认证与传输验证。测试用私钥统一由 `tests/internal/testkit` 的
  `PrivateKeyPath` 生成，只生成无口令私钥。
- 远程命令的取消与超时测试必须验证会话确实被关闭（例如断言服务端活动会话数归零），不能只验证本地
  停止等待。
- 真实远程 Docker 集成测试必须把 `tests/config/docker.yml` 的 `docker.remote_enabled` 设为 `true`
  并同时启用 `ssh.enable`；未启用时输出 `WARN [SKIP]`，普通测试不得修改真实远程 Docker。
- 真实远程 Docker 测试放在 `tests/integration/remote/docker/`，目标机器由 `tests/config/ssh.yml` 提供；
  开关已启用但目标缺失时说明缺少的配置项并跳过，目标完整但认证失败、Docker 不可用或断言不成立时必须
  让测试失败。
- 真实远程 Docker 测试只允许操作带随机后缀、属于本次测试的镜像标签、容器与远程目录；禁止执行全局
  `docker system prune`、`docker image prune`、`docker volume prune`。
- 真实远程 Agent 测试放在 `tests/integration/remote/agent/`，同样要求 `docker.remote_enabled` 与
  `ssh.enable` 都已启用；容器内 Pi CLI 是否可用必须通过容器内实际检查确认，不能依据本机是否安装 Pi
  判断。测试镜像由该目录下的 `Dockerfile` 构建，基础镜像与 Pi CLI 版本都固定到具体版本。
- 真实远程工作流测试放在 `tests/integration/remote/workflow/`，同样要求 `docker.remote_enabled` 与
  `ssh.enable` 都已启用；只操作带随机后缀、属于本次测试的容器与远程目录。
- 共用同一台设备的真实测试包内必须串行：这些用例的环境镜像标签由环境、版本号与构建配置哈希共同决定，
  并行构建会把同一标签改指到不同镜像，使先完成的用例在"复用已有构建"时核验失败。包内串行由包注释声明，
  包与包之间仍由 `.github/scripts/run-real-tests.sh` 以 `-p 1` 串行执行。
- 真实规划运行时测试放在 `tests/integration/remote/planning/`，同样要求 `docker.remote_enabled`、
  `ssh.enable` 与完整的 `docker.pi` 配置；它只使用生产服务装配（容器服务、规划运行时、规划 Agent、
  会话协调器、当前存储与角色服务），不新增第二套容器 / Session / Pi Runner 实现。测试镜像
  `tests/integration/remote/planning/Dockerfile` 只设置 `PI_CODING_AGENT_DIR=/tmp/pi-agent`：只读规划容器的
  唯一可写目录是 `/tmp`，模型配置只在运行期写入容器内 tmpfs，不进入镜像层、日志或数据库。
- 真实套件自带分钟级运行预算，`go test` 默认的 10 分钟上限会让它们必然超时；统一入口
  `.github/scripts/run-real-tests.sh` 因此用 `--timeout`（默认 `90m`）显式设置 `go test -timeout`，
  超时仍按失败上报。本机单独执行真实包时同样需要按需传入 `-timeout`。

## 6. 数据库与状态测试

- 数据库相关测试集中在 `tests/unit/store/`：仓储读写、未知枚举值原样保留、逻辑删除、条件更新拒绝迟到
  结果与并发写入；`tests/unit/store/migrations_test.go` 只确认全新数据库能完成初始迁移、重复打开不会重复
  执行。`tests/integration/local/database/`（Schema 审计、旧库升级与数据保留）已删除：Schema 与旧库不兼容
  行为没有自动化守点，新增迁移脚本需要人工核对。
- 涉及跨实体状态更新时，必须通过 `storage.WithTx` 在单个事务内完成。
- 测试不得读写用户真实配置、仓库或 `.agentflow/` 下的真实数据。

## 7. 执行链路改动后的覆盖要求

修改执行链路后至少覆盖：成功、非零退出，以及超时或取消导致的中断。执行结论必须由真实结果决定：
`completed` 要求可核验的运行痕迹（完整 Session 或进程标识），没有运行痕迹的"成功"要按失败处理。
并发重复请求只有在会造成同一任务重复执行时才需要相应保护与测试。

修改任务领取或需求聚合删除路径后，还必须覆盖"已删除的任务不会被新的一次领取"。需求聚合删除只写
`deleted_at`、不改任务状态，因此候选事实中的旧状态不足以发现删除；用例应断言数据库事实（是否写入
`TaskClaimed` 事件、执行权是否残留、需求与任务的删除状态是否一致），而不是只看方法返回值。

## 8. 结果报告

- 运行任何测试前都必须先获得用户明确授权；运行后如实报告通过、失败、跳过与未执行的测试，不得把未执行、
  被跳过或失败的测试报告为通过。
- 测试产物路径（`TEST_ARTIFACT_DIR`）与远程测试资源位置必须在最终报告中列出。
