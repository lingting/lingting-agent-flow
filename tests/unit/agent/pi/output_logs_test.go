// Package pi_test 提供 Pi Runner 输出日志与 Session 路径约束的单元测试。
//
// 这些用例复用 tests/internal/fakepi 的测试二进制替身：输出日志的写入、截断标记
// 与关停行为都经过真实文件系统与真实进程路径，不依赖本机是否安装 Pi。
package pi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestOutputLogsAreWrittenSeparately 验证 stdout 与 stderr 分别写入各自的日志文件。
func TestOutputLogsAreWrittenSeparately(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "stderr_output", pi.Options{})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.StdoutPath == "" {
		t.Fatal("标准输出日志路径不能为空")
	}
	if result.StderrPath == "" {
		t.Fatal("标准错误日志路径不能为空")
	}
	if result.StdoutPath == result.StderrPath {
		t.Fatalf("两个流不能共用一个日志文件: %s", result.StdoutPath)
	}
	stdout := readFile(t, result.StdoutPath)
	stderr := readFile(t, result.StderrPath)
	if !strings.Contains(stdout, "READY") {
		t.Fatalf("标准输出日志缺少内容: %q", stdout)
	}
	// 诊断信息不得混入标准输出日志。
	if strings.Contains(stdout, "诊断信息") {
		t.Fatalf("标准输出日志混入了标准错误内容: %q", stdout)
	}
	if !strings.Contains(stderr, "诊断信息") {
		t.Fatalf("标准错误日志缺少内容: %q", stderr)
	}
	if strings.Contains(stderr, "READY") {
		t.Fatalf("标准错误日志混入了标准输出内容: %q", stderr)
	}
}

// TestOutputLogsAreTruncatedWithMarker 验证日志超限后写入明确的截断标记。
func TestOutputLogsAreTruncatedWithMarker(t *testing.T) {
	t.Parallel()
	const limit = 4 << 10
	h := newHarness(t, "huge_output", pi.Options{LogLimit: limit})
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.StdoutTruncated {
		t.Fatal("超长输出应当标记为标准输出被截断")
	}
	info, statErr := os.Stat(result.StdoutPath)
	if statErr != nil {
		t.Fatalf("读取日志文件状态失败: %v", statErr)
	}
	if info.Size() <= limit {
		// 日志上限之外只允许追加一小段标记文本。
		t.Fatalf("日志文件过小，可能没有写入内容: %d", info.Size())
	}
	if info.Size() > limit+(1<<10) {
		t.Fatalf("日志文件超过上限过多: %d", info.Size())
	}
	content := readFile(t, result.StdoutPath)
	if !strings.Contains(content, "日志超过") {
		t.Fatalf("截断时必须留下明确标记")
	}
	if !strings.Contains(content, "后续内容已丢弃") {
		t.Fatalf("截断标记必须说明后续内容被丢弃")
	}
}

// TestOutputLogsStayWithinSessionDir 验证日志写在本次调用的 Session 目录内。
func TestOutputLogsStayWithinSessionDir(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "normal", pi.Options{})
	h.request.TaskID = "task-1"
	result, err := h.run(context.Background(), t)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	sessionDir := filepath.Dir(result.Session.JSONLPath)
	for name, path := range map[string]string{
		"标准输出": result.StdoutPath,
		"标准错误": result.StderrPath,
	} {
		if filepath.Dir(path) != sessionDir {
			t.Fatalf("%s日志不在 Session 目录内: %s != %s", name, path, sessionDir)
		}
		if !strings.HasPrefix(path, h.request.SessionRoot) {
			t.Fatalf("%s日志不在 Session 根目录内: %s", name, path)
		}
	}
}

// TestSessionPathRejectsEscape 验证会话标识不能把文件写到根目录之外。
func TestSessionPathRejectsEscape(t *testing.T) {
	t.Parallel()
	root := filepath.Join(testkit.Dir(t), "sessions")
	paths, err := pi.SessionLayout{
		SessionID:    "../../escape",
		InvocationID: "../../../escape2",
	}.Paths(root)
	if err != nil {
		t.Fatalf("路径清洗后应当生成合法目录: %v", err)
	}
	if !strings.HasPrefix(paths.Dir, root) {
		t.Fatalf("会话目录逃出了根目录: %s", paths.Dir)
	}
	for _, segment := range strings.Split(paths.Dir, string(filepath.Separator)) {
		if segment == ".." {
			t.Fatalf("目录中不应保留路径跳转片段: %s", paths.Dir)
		}
	}
}

// TestMissingExecutableKeepsLogPaths 验证启动失败时不留下伪造的日志路径。
func TestMissingExecutableKeepsLogPaths(t *testing.T) {
	t.Parallel()
	root := testkit.Dir(t)
	h := newHarness(t, "normal", pi.Options{})
	h.request.SessionRoot = filepath.Join(root, "sessions")
	h.runner = pi.NewRunner(pi.Options{Executable: filepath.Join(root, "absent-pi")})
	result, err := h.run(context.Background(), t)
	if err == nil {
		t.Fatal("可执行程序缺失时应返回错误")
	}
	if result.Status != agent.RunFailed {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunFailed, result.Status)
	}
	if result.StdoutPath != "" || result.StderrPath != "" {
		t.Fatalf("启动失败时不应报告日志路径: %q / %q", result.StdoutPath, result.StderrPath)
	}
}

// readFile 读取文件内容，失败时让测试终止。
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取文件失败: %s: %v", path, err)
	}
	return string(data)
}
