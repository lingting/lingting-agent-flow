package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/storage"
)

// ContainerService 管理远程 Docker 容器的完整生命周期。
//
// 记录同时保存 AgentFlow 管理状态与最近观测到的 Docker 状态；所有权通过本地记录
// 与 Docker 标签共同核验。已脱离管理的容器不会再被自动启动、停止或删除。
type ContainerService struct {
	*remoteDockerService
	// locks 按容器登记 ID 提供范围明确的互斥锁，使同一容器的并发操作串行化，
	// 不同容器之间仍然可以并发。
	locks keyedMutex
	// createLocks 按节点提供创建期互斥，避免并发创建时名称冲突。
	createLocks keyedMutex
}

// NewContainerService 构造容器服务。
func NewContainerService(options DockerServiceOptions) (*ContainerService, error) {
	base, err := newRemoteDockerService(options)
	if err != nil {
		return nil, err
	}
	return &ContainerService{remoteDockerService: base}, nil
}

// CreateContainerRequest 是创建并启动容器的输入。
type CreateContainerRequest struct {
	// MachineID 是目标执行节点。
	MachineID string
	// Ref 是环境引用。
	Ref domain.EnvironmentRef
	// VersionNumber 是环境版本号，0 表示当前可用版本。
	VersionNumber int
	// ProjectID 是可选的项目标识，用于缓存共享范围与元数据。
	ProjectID string
	// Caches 是允许挂载的缓存类型；空值表示不挂载任何缓存。
	Caches []string
	// TaskID 是可选的任务关联。
	TaskID *domain.ID
	// AttemptID 是可选的尝试关联。
	AttemptID *domain.ID
	// WorkDir 是容器内项目工作目录，空值使用默认目录。
	WorkDir string
	// Command 是容器启动命令，空值使用默认常驻命令。
	Command []string
	// SkipRunContractCheck 跳过运行契约预检，仅用于已知镜像已满足契约的场景。
	SkipRunContractCheck bool
}

// ContainerDetail 是容器记录与远程观测结果的组合。
type ContainerDetail struct {
	// Container 是本地登记记录。
	Container domain.Container
	// Inspect 是最近一次远程观测结果，容器已不存在时为空。
	Inspect *docker.ContainerInspect
}

// CreateContainer 创建并启动一个受管理的任务容器。
//
// 流程：解析并核验版本 → 预检运行契约 → 创建缓存卷 → 创建容器 → 启动 →
// 核验所有权与运行状态 → 写入远程登记清单与本地记录。任一步失败都会把记录
// 置为可诊断状态，不会留下"已登记但未核验"的成功假象。
func (s *ContainerService) CreateContainer(
	ctx context.Context,
	request CreateContainerRequest,
) (ContainerDetail, error) {
	if err := request.Ref.Validate(); err != nil {
		return ContainerDetail{}, fmt.Errorf("创建容器失败: %w", err)
	}
	if request.Ref.IsBase() {
		return ContainerDetail{}, fmt.Errorf("创建容器失败: 任务容器必须使用语言环境")
	}
	var detail ContainerDetail
	err := s.withRemoteDocker(
		ctx,
		request.MachineID,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.createLocks.Lock(session.host.ID.String())
			defer unlock()
			created, err := s.createLocked(ctx, session, request)
			if err != nil {
				return err
			}
			detail = created
			return nil
		},
	)
	if err != nil {
		return detail, err
	}
	s.debug(
		ctx,
		"容器已创建并启动",
		slog.String("machine", request.MachineID),
		slog.String("container", detail.Container.Name),
		slog.String("docker_id", detail.Container.DockerID),
	)
	return detail, nil
}

// createLocked 在创建互斥锁保护下完成容器创建。
func (s *ContainerService) createLocked(
	ctx context.Context,
	session *remoteDocker,
	request CreateContainerRequest,
) (ContainerDetail, error) {
	version, err := s.resolveUsableVersion(ctx, session.host.ID, request.Ref, request.VersionNumber)
	if err != nil {
		return ContainerDetail{}, err
	}
	if !request.SkipRunContractCheck {
		if err := session.runner.CheckRunContract(ctx, version.ImageRef); err != nil {
			return ContainerDetail{}, fmt.Errorf("容器运行契约预检失败: %w", err)
		}
	}
	containerID := domain.NewID()
	name, err := docker.ContainerNameFor(containerID)
	if err != nil {
		return ContainerDetail{}, err
	}
	workDir := strings.TrimSpace(request.WorkDir)
	if workDir == "" {
		workDir = domain.DefaultContainerWorkDir
	}
	container, err := domain.NewContainer(
		containerID,
		session.host.ID,
		name,
		request.Ref,
		version.Label(),
		version.BuildConfigSHA256,
		version.ImageID,
		time.Now(),
	)
	if err != nil {
		return ContainerDetail{}, fmt.Errorf("创建容器失败: %w", err)
	}
	container.TaskID = request.TaskID
	container.AttemptID = request.AttemptID
	if err := container.Validate(); err != nil {
		return ContainerDetail{}, fmt.Errorf("创建容器失败: %w", err)
	}
	caches, err := s.prepareCaches(ctx, session, request, version)
	if err != nil {
		return ContainerDetail{}, err
	}
	if err := s.store.CreateContainer(ctx, container); err != nil {
		return ContainerDetail{}, fmt.Errorf("写入容器登记记录失败: %w", err)
	}
	dockerID, createErr := session.runner.CreateContainer(ctx, docker.CreateContainerSpec{
		Container: container,
		ImageRef:  version.ImageRef,
		WorkDir:   workDir,
		Command:   request.Command,
		Caches:    caches,
	})
	if createErr != nil {
		// 创建结果不确定时按名称核验，避免重复创建同名容器。
		recovered, recoverErr := s.recoverByName(ctx, session, container)
		if recoverErr != nil {
			s.markUnknown(ctx, container, createErr)
			return ContainerDetail{}, fmt.Errorf("创建容器失败: %w", createErr)
		}
		dockerID = recovered
	}
	if err := container.MarkCreated(dockerID, domain.DockerStateCreated, time.Now()); err != nil {
		return ContainerDetail{}, fmt.Errorf("记录容器创建结果失败: %w", err)
	}
	if err := s.store.UpdateContainer(ctx, container); err != nil {
		return ContainerDetail{}, fmt.Errorf("更新容器登记记录失败: %w", err)
	}
	if err := session.runner.StartContainer(ctx, dockerID); err != nil {
		s.markUnknown(ctx, container, err)
		return ContainerDetail{}, fmt.Errorf("启动容器失败: %w", err)
	}
	inspect, inspectErr := session.runner.InspectContainer(ctx, dockerID)
	if inspectErr != nil {
		s.markUnknown(ctx, container, inspectErr)
		return ContainerDetail{}, fmt.Errorf("核验容器状态失败: %w", inspectErr)
	}
	if err := docker.VerifyOwnership(inspect, container); err != nil {
		s.markUnknown(ctx, container, err)
		return ContainerDetail{}, fmt.Errorf("容器所有权核验失败: %w", err)
	}
	if !inspect.Running {
		markErr := fmt.Errorf(
			"容器启动后未进入运行状态: Docker 状态 %s，退出码 %d",
			inspect.State,
			inspect.ExitCode,
		)
		s.markUnknown(ctx, container, markErr)
		return ContainerDetail{}, markErr
	}
	if err := container.MarkRunning(time.Now()); err != nil {
		return ContainerDetail{}, fmt.Errorf("记录容器运行状态失败: %w", err)
	}
	container.DockerID = dockerID
	if err := s.store.UpdateContainer(ctx, container); err != nil {
		return ContainerDetail{}, fmt.Errorf("更新容器登记记录失败: %w", err)
	}
	if err := s.syncRegistry(ctx, session, container); err != nil {
		return ContainerDetail{
			Container: container,
			Inspect:   &inspect,
		}, fmt.Errorf("容器已启动，但写入远程登记清单失败: %w", err)
	}
	return ContainerDetail{Container: container, Inspect: &inspect}, nil
}

// prepareCaches 创建并返回需要挂载的缓存卷。
func (s *ContainerService) prepareCaches(
	ctx context.Context,
	session *remoteDocker,
	request CreateContainerRequest,
	version domain.EnvironmentVersion,
) ([]docker.CacheVolumeSpec, error) {
	if len(request.Caches) == 0 {
		return nil, nil
	}
	template, err := docker.LanguageTemplateFor(request.Ref.Name)
	if err != nil {
		return nil, fmt.Errorf("解析缓存定义失败: %w", err)
	}
	selected, err := domain.SelectCaches(template.Caches, request.Caches)
	if err != nil {
		return nil, fmt.Errorf("解析缓存定义失败: %w", err)
	}
	specs := make([]docker.CacheVolumeSpec, 0, len(selected))
	for _, mount := range selected {
		scopeKey := ""
		if mount.Scope == domain.CacheScopeProject {
			if strings.TrimSpace(request.ProjectID) == "" {
				return nil, fmt.Errorf(
					"缓存 %s 的共享范围是项目级，必须提供项目标识",
					mount.Type,
				)
			}
			scopeKey = request.ProjectID
		}
		volumeName, nameErr := docker.CacheVolumeName(session.host.ID, mount, scopeKey)
		if nameErr != nil {
			return nil, fmt.Errorf("生成缓存卷名失败: %w", nameErr)
		}
		if err := session.runner.EnsureVolume(ctx, volumeName, map[string]string{
			docker.EnvironmentLabelKey: mount.Type,
			docker.HostLabelKey:        session.host.ID.String(),
		}); err != nil {
			return nil, fmt.Errorf("创建缓存卷失败: %w", err)
		}
		specs = append(specs, docker.CacheVolumeSpec{
			VolumeName:    volumeName,
			ContainerPath: mount.ContainerPath,
		})
	}
	return specs, nil
}

// recoverByName 在创建结果不确定时按名称核验容器是否已经存在。
func (s *ContainerService) recoverByName(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
) (string, error) {
	inspect, err := session.runner.InspectContainer(ctx, container.Name)
	if err != nil {
		return "", err
	}
	if err := docker.VerifyOwnership(inspect, container); err != nil {
		return "", err
	}
	if strings.TrimSpace(inspect.ID) == "" {
		return "", fmt.Errorf("核验到的容器 ID 为空")
	}
	return inspect.ID, nil
}

// resolveUsableVersion 解析可用于创建容器的环境版本。
func (s *ContainerService) resolveUsableVersion(
	ctx context.Context,
	hostID domain.ID,
	ref domain.EnvironmentRef,
	number int,
) (domain.EnvironmentVersion, error) {
	var version domain.EnvironmentVersion
	if number > 0 {
		found, err := s.store.GetEnvironmentVersion(ctx, hostID, ref, number)
		if err != nil {
			return domain.EnvironmentVersion{}, fmt.Errorf("读取环境版本失败: %w", err)
		}
		version = *found
	} else {
		versions, err := s.store.ListEnvironmentVersions(ctx, storage.EnvironmentVersionFilter{
			HostID:   hostID,
			Ref:      &ref,
			Statuses: []domain.BuildStatus{domain.BuildStatusSucceeded},
		})
		if err != nil {
			return domain.EnvironmentVersion{}, fmt.Errorf("读取环境版本失败: %w", err)
		}
		if len(versions) == 0 {
			return domain.EnvironmentVersion{}, fmt.Errorf(
				"环境 %s 没有构建成功的版本，无法创建容器",
				ref.Label(),
			)
		}
		version = versions[len(versions)-1]
	}
	if !version.Usable() {
		return domain.EnvironmentVersion{}, fmt.Errorf(
			"环境版本 %s 尚未构建成功，不能用于创建容器",
			version.Label(),
		)
	}
	return version, nil
}

// syncRegistry 把容器记录写入远程登记清单。
func (s *ContainerService) syncRegistry(
	ctx context.Context,
	session *remoteDocker,
	container domain.Container,
) error {
	registryPath := session.layout.ContainersRegistryPath()
	registry, err := session.runner.ReadContainerRegistry(ctx, session.transfer, registryPath)
	if err != nil {
		return err
	}
	registry.Upsert(docker.RegistryEntryFor(container))
	return session.runner.WriteContainerRegistry(ctx, session.transfer, registryPath, registry)
}

// markUnknown 把容器记录标记为状态不确定。
func (s *ContainerService) markUnknown(
	ctx context.Context,
	container domain.Container,
	cause error,
) {
	if cause == nil {
		return
	}
	failed := container
	if err := failed.MarkUnknown(cause.Error(), time.Now()); err != nil {
		// 状态迁移不被允许时只记录日志，不覆盖原始错误。
		s.debug(
			ctx,
			"标记容器状态不确定失败",
			slog.String("container", container.ID.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := s.store.UpdateContainer(ctx, failed); err != nil {
		s.debug(
			ctx,
			"保存容器状态不确定失败",
			slog.String("container", container.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}
