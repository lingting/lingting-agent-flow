# V4 R4：目录收敛（`tests/testkit` 与 `tests/*`）

**状态：** 已完成（2026-11-23）。`tests/testkit` 与 `tests/*` 目录收敛、导入路径与文档同步均已完成，
验收记录见 §8。
**上游文档：** [V4 路线总规划](../ROADMAP.md)、[R0 冻结决策 D-01](../R0/README.md)、[R3 worker 池](../R3/README.md)。
**下游关系：** 纯移动，不改用例语义；unit 迁移试点在 [R5](../R5/README.md)。

---

## 1. 目标

`tests/` 只剩一层用例目录与一个共享辅助目录：

```text
tests/
├── config/            真实外部依赖配置（示例入库）
├── testkit/           <- tests/internal/testkit（共享辅助，不含 Main）
├── registry/          <- tests/runner/registry（空导入聚合）
├── concurrencycheck/  <- tests/runner/concurrencycheck
├── e2e/               <- tests/runner/e2e
├── frontend/          <- tests/runner/frontend
├── integration/       <- tests/runner/integration
├── pilot/             <- tests/runner/pilot
├── resourcecheck/     <- tests/runner/resourcecheck
├── selfcheck/         <- tests/runner/selfcheck
└── unit/              unit 用例来源包（目录保留，文件仍由 go test 执行直到 R9）
```

`tests/runner/**` 与 `tests/internal/**` 必须整体消失，不得留下转发包。

## 2. 同步点清单

`tests/internal/testkit` → `tests/testkit`：

| 同步点 | 位置 |
|---|---|
| Go 导入路径 | 121 个文件的 `github.com/lingting/agentflow/tests/internal/testkit`（`git grep -l` 枚举） |
| 文档 | `docs/testing.md`、`docs/workspace.md`、`tests/README.md` |
| 包注释 | `testkit` 包内引用自身路径的注释 |

`tests/runner/*` → `tests/*`：

| 同步点 | 位置 |
|---|---|
| Go 导入路径 | `tests/runner/registry/registry.go` 的 21 行空导入、`cmd/agentflow-test/main.go` 的 registry 空导入 |
| `Case.Package` 值 | **不改**：保留来源路径（`tests/integration/...`、`tests/e2e/...`），服务串行作用域与报告定位 |
| 注册分组名 | 不改：`group` 取 `ID` 第一段，与目录无关 |
| 用例内相对路径 | `tests/runner/**` 内按相对路径读取的文件（真实 remote 用例的包内 `Dockerfile`、`testkit` 远程目录常量）必须逐个核对 |
| 文档 | `docs/workspace.md`、`docs/testing.md`、`tests/README.md`、`.github/scripts/README.md` |

## 3. 强制规则

- 用 `git mv` 而不是"复制 + 删除"，保证 Git 能识别重命名。
- 一次提交只搬一个顶层目录（`testkit`、`registry`、`concurrencycheck`、…），每个提交都必须 `go build ./...` 通过。
- 搬完后 `tests/runner`、`tests/internal` 为空目录并删除。

## 4. 验收探针

| 探针 | 期望 |
|---|---|
| `go build ./...`、`go vet ./...`、`gofmt -l` | 干净 |
| `./bin/agentflow-test --list` | 403 条 / 25 个分组（与 R3 §9.4 基线一致；R0 的 360 是 R2/R3 新增自检之前的数字） |
| `git grep -n 'tests/runner\|tests/internal' -- ':!*.md'` | 只剩 `lingting-agent-flow-ui/tests/internal/`（前端工程自己的 Playwright 产物辅助，不属于根 `tests/` 树） |
| 目录 | `tests/runner`、`tests/internal` 不存在 |
| 文档 | 目录树与新结构一致 |

## 5. 授权

只读核对为主；一次 `--list` 不算执行用例，可直接跑。不运行任何用例。

## 6. 风险

| 风险 | 处置 |
|---|---|
| 用例内相对路径失效（只有真实分组才暴露） | 改完后 `grep` 所有相对路径，并在 R4 之后第一个跑真实分组的阶段复核 |
| 移动与语义改写混在同一次提交 | 提交纪律：R4 只允许重命名与导入路径变化 |
| 空导入遗漏导致用例不注册 | `--list` 的 403/25 是硬门禁 |

## 7. 不做的事

不迁移 unit 用例、不改 `Case.Package` 值、不改注册分组名、不改 CI 结构、不改 worker 模型。

## 8. 验收记录（2026-11-23）

### 8.1 落点

| 目录 / 文件 | 改动 |
|---|---|
| `tests/internal/testkit` → `tests/testkit` | 整体 `git mv`；全部导入路径、包注释与文档引用改新路径 |
| `tests/testkit/config.go` | `repoRootPath` 的 `filepath.Dir` 层数 4 → 3：路径少一层后仍解析到仓库根，`tests/config` 定位不依赖工作目录 |
| `tests/runner/{registry,concurrencycheck,e2e,frontend,integration,pilot,resourcecheck,selfcheck}` → `tests/*` | 八个顶层目录各自 `git mv`；`tests/registry/registry.go` 的空导入与 `cmd/agentflow-test/main.go` 的 registry 导入改新路径 |
| `tests/runner/concurrencycheck/quota.go` | 自检用例的合成 `Package`（`quota` / `quota-conflict` / `quota-override`）改为 `tests/concurrencycheck/*`，与目录归一 |
| `tests/integration/remote/planning/Dockerfile` | 文件头注释里的构建命令改为新路径 |
| 用例内相对路径 | `remote/agent` 与 `remote/planning` 的 `dockerfilePath` 按编译期源码路径取同目录 `Dockerfile`，随文件一起搬；`pilot`、`frontend`、`remote/planning` 的仓库根定位从工作目录向上查找，与包深度无关 |
| 文档 | `docs/testing.md`、`docs/workspace.md`、`docs/docker-compatibility.md`、`tests/README.md` 的目录树与路径引用；`tests/README.md` 的 `--list` 规模按实测订正 |

### 8.2 实现决定

- **`Case.Package` 的来源路径不动**：`tests/integration/...`、`tests/e2e/...` 保持原值，串行表为空，
  报告定位不变；只有本来就不指向任何真实目录的合成包名跟着目录改名。
- **不留转发包**：`tests/runner` 与 `tests/internal` 直接删除，没有为旧导入路径保留任何包。
- **`testkit.Main` 保留**：它属于 R9.1 的删除清单，R4 只搬目录；`tests/unit/**` 的 `main_test.go`
  继续调用它。
- **注册分组名不动**：`group` 取 `ID` 第一段，与目录无关，`--list` 的分组集合与搬迁前一致。
- **文档目录树按终态顺序列**：`testkit`、`unit`、`registry`、`integration/*`、`e2e`、`frontend`、
  `selfcheck`、`resourcecheck`、`concurrencycheck`、`pilot`、`config`，与 ROADMAP §4.1 一致。

### 8.3 探针结果

| 探针 | 结果 |
|---|---|
| `make check`（fmt-check / vet / build） | 通过 |
| `go build ./...`、`go vet ./...`、`gofmt -l cmd internal tests` | 干净 |
| `./bin/agentflow-test --list` | 403 条 / 25 个分组，与搬迁前一致 |
| `.github/scripts/go-test-groups.sh --check --declared-groups unit --declared-case-kinds local,e2e,selfcheck,frontend` | 17 个 go test 测试包与 25 个 Case 分组全部分类，无遗漏、无重复归属 |
| 旧路径引用 | 根 `tests/` 树与 `cmd/`、`internal/`、`docs/`、`tests/README.md` 中已清零 |
| 目录 | `tests/runner`、`tests/internal` 不存在 |

按 §5 授权未运行任何用例。

### 8.4 口径与遗留

- R5–R8 登记的 `--list` 目标（498 / 814 / 1712）是在 360 基线上算出的，与 R3 §9.4 同一问题，收口时
  按实测顺延。
- `real`、`resourcecheck`、`pilot` 分组本阶段未运行（§5 授权为"不运行用例"）；R-08 的"第一个跑真实
  分组的阶段复核相对路径"顺延到 R5 试点。
- 一次完整 CI 复核仍需推送授权后执行。
