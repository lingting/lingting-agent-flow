# M1：设置中心、环境管理与固定角色

> 状态：**pending**（子任务 V2-M1-E1、V2-M1-E2 已完成）。
> 前置：M0 completed。参考：[`../ROLE-DESIGN.md`](../ROLE-DESIGN.md)、
> [`../ENVIRONMENT-MANAGEMENT.md`](../ENVIRONMENT-MANAGEMENT.md)、[`../ARCHITECTURE.md`](../ARCHITECTURE.md) §8。

## 0. 实施进度

| 子任务 | 内容 | 状态 |
|---|---|---|
| V2-M1-E1 | Settings 内嵌环境管理、环境写 API、后台构建、构建日志 | **已完成**（2026-11-02） |
| V2-M1-E2 | 固定四角色、角色配置持久化、角色配置服务、角色管理与角色 API | **已完成**（2026-11-02） |
| 其余 | 执行层工具限制（Pi 工具策略真正生效）、基础配置只读查看 | pending |

E1 的交付范围：`GET /api/v1/environments`、`GET /api/v1/environments/{type}/{name}`、
`GET .../versions/{v}`、`GET .../versions/{v}/dockerfile`、`GET .../versions/{v}/build-log`、
`GET .../versions/{v}/build-status`、`POST /api/v1/environments/init`、
`POST .../versions`、`POST .../build`；写路径全部复用 `app.EnvironmentService`，构建由
`app.EnvironmentBuildManager` 在 `internal/lifecycle` 中后台执行（受理去重、停止取消、启动收敛）。
前端把环境管理内嵌设置页并移除独立环境导航入口。

E2 的交付范围：`domain` 定义四个固定角色类型与固定 Role ID，内置默认 Prompt 与默认模型；
`app.RoleSeeds` 由内置定义生成种子，启动时按固定 ID 补齐缺失角色、不覆盖已有配置；
`app.RoleService` 提供 `List` / `Get` / `Update`（仅 `system_prompt`、`model`、`enabled`）；
`GET /api/v1/roles`（响应兼容旧版）、`GET /api/v1/roles/{roleID}`、`PUT /api/v1/roles/{roleID}`
（无 `POST` / `DELETE`）；YAML 不再承担角色定义；设置页承载角色列表与编辑对话框，工具策略只展示。
E2 明确**不**实现工具策略的编辑与执行层限制。

M1 整体状态仍为 **pending**：上面的"其余"项未满足退出条件，不得把 E1 / E2 的完成当作 M1 完成。
特别是工具策略目前仍只落库、不生效，角色配置的"真正生效"仍缺执行层限制。

## 1. 目标

把「设置」从占位页变成可用能力，并把角色从"只读声明"变成"可配置且真正生效"：

1. Settings 页面具备实际功能（环境管理 + 角色管理 + 基础配置只读）。
2. 环境管理提供受控写入口（新版本、构建、日志），历史版本不可覆盖。
3. 固定四个角色，不可增删；Prompt / 工具策略 / 模型可编辑并持久化到 SQLite。
4. 角色权限在 Agent 执行层**真正实施**，不只是 Prompt 约束。

## 2. 范围

### 2.1 Domain

- `RoleType` 增加 `requirement_analyst`；四个固定 Role ID 常量与"不可增删"校验。**已完成（E2）**：
  四个类型与四个固定 ID 在 `internal/domain/role.go` 定义，`RoleType.Validate` 只接受这四个类型。
- 角色配置校验：`system_prompt`、`tools_policy_json`、`model`、`enabled` 的合法性与最小权限默认值。**部分完成（E2）**：
  内置默认角色与最小权限默认值已落地（`internal/domain/role_defaults.go`）；`Update` 只对可编辑字段做领域校验，
  角色新增/删除不开放，因此不再有配置文件侧的完整角色校验。

### 2.2 App

- `RoleConfigService`：读取四角色、补齐缺失角色、更新可改字段、生成调用期快照。**已完成（E2）**：
  扩展现有 `app.RoleService`（`List` / `Get` / `Update`），补齐缺失角色由 `app.RoleSeeds` + `SeedRoles`
  在启动时按固定 ID 完成；调用期快照沿用既有 `Role.Snapshot()`，未新增第二套存储。
- `EnvironmentService` 的写路径按需补齐**幂等**入口（不新增第二套环境服务）。**已完成（E1）**：写路径
  直接复用现有 `EnvironmentService`，新增的只有后台构建管理器与 `ReadBuildLog`。

### 2.3 HTTP

- `GET /api/v1/roles`、`GET /api/v1/roles/{roleID}`、`PUT /api/v1/roles/{roleID}`。**已完成（E2）**：
  列表响应保持旧字段不变；更新只接受 `system_prompt` / `model` / `enabled`，未知字段 400，无 `POST` / `DELETE`。
- 环境写入口（方向见 `ENVIRONMENT-MANAGEMENT.md` §5），DTO 按当时方法签名核实后确定。**已完成（E1）**：
  实际交付路径见该文档 §5 与 `docs/http-api.md` §5。

### 2.4 执行层

- Pi 调用按快照应用工具允许/拒绝列表；无法通过 CLI 参数表达的，由执行环境实施。**未完成（pending）**：
  `tools_policy_json` 仍只落库、不生效，本项是 M1 剩余的主要缺口。
- 每次调用的生效策略写入 Session 快照；配置修改不影响在途会话。**部分完成（E2）**：会话仍按创建时读取
  的角色配置写入快照，配置修改不影响在途调用；但"生效的策略"在执行层尚未实施，因此快照里的策略目前
  不代表真实限制。

### 2.5 前端

- Settings 承载环境管理与角色管理；移除独立环境导航入口。**环境部分已完成（E1）**：设置页已承载
  环境管理，独立环境页面与导航入口已删除。**角色部分已完成（E2）**：设置页新增角色列表与编辑对话框。
- 环境页支持版本列表、构建状态、构建日志、新建版本；构建失败原因如实展示。**已完成（E1）**：
  构建状态额外区分“结果不确定”（`uncertain`），并用独立提示展示。
- 角色编辑只暴露可改字段；保存失败显示服务端原因，不伪造成功。**已完成（E2）**：工具策略只展示并标注
  “暂不可编辑”，保存结果取服务端重新读取的返回值。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 四个角色不能新增或删除（**已满足**，E2） | 无 `POST /roles`、无 `DELETE /roles/{id}`（均返回 405）；`RoleType.Validate` 只接受四个固定类型，角色集合固定 |
| 角色配置修改能持久化（**已满足**，E2） | `PUT /api/v1/roles/{roleID}` 只更新 `roles` 行的 `system_prompt` / `model` / `enabled`，响应取重新读取结果；启动补齐不覆盖已有行 |
| 历史 Session 保留原角色快照（**已满足**，E2 未改动该路径） | 快照只在会话创建时由 `Role.Snapshot()` 写入；没有任何代码在会话创建后回写 `role_snapshot_json` |
| 规划角色不能写入项目代码 | 工具限制在执行层生效；容器只读为 M2 的最终边界 |
| 现有 V1 任务工作流不回归失败（**待人工确认**） | E2 只改角色定义来源、角色种子与角色接口，未改动计划 / 开发 / 验证 / 收尾调用链；接口兼容性由任务创建仍使用角色列表完成核验 |
| 环境版本只追加（**已满足**，E1） | 提交不同内容产生新版本号，旧版本内容、哈希与构建元数据不变；提交内容与某个成功版本完全一致时复用该版本号，不产生重复版本 |

## 4. 验收方式（默认不自动执行）

- 阅读代码与 `git diff`，核对：角色更新字段白名单、快照生成点、Pi 参数构造点、
  环境写入口的前置校验、历史版本不可覆盖的写入路径。
- `gofmt`、`go vet ./...`；前端 `pnpm lint` / 构建（不触发测试的部分）。
- 若用户明确要求测试，则覆盖：角色更新与快照、非法字段拒绝、环境版本追加与构建状态、
  Pi 参数包含工具限制、前端角色编辑禁用态与错误展示。

## 5. 风险与待决

1. **Pi CLI 是否有工具限制参数未知**。若没有，必须由执行环境实施（例如规划阶段只读容器，
   或禁用写入类工具的包装），不得退化为只改 Prompt。
2. ~~角色配置迁移会影响现有配置文件中的角色段读取路径~~ **已定稿（E2）**：角色定义改为代码内置，
   `Config.Roles` 与 `roles[].tools_policy.mode` 校验已移除，YAML 不再声明角色；配置加载器仍拒绝未知字段，
   因此已存在的本地配置必须删除 `roles:` 段（`configs/example.yaml` 与本地 `configs/agentflow.yaml` 已同步）。
3. ~~环境构建是长操作：并发/取消语义未定~~ **已定稿（E1）**：同一节点同一环境同时只允许一个后台
   构建；不提供单独取消接口，取消只发生在服务停止时；重启时把遗留 `building` 收敛为 `uncertain`。

## 6. 明确不做

- 不做 PlanningSession、不做规划容器（M2）。
- 不做需求工作台（M3）。
- 不提供角色增删、权限体系、用户管理。
- 不在 Settings 中管理任何凭据。
