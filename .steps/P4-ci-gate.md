# P4 工程质量：全栈 CI 测试门禁

- 日期：2026-10-30
- 基线：本地 checkout（`master`），未做 `git fetch`，不校准远端 SHA
- 结论：**工作流配置完成并通过静态校验与本地等价测试**；GitHub Actions 远端 Workflow Run
  尚未取得证据，不得表述为"CI 已通过"
- 范围限制：`P4-M5`（基于真实使用数据的性能优化）保持 `pending`，本轮不进行任何性能优化

## 1. 盘点结果：本轮开工前的 CI 现状

开工前 `.github/workflows/` 目录**不存在**，仓库没有任何自动化门禁。因此本轮是"从零建立"，
不存在与既有工作流重复执行的问题，也不需要判断哪些能力已被既有 Job 覆盖。

同时盘点到的相关事实（全部来自代码，不依赖推测）：

| 项 | 事实来源 | 结论 |
|---|---|---|
| Go 版本 | `go.mod`：`go 1.24.5` | 本地 `go version` 一致 |
| Node 版本 | `lingting-agent-flow-ui/package.json` 的 `volta.node`：`22.22.3` | `actions/setup-node` 的 `node-version-file` 原生支持 Volta 字段 |
| pnpm 版本 | 本地 `pnpm --version`：`11.24.0`；`pnpm-lock.yaml` 为 `lockfileVersion: '9.0'` | 用 `pnpm install --frozen-lockfile` |
| Playwright | `playwright.config.ts`：`chromium` 单 project，`webServer` 为 `vp dev` | 需要 `playwright install --with-deps chromium` |
| 前端并发 | `tests/internal/test-resource-policy.ts`：Vitest 4 / Playwright 4，上限 8 | 保持现状，不提高 |
| Go 测试入口 | `Makefile`：`test`、`test-race`、`test-packaging` | CI 只调用这些入口 |
| 打包冒烟前置 | `tests/e2e/packaging/packaging_test.go`：缺少 `pnpm` 或前端依赖时 `Skip` | `go-test` 必须先装前端依赖，否则打包冒烟会被静默跳过 |
| 测试配置 | `tests/config/*.yml` 被 `.gitignore` 排除，只提交 `example.*` | CI 天然没有真实 Pi / SSH / 远程 Docker 配置 |
| 本机配置现状 | 本地 `tests/config/ssh.yml`、`pi.yml`、`docker.yml` 全部 `enable: true` | 本地运行会真实访问外部资源，CI 不会 |
| 产物位置 | `tests/internal/testkit/testkit.go`：`os.TempDir()/lingting-agent-flow-tests`；前端 `tests/internal/artifacts.ts`：`os.tmpdir()` | 产物默认在临时目录，`upload-artifact` 取不到 |
| 格式门禁漏洞 | `Makefile` 的 `fmt-check` 只打印未格式化文件，退出码恒为 0 | 不能直接作为门禁 |
| 前端格式门禁漏洞 | `package.json` 的 `check` 是 `vp fmt && vp lint --fix && tsc --noEmit`，两者会自动改文件且不失败 | 单独核验工作区是否仍有待修复改动 |

## 2. 缺口清单（开工前 → 本轮处理）

| # | 缺口 | 影响 | 本轮处理 |
|---|---|---|---|
| G1 | 完全没有 CI 工作流 | 回归无人发现 | 新增 `.github/workflows/ci.yml` |
| G2 | `make fmt-check` 退出码恒为 0 | 格式问题不会被拦下 | `go-static` 显式检查 `gofmt -l` 输出非空即失败 |
| G3 | `vp fmt` / `vp lint --fix` 自动改文件且不失败 | 前端格式与 lint 实际不构成门禁 | `ui-test` 增加"核验无待修复改动"步骤 |
| G4 | 打包冒烟在缺少 pnpm 时静默跳过 | CI 会漏掉单文件发布链路 | `go-test` 先 `pnpm install --frozen-lockfile` |
| G5 | 测试产物写在 `os.TempDir()` | 失败后无法取回 `test.log` 与 Playwright 报告 | 仅测试步骤内把 `TMPDIR` 指向 `.test-artifacts/` |
| G6 | 测试工作空间含用例生成的临时私钥 | 整体上传会泄露密钥材料 | 只上传 `test.log` / `latest-run.txt` / Playwright 报告 / 控制台日志 |
| G7 | 无统一验收状态 | 单个 Job 失败可能被忽略 | `ci-summary` 以 `needs.*.result` + `steps.*.outcome` 计算 |
| G8 | 竞态检测范围不足 | 数据库迁移与工作流编排并发问题可能漏检 | `go-race` 在 `make test-race` 之上补两个范围 |
| G9 | 无缓存 | 每次重新下载 Go 模块与 pnpm store | `setup-go` 与 `setup-node` 按锁文件做缓存键 |
| G10 | 无并发取消 | 同一 PR 连推多次会重复占用 Runner | `concurrency` 仅对 `pull_request` 取消 |

## 3. 工作流结构

新增文件：`.github/workflows/ci.yml`（唯一工作流，未新增其他文件）。

### 3.1 触发与并发

```text
push -> master
pull_request -> master
workflow_dispatch
```

`concurrency.group = ci-${{ github.workflow }}-${{ github.ref }}`，
`cancel-in-progress = ${{ github.event_name == 'pull_request' }}`：PR 上旧运行被新提交取消，
`push` 与手动触发不取消，避免丢失主分支门禁证据。

`permissions: contents: read`：所有 Job 只读仓库，不使用 Secret，不写入仓库，不上传包。

### 3.2 Job 划分

| Job | 职责 | 超时 | 依赖 |
|---|---|---|---|
| `go-static` | 格式、`go vet ./...`、`go build ./...` | 15 分钟 | 无 |
| `go-test` | `make test`（`tests/...` 全量） | 60 分钟 | 无 |
| `go-race` | `make test-race` + 数据库迁移 + 任务级工作流编排 | 60 分钟 | 无 |
| `ui-test` | `pnpm test:all:safe` + 格式 / lint 待修复改动核验 | 45 分钟 | 无 |
| `ci-summary` | 统一验收状态 | 5 分钟 | 上述四个全部 |

前四个 Job 互不依赖，全部并行；没有人为串行化。`ci-summary` 是唯一汇聚点。

### 3.3 各 Job 的实际命令

`go-static`：

```bash
unformatted="$(gofmt -l cmd internal tests)"
[ -n "$unformatted" ] && exit 1     # 非空即失败
go vet ./...
go build ./...
```

`go-test`：`pnpm install --frozen-lockfile`（在 `lingting-agent-flow-ui/`）→ `make test`。

`go-race`：

```bash
make test-race
go test -race -count=1 ./tests/integration/local/database/
go test -race -count=1 ./tests/integration/local/workflow/
```

**关于去重决策**：需求列出的范围是 `make test-race`、`./tests/unit/source/codesaver/` +
`./tests/unit/store/`、`./tests/integration/local/database/`、
`./tests/unit/app/` + `./tests/unit/httpapi/` + `./tests/integration/local/workflow/`。
而 `make test-race` 定义是 `go test -race -count=1 ./tests/unit/...` 与
`./tests/integration/local/ssh/...`，已经包含前两组范围。因此按要求允许的合并方式去重后，
只补 `database` 与 `workflow` 两条。实际覆盖：

- `./tests/unit/...`（含 `unit/source/codesaver`、`unit/store`、`unit/app`、`unit/httpapi`）
- `./tests/integration/local/ssh/...`
- `./tests/integration/local/database/`
- `./tests/integration/local/workflow/`

是需求所列范围的**超集**（多出 `unit/agent`、`unit/domain`、`unit/container` 等全部单元包
与整个 SSH 连接层），没有减少任何要求覆盖的范围。

`ui-test`：`pnpm test:all:safe`，其内部顺序固定为

```text
vitest --maxWorkers 4
→ playwright test --workers=4
→ vp fmt && vp lint --fix && tsc --noEmit
→ node scripts/build-webui.mjs（生产构建）
```

worker 数完全取自 `tests/internal/test-resource-policy.ts` 的既有默认值，CI 未设置任何
`AGENTFLOW_TEST_VITEST_WORKERS` / `AGENTFLOW_TEST_PLAYWRIGHT_WORKERS`，也没有覆盖
`--maxWorkers` / `--workers` 参数，并发度保持 4。

### 3.4 统一验收状态

`ci-summary` 使用 `needs.*.result`（Job 级）与各 Job 导出的 `steps.*.outcome`（步骤级）：

| 输入组合 | 判定 |
|---|---|
| Job `success` | 成功 |
| Job `failure` + 测试步骤 `failure` | 失败：测试步骤失败 |
| Job `failure` + 准备步骤 `failure` | 失败：环境准备失败，测试未执行 |
| Job `failure` + 测试步骤 `skipped` 或空 | 失败：测试步骤未执行 |
| Job `failure` + 其他步骤失败 | 失败：测试之外的步骤失败 |
| Job `cancelled` | 未完成：运行被取消 |
| Job `skipped` | 未执行：Job 被跳过，不计为通过 |
| 结论为空（未知） | 未执行：缺少 Job 结论，不计为通过 |

只要存在任一非 `success`，步骤以 `::error::` 输出并以退出码 1 结束。**不使用日志字符串搜索
判定测试成败**——外部依赖的 `WARN [SKIP]` 只在 Job 摘要里说明情况，不参与结论计算。

## 4. 版本、缓存与依赖安全

| 项 | 取值 | 依据 |
|---|---|---|
| Go | `go-version-file: go.mod` | 与 `go.mod` 的 `go 1.24.5` 一致，不写死版本 |
| Node | `node-version-file: lingting-agent-flow-ui/package.json` | `setup-node` 原生读取 `volta.node` = `22.22.3` |
| pnpm | `pnpm/action-setup` `version: "11.24.0"` | 与本地 `pnpm --version` 一致 |
| 安装方式 | `pnpm install --frozen-lockfile` | 不执行非冻结安装，不改锁文件 |
| Go 缓存 | `setup-go` `cache-dependency-path: go.sum` | 命中只加速下载，不替代 `go build` / `go test` |
| pnpm 缓存 | `setup-node` `cache: pnpm` + `cache-dependency-path: lingting-agent-flow-ui/pnpm-lock.yaml` | 缓存键与锁文件绑定 |

Action 版本全部固定到**提交 SHA**（不是可移动 tag），并保留版本注释：

| Action | 版本 | 提交 SHA |
|---|---|---|
| `actions/checkout` | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `actions/setup-go` | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| `actions/setup-node` | v7.1.0 | `949feb2413d6458794dcd2491c4babbbce0c15c1` |
| `actions/upload-artifact` | v7.0.2 | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| `pnpm/action-setup` | v6.1.0 | `ea17c68df8912ef543352723c149a84f56e3d413`（解引用 annotated tag 得到的提交） |

未添加任何其他第三方 Action。`actions/checkout` 显式 `persist-credentials: false`，
不把 token 留在 `git config`。

## 5. 产物与脱敏边界

### 5.1 上传内容

| Job | 上传路径 | 条件 | 保留 |
|---|---|---|---|
| `go-test` | `.test-artifacts/go/make-test-output.log`、`.../lingting-agent-flow-tests/**/test.log`、`.../latest-run.txt` | `if: failure()` | 7 天 |
| `go-race` | `.test-artifacts/go-race/race-output.log`、`.../lingting-agent-flow-tests/**/test.log`、`.../latest-run.txt` | `if: failure()` | 7 天 |
| `ui-test` | `.test-artifacts/ui`（含 `ui-test-output.log`、Playwright HTML 报告、失败截图与 trace） | `if: failure()` | 7 天 |

产物名包含 `${{ github.run_id }}` 与 `${{ github.run_attempt }}`，重跑不覆盖。
所有上传步骤显式 `continue-on-error: true`，**上传失败不会改写原本的测试失败结论**。

### 5.2 不上传内容与脱敏核查

本地实测发现测试工作空间里存在用例生成的 SSH 私钥文件（`id_ed25519` / `id_test`，
位于 `ssh-*`、`cli-*`、`fetcher-*`、`httpserver-*`、`server-*` 等包目录下）。这是测试代码
自己生成的临时密钥，但**整体上传工作空间会把它们带走**，因此产物白名单只取
`test.log` / `latest-run.txt` / 控制台日志 / Playwright 报告目录。

脱敏核查（针对本轮实际运行产生的 43 个 Go 测试工作空间与 33 个前端工作空间）：

```bash
rg -l 'sk-magpie|BEGIN OPENSSH PRIVATE KEY|BEGIN RSA PRIVATE KEY' \
   --glob 'test.log' <本次运行的 TEST_ARTIFACT_DIR 列表>
rg -c 'password=|api_key=|Authorization: Bearer' \
   --glob 'test.log' <同上>
```

结论：所有 `test.log` 中均未命中真实 API Key、私钥内容、`password=` 明文或 Bearer 凭据。
（本地 `tests/config/docker.yml` 确实含真实 `api_key`，该文件被 `.gitignore` 排除、不会进入
CI；日志里也没有泄漏它。）

不执行 `pnpm install` 之外任何依赖变更，不自动更新依赖，不提交锁文件变化。

## 6. 工作流静态校验

| 校验项 | 方式 | 结果 |
|---|---|---|
| YAML 可解析 | 项目内已有的 `yaml@1.10.3`（`lingting-agent-flow-ui` 依赖树中已安装，不新增依赖）解析 `.github/workflows/ci.yml` | 通过 |
| 顶层结构 | 解析出 `jobs = go-static, go-test, go-race, ui-test, ci-summary`；`on` 含 `push` / `pull_request` / `workflow_dispatch`；`permissions = {contents: read}` | 通过 |
| 依赖环 | 对 `needs` 做 DFS 三色标记 | 无环，无缺失 Job |
| 超时 | 逐 Job 读取 `timeout-minutes` | 15 / 60 / 60 / 45 / 5 |
| 失败传播 | 逐 Job 检查无 `continue-on-error`（仅三个 upload 步骤显式开启） | 无 `continue-on-error` 吞掉测试失败 |
| 内嵌脚本语法 | 提取每个 `run` 块到临时目录后 `bash -n` | 全部通过 |
| 汇总状态机 | 用受控输入（成功 / 测试失败 / 准备失败 / 取消 / Job 跳过）驱动 `ci-summary` 的 `run` 块 | 5 类判定与退出码均符合预期 |
| 跳过原因汇总 | 用受控 `test.log` 驱动"汇总外部依赖跳过原因"步骤 | 正确聚合到 Job 摘要，退出码 0 |
| 格式核验步骤 | 在干净工作区执行 `ui-test` 的"核验无待修复改动" | 退出码 0（无待修复改动） |
| 凭据与权限 | 全文检索 `secrets` / `password` / `token` / `permissions` | 无 Secret 引用，权限最小化 |

未引入临时脚本到仓库：上述校验脚本全部写在临时目录，执行后不进入版本库。

## 7. 实际本地测试结果

以下结果来自本轮实际执行，命令与退出码如实记录。

### 7.1 Go 全量测试（对应 `make test`）

```bash
go test -count=1 -v -p 8 -parallel 8 ./tests/...
```

退出码 1。按包统计：

- 通过：`tests/e2e/cli`、`tests/e2e/packaging`、`tests/integration/local/*`（codesaver、
  database、docker、environment、execenv、fetcher、httpserver、scheduler、session、source、
  ssh、sshprobe、validation、workflow）、`tests/integration/pi`、`tests/integration/remote/docker`、
  `tests/integration/remote/workflow`、`tests/integration/ssh`、`tests/unit/*`（agent、app、
  config、container、docker、dockerfile、domain、environment、execenv、httpapi、image、
  lifecycle、process、server、source、source/codesaver、ssh、store、testkit、webui）
- 跳过：`TestRealPiModelRecognition`（`tests/integration/pi`）、
  `TestEmbeddedFSHasNoIndexInSourceCheckout`（`tests/unit/webui`）
- 失败：`tests/integration/remote/agent`、`tests/integration/remote/codesaver`

失败根因（日志原文）：两个远程包的**测试用例本身全部通过**，失败来自包级最终核验
`FINALIZER name=remote-container-cleanup-* status=FAIL`：

```text
测试容器零残留核验未通过: 出现未登记的测试容器 agentflow-test-5c08fcfbdf3ab715-run（ID 9e2ffbf36ffa），无法确认归属，未删除
```

这来自远程主机上**其他来源**（不属于本次运行登记）的残留测试容器：本次两个包的
`REMOTE_CONTAINER_EVIDENCE` 显示 `tracked=2`，两个自己创建的容器都以 `result=absent` 正常清理；
未登记的那个容器在 `REMOTE_CONTAINER_BASELINE` 中不存在、在 `FINAL` 中出现，说明它是**并行运行
的其他进程**（例如同时进行的远程 workflow 测试）创建的，被本包的零残留核验当成"未登记残留"。

这是本地并行执行 `./tests/...` 时多个远程测试包同时操作同一台远程 Docker 造成的**测试编排
冲突**，不是产品缺陷，也不是 CI 会遇到的形态（CI 不启用远程 Docker）。本轮未修改该机制：
它按设计"无法确认归属就不删除"，是资源治理的正确行为。

### 7.2 Go 竞态检测

| 命令 | 退出码 | 结果 |
|---|---|---|
| `go test -race -count=1 ./tests/unit/... ./tests/integration/local/ssh/...` | 1 | 除 `tests/unit/store` 外全部通过 |
| `go test -race -count=1 ./tests/integration/local/database/` | 0 | 通过 |
| `go test -race -count=1 ./tests/unit/app/ ./tests/unit/httpapi/ ./tests/integration/local/workflow/` | 0 | 通过（app 与 httpapi 单独重跑亦通过） |
| `go test -race -count=1 ./tests/unit/store/`（单独包） | 0 | 通过，167.8s |
| `go test -race -count=1 -run TestMigrateUpgradesExistingDatabase ./tests/unit/store/` | 0 | 通过，6.5s |
| `go test -race -count=1 -parallel 4 ./tests/unit/store/` | 0 | 通过，147.8s |
| `go test -race -count=1 -p 4 ./tests/unit/... ./tests/integration/local/ssh/...` | 1 | 仅 `TestMigrateUpgradesExistingDatabase` 失败 |

失败原文：

```text
migrate_test.go:213: 读取 schema 版本失败: sqlite: 读取迁移版本失败: context deadline exceeded
--- FAIL: TestMigrateUpgradesExistingDatabase (45.13s)
```

分析（区分环境容量与产品缺陷）：

- 该用例的上下文超时是固定的 30 秒（`tests/unit/store/fixtures_test.go` 的 `testContext`），
  实测耗时 45.13 秒 —— 已经超过超时，说明不是"代码把结果算错"，而是**用例本身耗时随机器负载
  增长而突破固定上限**。
- `tests/unit/store` 有大量 `t.Parallel()` 用例，每包内并行度默认等于 `GOMAXPROCS`（本机 18）；
  加上 `./tests/unit/...` 同时有 20+ 个测试包在跑（其中 `unit/app`、`unit/httpapi` 在 race 下
  分别耗时 229s、144s），store 包的用例被挤到远超单跑的耗时。
- 单独跑该包（无论默认并行度、`-parallel 4`，还是 `-p 4` 混合包场景外的单包运行）都稳定通过，
  单独跑该用例仅 6.5s。**这是并发压力下的超时放大，不是数据竞争**，日志中也没有出现
  `WARNING: DATA RACE`。
- 本轮**没有修改该用例的超时值**：调大超时属于改动测试契约，不在本任务范围（任务要求"修复
  CI 环境与项目测试脚本之间的兼容问题"，而这里是测试用例自身的超时预算与本机核数×并行度的
  关系，属于需要单独决策的测试资源治理项）。

CI 侧的实际影响：`go-race` 使用 GitHub 标准 4 vCPU Runner，`GOMAXPROCS=4`，
`./tests/unit/...` 的包级并发与包内并行度都远低于本机 18 核，该失败形态在 CI 上不会以同样
幅度出现；但**本轮没有 CI 实测证据**，因此不能声称"CI 上不会失败"，只记录本机真实结果与
差异原因。

### 7.3 前端

```bash
cd lingting-agent-flow-ui && pnpm test:all:safe
```

退出码 0。逐项结果：

- Vitest：`Test Files 28 passed (28)`、`Tests 635 passed (635)`
- Playwright：`Running 257 tests using 4 workers` → `257 passed`，`PLAYWRIGHT_WORKERS=4`
- `vp fmt && vp lint --fix && tsc --noEmit`：`Finished in 937ms on 145 files`
- 生产构建：`internal/webui/dist` 产出 `index.html`、`assets/index-*.css`、`assets/index-*.js`

执行后 `git status --short` 为空，说明格式化与 lint 未留下待修复改动 —— 这正是 `ui-test`
"核验无待修复改动"步骤要守住的形态。

### 7.4 非测试检查

| 命令 | 结果 |
|---|---|
| `gofmt -l cmd internal tests` | 无输出（全部已格式化） |
| `go vet ./...` | 退出码 0 |

## 8. 本轮未取得 / 未执行的内容

必须明确区分，不得混写：

| 项 | 状态 |
|---|---|
| GitHub Actions 远端 Workflow Run | **未取得**。本轮未推送、未触发远端运行，因此没有 Run ID、没有 Job 日志、没有 Artifact。不能声称"GitHub Actions 已经通过" |
| CI Runner 上的 `go-test` / `go-race` / `ui-test` 实测 | **未执行**。本地等价命令已执行，但 Runner 的核数、网络与镜像差异未经实测 |
| `TestMigrateUpgradesExistingDatabase` 在 race + 高并发下的通过性 | **本机失败**，单独运行通过。未在 CI 形态下实测 |
| `tests/integration/remote/agent`、`remote/codesaver` 的包级零残留核验 | **本机失败**，原因是并行运行的其他远程测试包留下的未登记容器；CI 不启用远程 Docker，不受影响 |
| 分支保护 / 规则集 / 组织权限 | **未修改**（任务明确禁止） |
| `P4-M5` 性能优化 | 保持 `pending`，本轮未做任何优化 |

## 9. 文档同步

| 文件 | 变更 |
|---|---|
| `README.md` | "开发与测试命令"下新增「持续集成（CI）」小节：Job 表、本地等价入口、跳过与产物边界、远端证据缺失声明 |
| `tests/README.md` | 新增「CI 门禁与本地入口的对应关系」：Job 与本地命令对照、`go-race` 去重依据、跳过规则在 CI 中的表现、产物留存策略 |
| `.steps/README.md` | 文档索引新增本文件条目 |
| `.steps/P4-ci-gate.md` | 本文件 |

## 10. 剩余风险与后续候选

1. **远端证据缺失**：需要在真实 PR 上触发一次 CI 并核对 4 个 Job 的实际耗时与结论；
   `go-test` 的 60 分钟上限与 `go-race` 的 60 分钟上限都是按本机耗时的保守估计，需实测校准。
2. **远程测试包的并行编排冲突**：`remote/agent`、`remote/codesaver`、`remote/workflow`、
   `remote/docker` 同时运行时会互把对方的容器当成"未登记残留"。若后续希望远程测试也能在
   CI 中运行，需要先解决这个跨包共享远程 Docker 的编排问题（例如串行化远程包，或让零残留
   核验按运行标识隔离）。本轮未修改该机制。
3. **`tests/unit/store` 的超时预算**：固定 30 秒上下文在高并发 + race 下会被突破。若要支持
   高核数本地全量 race 运行，需要单独评估该用例的超时预算或并行度，而不是简单调大数值。
4. **Playwright 浏览器版本**：CI 用 `playwright install --with-deps chromium` 下载与
   `@playwright/test` 匹配的版本；本机已安装的浏览器版本未核对，理论上存在版本差异带来的
   渲染差异风险。
5. **`actions/*` 版本维护**：SHA 固定带来可重现性，也带来升级成本。升级时必须同时核对
   action 的 `action.yml` 输入是否仍支持当前用法（本轮已核对 `node-version-file` 对 Volta
   字段的支持、`pnpm/action-setup` 的 `version` 输入）。
