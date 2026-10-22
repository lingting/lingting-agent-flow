# AgentFlow

AgentFlow 是一个使用 Go 编写的本地优先 AI 编码工作流编排器。

它把编码需求管理为持久化任务，并通过相互隔离的 Pi Agent 会话，协同完成规划、实现、验证与自动修复。

## 当前开发阶段

AgentFlow 处于**单步骤执行阶段**。当前仓库包含工程骨架、可运行的 CLI、配置加载与校验、结构化日志封装、SQLite 数据模型与版本化迁移、执行节点管理与完整的 SSH 连接层、统一 Pi Runner，以及**单步骤执行协调器**。

执行节点已支持本机私钥认证、严格的主机密钥校验、带超时与取消的远程命令执行、SFTP
文件传输、远程 Home 与工作空间解析，以及**执行节点环境预检**（SSH 连接、Docker 版本与
API 兼容性、Docker CLI 帮助信息、基础 Dockerfile 的外部 `FROM` 镜像元数据）。

单步骤执行已打通 `HTTP API → Task Service → Step Execution Service → Pi Runner` 的闭环：
可以通过接口对**明确指定的 Step** 发起一次真实 Pi 调用，并把 Attempt、Session 与状态变化
持久化。在它之上还有单步骤开发—验证闭环与**任务级工作流编排器**：按数据库中已持久化的计划
顺序推进开发与验证步骤，并在收尾阶段复用 CodeSaver。

任务执行只存在于 `agentflow server` 进程内：旧的全能引擎包（`internal/workflow`）与
`agentflow task retry` / `task recover` 命令已移除，`agentflow task run` 改为调用
`POST /api/v1/tasks/{taskID}/execute-workflow` 并轮询真实进度。编排器不生成计划、不创建
容器：启动前要求任务已有受管理且运行中的容器与已持久化的计划步骤，否则返回前置条件错误或
状态冲突。**计划自动生成、自动重试、多任务并发调度仍未实现**。文档只会在对应能力落地后
才声明其可用。

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
- SSH 身份认证：只支持本机私钥认证（`private_key`）。节点配置只保存私钥文件路径，
  私钥文件只被读取，不复制、不修改、不写入数据库或日志；不支持 SSH Agent、密码认证
  与加密私钥（口令）。
- 主机密钥校验：复用 OpenSSH `known_hosts` 并维护 AgentFlow 自己的信任记录文件；未知主机必须核验 SHA-256 指纹后显式信任，已信任密钥发生变化时拒绝连接，禁止跳过校验。
- 主机密钥类型协商：按 `known_hosts` 中该主机已记录的类型依次尝试握手，取第一个校验
  通过的；没有任何记录时交给 `golang.org/x/crypto/ssh` 自身协商。
- 远程命令执行：返回退出码、标准输出、标准错误、耗时与结果状态，区分成功、非零退出、超时、取消与连接中断；输出超过上限时标记截断，取消与超时只关闭对应会话而不影响同连接的其他操作。
- SFTP 文件操作：创建目录、属性查询（含符号链接识别）、目录列举、上传、下载、重命名与删除；上传先写同目录临时文件并校验字节数，覆盖提交优先使用 `posix-rename` 原子替换；下载支持大小上限，失败不留下最终文件。
- 远程 Home 与工作空间：通过已认证连接解析远程登录用户 Home，把 `~/.lingting/agentflow` 或自定义路径解析为远程绝对路径，并检查存在性与可写性。
- 执行节点环境预检：新建与有效修改后自动进入 `checking`，由后台服务依次执行 SSH 连接、
  Docker 版本与 API 兼容性、Docker CLI 帮助信息与基础镜像元数据四项检查，全部通过才写
  `normal`；未通过校验的节点不能用于真实远程操作。Docker 兼容性检查不验证 Engine 运行契约。
- Docker 兼容性基线：CLI 与 Engine 最低 `29.0.0`，Engine API 基准固定 `1.52`；所有 Docker
  调用统一固定 API 基准，详见 [`docs/docker-compatibility.md`](docs/docker-compatibility.md)。
- 统一服务入口：`agentflow server` 在同一个进程内启动 HTTP API、内嵌 Web 页面与后台服务，按依赖顺序初始化并按逆序优雅关闭，默认只监听 `127.0.0.1:26380`。
- 只读与资源接口：`GET /api/v1/health` 报告 HTTP API 与 SQLite 可用性，`GET /api/v1/system/info`
  返回构建信息，`/api/v1/tasks` 提供任务管理，`/api/v1/hosts` 提供执行节点管理与环境预检
  状态；错误响应统一为 `{"error": {...}}`。
- 单文件交付：前端生产构建产物通过 `//go:embed` 嵌入 Go 可执行文件，`make release` 是唯一推荐的生产构建入口。
- 单步骤执行协调器：`POST /api/v1/tasks/{taskID}/steps/{stepID}/execute` 对指定 Step 发起一次
  真实 Pi 调用。校验任务、步骤、角色的归属与状态，核验执行节点与任务容器，以数据库事务
  原子占用步骤并创建新的 Attempt 与独立 Session，随后按真实结果写入 Attempt / Step / Task
  终态。同一 Step 同一时刻只允许一个执行占用。
- 单步骤开发—验证闭环：`POST /api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate`
  在同一个任务容器工作区内先执行 Developer，再执行确定性验证，并依据真实检查结果收口步骤
  状态。Developer 完成不等于验证通过：开发成功时步骤进入 `verifying`，只有必要检查全部通过
  才进入 `completed`。验证失败时保存结构化失败反馈供下次显式重试引用，并停止等待用户决定。
- 代码获取与任务分支管理：`POST /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code`
  与 `agentflow code init` 在对应 Attempt 的任务容器内执行 Git Clone，识别基础分支
  （`origin/HEAD` → `main` → `master`，或显式指定），创建并核验任务分支 `agent/<task-id>`，
  记录实际 HEAD。代码获取抽象为 `source.Fetcher`，第一版只注册 Git Clone 实现；初始化结果
  持久化到 `code_fetches`（来源、脱敏仓库地址、目标目录、基础分支与 commit、任务分支与来源、
  实际 HEAD、状态、失败阶段、错误类别与摘要、起止时间）。目标目录非空时直接失败；同一
  Attempt 已初始化成功时写入 `skipped` 记录并复用原 commit，不会重新 Clone。私有仓库认证
  尚未支持，需要认证时返回明确错误，不静默降级为匿名访问。

第一版目标（尚未实现）：

- 持久化任务队列，并限制同时执行的任务数量。
- 结构化、分步骤的开发计划的自动生成：当前没有任何入口生成并持久化计划步骤，任务级编排器
  只执行已经落库的 `steps`，缺少计划时 `domain.CheckWorkflowStart` 直接拒绝启动。
- 容器准备工作与执行请求的自动串联：当前需要先执行 `agentflow execenv prepare-task <task-id>`
  准备容器，再执行 `agentflow task run <task-id>`；编排器不会自动创建容器。
- Planner → Developer → Validator 的自动串联中的 Validator Agent 环节：当前只执行确定性
  检查，编排器不发起 Validator Session 调用。
- 自动重试与退避策略。
- 以验证结果驱动的修复循环（确定性验证与 Validator 阶段）。
- 相互隔离的 Git Worktree。
- 远程 Docker 容器创建、启动与销毁的自动编排（容器能力与 `execenv` 已提供，但不由工作流
  自动创建）。
- Commit、Push 与 Merge 的自动编排（代码获取与任务分支管理已提供接口与命令）。
- 可恢复的任务状态：旧的 `task retry` / `task recover` 命令已随 `internal/workflow` 引擎包
  一并移除，当前恢复只通过 `POST /api/v1/tasks/{taskID}/resume-workflow` 执行。
- 业务 Web 页面（当前只有 Web 外壳与连接状态）。

## 技术栈

- Go（版本要求见 `go.mod`）
- `golang.org/x/crypto/ssh`（SSH 握手、主机密钥校验、私钥认证与远端 Unix 套接字通道）
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
后者还会用角色配置的模型发起一次真实调用，因此需要对应 Provider 的凭据。
`agent run <container>` 在任务容器内部调用 Pi，要求该容器内已安装可用版本的 `pi`。
`agentflow server` 启动时不会调用 Pi，但会把统一 Pi Runner 注册为后台服务。
其余命令（`config`、`version`、`host`）不依赖 Pi，也不需要模型凭据。

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
| GET    | `/api/v1/tasks/{taskID}/steps`  | 步骤列表（分页、按序号稳定排序）                      |
| GET    | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts` | 步骤的尝试列表（分页、按序号）    |
| GET    | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/sessions` | 尝试的会话列表 |
| GET    | `/api/v1/tasks/{taskID}/sessions/{sessionID}` | 会话详情（归档与导出状态分列）      |
| POST   | `/api/v1/tasks/{taskID}/steps/{stepID}/execute` | 单步骤执行（真实 Pi 调用）             |
| POST   | `/api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate` | 单步骤开发—验证闭环 |
| POST   | `/api/v1/tasks/{taskID}/execute-steps` | 按序号显式顺序执行多个开发步骤        |
| POST   | `/api/v1/tasks/{taskID}/execute-workflow` | 启动任务级工作流执行，返回 202 与真实初始状态 |
| GET    | `/api/v1/tasks/{taskID}/workflow` | 查询工作流执行进度（任务级编排）      |
| POST   | `/api/v1/tasks/{taskID}/cancel-workflow` | 请求取消工作流执行            |
| POST   | `/api/v1/tasks/{taskID}/resume-workflow` | 恢复一次已停止的工作流执行    |
| POST   | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/validate` | 执行确定性验证 |
| POST   | `/api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/initialize-code` | 容器内初始化代码 |
| GET    | `/api/v1/hosts`                 | 执行节点列表（含校验状态与详情）                       |
| POST   | `/api/v1/hosts`                 | 新增执行节点，返回 201 与真实初始状态 `checking`       |
| GET    | `/api/v1/hosts/{hostID}`        | 执行节点详情                                          |
| PATCH  | `/api/v1/hosts/{hostID}`        | 修改执行节点；影响校验输入的修改会重新进入 `checking`  |
| DELETE | `/api/v1/hosts/{hostID}`        | 逻辑删除执行节点，成功返回 204                         |
| POST   | `/api/v1/hosts/{hostID}/validate` | 手动重新校验，立即返回 202 与 `checking`             |

`/api/v1/health` 的 `checks` 分别表达 `http_api` 与 `database`，不会因为 HTTP 服务已启动就
认为数据库一定可用。错误响应统一为 `{"error": {"code", "message", "request_id"}}`，
错误代码取值为 `invalid_request`、`not_found`、`state_conflict`、`method_not_allowed`、
`internal_error`，分别对应 HTTP 400、404、409、405、500。

#### 执行节点接口

`POST /api/v1/hosts` 只接受连接目标与私钥路径，**不接受**任何校验字段（`validation_status`、
`validation_generation` 等）；请求体包含未知字段时返回 400。

```json
{
  "host": "192.0.2.10",
  "port": 22,
  "username": "deploy",
  "workspace_root": "~/.lingting/agentflow",
  "private_key_path": "/home/me/.ssh/id_ed25519",
  "name": "构建节点",
  "default": false
}
```

响应在节点字段之外额外提供：

| 字段 | 说明 |
|---|---|
| `validation_status` | `checking`、`normal` 或 `abnormal` |
| `validation_message` | 最新校验结果的摘要或失败原因 |
| `validation_details` | 结构化详情：校验器版本、代次、三项检查、逐镜像结果、能力边界说明 |
| `validation_started_at` / `validation_completed_at` | 最近一次校验的时间范围 |
| `validation_generation` | 校验代次，用于识别结果是否属于当前配置 |
| `usable_for_remote_task` | 服务端按与准入校验相同的规则计算，前端据此禁用操作 |

`usable_for_remote_task` 只是给界面的提示：真正的限制由后端在建立远程会话前强制实施，
篡改前端状态无法绕过。

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

#### 执行记录查询

执行记录按 `Task → Step → Attempt → Session` 四层组织，四个接口都是只读的：

| 层级 | 语义 |
|---|---|
| Task | 用户提交的整体任务，生命周期与状态沿用任务管理接口 |
| Step | 逻辑执行阶段（`prepare`、`source`、`plan`、`develop`、`verify`、`review`、`save`、`cleanup`） |
| Attempt | 同一步骤的一次执行尝试，`attempt_number` 从 1 开始且在同一步骤内唯一 |
| Session | 一次独立的 Agent 调用会话；每次 Planner / Developer / Validator 调用各有独立会话 |

列表接口统一返回 `{"records", "total", "current", "size"}`，与任务列表一致：

- `current` 默认 1，`size` 默认 20、上限 100，超限返回 400。
- `status` 可重复或使用逗号分隔，未知状态返回 400。
- 步骤按 `sequence` 升序，尝试按 `attempt_number` 升序，会话按创建时间升序；排序键附加
  主键，分页结果稳定。

```http
GET /api/v1/tasks/{taskID}/steps?current=1&size=20
GET /api/v1/tasks/{taskID}/steps/{stepID}/attempts
GET /api/v1/tasks/{taskID}/steps/{stepID}/attempts/{attemptID}/sessions
GET /api/v1/tasks/{taskID}/sessions/{sessionID}
```

会话响应把三种状态分开输出，不能互相替代：

| 字段 | 含义 |
|---|---|
| `status` | 会话执行状态：`starting`、`running`、`completed`、`failed`、`cancelled`、`interrupted` |
| `archive_status` / `archive_path` | 原始 JSONL 的归档结果与位置：`not_attempted`、`succeeded`、`failed` |
| `export_status` / `html_export_path` | HTML 导出结果与位置：`not_attempted`、`succeeded`、`failed` |

因此"调用成功但归档失败""调用失败但会话已保存""原始 JSONL 已保存但 HTML 导出失败"都能
如实表达。归档位置保存在 AgentFlow 本机的持久目录，容器删除后原始会话与归档仍可读取。

归属校验完全在应用层完成：对象存在但属于其他任务时返回 404，不会泄露其他任务的执行记录；
已逻辑删除的任务下不再提供执行记录查询，但历史步骤、尝试、会话记录仍然保留在数据库中。

#### 当前能力边界

任务管理接口**只负责管理数据**，执行通过单步骤执行、单步骤闭环与任务级工作流编排接口发起：

- 任务管理接口不会调用 Pi CLI、不会创建 Docker 容器、不会连接 SSH、不会克隆 Git 仓库。
- 任务级编排器已提供（见"任务级工作流编排"一节），但它**只执行已持久化的计划步骤**，
  不生成 Step、不创建容器：缺少容器返回 412，缺少计划步骤返回 409。
- 执行记录接口**只提供查询**；写入口是单步骤执行、单步骤闭环、顺序执行与任务级编排接口。
- 单步骤执行接口 `POST /api/v1/tasks/{taskID}/steps/{stepID}/execute` 执行一个已存在的 Step。
- 未经真实执行器创建时不会存在 Step / Attempt / Session 记录；查询返回空列表是正确结果，
  不会用虚构数据填充。
- 角色没有查询接口，创建任务时需要手工填写其标识；执行节点已提供 `/api/v1/hosts` 接口。

`/api` 下的未知路径返回 JSON 404，不会回退到前端页面；`/api` 之外的未知页面路径进入 SPA
回退，因此浏览器刷新任意前端路由都能正常打开页面。

监听地址默认是 `127.0.0.1:26380`，可在配置文件 `server` 段或 `--address` 参数中修改；优雅
关闭超时通过 `server.shutdown_timeout_seconds` 设置（0 表示默认 10 秒）。默认只监听回环
地址，未实现远程访问认证前不会默认监听所有网卡。

### 单步骤执行

对**已经在数据库中存在的 Step** 发起一次真实 Pi 调用。这是当前唯一的执行写入口，也是
`HTTP API → Task Service → Step Execution Service → Pi Runner` 闭环的入口。

```http
POST /api/v1/tasks/{taskID}/steps/{stepID}/execute
Content-Type: application/json

{
  "prompt": "可选补充说明",
  "timeout_seconds": 600
}
```

两个字段都是可选的，请求体可以为空。关联标识全部来自路径与数据库记录：

- 不接受 Session 文件路径、工作目录、命令行参数或任意本地路径。
- 不接受角色覆盖：执行角色由步骤在计划生成时锁定，角色类型必须与步骤类型匹配。
- `prompt` 只作为补充说明追加在编排器生成的提示词之后，不能替换任务目标与步骤上下文。

#### 执行前校验

按固定顺序校验，任一不满足时不会创建任何执行记录：

1. 任务存在、未被逻辑删除，步骤存在且属于该任务（归属不符与不存在都返回 404）。
2. 任务状态允许执行：`preparing`、`planned`、`developing`、`retrying`、`paused`、
   `recovery_required`。`queued` 需要先完成环境准备与计划；`cancelling` 与已结束状态拒绝。
3. 步骤状态允许执行：`pending`、`retry_wait`、`failed`、`recovery_required`，且当前没有被
   任何 Attempt 占用。
4. 步骤类型对应 Agent 角色（`plan` / `develop` / `review`），角色存在、类型匹配、已启用并
   配置了模型。确定性阶段（`prepare`、`source`、`verify`、`save`、`cleanup`）不能通过该
   接口执行。
5. 任务绑定的执行节点 `validation_status` 为 `normal`。
6. 任务容器存在、未被 detached、未被删除，且核验为正在运行。

第 5、6 步缺失时返回 412 `precondition_failed`：本接口**不会**创建容器、启动容器或回退到
本机执行。

#### 执行过程

1. 在单个数据库事务内占用步骤：以状态为前置条件创建新的 Attempt（`running`，序号由存储层
   分配），把 Step 推进到 `running` 并记录 `current_attempt_id`，把 Task 推进到 `developing`
   并记录当前步骤与尝试。
2. 通过容器内执行入口调用 Pi：每次执行使用独立的 Attempt 与独立 Session 文件。
3. 按真实结果写入 Attempt、Step、Task 终态。三条写入各自独立并使用未取消的上下文，任一
   失败都不阻止其余写入。

占用失败（并发请求、重复请求、状态已变更）返回 409 `state_conflict`，不会启动第二个 Pi
进程。进程内也有一层登记作为快速拒绝，但一致性由数据库条件更新保证。

#### 终态映射

| 情形 | Attempt | Step | Task |
|---|---|---|---|
| Pi 正常退出（有可核验运行痕迹） | `completed` | `completed` | `developing` |
| Pi 非零退出或启动失败 | `failed` | `failed` | `recovery_required` |
| 调用超时 | `timed_out` | `failed` | `recovery_required` |
| 用户取消 | `cancelled` | `cancelled` | `cancelled`（经 `cancelling`） |
| 服务关闭导致中断 | `interrupted` | `failed` | `recovery_required` |

关键语义：

- **单个步骤成功不等于任务成功**：任务停留在 `developing`，最终结论留给后续确定性验证与
  代码保存阶段，不会被本接口标记为 `completed`。
- 单个步骤失败不会覆盖其他步骤的历史结果，也不会把任务直接判为终态 `failed`。
- 服务关闭与用户取消必须区分：前者记录 `interrupted` 并需要人工核验现场，后者才写入
  "已取消"的结论。
- 超时不会被当作成功，也不会触发自动重试。

#### 响应

成功返回 200：

```json
{
  "task_id": "<任务标识>",
  "step_id": "<步骤标识>",
  "attempt_id": "<尝试标识>",
  "attempt_number": 1,
  "session_id": "<会话标识>",
  "outcome": "completed",
  "success": true,
  "summary": "Agent 调用已完成: ...",
  "exit_code": 0,
  "attempt_status": "completed",
  "step_status": "completed",
  "task_status": "developing",
  "session_status": "completed",
  "session_run_status": "completed",
  "archive_status": "succeeded",
  "export_status": "not_attempted",
  "finalized": true,
  "started_at": "2025-01-01T00:00:00Z",
  "finished_at": "2025-01-01T00:02:00Z"
}
```

`outcome`、`attempt_status`、`step_status`、`task_status` 分别表达"本次调用如何结束""尝试
终态""步骤终态""任务当前状态"，不能互相替代。`success` 是"调用正常结束且状态已写完"的
便捷值，不代表任务验收通过。

`archive_status` 与 `export_status` 分别记录原始 JSONL 归档与 HTML 导出结果：归档失败不会
把成功的调用改写为失败，也不会删除 Attempt 或 Session 记录。

#### 错误语义

错误响应沿用统一结构 `{"error": {"code", "message", "request_id"}}`：

| 状态码 | code | 含义 |
|---|---|---|
| 400 | `invalid_request` | 参数非法，例如超时秒数越界 |
| 404 | `not_found` | 任务或步骤不存在，或步骤不属于该任务 |
| 409 | `state_conflict` | 状态不允许执行，或步骤已被其他执行占用 |
| 412 | `precondition_failed` | 缺少受管理的运行中容器，或执行节点未通过环境预检 |
| 500 | `execution_failed` | 执行已发生但未成功（失败、超时、取消、中断） |
| 500 | `execution_not_persisted` | 执行已结束，但最终状态未能完整写入数据库 |
| 503 | `internal_error` | 执行协调器未初始化 |

`execution_not_persisted` 是刻意与成功区分的结果：执行事实仍然在响应体之外成立，但数据库
记录可能不完整，因此不能返回 200。执行本身出错时同样不会删除已有的 Attempt 与 Session。

#### 本阶段不提供的执行能力

- 不自动生成或选择 Step，不串联多个 Step。
- 不自动重试，也不按退避策略重新执行。
- 不创建、启动、停止或接管容器；容器缺失时返回 412。
- 不执行 Git Clone、Commit、Push 或 Merge。
- 不实现确定性验证与 Validator 阶段：`completed` 只代表本次 Agent 调用正常结束；需要
  "开发 + 验证"闭环时使用下一节的 `execute-and-validate` 入口。
- 不提供执行队列与跨步骤并行调度。

#### 执行前的前置步骤校验

执行某个步骤前，全部 `sequence` 更小的步骤必须处于 `completed` 或 `skipped`，否则返回
409 `state_conflict`。该规则只在 `domain.CheckStepPrerequisites` 中定义一次：跳过未完成的
前置步骤会让后续步骤在错误的代码基础上运行，因此不做"警告后继续"。

#### 代码状态记录

每次执行在占用步骤之后采集一次执行前快照、在 Pi 结束后采集一次执行后快照，写入该次 Attempt：

| 字段 | 含义 |
|---|---|
| `git_branch` | 采集时的工作区分支名 |
| `git_before` / `git_after` | 分支、HEAD、是否存在未提交变更、变更条目计数、采集是否成功 |

采集是只读的：只执行 `git status` 与 `git rev-parse`，不提交、不推送、不合并，也不写
`code_saves`。采集失败会记录 `status_read: false` 与原因，不会被当作"工作区干净"，也不会把
成功的执行改写为失败。完整 diff 不落库。

### 顺序执行开发步骤

按已保存的计划顺序依次执行开发步骤。这是**显式**入口：不生成计划、不自动重试、不自动执行
验证与代码保存。

```http
POST /api/v1/tasks/{taskID}/execute-steps
Content-Type: application/json

{
  "prompt": "可选补充说明",
  "timeout_seconds": 600
}
```

两个字段都是可选的，请求体可以为空。步骤顺序完全由数据库中的 `steps.sequence` 决定，调用方
不能指定执行哪些步骤、跳过哪些步骤或改变顺序。

执行语义：

- 每次只执行一个 Step，前一步骤结束后才开始下一步。
- 前一步骤失败、超时、取消或中断时立即停止，不自动重试，也不执行后续步骤。
- `recovery_required` 的步骤与已被占用的步骤阻止自动推进，必须先人工核验现场。
- 每个开发步骤使用独立 Attempt 与独立 Session，但共享同一任务容器的同一代码工作区。
- 同一任务的并发顺序执行返回 409；跨进程并发仍由步骤占用的事务条件更新拒绝。
- 不 Commit、不 Push、不 Merge，也不自动执行 Validator。

响应示例（全部成功）：

```json
{
  "task_id": "<任务标识>",
  "steps": [
    {
      "step_id": "<步骤标识>",
      "sequence": 1,
      "title": "实现接口",
      "attempt_id": "<尝试标识>",
      "session_id": "<会话标识>",
      "outcome": "completed",
      "step_status": "completed",
      "finalized": true
    }
  ],
  "executed_steps": 1,
  "stopped": false,
  "success": true,
  "task_status": "developing"
}
```

`stopped` 表示是否提前停止，`success` 表示全部步骤是否都以正常结束且状态已完整写入，两者
不能互相替代。提前停止时 `stopped_step_id` 与 `stop_reason` 给出实际失败步骤与原因。

错误语义与单步骤执行一致：参数错误 400、任务不存在 404、状态冲突 409、前置条件缺失 412。
协调器返回错误（例如缺少容器）时整个请求返回该错误，不会被伪装成"执行后正常停止"。

### 单步骤开发—验证闭环

对一个已经在数据库中存在的开发 Step 依次执行"开发 → 验证"，并依据真实验证结果收口步骤状态。
这是当前唯一把 Developer 与 Validator 串起来的入口。

```http
POST /api/v1/tasks/{taskID}/steps/{stepID}/execute-and-validate
Content-Type: application/json

{
  "prompt": "可选补充说明",
  "timeout_seconds": 600,
  "validation_timeout_seconds": 900
}
```

三个字段都是可选的，请求体可以为空。`timeout_seconds` 约束开发阶段单次 Agent 调用，
`validation_timeout_seconds` 约束验证阶段全部检查的总时长，两者是不同概念。

#### 执行流程

```text
归属 / 前置步骤 / 验证配置校验
  → StepExecutor.Execute（DeferCompletion=true，新建 Attempt 与独立 Session）
      → 开发成功时步骤进入 verifying 并保持被当前尝试占用
  → ValidationService.Validate（同一容器、同一工作区）
  → domain.DeriveStepVerificationState（由 Run 状态 / 结论 / 检查结果推导验证状态）
  → domain.DecideStepCloseout（开发结论 + 验证状态 → 步骤收口状态）
  → 收口写入：步骤（条件更新）→ 任务（条件更新）→ 事件
```

- 开发与验证使用**同一份**任务容器工作区：中间不重新 Clone、不重置代码、不清理改动。
- 每次执行创建新的 Attempt 与独立 Session；重新执行不会复用旧记录。
- 只有开发结论为 `completed`、开发记录已完整写入、验证配置有效且容器仍可用时才启动验证。
- 验证配置缺少启用的必需检查时在开发之前返回 400，不会跑完开发再把配置问题伪装成
  "验证未通过"。

#### 收口规则

| 开发结论 | 验证情况 | 步骤状态 |
|---|---|---|
| 失败 / 超时 | 未执行 | `failed` |
| 取消 | 未执行 | `cancelled` |
| 中断 | 未执行 | `recovery_required` |
| 成功 | 未发起验证 | `recovery_required` |
| 成功 | 验证通过 | `completed` |
| 成功 | 必要检查失败 | `failed` |
| 成功 | 验证被取消 | `cancelled` |
| 成功 | 验证基础设施异常 | `recovery_required` |

- **Developer 完成不等于验证通过**：开发成功时步骤进入 `verifying`，只有真实验证通过才会
  进入 `completed`。
- **单个步骤通过验证不等于任务完成**：任务保持 `developing`，终态由后续代码保存与任务级
  编排决定。
- 必要检查失败时不会标记为已验证完成；基础设施异常（容器不可用、命令无法启动、结果无法
  确认、验证流程整体失败或超时）既不会伪装成代码失败，也不会判为通过。
- 验证失败后停止并等待用户显式决定：不自动重试、不自动修复代码、不删除容器。

#### 响应

成功（已验证通过）返回 200：

```json
{
  "task_id": "<任务标识>",
  "step_id": "<步骤标识>",
  "attempt_id": "<尝试标识>",
  "attempt_number": 1,
  "session_id": "<会话标识>",
  "outcome": "completed",
  "validation_ran": true,
  "validation_run_id": "<验证记录标识>",
  "validation_status": "completed",
  "validation_verdict": "passed",
  "validation_passed": true,
  "checks": [{"check_id": "build", "status": "passed", "exit_code": 0}],
  "validation_persisted": true,
  "infrastructure_error": false,
  "verification_state": "passed",
  "step_status": "completed",
  "task_status": "developing",
  "verified": true,
  "finalized": true
}
```

`outcome`、`validation_ran`、`validation_verdict`、`step_status`、`verified` 分别表达"开发
如何结束""验证是否发起""验证结论""步骤状态""是否已验证完成"，不能互相替代。

#### 错误语义

| 状态码 | code | 含义 |
|---|---|---|
| 400 | `invalid_request` | 参数非法，或步骤验证配置缺少启用的必需检查 |
| 404 | `not_found` | 任务或步骤不存在，或步骤不属于该任务 |
| 409 | `state_conflict` | 状态不允许执行，或步骤已被另一条闭环占用 |
| 412 | `precondition_failed` | 缺少受管理的运行中容器，或执行节点未通过环境预检 |
| 500 | `execution_failed` | 开发未成功，或验证未通过 |
| 500 | `execution_not_persisted` | 闭环已结束，但结果未能完整写入数据库 |
| 503 | `internal_error` | 闭环协调器未初始化 |

未通过验证不会返回 200：调用方必须看到真实的失败原因与状态。

#### 验证失败反馈

验证未通过时写入结构化的失败反馈（`validation_feedbacks`），包含失败检查标识与名称、实际
退出码、失败原因、有长度限制的输出摘要、检查起止时间、超时/取消/基础设施标记，以及开发
Attempt 与 Validation Run 引用。

- 摘要会过滤令牌、口令、私钥与 URL 内嵌凭据，并按上限裁剪。
- 反馈与原始 Validation Run 一起保留，不替代它；同一轮验证至多一条反馈，历史不被覆盖。
- 用户显式重新执行时，提示词会引用上一轮失败反馈（"上一次验证未通过的问题"），但反馈不是
  当前验证结论，新的执行仍然必须通过新的验证。

#### 查询验证历史

```http
GET /api/v1/tasks/{taskID}/steps/{stepID}/validations
```

响应包含派生出的 `state`（`unverified` / `verifying` / `passed` / `failed` / `cancelled` /
`infrastructure_error`）、`latest`（最新一次验证）与 `records`（全部历史，按创建时间升序）。
历史记录只追加不覆盖，因此重新执行不会抹掉上一轮失败证据。

#### 本阶段不提供的闭环能力

- 不自动生成计划，不串联多个 Step，不按计划自动推进。
- 不自动重试，也不在验证失败后自动调用 Developer 修复。
- 不创建、启动、删除或接管容器；容器缺失时返回 412。
- 不执行 Commit / Push / Merge，也不提供任务级调度队列。

### 任务级工作流编排

编排器按数据库中**已持久化的计划**顺序推进多个开发步骤，是 CLI 与 Web 之上的唯一执行入口。
它只协调既有能力：不生成计划、不创建容器、不重写验证结论。

```http
POST /api/v1/tasks/{taskID}/execute-workflow
Content-Type: application/json

{
  "prompt": "可选补充说明",
  "timeout_seconds": 1800,
  "validation_timeout_seconds": 600
}
```

三个字段都是可选的，请求体可以为空。成功返回 202 与这次执行的真实初始状态（不会返回
"已受理但仍在后台"的模糊语义）：

```json
{
  "run_id": "<执行记录标识>",
  "task_id": "<任务标识>",
  "status": "running",
  "status_terminal": false,
  "status_active": true,
  "stage": "step_execution",
  "current_step_id": "<步骤标识>",
  "completed_steps": 0,
  "total_steps": 2,
  "cancel_requested": false,
  "code_saver_available": true,
  "plan_digest": "<计划内容摘要>",
  "updated_at": "2025-01-01T00:00:00Z",
  "revision": 1
}
```

配套接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/tasks/{taskID}/workflow` | 查询当前或最近一次执行进度，含逐步骤快照 |
| POST | `/api/v1/tasks/{taskID}/cancel-workflow` | 请求取消：先落盘再传播，重复请求幂等 |
| POST | `/api/v1/tasks/{taskID}/resume-workflow` | 恢复一次已停止的执行，请求体必须给出 `run_id` |

#### 执行前条件

编排器不创建容器、不生成计划，因此启动前必须同时满足：

1. 任务未被删除、未记录取消请求，且状态允许执行（`AllowsStepExecution`）。
2. 任务已有受管理的容器标识，且该容器未被 detached、未被删除、核验为运行中。
3. 执行节点 `validation_status` 为 `normal`。
4. 任务已有持久化的计划步骤：序号唯一且大于 0、类型为开发步骤、配置了执行角色、包含启用的
   必需检查；不存在 `recovery_required` 或被占用（`running` / `developing` / `verifying`）
   的步骤；且至少有一个步骤尚未完成。

条件 1 不满足返回 409，条件 2 / 3 不满足返回 412，条件 4 不满足返回 409。判断只在
`domain.CheckWorkflowStart` 中定义一次，Handler 与前端不复制第二份规则。

因此完整链路需要人工串联：

```bash
# 1. 准备容器与容器内代码工作区（一次执行一个全新容器）
./bin/agentflow execenv prepare-task <task-id>

# 2. 通过服务启动任务级工作流并等待收敛
./bin/agentflow task run <task-id>
```

#### 执行与收口

- 每一步都先写入 `workflow_runs` 的阶段与游标，再调用单步骤闭环
  （`app.StepWorkflowService.ExecuteAndValidate`）；有副作用的操作不会先于状态落盘。
- 只有当前步骤验证通过（`Verified` 且步骤为 `completed`）才推进下一步骤。
- 收尾复用 `app.ContainerService.SaveCode`，并核对会话归档；结论只由
  `domain.DecideWorkflowCloseout` 计算——步骤全部通过验证不等于任务成功。
- 停止分类固定：开发失败 `blocked` / `developer_failed`；开发超时或中断
  `recovery_required` / `developer_interrupted`；验证未通过 `blocked` /
  `verification_failed`；证据未可靠写入 `recovery_required` / `verification_unconfirmed`；
  代码保存记录未确认 `recovery_required` / `code_save_unconfirmed`；代码保存结果不确定
  `recovery_required` / `code_save_indeterminate`；代码保存未成功 `blocked` /
  `code_save_failed`；无代码保存能力 `blocked` / `code_saver_unavailable`。
- 不自动重试、不自动修复代码、不自动删除容器；停止后等待用户显式决定。

#### 错误语义

| 状态码 | code | 含义 |
|---|---|---|
| 400 | `invalid_request` | 超时秒数越界（0–21600）或 `run_id` 缺失 |
| 404 | `not_found` | 任务或工作流执行记录不存在 |
| 409 | `state_conflict` | 状态不允许启动、计划不满足条件，或同一任务已有未结束执行 |
| 412 | `precondition_failed` | 缺少受管理的运行中容器，或执行节点未通过环境预检 |
| 503 | `internal_error` | 编排器已停止，不再接受新请求 |

#### 本阶段不提供的编排能力

- 不自动生成或补充计划步骤：任务没有已持久化步骤时直接拒绝启动。
- 不在执行路径上创建、启动或接管容器。
- 不发起 Validator Agent 调用：验证结论只来自确定性检查命令。
- 不自动重试、不自动修复代码、不提供多任务并发调度队列。

### 代码保存的状态写入失败与恢复边界

远程 Git 操作与本机 SQLite 无法共享事务。提交、推送或合并成功之后，写回 `code_saves` 仍可能
失败：此时数据库里仍是 `pending`，而仓库已经是另一个事实。因此这三组状态必须与"记录本身
是否可信"分开表达。

#### 失败窗口

| 场景 | 仓库真实状态 | 记录可能的状态 | 风险 |
|---|---|---|---|
| 提交成功、记录写入失败 | 任务分支已产生新提交 | `commit_status=pending` | 重启后无法仅凭记录判断是否已提交 |
| 推送成功、记录写入失败 | 远程任务分支已指向目标提交 | `push_status=pending` | 重复推送或误报保存失败 |
| 基础分支推送成功、记录写入失败 | 远程基础分支已包含合并提交 | `merge_status=pending` | 重复合并或无法确认合并结果 |

#### 记录字段

补齐 `0019_code_save_recovery.sql` 后，`code_saves` 增加五个可空列：

| 字段 | 含义 |
|---|---|
| `baseline_commit_sha` | 执行提交**之前**的 HEAD：判断 HEAD 是否被本次操作推进 |
| `baseline_base_commit_sha` | 执行合并**之前**的远程基础分支 commit：把基础分支的变化归因到本次操作 |
| `persist_status` | `persisted` / `unconfirmed`；空值按 `persisted` 处理，历史记录仍然可读 |
| `persist_error` | 最后一次写入失败的脱敏摘要，写入成功后清空 |
| `recovery_status` | `unverified` / `confirmed_applied` / `confirmed_not_applied` / `unverifiable` |

基线一律在**副作用之前**落盘：基线写不进去就中止该阶段，不在未知起点上产生新提交或合并。
历史记录没有基线，保持 `NULL`，不会被补造。

#### 恢复原则

1. **写入失败不等于 Git 失败**：`codesaver.Result` 分别暴露 Git 结论与 `PersistenceErrors`，
   写盘失败返回 `state_unconfirmed` 而不是 `record_failed`。
2. **恢复前先核验真实状态**：不得仅因记录是 `pending` 就再次提交、推送或合并。
3. **不能核验就保留不确定性**：容器不可访问、远程不可达、无法唯一确定合并提交时一律
   `unverifiable`，不伪造成功或失败结论。
4. **恢复动作可重复执行**：重复核验得到相同结论，且不产生任何副作用。
5. **保留原始故障证据**：最后已落盘的阶段、写入失败摘要、可读取的 Git 状态与恢复判定分别保存，
   新的失败摘要不覆盖旧证据。

#### 判定依据

结论只由纯函数 `domain.DecideCodeSaveRecovery` 计算，三个阶段使用同一套判据：

| 阶段 | 确认已发生 | 确认未发生 | 其余情况 |
|---|---|---|---|
| 提交 | 有基线且当前 HEAD ≠ 基线 | 有基线且 HEAD = 基线 | 无基线或读不到 HEAD → `unverifiable` |
| 推送 | 远程任务分支 = 预期提交 | 远程任务分支不存在 | 指向其他提交 → `unverifiable`（禁止 force push） |
| 合并 | 远程基础分支 = 唯一候选合并提交 | 远程基础分支 = 基线 | 无法推算或与预期不一致 → `unverifiable` |

"唯一候选合并提交"要求同时满足：候选是合并提交、第一父提交等于基线、第二父提交等于任务
分支提交。仅发现基础分支发生变化，不足以证明合并由本次操作产生。只有"确认未发生"才标记为
可以安全重试。

#### 只读核验

`agentflow code verify` 是唯一的恢复入口，需要人工显式执行：

```bash
./bin/agentflow code verify --record <code-save-record-id>
./bin/agentflow code verify --record <code-save-record-id> --container <container-id>
```

它只执行 `rev-parse`、`ls-remote` 与 `rev-list --first-parent`，不提交、不推送、不合并、
不切换分支、不删除或接管容器，也不改动记录的 Git 状态字段；核验结论写回 `recovery_status`。
`agentflow code status --task <task-id>` 同时展示基线、写入状态、写入失败摘要与恢复结论。

核验结论的写回是条件写入：`storage.CodeSaveStore.UpdateCodeSaveRecovery` 只更新
`recovery_status` 与 `updated_at`，并以读取时的恢复结论作为条件。核验期间记录若被其他执行
推进（提交、推送或合并状态变化导致恢复结论被改写），写入返回 `storage.ErrConflict`，接口的
`Persisted` 保持 `false`，`Updated` 回落到数据库中的真实记录。因此：

- 核验不会用读取时的整条记录覆盖更新后的事实；
- SQLite 写入失败或冲突时不得报告恢复已经闭环；
- 重复执行同一只读核验在同一现场下收敛到同一结论。

只有 `confirmed_applied` 与 `confirmed_not_applied` 是收敛结论（`CodeSaveRecord.RecoveryConverged()`）；
`unverified` 与 `unverifiable` 仍需人工继续处理。

本轮不引入自动重试、后台恢复调度与新的工作流功能：恢复语义先由人工核验确立。

### 尚未实现的接口

项目、环境、容器、代码保存与 Agent 调度均**没有** HTTP 接口。Web 页面中的对应入口仍为
占位说明，连接指示器只表示 HTTP API 的可用性，不代表 SSH、Docker 或 Pi Agent 已就绪。
任务页面已接入真实接口，但只能创建、查看、取消与删除任务记录，尚未接入步骤执行入口。
机器页面已接入执行节点接口与环境预检结果展示。

后台服务目前注册了执行节点环境预检、统一 Pi Runner、单步骤执行协调器、确定性验证引擎、
开发—验证闭环协调器与任务级工作流编排器：任务调度器、计划自动生成与自动 Retry 编排尚未
接入，`agentflow server` 启动后提供 API、页面、环境预检、单步骤执行、显式顺序执行、单步骤
开发—验证闭环与任务级工作流编排能力。

## 统一 Pi Runner 与 Session 生命周期

Planner、Developer 与 Validator 的每一次调用都经由同一个执行入口，由 Go 侧管理完整进程
生命周期，而不是在业务代码里各自拼接 Pi 命令。

### Pi CLI 依赖与版本检查

非交互调用固定使用以下形式（Prompt 通过标准输入写入并立即关闭）：

```text
pi --print --session <session.jsonl> --model <provider/model-id> [--append-system-prompt <text>]
```

当前实测版本为 `1.3.0`，已确认 `--print`、`--session`、`--export` 与 `--list-models` 可用。
运行前可用下面两条命令自行核对：

```bash
pi --version
./bin/agentflow agent check --model group/free
```

`agent check` 会确认可执行程序、版本、`--print` 与 `--session` 是否出现在帮助中，并在给出
`--model` 时确认模型被识别以及凭据状态。该命令不发送提示词，因此不消耗模型额度。

Pi 调用不使用 RPC 模式，不使用 `--no-session`，也不依赖伪终端。用户配置的扩展保持正常加载：
Runner 不会自动追加 `--no-extensions`、`--no-skills` 等参数。

### 会话保存位置

每次调用使用独立 Session 文件，目录按任务、步骤、尝试、会话与调用标识分层：

```text
<数据库目录>/sessions/<task-id>/<step-id>/<attempt-id>/<session-id>/<invocation-id>/
    session.jsonl     原始 JSONL，Pi 直接写入
    session.html      Pi 官方导出的 HTML（可失败）
    stdout.log        标准输出日志（有字节上限）
    stderr.log        标准错误日志（有字节上限）
```

- 目录由服务端生成并校验落在 Session 根目录之内，调用方不能指定任意本地路径。
- 会话记录在进程启动之前写入数据库并置为 `running`；启动失败会收敛为 `failed`，不会残留
  `running`。
- 原始 JSONL 归档状态、HTML 导出状态与执行状态分别在数据库中记录，导出失败不会删除原始
  文件，也不会把成功的执行改写为失败。
- 日志超过上限后停止写入并在文件末尾留下明确标记，不会把截断的文件伪装成完整日志。

### 超时、取消与关闭

- 单次调用必须有超时边界，默认 30 分钟；超时后结束实际进程再读取退出状态。
- 取消与超时都会尽力保存已经写入的 Session 文件，并分别记录为 `cancelled` 与 `timed_out`。
- `agentflow server` 关闭时取消在途调用并等待其收敛，不会在退出后残留子进程。
- 容器内执行时，Session 与两个输出流都会回传到本机持久化目录，容器删除后仍可读取。

### 当前实施边界

- 统一 Runner 只负责单次 Agent 调用，不实现任务调度、步骤排序、自动重试、Docker 生命周期
  或 Git 工作流。是否执行、执行哪个 Step、是否重试由 Go 业务层决定。
- 调用入口有三个：`agent test`（本机连通性检查）、`agent run <container>`（容器内单次调用）、
  以及任务下的 `POST /api/v1/tasks/{taskID}/steps/{stepID}/execute`（单步骤执行，见上文）。
  三者的共同点是每次调用都走同一条 Runner 与独立的 Session 文件路径。
- 单步骤执行与任务级编排都要求任务已经拥有受管理的运行中容器：容器由
  `agentflow execenv prepare-task` 单独准备，执行接口不会创建容器。
- 仍然没有"生成并持久化计划步骤"的入口，因此仅通过 HTTP 无法把 `queued` 任务推进到可
  执行状态：任务级编排器只执行已经落库的 `steps`，缺少计划时返回状态冲突。
- 没有提供 HTML 会话查看器，也没有自动上传会话内容到外部存储。

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
./bin/agentflow host add --id node-a --host 192.0.2.10 --user deploy --key ~/.ssh/id_ed25519
./bin/agentflow host probe node-a      # 只探测 TCP 与 SSH 协议标识
./bin/agentflow host trust node-a      # 探测并展示主机密钥指纹
./bin/agentflow host test node-a       # 完整 SSH 身份认证
./bin/agentflow host workspace node-a  # 解析远程 Home 与工作空间
./bin/agentflow host validate node-a   # 环境预检：SSH、Docker CLI 帮助、镜像元数据

# 代码初始化：在任务容器内 Clone 并准备 agent/<task-id> 分支
./bin/agentflow code init --task <task-id> --step <step-id> --attempt <attempt-id>
./bin/agentflow code init --task <task-id> --step <step-id> --attempt <attempt-id> --workdir /workspace/project
```

`code init` 的边界：

- 代码只克隆到对应 Attempt 的任务容器内工作目录（默认 `/workspace/project`），
  不经过本机数据目录，也不使用宿主机 bind mount。
- 任务分支固定为 `agent/<task-id>`；分支已存在时只检出，不强制重置或覆盖。
- 目标目录非空时直接失败，不会为了重试自动清空目录内容。
- 同一 Attempt 已初始化成功时不会重新 Clone，只记录 `skipped` 并复用原 commit。
- 需要认证的私有仓库返回 `repository_auth_required`；第一版不提供凭据存储，凭据需由
  环境镜像自行注入。
- 容器必须由环境准备阶段登记本次 Attempt（`attempt_id`），否则返回前置条件错误。

任务执行的完整链路（需要先启动 `agentflow server`）：

```bash
# 1. 依据任务记录准备执行环境：校验配置、准备镜像、创建全新容器并写回任务关联
./bin/agentflow execenv prepare-task <task-id>

# 2. 启动任务级工作流执行并轮询真实进度
./bin/agentflow task run <task-id>
./bin/agentflow task run <task-id> --address 127.0.0.1:26380 --timeout 30m --verify-timeout 10m
./bin/agentflow task run <task-id> --wait=false   # 只发起请求，立即返回真实初始状态
```

`task run` 是纯 HTTP 客户端：调用 `POST /api/v1/tasks/{taskID}/execute-workflow`，随后轮询
`GET /api/v1/tasks/{taskID}/workflow` 直到执行收敛。它不在 CLI 进程内装配 SSH、容器、Pi 与
代码保存能力，服务不可达时直接失败并提示启动服务，不会退回本机执行。`task retry` 与
`task recover` 已移除：恢复走 `POST /api/v1/tasks/{taskID}/resume-workflow`，旧容器处置走容器
管理命令。

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
远程节点；节点记录只保存连接目标、本机私钥文件路径与远程工作空间配置，**不保存任何
凭据内容**。SSH 认证只支持无口令私钥。

`host` 命令组复用 `config validate` 的配置加载流程，因此同样接受 `--config`；
未指定时读取 `configs/agentflow.yaml`，数据库路径取自该配置的 `storage.database_path`。

### 新增执行节点

```bash
./bin/agentflow host add \
  --id node-build-01 --name 构建节点 \
  --host 192.0.2.10 --port 22 --user deploy \
  --workspace "~/.lingting/agentflow" \
  --key ~/.ssh/id_ed25519 --default
```

| 参数 | 说明 |
|---|---|
| `--id` | 节点标识，省略时自动生成 |
| `--name` | 节点名称，省略时使用 `--host` 的值 |
| `--host` | IP 地址或主机名，必填；只写地址，不写端口 |
| `--port` | SSH 端口，默认 `22`，取值范围 1-65535 |
| `--user` | SSH 登录用户名，必填 |
| `--workspace` | 远程工作空间路径，默认 `~/.lingting/agentflow` |
| `--auth` | 认证方式，只支持 `private_key`（也是默认值） |
| `--key` | 本机私钥文件路径，**必填** |
| `--default` | 同时设为默认执行节点 |

该命令只写入本机数据库，**不会**连接目标机器、不会创建远程目录，也不代表可以登录。
新建节点的校验状态一律为 `checking`，由后台服务异步完成环境预检；客户端不能指定
`validation_status` 等校验字段。该命令不接受任何凭据输入参数。

### 修改执行节点

```bash
./bin/agentflow host update node-build-01 --port 2222
./bin/agentflow host update node-build-01 --key ~/.ssh/id_ed25519
```

只修改显式给出的参数，未给出的配置保持原值。任何影响连接目标、认证方式、私钥路径、
远程工作空间或启用状态的修改都会使旧校验结果失效：状态回到 `checking`、清空旧摘要与
详情、`validation_generation` 加一，并重新提交后台校验。仅修改名称不会使结果失效。
该命令同样不执行登录，也不接受任何凭据。

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

只支持本机私钥认证，且私钥必须是**未加密**的：

| 认证方式 | 凭据来源 | 说明 |
|---|---|---|
| `private_key` | 节点配置中的本机私钥文件路径 | 唯一支持的认证方式，无口令私钥 |

- 私钥文件只被读取，不会被复制到工作空间、写入数据库或日志，也不会修改其权限或内容。
- 不支持 SSH Agent、密码认证与加密私钥（passphrase）。遇到受口令保护的私钥时返回明确的
  「不支持受口令保护的私钥，请改用无口令私钥文件」，**不会挂起等待口令输入**。
- 命令层不再提供 `--password-stdin`、`--passphrase-stdin`、`--ask-pass` 等凭据输入参数。
- 存量数据库中 `auth_method` 为 `agent` 或 `password` 的记录保留原值，不写迁移改写；
  这类节点在环境预检时因认证方式不受支持而判为 `abnormal`，需要显式改成私钥。

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
| `private_key_error` | 私钥文件不存在、格式不支持或是受口令保护的私钥 |
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

### 环境预检（validation）

执行节点在用于真实远程操作之前必须通过环境预检。预检状态只有三种：

| 状态 | 中文 | 含义 |
|---|---|---|
| `checking` | 校验中 | 尚未完成当前配置版本的预检 |
| `normal` | 正常 | 当前版本的全部必需预检项均通过 |
| `abnormal` | 异常 | 至少一项必需检查失败，或无法完成必要检查 |

单项结果使用独立的状态：`passed`、`failed`、`timed_out`、`skipped`、`not_run`。
它们只出现在校验详情里，不新增总体状态；`skipped` 与 `not_run` 一律**不算通过**。

四项必需检查按顺序执行，前一项失败时后续检查记为 `not_run`：

1. **SSH 连接**：连接、握手、认证，并执行一条无副作用的非交互远程命令（`true`）取得退出码。
2. **Docker 版本与 API 兼容性**：分别检查实际 CLI 版本与 Engine 版本不低于 `29.0.0`，并通过
   `GET /v1.52/version` 确认固定 API 基准 `1.52` 落在 Engine 声明的
   `[MinAPIVersion, APIVersion]` 区间内。版本探测使用 `env -u DOCKER_API_VERSION` 隔离远端
   遗留变量。
3. **Docker CLI 帮助信息**：对 `internal/docker` 中实际使用的命令逐条执行
   `docker <command> --help`，核对所需参数选项是否出现在帮助文本中（同时读取标准输出与
   标准错误）。
4. **基础镜像元数据**：解析当前节点最新的基础环境 Dockerfile，提取全部外部 `FROM` 镜像，
   通过远端 Docker Engine 的 `GET /v1.52/distribution/{name}/json` 查询元数据（摘要与平台）。

```bash
./bin/agentflow host validate node-build-01
```

该命令同步执行一次完整预检并输出四项结果与能力边界说明；退出码非零表示预检未通过。

#### 触发时机

- `host add`：写入 `checking` 后立即提交后台校验。
- `host update`：影响连接目标、认证方式、私钥路径、工作空间或启用状态的修改会使旧结果
  失效（状态回到 `checking`、清空详情、代次加一）并重新提交校验；仅改名不失效。
- `POST /api/v1/hosts/{id}/validate`：手动重新校验，立即返回 `202` 与 `checking`。
- `agentflow server` 启动时重新调度仍停留在 `checking` 的记录。

校验结果以 `validation_generation` 为条件写回：用户在校验期间再次修改配置时，较早的
结果会被丢弃，不会覆盖新版本状态。

#### 能力边界（第一版）

- Docker 兼容性检查**只**包含版本与 API 基准探测，以及 CLI 帮助信息核对；不执行
  `create`、`run`、`pull` 等任何创建容器、启动工作负载或下载镜像层的操作，因此 `normal`
  **不代表**该 Docker Engine 一定能成功创建并运行 AgentFlow 所需的容器。
- 镜像检查**只**查询元数据：不 `docker pull`、不执行完整 `docker build`、不创建临时容器、
  不运行镜像内程序，也不改变远端镜像缓存、容器与卷状态。
- 查询经由已认证 SSH 连接直接访问远端 Docker Engine 的 Unix 套接字（OpenSSH
  `direct-streamlocal@openssh.com`），因此走该 Engine 配置的镜像解析路径，且不要求远端安装
  `curl` 等额外工具。不使用 `docker pull` 作为回退手段。
- 无法解析的 `FROM` 动态引用（例如依赖构建期参数的变量）判为失败并记录原因；`scratch`
  与内部构建阶段别名不产生网络查询。
- 超时集中定义在 `internal/docker/validation.go`：单步 SSH 检查 10 秒、版本与 API 检查 15 秒、
  单镜像查询 8 秒、整体 45 秒。

#### 使用限制

任何使用持久化执行节点执行真实远程操作的入口都会先经过统一准入校验：节点必须存在、
未禁用，且当前配置版本的状态为 `normal`。处于 `checking` 或 `abnormal` 时返回明确错误并
提示执行 `agentflow host validate`。该校验由后端强制实施，**不能**通过篡改前端状态绕过；
只有预检本身与只读预检允许在节点未通过校验时执行。

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

`ssh` 段只保存路径与超时，**不保存任何凭据**；认证只使用本机私钥文件，私钥路径保存在
执行节点记录中，私钥内容既不进配置也不进数据库。
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
- 修改 schema 必须新增更高版本的脚本（例如 `0011_execution_host_validation.sql`），已发布的
  迁移不得修改。
- 增量迁移只新增列与索引，不重建表、不清空数据，也不添加外键或枚举白名单。

当前最新版本为 `0011`，它给 `execution_hosts` 增加 `validation_status`、
`validation_message`、`validation_details_json`、`validation_started_at`、
`validation_completed_at`、`validation_generation` 六个字段与一个按校验状态的部分索引。
已有记录一律补齐为 `checking`（代次 1），**不会被当作 `normal`**，必须重新执行校验。

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
| `execution_hosts` | 通过 SSH 管理的执行节点 | `port` 限定 1-65535；私钥认证必须给出 `private_key_path`；指纹必须以 `SHA256:` 开头；`last_connect_error` 只保存稳定分类代码；`validation_generation` 必须大于 0；`validation_details_json` 必须为合法 JSON；部分唯一索引保证最多一个默认节点；**不保存任何凭据字段** |
| `schema_migrations` | 迁移版本记录 | `version` 主键，记录迁移名与应用时间 |

通用约定：

- 主键为 TEXT，由应用层生成（`events` 使用自增 INTEGER）。
- 时间统一为 UTC Unix 毫秒时间戳，字段以 `_at` 结尾。
- JSON 统一以 UTF-8 文本保存，并通过 `json_valid` 约束校验。
- 业务 Schema 不声明外键，引用完整性、状态迁移与生命周期由应用层显式承担。
- 枚举式字段不使用数据库 `CHECK` 白名单，新增取值可在不修改 Schema 的情况下写入；
  读取层保留未知取值，不自动映射为默认状态。

### 存储接口

业务层只依赖 `internal/storage` 的接口，不接触 SQL 或驱动类型：

- `storage.Store`：各实体的读写方法与 `WithTx`、`Close`。
- `storage.Tx`：事务内可用的同一组实体方法。
- `storage.RoleSeeder`：角色初始化入口，仅在 `roles` 为空时导入，不覆盖已有角色。
- `storage.ExecutionHostStore`：执行节点读写、`SetDefaultExecutionHost`（默认节点切换在单个
  事务内完成）、`UpdateExecutionHostValidation`（以代次为条件写入校验结果）与
  `ListExecutionHostsByValidationStatus`（服务启动时恢复待校验节点）。
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
机器，需要在 `tests/config/ssh.yml` 中把 `ssh.enable` 设为 true 并填好目标，未启用或目标
缺失时警告并跳过；`ssh.known_hosts` 留空时复用本机 `~/.ssh/known_hosts`）。

测试分类、配置文件与编写约定见 [tests/README.md](tests/README.md)。

开发环境、代码风格与提交约定见 [docs/development.md](docs/development.md)，分层与依赖方向见 [docs/architecture.md](docs/architecture.md)。

## 当前未实现的功能

以下能力均在计划中，当前版本**不提供**：

- 需求队列、Worker 与并发调度（`internal/scheduler`）。
- 持久化任务队列与任务自动领取。
- 计划的自动生成与持久化：没有任何入口生成计划步骤，任务级编排器只执行已经落库的 `steps`。
- 容器准备与执行请求的自动串联：需要先 `agentflow execenv prepare-task`，再 `agentflow task run`。
- Validator Agent 调用接入：验证结论当前只来自确定性检查命令。
- 旧容器的显式处置命令：`task retry` / `task recover` 已随 `internal/workflow` 引擎包移除，
  重新开始需要先通过容器管理命令处置旧容器。
- 多步骤自动编排与 Planner → Developer → Validator 自动串联。
- 自动重试、退避策略与验证失败回退。
- Git Worktree 创建与清理（每个任务目前使用远程容器内的工作区）。
- Git 之外的代码获取方式（HTTP 下载、wget、curl、SCP）：`source.Fetcher` 抽象已就位，
  但只注册了 Git Clone 实现。
- 私有仓库认证：仓库地址中的内嵌凭据被拒绝，凭据需由环境镜像自行注入。
- 项目、环境的 HTTP 接口与对应页面（任务、执行节点、执行记录已提供接口）。
- 多 Agent Provider 与分布式执行（当前只支持 Pi）。
- 角色管理 CLI 与 Web 页面（当前只有存储层的角色初始化与增删改查接口）。
- 前端步骤执行入口与实时日志页面。

## 安全与风险提示

AgentFlow 的最终形态会**执行外部程序并修改项目文件**，请注意：

- 后续版本的 Agent 会在你的仓库中创建与修改文件，并可能执行命令。
- 请使用相互隔离的工作区，并配置合理的 Agent 权限；提示词不是权限边界。
- 审查任何可能修改或删除数据的操作，尤其是工作区清理与 Git 操作。
- 不要在配置或仓库中保存真实 API Key；模型凭据由 Pi 及其 Provider 管理。
- 执行节点表只保存连接目标、本机私钥文件路径与工作空间，**不保存任何凭据内容**；私钥文件
  只被读取，不会被复制到工作空间或远程机器。SSH 认证只支持无口令私钥，因此 AgentFlow
  不收集、不保存、不转发任何密码或口令。
- 主机密钥校验不可关闭。未知主机必须核验 SHA-256 指纹后显式信任，已信任密钥发生变化时
  连接会被拒绝；不要为了让连接"先跑通"而手工删除信任记录。
- `host probe` 只验证 SSH 服务可达性，**不代表已经登录成功或远程环境已验证**。
- 远程命令超时或取消时，AgentFlow 会关闭对应会话通道，但 SSH 协议不保证远程进程一定
  已终止；结果中的终止确认标志为 false 时，不要假定远程进程已经退出。
- SFTP 上传覆盖优先使用 `posix-rename` 原子替换；服务端不支持该扩展时会退化为"删除旧
  文件后重命名"，此时替换不是原子的，请避免在关键文件上依赖强原子性。
- 本地 HTTP 服务默认只监听 `127.0.0.1`，且不存储模型凭据。Web 连接指示器只表示 HTTP API 可用，不代表 SSH、Docker 或 Pi Agent 已就绪。
- 环境预检的 `normal` **不是**"远端 Docker Engine 已验证可用"：Docker 检查基于实际版本、
  API 基准与 CLI 帮助信息，不创建或启动容器，因此仍可能出现 `normal` 但容器创建因运行时
  条件不满足而失败的情况。镜像检查也只查询元数据，不保证镜像层可下载、构建可成功。
- 预检不会改变远端镜像缓存、容器与卷状态，但会产生正常的远端网络查询与 Docker 日志。

## 许可证

许可证将在首次公开发布前确定。
