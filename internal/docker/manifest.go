package docker

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
)

// ImageManifestEntry 是远程镜像元数据清单中的一条记录。
//
// 清单保存的是镜像元数据，不是镜像文件本身：镜像数据由远程 Docker Engine 管理。
type ImageManifestEntry struct {
	// Environment 是环境引用标签，例如 base 或 language/node。
	Environment string `json:"environment"`
	// Version 是环境版本标签，例如 v0001。
	Version string `json:"version"`
	// DockerfileSHA256 是 Dockerfile 内容哈希。
	DockerfileSHA256 string `json:"dockerfile_sha256"`
	// BuildConfigSHA256 是构建配置哈希。
	BuildConfigSHA256 string `json:"build_config_sha256"`
	// ContextSHA256 是构建上下文哈希。
	ContextSHA256 string `json:"context_sha256"`
	// ImageRef 是镜像引用。
	ImageRef string `json:"image_ref"`
	// ImageID 是实际镜像 ID。
	ImageID string `json:"image_id"`
	// BuiltAt 是构建完成时间（RFC3339）。
	BuiltAt string `json:"built_at"`
}

// ImageManifest 是远程镜像元数据清单。
type ImageManifest struct {
	// Entries 是按环境与版本排序的记录列表。
	Entries []ImageManifestEntry `json:"entries"`
}

// Upsert 插入或替换同一环境版本的记录，保持列表有序。
func (m *ImageManifest) Upsert(entry ImageManifestEntry) {
	for index, existing := range m.Entries {
		if existing.Environment == entry.Environment && existing.Version == entry.Version {
			m.Entries[index] = entry
			return
		}
	}
	m.Entries = append(m.Entries, entry)
	sort.Slice(m.Entries, func(i, j int) bool {
		if m.Entries[i].Environment == m.Entries[j].Environment {
			return m.Entries[i].Version < m.Entries[j].Version
		}
		return m.Entries[i].Environment < m.Entries[j].Environment
	})
}

// Find 查找指定环境版本的记录。
func (m ImageManifest) Find(environment, version string) (ImageManifestEntry, bool) {
	for _, entry := range m.Entries {
		if entry.Environment == environment && entry.Version == version {
			return entry, true
		}
	}
	return ImageManifestEntry{}, false
}

// ReadImageManifest 读取远程镜像元数据清单。
//
// 清单不存在时返回空清单而不是错误：首次构建前清单本来就不存在。
func (r *Runner) ReadImageManifest(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	manifestPath string,
) (ImageManifest, error) {
	manifest := ImageManifest{Entries: []ImageManifestEntry{}}
	if transfer == nil {
		return manifest, newError(CodeSSHUnavailable, "read image manifest", "SFTP 通道不可用", nil)
	}
	content, err := transfer.ReadFile(ctx, manifestPath, MaxBuildLogBytes)
	if err != nil {
		if isNotExist(err) {
			return manifest, nil
		}
		return manifest, newError(
			CodeSSHUnavailable,
			"read image manifest",
			"读取远程镜像清单失败",
			err,
		)
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return manifest, nil
	}
	if unmarshalErr := json.Unmarshal([]byte(trimmed), &manifest); unmarshalErr != nil {
		return ImageManifest{Entries: []ImageManifestEntry{}}, newError(
			CodeStateUncertain,
			"read image manifest",
			"解析远程镜像清单失败",
			unmarshalErr,
		)
	}
	if manifest.Entries == nil {
		manifest.Entries = []ImageManifestEntry{}
	}
	return manifest, nil
}

// WriteImageManifest 以原子方式写入远程镜像元数据清单。
func (r *Runner) WriteImageManifest(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	manifestPath string,
	manifest ImageManifest,
) error {
	if transfer == nil {
		return newError(CodeSSHUnavailable, "write image manifest", "SFTP 通道不可用", nil)
	}
	if manifest.Entries == nil {
		manifest.Entries = []ImageManifestEntry{}
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return newError(CodeInvalidArgument, "write image manifest", "编码镜像清单失败", err)
	}
	content = append(content, '\n')
	if writeErr := transfer.WriteFile(ctx, manifestPath, content, sshclient.WriteOptions{
		Overwrite:    true,
		Mode:         0o644,
		CreateParent: true,
	}); writeErr != nil {
		return newError(
			CodeSSHUnavailable,
			"write image manifest",
			"写入远程镜像清单失败",
			writeErr,
		)
	}
	return nil
}

// ContainerRegistryEntry 是远程容器登记清单中的一条记录。
type ContainerRegistryEntry struct {
	// ID 是容器登记记录 ID。
	ID string `json:"id"`
	// HostID 是所属节点 ID。
	HostID string `json:"host_id"`
	// DockerID 是 Docker 容器 ID。
	DockerID string `json:"docker_id"`
	// Name 是容器名称。
	Name string `json:"name"`
	// Environment 是环境引用标签。
	Environment string `json:"environment"`
	// Version 是环境版本标签。
	Version string `json:"version"`
	// ImageID 是镜像 ID。
	ImageID string `json:"image_id"`
	// State 是 AgentFlow 管理状态。
	State string `json:"state"`
	// DockerState 是最近观测到的 Docker 状态。
	DockerState string `json:"docker_state"`
	// Detached 表示容器已脱离 AgentFlow 自动管理。
	Detached bool `json:"detached"`
	// UpdatedAt 是最后更新时间（RFC3339）。
	UpdatedAt string `json:"updated_at"`
}

// ContainerRegistry 是远程容器登记清单。
//
// 清单按需持久化管理事实，使远程状态检查与恢复不必只依赖本机数据库；
// 它不能替代本机记录，也不能作为恢复管理权的唯一依据。
type ContainerRegistry struct {
	// Containers 是按登记 ID 排序的记录列表。
	Containers []ContainerRegistryEntry `json:"containers"`
}

// Upsert 插入或替换同一登记 ID 的记录，保持列表有序。
func (r *ContainerRegistry) Upsert(entry ContainerRegistryEntry) {
	for index, existing := range r.Containers {
		if existing.ID == entry.ID {
			r.Containers[index] = entry
			return
		}
	}
	r.Containers = append(r.Containers, entry)
	sort.Slice(r.Containers, func(i, j int) bool {
		return r.Containers[i].ID < r.Containers[j].ID
	})
}

// ReadContainerRegistry 读取远程容器登记清单。
func (r *Runner) ReadContainerRegistry(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	registryPath string,
) (ContainerRegistry, error) {
	registry := ContainerRegistry{Containers: []ContainerRegistryEntry{}}
	if transfer == nil {
		return registry, newError(CodeSSHUnavailable, "read container registry", "SFTP 通道不可用", nil)
	}
	content, err := transfer.ReadFile(ctx, registryPath, MaxBuildLogBytes)
	if err != nil {
		if isNotExist(err) {
			return registry, nil
		}
		return registry, newError(
			CodeSSHUnavailable,
			"read container registry",
			"读取远程容器清单失败",
			err,
		)
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return registry, nil
	}
	if unmarshalErr := json.Unmarshal([]byte(trimmed), &registry); unmarshalErr != nil {
		return ContainerRegistry{Containers: []ContainerRegistryEntry{}}, newError(
			CodeStateUncertain,
			"read container registry",
			"解析远程容器清单失败",
			unmarshalErr,
		)
	}
	if registry.Containers == nil {
		registry.Containers = []ContainerRegistryEntry{}
	}
	return registry, nil
}

// WriteContainerRegistry 以原子方式写入远程容器登记清单。
func (r *Runner) WriteContainerRegistry(
	ctx context.Context,
	transfer sshclient.FileTransfer,
	registryPath string,
	registry ContainerRegistry,
) error {
	if transfer == nil {
		return newError(CodeSSHUnavailable, "write container registry", "SFTP 通道不可用", nil)
	}
	if registry.Containers == nil {
		registry.Containers = []ContainerRegistryEntry{}
	}
	content, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return newError(CodeInvalidArgument, "write container registry", "编码容器清单失败", err)
	}
	content = append(content, '\n')
	if writeErr := transfer.WriteFile(ctx, registryPath, content, sshclient.WriteOptions{
		Overwrite:    true,
		Mode:         0o644,
		CreateParent: true,
	}); writeErr != nil {
		return newError(
			CodeSSHUnavailable,
			"write container registry",
			"写入远程容器清单失败",
			writeErr,
		)
	}
	return nil
}

// RegistryEntryFor 把容器记录转换为远程登记条目。
func RegistryEntryFor(container domain.Container) ContainerRegistryEntry {
	return ContainerRegistryEntry{
		ID:          container.ID.String(),
		HostID:      container.HostID.String(),
		DockerID:    container.DockerID,
		Name:        container.Name,
		Environment: container.Environment.Label(),
		Version:     container.EnvironmentVersion,
		ImageID:     container.ImageID,
		State:       container.State.String(),
		DockerState: container.DockerState.String(),
		Detached:    container.Detached,
		UpdatedAt:   container.UpdatedAt.UTC().Format(rfc3339),
	}
}

// rfc3339 是清单中使用的统一时间格式。
const rfc3339 = "2006-01-02T15:04:05Z07:00"
