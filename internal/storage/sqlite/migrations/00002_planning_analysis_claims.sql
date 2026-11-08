-- 00002_planning_analysis_claims：规划分析的两项结构化事实。
--
-- 本脚本只新增内容，不修改 00001 已执行过的定义。
--
--   * planning_sessions.analysis_claim_id / analysis_claimed_at：一轮分析的数据库级占用。
--
--     规划会话在会话提交用户消息之后就已经是 analyzing，但这个状态本身不能回答"现在
--     是否有一轮分析正在跑"。占用标识是该问题的唯一答案：抢占是一条带条件的 UPDATE
--     （status = 'analyzing' 且 analysis_claim_id 为空），因此两个并发请求不可能同时
--     开始两轮模型调用，也不依赖任何进程内锁——跨进程同样成立。占用标识由发起方生成，
--     释放必须带同一个标识，所以"释放别人的占用"不会成功。
--
--     它不参与状态机：状态迁移仍只由 internal/domain 的语义方法表达，占用只是运行期
--     资源归属。进程意外退出时残留的占用不会让会话永久卡死——显式重试入口会以同一份
--     重试语义重新抢占，代价是可能覆盖一次已经无人推进的占用，这属于已知的用户操作。
--
--   * planning_revisions.snapshot_version：版本快照的结构版本。
--
--     版本号随每一版记录落库，读取方据此判断 JSON 字段含义，而不是假设"库里的都是当前
--     结构"。默认值 1 与 internal/domain.PlanningRevisionSnapshotVersion 对应。
--
-- 枚举式字段仍不设取值白名单；新增列不引入外键、触发器或级联行为。
--
-- +goose Up
ALTER TABLE planning_sessions ADD COLUMN analysis_claim_id TEXT;

ALTER TABLE planning_sessions ADD COLUMN analysis_claimed_at INTEGER
    CHECK (analysis_claimed_at IS NULL OR analysis_claimed_at > 0);

-- 按占用标识定位正在运行的分析：恢复与诊断只读取有占用的会话。
CREATE INDEX idx_planning_sessions_claim ON planning_sessions (analysis_claim_id);

ALTER TABLE planning_revisions ADD COLUMN snapshot_version INTEGER NOT NULL DEFAULT 1
    CHECK (snapshot_version > 0);
