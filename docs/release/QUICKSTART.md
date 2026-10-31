# AgentFlow 快速启动

AgentFlow 是本地优先的 AI 编码工作流编排器。发布制品是**单个可执行文件**：Web 界面、
HTTP API 与后台服务由同一个进程提供，前端页面已通过 `//go:embed` 编译进程序，因此
运行不需要 Node.js、前端 `dist/` 目录或额外的静态文件服务器。

## 1. 解压

```bash
# Linux / macOS
tar -xzf agentflow_vX.Y.Z_linux_amd64.tar.gz

# Windows（PowerShell）
Expand-Archive agentflow_vX.Y.Z_windows_amd64.zip -DestinationPath .
```

解压后目录内容：

```text
agentflow            可执行文件（Windows 为 agentflow.exe）
QUICKSTART.md        本文件
THIRD-PARTY-NOTICES.md  第三方依赖声明与许可证
version.json         版本、提交、构建时间、Go 版本与目标平台
```

## 2. 核对版本

```bash
./agentflow version
```

输出包含版本号、提交 SHA 与构建时间。可用 `version.json` 与同一版本的其他制品交叉核对：
`commit` 必须与构建该制品的源码提交一致。

## 3. 启动

```bash
./agentflow server
```

服务默认监听 `127.0.0.1:26380`，启动后浏览器打开 <http://127.0.0.1:26380> 即可使用。
需要其他地址或端口时：

```bash
./agentflow server --address 127.0.0.1:26381
```

可执行文件可以放在任意目录并从该目录启动，不依赖源码目录、前端产物或当前工作目录。

## 4. 配置文件

首次启动会在数据目录创建默认配置与 SQLite 数据库。需要指定配置时：

```bash
./agentflow server --config ./agentflow.yaml
```

## 5. 运行前提

- 可执行文件本身不需要 Go、Node.js 或任何系统库。
- 需要执行远程任务的机器必须能被 SSH 访问，并装有 Docker CLI/Engine 29.0.0 及以上；
  这些是本机运行前置条件，不是发布制品的编译依赖。
- 真实 Pi Agent 运行在任务容器内，不在宿主机执行。

## 6. 校验制品

同一版本的制品都附带 `SHA256SUMS`：

```bash
# Linux
sha256sum -c SHA256SUMS

# macOS
shasum -a 256 -c SHA256SUMS
```

Windows 上可用 `Get-FileHash <文件名> -Algorithm SHA256` 与清单中的摘要比对。
