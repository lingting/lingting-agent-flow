# AgentFlow

AgentFlow 是一个使用 Go 编写的本地优先 AI 编码工作流编排器。

它把编码需求管理为持久化任务，并通过相互隔离的 Pi Agent 会话，协同完成规划、实现、验证与自动修复。

## 当前开发阶段

AgentFlow 处于**数据层落地阶段**。当前仓库包含工程骨架、可运行的 CLI、配置加载与校验、结构化日志封装，以及 SQLite 数据模型、版本化迁移与仓储实现。

调度、Agent 执行与 Web UI 尚未实现。文档只会在对应能力落地后才声明其可用。

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
- Go 标准库 `testing`（测试）
- Makefile（构建入口）

Pi RPC、Git Worktree、React 与 TypeScript 属于后续阶段的技术选型，当前尚未引入。

## 环境依赖

- 受支持的 Go 工具链
- Git（`make build` 用它注入提交号）
- Make（可选，也可直接使用 `go` 命令）

当前版本不调用 Pi，也不需要模型 Provider 凭据。安装并配置 Pi 是后续阶段的要求。

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
- 错误通过 `errors.Is` 判断：`ErrNotFound`、`ErrConflict`、`ErrForeignKey`、`ErrInvalidData`、
  `ErrBusy`、`ErrUnsupportedSchema`。

跨实体的关键状态更新必须使用 `WithTx`，并在回调内使用传入的 `Tx`，不能在应用层分多次
调用独立方法后假定操作具有原子性。

## 开发与测试命令

```bash
make fmt          # 格式化 cmd 与 internal
make fmt-check    # 列出未格式化的文件
make vet          # 静态检查
make test         # 单元测试
make build        # 构建 bin/agentflow
make run          # 直接运行
make check        # fmt + vet + test + build
```

数据库相关测试全部使用临时目录中的临时数据库，不读写用户真实数据：

```bash
go test ./internal/storage/...        # 迁移、实体读写、约束、事务与恢复
go test -race ./internal/storage/...  # 并发写入与事务竞争
```

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

## 安全与风险提示

AgentFlow 的最终形态会**执行外部程序并修改项目文件**，请注意：

- 后续版本的 Agent 会在你的仓库中创建与修改文件，并可能执行命令。
- 请使用相互隔离的工作区，并配置合理的 Agent 权限；提示词不是权限边界。
- 审查任何可能修改或删除数据的操作，尤其是工作区清理与 Git 操作。
- 不要在配置或仓库中保存真实 API Key；模型凭据由 Pi 及其 Provider 管理。
- 本地 HTTP 服务在实现后将默认只监听 `127.0.0.1`，且不存储模型凭据。

## 许可证

许可证将在首次公开发布前确定。
