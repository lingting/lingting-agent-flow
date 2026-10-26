# P4-M3-5 —— Web 展示代码保存三阶段状态

> 实施依据：`master` HEAD `d762597`（`:zap: 展示阶段进度、验证结果`）的实际代码核实与实施。
> 核实范围：`internal/httpapi/code_saves.go`、`internal/app/code_save_query.go`、
> `internal/domain/code_save.go`、`internal/storage/sqlite/code_saves.go`；
> 前端 `lingting-agent-flow-ui/src/lib/api/{types.ts, endpoints.ts, index.ts}`、
> `src/pages/tasks/{TasksPage.tsx, useExecutionRecords.ts}`、`tests/unit/`、`tests/e2e/`。
> 覆盖 P4-M1 缺口 **G8** 的展示部分（核验操作入口仍归 P4-M4 M4-2）。
>
> **本轮未修改任何后端代码**：`GET /api/v1/tasks/{taskID}/code-saves` 已由 P4-M2-6 交付，
> 本轮只做前端类型、查询封装与只读展示。

## 1. 原有后端 API 的实际字段契约

核实结论：后端契约与 P4-M2-6 交付文档一致，**未发现文档与代码不符之处**。

### 1.1 路由与响应结构

```text
GET /api/v1/tasks/{taskID}/code-saves
```

响应为 `records` + `total`（不分页，`total` 即本次返回的记录数），`records` 空时为 `[]`
而不是 `null`。排序由存储层给出：`ORDER BY created_at DESC, id DESC`（最新在前）。

### 1.2 字段表（`httpapi.CodeSaveRecordListEntryResponse`）

| JSON | Go 类型 | `omitempty` | 语义 |
|---|---|---|---|
| `id` | `string` | 否 | 记录标识 |
| `task_id` | `string` | 否 | 记录所属任务 |
| `project_id` | `string` | 是 | 可选项目标识 |
| `container_id` | `string` | 否 | 容器登记记录标识 |
| `docker_id` | `string` | 是 | 执行时使用的 Docker 容器 ID |
| `attempt_id` | `string` | 是 | 发起本次保存的执行尝试 |
| `remote_name` | `string` | 否 | Git 远程名称 |
| `repository` | `string` | 是 | 已脱敏的仓库地址 |
| `task_branch` | `string` | 否 | 任务分支 `agent/<task-id>` |
| `base_branch` | `string` | 是 | 识别出的基础分支 |
| `commit_status` | `string` | 否 | 提交结果 |
| `commit_sha` | `string` | 是 | 提交产生的 commit |
| `commit_message` | `string` | 是 | 提交信息摘要（单行、有上限） |
| `push_status` | `string` | 否 | 任务分支推送结果 |
| `remote_commit_sha` | `string` | 是 | 核验到的远程任务分支 commit |
| `merge_enabled` | `bool` | 否 | 是否请求推送后合并基础分支 |
| `merge_status` | `string` | 否 | 合并结果 |
| `merge_sha` | `string` | 是 | 合并后基础分支 commit |
| `stage` | `string` | 是 | 最后进入的操作阶段（`commit`/`push`/`merge`） |
| `error_code` | `string` | 是 | 可诊断的错误分类 |
| `error_summary` | `string` | 是 | 持久化时已脱敏的失败摘要 |
| `created_at` | `time.Time` | 否 | 记录创建时间 |
| `started_at` | `*time.Time` | 是 | 开始执行远程操作的时间 |
| `finished_at` | `*time.Time` | 是 | 操作结束时间 |
| `updated_at` | `time.Time` | 否 | 记录最后更新时间 |
| `baseline_commit_sha` | `string` | 是 | 提交前 HEAD，缺失表示无法判定 |
| `baseline_base_commit_sha` | `string` | 是 | 合并前远程基础分支，缺失表示无法判定 |
| `persist_status` | `string` | 否 | 记录写入状态，空值归一化为 `persisted` |
| `persist_error` | `string` | 是 | 最后一次写入失败的脱敏摘要 |
| `recovery_status` | `string` | 否 | 只读核验结论，空值归一化为 `unverified` |

### 1.3 状态取值（`internal/domain/code_save.go`）

| 维度 | 取值 |
|---|---|
| `CommitStatus` | `pending`、`not_attempted`、`no_changes`、`succeeded`、`failed`、`unknown` |
| `PushStatus` | `pending`、`not_attempted`、`succeeded`、`failed`、`conflict`、`unknown` |
| `MergeStatus` | `pending`、`not_requested`、`skipped`、`succeeded`、`failed`、`conflict`、`unknown` |
| `PersistStatus` | `persisted`、`unconfirmed` |
| `RecoveryStatus` | `unverified`、`confirmed_applied`、`confirmed_not_applied`、`unverifiable` |

### 1.4 错误映射（前端必须区分的三种结果）

| 场景 | 状态码 | 前端处理 |
|---|---|---|
| 查询成功且 `records: []` | `200` | "该任务当前没有代码保存记录" |
| 任务不存在 / 已逻辑删除 | `404` | 错误提示 + 重试入口，**不能当作空列表** |
| 查询服务未初始化 | `503` | 错误提示 + 重试入口 |
| 存储或内部错误 | `500` | 错误提示 + 重试入口 |

## 2. 前端新增内容

### 2.1 类型（`src/lib/api/types.ts`）

新增五个枚举类型与两个对象类型，全部按后端真实字段名与 `omitempty` 语义声明：

- `CodeSaveCommitStatus`、`CodeSavePushStatus`、`CodeSaveMergeStatus`、
  `CodeSavePersistStatus`、`CodeSaveRecoveryStatus`；
- `CodeSaveRecord`：所有枚举字段声明为 `XxxStatus | (string & {})`，服务端新增取值时
  类型不会隐藏真实数据；
- `CodeSaveRecordListResponse`：`records` + `total`。

`src/lib/api/index.ts` 导出全部新类型。

### 2.2 查询封装（`src/lib/api/endpoints.ts`）

```ts
export function taskCodeSavesPath(taskID: string): string {
  return `${taskPath(taskID)}/code-saves`;
}

export function fetchTaskCodeSaves(taskID: string, options: ApiRequestOptions = {}) {
  return apiRequest<CodeSaveRecordListResponse>(taskCodeSavesPath(taskID), options);
}
```

路径复用 `taskPath` 的空值拒绝与 `encodeURIComponent` 编码规则；请求走统一的
`apiRequest`，因此错误按 `ApiError` 分类（`http` / `network` / `timeout` / `aborted` /
`invalid_response`）。页面不拼接路径、不直接调用 `fetch`。

### 2.3 展示元数据（`src/pages/tasks/codeSaves.ts`，新增）

单一映射来源，只做"服务端事实 → 中文标签"，不含任何推断：

| 入口 | 职责 |
|---|---|
| `CODE_SAVE_COMMIT_META` / `CODE_SAVE_PUSH_META` / `CODE_SAVE_MERGE_META` | 三阶段全部取值的标签、颜色与"是否需要关注" |
| `CODE_SAVE_PERSIST_META` / `CODE_SAVE_RECOVERY_META` | 记录可信度与核验结论的标签 |
| `commitStatusMeta` / `pushStatusMeta` / `mergeStatusMeta` / `persistStatusMeta` / `recoveryStatusMeta` | 已知取值返回元数据；缺失与未知取值走"未提供 / 原始值"分支 |
| `stageLabel` | `commit`/`push`/`merge` → 中文阶段名；未知阶段原样展示 |
| `mergeRequested` / `mergeSummary` | 结合 `merge_enabled` 解释合并结果，`not_requested` 不显示为失败 |
| `pushSummary` / `mergeWarning` | `unknown` 明确说明"结果尚不确定、不能直接重试" |
| `recordWarnings` | `persist_status` / `recovery_status` 的独立警示 |
| `recoveryScope` | 核验结论只针对 `stage` 那一个阶段 |
| `recordNeedsAttention` | 五个维度中任一项需要关注即返回 true |

`isKnown*` 系列用 `Object.hasOwn` 判定，未知取值一律走"原始值"分支，避免 `DictTag`
因字典缺项渲染为空。

### 2.4 数据加载（`src/pages/tasks/useExecutionRecords.ts`）

新增 `useTaskCodeSaves(taskID)`，复用既有的 `useLoader` 状态机（`idle` / `loading` /
`ready` / `error`，`ready` + `error` 表示刷新失败但保留旧数据）。没有新增第二套请求状态机。

### 2.5 展示组件（`src/pages/tasks/CodeSavePanel.tsx`，新增）

每条记录分五组独立事实：

| 分组 | 内容 |
|---|---|
| 基本信息 | 记录标识、`stage`、关联尝试（缺失显示"未关联执行尝试"）、任务分支、基础分支、远程名称、仓库地址、创建 / 开始 / 结束 / 更新时间 |
| 提交 | `commit_status` 标签 + `commit_message` + `commit_sha` |
| 推送 | `push_status` 标签 + `remote_commit_sha` + 按状态生成的说明 |
| 合并 | `merge_enabled`（"已请求 / 未请求合并基础分支"）+ `merge_status` 标签 + `merge_sha` + `mergeSummary` |
| 记录可信度与核验 | `persist_status`（+ `persist_error`）、`recovery_status`（+ `recoveryScope`）、`error_code`、`error_summary` |

五种加载状态分别呈现：加载中、加载失败（带重试按钮）、空记录（明确说明"不代表任务成功、
代码无须保存或工作流已经完成"）、有记录、刷新失败但保留旧数据（提示可能不是最新状态）。

### 2.6 挂载位置（`src/pages/tasks/TasksPage.tsx`）

```tsx
{selected && <TaskExecutionPanel onRefreshTask={refreshTaskDetail} task={selected} />}
{selected && <CodeSavePanel task={selected} />}
```

记录属于**任务维度**，因此挂在任务详情下、与执行现场面板并列，不挂在某个被选中的步骤
之下；`attempt_id` 仅作为关联信息展示。列表与排序完全采用服务端顺序，前端不做任何排序。

## 3. 五个维度的语义区别

| 维度 | 回答的问题 | 不得用于 |
|---|---|---|
| `commit_status` | 提交这一步做了什么 | 推断推送是否成功 |
| `push_status` | 任务分支是否到达远端 | 推断合并是否成功 |
| `merge_status` | 基础分支合并的结果（含"没请求过"） | 推断推送是否成功 |
| `persist_status` | 这条记录本身是否被可靠写入 | 描述 Git 做了什么 |
| `recovery_status` | 只读核验对 `stage` 那个阶段的结论 | 当作三个阶段的总体结论 |

落实在代码与测试里的十条规则：

1. `commit_status = succeeded` 不表示推送成功（`CODE_SAVE_PUSH_META` 独立映射，用例
   "提交成功不表示推送成功"）。
2. `push_status = succeeded` 不表示合并成功（用例"推送成功不表示合并成功"）。
3. `merge_status = not_requested` 既不是失败也不是已合并（颜色为 `default`，
   `mergeSummary` 明确说明"没有请求合并基础分支；这不表示合并失败，也不表示已经合并"）。
4. `push_status` / `merge_status = unknown` 明确展示"结果尚不确定，需核验"，
   `pushSummary` 追加"不能直接重试"。
5. `persist_status = unconfirmed` 通过 `recordWarnings` 明确指出"可能落后于真实 Git 现场"。
6. `recovery_status` 必须结合 `stage`：`recoveryScope` 固定输出"核验结论只针对X阶段；它不
   代表提交、推送、合并三个阶段都已核验"。
7. `unverified` 与 `unverifiable` 文案不同（"尚未核验" / "无法取得可信事实，必须人工核验"）。
8. `merge_enabled = false` 时展示"未请求合并基础分支"，不出现"已请求合并基础分支"。
9. 空列表只说明"当前没有代码保存记录"，并明确否定"任务成功 / 代码无须保存 / 工作流完成"。
10. 任务状态与代码保存阶段状态分别展示，前端不从 `completed` 推导保存完整成功（E2E 用例
    "任务状态为 completed 不推导代码保存完整成功"）。

## 4. 修改文件与代码证据

前端修改：

| 文件 | 改动 |
|---|---|
| `lingting-agent-flow-ui/src/lib/api/types.ts` | 新增五个状态枚举、`CodeSaveRecord`、`CodeSaveRecordListResponse` |
| `lingting-agent-flow-ui/src/lib/api/endpoints.ts` | 新增 `taskCodeSavesPath`、`fetchTaskCodeSaves` |
| `lingting-agent-flow-ui/src/lib/api/index.ts` | 导出新类型与查询函数 |
| `lingting-agent-flow-ui/src/pages/tasks/useExecutionRecords.ts` | 新增 `useTaskCodeSaves` |
| `lingting-agent-flow-ui/src/pages/tasks/TasksPage.tsx` | 任务详情下挂载 `CodeSavePanel` |

前端新增：

| 文件 | 职责 |
|---|---|
| `lingting-agent-flow-ui/src/pages/tasks/codeSaves.ts` | 五个维度的展示元数据、缺失与未知取值处理、合并 / 推送 / 核验说明与警示文案 |
| `lingting-agent-flow-ui/src/pages/tasks/CodeSavePanel.tsx` | 只读记录面板（五组事实 + 五种加载状态） |
| `lingting-agent-flow-ui/tests/unit/task-code-saves.test.ts` | 30 个单元用例 |
| `lingting-agent-flow-ui/tests/e2e/task-code-saves.spec.ts` | 14 个浏览器用例 |

后端：**无改动**。未新增接口、未改 DTO、未改领域状态机、未新增数据库迁移。

## 5. 测试结果（实际执行）

| 命令 | 结果 |
|---|---|
| `npx vp test run tests/unit/task-code-saves.test.ts` | 通过：1 个文件 / 30 个用例 |
| `npx playwright test tests/e2e/task-code-saves.spec.ts` | 通过：14/14 |
| `npx vp test run`（全量前端单元测试） | 通过：16 个文件 / 248 个用例 |
| `npx playwright test`（全量前端 E2E） | 94 通过 / 3 失败（失败项见 §6.1） |
| `npx tsc --noEmit` | 通过（无输出） |
| `npx vp lint`（含 `vp fmt`、`vp lint --fix`、`tsc --noEmit`） | 通过（无输出） |
| `pnpm build`（`scripts/build-webui.mjs`） | 通过；仅有既有的 chunk 体积告警 |

`TEST_ARTIFACT_DIR`：每次运行由 `tests/internal/artifacts.ts` 生成独立随机目录
（`<OS 临时目录>/lingting-agent-flow-ui-tests/run-*`），运行结束后保留。本轮实际观测到的
目录例如 `run-CACfmF`、`run-zqEYpZ`、`run-CuHu7l`。

## 6. 未执行项、失败项与剩余风险

### 6.1 全量 E2E 的 3 个失败项（既有问题，已基线对照）

1. `tests/e2e/layout.spec.ts:43` `/machines 可访问且导航高亮、页面标题一致`
2. `tests/e2e/layout.spec.ts:64` `刷新后路由与导航状态保持`
3. `tests/e2e/tasks.spec.ts:478` `详情返回 404 时展示错误而不是伪造任务`

基线对照：`git stash push -u -- lingting-agent-flow-ui` 移除本轮全部前端改动后，执行
`npx playwright test tests/e2e/layout.spec.ts tests/e2e/tasks.spec.ts`
→ **3 failed / 23 passed**，失败用例与失败断言与本轮完全相同；随后 `git stash pop` 恢复。
本轮未修改这三个用例涉及的代码路径（`layout`、详情 404 分支的错误提示链路），也未修复它们。

### 6.2 未执行项

- 未运行任何 Go 测试（本轮未修改后端代码）。
- 未做真实环境验证：没有启动 `agentflow server` + 真实 SQLite / SSH / Docker / Git / Pi，
  全部浏览器测试都在网络层拦截 API。真实代码保存产生的记录未在本轮被观察。
- 未做真实远程 Git、SSH 或 Docker 验证；本轮的网络拦截测试只能证明 UI 对给定响应的处理
  行为，不能证明真实 Git 行为。

### 6.3 剩余风险

1. **未知取值只有原始值、没有中文标签**：服务端新增状态时界面走"原始值"分支，语义不会错，
   但需要同步 `codeSaves.ts` 的元数据表。这是刻意取舍：宁可展示原始值，也不猜测为成功。
2. **没有核验操作入口**：本轮只读。`id` 已经拿到并在面板上展示，但"发起只读核验"的按钮归
   P4-M4（M4-2）。
3. **没有自动刷新**：代码保存记录只在打开详情与点击"刷新代码保存"时重新读取，进度自动刷新
   归 M3-7。
4. **列表接口不分页**：记录数由"一次任务执行产生的保存次数"决定，量级很小；若未来出现
   大量记录的现场，应在此接口上统一加 `current` / `size`，而不是新增第二个入口。
5. **`recovery_status` 是记录级单字段**：一条记录里提交、推送、合并依次推进，但恢复结论只有
   一个字段，语义上属于"第一个仍待确认的阶段"。面板通过与 `stage` 并列展示与固定说明来避免
   误读，但没有逐阶段的核验结论字段（后端契约如此，前端不改契约）。

## 7. M3-5 退出条件逐项核验

| 条件 | 状态 |
|---|---|
| Web 通过真实 API 封装读取指定任务的代码保存记录 | 满足（`fetchTaskCodeSaves` → `apiRequest`；E2E 断言请求路径为 `/api/v1/tasks/task-1/code-saves`） |
| 任务详情能展示所有记录 | 满足（`CodeSavePanel` 遍历 `state.data`，E2E 用例"多条记录各自独立展示并保留服务端顺序"） |
| commit / push / merge 三阶段分别展示真实状态 | 满足（三组独立 `Descriptions` 与三个独立状态字典） |
| 持久化可信度与恢复核验结论独立展示，不混同 Git 操作结果 | 满足（`persist_status` / `recovery_status` 独立分组与独立警示文案） |
| `unknown` / `unconfirmed` / `unverifiable` / `pending` / `conflict` 不误报成功、不被擅自归并 | 满足（单元用例覆盖全部取值与颜色，E2E 覆盖 unknown / unconfirmed / unverifiable / 非成功状态） |
| 空列表、失败、刷新失败与未知状态都有明确的用户可见反馈 | 满足（空列表文案、错误 Alert + 重试、刷新失败保留旧数据 + 单独提示、未知取值原样展示） |
| 实际测试通过，或未通过项按真实结果列明 | 满足（§5、§6.1） |
| 路线图与交付记录与实际代码一致 | 满足（`README.md`、`ROADMAP.md`、`P4-product-experience-optimization.md` 与本文件） |

## 8. 下一步

`P4-M3` 整体**未完成**。已完成的子项：

- **M3-2** 由 P4-M2-7 交付，P4-M3-3 核实为满足。
- **M3-3** 于 2026-10-26 完成（任务最终结论与收尾闭环展示）。
- **M3-5** 于 2026-10-26 完成（本轮）。

M3-1 仍为部分满足（尝试的 `timed_out` / `cancelled` / `error_class` / `failure_stage`
未渲染，会话归档 / 导出状态未字典化）。

其余子项按
[`P4-product-experience-optimization.md`](./P4-product-experience-optimization.md) 推进：

- **M3-4** 续跑资格与阻塞原因展示（`resume-eligibility`，覆盖 G11，只读接口已就绪）。
- **M3-6** 工作流步骤起止时间补全（需先修 `workflowStatusResponse` 未赋值，覆盖 G18）。
- **M3-7** 进度自动刷新（覆盖 G19）。
- **M3-8** 事件时间线与修复周期历史（需先补 API，覆盖 G15、G16）。

建议顺序：M3-4 → M3-1 的剩余字段 → M3-7 → M3-6 / M3-8。代码保存记录的**核验操作入口**
归 P4-M4（M4-2），不在 M3 范围内。
