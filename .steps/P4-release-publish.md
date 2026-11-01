# P4 工程质量：正式 Release 发布与制品可信分发

本轮把"能构建发布制品"推进到"能发布可验证的正式版本"：建立 GitHub Release 自动发布流程，
把制品、校验文件与版本清单绑定到同一个标签，用 GitHub Actions OIDC 无密钥来源证明回答
"这份二进制是不是由这个仓库构建的"，并在上传前后各做一次完整性核验。

基线为当前本地 checkout（`8c6a28b`，未执行 `git fetch`）。本轮**不创建、不推送任何正式版本
标签，也不发布公开 Release**。

## 1. 开工前的资产盘点

| 已有能力 | 位置 | 本轮处理 |
| --- | --- | --- |
| 版本 tag 规范 `vMAJOR.MINOR.PATCH` | `internal/release.ValidateVersion` | 复用；`publish.yml` 在第一步用同一格式校验标签 |
| Go / Node / pnpm 环境固定 | `ci.yml`、`release.yml` | 复用；补齐 Node / pnpm 版本**进入制品元数据** |
| 多平台构建矩阵（5 个） | `internal/release.SupportedPlatforms` | 复用；新增"平台覆盖完整"核验 |
| 单文件程序 + 前端嵌入 | `//go:embed`、`VerifyWebDist` | 复用 |
| `SHA256SUMS` 与 `release-manifest.json` | `internal/release.builder_output.go` | 复用；清单新增 `node_version` / `pnpm_version` / 来源字段 |
| 打包冒烟测试 | `tests/e2e/packaging` | 复用；补 `verify-release` / `verify-assets` 真实核验路径 |
| CI 全量门禁 | `ci.yml` | 复用为可调用工作流（`workflow_call`） |

盘点结论：制品能对应到源码提交、目标平台与校验文件，但**无法对应到 Node / pnpm 版本，也无法
对应到"哪一次工作流运行"**，且没有任何发布与来源证明能力。先把这三处补齐，再接发布流程。

## 2. 补齐溯源信息（`internal/release`）

- `Metadata` / `Manifest` 新增 `node_version`、`pnpm_version`。这两个值由构建时实际执行的
  `node --version`、`pnpm --version` 得出（`Makefile` 的 `NODE_VERSION` / `PNPM_VERSION`），
  不是人工填写的期望值。正式版本缺少任一项时构建直接失败；`dev` 构建允许留空。
- `Manifest` 新增 `source_repository`、`source_workflow`、`source_run_id`、
  `attestation_provider`。前三个由工作流从 `github.repository` / `github.workflow_ref` /
  `github.run_id` 传入，本地构建为空（`omitempty`）。
- `ValidToolVersion` / `NormalizeToolVersion`（`internal/release/toolchain.go`）统一
  `MAJOR.MINOR.PATCH` 格式与 `v` 前缀处理，拒绝 `latest`、分支名与 CI 运行编号。

## 3. 发布核验规则（`internal/release/publish.go`）

规则只有一处来源，供工作流、e2e 用例与本地入口共用：

| 规则 | 函数 | 拒绝的情形 |
| --- | --- | --- |
| 资产集合 | `Manifest.ReleaseAssetNames` | 集合不由清单派生、顺序不稳定 |
| 平台覆盖 | `Manifest.VerifyPlatformCoverage` | 少一个平台（上传阶段不会自己暴露） |
| 来源信息 | `Manifest.RequireProvenance` | 缺仓库 / 工作流 / 运行标识 / 证明提供方 |
| 发布目录 | `VerifyRelease` | 版本或提交不符、校验文件不符、摘要不符、体积不符、压缩包布局错、目录里多出文件 |
| 远端资产 | `VerifyReleaseAssets` | 缺资产、重名、存在未声明的资产 |
| 版本说明 | `Manifest.Notes` | 说明引用的资产名与实际资产集合不一致 |

`cmd/agentflow-release` 新增三个子命令：`verify-release`、`verify-assets`、`notes`
（无子命令时仍是 `build`，`make release-dist` 调用方式不变）。退出码区分参数错误（2）与
核验失败（1）。

## 4. 工作流结构

```text
push v*.*.*  ─┐
              ├─→ resolve ─→ ci（复用 ci.yml）────┐
workflow_dispatch ┘         └→ build（复用 release.yml）─┤
                                                       ↓
                                              publish（contents/id-token/attestations 写权限）
                                                       ↓
                                              publish-summary（always，非 success 即失败）
```

- `release.yml` 去掉 `push: tags` 触发，改为 `workflow_call` + `workflow_dispatch`，并暴露
  `version` / `commit` / `artifact_name` 输出。标签发布只经 `publish.yml` 一次构建。
- `ci.yml` 增加 `workflow_call`，让"同一次运行里通过的 CI 门禁"成为发布前提，而不是
  "标签提交早先在 master 上通过过"。
- `publish.yml` 顺序固定：发布前核验 → 生成来源证明 → 复核来源声明 → 拒绝覆盖 → 创建草稿 →
  上传 → 重新下载核验远端资产 → 核验来源证明 → 草稿转公开 → 复核已公开。
- 权限：工作流级只有 `contents: read`；`publish` Job 才有写权限；不使用
  `pull_request_target` / `workflow_run`，不使用任何仓库 Secret。

顺带修复了一个真实缺陷：`release.yml` 的"输出发布清单摘要"步骤里 Python f-string 写成了
`` `artifact['file']}` ``（少了 `{`），在 Python 3.12 上会抛
`f-string: single '}' is not allowed`，导致构建 Job 在最后一步失败。

## 5. 来源证明的取舍

采用 GitHub 官方 Artifact Attestation（`actions/attest-build-provenance`）：

- 无密钥：由 Actions OIDC 签发，不需要私钥、不需要 Secret、不需要维护签名基础设施；
- 证明对象是 7 个资产的内容摘要（5 个压缩包 + `SHA256SUMS` + 清单）；
- 用户用 `gh attestation verify <文件> --repo <repo>` 核验，不需要额外下载材料；
- 证明生成失败或核验失败都会让发布失败，**不提供降级路径**。

**已知例外**：该 Action 使用官方主版本引用 `@v3`，是本仓库唯一未按提交 SHA 固定的 Action。
这是本轮明确记录、需要后续收紧的少数项，由 `TestWorkflowActionsPinned` 逐条约束（只能是这
一条、只能出现在 `publish.yml`、豁免清单必须与实际使用一致）。

## 6. 本轮新增 / 修改的测试

| 用例 | 覆盖 |
| --- | --- |
| `TestVerifyReleaseAcceptsCompleteAssetSet` | 真实压缩包 + 清单 + 校验文件目录能通过核验 |
| `TestVerifyReleaseRejectsInconsistentDirectory` | 缺制品、多文件、内容被篡改、校验文件被改、缺清单、版本/提交不符、平台覆盖不完整、缺来源信息 |
| `TestVerifyReleaseAssets` | 完整 / 缺项 / 多余 / 重名 / 空列表 |
| `TestReleaseAssetNames` | 资产集合由清单派生且按文件名有序 |
| `TestManifestRequireProvenance` | 四个来源字段缺失、提供方不受支持、格式非法 |
| `TestManifestNotes` | 说明含版本元数据、资产与两种核验方式；无来源信息时不给证明命令 |
| `TestReleaseWorkflowTriggers` | 构建工作流不再由 push 触发，改为 `workflow_call` + 输出契约 |
| `TestPublishWorkflowTriggers` | 标签推送 / 手动触发；禁止 PR 与 `workflow_run`；禁止 `continue-on-error` |
| `TestPublishWorkflowPermissions` | 写权限只属于 `publish` Job，且必须是 contents / id-token / attestations |
| `TestPublishWorkflowGates` | 依赖关系、门禁条件、复用工作流、发布步骤顺序、发布 Job 不重新构建 |
| `TestPublishWorkflowAttestation` | 证明存在、`subject-path` 覆盖全部资产、核验早于公开 |
| `TestPublishWorkflowAssetWiring` | 制品名单一来源，构建上传与发布下载一致 |
| `TestWorkflowActionsPinned` | 豁免清单唯一且必须与工作流一致 |
| `TestWorkflowExpressions` | 三个工作流的表达式上下文（新增 `jobs` 用于 `workflow_call.outputs`） |
| `TestMetadataValidate` / `TestManifestValidate` / `TestBuildDistributionRejectsBadInput` | Node / pnpm 版本缺失或非法被拒绝 |
| `TestReleaseArtifacts`（e2e） | 真实构建 5 个平台制品后用 `verify-release` / `verify-assets` 复核目录与资产列表，并断言两种异常列表必须失败 |

## 7. 实际执行结果

全部命令在本机 Windows 11 + Git Bash 下执行，并发度保持项目既有设置（`-p 8 -parallel 8`、
Vitest `--maxWorkers 4`、Playwright `--workers=4`）。

### 7.1 发布相关

| 命令 | 结果 |
| --- | --- |
| `go test -count=1 ./tests/unit/release/` | 通过（38 个用例） |
| `go test -count=1 -v ./tests/e2e/packaging/` | 通过：`TestSingleBinaryPackaging`、`TestReleaseArtifacts` 通过；`TestReleaseArchiveSmoke` 输出 `WARN [SKIP]`（未提供 `AGENTFLOW_RELEASE_ARCHIVE`） |
| `make release-dist VERSION=v0.0.0` | 通过：5 个平台制品 + `SHA256SUMS` + 清单自检；清单含 `node_version=22.22.3`、`pnpm_version=11.24.0`，本地构建的 `source_*` 为空 |
| `make release-verify VERSION=v0.0.0` | 通过：版本、提交、平台覆盖、摘要、目录无多余文件全部成立 |
| `make release-notes VERSION=v0.0.0` | 按设计失败：本地清单缺少来源信息，说明拒绝生成 |
| `verify-assets`（完整 / 缺一项 / 多一项） | 依次为 通过 / 失败 / 失败，异常列表被拒绝 |
| `gofmt -l cmd internal tests` | 无输出 |
| `go vet ./...` / `go build ./...` | 通过 |

### 7.2 全量测试

| 命令 | 结果 |
| --- | --- |
| `make test` | 42 个包通过；2 个远程包失败（见下） |
| `make test-race` | 19 个包通过；1 个包出现加载导致的超时（见下） |
| `go test -race -count=1 ./tests/integration/local/database/` | 通过（21 个用例） |
| `go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/` | 通过（699 个用例） |
| `pnpm test:all:safe`（`lingting-agent-flow-ui/`） | 通过：Vitest 635 个用例、Playwright 257 个用例、`vp fmt` / `vp lint --fix` / `tsc --noEmit` / 生产构建全部成功 |

### 7.3 失败项的真实原因（均不在本轮改动范围内）

1. `tests/integration/remote/agent` 与 `tests/integration/remote/codesaver`：包级收尾的
   "远程容器零残留核验"把**并行运行的其他远程套件创建的容器**判为"未登记归属"而失败，
   典型信息为 `出现未登记的测试容器 agentflow-test-…-impostor|flaky|run，无法确认归属，未删除`。
   同一轮中 `tests/integration/remote/docker` 与 `tests/integration/remote/workflow` 通过，
   说明这是既有路线图中已记录的"并行误判"问题（下一步候选之一是"按包归属 / 运行标识过滤
   远程零残留核验"），与本轮发布改动无关。
2. `tests/unit/store` 的 `TestMigrateUpgradesExistingDatabase`：`make test-race` 下报
   `读取 schema 版本失败: context deadline exceeded`（44 秒），单独以 `-race` 复跑该包
   （248 个用例）与单独复跑该用例均通过。属于整机并发满载下的超时，不涉及本轮改动。
3. 其中一次 `make test-race` 运行中 `tests/unit/httpapi` 的 `TestExecuteStepAPINotFound`
   也出现过一次同类超时；单独以 `-race` 复跑该包（117 秒）与单独复跑该用例均通过。

以上三项都在本轮未修改的代码路径上（`tests/internal/testkit` 的远程收尾、`tests/unit/store`、
`tests/unit/httpapi`），本轮按"只做必要修改"的原则未改动它们，也未通过降低断言或跳过测试
制造通过结果。

### 7.4 未执行的验证

- 未推送版本标签，未在 GitHub Actions 上运行 `publish.yml`：远端发布、来源证明签发、
  草稿转公开、重复发布拒绝均未真实执行；
- 未创建公开 Release；
- Linux / macOS 四个平台的运行冒烟仍未执行（等待 CI 原生 Runner）。

## 8. 尚未执行的真实公开发布步骤

本轮只建设发布能力，下列步骤一次也没有真实执行过，不得报告为已完成：

1. 创建并推送版本标签（例如 `git tag v1.2.3 && git push origin v1.2.3`）；
2. `publish.yml` 在 GitHub Actions 上的首次运行（标签校验 → CI 门禁 → 制品构建与五平台冒烟）；
3. GitHub 签发 Artifact Attestation（`attestations: write` 实际生效）；
4. 创建草稿 Release、上传 7 个资产；
5. 上传后重新下载核验与 `gh attestation verify` 逐个资产核验；
6. 草稿转公开、`--latest` 生效；
7. 重复发布拒绝路径（第二次对同一标签运行）；
8. Linux / macOS 四个平台的运行冒烟。

另外：`P4-M5` 仍为 `pending`——本轮只建设发布能力，没有产生任何真实使用数据。
