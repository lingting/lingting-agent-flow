-- 0012_execution_records.sql
-- 补齐 Task → Step → Attempt → Session 四层执行记录模型所需的字段与索引。
--
-- 变更范围：
--   * attempts.task_id          执行尝试的任务归属，写入时由服务层校验与步骤一致
--   * steps.failure_summary     步骤失败原因摘要，与 failure_stage（失败阶段）分开保存
--   * sessions.archive_status   原始 Session 文件的归档状态
--   * sessions.archive_path     原始 Session 文件的归档位置
--
-- 约束说明：
--   * 只新增列与索引，不重建表：已有数据、索引、触发器与其他列定义原样保留；
--   * 不声明外键，不为任何新增列声明枚举白名单 CHECK，未知取值必须能写入并原样读回；
--   * attempts.task_id 允许为 NULL，仅为兼容既有的孤立历史记录：数据库不声明外键，
--     允许出现指向已不存在步骤的记录。新写入的尝试由领域层强制要求任务归属；
--   * 回填只读取 steps，不修改任何已有列，重复执行结果一致；
--   * 归档状态与执行状态、HTML 导出状态相互独立，不得合并为一个字段：
--     Agent 调用成功但归档失败、调用失败但会话已保存、JSONL 已保存但 HTML 导出失败
--     都必须能分别表达。
--
-- 字段与既有列的对应关系（不重复建列）：
--   * 会话执行状态     → sessions.status
--   * 原始会话文件路径 → sessions.session_jsonl_path
--   * HTML 导出状态    → sessions.export_status
--   * HTML 导出路径    → sessions.session_html_path
--   * Agent 角色       → sessions.role_id 与 sessions.role_snapshot_json

ALTER TABLE attempts ADD COLUMN task_id TEXT
    CHECK (task_id IS NULL OR length(trim(task_id)) > 0);

-- 既有尝试通过所属步骤回填任务归属；步骤已不存在的孤立记录保持 NULL。
UPDATE attempts
SET task_id = (
    SELECT steps.task_id FROM steps WHERE steps.id = attempts.step_id
)
WHERE task_id IS NULL;

-- 按任务与状态查询尝试（例如列出某个任务仍在运行的尝试）。
CREATE INDEX idx_attempts_task ON attempts (task_id, status);

ALTER TABLE steps ADD COLUMN failure_summary TEXT;

ALTER TABLE sessions ADD COLUMN archive_status TEXT;

ALTER TABLE sessions ADD COLUMN archive_path TEXT
    CHECK (archive_path IS NULL OR length(trim(archive_path)) > 0);

-- 按任务、角色与状态查询会话：Agent 角色以 role_id 表达，角色类型在快照中。
CREATE INDEX idx_sessions_task_role ON sessions (task_id, role_id, status);
