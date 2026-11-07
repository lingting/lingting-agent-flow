// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 本文件提供受控容器观测结果的构造入口：测试替身必须同时给出 Docker 的 State.Status 与
// State.Running，不能只设置 Running，否则判定会退化成"看一个布尔值"，与真实 docker inspect
// 的语义不一致（paused 与 restarting 容器同样报告 Running=true）。
package testkit

import (
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
)

// ContainerInspectFor 构造一份"运行中 / 未运行"两个字段自洽的受控检查结果。
//
// dockerID 可以为空：只按名称核验的替身不需要容器 ID。需要构造暂停、重启中或其他字段矛盾的
// 现场时，直接写 docker.ContainerInspect 字面量，不要用本函数伪造。
func ContainerInspectFor(dockerID string, running bool) *docker.ContainerInspect {
	state := domain.DockerStateExited
	if running {
		state = domain.DockerStateRunning
	}
	return &docker.ContainerInspect{ID: dockerID, State: state, Running: running}
}
