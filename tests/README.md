# AgentFlow 测试

本目录是 AgentFlow 唯一的测试代码位置。生产源码目录（`internal/`、`cmd/`）不再存放
测试文件，新增测试一律放在本目录下。

## 目录结构

```text
tests/
├── internal/
│   ├── testkit/        # 测试工作空间、通用辅助组件与工作流测试数据构造
│   ├── workflowfake/   # 工作流下层组件（容器/代码获取/Agent/命令/代码保存）替身
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
│   ├── ssh/            # SSH 主机密钥、认证配置、路径与结果状态
│   ├── store/          # SQLite 仓储与数据库约束
│   ├── workflow/       # 工作流引擎、状态机、重试与恢复核验
│   └── testkit/        # testkit 自身的测试
├── integration/
│   ├── local/          # 本机集成测试，不依赖外部服务
│   │   ├── database/   # 持久化工作流全链路、Schema 审计、旧库升级
│   │   ├── docker/     # 远程 Docker 命令构造与结果解析（使用执行替身）
│   │   ├── environment/# 环境版本文件写入与清单读写（真实文件系统）
│   │   ├── session/    # Session 提取与持久化边界（真实文件系统）
│   │   ├── codesaver/  # 真实 Git 提交、推送与合并（本机 bare 仓库）
│   │   ├── source/     # 容器内 Git Clone 与任务分支（真实 Git）
│   │   ├── workflow/   # 工作流持久化、进程重启恢复与并发隔离
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
    └── cli/            # CLI 完整用户操作路径
```

## 测试入口

每个目录都是独立的 Go 测试包，可直接用 `go test` 执行，无需额外脚本解析日志。

```bash
# 快速单元测试（日常反馈，不访问真实模型与远程网络）
go test -count=1 -v -p 8 -parallel 8 ./tests/unit/...

# 本机集成测试
go test -count=1 -v -p 8 -parallel 8 ./tests/integration/local/...

# 本机 Git 代码保存集成测试
go test -count=1 -v ./tests/integration/local/codesaver/...

# 真实 Pi 集成测试
go test -count=1 -v ./tests/integration/pi/...

# 真实 SSH 集成测试
go test -count=1 -v ./tests/integration/ssh/...

# 真实远程 Docker 集成测试（必须显式启用）
AGENTFLOW_TEST_REMOTE_DOCKER=1 go test -count=1 -v ./tests/integration/remote/docker/...

# 真实远程 Agent 集成测试（必须显式启用）
AGENTFLOW_TEST_REMOTE_DOCKER=1 go test -count=1 -v ./tests/integration/remote/agent/...

# 真实远程工作流集成测试（必须显式启用）
AGENTFLOW_TEST_REMOTE_DOCKER=1 go test -count=1 -v ./tests/integration/remote/workflow/...

# CLI 端到端测试
go test -count=1 -v ./tests/e2e/...

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
# 竞态检测（工作流状态机、租约与并发任务隔离）
go test -race -count=1 ./tests/unit/workflow/ ./tests/integration/local/workflow/
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

## SSH 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/ssh/` | 主机密钥指纹与结论、认证配置、私钥路径、参数转义、远程路径、结果状态 | 无 |
| `tests/integration/local/ssh/` | 本机真实 SSH 握手与认证、远程命令执行、取消与超时、SFTP 传输、工作空间解析、并发与资源释放 | 无（测试内启动服务端） |
| `tests/integration/ssh/` | 真实远程 Linux 机器上的认证、命令状态、超时、SFTP 与工作空间 | 环境变量指定的 SSH 目标 |

本机集成测试使用 `golang.org/x/crypto/ssh` 启动真实的服务端进程内实例，并配合
`github.com/pkg/sftp` 的服务端实现，因此主机密钥校验、密码/私钥/Agent 认证、
SFTP 上传下载与远程命令执行都经过真实协议交互，但不访问外网。

SSH Agent 用例在测试工作空间中创建 Unix 套接字并提供 Agent 协议服务；平台不支持
Unix 套接字时该用例输出 `WARN [SKIP]` 并跳过，其余用例仍会执行。

所有 SSH 测试都禁止关闭主机密钥校验，禁止自动接受未知主机密钥。

## Docker 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/environment/` | 环境类型、版本号递增、构建状态迁移、镜像引用与 ID 校验 | 无 |
| `tests/unit/image/` | 构建上下文确定性哈希、Dockerfile 静态校验、语言模板、构建配置哈希 | 无 |
| `tests/unit/container/` | 容器状态迁移、detached 语义、生命周期策略、缓存卷命名、创建参数安全边界、JSON 解析 | 无 |
| `tests/integration/local/docker/` | 预检错误分类、命令构造与转义、幂等删除、状态不确定分类、清单读写 | 无（使用 SSH 执行替身） |
| `tests/integration/local/environment/` | 版本文件不可覆盖、清单落盘一致性、构建上下文上传与清理 | 无（远程路径映射到本机目录） |
| `tests/integration/remote/docker/` | 真实远程预检、镜像构建、容器创建/启动/执行/停止/删除 | 环境变量指定的 SSH 目标与真实 Docker |

本机 Docker 测试使用可控的 SSH 执行替身：替身记录收到的远程命令并返回预设输出，
因此命令构造、参数转义、结果解析与错误分类都经过真实代码路径，但不需要 Docker Engine。
本机环境测试把远程绝对路径映射到测试工作空间中的目录，因此文件写入、原子替换与
"历史版本不可覆盖"都在真实文件系统上验证。

真实远程 Docker 测试只使用带随机后缀的测试镜像标签、容器名称与远程目录，并只清理
自己创建并已核验所有权的测试容器；不会执行全局 prune，也不会删除共享镜像或缓存卷。

## 环境变量

### 真实 Pi

| 变量 | 用途 | 默认值 |
|---|---|---|
| `AGENTFLOW_TEST_PI_MODEL` | 覆盖真实 Pi 测试使用的模型 | `group/free` |
| `AGENTFLOW_TEST_PI_EXECUTABLE` | 指定 Pi 可执行文件路径 | PATH 中的 `pi` |

本机 PATH 中没有 `pi` 时，真实 Pi 用例输出 `WARN [SKIP]` 并跳过；Pi Runner 的单元
测试位于 `tests/unit/agent/pi/`，使用替身，始终执行。

默认模型 `group/free` 是特意选定的非标准标识，避免测试意外消耗付费额度。每次真实
调用都显式传入模型，不会回退到 Pi CLI 配置中的默认模型。

### 真实 SSH

| 变量 | 用途 | 必填 |
|---|---|---|
| `AGENTFLOW_TEST_SSH_HOST` | SSH 服务器地址 | 是 |
| `AGENTFLOW_TEST_SSH_USER` | 登录用户名 | 是 |
| `AGENTFLOW_TEST_SSH_PORT` | SSH 端口 | 否，默认 `22` |
| `AGENTFLOW_TEST_SSH_KEY_PATH` | 私钥路径 | 否，为空时使用 SSH Agent |
| `AGENTFLOW_TEST_SSH_KNOWN_HOSTS` | 只读 known_hosts 文件 | 否 |

示例：

```bash
AGENTFLOW_TEST_SSH_HOST=10.0.0.10 \
AGENTFLOW_TEST_SSH_USER=deploy \
AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 \
go test -count=1 -v ./tests/integration/ssh/...
```

必需目标变量缺失时输出 `WARN [SKIP]` 并说明缺少哪一项；目标配置完整但连接、认证或
主机密钥校验失败时必须让测试失败。真实 SSH 测试始终执行主机密钥校验，不会自动接受
未知主机密钥。远程文件操作只发生在随机生成的远程临时目录内。

### 真实远程 Docker

| 变量 | 用途 | 必填 |
|---|---|---|
| `AGENTFLOW_TEST_REMOTE_DOCKER` | 显式启用真实远程 Docker 测试，必须为 `1` | 是 |
| `AGENTFLOW_TEST_SSH_HOST` | SSH 服务器地址 | 是 |
| `AGENTFLOW_TEST_SSH_USER` | 登录用户名 | 是 |
| `AGENTFLOW_TEST_SSH_PORT` | SSH 端口 | 否，默认 `22` |
| `AGENTFLOW_TEST_SSH_KEY_PATH` | 私钥路径 | 否，为空时使用 SSH Agent |
| `AGENTFLOW_TEST_SSH_KNOWN_HOSTS` | 只读 known_hosts 文件 | 否 |

示例：

```bash
AGENTFLOW_TEST_REMOTE_DOCKER=1 AGENTFLOW_TEST_SSH_HOST=10.0.0.10 AGENTFLOW_TEST_SSH_USER=deploy AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 go test -count=1 -v ./tests/integration/remote/docker/...
```

开关未设置时输出 `WARN [SKIP]`，普通测试不会修改真实远程 Docker；开关已启用但 SSH
目标缺失时说明缺少的变量并跳过；目标完整但认证失败、Docker 不可用或断言不成立时
必须让测试失败。

## 编写测试的约定

- 使用 `testkit.New(t)` 创建工作空间，用 `testkit.Dir(t)` 取代 `t.TempDir()`。
- 不要用 `t.Setenv` 修改全局环境来切换数据目录；通过结构体、构造参数或子进程环境
  传入路径。
- 每个用例的数据库、Session、输出文件路径必须来自自己的工作空间。
- 需要真实外部依赖时，由测试自身检查依赖并在缺失时输出警告后跳过。
- 成功判定必须基于具体业务结果（退出码、数据库记录、文件内容、状态字段），不能只
  依赖日志文本。
- Docker 相关测试必须显式启用 `AGENTFLOW_TEST_REMOTE_DOCKER=1` 才允许修改真实远程
  Docker；本机测试一律使用执行替身或路径映射，不依赖本机是否安装 Docker。
- 测试结束不得删除测试工作空间与产物；真实远程测试的远程测试目录同样保留。

## 远程执行测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/source/` | 仓库地址校验、目标目录冲突、Git 缺失分类、参数转义、基础分支识别优先级、任务分支初始化、取消与超时、结果不确定分类 | 无（使用容器执行替身） |
| `tests/unit/agent/` | 容器内 Pi 参数构造、调用标识唯一性、标准输入、退出状态分类、Session 缺失/残缺、导出失败保留 JSONL、容器所有权核验 | 无（使用容器执行替身） |
| `tests/integration/local/source/` | 真实 Git 克隆、基础分支识别、`agent/<task-id>` 分支、远程任务分支检出、目标目录不被删除、并发目录隔离 | 本机 `git` |
| `tests/integration/local/session/` | 容器内 → 宿主机 → 本机三段式传输、字节校验、传输中断保留部分文件、导出失败保留 JSONL、并发 Session 隔离、容器删除后本机 Session 可读 | 无（路径映射到测试工作空间） |
| `tests/integration/remote/agent/` | 真实远程容器内 Git Clone、容器内 Pi 调用、独立 Session、本机持久化、容器删除后 Session 仍可读 | 环境变量指定的 SSH 目标、真实 Docker 与容器内真实 Pi CLI |

本机 Git 缺失时，`tests/integration/local/source/` 的用例输出 `WARN [SKIP]` 并跳过，
其余用例仍会执行。

## 代码保存测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/source/codesaver/` | 容器登记与实际 ID 不一致、detached 容器、任务分支不匹配、非 Git 工作区、无变更、既有冲突、提交身份缺失、提交信息与作者参数传递、提交成功与 SHA 读取、提交异常但 HEAD 已变化、既有暂存内容不被重置、只推送任务分支、禁止强制推送、远程缺失、内嵌凭据拒绝、认证失败、非快进冲突、推送超时但远程已收到、远程与本地不一致、无法核验远程、合并默认关闭、推送失败禁止合并、基础分支识别失败、合并冲突、主分支推送失败、合并结果不确定、记录先于远程操作落盘、并发隔离、取消不影响其他任务 | 无（使用容器执行替身） |
| `tests/unit/store/` | `code_saves` 写入与更新、状态字段约束（成功必须带 SHA）、未请求合并不能记录合并结果、按任务查询、并发写入独立 | 无 |
| `tests/integration/local/codesaver/` | 真实 Git 提交、任务分支真实 Push 到 bare 仓库、远程引用核验、默认不合并、显式合并后基础分支包含任务提交、推送失败不误报成功、合并冲突保留现场、无变更不制造空提交、并发仓库隔离 | 本机 `git` |
| `tests/integration/remote/codesaver/` | 真实远程容器内提交、推送与合并，远程任务分支与基础分支 commit 核验 | 环境变量指定的 SSH 目标、真实 Docker 与容器内 Git |

本机 Git 缺失时，`tests/integration/local/codesaver/` 的用例输出 `WARN [SKIP]` 并跳过，
`tests/unit/source/codesaver/` 与 `tests/unit/store/` 的用例仍然执行。

### 代码保存测试的环境变量

| 变量 | 用途 | 必填 |
|---|---|---|
| `AGENTFLOW_TEST_REMOTE_DOCKER` | 显式启用真实远程容器测试，必须为 `1` | 是 |
| `AGENTFLOW_TEST_SSH_HOST` | SSH 服务器地址 | 是 |
| `AGENTFLOW_TEST_SSH_USER` | 登录用户名 | 是 |
| `AGENTFLOW_TEST_SSH_PORT` | SSH 端口 | 否，默认 `22` |
| `AGENTFLOW_TEST_SSH_KEY_PATH` | 私钥路径 | 否，为空时使用 SSH Agent |
| `AGENTFLOW_TEST_SSH_KNOWN_HOSTS` | 只读 known_hosts 文件 | 否 |
| `AGENTFLOW_TEST_REMOTE_GIT_IMAGE` | 远程代码保存测试使用的容器镜像（需包含 `git`） | 是 |

```bash
# 本机 Git 集成测试（不依赖公网）
go test -count=1 -v ./tests/integration/local/codesaver/...

# 真实远程容器代码保存测试（必须显式启用）
AGENTFLOW_TEST_REMOTE_DOCKER=1 AGENTFLOW_TEST_SSH_HOST=10.0.0.10 AGENTFLOW_TEST_SSH_USER=deploy AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 AGENTFLOW_TEST_REMOTE_GIT_IMAGE=alpine/git:2.45.2 go test -count=1 -v ./tests/integration/remote/codesaver/...
```

开关未设置时输出 `WARN [SKIP]`；开关已启用但 SSH 目标或镜像变量缺失时说明缺少的变量
并跳过；目标完整但连接、认证、主机密钥校验失败或断言不成立时必须让测试失败。

远程代码保存测试只在本次测试创建并已核验所有权的容器内操作，只使用带随机后缀的容器
名称与容器内临时目录，不执行全局 prune，也不在用户的真实主分支上执行合并。

### 远程 Agent 测试镜像

`tests/integration/remote/agent/Dockerfile` 定义测试用任务环境镜像，基础镜像与 Pi CLI
版本都固定到具体版本。构建方式（在仓库根目录执行，可在远程机器上执行）：

```bash
docker build -t agentflow-test-agent:1.0.0 -f tests/integration/remote/agent/Dockerfile .
```

### 远程 Agent 测试的环境变量

| 变量 | 用途 | 必填 |
|---|---|---|
| `AGENTFLOW_TEST_REMOTE_DOCKER` | 显式启用远程 Docker 测试，必须为 `1` | 是 |
| `AGENTFLOW_TEST_SSH_HOST` | SSH 服务器地址 | 是 |
| `AGENTFLOW_TEST_SSH_USER` | 登录用户名 | 是 |
| `AGENTFLOW_TEST_SSH_PORT` | SSH 端口 | 否，默认 `22` |
| `AGENTFLOW_TEST_SSH_KEY_PATH` | 私钥路径 | 否，为空时使用 SSH Agent |
| `AGENTFLOW_TEST_SSH_KNOWN_HOSTS` | 只读 known_hosts 文件 | 否 |
| `AGENTFLOW_TEST_PI_MODEL` | 覆盖真实模型标识 | 否，默认 `group/free` |
| `AGENTFLOW_TEST_REMOTE_AGENT_IMAGE` | 覆盖测试镜像引用 | 否，默认 `agentflow-test-agent:1.0.0` |
| `AGENTFLOW_TEST_REMOTE_GIT_REPOSITORY` | 测试使用的仓库地址（必须能从远程机器访问） | 是 |

示例（Git Bash / Linux / macOS）：

```bash
AGENTFLOW_TEST_REMOTE_DOCKER=1 AGENTFLOW_TEST_SSH_HOST=10.0.0.10 AGENTFLOW_TEST_SSH_USER=deploy AGENTFLOW_TEST_SSH_KEY_PATH=$HOME/.ssh/id_ed25519 AGENTFLOW_TEST_REMOTE_GIT_REPOSITORY=https://github.com/example/repo.git go test -count=1 -v ./tests/integration/remote/agent/...
```

示例（PowerShell）：

```powershell
$env:AGENTFLOW_TEST_REMOTE_DOCKER="1"
$env:AGENTFLOW_TEST_SSH_HOST="10.0.0.10"
$env:AGENTFLOW_TEST_SSH_USER="deploy"
$env:AGENTFLOW_TEST_REMOTE_GIT_REPOSITORY="https://github.com/example/repo.git"
go test -count=1 -v ./tests/integration/remote/agent/...
```

容器内 Pi CLI 是否可用由测试在容器内实际执行 `pi --version` 确认，不依据本机是否安装 Pi。
远程测试只删除本次测试创建并已核验所有权的容器，不执行全局 prune。

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
迁移结果。
