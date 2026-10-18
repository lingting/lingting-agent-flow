# AgentFlow

AgentFlow 是一个使用 Go 编写的本地优先 AI 编码工作流编排器。

它把编码需求管理为持久化任务，并通过相互隔离的 Pi Agent 会话，协同完成规划、实现、验证与自动修复。

## 当前开发阶段

AgentFlow 处于**执行节点接入阶段**。当前仓库包含工程骨架、可运行的 CLI、配置加载与校验、结构化日志封装、SQLite 数据模型与版本化迁移，以及执行节点管理与完整的 SSH 连接层。

执行节点已支持完整的 SSH 身份认证（SSH Agent、本机私钥、内存密码）、严格的主机密钥校验、带超时与取消的远程命令执行、SFTP 文件传输，以及远程 Home 与工作空间解析。**Docker 环境、远程代码仓库操作与工作流引擎尚未实现**。文档只会在对应能力落地后才声明其可用。

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
- SSH 身份认证：支持 SSH Agent、本机私钥（含加密私钥口令）与内存密码三种方式；密码与私钥口令只在连接时于内存中传递，不写入数据库、日志或命令行参数。
- 主机密钥校验：复用 OpenSSH `known_hosts` 并维护 AgentFlow 自己的信任记录文件；未知主机必须核验 SHA-256 指纹后显式信任，已信任密钥发生变化时拒绝连接，禁止跳过校验。
- 主机密钥类型协商：按 `known_hosts` 中该主机已记录的类型依次尝试握手，取第一个校验
  通过的；没有任何记录时交给 `golang.org/x/crypto/ssh` 自身协商。
- 远程命令执行：返回退出码、标准输出、标准错误、耗时与结果状态，区分成功、非零退出、超时、取消与连接中断；输出超过上限时标记截断，取消与超时只关闭对应会话而不影响同连接的其他操作。
- SFTP 文件操作：创建目录、属性查询（含符号链接识别）、目录列举、上传、下载、重命名与删除；上传先写同目录临时文件并校验字节数，覆盖提交优先使用 `posix-rename` 原子替换；下载支持大小上限，失败不留下最终文件。
- 远程 Home 与工作空间：通过已认证连接解析远程登录用户 Home，把 `~/.lingting/agentflow` 或自定义路径解析为远程绝对路径，并检查存在性与可写性。
- 统一服务入口：`agentflow server` 在同一个进程内启动 HTTP API、内嵌 Web 页面与后台服务，按依赖顺序初始化并按逆序优雅关闭，默认只监听 `127.0.0.1:26380`。
- 最小只读接口：`GET /api/v1/health` 分别报告 HTTP API 与 SQLite 的可用性，`GET /api/v1/system/info` 返回构建信息；错误响应统一为 `{"error": {...}}`。
- 单文件交付：前端生产构建产物通过 `//go:embed` 嵌入 Go 可执行文件，`make release` 是唯一推荐的生产构建入口。

第一版目标（尚未实现）：

- 持久化任务队列，并限制同时执行的任务数量。
- 结构化、分步骤的开发计划。
- 每次执行尝试使用独立的 Agent 会话。
- 以验证结果驱动的修复循环。
- 相互隔离的 Git Worktree。
- 可恢复的任务状态。
- 业务 Web 页面（当前只有 Web 外壳与连接状态）。

## 技术栈

- Go（版本要求见 `go.mod`）
- Cobra（CLI）
- `gopkg.in/yaml.v3`（配置）
- `log/slog`（日志）
- `database/sql` + `modernc.org/sqlite`（持久化，纯 Go 驱动，不依赖 CGO）
- Go 标准库 `net`、`context`（SSH 端点探测）
- `golang.org/x/crypto/ssh`（SSH 握手、主机密钥校验与身份认证）
- `github.com/pkg/sftp`（SFTP 文件传输）
- Go 标准库 `testing`（测试）
- Makefile（构建入口）

Pi RPC、Git Worktree、React 与 TypeScript 属于后续阶段的技术选型，当前尚未引入。

## 环境依赖

- 受支持的 Go 工具链
- Git（`make build` 用它注入提交号）
- Make（可选，也可直接使用 `go` 命令）

只有 `agent check` 与 `agent test` 需要 Pi：前者检查 PATH 中的 `pi` 及其非交互式参数，
后者还会用角色配置的模型发起一次真实调用，因此需要对应 Provider 的凭据。其余命令
（`config`、`version`、`host`、`server`）不依赖 Pi，也不需要模型凭据。

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

## 统一服务入口

`agentflow server` 是 AgentFlow 完整应用服务的启动命令，Web 服务与后台服务由同一个进程管理：

```text
配置校验 → 日志与公共依赖 → SQLite → 后台服务 → HTTP 服务
关闭顺序完全相反：HTTP 服务 → 后台服务 → SQLite
```

- 任一必需组件启动失败时，命令返回明确错误，已启动组件按逆序停止，不留下监听端口与 goroutine。
- 数据库是必需组件：初始化失败时服务不会启动，也不会带着不可用的存储继续运行。
- HTTP 服务退出或关键后台服务异常退出时，统一入口会感知并进入关闭流程，而不是留下半运行状态。
- 当前版本还没有调度器与执行器等后台业务模块；后续模块通过 `internal/lifecycle` 的
  `Service` 契约（`Name`/`Start`/`Stop`）注册进同一入口，不在 HTTP Handler 中自行创建。

## Web 界面与单文件交付

AgentFlow 的 Web 界面、HTTP API 与后台服务由同一个 Go 进程提供。生产环境只交付一个
可执行文件，前端构建产物通过 `//go:embed` 嵌入其中；部署时不需要 Node.js、前端 `dist/`
目录或额外的静态文件服务器。

### 正式构建（唯一推荐入口）

```bash
make release
```

该命令先按 `pnpm-lock.yaml` 安装前端依赖并执行前端生产构建，把产物写入 `internal/webui/dist/`，
然后编译 Go 程序，最终可执行文件为 `bin/agentflow`。构建顺序不可颠倒：先编译 Go 再构建前端
会让二进制中保留旧 UI。

前端构建失败、`index.html` 缺失或 `assets/` 中缺少 `.js`、`.css` 时构建立即失败，不会产出
"看起来成功但实际没有页面"的可执行文件。

构建完成后把 `bin/agentflow` 复制到任意目录，只启动它即可访问页面：

```bash
./bin/agentflow server
# AgentFlow 服务已启动: http://127.0.0.1:26380
```

### 开发模式

开发模式保留两个进程：

```bash
# 终端 1：Go 服务，提供 /api 接口与后台服务
make serve              # 等价于 go run ./cmd/agentflow server --require-web-ui=false

# 终端 2：Vite+ 开发服务器，负责页面热更新
make web-dev            # 等价于 cd lingting-agent-flow-ui && pnpm dev
```

开发服务器监听 `127.0.0.1:26390`，并把 `/api` 代理到 `http://127.0.0.1:26380`。需要其他后端
地址时设置 `AGENTFLOW_DEV_API_TARGET`；该变量只影响开发代理，不会进入生产构建产物。

`agentflow server` 默认要求二进制内已包含前端产物，缺少时启动即失败；开发阶段页面由 Vite+
提供，因此用 `--require-web-ui=false` 关闭该校验，此时后端仍提供 API，页面请求返回说明原因
的诊断页。

### HTTP 接口与路径分发

所有后端接口使用 `/api` 前缀，当前版本挂在 `/api/v1` 下。请求分发规则：

| 请求路径 | 处理方式 |
| --- | --- |
| `/api`、`/api/...` | 交给 API 处理器；未注册路径返回统一 JSON 404，永不回退页面 |
| `/assets/...` 与真实存在的静态资源 | 返回嵌入的构建产物 |
| 缺失的静态资源 | 返回 404，不返回 `index.html` |
| 其他 GET/HEAD 页面路径 | 回退到 `index.html`，由前端路由展示 |
| 非 GET/HEAD 的页面请求 | 返回 405 |

`/api` 是保留路径空间，判定按路径段边界进行：`/apiary`、`/apix` 不是 API 路径，按页面处理。

当前已实现的接口：

| 方法   | 路径                            | 说明                                                  |
| ------ | ------------------------------- | ----------------------------------------------------- |
| GET    | `/api/v1/health`                | HTTP API 与 SQLite 可用性；存在不可用依赖时返回 503    |
| GET    | `/api/v1/system/info`           | 程序版本、提交、构建时间、Go 版本与编译平台            |
| POST   | `/api/v1/tasks`                 | 创建任务，成功返回 201 与完整任务对象                  |
| GET    | `/api/v1/tasks`                 | 任务列表，支持分页与状态筛选                          |
| GET    | `/api/v1/tasks/{taskID}`        | 任务详情                                              |
| POST   | `/api/v1/tasks/{taskID}/cancel` | 请求取消任务                                          |
| DELETE | `/api/v1/tasks/{taskID}`        | 逻辑删除任务，成功返回 204                            |

`/api/v1/health` 的 `checks` 分别表达 `http_api` 与 `database`，不会因为 HTTP 服务已启动就
认为数据库一定可用。错误响应统一为 `{"error": {"code", "message", "request_id"}}`，
错误代码取值为 `invalid_request`、`not_found`、`state_conflict`、`method_not_allowed`、
`internal_error`，分别对应 HTTP 400、404、409、405、500。

#### 任务状态

任务状态取值与 `internal/domain` 完全一致，API 不提供第二套名称：

| 状态                | 含义                                     |
| ------------------- | ---------------------------------------- |
| `queued`            | 已创建，尚未执行（新建任务的唯一初始状态）|
| `preparing`         | 准备执行环境                             |
| `planning`          | 正在生成计划                             |
| `planned`           | 计划已确认，等待执行步骤                 |
| `developing`        | 正在执行开发步骤                         |
| `verifying`         | 正在执行验证                             |
| `saving`            | 正在保存代码（Commit / Push / 可选 Merge）|
| `cancelling`        | 已请求取消，尚未确认停止                 |
| `retrying`          | 按重试策略准备新的尝试                   |
| `paused`            | 人工介入后暂停                           |
| `recovery_required` | 执行结果或资源状态无法确认，需要核验     |
| `completed`         | 所有必需阶段成功                         |
| `failed`            | 任务执行失败                             |
| `cancelled`         | 取消流程已完成                           |

响应中的两个布尔字段表达不同语义，不能互相替代：

- `status_terminal` 表示任务是否处于终态，只有 `completed` 与 `cancelled` 为真；
  `failed` 允许用户显式重试，因此不是终态。
- `status_finished` 表示任务是否已停止执行，`completed`、`failed`、`cancelled` 为真。
  管理接口用它判定可用操作：已停止执行的任务可以删除、不可再次取消。

#### 创建任务

```http
POST /api/v1/tasks
Content-Type: application/json

{
  "name": "实现登录接口",
  "description": "为登录页接入真实接口",
  "project_id": "demo-service",
  "host_id": "<执行节点标识>",
  "repository_url": "ssh://git.example.com/team/repo",
  "base_branch": "main",
  "environment": "go",
  "planner_role_id": "<规划角色标识>",
  "developer_role_id": "<开发角色标识>"
}
```

`name`、`description`、`host_id`、`repository_url`、`base_branch`、`environment`、
`planner_role_id`、`developer_role_id` 必填；其余字段可选。任务名称与描述写入关联需求，
`name` 最长 200 字符，`description` 最长 4000 字符。客户端**不能**指定 `status`、
`created_at`、`started_at`、`finished_at`；出现未知字段时请求被拒绝。

创建只落库，不触发执行：新任务固定为 `queued`，`started_at` 与 `finished_at` 为空。
相同的 `idempotency_key` 重复提交不会创建第二个任务。

#### 任务列表

```http
GET /api/v1/tasks?current=1&size=20&status=queued&status=failed
```

- `current` 为页号，从 1 开始，默认 1；`size` 为页大小，默认 20，最大 100。
- 非法或超限参数返回 400，不会退化为全表查询。
- `status` 可重复或使用逗号分隔；未知状态返回 400。
- 排序固定为创建时间倒序，同一毫秒内按写入顺序倒序，分页结果稳定。
- 已逻辑删除的任务不出现在列表与总数中。

响应：

```json
{
  "records": [
    {
      "id": "<任务标识>",
      "name": "实现登录接口",
      "description": "为登录页接入真实接口",
      "status": "queued",
      "status_terminal": false,
      "status_finished": false,
      "project_id": "demo-service",
      "cancel_requested": false,
      "created_at": "2025-01-01T00:00:00Z",
      "updated_at": "2025-01-01T00:00:00Z"
    }
  ],
  "total": 1,
  "current": 1,
  "size": 20
}
```

时间统一为 UTC 的 RFC 3339 字符串；`started_at`、`finished_at`、`cancel_requested_at`、
`error` 只在有值时出现。

#### 取消任务

```http
POST /api/v1/tasks/{taskID}/cancel
```

- `queued` 任务直接收敛为 `cancelled`，并记录取消请求时间与结束时间。
- 其他未结束状态只记录取消请求并进入 `cancelling`，实际停止由执行者确认；此时不写入
  最终结论，也不写 `finished_at`。
- 已处于 `cancelling` 时重复请求幂等，返回当前状态且不改写请求时间。
- 已结束任务返回 409 `state_conflict`。

#### 删除任务

```http
DELETE /api/v1/tasks/{taskID}
```

- 只有 `completed`、`failed`、`cancelled` 允许删除，其他状态返回 409。
- 删除是逻辑删除：只写 `tasks.deleted_at`，任务行、需求、步骤、尝试、会话与事件全部保留。
- 成功返回 204 且无响应体；删除后详情返回 404，重复删除返回 404。

#### 当前能力边界

本阶段的任务 API **只负责管理数据**，不代表任务已经执行：

- 不会调用 Pi CLI、不会创建 Docker 容器、不会连接 SSH、不会克隆 Git 仓库。
- 没有执行器，因此没有任务会被自动推进到 `running` 或 `succeeded` 等执行状态。
- 执行节点与角色没有查询接口，创建任务时需要手工填写其标识。

`/api` 下的未知路径返回 JSON 404，不会回退到前端页面；`/api` 之外的未知页面路径进入 SPA
回退，因此浏览器刷新任意前端路由都能正常打开页面。

监听地址默认是 `127.0.0.1:26380`，可在配置文件 `server` 段或 `--address` 参数中修改；优雅
关闭超时通过 `server.shutdown_timeout_seconds` 设置（0 表示默认 10 秒）。默认只监听回环
地址，未实现远程访问认证前不会默认监听所有网卡。

### 尚未实现的接口

项目、机器、环境、容器、代码保存与 Agent 调度均**没有** HTTP 接口。Web 页面中的对应入口仍为
占位说明，连接指示器只表示 HTTP API 的可用性，不代表 SSH、Docker 或 Pi Agent 已就绪。
任务页面已接入真实接口，但只能创建、查看、取消与删除任务记录，不能启动执行。

后台服务目前只有生命周期框架：任务调度器、任务执行器、SSH 与 Docker 运维均尚未接入，
`agentflow server` 启动后只提供 API 与页面。

## 前端工程

前端源码位于 `lingting-agent-flow-ui/`，依赖管理与构建工具为 pnpm 与 Vite+：

```bash
cd lingting-agent-flow-ui
pnpm install
pnpm dev                     # vp dev
pnpm build                   # 生产构建，写入 ../internal/webui/dist
pnpm lint                    # 格式、lint 与类型检查
pnpm test                    # 单元测试（vp test run）
pnpm exec playwright test    # 浏览器测试
```

前端通过 `src/lib/api/` 的统一客户端访问 API，路径全部是同源相对路径（如 `/api/v1/health`）。
测试产物写入操作系统临时目录下的随机目录并保留，不自动清理。

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

# 启动完整服务（HTTP API、内嵌页面与后台服务）
./bin/agentflow server --config configs/example.yaml --address 127.0.0.1:26380

# 执行节点：配置、认证、主机密钥与远程工作空间
./bin/agentflow host add --id node-a --host 192.0.2.10 --user deploy --auth agent
./bin/agentflow host probe node-a     # 只探测 TCP 与 SSH 协议标识
./bin/agentflow host trust node-a     # 探测并展示主机密钥指纹
./bin/agentflow host test node-a      # 完整 SSH 身份认证
./bin/agentflow host workspace node-a # 解析远程 Home 与工作空间
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

执行节点是通过 SSH 管理的远程 Linux 机器。AgentFlow 控制程序运行在本机，通过 SSH 连接
远程节点；节点记录只保存连接目标、认证方式种类、本机私钥文件路径与远程工作空间配置，
**不保存密码、私钥原文或私钥口令**。

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
| `--auth` | 认证方式，`agent`（默认）、`private_key` 或 `password` |
| `--key` | 本机私钥文件路径，仅 `--auth private_key` 时使用，且该方式必填 |
| `--default` | 同时设为默认执行节点 |

该命令只写入本机数据库，**不会**连接目标机器、不会创建远程目录，也不代表可以登录。
该命令不接受密码或私钥口令，凭据只在连接时于内存中提供。

### 修改执行节点

```bash
./bin/agentflow host update node-build-01 --port 2222
./bin/agentflow host update node-build-01 --auth private_key --key ~/.ssh/id_ed25519
```

只修改显式给出的参数，未给出的配置保持原值。切换认证方式时需要同时给出该方式所需的
参数，例如 `--auth private_key --key ...`。该命令同样不执行登录，也不接受密码或口令。

### 列出与查看

```bash
./bin/agentflow host list
./bin/agentflow host show node-build-01
```

`host list` 输出标识、名称、地址、用户名、认证方式、工作空间、默认标记与启用状态。
`host show` 额外显示私钥文件路径、主机密钥算法与指纹、信任时间以及最近一次连接结果。

### 设置默认节点

```bash
./bin/agentflow host set-default node-build-01
```

默认节点在单个事务内切换，数据库保证最多只有一个默认节点。节点不存在或已被禁用时
返回明确错误。

### SSH 身份认证

三种认证方式都通过统一的凭据接口在连接时提供材料，凭据不落盘、不进数据库、不进日志：

| 认证方式 | 凭据来源 | 说明 |
|---|---|---|
| `agent` | 系统 SSH Agent（`SSH_AUTH_SOCK`） | 默认方式，不在配置中保存任何机密材料 |
| `private_key` | 节点配置中的本机私钥文件路径 | 支持加密私钥，口令在连接时提供 |
| `password` | 调用方在内存中提供 | 默认不持久化 |

凭据输入方式（`host test`、`host workspace`、`host transfer`）：

- `--password-stdin`：从标准输入读取一行密码。
- `--passphrase-stdin`：从标准输入读取一行私钥口令。
- `--ask-pass`：在交互式终端中强制使用隐藏输入。

**非交互环境不会挂起等待输入**：没有可用凭据时立即返回「没有可用的凭据」，
并提示可用的输入参数。

### 主机密钥校验

主机密钥校验不可关闭，也不存在"跳过校验"的开关。

- 校验时同时读取本机用户 OpenSSH 的 `known_hosts` 与 AgentFlow 自己的信任记录文件；
  只读的 `known_hosts` 不会被修改，新记录只写入 AgentFlow 的记录文件。
- 未知主机的连接**必须失败**，并展示算法与 SHA-256 指纹；只有在用户核验指纹并显式
  确认后才会写入信任记录。
- 已信任的主机提供不同密钥时立即拒绝连接，并同时展示当前指纹与已信任指纹，不会自动
  覆盖旧记录。
- 服务端可能同时提供多种主机密钥（例如 ed25519 与 ECDSA），而 `known_hosts` 里可能只
  记录了其中一部分。程序按记录顺序逐个尝试：每次只用一种已记录的类型握手，校验通过并
  完成认证即成功；某个类型校验失败或服务端不再提供该类型时换下一个。全部已记录类型都不
  可用时，返回第一个失败类型对应的结论（未知主机或密钥已变化）。
- 没有任何记录时不做额外限制，交给 `golang.org/x/crypto/ssh` 自身协商；首次信任后实际
  使用的类型会被记录下来，后续连接优先复用它。

```bash
# 只探测主机密钥并展示指纹与信任状态（不执行身份认证）
./bin/agentflow host trust node-build-01

# 核验指纹后显式写入信任记录（非交互执行必须显式给出 --yes）
./bin/agentflow host trust node-build-01 \
  --fingerprint SHA256:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx \
  --yes
```

`--fingerprint` 必须与用户实际核验过的指纹一致；命令会重新探测服务端密钥并在指纹不符时
拒绝写入。

### 测试 SSH 认证连接

```bash
./bin/agentflow host test node-build-01
./bin/agentflow host test node-build-01 --check-workspace
./bin/agentflow host test node-build-01 --create-workspace --timeout 30s
```

该命令执行完整的身份认证，而不是只探测 TCP 可达性。成功后输出认证状态、远程用户、
远程 Home 与（可选）工作空间路径；失败时按阶段给出可行动的提示：

| 结果类别 | 含义 |
|---|---|
| `invalid_config` | 节点配置或认证配置非法 |
| `unreachable` | DNS 解析或 TCP 连接失败 |
| `handshake_failed` | TCP 已连接但 SSH 协议握手失败 |
| `host_key_unknown` | 主机密钥未知，需要核验指纹后信任 |
| `host_key_mismatch` | 主机密钥与已信任记录不一致 |
| `host_key_revoked` | 主机密钥已被标记为吊销 |
| `auth_failed` | 登录认证被拒绝 |
| `no_credential` | 没有可用的凭据 |
| `private_key_error` | 私钥文件不存在、格式不支持或口令错误 |
| `agent_unavailable` | SSH Agent 不可用或没有身份 |
| `timeout` | 连接或认证超时 |
| `cancelled` | 调用被取消 |

最近一次连接结果会写入节点记录（只保存结果类别，不保存错误详情与凭据）。

### 解析远程工作空间

```bash
./bin/agentflow host workspace node-build-01
./bin/agentflow host workspace node-build-01 --create
```

该命令通过已认证连接取得远程登录用户的 Home，把节点配置的工作空间解析为远程绝对路径，
并检查目录是否存在与可写。未指定 `--create` 时不会创建目录；创建只发生在解析出的工作
空间路径内，不会切换到 `/tmp`、`/root` 等其他目录。输出包含配置值、远程 Home、解析后的
绝对路径、存在/创建/可写状态。

### SFTP 文件传输（诊断命令）

```bash
./bin/agentflow host transfer upload node-build-01 ./local.txt /srv/agentflow/remote.txt
./bin/agentflow host transfer download node-build-01 /srv/agentflow/remote.txt ./local.txt
./bin/agentflow host transfer download node-build-01 /srv/agentflow/remote.txt ./local.txt \
  --max-bytes 1048576
```

这两个子命令用于验证 SSH/SFTP 基础能力，**不代表项目代码同步已经开始**。

- 远程路径必须是 Linux 绝对路径，不允许包含 `..` 段。
- 上传先写入同目录临时文件，校验写入字节数与远程文件大小后再提交；覆盖需要显式给出
  `--overwrite`，提交优先使用 `posix-rename` 原子替换，服务端不支持时退化为删除后重命名。
- 下载先写入本机同目录临时文件，成功后才重命名到目标路径；默认不覆盖已存在的本机文件，
  默认权限为 `0600`；`--max-bytes` 可限制允许下载的文件大小，超限时不产生本机文件。
- 删除接口只删除普通文件，不会删除目录，也不会删除符号链接。
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
- **认证连接**（`host test`、`host workspace`、`host transfer`）才执行主机密钥校验与身份
  认证，是唯一可以执行远程命令与文件传输的路径。
- 探测成功不等于登录成功，也不代表远程 Linux 环境已经验证通过；探测结果中的认证状态
  恒为未认证，用于阻止调用方误判。

### 远程工作空间路径

- 默认值为 `~/.lingting/agentflow`。
- `~` 指**远程 SSH 登录用户的 Home**，不是本机用户的 Home。
- 路径遵循 POSIX 语义，必须使用 `/`，可以是以 `/` 开头的绝对路径或以 `~/` 开头的路径。
- 不接受根目录 `/`、`~` 本身、相对路径、`\` 分隔符、连续 `//` 与 `..` 上级引用。
- `host add` 只保存配置；`host workspace` 才会通过已认证连接解析远程 Home 并转换为绝对
  路径。目录创建只发生在解析出的工作空间路径内，且必须显式给出 `--create`。

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
| `ssh.known_hosts_path` | 额外的只读 `known_hosts` 文件；留空时复用本机用户 OpenSSH 的 `known_hosts` |
| `ssh.known_hosts_store_path` | AgentFlow 自己写入主机密钥信任记录的文件；留空时放在数据库文件所在目录下的 `known_hosts` |
| `ssh.connect_timeout_seconds` | TCP 连接与 SSH 认证的超时秒数，`0` 表示使用默认值，不得为负数 |
| `roles[].id` | 角色标识，不能为空且必须唯一 |
| `roles[].name` | 角色名称，可自由命名，不能为空 |
| `roles[].type` | 角色类型，只能是 `planner`、`developer`、`validator` |
| `roles[].runner` | Agent 运行时，当前只支持 `pi` |
| `roles[].model` | 模型标识，不能为空；示例中的值为占位符 |
| `roles[].system_prompt` | 系统提示词，不能为空 |
| `roles[].tools_policy.mode` | 工具策略模式，需与角色类型匹配：`planner` → `read_only`、`developer` → `development`、`validator` → `verification` |

工作流只依据 `type` 识别角色职责，`name` 仅用于展示。

`tools_policy` 当前只是配置声明，用于后续阶段定义角色权限，**并不代表 Pi 已经执行了对应限制**。

`ssh` 段只保存路径与超时，**不保存任何凭据**；密码与私钥口令只能在连接时通过内存中的凭据
提供器传入。
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
| `execution_hosts` | 通过 SSH 管理的执行节点 | `port` 限定 1-65535；`is_default` / `enabled` 只能为 0/1；`auth_method` 限定 `agent`/`private_key`/`password`；私钥认证必须给出 `private_key_path`；指纹必须以 `SHA256:` 开头；`last_connect_error` 只保存稳定分类代码；部分唯一索引保证最多一个默认节点；**不保存任何凭据字段** |
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
make test-race    # 竞态检测（单元测试与 SSH 连接层）
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
go test -race -count=1 ./tests/unit/...                             # 竞态检测（单元测试）
go test -race -count=1 ./tests/integration/local/ssh/...            # 竞态检测（SSH 连接与并发）
```

每次测试入口启动时在系统临时目录下创建随机工作空间，数据库、Session、日志与生成文件
全部落在其中，并把绝对路径输出为 `TEST_ARTIFACT_DIR`。测试结束只关闭运行时资源，
**不删除测试产物**，便于检查失败现场。快速单元测试与本机集成测试不访问真实模型或远程
网络；本机没有 `pi` 时真实 Pi 用例输出警告并跳过。

SSH 连接层按三层覆盖：`tests/unit/ssh/`（主机密钥、认证配置、路径与结果状态，不启动
服务端）、`tests/integration/local/ssh/`（进程内启动真实 SSH/SFTP 服务端，覆盖认证、
命令执行、取消与超时、SFTP 传输、并发与资源释放）、`tests/integration/ssh/`（真实远程
机器，需要 `AGENTFLOW_TEST_SSH_HOST` 与 `AGENTFLOW_TEST_SSH_USER`，缺失时警告并跳过；
未设置 `AGENTFLOW_TEST_SSH_KNOWN_HOSTS` 时复用本机 `~/.ssh/known_hosts`）。

测试分类、环境变量与编写约定见 [tests/README.md](tests/README.md)。

开发环境、代码风格与提交约定见 [docs/development.md](docs/development.md)，分层与依赖方向见 [docs/architecture.md](docs/architecture.md)。

## 当前未实现的功能

以下能力均在计划中，当前版本**不提供**：

- 需求队列、Worker 与并发调度。
- Pi RPC 客户端、子进程管理与 Agent 执行。
- Git Worktree 创建与清理。
- Planner / Developer / Validator 的实际执行。
- 验证失败回退与任务恢复。
- 任务、项目、机器、环境等业务 HTTP 接口与对应页面。
- 多 Agent Provider 与分布式执行。
- 角色管理 CLI 与 Web 页面（当前只有存储层的角色初始化与增删改查接口）。
- 远程 Docker 检测、Dockerfile 版本化、镜像构建与容器生命周期管理。
- 远程代码仓库操作与项目代码同步。
- 在远程容器内执行 Pi Agent。

## 安全与风险提示

AgentFlow 的最终形态会**执行外部程序并修改项目文件**，请注意：

- 后续版本的 Agent 会在你的仓库中创建与修改文件，并可能执行命令。
- 请使用相互隔离的工作区，并配置合理的 Agent 权限；提示词不是权限边界。
- 审查任何可能修改或删除数据的操作，尤其是工作区清理与 Git 操作。
- 不要在配置或仓库中保存真实 API Key；模型凭据由 Pi 及其 Provider 管理。
- 执行节点表只保存连接目标、认证方式种类、本机私钥文件路径与工作空间，**不保存密码、
  私钥原文或私钥口令**；私钥文件只被读取，不会被复制到工作空间或远程机器。
- 主机密钥校验不可关闭。未知主机必须核验 SHA-256 指纹后显式信任，已信任密钥发生变化时
  连接会被拒绝；不要为了让连接"先跑通"而手工删除信任记录。
- `host probe` 只验证 SSH 服务可达性，**不代表已经登录成功或远程环境已验证**。
- 远程命令超时或取消时，AgentFlow 会关闭对应会话通道，但 SSH 协议不保证远程进程一定
  已终止；结果中的终止确认标志为 false 时，不要假定远程进程已经退出。
- SFTP 上传覆盖优先使用 `posix-rename` 原子替换；服务端不支持该扩展时会退化为"删除旧
  文件后重命名"，此时替换不是原子的，请避免在关键文件上依赖强原子性。
- 本地 HTTP 服务默认只监听 `127.0.0.1`，且不存储模型凭据。Web 连接指示器只表示 HTTP API 可用，不代表 SSH、Docker 或 Pi Agent 已就绪。

## 许可证

许可证将在首次公开发布前确定。
