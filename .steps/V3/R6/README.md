# V3 R6：前端适配器

**状态：** 已完成。37 条前端用例（24 个 Playwright spec + 13 个 Vitest 文件）已接入统一执行器，
`--audit-coverage --audit-stage R6` 退出码 0（37 / 37 覆盖、缺失 0、多余 0），连续全量运行与迁移前
两套入口逐条一致（Playwright 297 条、Vitest 361 条、跳过 0），每次零残留 `passed`。残留的间歇失败与
新旧入口共同的 UI 时序抖动有关，处置见 §7.1 与 §8。
**上游文档：** [路线总规划](../ROADMAP.md)、[R5 迁移](../R5/README.md)、[R2 资源契约](../R2/README.md)。
**范围：** 把 13 个 Vitest 文件与 24 个 Playwright spec 接入统一执行器，一个文件一条 Case。
不改前端产品代码与断言语义，不动 Go 用例（R5）、不改 `Makefile` / CI / `docs/`。

---

## 1. 目标

1. 前端测试不再由 `playwright test` / `vitest` 的并发策略决定并发，改由统一调度器决定。**已满足**
2. Vite dev server 从 Playwright 的 `webServer` 变成 `frontend-dev-server` 共享资源。**已满足**
3. 37 个文件各有独立产物目录、独立结论，并进入统一报告。**已满足**
4. 先测冷启动开销，再决定批量迁移；跑得比现在慢一倍以上的方案不接受。**已满足**（数据见 §4.1）

---

## 2. 输入与事实

- 24 个 spec 全部使用受控后端（`page.route` 或 `tests/e2e` 下的 helper），不依赖 Go 服务、Docker、
  SSH 或 Pi（R0 §8）。审计结论见 §4.6。
- `playwright.config.ts`（迁移前）：`webServer.command = "vp dev"`，`baseURL = http://127.0.0.1:26390`，
  `fullyParallel: false`，`workers` 来自 `tests/internal/test-resource-policy.ts`，产物写入随机
  `TEST_ARTIFACT_DIR`。
- `vite.config.ts`（迁移前）：`server.port = 26390`（`strictPort: true`），`test.include = ["tests/**/*.test.ts"]`，
  Vitest worker 上限来自同一策略文件（默认 4，上限 8）。
- `tests/internal/artifacts.ts` 定义随机产物目录；接管后不得出现两套产物路径。
- R0 判定两者 `NeedsReview` 都是「共享 dev server 的写操作策略」，不是发现了真实写入（§4.6 已确认）。
- R2 已冻结 `frontend-dev-server`、`browser`、`node-worker` 的 payload 契约，提供者由本阶段实现；
  本阶段对 `node-worker` 的 payload 做了最小调整（D8）。

---

## 3. 交付物

| 位置 | 内容 | 落地形态 |
|---|---|---|
| `tests/runner/frontend` | 37 条 Case 的注册与结果映射 | `case.go` 登记 37 条，`playwright.go` / `vitest.go` 负责调用与结论映射，`cli.go` 负责子进程与进程树回收，`project.go` 负责项目定位与依赖检查 |
| `internal/testrunner` | 前端三类资源的提供者 | `resource_frontend.go`：`FrontendDevServerProvider`、`FrontendSessionProvider`（browser / node-worker 共用） |
| `internal/testrunner` | 覆盖对账支持非 go 条目 | `Case.LegacyID` + 按清单 ID 匹配，逐文件统计 |
| `lingting-agent-flow-ui/playwright.config.ts` | 去掉 `webServer`，并发与地址改由注入 | `baseURL` 取 `AGENTFLOW_UI_BASE_URL`，`workers: 1`，JSON reporter 由 `AGENTFLOW_UI_JSON_REPORT` 打开 |
| `lingting-agent-flow-ui/vite.config.ts` | 端口与并发改由注入 | host/port 取 `AGENTFLOW_UI_HOST` / `AGENTFLOW_UI_PORT`，`maxWorkers: 1` |
| `tests/internal/test-resource-policy.ts` | 并发决策去留 | 整体删除（D6） |
| `tests/internal/artifacts.ts` | 产物路径统一 | 优先 `AGENTFLOW_UI_ARTIFACT_DIR`，独立运行退回临时目录 |
| 本文 §4 / §5 / §7 / §8 | 冷启动基数、写策略审计、结论 | 已填写 |

---

## 4. 实施步骤与结果

### 4.1 冷启动基数（实测，未改代码前采集）

| 测量 | 命令 | 结果 |
|---|---|---|
| 24 个 spec 一次全量 | `pnpm exec playwright test --workers=4` | 297 passed / 4m03s |
| 单个 spec 独立运行 | `pnpm exec playwright test tests/e2e/layout.spec.ts --workers=1` | 13 passed / 17.1s（含 dev server 冷启动） |
| 单个 spec 独立运行 | `pnpm exec playwright test tests/e2e/tasks.spec.ts --workers=1` | 13 passed / 24.7s |
| 13 个 Vitest 文件全量 | `pnpm exec vitest run` | 361 passed / 4.2s |
| 单个 Vitest 文件 | `pnpm exec vitest run tests/unit/tasks-page.test.ts` | 14 passed / 2.9s |

结论只用于判断「一文件一调用」的相对开销，不作为通过/失败证据：单文件冷启动约 2–5s，Vitest 每文件
约 1.9s 固定开销，Playwright 的主要成本是每个 Case 一次 Node + 浏览器启动，但文件间的并发由调度器
并行承担，总体与基线持平（§7.1）。

### 4.2 调用方式

按 D2 冻结为 CLI 子进程、**一文件一次调用**（`pnpm exec playwright test <file> --workers=1`、
`pnpm exec vitest run <file> --maxWorkers=1`）。

批处理（一次调用跑完全部文件，再按 JSON 映射到多条 Case）未采用，理由与实测：

1. 批量执行的唯一可行落点是把整套前端测试塞进共享资源的分配阶段，与「一条 Case 一次调用」的
   worker 模型冲突：`--select` 单条用例会连带跑全量，失败结论也无法归属到具体 Case 的产物目录。
2. 实测单文件调用并没有带来整体变慢（§7.1：适配层 4m09–4m48，基线 4m03），而 Vitest 的固定开销
   总量约 25s，远低于 §1 目标 4 的「慢一倍」红线。

### 4.3 资源提供者

- `frontend-dev-server`（run-shared）：在 `lingting-agent-flow-ui` 下启动 `pnpm exec vp dev`，端口由
  `freeTCPPort` 分配并经 `AGENTFLOW_UI_PORT` 注入，`AGENTFLOW_UI_HOST` 固定 `127.0.0.1`；等待
  `http://127.0.0.1:<port>/` 返回 200（上限 180s，覆盖首次依赖预构建）。句柄给出 `base_url` 与 `pid`，
  fingerprint 取 `package.json` 的 SHA-256。释放后自证三件事：进程消失、端口不再可连、HTTP 不再可用。
- `browser`（case-exclusive）：分配会话目录（`<运行工作空间>/shard_browser_browser/<token>`），探测并缓存
  Playwright 版本，句柄给出 `browser=chromium`、`version`、`session_dir`；不自行驱动浏览器。
- `node-worker`（case-exclusive）：与会话目录同理，探针为 `pnpm exec node --version`，句柄给出
  `runtime=node`、`version`、`session_dir`。
- 会话类资源的进程登记：用例启动 CLI 后立刻调用 `testrunner.RecordFrontendProcess` 把 pid 写进会话目录；
  释放时若登记进程仍存活先按进程树终止，终检再自证进程消失。CLI 以独立进程组启动
  （`testrunner.CommandProcessGroup`），超时或取消时用 `KillProcessTree` 回收整棵树。

### 4.4 配置注入

- `playwright.config.ts`：删除 `webServer`；`baseURL` 取 `AGENTFLOW_UI_BASE_URL`，独立运行时退回
  `http://127.0.0.1:26390`（此时需自己先起 dev server）；`workers` 固定 1，不再读取任何 worker 环境变量；
  `outputDir` / HTML 报告写在注入的产物目录；`AGENTFLOW_UI_JSON_REPORT` 给出时追加 JSON reporter。
- `vite.config.ts`：`server.host` / `server.port` 取 `AGENTFLOW_UI_HOST` / `AGENTFLOW_UI_PORT`，`strictPort`
  保留；`test.maxWorkers` 固定 1。
- 并发：单次调用固定 `--workers=1` / `--maxWorkers=1`；文件之间的并发只由调度器控制。

### 4.5 结果映射与产物归档

- 每条 Case 的产物落在 `<artifact-root>/<run-id>/cases/frontend__<runtime>-<name>/artifacts/`：
  Playwright 的 `playwright.json`、`playwright-report/`、`playwright-output/`（截图与 trace）、`playwright.log`；
  Vitest 的 `vitest.json` 与 `vitest.log`。
- 结论以 JSON 报告为准，退出码只作为冲突检测：退出码非 0 而 JSON 报 0 失败时判为冲突失败；报告缺失或
  用例数为 0 同样判失败，不把「没读到结论」当作通过。
- 跳过（`test.skip` / 条件跳过）带原因进入报告：整文件全部跳过时记 `skipped` 并写明子用例名。

### 4.6 写策略审计

逐 spec 结论（24 个文件）：

| 类别 | 文件 | 结论 |
|---|---|---|
| 直接 `page.route` 拦截后端 | `api-connection`、`projects`、`settings-environments`、`tasks`、`task-*`（14 个，共 18 个文件） | 只读，无服务端写入 |
| 通过 helper 拦截后端 | `planning-analysis-states`、`planning-confirm`、`planning-states`、`planning-workbench`（`tests/e2e/planning-backend.ts`）、`requirement-delete`（`tests/e2e/requirements-backend.ts`），共 5 个文件 | 只读，无服务端写入 |
| 纯渲染 | `layout`，共 1 个文件 | 不请求后端 |

全部 24 个文件都不依赖固定端口（`baseURL` 由句柄注入），不使用 `localStorage` 残留，也没有向真实服务端
写入的路径，因此共享一份 dev server 成立（D3 按默认落地，未出现需要独享的 spec）。

### 4.7 覆盖对账

37 个文件对应 37 条 Case，与 `legacy-cases.manifest.json` 的前端条目逐条对齐：

```text
覆盖对账：范围 R6；清单 .steps/V3/R0/legacy-cases.manifest.json
条目：清单 1707 / 范围 37 / 覆盖 37 / dropped 0 / 缺失 0 / 多余 0
自检用例：3 条不参与对账（RunnerInternal）
```

同一次改动后重跑 R5 范围仍为 328 = 279 + 49、缺失 0、多余 0，两阶段的退出码都是 0。

---

## 5. 阶段内冻结的决策

| 编号 | 决策 | 结论 |
|---|---|---|
| D1 | Case 粒度 | 一个文件一条 Case；文件内串行 |
| D2 | 调用方式 | CLI 子进程，一 Case 一次调用；批量模式按 §4.2 的两条理由不采用 |
| D3 | dev server 归属 | 默认共享；§4.6 审计未发现不可验证写入，未出现独享 spec |
| D4 | 单次调用并发 | `--workers=1`（Vitest `--maxWorkers=1`），文件间由调度器决定 |
| D5 | 冷启动可接受阈值 | 以 §4.1 基线为准：适配层全量 4m09–4m48 对基线 4m03，未超过「慢一倍」红线，接受 |
| D6 | `test-resource-policy.ts` | 整体删除；两个配置不再读取 worker 环境变量，`AGENTFLOW_TEST_*_WORKERS` 一并失效 |
| D7 | 产物路径 | 统一到 `cases/<case>/artifacts/`，`artifacts.ts` 优先使用注入目录，独立运行才生成随机目录 |
| D8 | 会话类资源的进程登记与契约 | `browser` / `node-worker` 只分配会话目录；真实 CLI 进程由用例启动后登记 pid。`node-worker` 的 payload 因此从「必填 `pid` + `runtime`」调整为「必填 `runtime` + `session_dir` + `version`，`pid` 可选」；`browser` / `node-worker` 的 fingerprint 取运行时版本，`frontend-dev-server` 取 `package.json` 的 SHA-256。这是对 R2 冻结契约的最小修订，需回写 R2 §7 |
| D9 | 建议运行并发 | 前端组建议 `--parallel 4`：`browser` 上限 4 是真正的资源边界，`--parallel 8` 会让 4 个浏览器与 4 个 Vitest 争 CPU，实测出现 5s `expect` 与 30s 用例超时（§7.1）。该建议只影响调用参数，不改调度器语义 |

---

## 6. 运行授权与执行范围（已授权）

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `frontend` 组 37 条；`resourcecheck` 组（回归确认前端提供者未破坏本机资源自检） |
| 允许的命令 | 适配层内部的 `pnpm exec playwright test` / `pnpm exec vitest run`；基线测量（§4.1 三组，各含一次全量与一次单文件）；旧入口对照（`pnpm exec playwright test --workers=4`，dev server 手动预启动） |
| 明确不允许 | Go 的 `tests/**`（除 `resourcecheck` 外未运行）；真实 Pi / SSH / Docker；`pnpm update` / 依赖升级 |
| 实际执行 | §4.1 基线 5 次；适配层全量 7 次（`--parallel 8` 四次、`--parallel 4` 三次）；单 spec 隔离复跑 6 次；旧入口对照 3 次；`pnpm test` 1 次 |

---

## 7. 验收条件

1. 37 条 Case 全部注册并与 manifest 对齐；`--audit-coverage` 对该范围退出码 0。**已满足**（§4.7）。
2. 一次运行只启动一个共享 dev server；释放后端口关闭、进程消失。**已满足**：每次全量运行的 24 条
   Playwright 用例拿到同一个 `base_url`（端口随机，例如 `http://127.0.0.1:51373`），`scope` 为
   `run-shared`；运行结束后终检 `passed`，dev server 提供者自证进程消失、端口关闭、HTTP 不可用。
3. 每条 Case 有独立产物目录；失败时保留 trace、截图与报告。**已满足**（§4.5；失败现场的
   `test-failed-1.png`、`error-context.md`、`trace.zip` 与 `playwright.json` 都留在该 Case 的 `artifacts/`）。
4. 单次调用固定 `--workers=1`；不存在第二套并发决策。**已满足**：CLI 参数写死，配置里 `workers: 1` /
   `maxWorkers: 1`，策略文件已删除。
5. 冷启动数据与相对现状的差异写入本文，阈值经确认。**已满足**（§4.1、D5）。
6. 写策略审计表填满；不可验证写入的 spec 已改独享并记录成本。**已满足**（§4.6，无需独享）。
7. 与迁移前的 `pnpm test` 结果对齐：通过/跳过逐条一致，差异逐项解释。**已满足**：Vitest 13 个文件
   361 / 361 通过（迁移后 `pnpm test` 复跑同为 361 / 361），Playwright 24 个文件 297 / 297 通过，
   跳过 0；没有断言语义差异。
8. 无遗留浏览器、Vitest fork 或 dev server 进程；终检 `passed`。**已满足**：全部 7 次全量运行终检
   都是 `passed`（全量快照比对通过），手动启动的对照 dev server 也已终止。

### 7.1 运行记录

| 运行 | 结论 | wall time | 终检 |
|---|---|---|---|
| 适配层 `--parallel 8` 第一次 | 37 / 37 通过 | 4m48s | passed |
| 适配层 `--parallel 8` 第二次 | 37 / 37 通过 | 4m11s | passed |
| 适配层 `--parallel 8` 第三次 | 37 / 37 通过 | 4m15s | passed |
| 适配层 `--parallel 8` 第四次 | 36 / 37（`task-execution` 的 `单步骤执行使用当前选中步骤的真实标识` 30s 超时） | 4m36s | passed |
| 适配层 `--parallel 4` 第一次 | 37 / 37 通过 | 4m21s | passed |
| 适配层 `--parallel 4` 第二次 | 37 / 37 通过 | 4m09s | passed |
| 适配层 `--parallel 4` 第三次 | 35 / 37（`task-launch`、`tasks` 各 1 条 5s `expect` 超时） | 4m24s | passed |
| 旧入口对照（预启动 dev server + `playwright test --workers=4`） | 294 / 297、297 / 297、297 / 297 | 4.3m / 4.1m / 3.8m | — |
| `pnpm test`（Vitest 全量） | 361 / 361 通过 | 4.1s | — |

报告目录：`--parallel 8` 为 `.test-artifacts/20261120t062327z-3f84`、`…t062833z-faf9`、
`…t063247z-c45e`、`…t063854z-5635`；`--parallel 4` 为 `…t064547z-4d83`、`…t065011z-9f62`、
`…t065426z-4728`。

失败归因：

1. 7 次适配层运行中 2 次出现 5s `expect` 或 30s 用例超时，失败项互不相同，且都在并行下发生；
   把失败用例单独复跑（`tasks`、`task-launch` 各 3 次）全部通过。
2. 旧入口在同一台机器上以 `--workers=4` 连续三次运行时，第一次同样失败 3 条（`settings-environments`、
   `task-closeout` 等），后两次全通过。因此这是前端用例在并发下既有的时序抖动，不是迁移差异：
   两套入口的失败形态一致（都是 UI 断言等不到预期状态），迁移前后都出现过。
3. `--parallel 8` 与 `--parallel 4` 的稳态耗时相近（前者受 `browser` 上限 4 约束），但 8 会同时跑
   4 个浏览器与 4 个 Vitest，CPU 争用更重；因此 D9 建议前端组固定在 `--parallel 4`。

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 每文件一次冷启动导致整体变慢 | §4.1 先测基线；实测与基线持平，未启用批处理；若后续文件数增长到明显变慢，再评估批量调用与粒度 |
| 共享 dev server 的状态跨用例污染 | §4.6 写策略审计；全部 spec 只读且隔离在浏览器侧，未出现需要独享的用例 |
| 并发下 UI 时序抖动（既有） | 前端组按 D9 使用 `--parallel 4`；不新增重试、不放宽断言。若必须消除抖动，需要单独决策（提高 `expect` 超时或加 retries），本阶段未改 |
| Playwright 的 HTML 报告与 JSON 结果口径不一致 | 以 JSON 结论为准，退出码冲突一律判失败 |
| 固定端口 26390 冲突 | dev server 端口由提供者分配并注入，`strictPort` 保留；端口占用表现为分配失败 |
| 浏览器 / Vitest 进程残留 | CLI 以独立进程组启动，pid 登记到会话目录；释放按进程树回收，终检自证进程消失 |
| 依赖安装被当成测试步骤 | 只用 `pnpm exec`，缺依赖时以「依赖未安装」跳过并写清原因，不自动安装 |
| 旧入口被误用 | `playwright.config.ts` 已删除 `webServer`，独立运行需要自己提供 dev server；CI 的切换在 R8 |

---

## 9. 不做的事

- 不改前端产品代码与 spec 断言语义。
- 不迁移 Go 用例（R5 已完成）。
- 不删除 `tests/internal/testkit`（R7）。
- 不修改 CI 工作流、`Makefile` 与文档（R8 / R9）。
- 不引入第二套前端测试框架或替代 Playwright / Vitest。

---

## 10. 输出给 R7 / R8

1. 前端 37 条 Case 的覆盖结论与产物布局：`frontend/playwright-<name>`、`frontend/vitest-<name>`，
   产物在 `cases/frontend__<runtime>-<name>/artifacts/`。
2. 共享 dev server、browser、node-worker 的容量与残留检查方式：`frontend-dev-server` 上限 1（run-shared，
   自证进程 + 端口 + HTTP 三重消失）、`browser` 上限 4、`node-worker` 上限 4（均为 case-exclusive，
   以会话目录登记的 pid 做残留自证）。
3. 冷启动基线、最终调用方式与阈值结论：见 §4.1、D2、D5。
4. `run-frontend-tests.sh` 的替代入口形态：`agentflow-test --parallel 4 frontend`（旧脚本在 R8 删除时
   用它替换）；CI 若要保留分段结论，可用 `agentflow-test frontend/playwright-…` 逐条选择。
5. 仍需处理的事项：①`node-worker` 契约调整需要回写 R2 §7；②R2 §7 的 `resourcecheck-fault` 记录里
   `unimplemented` 用例已随提供者补齐而删除；③`lingting-agent-flow-ui/README.md` 仍引用已删除的
   `tests/internal/test-resource-policy.ts`，`test:e2e:safe` / `test:all:safe` 不能独立运行；
   ④`tests/README.md` 的 `ui-test` 行与 `.github/workflows/ci.yml` 的 `pnpm test:all:safe` 需在 R8 切换。
