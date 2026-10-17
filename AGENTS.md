# AGENTS.md

本文件约束在本仓库内进行开发时的行为约定。通用开发规范（语言、权限、测试授权）由全局配置提供，此处只补充 AgentFlow 项目自身的约定。

## 项目定位

AgentFlow 是本地优先的 AI Coding Workflow Orchestrator。编排器由 Go 实现，负责角色配置、任务队列、并发调度、开发步骤、验证、失败回退与执行记录；Pi 只作为 Agent 执行引擎，通过独立子进程与 JSONL RPC 通信。

## 分层与依赖方向

依赖只能自上向下，不得反向：

```text
cmd → cli / httpapi → app → scheduler / workflow → agent / process / gitworktree / storage / events
                                                                    ↘ domain
```

- `internal/domain/` 是业务语义的唯一来源，只定义实体、状态与业务规则，禁止依赖数据库、HTTP、Agent 运行时、外部进程或任何第三方包。
- `internal/app/` 是应用服务层，是 CLI 与 HTTP 的共同入口。CLI 与 Web UI 不得各自实现工作流或状态迁移。
- `internal/workflow/` 负责 Plan / Develop / Verify / Retry 的状态迁移；`internal/scheduler/` 负责任务领取、并发控制与恢复。
- `internal/httpapi/` 只做协议转换与 SSE 推送，不承载业务规则。
- `web/ui/` 只负责展示与发起操作，不实现独立工作流逻辑。

## 领域模型约定

- `Requirement`、`Task`、`Step`、`Attempt`、`Session`、`VerificationResult` 是不同层次的概念，不得合并。
- Step 是业务对象，Attempt 是一次执行尝试，Session 是一次 Pi 会话；每次重试必须创建新的 Attempt 与新的 Session，并保留历史记录。
- 状态迁移统一在 `domain` 内通过 `Transition` 方法校验，非法迁移返回 `ErrInvalidTransition`。禁止在业务层直接赋值状态字段绕过校验。
- 实体字段校验失败返回 `*ValidationError`，可通过 `errors.Is(err, ErrInvalidValue)` 判断。

## 架构硬性约束

- 数据库是唯一真相来源。关键状态迁移（任务领取、步骤开始、Session 启动、验证失败、重试、任务完成）都必须持久化，内存对象只能作为执行期缓存。
- 任务领取必须基于事务与原子状态迁移，禁止“先查询、后更新”的无保护流程。
- 不得假设 Pi 正常结束，必须处理启动失败、非零退出、RPC 解析失败、超时、取消、意外退出、输出流关闭与子进程未退出。
- 每个 Task 使用独立 Git Worktree；未经明确配置不得删除包含未提交代码的工作区。
- 角色权限采用最小化原则，实际权限通过 Agent 配置与执行环境实施，不能只靠提示词。
- HTTP 服务默认只监听 `127.0.0.1`，不存储模型凭据。
- 优先使用 Go 标准库；新增第三方依赖必须有明确用途。

## 代码约定

- 所有输出、注释与提交信息使用简体中文。
- 每个文件保持单一清晰职责；职责扩散时拆分，不按固定行数机械拆分。
- 项目未声明行宽时以每行不超过 120 字符为目标。
- 同一方法在调用链中连续出现两次及以上时，从第一次重复调用开始逐段换行。
- 完成修改后对改动的 Go 文件执行 `gofmt`，并确认 `go vet ./...` 无新增问题。

## 验证与测试

- 默认只做非测试验证：阅读代码、检查 `git diff`、`gofmt`、`go vet`、类型与引用检查。
- 未经用户明确要求，不运行测试、不创建测试文件、不编写临时验证脚本，也不通过“测试后删除”的方式绕过该限制。
- 数据库 schema 变更必须同时提供 `migrations/` 下的迁移脚本。

## 文档同步

- 代码编辑期间不修改 `README.md` 及其他文档。
- 只有用户明确要求时才同步文档。文档不得描述尚未实现的命令或能力。

## 常用命令

```bash
gofmt -l internal cmd        # 检查格式
gofmt -w internal cmd        # 格式化
go vet ./...                 # 静态检查
go build ./...               # 编译
```
