# Docker 兼容性

本文件是 Docker 版本基线、命令与 API 使用规则、容器管理约束的唯一说明。执行节点预检的状态与代次语义
见 [ssh-hosts.md](ssh-hosts.md)；容器生命周期在架构中的位置见 [architecture.md](architecture.md)。

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

强制规则：

1. 新增 Docker 命令、子命令与参数，都以 Docker CLI `29.0.0` 为兼容性基准。
2. 新增 Engine API 路由、请求字段、响应字段与行为，都以 API `1.52` 为兼容性基准。
3. 不得因开发环境安装了较新 Docker 而依赖更高版本才支持的功能；高版本 Docker 可以使用，但实际命令与
   API 使用必须保持兼容最低基线。
4. 所有 Docker 调用必须经过项目统一的命令执行或 API 访问入口，禁止在业务代码中随意拼接。
5. CLI 业务调用统一固定 API 基准 `1.52`，只有版本发现与兼容性探测阶段例外，且必须显式控制
   `DOCKER_API_VERSION`。
6. 直接调用 Engine API 时必须使用 `/v1.52/` 路径，不得擅自使用更高版本。
7. SSH 环境预检必须同时检查实际使用的 CLI 版本、Engine 版本与 API 版本信息。
8. `docker --help` 只能用于辅助检查命令与参数，不得作为 Engine 实际运行能力的证明。
9. 预检禁止执行 `docker create`、`docker run`、`docker pull` 等会创建容器、启动工作负载或下载镜像层
   的操作，也不得解析基础 Dockerfile 或查询 `FROM` 镜像的 Registry 元数据；基础镜像可用性由真实的环境
   构建负责。
10. 命令的新增、修改与移除都必须检查最低基线兼容性；不得为了让单个测试通过而提高最低版本或依赖高
    版本专属参数。
11. 修改最低版本或 API 基准属于架构决策，必须同步更新规则、代码、测试与文档。
12. 未真实运行的 Docker 测试不得报告为通过；跳过测试必须说明原因。

最低兼容版本是**开发约束**，不是生产版本推荐值。

### 基线的落地位置

- 基线取值只在 `internal/docker/compatibility.go` 定义，规则文件、代码、测试与文档必须完全一致。
- 版本发现与业务调用的环境变量策略必须分离：探测阶段用 `env -u DOCKER_API_VERSION` 隔离遗留变量，
  业务调用统一显式设置 `DOCKER_API_VERSION=1.52`；不得修改远端用户 Shell 配置来实现固定版本。
- 固定 API 版本与 Engine 不兼容时必须返回明确错误，不得静默降级到旧 API 或升级到新 API。
- 区分 Engine 声明的最高 API 版本与最低接受版本：基准必须落在 `[MinAPIVersion, APIVersion]` 区间内，
  不得把最低接受版本与基准做"大于等于"比较。
- 不得在业务代码中直接调用 `docker` 命令：`internal/docker` 之外禁止构造 Docker 命令行，
  `internal/docker` 内部也必须经由 `Runner` 的统一执行入口。

## 2. CLI 版本与 Engine 版本的区别

- **Docker CLI 版本**：实际执行 `docker` 命令的客户端版本，来自 `docker version` 的 `Client.Version`。
- **Docker Engine 版本**：CLI 所连接守护进程的版本，来自 `docker info` 的 `ServerVersion`。

二者可能不同：开发机装的是新 CLI，远端机器跑的是旧 Engine。预检分别检查两者，不得用 CLI 版本代替
Engine 版本。

## 3. API 版本字段的区别

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

**不得**把 `MinAPIVersion` 与 `1.52` 做「必须大于等于」的比较——它表达的是服务端能接受的下界，而不是
能力上界。

## 4. API 版本如何固定

Docker 支持用 `DOCKER_API_VERSION` 让 CLI 使用指定 API 版本，但设置它会关闭自动版本协商。因此探测与
业务调用分开：

| 场景 | 远程命令形态 | 目的 |
|---|---|---|
| 版本探测 | `env -u DOCKER_API_VERSION docker version ...` | 隔离远端遗留变量，取得真实客户端与服务端能力 |
| 业务调用 | `env DOCKER_API_VERSION=1.52 docker <subcommand> ...` | 固定 API 基准，不依赖隐式默认值 |

两条路径都由 `internal/docker` 的命令构造入口生成（`dockerProbeCommand` / `dockerCommand`），业务代码
不得自行拼接。固定版本与 Engine 不兼容时 Docker 直接返回错误（例如 `client version 1.52 is too new`），
AgentFlow 会把它作为明确失败上报。不修改远端用户的 Shell 配置，也不写入远端全局环境变量。

## 5. 直接 Engine API 调用

当前只有一处直接访问 Engine API，经由已认证 SSH 连接访问远端 Docker 的 Unix 套接字：

| 用途 | 请求 |
|---|---|
| API 基准探测 | `GET /v1.52/version` |

路径前缀由 `dockerAPIVersionPrefix`（`"/v" + EngineAPIBaseVersion`）派生，禁止硬编码。

该请求是只读的：不创建容器、不拉取镜像层、不改变远端镜像缓存与容器状态。不要求远端安装 `curl`
等额外工具，也不得用 `docker pull` 作为回退手段。Distribution Inspect（`/distribution/{name}/json`）
不再是 AgentFlow 的调用范围：基础镜像可用性由 Docker Build 在真实环境构建时处理。

## 6. Docker 命令清单

`docker.RequiredCLICommands` 是唯一的命令与参数清单，环境预检按它对每条命令执行 `docker <command>
--help` 并核对参数选项。

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

注意：`docker create` **不接受** `--detach`（该参数只属于 `docker run`），传入会被 CLI 直接拒绝为
`unknown flag`。

## 7. 容器管理约束

- 远程 Docker 操作只能通过已验证的 SSH 执行层进行，不开放 Docker TCP 端口，不创建反向隧道，也不要求
  本机访问远程 Docker Socket。
- 禁止把 Docker Socket、用户 Home、SSH 私钥或远程项目配置目录挂载到任务容器。
- 项目代码只存在于任务容器内部（`/workspace/project`），不得绑定挂载到宿主机目录，也不得写入远程项目
  配置目录。
- 环境 Dockerfile 的历史版本不得覆盖；每次保存只能生成新的递增版本号。
- 只有构建成功并核实到实际镜像 ID 的版本才能用于创建容器；构建失败不得标记为可用。
- 删除任务容器不得删除共享镜像、共享缓存卷或远程项目配置目录；默认禁止全局 prune。
- 失败任务与默认中断任务必须保留容器；重新开始必须创建新容器，旧容器只能显式 `delete` 或 `detach`。
- Detached 容器不得被 AgentFlow 自动重新接管，也不得被自动启动、停止或删除。

## 8. 与执行节点预检的关系

预检的状态机、代次写入与三项检查的顺序在 [ssh-hosts.md](ssh-hosts.md) 中定义；本文件只提供判据：

- `docker_version`：CLI 与 Engine 版本分别不低于基线，固定 API `1.52` 落在 Engine 声明的区间内。
- `docker_cli_help`：逐条核对第 6 节的命令与参数清单，只执行 `docker <command> --help`。
- 帮助文本可能写入标准输出或标准错误，核对时必须同时读取两个流；退出码为 0 但没有输出时不能判为
  通过。
- 校验超时集中定义在 `internal/docker/validation.go`：单步 SSH 检查、Docker CLI 帮助检查与整体时限都
  不得分散写入 Handler 或业务方法，且整体时限必须大于单步时限。
- 预检不解析基础 Dockerfile、不查询 `FROM` 镜像元数据，也不执行 Docker Build 或 Pull；基础镜像可用性
  由真实的环境构建负责，构建失败沿用既有状态并阻止容器创建。
- `normal` 的含义仅限实际执行的三项检查，**不代表** Registry 可访问、基础镜像存在或环境镜像构建成功，
  也不代表已验证容器创建与运行。

## 9. 新增 Docker 命令

1. 在 `internal/docker` 内用 `dockerCommand(...)` 构造命令，不要在业务层拼接命令行。
2. 确认命令与参数在 Docker CLI `29.0.0` 上存在；不要使用更高版本才引入的参数。
3. 同步更新 `docker.RequiredCLICommands`，补充参数选项与代码位置。
4. 如涉及直接 API 调用，使用 `/v1.52/` 前缀并确认字段在 API `1.52` 中已存在。
5. 在 `tests/unit/container/` 增加命令构造与参数断言；确有必要时补充集成测试。
6. 若基线本身需要提高，见下一节。

## 10. 如何验证最低基线

- **单元测试**（不依赖真实 Docker）：`tests/unit/container/` 覆盖命令构造、参数安全边界与结果解析；
  `tests/unit/environment/` 与 `tests/unit/image/` 覆盖镜像引用、构建上下文与 Dockerfile 校验。
- **本机集成测试**：`tests/integration/local/validation/` 覆盖验证执行与检查结论。
- **真实远程测试**（需真实 Docker）：`tests/integration/remote/docker/` 等目录，通过
  `tests/config/docker.yml` 的 `docker.remote_enabled`（同时需 `tests/config/ssh.yml` 的 `ssh.enable`）
  显式启用。目标环境版本低于基线时预检必然失败，这属于环境不满足基线，不是代码缺陷；必须如实报告，
  不得把未运行的测试记为通过。

`tests/unit/docker/`（版本解析与比较、API 区间判定、`RequiredCLICommands` 与 `docker <cmd> --help` 基线核对）
与 `tests/integration/local/docker/` 已随测试精简删除：最低基线目前**没有自动化守点**，只能靠
`docker.RequiredCLICommands`、真实远程预检与人工核对 `--help` 输出保证。

## 11. 如何提出基线升级

修改最低版本或 API 基准属于架构决策，必须同时更新：

1. `internal/docker/compatibility.go` 中的集中式常量；
2. `docker.RequiredCLICommands` 与直接 API 访问路径；
3. 单元测试与相关集成测试；
4. 本文件；
5. SSH 环境预检行为（含校验器版本是否需要提升）。

缺少任一项同步变更时，不得单独提高某个命令的版本要求，也不得为了让单个测试通过而调整基线。
