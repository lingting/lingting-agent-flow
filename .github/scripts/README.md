# GitHub Actions 脚本目录

本目录集中存放 CI/CD 专用的执行脚本。工作流 YAML 只负责声明与调度：

- 触发条件、Job、依赖、权限、矩阵与 Action 调用留在 `.github/workflows/*.yml`；
- 具体 Shell / Python 逻辑一律放在本目录；
- 每个脚本需要的参数都在调用它的 YAML 步骤里显式传入；
- 不在 YAML 的 `run:` 里保留多行程序、控制流、管道或 heredoc。

这里只放 CI/CD 执行逻辑，不放产品源码、应用运行脚本或用户运维命令。

## 参数传递约定

| 输入类型 | 传递方式 | 说明 |
| --- | --- | --- |
| 固定参数 | 命令行参数（`--artifact-dir`、`--version`） | 脚本自行解析、校验；缺失或非法以退出码 2 拒绝 |
| 动态值（GitHub Context、事件载荷、用户变量） | 步骤级 `env:`，脚本读取环境变量 | 不把 PR 标题、分支名等不可信文本拼进命令行 |
| 结论回传 | `GITHUB_OUTPUT` / `GITHUB_STEP_SUMMARY` | 变量名固定、取值经过校验；成功失败只由退出码表达 |
| Secret | 只在需要的 Job 用 `env:` 或受控文件传入 | 不作为命令行参数，不打印、不写入输出或制品 |

## 脚本清单

| 脚本 | 调用方 | 职责 | 参数 | 输出 | 退出码 |
| --- | --- | --- | --- | --- | --- |
| `check-go-static.sh` | `ci.yml` / `go-static` | `gofmt -l` 门禁、`go vet ./...`、`go build ./...` | 无 | 未格式化文件清单与失败注解 | 0 通过；1 格式 / 静态检查 / 编译失败；2 参数错误 |
| `go-test-groups.sh` | `ci.yml` / `go-inventory`、`run-go-group-tests.sh`、`run-real-tests.sh` | Go 测试分组清单的唯一来源（分组类型、Makefile 入口、包模式）与分组覆盖核验 | `--check [--declared-groups LIST]`、`--list normal\|real`、`--packages GROUP`、`--make-target GROUP`、`--describe` | 分组表格、`coverage` / `package_total` / `groups_markdown` 与 `GITHUB_STEP_SUMMARY` | 0 查询成功或核验通过；1 分组遗漏 / 重复 / 与工作流或 Makefile 不一致；2 参数错误 |
| `run-go-group-tests.sh` | `ci.yml` / `go-unit`、`go-local`、`go-e2e` | 执行一个普通测试分组（分组、Makefile 入口与包模式全部来自 `go-test-groups.sh`），拒绝真实测试分组 | `--group NAME`（必填）、`--artifact-dir DIR`（必填）、`--parallel N`（默认 4） | `DIR/<group>-output.log`、`DIR/<group>-summary.txt` 与 `GITHUB_STEP_SUMMARY`（耗时、包数量、用例结论） | 0 通过；1 测试失败或分组不是普通分组；2 参数错误 |
| `summarize-test-skips.sh` | `ci.yml` / 各 Go 测试组 | 汇总 `test.log` 中的 `WARN [SKIP]`，只做展示 | `--base DIR`、`--title TEXT` | `GITHUB_STEP_SUMMARY` 与标准输出 | 0 汇总完成；2 参数错误 |
| `run-go-race-tests.sh` | `ci.yml` / `go-race` | `make test-race` + 任务级工作流编排的 `-race` 检测，并行度显式传入 | `--artifact-dir DIR`（必填）、`--parallel N`（默认 4） | `DIR/race-output.log` | 0 通过；非零沿用失败命令的退出码；2 参数错误 |
| `collect-test-artifacts.sh` | `ci.yml` / 各 Go 测试组与 `real-tests` | 把白名单测试产物（控制台输出、`test.log`、`latest-run.txt`）收敛到暂存目录，供 `upload-artifact` 上传 | `--source-dir DIR`、`--job NAME`（必填）、`--staging-dir DIR`（可选） | `staging-dir` / `artifact-name` / `file-count` 与 `GITHUB_STEP_SUMMARY` | 0 收集完成（含无可收集文件）；1 暂存失败；2 参数错误 |
| `run-frontend-tests.sh` | `ci.yml` / `ui-test` | 执行 `pnpm test:all:safe`（vitest → Playwright → fmt / lint / 类型检查 → 构建） | `--artifact-dir DIR`（必填）、`--project DIR`（可选） | `DIR/ui-test-output.log` 与 Playwright 产物 | 0 通过；非零沿用 pnpm 的退出码；2 参数错误 |
| `check-frontend-clean.sh` | `ci.yml` / `ui-test` | 核验 `vp fmt` / `vp lint --fix` 之后工作区没有残留改动 | `--path REL`（默认 `lingting-agent-flow-ui`） | 改动清单与 diff | 0 干净；1 有残留改动；2 参数错误 |
| `check-real-test-authorization.sh` | `ci.yml` / `real-tests-gate` | 把事件上下文交给 `cmd/agentflow-ci real-tests-gate`，判定规则只在 `internal/cigate` | 无（全部输入来自 `env:`） | `real_tests_enabled` / `mode` / `reason` 与判定摘要 | 0 判定完成（含拒绝）；1 写输出失败；2 参数错误 |
| `prepare-real-tests-config.py` | `ci.yml` / `real-tests` | 由 Environment Secrets 生成 `tests/config/*.yml` 与临时私钥文件 | 无（读取约定环境变量） | `configured=true/false` 与摘要 | 0 完成（缺少 Secret 时 `configured=false`）；1 配置非法 |
| `run-real-tests.sh` | `ci.yml` / `real-tests` | 只跑真实外部依赖测试包（真实 Pi / SSH / 远程 Docker），未获授权时直接失败 | `--artifact-dir DIR`（必填）、`--parallel N`（默认 4） | `DIR/real-tests-output.log` | 0 通过；1 失败或未授权；2 参数错误 |
| `summarize-real-tests.sh` | `ci.yml` / `real-tests` | 授权模式、配置状态、测试结论与跳过清单摘要（只做展示） | `--skip-base DIR` | `GITHUB_STEP_SUMMARY` 与标准输出 | 0 汇总完成；2 参数错误 |
| `summarize-ci-gate.sh` | `ci.yml` / `ci-summary` | 依据 `needs.*.result` 与 `steps.*.outcome` 生成门禁结论 | `--report-path FILE`（可选） | 报告文件、控制台与 `GITHUB_STEP_SUMMARY` | 0 全部必需 Job 成功；1 存在非成功必需 Job 或真实测试失败；2 参数错误 |
| `resolve-release-version.sh` | `publish.yml` / `resolve`、`release.yml` / `resolve` | 校验版本标签与提交号，只在此处生成制品名 | `--require-tag`、`--input-version V` | `version` / `commit` / `artifact_name` 与摘要 | 0 解析成功；1 版本或提交非法；2 参数错误 |
| `build-release-dist.sh` | `release.yml` / `build` | 调用唯一发布入口 `make release-dist`，写入来源元数据 | `--version`、`--dir`、`--attestation-provider`、`--platforms`（可选） | 制品目录、`SHA256SUMS`、`release-manifest.json` | 0 成功；非零沿用 make 的退出码；2 参数或工具链缺失 |
| `verify-release-assets.sh` | `release.yml` / `build`、`publish.yml` / `publish` | `sha256sum -c` + `verify-release`（可加 `-require-provenance`），可选从远端下载并核对资产列表 | `--dir`、`--version`、`--commit`、`--require-provenance`、`--download-from-tag`、`--assets-file` | 核验结论 | 0 通过；1 核验失败；2 参数错误 |
| `release-artifacts-summary.py` | `release.yml` / `build` | 由发布清单生成制品摘要（名称、大小、SHA-256） | `<制品目录>` | 摘要到标准输出与 `GITHUB_STEP_SUMMARY` | 0 成功；1 清单不可读；2 参数错误 |
| `github-release.sh` | `publish.yml` / `publish` | Release 生命周期：拒绝覆盖、草稿、上传、来源证明核验、公开、复核 | 子命令 + `--tag` / `--dir` / `--title` / `--notes-file` / `--commit` | 状态输出；`verify-published` 写摘要 | 0 成功；1 操作失败；2 参数错误 |
| `summarize-release-stages.sh` | `publish.yml` / `publish-summary`、`release.yml` / `release-summary` | 汇总阶段结论，任一阶段不是 `success` 即失败 | `--title`、`--stage "名称=结论"`、`--version`、`--commit`、`--note`、`--report-path` | 报告文件、控制台与 `GITHUB_STEP_SUMMARY` | 0 全部成功；1 存在非成功阶段；2 参数错误 |

## 本地复核

脚本不依赖 Actions 运行时，可以在仓库根目录直接执行（缺少必需参数时会以退出码 2 拒绝）：

```bash
bash -n .github/scripts/*.sh                  # 全部 Shell 脚本语法检查
bash .github/scripts/check-go-static.sh       # 与 go-static Job 等价
bash .github/scripts/go-test-groups.sh --check --declared-groups unit,local,e2e   # 分组覆盖核验
bash .github/scripts/run-go-group-tests.sh --group unit --artifact-dir .test-artifacts/unit --parallel 4
bash .github/scripts/resolve-release-version.sh --require-tag   # 需要 REF_TYPE/REF_NAME/COMMIT_SHA
```

脚本行为**目前没有自动化契约测试**（原 `tests/unit/release/ci_scripts_test.go` 已随测试精简
删除）：解释器调用方式、`set -euo pipefail` 与文档头部、YAML 参数与脚本声明一致、LF 行尾，
以及各脚本的退出码都只能人工核对。分组覆盖仍由 `go-test-groups.sh --check` 在每次 CI 中机械核验。

## Go 测试分组与 CI 编排

普通 CI 的 Go 测试按职责拆成三个互不依赖的 Job，每个 Job 独享一台 Runner 并行执行。
分组归属、包模式与 Makefile 入口只定义在 `go-test-groups.sh`，工作流里只出现分组名：

| 分组 | 类型 | 包模式 | 本地等价入口 | CI Job |
| --- | --- | --- | --- | --- |
| `unit` | 普通 CI | `./tests/unit/...` | `make test-unit` | `go-unit` |
| `local` | 普通 CI | `./tests/integration/local/...` | `make test-local` | `go-local` |
| `e2e` | 普通 CI | `./tests/e2e/...` | `make test-e2e` | `go-e2e` |
| `pi` | 真实外部依赖 | `./tests/integration/pi/...` | 无（需要真实 Pi CLI） | `real-tests`（授权后） |
| `ssh` | 真实外部依赖 | `./tests/integration/ssh/...` | 无（需要真实主机） | `real-tests`（授权后） |
| `remote` | 真实外部依赖 | `./tests/integration/remote/...` | 无（需要真实远程 Docker） | `real-tests`（授权后） |

`tests/integration/local/` 下的 SSH、Docker、Agent 用例都是本机进程内模拟，属于普通 CI 的
`local` 分组；只有 `tests/integration/{pi,ssh,remote/...}` 需要真实外部设备。

分组覆盖由 `go-test-groups.sh --check` 机械核验，`go-inventory` Job 在跑测试之前执行它：

- 用 `go list` 取出仓库中真实存在测试文件的包，与分组模式做集合比较；
- 新增测试包没有分组归属、一个测试包落在多个分组、测试包总数为 0，一律失败；
- 工作流声明的普通分组（`--declared-groups`）必须与清单完全一致：分组已定义却没有 Job 执行
  同样算遗漏，把真实分组写进普通 CI 也会失败；
- 每个普通分组的 Makefile 入口必须出现同一组包模式，避免本地入口与 CI 分组漂移；
- `run-go-group-tests.sh` 与 `run-real-tests.sh` 不得硬编码 `./tests` 包路径，范围只能来自清单。

并行与资源上限：

- 三个普通分组 Job 之间没有 `needs` 依赖，由 GitHub Actions 并行调度；它们都依赖
  `go-inventory`，分组核验失败时测试 Job 直接跳过而不是跑一半；
- 每个分组 Job 独享一台 4 vCPU Runner，`go test` 只用 `-p 4` / `-parallel 4`，不在同一台
  机器上叠加多个高并发测试进程；
- `go-race` 同样显式传入 `--parallel 4`（`-race` 下 SQLite 迁移与连接建立对并发量敏感，
  不能用机器核数兜底）；`make test-race` 的并行度跟随 `PARALLEL`；
- 真实测试保持单 Job，并且包之间固定 `-p 1` 串行：所有真实套件共用同一台远程机器与同一个
  Docker Engine，并发执行时各套件会在"零残留核验"里把对方刚创建的一次性容器当成未登记残留。

缓存策略（集中在 `ci.yml` 的 `actions/setup-go` 步骤，不额外引入缓存系统）：

- `cache: true` 同时缓存 Go Module 下载缓存（`GOMODCACHE`）与编译 / 测试包缓存（`GOCACHE`），
  多个测试 Job 因此能复用同一份编译产物；
- 缓存键由 setup-go 生成，包含实际安装的 Go 版本、Runner 操作系统与镜像、架构，以及
  `go.mod` + `go.sum` 的内容哈希，键里没有每次都变化的随机值；
- 依赖变更或 Go 版本变更会自动失效；未命中时按普通构建继续，缓存损坏只会退化为重新编译；
- 缓存里只有依赖与编译产物，不含测试数据库、Session、测试工作空间、真实测试凭据或用户配置，
  也不在 Job 之间上传下载编译目录。

产物与耗时：

- 失败时由 `collect-test-artifacts.sh` 把白名单文件（`*-output.log`、各测试包 `test.log`、
  `latest-run.txt`）收敛到暂存目录再上传，制品名为 `<job>-<run_id>-<run_attempt>`；测试工作空间
  里的数据库、Session、临时私钥与依赖目录不上传；收集与上传两步都 `continue-on-error`，
  产物问题不会覆盖测试本身的失败结论；
- 每个分组把开始 / 结束时间、耗时、退出码、测试包数量、顶层用例通过 / 失败 / 跳过数量写进
  `GITHUB_STEP_SUMMARY` 与 `<group>-summary.txt`；`ci-summary` 直接展示 `go-inventory` 的分组表
  与各 Job 结论，不重新解析日志。

## 安全边界

- CI 内的真实测试授权只有 `AGENTFLOW_REAL_TESTS` 一个用户变量，规则只在 `internal/cigate` 实现；
  `check-real-test-authorization.sh` 只做参数转发，不重新实现身份判断。本机运行不读取该变量：
  没有 `GITHUB_ACTIONS` 标记时门禁直接放行，真实测试是否执行只由 `tests/config/*.yml` 的
  enable 开关决定。
- 授权判断 Job 检出的是 PR 的基础提交或本次运行的提交，判定不执行被审核 PR 的代码。
- 真实设备凭据只来自 `real-tests` 环境的 Environment Secrets，普通 CI Job 不读取 Secret；
  未获授权时 `run-real-tests.sh` 直接失败，不会把"一个用例都没跑"报告为成功。
- 发布流程不使用任何仓库 Secret；`github-release.sh` 不删除 Release、不覆盖已有资产、
  不打印令牌。
