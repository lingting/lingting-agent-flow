# V3 R5：集成与 E2E 用例迁移

**状态：** 执行中。§4.1 `--audit-coverage` 已落地；批次 B1 已收口：`e2e/cli` 64 条、`local/source` 6 条、
`local/validation` 11 条、`local/codesaver` 12 条、`local/ssh` 22 条全部迁移，覆盖 156 / 328、dropped 3，
运行与对照结果见 §5.2。B2/B3 未开始。
**上游文档：** [路线总规划](../ROADMAP.md)、[R4.5 试点](../R4.5/README.md)。
**范围：** 迁移 `tests/integration/**` 与 `tests/e2e/cli` 的 328 个用例（R4.5 已覆盖其中的 41 个），
并让覆盖对账可执行。不迁移 `tests/unit`（路径 C）、不做前端（R6）、不改 `Makefile` / CI / `docs/`。

---

## 1. 目标

1. 把 18 个包的 328 个用例迁到 Case 模型，迁移后不得同时保留 `go test` 与统一执行器两套入口。
2. 落实 R2 的共享/独享结论，修正试点发现的偏差。
3. 落地 `--audit-coverage`：以 `legacy-cases.manifest.json` 为分母，逐条比对注册表。
4. 真实依赖批次在授权后运行，未授权时如实跳过并统计，不得伪造通过。

---

## 2. 输入与事实

- 迁移面：`tests/e2e/cli` 65 个 + `tests/integration/**` 263 个 = 328 个；包数 18（R0 §4.1）。
- 其中 7 个包带真实依赖门禁（`pi` / `ssh` / `docker`）：`integration/pi`、`integration/ssh`、
  `integration/remote/{agent,codesaver,docker,planning,workflow}`。
- 不可逆线索集中在 `local/{environment,execenv,session,workflow}` 与 `remote/{agent,planning,workflow}`。
- `integration/remote/workflow` 必须整包串行（R0 §6）；6 个文件含 `sync.Once`（R0 §7）。
- 真实依赖判定只由编排进程做一次（R1 §6）；worker 只读取下发的 `real-tests-grant`。
- R4.5 冻结了迁移写法、辅助函数复用方案与用例包位置。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `tests/runner/integration/**`、`tests/runner/e2e/cli` | 迁移后的 Case（非 `_test.go`） |
| `tests/runner/registry` | 按包追加导入，聚合点仍是唯一入口 |
| `cmd/agentflow-test` | `--audit-coverage` 子命令与退出码语义 |
| `internal/testrunner` | 按迁移暴露的问题补齐能力，不改已冻结语义 |
| 删除同批次旧文件 | 对应 `_test.go` 与 `TestMain` 在同批次内删除，不留双轨 |
| 本文 §5 / §6 表格 | 批次记录：覆盖数、差异、真实依赖结论 |

---

## 4. 实施步骤

### 4.1 先落地 `--audit-coverage`
- 输入：`legacy-cases.manifest.json` 与当前注册表。
- 输出：缺失条目、多余条目、`dropped` 条目（含理由）与 counts 对账。
- 退出码：全部覆盖或有理由的 `dropped` 为 0；缺失或未解释的多余为 1；用法错误为 2。
- 该命令只读文件与注册表，不执行用例。

### 4.2 批次划分
按「真实依赖 → 不可逆操作 → 纯本机」从低风险到高风险推进；每批独立验收，禁止跨批并行。

| 批次 | 包 | 用例 | 资源特征 | 风险 |
|---|---|---:|---|---|
| P（已完成于 R4.5） | `local/scheduler` | 41 | `sqlite-db` | 低 |
| B1 | `e2e/cli` | 65 | 本地端口、本地 SSH 服务端、SQLite | 低 |
| B1 | `local/codesaver` | 12 | 子进程、Git 工作区 | 低 |
| B1 | `local/source` | 6 | 子进程、Git 工作区 | 低 |
| B1 | `local/ssh` | 22 | 本地 SSH 服务端 | 低 |
| B1 | `local/validation` | 11 | 本地端口、SQLite | 低 |
| B2 | `local/environment` | 7 | SQLite；`RemoveAll(` | 中 |
| B2 | `local/execenv` | 28 | `remote-container`、SSH 会话；`ContainerRemove` | 中 |
| B2 | `local/session` | 7 | 无声明；`RemoveAll(` | 中 |
| B2 | `local/workflow` | 81 | 子进程、Git 工作区、容器、SQLite；`Shutdown(` | 中 |
| B3 | `integration/fetcher` | 14 | `remote-container` 等 | 高 |
| B3 | `integration/pi` | 2 | 真实 Pi | 高 |
| B3 | `integration/ssh` | 6 | 真实 SSH 主机 | 高 |
| B3 | `integration/remote/agent` | 2 | Pi + SSH + Docker | 高 |
| B3 | `integration/remote/codesaver` | 2 | SSH + Docker | 高 |
| B3 | `integration/remote/docker` | 6 | SSH + Docker | 高 |
| B3 | `integration/remote/planning` | 9 | SSH + Docker；`docker rm` | 高 |
| B3 | `integration/remote/workflow` | 7 | Pi + SSH + Docker；整包串行 | 高 |

小计：B1 116、B2 123、B3 48，加 R4.5 的 41 共 328。

### 4.3 每批次的固定流程
1. 迁移：按 R4.5 的写法逐条改写，保留断言语义，不合并、不删减用例。
2. 注册：在 `tests/runner/registry` 追加导入；用例 ID 与包路径对应。
3. 对账：`--audit-coverage` 必须显示该批全部覆盖；缺失立即停。
4. 验证：在授权的批次范围内运行，记录逐条结论；同一批次允许一次 `go test` 对照。
5. 清理：同批次删除旧 `_test.go` 与 `TestMain`；不保留双轨。
6. 回写：把实际资源排布与偏差写回 R2 §7 与本文 §5。

### 4.4 真实依赖批次
- 默认 `--real-tests=auto`：本机无授权标记时一律拒绝，真实用例记为 SKIP 并带原因。
- 真实批次单独授权，且要求 `tests/config/ssh.yml`、`tests/config/pi.yml` 等真实设备配置就绪；
  设备缺失时如实跳过，不得回退到本机，也不得写成通过。
- 运行前后各做一次资源快照；残留判定沿用 R2 的终检。

### 4.5 语义判定
- `t.Parallel()` 一律映射为 Case 的并行属性，不保留强制并行。
- 6 个含 `sync.Once` 的文件逐个判定：能否降级为 Case 内局部状态；不能则整包串行并写明原因。
- 表驱动子测试保留为 Case 内顺序块，子测试名与 manifest 对账。
- 依赖包级固定状态的用例，改为按 Case 初始化；不得依赖同包其他用例留下的状态。

### 4.6 覆盖对账口径
- 分母是 manifest 的 1707 条；本阶段只对 R5 的 328 条负责，unit 与前端分别由路径 C 与 R6 负责。
- 条目消失即失败；确需删除时必须显式标记 `dropped: <理由>`，并在本文登记。
- counts 对账不一致时以 manifest 为准，修正注册表或登记 dropped，不得改口径。

---

## 5. 批次记录（执行时填写）

| 批次 | 覆盖 | 首次通过 | 跳过（原因） | 与 `go test` 差异 | 结论 |
|---|---|---|---|---|---|
| P | 41 / 41 | R4.5 已记录 | 0 | 无差异 | 完成 |
| B1 | 116 / 116（`e2e/cli` 64、source 6、validation 11、codesaver 12、ssh 22） | 64 / 64 与 51 / 51 通过（各自连续三次，删除旧入口后合并复跑） | 0 | 1 条断言语义偏差 + 2 条表驱动子测试计数差异（见 §5.2） | 完成 |
| B2 | 0 / 123 | 待填 | 待填 | 待填 | 未开始 |
| B3 | 0 / 48 | 待填 | 待填 | 待填 | 未开始 |

### 5.1 进度记录

**§4.1 `--audit-coverage`（已完成）**

| 项 | 落地形态 |
|---|---|
| 命令形态 | `agentflow-test --audit-coverage [--audit-stage R5] [--audit-manifest <path>] [--audit-json <path>]` |
| 分母 | R0 冻结清单 `.steps/V3/R0/legacy-cases.manifest.json`（默认路径，可由 `--audit-manifest` 覆盖） |
| 匹配键 | `Case.Package`（来源包路径）+ `Case.LegacyName`（来源测试函数名） |
| `LegacyName` 来源 | 由 `Register` 从用例主体函数名推导（`func run<原函数名去掉 Test 前缀>`），也可显式声明 |
| dropped | `testrunner.DeclareDropped(清单ID, 理由)`，`tests/runner/legacy` 登记 3 条（2 条本机 Pi + 1 条依赖已移除的 `check` 子命令） |
| 输出 | 文本摘要（逐包 `覆盖/范围`、缺失、dropped、多余、问题）+ 可选 JSON（`agentflow-test-coverage/1`） |
| 退出码 | 0 全部覆盖或有理由的 dropped；1 缺失 / 未解释的多余 / 结构性问题；2 用法错误 |
| 当前结论 | 范围 R5 共 328 条：覆盖 156、dropped 3、缺失 169、多余 0，退出码 1（B2/B3 未迁移） |

`dropped` 目前 3 条，都由 commit 9515a72「移除本机pi测试」造成，没有可迁移的等价断言：
`tests/integration/pi` 的 2 条（`TestRealPiCheck`、`TestRealPiModelRecognition`）与
`tests/e2e/cli` 的 `TestAgentCheckUnknownExecutable`（`agent check` 只在 Pi 不可用时才有意义）。
其余 169 条属于 B2/B3，因此对账当前仍非零退出。

**B1 已迁移并通过对照的包**

| 包 | 分组 | 用例 | 资源声明 | 迁移要点 |
|---|---|---:|---|---|
| `e2e/cli` | `e2e-cli` | 64 | 40 条声明 1 份 `sqlite-db`，其余不声明 | 进程内执行 cobra 命令树；配置里的数据库路径只从注入句柄读取、工作区落在用例工作空间内；SSH 探测与主机密钥握手用用例自建的本机临时端口，不声明端口资源 |
| `local/source` | `integration-local-source` | 6 | 无 | 执行替身映射容器命令到用例工作空间；缺 git 时 `Skip` |
| `local/validation` | `integration-local-validation` | 11 | 每条 1 份 `sqlite-db` | 进程内真实 SSH 服务端；临时端口由 OS 分配，不声明端口资源 |
| `local/codesaver` | `integration-local-codesaver` | 12 | 无 | 真实 Git 工作仓库 + bare 仓库；每套仓库用独立子目录 |
| `local/ssh` | `integration-local-ssh` | 22 | 无 | 进程内真实 SSH/SFTP 服务端；`known_hosts` 与私钥落在用例工作空间 |

B1 的 5 个包按 §4.3 删除了旧 `_test.go` 与 `TestMain`，`tests/e2e` 目录随后整体移除，不再保留双轨。

### 5.2 运行与对照记录（授权：B1 范围）

授权按 §7 的 B1 执行：先跑 `e2e/cli` 与 `integration/local/{codesaver,source,ssh,validation}` 的新集合，
每个子范围允许一次 `go test` 对照；未跑 B2/B3、unit 与前端。

| 项 | 结果 |
|---|---|
| 新执行器（source + validation + codesaver） | 连续三次：每次 29 通过 / 0 跳过；峰值 8 / 上限 8；零残留 `passed` |
| `go test` 对照（同 3 包） | 31 通过；与 29 条 Case 的差异 2 条，全部来自 `validate-fails-when-api-incompatible` 的表驱动子测试（D6：按 Case 登记，仍是 1 条 Case 内的 2 个顺序块） |
| 新执行器（ssh） | 22 通过 / 0 跳过；零残留 `not-applicable`（本包不分配资源） |
| `go test` 对照（ssh） | 22 通过，逐条一致，无差异 |
| 新执行器（`e2e/cli`，旧入口仍在） | 64 通过 / 0 跳过；峰值 8 / 上限 8；零残留 `passed` |
| `go test` 对照（同范围） | 63 通过 + 1 失败：`TestAgentHelp` 要求 `agent --help` 含 `check`，而该子命令已由 commit e36ecff 移除，源用例在迁移前就已失效；按确认迁移时只保留 `run` 断言，差异登记于本节 |
| 删除旧入口后的合并复跑（`e2e/cli` 64 条） | 连续三次：每次 64 通过 / 0 跳过；峰值 8 / 上限 8；零残留 `passed` |
| 删除旧入口后的合并复跑（4 包 51 条） | 51 通过 / 0 跳过；零残留 `passed` |
| 静态检查 | `go vet ./...` 与 `gofmt -l cmd internal tests` 无新增问题 |

结论：B1 的 5 个包（116 条）与 `go test` 一致，删除旧入口后仍全部通过，B1 就此收口。唯一断言语义差异是
`TestAgentHelp` 里依赖已移除 `check` 子命令的那一项，已按确认改为只断言 `run`；`validate-fails-when-api-incompatible`
的 2 条表驱动子测试差异沿用 D6（按 Case 登记，子测试名写入日志）。
---

## 6. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | 用例 ID 与包路径的映射 | `<layer>/<pkg>/<func>`，选择器支持分组前缀 |
| D2 | 旧文件的删除时机 | 同批次内删除，禁止双轨并存 |
| D3 | 真实依赖默认值 | `auto`，无授权标记即拒绝 |
| D4 | `--audit-coverage` 输出 | 文本摘要 + JSON，退出码语义见 §4.1 |
| D5 | dropped 的审批 | 只在用例确实不适用时使用，理由写入本文并核对 |
| D6 | 子测试对账粒度 | 有字符串字面量名的子测试逐条登记；表驱动按 Case 登记 |

---

## 7. 运行授权申请

按批次申请，一次只覆盖一个批次：

| 批次 | 允许执行的集合 | 允许的一次性对照 | 明确不允许 |
|---|---|---|---|
| B1 | `e2e/cli`、`integration/local/{codesaver,source,ssh,validation}` | 同范围的 `go test` 一次 | B2/B3 与 unit |
| B2 | B2 的 4 个包 | 同范围的 `go test` 一次 | B1/B3 与 unit |
| B3 | B3 的 8 个包（真实设备） | 同范围的 `go test` 一次 | B1/B2 与 unit；未配置设备时不得强行运行 |

真实设备批次还需要：`tests/config/ssh.yml`、`tests/config/pi.yml`、远程 Docker 均可用，且用户明确
同意占用这些设备。未获授权时 B3 只允许迁移代码与静态检查。

---

## 8. 验收条件

1. 328 条用例全部迁移并注册；`--audit-coverage` 对该范围退出码 0。
2. 同批次内旧 `_test.go` 与 `TestMain` 已删除，不存在两套入口。
3. 每批次与 `go test` 对照的结果一致；差异逐项解释并处置。
4. 每批次连续三次运行结论一致；真实批次单独记录设备与时间。
5. 共享/独享排布与 R2 结论一致；偏差已回写并有依据。
6. 真实依赖未被授权时如实记为 SKIP，不计入通过；不出现本机回退。
7. 终检 `passed`；无遗留进程、端口、容器、远程目录与会话。
8. `go vet ./...` 与 `gofmt -l` 无新增问题（除授权运行外不运行测试）。

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| 真实设备不稳定导致批次反复 | 真实批次独立授权、独立记录；失败不连累本机批次 |
| 迁移改变断言语义 | 逐条对照 `go test`；禁止「顺手优化」断言 |
| 包级状态在单进程下互相影响 | 按 Case 初始化；必要时整包串行并写明依据 |
| SQLite 与端口并发争用 | 依赖 R3 的容量控制；不用重试掩盖 |
| 覆盖口径被悄悄放宽 | `--audit-coverage` 硬门禁；dropped 必须带理由 |
| 迁移工作量超出预期 | 每批完成后评估；超出即停并反馈，不擅自缩小范围 |

---

## 10. 不做的事

- 不改写 `tests/unit/**` 的 1342 个用例（路径 C）。
- 不做前端适配器（R6）。
- 不删除 `testkit` 的入口与最终核验（R7）。
- 不修改 `Makefile`、CI 与文档（R8 / R9）。
- 不为迁移新增第二套断言库或平行 helper。

---

## 11. 输出给 R6 / R7 / R8

1. 328 条覆盖结论与 dropped 清单。
2. 迁移后仍依赖 `testkit` 的包与函数清单（R7 输入）。
3. 已确认的资源排布、包级串行声明与容量取值（R8 输入）。
4. `--audit-coverage` 的命令形态与 CI 接入点。
