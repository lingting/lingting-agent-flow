# M1：设置中心、环境管理与固定角色

> 状态：**completed**（2026-11-03）。子任务 V2-M1-E1～V2-M1-E4 已完成；退出条件「现有 V1 任务工作流不
> 回归失败」先于 2026-11-02 真实远程端到端运行中**未通过**（工作流在规划阶段步骤被判验证失败后 `blocked`，
> 未进入开发阶段，详见 §7），修复后于 2026-11-03 真实远程端到端运行中**通过**（详见 §8）。
> 前置：M0 completed。参考：[`../ROLE-DESIGN.md`](../ROLE-DESIGN.md)、
> [`../ENVIRONMENT-MANAGEMENT.md`](../ENVIRONMENT-MANAGEMENT.md)、[`../ARCHITECTURE.md`](../ARCHITECTURE.md) §8。

## 0. 实施进度

| 子任务 | 内容 | 状态 |
|---|---|---|
| V2-M1-E1 | Settings 内嵌环境管理、环境写 API、后台构建、构建日志 | **已完成**（2026-11-02） |
| V2-M1-E2 | 固定四角色、角色配置持久化、角色配置服务、角色管理与角色 API | **已完成**（2026-11-02） |
| V2-M1-E3 | 角色工具策略执行（Pi `--tools` / `--no-tools` / `--no-mcp` / `--no-extensions`）、工具策略编辑 | **已完成**（2026-11-02） |
| V2-M1-E4 | 基础配置只读查看（`GET /api/v1/settings/config`、Settings 只读区域） | **已完成**（2026-11-02） |

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

E3 的交付范围：`domain` 新增 `internal/domain/role_tools.go`（Pi 内置工具名称、能力→工具映射、
角色能力上限 `RoleToolCeiling`、策略校验 `ValidateToolsPolicyForRole`、实际工具计算
`EffectivePiTools`）；`app.ContainerService.RunAgent` 统一把角色类型与 `ToolsPolicy` 换算为工具列表，
`agent.RunRequest.Tools` 由 Pi Runner 统一构造 `--tools <list>` 或 `--no-tools`，并固定附加
`--no-mcp`、`--no-extensions`；`TaskWorkflowService`（Planner）、`StepExecutor`（Developer）、
`ValidatorReviewer`（Validator）三条真实调用路径提交 `role.ToolsPolicy`，与 Session 快照同源；
`PUT /api/v1/roles/{roleID}` 开放 `tools_policy`，越出角色能力上限返回 400；`GET`/`PUT` 角色详情新增
`capability_ceiling`、`available_tools`、`effective_tools` 三个服务端计算字段；设置页可编辑能力、
允许工具与拒绝工具，上限与生效工具只按服务端返回值展示。
验收者的默认策略调整为只读（能力上限 read，不含 execute）：确定性验证命令由
`app.ValidationService` 在容器内执行，模型调用只审阅代码与验证证据。

E3 的退出条件与证据：

- 规划角色（Planner / 需求分析师）调用不含 `bash`、`edit`、`write`：`RoleToolCeiling` 只读、
  `EffectivePiTools` 先与上限取交集；`tests/unit/app/plan_generation_test.go`、
  `tests/unit/app/roles_test.go` 覆盖。
- Developer 的实际工具与配置一致，`deny_tools` 优先：`tests/unit/app/role_service_test.go`、
  `tests/unit/httpapi/roles_test.go` 覆盖。
- 验收者不获得写入能力：`tests/unit/app/validation_review_test.go` 覆盖。
- Pi 参数构造正确（空工具走 `--no-tools`，MCP 与扩展始终关闭）：`tests/unit/agent/container_runner_test.go`
  覆盖。
- 本次**未运行**上述测试（用户未授权测试），只完成编译、`gofmt`、`go vet ./...`、前端 lint / 类型检查
  与 `git diff --check`。

E4 的交付范围：`app.RuntimeConfig` 是启动阶段生成的只读快照（`database_path`、`listen_address`、
`max_concurrent_tasks`、`session_root`），由 `internal/server` 在启动 HTTP 前用已加载并完成覆盖的
`config.Config` 与 `resolveAddress` 的结果构造一次，再经 `httpserver` 注入 `httpapi`；
`GET /api/v1/settings/config` 只做协议转换，请求期间不重新读取 YAML、不扫描磁盘、不建立远程连接，
快照缺失时返回 `503` 而不是默认值。`database_path` 与 `sqlite.Open` 收到的路径表达式一致；
`session_root` 与 `SessionService.localRoot` 同源（`app.SessionRoot` 由数据库目录派生的 `sessions`
目录再绝对化），不使用 `workspace.worktree_root`。Settings 页新增"基础配置"只读区域，加载中、
失败与成功三种状态分别展示，不提供编辑、保存、重置或重启入口；`/api/v1/system/info` 字段与语义不变。

E4 的退出条件与证据：

- 四项取值来自服务启动时实际生效的配置：`internal/app/runtime_config.go`（`NewRuntimeConfig`）、
  `internal/server/server.go`（`startHTTP` 生成一次快照并注入）、`internal/httpapi/settings.go`（只读入口）。
- 监听地址包含启动参数覆盖：`resolveAddress` 先应用 `--address`，未覆盖时回落到 `cfg.Server.Address()`。
- Session 根目录来自真实写入路径：`internal/app/sessions.go` 的 `resolveSessionRoot` 同时供
  `NewSessionService` 与 `NewRuntimeConfig` 使用。
- 前端展示服务端真实值、失败时展示服务端原因：`src/pages/settings/BasicConfigPanel.tsx`、
  `src/lib/api/endpoints.ts`（`fetchSettingsConfig`），页面不直接调用 `fetch`。
- 本次**未运行**测试（用户未授权），只完成 `gofmt`、`go vet ./...`、前端 `pnpm lint`（fmt / lint / tsc）、
  前端生产构建与 `git diff --check`。

M1 整体状态为 **completed**（2026-11-03）：E1～E4 已交付，退出条件「现有 V1 任务工作流不回归失败」由
2026-11-03 的真实远程端到端运行确认（规划→开发→确定性验证→语义审查→交付全部真实完成，见 §8）。
修复前的失败现场保留在 §7，不因最终通过而改写。
工具策略自 E3 起在真实调用链路上生效，但"项目代码只读"的最终文件系统边界仍由 M2 的只读容器承担。

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
  列表响应保持旧字段不变；更新接受 `system_prompt` / `tools_policy` / `model` / `enabled`，未知字段 400，
  无 `POST` / `DELETE`。**E3 补充**：`tools_policy` 越出角色能力上限（规划角色写/执行、验收者执行、
  未知工具名）返回 400；详情响应新增服务端计算的 `capability_ceiling` / `available_tools` /
  `effective_tools`，策略无法换算为工具时返回 500 而不是返回"无限制"。
- 环境写入口（方向见 `ENVIRONMENT-MANAGEMENT.md` §5），DTO 按当时方法签名核实后确定。**已完成（E1）**：
  实际交付路径见该文档 §5 与 `docs/http-api.md` §5。
- 基础运行配置只读入口 `GET /api/v1/settings/config`。**已完成（E4）**：返回 `database_path` /
  `listen_address` / `max_concurrent_tasks` / `session_root` 四项，取自启动阶段的配置快照（含
  `--address` 覆盖）；不与 `/api/v1/system/info` 混合，不提供编辑、保存、热更新或重启入口，快照缺失
  返回 `503`，不返回默认配置。

### 2.4 执行层

- Pi 调用按快照应用工具允许/拒绝列表；无法通过 CLI 参数表达的，由执行环境实施。**已完成（E3）**：
  `app.ContainerService.RunAgent` 用 `domain.EffectivePiTools` 把角色类型与 `ToolsPolicy` 换算为工具
  列表，`pi.ContainerRunner.execute` 统一构造 `--tools <list>`（为空时 `--no-tools`）并固定附加
  `--no-mcp`、`--no-extensions`。工具映射与能力上限只在 `internal/domain/role_tools.go` 定义一次，
  三条调用路径都只提交 `role.ToolsPolicy`。
- 每次调用的生效策略写入 Session 快照；配置修改不影响在途会话。**已完成（E2 + E3）**：会话在创建时
  按当次读取的角色配置写入快照，`ToolsPolicy` 与快照取自同一个 `domain.Role` 值，因此 Pi 实际工具
  与快照策略一致；配置修改只影响后续新会话。

### 2.5 前端

- Settings 承载环境管理与角色管理；移除独立环境导航入口。**环境部分已完成（E1）**：设置页已承载
  环境管理，独立环境页面与导航入口已删除。**角色部分已完成（E2）**：设置页新增角色列表与编辑对话框。
- 环境页支持版本列表、构建状态、构建日志、新建版本；构建失败原因如实展示。**已完成（E1）**：
  构建状态额外区分“结果不确定”（`uncertain`），并用独立提示展示。
- 角色编辑只暴露可改字段；保存失败显示服务端原因，不伪造成功。**已完成（E3）**：`RoleEditModal`
  可编辑能力、允许工具与拒绝工具，选项全部来自服务端的 `capability_ceiling` / `available_tools`，并展示
  `effective_tools`；保存结果取服务端重新读取的返回值。
- Settings 增加基础配置只读区域：展示数据库路径、监听地址、最大并发任务数与 Session 根目录。**已完成（E4）**：
  `BasicConfigPanel` 经 `src/lib/api/` 的 `fetchSettingsConfig` 读取，加载中 / 失败 / 成功分别展示，
  不出现编辑、保存、重置或重启按钮。

## 3. 退出条件

| 条件 | 判定方式 |
|---|---|
| 四个角色不能新增或删除（**已满足**，E2） | 无 `POST /roles`、无 `DELETE /roles/{id}`（均返回 405）；`RoleType.Validate` 只接受四个固定类型，角色集合固定 |
| 角色配置修改能持久化（**已满足**，E2 + E3） | `PUT /api/v1/roles/{roleID}` 只更新 `roles` 行的 `system_prompt` / `tools_policy_json` / `model` / `enabled`，响应取重新读取结果；启动补齐不覆盖已有行；越出角色能力上限的工具策略返回 400，不落库 |
| 历史 Session 保留原角色快照（**已满足**，E2 未改动该路径） | 快照只在会话创建时由 `Role.Snapshot()` 写入；没有任何代码在会话创建后回写 `role_snapshot_json` |
| 规划角色不能写入项目代码（**工具层已满足**，E3） | 规划角色能力上限只读，Pi 调用只收到 `read`/`grep`/`find`/`ls`，并固定 `--no-mcp` / `--no-extensions`；文件系统级的只读边界仍属 M2 |
| 现有 V1 任务工作流不回归失败（**已满足**，2026-11-03 真实运行） | 2026-11-02 曾失败：任务级必需验证命令被合并进序号 1 的 `plan` 步骤，确定性验证判 `failed`（退出码 1），修复循环对 `plan` 类型判 `unsupported_step`，工作流 `blocked`（§7）。业务语义修正后，规划阶段只做真实 Planner 执行、不跑确定性验证，任务级必需命令只进入开发步骤；2026-11-03 真实远程端到端运行 `TestRemoteE2EFullChain` 通过：`plan` 步骤真实完成、`develop` 步骤执行 `notes-exists`（退出码 0）并通过语义审查，工作流 `completed`（§8） |
| 环境版本只追加（**已满足**，E1） | 提交不同内容产生新版本号，旧版本内容、哈希与构建元数据不变；提交内容与某个成功版本完全一致时复用该版本号，不产生重复版本 |
| Settings 展示真实运行配置且不泄露无关信息（**已满足**，E4） | `GET /api/v1/settings/config` 只返回四项运行参数，取值来自启动快照；不返回 SSH known_hosts 路径、凭据、私钥、模型或仓库凭据与完整配置对象；`/api/v1/system/info` 字段与语义不变 |

## 4. 验收方式（默认不自动执行）

- 阅读代码与 `git diff`，核对：角色更新字段白名单、能力上限校验点、快照生成点、工具列表计算点、
  Pi 参数构造点、环境写入口的前置校验、历史版本不可覆盖的写入路径。
- `gofmt`、`go vet ./...`；前端 `vp fmt` / `vp lint` / `tsc --noEmit`（均不触发测试）。
- E3 已把必要断言写入既有测试文件（`tests/unit/app/{roles,role_service,plan_generation,validation_review}_test.go`、
  `tests/unit/httpapi/roles_test.go`、`tests/unit/agent/container_runner_test.go`），但**未运行**：
  本次任务未获测试授权。
- E4 的核对点：`NewRuntimeConfig` 的三项取值来源（配置快照、`resolveAddress` 结果、`resolveSessionRoot`）、
  快照缺失时的 `503`、Settings 页只读展示与错误提示；已执行的非测试验证为 `gofmt`、`go vet ./...`、
  前端 `pnpm lint`（fmt / lint / tsc）、前端生产构建与 `git diff --check`，未新增测试文件。
- 若用户明确要求测试，则运行上述文件并覆盖：工具策略持久化与上限拒绝、规划/验收者工具限制、
  Pi 参数包含工具限制、环境版本追加与构建状态、前端角色编辑错误展示。

## 5. 风险与待决

1. ~~Pi CLI 是否有工具限制参数未知~~ **已定稿（E3）**：Pi 官方 CLI 提供 `--tools`、`--no-tools`、
   `--no-mcp`、`--no-extensions`。工具限制由 Pi Runner 的参数实施，MCP 与扩展在托管调用中不加载；
   规划阶段的项目代码只读边界仍由 M2 的容器只读挂载兜底。
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

## 7. 真实端到端回归验收记录（2026-11-02）

M1 退出条件「现有 V1 任务工作流不回归失败」以一次真实远程端到端运行判定，结论为 **未满足**，
M1 保持 **pending**。

- 用例与命令：`TestRemoteE2EFullChain`，
  `go test -count=1 -run '^TestRemoteE2EFullChain$' -v ./tests/integration/remote/workflow`（只执行一次，未重试）。
- 结果：**FAIL**（199s）。终局 `WorkflowRun=blocked` / `stage=stopped` /
  `error_class=step_not_repairable`，任务 `recovery_required`，未进入开发阶段，也未进入交付阶段。
- 执行环境：远程 `127.0.0.1:22`（私钥认证，主机密钥校验未关闭）；基础镜像
  `agentflow-base-base:v0001-a253a6178fe7`，语言镜像 `agentflow-language-node:v0001-f02408055e30`；
  容器内 Pi provider `agentflow-test`、模型 `group/free`、thinking `low`（`docker.pi` 覆盖）。
- 可追溯标识：Task `0b4eb524fc2ea3fe01778618dd8275a1`；WorkflowRun
  `723e6f6adfeda469571a5ead8cc3825f`；Attempt `8bb7fe12018acbaed1eac832a3b0abdb`（步骤 1，completed / exit 0）；
  ValidationRun `340e9bd4155623be91538c870ba250b2`（completed / verdict failed / 1 项检查失败）；
  会话 `d4cb5af733905c3eba8e85c7b9fe39f9`（计划生成）、`5350b2a989cbcf4f9dd3beabbfb63bac`（规划阶段步骤），
  两者角色快照均为 `planner`、策略 `{"permissions":["read"]}`、归档成功。
- 测试产物：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-1488447157`
  （`test.log`、`case-4095252777/agentflow.db`、`sessions/`、`validations/`）；远程工作区与容器按既有
  清理机制处置（容器 finalizer `PASS`，无新增残留）。
- 阻塞事实：持久化的计划为 2 个步骤——序号 1 `plan`「生成开发计划」、序号 2 `develop`「创建 NOTES.md 文件」，
  任务级必需命令 `notes-exists`（`sh -c "test -f NOTES.md"`）被合并进**两个**步骤。编排器执行并验证了
  序号 1 的 `plan` 步骤，该命令只能在开发步骤产出文件后成立，因此验证结论为 `failed`（退出码 1）；
  修复循环对 `plan` 类型判 `unsupported_step`，工作流 `blocked`，序号 2 的 `develop` 步骤始终保持 `pending`。
  `code_saves` 表为空，说明既未提交也未推送，交付阶段从未开始；据此判定 Push 结果与本次失败无关。
- 待解决：规划阶段步骤不应承载「只能在开发完成后成立」的必需验证命令（或规划步骤不应进入确定性验证与
  自动修复循环），需先在产品侧确定归属并修复，再次真实运行必须获得新的明确授权。
- 本次未修改生产代码中的角色工具上限、默认策略或 Pi CLI 参数；仅在同一既有测试文件中补充了本次验收
  所需的阶段证据断言（Planner / Developer / Validator 角色快照与工具、ValidationRun 与 `notes-exists`
  退出码、`ReviewStatus`、Attempt 与容器归属、任务分支唯一性与「不自动合并」），未新增测试文件或测试函数。

## 8. 修复记录与最终真实通过（2026-11-03）

§7 的失败不是模型自述失败，而是确定性的业务语义缺陷：任务级代码验收命令被同时分配给规划阶段与开发阶段。
本轮按「规划只规划、开发只验收」重新划分职责，并用同一条真实远程端到端用例验证通过。

### 8.1 首次失败根因（生产代码位置）

1. `internal/domain/plan_generation.go` 的 `BuildPlan` 把完整 `taskVerification` 传给序号 1 的 `StepTypePlan`，
   同时又把同一份规格合并进每个 `StepTypeDevelop`。同一条任务级必需命令因此出现在两个阶段。
2. `internal/domain/workflow_run.go` 的 `checkStepHasRequiredCheck` 对**所有**待推进步骤要求「启用的必需检查」，
   所以只删除规划步骤的验证配置并不足以通过启动准入。
3. `internal/app/task_workflow_exec.go` 的 `runStep` 把工作流里的每个步骤统一交给
   `StepWorkflowService.ExecuteAndValidate`，规划阶段因此走开发—验证闭环。
4. `internal/app/step_workflow_repair.go` 的 `ExecuteAndValidate` 在调用 Planner **之前**执行
   `requireStepVerificationSpec`，并让验证失败进入通用自动修复循环；而领域规则
   （`domain.DecideRepairLoop`）明确不允许修复非开发步骤，于是落入 `unsupported_step`。

### 8.2 实际修改的业务规则

- 规划阶段步骤（`StepTypePlan`）只承载规划职责：`BuildPlan` 不再把任务级验收条件与验证命令合并给它，
  它也不再要求「启用的必需检查」（`checkStepHasRequiredCheck` 按步骤类型分流）。
- 新增规划专用执行路径 `StepWorkflowService.ExecutePlanning`（真实 `StepExecutor.Execute`，Planner 角色、
  只读工具、真实 Attempt / Session / 角色快照 / 归档），**不校验验证配置、不产生 ValidationRun、不进入自动
  修复循环**；步骤终态只由真实执行结论决定，成功即 `completed`，失败/超时/取消/中断如实写入。
- `TaskWorkflowService.runStep` 按步骤类型分流：`plan` 走 `ExecutePlanning`，`develop` 继续走
  `ExecuteAndValidate`（`requireStepVerificationSpec`、确定性验证、领域修复判定、失败反馈持久化全部保留）。
- 规划阶段成功判定为「Planner 真实执行完成 + 执行记录已写入（`Finalized`）」，没有把 `Verified` 或验证结果
  字段硬编码为 true；规划失败不自动重试，也不改动 `Retry.MaxAttempts`。
- 开发步骤语义不变：任务级必需验收条件与命令、Planner 输出的步骤级验收条件与命令继续合并（任务级优先），
  每轮开发后仍执行真实确定性验证，`notes-exists` 在 Developer 创建 `NOTES.md` 之后执行为退出码 0。

### 8.3 测试断言的同步（只调整与业务语义不一致的断言）

- `tests/integration/remote/workflow/e2e_chain_test.go`：改为分别核对「`plan` 步骤不得携带代码验证命令」
  与「每个 `develop` 步骤必须包含任务级 `notes-exists` 且为 Required」。
- `tests/unit/httpapi/plan_test.go`、`tests/integration/local/workflow/e2e_success_test.go`：同一语义修正。
- `tests/unit/domain/plan_generation_test.go`：补充「规划阶段步骤不得携带任务级验收条件与验证命令」。
- 未新增测试文件，未新增测试函数；`assertStageEvidence`、Attempt / Session / ValidationRun 真实性断言、
  角色工具上限断言、交付分支唯一性断言全部保留。

### 8.4 每轮真实测试结果

| 轮次 | 命令 | 结果 | 说明 |
|---|---|---|---|
| 第 1 轮 | `go test -count=1 -run '^TestRemoteE2EFullChain$' -v ./tests/integration/remote/workflow` | **未执行到业务链路** | 真实依赖前置失败：远端 SSH 目标（本机 WSL Ubuntu 内的 sshd）已停止，`127.0.0.1:22` TCP 层拒绝连接。判定为环境前置条件缺失，不是代码问题；启动该测试虚拟机后重跑 |
| 第 2 轮 | 同上 | **PASS**（257.51s） | 计划→开发→确定性验证→语义审查→交付全部真实完成 |

第 1 轮属于测试环境前置（远端主机未运行），未产生 Task / WorkflowRun 记录；环境恢复后未改动任何 SSH 目标、
凭据、known_hosts、主机密钥校验或模型配置。

### 8.5 最终成功现场（第 2 轮）

- 用例：`TestRemoteE2EFullChain`，退出码 **0**。
- `TEST_ARTIFACT_DIR`：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-tests\workflow-3637537598`
  （`test.log`、`case-3321517040/agentflow.db`、`sessions/`、`validations/`）。
- 执行环境：远程 `127.0.0.1:22`（私钥认证，主机密钥校验未关闭，`known_hosts` 未修改）；基础镜像
  `agentflow-base-base:v0001-a253a6178fe7`，语言镜像 `agentflow-language-node:v0001-f02408055e30`；
  容器内 Pi provider `agentflow-test`、模型 `group/free`。
- 标识：
  - Task `978c33738acf5cb9bc7faef88dee6fab` → `completed` / `succeeded`；
  - WorkflowRun `90c00e4c7c5491b26bd192fbefa48369` → `completed` / `stage=finalize` / `2 of 2` 步骤完成；
  - 计划生成会话 `bee42fc17280060139379debfd0cc981`（`planner`，2 个步骤，未关联 Attempt）；
  - 规划阶段步骤 `a860a5e682d525583e00a2c2cd0f3980`、Attempt `45b5f8e7a6f47aa2a6a182bfb904a36e`
    （`completed`，exit 0）、会话 `4f998a6d7834ff379f60611582a24e8e`（`planner`，工具 `read,grep,find,ls`）；
  - 开发步骤 `87de6b93e019f180db304482c0cf75f3`、Attempt `862e1a2641ff5bf8d54c82bd1e717481`
    （`completed`，`git_after` 记录到真实未提交变更）、会话 `b027d4d836c4af7fb9a171b9b6be28ee`
    （`developer`，工具 `read,grep,find,ls,edit,write,bash`）；
  - ValidationRun `7dc84200da067d213662d9773e31a68b` → `completed` / `verdict=passed`，
    检查 `notes-exists`（Required）`passed`，退出码 **0**，在本次任务容器内执行；
  - 语义审查会话 `d16da4c841cebe5df19a9a9945b067c6`（`validator`，工具 `read,grep,find,ls`），
    `ReviewStatus=passed`，评估证据引用 `/workspace/project/NOTES.md`；
  - 任务容器 `d77d996162ba2ca8f65190df558c25c2`（Docker `3e78fd632d00eba707e7b59869e2fcc99f10fd346bc4f40be507ec2d341be9d4`），
    finalizer 核验容器已清理（`PASS`，无其他容器变动，基线 `pgsql-server` 保持不变）；
  - 会话归档：4 条会话全部 `complete` / `archive=succeeded`。
- 持久化的步骤验证规格（数据库实读）：
  - 序号 1 `plan`：`{}`（无验收条件、无命令）；
  - 序号 2 `develop`：任务级 `notes-exists`（`sh -c "test -f NOTES.md"`，`required=true`）与步骤级验收条件合并结果。
- 交付：`code_saves` 记录 commit `744fd81adb57c27ca7dcbae40802856891220ebd`、`push=succeeded`、
  远程分支 `agent/978c33738acf5cb9bc7faef88dee6fab` 指向同一提交、`merge_status=not_requested`（未合并基础分支，
  未删除或改写历史测试分支，目标仓库仍是专用测试仓库 `lingting/lingting-agent-flow-test`）。
- 非测试验证：改动文件 `gofmt`、`go vet ./...`（含 `./tests/...` 编译）、`git diff --check` 均无问题。

### 8.6 规划阶段与开发阶段的最终执行规则

| 阶段 | 执行路径 | 成功判定 | 禁止 |
|---|---|---|---|
| `plan` | `StepWorkflowService.ExecutePlanning` → 真实 `StepExecutor.Execute`（Planner 角色） | Planner 真实执行完成且执行记录完整写入（`completed` + `Finalized`） | 不跑确定性代码验证、不生成 ValidationRun、不进入自动修复、不自动重试、不伪造 `Verified` |
| `develop` | `StepWorkflowService.ExecuteAndValidate` → 开发 + 确定性验证 + 语义审查 + 领域修复判定 | 必需检查全部通过且语义审查通过，步骤进入 `completed` 且 `Verified=true` | 缺少启用的必需检查时在开发前直接拒绝；验证失败按 `domain.DecideRepairLoop` 处理 |
| `validator` | `ValidatorReviewer`（Validator 角色，只读） | 结构化评估被真实解析并持久化，不覆盖确定性结论 | 无 Bash / 写入 / 编辑能力，不取代 `ValidationService` |

M2 状态保持 **pending**：本轮未开始 M2 的规划容器、只读挂载或 PlanningSession。
