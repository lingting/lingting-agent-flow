package release_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 工作流文件路径（相对仓库根目录）。
// 工作流文件路径（相对仓库根目录）。
const (
	ciWorkflowPath      = ".github/workflows/ci.yml"
	releaseWorkflowPath = ".github/workflows/release.yml"
	publishWorkflowPath = ".github/workflows/publish.yml"
)

// workflowPaths 是参与静态核验的全部工作流文件。
func workflowPaths() []string {
	return []string{ciWorkflowPath, releaseWorkflowPath, publishWorkflowPath}
}

// unpinnedActionAllowList 是唯一允许不按提交 SHA 固定的 Action 引用。
//
// 来源证明 Action 必须使用 GitHub 官方发布的版本引用：只有签发方持续维护的
// 无密钥证明实现才能产出可被 gh CLI 验证的证明。这条豁免被下面的用例逐条约束：
// 只能是这一条、只能出现在 publish.yml、且必须仍由发布流程强制要求证明成功。
var unpinnedActionAllowList = map[string]string{
	"actions/attest-build-provenance@v3": publishWorkflowPath,
}

// pinnedActionPattern 是 Action 引用被提交 SHA 固定的形式，例如
// actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1。
//
// 浮动标签会在上游重新指向时改变构建行为，因此 CI 与发布工作流都必须固定 SHA。
var pinnedActionPattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+(/[\w./-]+)?@[0-9a-f]{40}$`)

// expressionPattern 匹配 GitHub Actions 表达式。
var expressionPattern = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

// expressionContextPattern 取属性访问链的上下文名（第一个点号之前的部分）。
var expressionContextPattern = regexp.MustCompile(`^[!(\s]*([A-Za-z_][A-Za-z0-9_-]*)\.`)

// forbiddenExpressionContexts 是在任何位置都不允许出现的上下文。
//
// 发布构建不使用任何仓库凭据；一旦表达式里出现这些上下文，说明有人在往构建
// 参数或日志里塞凭据。
var forbiddenExpressionContexts = []string{"secrets"}

// allowedExpressionContexts 是允许出现在本项目工作流里的上下文。
//
// jobs 只在 workflow_call.outputs.value 里合法（复用工作流对外暴露的结论），
// 因此它出现在表达式里不代表可以随便访问 Job 内部状态。
var allowedExpressionContexts = []string{
	"github", "inputs", "matrix", "needs", "steps", "env", "strategy", "job", "jobs", "runner", "vars",
}

// repoRootPath 返回仓库根目录，找不到 go.mod 时结束用例。
func repoRootPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取当前目录失败: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未能定位仓库根目录: %s", dir)
		}
		dir = parent
	}
}

// loadWorkflow 解析工作流 YAML，解析失败直接结束用例。
func loadWorkflow(t *testing.T, relative string) map[string]any {
	t.Helper()
	path := filepath.Join(repoRootPath(t), relative)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取工作流 %s 失败: %v", relative, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("解析工作流 %s 失败: %v", relative, err)
	}
	return document
}

// readWorkflowText 读取工作流原文，用于检查 YAML 表达式无法表达的约束（例如禁用参数）。
func readWorkflowText(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRootPath(t), relative))
	if err != nil {
		t.Fatalf("读取工作流 %s 失败: %v", relative, err)
	}
	return string(data)
}

// mapValue 取出嵌套的映射字段，缺失或类型不符时返回 nil。
func mapValue(value any, keys ...string) map[string]any {
	current := value
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[key]
	}
	result, ok := current.(map[string]any)
	if !ok {
		return nil
	}
	return result
}

// hasKey 判断映射里是否存在某个键。
//
// `workflow_dispatch:` 这种没有参数的触发器在 YAML 里是 null，用 mapValue 判断会
// 误判成"不存在"，因此存在性检查必须单独实现。
func hasKey(value any, key string) bool {
	asMap, ok := value.(map[string]any)
	if !ok {
		return false
	}
	_, ok = asMap[key]
	return ok
}

// conditionExpressions 收集工作流里所有 if 条件（Job 与步骤两级）。
//
// GitHub 允许 if 省略 ${{ }} 直接写表达式，这类条件不会被表达式扫描覆盖，因此必须
// 单独检查它们引用了哪些上下文。
func conditionExpressions(workflow map[string]any) []string {
	conditions := make([]string, 0, 4)
	for _, jobValue := range mapValue(workflow, "jobs") {
		job, ok := jobValue.(map[string]any)
		if !ok {
			continue
		}
		if condition, ok := job["if"].(string); ok && condition != "" {
			conditions = append(conditions, condition)
		}
		for _, stepValue := range listValue(job, "steps") {
			step, ok := stepValue.(map[string]any)
			if !ok {
				continue
			}
			if condition, ok := step["if"].(string); ok && condition != "" {
				conditions = append(conditions, condition)
			}
		}
	}
	return conditions
}

// listValue 取出嵌套的列表字段，缺失或类型不符时返回 nil。
func listValue(value any, keys ...string) []any {
	current := value
	for index, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[key]
		if index == len(keys)-1 {
			result, ok := current.([]any)
			if !ok {
				return nil
			}
			return result
		}
	}
	return nil
}

// expressionContext 返回表达式里属性访问链的上下文名，没有属性访问时返回空串。
//
// 只取链头：needs.go-static.outputs.tests 的上下文是 needs，后面的 go-static 与
// outputs 都是它下面的字段。把每一段都当成上下文会产生大量误报。
func expressionContext(expression string) string {
	match := expressionContextPattern.FindStringSubmatch(expression)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// stringList 取出嵌套的字符串列表字段，同时接受单值写法（needs: resolve）。
//
// YAML 里 needs: resolve 与 needs: [resolve] 语义相同但类型不同；门禁判断依赖这层
// 关系，不能因为写法差异而漏检。
func stringList(value any, keys ...string) []string {
	if len(keys) == 0 {
		return nil
	}
	current := value
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[key]
	}
	switch typed := current.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return nil
			}
			result = append(result, name)
		}
		return result
	default:
		return nil
	}
}

// jobSteps 返回指定 Job 的步骤列表。
func jobSteps(t *testing.T, workflow map[string]any, job string) []any {
	t.Helper()
	steps := listValue(workflow, "jobs", job, "steps")
	if steps == nil {
		t.Fatalf("Job %s 缺少 steps", job)
	}
	return steps
}

// jobMap 返回指定 Job 的映射。
func jobMap(t *testing.T, workflow map[string]any, job string) map[string]any {
	t.Helper()
	value := mapValue(workflow, "jobs", job)
	if value == nil {
		t.Fatalf("工作流缺少 Job %s", job)
	}
	return value
}

// triggerBlock 返回触发器定义。
//
// YAML 1.1 会把裸写的 on 解析成布尔 true，因此这里同时接受两种键名。
func triggerBlock(t *testing.T, workflow map[string]any) map[string]any {
	t.Helper()
	if trigger := mapValue(workflow, "on"); trigger != nil {
		return trigger
	}
	trigger := mapValue(workflow, "true")
	if trigger == nil {
		t.Fatal("工作流缺少触发器定义")
	}
	return trigger
}

// useStatements 返回工作流中全部使用 Action 的引用字符串。
func useStatements(workflow map[string]any) []string {
	var uses []string
	jobs := mapValue(workflow, "jobs")
	for _, jobValue := range jobs {
		for _, stepValue := range listValue(jobValue, "steps") {
			step, ok := stepValue.(map[string]any)
			if !ok {
				continue
			}
			if reference, ok := step["uses"].(string); ok && reference != "" {
				uses = append(uses, reference)
			}
		}
	}
	return uses
}

// TestReleaseWorkflowTriggers 校验构建工作流只被复用或手动触发。
//
// 版本标签不再直接触发构建工作流：标签发布统一走 publish.yml，由它先跑 CI 门禁再
// 复用本工作流构建，避免同一个标签被构建两次、产出两份不同的字节。
func TestReleaseWorkflowTriggers(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, releaseWorkflowPath)
	trigger := triggerBlock(t, workflow)
	if mapValue(trigger, "push") != nil {
		t.Error("构建工作流不应由 push 直接触发：标签发布必须经过 publish.yml 的门禁")
	}
	if mapValue(trigger, "workflow_call") == nil {
		t.Fatal("构建工作流必须提供 workflow_call，供 publish.yml 复用同一份构建逻辑")
	}
	if mapValue(trigger, "workflow_dispatch") == nil {
		t.Fatal("构建工作流必须支持 workflow_dispatch 手动预演")
	}
	// 复用契约：调用方需要拿到版本、提交与制品名，否则无法下载同一份制品。
	outputs := mapValue(trigger, "workflow_call", "outputs")
	for _, name := range []string{"version", "commit", "artifact_name"} {
		if outputs == nil {
			t.Fatalf("workflow_call 缺少 outputs 定义（需要 %s）", name)
		}
		if mapValue(outputs, name) == nil {
			t.Errorf("workflow_call 缺少输出 %s", name)
		}
	}
	inputs := mapValue(trigger, "workflow_call", "inputs")
	if inputs == nil || mapValue(inputs, "version") == nil {
		t.Error("workflow_call 必须声明 version 输入，由调用方传入标签解析结果")
	}
}

// TestPublishWorkflowTriggers 校验正式发布工作流由版本标签或手动触发。
func TestPublishWorkflowTriggers(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, publishWorkflowPath)
	trigger := triggerBlock(t, workflow)
	tags := listValue(trigger, "push", "tags")
	if len(tags) != 1 || tags[0] != "v*.*.*" {
		t.Fatalf("正式发布工作流必须由版本标签 v*.*.* 触发，实际: %v", tags)
	}
	if !hasKey(trigger, "workflow_dispatch") {
		t.Fatal("正式发布工作流必须支持 workflow_dispatch 手动触发")
	}
	if hasKey(trigger, "pull_request") || hasKey(trigger, "pull_request_target") {
		t.Error("正式发布工作流不得由 Pull Request 触发")
	}
	raw := readWorkflowText(t, publishWorkflowPath)
	// 不可信 PR 代码不能拿到发布凭据；workflow_run 也会绕过门禁，因此同样禁止。
	for _, forbidden := range []string{"pull_request_target", "workflow_run", "continue-on-error"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("正式发布工作流不应包含 %q", forbidden)
		}
	}
}

// TestReleaseWorkflowPermissions 校验权限最小化：只读仓库内容，不使用 secrets。
func TestReleaseWorkflowPermissions(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, releaseWorkflowPath)
	permissions := mapValue(workflow, "permissions")
	if permissions == nil {
		t.Fatal("发布工作流必须显式声明 permissions")
	}
	if permissions["contents"] != "read" {
		t.Fatalf("发布工作流 contents 权限应为 read，实际 %v", permissions["contents"])
	}
	raw, err := os.ReadFile(filepath.Join(repoRootPath(t), releaseWorkflowPath))
	if err != nil {
		t.Fatalf("读取发布工作流原文失败: %v", err)
	}
	text := string(raw)
	forbidden := []string{"contents: write", "secrets.", "pull_request_target", "NODE_AUTH_TOKEN", "docker push", "gh release create"}
	for _, item := range forbidden {
		if strings.Contains(text, item) {
			t.Errorf("发布工作流不应包含 %q", item)
		}
	}
}

// TestReleaseWorkflowStages 校验发布构建的阶段顺序与必需步骤。
//
// 顺序必须是"先构建前端、再编译 Go"：反过来会让二进制里保留旧 UI。
func TestReleaseWorkflowStages(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, releaseWorkflowPath)
	for _, job := range []string{"resolve", "build", "smoke", "release-summary"} {
		jobMap(t, workflow, job)
	}
	if needs := stringList(workflow, "jobs", "build", "needs"); len(needs) != 1 || needs[0] != "resolve" {
		t.Fatalf("build Job 必须依赖 resolve，实际 %v", needs)
	}
	smokeNeeds := stringList(workflow, "jobs", "smoke", "needs")
	if len(smokeNeeds) != 2 {
		t.Fatalf("smoke Job 必须依赖 resolve 与 build，实际 %v", smokeNeeds)
	}
	strategy := mapValue(workflow, "jobs", "smoke", "strategy")
	if strategy == nil || strategy["fail-fast"] != false {
		t.Fatal("smoke 的 fail-fast 必须为 false，否则一个平台的失败会掩盖其他平台的结论")
	}

	buildSteps := jobSteps(t, workflow, "build")
	indexes := make(map[string]int)
	for index, stepValue := range buildSteps {
		step, _ := stepValue.(map[string]any)
		name, _ := step["name"].(string)
		run, _ := step["run"].(string)
		switch {
		case strings.Contains(name, "前端依赖") && strings.Contains(run, "pnpm install --frozen-lockfile"):
			indexes["install"] = index
		case strings.Contains(run, "make release-dist"):
			indexes["release"] = index
		case strings.Contains(name, "上传发布制品"):
			indexes["upload"] = index
		case strings.Contains(name, "校验制品与校验文件一致"):
			indexes["verify"] = index
		}
	}
	for _, required := range []string{"install", "release", "upload", "verify"} {
		if _, ok := indexes[required]; !ok {
			t.Fatalf("build Job 缺少步骤 %s", required)
		}
	}
	if indexes["install"] > indexes["release"] {
		t.Fatal("前端依赖安装必须早于发布构建")
	}
	if indexes["release"] > indexes["verify"] || indexes["verify"] > indexes["upload"] {
		t.Fatal("顺序必须是：构建制品 → 校验制品 → 上传制品")
	}
	// 制品必须来自唯一的发布入口，而不是工作流里另拼一套编译命令。
	releaseStep, _ := buildSteps[indexes["release"]].(map[string]any)
	releaseRun, _ := releaseStep["run"].(string)
	if strings.Contains(releaseRun, "go build") {
		t.Fatal("发布工作流不得自己拼接 go build 命令")
	}
	uploadStep, _ := buildSteps[indexes["upload"]].(map[string]any)
	uploadWith := mapValue(uploadStep, "with")
	if uploadWith == nil || uploadWith["if-no-files-found"] != "error" {
		t.Fatal("制品上传必须设置 if-no-files-found: error，避免成功后遗漏制品")
	}
}

// TestReleaseWorkflowSmokeMatrix 校验冒烟矩阵恰好覆盖全部发布平台。
func TestReleaseWorkflowSmokeMatrix(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, releaseWorkflowPath)
	include := listValue(workflow, "jobs", "smoke", "strategy", "matrix", "include")
	if include == nil {
		t.Fatal("smoke 缺少矩阵定义")
	}
	expected := make(map[string]bool, len(testPlatforms))
	for _, value := range testPlatforms {
		expected[value] = false
	}
	for _, itemValue := range include {
		item, ok := itemValue.(map[string]any)
		if !ok {
			t.Fatalf("矩阵项格式错误: %v", itemValue)
		}
		osName, _ := item["os"].(string)
		arch, _ := item["arch"].(string)
		platform := osName + "/" + arch
		if _, ok := expected[platform]; !ok {
			t.Errorf("冒烟矩阵包含非发布平台: %s", platform)
			continue
		}
		if runner, ok := item["runner"].(string); !ok || runner == "" {
			t.Errorf("冒烟矩阵项 %s 缺少 runner", platform)
		}
		expected[platform] = true
	}
	for platform, seen := range expected {
		if !seen {
			t.Errorf("冒烟矩阵缺少发布平台: %s", platform)
		}
	}
	// 冒烟必须调用仓库内的 Go 用例，而不是在工作流里另写一套启动断言。
	found := false
	for _, stepValue := range jobSteps(t, workflow, "smoke") {
		step, _ := stepValue.(map[string]any)
		run, _ := step["run"].(string)
		if !strings.Contains(run, "TestReleaseArchiveSmoke") {
			continue
		}
		found = true
		if !strings.Contains(run, "./tests/e2e/packaging/...") {
			t.Errorf("冒烟必须执行仓库内的打包验收用例，实际: %s", run)
		}
	}
	if !found {
		t.Fatal("smoke Job 必须执行 TestReleaseArchiveSmoke")
	}
}

// TestWorkflowActionsPinned 校验全部工作流中的 Action 都用提交 SHA 固定。
//
// 只有 unpinnedActionAllowList 中逐条列出的引用可以例外，且必须出现在指定文件里：
// 任何新增的浮动引用都会让构建行为随上游标签漂移。
func TestWorkflowActionsPinned(t *testing.T) {
	t.Parallel()
	usedExceptions := make(map[string]string)
	for _, relative := range workflowPaths() {
		workflow := loadWorkflow(t, relative)
		uses := useStatements(workflow)
		if len(uses) == 0 {
			t.Fatalf("%s 未解析到任何 Action 引用", relative)
		}
		for _, reference := range uses {
			// 本地复用工作流（./.github/workflows/*.yml）不是第三方 Action，
			// 它由本仓库的提交内容决定，天然跟随当前 ref。
			if strings.HasPrefix(reference, "./") || pinnedActionPattern.MatchString(reference) {
				continue
			}
			allowedPath, ok := unpinnedActionAllowList[reference]
			if !ok {
				t.Errorf("%s 的 Action %q 未用提交 SHA 固定", relative, reference)
				continue
			}
			if allowedPath != relative {
				t.Errorf("Action %q 只允许出现在 %s，实际出现在 %s", reference, allowedPath, relative)
			}
			usedExceptions[reference] = relative
		}
	}
	// 豁免清单与工作流必须一致：清单里留着已经不用的条目会掩盖后来新增的浮动引用。
	for reference := range unpinnedActionAllowList {
		if _, ok := usedExceptions[reference]; !ok {
			t.Errorf("豁免清单中的 %q 在工作流里已不存在，请同步清理", reference)
		}
	}
}

// TestWorkflowExpressions 校验工作流中的表达式语法与可用上下文。
//
// 只允许已声明的上下文标识符，并拒绝 secrets：发布构建不需要任何仓库凭据。
func TestWorkflowExpressions(t *testing.T) {
	t.Parallel()
	allowed := make(map[string]bool, len(allowedExpressionContexts))
	for _, context := range allowedExpressionContexts {
		allowed[context] = true
	}
	for _, relative := range workflowPaths() {
		text := readWorkflowText(t, relative)
		// 括号数量必须配对：多写一个右括号会让整个工作流在运行时才失败。
		if strings.Count(text, "${{") != strings.Count(text, "}}") {
			t.Errorf("%s 的表达式括号不配对", relative)
		}
		for _, match := range expressionPattern.FindAllStringSubmatch(text, -1) {
			expression := match[1]
			if strings.TrimSpace(expression) == "" {
				t.Errorf("%s 存在空表达式: %q", relative, match[0])
			}
			if context := expressionContext(strings.TrimSpace(expression)); context != "" && !allowed[context] {
				t.Errorf("%s 的表达式使用了不允许的上下文 %q：%s", relative, context, match[0])
			}
			// 上下文出现在链中间（例如 format('0}', secrets.X)）时上面的检查看不到，
			// 因此对禁用上下文再做一次整体扫描。
			for _, forbidden := range forbiddenExpressionContexts {
				if strings.Contains(expression, forbidden+".") {
					t.Errorf("%s 的表达式引用了禁用的上下文 %q：%s", relative, forbidden, match[0])
				}
			}
		}
		// if 条件可以省略 ${{ }}，作为隐式表达式求值；这些条件同样不得引用禁用上下文。
		for _, condition := range conditionExpressions(loadWorkflow(t, relative)) {
			context := expressionContext(condition)
			if context != "" && !allowed[context] {
				t.Errorf("%s 的 if 条件使用了不允许的上下文 %q：%s", relative, context, condition)
			}
			for _, forbidden := range forbiddenExpressionContexts {
				if strings.Contains(condition, forbidden+".") {
					t.Errorf("%s 的 if 条件引用了禁用的上下文 %q：%s", relative, forbidden, condition)
				}
			}
		}
	}
}

// TestCIWorkflowGatePreserved 校验 CI 门禁结构在新增发布流程后仍然完整。
func TestCIWorkflowGatePreserved(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, ciWorkflowPath)
	permissions := mapValue(workflow, "permissions")
	if permissions == nil || permissions["contents"] != "read" {
		t.Fatal("CI 工作流的 contents 权限必须保持 read")
	}
	for _, job := range []string{"go-static", "go-test", "go-race", "ui-test", "ci-summary"} {
		jobMap(t, workflow, job)
	}
	if needs := stringList(workflow, "jobs", "ci-summary", "needs"); len(needs) != 4 {
		t.Fatalf("ci-summary 必须依赖四个必需 Job，实际 %v", needs)
	}
}

// stepIndex 返回指定 Job 中第一个名字包含 fragment 的步骤序号，找不到时结束用例。
func stepIndex(t *testing.T, workflow map[string]any, job string, fragment string) int {
	t.Helper()
	for index, stepValue := range jobSteps(t, workflow, job) {
		step, ok := stepValue.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := step["name"].(string); strings.Contains(name, fragment) {
			return index
		}
	}
	t.Fatalf("Job %s 缺少名字包含 %q 的步骤", job, fragment)
	return -1
}

// TestPublishWorkflowPermissions 校验发布权限只授予真正需要写入的 Job。
//
// 权限边界是发布流程的核心安全约束：构建与 CI Job 只读，只有创建 Release 与生成
// 来源证明的 Job 取得写权限，且整个流程不使用任何仓库 Secret。
func TestPublishWorkflowPermissions(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, publishWorkflowPath)
	permissions := mapValue(workflow, "permissions")
	if permissions == nil {
		t.Fatal("正式发布工作流必须显式声明 permissions")
	}
	if permissions["contents"] != "read" {
		t.Fatalf("工作流默认 contents 权限应为 read，实际 %v", permissions["contents"])
	}
	writeJobs := make([]string, 0, 2)
	jobs := mapValue(workflow, "jobs")
	for name, jobValue := range jobs {
		job, ok := jobValue.(map[string]any)
		if !ok {
			continue
		}
		jobPermissions, ok := job["permissions"].(map[string]any)
		if !ok {
			continue
		}
		for scope, level := range jobPermissions {
			if level == "write" {
				writeJobs = append(writeJobs, name+":"+scope)
			}
		}
	}
	expected := map[string]bool{
		"publish:contents":     false,
		"publish:id-token":     false,
		"publish:attestations": false,
	}
	for _, entry := range writeJobs {
		if _, ok := expected[entry]; !ok {
			t.Errorf("Job 权限超出最小化边界: %s", entry)
			continue
		}
		expected[entry] = true
	}
	for entry, seen := range expected {
		if !seen {
			t.Errorf("发布 Job 缺少必需权限: %s", entry)
		}
	}
	raw := readWorkflowText(t, publishWorkflowPath)
	for _, forbidden := range []string{"secrets.", "write-all", "NODE_AUTH_TOKEN", "docker push", "permissions: write"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("正式发布工作流不应包含 %q", forbidden)
		}
	}
	if strings.Contains(raw, "GH_TOKEN: ${{ secrets") {
		t.Error("发布工作流不应把 Secret 注入为 GH_TOKEN")
	}
}

// TestPublishWorkflowGates 校验发布依赖的 Job、门禁条件与执行顺序。
//
// 任何必需 Job 不是 success 都不能发布；制品只构建一次（复用 release.yml），
// 并且只有"上传后核验全部通过"才把草稿转成公开 Release。
func TestPublishWorkflowGates(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, publishWorkflowPath)
	for _, job := range []string{"resolve", "ci", "build", "publish", "publish-summary"} {
		jobMap(t, workflow, job)
	}
	if needs := stringList(workflow, "jobs", "publish", "needs"); len(needs) != 3 {
		t.Fatalf("publish Job 必须依赖 resolve / ci / build，实际 %v", needs)
	}
	condition, _ := jobMap(t, workflow, "publish")["if"].(string)
	for _, required := range []string{
		"needs.resolve.result == 'success'",
		"needs.ci.result == 'success'",
		"needs.build.result == 'success'",
	} {
		if !strings.Contains(condition, required) {
			t.Errorf("publish Job 的门禁条件缺少 %q：%s", required, condition)
		}
	}
	// 复用既有工作流，不在发布流程里重跑一套编译。
	ciUses, _ := jobMap(t, workflow, "ci")["uses"].(string)
	if ciUses != "./.github/workflows/ci.yml" {
		t.Errorf("CI 门禁必须复用 ci.yml，实际 %q", ciUses)
	}
	buildUses, _ := jobMap(t, workflow, "build")["uses"].(string)
	if buildUses != "./.github/workflows/release.yml" {
		t.Errorf("制品构建必须复用 release.yml，实际 %q", buildUses)
	}
	buildWith := mapValue(jobMap(t, workflow, "build"), "with")
	if buildWith == nil || buildWith["version"] != "${{ needs.resolve.outputs.version }}" {
		t.Errorf("构建工作流的版本必须来自标签解析结果，实际 %v", buildWith)
	}
	publishSteps := jobSteps(t, workflow, "publish")
	for _, stepValue := range publishSteps {
		step, _ := stepValue.(map[string]any)
		run, _ := step["run"].(string)
		if strings.Contains(run, "make release-dist") || strings.Contains(run, "go build") {
			t.Fatal("发布 Job 不得重新构建制品：必须复用 CI 验证过的同一份字节")
		}
	}

	// 顺序：核验 → 证明 → 复核证明声明 → 建草稿 → 上传 → 远端核验 → 证明核验 → 公开。
	ordered := []string{
		"发布前核验制品",
		"生成来源证明",
		"核验来源信息完整",
		"创建草稿 Release",
		"上传发布资产",
		"上传后核验远端资产",
		"核验来源证明",
		"发布正式 Release",
		"核验 Release 已公开",
	}
	previous := -1
	for _, fragment := range ordered {
		index := stepIndex(t, workflow, "publish", fragment)
		if index <= previous {
			t.Fatalf("发布步骤顺序错误：%q 不应早于前一步骤", fragment)
		}
		previous = index
	}
	// 汇总 Job 必须始终运行，并在任何阶段非成功时失败。
	if summaryIf, _ := jobMap(t, workflow, "publish-summary")["if"].(string); summaryIf != "always()" {
		t.Errorf("publish-summary 必须使用 if: always()，实际 %q", summaryIf)
	}
}

// TestPublishWorkflowAttestation 校验来源证明是发布的必要环节。
//
// 证明无法生成或无法验证时发布必须失败，不允许"降级为没有证明的发布"。
func TestPublishWorkflowAttestation(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, publishWorkflowPath)
	attestIndex := -1
	verifyIndex := -1
	for index, stepValue := range jobSteps(t, workflow, "publish") {
		step, ok := stepValue.(map[string]any)
		if !ok {
			continue
		}
		uses, _ := step["uses"].(string)
		if uses == "actions/attest-build-provenance@v3" {
			attestIndex = index
			subjectPath, _ := mapValue(step, "with")["subject-path"].(string)
			for _, required := range []string{"*.tar.gz", "*.zip", "SHA256SUMS", "release-manifest.json"} {
				if !strings.Contains(subjectPath, required) {
					t.Errorf("来源证明的 subject-path 缺少 %q", required)
				}
			}
		}
		run, _ := step["run"].(string)
		if strings.Contains(run, "gh attestation verify") {
			verifyIndex = index
		}
	}
	if attestIndex < 0 {
		t.Fatal("发布 Job 必须生成 GitHub OIDC 来源证明")
	}
	if verifyIndex < 0 {
		t.Fatal("发布 Job 必须用 gh attestation verify 核验来源证明")
	}
	if verifyIndex < attestIndex {
		t.Error("来源证明核验必须发生在生成之后")
	}
	// 证明必须在公开之前核验：草稿转公开是最后一步。
	if publicIndex := stepIndex(t, workflow, "publish", "发布正式 Release"); verifyIndex > publicIndex {
		t.Error("来源证明核验必须早于公开 Release")
	}
}

// TestPublishWorkflowAssetWiring 校验制品名只有一个来源，构建与下载不会对不上。
func TestPublishWorkflowAssetWiring(t *testing.T) {
	t.Parallel()
	releaseText := readWorkflowText(t, releaseWorkflowPath)
	if !strings.Contains(releaseText, `echo "artifact_name=agentflow-release-$version"`) {
		t.Error("制品名必须由构建工作流的 resolve 步骤生成")
	}
	releaseWorkflow := loadWorkflow(t, releaseWorkflowPath)
	var uploadWith map[string]any
	for _, stepValue := range jobSteps(t, releaseWorkflow, "build") {
		step, _ := stepValue.(map[string]any)
		if name, _ := step["name"].(string); !strings.Contains(name, "上传发布制品") {
			continue
		}
		uploadWith = mapValue(step, "with")
	}
	if uploadWith == nil || uploadWith["name"] != "${{ needs.resolve.outputs.artifact_name }}" {
		t.Errorf("上传制品的名称必须来自 resolve 输出，实际 %v", uploadWith)
	}
	if uploadWith["if-no-files-found"] != "error" {
		t.Error("上传制品必须设置 if-no-files-found: error")
	}

	publishWorkflow := loadWorkflow(t, publishWorkflowPath)
	downloadWith := map[string]any(nil)
	for _, stepValue := range jobSteps(t, publishWorkflow, "publish") {
		step, _ := stepValue.(map[string]any)
		if name, _ := step["name"].(string); !strings.Contains(name, "下载发布制品") {
			continue
		}
		downloadWith = mapValue(step, "with")
	}
	if downloadWith == nil || downloadWith["name"] != "${{ needs.build.outputs.artifact_name }}" {
		t.Errorf("发布必须下载构建工作流产出的同一份制品，实际 %v", downloadWith)
	}
}
