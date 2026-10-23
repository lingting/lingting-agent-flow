package workflow_remote

import (
	"context"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// remoteExecutor 把 agent.ContainerExecutor 与 source.ContainerExecutor 适配到真实
// 远程 Docker 与 SFTP 通道。
//
// 它不替换任何外部依赖：命令经 SSH 在容器内真实执行，文件经真实 SFTP 传输。
type remoteExecutor struct {
	client   sshclient.Client
	transfer sshclient.FileTransfer
	runner   *docker.Runner
}

// newRemoteExecutor 构造远程执行器。
func newRemoteExecutor(t *testing.T, client sshclient.Client) *remoteExecutor {
	t.Helper()
	runner, err := docker.NewRunner(client, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Logf("WARN 关闭 SFTP 通道失败: %v", closeErr)
		}
	})
	return &remoteExecutor{client: client, transfer: transfer, runner: runner}
}

// Exec 在容器内执行命令并写入标准输入。
func (e *remoteExecutor) Exec(
	ctx context.Context,
	request agent.ContainerExecRequest,
) (agent.ContainerExecResult, error) {
	var (
		run docker.CommandResult
		err error
	)
	if len(request.Stdin) == 0 {
		run, err = e.runner.ExecCommand(ctx, request.DockerID, request.Command, request.Timeout)
	} else {
		run, err = e.runner.ExecCommandWithInput(
			ctx, request.DockerID, request.Command,
			strings.NewReader(string(request.Stdin)), request.Timeout,
		)
	}
	return agent.ContainerExecResult{
		ExitCode:        run.ExitCode,
		Stdout:          run.Stdout,
		Stderr:          run.Stderr,
		StdoutTruncated: run.Truncated,
		StderrTruncated: run.Truncated,
		StdinStatus:     run.Stdin.Status,
		StdinBytes:      run.Stdin.Bytes,
		StdinError:      run.Stdin.Error,
		Duration:        run.Duration,
		TimedOut:        run.TimedOut(),
		Cancelled:       run.Cancelled(),
		Certain:         run.Certain,
	}, err
}

// ExecContainer 在容器内执行命令并写入标准输入，供 testkit 注入 Git 凭证使用。
func (e *remoteExecutor) ExecContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	stdin string,
) (testkit.GitCredentialResult, error) {
	var (
		run docker.CommandResult
		err error
	)
	if stdin == "" {
		run, err = e.runner.ExecCommand(ctx, dockerID, command, e2eOpTimeout)
	} else {
		run, err = e.runner.ExecCommandWithInput(
			ctx, dockerID, command, strings.NewReader(stdin), e2eOpTimeout,
		)
	}
	if err != nil {
		return testkit.GitCredentialResult{}, err
	}
	return testkit.GitCredentialResult{
		ExitCode: run.ExitCode,
		Stdout:   run.Stdout,
		Stderr:   run.Stderr,
	}, nil
}

// CopyFromContainer 用 docker cp 把容器内文件复制到远程临时目录。
func (e *remoteExecutor) CopyFromContainer(
	ctx context.Context,
	dockerID, containerPath, hostPath string,
) error {
	if err := e.transfer.MkdirAll(ctx, path.Dir(hostPath)); err != nil {
		return err
	}
	run, runErr := e.runner.CopyFromContainer(ctx, dockerID, containerPath, hostPath, e2eOpTimeout)
	if runErr != nil {
		return runErr
	}
	if !run.Succeeded() {
		return fmt.Errorf("docker cp 失败（退出码 %d）: %s", run.ExitCode, run.Stderr)
	}
	return nil
}

// DownloadToLocal 把远程宿主机文件下载到本机。
func (e *remoteExecutor) DownloadToLocal(
	ctx context.Context,
	hostPath, localPath string,
	maxBytes int64,
) (int64, error) {
	if err := e.transfer.Download(ctx, hostPath, localPath, sshclient.DownloadOptions{
		Overwrite:    true,
		CreateParent: true,
		MaxBytes:     maxBytes,
	}); err != nil {
		return 0, err
	}
	info, err := e.transfer.Stat(ctx, hostPath)
	if err != nil {
		return 0, nil
	}
	return info.Size, nil
}

// RemoveHostFile 删除远程临时文件。
func (e *remoteExecutor) RemoveHostFile(ctx context.Context, hostPath string) error {
	return e.transfer.RemoveAll(ctx, hostPath)
}

// ExecInContainer 在容器内执行命令，满足 source.ContainerExecutor。
func (e *remoteExecutor) ExecInContainer(
	ctx context.Context,
	dockerID string,
	command []string,
	timeout time.Duration,
) (docker.CommandResult, error) {
	return e.runner.ExecCommand(ctx, dockerID, command, timeout)
}
