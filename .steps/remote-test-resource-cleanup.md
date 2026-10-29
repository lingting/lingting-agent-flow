# 远程真实测试资源治理：统一目录与容器零残留验收

> 结论：`completed`（真实远程零残留已由远程 `docker ps -a` 实际清单证明）
>
> 实施基线：`origin/master` `7d96dcef93da14b1aaf8f8e7c3c2339adc2f916b`（2026-10-29 核实，
> 本地 `master` 与该基线一致，工作区无未提交改动）。
>
> 本文档只覆盖"远程真实测试资源生命周期"，不重新审计服务端第一版业务链路。
> 第一版历史结论保留在 [`SERVER-v1-acceptance.md`](./SERVER-v1-acceptance.md)。

## 1. 任务目标

1. 所有远程真实测试的远程主机资源目录统一到 `~/.lingting/agentflow-tests`，不再使用
   旧的 `~/agentflow-tests` 与平行的 `~/.lingting/agentflow-e2e`。
2. 所有真实远程测试创建的 Docker 容器都具备可追踪身份与明确的清理责任：无论测试通过、
   失败、跳过还是中途出错，都能回收。
3. 清理是否成功由远程 Docker 的真实状态证明（`docker ps -a`），不采信删除方法的返回值。
4. 清理失败、状态无法确认、所有权无法确认一律反映为测试失败或阻塞，不允许只输出
   `WARN` 后仍报告通过。
5. 生产容器、其他测试运行的活动容器、共享镜像与缓存卷不被误删。

## 2. 实施前的实际观察（基线事实）

| 观察项 | 实施前事实 |
|---|---|
| 远程测试目录 | `~/agentflow-tests/{agent,docker}` 与 `~/.lingting/agentflow-e2e/<后缀>` 两套并存 |
| 远程容器 | `docker ps -a` 只有 1 个非测试容器 `pgsql-server`，无历史测试容器 |
| 远程镜像 | `agentflow-test-agent:1.0.0` 与多个 `agentflow-test-lifecycle:*`（共享资源，不删除） |
| 清理实现 | 多数用例在 `t.Cleanup` / `defer` 中调用 `RemoveContainer`，失败只 `t.Logf("WARN ...")`；`remote/workflow` 完全不删除生产服务创建的容器 |
| 工作流容器登记时机 | 通过生产服务创建，测试侧没有任何创建记录或清理责任 |
| `remote/codesaver` 连接关闭顺序 | `defer client.Close()` 早于 `t.Cleanup` 的容器清理执行，清理必然拿不到连接 |
| 最终核验 | 不存在；测试包退出后不再检查远程状态 |

## 3. 统一远程测试目录

### 3.1 约定

```text
<远程 Home>/.lingting/agentflow-tests/<测试包>/<用例唯一标识>/...
```

| 测试包 | 子目录 | 目录用途 |
|---|---|---|
| `tests/integration/remote/agent` | `agent` | 按需构建的测试镜像构建上下文与构建日志 |
| `tests/integration/remote/docker` | `docker` | 预检工作空间、构建上下文、构建日志 |
| `tests/integration/remote/codesaver` | `codesaver` | 预留（该用例只使用容器内部路径） |
| `tests/integration/remote/workflow` | `workflow` | 执行节点的远程管理工作区（项目配置、Dockerfile、镜像构建资源） |
| `tests/integration/ssh` | `ssh` | 真实 SSH/SFTP 用例的远程临时目录 |

- 解析与创建统一由 `tests/internal/testkit/remotedir.go` 提供：
  `RemoteTestRoot` 负责纯路径拼接，`ResolveRemoteTestRoot` 负责通过 SSH 解析远程 Home、
  拼接并 `mkdir -p`。
- 路径只能来自 `sshclient.ResolveHome` 解析出的绝对路径，禁止把字面量 `~` 作为远程 API
  的路径参数，也不依赖 Shell 隐式展开。
- 用例标识只允许 `[A-Za-z0-9._-]`，禁止 `..` 与路径分隔符，避免逃逸出测试目录。
- 测试包子目录是白名单（`remoteTestPackages`），新增测试包必须在该文件中登记，防止各包
  各自拼接路径造成漂移。
- **不影响本机测试产物目录**：`testkit` 的本机产物仍是
  `os.TempDir()/lingting-agent-flow-tests/`，两者互不影响；生产管理工作区
  （`domain.DefaultWorkspaceRoot` = `~/.lingting/agentflow`）也不受任何改写。
- **不涉及容器内部路径**：`/workspace/project`、`/tmp/agentflow/sessions` 等容器内路径
  保持原样，未被当作远程主机路径迁移。

### 3.2 路径迁移对照

| 位置 | 实施前 | 实施后 |
|---|---|---|
| `remote/docker` `remoteTestRoot` | `<home>/agentflow-tests/docker/<suffix>` | `<home>/.lingting/agentflow-tests/docker/<suffix>` |
| `remote/agent` `remoteTempRoot` | `<home>/agentflow-tests/agent` | `<home>/.lingting/agentflow-tests/agent` |
| `remote/workflow` `createHost` 管理工作区 | `<home>/.lingting/agentflow-e2e/<suffix>` | `<home>/.lingting/agentflow-tests/workflow/<suffix>` |
| `tests/integration/ssh` 用例目录 | `<home>/agentflow-tests/<suffix>` | `<home>/.lingting/agentflow-tests/ssh/<suffix>` |

远程主机上的旧目录没有被自动删除（其中可能有人工排查资料），只是不再被写入；需要清理时
由用户自行处置。本次任务只要求回收测试容器。

## 4. 容器追踪、清理与核验机制

### 4.1 代码位置

| 能力 | 位置 |
|---|---|
| 容器清单查询（只读，含已停止容器） | `internal/docker.Runner.ListContainers` |
| 所有权核验 | `internal/docker.VerifyOwnership`（既有） |
| 单容器清理 + 后置条件核验 | `testkit.CleanupContainer` |
| 运行级登记表与用例级清理 | `testkit.ContainerRun.Track` |
| 包级最终核验 | `testkit.ContainerFinalizer.Finalize` |
| 统一入口 finalize 钩子 | `testkit.RegisterFinalizer` + `testkit.RunFinalizers`（在 `testkit.Main` 内执行） |

`internal/docker` 只新增了一个只读查询方法（`docker ps` 已在 CLI 兼容清单中），
没有为测试扩大任何写能力，也没有改变生产容器的语义。

### 4.2 登记规则（创建之前登记）

- 每个测试容器在**创建之前**登记清理责任：此时容器名称已知，即使创建返回失败、启动失败、
  后续注入步骤 `t.Fatalf`，清理能力也不会丢失。
- 取得真实 Docker ID 后立即补写，使清理与核验优先使用准确 ID；名称始终作为兜底查找依据。
- 所有者证据：`domain.Container` 登记记录（`docker.VerifyOwnership` 校验
  `agentflow.managed` / `agentflow.host_id` / `agentflow.container_id` 三个标签）或精确名称。
- 生产服务创建的工作流容器采用两层登记：
  1. 统一启动之前登记"按任务从数据库解析容器"的延迟登记（生产服务先写登记记录再创建
     容器，因此创建中途失败也能找回容器）；
  2. 创建成功后立即按精确名称 / ID / 登记记录再登记一次。
- `remote/codesaver` 的容器创建改为经 `docker.Runner.CreateContainer`，从而获得与管理
  容器一致的归属标签；此前用裸 `docker run` 创建的容器没有任何归属证据。

### 4.3 清理规则

1. 优先按准确 Docker ID 定位；ID 已不存在时按名称查找并核对全名（Docker 的 `name`
   过滤是子串匹配）。
2. 删除前重新核验所有权；核验失败**禁止删除**，记录冲突并使测试失败。
3. 运行中的容器先 `docker stop`，再 `docker rm --force`。
4. 目标已不存在视为已达清理目标（幂等）；业务用例已自行删除容器不会误报残留。
5. 删除返回错误不直接判定失败，但**必须**重新检查真实状态：仍存在即失败，无法确认即失败。
6. 禁止全局 prune，禁止按名称包含 `agentflow` 就删除，禁止删除共享镜像与缓存卷。

### 4.4 最终核验（`docker ps -a`）

- 每个真实远程测试包在 `TestMain` 中注册 `RegisterContainerFinalizer(containerRun)`，
  `testkit.Main` 在 `m.Run()` 之后、日志刷新之前执行它：
  - 因此 `t.Parallel` 用例已全部结束；
  - 核验输出（含真实容器清单）写入同一份 `test.log`；
  - 核验失败会把进程退出码改成非 0，不能"只 WARN 仍通过"。
- 核验内容：
  1. 逐条检查本次登记容器是否已消失（按 ID 与名称，都以真实观测为准）；
  2. 读取真实 `docker ps -a` 清单，记录 `baseline` / `final` / `tracked` 三组数量与逐条清单；
  3. 出现**未登记但明确属于测试资源**（名称前缀 `agentflow-test-` / `agentflow-codesaver-`）
     的新容器时判定失败，且**不删除**（归属无法确认）；
  4. 运行前就存在的历史测试残留只报告（`REMOTE_CONTAINER_PREEXISTING`），不处置；
  5. 运行前存在、运行后消失的非测试容器输出 `REMOTE_CONTAINER_BASELINE_GONE` 供人工复核
     （测试只删除自己登记并核验过归属的容器，该差异也可能是运行契约探测等一次性容器）。
- 本次运行没有创建任何测试容器（例如只跑不创建容器的用例）时报告
  `REMOTE_CONTAINER_VERIFY ... status=SKIP reason=no_test_container_created`：既不虚构
  通过，也不因为缺少基线误判失败。
- 远程测试未启用或目标缺失时输出 `WARN [SKIP]`，`FINALIZER ... status=SKIP`，
  不虚构"清理通过"。
- 判定用语固定为 `REMOTE_CONTAINER_VERIFY run=<包> status=PASS|FAIL|SKIP` 与
  `FINALIZER name=... status=PASS|SKIP|FAIL`。

## 5. 审计：所有真实远程容器创建入口

| 文件 | 创建方式 | 实施后清理证据 |
|---|---|---|
| `remote/docker/docker_test.go` | `Runner.CreateContainer` + `StartContainer` | 创建前登记，用例结束清理并核验 |
| `remote/docker/concurrency_acceptance_test.go` | 同上（unknown / 孤儿 / 并发 / 暂时不可达四个现场，共 4 个容器） | 同上，逐个登记与核验 |
| `remote/docker/execenv_test.go` | 同上 | 同上 |
| `remote/agent/agent_test.go` | 同上 | 同上 |
| `remote/codesaver/codesaver_test.go` | 改为 `Runner.CreateContainer` + `StartContainer` | 同上；并修正连接关闭顺序 |
| `remote/workflow/e2e_chain_test.go` | 生产 `app.ContainerService`（经 `ExecutionEnvironmentService`） | 启动前登记数据库延迟解析 + 创建后精确登记 |

实施中发现的真实缺陷（已修复）：

- `remote/codesaver` 用 `defer client.Close()` 关闭连接，而容器清理注册为 `t.Cleanup`；
  Go 保证 `defer` 先于 `t.Cleanup` 执行，因此第一次运行出现
  `清理前无法确认容器状态: ssh_unavailable`，容器真实残留，最终核验判 `FAIL` 且进程
  以非 0 退出——这正是本任务要求的"清理失败不得静默通过"。改为
  `workspace.Cleanup(client.Close)` 后连接在容器清理之后才关闭。
- 该次残留容器（`agentflow-codesaver-a6ac54dab085`，带 `agentflow.managed=true` 标签）
  已按名称与标签核验归属后手工 `docker rm --force` 清除，并在随后的运行中得到零残留证明。

## 6. 实际执行命令与结果

### 6.1 本地检查（无外部依赖）

| 命令 | 结果 |
|---|---|
| `gofmt -l cmd internal tests` | 无输出 |
| `go vet ./...` | 无问题 |
| `go test -count=1 ./tests/unit/testkit/...` | 50 passed |
| `go test -count=1 ./tests/integration/local/docker/...` | 17 passed |
| `go test -count=1 ./tests/integration/local/workflow/...` | 116 passed |

### 6.2 真实远程测试（`-p 1` 顺序执行）

```bash
go test -count=1 -timeout 50m -p 1 -v ./tests/integration/remote/...
```

**结果：13 passed / 0 failed（4 个包）**，另单独执行
`go test -count=1 ./tests/integration/ssh/...` → 6 passed。

| 包 | 结果 | 容器登记数 | 最终核验 |
|---|---|---|---|
| `remote/docker` | 6 passed | 7 | `REMOTE_CONTAINER_VERIFY run=remote/docker status=PASS` |
| `remote/agent` | 2 passed | 2 | `remote/agent status=PASS` |
| `remote/codesaver` | 1 passed | 1 | `remote/codesaver status=PASS` |
| `remote/workflow` | 4 passed | 3 | `remote/workflow status=PASS` |

### 6.3 远程 `docker ps -a` 最终核验（真实清单）

测试结束后直接查询远程主机：

```text
279fd62835cf  pgsql-server  running  Up 3 hours
```

- 测试前基线：`pgsql-server`（非测试容器，未被删除）。
- 测试后最终：仍只有 `pgsql-server`；`docker ps -a --format '{{.Names}}' | grep -cE '^agentflow-(test|codesaver)-'` = `0`。
- 各包日志中的 `REMOTE_CONTAINER_CLEANUP ... result=absent` 与
  `REMOTE_CONTAINER_EVIDENCE ... baseline=1 final=1` 与上述清单一致。
  `remote/agent` 的基线为 2、最终为 1：多出的一个是运行契约探测
  （`docker run --rm`，自动生成名称如 `pedantic_jones`）在同一包运行期间出现并自动消失，
  日志以 `REMOTE_CONTAINER_BASELINE_GONE` 如实记录，测试没有删除任何非登记容器。
- 远程目录：`~/.lingting/agentflow-tests/{docker,ssh,workflow}`（`docker` 12 项、
  `ssh` 2 项、`workflow` 3 项，均为目录与构建产物，不是容器）。`agent` 目录本次未产生，
  因为测试镜像已存在、未触发按需构建。

### 6.4 一次业务层失败（如实记录，与资源回收无关）

`remote/workflow` 第一次单独执行时 `TestRemoteE2EFullChain` 业务断言失败：

```text
WORKFLOW status=blocked class=step_not_repairable summary=修复循环停止（unsupported_step）:
         步骤类型 plan 不适用自动修复
FINAL_TASK status=recovery_required failure=必需检查未通过: notes-exists（退出码 1）
e2e_chain_test.go:582: 停在交付阶段时必须留下代码保存记录
```

- 该轮**容器清理与最终核验仍然 PASS**（容器已删除并核验不存在），失败只发生在业务断言。
- 根因是真实模型的输出不确定性：规划阶段步骤继承了任务级必需检查
  `test -f NOTES.md`，而基座仓库 `lingting/lingting-agent-flow-test` 在
  `92f6432678902c7532b27c713dbbd026cbbed5a0` 只含 `README.md`；规划角色是只读策略，
  是否满足该检查取决于模型行为。同一份代码随后两次重跑均 `PASS`（完整成功链路：
  `WORKFLOW status=completed`、`CODE_SAVE push=succeeded`、`SESSIONS=5 全部已归档`）。
- 结论：该用例存在"规划阶段步骤验证任务级文件检查"的固有抖动，属于业务层测试设计问题，
  不在本次资源治理范围内，已作为剩余风险记录（见 §8），未在本轮修改产品行为。

## 7. 新增与调整的测试

### 7.1 目录辅助（`tests/unit/testkit/remotedir_test.go`）

- 统一路径拼接（`.lingting/agentflow-tests/<包>/<用例>`）；
- 5 个测试包子目录互不相同；
- 同一测试包不同用例目录互不覆盖；
- 非法输入（相对 Home、字面量 `~`、未登记测试包、`..`、非法字符）给出明确错误；
- 不影响本机产物目录（仍为 `os.TempDir()/lingting-agent-flow-tests`）；
- 不改写生产管理工作区 `~/.lingting/agentflow`。

### 7.2 清理辅助（`tests/unit/testkit/remotecontainer_test.go`）

用可控 Docker 替身覆盖需求中列出的失败场景：

- 创建成功但未启动仍能清理；
- 容器已不存在时清理幂等、且不发出删除请求；
- 删除返回成功但容器仍在 → 清理失败；
- 删除返回错误但容器实际已消失 → 清理成功（以真实状态为准）；
- 检查因连接错误无法确认 → 判定失败且不删除；
- 所有权不符 → 拒绝删除、不发出删除请求、容器保留；
- 未取得 Docker ID 时按名称找回容器；
- 缺少名称与 ID 时拒绝执行；
- 用例结束时自动清理并核验；
- 最终核验：零残留通过 / 登记残留失败 / 未登记测试容器失败且不删除 /
  历史残留只报告 / 非测试容器保留 / 清单读取失败判失败 / 没有创建容器时报告跳过；
- 运行中的容器先停止再删除；停止失败但删除成功时以真实状态判定；
- `IsTestContainerName` 只匹配测试前缀，不误判 `agentflow-<hex8>` 与 `pgsql-server`。

### 7.3 统一入口语义（`tests/unit/testkit/finalize_test.go`）

- 核验失败 → 判定失败（改变退出码）；
- 跳过与通过都不改变退出码，但跳过单独标记为 `SKIP`；
- 核验函数 panic 按失败处理。

## 8. 剩余风险与未决项

1. **`TestRemoteE2EFullChain` 业务抖动**（见 §6.4）：规划阶段步骤携带任务级文件检查，
   模型行为不确定时会在规划阶段被判失败。修复需要产品/测试设计决策（例如规划步骤不套用
   文件型必需检查，或测试改用规划可满足的检查），不在本次范围。
2. **历史残留不做自动处置**：运行前已存在的测试命名容器只报告不删除，因为无法确认是否
   属于另一个正在运行的测试。远程主机目前没有此类残留。
3. **无人值守场景的清理窗口**：进程被强制杀死（`kill -9`、CI 取消）时用例级 `t.Cleanup`
   与包级 finalize 都不会执行，容器会残留。当前机制保证的是"进程正常结束（含测试失败）
   后零残留"，被强杀的场景仍需要人工/外部回收。
4. **旧远程目录未删除**：`~/agentflow-tests` 与 `~/.lingting/agentflow-e2e` 中的历史目录
   仍保留在远程主机上，只是不再写入。
5. **旧的 `agentflow-test-lifecycle:*` 镜像**：属于共享镜像，按要求不删除。
