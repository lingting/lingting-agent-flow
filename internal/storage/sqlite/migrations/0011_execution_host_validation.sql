-- 0011_execution_host_validation.sql
-- 为执行节点（SSHConfig）增加环境预检的状态与结果字段。
--
-- 变更范围：
--   * validation_status       总体状态：checking / normal / abnormal
--   * validation_message      最新校验结果的摘要或失败原因
--   * validation_details_json 各项校验的结构化结果（JSON 文本）
--   * validation_started_at   最近一次校验开始时间（毫秒时间戳）
--   * validation_completed_at 最近一次校验结束时间（毫秒时间戳，可空）
--   * validation_generation   校验代次，用于阻止旧结果覆盖新配置
--
-- 约束说明：
--   * 只新增列，不重建表，已有数据、索引与触发器原样保留；
--   * 不声明外键，不为 validation_status 声明枚举白名单 CHECK：新增取值必须能在
--     不修改 Schema 的情况下写入，未知取值由读取层原样保留；
--   * validation_details_json 的 json_valid 校验属于 JSON 结构校验，不是枚举白名单，
--     按数据库规范保留；
--   * 已有记录一律补齐为 checking：旧配置不能被直接视为 normal，必须重新执行校验；
--   * validation_generation 从 1 开始，使新建记录与既有记录都拥有有效的首代代次。

ALTER TABLE execution_hosts ADD COLUMN validation_status TEXT NOT NULL DEFAULT 'checking';

ALTER TABLE execution_hosts ADD COLUMN validation_message TEXT;

ALTER TABLE execution_hosts ADD COLUMN validation_details_json TEXT
    CHECK (validation_details_json IS NULL OR json_valid(validation_details_json));

ALTER TABLE execution_hosts ADD COLUMN validation_started_at INTEGER
    CHECK (validation_started_at IS NULL OR validation_started_at > 0);

ALTER TABLE execution_hosts ADD COLUMN validation_completed_at INTEGER
    CHECK (validation_completed_at IS NULL OR validation_completed_at > 0);

ALTER TABLE execution_hosts ADD COLUMN validation_generation INTEGER NOT NULL DEFAULT 1
    CHECK (validation_generation > 0);

-- 启动时需要找出"仍停留在 checking"的记录并重新调度校验。
CREATE INDEX idx_execution_hosts_validation ON execution_hosts (validation_status)
    WHERE deleted_at IS NULL;
