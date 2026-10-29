# P4-G12：项目管理能力（项目从自由文本升级为受校验的登记记录）

> - **状态**：`completed`（2026-10-28）
> - **基线**：`origin/master` = `2cb21ca0d81bc3e6d5e93b4a8e827de6b310b16f`（`决策评估报告`），
>   工作区在本轮开始前干净（`git status --short` 无输出）。
> - **前置决策**：用户已明确"当前没有足量的真实使用数据支持性能评测，先恢复常规产品功能开发"，
>   因此 **P4-M5 保持 `pending`**，本轮不推进也不重复进行缺少数据的评估。
> - **本文件记录的是实际实施与验证证据**，不是计划；未执行、未验证的内容一律标注为未执行。

---

## 1. G12 的原始缺口说明与核实结论

### 1.1 原始缺口说明（P4-M1 §5 G12）

`P4-M1-api-web-gap-inventory.md` 把 G12 记为"项目能力在后端缺失（P2 优先级）"，事实描述为：

- `domain.ProjectConfig` 与 `ValidateProjectID` 存在，但**没有 `projects` 表**、没有 store、
  没有 app 服务、没有 CLI、没有 HTTP；
- `task.Config.ProjectID` 只是一个受校验的字符串，进入 `code_saves.project_id` /
  `code_fetches.project_id` 与缓存共享范围键；
- 结论是"这不是缺前端页面，而是后端业务能力本身不存在"；
- 若立项，验收标准是：存在 `projects` 表与迁移（记录 id / name / 环境 / 版本 / 缓存 / 工作目录）；
  创建任务时项目来自真实记录且 `ValidateProjectID` 不被绕过；未立项时项目页继续是占位页。

### 1.2 本轮按代码核实的结论

按要求没有直接照抄摘要，而是逐项核实（结论与摘要一致，但补出了"为什么必须做"的关键事实）：

| 核实项 | 结论 | 证据 |
|---|---|---|
| 项目概念是否已以其他形式存在 | **只以自由文本存在** | `internal/domain/project.go` 的 `ProjectConfig` / `NewProjectConfig` / `ValidateProjectID` 在 `internal/`、`cmd/` 下**零生产调用者**（唯一引用者是 `tests/unit/container/container_test.go`） |
| 是否有持久化 | **没有** | `internal/storage/sqlite/migrations/0001…0024` 中无 `projects` 表；`storage.Tx` 无 `ProjectStore` |
| 是否有 HTTP / CLI | **没有** | `internal/httpapi` 无项目路由；`internal/cli` 无 `project` 命令 |
| `task.Config.ProjectID` 的真实作用 | **决定两处真实资源位置** | ① 项目级缓存卷的共享范围键：`internal/app/containers.go` 在 `CacheScopeProject` 时要求非空 `ProjectID` 并把它作为 `scopeKey` 传给 `docker.CacheVolumeName(host, mount, scopeKey)`；② 远程管理工作目录名：`internal/app/execenv.go` 的 `resolveProjectManagementDir` → `docker.Layout.ProjectDir(projectID)`，空值落为 `unassigned` |
| 因此写错的后果 | **不可自愈的分裂** | 同一项目的两个任务若写成 `demo-service` 与 `demo_service`，会得到不同的缓存卷名与不同的远程管理目录，系统没有任何地方能发现它们是同一个项目 |
| 前端现状 | 占位页 + 自由文本输入 | `lingting-agent-flow-ui/src/pages/projects/ProjectsPage.tsx` 是 `PagePlaceholder`；`CreateTaskModal.tsx` 用 `<Input>` 手填 `project_id`，且字段标注"（可选）" |

因此 G12 要解决的具体用户问题是两条：

1. **项目标识必须指向真实记录**，否则拼写错误会静默产生分裂的缓存卷与远程目录；
2. **项目级配置只写一次**，否则每次建任务都要重抄仓库地址、基础分支、语言环境与版本、缓存类型、
   容器内工作目录，容易不一致。

### 1.3 实施前向用户确认的三项业务规则

缺口说明没有回答三处必须选定的规则，按要求先询问再实施：

| 决策点 | 用户选择 | 落地方式 |
|---|---|---|
| 项目承载哪些字段 | **环境配置 + 仓库地址** | 项目记录 id / name / repository_url / base_branch / 环境 / 环境版本 / 缓存类型 / 工作目录 |
| 任务的 `project_id` | **改为必填** | 空值、书写非法、不存在、已逻辑删除一律拒绝（`ErrTaskInvalidInput`） |
| 本轮交付范围 | **后端 + Web 完整闭环** | 迁移 + 领域 + 存储 + 应用 + HTTP + CLI + 前端页面与选择器 |

---

## 2. 数据模型与迁移

### 2.1 迁移 `internal/storage/sqlite/migrations/0025_projects.sql`

```sql
CREATE TABLE projects (
    id             TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    name           TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    repository_url TEXT    NOT NULL CHECK (length(trim(repository_url)) > 0),
    base_branch    TEXT    NOT NULL CHECK (length(trim(base_branch)) > 0),
    env_type       TEXT    NOT NULL CHECK (length(trim(env_type)) > 0),
    env_name       TEXT    NOT NULL CHECK (length(trim(env_name)) > 0),
    env_version    TEXT    CHECK (env_version IS NULL OR length(trim(env_version)) > 0),
    work_dir       TEXT    NOT NULL CHECK (length(trim(work_dir)) > 0),
    caches_json    TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(caches_json)),
    created_at     INTEGER NOT NULL CHECK (created_at > 0),
    updated_at     INTEGER NOT NULL CHECK (updated_at > 0),
    deleted_at     INTEGER CHECK (deleted_at IS NULL OR deleted_at > 0)
);
CREATE INDEX idx_projects_created ON projects (deleted_at, created_at, id);
```

约束取舍（遵循 `.steps` 与 `AGENTS.md` 的数据库约束规范）：

- **不声明外键**：`tasks` / `code_saves` / `code_fetches` 上的 `project_id` 是普通文本，历史记录
  可以指向已删除的项目；
- **不声明枚举白名单**：`env_type` 只要求非空，不写 `IN (...)`，未知取值必须能写入并原样读回；
- 保留的是**非枚举性质**的约束：主键、`NOT NULL`、长度、时间戳为正、`json_valid`；
- **逻辑删除**：`deleted_at` 标记删除，不物理删除、不级联改写任何历史记录；
- 缓存类型用 `caches_json` 而不是第二张关联表：类型集合由语言环境定义，项目只声明允许挂载哪些。

### 2.2 「空环境版本」的语义对齐（有意调整）

`domain.ProjectConfig.Validate` 此前无条件调用 `ParseVersionLabel`，即**项目必须锁定版本标签**。
本轮改为"非空才校验"：空值表示**使用当前可用版本**，与 `domain.TaskConfig` 对
`EnvironmentVersion` 的处理完全一致。理由：项目是任务的配置来源，若项目必须锁定版本而任务允许
省略，则"从项目继承"与"任务可省略"两条规则会互相矛盾。该调整不放松任何格式校验（给出值仍必须
是 `v0001` 形式）。当前可用版本由后续执行阶段解析，项目记录不猜测版本。

### 2.3 旧库升级行为

- 迁移只新增一张表和一个索引，不重建任何既有表，不改写任何既有行；
- `tests/unit/store/migrate_test.go` 的"模拟旧版本数据库"流程已加入 `DROP TABLE projects`，
  与其余后期迁移的处理方式一致；
- `latestSchemaVersion`（`tests/unit/store/migrate_test.go`）与 `expectedSchemaVersion`
  （`tests/integration/local/database/workflow_test.go`）同步为 `25`；
- 旧库升级、重复迁移、数据保留、Schema 审计由既有用例覆盖并通过（见 §10.1）。

---

## 3. 领域模型

`internal/domain/project.go`：

- 新增 `domain.Project`，**内嵌** `ProjectConfig` 并补充三件事：
  - `RepositoryURL`（已脱敏的仓库地址，`ParseGitRepositoryURL` 拒绝内嵌凭据）；
  - `BaseBranch`（`NormalizeGitBranch` 规范化后写入，`origin/main` → `main`）；
  - `DeletedAt`（逻辑删除时间）。
- `NewProject(ProjectInput, time.Time)` 是唯一构造入口：复用 `NewProjectConfig` 产生环境配置部分，
  补上显式 `WorkDir` 与规范化分支，最后统一走 `Project.Validate()`；创建路径与更新路径共用同一套
  校验（`app.ProjectService.Update` 会重新调用 `NewProject` 再写库，避免更新绕过校验）。
- `Project.MarkDeleted(at)` 一次性写入删除时间，重复删除返回校验错误，不改写首次删除时间。
- `Project.NormalizedBaseBranch()` / `Project.EffectiveWorkDir()` 提供只读派生值。
- 新增 `RequireProjectID(id)`：任务必须关联项目的规则落点（空值 → 校验错误；非空走
  `ValidateProjectID` 的书写规则）。`ValidateProjectID` 本身保留"空值合法"的语义，因为它只表达
  书写形式，是否必填由调用方按业务规则决定。
- `ProjectConfig` 删除了两个**零调用者**的占位字段 `SourceProvider` / `CodeSaver`
  （注释原文即"后续代码获取方式的名字"）。它们没有任何消费者，若保留会变成永远为 NULL 的列；
  实际的代码获取与保存由固定实现承担，不可按项目选择。

`internal/domain/task_config.go`：`TaskConfig.ProjectID` 的说明从"可选的项目标识"改为"任务关联的
项目标识"，并注明"新建任务必须关联项目；历史任务可能为空，读取层不得把空值当成默认项目"。

---

## 4. 存储层

- `internal/storage/interfaces.go`：新增 `ProjectFilter`（`IncludeDeleted`）与 `ProjectStore`
  （`CreateProject` / `UpdateProject` / `DeleteProject` / `GetProject` / `ListProjects`），并加入
  `Tx`，因此项目读写可以与其他实体共享同一事务边界。
- `internal/storage/sqlite/projects.go`：
  - `CreateProject` 先走 `domain.Project.Validate()`，缓存类型用 `marshalSlice` 写 JSON；
  - `UpdateProject` 以 `WHERE id = ? AND deleted_at IS NULL` 更新，影响行数为 0 返回
    `storage.ErrNotFound`（更新已删除项目没有业务意义）；
  - `DeleteProject` 只写 `deleted_at` 与 `updated_at`，重复删除返回 `ErrNotFound`；
  - `GetProject` / `ListProjects` 默认过滤已删除记录，列表顺序固定为
    `created_at ASC, id ASC`（项目是配置对象，用户按建立顺序浏览；与任务列表的倒序不同）；
  - **读取路径不做枚举校验**：`env_type` 按原值还原为 `domain.EnvironmentRef`，未知取值可以原样
    读回（与 `execution_hosts.auth_method` 的读取方式一致），不映射默认值、不当作损坏数据。
- `internal/storage/sqlite/store.go`：`dbStore` 与 `txStore` 均注册 `projectStore`。

---

## 5. 应用层与任务创建

### 5.1 `internal/app/projects.go`：`ProjectService`

| 方法 | 语义 | 错误类别 |
|---|---|---|
| `Create` | 校验并写入新项目 | `ErrProjectInvalidInput` / `ErrProjectStateConflict`（标识已存在） |
| `Update` | 按标识**整体替换**配置（标识不可改） | `ErrProjectInvalidInput` / `ErrProjectNotFound` |
| `Delete` | 逻辑删除 | `ErrProjectNotFound` |
| `Get` | 读取未删除项目 | `ErrProjectInvalidInput`（标识书写非法或为空）/ `ErrProjectNotFound` |
| `List` | 全部未删除项目（不分页） | 存储错误 |

设计取舍：

- **更新是整体替换而不是部分更新**：与执行节点的 `PATCH + nil 表示不修改` 不同。项目配置是一个
  整体，部分更新会让调用方难以判断某次修改后缓存与工作目录到底是什么。
- **更新不影响历史任务**：任务在创建时已把配置快照写进自身 `TaskConfig`，项目变更只影响之后新建
  的任务。这一点写进了 CLI 帮助与前端说明。
- **删除不级联**：历史任务、`code_fetches`、`code_saves` 仍保留 `project_id`，只是不能再被新任务
  关联。

### 5.2 任务创建：必填 + 继承

`internal/app/task.go` 的 `prepareTaskRequest` 新增一条前置阶段：

```text
resolveTaskProject(project_id)
  → 为空 / 书写非法          → ErrTaskInvalidInput
  → 不存在或已逻辑删除        → ErrTaskInvalidInput（错误信息带具体标识）
  → 成功                     → 继承仓库 / 分支 / 环境 / 版本 / 缓存 / 工作目录
```

继承规则固定为"**调用方显式给出的值优先，缺省继承项目**"（`firstNonBlank` / `inheritList`）：

| 字段 | 任务请求为空时 | 任务请求非空时 |
|---|---|---|
| `repository_url` | 用项目的 `RepositoryURL` | 用请求值（仍走 `ParseGitRepositoryURL`） |
| `base_branch` | 用项目的规范化分支 | 用请求值（仍走 `NormalizeGitBranch`） |
| `environment` | 用项目的语言环境名 | 用请求值 |
| `environment_version` | 用项目的版本标签（可空） | 用请求值 |
| `caches` | 用项目的缓存类型 | 用请求值 |
| `work_dir` | 用项目的有效工作目录 | 用请求值 |

`task.Config.ProjectID` 一律写成项目的**规范标识**（服务端回读到的记录 ID），而不是客户端原文。

`internal/app/task_management.go` 的 `CreateTaskFromAPIRequest.normalize` 从
`domain.ValidateProjectID` 改为 `domain.RequireProjectID`，因此 HTTP 入口在参数阶段就会拒绝空项目。

`internal/app/task.go` 新增 `firstNonBlank` / `inheritList` 两个小工具，注释写明了取舍：
**空字符串表示"未覆盖"，因此无法用空值表达"清空项目的缓存或工作目录"**。这是有意的——建任务弹窗
与 CLI 都只提交用户真正改过的字段。

---

## 6. HTTP API 契约

`internal/httpapi/projects.go`，路由在 `internal/httpapi/router.go` 注册：

| 方法 | 路径 | 成功 | 说明 |
|---|---|---|---|
| `GET` / `HEAD` | `/api/v1/projects` | `200` | `{records, total}`，不分页，默认不含已删除项目 |
| `POST` | `/api/v1/projects` | `201` | 返回创建后的真实记录 |
| `GET` / `HEAD` | `/api/v1/projects/{projectID}` | `200` | 单项目详情 |
| `PUT` | `/api/v1/projects/{projectID}` | `200` | 整体替换；标识不可改（不在请求体里） |
| `DELETE` | `/api/v1/projects/{projectID}` | `204` | 逻辑删除，无响应体 |

错误映射（统一 `{"error":{"code","message","request_id"}}`）：

| 情况 | 状态 | code |
|---|---|---|
| 参数非法（标识/名称/仓库/分支/环境/版本/工作目录/缓存类型） | 400 | `invalid_request` |
| 请求体含未知字段（例如客户端试图指定 `deleted_at`） | 400 | `invalid_request` |
| 项目不存在或已逻辑删除（详情 / 更新 / 删除） | 404 | `not_found` |
| 标识已存在 | 409 | `state_conflict` |
| 项目服务未装配 | 503 | `internal_error` |
| 其他存储错误 | 500 | `internal_error`（原因只写日志） |

边界约束：

- 不接受任何客户端指定的时间戳、删除标记、校验状态；
- 路径按段边界判定：`/api/v1/projects/`（尾部空标识）与 `/api/v1/projects/{id}/extra` 都返回 404，
  不会被当成集合路径或子资源；
- 不支持的方法返回 405 并带精确 `Allow` 头；
- 响应不包含任何凭据字段（项目本来就不保存凭据）。

### 6.1 任务创建入口的语义变化

`POST /api/v1/tasks` 的 `project_id` 由可选变为**必填**，未给出时返回 400
（`invalid_request`，message 为"任务必须关联项目"）；给出但不存在时返回 400，message 带具体标识。
响应体的 `project_id` 是服务端回读到的规范标识。

---

## 7. CLI

`internal/cli/project.go` 新增 `agentflow project` 命令组：

```text
agentflow project add    --id --name --repo --base --environment [--environment-version] [--cache] [--workdir]
agentflow project list
agentflow project show   <project-id>
agentflow project update --id --name --repo --base --environment [--environment-version] [--cache] [--workdir]
agentflow project remove <project-id>
```

- `add` / `update` 的 `--id --name --repo --base --environment` 是必需参数（`MarkFlagRequired`）；
- `update` 的帮助明确写出"所有字段按整体替换处理，未给出的字段不会继承原值"，且"项目标识不可修改"；
- `remove` 的帮助明确写出"只标记删除时间，历史任务与代码保存记录仍保留该标识"；
- 命令组在 `internal/cli/root.go` 注册，与 `host` / `task` 同级。

`agentflow task create` 的适配：

- `--project` 由可选变为**必需**；
- `--repo` / `--base` / `--environment` 由必需变为**可省略**，帮助文本改为"省略时使用项目配置"。

这一改动是必要的：项目变为必填后，若 CLI 没有登记项目的能力，纯 CLI 用户将无法创建任何任务。

---

## 8. 前端

### 8.1 类型与请求封装

- `lingting-agent-flow-ui/src/lib/api/types.ts`：新增 `Project`、`ProjectListResponse`、
  `CreateProjectRequest`、`UpdateProjectRequest`；`CreateTaskRequest.project_id` 由
  `project_id?: string` 改为 `project_id: string`（必填）。
- `lingting-agent-flow-ui/src/lib/api/endpoints.ts`：新增 `PROJECTS_PATH`、`projectPath`、
  `fetchProjects`、`createProject`、`updateProject`、`deleteProject`。`deleteProject` **不使用**
  `apiRequest`，因为服务端成功时返回 204 且无响应体（与既有 `deleteTask` / `deleteHost` 的处理
  方式一致）。
- `src/lib/api/index.ts` 补齐导出。

### 8.2 项目页（从占位页升级）

- `src/pages/projects/projects.ts`：纯函数模块，负责展示与表单转换
  （`describeProjectEnvironment`、`describeProjectCaches`、`projectSelectOptions`、`findProject`、
  `splitCacheTypes` / `joinCacheTypes`、`toCreateProjectRequest` / `toUpdateProjectRequest` /
  `toProjectFormValues`、`resolveProjectSelection`）。
  关键语义：环境版本为空显示 `当前可用版本`（`PROJECT_VERSION_FALLBACK`），缓存类型为空显示
  `未声明`（`PROJECT_CACHES_FALLBACK`），**不把空值伪装成某个具体版本或某组缓存**。
- `src/pages/projects/useProjects.ts`：`useProjects()`，`loading` / `ready` / `error` 三态，
  请求票据丢弃迟到响应，失败不伪装成空列表。
- `src/pages/projects/ProjectFormModal.tsx`：新增/修改共用弹窗；`项目标识` 只在新增时可填
  （编辑时 `disabled`），表单校验规则与服务端书写规则一致；缓存类型按行或逗号分隔；环境版本可空并
  说明其含义。
- `src/pages/projects/ProjectsPage.tsx`：真实 `ExTable` + 新增/修改/删除。
  - 列表来自 `GET /api/v1/projects`，**查询失败显示"加载项目失败"+ 重试**，空状态文案与失败文案
    分开（失败时表格空文案是"项目数据加载失败，请重试"，不会诱导用户去新建项目）；
  - 删除使用 `LinkButton` 的 `confirm`，`okText` 明确为"确认删除"，描述写明"删除只标记项目下线：
    历史任务、代码获取与代码保存记录仍保留该标识，任务自身的配置快照也不变"；
  - 页面顶部固定说明项目的作用、标识不可修改的原因与环境版本的空值语义。

### 8.3 创建任务弹窗

- 把原来的自由文本 `<Input name="project_id">` 换成真实项目下拉（`aria-label="关联项目"`），
  数据来自 `fetchProjects`；加载中 / 查询失败 / 空列表三种状态分别呈现，**空列表时根本不渲染下拉**。
- `src/pages/tasks/projectPrefill.ts` 的 `projectTaskPrefill` 定义预填规则：
  - 始终预填仓库地址、基础分支、工作目录；
  - 环境与版本**只在当前执行节点的环境列表已加载**时才预填；
  - 该项目环境在当前节点没有可用版本时**不预填环境**（预填只会制造必然失败的配置，并掩盖
    "这个环境暂不可用"这个真实事实）；
  - 项目锁定的版本若不是该环境的可用版本，只预填环境、不猜版本。
- `CreateTaskModal` 在 `handleValuesChange` 中处理 `project_id`（应用预填）与 `host_id`
  （清空环境与版本，保持既有行为），并用一个 `useEffect` 在环境列表就绪后补齐预填，覆盖
  "先选项目、后选节点"的顺序；已经选过环境时不覆盖用户的选择。
- 提交前新增 `resolveProjectSelection` 校验：**项目列表未加载完成 / 空标识 / 标识不在列表中**
  三档分别给出可读原因并阻止提交，且请求体里的 `project_id` 使用校验通过的记录标识。
- 弹窗文档注释从"三类外部标识"更新为"四类"，把项目列为第一项。

---

## 9. 完整调用链

```text
用户登记项目
  Web:  /projects → ProjectFormModal → createProject/updateProject/deleteProject
        → POST|PUT|DELETE /api/v1/projects[...] → httpapi.projectHandler
  CLI:  agentflow project add|update|remove → cli.withProjectService
        → app.ProjectService.Create/Update/Delete
  → domain.NewProject（NewProjectConfig + 仓库/分支校验）
  → storage.ProjectStore.CreateProject/UpdateProject/DeleteProject
  → sqlite projects 表

用户创建任务
  Web:  CreateTaskModal → fetchProjects → 选择项目 → projectTaskPrefill 预填
        → resolveProjectSelection → createTask(project_id, ...)
  CLI:  agentflow task create --project <id>
  → POST /api/v1/tasks → httpapi.taskHandler.create → app.CreateTaskFromAPI
  → app.TaskService.prepareTaskRequest
      → domain.RequireProjectID → app.resolveTaskProject → storage.GetProject
      → 继承仓库/分支/环境/版本/缓存/工作目录 → ParseGitRepositoryURL / NormalizeGitBranch
      → resolveHost / ensureRoles（既有逻辑不变）
  → 单事务写 requirements + tasks + task.created 事件

任务执行阶段（本轮未改动）
  project_id 继续作为项目级缓存卷 scopeKey（containers.go）
                  与远程管理工作目录名（execenv.go / docker.Layout.ProjectDir）
```

---

## 10. 测试与验证（实际执行）

### 10.1 Go

| 命令 | 结果 |
|---|---|
| `gofmt -l internal cmd tests` | 无输出（全部已格式化） |
| `go vet ./...` | 通过，无问题 |
| `go test -count=1 ./tests/unit/... ./tests/integration/local/...` | 全部通过 |
| `go test -count=1 ./tests/e2e/...` | 全部通过 |
| `go test -count=1 ./tests/...`（最终全量） | **43 个测试包全部通过，0 失败、0 跳过** |

本轮新增的 Go 测试：

| 文件 | 覆盖 |
|---|---|
| `tests/unit/domain/project_test.go`（7 个用例） | 归一化（标识小写、`origin/` 前缀、缓存去重）、12 类非法输入、必须使用语言环境、环境版本可空、逻辑删除只写一次、`RequireProjectID` 与 `ValidateProjectID` 的语义差别、标识长度上限 |
| `tests/unit/store/projects_test.go`（9 个用例） | 字段往返、缓存数组往返与自定义工作目录、重复标识 → `ErrConflict`、不存在 → `ErrNotFound`、整体更新、更新不存在、删除是逻辑删除（物理行仍在、重复删除失败、已删除不可更新）、列表过滤已删除、未知环境类型原样读回 |
| `tests/unit/app/project_test.go`（8 个用例） | 创建/读取、重复标识 → `ErrProjectStateConflict`、11 类参数错误、不存在与已删除都按不存在处理、整体替换更新、更新不存在、列表不含已删除、**任务创建的三种项目失败分支 + 已删除项目不可关联** |
| `tests/unit/app/task_management_test.go`（+2 个用例） | 未给出的执行配置从项目继承；显式给出的值优先且不改变项目关联 |
| `tests/unit/httpapi/projects_test.go`（9 个用例） | 完整生命周期（201/200/204/404）、7 类 400、未知字段被拒绝、重复标识 409、未知项目 404（详情/更新/删除）、405 + `Allow` 头、路径形状 404、未装配服务 503、**HTTP 建任务接口要求真实项目并在响应中回显项目标识** |
| `tests/e2e/cli/project_test.go`（6 个用例） | `project add|list|show|update|remove` 全链路、3 类非法输入、重复标识、**`task create` 省略 `--repo/--base/--environment` 时从项目继承**、缺少 `--project` 失败、项目不存在失败 |

为适配"任务必须关联项目"而修改的既有测试（全部属于必要的契约同步，不是放松断言）：

- `tests/unit/app/task_management_test.go`、`tests/unit/httpapi/tasks_test.go`、
  `tests/integration/local/httpserver/tasks_test.go`：fixture 增加一条真实项目记录，创建请求体带
  `project_id`；参数校验用例新增"项目标识为空""项目不存在"两档。
- `tests/e2e/cli/task_test.go`：6 处 `task create` 增加 `--project`，`seedTaskPrerequisites`
  预置项目；`TestTaskCreateRejectsMissingConfiguration` 的非法输入从"省略环境"改为
  "显式给出非法环境名"——因为"省略环境"现在合法（继承项目）。
- `tests/unit/store/migrate_test.go`、`tests/integration/local/database/workflow_test.go`：
  schema 版本 24 → 25，并在旧库模拟中加入 `DROP TABLE projects`。
- `tests/internal/testkit/project.go`（新增）：`testkit.CreateProject` 与项目默认常量，避免各测试
  包各写一套项目参数。

### 10.2 前端

| 命令 | 结果 |
|---|---|
| `pnpm test:unit:safe` | **582 通过 / 0 失败**（27 个测试文件） |
| `pnpm test:e2e:safe` | **214 通过 / 0 失败** |
| `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | 通过 |
| `pnpm build` | 通过（产物写入 `internal/webui/dist`） |

新增前端测试：

- `tests/unit/projects.test.ts`（24 个用例）：项目路径与编码、四类请求的方法/路径/请求体、
  展示语义（空版本、空缓存）、下拉选项、查找、表单转换（新增省略空字段 / 更新不含标识 / 回填）、
  项目选择三档校验、**预填规则四种情形（含"环境无可用版本时不预填"）**。
- `tests/e2e/projects.spec.ts`（7 个用例）：真实字段展示与中性占位、空状态、查询失败不伪装成空
  列表、新增使用 POST 并刷新列表、标识冲突保留真实错误、修改使用 PUT 且标识不可编辑/不在请求体、
  删除需显式确认并写明后果。
- `tests/e2e/task-create-selectors.spec.ts`（+2 个用例）：项目查询失败时显示真实原因并阻止提交、
  没有项目时提示先新增且不伪造默认项目；`fillCommonFields` 现在包含项目选择，第一个用例额外断言
  提交体的 `project_id` 是选择器返回的真实标识。
- 既有 E2E 适配：`layout.spec.ts` 的项目页正文断言从占位文案改为页面固定说明"项目的作用"；
  `task-launch.spec.ts`、`tasks.spec.ts` 的创建任务用例增加项目 stub 与项目选择。

### 10.3 浏览器验证

**未执行**。用户未提供地址，也未要求访问页面；按项目规范不主动使用 `chrome-devtools`。

---

## 11. 剩余的语义变化与风险

| 项 | 说明 | 处理 |
|---|---|---|
| R1 破坏性变更 | `POST /api/v1/tasks` 与 `agentflow task create` 的 `project_id` / `--project` 变为必填；历史客户端（脚本、自动化）会开始收到 400 | 属于用户明确选定的规则。已在 README/ROADMAP 与本次文档中写明；`project_id` 为空的历史任务记录保持原样，读取层不把空值当作默认项目 |
| R2 `--repo/--base/--environment` 语义变化 | 由"必填"变为"省略即继承" | 显式给出仍然生效且优先；CLI 帮助已同步 |
| R3 `ProjectConfig.SourceProvider` / `CodeSaver` 被删除 | 两个字段零调用者，保留会成为永远为 NULL 的列 | 实际的代码获取与保存由固定实现承担，不可按项目选择；无外部读取方受影响（全仓库搜索确认） |
| R4 环境版本可空 | 项目不再必须锁定版本标签 | 与 `TaskConfig` 对齐；版本存在性由执行阶段解析，项目记录不猜版本 |
| R5 项目删除后历史引用成为孤立 ID | 任务 / `code_fetches` / `code_saves` 仍保留该标识 | 这是既有数据库规范允许的（不声明外键、保留历史）；任务的配置快照不依赖项目继续存在 |
| R6 更新是整体替换 | CLI / HTTP 的 `PUT` 未给出的字段会被清空（环境版本、缓存、工作目录回到默认/空） | 在 CLI 帮助、HTTP DTO 注释与前端弹窗中均写明；前端始终提交完整字段，不会误清空 |
| R7 项目标识不可修改 | 它同时是缓存共享范围键与远程管理目录名 | 编辑时标识输入框禁用；服务端把标识放在路径里而不是请求体里 |
| R8 已有真实远程证据未覆盖本项目改动 | 本轮未在真实远程 Docker 上重跑端到端链路；所有远程相关链路只经过既有替身/既有真实远程用例（全量 `go test ./tests/...` 通过，其中 `tests/integration/remote/**` 为真实 SSH + 真实 Docker） | 远程链路本身未被修改，`project_id` 的传递点在 `execenv` / `containers` 中保持不变（既有 `execenv` 用例断言 `ProjectID` 透传到容器创建） |

---

## 12. 未做的事（明确边界）

- 没有引入组织、团队、成员权限、项目模板、多仓库编排或通用插件框架；
- 没有让项目承载或注入 Git 凭据。这**不是待补缺口**：私有仓库认证的用户自定义镜像方案已于
  2026-10-29 定为产品边界（见 `AGENTS.md` 的"私有仓库认证的产品边界"），项目配置永远不承载凭据；
- 没有让项目改写已创建任务的配置快照；
- 没有在项目侧发起任何远程 / Git / Docker 操作，项目接口是纯持久化操作；
- 没有新增角色变更入口、环境构建入口等其它 P4 遗留项；
- **没有把 P4-M5 标记为完成**，也没有重复进行缺少真实数据的性能评估。

---

## 13. 下一项建议的常规功能工作

P4-M1 盘点的 P0 / P1 / P2 / P3 缺口在本轮之后**已全部闭合**，因此下一项工作不能从旧清单里挑，
必须先做一次有代码证据的重新盘点。按现有代码，候选方向（需要先核实再立项，不在本文档中预先批准）：

1. **任务详情页的信息密度与可操作性复核**：任务详情已累积 9 个面板（收尾、代码保存、续跑资格、
   重试、容器处置、步骤/尝试/会话、事件时间线、修复周期、自动刷新）。是否存在"同一事实在多处
   重复展示"或"用户找不到关键动作"的真实问题，需要按实际页面结构核实，而不是凭感觉重排。
2. **创建任务表单的字段收敛**：项目引入后，弹窗里仓库地址、基础分支、工作目录与环境版本都能从
   项目继承，但表单仍要求用户逐个确认。是否应该把这些字段折叠为"继承自项目，可展开覆盖"，需要
   先核实真实使用路径。
3. ~~**私有仓库 Git 凭据支持**~~：**已由用户明确决定不纳入产品范围（2026-10-29）**，从候选清单
   中移除。AgentFlow 不做凭据存储、注入、轮换与撤销；需要私有仓库的用户自行维护执行镜像里的认证
   环境，`source.GitCredentialProvider` / `codesaver.GitCredentialProvider` 保持为空接口占位。
   见 `AGENTS.md` 的"私有仓库认证的产品边界"。
4. ~~**`verification_results` 历史遗留模型清理**~~：**已于 2026-10-29 完成**（领域类型、存储接口
   与唯一的生产读取方已删除，遗留表与历史数据保留，未新增删除迁移），因此不再是候选。

优先级建议：先做第 1 项（成本最低、直接影响已完成能力的可用性），第 2 项紧随其后；第 1、2 项都要
先补代码核实与验收标准。`P4-M5` 只有在出现真实使用数据（`tasks.status`
分布、队列长度、排队等待分位数，"列表卡顿"或"任务长期停在 `queued`"的实际报告）时才重新启动。

