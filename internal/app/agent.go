package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/internal/config"
)

// SessionRoot 返回本机 Session 持久化根目录。
//
// 目录与数据库放在同一数据目录下，避免 Session 文件散落在系统临时目录。本版本
// 所有 Agent 调用都在远程容器内执行，Session 文件回传到该目录后仍然可读。
func SessionRoot(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	database := strings.TrimSpace(cfg.Storage.DatabasePath)
	if database == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(database), "sessions")
}

// AgentCheck 检查 Pi 可执行程序与非交互式执行所需的参数支持。
//
// model 为空时只检查安装与参数支持，不查询模型。该检查只验证本机是否具备可用的
// Pi CLI（用于诊断与容器镜像准备参考），不发起任何模型调用。
func (a *App) AgentCheck(ctx context.Context, model string) (pi.CheckReport, error) {
	report, err := pi.Check(ctx, pi.Options{}, model)
	if err != nil {
		return report, fmt.Errorf("Agent 检查失败: %w", err)
	}
	a.logger.Slog().DebugContext(
		ctx,
		"Agent 检查通过",
		slog.String("executable", report.Executable),
		slog.String("version", report.Version),
		slog.String("model", report.Model),
	)
	return report, nil
}
