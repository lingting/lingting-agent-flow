# V4 R2：并发额度内核（包级上限 + 命名池）

**状态：** 已完成（2026-11-21）。并发额度内核、报告 v2、CLI 与自检用例均已落地，验收记录见 §9。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R0 冻结决策](../R0/README.md)（D-04 / D-05 / D-07 / D-08）、
[R1 清理收尾](../R1/README.md)。
**下游关系：** 只改调度决策与报告，不改 worker 进程模型；worker 池与批量执行在 [R3](../R3/README.md)。

---

## 1. 目标

在现有 `--parallel`、资源容量与独占键之上，新增两类额度：

- **包级上限**：同一 `Case.Package` 同时在跑的上限（键由 `Package` 自动得到）。
- **命名并发池**：多个包共享同一份真实约束（例如同一模型供应商），键由用例显式声明。

两者都必须满足 work-conserving：不制造空闲槽位、不制造队头阻塞。

## 2. 冻结口径（来自 R0）

- **D-04 CLI 优先，可临时放大**：运行级 `--package-limit` / `--pool` 存在时以运行级值为生效值，可以大于
  用例声明；报告与日志记录 `declared_limit` 与 `limit`（生效值）并 `WARN` 差异。用例之间的同 key 声明
  冲突仍在注册期硬失败。池名拼错是参数错误；负数或非整数退出码 2。
- **D-05**：I5（可派发就派发）与 I6（被阻塞不挡后面）。
- **D-07/D-08**：报告 `schema_version` 升到 `agentflow-test-report/2`，新增额度分节。

## 3. 设计要点

```go
// Case 新增字段（V4 R2）
Concurrency ConcurrencyDecl

type ConcurrencyDecl struct {
    PackageLimit int            // 0 表示不声明；同包非零声明必须一致
    Pools        map[string]int // 0 表示"参与该池但容量由别处声明"
}

type ConcurrencyPolicy struct {
    Parallel     int
    PackageLimit int            // 运行级默认上限；0 表示不限
    Pools        map[string]int // 运行级池容量
}
```

生效上限解析（纯函数，便于自检）：

```text
effectivePackageLimit(p) = 运行级 PackageLimit（若 >0）否则 Case.PackageLimit（若 >0）否则不限
effectivePoolLimit(k)    = 运行级 Pools[k]（若 >0）否则用例声明 Pools[k]（若 >0）否则预检失败
```

预检错误（全部在派出第一条 Case 之前）：

| 场景 | 结果 |
|---|---|
| 同包两个不同的非零 `PackageLimit` | 注册期失败 |
| 同池两个不同的非零容量 | 注册期失败 |
| 用例引用某池，但运行级与用例都没给容量 | 预检失败：`池 <name> 没有容量声明` |
| `--pool` 的池名在注册表里不存在 | 参数错误（退出码 2） |
| 负数 / 非整数 | 参数错误（退出码 2） |
| 单条 Case 所需额度超过生效上限 | 预检失败（不重试、不降级） |

## 4. 实现落点

| 文件 | 改动 |
|---|---|
| `internal/testrunner/limit.go`（新增） | 声明类型、策略类型、生效上限解析、冲突校验（纯函数） |
| `internal/testrunner/case.go` | `Case.Concurrency` 字段 + `Validate` 校验（非负、池名字符集） |
| `internal/testrunner/scheduler.go` | `caseRequirement` 增加 `pkg` / `pkgLimit` / `pools`；状态增加 `pkgInFlight`、`poolInFlight`；`admits`/`reserve`/`release` 扩展；`reservation` 记录本次占用的包与池 |
| `internal/testrunner/orchestrator.go` | `RunOptions` 增加 `Policy`；预检与 I7 判定 |
| `internal/testrunner/report.go` | `schema_version` 升 v2；`concurrency` 分节含 `package` 与 `quotas` 条目 |
| `cmd/agentflow-test/main.go` | `--package-limit`、`--pool name=limit`（可重复） |
| `.github/scripts/run-cases.sh`、`Makefile` | 透传 `PACKAGE_LIMIT` / `POOLS`，不引入分组知识 |
| `tests/runner/concurrencycheck/` | 新增额度自检 Case（见 §5） |

## 5. 验收探针

| 探针 | 命令 / 产物 | 期望 |
|---|---|---|
| 既有自检不退化 | `./bin/agentflow-test --parallel 8 concurrencycheck`（需授权；选择条件放在参数之后，flag 解析在第一个位置参数处停止） | 17 条全绿；`max_in_flight` 不被压到 3 |
| 包额度生效 | 新增自检 Case | 同包在跑数不超过生效上限 |
| 池额度生效 | 新增自检 Case | 同池在跑数不超过生效上限，池内第 4 条排队但不挡池外 |
| 8/3/5 场景 | 新增自检 Case | 池 3 + 池外 5 同时在飞；池内完成一条立刻补一条 |
| 声明冲突 | 新增自检 Case | 注册期失败并给出两个冲突值 |
| 池名拼错 | `./bin/agentflow-test --pool typo=1` | 退出码 2 |
| 报告字段 | `report.json` | `schema_version=agentflow-test-report/2`，含 `package` 与 `quotas[]` 的 limit/declared_limit/peak/completed/queued_ms_*/undispatched |
| 确定性 | 同一选择集连续两次 `--list` + 派发序列 | 派发序列一致 |

## 6. 授权

- 静态：`gofmt`、`go vet`、`go build`、`--list`。
- 运行：一次 `concurrencycheck` 分组 + 新增额度自检；不跑 local / e2e / frontend / real。

## 7. 风险

| 风险 | 处置 |
|---|---|
| 额度计数泄漏（运行越来越慢直到不派发） | I8 计数守恒 + 自检覆盖 `passed/failed/skipped/panic/timeout/error/取消/worker 启动失败/无结果` 全部路径 |
| 包额度被误用成"变相串行" | 报告必须给出每额度 `peak`；`peak` 长期等于 1 在评审中指出 |
| CLI 放宽导致真实限流被突破 | D-04 已由 owner 决定；报告必须同时记录 `declared_limit` 与生效值，便于事后核对 |

## 8. 不做的事

不改 worker 进程模型、不改资源词汇表、不搬目录、不迁移 unit 用例、不恢复 race/覆盖率。

---

## 9. 验收记录（2026-11-21）

### 9.1 落点

| 文件 | 改动 |
|---|---|
| `internal/testrunner/limit.go`（新增） | `ConcurrencyDecl` / `ConcurrencyPolicy` / `ConcurrencyLimits`、生效上限解析、声明冲突校验、`--pool` 取值解析、`QuotaReport` |
| `internal/testrunner/case.go` | `Case.Concurrency` 字段与声明校验；`Register` 做注册期额度冲突登记 |
| `internal/testrunner/scheduler.go` | `planCase` 增加包与池约束；状态增加 `pkgInFlight` / `poolInFlight` / 额度观测；`admits` 纳入额度判定；排队时长与未派发原因按额度记账 |
| `internal/testrunner/orchestrator.go` | `RunOptions.Policy`；派发前预检；把解析结果交给调度器 |
| `internal/testrunner/report.go` | `schema_version` 升 `agentflow-test-report/2`；`concurrency.package` / `concurrency.quotas` / `concurrency.warnings`；终端摘要增加额度行 |
| `cmd/agentflow-test/main.go` | `--package-limit`、可重复的 `--pool name=limit`；池名拼错、负数、非整数按参数错误退出码 2 |
| `.github/scripts/run-cases.sh`、`Makefile` | 透传 `PACKAGE_LIMIT` / `POOLS`，脚本不认识任何具体包或池 |
| `tests/runner/concurrencycheck/quota.go`（新增） | 包级上限、命名池与队头阻塞、8/3/5、额度声明预检共 31 条自检 Case |
| `internal/testrunner/executable_{windows,other}.go`（新增） | 无扩展名执行器在 Windows 下的可启动别名（见 §9.3），非 Windows 直接返回原路径 |

### 9.2 探针结果

| 探针 | 命令 / 产物 | 结果 |
|---|---|---|
| 既有自检不退化 | `./bin/agentflow-test --parallel 8 concurrencycheck` | 48/48 `passed`；`max_in_flight` 8、`worker_launches` 48、零残留 `passed` |
| 包额度生效 | `report.json` 的 `concurrency.package` | `tests/runner/concurrencycheck/quota`：`limit` 2 / `peak` 2 / `completed` 4 / `queued_ms_total` 2203 |
| 池额度生效 | `report.json` 的 `concurrency.quotas` | `model-provider:vendor-A`：`limit` 3 / `peak` 3 / `completed` 4 / `queued_ms_total` 2289；两条池外用例照常通过（无队头阻塞） |
| 8/3/5 场景 | 同上（12 条池内 + 8 条池外） | `max_in_flight` 8 且 `model-provider:vendor-B` 的 `peak` 3 / `completed` 12 / `queued_ms_total` 17982 |
| 声明冲突 | `concurrencycheck/quota-declaration-check` | 通过：包冲突、池冲突、缺容量均被拦下且给出两个声明值 |
| D-04 运行级覆盖 | `./bin/agentflow-test --parallel 8 --package-limit 1 --pool model-provider:vendor-A=2 --artifact-dir ... concurrencycheck` | 48/48 `passed`；生效值降为 `limit` 1 / 2、`declared_limit` 仍记 2 / 3，`concurrency.warnings` 与终端各有一条 WARN |
| 池名拼错 | `./bin/agentflow-test --pool typo=1` | 退出码 2，错误信息指出池名未被任何用例声明 |
| 报告字段 | `schema_version` | `agentflow-test-report/2`，`package` 与 `quotas` 均含 `limit` / `declared_limit` / `peak` / `completed` / `queued_ms_total` / `queued_ms_max` / `undispatched` |
| 确定性 | 连续两次 `--list` | 输出一致；派发顺序只由 ID 升序与当前占用决定 |
| 静态门禁 | `make check`、`go-test-groups.sh --check` | 通过；`--list` 391 条 Case / 25 个分组，普通门禁 kind 与分组声明无漂移 |

### 9.3 口径变化与遗留

- 自检用例从 17 条增加到 48 条，`--list` 总量由 360 变为 391。R5–R8 登记的 `--list` 目标（498 / 814 / 1712）
  是在 360 基线上算出的，收口时需要按实测顺延，不作为本阶段的偏差。
- `concurrency.quotas` 的排队时长按"用例第一次没被派发的时刻"起算，因此全局槽位满导致的等待也计入
  该用例所属额度：这是 I9"被阻塞过的 Case 计入该额度"的直接口径，不是单纯的池内等待。
- 报告的 worker 池字段（`workers` / `worker_reuse` / `worker_restarts` / `isolated_cases`）属于 D-08 的 v2 契约，
  但只在 [R3](../R3/README.md) 落地，本阶段不写空值冒充已实现。
- 探针命令里选择条件必须写在参数之后：`flag` 在第一个位置参数处停止解析，写成
  `agentflow-test concurrencycheck --parallel 8` 时 `--parallel` 会被当成选择条件，实际用的是默认值。
- 待同步文档（需单独授权）：`docs/testing.md` 的执行器参数与报告字段、`tests/README.md` 的自检分组说明。
- 执行器产物保持 `bin/agentflow-test`（无扩展名），由执行器自己在 Windows 上解决启动路径：Windows 的
  `os/exec` 与 `CreateProcess` 在文件名无扩展名时会补 `.exe`，因此按原路径启动 worker 会命中同目录的旧
  `.exe`（症状是"未注册用例"）或直接找不到文件。`spawnableExecutable` 在本次运行的产物目录里做一个
  同名 `.exe` 别名（优先硬链接，跨卷退化为复制）供 worker 与子进程启动；别名按运行隔离、不会跨运行复用，
  删除是尽力而为（Windows 不能删除仍在运行的镜像的链接，残留只落在本次运行的产物目录里）。
  探针：删除 `bin/agentflow-test.exe` 后 `make test-runner` 只产出无扩展名文件，`--parallel 8 concurrencycheck`
  仍 48/48 通过。
