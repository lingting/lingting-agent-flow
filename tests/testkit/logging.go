// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件提供测试专用日志器：输出写进当前标准输出，由统一测试执行器落到该用例的产物日志，
// 使应用服务在测试中产生的日志可以事后复核，而不是被各包的丢弃实现吞掉。
package testkit

import (
	"log/slog"
	"os"

	"github.com/lingting/agentflow/internal/logging"
)

// Logger 返回写入当前标准输出的日志器。
//
// 它替代各测试包里各自实现的 discardWriter / nopWriter：应用服务的日志与用例输出一起
// 落到用例产物，排查失败时不必再临时改代码打开日志。
func Logger(level string) (*logging.Logger, error) {
	return logging.New(os.Stdout, level)
}

// Slog 返回写入当前标准输出的结构化日志器。
func Slog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}
