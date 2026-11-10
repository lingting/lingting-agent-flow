package testrunner

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ReportSchemaVersion 是统一报告的格式版本。
const ReportSchemaVersion = "agentflow-test-report/1"

// 运行级终检状态：正常运行结束时不允许停留在 not-run（R2 起由 residual.go 给出结论）。
const (
	// ResidualNotRun 表示本次运行没有执行零残留核验，只用于未完成核验的异常报告。
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
	// BuildArtifacts 记录按内容指纹缓存的构建产物；R2 冻结契约，真实构建在 R5 落地。
	BuildArtifacts []BuildArtifact `json:"build_artifacts"`
	// Residual 是运行级零残留终检结论。
	Residual ResidualCheck `json:"residual_check"`
	ExitCode int           `json:"exit_code"`
}

// BuildArtifact 是一条按内容指纹缓存的构建产物记录。
//
// 它只进入报告的"构建产物"分节，与普通资源共享资源分开统计：镜像构建是写操作，
// 不能按 Destructive + Shared 的普通资源处理。
type BuildArtifact struct {
	// Kind 取 image / volume。
	Kind string `json:"kind"`
	// Ref 是镜像引用或命名卷名。
	Ref string `json:"ref"`
	// Fingerprint 是按内容寻址的指纹（例如构建配置哈希）。
	Fingerprint string `json:"fingerprint"`
	// CreatedAt 是产物创建时间。
	CreatedAt time.Time `json:"created_at,omitempty"`
	// ExpiresAt 是产物的过期时间。
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// Owner 是创建该产物的运行标识。
	Owner string `json:"owner,omitempty"`
	// Cleanup 是该条目的清理责任说明。
	Cleanup string `json:"cleanup"`
}

// 构建产物缓存通道的条目类型（V3 R2 §4.5 / D2 冻结）。
const (
	// BuildArtifactKindImage 表示按内容指纹缓存的镜像标签。
	BuildArtifactKindImage = "image"
	// BuildArtifactKindVolume 表示按内容指纹缓存的命名卷。
	BuildArtifactKindVolume = "volume"
)

// BuildArtifactCleanupPolicy 是冻结的构建产物清理责任说明。
//
// 构建属于写操作，不能按普通 run-shared 资源处理：条目必须写清谁创建、何时过期、谁删除，
// 过期标签删除失败要如实报告，不允许静默沿用。
const BuildArtifactCleanupPolicy = "由创建该产物的运行在过期后删除；删除失败使该运行失败，不允许静默沿用过期标签"

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
// ResidualCheck 记录运行级零残留终检的结论。
type ResidualCheck struct {
	// Status 取 passed / failed / not-applicable；正常运行结束后不得为 not-run。
	Status string `json:"status"`
	// Reason 是结论文本，故障注入来源也会写在这里。
	Reason string `json:"reason,omitempty"`
	// CheckedAt 是终检快照的采集时刻。
	CheckedAt time.Time `json:"checked_at,omitempty"`
	// Baseline 是运行前就存在的资源快照，不计入本次运行的残留。
	Baseline []string `json:"baseline,omitempty"`
	// Residuals 是本次运行留下的残留资源说明。
	Residuals []string `json:"residuals,omitempty"`
	// Errors 是无法确认的核验说明；出现任何一条都判 failed。
	Errors []string `json:"errors,omitempty"`
}

// Failed 报告终检是否失败（存在残留或无法确认）。
func (r ResidualCheck) Failed() bool {
	return r.Status == ResidualFailed
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
	fmt.Fprintf(&builder, "零残留：%s", r.Residual.Status)
	if r.Residual.Reason != "" {
		fmt.Fprintf(&builder, "（%s）", r.Residual.Reason)
	}
	builder.WriteString("\n")
	for _, resource := range r.Resources {
		if resource.Failed() {
			fmt.Fprintf(
				&builder,
				"共享资源 %s 释放失败：%s%s\n",
				resource.Name,
				resource.ReleaseError,
				resource.VerifyError,
			)
		}
	}
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
