-- 代码保存的恢复判定依据：基线 SHA、落盘状态与恢复结论。
--
-- 背景：远程 Git 操作与本机数据库无法共享事务。Git 已经成功、但状态写入失败时，
-- 记录会停留在 pending，无法区分"操作尚未发生"与"操作已发生但结果没落盘"。
--
-- 约定：
--   * baseline_commit_sha / baseline_base_commit_sha 是执行副作用之前的只读基线，
--     用于判断 HEAD 或远程基础分支是否因为本次操作而变化；历史记录保持 NULL，
--     表示"没有基线"，此时不得推断，只能转人工核验；
--   * persist_status 表达"最后一次状态写入是否成功"，NULL 视为 persisted，
--     使历史记录仍然可读且不被误判为不可信；
--   * persist_error 只保存脱敏并裁剪后的写入失败摘要，写入成功后清空；
--   * recovery_status 保存人工/只读核验得到的结论，NULL 表示尚未核验；
--   * 全部列可空，只使用 ALTER TABLE ADD COLUMN，不重建表、不改动历史数据。
--
-- 不声明枚举白名单：未知取值必须能写入并原样读回，业务校验由 Go 层状态机承担。

ALTER TABLE code_saves ADD COLUMN baseline_commit_sha TEXT
    CHECK (baseline_commit_sha IS NULL OR length(baseline_commit_sha) IN (40, 64));

ALTER TABLE code_saves ADD COLUMN baseline_base_commit_sha TEXT
    CHECK (baseline_base_commit_sha IS NULL OR length(baseline_base_commit_sha) IN (40, 64));

ALTER TABLE code_saves ADD COLUMN persist_status TEXT;

ALTER TABLE code_saves ADD COLUMN persist_error TEXT;

ALTER TABLE code_saves ADD COLUMN recovery_status TEXT;
