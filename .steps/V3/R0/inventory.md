# V3 R0：用例与资源盘点

**盘点基线：** `f5eb3ef955d22c45f4f76c8d3ab001c24760c1ad`
**生成方式：** `make inventory`（`cmd/agentflow-test-inventory`，只读盘点，不运行任何测试）
**机器可读资产：** [legacy-cases.manifest.json](legacy-cases.manifest.json)、
[case-resource-matrix.json](case-resource-matrix.json)

本文件是两份 JSON 的人工视图与口径说明。数字均由工具从当前工作树生成，改动测试后重新执行
`make inventory` 即可刷新；JSON 中的 `baseline_commit` 记录每次生成的基线提交。

---

## 1. 规模基线

| 项 | 数量 | 说明 |
|---|---:|---|
| Go 测试文件 | 246 | 其中 190 个含测试函数，56 个只含 `TestMain` 或辅助夹具 |
| Go 测试包 | 35 | 有测试函数的包；3 个只含辅助代码的目录不进入矩阵 |
| Go 测试函数 | 1670 | `func TestXxx(t *testing.T)` |
| `TestMain` | 35 | 每个测试包一个，全部经 `testkit.Main` |
| `t.Run` 调用 | 167 | 其中字符串字面量子测试名 71 个 |
| 前端 Vitest 文件 | 13 | `lingting-agent-flow-ui/tests/unit/*.test.ts` |
| 前端 Playwright 文件 | 24 | `lingting-agent-flow-ui/tests/e2e/*.spec.ts` |
| 用例条目合计 | 1707 | Go 1670 + 前端 37 |

只含辅助代码的 3 个目录是 `tests/`、`tests/internal/fakepi`、`tests/internal/testkit`：它们没有用例
函数，因而不出现在资源矩阵中。

调用点口径：只统计 `_test.go` 内的调用，接收者限定为 `t` / `b` / `f` / `tb` / `c`，不含注释。
该口径用于"迁移没有静默丢失"的分母。

| 调用点 | 数量 |
|---|---:|
| `t.Fatal*` | 9503 |
| `t.Error*` | 131 |
| `t.Helper()` | 783 |
| `t.Cleanup(...)` | 245 |
| `t.Parallel()` | 1744 |
| `t.Skip*` | 31 |
| `t.TempDir()` | 1 |

### 与路线图审阅口径的对账

审阅文档中的数字来自文本匹配，本工具来自 AST 解析，差异逐项可解释：

| 审阅 | 本工具 | 差异原因 |
|---:|---:|---|
| 1705 测试函数 | 1670 + 35 | 审阅的 `func Test*` 正则把 35 个 `TestMain` 一并算入 |
| 9545 `t.Fatal*` | 9503 | 文本匹配会命中注释里的写法与 `t2.` 等非标准接收者 |
| 262 `t.Error*` | 131 | 文本匹配把 `err.Error()`、`fmt.Errorf` 算成测试断言 |
| 179 `t.Run` | 167 | `t.Run(` 文本会命中 `client.Run(`、`script.Run(` 之类调用 |
| 828 / 249 / 2 | 783 / 245 / 1 | 差值为 `tests/internal/testkit` 辅助文件内的调用，不属于迁移面 |

结论：迁移规模应以本文件的 AST 口径为准，审阅数字用于量级判断仍然成立。

---

## 2. 迁移阶段划分（路径 C）

| 阶段 | 用例 | 范围 |
|---|---:|---|
| `keep-go-test` | 1342 | `tests/unit/**`：157 个文件、纯函数与进程内替身，继续由 `go test` 执行 |
| `R5` | 328 | `tests/integration/**`（52 个含用例的包内文件）与 `tests/e2e/cli`（6 个文件） |
| `R6` | 37 | 前端适配器：Vitest 13 + Playwright 24，一个文件一个用例 |

`R5` 与 `R6` 合计 365 个用例是新体系必须覆盖的迁移面；`keep-go-test` 的 1342 个用例只要求
"继续通过"，不要求改写签名或断言。

---

## 3. 资源类型清单（冻结词汇表）

`case-resource-matrix.json` 的 `resource_types` 是唯一来源，调度器（R3）按它校验声明。默认作用域
表示该类资源的通常归属，实际排布由调度器决定。

| 类型 | 默认作用域 | 含义 |
|---|---|---|
| `docker-daemon` | `run-shared` | 远程 Docker daemon 命令入口（经 `internal/docker`） |
| `remote-container` | `case-exclusive` | 真实远程容器，含 `testkit` 的登记与零残留核验 |
| `remote-workspace` | `case-exclusive` | 远程机器上的临时测试目录 |
| `ssh-host` | `run-shared` | `tests/config/ssh.yml` 指向的真实远程主机 |
| `ssh-session` | `case-exclusive` | 一条真实 SSH 连接及其会话 |
| `local-ssh-server` | `case-exclusive` | 测试进程内启动的 SSH/SFTP 服务端 |
| `pi-cli` | `case-exclusive` | 本机真实 Pi CLI 调用 |
| `sqlite-db` | `case-exclusive` | 用例工作空间内的 SQLite 数据库文件 |
| `local-port` | `case-exclusive` | 用例自己监听的本地端口 |
| `local-http-server` | `case-exclusive` | `httptest` 启动的进程内 HTTP 服务 |
| `child-process` | `case-exclusive` | 用例启动的外部子进程（git、ssh、docker、pi） |
| `service-process` | `case-exclusive` | 被测服务进程（`./cmd/agentflow` 构建产物） |
| `git-worktree` | `case-exclusive` | 用例独占的 Git 仓库或工作区 |
| `node-worker` | `case-exclusive` | Vitest fork 进程（一个文件一个 worker） |
| `frontend-dev-server` | `run-shared` | 前端 Vite dev server（Playwright `webServer`） |
| `browser` | `case-exclusive` | 无头浏览器实例（Playwright project `chromium`） |

外部设备资源（`remote-container` / `remote-workspace` / `docker-daemon` / `ssh-host` / `ssh-session` /
`pi-cli` / `service-process` / `git-worktree`）只在集成、E2E 包或带 `RequireRealTests` 门禁的包内声明。
单元测试里的 Docker、SSH 与 Pi 都是进程内替身，把它们算成外部资源会让矩阵失真。

---

## 4. 依赖矩阵（按测试包）

`sharing` 是初判：`shared-candidate`（未发现不可逆操作，可共享）、`exclusive-required`（命中不可逆
操作线索，必须独享）、`unclassified`（真实依赖门禁下需逐用例确认）。`待确认` 为是的条目在 R2 冻结
接口前必须人工确认。

### 4.1 R5 迁移面（18 个包）

- `tests/e2e/cli` — 65 个用例，`shared-candidate`，无需确认，无真实依赖
  - 资源：local-port、local-ssh-server、sqlite-db
- `tests/integration/local/codesaver` — 12 个用例，`shared-candidate`，无真实依赖
  - 资源：child-process、git-worktree、ssh-session
- `tests/integration/local/environment` — 7 个用例，`exclusive-required`，待确认
  - 资源：sqlite-db、ssh-session；不可逆线索：`RemoveAll(`
- `tests/integration/local/execenv` — 28 个用例，`exclusive-required`，待确认
  - 资源：remote-container、ssh-session；不可逆线索：`ContainerRemove`
- `tests/integration/local/fetcher` — 14 个用例，`shared-candidate`，待确认
  - 资源：child-process、git-worktree、remote-container、sqlite-db、ssh-session
- `tests/integration/local/scheduler` — 41 个用例，`shared-candidate`，无需确认
  - 资源：sqlite-db
- `tests/integration/local/session` — 7 个用例，`shared-candidate`，待确认
  - 资源：无声明（未识别出资源）；不可逆线索：`RemoveAll(`
- `tests/integration/local/source` — 6 个用例，`shared-candidate`，无需确认
  - 资源：child-process、git-worktree、ssh-session
- `tests/integration/local/ssh` — 22 个用例，`shared-candidate`，无需确认
  - 资源：local-port、local-ssh-server、ssh-session
- `tests/integration/local/validation` — 11 个用例，`shared-candidate`，无需确认
  - 资源：local-port、local-ssh-server、sqlite-db
- `tests/integration/local/workflow` — 81 个用例，`exclusive-required`，待确认
  - 资源：child-process、git-worktree、remote-container、sqlite-db、ssh-session
  - 不可逆线索：`Shutdown(`
- `tests/integration/pi` — 2 个用例，`unclassified`，待确认
  - 资源：pi-cli；真实依赖：pi
- `tests/integration/remote/agent` — 2 个用例，`exclusive-required`，待确认
  - 资源：docker-daemon、git-worktree、pi-cli、remote-container、remote-workspace、ssh-host、ssh-session
  - 真实依赖：pi、ssh、docker；不可逆线索：`RemoveAll(`
- `tests/integration/remote/codesaver` — 2 个用例，`unclassified`，待确认
  - 资源：docker-daemon、git-worktree、remote-container、sqlite-db、ssh-host、ssh-session
  - 真实依赖：ssh、docker
- `tests/integration/remote/docker` — 6 个用例，`unclassified`，待确认
  - 资源：docker-daemon、remote-container、remote-workspace、sqlite-db、ssh-host、ssh-session
  - 真实依赖：ssh、docker
- `tests/integration/remote/planning` — 9 个用例，`exclusive-required`，待确认
  - 资源：child-process、docker-daemon、local-port、remote-container、remote-workspace、
    service-process、sqlite-db、ssh-host、ssh-session
  - 真实依赖：ssh、docker；不可逆线索：`docker rm `
- `tests/integration/remote/workflow` — 7 个用例，`exclusive-required`，待确认（包内固定串行）
  - 资源：docker-daemon、pi-cli、remote-container、remote-workspace、sqlite-db、ssh-host、ssh-session
  - 真实依赖：pi、ssh、docker；不可逆线索：`RemoveAll(`
- `tests/integration/ssh` — 6 个用例，`unclassified`，待确认
  - 资源：remote-workspace、ssh-host、ssh-session；真实依赖：ssh

### 4.2 keep-go-test 面（17 个包）

这些包在路径 C 下仍由 `go test` 执行，矩阵条目不进入调度器，只作为资源事实记录。

| 包 | 用例 | 资源 |
|---|---:|---|
| `tests/unit/agent` | 35 | — |
| `tests/unit/agent/pi` | 9 | — |
| `tests/unit/app` | 406 | local-port,local-ssh-server,sqlite-db |
| `tests/unit/app/planningagent` | 9 | sqlite-db |
| `tests/unit/app/planninganalysis` | 24 | sqlite-db |
| `tests/unit/cigate` | 9 | — |
| `tests/unit/container` | 29 | — |
| `tests/unit/domain` | 249 | — |
| `tests/unit/environment` | 11 | — |
| `tests/unit/execenv` | 20 | — |
| `tests/unit/httpapi` | 241 | local-http-server,sqlite-db |
| `tests/unit/image` | 10 | — |
| `tests/unit/lifecycle` | 6 | — |
| `tests/unit/server` | 13 | sqlite-db |
| `tests/unit/source` | 21 | — |
| `tests/unit/store` | 248 | sqlite-db |
| `tests/unit/testkit` | 2 | — |

12 个待确认包全部落在 R5：7 个带真实依赖门禁（`pi` / `ssh` / `docker`），5 个是本机集成包
（`local/environment`、`local/execenv`、`local/fetcher`、`local/session`、`local/workflow`）。

---

## 5. 破坏性操作清单

识别规则只保留会作用于共享资源的操作：容器与镜像删除、远程目录清理、服务关闭。通用
`Stop()` / `Kill()` / `os.Remove` 会命中进程内的生命周期替身，不纳入。命中且包内确有资源时，
`sharing` 直接判定为 `exclusive-required`。

| 包 | 证据 | sharing |
|---|---|---|
| `tests/integration/local/environment` | `RemoveAll(` | exclusive-required |
| `tests/integration/local/execenv` | `ContainerRemove` | exclusive-required |
| `tests/integration/local/session` | `RemoveAll(` | shared-candidate（无资源声明，待确认） |
| `tests/integration/local/workflow` | `Shutdown(` | exclusive-required |
| `tests/integration/remote/agent` | `RemoveAll(` | exclusive-required |
| `tests/integration/remote/planning` | `docker rm ` | exclusive-required |
| `tests/integration/remote/workflow` | `RemoveAll(` | exclusive-required |
| `tests/unit/app` | `RemoveAll(`、`docker rm ` | shared-candidate（替身文本，不参与调度） |

`tests/unit/app` 的命中来自受控替身与夹具字符串，属于文本证据的已知噪声；该包在路径 C 下仍由
`go test` 执行，不进入调度器。

---

## 6. 固定串行包

| 包 | 声明 |
|---|---|
| `tests/integration/remote/workflow` | 包注释声明固定串行（共用同一台真实远程 Docker，并行构建会改写同一镜像标签） |

迁移后该声明必须转成调度器可读的约束：7 个用例的 `remote-container` 与镜像标签互斥，不能并行。

---

## 7. 疑似依赖并行时序（P1-4）

`t.Parallel()` 一律映射为"由调度器决定是否并行"，下列文件因包级一次性初始化可能在迁移后改变行为，
需要在 R5 逐一判定：

| 文件 | 证据 |
|---|---|
| `tests/integration/local/scheduler/concurrency_acceptance_test.go` | `sync.Once` |
| `tests/integration/remote/planning/ssh_teardown_test.go` | `sync.Once` |
| `tests/integration/remote/workflow/acceptance_m6_test.go` | `sync.Once` |
| `tests/unit/app/planning_runtime_fixture_test.go` | `sync.Once` |
| `tests/unit/app/requirement_delete_test.go` | `sync.Once` |
| `tests/unit/app/planningagent/planning_agent_fixture_test.go` | `sync.Once` |

未发现用例内写死的本地端口；唯一的固定端口 `26390` 属于前端 dev server 配置，不是测试断言。

---

## 8. 前端用例

- 24 个 Playwright spec 全部使用受控后端（`page.route` 或 `tests/e2e` 下的 helper），不依赖 Go 服务、
  Docker、SSH 或 Pi；资源是共享的 `frontend-dev-server` 加每 worker 一个 `browser`。
- 13 个 Vitest 文件是纯函数测试，并发单位是 fork 进程（`node-worker`）。
- 两者的 `NeedsReview` 都是"P1-5：共享 dev server 的写操作策略需在 R6 确认"，而不是发现了真实写入。

---

## 9. 复现与核对

```bash
make inventory                     # 重新生成两份 JSON 并打印摘要
go run ./cmd/agentflow-test-inventory -out .steps/V3/R0
```

工具位置：`cmd/agentflow-test-inventory`（入口）、`internal/caseinventory`（解析与规则）。枚举只用
`git ls-files`，因此不会进入 `node_modules`、`bin`、`.test-artifacts` 等工作区噪声目录。

---

## 10. 已知局限

- **粒度到包**：资源证据按测试包聚合（同包的辅助文件与用例共享资源），同包内不同用例的资源差异
  尚未展开，需要 R2 按用例细化后回填。
- **文本证据**：破坏性操作与固定端口用文本匹配识别，会命中注释与替身字符串；命中的条目已标记
  `needs_review`，不作为调度依据。
- **子测试语义**：只记录字符串字面量子测试名，表驱动用例的子测试名来自变量，未逐个展开。
- **构建标签**：不解析 `//go:build` 条件，所有已跟踪文件都按参与编译处理。
- **不校验执行结果**：本工具不运行测试，报告里不出现任何通过/失败结论。
