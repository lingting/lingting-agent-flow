# 发布说明

本文记录 AgentFlow 正式发布制品的版本约定、构建环境、制品布局、校验方法与平台支持边界。
它描述**已经实现并能被核对**的事实；未验证的能力不在此列。

## 1. 版本命名

版本标签使用 `vMAJOR.MINOR.PATCH`，例如 `v1.2.3`。同一字符串同时用于：

- Git 标签名；
- 制品文件名（`agentflow_v1.2.3_linux_amd64.tar.gz`）；
- 程序内 `version` 字段与 `version.json`、`release-manifest.json` 的 `version` 字段。

校验规则集中在 `internal/release.ValidateVersion`：只接受 `dev` 与 `vMAJOR.MINOR.PATCH`。
不接受前缀、后缀、构建元数据与 CI 运行编号——它们会让制品无法与源码一一对应。

| 场景 | version | commit | buildDate |
| --- | --- | --- | --- |
| `make build` / `make release`（未指定 VERSION） | `dev` | 当前 HEAD 短 SHA | 当前 UTC 时间 |
| `make release-dist`（未指定 VERSION） | `dev` | 当前 HEAD 完整 SHA | 当前 UTC 时间 |
| `make release-dist VERSION=v1.2.3` | `v1.2.3` | 当前 HEAD 完整 SHA | 当前 UTC 时间 |
| 标签推送触发的发布工作流 | 与标签完全一致 | 标签指向提交的完整 SHA | 工作流运行时的 UTC 时间 |

本地开发构建显示 `dev`：它是唯一的"非正式版本"取值，不会伪装成正式版本号。环境变量缺失
时不会有任何字段回落到看起来像正式版本的默认值——提交号、构建时间与 Go 版本都取自真实
来源，取不到就直接失败。

`goVersion` 不单独注入：CLI 与 HTTP 接口都通过 `runtime.Version()` 读取实际编译该二进制
的工具链版本，因此不可能与制品清单里的记录不一致。

## 2. 构建环境

| 组件 | 版本要求 | 固定方式 |
| --- | --- | --- |
| Go | `go.mod` 声明的版本（当前 1.24.5） | 工作流 `go-version-file: go.mod` |
| Node.js | `lingting-agent-flow-ui/package.json` 的 `volta.node`（当前 22.22.3） | 工作流 `node-version-file` |
| pnpm | 11.24.0 | 工作流 `pnpm/action-setup` 的 `version` |
| 前端依赖 | 以 `pnpm-lock.yaml` 为准 | `pnpm install --frozen-lockfile` |
| Go 依赖 | 以 `go.sum` 为准 | Go 模块校验和 |

构建制品时显式设置 `CGO_ENABLED=0`。项目的全部依赖（`modernc.org/sqlite`、`pkg/sftp`、
`cobra`、`x/crypto`、`x/term`、`yaml.v3`）都是纯 Go 实现，代码中没有 `cgo` 调用，也没有
平台分文件（`//go:build`）。因此制品不依赖目标机器上预装的 C 库、SQLite 或 Node.js。

## 3. 构建流程

发布入口只有两个，二者共用同一份实现（`internal/release.BuildDistribution`）：

```bash
# 本地生成全部平台的发布制品
make release-dist VERSION=v1.2.3

# CI 中由标签推送或手动触发执行同一入口
.github/workflows/release.yml
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

## 4. 制品布局与命名

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
| `version.json` | 版本、提交、构建时间、Go 版本、目标 OS 与架构 |

`release-manifest.json` 汇总本次构建的全部制品：

```json
{
  "version": "v1.2.3",
  "commit": "3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a",
  "build_date": "2026-01-02T03:04:05Z",
  "go_version": "go1.24.5",
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

清单与校验文件都不包含仓库绝对路径、环境变量、令牌、私钥路径或测试配置：它们的字段由
`internal/release.Manifest` 固定，`tests/unit/release` 会逐项检查这一点。

## 5. 校验方法

```bash
# Linux
sha256sum -c SHA256SUMS

# macOS
shasum -a 256 -c SHA256SUMS

# Windows（PowerShell）
Get-FileHash agentflow_v1.2.3_windows_amd64.zip -Algorithm SHA256
```

`SHA256SUMS` 的每一行都是 `摘要  文件名`（两个空格分隔），按文件名排序。排序只依赖文件名，
因此同一版本重新生成会得到完全相同的文件内容。

单文件核对：

```bash
tar -xzf agentflow_v1.2.3_linux_amd64.tar.gz
./agentflow version
```

`version` 输出必须与 `version.json` 中的 `version`、`commit`、`build_date` 一致。三个制品
（压缩包内的元数据、程序内注入值、发布清单）描述同一份源码，任何一个不一致都说明制品
不可信。

## 6. 平台支持边界

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
尚未在真实运行环境执行过**：以下结论都不来自工作流的历史运行记录。首次发布时必须以工作流
的实际结果为准，在此之前只有 Windows amd64 具备运行验收证据。

未进入发布矩阵的平台（Linux 386、Windows arm64、FreeBSD 等）**不支持**：Go 能交叉编译
不等于产品支持该平台。这些平台既不会被构建，也不会出现在清单与冒烟矩阵里。

"构建成功"与"运行验收"是两件事：

- 交叉编译成功只证明该目标能产出可执行文件；
- 运行验收要求解压制品、执行 `version`、启动服务、核验 HTTP API 与前端静态资源；
- `TestReleaseArchiveSmoke` 在当前运行平台与制品平台不一致时输出 `WARN [SKIP]` 并跳过，
  不会把跳过报告成通过。

### 已知限制

- Linux 与 macOS 四个目标平台只完成交叉编译与制品校验，从未在对应操作系统上启动过制品；
  它们的"可运行"目前是推断，不是实测结论。
- 即使在原生 Runner 上完成冒烟，验证范围也仅覆盖"制品可解压、可启动、页面与 API 可用"，
  不包括完整的业务流程。
- 本机没有 Linux/macOS 运行环境，也没有可用的远程 Linux 主机，因此本地无法补齐这部分证据。
- 远程任务执行需要目标机器具备 SSH 与 Docker CLI/Engine 29.0.0 及以上，这属于运行前提，
  不是制品的编译依赖。
- 真实 Pi Agent 运行在任务容器内，宿主机不需要安装 Pi。

## 7. 发布安全边界

发布工作流只做"构建与制品准备"：

- 权限固定为 `contents: read`，不使用任何仓库 Secret；
- 制品上传到本次 Actions 运行的制品区（保留 30 天）；
- **不创建 GitHub Release**、不推送 Docker 镜像、不部署任何环境；
- 不生成或使用签名密钥，不修改分支保护或仓库权限；
- 所有 Action 引用都用提交 SHA 固定，避免上游重新指向标签时构建行为变化；
- 制品上传步骤设置 `if-no-files-found: error`，遗漏制品会让构建 Job 失败，而不是"显示
  成功但没有制品"。

## 8. 相关入口

| 目的 | 命令 |
| --- | --- |
| 生成全部发布制品 | `make release-dist VERSION=v1.2.3` |
| 只构建当前平台的本机可执行文件 | `make release` |
| 发布制品规则与工作流结构测试 | `go test -count=1 ./tests/unit/release/...` |
| 发布制品端到端打包验收 | `make test-release-verify` |
| 制品运行冒烟（CI 使用） | `TestReleaseArchiveSmoke`，见 [`../tests/README.md`](../tests/README.md) |
| 版本查询 | `agentflow version` 或 `agentflow --version` |
