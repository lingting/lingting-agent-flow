package app

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/execenv"
)

// PlanningContainerSpec 是只读规划容器的创建参数。
//
// 它只由内部受控调用方（规划运行时）构造：普通任务容器的创建请求不带该字段，
// 因此 HTTP、CLI 与任务执行链路都无法让任务容器获得只读 rootfs、只读项目目录
// 或额外的可写临时目录。
type PlanningContainerSpec struct {
	// CodeVolumeName 是承载项目代码快照的只读命名卷。
	CodeVolumeName string
	// Workspace 是只读工作区语义：项目代码目录只读，可写目录显式声明。
	Workspace execenv.Workspace
}

// Validate 校验规划容器参数。
//
// 校验在创建任何资源之前完成：非法的只读参数不会先创建缓存卷或容器登记记录。
// 它同时拒绝可写缓存卷，避免规划容器通过缓存挂载获得项目代码写权限。
func (p PlanningContainerSpec) Validate(caches []string) error {
	if err := p.Workspace.Validate(); err != nil {
		return fmt.Errorf("规划容器工作区非法: %w", err)
	}
	if !p.Workspace.ReadOnly() {
		return fmt.Errorf("%w: 规划容器必须使用只读工作区", execenv.ErrInvalidValue)
	}
	if strings.TrimSpace(p.CodeVolumeName) == "" {
		return fmt.Errorf("%w: 规划容器必须声明项目代码快照卷", execenv.ErrInvalidValue)
	}
	if len(caches) != 0 {
		return fmt.Errorf(
			"%w: 规划容器不能挂载可写缓存卷，否则会绕过项目代码只读限制",
			execenv.ErrInvalidValue,
		)
	}
	return nil
}

// apply 把只读工作区语义转换为 Docker 创建参数。
//
// 转换规则固定：rootfs 只读、项目代码快照卷只读挂载到工作区项目目录、工作区显式
// 声明的可写目录转换为临时目录。调用方必须先执行 Validate，这里不再重复校验。
func (p PlanningContainerSpec) apply(spec *docker.CreateContainerSpec) {
	spec.WorkDir = p.Workspace.Project
	spec.ReadOnlyRootFS = true
	spec.CodeVolumes = []docker.ReadOnlyVolumeSpec{
		{
			VolumeName:    strings.TrimSpace(p.CodeVolumeName),
			ContainerPath: p.Workspace.Project,
		},
	}
	tmpfs := make([]docker.TmpfsSpec, 0, len(p.Workspace.WritableDirs))
	for _, dir := range p.Workspace.WritableDirs {
		tmpfs = append(tmpfs, docker.TmpfsSpec{Target: dir})
	}
	spec.Tmpfs = tmpfs
}
