# V3 R0：盘点结果与决策记录

**状态：** 完成。盘点资产可用 `make inventory` 复现。
**盘点基线：** `f5eb3ef955d22c45f4f76c8d3ab001c24760c1ad`（工作区干净）
**上游文档：** [../TEST-RUNNER-REFACTOR-REVIEW.md](../TEST-RUNNER-REFACTOR-REVIEW.md)
**范围：** 只产出盘点资产与决策记录。不修改产品代码与测试代码，不运行任何测试。

---

## 1. 交付物

| 文件 | 内容 |
|---|---|
| [inventory.md](inventory.md) | 规模基线、迁移阶段、资源类型、依赖矩阵、破坏性操作清单、并行风险清单 |
| [legacy-cases.manifest.json](legacy-cases.manifest.json) | 1707 条用例的冻结清单（覆盖对账基准） |
| [case-resource-matrix.json](case-resource-matrix.json) | 35 个测试包与 37 个前端文件的资源声明（调度器输入） |

生成工具：`cmd/agentflow-test-inventory` → `internal/caseinventory`，枚举只用 `git ls-files` 加 Go
AST 解析，不遍历工作区噪声目录，不执行测试，不连接外部设备。

---

## 2. 已冻结的决策

### 2.1 P0-1：Go 单元测试走路径 C（分层迁移）

- `tests/unit/**` 的 1342 个用例继续由 `go test` 执行，不改写签名、不替换断言。
- `tests/integration/**` 与 `tests/e2e/cli` 的 328 个用例在 R5 迁移到统一 Case 模型。
- 前端 37 个文件在 R6 接适配器。
- 理由：集成与 E2E 本来就是子进程与真实资源驱动，资源声明、互斥与清理核验的收益集中在这一层；
  单元测试改写需要付出八成迁移成本，而并发与资源收益接近零。

### 2.2 P0-2：执行模型为"编排进程 + worker 子进程"

```text
agentflow-test（编排进程）
├── 资源管理器（本进程）：容器、SQLite、服务进程、镜像、SSH
├── 调度器（本进程）
└── worker（子进程，每 Case 或每批次一个）：agentflow-test --exec-case <id>
    ├── 独占 stdout/stderr → cases/<case-id>/{stdout,stderr}.log
    ├── 包级全局状态隔离
    ├── panic / os.Exit 只影响该 Case
    └── 可按需以 -race 变体构建运行
```

跨进程传递资源句柄（容器 ID、端口、数据库路径、远程目录、SSH 端点）需要序列化，这条必须在 R1 的
接口设计里出现，不能等到 R3 才发现。

### 2.3 P0-3：race 检测显式放弃

- 本次重构不保留 `go test -race` 能力，也不保留覆盖率能力（现状本来没有 `-cover` 目标）。
- R8 删除 `Makefile` 的 `test-race`、`.github/scripts/run-go-race-tests.sh` 与 CI 的 go-race Job。
- 恢复路径已经留好但不在本次实现：worker 二进制可提供 `go build -race` 变体，由 `agentflow-test --race`
  选择变体。R9 需把"race 覆盖缺失"登记为已接受的风险。

### 2.4 R0 清单产出：文档 + 可执行 manifest

清单与资源矩阵由工具生成，不靠人工誊抄；后续 `agentflow-test --audit-coverage` 直接读
`legacy-cases.manifest.json` 与当前注册表比对，条目只允许显式标记 `dropped`，不允许消失。

---

## 3. 由决策直接推导的两条结论

### 3.1 P0-4：零残留核验的两个落点

现有 `tests/internal/testkit/finalize.go` 的核验对象是"整个测试进程"，新模型下拆成两级：

1. **资源级核验**：每个资源停止或释放后自证消失（容器 `docker ps -a`、远程目录、SSH 会话计数）。
2. **运行级终检**：编排进程退出前对全部资源做一次快照比对，失败时退出码非零并保留此前所有产物。

核验结论同时进入资源结果与运行结果：资源级失败标记该资源，运行级失败标记整次运行。跳过不等于通过，
未执行必须如实显示为 SKIP。

### 3.2 P0-5：阶段验收的运行授权

每个阶段的说明里写明"本阶段验收允许执行的 Case 集合"，由你在阶段开始时一次性授权；授权范围之外
一律不执行。这样既满足"未经授权不运行测试"，也不至于让退出条件只能靠读代码宣称达成。

R0 本身不需要任何运行授权：全部交付物来自静态盘点。

---

## 4. 完成标准的改写（路径 C 的直接后果）

| 原标准 | 改写后 |
|---|---|
| 9. 不得再用 `go test` 作为测试执行器 | 集成与 E2E 用例不得再以 `go test` 作为执行器；`tests/unit` 仍由 `go test` 执行，其结果由统一报告聚合 |
| 14. 原有测试场景和断言没有因迁移而静默丢失 | 以 `legacy-cases.manifest.json` 的全量覆盖与 `counts` 对账为准，条目消失即失败 |

---

## 5. 非测试门禁与既有脚本归属（P2-4）

保留的非测试门禁：`make fmt`、`fmt-check`、`vet`、`build`、`web-install`、`web-build`、`web-verify`、
`check`（`fmt-check vet build`）。这些都不执行测试，不受本重构影响。

`.github/scripts` 逐文件归属：

| 脚本 | 归属 | 责任阶段 |
|---|---|---|
| `go-test-groups.sh` | 保留：分组声明的唯一来源；`--check` 是静态门禁，继续存在；R5 后需表达"哪些包已迁出 go test" | R5 |
| `run-go-group-tests.sh` | 收敛为只跑 `unit` 组与尚未迁移的包 | R5 |
| `run-go-race-tests.sh` | 删除（race 显式放弃） | R8 |
| `check-real-test-authorization.sh` | 合并进 Runner 的授权入口 | R1 |
| `prepare-real-tests-config.py` | 合并进 Runner 的配置准备入口 | R1 |
| `summarize-real-tests.sh` | 合并进 Runner 的报告汇总 | R1 |
| `summarize-test-skips.sh` | 合并进统一报告的跳过统计 | R1 |
| `collect-test-artifacts.sh` | 合并进统一产物目录 | R1 |
| `run-frontend-tests.sh` | 合并进前端适配器 | R6 |
| `check-go-static.sh` | 保留（非测试门禁） | — |
| `check-frontend-clean.sh` | 保留（非测试门禁） | — |
| `summarize-ci-gate.sh` | 保留，输入需要随 Job 拆分更新 | R8 |
| `build-release-dist.sh` | 保留（发布链路） | — |
| `github-release.sh` | 保留（发布链路） | — |
| `resolve-release-version.sh` | 保留（发布链路） | — |
| `release-artifacts-summary.py` | 保留（发布链路） | — |
| `summarize-release-stages.sh` | 保留（发布链路） | — |
| `verify-release-assets.sh` | 保留（发布链路） | — |

---

## 6. 与 V2 / V3 编号的关系（P2-5）

V2 已收口（[../../V2-final.md](../../V2-final.md)），本路线图是 V2 之后的独立里程碑 V3，R0 是其第一个阶段；
不并入 V2 的阶段编号，也不替换 `.plan/mian.md` 里的产品需求清单。

---

## 7. 遗留待确认（R2 冻结接口前的输入）

12 个集成层测试包需要在 R2 逐个人工确认共享/独享与真实依赖策略，清单与理由见
[inventory.md 第 4 节](inventory.md#4-依赖矩阵按测试包)。其中 7 个带真实依赖门禁（`pi` / `ssh` /
`docker`），5 个是本机集成包（含 `RemoveAll(`、`ContainerRemove`、`Shutdown(` 等不可逆操作线索）。

---

## 8. R1 的输入清单

1. worker 边界与 `--exec-case` 入口签名；Case 与资源声明的 API 形态。
2. 跨进程资源句柄序列化格式（哪些字段、如何校验、缺失时如何失败）。
3. worker 启动开销基线（需要授权运行后才能给出，R1 阶段一并申请）。
4. 每 Case 独立 stdout/stderr 与产物目录布局（`cases/<case-id>/`）。
5. 真实依赖授权入口与 `RequireRealTests` 的关系，避免两套门禁并存。
6. 统一报告的必备字段：包、用例、资源声明、跳过原因、零残留核验结论。
7. Windows 进程树终止方式（`Taskkill /T /F` 或 Job Object），覆盖
   `worker → go test → Pi CLI → Docker CLI` 这条链。

---

## 9. R0 未做的事

- 未运行任何测试，未创建测试文件，未改动测试与产品代码。
- 未设计 Case / Resource / Lease / 调度器的接口签名（R1）。
- 未对 12 个待确认包给出最终共享/独享结论（R2）。
- 未修改文档与 CI：`docs/testing.md`、`docs/development.md`、`README.md` 与工作流清单在 R8/R9 统一同步。
