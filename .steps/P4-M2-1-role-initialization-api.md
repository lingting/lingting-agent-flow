# P4-M2-1 —— 首次部署角色初始化与角色查询 API

> 实施依据：`master` 上 `internal/app/roles.go`、`internal/server/server.go`、
> `internal/storage/{interfaces.go,sqlite/roles.go}`、`internal/httpapi/router.go` 与
> `tests/` 的实际代码核实。对应 P4-M1 缺口 **G2**（见
> [`P4-M1-api-web-gap-inventory.md`](./P4-M1-api-web-gap-inventory.md) §5）。

## 1. 结论摘要

本轮把角色初始化接入 `agentflow server` 的启动协议，并新增只读角色查询接口
`GET /api/v1/roles`。**G2 的后端部分已闭合**：全新数据库启动后配置中的角色会被真实写入，
已有角色不会被重启覆盖，初始化失败会阻止服务进入可用状态。

本轮**未实现**：环境查询 API、前端角色选择器、任务创建表单改造、`/start` 接入、角色
CRUD / 同步 / 管理页面。

## 2. 角色初始化契约

### 2.1 调用位置与顺序

初始化发生在 `internal/server/server.go` 的 `openDatabase` 中，位置固定在
**数据库成功打开之后、任何业务服务构造之前**：

```text
配置加载 → 数据库打开（含迁移）→ 角色初始化 → Session / SSH / 执行与编排服务 → 后台服务 → HTTP
```

理由：全新数据库的 `roles` 为空时，`TaskService.CreateTask` → `ensureRoles` 必然失败。
把初始化放在业务服务之前，使"服务可接收请求"与"角色已就绪"成为同一个启动前提。

### 2.2 语义

| 场景 | 行为 |
|---|---|
| 角色表为空 | 导入配置中的全部角色，返回导入数量 |
| 角色表已有记录（含已禁用） | 不写入任何记录，返回 0 |
| 重复 / 并发启动 | 检查与写入在同一事务内完成，只导入一次 |
| 配置中的角色发生变化 | **不**隐式同步、**不**覆盖数据库中的角色 |
| 配置 `roles` 为空 | 由既有配置校验拒绝（`config.validateRoles`），启动不进入角色初始化 |

初始化完全复用既有能力，未新增第二套转换或写入逻辑：

- `app.RoleSeeds(cfg)`（`internal/app/roles.go`）把配置转换为 `storage.RoleSeed`；
- `storage.RoleSeeder.SeedRoles`（`internal/storage/sqlite/roles.go`）在事务内做
  "计数为 0 才写入"，角色时间戳与工具策略由存储层补齐。

服务层通过 `storage.RoleSeeder` 接口断言获取能力（`Server.seedRoles`），没有在业务代码
里散布 SQLite 类型断言；存储不支持该契约时直接失败，而不是静默跳过。

### 2.3 失败处理

| 失败 | 结果 |
|---|---|
| 存储不支持 `RoleSeeder` | `初始化角色失败: 存储不支持角色初始化`，启动失败 |
| 配置转换失败（重复 ID、非法类型等） | 透传 `app.RoleSeeds` 的定位错误（含 `roles[i]`） |
| 数据库写入失败 | `初始化角色失败: <存储层原因>`，启动失败 |

任一路径都返回错误，`server.Run` 按既有逻辑标记 `ErrStartup` 并逆序清理：已打开的数据库
被关闭，后台服务与 HTTP 监听都不会启动。因此不存在"服务已可接收请求但角色缺失"的窗口。

## 3. 角色查询接口契约

### 3.1 路由

```text
GET /api/v1/roles
```

- 只读，仅支持 `GET` 与 `HEAD`；其他方法返回 `405` 且 `Allow: GET, HEAD`。
- `/api/v1/roles/{id}` 等子路径一律 `404`：本阶段不提供角色 CRUD。
- 依赖未初始化时返回 `503`（`code: internal_error`），**不**返回空列表。

### 3.2 响应

```json
{
  "records": [
    {
      "id": "planner",
      "name": "架构规划师",
      "type": "planner",
      "runner": "pi",
      "model": "provider/model",
      "enabled": true
    }
  ],
  "total": 1
}
```

- 形状与执行节点列表一致：`records` + `total`，不引入分页（角色数量有限）。
- `type` 直接取 `domain.RoleType` 的真实取值（`planner` / `developer` / `validator`），
  HTTP 层不定义第二套类型。
- **不返回** `system_prompt`、`tools_policy`、时间戳或任何凭据类字段。

### 3.3 禁用角色语义

`enabled=false` 的角色**保留在响应中并如实标注**，用于诊断；创建任务时的角色选择器必须
自行排除它们。服务端仍会在 `CreateTask` 中拒绝禁用角色，前端过滤不是唯一防线。

### 3.4 错误映射

查询失败一律按服务内部错误返回 `500`（`code: internal_error`，消息脱敏为
`服务内部错误`，原始原因只写日志）。**存储错误不会被伪装成成功空列表。**

## 4. 代码证据

| 关注点 | 位置 |
|---|---|
| 启动期初始化 | `internal/server/server.go` 的 `openDatabase` → `seedRoles` |
| 配置转换 | `internal/app/roles.go` 的 `RoleSeeds` / `roleSeed`（未改动） |
| 原子导入 | `internal/storage/sqlite/roles.go` 的 `SeedRoles`（未改动） |
| 只读查询服务 | `internal/app/role_query.go` 的 `RoleService.List` |
| HTTP 处理器 | `internal/httpapi/roles.go`（`PathRoles`、`roleHandler`） |
| 路由注册 | `internal/httpapi/router.go` 的 `NewHandler` / `router.rolesHandler` |
| 依赖注入 | `internal/httpserver/server.go` 的 `Options.Roles` → `httpapi.Deps.Roles` |
| 服务装配 | `internal/server/server.go` 的 `roleService` → `startHTTP` |

## 5. 测试证据

全部为真实 SQLite + 正式迁移；测试产物保留在
`os.TempDir()/lingting-agent-flow-tests/` 下的随机目录，运行时输出 `TEST_ARTIFACT_DIR`。

| 测试 | 覆盖行为 |
|---|---|
| `tests/unit/server/roles_test.go` 的 `TestFreshDatabaseImportsConfiguredRoles` | 空库首次启动后配置角色真实写入（名称/模型/提示词/类型/启用/工具策略） |
| 同文件 `TestRestartDoesNotDuplicateOrOverwriteRoles` | 重启不重复导入；已改写的名称/模型/提示词/工具策略/启用状态不被覆盖 |
| 同文件 `TestExistingRolesAreNotAugmentedFromConfig` | 已有部分角色时不追加配置中的其他角色 |
| 同文件 `TestRoleSeedConversionFailureStopsStartup` | 配置转换失败返回可定位错误（含 `roles[1]`），数据库被关闭 |
| 同文件 `TestRoleSeedStorageFailureStopsStartup` | 存储写入失败时启动失败，且没有 HTTP 监听 |
| 同文件 `TestRepeatedStartupKeepsSingleRoleSet` | 连续三次启动后角色数量稳定 |
| `tests/unit/store/roles_seed_test.go` 的 `TestConcurrentSeedRolesImportsOnce` | 6 个并发调用只写入一次，无重复角色 |
| 同文件 `TestSeedRolesKeepsDisabledRoleAsExisting` | 已禁用角色同样视为已有记录，不被改写 |
| `tests/unit/httpapi/roles_test.go` | 真实持久化角色返回、字段最小化（无 `system_prompt`）、禁用角色语义、空库空列表、405 + `Allow`、依赖缺失 503、存储错误 500、子路径 404 |
| `tests/integration/local/httpserver/roles_test.go` | 真实 HTTP 服务上 `GET /api/v1/roles` 返回初始化角色、405 语义、空库空列表 |

执行结果（本轮实际运行）：

| 命令 | 结果 |
|---|---|
| `go test -count=1 ./tests/unit/httpapi/ ./tests/unit/store/ ./tests/integration/local/httpserver/ ./tests/unit/app/ ./tests/unit/server/` | 通过 |
| `go test -count=1 ./tests/unit/...` | 通过（21 个包） |
| `go test -count=1 ./tests/integration/local/...` | 通过（14 个包） |
| `gofmt -l internal cmd tests` | 无输出 |
| `go vet ./...` | 无输出 |

未执行：`tests/integration/pi`、`tests/integration/ssh`、`tests/integration/remote/**`、
`tests/e2e/**`（依赖真实远程目标与发布构建，与本轮改动无关）。这些**不计为通过**。

## 6. 剩余风险与后续依赖

1. **角色变更没有正式入口**：本轮只做"空表初始化 + 只读查询"。配置中新增或修改角色
   不会同步到已有数据库，用户目前只能通过 CLI 之外的 SQLite 操作或后续里程碑补齐
   （P4-M1 §5 的 G2 剩余部分：角色 CRUD 或显式同步命令）。
2. **禁用角色的选择器责任**：接口如实返回禁用角色，前端选择器必须过滤；后端
   `CreateTask` 已有校验，因此不会因前端遗漏而创建出不可执行任务。
3. **首次启动即失败的行为变化**：配置 `roles` 非法（例如重复 ID）现在会让
   `agentflow server` 直接启动失败，而不再"先起来、创建任务时才报错"。这是刻意的。
4. **启动期写入**：角色初始化在启动阶段产生一次写事务，并发启动同一数据库时由
   `_txlock=immediate` 与 `SeedRoles` 的事务语义保证只导入一次；已在
   `TestConcurrentSeedRolesImportsOnce` 中验证。
5. **后续依赖**：P4-M2-2（环境查询 HTTP API）→ P4-M2-3（前端接入 `/start`）→
   P4-M2-4（创建任务弹窗复用角色接口与执行节点接口）。

## 7. 退出条件核对

| 条件 | 状态 |
|---|---|
| 全新数据库启动后初始角色真实写入数据库 | 满足（`TestFreshDatabaseImportsConfiguredRoles`） |
| 已有角色不会因重启被覆盖或隐式同步 | 满足（`TestRestartDoesNotDuplicateOrOverwriteRoles`、`TestExistingRolesAreNotAugmentedFromConfig`） |
| 初始化失败阻止服务进入可用状态 | 满足（`TestRoleSeedConversionFailureStopsStartup`、`TestRoleSeedStorageFailureStopsStartup`） |
| `GET /api/v1/roles` 返回真实、最小化、语义明确的列表 | 满足（`tests/unit/httpapi/roles_test.go`、`tests/integration/local/httpserver/roles_test.go`） |
| 相关测试实际执行并通过，证据与文档一致 | 满足（见 §5） |
