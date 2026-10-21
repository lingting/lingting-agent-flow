// 本文件验证执行环境使用的 Docker 命令构造与兼容性基线。
//
// 用例使用可控的 SSH 执行替身：不访问真实 Docker Engine 与远程机器，因此可以
// 精确断言命令参数、转义与 API 基准，同时不修改任何真实 Docker 资源。
package execenv_local

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
)

// fakeClient 是 sshclient.Client 的可控替身。
type fakeClient struct {
	mu sync.Mutex
	// commands 记录收到的远程命令。
	commands []string
	// result 是固定返回的执行结果。
	result sshclient.CommandResult
}

// Endpoint 返回固定目标。
func (c *fakeClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

// Run 记录命令并返回受控结果。
func (c *fakeClient) Run(
	ctx context.Context,
	command string,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	c.mu.Lock()
	c.commands = append(c.commands, command)
	result := c.result
	c.mu.Unlock()
	if result.Status == "" {
		result = sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			ExitCode:             0,
			TerminationConfirmed: true,
			Started:              true,
		}
	}
	return result, nil
}

// RunWithInput 读取标准输入后转发到 Run。
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
	return nil, nil
}

// Close 表示连接关闭。
func (c *fakeClient) Close() error {
	return nil
}

// recorded 返回收到的命令副本。
func (c *fakeClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

// newRunner 构造使用替身连接的 Docker 执行器。
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

func TestExecCommandInPassesEnvAndWorkDir(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	runner := newRunner(t, client)
	if _, err := runner.ExecCommandIn(
		context.Background(),
		"docker-1",
		[]string{"go", "test", "./..."},
		docker.ExecOptions{
			WorkDir:        "/workspace/project",
			Env:            []string{"CI=1", "GOFLAGS=-mod=mod"},
			Timeout:        time.Minute,
			MaxOutputBytes: 4096,
		},
	); err != nil {
		t.Fatalf("执行容器内命令失败: %v", err)
	}
	commands := client.recorded()
	if len(commands) != 1 {
		t.Fatalf("应当只执行一条命令，实际 %d", len(commands))
	}
	command := commands[0]
	// 命令必须显式固定 API 基准，不得依赖远端环境变量。
	if !strings.Contains(command, "DOCKER_API_VERSION="+docker.EngineAPIBaseVersion) {
		t.Fatalf("命令未固定 API 基准: %s", command)
	}
	if !strings.Contains(command, "docker exec") {
		t.Fatalf("命令未使用 docker exec: %s", command)
	}
	if !strings.Contains(command, "--workdir /workspace/project") {
		t.Fatalf("命令未指定工作目录: %s", command)
	}
	if !strings.Contains(command, "--env") || !strings.Contains(command, "CI=1") {
		t.Fatalf("命令未传递环境变量: %s", command)
	}
	if !strings.Contains(command, "GOFLAGS=-mod=mod") {
		t.Fatalf("命令未传递第二个环境变量: %s", command)
	}
	if !strings.Contains(command, "docker-1 go test ./...") {
		t.Fatalf("命令参数未逐项传递: %s", command)
	}
}

func TestExecCommandInRejectsInvalidEnv(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	runner := newRunner(t, client)
	cases := []struct {
		name string
		env  []string
	}{
		{name: "缺少等号", env: []string{"CI"}},
		{name: "空键名", env: []string{"=1"}},
		{name: "重复键名", env: []string{"CI=1", "CI=2"}},
	}
	for _, testCase := range cases {
		_, err := runner.ExecCommandIn(
			context.Background(),
			"docker-1",
			[]string{"ls"},
			docker.ExecOptions{Env: testCase.env},
		)
		if err == nil {
			t.Fatalf("用例 %s 必须被拒绝", testCase.name)
		}
		if code := docker.ErrorCodeOf(err); code != docker.CodeInvalidArgument {
			t.Fatalf("用例 %s 应返回参数错误，实际 %s", testCase.name, code)
		}
	}
	if len(client.recorded()) != 0 {
		t.Fatal("参数非法时不得发出远程命令")
	}
}

func TestExecCommandInWithInputEnablesInteractive(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	runner := newRunner(t, client)
	if _, err := runner.ExecCommandInWithInput(
		context.Background(),
		"docker-1",
		[]string{"pi", "--print"},
		strings.NewReader("提示词\n"),
		docker.ExecOptions{
			WorkDir: "/workspace/project",
			Env:     []string{"CI=1"},
			Timeout: time.Minute,
		},
	); err != nil {
		t.Fatalf("执行带输入的命令失败: %v", err)
	}
	commands := client.recorded()
	if len(commands) != 1 {
		t.Fatalf("应当只执行一条命令，实际 %d", len(commands))
	}
	command := commands[0]
	if !strings.Contains(command, "--interactive") {
		t.Fatalf("带标准输入时必须启用 --interactive: %s", command)
	}
	if !strings.Contains(command, "--env") || !strings.Contains(command, "CI=1") {
		t.Fatalf("带标准输入时也必须传递环境变量: %s", command)
	}
	if !strings.Contains(command, "--workdir /workspace/project") {
		t.Fatalf("带标准输入时必须指定工作目录: %s", command)
	}
}

func TestRequiredCLICommandsCoversExecEnv(t *testing.T) {
	t.Parallel()
	// docker exec 实际使用的参数必须全部出现在兼容性清单中，
	// 否则环境预检会漏检新增参数。
	var execOptions []string
	for _, command := range docker.RequiredCLICommands() {
		if command.Subcommand != "exec" {
			continue
		}
		for _, option := range command.Options {
			execOptions = append(execOptions, option.Name)
		}
	}
	want := []string{"--workdir", "--env", "--interactive"}
	for _, option := range want {
		found := false
		for _, actual := range execOptions {
			if actual == option {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("兼容性清单缺少 docker exec 参数 %s，实际 %v", option, execOptions)
		}
	}
}

func TestCompatibilityBaselineIsUnchanged(t *testing.T) {
	t.Parallel()
	if docker.MinimumCLIVersion != "29.0.0" {
		t.Fatalf("Docker CLI 最低版本基线被改动: %s", docker.MinimumCLIVersion)
	}
	if docker.MinimumEngineVersion != "29.0.0" {
		t.Fatalf("Docker Engine 最低版本基线被改动: %s", docker.MinimumEngineVersion)
	}
	if docker.EngineAPIBaseVersion != "1.52" {
		t.Fatalf("Engine API 基准版本被改动: %s", docker.EngineAPIBaseVersion)
	}
}

func TestContainerCreateArgsKeepSafetyBoundaries(t *testing.T) {
	t.Parallel()
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeLanguage, "go")
	if err != nil {
		t.Fatalf("构造语言环境失败: %v", err)
	}
	container, err := domain.NewContainer(
		"container-1",
		"host-1",
		"agentflow-abcdef123456",
		ref,
		"v0001",
		strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("b", 64),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("构造容器记录失败: %v", err)
	}
	args, err := docker.ContainerCreateArgs(docker.CreateContainerSpec{
		Container: container,
		ImageRef:  "agentflow-language-go:v0001-0123456789ab",
		WorkDir:   "/workspace/project",
		Caches: []docker.CacheVolumeSpec{
			{VolumeName: "agentflow-cache", ContainerPath: "/var/cache/agentflow/go/pkg/mod"},
		},
	})
	if err != nil {
		t.Fatalf("构造创建参数失败: %v", err)
	}
	joined := strings.Join(args, " ")
	// 安全边界：不得启用特权模式、宿主机网络与 Docker Socket 挂载。
	for _, forbidden := range []string{"--privileged", "--network=host", "--pid=host", "docker.sock"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("容器创建参数包含禁止项 %s: %s", forbidden, joined)
		}
	}
	// 缓存必须使用命名卷而不是宿主机 bind mount。
	if !strings.Contains(joined, "type=volume") {
		t.Fatalf("缓存挂载必须使用命名卷: %s", joined)
	}
	if !strings.Contains(joined, "--workdir /workspace/project") {
		t.Fatalf("容器创建工作目录不符: %s", joined)
	}
}
