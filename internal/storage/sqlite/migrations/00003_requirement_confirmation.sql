-- 00003_requirement_confirmation：方案确认生成的正式需求与需求内任务顺序。
--
-- 本脚本只新增内容，不修改 00001 / 00002 已执行过的定义。
--
--   * requirements 新增项目关联与规划快照：
--
--     确认流程把"用户采纳的那一版 Revision"变成一条正式需求，因此需求需要能指出自己的
--     来源与最终结论：所属项目、规划会话、最终需求分析快照、最终实施方案快照与确认时间。
--     这些列全部可空，因为历史上由手工提交产生的需求没有规划来源；业务必填性由
--     internal/domain 与确认流程在应用层强制（"有规划来源就必须完整"），数据库不复制
--     这套组合判定。
--
--     final_* 只保存用户确认的那一版快照，只做 json_valid 结构约束：快照内部字段由
--     Revision 的解析路径负责，不为内部字段建列，也不设取值白名单。
--
--   * requirements 增加逻辑删除列：
--
--     删除属于后续里程碑的能力，这里只补列，不实现级联删除，也不物理删除历史记录。
--
--   * tasks 新增 requirement_sequence：
--
--     顺序是确认生成时确定的业务事实，不是创建时间的推断值。部分唯一索引保证同一需求内
--     一个顺序只对应一个任务，同时允许历史任务保持 NULL（没有这个事实就不补造）。
--
-- 枚举式字段仍不设取值白名单；新增列不引入外键、触发器或级联行为，也不做历史数据回填。
--
-- +goose Up
ALTER TABLE requirements ADD COLUMN project_id TEXT
    CHECK (project_id IS NULL OR length(trim(project_id)) > 0);

ALTER TABLE requirements ADD COLUMN planning_session_id TEXT
    CHECK (planning_session_id IS NULL OR length(trim(planning_session_id)) > 0);

ALTER TABLE requirements ADD COLUMN final_analysis_json TEXT
    CHECK (final_analysis_json IS NULL OR json_valid(final_analysis_json));

ALTER TABLE requirements ADD COLUMN final_plan_json TEXT
    CHECK (final_plan_json IS NULL OR json_valid(final_plan_json));

ALTER TABLE requirements ADD COLUMN confirmed_at INTEGER
    CHECK (confirmed_at IS NULL OR confirmed_at > 0);

ALTER TABLE requirements ADD COLUMN deleted_at INTEGER
    CHECK (deleted_at IS NULL OR deleted_at > 0);

-- 项目需求列表：按项目查询未删除需求，最近的在前。
CREATE INDEX idx_requirements_project ON requirements (project_id, created_at)
    WHERE project_id IS NOT NULL AND deleted_at IS NULL;

-- 需求列表默认只读取未删除记录。
CREATE INDEX idx_requirements_active ON requirements (created_at)
    WHERE deleted_at IS NULL;

ALTER TABLE tasks ADD COLUMN requirement_sequence INTEGER
    CHECK (requirement_sequence IS NULL OR requirement_sequence > 0);

-- 同一需求内的执行顺序唯一：并发确认不会写出两个相同顺序的任务。
CREATE UNIQUE INDEX idx_tasks_requirement_sequence
    ON tasks (requirement_id, requirement_sequence)
    WHERE requirement_sequence IS NOT NULL;
