// Package server_test 验证服务端进程锁的真实操作系统语义。
//
// 用例直接操作锁文件与两个独立锁实例：断言的是"两个进程不可能同时持有同一个锁文件"，
// 而不是进程内 Mutex 的行为。数据库路径的符号链接别名必须收敛到同一个锁身份，否则两份
// 服务端会各自取得一把锁、却打开同一个 SQLite 文件。
package server_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/config"
	"github.com/lingting/agentflow/internal/server"
	"github.com/lingting/agentflow/internal/storage"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// windowsErrorPrivilegeNotHeld 是 Windows 上"缺少创建符号链接所需权限"的错误码。
//
// Go 只在 Windows 版 syscall 中提供该常量名，这里按数值判断，使本文件保持跨平台编译。
const windowsErrorPrivilegeNotHeld = syscall.Errno(1314)

// TestMain 把本次运行的完整测试输出写入本次运行根目录下的 test.log。
func TestMain(m *testing.M) {
	os.Exit(testkit.Main(m))
}

// TestProcessLockRejectsSecondInstance 验证同一数据库路径只允许一个锁实例。
func TestProcessLockRejectsSecondInstance(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(testkit.Dir(t), "data", "agentflow.db")

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

// TestServerLockPathIsStableForSameDatabase 验证冗余路径段收敛到同一个锁文件。
func TestServerLockPathIsStableForSameDatabase(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(testkit.Dir(t), "agentflow.db")
	absolute, err := server.ServerLockPath(databasePath)
	if err != nil {
		t.Fatalf("计算绝对路径锁文件失败: %v", err)
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

// TestServerLockPathMatchesRelativePath 验证相对路径与绝对路径得到同一个锁身份。
func TestServerLockPathMatchesRelativePath(t *testing.T) {
	workspace := testkit.Dir(t)
	absolute, err := server.ServerLockPath(filepath.Join(workspace, "agentflow.db"))
	if err != nil {
		t.Fatalf("计算绝对路径锁文件失败: %v", err)
	}

	// 切换到工作目录之后再以相对路径计算：两种表达式必须指向同一个数据库与同一个锁文件。
	t.Chdir(workspace)
	relative, err := server.ServerLockPath(filepath.Join(".", "agentflow.db"))
	if err != nil {
		t.Fatalf("计算相对路径锁文件失败: %v", err)
	}
	if relative != absolute {
		t.Fatalf("相对路径必须得到同一锁文件: %s / %s", relative, absolute)
	}
}

// TestProcessLockUnifiesDatabaseSymlinkAlias 验证数据库文件本身是符号链接时的锁身份。
func TestProcessLockUnifiesDatabaseSymlinkAlias(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	realPath := filepath.Join(workspace, "real.db")
	if err := os.WriteFile(realPath, nil, 0o600); err != nil {
		t.Fatalf("创建数据库文件失败: %v", err)
	}
	aliasPath := filepath.Join(workspace, "alias.db")
	createPathAlias(t, realPath, aliasPath)

	realLock, err := server.NewProcessLock(realPath)
	if err != nil {
		t.Fatalf("构造真实路径锁失败: %v", err)
	}
	aliasLock, err := server.NewProcessLock(aliasPath)
	if err != nil {
		t.Fatalf("构造符号链接别名锁失败: %v", err)
	}
	if aliasLock.Path() != realLock.Path() {
		t.Fatalf("符号链接别名必须得到同一锁文件: %s / %s", aliasLock.Path(), realLock.Path())
	}
	if aliasLock.DatabasePath() != realLock.DatabasePath() {
		t.Fatalf("符号链接别名必须得到同一数据库路径: %s / %s", aliasLock.DatabasePath(), realLock.DatabasePath())
	}

	// 两个不同路径表达式指向同一个数据库：第二个不得取得锁。
	if err := realLock.TryAcquire(); err != nil {
		t.Fatalf("第一个锁应当取得成功: %v", err)
	}
	t.Cleanup(func() {
		if releaseErr := realLock.Release(); releaseErr != nil {
			t.Errorf("释放真实路径锁失败: %v", releaseErr)
		}
	})
	if acquireErr := aliasLock.TryAcquire(); !errors.Is(acquireErr, server.ErrServerAlreadyRunning) {
		t.Fatalf("别名锁必须被拒绝: 实际 %v", acquireErr)
	}
}

// TestProcessLockUnifiesParentDirectorySymlinkAlias 验证父目录符号链接与尚未创建路径段的锁身份。
func TestProcessLockUnifiesParentDirectorySymlinkAlias(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	realDir := filepath.Join(workspace, "real")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("创建真实目录失败: %v", err)
	}
	aliasDir := filepath.Join(workspace, "alias")
	createPathAlias(t, realDir, aliasDir)

	// 数据库文件与其子目录都尚未创建：仍必须解析已存在祖先的符号链接。
	directPath := filepath.Join(realDir, "nested", "agentflow.db")
	aliasPath := filepath.Join(aliasDir, "nested", "agentflow.db")
	directLock, err := server.NewProcessLock(directPath)
	if err != nil {
		t.Fatalf("构造真实目录锁失败: %v", err)
	}
	aliasLock, err := server.NewProcessLock(aliasPath)
	if err != nil {
		t.Fatalf("构造目录别名锁失败: %v", err)
	}
	if aliasLock.Path() != directLock.Path() {
		t.Fatalf("父目录符号链接必须得到同一锁文件: %s / %s", aliasLock.Path(), directLock.Path())
	}
	if aliasLock.DatabasePath() != directLock.DatabasePath() {
		t.Fatalf("父目录符号链接必须得到同一数据库路径: %s / %s", aliasLock.DatabasePath(), directLock.DatabasePath())
	}
	// 尚未创建的路径段必须按原顺序拼回：不同平台的上级路径规范化不会改变这两段。
	tail := filepath.Join("real", "nested", "agentflow.db")
	if !strings.HasSuffix(directLock.DatabasePath(), tail) {
		t.Fatalf("规范化路径必须拼回尚未创建的路径段 %s，实际 %s", tail, directLock.DatabasePath())
	}
	if _, statErr := os.Stat(filepath.Dir(directLock.Path())); statErr != nil {
		t.Fatalf("取得锁之前必须创建锁文件目录: %v", statErr)
	}

	if err := directLock.TryAcquire(); err != nil {
		t.Fatalf("第一个锁应当取得成功: %v", err)
	}
	t.Cleanup(func() {
		if releaseErr := directLock.Release(); releaseErr != nil {
			t.Errorf("释放锁失败: %v", releaseErr)
		}
	})
	if acquireErr := aliasLock.TryAcquire(); !errors.Is(acquireErr, server.ErrServerAlreadyRunning) {
		t.Fatalf("别名锁必须被拒绝: 实际 %v", acquireErr)
	}
}

// TestProcessLockRejectsDanglingDatabaseSymlink 验证悬空数据库符号链接失败关闭。
func TestProcessLockRejectsDanglingDatabaseSymlink(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	danglingPath := filepath.Join(workspace, "dangling.db")
	createPathAlias(t, filepath.Join(workspace, "missing", "agentflow.db"), danglingPath)

	lockPath, err := server.ServerLockPath(danglingPath)
	if err == nil {
		t.Fatalf("悬空符号链接必须返回错误，实际得到锁文件 %s", lockPath)
	}
	if !strings.Contains(err.Error(), "悬空符号链接") {
		t.Fatalf("悬空符号链接错误必须说明原因，实际 %v", err)
	}
	if _, lockErr := server.NewProcessLock(danglingPath); lockErr == nil {
		t.Fatal("悬空符号链接不得构造出进程锁")
	}
}

// TestProcessLockRejectsVanishedAliasTarget 验证父目录别名指向已消失目录时失败关闭。
//
// 它覆盖与悬空文件符号链接相同的失败语义，并且在缺少符号链接权限的环境里仍然可执行。
func TestProcessLockRejectsVanishedAliasTarget(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	vanishingDir := filepath.Join(workspace, "vanishing")
	if err := os.MkdirAll(vanishingDir, 0o750); err != nil {
		t.Fatalf("创建别名目标目录失败: %v", err)
	}
	aliasDir := filepath.Join(workspace, "alias")
	createPathAlias(t, vanishingDir, aliasDir)
	// 删除刚创建的别名目标目录，使路径别名悬空；本次删除属于用例夹具，不是测试工作空间。
	if err := os.Remove(vanishingDir); err != nil {
		t.Fatalf("删除别名目标目录失败: %v", err)
	}

	if _, err := server.ServerLockPath(filepath.Join(aliasDir, "agentflow.db")); err == nil {
		t.Fatal("指向已消失目录的路径别名必须返回错误")
	}
	if _, err := server.NewProcessLock(filepath.Join(aliasDir, "agentflow.db")); err == nil {
		t.Fatal("指向已消失目录的路径别名不得构造出进程锁")
	}
}

// TestServerRunRejectsSymlinkAliasConflict 验证符号链接别名不能绕过进程锁。
func TestServerRunRejectsSymlinkAliasConflict(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	realPath := filepath.Join(workspace, "real.db")
	if err := os.WriteFile(realPath, nil, 0o600); err != nil {
		t.Fatalf("创建数据库文件失败: %v", err)
	}
	aliasPath := filepath.Join(workspace, "alias.db")
	createPathAlias(t, realPath, aliasPath)

	holder, err := server.NewProcessLock(aliasPath)
	if err != nil {
		t.Fatalf("构造别名锁失败: %v", err)
	}
	if err := holder.TryAcquire(); err != nil {
		t.Fatalf("别名锁应当取得成功: %v", err)
	}
	t.Cleanup(func() {
		if releaseErr := holder.Release(); releaseErr != nil {
			t.Errorf("释放别名锁失败: %v", releaseErr)
		}
	})

	var opened atomic.Int64
	instance := newTestServer(t, func(context.Context, *config.Config) (storage.Store, error) {
		opened.Add(1)
		return nil, errors.New("别名锁被占用时不得打开数据库")
	})
	runErr := instance.Run(context.Background(), newConfigLoader(realPath))
	if !errors.Is(runErr, server.ErrServerAlreadyRunning) {
		t.Fatalf("同一数据库的别名锁被占用时必须返回已有实例错误，实际 %v", runErr)
	}
	if opened.Load() != 0 {
		t.Fatal("别名锁被占用时不得打开数据库")
	}
}

// TestServerRunUsesNormalizedDatabasePath 验证 Server 用规范化路径打开数据库。
func TestServerRunUsesNormalizedDatabasePath(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	realDir := filepath.Join(workspace, "real")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("创建真实目录失败: %v", err)
	}
	aliasDir := filepath.Join(workspace, "alias")
	createPathAlias(t, realDir, aliasDir)

	aliasPath := filepath.Join(aliasDir, "agentflow.db")
	reference, err := server.NewProcessLock(aliasPath)
	if err != nil {
		t.Fatalf("构造参考锁失败: %v", err)
	}
	recorded := make(chan string, 1)
	instance := newTestServer(t, func(_ context.Context, cfg *config.Config) (storage.Store, error) {
		recorded <- cfg.Storage.DatabasePath
		return nil, errors.New("数据库不可用")
	})
	runErr := instance.Run(context.Background(), newConfigLoader(aliasPath))
	if !errors.Is(runErr, server.ErrStartup) {
		t.Fatalf("数据库打开失败必须返回启动错误，实际 %v", runErr)
	}
	select {
	case openedPath := <-recorded:
		if openedPath != reference.DatabasePath() {
			t.Fatalf("锁与数据库必须使用同一条规范化路径: %s / %s", reference.DatabasePath(), openedPath)
		}
		if !strings.HasSuffix(filepath.Dir(openedPath), string(os.PathSeparator)+"real") {
			t.Fatalf("数据库路径必须解析父目录符号链接，实际 %s", openedPath)
		}
	default:
		t.Fatal("启动流程必须调用数据库打开器")
	}
}

// TestServerRunDoesNotStartWhenLockHeld 验证锁被占用时不会进入 Server 启动流程。
func TestServerRunDoesNotStartWhenLockHeld(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(testkit.Dir(t), "agentflow.db")
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
	instance := newTestServer(t, func(context.Context, *config.Config) (storage.Store, error) {
		opened.Add(1)
		return nil, errors.New("锁被占用时不得打开数据库")
	})
	runErr := instance.Run(context.Background(), newConfigLoader(databasePath))
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

// TestServerRunRejectsDanglingDatabaseSymlink 验证悬空符号链接时 Server 失败关闭。
func TestServerRunRejectsDanglingDatabaseSymlink(t *testing.T) {
	t.Parallel()
	workspace := testkit.Dir(t)
	danglingPath := filepath.Join(workspace, "dangling.db")
	createPathAlias(t, filepath.Join(workspace, "missing", "agentflow.db"), danglingPath)

	var opened atomic.Int64
	instance := newTestServer(t, func(context.Context, *config.Config) (storage.Store, error) {
		opened.Add(1)
		return nil, errors.New("悬空符号链接时不得打开数据库")
	})
	runErr := instance.Run(context.Background(), newConfigLoader(danglingPath))
	if !errors.Is(runErr, server.ErrStartup) {
		t.Fatalf("悬空符号链接必须返回启动错误，实际 %v", runErr)
	}
	if opened.Load() != 0 {
		t.Fatal("悬空符号链接时不得打开数据库")
	}
}

// TestServerRunReleasesLockWhenStartupFails 验证启动失败后锁被释放。
func TestServerRunReleasesLockWhenStartupFails(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(testkit.Dir(t), "agentflow.db")
	instance := newTestServer(t, func(context.Context, *config.Config) (storage.Store, error) {
		return nil, errors.New("数据库不可用")
	})
	runErr := instance.Run(context.Background(), newConfigLoader(databasePath))
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

// newTestServer 构造只注入数据库打开替身的服务实例。
func newTestServer(t *testing.T, openStore server.StoreOpener) *server.Server {
	t.Helper()
	instance, err := server.New(server.Options{
		Logger:     discardLogger(),
		OpenStore:  openStore,
		SystemInfo: func() app.SystemInfo { return app.SystemInfo{} },
	})
	if err != nil {
		t.Fatalf("构造服务实例失败: %v", err)
	}
	return instance
}

// newConfigLoader 构造只设置数据库路径的配置加载器。
func newConfigLoader(databasePath string) server.ConfigLoader {
	return func(context.Context) (*config.Config, error) {
		return &config.Config{Storage: config.StorageConfig{DatabasePath: databasePath}}, nil
	}
}

// createPathAlias 创建测试所需的符号链接。
//
// 只有当前环境确实不能创建符号链接（例如未开启开发模式的 Windows、缺少相应权限）时才
// 跳过并记录原因；支持符号链接的环境必须实际执行用例。
func createPathAlias(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if symlinkUnavailable(err) {
			t.Skipf("当前环境不允许创建符号链接，跳过路径别名用例: %v", err)
		}
		t.Fatalf("创建符号链接失败: %s -> %s: %v", link, target, err)
	}
}

// symlinkUnavailable 判断错误是否只表示"当前环境不能创建符号链接"。
func symlinkUnavailable(err error) bool {
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == windowsErrorPrivilegeNotHeld
}

// discardLogger 返回一个不输出任何内容的日志器。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
