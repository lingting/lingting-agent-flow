# V3 R2：资源管理器与零残留核验

**状态：** 待执行。
**上游文档：** [路线总规划](../ROADMAP.md)、[R1 接口与骨架](../R1/README.md)、
[R0 盘点与决策](../R0/README.md)。
**范围：** 补齐资源提供者、冻结 payload 契约、落地共享/独享排布与零残留两级核验。不实现并发调度
（R3）、不迁移既有用例（R5/R6）、不改 `Makefile` / CI / `docs/`。

---

## 1. 目标与边界

R1 冻结了 `ResourceDecl` / `Handle` / `HandleFile` / `Provider` 的外壳，只实现了 `local-port`。本阶段把
外壳补成可用体系，并回答三个问题：

1. 每种资源的句柄里到底放什么，缺失时如何失败；
2. 每种资源释放后如何自证消失，整次运行如何做终检；
3. R0 判定「待确认」的 12 个包，在调度器里如何排布。

边界：本阶段只保证编排进程侧的资源语义正确。用例侧的真实使用由 R4 / R4.5 验证；本阶段不运行真实
设备（真实 Pi / SSH / 远程 Docker），这三类提供者只做代码审查与静态检查。

---

## 2. 输入（已冻结，不得在本阶段改写）

- `Providers` 未登记的类型必须失败并给出「R2 补齐」类错误，不得退化成空句柄（R1 §3）。
- 句柄外壳固定为 `agentflow-case-handles/1`；版本不符直接失败，不尝试兼容（R1 §4）。
- 报告外壳固定为 `agentflow-test-report/1`；`residual_check` 已是必备字段（R1 §5）。
- 资源类型词汇表固定 16 种，来自 `case-resource-matrix.json`（R0 §3）。
- 默认超时 10m、宽限期 10s、worker 启动开销约 20ms（R1 §8）。
- 测试工作空间与测试产物是**保留产物**，不删除（[docs/testing.md](../../../docs/testing.md) §4）。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `internal/testrunner/resource_*.go` | 按资源类型拆分的提供者实现与自证逻辑 |
| `internal/testrunner/residual.go` | 资源级核验汇总与运行级终检 |
| `tests/runner/resourcecheck` | 本机资源自检组：正常获取/自证/释放、重复释放、泄漏可检出 |
| 本文 §5 / §7 | payload 契约与 12 包结论（完成后即为冻结记录） |

R6 才消费的 `node-worker` / `browser` / `frontend-dev-server` 本阶段只冻结契约，不实现提供者。

---

## 4. 实施步骤

### 4.1 冻结 payload 契约（第一步，先做）
按 §5 逐类型确认键名、必填项与 `fingerprint` 语义；契约写入本文后不再改动。未实现类型继续返回明确
错误，不允许「返回空 payload 当作成功」。

### 4.2 本机提供者（第一批）
`sqlite-db`、`local-http-server`、`child-process`、`service-process`、`git-worktree`、`local-ssh-server`、
`pi-cli`。每个提供者必须回答：如何创建、如何判断就绪、如何释放、释放后如何自证。

### 4.3 远程提供者（第二批）
`ssh-host`、`ssh-session`、`docker-daemon`、`remote-container`、`remote-workspace`。
- 复用 `internal/sshclient` 与 `internal/docker`，不新增第二套连接或命令封装（AGENTS 红线）。
- 远程目录只在 `os.TempDir()` 对应的测试根目录下创建，释放后自证目录不存在。
- 容器释放后以 `docker ps -a` 自证；SSH 会话释放后以会话计数或连接关闭状态自证。
- 本阶段不执行这些提供者，只做编译与静态检查。

### 4.4 资源级自证与运行级终检
- 资源级：`Provider.Release` 保持可重复调用；新增自证步骤，失败时该资源 `released=false` 且带
  `release_error`，整次运行失败。
- 运行级：全部用例结束后、写报告之前做一次全量快照比对；失败时 `residual_check.status=failed`，
  退出码非零，产物全部保留。
- 排除项：测试工作空间、`.test-artifacts`、`TEST_ARTIFACT_DIR`、Docker 构建缓存。这些是保留产物，
  不得为了「零残留」删除（docs/testing.md §4）。

### 4.5 构建产物缓存通道（P1-6）
镜像构建是写操作，不能按普通 `run-shared` 资源处理：
- 构建一次 → 打内容指纹标签 → 用例只读引用标签；
- 过期标签的删除责任与时机必须写明（谁删、何时删、删除失败如何报告）；
- 本阶段只冻结契约并在报告中记录「构建产物」与「共享资源」两类条目，不实现真实构建流程。

### 4.6 输出 R3 需要的调度输入
为每个资源类型与每个包补三类声明：
- 独占键：同键资源不能并行（例如同一镜像标签、同一固定远程目录）；
- 包级串行：`tests/integration/remote/workflow` 必须整包串行（R0 §6）；
- 容量：每类资源同时可用的份数（例如容器、SSH 会话、本地端口）。

### 4.7 12 个待确认包的结论
按 §7 的规则逐包给出「能否并行 / 哪个资源共享 / 哪个独享」的结论，不允许保留 `needs_review`。

---

## 5. payload 契约（阶段内冻结）

`payload` 只放可序列化的自描述字符串键；`fingerprint` 只用于按内容寻址的资源。

| 类型 | 默认作用域 | 建议 payload 键 | fingerprint | 实现 |
|---|---|---|---|---|
| `local-port` | case-exclusive | `address` | 空 | R1 已完成 |
| `sqlite-db` | case-exclusive | `path` | 空 | R2 |
| `local-http-server` | case-exclusive | `base_url` | 空 | R2 |
| `child-process` | case-exclusive | `pid`、`executable` | 空 | R2 |
| `service-process` | case-exclusive | `pid`、`base_url`、`binary` | 二进制 SHA256 | R2 |
| `git-worktree` | case-exclusive | `path`、`commit` | 空 | R2 |
| `local-ssh-server` | case-exclusive | `address`、`host_key` | 空 | R2 |
| `pi-cli` | case-exclusive | `path`、`version` | 可执行文件 SHA256 | R2 |
| `ssh-host` | run-shared | `host_id`、`address`、`user`、`port` | 已知主机指纹 | R2 |
| `ssh-session` | case-exclusive | `session_id`、`host_id` | 空 | R2 |
| `docker-daemon` | run-shared | `host_id`、`endpoint` | 空 | R2 |
| `remote-container` | case-exclusive | `container_id`、`host_id` | 镜像 ID | R2 |
| `remote-workspace` | case-exclusive | `host_id`、`path` | 空 | R2 |
| `node-worker` | case-exclusive | `pid`、`runtime` | 空 | R6 |
| `frontend-dev-server` | run-shared | `base_url`、`pid` | 空 | R6 |
| `browser` | case-exclusive | `browser`、`version`、`session_dir` | 空 | R6 |

校验规则沿用 R1：缺失、类型不符、作用域不符、重复 → worker 结论 `error`；多余句柄记 warning。
新增：契约声明的必填键缺失时同样判 `error`，不允许回退成空字符串继续执行。

---

## 6. 释放与核验语义

- 释放顺序：用例独享资源在用例结束后立即释放；运行共享资源在全部用例结束后按分配顺序释放。
- 释放可重复调用：重复释放返回成功，不重复记录失败。
- 自证失败或无法确认时结论为失败，不得改写为「已释放」或跳过。
- `residual_check.status` 取值本阶段冻结；一次正常运行结束后不允许是 `not-run`。建议取值：
  `passed`（全量比对通过）、`failed`（存在残留或无法确认）、`not-applicable`（本次没有问题需要核验）。
- 终检失败不得删除任何产物；报告与日志必须保留，便于人工复核。

---

## 7. 12 个待确认包的并行结论

判定规则：
1. 出现不可逆操作（容器删除、远程目录清理、服务关闭、镜像标签改写）→ 该资源独享，包内互斥。
2. 写操作无法命名空间化 → 独享。
3. 只读或可命名空间化的写 → 共享。
4. 真实依赖未确认前默认独享；确认后可在本表标注降级理由。

### 7.1 R0 标记「待确认」的 12 个包

| 包 | R0 初判 | 建议结论 | 依据 |
|---|---|---|---|
| `integration/local/environment` | exclusive | 独享 | `RemoveAll(` 清理工作空间 |
| `integration/local/execenv` | exclusive | 独享 | `ContainerRemove` |
| `integration/local/fetcher` | shared-candidate | 独享 | 声明 `remote-container`，构建与容器不可共享 |
| `integration/local/session` | shared-candidate | 独享 | 无资源声明但存在 `RemoveAll(` |
| `integration/local/workflow` | exclusive | 独享 | `Shutdown(` 关闭服务 |
| `integration/pi` | unclassified | 独享 | 真实 Pi CLI 调用，真实依赖 |
| `integration/ssh` | unclassified | 共享 `ssh-host` + 独享 `ssh-session` | 主机可复用，会话不可复用 |
| `integration/remote/agent` | exclusive | 独享 | `RemoveAll(`；Pi + SSH + Docker |
| `integration/remote/codesaver` | unclassified | 独享 | 真实容器写入，无法命名空间化 |
| `integration/remote/docker` | unclassified | 独享 | 容器删除与远程工作区 |
| `integration/remote/planning` | exclusive | 独享 | `docker rm`；卷清理 |
| `integration/remote/workflow` | exclusive | 独享 + 整包串行 | 同一台真实远程 Docker 与镜像标签 |

### 7.2 R0 已判定无需确认的 6 个包

| 包 | 结论 | 依据 |
|---|---|---|
| `e2e/cli` | 共享 | 端口、本地 SSH 服务端与数据库均按用例分配 |
| `integration/local/codesaver` | 共享 | 本机子进程与 Git 工作区各自独立目录 |
| `integration/local/scheduler` | 共享 | 只有 SQLite，每用例独立库文件（R4.5 校验） |
| `integration/local/source` | 共享 | 独立工作区与子进程 |
| `integration/local/ssh` | 共享（本地 SSH 服务端独享） | 端口与工作区按用例分配 |
| `integration/local/validation` | 共享 | 端口与数据库按用例分配 |

`integration/remote/workflow` 是唯一需要整包串行的包（镜像标签互斥）。其余包的逐资源排布在 R4.5 试点
与 R5 批次中复核，偏差回写本表并写明依据。

---

## 8. 阶段内需要冻结的决策

| 编号 | 决策 | 选项 | 建议 |
|---|---|---|---|
| D1 | `residual_check.status` 取值 | 三值 / 四值 | `passed` / `failed` / `not-applicable`，禁止运行后仍是 `not-run` |
| D2 | 镜像构建产物归属 | 资源 / 缓存通道 | 独立缓存通道，见 §4.5 |
| D3 | 未实现类型的失败时机 | 声明时 / 分配时 | 分配时失败，保持 R1 语义 |
| D4 | 真实资源提供者首次验证时机 | R2 / R4 | R4 与 R4.5，本阶段只做静态检查 |

---

## 9. 运行授权申请

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `selfcheck`、`selfcheck-fault`、新增 `resourcecheck`（本机资源）、`resourcecheck-fault` |
| 允许的命令 | `./bin/agentflow-test --artifact-dir .test-artifacts <集合>`；`go vet ./...`；`gofmt -l` |
| 明确不允许 | `tests/unit`、`tests/integration`、`tests/e2e` 的任何 `go test`；真实 Pi / SSH / 远程 Docker；前端测试命令 |
| 理由 | 本阶段验收只依赖本机资源自检；真实设备第一次使用放在 R4 / R4.5，避免在接口未稳定时占用真实环境 |

---

## 10. 验收条件

1. 本机 7 类提供者各自有正反自检：获取、自证、释放、重复释放、缺失必须报错。
2. 注入式泄漏（故意不释放）能被运行级终检判为 `failed`，且退出码非零、产物保留。
3. `residual_check.status` 在正常运行后不再是 `not-run`。
4. §5 契约表填满并冻结；未实现类型（三种前端资源）在分配时给出明确错误。
5. §7 结论表不再有 `needs_review`；§4.6 的独占键、包级串行与容量声明可供 R3 直接读取。
6. `go vet ./...` 与 `gofmt -l` 无新增问题（不运行测试）。

---

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| 资源自证本身依赖外部命令，失败原因含糊 | 报告里保留命令、退出码与关键输出摘要，不吞错误 |
| 远程资源首次执行可能暴露接口缺陷 | 接口在 R2 冻结，R4.5 试点只允许回写契约补丁，不允许改语义 |
| 零残留核验误删保留产物 | 排除清单写死在代码与本文 §4.4，不得依赖人工记忆 |
| 12 包结论凭文本线索误判 | 结论必须写明依据；试点发现冲突时在 R4.5 修正并记录 |

---

## 12. 不做的事

- 不实现并发调度、取消、worker 池（R3）。
- 不迁移任何既有 `_test.go` 用例（R4 / R4.5 / R5）。
- 不实现前端资源提供者与适配器（R6）。
- 不删除 `testkit` 的任何辅助代码（R7）。
- 不修改 `Makefile`、`.github/**`、`docs/**`、`README.md`（R8 / R9）。

---

## 13. 输出给 R3 / R4

1. 冻结后的 payload 契约与必填键校验规则。
2. 每类资源的容量上限与独占键；包级串行声明。
3. 资源级自证与运行级终检的实现位置、状态取值与失败语义。
4. 12 包并行结论，以及仍需 R4.5 复核的包清单。
5. 报告新增字段（资源核验、缓存产物）的最终形态。
