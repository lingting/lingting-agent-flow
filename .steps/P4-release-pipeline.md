# P4 工程质量：正式发布与构建制品流水线

本轮任务是在已完成的 CI 自动化测试门禁（[`P4-ci-gate.md`](./P4-ci-gate.md)）之上，建立正式
版本发布构建流程：统一版本元数据、版本标签触发的发布工作流、跨平台制品、SHA-256 校验文件与
版本构建信息，并对制品本身做打包验收。

本轮重点是把"能编译"变成"能交付"：区分**交叉编译成功**与**该平台已完成运行验收**，
不接受用其中一个冒充另一个。

- 基线与执行原则：以当前本地 checkout 为最新代码基线，不执行 `git fetch` 与远端 SHA 比较。
- 本轮未创建 GitHub Release、未推送任何镜像、未部署任何环境、未写入任何仓库 Secret。
- `P4-M5` 继续保持 `pending`，本轮没有产生性能优化依据。

## 1. 开工前的现状盘点

| 机制 | 开工前状态 | 代码证据 |
| --- | --- | --- |
| 版本注入 | `-ldflags -X main.version/commit/buildDate`，默认 `dev`/`none`/`unknown` | `Makefile`、`cmd/agentflow/main.go` |
| 版本查询 | `agentflow version` 与 `--version` 已存在，输出三行 | `internal/cli/version.go`、`internal/app/app.go` |
| Go 版本与平台 | 仅 HTTP `/api/v1/system/info` 通过 `runtime.Version()` 与 `GOOS/GOARCH` 提供 | `internal/app/health.go` |
| 前端顺序 | `make release` 先 `web-build`、再 `web-verify`、最后 `go build` | `Makefile` |
| 前端完整性 | 在两条 Makefile 配方里各写一份 shell 检查（`test -f` + `ls`） | `Makefile` 的 `web-verify` |
| 单文件启动 | 已有 E2E：构建前端、编译、复制到独立目录后启动 | `tests/e2e/packaging/packaging_test.go` |
| 跨平台制品 | **缺失**：没有任何打包、命名规范、校验文件或清单 | —— |
| 发布工作流 | **缺失**：`.github/workflows/` 只有 `ci.yml` | —— |

结论：前端先构建后嵌入的顺序、缺失前端产物即失败、单文件可独立目录启动这三件事**已经实现**，
不重复造轮子；缺的是版本元数据统一、制品打包与校验、发布工作流与制品级验收。

## 2. 统一版本元数据

新增 `internal/release` 包承载全部发布规则（纯计算与纯文件读写，不执行 git/go/docker）：

| 字段 | 来源 | 约束 |
| --- | --- | --- |
| `version` | `-version` 参数或版本标签 | `dev` 或 `vMAJOR.MINOR.PATCH`，否则构建失败 |
| `commit` | 参数缺省时读取 `git rev-parse HEAD` | 必须是完整 Git SHA；短 SHA、`none`、`unknown` 一律拒绝 |
| `build_date` | 参数缺省时取当前 UTC 时间 | 必须是 RFC 3339 |
| `go_version` | 参数缺省时取 `runtime.Version()` | 必须形如 `go1.x.y` |
| `os` / `arch` | 目标平台 | 必须在 `SupportedPlatforms` 内 |

关键取舍：

- **`goVersion` 不通过 `-ldflags` 注入**。CLI 与 HTTP 都读 `runtime.Version()`，只有一个来源，
  不可能出现"程序显示的 Go 版本"与"清单记录的 Go 版本"不一致。
- **不接受 CI 运行编号**。`ValidateVersion` 只认 `dev` 与 `vMAJOR.MINOR.PATCH`；用运行编号
  或分支名冒充版本会让制品无法与源码对应。
- **缺省值全部来自真实来源，取不到就直接失败**。提交号读不到 git 时报错并提示可用 `-commit`
  显式指定，不会回落到 `none` 之类的占位值。

`cmd/agentflow/main.go` 的构建变量默认值保持 `dev`/`none`/`unknown`：本地 `go build` 不注入时
显示 `dev`，这是唯一的"非正式版本"取值，不会伪装成正式版本号。

CLI 与 HTTP 两个入口的字段口径一致：`version` 输出补充了 `Go:` 与 `平台:` 两行，
`/api/v1/system/info` 的 `go_version` 与 `platform` 保持不变（已有接口契约不变）。

## 3. 发布矩阵与已核实依据

`internal/release.SupportedPlatforms` 是发布矩阵的唯一来源，只包含五个平台：
`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`。

核实依据（不是"Go 能交叉编译"）：

- 持久化用 `modernc.org/sqlite`（纯 Go），不链接系统 SQLite 库，无 `cgo` 调用；
- 代码中没有 `//go:build` 平台分文件；
- 进程信号（`SIGTERM`）、终端交互、路径与权限语义只用标准库与 `x/term` 的跨平台实现；
- 前端产物通过 `//go:embed` 嵌入，与目标平台无关；
- 构建时显式 `CGO_ENABLED=0`，不依赖目标机器预装的 C 库；
- 五个目标都已用本仓库工具链实际产出可执行文件。

未列入矩阵的平台（Linux 386、Windows arm64、FreeBSD）在构建时被直接拒绝：`-platforms` 传入
非矩阵平台会报错，`BuildDistribution` 的 `resolvePlatforms` 同样拒绝。

## 4. 发布入口与制品

发布入口只有两个，且共用同一份实现：

```bash
make release-dist VERSION=v1.2.3     # 本地
.github/workflows/release.yml        # 标签推送 / 手动触发
```

两者都调用 `internal/release.BuildDistribution`：前端产物校验 → 逐平台 `go build` →
写 `version.json` → 打包 → 写 `release-manifest.json` 与 `SHA256SUMS` → 回读自检。

制品命名与内容：

```text
agentflow_<version>_linux_amd64.tar.gz
agentflow_<version>_linux_arm64.tar.gz
agentflow_<version>_darwin_amd64.tar.gz
agentflow_<version>_darwin_arm64.tar.gz
agentflow_<version>_windows_amd64.zip
SHA256SUMS
release-manifest.json
```

每个压缩包是扁平布局，含可执行文件（0755）、`QUICKSTART.md`、`THIRD-PARTY-NOTICES.md` 与
`version.json`。`SHA256SUMS` 按文件名排序，同一版本重新生成得到完全相同的文件内容。

自检覆盖四件事，任何一件不成立都让构建失败：

1. 清单能重新解析并通过 `Manifest.Validate`（命名、格式、可执行文件名、摘要、大小、平台唯一）；
2. `SHA256SUMS` 与清单逐行一致；
3. 回读每个制品的 SHA-256 与大小；
4. 回读每个压缩包的布局与预期文件清单完全一致。

## 5. 发布工作流

`.github/workflows/release.yml` 的结构与边界：

| Job | 职责 | 关键约束 |
| --- | --- | --- |
| `resolve` | 解析版本号与提交号 | 标签必须与版本号完全一致；手动输入的版本与标签冲突时失败；格式与 `ValidateVersion` 同规则 |
| `build` | 安装冻结依赖 → `make release-dist` → `sha256sum -c` → 上传制品 | `if-no-files-found: error`；上传失败即 Job 失败 |
| `smoke` | 五个平台的原生 Runner 上执行 `TestReleaseArchiveSmoke` | `fail-fast: false`，一个平台失败不掩盖其他平台 |
| `release-summary` | 按结构化结果汇总 | 只依据 `needs.*.result`，不对日志做字符串搜索 |

触发条件只有 `push` 版本标签（`v*.*.*`）与 `workflow_dispatch`。权限固定 `contents: read`，
不使用任何 Secret，不创建 Release、不推送镜像、不部署环境；工作流内不出现 `go build`，
制品只来自 `make release-dist`。

## 6. 测试与验收

新增与扩展的测试入口：

| 入口 | 覆盖 |
| --- | --- |
| `tests/unit/release/` | 版本元数据校验、发布矩阵、命名与压缩格式、`SHA256SUMS` 排序稳定性、清单字段与禁止字段（无路径/令牌）、压缩包布局与权限、前端产物完整性、构建入口拒绝非法输入、构建产物与校验文件一致、工作流触发条件 / 权限 / 阶段顺序 / 冒烟矩阵 / Action 固定 / 表达式上下文 |
| `tests/e2e/packaging/`（`TestReleaseArtifacts`） | 真实执行一次发布构建，逐平台校验压缩包布局、`version.json`、清单、SHA256SUMS，并把当前平台制品解压后执行 `version` |
| `tests/e2e/packaging/`（`TestReleaseArchiveSmoke`） | 校验并启动流水线下载的单个制品：布局、元数据、版本查询、HTTP API、前端静态资源、停止后端口释放 |
| `tests/e2e/cli/` | `version` 输出字段完整性、`dev` 不覆盖注入值、`--version` 与 `version` 输出一致 |

## 7. 实际执行结果

本轮已获测试授权，实际执行与结果如下（对照最终代码）：

### 7.1 Go 全量

`go test -count=1 -p 8 -parallel 8 ./tests/...`

- 单元与集成包全部通过，包含新增的 `tests/unit/release`（20 个用例）与
  `tests/e2e/packaging`（含新增的发布制品用例）。
- 失败包（在基线上同样失败，与本轮改动无关，已用 `git stash` 前后对照确认）：
  - `tests/integration/remote/codesaver`：`TestSaveCodeInsideRemoteContainer` 本体 PASS，
    但包内 `docker ps -a` 零残留核验把**并行运行的同级包**在途容器判为残留。
  - `tests/integration/remote/agent`：`TestRemoteSourceFetch` 因容器内 `git clone` 出网被
    重置失败（`Connection reset by 172.20.0.20 port 443`）。
  - `tests/integration/remote/workflow`：`TestRemoteE2EFullChain` 同一出网原因，停在
    `workspace` 阶段的 clone。
- 结论：三个失败均为远程环境/并行核验问题，与发布流水线无关。
- 最终代码复跑（`-p 8 -parallel 8`）：42 个包 ok，2 个远程包失败
  （`integration/remote/agent` 出网被重置、`integration/remote/codesaver` 并行零残留误判），
  失败集合与基线一致。

### 7.2 Go 竞态

- `go test -race -count=1 ./tests/unit/...`：全部包 ok，包含 `tests/unit/release`。
- `go test -race -count=1 ./tests/integration/local/ssh/...`：ok。
- `go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/`：
  699 个用例通过。
- `go test -race -count=1 ./tests/integration/local/database/`：第 1 次 ok，第 2 次
  `TestLegacyDatabaseConcurrentOpenMigratesOnce` 失败（`database is locked (5)`）。该用例
  单独运行时连续三次 ok，基线复跑同样出现一次失败，属于已知的并发迁移忙等待窗口，不是本轮
  引入的问题；本轮未做范围外重构。

### 7.3 前端

`pnpm test:all:safe`（`--maxWorkers 4` / `--workers=4` 保持不变）：退出码 0。

- Vitest：28 个文件、635 个用例全部通过。
- Playwright：257 个用例全部通过（首次全量运行有 9 个失败，单独复跑对应文件全部通过，
  属于顺序/环境抖动，非本轮引入）。
- `vp fmt` / `vp lint --fix` / `tsc --noEmit` 与生产构建通过。

### 7.4 发布专项

| 检查 | 结果 |
| --- | --- |
| `go test -count=1 ./tests/unit/release/...` | 20 个用例通过 |
| `go test -count=1 -run TestReleaseArtifacts ./tests/e2e/packaging/` | 通过（真实生成 5 个平台制品） |
| `go test -count=1 -run TestReleaseArchiveSmoke ./tests/e2e/packaging/`（Windows 制品） | 通过 |
| 工作流 YAML 与表达式检查 | 通过（触发条件、权限、阶段顺序、矩阵、Action 固定、表达式上下文） |
| `gofmt -l cmd internal tests` | 无输出 |
| `go vet ./...` / `go build ./...` | 通过 |
| `go test -count=1 ./tests/e2e/packaging/...`（最终代码复跑） | 通过（`ok ... 19.9s`） |

### 7.5 制品实测数据

本轮在本机（Windows 11 amd64）真实生成 `v0.0.0` 制品（提交 `cd983e4c…`，Go `go1.24.5`）：

| 制品 | 大小（字节） | SHA-256 |
| --- | --- | --- |
| `agentflow_v0.0.0_linux_amd64.tar.gz` | 8499565 | `06c84dc4198e258c6fc22c21d38842247922025df68014a99d516d78e688c3d2` |
| `agentflow_v0.0.0_linux_arm64.tar.gz` | 7955052 | `f92c2f9815c7cf50b39e86dd040498673f347466140f5ebd0b1280443e4242fa` |
| `agentflow_v0.0.0_darwin_amd64.tar.gz` | 8666885 | `d5554dd3f2ed10f67af616afc93d5d031247063678d4e1cfd801ec547660803e` |
| `agentflow_v0.0.0_darwin_arm64.tar.gz` | 8277408 | `bf7b45db38894f6f428ec8ca86694dc458e38cedf4834c2ff82f8686bf86499c` |
| `agentflow_v0.0.0_windows_amd64.zip` | 8732339 | `42bb3140b0c01a010437f50b08861c8c44f43b506a73837994cf721415aed349` |

各平台验证状态：

| 平台 | 交叉编译 | 运行验收 | 证据 |
| --- | --- | --- | --- |
| linux/amd64 | 已完成 | **未执行** | 制品已生成并校验 SHA-256；本机无 Linux 环境 |
| linux/arm64 | 已完成 | **未执行** | 同上 |
| darwin/amd64 | 已完成 | **未执行** | 本机无 macOS 环境 |
| darwin/arm64 | 已完成 | **未执行** | 同上 |
| windows/amd64 | 已完成 | 已完成 | 解压制品 → `version` 输出与 `version.json` 一致 → 启动服务 → `/` 返回 200 且含 `id="root"` → `.js`/`.css` 均 200 且内容类型正确 → `/api/v1/health` 返回 200 → `/api/v1/nope` 返回 JSON 404 → 停止后端口释放 |

`release.yml` 的冒烟 Job 已为五个平台配置原生 Runner，但**该工作流尚未在真实运行环境执行过**，
因此 Linux 与 macOS 的"可运行"目前是待验证项，不是已确认事实。

## 8. 未执行与剩余限制

- Linux 与 macOS 四个平台没有任何真实运行证据，只有交叉编译与制品结构校验。
- 未在真实 GitHub Actions 环境执行过 `release.yml`，其静态结构由本地用例校验，
  运行时行为（Runner 标签可用性、下载制品路径、`sha256sum` 可用性）尚未验证。
- 未生成或使用任何签名密钥，制品没有签名，只提供 SHA-256 校验。
- 未创建 GitHub Release、未推送镜像、未部署环境：本轮只上传 Actions 运行制品。
- 远程真实测试（Pi / SSH / Docker）的失败全部来自环境与并行核验，未在本轮修复，
  也未按范围外处理。

## 9. 平台支持边界声明

**已经编译的目标平台**：linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64。

**已经完成运行冒烟的目标平台**：windows/amd64（本机实测）。

**尚未获得真实运行验证的平台**：linux/amd64、linux/arm64、darwin/amd64、darwin/arm64
（等待 `release.yml` 在对应原生 Runner 上执行）。

三者的区分是本轮的核心约束：交叉编译成功不等于该平台可用。发布文档与测试断言都按这一
边界书写，不把未执行的验证写成已完成。

## 10. 文档同步

- `README.md`：新增跨平台发布制品与版本查询两节，平台表区分交叉编译与运行验证。
- `docs/release.md`（新增）：版本命名、构建环境、构建流程、制品布局、校验方法、平台支持
  边界与发布安全边界。
- `tests/README.md`：新增发布制品测试分层与发布阶段/本地入口对照表。
- 本文件：本轮建设结果与实测数据。
- `P4-M5` 保持 `pending`，本轮未采集任何真实使用数据。
