package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// CheckExecutionRequest 是一条待执行的确定性检查。
//
// 它只携带已经过校验的字段：命令以可执行文件 + 参数数组表达，工作目录必须是
// 容器内的绝对路径，调用方不能指定宿主机目录。
type CheckExecutionRequest struct {
	// CheckID 是检查的稳定标识，用于结果引用与日志关联。
	CheckID string
	// CheckName 是检查名称，用于诊断。
	CheckName string
	// Command 是可执行文件路径或 PATH 中的名称。
	Command string
	// Args 是命令参数，逐项传递，不经过 shell。
	Args []string
	// WorkDir 是容器内执行目录，必须位于 /workspace 之下。
	WorkDir string
	// Timeout 是执行超时。
	Timeout time.Duration
	// MaxOutputBytes 是保留的输出字节上限。
	MaxOutputBytes int
}

// CheckExecutionResult 是一条检查的结构化执行结果。
//
// 它必须区分"命令正常结束"、"启动失败"、"超时"与"取消"：这些情况不能被压缩成
// 一个布尔值，否则失败证据无法解释。
type CheckExecutionResult struct {
	// ExitCode 是命令退出码，未能取得时为 -1。
	ExitCode int
	// Stdout 与 Stderr 是分别捕获的标准输出与标准错误。
	Stdout string
	Stderr string
	// Truncated 表示输出因超过上限被截断。
	Truncated bool
	// Duration 是执行耗时。
	Duration time.Duration
	// TimedOut 表示命令因超时被终止。
	TimedOut bool
	// Cancelled 表示命令被调用方取消。
	Cancelled bool
	// Certain 表示可以确认命令已经结束；为 false 时结果不可信。
	Certain bool
	// Started 表示命令确实被启动过：为 false 时退出码与输出都无意义。
	Started bool
	// Error 是执行层返回的原始错误，仅用于诊断。
	Error error
}

// CheckExecutor 是确定性检查的执行器抽象。
//
// 验证引擎只依赖该接口，不感知 Docker、SSH 或具体操作系统：接入新的执行环境时
// 只需要提供新的实现，验证业务逻辑（顺序执行、结果记录、结论计算）完全复用。
//
// 实现约束：
//   - 不得通过 shell 字符串拼接执行命令，参数必须逐项传递；
//   - 必须在请求给定的 WorkDir 内执行，不得退化为服务进程的当前目录；
//   - 必须响应 ctx 取消，并在超时后终止实际进程；
//   - 必须分别捕获标准输出与标准错误，并遵守 MaxOutputBytes 上限；
//   - 必须区分"命令以非零码正常结束"与"启动失败、超时、取消"。
type CheckExecutor interface {
	// Name 返回执行环境类型标识，例如 container。它会被写入验证记录。
	Name() string
	// Exec 执行一条检查并返回结构化结果。
	//
	// 返回 error 表示执行层自身无法完成本次执行（例如容器不可用）；此时结果中的
	// Started / Certain 字段说明命令是否真的跑过。
	Exec(ctx context.Context, request CheckExecutionRequest) (CheckExecutionResult, error)
}

// 输出摘要与日志落盘的默认参数。
const (
	// defaultValidationCheckTimeout 是未声明超时时的单条检查超时。
	defaultValidationCheckTimeout = 10 * time.Minute
	// defaultValidationOutputBytes 是未声明输出上限时的保留字节数。
	defaultValidationOutputBytes = 16 << 10
	// checkOutputSummaryBytes 是写入数据库的输出摘要上限。
	checkOutputSummaryBytes = 2 << 10
	// checkOutputLogBytes 是落盘输出文件的字节上限。
	checkOutputLogBytes = 1 << 20
)

// checkOutputWriter 把检查输出落到本机持久化目录。
//
// 输出与数据库分离：数据库只保存位置与摘要，完整内容由文件承载。
// 归档失败必须被单独记录，不能改写命令的退出码与判定。
type checkOutputWriter struct {
	// root 是本机输出根目录，位于数据目录之下。
	root string
}

// newCheckOutputWriter 构造输出写入器。
func newCheckOutputWriter(root string) *checkOutputWriter {
	return &checkOutputWriter{root: strings.TrimSpace(root)}
}

// archive 把一次检查的 stdout 与 stderr 分别写入独立文件。
//
// 两个流分别落盘：混流会丢失"哪一行来自哪条流"的上下文，出错时无法区分真实输出
// 与诊断信息。
func (w *checkOutputWriter) archive(
	runID domain.ID,
	checkID string,
	result CheckExecutionResult,
) (stdoutPath, stderrPath, archiveError string) {
	if w == nil || w.root == "" {
		return "", "", "未配置检查输出目录"
	}
	dir := filepath.Join(w.root, runID.String(), sanitizePathSegment(checkID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", "创建检查输出目录失败: " + err.Error()
	}
	var failures []string
	stdoutPath, writeErr := writeBoundedFile(
		filepath.Join(dir, "stdout.log"),
		result.Stdout,
		result.Truncated,
	)
	if writeErr != nil {
		failures = append(failures, "写入标准输出失败: "+writeErr.Error())
		stdoutPath = ""
	}
	stderrPath, writeErr = writeBoundedFile(
		filepath.Join(dir, "stderr.log"),
		result.Stderr,
		result.Truncated,
	)
	if writeErr != nil {
		failures = append(failures, "写入标准错误失败: "+writeErr.Error())
		stderrPath = ""
	}
	if len(failures) > 0 {
		return stdoutPath, stderrPath, strings.Join(failures, "；")
	}
	return stdoutPath, stderrPath, ""
}

// writeBoundedFile 写入有字节上限的日志文件，截断时在末尾留下明确标记。
func writeBoundedFile(path, content string, alreadyTruncated bool) (string, error) {
	truncated := alreadyTruncated
	if len(content) > checkOutputLogBytes {
		content = content[:checkOutputLogBytes]
		truncated = true
	}
	if truncated {
		content += "\n…（输出已超过上限被截断）\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// sanitizePathSegment 把标识转换为安全的单层目录名。
//
// 检查标识来自配置，虽然已经过校验，这里仍然拒绝路径分隔符与父目录引用，
// 保证落盘位置永远在输出根目录之内。
func sanitizePathSegment(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "check"
	}
	var builder strings.Builder
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteRune('-')
		}
	}
	cleaned := builder.String()
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "check"
	}
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	return cleaned
}

// summarizeOutput 生成写入数据库的输出摘要。
//
// 摘要只保留末尾若干行：失败证据通常出现在最后，而完整内容已经落盘。
func summarizeOutput(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > checkOutputSummaryBytes {
		trimmed = trimmed[len(trimmed)-checkOutputSummaryBytes:]
	}
	return trimmed
}

// evaluateCheckStatus 依据执行结果与检查规则计算单项检查状态。
//
// 这是"单项判定"的唯一入口，与最终结论计算（domain.DecideValidation）分离：
// 单项只看这一条命令的事实，整体只看全部必需检查的事实。
func evaluateCheckStatus(
	definition domain.ValidationCheckDefinition,
	result CheckExecutionResult,
	execErr error,
) (domain.ValidationCheckStatus, string) {
	if result.Cancelled {
		return domain.ValidationCheckStatusCancelled, "命令被取消"
	}
	if result.TimedOut {
		return domain.ValidationCheckStatusTimedOut, "命令执行超时"
	}
	if !result.Started {
		// 命令根本没有启动：这不是"命令失败"，而是"没有拿到证据"。
		reason := "命令无法启动"
		if execErr != nil {
			reason += ": " + execErr.Error()
		}
		return domain.ValidationCheckStatusError, reason
	}
	if !result.Certain {
		reason := "命令执行结果无法确认"
		if execErr != nil {
			reason += ": " + execErr.Error()
		}
		return domain.ValidationCheckStatusError, reason
	}
	if !definition.AcceptsExitCode(result.ExitCode) {
		return domain.ValidationCheckStatusFailed, fmt.Sprintf(
			"退出码 %d 不在允许范围内",
			result.ExitCode,
		)
	}
	if expected := definition.ExpectedStdoutContains; expected != "" &&
		!strings.Contains(result.Stdout, expected) {
		return domain.ValidationCheckStatusFailed, "标准输出未包含预期内容: " + expected
	}
	if expected := definition.ExpectedStderrContains; expected != "" &&
		!strings.Contains(result.Stderr, expected) {
		return domain.ValidationCheckStatusFailed, "标准错误未包含预期内容: " + expected
	}
	return domain.ValidationCheckStatusPassed, ""
}

// checkTimeoutOf 返回一条检查生效的超时时间。
func checkTimeoutOf(definition domain.ValidationCheckDefinition, spec domain.ValidationSpec) time.Duration {
	if definition.TimeoutSeconds > 0 {
		return time.Duration(definition.TimeoutSeconds) * time.Second
	}
	if spec.DefaultTimeoutSeconds > 0 {
		return time.Duration(spec.DefaultTimeoutSeconds) * time.Second
	}
	return defaultValidationCheckTimeout
}

// checkMaxOutputOf 返回一条检查生效的输出上限。
func checkMaxOutputOf(definition domain.ValidationCheckDefinition, spec domain.ValidationSpec) int {
	if definition.MaxOutputBytes > 0 {
		return definition.MaxOutputBytes
	}
	if spec.DefaultMaxOutputBytes > 0 {
		return spec.DefaultMaxOutputBytes
	}
	return defaultValidationOutputBytes
}
