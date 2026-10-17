# AgentFlow

AgentFlow 是一个使用 Go 编写的本地优先 AI 编码工作流编排器。

它把编码需求管理为持久化任务，并通过相互隔离的 Pi Agent 会话，协同完成规划、实现、验证与自动修复。

## 当前开发阶段

AgentFlow 处于**项目初始化阶段**。当前仓库只包含工程骨架、可运行的 CLI、配置加载与校验、结构化日志封装，以及一组针对配置与 CLI 的单元测试。

编排能力尚未实现。文档只会在对应能力落地后才声明其可用。

## 第一版功能范围

已实现：

- Cobra CLI，提供 `--help`、`--version`、`version`、`config validate`。
- 构建信息注入（版本、提交、构建时间）。
- YAML 配置加载与校验：拒绝未知字段、校验并发数、重试次数、角色列表、角色 ID 唯一性、角色类型、工具策略模式与必需路径。
- 基于 `log/slog` 的结构化日志，输出到标准错误流，级别可通过配置切换。
- Makefile 统一入口：`fmt`、`fmt-check`、`vet`、`test`、`build`、`run`、`check`。

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
- Go 标准库 `testing`（测试）
- Makefile（构建入口）

SQLite、Pi RPC、Git Worktree、React 与 TypeScript 属于后续阶段的技术选型，当前尚未引入。

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

开发环境、代码风格与提交约定见 [docs/development.md](docs/development.md)，分层与依赖方向见 [docs/architecture.md](docs/architecture.md)。

## 当前未实现的功能

以下能力均在计划中，当前版本**不提供**：

- SQLite 建表、仓储与迁移脚本。
- 需求队列、Worker 与并发调度。
- Pi RPC 客户端、子进程管理与 Agent 执行。
- Git Worktree 创建与清理。
- Planner / Developer / Validator 的实际执行。
- 验证失败回退与任务恢复。
- HTTP API 与 Web UI。
- 多 Agent Provider 与分布式执行。

## 安全与风险提示

AgentFlow 的最终形态会**执行外部程序并修改项目文件**，请注意：

- 后续版本的 Agent 会在你的仓库中创建与修改文件，并可能执行命令。
- 请使用相互隔离的工作区，并配置合理的 Agent 权限；提示词不是权限边界。
- 审查任何可能修改或删除数据的操作，尤其是工作区清理与 Git 操作。
- 不要在配置或仓库中保存真实 API Key；模型凭据由 Pi 及其 Provider 管理。
- 本地 HTTP 服务在实现后将默认只监听 `127.0.0.1`，且不存储模型凭据。

## 许可证

许可证将在首次公开发布前确定。
