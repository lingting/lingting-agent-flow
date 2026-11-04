-- 0028_projects_default_host.sql
-- 新增 projects.default_host_id：项目默认执行节点。
--
-- 背景：项目已经登记了"同一份代码 + 同一套执行环境"，但"这个项目默认在哪台执行
-- 节点上运行"此前只存在于调用方每次发起时的临时选择里。把它登记到项目上，是为了让
-- 项目成为完整的执行配置载体，而不是每发起一次工作就重复确认同一件事。
--
-- 设计说明：
--   * 可空表示"未指定默认执行节点"：没有默认节点时，发起方必须由用户显式选择节点，
--     不得由系统猜测、回退到某台默认机器或自动挑选任意节点。
--   * 只保存执行节点标识，不声明外键：节点可能被逻辑删除，历史引用必须保留，让用户
--     仍能打开项目并清除或重新选择；引用完整性由应用层在写入时校验。
--   * 该字段是配置偏好，不代表节点当前可用，也不代表可以跳过运行时的节点与环境校验。
--   * 历史任务继续使用自己 tasks.host_id 与 config_json 快照，不因本列变化被改写。
--   * 不新增枚举白名单、不加 CHECK 取值约束，遵循既有 Schema 约定。
--
-- 索引：只为非空值建立部分索引。NULL 行表示"未设置"，不会参与按默认节点查询，
-- 为它们建立索引项没有意义。

ALTER TABLE projects ADD COLUMN default_host_id TEXT;

CREATE INDEX idx_projects_default_host ON projects (default_host_id)
    WHERE default_host_id IS NOT NULL;
