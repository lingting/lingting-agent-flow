# AgentFlow 测试

本目录是 AgentFlow 唯一的测试代码位置。生产源码目录（`internal/`、`cmd/`）不再存放
测试文件，新增测试一律放在本目录下。

## 目录结构

```text
tests/
├── internal/
│   ├── testkit/        # 统一测试入口（testkit.Main）、日志落盘、工作空间、known_hosts 快照与测试数据构造
│   └── fakepi/         # 以测试二进制充当 Pi CLI 的可控替身
├── unit/               # 快速单元测试，不依赖真实 Pi、SSH 或网络
│   ├── agent/          # Agent 事件、结果类型与容器内 Pi Runner（使用替身）
│   │   └── pi/         # Pi Runner 单元测试（使用 fakepi 替身）
│   ├── app/            # 应用服务层
│   ├── config/         # 配置加载与校验
│   ├── container/      # 容器状态、生命周期策略与 Docker 命令参数构造
│   ├── domain/         # 领域模型与状态迁移
│   ├── environment/    # 环境类型、版本号与镜像引用规则
│   ├── image/          # 构建上下文哈希、Dockerfile 校验与语言模板
│   ├── process/        # 子进程监管
│   ├── source/         # Git Clone 代码来源（容器执行替身）
│   │   └── codesaver/  # 代码保存：提交、推送、合并与状态记录
│   ├── ssh/            # SSH 主机密钥与类型记录、认证配置、路径与结果状态
│   ├── httpapi/        # HTTP API 处理器：健康检查、错误结构、API 路径边界与 404
│   ├── lifecycle/      # 后台服务生命周期：启动顺序、逆序清理、超时与故障传播
│   ├── server/         # 统一服务编排：启动失败清理、优雅关闭与组件故障
│   ├── store/          # SQLite 仓储与数据库约束
│   ├── webui/          # 前端静态资源处理器与 SPA 回退
│   └── testkit/        # testkit 自身的测试
├── integration/
│   ├── local/          # 本机集成测试，不依赖外部服务
│   │   ├── database/   # 持久化工作流全链路、Schema 审计、旧库升级
│   │   ├── httpserver/ # HTTP 服务：请求分发表、静态资源、SPA 回退与关闭
│   │   ├── docker/     # 远程 Docker 命令构造与结果解析（使用执行替身）
│   │   ├── environment/# 环境版本文件写入与清单读写（真实文件系统）
│   │   ├── session/    # Session 提取与持久化边界（真实文件系统）
│   │   ├── codesaver/  # 真实 Git 提交、推送与合并（本机 bare 仓库）
│   │   ├── source/     # 容器内 Git Clone 与任务分支（真实 Git）
│   │   ├── workflow/   # 应用层编排的启动恢复与重启一致性（真实 SQLite）
│   │   ├── ssh/        # 本地模拟 SSH/SFTP 服务端
│   │   └── sshprobe/   # 本机 TCP 探测
│   ├── pi/             # 需要本机真实 Pi CLI
│   ├── remote/
│   │   ├── agent/      # 需要真实远程容器与容器内真实 Pi CLI
│   │   ├── codesaver/  # 需要真实远程容器与容器内 Git
│   │   ├── workflow/   # 需要真实远程容器与真实工作流前置条件
│   │   └── docker/     # 需要真实远程 Linux 机器与真实 Docker Engine
│   └── ssh/            # 需要真实远程 Linux 机器
└── e2e/
    ├── cli/            # CLI 完整用户操作路径
    └── packaging/      # 单文件打包冒烟测试（前端构建 + Go 编译 + 独立目录启动）
```

## 测试入口

每个目录都是独立的 Go 测试包，可直接用 `go test` 执行，无需额外脚本解析日志。所有入口
统一经 `testkit.Main` 启动，因此即使不显式加 `-v`，本次运行的逐用例结论与跳过原因也会
写入 `<TEST_ARTIFACT_DIR>/test.log`（见「运行日志」）。

```bash
# 快速单元测试（日常反馈，不访问真实模型与远程网络）
go test -count=1 -v -p 8 -parallel 8 ./tests/unit/...

# 本机集成测试
go test -count=1 -v -p 8 -parallel 8 ./tests/integration/local/...

# 本机 Git 代码保存集成测试
go test -count=1 -v ./tests/integration/local/codesaver/...

# 真实 Pi 集成测试（在 tests/config/pi.yml 中把 pi.enable 设为 true 后才会执行）
go test -count=1 -v ./tests/integration/pi/...

# 真实 SSH 集成测试（在 tests/config/ssh.yml 中把 ssh.enable 设为 true 后才会执行）
go test -count=1 -v ./tests/integration/ssh/...

# 真实远程 Docker / Agent / 工作流集成测试
# （docker.yml 的 docker.remote_enabled 与 ssh.yml 的 ssh.enable 都必须为 true）
go test -count=1 -v ./tests/integration/remote/docker/...
go test -count=1 -v ./tests/integration/remote/agent/...
go test -count=1 -v ./tests/integration/remote/workflow/...

# CLI 端到端测试
go test -count=1 -v ./tests/e2e/...

# 单文件打包冒烟测试（需要 pnpm 与已安装的前端依赖，否则自行跳过）
go test -count=1 -v -run TestSingleBinaryPackaging ./tests/e2e/packaging/...

# 完整测试
go test -count=1 -v -p 8 -parallel 8 ./tests/...

# 竞态检测
go test -race -count=1 ./tests/unit/...
# 数据库约束、Schema 审计与旧库升级
go test -count=1 -v ./tests/unit/store/ ./tests/integration/local/database/

# 竞态检测（代码保存的并发隔离）
go test -race -count=1 ./tests/unit/source/codesaver/ ./tests/unit/store/
# 竞态检测（数据库迁移与并发访问）
go test -race -count=1 ./tests/integration/local/database/
# 竞态检测（任务级工作流编排与并发隔离）
go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/
# 竞态检测（SSH 连接管理与并发操作，需要真实协议交互）
go test -race -count=1 ./tests/integration/local/ssh/...
```

并发由 Go 测试工具负责：`-p` 控制同时运行的测试包数量，`-parallel` 控制单个包内
并行用例数量。用例内部通过 `t.Parallel()` 声明可并行，不依赖固定执行顺序。

## 测试产物

每个测试包启动时在系统临时目录下创建随机根目录，并把绝对路径输出为：

```text
TEST_ARTIFACT_DIR=<绝对路径>
```

实际位置为 `os.TempDir()/lingting-agent-flow-tests/<包名>-<随机数>/`。每个用例在其中
获得独立子目录，数据库、Session、日志、上传下载文件都放在该目录内。

测试结束只关闭数据库连接、子进程与监听端口，**不删除任何测试产物**，便于检查失败
现场。需要清理时由开发者手动删除 `lingting-agent-flow-tests` 下的历史目录。

## 运行日志

每个测试包通过 `testkit.Main` 作为唯一入口（`os.Exit(testkit.Main(m))`），它把本次运行
的完整输出写入 `<TEST_ARTIFACT_DIR>/test.log`：

- 逐用例结论（`=== RUN` / `--- PASS` / `--- FAIL` / `--- SKIP`）；
- `t.Logf` 与 `fmt.Println` 等标准输出内容；
- `WARN [SKIP]` 跳过原因（例如配置未启用、缺少配置项、本机没有 `pi`）；
- 应用服务的 `slog` 日志：测试统一用 `testkit.Logger` / `testkit.Slog` 构造日志器。

入口同时默认打开 `-test.v`，因为非 verbose 模式下 `go test` 会缓冲包输出、只在失败时
打印，逐用例结论与跳过原因都会丢失；命令行显式给出 `-test.v=false` 时以命令行为准。

基目录下的 `latest-run.txt` 记录最新一次运行 `test.log` 的绝对路径，复核结论与跳过原因
时不需要重跑测试：

```bash
cat "$(os.TempDir)/lingting-agent-flow-tests/latest-run.txt"   # 具体路径按平台解析
```

已有 `TestMain` 的包（`tests/integration/pi`、`tests/unit/agent/pi`、`tests/unit/process`）
在替身分支之后转调 `testkit.Main`，替身行为不受影响。

## SSH 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/ssh/` | 主机密钥指纹与结论、已记录主机密钥类型、认证配置、私钥路径、参数转义、远程路径、结果状态 | 无 |
| `tests/integration/local/ssh/` | 本机真实 SSH 握手与认证、多主机密钥类型协商、远程命令执行、取消与超时、SFTP 传输、工作空间解析、并发与资源释放 | 无（测试内启动服务端） |
| `tests/integration/ssh/` | 真实远程 Linux 机器上的认证、命令状态、超时、SFTP 与工作空间 | tests/config/ssh.yml 指定的 SSH 目标 |

本机集成测试使用 `golang.org/x/crypto/ssh` 启动真实的服务端进程内实例，并配合
`github.com/pkg/sftp` 的服务端实现，因此主机密钥校验、密码/私钥/Agent 认证、
SFTP 上传下载与远程命令执行都经过真实协议交互，但不访问外网。

SSH Agent 用例在测试工作空间中创建 Unix 套接字并提供 Agent 协议服务；平台不支持
Unix 套接字时该用例输出 `WARN [SKIP]` 并跳过，其余用例仍会执行。

所有 SSH 测试都禁止关闭主机密钥校验，禁止自动接受未知主机密钥。

### 主机密钥类型协商

服务端可能同时提供多种主机密钥（例如 ed25519 与 ECDSA），known_hosts 里可能记录了其中
若干种。`sshclient.Dialer` 会按 known_hosts 中该主机已记录的类型**逐个**尝试握手：每次
只用一种类型，校验通过并完成认证即成功；某个类型校验失败或服务端不再提供该类型时换下
一个。没有任何记录时不做限制，交给 `golang.org/x/crypto/ssh` 自身协商。

逐类型重试只针对主机密钥类失败（未知、已变化、已吊销、无共同算法）；TCP 不可达、超时、
取消与认证失败会立即返回，避免换类型掩盖真实原因。全部已记录类型都不可用时返回第一个
失败类型对应的结论。

测试覆盖：

- `tests/unit/ssh/hostkey_algorithms_test.go`：已记录类型按文件顺序去重返回、无记录返回
  空、主机字段的多种写法（带端口、逗号多主机、通配符、哈希主机名）、注释与 `@revoked`、
  `@cert-authority` 行的忽略规则。
- `tests/integration/local/ssh/hostkey_retry_test.go`：进程内多主机密钥服务端，验证旧记录
  失效时回落到下一条记录、记录顺序决定优先类型、全部类型失败时返回首个结论。

## Docker 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/environment/` | 环境类型、版本号递增、构建状态迁移、镜像引用与 ID 校验 | 无 |
| `tests/unit/image/` | 构建上下文确定性哈希、Dockerfile 静态校验、语言模板、构建配置哈希 | 无 |
| `tests/unit/container/` | 容器状态迁移、detached 语义、生命周期策略、缓存卷命名、创建参数安全边界、JSON 解析 | 无 |
| `tests/integration/local/docker/` | 预检错误分类、命令构造与转义、幂等删除、状态不确定分类、清单读写 | 无（使用 SSH 执行替身） |
| `tests/integration/local/environment/` | 版本文件不可覆盖、清单落盘一致性、构建上下文上传与清理 | 无（远程路径映射到本机目录） |
| `tests/integration/remote/docker/` | 真实远程预检、镜像构建、容器创建/启动/执行/停止/删除 | tests/config/ssh.yml 指定的 SSH 目标与真实 Docker |

本机 Docker 测试使用可控的 SSH 执行替身：替身记录收到的远程命令并返回预设输出，
因此命令构造、参数转义、结果解析与错误分类都经过真实代码路径，但不需要 Docker Engine。
本机环境测试把远程绝对路径映射到测试工作空间中的目录，因此文件写入、原子替换与
"历史版本不可覆盖"都在真实文件系统上验证。

真实远程 Docker 测试只使用带随机后缀的测试镜像标签、容器名称与远程目录，并只清理
自己创建并已核验所有权的测试容器；不会执行全局 prune，也不会删除共享镜像或缓存卷。

### 远程真实测试目录与容器零残留

- 远程主机上的测试资源统一位于 `<远程 Home>/.lingting/agentflow-tests/<测试包>/<用例标识>`，
  由 `tests/internal/testkit` 的 `RemoteTestRoot` / `ResolveRemoteTestRoot` 统一解析与创建；
  旧路径 `~/agentflow-tests` 与 `~/.lingting/agentflow-e2e` 已废弃，不再写入。
  子目录按测试包划分（`agent` / `docker` / `codesaver` / `workflow` / `ssh`），
  新增测试包必须在 `testkit` 的白名单中登记。
- 远程主机目录与容器内部目录是两套约定：`/workspace/project`、`/tmp/agentflow/sessions`
  等容器内路径不受该规则影响；本机测试产物仍使用
  `os.TempDir()/lingting-agent-flow-tests/`。
- 每个测试容器都必须在**创建之前**登记清理责任（`testkit.ContainerRun.Track`），取得真实
  Docker ID 后立即补写；创建失败、启动失败与后续步骤失败都不允许失去清理能力。
- 清理必须核验真实状态（`testkit.CleanupContainer`）：删除前核验所有权（所有权不符时
  禁止删除），删除后重新检查容器是否真的不存在；状态无法确认按清理失败处理。
  删除方法的返回值不能作为"容器已不存在"的证据，清理失败也不能只打印 `WARN` 后通过。
- 每个真实远程测试包在 `TestMain` 中通过 `testkit.RegisterContainerFinalizer` 注册包级
  最终核验：`testkit.Main` 在全部测试（含 `t.Parallel`）结束之后、日志刷新之前执行它，
  用真实 `docker ps -a` 清单确认本次运行的容器已全部消失；核验失败会让测试进程以非零码
  退出。远程测试未启用时该核验报告 `SKIP`，不虚构"零残留通过"。
- 校验发现的日志行固定为 `REMOTE_CONTAINER_CLEANUP`、`REMOTE_CONTAINER_EVIDENCE`、
  `REMOTE_CONTAINER_BASELINE` / `REMOTE_CONTAINER_FINAL`、`REMOTE_CONTAINER_RESIDUE`、
  `REMOTE_CONTAINER_PREEXISTING`、`REMOTE_CONTAINER_VERIFY` 与 `FINALIZER`。
- 机制与验收证据见 `.steps/remote-test-resource-cleanup.md`。

## 测试配置

需要真实外部依赖的测试统一由 `tests/config/` 下的 YAML 配置驱动，示例文件随仓库提交，
真实配置文件不入库。环境变量只用于在配置值中写 `${NAME}` 占位符，不再有逐项覆盖配置的
测试专用环境变量（`AGENTFLOW_TEST_CONFIG_DIR` 仍然存在，仅用于把测试指向另一份配置目录）。

### 真实 Pi

真实 Pi 测试的全部配置来自 `tests/config/pi.yml`（示例文件为 `example.pi.yml`）：

| 配置项 | 用途 | 默认值 |
|---|---|---|
| `pi.enable` | 是否执行需要真实 Pi CLI 的测试 | `false` |
| `pi.model` | 真实 Pi 测试使用的模型 | `group/free` |
| `pi.executable` | Pi 可执行文件路径 | PATH 中的 `pi` |

`pi.enable` 未设为 `true` 或本机 PATH 中没有 `pi` 时，真实 Pi 用例输出 `WARN [SKIP]` 并
跳过；Pi Runner 的单元测试位于 `tests/unit/agent/pi/`，使用替身，始终执行；统一 Runner 的
生命周期用例位于 `tests/unit/app/`，验证「启动失败不残留 running」「停止服务取消在途调用
并收敛」等约束，同样不依赖本机 Pi。

默认模型 `group/free` 是特意选定的非标准标识，避免测试意外消耗付费额度。每次真实
调用都显式传入模型，不会回退到 Pi CLI 配置中的默认模型。

### 真实 SSH

真实 SSH 测试的全部配置来自 `tests/config/ssh.yml`（示例文件为 `example.ssh.yml`）：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `ssh.enable` | 是否执行需要真实远程机器的测试 | 是，默认 `false` |
| `ssh.host` | SSH 服务器地址 | 是 |
| `ssh.user` | 登录用户名 | 是 |
| `ssh.port` | SSH 端口 | 否，默认 `22` |
| `ssh.private_key_path` | 私钥路径，本版本只支持私钥认证 | 是（连接类用例） |
| `ssh.known_hosts` | 只读 known_hosts 文件列表 | 否，留空时复用本机 `~/.ssh/known_hosts` 与 `known_hosts2` |

示例：

```bash
cp tests/config/example.ssh.yml tests/config/ssh.yml   # 填写内容并把 ssh.enable 设为 true
go test -count=1 -v ./tests/integration/ssh/...
```

`ssh.enable` 未设为 `true` 时输出 `WARN [SKIP]`；已启用但必需目标缺失时说明缺少哪一项；
目标配置完整但连接、认证或主机密钥校验失败时必须让测试失败。真实 SSH 测试始终执行主机
密钥校验，不会自动接受未知主机密钥。远程文件操作只发生在随机生成的远程临时目录内。

known_hosts 来源由 `tests/internal/testkit` 的 `KnownHostsSources` 统一解析：`ssh.known_hosts`
显式配置时优先，文件不可读时直接让测试失败；未配置时复用本机用户 OpenSSH 的 known_hosts，
与产品 `internal/app.SSHService` 的默认行为一致。解析结果会先快照复制进当前用例工作空间，
校验只读快照，因此不会读取或修改用户真实配置。配置中的取值支持 `${NAME}` 占位符，
需要引用环境变量时写在 yml 里。

产品侧 known_hosts 全程只读（不创建、不追加、不加锁、不写临时文件），测试也遵守同一边界：
需要信任某主机时直接构造 known_hosts 文件，而不是调用任何写入接口。

每个真实 SSH 用例都会在日志中输出目标与 known_hosts 来源，便于区分"配置写错"与
"连接层缺陷"。主机密钥未知时，失败信息会列出实际读取的 known_hosts 路径与可用配置项，
并提示用 OpenSSH 把已核验的密钥追加进 known_hosts。

### 真实远程 Docker

真实远程 Docker 测试同样由配置文件驱动：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `docker.remote_enabled` | 显式启用真实远程 Docker 测试 | 是，默认 `false` |
| `ssh.enable` 及 `ssh.*` | SSH 目标，见「真实 SSH」 | 是 |

示例：

```bash
# 先在 tests/config/docker.yml 把 docker.remote_enabled 设为 true，
# 并在 tests/config/ssh.yml 把 ssh.enable 设为 true 且填好目标
go test -count=1 -v ./tests/integration/remote/docker/...
```

`docker.remote_enabled` 未启用时输出 `WARN [SKIP]`，普通测试不会修改真实远程 Docker；
该开关已启用但 `ssh.enable` 未开启或 SSH 目标缺失时说明缺少的配置项并跳过；目标完整但
认证失败、Docker 不可用或断言不成立时必须让测试失败。

## 编写测试的约定

- 使用 `testkit.New(t)` 创建工作空间，用 `testkit.Dir(t)` 取代 `t.TempDir()`。
- 不要用 `t.Setenv` 修改全局环境来切换数据目录；通过结构体、构造参数或子进程环境
  传入路径。
- 每个用例的数据库、Session、输出文件路径必须来自自己的工作空间。
- 需要真实外部依赖时，由测试自身检查依赖并在缺失时输出警告后跳过。
- 成功判定必须基于具体业务结果（退出码、数据库记录、文件内容、状态字段），不能只
  依赖日志文本。
- Docker 相关测试必须在 `tests/config/docker.yml` 中显式启用 `docker.remote_enabled`
  才允许修改真实远程 Docker；本机测试一律使用执行替身或路径映射，不依赖本机是否安装
  Docker。
- 测试结束不得删除测试工作空间与产物；真实远程测试的远程测试目录同样保留（只回收测试
  容器，不删除共享镜像、缓存卷与非测试容器）。

## 远程执行测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/source/` | 仓库地址校验、目标目录冲突、Git 缺失分类、参数转义、基础分支识别优先级、任务分支初始化、取消与超时、结果不确定分类 | 无（使用容器执行替身） |
| `tests/unit/agent/` | 容器内 Pi 参数构造、调用标识唯一性、标准输入、退出状态分类、Session 缺失/残缺、导出失败保留 JSONL、容器所有权核验 | 无（使用容器执行替身） |
| `tests/integration/local/source/` | 真实 Git 克隆、基础分支识别、`agent/<task-id>` 分支、远程任务分支检出、目标目录不被删除、并发目录隔离 | 本机 `git` |
| `tests/integration/local/session/` | 容器内 → 宿主机 → 本机三段式传输、字节校验、传输中断保留部分文件、导出失败保留 JSONL、并发 Session 隔离、容器删除后本机 Session 可读 | 无（路径映射到测试工作空间） |
| `tests/integration/remote/agent/` | 真实远程容器内 Git Clone、容器内 Pi 调用、独立 Session、本机持久化、容器删除后 Session 仍可读 | tests/config/ssh.yml 指定的 SSH 目标、真实 Docker 与容器内真实 Pi CLI |

本机 Git 缺失时，`tests/integration/local/source/` 的用例输出 `WARN [SKIP]` 并跳过，
其余用例仍会执行。

## 代码保存测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/source/codesaver/` | 容器登记与实际 ID 不一致、detached 容器、任务分支不匹配、非 Git 工作区、无变更、既有冲突、提交身份缺失、提交信息与作者参数传递、提交成功与 SHA 读取、提交异常但 HEAD 已变化、既有暂存内容不被重置、只推送任务分支、禁止强制推送、远程缺失、内嵌凭据拒绝、认证失败、非快进冲突、推送超时但远程已收到、远程与本地不一致、无法核验远程、合并默认关闭、推送失败禁止合并、基础分支识别失败、合并冲突、主分支推送失败、合并结果不确定、记录先于远程操作落盘、并发隔离、取消不影响其他任务、**提交成功但记录写入失败（`state_unconfirmed` 与内存结论并存）**、**数据库停留在 pending 且基线可支撑恢复判定**、**短暂写入故障后重新执行可完整落盘**、**`fail` 使用未取消上下文**、**提交与合并基线在副作用之前落盘**、**只读核验确认三种结论且不写记录/不产生副作用**、**重复核验幂等**、**核验失败保留不确定性** | 无（使用容器执行替身） |
| `tests/unit/domain/` | `DecideCodeSaveRecovery` 判定表：提交/推送/合并三个窗口的"已发生/未发生/不确定"，缺少基线与核验失败一律转人工核验，仅"确认未发生"才可安全重试、**合并父关系无法匹配时不得判定已合并**、**`RecoveryConverged` 收敛判定** | 无（纯函数） |
| `tests/unit/store/` | `code_saves` 写入与更新、状态字段约束（成功必须带 SHA）、未请求合并不能记录合并结果、按任务查询、并发写入独立、恢复字段读写（基线/落盘状态/恢复结论）、历史记录（新列为 NULL）可读且不被误判、未知恢复状态原样读回、**核验结论条件写入（读取后结论被改写返回 `ErrConflict` 且不覆盖）、核验只写 `recovery_status` 不覆盖提交/推送状态、记录不存在返回 `ErrNotFound`** | 无 |
| `tests/integration/local/codesaver/` | 真实 Git 提交、任务分支真实 Push 到 bare 仓库、远程引用核验、默认不合并、显式合并后基础分支包含任务提交、推送失败不误报成功、合并冲突保留现场、无变更不制造空提交、并发仓库隔离、**真实 Git 已提交但记录写入失败时基线与真实 HEAD 一起确认"提交已发生"**、**远程与预期提交不一致时转人工核验**、**远程不可达时保留不确定性**、**真实合并后依据第一父/第二父确认合并已发生** | 本机 `git` |
| `tests/integration/remote/codesaver/` | 真实远程容器内提交、推送与合并，远程任务分支与基础分支 commit 核验 | tests/config/ssh.yml 指定的 SSH 目标、真实 Docker 与容器内 Git |

本机 Git 缺失时，`tests/integration/local/codesaver/` 的用例输出 `WARN [SKIP]` 并跳过，
`tests/unit/source/codesaver/` 与 `tests/unit/store/` 的用例仍然执行。

### 代码保存测试的配置

远程代码保存测试使用 `tests/config/docker.yml` 与 `tests/config/ssh.yml`：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `docker.remote_enabled` | 显式启用真实远程容器测试 | 是 |
| `docker.git_image` | 远程代码保存测试使用的容器镜像（需包含 `git`） | 是 |
| `ssh.enable` 及 `ssh.*` | SSH 目标，见「真实 SSH」 | 是 |

```bash
# 本机 Git 集成测试（不依赖公网）
go test -count=1 -v ./tests/integration/local/codesaver/...

# 真实远程容器代码保存测试（先在 docker.yml 与 ssh.yml 中启用并填写配置）
go test -count=1 -v ./tests/integration/remote/codesaver/...
```

`docker.remote_enabled` 未启用时输出 `WARN [SKIP]`；已启用但 `ssh.enable` 未开启、SSH 目标
或 `docker.git_image` 缺失时说明缺少的配置项并跳过；目标完整但连接、认证、主机密钥校验
失败或断言不成立时必须让测试失败。

远程代码保存测试只在本次测试创建并已核验所有权的容器内操作，只使用带随机后缀的容器
名称与容器内临时目录，不执行全局 prune，也不在用户的真实主分支上执行合并。

### 远程 Agent 测试镜像

`tests/integration/remote/agent/Dockerfile` 定义测试用任务环境镜像，基础镜像与 Pi CLI
版本都固定到具体版本。构建方式（在仓库根目录执行，可在远程机器上执行）：

```bash
docker build -t agentflow-test-agent:1.0.0 -f tests/integration/remote/agent/Dockerfile .
```

### 远程 Agent 测试的配置

远程 Agent 测试同样使用 `tests/config/docker.yml` 与 `tests/config/ssh.yml`：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `docker.remote_enabled` | 显式启用远程 Docker 测试 | 是 |
| `docker.agent_image` | 测试使用的镜像引用 | 否，默认 `agentflow-test-agent:1.0.0` |
| `ssh.enable` 及 `ssh.*` | SSH 目标，见「真实 SSH」 | 是 |
| `docker.pi.*` | 可选的容器内 Pi 模型接入覆盖 | 否 |

真实模型标识来自 `tests/config/pi.yml` 的 `pi.model`，默认 `group/free`；该配置在本目录
只提供模型标识，是否执行本机真实 Pi 测试由同一文件的 `pi.enable` 控制。

```bash
# 先在 docker.yml 与 ssh.yml 中启用，并准备好测试镜像
go test -count=1 -v ./tests/integration/remote/agent/...
```

容器内 Pi CLI 是否可用由测试在容器内实际执行 `pi --version` 确认，不依据本机是否安装 Pi。
远程测试只删除本次测试创建并已核验所有权的容器，不执行全局 prune。

## 开发—验证闭环测试分层

单步骤"开发 → 验证"闭环的测试集中在两个目录，全部使用受控替身，不依赖真实 Pi、SSH、
Docker 或模型：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/domain/` | 收口决策（`DecideStepCloseout`）、验证状态推导（`DeriveStepVerificationState`）、失败反馈构建与脱敏裁剪、代码保存恢复判定（`DecideCodeSaveRecovery`，纯函数） |
| `tests/unit/app/` | 闭环协调器：开发成功后启动验证、开发失败/取消不启动验证、必需检查失败不得完成、基础设施异常不得完成、验证配置无效拒绝、容器不可用不回退本机、失败反馈持久化与重新执行引用、并发请求拒绝、开发记录写入失败不验证、服务停止取消在途闭环 |
| `tests/unit/httpapi/` | 闭环接口：200/400/404/409/500 映射、`verified` 与 `validation_ran` 不互相替代、验证历史查询 |
| `tests/unit/store/` | `validation_feedbacks` 的追加、只追加不覆盖、按步骤取最近一次、不存在语义 |

闭环用例的关键约束：

- 用例的容器替身同时实现 `StepExecutionContainerPort` 与 `ValidationContainerPort`，因此可以
  断言"开发与验证看到的是同一个容器与同一份工作区"。
- 开发阶段结论由替身返回，验证阶段结论由替身返回的检查退出码与启动状态决定；不通过 Mock
  伪造"验证通过"。
- 验证失败反馈用例会检查输出摘要已脱敏（例如 `password=` 的内容被隐藏）且长度受限。
- 服务停止用例会确认在途闭环收敛，且停止后不再接受新请求。

## 任务级工作区初始化测试分层

统一启动中的「初始化工作区」阶段（`app.TaskWorkspaceService`）与尝试级的 `code init` 是两个
层次，测试按此分开：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/domain/` | 记录状态机（`uncertain` 非终态、终态不可重开）、`DecideTaskWorkspaceNext`（无记录/成功/跳过/失败/超时/不确定/占用）、`DecideTaskWorkspaceRecovery` |
| `tests/unit/app/` | 协调器：成功写入完整记录、同容器重复初始化记录 `skipped` 且不重新 Clone、换容器后重新初始化、`uncertain` 阻止重放、容器未运行/detached/已删除/缺失的前置条件错误、失败阶段与错误类别落库、超时与取消映射、事件写入、中断收敛不改写终态 |
| `tests/unit/store/` | `task_workspace_initializations` 的写入、按任务/容器查询、同任务同容器只允许一条未结束记录（部分唯一索引）、中断记录查询、未知枚举值原样读回、成功必须带 commit、记录不存在语义 |
| `tests/unit/httpapi/` | 启动接口的工作区阶段与错误映射：`uncertain` → 409、前置条件缺失 → 412、未落库 → `execution_not_persisted` |
| `tests/integration/local/workflow/` | 真实 SQLite 上的阶段顺序、结果不确定时停在 workspace 阶段、重启后记录仍有效、中断记录收敛为 `uncertain` 且不重放 |
| `tests/integration/local/fetcher/` | 真实 Git：任务级初始化复用同一套 `source.Fetcher`（`AttemptID` 为空、任务分支正确、工作区落在容器内目录）、重复初始化不覆盖已有改动、目标目录非空时失败且不删除内容 |

工作区用例的关键约束：

- 任务级初始化不携带 `attempt_id`：用例显式断言传给 `FetchSourceWith` 的 `AttemptID` 为空，
  防止后续实现为了复用尝试级代码而伪造归属。
- 集成用例的容器替身同时是代码获取端口，内部直接调用 `source.NewGitFetcher`，因此断言的是
  "走的是同一条实现"，而不是"另写了一套 Git 命令"。
- 重启通过"关闭连接 → 同一路径重新打开"模拟，只断言数据库里的持久化事实。
- 收敛用例的容器替身在被调用时直接失败，用来证明恢复路径不访问容器、不重放代码获取。

## 启动恢复测试分层

启动恢复（`app.TaskWorkflowService.Start`）的测试分两层，纯函数与链路分开覆盖：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/domain/` | 收敛决策纯函数：`DecidePlanGenerationRecovery`、`DecideCancellationRecovery`（含"未结束执行优先于取消请求"的规则顺序） |
| `tests/unit/app/` | 计划生成的失败与恢复路径：非法输出、非零退出、超时、用户取消、服务停止中断、计划事务回滚、并发占用、`planning` 残留收敛 |
| `tests/integration/local/workflow/` | 真实 SQLite 上的重启一致性：关闭连接后用同一路径重新打开，构造新的编排器实例并 `Start`，随后只查库断言 |

集成用例的约束：

- 使用真实数据库文件与真实 `app.TaskWorkflowService`，只有 Agent 执行、容器核验与单步骤闭环
  是替身；恢复链路、事务边界、状态机与写入全部走产品代码。
- 重启通过"关闭连接 → 同一路径重新打开"模拟，不使用同一个 `*sqlite.DB` 句柄冒充重启。
- 断言对象是数据库事实：任务状态与 `failure_summary`、步骤集合、`workflow_runs`、`sessions`、
  `events`，不以方法返回值代替持久化断言。
- 替身记录调用次数，用于断言"恢复不重放 Planner、不执行步骤"。
- 恢复写入失败用 `failingStore`（包装真实存储、只让指定方法返回错误）注入，验证启动失败且
  现场保持原状。
- 并发恢复用例用同一启动信号同时放行两个实例，断言持久化事实（最终状态唯一、恢复事件不重复），
  不断言启动返回值——恢复是幂等收敛，竞争结果由数据库状态前置条件决定。

```bash
go test -count=1 -v ./tests/integration/local/workflow/
go test -race -count=1 ./tests/integration/local/workflow/
```

## 数据库约束与迁移测试

数据库结构遵循两条长期规则：**枚举字段不受数据库级封闭取值限制**，**业务 Schema
不声明外键**。相关测试负责守住这两条规则，并验证旧数据库升级不丢数据。

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/store/` | 仓储读写、未知枚举值原样保留、逻辑删除、唯一约束与非枚举 `CHECK` 仍生效、并发写入与事务 | 无 |
| `tests/integration/local/database/` | 迁移后完整 Schema 审计（外键、触发器、枚举白名单、索引、必需约束）、未知枚举值可写入、旧版本数据库升级与数据保留、自增主键续号、迁移失败不留半成品、并发打开只迁移一次 | 无 |

```bash
# 数据库约束与 Schema 审计
go test -count=1 -v ./tests/unit/store/ ./tests/integration/local/database/

# 竞态检测（迁移与并发访问）
go test -race -count=1 ./tests/integration/local/database/
```

### 旧版本数据库样本

旧库样本由 `tests/internal/testkit.CreateLegacySchema` 在测试工作空间中直接执行仓库里
真实的 `0001…0008` 迁移脚本生成，不维护第二份 DDL，也不读取开发者本机的真实数据库。
样本随后写入完整的 Task / Step / Attempt / Session / 验证结果 / 事件 / 容器 / 环境版本
/ 代码保存 / 执行节点 / 角色记录，用于验证迁移前后记录数量与关键字段一致。

### Schema 审计范围

- 每张业务表的 `PRAGMA foreign_key_list` 必须返回零条记录。
- 建表语句不得包含 `REFERENCES` 与 `FOREIGN KEY`。
- 触发器只允许存在"私钥认证必须给出私钥路径"这一跨字段要求，不得模拟外键或枚举白名单。
- 枚举字段不得残留 `CHECK (... IN (...))` 形式的封闭取值集合。
- 主键、`NOT NULL`、`UNIQUE`、非枚举 `CHECK` 与关联索引必须保留。
- 全新数据库与"旧库迁移后"的建表语句必须逐表一致。

### 查看测试结果

每个测试入口输出 `TEST_ARTIFACT_DIR=<绝对路径>`，旧库样本、迁移后的数据库与 WAL 文件
都保留在该目录内。测试结束只关闭连接，不删除任何产物，便于直接打开 SQLite 文件核对
迁移结果。同一目录下的 `test.log` 记录本次运行的逐用例结论与日志（见「运行日志」），
复核时不需要重跑测试。
