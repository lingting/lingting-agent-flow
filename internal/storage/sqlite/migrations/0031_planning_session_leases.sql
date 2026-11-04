-- 0031_planning_session_leases.sql
-- 新增规划会话执行租约：planning_session_leases。
--
-- 背景：规划调用（容器准备、角色会话绑定、Pi 执行与 Session 归档）必须由同一个执行者
-- 独占。进程内互斥只在单个 Server 内有效：两个 Server 连接同一个数据库时仍可能同时对
-- 同一个 PlanningSession 发起规划调用，或在一个实例准备 / 执行期间由另一个实例释放
-- 容器与快照卷。租约把"谁在占用这个规划会话"变成数据库事实。
--
-- 设计说明：
--   * 一行代表一个规划会话的当前租约：planning_session_id 是主键，因此"每个规划会话
--     最多一个持有者"由主键约束保证，不依赖进程内 mutex。
--   * epoch 是领取代次，每次成功领取时由存储层在同一条原子 UPSERT 内递增（从 1 开始）。
--     (owner, epoch) 唯一确定一次领取，旧代次不得续期，也不得释放新持有者的租约。
--   * 释放时不删行：只清空 owner / operation / lease_expires_at，保留 epoch，使下一次
--     领取继续递增。因此 epoch 单调，不会因为释放而回退，旧持有者的迟到写入仍被拒绝。
--   * owner 由服务端生成、每次领取唯一；operation 是当前操作类型（run_role / prepare /
--     release），它只是诊断与审计分类，**不设取值白名单**：新增操作类型必须能在不修改
--     Schema 的情况下写入。
--   * lease_expires_at 是租约到期时间（UTC 毫秒）。到期即视为持有者已消失，可被接管，
--     但接管必须递增 epoch；未到期且未释放时其他执行者一律被拒绝。
--   * 组合一致性用表级 CHECK 表达：持有中必须同时具备 owner、operation 与到期时间；
--     释放后三者必须同时为空。它不是枚举白名单，也不限制具体取值。
--   * 部分索引只覆盖"持有中"的租约，用于按到期时间判断可接管范围；已释放的行不进入索引。
--
-- 约束取舍（遵循既有 Schema 约定）：
--   * 不声明外键，不使用 REFERENCES / FOREIGN KEY / 级联行为；planning_session_id 允许
--     指向已不存在的规划会话（例如领取后会话被清理），关系合法性由应用层校验。
--   * 保留非枚举约束：主键、NOT NULL、长度、正数时间戳、代次非负与字段组合一致性。
--   * 不修改 planning_sessions：租约状态不写进业务状态字段，会话状态机保持不变。

CREATE TABLE planning_session_leases (
    planning_session_id TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(planning_session_id)) > 0),
    owner               TEXT,
    operation           TEXT,
    epoch               INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    lease_expires_at    INTEGER,
    updated_at          INTEGER NOT NULL CHECK (updated_at > 0),
    CHECK (
        (owner IS NULL AND operation IS NULL AND lease_expires_at IS NULL)
        OR (
            owner IS NOT NULL AND length(trim(owner)) > 0
            AND operation IS NOT NULL AND length(trim(operation)) > 0
            AND lease_expires_at IS NOT NULL AND lease_expires_at > 0
        )
    )
);

-- 持有中的租约按到期时间检索：过期即可被接管，已释放的行不进入索引。
CREATE INDEX idx_planning_session_leases_expiry
    ON planning_session_leases (lease_expires_at)
    WHERE owner IS NOT NULL;
