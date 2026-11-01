# 发布说明

本文记录 AgentFlow 正式发布制品的版本约定、构建环境、制品布局、来源证明、发布门禁与
核验方法。它描述**已经实现并能被核对**的事实；未执行的能力在「发布状态」一节单独标注，
不混入已完成项。

## 1. 发布状态

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| 制品构建入口（`make release-dist` / `cmd/agentflow-release`） | 已实现 | `internal/release`、`tests/unit/release`、`tests/e2e/packaging` |
| 制品核验入口（`verify-release` / `verify-assets` / `notes`） | 已实现 | 同上，规则集中在 `internal/release/publish.go` |
| 构建工作流（`.github/workflows/release.yml`） | 已实现 | 可被 `publish.yml` 复用，也可手动预演 |
| 正式发布工作流（`.github/workflows/publish.yml`） | 已实现 | 标签推送 / 手动触发，门禁 + 来源证明 + 上传后核验 |
| 工作流本地静态验证（YAML 解析、触发、权限、依赖、步骤顺序、表达式上下文） | 已通过 | `go test ./tests/unit/release/` 的 `TestPublishWorkflow*`、`TestWorkflow*` |
| GitHub Actions 上的实际运行 | **未执行** | 本轮没有推送版本标签，也没有在远端运行工作流 |
| 公开的 GitHub Release | **未发布** | 本轮不创建、不推送任何正式版本 |

工作流本体只做了本地静态核验与本地等价命令验证（`make release-dist` 真实生成制品、
`verify-release` / `verify-assets` 真实核验同一份目录）。**远端发布步骤从未执行过**，
首次正式发布必须以工作流的真实运行结果为准。

## 2. 版本命名

版本标签使用 `vMAJOR.MINOR.PATCH`，例如 `v1.2.3`。同一字符串同时用于：

- Git 标签名；
- 制品文件名（`agentflow_v1.2.3_linux_amd64.tar.gz`）；
- 程序内 `version` 字段、`version.json`、`release-manifest.json` 与 Release 标题。

校验规则集中在 `internal/release.ValidateVersion`：只接受 `dev` 与 `vMAJOR.MINOR.PATCH`。
不接受前缀、后缀、构建元数据、分支名与 CI 运行编号——它们会让制品无法与源码一一对应。
`publish.yml` 在构建开始之前用同一格式再校验一次标签，`dev` 标签会在第一步失败。

| 场景 | version | commit | buildDate |
| --- | --- | --- | --- |
| `make build` / `make release`（未指定 VERSION） | `dev` | 当前 HEAD 短 SHA | 当前 UTC 时间 |
| `make release-dist`（未指定 VERSION） | `dev` | 当前 HEAD 完整 SHA | 当前 UTC 时间 |
| `make release-dist VERSION=v1.2.3` | `v1.2.3` | 当前 HEAD 完整 SHA | 当前 UTC 时间 |
| 版本标签触发的 `publish.yml` | 与标签完全一致 | 标签指向提交的完整 SHA | 工作流运行时的 UTC 时间 |

本地开发构建显示 `dev`：它是唯一的"非正式版本"取值，不会伪装成正式版本号。缺省值只来自
真实来源，取不到就直接失败。

## 3. 构建环境

| 组件 | 版本要求 | 固定方式 |
| --- | --- | --- |
| Go | `go.mod` 声明的版本（当前 1.24.5） | 工作流 `go-version-file: go.mod` |
| Node.js | `lingting-agent-flow-ui/package.json` 的 `volta.node`（当前 22.22.3） | 工作流 `node-version-file` |
| pnpm | 11.24.0 | 工作流 `pnpm/action-setup` 的 `version` |
| 前端依赖 | 以 `pnpm-lock.yaml` 为准 | `pnpm install --frozen-lockfile` |
| Go 依赖 | 以 `go.sum` 为准 | Go 模块校验和 |

工具链版本不只写在文档里：`go_version`、`node_version`、`pnpm_version` 三个字段会写进制品内的
`version.json` 与 `release-manifest.json`。`node` / `pnpm` 版本由构建时实际执行的
`node --version`、`pnpm --version` 得出，不是人工填写的期望值；正式版本缺少这两个字段时
`cmd/agentflow-release` 直接失败。

构建制品时显式设置 `CGO_ENABLED=0`。项目的全部依赖（`modernc.org/sqlite`、`pkg/sftp`、
`cobra`、`x/crypto`、`x/term`、`yaml.v3`）都是纯 Go 实现，代码中没有 `cgo` 调用，也没有
平台分文件（`//go:build`）。因此制品不依赖目标机器上预装的 C 库、SQLite 或 Node.js。

## 4. 构建流程

发布入口只有两个，二者共用同一份实现（`internal/release.BuildDistribution`）：

```bash
# 本地生成全部平台的发布制品
make release-dist VERSION=v1.2.3

# CI 中由 publish.yml 复用 release.yml 执行同一入口
.github/workflows/publish.yml
```

步骤顺序固定，不可颠倒：

```text
源码 checkout
    ↓
pnpm install --frozen-lockfile
    ↓
前端生产构建（产物写入 internal/webui/dist）
    ↓
校验 index.html、assets/*.js、assets/*.css 均存在且非空
    ↓
逐平台 go build（CGO_ENABLED=0，-ldflags 注入 version/commit/buildDate）
    ↓
写入 version.json
    ↓
打压缩包（tar.gz / zip）
    ↓
写出 release-manifest.json 与 SHA256SUMS
    ↓
回读产物：清单校验 + SHA-256 复核 + 压缩包布局复核
```

前端校验在编译之前：`//go:embed` 会把 `dist/` 原样嵌入，缺少产物时 `go build` 仍然成功，
只会产出页面空白的可执行文件。因此缺少 `index.html`、`.js` 或 `.css` 时构建立即失败。

## 5. 制品布局与命名

```text
agentflow_<version>_linux_amd64.tar.gz
agentflow_<version>_linux_arm64.tar.gz
agentflow_<version>_darwin_amd64.tar.gz
agentflow_<version>_darwin_arm64.tar.gz
agentflow_<version>_windows_amd64.zip
SHA256SUMS
release-manifest.json
```

Linux 与 macOS 使用 `tar.gz`，Windows 使用 `zip`。每个压缩包是**扁平布局**，不含子目录：

| 文件 | 说明 |
| --- | --- |
| `agentflow` / `agentflow.exe` | 单文件程序（权限 0755），内含前端生产产物 |
| `QUICKSTART.md` | 快速启动说明 |
| `THIRD-PARTY-NOTICES.md` | 第三方依赖声明与许可证 |
| `version.json` | 版本、提交、构建时间、Go / Node / pnpm 版本、目标 OS 与架构 |

`release-manifest.json` 汇总本次构建的全部制品与构建来源：

```json
{
  "version": "v1.2.3",
  "commit": "3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a",
  "build_date": "2026-01-02T03:04:05Z",
  "go_version": "go1.24.5",
  "node_version": "22.22.3",
  "pnpm_version": "11.24.0",
  "source_repository": "lingting/lingting-agent-flow",
  "source_workflow": ".github/workflows/publish.yml",
  "source_run_id": "1234567890",
  "attestation_provider": "github-artifact-attestation",
  "artifacts": [
    {
      "file": "agentflow_v1.2.3_linux_amd64.tar.gz",
      "os": "linux",
      "arch": "amd64",
      "format": "tar.gz",
      "binary": "agentflow",
      "sha256": "…",
      "size": 8499565
    }
  ]
}
```

`source_*` 与 `attestation_provider` 在本地构建中为空（没有 CI 运行上下文），正式发布时由
工作流写入；缺少这些字段的清单不能通过发布核验（`-require-provenance`）。

清单与校验文件都不包含仓库绝对路径、环境变量、令牌、私钥路径或测试配置：字段由
`internal/release.Manifest` 固定，`tests/unit/release` 会逐项检查这一点。

## 6. 正式发布工作流

`.github/workflows/publish.yml` 是唯一的正式发布入口，触发方式只有两种：

- 推送 `vMAJOR.MINOR.PATCH` 标签；
- 在某个版本标签上手动运行（`workflow_dispatch`），用于失败重试。

分支上的手动运行会在第一步被拒绝（`github.ref_type != 'tag'`）。版本标签不再直接触发
`release.yml`：标签发布统一走 `publish.yml`，避免同一个标签被构建两次、产出两份不同的字节。

### 6.1 门禁清单

| 阶段 | 内容 | 失败后果 |
| --- | --- | --- |
| `resolve` | 标签格式 `vMAJOR.MINOR.PATCH`、ref 必须是标签、提交必须是完整 SHA | 后续阶段全部不运行 |
| `ci`（复用 `ci.yml`） | Go 格式与静态检查、`make test`、竞态检测、前端单元 + Playwright E2E + 格式 + lint + 类型检查 + 生产构建 | 不创建 Release |
| `build`（复用 `release.yml`） | 五个平台制品、`SHA256SUMS`、`release-manifest.json`、独立 `sha256sum -c`、`verify-release` 核验、目标平台原生 Runner 运行冒烟 | 不创建 Release |
| `publish` | 发布前核验 → 生成来源证明 → 复核来源信息 → 拒绝覆盖 → 创建草稿 → 上传 → 重新下载核验 → 核验来源证明 → 转公开 | 不公开 Release |
| `publish-summary` | 汇总四个阶段的真实结论，任何非 success（含跳过）都让工作流失败 | 工作流失败 |

没有任何一步使用 `continue-on-error`，也不存在"默认成功"的占位值。真实 Pi / SSH / 远程
Docker 用例仍按既有显式配置规则由 `ci.yml` 决定：未提供配置时输出 `WARN [SKIP]` 并跳过，
跳过不计为通过，跳过原因保留在 `test.log` 产物里，`ci-summary` 单独标注。

### 6.2 发布过程

```text
版本标签
   ↓  resolve：标签格式 + ref 类型 + 提交 SHA
   ↓  ci：复用 ci.yml 的全量门禁
   ↓  build：复用 release.yml 生成并核验五个平台制品
   ↓  publish：发布前完整性核验（sha256sum -c + verify-release）
   ↓  生成 GitHub OIDC 来源证明（attest-build-provenance）
   ↓  verify-release -require-provenance：清单必须声明来源信息
   ↓  拒绝覆盖已有 Release（含草稿）
   ↓  创建草稿 Release（可见性隔离：外部看不到未完成的发布）
   ↓  上传 7 个资产
   ↓  上传后核验：远端资产列表 + 重新下载 + sha256sum -c + verify-release
   ↓  gh attestation verify：逐个资产核验来源证明
   ↓  草稿转公开（gh release edit --draft=false --latest）
   ↓  复核 Release 状态确实已公开
```

### 6.3 Release 资产

正式 Release 恰好发布 7 个资产，集合由清单派生（`Manifest.ReleaseAssetNames`）：

- 五个平台压缩包；
- `SHA256SUMS`；
- `release-manifest.json`。

版本变更说明写在 Release 正文中（`cmd/agentflow-release notes` 由清单生成），不上传为
独立资产，因此正文引用的资产名与上传的资产集合完全一致。发布目录中不允许出现其它文件：
测试报告、日志、配置或临时产物会让发布前核验失败。

### 6.4 重复发布与失败处理

- 同一个标签**不允许静默覆盖**：`gh release view` 发现已存在（含上次失败留下的草稿）时
  工作流直接失败，并提示人工处置。
- 人工处置方式：在 GitHub 上确认该 Release 的内容确实是失败产物后删除它（含草稿），再从
  标签重新运行 `publish.yml`。已公开的 Release 不会被工作流删除或改写。
- 上传或核验失败时，Release 停留在**草稿**状态：外部看不到，工作流以非零结果结束。
- 发布成功与否只由结构化结论决定（`needs.*.result`），不对日志做字符串搜索。

## 7. 来源证明

### 7.1 机制

正式发布的制品通过 GitHub Actions OIDC 生成**无密钥**来源证明
（GitHub Artifact Attestation，`attestations: write` + `id-token: write`）：

- 证明由 GitHub 依据本次工作流运行签发，绑定仓库、工作流、提交与制品摘要；
- 不需要私钥、不需要仓库 Secret、不需要维护签名基础设施；
- 证明存放在 GitHub 的证明仓库中，用户用官方 CLI 核验，不需要下载额外材料；
- 证明对象是 7 个资产的全部内容摘要（`subject-path` 覆盖压缩包、`SHA256SUMS` 与清单）。

### 7.2 用户核验

```bash
# 核验单个下载到的制品（需要 GitHub CLI 2.49 及以上）
gh attestation verify agentflow_v1.2.3_linux_amd64.tar.gz \
  --repo lingting/lingting-agent-flow
```

核验通过说明"该文件由这个仓库的构建工作流在对应提交上产出"。它**不**说明代码经过安全
审计或功能验收，也不替代 SHA-256 校验（见 §8.1）。

### 7.3 强制策略

- 证明生成失败（权限缺失、OIDC 不可用等）会让 `publish` Job 失败，**不会**降级为"没有
  证明的发布"。
- 证明生成之后，`verify-release -require-provenance` 校验清单确实声明了仓库、工作流、
  运行标识与证明提供方。
- 上传后必须逐个资产跑通 `gh attestation verify` 才能把草稿转为公开。

## 8. 校验方法

### 8.1 SHA-256：只证明文件一致性

```bash
# Linux
sha256sum -c SHA256SUMS

# macOS
shasum -a 256 -c SHA256SUMS

# Windows（PowerShell，逐个文件）
Get-FileHash agentflow_v1.2.3_windows_amd64.zip -Algorithm SHA256
# 与 release-manifest.json 中该资产的 sha256 逐字比对
```

`SHA256SUMS` 的每一行都是 `摘要  文件名`（两个空格分隔），按文件名排序。排序只依赖文件名，
因此同一版本重新生成会得到完全相同的文件内容。SHA-256 只能发现"文件损坏或被替换"，
不能说明文件来自哪里——来源要靠上一节的证明。

### 8.2 版本一致性

```bash
tar -xzf agentflow_v1.2.3_linux_amd64.tar.gz
./agentflow version
```

`version` 输出必须与 `version.json` 中的 `version`、`commit`、`build_date` 一致。三个来源
（压缩包内的元数据、程序内注入值、发布清单）描述同一份源码，任何一个不一致都说明制品不可信。

### 8.3 发布会复核的项

发布流程在**上传前**与**上传后**各做一次同一套核验（`internal/release/publish.go`）：

- 清单结构合法，版本与标签一致，提交与 `github.sha` 一致；
- 五个平台全部覆盖，每个平台恰好一个制品；
- `SHA256SUMS` 内容与清单逐行一致；
- 每个制品的摘要、体积、压缩包布局与清单一致；
- 目录/远端资产里没有清单之外的任何文件；
- 远端资产没有缺项、没有重名、没有多余项（`verify-assets`）；
- 清单声明了完整的来源信息（`-require-provenance`）。

## 9. 平台支持边界

`internal/release.SupportedPlatforms` 是发布矩阵的唯一来源。

| 平台 | 交叉编译 | 运行冒烟 | 证据 |
| --- | --- | --- | --- |
| Linux amd64 | 已完成 | **未执行** | 制品已生成并校验；本机无 Linux 环境，等待 CI 原生 Runner |
| Linux arm64 | 已完成 | **未执行** | 同上，需要 arm64 运行环境 |
| macOS amd64 | 已完成 | **未执行** | 制品已生成并校验；本机无 macOS 环境 |
| macOS arm64 | 已完成 | **未执行** | 同上 |
| Windows amd64 | 已完成 | 已完成 | 在本机 Windows 11 解压制品、执行 `version`、启动服务并核验页面、资源与 API |

`.github/workflows/release.yml` 的冒烟 Job 已为五个平台配置对应的原生 Runner
（`ubuntu-latest`、`ubuntu-24.04-arm`、`macos-13`、`macos-14`、`windows-latest`），但**该工作流
尚未在真实运行环境执行过**：上表结论都不来自工作流的历史运行记录。首次发布时必须以工作流
的实际结果为准，在此之前只有 Windows amd64 具备运行验收证据。

未进入发布矩阵的平台（Linux 386、Windows arm64、FreeBSD 等）**不支持**：Go 能交叉编译
不等于产品支持该平台。

"构建成功"与"运行验收"是两件事：

- 交叉编译成功只证明该目标能产出可执行文件；
- 运行验收要求解压制品、执行 `version`、启动服务、核验 HTTP API 与前端静态资源；
- `TestReleaseArchiveSmoke` 在当前运行平台与制品平台不一致时输出 `WARN [SKIP]` 并跳过，
  不会把跳过报告成通过。

### 已知限制

- Linux 与 macOS 四个目标平台只完成交叉编译与制品校验，从未在对应操作系统上启动过制品；
  它们的"可运行"目前是推断，不是实测结论。
- 发布工作流从未在 GitHub Actions 上真实运行：`publish.yml` 的门禁、来源证明、上传后核验
  与草稿转公开都只经过本地静态核验与本地等价命令验证。
- 即使在原生 Runner 上完成冒烟，验证范围也仅覆盖"制品可解压、可启动、页面与 API 可用"，
  不包括完整的业务流程。
- 远程任务执行需要目标机器具备 SSH 与 Docker CLI/Engine 29.0.0 及以上，这属于运行前提，
  不是制品的编译依赖。
- 真实 Pi Agent 运行在任务容器内，宿主机不需要安装 Pi。

## 10. 发布安全与权限边界

`release.yml`（构建）：

- 权限固定为 `contents: read`，不使用任何仓库 Secret；
- 制品只上传到本次 Actions 运行的制品区（保留 30 天）；
- 不创建 GitHub Release、不推送镜像、不部署任何环境；
- 制品上传步骤设置 `if-no-files-found: error`，遗漏制品会让构建失败。

`publish.yml`（发布）：

- 工作流级权限只有 `contents: read`；
- 只有 `publish` Job 取得 `contents: write`、`id-token: write`、`attestations: write`；
- 不使用 `pull_request` / `pull_request_target` / `workflow_run` 触发，不可信 PR 代码拿不到
  发布凭据；
- 不写入仓库 Secret，`GH_TOKEN` 使用运行时 `github.token`，不打印到日志；
- 标签值先经正则校验再进入 `env`，所有使用处都加引号，不做命令拼接；
- 不修改分支保护与规则集，不自动发布 Docker 镜像，不自动部署生产环境。

第三方 Action 引用一律用提交 SHA 固定。唯一的例外是来源证明 Action
（`actions/attest-build-provenance@v3`）。该引用目前使用官方主版本引用、**尚未固定到提交
SHA**，这是本轮唯一的豁免项，必须在取得可核对的提交 SHA 后立即收紧。豁免被
`tests/unit/release` 的 `TestWorkflowActionsPinned` 逐条约束：只能是这一条、只能出现在
`publish.yml`、且豁免清单必须与实际使用保持一致。

## 11. 相关入口

| 目的 | 命令 |
| --- | --- |
| 生成全部发布制品 | `make release-dist VERSION=v1.2.3` |
| 核验发布目录（版本、提交、平台、摘要、多余文件） | `make release-verify VERSION=v1.2.3` |
| 由清单生成 Release 说明 | `make release-notes VERSION=v1.2.3` |
| 核验远端资产列表与实际下载内容 | `go run ./cmd/agentflow-release verify-assets -dir <目录> -version <版本> -assets-file <列表>` |
| 只构建当前平台的本机可执行文件 | `make release` |
| 发布制品规则与工作流结构测试 | `go test -count=1 ./tests/unit/release/...` |
| 发布制品端到端打包验收 | `make test-release-verify` |
| 制品运行冒烟（CI 使用） | `TestReleaseArchiveSmoke`，见 [`../tests/README.md`](../tests/README.md) |
| 版本查询 | `agentflow version` 或 `agentflow --version` |
