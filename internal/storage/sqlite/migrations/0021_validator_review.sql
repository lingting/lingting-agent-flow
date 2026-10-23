-- 0021_validator_review.sql
-- 让确定性验证记录能够承载 Validator Agent 的语义审查结果。
--
-- 设计说明：
--   * 语义审查状态、结构化评估与最终结论三者互相独立：
--       validation_runs.status          确定性检查流程发生了什么；
--       validation_runs.review_status   语义审查这一次实际发生了什么；
--       validation_runs.assessment_json 结构化 AgentAssessment（可解释的评估）；
--       validation_runs.verdict         现有证据能否支持通过（由领域规则计算）。
--     把"审查调用失败"塞进 status 会让"验证跑完了但审查没跑成"无法表达，因此
--     单独加列，而不是复用既有枚举。
--   * 生产链路的验证结论只有一套：domain.DecideValidation。历史表
--     verification_results 及其 NewVerificationResult 判定规则不再是生产写入路径，
--     因此不把评估写进那张表，避免同时维护两套判定模型。
--   * validator_session_id 是既有列，本迁移只补齐它的生产写入约定：审查会话由
--     服务端在进程启动前创建，并与本次验证记录一起写入。
--   * review_summary 保存脱敏并裁剪后的审查说明（解析错误、失败原因），长度由
--     Go 层约束，不在数据库层面限定枚举取值。
--   * 不声明外键、不为枚举字段声明取值白名单 CHECK：未知取值必须能写入并原样读回，
--     业务校验由 Go 层状态机承担。保留的是非枚举性质的 CHECK（JSON 结构）。
--   * 只新增列，不重建既有表：已有数据、索引与触发器原样保留。

ALTER TABLE validation_runs ADD COLUMN review_status TEXT;
ALTER TABLE validation_runs ADD COLUMN review_summary TEXT;
ALTER TABLE validation_runs ADD COLUMN assessment_json TEXT
    CHECK (assessment_json IS NULL OR json_valid(assessment_json));

CREATE INDEX idx_validation_runs_review_status
    ON validation_runs (review_status, created_at);
