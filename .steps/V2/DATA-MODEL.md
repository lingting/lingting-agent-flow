# V2 数据模型与当前 Schema

> 状态：V2 设计基线（M0 交付物，已按"版本化迁移脚本"更新）。
> 依据：`internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本、`domain/` 实体定义，以及
> `tests/unit/store/` 的存储层用例。
> **本文件只描述当前最终态结构与后续里程碑需要的结构变化；实现时必须重新核对当时的迁移脚本。**

## 1. Schema 的唯一入口

- `internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本通过 `//go:embed` 编译进程序，是数据库结构的
  **唯一事实来源**；`00001_initial_schema.sql` 是当前初始最终态。
- 每次打开数据库时由 Goose 读取脚本、识别版本并执行所有待执行的迁移，已执行的版本记录在
  `goose_db_version` 表中，重复打开不会重复执行；每个脚本在单个事务内执行，失败整体回滚。
- **结构变化只追加一个更高版本的脚本**（如 `00002_xxx.sql`）：不修改已经执行过的脚本，不再维护第二份
  完整的最终态 Schema 文件，也不手写迁移引擎、版本判断与自定义版本表。
- 程序不提供旧的手写 Schema 数据库兼容：不识别旧库、不转换 `user_version`、不回填数据，也不自动删除、
  清空或重建用户数据库，旧库需由用户自行备份后手动重建。

## 2. 当前最终 Schema 结构

### 2.1 角色、需求与执行节点

| 表 | 关键事实 |
|---|---|
| `roles` | `id`、`name`、`type`（无取值白名单）、`runner`、`model`、`system_prompt`、`tools_policy_json`（`json_valid`）、`enabled`、时间戳、`deleted_at`（逻辑删除）；索引 `idx_roles_type (type, enabled)` |
| `requirements` | `id`、`title`、`description`、`priority`（非负整数，0 最高）、`status`（无白名单）、`source`、`source_id`、时间戳；确认生成相关列 `project_id`、`planning_session_id`、`final_analysis_json` / `final_plan_json`（`json_valid`）、`confirmed_at`、`deleted_at`（全部可空，业务完整性由应用层强制）；索引 `idx_requirements_queue (status, priority, created_at)`、`idx_requirements_project (project_id, created_at) WHERE project_id IS NOT NULL AND deleted_at IS NULL`、`idx_requirements_active (created_at) WHERE deleted_at IS NULL` |
| `execution_hosts` | 连接目标（`host` / `port` 1–65535 / `username` / `workspace_root`）、`is_default`（部分唯一索引保证最多一个）、`enabled`、认证配置（`auth_method`、`private_key_path`、主机密钥算法/指纹/信任时间）、最近连接状态与错误、`validation_*` 预检状态与代次、`deleted_at`；触发器保证"私钥认证必须给出私钥路径" |

`execution_hosts` 的索引：`idx_execution_hosts_default (is_default) WHERE is_default = 1`、
`idx_execution_hosts_name (name)`、`idx_execution_hosts_validation (validation_status) WHERE deleted_at IS NULL`。

### 2.2 任务执行链

| 表 | 关键事实 |
|---|---|
| `tasks` | `requirement_id`、`status`（无白名单）、`host_id`、`config_json`、`repository_path`、`base_ref`、`worktree_path`、`container_id`、`current_step_id` / `current_attempt_id`、取消请求、`final_result`、`failure_summary`、`idempotency_key`（部分唯一索引）、`requirement_sequence`（可空，部分唯一索引 `(requirement_id, requirement_sequence) WHERE requirement_sequence IS NOT NULL`）、`deleted_at`；**没有租约列**：任务级执行权由进程内任务协调器登记，数据库不保存 |
| `steps` | `task_id`、`sequence`（`UNIQUE (task_id, sequence)`）、`step_type`（默认 `develop`，无白名单）、`title`、`objective`、`instructions_json`、`role_id` / `role_snapshot_json`（非 Agent 阶段为空）、`verification_spec_json`、`retry_json`、`max_attempts`（1–10）、`current_attempt_id`、`status`、`result`、`failure_stage`、`failure_summary`、时间戳 |
| `attempts` | `step_id`、`task_id`（可空，仅供历史孤立记录）、`attempt_number`（`UNIQUE (step_id, attempt_number)`）、`status`、`container_id` / `docker_id`、`input_digest`、`exit_code`、`timed_out` / `cancelled`、重试与失败分类、`verification_verdict` / `verification_summary` / `verification_id`、`session_ids_json`、`code_save_id`、Git 快照（`git_branch` / `git_before_json` / `git_after_json`）、修复预算归属（`budget_scope` / `budget_scope_id`）、时间戳 |
| `sessions` | `task_id`、`step_id` / `attempt_id`（`attempt_id` 非空时 `step_id` 必须非空）、`role_id` / `role_snapshot_json`、`provider` / `model`、`external_session_id`、`process_id`、`status`、Session 与输出路径（`session_jsonl_path` / `session_html_path` / `stdout_path` / `stderr_path` / `archive_path` / `archive_status`）、`run_status` / `completeness` / `export_status` / `exit_code` / `failure_summary`、`invocation_id`、`container_id` / `docker_id`、时间戳 |
| `events` | 自增整数主键保留写入顺序；`task_id`、`step_id` / `attempt_id` / `session_id`、`event_type`、`payload_json`、`created_at`；只追加写入 |

执行链索引：`tasks` 按需求/状态/节点/容器/幂等键/未删除列表；`steps` 按任务序号、角色、状态；
`attempts` 按步骤序号、状态、任务、Git 分支、修复周期；`sessions` 按任务、步骤、尝试、运行状态、
完整度、调用标识、容器、任务+角色+状态；`events` 按任务、步骤、尝试。

### 2.3 环境版本与容器

| 表 | 关键事实 |
|---|---|
| `environment_versions` | 主键 `(host_id, env_type, env_name, version_number)`（`version_number` 1–9999）；三个 64 位小写十六进制哈希（`dockerfile_sha256` / `build_config_sha256` / `context_sha256`）、`parent_version`、`image_ref`、`image_id`（`sha256:` + 64 位）、`build_status`（无白名单）、`error_summary`、`created_at` / `built_at` |
| `containers` | `id`、`host_id`、`docker_id`、`name`（`UNIQUE (host_id, name)`）、环境三元组、构建配置哈希、`image_id`、`task_id` / `attempt_id`（尝试关联必须同时给出任务）、管理状态 `state` 与观测状态 `docker_state`、时间戳、`end_reason`、`detached` / `detached_at` / `synced_at`、`error_summary` |

### 2.4 代码初始化、代码保存与验证

| 表 | 关键事实 |
|---|---|
| `code_fetches` | 尝试级代码初始化：`task_id` / `step_id` / `attempt_id` 必填、来源与仓库、`work_dir`、容器、基础分支与任务分支（两者不得相同）、commit SHA（40/64 位）、状态与失败分类、时间戳；部分唯一索引 `idx_code_fetch_active_attempt` 保证同一 Attempt 最多一条未结束记录 |
| `task_workspace_initializations` | 任务级工作区初始化：以「任务 + 容器代次」为幂等边界，字段与 `code_fetches` 同构但不伪造 `step_id` / `attempt_id`；部分唯一索引 `idx_task_workspace_init_active` |
| `code_saves` | 提交、推送、合并三段状态互相独立；分支名非空且基础分支不得等于任务分支；三个 SHA 列限定 40/64 位；`merge_enabled`；`attempt_id`；恢复相关列（`baseline_commit_sha` / `baseline_base_commit_sha` / `persist_status` / `persist_error` / `recovery_status`）；时间戳 |
| `validation_runs` | 一次确定性验证：`status` 与 `verdict` 分离，`spec_json` / `spec_snapshot`、四项计数（`passed + failed + skipped <= total`）、执行环境与工作区、`validator_session_id`、语义审查（`review_status` / `review_summary` / `assessment_json`）、时间戳 |
| `validation_check_results` | 验证运行内逐项检查：`(validation_run_id, check_id)` 唯一、`sequence`、`definition_json`、`status`、退出码与输出路径/摘要、`timed_out` / `cancelled` / `certain`、失败原因、`duration_ms` |
| `validation_feedbacks` | 可复用的失败反馈：`validation_run_id` 唯一，`verdict`、`summary`、`infrastructure_error`、`checks_json`；只描述上一轮发生了什么 |

### 2.5 编排、修复与项目

| 表 | 关键事实 |
|---|---|
| `workflow_runs` | 任务级编排记录：`plan_digest`、`status` / `stage`（无白名单）、当前游标与已完成游标、`completed_steps <= total_steps`、取消请求、`worker_id` / `lease_expires_at`、`error_class` / `error_summary`、`revision`（乐观并发）；部分唯一索引 `idx_workflow_runs_active_task` 保证同一任务最多一条未结束记录 |
| `repair_cycles` | 开发执行周期：`task_id` / `step_id`、`sequence`（步骤内唯一）、`max_attempts` 上限快照、`opened_by`、`opened_at` / `closed_at`（结束不得早于开启）；部分唯一索引 `idx_repair_cycles_active_step` 保证步骤内最多一个活动周期 |
| `projects` | 项目登记：`id`（同时是远程目录名）、`name`、`repository_url`（已脱敏）、`base_branch`、`env_type` / `env_name` / `env_version`（可空）、`work_dir`、`caches_json`、时间戳、`deleted_at`、`default_host_id`（可空，部分索引） |

### 2.6 规划会话

| 表 | 关键事实 |
|---|---|
| `planning_sessions` | 所属项目、`status`（无白名单）、运行期绑定（`host_id` / `container_id` / `source_commit`）、两个角色的 Pi 会话标识、`adopted_revision_id`、`error_summary`、时间戳；索引按项目+状态+创建时间与按状态 |
| `planning_messages` | 会话内多轮消息：`(planning_session_id, sequence)` 唯一，`sender_type`（无白名单）、`role_id`、`content`、`created_at`；只追加不覆盖 |
| `planning_revisions` | 每轮完整快照：`(planning_session_id, revision_number)` 唯一，需求分析 / 实施方案 / 校验结果均为 JSON 文档（只做 `json_valid`） |

规划会话的互斥不是数据库事实：同一份数据目录只允许一个 Server 进程，`RunRole` / `Prepare` / `Release`
由进程内的 `app.PlanningSessionCoordinator` 串行化，规划会话**没有**租约表、owner、epoch 或 TTL。
（M5 的 `project_execution_leases` 属于项目级执行协调，与规划会话的互斥无关，两者互不替代。）

### 2.7 冻结的遗留表

`verification_results` 是**已冻结的遗留数据**：结构仍由初始迁移保留（`passed`、`verdict`、
`checks_json`、`command_results_json`、`assessment_json`、`review_skipped` 等），但当前生产代码不再读写
它，确定性验证结论由 `validation_runs` 与 `validation_check_results` 承载。禁止在业务代码中重新写入或
读取，也禁止在没有可靠数据保全方案时删除该表定义。

## 3. V2 已交付的结构（M2 / M3 / M4 / M5）

- **规划会话**（M2 第二阶段）：`planning_sessions` / `planning_messages` / `planning_revisions` 三张表
  与四个索引（两个会话列表索引、消息序号唯一索引、方案版本唯一索引）。
- **项目默认执行节点**（M2 第八阶段）：`projects.default_host_id`（可空）与部分索引
  `idx_projects_default_host`。可空表示"未设置"，写入时把空值落成 SQL NULL，不为 NULL 行建立索引项；
  历史任务继续使用自己的 `tasks.host_id` 与 `config_json` 快照。
- **规划会话租约表已删除**：M2 第九阶段曾引入 `planning_session_leases`（含部分索引
  `idx_planning_session_leases_expiry`）做跨进程互斥；单 Server 架构确认后该表与对应的 owner、epoch、
  TTL、续租机制一起删除，迁移脚本中不再存在，也不需要为旧开发数据库保留迁移路径。
- **需求的项目关联与规划快照**（M4，迁移 `00003_requirement_confirmation.sql`）：
  `requirements` 增加 `project_id`、`planning_session_id`、`final_analysis_json`、`final_plan_json`、
  `confirmed_at`、`deleted_at` 与两个索引（项目需求列表、未删除需求列表）。这些列全部可空：
  "有规划来源就必须完整"由 `internal/domain` 与确认流程在应用层强制，数据库不复制这套组合判定；
  `final_*` 只存用户确认的那一版 Revision 快照，只做 `json_valid` 结构约束。
- **需求内任务顺序**（M4，同一个迁移）：`tasks` 增加 `requirement_sequence` 与部分唯一索引
  `(requirement_id, requirement_sequence) WHERE requirement_sequence IS NOT NULL`。顺序是业务事实而不是
  时间戳推断值，由确认生成流程按下标写入；历史任务是 NULL，不参与唯一索引，也不补造顺序。
- **项目执行权与执行意图**（M5，迁移 `00004_project_execution_leases.sql`）：
  `project_execution_leases` 以 `project_id` 为主键表达"每个项目最多一份执行权"，列为 `owner`、
  单调递增 `epoch`、`expires_at`、`acquired_at`、`updated_at`，另有按 `owner` 的索引（续租与收敛按
  持有者列出自己的登记）。取得只允许"没有登记"或"已到期"，并把 `epoch` 原子递增；释放只把
  `expires_at` 置为当前时间，**不删行、不重置 epoch**，因此旧持有者的续租 / 释放（条件为
  `owner + epoch`）会被拒绝。它不用于多实例仲裁，只用于跨请求、跨重启识别执行权归属。
  `project_execution_states` 以 `project_id` 为主键，保存 `state`（无白名单，最后一次决策的投影记录）
  与可空 `stop_requested_at`（用户已请求停止领取的事实）。两者都不声明外键、不使用枚举白名单、
  不做数据回填；项目被逻辑删除或任务被删除后，孤立行允许保留。

固定四角色不需要结构变化：`roles.type` 没有取值白名单，新增角色类型可直接写入；"固定身份"由 `domain`
常量与 `app.RoleService` 保证，不是数据库约束；缺失角色由应用层按固定 ID 补齐，已有角色不被覆盖。

## 4. 后续里程碑需要的结构变化（尚未实现）

M5 之后只剩 M6（需求停止与逻辑删除、端到端验收），它不新增表、不新增列：删除只写既有
`requirements.deleted_at` 与 `tasks.deleted_at`，执行历史（Step / Attempt / Session / Event /
CodeSave / 容器记录）保持原样。因此当前没有待实现的 Schema 变化。

后续若出现新的结构变化，仍按同一规则**追加新的迁移脚本**，不提供旧库升级：已有的开发数据库需要
用户自行备份后手动重建。

不再保留历史数据回填规则：单文件 Schema 只用于全新数据库初始化，既有开发库不升级，因此不需要用
`json_extract` 从 `tasks.config_json` 推导 `requirements.project_id`，也不需要在 Schema 初始化中做任何
数据搬运。

## 5. 数据库约束规则（不变）

- 不声明外键，不使用 `REFERENCES` / `FOREIGN KEY` / 级联行为，不用触发器模拟引用完整性。
- 枚举式字段不设 `CHECK` 取值白名单；未知取值必须能写入并原样读回。
- 保留非枚举约束：主键、`NOT NULL`、有业务意义的 `UNIQUE`、数值范围、长度、`json_valid`、跨字段组合
  一致性。
- 逻辑删除优先：`roles`、`execution_hosts`、`tasks`、`projects` 用 `deleted_at`，删除不级联清空历史。
- 连接显式关闭外键检查（`foreign_keys(0)`），业务不得依赖该开关。
- 每个迁移脚本由 Goose 在单个事务内执行，失败整体回滚；同一数据库只允许一个 Server 进程，迁移只由持有
  进程文件锁的实例执行。
- `verification_results` 是冻结遗留表：结构保留在初始迁移，不读、不写、不删除。

## 6. 待确认风险

1. **Schema 没有自动化审计守点**：`tests/integration/local/database/`（Schema 审计、旧库升级与数据保留）
   已在测试精简中删除。新增迁移脚本必须人工核对表结构、索引、非枚举约束、未知枚举值的写入与读回，
   并确认仓储 SQL 依赖的列与索引全部存在；未执行的核对不得报告为已完成。
2. **不兼容旧库的边界**：程序不识别旧的手写 Schema 数据库，也没有在真实旧库上执行过；需要重建时必须
   明确提示用户自行备份后手动重建数据库文件，不得让用户误以为程序会自动升级。
3. **结构变化的重建成本**：因为不提供升级，`M3`–`M5` 已落地的结构变化（`00002`–`00004` 迁移脚本）意味着
   在此之前创建的开发数据库必须人工备份后重建；`docs/development.md` 已写明该操作方式。
