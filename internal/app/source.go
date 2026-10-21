package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/source"
)

// FetchSourceRequest 是把项目代码获取到任务容器内部的输入。
type FetchSourceRequest struct {
	// ContainerID 是容器登记记录 ID。
	ContainerID string
	// TaskID 是本次代码获取所属任务，用于生成任务分支 agent/<task-id>。
	TaskID string
	// RepositoryURL 是仓库地址，必须是 ssh://、https:// 或 host:path 形式。
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
// 它是 FetchSourceWith 的兼容入口：工作流端口仍然使用 source.Result，
// 但执行路径统一走 FetchSourceWith → source.Fetcher，避免同一套规则出现两份实现。
//
// 代码只克隆到容器内部目录，不经过本机数据目录，也不使用宿主机 bind mount；
// 容器只被核验与使用，不会被启动、停止或删除。
func (s *ContainerService) FetchSource(
	ctx context.Context,
	request FetchSourceRequest,
) (source.Result, error) {
	fetched, err := s.FetchSourceWith(ctx, request.ContainerID, source.FetchRequest{
		TaskID:      strings.TrimSpace(request.TaskID),
		SourceType:  domain.CodeSourceGit,
		Repository:  request.RepositoryURL,
		WorkDir:     request.WorkDir,
		BaseBranch:  request.BaseBranch,
		ContainerID: request.ContainerID,
		Timeout:     request.Timeout,
	})
	return source.Result{
		Repository:       fetched.Repository,
		ContainerID:      fetched.ContainerID,
		DockerID:         fetched.DockerID,
		WorkDir:          fetched.WorkDir,
		BaseBranch:       fetched.BaseBranch,
		BaseSelection:    fetched.BaseSelection,
		BaseCommit:       fetched.BaseCommit,
		TaskBranch:       fetched.TaskBranch,
		TaskBranchSource: fetched.TaskBranchSource.String(),
		Commit:           fetched.Commit,
		Duration:         fetched.Duration(),
	}, err
}

// FetchSourceWith 用指定代码获取方式执行一次容器内代码获取。
//
// 服务层负责核验容器、建立远程会话、串行化同一容器的操作，并按 sourceType 选择
// 具体的获取实现；第一版只注册了 Git Clone。后续新增 HTTP 下载、wget、curl 或
// SCP 时只需在 fetcherFactory 中注册新实现，调用方与持久化模型都不必改动。
func (s *ContainerService) FetchSourceWith(
	ctx context.Context,
	containerID string,
	request source.FetchRequest,
) (source.FetchResult, error) {
	sourceType := request.SourceType
	if sourceType == "" {
		sourceType = domain.CodeSourceGit
	}
	if err := sourceType.Validate(); err != nil {
		return source.FetchResult{}, fmt.Errorf("获取代码失败: %w", err)
	}
	if sourceType != domain.CodeSourceGit {
		return source.FetchResult{}, fmt.Errorf(
			"获取代码失败: 当前只支持 git 代码获取，不支持 %s",
			sourceType,
		)
	}
	container, err := s.loadContainer(ctx, containerID)
	if err != nil {
		return source.FetchResult{}, err
	}
	if err := ensureManaged(container); err != nil {
		return source.FetchResult{}, fmt.Errorf("获取代码失败: %w", err)
	}
	var result source.FetchResult
	err = s.withRemoteDocker(
		ctx,
		container.HostID.String(),
		false,
		func(ctx context.Context, session *remoteDocker) error {
			unlock := s.locks.Lock(container.ID.String())
			defer unlock()
			current, loadErr := s.loadContainer(ctx, containerID)
			if loadErr != nil {
				return loadErr
			}
			if err := ensureManaged(current); err != nil {
				return fmt.Errorf("获取代码失败: %w", err)
			}
			scoped := request
			scoped.ContainerID = current.ID.String()
			fetcher, fetcherErr := newContainerFetcher(s, session, sourceType)
			if fetcherErr != nil {
				return fmt.Errorf("获取代码失败: %w", fetcherErr)
			}
			fetched, fetchErr := fetcher.Fetch(ctx, scoped)
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
		slog.String("source_type", result.SourceType.String()),
		slog.String("repository", result.Repository),
		slog.String("base_branch", result.BaseBranch),
		slog.String("task_branch", result.TaskBranch),
		slog.String("commit", result.Commit),
	)
	return result, nil
}

// newContainerFetcher 按代码获取方式构造绑定到本次远程会话的实现。
//
// 第一版只注册 git：未知方式返回明确错误，不会静默当成 git 执行。
func newContainerFetcher(
	service *ContainerService,
	session *remoteDocker,
	sourceType domain.CodeSourceType,
) (source.Fetcher, error) {
	switch sourceType {
	case domain.CodeSourceGit:
		return source.NewGitFetcher(source.Options{
			Executor: &containerSourceExecutor{session: session},
			Guard:    &sessionContainerGuard{service: service, session: session},
		})
	default:
		return nil, fmt.Errorf("未注册的代码获取方式 %s", sourceType)
	}
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
