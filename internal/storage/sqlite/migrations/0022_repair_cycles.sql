-- 0022_repair_cycles.sql
-- 为开发执行周期与修复预算增加持久化依据。
--
-- 设计说明：
--   * 修复预算不能只依赖进程内计数：一次闭环调用内的 attempts 变量在进程退出后
--     消失，重启后的恢复与重复调用会重新得到"还有余额"的结论，使 MaxAttempts
--     形同虚设。因此预算必须能从数据库事实重新计算。
--   * repair_cycles 登记一次逻辑开发执行周期：task_id / step_id 表达归属，
--     sequence 是步骤内的周期序号，max_attempts 是周期开启时的上限快照。
--     快照而不是引用：之后修改步骤配置不会让已开启的周期获得更多预算。
--   * attempts 新增 budget_scope / budget_scope_id 两列，表达"这一次尝试算在
--     哪一个执行周期里"。同一逻辑周期的恢复（进程重启、HTTP 重试、workflow
--     resume）复用同一个标识，只有用户显式开启的新周期才写入新标识。
--   * 两列为 NULL 的历史记录表示周期归属无法从持久化事实确定。这类记录不得被
--     自动归入当前周期（会让旧执行凭空消耗新预算），也不得被忽略（会让预算
--     无限重置）：领域判定 domain.DecideRepairStart 会拒绝在其之上继续执行，
--     需要用户显式核验。迁移本身不回填、不改写历史记录。
--   * 同一任务内步骤执行周期序号唯一：序号由应用层在事务内分配，唯一约束是
--     并发写入的最后防线。
--   * 同一步骤同一时刻最多一个活动周期：由部分唯一索引保证，因此"哪一个是当前
--     周期"不需要应用层猜测。
--   * 不声明外键，不为枚举式字段（budget_scope、opened_by）声明取值白名单
--     CHECK：未知取值必须能写入并原样读回，业务校验由 Go 层承担。保留的是非
--     枚举性质的 CHECK（非空、数值范围、跨字段组合一致性）。
--   * 只新增表、列与索引，不重建既有表：已有数据、索引与触发器原样保留。

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

ALTER TABLE attempts ADD COLUMN budget_scope TEXT;

ALTER TABLE attempts ADD COLUMN budget_scope_id TEXT;

-- 按周期统计已消耗的尝试数：预算计算的主查询。
CREATE INDEX idx_attempts_budget_scope
    ON attempts (step_id, budget_scope_id);
