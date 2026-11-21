# V4 R4：目录收敛（`tests/testkit` 与 `tests/*`）

**状态：** 未开始。
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
| `./bin/agentflow-test --list` | 360 条 / 25 个分组（与 R0 基线一致） |
| `git grep -n 'tests/runner\|tests/internal' -- ':!*.md'` | 无输出 |
| 目录 | `tests/runner`、`tests/internal` 不存在 |
| 文档 | 目录树与新结构一致 |

## 5. 授权

只读核对为主；一次 `--list` 不算执行用例，可直接跑。不运行任何用例。

## 6. 风险

| 风险 | 处置 |
|---|---|
| 用例内相对路径失效（只有真实分组才暴露） | 改完后 `grep` 所有相对路径，并在 R4 之后第一个跑真实分组的阶段复核 |
| 移动与语义改写混在同一次提交 | 提交纪律：R4 只允许重命名与导入路径变化 |
| 空导入遗漏导致用例不注册 | `--list` 的 360/25 是硬门禁 |

## 7. 不做的事

不迁移 unit 用例、不改 `Case.Package` 值、不改注册分组名、不改 CI 结构、不改 worker 模型。
