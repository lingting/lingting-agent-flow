# V3 R6：前端适配器

**状态：** 待执行。
**上游文档：** [路线总规划](../ROADMAP.md)、[R5 迁移](../R5/README.md)、[R2 资源契约](../R2/README.md)。
**范围：** 把 13 个 Vitest 文件与 24 个 Playwright spec 接入统一执行器，一个文件一条 Case。
不改前端产品代码与断言语义，不动 Go 用例（R5）、不改 `Makefile` / CI / `docs/`。

---

## 1. 目标

1. 前端测试不再由 `playwright test` / `vitest` 的并发策略决定并发，改由统一调度器决定。
2. Vite dev server 从 Playwright 的 `webServer` 变成 `frontend-dev-server` 共享资源。
3. 37 个文件各有独立产物目录、独立结论，并进入统一报告。
4. 先测冷启动开销，再决定批量迁移；跑得比现在慢一倍以上的方案不接受。

---

## 2. 输入与事实

- 24 个 spec 全部使用受控后端（`page.route` 或 `tests/e2e` 下的 helper），不依赖 Go 服务、Docker、
  SSH 或 Pi（R0 §8）。
- `playwright.config.ts`：`webServer.command = "vp dev"`，`baseURL = http://127.0.0.1:26390`，
  `fullyParallel: false`，`workers` 来自 `tests/internal/test-resource-policy.ts`，产物写入随机
  `TEST_ARTIFACT_DIR`。
- `vite.config.ts`：`server.port = 26390`（`strictPort: true`），`test.include = ["tests/**/*.test.ts"]`，
  Vitest worker 上限来自同一策略文件（默认 4，上限 8）。
- `tests/internal/artifacts.ts` 定义随机产物目录；接管后不得出现两套产物路径。
- R0 判定两者 `NeedsReview` 都是「共享 dev server 的写操作策略」，不是发现了真实写入。
- R2 已冻结 `frontend-dev-server`、`browser`、`node-worker` 的 payload 契约，提供者由本阶段实现。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `tests/runner/frontend` | 37 条 Case 的注册与结果映射（非 `_test.go`） |
| `internal/testrunner` 或 `tests/runner/frontend` 内的适配层 | 子进程调用、退出码/JSON 解析、产物归档、失败原因透传 |
| `lingting-agent-flow-ui/playwright.config.ts`、`vite.config.ts` | 去掉自动 `webServer`、并发与端口改由句柄注入 |
| `tests/internal/test-resource-policy.ts` | 并发决策去留（见 §5 D6） |
| 本文 §6 / §7 | 冷启动基数、写策略审计、结论 |

---

## 4. 实施步骤

### 4.1 先测冷启动（不改代码）
在现有配置下测量三组数据，作为方案取舍依据：
- 24 个 spec 一次全量运行的 wall time；
- 单个 spec 独立运行（冷启动）的 wall time；
- 13 个 Vitest 文件全量与单文件运行的 wall time。

结论只用于判断「一文件一调用」的相对开销，不作为通过/失败证据。

### 4.2 冻结调用方式
| 方案 | 做法 | 优点 | 风险 |
|---|---|---|---|
| CLI 子进程 | `pnpm exec playwright test <file> --workers=1 --reporter=json` | 与现有配置差异最小，隔离好 | 每文件一次 Node + 浏览器冷启动 |
| 程序化调用 | Vitest `startVitest` / Playwright 受限入口 | 启动少 | Playwright 程序化入口有限，产物收集方式不同 |

建议：两者都用 CLI 子进程，一个 Case 一次调用；Vitest 若实测冷启动占比过高，可评估一次调用多文件、
按 JSON 结果映射到多条 Case（Batch Case 模式），但必须在本文记录并保证调度粒度可解释。

### 4.3 实现资源提供者
- `frontend-dev-server`（run-shared）：启动 `vp dev`，分配端口并等待 HTTP 就绪；`base_url` 与 `pid`
  写入句柄；释放后自证端口关闭、进程消失。
- `browser`（case-exclusive）：Playwright 自带的浏览器进程由单次调用管理，提供者只负责产物目录与
  进程残留自证；不自行实现浏览器驱动。
- `node-worker`（case-exclusive）：Vitest fork 进程的生命周期登记与残留自证。

### 4.4 改造配置注入
- `playwright.config.ts`：删除 `webServer`；`baseURL` 改为从注入的环境参数读取，缺省值保留本地可用。
- `vite.config.ts`：端口从注入参数读取，`strictPort` 保留；不得在测试运行时抢占固定端口。
- 并发：单次调用固定 `--workers=1`；文件间的并发只由调度器控制。

### 4.5 结果映射与产物归档
- 每条 Case 的产物落在 `cases/<group>__<name>/artifacts/`：Playwright 的 HTML 报告、trace、截图与
  Vitest 的 JSON 结果。
- 调用退出码与 JSON 结论冲突时按失败处理，并写清冲突点；不得只取退出码。
- 跳过（`test.skip` / 条件跳过）必须带原因进入报告。

### 4.6 写策略审计
逐 spec 记录：是否向服务端写入、是否依赖固定端口、是否依赖 localStorage 残留。
- 只读或浏览器侧隔离的 spec 可共享 dev server；
- 有不可验证服务端写入的 spec 改为独享 dev server，并记录额外资源成本。

### 4.7 覆盖对账
37 个文件对应 37 条 Case，与 `legacy-cases.manifest.json` 的前端条目逐条对齐；缺失即失败。

---

## 5. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | Case 粒度 | 一个文件一条 Case；文件内串行 |
| D2 | 调用方式 | CLI 子进程，一 Case 一次调用 |
| D3 | dev server 归属 | 默认共享；审计发现不可验证写入即改独享 |
| D4 | 单次调用并发 | `--workers=1`，文件间由调度器决定 |
| D5 | 冷启动可接受阈值 | 由用户确认；超出即调整调用方式，不硬迁移 |
| D6 | `test-resource-policy.ts` | 迁移完成后删除自动并发决策，只保留必要校验或整体删除 |
| D7 | 产物路径 | 统一到 `cases/<case>/artifacts/`，不保留两套随机目录 |

---

## 6. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | 新增 `frontend` 组（37 条）、`frontend-fault`（故意失败的自检） |
| 允许的命令 | 适配层内部的 `pnpm exec playwright test` / `pnpm exec vitest run`；`pnpm install --frozen-lockfile`（仅在依赖缺失时） |
| 允许的基线测量 | §4.1 的三组测量，各自最多一次全量与一次单文件 |
| 明确不允许 | Go 的 `tests/**`；真实 Pi / SSH / Docker；`pnpm update` / 依赖升级 |
| 理由 | 前端批次必须能独立验收；基线测量是方案取舍的前置条件，单独计入授权 |

---

## 7. 验收条件

1. 37 条 Case 全部注册并与 manifest 对齐；`--audit-coverage` 对该范围退出码 0。
2. 一次运行只启动一个共享 dev server；释放后端口关闭、进程消失。
3. 每条 Case 有独立产物目录；失败时保留 trace、截图与报告。
4. 单次调用固定 `--workers=1`；不存在第二套并发决策。
5. 冷启动数据与相对现状的差异写入本文，阈值经用户确认。
6. 写策略审计表填满；不可验证写入的 spec 已改独享并记录成本。
7. 与迁移前的 `pnpm test` 结果对齐：通过/跳过逐条一致，差异逐项解释。
8. 无遗留浏览器、Vitest fork 或 dev server 进程；终检 `passed`。

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 每文件一次冷启动导致整体变慢 | 先测基线；必要时改批量调用 + JSON 映射，并记录粒度 |
| 共享 dev server 的状态跨用例污染 | 写策略审计；不可验证写入改独享 |
| Playwright 的 HTML 报告与 JSON 结果口径不一致 | 以 JSON 结论为准，退出码冲突一律判失败 |
| 固定端口 26390 冲突 | 端口改由 `local-port` / `frontend-dev-server` 句柄分配 |
| 浏览器进程残留 | 提供者释放后自证；残留判 `error`，不静默清理 |
| 依赖安装被当成测试步骤 | `pnpm install` 只用于补齐依赖，不触发测试 |

---

## 9. 不做的事

- 不改前端产品代码与 spec 断言语义。
- 不迁移 Go 用例（R5 已完成）。
- 不删除 `tests/internal/testkit`（R7）。
- 不修改 CI 工作流、`Makefile` 与文档（R8 / R9）。
- 不引入第二套前端测试框架或替代 Playwright / Vitest。

---

## 10. 输出给 R7 / R8

1. 前端 37 条 Case 的覆盖结论与产物布局。
2. 共享 dev server、browser、node-worker 的容量与残留检查方式。
3. 冷启动基线、最终调用方式与阈值结论。
4. `run-frontend-tests.sh` 的替代入口形态（R8 删除该脚本时使用）。
