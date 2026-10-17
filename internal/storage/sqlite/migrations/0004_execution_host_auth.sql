-- 执行节点 SSH 认证与主机密钥信任配置。
--
-- 约定：
--   * 只保存非机密的认证配置：认证方式种类与本机私钥文件路径；
--   * 禁止保存 SSH 密码、私钥原文或私钥口令，这些材料只在连接时通过内存中的
--     凭据提供器传递；
--   * auth_method 默认 agent，使既有记录升级后仍可继续使用；
--   * 私钥认证必须同时给出 private_key_path，其余认证方式允许为空；
--   * last_connect_error 只保存稳定的分类代码，不保存完整错误消息；
--   * host_key_trusted_at 为空表示尚未确认信任该主机密钥。

ALTER TABLE execution_hosts ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'agent'
    CHECK (auth_method IN ('agent', 'private_key', 'password'));

ALTER TABLE execution_hosts ADD COLUMN private_key_path TEXT
    CHECK (private_key_path IS NULL OR length(trim(private_key_path)) > 0);

ALTER TABLE execution_hosts ADD COLUMN host_key_algorithm TEXT
    CHECK (host_key_algorithm IS NULL OR length(trim(host_key_algorithm)) > 0);

ALTER TABLE execution_hosts ADD COLUMN host_key_fingerprint TEXT
    CHECK (
        host_key_fingerprint IS NULL
        OR (length(trim(host_key_fingerprint)) > 0 AND host_key_fingerprint LIKE 'SHA256:%')
    );

ALTER TABLE execution_hosts ADD COLUMN host_key_trusted_at INTEGER
    CHECK (host_key_trusted_at IS NULL OR host_key_trusted_at > 0);

ALTER TABLE execution_hosts ADD COLUMN last_connect_status TEXT
    CHECK (last_connect_status IS NULL OR last_connect_status IN ('success', 'failed'));

ALTER TABLE execution_hosts ADD COLUMN last_connect_error TEXT
    CHECK (last_connect_error IS NULL OR length(trim(last_connect_error)) > 0);

ALTER TABLE execution_hosts ADD COLUMN last_connected_at INTEGER
    CHECK (last_connected_at IS NULL OR last_connected_at > 0);

-- 私钥认证必须给出私钥文件路径。
CREATE TRIGGER trg_execution_hosts_private_key_required
BEFORE INSERT ON execution_hosts
WHEN NEW.auth_method = 'private_key'
    AND (NEW.private_key_path IS NULL OR length(trim(NEW.private_key_path)) = 0)
BEGIN
    SELECT RAISE(ABORT, 'private_key_path is required for private_key auth');
END;

CREATE TRIGGER trg_execution_hosts_private_key_required_update
BEFORE UPDATE ON execution_hosts
WHEN NEW.auth_method = 'private_key'
    AND (NEW.private_key_path IS NULL OR length(trim(NEW.private_key_path)) = 0)
BEGIN
    SELECT RAISE(ABORT, 'private_key_path is required for private_key auth');
END;
