# V3 R9：全量验收与 V3 收口

**状态：** 待执行。
**上游文档：** [路线总规划](../ROADMAP.md)、各阶段 README（[R2](../R2/README.md) – [R8](../R8/README.md)）。
**范围：** 按路线总规划的完成标准做全量验收、执行覆盖对账、同步文档、登记已接受的能力回退，并产出
V3 收口文档。不新增能力、不恢复 race 与覆盖率、不改写 unit 用例。

---

## 1. 目标

1. 逐条验证 [ROADMAP §5](../ROADMAP.md#5-完成标准) 的 14 条完成标准，证据可定位。
2. 以 `legacy-cases.manifest.json` 冻结基线做全量覆盖对账，`dropped` 逐条有理由。
3. 同步所有受影响的文档，只描述已落地能力。
4. 把 race 缺失、覆盖率缺失、unit 仍用 `go test`、`t.Parallel()` 语义降级登记为已接受风险。
5. 产出 `.steps/V3-final.md`，并把 ROADMAP 的阶段状态更新为完成。

---

## 2. 输入与事实

- 冻结基线：R0 的 `legacy-cases.manifest.json`（1707 条）与 `case-resource-matrix.json`，
  基线提交 `f5eb3ef`。
- R5/R6 的覆盖记录与 `dropped` 清单；R7 的核验对账表；R8 的 Makefile / CI / 脚本最终清单。
- 迁移后 `_test.go` 只剩 `tests/unit/**` 与 `tests/internal/testkit` 等辅助文件，重新生成清单会
  失去分母；基线不再刷新。
- `docs/testing.md` §1 要求运行前必须获得授权；本阶段的授权范围见 §6。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| 本文 §4.2 / §7 | 验收矩阵的实际结果与逐条证据 |
| `docs/testing.md`、`docs/development.md`、`docs/workspace.md`、`README.md` | 命令、目录、报告与授权规则同步 |
| `.github/scripts/README.md`、`tests/README.md` | 脚本与测试入口说明 |
| `.steps/V3-final.md` | V3 收口文档（对齐 `V2-final.md` 结构） |
| `ROADMAP.md` | 阶段状态与风险登记更新 |

---

## 4. 实施步骤

### 4.1 冻结分母
- `legacy-cases.manifest.json` 保持 R0 生成时的内容，不再重新生成；`make inventory` 只盘点当前
  `_test.go` 面（R8 已调整写目标）。
- 迁移后的现状计数与 1707 的差额必须由 `dropped` 清单与 `tests/unit` 保留面解释，不允许把差额
  归因于「工具口径变化」。

### 4.2 全量验收矩阵
| 范围 | 执行方式 | 期望 |
|---|---|---|
| `tests/unit/**` 1342 条 | `go test ./tests/unit/... -count=1` | 全部通过或带原因的跳过 |
| 集成 18 包 328 条 | `agentflow-test` 对应分组 | 通过或带原因的跳过；无 error |
| 前端 37 条 | `agentflow-test frontend` | 通过或带原因的跳过 |
| 真实设备批次 | `agentflow-test` 真实分组，独立授权 | 通过；未配置设备则跳过并写明原因 |
| 覆盖对账 | `agentflow-test --audit-coverage` | 退出码 0；`dropped` 全部有理由 |
| 零残留 | 每次运行后读 `residual_check` | `passed`；残留即失败 |
| 产物隔离 | 检查两次运行的 `run-id` 目录 | 互不覆盖；失败运行产物完整 |

一次全量 run 即可，真实批次单独一次；两轮之间核对产物目录不被覆盖。

### 4.3 覆盖对账
- 分三部分核对：`tests/unit` 保留面、R5 的 328、R6 的 37。
- 任何缺失条目必须在 R5/R6 记录中找到 `dropped` 理由；找不到即阻断收口。
- 对账输出与结论写入 §7，命令与产物路径一并记录。

### 4.4 文档同步
| 文件 | 需要更新的内容 |
|---|---|
- `docs/testing.md`：统一入口与命令；并发由调度器决定；真实依赖一次判定；`report.json` 与产物布局；
  零残留两级核验；unit 仍用 `go test`；race 与覆盖率缺失；`--audit-coverage`；`TestMain` 职责边界。
- `docs/development.md`：常用命令（`make test`、`test-unit`、`test-cases`、`test-local`、`test-e2e`、
  `test-pi`、`test-ssh`）；移除 `test-race`；产物目录。
- `docs/workspace.md`：新增 `cmd/agentflow-test`、`internal/testrunner`、`internal/caseinventory`、
  `tests/runner`。
- `README.md`：文档索引与命令；V3 状态。
- `.github/scripts/README.md`：R8 之后的脚本清单与替代关系。
- `tests/README.md`：测试入口、目录与产物说明。
- `docs/runbook.md`：仅在排障条目涉及测试执行时更新。
- `docs/cli.md`：不记录 `agentflow-test`：它是开发工具，不是产品 CLI（见 §5 D2）。

文档只写已实现的能力；不写「计划中」「后续支持」。

### 4.5 已接受的能力回退
| 能力 | 结论 | 恢复路径 |
|---|---|---|
| `go test -race` | 不保留（R0 §2.3） | worker 提供 `go build -race` 变体 + `--race` 选择变体 |
| 覆盖率门禁 | 不新增（现状本来没有） | 同上变体 + 报告合并 |
| 集成/E2E 仍用 `go test` | 不允许（路径 C 已迁出） | — |
| unit 仍用 `go test` | 允许（路径 C 的既定结果） | 若要改写需重新立项 |
| `t.Parallel()` 等价语义 | 不保留；并发由调度器决定 | — |

### 4.6 V3 收口文档
`.steps/V3-final.md` 对齐 `V2-final.md` 的结构：产品/工程定位、已交付能力、命令与目录、已接受风险、
遗留事项。只写最终状态，不复述过程；证据不足的条目不写成已完成。

### 4.7 收尾
- 更新 ROADMAP §4 的状态与 §8 的风险登记。
- 各阶段 README 的状态改为实际结论，验收记录保留。
- 不删除 R0 的盘点资产；`.test-artifacts` 与 `bin/` 保持忽略状态。

---

## 5. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | 冻结基线的处置 | 只读保留，不再生成；现状盘点另起输出 |
| D2 | `agentflow-test` 的文档归属 | `docs/testing.md` 与 `docs/development.md`，不进 `docs/cli.md` |
| D3 | 能力回退的登记位置 | 本文 §4.5 + `docs/testing.md` + `V3-final.md` 三处一致 |
| D4 | `V3-final.md` 的结构 | 与 `V2-final.md` 对齐 |
| D5 | 阶段文档状态 | 完成即写结论与证据位置，不改写阶段内决策 |

---

## 6. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `tests/unit` 全量；全部已注册 Case（含 `frontend`）；真实分组一次 |
| 允许的命令 | `go test ./tests/unit/... -count=1`、`make test`、`./bin/agentflow-test --audit-coverage` |
| 允许的 CI 触发 | 经用户确认后触发一次完整 CI |
| 明确不允许 | 修改断言以换取通过；跳过真实分组却声明完成；为通过而缩小选择范围 |
| 理由 | 收口验收必须覆盖全量范围；真实设备单独确认可用性与占用时间 |

---

## 7. 验收条件

1. ROADMAP §5 的 14 条完成标准逐条有证据；无「声明式完成」。
2. 覆盖对账退出码 0；`dropped` 全部有理由并可追溯。
3. 全量运行结论如实呈现：通过、失败、跳过、未执行分开统计。
4. 文档同步完成，且不包含未实现能力；命令可直接执行。
5. 能力回退在三处登记一致。
6. `.steps/V3-final.md` 完成，ROADMAP 状态更新。
7. 无遗留临时脚本、无孤儿用例包、无第二套入口。

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 覆盖分母被重新生成导致「全绿」 | 基线只读；`make inventory` 不覆盖 R0 |
| 真实设备不可用导致收口被无限推迟 | 如实记录未执行并阻塞对应条目，不用替身代替 |
| 文档写成愿景而非现状 | 只写能指向代码/命令的能力；存疑项进遗留 |
| 收口阶段顺手改功能 | 只做验收、文档与状态更新 |
| 跳过原因被吞掉 | 报告与文档都必须列出跳过原因 |

---

## 9. 不做的事

- 不恢复 race / 覆盖率门禁。
- 不改写 `tests/unit` 的用例。
- 不新增测试能力或资源类型。
- 不修改已冻结的 payload 契约与报告字段语义。
- 不顺手优化产品代码。

---

## 10. 收口输出

1. 全量验收结论与产物路径。
2. 覆盖对账结论（1707 分母的归属）。
3. 文档同步清单的完成状态。
4. 已接受风险与遗留事项。
5. `.steps/V3-final.md` 与 ROADMAP 的最终状态。
