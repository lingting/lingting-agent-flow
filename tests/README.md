# AgentFlow 测试

本目录是 AgentFlow 唯一的测试代码位置。生产源码目录（`internal/`、`cmd/`）不再存放测试文件，
新增测试一律放在本目录下。

测试的规范条款（授权边界、两个执行入口、用例模型、并发与调度、资源与零残留、真实依赖开关、覆盖
对账、结果报告）以 [`docs/testing.md`](../docs/testing.md) 为准；本文件说明目录结构、入口命令、
产物布局与各条链路的分层覆盖。

## 目录结构

```text
tests/
├── internal/testkit/           通用测试辅助：工作空间、日志、known_hosts 快照、容器核验、testkit.Main
├── unit/                       仍由 go test 执行（17 个包，1352 条顶层用例）
│   ├── agent/ app/ cigate/ container/ domain/ environment/ execenv/ httpapi/
│   ├── image/ lifecycle/ server/ source/ store/ testkit/ testrunner/
│   └── ...
├── runner/                     由 agentflow-test 执行（25 个分组，360 条 Case）
│   ├── registry/               用例包聚合入口：新增用例包只需一行空导入
│   ├── integration/local/      本机集成：codesaver、environment、execenv、scheduler、
│   │                           session、source、ssh、validation、workflow
│   ├── integration/ssh/        真实 SSH 主机
│   ├── integration/remote/     真实远程 Docker / Agent / 代码保存 / 规划 / 工作流
│   ├── e2e/cli/                CLI 端到端
│   ├── frontend/               Vitest 13 + Playwright 24（一文件一条 Case）
│   ├── selfcheck/              执行器自检：跳过、超时、迟到结果、取消
│   ├── resourcecheck/          资源提供者自检：分配、释放、自证、故障注入
│   ├── concurrencycheck/       调度场景：并发上限、容量、独占键、包级串行、取消
│   └── pilot/                  R4 试点：真实服务进程的启动、就绪、取消与零残留
└── config/                     真实外部依赖配置（示例文件入库，真实配置不入库）
```

## 两个入口

| 范围 | 入口 | 命令 |
|---|---|---|
| `tests/unit/**` | `go test` | `make test-unit` |
| 其余全部 Case | `agentflow-test` | `make test-cases` / `make test-real` |

```bash
# 单元测试：日常反馈，不访问真实模型与远程网络
make test-unit

# 全部普通 Case（local + e2e + selfcheck + frontend，共 314 条）
make test-cases

# 只跑一个 kind
make test-cases TEST_KIND=local
make test-cases TEST_KIND=e2e
make test-cases TEST_KIND=selfcheck
make test-cases TEST_KIND=frontend

# 真实设备分组（32 条，需要单独授权与 tests/config 下的真实配置）
AGENTFLOW_REAL_TESTS_GRANTED=true make test-real

# 单个分组 / 单条用例 / 前缀选择
./bin/agentflow-test integration-local-scheduler
./bin/agentflow-test integration-local-scheduler/retry-eligibility-is-read-only
./bin/agentflow-test integration-local-...

# 只列用例，不执行
./bin/agentflow-test --list


# 真实测试授权判定（CI 使用同一入口；只读判定，不连接任何真实设备）
go run ./cmd/agentflow-ci real-tests-gate \
  -mode self -event push -actor <账号> -repository-owner <所有者> \
  -github-output "$GITHUB_OUTPUT" -step-summary "$GITHUB_STEP_SUMMARY"
```

- 集成、E2E 与前端浏览器用例不得再用 `go test` 执行；它们只有 `agentflow-test` 一个入口。
- 前端单元测试 `pnpm test`（`vp test run`）是本机开发入口，不参与 CI 门禁。
- 执行器是 `cmd/agentflow-test`，`make test-runner` 负责构建 `bin/agentflow-test`；worker 由同一
  二进制以 `--exec-case` 重启自身，因此入口必须是最新构建。
- 选择条件只有三种写法：分组名、精确用例 ID、以 `...` 结尾的前缀；没有排除语法与 `--count`。

## 分组一览

见 `bash .github/scripts/go-test-groups.sh --describe`。当前分组与 kind：

| kind | 分组数 | 分组 | 条数 |
|---|---:|---|---:|
| `local` | 9 | `integration-local-codesaver`(12)、`environment`(7)、`execenv`(28)、`scheduler`(41)、`session`(7)、`source`(6)、`ssh`(22)、`validation`(11)、`workflow`(49) | 183 |
| `e2e` | 1 | `e2e-cli` | 64 |
| `selfcheck` | 4 | `selfcheck`(3)、`resourcecheck`(7)、`concurrencycheck`(17)、`pilot`(1) | 28 |
| `frontend` | 1 | `frontend`（13 Vitest + 24 Playwright） | 37 |
| `real` | 6 | `integration-ssh`(6)、`integration-remote-agent`(2)、`codesaver`(2)、`docker`(6)、`planning`(9)、`workflow`(7) | 32 |
| `fault` | 4 | `selfcheck-fault`(3)、`resourcecheck-fault`(2)、`concurrencycheck-fault`(8)、`pilot-fault`(1) | 14 |

`fault` 分组按设计注入失败，必定以非零退出码结束，不进绿色门禁。

## 用例写法

一条 Case 由 `internal/testrunner.Case` 描述并在包里 `Register`：

```go
testrunner.Register(testrunner.Case{
	ID:         "integration-local-scheduler/queue-order",
	Package:    "tests/runner/integration/local/scheduler",
	Parallel:   true,
	Resources:  []testrunner.ResourceDecl{...},
	Run:        func(c *testrunner.Context) { ... },
})
```

- `Run` 的命名约定是 `func run<intent>`（迁移来源与可读性约定，执行器不再按函数名对账）。
- 源用例里的 `t.Run` 子测试用 `c.Run("名称", func(c *Context) {...})` 表达（单层顺序）；
  失败用 `c.Fatalf`，跳过用 `c.Skipf` 并给出原因。
- 用例只声明资源，不自行创建或清理共享资源；句柄从 `c.Resource(name)` 读取（反序列化的
  `agentflow-case-handles/1` 外壳）。
- 用例主体只在 worker 子进程中执行，因此可以安全地包含 `os.Exit` 风险与包级全局状态。

## 产物与结果

```text
.test-artifacts/<run-id>/report.json
.test-artifacts/<run-id>/cases/<case-id>/{stdout.log,stderr.log,result.json,artifacts/}
.test-artifacts/cases-<kind>-output.log         # run-cases.sh 的控制台记录
.test-artifacts/cases-<kind>-summary.txt        # 机器可读结论
%TEMP%/lingting-agent-flow-tests/<run-id>/      # 用例工作空间与资源存放目录
```

- `report.json` 含选择条件、真实依赖授权结论、用例结论、资源结论、零残留结论、并发与取消记录、
  退出码；跳过统计只读 `totals.skipped` 与每条 `skip_reason`。
- 用例结论只有六种：`passed` / `failed` / `skipped` / `panic` / `timeout` / `error`。跳过不计为通过。
- `tests/unit/**` 的包仍以 `testkit.Main` 为入口，把本次运行输出写入用例工作空间下的 `test.log`，
  并在基目录写 `latest-run.txt`：

```bash
cat "$(go env TMPDIR 2>/dev/null || echo /tmp)/lingting-agent-flow-tests/latest-run.txt"
```

## CI 门禁与本地入口的对应关系

`.github/workflows/ci.yml` 在 Pull Request、`master` push 与手动触发时执行。它不新增测试逻辑，只
调用本文件的入口，因此本地复现失败的顺序与 CI 一致：

| CI Job | 覆盖范围 | 本地等价入口 |
|---|---|---|
| `go-static` | `gofmt -l cmd internal tests` 无输出、`go vet ./...`、`go build ./...` | `make check` |
| `go-inventory` | 分组核验（`go list` 的测试包 + 执行器注册表 + Makefile 入口 + 消费脚本） | `bash .github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` |
| `go-unit` | `./tests/unit/...` | `make test-unit` |
| `cases-local` | `local` 分组 | `make test-cases TEST_KIND=local` |
| `cases-e2e` | `e2e` 分组 | `make test-cases TEST_KIND=e2e` |
| `cases-selfcheck` | `selfcheck` 分组 | `make test-cases TEST_KIND=selfcheck` |
| `ui-test` | `frontend` 分组 + `pnpm check` / `pnpm build` / `check-frontend-clean.sh` / `make web-verify` | `make test-cases TEST_KIND=frontend` + 四项非测试门禁 |
| `real-tests-gate` | 只读判定真实测试是否授权 | 无（仅 CI） |
| `real-tests` | `real` 分组（授权通过后） | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real` |
| `ci-summary` | 其余全部 Job 成功才通过 | 无（仅 CI） |

Case Job 只上传自己的 `<artifact-root>/<job>/<run-id>/`；跳过与零残留结论只读 `report.json`。
脚本清单与参数见 [`.github/scripts/README.md`](../.github/scripts/README.md)。

## SSH 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/runner/integration/local/ssh/` | 本机真实 SSH 握手与主机密钥校验、私钥认证、远程命令执行与取消、SFTP 传输、并发会话隔离 | 无（测试内启动服务端） |
| `tests/runner/integration/ssh/` | 真实远程 Linux 机器上的认证、命令状态、超时、SFTP 与工作空间 | `tests/config/ssh.yml` 的 SSH 目标 |

本机集成用例使用 `golang.org/x/crypto/ssh` 启动真实的服务端进程内实例，并配合
`github.com/pkg/sftp` 的服务端实现，因此主机密钥校验、私钥认证、SFTP 上传下载与远程命令执行都
经过真实协议交互，但不访问外网。所有 SSH 测试都禁止关闭主机密钥校验，禁止自动接受未知主机密钥。

### 主机密钥类型协商

服务端可能同时提供多种主机密钥（例如 ed25519 与 ECDSA），known_hosts 里可能记录了其中若干种。
`sshclient.Dialer` 会按 known_hosts 中该主机已记录的类型**逐个**尝试握手：每次只用一种类型，校验
通过并完成认证即成功；某个类型校验失败或服务端不再提供该类型时换下一个。没有任何记录时不做限制，
交给 `golang.org/x/crypto/ssh` 自身协商。

逐类型重试只针对主机密钥类失败（未知、已变化、已吊销、无共同算法）；TCP 不可达、超时、取消与认证
失败会立即返回，避免换类型掩盖真实原因。

测试覆盖：`tests/runner/integration/local/ssh/dialer_test.go` 与 `hostkey_port_test.go` 验证未知主机
密钥被拒绝、已信任主机密钥可完成私钥认证、主机密钥变化被拒绝，以及带端口与哈希主机名的记录形式。

## 远程执行测试分层

| 分组 / 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/source/` | 仓库地址校验、目标目录冲突、Git 缺失分类、参数转义、基础分支识别优先级、任务分支初始化、取消与超时、结果不确定分类 | 无（容器执行替身） |
| `tests/unit/agent/` | 容器内 Pi 参数构造、调用标识唯一性、标准输入、退出状态分类、Session 缺失/残缺、导出失败保留 JSONL、容器所有权核验 | 无（容器执行替身） |
| `integration-local-source` | 真实 Git 克隆、基础分支识别、`agent/<task-id>` 分支、远程任务分支检出、目标目录不被删除、并发目录隔离 | 本机 `git` |
| `integration-local-session` | 容器内 → 宿主机 → 本机三段式传输、字节校验、传输中断保留部分文件、导出失败保留 JSONL、并发 Session 隔离、容器删除后本机 Session 可读 | 无（路径映射到测试工作空间） |
| `integration-remote-agent` | 真实远程容器内 Git Clone、容器内 Pi 调用、独立 Session、本机持久化、容器删除后 Session 仍可读 | `tests/config/ssh.yml`、真实 Docker 与容器内真实 Pi |
| `integration-local-execenv` | 执行环境规格、工作区与生命周期策略在真实文件系统上的行为 | 无 |

本机 Git 缺失时相关用例输出 `WARN [SKIP]` 并跳过，其余用例仍会执行。

## 代码保存测试分层

| 分组 / 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/domain/` | 代码保存记录的终态与"必须转人工核验"语义 | 无（纯函数） |
| `tests/unit/store/` | `code_saves` 写入与更新、成功必须带 SHA、未请求合并不能记录合并结果、并发写入独立、恢复字段读写、未知恢复状态原样读回、核验结论条件写入（读取后结论被改写返回 `ErrConflict`） | 无 |
| `integration-local-codesaver` | 真实 Git 提交、任务分支真实 Push 到 bare 仓库、默认不合并、显式合并后基础分支包含任务提交、推送失败不误报成功、合并冲突保留现场、无变更不制造空提交、并发仓库隔离 | 本机 `git` |
| `integration-remote-codesaver` | 真实远程容器内提交、推送与合并；**真实副作用结果不明（推送成功但记录写回失败）时需求删除失败关闭，且不回滚远端 Git、不清理容器** | 真实 SSH、远程 Docker 与容器内 Git |

远程代码保存测试只在本次测试创建并已核验所有权的容器内操作，只使用带随机后缀的容器名称与容器内
临时目录，不执行全局 prune，也不在用户的真实主分支上执行合并。

## Docker 测试分层

| 分组 / 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/environment/` | 环境类型、版本号递增与复用判定、构建状态迁移（含 `uncertain`）、镜像引用与 ID 校验 | 无 |
| `tests/unit/image/` | 构建上下文确定性哈希、Dockerfile 静态校验、语言模板、构建配置哈希 | 无 |
| `tests/unit/container/` | 容器状态迁移、detached 语义、生命周期策略、缓存卷命名、创建参数安全边界、JSON 解析 | 无 |
| `integration-local-environment` | 版本文件不可覆盖、清单落盘一致性、构建上下文上传与清理 | 无（远程路径映射到本机目录） |
| `integration-remote-docker` | 真实远程预检、镜像构建、容器创建/启动/执行/停止/删除 | `tests/config/ssh.yml` 与真实 Docker |

真实远程 Docker 测试只使用带随机后缀的测试镜像标签、容器名称与远程目录，并只清理自己创建并已核验
所有权的容器；不会执行全局 prune，也不会删除共享镜像或缓存卷。

### 远程真实测试目录与容器零残留

- 远程主机上的测试资源统一位于 `<远程 Home>/.lingting/agentflow-tests/<测试包>/<用例标识>`，由
  `tests/internal/testkit` 的 `RemoteTestRoot` / `ResolveRemoteTestRoot` 统一解析与创建。
- 每个测试容器都必须在**创建之前**登记清理责任（`testkit.ContainerRun.Track`），取得真实 Docker ID
  后立即补写；创建失败、启动失败与后续步骤失败都不允许失去清理能力。
- 清理必须核验真实状态（`testkit.CleanupContainer`）：删除前核验所有权（不符时禁止删除），删除后
  重新检查容器是否真的不存在；状态无法确认按清理失败处理。"删除命令返回成功"不能作为证据。
- 执行器的资源提供者为远程容器登记 `Verify`：`docker ps -a` 清单里仍有该容器即判该资源释放失败；
  运行级终检再对全部提供者做一次快照比对，把结论写进 `report.json` 的 `residual_check`。
- 容器基线差异只报告、不判失败：如果运行前存在同名或同前缀容器（例如设备维护者留下的历史容器），
  核验输出 `WARN REMOTE_CONTAINER_BASELINE_GONE` / `REMOTE_CONTAINER_PREEXISTING`，结论仍以本次
  运行是否新增残留为准。

## 规划运行时验收

`integration-remote-planning` 是 M2 规划运行时的真实环境验收入口：真实文件型 SQLite、真实 SSH、真实
远程 Docker Engine、真实只读规划容器与真实容器内 Pi CLI，全部通过生产服务装配
（`app.ContainerService` / `app.PlanningRuntimeService` / `app.PlanningAgentService` /
`app.PlanningSessionCoordinator` / 当前 `storage.Store` / `app.RoleService`），不存在第二套容器管理、
Pi Runner 或 Session 管理实现。

| 用例 | 覆盖内容 |
|---|---|
| `TestRemotePlanningReadOnlyEnvironment` | 真实 `docker inspect` 的只读 rootfs / 只读代码卷 / tmpfs / 无 bind 与 Socket；真实文件工具与 Shell 写入被文件系统拒绝；`Release` 真实删除容器与卷、重复释放幂等 |
| `TestRemotePlanningMultiRoundSessions` | 两个规划角色的真实 Pi 调用、角色会话标识与容器内 / 本机路径隔离、同一角色多轮上下文连续、取消的调用不覆盖最后一份完整归档 |
| `TestRemotePlanningContainerRestartRecovery` | 生产 `StopContainer` 停止容器并核验 `exited`；关闭服务与 SQLite 后用同一数据库文件与 Session 根目录重建并恢复历史 Session |
| `TestRemotePlanningSSHInterruptionTermination` | 真实 Pi 执行期间关闭 SSH 连接；独立连接核验容器退出与进程消失；中断后可复用同一容器继续上下文 |
| `TestRemotePlanningPausedState` | 真实 `docker pause` 后的状态字段；启动、执行、`Prepare` 复用全部拒绝且不改写状态 |
| `TestRemotePlanningRestartingObservation` | 用一次性容器尝试观察真实 `restarting`；`dead` / `removing` 未制造时如实报告未观测 |
| `TestRemotePlanningPreconditions` | 记录 Docker CLI / Engine / API 版本、测试镜像、容器内 Pi 与 Git 版本、模型标识与测试仓库分支 |

测试镜像 `tests/runner/integration/remote/planning/Dockerfile`（`agentflow-test-planning:1.0.0`）在通用
测试镜像之上只设置 `PI_CODING_AGENT_DIR=/tmp/pi-agent`：规划容器 rootfs 只读，唯一可写目录是 `/tmp`
（tmpfs），模型接入配置由测试在运行期写入该目录，容器删除后随之消失。

## 前端用例

`frontend` 分组一条文件一条 Case：13 条 Vitest（`vitest-*`）与 24 条 Playwright（`playwright-*`），与
迁移前的 361 条 Vitest 用例、297 条 Playwright 用例逐条对应。

- dev server 由执行器的 `frontend-dev-server` 提供者启动（端口由 OS 分配，通过
  `AGENTFLOW_UI_BASE_URL` 注入），Playwright 的 `webServer` 已删除；释放后自证进程、端口与 HTTP
  三重消失。
- `browser` 与 `node-worker` 资源只分配会话目录，并核验用例登记的 CLI 进程真的退出。
- `playwright.config.ts` 固定 `workers: 1`，一条 Case 一次 CLI 调用；并发只由调度器决定。产物（截图、
  trace、HTML 报告）写入注入的用例产物目录，`AGENTFLOW_UI_JSON_REPORT` 给出时追加 JSON reporter，
  两种结论冲突时一律按失败处理。

## 数据库约束测试

数据库结构遵循两条长期规则：**枚举字段不受数据库级封闭取值限制**，**业务 Schema 不声明外键**。
守点集中在 `tests/unit/store/`：仓储读写、未知枚举值原样保留、逻辑删除、唯一约束与非枚举 `CHECK`
仍生效、条件更新拒绝迟到结果、并发写入与事务原子性，以及 `migrations_test.go` 对全新数据库初始迁移
与重复打开不重复执行的确认。

`tests/integration/local/database/`（迁移后全量 Schema 审计、旧库升级与数据保留）已删除：Schema 审计
与旧库不兼容行为因此**目前没有自动化守点**，迁移脚本中的表结构或约束只能靠脚本自身、`make vet` 与
人工核对。

## 分层覆盖说明

以下链路只在 `tests/unit/**` 与 `integration-local-*` / `integration-remote-*` 分组里覆盖，全部使用
受控替身或真实 SQLite 文件，不依赖真实 Pi、SSH 或模型：

| 链路 | 覆盖位置 |
|---|---|
| 需求聚合删除 | `tests/unit/{domain,store,app,httpapi}` + `integration-local-scheduler`（已删除需求的任务不被领取、在途分派期间删除失败关闭）+ `integration-remote-codesaver`（结果不明时失败关闭） |
| 开发 → 验证闭环 | `tests/unit/{domain,app,httpapi,store}`：开发成功后启动验证、开发失败/取消不启动验证、必需检查失败不得完成、容器不可用不回退本机、失败反馈脱敏与持久化 |
| 任务级工作区初始化 | `tests/unit/{domain,app,store,httpapi}` + `integration-local-workflow`：任务级初始化不携带 `attempt_id`、`uncertain` 阻止重放、同容器重复初始化记 `skipped` |
| 启动恢复 | `tests/unit/{domain,app}` 的纯函数与失败路径 + `integration-local-workflow` 的重启一致性（关闭连接后用同一路径重新打开，只断言数据库事实） |
| 环境管理 | `tests/unit/{environment,app,store,httpapi}` + `integration-local-environment` + 前端的 `vitest-settings-environments` / `playwright-settings-environments` |

## 测试配置

需要真实外部依赖的用例统一由 `tests/config/` 下的 YAML 配置驱动，示例文件随仓库提交，真实配置文件
不入库。环境变量只用于在配置值中写 `${NAME}` 占位符；`AGENTFLOW_TEST_CONFIG_DIR` 仍然存在，仅用于把
测试指向另一份配置目录。

| 配置项 | 用途 | 默认 |
|---|---|---|
| `pi.enable` | 是否执行真实远程端到端验收 | `false` |
| `pi.model` / `pi.provider` | 真实调用使用的模型与 provider 前缀 | `group/free` / 空 |
| `ssh.enable` | 是否执行需要真实远程机器的用例 | `false` |
| `ssh.host` / `ssh.user` / `ssh.port` | SSH 目标 | 必填 / 必填 / `22` |
| `ssh.private_key_path` | 私钥路径（只支持私钥认证） | 连接类用例必填 |
| `ssh.known_hosts` | 只读 known_hosts 列表；留空时复用本机 `~/.ssh/known_hosts` 与 `known_hosts2` | 空 |
| `docker.remote_enabled` | 显式启用真实远程 Docker 用例 | `false` |
| `docker.agent_image` / `docker.git_image` | 测试镜像引用 | `agentflow-test-agent:1.0.0` / 必填 |
| `docker.pi.*` | 容器内 Pi 的模型接入覆盖 | 空 |

```bash
cp tests/config/example.ssh.yml tests/config/ssh.yml   # 填写内容并把 ssh.enable 设为 true
```

known_hosts 来源由 `tests/internal/testkit` 的 `KnownHostsSources` 统一解析：显式配置时优先，文件
不可读时直接让测试失败；未配置时复用本机用户 OpenSSH 的 known_hosts。解析结果先快照复制进当前用例
工作空间，校验只读快照，因此不会读取或修改用户真实配置。产品侧 known_hosts 全程只读，测试遵守同一
边界。

## 编写测试的约定

- 使用 `testkit.New(t)` 创建工作空间，用 `testkit.Dir(t)` 取代 `t.TempDir()`；不要用 `t.Setenv` 切换
  全局数据目录。
- 每个用例的数据库、Session、输出文件路径必须来自自己的工作空间。
- 需要真实外部依赖时，由用例声明资源并读取配置，在缺失时输出 `WARN [SKIP]` 并给出原因。
- 成功判定必须基于具体业务结果（退出码、数据库记录、文件内容、状态字段），不能只依赖日志文本。
- Docker 相关用例必须在 `tests/config/docker.yml` 中显式启用 `docker.remote_enabled` 才允许修改真实
  远程 Docker；本机用例一律使用执行替身或路径映射，不依赖本机是否安装 Docker。
- 测试结束不得删除测试工作空间与产物；真实远程测试的远程测试目录同样保留（只回收测试容器，不删除
  共享镜像、缓存卷与非测试容器）。
- 共用同一台真实设备的用例必须声明为不可并行，并在包注释写明原因（例如环境镜像标签会被并行构建
  改指）。

## 发布制品测试

发布制品与发布流程目前**没有自动化测试**：`tests/unit/release/` 与 `tests/e2e/packaging/` 已在测试
精简中删除，`release.yml` 的运行冒烟 Job 一并移除。

`internal/release.SupportedPlatforms` 仍是发布矩阵的唯一来源，不在矩阵内的平台会在构建时被拒绝；制品
布局、`SHA256SUMS`、清单与来源信息声明只能靠 `make release-dist` / `make release-verify` 与发布流程
本身校验。平台的"交叉编译成功"与"已完成运行验收"因此不再有自动化区分手段。
