-- 工作流引擎所需的扩展数据模型。
--
-- 约定：
--   * 兼容迁移：不删除任何机器、项目、容器或 Session 记录，历史任务仍然可读；
--   * Task、Step、Attempt 的状态取值扩展为工作流语义，旧取值全部保留；
--   * 新增列一律可空或带安全默认值，旧记录升级后仍能通过约束校验；
--   * 任务配置、重试策略与验证规格以 JSON 保存，且必须通过 json_valid 校验；
--   * 不保存任何凭据：任务配置中禁止出现私钥、密码或 Token。
--
-- 状态约束需要扩展，SQLite 无法直接修改 CHECK 约束，因此对 tasks、steps、
-- attempts、verification_results 采用"新建表 → 复制 → 删除旧表 → 重命名"的
-- 标准重建流程。外键校验在事务内通过 defer_foreign_keys 推迟到提交时执行，
-- 因此重建过程中短暂缺失的引用不会导致迁移失败。

PRAGMA defer_foreign_keys = ON;

-- tasks：补齐工作流所需的关联与租约字段。
CREATE TABLE tasks_new (
    id                   TEXT    PRIMARY KEY NOT NULL,
    requirement_id       TEXT    NOT NULL REFERENCES requirements (id) ON DELETE RESTRICT,
    status               TEXT    NOT NULL CHECK (
        status IN (
            'queued', 'preparing', 'planning', 'planned', 'developing', 'verifying',
            'saving', 'cancelling', 'retrying', 'paused', 'completed', 'failed',
            'cancelled', 'recovery_required'
        )
    ),
    host_id              TEXT    REFERENCES execution_hosts (id) ON DELETE RESTRICT,
    config_json          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(config_json)),
    repository_path      TEXT    NOT NULL CHECK (length(trim(repository_path)) > 0),
    base_ref             TEXT    NOT NULL CHECK (length(trim(base_ref)) > 0),
    worktree_path        TEXT,
    container_id         TEXT,
    current_step_id      TEXT,
    current_attempt_id   TEXT,
    cancel_requested     INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0, 1)),
    cancel_requested_at  INTEGER CHECK (cancel_requested_at IS NULL OR cancel_requested_at > 0),
    final_result         TEXT    CHECK (
        final_result IS NULL
        OR final_result IN ('succeeded', 'failed', 'cancelled')
    ),
    failure_summary      TEXT,
    idempotency_key      TEXT    CHECK (
        idempotency_key IS NULL OR length(trim(idempotency_key)) > 0
    ),
    lease_owner          TEXT,
    lease_expires_at     INTEGER CHECK (lease_expires_at IS NULL OR lease_expires_at > 0),
    created_at           INTEGER NOT NULL,
    started_at           INTEGER,
    finished_at          INTEGER,
    updated_at           INTEGER NOT NULL,
    -- 取消请求必须同时记录请求时间。
    CHECK (cancel_requested = 0 OR cancel_requested_at IS NOT NULL),
    -- 关联尝试时必须同时指定步骤。
    CHECK (current_attempt_id IS NULL OR current_step_id IS NOT NULL),
    -- 持有租约时必须记录到期时间。
    CHECK (
        lease_owner IS NULL
        OR (length(trim(lease_owner)) > 0 AND lease_expires_at IS NOT NULL)
    )
);

INSERT INTO tasks_new (
    id, requirement_id, status, config_json, repository_path, base_ref, worktree_path,
    current_step_id, created_at, started_at, finished_at, updated_at
)
SELECT
    id, requirement_id, status, '{}', repository_path, base_ref, worktree_path,
    current_step_id, created_at, started_at, finished_at, updated_at
FROM tasks;

DROP TABLE tasks;

ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX idx_tasks_requirement ON tasks (requirement_id, created_at);
CREATE INDEX idx_tasks_status ON tasks (status, created_at);
CREATE INDEX idx_tasks_host ON tasks (host_id) WHERE host_id IS NOT NULL;
CREATE INDEX idx_tasks_container ON tasks (container_id) WHERE container_id IS NOT NULL;
-- 幂等键在全局唯一：同一客户端请求不会被重复创建为两个正在运行的任务。
CREATE UNIQUE INDEX idx_tasks_idempotency ON tasks (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- steps：补齐阶段类型、重试策略与执行结果字段。
--
-- role_id 与 role_snapshot_json 允许为空：环境准备、确定性验证、代码保存与清理
-- 阶段不调用 Agent，因此不关联角色。
CREATE TABLE steps_new (
    id                     TEXT    PRIMARY KEY NOT NULL,
    task_id                TEXT    NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    sequence               INTEGER NOT NULL CHECK (sequence > 0),
    step_type              TEXT    NOT NULL DEFAULT 'develop' CHECK (
        step_type IN ('prepare', 'source', 'plan', 'develop', 'verify', 'review', 'save', 'cleanup')
    ),
    title                  TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    objective              TEXT    NOT NULL CHECK (length(trim(objective)) > 0),
    instructions_json      TEXT    NOT NULL CHECK (json_valid(instructions_json)),
    role_id                TEXT    REFERENCES roles (id) ON DELETE RESTRICT,
    role_snapshot_json     TEXT    CHECK (
        role_snapshot_json IS NULL OR json_valid(role_snapshot_json)
    ),
    verification_spec_json TEXT    NOT NULL CHECK (json_valid(verification_spec_json)),
    retry_json             TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(retry_json)),
    max_attempts           INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 10),
    current_attempt_id     TEXT,
    status                 TEXT    NOT NULL CHECK (
        status IN (
            'pending', 'running', 'developing', 'verifying', 'completed', 'failed',
            'retry_wait', 'skipped', 'cancelled', 'recovery_required'
        )
    ),
    result                 TEXT,
    failure_stage          TEXT,
    started_at             INTEGER,
    finished_at            INTEGER,
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL,
    UNIQUE (task_id, sequence),
    -- 需要 Agent 的阶段必须关联角色，非 Agent 阶段不得关联角色。
    CHECK (
        (step_type IN ('plan', 'develop', 'review') AND role_id IS NOT NULL)
        OR (step_type IN ('prepare', 'source', 'verify', 'save', 'cleanup') AND role_id IS NULL)
    )
);

INSERT INTO steps_new (
    id, task_id, sequence, step_type, title, objective, instructions_json, role_id,
    role_snapshot_json, verification_spec_json, retry_json, max_attempts,
    current_attempt_id, status, result, failure_stage, started_at, finished_at,
    created_at, updated_at
)
SELECT
    id, task_id, sequence, 'develop', title, objective, instructions_json, role_id,
    role_snapshot_json, verification_spec_json, '{}', 3,
    NULL, status, NULL, NULL, NULL, NULL,
    created_at, updated_at
FROM steps;

DROP TABLE steps;

ALTER TABLE steps_new RENAME TO steps;

CREATE INDEX idx_steps_task ON steps (task_id, sequence);
CREATE INDEX idx_steps_role ON steps (role_id);
CREATE INDEX idx_steps_status ON steps (status, updated_at);

-- attempts：补齐容器关联、退出状态、重试原因与验证摘要。
CREATE TABLE attempts_new (
    id                    TEXT    PRIMARY KEY NOT NULL,
    step_id               TEXT    NOT NULL REFERENCES steps (id) ON DELETE RESTRICT,
    attempt_number        INTEGER NOT NULL CHECK (attempt_number > 0),
    status                TEXT    NOT NULL CHECK (
        status IN (
            'running', 'completed', 'failed', 'cancelled', 'interrupted',
            'timed_out', 'unknown', 'recovery_required'
        )
    ),
    container_id          TEXT,
    docker_id             TEXT,
    input_digest          TEXT,
    exit_code             INTEGER NOT NULL DEFAULT -1,
    timed_out             INTEGER NOT NULL DEFAULT 0 CHECK (timed_out IN (0, 1)),
    cancelled             INTEGER NOT NULL DEFAULT 0 CHECK (cancelled IN (0, 1)),
    retry_reason          TEXT,
    error_class           TEXT,
    failure_stage         TEXT,
    failure_summary       TEXT,
    verification_verdict  TEXT    CHECK (
        verification_verdict IS NULL
        OR verification_verdict IN ('passed', 'failed', 'inconclusive')
    ),
    verification_summary  TEXT,
    verification_id       TEXT,
    session_ids_json      TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(session_ids_json)),
    code_save_id          TEXT,
    started_at            INTEGER NOT NULL,
    finished_at           INTEGER,
    created_at            INTEGER NOT NULL,
    UNIQUE (step_id, attempt_number),
    -- 已结束的尝试必须记录结束时间。
    CHECK (
        status = 'running'
        OR status = 'unknown'
        OR status = 'recovery_required'
        OR finished_at IS NOT NULL
    )
);

INSERT INTO attempts_new (
    id, step_id, attempt_number, status, exit_code, session_ids_json,
    failure_summary, started_at, finished_at, created_at
)
SELECT
    id, step_id, attempt_number, status, -1, '[]',
    failure_summary, started_at, finished_at, created_at
FROM attempts;

DROP TABLE attempts;

ALTER TABLE attempts_new RENAME TO attempts;

CREATE INDEX idx_attempts_step ON attempts (step_id, attempt_number);
CREATE INDEX idx_attempts_status ON attempts (status, started_at);

-- verification_results：分离确定性检查结果与 Agent 辅助评估结果。
CREATE TABLE verification_results_new (
    id                       TEXT    PRIMARY KEY NOT NULL,
    task_id                  TEXT    NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    step_id                  TEXT    NOT NULL REFERENCES steps (id) ON DELETE RESTRICT,
    attempt_id               TEXT    NOT NULL REFERENCES attempts (id) ON DELETE RESTRICT,
    validator_session_id     TEXT    REFERENCES sessions (id) ON DELETE RESTRICT,
    passed                   INTEGER NOT NULL CHECK (passed IN (0, 1)),
    verdict                  TEXT    NOT NULL CHECK (
        verdict IN ('passed', 'failed', 'inconclusive')
    ),
    summary                  TEXT,
    checks_json              TEXT    NOT NULL CHECK (json_valid(checks_json)),
    command_results_json     TEXT    NOT NULL CHECK (json_valid(command_results_json)),
    assessment_json          TEXT    CHECK (assessment_json IS NULL OR json_valid(assessment_json)),
    review_skipped           INTEGER NOT NULL DEFAULT 0 CHECK (review_skipped IN (0, 1)),
    failure_summary          TEXT,
    created_at               INTEGER NOT NULL,
    -- passed 必须与判定一致，且失败时必须给出失败摘要。
    CHECK (passed = 0 OR verdict = 'passed'),
    CHECK (verdict = 'passed' OR length(trim(coalesce(failure_summary, ''))) > 0)
);

INSERT INTO verification_results_new (
    id, task_id, step_id, attempt_id, validator_session_id, passed, verdict, summary,
    checks_json, command_results_json, assessment_json, review_skipped,
    failure_summary, created_at
)
SELECT
    id, task_id, step_id, attempt_id, validator_session_id, passed,
    CASE WHEN passed = 1 THEN 'passed' ELSE 'failed' END,
    summary, checks_json, command_results_json, NULL, 0,
    failure_summary, created_at
FROM verification_results;

DROP TABLE verification_results;

ALTER TABLE verification_results_new RENAME TO verification_results;

CREATE INDEX idx_verification_results_attempt
    ON verification_results (attempt_id, created_at);
CREATE INDEX idx_verification_results_step
    ON verification_results (step_id, created_at);
CREATE INDEX idx_verification_results_verdict
    ON verification_results (verdict, created_at);

-- code_saves：记录发起保存的执行尝试，便于从 Attempt 反查保存结果。
ALTER TABLE code_saves ADD COLUMN attempt_id TEXT
    CHECK (attempt_id IS NULL OR length(trim(attempt_id)) > 0);
