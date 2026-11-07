package planning_remote

import (
	"encoding/json"
	"fmt"
	"strings"
)

// rawContainerInspect 是 `docker inspect` 中只读环境验收需要的原始字段。
//
// 生产 docker.ContainerInspect 只解析生命周期与标签；只读挂载、tmpfs、可写绑定与
// 特权模式必须从真实 Docker 输出核验，不能用创建参数推断。
type rawContainerInspect struct {
	// ID 是容器 ID。
	ID string `json:"Id"`
	// Name 是容器名称（带前导斜杠）。
	Name string `json:"Name"`
	// State 是容器状态区段。
	State rawContainerState `json:"State"`
	// HostConfig 是创建时的主机侧配置。
	HostConfig rawHostConfig `json:"HostConfig"`
	// Mounts 是实际挂载清单。
	Mounts []rawMount `json:"Mounts"`
}

// rawContainerState 是容器状态区段。
type rawContainerState struct {
	// Status 是 Docker 报告的状态文本。
	Status string `json:"Status"`
	// Running 是 Docker 原样报告的运行标志。
	Running bool `json:"Running"`
	// Paused 是 Docker 原样报告的暂停标志。
	Paused bool `json:"Paused"`
	// ExitCode 是容器退出码。
	ExitCode int `json:"ExitCode"`
}

// rawHostConfig 是创建时的主机侧配置。
type rawHostConfig struct {
	// ReadonlyRootfs 表示 root 文件系统是否只读挂载。
	ReadonlyRootfs bool `json:"ReadonlyRootfs"`
	// Privileged 表示容器是否以特权模式运行。
	Privileged bool `json:"Privileged"`
	// PidMode 是 PID 命名空间模式。
	PidMode string `json:"PidMode"`
	// NetworkMode 是网络模式。
	NetworkMode string `json:"NetworkMode"`
	// Binds 是宿主机绑定挂载清单。
	Binds []string `json:"Binds"`
	// Tmpfs 是显式声明的 tmpfs 可写目录。
	Tmpfs map[string]string `json:"Tmpfs"`
}

// rawMount 是一条实际挂载。
type rawMount struct {
	// Type 是挂载类型：volume、bind 或 tmpfs。
	Type string `json:"Type"`
	// Name 是命名卷名称。
	Name string `json:"Name"`
	// Source 是宿主机侧来源路径。
	Source string `json:"Source"`
	// Destination 是容器内目标路径。
	Destination string `json:"Destination"`
	// RW 表示挂载是否可写。
	RW bool `json:"RW"`
}

// parseRawContainerInspect 解析 `docker inspect` 的 JSON 数组输出。
func parseRawContainerInspect(output string) (rawContainerInspect, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return rawContainerInspect{}, fmt.Errorf("docker inspect 输出为空")
	}
	var entries []rawContainerInspect
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return rawContainerInspect{}, err
	}
	if len(entries) == 0 {
		return rawContainerInspect{}, fmt.Errorf("docker inspect 结果为空")
	}
	return entries[0], nil
}

// mountFor 返回挂载到指定容器内路径的条目。
func (r rawContainerInspect) mountFor(destination string) (rawMount, bool) {
	for _, mount := range r.Mounts {
		if mount.Destination == destination {
			return mount, true
		}
	}
	return rawMount{}, false
}
