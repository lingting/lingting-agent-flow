// Package execenv 定义与具体执行环境实现无关的统一执行环境契约。
//
// 本包只包含纯类型与纯函数：
//   - 不依赖 Docker、SSH、SQLite 或任何外部进程；
//   - 不感知任务状态机，不推进任何业务状态；
//   - 不持有连接、容器或子进程。
//
// 具体实现位于 app 层（远程 Docker 适配器），由它把容器准备、命令执行与
// 资源释放翻译为 Docker 与 SSH 操作。这样 Pi Runner、验证引擎与工作流只依赖
// 本包的接口，不会直接拼 Docker 命令，也不会在宿主机当前目录隐式执行任务代码。
//
// 工作区边界由 Workspace 统一表达：
//   - Project 是容器内项目代码目录（默认 /workspace/project）；
//   - SessionDir 是容器内临时 Session 目录，容器删除后随之消失；
//   - ManagementRoot / ProjectManagement 是远程 AgentFlow 管理工作区，
//     只存放项目配置、Dockerfile 与镜像构建资源，不得存放项目源代码。
package execenv
