-- 会话执行产物：原始 Session 文件、HTML 导出与运行结果元数据。
--
-- 约定：
--   * 完整 JSONL 内容不写入数据库，只记录本机持久化路径；
--   * 保留 Session 文件（complete、partial、export_failed）时必须给出 JSONL 路径；
--   * 导出成功时必须给出 HTML 路径；
--   * run_status、completeness、export_status 允许为空，表示尚未记录；
--   * exit_code 允许为空，表示未能取得进程退出码。

ALTER TABLE sessions ADD COLUMN session_jsonl_path TEXT
    CHECK (session_jsonl_path IS NULL OR length(trim(session_jsonl_path)) > 0);

ALTER TABLE sessions ADD COLUMN session_html_path TEXT
    CHECK (session_html_path IS NULL OR length(trim(session_html_path)) > 0);

ALTER TABLE sessions ADD COLUMN run_status TEXT
    CHECK (run_status IS NULL OR run_status IN ('completed', 'failed', 'cancelled', 'timed_out'));

ALTER TABLE sessions ADD COLUMN completeness TEXT
    CHECK (completeness IS NULL OR completeness IN ('complete', 'partial', 'missing', 'export_failed'));

ALTER TABLE sessions ADD COLUMN export_status TEXT
    CHECK (export_status IS NULL OR export_status IN ('not_attempted', 'succeeded', 'failed'));

ALTER TABLE sessions ADD COLUMN exit_code INTEGER;

ALTER TABLE sessions ADD COLUMN failure_summary TEXT;

CREATE INDEX idx_sessions_run_status ON sessions (run_status, created_at);

CREATE INDEX idx_sessions_completeness ON sessions (completeness, created_at);
