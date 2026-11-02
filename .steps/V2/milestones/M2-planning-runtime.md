# M2：规划运行环境

> 状态：**pending**。
> 前置：M1 completed。参考：[`../ARCHITECTURE.md`](../ARCHITECTURE.md) §4、
> [`../DATA-MODEL.md`](../DATA-MODEL.md) `0027`、[`../ROLE-DESIGN.md`](../ROLE-DESIGN.md) §7。

## 1. 目标

为需求规划提供**可复用、只读、可恢复**的运行环境：

1. 一个 PlanningSession 对应一个独立规划容器。
2. 项目代码以固定 commit SHA 的只读快照进入容器。
3. 代码目录只读、rootfs 只读、唯一可写目录是 Session / 临时目录。
4. 多轮规划复用同一容器、同一快照，两个角色各自保留 Pi Session 上下文。
5. 会话确认 / 放弃 / 失败后释放容器与快照卷，保留消息、分析、方案历史。

## 2. 范围

### 2.1 执行环境能力（`internal/execenv` + `internal/docker`）

- `execenv` 增加只读工作区语义（代码目录只读、可写目录显式声明），未支持的类型继续返回
  `ErrUnsupportedKind`。
- `internal/docker` 增加 `--read-only`、`--tmpfs`、`--mount ...,readonly` 的构造能力。
- 同步 `docker.RequiredCLICommands`，保证节点预检覆盖新增参数。

### 2.2 App

- `PlanningRuntimeService`：准备（准备容器克隆到快照卷）→ 启动规划容器 → 复用 → 释放。
- `PlanningSessionService`：会话创建、状态推进、容器/Session 关联、结束与清理。
- 两个角色各一条 Pi Session 路径，由服务端生成并校验落在 Session 根目录内。

### 2.3 持久化

- 迁移 `0027_planning_sessions.sql`（`planning_sessions` / `planning_messages` / `planning_revisions`）。
- 迁移 `0028_projects_default_host.sql`（项目默认执行节点）。

### 2.4 生命周期

- 规划运行时作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`；
  服务停止时取消在途调用并等待收敛，不留不受管理的容器与子进程。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 同一规划会话多轮复用同一容器 | 多轮请求后 `planning_sessions.container_id` 不变，快照卷不变 |
| 不同规划会话互相隔离 | 会话之间容器、卷、Session 文件均不同 |
| AI 能读取真实项目代码 | 规划调用中读到实际仓库文件内容 |
| 所有代码写入尝试被运行环境拒绝 | 文件工具与 Shell 两条路径写入均失败，且无提交记录 |
| Session 与容器生命周期可持久化恢复 | 重启后会话状态与容器关联仍可读，过期会话可被收敛 |
| 规划容器不进入提交流程 | 规划路径不调用任何 code save / push / merge 服务 |

## 4. 验收方式（默认不自动执行）

- 阅读代码：只读挂载参数构造、`--read-only` 与 tmpfs 的构造点、`RequiredCLICommands` 同步、
  Session 路径生成与根目录校验、释放路径、生命周期注册顺序。
- 实际写入尝试（若用户授权在真实环境验证）：文件写入与 `sh -c 'echo > 文件'` 都必须失败。
- 若用户明确要求测试，覆盖：只读参数构造、会话隔离、容器复用、释放与收敛、失败诊断。

## 5. 风险与待决

1. **只读方案的实现成本**：方案 A（准备容器 + 只读卷）需要两阶段准备与快照卷管理；
   必须在 M2 开始时确认真实 Docker 29.0.0 支持所用的参数组合。
2. **克隆到卷的失败恢复**：准备容器失败时不得留下"已登记但不存在"的容器或卷引用。
3. **长会话资源占用**：闲置规划会话的容器是否回收（保留到显式放弃 vs 超时回收），M2 定稿。
4. **Session 归档**：规划 Session 归档沿用 V1 能力，需确认归档路径不落在项目代码目录内。

## 6. 明确不做

- 不做需求分析、方案生成业务（M3）。
- 不创建正式 Requirement / Task（M4）。
- 不实现"用 Prompt 让模型别写代码"作为唯一限制。
- 不把规划会话写进 Task 的 Step / Attempt 执行记录。
