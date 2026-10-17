package pi_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/agent/pi"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestSessionLayoutDirectory 验证目录按任务、步骤与尝试分层。
func TestSessionLayoutDirectory(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		layout   pi.SessionLayout
		expected string
	}{
		"完整归属": {
			layout: pi.SessionLayout{
				TaskID:    "task-1",
				StepID:    "step-2",
				AttemptID: "attempt-3",
				SessionID: "session-4",
			},
			expected: filepath.Join("task-1", "step-2", "attempt-3", "session-4"),
		},
		"缺少尝试": {
			layout: pi.SessionLayout{
				TaskID:    "task-1",
				StepID:    "step-2",
				SessionID: "session-4",
			},
			expected: filepath.Join("task-1", "step-2", "session-4"),
		},
		"只有任务": {
			layout:   pi.SessionLayout{TaskID: "task-1", SessionID: "session-4"},
			expected: filepath.Join("task-1", "session-4"),
		},
		"缺少任务归属": {
			layout:   pi.SessionLayout{SessionID: "session-4"},
			expected: filepath.Join("unscoped", "session-4"),
		},
	}
	for name, testCase := range cases {
		directory, err := testCase.layout.Directory()
		if err != nil {
			t.Fatalf("%s: 解析目录失败: %v", name, err)
		}
		if directory != testCase.expected {
			t.Fatalf("%s: 期望 %s，实际 %s", name, testCase.expected, directory)
		}
	}
}

// TestSessionLayoutRequiresSessionID 验证缺少会话标识时拒绝生成路径。
func TestSessionLayoutRequiresSessionID(t *testing.T) {
	t.Parallel()
	if _, err := (pi.SessionLayout{TaskID: "task-1"}).Directory(); err == nil {
		t.Fatal("缺少会话标识时应当返回错误")
	}
	if _, err := (pi.SessionLayout{SessionID: "session-1"}).Paths(""); err == nil {
		t.Fatal("缺少根目录时应当返回错误")
	}
}

// TestSessionLayoutSanitizesSegments 验证标识中的特殊字符不会影响目录结构。
func TestSessionLayoutSanitizesSegments(t *testing.T) {
	t.Parallel()
	layout := pi.SessionLayout{
		TaskID:    "../task/1",
		StepID:    "step:2",
		SessionID: "session 4",
	}
	directory, err := layout.Directory()
	if err != nil {
		t.Fatalf("解析目录失败: %v", err)
	}
	if strings.Contains(directory, ":") {
		t.Fatalf("目录不应包含冒号: %s", directory)
	}
	if strings.Contains(directory, " ") {
		t.Fatalf("目录不应包含空格: %s", directory)
	}
	for _, segment := range strings.Split(directory, string(filepath.Separator)) {
		if segment == ".." || segment == "." {
			t.Fatalf("目录不应包含路径跳转片段: %s", directory)
		}
	}
}

// TestSessionLayoutPathsAreAbsolute 验证返回的路径是绝对路径。
func TestSessionLayoutPathsAreAbsolute(t *testing.T) {
	t.Parallel()
	root := filepath.Join(testkit.Dir(t), "sessions")
	paths, err := pi.SessionLayout{SessionID: "session-1"}.Paths(root)
	if err != nil {
		t.Fatalf("解析路径失败: %v", err)
	}
	if !filepath.IsAbs(paths.JSONL) || !filepath.IsAbs(paths.HTML) {
		t.Fatalf("路径应当是绝对路径: %+v", paths)
	}
	if filepath.Dir(paths.JSONL) != paths.Dir || filepath.Dir(paths.HTML) != paths.Dir {
		t.Fatalf("两个文件应当位于同一目录: %+v", paths)
	}
	if !strings.HasSuffix(paths.JSONL, "session.jsonl") {
		t.Fatalf("原始 Session 文件名不符: %s", paths.JSONL)
	}
	if !strings.HasSuffix(paths.HTML, "session.html") {
		t.Fatalf("HTML 文件名不符: %s", paths.HTML)
	}
}
