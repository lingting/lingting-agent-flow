-- 代码保存记录：Commit、Push 与 Merge 分别保存结果。
--
-- 约定：
--   * 一条记录对应一次 CodeSaver 调用，提交、推送与合并的状态互不替代；
--   * 提交成功必须有 commit_sha，推送成功必须有 remote_commit_sha，合并成功
--     必须有 merge_sha；
--   * merge_enabled = 0 时合并状态只能是 not_requested：默认不合并主分支；
--   * 不保存任何凭据：repository 只保存脱敏后的仓库地址，error_summary 只保存
--     裁剪后的摘要；
--   * 容器记录可能被删除，因此容器标识只作为可追溯文本，不建立外键。

CREATE TABLE code_saves (
    id                TEXT    PRIMARY KEY NOT NULL,
    project_id        TEXT    CHECK (project_id IS NULL OR length(trim(project_id)) > 0),
    task_id           TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    container_id      TEXT    NOT NULL CHECK (length(trim(container_id)) > 0),
    docker_id         TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    remote_name       TEXT    NOT NULL CHECK (length(trim(remote_name)) > 0),
    repository        TEXT,
    task_branch       TEXT    NOT NULL CHECK (length(trim(task_branch)) > 0),
    base_branch       TEXT    CHECK (base_branch IS NULL OR length(trim(base_branch)) > 0),
    commit_status     TEXT    NOT NULL
        CHECK (commit_status IN (
            'pending', 'not_attempted', 'no_changes', 'succeeded', 'failed', 'unknown'
        )),
    commit_sha        TEXT
        CHECK (commit_sha IS NULL OR length(commit_sha) IN (40, 64)),
    commit_message    TEXT,
    push_status       TEXT    NOT NULL
        CHECK (push_status IN (
            'pending', 'not_attempted', 'succeeded', 'failed', 'conflict', 'unknown'
        )),
    remote_commit_sha TEXT
        CHECK (remote_commit_sha IS NULL OR length(remote_commit_sha) IN (40, 64)),
    merge_enabled     INTEGER NOT NULL DEFAULT 0 CHECK (merge_enabled IN (0, 1)),
    merge_status      TEXT    NOT NULL
        CHECK (merge_status IN (
            'pending', 'not_requested', 'skipped', 'succeeded', 'failed', 'conflict', 'unknown'
        )),
    merge_sha         TEXT
        CHECK (merge_sha IS NULL OR length(merge_sha) IN (40, 64)),
    stage             TEXT,
    error_code        TEXT,
    error_summary     TEXT,
    created_at        INTEGER NOT NULL,
    started_at        INTEGER CHECK (started_at IS NULL OR started_at > 0),
    finished_at       INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    updated_at        INTEGER NOT NULL,
    -- 提交成功必须记录实际 commit SHA。
    CHECK (commit_status <> 'succeeded' OR commit_sha IS NOT NULL),
    -- 推送成功必须记录核验过的远程 commit SHA。
    CHECK (push_status <> 'succeeded' OR remote_commit_sha IS NOT NULL),
    -- 合并成功必须记录合并后的 commit SHA。
    CHECK (merge_status <> 'succeeded' OR merge_sha IS NOT NULL),
    -- 未请求合并时合并状态只能是 not_requested。
    CHECK (merge_enabled = 1 OR merge_status = 'not_requested'),
    -- 任务分支与基础分支不能相同。
    CHECK (base_branch IS NULL OR base_branch <> task_branch)
);

CREATE INDEX idx_code_saves_task ON code_saves (task_id, created_at);

CREATE INDEX idx_code_saves_container ON code_saves (container_id, created_at);

CREATE INDEX idx_code_saves_push_status ON code_saves (push_status, created_at);
