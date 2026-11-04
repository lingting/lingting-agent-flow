-- AgentFlow SQLite 最终态 Schema。
--
-- 本文件是数据库结构的唯一事实来源：新数据库首次打开时由 sqlite.Open 在一个事务内
-- 完整执行一次，已初始化的当前 Schema 数据库不会再执行本文件。本程序不提供增量迁移，
-- 也不提供旧数据库升级：结构变化直接修改本文件。
--
-- 约定：
--   * 主键统一为 TEXT，由应用层生成；events 使用自增整数主键保留写入顺序；
--   * 时间统一为 UTC Unix 毫秒时间戳，字段以 _at 结尾；
--   * JSON 统一以 UTF-8 文本保存，并通过 json_valid 约束保证结构合法；
--   * 枚举式字段不设取值白名单：未知取值必须能写入并原样读回，状态规则由
--     internal/domain 的状态机承担；
--   * 不声明外键、不使用级联行为、不用触发器模拟引用完整性：关联 ID 作为普通文本
--     保存并保留索引，引用合法性由应用层校验；
--   * 保留非枚举性质约束：主键、NOT NULL、有业务意义的 UNIQUE、数值范围、长度、
--     JSON 结构与跨字段组合一致性；
--   * 逻辑删除优先：roles、execution_hosts、tasks 与 projects 使用 deleted_at 标记删除，
--     删除操作不级联删除历史记录。

-- user_version 是当前 Schema 的兼容标记，不是迁移版本号：只在完整初始化时写入，
-- 用于区分"当前 Schema"与"不支持的数据库"。不匹配时程序拒绝打开，不执行任何升级。
PRAGMA user_version = 1;

-- ---------------------------------------------------------------------------
-- 角色配置
-- ---------------------------------------------------------------------------

-- roles：运行时角色配置，deleted_at 标记逻辑删除。
CREATE TABLE roles (
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

CREATE INDEX idx_roles_type ON roles (type, enabled);

-- ---------------------------------------------------------------------------
-- 需求
-- ---------------------------------------------------------------------------

-- requirements：用户提交的原始需求。
--
-- priority 以整数排序值保存（0 最高），与 domain.Priority.Rank 一致。
CREATE TABLE requirements (
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

CREATE INDEX idx_requirements_queue ON requirements (status, priority, created_at);

-- ---------------------------------------------------------------------------
-- 执行节点
-- ---------------------------------------------------------------------------

-- execution_hosts：通过 SSH 管理的远程 Linux 机器。
--
--   * 只保存连接目标与非机密认证配置，不保存密码、私钥原文或私钥口令；
--   * workspace_root 遵循 POSIX 路径语义，~ 指远程 SSH 登录用户的 Home；
--   * is_default 通过部分唯一索引保证最多只有一个默认节点；
--   * validation_* 保存环境预检状态与结果，validation_generation 阻止旧结果覆盖新配置；
--   * deleted_at 标记逻辑删除。
CREATE TABLE execution_hosts (
    id                      TEXT    PRIMARY KEY NOT NULL,
    name                    TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    host                    TEXT    NOT NULL CHECK (length(trim(host)) > 0),
    port                    INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    username                TEXT    NOT NULL CHECK (length(trim(username)) > 0),
    workspace_root          TEXT    NOT NULL CHECK (length(trim(workspace_root)) > 0),
    is_default              INTEGER NOT NULL DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    auth_method             TEXT    NOT NULL DEFAULT 'agent',
    private_key_path        TEXT    CHECK (private_key_path IS NULL OR length(trim(private_key_path)) > 0),
    host_key_algorithm      TEXT    CHECK (host_key_algorithm IS NULL OR length(trim(host_key_algorithm)) > 0),
    host_key_fingerprint    TEXT    CHECK (
        host_key_fingerprint IS NULL
        OR (length(trim(host_key_fingerprint)) > 0 AND host_key_fingerprint LIKE 'SHA256:%')
    ),
    host_key_trusted_at     INTEGER CHECK (host_key_trusted_at IS NULL OR host_key_trusted_at > 0),
    last_connect_status     TEXT,
    last_connect_error      TEXT    CHECK (last_connect_error IS NULL OR length(trim(last_connect_error)) > 0),
    last_connected_at       INTEGER CHECK (last_connected_at IS NULL OR last_connected_at > 0),
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    deleted_at              INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0),
    validation_status       TEXT    NOT NULL DEFAULT 'checking',
    validation_message      TEXT,
    validation_details_json TEXT    CHECK (validation_details_json IS NULL OR json_valid(validation_details_json)),
    validation_started_at   INTEGER CHECK (validation_started_at IS NULL OR validation_started_at > 0),
    validation_completed_at INTEGER CHECK (validation_completed_at IS NULL OR validation_completed_at > 0),
    validation_generation   INTEGER NOT NULL DEFAULT 1 CHECK (validation_generation > 0)
);

-- 最多一个默认节点：只对 is_default = 1 的行建立唯一索引。
CREATE UNIQUE INDEX idx_execution_hosts_default ON execution_hosts (is_default)
    WHERE is_default = 1;

CREATE INDEX idx_execution_hosts_name ON execution_hosts (name);

-- 启动时需要找出"仍停留在 checking"的记录并重新调度校验。
CREATE INDEX idx_execution_hosts_validation ON execution_hosts (validation_status)
    WHERE deleted_at IS NULL;

-- 私钥认证必须给出私钥文件路径（跨字段要求，不是枚举白名单）。
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

-- ---------------------------------------------------------------------------
-- 任务
-- ---------------------------------------------------------------------------

-- tasks：需求的一次执行实例。
--
--   * 同一需求可以拥有多次执行记录；历史任务不允许通过删除重建实现重试；
--   * config_json 保存任务配置快照，禁止出现私钥、密码或 Token；
--   * lease_owner / lease_expires_at / lease_epoch 构成执行租约，(owner, epoch) 唯一
--     确定一次领取，旧代次的迟到写入由条件更新拒绝；
--   * deleted_at 标记逻辑删除。
CREATE TABLE tasks (
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
    lease_epoch          INTEGER NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    created_at           INTEGER NOT NULL,
    started_at           INTEGER,
    finished_at          INTEGER,
    updated_at           INTEGER NOT NULL,
    deleted_at           INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0),
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

CREATE INDEX idx_tasks_requirement ON tasks (requirement_id, created_at);
CREATE INDEX idx_tasks_status ON tasks (status, created_at);
CREATE INDEX idx_tasks_host ON tasks (host_id) WHERE host_id IS NOT NULL;
CREATE INDEX idx_tasks_container ON tasks (container_id) WHERE container_id IS NOT NULL;
-- 幂等键在全局唯一：同一客户端请求不会被重复创建为两个正在运行的任务。
CREATE UNIQUE INDEX idx_tasks_idempotency ON tasks (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
-- 列表与计数默认只读取未删除的任务，用部分索引避免全表扫描。
CREATE INDEX idx_tasks_active ON tasks (status, created_at) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- 步骤与执行尝试
-- ---------------------------------------------------------------------------

-- steps：Planner 生成的结构化开发步骤。
--
--   * role_snapshot_json 保存计划生成时的角色配置，避免角色日后修改导致历史不可追溯；
--   * 不调用 Agent 的阶段（prepare / source / verify / save / cleanup）不关联角色，
--     该规则属于业务层职责，不在 Schema 中限制 step_type 取值。
CREATE TABLE steps (
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
    failure_summary        TEXT,
    started_at             INTEGER,
    finished_at            INTEGER,
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL,
    UNIQUE (task_id, sequence)
);

CREATE INDEX idx_steps_task ON steps (task_id, sequence);
CREATE INDEX idx_steps_role ON steps (role_id);
CREATE INDEX idx_steps_status ON steps (status, updated_at);

-- attempts：某个开发步骤的每次完整执行尝试。
--
--   * 失败后保留原有 Attempt，新的修复必须创建新的 Attempt；
--   * budget_scope / budget_scope_id 表达本次尝试算在哪个执行周期里，用于从数据库
--     事实重新计算修复预算；
--   * Git 工作区状态以 JSON 快照保存，快照为空表示本次没有采集，与"采集成功且工作区
--     干净"必须能区分。
CREATE TABLE attempts (
    id                    TEXT    PRIMARY KEY NOT NULL,
    step_id               TEXT    NOT NULL,
    task_id               TEXT    CHECK (task_id IS NULL OR length(trim(task_id)) > 0),
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
    git_branch            TEXT,
    git_before_json       TEXT    CHECK (git_before_json IS NULL OR json_valid(git_before_json)),
    git_after_json        TEXT    CHECK (git_after_json IS NULL OR json_valid(git_after_json)),
    budget_scope          TEXT,
    budget_scope_id       TEXT,
    UNIQUE (step_id, attempt_number)
);

CREATE INDEX idx_attempts_step ON attempts (step_id, attempt_number);
CREATE INDEX idx_attempts_status ON attempts (status, started_at);
-- 按任务与状态查询尝试（例如列出某个任务仍在运行的尝试）。
CREATE INDEX idx_attempts_task ON attempts (task_id, status);
-- 按任务与分支查询执行记录，用于定位某个分支上的改动历史。
CREATE INDEX idx_attempts_git_branch ON attempts (task_id, git_branch);
-- 按周期统计已消耗的尝试数：预算计算的主查询。
CREATE INDEX idx_attempts_budget_scope ON attempts (step_id, budget_scope_id);

-- ---------------------------------------------------------------------------
-- 会话
-- ---------------------------------------------------------------------------

-- sessions：每次具体 Agent 执行会话的元数据。
--
--   * Planner 等任务级会话的 step_id 与 attempt_id 为空；关联 Attempt 的会话必须
--     同时给出 step_id；
--   * 完整 JSONL 内容不写入数据库，只记录本机持久化路径；
--   * container_id / docker_id 只作为可追溯文本，不建立外键：容器记录被删除后会话
--     历史仍需保留；
--   * archive_status / archive_path 与 stdout_path / stderr_path 分别保存原始 Session
--     归档位置与输出日志位置。
CREATE TABLE sessions (
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
    archive_status      TEXT,
    archive_path        TEXT    CHECK (archive_path IS NULL OR length(trim(archive_path)) > 0),
    stdout_path         TEXT    CHECK (stdout_path IS NULL OR length(trim(stdout_path)) > 0),
    stderr_path         TEXT    CHECK (stderr_path IS NULL OR length(trim(stderr_path)) > 0),
    CHECK (attempt_id IS NULL OR step_id IS NOT NULL)
);

CREATE INDEX idx_sessions_task ON sessions (task_id, created_at);
CREATE INDEX idx_sessions_step ON sessions (step_id, created_at);
CREATE INDEX idx_sessions_attempt ON sessions (attempt_id, created_at);
CREATE INDEX idx_sessions_run_status ON sessions (run_status, created_at);
CREATE INDEX idx_sessions_completeness ON sessions (completeness, created_at);
CREATE INDEX idx_sessions_invocation ON sessions (invocation_id)
    WHERE invocation_id IS NOT NULL;
CREATE INDEX idx_sessions_container ON sessions (container_id)
    WHERE container_id IS NOT NULL;
-- 按任务、角色与状态查询会话：Agent 角色以 role_id 表达，角色类型在快照中。
CREATE INDEX idx_sessions_task_role ON sessions (task_id, role_id, status);

-- ---------------------------------------------------------------------------
-- 冻结的遗留表
-- ---------------------------------------------------------------------------

-- verification_results：早期版本的结构化验证结果。
--
-- 该表是**已冻结的遗留数据**：当前生产代码不再读写它，也没有后续写入路径（确定性验证
-- 结论由 validation_runs 与 validation_check_results 承载）。这里只保留最终结构，不得
-- 在业务代码中重新写入或读取，也不得为此表新增强制性约束。
CREATE TABLE verification_results (
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

CREATE INDEX idx_verification_results_attempt
    ON verification_results (attempt_id, created_at);
CREATE INDEX idx_verification_results_step
    ON verification_results (step_id, created_at);
CREATE INDEX idx_verification_results_verdict
    ON verification_results (verdict, created_at);

-- ---------------------------------------------------------------------------
-- 事件
-- ---------------------------------------------------------------------------

-- events：任务工作流中的关键事件，只追加写入。
CREATE TABLE events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id      TEXT    NOT NULL,
    step_id      TEXT,
    attempt_id   TEXT,
    session_id   TEXT,
    event_type   TEXT    NOT NULL CHECK (length(trim(event_type)) > 0),
    payload_json TEXT    NOT NULL CHECK (json_valid(payload_json)),
    created_at   INTEGER NOT NULL
);

-- 任务时间线查询：按任务过滤并按写入顺序返回。
CREATE INDEX idx_events_task ON events (task_id, id);
CREATE INDEX idx_events_step ON events (step_id, id);
CREATE INDEX idx_events_attempt ON events (attempt_id, id);

-- ---------------------------------------------------------------------------
-- 环境版本与受管理容器
-- ---------------------------------------------------------------------------

-- environment_versions：远程 Docker 环境版本登记，只追加不覆盖。
--
--   * 版本号在 (host_id, env_type, env_name) 内唯一，历史版本的哈希与构建配置不允许被
--     新版本修改；
--   * 不保存任何凭据：镜像构建参数与容器环境变量中不得出现 Token、私钥或密码。
CREATE TABLE environment_versions (
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

CREATE INDEX idx_environment_versions_host
    ON environment_versions (host_id, env_type, env_name);

-- containers：受管理容器登记。
--
--   * state 保存管理状态，docker_state 保存最近观测到的 Docker 状态，两者互不替代；
--   * 容器名称在同一节点内唯一，作为远程操作前的二次核验依据；
--   * detached 语义与删除记录的结束时间属于业务层职责，不在 Schema 中按枚举取值限制。
CREATE TABLE containers (
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
    -- 容器名称在同一节点内唯一，避免同名容器被误认为同一实例。
    UNIQUE (host_id, name),
    -- 尝试关联必须同时给出任务关联。
    CHECK (attempt_id IS NULL OR task_id IS NOT NULL)
);

CREATE INDEX idx_containers_host_state ON containers (host_id, state);
CREATE INDEX idx_containers_task ON containers (task_id) WHERE task_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 代码保存与代码初始化记录
-- ---------------------------------------------------------------------------

-- code_saves：一条记录对应一次 CodeSaver 调用，提交、推送与合并的状态互不替代。
--
--   * 不保存任何凭据：repository 只保存脱敏后的仓库地址，error_summary 只保存裁剪摘要；
--   * baseline_* 是执行副作用之前的只读基线，为空表示"没有基线"，此时不得推断；
--   * persist_status 为空视为 persisted，使历史记录仍可读且不被误判为不可信；
--   * SHA 长度约束区分 SHA-1 与 SHA-256。
CREATE TABLE code_saves (
    id                       TEXT    PRIMARY KEY NOT NULL,
    project_id               TEXT    CHECK (project_id IS NULL OR length(trim(project_id)) > 0),
    task_id                  TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    container_id             TEXT    NOT NULL CHECK (length(trim(container_id)) > 0),
    docker_id                TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    attempt_id               TEXT    CHECK (attempt_id IS NULL OR length(trim(attempt_id)) > 0),
    remote_name              TEXT    NOT NULL CHECK (length(trim(remote_name)) > 0),
    repository               TEXT,
    task_branch              TEXT    NOT NULL CHECK (length(trim(task_branch)) > 0),
    base_branch              TEXT    CHECK (base_branch IS NULL OR length(trim(base_branch)) > 0),
    commit_status            TEXT    NOT NULL,
    commit_sha               TEXT
        CHECK (commit_sha IS NULL OR length(commit_sha) IN (40, 64)),
    commit_message           TEXT,
    push_status              TEXT    NOT NULL,
    remote_commit_sha        TEXT
        CHECK (remote_commit_sha IS NULL OR length(remote_commit_sha) IN (40, 64)),
    merge_enabled            INTEGER NOT NULL DEFAULT 0,
    merge_status             TEXT    NOT NULL,
    merge_sha                TEXT
        CHECK (merge_sha IS NULL OR length(merge_sha) IN (40, 64)),
    stage                    TEXT,
    error_code               TEXT,
    error_summary            TEXT,
    created_at               INTEGER NOT NULL,
    started_at               INTEGER CHECK (started_at IS NULL OR started_at > 0),
    finished_at              INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    updated_at               INTEGER NOT NULL,
    baseline_commit_sha      TEXT
        CHECK (baseline_commit_sha IS NULL OR length(baseline_commit_sha) IN (40, 64)),
    baseline_base_commit_sha TEXT
        CHECK (baseline_base_commit_sha IS NULL OR length(baseline_base_commit_sha) IN (40, 64)),
    persist_status           TEXT,
    persist_error            TEXT,
    recovery_status          TEXT,
    -- 任务分支与基础分支不能相同。
    CHECK (base_branch IS NULL OR base_branch <> task_branch)
);

CREATE INDEX idx_code_saves_task ON code_saves (task_id, created_at);
CREATE INDEX idx_code_saves_container ON code_saves (container_id, created_at);
CREATE INDEX idx_code_saves_push_status ON code_saves (push_status, created_at);

-- code_fetches：针对具体 Attempt 的一次代码初始化（代码获取 + 任务分支准备）。
--
--   * 记录只追加不覆盖，"同一 Attempt 已初始化成功"由记录直接判断；
--   * 状态、失败阶段与错误类别分开保存，成功记录必须带可核验的 commit SHA。
CREATE TABLE code_fetches (
    id                  TEXT    PRIMARY KEY NOT NULL,
    project_id          TEXT    CHECK (project_id IS NULL OR length(trim(project_id)) > 0),
    task_id             TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    step_id             TEXT    NOT NULL CHECK (length(trim(step_id)) > 0),
    attempt_id          TEXT    NOT NULL CHECK (length(trim(attempt_id)) > 0),
    source_type         TEXT    NOT NULL CHECK (length(trim(source_type)) > 0),
    repository          TEXT,
    work_dir            TEXT    NOT NULL CHECK (length(trim(work_dir)) > 0),
    container_id        TEXT    NOT NULL CHECK (length(trim(container_id)) > 0),
    docker_id           TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    base_branch         TEXT,
    base_selection      TEXT,
    base_commit         TEXT,
    task_branch         TEXT    NOT NULL CHECK (length(trim(task_branch)) > 0),
    task_branch_source  TEXT,
    commit_sha          TEXT,
    status              TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    stage               TEXT,
    error_code          TEXT,
    error_summary       TEXT,
    started_at          INTEGER NOT NULL,
    finished_at         INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    -- 基础分支与任务分支不能相同：任务分支必须建立在其他分支之上。
    CHECK (base_branch IS NULL OR base_branch <> task_branch),
    -- commit SHA 只允许 40 位（SHA-1）或 64 位（SHA-256）小写十六进制。
    CHECK (base_commit IS NULL OR length(base_commit) IN (40, 64)),
    CHECK (commit_sha IS NULL OR length(commit_sha) IN (40, 64)),
    -- 成功记录必须带可核验的 commit：这是"工作区确实就绪"的唯一证据。
    CHECK (status NOT IN ('succeeded', 'skipped') OR commit_sha IS NOT NULL)
);

CREATE INDEX idx_code_fetches_task ON code_fetches (task_id, created_at);
CREATE INDEX idx_code_fetches_attempt ON code_fetches (attempt_id, created_at);
CREATE INDEX idx_code_fetches_container ON code_fetches (container_id, created_at);
CREATE INDEX idx_code_fetches_status ON code_fetches (status, created_at);

-- 同一 Attempt 同一时刻最多一条未结束的代码初始化记录；uncertain 不是终态，同样占用
-- 唯一性，从而阻止在未核验现场之前重新初始化。
CREATE UNIQUE INDEX idx_code_fetch_active_attempt
    ON code_fetches (attempt_id)
    WHERE status NOT IN ('succeeded', 'skipped', 'failed', 'cancelled', 'timed_out');

-- task_workspace_initializations：任务级工作区初始化记录。
--
-- 统一任务启动发生在任何 Step / Attempt 存在之前，因此以"任务 + 容器代次"为幂等边界，
-- 不复用 code_fetches。
CREATE TABLE task_workspace_initializations (
    id                  TEXT    PRIMARY KEY NOT NULL,
    task_id             TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    container_id        TEXT    NOT NULL CHECK (length(trim(container_id)) > 0),
    docker_id           TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    source_type         TEXT    NOT NULL CHECK (length(trim(source_type)) > 0),
    repository          TEXT,
    work_dir            TEXT    NOT NULL CHECK (length(trim(work_dir)) > 0),
    base_branch         TEXT,
    base_selection      TEXT,
    base_commit         TEXT,
    task_branch         TEXT    NOT NULL CHECK (length(trim(task_branch)) > 0),
    task_branch_source  TEXT,
    commit_sha          TEXT,
    status              TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    stage               TEXT,
    error_code          TEXT,
    error_summary       TEXT,
    started_at          INTEGER NOT NULL,
    finished_at         INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    -- 基础分支与任务分支不能相同：任务分支必须建立在其他分支之上。
    CHECK (base_branch IS NULL OR base_branch <> task_branch),
    -- commit SHA 只允许 40 位（SHA-1）或 64 位（SHA-256）小写十六进制。
    CHECK (base_commit IS NULL OR length(base_commit) IN (40, 64)),
    CHECK (commit_sha IS NULL OR length(commit_sha) IN (40, 64)),
    -- 成功记录必须带可核验的 commit：这是"工作区确实就绪"的唯一证据。
    CHECK (status NOT IN ('succeeded', 'skipped') OR commit_sha IS NOT NULL)
);

CREATE INDEX idx_task_workspace_init_task
    ON task_workspace_initializations (task_id, created_at);
CREATE INDEX idx_task_workspace_init_container
    ON task_workspace_initializations (container_id, created_at);
CREATE INDEX idx_task_workspace_init_status
    ON task_workspace_initializations (status, created_at);

-- 同一任务在同一容器上同一时刻最多一条未结束的初始化记录；uncertain 不是终态，同样
-- 占用唯一性。
CREATE UNIQUE INDEX idx_task_workspace_init_active
    ON task_workspace_initializations (task_id, container_id)
    WHERE status NOT IN ('succeeded', 'skipped', 'failed', 'cancelled', 'timed_out');

-- ---------------------------------------------------------------------------
-- 确定性验证
-- ---------------------------------------------------------------------------

-- validation_runs：一次确定性验证的执行记录，只追加不覆盖。
--
--   * status 记录验证流程发生了什么，verdict 记录现有证据能否支持通过，两者不互相推导；
--   * review_status / review_summary / assessment_json 承载 Validator Agent 的语义审查
--     结果，与确定性结论互相独立；
--   * 完整输出只保存落盘位置与摘要，大输出由文件承载。
CREATE TABLE validation_runs (
    id                   TEXT    PRIMARY KEY NOT NULL,
    task_id              TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    step_id              TEXT    NOT NULL CHECK (length(trim(step_id)) > 0),
    attempt_id           TEXT    NOT NULL CHECK (length(trim(attempt_id)) > 0),
    status               TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    verdict              TEXT,
    summary              TEXT,
    failure_summary      TEXT,
    spec_json            TEXT    NOT NULL CHECK (json_valid(spec_json)),
    spec_snapshot        TEXT    CHECK (spec_snapshot IS NULL OR json_valid(spec_snapshot)),
    total_checks         INTEGER NOT NULL DEFAULT 0 CHECK (total_checks >= 0),
    passed_checks        INTEGER NOT NULL DEFAULT 0 CHECK (passed_checks >= 0),
    failed_checks        INTEGER NOT NULL DEFAULT 0 CHECK (failed_checks >= 0),
    skipped_checks       INTEGER NOT NULL DEFAULT 0 CHECK (skipped_checks >= 0),
    execution_env        TEXT,
    workspace            TEXT,
    container_id         TEXT,
    validator_session_id TEXT,
    started_at           INTEGER NOT NULL,
    finished_at          INTEGER,
    duration_ms          INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    error_detail         TEXT,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    review_status        TEXT,
    review_summary       TEXT,
    assessment_json      TEXT    CHECK (assessment_json IS NULL OR json_valid(assessment_json)),
    -- 统计计数之和不得超过检查总数：它约束的是跨字段一致性，不是枚举取值。
    CHECK (passed_checks + failed_checks + skipped_checks <= total_checks)
);

CREATE INDEX idx_validation_runs_attempt ON validation_runs (attempt_id, created_at);
CREATE INDEX idx_validation_runs_step ON validation_runs (step_id, created_at);
CREATE INDEX idx_validation_runs_task ON validation_runs (task_id, created_at);
CREATE INDEX idx_validation_runs_status ON validation_runs (status, created_at);
CREATE INDEX idx_validation_runs_review_status
    ON validation_runs (review_status, created_at);

-- validation_check_results：验证运行内逐项检查结果。
--
-- 检查结果独立成表而不是塞进单个 JSON 字段：需要按检查标识、状态与验证记录查询与审计。
CREATE TABLE validation_check_results (
    id                   TEXT    PRIMARY KEY NOT NULL,
    validation_run_id    TEXT    NOT NULL CHECK (length(trim(validation_run_id)) > 0),
    check_id             TEXT    NOT NULL CHECK (length(trim(check_id)) > 0),
    check_name           TEXT    NOT NULL CHECK (length(trim(check_name)) > 0),
    required             INTEGER NOT NULL DEFAULT 0,
    sequence             INTEGER NOT NULL DEFAULT 1 CHECK (sequence > 0),
    definition_json      TEXT    NOT NULL CHECK (json_valid(definition_json)),
    status               TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    exit_code            INTEGER NOT NULL DEFAULT -1,
    stdout_path          TEXT,
    stderr_path          TEXT,
    stdout_summary       TEXT,
    stderr_summary       TEXT,
    output_truncated     INTEGER NOT NULL DEFAULT 0,
    output_archive_error TEXT,
    timed_out            INTEGER NOT NULL DEFAULT 0,
    cancelled            INTEGER NOT NULL DEFAULT 0,
    certain              INTEGER NOT NULL DEFAULT 0,
    failure_reason       TEXT,
    error_detail         TEXT,
    duration_ms          INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    started_at           INTEGER NOT NULL,
    finished_at          INTEGER NOT NULL,
    created_at           INTEGER NOT NULL,
    -- 同一个验证记录内检查标识唯一：重复写入说明业务层重复执行了同一项检查。
    UNIQUE (validation_run_id, check_id)
);

CREATE INDEX idx_validation_check_results_run
    ON validation_check_results (validation_run_id, sequence);
CREATE INDEX idx_validation_check_results_status
    ON validation_check_results (status, created_at);

-- validation_feedbacks：验证失败时可复用的结构化反馈，供下一次 Developer 执行引用。
--
-- 反馈只描述上一轮发生了什么，不能被当作当前验证结论；同一轮验证最多一条反馈。
CREATE TABLE validation_feedbacks (
    id                     TEXT    PRIMARY KEY NOT NULL,
    task_id                TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    step_id                TEXT    NOT NULL CHECK (length(trim(step_id)) > 0),
    developer_attempt_id   TEXT    NOT NULL CHECK (length(trim(developer_attempt_id)) > 0),
    validation_run_id      TEXT    NOT NULL CHECK (length(trim(validation_run_id)) > 0),
    verdict                TEXT    NOT NULL CHECK (length(trim(verdict)) > 0),
    summary                TEXT,
    infrastructure_error   INTEGER NOT NULL DEFAULT 0,
    checks_json            TEXT    NOT NULL CHECK (json_valid(checks_json)),
    created_at             INTEGER NOT NULL,
    -- 同一轮验证最多一条反馈：它约束的是跨字段一致性，不是枚举取值。
    UNIQUE (validation_run_id)
);

CREATE INDEX idx_validation_feedbacks_step
    ON validation_feedbacks (step_id, created_at);
CREATE INDEX idx_validation_feedbacks_attempt
    ON validation_feedbacks (developer_attempt_id, created_at);
CREATE INDEX idx_validation_feedbacks_task
    ON validation_feedbacks (task_id, created_at);

-- ---------------------------------------------------------------------------
-- 工作流编排与修复周期
-- ---------------------------------------------------------------------------

-- workflow_runs：任务级工作流编排的执行记录。
--
--   * tasks.status 表达任务的业务阶段，workflow_runs.status 表达"这一次编排执行是否在
--     推进"，两者不能互相替代；
--   * 当前游标是恢复的唯一依据，plan_digest 用于判断计划是否已重新生成；
--   * worker_id + lease_expires_at 识别"进程已退出但记录仍停留在 running"的残留；
--   * revision 是乐观并发版本号。
CREATE TABLE workflow_runs (
    id                      TEXT    PRIMARY KEY NOT NULL,
    task_id                 TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    plan_digest             TEXT    NOT NULL CHECK (length(trim(plan_digest)) > 0),
    status                  TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    stage                   TEXT    NOT NULL CHECK (length(trim(stage)) > 0),
    current_step_id         TEXT,
    current_attempt_id      TEXT,
    last_completed_step_id  TEXT,
    last_completed_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_completed_sequence >= 0),
    completed_steps         INTEGER NOT NULL DEFAULT 0 CHECK (completed_steps >= 0),
    total_steps             INTEGER NOT NULL DEFAULT 0 CHECK (total_steps >= 0),
    cancel_requested        INTEGER NOT NULL DEFAULT 0,
    cancel_requested_at     INTEGER,
    worker_id               TEXT,
    lease_expires_at        INTEGER,
    error_class             TEXT,
    error_summary           TEXT,
    revision                INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    started_at              INTEGER,
    finished_at             INTEGER,
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    -- 跨字段一致性：以下约束描述字段组合，不是枚举取值白名单。
    CHECK (completed_steps <= total_steps),
    CHECK (current_attempt_id IS NULL OR current_step_id IS NOT NULL),
    CHECK (cancel_requested = 0 OR cancel_requested_at IS NOT NULL),
    CHECK (worker_id IS NULL OR lease_expires_at IS NOT NULL)
);

-- 同一任务同一时刻最多一条未结束的执行记录：并发启动由唯一约束拒绝，而不是只依赖
-- 进程内互斥锁。终态记录不参与唯一性判断，历史记录可以保留多条。
CREATE UNIQUE INDEX idx_workflow_runs_active_task
    ON workflow_runs (task_id)
    WHERE status NOT IN ('completed', 'cancelled', 'failed');

CREATE INDEX idx_workflow_runs_task
    ON workflow_runs (task_id, created_at);
CREATE INDEX idx_workflow_runs_status
    ON workflow_runs (status, updated_at);
CREATE INDEX idx_workflow_runs_step
    ON workflow_runs (current_step_id);

-- repair_cycles：一次逻辑开发执行周期，是修复预算的持久化依据。
--
--   * max_attempts 是周期开启时的上限快照：之后修改步骤配置不会让已开启的周期获得
--     更多预算；
--   * attempts.budget_scope / budget_scope_id 表达这一次尝试算在哪个周期里。
CREATE TABLE repair_cycles (
    id           TEXT    PRIMARY KEY NOT NULL,
    task_id      TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    step_id      TEXT    NOT NULL CHECK (length(trim(step_id)) > 0),
    sequence     INTEGER NOT NULL CHECK (sequence >= 1),
    max_attempts INTEGER NOT NULL CHECK (max_attempts >= 1),
    opened_by    TEXT    NOT NULL CHECK (length(trim(opened_by)) > 0),
    opened_at    INTEGER NOT NULL,
    closed_at    INTEGER,
    -- 跨字段一致性：周期结束时间不能早于开启时间。
    CHECK (closed_at IS NULL OR closed_at >= opened_at)
);

-- 步骤内的周期序号唯一：并发开启同一周期会被数据库拒绝。
CREATE UNIQUE INDEX idx_repair_cycles_step_sequence
    ON repair_cycles (step_id, sequence);

-- 同一步骤同一时刻最多一个活动周期。
CREATE UNIQUE INDEX idx_repair_cycles_active_step
    ON repair_cycles (step_id)
    WHERE closed_at IS NULL;

CREATE INDEX idx_repair_cycles_task
    ON repair_cycles (task_id, opened_at);

-- ---------------------------------------------------------------------------
-- 项目登记
-- ---------------------------------------------------------------------------

-- projects：项目登记表，保存"同一份代码 + 同一套执行环境"的最小事实。
--
--   * 项目标识由调用方给出并作为主键，同时成为远程目录名，写入前由领域层校验；
--   * 仓库地址只保存已脱敏的形式，不保存任何 Token、私钥或密码；
--   * env_version 为空表示使用当前可用版本；
--   * caches_json 保存缓存类型名称数组，只做 JSON 结构校验；
--   * default_host_id 可空表示"未指定默认执行节点"，为空的引用不建立索引项；
--   * deleted_at 标记逻辑删除，不级联改写历史任务与代码保存记录上的 project_id。
CREATE TABLE projects (
    id              TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    name            TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    repository_url  TEXT    NOT NULL CHECK (length(trim(repository_url)) > 0),
    base_branch     TEXT    NOT NULL CHECK (length(trim(base_branch)) > 0),
    env_type        TEXT    NOT NULL CHECK (length(trim(env_type)) > 0),
    env_name        TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    env_version     TEXT    CHECK (env_version IS NULL OR length(trim(env_version)) > 0),
    work_dir        TEXT    NOT NULL CHECK (length(trim(work_dir)) > 0),
    caches_json     TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(caches_json)),
    created_at      INTEGER NOT NULL CHECK (created_at > 0),
    updated_at      INTEGER NOT NULL CHECK (updated_at > 0),
    deleted_at      INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0),
    default_host_id TEXT
);

-- 列表默认按创建时间升序返回未删除记录，删除标记参与过滤。
CREATE INDEX idx_projects_created ON projects (deleted_at, created_at, id);

-- 只为非空值建立部分索引：NULL 表示"未设置"，不会参与按默认节点查询。
CREATE INDEX idx_projects_default_host ON projects (default_host_id)
    WHERE default_host_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 规划会话
-- ---------------------------------------------------------------------------

-- planning_sessions：需求规划会话的生命周期与运行环境关联。
--
--   * 规划阶段不产生正式任务，因此不复用 steps / attempts / sessions 表；
--   * status 是枚举式字段，不设取值白名单，状态迁移规则由 internal/domain 承担；
--   * host_id / container_id / adopted_revision_id 等关联标识允许指向已不存在的记录，
--     关系合法性由应用层校验；
--   * 逻辑删除不适用于规划会话：结束后保留消息与版本历史。
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

-- 项目下的会话列表按 project_id 过滤、按创建时间倒序读取。
CREATE INDEX idx_planning_sessions_project
    ON planning_sessions (project_id, status, created_at);

-- 按状态定位未结束的会话：服务重启后只处理仍可能占用规划容器的记录。
CREATE INDEX idx_planning_sessions_status
    ON planning_sessions (status, created_at);

-- planning_messages：规划会话的多轮消息，只追加不覆盖。
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

-- planning_revisions：每一轮规划的完整快照。
--
-- 需求分析、实施方案与校验结果都作为 JSON 文档保存，只做 json_valid 结构约束，不为
-- JSON 内部字段建列、不设字段白名单。
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

-- planning_session_leases：规划会话的跨进程执行租约。
--
--   * planning_session_id 是主键，因此"每个规划会话最多一个持有者"由主键约束保证；
--   * epoch 每次成功领取时原子递增，(owner, epoch) 唯一确定一次领取；
--   * 释放时不删行，只清空 owner / operation / lease_expires_at 并保留 epoch；
--   * operation 只是诊断与审计分类，不设取值白名单；
--   * 组合一致性：持有中必须同时具备 owner、operation 与到期时间，释放后三者同时为空。
CREATE TABLE planning_session_leases (
    planning_session_id TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(planning_session_id)) > 0),
    owner               TEXT,
    operation           TEXT,
    epoch               INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    lease_expires_at    INTEGER,
    updated_at          INTEGER NOT NULL CHECK (updated_at > 0),
    CHECK (
        (owner IS NULL AND operation IS NULL AND lease_expires_at IS NULL)
        OR (
            owner IS NOT NULL AND length(trim(owner)) > 0
            AND operation IS NOT NULL AND length(trim(operation)) > 0
            AND lease_expires_at IS NOT NULL AND lease_expires_at > 0
        )
    )
);

-- 持有中的租约按到期时间检索：过期即可被接管，已释放的行不进入索引。
CREATE INDEX idx_planning_session_leases_expiry
    ON planning_session_leases (lease_expires_at)
    WHERE owner IS NOT NULL;
