package app

import (
	"context"
	"strings"

	"github.com/lingting/agentflow/internal/codesaver"
	"github.com/lingting/agentflow/internal/domain"
)

// InspectWorktree 只读地读取任务容器内 Git 工作区的当前状态。
//
// 该入口用于记录"代码改了什么"，不是保存代码：它只执行只读 Git 查询，不提交、
// 不推送、不合并，也不写入 code_saves 记录。
//
// 容器只被核验与使用：本方法不会创建、启动、停止、删除或重新接管容器；容器缺失
// 或已 detached 时返回明确错误，不会回退到本机执行。
func (s *ContainerService) InspectWorktree(
	ctx context.Context,
	request GitWorktreeRequest,
) (domain.GitWorktreeState, error) {
	container, err := s.loadContainer(ctx, request.ContainerID)
	if err != nil {
		return failedGitState(err), err
	}
	if err := ensureManaged(container); err != nil {
		return failedGitState(err), err
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = defaultGitStateTimeout
	}
	var state domain.GitWorktreeState
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
				return err
			}
			if _, observeErr := s.observe(ctx, session, current); observeErr != nil {
				return observeErr
			}
			inspector, inspectorErr := codesaver.NewInspector(codesaver.InspectorOptions{
				Executor:     &containerSourceExecutor{session: session},
				Guard:        &sessionContainerGuard{service: s, session: session},
				ProbeTimeout: timeout,
			})
			if inspectorErr != nil {
				return inspectorErr
			}
			workDir := strings.TrimSpace(request.WorkDir)
			if workDir == "" {
				workDir = domain.DefaultContainerWorkDir
			}
			inspected, inspectErr := inspector.Inspect(ctx, codesaver.Request{
				ContainerID: current.ID.String(),
				DockerID:    current.DockerID,
				TaskID:      strings.TrimSpace(request.TaskID),
				WorkDir:     workDir,
			})
			if inspectErr != nil {
				return inspectErr
			}
			state = domain.GitWorktreeState{
				StatusRead: true,
				Branch:     inspected.Branch,
				Head:       inspected.Head,
				Dirty:      !inspected.Clean,
				Modified:   inspected.Modified,
				Added:      inspected.Added,
				Deleted:    inspected.Deleted,
				Renamed:    inspected.Renamed,
				Staged:     inspected.Staged,
				Conflicts:  inspected.Conflicts,
			}
			return nil
		},
	)
	if err != nil {
		return failedGitState(err), err
	}
	return state, nil
}

// 编译期确认容器服务满足步骤执行的 Git 状态采集契约。
var _ StepExecutionGitPort = (*ContainerService)(nil)
