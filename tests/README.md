# AgentFlow 测试

本目录是 AgentFlow 唯一的测试代码位置。生产源码目录（`internal/`、`cmd/`）不再存放
测试文件，新增测试一律放在本目录下。

测试的规范条款（目录分层、独立入口、测试工作空间、真实依赖开关、结果报告）以
[`docs/testing.md`](../docs/testing.md) 为准；本文件只说明各测试套件的目录、入口与操作细节。

## 目录结构

```text
tests/
├── internal/
│   └── testkit/        # 统一测试入口（testkit.Main）、日志落盘、工作空间、known_hosts 快照与测试数据构造
├── unit/               # 快速单元测试，不依赖真实 Pi、SSH 或网络
│   ├── agent/          # Agent 事件、结果类型与容器内 Pi Runner（使用替身）
│   │   └── pi/         # Session 目录与归档路径构造
│   ├── app/            # 应用服务层
│   ├── cigate/         # 真实测试授权判定（只读、fail closed）
│   ├── container/      # 容器状态、生命周期策略与 Docker 命令参数构造
│   ├── domain/         # 领域模型与状态迁移
│   ├── environment/    # 环境类型、版本号与镜像引用规则
│   ├── execenv/        # 执行环境规格、工作区与生命周期策略
│   ├── httpapi/        # HTTP API 处理器：用户操作路径、错误结构与 API 路径边界
│   ├── image/          # 构建上下文哈希、Dockerfile 校验与语言模板
│   ├── lifecycle/      # 后台服务生命周期：启动顺序、逆序停止、故障传播与停止超时
│   ├── server/         # 服务端进程锁与启动守卫（真实 SQLite 与真实文件锁）
│   ├── source/         # Git Clone 代码来源（容器执行替身）
│   ├── store/          # SQLite 仓储与数据库约束
│   └── testkit/        # 测试辅助逻辑：文件缺失探针分类、Pi 模型标识组成
├── integration/
│   ├── local/          # 本机集成测试，不依赖外部服务
│   │   ├── codesaver/  # 真实 Git 提交、推送与合并（本机 bare 仓库）
│   │   ├── environment/# 环境版本文件写入与清单读写（真实文件系统）
│   │   ├── execenv/    # 执行环境准备与容器状态（真实 SQLite）
│   │   ├── fetcher/    # 任务级工作区初始化的真实 Git 代码获取
│   │   ├── scheduler/  # 项目级调度：准入、排序、领取、名额、租约、并发竞争与需求删除竞争
│   │   ├── session/    # Session 提取与持久化边界（真实文件系统）
│   │   ├── source/     # 容器内 Git Clone 与任务分支（真实 Git）
│   │   ├── ssh/        # 本地模拟 SSH/SFTP 服务端
│   │   ├── validation/ # 验证执行与检查结论（真实 SQLite）
│   │   └── workflow/   # 应用层编排、取消与收尾一致性（真实 SQLite）
│   ├── remote/
│   │   ├── agent/      # 需要真实远程容器与容器内真实 Pi CLI
│   │   ├── codesaver/  # 需要真实远程容器与容器内 Git
│   │   ├── workflow/   # 需要真实远程容器与真实工作流前置条件
│   │   └── docker/     # 需要真实远程 Linux 机器与真实 Docker Engine
│   └── ssh/            # 需要真实远程 Linux 机器
└── e2e/
    └── cli/            # CLI 完整用户操作路径
```

## 测试入口

每个目录都是独立的 Go 测试包，可直接用 `go test` 执行，无需额外脚本解析日志。所有入口
统一经 `testkit.Main` 启动，因此即使不显式加 `-v`，本次运行的逐用例结论与跳过原因也会
写入 `<TEST_ARTIFACT_DIR>/test.log`（见「运行日志」）。

```bash
# 快速单元测试（日常反馈，不访问真实模型与远程网络）
go test -count=1 -v -p 8 -parallel 8 ./tests/unit/...

# 本机集成测试
go test -count=1 -v -p 8 -parallel 8 ./tests/integration/local/...

# 本机 Git 代码保存集成测试
go test -count=1 -v ./tests/integration/local/codesaver/...


# 真实 SSH 集成测试（在 tests/config/ssh.yml 中把 ssh.enable 设为 true 后才会执行）
go test -count=1 -v ./tests/integration/ssh/...

# 真实远程 Docker / Agent / 工作流集成测试
# （docker.yml 的 docker.remote_enabled 与 ssh.yml 的 ssh.enable 都必须为 true）
go test -count=1 -v ./tests/integration/remote/docker/...
go test -count=1 -v ./tests/integration/remote/agent/...
go test -count=1 -v ./tests/integration/remote/planning/...
go test -count=1 -v ./tests/integration/remote/workflow/...
# 真实包自带分钟级预算，本机单包执行时按需放宽 go test 超时：
go test -count=1 -v -timeout 45m ./tests/integration/remote/workflow/...

# CLI 端到端测试
go test -count=1 -v ./tests/e2e/...

# 完整测试
go test -count=1 -v -p 8 -parallel 8 ./tests/...

# 真实测试授权判定（CI 使用同一入口；只读判定，不连接任何真实设备）
go run ./cmd/agentflow-ci real-tests-gate \
  -mode self -event push -actor <账号> -repository-owner <所有者> \
  -github-output "$GITHUB_OUTPUT" -step-summary "$GITHUB_STEP_SUMMARY"
# 竞态检测（单元测试）
go test -race -count=1 ./tests/unit/...

# 竞态检测（业务并发写入与条件更新）
go test -race -count=1 ./tests/unit/store/ ./tests/integration/local/scheduler/

# 竞态检测（任务级工作流编排、调度与并发隔离）
go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/ ./tests/integration/local/scheduler/

# 竞态检测（SSH 连接管理与并发操作，需要真实协议交互）
go test -race -count=1 ./tests/integration/local/ssh/...
```

并发由 Go 测试工具负责：`-p` 控制同时运行的测试包数量，`-parallel` 控制单个包内
并行用例数量。用例内部通过 `t.Parallel()` 声明可并行，不依赖固定执行顺序。

CI 不会让 `go test` 按机器核数自行决定并行度：三个 Go 测试组与竞态检测都显式使用 4
（Runner 为 4 vCPU），真实测试在单 Job 内以 `-p 1` 串行执行多个真实套件。`-race` 下
SQLite 初始化与连接建立对并发量敏感，在高核数机器上用默认并行度会出现
`database is locked` 与初始化超时，因此竞态检测的并行度同样显式设置（`make test-race`
跟随 `PARALLEL`）。

## CI 门禁与本地入口的对应关系

`.github/workflows/ci.yml` 在 Pull Request、`master` push 与手动触发时执行。它不新增测试
逻辑，只调用本文件里的入口，因此本地复现失败的顺序与 CI 一致：

| CI Job | 覆盖范围 | 本地等价入口 |
|---|---|---|
| `go-static` | `gofmt -l cmd internal tests` 无输出、`go vet ./...`、`go build ./...` | `make fmt-check`、`make vet`、`make build` |
| `go-inventory` | Go 测试包清单与分组核验：`go list` 得到的真实测试包必须恰好被分组清单覆盖一次（遗漏、重复归属、分组未被任何 Job 执行都会失败） | `bash .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e` |
| `go-unit` | `./tests/unit/...`（不需要任何外部设备） | `make test-unit` |
| `go-local` | `./tests/integration/local/...`（本机 SQLite / 进程 / 本机 bare 仓库 / 进程内模拟 SSH 服务端） | `make test-local` |
| `go-e2e` | `./tests/e2e/...`（CLI 完整用户操作路径，不需要 pnpm 与前端依赖） | `make test-e2e` |
| `go-race` | `make test-race` 的全部范围 + 业务并发写入 + 调度与任务级工作流编排 | `make test-race`，再执行下文两条补充命令 |
| `ui-test` | 前端单元（`--maxWorkers 4`）、Playwright E2E（`--workers=4`）、格式、lint、类型检查、生产构建 | `cd lingting-agent-flow-ui && pnpm test:all:safe` |
| `real-tests-gate` | 只读判定真实测试是否获授权，输出 `real_tests_enabled`、`mode` 与 `reason` | `go run ./cmd/agentflow-ci real-tests-gate -mode ... -event ...` |
| `real-tests` | 仅授权通过时执行真实外部依赖测试包（见下文清单） | `go test -count=1 ./tests/integration/ssh/... ./tests/integration/remote/...` |
| `ci-summary` | 其余全部 Job 都成功才通过；分组核验失败算失败，授权判断失败算失败，真实测试未授权跳过不算失败但也不计为通过 | 无（仅 CI） |

这些 Job 的执行逻辑都在 `.github/scripts/` 下，工作流只负责触发、依赖与参数传递：脚本内部
调用的正是本地入口（`make test-unit`、`make test-local`、`make test-e2e`、`make test-race`、
`pnpm test:all:safe`、`go run ./cmd/agentflow-ci ...`、`make release-dist`），CI 与本地命令
因此不会各跑一套。分组归属、包模式与 Makefile 入口只定义在
`.github/scripts/go-test-groups.sh`，工作流与执行脚本都不重复列举测试包。

Go 测试的分组与并行策略：

- 三个普通分组（`unit` / `local` / `e2e`）各自对应一个 CI Job，互不依赖，由 GitHub Actions
  在不同 Runner 上并行执行；三者都依赖 `go-inventory`，分组核验失败时不会跑一半测试；
- 分组包集合互不重叠，并集等于 `make test` 覆盖的全部普通测试包；这一点由
  `go list` 的真实清单核验，而不是人眼比对；
- 每个分组 Job 独享一台 4 vCPU Runner，`go test` 只用 `-p 4` / `-parallel 4`；`go-race` 也
  显式传入 `--parallel 4`（`-race` 下 SQLite 初始化与连接建立对并发量敏感）；
- 真实测试（`ssh` / `remote`）不进普通 CI，并由 `real-tests` 单 Job 执行，包之间固定
  `-p 1` 串行：全部真实套件共用同一台远程机器与同一个 Docker Engine。

本地等价入口与全量入口的关系：`make test` 仍是本地全量入口（`tests/...` 全部测试包），
分组入口是它的子集划分，CI 用三个分组 Job 并行覆盖同一批普通测试包。

脚本清单、参数与退出码见 [`.github/scripts/README.md`](../.github/scripts/README.md)；脚本自身没有
专门的自动化测试，分组覆盖每次 CI 都由 `go-test-groups.sh --check` 机械核验。

`.github/workflows/release.yml` 是制品构建流水线，只做"构建与制品准备"：它由
`publish.yml` 复用（`workflow_call`）或手动触发（`workflow_dispatch`）运行，调用
`make release-dist` 构建五个平台制品，再用 `verify-release` 复核目录与制品元数据。它只上传
Actions 运行制品，不创建 GitHub Release、不推送镜像、不部署环境。**版本标签不再直接触发它**：标签发布
统一走 `publish.yml`，避免同一个标签被构建两次、产出两份不同的字节。

`.github/workflows/publish.yml` 是正式发布流水线，由版本标签推送或手动触发运行。它依次复用
`ci.yml`（全量门禁）与 `release.yml`（制品构建），然后在 `publish` Job 里做发布前核验、
生成来源证明、创建草稿 Release、上传、重新下载核验、核验来源证明，全部通过后才把草稿转为
公开 Release。发布权限（`contents: write`、`id-token: write`、`attestations: write`）只授予
该 Job。

| 发布阶段 | 覆盖范围 | 本地等价入口 |
|---|---|---|
| 版本解析 | 标签格式、ref 必须是标签、提交必须是完整 SHA | 无本地等价入口（解析本身由 `release.yml` 的 `resolve` Job 执行） |
| CI 门禁 | 复用 `ci.yml` 的四个必需 Job | 见上表 |
| 制品构建 | 前端产物校验 → 交叉编译 → 打包 → `SHA256SUMS` 与清单自检 | `make release-dist VERSION=v1.2.3` |
| 制品校验 | 用 `sha256sum -c` 与 `verify-release` 独立复核目录 | `cd dist/release && sha256sum -c SHA256SUMS`，`make release-verify VERSION=v1.2.3` |
| 发布前核验 | 版本 / 提交 / 平台覆盖 / 摘要 / 目录无多余文件 / 来源信息声明 | `go run ./cmd/agentflow-release verify-release -dir dist/release -version v1.2.3 -commit <sha> -require-provenance` |
| 来源证明 | 生成与核验只能由 GitHub Actions 完成 | 无本地等价入口（`gh attestation verify` 只能核验已生成的证明） |
| 上传后核验 | 远端资产列表 + 重新下载 + 摘要 + 来源信息 | `go run ./cmd/agentflow-release verify-assets -dir <下载目录> -version v1.2.3 -assets-file <资产名列表>` |
| 重复发布 | 已存在 Release（含草稿）时直接失败，不覆盖 | 无本地等价入口 |

**这些工作流尚未在 GitHub Actions 上真实运行**：本轮没有推送版本标签，也没有创建公开
Release。上表"本地等价入口"一列是可以在本地真实执行并已执行过的部分；远端上传、来源证明
签发与草稿转公开目前没有自动化测试覆盖，只能靠实际发布流程验证。
`go-race` 的补充范围（`make test-race` 未包含），竞态检测同样按 Runner 的 4 vCPU 显式限制
并行度（`-race` 下 SQLite 初始化与连接建立对并发量敏感，用机器核数兜底会在高核数机器上超时）：

```bash
go test -race -count=1 -p 4 -parallel 4 ./tests/integration/local/workflow/
go test -race -count=1 -p 4 -parallel 4 ./tests/integration/local/scheduler/
```

`make test-race` 本身已覆盖 `./tests/unit/...`（含 `unit/store`、`unit/app`、`unit/httpapi`、
`unit/domain`）与 `./tests/integration/local/ssh/...`，因此 CI 不再重复执行这些范围，
实际覆盖是上表所列范围的超集而不是子集；`make test-race` 的并行度跟随 `PARALLEL`（CI 传 4）。

### 真实测试授权与跳过语义

真实外部依赖测试（真实 SSH 设备、真实远程 Docker、容器内真实 Pi、真实远程 Git）的启用有两层门禁，
两层都必须通过：

1. **CI 授权门禁（只在 GitHub Actions 生效）**：`GITHUB_ACTIONS=true` 时，用例还要求
   `AGENTFLOW_REAL_TESTS_GRANTED=true`。该标记由授权判定 Job 产出，用户唯一需要维护的输入是 GitHub
   Repository Variable `AGENTFLOW_REAL_TESTS`（取值见下表）。
2. **本机配置开关（任何环境都生效）**：`tests/config/{pi,ssh,docker}.yml` 的 `pi.enable`、`ssh.enable`
   与 `docker.remote_enabled`；文件缺失或开关为 false 时跳过。

本机开发环境没有 `GITHUB_ACTIONS` 标记，**不经过第 1 层**：`tests/internal/testkit` 的
`RealTestsGateDecision` 直接按"本机运行环境不使用 CI 授权门禁"放行，是否执行只由 `tests/config`
决定。因此本机把 `tests/config` 里的开关设为 true 后，`make test`（`go test ./tests/...`）会真实连接
测试设备；`make test-unit` / `make test-race` / `make test-local` 不包含真实测试分组。测试代码不读取
`AGENTFLOW_REAL_TESTS` 本身，只读取上面两个门禁变量。

`AGENTFLOW_REAL_TESTS` 的取值（CI 内）：

```text
Settings → Secrets and variables → Actions → Variables → AGENTFLOW_REAL_TESTS
```

它不是 Secret，也不需要额外的凭据管理机制；未设置、为空或取值无法识别时一律关闭。

| 取值 | 授权范围 |
|---|---|
| `self` | 只有仓库所有者发起的 push 与 PR |
| `collaborator` | 仓库所有者、组织成员与仓库协作者 |
| `all` | 不区分作者身份，所有 push 与 PR |
| 其他任意值 | 关闭（`SELF`、`Collaborator` 等错误拼写同样按关闭处理） |

```text
# 个人开发、需要保护真实设备
self

# 确认仓库成员与协作者可信
collaborator

# 明确接受所有 PR 代码进入真实测试执行环境的风险
all
```

`all` 是主动放宽的信任策略，**不等于安全隔离**：启用后任意 PR 作者的代码都可能进入真实
测试执行环境并接触真实测试设备与凭据。任务执行权限与信任边界由这个取值决定，不是 GitHub
Actions 表达式能够兜住的问题。

判定规则集中在 `internal/cigate`（由 `cmd/agentflow-ci real-tests-gate` 在 CI 中执行），
不在工作流 YAML 里重复实现：

- 只看 GitHub 事件字段，不看 commit author 姓名或邮箱（这些字段由提交者自行填写，没有权限
  证明能力）。
- **push**：用 `github.actor` 判定 Push 发起者；`self` 要求它等于 `github.repository_owner`
  （忽略大小写）；`collaborator` 还要求该账号有仓库写入权限，查不出权限按拒绝处理。
- **pull_request**：用 `github.event.pull_request.author_association` 判定 PR **作者**
  （不是触发者）；`self` 只允许 `OWNER`，`collaborator` 允许 `OWNER` / `MEMBER` /
  `COLLABORATOR`；`CONTRIBUTOR` 只是历史贡献者，不算当前协作者。
- **其他事件**（含 `workflow_call`）：不参与身份判定，真实测试一律跳过。因此
  `publish.yml` 复用 `ci.yml` 时不会执行真实测试。
- 身份字段缺失、变量取值未知、权限查询失败都按拒绝处理（fail closed）。

真实测试包清单（`real-tests` Job 只执行这些，不重复跑普通 Go 测试）。范围由
`.github/scripts/go-test-groups.sh` 的 `real` 分组定义（`ssh` / `remote`），执行脚本
不自己列举包路径：

```text
tests/integration/ssh/                # ssh 分组：真实远程 Linux 机器
tests/integration/remote/docker/      # remote 分组：真实远程 Docker Engine
tests/integration/remote/agent/       # remote 分组：真实远程容器与容器内真实 Pi
tests/integration/remote/codesaver/   # remote 分组：真实远程容器与容器内 Git
tests/integration/remote/planning/    # remote 分组：真实规划运行时（只读容器 / 快照 / 容器内 Pi）
tests/integration/remote/workflow/    # remote 分组：真实远程端到端工作流
```

以下本机模拟测试**不属于**真实测试，继续在普通 CI 的 `go-local` 分组中执行：
`tests/integration/local/ssh/`、使用本机 SQLite 的调度与工作流测试、使用容器执行替身
的 Agent 测试，以及使用本机 bare 仓库的代码保存测试。

#### 跳过规则

所有真实测试入口在读取配置、建立连接、执行外部命令之前先调用
`testkit.RequireRealTests`，因此：

- 在 CI 中未获授权时用例输出 `WARN [SKIP] 真实测试未获 CI 授权：<套件>；<判定原因>` 并跳过；
  本机运行不会产生这条跳过；
- 跳过不计为通过，`ci-summary` 与 `real-tests` 的 Job 摘要单独标注这一点；
- 跳过不会让 `make test` 失败：只要没有真实测试之外的失败，整体 Go CI 仍然成功；
- CI 已获授权（或本机运行）但没有可用配置（配置文件缺失、`enable` 为 false、目标字段不全）时，各套件仍按
  原有 `WARN [SKIP]` 语义跳过并说明缺少哪一项；
- 配置完整但连接、认证或主机密钥校验失败时必须让测试失败——跳过只来自"未提供配置"或
  "未获授权"，不来自"配置存在但连不上"。
- 真实测试 Job 的执行清单只写在 `.github/scripts/run-real-tests.sh`，它要求
  `AGENTFLOW_REAL_TESTS_GRANTED=true` 才肯执行：缺少授权标记时脚本直接失败，不会跑出一堆
  SKIP 后把"一个用例都没跑"报告成成功。
- 该脚本用 `--timeout`（默认 `90m`）显式设置 `go test -timeout`：真实套件自带分钟级运行预算
  （`tests/integration/remote/workflow` 的端到端用例声明 60 分钟），`go test` 默认的 10 分钟
  上限会让它们必然超时；超时仍然按失败上报，不放宽任何断言。

要在本机复现 CI 的跳过行为，可以显式设置 CI 标记（默认本机运行不需要，见上文）：

```bash
# 复现"CI 未授权"：真实外部依赖用例全部 WARN [SKIP]，其余测试照常执行
GITHUB_ACTIONS=true make test

# 只跑真实测试包并观察跳过原因
GITHUB_ACTIONS=true go test -count=1 -v ./tests/integration/ssh/... ./tests/integration/remote/...
```

#### 凭据边界

- 真实测试的连接配置只来自 `real-tests` 环境的 Environment Secrets，由
  `.github/scripts/prepare-real-tests-config.py` 现场生成被 `.gitignore` 忽略的
  `tests/config/*.yml`；私钥与 known_hosts 写入 `RUNNER_TEMP` 下的临时目录（0600）。
- 普通 CI 的必需 Job（含 `go-inventory` 与三个 Go 测试组）与授权判断 Job 都不读取任何 Secret，
  fork PR 因此也无法获得真实设备凭据；缺少必需 Secret 时 `real-tests` 明确报告 SKIP，不伪造通过。
- 授权判断 Job 只检出可信提交（PR 事件检出基础提交），不执行被提交的代码；所有 Action 用
  提交 SHA 固定；`pull_request_target` 不被使用。
- 授权判定脚本（`check-real-test-authorization.sh`）只把事件上下文转发给
  `cmd/agentflow-ci real-tests-gate`，不读取 Secret、不重新实现身份判断；本机开发完全不走这条
  判定，仍由 `tests/config/*.yml` 的 `enable` 开关决定是否执行真实测试。
- 配置内容、私钥、令牌与认证头都不写入日志；真实测试日志只在失败时上传。

### CI 产物留存

| Job | 产物 | 触发条件 | 保留 |
|---|---|---|---|
| `go-unit` / `go-local` / `go-e2e` | 该分组的控制台输出、各测试包 `test.log`、`latest-run.txt` | 仅失败 | 7 天 |
| `go-race` | 竞态检测控制台输出、各测试包 `test.log`、`latest-run.txt` | 仅失败 | 7 天 |
| `ui-test` | Vitest 输出、Playwright HTML 报告、失败截图与 trace、完整控制台日志 | 仅失败 | 7 天 |
| `real-tests` | 真实测试控制台输出、各测试包 `test.log`、`latest-run.txt` | 仅失败 | 7 天 |

产物由 `.github/scripts/collect-test-artifacts.sh` 统一收集：它只把白名单文件（该 Job 的
`*-output.log`、各测试包 `test.log`、`latest-run.txt`）复制到暂存目录再上传，制品名为
`<job>-<run_id>-<run_attempt>`，同一提交的多次重跑不会互相覆盖。**不整体上传测试工作空间**：
其中含用例生成的临时私钥、SQLite 数据库与 Session 文件，这些不是诊断必需，也不该离开 Runner。
收集与上传两步都显式 `continue-on-error`，产物问题不会改写原本的测试失败结论。

每个分组还会把开始 / 结束时间、耗时、执行命令、退出码、测试包数量与顶层用例通过 / 失败 /
跳过数量写进 Job 摘要（同时落盘为 `<group>-summary.txt`）；这是本轮建立的 CI 耗时基线，
用于判断后续优化是否真实有效。测试包数量与用例数量分开记录：用例数量只统计
`go test -v` 的顶层结论行，子用例不计入。`ci-summary` 展示 `go-inventory` 输出的分组表与
各 Job 结论，不重新解析日志。

CI 中测试工作空间仍然落在 `os.TempDir()/lingting-agent-flow-tests/` 之下；Job 只把
`TMPDIR` 指向工作区内的 `.test-artifacts/` 目录，使失败产物可被 `upload-artifact` 取到。
本地运行时 `TMPDIR` 不变，产物位置与下文「测试产物」一致。

## 测试产物

每个测试包启动时在系统临时目录下创建随机根目录，并把绝对路径输出为：

```text
TEST_ARTIFACT_DIR=<绝对路径>
```

实际位置为 `os.TempDir()/lingting-agent-flow-tests/<包名>-<随机数>/`。每个用例在其中
获得独立子目录，数据库、Session、日志、上传下载文件都放在该目录内。

测试结束只关闭数据库连接、子进程与监听端口，**不删除任何测试产物**，便于检查失败
现场。需要清理时由开发者手动删除 `lingting-agent-flow-tests` 下的历史目录。

## 运行日志

每个测试包通过 `testkit.Main` 作为唯一入口（`os.Exit(testkit.Main(m))`），它把本次运行
的完整输出写入 `<TEST_ARTIFACT_DIR>/test.log`：

- 逐用例结论（`=== RUN` / `--- PASS` / `--- FAIL` / `--- SKIP`）；
- `t.Logf` 与 `fmt.Println` 等标准输出内容；
- `WARN [SKIP]` 跳过原因（例如配置未启用、缺少配置项）；
- 应用服务的 `slog` 日志：测试统一用 `testkit.Logger` / `testkit.Slog` 构造日志器。

入口同时默认打开 `-test.v`，因为非 verbose 模式下 `go test` 会缓冲包输出、只在失败时
打印，逐用例结论与跳过原因都会丢失；命令行显式给出 `-test.v=false` 时以命令行为准。

基目录下的 `latest-run.txt` 记录最新一次运行 `test.log` 的绝对路径，复核结论与跳过原因
时不需要重跑测试：

```bash
cat "$(os.TempDir)/lingting-agent-flow-tests/latest-run.txt"   # 具体路径按平台解析
```

所有测试包的 `TestMain` 都只转调 `testkit.Main`，不存在自行接管输出的分包入口。

## SSH 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/integration/local/ssh/` | 本机真实 SSH 握手与主机密钥校验、私钥认证、远程命令执行与取消、SFTP 传输、并发会话隔离 | 无（测试内启动服务端） |
| `tests/integration/ssh/` | 真实远程 Linux 机器上的认证、命令状态、超时、SFTP 与工作空间 | tests/config/ssh.yml 指定的 SSH 目标 |

本机集成测试使用 `golang.org/x/crypto/ssh` 启动真实的服务端进程内实例，并配合
`github.com/pkg/sftp` 的服务端实现，因此主机密钥校验、密码/私钥/Agent 认证、
SFTP 上传下载与远程命令执行都经过真实协议交互，但不访问外网。

所有 SSH 测试都禁止关闭主机密钥校验，禁止自动接受未知主机密钥。

### 主机密钥类型协商

服务端可能同时提供多种主机密钥（例如 ed25519 与 ECDSA），known_hosts 里可能记录了其中
若干种。`sshclient.Dialer` 会按 known_hosts 中该主机已记录的类型**逐个**尝试握手：每次
只用一种类型，校验通过并完成认证即成功；某个类型校验失败或服务端不再提供该类型时换下
一个。没有任何记录时不做限制，交给 `golang.org/x/crypto/ssh` 自身协商。

逐类型重试只针对主机密钥类失败（未知、已变化、已吊销、无共同算法）；TCP 不可达、超时、
取消与认证失败会立即返回，避免换类型掩盖真实原因。全部已记录类型都不可用时返回第一个
失败类型对应的结论。

测试覆盖：`tests/integration/local/ssh/dialer_test.go` 与 `hostkey_port_test.go` 验证未知主机密钥
被拒绝、已信任主机密钥可完成私钥认证、主机密钥变化被拒绝，以及带端口与哈希主机名的记录形式。
逐类型重试本身目前没有独立用例。
## 发布制品测试

发布制品与发布流程目前**没有自动化测试**：`tests/unit/release/`（版本元数据、发布矩阵、制品
命名、压缩包布局、清单与工作流结构）与 `tests/e2e/packaging/`（端到端打包验收、目标平台运行冒烟）
已在测试精简中删除，`release.yml` 的运行冒烟 Job 一并移除。

`internal/release.SupportedPlatforms` 仍是发布矩阵的唯一来源，不在矩阵内的平台会在构建时被拒绝；
制品布局、`SHA256SUMS`、清单与来源信息声明只能靠 `make release-dist` / `make release-verify` 与
发布流程本身校验。平台的"交叉编译成功"与"已完成运行验收"因此不再有自动化区分手段。

## Docker 测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/environment/` | 环境类型、版本号递增与复用判定、构建状态迁移（含 `uncertain`）、镜像引用与 ID 校验 | 无 |
| `tests/unit/image/` | 构建上下文确定性哈希、Dockerfile 静态校验、语言模板、构建配置哈希 | 无 |
| `tests/unit/container/` | 容器状态迁移、detached 语义、生命周期策略、缓存卷命名、创建参数安全边界、JSON 解析 | 无 |
| `tests/integration/local/environment/` | 版本文件不可覆盖、清单落盘一致性、构建上下文上传与清理 | 无（远程路径映射到本机目录） |
| `tests/integration/remote/docker/` | 真实远程预检、镜像构建、容器创建/启动/执行/停止/删除 | tests/config/ssh.yml 指定的 SSH 目标与真实 Docker |

容器命令构造与参数安全边界、结果解析与错误分类由 `tests/unit/container/` 覆盖（不依赖 Docker
Engine），远程预检与真实容器流程由 `tests/integration/remote/docker/` 覆盖。
本机环境测试把远程绝对路径映射到测试工作空间中的目录，因此文件写入、原子替换与
"历史版本不可覆盖"都在真实文件系统上验证。

真实远程 Docker 测试只使用带随机后缀的测试镜像标签、容器名称与远程目录，并只清理
自己创建并已核验所有权的测试容器；不会执行全局 prune，也不会删除共享镜像或缓存卷。

### 远程真实测试目录与容器零残留

- 远程主机上的测试资源统一位于 `<远程 Home>/.lingting/agentflow-tests/<测试包>/<用例标识>`，
  由 `tests/internal/testkit` 的 `RemoteTestRoot` / `ResolveRemoteTestRoot` 统一解析与创建；
  旧路径 `~/agentflow-tests` 与 `~/.lingting/agentflow-e2e` 已废弃，不再写入。
  子目录按测试包划分（`agent` / `docker` / `codesaver` / `workflow` / `ssh`），
  新增测试包必须在 `testkit` 的白名单中登记。
- 远程主机目录与容器内部目录是两套约定：`/workspace/project`、`/tmp/agentflow/sessions`
  等容器内路径不受该规则影响；本机测试产物仍使用
  `os.TempDir()/lingting-agent-flow-tests/`。
- 每个测试容器都必须在**创建之前**登记清理责任（`testkit.ContainerRun.Track`），取得真实
  Docker ID 后立即补写；创建失败、启动失败与后续步骤失败都不允许失去清理能力。
- 清理必须核验真实状态（`testkit.CleanupContainer`）：删除前核验所有权（所有权不符时
  禁止删除），删除后重新检查容器是否真的不存在；状态无法确认按清理失败处理。
  删除方法的返回值不能作为"容器已不存在"的证据，清理失败也不能只打印 `WARN` 后通过。
- 每个真实远程测试包在 `TestMain` 中通过 `testkit.RegisterContainerFinalizer` 注册包级
  最终核验：`testkit.Main` 在全部测试（含 `t.Parallel`）结束之后、日志刷新之前执行它，
  用真实 `docker ps -a` 清单确认本次运行的容器已全部消失；核验失败会让测试进程以非零码
  退出。远程测试未启用时该核验报告 `SKIP`，不虚构"零残留通过"。
- 校验发现的日志行固定为 `REMOTE_CONTAINER_CLEANUP`、`REMOTE_CONTAINER_EVIDENCE`、
  `REMOTE_CONTAINER_BASELINE` / `REMOTE_CONTAINER_FINAL`、`REMOTE_CONTAINER_RESIDUE`、
  `REMOTE_CONTAINER_PREEXISTING`、`REMOTE_CONTAINER_VERIFY` 与 `FINALIZER`。
- 机制与验收证据见 `.steps/remote-test-resource-cleanup.md`。

## 环境管理测试分层

环境管理（设置页内嵌化与后台构建）的测试全部使用受控替身，不依赖真实 SSH、Docker 或远程主机：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/environment/` | 版本号递增与复用判定（`domain.FindReusableVersion`：只有内容、父版本一致且已成功构建才复用）、`uncertain` 表示"结果无法确认"且不可用、已成功版本不得被改写成不确定 |
| `tests/unit/app/` | 后台构建管理器：受理返回真实版本快照、重复提交拒绝、节点/版本非法时不启动构建、未启动时拒绝受理、启动时把遗留 `building` 收敛为 `uncertain`、停止时取消在途构建并等其收敛、条件更新拒绝迟到的构建结果 |
| `tests/unit/store/` | `environment_versions` 的只追加约束：重复版本号被唯一约束拒绝，历史版本内容与哈希不被覆盖 |
| `tests/unit/httpapi/` | 环境写接口的参数校验（缺失 `host_id`、Dockerfile 非法、未知字段、非法类型/名称、方法不允许、未知子路径）、错误类别映射（400/404/409/412/503）、请求透传、构建受理返回 `202` 且不声称成功、构建状态查询区分成功/在途/失败/不确定、日志接口不接受路径参数且缺失时 404、只读查询回归 |
| `lingting-agent-flow-ui/tests/unit/` | 前端业务判定（`settings-environments.test.ts`）：环境状态标签、可用性只认服务端字段、构建结论归一（失败与不确定分别保留服务端原因）、节点选项与阻断原因 |
| `lingting-agent-flow-ui/tests/e2e/` | 浏览器用例：设置页承载环境管理、独立环境导航已移除、不自动选中默认节点、真实状态展示、构建失败原因、结果不确定、在途构建禁止重复提交、日志读取失败不显示为空、Dockerfile 编辑为新版本、切换节点丢弃迟到响应、不可用节点只读可看 |

环境相关用例的关键约束：

- 构建替身不调用 Docker：它只记录收到的 `BuildRequest`，或在 `ctx` 取消时返回错误，因此"停止取消"
  与"重复受理拒绝"都经过真实管理器的代码路径。
- HTTP 用例的写路径替身不建立 SSH 会话：协议转换与错误映射被完整覆盖，远程副作用由应用层与本机
  集成测试分别验证。
- 真实远程环境操作仍由 `tests/integration/remote/` 覆盖，需要 `tests/config/ssh.yml` 与
  `docker.yml` 显式启用。

## 测试配置

需要真实外部依赖的测试统一由 `tests/config/` 下的 YAML 配置驱动，示例文件随仓库提交，
真实配置文件不入库。环境变量只用于在配置值中写 `${NAME}` 占位符，不再有逐项覆盖配置的
测试专用环境变量（`AGENTFLOW_TEST_CONFIG_DIR` 仍然存在，仅用于把测试指向另一份配置目录）。

### 真实 Pi 模型调用

真实模型调用测试的全部配置来自 `tests/config/pi.yml`（示例文件为 `example.pi.yml`）：

| 配置项 | 用途 | 默认值 |
|---|---|---|
| `pi.enable` | 是否执行真实远程端到端验收（`tests/integration/remote/workflow`） | `false` |
| `pi.model` | 真实调用使用的模型 | `group/free` |
| `pi.provider` | 模型 provider 名称，留空时按 `pi.model` 原样使用 | 空 |

本机不执行 Pi：Agent 调用全部在容器内完成，因此测试不检查本机是否安装 `pi`，也不再读取本机
Pi 可执行文件路径。容器内 Pi 是否可用由 `tests/integration/remote/agent/` 在容器内执行
`pi --version` 与 `pi --list-models` 确认；容器内 Pi Runner 的单元测试位于 `tests/unit/agent/`，
使用容器执行替身，始终执行；统一 Runner 的生命周期用例位于 `tests/unit/app/`，验证「启动失败
不残留 running」「停止服务取消在途调用并收敛」等约束，同样不依赖容器内 Pi。

默认模型 `group/free` 是特意选定的、不会意外消耗付费额度的标识。它挂在哪个 provider 下由 `pi.provider`
指定：填写后调用标识组装为 `provider/model`（例如 `magpie/group/free`），留空时按 `pi.model` 原样使用。
每次真实调用都显式传入模型，不会回退到 Pi CLI 配置中的默认模型。

### 真实 SSH

真实 SSH 测试的全部配置来自 `tests/config/ssh.yml`（示例文件为 `example.ssh.yml`）：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `ssh.enable` | 是否执行需要真实远程机器的测试 | 是，默认 `false` |
| `ssh.host` | SSH 服务器地址 | 是 |
| `ssh.user` | 登录用户名 | 是 |
| `ssh.port` | SSH 端口 | 否，默认 `22` |
| `ssh.private_key_path` | 私钥路径，本版本只支持私钥认证 | 是（连接类用例） |
| `ssh.known_hosts` | 只读 known_hosts 文件列表 | 否，留空时复用本机 `~/.ssh/known_hosts` 与 `known_hosts2` |

示例：

```bash
cp tests/config/example.ssh.yml tests/config/ssh.yml   # 填写内容并把 ssh.enable 设为 true
go test -count=1 -v ./tests/integration/ssh/...
```

`ssh.enable` 未设为 `true` 时输出 `WARN [SKIP]`；已启用但必需目标缺失时说明缺少哪一项；
目标配置完整但连接、认证或主机密钥校验失败时必须让测试失败。真实 SSH 测试始终执行主机
密钥校验，不会自动接受未知主机密钥。远程文件操作只发生在随机生成的远程临时目录内。

known_hosts 来源由 `tests/internal/testkit` 的 `KnownHostsSources` 统一解析：`ssh.known_hosts`
显式配置时优先，文件不可读时直接让测试失败；未配置时复用本机用户 OpenSSH 的 known_hosts，
与产品 `internal/app.SSHService` 的默认行为一致。解析结果会先快照复制进当前用例工作空间，
校验只读快照，因此不会读取或修改用户真实配置。配置中的取值支持 `${NAME}` 占位符，
需要引用环境变量时写在 yml 里。

产品侧 known_hosts 全程只读（不创建、不追加、不加锁、不写临时文件），测试也遵守同一边界：
需要信任某主机时直接构造 known_hosts 文件，而不是调用任何写入接口。

每个真实 SSH 用例都会在日志中输出目标与 known_hosts 来源，便于区分"配置写错"与
"连接层缺陷"。主机密钥未知时，失败信息会列出实际读取的 known_hosts 路径与可用配置项，
并提示用 OpenSSH 把已核验的密钥追加进 known_hosts。

### 真实远程 Docker

真实远程 Docker 测试同样由配置文件驱动：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `docker.remote_enabled` | 显式启用真实远程 Docker 测试 | 是，默认 `false` |
| `ssh.enable` 及 `ssh.*` | SSH 目标，见「真实 SSH」 | 是 |

示例：

```bash
# 先在 tests/config/docker.yml 把 docker.remote_enabled 设为 true，
# 并在 tests/config/ssh.yml 把 ssh.enable 设为 true 且填好目标
go test -count=1 -v ./tests/integration/remote/docker/...
```

`docker.remote_enabled` 未启用时输出 `WARN [SKIP]`，普通测试不会修改真实远程 Docker；
该开关已启用但 `ssh.enable` 未开启或 SSH 目标缺失时说明缺少的配置项并跳过；目标完整但
认证失败、Docker 不可用或断言不成立时必须让测试失败。

## 编写测试的约定

- 使用 `testkit.New(t)` 创建工作空间，用 `testkit.Dir(t)` 取代 `t.TempDir()`。
- 不要用 `t.Setenv` 修改全局环境来切换数据目录；通过结构体、构造参数或子进程环境
  传入路径。
- 每个用例的数据库、Session、输出文件路径必须来自自己的工作空间。
- 需要真实外部依赖时，由测试自身检查依赖并在缺失时输出警告后跳过。
- 成功判定必须基于具体业务结果（退出码、数据库记录、文件内容、状态字段），不能只
  依赖日志文本。
- Docker 相关测试必须在 `tests/config/docker.yml` 中显式启用 `docker.remote_enabled`
  才允许修改真实远程 Docker；本机测试一律使用执行替身或路径映射，不依赖本机是否安装
  Docker。
- 测试结束不得删除测试工作空间与产物；真实远程测试的远程测试目录同样保留（只回收测试
  容器，不删除共享镜像、缓存卷与非测试容器）。
- 共用同一台真实设备的测试包必须保持包内串行（不使用 `t.Parallel()`），并在包注释写明原因：
  这些用例共用远程 Docker 与环境镜像标签，并行构建会把同一标签改指到不同镜像，使先完成用例记录的
  镜像 ID 失效。无共享状态的普通测试仍需支持并发。

## 远程执行测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/source/` | 仓库地址校验、目标目录冲突、Git 缺失分类、参数转义、基础分支识别优先级、任务分支初始化、取消与超时、结果不确定分类 | 无（使用容器执行替身） |
| `tests/unit/agent/` | 容器内 Pi 参数构造、调用标识唯一性、标准输入、退出状态分类、Session 缺失/残缺、导出失败保留 JSONL、容器所有权核验 | 无（使用容器执行替身） |
| `tests/integration/local/source/` | 真实 Git 克隆、基础分支识别、`agent/<task-id>` 分支、远程任务分支检出、目标目录不被删除、并发目录隔离 | 本机 `git` |
| `tests/integration/local/session/` | 容器内 → 宿主机 → 本机三段式传输、字节校验、传输中断保留部分文件、导出失败保留 JSONL、并发 Session 隔离、容器删除后本机 Session 可读 | 无（路径映射到测试工作空间） |
| `tests/integration/remote/agent/` | 真实远程容器内 Git Clone、容器内 Pi 调用、独立 Session、本机持久化、容器删除后 Session 仍可读 | tests/config/ssh.yml 指定的 SSH 目标、真实 Docker 与容器内真实 Pi CLI |

本机 Git 缺失时，`tests/integration/local/source/` 的用例输出 `WARN [SKIP]` 并跳过，
其余用例仍会执行。

## 代码保存测试分层

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/domain/` | 代码保存记录的终态与"必须转人工核验"语义 | 无（纯函数） |
| `tests/unit/store/` | `code_saves` 写入与更新、状态字段约束（成功必须带 SHA）、未请求合并不能记录合并结果、按任务查询、并发写入独立、恢复字段读写（基线/落盘状态/恢复结论）、历史记录（新列为 NULL）可读且不被误判、未知恢复状态原样读回、**核验结论条件写入（读取后结论被改写返回 `ErrConflict` 且不覆盖）、核验只写 `recovery_status` 不覆盖提交/推送状态、记录不存在返回 `ErrNotFound`** | 无 |
| `tests/integration/local/codesaver/` | 真实 Git 提交、任务分支真实 Push 到 bare 仓库、默认不合并、显式合并后基础分支包含任务提交、推送失败不误报成功、合并冲突保留现场、无变更不制造空提交、并发仓库隔离、提交成功但记录写入失败后可核验、远程不一致与不可达时的核验结论 | 本机 `git` |
| `tests/integration/remote/codesaver/` | 真实远程容器内提交、推送与合并，远程任务分支与基础分支 commit 核验；**真实副作用结果不明（推送成功但记录写回失败）时需求删除失败关闭，且不回滚远端 Git、不清理容器** | tests/config/ssh.yml 指定的 SSH 目标、真实 Docker 与容器内 Git |

本机 Git 缺失时，`tests/integration/local/codesaver/` 的用例输出 `WARN [SKIP]` 并跳过，
`tests/unit/domain/` 与 `tests/unit/store/` 的用例仍然执行。

### 代码保存测试的配置

远程代码保存测试使用 `tests/config/docker.yml` 与 `tests/config/ssh.yml`：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `docker.remote_enabled` | 显式启用真实远程容器测试 | 是 |
| `docker.git_image` | 远程代码保存测试使用的容器镜像（需包含 `git`） | 是 |
| `ssh.enable` 及 `ssh.*` | SSH 目标，见「真实 SSH」 | 是 |

```bash
# 本机 Git 集成测试（不依赖公网）
go test -count=1 -v ./tests/integration/local/codesaver/...

# 真实远程容器代码保存测试（先在 docker.yml 与 ssh.yml 中启用并填写配置）
go test -count=1 -v ./tests/integration/remote/codesaver/...
```

`docker.remote_enabled` 未启用时输出 `WARN [SKIP]`；已启用但 `ssh.enable` 未开启、SSH 目标
或 `docker.git_image` 缺失时说明缺少的配置项并跳过；目标完整但连接、认证、主机密钥校验
失败或断言不成立时必须让测试失败。

远程代码保存测试只在本次测试创建并已核验所有权的容器内操作，只使用带随机后缀的容器
名称与容器内临时目录，不执行全局 prune，也不在用户的真实主分支上执行合并。

### 远程 Agent 测试镜像

`tests/integration/remote/agent/Dockerfile` 定义测试用任务环境镜像，基础镜像与 Pi CLI
版本都固定到具体版本。构建方式（在仓库根目录执行，可在远程机器上执行）：

```bash
docker build -t agentflow-test-agent:1.0.0 -f tests/integration/remote/agent/Dockerfile .
```

### 远程 Agent 测试的配置

远程 Agent 测试同样使用 `tests/config/docker.yml` 与 `tests/config/ssh.yml`：

| 配置项 | 用途 | 必填 |
|---|---|---|
| `docker.remote_enabled` | 显式启用远程 Docker 测试 | 是 |
| `docker.agent_image` | 测试使用的镜像引用 | 否，默认 `agentflow-test-agent:1.0.0` |
| `ssh.enable` 及 `ssh.*` | SSH 目标，见「真实 SSH」 | 是 |
| `docker.pi.*` | 可选的容器内 Pi 模型接入覆盖 | 否 |

真实模型标识来自 `tests/config/pi.yml` 的 `pi.model`，默认 `group/free`。模型标识按 provider 与
模型两列组装，`--model` 使用 `provider/model` 形式：模型挂在不含模型前缀的 provider 下时（例如
provider 为 `magpie`、模型标识为 `group/free`），必须同时填写 `pi.provider`，调用标识才会组装成
`magpie/group/free`；`pi.provider` 留空时按 `pi.model` 原样使用。容器内 Pi 的模型来自 `docker.yml`
的 `pi.*`（provider 固定），不受 `pi.provider` 影响。`pi.enable` 只控制真实远程端到端验收
（`tests/integration/remote/workflow`）是否执行。

```bash
# 先在 docker.yml 与 ssh.yml 中启用，并准备好测试镜像
go test -count=1 -v ./tests/integration/remote/agent/...
```

容器内 Pi CLI 是否可用由测试在容器内实际执行 `pi --version` 确认，不依据本机是否安装 Pi。
远程测试只删除本次测试创建并已核验所有权的容器，不执行全局 prune。

### 真实规划运行时验收（`tests/integration/remote/planning/`）

该目录是 M2 规划运行时的真实环境验收入口：真实文件型 SQLite、真实 SSH、真实远程 Docker
Engine、真实只读规划容器、真实固定 commit 快照卷与真实容器内 Pi CLI，全部通过生产服务装配
（`app.ContainerService` / `app.PlanningRuntimeService` / `app.PlanningAgentService` /
`app.PlanningSessionCoordinator` / 当前 `storage.Store` / `app.RoleService`），不存在第二套
容器管理、Pi Runner 或 Session 管理实现。

| 用例 | 覆盖内容 |
|---|---|
| `TestRemotePlanningReadOnlyEnvironment` | 真实 `docker inspect` 的 `ReadonlyRootfs` / 只读代码卷挂载 / tmpfs / 无 bind、无 Socket / 非特权；角色工具策略只有只读工具；真实文件工具与 Shell 写入被文件系统拒绝且快照 commit 与工作区不变；`Release` 真实删除容器与卷、保留消息、重复释放幂等 |
| `TestRemotePlanningMultiRoundSessions` | 两个规划角色的真实 Pi 调用、角色会话标识与容器内 / 本机路径隔离、同一角色多轮上下文连续、归档随轮次增长、取消的调用不覆盖最后一份完整归档 |
| `TestRemotePlanningContainerRestartRecovery` | 生产 `StopContainer` 停止容器并核验 `exited`；关闭服务与 SQLite 后用同一数据库文件与 Session 根目录重建；`Prepare` 复用启动同一容器并恢复历史 Session |
| `TestRemotePlanningSSHInterruptionTermination` | 在真实 Pi 执行期间关闭该次调用使用的真实 SSH 连接；独立控制连接核验容器退出与进程消失；本地记录 `stopped`、绑定不变；中断后可复用同一容器继续上下文 |
| `TestRemotePlanningPausedState` | 真实 `docker pause` 后的 `Status` / `Running` / `Paused`；启动、执行、规划调用与 `Prepare` 复用全部拒绝且不改写状态；观测结论为 `indeterminate`；本地不写 `stopped` |
| `TestRemotePlanningRestartingObservation` | 用一次性容器尝试观察真实 `restarting`；`dead` / `removing` 未制造时如实报告未观测，不编造状态 |
| `TestRemotePlanningPreconditions` | 记录 Docker CLI / Engine / API 版本、测试镜像、容器内 Pi 与 Git 版本、模型标识与测试仓库分支 |

测试镜像 `tests/integration/remote/planning/Dockerfile`（`agentflow-test-planning:1.0.0`）
在通用测试镜像之上只设置 `PI_CODING_AGENT_DIR=/tmp/pi-agent`：规划容器 rootfs 只读，唯一
可写目录是 `/tmp`（tmpfs），因此 Pi 的配置目录必须落在其中。镜像内不含任何凭据，模型接入
配置由测试在运行期写入该目录，容器删除后随之消失。

```bash
# 需要 docker.remote_enabled、ssh.enable 与完整的 docker.pi 配置
go test -count=1 -v ./tests/integration/remote/planning/...
```

已知的真实环境行为：`docker cp` 只解析容器 rootfs，看不到 tmpfs 上的路径，因此只读规划
容器的 Session 提取与恢复由生产代码退回 `docker exec` 加宿主机重定向；本目录的用例覆盖
该路径。测试包结束时按真实 `docker ps -a` 与命名卷核验零残留，只清理本次测试创建并核验过
所有权的容器与卷。

## 需求聚合删除测试分层

需求与它的全部任务在同一事务内软删除，任一任务不满足条件就整体拒绝；删除不回滚 Git、不清理容器、
不删除执行历史。覆盖分成四层：

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/domain/` | 需求与任务删除的状态白名单、未知状态失败关闭、删除事件类型 | 无（纯函数） |
| `tests/unit/store/` | 条件软删除（影响行数为 0 时拒绝迟到写入）、事务回滚不留半成品、并发删除只有一个成功、执行历史保留、未知状态原样读回 | 无 |
| `tests/unit/app/` | 事务外取权 + 事务内复读核验；活动执行记录、未收敛步骤、结果不明的代码保存副作用一律拒绝；代码保存各形态的删除矩阵（见下文）；任务取消并发、多任务取权中途失败、事务中途取消、服务停止释放执行权 | 无（真实 SQLite + 真实协调器） |
| `tests/unit/httpapi/` | 409 语义、真实删除结果返回、持久化与事件写入失败的错误映射 | 无 |
| `tests/integration/local/scheduler/` | 已删除需求的任务不被调度领取、在途分派期间删除失败关闭、删除与调度扫描并发后删除状态一致且不残留执行权 | 无（真实 SQLite + 真实调度器，启动端口用受控替身） |
| `tests/integration/remote/codesaver/` | 真实副作用结果不明时删除失败关闭，且远端 Git 不回滚、容器不被清理 | 真实 SSH 与远程 Docker |

代码保存形态的删除矩阵（`tests/unit/app/requirement_delete_code_save_test.go`）：从未开始、提交与推送已完成、
推送仍 `pending` 未核验、已核验但无法判定（`unverifiable`）、记录写入未确认（`persist_status=unconfirmed`）、
推送 `unknown`、推送 `conflict`、合并待确认——全部拒绝删除；只有核验结论为"确认未发生"
（`confirmed_not_applied`）时才允许删除。被拒绝时需求与任务都不得写入 `deleted_at`，代码保存记录也不得被改写。

"结果不明时失败关闭"由应用层用例与真实环境用例共同守住：把结果不明当作"没有副作用"的改动会让两层同时失败。

## 开发—验证闭环测试分层

单步骤"开发 → 验证"闭环的测试集中在两个目录，全部使用受控替身，不依赖真实 Pi、SSH、
Docker 或模型：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/domain/` | 收口决策（`DecideStepCloseout`）、验证状态推导（`DeriveStepVerificationState`）、失败反馈构建与脱敏裁剪、代码保存恢复判定（`DecideCodeSaveRecovery`，纯函数） |
| `tests/unit/app/` | 闭环协调器：开发成功后启动验证、开发失败/取消不启动验证、必需检查失败不得完成、基础设施异常不得完成、验证配置无效拒绝、容器不可用不回退本机、失败反馈持久化与重新执行引用、并发请求拒绝、开发记录写入失败不验证、服务停止取消在途闭环 |
| `tests/unit/httpapi/` | 闭环接口：200/400/404/409/500 映射、`verified` 与 `validation_ran` 不互相替代、验证历史查询 |
| `tests/unit/store/` | `validation_feedbacks` 的追加、只追加不覆盖、按步骤取最近一次、不存在语义 |

闭环用例的关键约束：

- 用例的容器替身同时实现 `StepExecutionContainerPort` 与 `ValidationContainerPort`，因此可以
  断言"开发与验证看到的是同一个容器与同一份工作区"。
- 开发阶段结论由替身返回，验证阶段结论由替身返回的检查退出码与启动状态决定；不通过 Mock
  伪造"验证通过"。
- 验证失败反馈用例会检查输出摘要已脱敏（例如 `password=` 的内容被隐藏）且长度受限。
- 服务停止用例会确认在途闭环收敛，且停止后不再接受新请求。

## 任务级工作区初始化测试分层

统一启动中的「初始化工作区」阶段（`app.TaskWorkspaceService`）与尝试级的 `code init` 是两个
层次，测试按此分开：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/domain/` | 记录状态机（`uncertain` 非终态、终态不可重开）、`DecideTaskWorkspaceNext`（无记录/成功/跳过/失败/超时/不确定/占用）、`DecideTaskWorkspaceRecovery` |
| `tests/unit/app/` | 协调器：成功写入完整记录、同容器重复初始化记录 `skipped` 且不重新 Clone、换容器后重新初始化、`uncertain` 阻止重放、容器未运行/detached/已删除/缺失的前置条件错误、失败阶段与错误类别落库、超时与取消映射、事件写入、中断收敛不改写终态 |
| `tests/unit/store/` | `task_workspace_initializations` 的写入、按任务/容器查询、同任务同容器只允许一条未结束记录（部分唯一索引）、中断记录查询、未知枚举值原样读回、成功必须带 commit、记录不存在语义 |
| `tests/unit/httpapi/` | 启动接口的工作区阶段与错误映射：`uncertain` → 409、前置条件缺失 → 412、未落库 → `execution_not_persisted` |
| `tests/integration/local/workflow/` | 真实 SQLite 上的阶段顺序、结果不确定时停在 workspace 阶段、重启后记录仍有效、中断记录收敛为 `uncertain` 且不重放 |
| `tests/integration/local/fetcher/` | 真实 Git：任务级初始化复用同一套 `source.Fetcher`（`AttemptID` 为空、任务分支正确、工作区落在容器内目录）、重复初始化不覆盖已有改动、目标目录非空时失败且不删除内容 |

工作区用例的关键约束：

- 任务级初始化不携带 `attempt_id`：用例显式断言传给 `FetchSourceWith` 的 `AttemptID` 为空，
  防止后续实现为了复用尝试级代码而伪造归属。
- 集成用例的容器替身同时是代码获取端口，内部直接调用 `source.NewGitFetcher`，因此断言的是
  "走的是同一条实现"，而不是"另写了一套 Git 命令"。
- 重启通过"关闭连接 → 同一路径重新打开"模拟，只断言数据库里的持久化事实。
- 收敛用例的容器替身在被调用时直接失败，用来证明恢复路径不访问容器、不重放代码获取。

## 启动恢复测试分层

启动恢复（`app.TaskWorkflowService.Start`）的测试分两层，纯函数与链路分开覆盖：

| 目录 | 覆盖内容 |
|---|---|
| `tests/unit/domain/` | 收敛决策纯函数：`DecidePlanGenerationRecovery`、`DecideCancellationRecovery`（含"未结束执行优先于取消请求"的规则顺序） |
| `tests/unit/app/` | 计划生成的失败与恢复路径：非法输出、非零退出、超时、用户取消、服务停止中断、计划事务回滚、并发占用、`planning` 残留收敛 |
| `tests/integration/local/workflow/` | 真实 SQLite 上的重启一致性：关闭连接后用同一路径重新打开，构造新的编排器实例并 `Start`，随后只查库断言 |

集成用例的约束：

- 使用真实数据库文件与真实 `app.TaskWorkflowService`，只有 Agent 执行、容器核验与单步骤闭环
  是替身；恢复链路、事务边界、状态机与写入全部走产品代码。
- 重启通过"关闭连接 → 同一路径重新打开"模拟，不使用同一个 `*sqlite.DB` 句柄冒充重启。
- 断言对象是数据库事实：任务状态与 `failure_summary`、步骤集合、`workflow_runs`、`sessions`、
  `events`，不以方法返回值代替持久化断言。
- 替身记录调用次数，用于断言"恢复不重放 Planner、不执行步骤"。
- 恢复写入失败用 `failingStore`（包装真实存储、只让指定方法返回错误）注入，验证启动失败且
  现场保持原状。
- 并发恢复用例用同一启动信号同时放行两个实例，断言持久化事实（最终状态唯一、恢复事件不重复），
  不断言启动返回值——恢复是幂等收敛，竞争结果由数据库状态前置条件决定。

```bash
go test -count=1 -v ./tests/integration/local/workflow/
go test -race -count=1 ./tests/integration/local/workflow/
```

## 数据库约束测试

数据库结构遵循两条长期规则：**枚举字段不受数据库级封闭取值限制**，**业务 Schema 不声明外键**。
这两条规则当前只在存储层有守点：

| 目录 | 内容 | 外部依赖 |
|---|---|---|
| `tests/unit/store/` | 仓储读写、未知枚举值原样保留、逻辑删除、唯一约束与非枚举 `CHECK` 仍生效、条件更新拒绝迟到结果、并发写入与事务原子性 | 无 |

```bash
go test -count=1 -v ./tests/unit/store/
go test -race -count=1 ./tests/unit/store/ ./tests/integration/local/scheduler/
```

`tests/integration/local/database/`（迁移后全量 Schema 审计、旧库升级与数据保留、自增主键续号、
迁移失败不留半成品、并发打开只迁移一次）已在测试精简中删除。Schema 审计与旧库不兼容行为因此**目前
没有自动化守点**：迁移脚本中的表结构或约束不会被测试发现，只能靠脚本自身、`make vet` 与人工核对。
Schema 由 `internal/storage/sqlite/migrations/` 下的版本化脚本维护，Goose 在打开数据库时执行待执行的迁移。

### 查看测试结果

每个测试入口输出 `TEST_ARTIFACT_DIR=<绝对路径>`，数据库、WAL/SHM 文件与测试产物都保留在该目录
内。测试结束只关闭连接，不删除任何产物，便于直接打开 SQLite 文件核对结果。同一目录下的
`test.log` 记录本次运行的逐用例结论与日志（见「运行日志」），复核时不需要重跑测试。
