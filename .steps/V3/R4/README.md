# V3 R4：真实服务进程试点（`server.shutdown`）

**状态：** 待执行。
**上游文档：** [路线总规划](../ROADMAP.md)、[R2 资源管理器](../R2/README.md)、
[R3 并发调度与取消](../R3/README.md)。
**范围：** 用一条真实用例打通「启动真实服务进程 → 就绪 → 停止 → 资源释放 → 零残留 → 统一报告」。
不批量迁移用例（R4.5 / R5）、不做前端（R6）、不改 `Makefile` / CI / `docs/`。

---

## 1. 目标

R1–R3 验证的都是自检用例与本地端口。本阶段第一次把真实产品进程放进新体系，检验 R2 的
`service-process` 提供者、R3 的取消路径与零残留核验是否成立，并以此确定后续迁移的用例组织方式。

验收对象是一条 Case（建议 ID `pilot/server-shutdown`）：
- 启动 `bin/agentflow server`，使用工作空间内的绝对路径配置与独立数据库文件；
- 等待 `/api/v1/health` 就绪，健康检查通过前不算启动成功；
- 触发服务停止，等待进程退出与端口释放；
- 断言服务退出后数据库可重新打开、端口可重新绑定、无遗留子进程；
- 报告里能看到 `service-process` 的句柄、释放结论与终检结论。

---

## 2. 输入与事实

- 服务入口：`cmd/agentflow` → `agentflow server`，参数支持 `--config`、`--address`、`--require-web-ui`；
  配置项包含 `server.shutdown_timeout_seconds`（`internal/cli/server.go`）。
- 停止信号：`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`
  （`cmd/agentflow/main.go`）。
- 健康检查：`/api/v1/health`（`internal/httpapi/routes.go`）。
- 测试工作空间必须落在 `os.TempDir()` 下，路径使用绝对路径，数据库不写进仓库
  （[docs/testing.md](../../../docs/testing.md) §4）。
- 测试产物不删除；「零残留」只针对进程、端口、容器、远程目录与会话，不针对工作空间与产物。
- 真实依赖判定必须由编排进程下发，worker 不读环境变量（R1 §6）。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `tests/runner/pilot` | 试点 Case 与必要的辅助代码（非 `_test.go`，由 registry 导入） |
| `tests/runner/registry` | 追加一行导入 |
| `internal/testrunner` | 按试点暴露的问题修补 `service-process` 提供者与终检，不改已冻结语义 |
| 本文更新 | 停止方式、时序、退出码与三连跑记录的最终结论 |

本阶段确定的用例包组织方式（目录层级、辅助代码位置、资源声明写法）作为 R4.5 / R5 的模板。

---

## 4. 实施步骤

### 4.1 准备运行环境
- 构建被测二进制（`go build -o bin/agentflow ./cmd/agentflow`），构建产物按内容指纹登记。
- 生成工作空间：配置、数据库、Session、日志目录全部落在用例产物目录内，使用绝对路径。
- 监听地址取一条 `local-port` 句柄，避免固定端口冲突。

### 4.2 启动与就绪
- 通过 `service-process` 提供者启动进程，句柄记录 `pid`、`base_url`、`binary` 与指纹。
- 轮询 `/api/v1/health`，带超时与明确失败原因；就绪前任何断言都不执行。
- 启动日志写入该 Case 的 `artifacts/`，不从 worker 的 stdout 反推。

### 4.3 停止
优先路径：向服务进程投递中断信号，验证优雅退出与 `shutdown_timeout_seconds` 生效。
- Windows 下子进程以独立进程组启动，使用 `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)`。
- 若本机无法可靠投递中断信号，退回「编排进程取消 → 提供者停止子进程」路径，并把它记录为已知差异。

禁止路径：用 `taskkill /F` 冒充优雅停止；把「进程被杀」写成「优雅关闭通过」。

### 4.4 关闭后核验
- 进程退出码符合预期，进程消失（不只依赖 `Wait` 返回）。
- 端口可重新绑定；数据库文件可重新打开且无未完成迁移；WAL/SHM 状态可解释。
- 提供者释放后可自证：无遗留子进程、无遗留监听端口。
- 运行级终检结论为 `passed`；任何一项无法确认都判失败。

### 4.5 取消路径
- 在服务运行中取消整次运行：worker 被终止，提供者负责停止服务进程，终检仍执行。
- 断言：无遗留 `agentflow` 进程、无遗留端口、产物保留、退出码非零。
- 与 §4.3 的优雅停止分开记录，两者不得互相替代。

### 4.6 稳定性
同一试点连续运行三次，结论一致；任何一次出现超时或偶发失败都必须查清原因再进入 R4.5。

---

## 5. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | 用例包组织方式 | `tests/runner/<layer>/<pkg>`，辅助代码同包，非 `_test.go` |
| D2 | 停止方式 | 优先中断信号；不可行时退回提供者停止，并记录差异 |
| D3 | 就绪判定 | `/api/v1/health` 返回成功，禁止用固定 sleep |
| D4 | 服务进程失败时的结论 | `error`（原因写清是启动失败、未就绪还是停止后仍存活） |
| D5 | 工作空间保留策略 | 一律保留；「零残留」不覆盖工作空间与产物 |

---

## 6. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `pilot`（仅 `pilot/server-shutdown`） |
| 允许的命令 | 见下方命令块 |
| 明确不允许 | `tests/unit`、`tests/integration`、`tests/e2e` 的 `go test`；真实 Pi / SSH / 远程 Docker；前端命令 |
| 理由 | 试点只依赖本机服务进程；真实外部设备留到 R4.5 / R5 单独授权 |

```bash
go build -o bin/agentflow ./cmd/agentflow
./bin/agentflow-test --artifact-dir .test-artifacts pilot
# 只读核对：tasklist / netstat；不终止非本运行创建的进程
```

---

## 7. 验收条件

1. 试点 Case 连续三次运行结论一致，报告齐全。
2. 启动前就绪判定有效：服务未就绪时用例失败且失败原因明确。
3. 优雅停止后：进程消失、端口释放、数据库可重开、终检 `passed`。
4. 取消路径：无遗留进程、无遗留端口、资源已释放、终检仍执行、退出码非零。
5. 报告包含 `service-process` 句柄、释放结论、终检结论与产物路径。
6. 用例组织方式与资源声明写法可供 R4.5 直接复用。
7. `go vet ./...` 与 `gofmt -l` 无新增问题（不运行测试）。

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| Windows 无法可靠向子进程投递中断 | 独立进程组 + `CTRL_BREAK_EVENT`；不可行则退回提供者停止并明确记录 |
| 服务停止时间超过默认超时 | 使用配置的 `shutdown_timeout_seconds`，超时判失败并保留现场 |
| 数据库仍被占用导致「已关闭」误判 | 关闭后重新打开同一路径，以真实文件锁与迁移状态为准 |
| 遗留 `agentflow` 进程污染后续运行 | 每轮开始前检查残留，发现即失败并指出 PID |
| 试点通过掩盖真实用例的差异 | 试点只证明机制，R4.5 必须用既有集成用例再验证 |

---

## 9. 不做的事

- 不迁移 `tests/integration/**` 与 `tests/e2e/cli` 的既有用例（R4.5 / R5）。
- 不扩充资源提供者（R2 已交付）。
- 不实现前端（R6）。
- 不修改 CI、`Makefile` 与文档（R8 / R9）。
- 不因为试点通过就宣称「统一测试执行器已完成」。

---

## 10. 输出给 R4.5

1. 用例包组织方式与 registry 接入方式。
2. 真实进程资源的就绪、停止、自证写法。
3. 取消路径的实测时序与残留检查方法。
4. 试点暴露的 R2 / R3 缺陷清单与已修补项。
