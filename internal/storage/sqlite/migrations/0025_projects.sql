-- 0025_projects.sql
-- 新增 projects：项目登记表。
--
-- 背景：项目的概念此前只以"受书写规则校验的自由文本标识"存在（tasks 配置里的
-- project_id），没有任何记录可以指向。该标识同时决定缓存卷的共享范围键与远程
-- 管理工作目录名，因此写错一个字就会让同一项目的任务落在不同的缓存卷与目录里，
-- 而且系统无法自行发现这种分裂。
--
-- 设计说明：
--   * 项目保存"同一份代码 + 同一套执行环境"的最小事实：仓库地址、基础分支、语言
--     环境与版本、缓存类型、容器内工作目录。这些是任务创建时按项目标识继承的内容。
--   * 项目标识由调用方给出，是主键；它同时成为远程目录名，因此写入前由领域层按
--     环境名称规则校验。
--   * 仓库地址只保存已脱敏的形式：内嵌凭据的地址在领域层被拒绝，本表不保存任何
--     Token、私钥或密码。
--   * env_version 可为空，表示使用当前可用版本；空值不等同于某个具体版本。
--   * caches_json 保存缓存类型名称数组，只做 JSON 结构校验，不建立第二张关联表：
--     缓存类型由语言环境定义，项目只声明允许挂载哪些。
--   * 逻辑删除：deleted_at 标记删除，不物理删除记录，也不级联改写历史任务与代码
--     保存记录上的 project_id。
--
-- 约束取舍（遵循既有 Schema 约定）：
--   * 不声明外键，不声明枚举值白名单 CHECK（env_type 不限定取值集合）；
--   * 保留 NOT NULL、长度、时间戳为正数与 JSON 结构等非枚举约束。

CREATE TABLE projects (
    id            TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    name          TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    repository_url TEXT   NOT NULL CHECK (length(trim(repository_url)) > 0),
    base_branch   TEXT    NOT NULL CHECK (length(trim(base_branch)) > 0),
    env_type      TEXT    NOT NULL CHECK (length(trim(env_type)) > 0),
    env_name      TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    env_version   TEXT    CHECK (env_version IS NULL OR length(trim(env_version)) > 0),
    work_dir      TEXT    NOT NULL CHECK (length(trim(work_dir)) > 0),
    caches_json   TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(caches_json)),
    created_at    INTEGER NOT NULL CHECK (created_at > 0),
    updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
    deleted_at    INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0)
);

-- 列表默认按创建时间升序返回未删除记录，删除标记参与过滤。
CREATE INDEX idx_projects_created ON projects (deleted_at, created_at, id);
