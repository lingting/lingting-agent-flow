package testkit_test

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRemoteTestRootBuildsUnifiedPath 验证远程测试目录统一落在
// <远程 Home>/.lingting/agentflow-tests/<测试包>/<用例标识> 下。
func TestRemoteTestRootBuildsUnifiedPath(t *testing.T) {
	t.Parallel()
	root, err := testkit.RemoteTestRoot("/home/tester", testkit.RemoteTestPackageDocker, "abc123")
	if err != nil {
		t.Fatalf("拼接远程测试目录失败: %v", err)
	}
	expected := "/home/tester/.lingting/agentflow-tests/docker/abc123"
	if root != expected {
		t.Fatalf("远程测试目录不符: 期望 %s，实际 %s", expected, root)
	}
	if !path.IsAbs(root) {
		t.Fatalf("远程测试目录必须是绝对路径: %s", root)
	}
	// 旧的 ~/agentflow-tests 形式不得再出现。
	if strings.Contains(root, "/home/tester/agentflow-tests") {
		t.Fatalf("远程测试目录不得使用旧的 ~/agentflow-tests 形式: %s", root)
	}
	if strings.Contains(root, "~") {
		t.Fatalf("远程测试目录不得保留字面量 ~: %s", root)
	}
}

// TestRemoteTestRootIsolatesPackages 验证各测试包使用互不相同的子目录。
func TestRemoteTestRootIsolatesPackages(t *testing.T) {
	t.Parallel()
	packages := []string{
		testkit.RemoteTestPackageAgent,
		testkit.RemoteTestPackageDocker,
		testkit.RemoteTestPackageCodeSaver,
		testkit.RemoteTestPackageWorkflow,
		testkit.RemoteTestPackageSSH,
	}
	seen := make(map[string]string, len(packages))
	for _, name := range packages {
		root, err := testkit.RemoteTestRoot("/home/tester", name, "same-id")
		if err != nil {
			t.Fatalf("拼接 %s 的远程测试目录失败: %v", name, err)
		}
		if previous, duplicated := seen[root]; duplicated {
			t.Fatalf("测试包 %s 与 %s 使用了同一目录: %s", name, previous, root)
		}
		seen[root] = name
		if !strings.Contains(root, "/agentflow-tests/"+name+"/") {
			t.Fatalf("测试包 %s 的目录层级不符: %s", name, root)
		}
	}
}

// TestRemoteTestRootIsolatesCases 验证同一测试包内不同用例互不覆盖。
func TestRemoteTestRootIsolatesCases(t *testing.T) {
	t.Parallel()
	first, err := testkit.RemoteTestRoot("/home/tester", testkit.RemoteTestPackageDocker, "case-a")
	if err != nil {
		t.Fatalf("拼接第一个用例目录失败: %v", err)
	}
	second, err := testkit.RemoteTestRoot("/home/tester", testkit.RemoteTestPackageDocker, "case-b")
	if err != nil {
		t.Fatalf("拼接第二个用例目录失败: %v", err)
	}
	if first == second {
		t.Fatalf("不同用例不得共享远程目录: %s", first)
	}
	if path.Dir(first) != path.Dir(second) {
		t.Fatalf("同一测试包的不同用例应位于同一父目录: %s / %s", first, second)
	}
}

// TestRemoteTestRootRejectsInvalidInput 验证非法输入给出明确错误而不是静默降级。
func TestRemoteTestRootRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		home    string
		pkg     string
		parts   []string
		wantErr string
	}{
		{name: "相对路径 Home", home: "home/tester", pkg: testkit.RemoteTestPackageDocker, wantErr: "绝对路径"},
		{name: "字面量 ~", home: "~", pkg: testkit.RemoteTestPackageDocker, wantErr: "绝对路径"},
		{name: "空 Home", home: "", pkg: testkit.RemoteTestPackageDocker, wantErr: "绝对路径"},
		{name: "未登记的测试包", home: "/home/tester", pkg: "unknown", wantErr: "未登记"},
		{name: "空测试包", home: "/home/tester", pkg: "", wantErr: "未登记"},
		{
			name: "路径逃逸", home: "/home/tester", pkg: testkit.RemoteTestPackageDocker,
			parts: []string{".."}, wantErr: "相对路径",
		},
		{
			name: "非法字符", home: "/home/tester", pkg: testkit.RemoteTestPackageDocker,
			parts: []string{"a/b"}, wantErr: "非法字符",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			root, err := testkit.RemoteTestRoot(item.home, item.pkg, item.parts...)
			if err == nil {
				t.Fatalf("非法输入必须返回错误，实际得到 %s", root)
			}
			if !strings.Contains(err.Error(), item.wantErr) {
				t.Fatalf("错误信息应包含 %q，实际: %v", item.wantErr, err)
			}
		})
	}
}

// TestRemoteTestRootDoesNotTouchLocalArtifactDir 验证远程目录约定不影响本机产物目录。
func TestRemoteTestRootDoesNotTouchLocalArtifactDir(t *testing.T) {
	t.Parallel()
	root, err := testkit.RemoteTestRoot("/home/tester", testkit.RemoteTestPackageAgent)
	if err != nil {
		t.Fatalf("拼接远程测试目录失败: %v", err)
	}
	if strings.Contains(root, "lingting-agent-flow-tests") {
		t.Fatalf("远程测试目录不得使用本机测试产物目录名: %s", root)
	}
	if strings.Contains(root, os.TempDir()) {
		t.Fatalf("远程测试目录不得位于本机系统临时目录: %s", root)
	}
	// 本机测试产物目录的约定保持不变。
	localRoot := testkit.RunRoot(t)
	expectedParent := filepath.Join(os.TempDir(), "lingting-agent-flow-tests")
	if filepath.Dir(localRoot) != expectedParent {
		t.Fatalf("本机测试产物目录应仍位于 %s，实际 %s", expectedParent, localRoot)
	}
}

// TestRemoteTestRootDoesNotRewriteProductionWorkspace 验证生产管理工作区不受影响。
func TestRemoteTestRootDoesNotRewriteProductionWorkspace(t *testing.T) {
	t.Parallel()
	root, err := testkit.RemoteTestRoot("/home/tester", testkit.RemoteTestPackageWorkflow, "case-a")
	if err != nil {
		t.Fatalf("拼接远程测试目录失败: %v", err)
	}
	production := path.Join("/home/tester", strings.TrimPrefix(domain.DefaultWorkspaceRoot, "~/"))
	if root == production {
		t.Fatalf("远程测试目录不得等于生产管理工作区: %s", root)
	}
	if strings.HasPrefix(root, production+"/") {
		t.Fatalf("远程测试目录不得位于生产管理工作区内: %s", root)
	}
}
