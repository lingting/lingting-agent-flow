# P4 工程质量：前端测试并发治理（不再引入 CPU 配额）

- **任务类型**：测试基础设施治理（不对应 P4-M3 的任何 UI 功能缺口）
- **状态**：`completed`（2026-10-27）
- **实施前 HEAD SHA**：`9055cba3bb0fa85a67b95c31e5ca86ba191b96c7`（`git fetch` 后
  `HEAD` 与 `origin/master` 一致，与交接时记录的参考值相同，实测确认未变化）
- **实施后 HEAD SHA**：工作区改动尚未提交（见 §10 交接事项）

---

## 1. 问题与结论

### 1.1 原始问题

前端测试期间 CPU 长时间满载：`playwright.config.ts` 里的 `fullyParallel: true` 允许用例级并行，
Vitest 又没有限制 worker 数（默认取逻辑 CPU 数，本机 18 个逻辑 CPU 就会起 18 个 fork）。

### 1.2 核实后的真实资源模型

先按官方实现核实，再决定方案，没有照搬「限 CPU 用量」的直觉方案：

1. **dev server 只有一份。** Playwright 的 `webServer` 是运行级插件：
   `webServerPluginsForConfig(config)` 按 `config.webServers` 逐个创建 `WebServerPlugin`，
   本项目配置里只有一条 `webServer`，因此一次运行只启动一个 `vp dev` 进程，全部 worker
   共享同一个 `baseURL`。这是 Playwright 的原生行为，不需要额外机制。
   实测证据：全量运行打印 `Running 113 tests using 4 workers`，而 `[WebServer]` 日志只出现一段。
2. **CPU 开销 = 无头浏览器实例数 = worker 数。** 每个 worker 内启动一个浏览器实例，
   页面与上下文在 worker 内串行。
3. 因此「只起一个 Vite + 多个无头浏览器 + 限制 worker」就是正确答案，**限制 worker 即限制
   CPU 峰值**，不需要进程级 CPU 配额。

### 1.3 与最初设想方案的差异（重要）

最初按「必须提供可核实的 CPU 硬配额」实施了 Windows Job Object 方案
（`scripts/test-cpu-limiter.ps1` + PowerShell 编排 + 中断托管 + 忙碌负载配额实测，
实测数据：同一负载下不限额 3.94 核 / 2 核配额 2.36 核 / 1 核配额 1.11 核，配额确实生效）。
该方案机制有效但**过度复杂**：Windows 专属、需要 PowerShell 互操作、需要自己托管父进程
消失后的清理，维护成本远高于收益。

经确认后**全部删除**，收敛为「限制 worker」这一条原生做法。本文件记录的是最终交付形态；
被删除的方案留在本条记录里，避免后来者重复走这条路。

---

## 2. 实际修改的文件

| 文件 | 状态 | 理由 |
|---|---|---|
| `lingting-agent-flow-ui/playwright.config.ts` | 修改 | `fullyParallel: false`、显式 `workers`、并发值来自统一策略；保留截图 / trace / HTML 报告 / 单 chromium 项目 / 单 dev server |
| `lingting-agent-flow-ui/vite.config.ts` | 修改 | `test.maxWorkers` 来自统一策略；不设置 `minWorkers` / `fileParallelism`，保持 `isolate` 默认开启 |
| `lingting-agent-flow-ui/tests/internal/test-resource-policy.ts` | 新增 | 并发默认值、上限、环境变量解析与校验的唯一定义处（纯常量 + 纯函数，无 IO） |
| `lingting-agent-flow-ui/tests/unit/test-resource-policy.test.ts` | 新增 | 覆盖默认值、非法输入拒绝、配置确实接入、脚本入口存在 |
| `lingting-agent-flow-ui/package.json` | 修改 | 新增 `check`、`test:unit:safe`、`test:e2e:safe`、`test:safe`、`test:all:safe`；原有脚本语义不变 |
| `lingting-agent-flow-ui/README.md` | 修改 | 新增「测试并发与资源」章节：默认值、覆盖方式、资源受限入口、诊断命令、产物目录 |
| `.steps/README.md` | 修改 | 记录本次测试基础设施治理任务与状态 |
| `.steps/ROADMAP.md` | 修改 | 列为 P4 测试基础设施保障项，并说明与 M3-1 无关 |
| `.steps/P4-product-experience-optimization.md` | 修改 | 说明该项与 P4 的关系及后续推进顺序 |
| `.steps/P4-frontend-test-resource-governance.md` | 新增 | 本交付文档 |

**未修改**：任何前端业务代码（`src/`）、任何 Go 代码（`internal/`、`cmd/`）、测试用例的断言与
覆盖范围、`tests/internal/artifacts.ts` 的产物目录约定。

---

## 3. 并发配置的实际取值

| 项目 | 默认值 | 上限 | 覆盖方式 |
|---|---|---|---|
| Playwright `workers` | 4 | 8 | `AGENTFLOW_TEST_PLAYWRIGHT_WORKERS` / `--workers=N` |
| Vitest `maxWorkers` | 4 | 8 | `AGENTFLOW_TEST_VITEST_WORKERS` / `--maxWorkers N` |
| 用例级完全并行 | `false` | 固定 | 不提供覆盖：与「一个 worker 一个浏览器」的资源模型冲突 |
| `retries` | `0` | 固定 | 不提供覆盖：资源治理不引入自动重跑 |

校验规则（`tests/internal/test-resource-policy.ts`）：只接受 1–8 的十进制整数；
`0`、负数、小数、百分比、`abc`、`9` 以上一律抛错中断，不静默回退到默认值或更高并发。

**为什么默认 4**：本机 18 个逻辑 CPU，4 worker 即 4 个无头 Chromium + 1 个 Vite，
最高约 5 个进程组并发，仍有充足余量；同时把全量 E2E 从单 worker 的 4.1 分钟压到 1.3 分钟。
这是**并发控制**，不是 CPU 百分比上限，不得混称。

**为什么关闭 `fullyParallel`**：它在同一 worker 内再并发起多个页面/上下文，与
「一个 worker 一个浏览器」的模型冲突，会让 worker 数不再等价于浏览器实例数。

---

## 4. CPU 配额能力边界（如实说明）

**当前平台没有启用任何进程级 CPU 配额，该项属于明确的非目标。**

- 不提供 cgroup / Job Object / `nice` 等机制。
- 降低 worker 数与顺序执行属于**软限制**，**不是** CPU 百分比上限。
- 若将来确实需要硬配额，Windows 侧已验证过可行路径（Job Object
  `JOBOBJECT_CPU_RATE_CONTROL_INFORMATION` 的 `ENABLE|HARD_CAP` + `KILL_ON_JOB_CLOSE`），
  但本次判断其维护成本高于收益，未保留。重新启用前必须先证明「限制 worker 不足以解决问题」。

---

## 5. 验收命令与实际结果

全部在 `lingting-agent-flow-ui/` 下执行。

| # | 命令 | 结果 | 备注 |
|---|---|---|---|
| 1 | `pnpm test` | 通过：18 文件 / 296 用例 | 覆盖数从 282 增至 296（新增 14），未减少 |
| 2 | `pnpm test tests/unit/api-client.test.ts` | 通过 | 单文件运行正常 |
| 3 | `pnpm test -t "Playwright 与 Vitest 默认并发都是 4"` | 通过：1 个用例匹配 | 单用例筛选正常 |
| 4 | `pnpm test:unit:safe` | 通过：18 文件 / 296 用例 | 1 worker，不启动浏览器 |
| 5 | `pnpm exec playwright test tests/e2e/layout.spec.ts` | 11 通过 / 2 失败 | 单文件、1 worker；2 个失败为既有问题 |
| 6 | `pnpm test:e2e:safe tests/e2e/api-connection.spec.ts tests/e2e/layout.spec.ts` | 16 通过 / 2 失败 | `Running 18 tests using 1 worker`；失败同上 |
| 7 | `pnpm exec playwright test`（全量） | **110 通过 / 3 失败** | `Running 113 tests using 4 workers`，耗时 **1.3 分钟**；重复运行结果一致，未出现随机漂移 |
| 8 | `pnpm check`（`vp fmt && vp lint --fix && tsc --noEmit`） | 通过 | 格式、lint、类型检查均无问题 |
| 9 | `pnpm build` | 通过 | 产物写入 `internal/webui/dist` |

### 5.1 关于 3 个既有失败

3 个失败项固定为：

1. `layout.spec.ts:43` `/machines 可访问且导航高亮、页面标题一致`
2. `layout.spec.ts:64` `刷新后路由与导航状态保持`
3. `tasks.spec.ts:478` `详情返回 404 时展示错误而不是伪造任务`

已用 `git stash -u` 在**未修改任何文件**的基线上逐个复现（`layout` 两项一起跑、`tasks` 一项
单独跑），失败现象与断言错误完全一致，确认为本次改动之前就存在的问题，**不是本次治理引入**。
本次未修复它们：任务范围限定为测试基础设施，且修复涉及业务逻辑判断。

### 5.2 并发与资源实测

| 观测项 | 结果 |
|---|---|
| 全量 E2E 使用 worker 数 | 4（`Running 113 tests using 4 workers`） |
| 全量 E2E 中 `vp dev` 启动次数 | 1 次（`[WebServer]` 日志只有一段），4 worker 共享 |
| 单 spec 文件 worker 数 | 1（`Running 13 tests using 1 worker`，`fullyParallel: false` 下按文件并行） |
| 全量 E2E 耗时 | 1.3 分钟（单 worker 时为 4.1 分钟） |
| 单元测试 worker 数 | 4（默认）/ 1（`test:unit:safe`） |

**没有做 CPU 峰值或占用的定量测量。** 上表只有「worker 数」「dev server 次数」「耗时」三项
可复现观测，不包含 CPU 占用百分比结论。

---

## 6. 测试产物

- 目录约定未变：`TEST_ARTIFACT_DIR=<OS 临时目录>/lingting-agent-flow-ui-tests/run-<随机>`，
  每次运行独立，测试结束保留，不自动清理。
- 由 `tests/internal/artifacts.ts` 的 `mkdtempSync` 生成，两个测试入口都会打印该变量。
- `playwright-output/` 保存失败截图与 trace（`only-on-failure` / `retain-on-failure`），
  `playwright-report/` 保存 HTML 报告；三者均未被本次改动削弱。
- 本次实际产物目录示例：
  `C:\Users\lingting\AppData\Local\Temp\lingting-agent-flow-ui-tests\run-BTrAtL`（全量 E2E）、
  `...\run-yvikK0`（`tasks.spec.ts` 404 用例）。

---

## 7. 未执行 / 跳过 / 无法确认

| 项目 | 原因 |
|---|---|
| `pnpm test:safe` 完整串联 | 其 E2E 阶段必然带回那 3 个既有失败，`&&` 会在此中断。已分别验证各阶段（表中 1/4/5/8/9），串联逻辑本身由单元测试断言 |
| `pnpm test:all:safe` 完整串联 | 同上 |
| CPU 占用定量测量 | 未做，也没有据此下结论；只报告 worker 数与耗时 |
| 3 个既有 E2E 失败的修复 | 超出本任务范围，涉及业务逻辑判断，需单独立项 |
| Windows 之外的平台验证 | 本次改动不含平台相关机制，故无平台分支需要验证 |

---

## 8. 测试覆盖变化的核对

- 改动前全量单元测试：282 通过。
- 改动后全量单元测试：**296 通过**（新增 `tests/unit/test-resource-policy.test.ts` 14 个用例）。
- E2E 用例数：113（未增未减，覆盖范围不变）。
- 没有删除任何测试文件、跳过任何用例、降低任何断言。

---

## 9. 已知局限与剩余风险

1. **`fullyParallel: false` 下 worker 数等于并行文件数**：单个 spec 文件只能用一个 worker。
   若将来出现超大的单个 spec 文件，需要拆分文件而不是提高 worker 数。
2. **并发提高会放大既有失败的不稳定性**：3 个既有失败在 4 worker 下仍稳定复现（未出现
   随机漂移），但未在高并发下做重复运行统计。
3. **没有 CPU 硬上限**：极端情况下（开发者本机同时跑别的重负载）测试仍可能把 CPU 占满，
   此时只能手动降到 `test:e2e:safe`。这是明确的、已记录的边界。
4. **`AGENTFLOW_TEST_*` 环境变量是软接口**：只被两个配置文件读取，任何绕过配置文件
   （例如直接 `playwright test --config` 指向别的配置）的调用不受约束。

---

## 10. 交接事项

- 工作区改动尚未提交，**实施后 HEAD SHA 仍为 `9055cba`**；提交后需在本文档补记新 SHA。
- 3 个既有 E2E 失败需要单独立项定位（`layout` 两项的导航高亮 / 标题断言，
  `tasks.spec.ts:478` 的 404 错误态可见性）。
- 下一步建议推进 **P4-M3-1 的 Attempt / Session 历史字段补齐**：尝试的 `timed_out` /
  `cancelled` / `error_class` / `failure_stage`，以及会话归档 / 导出状态的中文标签。
  本次治理与该项无依赖关系，不构成其前置条件。
