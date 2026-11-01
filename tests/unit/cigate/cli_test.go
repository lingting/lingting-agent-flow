package cigate_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lingting/agentflow/internal/cigate"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// runGate 在可控环境下执行 real-tests-gate 子命令。
func runGate(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	getenv := func(name string) string { return env[name] }
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := cigate.Run(append([]string{cigate.CommandRealTestsGate}, args...), getenv, stdout, stderr)
	return code, stdout.String(), stderr.String()
}

// readFile 读取输出文件内容。
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	return string(data)
}

// TestRunWritesOutputsAndSummary 校验判定结论写入 GitHub Actions 输出与摘要文件。
func TestRunWritesOutputsAndSummary(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	outputPath := filepath.Join(dir, "github-output.txt")
	summaryPath := filepath.Join(dir, "step-summary.md")
	code, stdout, stderr := runGate(t, map[string]string{},
		"-mode", "self",
		"-event", "push",
		"-actor", "owner",
		"-repository-owner", "owner",
		"-github-output", outputPath,
		"-step-summary", summaryPath,
	)
	if code != 0 {
		t.Fatalf("判定应当正常结束，退出码 %d，stderr=%s", code, stderr)
	}
	outputs := readFile(t, outputPath)
	for _, expected := range []string{
		"real_tests_enabled=true",
		"mode=self",
		"reason=",
	} {
		if !strings.Contains(outputs, expected) {
			t.Fatalf("输出文件缺少 %q:\n%s", expected, outputs)
		}
	}
	summary := readFile(t, summaryPath)
	for _, expected := range []string{
		cigate.SummaryHeader,
		"已授权",
		"`self`",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("摘要缺少 %q:\n%s", expected, summary)
		}
	}
	if !strings.Contains(stdout, "真实测试授权: true") {
		t.Fatalf("标准输出应说明授权结论: %s", stdout)
	}
}

// TestRunDeniedIsSuccess 校验拒绝授权不是命令失败。
func TestRunDeniedIsSuccess(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	outputPath := filepath.Join(dir, "github-output.txt")
	code, stdout, stderr := runGate(t, map[string]string{},
		"-mode", "",
		"-event", "pull_request",
		"-author-association", "OWNER",
		"-github-output", outputPath,
	)
	if code != 0 {
		t.Fatalf("拒绝授权也应当正常结束，退出码 %d，stderr=%s", code, stderr)
	}
	outputs := readFile(t, outputPath)
	if !strings.Contains(outputs, "real_tests_enabled=false") {
		t.Fatalf("未设置变量时必须写出关闭结论:\n%s", outputs)
	}
	if !strings.Contains(outputs, "mode=disabled") {
		t.Fatalf("未设置变量时模式应为 disabled:\n%s", outputs)
	}
	if !strings.Contains(stdout, "真实测试授权: false") {
		t.Fatalf("标准输出应说明拒绝结论: %s", stdout)
	}
}

// TestRunUnknownModeDenies 校验未知取值默认关闭。
func TestRunUnknownModeDenies(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	outputPath := filepath.Join(dir, "github-output.txt")
	code, _, _ := runGate(t, map[string]string{},
		"-mode", "Collaborator",
		"-event", "push",
		"-actor", "anyone",
		"-repository-owner", "owner",
		"-github-output", outputPath,
	)
	if code != 0 {
		t.Fatalf("判定应当正常结束，退出码 %d", code)
	}
	if !strings.Contains(readFile(t, outputPath), "real_tests_enabled=false") {
		t.Fatal("未知取值必须按关闭处理")
	}
}

// TestRunUsageErrors 校验参数错误与未知子命令返回参数错误退出码。
func TestRunUsageErrors(t *testing.T) {
	t.Parallel()
	t.Run("未知子命令", func(t *testing.T) {
		t.Parallel()
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		code := cigate.Run([]string{"unknown"}, func(string) string { return "" }, stdout, stderr)
		if code != 2 {
			t.Fatalf("未知子命令应返回参数错误退出码 2，实际 %d", code)
		}
	})
	t.Run("缺少子命令", func(t *testing.T) {
		t.Parallel()
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		code := cigate.Run(nil, func(string) string { return "" }, stdout, stderr)
		if code != 2 {
			t.Fatalf("缺少子命令应返回参数错误退出码 2，实际 %d", code)
		}
	})
	t.Run("多余参数", func(t *testing.T) {
		t.Parallel()
		code, _, stderr := runGate(t, map[string]string{}, "-mode", "self", "extra")
		if code != 2 {
			t.Fatalf("多余参数应返回参数错误退出码 2，实际 %d", code)
		}
		if !strings.Contains(stderr, "多余参数") {
			t.Fatalf("错误说明应指出多余参数: %s", stderr)
		}
	})
}

// TestRunDoesNotQueryPermissionForNonCollaboratorPush 校验非必要场景不发起权限查询。
func TestRunDoesNotQueryPermissionForNonCollaboratorPush(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"permission":"write"}`)
	}))
	t.Cleanup(server.Close)
	for _, args := range [][]string{
		{"-mode", "self", "-event", "push", "-actor", "someone", "-repository-owner", "owner"},
		{"-mode", "all", "-event", "push", "-actor", "someone", "-repository-owner", "owner"},
		{"-mode", "collaborator", "-event", "pull_request", "-author-association", "MEMBER"},
	} {
		code, _, stderr := runGate(t, map[string]string{"GITHUB_TOKEN": "token"},
			append(args, "-api-url", server.URL, "-repository", "owner/repo")...)
		if code != 0 {
			t.Fatalf("判定应当正常结束，退出码 %d，stderr=%s", code, stderr)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("不需要权限查询的场景不应访问 API，实际请求 %d 次", got)
	}
}

// TestRunCollaboratorPushQueriesPermission 校验协作者 Push 会真实查询仓库权限。
func TestRunCollaboratorPushQueriesPermission(t *testing.T) {
	t.Parallel()
	var (
		recordedMutex sync.Mutex
		recordedPath  string
		recordedAuth  string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedMutex.Lock()
		recordedPath = r.URL.Path
		recordedAuth = r.Header.Get("Authorization")
		recordedMutex.Unlock()
		_, _ = io.WriteString(w, `{"permission":"write"}`)
	}))
	t.Cleanup(server.Close)
	dir := testkit.Dir(t)
	outputPath := filepath.Join(dir, "github-output.txt")
	code, stdout, stderr := runGate(t, map[string]string{"GITHUB_TOKEN": "secret-token"},
		"-mode", "collaborator",
		"-event", "push",
		"-actor", "writer",
		"-repository-owner", "owner",
		"-repository", "owner/repo",
		"-api-url", server.URL,
		"-github-output", outputPath,
	)
	if code != 0 {
		t.Fatalf("判定应当正常结束，退出码 %d，stderr=%s", code, stderr)
	}
	if !strings.Contains(readFile(t, outputPath), "real_tests_enabled=true") {
		t.Fatal("有写入权限的推送者应被放行")
	}
	recordedMutex.Lock()
	gotPath, gotAuth := recordedPath, recordedAuth
	recordedMutex.Unlock()
	if !strings.HasSuffix(gotPath, "/repos/owner/repo/collaborators/writer/permission") {
		t.Fatalf("权限查询路径错误: %s", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("权限查询应使用只读令牌，实际 %q", gotAuth)
	}
	if strings.Contains(stdout, "secret-token") || strings.Contains(stderr, "secret-token") {
		t.Fatal("标准输出与标准错误不得包含令牌")
	}
}

// TestRunCollaboratorPushFailsClosed 校验权限接口不可用时按拒绝处理。
func TestRunCollaboratorPushFailsClosed(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	dir := testkit.Dir(t)
	outputPath := filepath.Join(dir, "github-output.txt")
	code, _, _ := runGate(t, map[string]string{"GITHUB_TOKEN": "secret-token"},
		"-mode", "collaborator",
		"-event", "push",
		"-actor", "writer",
		"-repository-owner", "owner",
		"-repository", "owner/repo",
		"-api-url", server.URL,
		"-github-output", outputPath,
	)
	if code != 0 {
		t.Fatalf("权限查询失败不是命令失败，退出码 %d", code)
	}
	if !strings.Contains(readFile(t, outputPath), "real_tests_enabled=false") {
		t.Fatal("权限查询失败时必须按拒绝处理")
	}
}
