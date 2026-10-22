# 开发指南

本文件描述本地开发环境、代码风格、测试流程与提交约定。目录职责与依赖方向见 [architecture.md](architecture.md)。

## 环境要求

- Go：使用当前受支持的稳定版本，版本要求以 `go.mod` 声明为准。
- Git：用于工作区隔离与版本管理，Makefile 会用 `git rev-parse` 注入提交号。
- Node.js 22 + pnpm 11：仅在构建或开发 Web UI 时需要；`make release` 会用它完成前端生产构建，
  产物随后由 Go 通过 `//go:embed` 嵌入可执行文件。
- Pi CLI：只有 `agent check` 需要 PATH 中存在 `pi`，它不发起模型调用。所有真实 Agent 调用
  都在远程任务容器内执行，因此容器镜像内必须安装可用版本的 `pi`，并完成对应 Provider 的
  凭据配置。

SQLite 驱动为 `modernc.org/sqlite`，纯 Go 实现，无需安装 CGO 工具链或系统 SQLite。

## 初始化

```bash
git clone <repo>
cd agentflow
go mod tidy
```

## 常用命令

```bash
make fmt          # gofmt -w cmd internal tests
make fmt-check    # 列出未格式化的文件
make vet          # go vet ./...
make test         # go test ./tests/...
make test-unit    # 快速单元测试
make test-local   # 本机集成测试
make test-pi      # 真实 Pi 集成测试
make test-ssh     # 真实 SSH 集成测试
make test-e2e     # CLI 端到端测试
make test-race    # 竞态检测（单元测试与 SSH 连接层）
make build        # 构建 bin/agentflow（不重建前端）
make release      # 单文件生产构建：前端构建 + Go 编译
make run          # go run ./cmd/agentflow server
make web-dev      # 前端开发服务器（另开终端运行 make serve）
make serve        # 统一服务入口（API + 内嵌页面 + 后台服务）
make test-packaging  # 单文件打包冒烟测试
make check        # fmt-check + vet + build
```

Web 相关说明见根目录 [README](../README.md#web-界面与单文件交付)。

不依赖 Make 时可以直接使用底层命令：

```bash
gofmt -w cmd internal tests
go vet ./...
go test -count=1 ./tests/...
go build -o bin/agentflow ./cmd/agentflow
```

测试代码全部位于根目录 `tests/` 下，按测试类型分层，每个目录都是独立的 Go 测试包，
可以单独运行：

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

真实 Pi 与真实 SSH 入口在依赖缺失时输出 `WARN [SKIP]` 并跳过；本机集成测试与快速单元
测试只使用本机回环地址上的服务，不依赖真实 SSH 服务器、外部网络或模型凭据。本机 SSH/SFTP
集成测试在进程内启动真实服务端，因此主机密钥校验、认证、命令执行与文件传输都经过真实协议
交互。测试用私钥统一由 `tests/internal/testkit` 的 `PrivateKeyPath` 生成，只生成无口令私钥。

`make build` 会通过 `-ldflags -X` 注入 `version`、`commit`、`buildDate`；直接 `go build` 时三者取默认的开发值 `dev` / `none` / `unknown`。可以用环境变量覆盖版本号：

```bash
make build VERSION=0.1.0
```

## CLI 使用

```bash
./bin/agentflow --help
./bin/agentflow --version
./bin/agentflow version
./bin/agentflow config validate --config configs/example.yaml

./bin/agentflow host add --id node-a --host 192.0.2.10 --user deploy --default
./bin/agentflow host list
./bin/agentflow host show node-a
./bin/agentflow host set-default node-a
./bin/agentflow host probe node-a
./bin/agentflow host update node-a --port 2222
./bin/agentflow host trust node-a
./bin/agentflow host test node-a
./bin/agentflow host workspace node-a --create
./bin/agentflow host transfer upload node-a ./local.txt /srv/agentflow/remote.txt
./bin/agentflow host remove node-a
```

任务执行命令（需要先启动 `agentflow server`）：

```bash
# 推荐：一个入口完成「准备容器 → 生成计划 → 启动编排」
./bin/agentflow task start <task-id>
./bin/agentflow task start <task-id> --prompt "优先保证兼容性" --timeout 30m --wait=false

# 分步排查时使用底层动作：
# 1. 依据任务记录准备执行环境（容器 + 容器内代码工作区）
./bin/agentflow execenv prepare-task <task-id>

# 2. 在容器内生成并持久化计划
./bin/agentflow task plan <task-id>

# 3. 通过本地服务启动一次任务级工作流执行，并轮询真实进度
./bin/agentflow task run <task-id>
./bin/agentflow task run <task-id> --address 127.0.0.1:26380 --timeout 30m --verify-timeout 10m
./bin/agentflow task run <task-id> --wait=false   # 只发起请求，立即返回初始状态
```

`task start <task-id>` 调用 `POST /api/v1/tasks/{taskID}/start`，由服务端依据数据库事实决定
从哪个阶段继续：已有受管理且运行中的容器时不会创建第二个容器，计划已持久化时不会重新生成。
响应显式区分 `stage`、`task_status`、`container_prepared`、`plan_persisted` 与
`workflow_started`，任一为 false 都不得当作任务已开始执行。它同样只是 HTTP 客户端，
不装配 SSH、容器、Pi 或代码保存能力。

`task plan <task-id>` 调用 `POST /api/v1/tasks/{taskID}/plan`，由服务在任务容器内调用
Planner 并把输出校验为结构化计划后一次写入数据库；计划固定以 `plan` 阶段步骤开头，其后是
`develop` 步骤。它不创建容器、不自动重试、不覆盖已有计划。

`task run` 是 HTTP 客户端：它调用 `POST /api/v1/tasks/{taskID}/execute-workflow`，再轮询
`GET /api/v1/tasks/{taskID}/workflow` 直到执行收敛。它不在 CLI 进程内装配 SSH、容器、Pi
或代码保存能力，服务不可达时直接失败并提示启动服务，不会退回本机执行。它也不生成计划：
任务没有已持久化的步骤时接口返回状态冲突。

`host` 命令组同样接受 `--config`，未指定时读取 `configs/agentflow.yaml`。

命令边界要分清：

- `host probe` 只验证 TCP 可达性与 SSH 协议标识，**不执行身份认证**。
- `host trust` 只探测主机密钥并展示指纹，写入信任记录必须显式给出已核验的 `--fingerprint`
  与非交互场景的 `--yes`。
- `host test`、`host workspace`、`host transfer`、`host validate` 执行完整的主机密钥校验与
  身份认证，是唯一可以执行远程命令与文件传输的路径。认证只使用节点配置中的无口令私钥，
  命令层不收集任何凭据。
- `host validate` 同步执行一次环境预检（SSH 连接、Docker CLI 帮助信息、基础镜像元数据），
  退出码非零表示未通过；它不受"节点必须为 normal"的准入限制约束。

`config validate` 未指定 `--config` 时读取 `configs/agentflow.yaml`；该文件不存在视为错误，不会回退到内置默认值。

## 代码风格

- 所有输出、注释与提交信息使用简体中文。
- 每个文件保持单一、可描述的主要职责；职责出现分化时按职责拆分，而不是继续追加。
- 同一职责的文件不拆分到多个文件；不同职责的代码不堆在同一文件。
- 不使用固定行数作为拆分标准，以“是否妨碍定位、修改是否牵动无关代码”为准。
- 项目未声明行宽时以每行不超过 120 字符为目标；超过时结构化换行。
- 同一方法在调用链中连续出现两次及以上时，从第一次重复调用开始逐段换行。
- 错误必须保留上下文，底层不重复打印同一个错误；只有命令执行入口负责输出一次。
- 不使用 `panic` 处理普通的配置或用户输入错误。
- 新增第三方依赖必须有明确用途，并只通过 Go Modules 管理。

## 改动流程

1. 确认改动属于哪一层，按 [architecture.md](architecture.md) 的依赖方向选择位置。
2. 领域规则变化优先修改 `internal/domain`，其余层只做编排与适配。
3. 涉及状态迁移时，检查是否仍通过 `Transition` 校验，并确认迁移路径在状态机中已声明。
4. 涉及持久化时，在 `internal/storage/sqlite/migrations/` 追加新迁移脚本，并在对应实体文件中补充仓储实现；不要修改已发布的迁移。
5. 改动完成后执行 `gofmt` 与 `go vet`，并自查 `git diff`。

### 修改单步骤执行链路时

单步骤执行涉及多份一致的约束，改动时按下列清单自查：

1. 准入规则只在 `internal/domain/step_execution.go` 修改：任务/步骤状态集合、角色类型、
   占用检查都集中在 `CheckStepExecutionStart`；不得在 `app` 或 `httpapi` 复制第二份判断。
2. 步骤占用必须仍走 `storage.StepExecutionStore.BeginStepExecution` 的单事务路径；不要在
   `app` 里改成"先查再写"，否则并发请求会各自创建 running Attempt。
3. 新增执行结论时同步更新 `StepExecutionOutcome` 的 `AttemptStatus`、步骤终态映射与 HTTP
   错误码，避免同一结论在不同层表达不一致。
4. 终态写入必须保持"三条互不阻断 + 未取消上下文 + 状态前置条件"的形式；新增写入路径同样
   不得因执行错误而跳过其余写入。
5. 取消与服务关闭必须继续区分：调用方取消记为 `cancelled`，服务停止记为 `interrupted`。
6. Timer/协程不得脱离 `lifecycle` 管理：执行协调器实现了 `Name/Start/Stop`，Server 关闭时
   会取消在途执行并等待收敛。

### 修改任务级工作流编排时

编排器只协调已有能力，改动时按下列清单自查：

1. 启动校验只在 `domain.CheckWorkflowStart` 修改：任务状态、容器、计划序号、必需检查与现场
   明确性都集中在那里；不得在 `app` 或 `httpapi` 复制第二份判断。
2. 步骤执行必须继续复用 `app.StepWorkflowService.ExecuteAndValidate`：不要为了"少一层"在
   编排器里直接创建 Attempt 或拼接 Pi 命令。
3. 进度写入必须保持"先落盘、再执行有副作用的操作"：新增阶段同样要先写 `workflow_runs`，
   再调用容器、Pi 或 Git。
4. 新增工作流状态时同步更新 `workflowRunStatusMachine` 的合法迁移、`IsTerminal` / `CanStart`
   与 HTTP 响应中的 `status_terminal` / `status_active`，避免同一状态在不同层表达不一致。
5. 收尾结论只在 `domain.DecideWorkflowCloseout` 修改；不得因为"步骤都通过了"就跳过代码保存
   与会话归档判定。
6. 恢复逻辑必须继续依据数据库事实：新增恢复路径同样不得重放 Pi 调用，也不得抢占有效租约。
7. 编排器实现了 `Name/Start/Stop` 并注册进 `internal/lifecycle`：新增后台协程必须挂在它下面，
   Server 关闭时会先停止新请求，再等待在途工作收敛。
8. 新增接口路径必须落在 `/api/v1/tasks/{taskID}/` 之下，并在 `router.go` 的 `isWorkflowPath`
   中登记；否则会被任务处理器当作未知子路径返回 404。
9. 不要在 CLI 里重新装配执行栈：`internal/workflow` 引擎包已移除，任务执行只存在于
   `agentflow server` 的 `app.TaskWorkflowService` 中；`agentflow task run` 是 HTTP 客户端，
   新增执行参数同样只能通过 `POST .../execute-workflow` 的请求体表达。
10. 计划必须落库到 `steps`（含 `sequence` 与角色快照），不得在编排器或 Handler 里临时
    拼装步骤：`domain.CheckWorkflowStart` 只接受已持久化计划。当前唯一入口是
    `app.TaskWorkflowService.GeneratePlan`（`POST /api/v1/tasks/{taskID}/plan`），计划固定以
    `plan` 阶段步骤开头、其后是 `develop` 步骤；新增计划来源也必须产出同样的结构。

### 修改统一任务启动流程时

启动入口只串联既有能力，改动时按下列清单自查：

1. 阶段判定只在 `domain.DecideTaskLaunchNext` / `CheckTaskLaunchStart` 修改：要不要建容器、
   要不要生成计划都集中在那里；不得在 `app`、`httpapi` 或 CLI 复制第二份判断。
2. 三个有副作用的阶段必须继续复用 `PrepareForTask`、`GeneratePlan` 与 `StartWorkflow`：
   不要在启动服务里直接创建容器、创建 Attempt 或拼接 Pi / Docker / Git 命令。
3. 容器可用性必须来自真实核验，不得凭 `tasks.container_id` 推断；核验失败按前置条件缺失
   返回 412，不得据此创建新容器。
4. 已有受管理且运行中的容器时不得创建第二个容器：重复准备只会留下无法被任何链路使用的
   孤儿容器，而孤儿容器不应被自动删除。
5. 任一阶段失败必须立即停止并保留真实 `FailureStage`（下层给出的更具体阶段优先），不得
   自动重试、不得删除容器、不得重放外部操作。
6. 步骤占用恢复只在 `domain.DecideStepOccupancyRecovery` 修改，且必须保持「不重放 Agent、
   不删除容器、所属任务仍有未结束执行时不抢占、写入带状态前置条件」。
7. 启动服务实现 `Name/Start/Stop` 并注册进 `internal/lifecycle`，注册顺序在任务级编排器之后；
   新增后台协程必须挂在它下面，Server 关闭时先停止新启动请求。
8. 新增路径必须落在 `/api/v1/tasks/{taskID}/` 之下，并在 `router.go` 中先于任务处理器判定，
   否则会被当作未知子路径返回 404；`isTaskLaunchPath` 的判定必须早于任务服务可用性检查。
9. 不要在 CLI 里重新装配执行栈：`agentflow task start` 只是 HTTP 客户端，新增执行参数只能
   通过 `POST .../start` 的请求体表达。
## 新增角色类型或状态

- 新增角色类型：在 `internal/domain/role.go` 增加 `RoleType` 常量与 `Validate` 分支，并在 `internal/config/config.go` 补充对应的工具策略模式映射与示例配置。
- 新增状态：在对应实体的状态机中声明合法迁移与终态，并确认重试、取消路径可达。
- 新增状态后，必须检查持久化层对旧数据的兼容性，必要时提供迁移。

## 测试流程

- 测试使用 Go 标准库 `testing`，全部放在根目录 `tests/` 下，按 `unit/`、
  `integration/local/`、`integration/pi/`、`integration/ssh/`、`e2e/` 分层；生产源码目录
  `internal/`、`cmd/` 不存放测试文件。
- 每个测试目录都是独立的 Go 测试包，用 `go test` 直接执行，成功判定与退出码由测试框架
  负责，不依赖额外脚本解析日志。
- 测试工作空间由 `tests/internal/testkit` 创建：在系统临时目录的
  `lingting-agent-flow-tests` 下生成随机根目录，每个用例使用独立子目录，并输出绝对路径
  `TEST_ARTIFACT_DIR`。数据库、Session、日志与生成文件必须全部落在其中。
- 使用 `testkit.New(t)`、`testkit.Dir(t)`，不用 `t.TempDir()`；不用 `t.Setenv` 切换全局
  数据目录，路径通过结构体、构造参数或子进程独立环境变量传入。
- 测试结束禁止删除工作空间与产物，只关闭数据库连接、子进程与监听端口。
- 无共享状态的用例使用 `t.Parallel()`，不得依赖固定执行顺序或其他测试预先创建的数据；
  并发度通过 `-p` 与 `-parallel` 控制。
- 测试不得读写用户真实配置、仓库或 `.agentflow/` 下的真实数据。
- 真实外部依赖测试统一由 `tests/config/` 下的 YAML 配置驱动：真实 Pi 测试由 `pi.yml` 的
  `pi.enable` 开启（默认模型 `group/free`），真实 SSH 测试由 `ssh.yml` 的 `ssh.enable` 开启，
  真实远程 Docker 测试由 `docker.yml` 的 `docker.remote_enabled` 开启且同时要求 `ssh.enable`。
  未开启或必需项缺失时输出 `WARN [SKIP]` 并跳过，配置完整但连接、认证或主机密钥校验失败时
  必须失败。
- 真实 SSH 测试的 known_hosts 来源统一由 `testkit.KnownHostsSources` 解析：`ssh.known_hosts`
  显式配置时读取该文件（不可读直接失败），未配置时复用本机 `~/.ssh/known_hosts` 与
  `known_hosts2`。写入的信任记录只落在测试工作空间。
- SSH 连接层按三层覆盖：`tests/unit/ssh/` 放纯逻辑单元测试，`tests/integration/local/ssh/`
  在进程内启动真实 SSH/SFTP 服务端，`tests/integration/ssh/` 面向真实远程机器。本机集成测试
  不得要求用户预先启动服务，也不得用"配置解析正确"代替真实握手、认证与传输验证。
- 取消与超时的用例必须验证远程会话确实被关闭（例如断言服务端活动会话数归零），不能只验证
  本端停止等待。
- 禁止通过关闭主机密钥校验或自动接受未知主机密钥让测试通过。
- 未经明确授权不运行测试、不创建测试代码与临时验证脚本，也不通过“测试后删除”的方式绕过该限制。
- 数据库 schema 变更必须同时在 `internal/storage/sqlite/migrations/` 提交新迁移脚本。
- 涉及跨实体状态更新时，必须通过 `storage.WithTx` 在单个事务内完成。

## 与 Pi 交互的注意事项

- Pi 作为外部进程运行，任何交互都必须假设对方可能异常退出。
- 协议字段以官方文档与当前安装版本为准，不凭记忆猜测。
- 新增交互路径时同时实现成功路径、失败路径与取消路径。
- 业务代码不得自行拼接 Pi 命令：所有调用都走 `app.ContainerService.RunAgent`，
  它通过 `agent.Runner` 接口驱动容器内的 Pi 进程。本版本只支持远程 SSH + Docker 容器执行，
  不存在本机 Pi 调用路径（`app.AgentRunnerService` 已移除）。
- 单步骤执行的执行结论必须由真实结果决定：`completed` 要求可核验的运行痕迹（完整 Session
  或进程标识），没有运行痕迹的"成功"要按失败处理。
- 用户取消与服务关闭都会终止进程，但必须区分记录：前者 `cancelled`，后者 `interrupted`。
- 修改执行链路后至少覆盖：成功、非零退出、进程启动失败、超时、取消、服务停止中断、
  状态写入失败与并发重复请求。

## 与 SSH 交互的注意事项

- 主机密钥校验不得关闭：`NewDialer` 必须拿到 `HostKeyStore`，未知或不匹配一律拒绝。
- 主机密钥类型由 `KnownHostsStore.RecordedHostKeyTypes` 提供：`Dialer` 按记录顺序逐个
  用单一类型握手，取第一个校验通过的结果；全部失败时返回首个结论。没有记录时交给
  `golang.org/x/crypto/ssh` 自行协商。逐类型重试只针对主机密钥类失败。
- 认证只使用无口令私钥：不得把私钥内容写入错误信息、日志、数据库或命令行参数，也不得
  复制私钥文件。遇到加密私钥必须返回明确的不支持错误，而不是要求交互输入口令。
- 远端 Unix 套接字通道（`DialUnixSocket`）用于访问远端 Docker Engine；SSH 通道不支持
  `SetDeadline`，因此截止时间只能通过关闭通道生效。自定义 HTTP 传输必须显式监听请求上下文，
  否则取消与超时不会真正中断查询。
- 远程命令必须用 `ShellCommand` 构造（程序名 + 参数数组 + 逐参数转义），禁止拼接用户输入。
- 取消与超时不仅要停止本端等待，还要关闭远程会话通道并释放本端 goroutine；无法确认远程
  进程已终止时，必须在结果中如实标记，不能声称命令已终止。
- SFTP 上传与下载都使用同目录临时文件 + 校验 + 重命名，失败时清理临时文件但不得留下最终
  文件；覆盖需要调用方显式声明。
- 远程路径只使用 POSIX 规则（`NormalizeRemotePath`），不得把本机 `filepath` 语义套用到远程
  路径，也不得用字符串前缀判断目录包含关系。
## 与 Docker 交互的注意事项

- 兼容性基线（CLI / Engine `29.0.0`、API `1.52`）与操作细则见
  [`docker-compatibility.md`](docker-compatibility.md)；基线取值只在
  `internal/docker/compatibility.go` 定义，不得在别处重复硬编码。
- 所有 Docker 命令必须经 `internal/docker` 的命令构造入口生成：业务调用走
  `dockerCommand`（显式 `DOCKER_API_VERSION=1.52`），版本探测走 `dockerProbeCommand`
  （`env -u DOCKER_API_VERSION` 隔离远端遗留变量）。业务层不得自行拼接 Docker 命令行。
- 版本与 API 兼容性检查使用 `docker.ProbeVersion`：CLI 版本、Engine 版本分别检查，固定 API
  基准必须落在 Engine 声明的 `[MinAPIVersion, APIVersion]` 区间内，不得把最低接受版本与
  基准做「大于等于」比较。
- 直接 Engine API 调用统一使用 `/v1.52/` 前缀（由 `EngineAPIBaseVersion` 派生），且只允许
  只读请求：`GET /version` 与 `GET /distribution/{name}/json`。
- 帮助检查只允许执行 `docker <command> --help`，禁止在其中执行 `create`、`run`、`start`、
  `pull` 等任何创建容器、启动工作负载或下载镜像层的命令。
- 帮助检查的命令与参数清单必须与 `internal/docker` 中真实的命令构造一致
  （`docker.RequiredCLICommands`）；新增 Docker 调用必须同步更新清单，否则预检会漏检。
- 帮助文本可能写入标准输出或标准错误，核对时必须同时读取两个流；退出码为 0 但没有输出
  时不能判为通过。
- 镜像预检只查询 `FROM` 元数据，禁止 `docker pull`、完整 `docker build`、创建临时容器或
  运行镜像内程序，也不得改变远端镜像缓存、容器与卷状态。
- 校验超时集中定义在 `internal/docker/validation.go`，不得分散写入 Handler 或业务方法。

## 提交约定

- 提交信息使用简体中文，一行标题说明改动的目的。
- 一次提交只做一件事；格式化产生的无关改动不混入功能提交。
- 不提交 `bin/`、`.agentflow/` 与本地配置文件。
- 不提交任何真实 API Key、访问凭据或本机绝对路径。

## 文档

- 代码编辑期间不同步文档。
- 需要更新文档时，只描述已实现的能力，不提前声明尚未落地的命令或接口。
- 命令或输出目录发生变化时，README 必须同步更新。
