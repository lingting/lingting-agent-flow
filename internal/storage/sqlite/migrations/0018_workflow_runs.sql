-- 0018_workflow_runs.sql
-- 为任务级工作流编排增加执行记录。
--
-- 设计说明：
--   * 任务状态（tasks.status）表达"任务的业务阶段"，workflow_runs.status 表达
--     "这一次编排执行是否在推进"。两者是不同层次的概念，不能互相替代，也不能把
--     阻塞、取消请求、需要人工核验等编排语义塞进同一个任务字段。
--   * 同一条任务允许多次执行记录（失败后重新开始、阻塞后恢复）：记录只追加，
--     恢复会复用未结束的记录并推进其游标，历史记录一律保留。
--   * plan_digest 保存本次执行所用计划的内容摘要。恢复时必须与当前步骤计划一致，
--     否则说明计划已被重新生成，不能沿用旧游标继续推进。
--   * 当前游标（current_step_id / current_attempt_id / last_completed_step_id /
--     last_completed_sequence）是恢复的唯一依据，不依赖进程内状态。
--   * worker_id + lease_expires_at 用于识别"进程已退出但记录仍停留在 running"的
--     残留：只有租约过期后其他进程才允许接管，不会出现两个 Worker 同时推进。
--   * revision 是乐观并发版本号：恢复不得覆盖更新的状态。
--   * 不声明外键，不为枚举式字段（status、stage、error_class）声明取值白名单
--     CHECK：未知取值必须能写入并原样读回，业务校验由 Go 层状态机承担。
--     保留的是非枚举性质的 CHECK（非空、数值范围、跨字段组合一致性、JSON 结构）。
--   * 只新增表与索引，不重建既有表：已有数据、索引与触发器原样保留。

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

-- 同一任务同一时刻最多一条未结束的执行记录。
-- 这是"不会为同一任务启动两次工作流"的落点：并发启动由数据库唯一约束拒绝，
-- 而不是只依赖进程内互斥锁。终态记录（completed / cancelled / failed）不参与
-- 唯一性判断，因此历史记录可以保留多条。
CREATE UNIQUE INDEX idx_workflow_runs_active_task
    ON workflow_runs (task_id)
    WHERE status NOT IN ('completed', 'cancelled', 'failed');

CREATE INDEX idx_workflow_runs_task
    ON workflow_runs (task_id, created_at);

CREATE INDEX idx_workflow_runs_status
    ON workflow_runs (status, updated_at);

CREATE INDEX idx_workflow_runs_step
    ON workflow_runs (current_step_id);
