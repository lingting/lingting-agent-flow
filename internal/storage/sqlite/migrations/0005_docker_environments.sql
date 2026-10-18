-- 远程 Docker 环境版本与受管理容器登记。
--
-- 约定：
--   * environment_versions 只追加不覆盖：版本号在 (host_id, env_type, env_name) 内
--     唯一，历史版本的 Dockerfile 哈希与构建配置哈希不允许被新版本修改；
--   * 只有 build_status = 'succeeded' 的版本可以用于创建容器；
--   * containers 同时保存管理状态 state 与最近观测到的 Docker 状态 docker_state，
--     两者互不替代；
--   * detached = 1 与 state = 'detached' 必须同时成立，脱离管理是终态，不能被
--     自动操作重新接管；
--   * 容器名称在同一节点内唯一，作为远程操作前的二次核验依据；
--   * 不保存任何凭据：镜像构建参数与容器环境变量中不得出现 Token、私钥或密码。

CREATE TABLE environment_versions (
    host_id             TEXT    NOT NULL,
    env_type            TEXT    NOT NULL CHECK (env_type IN ('base', 'language')),
    env_name            TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    version_number      INTEGER NOT NULL CHECK (version_number BETWEEN 1 AND 9999),
    dockerfile_sha256   TEXT    NOT NULL
        CHECK (length(dockerfile_sha256) = 64 AND dockerfile_sha256 NOT GLOB '*[^0-9a-f]*'),
    build_config_sha256 TEXT    NOT NULL
        CHECK (length(build_config_sha256) = 64 AND build_config_sha256 NOT GLOB '*[^0-9a-f]*'),
    context_sha256      TEXT    NOT NULL
        CHECK (length(context_sha256) = 64 AND context_sha256 NOT GLOB '*[^0-9a-f]*'),
    parent_version      TEXT    CHECK (parent_version IS NULL OR length(trim(parent_version)) > 0),
    image_ref           TEXT    NOT NULL CHECK (length(trim(image_ref)) > 0),
    image_id            TEXT
        CHECK (image_id IS NULL OR (image_id LIKE 'sha256:%' AND length(image_id) = 71)),
    build_status        TEXT    NOT NULL
        CHECK (build_status IN ('pending', 'building', 'succeeded', 'failed')),
    error_summary       TEXT,
    created_at          INTEGER NOT NULL,
    built_at            INTEGER CHECK (built_at IS NULL OR built_at > 0),
    PRIMARY KEY (host_id, env_type, env_name, version_number),
    FOREIGN KEY (host_id) REFERENCES execution_hosts (id) ON DELETE CASCADE,
    -- 构建成功的版本必须同时具备镜像 ID 与构建时间。
    CHECK (
        build_status <> 'succeeded'
        OR (image_id IS NOT NULL AND built_at IS NOT NULL)
    ),
    -- 基础环境不能依赖父环境版本，语言环境必须记录父版本。
    CHECK (
        (env_type = 'base' AND parent_version IS NULL)
        OR (env_type = 'language' AND parent_version IS NOT NULL)
    )
);

CREATE INDEX idx_environment_versions_host ON environment_versions (host_id, env_type, env_name);

CREATE TABLE containers (
    id                  TEXT    PRIMARY KEY NOT NULL,
    host_id             TEXT    NOT NULL,
    docker_id           TEXT    CHECK (docker_id IS NULL OR length(trim(docker_id)) > 0),
    name                TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    env_type            TEXT    NOT NULL CHECK (env_type IN ('base', 'language')),
    env_name            TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    env_version         TEXT    NOT NULL CHECK (length(trim(env_version)) > 0),
    build_config_sha256 TEXT    NOT NULL
        CHECK (length(build_config_sha256) = 64 AND build_config_sha256 NOT GLOB '*[^0-9a-f]*'),
    image_id            TEXT    NOT NULL
        CHECK (image_id LIKE 'sha256:%' AND length(image_id) = 71),
    task_id             TEXT,
    attempt_id          TEXT,
    state               TEXT    NOT NULL
        CHECK (state IN ('creating', 'created', 'running', 'stopped', 'removed', 'detached', 'unknown')),
    docker_state        TEXT    NOT NULL DEFAULT 'unknown'
        CHECK (docker_state IN (
            'unknown', 'created', 'running', 'paused',
            'restarting', 'exited', 'dead', 'removing'
        )),
    created_at          INTEGER NOT NULL,
    started_at          INTEGER CHECK (started_at IS NULL OR started_at > 0),
    finished_at         INTEGER CHECK (finished_at IS NULL OR finished_at > 0),
    end_reason          TEXT
        CHECK (
            end_reason IS NULL
            OR end_reason IN ('task_succeeded', 'task_failed', 'interrupted', 'detached', 'manual')
        ),
    detached            INTEGER NOT NULL DEFAULT 0 CHECK (detached IN (0, 1)),
    detached_at         INTEGER CHECK (detached_at IS NULL OR detached_at > 0),
    synced_at           INTEGER CHECK (synced_at IS NULL OR synced_at > 0),
    error_summary       TEXT,
    updated_at          INTEGER NOT NULL,
    FOREIGN KEY (host_id) REFERENCES execution_hosts (id) ON DELETE CASCADE,
    -- 容器名称在同一节点内唯一，避免同名容器被误认为同一实例。
    UNIQUE (host_id, name),
    -- detached 标记与管理状态必须同时成立。
    CHECK (
        (detached = 1 AND state = 'detached' AND detached_at IS NOT NULL)
        OR (detached = 0 AND state <> 'detached')
    ),
    -- 已删除的容器必须记录结束时间。
    CHECK (state <> 'removed' OR finished_at IS NOT NULL),
    -- 尝试关联必须同时给出任务关联。
    CHECK (attempt_id IS NULL OR task_id IS NOT NULL)
);

CREATE INDEX idx_containers_host_state ON containers (host_id, state);
CREATE INDEX idx_containers_task ON containers (task_id) WHERE task_id IS NOT NULL;
