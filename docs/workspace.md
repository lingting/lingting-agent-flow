# 工作区与代码组织

本文件只回答两个问题：代码放在哪个目录，以及依赖可以指向哪里。各条执行链路的边界、状态规则与
数据事实见 [architecture.md](architecture.md)，测试目录与测试规范见 [testing.md](testing.md)。

## 1. 仓库布局

```text
cmd/agentflow/            产品主程序入口（CLI 与服务启动）
cmd/agentflow-ci/         CI 准入门禁程序
cmd/agentflow-release/    发布制品构建程序
internal/                 产品实现（见下节包职责）
lingting-agent-flow-ui/   React + TypeScript 前端（独立 pnpm 工程）
configs/                  示例配置
docs/                     工程文档
tests/                    全部测试代码（见 testing.md）
.steps/                   规划与阶段性成果文档（V1 收口、V2 设计与里程碑）
```

## 2. 包职责

| 目录 | 职责 |
|---|---|
| `internal/domain/` | 实体、状态、状态迁移与业务规则；不依赖数据库、HTTP、Agent 运行时、外部进程 |
| `internal/app/` | 应用服务，CLI 与 HTTP 的共同入口；编排领域能力，不含协议细节 |
| `internal/httpapi/` | HTTP 路由与处理器，只做协议转换与 SSE 推送 |
| `internal/httpserver/` | HTTP 服务器装配与请求分发 |
| `internal/server/` | 统一服务装配：配置、数据库、后台服务与 HTTP 服务器 |
| `internal/cli/` | Cobra 命令实现，只做参数解析、结果输出与退出码转换 |
| `internal/lifecycle/` | 后台服务注册、启动、故障传播与逆序停止 |
| `internal/config/` | 配置结构与 YAML 加载、校验 |
| `internal/logging/` | 结构化日志封装（`log/slog`） |
| `internal/storage/` | 存储接口与统一存储错误 |
| `internal/storage/sqlite/` | 连接、Schema 初始化与仓储实现（`schema.sql` 是唯一 Schema 文件） |
| `internal/execenv/` | 统一执行环境契约（纯类型与纯函数） |
| `internal/agent/` | Agent 接口与 Pi CLI 适配器、容器 Runner |
| `internal/process/` | 外部进程生命周期管理（启动、输出、超时、取消、回收） |
| `internal/source/` | 代码获取抽象（`Fetcher`）与 Git Clone 实现 |
| `internal/codesaver/` | 代码保存：提交、推送、可选合并与只读核验 |
| `internal/sshprobe/` | SSH 端点探测（TCP 可达性与协议标识） |
| `internal/sshclient/` | SSH 连接层：主机密钥校验、私钥认证、远程执行、SFTP、远端套接字 |
| `internal/docker/` | 远程 Docker 命令构造、版本与 API 兼容性、镜像元数据查询 |
| `internal/dockerfile/` | Dockerfile 静态解析（多阶段、`AS` 别名、`ARG`、`--platform`） |
| `internal/webui/` | 前端产物嵌入与静态资源服务 |
| `internal/cigate/` | CI 准入门禁 |
| `internal/release/` | 发布制品构建、清单、校验和与来源证明 |

前端目录内部分层见 [frontend.md](frontend.md)。

## 3. 目标架构

```text
CLI / Web UI
      |
Application Services
      |
Scheduler / Workflow
      |
Container Agent Runner / Docker Exec
      |
Pi CLI（容器内）
      |
SQLite
```

- **CLI / Web UI**：只负责发起操作与展示结果。
- **Application Services**：CLI 与 HTTP 的共同入口，编排各领域能力。
- **Scheduler / Workflow**：任务领取与执行推进，均在 `agentflow server` 进程内。
- **Container Agent Runner / Docker Exec**：在受管理任务容器内以独立子进程驱动 Pi。
- **SQLite**：唯一真相来源。

不存在本机 Pi 调用路径：所有 Agent 调用都在远程任务容器内执行。

## 4. 依赖方向

依赖只能自上向下，不得反向：

```text
cmd/agentflow → cli / httpapi / server
cmd/agentflow-ci → cigate
cmd/agentflow-release → release
server → httpserver / app / lifecycle / config / storage
httpserver → httpapi / webui / app
cli / httpapi → app
app → agent / process / source / codesaver / execenv / docker / sshclient / storage / config / logging / lifecycle
storage/sqlite → storage / domain
domain → 仅 Go 标准库

约束：

- `domain` 处于最底层，只依赖 Go 标准库，禁止 import 其他内部包，也不得感知数据库、HTTP、
  JSONL、子进程、Docker 与文件系统。
- `app` 是唯一的编排层：CLI 与 HTTP 不得各自实现工作流或状态迁移，`httpapi` 与前端不得复制
  准入、占用、验证与收口判断。
- `app` 之上不得直接访问 SQLite，所有持久化经由 `storage` 接口。
- `httpapi`、`cli`、`webui` 不得包含状态迁移逻辑，也不得自行拼接 Pi、Docker 或 Git 命令。
- `config` 可以复用 `domain.RoleType` 的取值校验，但配置结构自身保持独立。
- 前端不访问数据库，不执行 SSH、Docker、Git 或 Pi 命令，只消费 HTTP API。
- 没有对应能力的目录不预先创建，避免只剩接口没有使用场景的抽象。

## 5. 代码职责划分

- 每个文件保持单一、可描述的主要职责；职责出现分化时按职责拆分，不按固定行数机械拆分。
- 同一职责不拆到多个文件，不同职责不堆在同一文件。
- 新增能力先确认归属层，再决定放在哪个包；跨层实现视为架构缺陷，而不是风格问题。
