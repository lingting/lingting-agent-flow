package docker_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/sshclient"
)

// helpClient 是 sshclient.Client 的受控替身，用于验证帮助检查只执行帮助命令。
type helpClient struct {
	mu sync.Mutex
	// commands 记录收到的全部远程命令。
	commands []string
	// responder 按命令返回结果。
	responder func(command string) (sshclient.CommandResult, error)
}

func (c *helpClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

func (c *helpClient) Run(
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

func (c *helpClient) RunWithInput(
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

func (c *helpClient) SFTP() (sshclient.FileTransfer, error) {
	return nil, errors.New("测试替身不支持 SFTP")
}

func (c *helpClient) Close() error { return nil }

// recorded 返回收到的全部命令副本。
func (c *helpClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

// newRunner 构造使用替身连接的 Docker 执行器。
func newRunner(t *testing.T, client sshclient.Client) *docker.Runner {
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

// successHelp 返回一份包含全部实际使用参数的帮助文本。
func successHelp() string {
	return strings.Join([]string{
		"Usage:  docker create [OPTIONS] IMAGE [COMMAND] [ARG...]",
		"Options:",
		"      --name string    Assign a name to the container",
		"  -d, --detach         Run container in background",
		"  -w, --workdir string Working directory inside the container",
		"  -l, --label list     Set meta data on a container",
		"      --mount mount    Attach a filesystem mount to the container",
		"      --time int       Seconds to wait before killing the container",
		"  -f, --force          Force the removal of a running container",
		"      --interactive    Keep STDIN open even if not attached",
		"      --tail string    Number of lines to show from the end of the logs",
		"      --rm             Automatically remove the container",
		"      --entrypoint string Overwrite the default ENTRYPOINT",
		"  -a, --all            Show all containers",
		"      --filter filter  Filter output based on conditions provided",
		"      --format string  Format output using a custom template",
		"  -t, --tag list       Name and optionally a tag",
		"  -f, --file string    Name of the Dockerfile",
		"      --build-arg list Set build-time variables",
		"      --no-cache       Do not use cache when building the image",
		"      --progress string Set type of progress output",
	}, "\n")
}

// TestRequiredCLICommandsMatchCode 验证命令清单与代码中的实际调用一致。
func TestRequiredCLICommandsMatchCode(t *testing.T) {
	t.Parallel()
	commands := docker.RequiredCLICommands()
	if len(commands) == 0 {
		t.Fatal("命令清单不能为空")
	}
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		label := command.Label()
		if _, ok := seen[label]; ok {
			t.Fatalf("命令清单存在重复项: %s", label)
		}
		seen[label] = struct{}{}
		if command.Source == "" {
			t.Fatalf("命令 %s 缺少来源说明", label)
		}
		for _, option := range command.Options {
			if !strings.HasPrefix(option.Name, "--") {
				t.Fatalf("选项 %s 必须使用长选项写法", option.Name)
			}
			if option.Source == "" {
				t.Fatalf("选项 %s 缺少来源说明", option.Name)
			}
		}
	}
	// 这些命令是 AgentFlow 实际使用的，缺失会让检查漏掉真实依赖。
	for _, expected := range []string{
		"docker create",
		"docker start",
		"docker stop",
		"docker rm",
		"docker inspect",
		"docker exec",
		"docker logs",
		"docker run",
		"docker ps",
		"docker volume create",
		"docker build",
		"docker image inspect",
		"docker version",
		"docker info",
	} {
		if _, ok := seen[expected]; !ok {
			t.Fatalf("命令清单缺少实际使用的命令: %s", expected)
		}
	}
}

// TestCheckCLIHelpPasses 验证帮助信息完整时检查通过。
func TestCheckCLIHelpPasses(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			Stdout:               successHelp(),
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), docker.RequiredCLICommands(), 0)
	if !result.Succeeded() {
		t.Fatalf("帮助检查应当通过: %s %+v", result.Message, result.Checks)
	}
	if len(result.Checks) != len(docker.RequiredCLICommands()) {
		t.Fatalf("应逐条检查全部命令: %d", len(result.Checks))
	}
}

// TestCheckCLIHelpOnlyRunsHelpCommands 验证检查只执行 --help，不执行任何变更命令。
func TestCheckCLIHelpOnlyRunsHelpCommands(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			Stdout:               successHelp(),
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	runner.CheckCLIHelp(context.Background(), docker.RequiredCLICommands(), 0)

	recorded := client.recorded()
	if len(recorded) == 0 {
		t.Fatal("未执行任何帮助命令")
	}
	for _, command := range recorded {
		if !strings.HasSuffix(command, "--help") {
			t.Fatalf("帮助检查执行了非帮助命令: %s", command)
		}
		for _, forbidden := range []string{
			"docker create --name",
			"docker pull",
			"docker run --rm --entrypoint",
		} {
			if strings.Contains(command, forbidden) {
				t.Fatalf("帮助检查不得执行变更命令: %s", command)
			}
		}
	}
}

// TestCheckCLIHelpDetectsMissingOption 验证缺失参数被识别为失败。
func TestCheckCLIHelpDetectsMissingOption(t *testing.T) {
	t.Parallel()
	help := strings.ReplaceAll(successHelp(), "--mount", "--volume")
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			Stdout:               help,
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), docker.RequiredCLICommands(), 0)
	if result.Succeeded() {
		t.Fatal("缺少 --mount 时检查不应通过")
	}
	if result.Status != docker.CLIHelpCodeOptionMissing {
		t.Fatalf("状态不符: %s", result.Status)
	}
	if len(result.MissingOptions) != 1 || result.MissingOptions[0] != "--mount" {
		t.Fatalf("缺失选项不符: %+v", result.MissingOptions)
	}
}

// TestCheckCLIHelpDoesNotMatchPrefix 验证 --log 不会命中 --logs 之外的误匹配。
func TestCheckCLIHelpDoesNotMatchPrefix(t *testing.T) {
	t.Parallel()
	// 帮助文本只包含 --logs（不存在），不应让 --log 判定为存在。
	help := "Options:\n      --logs string   bogus\n"
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			Stdout:               help,
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), []docker.CLICommand{
		{Subcommand: "logs", Options: []docker.CLIOption{{Name: "--tail"}}, Source: "test"},
	}, 0)
	if result.Succeeded() {
		t.Fatal("--tail 未出现时检查不应通过")
	}
	if len(result.MissingOptions) != 1 || result.MissingOptions[0] != "--tail" {
		t.Fatalf("缺失选项不符: %+v", result.MissingOptions)
	}
}

// TestCheckCLIHelpReadsStderr 验证帮助文本写入标准错误时同样被识别。
func TestCheckCLIHelpReadsStderr(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			Stderr:               successHelp(),
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), docker.RequiredCLICommands(), 0)
	if !result.Succeeded() {
		t.Fatalf("标准错误中的帮助文本应被识别: %s", result.Message)
	}
}

// TestCheckCLIHelpDetectsMissingCommand 验证命令不存在时给出明确分类。
func TestCheckCLIHelpDetectsMissingCommand(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusExitError,
			ExitCode:             1,
			Stderr:               "docker: 'nonexistent' is not a docker command.\nSee 'docker --help'\n",
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), []docker.CLICommand{
		{Subcommand: "nonexistent", Source: "test"},
	}, 0)
	if result.Status != docker.CLIHelpCodeCommandMissing {
		t.Fatalf("状态不符: %s (%s)", result.Status, result.Message)
	}
}

// TestCheckCLIHelpDetectsNonZeroExit 验证非零退出码被判为失败。
func TestCheckCLIHelpDetectsNonZeroExit(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusExitError,
			ExitCode:             2,
			Stderr:               "flag provided but not defined: -bogus\n",
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), []docker.CLICommand{
		{Subcommand: "create", Source: "test"},
	}, 0)
	if result.Status != docker.CLIHelpCodeCommandFailed {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestCheckCLIHelpRejectsEmptyHelp 验证退出码 0 但无输出时不能判为通过。
func TestCheckCLIHelpRejectsEmptyHelp(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusSucceeded,
			TerminationConfirmed: true,
		}, nil
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), []docker.CLICommand{
		{Subcommand: "create", Source: "test"},
	}, 0)
	if result.Succeeded() {
		t.Fatal("没有帮助输出时不应判定通过")
	}
}

// TestCheckCLIHelpDetectsTimeout 验证超时被单独分类。
func TestCheckCLIHelpDetectsTimeout(t *testing.T) {
	t.Parallel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusTimeout,
			ExitCode:             -1,
			TerminationConfirmed: false,
		}, errors.New("sshclient: 远程命令超时")
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(context.Background(), []docker.CLICommand{
		{Subcommand: "create", Source: "test"},
	}, 0)
	if result.Status != docker.CLIHelpCodeTimeout {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestCheckCLIHelpDetectsCancellation 验证取消被单独分类。
func TestCheckCLIHelpDetectsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &helpClient{responder: func(command string) (sshclient.CommandResult, error) {
		return sshclient.CommandResult{
			Command:              command,
			Status:               sshclient.CommandStatusCancelled,
			ExitCode:             -1,
			TerminationConfirmed: false,
		}, ctx.Err()
	}}
	runner := newRunner(t, client)
	result := runner.CheckCLIHelp(ctx, []docker.CLICommand{
		{Subcommand: "create", Source: "test"},
	}, 0)
	if result.Status != docker.CLIHelpCodeCancelled {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestSortedMissingOptions 验证缺失选项汇总按字典序稳定输出。
func TestSortedMissingOptions(t *testing.T) {
	t.Parallel()
	sorted := docker.SortedMissingOptions([]string{"--z", "--a", "--m"})
	expected := []string{"--a", "--m", "--z"}
	for index, value := range expected {
		if sorted[index] != value {
			t.Fatalf("排序结果不符: %+v", sorted)
		}
	}
}
