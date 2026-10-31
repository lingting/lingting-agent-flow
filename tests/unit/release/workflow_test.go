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
const (
	ciWorkflowPath      = ".github/workflows/ci.yml"
	releaseWorkflowPath = ".github/workflows/release.yml"
)

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
var allowedExpressionContexts = []string{
	"github", "inputs", "matrix", "needs", "steps", "env", "strategy", "job", "runner", "vars",
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

// TestReleaseWorkflowTriggers 校验发布工作流只在版本标签与手动触发时运行。
func TestReleaseWorkflowTriggers(t *testing.T) {
	t.Parallel()
	workflow := loadWorkflow(t, releaseWorkflowPath)
	trigger := triggerBlock(t, workflow)
	tags := listValue(trigger, "push", "tags")
	if len(tags) != 1 || tags[0] != "v*.*.*" {
		t.Fatalf("发布工作流必须由版本标签 v*.*.* 触发，实际: %v", tags)
	}
	if mapValue(trigger, "workflow_dispatch") == nil {
		t.Fatal("发布工作流必须支持 workflow_dispatch 手动触发")
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

// TestWorkflowActionsPinned 校验 CI 与发布工作流中的 Action 都用提交 SHA 固定。
func TestWorkflowActionsPinned(t *testing.T) {
	t.Parallel()
	for _, relative := range []string{ciWorkflowPath, releaseWorkflowPath} {
		workflow := loadWorkflow(t, relative)
		uses := useStatements(workflow)
		if len(uses) == 0 {
			t.Fatalf("%s 未解析到任何 Action 引用", relative)
		}
		for _, reference := range uses {
			if !pinnedActionPattern.MatchString(reference) {
				t.Errorf("%s 的 Action %q 未用提交 SHA 固定", relative, reference)
			}
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
	for _, relative := range []string{ciWorkflowPath, releaseWorkflowPath} {
		raw, err := os.ReadFile(filepath.Join(repoRootPath(t), relative))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", relative, err)
		}
		text := string(raw)
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
