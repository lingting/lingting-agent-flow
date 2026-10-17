# AgentFlow

AgentFlow 是一个使用 Go 编写的本地优先 AI 编码工作流编排器。

它把编码需求管理为持久化任务，并通过相互隔离的 Pi Agent 会话，协同完成规划、实现、验证与自动修复。

## 当前开发阶段

AgentFlow 处于**执行节点接入阶段**。当前仓库包含工程骨架、可运行的 CLI、配置加载与校验、结构化日志封装、SQLite 数据模型与版本化迁移，以及执行节点管理与 SSH 服务探测。

执行节点目前只支持配置管理与 SSH 服务可达性探测，**没有任何 SSH 身份认证方式**，因此不具备远程登录、远程命令执行、文件传输或 Docker 管理能力。调度、Agent 执行与 Web UI 尚未实现。文档只会在对应能力落地后才声明其可用。

## 第一版功能范围

已实现：

- Cobra CLI，提供 `--help`、`--version`、`version`、`config validate`。
- 构建信息注入（版本、提交、构建时间）。
- YAML 配置加载与校验：拒绝未知字段、校验并发数、重试次数、角色列表、角色 ID 唯一性、角色类型、工具策略模式与必需路径。
- 基于 `log/slog` 的结构化日志，输出到标准错误流，级别可通过配置切换。
- Makefile 统一入口：`fmt`、`fmt-check`、`vet`、`test`、`build`、`run`、`check`。
- SQLite 持久化：`roles`、`requirements`、`tasks`、`steps`、`attempts`、`sessions`、`verification_results`、`events` 八张核心表。
- 版本化迁移：SQL 脚本通过 `embed` 嵌入程序，启动时按版本顺序自动执行，已应用版本记录在 `schema_migrations`。
- 存储接口与事务边界：`storage.Store` / `storage.Tx` 提供跨实体原子更新，错误可按类别判断。
- 执行节点管理：`agentflow host` 命令组支持新增、列出、查看、设置默认与删除执行节点，节点保存在 SQLite 的 `execution_hosts` 表中。
- SSH 服务探测：`agentflow host probe` 使用 Go 代码建立 TCP 连接并校验服务端 SSH 协议标识，不依赖本机 `ssh` 命令，也不执行身份认证。

第一版目标（尚未实现）：

- 持久化任务队列，并限制同时执行的任务数量。
- 结构化、分步骤的开发计划。
- 每次执行尝试使用独立的 Agent 会话。
- 以验证结果驱动的修复循环。
- 相互隔离的 Git Worktree。
- 可恢复的任务状态。
- 可选的本地 Web UI。

## 技术栈

- Go（版本要求见 `go.mod`）
- Cobra（CLI）
- `gopkg.in/yaml.v3`（配置）
- `log/slog`（日志）
- `database/sql` + `modernc.org/sqlite`（持久化，纯 Go 驱动，不依赖 CGO）
- Go 标准库 `net`、`context`（SSH 端点探测）
- Go 标准库 `testing`（测试）
- Makefile（构建入口）

Pi RPC、Git Worktree、React 与 TypeScript 属于后续阶段的技术选型，当前尚未引入。

## 环境依赖

- 受支持的 Go 工具链
- Git（`make build` 用它注入提交号）
- Make（可选，也可直接使用 `go` 命令）

只有 `agent check` 与 `agent test` 需要 Pi：前者检查 PATH 中的 `pi` 及其非交互式参数，
后者还会用角色配置的模型发起一次真实调用，因此需要对应 Provider 的凭据。其余命令
（`config`、`version`、`host`）不依赖 Pi，也不需要模型凭据。

## 安装与运行

```bash
git clone <repo>
cd agentflow
go mod tidy

make build
./bin/agentflow --help
```

不依赖 Make：

```bash
go build -o bin/agentflow ./cmd/agentflow
go run ./cmd/agentflow --help
```

## CLI 命令示例

```bash
./bin/agentflow --help
./bin/agentflow --version
./bin/agentflow version

# 校验示例配置
./bin/agentflow config validate --config configs/example.yaml
```

配置路径规则：

1. 显式指定 `--config` 时使用指定文件。
2. 未指定时读取 `configs/agentflow.yaml`。
3. 文件不存在视为错误，不会静默回退到默认配置。

校验失败时命令返回非零退出码，并逐条列出字段级问题：

```text
错误: 配置校验失败: configs/agentflow.yaml
  - scheduler.max_concurrent_tasks: 必须大于 0
  - roles[0].type: Role.type: 不支持的角色类型 reviewer
```

## 执行节点管理

执行节点是通过 SSH 管理的远程 Linux 机器。当前版本只保存连接目标与远程工作空间配置，
并提供 SSH 服务可达性探测。

`host` 命令组复用 `config validate` 的配置加载流程，因此同样接受 `--config`；
未指定时读取 `configs/agentflow.yaml`，数据库路径取自该配置的 `storage.database_path`。

### 新增执行节点

```bash
./bin/agentflow host add   --id node-build-01   --name 构建节点   --host 192.0.2.10   --port 22   --user deploy   --workspace "~/.lingting/agentflow"   --default
```

| 参数 | 说明 |
|---|---|
| `--id` | 节点标识，省略时自动生成 |
| `--name` | 节点名称，省略时使用 `--host` 的值 |
| `--host` | IP 地址或主机名，必填；只写地址，不写端口 |
| `--port` | SSH 端口，默认 `22`，取值范围 1-65535 |
| `--user` | SSH 登录用户名，必填 |
| `--workspace` | 远程工作空间路径，默认 `~/.lingting/agentflow` |
| `--default` | 同时设为默认执行节点 |

该命令只写入本机数据库，**不会**连接目标机器、不会创建远程目录，也不代表可以登录。

### 列出与查看

```bash
./bin/agentflow host list
./bin/agentflow host show node-build-01
```

`host list` 输出标识、名称、地址、用户名、工作空间、默认标记与启用状态。

### 设置默认节点

```bash
./bin/agentflow host set-default node-build-01
```

默认节点在单个事务内切换，数据库保证最多只有一个默认节点。节点不存在或已被禁用时
返回明确错误。

### 探测 SSH 服务

```bash
./bin/agentflow host probe node-build-01
```

探测由 Go 代码完成（`net` + `context`），不执行本机的 `ssh`、`scp` 等命令。它会：

1. 校验主机地址、端口与超时配置；
2. 建立 TCP 连接；
3. 等待并校验服务端 SSH 协议标识（RFC 4253，形如 `SSH-2.0-OpenSSH_9.6`）；
4. 记录耗时并关闭连接。

输出区分四类结果：

| 结果类别 | 含义 |
|---|---|
| `unreachable` | 地址不可达，例如连接被拒绝或域名无法解析 |
| `timeout` | 连接或读取协议标识超时 |
| `not_ssh` | 端口可连接，但未返回有效的 SSH 协议标识 |
| `ok` | 已识别到有效 SSH 协议标识 |

成功时输出固定说明：`SSH 服务可达，协议标识有效；尚未执行身份认证。`

### SSH 探测与 SSH 登录的区别

- **探测**只验证网络可达性与 SSH 服务协议标识，**不做任何身份认证**。
- 探测成功不等于登录成功，也不代表远程 Linux 环境已经验证通过。
- 当前版本没有密码、私钥或 SSH Agent 认证，因此无法执行远程命令或传输文件。
- 后续加入认证能力时，认证配置与凭据通过 `internal/sshclient` 的 `CredentialProvider`
  与 `ClientFactory` 注入，执行节点管理业务不需要改动。

### 远程工作空间路径

- 默认值为 `~/.lingting/agentflow`。
- `~` 指**远程 SSH 登录用户的 Home**，不是本机用户的 Home。
- 路径遵循 POSIX 语义，必须使用 `/`，可以是以 `/` 开头的绝对路径或以 `~/` 开头的路径。
- 不接受根目录 `/`、`~` 本身、相对路径、`\` 分隔符、连续 `//` 与 `..` 上级引用。
- 当前阶段只保存配置，不创建也不检查远程目录；后续远程操作应由远程用户环境解析该路径
  并转换为绝对路径，涉及目录创建与删除时还必须再做路径安全检查。

### 删除节点

```bash
./bin/agentflow host remove node-build-01
```

只删除本机数据库中的节点配置，**不会**删除远程工作空间或远程 Docker 资源。

## 配置文件说明

示例见 [configs/example.yaml](configs/example.yaml)。

| 字段 | 说明 |
|---|---|
| `app.name` | 应用名称，不能为空 |
| `app.log_level` | 日志级别，可选 `debug`、`info`、`warn`、`error` |
| `storage.database_path` | 数据库文件路径，不能为空 |
| `workspace.worktree_root` | 任务工作区根目录，不能为空 |
| `scheduler.max_concurrent_tasks` | 同时执行的任务上限，必须大于 0 |
| `scheduler.max_retries_per_step` | 单个步骤最大重试次数，不得为负数 |
| `roles[].id` | 角色标识，不能为空且必须唯一 |
| `roles[].name` | 角色名称，可自由命名，不能为空 |
| `roles[].type` | 角色类型，只能是 `planner`、`developer`、`validator` |
| `roles[].runner` | Agent 运行时，当前只支持 `pi` |
| `roles[].model` | 模型标识，不能为空；示例中的值为占位符 |
| `roles[].system_prompt` | 系统提示词，不能为空 |
| `roles[].tools_policy.mode` | 工具策略模式，需与角色类型匹配：`planner` → `read_only`、`developer` → `development`、`validator` → `verification` |

工作流只依据 `type` 识别角色职责，`name` 仅用于展示。

`tools_policy` 当前只是配置声明，用于后续阶段定义角色权限，**并不代表 Pi 已经执行了对应限制**。

## 数据库

### 文件位置

数据库路径由配置项 `storage.database_path` 提供，示例配置使用 `.agentflow/agentflow.db`：

```yaml
storage:
  database_path: .agentflow/agentflow.db
```

- 打开数据库时会自动创建父目录，无需手工 `mkdir`。
- `.agentflow/` 已被 `.gitignore` 忽略，不会进入版本库。
- 启用 WAL 模式，因此除主数据库文件外还会出现 `agentflow.db-wal` 与 `agentflow.db-shm`
  两个伴随文件。它们属于数据库的一部分，**不要单独删除或复制**；需要备份时应在关闭
  数据库后复制整个目录。
- 连接参数：`foreign_keys=1`、`busy_timeout=5000`、`journal_mode=WAL`、`synchronous=NORMAL`、
  `_txlock=immediate`。事务一开始即取得写锁，避免"先读后写"升级锁时返回 `SQLITE_BUSY`，
  这是后续多 Worker 原子领取任务的事务基础。
- 连接池默认上限为 4，第一版优先保证正确性，不通过增大连接池制造额外的写锁竞争。

### 迁移

迁移脚本位于 `internal/storage/sqlite/migrations/`，通过 `embed` 嵌入程序，运行时不依赖
工作目录下的外部 SQL 文件。

- 迁移在 `sqlite.Open` 时自动执行，已应用的版本记录在 `schema_migrations` 表中。
- 每个迁移在独立事务内执行：失败时回滚，不会记录成功版本，也不会留下半建的 schema。
- 启动语义幂等：已完成的迁移不会重复执行，重复打开数据库不会重复建表。
- 数据库版本高于程序支持的最新版本时，`Open` 返回 `storage.ErrUnsupportedSchema`。
- 修改 schema 必须新增更高版本的脚本（例如 `0002_add_retry_policy.sql`），已发布的迁移不得修改。

### 表结构

| 表 | 说明 | 关键约束 |
|---|---|---|
| `roles` | 运行时角色配置 | `type` 限定三种角色类型；`enabled` 只能为 0/1；`tools_policy_json` 必须为合法 JSON |
| `requirements` | 用户提交的原始需求 | `priority` 保存整数排序值；`status` 限定五种状态；保留 `source` / `source_id` |
| `tasks` | 某个需求的一次执行实例 | 外键引用 `requirements`；同一需求可有多条任务记录；`repository_path`、`base_ref` 非空 |
| `steps` | Planner 生成的结构化步骤 | `(task_id, sequence)` 唯一；`role_snapshot_json` 保存角色快照；多个 JSON 列需合法 |
| `attempts` | 步骤的每次完整执行尝试 | `(step_id, attempt_number)` 唯一；失败记录保留，新的修复创建新 Attempt |
| `sessions` | 每次 Agent 会话的元数据 | 可空 `step_id` / `attempt_id` 支持任务级会话；关联 Attempt 时必须给出 `step_id` |
| `verification_results` | 每次验证的结构化结果 | `passed` 只能为 0/1；失败必须带失败摘要；只追加，不覆盖历史 |
| `events` | 工作流关键事件 | 自增主键；只追加写入；`(task_id, id)` 联合索引支持任务时间线查询 |
| `execution_hosts` | 通过 SSH 管理的执行节点 | `port` 限定 1-65535；`is_default` / `enabled` 只能为 0/1；部分唯一索引保证最多一个默认节点；**不保存任何凭据字段** |
| `schema_migrations` | 迁移版本记录 | `version` 主键，记录迁移名与应用时间 |

通用约定：

- 主键为 TEXT，由应用层生成（`events` 使用自增 INTEGER）。
- 时间统一为 UTC Unix 毫秒时间戳，字段以 `_at` 结尾。
- JSON 统一以 UTF-8 文本保存，并通过 `json_valid` 约束校验。
- 所有外键使用 `ON DELETE RESTRICT`，禁止级联删除历史记录。

### 存储接口

业务层只依赖 `internal/storage` 的接口，不接触 SQL 或驱动类型：

- `storage.Store`：各实体的读写方法与 `WithTx`、`Close`。
- `storage.Tx`：事务内可用的同一组实体方法。
- `storage.RoleSeeder`：角色初始化入口，仅在 `roles` 为空时导入，不覆盖已有角色。
- `storage.ExecutionHostStore`：执行节点读写与 `SetDefaultExecutionHost`，默认节点切换在单个事务内完成。
- 错误通过 `errors.Is` 判断：`ErrNotFound`、`ErrConflict`、`ErrForeignKey`、`ErrInvalidData`、
  `ErrBusy`、`ErrUnsupportedSchema`。

跨实体的关键状态更新必须使用 `WithTx`，并在回调内使用传入的 `Tx`，不能在应用层分多次
调用独立方法后假定操作具有原子性。

## 开发与测试命令

```bash
make fmt          # 格式化 cmd、internal 与 tests
make fmt-check    # 列出未格式化的文件
make vet          # 静态检查
make test         # 全部测试
make test-unit    # 快速单元测试
make test-local   # 本机集成测试
make test-pi      # 真实 Pi 集成测试
make test-ssh     # 真实 SSH 集成测试
make test-e2e     # CLI 端到端测试
make test-race    # 单元测试竞态检测
make build        # 构建 bin/agentflow
make run          # 直接运行
make check        # fmt-check + vet + build
```

测试代码全部位于根目录 `tests/` 下，按 `unit/`、`integration/local/`、`integration/pi/`、
`integration/ssh/`、`e2e/` 分层，每个目录都是独立的 Go 测试包：

```bash
go test -count=1 -v -p 8 -parallel 8 ./tests/unit/...               # 快速单元测试
go test -count=1 -v -p 8 -parallel 8 ./tests/integration/local/...  # 本机集成测试
go test -count=1 -v ./tests/integration/pi/...                      # 真实 Pi
go test -count=1 -v ./tests/integration/ssh/...                     # 真实 SSH
go test -count=1 -v ./tests/e2e/...                                 # CLI 端到端
go test -count=1 -v -p 8 -parallel 8 ./tests/...                    # 全部
go test -race -count=1 ./tests/unit/...                             # 竞态检测
```

每次测试入口启动时在系统临时目录下创建随机工作空间，数据库、Session、日志与生成文件
全部落在其中，并把绝对路径输出为 `TEST_ARTIFACT_DIR`。测试结束只关闭运行时资源，
**不删除测试产物**，便于检查失败现场。快速单元测试与本机集成测试不访问真实模型或远程
网络；本机没有 `pi` 时真实 Pi 用例输出警告并跳过。

测试分类、环境变量与编写约定见 [tests/README.md](tests/README.md)。

开发环境、代码风格与提交约定见 [docs/development.md](docs/development.md)，分层与依赖方向见 [docs/architecture.md](docs/architecture.md)。

## 当前未实现的功能

以下能力均在计划中，当前版本**不提供**：

- 需求队列、Worker 与并发调度。
- Pi RPC 客户端、子进程管理与 Agent 执行。
- Git Worktree 创建与清理。
- Planner / Developer / Validator 的实际执行。
- 验证失败回退与任务恢复。
- HTTP API 与 Web UI。
- 多 Agent Provider 与分布式执行。
- 角色管理 CLI 与 Web 页面（当前只有存储层的角色初始化与增删改查接口）。
- SSH 身份认证（密码、私钥、SSH Agent），因此也没有已认证的远程命令执行与 SFTP 文件传输。
- 远程工作空间初始化、远程 Docker 检测、镜像构建与容器管理。
- 在远程容器内执行 Pi Agent。

## 安全与风险提示

AgentFlow 的最终形态会**执行外部程序并修改项目文件**，请注意：

- 后续版本的 Agent 会在你的仓库中创建与修改文件，并可能执行命令。
- 请使用相互隔离的工作区，并配置合理的 Agent 权限；提示词不是权限边界。
- 审查任何可能修改或删除数据的操作，尤其是工作区清理与 Git 操作。
- 不要在配置或仓库中保存真实 API Key；模型凭据由 Pi 及其 Provider 管理。
- 执行节点表只保存连接目标与工作空间，不保存密码、私钥或任何认证材料。
- `host probe` 只验证 SSH 服务可达性，**不代表已经登录成功或远程环境已验证**。
- 本地 HTTP 服务在实现后将默认只监听 `127.0.0.1`，且不存储模型凭据。

## 许可证

许可证将在首次公开发布前确定。
