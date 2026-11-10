# V3 R1：执行器接口与可运行骨架

**状态：** 完成。设计已冻结在代码中，自检用例可执行。
**上游文档：** [R0 盘点与决策](../R0/README.md)、[路线图审阅](../TEST-RUNNER-REFACTOR-REVIEW.md)
**范围：** 冻结进程模型与 Case / Resource / Report 接口，交付能跑通多条自检用例并产出统一报告
的最小实现。不迁移 `tests/unit`，不实现真实依赖资源，不做并发调度。

---

## 1. 交付物

| 位置 | 内容 |
|---|---|
| `cmd/agentflow-test` | 命令行入口：编排、`--list`、worker 模式 `--exec-case` |
| `internal/testrunner` | Case 注册表、执行上下文、资源声明与句柄、worker 协议、编排、统一报告 |
| `tests/runner/selfcheck` | 6 条自检用例：通过、跳过、资源、失败、panic、超时 |
| `tests/runner/registry` | 用例包聚合点，迁移期新增用例包只在这里追加导入 |

本阶段没有改动 `tests/unit`、`tests/integration`、`tests/e2e` 的任何用例，也没有改动
`Makefile`、CI 与 `docs/`。

---

## 2. 冻结的进程模型（对应 R0 P0-2）

```text
agentflow-test（编排进程，持资源管理器与调度器）
└── worker 子进程（同一二进制 --exec-case <id>）
    ├── 独占 stdout/stderr → cases/<case-id>/{stdout,stderr}.log
    ├── 包级全局状态隔离
    ├── panic / os.Exit 只影响该 Case
    └── 结果写入 cases/<case-id>/result.json
```

编排进程不执行用例主体：它只做选择、资源分配、启动 worker、读回结果与汇总。worker 的参数
是全部输入（不使用环境变量传参）：

```text
agentflow-test --exec-case <id> --result-file <path> --artifact-dir <path>
               --timeout <dur> --real-tests-grant allowed|denied
               --real-tests-reason <text> [--handles <path>]
```

`--exec-case` 是内部入口，不作为人工日常命令。

**结论来源。** worker 正常结束时以结果文件为准（退出码 0=通过/跳过，1=失败/panic/超时/
异常）；结果文件缺失或无法解析时，编排进程按退出码与超时观测如实给出 `error`/`timeout`，
不会把缺失当成通过。

---

## 3. Case 与资源声明的 API 形态

```go
testrunner.Register(testrunner.Case{
    ID:      "selfcheck/resources",          // 必须形如 <group>/<name>，字符集受限
    Timeout: time.Second,                    // 零值用运行级默认值（默认 10m）
    Parallel: false,                         // R3 才读取；R1 串行执行
    Resources: []testrunner.ResourceDecl{
        {Name: "shared-port", Type: testrunner.ResourceLocalPort, Scope: testrunner.ScopeRunShared},
        {Name: "own-port", Type: testrunner.ResourceLocalPort, Scope: testrunner.ScopeCaseExclusive},
    },
    Run: func(c *testrunner.Context) { /* 断言、日志、跳过 */ },
})
```

`Context` 是 `testing.T` 的最小替代集合：`Logf` / `Warnf` / `Errorf` / `Fatalf` / `Skipf`、
`Failed` / `Skipped` / `Warnings`、`TempDir`、`ArtifactDir`、`Resource` / `Resources`、
`RealTestsAllowed`、`Context` / `Deadline`。`Fatalf` 与 `Skipf` 用内部信号 panic 立即结束
用例主体，worker 统一 recover 并转成结论。

注册表在 init 阶段收集声明；校验失败只记录错误，由 `Cases()` 一次性报告，不会 panic，也
不会覆盖同 ID 的既有用例。

**资源类型**沿用 R0 冻结词汇表（16 种，见 `case-resource-matrix.json`）。R1 只实现 `local-port`
的提供者，其余类型在分配阶段直接失败并给出 "R2 补齐" 的错误，不会退化成空句柄。

---

## 4. 跨进程资源句柄格式

句柄文件由编排进程写在 `cases/<case-id>/handles.json`，worker 只读：

```json
{
  "schema_version": "agentflow-case-handles/1",
  "run_id": "20261110t081713z-fcde",
  "case_id": "selfcheck/resources",
  "handles": [
    {"name": "shared-port", "type": "local-port", "scope": "run-shared",
     "payload": {"address": "127.0.0.1:54797"}},
    {"name": "own-port", "type": "local-port", "scope": "case-exclusive",
     "payload": {"address": "127.0.0.1:54798"}}
  ]
}
```

- `payload` 只放可序列化的自描述键；`fingerprint` 预留给按内容寻址的资源（镜像、二进制）。
- R1 只冻结外壳与校验规则；payload 键名在 R2 按类型冻结，当前已实现的只有
  `local-port.address`。
- 版本号不等于 `agentflow-case-handles/1` 时直接失败，不尝试兼容。
- 句柄缺失、类型不符、作用域不符、句柄重复 → worker 结论为 `error`，绝不降级为通过；
  多余句柄忽略并记录为 warning。
- 声明了资源却没有句柄文件 → 同样是 `error`。

---

## 5. 产物布局与统一报告

```text
<artifact-root>/<run-id>/
├── report.json
└── cases/<group>__<name>/
    ├── stdout.log       # worker stdout（用例日志）
    ├── stderr.log       # worker stderr（FAIL 行与 panic 之外的输出）
    ├── result.json      # worker 结论
    ├── handles.json     # 资源句柄（仅声明了资源的用例）
    └── artifacts/       # 用例自写产物（Context.ArtifactDir）
```

默认 `--artifact-dir=.test-artifacts`（已在 `.gitignore` 中）。`run-id` 形如
`20261110t081713z-fcde`，历史运行目录不会被覆盖。

`report.json`（`agentflow-test-report/1`）必备字段：

| 字段 | 含义 |
|---|---|
| `run_id` / `artifact_dir` / `started_at` / `finished_at` / `duration_ms` | 运行标识与耗时 |
| `selection` / `default_timeout_ms` | 本次选择条件与默认超时 |
| `real_tests{allowed,mode,reason}` | 一次性的真实依赖授权结论 |
| `totals{cases,passed,failed,skipped,panics,timeouts,errors}` | 跳过单列，不计入通过 |
| `cases[].{id,group,status,message,skip_reason,stack_trace,exit_code,duration_ms}` | 单条结论 |
| `cases[].timings{worker_spawn_at,worker_entry_at,case_start_at,case_end_at}` | worker 启动开销观测 |
| `cases[].resources[].{name,type,scope,fingerprint,acquired,released,release_error}` | 用例级资源结论 |
| `cases[].artifacts{case_dir,stdout,stderr,result,artifact_dir}` | 产物位置 |
| `resources[]` | 运行级共享资源的释放结论 |
| `residual_check{status,reason}` | 零残留终检；R1 固定为 `not-run`（R2/R7 提供） |
| `exit_code` | 与进程退出码一致 |

共享资源的 `released` 结论记在运行级 `resources`；用例级条目里 `run-shared` 的
`released` 为 `false` 只表示"释放不在用例结束时发生"。

退出码：`0` 全部通过或跳过，`1` 存在失败/panic/超时/异常/资源释放失败，`2` 用法错误。
选择语法（R1）：分组名、精确 ID、或以 `...` 结尾的前缀；无选择条件表示全部已注册用例。

---

## 6. 真实依赖授权入口（与既有门禁合并）

- 编排进程只判定一次：`--real-tests=auto|on|off`。`auto` 复用与
  `tests/internal/testkit` 相同的环境变量（`GITHUB_ACTIONS`、`AGENTFLOW_REAL_TESTS_GRANTED`、
  `AGENTFLOW_REAL_TESTS_GRANT_REASON`），CI 中要求授权标记精确等于 `true`；`on` 表示调用方
  已完成授权判定；`off` 一律拒绝。
- 结论以 `--real-tests-grant allowed|denied` 下发给 worker，worker **不读取环境变量**，
  也不自行判定；用例通过 `Context.RealTestsAllowed()` 读取结论与原因。
- 因此不存在第二套开关：`check-real-test-authorization.sh` 的职责由这里的 `auto` 承接，
  `prepare-real-tests-config.py` 与报告汇总脚本在 R5/R8 收敛时再替换。

---

## 7. Windows 进程树终止

`worker → 用例子进程 → Pi CLI / Docker CLI` 是一条多层子进程链，`exec.Cmd.Kill` 只杀直接
子进程：

- Windows：`taskkill /T /F /PID <pid>`；退出码 128 或输出含 `not found` 视为进程已退出。
- 非 Windows：worker 以独立进程组启动（`Setpgid`），用 `kill(-pid, SIGKILL)` 终止整组。

编排进程在 `用例超时 + 宽限期（默认 10s）` 仍未回收 worker 时终止进程树；回收失败会让该
用例结论变成 `error` 而不是静默通过。用例自身超时先由 worker 的 deadline 接管（R1 自检的
超时用例即走这条路径），进程树终止是兜底路径。

---

## 8. worker 启动开销基线

环境：Windows 11 + Go 1.25.7，`bin/agentflow-test.exe`，20 次 `selfcheck/pass` 单用例运行
（2026-11-10，产物见 `.test-artifacts/baseline/`）。

| 观测 | 最小 | 中位 | 最大 |
|---|---:|---:|---:|
| 编排启动 → worker 入口 | 18.9ms | 19.6ms | 21.4ms |
| worker 入口 → 用例开始 | 0.0ms | 0.0ms | 0.0ms |
| 用例主体 | 0.5ms | 0.5ms | 1.1ms |
| 单用例 wall（含启停与结果回读） | 28ms | 29ms | 31ms |

结论：一次 worker 启动约 20ms，单用例总开销约 30ms。R3 的并发预算按"每 Case 一份 20ms
启动开销 + 用例自身耗时"计算；R6 的 Playwright/Vitest 冷启动不适用这个数字，需要单独测量。

---

## 9. 验收记录

本阶段只运行新自检用例（`selfcheck` 与 `selfcheck-fault` 两组），未运行任何既有 `go test`
用例，未触碰真实 Pi / SSH / Docker 设备。

```bash
go build -o bin/agentflow-test.exe ./cmd/agentflow-test
./bin/agentflow-test.exe --artifact-dir .test-artifacts selfcheck        # 退出码 0
./bin/agentflow-test.exe --artifact-dir .test-artifacts selfcheck-fault  # 退出码 1
```

| 结论 | 用例 | 验证点 |
|---|---|---|
| `passed` | `selfcheck/pass` | 独立 worker、日志分离、`TempDir`、退出码 0 |
| `passed` | `selfcheck/resources` | 声明 → 分配 → 句柄序列化 → worker 校验 → 端口可连 → 释放 |
| `skipped` | `selfcheck/skip` | 跳过带原因，且不计入通过 |
| `failed` | `selfcheck-fault/fail` | `Errorf` 结论与非零退出码 |
| `panic` | `selfcheck-fault/panic` | panic 只终止 worker，编排进程照常出报告 |
| `timeout` | `selfcheck-fault/timeout` | 用例级超时与 `timeout` 结论 |

`selfcheck-fault` 是故意失败的组，只用于验证失败路径，不作为常规验收集合。

---

## 10. R0 输入清单对账

| R0 第 8 节输入 | R1 结论 |
|---|---|
| worker 边界与 `--exec-case` 入口签名 | 已冻结（第 2 节）；Case / Resource 声明见第 3、4 节 |
| 跨进程句柄序列化格式 | 已冻结（第 4 节）：外壳 + payload + 校验规则与失败语义 |
| worker 启动开销基线 | 已测量（第 8 节），20 次采样 |
| 每 Case 独立 stdout/stderr 与产物布局 | 已冻结并落地（第 5 节） |
| 真实依赖授权入口与 `RequireRealTests` 的关系 | 一次判定 + 下发结论，worker 不再自行判定（第 6 节） |
| 统一报告必备字段 | 已冻结（第 5 节） |
| Windows 进程树终止方式 | `taskkill /T /F`，非 Windows 用进程组（第 7 节） |

---

## 11. 留给 R2 的输入

1. 12 个集成测试包的共享/独享结论（R0 4.1 的 `待确认` 项），决定 `run-shared` 的实际排布。
2. 16 种资源类型的 payload 键名与 `fingerprint` 语义；R1 只实现了 `local-port`。
3. "构建产物缓存"与"共享资源"的分节（P1-6）：镜像构建属写入语义，不能与 `Destructive + Shared`
   禁令冲突。
4. 资源级自证（容器 `docker ps -a`、远程目录、SSH 会话计数）与运行级终检，替换
   `residual_check.status=not-run`。
5. 真实依赖授权入口与 CI 工作流脚本的合并顺序（`check-real-test-authorization.sh` 等）。
6. 并发调度（R3）需要 `Case.Parallel` 之外的信息：资源独占键、包级串行声明（R0 第 6 节）、
   worker 数量与资源容量。

## 12. 未做的事

- 没有并发调度：R1 串行执行，`Parallel` 字段只冻结接口。
- 没有 `go test` 适配器，`tests/unit` 的 1342 条用例仍由 `go test` 执行（路径 C）。
- 没有覆盖对账命令 `--audit-coverage`，`legacy-cases.manifest.json` 尚未被读取。
- 没有取消与信号处理（R3）、没有零残留核验（R2/R7）、没有前端适配器（R6）。
- 没有修改 `Makefile`、CI、`docs/`；文档同步按 R8/R9 统一处理。
