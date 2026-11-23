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
| 固定参数 | 命令行参数（`--kind`、`--artifact-dir`） | 脚本自行解析、校验；缺失或非法以退出码 2 拒绝 |
| 动态值（GitHub Context、事件载荷、用户变量） | 步骤级 `env:`，脚本读取环境变量 | 不把 PR 标题、分支名等不可信文本拼进命令行 |
| 结论回传 | `GITHUB_OUTPUT` / `GITHUB_STEP_SUMMARY` | 变量名固定、取值经过校验；成功失败只由退出码表达 |
| Secret | 只在需要的 Job 用 `env:` 或受控文件传入 | 不作为命令行参数，不打印、不写入输出或制品 |

## 脚本清单

| 脚本 | 调用方 | 职责 | 参数 | 输出 | 退出码 |
| --- | --- | --- | --- | --- | --- |
| `check-go-static.sh` | `ci.yml` / `go-static` | `gofmt -l` 门禁、`go vet ./...`、`go build ./...` | 无 | 未格式化文件清单与失败注解 | 0 通过；1 格式 / 静态检查 / 编译失败；2 参数错误 |
| `go-test-groups.sh` | `ci.yml` / `go-inventory`、`run-cases.sh` | 分组声明的唯一来源：Go 测试分组（R9 之后为空，`_test.go` 一律算失败）+ 42 个 Case 分组的 kind，并用真实清单核验 | `--check [--declared-groups LIST] [--declared-case-kinds LIST]`、`--list-groups`、`--packages GROUP`、`--make-target GROUP`、`--case-selectors KIND`、`--describe` | 分组表格与核验结论；`GITHUB_OUTPUT`（`package_total` / `case_total` / `groups_markdown`） | 0 查询成功或核验通过；1 残留测试包 / 分组遗漏 / 重复 / 落空声明 / 与工作流、Makefile、注册表不一致；2 参数错误 |
| `run-cases.sh` | `ci.yml` / `cases-unit`、`cases-local`、`cases-e2e`、`cases-selfcheck`、`ui-test`、`real-tests`；`Makefile` 的 `test-cases` / `test-real` | Case 执行的唯一入口：按 kind 展开选择条件、构建执行器、运行并渲染结论；`kind=real` 缺授权时直接失败 | `--kind normal\|local\|e2e\|selfcheck\|frontend\|unit\|real`（必填）、`--artifact-dir DIR`（必填）、`--parallel N`（默认 4）、`--timeout D`、`--bin PATH` | `DIR/<run-id>/report.json`、`DIR/cases-<kind>-output.log`、`DIR/cases-<kind>-summary.txt` 与 `GITHUB_STEP_SUMMARY` | 0 选定 Case 全部通过且零残留未失败；1 失败 / 零残留失败 / 无报告 / `real` 未授权；2 参数错误 |
| `check-real-test-authorization.sh` | `ci.yml` / `real-tests-gate` | 把事件上下文交给 `cmd/agentflow-ci real-tests-gate`，判定规则只在 `internal/cigate` | 无（全部输入来自 `env:`） | `real_tests_enabled` / `mode` / `reason` 与判定摘要 | 0 判定完成（含拒绝）；1 写输出失败；2 参数错误 |
| `summarize-ci-gate.sh` | `ci.yml` / `ci-summary` | 依据 `needs.*.result` 与 `steps.*.outcome` 生成门禁结论，不搜索日志文本 | `--report-path FILE`（可选） | 报告文件、控制台与 `GITHUB_STEP_SUMMARY` | 0 全部必需 Job 成功；1 存在非成功必需 Job 或真实测试失败；2 参数错误 |
| `check-frontend-clean.sh` | `ci.yml` / `ui-test` | 核验 `vp fmt` / `vp lint --fix` 之后工作区没有残留改动 | `--path REL`（默认 `lingting-agent-flow-ui`） | 改动清单与 diff | 0 干净；1 有残留改动；2 参数错误 |
| `resolve-release-version.sh` | `publish.yml` / `resolve`、`release.yml` / `resolve` | 校验版本标签与提交号，只在此处生成制品名 | `--require-tag`、`--input-version V` | `version` / `commit` / `artifact_name` 与摘要 | 0 解析成功；1 版本或提交非法；2 参数错误 |
| `build-release-dist.sh` | `release.yml` / `build` | 调用唯一发布入口 `make release-dist`，写入来源元数据 | `--version`、`--dir`、`--attestation-provider`、`--platforms`（可选） | 制品目录、`SHA256SUMS`、`release-manifest.json` | 0 成功；非零沿用 make 的退出码；2 参数或工具链缺失 |
| `verify-release-assets.sh` | `release.yml` / `build`、`publish.yml` / `publish` | `sha256sum -c` + `verify-release`（可加 `-require-provenance`），可选从远端下载并核对资产列表 | `--dir`、`--version`、`--commit`、`--require-provenance`、`--download-from-tag`、`--assets-file` | 核验结论 | 0 通过；1 核验失败；2 参数错误 |
| `release-artifacts-summary.py` | `release.yml` / `build` | 由发布清单生成制品摘要（名称、大小、SHA-256） | `<制品目录>` | 摘要到标准输出与 `GITHUB_STEP_SUMMARY` | 0 成功；1 清单不可读；2 参数错误 |
| `github-release.sh` | `publish.yml` / `publish` | Release 生命周期：拒绝覆盖、草稿、上传、来源证明核验、公开、复核 | 子命令 + `--tag` / `--dir` / `--title` / `--notes-file` / `--commit` | 状态输出；`verify-published` 写摘要 | 0 成功；1 操作失败；2 参数错误 |
| `summarize-release-stages.sh` | `publish.yml` / `publish-summary`、`release.yml` / `release-summary` | 汇总阶段结论，任一阶段不是 `success` 即失败 | `--title`、`--stage "名称=结论"`、`--version`、`--commit`、`--note`、`--report-path` | 报告文件、控制台与 `GITHUB_STEP_SUMMARY` | 0 全部成功；1 存在非成功阶段；2 参数错误 |

R8 删除的脚本：`run-real-tests.sh`（改由 `run-cases.sh --kind real` 承担）、`run-go-race-tests.sh`
（race 显式放弃）、`run-frontend-tests.sh`（改由 `run-cases.sh --kind frontend`）、
`summarize-real-tests.sh`（授权与跳过只读 `report.json`）、`summarize-test-skips.sh`（不再解析日志文本）、
`collect-test-artifacts.sh`（产物目录本身可上传）、`prepare-real-tests-config.py`（职责收回执行器的
`--prepare-real-config`）。

R9 删除的脚本：`run-go-group-tests.sh`（`go test` 执行路径随 `make test-unit` 与 `go-unit` Job 一并删除，
unit 面全部由 `run-cases.sh --kind unit` 执行）。

## 本地复核

脚本不依赖 Actions 运行时，可以在仓库根目录直接执行（缺少必需参数时会以退出码 2 拒绝）：

```bash
bash -n .github/scripts/*.sh                  # 全部 Shell 脚本语法检查
bash .github/scripts/check-go-static.sh       # 与 go-static Job 等价
bash .github/scripts/go-test-groups.sh --check \
  --declared-case-kinds local,e2e,selfcheck,frontend,unit   # 分组覆盖核验
bash .github/scripts/run-cases.sh --kind local --artifact-dir .test-artifacts/local --parallel 4
bash .github/scripts/run-cases.sh --kind real --artifact-dir .test-artifacts/real --parallel 1   # 缺授权即失败
bash .github/scripts/resolve-release-version.sh --require-tag   # 需要 REF_TYPE/REF_NAME/COMMIT_SHA
```

脚本行为**目前没有自动化契约测试**（原 `tests/unit/release/ci_scripts_test.go` 已随测试精简删除）：
解释器调用方式、`set -euo pipefail` 与文档头部、YAML 参数与脚本声明一致、LF 行尾，以及各脚本的退出码
都只能人工核对。分组覆盖仍由 `go-test-groups.sh --check` 在每次 CI 中机械核验。

## 分组与 CI 编排

`go-test-groups.sh` 是分组声明的唯一来源，工作流里只出现分组名与 kind：

| 分组 | 类型 | 执行入口 | 本地等价命令 |
| --- | --- | --- | --- |
| `unit`（17 组） | 普通 Case | `run-cases.sh --kind unit` | `make test-cases TEST_KIND=unit` |
| `local`（9 组）、`e2e`（1 组）、`selfcheck`（4 组）、`frontend`（1 组） | 普通 Case | `run-cases.sh --kind ...` | `make test-cases TEST_KIND=...` |
| `real`（6 组） | 真实外部依赖（需授权） | `run-cases.sh --kind real` | `AGENTFLOW_REAL_TESTS_GRANTED=true make test-real` |
| `fault`（4 组） | 故障注入（按设计非零退出） | 不进工作流 | `./bin/agentflow-test selfcheck-fault` 等 |

`go-test-groups.sh --check` 在跑测试之前机械核验：

- 仓库里不允许再有 go test 测试包：`go list ./...` 出现任何带 `_test.go` 的包即失败；
- 执行器 `--list` 的 42 个 Case 分组必须各命中一条 kind 声明，声明不得落空；
- 工作流不得再声明 go test 分组，声明的普通 kind 必须与清单完全一致；
- `Makefile` 不得调用 `go test`、不得定义 `test-unit`，`test-cases` 与 `test-real` 的配方必须调用
  `run-cases.sh`、不得硬编码分组名，一并核验。

并行与资源上限：

- 每个 Job 独享一台 4 vCPU Runner，执行器只用 `--parallel 4`；
- `cases-unit`、`cases-local`、`cases-e2e`、`cases-selfcheck`、`ui-test` 之间没有依赖，由 GitHub Actions 并行调度，
  它们都依赖 `go-inventory`，分组核验失败时不会跑一半测试；
- 真实分组固定 `--parallel 1`：全部真实套件共用同一台远程机器与同一个 Docker Engine；
- 共用同一台设备的真实分组在包内保持串行（`remote/docker`），前端 Playwright 固定 `workers: 1`：
  并发只由执行器调度器决定。

缓存策略（集中在 `ci.yml` 的 `actions/setup-go` 步骤，不额外引入缓存系统）：

- `cache: true` 同时缓存 Go Module 下载缓存（`GOMODCACHE`）与编译 / 测试包缓存（`GOCACHE`）；
- 缓存键由 setup-go 生成，包含 Go 版本、Runner 镜像、架构以及 `go.mod` + `go.sum` 的内容哈希；
- 缓存里只有依赖与编译产物，不含测试数据库、Session、测试工作空间、真实测试凭据或用户配置。

产物与耗时：

- Case Job 只上传自己的 `<artifact-root>/<job>/<run-id>/`（`report.json`、worker 日志、
  `cases/<case>/`）；上传步骤都 `continue-on-error`，产物问题不会覆盖测试结论；
- 测试工作空间落在 `TMPDIR`，
  数据库、Session 与临时私钥因此不进制品；
- 跳过统计只读 `report.json` 的 `totals.skipped` 与 `skip_reason`（`run-cases.sh` 渲染），不解析日志文本；
- `ci-summary` 直接展示 `go-inventory` 的分组表与各 Job 结论，不重新解析日志。

## 安全边界

- CI 内的真实测试授权只有 `AGENTFLOW_REAL_TESTS` 一个用户变量，规则只在 `internal/cigate` 实现；
  `check-real-test-authorization.sh` 只做参数转发，不重新实现身份判断。本机运行不读取该变量：
  没有 `GITHUB_ACTIONS` 标记时门禁直接放行，真实测试是否执行只由 `tests/config/*.yml` 的 enable
  开关决定。
- 授权判断 Job 检出的是 PR 的基础提交或本次运行的提交，判定不执行被审核 PR 的代码。
- 真实设备凭据只来自 `real-tests` 环境的 Environment Secrets，由
  `go run ./cmd/agentflow-test --prepare-real-config` 现场生成被 `.gitignore` 忽略的 `tests/config/*.yml`
  与 `RUNNER_TEMP` 下的私钥 / known_hosts（0600）；普通 CI Job 不读取 Secret。
- 未获授权时 `run-cases.sh --kind real` 直接失败，不会把"一个用例都没跑"报告为成功；缺少必需 Secret 时
  配置步骤输出 `configured=false`，工作流把用例步骤标为跳过并在摘要里标注不计为通过。
- 发布流程不使用任何仓库 Secret；`github-release.sh` 不删除 Release、不覆盖已有资产、不打印令牌。
