-- 0014_validation_runs.sql
-- 增加确定性验证引擎所需的两张表：validation_runs 与 validation_check_results。
--
-- 设计说明：
--   * 状态与结论分离：validation_runs.status 记录验证流程发生了什么
--     （pending / running / completed / failed / cancelled / timed_out），
--     validation_runs.verdict 记录现有证据能否支持通过（passed / failed /
--     inconclusive）。两者不得合并为一个字段，也不得互相推导。
--   * 每次验证创建独立记录，只追加不覆盖：历史结论不会被新的验证改写。
--   * 检查结果独立成表，不塞进单个 JSON 字段：需要按检查标识、状态与验证记录
--     查询与审计，合并成 JSON 会失去查询能力。
--   * 完整输出只保存落盘位置与摘要：大输出由文件承载，数据库只保存路径、状态与
--     摘要，避免撑爆 SQLite 普通字段。
--   * 不声明外键，不为枚举字段声明取值白名单 CHECK：未知取值必须能写入并原样
--     读回，业务校验由 Go 层状态机承担。保留的是非枚举性质的 CHECK（非空、长度、
--     JSON 结构、非负计数、跨字段一致性）。
--   * 不写"由枚举取值派生"的约束（例如"只有 completed 才能是 passed"）：这类
--     规则随状态取值演进而变化，属于业务层状态机职责，由 domain.ValidationRun
--     的 Validate 承担。
--   * 只新增表与索引，不重建既有表：已有数据、索引与触发器原样保留。

CREATE TABLE validation_runs (
    id                    TEXT    PRIMARY KEY NOT NULL,
    task_id               TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    step_id               TEXT    NOT NULL CHECK (length(trim(step_id)) > 0),
    attempt_id            TEXT    NOT NULL CHECK (length(trim(attempt_id)) > 0),
    status                TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    verdict               TEXT,
    summary               TEXT,
    failure_summary       TEXT,
    spec_json             TEXT    NOT NULL CHECK (json_valid(spec_json)),
    spec_snapshot         TEXT    CHECK (spec_snapshot IS NULL OR json_valid(spec_snapshot)),
    total_checks          INTEGER NOT NULL DEFAULT 0 CHECK (total_checks >= 0),
    passed_checks         INTEGER NOT NULL DEFAULT 0 CHECK (passed_checks >= 0),
    failed_checks         INTEGER NOT NULL DEFAULT 0 CHECK (failed_checks >= 0),
    skipped_checks        INTEGER NOT NULL DEFAULT 0 CHECK (skipped_checks >= 0),
    execution_env         TEXT,
    workspace             TEXT,
    container_id          TEXT,
    validator_session_id  TEXT,
    started_at            INTEGER NOT NULL,
    finished_at           INTEGER,
    duration_ms           INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    error_detail          TEXT,
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    -- 统计计数之和不得超过检查总数：它约束的是跨字段一致性，不是枚举取值。
    CHECK (passed_checks + failed_checks + skipped_checks <= total_checks)
);

CREATE INDEX idx_validation_runs_attempt ON validation_runs (attempt_id, created_at);
CREATE INDEX idx_validation_runs_step ON validation_runs (step_id, created_at);
CREATE INDEX idx_validation_runs_task ON validation_runs (task_id, created_at);
CREATE INDEX idx_validation_runs_status ON validation_runs (status, created_at);

CREATE TABLE validation_check_results (
    id                    TEXT    PRIMARY KEY NOT NULL,
    validation_run_id     TEXT    NOT NULL CHECK (length(trim(validation_run_id)) > 0),
    check_id              TEXT    NOT NULL CHECK (length(trim(check_id)) > 0),
    check_name            TEXT    NOT NULL CHECK (length(trim(check_name)) > 0),
    required              INTEGER NOT NULL DEFAULT 0,
    sequence              INTEGER NOT NULL DEFAULT 1 CHECK (sequence > 0),
    definition_json       TEXT    NOT NULL CHECK (json_valid(definition_json)),
    status                TEXT    NOT NULL CHECK (length(trim(status)) > 0),
    exit_code             INTEGER NOT NULL DEFAULT -1,
    stdout_path           TEXT,
    stderr_path           TEXT,
    stdout_summary        TEXT,
    stderr_summary        TEXT,
    output_truncated      INTEGER NOT NULL DEFAULT 0,
    output_archive_error  TEXT,
    timed_out             INTEGER NOT NULL DEFAULT 0,
    cancelled             INTEGER NOT NULL DEFAULT 0,
    certain               INTEGER NOT NULL DEFAULT 0,
    failure_reason        TEXT,
    error_detail          TEXT,
    duration_ms           INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    started_at            INTEGER NOT NULL,
    finished_at           INTEGER NOT NULL,
    created_at            INTEGER NOT NULL,
    -- 同一个验证记录内检查标识唯一：重复写入说明业务层重复执行了同一项检查。
    UNIQUE (validation_run_id, check_id)
);

CREATE INDEX idx_validation_check_results_run
    ON validation_check_results (validation_run_id, sequence);
CREATE INDEX idx_validation_check_results_status
    ON validation_check_results (status, created_at);
