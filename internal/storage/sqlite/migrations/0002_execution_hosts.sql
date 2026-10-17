-- 执行节点：通过 SSH 管理的远程 Linux 机器。
--
-- 约定：
--   * 只保存连接目标与远程工作空间配置，不保存密码、私钥或任何认证材料；
--   * workspace_root 遵循 POSIX 路径语义，~ 指远程 SSH 登录用户的 Home；
--   * is_default 通过部分唯一索引保证最多只有一个默认节点；
--   * port 使用 1-65535 的显式约束，避免应用层之外的写入破坏取值范围。

CREATE TABLE execution_hosts (
    id             TEXT    PRIMARY KEY NOT NULL,
    name           TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    host           TEXT    NOT NULL CHECK (length(trim(host)) > 0),
    port           INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    username       TEXT    NOT NULL CHECK (length(trim(username)) > 0),
    workspace_root TEXT    NOT NULL CHECK (length(trim(workspace_root)) > 0),
    is_default     INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

-- 最多一个默认节点：只对 is_default = 1 的行建立唯一索引。
CREATE UNIQUE INDEX idx_execution_hosts_default ON execution_hosts (is_default)
    WHERE is_default = 1;

CREATE INDEX idx_execution_hosts_name ON execution_hosts (name);
