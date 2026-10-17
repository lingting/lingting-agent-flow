-- AgentFlow 第一版数据库结构。
--
-- 约定：
--   * 主键统一为 TEXT，由应用层生成；
--   * 时间统一为 UTC Unix 毫秒时间戳，字段以 _at 结尾；
--   * JSON 统一以 UTF-8 文本保存，并通过 json_valid 约束保证结构合法；
--   * 布尔值以 INTEGER 保存，只能取 0 或 1；
--   * 所有外键显式声明 ON DELETE 行为，禁止级联删除历史记录。

-- roles：运行时角色配置。
CREATE TABLE roles (
    id                TEXT    PRIMARY KEY NOT NULL,
    name              TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    type              TEXT    NOT NULL CHECK (type IN ('planner', 'developer', 'validator')),
    runner            TEXT    NOT NULL CHECK (length(trim(runner)) > 0),
    model             TEXT    NOT NULL CHECK (length(trim(model)) > 0),
    system_prompt     TEXT    NOT NULL CHECK (length(trim(system_prompt)) > 0),
    tools_policy_json TEXT    NOT NULL CHECK (json_valid(tools_policy_json)),
    enabled           INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE INDEX idx_roles_type ON roles (type, enabled);

-- requirements：用户提交的原始需求。
--
-- priority 以整数排序值保存（0 最高），与 domain.Priority.Rank 一致。
CREATE TABLE requirements (
    id          TEXT    PRIMARY KEY NOT NULL,
    title       TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    description TEXT    NOT NULL CHECK (length(trim(description)) > 0),
    priority    INTEGER NOT NULL CHECK (priority >= 0),
    status      TEXT    NOT NULL CHECK (
        status IN ('queued', 'processing', 'completed', 'failed', 'cancelled')
    ),
    source      TEXT    NOT NULL CHECK (length(trim(source)) > 0),
    source_id   TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE INDEX idx_requirements_queue ON requirements (status, priority, created_at);

-- tasks：某个需求的一次执行实例。
--
-- 同一需求可以拥有多次执行记录；历史任务不允许通过删除重建实现重试。
CREATE TABLE tasks (
    id              TEXT    PRIMARY KEY NOT NULL,
    requirement_id  TEXT    NOT NULL REFERENCES requirements (id) ON DELETE RESTRICT,
    status          TEXT    NOT NULL CHECK (
        status IN (
            'queued', 'planning', 'planned', 'developing', 'verifying',
            'retrying', 'paused', 'completed', 'failed', 'cancelled'
        )
    ),
    repository_path TEXT    NOT NULL CHECK (length(trim(repository_path)) > 0),
    base_ref        TEXT    NOT NULL CHECK (length(trim(base_ref)) > 0),
    worktree_path   TEXT,
    current_step_id TEXT,
    created_at      INTEGER NOT NULL,
    started_at      INTEGER,
    finished_at     INTEGER,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX idx_tasks_requirement ON tasks (requirement_id, created_at);
CREATE INDEX idx_tasks_status ON tasks (status, created_at);

-- steps：Planner 生成的结构化开发步骤。
--
-- role_snapshot_json 保存计划生成时的角色配置，避免角色日后修改导致历史不可追溯。
CREATE TABLE steps (
    id                     TEXT    PRIMARY KEY NOT NULL,
    task_id                TEXT    NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    sequence               INTEGER NOT NULL CHECK (sequence > 0),
    title                  TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    objective              TEXT    NOT NULL CHECK (length(trim(objective)) > 0),
    instructions_json      TEXT    NOT NULL CHECK (json_valid(instructions_json)),
    role_id                TEXT    NOT NULL REFERENCES roles (id) ON DELETE RESTRICT,
    role_snapshot_json     TEXT    NOT NULL CHECK (json_valid(role_snapshot_json)),
    verification_spec_json TEXT    NOT NULL CHECK (json_valid(verification_spec_json)),
    status                 TEXT    NOT NULL CHECK (
        status IN ('pending', 'developing', 'verifying', 'completed', 'failed', 'skipped')
    ),
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL,
    UNIQUE (task_id, sequence)
);

CREATE INDEX idx_steps_task ON steps (task_id, sequence);
CREATE INDEX idx_steps_role ON steps (role_id);

-- attempts：某个开发步骤的每次完整执行尝试。
--
-- 失败后保留原有 Attempt，新的修复必须创建新的 Attempt。
CREATE TABLE attempts (
    id              TEXT    PRIMARY KEY NOT NULL,
    step_id         TEXT    NOT NULL REFERENCES steps (id) ON DELETE RESTRICT,
    attempt_number  INTEGER NOT NULL CHECK (attempt_number > 0),
    status          TEXT    NOT NULL CHECK (
        status IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')
    ),
    failure_summary TEXT,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    created_at      INTEGER NOT NULL,
    UNIQUE (step_id, attempt_number)
);

CREATE INDEX idx_attempts_step ON attempts (step_id, attempt_number);

-- sessions：每次具体 Agent 执行会话的元数据。
--
-- Planner 等任务级会话的 step_id 与 attempt_id 为空；关联 Attempt 的会话
-- 必须同时给出 step_id，其任务与步骤归属由存储层在事务内校验。
CREATE TABLE sessions (
    id                  TEXT    PRIMARY KEY NOT NULL,
    task_id             TEXT    NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    step_id             TEXT    REFERENCES steps (id) ON DELETE RESTRICT,
    attempt_id          TEXT    REFERENCES attempts (id) ON DELETE RESTRICT,
    role_id             TEXT    NOT NULL REFERENCES roles (id) ON DELETE RESTRICT,
    role_snapshot_json  TEXT    NOT NULL CHECK (json_valid(role_snapshot_json)),
    provider            TEXT    NOT NULL CHECK (length(trim(provider)) > 0),
    model               TEXT    NOT NULL CHECK (length(trim(model)) > 0),
    external_session_id TEXT,
    process_id          INTEGER CHECK (process_id IS NULL OR process_id > 0),
    status              TEXT    NOT NULL CHECK (
        status IN ('starting', 'running', 'completed', 'failed', 'cancelled', 'interrupted')
    ),
    started_at          INTEGER NOT NULL,
    finished_at         INTEGER,
    created_at          INTEGER NOT NULL,
    CHECK (attempt_id IS NULL OR step_id IS NOT NULL)
);

CREATE INDEX idx_sessions_task ON sessions (task_id, created_at);
CREATE INDEX idx_sessions_step ON sessions (step_id, created_at);
CREATE INDEX idx_sessions_attempt ON sessions (attempt_id, created_at);

-- verification_results：每次验证的结构化结果。
--
-- 每次验证产生独立记录，禁止用更新 passed 字段的方式覆盖历史。
CREATE TABLE verification_results (
    id                       TEXT    PRIMARY KEY NOT NULL,
    task_id                  TEXT    NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    step_id                  TEXT    NOT NULL REFERENCES steps (id) ON DELETE RESTRICT,
    attempt_id               TEXT    NOT NULL REFERENCES attempts (id) ON DELETE RESTRICT,
    validator_session_id     TEXT    REFERENCES sessions (id) ON DELETE RESTRICT,
    passed                   INTEGER NOT NULL CHECK (passed IN (0, 1)),
    summary                  TEXT,
    checks_json              TEXT    NOT NULL CHECK (json_valid(checks_json)),
    command_results_json     TEXT    NOT NULL CHECK (json_valid(command_results_json)),
    failure_summary          TEXT,
    created_at               INTEGER NOT NULL,
    CHECK (passed = 1 OR length(trim(coalesce(failure_summary, ''))) > 0)
);

CREATE INDEX idx_verification_results_attempt ON verification_results (attempt_id, created_at);
CREATE INDEX idx_verification_results_step ON verification_results (step_id, created_at);

-- events：任务工作流中的关键事件，只追加写入。
CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      TEXT    NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    step_id      TEXT    REFERENCES steps (id) ON DELETE RESTRICT,
    attempt_id   TEXT    REFERENCES attempts (id) ON DELETE RESTRICT,
    session_id   TEXT    REFERENCES sessions (id) ON DELETE RESTRICT,
    event_type   TEXT    NOT NULL CHECK (length(trim(event_type)) > 0),
    payload_json TEXT    NOT NULL CHECK (json_valid(payload_json)),
    created_at   INTEGER NOT NULL
);

-- 任务时间线查询：按任务过滤并按写入顺序返回。
CREATE INDEX idx_events_task ON events (task_id, id);
CREATE INDEX idx_events_step ON events (step_id, id);
CREATE INDEX idx_events_attempt ON events (attempt_id, id);
