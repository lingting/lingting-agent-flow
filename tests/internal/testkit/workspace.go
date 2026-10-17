package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Workspace 是一个测试用例独占的工作空间。
//
// 所有测试产物（数据库、Session、日志、生成文件）都应当放在工作空间内，
// 测试结束时工作空间保留在磁盘上，只有通过 Cleanup 注册的运行时资源会被关闭。
type Workspace struct {
	t    *testing.T
	root string
}

// New 在当前测试运行根目录下创建一个用例级工作空间。
func New(t *testing.T) *Workspace {
	t.Helper()
	return &Workspace{t: t, root: Dir(t)}
}

// Root 返回工作空间的绝对路径。
func (w *Workspace) Root() string {
	return w.root
}

// Path 返回工作空间内的路径，参数按 filepath.Join 规则拼接。
func (w *Workspace) Path(elem ...string) string {
	parts := append([]string{w.root}, elem...)
	return filepath.Join(parts...)
}

// Dir 在工作空间内创建并返回一个子目录，参数按 filepath.Join 规则拼接。
func (w *Workspace) Dir(elem ...string) string {
	w.t.Helper()
	path := w.Path(elem...)
	if err := os.MkdirAll(path, 0o755); err != nil {
		w.t.Fatalf("创建测试子目录失败: %s: %v", path, err)
	}
	return path
}

// DatabasePath 返回工作空间内的 SQLite 数据库路径，不创建文件。
func (w *Workspace) DatabasePath() string {
	return w.Path("agentflow.db")
}

// SessionRoot 返回工作空间内的 Session 目录并确保其存在。
func (w *Workspace) SessionRoot() string {
	w.t.Helper()
	return w.Dir("sessions")
}

// LogDir 返回工作空间内的日志目录并确保其存在。
func (w *Workspace) LogDir() string {
	w.t.Helper()
	return w.Dir("logs")
}

// ConfigPath 返回工作空间内的配置文件路径，不创建文件。
func (w *Workspace) ConfigPath() string {
	return w.Path("agentflow.yaml")
}

// Env 返回追加了工作空间隔离变量后的子进程环境。
//
// 子进程通过独立的环境变量定位数据目录，测试之间不共享全局状态。
func (w *Workspace) Env(extra ...string) []string {
	env := append(os.Environ(),
		"AGENTFLOW_TEST_WORKSPACE="+w.root,
		"AGENTFLOW_TEST_DATABASE="+w.DatabasePath(),
		"AGENTFLOW_TEST_SESSION_ROOT="+w.SessionRoot(),
		"AGENTFLOW_TEST_LOG_DIR="+w.LogDir(),
	)
	return append(env, extra...)
}

// Cleanup 注册运行时资源释放逻辑，例如关闭数据库、终止子进程、关闭监听端口。
//
// 该方法只接受资源释放回调；工作空间目录本身不会被删除。
func (w *Workspace) Cleanup(closeFn func() error) {
	w.t.Helper()
	if closeFn == nil {
		return
	}
	w.t.Cleanup(func() {
		if err := closeFn(); err != nil {
			w.t.Errorf("释放测试资源失败: %v", err)
		}
	})
}

// Report 输出工作空间与常用产物的绝对路径。
func (w *Workspace) Report() {
	w.t.Helper()
	fmt.Fprintf(os.Stdout, "TEST_WORKSPACE=%s\n", w.root)
	w.t.Logf("TEST_WORKSPACE=%s", w.root)
}
