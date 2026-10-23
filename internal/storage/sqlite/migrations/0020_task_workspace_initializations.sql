-- 0020_task_workspace_initializations.sql
-- 增加任务级工作区初始化（容器内代码获取 + 任务分支准备）的执行记录表。
--
-- 背景：code_fetches 是"针对具体 Attempt 的一次初始化"，其 step_id / attempt_id
-- 为必填字段，契约不得破坏。统一任务启动（prepare container → initialize workspace
-- → plan → workflow）发生在任何 Step / Attempt 存在之前，因此它需要一份独立的、
-- 以"任务 + 容器代次"为幂等边界的记录，不能伪造 step_id / attempt_id 去复用
-- code_fetches。
--
-- 设计说明：
--   * 一条记录对应一次针对任务容器的初始化调用；记录只追加不覆盖，因此"该任务在
--     该容器上是否已经初始化成功"可以直接由记录判断，不需要重新探测容器文件系统。
--   * container_id 是容器代次判据：任务重新开始时创建新容器，旧容器上的成功记录
--     不适用于新容器，判定据此重新初始化，历史记录一律保留。
--   * 状态（status）与失败阶段（stage）、错误类别（error_code）分开保存：只记录
--     "失败"无法区分仓库不可访问、目标目录被占用与分支创建失败。
--   * uncertain 表达"远端操作结果无法确认"：它不是终态，必须先做只读核验再收敛，
--     不得据此重放 Clone。
--   * 成功记录必须带 commit_sha：没有可核验 commit 的"成功"无法被后续步骤使用。
--   * 不声明外键，不为 status / source_type / stage / task_branch_source 等枚举式
--     字段声明取值白名单 CHECK：未知取值必须能写入并原样读回，业务校验由 Go 层
--     状态机承担。保留的是非枚举性质的 CHECK（非空、长度、commit SHA 长度、
--     跨字段一致性）。
--   * 不保存任何凭据：repository 只保存脱敏后的地址，error_summary 只保存裁剪后的摘要。
--   * 只新增表与索引，不重建既有表：已有数据、索引与触发器原样保留。

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

-- 按任务、容器与状态查询任务级工作区初始化记录。
CREATE INDEX idx_task_workspace_init_task
    ON task_workspace_initializations (task_id, created_at);
CREATE INDEX idx_task_workspace_init_container
    ON task_workspace_initializations (container_id, created_at);
CREATE INDEX idx_task_workspace_init_status
    ON task_workspace_initializations (status, created_at);

-- 同一任务在同一容器上同一时刻最多一条未结束的初始化记录。
-- 这是"不会为同一任务同一容器启动两次代码获取"的落点：并发初始化由数据库唯一
-- 约束拒绝，而不是只依赖进程内互斥锁。终态记录（succeeded / skipped / failed /
-- cancelled / timed_out）不参与唯一性判断，因此历史记录可以保留多条；
-- uncertain 不是终态，它同样占用唯一性，从而阻止在未核验现场之前重新初始化。
CREATE UNIQUE INDEX idx_task_workspace_init_active
    ON task_workspace_initializations (task_id, container_id)
    WHERE status NOT IN ('succeeded', 'skipped', 'failed', 'cancelled', 'timed_out');
