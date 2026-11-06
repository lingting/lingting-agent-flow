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

需求状态只表达业务阶段，**不表达执行进度**：

- `planned`：已确认生成（初态）
- `executing`：至少一个关联 Task 正在执行或已执行
- `completed`：全部关联 Task 均为 `completed`
- `blocked`：存在 `failed` / `recovery_required` / `cancelled` 的关联 Task，且不满足完成条件
- `deleted` 语义由 `deleted_at` 表达，不新增状态值

状态由 `domain` 纯函数依据关联 Task 的真实状态计算，与 `RequirementProgress` 同源。

### 4.3 项目执行状态（只读投影）

- `idle`：没有关联 Task 在执行
- `running`：当前有 Task 在执行
- `blocked`：存在需要人工处置的 Task（失败 / 待恢复），且没有 Task 在执行
- `paused`：用户显式停止

项目状态是**投影**，不落库为独立状态机，也不允许覆盖 Task 状态。

## 5. 确认与原子生成

`RequirementService.Confirm(planningSessionID, revisionID)`：

前置条件（任一不满足即返回 409 / 412，不写入任何记录）：

1. 会话 `status = preview_ready`；
2. `revisionID` 属于该会话，且是该会话已生成的有效 Revision；
3. 方案通过 §3 的结构化校验；
4. 会话所属 Project 存在且未逻辑删除，且有可用执行节点（项目默认节点或调用时指定）；
5. 会话与 Project 一致，防止把 A 项目的草案落到 B 项目。

事务内一次性创建（同一事务，失败整体回滚，不留部分任务）：

- `requirements`：1 条，`project_id`、`planning_session_id`、`final_analysis_json`、
  `final_plan_json`、`confirmed_at` 来自确认的 Revision；
- `tasks`：N 条，`requirement_id` 指向新需求，`requirement_sequence` = 方案顺序，
  项目/环境/节点/工作目录从 Project 继承并写入 `config_json` 快照；
- 步骤：按 V1 既有计划生成约定写入（不在 HTTP Handler 内临时拼装）；
- `planning_sessions`：`status = confirmed`、`adopted_revision_id`、`finished_at`。

确认完成后：任务处于待执行（`queued`），**不**调用 `TaskLaunchService`，**不**进入 Scheduler。

## 6. 项目级执行调度

### 6.1 领取流程

```text
用户点击「开始执行」（项目级显式动作）
  → 校验项目存在、未删除、有可执行任务、执行节点通过环境预检
  → 进程内取得项目级执行权（同一份数据目录只有一个 Server 进程，进程锁已排除第二个执行者）
  → 选择该项目下一个 Task（按 requirement_sequence，其次 created_at；跳过已终态）
  → 候选 Task 必须满足：同项目无更早的未完成/失败/待恢复 Task
  → 调用既有 TaskLaunchService + TaskWorkflowService 执行
  → 收敛后：成功则释放执行权并允许领取下一个
            失败 / 待恢复 / 取消 → 停止继续领取，项目进入 blocked
```

### 6.2 必须覆盖的失败与并发场景

| 场景 | 要求 |
|---|---|
| 同一进程并发领取 | 进程内项目级执行权串行化同一项目的领取；状态写入仍用条件更新兜底 |
| 第二个 Server 进程 | 进程锁在打开数据库之前失败退出，不存在第二个执行者，也不做跨进程仲裁 |
| 服务重启 | 按既有恢复规则收敛未完成的执行记录，不重放 Agent 调用；进程内执行权不跨进程存活 |
| 任务取消 | 取消经既有 `cancelling` → `cancelled` 收敛后才释放执行权 |
| 执行超时 | 映射为 `timed_out`，按阻塞处理，不自动领取下一个 |
| 执行权丢失 | 持有者在收敛前不得继续写任务状态；状态写入带条件，冲突时读取数据库真实值 |
| 迟到结果 | 所有状态写入带条件（必要时用记录版本做比较并交换），冲突时读取数据库真实值 |
| 同项目失败阻塞 | 后续 Task 不得越过失败任务；其他项目不受影响 |
| 全局并发 | 仍受 `scheduler.max_concurrent_tasks` 限制；项目串行不等于降低全局上限 |

上述场景在 M5 实施时按**单 Server** 架构重新确定具体执行协调方式：项目级执行权是否只保存在进程内、
是否需要辅助的数据库进度记录，都在那时决定；不引入多实例 epoch 仲裁或数据库租约表。

## 7. 进度计算

规则唯一实现在 `domain`（例如 `DecideRequirementProgress`），输入只有关联 Task 的状态与
`requirement_sequence`，输出结构化结果：

```text
total / completed / running / pending / failed / recovery_required
current = completed
status  = completed | blocked | executing | planned
```

- 完成判定：全部关联 Task 均 `completed`（且需求未删除）。
- 阻塞判定：存在 `failed` / `recovery_required` / `cancelled` 时不判完成。
- 进度是**任务完成数量比**，不是语义完成度；不显示模型生成的百分比或总结。
- 项目进度由项目下全部正式 Task 状态聚合，规则同源，不另建一套判定。

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

1. 项目级"开始执行"是**自动连续领取**下一个 Task，还是每完成一个任务需要用户再次确认。
   默认倾向：自动连续领取，失败即停（与 V1 任务级工作流一致的收敛语义）。
2. 需求删除时，已结束 Task 是随需求一起逻辑删除，还是保留可见（见
   [`PRODUCT-SCOPE.md`](./PRODUCT-SCOPE.md) §7）。
3. 同一需求内是否允许用户显式调整已生成任务的执行顺序。默认倾向：不允许，顺序由方案确认时固定。
