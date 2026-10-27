# P4-M4-2：代码保存核验 Web 操作入口（G8）

- **状态**：`completed`（2026-10-27）
- **里程碑**：`P4-M4`（支持必要的继续执行、取消、恢复和结果查看操作）
- **覆盖缺口**：G8「查看代码保存（提交/推送/合并）记录并核验」的**核验操作**部分
- **范围**：只接入前端。`POST /api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save`
  与 `GET /api/v1/tasks/{taskID}/code-saves` 均已存在，本轮**没有修改任何 Go 生产代码、
  核验领域规则、状态机、迁移脚本或第二套核验业务**。

---

## 1. 实施前后的基线 SHA 与工作区状态

| 阶段 | 分支 | HEAD SHA | 工作区 |
|---|---|---|---|
| 实施前 | `master`（与 `origin/master` 一致） | `277e458743b2041d1c7a3fee05bd9a7826f49caf` | `git status --short` **为空**（无预先修改） |
| 实施后 | `master`（改动未提交） | `277e458743b2041d1c7a3fee05bd9a7826f49caf` | 本轮新增 / 修改的前端文件（见 §4） |

开始前执行 `git fetch origin master`，`origin/master` 与 `HEAD` 同为 `277e458`
（`:zap:  事件时间线与修复周期历史`），上游未漂移；`git status --short` 为空，说明没有需要
保留的他人改动。任务书给出的基线提交 `277e458` 与实测一致。

核对的规划文档：`.steps/README.md`、`.steps/ROADMAP.md`、
`.steps/P4-product-experience-optimization.md`、`.steps/P4-M1-api-web-gap-inventory.md`（§G8
与 §7 M4 工作项表）、`.steps/P4-M4-1-task-retry-web-entry.md`、
`.steps/P4-M3-5-code-save-status-display.md`、`.steps/P4-M3-4-resume-eligibility-display.md`、
`.steps/P4-M3-7-progress-auto-refresh.md`、`.steps/P4-M3-8-event-timeline-repair-cycle-history.md`。

---

## 2. 实施前核实到的真实契约（全部结论来自代码）

### 2.1 路由与分发

| 接口 | 方法 | 分发位置 | 语义 |
|---|---|---|---|
| `/api/v1/tasks/{taskID}/code-saves/{recordID}/verify-code-save` | `POST` | `internal/httpapi/router.go` → `serveCodeRecovery` → `codeRecoveryHandler.verify`（`internal/httpapi/code_recovery.go:145`） | 同步的只读核验，成功返回 **200** |
| `/api/v1/tasks/{taskID}/code-saves` | `GET` | `serveCodeSaveList` → `codeSaveListHandler`（`internal/httpapi/code_saves.go`） | 记录列表，`record_id` 的真实来源 |

路径判定：`isCodeRecoveryPath` → `matchSubtasksPath(path, "verify-code-save")`，形状固定为
`{taskID}/code-saves/{recordID}/verify-code-save` 四段（`nestedPathID`）。方法不是 `POST`
时返回 405 + `Allow: POST`。

**只返回记录标识**：`nestedPathID` 的注释明确说明"任务标识由记录本身校验归属，接口不接受
任意记录的任意任务"，因此路径里的 `taskID` 只用于路由，真正的归属由应用层读取记录后比对。

### 2.2 请求 DTO

`httpapi.CodeSaveVerifyRequest`（`internal/httpapi/code_recovery.go:189`）只有两个可选字段，
由 `decodeOptionalJSONBody`（`DisallowUnknownFields`）解析：

| 字段 | 含义 | 缺省 |
|---|---|---|
| `container_id` | 可选覆盖的容器登记记录标识 | 空 → 使用记录上的 `record.ContainerID` |
| `work_dir` | 容器内 Git 工作区路径 | 空 → `domain.DefaultContainerWorkDir`（`/workspace/project`） |

请求体可完全省略（`io.EOF` 直接放行）。**没有**记录 ID、任务 ID、分支、基线、commit 或任何
"已核验事实"字段：这些一律从数据库读取。

### 2.3 响应 DTO

`httpapi.CodeSaveVerifyResponse` 由 `codeSaveVerifyResponse` 转换，五个字段分别输出：

| 字段 | 类型 | 语义 |
|---|---|---|
| `record` | `CodeSaveRecordResponse` | 核验**之前**从数据库读到的记录 |
| `observation` | `CodeSaveObservationResponse` | 现场只读查询结果 |
| `decision` | `CodeSaveDecisionResponse` | `domain.DecideCodeSaveRecovery` 的结论 |
| `updated` | `CodeSaveRecordResponse` | 写入核验结论**之后**读取到的记录 |
| `persisted` | `bool` | 核验结论是否成功写回数据库 |

`CodeSaveObservationResponse`：`read_failed`、可选 `read_error`、可选 `head_commit`、
可选 `remote_task_commit`、可选 `remote_base_commit`。

`CodeSaveDecisionResponse`：`status`、可选 `stage`、`summary`、`safe_to_retry`。

**关键差异（本轮必须处理）**：`CodeSaveRecordResponse` 与列表接口的
`CodeSaveRecordListEntryResponse` **不是同一个 DTO**：

- 主键字段名是 `record_id`，列表里叫 `id`；
- 没有 `merge_enabled`、`stage`、`baseline_commit_sha`、`baseline_base_commit_sha`、
  `persist_error`、`project_id`、`docker_id`、`attempt_id`、`remote_name`、`repository`、
  `commit_message` 与全部时间字段；
- `CodeSaveObservation` 里另有 `HeadParentCommit`、`ExpectedMergeCommit`、
  `MergeCommitResolvable` 三个字段**不下发到 HTTP**（只在领域层使用）。

因此前端新增独立类型 `CodeSaveRecordView`，**不**用列表类型冒充核验响应视图。

### 2.4 应用层语义（`app.ContainerService.VerifyCodeSave`）

调用链：`httpapi.verify` → `ContainerService.VerifyCodeSave`（`internal/app/code_save_verify.go`）
→ `store.GetCodeSaveRecord` → `loadContainer` → `ensureManaged` → `withRemoteDocker` →
`codesaver.Saver.Observe`（`internal/codesaver/verify.go`）→ `domain.DecideCodeSaveRecovery` →
`persistCodeSaveRecovery`。

逐条核实结果：

1. **省略请求体时的容器与工作区**：容器取 `record.ContainerID`（请求体 `container_id` 只在
   显式给出时覆盖）；工作区取 `domain.DefaultContainerWorkDir`（`codesaver.Saver.validate`
   的 `workDir == ""` 分支）。**因此前端不需要、也不应该提供这两个参数。**
2. **可信来源**：记录、分支、基线、容器登记记录全部来自 `store.GetCodeSaveRecord`。
3. **归属校验层**：应用层按 `recordID` 读取记录后使用记录自身的 `ContainerID` / `TaskID`，
   不信任路径里的 `taskID`；HTTP 层只把 `recordID` 传下去。
4. **远程读取内容**：`Saver.Observe` 只执行 `rev-parse` / `ls-remote` / `rev-list` 等只读
   Git 查询。注释明确："不提交、不推送、不切换分支、不写入 `code_saves` 记录"。
   核验**不重新执行代码保存**，不删除或接管容器。
5. **各失败的返回**：
   - 记录或容器登记记录不存在（`storage.ErrNotFound`）→ **返回错误** → HTTP 412
     （`CodePreconditionFailed`）；
   - 容器已删除 / 已 detached / 节点不可达 / Docker 不可访问 / Git 查询失败 →
     **不返回 HTTP 错误**，而是降级为 `observation.read_failed=true` +
     `decision.status=unverifiable` 并落盘（`degradeCodeSaveUnverifiable`）；
   - 结论写回失败或冲突 → **仍返回 200**，`persisted=false`，`updated` 是数据库中的真实记录
     （`persistCodeSaveRecovery` 的 `reloadCodeSaveRecord`）。
6. **错误映射**：`writeError` 固定为 **412 / `precondition_failed`**，message 保留
   `err.Error()` 原文（无 400/404/409/500 分支）。

### 2.5 与任务书已知描述一致 / 不一致之处

任务书列出的响应字段与 §2.3 完全一致。任务书未提及的两点差异按代码事实处理：

| 任务书/文档 | 代码事实 | 本轮处理 |
|---|---|---|
| 「容器不存在、不可访问」可能返回错误 | 只有**记录层**不存在才返回 412；容器层失败会降级为 `read_failed` 的 200 响应 | 前端两种都处理：412 走"请求失败、无结论"，200+`read_failed` 走"现场读取失败" |
| 未提及 `record` / `updated` 的 DTO 与列表不同 | 二者是 `CodeSaveRecordResponse`（字段名 `record_id`） | 新增 `CodeSaveRecordView` 类型，不混用 |

### 2.6 为什么没有猜测容器 ID 或工作区路径

1. 请求 DTO 的两个字段都可省略，服务端有确定的兜底来源（记录 + `DefaultContainerWorkDir`）；
2. 记录列表虽然返回 `container_id`，但工作区路径在列表里**根本不存在**，从展示数据反推只能
   编造；
3. `codesaver.Saver.validate` 会校验 `domain.ValidateContainerWorkDir`，猜测值只会制造
   `CodeInvalidArgument` 错误，而不是让核验更准确；
4. 应用层注释明确要求"调用方不能指定任意仓库状态作为已核验事实"。

**结论：前端始终发送空请求体 `{}`。**

---

## 3. 核验的真实业务边界（前端遵守的约束）

| 事实 | 不能推出的结论 |
|---|---|
| `observation.read_failed=false` | 不能推出"操作已经发生"：只说明现场可读 |
| `observation.read_failed=true` | 不能推出"操作没有发生"：其余字段一律不可信 |
| `decision.status=confirmed_applied` | 不能推出任务完成或代码保存整体成功 |
| `decision.status=confirmed_not_applied` | 不能推出可以自动重试（`persisted` / `safe_to_retry` 仍是独立事实） |
| `decision.safe_to_retry=true` | 不构成执行许可：页面不会自动提交、推送、合并或重试 |
| `persisted=true` | **不等于**核验通过：它只说明结论已按契约写回 |
| `persisted=false` | **不等于**没有结论：`observation` / `decision` 仍是本次取得的真实事实 |
| `record` 与 `updated` | 是**两个时间点**的记录视图，不得互相替代，也不得用来覆盖其他记录 |
| 字段缺失（如 `head_commit` 为空） | 不推出"未发生变化"或"远程与本地一致"：只表示没有结果 |

---

## 4. 新增与修改的前端文件及必要性

| 文件 | 改动 | 必要性 |
|---|---|---|
| `src/lib/api/types.ts` | 新增 `CodeSaveVerifyRequest`、`CodeSaveObservation`、`CodeSaveDecision`、`CodeSaveRecordView`、`CodeSaveVerifyResponse` | 按真实 Go DTO 声明类型；`CodeSaveRecordView` 单独定义是因为它与列表的 `CodeSaveRecord` 不同（见 §2.3） |
| `src/lib/api/endpoints.ts` | 新增 `taskCodeSaveVerifyPath`、`verifyCodeSave` | 页面不得直接 `fetch`；路径段编码与空值拒绝沿用既有 `taskPath` / `requireSegment` 惯例；请求沿用同步写操作"不设客户端超时"的惯例 |
| `src/lib/api/index.ts` | 导出上述类型与函数 | 保持统一出口 |
| `src/pages/tasks/codeSaveVerification.ts`（新增） | 观察行展开、判定解释、`safe_to_retry` 解释、`persisted` 解释、前后对比、记录归属判定、请求票据与迟到响应判定、结果不确定提示 | 纯函数与纯数据，使"五组事实互不替代""切换任务丢弃迟到响应""未知取值保留"都可被直接断言 |
| `src/pages/tasks/useCodeSaveVerification.ts`（新增） | 按记录归档的核验状态 + 显式核验 + 每记录防重 + 核验后刷新列表 | 唯一状态实现；避免在面板里堆竞态逻辑 |
| `src/pages/tasks/CodeSaveVerificationSection.tsx`（新增） | 单条记录的核验入口与 A/B/C/D 四块结果展示 | 面板已有 239 行且职责是"列出记录"，核验展示是独立可演进职责（单文件限制规则） |
| `src/pages/tasks/CodeSavePanel.tsx` | 说明文案去掉"不发起核验"；每条记录渲染核验区块并传入 `entry` / `onVerify` / `onClear` | 接入点；记录列表与核验结果彼此独立，不改动既有五组事实展示 |

**未改动**：任何 Go 文件、迁移脚本、`internal/domain/code_save_recovery.go`、
`app.ContainerService`、`internal/codesaver`、`useTaskLiveSnapshot`、`liveSnapshot`、
`autoRefresh`、`requestGate`、`useTaskCodeSaves`、`useExecutionRecords`、`codeSaves.ts`。

---

## 5. 展示规则

### 5.1 核验入口

- 每条记录在自己的信息区域里有一个"核验代码保存"按钮，传入的是**该记录真实的
  `record.id`**；路径由 `taskCodeSaveVerifyPath(taskID, record.id)` 构造。
- 只有用户点击才发起请求：`useCodeSaveVerification` 没有任何触发请求的 effect，依赖只有
  `taskID`（用于在切换任务时**清空**状态），不参与 5 秒自动刷新，也没有任何轮询。
- 在途时按钮 `disabled` + `loading`，文案变为"核验中"，并显示"正在核验代码保存"提示。
- **未按记录状态做任何前端白名单**：代码里没有 `if (record.recovery_status === ...)` 决定
  显示/隐藏入口。领域层没有任何"该记录允许核验"的服务端资格接口，`VerifyCodeSave` 也只对
  "记录/容器登记记录不存在"报错；因此保留显式入口、把真实判定交给服务端，是唯一有代码依据的
  做法。

### 5.2 A. 现场观察（`observation`）

| 情况 | 展示 |
|---|---|
| `read_failed=false` | "否：只读查询已完成（这只说明现场可读，不代表操作已经发生）" |
| `read_failed=true` | "是：本次只读核验没有完成，其余现场字段不可信" |
| `read_error` 存在 | 单独一行"读取失败原因"，值为服务端脱敏后的原文 |
| `head_commit` / `remote_task_commit` / `remote_base_commit` 有值 | 各自的真实 commit |
| 三者任意缺失 | 中性占位符"服务端未返回…（缺失不代表没有变化，需结合记录与判定理解）" |

不出现"未发生变化""远程与本地一致"一类推断文案（单测与 E2E 都断言这两个词不存在）。

### 5.3 B. 核验判定（`decision`）

| 字段 | 展示 |
|---|---|
| `status` | `CODE_SAVE_RECOVERY_META` 的中文标签；未知取值**原样展示**且不用成功色 |
| `stage` | `stageLabel` → "提交阶段/推送阶段/合并阶段"；缺失为"未记录阶段"；未知值原样 |
| `summary` | 服务端原文；为空时"服务端未提供判定说明" |
| `safe_to_retry` | 单独一行保留真实布尔值（"服务端返回 safe_to_retry = true/false"），并配准确说明：`true` 明确"页面不会自动重试、必须显式发起"；`false` 明确"可能产生重复提交或覆盖远程历史"；非布尔时明确"不得据此判断可以安全重试" |

### 5.4 C. 核验后的记录（`record` vs `updated`）

按提交 / 推送 / 合并 / 记录写入状态 / 核验结论五个维度逐项做**前后对照**：

- 变化时展示 `旧值 → 新值（核验前后不同）`；
- 未变化时展示 `值（核验前后相同）`；
- 全部未变化时额外说明"这不代表核验没有发生"；
- 有变化时额外说明"上方的核验后记录只描述这一条记录；其他代码保存记录以列表中的服务端数据
  为准"。

由于 `CodeSaveRecordResponse` 没有 `stage`，对比维度不含阶段；字段缺失只表示"该视图没有这个
字段"，不会被读成"状态被清除"。

### 5.5 D. 结果持久化（`persisted`）

| 值 | 展示 |
|---|---|
| `true` | 绿色"核验结论已写入记录" + "不代表代码保存成功或核验通过，请以观察与判定为准" |
| `false` | 橙色"核验结论未成功持久化" + "上面的观察与判定仍是本次真实事实，但记录里的结论可能是旧值；请用「刷新代码保存」重新读取后再决定下一步" |

`persisted=false` 时 A/B/C 三块**照常展示**（E2E 用 `persisted=false` + `confirmed_applied`
断言"未持久化"和"已确认操作发生"同时可见）。

### 5.6 加载 / 错误 / 刷新七种状态

| 状态 | 处理 |
|---|---|
| 1. 未执行 | 只显示入口与说明，无结果区 |
| 2. 请求进行中 | 按钮 loading + "正在核验代码保存"提示 |
| 3. 成功并取得完整响应 | 展示 A/B/C/D |
| 4. 请求失败 | 红色"核验请求失败" + 服务端 message + "本次请求没有取得任何可展示的核验结论，页面不会自动重新核验"；**不渲染任何 A/B 区块** |
| 5. `persisted=false` | D 区橙色警示，A/B/C 保留 |
| 6. 超时 / 网络中断 / 取消 | 在 4 之外额外显示"请求结果无法确认"：明确"服务端可能已完成核验并写回结论"，要求先刷新记录再显式决定，**不自动重发** |
| 7. 核验后刷新列表失败 | 由记录列表自身的"代码保存记录刷新失败"提示呈现；**已取得的核验响应不被清除** |

核验结果与记录列表是两类事实：`useCodeSaveVerification.entries` 与 `useTaskCodeSaves.state`
互不写入。`refreshError` 字段保留在 entry 类型中但当前不由 Hook 写入——列表刷新失败已经由面板
的 `state.error` 提供了同样信息，重复提示会造成同一失败出现两次。

---

## 6. 并发、竞态与防重

参考 `useTaskRetry.ts`（票据 + 在途 ref + 卸载标记）与 `useResumeEligibility.ts`（快照按任务
归属）的既有模式，未引入第二套通用请求框架。

| 场景 | 处理 |
|---|---|
| 任务切换 | `activeTaskRef` 记录当前任务；`useEffect([trimmed])` 清空全部 entries 并重建票据计数器；迟到响应因任务标识不符被丢弃（纯函数 `acceptVerifyResponse`）。E2E 断言 A 的迟到判定不出现在 B 的详情中 |
| 记录切换 / 跨记录并发 | entries 按真实 `record.id` 归档；票据计数器按 `taskID/recordID` 独立计数。不同记录可以各自在途，A 的 loading 不会显示在 B 上 |
| 迟到响应 | 同一记录多次核验只有最后一次生效（`gate.requests[key] === ticket.requestID`） |
| 组件卸载 | `mountedRef` 拦截，卸载后不写入任何状态 |
| 重复提交 | `runningRef`（同步 `Set`）在 `await` 之前占位，同帧内连续点击不会发第二个请求；`canSubmitVerify` 同时驱动按钮 `disabled`。E2E 用延迟响应构造在途状态并断言请求数恒为 1 |
| 核验后刷新 | 请求结束后调用 `reload()` 重新读取列表；刷新失败只影响列表，不影响 entries |
| 取消语义 | 客户端没有主动取消入口；`acceptVerifyResponse` 的 `aborted` 分支保留给未来，且不会把"客户端不再等待"显示成"服务端已取消" |
| 自动刷新 | 核验不在 `useTaskLiveSnapshot` 的 5 秒循环内，也没有自己的轮询；E2E 断言等待 + 手动刷新列表后核验请求数仍为 0 |
| 响应归属校验 | `responseBelongsTo` 要求 `record.record_id` 或 `updated.record_id` 等于本次记录 ID；不一致时报错而不是把结论挂到别的记录上 |

---

## 7. 测试与验证的实际执行结果

全部命令在 `lingting-agent-flow-ui/` 下执行。测试授权来自本次任务书 §7 与 §8 的明确要求。

| # | 命令 | 是否执行 | 退出码 | 结果 |
|---|---|---|---|---|
| 1 | `npx vitest run tests/unit/task-code-save-verification.test.ts` | 已执行 | 0 | 1 个文件通过，**42 passed** |
| 2 | `npx playwright test tests/e2e/task-code-save-verification.spec.ts --workers=1` | 已执行 | 0 | **10 passed**（21.3s） |
| 3 | `pnpm test:unit:safe` | 已执行 | 0 | 24 个文件通过，**485 passed** |
| 4 | `pnpm test:e2e:safe` | 已执行 | 0 | **165 passed / 0 failed**（6.2m） |
| 5 | `pnpm check`（`vp fmt && vp lint --fix && tsc --noEmit`） | 已执行 | 0 | 通过（122 files），无 warning |
| 6 | `pnpm build` | 已执行 | 0 | 通过，产物写入 `internal/webui/dist` |

测试产物目录（`TEST_ARTIFACT_DIR`，保留未清理，例如）：
`C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-jf0q6O`（定向单元测试）、
`...\run-RYS6bk` / `...\run-hWnreb`（E2E 诊断运行）。
`pnpm test:e2e:safe` 的完整报告位于其自身输出的 `run-*` 目录下 `playwright-report/`。

### 7.1 过程中的失败与处理（如实记录）

| 失败 | 原因 | 是否本轮回归 | 处理 |
|---|---|---|---|
| 单元测试首轮：`CodeSaveRecordView` 未从 `@/lib/api` 导出；`patchEntry` 中 `running` 被重复指定 | 新增文件与导出顺序问题 | 是（本轮新增代码） | 补齐 `index.ts` 导出；调整展开顺序为 `{ ...current[recordID], running: false, ...patch }` |
| E2E 首轮 3 项失败（"显式触发核验后展示…"、"read_failed=true…"、"快速切换任务…"） | ①观察区 commit 与记录自身的 `commit_sha` 用了同一个 `'1'.repeat(40)`，`getByText` 命中 3 个元素；②`openDetail` 硬编码"实现登录接口"，切换任务用例无法打开"任务 A" | 是（测试自身缺陷） | 观察值改用 `a`/`b`/`c` 开头；`openDetail` 增加 `taskName` 参数 |
| E2E 次轮 2 项失败（strict mode violation on "已确认操作未发生" / "无法取得可信结论"） | 判定状态标签与 C 区前后对比行同时包含同一文案（后者带"（核验前后不同）"后缀） | 是（测试断言不够精确，**不是**产品缺陷——两处本来就应同时出现） | 断言改为 `{ exact: true }` |
| `pnpm check` 首轮 TS2322/TS2345（`preparePage` 的 `task` 选项类型） | 测试辅助函数的可选参数与 `sampleTask()` 的字面量类型不一致 | 是（测试类型问题） | 移除未使用的 `task` 选项，固定使用 `sampleTask()` |

### 7.2 定向用例覆盖（对应任务书 §7.1 的每一项）

单元测试（`tests/unit/task-code-save-verification.test.ts`，42 项）：

- 路径构造、URL 编码（任务与记录均编码）、空标识拒绝；
- 请求方法 `POST`、路径、请求体为 `{}`、只发一次；60 秒假时钟推进后仍未中断（证明没有客户端
  超时）；404/412 保留服务端业务说明；网络中断只发一次；空响应体不被当作成功；DTO 字段与真实
  契约一致（`record_id`、五个分区字段）；
- `read_failed=false/true` 的真实文案与"其余字段不可信"；`read_error` 缺失不渲染；三个 commit
  字段独立；缺失时中性占位符且**不出现**"一致""未发生变化"；空白字符串按缺失处理；
- `decision.status`/`stage`/`summary`/`safe_to_retry` 独立展示；未知状态原样且非成功色；缺失
  阶段与缺失说明的中性文案；未知阶段原样；`safe_to_retry` 三种取值语义（true 不构成许可）；
- `persisted=true` 不等于核验通过；`persisted=false` 保留真实结论；三者可同时取不同值（不合并
  成一个布尔）；
- `record`/`updated` 逐维度对比、SHA 变化、全部相同、字段缺失不被读成"被清除"；
- 响应记录归属校验；
- 在途防重（无 entry / 在途 / 结束后可再次）；
- 票据：同一记录只有最后一次生效、不同记录独立、切换任务丢弃、取消不接受、票据键包含任务与
  记录；
- 结果不确定的错误分类与提示文案；
- 既有 `codeSaves.ts` 的展示映射与 `describeApiError` 未回归（由 `task-code-saves.test.ts`
  继续覆盖，全量 485 项通过）。

E2E（`tests/e2e/task-code-save-verification.spec.ts`，10 项）：

1. 有两条记录时每条各有自己的核验入口，打开详情与加载列表**不**发核验请求；
2. 显式触发后 A/B/C/D 全部按真实响应展示（含 `safe_to_retry=true` 与"页面不会自动重试"）；
3. `persisted=false` 时明确未持久化且真实判定仍可见；
4. `read_failed=true` 时如实展示读取失败，不出现"已确认操作发生/未发生"；
5. 服务端 412 时显示错误、不渲染任何 A/B 区块、不自动重发；
6. 核验后列表刷新 500 时核验响应仍可见；
7. 在途时按钮禁用 + "核验中"，强制重复点击后请求数仍为 1；
8. 两条记录的判定说明各自独立、互不串线；
9. 在途时切换到任务 B，A 的迟到响应不出现在 B 的详情里；
10. 等待 + 手动刷新列表后核验请求数仍为 0（不进自动刷新循环）。

### 7.3 未执行项

| 项目 | 状态 | 说明 |
|---|---|---|
| Go 测试（`go test`）、`gofmt`、`go vet` | **未执行** | 本轮未修改任何 Go 代码 |
| 真实后端联调（真实 SQLite + 容器 + SSH + Docker） | **未执行** | 用户未要求；E2E 全部为浏览器层拦截，不能证明真实核验在容器内可用 |
| 浏览器人工验收（真实地址访问） | **未执行** | 用户未提供地址，也未要求访问页面 |

---

## 8. 未解决问题、风险与后续工作

1. **`refreshError` 字段暂未写入**：`CodeSaveVerifyEntry.refreshError` 保留在类型中但 Hook 不写入，
   因为列表刷新失败已由面板的 `state.error` 呈现。若后续希望核验结果区自带该提示，需要
   `useTaskCodeSaves` 暴露失败原因（当前 `reload()` 无返回值）。
2. **`CodeSaveRecordView` 不含 `stage`**：C 区的前后对比只能覆盖五个字段。服务端若希望核验响应
   提供 `stage`，需先扩展 `httpapi.CodeSaveRecordResponse`（本轮未改 Go）。
3. **`observation` 的三个内部字段不下发**：`HeadParentCommit`、`ExpectedMergeCommit`、
   `MergeCommitResolvable` 是判定的关键证据，但 HTTP 层没有输出。用户能看到结论却看不到归因依据
   （例如"第一父不是基线"）。这是 DTO 层的信息缺口，补全需要改 Go（本轮范围外）。
4. **核验入口对所有记录一律显示**：这是任务书 §5.1 的要求（无服务端资格接口时不得在前端发明
   白名单）。代价是对已收敛记录（如 `recovery_status=confirmed_applied`）仍可点击，服务端会正常
   返回 `unverified`（"记录中没有待确认的阶段，无需恢复"）。
5. **核验是重操作**：需要 SSH + Docker + 容器内多次 Git 查询，因此请求不设客户端超时。若服务端
   长时间无响应，用户只能等浏览器/网络层超时，之后按"结果无法确认"处理。
6. **本轮不覆盖 G8 之外的内容**：G9（容器 `delete`/`detach`）、G11 的后端联动（M4-4）、G20
   （单步诊断）仍归后续子任务。

## 9. 本轮范围声明

- **未修改任何 Go 生产代码**（`internal/`、`cmd/`、`tests/*.go`、迁移脚本全部未改）。
- **未改变核验领域规则**：`domain.DecideCodeSaveRecovery`、`domain.ApplyRecovery`、
  `app.ContainerService.VerifyCodeSave` 与 `internal/codesaver` 的行为完全不变。
- 未新增第二套核验业务、未新增表或迁移、未改动工作流编排与状态机。
