package cigate

import (
	"fmt"
	"strings"
)

// SummaryHeader 是 CI 摘要块的固定标题。
const SummaryHeader = "## 真实测试授权判断"

// OutputNames 是写入 GitHub Actions 输出文件的键名。
const (
	// OutputEnabled 表示是否授权执行真实测试（true / false）。
	OutputEnabled = "real_tests_enabled"
	// OutputMode 是解析后的授权模式（self / collaborator / all / disabled）。
	OutputMode = "mode"
	// OutputReason 是单行的中文判定原因。
	OutputReason = "reason"
)

// Outputs 返回需要写入 GitHub Actions 输出文件的键值对，顺序固定。
func (d Decision) Outputs() [][2]string {
	return [][2]string{
		{OutputEnabled, boolText(d.Enabled)},
		{OutputMode, d.Mode.Display()},
		{OutputReason, d.Reason},
	}
}

// Summary 返回写入 Job 摘要的 Markdown 片段。
//
// 摘要只包含授权模式、事件、结论与原因，不包含任何凭据、令牌或完整事件载荷。
func (d Decision) Summary(input Input) string {
	var builder strings.Builder
	builder.WriteString(SummaryHeader)
	builder.WriteString("\n\n")
	builder.WriteString("| 项目 | 值 |\n")
	builder.WriteString("| --- | --- |\n")
	fmt.Fprintf(&builder, "| 变量 | `%s` |\n", ModeVariable)
	fmt.Fprintf(&builder, "| 模式 | `%s` |\n", d.Mode.Display())
	fmt.Fprintf(&builder, "| 事件 | `%s` |\n", tableText(input.Event))
	fmt.Fprintf(&builder, "| 结论 | %s |\n", verdictText(d.Enabled))
	fmt.Fprintf(&builder, "| 原因 | %s |\n", tableText(d.Reason))
	if d.Enabled {
		builder.WriteString("\n真实测试 Job 已获授权：是否真的执行仍取决于环境变量所提供的")
		builder.WriteString("真实测试配置与凭据，缺少配置的套件照常输出 `WARN [SKIP]`。\n")
	} else {
		builder.WriteString("\n真实测试 Job 不执行，未产生任何真实设备访问；")
		builder.WriteString("未执行的用例不计为通过。\n")
	}
	if d.Mode == ModeAll {
		builder.WriteString("\n> 注意：模式 `all` 是主动放宽的信任策略。")
		builder.WriteString("任意 PR 作者的代码都可能进入真实测试执行环境并接触真实测试设备与凭据；")
		builder.WriteString("它不等于安全隔离。\n")
	}
	if author := strings.TrimSpace(input.PullRequestAuthor); author != "" {
		fmt.Fprintf(&builder, "\nPR 作者账号：`%s`（判定依据是作者关系，不是触发者）。\n",
			tableText(author))
	}
	return builder.String()
}

// verdictText 把授权结论渲染为摘要用语。
func verdictText(enabled bool) string {
	if enabled {
		return "已授权（真实测试可执行）"
	}
	return "未授权（真实测试跳过）"
}

// boolText 把布尔值渲染为 GitHub Actions 输出使用的文本。
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// tableText 清理单个 Markdown 表格单元格的内容。
func tableText(value string) string {
	cleaned := strings.Join(strings.Fields(value), " ")
	cleaned = strings.ReplaceAll(cleaned, "|", "\\|")
	if cleaned == "" {
		return "（空）"
	}
	return cleaned
}
