# V3 R2：资源管理器与零残留核验

**状态：** 完成。payload 契约、12 类提供者、两级零残留核验与 12 包排布结论已冻结在代码与本文中。
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

- `Providers` 未登记的类型必须失败并给出「R6 补齐」类错误，不得退化成空句柄（R1 §3）。
- 句柄外壳固定为 `agentflow-case-handles/1`；版本不符直接失败，不尝试兼容（R1 §4）。
- 报告外壳固定为 `agentflow-test-report/1`；`residual_check` 已是必备字段（R1 §5）。
- 资源类型词汇表固定 16 种，来自 `case-resource-matrix.json`（R0 §3）。
- 默认超时 10m、宽限期 10s、worker 启动开销约 20ms（R1 §8）。
- 测试工作空间与测试产物是**保留产物**，不删除（[docs/testing.md](../../../docs/testing.md) §4）。

---

## 3. 交付物

| 位置 | 内容 |
|---|---|
| `internal/testrunner/resource_contract.go` | 16 类资源的 payload / Spec 契约、分配前校验 |
| `internal/testrunner/resource_registry.go` | `ProviderOptions`、默认提供者集合、故障注入包装 |
| `internal/testrunner/resource_probe.go` | 端口关闭与 HTTP 就绪/消失探测 |
| `internal/testrunner/resource_local_port.go` | `local-port`（R1 迁移 + 自证 + 快照） |
| `internal/testrunner/resource_sqlite.go` | `sqlite-db` |
| `internal/testrunner/resource_http.go` | `local-http-server` |
| `internal/testrunner/resource_process.go` | `child-process`、`service-process` 与受管子进程 |
| `internal/testrunner/resource_git_worktree.go` | `git-worktree` |
| `internal/testrunner/resource_ssh_server.go` | `local-ssh-server`（真实 SSH/SFTP 服务端） |
| `internal/testrunner/resource_pi.go` | `pi-cli`（只做版本探测与内容指纹） |
| `internal/testrunner/resource_remote.go` | 远程主机连接池、`ssh-host`、`ssh-session` |
| `internal/testrunner/resource_docker.go` | `docker-daemon`、`remote-container` |
| `internal/testrunner/resource_remote_workspace.go` | `remote-workspace` |
| `internal/testrunner/residual.go` | 资源级自证汇总、运行级快照终检、终检状态取值 |
| `internal/testrunner/schedule.go` | 容量、独占键、包级串行声明（R3 的调度输入） |
| `tests/runner/resourcecheck` | 本机资源自检组 `resourcecheck` + 故意失败的 `resourcecheck-fault` |
| 本文 §5 / §7 / §8 / §10 | payload 契约、12 包结论、冻结决策与验收记录 |

R6 才消费的 `node-worker` / `browser` / `frontend-dev-server` 本阶段只冻结契约与调度属性，不实现
提供者：声明它们的用例在分配阶段失败。

---

## 4. 实施步骤

### 4.1 冻结 payload 契约（已完成）

`ResourceContract` 同时约束两侧：声明 `Spec` 允许出现哪些创建参数，句柄 `payload` 必须给出哪些键。
契约外的 Spec 键在**声明阶段**报错；契约声明的必填 Spec 键缺失在**分配阶段**报错。句柄必填 payload
键缺失时 worker 结论为 `error`，不回退成空字符串。契约表见 §5。

### 4.2 本机提供者（已完成）

`sqlite-db`、`local-http-server`、`child-process`、`service-process`、`git-worktree`、`local-ssh-server`、
`pi-cli` 全部落地，各自的创建、就绪、释放与自证方式见 §5.2。`local-port` 从 R1 迁入并补上自证与快照。

### 4.3 远程提供者（已完成，本阶段不执行）

`ssh-host`、`ssh-session`、`docker-daemon`、`remote-container`、`remote-workspace` 复用现有封装：

- 连接只用 `internal/sshclient`（主机密钥必须校验通过，禁止跳过）；Docker 命令只用 `internal/docker`
  的 `Runner` 与既有入口（`ProbeVersion`、`StartTemporaryContainer`、`RemoveContainer`、
  `InspectContainer`、`ListContainers`），不新增第二套连接或命令封装。
- 远程目录只在本次运行的远程测试根目录（默认 `/tmp/agentflow-test/<run-id>`）下创建。
- 容器释放后以 `docker inspect` 判 `container_not_found` 自证；快照按 `agentflow-test-<run-id>-` 前缀
  扫描 `docker ps -a`，能发现本进程之外的遗留测试容器。SSH 会话释放后立刻重试命令，要求连接已不可用。
- 主机连接归 `ssh-host`（run-shared）所有：只声明 `docker-daemon` / `remote-container` 而不声明
  `ssh-host` 时，连接会在终检里如实表现为残留。这是有意的强制约束，不是缺陷。
- 远程主机配置由调用方注入（`ProviderOptions.Hosts`）；R2 不提供装载入口，R4.5 试点负责从
  `tests/config/ssh.yml` 装载。未注入的主机一律失败，不回退到本机执行。

### 4.4 资源级自证与运行级终检（已完成）

- 资源级：`Provider.Release` 保持可重复调用；释放后调用 `Provider.Verify` 自证消失，结论写入
  `resources[].verified` / `verify_error`。编排进程在每次成功释放后**再调用一次** `Release`，用于验证
  幂等性；重复释放报错记为 `release_error`。
- 运行级：用例全部结束、写报告之前采集全部提供者快照，与运行前基线比对；结论写入
  `residual_check{status,reason,checked_at,baseline,residuals,errors}`。
- 排除项：测试工作空间、`.test-artifacts`、`TEST_ARTIFACT_DIR`、Docker 构建缓存。终检只认提供者
  观测到的资源，不对产物目录做删除或扫描。

### 4.5 构建产物缓存通道（契约已冻结）

镜像构建是写操作，不能按普通 `run-shared` 资源处理：报告新增 `build_artifacts[]`（`kind` / `ref` /
`fingerprint` / `created_at` / `expires_at` / `owner` / `cleanup`）与
`BuildArtifactCleanupPolicy`（谁创建、何时过期、谁删除、删除失败如何报告）。R2 不产生条目，真实构建
在 R5 落地；条目与普通资源共享资源分节统计。

### 4.6 输出 R3 需要的调度输入（已完成）

`internal/testrunner/schedule.go` 冻结三类输入：

- `ResourceProfiles()`：每类资源的默认作用域、容量上限（`Capacity`，0 表示不限）与独占键语义；
- `ExclusiveKeyOf(decl)`：同键资源不得并行（同一仓库的 worktree、同一主机、同一镜像标签、
  同一远程目录、同一 dev server `base_url`）；
- `SerialPackageReason(pkg)` / `SerialPackages()`：必须整包串行的包及理由。

### 4.7 12 个待确认包的结论（已完成）

结论见 §7，不再保留 `needs_review`；`integration/remote/workflow` 是唯一整包串行的包。

---

## 5. payload 契约（已冻结）

`payload` 只放可序列化的自描述字符串键；`fingerprint` 只用于按内容寻址的资源。**加粗**为必填键。

### 5.1 契约表

| 类型 | 默认作用域 | payload 键 | Spec 键 | fingerprint | 实现 |
|---|---|---|---|---|---|
| `local-port` | case-exclusive | **address** | — | 空 | R1/R2 |
| `sqlite-db` | case-exclusive | **path** | — | 空 | R2 |
| `local-http-server` | case-exclusive | **base_url** | — | 空 | R2 |
| `child-process` | case-exclusive | **pid**、**executable** | `command`、`args` | 空 | R2 |
| `service-process` | case-exclusive | **pid**、**base_url**、**binary** | `binary`、`args`、`ready_path`、`ready_timeout_ms` | 二进制 SHA256 | R2 |
| `git-worktree` | case-exclusive | **path**、**commit** | `repo`、`commit` | 空 | R2 |
| `local-ssh-server` | case-exclusive | **address**、**host_key**、**user**、**private_key_path** | — | 空 | R2 |
| `pi-cli` | case-exclusive | **path**、**version** | `binary`、`version_timeout_ms` | 可执行文件 SHA256 | R2 |
| `ssh-host` | run-shared | **host_id**、**address**、**user**、**port** | `host_id`（必填） | 主机密钥指纹 | R2 |
| `ssh-session` | case-exclusive | **session_id**、**host_id** | `host_id`（必填） | 空 | R2 |
| `docker-daemon` | run-shared | **host_id**、**endpoint** | `host_id`（必填） | 空 | R2 |
| `remote-container` | case-exclusive | **container_id**、**host_id** | `host_id`、`image`（均必填）、`command` | 镜像 ID | R2 |
| `remote-workspace` | case-exclusive | **host_id**、**path** | `host_id`（必填）、`path` | 空 | R2 |
| `node-worker` | case-exclusive | **pid**、**runtime** | — | R6 冻结 | R6 |
| `frontend-dev-server` | run-shared | **base_url**、**pid** | — | R6 冻结 | R6 |
| `browser` | case-exclusive | **browser**、**version**、**session_dir** | — | R6 冻结 | R6 |

`endpoint` 形如 `ssh://user@host:port`（Docker 命令经该 SSH 连接执行）。`host_key` 是 known_hosts
行（非 22 端口用 `[host]:port` 写法）。`private_key_path` 是本机私钥文件，释放时随资源目录一起删除。

### 5.2 创建、就绪、释放与自证

| 类型 | 创建 | 就绪判定 | 释放 | 自证 |
|---|---|---|---|---|
| `local-port` | `net.Listen` 随机端口 | 连接成功 | 关闭监听器 | 连接必须失败 |
| `sqlite-db` | 建库并写入数据库头 | 文件头 + `database/sql` 连接 | 删除 `.db` / `-wal` / `-shm` | 文件必须不存在 |
| `local-http-server` | 进程内 `http.Server` | `base_url` 返回 200 | 关闭 server 与监听器 | 请求必须失败 |
| `child-process` | 独立进程组启动（默认自身 `--hold`） | 启动窗口内不退出 | 终止进程树并回收 | 进程号必须不存在 |
| `service-process` | 分配空闲端口 → 替换 `{port}` → 启动 | `base_url` 返回 200 | 终止进程树并回收 | 进程不存在 + 端点不响应 |
| `git-worktree` | `git worktree add --detach` | 目录存在且 `.git` 就位 | `worktree remove --force` + `prune` | 目录必须不存在 |
| `local-ssh-server` | 生成主机密钥与客户端密钥、监听 | 用生成私钥真实握手并执行命令 | 关闭监听与连接、删除密钥目录 | 端口必须关闭 + 密钥目录不存在 |
| `pi-cli` | 解析可执行文件 | `--version` 输出可识别版本 | 清理占用记录 | 提供者不再持有 + 文件仍存在 |
| `ssh-host` | `sshclient` 连接（校验主机密钥） | 解析远程 Home 成功 | 关闭共享连接 | 主机不再保留连接 |
| `ssh-session` | 独立连接 | 探测命令退出码 0 | 关闭连接并重试命令 | 提供者不再持有 + 连接不可用 |
| `docker-daemon` | 基于共享连接构造 `docker.Runner` | `ProbeVersion` 通过 | 清理占用记录 | 提供者不再持有 |
| `remote-container` | `StartTemporaryContainer`（带所有权标签） | 返回容器 ID | `RemoveContainer`（幂等） | `inspect` 返回 `container_not_found` |
| `remote-workspace` | `sshclient.EnsureWorkspace(create=true)` | 目录可写 | SFTP `RemoveAll` | 远程 `test -d` 必须失败 |

`child-process` 未给 `command` 时以编排进程自身 `--hold` 启动常驻子进程；`service-process` 未给
`binary` 时以自身 `--serve 127.0.0.1:{port}` 启动自检 HTTP 服务。两个内部入口只服务资源自检，不作为
人工日常命令。

校验规则沿用 R1：缺失、类型不符、作用域不符、重复 → worker 结论 `error`；多余句柄记 warning。新增：
契约必填 payload 键缺失同样判 `error`。

---

## 6. 释放与核验语义

- 释放顺序：用例独享资源在用例结束后立即释放；运行共享资源在全部用例结束后**按分配顺序的逆序**
  释放（后分配的资源依赖先分配的连接或服务，必须先撤销）。
- 释放可重复调用：重复释放返回成功，不重复记录失败；编排进程在每次成功释放后主动再调一次，验证幂等。
- 自证失败或无法确认时结论为失败：`released` 与 `verified` 各自独立记录，`release_error` /
  `verify_error` 都让整次运行失败，不得改写为「已释放」或跳过。
- `residual_check.status` 取值冻结为 `passed` / `failed` / `not-applicable`；`not-run` 只保留给未完成
  核验的异常报告，一次正常运行结束后不允许停留在 `not-run`。
- 终检口径：终检快照里出现、且运行前基线里不存在的资源即为本次运行的残留；快照查询失败记为
  「无法确认」，一律判 `failed`。基线里本来就存在的残留不计入本次运行，但原因里写明条数。
- 终检失败不得删除任何产物；报告与日志必须保留，便于人工复核。

---

## 7. 12 个待确认包的并行结论

判定规则：

1. 出现不可逆操作（容器删除、远程目录清理、服务关闭、镜像标签改写）→ 该资源独享，包内互斥。
2. 写操作无法命名空间化 → 独享。
3. 只读或可命名空间化的写 → 共享。
4. 真实依赖未确认前默认独享；确认后可在本表标注降级理由。

### 7.1 R0 标记「待确认」的 12 个包

| 包 | R0 初判 | 结论 | 依据 |
|---|---|---|---|
| `integration/local/environment` | exclusive | 独享 | `RemoveAll(` 清理工作空间 |
| `integration/local/execenv` | exclusive | 独享 | `ContainerRemove` |
| `integration/local/fetcher` | shared-candidate | 独享 | 声明 `remote-container`，构建与容器不可共享 |
| `integration/local/session` | shared-candidate | 独享 | 无资源声明但存在 `RemoveAll(` |
| `integration/local/workflow` | exclusive | 独享 | `Shutdown(` 关闭服务 |
| `integration/pi` | unclassified | 独享（容量 1） | 真实 Pi CLI 调用，`pi-cli` 容量为 1 |
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

## 8. 阶段内冻结的决策（已定稿）

| 编号 | 决策 | 结论 |
|---|---|---|
| D1 | `residual_check.status` 取值 | `passed` / `failed` / `not-applicable`；禁止运行后仍是 `not-run` |
| D2 | 镜像构建产物归属 | 独立缓存通道：报告 `build_artifacts[]`，清理责任写进 `BuildArtifactCleanupPolicy` |
| D3 | 未实现类型的失败时机 | 分配阶段失败，保持 R1 语义；前端三类错误文案指向 R6 |
| D4 | 真实资源提供者首次验证时机 | R4 与 R4.5；本阶段只做编译与静态检查 |
| D5 | 资源规格的承载方式 | `ResourceDecl` 新增 `Spec map[string]string`，键名与 payload 契约一起冻结；契约外键在声明阶段失败，必填键缺失在分配阶段失败 |
| D6 | 提供者能力扩展 | `Provider` 新增 `Verify`（释放后自证）与 `Snapshot`（运行级终检），全部提供者必须实现 |
| D7 | 共享资源释放顺序 | 按分配顺序的**逆序**释放，满足共享资源之间的依赖 |

---

## 9. 运行授权申请与执行记录

| 项 | 内容 |
|---|---|
| 允许执行的集合 | `selfcheck`、`selfcheck-fault`、`resourcecheck`（本机资源）、`resourcecheck-fault` |
| 允许的命令 | `./bin/agentflow-test --artifact-dir .test-artifacts <集合>`；`go vet ./...`；`gofmt -l` |
| 明确不允许 | `tests/unit`、`tests/integration`、`tests/e2e` 的任何 `go test`；真实 Pi / SSH / 远程 Docker；前端测试命令 |
| 理由 | 本阶段验收只依赖本机资源自检；真实设备第一次使用放在 R4 / R4.5，避免在接口未稳定时占用真实环境 |

执行记录（2026-11-10，本机 Windows 11）：

| 命令 | 结论 | 观测 |
|---|---|---|
| `selfcheck` | 退出码 0 | 通过 2 / 跳过 1；`selfcheck/skip` 带原因，不计入通过 |
| `selfcheck-fault` | 退出码 1 | 失败 1 / panic 1 / 超时 1，均如实记录 |
| `resourcecheck` | 退出码 0 | 8 条本机资源用例全部通过，`residual_check.status=passed`，逐资源 `verified=true` |
| `resourcecheck-fault` | 退出码 1 | `missing-spec` 与 `unimplemented` 报 `error`，`release-failed` 通过 |
| `resourcecheck-fault/release-failed --selfcheck-fail-release local-port` | 退出码 1 | `residual_check.status=failed`，残留 1 条本地端口，产物全部保留 |

`pi-cli` 用例只执行 `pi --version`（版本 1.4.0）探测可执行文件与内容指纹，没有发起任何 Pi 会话；真实
Pi / SSH / 远程 Docker 资源本阶段未运行。

---

## 10. 验收条件与结果

| 条件 | 结果 |
|---|---|
| 1. 本机 7 类提供者各自有正反自检：获取、自证、释放、重复释放、缺失必须报错 | 达成：`resourcecheck` 8 条用例覆盖 7 类提供者 + `local-port`；重复释放由编排进程对每次成功释放主动复检；缺 Spec 与未实现类型由 `resourcecheck-fault` 覆盖 |
| 2. 注入式泄漏能被运行级终检判 `failed`，退出码非零、产物保留 | 达成：`--selfcheck-fail-release` 注入后 `residual_check.status=failed`，退出码 1，报告与 `stdout/stderr/result.json` 全部保留 |
| 3. `residual_check.status` 正常运行后不再是 `not-run` | 达成：`resourcecheck` 运行为 `passed`，无资源声明时为 `not-applicable` |
| 4. §5 契约表填满并冻结；未实现类型在分配时给出明确错误 | 达成：契约表 16 类齐全；`browser` 报「由 R6（前端适配器）补齐」 |
| 5. §7 结论表无 `needs_review`；§4.6 输入可供 R3 直接读取 | 达成：§7 表格全部给出结论；`schedule.go` 提供容量、独占键与包级串行 |
| 6. `go vet ./...` 与 `gofmt -l` 无新增问题 | 达成 |

---

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| 资源自证本身依赖外部命令，失败原因含糊 | 报告保留命令、退出码与关键输出摘要（`ResourceResult.verify_error`），不吞错误 |
| 远程资源首次执行可能暴露接口缺陷 | 接口在 R2 冻结，R4.5 试点只允许回写契约补丁，不允许改语义 |
| 零残留核验误删保留产物 | 终检只比对提供者观测到的资源，不扫描、不删除产物目录；排除清单固定在本文 §4.4 |
| 12 包结论凭文本线索误判 | 结论写明依据；试点发现冲突时在 R4.5 修正并记录 |
| `service-process` 端口存在「探测空闲 → 服务监听」的竞争窗口 | 就绪判定在超时内重试；窗口期失败表现为「服务没有就绪」，不会静默通过 |
| 只声明远程容器而不声明 `ssh-host` 会让连接在终检里成为残留 | 有意保留：远程用例必须声明 `ssh-host`，否则运行失败并指出残留条目 |

---

## 12. 不做的事

- 不实现并发调度、取消、worker 池（R3）。
- 不迁移任何既有 `_test.go` 用例（R4 / R4.5 / R5）。
- 不实现前端资源提供者与适配器（R6）。
- 不删除 `testkit` 的任何辅助代码（R7）。
- 不修改 `Makefile`、`.github/**`、`docs/**`、`README.md`（R8 / R9）。

---

## 13. 输出给 R3 / R4

1. 冻结后的 payload / Spec 契约与必填键校验规则（§5）。
2. 每类资源的容量上限与独占键；包级串行声明（`schedule.go`、§7）。
3. 资源级自证与运行级终检的实现位置、状态取值与失败语义（§4.4、§6）。
4. 12 包并行结论，以及仍需 R4.5 复核的包清单（§7 其余包）。
5. 报告新增字段：`resources[].verified` / `verify_error`、`residual_check` 五个子字段、
   `build_artifacts[]`（§5.1、§4.4、§4.5）。

R3 直接可用的接口：`CapacityOf(type)`、`ExclusiveKeyOf(decl)`、`SerialPackageReason(pkg)`、
`ResourceProfiles()`；`Providers.ProviderFor(type)` 返回的提供者同时可做 `Verify` 与 `Snapshot`。
