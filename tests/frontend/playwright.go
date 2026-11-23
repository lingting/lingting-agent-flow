package frontend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// playwrightJSONReport 是 Playwright JSON reporter 的输出（只取本适配器需要的字段）。
type playwrightJSONReport struct {
	Stats  playwrightStats   `json:"stats"`
	Suites []playwrightSuite `json:"suites"`
}

// playwrightStats 是 Playwright 自报的用例结论计数。
type playwrightStats struct {
	Expected   int `json:"expected"`
	Unexpected int `json:"unexpected"`
	Flaky      int `json:"flaky"`
	Skipped    int `json:"skipped"`
}

// playwrightSuite 是 Playwright 的套件节点，describe 会形成嵌套。
type playwrightSuite struct {
	Title  string            `json:"title"`
	Specs  []playwrightSpec  `json:"specs"`
	Suites []playwrightSuite `json:"suites"`
}

// playwrightSpec 是一条 spec（一个 test 或一组参数化用例）。
type playwrightSpec struct {
	Title string           `json:"title"`
	OK    bool             `json:"ok"`
	Tests []playwrightCase `json:"tests"`
}

// playwrightCase 是 spec 在某个 project 下的一次执行。
type playwrightCase struct {
	Status string `json:"status"`
}

// runPlaywright 执行一个 Playwright spec 文件并映射结论。
//
// 结论只取自 JSON 报告与退出码：两者冲突一律判失败，不因为退出码为 0 就认为通过。
func runPlaywright(c *testrunner.Context, name string) {
	uiDir, err := uiRoot()
	if err != nil {
		c.Fatalf("%v", err)
	}
	requireFrontendTooling(c, uiDir, "playwright")
	devServer, ok := c.Resource("dev-server")
	if !ok {
		c.Fatalf("缺少 frontend-dev-server 句柄，无法执行 Playwright 用例")
	}
	browser, ok := c.Resource("browser")
	if !ok {
		c.Fatalf("缺少 browser 句柄，无法登记 Playwright 进程")
	}
	baseURL := devServer.PayloadValue("base_url")
	if baseURL == "" {
		c.Fatalf("frontend-dev-server 句柄缺少 base_url")
	}
	artifactDir := c.ArtifactDir()
	reportPath := filepath.Join(artifactDir, "playwright.json")
	outputDir := filepath.Join(artifactDir, "playwright-output")
	run, err := runFrontendCLI(c, cliRequest{
		uiDir: uiDir,
		args: []string{
			"exec", "playwright", "test",
			"tests/e2e/" + name + ".spec.ts",
			"--workers=1",
			"--output", outputDir,
		},
		env: cliEnv(artifactDir, map[string]string{
			"AGENTFLOW_UI_BASE_URL":    baseURL,
			"AGENTFLOW_UI_JSON_REPORT": reportPath,
		}),
		logPath: filepath.Join(artifactDir, "playwright.log"),
		session: browser,
	})
	if err != nil {
		c.Fatalf("执行 Playwright 用例失败: %v", err)
	}
	report, err := readPlaywrightReport(reportPath)
	if err != nil {
		c.Fatalf("读取 Playwright JSON 报告失败: %v\n退出码 %d\n%s", err, run.exitCode, run.output)
	}
	verdict := summarizePlaywright(report)
	c.Logf(
		"Playwright %s：共 %d 条，失败 %d，跳过 %d，退出码 %d；报告 %s",
		name,
		verdict.total,
		len(verdict.failed),
		verdict.skipped,
		run.exitCode,
		reportPath,
	)
	switch {
	case len(verdict.failed) > 0:
		c.Errorf(
			"Playwright 用例失败 %d 条：%s\n退出码 %d\n%s",
			len(verdict.failed),
			strings.Join(verdict.failed, "；"),
			run.exitCode,
			run.output,
		)
	case verdict.total == 0:
		c.Errorf("Playwright JSON 报告里没有任何用例，视为无法确认结论\n%s", run.output)
	case run.exitCode != 0:
		c.Errorf(
			"Playwright 退出码 %d 与 JSON 结论（失败 0）冲突，按失败处理\n%s",
			run.exitCode,
			run.output,
		)
	case verdict.skipped == verdict.total:
		c.Skipf("该 spec 的全部用例都被跳过：%s", strings.Join(verdict.skipReasons, "；"))
	}
}

// playwrightVerdict 是 Playwright 报告的映射结论。
type playwrightVerdict struct {
	total       int
	failed      []string
	skipped     int
	skipReasons []string
}

// summarizePlaywright 递归收集全部 spec 的结论。
func summarizePlaywright(report playwrightJSONReport) playwrightVerdict {
	verdict := playwrightVerdict{}
	var walk func(suites []playwrightSuite, prefix []string)
	walk = func(suites []playwrightSuite, prefix []string) {
		for _, suite := range suites {
			titles := append(append([]string{}, prefix...), suite.Title)
			for _, spec := range suite.Specs {
				verdict.total++
				path := strings.TrimSpace(strings.Join(append(append([]string{}, titles...), spec.Title), " › "))
				if !spec.OK && !allSkipped(spec.Tests) {
					verdict.failed = append(verdict.failed, path)
					continue
				}
				if allSkipped(spec.Tests) {
					verdict.skipped++
					verdict.skipReasons = append(verdict.skipReasons, path)
				}
			}
			walk(suite.Suites, titles)
		}
	}
	walk(report.Suites, nil)
	if len(verdict.skipReasons) > 3 {
		verdict.skipReasons = append(verdict.skipReasons[:3], "…")
	}
	return verdict
}

// allSkipped 判断 spec 的全部执行都是 skipped。
func allSkipped(tests []playwrightCase) bool {
	if len(tests) == 0 {
		return false
	}
	for _, test := range tests {
		if test.Status != "skipped" {
			return false
		}
	}
	return true
}

// readPlaywrightReport 读取并解析 Playwright JSON 报告。
func readPlaywrightReport(path string) (playwrightJSONReport, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return playwrightJSONReport{}, fmt.Errorf("报告文件 %s 不可读: %w", path, err)
	}
	var report playwrightJSONReport
	if err := json.Unmarshal(content, &report); err != nil {
		return playwrightJSONReport{}, fmt.Errorf("报告文件 %s 不是合法 JSON: %w", path, err)
	}
	return report, nil
}
