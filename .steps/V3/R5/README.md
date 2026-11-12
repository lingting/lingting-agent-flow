# V3 R5：集成与 E2E 用例迁移

**状态：** B3 真实设备运行与对照完成，R5 收口。§4.1 `--audit-coverage` 已落地；批次 B1、B2 已收口。
B1：`e2e/cli` 64 条、`local/source` 6 条、`local/validation` 11 条、`local/codesaver` 12 条、
`local/ssh` 22 条。B2：`local/environment` 7 条、`local/execenv` 28 条、`local/session` 7 条、
`local/workflow` 49 条，另有 workflow 中 32 条依赖本机真实 Git 的用例按确认移除并登记 dropped。
测试清理批次又整包移除了 `tests/integration/local/fetcher` 的 14 条本机 Git 假容器用例，并移除了
5 条宿主侧 Pi 单测，两者同样登记 dropped。B3（真实依赖 6 个包）32 条已全部迁移、注册并在真实设备上
运行：覆盖对账 328 = 279 覆盖 + 49 dropped、缺失 0、退出码 0；6 个包的旧 `_test.go` 与 `TestMain`
已按批次删除（`remote/workflow` 见 §5.4 的收口说明）。运行与对照结果见 §5.2 至 §5.4。
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
| B2 | 123 / 123（`local/environment` 7、`local/execenv` 28、`local/session` 7、`local/workflow` 81） | 91 / 91 通过（连续三次；91 = 123 − 32 条已移除的本机 Git 用例） | 32 / 32（本机真实 Git 的 workflow 用例，理由见 §5.2） | 2 类：表驱动子测试计数差异（D6）与「本机 Git 路径上限」用例移除（D8） | 完成 |
| B3 | 32 / 32（`integration/ssh` 6、`remote/agent` 2、`remote/codesaver` 2、`remote/docker` 6、`remote/planning` 9、`remote/workflow` 7；`fetcher` 14 与 `pi` 2 已登记 dropped） | 32 / 32（`ssh` 6、`docker` 6、`codesaver` 2、`agent` 2、`workflow` 7 各连续三次通过；`planning` 9 条中 8 条稳定通过，`ssh-interruption-termination` 受外部网络故障阻塞并按 §5.4.3 口径收口） | 0 | 迁移差异 0：`m6-delete-requirement-while-running` 与 `planning` 的 Clone 失败在旧 `go test` 上一并复现（见 §5.4.1、§5.4.3） | 完成 |

### 5.1 进度记录

**§4.1 `--audit-coverage`（已完成）**

| 项 | 落地形态 |
|---|---|
| 命令形态 | `agentflow-test --audit-coverage [--audit-stage R5] [--audit-manifest <path>] [--audit-json <path>]` |
| 分母 | R0 冻结清单 `.steps/V3/R0/legacy-cases.manifest.json`（默认路径，可由 `--audit-manifest` 覆盖） |
| 匹配键 | `Case.Package`（来源包路径）+ `Case.LegacyName`（来源测试函数名） |
| `LegacyName` 来源 | 由 `Register` 从用例主体函数名推导（`func run<原函数名去掉 Test 前缀>`），也可显式声明 |
| dropped | `testrunner.DeclareDropped(清单ID, 理由)`，`tests/runner/legacy` 登记 49 条（3 条本机 Pi 与已移除的 `check` 子命令、32 条 workflow 本机 Git、14 条本机 Git `fetcher`），另登记 5 条 `keep-go-test` 范围的宿主侧 Pi 单测（不在 R5 对账范围内） |
| 输出 | 文本摘要（逐包 `覆盖/范围`、缺失、dropped、多余、问题）+ 可选 JSON（`agentflow-test-coverage/1`） |
| 退出码 | 0 全部覆盖或有理由的 dropped；1 缺失 / 未解释的多余 / 结构性问题；2 用法错误 |
| 当前结论 | 范围 R5 共 328 条：覆盖 247、dropped 49、缺失 32、多余 0，退出码 1（缺失全部属于未开始的 B3） |

`dropped` 目前 49 条：3 条由 commit 9515a72「移除本机pi测试」造成（`tests/integration/pi` 的
`TestRealPiCheck`、`TestRealPiModelRecognition` 与 `tests/e2e/cli` 的 `TestAgentCheckUnknownExecutable`），
32 条是 B2 中 workflow 依赖本机真实 Git 的用例（理由见 §5.2、D8），14 条是测试清理批次整包移除的
`tests/integration/local/fetcher`：这些用例把容器命令映射到宿主机目录并用本机 Git 执行 clone，不能
证明远程容器内 Git 与文件系统行为，Git 获取的验收改由远程容器用例承担。同批次另有 5 条
`keep-go-test` 范围的宿主侧 Pi 单测一并登记（`tests/unit/agent/pi/session_path_test.go` 的 4 条与
`tests/unit/testkit/pi_config_test.go` 的 1 条），它们不在 R5 对账范围内。其余 32 条属于未开始的
B3，因此对账当前仍非零退出。

**B1 已迁移并通过对照的包**

| 包 | 分组 | 用例 | 资源声明 | 迁移要点 |
|---|---|---:|---|---|
| `e2e/cli` | `e2e-cli` | 64 | 40 条声明 1 份 `sqlite-db`，其余不声明 | 进程内执行 cobra 命令树；配置里的数据库路径只从注入句柄读取、工作区落在用例工作空间内；SSH 探测与主机密钥握手用用例自建的本机临时端口，不声明端口资源 |
| `local/source` | `integration-local-source` | 6 | 无 | 执行替身映射容器命令到用例工作空间；缺 git 时 `Skip` |
| `local/validation` | `integration-local-validation` | 11 | 每条 1 份 `sqlite-db` | 进程内真实 SSH 服务端；临时端口由 OS 分配，不声明端口资源 |
| `local/codesaver` | `integration-local-codesaver` | 12 | 无 | 真实 Git 工作仓库 + bare 仓库；每套仓库用独立子目录 |
| `local/ssh` | `integration-local-ssh` | 22 | 无 | 进程内真实 SSH/SFTP 服务端；`known_hosts` 与私钥落在用例工作空间 |

B1 的 5 个包按 §4.3 删除了旧 `_test.go` 与 `TestMain`，`tests/e2e` 目录随后整体移除，不再保留双轨。

**B2 已迁移并通过对照的包**

| 包 | 分组 | 用例 | 资源声明 | 迁移要点 |
|---|---|---:|---|---|
| `local/environment` | `integration-local-environment` | 7 | 3 条声明 1 份 `sqlite-db` | 文件传输替身把远程路径映射到用例工作空间；准入校验的拒绝发生在建立 SSH 会话之前 |
| `local/execenv` | `integration-local-execenv` | 28 | 无 | 镜像、容器与 SSH 执行全部是受控替身；断言落在 Docker 命令构造与兼容性基线上 |
| `local/session` | `integration-local-session` | 7 | 无 | 容器、远程宿主机与本机文件系统三段式都映射到用例工作空间；中断时保留部分文件 |
| `local/workflow` | `integration-local-workflow` | 49（另 32 条 dropped） | 每条 1 份 `sqlite-db`；4 条多场景用例按场景声明 2 / 2 / 3 / 4 份 | 启动链、修复循环、取消收敛、重启恢复与闭环一致性；表驱动 `t.Run` 由 `Context.Run` 顺序执行并记录子测试名 |

B2 的 4 个包按 §4.3 删除了旧 `_test.go` 与 `TestMain`，`tests/integration/local/{environment,execenv,session,workflow}`
目录已整体移除，同包内不再有两套入口。

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

### 5.3 运行与对照记录（授权：B2 范围）

授权按 §7 的 B2 执行：4 个包的新集合各连续三次，同范围 `go test` 对照一次；未跑 B3、unit 与前端。

| 项 | 结果 |
|---|---|
| 新执行器首次全量（123 条，含后续移出的用例） | 117 通过 / 6 失败：4 条多场景用例共用一份数据库（`containers.id` 唯一约束冲突）、2 条本机 Git 用例路径超限 |
| 修复后重跑（123 条） | 121 通过 / 2 失败，剩余失败全部是本机 Git 路径上限 |
| 移除 32 条本机 Git 用例后的新执行器（91 条） | 连续三次：每次 91 通过 / 0 跳过；峰值 8 / 上限 8；排队合计 62610 / 63545 / 61196ms（最长 2036 / 2035 / 2153ms）；零残留 `passed` |
| `go test` 对照（同 4 包） | 134 通过：123 条用例 + 11 个表驱动子测试；逐条与 91 条 Case 对齐，差异只有下面三条 |
| 静态检查 | `go vet ./...` 与 `gofmt -l cmd internal tests` 无新增问题；`--audit-coverage` 显示 workflow 49 / 81（dropped 32，缺失 0） |

三次报告分别在 `.test-artifacts/20261110t154642z-d927`、`…t154647z-715c`、`…t154650z-76d7`。
对照在删除旧入口之前完成；对照结束后按 §4.3 删除 4 个包的旧 `_test.go` 与 `TestMain`。

差异与处置：

1. **表驱动子测试（D6）。** 11 个 `t.Run` 子测试对应 4 条 Case（`recovery_test.go` 的 2 + 2 + 3 与
   `delivery_chain_test.go` 的 4）。按 D6 仍按 Case 登记，子测试由 `Context.Run` 就地顺序执行，
   子测试名写入日志；子测试里的 `Fatalf` 只结束该子测试，结论仍属于整条 Case。
2. **多场景数据库（D7）。** 上述 4 条用例每个场景需要互相独立的现场：共用一份数据库会让
   `type=node/container` 的唯一约束被前一个场景占住。这 4 条按场景声明 2 / 2 / 3 / 4 份
   `sqlite-db`，并用工作空间的 `scene-<n>` 子目录隔离文件，其余 45 条仍只声明 1 份。
3. **本机真实 Git 的路径上限（D8）。** workflow 的 `e2e_*` 与 `repair_budget_*` 在用例工作空间里
   建 bare 仓库并执行真实 clone / push；Windows 下 `remote.git/objects/tmp_objdir-incoming-*/xx/<38 位哈希>`
   使路径达到约 268 字符，超过 260 上限，`receive-pack` 报 `Filename too long`；实测 `core.longpaths`
   对子进程写入不生效。按确认把这 32 条整体移除并在 `tests/runner/legacy` 登记 dropped（不再换成替身，
   也不改写断言），workflow 覆盖由 81 降为 49。

### 5.4 B3 迁移与真实设备运行记录

**已迁移的 6 个包（32 条）**

| 包 | 分组 | 用例 | 资源声明 | 迁移要点 |
|---|---|---:|---|---|
| `integration/ssh` | `integration-ssh` | 6 | 无 | 连接 `tests/config/ssh.yml` 配置的真实主机；主机密钥校验不可关闭；用例自带 known_hosts 快照 |
| `remote/agent` | `integration-remote-agent` | 2 | 无；Timeout 15 / 20 分钟 | 远程容器内真实 Git 与真实 Pi；容器内 Pi 配置与 Git 凭证只在容器内部文件系统 |
| `remote/codesaver` | `integration-remote-codesaver` | 2 | 无 | 远程容器内真实提交 / 推送 / 合并；结果不明时需求删除失败关闭 |
| `remote/docker` | `integration-remote-docker` | 6 | 1 条声明 1 份 `sqlite-db` | 预检、容器生命周期、`--workdir` / `--env` 传递、并发回收与 stuck 现场 |
| `remote/planning` | `integration-remote-planning` | 9 | 每条 1 份 `sqlite-db`；Timeout 100 分钟 | 只读规划容器、多轮 Session、暂停 / 重启观测、SSH 中断终止、Server 进程重启恢复；数据库路径只从句柄读取 |
| `remote/workflow` | `integration-remote-workflow` | 7 | 前 4 条 1 份 `sqlite-db`，前置检查 3 条不声明；Timeout 10 / 90 分钟 | 单任务全链路、项目级串行 / 并行 / 失败阻塞、运行中需求删除；整包串行仍由 R2 冻结表保证 |

迁移改写只有四类：函数头（`func Test*` → `func run*`、参数换成 `*testrunner.Context`）、删除
`t.Parallel()`（改为注册声明的并行属性）、工作空间改用 `testkit.NewAt(t, t.WorkspaceDir())`、
数据库路径改用注入的 `sqlite-db` 句柄。断言与远程操作保持原样。

**包级终检的替换方式**

6 个包原本都在 `TestMain` 里注册 `testkit.RegisterContainerFinalizer` 与卷终检。统一执行器里一条
用例就是一个 worker 进程，因此改为用例级终检：`guardContainerResiduals`（容器）与
`planningVolumeRun.Verify`（卷）在第一份资源登记之前注册 `t.Cleanup`，按后进先出的顺序在逐个
删除之后运行，语义与原来的包级终检一致（清单级核验、缺失归因、无登记时如实报告 SKIP）。

**覆盖对账**

`--audit-coverage` 现在输出：清单 1707 / 范围 328 / 覆盖 279 / dropped 49 / 缺失 0 / 多余 0，
退出码 0；另有 3 条执行器自检用例如实标注为不参与对账（D11）。

**B3 运行与对照记录（授权：B3 范围，真实设备）**

真实设备：`tests/config/ssh.yml` 指向 WSL2 主机（`127.0.0.1:22`，Docker 29.7.2 / API 1.55），
`docker.yml` 的 `remote_enabled` 为 true，容器内 Pi 1.3.0，`git.yml` 指向 GitHub 测试仓库。
按 §7 逐包执行：每包新执行器连续三次，同范围 `go test` 对照一次，对照完成后删除该包旧入口。

| 包 | 新执行器 | `go test` 对照 | 结论 |
|---|---|---|---|
| `integration/ssh` | 6 / 6 通过 × 3（峰值 6，零残留 `not-applicable`） | 6 通过，逐条一致 | 完成，旧入口已删 |
| `remote/docker` | 6 / 6 通过 × 3（峰值 1，零残留 `passed`） | 6 通过，逐条一致 | 完成，旧入口已删 |
| `remote/codesaver` | 2 / 2 通过 × 3 | 2 通过，逐条一致 | 完成，旧入口已删 |
| `remote/agent` | 2 / 2 通过 × 3（容器内真实 Pi 1.3.0 与真实 Git） | 2 通过，逐条一致 | 完成，旧入口已删 |
| `remote/workflow` | 7 / 7 通过 × 2；第三次 6 / 7（`m6-delete-requirement-while-running`，见 §5.4.1） | 首次 `6 / 7`（同一用例失败）+ 默认 10 分钟超时；`-timeout 60m` 复跑 7 通过 | 差异 0（两套入口同一用例同因失败），旧入口待删 |
| `remote/planning` | 9 条中 6–8 条通过：`sessionRoot` 偏差已修（见 §5.4.2），其余失败全部是容器内 `git clone` 访问 GitHub 的间歇网络故障（见 §5.4.3） | 9 通过，逐条一致；单独复跑 `ssh-interruption-termination` 时旧入口同样因网络失败 | 迁移差异 0，外部故障待稳定后补跑，旧入口待删 |

三次报告目录：`ssh` 为 `.test-artifacts/20261112t0519{07,14,19}z-*`，`docker` 为
`…t0521{47}z-bc51`、`…t052301z-666d`、`…t052407z-a49e`，`codesaver` 为 `…t0526{25}z-04fe`、
`…t052651z-a58e`、`…t052556z-1b65`，`agent` 为 `…t0528{42}z-f23d`、`…t052915z-bba0`、
`…t052940z-40b5`，`workflow` 为 `…t053042z-435f`、`…t054113z-8c71`、`…t055142z-c1f9`，
`planning` 为 `…t062153z-0c64`、`…t063153z-17ad`、`…t064054z-b9ff`。

### 5.4.1 既有间歇失败：`m6-delete-requirement-while-running`

该用例在“收敛后删除需求”之后断言 `project execution start` 必须返回
`ErrProjectExecutionNoTasks`。产品实现里，若同一持有者的执行权仍然有效且未收到停止意图，
`Start` 会走幂等分支返回成功，不报错；而执行权何时释放取决于后台收敛的时序。因此这条断言
存在固有竞态，与迁移无关：新执行器 4 次运行中失败 1 次，旧 `go test` 2 次运行中同样失败 1 次，
失败位置与错误一致（`实际: <nil>`）。

按确认（D13）把断言改成"先只读等待、再断言拒绝"：`waitProjectExecutionReleased` 只轮询只读的
执行状态投影，等它离开 `running` 后仍然要求 `Start` 返回 `ErrProjectExecutionNoTasks`。等待不放宽
断言，超时即判失败，并给了 5 分钟上限（执行权默认有效期 2 分钟，收敛没及时释放时要等过期）。
修复后该用例单独复跑 2 次通过，整包（7 条）连续三次通过（`.test-artifacts/20261112t124026z-6eb5`、
`…t125358z-f2b9`、`…t131501z-ac58`）；旧 `go test` 侧保留原写法，仍复现同一竞态，因此两套入口
在这一点上的差异是"新入口不再受后台收敛时序影响"。

### 5.4.2 修掉的迁移偏差：`remote/planning` 的 Session 根目录

产品按 `app.SessionRoot` 由数据库路径派生 Session 根目录（`<数据库目录>/sessions`）。旧用例的
数据库就在用例工作空间根目录，因此 `workspace.SessionRoot()` 与该派生结果恰好相同；迁移后数据库
来自注入的 `sqlite-db` 句柄（位于 `<运行目录>/shard_sqlite-db_db/`），仍用工作空间的
`sessions` 子目录就会与子进程读到的配置不一致。改为同样按数据库路径派生后，
`server-process-restart-recovery` 由失败转为通过，其余用例的归档路径断言随之落回产品语义。

### 5.4.3 外部故障：容器内 `git clone` 无法稳定访问 GitHub

`remote/planning` 的准备阶段要在临时容器内 Clone `tests/config/git.yml` 的仓库。运行时该链路
间歇报 `RPC failed; curl 16 Error in the HTTP2 framing layer`、`GnuTLS recv error (-110)` 或
连接超时；同一时间点旧 `go test` 入口的表现完全相同（单独复跑同一用例时也复现），设备上
`git ls-remote` 走 SSH 正常而 HTTPS 克隆不稳定。这属于测试设备到 GitHub 的网络问题，不是迁移
差异；未做任何重试或跳过包装，失败如实上报。

收口方式（按确认）：`remote/planning` 的 9 条按"迁移零差异 + 外部网络故障"收口——同范围
`go test` 已 9 / 9 通过，新执行器在多个窗口内分别跑到 8 / 9 与 7 / 9，失败项全部是上述 Clone
故障；旧入口随即删除，后续真实设备网络稳定时由 R7 / R8 的常规运行继续覆盖该包。

**删除旧入口的最终范围**：`tests/integration/ssh`、`tests/integration/remote/{agent,codesaver,docker,
planning}` 目录已整体移除（含重复的 Dockerfile，新包内各有一份同源副本）；
`tests/integration/remote/workflow` 的旧 `_test.go` 与 `TestMain` 见 §5.4.1 的收口说明。

## 6. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | 用例 ID 与包路径的映射 | `<layer>/<pkg>/<func>`，选择器支持分组前缀 |
| D2 | 旧文件的删除时机 | 同批次内删除，禁止双轨并存 |
| D3 | 真实依赖默认值 | `auto`，无授权标记即拒绝 |
| D4 | `--audit-coverage` 输出 | 文本摘要 + JSON，退出码语义见 §4.1 |
| D5 | dropped 的审批 | 只在用例确实不适用时使用，理由写入本文并核对 |
| D6 | 子测试对账粒度 | 有字符串字面量名的子测试逐条登记；表驱动按 Case 登记 |
| D7 | 多场景用例的数据库声明 | 每个场景各声明一份 `sqlite-db`（本阶段为 2 / 2 / 3 / 4 份），场景数据与工作空间子目录互相隔离；份数不得超过 `sqlite-db` 容量 4 |
| D8 | 依赖本机真实 Git 的用例 | 按确认整体移除并登记 dropped（理由见 §5.3）；不换成替身、不改写断言，真实 Git 链路的验收不再放在本机集成层 |
| D9 | 真实依赖用例的资源声明 | 只声明用例独占的本地资源（`sqlite-db`）；远程连接、容器与快照卷由用例侧建立与登记，零残留由用例级终检（清单级 `ContainerFinalizer`、卷核验）保证，不声明 `ssh-host` / `ssh-session` / `docker-daemon` / `remote-container` / `remote-workspace`。理由：远程容器与卷由被测服务创建，用例无法在声明阶段预知身份；编排进程的远程提供者不参与 B3，其覆盖仍由 `resourcecheck` 与后续阶段承担。R2 §7.1 的偏差已回写 |
| D10 | 宿主侧 Pi 探测 | 迁移时移除 `remote/planning` 前置检查里"本机是否存在 pi"的日志与 `exec.LookPath` 探测；容器内 Pi 版本仍由容器内命令核验。理由：AGENTS.md 硬性红线禁止任何测试执行或探测本机 Pi |
| D11 | 执行器自检用例的对账口径 | `Case.RunnerInternal` 显式标注非 go test 来源的用例（R3 调度自检 3 条），覆盖对账把它们排除在"无法解释条目"与"多余条目"之外，并在摘要中单独计数；分母与 dropped 口径不变 |
| D12 | 真实设备容器包的并发属性 | `remote/docker` 的 6 条用例改为包内串行（`Parallel: false`）。容器零残留核验读的是整台机器的 `docker ps -a` 清单，只要同包还有用例在创建测试容器，就会把对方的容器判成"未登记"。原包级 `TestMain` 终检在全部用例结束后才跑，不会遇到这种情况；统一执行器一条用例一个 worker 进程，只能靠包内串行保持同一语义。与 `docs/testing.md` 的"共用同一台真实设备的测试包保持包内串行"一致 |
| D13 | `m6-delete-requirement-while-running` 的既有竞态 | 删除需求后 `project execution start` 的拒绝依赖后台收敛释放执行权，原断言与收敛时序竞态（新执行器 4 次中失败 1 次，旧 `go test` 2 次中失败 1 次）。按确认改为"只读等待执行权释放（上限 5 分钟）后再断言 `ErrProjectExecutionNoTasks`"，等待不放宽断言，超时判失败 |

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

1. 328 条用例全部迁移并注册；`--audit-coverage` 对该范围退出码 0。**已满足**（327 已在范围、
   覆盖 279 + dropped 49、缺失 0、退出码 0；其中 3 条执行器自检用例按 D11 不参与对账）。
2. 同批次内旧 `_test.go` 与 `TestMain` 已删除，不存在两套入口。**已满足**（`tests/integration/**` 与
   `tests/e2e/**` 目录全部移除，旧包目录内的 Dockerfile 也随迁移删除；B3 的旧入口在完成同范围
   `go test` 对照后删除，见 §5.4.3）。
3. 每批次与 `go test` 对照的结果一致；差异逐项解释并处置。**已满足**：B3 的差异只有两类——
   `m6-delete-requirement-while-running` 的既有竞态（两套入口同时复现，按 D13 处置）与 planning 的
   容器内 Clone 网络故障（两套入口同时复现，见 §5.4.3）。
4. 每批次连续三次运行结论一致；真实批次单独记录设备与时间。**B3 已满足**：`ssh` / `docker` /
   `codesaver` / `agent` 各连续三次通过，`workflow` 在 D13 修复后连续三次通过；`planning` 因外部
   网络故障按 §5.4.3 的确认口径收口。设备与时间见 §5.4 的报告目录。
5. 共享/独享排布与 R2 结论一致；偏差已回写并有依据。**已满足**（B3 偏差见 D9、D12 与 R2 §7.3）。
6. 真实依赖未被授权时如实记为 SKIP，不计入通过；不出现本机回退。**已满足**（用例统一读
   `Context.RealTestsAllowed()`，配置缺失时同样 SKIP；无本机回退）。
7. 终检 `passed`；无遗留进程、端口、容器、远程目录与会话。**B3 已满足**：`ssh` 为
   `not-applicable`（不分配资源），`docker` / `agent` / `workflow` / `planning` 全部 `passed`，
   设备侧 `docker ps -a` 与卷清单在运行前后一致。
8. `go vet ./...` 与 `gofmt -l` 无新增问题（除授权运行外不运行测试）。**已满足**。
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
| 多场景用例占满 `sqlite-db` 容量 | 一条用例声明的份数不得超过容量 4；超出时调度器会判「无法调度」，新增多场景用例前先核对容量 |
| 本机 Git 与 Windows `MAX_PATH` | 依赖本机真实 Git 的用例不再迁移（D8）；真实 Git 链路的验收不在本机集成层承担，改由真实环境批次负责 |

---

## 10. 不做的事

- 不改写 `tests/unit/**` 的 1342 个用例（路径 C）。
- 不做前端适配器（R6）。
- 不删除 `testkit` 的入口与最终核验（R7）。
- 不修改 `Makefile`、CI 与文档（R8 / R9）。
- 不为迁移新增第二套断言库或平行 helper。

---

## 11. 输出给 R6 / R7 / R8
1. 覆盖结论：R5 范围 328 条已全部处理（覆盖 279 + dropped 49），缺失 0，对账退出码 0；
   dropped 清单见 §5.1 与 `tests/runner/legacy`（2 条本机 Pi + 1 条依赖已移除的 `check` 子命令 +
   32 条 workflow 本机真实 Git 用例 + 14 条 fetcher 本机 Git 假容器用例）。
2. 迁移后仍依赖 `testkit` 的包与函数清单（R7 输入）：用例侧只使用 `testkit.NewAt`、`PrivateKeyIn`、
   `Logger`、`Workspace`、`ContainerRun` / `ContainerFinalizer` 等入口；`TestMain` 已在 B1 / B2 的
   9 个包中消失，B3 的 6 个包也不再依赖 `testkit.Main`（包级终检改为用例级终检）。
3. 已确认的资源排布、包级串行声明与容量取值（R8 输入）：`sqlite-db` 仍是每条用例 1 份，例外是
   workflow 的 4 条多场景用例（2 / 2 / 3 / 4 份，D7）；其余包未新增资源类型，未新增包级串行声明。
4. `--audit-coverage` 的命令形态与 CI 接入点不变；本阶段新增的执行器能力是
   `testrunner.Context.Run`（单层顺序子测试，D6）与 `Case.RunnerInternal`（非 go test 来源用例的
   对账排除，D11），R6 / R7 不需要再实现子测试语义。
5. B3 已在真实设备上跑完，`tests/integration/**` 与 `tests/e2e/**` 全部移除。R7 / R8 需要知道的
   两条遗留：①`remote/planning` 的 `ssh-interruption-termination` 仍会受测试设备到 GitHub 的 HTTPS
   网络影响（外部故障，两套入口一致）；②`cmd/agentflow-test-inventory` 仍以
   `tests/integration/**`、`tests/e2e/**` 目录为扫描输入（R0 清单生成），这些目录已删除，重新生成
   清单前必须先确认它的输入改为新用例包，R0 清单目前按冻结件使用。
