package testrunner

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ReportSchemaVersion 是统一报告的格式版本。
const ReportSchemaVersion = "agentflow-test-report/1"

// 运行级终检状态：R1 只释放资源，逐资源自证与全量快照比对在 R2/R7 补齐。
const (
	// ResidualNotRun 表示本次运行没有执行零残留核验。
	ResidualNotRun = "not-run"
)

// Report 是一次运行的统一报告，也是后续审计与覆盖对账的唯一输入。
type Report struct {
	SchemaVersion    string            `json:"schema_version"`
	RunID            string            `json:"run_id"`
	ArtifactDir      string            `json:"artifact_dir"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
	DurationMS       int64             `json:"duration_ms"`
	Selection        []string          `json:"selection"`
	DefaultTimeoutMS int64             `json:"default_timeout_ms"`
	RealTests        RealTestsDecision `json:"real_tests"`
	Totals           Totals            `json:"totals"`
	Cases            []CaseResult      `json:"cases"`
	Resources        []ResourceResult  `json:"resources"`
	Residual         ResidualCheck     `json:"residual_check"`
	ExitCode         int               `json:"exit_code"`
}

// Totals 是运行级的用例计数；跳过单独统计，不计入通过。
type Totals struct {
	Cases    int `json:"cases"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Skipped  int `json:"skipped"`
	Panics   int `json:"panics"`
	Timeouts int `json:"timeouts"`
	Errors   int `json:"errors"`
}

// ResidualCheck 记录运行级零残留终检的结论。
type ResidualCheck struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// summarize 统计用例结论并填充 Totals。
func summarize(cases []CaseResult) Totals {
	totals := Totals{Cases: len(cases)}
	for _, result := range cases {
		switch result.Status {
		case StatusPassed:
			totals.Passed++
		case StatusSkipped:
			totals.Skipped++
		case StatusPanic:
			totals.Panics++
		case StatusTimeout:
			totals.Timeouts++
		case StatusError:
			totals.Errors++
		default:
			totals.Failed++
		}
	}
	return totals
}

// WriteReport 以原子方式写出统一报告。
func WriteReport(path string, report Report) error {
	if err := writeJSONFile(path, report); err != nil {
		return fmt.Errorf("写出统一报告失败: %w", err)
	}
	return nil
}

// Summary 返回面向终端的运行摘要：每个用例一行，末尾给出总数与报告路径。
func (r Report) Summary() string {
	var builder strings.Builder
	for _, result := range r.Cases {
		status := strings.ToUpper(result.Status)
		fmt.Fprintf(&builder, "%-7s %-40s %6dms", status, result.ID, result.DurationMS)
		if result.SkipReason != "" {
			fmt.Fprintf(&builder, "  (%s)", result.SkipReason)
		}
		if result.Message != "" {
			fmt.Fprintf(&builder, "  %s", firstLine(result.Message))
		}
		builder.WriteString("\n")
	}
	fmt.Fprintf(
		&builder,
		"总计 %d：通过 %d / 失败 %d / 跳过 %d / panic %d / 超时 %d / 异常 %d\n",
		r.Totals.Cases,
		r.Totals.Passed,
		r.Totals.Failed,
		r.Totals.Skipped,
		r.Totals.Panics,
		r.Totals.Timeouts,
		r.Totals.Errors,
	)
	fmt.Fprintf(&builder, "报告：%s\n", filepath.Join(r.ArtifactDir, "report.json"))
	return builder.String()
}

// firstLine 取多行消息的第一行，供终端摘要使用。
func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
