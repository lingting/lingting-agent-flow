# AgentFlow V1 最终产品状态

> **文档状态**：V1 收口文档（V2 M0 交付物）。
> **核实基线**：commit `e5c7845`（2026-11-02）工作区实际代码、迁移、`tests/` 结构与 CI 配置。
> **使用规则**：本文只记录最终状态，不记录逐步开发过程。旧过程文档已按 V2 M0 决策删除，
> 历史可从 Git 历史追溯。凡写"已交付"的能力，都能在代码中指向具体包、路由或测试目录；
> 凡证据不足或未复核的，一律列入末尾遗留事项，不写成已完成。

---

## 1. 产品定位

AgentFlow 是**本地优先的 AI Coding Workflow Orchestrator**。它把一条编码需求管理为持久化
任务，在受管理的**远程 Linux + Docker** 执行环境中完成代码获取、计划生成、分步开发、
确定性验证、语义审查、验证失败修复、代码保存（提交/推送/可选合并）与会话归档，并让用户
能发起、观察、取消、恢复与查看这些任务。

用户可见入口有两个，且职责不重叠：

- `agentflow server`：唯一完整服务，同时提供 HTTP API、内嵌 Web 页面与全部后台服务；
- `agentflow` CLI：管理命令（节点、项目、环境、容器、代码、任务），其中 `task run` 只是
  服务端工作流的 HTTP 客户端。

## 2. 已交付能力

### 2.1 执行节点与环境

- 执行节点（`execution_hosts`）注册、更新、逻辑删除、设为默认；认证方式固定为**无口令私钥**。
- 主机密钥校验不可关闭：known_hosts 全程只读，未知主机返回指纹与算法，密钥变化返回已信任记录。
- 节点环境预检：SSH 连接 → Docker 版本与 API 兼容性 → Docker CLI 帮助 → 基础 `FROM` 镜像元数据；
  四项必需检查全部通过才写 `normal`，`skipped` / `not_run` 不算通过。
- 语言环境定义、环境版本（只追加，历史版本不可覆盖）、构建与构建状态查询。
- Docker 兼容性基线固定在 CLI 29.0.0 / Engine 29.0.0 / API 1.52，所有 Docker 调用经
  `internal/docker` 统一入口。

### 2.2 项目与任务

- 项目（`projects`）登记仓库地址、基础分支、语言环境与版本、缓存类型、容器内工作目录、逻辑删除。
- 任务创建、列表分页、详情、取消、逻辑删除；任务状态机只在 `internal/domain` 内迁移。
- 统一任务启动（`TaskLaunchService`）与任务租约（`task_leases`，含 `lease_epoch` fencing）。
- 任务级工作流编排（`TaskWorkflowService`）：计划 → 逐步骤开发与验证 → 代码保存 → 收尾。
- 显式重试（`failed` 任务重开为新的执行周期）、续跑资格查询、修复周期（有上限）与周期历史。
- 失败现场的容器处置：只允许显式 `delete` / `detach`，detached 容器不被自动接管。

### 2.3 执行环境

- 统一执行环境契约 `internal/execenv`（`Provider` / `ExecPort`），已实现远程 Linux + Docker 容器，
  未实现类型返回 `ErrUnsupportedKind`，**没有**本机静默回退路径。
- 容器准备分四阶段（校验配置 → 准备镜像 → 创建容器 → 验证可用性），每次准备创建全新容器。
- 容器内命令一律以可执行文件 + 参数数组执行，不拼接 shell 字符串，不继承宿主机环境。
- 代码只存在于容器内 `/workspace/project`，不 bind mount 到宿主机。

### 2.4 代码获取与保存

- 代码获取（`source.Fetcher` 的 Git 实现）：`agent/<task-id>` 分支、固定 commit SHA、
  失败阶段与错误类别分别落库；凭据不入库、不入 URL、不入日志。
- 代码保存（`ContainerService.SaveCode`）：commit / push / merge 状态分别持久化；
  默认不 force push，默认不 merge（`merge_after_push` 默认关闭）；push 结果不确定时先核验远程。
- 代码初始化记录（`code_fetches`）与工作区初始化记录（`task_workspace_initializations`）可查询、可恢复。

### 2.5 验证与执行记录

- 确定性验证（`ValidationService`）：容器内必需检查命令，`ValidationRun` 状态与 `verdict` 结论分离，
  结论只由 `domain.DecideValidation` 依据结构化检查结果计算。
- 语义审查（Validator Agent）只能收紧结论，不能覆盖必需检查失败。
- 验证失败反馈（`validation_feedbacks`）结构化保存失败检查、退出码、脱敏输出摘要。
- 四层执行记录 `Task → Step → Attempt → Session` 全量保留；每次重试新建 Attempt 与新 Session。
- 会话原始 JSONL 归档与 HTML 导出状态分别记录，归档路径在本机持久目录，容器删除后仍可读。

### 2.6 调度与并发

- `TaskScheduler`：队列推进、全局有界并发（`scheduler.max_concurrent_tasks`）、任务租约。
- 每个任务独立 Git Worktree、独立容器、独立 Session；优先级由 `domain.Priority` 给出排序值。
- 服务重启后按数据库事实恢复：任务租约、工作流运行、计划生成现场、取消现场、代码获取/保存、
  容器与执行残留；不重放 Agent 调用。

### 2.7 Web 与交付形态

- Web 页面：概览、需求、项目、任务、机器、环境、设置（设置页当前为占位）。
- 任务详情覆盖启动、取消、重试、恢复、修复周期、代码保存核验、容器处置、单步诊断、
  执行记录与事件时间线、实时事件流。
- 单文件生产交付：前端产物经 `//go:embed` 内嵌，`make release` 先生成前端再编译 Go；
  `/api` 保留路径永远不回退 `index.html`；缺失静态资源返回 404。
- 服务默认只监听 `127.0.0.1`，`--require-web-ui` 默认开启。

## 3. 核心架构

```text
cmd → cli / httpapi → app → agent / process / gitworktree / docker / source / storage / events
                                                                         ↘ domain
```

- `internal/domain`：实体、状态与业务规则的唯一来源，不依赖数据库、HTTP、外部进程。
- `internal/app`：CLI 与 HTTP 的共同业务入口；任务级编排与单步骤闭环固定为
  `TaskWorkflowService` / `StepWorkflowService`。
- `internal/httpapi`：只做协议转换与 SSE 推送。
- `internal/lifecycle`：后台服务的统一启动与逆序停止，按注册顺序启动、反序停止。

注册到统一生命周期的后台服务：`execution_host_validation`、`task_lease_recovery`、
`task_scheduler`、`step_executor`、`validation_service`、`step_workflow`、`task_workflow`、
`container_recovery`、`code_fetch_recovery`、`code_save_recovery`、`execution_residue`。

## 4. 核心业务模型

| 模型 | 职责 | 关键约束 |
|---|---|---|
| `Requirement` | 用户希望的编码需求 | 当前只有标题/描述/优先级/状态/来源，**没有项目关联** |
| `Task` | 需求的一个执行单元 | 状态机唯一入口；`requirement_id` 必填；租约与 `lease_epoch` |
| `Step` | 逻辑执行阶段（plan / develop / verify …） | 序号唯一；占用由事务条件更新保证 |
| `Attempt` | 一次执行尝试 | 序号由存储层在事务内分配 |
| `Session` | 一次 Pi 会话 | 执行 / 归档 / 导出三种状态分别记录 |
| `ValidationRun` + `ValidationCheckResult` | 验证流程与逐项检查 | 结论只由 `domain.DecideValidation` 计算 |
| `WorkflowRun` | 一次任务级编排的推进状态 | 同一任务最多一条未结束记录（部分唯一索引） |
| `CodeFetch` / `CodeSave` | 代码获取与保存事实 | commit / push / merge 分别持久化 |
| `Container` | 任务容器生命周期 | 与任务执行状态分离；keep / delete / detach |
| `ExecutionHost` | 执行节点 | 私钥认证；校验状态只有 checking / normal / abnormal |
| `TaskLease` | 任务执行权 | 原子领取、续租、释放，epoch fencing |
| `Event` | 任务时间线 | 只追加，事件写入失败不回滚执行状态 |

## 5. 任务执行完整流程

```text
创建任务（requirement_id + 项目 + 节点 + 环境）
  → 准备执行环境（远程 SSH + Docker，任务级容器与工作区）
  → 获取代码并初始化 agent/<task-id> 分支
  → Planner 生成并持久化计划
  → 按计划顺序逐步执行：
       Developer 开发（独立 Attempt / Session）
       → 确定性验证（容器内必需检查）
       → Validator 语义审查（可收紧，不可放宽）
       → 验证失败进入有上限的修复周期并重新验证
  → 代码保存（Commit → Push → 可选 Merge）
  → 会话归档核验
  → 收尾（completed / blocked / recovery_required / failed / cancelled）
```

成功收尾同时要求：步骤全部通过验证、代码保存结论、会话归档事实齐备；缺少 CodeSaver 时
即使步骤全部通过也只能停留在 `blocked`。

## 6. 安全边界

- 主机密钥校验不可关闭，`InsecureIgnoreHostKey` 等价实现被禁止；known_hosts 不写、不加锁。
- 私钥只读、不复制、不入库、不入日志；加密私钥直接报错而不等待口令。
- 不存储模型凭据与私有仓库凭据；私有仓库认证是**用户自定义镜像的责任**，不是产品能力。
- 不挂载 Docker Socket、用户 Home、SSH 私钥、远程项目配置目录到任务容器。
- HTTP 默认只监听回环地址，未实现远程访问认证前不监听所有网卡。

## 7. CI/CD 与发布

- `ci.yml`：格式与静态检查、测试包分组核验、Go 单元测试、本机集成测试、前端检查与构建、
  单文件打包冒烟、真实远程用例（按配置跳过）。
- `release.yml` / `publish.yml`：版本标签校验 → CI 门禁 → 构建制品 → 发布前核验 →
  来源证明 → 拒绝覆盖已有 Release → 上传后核验远端资产。
- `make release` 是唯一推荐的单文件生产构建入口。

## 8. 测试结构

```text
tests/
  config/            真实外部依赖的开关与目标（pi / ssh / docker）
  internal/testkit/  统一入口与工作空间（TEST_ARTIFACT_DIR）
  internal/fakepi/   Pi 替身
  unit/              纯单元测试（domain / app / httpapi / docker / ssh / store …）
  integration/local/ 真实 SQLite + 真实服务装配，只替换外部依赖
  integration/pi/    真实 Pi CLI
  integration/ssh/   真实 SSH 目标
  integration/remote/ 真实远程 Docker / Agent / 工作流
  e2e/               CLI 与单文件打包冒烟
```

规则：测试产物落在 OS 临时目录随机工作空间并保留；本机缺少 `pi` 时只有真实 Pi 用例输出
`WARN [SKIP]`；真实远程用例必须显式开启且只操作随机后缀资源。

## 9. 最终验收依据

- 服务端第一版验收：结论 `completed`。代码层面在基线 `9c79a2c` 上复核成立（真实 SQLite +
  正式迁移 + 生产服务装配，仅替换 SSH / Docker / 容器内 Pi / 远程 Git 这些外部依赖）；
  真实远程完整链路（克隆 → 计划 → 开发 → 确定性验证 → 语义审查 → 代码保存 → 会话归档 → 收尾）
  实际跑通：`tasks.status=completed`、`result=succeeded`、`push=succeeded`、会话全部归档。
- 远程真实测试资源治理：结论 `completed`，以远程 `docker ps -a` 前后清单证明零残留。
- 上述两项都是**当时基线上的结论**；V2 M0 本轮**没有重跑**任何测试，也没有修改生产代码。

## 10. 明确遗留事项

1. **`P4-M5`（基于真实使用数据的优化）仍为 `pending`**：第一轮测量确认了候选 B（列表分页）
   与候选 C（调度扫描随历史线性增长）的**机制**，但缺"真实使用数据"这一必要条件，故不立项。
   测量基线工件保留在本机临时目录 `C:\Users\lingting\AppData\Local\Temp\p4m5-1793159954`。
2. **真实远程 `merge` 结果不确定后的核验**只有本机真实 Git + bare 远程证据，缺真实远程证据。
3. **Clone 失败信息**用脱敏形式，不显示实际 host:port 与传输类型，影响诊断可读性。
4. **真实远程工作流用例只覆盖成功路径**；远程失败与恢复路径由注入用例与本地链路级用例覆盖。
5. **`verification_results` 是冻结遗留表**：无运行时读写方，禁止重新读写、修改历史迁移或直接删表。
6. **`settings` 页面仍是占位**；环境管理只有查询 API，没有创建/修改/构建的 HTTP 入口。
7. **没有用户、用户组与权限审计**，也不支持自定义 Agent 角色；`roles.tools_policy_json`
   目前**只是声明**，调用 Pi 时未实施工具限制（`--append-system-prompt` 与 `--model` 生效，
   工具允许/拒绝列表不生效）。
8. **`Requirement` 没有项目关联**，也没有规划会话、需求分析与实施方案的持久化模型；
   需求与任务之间只有 `tasks.requirement_id` 单向引用。这是 V2 要补齐的核心数据缺口。
9. 代码注释中仍有一处指向已删除过程文档的引用（`internal/httpapi/execution_feedback.go`），
   属于本次收口的遗留引用，随后续里程碑清理。
