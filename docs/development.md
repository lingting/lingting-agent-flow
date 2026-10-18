# 开发指南

本文件描述本地开发环境、代码风格、测试流程与提交约定。目录职责与依赖方向见 [architecture.md](architecture.md)。

## 环境要求

- Go：使用当前受支持的稳定版本，版本要求以 `go.mod` 声明为准。
- Git：用于工作区隔离与版本管理，Makefile 会用 `git rev-parse` 注入提交号。
- Node.js 22 + pnpm 11：仅在构建或开发 Web UI 时需要；`make release` 会用它完成前端生产构建，
  产物随后由 Go 通过 `//go:embed` 嵌入可执行文件。
- Pi CLI：`agent check` 与 `agent test` 需要 PATH 中存在 `pi`；`agent test` 还会使用角色
  配置中的模型，因此需要完成对应 Provider 的凭据配置。其他命令不依赖 Pi。

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
make run          # go run ./cmd/agentflow web serve
make web-dev      # 前端开发服务器（另开终端运行 make web-serve）
make web-serve    # 本地 HTTP 服务（API + 内嵌页面）
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
交互；SSH Agent 用例依赖 Unix 套接字，平台不支持时单独跳过。

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

`host` 命令组同样接受 `--config`，未指定时读取 `configs/agentflow.yaml`。

命令边界要分清：

- `host probe` 只验证 TCP 可达性与 SSH 协议标识，**不执行身份认证**。
- `host trust` 只探测主机密钥并展示指纹，写入信任记录必须显式给出已核验的 `--fingerprint`
  与非交互场景的 `--yes`。
- `host test`、`host workspace`、`host transfer` 执行完整的主机密钥校验与身份认证，是唯一
  可以执行远程命令与文件传输的路径。凭据通过 `--password-stdin`、`--passphrase-stdin` 或
  交互式 `--ask-pass` 提供，非交互环境缺少凭据时立即失败而不是等待输入。

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
- 真实 Pi 测试默认使用模型 `group/free`，可用 `AGENTFLOW_TEST_PI_MODEL` 覆盖；本机没有
  `pi` 时输出 `WARN [SKIP]` 并跳过。真实 SSH 测试通过 `AGENTFLOW_TEST_SSH_*` 获取目标，
  缺少必需目标时跳过，配置完整但连接、认证或主机密钥校验失败时必须失败。
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

## 与 SSH 交互的注意事项

- 主机密钥校验不得关闭：`NewDialer` 必须拿到 `HostKeyStore`，未知或不匹配一律拒绝。
- 凭据只在内存中传递，用完调用 `Credential.Clear()`；不得把密码、口令或私钥内容写入
  错误信息、日志、数据库或命令行参数。
- 远程命令必须用 `ShellCommand` 构造（程序名 + 参数数组 + 逐参数转义），禁止拼接用户输入。
- 取消与超时不仅要停止本端等待，还要关闭远程会话通道并释放本端 goroutine；无法确认远程
  进程已终止时，必须在结果中如实标记，不能声称命令已终止。
- SFTP 上传与下载都使用同目录临时文件 + 校验 + 重命名，失败时清理临时文件但不得留下最终
  文件；覆盖需要调用方显式声明。
- 远程路径只使用 POSIX 规则（`NormalizeRemotePath`），不得把本机 `filepath` 语义套用到远程
  路径，也不得用字符串前缀判断目录包含关系。
## 提交约定

- 提交信息使用简体中文，一行标题说明改动的目的。
- 一次提交只做一件事；格式化产生的无关改动不混入功能提交。
- 不提交 `bin/`、`.agentflow/` 与本地配置文件。
- 不提交任何真实 API Key、访问凭据或本机绝对路径。

## 文档

- 代码编辑期间不同步文档。
- 需要更新文档时，只描述已实现的能力，不提前声明尚未落地的命令或接口。
- 命令或输出目录发生变化时，README 必须同步更新。
