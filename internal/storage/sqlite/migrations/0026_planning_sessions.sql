-- 0026_planning_sessions.sql
-- 新增规划会话：planning_sessions / planning_messages / planning_revisions。
--
-- 背景：V2 把工作入口从"创建任务"升级为"提出需求"，需求在生成正式 Requirement 与
-- Task 之前先经过多轮规划。规划阶段的全部事实必须落库，否则进程退出后既无法恢复
-- 对话，也无法判断某一版方案是否被采纳。
--
-- 设计说明：
--   * 规划会话与被执行的 Task / Step / Attempt / Session 是两套模型：规划阶段不产生
--     正式任务，因此不复用 steps / attempts / sessions 表，也不写 executed 记录。
--   * planning_sessions 保存会话生命周期与运行环境关联：所属项目、状态、执行节点、
--     规划容器、固定代码快照 commit、两个规划角色的 Pi 会话标识、采纳的方案版本、
--     失败摘要与时间戳。这些字段是本机重启后恢复会话所需的全部事实。
--   * status / sender_type 是枚举式字段，**不设取值白名单**：新增取值必须能在不修改
--     Schema 的情况下写入，未知取值也必须能原样读回。状态迁移规则由 internal/domain
--     的状态机承担。
--   * planning_messages 只追加不覆盖：消息序号在会话内唯一，由存储层在写入事务内
--     分配；唯一索引是"序号不重复"的最终数据约束。
--   * planning_revisions 记录每一轮的完整快照：需求分析、实施方案与校验结果都作为
--     JSON 文档保存，只做 json_valid 结构约束，不为 JSON 内部字段建列、不设字段白名单。
--     版本号同样在会话内唯一，由存储层在事务内分配。
--   * 规划容器、快照卷与 Session 资源的创建与释放不在本迁移范围内：本表只保存其标识。
--
-- 约束取舍（遵循既有 Schema 约定）：
--   * 不声明外键，不使用 REFERENCES / FOREIGN KEY / 级联行为，也不用触发器模拟引用
--     完整性；project_id、host_id、container_id、adopted_revision_id 等关联标识允许
--     出现指向已不存在记录的值，关系合法性由应用层校验。
--   * 保留非枚举约束：主键、NOT NULL、长度、正数时间戳、序号范围与 json_valid 结构。
--   * 逻辑删除不适用于规划会话：结束后保留消息与版本历史，不物理删除记录。

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

-- 项目下的会话列表按 project_id 过滤、按创建时间倒序读取：索引以 project_id 为前缀，
-- 使过滤阶段走索引而不是全表扫描。
CREATE INDEX idx_planning_sessions_project
    ON planning_sessions (project_id, status, created_at);

-- 按状态定位未结束的会话：服务重启后只处理仍可能占用规划容器的记录。
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

-- 消息序号在会话内唯一：存储层在事务内分配序号，本索引是写入路径被绕过时的最后防线。
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

-- 版本号在会话内唯一：历史版本只追加不覆盖，确认时按标识指出采纳的那一版。
CREATE UNIQUE INDEX idx_planning_revisions_number
    ON planning_revisions (planning_session_id, revision_number);
