# 架构说明

本文件描述 AgentFlow 的架构边界与依赖方向，用于判断“新代码应该放在哪里”以及“哪些依赖不允许出现”。

> 下文的架构图为**目标架构**，仅说明分层意图。当前代码实现了 CLI 骨架、配置加载与校验、日志封装、SQLite 数据层、执行节点管理、SSH 服务探测与完整的 SSH 连接层（主机密钥校验、身份认证、远程命令执行与 SFTP 传输），图中调度、Agent 执行与 UI 部分尚未实现。

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
- **Scheduler / Workflow Engine**：任务领取、并发控制与 Plan / Develop / Verify / Retry 状态迁移。
- **Agent Runner / Process Manager**：以独立子进程方式驱动 Pi，并管理进程生命周期。
- **Pi RPC / Git Worktree**：JSONL RPC 协议与任务工作区隔离。
- **SQLite**：唯一真相来源。

## 目录地图

```text
cmd/agentflow/            CLI 入口，只做构建信息注入与启动
internal/app/             应用服务，CLI 与 HTTP 的共同调用入口
internal/cli/             Cobra 命令实现
internal/config/          配置结构与 YAML 加载、校验
internal/logging/         结构化日志封装
internal/domain/          核心实体、状态与业务规则
internal/scheduler/       任务领取、并发控制、Worker 与恢复（未实现）
internal/workflow/        Plan / Develop / Verify / Retry 状态迁移（未实现）
internal/agent/           统一 Agent 接口（未实现）
internal/agent/pi/        Pi RPC 适配器（未实现）
internal/process/         外部进程生命周期管理（未实现）
internal/gitworktree/     任务工作区创建、检查与清理（未实现）
internal/storage/         存储接口与统一存储错误
internal/storage/sqlite/  数据库连接、迁移与仓储实现
internal/sshprobe/        SSH 端点探测：TCP 可达性与协议标识校验
internal/sshclient/        SSH 连接层：主机密钥校验、身份认证、远程执行与 SFTP 传输
internal/events/          内部事件定义与事件总线（未实现）
internal/httpapi/         HTTP 路由、处理器与 SSE（未实现）
web/ui/                   React + TypeScript 前端（未实现）
configs/                  示例配置
migrations/               迁移说明（实际迁移脚本在 internal/storage/sqlite/migrations/）
docs/                     架构与开发文档
tests/unit/               快速单元测试（不依赖真实 Pi、SSH 或网络）
tests/unit/ssh/           SSH 连接层单元测试（主机密钥、认证配置、路径与结果状态）
tests/integration/local/  本机集成测试（数据库全链路、进程内 SSH/SFTP 服务端、TCP 探测）
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
- `domain` 不得感知数据库、HTTP、JSONL RPC、子进程与文件系统。
- `config` 可以把角色类型映射到 `domain.RoleType` 以复用取值校验，但配置结构自身保持独立，不把 YAML 细节泄漏进 `domain`。
- `app` 之上的层不得直接访问 SQLite；所有持久化经由仓储接口。
- `scheduler` 与 `workflow` 不得直接创建 Pi 进程，只能通过 `agent` 接口。
- `httpapi` 与 `cli` 不得包含状态迁移逻辑，只调用 `app`。
- `web/ui` 不得复制工作流规则，只消费 HTTP API 与 SSE。

## 当前实现的分层

```text
cmd/agentflow → cli → app → config / logging / storage / sshprobe / sshclient
                          ↘ domain
```

- `main.go` 只注入构建信息、处理信号并返回退出码。
- `cli` 只做参数解析、结果输出与退出码转换，不包含校验规则；凭据输入（隐藏输入、
  标准输入读取）也只在命令层发生，转换后立即交给 `sshclient` 的凭据提供器。
- `app` 组装配置与日志，并把配置校验编排成一次应用服务调用；同时负责把配置中的角色转换为存储层的初始化定义，以及把节点配置转换为 SSH 连接目标与认证配置。
- `config` 负责 YAML 解析、未知字段拒绝与字段级校验。
- `logging` 封装 `log/slog`，日志统一输出到标准错误流。
- `storage` 只定义接口与存储错误，不依赖具体驱动；`storage/sqlite` 实现连接、迁移与各实体仓储。
- `sshprobe` 只做网络与协议标识探测，不进行身份认证，不依赖 `cli` 或 `storage`。
- `sshclient` 提供 SSH 连接层，内部按职责分为：凭据提供器（`CredentialProvider`）、
  主机密钥信任（`HostKeyStore` / `KnownHostsStore`）、连接管理（`Dialer` / `Client`）、
  远程执行（`Run` 与 `CommandResult`）、SFTP 传输（`FileTransfer`）与远程工作空间
  （`ResolveHome` / `ResolveWorkspace` / `EnsureWorkspace`）。它不依赖 `cli`、`storage`
  或 `app`，业务代码通过接口注入依赖，不在各处直接创建 SSH 客户端。

## 领域模型边界

| 概念 | 含义 | 归属 |
|---|---|---|
| Role | 用户自定义角色，含类型、模型、提示词与工具策略 | domain |
| Requirement | 用户提交的原始需求 | domain |
| Task | 某需求的一次完整执行，持有工作区 | domain |
| Step | 计划中的业务步骤 | domain |
| Attempt | 某个 Step 的一次执行尝试 | domain |
| Session | 一次具体的 Pi 会话 | domain |
| VerificationResult | 某次验证的结构化结果 | domain |
| ExecutionHost | 通过 SSH 管理的远程执行节点（连接目标与工作空间） | domain |

- Step / Attempt / Session 是三个不同层次，不得合并为一个对象。
- 重试必须产生新的 Attempt 与新的 Session，历史记录只追加不覆盖。
- 状态只能通过 `Transition`（或实体自身的结束方法）变更，不允许直接赋值绕过校验。
- 角色名称由用户自由命名，工作流只依据角色类型识别职责。
- `ExecutionHost` 只保存连接目标、认证方式种类、本机私钥文件路径、远程工作空间与主机密钥
  信任信息，**不保存任何凭据材料**；密码与私钥口令只能通过内存中的凭据提供器在连接时提供。
- `AuthMethod` 只描述认证方式种类（`agent`、`private_key`、`password`），与具体凭据内容解耦，
  便于后续接入系统密钥链或其他凭据存储而不改动数据模型。

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

## 外部进程边界

- Pi 通过独立子进程与 JSONL RPC 交互，协议以官方文档与当前安装版本为准。
- 必须处理启动失败、非零退出码、消息解析失败、超时、取消、意外退出、输出流关闭与子进程未退出。
- 子进程的终止与回收统一由 `process` 负责，其他包不直接管理进程。

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
- 凭据边界：`CredentialProvider` 只负责在连接时提供材料，`Credential.Clear` 在使用后清零
  内存；密码、私钥口令与私钥原文不得进入数据库、配置文件、命令行参数或日志。
- 远程命令执行以结构化命令描述（程序名 + 参数数组）构造，参数统一经过 POSIX Shell 转义，
  禁止把用户输入直接拼接进命令字符串。
- 超时与取消只关闭对应的远程会话，不关闭整条连接，避免一个命令影响同连接上的其他操作。
- 日志中不得出现密码、私钥或其他凭据材料。

## 工作区边界

- 每个 Task 拥有独立 Git Worktree，同任务多步骤共享该工作区。
- 未经明确配置，不得自动删除包含未提交代码的工作区。
- 执行节点的远程工作空间默认 `~/.lingting/agentflow`，`~` 指远程 SSH 登录用户的 Home；
  路径遵循 POSIX 语义。SFTP 不会展开 `~`，因此必须先通过已认证连接取得远程 Home，再把
  配置解析为远程绝对路径；目录创建只在解析出的工作空间路径内进行，且必须显式授权。
  本阶段不创建项目配置、Dockerfile 或容器目录结构。

## 安全边界

- HTTP 服务默认只监听 `127.0.0.1`，不开放任意来源 CORS。
- 不存储模型 API 密钥，凭据由 Pi 及其 Provider 管理。
- 角色权限采用最小化原则，实际权限通过 Agent 配置与执行环境实施。

## 文档边界

- 文档只描述已实现的能力，不提前声明可用命令或接口。
- 代码编辑期间不同步文档，仅在明确要求时更新。
