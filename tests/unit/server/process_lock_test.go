// Package server_test 验证服务端进程锁的真实操作系统语义。
//
// 用例直接操作锁文件与两个独立锁实例：断言的是"两个进程不可能同时持有同一个锁文件"，
// 而不是进程内 Mutex 的行为。
package server_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
)

// TestProcessLockRejectsSecondInstance 验证同一数据库路径只允许一个锁实例。
func TestProcessLockRejectsSecondInstance(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "data", "agentflow.db")

	first, err := server.NewProcessLock(databasePath)
	if err != nil {
		t.Fatalf("构造第一个锁失败: %v", err)
	}
	if err := first.TryAcquire(); err != nil {
		t.Fatalf("第一个锁应当取得成功: %v", err)
	}
	if _, statErr := os.Stat(first.Path()); statErr != nil {
		t.Fatalf("取得锁后锁文件必须存在: %v", statErr)
	}

	second, err := server.NewProcessLock(databasePath)
	if err != nil {
		t.Fatalf("构造第二个锁失败: %v", err)
	}
	acquireErr := second.TryAcquire()
	if !errors.Is(acquireErr, server.ErrServerAlreadyRunning) {
		t.Fatalf("第二个锁必须返回已有实例错误，实际 %v", acquireErr)
	}
	if !strings.Contains(acquireErr.Error(), second.Path()) {
		t.Fatalf("错误信息必须包含锁文件路径 %s，实际 %v", second.Path(), acquireErr)
	}
	if second.Path() != first.Path() {
		t.Fatalf("两个实例必须指向同一个锁文件: %s / %s", first.Path(), second.Path())
	}

	// 第一个锁释放后第二个可以取得；锁文件本身仍然存在。
	if err := first.Release(); err != nil {
		t.Fatalf("释放第一个锁失败: %v", err)
	}
	if _, statErr := os.Stat(second.Path()); statErr != nil {
		t.Fatalf("释放锁不得删除锁文件: %v", statErr)
	}
	if err := second.TryAcquire(); err != nil {
		t.Fatalf("第一个锁释放后第二个应当可以取得: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("释放第二个锁失败: %v", err)
	}
	if _, statErr := os.Stat(second.Path()); statErr != nil {
		t.Fatalf("再次释放后锁文件仍必须存在: %v", statErr)
	}
}

// TestServerLockPathIsStableForSameDatabase 验证同一数据库文件的相对与绝对路径得到同一锁文件。
func TestServerLockPathIsStableForSameDatabase(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "agentflow.db")
	absolute, err := server.ServerLockPath(databasePath)
	if err != nil {
		t.Fatalf("计算绝对路径锁文件失败: %v", err)
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取工作目录失败: %v", err)
	}
	relative, relErr := filepath.Rel(workingDir, databasePath)
	if relErr == nil && !strings.Contains(relative, "..") {
		relativeLock, lockErr := server.ServerLockPath(relative)
		if lockErr != nil {
			t.Fatalf("计算相对路径锁文件失败: %v", lockErr)
		}
		if relativeLock != absolute {
			t.Fatalf("相对路径必须得到同一锁文件: %s / %s", relativeLock, absolute)
		}
	}

	// 含冗余段的路径同样收敛到同一个锁文件。
	messy := filepath.Join(filepath.Dir(databasePath), "nested", "..", "agentflow.db")
	messyLock, err := server.ServerLockPath(messy)
	if err != nil {
		t.Fatalf("计算冗余路径锁文件失败: %v", err)
	}
	if messyLock != absolute {
		t.Fatalf("冗余路径必须得到同一锁文件: %s / %s", messyLock, absolute)
	}
	if !strings.HasSuffix(absolute, ".server.lock") {
		t.Fatalf("锁文件必须使用 .server.lock 后缀: %s", absolute)
	}
}

// TestServerRunDoesNotStartWhenLockHeld 验证锁被占用时不会进入 Server 启动流程。
func TestServerRunDoesNotStartWhenLockHeld(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "agentflow.db")
	holder, err := server.NewProcessLock(databasePath)
	if err != nil {
		t.Fatalf("构造持有者锁失败: %v", err)
	}
	if err := holder.TryAcquire(); err != nil {
		t.Fatalf("持有者取得锁失败: %v", err)
	}
	t.Cleanup(func() {
		if releaseErr := holder.Release(); releaseErr != nil {
			t.Errorf("释放持有者锁失败: %v", releaseErr)
		}
	})

	var opened atomic.Int64
	instance, err := server.New(server.Options{
		Logger: discardLogger(),
		OpenStore: func(ctx context.Context, cfg *config.Config) (storage.Store, error) {
			opened.Add(1)
			return nil, errors.New("锁被占用时不得打开数据库")
		},
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	if err != nil {
		t.Fatalf("构造服务实例失败: %v", err)
	}
	runErr := instance.Run(context.Background(), func(context.Context) (*config.Config, error) {
		return &config.Config{Storage: config.StorageConfig{DatabasePath: databasePath}}, nil
	})
	if !errors.Is(runErr, server.ErrServerAlreadyRunning) {
		t.Fatalf("锁被占用时必须返回已有实例错误，实际 %v", runErr)
	}
	if opened.Load() != 0 {
		t.Fatal("锁未取得时不得打开数据库")
	}
	// 持有者的锁必须不受影响。
	other, err := server.NewProcessLock(databasePath)
	if err != nil {
		t.Fatalf("构造第三个锁失败: %v", err)
	}
	if acquireErr := other.TryAcquire(); !errors.Is(acquireErr, server.ErrServerAlreadyRunning) {
		t.Fatalf("持有者的锁必须仍然有效，实际 %v", acquireErr)
	}
}

// TestServerRunReleasesLockWhenStartupFails 验证启动失败后锁被释放。
func TestServerRunReleasesLockWhenStartupFails(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "agentflow.db")
	instance, err := server.New(server.Options{
		Logger: discardLogger(),
		OpenStore: func(ctx context.Context, cfg *config.Config) (storage.Store, error) {
			return nil, errors.New("数据库不可用")
		},
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	if err != nil {
		t.Fatalf("构造服务实例失败: %v", err)
	}
	runErr := instance.Run(context.Background(), func(context.Context) (*config.Config, error) {
		return &config.Config{Storage: config.StorageConfig{DatabasePath: databasePath}}, nil
	})
	if !errors.Is(runErr, server.ErrStartup) {
		t.Fatalf("启动失败必须返回启动错误，实际 %v", runErr)
	}
	// 失败路径必须释放锁：另一个实例可以立刻取得。
	lock, err := server.NewProcessLock(databasePath)
	if err != nil {
		t.Fatalf("构造新锁失败: %v", err)
	}
	if err := lock.TryAcquire(); err != nil {
		t.Fatalf("启动失败后必须释放锁，实际 %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("释放锁失败: %v", err)
	}
	if _, statErr := os.Stat(lock.Path()); statErr != nil {
		t.Fatalf("锁文件必须保留: %v", statErr)
	}
}

// discardLogger 返回一个不输出任何内容的日志器。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
