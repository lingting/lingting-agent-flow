package testkit_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRunRootIsAbsoluteAndRandom 验证运行根目录是绝对路径且位于统一父目录下。
func TestRunRootIsAbsoluteAndRandom(t *testing.T) {
	t.Parallel()
	root := testkit.RunRoot(t)
	if !filepath.IsAbs(root) {
		t.Fatalf("运行根目录应当是绝对路径: %s", root)
	}
	expectedParent := filepath.Join(os.TempDir(), "lingting-agent-flow-tests")
	if filepath.Dir(root) != expectedParent {
		t.Fatalf("运行根目录应位于 %s，实际 %s", expectedParent, root)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("运行根目录不存在: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("运行根目录应当是目录: %s", root)
	}
}

// TestRunRootIsStableWithinProcess 验证同一进程内多次取用得到同一根目录。
func TestRunRootIsStableWithinProcess(t *testing.T) {
	t.Parallel()
	first := testkit.RunRoot(t)
	second := testkit.RunRoot(t)
	if first != second {
		t.Fatalf("同一进程内的运行根目录应当一致: %s != %s", first, second)
	}
}

// TestDirIsUniqueAndIsolated 验证每次取用的用例目录互不相同且都位于根目录下。
func TestDirIsUniqueAndIsolated(t *testing.T) {
	t.Parallel()
	root := testkit.RunRoot(t)
	seen := make(map[string]struct{}, 32)
	for index := 0; index < 32; index++ {
		dir := testkit.Dir(t)
		if _, duplicated := seen[dir]; duplicated {
			t.Fatalf("用例目录重复: %s", dir)
		}
		seen[dir] = struct{}{}
		if filepath.Dir(dir) != root {
			t.Fatalf("用例目录应直接位于运行根目录下: %s", dir)
		}
	}
}

// TestDirIsUniqueUnderConcurrency 验证并发创建用例目录时路径唯一。
func TestDirIsUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()
	const workers = 16
	var group sync.WaitGroup
	results := make(chan string, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- testkit.Dir(t)
		}()
	}
	group.Wait()
	close(results)
	seen := make(map[string]struct{}, workers)
	for dir := range results {
		if _, duplicated := seen[dir]; duplicated {
			t.Fatalf("并发创建的用例目录重复: %s", dir)
		}
		seen[dir] = struct{}{}
	}
	if len(seen) != workers {
		t.Fatalf("并发创建的目录数量不符: %d", len(seen))
	}
}

// TestWorkspacePathsAreIsolated 验证工作空间内各类产物路径互不重叠。
func TestWorkspacePathsAreIsolated(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	ws.Report()
	root := ws.Root()
	if !filepath.IsAbs(root) {
		t.Fatalf("工作空间应当是绝对路径: %s", root)
	}
	paths := map[string]string{
		"database": ws.DatabasePath(),
		"config":   ws.ConfigPath(),
		"sessions": ws.SessionRoot(),
		"logs":     ws.LogDir(),
		"work":     ws.Dir("work"),
	}
	for name, path := range paths {
		if !strings.HasPrefix(path, root+string(filepath.Separator)) {
			t.Fatalf("%s 路径不在工作空间内: %s", name, path)
		}
	}
	for name, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			continue
		}
		if name == "database" || name == "config" {
			continue
		}
		t.Fatalf("%s 目录应当已创建: %s", name, path)
	}
	if paths["database"] == paths["config"] {
		t.Fatal("数据库与配置文件路径不应相同")
	}
}

// TestWorkspaceEnvIsolatesSubprocess 验证子进程环境变量指向当前工作空间。
func TestWorkspaceEnvIsolatesSubprocess(t *testing.T) {
	t.Parallel()
	ws := testkit.New(t)
	env := ws.Env("AGENTFLOW_EXTRA=1")
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[key] = value
	}
	expected := map[string]string{
		"AGENTFLOW_TEST_WORKSPACE":    ws.Root(),
		"AGENTFLOW_TEST_DATABASE":     ws.DatabasePath(),
		"AGENTFLOW_TEST_SESSION_ROOT": ws.SessionRoot(),
		"AGENTFLOW_TEST_LOG_DIR":      ws.LogDir(),
		"AGENTFLOW_EXTRA":             "1",
	}
	for key, want := range expected {
		if values[key] != want {
			t.Fatalf("%s 不符: 期望 %s，实际 %s", key, want, values[key])
		}
	}
}

// TestWorkspaceCleanupReleasesResourcesOnly 验证注册的清理回调只释放资源，不删除工作空间。
//
// t.Cleanup 按后进先出执行，因此先注册的断言会晚于资源释放回调运行。
func TestWorkspaceCleanupReleasesResourcesOnly(t *testing.T) {
	ws := testkit.New(t)
	path := ws.Dir("keep")
	file := filepath.Join(path, "artifact.txt")
	if err := os.WriteFile(file, []byte("保留测试产物"), 0o600); err != nil {
		t.Fatalf("写入测试产物失败: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			t.Error("清理回调应当已被执行")
		}
		if _, err := os.Stat(file); err != nil {
			t.Errorf("清理回调不应删除测试产物: %v", err)
		}
		if _, err := os.Stat(ws.Root()); err != nil {
			t.Errorf("清理回调不应删除工作空间: %v", err)
		}
	})
	ws.Cleanup(func() error {
		released = true
		return nil
	})
}
