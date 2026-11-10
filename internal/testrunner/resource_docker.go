package testrunner

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lingting/agentflow/internal/docker"
)

// testContainerPrefix 是测试容器名称的统一前缀，与 tests/internal/testkit 的识别规则一致。
const testContainerPrefix = "agentflow-test-"

// DockerDaemonProvider 分配一台远程主机上的 Docker 命令入口（run-shared）。
//
// 就绪判定是 internal/docker 的版本探测：CLI 缺失、daemon 不可访问或版本低于基线都会在
// 分配阶段失败，不会回退到本机 Docker。连接归 ssh-host 所有，释放只清理占用记录。
type DockerDaemonProvider struct {
	hosts   *remoteHosts
	mu      sync.Mutex
	daemons map[string]Handle
}

// NewDockerDaemonProvider 创建远程 Docker 提供者。
func NewDockerDaemonProvider(hosts *remoteHosts) *DockerDaemonProvider {
	return &DockerDaemonProvider{hosts: hosts, daemons: make(map[string]Handle)}
}

// Acquire 探测远程 Docker 版本，句柄给出 host_id 与 endpoint。
func (p *DockerDaemonProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	hostID := specValue(decl, "host_id")
	host, err := p.hosts.config(hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	runner, err := p.hosts.runner(ctx, hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	probe := runner.ProbeVersion(ctx)
	if !probe.Succeeded() {
		return Handle{}, fmt.Errorf(
			"分配资源 %q 失败: 主机 %s 的 Docker 不可用（%s）：%s",
			decl.Name,
			hostID,
			probe.Status,
			probe.Message,
		)
	}
	handle, err := buildHandle(decl, map[string]string{
		"host_id":  host.HostID,
		"endpoint": host.endpoint(),
	}, "")
	if err != nil {
		return Handle{}, err
	}
	p.mu.Lock()
	p.daemons[host.HostID] = handle
	p.mu.Unlock()
	return handle, nil
}

// Release 清理占用记录；连接仍由 ssh-host 持有。
func (p *DockerDaemonProvider) Release(ctx context.Context, handle Handle) error {
	hostID := handle.PayloadValue("host_id")
	p.mu.Lock()
	delete(p.daemons, hostID)
	p.mu.Unlock()
	return nil
}

// Verify 自证提供者不再持有该主机的 Docker 入口。
func (p *DockerDaemonProvider) Verify(ctx context.Context, handle Handle) error {
	hostID := handle.PayloadValue("host_id")
	p.mu.Lock()
	_, tracked := p.daemons[hostID]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("主机 %q 的 Docker 入口仍被提供者持有，释放没有生效", hostID)
	}
	return nil
}

// Snapshot 返回仍被占用的 Docker 入口。
func (p *DockerDaemonProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.daemons))
	for hostID, handle := range p.daemons {
		entries = append(entries, LiveResource{
			Handle: handle,
			Detail: "仍被占用的 Docker 入口 " + hostID,
		})
	}
	return entries, nil
}

// RemoteContainerProvider 在一台远程主机上分配用例独占的测试容器。
//
// 容器只由 internal/docker 的 StartTemporaryContainer 创建（带 AgentFlow 所有权标签），
// 名称统一带 agentflow-test- 前缀；镜像必须已存在于远端，本阶段不拉取镜像。释放用
// RemoveContainer（幂等），自证用 InspectContainer 确认容器确实不存在。
type RemoteContainerProvider struct {
	hosts      *remoteHosts
	runID      string
	mu         sync.Mutex
	containers map[string]*remoteContainerEntry
}

// remoteContainerEntry 是一条已创建的测试容器记录。
type remoteContainerEntry struct {
	handle Handle
	name   string
	hostID string
}

// NewRemoteContainerProvider 创建远程测试容器提供者。
func NewRemoteContainerProvider(hosts *remoteHosts, runID string) *RemoteContainerProvider {
	return &RemoteContainerProvider{
		hosts:      hosts,
		runID:      strings.TrimSpace(runID),
		containers: make(map[string]*remoteContainerEntry),
	}
}

// Acquire 创建并启动测试容器，句柄给出 container_id 与 host_id。
func (p *RemoteContainerProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	hostID := specValue(decl, "host_id")
	image := specValue(decl, "image")
	runner, err := p.hosts.runner(ctx, hostID)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	imageID, err := runner.InspectImageID(ctx, image)
	if err != nil {
		return Handle{}, fmt.Errorf(
			"分配资源 %q 失败: 远端没有可用镜像 %q（本阶段不拉取镜像）: %w",
			decl.Name,
			image,
			err,
		)
	}
	token, err := randomToken(4)
	if err != nil {
		return Handle{}, err
	}
	name := p.containerName(decl.Name, token)
	dockerID, err := runner.StartTemporaryContainer(ctx, docker.TemporaryContainerSpec{
		Name:     name,
		ImageRef: image,
		Labels: map[string]string{
			"agentflow.test.run":      p.runLabel(),
			"agentflow.test.resource": safeName(decl.Name),
		},
		Command: specArgs(decl, "command"),
	})
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 的测试容器失败: %w", decl.Name, err)
	}
	handle, err := buildHandle(decl, map[string]string{
		"container_id": dockerID,
		"host_id":      hostID,
	}, imageID)
	if err != nil {
		_, _ = runner.RemoveContainer(context.Background(), dockerID)
		return Handle{}, err
	}
	p.mu.Lock()
	p.containers[dockerID] = &remoteContainerEntry{handle: handle, name: name, hostID: hostID}
	p.mu.Unlock()
	return handle, nil
}

// Release 删除测试容器，可重复调用。
func (p *RemoteContainerProvider) Release(ctx context.Context, handle Handle) error {
	dockerID := handle.PayloadValue("container_id")
	p.mu.Lock()
	entry := p.containers[dockerID]
	p.mu.Unlock()
	if entry == nil {
		return nil
	}
	runner, err := p.hosts.runner(ctx, entry.hostID)
	if err != nil {
		return fmt.Errorf("释放资源 %q 时无法取得 Docker 入口: %w", handle.Name, err)
	}
	if _, err := runner.RemoveContainer(ctx, dockerID); err != nil {
		return fmt.Errorf("删除测试容器 %s 失败: %w", entry.name, err)
	}
	p.mu.Lock()
	delete(p.containers, dockerID)
	p.mu.Unlock()
	return nil
}

// Verify 自证测试容器在释放后不再存在。
func (p *RemoteContainerProvider) Verify(ctx context.Context, handle Handle) error {
	dockerID := handle.PayloadValue("container_id")
	hostID := handle.PayloadValue("host_id")
	p.mu.Lock()
	_, tracked := p.containers[dockerID]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("测试容器 %s 仍被提供者持有，释放没有生效", dockerID)
	}
	runner, err := p.hosts.runner(ctx, hostID)
	if err != nil {
		return fmt.Errorf("核验测试容器 %s 时无法取得 Docker 入口: %w", dockerID, err)
	}
	if _, err := runner.InspectContainer(ctx, dockerID); err != nil {
		if docker.ErrorCodeOf(err) == docker.CodeContainerNotFound {
			return nil
		}
		return fmt.Errorf("无法确认测试容器 %s 是否已删除: %w", dockerID, err)
	}
	return fmt.Errorf("测试容器 %s 在释放后仍然存在", dockerID)
}

// Snapshot 返回提供者持有以及远端仍然存在的测试容器。
//
// 远端按名称前缀扫描：既覆盖本提供者创建的容器，也能发现由其他进程遗留的测试容器，
// 基线比对由运行级终检负责。
func (p *RemoteContainerProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	entries := p.trackedSnapshot()
	failures := make([]string, 0, 1)
	for _, hostID := range p.hosts.runnerHostIDs() {
		runner, err := p.hosts.runner(ctx, hostID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("主机 %s 无法取得 Docker 入口: %v", hostID, err))
			continue
		}
		listed, err := runner.ListContainers(ctx, docker.ListContainersOptions{
			All:         true,
			NameFilters: []string{p.namePrefix()},
		})
		if err != nil {
			failures = append(failures, fmt.Sprintf("主机 %s 无法列出测试容器: %v", hostID, err))
			continue
		}
		for _, item := range listed {
			if !strings.HasPrefix(item.Name, p.namePrefix()) {
				continue
			}
			entries = append(entries, LiveResource{
				Handle: Handle{
					Name: item.Name,
					Type: ResourceRemoteContainer,
					Payload: map[string]string{
						"container_id": item.ID,
						"host_id":      hostID,
					},
				},
				Detail: fmt.Sprintf("主机 %s 上仍存在的测试容器 %s", hostID, item.Name),
			})
		}
	}
	if len(failures) > 0 {
		return entries, fmt.Errorf("远端测试容器快照不完整: %s", strings.Join(failures, "; "))
	}
	return entries, nil
}

func (p *RemoteContainerProvider) trackedSnapshot() []LiveResource {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.containers))
	for dockerID, entry := range p.containers {
		entries = append(entries, LiveResource{
			Handle: entry.handle,
			Detail: fmt.Sprintf("仍被持有的测试容器 %s（%s）", entry.name, dockerID),
		})
	}
	return entries
}

// namePrefix 返回本次运行使用的测试容器名前缀。
func (p *RemoteContainerProvider) namePrefix() string {
	return testContainerPrefix + p.runLabel() + "-"
}

// containerName 生成带运行标识的测试容器名。
func (p *RemoteContainerProvider) containerName(resourceName, token string) string {
	return p.namePrefix() + safeName(resourceName) + "-" + token
}

// runLabel 返回用于容器标签与名称的运行标识。
func (p *RemoteContainerProvider) runLabel() string {
	if p.runID == "" {
		return "manual"
	}
	return safeName(strings.ToLower(p.runID))
}
