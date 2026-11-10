# AgentFlow

AgentFlow 是一个本地优先的 AI 编码工作流编排器，使用 Go 实现。

它把编码需求管理为持久化任务，在受管理的远程 Linux + Docker 执行环境中完成代码获取、计划生成、
分步开发、确定性验证、代码保存与会话归档。Pi 只作为 Agent 执行引擎，运行在任务容器内部。

## 当前阶段

**第一个可用版本尚未完成，项目仍在推进中。** 现阶段没有面向使用者的安装、配置与操作教程：能力边界、
接口形态与命令行都会继续变化，任何提前写出的使用步骤都会很快失效。

已完成的进展与后续计划以 [`.steps/`](.steps) 为准，尤其是
[`.steps/V2-final.md`](.steps/V2-final.md)（V2 最终状态）与
[`.steps/V1-final.md`](.steps/V1-final.md)（V1 最终状态）。

## 文档索引

### 工程文档（`docs/`）

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 各条执行链路的边界、状态分层与数据事实 |
| [docs/workspace.md](docs/workspace.md) | 仓库目录、包职责与依赖方向 |
| [docs/development.md](docs/development.md) | 本地环境、常用命令、改动流程与提交约定 |
| [docs/testing.md](docs/testing.md) | 测试目录、测试工作空间、真实依赖开关与结果报告 |
| [docs/ssh-hosts.md](docs/ssh-hosts.md) | SSH 连接层、执行节点环境预检与远程工作空间 |
| [docs/http-api.md](docs/http-api.md) | HTTP 接口路径、请求边界与错误语义 |
| [docs/cli.md](docs/cli.md) | CLI 命令索引与边界 |
| [docs/frontend.md](docs/frontend.md) | 前端工程约束、Web 交付与请求分发 |
| [docs/docker-compatibility.md](docs/docker-compatibility.md) | Docker 版本基线、命令清单与容器管理约束 |
| [docs/release.md](docs/release.md) | 发布流程、制品与来源证明；[docs/release/](docs/release/) 下是随附说明 |
| [docs/runbook.md](docs/runbook.md) | 运行手册：启动前提、停止语义、结果不明处置、备份恢复与故障排查 |

代码风格、硬性红线与数据库约束见根目录 [AGENTS.md](AGENTS.md)。

### 规划与阶段成果（`.steps/`）

| 路径 | 内容 |
|---|---|
| [.steps/V1-final.md](.steps/V1-final.md) | V1 最终产品状态与遗留事项 |
| [.steps/V2-final.md](.steps/V2-final.md) | V2 最终产品状态与遗留事项 |
| [.steps/V3/](.steps/V3/) | V3 阶段文档（统一测试执行器重构审阅） |

## 许可证
许可证将在首次公开发布前确定。
