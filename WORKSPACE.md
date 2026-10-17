# WORKSPACE.md

本文件描述仓库的工作区地图与边界约定，用于判断“新代码应该放在哪里”以及“哪些依赖不允许出现”。开发环境与命令见 [DEVELOP.md](DEVELOP.md)。

## 目录地图

```text
cmd/agentflow/            CLI 入口，只做依赖装配与命令注册
internal/app/             应用服务，CLI 与 HTTP 的共同调用入口
internal/domain/          核心实体、状态与业务规则
internal/config/          配置结构与加载
internal/cli/             Cobra 命令实现
internal/scheduler/       任务领取、并发控制、Worker 与恢复
internal/workflow/        Plan / Develop / Verify / Retry 状态迁移与提示词
internal/agent/           统一 Agent 接口
internal/agent/pi/        Pi RPC 适配器（进程、RPC、消息解析）
internal/process/         外部进程生命周期与优雅退出
internal/gitworktree/     任务工作区创建、检查与清理
internal/storage/sqlite/  数据库连接、迁移与仓储实现
internal/events/          内部事件定义与事件总线
internal/httpapi/         HTTP 路由、处理器与 SSE
web/ui/                   React + TypeScript 前端
configs/                  示例配置
migrations/               版本化 SQL 迁移
docs/                     架构与开发文档
tests/e2e/                端到端测试
```

## 依赖方向

```text
cmd
 └── cli / httpapi
      └── app
           ├── scheduler
           ├── workflow
           ├── storage
           └── events
                └── agent / process / gitworktree / config
                     └── domain
```

约束：

- `domain` 处于最底层，只依赖 Go 标准库，禁止 import 本项目其他内部包。
- `domain` 不得感知数据库、HTTP、JSONL RPC、子进程与文件系统。
- `app` 之上的层不得直接访问 SQLite；所有持久化经由仓储接口。
- `scheduler` 与 `workflow` 不得直接创建 Pi 进程，只能通过 `agent` 接口。
- `httpapi` 与 `cli` 不得包含状态迁移逻辑，只调用 `app`。
- `web/ui` 不得复制工作流规则，只消费 HTTP API 与 SSE。

## 文件职责与规模

- 每个文件保持单一、可描述的主要职责；职责出现分化时按职责拆分，而不是继续追加。
- 同一职责的文件不拆分到多个文件；不同职责的代码不堆在同一文件。
- 不使用固定行数作为拆分标准，以“是否妨碍定位、修改是否牵动无关代码”为准。
- 文件超过 120 字符的行需结构化换行；连续重复的方法调用从第一次重复开始逐段换行。

## 领域模型边界

| 概念 | 含义 | 归属 |
|---|---|---|
| Role | 用户自定义角色，含类型、模型、提示词与工具策略 | domain |
| Requirement | 用户提交的原始需求 | domain |
| Task | 某需求的一次完整执行，持有工作区 | domain |
| Step | 计划中的业务步骤 | domain |
| Attempt | 某个 Step 的一次执行尝试 | domain |
| Session | 一次具体的 Pi 会话 | domain |
| VerificationResult | 某次验证的结构化结果 | domain |

- Step / Attempt / Session 是三个不同层次，不得合并为一个对象。
- 重试必须产生新的 Attempt 与新的 Session，历史记录只追加不覆盖。
- 状态只能通过 `Transition`（或实体自身的结束方法）变更，不允许直接赋值绕过校验。

## 数据与状态

- 数据库是唯一真相来源，关键状态迁移必须落库后再继续执行。
- 任务领取使用事务与原子状态迁移，禁止“先查询、后更新”。
- schema 变更必须附带 `migrations/` 下的新迁移脚本，不修改已发布的迁移。
- 完整 Agent 会话历史不能作为唯一业务记录，业务结论必须写入结构化字段。

## 外部进程边界

- Pi 通过独立子进程与 JSONL RPC 交互，协议以官方文档与当前安装版本为准。
- 必须处理启动失败、非零退出码、消息解析失败、超时、取消、意外退出、输出流关闭与子进程未退出。
- 子进程的终止与回收统一由 `process` 负责，其他包不直接管理进程。

## 工作区边界

- 每个 Task 拥有独立 Git Worktree，同任务多步骤共享该工作区。
- 未经明确配置，不得自动删除包含未提交代码的工作区。

## 安全边界

- HTTP 服务默认只监听 `127.0.0.1`，不开放任意来源 CORS。
- 不存储模型 API 密钥，凭据由 Pi 及其 Provider 管理。
- 角色权限采用最小化原则，实际权限通过 Agent 配置与执行环境实施。

## 文档边界

- 文档只描述已实现的能力，不提前声明可用命令或接口。
- 代码编辑期间不同步文档，仅在明确要求时更新。
