-- 0010_task_soft_delete.sql
-- 为任务管理 API 增加逻辑删除支持。
--
-- 变更范围：
--   * tasks 新增 deleted_at，标记任务被用户删除的时间；NULL 表示未删除。
--
-- 约束说明：
--   * 只新增列与索引，不重建表，已有数据、索引与触发器原样保留；
--   * 新增列可空，历史记录保持 deleted_at = NULL，语义不变；
--   * deleted_at 上的 CHECK 属于非枚举性质的时间范围校验，按规范保留。

ALTER TABLE tasks ADD COLUMN deleted_at INTEGER
    CHECK (deleted_at IS NULL OR deleted_at > 0);

-- 列表与计数默认只读取未删除的任务，用部分索引避免全表扫描。
CREATE INDEX idx_tasks_active ON tasks (status, created_at) WHERE deleted_at IS NULL;
