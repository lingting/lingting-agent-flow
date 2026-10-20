-- 0013_session_output_logs.sql
-- 记录标准输出与标准错误日志在 AgentFlow 本机的持久化位置。
--
-- 设计说明：
--   * 两个字段分别保存，不把 stdout 与 stderr 混成一个"日志路径"：混流会丢失
--     "哪一行来自哪条流"的上下文，出错时无法区分真实输出与诊断信息；
--   * 日志写入文件而不是数据库：大输出不适合放进 SQLite 普通字段，数据库只保存
--     位置与状态，完整内容由文件承载；
--   * 允许为空：日志创建失败只降级诊断能力，不应阻止会话记录写入；
--   * 不声明枚举白名单 CHECK，也不新增外键，遵守 schema 长期约束；
--   * 只新增列与索引，不重建表：已有会话记录与其状态原样保留，重复执行结果一致。

ALTER TABLE sessions ADD COLUMN stdout_path TEXT
    CHECK (stdout_path IS NULL OR length(trim(stdout_path)) > 0);

ALTER TABLE sessions ADD COLUMN stderr_path TEXT
    CHECK (stderr_path IS NULL OR length(trim(stderr_path)) > 0);
