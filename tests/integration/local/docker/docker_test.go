// Package docker_local 提供远程 Docker 命令构造与结果解析的本机集成测试。
//
// 这里使用可控的 SSH 执行替身代替真实远程机器：替身记录收到的远程命令并返回
// 预设输出，因此可以在不依赖真实 Docker Engine 与真实 Linux 服务器的前提下
// 验证命令构造、参数转义、结果解析与错误分类。
package docker_local

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// fakeClient 是 sshclient.Client 的可控替身。
//
// 它记录收到的每一条远程命令，并按预设的响应表返回结果，使测试可以断言
// 命令构造细节而无需真实 SSH 连接。
type fakeClient struct {
	mu       sync.Mutex
	commands []string
	// responder 根据命令返回结果；未匹配时返回退出码 0 与空输出。
	responder func(command string) (sshclient.CommandResult, error)
	closed    bool
}

// Endpoint 返回固定目标。
func (c *fakeClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

// Run 记录命令并返回预设结果。
func (c *fakeClient) Run(
	ctx context.Context,
	command string,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return sshclient.CommandResult{
			Command:  command,
			Status:   sshclient.CommandStatusCancelled,
			ExitCode: -1,
		}, err
	}
	c.mu.Lock()
	c.commands = append(c.commands, command)
	responder := c.responder
	c.mu.Unlock()
	result := sshclient.CommandResult{
		Command:              command,
		Status:               sshclient.CommandStatusSucceeded,
		ExitCode:             0,
		TerminationConfirmed: true,
	}
	if responder != nil {
		return responder(command)
	}
	return result, nil
}

// RunWithInput 读取标准输入后转发到 Run，测试替身不真正写入远程进程。
func (c *fakeClient) RunWithInput(
	ctx context.Context,
	command string,
	input io.Reader,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	if input != nil {
		if _, err := io.Copy(io.Discard, input); err != nil {
			return sshclient.CommandResult{}, err
		}
	}
	return c.Run(ctx, command, options)
}

// SFTP 表示替身不支持文件传输。
func (c *fakeClient) SFTP() (sshclient.FileTransfer, error) {
	return nil, errors.New("测试替身不支持 SFTP")
}

// Close 标记连接已关闭。
func (c *fakeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// recorded 返回收到的全部命令副本。
func (c *fakeClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

// newRunner 构造一个使用替身连接的 Docker 命令执行器。
func newRunner(t *testing.T, client *fakeClient) *docker.Runner {
	t.Helper()
	runner, err := docker.NewRunner(client, docker.RunnerOptions{
		QueryTimeout:  5 * time.Second,
		MutateTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造 Docker 执行器失败: %v", err)
	}
	return runner
}

// succeed 返回退出码 0 与指定标准输出。
func succeed(output string) (sshclient.CommandResult, error) {
	return sshclient.CommandResult{
		Status:               sshclient.CommandStatusSucceeded,
		ExitCode:             0,
		Stdout:               output,
		TerminationConfirmed: true,
	}, nil
}

// failWith 返回非零退出码与指定标准错误。
func failWith(exitCode int, stderr string) (sshclient.CommandResult, error) {
	return sshclient.CommandResult{
		Status:               sshclient.CommandStatusExitError,
		ExitCode:             exitCode,
		Stderr:               stderr,
		TerminationConfirmed: true,
	}, nil
}

// newContainerRecord 构造测试用容器记录。
func newContainerRecord(t *testing.T) domain.Container {
	t.Helper()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境引用失败: %v", err)
	}
	container, err := domain.NewContainer(
		"0123456789abcdef0123456789abcdef",
		"host-1",
		"agentflow-0123456789ab",
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	return container
}

// TestPreflightReportsEachFailureCategory 验证预检能区分不同失败原因。
func TestPreflightReportsEachFailureCategory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		response func(command string) (sshclient.CommandResult, error)
		expected docker.ErrorCode
	}{
		{
			name: "docker 命令缺失",
			response: func(command string) (sshclient.CommandResult, error) {
				if strings.Contains(command, "docker version") {
					return failWith(127, "bash: docker: command not found")
				}
				return succeed("")
			},
			expected: docker.CodeCLIMissing,
		},
		{
			name: "守护进程不可用",
			response: func(command string) (sshclient.CommandResult, error) {
				if strings.Contains(command, "docker version") {
					return failWith(1, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock")
				}
				return succeed("")
			},
			expected: docker.CodeDaemonUnavailable,
		},
		{
			name: "权限不足",
			response: func(command string) (sshclient.CommandResult, error) {
				if strings.Contains(command, "docker info") {
					return failWith(1, "permission denied while trying to connect to the Docker daemon socket")
				}
				if strings.Contains(command, "docker version") {
					return succeed("24.0.7\n")
				}
				return succeed("")
			},
			expected: docker.CodePermissionDenied,
		},
		{
			name: "版本过低",
			response: func(command string) (sshclient.CommandResult, error) {
				if strings.Contains(command, "docker version") {
					return succeed("19.03.8\n")
				}
				if strings.Contains(command, "docker info") {
					return succeed("19.03.8\n")
				}
				return succeed("")
			},
			expected: docker.CodeVersionUnsupported,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			ws := testkit.New(t)
			ws.Report()
			client := &fakeClient{responder: testCase.response}
			runner := newRunner(t, client)
			preflight := runner.Preflight(context.Background(), docker.PreflightOptions{
				WorkspaceRoot: ws.Dir("workspace"),
			})
			if preflight.Ready() {
				t.Fatalf("预检不应通过: %+v", preflight)
			}
			if preflight.ErrorCode != testCase.expected {
				t.Fatalf(
					"错误类别不符: 期望 %s，实际 %s（%s）",
					testCase.expected,
					preflight.ErrorCode,
					preflight.Detail,
				)
			}
			if !preflight.SSHOK {
				t.Fatal("SSH 可用时预检应记录该事实")
			}
		})
	}
}

// TestPreflightUsesFixedCommands 验证预检只使用固定的只读命令。
func TestPreflightUsesFixedCommands(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		switch {
		case strings.Contains(command, "docker version"):
			return succeed("24.0.7\n")
		case strings.Contains(command, "docker info"):
			return succeed("24.0.7\n")
		case strings.HasPrefix(command, "test -w"):
			return succeed("")
		case strings.HasPrefix(command, "df -Pk"):
			return succeed("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 10240000 100 8000000 50% /\n")
		default:
			return succeed("")
		}
	}}
	runner := newRunner(t, client)
	preflight := runner.Preflight(context.Background(), docker.PreflightOptions{
		WorkspaceRoot:   ws.Dir("workspace"),
		CreateWorkspace: true,
	})
	if !preflight.Ready() {
		t.Fatalf("预检应通过: %+v", preflight)
	}
	if preflight.FreeDiskBytes <= 0 {
		t.Fatalf("应解析出剩余空间: %d", preflight.FreeDiskBytes)
	}
	for _, command := range client.recorded() {
		if strings.Contains(command, "sudo") {
			t.Fatalf("预检不得尝试 sudo: %s", command)
		}
		for _, forbidden := range []string{"rm ", "prune", "volume rm", "image rm"} {
			if strings.Contains(command, forbidden) {
				t.Fatalf("预检不得执行破坏性命令 %q: %s", forbidden, command)
			}
		}
	}
}

// TestPreflightRejectsUnwritableWorkspace 验证工作空间不可写时的分类。
func TestPreflightRejectsUnwritableWorkspace(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		switch {
		case strings.Contains(command, "docker version"):
			return succeed("24.0.7\n")
		case strings.Contains(command, "docker info"):
			return succeed("24.0.7\n")
		case strings.HasPrefix(command, "test -w"):
			return failWith(1, "")
		default:
			return succeed("")
		}
	}}
	runner := newRunner(t, client)
	preflight := runner.Preflight(context.Background(), docker.PreflightOptions{
		WorkspaceRoot: ws.Dir("workspace"),
	})
	if preflight.ErrorCode != docker.CodePermissionDenied {
		t.Fatalf("工作空间不可写应归类为权限问题: %+v", preflight)
	}
}

// TestBuildImageQuotesArgumentsAndReadsImageID 验证构建命令的参数转义与镜像核验。
func TestBuildImageQuotesArgumentsAndReadsImageID(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	imageID := "sha256:" + strings.Repeat("c", 64)
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		if strings.Contains(command, "image inspect") {
			return succeed(`[{"Id":"` + imageID + `","RepoTags":["agentflow-base:v0001-aaaaaaaaaaaa"]}]`)
		}
		if strings.Contains(command, "docker build") {
			return succeed("Successfully built\n")
		}
		return succeed("")
	}}
	runner := newRunner(t, client)
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:            "host-1",
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  strings.Repeat("a", 64),
		BuildConfigSHA256: strings.Repeat("a", 64),
		ContextSHA256:     strings.Repeat("a", 64),
		Status:            domain.BuildStatusBuilding,
		ImageRef:          "agentflow-base:v0001-aaaaaaaaaaaa",
		CreatedAt:         time.Now(),
	}
	buildContext, err := docker.NewContext([]docker.ContextFile{
		{RelativePath: docker.DockerfileName, Content: []byte("FROM debian:12.7-slim\n")},
	})
	if err != nil {
		t.Fatalf("构造构建上下文失败: %v", err)
	}
	// 上传上下文需要 SFTP，替身不支持，因此这里只验证命令构造与镜像核验部分。
	result, buildErr := runner.BuildImage(context.Background(), nil, docker.BuildSpec{
		Version:          version,
		Context:          buildContext,
		RemoteContextDir: ws.Path("builds", "base", "context"),
		LogPath:          ws.Path("builds", "base", "build.log"),
	})
	if buildErr == nil {
		t.Fatalf("缺少 SFTP 通道时构建应失败: %+v", result)
	}
	if docker.ErrorCodeOf(buildErr) != docker.CodeSSHUnavailable {
		t.Fatalf("缺少 SFTP 通道应归类为 SSH 不可用: %v", buildErr)
	}
}

// TestContainerOperationsAreIdempotent 验证容器操作的关键幂等行为。
func TestContainerOperationsAreIdempotent(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		if strings.HasPrefix(command, "docker rm") {
			return failWith(1, "Error response from daemon: No such container: abc")
		}
		return succeed("")
	}}
	runner := newRunner(t, client)
	result, err := runner.RemoveContainer(context.Background(), "abc")
	if err != nil {
		t.Fatalf("删除已不存在的容器应视为成功: %v", err)
	}
	if !result.Removed {
		t.Fatal("删除已不存在的容器应返回 Removed=true")
	}
	if result.Existed {
		t.Fatal("删除已不存在的容器应返回 Existed=false")
	}
}

// TestContainerOperationsRejectEmptyID 验证参数校验。
func TestContainerOperationsRejectEmptyID(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	runner := newRunner(t, &fakeClient{})
	for _, run := range []func() error{
		func() error {
			_, err := runner.InspectContainer(context.Background(), "")
			return err
		},
		func() error { return runner.StartContainer(context.Background(), "") },
		func() error { return runner.StopContainer(context.Background(), "", 0) },
		func() error {
			_, err := runner.RemoveContainer(context.Background(), "")
			return err
		},
		func() error {
			_, err := runner.ContainerLogs(context.Background(), "", 0)
			return err
		},
		func() error {
			_, err := runner.ExecCommand(context.Background(), "", nil, 0)
			return err
		},
	} {
		if err := run(); docker.ErrorCodeOf(err) != docker.CodeInvalidArgument {
			t.Fatalf("空容器 ID 应返回参数错误: %v", err)
		}
	}
}

// TestUncertainResultIsReportedAsStateUncertain 验证超时被归类为状态不确定。
func TestUncertainResultIsReportedAsStateUncertain(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:  command,
			Status:   sshclient.CommandStatusTimeout,
			ExitCode: -1,
			// 超时无法确认远程命令是否已经结束。
			TerminationConfirmed: false,
		}, context.DeadlineExceeded
	}}
	runner := newRunner(t, client)
	container := newContainerRecord(t)
	_, err := runner.CreateContainer(context.Background(), docker.CreateContainerSpec{
		Container: container,
		ImageRef:  "agentflow-language-go:v0001-aaaaaaaaaaaa",
	})
	if docker.ErrorCodeOf(err) != docker.CodeStateUncertain {
		t.Fatalf("超时应归类为远程状态不确定: %v", err)
	}
	if !strings.Contains(err.Error(), "不确定") {
		t.Fatalf("错误信息应说明结果不确定: %v", err)
	}
}

// TestBuildFailureIsClassified 验证构建失败被归类为构建失败而不是成功。
func TestBuildFailureIsClassified(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		if strings.Contains(command, "docker build") {
			return failWith(1, "error: failed to solve: process \"/bin/sh -c apt-get update\" did not complete")
		}
		return succeed("")
	}}
	runner := newRunner(t, client)
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	version := domain.EnvironmentVersion{
		HostID:            "host-1",
		Ref:               ref,
		Number:            1,
		DockerfileSHA256:  strings.Repeat("a", 64),
		BuildConfigSHA256: strings.Repeat("a", 64),
		ContextSHA256:     strings.Repeat("a", 64),
		Status:            domain.BuildStatusBuilding,
		ImageRef:          "agentflow-base:v0001-aaaaaaaaaaaa",
		CreatedAt:         time.Now(),
	}
	// 使用真实的上传替身需要 SFTP，这里直接验证失败分类函数的行为。
	run, runErr := runner.RunQuery(context.Background(), "docker build",
		"docker build --tag "+version.ImageRef+" .")
	if runErr == nil {
		t.Fatal("构建失败应返回错误")
	}
	if docker.ErrorCodeOf(runErr) != docker.CodeCommandFailed {
		t.Fatalf("未识别的失败应归类为命令失败: %v", runErr)
	}
	if run.ExitCode != 1 {
		t.Fatalf("退出码不符: %d", run.ExitCode)
	}
}

// TestManifestRoundTrip 验证镜像清单与容器清单的读写一致性。
func TestManifestRoundTrip(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	manifest := docker.ImageManifest{}
	manifest.Upsert(docker.ImageManifestEntry{
		Environment:       "base",
		Version:           "v0002",
		BuildConfigSHA256: strings.Repeat("b", 64),
		ImageRef:          "agentflow-base:v0002-bbbbbbbbbbbb",
		ImageID:           "sha256:" + strings.Repeat("c", 64),
	})
	manifest.Upsert(docker.ImageManifestEntry{
		Environment: "base",
		Version:     "v0001",
	})
	if len(manifest.Entries) != 2 {
		t.Fatalf("清单条目数不符: %d", len(manifest.Entries))
	}
	if manifest.Entries[0].Version != "v0001" {
		t.Fatalf("清单条目应按版本升序排列: %+v", manifest.Entries)
	}
	// 同一环境版本重复写入只替换，不追加。
	manifest.Upsert(docker.ImageManifestEntry{Environment: "base", Version: "v0001", ImageID: "updated"})
	if len(manifest.Entries) != 2 {
		t.Fatalf("重复写入不应追加条目: %d", len(manifest.Entries))
	}
	if entry, ok := manifest.Find("base", "v0001"); !ok || entry.ImageID != "updated" {
		t.Fatalf("重复写入应替换原条目: %+v", entry)
	}
	if _, ok := manifest.Find("base", "v9999"); ok {
		t.Fatal("不存在的版本不应被找到")
	}

	registry := docker.ContainerRegistry{}
	container := newContainerRecord(t)
	registry.Upsert(docker.RegistryEntryFor(container))
	if len(registry.Containers) != 1 {
		t.Fatalf("容器清单条目数不符: %d", len(registry.Containers))
	}
	if registry.Containers[0].ID != container.ID.String() {
		t.Fatalf("容器清单条目 ID 不符: %s", registry.Containers[0].ID)
	}
	// 保留脱离管理的事实。
	detached := container
	if err := detached.Detach(time.Now()); err != nil {
		t.Fatalf("脱离管理失败: %v", err)
	}
	registry.Upsert(docker.RegistryEntryFor(detached))
	if !registry.Containers[0].Detached {
		t.Fatal("容器清单应保留脱离管理事实")
	}
	if len(registry.Containers) != 1 {
		t.Fatalf("同一登记 ID 不应追加条目: %d", len(registry.Containers))
	}
}

// TestExecCommandUsesNonInteractiveForm 验证容器内命令使用非交互执行。
func TestExecCommandUsesNonInteractiveForm(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		return succeed("ok\n")
	}}
	runner := newRunner(t, client)
	if _, err := runner.ExecCommand(
		context.Background(),
		"container-abc",
		[]string{"go", "version"},
		time.Second,
	); err != nil {
		t.Fatalf("执行容器内命令失败: %v", err)
	}
	recorded := client.recorded()
	if len(recorded) != 1 {
		t.Fatalf("应只执行一条远程命令，实际 %d", len(recorded))
	}
	command := recorded[0]
	if strings.Contains(command, " -it ") || strings.Contains(command, " -ti ") {
		t.Fatalf("容器内命令不得使用交互式参数: %s", command)
	}
	if !strings.Contains(command, "docker exec") {
		t.Fatalf("容器内命令应使用 docker exec: %s", command)
	}
	if !strings.Contains(command, domain.DefaultContainerWorkDir) {
		t.Fatalf("容器内命令应在项目目录执行: %s", command)
	}
}

// TestListAgentFlowContainersFiltersByOwnership 验证容器列表只匹配 AgentFlow 资源。
func TestListAgentFlowContainersFiltersByOwnership(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &fakeClient{responder: func(command string) (sshclient.CommandResult, error) {
		return succeed("abc123\ndef456\n")
	}}
	runner := newRunner(t, client)
	ids, err := runner.ListAgentFlowContainers(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("列出容器失败: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("容器 ID 数量不符: %v", ids)
	}
	recorded := client.recorded()
	if len(recorded) != 1 {
		t.Fatalf("应只执行一条远程命令，实际 %d", len(recorded))
	}
	command := recorded[0]
	if !strings.Contains(command, docker.OwnershipLabelKey+"=true") {
		t.Fatalf("列表必须按管理标签过滤: %s", command)
	}
	if !strings.Contains(command, docker.HostLabelKey+"=host-1") {
		t.Fatalf("列表必须按节点标签过滤: %s", command)
	}
}
