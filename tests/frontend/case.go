package frontend

import (
	"time"

	"github.com/lingting/agentflow/internal/testrunner"
)

const (
	// uiProjectDir 是前端项目目录（相对仓库根）。
	uiProjectDir = "lingting-agent-flow-ui"
	// playwrightSuiteTimeout 是单个 Playwright 文件的执行上限：实测最大文件约 25 秒。
	playwrightSuiteTimeout = 10 * time.Minute
	// vitestSuiteTimeout 是单个 Vitest 文件的执行上限：实测单文件约 3 秒。
	vitestSuiteTimeout = 5 * time.Minute
)

// playwrightSpecs 是 tests/e2e 下的 24 个 spec，取值是去掉 .spec.ts 后缀的文件名。
var playwrightSpecs = []string{
	"api-connection",
	"layout",
	"planning-analysis-states",
	"planning-confirm",
	"planning-states",
	"planning-workbench",
	"projects",
	"requirement-delete",
	"settings-environments",
	"task-closeout",
	"task-code-save-verification",
	"task-code-saves",
	"task-containers",
	"task-create-project-inheritance",
	"task-create-selectors",
	"task-event-history",
	"task-execution",
	"task-launch",
	"task-progress-auto-refresh",
	"task-resume-action",
	"task-resume-eligibility",
	"task-retry",
	"task-step-timestamps",
	"tasks",
}

// vitestFiles 是 tests/unit 下的 13 个 Vitest 文件，取值是去掉 .test.ts 后缀的文件名。
var vitestFiles = []string{
	"planning",
	"planning-confirm",
	"projects",
	"requirement-actions",
	"settings-environments",
	"task-closeout",
	"task-code-save-verification",
	"task-code-saves",
	"task-containers",
	"task-environments",
	"task-resume-eligibility",
	"task-retry",
	"tasks-page",
}

// frontendGroup 按产品领域返回 CI 分片组。新增前端测试文件时必须显式归类，避免遗漏门禁。
func frontendGroup(name string) string {
	switch name {
	case "api-connection", "layout":
		return "frontend-shell"
	case "planning-analysis-states", "planning-confirm", "planning-states", "planning-workbench", "planning":
		return "frontend-planning"
	case "projects":
		return "frontend-projects"
	case "requirement-delete", "requirement-actions":
		return "frontend-requirements"
	case "settings-environments":
		return "frontend-settings"
	case "tasks", "task-create-project-inheritance", "task-create-selectors", "task-launch", "tasks-page":
		return "frontend-task-intake"
	case "task-closeout", "task-event-history", "task-execution", "task-progress-auto-refresh", "task-step-timestamps":
		return "frontend-task-execution"
	case "task-resume-action", "task-resume-eligibility", "task-retry":
		return "frontend-task-recovery"
	case "task-code-save-verification", "task-code-saves", "task-containers", "task-environments":
		return "frontend-task-resources"
	default:
		panic("frontend case has no CI domain mapping: " + name)
	}
}

func init() {
	for _, name := range playwrightSpecs {
		registerPlaywright(name)
	}
	for _, name := range vitestFiles {
		registerVitest(name)
	}
}

// registerPlaywright 登记一条 Playwright spec 用例。
//
// Package 记录来源文件，既是串行作用域（每个文件各自独立），也是报告里的来源位置。
func registerPlaywright(name string) {
	file := uiProjectDir + "/tests/e2e/" + name + ".spec.ts"
	testrunner.Register(testrunner.Case{
		ID:       frontendGroup(name) + "/playwright-" + name,
		Package:  file,
		Parallel: true,
		Timeout:  playwrightSuiteTimeout,
		Resources: []testrunner.ResourceDecl{
			{
				Name:  "dev-server",
				Type:  testrunner.ResourceFrontendDevServer,
				Scope: testrunner.ScopeRunShared,
			},
			{
				Name:  "browser",
				Type:  testrunner.ResourceBrowser,
				Scope: testrunner.ScopeCaseExclusive,
			},
		},
		Run: func(c *testrunner.Context) {
			runPlaywright(c, name)
		},
	})
}

// registerVitest 登记一条 Vitest 文件用例。
func registerVitest(name string) {
	file := uiProjectDir + "/tests/unit/" + name + ".test.ts"
	testrunner.Register(testrunner.Case{
		ID:       frontendGroup(name) + "/vitest-" + name,
		Package:  file,
		Parallel: true,
		Timeout:  vitestSuiteTimeout,
		Resources: []testrunner.ResourceDecl{
			{
				Name:  "node",
				Type:  testrunner.ResourceNodeWorker,
				Scope: testrunner.ScopeCaseExclusive,
			},
		},
		Run: func(c *testrunner.Context) {
			runVitest(c, name)
		},
	})
}
