-- 0024_task_lease_epoch.sql
-- 为任务租约增加领取代次，使任务执行权具备持久化 fencing 能力。
--
-- 背景：tasks.lease_owner / lease_expires_at 已经能表达"谁持有租约、何时过期"，
-- 但只有 owner 无法区分"同一执行者的上一次领取"与"这一次领取"。一旦服务重启
-- 复用同一个 owner 标识，旧执行者的迟到写入（状态推进、续租、释放）会被数据库
-- 当作合法写入接受，从而覆盖新领取者的状态。
--
-- 设计说明：
--   * lease_epoch 是领取代次，每次建立新的领取时由存储层在同一条条件 UPDATE 内
--     原子递增（lease_epoch = lease_epoch + 1）。因此 (lease_owner, lease_epoch)
--     唯一确定一次领取，旧代次的写入、续租与释放都会被条件更新拒绝。
--   * 默认值为 0：历史记录从未被领取过，0 表示"没有有效代次"。真正的领取从 1
--     开始，因此 0 永远不会被误认为一次有效领取。
--   * 代次是内部栅栏，不对外暴露：HTTP / CLI 接口不得接受调用方传入的 epoch。
--   * 只使用 ALTER TABLE ADD COLUMN，不重建 tasks 表：既有数据、索引与触发器原样
--     保留，历史记录的 owner / expires_at 也不做任何改写。
--   * 不声明枚举白名单 CHECK；保留的是非枚举性质的数值范围约束（代次不得为负）。

ALTER TABLE tasks ADD COLUMN lease_epoch INTEGER NOT NULL DEFAULT 0
    CHECK (lease_epoch >= 0);
