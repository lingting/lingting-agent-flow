# V3 R7：TestMain 与最终核验收口

**状态：** 待执行。
**上游文档：** [路线总规划](../ROADMAP.md)、[R5 迁移](../R5/README.md)、[R6 前端适配器](../R6/README.md)、
[R2 零残留核验](../R2/README.md)。
**范围：** 收口 `tests/internal/testkit` 的包级最终核验与 `TestMain` 职责，删除迁移后无引用的组件。
不迁移用例（R5/R6）、不改 CI 与文档（R8/R9）。

---

## 1. 目标

R5/R6 删除了已迁移包的 `_test.go` 与 `TestMain`，但 `testkit` 里的核验实现仍在：`finalize.go` 的
`RegisterFinalizer`、`RegisterContainerFinalizer`、远程目录与文件缺失核验，以及 35 个 `TestMain` 曾
承载的收尾职责。本阶段回答：

1. 每个旧核验在 R2 的资源级自证 / 运行级终检里是否有等价落点；
2. 迁移后哪些 `testkit` 组件已无引用，可以删除；
3. 17 个仍由 `go test` 执行的 unit 包保留哪些入口，职责是否只剩日志与工作空间；
4. 真实设备批次的零残留是否真的由新体系承担，而不是继续靠旧 finalizer。

---

## 2. 输入与事实

- 35 个 `TestMain` 全部经 `testkit.Main`；R0 记录其中 18 个包在 R5 迁出，17 个 unit 包保留。
- `testkit/finalize.go` 提供包级最终核验：失败改变退出码，跳过如实记为 SKIP；远程容器核验使用
  真实 `docker ps -a` 清单。
- R2 已实现资源级自证与运行级终检，`residual_check` 进入报告。
- `tests/integration/remote/planning` 显式注册了卷清理终检；该包在 R5 迁出后不得再依赖旧 finalizer。
- `testkit` 还承担 unit 侧的运行根目录、日志、配置解析与 SQLite 打开，这些没有被 R5 替换。
- `docs/testing.md` §4 要求不删除测试工作空间；「零残留」不覆盖工作空间与产物。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `tests/internal/testkit` | 删除无引用组件；保留 unit 侧必要入口并收敛职责 |
| `tests/unit/**` | 仅在有引用变化时更新调用点，不改断言 |
| 本文 §5 | 旧核验 → 新落点对账表（逐项） |
| `internal/testrunner` | 补齐对账表里缺口，不改已冻结语义 |

---

## 4. 实施步骤

### 4.1 建立对账表
逐项列出旧 `TestMain` / `finalize.go` 承担的职责：
- 运行根目录与日志落盘；
- 真实容器零残留核验（含按标签查询）；
- 远程目录与文件缺失核验；
- SSH 会话关闭；
- 卷与镜像清理；
- 失败改变退出码、跳过如实报告。

每项必须落到「资源级自证」「运行级终检」「worker 产物」或「unit 入口保留」四者之一。没有落点的项
在 R7 补齐，不允许「随迁移消失」。

### 4.2 删除迁移后无引用的组件
以编译与引用为判据，而不是凭印象：
- `RegisterFinalizer` / `RegisterContainerFinalizer` 在迁出包不再调用后删除；
- 远程容器、远程目录、文件缺失、known_hosts 等辅助在无引用后删除；
- 删除后 `go vet ./...` 与编译必须通过；不得保留「注释掉待用」。

### 4.3 收敛 unit 侧入口
- 17 个 unit 包的 `TestMain` 保留，但必须确认其职责只剩日志与工作空间准备，不注册真实资源核验。
- `testkit.Main` 的注释与行为只描述 unit 侧用途；不得暗示仍服务集成用例。
- unit 包的日志路径与 runner 的产物路径互不覆盖，两套路径的说明留到 R9 写进文档。

### 4.4 真实批次的零残留复核
- 在授权范围内跑一次真实批次（B3 的范围），核对：运行前快照、运行后快照、`residual_check`、
  容器/远程目录/SSH 会话是否有残留。
- 旧 finalizer 已删除的情况下仍能判定残留，才算收口成功；若只能靠旧实现判定，说明 R2 有缺口，先补 R2。

### 4.5 失败与跳过的语义复核
- 核验失败必须使运行退出码非零；
- 设备未配置导致的跳过必须带原因并计入跳过，不得计为通过；
- 不因删除旧 finalizer 而降低「无法确认即失败」的标准。

---

## 5. 旧核验 → 新落点对账表（执行时填写）

| 旧职责 | 原位置 | 新落点 | 状态 |
|---|---|---|---|
| 运行根目录与日志 | `testkit.Main` / `runRootInternal` | worker 产物目录；unit 保留 `testkit.Main` | 待填 |
| 容器零残留 | `RegisterContainerFinalizer` | `remote-container` 释放后 `docker ps -a` 自证 + 运行级终检 | 待填 |
| 远程目录清理 | `remotedir.go` / finalizer | `remote-workspace` 释放后目录不存在自证 | 待填 |
| 文件缺失核验 | `fileabsence.go` | 资源自检或 Case 断言 | 待填 |
| SSH 会话关闭 | finalizer | `ssh-session` 释放后连接关闭自证 | 待填 |
| 卷/镜像清理 | `remote/planning` 终检 | `remote-container` / 缓存通道 + 终检 | 待填 |
| 失败改变退出码 | finalizer 返回值 | `residual_check.failed` + 退出码 | 待填 |
| 跳过如实报告 | `WARN [SKIP]` | 报告 `skip_reason` | 待填 |

---

## 6. 阶段内需要冻结的决策

| 编号 | 决策 | 建议 |
|---|---|---|
| D1 | `RegisterFinalizer` 是否保留 | 无引用即删除，不留兼容层 |
| D2 | unit 侧 `testkit.Main` 去留 | 保留，职责限定为日志与工作空间 |
| D3 | 两套产物路径 | unit 用 `testkit` 根目录，迁移用例用 runner 产物目录；互不覆盖 |
| D4 | 核验缺口的补齐位置 | 在 R2 语义内补，不新增第三套核验机制 |
| D5 | 旧辅助文件删除判据 | 编译 + 引用检索，不凭注释判断 |

---

## 7. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | 全部已注册 Case（`pilot`、`selfcheck*`、`integration/**`、`frontend`）；`tests/unit` 全量 `go test` |
| 允许的真实设备批次 | R5 的 B3 范围，一次；需要真实 Pi / SSH / 远程 Docker 配置就绪 |
| 允许的命令 | `go build`、`./bin/agentflow-test <集合>`、`go test ./tests/unit/... -count=1`、`go vet`、`gofmt` |
| 明确不允许 | 修改断言以让结果通过；跳过真实批次却声称已完成；`pnpm` 依赖升级 |
| 理由 | 删除 `testkit` 组件会影响 unit 侧，必须跑一次 unit 全量；真实批次用于复核零残留归属 |

---

## 8. 验收条件

1. §5 对账表填满，每项都有新落点，没有「随迁移消失」的核验。
2. 无引用的 `testkit` 组件已删除；编译与 `go vet ./...` 通过。
3. 17 个 unit 包的 `TestMain` 行为不变；unit 全量通过数与迁移前一致。
4. 真实批次一次运行：无容器、远程目录、SSH 会话残留，`residual_check` 为 `passed`。
5. 核验失败仍能使退出码非零；跳过仍带原因且不计入通过。
6. 不存在两套并行的零残留核验实现。
7. 全部已注册 Case 一次运行结论与 R5/R6 记录一致（允许因环境导致的 SKIP，但必须带原因）。

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| 删除组件后才发现 unit 仍依赖 | 以编译与引用检索为判据；unit 全量运行验证 |
| 旧核验的能力被悄悄削弱 | 对账表逐项落点；无法确认即失败的标准不降低 |
| 真实批次不可用导致无法复核 | 如实记录为未执行并阻塞 R7 完成，不用本机替身代替 |
| 两套产物路径混淆 | D3 明确边界，R9 写入文档 |

---

## 10. 不做的事

- 不迁移新用例（R5/R6 已完成）。
- 不修改 `Makefile`、CI 与文档（R8 / R9）。
- 不新增第三套核验机制。
- 不改写 unit 断言，只跟随接口变化调整调用点。

---

## 11. 输出给 R8 / R9

1. 删除清单与保留清单（`testkit` 各文件）。
2. 对账表的最终结论，供 R9 写入测试文档。
3. 真实批次零残留复核记录。
4. unit 侧入口的最终职责说明。
