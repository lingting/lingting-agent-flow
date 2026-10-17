# DEVELOP.md

本文件描述 AgentFlow 的本地开发环境、常用命令与改动流程。工作区边界与目录职责见 [WORKSPACE.md](WORKSPACE.md)。

## 环境要求

- Go：使用当前受支持的稳定版本，版本要求以 `go.mod` 声明为准。
- Git：用于工作区隔离与版本管理。
- Node.js + pnpm：仅在开发 Web UI 时需要。
- Pi CLI：已安装并完成模型 Provider 配置。

## 初始化

```bash
git clone <repo>
cd agentflow

# Go 依赖（当前仅标准库）
go build ./...

# Web UI 依赖（需要时）
pnpm --dir web/ui install
```

## 常用命令

```bash
gofmt -w internal cmd     # 格式化本次改动的 Go 文件
gofmt -l internal cmd     # 仅检查格式
go vet ./...              # 静态检查
go build ./...            # 编译全部包
```

约定：

- 格式化只作用于本次改动的文件或目录，避免产生无关 diff。
- 未经明确授权不运行测试、不创建测试代码与临时验证脚本。
- 数据库 schema 变更必须同时提交 `migrations/` 下的新迁移脚本。

## 改动流程

1. 确认改动属于哪一层，按 [WORKSPACE.md](WORKSPACE.md) 的依赖方向选择位置。
2. 领域规则变化优先修改 `internal/domain`，其余层只做编排与适配。
3. 涉及状态迁移时，检查是否仍通过 `Transition` 校验，并确认迁移路径在状态机中已声明。
4. 涉及持久化时，同步补充迁移脚本与仓储实现。
5. 改动完成后执行 `gofmt` 与 `go vet`，并自查 `git diff`。

## 新增角色类型或状态

- 新增角色类型：在 `internal/domain/role.go` 增加 `RoleType` 常量、`Validate` 分支与默认工具策略。
- 新增状态：在对应实体的状态机中声明合法迁移与终态，并确认重试、取消路径可达。
- 新增状态后，必须检查持久化层对旧数据的兼容性，必要时提供迁移。

## 与 Pi 交互的注意事项

- Pi 作为外部进程运行，任何交互都必须假设对方可能异常退出。
- 协议字段以官方文档与当前安装版本为准，不凭记忆猜测。
- 新增交互路径时同时实现成功路径、失败路径与取消路径。

## 文档

- 代码编辑期间不同步文档。
- 需要更新文档时，只描述已实现的能力，不提前声明尚未落地的命令或接口。
