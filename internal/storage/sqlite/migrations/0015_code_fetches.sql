-- 0015_code_fetches.sql
-- 增加代码初始化（代码获取 + 任务分支准备）的执行记录表 code_fetches。
--
-- 设计说明：
--   * 一条记录对应一次针对具体 Attempt 的代码初始化调用，记录只追加不覆盖：
--     "同一 Attempt 已初始化成功"因此可以直接由记录判断，不需要重新探测文件系统，
--     也不会因为重复请求而重新 Clone 覆盖已有开发成果。
--   * 状态（status）与失败阶段（stage）、错误类别（error_code）分开保存：
--     只记录"失败"无法区分仓库不可访问、目标目录被占用与分支创建失败。
--   * 成功记录必须带 commit_sha：没有可核验 commit 的"成功"无法被后续步骤使用。
--   * 不声明外键，不为 status / source_type / stage / task_branch_source 等枚举式字段
--     声明取值白名单 CHECK：未知取值必须能写入并原样读回，业务校验由 Go 层状态机承担。
--     保留的是非枚举性质的 CHECK（非空、长度、commit SHA 长度、跨字段一致性）。
--   * 不保存任何凭据：repository 只保存脱敏后的地址，error_summary 只保存裁剪后的摘要。
--   * 只新增表与索引，不重建既有表：已有数据、索引与触发器原样保留。

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

-- 按任务、尝试与容器查询代码初始化记录。
CREATE INDEX idx_code_fetches_task ON code_fetches (task_id, created_at);
CREATE INDEX idx_code_fetches_attempt ON code_fetches (attempt_id, created_at);
CREATE INDEX idx_code_fetches_container ON code_fetches (container_id, created_at);
CREATE INDEX idx_code_fetches_status ON code_fetches (status, created_at);
