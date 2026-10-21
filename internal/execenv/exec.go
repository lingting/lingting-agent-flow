package execenv

import (
	"fmt"
	"strings"
	"time"
)

// 命令执行的默认限制。
const (
	// DefaultTimeout 是未显式声明超时时的单条命令上限。
	DefaultTimeout = 30 * time.Minute
	// DefaultMaxOutputBytes 是标准输出与标准错误各自保留的字节上限。
	DefaultMaxOutputBytes = 64 << 10
)

// ExecSpec 是一条待执行的命令。
//
// 命令必须以"可执行文件 + 参数数组"表达：禁止把参数拼成 shell 字符串，
// 也禁止把所有命令统一交给 sh -c 执行。环境变量只允许调用方显式声明，
// 执行层不会自动继承本机环境。
type ExecSpec struct {
	// Command 是可执行文件路径或 PATH 中的名称。
	Command string
	// Args 是命令参数，逐项传递，不经过 shell。
	Args []string
	// WorkDir 是容器内执行目录，空值使用环境的工作目录。
	WorkDir string
	// Env 是显式传入的环境变量，形如 KEY=VALUE。
	Env []string
	// Stdin 非空时写入命令的标准输入，并在写入后关闭。
	Stdin []byte
	// Timeout 是本次执行的超时，零值使用 DefaultTimeout。
	Timeout time.Duration
	// MaxOutputBytes 是标准输出与标准错误各自保留的字节上限，零值使用默认值。
	MaxOutputBytes int
}

// Resolve 补齐默认值并校验命令。
//
// 校验内容：命令不能为空，工作目录必须落在环境工作区之内，环境变量必须是
// KEY=VALUE 形式且键名合法。
func (s ExecSpec) Resolve(environment Environment) (ExecSpec, error) {
	resolved := s
	resolved.Command = strings.TrimSpace(s.Command)
	if resolved.Command == "" {
		return ExecSpec{}, fmt.Errorf("%w: 命令不能为空", ErrInvalidValue)
	}
	workDir, err := environment.EnsureWorkDir(s.WorkDir)
	if err != nil {
		return ExecSpec{}, err
	}
	resolved.WorkDir = workDir
	if resolved.Timeout <= 0 {
		resolved.Timeout = DefaultTimeout
	}
	if resolved.MaxOutputBytes <= 0 {
		resolved.MaxOutputBytes = DefaultMaxOutputBytes
	}
	normalized, err := normalizeEnv(s.Env)
	if err != nil {
		return ExecSpec{}, err
	}
	resolved.Env = normalized
	return resolved, nil
}

// Argv 返回完整参数列表，第一个元素是命令本身。
func (s ExecSpec) Argv() []string {
	argv := make([]string, 0, len(s.Args)+1)
	argv = append(argv, s.Command)
	return append(argv, s.Args...)
}

// normalizeEnv 校验并规范化环境变量列表。
func normalizeEnv(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key, _, found := strings.Cut(trimmed, "=")
		if !found {
			return nil, fmt.Errorf("%w: 环境变量 %q 必须是 KEY=VALUE 形式", ErrInvalidValue, value)
		}
		if err := validateEnvKey(key); err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			// 同名变量重复出现会让实际生效值取决于顺序，直接拒绝。
			return nil, fmt.Errorf("%w: 环境变量 %s 重复声明", ErrInvalidValue, key)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

// validateEnvKey 校验环境变量键名。
func validateEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: 环境变量名不能为空", ErrInvalidValue)
	}
	for index, char := range key {
		switch {
		case char >= 'A' && char <= 'Z':
		case char >= 'a' && char <= 'z':
		case char == '_':
		case char >= '0' && char <= '9':
			if index == 0 {
				return fmt.Errorf("%w: 环境变量名 %q 不能以数字开头", ErrInvalidValue, key)
			}
		default:
			return fmt.Errorf("%w: 环境变量名 %q 含有非法字符", ErrInvalidValue, key)
		}
	}
	return nil
}

// ExecStatus 是命令执行的结束状态。
//
// 必须与退出码分开表达：只凭退出码无法区分"命令以非零码正常结束"与
// "命令被取消、超时或结果无法确认"。
type ExecStatus string

// 命令执行状态取值。
const (
	// ExecStatusSucceeded 表示命令以退出码 0 正常结束。
	ExecStatusSucceeded ExecStatus = "succeeded"
	// ExecStatusExitError 表示命令正常结束但退出码非零。
	ExecStatusExitError ExecStatus = "exit_error"
	// ExecStatusTimeout 表示命令因超时被中断。
	ExecStatusTimeout ExecStatus = "timeout"
	// ExecStatusCancelled 表示命令因调用方取消被中断。
	ExecStatusCancelled ExecStatus = "cancelled"
	// ExecStatusConnectionError 表示执行通道不可用，命令是否执行无法确认。
	ExecStatusConnectionError ExecStatus = "connection_error"
	// ExecStatusUnknown 表示结果无法确认。
	ExecStatusUnknown ExecStatus = "unknown"
)

// String 返回命令执行状态的字符串形式。
func (s ExecStatus) String() string {
	return string(s)
}

// ExecResult 是一条命令的执行结果。
//
// Started 与 Certain 是两个独立事实：前者说明命令是否真的被启动过，
// 后者说明执行层能否确认命令已经结束。缺少真实运行痕迹时不得判定为成功。
type ExecResult struct {
	// Command 是实际执行的命令描述，用于诊断，不含凭据。
	Command string
	// ExitCode 是命令退出码，未能取得时为 -1。
	ExitCode int
	// Stdout 与 Stderr 分别保存标准输出与标准错误。
	Stdout string
	// Stderr 是标准错误。
	Stderr string
	// Truncated 表示输出因超过上限被截断。
	Truncated bool
	// Duration 是执行耗时。
	Duration time.Duration
	// Status 是结束状态。
	Status ExecStatus
	// Started 表示命令确实被启动过。
	Started bool
	// Certain 表示可以确认命令已经结束。
	Certain bool
	// StdinBytes 是成功写入标准输入的字节数。
	StdinBytes int64
	// StdinError 是标准输入写入失败的说明。
	StdinError string
}

// Succeeded 判断命令是否以退出码 0 正常结束。
func (r ExecResult) Succeeded() bool {
	return r.Certain && r.Status == ExecStatusSucceeded && r.ExitCode == 0
}

// TimedOut 判断命令是否因超时被中断。
func (r ExecResult) TimedOut() bool {
	return r.Status == ExecStatusTimeout
}

// Cancelled 判断命令是否因调用方取消被中断。
func (r ExecResult) Cancelled() bool {
	return r.Status == ExecStatusCancelled
}

// Uncertain 判断执行结果是否无法确认。
func (r ExecResult) Uncertain() bool {
	return !r.Certain
}

// Summary 返回可写入日志的简短摘要。
func (r ExecResult) Summary() string {
	detail := strings.TrimSpace(r.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(r.Stdout)
	}
	if detail == "" {
		return fmt.Sprintf("状态 %s，退出码 %d", r.Status, r.ExitCode)
	}
	lines := strings.Split(detail, "\n")
	const keepLines = 4
	if len(lines) > keepLines {
		lines = lines[len(lines)-keepLines:]
	}
	joined := strings.Join(lines, " | ")
	const limit = 600
	if len(joined) > limit {
		joined = joined[:limit] + "..."
	}
	return fmt.Sprintf("状态 %s，退出码 %d: %s", r.Status, r.ExitCode, joined)
}
