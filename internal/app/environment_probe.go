package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
)

// 环境预检的超时时间。
const environmentProbeTimeout = 90 * time.Second

// EnvironmentProbe 是一次容器内环境能力预检的结果。
//
// 预检只读，不修改容器内任何文件；它验证的正是 SourceProvider 与 Runner
// 真正依赖的能力，而不是 Dockerfile 里是否写了安装命令。
type EnvironmentProbe struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// DockerID 是 Docker 侧容器 ID。
	DockerID string
	// GitAvailable 表示容器内存在可用的 Git。
	GitAvailable bool
	// GitVersion 是容器内 Git 版本。
	GitVersion string
	// PiAvailable 表示容器内存在可用的 Pi CLI。
	PiAvailable bool
	// PiVersion 是容器内 Pi CLI 版本。
	PiVersion string
	// WorkDirUsable 表示容器内项目目录可创建且可写。
	WorkDirUsable bool
	// Issues 是发现的兼容性问题说明。
	Issues []string
}

// Ready 判断环境是否满足代码获取与 Agent 执行的最低要求。
func (p EnvironmentProbe) Ready() bool {
	return p.GitAvailable && p.PiAvailable && p.WorkDirUsable
}

// ProbeEnvironment 检查容器内 Git、Pi CLI 与项目目录是否可用。
//
// 用户自行修改 Dockerfile 后可能缺少这些依赖，因此该检查在获取代码或执行 Agent
// 之前提供明确的能力报告，而不是等到 Clone 或 Pi 启动时才失败。
func (s *ContainerService) ProbeEnvironment(
	ctx context.Context,
	containerID string,
	credentials sshclient.CredentialProvider,
) (EnvironmentProbe, error) {
	container, err := s.loadContainer(ctx, containerID)
	if err != nil {
		return EnvironmentProbe{}, err
	}
	if err := ensureManaged(container); err != nil {
		return EnvironmentProbe{}, fmt.Errorf("环境预检失败: %w", err)
	}
	probe := EnvironmentProbe{
		ContainerID: container.ID.String(),
		DockerID:    container.DockerID,
	}
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		credentials,
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			current, loadErr := s.loadContainer(ctx, containerID)
			if loadErr != nil {
				return loadErr
			}
			if err := ensureManaged(current); err != nil {
				return fmt.Errorf("环境预检失败: %w", err)
			}
			inspect, inspectErr := s.observe(ctx, session, current)
			if inspectErr != nil {
				return inspectErr
			}
			if !inspect.Running {
				return fmt.Errorf("容器 %s 当前未运行，无法预检环境", current.ID)
			}
			probe.GitVersion = probeCommand(
				ctx,
				session,
				current.DockerID,
				[]string{"git", "--version"},
			)
			probe.GitAvailable = probe.GitVersion != ""
			probe.PiVersion = probeCommand(
				ctx,
				session,
				current.DockerID,
				[]string{"pi", "--version"},
			)
			probe.PiAvailable = probe.PiVersion != ""
			probe.WorkDirUsable = probeWorkDir(ctx, session, current.DockerID)
			probe.Issues = probeIssues(probe)
			return nil
		},
	)
	if err != nil {
		return probe, err
	}
	return probe, nil
}

// probeCommand 在容器内执行一条探测命令并返回首行输出，失败时返回空字符串。
func probeCommand(
	ctx context.Context,
	session *remoteDocker,
	dockerID string,
	command []string,
) string {
	run, err := session.runner.ExecCommand(ctx, dockerID, command, environmentProbeTimeout)
	if err != nil && !run.Succeeded() {
		return ""
	}
	if !run.Succeeded() {
		return ""
	}
	output := strings.TrimSpace(run.Stdout)
	if output == "" {
		return ""
	}
	return strings.Split(output, "\n")[0]
}

// probeWorkDir 检查容器内项目目录是否可用。
func probeWorkDir(ctx context.Context, session *remoteDocker, dockerID string) bool {
	run, err := session.runner.ExecCommand(
		ctx,
		dockerID,
		[]string{"sh", "-c", "mkdir -p /workspace/project && test -w /workspace/project"},
		environmentProbeTimeout,
	)
	if err != nil {
		return false
	}
	return run.Succeeded()
}

// probeIssues 汇总环境能力缺失的说明。
func probeIssues(probe EnvironmentProbe) []string {
	issues := make([]string, 0, 3)
	if !probe.GitAvailable {
		issues = append(issues, "容器内没有可用的 Git，无法在容器内获取代码")
	}
	if !probe.PiAvailable {
		issues = append(issues, "容器内没有可用的 Pi CLI，无法执行 Agent")
	}
	if !probe.WorkDirUsable {
		issues = append(issues, "容器内项目目录 /workspace/project 不可写")
	}
	return issues
}
