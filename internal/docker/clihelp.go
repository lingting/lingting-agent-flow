package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Docker CLI 帮助检查的默认参数。
const (
	// DefaultCLIHelpTimeout 是单条帮助命令的超时时间。
	DefaultCLIHelpTimeout = 5 * time.Second
)

// 帮助检查的结果分类。
const (
	// CLIHelpCodeOK 表示命令与参数都出现在帮助信息中。
	CLIHelpCodeOK = ""
	// CLIHelpCodeCommandMissing 表示子命令不存在或无法输出帮助信息。
	CLIHelpCodeCommandMissing = "docker_command_missing"
	// CLIHelpCodeOptionMissing 表示帮助信息中没有必需的参数选项。
	CLIHelpCodeOptionMissing = "docker_option_missing"
	// CLIHelpCodeCommandFailed 表示帮助命令以非零退出码结束。
	CLIHelpCodeCommandFailed = "docker_help_failed"
	// CLIHelpCodeTimeout 表示帮助命令超时。
	CLIHelpCodeTimeout = "docker_help_timeout"
	// CLIHelpCodeCancelled 表示调用方取消了帮助检查。
	CLIHelpCodeCancelled = "docker_help_cancelled"
	// CLIHelpCodeSSHUnavailable 表示远程命令无法执行。
	CLIHelpCodeSSHUnavailable = "ssh_unavailable"
)

// CLIOption 是一条 Docker 命令实际使用的参数选项。
type CLIOption struct {
	// Name 是选项写法，例如 --label 或 --time。
	Name string
	// Source 是使用该选项的代码位置说明，便于定位需求来源。
	Source string
}

// CLICommand 是一条 Docker 命令及其实际使用的参数选项。
type CLICommand struct {
	// Subcommand 是 docker 的子命令，例如 create 或 volume。
	Subcommand string
	// Args 是子命令之后的固定参数，例如 image inspect 的 image。
	Args []string
	// Options 是该命令实际使用的参数选项。
	Options []CLIOption
	// Source 是使用该命令的代码位置说明。
	Source string
}

// HelpCommand 返回检查该命令帮助信息时执行的 docker 参数。
func (c CLICommand) HelpCommand() []string {
	args := make([]string, 0, len(c.Args)+2)
	args = append(args, c.Subcommand)
	args = append(args, c.Args...)
	return append(args, "--help")
}

// Label 返回便于展示的命令名称。
func (c CLICommand) Label() string {
	parts := append([]string{"docker", c.Subcommand}, c.Args...)
	return strings.Join(parts, " ")
}

// RequiredCLICommands 返回 AgentFlow 实际使用的 Docker 命令与参数选项。
//
// 清单必须与 internal/docker 中真实的命令构造保持一致：这里列出的是
// container.go、image.go、manifest.go 与 runner.go 里实际出现的子命令和选项，
// 不包含 AgentFlow 没有使用的命令。新增 Docker 调用时必须同步更新该清单，
// 否则帮助检查会漏检新引入的选项。
func RequiredCLICommands() []CLICommand {
	return []CLICommand{
		{
			Subcommand: "create",
			Options: []CLIOption{
				{Name: "--name", Source: "ContainerCreateArgs"},
				{Name: "--workdir", Source: "ContainerCreateArgs"},
				{Name: "--label", Source: "ContainerCreateArgs"},
				{Name: "--mount", Source: "ContainerCreateArgs"},
			},
			Source: "CreateContainer",
		},
		{
			Subcommand: "start",
			Source:     "StartContainer",
		},
		{
			Subcommand: "stop",
			Options: []CLIOption{
				{Name: "--time", Source: "StopContainer"},
			},
			Source: "StopContainer",
		},
		{
			Subcommand: "rm",
			Options: []CLIOption{
				{Name: "--force", Source: "RemoveContainer"},
			},
			Source: "RemoveContainer",
		},
		{
			Subcommand: "inspect",
			Source:     "InspectContainer",
		},
		{
			Subcommand: "exec",
			Options: []CLIOption{
				{Name: "--workdir", Source: "execCommandString"},
				{Name: "--env", Source: "execEnvArgs"},
				{Name: "--interactive", Source: "ExecCommandWithInput"},
			},
			Source: "ExecCommand",
		},
		{
			Subcommand: "logs",
			Options: []CLIOption{
				{Name: "--tail", Source: "ContainerLogs"},
			},
			Source: "ContainerLogs",
		},
		{
			Subcommand: "run",
			Options: []CLIOption{
				{Name: "--rm", Source: "CheckRunContract"},
				{Name: "--entrypoint", Source: "CheckRunContract"},
			},
			Source: "CheckRunContract",
		},
		{
			Subcommand: "ps",
			Options: []CLIOption{
				{Name: "--all", Source: "ListAgentFlowContainers/ListContainers"},
				{Name: "--filter", Source: "ListAgentFlowContainers/ListContainers"},
				{Name: "--format", Source: "ListAgentFlowContainers/ListContainers"},
			},
			Source: "ListAgentFlowContainers",
		},
		{
			Subcommand: "volume",
			Args:       []string{"create"},
			Options: []CLIOption{
				{Name: "--label", Source: "EnsureVolume"},
			},
			Source: "EnsureVolume",
		},
		{
			Subcommand: "build",
			Options: []CLIOption{
				{Name: "--tag", Source: "BuildImage"},
				{Name: "--label", Source: "BuildImage"},
				{Name: "--file", Source: "BuildImage"},
				{Name: "--build-arg", Source: "BuildImage"},
				{Name: "--no-cache", Source: "BuildImage"},
				{Name: "--progress", Source: "BuildImage"},
			},
			Source: "BuildImage",
		},
		{
			Subcommand: "image",
			Args:       []string{"inspect"},
			Source:     "InspectImage",
		},
		{
			Subcommand: "version",
			Options: []CLIOption{
				{Name: "--format", Source: "checkCLI"},
			},
			Source: "Preflight",
		},
		{
			Subcommand: "info",
			Options: []CLIOption{
				{Name: "--format", Source: "checkEngine"},
			},
			Source: "Preflight",
		},
		{
			Subcommand: "cp",
			Source:     "CopyFromContainer",
		},
	}
}

// CLIHelpCheck 是单条命令的帮助检查结果。
type CLIHelpCheck struct {
	// Command 是被检查的命令。
	Command CLICommand
	// Status 是结果分类，成功时为 CLIHelpCodeOK。
	Status string
	// Message 是简短说明，已截断并脱敏。
	Message string
	// MissingOptions 是帮助信息中缺失的参数选项。
	MissingOptions []string
	// Duration 是本次检查耗时。
	Duration time.Duration
}

// Succeeded 判断该命令的帮助检查是否通过。
func (c CLIHelpCheck) Succeeded() bool {
	return c.Status == CLIHelpCodeOK
}

// CLIHelpResult 是全部命令帮助检查的结果。
type CLIHelpResult struct {
	// Checks 是按执行顺序排列的单命令结果。
	Checks []CLIHelpCheck
	// MissingOptions 是全部缺失参数选项的去重汇总。
	MissingOptions []string
	// Status 是总体结果分类。
	Status string
	// Message 是总体说明。
	Message string
	// Duration 是总耗时。
	Duration time.Duration
}

// Succeeded 判断全部命令的帮助检查是否通过。
func (r CLIHelpResult) Succeeded() bool {
	return r.Status == CLIHelpCodeOK && len(r.Checks) > 0
}

// CheckCLIHelp 逐条执行 docker 帮助命令并核对参数选项。
//
// 该检查只执行 `docker <command> --help`：不创建容器、不启动工作负载、不下载镜像
// 层，也不执行 docker version、API 探测或任何其他命令。帮助文本同时从标准输出与
// 标准错误读取，因为不同版本的 CLI 会把帮助写到不同流。
func (r *Runner) CheckCLIHelp(
	ctx context.Context,
	commands []CLICommand,
	timeout time.Duration,
) CLIHelpResult {
	result := CLIHelpResult{Status: CLIHelpCodeOK}
	started := time.Now()
	defer func() {
		result.Duration = time.Since(started)
	}()
	if timeout <= 0 {
		timeout = DefaultCLIHelpTimeout
	}
	if len(commands) == 0 {
		result.Status = CLIHelpCodeCommandFailed
		result.Message = "没有需要检查的 Docker 命令"
		return result
	}
	missing := make(map[string]struct{})
	for _, command := range commands {
		check := r.checkOneCLIHelp(ctx, command, timeout)
		result.Checks = append(result.Checks, check)
		for _, option := range check.MissingOptions {
			missing[option] = struct{}{}
		}
		if check.Succeeded() {
			continue
		}
		if result.Status == CLIHelpCodeOK {
			// 总体状态取第一条失败命令的分类，避免后续失败覆盖首要原因。
			result.Status = check.Status
			result.Message = check.Command.Label() + ": " + check.Message
		}
	}
	if len(missing) > 0 {
		options := make([]string, 0, len(missing))
		for option := range missing {
			options = append(options, option)
		}
		sort.Strings(options)
		result.MissingOptions = options
	}
	if result.Status == CLIHelpCodeOK {
		result.Message = fmt.Sprintf(
			"已确认 %d 条 Docker 命令的帮助信息与所需参数选项；未验证 Engine 的实际运行兼容性",
			len(result.Checks),
		)
	}
	return result
}

// checkOneCLIHelp 检查单条命令的帮助信息。
func (r *Runner) checkOneCLIHelp(
	ctx context.Context,
	command CLICommand,
	timeout time.Duration,
) CLIHelpCheck {
	check := CLIHelpCheck{Command: command, Status: CLIHelpCodeOK}
	started := time.Now()
	defer func() {
		check.Duration = time.Since(started)
	}()
	remote, err := dockerCommand(command.Subcommand, command.HelpCommand()[1:]...)
	if err != nil {
		check.Status = CLIHelpCodeCommandFailed
		check.Message = "构造帮助命令失败: " + summarizeOutput(err.Error())
		return check
	}
	run, runErr := r.Run(ctx, command.Label()+" --help", remote, timeout)
	combined := run.Stdout + "\n" + run.Stderr
	if run.Cancelled() {
		check.Status = CLIHelpCodeCancelled
		check.Message = "帮助命令被取消"
		return check
	}
	if run.TimedOut() {
		check.Status = CLIHelpCodeTimeout
		check.Message = "帮助命令超时"
		return check
	}
	if runErr != nil && !run.Certain {
		check.Status = CLIHelpCodeSSHUnavailable
		check.Message = "帮助命令未能确认结束: " + summarizeOutput(runErr.Error())
		return check
	}
	if run.ExitCode == 127 || isCommandNotFoundOutput(combined) {
		check.Status = CLIHelpCodeCommandMissing
		check.Message = "远程 Docker CLI 不支持该命令"
		return check
	}
	if run.ExitCode != 0 {
		check.Status = CLIHelpCodeCommandFailed
		check.Message = fmt.Sprintf(
			"帮助命令退出码 %d: %s",
			run.ExitCode,
			summarizeOutput(combined),
		)
		return check
	}
	text := strings.TrimSpace(combined)
	if text == "" {
		// 退出码为 0 但没有任何帮助文本：无法确认参数是否受支持，不能判为通过。
		check.Status = CLIHelpCodeCommandMissing
		check.Message = "帮助命令没有输出任何内容，无法确认命令与参数"
		return check
	}
	check.MissingOptions = missingOptions(text, command.Options)
	if len(check.MissingOptions) > 0 {
		check.Status = CLIHelpCodeOptionMissing
		check.Message = "帮助信息中缺少必需参数: " + strings.Join(check.MissingOptions, ", ")
		return check
	}
	check.Message = "帮助信息包含所需命令与参数选项"
	return check
}

// isCommandNotFoundOutput 判断输出是否表示命令不存在。
func isCommandNotFoundOutput(output string) bool {
	message := strings.ToLower(output)
	return strings.Contains(message, "docker: '") && strings.Contains(message, "is not a docker command") ||
		strings.Contains(message, "unknown command") ||
		strings.Contains(message, "command not found") ||
		strings.Contains(message, "not a docker command")
}

// missingOptions 返回帮助文本中缺失的选项。
//
// 选项在帮助文本中可能写作 `--label`、`--label list` 或 `--label key=value`，
// 因此按"以该选项开头且后面是空白或行尾"匹配，避免 `--log` 命中 `--logs`。
func missingOptions(helpText string, options []CLIOption) []string {
	if len(options) == 0 {
		return nil
	}
	missing := make([]string, 0, len(options))
	for _, option := range options {
		if optionPresent(helpText, option.Name) {
			continue
		}
		missing = append(missing, option.Name)
	}
	return missing
}

// optionPresent 判断帮助文本中是否出现该选项。
func optionPresent(helpText, name string) bool {
	index := strings.Index(helpText, name)
	for index >= 0 {
		end := index + len(name)
		if end >= len(helpText) {
			return true
		}
		next := helpText[end]
		if next == ' ' || next == '\t' || next == '\n' || next == '\r' ||
			next == ',' || next == '=' || next == ']' || next == ')' {
			return true
		}
		nextIndex := strings.Index(helpText[end:], name)
		if nextIndex < 0 {
			return false
		}
		index = end + nextIndex
	}
	return false
}
