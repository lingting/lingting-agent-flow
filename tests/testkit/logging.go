// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件提供测试专用日志器：输出统一写入本次运行日志文件（test.log），
// 使应用服务在测试中产生的日志可以事后复核，而不是被各包的丢弃实现吞掉。
package testkit

import (
	"io"
	"log/slog"
	"os"

	"github.com/lingting/agentflow/internal/logging"
)

// Logger 返回写入本次运行日志文件的日志器。
//
// 它替代各测试包里各自实现的 discardWriter / nopWriter：应用服务的日志会与控制台
// 输出一起落到 test.log，排查失败时不必再临时改代码打开日志。
func Logger(level string) (*logging.Logger, error) {
	return logging.New(logWriter(), level)
}

// Slog 返回写入本次运行日志文件的结构化日志器。
func Slog() *slog.Logger {
	return slog.New(slog.NewTextHandler(logWriter(), nil))
}

// logWriter 返回当前测试运行的日志写入目标。
//
// 尚未初始化（没有通过 Main 启动）时退回当前标准输出，避免日志静默丢失。这里必须动态
// 读取 os.Stdout：统一测试执行器在每条用例执行期间把标准输出指向该用例的产物日志，用例
// 结束后指向空设备；写成包初始化时捕获的标准输出会把日志灌进 worker 的控制通道。
func logWriter() io.Writer {
	if runLogSink != nil {
		return runLogSink
	}
	return os.Stdout
}
