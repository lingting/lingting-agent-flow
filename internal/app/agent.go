package app

import (
	"path/filepath"
	"strings"

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
