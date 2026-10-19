// Package logging 封装标准库 log/slog，提供统一的结构化日志输出。
//
// 日志输出到调用方给定的写入器（通常为标准错误流），默认使用易读的文本格式。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// 支持的日志级别名称。
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	// DefaultLevel 是未显式配置时使用的日志级别。
	DefaultLevel = LevelInfo
)

// ParseLevel 把配置中的级别名称解析为 slog 级别。
//
// 空字符串按 DefaultLevel 处理，未知级别返回错误。
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case LevelDebug:
		return slog.LevelDebug, nil
	case "", LevelInfo:
		return slog.LevelInfo, nil
	case LevelWarn, "warning":
		return slog.LevelWarn, nil
	case LevelError:
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf(
			"不支持的日志级别 %q（可选 %s、%s、%s、%s）",
			name,
			LevelDebug,
			LevelInfo,
			LevelWarn,
			LevelError,
		)
	}
}

// Logger 是应用统一使用的结构化日志器，支持运行期调整级别。
type Logger struct {
	logger *slog.Logger
	level  *slog.LevelVar
	// wrapped 表示该日志器包装了外部传入的 slog.Logger。
	wrapped bool
}

// New 创建输出到 w 的日志器。
func New(w io.Writer, level string) (*Logger, error) {
	parsed, err := ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(parsed)
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: levelVar})
	return &Logger{logger: slog.New(handler), level: levelVar}, nil
}

// FromSlog 把已有的 slog 日志器包装为应用层日志器。
//
// 该函数用于上层已经持有 slog.Logger 的场景（例如 internal/server）：包装后的日志器
// 与传入日志器共用同一个 handler，因此输出目标与级别完全一致。
//
// 包装结果不接受 SetLevel：级别由调用方持有的 handler 决定，这里不复制也不覆盖它，
// 避免出现"设置了级别却没有生效"的假象。
func FromSlog(logger *slog.Logger) *Logger {
	if logger == nil {
		logger = slog.Default()
	}
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	return &Logger{logger: logger, level: level, wrapped: true}
}

// Slog 返回底层 slog 日志器，供各层记录结构化日志。
func (l *Logger) Slog() *slog.Logger {
	return l.logger
}

// SetLevel 调整日志级别，用于加载配置后应用 app.log_level。
func (l *Logger) SetLevel(level string) error {
	if l.wrapped {
		// 包装的日志器级别由外部 handler 决定，这里只校验取值是否合法。
		_, err := ParseLevel(level)
		return err
	}
	parsed, err := ParseLevel(level)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	l.level.Set(parsed)
	return nil
}

// Level 返回当前日志级别名称。
func (l *Logger) Level() string {
	switch l.level.Level() {
	case slog.LevelDebug:
		return LevelDebug
	case slog.LevelWarn:
		return LevelWarn
	case slog.LevelError:
		return LevelError
	default:
		return LevelInfo
	}
}
