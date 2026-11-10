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

以下规则是长期约束，任何新增或修改表结构、`migrations/` 下的迁移脚本、数据访问层的工作都必须遵守。

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
- SQLite Schema 以 `internal/storage/sqlite/migrations/` 下的版本化 SQL 脚本为唯一事实来源：脚本通过
  `embed` 编译进程序，由 Goose 在打开数据库时按版本顺序执行，已执行版本记录在 `goose_db_version` 表中。
- 新增或修改 Schema 只追加更高版本的迁移脚本，不修改已经执行过的脚本，不再维护第二份完整的最终态
  Schema 文件；不手写迁移引擎、版本判断与自定义版本表。
- 不支持旧的手写 Schema 数据库兼容：本程序处于开发阶段，不提供旧库升级、Schema 转换或历史数据回填，
  也不得自动删除、覆盖、清空用户数据库，或把旧库静默当作空库初始化。
- 数据库连接显式关闭外键检查（`foreign_keys(0)`），业务 Schema 本身不得依赖该开关。
- 每个迁移脚本由 Goose 在单个事务内执行，失败时整体回滚，不留下半套结构；同一数据库只允许一个 Server
  进程，迁移只由取得 `<database_path>.server.lock` 文件锁的服务实例执行。
- 执行记录表沿用同一套约束：`steps`、`attempts`、`sessions` 不声明外键，`step_type`、`status`、
  `run_status`、`completeness`、`export_status`、`archive_status` 等枚举式字段不得设置取值白名单，
  未知取值必须能写入并原样读回。
- 执行尝试的 Git 工作区状态以 JSON 快照保存（`attempts.git_before_json` / `git_after_json`），只受
  `json_valid` 这类结构约束，不为快照内部字段建列或设白名单；快照为空表示本次没有采集，与"采集成功
  且工作区干净"必须能区分。
- 表 `verification_results` 是**已冻结的遗留数据**：结构只由 `migrations/00001_initial_schema.sql` 定义，
  不再有运行时读写方。禁止在业务代码中重新写入或读取它，也禁止在没有可靠数据保全方案时删除该表定义。
- 数据库相关测试集中在 `tests/unit/store/`：仓储读写、未知枚举值原样保留、逻辑删除、条件更新拒绝
  迟到结果与并发写入。Schema 没有自动化审计守点，新增迁移脚本必须人工核对（测试规范见
  [docs/testing.md](docs/testing.md)）。

## 硬性红线

这些规则不允许被绕过，也不允许"先实现再补"：

- 状态只能由 `internal/domain` 的状态机方法推进，禁止在业务层直接赋值状态字段；迟到的结果不得覆盖
  已确认的终态。
- 不得伪造执行结果：未调用真实执行器时不得把 Step / Attempt / Session 标记为 `running`、`completed`
  或 `succeeded`，也不得把测试夹具数据写入真实开发数据库冒充执行历史。
- 同一份服务端数据目录只允许一个 Server 进程：启动时必须取得 `<database_path>.server.lock` 的独占文件锁，
  锁被占用时立即失败；不得为多进程共享同一数据库重新引入跨进程租约、执行权接管或多实例仲裁。
  说明：M5 的 `project_execution_leases` 不是多实例仲裁机制——它服务于单进程内的跨请求与跨重启识别
  （`owner` + 单调 `epoch` + `expires_at`），进程锁仍是唯一的多进程边界。
- 不得在容器缺失、未运行、已 detached 或节点未通过预检时静默回退到宿主机执行 Pi、Docker 或 Git。
- 不得关闭 SSH 主机密钥校验，不得引入密码认证、私钥口令或产品级 Git 凭据管理。
- Docker 调用必须遵守既定版本与 API 基线，且只经由
  `internal/docker` 的命令入口（见 [docs/docker-compatibility.md](docs/docker-compatibility.md)）。
- 执行节点预检不得解析基础 Dockerfile，不得查询 `FROM` 镜像的 Registry 元数据，不得执行 Docker Build
  或 Pull；基础镜像可用性由真实环境构建负责（见 [docs/docker-compatibility.md](docs/docker-compatibility.md)）。
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
| 运行手册：启动前提、停止语义、结果不明处置、备份恢复、故障排查 | [docs/runbook.md](docs/runbook.md) |
| 第三方组件替换自研实现的评估结论 | [docs/component-evaluations/](docs/component-evaluations/) |
| 项目当前阶段与里程碑 | [README.md](README.md)、[.steps/](.steps) |


## 项目发布阶段与兼容性政策

**AgentFlow 尚未正式开始发布运营。当前项目处于开发阶段，不存在必须维护的历史正式版本兼容性。**

所有开发、重构、数据库设计、依赖替换和架构调整必须遵守：

1. 禁止为了兼容尚未正式发布的旧版本、旧接口、旧数据库结构或历史内部实现增加代码和工作。
2. 允许直接修改当前内部 API、数据结构、数据库 Schema、配置格式和模块边界，以当前确定的产品设计为准。
3. 不得为了所谓的向前兼容、向后兼容或平滑升级，保留没有实际使用价值的兼容层、适配器、双写逻辑、数据回填逻辑和废弃接口。
4. 数据库以当前唯一确定的 Schema 为准，不要求支持历史开发阶段产生的数据库。
5. 替换自研组件时，以最终确定的目标架构为准，不要求保留旧实现作为回退方案。
6. 可以进行必要的数据安全保护，但不得将历史版本兼容性包装成数据安全要求。
7. 新增第三方依赖必须有明确的功能收益、可信来源、活跃维护情况和适用许可证，并确认其满足当前 Go 版本及产品安全约束；引入的组件必须实际减少维护成本，不得为了"用组件"而增加不必要的抽象或双实现。
8. 如果成熟组件不能完整满足当前产品要求，不得为了减少代码而牺牲既有功能、安全边界或数据正确性。

**唯一需要保证的是：当前开发版本按照已经确定的产品需求正确运行，而不是兼容过去尚未正式发布的版本。**