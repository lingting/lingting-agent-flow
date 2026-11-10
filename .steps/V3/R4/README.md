# V3 R4：真实服务进程试点（`server.shutdown`）

**状态：** 完成。`pilot/server-shutdown` 连续三次通过，未就绪反证与取消路径各自给出非零退出码与
`passed` 终检，`go vet` / `gofmt` 无新增问题。
**上游文档：** [路线总规划](../ROADMAP.md)、[R2 资源管理器](../R2/README.md)、
[R3 并发调度与取消](../R3/README.md)。
**范围：** 用一条真实用例打通「启动真实服务进程 → 就绪 → 停止 → 资源释放 → 零残留 → 统一报告」。
未批量迁移用例（R4.5 / R5）、未做前端（R6）、未改 `Makefile` / CI / `docs/`。

---

## 1. 目标

R1–R3 验证的都是自检用例与本地端口。本阶段第一次把真实产品进程放进新体系，检验 R2 的
`service-process` 提供者、R3 的取消路径与零残留核验是否成立，并以此确定后续迁移的用例组织方式。

验收对象是一条 Case（`pilot/server-shutdown`）：

- 启动 `bin/agentflow server`，使用示例配置与用例工作空间内的独立数据库文件；
- 等待 `/api/v1/health` 就绪，健康检查通过前不算启动成功；
- 向服务进程投递中断信号，等待进程退出并读取真实退出码；
- 断言服务退出后数据库可重新打开、端口可重新绑定、无遗留子进程；
- 报告里能看到 `service-process` 的句柄、释放结论与终检结论。

另有一条反证用例（`pilot-fault/server-not-ready`）与一次取消注入运行，分别验证 §7.2 与 §7.4。

---

## 2. 输入与事实

- 服务入口：`cmd/agentflow` → `agentflow server`，参数支持 `--config`、`--address`、`--require-web-ui`；
  配置项包含 `server.shutdown_timeout_seconds`（`internal/cli/server.go`）。
- 停止信号：`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`
  （`cmd/agentflow/main.go`）；Windows 下 `CTRL_BREAK_EVENT` 由 Go 运行时映射成 `os.Interrupt`。
- 健康检查：`/api/v1/health`（`internal/httpapi/routes.go`），成功时返回健康检查 JSON。
- 优雅关闭标记：服务走完关闭流程后把 `AgentFlow 服务已关闭` 写入标准输出
  （`internal/httpserver/server.go` 的 `report`），被强制终止时不会写出这一行。
- 数据库：`sqlite.Open` 会校验连接参数并执行版本化迁移，因此"能重新打开"同时说明文件锁已释放、
  Schema 完整；正常关闭数据库时 SQLite 会收起 `-wal` / `-shm`。
- 测试工作空间必须落在 `os.TempDir()` 下，路径使用绝对路径，数据库不写进仓库
  （[docs/testing.md](../../../docs/testing.md) §4）。
- 测试产物不删除；「零残留」只针对进程、端口、容器、远程目录与会话，不针对工作空间与产物。
- 真实依赖判定必须由编排进程下发，worker 不读环境变量（R1 §6）。

### 2.1 试点新增的事实（本阶段实测）

- 托管子进程此前没有独立进程组，Windows 上无法按进程组投递 `CTRL_BREAK`：`GenerateConsoleCtrlEvent`
  的进程组必须由 `CREATE_NEW_PROCESS_GROUP` 建立。已在 `internal/testrunner` 补上。
- 服务进程此前的标准输出被丢弃（`exec.Cmd` 未接管 stdio），"是否优雅关闭"没有可读证据。现在由
  提供者写入用例工作空间的 `process/` 目录，路径进入报告的 `log_path`。
- Windows 上 `os.FindProcess(pid).Wait()` 可等待任意同用户进程并读取退出码（不接管父子关系），
  因此用例侧能拿到服务自己退出的真实退出码；非 Windows 无法读取，按 §9 记录差异。

---

## 3. 交付物（已完成）

| 位置 | 内容 |
|---|---|
| `tests/runner/pilot` | 试点用例组：`pilot/server-shutdown`、`pilot-fault/server-not-ready` 与同包辅助代码 |
| `tests/runner/registry` | 追加一行导入 |
| `internal/testrunner/workspace.go` | 用例工作空间根、用例工作空间目录、资源存放目录 |
| `internal/testrunner/resource_process.go` | 托管子进程的工作目录、输出落盘、独立进程组、`ProcessLogPath` |
| `internal/testrunner/proctree_*.go` | `managedProcessGroupAttr`、`InterruptProcess` |
| `internal/testrunner/orchestrator.go` | 分配前建工作空间、经分配上下文下发、报告填 `log_path` / `payload` |
| `internal/testrunner/result.go` | 报告新增 `resources[].log_path`、`resources[].payload` |
| `internal/testrunner/context.go` / `worker.go` / `workerproc.go` / `cmd/agentflow-test` | `--workspace` 参数与 `Context.WorkspaceDir()` |
| `internal/testrunner/resource_{sqlite,git_worktree,ssh_server}.go` | 资源目录改为 `shard_<type>_<id>` |
| 本文 §5 / §6 / §7 / §9 / §10 | 冻结决策、执行记录、验收结果、已知差异与缺陷清单 |

用例包组织方式（D1）作为 R4.5 / R5 的模板。

---

## 4. 实施步骤与结论

### 4.1 准备运行环境（已完成）

- 被测二进制由授权命令构建；提供者按内容寻址计算二进制 SHA256 写入 `resources[].fingerprint`，
  报告因此能对上"本次跑的是哪一份构建"。
- 工作空间：`<os.TempDir()>/lingting-agent-flow-tests/<run-id>/<case-id>/`。服务进程以该目录为工作
  目录启动，示例配置里的相对路径（`.agentflow/agentflow.db`、`.agentflow/worktrees`）因此全部落在
  用例工作空间内，仓库与用户目录不产生任何运行期文件。
- 监听地址取提供者分配的空闲端口：声明的 `args` 里写 `127.0.0.1:{port}`，由提供者替换。

### 4.2 启动与就绪（已完成）

- 通过 `service-process` 提供者启动进程，句柄记录 `pid`、`base_url`、`binary` 与指纹；三项内容
  同时写进报告的 `resources[].payload`，句柄因此可以直接在报告里读到。
- 就绪判定是轮询 `/api/v1/health` 返回 200，带超时与失败原因；就绪前用例主体不执行。
- 服务进程的 stdout/stderr 写入 `<用例工作空间>/process/<type>-<name>-<token>.log`，路径写入报告；
  失败信息（未能常驻、未就绪）都会指出该文件，不从 worker 的 stdout 反推。

### 4.3 停止（已完成）

主路径：用例先 `os.FindProcess(pid)` 打开进程句柄，再 `testrunner.InterruptProcess(pid)` 投递中断，
然后等待退出并读取退出码。

- Windows 下托管子进程以独立进程组启动（`CREATE_NEW_PROCESS_GROUP`），
  `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, pid)` 按组投递，同控制台的其它进程不受影响。
- 用例同时断言两条独立证据：退出码为 0，且服务输出含 `AgentFlow 服务已关闭`。被强制终止的进程
  两条都不满足，因此"进程被杀"不会被写成"优雅关闭通过"。
- 投递失败时不伪造成通过：用例直接判失败并写明"优雅停止路径未验证"，由本文 §9 记录差异；提供者
  的强制停止路径由 `pilot-fault`（就绪失败 → 提供者回收）单独覆盖。
- 实测关闭耗时 9–12ms（示例配置的 `shutdown_timeout_seconds` 为 0，即默认 10s）。

### 4.4 关闭后核验（已完成）

用例按固定顺序核验，顺序本身有意义：

1. 退出码 0（见 4.3）；
2. 进程消失：`testrunner.ProcessAlive` 重新查询系统状态，不只依赖 `Wait` 的返回值；
3. WAL/SHM：仍在则记警告（正常关闭会收起），不单独判失败；
4. 端口重新绑定：`net.Listen` 同一 `host:port` 成功后再关闭；
5. 数据库重新打开：走产品入口 `sqlite.Open`（连接参数校验 + 迁移 + 仓储），再用 `ListRoles` 确认
   打开的确实是服务初始化过的那份库；
6. 迁移完整：直接读 `goose_db_version`，已应用条数必须等于版本表总条数；
7. 优雅关闭标记：服务输出里必须出现 `AgentFlow 服务已关闭`。

提供者侧的自证不变：`Release` 终止进程树后 `Verify` 复核进程不存在且端点不再响应，
`resources[].released` / `verified` 同时为 true 才算释放成功。

### 4.5 取消路径（已完成）

`--selfcheck-cancel-on pilot/server-shutdown` 在 worker 真正启动后取消整次运行：
worker 被终止，服务进程由提供者在清理阶段（`context.WithoutCancel`）停止，终检照常执行。

实测：`cancelled=true`、原因写明注入来源、用例结论 `error`「运行被取消」、
`resources[].released` / `verified` 均为 true、`residual_check=passed`、退出码 1、
`tasklist` 复查无遗留 `agentflow.exe`。

观测到的一个时序细节：`--grace 300ms` 与 `--grace 1ms` 两次运行时，用例主体都在被终止前完成了
中断投递，服务因此仍是优雅退出的。这说明"取消"与"按 §4.3 停止"是两条独立路径，不能互相替代；
提供者强制停止的实测证据来自 `pilot-fault`（服务就绪失败后由提供者 `taskkill /T /F` 回收）。

### 4.6 稳定性（已完成）

同一试点连续运行三次，结论一致（§6.1）。三次运行之间用只读 `tasklist` 核对，无遗留 `agentflow.exe`。

---

## 5. 阶段内冻结的决策

| 编号 | 决策 | 结论 |
|---|---|---|
| D1 | 用例包组织方式 | `tests/runner/<layer>/<pkg>`；用例与辅助代码同包、非 `_test.go`，按职责拆文件（声明与流程 / 核验 / 数据库 / 进程 / 路径约定），`registry` 追加一行导入 |
| D2 | 停止方式 | 优先按进程组投递中断（Windows 用 `CTRL_BREAK_EVENT`）；投递不可用时用例判失败并记录差异，禁止用 `taskkill /F` 冒充优雅停止 |
| D3 | 就绪判定 | `/api/v1/health` 返回 200（并复核响应是健康检查 JSON）；禁止固定 `sleep`，禁止用"进程还活着"代替"服务可用" |
| D4 | 服务进程失败时的结论 | `error`，原因必须写明是启动失败、未就绪还是停止后仍存活（实测见 §6） |
| D5 | 工作空间保留策略 | 一律保留；「零残留」不覆盖工作空间与产物 |
| D6 | 工作空间布局 | `<os.TempDir()>/lingting-agent-flow-tests/<run-id>/<case-id>/` 为用例工作空间；`<工作空间根>/shard_<type>_<id>/` 为资源存放目录；服务进程输出在 `<用例工作空间>/process/` |
| D7 | 提供者与用例之间的新通道 | 用例工作空间经资源分配上下文下发给提供者、经 `--workspace` 下发给 worker，不新增环境变量、不改资源契约；平台能力以 `InterruptProcess` / `ProcessAlive` 导出 |
| D8 | 报告新增字段 | `cases[].resources[].log_path`、`cases[].resources[].payload`；报告版本仍为 `agentflow-test-report/1`（只做加法） |

---

## 6. 运行授权与执行记录

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `pilot`（`pilot/server-shutdown`）、`pilot-fault`（`pilot-fault/server-not-ready`）；经用户追加授权复跑 `selfcheck` 与 `resourcecheck` 作为本轮改动的回归 |
| 允许的命令 | 见下方命令块 |
| 明确不允许 | `tests/unit`、`tests/integration`、`tests/e2e` 的 `go test`；真实 Pi / SSH / 远程 Docker；前端命令 |
| 理由 | 试点只依赖本机服务进程；真实外部设备留到 R4.5 / R5 单独授权 |

```bash
go build -o bin/agentflow.exe ./cmd/agentflow
go build -o bin/agentflow-test.exe ./cmd/agentflow-test
./bin/agentflow-test.exe --artifact-dir .test-artifacts pilot
./bin/agentflow-test.exe --artifact-dir .test-artifacts pilot-fault
./bin/agentflow-test.exe --grace 300ms --selfcheck-cancel-on pilot/server-shutdown \
  --artifact-dir .test-artifacts pilot
./bin/agentflow-test.exe --artifact-dir .test-artifacts selfcheck
./bin/agentflow-test.exe --artifact-dir .test-artifacts resourcecheck
# 只读核对：tasklist / netstat；不终止非本运行创建的进程
```

Windows 下构建产物带 `.exe`：`CreateProcess` 使用 `bin/agentflow` 这类无扩展名路径启动并不可靠，
试点因此优先使用 `bin/agentflow.exe`，构建命令也按带扩展名的形式执行。

### 6.1 执行记录（Windows 11 + Go 1.25.7）

| 命令 | 退出码 | 观测 |
|---|---|---|
| `--artifact-dir .test-artifacts pilot`（第 1 次） | 0 | 通过 1；用例 278ms；退出码 0；关闭耗时 11ms；端口可重新绑定；数据库可重开（角色 4 条）；迁移版本 4（已应用 5 条）；输出含优雅关闭标记；零残留 passed |
| 同上（连续第 2 次） | 0 | 通过 1；用例 237ms；关闭耗时 10ms；其余断言与第 1 次一致 |
| 同上（连续第 3 次） | 0 | 通过 1；用例 255ms；关闭耗时 12ms；其余断言一致 |
| 同上（连续第 4 次） | 0 | 通过 1；用例 267ms；关闭耗时 11ms；其余断言一致 |
| `--artifact-dir .test-artifacts pilot-fault` | 1 | `pilot-fault/server-not-ready` 判 `error`：`分配资源 "service" 时服务没有就绪: …: 在 8s 内没有就绪: 就绪探测返回状态码 404`；worker 启动 0（主体未执行）；零残留 `not-applicable`；`tasklist` 无遗留进程 |
| `--grace 300ms --selfcheck-cancel-on pilot/server-shutdown pilot` | 1 | `cancelled=true`，原因写明注入来源；用例 `error`「运行被取消」；`released` / `verified` 为 true；零残留 passed；`tasklist` 无遗留进程 |
| `--grace 1ms --selfcheck-cancel-on pilot/server-shutdown pilot` | 1 | 结论同上；用例 290ms |
| `--artifact-dir .test-artifacts selfcheck` | 0 | 通过 2 / 跳过 1；零残留 passed（回归：本轮改了工作空间与托管子进程） |
| `--artifact-dir .test-artifacts resourcecheck` | 0 | 7 条全部通过；零残留 passed（回归：`sqlite-db` / `git-worktree` / `local-ssh-server` 目录改为 shard 布局，`child-process` / `service-process` 启动方式改变） |
| `go vet ./...`、`gofmt -l cmd internal tests` | 0 / 空 | 无新增问题 |

三次连续运行的报告分别在 `.test-artifacts/20261110t132151z-a99e`、`…t132152z-01a4`、
`…t132153z-b182`。每条用例的完整观测在 `<run-dir>/cases/pilot__server-shutdown/stdout.log`，
服务进程输出在报告 `resources[].log_path` 指出的文件里。

本阶段未运行 `tests/unit` / `tests/integration` / `tests/e2e` 的任何 `go test`，未触碰真实 Pi / SSH /
远程 Docker，未运行前端命令。

---

## 7. 验收条件与结果

| 条件 | 结果 |
|---|---|
| 1. 试点 Case 连续三次运行结论一致，报告齐全 | 达成：三次均 `passed`，逐条断言一致（§6.1） |
| 2. 就绪判定有效：服务未就绪时用例失败且失败原因明确 | 达成：`pilot-fault` 判 `error` 并给出"在 8s 内没有就绪: 就绪探测返回状态码 404"，主体未执行 |
| 3. 优雅停止后：进程消失、端口释放、数据库可重开、终检 `passed` | 达成：退出码 0 + 进程消失 + 端口重新绑定 + 数据库重开 + 迁移完整 + 优雅关闭标记 + 零残留 passed |
| 4. 取消路径：无遗留进程、无遗留端口、资源已释放、终检仍执行、退出码非零 | 达成：两次取消运行的 `released` / `verified` 均为 true、零残留 passed、退出码 1、`tasklist` 无遗留 |
| 5. 报告包含 `service-process` 句柄、释放结论、终检结论与产物路径 | 达成：`resources[]` 含 `type` / `fingerprint` / `payload{pid,base_url,binary}` / `released` / `verified` / `log_path`，另有 `residual_check` 与 `cases[].artifacts` |
| 6. 用例组织方式与资源声明写法可供 R4.5 直接复用 | 达成（§5 D1、§10） |
| 7. `go vet ./...` 与 `gofmt -l` 无新增问题（不运行测试） | 达成 |

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| Windows 无法可靠向子进程投递中断 | 独立进程组 + `CTRL_BREAK_EVENT`；投递失败判失败并记录差异（§9），不伪造成优雅关闭 |
| 服务停止时间超过默认超时 | 等待窗口 30s（大于配置的 10s），超时判 `error` 并保留服务输出与工作空间 |
| 数据库仍被占用导致「已关闭」误判 | 关闭后重新打开同一路径，以连接参数校验、迁移与仓储读取为准 |
| 遗留 `agentflow` 进程污染后续运行 | 每轮开始前只读 `tasklist` 核对，发现即停止并指出 PID；运行级终检由提供者快照兜底 |
| 服务进程输出被丢弃、失败无法定位 | 提供者把输出写入用例工作空间，路径进入报告；未能常驻/未就绪的错误信息直接给出该路径 |
| 试点通过掩盖真实用例的差异 | 试点只证明机制；R4.5 必须用既有集成用例再验证，R5 才批量迁移 |

---

## 9. 已知差异与未覆盖面

1. **`shutdown_timeout_seconds` 的配置值未被单独验证。** 试点使用示例配置的默认值（10s），实测关闭
   耗时 9–12ms。构造"关闭被阻塞到超时"的场景需要长连接请求，留到 R5 的真实用例。
2. **非 Windows 平台无法读取非子进程退出码。** 用例会记警告并改以服务日志的优雅关闭标记为准；
   本机（Windows 11）已断言退出码为 0。
3. **跨运行的遗留进程没有自动检测。** 每次运行新建工作空间，服务端进程锁只在同一次运行内生效，
   因此上一轮遗留的服务只能由运行前的只读 `tasklist` 发现。R4.5 / R5 需要 runner 级的运行前检查。
4. **`Spec` 参数按空格切分（R2 冻结语义）。** 仓库路径含空格时试点无法传递 `--config`，会在分配
   阶段以"服务没有就绪"失败。
5. **声明在注册期定稿，运行期路径无法注入。** 服务只能用仓库内示例配置，靠工作目录隔离数据；如果
   R4.5 / R5 需要"每用例一份配置文件"，要先解决"注册期声明 vs 运行期路径"，本阶段未做。
6. **取消路径通常先由用例的中断投递收敛。** 见 §4.5：两条路径的实测证据分别来自 `pilot` 与
   `pilot-fault`，不能互相替代。

文档同步待办（R4 不改文档，留给 R9）：`docs/testing.md` §4 与 R2 §4 描述的资源目录已变为
`<工作空间根>/shard_<type>_<id>/`，用例工作空间根移到 `os.TempDir()` 下。

---

## 10. 试点暴露的缺陷与已修补项（输出给 R4.5）

| 暴露的问题 | 处理 |
|---|---|
| `service-process` / `child-process` 丢弃子进程 stdout/stderr，失败时无现场 | 输出写入 `<用例工作空间>/process/`，路径进报告 `log_path` |
| 托管子进程没有独立进程组，Windows 上无法按组投递中断 | `managedProcessGroupAttr`：Windows `CREATE_NEW_PROCESS_GROUP`，非 Windows `Setpgid` |
| 没有"用例工作空间"，真实进程只能落在仓库或产物目录 | 新增工作空间根与用例工作空间；分配上下文下发给提供者，`--workspace` 下发给 worker |
| 资源目录埋在产物目录里（`<run>/resources/<type>/`） | 改为 `<工作空间根>/shard_<type>_<id>/`，与产物目录分离（§9 待同步文档） |
| 报告只看得到指纹，看不到句柄内容 | `resources[].payload` |
| 用例侧没有平台能力可用（探测进程、投递中断） | 导出 `testrunner.ProcessAlive`、`testrunner.InterruptProcess` |
| 「优雅关闭」缺少可读证据 | 用例同时断言退出码 0 与服务输出的关闭标记 |

给 R4.5 的复用要点：

1. 用例包写法：声明与流程 / 核验 / 数据库 / 进程 / 路径约定各一个文件，路径与固定名称集中在
   "路径约定"文件；用例只消费句柄，释放与自证交给提供者。
2. 真实进程资源的写法：声明 `service-process` 并用 `{port}` 占位、`ready_path` 指向真实健康检查；
   用例侧复核健康检查 JSON 而不是只看 200。
3. 取消路径的写法：用 `--selfcheck-cancel-on <case-id>` 复现，断言 `released` / `verified` /
   `residual_check` / 退出码，不靠固定等待。
4. 迁移 `local/scheduler` 前需补齐的上下文能力（`Cleanup`、子测试、`Helper`）不在本阶段范围内，
   仍按 R4.5 计划执行。

---

## 11. 不做的事

- 不迁移 `tests/integration/**` 与 `tests/e2e/cli` 的既有用例（R4.5 / R5）。
- 不扩充资源类型（R2 已交付）与调度语义（R3 已冻结）。
- 不实现前端（R6）。
- 不修改 CI、`Makefile` 与 `docs/`（R8 / R9）。
- 不因为试点通过就宣称「统一测试执行器已完成」。
