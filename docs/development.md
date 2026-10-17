# 开发指南

本文件描述本地开发环境、代码风格、测试流程与提交约定。目录职责与依赖方向见 [architecture.md](architecture.md)。

## 环境要求

- Go：使用当前受支持的稳定版本，版本要求以 `go.mod` 声明为准。
- Git：用于工作区隔离与版本管理，Makefile 会用 `git rev-parse` 注入提交号。
- Node.js + pnpm：仅在开发 Web UI 时需要（当前未引入）。
- Pi CLI：已安装并完成模型 Provider 配置（当前版本不调用 Pi）。

## 初始化

```bash
git clone <repo>
cd agentflow
go mod tidy
```

## 常用命令

```bash
make fmt          # gofmt -w cmd internal
make fmt-check    # 列出未格式化的文件
make vet          # go vet ./...
make test         # go test ./...
make build        # 构建 bin/agentflow
make run          # go run ./cmd/agentflow
make check        # fmt + vet + test + build
```

不依赖 Make 时可以直接使用底层命令：

```bash
gofmt -w cmd internal
go vet ./...
go test ./...
go build -o bin/agentflow ./cmd/agentflow
```

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
```

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
4. 涉及持久化时，同步补充迁移脚本与仓储实现。
5. 改动完成后执行 `gofmt` 与 `go vet`，并自查 `git diff`。

## 新增角色类型或状态

- 新增角色类型：在 `internal/domain/role.go` 增加 `RoleType` 常量与 `Validate` 分支，并在 `internal/config/config.go` 补充对应的工具策略模式映射与示例配置。
- 新增状态：在对应实体的状态机中声明合法迁移与终态，并确认重试、取消路径可达。
- 新增状态后，必须检查持久化层对旧数据的兼容性，必要时提供迁移。

## 测试流程

- 测试使用 Go 标准库 `testing`，与源码同包或同目录的 `_test.go` 文件。
- 测试必须使用临时目录与临时配置，不得读写用户真实配置、仓库或数据库。
- 测试不得依赖真实模型凭据，也不得调用真实 Pi。
- 未经明确授权不运行测试、不创建测试代码与临时验证脚本，也不通过“测试后删除”的方式绕过该限制。
- 数据库 schema 变更必须同时提交 `migrations/` 下的新迁移脚本。

## 与 Pi 交互的注意事项

- Pi 作为外部进程运行，任何交互都必须假设对方可能异常退出。
- 协议字段以官方文档与当前安装版本为准，不凭记忆猜测。
- 新增交互路径时同时实现成功路径、失败路径与取消路径。

## 提交约定

- 提交信息使用简体中文，一行标题说明改动的目的。
- 一次提交只做一件事；格式化产生的无关改动不混入功能提交。
- 不提交 `bin/`、`.agentflow/` 与本地配置文件。
- 不提交任何真实 API Key、访问凭据或本机绝对路径。

## 文档

- 代码编辑期间不同步文档。
- 需要更新文档时，只描述已实现的能力，不提前声明尚未落地的命令或接口。
- 命令或输出目录发生变化时，README 必须同步更新。
