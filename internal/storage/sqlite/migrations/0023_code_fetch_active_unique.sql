-- 0023_code_fetch_active_unique.sql
-- 为尝试级代码获取（code_fetches）增加"同一 Attempt 最多一条活动记录"的唯一约束。
--
-- 设计说明：
--   * 与 0020_task_workspace_initializations.sql 的部分唯一索引同构：任务级工作区
--     初始化已经由数据库阻止"同一任务同一容器并发初始化"，尝试级代码获取此前只有
--     普通索引，重复请求会在数据库里留下多条互不相干的 pending/running 记录。
--   * 终态记录（succeeded / skipped / failed / cancelled / timed_out）不参与唯一性
--     判断，因此同一 Attempt 的历史记录可以保留多条。
--   * uncertain 不是终态，它同样占用唯一性：在只读核验之前不得再次初始化同一
--     Attempt，否则会覆盖现场或在容器内产生第二次 Git 操作。
--   * 只新增索引，不重建表，已有数据、索引与触发器原样保留。
--
-- 升级注意（必须人工确认，不得为通过迁移而删除历史）：
--   * 若库中已存在同一 attempt_id 的多条 pending / running / uncertain 记录，本迁移
--     会因唯一约束冲突而失败。此时迁移事务整体回滚，不会留下部分应用的状态。
--   * 排查语句：
--       SELECT attempt_id, COUNT(*) AS active_count, GROUP_CONCAT(id) AS record_ids
--       FROM code_fetches
--       WHERE status NOT IN ('succeeded', 'skipped', 'failed', 'cancelled', 'timed_out')
--       GROUP BY attempt_id HAVING active_count > 1;
--   * 处置原则：按 created_at 保留最新一条，其余记录由人工核验容器内实际状态后，
--     经 `agentflow code fetch-verify --record <id>` 或直接按现场事实收敛为终态；
--     不得通过删除记录来让迁移通过。

CREATE UNIQUE INDEX idx_code_fetch_active_attempt
    ON code_fetches (attempt_id)
    WHERE status NOT IN ('succeeded', 'skipped', 'failed', 'cancelled', 'timed_out');
