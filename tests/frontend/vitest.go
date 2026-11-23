package frontend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// vitestJSONReport 是 Vitest JSON reporter 的输出（只取本适配器需要的字段）。
type vitestJSONReport struct {
	NumTotalTests   int                `json:"numTotalTests"`
	NumPassedTests  int                `json:"numPassedTests"`
	NumFailedTests  int                `json:"numFailedTests"`
	NumPendingTests int                `json:"numPendingTests"`
	TestResults     []vitestFileResult `json:"testResults"`
}

// vitestFileResult 是单个测试文件的结果。
type vitestFileResult struct {
	Name             string            `json:"name"`
	Status           string            `json:"status"`
	Message          string            `json:"message"`
	AssertionResults []vitestAssertion `json:"assertionResults"`
}

// vitestAssertion 是一条断言的执行结果。
type vitestAssertion struct {
	Status          string   `json:"status"`
	Title           string   `json:"title"`
	FailureMessages []string `json:"failureMessages"`
}

// runVitest 执行一个 Vitest 文件并映射结论。
//
// 结论同样只取自 JSON 报告与退出码：退出码非 0 而 JSON 没有失败项时按冲突判失败。
func runVitest(c *testrunner.Context, name string) {
	uiDir, err := uiRoot()
	if err != nil {
		c.Fatalf("%v", err)
	}
	requireFrontendTooling(c, uiDir, "vitest")
	session, ok := c.Resource("node")
	if !ok {
		c.Fatalf("缺少 node-worker 句柄，无法执行 Vitest 用例")
	}
	artifactDir := c.ArtifactDir()
	reportPath := filepath.Join(artifactDir, "vitest.json")
	run, err := runFrontendCLI(c, cliRequest{
		uiDir: uiDir,
		args: []string{
			"exec", "vitest", "run",
			"tests/unit/" + name + ".test.ts",
			"--maxWorkers=1",
			"--reporter=json",
			"--outputFile", reportPath,
		},
		env:     cliEnv(artifactDir, nil),
		logPath: filepath.Join(artifactDir, "vitest.log"),
		session: session,
	})
	if err != nil {
		c.Fatalf("执行 Vitest 用例失败: %v", err)
	}
	report, err := readVitestReport(reportPath)
	if err != nil {
		c.Fatalf("读取 Vitest JSON 报告失败: %v\n退出码 %d\n%s", err, run.exitCode, run.output)
	}
	verdict := summarizeVitest(report)
	c.Logf(
		"Vitest %s：共 %d 条，通过 %d，失败 %d，跳过 %d，退出码 %d；报告 %s",
		name,
		report.NumTotalTests,
		report.NumPassedTests,
		report.NumFailedTests,
		report.NumPendingTests,
		run.exitCode,
		reportPath,
	)
	switch {
	case len(verdict.failed) > 0:
		c.Errorf(
			"Vitest 用例失败 %d 条：%s\n退出码 %d\n%s",
			len(verdict.failed),
			strings.Join(verdict.failed, "；"),
			run.exitCode,
			run.output,
		)
	case report.NumTotalTests == 0:
		c.Errorf("Vitest JSON 报告里没有任何用例，视为无法确认结论\n%s", run.output)
	case run.exitCode != 0:
		c.Errorf(
			"Vitest 退出码 %d 与 JSON 结论（失败 0）冲突，按失败处理\n%s",
			run.exitCode,
			run.output,
		)
	case report.NumPendingTests == report.NumTotalTests:
		c.Skipf(
			"该文件全部用例都被跳过（pending %d）：%s",
			report.NumPendingTests,
			strings.Join(verdict.skipReasons, "；"),
		)
	}
}

// vitestVerdict 是 Vitest 报告的映射结论。
type vitestVerdict struct {
	failed      []string
	skipReasons []string
}

// summarizeVitest 收集失败项与跳过项。
func summarizeVitest(report vitestJSONReport) vitestVerdict {
	verdict := vitestVerdict{}
	for _, file := range report.TestResults {
		for _, assertion := range file.AssertionResults {
			title := strings.TrimSpace(assertion.Title)
			switch assertion.Status {
			case "failed":
				detail := title
				if len(assertion.FailureMessages) > 0 {
					detail += "（" + firstLine(assertion.FailureMessages[0]) + "）"
				}
				verdict.failed = append(verdict.failed, detail)
			case "pending", "skipped", "todo":
				verdict.skipReasons = append(verdict.skipReasons, title)
			}
		}
		if file.Status == "failed" && len(file.AssertionResults) == 0 {
			verdict.failed = append(verdict.failed, file.Name+"（文件级失败："+firstLine(file.Message)+"）")
		}
	}
	if len(verdict.skipReasons) > 3 {
		verdict.skipReasons = append(verdict.skipReasons[:3], "…")
	}
	return verdict
}

// firstLine 返回文本的第一行，用于压缩失败信息。
func firstLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if index := strings.IndexByte(trimmed, '\n'); index >= 0 {
		trimmed = strings.TrimSpace(trimmed[:index])
	}
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "…"
	}
	return trimmed
}

// readVitestReport 读取并解析 Vitest JSON 报告。
func readVitestReport(path string) (vitestJSONReport, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return vitestJSONReport{}, fmt.Errorf("报告文件 %s 不可读: %w", path, err)
	}
	var report vitestJSONReport
	if err := json.Unmarshal(content, &report); err != nil {
		return vitestJSONReport{}, fmt.Errorf("报告文件 %s 不是合法 JSON: %w", path, err)
	}
	return report, nil
}
