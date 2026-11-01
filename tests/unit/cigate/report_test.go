package cigate_test

import (
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/cigate"
)

// TestDecisionOutputs 校验写入 GitHub Actions 输出文件的键值对。
func TestDecisionOutputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		enabled bool
		mode    cigate.Mode
	}{
		{name: "关闭", enabled: false, mode: cigate.ModeDisabled},
		{name: "self 模式授权", enabled: true, mode: cigate.ModeSelf},
		{name: "all 模式授权", enabled: true, mode: cigate.ModeAll},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decision := cigate.Decision{Enabled: testCase.enabled, Mode: testCase.mode, Reason: "原因"}
			outputs := decision.Outputs()
			if len(outputs) != 3 {
				t.Fatalf("输出项数量应为 3，实际 %d", len(outputs))
			}
			keys := make(map[string]string, len(outputs))
			for _, entry := range outputs {
				keys[entry[0]] = entry[1]
			}
			if keys[cigate.OutputMode] != testCase.mode.Display() {
				t.Fatalf("模式输出为 %q，期望 %q", keys[cigate.OutputMode], testCase.mode.Display())
			}
			expectedEnabled := "false"
			if testCase.enabled {
				expectedEnabled = "true"
			}
			if keys[cigate.OutputEnabled] != expectedEnabled {
				t.Fatalf("授权输出为 %q，期望 %q", keys[cigate.OutputEnabled], expectedEnabled)
			}
			if keys[cigate.OutputReason] != "原因" {
				t.Fatalf("原因输出为 %q", keys[cigate.OutputReason])
			}
		})
	}
}

// TestSummaryWarnsAboutAllMode 校验 all 模式的风险提示必须出现在摘要里。
func TestSummaryWarnsAboutAllMode(t *testing.T) {
	t.Parallel()
	summary := cigate.Decision{
		Enabled: true,
		Mode:    cigate.ModeAll,
		Reason:  "模式 all 不区分 PR 作者身份（author_association=NONE）",
	}.Summary(cigate.Input{Event: "pull_request", PullRequestAuthor: "outsider"})
	for _, expected := range []string{
		cigate.SummaryHeader,
		"`all`",
		"已授权",
		"不等于安全隔离",
		"`outsider`",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("all 模式摘要缺少 %q:\n%s", expected, summary)
		}
	}
}

// TestSummaryForDeniedDecision 校验拒绝结论的摘要明确说明真实测试不执行。
func TestSummaryForDeniedDecision(t *testing.T) {
	t.Parallel()
	summary := cigate.Decision{
		Enabled: false,
		Mode:    cigate.ModeDisabled,
		Reason:  "未配置 AGENTFLOW_REAL_TESTS",
	}.Summary(cigate.Input{Event: "push"})
	for _, expected := range []string{
		"未授权",
		"不计为通过",
		"`disabled`",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("拒绝摘要缺少 %q:\n%s", expected, summary)
		}
	}
	if strings.Contains(summary, "不等于安全隔离") {
		t.Fatal("未启用 all 模式时不应出现 all 模式风险提示")
	}
}

// TestSummaryEscapesTableCells 校验摘要表格中的特殊字符不会破坏结构。
func TestSummaryEscapesTableCells(t *testing.T) {
	t.Parallel()
	summary := cigate.Decision{
		Enabled: false,
		Mode:    cigate.ModeDisabled,
		Reason:  "包含 | 竖线与换行的原因\n第二行",
	}.Summary(cigate.Input{Event: "push"})
	if strings.Contains(summary, "| 包含 | 竖线") {
		t.Fatalf("摘要未转义竖线:\n%s", summary)
	}
	if !strings.Contains(summary, "\\|") {
		t.Fatalf("摘要应转义竖线:\n%s", summary)
	}
	if strings.Contains(summary, "原因\n第二行") {
		t.Fatalf("摘要应把原因压成单行:\n%s", summary)
	}
}
