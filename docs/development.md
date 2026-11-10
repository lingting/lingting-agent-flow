# 开发指南

本文件描述本地开发环境、常用命令、改动流程与提交约定。

- 目录布局与依赖方向：[workspace.md](workspace.md)
- 执行链路边界与状态规则：[architecture.md](architecture.md)
- 代码风格与硬性红线：根目录 `AGENTS.md`
- CLI 命令：[cli.md](cli.md)；HTTP 接口：[http-api.md](http-api.md)
- 测试规范：[testing.md](testing.md)
- SSH 与执行节点：[ssh-hosts.md](ssh-hosts.md)；Docker：[docker-compatibility.md](docker-compatibility.md)
- 前端与单文件交付：[frontend.md](frontend.md)；发布：[release.md](release.md)

## 发布阶段与兼容性政策

**AgentFlow 尚未正式开始发布运营，当前处于开发阶段，不存在必须维护的历史正式版本兼容性。**
根目录 `AGENTS.md` 的「项目发布阶段与兼容性政策」是这条规则的长期来源，本节说明它在日常开发中的具体含义。

### 不做什么

- 不为尚未正式发布的旧内部 API、旧数据结构、旧配置格式、旧数据库 Schema 或历史内部实现增加代码。
  这些版本从未对外承诺，改掉它们不需要过渡期。
- 不写兼容层、适配器、双写逻辑、数据回填、功能开关式新旧分支，也不保留"先留着万一有人用"的废弃接口。
- 不把历史版本兼容性包装成数据安全要求。数据安全只针对**当前**数据，不针对不存在的旧版本。
- 数据库不提供旧开发期库兼容：`internal/storage/sqlite/migrations/` 下的版本化脚本是唯一事实来源，由 Goose
  在打开数据库时执行；旧的手写 Schema 数据库既不识别也不升级，不自动删除、覆盖或重建，需要用户自行备份
  后手动重建数据库文件。

### 可以做什么

- 直接修改内部 API、领域类型、数据结构、配置格式与包边界，以当前确定的产品设计为准，不为了"少改调用方"
  保留两套实现。
- 替换自研组件时按最终目标架构实施，不要求保留旧实现作为回退方案；替换完成后旧实现必须删除，不能长期并存。

### 新增第三方依赖的准入

`AGENTS.md` 的代码约定要求"新增第三方依赖必须有明确用途"。发布阶段政策在此基础上补充：

1. **必须实际减少维护成本**：删掉的代码、避免的后续跟版工作要能对应到具体的功能收益；仅为统一风格、
   减少几行样板代码而引入依赖不成立。
2. **必须比自研更安全或更完整**：成熟组件无法完整满足当前产品要求时，不得为了减少代码牺牲既有功能、
   安全边界或数据正确性（例如 SSH 主机密钥校验、Docker API 版本固定、结果不确定语义）。
3. **不得制造双实现**：引入组件后若同一能力仍保留自研路径，必须能说明两条路径各自不可替代，
   否则应整体迁移或整体不迁移。
4. **必须记录评估结论**：涉及替换现有自研能力的组件评估，在
   [component-evaluations/](component-evaluations/) 下留一份明确结论（值得替换 / 部分值得替换 / 不值得替换）
   与复审触发条件，例如 [docker-engine-sdk.md](component-evaluations/docker-engine-sdk.md)。
5. **必须核对来源与许可**：维护状态、Go 版本要求、许可证、依赖规模（含强制引入的间接依赖）都要有事实依据。

### 与测试、文档的关系

- 本节不改变测试授权规则：未经明确要求仍不运行测试、不创建测试代码（见 [testing.md](testing.md)）。
- 组件评估属于文档产出，不修改生产代码与 `go.mod`；评估通过后由独立任务实施替换。
- 文档只描述已实现的能力。评估文档必须显式标注"未实施"，不得写成已经完成的能力。

## 环境要求

- Go：使用当前受支持的稳定版本，版本要求以 `go.mod` 声明为准。
- Git：用于工作区隔离与版本管理，Makefile 会用 `git rev-parse` 注入提交号。
- Node.js 22 + pnpm 11：仅在构建或开发 Web UI 时需要；`make release` 会用它完成前端生产构建，产物随后
  由 Go 通过 `//go:embed` 嵌入可执行文件。
- Pi CLI：只有 `agent check` 需要 PATH 中存在 `pi`，它不发起模型调用。所有真实 Agent 调用都在远程任务
  容器内执行，因此容器镜像内必须安装可用版本的 `pi`，并完成对应 Provider 的凭据配置。

SQLite 驱动为 `modernc.org/sqlite`，纯 Go 实现，无需安装 CGO 工具链或系统 SQLite。

数据库结构由 `internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本定义，通过 `//go:embed` 编译进程序：
每次打开数据库时由 Goose 按版本顺序执行待执行的迁移，已执行的版本记录在 `goose_db_version` 表中，重复
打开不会重复执行。Schema 变更只追加新的迁移脚本；程序不支持旧的手写 Schema 数据库，旧库需要用户自行
备份后手动重建数据库文件。

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
make check        # fmt-check + vet + build
```

`make test`（`go test ./tests/...`）包含 `tests/integration/{pi,ssh,remote}` 等真实依赖分组：本机是否
真的连接真实设备由 `tests/config/*.yml` 的 `pi.enable`、`ssh.enable` 与 `docker.remote_enabled`
决定（未启用时输出 `WARN [SKIP]`）；CI 中还会额外经过 `AGENTFLOW_REAL_TESTS` 授权门禁，且该门禁
只在 `GITHUB_ACTIONS` 下生效（见 [testing.md](testing.md) §5）。只想跑不需要真实设备的分组时使用
`make test-unit` / `make test-local` / `make test-race`。

不依赖 Make 时可以直接使用底层命令：

```bash
gofmt -w cmd internal tests
go vet ./...
go test -count=1 ./tests/...
go build -o bin/agentflow ./cmd/agentflow
```

`make build` 会通过 `-ldflags -X` 注入 `version`、`commit`、`buildDate`；直接 `go build` 时三者取默认的
开发值 `dev` / `none` / `unknown`。可以用环境变量覆盖版本号：

```bash
make build VERSION=0.1.0
```

前端命令在 `lingting-agent-flow-ui/` 下执行：

```bash
pnpm dev                  # 开发服务器（vp dev），/api 代理到 Go 服务
pnpm build                # 生产构建，产物写入 internal/webui/dist（含占位文件补回）
pnpm build:assets         # 仅执行 vp build，不补回占位文件
pnpm lint                 # 格式、lint 与类型检查
pnpm test                 # 单元测试（vp test run）
pnpm exec playwright test # 基础布局与连接状态浏览器测试
```

服务侧命令在仓库根目录执行：

```bash
go run ./cmd/agentflow server                        # 启动完整服务（API + 内嵌页面 + 后台服务）
go run ./cmd/agentflow server --address 127.0.0.1:26380
make serve                                           # 开发模式后端（--require-web-ui=false）
make web-build                                       # 前端生产构建（按锁文件安装依赖）
make release                                         # 单文件生产构建：前端 + Go
```

客户端的实际使用方式见 [cli.md](cli.md)；测试的目录、工作空间与真实依赖开关见 [testing.md](testing.md)。

同一份数据目录只允许一个 `agentflow server` 进程：它在启动时用数据库文件旁的 `<database_path>.server.lock`
取得独占文件锁，第二个进程立即失败并给出锁文件路径。数据库路径会先规范化为真实绝对路径（收敛相对路径、
冗余段与符号链接别名），锁文件与随后打开的 SQLite 使用同一条路径，因此指向同一数据库的不同路径写法不会各自
取得一把锁。锁文件属于运行期数据，`*.server.lock` 已被 `.gitignore` 忽略；排查"服务端启动失败"时先确认没有
遗留的服务端进程，而不是删除锁文件。

## 改动流程

1. 确认改动属于哪一层，按 [workspace.md](workspace.md) 的依赖方向选择位置。
2. 领域规则变化优先修改 `internal/domain`，其余层只做编排与适配。
3. 涉及状态迁移时，检查是否仍通过 `Transition` 校验，并确认迁移路径在状态机中已声明。
4. 涉及持久化时，在 `internal/storage/sqlite/migrations/` 下新增一个更高版本的迁移脚本，并在对应实体文件中
   补充仓储实现；不修改已执行过的脚本，也不手写版本判断。
5. 改动完成后执行 `gofmt` 与 `go vet`，并自查 `git diff`。

### 修改单步骤执行链路时

1. 准入规则只在 `internal/domain/step_execution.go` 修改：任务/步骤状态集合、角色类型、占用检查都集中
   在 `CheckStepExecutionStart`；不得在 `app` 或 `httpapi` 复制第二份判断。
2. 步骤占用必须仍走 `storage.StepExecutionStore.BeginStepExecution` 的单事务路径；不要在 `app` 里改成
   "先查再写"，否则并发请求会各自创建 running Attempt。
3. 新增执行结论时同步更新 `StepExecutionOutcome` 的 `AttemptStatus`、步骤终态映射与 HTTP 错误码，避免
   同一结论在不同层表达不一致。
4. 终态写入必须保持"三条互不阻断 + 未取消上下文 + 状态前置条件"的形式；新增写入路径同样不得因执行
   错误而跳过其余写入。
5. 取消与服务关闭必须继续区分：调用方取消记为 `cancelled`，服务停止记为 `interrupted`。
6. Timer/协程不得脱离 `lifecycle` 管理：执行协调器实现了 `Name/Start/Stop`，Server 关闭时会取消在途
   执行并等待收敛。

### 修改任务级工作流编排时

1. 启动校验只在 `domain.CheckWorkflowStart` 修改：任务状态、容器、计划序号、必需检查与现场明确性都
   集中在那里；不得在 `app` 或 `httpapi` 复制第二份判断。
2. 步骤执行必须继续复用 `app.StepWorkflowService.ExecuteAndValidate`：不要为了"少一层"在编排器里直接
   创建 Attempt 或拼接 Pi 命令。
3. 进度写入必须保持"先落盘、再执行有副作用的操作"：新增阶段同样要先写 `workflow_runs`，再调用容器、
   Pi 或 Git。
4. 新增工作流状态时同步更新 `workflowRunStatusMachine` 的合法迁移、`IsTerminal` / `CanStart` 与 HTTP
   响应中的 `status_terminal` / `status_active`，避免同一状态在不同层表达不一致。
5. 收尾结论只在 `domain.DecideWorkflowCloseout` 修改；不得因为"步骤都通过了"就跳过代码保存与会话归档
   判定。
6. 恢复逻辑必须继续依据数据库事实：新增恢复路径同样不得重放 Pi 调用，也不得抢占仍在推进的现场。
7. 编排器实现了 `Name/Start/Stop` 并注册进 `internal/lifecycle`：新增后台协程必须挂在它下面，Server
   关闭时会先停止新请求，再等待在途工作收敛。
8. 新增接口路径必须落在 `/api/v1/tasks/{taskID}/` 之下，并在 `router.go` 的 `isWorkflowPath` 中登记；
   否则会被任务处理器当作未知子路径返回 404。
9. 不要在 CLI 里重新装配执行栈：`internal/workflow` 引擎包已移除，任务执行只存在于
   `agentflow server` 的 `app.TaskWorkflowService` 中；`agentflow task run` 是 HTTP 客户端，新增执行
   参数同样只能通过 `POST .../execute-workflow` 的请求体表达。
10. 计划必须落库到 `steps`（含 `sequence` 与角色快照），不得在编排器或 Handler 里临时拼装步骤：
    `domain.CheckWorkflowStart` 只接受已持久化计划。当前唯一入口是
    `app.TaskWorkflowService.GeneratePlan`（`POST /api/v1/tasks/{taskID}/plan`），计划固定以 `plan` 阶段
    步骤开头、其后是 `develop` 步骤；新增计划来源也必须产出同样的结构。

### 修改统一任务启动流程时

1. 阶段判定只在 `domain.DecideTaskLaunchNext` / `CheckTaskLaunchStart` 修改：要不要建容器、要不要生成
   计划都集中在那里；不得在 `app`、`httpapi` 或 CLI 复制第二份判断。
2. 三个有副作用的阶段必须继续复用 `PrepareForTask`、`GeneratePlan` 与 `StartWorkflow`：不要在启动服务
   里直接创建容器、创建 Attempt 或拼接 Pi / Docker / Git 命令。
3. 容器可用性必须来自真实核验，不得凭 `tasks.container_id` 推断；核验失败按前置条件缺失返回 412，
   不得据此创建新容器。
4. 已有受管理且运行中的容器时不得创建第二个容器：重复准备只会留下无法被任何链路使用的孤儿容器，
   而孤儿容器不应被自动删除。
5. 任一阶段失败必须立即停止并保留真实 `FailureStage`（下层给出的更具体阶段优先），不得自动重试、不得
   删除容器、不得重放外部操作。
6. 步骤占用恢复只在 `domain.DecideStepOccupancyRecovery` 修改，且必须保持「不重放 Agent、不删除容器、
   所属任务仍有未结束执行时不抢占、写入带状态前置条件」。
7. 启动服务实现 `Name/Start/Stop` 并注册进 `internal/lifecycle`，注册顺序在任务级编排器之后；新增后台
   协程必须挂在它下面，Server 关闭时先停止新启动请求。
8. 新增路径必须落在 `/api/v1/tasks/{taskID}/` 之下，并在 `router.go` 中先于任务处理器判定，否则会被
   当作未知子路径返回 404；`isTaskLaunchPath` 的判定必须早于任务服务可用性检查。
9. 不要在 CLI 里重新装配执行栈：`agentflow task start` 只是 HTTP 客户端，新增执行参数只能通过
   `POST .../start` 的请求体表达。

### 修改项目级执行协调时

1. 排序、队首分类、阻塞与进度只在 `internal/domain/project_execution_plan.go` /
   `project_progress.go` 修改；不得在 `app`、`httpapi` 或前端复制第二份判定。
2. 项目执行权只能在存储层用带条件的 SQL 变更（取得 = 更新已到期行或插入；续租、释放带 `owner + epoch`），
   影响行数为 0 必须按冲突处理；不得改成"先查再写"，也不得用进程内互斥替代条件写入。
3. 不得修改或删除已执行过的迁移脚本：执行权与执行意图的结构变化一律追加更高版本的脚本。
4. 领取资格必须继续由调度器的项目闸门（`TaskSchedulerProjectGate`）约束：不要新增与 `TaskScheduler`
   竞争同一批任务的扫描循环，也不要让项目服务自己调用 `TaskLaunchService` 之外的启动流程。
5. 确认"同项目串行"仍然成立：新增显式启动入口时，必须在 `TaskLaunchService.Launch` 这条统一路径上
   核验同项目没有其他在途任务，而不是在每个 Handler 里各写一次。
6. 续租与收敛周期必须保持"续租周期 < 有效期"（当前 40 秒 / 2 分钟）；调整常量时同步检查
   `NewProjectExecutionService` 的校验与本文档。
7. 任务结论只能来自持久化状态：不得因 `Launch` 返回 `nil`、接口返回或 Agent 输出就把任务当作完成，
   也不得因执行权到期推断远端任务已停止。
8. 停止入口只记录意图并调用既有取消入口，不直接改写任务状态；不确定现场保留真实状态。
9. 项目执行状态与需求进度的接口字段发生变化时，同步更新 `docs/http-api.md` §17 与
   `.steps/V2-final.md` §2.6。

## 新增角色类型或状态

- 新增角色类型：在 `internal/domain/role.go` 增加 `RoleType` 常量与 `Validate` 分支，同时补齐
  `internal/domain/role_tools.go` 的能力上限（`RoleToolCeiling`）、默认策略与能力→Pi 工具映射；
  未补齐时 `EffectivePiTools` 会因"不支持的角色类型"拒绝调用，而不会回退到开发者权限。
  角色定义随代码内置，YAML 不再声明角色。
- 新增状态：在对应实体的状态机中声明合法迁移与终态，并确认重试、取消路径可达。
- 新增状态后，必须检查持久化层是否需要追加迁移脚本，并确认读取层仍原样保留未知取值。

## 提交约定

- 提交信息使用简体中文，一行标题说明改动的目的。
- 一次提交只做一件事；格式化产生的无关改动不混入功能提交。
- 不提交 `bin/`、`.agentflow/` 与本地配置文件。
- 不提交任何真实 API Key、访问凭据或本机绝对路径。

## 文档

- 代码编辑期间不改文档；只有明确要求时才同步文档。
- 文档只描述已实现的能力，不提前声明尚未落地的命令或接口。
- 新增或修改能力时，同步检查 `docs/` 下对应文档是否需要更新，而不是全部塞进 README。
