package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/source"
)

// FetchSourceRequest 是把项目代码获取到任务容器内部的输入。
type FetchSourceRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TaskID 是本次代码获取所属任务，用于生成任务分支 agent/<task-id>。
	TaskID string
	// RepositoryURL 是仓库地址，必须是 ssh:// 或 https:// 形式。
	RepositoryURL string
	// BaseBranch 是显式指定的基础分支，空值表示自动识别。
	BaseBranch string
	// WorkDir 是容器内目标目录，空值使用默认项目目录。
	WorkDir string
	// Timeout 是本次代码获取的超时时间。
	Timeout time.Duration
}

// FetchSource 在受管理的任务容器内部获取仓库代码并初始化任务分支。
//
// 代码只克隆到容器内部目录，不经过本机数据目录，也不使用宿主机 bind mount；
// 容器只被核验与使用，不会被启动、停止或删除。
func (s *ContainerService) FetchSource(
	ctx context.Context,
	request FetchSourceRequest,
) (source.Result, error) {
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return source.Result{}, err
	}
	if err := ensureManaged(container); err != nil {
		return source.Result{}, fmt.Errorf("获取代码失败: %w", err)
	}
	var result source.Result
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			current, loadErr := s.loadContainer(ctx, request.ContainerID)
			if loadErr != nil {
				return loadErr
			}
			if err := ensureManaged(current); err != nil {
				return fmt.Errorf("获取代码失败: %w", err)
			}
			executor := &containerSourceExecutor{session: session}
			guard := &sessionContainerGuard{service: s, session: session}
			provider, providerErr := source.NewProvider(source.Options{
				Executor: executor,
				Guard:    guard,
			})
			if providerErr != nil {
				return fmt.Errorf("获取代码失败: %w", providerErr)
			}
			fetched, fetchErr := provider.Fetch(ctx, source.Request{
				TaskID:        strings.TrimSpace(request.TaskID),
				ContainerID:   current.ID.String(),
				RepositoryURL: request.RepositoryURL,
				WorkDir:       request.WorkDir,
				BaseBranch:    request.BaseBranch,
				Timeout:       request.Timeout,
			})
			result = fetched
			return fetchErr
		},
	)
	if err != nil {
		return result, err
	}
	s.debug(
		ctx,
		"代码已获取到任务容器",
		slog.String("container", container.ID.String()),
		slog.String("repository", result.Repository),
		slog.String("base_branch", result.BaseBranch),
		slog.String("task_branch", result.TaskBranch),
		slog.String("commit", result.Commit),
	)
	return result, nil
}

// containerSourceExecutor 把一次远程 Docker 会话适配为 source.ContainerExecutor。
type containerSourceExecutor struct {
	session *remoteDocker
}

// ExecInContainer 在容器内执行一条命令。
func (e *containerSourceExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	if e == nil || e.session == nil || e.session.runner == nil {
		return docker.CommandResult{}, fmt.Errorf("容器内执行失败: 远程会话不可用")
	}
	if timeout <= 0 {
		timeout = source.DefaultProbeTimeout
	}
	return e.session.runner.ExecCommand(ctx, dockerID, command, timeout)
}

// 编译期确认适配器满足代码获取侧接口。
var _ source.ContainerExecutor = (*containerSourceExecutor)(nil)
