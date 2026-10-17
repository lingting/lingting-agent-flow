# migrations

数据库迁移脚本已迁移到 **`internal/storage/sqlite/migrations/`**，并通过 Go 的 `embed`
嵌入程序，本目录不再保存 SQL 文件。

## 为什么移动

- 应用可以在任意工作目录下运行，迁移不再依赖外部 SQL 文件路径。
- 只维护一份迁移脚本，避免根目录与嵌入目录出现两份不一致的 schema。

## 约定

- 文件名以递增序号前缀开头，例如 `0002_add_retry_policy.sql`。
- 已发布的迁移不得修改，schema 变更只能追加更高版本的脚本。
- 每次 schema 变更必须同时提供迁移脚本，不允许只改代码。
- 迁移脚本只描述结构变更，不写入业务数据。
- 迁移由 `sqlite.Open` 在打开数据库时按版本顺序自动执行，已应用的版本记录在
  `schema_migrations` 表中，重复启动不会重复执行。

迁移状态与数据库文件位置见仓库根目录的 `README.md`。
