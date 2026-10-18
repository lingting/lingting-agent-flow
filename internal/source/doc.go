// Package source 把项目代码获取到任务容器内部的工作目录。
//
// 本包只负责"把代码放进容器并初始化任务分支"：
//   - 代码一律克隆到容器内部目录，不经过本机数据目录，也不使用宿主机 bind mount；
//   - 所有命令通过 ContainerExecutor 在目标容器内执行，业务层不拼接 SSH 或 Docker
//     命令字符串；
//   - 容器归属与运行状态由 ContainerGuard 核验，本包不擅自启停或删除容器。
//
// 本包不实现 Push、Merge 或主分支更新策略，也不决定容器何时删除。
package source
