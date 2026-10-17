# AgentFlow 测试

本目录是 AgentFlow 唯一的测试代码位置。生产源码目录（`internal/`、`cmd/`）不再存放
测试文件，新增测试一律放在本目录下。

## 目录结构

```text
tests/
├── internal/
│   ├── testkit/        # 测试工作空间与通用辅助组件
│   └── fakepi/         # 以测试二进制充当 Pi CLI 的可控替身
├── unit/               # 快速单元测试，不依赖真实 Pi、SSH 或网络
│   ├── agent/          # Agent 事件与结果类型
│   │   └── pi/         # Pi Runner 单元测试（使用 fakepi 替身）
│   ├── app/            # 应用服务层
│   ├── config/         # 配置加载与校验
│   ├── domain/         # 领域模型与状态迁移
│   ├── process/        # 子进程监管
│   ├── ssh/            # SSH 主机密钥、认证配置、路径与结果状态
│   ├── store/          # SQLite 仓储与数据库约束
│   └── testkit/        # testkit 自身的测试
├── integration/
│   ├── local/          # 本机集成测试，不依赖外部服务
│   │   ├── database/   # 持久化工作流全链路
│   │   ├── ssh/        # 本地模拟 SSH/SFTP 服务端
│   │   └── sshprobe/   # 本机 TCP 探测
│   ├── pi/             # 需要本机真实 Pi CLI
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

# 真实 Pi 集成测试
go test -count=1 -v ./tests/integration/pi/...

# 真实 SSH 集成测试
go test -count=1 -v ./tests/integration/ssh/...

# CLI 端到端测试
go test -count=1 -v ./tests/e2e/...

# 完整测试
go test -count=1 -v -p 8 -parallel 8 ./tests/...

# 竞态检测
go test -race -count=1 ./tests/unit/...
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

## 编写测试的约定

- 使用 `testkit.New(t)` 创建工作空间，用 `testkit.Dir(t)` 取代 `t.TempDir()`。
- 不要用 `t.Setenv` 修改全局环境来切换数据目录；通过结构体、构造参数或子进程环境
  传入路径。
- 每个用例的数据库、Session、输出文件路径必须来自自己的工作空间。
- 需要真实外部依赖时，由测试自身检查依赖并在缺失时输出警告后跳过。
- 成功判定必须基于具体业务结果（退出码、数据库记录、文件内容、状态字段），不能只
  依赖日志文本。
