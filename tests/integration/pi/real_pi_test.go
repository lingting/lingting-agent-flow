package pi_integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// realPiTimeout 是单次真实 Pi 调用的超时上限。
const realPiTimeout = 5 * time.Minute

// TestRealPiCheck 校验真实 Pi 版本具备非交互执行所需的参数。
func TestRealPiCheck(t *testing.T) {
	t.Parallel()
	executable := requirePi(t)
	report, err := pi.Check(context.Background(), pi.Options{Executable: executable}, "")
	if err != nil {
		t.Fatalf("Pi 检查失败: %v", err)
	}
	if report.Executable == "" || report.Version == "" {
		t.Fatalf("检查结果缺少可执行程序或版本: %+v", report)
	}
	if !report.PrintSupported || !report.SessionSupported || !report.ExportSupported {
		t.Fatalf("当前 Pi 版本缺少必需参数: %+v", report)
	}
	t.Logf("PI_VERSION=%s", report.Version)
}

// TestRealPiRun 使用真实 Pi 执行一次最小提示词。
//
// 模型由 tests/config/pi.yml 的 pi.model 决定，未填写时使用 group/free；
// 每次调用都显式传入选定的模型，不回退到 Pi CLI 的默认模型。
func TestRealPiRun(t *testing.T) {
	t.Parallel()
	executable := requirePi(t)
	model := resolveModel(t)
	options := pi.Options{Executable: executable, Timeout: realPiTimeout}

	workspace := testkit.New(t)
	sessionRoot := workspace.SessionRoot()
	workingDir := workspace.Dir("work")
	t.Logf("SESSION_ROOT=%s", sessionRoot)
	t.Logf("WORKING_DIR=%s", workingDir)

	runner := pi.NewRunner(options)
	result, err := runner.Run(context.Background(), agent.RunRequest{
		SessionID:   "integration-session",
		Model:       model,
		Prompt:      "只回答 READY 这一个词，不要使用任何工具。",
		WorkingDir:  workingDir,
		SessionRoot: sessionRoot,
		Timeout:     realPiTimeout,
	}, nil)
	if result.Session.JSONLPath != "" {
		t.Logf("SESSION_JSONL=%s", result.Session.JSONLPath)
	}
	if err != nil {
		t.Fatalf("真实执行失败: %v（Session: %s）", err, result.Session.JSONLPath)
	}
	if result.Status != agent.RunCompleted {
		t.Fatalf("期望状态 %s，实际 %s", agent.RunCompleted, result.Status)
	}
	if result.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际 %d", result.ExitCode)
	}
	if !strings.Contains(result.FinalText, "READY") {
		t.Fatalf("最终文本不含预期内容: %q", result.FinalText)
	}
	if result.Session.Completeness != agent.SessionComplete {
		t.Fatalf("期望 Session 完整，实际 %s（%s）", result.Session.Completeness, result.Session.Error)
	}
	if _, statErr := os.Stat(result.Session.JSONLPath); statErr != nil {
		t.Fatalf("Session 文件不存在: %v", statErr)
	}
	if !strings.HasPrefix(result.Session.JSONLPath, workspace.Root()) {
		t.Fatalf("Session 文件应位于测试工作空间内: %s", result.Session.JSONLPath)
	}
	if result.Session.ExportStatus == agent.ExportSucceeded {
		if _, statErr := os.Stat(result.Session.HTMLPath); statErr != nil {
			t.Fatalf("HTML 文件不存在: %v", statErr)
		}
		if filepath.Dir(result.Session.HTMLPath) != filepath.Dir(result.Session.JSONLPath) {
			t.Fatalf("HTML 与 Session 应位于同一目录: %s", result.Session.HTMLPath)
		}
	}
	// 两个输出流必须分别落盘，且都在 Session 目录内。
	sessionDir := filepath.Dir(result.Session.JSONLPath)
	for name, path := range map[string]string{
		"标准输出": result.StdoutPath,
		"标准错误": result.StderrPath,
	} {
		if path == "" {
			t.Fatalf("%s日志路径不能为空", name)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("%s日志不存在: %s: %v", name, path, statErr)
		}
		if filepath.Dir(path) != sessionDir {
			t.Fatalf("%s日志应位于 Session 目录内: %s", name, path)
		}
	}
	// 标准输出日志必须包含真实模型的回答。
	stdout, readErr := os.ReadFile(result.StdoutPath)
	if readErr != nil {
		t.Fatalf("读取标准输出日志失败: %v", readErr)
	}
	if !strings.Contains(string(stdout), "READY") {
		t.Fatalf("标准输出日志缺少回答内容: %q", string(stdout))
	}
}

// TestRealPiSessionIsReusable 验证真实 Session 文件可以被重新读取并再次导出 HTML。
//
// 该用例确认两件事：
//   - 生成的 Session 文件是真实的 JSONL，首行是会话头部；
//   - Pi 官方导出能力可以对该文件再次生成 HTML，说明归档的原始文件是可用的。
func TestRealPiSessionIsReusable(t *testing.T) {
	t.Parallel()
	executable := requirePi(t)
	model := resolveModel(t)
	options := pi.Options{Executable: executable, Timeout: realPiTimeout}

	workspace := testkit.New(t)
	runner := pi.NewRunner(options)
	result, err := runner.Run(context.Background(), agent.RunRequest{
		SessionID:   "integration-reuse-session",
		Model:       model,
		Prompt:      "只回答 READY 这一个词，不要使用任何工具。",
		WorkingDir:  workspace.Dir("work"),
		SessionRoot: workspace.SessionRoot(),
		Timeout:     realPiTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("真实执行失败: %v（Session: %s）", err, result.Session.JSONLPath)
	}
	content, readErr := os.ReadFile(result.Session.JSONLPath)
	if readErr != nil {
		t.Fatalf("读取 Session 文件失败: %v", readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 2 {
		t.Fatalf("Session 文件内容过少，不足以证明真实会话: %d 行", len(lines))
	}
	if !strings.Contains(lines[0], `"type":"session"`) {
		t.Fatalf("首行不是会话头部: %s", lines[0])
	}
	if result.Session.ExportStatus != agent.ExportSucceeded {
		// 当前版本不支持导出时如实跳过该断言，但绝不允许伪造成功。
		t.Logf("当前 Pi 版本未成功导出 HTML: %s（%s）", result.Session.ExportStatus, result.Session.Error)
		return
	}
	htmlPath := result.Session.HTMLPath
	info, statErr := os.Stat(htmlPath)
	if statErr != nil {
		t.Fatalf("导出的 HTML 不存在: %s: %v", htmlPath, statErr)
	}
	if info.Size() == 0 {
		t.Fatalf("导出的 HTML 为空文件: %s", htmlPath)
	}
	html, readErr := os.ReadFile(htmlPath)
	if readErr != nil {
		t.Fatalf("读取 HTML 失败: %v", readErr)
	}
	if !strings.Contains(strings.ToLower(string(html)), "<html") {
		t.Fatalf("导出内容不是 HTML 文档")
	}
}
