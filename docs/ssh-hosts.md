# SSH 与执行节点

本文件描述执行节点（`execution_hosts`）的连接层规则、环境预检语义与远程工作空间约束。Docker 版本
基线与命令清单见 [docker-compatibility.md](docker-compatibility.md)；执行环境容器边界见
[architecture.md](architecture.md)。

## 1. 两个独立的边界

- SSH 探测（`sshprobe`）只验证 TCP 可达性与 SSH 协议标识，属于网络层检查。
- SSH 认证与远程操作（`sshclient`）属于另一个边界：只有完成认证后才能执行远程命令、传输文件或
  管理远程 Docker。
- 两者不得合并：探测成功不得被当作认证成功，也不得据此开放远程操作路径。探测结果中的
  `Authenticated` 恒为 `false`，用于阻止调用方误判。

## 2. 连接层职责

`sshclient` 内部按职责划分：私钥认证（`AuthConfig` / `buildAuthMethods`）、主机密钥校验
（`HostKeyStore` / `KnownHostsStore`，只读解析 known_hosts，并按记录提供主机密钥类型
`HostKeyTypeProvider`）、连接管理（`Dialer` / `Client`）、远程执行（`Run` / `CommandResult`）、SFTP
传输（`FileTransfer`）、远程工作空间（`ResolveHome` / `ResolveWorkspace` / `EnsureWorkspace`）与远端
Unix 套接字通道（`DialUnixSocket`）。它不依赖 `cli`、`storage` 或 `app`，业务代码通过接口注入依赖，
不在各处直接创建 SSH 客户端。

## 3. 主机密钥校验

- 校验不可关闭：`sshclient.NewDialer` 未收到 `HostKeyStore` 时直接失败，任何位置都不得引入
  `ssh.InsecureIgnoreHostKey()` 等价实现。
- 类型协商按 known_hosts 中该主机已记录的类型逐个尝试握手，取第一个校验通过的结果；某个类型校验
  失败或服务端不提供该类型时换下一个。不得只把全部已记录类型一次性交给库协商：服务端会选中第一个
  共同算法，使"旧记录已失效、新记录仍有效"的主机直接失败。没有记录时才交给库默认协商。
- 逐类型重试只针对主机密钥类失败（未知、已变化、已吊销、无共同算法）；TCP 不可达、超时、取消与
  认证失败必须立即返回，不得继续换类型掩盖真实原因。
- 未知主机必须返回 `CodeHostKeyUnknown` 并给出算法与 SHA-256 指纹；密钥变化必须返回
  `CodeHostKeyMismatch` 并给出已信任记录，不得静默接受或自动覆盖。
- known_hosts 全程只读：`KnownHostsStore` 只实现 `Check`，不创建、不追加、不加锁、不写临时文件，
  因此并发校验无需互斥，也不会与用户编辑 known_hosts 互相干扰。信任记录由用户在 AgentFlow 之外
  维护（例如用 OpenSSH 追加已核验的密钥）；`host trust` 只把核验过的指纹写进执行节点，并提示用户
  自行写入 known_hosts。

## 4. 认证与凭据

- 认证方式固定为无口令私钥：`domain.AuthMethod` 与 `sshclient.AuthMethod` 只有 `private_key` 一个
  取值，节点配置必须给出 `private_key_path`。不得重新引入 SSH Agent、密码认证或私钥口令
  （passphrase）支持，也不得注册 `--password-stdin`、`--passphrase-stdin`、`--ask-pass` 等凭据输入参数。
- 私钥文件只被读取：不得复制到工作空间或远程机器、写入数据库或日志，也不得修改其权限或内容。
  加密私钥必须返回明确的不支持错误，而不是挂起等待口令输入。
- 节点记录只保存连接目标、本机私钥文件路径、远程工作空间、主机密钥信任信息与预检状态，
  **不保存任何凭据材料**。
- 存量数据库中 `auth_method` 为 `agent` 或 `password` 的记录保留原值，不写迁移改写；这类节点在校验
  时因认证方式不受支持而判为 `abnormal`，由用户显式改成私钥。
- 日志中不得出现私钥内容、认证头或其他凭据材料；校验错误与日志必须截断并脱敏，不得写入密码、
  令牌、私钥内容、认证头或完整环境变量。

## 5. 远程命令与传输

- 远程命令以结构化命令描述（程序名 + 参数数组）构造，参数统一经过 POSIX Shell 转义，禁止把用户
  输入直接拼接进命令字符串。
- 超时与取消只关闭对应的远程会话，不关闭整条连接，避免一个命令影响同连接上的其他操作。取消与超时
  不仅要停止本端等待，还要关闭远程会话通道并释放本端 goroutine；无法确认远程进程已终止时，必须在
  结果中如实标记。
- 远端 Unix 套接字通道（`DialUnixSocket`）用于访问远端 Docker Engine。SSH 通道不支持 `SetDeadline`，
  截止时间只能通过关闭通道生效；自定义 HTTP 传输必须显式监听请求上下文，否则取消与超时不会真正中断
  查询。
- SFTP 上传与下载都使用同目录临时文件 + 校验 + 重命名，失败时清理临时文件但不得留下最终文件；
  覆盖需要调用方显式声明。
- 远程路径只使用 POSIX 规则（`NormalizeRemotePath`），不得把本机 `filepath` 语义套用到远程路径，
  也不得用字符串前缀判断目录包含关系。

## 6. 远程工作空间

- 远程工作空间默认 `~/.lingting/agentflow`，`~` 指远程 SSH 登录用户的 Home，路径遵循 POSIX 语义。
- SFTP 不会展开 `~`，因此必须先通过已认证连接取得远程 Home，再把配置解析为远程绝对路径。
- 目录创建只在解析出的工作空间路径内进行，且必须显式授权。

## 7. 执行节点环境预检

预检状态只有 `checking`、`normal`、`abnormal` 三种，定义在 `domain.ValidationStatus`；单项结果使用
`domain.CheckStatus`。不得新增第二套总体状态名，也不得把 `checking` 显示为成功。

### 状态与代次

- 新建节点一律由服务端置为 `checking` 并提交后台校验，客户端不能指定 `validation_status`、
  `validation_generation` 等校验字段。
- 任何影响连接目标、认证方式、私钥路径、远程工作空间或启用状态的修改都必须调用
  `InvalidateValidation`：状态回到 `checking`、清空旧摘要与详情、代次加一。
- 校验结果必须通过 `UpdateExecutionHostValidation` 以代次为条件写入；代次不一致时丢弃结果，不得
  覆盖新配置。`RecordValidationResult` 与 `FailValidation` 同样校验代次。

### 检查项

四项必需检查按顺序执行，前一项失败时后续检查记为 `not_run`，不得伪装成已验证：

1. **SSH 连接**：握手、认证、执行一条无副作用的非交互远程命令。
2. **Docker 版本与 API 兼容性**（`docker_version`）：CLI 版本、Engine 版本分别不低于基线，固定 API
   `1.52` 落在 Engine 声明的版本区间内。
3. **Docker CLI 帮助信息**（`docker_cli_help`）：逐条核对 `docker.RequiredCLICommands` 中的命令与
   参数选项。
4. **基础镜像元数据**（`base_image_metadata`）：解析基础 Dockerfile，查询外部 `FROM` 镜像的元数据。

Docker 相关判据与允许执行的命令见 [docker-compatibility.md](docker-compatibility.md)。

### 结果语义

- 只有当前代次全部必需检查都通过才允许写入 `normal`；`skipped` 与 `not_run` 一律不算通过。
- 无法开始校验（调度失败、服务未运行等）时不得永久停留在 `checking`：要么保持可恢复的待校验状态
  并由启动恢复重新调度，要么收敛为 `abnormal` 并给出可读原因。
- `normal` 的含义仅限本方案实际执行的检查：Docker 兼容性基于实际 CLI / Engine / API 版本与 CLI
  帮助信息，不验证 Engine 的实际运行契约，因此不得把 `normal` 表述为"容器创建与运行已验证"。
- 后台校验作为 `agentflow server` 的后台服务注册进 `internal/lifecycle`：由统一 Server 启动与停止，
  不得在 HTTP Handler 内创建不受管理的长期 goroutine。停止时必须取消在途校验并等待其释放 SSH 连接；
  启动时必须重新调度停留在 `checking` 的记录。
- 任何使用持久化执行节点执行真实远程操作的入口都必须经过 `app.EnsureHostUsableForRemoteTask`
  （`withRemoteSession` 已统一调用）；不得只依赖 Web UI 禁用按钮。只有校验本身与只读预检允许在节点
  未通过校验时执行。

## 8. 命令边界

命令清单见 [cli.md](cli.md)，其中需要区分的语义是：

- `host probe` 只验证 TCP 可达性与 SSH 协议标识，**不执行身份认证**。
- `host trust` 只探测主机密钥并展示指纹，把指纹记录到执行节点必须显式给出已核验的 `--fingerprint`
  与非交互场景的 `--yes`；命令只写节点记录，不写 known_hosts。
- `host test`、`host workspace`、`host transfer`、`host validate` 执行完整的主机密钥校验与身份认证，
  是唯一可以执行远程命令与文件传输的路径。
- `host validate` 同步执行一次环境预检，退出码非零表示未通过；它不受"节点必须为 normal"的准入限制。
