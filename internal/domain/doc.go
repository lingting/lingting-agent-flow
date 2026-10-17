// Package domain 定义 AgentFlow 的核心实体、状态与业务规则。
//
// 本包只描述业务语义，不依赖数据库、HTTP、Agent 运行时或任何外部进程。
// 持久化、调度与工作流编排都必须在其他包中实现，并通过本包的实体表达状态。
package domain
