# Docker 兼容性

本文说明 AgentFlow 支持的 Docker 版本基线、固定 API 基准、Docker 命令清单与新增 Docker
调用时必须遵守的规则。**强制规则**同时写在仓库根目录 `AGENTS.md` 的「Docker 兼容性基线」
章节，本文只展开说明与操作方法。

## 1. 基线

| 项目 | 固定基线 | 含义 |
|---|---|---|
| Docker CLI | `29.0.0` | 实际执行命令的 CLI 版本下限 |
| Docker Engine | `29.0.0` | CLI 所连接 Engine 的版本下限 |
| AgentFlow Engine API 基准 | `1.52` | 所有 CLI 调用与直接 API 调用的兼容性基准 |

基线取值只在 `internal/docker/compatibility.go` 中定义：

```go
docker.MinimumCLIVersion    // "29.0.0"
docker.MinimumEngineVersion // "29.0.0"
docker.EngineAPIBaseVersion // "1.52"
```

规则文件、代码、测试与文档必须保持一致。高于基线的 Docker 可以使用，但 AgentFlow 不得
依赖更高版本才出现的命令、参数、API 字段或行为。

最低兼容版本是**开发约束**，不是生产版本推荐值。

## 2. CLI 版本与 Engine 版本的区别

- **Docker CLI 版本**：实际执行 `docker` 命令的客户端版本，来自 `docker version` 的
  `Client.Version`。
- **Docker Engine 版本**：CLI 所连接守护进程的版本，来自 `docker info` 的 `ServerVersion`。

二者可能不同：开发机装的是新 CLI，远端机器跑的是旧 Engine。AgentFlow 的预检分别检查
两者，不得用 CLI 版本代替 Engine 版本。

## 3. API 版本字段的区别

Docker 服务端信息中会出现三个容易混淆的概念：

| 字段 | 含义 |
|---|---|
| `APIVersion`（最高版本） | 服务端声明支持的最高 API 版本 |
| `MinAPIVersion`（最低接受版本） | 服务端接受请求的最低 API 版本 |
| AgentFlow API 基准 | 固定 `1.52` |

兼容判定条件是：

```text
MinAPIVersion <= 1.52 <= APIVersion
```

- 服务端最高版本是 `1.51`：即使 CLI 帮助信息包含所需命令，也判为不兼容。
- 服务端最高版本是 `1.56`，但最低接受版本已升到 `1.53`：同样判为不兼容。

**不得**把 `MinAPIVersion` 与 `1.52` 做「必须大于等于」的比较——它表达的是服务端能接受的
下界，而不是能力上界。

## 4. API 版本如何固定

Docker 支持用 `DOCKER_API_VERSION` 让 CLI 使用指定 API 版本，但设置它会关闭自动版本协商。
因此 AgentFlow 把探测与业务调用分开：

| 场景 | 远程命令形态 | 目的 |
|---|---|---|
| 版本探测 | `env -u DOCKER_API_VERSION docker version ...` | 隔离远端遗留变量，取得真实客户端与服务端能力 |
| 业务调用 | `env DOCKER_API_VERSION=1.52 docker <subcommand> ...` | 固定 API 基准，不依赖隐式默认值 |

两条路径都由 `internal/docker` 的命令构造入口生成（`dockerProbeCommand` /
`dockerCommand`），业务代码不得自行拼接。固定版本与 Engine 不兼容时 Docker 直接返回错误
（例如 `client version 1.52 is too new`），AgentFlow 会把它作为明确失败上报，不会静默降级
到旧 API 或升级到新 API。

不修改远端用户的 Shell 配置，也不写入远端全局环境变量。

## 5. 直接 Engine API 调用

当前只有两处直接访问 Engine API，都经由已认证 SSH 连接访问远端 Docker 的 Unix 套接字：

| 用途 | 请求 |
|---|---|
| API 基准探测 | `GET /v1.52/version` |
| 基础镜像元数据 | `GET /v1.52/distribution/{name}/json` |

路径前缀由 `dockerAPIVersionPrefix`（`"/v" + EngineAPIBaseVersion`）派生，禁止硬编码。

两条请求都是只读的：不创建容器、不拉取镜像层、不改变远端镜像缓存、容器与卷状态。不要求
远端安装 `curl` 等额外工具，也不得用 `docker pull` 作为回退手段。

## 6. Docker 命令清单

`docker.RequiredCLICommands` 是唯一的命令与参数清单，SSH 环境预检按它对每条命令执行
`docker <command> --help` 并核对参数选项。

| 命令 | 使用的参数 | 代码位置 |
|---|---|---|
| `create` | `--name`、`--workdir`、`--label`、`--mount` | `CreateContainer` |
| `start` | — | `StartContainer` |
| `stop` | `--time` | `StopContainer` |
| `rm` | `--force` | `RemoveContainer` |
| `inspect` | — | `InspectContainer` |
| `exec` | `--workdir`、`--env`、`--interactive` | `ExecCommandIn` |
| `logs` | `--tail` | `ContainerLogs` |
| `run` | `--rm`、`--entrypoint` | `CheckRunContract` |
| `ps` | `--all`、`--filter`、`--format` | `ListAgentFlowContainers` |
| `volume create` | `--label` | `EnsureVolume` |
| `build` | `--tag`、`--label`、`--file`、`--build-arg`、`--no-cache`、`--progress` | `BuildImage` |
| `image inspect` | — | `InspectImage` |
| `cp` | — | `CopyFromContainer` |
| `version` | `--format` | `ProbeVersion` |
| `info` | `--format` | `ProbeVersion` |

注意：`docker create` **不接受** `--detach`（该参数只属于 `docker run`），传入会被 CLI
直接拒绝为 `unknown flag`。

## 7. SSH 环境预检

执行节点预检按顺序执行四项必需检查，前一项失败时后续检查记为 `not_run`：

1. **SSH 连接**：握手、认证、执行一条无副作用的非交互远程命令。
2. **Docker 版本与 API 兼容性**（`docker_version`）：CLI 版本、Engine 版本分别不低于
   基线；固定 API `1.52` 落在 Engine 声明的版本区间内。
3. **Docker CLI 帮助信息**（`docker_cli_help`）：逐条核对命令与参数选项。
4. **基础镜像元数据**（`base_image_metadata`）：解析基础 Dockerfile，查询外部 `FROM`
   镜像的元数据。

全部必需检查通过才写入 `normal`；版本检查失败时整体状态为 `abnormal`，`validation_message`
说明不兼容项，`validation_details` 记录实际版本、期望版本与失败原因，并且该节点不能用于
真实远程任务。

校验器版本为 `v2`（增加了 `docker_version` 检查项）。`v1` 产生的 `normal` 结果不再被视为
有效。

`normal` 的含义仅限实际执行的检查：Docker 兼容性基于版本与帮助信息，**不代表**已验证
容器创建与运行。

## 8. 新增 Docker 命令

1. 在 `internal/docker` 内用 `dockerCommand(...)` 构造命令，不要在业务层拼接命令行。
2. 确认命令与参数在 Docker CLI `29.0.0` 上存在；不要使用更高版本才引入的参数。
3. 同步更新 `docker.RequiredCLICommands`，补充参数选项与代码位置。
4. 如涉及直接 API 调用，使用 `/v1.52/` 前缀并确认字段在 API `1.52` 中已存在。
5. 在 `tests/unit/docker/` 增加命令构造断言；确有必要时补充集成测试。
6. 若基线本身需要提高，见下一节。

## 9. 如何测试最低基线

- **单元测试**（不依赖真实 Docker）：`tests/unit/docker/` 覆盖版本解析与比较、API 区间判定、
  `DOCKER_API_VERSION` 的控制策略、命令清单与参数。
- **本机集成测试**（进程内假 SSH 服务端与假 Engine）：`tests/integration/local/docker/`
  与 `tests/integration/local/validation/`。
- **真实远程测试**（需真实 Docker）：`tests/integration/remote/docker/` 等目录，通过
  `tests/config/docker.yml` 的 `docker.remote_enabled`（同时需 `tests/config/ssh.yml` 的
  `ssh.enable`）显式启用。目标环境版本低于基线时预检必然失败，这属于
  环境不满足基线，不是代码缺陷；必须如实报告，不得把未运行的测试记为通过。

真实环境缺少 Docker CLI `29.0.0` 时，帮助信息的最低基线验证只能跳过，且必须在报告中说明
跳过原因。

## 10. 如何提出基线升级

修改最低版本或 API 基准属于架构决策，必须同时更新：

1. 根目录 `AGENTS.md` 的「Docker 兼容性基线」章节；
2. `internal/docker/compatibility.go` 中的集中式常量；
3. `docker.RequiredCLICommands` 与直接 API 访问路径；
4. 单元测试与相关集成测试；
5. 本文档；
6. SSH 环境预检行为（含校验器版本是否需要提升）。

缺少任一项同步变更时，不得单独提高某个命令的版本要求，也不得为了让单个测试通过而调整
基线。
