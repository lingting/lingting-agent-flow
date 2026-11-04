# V2 环境管理与设置中心

> 状态：V2 设计基线。前置阅读：[`ARCHITECTURE.md`](./ARCHITECTURE.md) §3、§6，[`ROLE-DESIGN.md`](./ROLE-DESIGN.md)。

## 1. 现状事实

> M0 核实了原始缺口；V2-M1-E1（环境管理内嵌化）完成后按当时代码更新如下。

| 能力 | 现状 | 证据 |
|---|---|---|
| 环境定义与版本 | 应用层已具备：`EnvironmentService.Init`、`CreateVersion`、`Build`、`DownloadBuildLog`、`ReadDockerfile`、`ReadBuildLog` | `internal/app/environments*.go` |
| 环境查询 API | 已有：环境列表、环境详情、单版本详情、构建状态、真实构建状态与服务端计算的 `usable` | `internal/httpapi/environments.go`、`app.EnvironmentQueryService` |
| 环境写 API | **已有**（V2-M1-E1，原缺口 G3 已关闭）：初始化工作空间、追加版本、受理后台构建、读取 Dockerfile 与构建日志 | 同上；写路径全部复用 `app.EnvironmentService` |
| 环境构建管理 | **已有**：`app.EnvironmentBuildManager` 实现 `lifecycle.Service`，负责受理去重、后台执行、停止取消与启动收敛 | `internal/app/environments_build_manager.go`、`internal/server/server.go` |
| 环境 CLI | 已有：`agentflow docker env` 系列命令可用 | `internal/cli/docker_env*.go` |
| 环境 Web 页 | **已内嵌设置中心**：独立环境页面与导航入口已移除 | `src/pages/settings/` |
| 设置页 | 已承载环境管理、固定角色管理与基础配置只读查看 | `src/pages/settings/SettingsPage.tsx` |
| 角色 | 内置四个固定角色，配置持久化在 SQLite；列表、详情与 `PUT` 只改 Prompt / 工具策略 / 模型 / 启用状态 | `app.RoleService`、`internal/httpapi/roles.go` |

结论：环境**能力**、**受控写入口**与**内嵌管理 UI**均已具备，没有第二套环境服务；角色配置与
执行层工具限制由 V2-M1-E2 / E3 交付，基础配置只读查看由 V2-M1-E4 交付。

## 2. 设置中心结构

```text
设置
├── 环境管理（V2-M1-E1 已交付）
│   ├── 执行节点选择（环境属于具体节点，不猜默认值）
│   ├── 环境列表（type / name / 当前可用版本 / 最新版本 / 版本数与失败数）
│   ├── 环境详情（版本列表、构建状态、失败与不确定原因、Dockerfile、构建日志）
│   ├── 初始化环境工作空间 / 新建语言环境定义（服务端模板路径）
│   ├── 编辑 Dockerfile 生成新版本（不覆盖历史版本）
│   ├── 构建指定版本（后台执行，状态与结论来自服务端）
│   └── 项目默认环境选择（未交付：待 §7 第 2 项定稿）
├── 角色管理（四个固定角色，见 ROLE-DESIGN.md）—— 已交付（V2-M1-E2 / E3）
└── 基础配置查看（只读：数据库路径、监听地址、调度并发、Session 根目录）—— 已交付（V2-M1-E4）
```

规则：

- 环境版本**只追加**，历史版本与构建元数据不被覆盖；已有版本不可编辑，编辑即新建版本。内容与
  某个已成功构建的版本完全一致时由服务端复用该版本号（`domain.FindReusableVersion`），不产生重复版本。
- 环境定义与版本属于远程环境管理数据，不重新引入 YAML 业务配置。新建语言环境定义走服务端模板路径
  （`POST /api/v1/environments/init`），模板之外的内容通过"编辑 Dockerfile 生成新版本"提交。
- 构建是有副作用的长操作：状态取值为 `pending` / `building` / `succeeded` / `failed` / `uncertain`，
  可读取日志与失败原因，且不阻塞 HTTP 请求线程模型（后台服务由 `internal/lifecycle` 管理）。
- 构建并发与取消已定稿：同一节点同一环境同时只允许一个构建，重复请求返回 409；不提供单独取消接口，
  取消只发生在服务停止时，并在停止前把在途构建收敛为终态。服务重启时把遗留 `building` 记录收敛为
  `uncertain`（无法确认镜像是否已生成），既不报成功也不伪造失败。
- 所有写操作经由 `app.EnvironmentService`，Handler 只做协议转换；CLI 与 Web 共用同一套方法。
- 前端只显示服务端返回的 `usable` 与 `usable_for_remote_task`，不得由 `version` 或 `image_ref`
  自行推断可用性；`202`（已受理）不得显示为构建成功。

- 基础配置只读：四项取值来自服务启动时实际生效的配置快照（`GET /api/v1/settings/config`），请求期间
  不重新读取配置文件；监听地址是包含 `--address` 覆盖的最终值，Session 根目录取真实写入目录
  （数据库目录下的 `sessions`），不映射 `workspace.worktree_root`；接口不返回 SSH 路径、凭据或完整
  配置对象，也不提供编辑、保存、热更新与重启入口。

## 3. 配置迁移边界

判别标准只有一条：**是运行时启动参数，还是业务数据。**

| 类别 | 内容 | V2 决策 |
|---|---|---|
| 必须留在配置文件 | 数据库路径、服务监听地址与关闭超时、日志级别、Session 根目录/工作目录、SSH 相关运行参数、调度并发与重试上限 | 保留在 `configs/example.yaml` 与配置文件加载逻辑中 |
| 迁移为数据库业务数据 | 角色定义与 Prompt、工具策略、模型配置；环境定义与版本选择（已在 SQLite/远程布局中） | 由内置默认值 + SQLite 覆盖 |
| 明确不进数据库 | 模型凭据、SSH 私钥、私有仓库凭据 | 与 V1 相同：不入库、不入日志、不入仓库 URL |

落实方式：

- 角色定义从 YAML 迁移为**代码内置默认值 + SQLite 用户配置**（`roles` 表已存在，无需新增结构）。
  首次初始化仅补齐缺失角色；配置文件中的角色段被移除或降级为"首次种子"。
- 环境定义不再要求用户手工编辑业务配置文件；已有文件布局与远程管理工作区约定不变。
- 删除任何配置项或配置文件前必须先核实代码引用与真实运行方式（`grep` 引用、`config.Load`
  的使用点、`Makefile` 与 CI 调用），不得因为名字含 `config` 就删除。

## 4. 与项目的关系

- 项目继续引用"语言环境类型 + 名称 + 版本"，任务创建时继承并写入 `config_json` 快照。
- 项目默认执行节点来自 `projects.default_host_id`（`0028`）；未指定时必须由用户选择，不猜。
- 环境变更不影响已创建任务：历史任务使用自己的配置快照，不回写成"最新环境"。
- 项目环境可用性检查读真实构建状态；不可用时必须阻止启动并说明原因，不得静默降级到其他版本。

## 5. 已交付的环境 API（V2-M1-E1）

```text
GET    /api/v1/environments                                             环境列表（含 usable）
GET    /api/v1/environments/{type}/{name}                               环境详情（版本列表）
GET    /api/v1/environments/{type}/{name}/versions/{v}                  单版本（含 usable）
GET    /api/v1/environments/{type}/{name}/versions/{v}/dockerfile       Dockerfile 正文
GET    /api/v1/environments/{type}/{name}/versions/{v}/build-log        构建日志正文
GET    /api/v1/environments/{type}/{name}/versions/{v}/build-status     构建结论（200/202/500）
POST   /api/v1/environments/init                                        初始化工作空间
POST   /api/v1/environments/{type}/{name}/versions                      追加新版本
POST   /api/v1/environments/{type}/{name}/build                         受理后台构建（202）
```

- 与早期方向的两处差异：不采用 `POST /api/v1/environments` 用请求体区分动作，改为 `POST
  /api/v1/environments/init`（模板路径）与 `POST .../versions`（显式版本化追加）；构建结论由新增的
  `GET .../build-status` 表达，`failed` 与 `uncertain` 使用不同错误码（`environment_build_failed` /
  `environment_build_uncertain`），在途用 `202`。
- DTO 与错误码以 `docs/http-api.md` §5 与 `internal/httpapi/environments.go` 为准。
- 写入口幂等性与版本化：内容一致时复用既有成功版本（不产生重复版本号）；同一节点同一环境在途构建
  期间重复提交返回 409（`environment_build_in_progress`）。
- 参数、节点、版本与预检失败都返回结构化错误，不使用 200 表示远程操作失败。

## 6. 明确不做

- 不做环境模板市场、不做跨执行节点自动同步镜像、不做全局 `docker prune`。
- 不覆盖历史环境版本，不为"修正"而重写已有版本的 Dockerfile 或镜像标签。
- 不在 Settings 中提供凭据管理（模型凭据、私钥、仓库凭据）。
- 不因为设置页存在入口就放宽服务只监听回环地址的约束。

## 7. 待决问题

1. ~~环境构建任务的并发与取消语义（同一环境同一时刻是否只允许一个构建）~~ **已定稿（V2-M1-E1）**：
   同一节点同一环境同时只允许一个后台构建，重复请求返回 409；不提供单独取消接口，取消只发生在服务
   停止时；停止前把在途构建收敛，进程重启时把遗留 `building` 收敛为 `uncertain`（结果无法确认）。
2. 项目默认环境选择是新建 `projects.env_version` 必填项，还是沿用"空值=当前可用版本"——**仍未定稿**，
   V2-M1-E1 未实现该功能。
3. ~~基础配置查看是否提供"编辑并提供重启提示"~~ **已定稿（V2-M1-E4）**：只读。修改配置仍由用户改文件
   并重启服务，页面只展示启动阶段生效的四项运行参数，不提供编辑、保存、热更新或重启入口。
