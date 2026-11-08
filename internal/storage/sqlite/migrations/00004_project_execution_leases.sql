-- 00004_project_execution_leases：项目级执行权与项目执行意图。
--
-- 单 Server 架构下同一份数据目录只有一个进程（启动时已取得独占进程锁），因此这里不是
-- 多实例仲裁机制，也不提供跨进程接管协议。项目执行权之所以仍然落库，是因为它必须在
-- 跨请求、跨重启的场景下回答三个问题：
--
--   1. 这个项目现在是否有执行者（进程内登记无法跨重启保留，重启后不能凭内存判断）；
--   2. 迟到的旧执行者是否还能继续推进（epoch 单调递增提供 fencing）；
--   3. 用户是否已请求停止领取（停止意图必须跨重启保留，不能只存内存变量）。
--
-- 结构约束：
--
--   * project_execution_leases 以 project_id 为主键，表达"每个项目最多一份执行权"；
--     取得时原子递增 epoch，释放只把 expires_at 置为当前时间，不删除行、不重置 epoch；
--     条件写入（owner + epoch + 到期判断）是并发正确性的唯一保证，不依赖进程内互斥。
--
--   * project_execution_states 记录项目执行意图：state 是最后一次决策的投影结果，
--     stop_requested_at 是用户请求停止领取的时间。枚举式 state 不设取值白名单，未知
--     取值必须能写入并原样读回。
--
-- 与任务级执行权的关系：任务执行权由进程内的任务协调器登记（同一任务只有一个执行
-- 者），本表不承担该职责；两者的生命周期互不替代。
--
-- 不新增外键、触发器、级联行为与历史数据回填：项目 / 任务标识仍作为普通文本保存，
-- 允许出现指向已不存在记录的孤立行（项目被逻辑删除后其执行权行仍保留，用于核验现场）。
--
-- +goose Up
CREATE TABLE project_execution_leases (
    project_id  TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(project_id)) > 0),
    -- owner 是服务进程实例标识：它区分不同进程，不表达用户身份。
    owner       TEXT    NOT NULL CHECK (length(trim(owner)) > 0 AND length(owner) <= 128),
    -- epoch 单调递增：取得新执行权时递增，释放不重置，用于拒绝迟到的旧执行者。
    epoch       INTEGER NOT NULL CHECK (epoch > 0),
    -- expires_at 是执行权到期时间；只有已到期的执行权可以被其他持有者取得。
    expires_at  INTEGER NOT NULL CHECK (expires_at > 0),
    acquired_at INTEGER NOT NULL CHECK (acquired_at > 0),
    updated_at  INTEGER NOT NULL CHECK (updated_at > 0)
);

-- 续租、释放与收敛都按项目标识或持有者定位，因此只保留主键与按 owner 的查询索引：
-- 取得执行权是"按主键更新已到期行或插入新行"，不需要额外的到期时间索引。
CREATE INDEX idx_project_execution_leases_owner ON project_execution_leases (owner);

CREATE TABLE project_execution_states (
    project_id        TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(project_id)) > 0),
    -- state 是最后一次决策的投影结果，不设取值白名单。
    state             TEXT    NOT NULL CHECK (length(trim(state)) > 0),
    -- stop_requested_at 非空表示用户已请求停止领取新任务，跨重启保留。
    stop_requested_at INTEGER CHECK (stop_requested_at IS NULL OR stop_requested_at > 0),
    updated_at        INTEGER NOT NULL CHECK (updated_at > 0)
);

