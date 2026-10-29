# P4：创建任务表单的字段收敛（常规功能开发，不属于 P4-M5）

> - **状态**：`completed`（2026-10-28）
> - **基线**：`origin/master` = `5fbf059aa225915fbd75fde281d1b867187b4cb4`（`:zap: 任务创建时的项目配置继承与显式覆盖语义`），
>   本轮开始前 `git fetch origin` 后确认该 SHA 即最新基线，`git status --short` 无输出。
> - **前置决策**：没有足量真实使用数据，**P4-M5 保持 `pending`**；本轮不重复进行缺少数据的性能评估，
>   只按代码证据改进任务创建的实际操作流程。
> - **本文件记录实际核实与实施证据**，不是计划；未执行、未验证的内容一律标注为未执行。

---

## 1. 结论摘要

上一轮（`P4-task-create-project-inheritance.md`）交付了"项目默认值 vs 用户显式设置"的优先级与来源标识，
并把"字段收敛"登记为**未立项候选**，要求先核实真实使用路径。本轮按真实调用链逐字段核实后判定：

- **不存在重复操作**：走通创建流程时，可继承字段已被项目默认值预填，用户不需要额外点击。
- **存在三处可避免的交互负担**（代码可证），因此实施**最小收敛**：只把三个可继承文本字段
  （仓库地址 / 基础分支 / 容器内工作目录）折叠为"默认继承关联项目"的可展开分组；语言环境与版本
  **保持常显**，因为它取决于当前执行节点，属于"必须始终可确认"的字段。
- **未修改任何 Go 代码、HTTP 契约、数据库结构与领域规则**。

| 编号 | 修复前的负担 | 证据 |
|---|---|---|
| D1 | 项目摘要行展示的是**项目记录事实**，用户覆盖后与表单有效值不一致，且与下面的平铺字段重复 | `CreateTaskModal` 在 `关联项目` 下渲染 `仓库 … · 分支 … · 环境 …`（取 `selectedProject` 字段），紧接着又平铺渲染 `repository_url` / `base_branch` / 环境与版本字段 |
| D2 | `work_dir` 是唯一被角色配置隔开的继承字段 | 原顺序为 `… 语言环境与版本 → 角色配置（3 个选择器） → 容器内工作目录 → …` |
| D3 | 3 个 `required` 的继承字段插在两个必填选择器之间 | 原顺序为 `执行节点 → 仓库地址 → 基础分支 → 语言环境与版本 → 角色配置`；720px 弹窗内窄视口必须滚动越过它们才能从"执行节点"走到"角色配置" |

---

## 2. 逐字段核查（代码证据）

继承规则的**唯一权威**是 Go 端 `app.TaskService.prepareTaskRequest`
（`internal/app/task.go`），语义见 `firstNonBlank` 注释："调用方显式给出的值优先，缺省继承项目配置"。

| 字段 | 分类 | 代码证据 |
|---|---|---|
| 关联项目 `project_id` | **必须显式选择** | `resolveTaskProject` 对空 / 书写非法 / 不存在 / 已逻辑删除一律返回参数错误；`config.ProjectID = project.ID` 只来自服务端回读的记录 |
| 执行节点 `host_id` | **必须显式选择** | `domain.ProjectConfig` 没有节点字段；`resolveHost` 要求非空并能查到真实节点 |
| 规划 / 开发 / 验证角色 | **必须显式选择** | `ProjectConfig` 没有角色字段；`prepareTaskRequest` 只从请求取 `PlannerRoleID` / `DeveloperRoleID` / `ValidatorRoleID` 并交由 `ensureRoles` 校验 |
| 仓库地址 `repository_url` | 可安全继承 | `domain.ParseGitRepositoryURL(firstNonBlank(request.RepositoryURL, project.RepositoryURL))` |
| 基础分支 `base_branch` | 可安全继承 | `firstNonBlank` + `domain.NormalizeGitBranch` |
| 语言环境 `environment` | 可安全继承，但**依赖执行节点** | `domain.NewEnvironmentRef(language, firstNonBlank(request.Environment, project.Environment.Name))` |
| 环境版本 `environment_version` | 同上 | `firstNonBlank(request.EnvironmentVersion, project.EnvironmentVersion)`；可用性由 `GET /api/v1/environments?host_id=…` 真实返回 |
| 容器内工作目录 `work_dir` | 可安全继承（可选覆盖） | `firstNonBlank(request.WorkDir, project.EffectiveWorkDir())` |
| 缓存类型 `caches` | 可继承，但表单**没有入口** | `inheritList(request.Caches, project.Caches)`；`toCreateTaskRequest` 从不提交该字段 |
| 优先级 / `merge_after_push` / 验证命令 / 验收条件 | 不属于项目继承范围 | `prepareTaskRequest` 只从请求取值（`priority` 缺省 `normal`，`merge_after_push` 缺省 `false`），项目侧没有对应字段 |

### 2.1 回答立项核查问题

| 问题 | 结论与证据 |
|---|---|
| 用户是否必须在有效项目配置下再逐项操作一组可继承字段？ | **不是**。预填已生效，happy path 不需要任何额外点击；负担在"阅读与滚动"而不是"操作次数" |
| 哪些字段即使默认值有效也需要一直显示？ | **语言环境与版本**：它的可用性由当前执行节点决定（`usableVersions`、`resolveEnvironmentSelection`），节点变化会清空，且它的告警（查询失败 / 没有可用版本 / 项目环境不可用）必须随时可见，不能藏进收起区域 |
| 隐藏或折叠字段后，用户能否发现并修改它们？ | 能。收起时摘要逐行给出**当前生效取值**与来源；展开入口是可聚焦按钮（`aria-expanded` + `aria-controls`），并附"默认继承关联项目，无需重复填写"；必填值为空时摘要直接标"未填写，需要补充" |
| 项目配置缺失 / 不可用 / 查询失败时是否会把无效默认值当有效配置？ | 不会。项目列表失败时摘要只显示"尚未选择关联项目"与"未填写"，不出现任何"来自项目"的字样；项目环境在当前节点不可用时仍由 `projectTaskPrefill` 拒绝预填并在环境字段下方给出原因 |
| 是否存在不改变后端契约的最小改进？ | 存在，即本轮实施范围：折叠 3 个文本字段 + 摘要取自**表单**而非项目记录 + `work_dir` 归位 |

---

## 3. 实施后的交互与继承语义

### 3.1 表单结构（收敛后）

```text
任务名称 / 任务描述
关联项目（必选，项目事实摘要行已移除）  ·  优先级
执行配置
  执行节点（必选）
  语言环境与版本（必选，始终显示，带来源说明与真实原因）
  ▸ 展开修改仓库地址、基础分支与工作目录      ← 默认收起
      收起摘要：仓库地址 / 基础分支 / 容器内工作目录 各一行「当前有效取值 + 来源」
角色配置（规划 / 开发 / 验证）
必需验证命令 / 验收条件 / 推送成功后合并基础分支
```

- 折叠字段**保持挂载**（`display: none`），不使用条件渲染：`Form` 设置了 `preserve={false}`，
  卸载字段会丢弃已继承的取值。
- 展开状态是表单局部状态（`configFieldsExpanded`），打开弹窗时重置；没有全局状态、没有持久化。
- 收起摘要的取值来自 `Form.useWatch`（表单当前值），来源来自既有的 `appliedFields` /
  `overriddenFields`，**不读取项目记录的原始值**。
- 校验错误落在收起字段上时（`validateFields` 的 `errorFields` 命中 `PROJECT_TEXT_FIELDS`）
  先自动展开再返回，避免"提示存在但看不见"。

### 3.2 保留的既有语义（未改动）

- 用户显式设置优先；空字符串仍按 Go 端 `firstNonBlank` 视为"未覆盖"，因此清空输入框不会变成
  "手动设置"，摘要会显示"未填写，需要补充"而不是"继承值"。
- 写入环境时必定同时写入版本；环境被改写时旧版本清空；项目锁定的版本不属于当前环境时只填环境；
  项目环境在当前节点不可用时不预填环境。
- 执行节点变化清空环境与版本并丢弃这两个字段的"用户已选"标记。
- 提交请求仍由 `toCreateTaskRequest` 构造；`project_id` 来自 `resolveProjectSelection` 返回的
  真实记录；`work_dir` 为空时省略，`caches` 从不提交。
- 服务端仍是最终配置继承的权威实现。

---

## 4. 修改文件

| 文件 | 变化 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/projectExecutionSummary.ts`（新增） | 纯函数 `projectExecutionSummary`：按 `PROJECT_TEXT_FIELDS` 顺序生成收起摘要的取值 / 缺失标记 / 来源；`UNSET_FIELD_LABEL`、`DEFAULT_WORK_DIR_LABEL`、`NO_PROJECT_HINT` |
| `lingting-agent-flow-ui/src/pages/tasks/ProjectDefaultsSection.tsx`（新增） | 只负责展示：展开 / 收起按钮（`aria-expanded` + `aria-controls`）、摘要（`Descriptions`）、未选择项目时的提示 |
| `lingting-agent-flow-ui/src/pages/tasks/CreateTaskModal.tsx` | 移除与平铺字段重复的项目事实摘要行；把仓库地址 / 基础分支 / 容器内工作目录移入折叠分组（工作目录从角色配置之后归位）；新增展开状态、三条 `useWatch`、摘要 `useMemo`、校验失败自动展开、打开时重置展开状态 |
| `lingting-agent-flow-ui/tests/unit/task-project-execution-summary.test.ts`（新增） | 6 个纯函数用例：未选择项目、继承默认值、用户覆盖、必填清空、工作目录清空、行顺序 |
| `lingting-agent-flow-ui/tests/e2e/task-create-project-inheritance.spec.ts` | 改写首个用例为"收起摘要 + 键盘展开"；新增覆盖后摘要、校验失败自动展开、项目查询失败、环境查询失败 4 个用例；既有用例改为先展开再操作被折叠字段 |
| `lingting-agent-flow-ui/tests/e2e/{task-create-selectors,task-launch,tasks}.spec.ts` | 各新增 `expandProjectDefaults` 辅助函数（按钮可见时才点击），在填写仓库地址 / 基础分支前展开分组 |

**未修改**：所有 Go 代码（`internal/**`、`cmd/**`、`tests/**` 的 Go 部分）、`CreateTaskRequest`、HTTP 契约、
数据库结构与迁移、`domain` / `app` 的任何规则、`projectPrefill.ts` 的预填与来源模型、
`EnvironmentSelector` / `useEnvironments` / `useProjects` 的数据来源与四态处理。

---

## 5. 测试与构建（实际执行）

| 命令 | 实际结果 |
|---|---|
| `pnpm test:unit:safe` | **603 passed / 28 个测试文件**（基线 597 + 新增 6） |
| `pnpm test:e2e:safe` | **229 passed / 0 failed**（基线 225 − 改写 1 + 新增 5） |
| `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | 通过 |
| `pnpm build` | 通过，产物写入 `internal/webui/dist`（仅既有分块体积提示） |
| Go 测试 | **未执行**：本轮未修改任何 Go 文件与后端契约（改动全部在 `lingting-agent-flow-ui/`） |

测试产物目录：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<随机后缀>`
（按项目规范保留，未清理）。

浏览器人工走查未执行：用户未提供地址，也未要求访问页面，按项目规范不主动使用 `chrome-devtools`。

---

## 6. 剩余风险

| 项 | 说明 |
|---|---|
| R1 交互变化 | 手工填写仓库地址 / 基础分支 / 工作目录前必须先点开分组，历史用户会感觉"字段消失"。摘要与提示文案负责告知，且没有项目时摘要会明确要求手工填写 |
| R2 折叠依赖 `display: none` | 用 CSS 隐藏而不是卸载字段，是为了保住 `preserve={false}` 下已继承的取值；若将来把该 `Form` 的 `preserve` 改成 `true`，可以改用条件渲染，但不能只改一处 |
| R3 摘要不等于服务端结果 | 摘要只表达表单当前值与来源；未填写的可选字段（工作目录）仍由服务端按 `firstNonBlank` 继承 |
| R4 未做可视走查 | 结论来自单元测试与拦截接口的 E2E；窄视口布局只通过既有响应式布局与既有 E2E 覆盖，没有专门的视口尺寸用例 |
| R5 未收敛的字段 | 语言环境与版本、关联项目、执行节点、角色仍常显，这是本轮**有意**保留：它们决定有效执行目标或依赖当前节点 |
| R6 与 P4-M5 的关系 | 无关。本轮不涉及性能、并发、存储或架构，也没有产生可用于性能立项的数据 |

---

## 7. 下一项候选（仅列有代码证据的方向）

1. **任务详情页面板分组**（`P4-task-detail-experience-review.md` §8）：任务详情已累积 9 个面板，
   需要按实际页面结构核实"同一事实多处重复"与"关键动作不可达"，成本最低。
2. ~~**`verification_results` 遗留模型清理**~~：**已于 2026-10-29 完成**（生产链路无写入方，
   唯一读取方也不被 CLI 展示；领域类型、存储接口与实现一并删除，遗留表与历史数据保留）。
3. ~~**私有仓库 Git 凭据支持**~~：**已由用户明确决定不纳入产品范围（2026-10-29）**，不再是候选
   （见 `AGENTS.md` 的"私有仓库认证的产品边界"）。

`P4-M5` 仍为 `pending`：解除条件（`tasks.status` 分布、队列长度、排队等待分位数，或"列表卡顿"
"任务长期停在 `queued`"的实际报告）本轮同样没有获得，且本次改动不涉及性能、并发、存储或架构。
