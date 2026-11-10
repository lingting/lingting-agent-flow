package caseinventory

import (
	"path"
	"sort"
	"strings"
)

// frontendCase 是前端用例：一个文件就是一个用例，与前端测试的并行模型一致。
type frontendCase struct {
	RelPath string
	Runtime string
	Stage   string
	Name    string
}

// scanFrontendCases 从已跟踪文件列表中挑出前端用例文件。
func scanFrontendCases(tracked []string) []frontendCase {
	var cases []frontendCase
	for _, relPath := range tracked {
		switch {
		case strings.HasPrefix(relPath, FrontendRoot+"tests/e2e/") && strings.HasSuffix(relPath, ".spec.ts"):
			cases = append(cases, frontendCase{
				RelPath: relPath,
				Runtime: RuntimePlaywright,
				Stage:   StageFrontend,
				Name:    strings.TrimSuffix(path.Base(relPath), ".spec.ts"),
			})
		case strings.HasPrefix(relPath, FrontendRoot+"tests/unit/") && strings.HasSuffix(relPath, ".test.ts"):
			cases = append(cases, frontendCase{
				RelPath: relPath,
				Runtime: RuntimeVitest,
				Stage:   StageFrontend,
				Name:    strings.TrimSuffix(path.Base(relPath), ".test.ts"),
			})
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].RelPath < cases[j].RelPath })
	return cases
}

// frontendResources 返回一个前端用例文件的资源声明。
//
// 证据固定在两个配置文件的既有事实上：Playwright 的 webServer 只启动一个 dev server
// 并在全部 worker 间共享，浏览器实例则按 worker 独占；Vitest 的并发单位是 fork 进程。
func frontendResources(item frontendCase) FrontendResources {
	var builder needBuilder
	switch item.Runtime {
	case RuntimePlaywright:
		builder.add("frontend-dev-server", ScopeRunShared,
			[]string{"config:playwright.config.ts webServer"})
		builder.add("browser", ScopeCaseExclusive,
			[]string{"config:playwright.config.ts projects[chromium]"})
	case RuntimeVitest:
		builder.add("node-worker", ScopeCaseExclusive,
			[]string{"config:vite.config.ts test.include"})
	}
	declaration := FrontendResources{
		File:      item.RelPath,
		Runtime:   item.Runtime,
		CaseID:    item.Runtime + ":" + item.RelPath,
		Resources: builder.finish(),
		Sharing:   SharingSharedCandidate,
	}
	if item.Runtime == RuntimePlaywright {
		declaration.NeedsReview = true
		declaration.ReviewReasons = []string{
			"P1-5：共享 dev server 的写操作策略需在 R6 确认；当前 spec 全部使用受控后端" +
				"（page.route 或 tests/e2e 下的 helper），不产生真实服务端写入",
		}
	}
	return declaration
}
