# P4 —— 产品操作体验与针对性优化

> 撰写依据：commit `330e7c4`（2026-10-23）工作区的实际代码与测试状态。
> **P4-M1 已按 `master` HEAD `70f82b1`（2026-10-26）的实际代码重新核实并改写**，
> 缺口清单与代码证据见 [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md)。
> M2–M5 的工作项直接来自该清单的 G1–G21，不再使用"待补充"的骨架描述。
> **P4-M2-1（角色初始化 + `GET /api/v1/roles`）已于 2026-10-26 实施完成**，契约、代码证据、
> 测试结果与剩余风险见
> [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md)。
> **P4-M2-2（环境查询 HTTP API）已于 2026-10-26 实施完成**，契约、代码证据、测试结果与
> 剩余风险见
> [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)。
> **P4-M2-3（Web 接入统一任务启动 + 环境选择）已于 2026-10-26 实施完成**，调用链、
> 环境选择行为、测试结果与剩余风险见
> [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md)。
> **P4-M2-4（任务创建表单接入执行节点与角色选择器）已于 2026-10-26 实施完成**，接口
> 契约、选择与校验规则、四态展示、测试结果与剩余风险见
> [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md)。
> **P4-M2-5（验证失败反馈查询 API）已于 2026-10-26 实施完成**，路由契约、完整归属校验、
> 错误映射、最近反馈入口的范围决策、测试结果与剩余风险见
> [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md)。
> **P4-M2-6（代码保存记录列表 API）已于 2026-10-26 实施完成**，路由与 DTO、三阶段与记录
> 可信度语义、应用层依赖边界、只读边界、测试结果与剩余风险见
> [`P4-M2-6-code-save-records-api.md`](./P4-M2-6-code-save-records-api.md)。
> **P4-M2-7（Web 接入计划生成、步骤执行与验证修复操作）已于 2026-10-26 实施完成**，操作
> 矩阵与代码证据、实施顺序、前端 API 与操作界面结构、测试结果、未通过项与剩余风险见
> [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md)。
> **P4-M3-3（任务最终结论与收尾闭环展示）已于 2026-10-26 实施完成**，真实契约核实、
> M3-1 / M3-2 核验结论、改动文件、测试结果、未执行项与剩余风险见
> [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md)。

## 阶段目标

在主业务流程可用后，让用户能够清楚地操作、观察和管理任务，再根据实际瓶颈推进优化。
阶段结束时，用户实际能完成：

- 通过 API 与 Web 页面完成完整任务生命周期的必要操作。
- 看到阶段进度、验证结果、失败原因与恢复状态，不靠猜测。
- 支持必要的继续执行、取消、恢复和结果查看操作。
- 基于真实使用问题或可测量数据，推进性能、并发、存储与架构优化。

## 必要性

P1–P3 保证"能跑"，但用户无法清楚地操作与观察时，主流程的价值无法体现。这一阶段把
操作入口与可观测性补齐，并用真实数据驱动优化，而不是凭推测提前优化。

**优先级约束**：与完整流程无关的 UI 美化、通用化抽象和推测性性能优化一律排在后面，
不与 P1–P3 抢跑。

## 前置条件

- P3 退出（多任务可被管理、调度与可靠运行）。

## 当前状态（摘要，基于代码证据）

**已完成**：

- 机器页完整接通执行节点接口与环境预检结果展示（`lingting-agent-flow-ui/src/pages/machines/`）。
- 任务页已接通任务 CRUD、统一启动/取消/恢复与工作流进度面板
  （`lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx`）；新任务的启动入口已是
  `POST /tasks/{id}/start`（P4-M2-3），`execute-workflow` 不再用于新任务启动。
- 任务创建弹窗的语言环境与版本来自 `GET /api/v1/environments?host_id=…`，只有服务端
  标记 `usable=true` 的版本可选（P4-M2-3）。
- 任务创建弹窗的执行节点来自 `fetchHosts()`，规划 / 开发 / 验证角色来自新增的
  `fetchRoles()`，不再手填任何资源 ID（P4-M2-4）：节点只允许已启用且
  `usable_for_remote_task` 的记录，角色按 `type` 分类并排除 `enabled=false`；
  节点切换清空旧环境与版本并重新查询；加载中 / 失败 / 空列表 / 某类无启用角色分别呈现，
  失败不伪装为空列表且带重试入口。
- 后端已具备统一任务启动、计划生成、单步骤执行、顺序执行、开发—验证闭环、修复周期、
  确定性验证、任务级工作流编排、显式重试、续跑资格、代码保存核验、执行记录查询的
  服务与 HTTP 入口（`internal/httpapi/`，完整清单见 P4-M1 §4.1）。

**缺口（按优先级，详见 P4-M1 §5 主矩阵）**：

- **P0 端到端阻塞**：
  - G1 统一启动 `POST /tasks/{id}/start` 未接入 Web；任务页直接调 `execute-workflow`，
    对 `queued` 新任务必然 409（`domain.CheckWorkflowStart` 要求已有容器与已持久化步骤）。
    **M2-3 已闭合**：任务页"启动"改走统一启动，`execute-workflow` 已从新任务启动流程移除；
    上述描述保留为 P4-M1 盘点时的现场记录。
  - G2 角色初始化与角色查询 —— **M2-1 已闭合后端部分**：`agentflow server` 启动时在数据库
    打开后按 `app.RoleSeeds` + `storage.RoleSeeder.SeedRoles` 导入初始角色（只在空表时），
    新增只读 `GET /api/v1/roles`。**M2-4 已闭合前端部分**：创建任务弹窗的角色选择器来自
    该接口，按 `type` 分类并排除 `enabled=false`。仍缺：角色变更入口（CRUD 或显式同步）。
    详见
    [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md) 与
    [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md)。
  - G3 环境（语言环境 / 版本 / 镜像）—— **M2-2 已闭合后端部分**：新增只读
    `GET /api/v1/environments?host_id=…` 与
    `GET /api/v1/environments/{type}/{name}/versions/{number}?host_id=…`，
    返回真实构建状态、`image_ref`、服务端计算的可用性与版本元数据；纯读取不再要求
    SSH / Docker 依赖。剩余部分是前端接入（M2-3）与环境构建 Web 入口（未排期）。
- **P1 业务决策阻塞**：G5 重试无入口、G9 容器处置无 HTTP、G11 续跑阻塞原因不可见。
  **G7 步骤 / Attempt / Session 历史视图已由 M2-7 交付，M3-3 核实为部分满足**：步骤、
  尝试与会话（执行 / 归档 / 导出三态分列）均已展示，仍缺尝试的
  `timed_out` / `cancelled` / `error_class` / `failure_stage` 与会话归档 / 导出状态的中文
  标签。**G10 收尾结论展示已于 M3-3 闭合**（`types.ts` 补齐 `final_result` / `closeout` /
  `closeout_summary`，任务详情把任务状态、最终业务结论与收尾闭环拆成三行独立事实，缺省与
  未知取值不误报成功）。**G6 验证失败反馈查询的 API 部分已由 M2-5 闭合**
  （新增只读 `GET …/validations/{runID}/feedback`，应用层校验完整归属链），**其前端验证历史
  视图已由 M2-7 交付，M3-3 核实为满足**。**G8 代码保存列表的 API 部分已由 M2-6 闭合**
  （新增只读 `GET /tasks/{taskID}/code-saves`，让 Web 取得真实 `recordID`；提交 / 推送 /
  合并三阶段与 `persist_status` / `recovery_status` 分别输出；查询只依赖存储，不复用强制
  要求 SSH 的 `ContainerService`。前端三阶段展示仍归 M3-5，核验操作入口归 M4-2）。
- **P2 管理/诊断能力**：G12 项目后端能力不存在（无表、无服务）、G13 计划生成、G14 单步
  诊断入口、G15 事件时间线、G16 修复周期历史、G18 工作流步骤起止时间恒为空。
- **P3 体验优化**：G17 调度名额、G19 进度自动刷新、G20 单步诊断动作。

**历史文档需要纠正的地方**（详见 P4-M1 §3）：README 仍称"多任务并发调度未实现"
"任务调度器未接入""项目/环境/容器/代码保存均无 HTTP 接口""任务页尚未接入步骤执行入口"，
与当前代码不符。

- 性能/并发/存储优化缺少真实使用数据，属于待测量事项；本次核对未发现其阻塞主流程。

## 阶段范围

**做**：

- 盘点当前 API 与 Web 业务操作能力的缺口（M1）。
- 提供完整任务生命周期的必要操作入口（M2）。
- 展示阶段进度、验证结果、失败原因和恢复状态（M3）。
- 支持必要的继续执行、取消、恢复和结果查看操作（M4）。
- 根据真实使用问题或可测量数据，推进性能、并发、存储与架构优化（M5）。

**不做**：

- 不把与完整流程无关的 UI 美化排在主流程操作入口之前。
- 不提前做无数据支撑的性能/并发/存储优化。
- 不新建第二套工作流逻辑；前端只展示与发起操作，业务规则仍在 `internal/app`。

## 里程碑清单与依赖

| ID | 名称 | 状态 | 依赖 |
|---|---|---|---|
| P4-M1 | 盘点当前 API 与 Web 业务操作能力的缺口 | `completed`（2026-10-26，基于 `master` HEAD `70f82b1`） | P3 退出 |
| P4-M2 | 提供完整任务生命周期的必要操作入口 | `completed`（M2-1 至 M2-7 全部完成，2026-10-26） | P4-M1 |
| P4-M3 | 展示阶段进度、验证结果、失败原因和恢复状态 | `in_progress`（M3-2 经核实已由 M2-7 满足；M3-3、M3-4、M3-5 于 2026-10-26 完成；M3-1 部分满足，M3-6 至 M3-8 待做） | P4-M1、P4-M2（部分工作项需 M2 先补 API） |
| P4-M4 | 支持必要的继续执行、取消、恢复和结果查看操作 | `pending` | P4-M2、P4-M3 |
| P4-M5 | 根据真实使用问题或可测量数据推进性能、并发、存储与架构优化 | `pending` | P4-M2、P4-M3、P4-M4 |

## 阶段退出条件

1. 完整任务生命周期的必要操作都能通过 API 与 Web 完成。
2. 阶段进度、验证结果、失败原因与恢复状态可被用户清楚看到，且数据来自真实接口。
3. 继续执行、取消、恢复与结果查看操作可用，且不伪造状态。
4. 优化项有真实使用问题或可测量数据支撑，不凭推测。

## 风险与边界

- **误导用户**：用虚构数据或占位 UI 伪装真实业务状态，会让用户误以为能力已可用。
- **重复实现**：前端若自行推断工作流状态或重写业务规则，会与 `internal/app` 状态机冲突。
- **无效优化**：没有测量数据的性能/存储优化可能引入回归，收益不可验证。

## 下一步选择规则

M1 已完成，M2-1 已完成（角色初始化 + `GET /api/v1/roles`），M2-2 已完成（环境查询
HTTP API），M2-3 已完成（Web 接入统一启动 + 环境选择），M2-4 已完成（创建任务弹窗接入
执行节点与角色选择器），M2-5 已完成（验证失败反馈查询 API，覆盖 G6 的 API 部分），
M2-6 已完成（代码保存记录列表 API，覆盖 G8 的 API 部分），M2-7 已完成（Web 接入计划生成、
单步骤执行、顺序执行、开发—验证闭环、修复周期、确定性验证与代码初始化，覆盖 G14、G13，
并顺带闭合 G20）。**P4-M2 至此全部完成**，下一步是 P4-M3（展示阶段进度、验证结果、失败
原因和恢复状态）。
G1、G2、G3、G4 的端到端断链均已解除：全新数据库启动后角色会被真实写入，环境与版本可从
HTTP 读取，新任务可从 Web 显式启动并真实推进工作流，创建任务不再需要手抄任何资源 ID。

**P4-M3 已进入实施**（2026-10-26 按当前代码核实）：M3-3 已完成（任务最终结论与收尾闭环
展示，覆盖 G10，交付物
[`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md)）；M3-5 已完成
（代码保存三阶段状态展示，覆盖 G8 的前端部分，交付物
[`P4-M3-5-code-save-status-display.md`](./P4-M3-5-code-save-status-display.md)）；M3-2 经核实
已由 P4-M2-7 满足（验证历史、逐项检查与失败反馈视图齐备）；M3-1 部分满足，剩余字段级补全
（尝试的 `timed_out` / `cancelled` / `error_class` / `failure_stage` 与会话归档 / 导出状态的
中文标签）。M3-4 已完成（续跑资格与阻塞原因展示，覆盖 G11，交付物
[`P4-M3-4-resume-eligibility-display.md`](./P4-M3-4-resume-eligibility-display.md)）。
M3-6、M3-7、M3-8 尚未开始，其中 M3-6、M3-8 需要先补后端能力。
建议顺序：M3-1 的剩余字段 → M3-7 → M3-6 / M3-8。

M2 之后依次为 M3（可观测性）、M4（继续/取消/恢复/核验）、M5（基于实测数据的优化）。

---

## 里程碑详情

> 工作项编号 G1–G21 与验收标准定义在
> [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md) §5、§6，此处只列
> 交付顺序与依赖，不重复验收细节。

### P4-M1 盘点当前 API 与 Web 业务操作能力的缺口

- **ID**：`P4-M1`
- **状态**：`completed`（2026-10-26，基于 `master` HEAD `70f82b1`）
- **必要性**：先建立缺口清单，避免操作入口与可观测性工作碎片化。
- **交付物**：[`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md)
- **完成证据**：完整 HTTP 路由清单（§4.1，34 条路由逐条核实）、后端有能力但无 HTTP
  入口的能力清单（§4.2）、前端封装与页面接通情况（§4.3、§4.4）、21 项缺口主矩阵
  （§5）、逐项验收标准（§6）、优先级依据（§8）、文档与代码冲突清单（§3）。
- **关键发现**：G2（角色初始化生产零调用者）与 G1（统一启动未接入 Web）、G3（环境无
  HTTP 入口）共同构成两条端到端断链，历史文档均未记录。
- **本次未执行测试**：结论全部来自静态代码核实，详见 P4-M1 §9。

### P4-M2 提供完整任务生命周期的必要操作入口

- **ID**：`P4-M2`
- **状态**：`completed`（M2-1 于 2026-10-26 完成，交付物
  [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md)；
  M2-2 于 2026-10-26 完成，交付物
  [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)；M2-3 于
  2026-10-26 完成，交付物
  [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md)；M2-4 于 2026-10-26 完成，
  交付物
  [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md)；M2-5 于
  2026-10-26 完成，交付物
  [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md)；M2-6 于
  2026-10-26 完成，交付物
  [`P4-M2-6-code-save-records-api.md`](./P4-M2-6-code-save-records-api.md)；M2-7 于
  2026-10-26 完成，交付物
  [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md)）
- **必要性**：G2/G1/G3 是端到端阻塞，不解除则用户无法从 Web 开始任何任务。
- **前置依赖**：P4-M1。
- **工作项（按交付顺序）**：
  1. **M2-1** 角色初始化接入 `agentflow server` 启动流程（只在 `roles` 为空时导入）+
     `GET /api/v1/roles`。覆盖 G2 的后端部分。**已完成**（2026-10-26，见
     [`P4-M2-1-role-initialization-api.md`](./P4-M2-1-role-initialization-api.md)）。
     前端选择器已随 M2-4 完成；仍缺：角色变更入口（CRUD 或显式同步）。
  2. **M2-2** 环境查询 HTTP 接口（列表 + 版本 + 构建状态）。覆盖 G3。**已完成**
     （2026-10-26，见
     [`P4-M2-2-environment-query-api.md`](./P4-M2-2-environment-query-api.md)）。
     前端接入已随 M2-3 完成（见 [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md)）；
    仍缺：环境构建 Web 入口（未排期）。
  3. **M2-3** 前端接入统一启动 `POST /tasks/{id}/start`，展示真实 `stage` /
     `workflow_started`。覆盖 G1。**已完成**（2026-10-26，见
     [`P4-M2-3-web-task-launch.md`](./P4-M2-3-web-task-launch.md)）。同时把任务创建
     弹窗的环境与版本改为服务端数据驱动的选择器。仍缺：环境构建 Web 入口。
  4. **M2-4** 创建任务弹窗复用 `fetchHosts` 与角色接口，移除手填文案。覆盖 G4、G2。
     **已完成**（2026-10-26，见
     [`P4-M2-4-task-creation-selectors.md`](./P4-M2-4-task-creation-selectors.md)）。
     仍缺：环境构建 Web 入口、角色变更入口。
  5. **M2-5** 验证失败反馈查询 API（存储层已具备）。覆盖 G6 的 API 部分。**已完成**
     （2026-10-26，见
     [`P4-M2-5-validation-feedback-api.md`](./P4-M2-5-validation-feedback-api.md)）。
     新增只读 `GET /tasks/{taskID}/steps/{stepID}/validations/{runID}/feedback`，应用层
     校验任务 → 步骤 → 验证记录 → 反馈的完整归属链；步骤级"最近一次反馈"入口经核实由
     验证历史列表覆盖，本轮不新增并记录依据。仍缺：前端验证历史视图（归 P4-M3 M3-2）。
  6. **M2-6** 代码保存记录列表 API（让 `recordID` 可从 Web 获取）。覆盖 G8 的 API 部分。
     **已完成**（2026-10-26，见
     [`P4-M2-6-code-save-records-api.md`](./P4-M2-6-code-save-records-api.md)）。
     新增只读 `GET /tasks/{taskID}/code-saves`，逐字段映射 `domain.CodeSaveRecord`，
     提交 / 推送 / 合并三阶段与 `persist_status` / `recovery_status` 分别输出；查询由只依赖
     存储的 `app.CodeSaveQueryService` 承担，不复用强制要求 SSH 的 `ContainerService`。
     仍缺：前端三阶段展示（归 P4-M3 M3-5）、核验操作入口（归 P4-M4 M4-2）。
  7. **M2-7** 单步骤 / 顺序执行 / 闭环 / 修复周期 / 计划生成入口。覆盖 G14、G13。
     **已完成**（2026-10-26，见
     [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md)）。
     先按真实路由核实 8 项操作的请求 DTO、前置条件、成功语义与失败映射，再按
     "任务详情 → 计划步骤 → 步骤详情 → 尝试与验证结果 → 明确后续动作"组织界面；
     顺带接入 G20 的两项单步诊断动作。**未修改任何后端代码**。仍缺：计划版本化
     （服务端拒绝覆盖已有计划）、同步长请求的进度推送（归 M3-7）。
- **完成定义**：全新部署 → Web 创建任务 → 启动 → 工作流真实推进，全程无需 CLI；
  所有业务规则仍在服务端，前端不拼接阶段。
- **完成证据**：7 个子任务全部完成，均有独立交付文档（见上）；执行现场的全部操作入口
  已接入，所有动作只使用当前真实记录的标识、失败原样展示服务端原因、从不自动重试。
  缺口 G13、G14 与 G20 已闭合（详见 `ROADMAP.md` 的缺口清单）。新增前端单元测试 51 个
  （2 个文件）与 E2E 20 个；全量前端单元测试 191 通过，全量 E2E 70 通过 / 3 既有失败
  （经基线对照确认）。**未修改后端代码，未运行 Go 测试**，也未做真实 SSH / Docker / Pi
  环境验证——这两项在
  [`P4-M2-7-web-execution-operations.md`](./P4-M2-7-web-execution-operations.md) §5 记录为
  未执行项。
- **仍缺（归后续里程碑）**：计划版本化（服务端拒绝覆盖已有计划）、同步长请求的进度推送
  （M3-7）、闭环未通过验证时的结构化事实（需后端调整契约）、环境构建与角色变更的 Web
  入口（未排期）。

### P4-M3 展示阶段进度、验证结果、失败原因和恢复状态

- **ID**：`P4-M3`
- **状态**：`in_progress`（2026-10-26 核实的子项状态：M3-2 已由 P4-M2-7 满足；M3-3、M3-4、M3-5
  完成；M3-1 部分满足；M3-6、M3-7、M3-8 未开始）
- **必要性**：用户看不到真实进度与失败原因，就无法判断任务为何卡住。
- **前置依赖**：P4-M1；M2-5、M2-6（需先有对应查询 API）。
- **工作项**：
  1. **M3-1** 步骤 / Attempt / Session 历史视图。覆盖 G7。**部分满足**（由 P4-M2-7 交付）：
     步骤列表、尝试列表、会话列表（含执行 / 归档 / 导出三个独立状态）与四态展示均已实现，
     仍缺字段级补全——尝试的 `timed_out` / `cancelled` / `error_class` / `failure_stage`
     未渲染，会话的 `archive_status` / `export_status` 未字典化为中文标签。
     证据见 [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md) §1.1。
  2. **M3-2** 验证历史与逐项检查结果视图。覆盖 G6。**已满足**（由 P4-M2-7 交付）：
     验证历史、流程状态与结论分列、逐项检查、语义审查摘要与失败反馈（含 `404` 语义）
     均已实现，无需重复开发。证据见同一文档 §1.2。
  3. **M3-3** 任务最终结论与收尾闭环展示（含 `types.ts` 补 `final_result` /
     `closeout` / `closeout_summary`）。覆盖 G10。**已完成**（2026-10-26，交付物
     [`P4-M3-3-task-closeout-display.md`](./P4-M3-3-task-closeout-display.md)）：
     前端 `Task` 与真实 `httpapi.TaskResponse` 契约一致（新增 `TaskResult` /
     `TaskCloseoutState` 并保留未知取值分支）；任务详情把任务状态、最终业务结论与收尾闭环
     拆成三行独立事实并按时给出警示；`completed` / `succeeded` / `confirmed` 三者未被混同，
     `pending` / `unconfirmed` / `inconsistent` / 缺失值与未知值均有明确且不误报成功的文案。
     **本轮未修改后端代码**；新增前端单元测试 27 个（`tests/unit/task-closeout.test.ts`）
     与 E2E 10 个（`tests/e2e/task-closeout.spec.ts`），全量单元测试 218 通过，全量 E2E
     80 通过 / 3 既有失败（经 `git stash -u` 基线对照确认）。
  4. **M3-4** 续跑资格与阻塞原因展示。覆盖 G11。**已完成**（2026-10-26，交付物
     [`P4-M3-4-resume-eligibility-display.md`](./P4-M3-4-resume-eligibility-display.md)）：
     前端补齐 `ResumeEligibilityResponse` / `ResumeEligibilityCodeSave` 类型与
     `fetchTaskResumeEligibility`（`GET /api/v1/tasks/{taskID}/resume-eligibility`），任务详情
     新增只读面板 `ResumeEligibilityPanel`；`can_resume` 只采用服务端结论，`blocking` 逐项展示
     并为 9 个已核实分类给出中文解释、未知分类原样保留，`code_save_action` /
     `code_save_summary` 与 `code_save_records` 的五个记录维度分别展示，`next_actions` 只展示
     原文并标注 CLI 建议需人工执行；加载状态独立，查询失败不显示为允许续跑，刷新失败保留
     上一次真实快照，切换任务丢弃迟到响应，恢复按钮仍由既有 `canResumeWorkflow` 判定。
     **本轮未修改后端代码**；新增前端单元测试 34 个（`tests/unit/task-resume-eligibility.test.ts`）
     与 E2E 16 个（`tests/e2e/task-resume-eligibility.spec.ts`），全量单元测试 282 通过，全量
     E2E 110 通过 / 3 既有失败（经 `git stash -u` 基线对照确认）。
  5. **M3-5** 代码保存三阶段状态展示。覆盖 G8。**已完成**（2026-10-26，交付物
     [`P4-M3-5-code-save-status-display.md`](./P4-M3-5-code-save-status-display.md)）：
     前端补齐 `CodeSaveRecord` / `CodeSaveRecordListResponse` 与 `fetchTaskCodeSaves`
     （`GET /api/v1/tasks/{taskID}/code-saves`），任务详情新增只读面板
     `CodeSavePanel`，提交 / 推送 / 合并 / 记录写入状态 / 核验结论五组事实分别展示，
     `unknown` / `pending` / `not_requested` / `skipped` / `unconfirmed` / `unverifiable`
     与未知取值均不误报成功；核验结论与 `stage` 一起呈现，空列表只说明没有记录。
     **本轮未修改后端代码**；新增前端单元测试 30 个（`tests/unit/task-code-saves.test.ts`）
     与 E2E 14 个（`tests/e2e/task-code-saves.spec.ts`），全量单元测试 248 通过，全量 E2E
     94 通过 / 3 既有失败（经 `git stash -u` 基线对照确认）。
  6. **M3-6** 工作流步骤起止时间补全（服务端 `workflowStatusResponse` 未赋值）。覆盖 G18。
  7. **M3-7** 进度自动刷新。覆盖 G19。
  8. **M3-8** 事件时间线与修复周期历史（需先补 API）。覆盖 G15、G16。
- **完成定义**：任一失败或阻塞的任务，用户能从 Web 独立判断"卡在哪、为什么、下一步
  做什么"，且所有展示字段都能追溯到具体接口；不把工作流 `completed`、步骤成功或
  Agent 调用成功显示为任务已通过验收。

### P4-M4 支持必要的继续执行、取消、恢复和结果查看操作

- **ID**：`P4-M4`
- **状态**：`pending`
- **必要性**：这些是任务管理闭环的必要操作。
- **前置依赖**：P4-M2、P4-M3。
- **工作项**：
  1. **M4-1** 重试入口，可用性来自 `GET /tasks/{id}/retry-eligibility`（禁止前端推断，
     禁止硬编码状态白名单）。覆盖 G5。
  2. **M4-2** 代码保存核验操作入口。覆盖 G8。
  3. **M4-3** 容器记录查看与显式 `delete` / `detach`（需先补 HTTP 接口）。覆盖 G9。
  4. **M4-4** 继续执行 / 恢复工作流的可用性来自 `resume-eligibility`。覆盖 G11。
  5. **M4-5** 单步诊断动作（初始化代码、确定性验证）。覆盖 G20。
- **完成定义**：`failed` / `blocked` / `recovery_required` 三类现场都能在 Web 上被理解
  并处置；所有有副作用的操作都需要用户显式触发，不自动重放提交、推送、合并或其他
  不确定操作。

### P4-M5 根据真实使用问题或可测量数据推进性能、并发、存储与架构优化

- **ID**：`P4-M5`
- **状态**：`pending`
- **必要性**：优化必须由真实数据驱动。
- **前置依赖**：P4-M2、P4-M3、P4-M4。
- **当前证据**：P4-M1 核对**未发现**性能、存储或架构问题正在阻塞主流程。唯一与规模相关
  的现象是并发上限为"同一份数据库上的名额上限"（P3-M5 §3 已明确为非目标），当前部署
  形态下不构成阻塞。因此本阶段继续后置。
- **候选工作项（需实测数据支撑后才立项）**：G17 调度名额可见性（`TaskScheduler.Status`
  已有能力但无接口）、任务列表在真实数据量下的分页性能。
- **完成定义**：每项优化有可测量的收益与回归证据；不得凭推测提前优化。
