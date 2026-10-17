package app

import (
	"context"
	"fmt"

	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/internal/storage/sqlite"
)

// OpenStore 按配置打开数据库并返回存储入口。
//
// 打开过程会自动创建父目录并执行迁移；调用方负责在结束后调用 Close。
func (a *App) OpenStore(ctx context.Context, cfg *config.Config) (storage.Store, error) {
	if cfg == nil {
		return nil, fmt.Errorf("打开数据库失败: 配置为空")
	}
	path := cfg.Storage.DatabasePath
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	a.logger.Slog().DebugContext(ctx, "数据库已打开", "path", db.Path())
	return db, nil
}
