# V2 环境管理与设置中心

> 状态：V2 设计基线。前置阅读：[`ARCHITECTURE.md`](./ARCHITECTURE.md) §3、§6，[`ROLE-DESIGN.md`](./ROLE-DESIGN.md)。

## 1. 现状事实（M0 核实）

| 能力 | 现状 | 证据 |
|---|---|---|
| 环境定义与版本 | 应用层已具备：`EnvironmentService.Init`、`CreateVersion`、`Build`、`DownloadBuildLog`、`ReadDockerfile` | `internal/app/environments*.go` |
| 环境查询 API | 已有：环境列表、单版本详情、真实构建状态与服务端计算的 `usable` | `internal/httpapi/environments.go`、`app.EnvironmentQueryService` |
| 环境写 API | **缺失**：无创建版本、构建、上传/编辑 Dockerfile 的 HTTP 入口（G3） | 同上 |
| 环境 CLI | 已有：`agentflow docker env` 系列命令可用 | `internal/cli/docker_env*.go` |
| 环境 Web 页 | 存在独立页面（诊断为主），未纳入设置中心 | `src/pages/environments/` |
| 设置页 | 占位 | `src/pages/settings/SettingsPage.tsx` |
| 角色 | 只在首次初始化从配置导入，接口只读 | `app.RoleService`、`internal/httpapi/roles.go` |

结论：环境**能力**基本齐备，缺的是**受控的写入口**与**内嵌管理 UI**；不需要重建一套环境服务。

## 2. 设置中心结构（M1 交付）

```text
设置
├── 环境管理
│   ├── 环境列表（type / name / 当前可用版本 / 构建状态）
│   ├── 环境详情（版本列表、Dockerfile 只读查看、构建状态与日志）
│   ├── 新建语言环境定义
│   ├── 编辑环境定义（生成新版本，不覆盖历史版本）
│   ├── 构建指定版本（异步状态，失败原因如实显示）
│   └── 项目默认环境选择（写回项目，不隐式批量改历史任务）
├── 角色管理（四个固定角色，见 ROLE-DESIGN.md）
└── 基础配置查看（只读：数据库路径、监听地址、调度并发、Session 根目录）
```

规则：

- 环境版本**只追加**，历史版本与构建元数据不被覆盖；已有版本不可编辑，编辑即新建版本。
- 构建是有副作用的长操作：必须有明确状态（`pending` / `building` / `succeeded` / `failed`）、
  可下载日志、失败原因，且不阻塞 HTTP 请求线程模型（后台服务由 `internal/lifecycle` 管理）。
- 所有写操作经由 `app.EnvironmentService`，Handler 只做协议转换；CLI 与 Web 共用同一套方法。
- 前端只显示服务端返回的 `usable`，不得由 `version` 或 `image_ref` 自行推断可用性。

## 3. 配置迁移边界

判别标准只有一条：**是运行时启动参数，还是业务数据。**

| 类别 | 内容 | V2 决策 |
|---|---|---|
| 必须留在配置文件 | 数据库路径、服务监听地址与关闭超时、日志级别、Session 根目录/工作目录、SSH 相关运行参数、调度并发与重试上限 | 保留在 `configs/example.yaml` 与配置文件加载逻辑中 |
| 迁移为数据库业务数据 | 角色定义与 Prompt、工具策略、模型配置；环境定义与版本选择（已在 SQLite/远程布局中） | 由内置默认值 + SQLite 覆盖 |
| 明确不进数据库 | 模型凭据、SSH 私钥、私有仓库凭据 | 与 V1 相同：不入库、不入日志、不入仓库 URL |

落实方式：

- 角色定义从 YAML 迁移为**代码内置默认值 + SQLite 用户配置**（`roles` 表已存在，无需新迁移）。
  首次初始化仅补齐缺失角色；配置文件中的角色段被移除或降级为"首次种子"。
- 环境定义不再要求用户手工编辑业务配置文件；已有文件布局与远程管理工作区约定不变。
- 删除任何配置项或配置文件前必须先核实代码引用与真实运行方式（`grep` 引用、`config.Load`
  的使用点、`Makefile` 与 CI 调用），不得因为名字含 `config` 就删除。

## 4. 与项目的关系

- 项目继续引用"语言环境类型 + 名称 + 版本"，任务创建时继承并写入 `config_json` 快照。
- 项目默认执行节点来自 `projects.default_host_id`（`0028`）；未指定时必须由用户选择，不猜。
- 环境变更不影响已创建任务：历史任务使用自己的配置快照，不回写成"最新环境"。
- 项目环境可用性检查读真实构建状态；不可用时必须阻止启动并说明原因，不得静默降级到其他版本。

## 5. M1 必须交付的 API（方向）

```text
GET    /api/v1/environments                              已有
GET    /api/v1/environments/{type}/{name}                已有（版本列表）
GET    /api/v1/environments/{type}/{name}/versions/{v}   已有（含 usable）
POST   /api/v1/environments                              新建环境定义 / 新版本（请求体显式声明动作）
POST   /api/v1/environments/{type}/{name}/versions       创建新版本（基于现有版本派生）
POST   /api/v1/environments/{type}/{name}/build          触发构建（返回真实初始状态）
GET    /api/v1/environments/{type}/{name}/versions/{v}/build-log  构建日志
```

- 具体 DTO 与错误码在 M1 实现前按当时的 `app.EnvironmentService` 方法签名核实后确定，不在此预设。
- 写入口必须是幂等或显式版本化的：重复提交不能产生重复版本或重复构建。
- 构建触发失败、节点未通过预检、版本已存在等情形必须返回结构化错误，不得返回 200。

## 6. 明确不做

- 不做环境模板市场、不做跨执行节点自动同步镜像、不做全局 `docker prune`。
- 不覆盖历史环境版本，不为"修正"而重写已有版本的 Dockerfile 或镜像标签。
- 不在 Settings 中提供凭据管理（模型凭据、私钥、仓库凭据）。
- 不因为设置页存在入口就放宽服务只监听回环地址的约束。

## 7. 待决问题

1. 环境构建任务的并发与取消语义（同一环境同一时刻是否只允许一个构建），M1 定稿。
2. 项目默认环境选择是新建 `projects.env_version` 必填项，还是沿用"空值=当前可用版本"，M1 定稿。
3. 基础配置查看是否提供"编辑并提供重启提示"，默认倾向：**只读**，修改配置仍由用户改文件并重启。
