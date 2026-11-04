# 组件评估：Moby Docker Engine Go SDK（github.com/moby/moby/client）

> **状态：审计结论，未实施。**
> 本项目当前**没有**引入 `github.com/moby/moby/client`：`go.mod` 与 `go.sum` 未修改，
> `internal/docker`、`internal/sshclient` 的生产代码未修改，当前仍使用自研的直连 Engine API 实现。
> 本文是替换前的可行性审计记录，结论见第 13 节。
>
> **部分内容已被取代**：执行节点预检已移除基础镜像元数据检查，`internal/docker/distribution.go`
> （`InspectDistribution`、`DistributionResult`、`MetadataCode*`、Distribution 响应解析与错误分类）已删除，
> 预检只剩 `GET /v1.52/version` 一处直接 Engine API 调用。因此本文涉及 Distribution Inspect 的章节
> 只作为历史评估记录保留，不再对应现有实现。

- 审计对象：`github.com/moby/moby/client@v0.6.2`（含配套类型模块 `github.com/moby/moby/api@v1.56.1`）
- 审计时间：2026-11-04
- 审计方式：逐文件阅读本仓库实现；从 Go 模块代理取得上述两个模块的完整源码并逐文件阅读；
  用 `go1.24.5` 在本仓库之外的空模块中实际完成 `go build github.com/moby/moby/client`（未改动本仓库的
  `go.mod` / `go.sum`）；官方文档站（`docs.docker.com`）与 `github.com` 在本机 DNS 解析到内网地址，
  抓取工具按策略拒绝，因此线上文档以 `pkg.go.dev/github.com/moby/moby/client` 与模块源码为准，
  下文的每条行为结论都能对应到具体源文件。
- 相关文档：[docker-compatibility.md](../docker-compatibility.md)（版本基线与命令清单）、
  [ssh-hosts.md](../ssh-hosts.md)（SSH 连接层与远端 Unix 套接字通道）。

## 1. 当前自研实现的实际职责

`internal/docker` 同时承担两类完全不同的工作，必须分开看：

**A. 通过 Docker CLI 执行的操作（占绝大多数）**

| 文件 | 职责 | 关键入口 |
|---|---|---|
| `runner.go` | 在已认证 SSH 连接上执行远程命令、统一超时、结果与错误分类；构造 `env DOCKER_API_VERSION=1.52 docker …` 与 `env -u DOCKER_API_VERSION …` | `Runner.Run` / `RunWithInput` / `RunQuery` / `RunMutate` / `dockerCommand` / `dockerProbeCommand` |
| `container.go` | 容器创建/启动/停止/删除/检查/所有权核验、`docker exec`（含 stdin）、`docker logs`、`docker cp`、`docker ps`、命名卷管理 | `CreateContainer`、`StartContainer`、`StopContainer`、`RemoveContainer`、`InspectContainer`、`VerifyOwnership`、`ExecCommandIn`、`ExecCommandInWithInput`、`ContainerLogs`、`CopyFromContainer`、`CopyToContainer`、`EnsureVolume`、`InspectVolume`、`RemoveVolume` |
| `image.go` | 构建上下文上传、`docker build`（日志 tee 到远程文件 + 尾部摘要）、`docker image inspect` | `UploadContext`、`BuildImage`、`BuildLogCommand`、`InspectImage`、`InspectImageID` |
| `context.go` | 构建上下文模型、哈希、内容校验（私钥/Docker Socket 特征） | `Context`、`NewContext`、`ReadContextDirectory` |
| `templates.go` | 内置基础与语言环境 Dockerfile、语言清单、Dockerfile 静态校验 | `DefaultBaseDockerfile`、`DefaultLanguageTemplates`、`ValidateDockerfile` |
| `clihelp.go` | `docker <cmd> --help` 能力核对与参数清单 | `RequiredCLICommands`、`CheckCLIHelp` |
| `preflight.go` | 节点预检：CLI 版本、Engine 版本、API 基准、工作空间、磁盘 | `Preflight`、`checkCLI`、`checkEngine`、`checkEngineAPI` |
| `version_probe.go` | CLI/Engine/API 三方版本探测与判定 | `ProbeVersion` |
| `manifest.go` / `layout.go` / `validation.go` | 远程镜像与容器登记清单（SFTP）、远程工作空间布局、校验超时集中定义 | — |
| `compatibility.go` / `errors.go` | 版本解析与比较、API 区间判定、错误码体系 | `CheckAPICompatibility`、`ParseVersion`、`ErrorCode` |

**B. 直连 Docker Engine HTTP API（本审计的全部对象）**

| 文件 | 职责 | 关键入口 |
|---|---|---|
| `engine_api.go` | 只读 `GET /v1.52/version`，产出 `APIVersionResult`，含 6 个状态常量（5 个失败分类）与错误分类 | `Runner.ProbeAPIVersion`、`classifyAPIDialError`、`classifyAPIProbeError` |
| `distribution.go` | 只读 `GET /v1.52/distribution/{name}/json`，产出 `DistributionResult`，含 9 个状态常量（8 个失败分类）与错误分类；并承载全部 HTTP 传输与解析基础设施 | `Runner.InspectDistribution`、`unixSocketTransport`、`parseDistributionResponse`、`platformLabel`、`urlPathEscape`、`classifySocketError`、`classifyQueryError`、`classifyDistributionStatus`、`distributionFailureMessage` |
| `internal/sshclient/unixsocket.go` | 通过 OpenSSH 的 `direct-streamlocal@openssh.com` 通道打开远端 Unix 套接字，返回 `net.Conn` | `UnixSocketDialer`、`client.DialUnixSocket` |

**关键事实：真正困难的部分不在 HTTP 客户端里。** HTTP 请求只有两条 GET；真正带项目特性的是
「SSH 通道 → 远端 `docker.sock` 的 `net.Conn`」（`sshclient.DialUnixSocket`）、错误分类体系、
以及"失败必须可诊断且不得静默回退"的判定规则。这三者都不属于任何 Docker Go SDK 的职责范围。

## 2. 当前直接 Engine API 调用清单

全仓只有 2 个 HTTP 请求，2 个调用路径，无其他 `net/http` 访问 Docker 的代码
（`git grep 'net/http' internal/docker` 只有 `distribution.go` 与 `engine_api.go`）。

| 调用 | 请求 | 入口 | 调用链 | 超时 | 响应体上限 |
|---|---|---|---|---|---|
| API 基准探测 | `GET /v1.52/version` | `Runner.ProbeAPIVersion(ctx, timeout)` | `ProbeVersion`（`version_probe.go`）/ `Preflight.checkEngineAPI`（`preflight.go`）→ `app.HostValidator.checkDockerVersion`、`app.InitService`（`agentflow docker init` / `agentflow docker preflight`） | 调用方给（默认 `DefaultDistributionTimeout` = 8s） | 1 MiB（`maxDistributionBodyBytes`） |
| 基础镜像元数据 | `GET /v1.52/distribution/{name}/json` | `Runner.InspectDistribution(ctx, ref, timeout)` | `app.HostValidator.checkBaseImageMetadata`（`host_validation.go`） | 调用方给（`ValidationImageTimeoutSeconds` = 8s） | 1 MiB（`maxDistributionBodyBytes`） |

符号级依赖关系（全仓搜索结果）：

| 符号 | 定义 | 引用位置 |
|---|---|---|
| `ProbeAPIVersion` | `engine_api.go` | `preflight.go:checkEngineAPI`、`version_probe.go:ProbeVersion` |
| `InspectDistribution` | `distribution.go` | `internal/app/host_validation.go` |
| `DialUnixSocket` | `sshclient/unixsocket.go`（接口 `UnixSocketDialer`） | `distribution.go`、`engine_api.go`（两处都是局部接口断言）、`docs/ssh-hosts.md` |
| `unixSocketTransport` | `distribution.go` | 同上两处 HTTP 调用 |
| `dockerAPIVersionPrefix` | `compatibility.go`（`"/v" + EngineAPIBaseVersion`） | `distribution.go`、`engine_api.go`、`docs/docker-compatibility.md` |
| `EngineAPIBaseVersion` | `compatibility.go`（`"1.52"`） | `runner.go`（CLI 环境变量）、`compatibility.go`、`preflight.go`、`version_probe.go`、`app/host_validation.go`、`cli/docker.go`、`docs/*`、`tests/*` |
| `DefaultDockerSocketPath` | `distribution.go`（`/var/run/docker.sock`） | `distribution.go`、`engine_api.go` |

## 3. SDK 的版本、维护情况与许可证

| 项目 | 结论 | 依据 |
|---|---|---|
| 模块路径 | `github.com/moby/moby/client`（独立模块），配套类型模块 `github.com/moby/moby/api` | `client@v0.6.2/go.mod` |
| 当前最新版本 | `v0.6.2`，发布 2026-10-08 | 模块代理 `.info` |
| 版本线 | 独立 0.x：`v0.1.0-alpha.0` → `v0.6.2`；2026 年内已发布 `v0.4.0`（04-03）→ `v0.6.2`（10-08）至少 4 个次版本 | `.info` 与 `releases/v0.1.0-beta.toml` |
| 稳定性声明 | `releases/v0.1.0-beta.toml` 明示"0.x 是因为 Go 接口仍有变更考虑" | 同上 |
| 已发生的接口变动 | `NewClientWithOpts`、`WithVersion`、`WithVersionFromEnv` 标记 Deprecated；`WithAPIVersionNegotiation` 已变为 no-op；`MaxAPIVersion` 在 `v0.4.0` 为 `1.54`、`v0.6.2` 为 `1.56` | `client.go`、`client_options.go` |
| Go 最低版本 | `go 1.24`（client 与 api 两个模块都是）；本机 `go1.24.5` 实测可编译 | 两个 `go.mod` + 实际 `go build` |
| 与本项目 Go 版本 | 兼容（`go.mod` 声明 `go 1.24.5`） | 同上 |
| 许可证 | Apache-2.0 | 模块内 `LICENSE` |
| 官方维护状态 | Moby 官方仓库子模块（`docker` CLI 自身使用该包），与 Engine 版本同源发布 | 模块 README 与 `releases/*.toml` |
| 支持的 API 区间 | 客户端 `MaxAPIVersion = "1.56"`、`MinAPIVersion = "1.40"`（后者只用于自动协商下限） | `client.go` |
| 旧模块 | 未使用、也不会使用已弃用的 `github.com/docker/docker` 旧模块 | — |

## 4. SDK 与 SSH Unix Socket 的适配分析

### 4.1 结论：可行，且注入点只有一处

`client.WithDialContext(func(ctx context.Context, network, addr string) (net.Conn, error))` 直接把函数写入
`http.Transport.DialContext`（`client_options.go`）：

```go
func WithDialContext(dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) Opt {
	return func(c *clientConfig) error {
		if transport, ok := c.client.Transport.(*http.Transport); ok {
			transport.DialContext = dialContext
			return nil
		}
		return fmt.Errorf("cannot apply dialer to transport: %T", c.client.Transport)
	}
}
```

现有 `sshclient.UnixSocketDialer`（`DialUnixSocket(ctx, socketPath) (net.Conn, error)`）语义完全覆盖
`net.Dialer` 的用法：接受 `ctx`、返回只属于自己那条通道的 `net.Conn`、`Close` 只释放该通道。适配只需要
一个闭包：

```go
dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
	return dialer.DialUnixSocket(ctx, docker.DefaultDockerSocketPath)
}
```

`network` 与 `addr` 必须忽略：`addr` 来自 `WithHost("unix:///var/run/docker.sock")` 解析出的套接字路径，
在 SSH 语义下没有意义。`WithHost` 同时把 `proto` 置为 `unix`，从而（`docker/go-connections/sockets`）：

- `DisableCompression = true`（Unix 本地通信不需要压缩）；
- 默认的 **本机** Unix 拨号器被写入 `DialContext`（下一个选项再用 `WithDialContext` 覆盖）；
- `req.Host` 被改写为 `client.DummyHost`（`api.moby.localhost`），不会真的去解析域名；
- `tr.Proxy` 保持 `nil`，不会读取 `HTTP_PROXY` 环境变量。

顺序必须固定：先 `WithHost`，后 `WithDialContext`。同时**不能**用 `WithHTTPClient` 塞入自定义
`RoundTripper`，否则 `WithDialContext` 直接返回 `cannot apply dialer to transport`，
且 `Client.Close()` 也会失效（`baseTransport` 为 nil，见 4.5）。

### 4.2 连接池：会建，且是行为差异

`New()` 用 `defaultHTTPClient()` 建 `*http.Transport`，并显式设置
`MaxIdleConns = 6`、`IdleConnTimeout = 30 * time.Second`（`client.go`），没有 `MaxConnsPerHost`：

- 单个请求：1 条 SSH 通道；
- 连续两次探测（间隔 30s 内）：**复用**同一条 SSH 通道，而不是像现在这样每次新建、用完即关；
- 并发请求：每个并发请求各开一条通道，空闲时最多保留 6 条，30 秒后由 transport 关闭；
- 复用前提：响应体被读到 EOF 并 `Close`；SDK 统一走 `ensureReaderClosed()`（最多再 drain 512 字节后
  `Close`），因此常规 JSON 响应都能复用。

对当前用法的含义：`ProbeAPIVersion` 与 `InspectDistribution` 在一次预检里各调一次，池化带来的收益接近
于零，却会让 SSH 连接上多挂最多 6 条空闲 `direct-streamlocal` 通道（最长 30 秒）。这些通道不是 SSH
session，不受 `MaxSessions` 限制，但**所有权必须交代清楚**：每次用完 SDK 客户端都要 `Close()`
（`CloseIdleConnections`），否则空闲通道会活到 SSH 连接关闭为止。

### 4.3 多个 HTTP 请求复用 SSH 连接

可以，但不是在一条通道上复用：SSH 连接本身被复用（`x/crypto/ssh.Client` 支持多通道），每条 HTTP
连接对应一条独立的 `direct-streamlocal` 通道。这与当前实现的区别只是"通道是否被池化"，安全性不变。

### 4.4 Context 取消与超时

- 请求上下文取消：`http.Transport` 的取消机制会关闭底层 `net.Conn`（`persistConn.close`），
  连接的 `Close` 就是 SSH 通道的 `Close`，因此取消会真正中断阻塞中的读写，并关闭该通道。
- **不需要** `SetDeadline`。SSH 通道的 `SetDeadline` 返回 "deadline not supported"，而
  `http.Transport` 本身也不依赖它实现取消（本项目自研 transport 里的 `ctx` 监听协程，正是为了补上
  "自定义 `RoundTripper` 不会自动获得取消行为"这一空缺；换成 `http.Transport` 后该空缺不存在）。
- 建连阶段：`DialUnixSocket` 自己包了 10s 上限并响应 `ctx`，超时后若通道迟建成功会立即关闭，
  不泄漏通道。
- hijack 路径（`ExecAttach` 等）不看 transport，而是 `cli.dialer()` → `baseTransport.DialContext`，
  并且 `setupHijackConn` 用 `context.AfterFunc(ctx, close(conn))` 显式关闭连接（`hijack.go`）。
  也就是说**同一个 `WithDialContext` 同时驱动普通请求与 hijack**，这一点对本审计很重要：将来若要把
  `docker exec` 迁到 SDK，SSH 适配层不需要重写。

### 4.5 关闭与所有权

- `Client.Close()` 只做 `baseTransport.CloseIdleConnections()`，然后返回；它**不关闭** SSH 连接、
  也没有后台协程。`baseTransport` 只在构造时 transport 为 `*http.Transport` 时被保存。
- SSH 连接的所有权仍完全属于 `sshclient.Client`：`Runner` 依然不持有连接所有权。
- 正确顺序：`engineClient.Close()`（释放池化通道）→ `sshClient.Close()`（关闭整条连接）。
  顺序反过来也不会泄漏（SSH 关闭会连带关掉通道），但会产生一批已死的池化连接，直到 transport 发现。

### 4.6 并发与泄漏

- 每个在飞请求占 1 条通道；请求结束（含取消、超时、错误）后通道要么回到池、要么被关闭，
  没有 SDK 侧 goroutine 常驻（只有一次性协商锁，固定版本下不触发）。
- 池化带来的唯一泄漏风险是"忘记 `Close()`"，表现为最多 6 条空闲通道活到 SSH 连接关闭。
- 需要注意 `http.Transport` 在"复用到的空闲连接已失效"时会**自动重试一次**可重放请求。
  SDK 的 `jsonEncode` 返回 `*bytes.Reader`，`http.NewRequestWithContext` 因此会设置 `GetBody`，
  带 JSON body 的 POST 也被视为可重放。这对只读 GET 无害，但一旦用 SDK 做写操作（`ContainerCreate`、
  `ContainerStart`），就出现了当前实现没有的"静默重试一次"语义。

## 5. API 1.52 固定版本分析

### 5.1 `WithAPIVersion("1.52")` 的真实行为

1. `WithAPIVersion` 只做格式校验（`parseAPIVersion`），把结果存入 `clientConfig.manualAPIVersion`；
   它**不**校验该版本是否 ≤ `MaxAPIVersion`（源码注释明确要求调用方自己保证）。
2. `New()` 在应用完全部选项后：
   `if envAPIVersion != "" { setAPIVersion(env) } else if manualAPIVersion != "" { setAPIVersion(manual) }`，
   `setAPIVersion` 写 `cli.version` 并置 `cli.negotiated = true`。
3. 每个请求经 `getAPIPath` 拼成 `/v<version><path>`，即 `/v1.52/...`；`getAPIPath` 里的
   `checkVersion` 因 `negotiated == true` 直接返回，不发任何协商请求。

### 5.2 是否关闭了自动协商、是否有绕过版本前缀的请求

- 协商关闭：是。协商只有一条路径（`Ping(NegotiateAPIVersion: true)` → `negotiateAPIVersion`），
  而 `Ping` 是否协商由调用方参数决定；`getAPIPath`/`requiresVersion` 里的 `checkVersion` 在固定版本下
  短路。
- 版本前缀例外：**只有 `/_ping`**。`ping()` 刻意用 `buildRequest(... path.Join(basePath, "/_ping"))`
  绕开 `getAPIPath`，因为协商阶段还不知道版本。固定版本下它不会被调用，除非我们自己显式调用
  `Ping`。全模块搜索确认没有第二个绕开 `getAPIPath` 的路径（`hijack.go` 用的是 `getAPIPath`）。
- `Client.ClientVersion()` 返回 `"1.52"`，可以用作构造后的自校验断言。

### 5.3 能否继续保留严格区间判定

可以，而且必须保留：`ServerVersion` 返回 `ServerVersionResult.APIVersion` / `MinAPIVersion`，
把它们喂给现有 `CheckAPICompatibility(APIRange{Maximum, Minimum})` 即可，判定规则
（`MinAPIVersion <= 1.52 <= APIVersion`）一行都不用改。SDK 自己不做这个判定，也**不会**因为不兼容而
降级或升级——固定版本下它只会照发 `/v1.52/...`，由 Engine 返回 `client version 1.52 is too new` 之类的
错误。

### 5.4 三个必须写进规则的前置条件

| 风险 | 说明 | 约束 |
|---|---|---|
| 忘记 `WithAPIVersion` | `New()` 默认 `version = MaxAPIVersion`（当前 `1.56`）且 `negotiated = false`，首个请求会触发 Ping 协商，可能**静默降级**到服务端版本 | 构造后必须断言 `ClientVersion() == docker.EngineAPIBaseVersion` |
| 误用 `client.FromEnv` / `WithAPIVersionFromEnv` | 会读取进程环境变量 `DOCKER_API_VERSION`，把"固定基线"变成"取决于运行环境"；且 `New()` 中 env 版本**优先于** `WithAPIVersion`（与 `clientConfig` 字段注释相反） | 禁止使用任何 `*FromEnv` 选项 |
| 依赖 `WithVersion` | 已标记 Deprecated | 只用 `WithAPIVersion` |

## 6. 两处 API 调用的逐项能力对照

状态记号：✅ 完整覆盖；🟡 覆盖但语义/细节有差异，需要适配层；❌ SDK 不提供，必须保留自研。

### 6.1 A：`ProbeAPIVersion`（`GET /v1.52/version`）

| 当前行为（`engine_api.go`） | SDK 能力 | 结论 |
|---|---|---|
| 固定路径 `dockerAPIVersionPrefix + "/version"` | `Client.ServerVersion(ctx, ServerVersionOptions{})` → `getAPIPath("/version")` = `/v1.52/version` | ✅ |
| 取 `ApiVersion` / `MinAPIVersion` / `Version` | `ServerVersionResult.APIVersion` / `MinAPIVersion` / `Version`（另有 `Os`/`Arch`/`Platform`/`Components`） | ✅ |
| 判定基准是否落在区间内 | SDK 不做；`CheckAPICompatibility` 原样保留 | ❌（保留自研） |
| 5 类失败状态：`api_version_incompatible` / `api_version_unsupported` / `docker_socket_unavailable` / `api_probe_timeout` / `api_probe_cancelled` | 上下文错误原样返回（`errors.Is(err, context.Canceled/DeadlineExceeded)`，`doRequest` 明确不解包）；连接失败统一包装为内部 `errConnectionFailed`，可用 `client.IsErrConnectionFailed(err)` 判定，底层 SSH 错误仍在链上；HTTP 状态经 `errdefs` 分类（404→`ErrNotFound`、500→`ErrInternal`、501→`ErrNotImplemented`，其余落 `cause.ErrUnexpectedStatus`） | 🟡 分类逻辑要基于新的错误链重写，`classifyAPIDialError` / `classifyAPIProbeError` 不能原样复用 |
| 超时（默认 8s）、取消 | `context.WithTimeout` + transport 关闭连接 | ✅ |
| 响应体上限 1 MiB | 错误响应体在 `checkResponseErr` 内被限制为 1 MiB；**成功响应体**用 `json.Decoder` 直接解码，没有上限 | 🟡 行为收紧了一点（`/version` 响应很小，风险低，但必须记录） |
| 只读、不改远端状态 | 单个 GET；只要不调 `Ping` 就不会有额外请求 | ✅ |

### 6.2 B：`InspectDistribution`（`GET /v1.52/distribution/{name}/json`）

SDK 签名与返回结构（`distribution_inspect.go` + `api/types/registry`）：

```go
type DistributionInspectResult struct{ registry.DistributionInspect }
type DistributionInspectOptions struct{ EncodedRegistryAuth string }

// registry.DistributionInspect
//   Descriptor ocispec.Descriptor   // MediaType, Digest(digest.Digest), Size, Platform *ocispec.Platform
//   Platforms  []ocispec.Platform   // Architecture, OS, OSVersion, OSFeatures, Variant, Features
```

| 当前行为（`distribution.go`） | SDK 能力 | 结论 |
|---|---|---|
| 只读 `GET /v1.52/distribution/{name}/json`，不拉层 | `Client.DistributionInspect(ctx, ref, DistributionInspectOptions{})` | ✅ |
| 空引用 → `invalid_image_reference` | 返回内部 `objectNotFoundError{object:"distribution"}`（`Error: No such distribution:`） | 🟡 语义从"参数非法"变成"未找到"；调用前的空引用校验必须保留 |
| `Descriptor.Digest` | `result.Descriptor.Digest`（类型 `digest.Digest`，需 `.String()`） | ✅ |
| `Platforms` 的 `OS`/`Architecture`/`Variant` | `result.Platforms[i].OS` / `.Architecture` / `.Variant`（`ocispec.Platform`） | ✅ |
| 平台列表为空时回退到 `Descriptor.Platform` | `result.Descriptor.Platform`（`*ocispec.Platform`） | ✅ |
| 去重并拼成 `os/arch[/variant]` 标签 | SDK 不做；`platformLabel` + 去重循环保留（约 40 行） | 🟡（保留自研） |
| `MediaType` / `Size` | `Descriptor.MediaType` / `.Size`（当前未使用） | ✅ |
| 镜像不存在 → `image_not_found`（404/`manifest unknown`） | 404 → `&httpError{errdef: errdefs.ErrNotFound}`，可用 `errdefs.IsNotFound(err)` | ✅ |
| 认证失败 → `registry_auth_failed` | 401→`ErrUnauthenticated`、403→`ErrPermissionDenied`；但仓库认证失败常见形态是 **500 + `unauthorized`/`denied` 文本**，SDK 只给 `ErrInternal` | 🟡 仍需消息匹配 |
| 仓库不可达 → `registry_unreachable` | 只能从 500/`ErrInternal` 或错误链文本推断 | 🟡 |
| 超时 → `metadata_timeout` | `ctx` 超时可直接判定；但 **504 不在 `errhttp.ToNative` 表里**，落 `cause.ErrUnexpectedStatus{Status:504}`，现有"504 → timeout"规则要另行实现 | 🟡 |
| 接口不兼容 → `metadata_api_unsupported` | 400→`ErrInvalidArgument`、501→`ErrNotImplemented`、405→`ErrUnexpectedStatus{405}` | 🟡 |
| 套接字不可用/权限不足 → `docker_socket_unavailable` / `registry_auth_failed` | SDK 包装为 `errConnectionFailed`，底层仍是我们 `DialUnixSocket` 的错误；`errors.Is(err, os.ErrPermission)` 分支只对本机 unix 有效，对 SSH 错误不适用 | 🟡 只能在适配层判定（沿用字符串匹配，或在 `DialUnixSocket` 上游加哨兵错误） |
| 响应体上限 1 MiB | 同 6.1：错误体受限，成功体无上限 | 🟡 |
| `200` 但无 `digest` → `metadata_api_unsupported` | SDK 返回空 `Digest` 与 `nil` error | ❌ 必须保留该检查 |
| `urlPathEscape` 转义镜像引用 | SDK 不做显式转义，但 `url.URL.String()` 会按 `Path` 规则转义；`path.Join` 会清理路径 | 🟡 可删，但属于"靠标准库隐式保证"，需要写清 |
| 不改变远端资源 | 单个 GET | ✅ |

**小结**：SDK 覆盖了"取字段"，没有覆盖"我们的判定"。两处调用共 13 个失败状态常量，其中只有 3 个能靠
SDK 的类型化错误直接映射（`image_not_found`、`metadata_cancelled`、`docker_socket_unavailable`；
最后一个仅指"连接失败"，无法再区分套接字不存在与权限不足），其余 10 个都要重写规则，
另有 1 条业务规则（200 无 digest）SDK 完全不提供。

## 7. Docker CLI 与 SDK 的职责划分建议

| 操作 | 现有实现 | 建议归属 | 理由 |
|---|---|---|---|
| API 基准探测（`/version`） | 直连 API | SDK 可覆盖（见 6.1），但收益见第 9、13 节 | 只读、无副作用、字段简单 |
| 基础镜像元数据（`/distribution/...`） | 直连 API | SDK 可覆盖（见 6.2） | 只读、无副作用 |
| 容器 create/start/stop/rm | CLI | **保留 CLI** | 有副作用；SDK 会在陈旧空闲连接上自动重试可重放请求（4.6），且现有实现已围绕"结果不确定 → 先核验现场"建立了完整语义（`CodeStateUncertain`），迁移等于重写状态判定 |
| 容器 inspect / ps | CLI | **保留 CLI** | 与 `VerifyOwnership` 的标签核验、`domain.ParseDockerContainerState` 紧耦合；换成结构化 API 收益小、回归面大 |
| 容器 exec（含 stdin） | CLI | **保留 CLI** | SDK 需 `ExecCreate` + `ExecAttach`（hijack）+ `stdcopy` 解复用 + `ExecInspect` 取退出码，且 `ExecInspect` 的 `ExitCode` 是"当前值"而非"本次结果"；现有实现已用 SSH 会话语义表达取消/超时/结果不确定（`TerminationConfirmed`）。虽然 4.4 已证明 hijack 走 `WithDialContext`，但这是"可行"不等于"值得" |
| 容器日志 | CLI | **保留 CLI** | SDK 返回多路复用流，需要 `stdcopy`；现有实现的有界输出、截断标记与 `--tail` 语义要全部重做 |
| `docker cp` | CLI | **保留 CLI** | SDK 是 tar 流，与现有"宿主机路径 ↔ 容器路径"语义不同 |
| 镜像构建 | CLI | **保留 CLI**（明确不要迁移） | 现有构建依赖 `docker build --progress plain` + `tee` 到远程日志 + `PIPESTATUS` 透传 + 尾部摘要。SDK 的 `ImageBuild` 不支持 CLI 的 BuildKit 会话（`SessionID`/`Outputs` 需要自行实现 session），`--progress` 是 CLI 侧概念，`BuildArgs`/`NoCache`/`RemoteContext`/`Labels` 虽在 `ImageBuildOptions` 里都有，但拿不到同样的日志落盘与进度语义 |
| 镜像 inspect / 卷管理 | CLI | **保留 CLI** | 与 `RequiredCLICommands` 的 `--help` 能力核对、构建哈希核验绑定 |
| CLI 版本预检 / `--help` 检查 | CLI | **不可替换** | 本质是 CLI 能力检查，SDK 无对应能力 |

结论：**SDK 的适用范围只有当前那两条只读请求**；其余全部保留 CLI。若引入 SDK，则必须同时维护
"CLI 路径 + SDK 路径"两套 Docker 访问方式，与项目"不保留双实现"的政策直接冲突（见第 8 节论断）。

## 8. 新增依赖的维护成本与依赖规模

### 8.1 依赖规模（实测）

| 指标 | 数值 | 来源 |
|---|---|---|
| `client` 模块直接 require | 13 个 | `client@v0.6.2/go.mod` |
| `client` 模块间接 require | 9 个 | 同上 |
| 引入后**构建图**需要的模块 | 19 个（其中 `golang.org/x/sys` 本项目已有，实际新增 **18 个**） | 在空模块中 `go list -deps github.com/moby/moby/client` 后按模块去重 |
| 该包的包级依赖 | 245 个包（含标准库），其中外部包 86 个 | 同上 |
| 完整模块图（含依赖的测试依赖） | 49 个模块 | `go list -m all` |
| 强制引入的重量级组件 | OpenTelemetry（`otel`、`otel/trace`、`otel/metric`、`auto/sdk`、`contrib/.../otelhttp`）、`containerd/errdefs`(+`pkg`)、`docker/go-connections`、`distribution/reference`、`opencontainers/image-spec`、`opencontainers/go-digest`、`moby/moby/api`、`Microsoft/go-winio` | 依赖列表 |

关键点：**OpenTelemetry 不是可选项**。`New()` 无条件执行
`c.client.Transport = otelhttp.NewTransport(...)`，`setupHijackConn` 同样如此；即使我们完全不配置
trace，也要把这条包装链、`otel` 全局 provider 与相关模块一起带进来。相比之下，本项目当前的直接依赖
只有 6 个（`pkg/sftp`、`spf13/cobra`、`x/crypto`、`x/term`、`yaml.v3`、`modernc.org/sqlite`）。

### 8.2 维护成本

| 成本项 | 说明 |
|---|---|
| 版本跟进门槛 | 模块处于 0.x：半年内 4 个次版本、`MaxAPIVersion` 从 1.54 升到 1.56、4 个构造函数/选项被弃用。这意味着**每次升级都要重新审计接口变动**，而我们的 API 基准（1.52）不会随之变化 |
| 升级与基线耦合 | SDK 的 `MaxAPIVersion` 跟 Engine 发布节奏走；我们的基线是 1.52。未来一旦引入 SDK 做写操作，为了拿到新字段会被迫跟着抬基线 |
| 新增供应链面 | 18 个模块（含 OpenTelemetry 生态、containerd、docker 组织下多个仓库）进入 `go.sum`，需要额外的许可证与来源核查（Apache-2.0 为主，已核对 client 本体） |
| 双路径认知成本 | 同一次预检里，读走 SDK、写走 CLI，错误码、超时、取消与"结果不确定"的语义需要两套解释 |
| 替代收益 | 见第 9 节：净减少约 150 行机械 HTTP 代码，且分类逻辑要重写 |

## 9. 可能删除的自研代码

若按 6.1/6.2 迁移这两条请求，可删除的部分（均为机械管道，不含业务判定）：

| 文件 | 符号 | 说明 |
|---|---|---|
| `internal/docker/distribution.go` | `unixSocketTransport`、`RoundTrip`、`contextError` | 约 60 行；换由 `http.Transport` + `WithDialContext` 承担 |
| `internal/docker/distribution.go` | `distributionResponse`、`parseDistributionResponse` | 约 60 行；换由 `registry.DistributionInspect` 解码 |
| `internal/docker/distribution.go` | `urlPathEscape` | 约 25 行；换由 `url.URL.String()` 转义 |
| `internal/docker/distribution.go` | `InspectDistribution` 内的手工 HTTP 段落（建通道 → 建 client → 建 request → `client.Do` → 限长读取 → 状态码分支） | 约 60 行，替换为约 15 行 SDK 调用 |
| `internal/docker/engine_api.go` | `engineVersionResponse`、`ProbeAPIVersion` 内的手工 HTTP 段落 | 约 60 行，替换为约 15 行 `ServerVersion` |
| `internal/docker/distribution.go` | `classifySocketError`、`classifyQueryError`、`classifyDistributionStatus`、`distributionFailureMessage` | **不能净删除**：必须基于 `errdefs` / `IsErrConnectionFailed` / `ctx` 重写，代码量基本不变，且要额外处理 504 与 401/403/500 的文本匹配 |

净减少量级：约 150–200 行（`internal/docker` 现有约 4.7 千行 Go 代码，公开测试目录不计），
其中不含仍要继续维护的 13 个失败状态常量、2 个结果结构与全部判定规则。

不会被删除、也不可能被 SDK 替代的部分同样明确：`sshclient/unixsocket.go`（84 行）仍是 SDK 的依赖方；
`sshclient/session.go`、`dialer.go`、`hostkey*.go` 与其全部约束（主机密钥校验、known_hosts 类型协商、
取消/超时语义）不受影响。

## 10. 必须保留的自研代码

无论是否引入 SDK，以下内容都必须保留，且不得下沉到第三方包：

1. `sshclient.DialUnixSocket` + `UnixSocketDialer`：SDK 只消费 `net.Conn`，SSH 侧实现仍是我们的责任；
   不开放 Docker TCP 端口、不做反向隧道的约束也仍然成立。
2. 版本与区间判定：`ParseVersion`、`CheckMinimumVersion`、`CheckAPICompatibility`、`APIRange`、
   `EngineAPIBaseVersion`（唯一来源）与 `dockerAPIVersionPrefix`。
3. 结果与状态模型：`APIVersionResult` / `DistributionResult` / `DistributionInspect` 以及
   `APICheckCode*`、`MetadataCode*` 常量族，它们是 `domain.ValidationCheck` 与
   `domain.ImageMetadataResult` 的输入契约。
4. `summarizeOutput`（末尾若干行 + 600 字符裁剪）与所有"不得写入凭据"的裁剪规则。
5. 全部 CLI 相关实现：`dockerCommand` / `dockerProbeCommand`（`DOCKER_API_VERSION` 注入与隔离）、
   `Runner`、`RequiredCLICommands`、`Preflight`、`BuildLogCommand`、`ExecCommand*`、
   容器状态与所有权核验。
6. `200` 但缺少 digest 的"不算成功"判定（SDK 空缺，见 6.2）。

## 11. 替换实施步骤（仅在结论变为"替换"时执行）

1. 固定版本引入：`go get github.com/moby/moby/client@<审计通过的版本>` 与
   `github.com/moby/moby/api@<对应版本>`，在 `go.mod` 中显式 require 两个模块（`api` 不要留成间接依赖，
   因为我们要直接用 `registry.DistributionInspect` / `ocispec.Platform`）。
2. 新增 `internal/docker/engine_client.go`，只做三件事：
   - `newEngineClient(dialer UnixSocketDialer) (*client.Client, error)`：
     `client.New(client.WithHost("unix://"+DefaultDockerSocketPath), client.WithAPIVersion(EngineAPIBaseVersion),
     client.WithDialContext(adapter(dialer)))`；**不使用任何 `*FromEnv` 选项**；
     构造后断言 `apiClient.ClientVersion() == EngineAPIBaseVersion`，不等则返回错误。
   - `adapter(dialer)`：返回忽略 `network`/`addr`、调用 `dialer.DialUnixSocket(ctx, DefaultDockerSocketPath)` 的闭包。
   - 生命周期：`Runner` 增加可选字段 `engine *client.Client` 与 `Close()`；`Runner` 仍不拥有 SSH 连接，
     关闭顺序为 SDK → SSH。
3. 改写 `ProbeAPIVersion`：`apiClient.ServerVersion(ctx, client.ServerVersionOptions{})`；把
   `APIVersion`/`MinAPIVersion` 交给 `CheckAPICompatibility`，状态码与消息构造保持不变。
4. 改写 `InspectDistribution`：调用前保留空引用校验；`apiClient.DistributionInspect(ctx, ref,
   client.DistributionInspectOptions{})`；`Digest = result.Descriptor.Digest.String()`；
   平台标签仍由 `platformLabel` + 去重循环生成；保留"digest 为空即不算成功"。
5. 重写错误分类（新增 `classifyEngineError(ctx, err) string`）：
   `errors.Is(ctx.Err(), context.DeadlineExceeded)` → timeout；`context.Canceled` → cancelled；
   `errdefs.IsNotFound` → not found；`errdefs.IsUnauthenticated`/`IsPermissionDenied` → auth failed；
   `errdefs.IsNotImplemented` → unsupported；`client.IsErrConnectionFailed` → socket unavailable
   （并在适配层把 `DialUnixSocket` 的失败原因区分为"套接字不可用"与"权限不足"，最好在 `sshclient`
   增加哨兵错误而不是继续字符串匹配）；`cause.ErrUnexpectedStatus{Status:504}` 与 500 文本匹配 →
   timeout / registry_unreachable / auth failed。
6. 删除第 9 节列出的代码，保持状态常量与结构体签名不变（`app`/`domain` 不需要改动）。
7. 同步文档：`docs/docker-compatibility.md` §5（改为"经由 SDK 的 /v1.52/ 路径"）、
   `docs/ssh-hosts.md`（`DialUnixSocket` 的消费方从自研 transport 改为 SDK 的 `DialContext`）。
8. 人工核对：构造选项顺序、`ClientVersion()` 断言、`Close()` 调用点、空闲通道数量上限、
   504/401/403/500 四类错误的实际映射。**本次不运行任何测试。**

## 12. 可能存在的风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 0.x 接口继续变动 | 升级时编译失败或语义变化（`WithAPIVersionNegotiation` 已由"开启协商"变成 no-op） | 固定版本、升级前逐文件复核接口差异 |
| 忘记 `WithAPIVersion` 或误用 `*FromEnv` | 静默协商到 1.56 或跟随进程环境变量，破坏"固定 1.52"政策 | 构造后断言 `ClientVersion()`；禁止 `*FromEnv` |
| 连接池化 | 最多 6 条空闲 SSH 通道存活 30s；忘记 `Close()` 时更久 | 每次 Runner 生命周期结束显式 `Close()` |
| 自动重试 | `http.Transport` 对可重放请求在陈旧连接上重试一次；带 JSON body 的 POST 也可重放 | 只把只读 GET 迁到 SDK，写操作继续走 CLI |
| 错误语义迁移 | 504 无 typed 映射；仓库认证失败常为 500+文本；套接字权限在 SSH 错误链上无 `os.ErrPermission` | 重写分类并逐类人工核对；给 `sshclient` 加哨兵错误 |
| 成功响应无大小上限 | 大响应（多平台清单）不再被 1 MiB 截断 | 保留 `maxDistributionBodyBytes` 的等价保护（如包一层 `io.LimitReader` 的自定义解码），或记录为可接受差异 |
| OpenTelemetry 常驻依赖 | 依赖面扩大、`otel` 全局 provider 存在 | 接受（或在使用前显式 `WithTraceProvider(noop)`) |
| 双路径并存 | 同一次预检里读走 SDK、写走 CLI；违反"不保留双实现"政策 | 要么彻底迁移（不现实），要么不引入（本审计结论） |
| 行为对比缺失守点 | 替换后无自动化测试覆盖两处调用的状态分类 | 依赖人工核对 + 真实节点预检（需用户明确授权） |

## 13. 最终结论

**不值得替换。**

理由按重要性排序：

1. **SDK 替代不了真正的自研资产。** 两条请求都是无 body 的只读 GET；真正带项目特性的是
   `sshclient.DialUnixSocket`（SSH `direct-streamlocal` 通道）、13 个失败状态常量与"不得静默降级/回退"的
   判定规则。前者 SDK 不提供（它只消费 `net.Conn`），后两者 SDK 也不提供（见第 6 节：13 个失败状态里 10 个
   要重写，另有 1 条规则 SDK 完全没有）。
2. **收益只有约 150–200 行机械 HTTP 管道，成本是 18 个新模块 + 一个 0.x 模块的持续跟版。**
   其中包含无条件引入的 OpenTelemetry 生态，与本项目"直接依赖只有 6 个、优先标准库"的现状明显不符。
3. **会制造双实现。** 容器 create/exec/logs/cp/build/volume 无论从功能、日志语义还是副作用安全看都应
   继续走 CLI（第 7 节逐项论证），于是 Docker 访问会被切成"读走 SDK、写走 CLI"两条路径，
   与项目"不保留没有实际使用价值的双实现"的政策冲突，而这个双实现的收益仅是两条只读请求。
4. **版本固定目标不需要 SDK 才成立。** 现有实现已经用 `/v1.52/` 前缀把基准钉死；`WithAPIVersion`
   能做的事只是用另一种写法做同一件事，同时新增两个静默降级入口（漏传选项、误用 `*FromEnv`）。
   反过来说，若引入 SDK 还必须额外加"构造后断言 `ClientVersion()`"这样的守卫，净复杂度没有下降。

**同时明确：SSH 传输适配本身没有障碍。** 如果结论在未来改变，第 4 节已经证明
`WithDialContext` + `sshclient.DialUnixSocket` 足以覆盖普通请求与 hijack 两条路径，取消/超时会关闭
通道，`Client.Close()` 能释放池化通道，SSH 连接所有权仍在我们手里——不需要开放 Docker TCP 端口、
不需要反向隧道、也不需要放松主机密钥校验。

**结论的复审触发条件**（满足任一条再重新评估，不要凭直觉引入）：

1. AgentFlow 决定把容器生命周期、`docker exec`、容器日志或镜像构建迁移到 Engine API（此时需要 hijack、
   `stdcopy`、结构化 inspect、tar 流，自研成本远高于现在，SDK 价值成立）。
2. 直连 Engine API 的调用从 2 条增长到 5 条以上，或出现需要 body / 流式响应 / 版本分叉的接口。
3. Moby 发布 `v1.0.0` 且明确宣布 Go 接口稳定，同时 `MaxAPIVersion` 与我们的基线收敛到同一版本。
