# CLI 参考

本文件是 `agentflow` 命令的索引与边界说明。接口契约见 [http-api.md](http-api.md)，SSH 与执行节点的
认证语义见 [ssh-hosts.md](ssh-hosts.md)，Docker 基线见 [docker-compatibility.md](docker-compatibility.md)。

## 1. 通用约定

- 全局参数：`--help`、`--version`。`agentflow version` 输出构建信息。
- `host`、`project`、`task` 等命令组接受 `--config`，未指定时读取 `configs/agentflow.yaml`；
  `config validate` 在该文件不存在时直接报错，不回退到内置默认值。
- 命令层只做参数解析、结果输出与退出码转换，不包含校验规则，也不收集任何凭据：SSH 认证只使用节点
  配置中的本机私钥文件。
- 任务执行命令是 HTTP 客户端：它们调用 `agentflow server` 的接口并轮询真实进度，不在 CLI 进程内装配
  SSH、容器、Git 或 Pi 能力；服务不可达时直接失败并提示启动服务，不会退回本机执行。

## 2. 基础命令

| 命令 | 说明 |
|---|---|
| `agentflow config validate` | 校验配置文件的结构与取值 |
| `agentflow version` | 输出构建信息 |
| `agentflow server` | 启动 AgentFlow 服务（HTTP API、内嵌 Web 页面与后台服务） |

## 3. host —— 执行节点

| 命令 | 说明 |
|---|---|
| `host add` | 新增执行节点配置 |
| `host list` / `host show <id>` | 列出与查看节点 |
| `host update <id>` | 修改连接参数或认证方式 |
| `host set-default <id>` | 设为默认执行节点 |
| `host remove <id>` | 删除节点配置 |
| `host probe <id>` | 探测 TCP 与 SSH 协议可达性（不做身份认证） |
| `host trust <id>` | 核验并信任主机密钥，写入节点记录 |
| `host test <id>` | 测试完整 SSH 身份认证连接 |
| `host workspace <id>` | 解析并检查远程工作空间（`--create` 显式创建） |
| `host transfer upload|download <id> …` | 开发者诊断用 SFTP 单文件传输 |
| `host validate <id>` | 同步执行一次环境预检，非零退出码表示未通过 |

各命令的认证语义与主机密钥规则见 [ssh-hosts.md](ssh-hosts.md)。

## 4. project —— 项目登记

| 命令 | 说明 |
|---|---|
| `project add` / `project update` | 新增与整体更新项目配置（仓库、基础分支、执行环境） |
| `project list` / `project show <id>` | 列出与查看项目 |
| `project remove <id>` | 逻辑删除项目 |

## 5. task —— 任务

| 命令 | 说明 |
|---|---|
| `task create` | 校验配置并创建任务 |
| `task start <task-id>` | 一次启动：准备环境 → 初始化工作区 → 生成计划 → 启动工作流 |
| `task plan <task-id>` | 在任务容器内生成并持久化计划 |
| `task run <task-id>` | 启动一次任务级工作流并轮询进度 |
| `task get <task-id>` / `task list` | 查询任务状态、步骤、尝试、验证与代码保存结果 |
| `task cancel <task-id>` | 请求取消任务并保留现场 |
| `task retry <task-id>` / `task retry-status <task-id>` | 显式重试与重试资格查询 |
| `task resume-status <task-id>` | 查询任务能否继续推进及下一步操作 |
| `task reopen-step <task-id> <step-id>` | 为步骤开启新的执行周期（重置修复预算） |

- `task start` 由服务端依据数据库事实决定从哪个阶段继续：已有受管理且运行中的容器时不会创建第二个
  容器，计划已持久化时不会重新生成。响应字段与失败语义见 [http-api.md](http-api.md)。
- `task plan` 不创建容器、不自动重试、不覆盖已有计划。
- `task run` 不生成计划：任务没有已持久化的步骤时接口返回状态冲突。

## 6. execenv —— 统一执行环境

| 命令 | 说明 |
|---|---|
| `execenv prepare` | 按机器与环境定义准备执行环境（校验 → 镜像 → 创建容器 → 核验可用性） |
| `execenv prepare-task <task-id>` | 依据任务记录准备执行环境并写回容器关联 |
| `execenv exec --container <id> -- <命令>` | 在已有执行环境内执行一条命令 |
| `execenv release --container <id>` | 按处置方式释放执行环境资源 |

处置方式只有 keep / delete / detach：已 detached 的容器不会被自动启动、停止、删除或重新接管。

## 7. code 与 source —— 代码获取与保存

| 命令 | 说明 |
|---|---|
| `source fetch <container>` | 在任务容器内克隆仓库并创建 `agent/<task-id>` 分支 |
| `code init` | 在任务容器内获取代码并创建任务分支（尝试级入口） |
| `code save <container>` | 提交并推送任务分支，按配置决定是否合并基础分支 |
| `code push <container>` / `code merge <container>` | 只推送任务分支 / 把已推送的分支合并到基础分支 |
| `code status` | 展示任务的提交、推送与合并状态 |
| `code verify` / `code fetch-verify` | 只读核验一次代码保存 / 代码获取记录对应的真实仓库状态 |

- 保存与获取都只操作容器内的任务工作区，分支名固定为 `agent/<task-id>`（由 `domain.TaskBranchName`
  生成与校验）。
- 合并默认关闭（`merge_after_push = false`），开启代码保存本身不代表同意合并主分支。
- `code verify` 与 `code fetch-verify` 只读：不提交、不推送、不切换分支，只更新恢复结论。

## 8. docker 与 agent —— 诊断入口

| 命令 | 说明 |
|---|---|
| `docker preflight <machine>` | 检查远程 Docker CLI、Engine 与使用权限 |
| `docker init <machine>` | 初始化远程 AgentFlow 环境目录 |
| `docker env list/show/history/build/log/edit` | 管理远程环境定义与不可覆盖的配置版本 |
| `docker container create/list/show/logs/exec/start/stop/delete/detach` | 管理由 AgentFlow 创建的远程容器 |
| `docker container lifecycle <container-id>` / `restart` | 按任务结果应用容器生命周期策略 / 重新开始失败任务 |
| `agent check` | 检查 Pi 安装与非交互式执行所需的参数（不发起模型调用） |
| `agent run <container>` | 在任务容器内执行一次 Pi 调用并把 Session 保存到本机 |

这些命令用于诊断与运维，不是业务执行入口：业务链路只能经由 `agentflow server` 中的编排器推进。

`agent run` 也必须遵守角色工具策略：`--role-type`（必填）决定系统能力上限，`--permissions`、
`--allow-tools`、`--deny-tools`（逗号分隔）给出本次策略，取值由 `domain` 统一校验。省略
`--permissions` 表示以 `--no-tools` 调用（不加载任何工具）；越出能力上限或无可用工具时不会回退到
Pi 的默认工具列表，命令直接失败并说明原因。
