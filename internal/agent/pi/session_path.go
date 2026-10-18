package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Session 文件在本机目录布局中的固定名称。
const (
	// sessionFileName 是原始 Session 文件名。
	sessionFileName = "session.jsonl"
	// htmlFileName 是导出的 HTML 文件名。
	htmlFileName = "session.html"
	// fallbackDirName 是缺少任务与步骤归属时使用的目录名。
	fallbackDirName = "unscoped"
)

// SessionLayout 描述一次调用的 Session 目录归属。
//
// 目录按任务、步骤与调用标识分层，避免不同调用共用同一个 Session 文件；
// 缺少上层标识时退化为调用标识，仍能保证路径唯一。
type SessionLayout struct {
	// TaskID 是任务标识，可为空。
	TaskID string
	// StepID 是步骤标识，可为空。
	StepID string
	// AttemptID 是执行尝试标识，可为空。
	AttemptID string
	// SessionID 是内部会话标识，必填。
	SessionID string
	// InvocationID 是本次调用的唯一标识，非空时作为最内层目录。
	//
	// 同一次会话内可能发生多次调用，用调用标识分层可以保证每次调用都得到
	// 独立的 Session 文件，不会互相覆盖。
	InvocationID string
}

// Validate 校验布局是否可用。
func (l SessionLayout) Validate() error {
	if strings.TrimSpace(l.SessionID) == "" {
		return fmt.Errorf("pi: Session 布局缺少会话标识")
	}
	return nil
}

// Directory 返回本次调用在 Session 根目录下的相对目录。
func (l SessionLayout) Directory() (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	segments := make([]string, 0, 4)
	if task := sanitizeSegment(l.TaskID); task != "" {
		segments = append(segments, task)
		if step := sanitizeSegment(l.StepID); step != "" {
			segments = append(segments, step)
			if attempt := sanitizeSegment(l.AttemptID); attempt != "" {
				segments = append(segments, attempt)
			}
		}
	}
	if len(segments) == 0 {
		segments = append(segments, fallbackDirName)
	}
	segments = append(segments, sanitizeSegment(l.SessionID))
	if invocation := sanitizeSegment(l.InvocationID); invocation != "" {
		segments = append(segments, invocation)
	}
	return filepath.Join(segments...), nil
}

// Paths 返回本次调用的原始 JSONL 与 HTML 绝对路径。
func (l SessionLayout) Paths(root string) (SessionPaths, error) {
	base := strings.TrimSpace(root)
	if base == "" {
		return SessionPaths{}, fmt.Errorf("pi: Session 根目录不能为空")
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return SessionPaths{}, fmt.Errorf("pi: 解析 Session 根目录失败: %w", err)
	}
	directory, err := l.Directory()
	if err != nil {
		return SessionPaths{}, err
	}
	dir := filepath.Join(absolute, directory)
	return SessionPaths{
		Dir:   dir,
		JSONL: filepath.Join(dir, sessionFileName),
		HTML:  filepath.Join(dir, htmlFileName),
	}, nil
}

// SessionPaths 是一次调用的 Session 文件路径。
type SessionPaths struct {
	// Dir 是本次调用的 Session 目录。
	Dir string
	// JSONL 是原始 Session 文件路径。
	JSONL string
	// HTML 是导出的 HTML 路径。
	HTML string
}

// EnsureDir 创建 Session 目录，已存在时不报错。
func (p SessionPaths) EnsureDir() error {
	if strings.TrimSpace(p.Dir) == "" {
		return fmt.Errorf("pi: Session 目录不能为空")
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return fmt.Errorf("pi: 创建 Session 目录失败: %s: %w", p.Dir, err)
	}
	return nil
}

// sanitizeSegment 清洗路径片段，避免标识中的特殊字符影响目录结构。
func sanitizeSegment(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(trimmed))
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	cleaned := builder.String()
	// 纯点号片段会被文件系统解释为当前目录或上级目录，替换为下划线。
	if strings.Trim(cleaned, ".") == "" {
		return strings.Repeat("_", len(cleaned))
	}
	return cleaned
}
