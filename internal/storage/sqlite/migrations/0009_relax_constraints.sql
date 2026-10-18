-- 移除数据库级枚举值限制与外键约束。
--
-- 目标：
--   * 枚举式字段不再由 Schema 规定封闭取值集合，未知取值可以保存并原样读回；
--   * 业务表不再声明外键，引用字段（*_id）只作为普通文本保存，允许孤立 ID；
--   * roles 与 execution_hosts 改为逻辑删除，新增 deleted_at 列；
--   * 主键、NOT NULL、唯一约束、非枚举性质的 CHECK 与索引全部保留。
--
-- 做法：SQLite 无法直接删除既有列定义中的 CHECK 或 FOREIGN KEY，因此对全部
-- 业务表执行受控重建（建新表 → 复制数据 → 删除旧表 → 重命名 → 恢复索引与
-- 触发器）。重建语句与版本记录在同一事务内提交，任一步失败都不会留下半成品
-- Schema，也不会丢失原始业务数据。
--
-- 表按引用关系从子到父排列：先重建引用方，再重建被引用方，避免任何一步依赖
-- 连接级外键开关。业务 Schema 本身已不再包含外键，因此也不依赖
-- PRAGMA foreign_keys 的取值。
--
-- 仅移除枚举白名单与布尔 0/1 取值限制；数值范围、长度、JSON 结构与跨字段
-- 一致性等非枚举校验继续保留。

-- events：移除指向 tasks / steps / attempts / sessions 的外键。
CREATE TABLE events_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      TEXT    NOT NULL,
    step_id      TEXT,
    attempt_id   TEXT,
    session_id   TEXT,
    event_type   TEXT    NOT NULL CHECK (length(trim(event_type)) > 0),
    payload_json TEXT    NOT NULL CHECK (json_valid(payload_json)),
    created_at   INTEGER NOT NULL
);

INSERT INTO events_new (id, task_id, step_id, attempt_id, session_id, event_type,
    payload_json, created_at)
SELECT id, task_id, step_id, attempt_id, session_id, event_type, payload_json, created_at
FROM events;

DROP TABLE events;

ALTER TABLE events_new RENAME TO events;

CREATE INDEX idx_events_task ON events (task_id, id);
CREATE INDEX idx_events_step ON events (step_id, id);
CREATE INDEX idx_events_attempt ON events (attempt_id, id);

-- verification_results：移除 passed 与 verdict 的枚举白名单、由枚举派生的
-- 跨字段约束，以及指向 tasks / steps / attempts / sessions 的外键。
CREATE TABLE verification_results_new (
    id                       TEXT    PRIMARY KEY NOT NULL,
    task_id                  TEXT    NOT NULL,
    step_id                  TEXT    NOT NULL,
    attempt_id               TEXT    NOT NULL,
    validator_session_id     TEXT,
    passed                   INTEGER NOT NULL,
    verdict                  TEXT    NOT NULL,
    summary                  TEXT,
    checks_json              TEXT    NOT NULL CHECK (json_valid(checks_json)),
    command_results_json     TEXT    NOT NULL CHECK (json_valid(command_results_json)),
    assessment_json          TEXT    CHECK (assessment_json IS NULL OR json_valid(assessment_json)),
    review_skipped           INTEGER NOT NULL DEFAULT 0,
    failure_summary          TEXT,
    created_at               INTEGER NOT NULL
);

INSERT INTO verification_results_new (id, task_id, step_id, attempt_id,
    validator_session_id, passed, verdict, summary, checks_json, command_results_json,
    assessment_json, review_skipped, failure_summary, created_at)
SELECT id, task_id, step_id, attempt_id, validator_session_id, passed, verdict, summary,
    checks_json, command_results_json, assessment_json, review_skipped,
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

-- sessions：移除 status / run_status / completeness / export_status 的枚举白名单
-- 与指向 tasks / steps / attempts / roles 的外键。
--
-- 保留 CHECK (attempt_id IS NULL OR step_id IS NOT NULL)：它约束的是两个关联字段
-- 之间的组合关系，不是封闭取值集合。
CREATE TABLE sessions_new (
    id                  TEXT    PRIMARY KEY NOT NULL,
    task_id             TEXT    NOT NULL,
    step_id             TEXT,
    attempt_id          TEXT,
    role_id             TEXT    NOT NULL,
    role_snapshot_json  TEXT    NOT NULL CHECK (json_valid(role_snapshot_json)),
    provider            TEXT    NOT NULL CHECK (length(trim(provider)) > 0),
    model               TEXT    NOT NULL CHECK (length(trim(model)) > 0),
    external_session_id TEXT,
    process_id          INTEGER CHECK (process_id IS NULL OR process_id > 0),
    status              TEXT    NOT NULL,
    session_jsonl_path  TEXT
        CHECK (session_jsonl_path IS NULL OR length(trim(session_jsonl_path)) > 0),
    session_html_path   TEXT
        CHECK (session_html_path IS NULL OR length(trim(session_html_path)) > 0),
    run_status          TEXT,
    completeness        TEXT,
    export_status       TEXT,
    exit_code           INTEGER,
    failure_summary     TEXT,
    invocation_id       TEXT    CHECK (invocation_id IS NULL OR length(trim(invocation_id)) > 0),
    container_id        TEXT    CHECK (container_id IS NULL OR length(trim(container_id)) > 0),
    docker_id           TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    started_at          INTEGER NOT NULL,
    finished_at         INTEGER,
    created_at          INTEGER NOT NULL,
    CHECK (attempt_id IS NULL OR step_id IS NOT NULL)
);

INSERT INTO sessions_new (id, task_id, step_id, attempt_id, role_id, role_snapshot_json,
    provider, model, external_session_id, process_id, status, session_jsonl_path,
    session_html_path, run_status, completeness, export_status, exit_code,
    failure_summary, invocation_id, container_id, docker_id, started_at, finished_at,
    created_at)
SELECT id, task_id, step_id, attempt_id, role_id, role_snapshot_json,
    provider, model, external_session_id, process_id, status, session_jsonl_path,
    session_html_path, run_status, completeness, export_status, exit_code,
    failure_summary, invocation_id, container_id, docker_id, started_at, finished_at,
    created_at
FROM sessions;

DROP TABLE sessions;

ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX idx_sessions_task ON sessions (task_id, created_at);
CREATE INDEX idx_sessions_step ON sessions (step_id, created_at);
CREATE INDEX idx_sessions_attempt ON sessions (attempt_id, created_at);
CREATE INDEX idx_sessions_run_status ON sessions (run_status, created_at);
CREATE INDEX idx_sessions_completeness ON sessions (completeness, created_at);
CREATE INDEX idx_sessions_invocation ON sessions (invocation_id)
    WHERE invocation_id IS NOT NULL;
CREATE INDEX idx_sessions_container ON sessions (container_id)
    WHERE container_id IS NOT NULL;

-- attempts：移除 status / verification_verdict 的枚举白名单、布尔 0/1 限制、
-- 由枚举派生的 finished_at 约束，以及指向 steps 的外键。
CREATE TABLE attempts_new (
    id                    TEXT    PRIMARY KEY NOT NULL,
    step_id               TEXT    NOT NULL,
    attempt_number        INTEGER NOT NULL CHECK (attempt_number > 0),
    status                TEXT    NOT NULL,
    container_id          TEXT,
    docker_id             TEXT,
    input_digest          TEXT,
    exit_code             INTEGER NOT NULL DEFAULT -1,
    timed_out             INTEGER NOT NULL DEFAULT 0,
    cancelled             INTEGER NOT NULL DEFAULT 0,
    retry_reason          TEXT,
    error_class           TEXT,
    failure_stage         TEXT,
    failure_summary       TEXT,
    verification_verdict  TEXT,
    verification_summary  TEXT,
    verification_id       TEXT,
    session_ids_json      TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(session_ids_json)),
    code_save_id          TEXT,
    started_at            INTEGER NOT NULL,
    finished_at           INTEGER,
    created_at            INTEGER NOT NULL,
    UNIQUE (step_id, attempt_number)
);

INSERT INTO attempts_new (id, step_id, attempt_number, status, container_id, docker_id,
    input_digest, exit_code, timed_out, cancelled, retry_reason, error_class,
    failure_stage, failure_summary, verification_verdict, verification_summary,
    verification_id, session_ids_json, code_save_id, started_at, finished_at, created_at)
SELECT id, step_id, attempt_number, status, container_id, docker_id,
    input_digest, exit_code, timed_out, cancelled, retry_reason, error_class,
    failure_stage, failure_summary, verification_verdict, verification_summary,
    verification_id, session_ids_json, code_save_id, started_at, finished_at, created_at
FROM attempts;

DROP TABLE attempts;

ALTER TABLE attempts_new RENAME TO attempts;

CREATE INDEX idx_attempts_step ON attempts (step_id, attempt_number);
CREATE INDEX idx_attempts_status ON attempts (status, started_at);

-- steps：移除 step_type / status 的枚举白名单、由 step_type 枚举派生的角色耦合
-- 约束，以及指向 tasks / roles 的外键。
CREATE TABLE steps_new (
    id                     TEXT    PRIMARY KEY NOT NULL,
    task_id                TEXT    NOT NULL,
    sequence               INTEGER NOT NULL CHECK (sequence > 0),
    step_type              TEXT    NOT NULL DEFAULT 'develop',
    title                  TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    objective              TEXT    NOT NULL CHECK (length(trim(objective)) > 0),
    instructions_json      TEXT    NOT NULL CHECK (json_valid(instructions_json)),
    role_id                TEXT,
    role_snapshot_json     TEXT    CHECK (
        role_snapshot_json IS NULL OR json_valid(role_snapshot_json)
    ),
    verification_spec_json TEXT    NOT NULL CHECK (json_valid(verification_spec_json)),
    retry_json             TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(retry_json)),
    max_attempts           INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 10),
    current_attempt_id     TEXT,
    status                 TEXT    NOT NULL,
    result                 TEXT,
    failure_stage          TEXT,
    started_at             INTEGER,
    finished_at            INTEGER,
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL,
    UNIQUE (task_id, sequence)
);

INSERT INTO steps_new (id, task_id, sequence, step_type, title, objective,
    instructions_json, role_id, role_snapshot_json, verification_spec_json, retry_json,
    max_attempts, current_attempt_id, status, result, failure_stage, started_at,
    finished_at, created_at, updated_at)
SELECT id, task_id, sequence, step_type, title, objective,
    instructions_json, role_id, role_snapshot_json, verification_spec_json, retry_json,
    max_attempts, current_attempt_id, status, result, failure_stage, started_at,
    finished_at, created_at, updated_at
FROM steps;

DROP TABLE steps;

ALTER TABLE steps_new RENAME TO steps;

CREATE INDEX idx_steps_task ON steps (task_id, sequence);
CREATE INDEX idx_steps_role ON steps (role_id);
CREATE INDEX idx_steps_status ON steps (status, updated_at);

-- containers：移除 env_type / state / docker_state / end_reason 的枚举白名单、
-- 布尔 0/1 限制、由枚举派生的 detached 与 finished_at 约束，以及指向
-- execution_hosts 的级联外键。
--
-- 保留 CHECK (attempt_id IS NULL OR task_id IS NOT NULL)：它约束关联字段的组合
-- 关系，不是封闭取值集合。
CREATE TABLE containers_new (
    id                  TEXT    PRIMARY KEY NOT NULL,
    host_id             TEXT    NOT NULL,
    docker_id           TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    name                TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    env_type            TEXT    NOT NULL,
    env_name            TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    env_version         TEXT    NOT NULL CHECK (length(trim(env_version)) > 0),
    build_config_sha256 TEXT    NOT NULL
        CHECK (length(build_config_sha256) = 64 AND build_config_sha256 NOT GLOB '*[^0-9a-f]*'),
    image_id            TEXT    NOT NULL
        CHECK (image_id LIKE 'sha256:%' AND length(image_id) = 71),
    task_id             TEXT,
    attempt_id          TEXT,
    state               TEXT    NOT NULL,
    docker_state        TEXT    NOT NULL DEFAULT 'unknown',
    created_at          INTEGER NOT NULL,
    started_at          INTEGER CHECK (started_at IS NULL OR started_at > 0),
    finished_at         INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    end_reason          TEXT,
    detached            INTEGER NOT NULL DEFAULT 0,
    detached_at         INTEGER CHECK (detached_at IS NULL OR detached_at > 0),
    synced_at           INTEGER CHECK (synced_at IS NULL OR synced_at > 0),
    error_summary       TEXT,
    updated_at          INTEGER NOT NULL,
    UNIQUE (host_id, name),
    CHECK (attempt_id IS NULL OR task_id IS NOT NULL)
);

INSERT INTO containers_new (id, host_id, docker_id, name, env_type, env_name,
    env_version, build_config_sha256, image_id, task_id, attempt_id, state,
    docker_state, created_at, started_at, finished_at, end_reason, detached,
    detached_at, synced_at, error_summary, updated_at)
SELECT id, host_id, docker_id, name, env_type, env_name,
    env_version, build_config_sha256, image_id, task_id, attempt_id, state,
    docker_state, created_at, started_at, finished_at, end_reason, detached,
    detached_at, synced_at, error_summary, updated_at
FROM containers;

DROP TABLE containers;

ALTER TABLE containers_new RENAME TO containers;

CREATE INDEX idx_containers_host_state ON containers (host_id, state);
CREATE INDEX idx_containers_task ON containers (task_id) WHERE task_id IS NOT NULL;

-- environment_versions：移除 env_type / build_status 的枚举白名单、由枚举派生的
-- 父版本与镜像 ID 耦合约束，以及指向 execution_hosts 的级联外键。
CREATE TABLE environment_versions_new (
    host_id             TEXT    NOT NULL,
    env_type            TEXT    NOT NULL,
    env_name            TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    version_number      INTEGER NOT NULL CHECK (version_number BETWEEN 1 AND 9999),
    dockerfile_sha256   TEXT    NOT NULL
        CHECK (length(dockerfile_sha256) = 64 AND dockerfile_sha256 NOT GLOB '*[^0-9a-f]*'),
    build_config_sha256 TEXT    NOT NULL
        CHECK (length(build_config_sha256) = 64 AND build_config_sha256 NOT GLOB '*[^0-9a-f]*'),
    context_sha256      TEXT    NOT NULL
        CHECK (length(context_sha256) = 64 AND context_sha256 NOT GLOB '*[^0-9a-f]*'),
    parent_version      TEXT    CHECK (parent_version IS NULL OR length(trim(parent_version)) > 0),
    image_ref           TEXT    NOT NULL CHECK (length(trim(image_ref)) > 0),
    image_id            TEXT
        CHECK (image_id IS NULL OR (image_id LIKE 'sha256:%' AND length(image_id) = 71)),
    build_status        TEXT    NOT NULL,
    error_summary       TEXT,
    created_at          INTEGER NOT NULL,
    built_at            INTEGER CHECK (built_at IS NULL OR built_at > 0),
    PRIMARY KEY (host_id, env_type, env_name, version_number)
);

INSERT INTO environment_versions_new (host_id, env_type, env_name, version_number,
    dockerfile_sha256, build_config_sha256, context_sha256, parent_version,
    image_ref, image_id, build_status, error_summary, created_at, built_at)
SELECT host_id, env_type, env_name, version_number,
    dockerfile_sha256, build_config_sha256, context_sha256, parent_version,
    image_ref, image_id, build_status, error_summary, created_at, built_at
FROM environment_versions;

DROP TABLE environment_versions;

ALTER TABLE environment_versions_new RENAME TO environment_versions;

CREATE INDEX idx_environment_versions_host
    ON environment_versions (host_id, env_type, env_name);

-- tasks：移除 status / final_result 的枚举白名单、布尔 0/1 限制，以及指向
-- requirements / execution_hosts 的外键。
--
-- 保留取消请求、当前步骤与租约相关的跨字段 CHECK：它们约束字段组合关系，
-- 不是封闭取值集合。
CREATE TABLE tasks_new (
    id                   TEXT    PRIMARY KEY NOT NULL,
    requirement_id       TEXT    NOT NULL,
    status               TEXT    NOT NULL,
    host_id              TEXT,
    config_json          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(config_json)),
    repository_path      TEXT    NOT NULL CHECK (length(trim(repository_path)) > 0),
    base_ref             TEXT    NOT NULL CHECK (length(trim(base_ref)) > 0),
    worktree_path        TEXT,
    container_id         TEXT,
    current_step_id      TEXT,
    current_attempt_id   TEXT,
    cancel_requested     INTEGER NOT NULL DEFAULT 0,
    cancel_requested_at  INTEGER CHECK (cancel_requested_at IS NULL OR cancel_requested_at > 0),
    final_result         TEXT,
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
    CHECK (cancel_requested = 0 OR cancel_requested_at IS NOT NULL),
    CHECK (current_attempt_id IS NULL OR current_step_id IS NOT NULL),
    CHECK (
        lease_owner IS NULL
        OR (length(trim(lease_owner)) > 0 AND lease_expires_at IS NOT NULL)
    )
);

INSERT INTO tasks_new (id, requirement_id, status, host_id, config_json,
    repository_path, base_ref, worktree_path, container_id, current_step_id,
    current_attempt_id, cancel_requested, cancel_requested_at, final_result,
    failure_summary, idempotency_key, lease_owner, lease_expires_at, created_at,
    started_at, finished_at, updated_at)
SELECT id, requirement_id, status, host_id, config_json,
    repository_path, base_ref, worktree_path, container_id, current_step_id,
    current_attempt_id, cancel_requested, cancel_requested_at, final_result,
    failure_summary, idempotency_key, lease_owner, lease_expires_at, created_at,
    started_at, finished_at, updated_at
FROM tasks;

DROP TABLE tasks;

ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX idx_tasks_requirement ON tasks (requirement_id, created_at);
CREATE INDEX idx_tasks_status ON tasks (status, created_at);
CREATE INDEX idx_tasks_host ON tasks (host_id) WHERE host_id IS NOT NULL;
CREATE INDEX idx_tasks_container ON tasks (container_id) WHERE container_id IS NOT NULL;
CREATE UNIQUE INDEX idx_tasks_idempotency ON tasks (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- requirements：移除 status 的枚举白名单。
CREATE TABLE requirements_new (
    id          TEXT    PRIMARY KEY NOT NULL,
    title       TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    description TEXT    NOT NULL CHECK (length(trim(description)) > 0),
    priority    INTEGER NOT NULL CHECK (priority >= 0),
    status      TEXT    NOT NULL,
    source      TEXT    NOT NULL CHECK (length(trim(source)) > 0),
    source_id   TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

INSERT INTO requirements_new (id, title, description, priority, status, source,
    source_id, created_at, updated_at)
SELECT id, title, description, priority, status, source, source_id, created_at, updated_at
FROM requirements;

DROP TABLE requirements;

ALTER TABLE requirements_new RENAME TO requirements;

CREATE INDEX idx_requirements_queue ON requirements (status, priority, created_at);

-- roles：移除 type 的枚举白名单与 enabled 的布尔 0/1 限制，并新增 deleted_at
-- 支持逻辑删除。角色被历史步骤或会话引用时不再由数据库阻止删除。
CREATE TABLE roles_new (
    id                TEXT    PRIMARY KEY NOT NULL,
    name              TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    type              TEXT    NOT NULL,
    runner            TEXT    NOT NULL CHECK (length(trim(runner)) > 0),
    model             TEXT    NOT NULL CHECK (length(trim(model)) > 0),
    system_prompt     TEXT    NOT NULL CHECK (length(trim(system_prompt)) > 0),
    tools_policy_json TEXT    NOT NULL CHECK (json_valid(tools_policy_json)),
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    deleted_at        INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0)
);

INSERT INTO roles_new (id, name, type, runner, model, system_prompt, tools_policy_json,
    enabled, created_at, updated_at)
SELECT id, name, type, runner, model, system_prompt, tools_policy_json,
    enabled, created_at, updated_at
FROM roles;

DROP TABLE roles;

ALTER TABLE roles_new RENAME TO roles;

CREATE INDEX idx_roles_type ON roles (type, enabled);

-- execution_hosts：移除 auth_method / last_connect_status 的枚举白名单与
-- is_default / enabled 的布尔 0/1 限制，并新增 deleted_at 支持逻辑删除。
--
-- 保留的约束：端口范围、主机密钥指纹格式、时间戳为正数，以及"私钥认证必须
-- 给出私钥路径"的跨字段触发器；这些都不是封闭取值集合。
CREATE TABLE execution_hosts_new (
    id                   TEXT    PRIMARY KEY NOT NULL,
    name                 TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    host                 TEXT    NOT NULL CHECK (length(trim(host)) > 0),
    port                 INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    username             TEXT    NOT NULL CHECK (length(trim(username)) > 0),
    workspace_root       TEXT    NOT NULL CHECK (length(trim(workspace_root)) > 0),
    is_default           INTEGER NOT NULL DEFAULT 0,
    enabled              INTEGER NOT NULL DEFAULT 1,
    auth_method          TEXT    NOT NULL DEFAULT 'agent',
    private_key_path     TEXT    CHECK (private_key_path IS NULL OR length(trim(private_key_path)) > 0),
    host_key_algorithm   TEXT    CHECK (host_key_algorithm IS NULL OR length(trim(host_key_algorithm)) > 0),
    host_key_fingerprint TEXT    CHECK (
        host_key_fingerprint IS NULL
        OR (length(trim(host_key_fingerprint)) > 0 AND host_key_fingerprint LIKE 'SHA256:%')
    ),
    host_key_trusted_at  INTEGER CHECK (host_key_trusted_at IS NULL OR host_key_trusted_at > 0),
    last_connect_status  TEXT,
    last_connect_error   TEXT    CHECK (last_connect_error IS NULL OR length(trim(last_connect_error)) > 0),
    last_connected_at    INTEGER CHECK (last_connected_at IS NULL OR last_connected_at > 0),
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    deleted_at           INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0)
);

INSERT INTO execution_hosts_new (id, name, host, port, username, workspace_root,
    is_default, enabled, auth_method, private_key_path, host_key_algorithm,
    host_key_fingerprint, host_key_trusted_at, last_connect_status, last_connect_error,
    last_connected_at, created_at, updated_at)
SELECT id, name, host, port, username, workspace_root,
    is_default, enabled, auth_method, private_key_path, host_key_algorithm,
    host_key_fingerprint, host_key_trusted_at, last_connect_status, last_connect_error,
    last_connected_at, created_at, updated_at
FROM execution_hosts;

DROP TABLE execution_hosts;

ALTER TABLE execution_hosts_new RENAME TO execution_hosts;

CREATE UNIQUE INDEX idx_execution_hosts_default ON execution_hosts (is_default)
    WHERE is_default = 1;

CREATE INDEX idx_execution_hosts_name ON execution_hosts (name);

-- 触发器随旧表一起被删除，这里恢复跨字段要求（非枚举白名单、非外键模拟）。
CREATE TRIGGER trg_execution_hosts_private_key_required
BEFORE INSERT ON execution_hosts
WHEN NEW.auth_method = 'private_key'
    AND (NEW.private_key_path IS NULL OR length(trim(NEW.private_key_path)) = 0)
BEGIN
    SELECT RAISE(ABORT, 'private_key_path is required for private_key auth');
END;

CREATE TRIGGER trg_execution_hosts_private_key_required_update
BEFORE UPDATE ON execution_hosts
WHEN NEW.auth_method = 'private_key'
    AND (NEW.private_key_path IS NULL OR length(trim(NEW.private_key_path)) = 0)
BEGIN
    SELECT RAISE(ABORT, 'private_key_path is required for private_key auth');
END;

-- code_saves：移除 commit_status / push_status / merge_status 的枚举白名单、
-- merge_enabled 的布尔 0/1 限制，以及由枚举派生的 SHA 与合并开关耦合约束。
--
-- 保留的约束：标识与分支名非空、SHA 长度、任务分支与基础分支不同。
CREATE TABLE code_saves_new (
    id                TEXT    PRIMARY KEY NOT NULL,
    project_id        TEXT    CHECK (project_id IS NULL OR length(trim(project_id)) > 0),
    task_id           TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    container_id      TEXT    NOT NULL CHECK (length(trim(container_id)) > 0),
    docker_id         TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    attempt_id        TEXT    CHECK (attempt_id IS NULL OR length(trim(attempt_id)) > 0),
    remote_name       TEXT    NOT NULL CHECK (length(trim(remote_name)) > 0),
    repository        TEXT,
    task_branch       TEXT    NOT NULL CHECK (length(trim(task_branch)) > 0),
    base_branch       TEXT    CHECK (base_branch IS NULL OR length(trim(base_branch)) > 0),
    commit_status     TEXT    NOT NULL,
    commit_sha        TEXT
        CHECK (commit_sha IS NULL OR length(commit_sha) IN (40, 64)),
    commit_message    TEXT,
    push_status       TEXT    NOT NULL,
    remote_commit_sha TEXT
        CHECK (remote_commit_sha IS NULL OR length(remote_commit_sha) IN (40, 64)),
    merge_enabled     INTEGER NOT NULL DEFAULT 0,
    merge_status      TEXT    NOT NULL,
    merge_sha         TEXT
        CHECK (merge_sha IS NULL OR length(merge_sha) IN (40, 64)),
    stage             TEXT,
    error_code        TEXT,
    error_summary     TEXT,
    created_at        INTEGER NOT NULL,
    started_at        INTEGER CHECK (started_at IS NULL OR started_at > 0),
    finished_at       INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    updated_at        INTEGER NOT NULL,
    CHECK (base_branch IS NULL OR base_branch <> task_branch)
);

INSERT INTO code_saves_new (id, project_id, task_id, container_id, docker_id, attempt_id,
    remote_name, repository, task_branch, base_branch, commit_status, commit_sha,
    commit_message, push_status, remote_commit_sha, merge_enabled, merge_status,
    merge_sha, stage, error_code, error_summary, created_at, started_at,
    finished_at, updated_at)
SELECT id, project_id, task_id, container_id, docker_id, attempt_id,
    remote_name, repository, task_branch, base_branch, commit_status, commit_sha,
    commit_message, push_status, remote_commit_sha, merge_enabled, merge_status,
    merge_sha, stage, error_code, error_summary, created_at, started_at,
    finished_at, updated_at
FROM code_saves;

DROP TABLE code_saves;

ALTER TABLE code_saves_new RENAME TO code_saves;

CREATE INDEX idx_code_saves_task ON code_saves (task_id, created_at);
CREATE INDEX idx_code_saves_container ON code_saves (container_id, created_at);
CREATE INDEX idx_code_saves_push_status ON code_saves (push_status, created_at);
