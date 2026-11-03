# V2 数据模型与迁移方案

> 状态：V2 设计基线（M0 交付物）。
> 依据：迁移 `0001`–`0025` 的实际结构、`domain/` 实体定义，以及迁移脚本与 `tests/unit/store/` 的
> 存储层用例。
> **本文件只给设计与迁移顺序，迁移脚本在对应里程碑落地；落地时必须重新核对当时的表结构。**

## 1. 现有结构事实（V1 结束时）

| 表 | 关键事实 | V2 需要的改变 |
|---|---|---|
| `projects`（`0025`） | 主键 `id`；仓库、基础分支、`env_type/env_name/env_version`、`work_dir`、`caches_json`、`deleted_at` | 增加默认执行节点 |
| `requirements`（`0009` 重建） | `id`、`title`、`description`、`priority`(int)、`status`、`source`、`source_id`、时间戳；**无 project_id、无逻辑删除、无规划快照** | 增加 `project_id`、规划关联、最终快照、`confirmed_at`、`deleted_at` |
| `tasks`（`0009` 重建） | `requirement_id NOT NULL`；`config_json`（含 `project_id`）；`status`；租约与 `lease_epoch`；无需求内顺序 | 增加需求内稳定顺序 |
| `roles`（`0009` 重建） | `id`、`type`（无 CHECK 白名单）、`runner`、`model`、`system_prompt`、`tools_policy_json`、`enabled`、`deleted_at` | 无需结构变更（见 §3.6） |
| `task_leases`（`0024`） | 按任务，含 `epoch` | 保持不动，与新项目租约共存 |

**结论**：V2 只需 5 个新迁移（`0026`–`0030`），不需要改动任何已发布迁移。其中 `0026` 规划会话
（原设计编号 `0027`）已在 M2 第二阶段落地，`0027` 需求项目关联顺延为原 `0026`；其余编号不变，版本
顺序仍然单调递增。

## 2. 迁移顺序与内容

顺序按依赖排列，每个迁移单独可回滚到上一版本（迁移事务内失败即回滚，不写版本记录）。

### 0026_planning_sessions.sql（已实现，M2 第二阶段）

实际落地脚本 `internal/storage/sqlite/migrations/0026_planning_sessions.sql`：

```sql
CREATE TABLE planning_sessions (
    id                  TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    project_id          TEXT    NOT NULL CHECK (length(trim(project_id)) > 0),
    status              TEXT    NOT NULL,
    host_id             TEXT,
    container_id        TEXT,
    source_commit       TEXT,
    analyst_session_id  TEXT,
    planner_session_id  TEXT,
    adopted_revision_id TEXT,
    error_summary       TEXT,
    created_at          INTEGER NOT NULL CHECK (created_at > 0),
    updated_at          INTEGER NOT NULL CHECK (updated_at > 0),
    finished_at         INTEGER CHECK (finished_at IS NULL OR finished_at > 0)
);

CREATE INDEX idx_planning_sessions_project
    ON planning_sessions (project_id, status, created_at);
CREATE INDEX idx_planning_sessions_status
    ON planning_sessions (status, created_at);

CREATE TABLE planning_messages (
    id                  TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    planning_session_id TEXT    NOT NULL CHECK (length(trim(planning_session_id)) > 0),
    sequence            INTEGER NOT NULL CHECK (sequence > 0),
    sender_type         TEXT    NOT NULL CHECK (length(trim(sender_type)) > 0),
    role_id             TEXT,
    content             TEXT    NOT NULL CHECK (length(trim(content)) > 0),
    created_at          INTEGER NOT NULL CHECK (created_at > 0)
);

CREATE UNIQUE INDEX idx_planning_messages_sequence
    ON planning_messages (planning_session_id, sequence);

CREATE TABLE planning_revisions (
    id                        TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    planning_session_id       TEXT    NOT NULL CHECK (length(trim(planning_session_id)) > 0),
    revision_number           INTEGER NOT NULL CHECK (revision_number > 0),
    requirement_analysis_json TEXT    NOT NULL CHECK (json_valid(requirement_analysis_json)),
    implementation_plan_json  TEXT    NOT NULL CHECK (json_valid(implementation_plan_json)),
    validation_result_json    TEXT    CHECK (validation_result_json IS NULL
                                             OR json_valid(validation_result_json)),
    created_at                INTEGER NOT NULL CHECK (created_at > 0)
);

CREATE UNIQUE INDEX idx_planning_revisions_number
    ON planning_revisions (planning_session_id, revision_number);
```

- `planning_sessions.status` 与 `planning_messages.sender_type` 不设取值白名单；未知取值必须能
  写入并原样读回，状态迁移规则只由 `internal/domain` 的状态机承担。
- `adopted_revision_id` 表示用户确认的那一版；确认动作同时把 `status` 推进到 `confirmed`。
- 消息序号与方案版本号分别由 `(planning_session_id, sequence)` 与
  `(planning_session_id, revision_number)` 唯一索引保证；序号由存储层在写入事务内分配，不
  "先查最大值再插入"。
- 只 `json_valid` 结构约束，不为分析/方案内部字段建列、不设字段白名单。
- 规划会话不进入 `steps` / `attempts` / `sessions`：规划阶段不产生正式任务，也不复用执行记录表。
- 本阶段只落地表结构与存储层读写，不含需求分析、方案生成、容器准备与 HTTP 入口。
- 已人工核对表、索引、字段、约束与迁移版本顺序；旧库升级与 Schema 审计没有自动化守点
  （见 §5.2），本阶段未运行任何数据库测试。

### 0027_requirements_project.sql

```sql
ALTER TABLE requirements ADD COLUMN project_id TEXT;
ALTER TABLE requirements ADD COLUMN planning_session_id TEXT;
ALTER TABLE requirements ADD COLUMN final_analysis_json TEXT;
ALTER TABLE requirements ADD COLUMN final_plan_json TEXT;
ALTER TABLE requirements ADD COLUMN confirmed_at INTEGER;
ALTER TABLE requirements ADD COLUMN deleted_at INTEGER;

CREATE INDEX idx_requirements_project
    ON requirements (project_id, deleted_at, created_at);
```

- 全部**可空**：历史需求没有项目、没有规划快照、没有确认时间。用 NOT NULL 会破坏旧库升级，
  业务必填性由应用层在确认流程中强制（`RequirementService.Confirm`）。
- `final_analysis_json` / `final_plan_json` 只存**用户确认的那一版** Revision 快照；工作台展示的
  "最新草案"取自 `planning_revisions`，不写入 `requirements`。
- 排序索引 `(project_id, deleted_at, created_at)` 覆盖"项目下需求列表"这一唯一读路径。

### 0028_projects_default_host.sql

```sql
ALTER TABLE projects ADD COLUMN default_host_id TEXT;

CREATE INDEX idx_projects_default_host ON projects (default_host_id)
    WHERE default_host_id IS NOT NULL;
```

- 可空表示未指定；未指定时需求工作台必须要求用户选择执行节点，不得猜测默认节点。
- 历史任务继续使用自己 `tasks.host_id` 与 `config_json` 快照，不因项目字段变化被改写。

### 0029_task_requirement_sequence.sql

```sql
ALTER TABLE tasks ADD COLUMN requirement_sequence INTEGER;

CREATE UNIQUE INDEX idx_tasks_requirement_sequence
    ON tasks (requirement_id, requirement_sequence)
    WHERE requirement_sequence IS NOT NULL;
```

- 需求内顺序是**业务事实**，不是时间戳推断值：确认生成时按方案顺序写入 1..N。
- 历史任务一律保持 `NULL`（不猜测顺序），读取层把 `NULL` 表示为"历史数据/未排序"，展示时
  以 `created_at` 兜底并标注，不得显示成确定的执行顺序。
- 部分唯一索引保证同一需求内不出现重复序号，同时允许历史 `NULL` 行共存。

### 0030_project_execution_leases.sql

```sql
CREATE TABLE project_execution_leases (
    project_id      TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(project_id)) > 0),
    task_id         TEXT,
    owner           TEXT    NOT NULL CHECK (length(trim(owner)) > 0),
    epoch           INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    lease_expires_at INTEGER NOT NULL CHECK (lease_expires_at > 0),
    updated_at      INTEGER NOT NULL CHECK (updated_at > 0)
);
```

- 以 `project_id` 为主键即为"每个项目最多一份执行权"的数据库级保证，不依赖进程内互斥。
- 释放时**不删行**：`task_id` 置 `NULL`、`epoch` 递增，用单调 `epoch` 做 fencing，
  使旧 Worker 的迟到写入被条件更新拒绝。
- 与 `task_leases` 并存，互不替代：任务租约证明"谁在跑这个任务"，项目租约证明
  "谁在占用这个项目的执行名额"。
- 清理策略：服务重启时按 `lease_expires_at` 判定是否过期，过期即可被重新获取；不主动删行。

### 固定四角色：不需要新迁移

- `roles.type` 在 `0009` 重建时已去掉 CHECK 白名单，新增取值 `requirement_analyst` 可直接写入。
- 四角色的"固定身份"由 `domain` 常量与 `app.RoleService` 保证（无增删入口），不是数据库约束；已实现于 V2-M1-E2。
- 默认 Prompt / 默认工具策略由应用层在初始化时按固定 ID 补齐缺失角色，已存在角色不被启动流程覆盖。

## 3. 历史数据回填规则（`0027` / `0029` 必须遵守）

`Requirement` 的 `project_id` 用 `tasks.config_json` 的 `$.project_id` 推导：

1. 取该 requirement 下所有任务的 `json_extract(config_json, '$.project_id')`。
2. **全部非空且唯一**，并且该值在 `projects` 中确有一条记录 → 回填。
3. 没有任何任务，或所有任务都不带项目 → 保持 `NULL`，不猜测。
4. 同一 requirement 下出现两个及以上不同项目 → 保持 `NULL`，记录冲突清单，交人工处理。
5. 值存在但 `projects` 中查不到记录 → 保持 `NULL`（指向不存在的项目不是合法关系）。

回填必须：

- 在迁移事务内完成，失败整体回滚；
- 不修改任何 `tasks` 行、不修改历史迁移、不删除任何历史记录；
- 回填条数、跳过条数与冲突条数写入迁移输出，供 `legacy_migration_test` 逐类断言；
- 视为幂等操作：重复执行不会改变已决定的结果。

## 4. 数据库约束规则（不变）

- 不声明外键，不使用 `REFERENCES` / `FOREIGN KEY` / 级联行为，不用触发器模拟引用完整性。
- 枚举式字段不设 `CHECK` 取值白名单；未知取值必须能写入并原样读回。
- 保留非枚举约束：主键、`NOT NULL`、有业务意义的 `UNIQUE`、数值范围、长度、`json_valid`、
  跨字段组合一致性。
- 逻辑删除优先：`requirements`、`projects`、`tasks`、`roles`、`execution_hosts` 用 `deleted_at`，
  删除不级联清空历史。
- 连接显式关闭外键检查（`foreign_keys(0)`），业务不得依赖该开关。
- 迁移在事务内完成；并发打开同一数据库只允许执行一次迁移。
- `verification_results` 是冻结遗留表：不读、不写、不改历史迁移、不加删除迁移。

## 5. 待验证风险（落地前必须实测，不写成结论）

1. **`ALTER TABLE ADD COLUMN` 与 CHECK 的组合**：本方案在 `0027` / `0028` / `0029` 只用无 CHECK
   的加列；需要 `json_valid` 的列全部落在**新建表**（`0026`）。若某个迁移必须给历史表加带 CHECK
   的列，必须改为 `0009` 已证明可行的"建新表 → 复制 → 改名 → 重建索引"路径，并保留全部非枚举约束。
2. **旧库升级与 Schema 审计已没有自动化守点**：`tests/integration/local/database/`（
   `legacy_migration_test.go`、`schema_audit_test.go`）已在测试精简中删除，新增迁移落地时必须人工核对
   新表结构、索引、非枚举约束、旧库升级后数据保留、未知枚举值写入与读回、迁移幂等。未执行的核对不得
   报告为已完成。
   其中 `0026` 已人工核对表结构、索引、字段、非枚举约束与版本顺序；旧库升级后的数据保留、未知枚举值
   写入与读回、迁移幂等仍未在真实数据库上执行，不得报告为已验证。
3. **`json_extract` 依赖 SQLite JSON1 扩展**：需确认当前驱动与最低 SQLite 版本已内置（`caches_json`
   的 `json_valid` 已在用，仍按实际迁移验证）。
4. **回填冲突的人工处理入口**：本方案只记录冲突、不提供 UI；若真实库出现冲突，需要在
   M4 前给出处置方式（保持 `NULL` 并在需求页标注"历史需求未关联项目"）。
