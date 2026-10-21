-- 0016_attempt_git_state.sql
-- 为执行尝试增加执行前后的 Git 工作区状态快照。
--
-- 设计说明：
--   * 一次开发步骤执行必须留下"代码改了什么"的可核验痕迹：执行前 HEAD、执行后
--     HEAD、当前分支，以及工作区是否存在未提交变更。这些信息属于 Attempt 而不是
--     Step：同一步骤的不同尝试可能改动了完全不同的内容。
--   * 状态快照以 JSON 保存（git_before_json / git_after_json），结构与
--     domain.GitWorktreeState 一一对应；不为快照内部字段单独建列，避免为诊断信息
--     固化查询接口。JSON 结构本身仍通过 json_valid 做非枚举性质的完整性约束。
--   * 采集失败必须可表达：快照中的 status_read 为 false 时其余字段无意义，
--     因此不得用空值冒充"工作区干净"。
--   * 只保存摘要，不保存完整 diff：大仓库的 diff 既不适合放进数据库，也不是验收证据。
--   * 不声明外键，不为任何新增列声明枚举白名单 CHECK；只新增可空列与索引，
--     不重建表，已有数据、索引与触发器原样保留。

ALTER TABLE attempts ADD COLUMN git_branch TEXT;

ALTER TABLE attempts ADD COLUMN git_before_json TEXT
    CHECK (git_before_json IS NULL OR json_valid(git_before_json));

ALTER TABLE attempts ADD COLUMN git_after_json TEXT
    CHECK (git_after_json IS NULL OR json_valid(git_after_json));

-- 按任务与分支查询执行记录，用于定位某个分支上的改动历史。
CREATE INDEX idx_attempts_git_branch ON attempts (task_id, git_branch);
