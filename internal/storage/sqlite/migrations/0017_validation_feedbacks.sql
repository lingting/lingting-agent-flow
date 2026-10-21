-- 0017_validation_feedbacks.sql
-- 为验证失败增加结构化的可复用反馈记录。
--
-- 设计说明：
--   * 验证失败的反馈不能只覆盖步骤上的一个字符串字段：同一轮验证可能有多条失败
--     检查，每条都有自己的退出码、原因与输出摘要，这些都需要可追溯。
--   * 反馈与原始 Validation Run 一起保留，不替代它：validation_runs 仍然保存流程
--     状态与最终结论，validation_feedbacks 保存"下一次 Developer 执行可以引用的
--     问题清单"。两者是一对一（同一 Run 至多一条反馈），但语义不同。
--   * 反馈只描述上一轮发生了什么，不能被当作当前验证结论；新的执行仍然必须创建
--     新的 Attempt 与独立 Session，并通过新的验证。
--   * checks_json 保存逐条失败检查的摘要（含脱敏与长度限制后的输出），结构与
--     domain.ValidationCheckFeedback 一一对应；不为内部字段单独建列，避免为诊断
--     信息固化查询接口。JSON 结构本身仍通过 json_valid 做非枚举性质的完整性约束。
--   * 不声明外键，不为枚举式字段（verdict）声明取值白名单 CHECK：未知取值必须能
--     写入并原样读回，业务校验由 Go 层承担。保留的是非枚举性质的 CHECK（非空、
--     JSON 结构、唯一性）。
--   * 只新增表与索引，不重建既有表：已有数据、索引与触发器原样保留。

CREATE TABLE validation_feedbacks (
    id                     TEXT    PRIMARY KEY NOT NULL,
    task_id                TEXT    NOT NULL CHECK (length(trim(task_id)) > 0),
    step_id                TEXT    NOT NULL CHECK (length(trim(step_id)) > 0),
    developer_attempt_id   TEXT    NOT NULL CHECK (length(trim(developer_attempt_id)) > 0),
    validation_run_id      TEXT    NOT NULL CHECK (length(trim(validation_run_id)) > 0),
    verdict                TEXT    NOT NULL CHECK (length(trim(verdict)) > 0),
    summary                TEXT,
    infrastructure_error   INTEGER NOT NULL DEFAULT 0,
    checks_json            TEXT    NOT NULL CHECK (json_valid(checks_json)),
    created_at             INTEGER NOT NULL,
    -- 同一轮验证最多一条反馈：它约束的是跨字段一致性，不是枚举取值。
    UNIQUE (validation_run_id)
);

CREATE INDEX idx_validation_feedbacks_step
    ON validation_feedbacks (step_id, created_at);
CREATE INDEX idx_validation_feedbacks_attempt
    ON validation_feedbacks (developer_attempt_id, created_at);
CREATE INDEX idx_validation_feedbacks_task
    ON validation_feedbacks (task_id, created_at);
