package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lingting/agentflow/internal/domain"
)

// 验证执行的默认值。
const (
	// DefaultVerificationTimeout 是单条验证命令的默认超时。
	DefaultVerificationTimeout = 10 * time.Minute
	// DefaultVerificationOutputBytes 是单条验证命令保留的输出上限。
	DefaultVerificationOutputBytes = 16 << 10
	// verificationErrorLimit 是失败摘要中保留的错误行数。
	verificationErrorLimit = 6
	// verificationErrorLineLimit 是单行错误摘要保留的字符数。
	verificationErrorLineLimit = 400
)

// verificationRunner 按顺序执行确定性验证命令并结构化记录结果。
//
// 该类型只负责"执行并记录"，判定由 domain.NewVerificationResult 统一给出：
// 必需命令失败时无论 Agent 返回什么，验证结果都不会是通过。
type verificationRunner struct {
	commands CommandPort
	// defaultTimeout 是命令未声明超时时的默认值。
	defaultTimeout time.Duration
	// defaultOutputBytes 是命令未声明输出上限时的默认值。
	defaultOutputBytes int
}

// newVerificationRunner 构造确定性验证执行器。
func newVerificationRunner(
	commands CommandPort,
	taskTimeout time.Duration,
) *verificationRunner {
	timeout := taskTimeout
	if timeout <= 0 {
		timeout = DefaultVerificationTimeout
	}
	return &verificationRunner{
		commands:           commands,
		defaultTimeout:     timeout,
		defaultOutputBytes: DefaultVerificationOutputBytes,
	}
}

// run 按声明顺序执行全部命令。
//
// 任何一条命令返回错误时仍然继续执行其余命令：完整记录全部结果比提前退出更有
// 价值，调用方可以据此判断是命令失败还是执行环境不可用。
func (r *verificationRunner) run(
	ctx context.Context,
	containerID string,
	spec domain.VerificationSpec,
	workDir string,
) ([]domain.CommandResult, error) {
	results := make([]domain.CommandResult, 0, len(spec.Commands))
	for _, command := range spec.Commands {
		if err := ctx.Err(); err != nil {
			results = append(results, cancelledResult(command, err))
			continue
		}
		result, err := r.runOne(ctx, containerID, command, workDir)
		if err != nil && !result.Certain {
			// 结果不确定时保留 uncertain 标记，由判定逻辑判为 inconclusive。
			result.Name = command.Name
			result.Command = describeCommand(command)
			result.Required = command.IsRequired()
			result.AllowTimeoutPass = command.AllowTimeoutPass
			result.AllowedExitCodes = command.AllowedExitCodes
			results = append(results, result)
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

// runOne 执行一条命令并转换为结构化结果。
func (r *verificationRunner) runOne(
	ctx context.Context,
	containerID string,
	command domain.VerificationCommand,
	workDir string,
) (domain.CommandResult, error) {
	timeout := time.Duration(command.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}
	maxOutput := command.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = r.defaultOutputBytes
	}
	run := CommandRun{
		Command:        command,
		WorkDir:        firstNonEmpty(command.WorkDir, workDir),
		Timeout:        timeout,
		MaxOutputBytes: maxOutput,
	}
	result, err := r.commands.Exec(ctx, containerID, run)
	if err != nil && result.Certain && result.ExitCode == 0 {
		// 命令已确认结束但执行层报告错误：保留不确定语义，不当作通过。
		result.Certain = false
	}
	result.Name = command.Name
	result.Command = describeCommand(command)
	result.Required = command.IsRequired()
	result.AllowTimeoutPass = command.AllowTimeoutPass
	result.AllowedExitCodes = command.AllowedExitCodes
	result.Stdout = truncateOutput(result.Stdout, maxOutput)
	result.Stderr = truncateOutput(result.Stderr, maxOutput)
	return result, err
}

// cancelledResult 在调用方取消后构造一条取消结果。
func cancelledResult(command domain.VerificationCommand, cause error) domain.CommandResult {
	return domain.CommandResult{
		Name:             command.Name,
		Command:          describeCommand(command),
		Required:         command.IsRequired(),
		AllowTimeoutPass: command.AllowTimeoutPass,
		AllowedExitCodes: command.AllowedExitCodes,
		ExitCode:         -1,
		Cancelled:        true,
		Certain:          true,
		Stderr:           "命令未执行: " + cause.Error(),
	}
}

// describeCommand 返回命令的可读形式，用于结果记录与失败证据。
func describeCommand(command domain.VerificationCommand) string {
	parts := make([]string, 0, len(command.Args)+1)
	parts = append(parts, command.Command)
	parts = append(parts, command.Args...)
	return strings.Join(parts, " ")
}

// truncateOutput 按字节上限裁剪输出，并标记为已截断。
//
// 裁剪不会切断 UTF-8 字符：超出上限时逐字节回退到完整字符边界。
func truncateOutput(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	truncated := value[:limit]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "\n…（输出已截断）"
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// commandFailureSummary 生成命令失败的证据摘要。
func commandFailureSummary(result domain.CommandResult) string {
	if result.Passed() {
		return ""
	}
	switch {
	case result.Cancelled:
		return result.Name + ": 命令被取消，未得到可用结果"
	case !result.Certain:
		return result.Name + ": 命令执行结果无法确认"
	case result.TimedOut:
		return fmt.Sprintf("%s: 命令超时（退出码 %d）", result.Name, result.ExitCode)
	}
	detail := summarizeCommandOutput(result.Stderr, result.Stdout)
	if detail == "" {
		return fmt.Sprintf("%s: 命令以退出码 %d 结束", result.Name, result.ExitCode)
	}
	return fmt.Sprintf("%s: 退出码 %d: %s", result.Name, result.ExitCode, detail)
}

// summarizeCommandOutput 保留末尾少量错误行作为证据。
func summarizeCommandOutput(stderr, stdout string) string {
	source := strings.TrimSpace(stderr)
	if source == "" {
		source = strings.TrimSpace(stdout)
	}
	if source == "" {
		return ""
	}
	lines := strings.Split(source, "\n")
	if len(lines) > verificationErrorLimit {
		lines = lines[len(lines)-verificationErrorLimit:]
	}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > verificationErrorLineLimit {
			trimmed = trimmed[:verificationErrorLineLimit] + "…"
		}
		lines[index] = trimmed
	}
	return strings.Join(lines, " | ")
}

// summarizeVerification 生成验证结果的单行摘要。
func summarizeVerification(result domain.VerificationResult) string {
	switch result.Verdict {
	case domain.VerificationVerdictPassed:
		return fmt.Sprintf("验证通过（%d 条命令）", len(result.CommandResults))
	case domain.VerificationVerdictInconclusive:
		return "验证结果不确定: " + result.FailureSummary
	default:
		return "验证失败: " + result.FailureSummary
	}
}
