# AGENTS.md

本文件只保留两类长期约束：**代码约定**与**数据库约束**，外加少量不可违反的红线与文档索引。其余规则
（架构边界、测试、SSH、Docker、前端、接口）已经按职责放进 `docs/`，需要时按下表查阅，不要在这里重复。
通用开发规范（语言、权限、测试授权）由全局配置提供。

## 代码约定

- 所有输出、注释与提交信息使用简体中文。
- 每个文件保持单一清晰职责；职责扩散时按职责拆分，不按固定行数机械拆分。
- 同一职责不拆到多个文件；不同职责的代码不堆在同一文件。
- 项目未声明行宽时以每行不超过 120 字符为目标；超过时结构化换行。
- 同一方法在调用链中连续出现两次及以上时，从第一次重复调用开始逐段换行。
- 错误必须保留上下文，底层不重复打印同一个错误；只有命令执行入口负责输出一次。
- 不使用 `panic` 处理普通的配置或用户输入错误。
- 优先使用 Go 标准库；新增第三方依赖必须有明确用途，并只通过 Go Modules 管理。
- 完成修改后对改动的 Go 文件执行 `gofmt`，并确认 `go vet ./...` 无新增问题。

### 换行符

- 仓库内所有文本文件（`.go`、`.sql`、`.md`、`.ts`、`.tsx`、`.json`、`.yml`、`.yaml`、`.css`、
  `.html`、`.svg`、`.gitignore`、`Makefile`、`go.mod`、`go.sum`、`pnpm-lock.yaml` 等）一律使用
  LF（`\n`），不得写入 CRLF（`\r\n`）或孤立 CR（`\r`）。
- 换行符由仓库根目录 `.gitattributes` 的 `* text=auto eol=lf` 统一强制，不得依赖本机
  `core.autocrlf` / `core.eol` 配置；新增文本文件同样受该规则约束。
- 禁止同一文件混用 LF 与 CRLF：发现混合行尾必须整体转换为 LF，不得只改部分行，也不得只修改少数行
  掩盖混合状态。
- 新增或修改文件后必须确认行尾为 LF；Windows 下的编辑器、脚本与重定向写入都不得引入 CRLF，也不得
  通过 `unix2dos`、PowerShell 重定向等方式回写。
- 二进制文件、编译产物与生成目录（`bin/`、`node_modules/`、`lingting-agent-flow-ui/dist/`、
  `internal/webui/dist/`、`.idea/`、`.plan/`）不在本规则范围内，也不得提交进版本库。

## 数据库约束规范

以下规则是长期约束，任何新增或修改表结构、迁移脚本、数据访问层的工作都必须遵守。

- 业务数据库中的枚举式字段不得通过 `CHECK` 等数据库约束限定固定取值集合；新增取值必须能在不修改
  Schema 的情况下写入。
- 禁止在业务 Schema 中声明外键；不得新增列级 `REFERENCES` 或表级 `FOREIGN KEY`，也不得使用
  `ON DELETE` / `ON UPDATE` 级联行为。
- 不得通过触发器模拟外键引用完整性，也不得用触发器重新实施枚举值白名单。
- 普通关联 ID（`task_id`、`step_id`、`attempt_id`、`session_id`、`host_id`、`container_id` 等）仍作为
  普通文本保存，并保留索引以加速关联查询；允许出现指向已不存在记录的孤立 ID。
- 主键、`NOT NULL`、有明确业务意义的 `UNIQUE`、非枚举性质的 `CHECK`（数值范围、长度、JSON 结构、
  跨字段组合一致性）与合理默认值仍需逐项审核后保留，不得机械删除。
- 引用完整性、状态转换与生命周期规则由应用层显式承担，不得重新引入数据库外键或枚举白名单；数据读取
  层必须保留未知枚举值，不得自动映射为默认状态。
- 逻辑删除优先：`roles`、`execution_hosts` 与 `tasks` 使用 `deleted_at` 标记删除，删除操作不得级联
  删除历史记录。
- SQLite 变更必须使用增量迁移：已发布脚本不得修改，schema 变更只能追加更高版本的脚本，且优先使用
  `ALTER TABLE` 等非破坏性语句，不重建无关表。迁移脚本放在 `internal/storage/sqlite/migrations/`，
  通过 `embed` 嵌入程序，启动时按版本顺序执行，已应用版本记录在 `schema_migrations`。
- 迁移已有数据库必须保留数据，禁止删除数据库重建；表重建必须保留必要的索引、触发器、视图与自增主键
  行为。
- 数据库连接显式关闭外键检查（`foreign_keys(0)`），业务 Schema 本身不得依赖该开关。
- 迁移必须在事务内完成，失败时回滚且不得写入版本记录；并发打开同一数据库只允许执行一次迁移。
- 执行记录表沿用同一套约束：`steps`、`attempts`、`sessions` 不声明外键，`step_type`、`status`、
  `run_status`、`completeness`、`export_status`、`archive_status` 等枚举式字段不得设置取值白名单，
  未知取值必须能写入并原样读回。
- 新增列的迁移必须可重复执行且只读取既有表：`attempts.task_id` 的历史回填通过 `steps.task_id`
  完成，步骤已不存在的孤立记录保持空值，不得因此失败或删除记录。
- 执行尝试的 Git 工作区状态以 JSON 快照保存（`attempts.git_before_json` / `git_after_json`），只受
  `json_valid` 这类结构约束，不为快照内部字段建列或设白名单；快照为空表示本次没有采集，与"采集成功
  且工作区干净"必须能区分。
- 表 `verification_results` 是**已冻结的遗留数据**：只由已发布迁移（`0001` / `0008` / `0009`）定义，
  不再有运行时读写方。禁止在业务代码中重新写入或读取它，禁止为"清理"直接修改已发布迁移，也禁止在
  没有可靠数据保全方案时通过新迁移删表。
- 数据库相关测试必须覆盖 Schema 审计、旧库升级、数据保留、未知枚举值与并发访问（测试规范见
  [docs/testing.md](docs/testing.md)）。

## 硬性红线

这些规则不允许被绕过，也不允许"先实现再补"：

- 状态只能由 `internal/domain` 的状态机方法推进，禁止在业务层直接赋值状态字段；迟到的结果不得覆盖
  已确认的终态。
- 不得伪造执行结果：未调用真实执行器时不得把 Step / Attempt / Session 标记为 `running`、`completed`
  或 `succeeded`，也不得把测试夹具数据写入真实开发数据库冒充执行历史。
- 不得在容器缺失、未运行、已 detached 或节点未通过预检时静默回退到宿主机执行 Pi、Docker 或 Git。
- 不得关闭 SSH 主机密钥校验，不得引入密码认证、私钥口令或产品级 Git 凭据管理。
- Docker 调用必须遵守既定版本与 API 基线，且只经由
  `internal/docker` 的命令入口（见 [docs/docker-compatibility.md](docs/docker-compatibility.md)）。
- 未经用户明确要求，不运行测试、不创建测试文件与临时验证脚本，也不通过"测试后删除"绕过
  （见 [docs/testing.md](docs/testing.md)）。
- 文档只描述已实现的能力，不提前声明尚未落地的命令或接口；代码编辑期间不改文档。

## 索引

| 需要了解 | 查阅 |
|---|---|
| 目录布局、包职责、依赖方向 | [docs/workspace.md](docs/workspace.md) |
| 执行链路边界、状态分层、收口规则 | [docs/architecture.md](docs/architecture.md) |
| 本地环境、常用命令、改动流程、提交与文档约定 | [docs/development.md](docs/development.md) |
| 测试目录、测试工作空间、真实依赖开关、结果报告 | [docs/testing.md](docs/testing.md) |
| SSH 连接层、主机密钥、执行节点预检、远程工作空间 | [docs/ssh-hosts.md](docs/ssh-hosts.md) |
| HTTP 接口路径、请求边界、错误语义 | [docs/http-api.md](docs/http-api.md) |
| CLI 命令索引与实现边界 | [docs/cli.md](docs/cli.md) |
| 前端约束、Web 交付、请求分发、单文件构建 | [docs/frontend.md](docs/frontend.md) |
| Docker 版本基线、命令清单、容器管理 | [docs/docker-compatibility.md](docs/docker-compatibility.md) |
| 发布流程、制品与来源证明 | [docs/release.md](docs/release.md) |
| 项目当前阶段与里程碑 | [README.md](README.md)、[.steps/](.steps) |
