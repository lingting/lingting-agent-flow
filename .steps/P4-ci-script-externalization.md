# P4 工程质量：CI/CD 脚本外置化与 GitHub Actions 工作流精简

- **对象**：`.github/workflows/ci.yml`、`.github/workflows/release.yml`、
  `.github/workflows/publish.yml` 与 `.github/scripts/`
- **性质**：纯重构（执行位置迁移 + 必要修正），不新增产品功能、不改真实测试授权策略、
  不推进 `P4-M5`
- **结论**：`completed`（脚本迁移、YAML 精简、脚本契约测试、静态与行为验证均已完成；
  GitHub Actions 远端 Workflow Run 仍未执行，见 §8）

## 1. 迁移前盘点

开工前 `.github/` 只有 5 个文件：3 个工作流、2 个 Python 脚本，其余执行逻辑全部内嵌在
YAML 的 `run: |` 块里。

| 迁移前位置 | 内嵌逻辑职责 | 迁移目标 | 调用工作流 |
| --- | --- | --- | --- |
| `ci.yml` / `go-static` / 「格式、静态检查与编译」 | `gofmt -l` 非零门禁、`go vet ./...`、`go build ./...` | `.github/scripts/check-go-static.sh` | `ci.yml` |
| `ci.yml` / `go-test` / 「全量 Go 测试」 | 建产物目录、把 `TMPDIR` 指向工作区、`make test` 并 tee 日志 | `.github/scripts/run-go-tests.sh` | `ci.yml` |
| `ci.yml` / `go-test` / 「汇总外部依赖跳过原因」 | 扫描 `test.log` 的 `WARN [SKIP]` 并拼 Markdown 摘要 | `.github/scripts/summarize-test-skips.sh` | `ci.yml` |
| `ci.yml` / `go-race` / 「竞态检测」 | `make test-race` + 数据库 + 工作流三组命令、日志与结论核对 | `.github/scripts/run-go-race-tests.sh` | `ci.yml` |
| `ci.yml` / `ui-test` / 「前端单元、E2E、格式、lint、类型检查与构建」 | 建产物目录、`pnpm test:all:safe` 并 tee 日志 | `.github/scripts/run-frontend-tests.sh` | `ci.yml` |
| `ci.yml` / `ui-test` / 「核验格式与 lint 无待修复改动」 | `git status --porcelain` 门禁与 diff 输出 | `.github/scripts/check-frontend-clean.sh` | `ci.yml` |
| `ci.yml` / `real-tests-gate` / 「判定真实测试资格」 | 组装 8 个参数调用 `cmd/agentflow-ci real-tests-gate` | `.github/scripts/check-real-test-authorization.sh` | `ci.yml` |
| `ci.yml` / `real-tests` / 「准备真实测试配置」 | 已是仓库脚本，仅去掉多余的 `run: \|` 包装 | `.github/scripts/prepare-real-tests-config.py`（沿用） | `ci.yml` |
| `ci.yml` / `real-tests` / 「真实外部依赖测试」 | 三条真实测试包路径、`-p 4 -parallel 4`、日志与退出码 | `.github/scripts/run-real-tests.sh` | `ci.yml` |
| `ci.yml` / `real-tests` / 「汇总真实测试结论」 | 授权模式/配置/结论表 + 跳过清单 | `.github/scripts/summarize-real-tests.sh` | `ci.yml` |
| `ci.yml` / `ci-summary` / 「汇总必需 Job 结论」 | 6 个 Job 的结论状态机与真实测试跳过语义 | `.github/scripts/summarize-ci-gate.sh` | `ci.yml` |
| `release.yml` / `resolve` / 「解析版本号与提交号」 | 版本与标签一致性、正则校验、制品名生成 | `.github/scripts/resolve-release-version.sh` | `release.yml` |
| `publish.yml` / `resolve` / 「校验标签格式与提交号」 | 同上（更严格：ref 必须是标签） | `.github/scripts/resolve-release-version.sh` | `publish.yml` |
| `release.yml` / `build` / 「构建并打包发布制品」 | 从 `workflow_ref` 解析工作流路径、组装 `make release-dist` 参数 | `.github/scripts/build-release-dist.sh` | `release.yml` |
| `release.yml` / `build` / 「校验制品与校验文件一致」+「核验发布目录」 | `sha256sum -c` 与 `verify-release`（两步重复） | `.github/scripts/verify-release-assets.sh` | `release.yml`、`publish.yml` |
| `release.yml` / `build` / 「输出发布清单摘要」 | 读清单生成 Markdown 再 `cat` 追加摘要 | `.github/scripts/release-artifacts-summary.py`（契约收紧） | `release.yml` |
| `publish.yml` / `publish` / 草稿与上传相关 7 个步骤 | 拒绝覆盖、创建草稿、上传资产、上传后重新下载核验、`gh attestation verify`、转公开、复核 | `.github/scripts/github-release.sh` + `.github/scripts/verify-release-assets.sh` | `publish.yml` |
| `release.yml` / `release-summary` / 「汇总发布构建结论」 | 3 个阶段结论表与失败判定 | `.github/scripts/summarize-release-stages.sh` | `release.yml` |
| `publish.yml` / `publish-summary` / 「汇总发布结论」 | 4 个阶段结论表与失败判定（与上一步重复实现） | `.github/scripts/summarize-release-stages.sh` | `publish.yml` |

## 2. 迁移后目录结构

```text
.github/
├── workflows/
│   ├── ci.yml           # 只声明触发、Job、依赖、权限与脚本调用
│   ├── release.yml      # 只声明解析、构建、冒烟矩阵与汇总
│   └── publish.yml      # 只声明阶段门禁、权限与脚本调用
└── scripts/
    ├── README.md                     # 脚本索引：职责 / 参数 / 输出 / 退出码 / 调用方
    ├── check-go-static.sh
    ├── run-go-tests.sh
    ├── run-go-race-tests.sh
    ├── run-frontend-tests.sh
    ├── check-frontend-clean.sh
    ├── check-real-test-authorization.sh
    ├── prepare-real-tests-config.py
    ├── run-real-tests.sh
    ├── summarize-test-skips.sh
    ├── summarize-real-tests.sh
    ├── summarize-ci-gate.sh
    ├── resolve-release-version.sh
    ├── build-release-dist.sh
    ├── verify-release-assets.sh
    ├── release-artifacts-summary.py
    └── github-release.sh
```

脚本按职责划分，不按单条命令拆分：`check-go-static.sh` 承载一个门禁的三个阶段，
`github-release.sh` 承载同一资源（GitHub Release）的生命周期子命令，
`summarize-release-stages.sh` 被两个工作流复用而不是各写一份。

## 3. YAML 与脚本职责边界

YAML 现在只保留：`name` / `on` / `permissions` / `concurrency` / `env` / `jobs` / `needs` /
`if` / `strategy` / `matrix` / `runs-on` / `timeout-minutes` / `uses` / `with` / `secrets`，
以及步骤里的脚本调用与参数。

仍然留在 YAML 里的执行命令只有三类单行工具调用，它们没有逻辑分支、没有管道、没有 heredoc：

| 步骤 | 命令 | 保留原因 |
| --- | --- | --- |
| 安装前端依赖（ci / go-test、ci / ui-test、release / build） | `pnpm install --frozen-lockfile` | 单条依赖安装命令，无参数编排 |
| 安装 Chromium（ci / ui-test） | `pnpm exec playwright install --with-deps chromium` | 同上 |
| 运行制品冒烟（release / smoke） | `go test -run TestReleaseArchiveSmoke ./tests/e2e/packaging/...` | 仓库内 Go 用例的单一入口 |
| 生成 Release 说明（publish / publish） | `go run ./cmd/agentflow-release notes ...` | 单条只读工具调用，参数全部可见 |
| 输出发布清单摘要（release / build） | `python3 .github/scripts/release-artifacts-summary.py "$RELEASE_DIR"` | 直接调用仓库脚本 |

这条边界由测试机械约束（见 §7），不再依赖人工检查。

## 4. 参数传递规范

- **固定参数**走命令行：`--artifact-dir`、`--parallel`、`--dir`、`--version`、`--commit`、
  `--stage "名称=结论"`。脚本自行校验，缺失或非法以退出码 2 拒绝。
- **动态值**走步骤级 `env:`：`REF_TYPE` / `REF_NAME` / `COMMIT_SHA`、
  `SOURCE_REPOSITORY` / `SOURCE_RUN_ID` / `SOURCE_WORKFLOW_REF`、`GATE_*`、`CONFIGURED`、
  `TESTS_OUTCOME`、`GO_*_RESULT` 等。不可信文本（PR 作者、事件名、关联身份、PR 标题）
  一律只经环境变量进入脚本，再以独立 argv 传给判定工具，不拼接 shell 字符串。
- **结论回传**只走 `GITHUB_OUTPUT`（`version` / `commit` / `artifact_name` /
  `real_tests_enabled` / `mode` / `reason` / `configured`）与 `GITHUB_STEP_SUMMARY`；
  成功与失败只由退出码表达，不靠日志文本。
- **Secret** 只出现在 `ci.yml` 的 `real-tests` Job 的步骤级 `env:`，其余 Job 与授权判断
  Job 都不引用 Secret；Secret 不作为命令行参数，也不写入任何输出文件。

## 5. 安全边界（与迁移前一致）

- 权威判定仍在 `internal/cigate`：`check-real-test-authorization.sh` 只做
  `env → 独立 argv` 的转发，脚本内没有任何身份比较（由用例断言）。
- `AGENTFLOW_REAL_TESTS` 仍是唯一的用户变量，取值语义、默认关闭、大小写敏感、
  PR 按 `author_association` 判定、push 按 `github.actor` 判定、其他事件一律不授权，
  行为完全未变（§7 有逐项实测结果）。
- 授权判断 Job 仍只检出可信提交（PR 事件检出基础提交），不执行被审核 PR 的代码；
  `pull_request_target` 与 `workflow_run` 仍被禁用。
- 权限未扩大：`ci.yml` 仍为 `contents: read`；`publish.yml` 仍只给 `publish` Job
  `contents: write` / `id-token: write` / `attestations: write`。
- 新增的一道收紧：`run-real-tests.sh` 要求 `AGENTFLOW_REAL_TESTS_GRANTED=true`，缺失时
  直接失败——避免"没有任何真实用例被执行"被报告为成功。这不会放宽任何授权范围。
- 发布流程仍不使用仓库 Secret；`github-release.sh` 不删除 Release、不覆盖已有资产、
  不打印令牌。

## 6. 行为差异与取舍

| 差异 | 原因 | 影响 |
| --- | --- | --- |
| `publish.yml` / `resolve`、`release.yml` / `resolve` 现在先检出代码 | 校验逻辑已外置为仓库脚本，需要文件存在 | 检出仍发生在任何构建 / 上传 / 发布动作之前，标签不合格照样最早失败 |
| `release.yml` / `build` 的「校验制品与校验文件一致」与「核验发布目录」合并为一步 | 两步都在做 `sha256sum -c` + `verify-release`，重复且易漂移 | 核验内容不减（摘要 + 版本 + 提交 + 平台覆盖 + 无多余文件） |
| `release-artifacts-summary.py` 改为接收制品目录并直接写 Job 摘要 | 原来由 YAML 拼临时文件再 `cat`，属于 YAML 里的程序 | 摘要内容不变；脚本新增 `GITHUB_STEP_SUMMARY` 输出与目录校验 |
| `run-go-race-tests.sh` 用真实退出码代替 `grep '^FAIL'` 日志判定 | 管道退出码已可用，日志匹配是冗余判据 | 三组命令仍全部执行完再判定，失败结论更准确 |
| `run-real-tests.sh` 增加授权标记守卫 | 未授权时 testkit 全部 SKIP，Job 会"成功"但什么都没跑 | 只有授权 Job 通过后才会执行；未授权直接失败 |
| `resolve-release-version.sh` 统一承接两个工作流的版本解析 | 版本格式与制品名生成原来在两处重复 | 输出多一个 `artifact_name`（`publish.yml` 不使用，已在脚本 README 注明） |

补充：`publish.yml` 的「核验来源信息完整」也复用 `verify-release-assets.sh`，因此生成来源证明
之后会再跑一次 `sha256sum -c`。这是严格性的超集（不会减少任何核验），代价只是一次本地摘要
校验。

## 7. 验证命令与实际结果

### 7.1 静态检查

| 命令 | 结果 |
| --- | --- |
| `bash -n .github/scripts/*.sh` | 全部通过（无语法错误） |
| `shellcheck` | 本机未安装，按任务要求不额外引入依赖，未执行 |
| `actionlint` | 本机未安装，改用工作流 YAML 的结构化解析（见下） |
| `gofmt -l cmd internal tests` | 无输出 |
| `go vet ./...` | 无输出 |
| `git diff --check` | 无空白错误 |
| 变更文件 CR 扫描 | 无 CR，全部 LF |

工作流与脚本的机械核验由 `tests/unit/release/ci_scripts_test.go` 承担（YAML 用
`gopkg.in/yaml.v3` 解析，因此同时验证了三个工作流的语法）：

- `TestCIScriptReferencesExist`：YAML 引用的脚本必须存在，且用 `bash` / `python3`
  显式调用（含 `$GITHUB_WORKSPACE` 绝对路径形式）；
- `TestCIScriptsAllReferenced`：脚本目录里不得有未被引用的脚本；
- `TestCIScriptsStrictModeAndDocs`：`#!/usr/bin/env bash`、`set -euo pipefail`、不使用
  `eval`、头部必须写明用途 / 参数 / 退出码 / 副作用、脚本自行解析仓库根目录；
- `TestCIScriptFlagsDeclared`：YAML 传给脚本的每个参数都必须在脚本里存在；
- `TestWorkflowHasNoInlinePrograms`：`run:` 里不得出现 `set -`、heredoc、`$(`、管道、
  `tee` 或 Shell 控制流；不调用脚本的步骤必须是单行命令；
- `TestCIScriptsUseLF`：脚本全部 LF；
- `TestAuthorizationScriptPassesContextAsArguments`：授权脚本必须逐项转发事件上下文，
  且不得自己实现身份判断。

### 7.2 脚本行为测试

| 用例 | 覆盖 | 结果 |
| --- | --- | --- |
| `TestResolveReleaseVersionScript` | 标签成功输出三个 output + 摘要；分支拒绝（不写 output）；版本格式非法；提交号不是 40 位；输入版本与标签冲突；未知参数 | 通过 |
| `TestSummarizeReleaseStagesScript` | 全 success；被跳过阶段判失败；缺 `--title`；缺 `--stage`；阶段格式非法 | 通过 |
| `TestSummarizeCIGateScript` | 真实测试跳过不算失败；测试失败判失败；真实测试执行后失败判失败；缺结论判失败 | 通过 |
| `TestRunRealTestsScriptRequiresGrant` | 未授权直接失败并给出可读原因；缺 `--artifact-dir` 以 2 拒绝 | 通过 |
| `TestAuthorizationScriptWithStubTool` | 用替身 `go` 捕获 argv，校验 8 个参数与事件上下文一致 | Windows 下 `WARN [SKIP]`（MSYS 不支持 PATH 替身），CI 的 Ubuntu Runner 会执行 |

`go test -count=1 ./tests/unit/release/ ./tests/unit/cigate/` → **172 passed**
（`internal/cigate` 的身份判定用例未被改动，全部通过）。

### 7.3 手工执行脚本（本机实测）

| 命令 | 结果 |
| --- | --- |
| `bash .github/scripts/check-go-static.sh` | 退出码 0，输出「Go 格式检查、静态检查与编译全部通过。」 |
| `bash .github/scripts/check-frontend-clean.sh` | 退出码 0，输出「lingting-agent-flow-ui 没有待修复改动。」 |
| `summarize-test-skips.sh --base <伪造 test.log 目录>` | 退出码 0，摘要正确列出 `WARN [SKIP]` 记录 |
| `summarize-ci-gate.sh`（4 个 Job success、真实测试 skipped） | 退出码 0，结论表标注「未执行：未获授权……不计为通过」 |
| `check-real-test-authorization.sh`（8 组事件/模式组合） | 见 §7.4，结论与既有策略完全一致 |
| `verify-release-assets.sh` 无参数 | 退出码 2，输出「缺少必需参数: --dir --version --commit」 |
| `build-release-dist.sh` 无参数 | 退出码 2，输出「--version、--dir 与 --attestation-provider 都是必需参数」 |
| `github-release.sh` 无子命令 | 退出码 2，输出「缺少子命令」 |
| `run-real-tests.sh`（`AGENTFLOW_REAL_TESTS_GRANTED=false`） | 退出码 1，输出「未获真实测试授权……」 |
| `build-release-dist.sh --version v0.0.1 --dir <临时目录> --attestation-provider github-artifact-attestation` | 退出码 0，生成 5 平台制品、`SHA256SUMS` 与 `release-manifest.json` |
| `verify-release-assets.sh --dir <上一步目录> --version v0.0.1 --commit <HEAD>` | 退出码 0，`sha256sum -c` 全部 OK 且「发布目录核验通过：版本 v0.0.1 … 制品 5 个」 |
| 同上但 `--version v0.0.2` | 退出码 1，「发布清单版本 "v0.0.1" 与期望的版本 "v0.0.2" 不一致」 |
| `release-artifacts-summary.py <上一步目录>` | 退出码 0，列出 5 个制品的名称、大小与 SHA-256 |
| `github-release.sh ensure-absent/…` | 未执行：本机无发布标签，`gh` 写操作不得在本地模拟（见 §8） |

### 7.4 真实测试授权语义逐项实测（经新脚本执行）

| 事件 | `AGENTFLOW_REAL_TESTS` | PR 作者关联 | 结论 | 判定原因摘要 |
| --- | --- | --- | --- | --- |
| `pull_request` | （空） | `CONTRIBUTOR` | 拒绝 | 未配置，默认关闭 |
| `pull_request` | `self` | `OWNER` | 授权 | PR 作者是仓库所有者 |
| `pull_request` | `self` | `CONTRIBUTOR` | 拒绝 | `self` 只允许所有者 |
| `pull_request` | `collaborator` | `MEMBER` | 授权 | 具备成员或协作者身份 |
| `pull_request` | `collaborator` | `CONTRIBUTOR` | 拒绝 | 历史贡献者不算当前协作者 |
| `pull_request` | `all` | `CONTRIBUTOR` | 授权 | `all` 不区分作者身份 |
| `pull_request` | `SELF`（大小写错误） | `OWNER` | 拒绝 | 取值不受支持，按关闭处理 |
| `workflow_call` | `all` | `OWNER` | 拒绝 | 该事件不参与身份判定 |

`push` 事件的 `github.actor` 判定与 `collaborator` 模式的写入权限查询未在本机实测：
它需要真实仓库令牌，本地只验证了「查不出权限按拒绝处理」的既有用例
（`tests/unit/cigate`）。

### 7.5 Go 全量测试

`make test` → 退出码 2。42 个包通过，3 个包失败，全部集中在真实远程依赖套件：

| 失败包 | 失败原因（日志原文摘要） |
| --- | --- |
| `tests/integration/remote/agent` | `TestRemoteSourceFetch`：`kex_exchange_identification: Connection closed by remote host`（远端机器访问 GitHub 被拒） |
| `tests/integration/remote/codesaver` | 用例本体 PASS，finalizer 判失败：「出现未登记的测试容器……无法确认归属，未删除」（历史残留容器） |
| `tests/integration/remote/workflow` | `TestRemoteE2EFullChain`：任务分支 `Clone 失败: ssh://github.com/... Connection closed by remote host` |

这三处失败来自本机 `tests/config/{ssh,docker}.yml` 已启用的真实远程目标与远端网络状态，
与本轮改动的文件（工作流、脚本、`tests/unit/release`）无调用关系；同一次运行中
`remote/docker`、`local/*`、`unit/*`、`e2e/*` 均通过。跳过项：
`TestReleaseArchiveSmoke`（未提供 `AGENTFLOW_RELEASE_ARCHIVE`）、
`TestRealPiModelRecognition`、`TestEmbeddedFSHasNoIndexInSourceCheckout`、
`TestAuthorizationScriptWithStubTool`（Windows）。

### 7.6 竞态检测

| 命令 | 结果 |
| --- | --- |
| `make test-race` | 退出码 2：`tests/unit/store` 的 `TestMigrateUpgradesExistingDatabase` 报 `context deadline exceeded`（8 个包并发 + `-race` 下本机超时） |
| `go test -race -count=1 -run TestMigrateUpgradesExistingDatabase ./tests/unit/store/` | 通过（4 秒级完成） |
| `go test -race -count=1 ./tests/unit/store/` | 通过 |
| `go test -race -count=1 ./tests/integration/local/database/` | 退出码 1：`TestLegacyDatabaseConcurrentOpenMigratesOnce` 报 `SQLITE_BUSY` |
| `go test -race -count=1 -run TestLegacyDatabaseConcurrentOpenMigratesOnce ./tests/integration/local/database/` | 通过 |
| `go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/` | 退出码 1：`httpapi` 的 `TestExecuteStepAPINotFound/步骤不属于任务` 期望 404 实际 409（任务租约被另一并行子用例持有）；`app` 与 `workflow` 通过 |
| `go test -race -count=1 ./tests/unit/app/`（单独执行） | 通过 |
| `go test -race -count=1 ./tests/unit/httpapi/`（单独执行） | 通过 |

三次失败都在"高负载 + `-race`"下出现，单独执行同一用例 / 同一包均通过，且失败点全部位于
本轮未改动的存储、迁移与 HTTP 层；因此判定为本机资源争用导致的既有偶发问题，不是本轮引入的
回归。**未把这些失败报告为通过。**

### 7.7 前端

`cd lingting-agent-flow-ui && pnpm test:all:safe` → 退出码 0：vitest（`--maxWorkers 4`）通过，
Playwright（`--workers=4`）257 个用例通过，`vp fmt` / `vp lint --fix` / `tsc --noEmit` 无待修复
改动，生产构建写入 `internal/webui/dist`。

## 8. 未覆盖项与限制

1. **没有 GitHub Actions 远端运行证据**：本轮未推送分支或标签，也没有创建 Release。
   workflow 的结构、引用与参数一致性通过本地静态核验，但"YAML 在 GitHub 上真正跑通"未被证明。
2. **`github-release.sh` 的远端写操作未执行**：`ensure-absent`、`create-draft`、`upload`、
   `attestations`、`publish`、`verify-published` 依赖真实标签、`gh` 认证与 Actions OIDC，
   本地无法模拟且不应伪造。它们目前只有静态核验（脚本存在、被 publish.yml 按顺序引用、
   参数一致）。
3. **`--download-from-tag` 分支未执行**：需要真实（草稿）Release 才能下载资产。
4. **Windows 跳过**：`TestAuthorizationScriptWithStubTool` 依赖 PATH 替身，在 MSYS 下不可靠，
   只跳过该用例，判定逻辑本身由 `tests/unit/cigate` 覆盖并在 §7.4 逐项实测。
5. **`shellcheck` / `actionlint` 未执行**：本机未安装，按任务要求不新增依赖；替代手段是
   `bash -n` + `gofmt` + `go vet` + `gopkg.in/yaml.v3` 解析 + 上述脚本契约用例。
6. **`make test` / 竞态检测的失败项**：见 §7.5/§7.6，均判定为环境与资源争用问题，
   未做"重跑直到通过"的掩盖；相关包的单独执行结果已如实列出。

## 9. 与 `P4-M5` 的关系

本轮是 CI/CD 结构调整，不涉及任务执行性能、调度并发、数据库访问路径等被测对象的实现，
因此不产出 `P4-M5` 需要的测量数据；`P4-M5` 仍为 `pending`。
