# 运行手册

本文件面向**运行 AgentFlow 服务的人**（不是开发者）：说明启动前提、数据与进程边界、停止语义、
备份恢复步骤，以及在"结果不明"时应该做什么、绝对不应该做什么。

功能边界与内部实现见 [architecture.md](architecture.md)，发布制品与来源证明见
[release.md](release.md)，首次启动步骤见 [release/QUICKSTART.md](release/QUICKSTART.md)。

## 1. 启动前提

- 制品是单个可执行文件（`agentflow` / `agentflow.exe`），前端页面已通过 `//go:embed`
  编进程序，运行不需要 Node.js、`dist/` 目录或额外静态服务器。
- 需要执行远程任务时，还需要：可达的 SSH 执行节点、节点上的 Docker CLI 与 Engine 29.0.0 及以上
  （版本基线见 [docker-compatibility.md](docker-compatibility.md)）、任务容器镜像内可运行的 Pi。
  这些不是启动服务本身的依赖，只在真正执行任务时被使用。
- 同一份数据目录**只允许一个** `agentflow server` 进程，见第 3 节。
- 配置文件只保存非机密元数据：SSH 私钥路径可以写在节点记录里，但密码、口令、Token 一律不
  落盘，也不由本产品管理 Git 凭据。

## 2. 数据目录、数据库与迁移

| 项 | 说明 |
|---|---|
| 数据库路径 | 配置项 `storage.database_path`，默认 `.agentflow/agentflow.db` |
| 进程锁文件 | `<database_path>.server.lock`，与服务端进程同生命周期 |
| 工作目录 | 配置项 `workspace.worktree_root`，默认 `.agentflow/worktrees` |
| 日志级别 | 配置项 `app.log_level`（`debug` / `info` / `warn` / `error`） |

迁移行为固定如下，运行者不需要手工执行迁移：

- Schema 由 `internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本定义，脚本通过 `embed`
  编译进程序，由 Goose 在打开数据库时按版本顺序执行，已执行版本记录在 `goose_db_version` 表。
- 每个脚本在单个事务内执行，失败整体回滚，不会留下半套结构。
- 迁移只由取得 `<database_path>.server.lock` 的服务实例执行；进程启动失败时不会留下锁。
- **不会**自动删除、覆盖、清空或重建已有数据库；旧的手写 Schema 数据库不被识别，需要自行
  备份后处理。
- 当前 Schema 版本为 4（`00004_project_execution_leases`）。

数据库使用 WAL 模式（`journal_mode=WAL`、`synchronous=NORMAL`）并显式关闭外键检查
（`foreign_keys(0)`）。业务层不依赖数据库外键，引用完整性、状态转换与生命周期规则由应用层承担。
WAL 模式决定备份方式：**不允许**在服务运行中直接复制 `.db` 文件当作备份，见第 7 节。

## 3. 启动与停止

### 3.1 启动

```bash
./agentflow server                       # 读取 configs/agentflow.yaml
./agentflow server --config ./agentflow.yaml
./agentflow server --address 127.0.0.1:26381
```

默认监听 `127.0.0.1:26380`，Web 页面与 API 同源。启动成功后可以用任意方式确认监听：

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:26380/
```

### 3.2 第二个实例会立即失败

同一数据目录上再次启动服务端时，进程在获取锁的阶段就失败，退出码非零，错误信息形如：

```text
server: 已有 AgentFlow 服务端实例正在运行: 锁文件 ...agentflow.db.server.lock；请先停止已有服务端，再重新启动
```

这是**预期行为**，不是缺陷：不需要第二个服务端来执行任务，任务命令是对已有服务端的 HTTP 客户端。
锁被占用时不要删除锁文件、不要用第二个数据目录绕过它，先确认并停止占用者。

### 3.3 停止

- `agentflow server` 监听中断与终止信号（Ctrl+C / `SIGTERM`），收到后按固定顺序清理：
  取消服务上下文 → 停止 HTTP 服务 → 按注册逆序停止后台服务 → 关闭数据库。
- 停止有超时上限（`server.shutdown_timeout_seconds`，0 表示默认 10 秒）。超时会在日志中
  报告为关闭失败，此时数据库可能仍处于打开状态，必须等待进程真正退出后再做备份。
- **项目执行权**：停止时会释放"没有在途任务"的项目的执行权；仍有在途任务的项目保留执行权，
  按有效期（TTL）自然到期，不会在停止过程中提前释放给其他执行者，也不会重放任何远程操作。
- Windows 上从其他进程向服务端投递终止信号不可靠；请在控制台中用 Ctrl+C 停止。

## 4. 执行节点（SSH 与 Docker）

- 节点记录放在数据库里，通过 `agentflow host` 子命令管理；`host validate` 会做一次环境预检
  （SSH 连接、Docker 版本与 API 兼容性、Docker CLI 帮助信息），非零退出码表示未通过。
- **主机密钥校验不可关闭**：未知主机必须核验指纹后用 `host trust` 显式信任；不提供密码认证，
  私钥不得带口令。连接参数与信任规则见 [ssh-hosts.md](ssh-hosts.md)。
- 预检不解析 Dockerfile、不查询 `FROM` 镜像的 Registry 元数据、不执行 Build 或 Pull；基础镜像
  是否可用由真实环境负责。
- 容器缺失、未运行、已 detached，或节点未通过预检时，任务不会被静默退回宿主机执行，而是直接失败。
- 规划容器与任务容器不是同一个东西：规划容器服务于规划会话（`container.task_id` 为空），
  任务容器绑定到具体任务（`container.task_id` 指向该任务），两者的创建、复用与释放规则见
  [architecture.md](architecture.md)。

## 5. 执行与删除语义

### 5.1 项目执行权

- 一个项目同一时刻至多有一个执行权持有者（`owner` + 单调递增 `epoch` + `expires_at`）。
- 续租失败或执行权过期时，调度器闸门会拒绝该项目的候选任务；它只能停止**领取**，不会改写
  任务状态，也不会重放远程操作。
- 执行权不是多进程仲裁机制：多进程边界始终是数据目录上的进程锁。

### 5.2 任务停止

- `queued` 任务被取消时直接收敛为 `cancelled`，不进入 `cancelling`。
- 其他未结束状态只记录取消意图并推进到 `cancelling`，真实停止由当前执行者确认；收敛结果由既有
  工作流路径写入，外部不会代写终态。
- 取消不会删除任务、不会回滚 Git、不会清理容器。

### 5.3 需求聚合删除

需求删除是**事务内软删除**：需求与它的全部任务在同一事务内写入 `deleted_at`，任意一步失败整体
回滚。以下任一情况都会返回 409，并在响应中说明原因，此时**不会写入任何删除标记**：

- 关联任务状态不在允许删除的集合内（只允许 `queued` / `completed` / `failed` / `cancelled`）；
  数据库中出现未知状态同样拒绝；
- 任务仍有活动的执行记录（WorkflowRun 未结束）；
- 有步骤停在 `running` / `developing` / `verifying` / `recovery_required`；
- 存在**结果不明的代码保存副作用**（见第 6 节）；
- 该任务的执行权正被其他执行入口持有（包括正在启动、正在取消）；
- 任务集合在读取与写入之间发生变化。

删除**不会**做的事情，明确禁止期待它做：

- 不自动取消正在执行的任务——先显式停止任务并等它真实收敛，再删除需求；
- 不回滚 Git（不 `reset`、不 `push --force`、不删除远程分支）；
- 不停止、不删除、不清理任何容器；
- 不级联删除步骤、尝试、会话、事件、代码保存记录与容器记录，历史全部保留。

## 6. 结果不确定时的处理

代码保存的提交、推送、合并是三个独立事实，分别持久化。记录自身写入失败时会被标记为
`persist_status=unconfirmed`；远端结果无法判定时会被核验流程记为 `unverifiable`。这两种情况
都属于**外部副作用结果不明**。

### 6.1 四级分类

| 级别 | 典型情形 | 允许的动作 |
|---|---|---|
| **自动恢复** | 进程重启后重新登记后台服务；执行权按 TTL 到期；`queued` 任务取消 | 无需人工介入，等待流程自身收敛 |
| **可安全重试** | 记录明确为"操作没有发生"（`confirmed_not_applied`：HEAD 仍等于基线、远程任务分支不存在、远程基础分支仍等于基线） | 可以重新执行代码保存；执行前组件会重新核验分支、工作区与远程状态 |
| **必须人工核验** | 记录 `unconfirmed`、`unverifiable`；推送 `unknown` / `conflict`；合并无法归因 | 先按 6.2 核验真实 Git 与容器现场，结论落库后才允许继续 |
| **结果未知且禁止继续** | 远端操作可能已经发生但无法证明；容器不可访问；Docker 状态无法判定 | **禁止**重试、禁止删除需求/任务、禁止清理现场；保留证据并人工处置 |

禁止把"结果未知"写成普通失败，也禁止对带有外部副作用的操作无条件重试。删除需求在任一
"结果不明"状态下失败关闭，这是设计目标而不是障碍。

### 6.2 人工核验步骤（代码保存）

1. 读取记录的基线事实：`baseline_commit_sha`（提交前 HEAD）、`commit_sha`（预期提交）、
   `baseline_base_commit_sha`（合并前远程基础分支）。
2. 在任务容器或对应工作区用 Git 只读命令对照：
   - 提交是否发生：`git rev-parse <task_branch>`，与 `baseline_commit_sha` / `commit_sha` 比对，
     并用 `git rev-list --parents -n 1 <head>` 确认第一父等于基线（否则不能归因为本次提交）；
   - 推送是否发生：`git ls-remote <remote> refs/heads/<task_branch>`，与 `commit_sha` 比对；
   - 合并是否发生：`git ls-remote <remote> refs/heads/<base_branch>`，与基线或唯一可推算的合并
     提交比对；无法唯一推算时按"不可归因"处理。
3. 按结论记录事实：确认已发生 → 记录收敛为已发生；确认未发生 → 允许重新执行；两者都无法确认 →
   保持"结果不明"，继续人工处置并保留容器与仓库现场。
4. 远程历史与本地不一致（例如远程已被他人推进）时**禁止强制推送**，人工决定取舍。

核验只做只读查询。不要在核验前对仓库做 `reset`、`checkout`、`push --force`、删除远程分支等操作。

### 6.3 Docker 状态不明

容器状态判定统一走 `domain.ClassifyDockerContainerCondition`（`ContainerInspect` →
`RuntimeCondition` / `IsActivelyRunning` / `IsConfirmedExited` / `IsStartable` /
`ContainerObservedIndeterminate`）：

- `paused`、`restarting` 即使报告 `Running=true` 也不算可用，不得据此判定可以执行；
- 观察不到容器、Docker 不可达或状态无法归类时，一律按"不可判定"处理：既不当作已停止去重建，
  也不当作可运行去执行；
- `dead`、`removing` 属于已知但**未在真实环境观测**的状态，遇到时按"不可判定"处置并保留现场。

## 7. 数据库备份与恢复

备份与恢复只在独立目录里操作，**禁止**在生产数据库上试验恢复流程。

### 7.1 备份

```bash
# 1) 停止服务端并确认进程已退出（释放锁，WAL 由 SQLite 在关闭时收尾）
# 2) 用 SQLite 自身的备份能力生成一致性快照（不要在运行中复制 .db 文件）
sqlite3 /path/to/data/agentflow.db ".backup '/backup/agentflow-$(date +%Y%m%d).db'"

# 3) 校验备份完整性与 Schema 版本
sqlite3 /backup/agentflow-20260101.db "PRAGMA integrity_check; SELECT max(version_id) FROM goose_db_version;"
```

`integrity_check` 必须返回 `ok`；`goose_db_version` 的最大版本应等于当前发行版支持的版本（当前为 4）。

### 7.2 恢复

```bash
# 1) 在隔离目录中生成恢复库（从备份再导出一份，避免直接使用备份文件本身）
mkdir -p /restore/data
sqlite3 /backup/agentflow-20260101.db "VACUUM INTO '/restore/data/agentflow.db'"

# 2) 校验恢复库
sqlite3 /restore/data/agentflow.db \
  "PRAGMA integrity_check; SELECT max(version_id) FROM goose_db_version; SELECT count(*) FROM roles;"

# 3) 用指向恢复目录的配置启动服务端，确认页面与接口可用
./agentflow server --config /restore/agentflow.yaml
```

恢复后应核验：

- `goose_db_version` 与发行版一致（不一致说明备份来自其他版本，不要混用）；
- `roles` 中四个固定角色存在（缺失会在启动时补齐，但补齐说明备份不完整）；
- 项目的逻辑删除状态仍然生效：已删除项目不出现在 `agentflow project list` 与 `/api/v1/projects`
  的结果里，且历史任务、事件、代码保存记录仍在库中；
- 启动后不会被自动"修复"成另一种状态：本产品不做数据回填、不重放远程操作。

禁止：用旧备份覆盖正在运行的服务端数据库；手工改 `goose_db_version` 伪装版本；删除
`-wal` / `-shm` 文件来"修复"打开失败的库（先备份现场再判断）。

## 8. 常见故障

| 现象 | 判断依据 | 处置 |
|---|---|---|
| 启动失败：锁被占用 | 错误信息含 `server.lock` | 找到并停止已有服务端；不要删除锁文件 |
| 启动失败：配置错误 | `agentflow config validate --config <file>` 报错 | 按提示修正配置；`config validate` 在默认文件缺失时直接报错，不回退内置默认值 |
| 启动失败：端口占用 | 监听错误 | 换 `--address` 或释放端口 |
| 启动失败：迁移失败 | 日志含迁移错误，进程退出 | 保留数据库文件副本与完整日志；不要重试覆盖，先判断是否被其他版本占用 |
| `host validate` 不通过 | 预检输出非零退出码 | 按输出逐项修复（SSH 可达性、主机密钥信任、Docker 版本与 API 兼容性） |
| SSH 失败：主机密钥未知 | 错误指出主机密钥无法核验 | 人工核对指纹后用 `host trust` 写入；禁止关闭校验 |
| SSH 失败：认证失败 | 连接阶段失败 | 检查节点记录中的私钥路径与权限；本产品不支持密码认证 |
| Docker 状态不明 | `ContainerObservedIndeterminate` | 按 6.3 处置；不要删除容器或重建现场 |
| 需求删除返回 409 | 响应说明具体原因 | 按 5.3 逐项处理；不要在冲突未解决时反复重试删除 |
| 代码保存结果不明 | 记录 `code_save_indeterminate` / `code_save_unconfirmed` | 按 6.2 核验后处置；禁止直接重试或删除 |
| 任务长期停在 `queued`，反复出现 `任务启动流程未成功 stage=container` | `debug` 日志出现 `已有构建不可复用…镜像 ID 与记录不一致` | 镜像标签被改指，见 8.1；用新版本号重建 |

### 8.1 环境镜像标签与记录不一致

同一环境版本的镜像标签由「环境 + 版本号 + 构建配置哈希」决定。同一台 Docker 主机上**不要并行构建同一个
环境版本**：后完成的构建会把同一标签改指到另一个镜像，先前的记录随之失效。

- 现场表现：任务反复启动失败并长期停在 `queued`；`debug` 日志出现
  `已有构建不可复用，重新构建 version=vNNNN`，错误为 `镜像 ID 与记录不一致: 记录 sha256:…，实际 sha256:…`。
- 该回退路径不可用：版本已经是 `succeeded` 时会被领域状态机拒绝，用户看到的是
  `当前状态不允许该操作: EnvironmentVersion 已成功构建，不能重新标记为构建中`，真正原因只出现在 debug
  日志里。该限制在发布核验中登记为已知风险，产品代码不做自动重建。
- 处置：先用只读命令核对标签实际内容（`docker image inspect <image_ref>`）；确认镜像确实被改指后，让构建
  配置发生变化（例如固定父镜像摘要或调整 Dockerfile），再用 `agentflow docker env edit` 保存、用
  `agentflow docker env build` 构建。构建配置完全不变时会复用既有版本，不会产生新版本号。
- 禁止：手工改数据库里的 `image_id`、删除或重指镜像标签、用旧备份回填 `environment_versions`。

## 9. 日志与证据收集

- 服务端日志是结构化文本，输出到标准错误流，级别由 `app.log_level` 控制；排查时临时切到
  `debug`，问题定位后恢复。
- 任务时间线（事件）保存在数据库中，可通过 `GET /api/v1/tasks/{task_id}/events` 读取；任务详情
  接口同时给出步骤、尝试、会话、验证与代码保存结果。
- 远程证据：容器内 `git log` / `git rev-parse` / `git ls-remote` 的只读输出、`docker inspect`
  的状态判断结果、任务分支与基础分支的最新 SHA。收集证据时不要删除容器、不要改动仓库。
- 故障取证顺序：先保留运行中现场（容器、仓库、数据库副本），再改配置或重启；重启会推进
  部分收敛流程（例如执行权到期），抹掉部分现场信息。
