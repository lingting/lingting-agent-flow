package caseinventory

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
)

// Build 扫描仓库并生成冻结清单与资源矩阵。
func Build(root string) (Result, error) {
	tracked, err := listTrackedFiles(root)
	if err != nil {
		return Result{}, err
	}
	commit, err := baselineCommit(root)
	if err != nil {
		return Result{}, err
	}
	sources, err := scanGoSources(root, tracked)
	if err != nil {
		return Result{}, err
	}
	packages := groupPackages(sources)
	frontend := scanFrontendCases(tracked)
	cases, notices := buildCases(packages, frontend)

	manifest := Manifest{
		SchemaVersion:  ManifestSchemaVersion,
		BaselineCommit: commit,
		Counts:         countCases(packages, frontend, cases),
		Cases:          cases,
	}
	return Result{
		Manifest: manifest,
		Matrix:   buildMatrix(commit, packages, frontend),
		Notices:  notices,
	}, nil
}

// listTrackedFiles 返回已跟踪文件（仓库相对路径、斜杠分隔）。
//
// 只用 git ls-files 枚举：它天然跳过被忽略的构建产物与依赖目录，不需要遍历工作区。
func listTrackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "ls-files", "-z")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("枚举已跟踪文件失败（需在 Git 仓库内运行）: %w", err)
	}
	var files []string
	for _, item := range strings.Split(string(output), "\x00") {
		if item == "" {
			continue
		}
		files = append(files, path.Clean(strings.ReplaceAll(item, "\\", "/")))
	}
	return files, nil
}

// baselineCommit 返回盘点基线提交。
func baselineCommit(root string) (string, error) {
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("读取基线提交失败: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// scanGoSources 解析测试目录下的全部已跟踪 Go 文件；非 tests/ 下的测试文件同样纳入。
func scanGoSources(root string, tracked []string) ([]goSource, error) {
	var sources []goSource
	for _, relPath := range tracked {
		if !strings.HasSuffix(relPath, ".go") {
			continue
		}
		inTests := strings.HasPrefix(relPath, TestsPrefix)
		if !inTests && !strings.HasSuffix(relPath, "_test.go") {
			continue
		}
		source, err := scanGoSource(root, relPath)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// groupPackages 按目录聚合 Go 源码；资源证据按包（而非单文件）生效，因为同包的
// 测试辅助文件与用例共享同一套资源。
func groupPackages(sources []goSource) []*packageScan {
	byDir := map[string]*packageScan{}
	for _, source := range sources {
		pkg := byDir[source.Dir]
		if pkg == nil {
			pkg = &packageScan{
				Dir:         source.Dir,
				PackagePath: packagePath(source.Dir),
				Symbols:     map[string]bool{},
				Imports:     map[string]bool{},
			}
			byDir[source.Dir] = pkg
		}
		pkg.Files = append(pkg.Files, source)
		pkg.SourceAll += source.Source + "\n"
		if source.IsTest {
			pkg.TestSignals.add(source.Signals)
		}
		pkg.Markers.merge(source.Markers)
		if source.HasTestMain {
			pkg.HasTestMain = true
		}
		if source.IsTest {
			pkg.TestFileCount++
		}
		pkg.CaseCount += len(source.Funcs)
		for name := range source.Symbols {
			pkg.Symbols[name] = true
		}
		for _, importedPath := range source.Imports {
			pkg.Imports[importedPath] = true
		}
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	packages := make([]*packageScan, 0, len(dirs))
	for _, dir := range dirs {
		pkg := byDir[dir]
		sort.Slice(pkg.Files, func(i, j int) bool { return pkg.Files[i].RelPath < pkg.Files[j].RelPath })
		packages = append(packages, pkg)
	}
	return packages
}

// buildCases 生成用例清单；清单同时用于发现"测试文件不在 tests/ 下"的约定偏离。
func buildCases(packages []*packageScan, frontend []frontendCase) ([]Case, []string) {
	var cases []Case
	var notices []string
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			if !file.IsTest {
				continue
			}
			if !strings.HasPrefix(file.RelPath, TestsPrefix) {
				notices = append(notices, fmt.Sprintf("测试文件不在 tests/ 下（违反目录约定）: %s", file.RelPath))
			}
			for _, fn := range file.Funcs {
				cases = append(cases, Case{
					ID:          RuntimeGo + ":" + file.RelPath + ":" + fn.Name,
					Runtime:     RuntimeGo,
					Stage:       stageForGo(file.RelPath),
					File:        file.RelPath,
					PackageDir:  file.Dir,
					PackagePath: packagePath(file.Dir),
					Name:        fn.Name,
					Subtests:    fn.Subtests,
				})
			}
		}
	}
	for _, item := range frontend {
		cases = append(cases, Case{
			ID:      item.Runtime + ":" + item.RelPath,
			Runtime: item.Runtime,
			Stage:   item.Stage,
			File:    item.RelPath,
			Name:    item.Name,
		})
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, notices
}

// countCases 汇总规模与调用点统计。
func countCases(packages []*packageScan, frontend []frontendCase, cases []Case) Counts {
	counts := Counts{}
	var total signals
	for _, pkg := range packages {
		if pkg.TestFileCount > 0 {
			counts.GoTestPackages++
		}
		counts.GoTestFiles += pkg.TestFileCount
		counts.GoTestFunctions += pkg.CaseCount
		if pkg.HasTestMain {
			counts.GoTestMain++
		}
		total.add(pkg.TestSignals)
		for _, file := range pkg.Files {
			counts.GoSubtestCalls += file.Signals.Run
			for _, fn := range file.Funcs {
				counts.GoSubtests += len(fn.Subtests)
			}
		}
	}
	counts.Signals = Signals{
		FatalCalls:    total.Fatal,
		ErrorCalls:    total.Error,
		HelperCalls:   total.Helper,
		CleanupCalls:  total.Cleanup,
		ParallelCalls: total.Parallel,
		SkipCalls:     total.Skip,
		TempDirCalls:  total.TempDir,
	}
	for _, item := range frontend {
		switch item.Runtime {
		case RuntimeVitest:
			counts.VitestFiles++
		case RuntimePlaywright:
			counts.PlaywrightFiles++
		}
	}
	counts.Cases = len(cases)
	for _, item := range cases {
		if item.Stage != StageKeepGoTest {
			counts.MigrationCases++
		}
	}
	return counts
}

// buildMatrix 生成资源矩阵；没有测试文件的包不进矩阵。
func buildMatrix(commit string, packages []*packageScan, frontend []frontendCase) ResourceMatrix {
	matrix := ResourceMatrix{
		SchemaVersion:  ResourceMatrixSchemaVersion,
		BaselineCommit: commit,
		ResourceTypes:  resourceTypes,
	}
	for _, pkg := range packages {
		if pkg.TestFileCount == 0 {
			continue
		}
		matrix.Packages = append(matrix.Packages, packageResources(pkg))
		matrix.ParallelSuspects = append(matrix.ParallelSuspects, parallelSuspects(pkg)...)
	}
	for _, item := range frontend {
		matrix.Frontend = append(matrix.Frontend, frontendResources(item))
	}
	return matrix
}

// packageResources 组装一个测试包的资源声明与待确认项。
func packageResources(pkg *packageScan) PackageResources {
	declaration := PackageResources{
		Dir:              pkg.Dir,
		PackagePath:      pkg.PackagePath,
		Stage:            stageForPackage(pkg),
		CaseCount:        pkg.CaseCount,
		RealTestGate:     pkg.Symbols["RequireRealTests"],
		RealDependencies: realDependencies(pkg),
		Resources:        inferResources(pkg),
		DestructiveHints: destructiveHints(pkg),
	}
	if reason := serialReason(pkg); reason != "" {
		declaration.SerialDeclared = true
		declaration.SerialReason = reason
	}
	declaration.Sharing = classifySharing(declaration)
	declaration.ReviewReasons = packageReviewReasons(pkg, declaration)
	declaration.NeedsReview = len(declaration.ReviewReasons) > 0
	return declaration
}

// classifySharing 按"是否可能对共享资源造成不可逆损害"给出共享语义初判。
func classifySharing(declaration PackageResources) string {
	if declaration.Stage == StageKeepGoTest || len(declaration.Resources) == 0 {
		return SharingSharedCandidate
	}
	if len(declaration.DestructiveHints) > 0 {
		return SharingExclusiveRequired
	}
	if declaration.RealTestGate {
		return SharingUnclassified
	}
	return SharingSharedCandidate
}

// packageReviewReasons 列出必须由人工确认的条目，空表示可以按推断结果调度。
func packageReviewReasons(pkg *packageScan, declaration PackageResources) []string {
	var reasons []string
	if declaration.RealTestGate {
		reasons = append(reasons, "真实依赖门禁（RequireRealTests）：共享或独享必须在 R2 按用例确认")
	}
	if hasResource(declaration.Resources, "remote-container") {
		reasons = append(reasons, "真实容器按用例独享；镜像构建属 Docker 写入，需走缓存产物通道（P1-6）")
	}
	if declaration.Stage != StageKeepGoTest && len(declaration.Resources) > 0 && len(declaration.DestructiveHints) > 0 {
		reasons = append(reasons, "命中不可逆操作线索，禁止与共享资源组合")
	}
	if len(declaration.Resources) == 0 && pkg.isIntegrationOrE2E() {
		reasons = append(reasons, "集成包未推断出任何资源：需确认是纯内存用例还是漏识别")
	}
	return reasons
}

// hasResource 判断声明里是否包含指定资源类型。
func hasResource(needs []ResourceNeed, resource string) bool {
	for _, need := range needs {
		if need.Type == resource {
			return true
		}
	}
	return false
}

// stageForPackage 决定测试包在矩阵中的迁移阶段。
func stageForPackage(pkg *packageScan) string {
	if pkg.isIntegrationOrE2E() {
		return StageIntegration
	}
	return StageKeepGoTest
}

// stageForGo 决定 Go 用例的迁移阶段：路径 C 只迁移集成与 E2E。
func stageForGo(relPath string) string {
	switch {
	case strings.HasPrefix(relPath, "tests/integration/"), strings.HasPrefix(relPath, "tests/e2e/"):
		return StageIntegration
	default:
		return StageKeepGoTest
	}
}

// packagePath 由目录得到 Go 导入路径。
func packagePath(dir string) string {
	return ModulePath + "/" + dir
}

// ensureRootExists 校验根目录存在，给出比 git 报错更明确的信息。
func ensureRootExists(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("根目录不可用: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("根目录不是目录: %s", root)
	}
	return nil
}
