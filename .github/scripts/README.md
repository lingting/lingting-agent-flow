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
| `run-go-tests.sh` | `ci.yml` / `go-test` | 执行 `make test`（`tests/` 全量），把 `TMPDIR` 指向产物目录 | `--artifact-dir DIR`（必填）、`--parallel N`（默认 8） | `DIR/make-test-output.log` 与 testkit 测试工作空间 | 0 通过；非零沿用 make 的退出码；2 参数错误 |
| `summarize-test-skips.sh` | `ci.yml` / `go-test` | 汇总 `test.log` 中的 `WARN [SKIP]`，只做展示 | `--base DIR`、`--title TEXT` | `GITHUB_STEP_SUMMARY` 与标准输出 | 0 汇总完成；2 参数错误 |
| `run-go-race-tests.sh` | `ci.yml` / `go-race` | `make test-race` + 数据库迁移 + 任务级工作流编排的 `-race` 检测 | `--artifact-dir DIR`（必填） | `DIR/race-output.log` | 0 通过；非零沿用失败命令的退出码；2 参数错误 |
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
bash .github/scripts/run-go-tests.sh --artifact-dir .test-artifacts/go
bash .github/scripts/resolve-release-version.sh --require-tag   # 需要 REF_TYPE/REF_NAME/COMMIT_SHA
```

行为契约由 `tests/unit/release/ci_scripts_test.go` 覆盖：脚本存在性与解释器调用方式、
`set -euo pipefail` 与文档头部、YAML 参数与脚本声明一致、YAML 无内嵌程序、LF 行尾，以及
版本解析、结论汇总、门禁汇总与真实测试授权守卫的退出码。

## 安全边界

- 真实测试授权只有 `AGENTFLOW_REAL_TESTS` 一个用户变量，规则只在 `internal/cigate` 实现；
  `check-real-test-authorization.sh` 只做参数转发，不重新实现身份判断。
- 授权判断 Job 检出的是 PR 的基础提交或本次运行的提交，判定不执行被审核 PR 的代码。
- 真实设备凭据只来自 `real-tests` 环境的 Environment Secrets，普通 CI Job 不读取 Secret；
  未获授权时 `run-real-tests.sh` 直接失败，不会把"一个用例都没跑"报告为成功。
- 发布流程不使用任何仓库 Secret；`github-release.sh` 不删除 Release、不覆盖已有资产、
  不打印令牌。
