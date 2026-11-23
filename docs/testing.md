# 测试规范

本文件是 AgentFlow 的测试规则：授权边界、唯一执行入口、用例模型、并发与调度、资源与零残留、
测试工作空间与产物、真实外部依赖、覆盖对账与结果报告。测试目录在仓库中的位置见
[workspace.md](workspace.md)，用例包与操作细节见 [tests/README.md](../tests/README.md)，前端工程见
[frontend.md](frontend.md)。执行器内部实现见 [internal/testrunner/](../internal/testrunner/)，
冻结决策与已接受的回退见 [.steps/V3-final.md](../.steps/V3-final.md)。

## 1. 授权与默认验证

- 默认只做非测试验证：阅读代码、检查 `git diff`、`gofmt`、`go vet`、类型与引用检查。
- 未经用户明确要求，不运行测试、不创建测试文件、不编写临时验证脚本，也不通过"测试后删除"的方式绕过
  该限制。授权按任务或阶段给出，范围是明确列出的命令与用例集合；范围外的一律不执行。
- 授权范围内的运行必须如实报告通过、失败、跳过与未执行，并给出产物路径，不得只报通过数。
- 数据库结构变更只追加 `internal/storage/sqlite/migrations/` 下的版本化脚本，不手写版本判断，也不为
  Goose 自身的迁移调度、版本表或 SQLite 事务编写重复实现的测试（见 `AGENTS.md` 的「数据库约束规范」）。

## 2. 目录

```text
tests/
├── testkit/                通用测试辅助：工作空间、日志、known_hosts 快照与容器核验
├── unit/                   unit 用例的来源包路径（V4 R9 起无 `_test.go`，用例由执行器运行）
├── registry/               用例包聚合入口（空导入）
├── integration/local/      本机集成用例（9 个分组）
├── integration/ssh/        真实 SSH 主机用例
├── integration/remote/     真实远程 Docker / Agent / 代码保存 / 规划 / 工作流用例
├── e2e/cli/                CLI 端到端用例
├── frontend/               Vitest 与 Playwright 适配用例
├── selfcheck/              执行器自检：跳过、超时、迟到结果、取消
├── resourcecheck/          资源提供者自检：分配、释放、自证、故障注入
├── concurrencycheck/       调度场景与 worker 池：并发上限、容量、独占键、包级串行、额度、复用、独占、取消
├── pilot/                  R4 试点用例（真实服务进程）
└── config/                 真实外部依赖配置（示例文件入库，真实配置不入库）
```

- 所有测试代码与测试入口都必须放在根目录 `tests/` 下；禁止在生产源码目录（`internal/`、`cmd/`）新增
  或保留测试文件。
- `tests/integration/local/workflow/` 是应用层编排的集成用例：使用真实 SQLite 文件与真实
  `app.TaskWorkflowService`，只把外部依赖（Agent 执行、容器核验、单步骤闭环）替换为受控替身。恢复类
  用例必须"关闭连接后用同一路径重新打开"来模拟进程退出，并只断言数据库里的持久化事实（任务状态、
  步骤、执行记录、会话、事件），不得以方法返回值代替持久化断言。
- `tests/testkit/` 只存放通用测试辅助代码，不存放产品实现，也不存放需要额外执行的验证脚本。

## 3. 唯一执行入口

| 范围 | 入口 | 本地命令 | CI Job |
|---|---|---|---|
| 全部已注册 Case（含 unit 面） | `agentflow-test` | `make test` / `make test-cases` / `make test-real` | `cases-unit`、`cases-local`、`cases-e2e`、`cases-selfcheck`、`ui-test`（9 个领域分片）、`ui-gates`、`real-tests` |

- 集成测试、E2E 测试、前端浏览器测试与从 `tests/unit/**` 迁入的 unit 用例只有 `agentflow-test` 一个
  入口，不得再用 `go test` 执行任何用例，也不得用 `pnpm test:e2e:safe` 一类的平行入口启动 dev server。
  仓库里不存在 `_test.go`，因此 `go test` 不是门禁：它不执行任何用例，也不会产生"跑 0 条返回 0"的结论。
- 前端单元测试 `pnpm test`（`vp test run`）保留为开发者本机入口，不参与 CI 门禁，也不替代执行器的
  `frontend` kind 下的 9 个领域分组。CI 通过 `--group` 单独启动每个领域，所有 37 条前端文件级 Case 全部保留；
  `make test-cases TEST_KIND=frontend` 不传 `--group` 时仍会一次执行全部 9 组。
- `bin/agentflow-test` 由 `make test-runner` 构建；worker 由同一二进制启动，因此入口必须是最新构建，
  `make test-cases` / `make test-real` 会先构建再运行。worker 有两种启动方式：常驻 worker
  （`--exec-worker`，由编排进程复用执行多条用例）与一次性 worker（`--exec-case`，独占用例与单条调试）。
- `pilot` 试点用例把 `<repo>/bin/agentflow` 声明为 `service-process` 的可执行文件，因此
  `TEST_KIND` 为 `normal`、`selfcheck` 或 `fault` 时 `make test-cases` 会先 `make build` 再运行；
  其它 kind 只构建 `bin/agentflow-test`。CI 的 `cases-selfcheck` Job 有同一条前置，缺少该二进制时
  资源分配失败并被记为 `error`，不会被当成跳过。

执行器的选择条件只有三种写法：分组名（如 `frontend-planning`）、精确用例 ID（如
`integration-local-scheduler/...` 的完整 ID）、以 `...` 结尾的前缀（如 `integration-local-...`）。
没有排除语法，也没有 `--count`：重复运行即重新执行。

## 4. 用例模型

一条 Case 由 `internal/testrunner.Case` 描述，注册在 `tests/registry/` 聚合的包里：

| 字段 | 含义 |
|---|---|
| `ID` | 全局唯一标识 `<group>/<name>`，同时是产物目录名的一部分 |
| `Group` | 选择与并发决策的分组，留空时取 ID 第一段 |
| `Timeout` | 单条用例上限，零值使用运行级默认（10 分钟） |
| `Package` | 来源 Go 测试包路径：串行作用域与包级串行判定按它进行 |
| `Parallel` | 是否允许与其他用例并行；`false` 表示在串行作用域内独占 |
| `DedicatedWorker` | 是否必须独占一个全新的 worker 进程；默认 `false`，由常驻 worker 复用执行 |
| `Concurrency` | 并发额度声明：包级上限与参与的命名并发池 |
| `Resources` | 资源声明，由编排进程分配后以句柄注入 worker |
| `Run` | 用例主体，只在 worker 子进程中执行 |

- 一条 Case 独占"一次执行"：默认由常驻 worker 顺序执行多条用例，用例之间重置工作目录、环境变量与
  日志文件（包级变量由用例自己或 `DedicatedWorker` 处理）；`DedicatedWorker` 用例使用全新的一次性
  worker 并在结束后退出。因此 `panic`、`os.Exit` 只影响该条用例的那次执行，stdout/stderr 仍按用例
  分流到各自产物目录（见 §7 的产物布局）。
- 迁移写法固定为 `func run<原函数名去掉 Test 前缀>`；一条源测试函数内的多个 `t.Run` 子测试用
  `Context.Run` 表达（单层、顺序执行），不得用 `testing.T`。
- 用例的失败、跳过与超时必须通过 `Context` 表达，跳过必须带原因；不得把跳过写成通过。
- 真实设备用例只声明本地独占资源与远程资源声明，不在用例里创建或清理共享资源（见 §6）。

## 5. 并发与调度

- 并发只由 `internal/testrunner/scheduler.go` 的调度器决定：`--parallel`（默认 8）是同时在飞的 worker
  上限，用例无法通过代码自行扩大并发；`t.Parallel()` 不再决定并发，原有声明统一映射为
  `Case.Parallel`。
- 调度输入是五类约束：全局 `--parallel`、资源容量（同类资源同时在用的上限）、独占键（同一键的用例
  不得并行）、包级串行（`Case.Package` 相同的用例串行）、并发额度（包级上限 `--package-limit` 与命名池
  `--pool name=limit`）。
- worker 池：`--parallel` 同时也是槽位数，一个槽位对应一条常驻 worker 进程，进程在用例之间保留并复用，
  空闲槽位优先复用已有进程；`DedicatedWorker` 用例由槽位临时启动一次性 worker，执行完即退出，不复用。
  独占段（`DedicatedWorker`）优先派发，但可复用队列非空时独占派发不得拿走最后一个空闲槽位。派发顺序只
  由 ID 升序与当前占用决定，同一输入两次运行一致。
- 用例不再自行创建 worker：`--exec-worker` 与 `--exec-case` 都是编排进程的内部入口。
- 前端浏览器的实际并发等于调度器派发的用例数：`playwright.config.ts` 固定 `workers: 1`，一条 Case
  一次 CLI 调用，不再读取任何 worker 环境变量。

## 6. 资源与零残留

- 资源类型是冻结词汇表（`internal/testrunner/resource.go`）：`docker-daemon`、`remote-container`、
  `remote-workspace`、`ssh-host`、`ssh-session`、`local-ssh-server`、`sqlite-db`、`local-port`、
  `local-http-server`、`child-process`、`service-process`、`git-worktree`、`node-worker`、
  `frontend-dev-server`、`browser`。
- 作用域只有两种：`run-shared`（一次运行内同一声明只分配一次，多个用例复用）与 `case-exclusive`
  （每个用例各分配一份，用完立即释放）。用例只消费 `handles.json` 里的序列化句柄，不自行创建或清理
  共享资源。
- 释放可重复调用，释放失败使该资源与整次运行都失败；跳过不等于通过，一次正常运行结束后
  `residual_check.status` 不允许停留在未执行。
- 零残留是两级核验：资源级在每个资源释放后自证消失（`docker ps -a`、远程目录、端口、SSH 会话计数
  等）；运行级在编排进程退出前对全部提供者做一次快照比对，结论写入 `report.json` 的
  `residual_check`（`passed` / `failed` / `not-applicable`）。残留或无法确认都按失败处理。
- 测试工作空间（`TEST_ARTIFACT_DIR` 与 `.test-artifacts`）是保留产物，不参与残留判定。

## 7. 测试工作空间与产物

```text
<artifact-root>/<run-id>/report.json
<artifact-root>/<run-id>/cases/<case-id>/{stdout.log,stderr.log,result.json,artifacts/}
<artifact-root>/<run-id>/workers/<worker-序号>/stderr.log  常驻 worker 自身的标准错误（用例输出不在这里）
<os.TempDir()>/lingting-agent-flow-tests/<run-id>/<case-id>/           用例工作空间
<os.TempDir()>/lingting-agent-flow-tests/<run-id>/shard_<type>_<id>/   资源存放目录
```

- `<artifact-root>` 默认 `.test-artifacts`（`agentflow-test --artifact-dir`，Make 入口为
  `TEST_ARTIFACT_ROOT`）；`<run-id>` 形如 `20260101t120000z-1a2b`，历史运行目录不会被覆盖，失败的运行
  保留全部产物。
- 用例的数据库、WAL/SHM、Session、日志、配置与生成文件必须落在该用例的工作空间内，不得写入真实项目
  目录、用户默认数据目录或其他用例的工作空间。
- 使用 `tests/testkit` 提供的 `testkit.New(t)` 与 `testkit.Dir(t)`，不要用 `t.TempDir()`，也
  不要用 `t.Setenv` 切换全局数据目录；路径通过结构体、构造参数或子进程独立环境变量传入。
- 配置文件中的数据库与工作区路径必须是工作空间内的绝对路径：相对路径会按进程工作目录解析，把 SQLite
  文件写进仓库。配置模板中的路径一律使用占位符，由统一的辅助函数替换为绝对路径。
- 仓库忽略任意层级的 `.agentflow/` 目录，作为误用相对路径时的兜底；不得把运行期数据文件提交进版本库。
- 测试结束禁止删除工作空间与测试产物：不调用 `os.RemoveAll` 删除测试根目录，不使用会在结束时自动清理
  目录的临时目录 API，也不在 `t.Cleanup` 中删除目录。允许关闭数据库连接、终止测试启动的子进程、关闭
  监听端口等运行时资源。
- 应用服务日志统一用 `testkit.Logger` / `testkit.Slog` 构造：它们写当前标准输出，由执行器落到该用例的
  产物日志（`cases/<case-id>/stdout.log`）。逐条输出、跳过原因与失败现场都在产物目录里，不需要
  `TestMain` 一类的进程级日志重定向。

## 8. 真实外部依赖

真实测试的启用是**两层门禁**，两层都必须通过：

1. **CI 授权门禁（只在 GitHub Actions 生效）**：`GITHUB_ACTIONS=true` 时，用例要求
   `AGENTFLOW_REAL_TESTS_GRANTED=true`（由 `real-tests-gate` Job 依据 GitHub Repository Variable
   `AGENTFLOW_REAL_TESTS` 与事件身份字段产出，规则只实现一次于 `internal/cigate`）。未获授权时以
   `WARN [SKIP]` 跳过。用例只读取这个标记，不读取 `AGENTFLOW_REAL_TESTS` 本身。
2. **本机配置开关（任何环境都生效）**：`tests/config/*.yml` 中的 `pi.enable`、`ssh.enable` 与
   `docker.remote_enabled`；文件缺失、开关为 false 或目标字段不全时跳过并说明缺少哪一项。

- 执行器在编排进程里做**一次**真实依赖判定，结论通过 `real_tests` 字段与 worker 参数下发：`--real-tests`
  取 `auto`（按两层门禁判定）、`on`（强制允许）、`off`（强制禁止）。普通门禁一律传 `off`，因此普通
  门禁不可能选中或执行真实分组。
- 本机开发环境（没有 `GITHUB_ACTIONS` 标记）不经过第 1 层：`tests/config` 已启用真实依赖时，
  `make test-real` 会真实连接测试设备。跳过只能来自"未获授权"或"未提供配置"，配置完整但连接、认证或
  主机密钥校验失败时必须让测试失败。
- 真实分组共 32 条（`integration-ssh`、`integration-remote-*` 6 个分组），只能由 `make test-real`
  （要求 `AGENTFLOW_REAL_TESTS_GRANTED=true`）或 CI 的 `real-tests` Job 触发，包之间固定
  `--parallel 1` 串行。
- 本机不执行 Pi：所有 Agent 调用都在容器内，测试不检查本机是否安装 `pi`，也不读取本机 Pi 可执行文件
  路径；容器内 Pi 的可用性由 `tests/integration/remote/agent/` 在容器内执行 `pi --version` 与
  `pi --list-models` 确认。
- 真实模型调用由 `tests/config/pi.yml` 的 `pi.enable` 显式开启（当前只作用于远程规划与工作流用例），
  默认模型为 `group/free`；每次真实调用都必须显式传入模型，不得回退到 Pi CLI 配置中的默认付费模型。
  模型在清单里挂在不含模型前缀的 provider 下时，用同文件的 `pi.provider` 补充 provider 前缀（由
  `testkit.PiConfig.ResolvedModel` 统一组装，留空时原样使用模型标识）；容器内 Pi 的模型来自
  `docker.yml` 的 `pi.*`。
- 真实 SSH 目标统一来自 `tests/config/ssh.yml`（`ssh.host`、`ssh.user`、`ssh.port`、
  `ssh.private_key_path`、`ssh.known_hosts`），由 `ssh.enable` 显式开启；测试配置禁止再通过
  `AGENTFLOW_TEST_SSH_*` 等环境变量逐项读取。
- `ssh.known_hosts` 留空时复用本机用户 OpenSSH 的 `~/.ssh/known_hosts` 与 `known_hosts2`，与产品默认
  行为一致；显式填写但文件不可读时直接让测试失败，不得静默回落到空信任库。来源解析统一由
  `tests/testkit.KnownHostsSources` 提供。
- 解析出的 known_hosts 来源先快照复制进用例工作空间，校验只读快照：产品与测试都不得写入用户真实
  known_hosts。主机密钥未知的失败信息必须列出实际读取的路径与可用配置项，使"配置写错"与"连接层缺陷"
  可以区分。禁止关闭主机密钥验证，禁止自动接受未知主机密钥。
- 真实远程 Docker 用例要求 `docker.remote_enabled` 与 `ssh.enable` 同时启用，只操作带随机后缀、属于
  本次运行的镜像标签、容器、卷与远程目录，禁止执行全局 prune。
- 共用同一台设备的真实分组在包内保持串行（`remote/docker` 按 R5 D12 为 `Parallel: false`）：环境镜像
  标签由环境、版本号与构建配置哈希共同决定，并行构建会把同一标签改指到不同镜像。`remote/workflow`
  在 R7 D6 复核后解除整包串行，其资源、数据库、容器名与分支本就按用例隔离。
- 用例超时用 `--timeout`（默认 10m）与 `Case.Timeout` 表达；真实套件自带分钟级预算，需要时按分组单独
  放宽，超时仍按失败上报。

## 9. 覆盖对账（已移除）

V3 的覆盖对账以 R0 冻结清单为分母核验“迁移没有静默丢用例”。清单、`--audit-*` 参数、`dropped` 登记与
盘点工具已在 V4 R0 一并删除：仓库内不再保存历史用例分母，`go-inventory` Job 只做测试分组核验。
V4 的 unit 迁移不再用清单对账，改为按包逐条迁移并在批次记录中登记；V4 阶段文档已随 V4 收口移除，
需要细节时查 Git 历史，结论保留在 [`.steps/V4-final.md`](../.steps/V4-final.md)。

## 10. 常用命令与 CI 对应

```bash
make check                        # gofmt 检查 + go vet + go build，不执行任何测试
make test                         # 全部普通 Case（等价于 make test-cases TEST_KIND=normal）
make test-cases                   # 全部普通 Case（local + e2e + selfcheck + frontend + unit）
make test-cases TEST_KIND=local   # 只跑本机集成分组
make test-cases TEST_KIND=e2e     # 只跑 CLI 端到端分组
make test-cases TEST_KIND=selfcheck
make test-cases TEST_KIND=frontend
make test-cases TEST_KIND=unit
AGENTFLOW_REAL_TESTS_GRANTED=true make test-real   # 真实设备分组（需单独授权）
./bin/agentflow-test --list                        # 列出已注册用例（不执行）
```

| CI Job | 内容 | 本地等价入口 |
|---|---|---|
| `go-static` | 格式、静态检查与编译 | `make check` |
| `go-inventory` | 测试分组核验（残留 go test 测试包、Case kind 声明，不执行用例） | `bash .github/scripts/go-test-groups.sh --check ...` |
| `cases-unit` | unit 用例（`tests/unit/**` 迁入） | `make test-cases TEST_KIND=unit` |
| `cases-local` | 本机集成分组 | `make test-cases TEST_KIND=local` |
| `cases-e2e` | CLI 端到端分组 | `make test-cases TEST_KIND=e2e` |
| `cases-selfcheck` | 执行器与资源自检分组 | `make test-cases TEST_KIND=selfcheck` |
| `ui-test` | 前端用例：9 个领域分片（shell、planning、projects、requirements、settings、task-intake、task-execution、task-recovery、task-resources） | `make test-cases TEST_KIND=frontend`（一次跑全部领域） |
| `ui-gates` | `pnpm check` / `pnpm build` / `check-frontend-clean.sh` / `make web-verify` | `pnpm check && pnpm build`，再运行 `make web-verify` |
| `real-tests-gate` | 只读判定真实测试是否授权 | 无本地等价入口 |
| `real-tests` | 真实外部依赖分组（授权通过后） | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real` |
| `ci-summary` | 汇总各 Job 结论 | 无（仅 CI） |

故障注入分组（`*-fault`，16 条）不进门禁：它们按设计注入失败并以非零退出码结束，需要时按
`./bin/agentflow-test selfcheck-fault` 一类的选择条件单独执行。分组声明只有一个来源
（`.github/scripts/go-test-groups.sh`），`--check` 直接核验仓库里没有残留 go test 测试包、Makefile 不提供
go test 入口，并对执行器 `--list` 的注册表、Makefile 入口与消费脚本核验。

## 11. 结果报告

- 用例结论只有六种：`passed` / `failed` / `skipped` / `panic` / `timeout` / `error`。跳过必须带原因且
  不计入通过；worker 没能产出可信结果（启动失败、异常退出、结果文件缺失）按 `error` 处理。
- 统一报告是 `<artifact-root>/<run-id>/report.json`：选择条件、真实依赖授权结论、用例结论、资源结论、
  零残留结论、并发与取消记录、worker 池观测（`workers`：`total` / `reuse` / `restarts` /
  `isolated_cases`）、每条用例的 `worker_id` 与退出码都在其中，路径可复现。
- 全部结论只来自 `report.json`：跳过统计只读 `totals.skipped` 与每条 `skip_reason`，不解析日志文本。
- 取消或超时会终止 worker 进程树（Windows 用 `taskkill /T /F`，回收失败判 `error`），迟到结果不得覆盖
  已确认结论；被中止的编排进程不会执行零残留终检，残留需要人工核验（见 [runbook.md](runbook.md)）。

## 12. 已接受的能力边界

| 能力 | 结论 |
|---|---|
| `go test -race` | 本次重构不保留（R0 §2.3），`test-race` 目标与竞态 Job 已删除；恢复路径是 worker 的 `-race` 变体 + `--race` 选项，目前未实现 |
| 覆盖率门禁 | 不新增（现状本来没有）；恢复路径同上 |
| `t.Parallel()` 等价语义 | 不保留：1744 处声明统一映射为调度器属性，并发由调度器决定 |
| 中止运行后的零残留终检 | 不做：编排进程被中止时残留需人工确认 |
