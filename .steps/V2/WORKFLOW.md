# V2 工作流

> 状态：V2 设计基线。前置阅读：[`PRODUCT-SCOPE.md`](./PRODUCT-SCOPE.md)、[`ARCHITECTURE.md`](./ARCHITECTURE.md)。
> 所有状态与判定规则只允许实现在 `internal/domain`（纯函数），业务层只做编排。

## 1. 两个阶段，边界固定

| 阶段 | 对象 | 产物 | 是否创建正式业务实体 |
|---|---|---|---|
| 规划阶段 | PlanningSession | 消息历史、分析、方案、Revision（草案） | 否 |
| 执行阶段 | Requirement + Task | 正式需求、正式任务、步骤与执行记录 | 是（仅在确认时） |

规划阶段不得产生正式 `Task`；执行阶段不得重新生成方案。生成任务**不会**启动执行。

## 2. 需求分析输出（需求分析师）

结构化字段（全部必需，缺失或无法解析即本轮规划失败，不得用自然语言兜底写库）：

- `goal`：用户最终目标
- `current_state`：现有项目结构与相关模块、当前实现
- `scope`：本次需求范围
- `non_goals`：非目标
- `business_constraints` / `technical_constraints` / `compatibility_constraints`
- `impact`：影响范围
- `risks`
- `open_questions`：未确认事项
- `acceptance_criteria`

## 3. 实施方案输出（方案规划师）

- `summary`：总体实施思路
- `architecture_changes`：技术架构调整
- `modules`：涉及模块
- `tasks`：任务草案数组（**至少 1 个**），每项包含
  `title`、`description`、`objective`、`implementation_notes`、`done_criteria`、
  `acceptance_criteria`、`sequence`
- `execution_order`：任务执行顺序（与 `sequence` 一致）

任务拆分约束（由 `domain` 的纯函数校验，不满足即拒绝生成）：

1. 至少 1 个任务；`sequence` 从 1 开始连续递增且唯一。
2. 每个任务必须能**独立验收**：`objective`、`done_criteria`、`acceptance_criteria` 非空。
3. 不允许"依赖前序任务未合并代码才能编译/运行"的拆分；不能安全独立验收的步骤必须合并为一个任务。
4. 不建设通用 Task DAG，不支持任务间自动串接与自动合并分支。
5. 方案中的任务顺序就是生成后的 `tasks.requirement_sequence`，不依赖时间戳推断。

## 4. 状态机

### 4.1 PlanningSession

```text
active ──发送消息──> analyzing ──方案生成并落 Revision──> preview_ready
  ^                     │                                     │
  │                     └──失败──> failed (终态)               │
  │                                                          ├─用户确认──> confirmed (终态)
  └────────────继续对话（同容器、同代码快照）─────────────────┘
                                                             └─用户放弃──> abandoned (终态)
```

- `analyzing` 期间的重复消息请求返回 409（同会话同时只允许一轮运行）。
- `confirmed` / `abandoned` / `failed` 为终态：不再接受消息，容器进入释放流程。
- 失败可通过显式"重新运行分析"回到 `analyzing`；只有 `failed` 允许重试，不得把失败静默改成 `active`。

### 4.2 Requirement 状态

需求有**两个不同口径**，不得混用：

1. `requirements.status`（持久化业务阶段，V1 既有枚举，M5 未改动）：`queued` / `processing` /
   `completed` / `failed` / `cancelled`。它由既有需求状态机推进，不由任务状态反推。
2. `RequirementProgress`（M5 交付的进度投影，不落库）：`planned` / `executing` / `blocked` /
   `completed`，只由 `domain.DecideRequirementProgress` 依据关联 Task 的真实状态计算，与项目
   进度同源。`deleted` 语义由 `deleted_at` 表达，不新增状态值。

进度投影的判定顺序固定：

- `completed`：存在任务且全部关联 Task 都是 `completed`（这是唯一的完成口径）；
- `blocked`：存在 `failed` / `recovery_required` / `cancelled` / `paused` 的关联 Task，且不满足
  完成条件；阻塞任务取**按执行顺序第一个**阻止完成判定的任务，而不是只看队首：
  "已有任务完成、后面有任务失败"的现场必须显示阻塞；
- `executing`：有任务仍在执行，或已有任务完成但未全部完成；
- `planned`：其余情况。

### 4.3 项目执行状态（只读投影）

- `idle`：没有关联 Task 在执行，也没有持有项目执行权
- `running`：存在执行中的 Task，或项目仍持有有效执行权（正在按顺序领取任务）
- `blocked`：队首任务需要人工处置（失败 / 待恢复 / 取消 / 暂停 / 无执行记录的中间状态），
  且没有 Task 在执行、也没有执行权
- `paused`：用户已请求停止领取，且当前没有 Task 在执行

优先级固定为"执行中 > 已停止 > 阻塞 > 空闲"。

项目状态是**投影**：只由 `domain.DecideProjectExecution` 依据项目内全部正式 Task 的真实状态、
项目执行权与用户停止意图计算，不落库为独立状态机，也不允许覆盖 Task 状态。

`project_execution_states` 只保存**执行意图**（最后一次决策写下的 `state` 与 `stop_requested_at`），
用于跨重启保留"用户已请求停止领取"这一事实；接口返回的状态一律重新计算，不读该表当作状态机。

## 5. 确认与原子生成

`RequirementService.Confirm(planningSessionID, revisionID)`：

前置条件（任一不满足即返回 409 / 412 / 404，不写入任何记录）：

1. 会话存在且 `status = preview_ready`（`confirmed` 是终态，不再接受确认）；
2. `revisionID` 由用户显式指定，且属于该会话、快照版本可读、需求分析与实施方案都能独立解析；
3. 方案通过 §3 的结构化校验（至少 1 个任务、依赖只指向更早的任务、标题 / 描述 / 验收条件非空），
   任务顺序由数组下标 +1 推导并连续唯一；
4. 会话所属 Project 存在且未逻辑删除，且有可用执行节点（调用时指定优先，其次项目
   `default_host_id`；两者都没有时失败，不回退本机执行，也不读规划会话绑定的规划容器节点）；
5. 会话与 Project 一致，防止把 A 项目的草案落到 B 项目。

事务内一次性创建（同一事务，失败整体回滚，不留部分任务）：

- `requirements`：1 条，`project_id`、`planning_session_id`、`final_analysis_json`、
  `final_plan_json`、`confirmed_at` 来自确认的 Revision；标题取最终需求分析的摘要，描述是
  "摘要 + 目标 + 范围 + 约束 + 验收标准"的确定性摘要；初始状态沿用需求领域的既有约定（`queued`）；
- `tasks`：N 条，`requirement_id` 指向新需求，`requirement_sequence` = 方案数组顺序，
  仓库 / 分支 / 环境 / 环境版本 / 缓存 / 工作目录 / 执行节点从 Project 继承并写入 `config_json` 快照；
- 步骤：**确认阶段不创建 Step**。V1 的约定是任务进入执行流程后由 Planner 在任务容器内生成计划，
  因此 `queued` 任务在开始执行前没有 Step 是既定事实，`GeneratePlan` 的职责不变，也不在两处生成同一组步骤；
- `planning_sessions`：`status = confirmed`、`adopted_revision_id`、`finished_at`，以 `preview_ready`
  为前置条件做条件更新。

并发语义：同一会话只允许确认成功一次。状态条件更新以 `preview_ready` 为前置条件，并发确认只有一个
返回成功，其余返回 409 且整体回滚，不会创建第二个需求或第二批任务，也不会覆盖其他执行者写入的
`adopted_revision_id`。前端按钮禁用只是体验层，不承担并发安全。

确认完成后：任务处于待执行（`queued`），**不**调用 `TaskLaunchService`，**不**进入 Scheduler，
**不**创建 Step / Attempt / Session / WorkflowRun / 容器。

## 6. 项目级执行调度

### 6.1 领取流程

```text
用户点击「开始执行」（项目级显式动作，POST /api/v1/projects/{projectID}/start）
  → 校验项目存在且未逻辑删除（否则 404）
  → 读取项目内正式任务（经 requirements.project_id，排除已删除需求与任务）
  → 按领域规则排序并判定队首；已有任务在执行 / 队首需人工处置 / 没有可执行任务一律拒绝
  → 复用自动调度准入（domain.DecideTaskQueueAdmission）核验队首任务的前置条件
  → 原子取得项目执行权（project_execution_leases，epoch 递增；同一持有者重复开始是幂等的）
  → 清除停止意图并触发一次 TaskScheduler.Scan
  → 返回真实项目执行状态（不返回"可能已开始"）

TaskScheduler.Scan（唯一的自动分派循环，仍在原位置）
  → 收敛本进程持有的项目执行权（真实收敛的项目释放执行权）
  → 统计全局名额（scheduler.max_concurrent_tasks）
  → 读取 queued 候选 → 既有准入判定 → 既有队列排序
  → 项目闸门过滤：必须持有有效执行权、未收到停止意图、且是项目队首任务
  → 取得任务执行权 → 既有 TaskLaunchService.Launch（不新建启动流程）

收敛后
  → 成功：队首前移，下一次扫描自动领取下一个任务（自动连续领取，无需再次确认）
  → 失败 / 待恢复：项目阻塞（队首任务挡住后续任务），执行权释放
  → 用户停止且任务已真实收敛：意图记为 paused，执行权释放
  → 全部任务真实完成：意图记为 idle，执行权释放
```

项目执行权是**数据库事实**（`project_execution_leases`：`project_id` 主键、`owner`、单调递增
`epoch`、`expires_at`），不是进程内 map：

- 取得：`UPDATE ... WHERE project_id=? AND expires_at <= now`，影响 0 行再插入；仍冲突即"项目正忙"。
- 续租 / 释放：必须带 `owner + epoch` 条件，0 行按冲突处理，旧持有者的迟到写入被拒绝。
- 释放不删行、不重置 epoch；释放后按到期处理，重新取得时 epoch 递增。
- 有效期 2 分钟，续租与收敛周期 40 秒（`app.ProjectExecutionKeeper`）。
- 单 Server 进程锁不变：本表不用于多实例仲裁，只用于跨请求、跨重启识别执行权与停止意图。

### 6.2 必须覆盖的失败与并发场景

| 场景 | 实现 |
|---|---|
| 同一项目并发开始 | 条件写入只有一个成功，其余返回 409；进程内按项目串行化只是第一道防线 |
| 同一项目并发领取 | 项目闸门只放行队首任务；任务执行权仍由进程内协调器按任务互斥 |
| 第二个 Server 进程 | 进程锁在打开数据库之前失败退出，不存在第二个执行者，也不做跨进程仲裁 |
| 服务重启 | 新进程的 owner 与旧租约不同，不会自动领取项目任务；未过期租约到期后按 epoch 递增重取；不重放任何 Agent 调用 |
| 任务取消 | 项目停止只记录停止意图并调用既有取消入口；任务真实收敛（`cancelling` → `cancelled`）后才释放执行权 |
| 执行超时 | 映射为 `timed_out`，按阻塞处理，不自动领取下一个 |
| 执行权丢失 / 续租失败 | 停止按该执行权领取任务（闸门按 owner + epoch 判定），不重放远程操作，不影响其他项目 |
| 迟到结果 | 任务状态写入继续使用既有条件更新；项目租约的续租与释放同样带条件 |
| 同项目失败阻塞 | 后续 Task 不得越过失败任务；执行权释放，项目状态为 blocked；其他项目不受影响 |
| 显式启动入口 | 统一启动流程额外核验"同项目没有其他在途任务"，避免绕过项目执行权并发执行两个任务 |
| 全局并发 | 仍受 `scheduler.max_concurrent_tasks` 限制；项目串行不等于降低全局上限 |


### 6.3 项目停止语义

`POST /api/v1/projects/{projectID}/cancel` 的语义固定为：

1. 先把停止意图落库（`project_execution_states`：`state=stopping`、`stop_requested_at`），因此下一次
   扫描就不会再领取该项目的新任务；
2. 有在途任务时调用**既有**任务取消入口（`TaskService.CancelTask`），只记录取消请求并推进到
   `cancelling`，不直接改写任务状态；
3. 当前任务真实收敛到终态后，才释放项目执行权并记录 `paused`；收敛前执行权保留；
4. 没有在途任务时立即释放执行权并记录 `paused`；
5. 结果不确定（任务进入 `failed` / `recovery_required`）时保留真实状态，不伪造 `paused`；
6. 取消不删除任务、不删除执行记录、不回滚 Git，也不影响其他项目。

重复停止是幂等的：已停止且没有在途任务时按当前状态返回，不再生成新的写操作。

## 7. 进度计算

规则唯一实现在 `domain`（例如 `DecideRequirementProgress`），输入只有关联 Task 的状态与
`requirement_sequence`，输出结构化结果：

```text
total / completed / running / pending / failed / recovery_required / cancelled / paused
current = completed
status  = completed | blocked | executing | planned
```

- 完成判定：存在任务且全部关联 Task 均 `completed`（且需求未删除）。
- 阻塞判定：存在 `failed` / `recovery_required` / `cancelled` / `paused` 时不判完成。
  `cancelled` 单独计数，**不并入 `failed`**：两者都阻止完成判定，但原因不同，接口与前端分别展示。
- 进度是**任务完成数量比**，不是语义完成度；不显示模型生成的百分比或总结。
- 项目进度由项目下全部正式 Task 状态聚合，规则同源，不另建一套判定。
- 唯一实现：`domain.DecideRequirementProgress` / `domain.DecideProjectExecution`；
  接口字段见 `docs/http-api.md` §17。

## 8. 需求删除流程

```text
用户请求删除需求
  → 若存在执行中的 Task：先按既有规则发起取消（不直接删除）
  → 等待 Task 真实收敛（cancelling → cancelled / 或已终态）
  → 再次校验：存在 running / cancelling / recovery_required / 结果不确定 → 拒绝（409）
  → 逻辑删除 Requirement（deleted_at）与关联 Task（deleted_at）
  → 保留 Step / Attempt / Session / Event / CodeSave / 容器记录
```

硬性要求：

- 运行中任务**不能**被直接删除；不确定状态不能被伪装成已停止。
- 删除只写 `deleted_at`，不物理删除任何执行历史，不级联清理 Session 归档与代码保存记录。
- 删除不代表 Git 已回滚；不实现自动 Reset / Revert / 强制覆盖。
- 已 `detach` 的容器不被自动启动、停止、删除或重新接管。

## 9. 端到端场景映射

| 场景 | 覆盖里程碑 | 关键校验点 |
|---|---|---|
| A 单需求多任务 | M3 / M4 / M5 | 多轮同一容器、确认后 1 Requirement + N Task、顺序执行、进度真实 |
| B 规划阶段只读 | M2 | 文件工具与 Shell 两条写入路径都被文件系统拒绝、无提交记录 |
| C 不同项目并行 | M5 | 同项目串行、跨项目并行、全局并发不超限 |
| D 同项目失败阻塞 | M5 | A2 不自动开始、A 显示阻塞、B 正常 |
| E 需求删除 | M6 | 未收敛拒绝删除、删除后历史仍可查询 |

## 10. 待决问题

1. ~~项目级"开始执行"是自动连续领取还是每个任务人工确认~~ **已定稿（M5）**：自动连续领取，
   失败 / 待恢复 / 取消即停；不需要逐任务确认，也不提供"跳过前序任务"的入口。
2. 需求删除时，已结束 Task 是随需求一起逻辑删除，还是保留可见（见
   [`PRODUCT-SCOPE.md`](./PRODUCT-SCOPE.md) §7）。
3. 同一需求内是否允许用户显式调整已生成任务的执行顺序。默认倾向：不允许，顺序由方案确认时固定。
