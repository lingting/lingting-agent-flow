# P4：任务创建时的项目配置继承与显式覆盖语义（常规功能开发，不属于 P4-M5）

> - **状态**：`completed`（2026-10-28）
> - **基线**：`origin/master` = `c625dfbe216d347607fe10fadaf9e0ba0cf7678f`（`:bug: 符合并修复多个bug`），
>   本轮开始前 `git status --short` 无输出；`git fetch origin` 后确认该 SHA 即最新基线。
> - **前置决策**：没有足量真实使用数据，**P4-M5 保持 `pending`**；本轮不推进也不重复进行缺少数据的
>   性能评估，只修复"任务创建时项目配置继承与用户显式覆盖"这一有代码证据的交互缺陷。
> - **本文件记录实际核实与实施证据**，不是计划；未执行、未验证的内容一律标注为未执行。

---

## 1. 结论摘要

G12 交付的项目继承功能**能预填**，但预填与"用户显式设置"之间**没有优先级**：项目默认值每次
都会覆盖表单当前值。按当前代码核实出 3 个缺陷并全部按最小改动修复：

| 编号 | 缺陷 | 证据（修复前） |
|---|---|---|
| D1 | 切换项目会**静默覆盖**用户手动修改过的仓库地址 / 基础分支 / 工作目录 | `handleValuesChange` 对 `project_id` 变化无条件 `form.setFieldsValue(projectTaskPrefill(...))`，预填对象总是包含这三个字段 |
| D2 | 切换项目把环境换成新项目的环境时，**旧环境的版本仍留在表单里** | 预填对象只在"项目版本恰好可用"时带 `environment_version`；不带该键时 `setFieldsValue` 不会清空旧值 |
| D3 | 用户**无法判断**字段取自项目默认值还是自己设置 | 表单没有任何来源标识；预填值与用户输入在界面上完全等价 |

其中 D2 直接违反"环境与版本必须保持有效关联"：修复前在一次 E2E 中可以看到表单同时显示
`语言环境 node` 与 `环境版本 v0001`（v0001 属于 go）。该状态不会被提交（`resolveEnvironmentSelection`
会拒绝），但界面已经在表达一个不存在的配置。

另有 4 处**已核实但判定合理、未修改**的行为见 §6。

---

## 2. 核实方法与基线

1. 重新 `git fetch origin`，确认基线 SHA（见文首）与工作区干净；
2. 完整阅读 `.steps/README.md`、`ROADMAP.md`、`P4-product-experience-optimization.md`、
   `P4-G12-project-management.md`、`P4-task-detail-experience-review.md`；
3. 逐行核实前端调用链：`CreateTaskModal.tsx`、`projectPrefill.ts`、`environments.ts`、
   `EnvironmentSelector.tsx`、`useEnvironments.ts`、`useProjects.ts`、`projects/projects.ts`；
4. 核实 Go 端权威规则：`internal/app/task.go` 的 `prepareTaskRequest` / `firstNonBlank` /
   `inheritList`，以及 `tests/unit/app/task_management_test.go` 的
   `TestCreateTaskInheritsProjectConfig` / `TestCreateTaskOverridesProjectConfig`；
5. **先写用例复现缺陷再实现**：新增 `tests/e2e/task-create-project-inheritance.spec.ts`，在未修改
   生产代码时执行，4 个用例失败并给出确切现象（见 §3），修复后 7 个用例全部通过。

### 2.1 后端的继承规则（未修改，仅核实）

```text
prepareTaskRequest(request)
  → resolveTaskProject(request.ProjectID)            // 空/非法/不存在/已删除 → 参数错误
  → repository_url      = firstNonBlank(request, project.RepositoryURL)
  → base_branch         = firstNonBlank(request, project.BaseBranch) → NormalizeGitBranch
  → environment         = firstNonBlank(request, project.Environment.Name)
  → environment_version = firstNonBlank(request, project.EnvironmentVersion)
  → caches              = inheritList(request, project.Caches)
  → work_dir            = firstNonBlank(request, project.EffectiveWorkDir())
  → config.ProjectID    = project.ID                 // 服务端回读的规范标识
```

结论：**空字符串 = 未覆盖**，非空值优先。这正是前端"空值不算显式覆盖"规则的依据（§4.1）。

---

## 3. 缺陷的复现证据（修复前）

`npx playwright test tests/e2e/task-create-project-inheritance.spec.ts --workers=1` 在未修改生产
代码时的结果：**4 failed / 1 passed**。

| 用例 | 关键失败信息 |
|---|---|
| 项目默认值预填到表单，并标明取值来自项目 | `getByText(/来自项目「示例服务」的默认配置/)`：`element(s) not found`（**D3**） |
| 手动修改仓库地址后切换项目 | `Expected: "ssh://git.example.com/fork/repo"`，`Received: "ssh://git.example.com/team/other.git"`（**D1**：用户填写被新项目覆盖） |
| 项目切换把环境换成新项目的环境时，旧环境的版本不会残留 | `Locator: getByText(/^v0001 · /).visible()`，`Expected: 0`，`Received: 1`（**D2**：node 环境下仍显示 go 的 v0001） |
| 用户手动选择的环境与版本不会被项目切换覆盖 | 切换项目后 `getByText(/^python · /)` 消失（**D1 同类**：显式选择的环境被项目覆盖） |
| 提交体遵守后端继承规则 | 通过（修复前请求体已经符合后端规则，见 §5） |

运行产物目录：`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-<随机后缀>`
（`TEST_ARTIFACT_DIR`，按项目规范保留，未清理）。

---

## 4. 修复后的语义

### 4.1 字段级优先级（唯一规则，写在 `projectPrefill.ts`）

```text
字段当前取值有三态，来源由表单交互事实决定：

  用户显式设置（overridden）  → 任何项目切换都不覆盖；显示"已手动设置"
  项目默认值（applied）       → 重新应用新项目默认值；显示"来自项目「X」的默认配置"
  两者都不是（空 / 未设置）    → 按项目默认值填入；不显示来源
```

- **空值等于"未覆盖"**：与 Go 端 `firstNonBlank` 一致——清空仓库地址 / 工作目录不会让该字段变成
  "用户显式设置"，它仍然继承项目配置（也因此不会被显示成"手动设置"）。
- 环境与版本的附加约束：
  - 用户显式选择的环境**不会**被项目覆盖；
  - **写入环境时必定同时写入版本**（有效则填、无效则清空），因此不会出现"新环境 + 旧环境版本"；
  - 环境未变但当前版本不属于该环境时，版本被清空（跨环境 / 跨节点的旧版本一律不保留）；
  - 项目锁定的版本不是该环境的可用版本时只填环境、清空版本，**不猜版本**；
  - 项目环境在当前节点不存在或没有可用版本时**不预填环境**，并说明具体原因。
- 执行节点变化仍然清空环境与版本，并**同时清掉这两个字段的"用户已选"标记**：因节点变化而失效
  的选择必须重新选择，与"用户显式设置优先"不冲突。

### 4.2 界面表达

- 仓库地址、基础分支、容器内工作目录、语言环境与版本的表单项下方显示来源：
  - `来自项目「示例服务」的默认配置`
  - `已手动设置，不会随项目切换被覆盖`
  - `项目环境「node」在当前执行节点没有可用版本，未自动填入`
- 只在"用户显式设置过"且已选择项目时出现 `恢复项目默认值` 链接：没有它，用户在停止覆盖后只能
  手抄仓库地址才能回到项目默认值。该链接只是清掉该字段的覆盖标记并重新应用默认值，不引入新的
  状态来源。
- 来源标识只表达表单交互事实（是否由预填写入、是否被用户改过），**不推断服务端最终配置**；
  提交仍由服务端 `prepareTaskRequest` 决定。

### 4.3 请求体（未改动，仅核实）

`toCreateTaskRequest` 只提交表单真实取值：`project_id` 来自选择器返回的记录标识，`repository_url` /
`base_branch` / `environment` / `environment_version` 是必填校验后的值，`work_dir` 为空时省略，
**`caches` 不在表单中因此从不提交**（由服务端从项目继承）。该行为修复前后一致，已由 E2E 与 Go
单元测试共同覆盖。

---

## 5. 修改文件与行为变化

| 文件 | 变化 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/projectPrefill.ts` | `projectTaskPrefill` 改为返回 `{values, applied, environmentUnavailable}`，新增 `userOverrides` / `current` 入参；实现"显式设置优先 + 版本必须属于当前环境 + 不伪造不可用环境"；新增 `projectFieldSourceLabel` 与三处文案常量 |
| `lingting-agent-flow-ui/src/pages/tasks/CreateTaskModal.tsx` | 记录用户显式设置过的字段（`overriddenRef` + 状态镜像），项目切换 / 环境就绪只更新未覆盖字段；环境与版本处理器同样记录来源；节点切换清空并丢弃这两个字段的标记；新增字段来源说明与 `恢复项目默认值` 操作；打开弹窗时重置本次会话的来源记录 |
| `lingting-agent-flow-ui/tests/unit/projects.test.ts` | 预填用例改为新契约并扩展到 11 个（含显式覆盖、环境改写清版本、用户版本不属于环境、项目未锁版本），新增 3 个来源说明用例 |
| `lingting-agent-flow-ui/tests/e2e/task-create-project-inheritance.spec.ts`（新增） | 7 个浏览器用例：默认预填与来源标识、手改字段后切换项目、环境改写清版本、显式环境不被覆盖、先项目后节点、先节点环境后项目、提交体符合后端继承规则 |

**未修改**：Go 生产代码、`CreateTaskRequest`、HTTP 契约、数据库结构、`domain` / `app` 的任何规则、
`EnvironmentSelector` 与 `useEnvironments` / `useProjects`（数据来源与三态处理保持原样）。

---

## 6. 已核实但判定合理、未修改的项

1. **项目摘要行与字段来源说明并存**：`关联项目` 下方的"仓库 … · 分支 … · 环境 …"展示的是**项目记录
   本身的事实**，与表单取值可能不同（用户覆盖时）。它上面直接标注了所属项目，因此不构成"把项目
   默认值说成表单取值"；本轮保留，未做重排。
2. **必填字段被清空后不立即回填**：清空仓库地址 / 基础分支后不会立刻把项目默认值写回（交由表单
   必填校验阻止提交），只有再次切换项目或环境列表变化时才重新应用。这是有意的：立刻回填会让
   用户无法清空输入框。
3. **`priority` 默认值 `normal` 与 `merge_after_push` 默认 `false` 始终显式提交**：它们是表单默认值，
   与项目配置无关，后端也没有对应继承规则，因此不纳入"继承 / 覆盖"语义。
4. **未实现"从项目继承字段折叠为可展开覆盖"**：这是 README 中登记的独立候选（字段收敛），需要先
   核实真实使用路径；本轮只解决优先级与来源可见性，不改变表单结构。

---

## 7. 测试与验证（实际执行）

| 命令 | 实际结果 |
|---|---|
| `npx playwright test tests/e2e/task-create-project-inheritance.spec.ts --workers=1`（修复前） | **4 failed / 1 passed**，失败信息见 §3 |
| `npx playwright test tests/e2e/task-create-project-inheritance.spec.ts --workers=1`（修复后） | **7 passed** |
| `npx playwright test tests/e2e/task-create-{selectors,launch}.spec.ts tests/e2e/tasks.spec.ts --workers=1` | **37 passed**（创建任务相关既有用例未回归） |
| `pnpm test:unit:safe` | **597 passed / 27 个测试文件**（基线 589，新增 8 个用例） |
| `pnpm test:e2e:safe` | **225 passed / 0 failed**（基线 218 + 新增 7） |
| `pnpm check`（`vp fmt` + `vp lint --fix` + `tsc --noEmit`） | 通过 |
| `pnpm build` | 通过，产物写入 `internal/webui/dist`（仅分块体积提示，为既有警告） |
| Go 测试 | **未执行**：本轮未修改任何 Go 文件与后端契约（`git status` 仅 3 个修改文件 + 1 个新增测试文件，全部在前端） |

浏览器人工验证未执行：用户未提供地址，也未要求访问页面，按项目规范不主动使用 `chrome-devtools`。

---

## 8. 剩余风险

| 项 | 说明 |
|---|---|
| R1 行为变化 | 用户手改过的仓库地址 / 分支 / 工作目录 / 环境在切换项目后**不再跟随新项目**。这是本轮的目标语义，但历史用户可能预期"切换项目即重置"，因此界面用字段来源说明与 `恢复项目默认值` 明确表达 |
| R2 覆盖状态不持久化 | 覆盖记录只存在于当前弹窗会话，关闭后即重置（表单本身也随 `destroyOnHidden` 清空），不会跨会话残留 |
| R3 来源不等于服务端结果 | 未填写的字段仍由服务端按 `firstNonBlank` 继承项目配置；界面只在字段有值时说明来源，不对"最终会用什么"作推断 |
| R4 无浏览器人工验收 | 结论来自拦截接口的 E2E 与单元测试，没有对真实后端的可视化走查 |
| R5 未覆盖的场景 | 项目列表刷新（`useProjects.reload` 目前只在查询失败重试时使用）会触发一次重新应用，其行为由纯函数覆盖，但没有独立的 E2E 用例 |
| R6 与字段收敛候选的关系 | 本轮不做字段折叠；如果后续实施"从项目继承的字段折叠为可展开覆盖"，必须复用本轮的覆盖 / 来源模型，不能引入第二套优先级规则 |

---

## 9. 下一项候选（仅列有代码证据的方向）

1. **创建任务表单的字段收敛**（README 已登记的候选）：项目已经承载仓库地址、基础分支、工作目录、
   环境与版本，表单却仍然平铺 5 个字段 + 2 个选择器。若立项，必须复用本轮来源模型，先核实真实
   创建路径是否真的需要逐个确认。
2. **`verification_results` 遗留模型清理**：生产链路已不再使用该表与 `domain.decideVerdict`（G12
   文档 §13 记录），需要先确认是否还有读取方。
3. **私有仓库 Git 凭据支持**：`source.GitCredentialProvider` / `codesaver.GitCredentialProvider`
   仍无实现，成本与安全评审要求明显高于前两项。

`P4-M5` 仍为 `pending`：解除条件（`tasks.status` 分布、队列长度、排队等待分位数，或"列表卡顿"
"任务长期停在 `queued`"的实际报告）本轮同样没有获得，且本次改动不涉及性能、并发、存储或架构。
