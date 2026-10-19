package docker_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// newLanguageContainer 构造一个语言环境容器记录，用于命令构造断言。
func newLanguageContainer(t *testing.T) domain.Container {
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

// versionClient 是同时提供远程命令与 Engine 套接字的受控替身。
//
// 它记录收到的每一条远程命令与每一个 HTTP 请求行，使测试可以断言
// DOCKER_API_VERSION 的控制策略、API 基准路径与响应解析行为。
type versionClient struct {
	mu sync.Mutex
	// commands 记录收到的远程命令。
	commands []string
	// apiRequests 记录收到的 Engine API 请求行。
	apiRequests []string
	// responder 按命令返回结果。
	responder func(command string) (sshclient.CommandResult, error)
	// apiStatus 与 apiBody 覆盖版本接口响应。
	apiStatus int
	apiBody   string
}

func (c *versionClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

func (c *versionClient) Run(
	_ context.Context,
	command string,
	_ sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	c.mu.Lock()
	c.commands = append(c.commands, command)
	responder := c.responder
	c.mu.Unlock()
	if responder != nil {
		return responder(command)
	}
	return sshclient.CommandResult{
		Command:              command,
		Status:               sshclient.CommandStatusSucceeded,
		TerminationConfirmed: true,
	}, nil
}

func (c *versionClient) RunWithInput(
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

func (c *versionClient) SFTP() (sshclient.FileTransfer, error) {
	return nil, errors.New("测试替身不支持 SFTP")
}

func (c *versionClient) Close() error { return nil }

// DialUnixSocket 用本机管道模拟远端 Docker Engine 套接字。
func (c *versionClient) DialUnixSocket(
	ctx context.Context,
	socketPath string,
) (net.Conn, error) {
	if socketPath != docker.DefaultDockerSocketPath {
		return nil, errors.New("套接字路径不符: " + socketPath)
	}
	server, client := net.Pipe()
	go func() {
		defer func() {
			_ = server.Close()
		}()
		buffer := make([]byte, 4096)
		count, readErr := server.Read(buffer)
		if readErr != nil {
			return
		}
		request := string(buffer[:count])
		line := request
		if index := strings.Index(request, "\r\n"); index >= 0 {
			line = request[:index]
		}
		c.mu.Lock()
		c.apiRequests = append(c.apiRequests, line)
		status := c.apiStatus
		body := c.apiBody
		c.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		if body == "" {
			body = `{"ApiVersion":"1.52","MinAPIVersion":"1.24","Version":"29.0.0"}`
		}
		response := "HTTP/1.1 " + itoa(status) + " " + http.StatusText(status) + "\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: " + itoa(len(body)) + "\r\n" +
			"Connection: close\r\n\r\n" + body
		_, _ = server.Write([]byte(response))
	}()
	return client, nil
}

// recordedCommands 返回收到的远程命令副本。
func (c *versionClient) recordedCommands() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

// recordedAPIRequests 返回收到的 Engine API 请求行副本。
func (c *versionClient) recordedAPIRequests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.apiRequests...)
}

// newVersionRunner 构造使用替身连接的 Docker 执行器。
func newVersionRunner(t *testing.T, client *versionClient) *docker.Runner {
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

// successVersionResponder 返回满足基线的 CLI 与 Engine 版本。
func successVersionResponder(command string) (sshclient.CommandResult, error) {
	switch {
	case strings.Contains(command, "docker version"):
		return versionSucceed(docker.MinimumCLIVersion + "\n"), nil
	case strings.Contains(command, "docker info"):
		return versionSucceed(docker.MinimumEngineVersion + "\n"), nil
	default:
		return versionSucceed(""), nil
	}
}

// versionSucceed 返回退出码 0 与指定标准输出。
func versionSucceed(output string) sshclient.CommandResult {
	return sshclient.CommandResult{
		Status:               sshclient.CommandStatusSucceeded,
		ExitCode:             0,
		Stdout:               output,
		TerminationConfirmed: true,
	}
}

// TestCompatibilityBaselineMatchesRules 验证代码中的基线与规则文件声明一致。
func TestCompatibilityBaselineMatchesRules(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	if docker.MinimumCLIVersion != "29.0.0" {
		t.Fatalf("Docker CLI 基线应为 29.0.0，实际 %s", docker.MinimumCLIVersion)
	}
	if docker.MinimumEngineVersion != "29.0.0" {
		t.Fatalf("Docker Engine 基线应为 29.0.0，实际 %s", docker.MinimumEngineVersion)
	}
	if docker.EngineAPIBaseVersion != "1.52" {
		t.Fatalf("API 基准应为 1.52，实际 %s", docker.EngineAPIBaseVersion)
	}
	if docker.MinimumCLIVersion != docker.MinimumEngineVersion {
		t.Fatalf("CLI 与 Engine 基线必须一致: %s / %s", docker.MinimumCLIVersion, docker.MinimumEngineVersion)
	}
}

// TestCheckMinimumVersion 验证最低版本判定覆盖各类版本形态。
func TestCheckMinimumVersion(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	cases := []struct {
		name   string
		actual string
		wantOK bool
		// minimum 覆盖基线版本，空值使用 Docker CLI 基线。
		minimum string
	}{
		{name: "等于基线", actual: "29.0.0", wantOK: true},
		{name: "补丁版本高于基线", actual: "29.0.1", wantOK: true},
		{name: "次版本高于基线", actual: "29.1.0", wantOK: true},
		{name: "主版本高于基线", actual: "30.0.0", wantOK: true},
		{name: "两位次版本按数值比较", actual: "29.10.0", wantOK: true},
		{name: "一位次版本低于两位基线", actual: "29.9.0", wantOK: false, minimum: "29.10.0"},
		{name: "两位次版本高于一位基线", actual: "29.10.0", wantOK: true, minimum: "29.9.0"},
		{name: "低于基线", actual: "28.5.2", wantOK: false},
		{name: "远低于基线", actual: "19.03.8", wantOK: false},
		{name: "缺少版本信息", actual: "", wantOK: false},
		{name: "非法版本格式", actual: "abc", wantOK: false},
		{name: "预发布版本", actual: "29.0.0-rc.1", wantOK: false},
		{name: "带 v 前缀", actual: "v29.0.0", wantOK: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			minimum := testCase.minimum
			if minimum == "" {
				minimum = docker.MinimumCLIVersion
			}
			check := docker.CheckMinimumVersion("Docker CLI", testCase.actual, minimum)
			if check.Succeeded() != testCase.wantOK {
				t.Fatalf(
					"版本 %q 判定不符: 期望 %t，实际错误 %v",
					testCase.actual,
					testCase.wantOK,
					check.Err,
				)
			}
			if !testCase.wantOK && check.Err == nil {
				t.Fatal("失败的检查必须给出诊断信息")
			}
		})
	}
}

// TestCheckAPICompatibility 验证 API 基准区间判定。
func TestCheckAPICompatibility(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	cases := []struct {
		name     string
		apiRange docker.APIRange
		wantOK   bool
	}{
		{
			name:     "最高版本等于基准",
			apiRange: docker.APIRange{Maximum: "1.52", Minimum: "1.24"},
			wantOK:   true,
		},
		{
			name:     "最高版本高于基准",
			apiRange: docker.APIRange{Maximum: "1.56", Minimum: "1.24"},
			wantOK:   true,
		},
		{
			name:     "最低接受版本低于基准",
			apiRange: docker.APIRange{Maximum: "1.52", Minimum: "1.44"},
			wantOK:   true,
		},
		{
			name:     "最低接受版本等于基准",
			apiRange: docker.APIRange{Maximum: "1.56", Minimum: "1.52"},
			wantOK:   true,
		},
		{
			name:     "最高版本低于基准",
			apiRange: docker.APIRange{Maximum: "1.51", Minimum: "1.24"},
			wantOK:   false,
		},
		{
			name:     "最低接受版本高于基准",
			apiRange: docker.APIRange{Maximum: "1.56", Minimum: "1.53"},
			wantOK:   false,
		},
		{
			name:     "缺少最高版本",
			apiRange: docker.APIRange{Maximum: "", Minimum: "1.24"},
			wantOK:   false,
		},
		{
			name:     "缺少最低接受版本",
			apiRange: docker.APIRange{Maximum: "1.52", Minimum: ""},
			wantOK:   false,
		},
		{
			name:     "两位次版本按数值比较",
			apiRange: docker.APIRange{Maximum: "1.100", Minimum: "1.24"},
			wantOK:   true,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			check := docker.CheckAPICompatibility(testCase.apiRange)
			if check.Succeeded() != testCase.wantOK {
				t.Fatalf(
					"API 区间 %+v 判定不符: 期望 %t，实际错误 %v",
					testCase.apiRange,
					testCase.wantOK,
					check.Err,
				)
			}
			if check.Baseline != docker.EngineAPIBaseVersion {
				t.Fatalf("检查必须使用固定基准: %s", check.Baseline)
			}
		})
	}
}

// TestProbeVersionUsesIsolatedProbeEnvironment 验证版本探测隔离 DOCKER_API_VERSION。
func TestProbeVersionUsesIsolatedProbeEnvironment(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &versionClient{responder: successVersionResponder}
	runner := newVersionRunner(t, client)
	probe := runner.ProbeVersion(context.Background())
	if !probe.Succeeded() {
		t.Fatalf("版本探测应通过: %s", probe.Message)
	}
	commands := client.recordedCommands()
	if len(commands) == 0 {
		t.Fatal("版本探测必须执行远程命令")
	}
	for _, command := range commands {
		if !strings.HasPrefix(command, "env -u DOCKER_API_VERSION docker ") {
			t.Fatalf("探测阶段必须隔离 DOCKER_API_VERSION: %s", command)
		}
		if strings.Contains(command, "DOCKER_API_VERSION=") {
			t.Fatalf("探测阶段不得设置 DOCKER_API_VERSION: %s", command)
		}
	}
	requests := client.recordedAPIRequests()
	if len(requests) != 1 {
		t.Fatalf("应只发起一次 API 基准探测: %+v", requests)
	}
	if !strings.Contains(requests[0], "/v1.52/version") {
		t.Fatalf("API 基准探测必须使用固定路径: %s", requests[0])
	}
}

// TestProbeVersionRejectsLowVersions 验证 CLI 与 Engine 版本分别检查。
func TestProbeVersionRejectsLowVersions(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	cases := []struct {
		name       string
		cliVersion string
		engine     string
	}{
		{name: "CLI 低于基线", cliVersion: "28.5.2", engine: docker.MinimumEngineVersion},
		{name: "Engine 低于基线", cliVersion: docker.MinimumCLIVersion, engine: "28.5.2"},
		{name: "CLI 版本无法解析", cliVersion: "unknown", engine: docker.MinimumEngineVersion},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := &versionClient{responder: func(command string) (sshclient.CommandResult, error) {
				switch {
				case strings.Contains(command, "docker version"):
					return versionSucceed(testCase.cliVersion + "\n"), nil
				case strings.Contains(command, "docker info"):
					return versionSucceed(testCase.engine + "\n"), nil
				default:
					return versionSucceed(""), nil
				}
			}}
			runner := newVersionRunner(t, client)
			probe := runner.ProbeVersion(context.Background())
			if probe.Succeeded() {
				t.Fatalf("版本不满足基线时探测不应通过: %+v", probe)
			}
			if probe.Message == "" {
				t.Fatal("失败的探测必须给出诊断信息")
			}
		})
	}
}

// TestProbeVersionRejectsIncompatibleAPI 验证 API 区间不包含基准时探测失败。
func TestProbeVersionRejectsIncompatibleAPI(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	cases := []struct {
		name      string
		apiBody   string
		apiStatus int
	}{
		{
			name:    "最高版本低于基准",
			apiBody: `{"ApiVersion":"1.51","MinAPIVersion":"1.24","Version":"29.0.0"}`,
		},
		{
			name:    "最低接受版本高于基准",
			apiBody: `{"ApiVersion":"1.56","MinAPIVersion":"1.53","Version":"29.0.0"}`,
		},
		{
			name:      "基准路径不可用",
			apiStatus: http.StatusNotFound,
			apiBody:   `{"message":"page not found"}`,
		},
		{
			name:    "响应结构无法识别",
			apiBody: `{"unexpected":true}`,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := &versionClient{
				responder: successVersionResponder,
				apiStatus: testCase.apiStatus,
				apiBody:   testCase.apiBody,
			}
			runner := newVersionRunner(t, client)
			probe := runner.ProbeVersion(context.Background())
			if probe.Succeeded() {
				t.Fatalf("API 基准不可用时应失败: %+v", probe)
			}
			if probe.Message == "" {
				t.Fatal("失败的探测必须给出诊断信息")
			}
		})
	}
}

// TestBusinessCommandsUseFixedAPIVersion 验证业务命令统一固定 API 基准。
func TestBusinessCommandsUseFixedAPIVersion(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	client := &versionClient{}
	runner := newVersionRunner(t, client)
	if err := runner.StartContainer(context.Background(), "container-abc"); err != nil {
		t.Fatalf("启动容器命令构造失败: %v", err)
	}
	commands := client.recordedCommands()
	if len(commands) != 1 {
		t.Fatalf("应只执行一条远程命令: %+v", commands)
	}
	want := "env 'DOCKER_API_VERSION=" + docker.EngineAPIBaseVersion + "' docker start container-abc"
	if commands[0] != want {
		t.Fatalf("业务命令必须固定 API 基准:\n期望 %s\n实际 %s", want, commands[0])
	}
}

// TestContainerCreateArgsRejectDetachFlag 验证 docker create 不再传入 --detach。
//
// docker create 只创建容器，不接受 --detach（该参数属于 docker run），
// 传入会被 CLI 直接拒绝（unknown flag）。
func TestContainerCreateArgsRejectDetachFlag(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	spec := docker.CreateContainerSpec{
		ImageRef: "agentflow-base:v0001-aaaaaaaaaaaa",
		WorkDir:  "/workspace/project",
	}
	args, err := docker.ContainerCreateArgs(spec)
	if err == nil {
		t.Fatalf("缺少容器记录时不应构造成功: %+v", args)
	}
	// 使用最小合法记录再验证参数集合。
	container := newLanguageContainer(t)
	args, err = docker.ContainerCreateArgs(docker.CreateContainerSpec{
		Container: container,
		ImageRef:  "agentflow-base:v0001-aaaaaaaaaaaa",
		WorkDir:   "/workspace/project",
	})
	if err != nil {
		t.Fatalf("构造容器创建参数失败: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--detach") {
		t.Fatalf("docker create 不接受 --detach: %s", joined)
	}
	for _, required := range []string{"--name", "--workdir", "--label"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("缺少必需参数 %s: %s", required, joined)
		}
	}
}

// TestRequiredCLICommandsExcludeUnusedFlags 验证命令清单不包含未实际使用的参数。
func TestRequiredCLICommandsExcludeUnusedFlags(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	for _, command := range docker.RequiredCLICommands() {
		for _, option := range command.Options {
			if option.Name == "--detach" && command.Subcommand == "create" {
				t.Fatalf("命令清单不应为 docker create 声明 --detach: %s", command.Label())
			}
		}
	}
	// docker cp 已统一经由 docker.Runner，必须出现在清单中。
	found := false
	for _, command := range docker.RequiredCLICommands() {
		if command.Subcommand == "cp" {
			found = true
		}
	}
	if !found {
		t.Fatal("命令清单必须包含 docker cp")
	}
}
