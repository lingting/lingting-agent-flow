# AgentFlow

AgentFlow 是一个使用 Go 编写的本地优先 AI 编码工作流编排器。

它把编码需求管理为持久化任务，并通过相互隔离的 Pi Agent 会话，协同完成规划、实现、验证与自动修复。

## 目标

- 可自定义的角色与模型
- 持久化任务队列，并限制同时执行的任务数量
- 结构化、分步骤的开发计划
- 每次执行尝试使用独立的 Agent 会话
- 以验证结果驱动的修复循环
- 相互隔离的 Git Worktree
- 可恢复的任务状态
- 以 CLI 为主要入口，并提供可选的本地 Web UI

## 核心工作流

```text
需求队列
   |
   v
 调度器
   |
   v
 规划角色
   |
   v
结构化计划
   |
   v
 开发角色
   |
   v
 验证角色
   |
   +---- 通过 ----> 下一步骤 / 任务完成
   |
   +---- 失败 ----> 保存失败证据
                        |
                        v
                新的开发会话
```

## 架构

AgentFlow 负责任务编排与工作流状态，Pi 只提供 Agent 执行运行时。

```text
CLI / Web UI
     |
应用服务
     |
调度器 / 工作流引擎
     |
Pi RPC / 进程监管
     |
Git Worktree / 测试命令
     |
SQLite
```

分层约定：

- `internal/domain` 只定义实体、状态与业务规则，不依赖任何基础设施。
- `internal/app` 提供 CLI 与 HTTP 共用的应用服务。
- `internal/scheduler` 与 `internal/workflow` 负责并发控制与状态迁移。
- `internal/httpapi` 只做 HTTP 请求适配与 SSE 事件推送。

## 技术栈

- Go
- SQLite
- Pi RPC
- Git Worktree
- React
- TypeScript
- Vite

## 环境要求

- 受支持的 Go 工具链
- Git
- Node.js 与 pnpm（仅开发 Web UI 时需要）
- 已安装并完成配置的 Pi CLI
- 可访问所选用 Pi 模型 Provider 的凭据

## 项目状态

AgentFlow 处于初始开发阶段。

第一版目标是基于 Pi 的本地任务编排。其他 Agent 后端、远程执行与团队协作不在初始范围内。

方案中列出的 CLI 命令与 HTTP 接口目前尚未实现，文档只会在对应能力落地后才声明其可用。

## 开发

开发环境、目录结构与可用命令见 [DEVELOP.md](DEVELOP.md) 与 [WORKSPACE.md](WORKSPACE.md)。

## 安全

AgentFlow 会执行外部程序，并可能修改项目文件。

请使用相互隔离的工作区、配置合理的 Agent 权限，并审查任何可能修改或删除数据的命令。

本地 HTTP 服务默认只监听 `127.0.0.1`，且不存储模型凭据。

## 许可证

许可证将在首次公开发布前确定。
