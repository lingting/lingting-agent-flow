-- 会话的调用标识与容器关联。
--
-- 约定：
--   * invocation_id 是每次 Agent 调用的唯一标识，同一会话内多次调用各自独立，
--     用于区分本机 Session 目录与容器内临时 Session 路径；
--   * container_id 是容器登记记录 ID，记录本次调用实际执行所在的容器；
--     本机执行时为空；
--   * docker_id 是执行时的 Docker 容器 ID，用于在容器记录被更新后仍可追溯；
--   * 不保存任何凭据：容器关联字段只记录标识，不记录连接信息。
--
-- 会话记录与容器记录之间不建立外键：容器删除后会话历史仍需保留，
-- 因此容器标识只作为可追溯的文本字段。

ALTER TABLE sessions ADD COLUMN invocation_id TEXT
    CHECK (invocation_id IS NULL OR length(trim(invocation_id)) > 0);

ALTER TABLE sessions ADD COLUMN container_id TEXT
    CHECK (container_id IS NULL OR length(trim(container_id)) > 0);

ALTER TABLE sessions ADD COLUMN docker_id TEXT
    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0);

CREATE INDEX idx_sessions_invocation ON sessions (invocation_id)
    WHERE invocation_id IS NOT NULL;

CREATE INDEX idx_sessions_container ON sessions (container_id)
    WHERE container_id IS NOT NULL;
