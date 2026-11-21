# V4 R2：并发额度内核（包级上限 + 命名池）

**状态：** 未开始。
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
| 既有自检不退化 | `./bin/agentflow-test concurrencycheck --parallel 8`（需授权） | 17 条全绿；`max_in_flight` 不被压到 3 |
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
