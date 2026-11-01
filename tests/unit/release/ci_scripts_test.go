package release_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// ciScriptsDir 是 GitHub Actions 专用脚本目录（相对仓库根目录）。
//
// 工作流只负责声明与调度：所有执行逻辑都放在这里，因此它的文件清单、脚本契约与
// 工作流引用必须能被静态核验，而不是靠人眼看 YAML。
const ciScriptsDir = ".github/scripts"

// ciScriptReferencePattern 匹配工作流里对脚本的相对路径引用。
var ciScriptReferencePattern = regexp.MustCompile(`\.github/scripts/[A-Za-z0-9_.-]+\.(?:sh|py)`)

// ciScriptFlagPattern 匹配工作流调用脚本时传入的长参数。
var ciScriptFlagPattern = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// inlineProgramMarkers 是"YAML 内嵌程序"的特征片段。
//
// 工作流步骤里只允许出现脚本调用或单行工具调用；一旦出现这些片段，说明有逻辑
// 没有外置到 .github/scripts。
var inlineProgramMarkers = []string{
	"set -", "<<", "$(", "|", "tee ",
}

// shellControlKeywords 是"YAML 里正在写 Shell 程序"的整行形态。
//
// 只按行首关键字判断，避免把 pnpm 的 --frozen-lockfile 这类参数误判成程序。
var shellControlKeywords = []string{
	"if", "then", "elif", "else", "fi",
	"for", "while", "do", "done", "case", "esac",
}

// runStep 是工作流中的一个 run 步骤。
type runStep struct {
	workflow string
	name     string
	text     string
}

// readScriptText 读取 .github/scripts 下的脚本原文。
func readScriptText(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRootPath(t), ciScriptsDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取脚本 %s 失败: %v", path, err)
	}
	return string(data)
}

// ciScriptNames 返回 .github/scripts 下的全部脚本文件名。
func ciScriptNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoRootPath(t), ciScriptsDir))
	if err != nil {
		t.Fatalf("读取脚本目录失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

// workflowRunSteps 收集全部工作流中的 run 步骤。
func workflowRunSteps(t *testing.T) []runStep {
	t.Helper()
	steps := make([]runStep, 0, 32)
	for _, relative := range workflowPaths() {
		workflow := loadWorkflow(t, relative)
		jobs := mapValue(workflow, "jobs")
		for jobName, jobValue := range jobs {
			for _, stepValue := range listValue(jobValue, "steps") {
				step, ok := stepValue.(map[string]any)
				if !ok {
					continue
				}
				text, ok := step["run"].(string)
				if !ok || strings.TrimSpace(text) == "" {
					continue
				}
				name, _ := step["name"].(string)
				steps = append(steps, runStep{
					workflow: relative + "/" + jobName,
					name:     name,
					text:     strings.TrimSpace(text),
				})
			}
		}
	}
	if len(steps) == 0 {
		t.Fatal("工作流中没有解析到任何 run 步骤")
	}
	return steps
}

// invokesWithInterpreter 判断步骤是否用显式解释器调用了给定脚本。
//
// 允许 `bash .github/scripts/x.sh` 与 `bash "$GITHUB_WORKSPACE/.github/scripts/x.sh"`
// 两种写法：前者用于工作目录已在仓库根目录的步骤，后者用于声明了其他
// working-directory 的步骤。
func invokesWithInterpreter(text, reference string) bool {
	for _, prefix := range []string{
		"bash ",
		"python3 ",
		`bash "$GITHUB_WORKSPACE/`,
		`python3 "$GITHUB_WORKSPACE/`,
	} {
		if strings.Contains(text, prefix+reference) {
			return true
		}
	}
	return false
}

// TestCIScriptReferencesExist 校验工作流引用的每个脚本都真实存在且用显式解释器调用。
func TestCIScriptReferencesExist(t *testing.T) {
	t.Parallel()
	root := repoRootPath(t)
	seen := make(map[string]bool)
	for _, step := range workflowRunSteps(t) {
		for _, reference := range ciScriptReferencePattern.FindAllString(step.text, -1) {
			seen[reference] = true
			path := filepath.Join(root, filepath.FromSlash(reference))
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s 的步骤 %q 引用了不存在的脚本 %s", step.workflow, step.name, reference)
			}
			// 不依赖可执行位：CI 统一用 bash / python3 显式调用（含 $GITHUB_WORKSPACE 绝对路径形式）。
			if !invokesWithInterpreter(step.text, reference) {
				t.Errorf("%s 的步骤 %q 必须用 bash / python3 显式调用 %s，实际: %s",
					step.workflow, step.name, reference, step.text)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("工作流中没有引用任何 .github/scripts 脚本")
	}
}

// TestCIScriptsAllReferenced 校验没有未被任何工作流引用的脚本。
func TestCIScriptsAllReferenced(t *testing.T) {
	t.Parallel()
	referenced := make(map[string]bool)
	for _, step := range workflowRunSteps(t) {
		for _, reference := range ciScriptReferencePattern.FindAllString(step.text, -1) {
			referenced[filepath.Base(reference)] = true
		}
	}
	for _, name := range ciScriptNames(t) {
		if !referenced[name] {
			t.Errorf("脚本 %s 没有被任何工作流引用", name)
		}
	}
}

// TestCIScriptsStrictModeAndDocs 校验 Shell 脚本的写法与文档头部契约。
func TestCIScriptsStrictModeAndDocs(t *testing.T) {
	t.Parallel()
	for _, name := range ciScriptNames(t) {
		text := readScriptText(t, name)
		if strings.HasSuffix(name, ".sh") {
			if !strings.HasPrefix(text, "#!/usr/bin/env bash\n") {
				t.Errorf("%s 必须以 #!/usr/bin/env bash 开头", name)
			}
			if !strings.Contains(text, "set -euo pipefail") {
				t.Errorf("%s 必须启用 set -euo pipefail", name)
			}
			if strings.Contains(text, "eval ") {
				t.Errorf("%s 不得使用 eval 执行外部输入", name)
			}
			// 脚本必须自报用途、参数、退出码与副作用，不能靠文件名代替契约。
			for _, section := range []string{"# 用途", "# 参数", "# 退出码", "# 副作用"} {
				if !strings.Contains(text, section) {
					t.Errorf("%s 的头部注释缺少 %q 小节", name, section)
				}
			}
			// 凡是切换工作目录的脚本都必须按自身位置解析仓库根目录，
			// 不能依赖调用步骤碰巧在仓库根目录。
			if strings.Contains(text, "\ncd ") && !strings.Contains(text, "BASH_SOURCE") {
				t.Errorf("%s 使用了 cd 但没有按脚本自身位置解析仓库根目录", name)
			}
		} else if strings.HasSuffix(name, ".py") {
			if !strings.HasPrefix(text, "#!/usr/bin/env python3\n") {
				t.Errorf("%s 必须以 #!/usr/bin/env python3 开头", name)
			}
		}
	}
}

// TestCIScriptFlagsDeclared 校验工作流传入的每个参数都出现在被调用脚本里。
//
// 它拦住两类回归：YAML 写了脚本不认识的参数（拼错、改名后漏改），以及脚本改了
// 参数名却没有同步工作流。
func TestCIScriptFlagsDeclared(t *testing.T) {
	t.Parallel()
	for _, step := range workflowRunSteps(t) {
		for _, reference := range ciScriptReferencePattern.FindAllString(step.text, -1) {
			name := filepath.Base(reference)
			scriptText := readScriptText(t, name)
			for _, flag := range ciScriptFlagPattern.FindAllString(step.text, -1) {
				if !strings.Contains(scriptText, flag) {
					t.Errorf("%s 的步骤 %q 向 %s 传入了脚本未声明的参数 %s",
						step.workflow, step.name, name, flag)
				}
			}
		}
	}
}

// TestWorkflowHasNoInlinePrograms 校验 YAML 里不再保留内嵌程序。
//
// 规则：run 步骤要么调用 .github/scripts 下的脚本，要么是单行的工具调用；
// 不允许出现控制流、管道或 heredoc 这类"写在 YAML 里的程序"。
func TestWorkflowHasNoInlinePrograms(t *testing.T) {
	t.Parallel()
	for _, step := range workflowRunSteps(t) {
		callsScript := ciScriptReferencePattern.MatchString(step.text)
		for _, marker := range inlineProgramMarkers {
			if strings.Contains(step.text, marker) {
				t.Errorf("%s 的步骤 %q 含有内嵌程序片段 %q：%s",
					step.workflow, step.name, marker, step.text)
			}
		}
		for _, keyword := range shellControlKeywords {
			for _, line := range strings.Split(step.text, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), keyword+" ") ||
					strings.TrimSpace(line) == keyword {
					t.Errorf("%s 的步骤 %q 含有内嵌 Shell 控制流 %q：%s",
						step.workflow, step.name, keyword, step.text)
					break
				}
			}
		}
		if callsScript {
			continue
		}
		if strings.Contains(step.text, "\n") {
			t.Errorf("%s 的步骤 %q 不是脚本调用却是多行命令，应把逻辑移到 %s：%s",
				step.workflow, step.name, ciScriptsDir, step.text)
		}
	}
}

// TestCIScriptsUseLF 校验脚本使用 LF 行尾，与仓库统一约定一致。
func TestCIScriptsUseLF(t *testing.T) {
	t.Parallel()
	for _, name := range ciScriptNames(t) {
		path := filepath.Join(repoRootPath(t), ciScriptsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取脚本 %s 失败: %v", path, err)
		}
		if strings.Contains(string(data), "\r") {
			t.Errorf("%s 含有 CR，必须整体使用 LF 行尾", name)
		}
	}
}

// TestAuthorizationScriptPassesContextAsArguments 校验授权脚本的输入输出契约。
//
// 它只做静态断言：具体判定规则由 tests/unit/cigate 覆盖，这里确保脚本把每个
// 事件上下文作为独立 argv 传给判定工具，且不把变量拼进命令行字符串。
func TestAuthorizationScriptPassesContextAsArguments(t *testing.T) {
	t.Parallel()
	text := readScriptText(t, "check-real-test-authorization.sh")
	pairs := map[string]string{
		"-mode":               "AGENTFLOW_REAL_TESTS",
		"-event":              "GITHUB_EVENT_NAME",
		"-actor":              "GITHUB_ACTOR",
		"-repository-owner":   "REPOSITORY_OWNER",
		"-pr-author":          "PR_AUTHOR",
		"-author-association": "AUTHOR_ASSOCIATION",
		"-repository":         "GITHUB_REPOSITORY",
	}
	for flag, variable := range pairs {
		expected := flag + ` "${` + variable + `:-}"`
		if !strings.Contains(text, expected) {
			t.Errorf("授权脚本必须把 %s 以 %s 的形式传入判定工具", variable, expected)
		}
	}
	if !strings.Contains(text, "real-tests-gate") {
		t.Error("授权脚本必须调用 cmd/agentflow-ci 的 real-tests-gate 子命令")
	}
	// 判定规则只在 internal/cigate 实现：脚本里不得出现身份比较或关联取值判断。
	for _, forbidden := range []string{
		`"OWNER"`, `"MEMBER"`, `"COLLABORATOR"`, `"CONTRIBUTOR"`, "author_association ==",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("授权脚本不得自行实现身份判断（出现 %q），规则应留在 internal/cigate", forbidden)
		}
	}
}

// scriptEnv 返回执行脚本用的环境：保留宿主环境，但清掉 CI 注入的输出与授权变量，
// 避免测试把摘要写进真实 Job 摘要文件，或继承上一次运行的授权标记。
func scriptEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key := entry
		if index := strings.Index(entry, "="); index >= 0 {
			key = entry[:index]
		}
		if strings.HasPrefix(key, "GITHUB_") || strings.HasPrefix(key, "RUNNER_") ||
			key == "AGENTFLOW_REAL_TESTS" || key == "AGENTFLOW_REAL_TESTS_GRANTED" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}

// runCIScript 执行 .github/scripts 下的脚本并返回退出码与合并输出。
func runCIScript(t *testing.T, name string, env []string, args ...string) (int, string) {
	t.Helper()
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("WARN [SKIP] 未找到 bash，跳过 CI 脚本行为测试")
	}
	scriptPath := filepath.ToSlash(filepath.Join(repoRootPath(t), ciScriptsDir, name))
	cmd := exec.Command(bashPath, append([]string{scriptPath}, args...)...)
	cmd.Dir = repoRootPath(t)
	cmd.Env = scriptEnv(env...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("执行脚本 %s 失败: %v", name, err)
	}
	return exitErr.ExitCode(), string(output)
}

// TestResolveReleaseVersionScript 覆盖版本解析脚本的输出与拒绝路径。
func TestResolveReleaseVersionScript(t *testing.T) {
	t.Parallel()
	commit := strings.Repeat("a", 40)

	t.Run("标签解析成功", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		outputPath := filepath.ToSlash(filepath.Join(dir, "output.txt"))
		summaryPath := filepath.ToSlash(filepath.Join(dir, "summary.md"))
		code, stdout := runCIScript(t, "resolve-release-version.sh", []string{
			"REF_TYPE=tag",
			"REF_NAME=v1.2.3",
			"COMMIT_SHA=" + commit,
			"GITHUB_OUTPUT=" + outputPath,
			"GITHUB_STEP_SUMMARY=" + summaryPath,
		})
		if code != 0 {
			t.Fatalf("解析 v1.2.3 应当成功，实际退出码 %d：%s", code, stdout)
		}
		output := readTestFile(t, outputPath, true)
		for _, expected := range []string{
			"version=v1.2.3\n",
			"commit=" + commit + "\n",
			"artifact_name=agentflow-release-v1.2.3\n",
		} {
			if !strings.Contains(output, expected) {
				t.Errorf("GITHUB_OUTPUT 缺少 %q，实际:\n%s", expected, output)
			}
		}
		if summary := readTestFile(t, summaryPath, true); !strings.Contains(summary, "发布版本") {
			t.Errorf("Job 摘要应包含版本小节，实际:\n%s", summary)
		}
	})

	t.Run("分支引用且要求标签", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		outputPath := filepath.ToSlash(filepath.Join(dir, "output.txt"))
		code, stdout := runCIScript(t, "resolve-release-version.sh",
			[]string{"REF_TYPE=branch", "REF_NAME=master", "COMMIT_SHA=" + commit,
				"GITHUB_OUTPUT=" + outputPath},
			"--require-tag")
		if code != 1 {
			t.Fatalf("分支引用必须失败，实际退出码 %d：%s", code, stdout)
		}
		if _, err := os.Stat(outputPath); err == nil {
			t.Error("失败时不得写出 GITHUB_OUTPUT")
		}
	})

	t.Run("版本格式错误", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "resolve-release-version.sh", []string{
			"REF_TYPE=tag", "REF_NAME=v1.2", "COMMIT_SHA=" + commit,
		})
		if code != 1 {
			t.Fatalf("非 vMAJOR.MINOR.PATCH 的标签必须失败，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("提交号不是完整 SHA", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "resolve-release-version.sh", []string{
			"REF_TYPE=tag", "REF_NAME=v1.2.3", "COMMIT_SHA=abc123",
		})
		if code != 1 {
			t.Fatalf("短提交号必须失败，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("手动输入与标签不一致", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "resolve-release-version.sh", []string{
			"REF_TYPE=tag", "REF_NAME=v1.2.3", "COMMIT_SHA=" + commit,
		}, "--input-version", "v1.2.4")
		if code != 1 {
			t.Fatalf("输入版本与标签不一致时必须失败，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("未知参数", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "resolve-release-version.sh", nil, "--unknown")
		if code != 2 {
			t.Fatalf("未知参数必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestSummarizeReleaseStagesScript 覆盖阶段汇总脚本的结论与参数校验。
func TestSummarizeReleaseStagesScript(t *testing.T) {
	t.Parallel()

	t.Run("全部成功", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "report.md"))
		summaryPath := filepath.ToSlash(filepath.Join(dir, "summary.md"))
		code, stdout := runCIScript(t, "summarize-release-stages.sh",
			[]string{"GITHUB_STEP_SUMMARY=" + summaryPath},
			"--title", "发布结论",
			"--stage", "标签校验=success",
			"--stage", "CI 门禁=success",
			"--version", "v1.2.3",
			"--commit", strings.Repeat("b", 40),
			"--report-path", reportPath)
		if code != 0 {
			t.Fatalf("全部阶段成功时应以 0 结束，实际退出码 %d：%s", code, stdout)
		}
		report := readTestFile(t, reportPath, true)
		for _, expected := range []string{"## 发布结论", "| 标签校验 | success |", "| CI 门禁 | success |"} {
			if !strings.Contains(report, expected) {
				t.Errorf("报告缺少 %q，实际:\n%s", expected, report)
			}
		}
		if summary := readTestFile(t, summaryPath, true); !strings.Contains(summary, "CI 门禁") {
			t.Errorf("Job 摘要应写入报告内容，实际:\n%s", summary)
		}
	})

	t.Run("被跳过的阶段不计为通过", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "report.md"))
		code, stdout := runCIScript(t, "summarize-release-stages.sh",
			nil,
			"--title", "发布结论",
			"--stage", "标签校验=success",
			"--stage", "CI 门禁=skipped",
			"--report-path", reportPath)
		if code != 1 {
			t.Fatalf("存在被跳过的阶段必须以 1 结束，实际退出码 %d：%s", code, stdout)
		}
		if report := readTestFile(t, reportPath, true); !strings.Contains(report, "skipped") {
			t.Errorf("报告应保留被跳过阶段的原始结论，实际:\n%s", report)
		}
	})

	t.Run("缺少标题", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "summarize-release-stages.sh", nil, "--stage", "a=success")
		if code != 2 {
			t.Fatalf("缺少 --title 必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("缺少阶段", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "summarize-release-stages.sh", nil, "--title", "结论")
		if code != 2 {
			t.Fatalf("缺少 --stage 必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("阶段格式非法", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "summarize-release-stages.sh",
			nil, "--title", "结论", "--stage", "没有等号")
		if code != 2 {
			t.Fatalf("阶段格式非法必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestSummarizeCIGateScript 覆盖 CI 门禁汇总脚本的结论矩阵。
// replaceEnv 按键覆盖环境变量条目：已存在则替换，未存在则追加。
func replaceEnv(base []string, updates ...string) []string {
	env := append([]string{}, base...)
	for _, update := range updates {
		key := update[:strings.Index(update, "=")+1]
		replaced := false
		for index, entry := range env {
			if strings.HasPrefix(entry, key) {
				env[index] = update
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, update)
		}
	}
	return env
}

func TestSummarizeCIGateScript(t *testing.T) {
	t.Parallel()

	successEnv := []string{
		"GO_STATIC_RESULT=success", "GO_STATIC_STEP=success",
		"GO_INVENTORY_RESULT=success", "GO_INVENTORY_STEP=success",
		"GO_INVENTORY_COVERAGE=passed", "GO_INVENTORY_PACKAGES=45",
		"GO_GROUPS_SUMMARY=| 分组 | 类型 |\n| --- | --- |\n| unit | 普通 CI |",
		"GO_UNIT_RESULT=success", "GO_UNIT_STEP=success",
		"GO_LOCAL_RESULT=success", "GO_LOCAL_STEP=success",
		"GO_E2E_RESULT=success", "GO_E2E_PREPARE=success", "GO_E2E_STEP=success",
		"GO_RACE_RESULT=success", "GO_RACE_STEP=success",
		"UI_TEST_RESULT=success", "UI_TEST_PREPARE=success", "UI_TEST_STEP=success",
		"REAL_GATE_RESULT=success", "REAL_GATE_MODE=", "REAL_GATE_ENABLED=false",
		"REAL_GATE_REASON=未设置", "REAL_TESTS_RESULT=skipped",
	}

	t.Run("真实测试未授权跳过不算失败", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "ci-summary.md"))
		code, stdout := runCIScript(t, "summarize-ci-gate.sh", successEnv, "--report-path", reportPath)
		if code != 0 {
			t.Fatalf("真实测试跳过不应让门禁失败，实际退出码 %d：%s", code, stdout)
		}
		report := readTestFile(t, reportPath, true)
		if !strings.Contains(report, "未执行：未获授权") {
			t.Errorf("报告必须标注真实测试未执行且不计为通过，实际:\n%s", report)
		}
	})

	t.Run("测试失败必须让门禁失败", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "ci-summary.md"))
		env := replaceEnv(successEnv, "GO_LOCAL_RESULT=failure", "GO_LOCAL_STEP=failure")
		code, stdout := runCIScript(t, "summarize-ci-gate.sh", env, "--report-path", reportPath)
		if code != 1 {
			t.Fatalf("测试失败必须以 1 结束，实际退出码 %d：%s", code, stdout)
		}
		if report := readTestFile(t, reportPath, true); !strings.Contains(report, "失败：测试步骤失败") {
			t.Errorf("报告应写明失败阶段，实际:\n%s", report)
		}
	})

	t.Run("真实测试执行后失败必须让门禁失败", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "ci-summary.md"))
		env := replaceEnv(successEnv, "REAL_TESTS_RESULT=failure")
		code, stdout := runCIScript(t, "summarize-ci-gate.sh", env, "--report-path", reportPath)
		if code != 1 {
			t.Fatalf("真实测试失败必须以 1 结束，实际退出码 %d：%s", code, stdout)
		}
	})
	t.Run("分组核验失败必须让门禁失败", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "ci-summary.md"))
		env := replaceEnv(successEnv,
			"GO_INVENTORY_RESULT=failure", "GO_INVENTORY_STEP=failure",
			"GO_UNIT_RESULT=skipped", "GO_UNIT_STEP=skipped",
			"GO_LOCAL_RESULT=skipped", "GO_LOCAL_STEP=skipped",
			"GO_E2E_RESULT=skipped", "GO_E2E_STEP=skipped",
		)
		code, stdout := runCIScript(t, "summarize-ci-gate.sh", env, "--report-path", reportPath)
		if code != 1 {
			t.Fatalf("分组核验失败必须以 1 结束，实际退出码 %d：%s", code, stdout)
		}
		report := readTestFile(t, reportPath, true)
		if !strings.Contains(report, "| go-inventory | failure |") {
			t.Errorf("报告应把分组核验失败列为失败 Job，实际:\n%s", report)
		}
		if !strings.Contains(report, "| go-unit | skipped |") {
			t.Errorf("报告应如实记录因分组核验失败而未执行的测试 Job，实际:\n%s", report)
		}
	})

	t.Run("分组清单进入报告", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "ci-summary.md"))
		code, stdout := runCIScript(t, "summarize-ci-gate.sh", successEnv, "--report-path", reportPath)
		if code != 0 {
			t.Fatalf("全部分组成功时应以 0 结束，实际退出码 %d：%s", code, stdout)
		}
		report := readTestFile(t, reportPath, true)
		for _, expected := range []string{
			"### Go 测试分组", "| go-unit | success |", "| unit | 普通 CI |", "测试包总数 45",
		} {
			if !strings.Contains(report, expected) {
				t.Errorf("报告缺少 %q，实际:\n%s", expected, report)
			}
		}
	})

	t.Run("缺少 Job 结论不计为通过", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		reportPath := filepath.ToSlash(filepath.Join(dir, "ci-summary.md"))
		code, stdout := runCIScript(t, "summarize-ci-gate.sh", nil, "--report-path", reportPath)
		if code != 1 {
			t.Fatalf("缺少结论必须以 1 结束，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestRunRealTestsScriptRequiresGrant 校验真实测试脚本在未授权时直接失败。
//
// 这是"跳过不得冒充通过"的最后一道防线：缺少授权标记时脚本必须在执行任何
// 外部命令之前失败，而不是跑出一堆 SKIP 后报告成功。
func TestRunRealTestsScriptRequiresGrant(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	code, stdout := runCIScript(t, "run-real-tests.sh",
		[]string{"AGENTFLOW_REAL_TESTS_GRANTED=false"},
		"--artifact-dir", filepath.ToSlash(filepath.Join(dir, "real")))
	if code != 1 {
		t.Fatalf("未授权的真实测试必须以 1 结束，实际退出码 %d：%s", code, stdout)
	}
	if !strings.Contains(stdout, "未获真实测试授权") {
		t.Errorf("失败原因必须可读，实际输出:\n%s", stdout)
	}

	t.Run("缺少产物目录", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "run-real-tests.sh",
			[]string{"AGENTFLOW_REAL_TESTS_GRANTED=true"})
		if code != 2 {
			t.Fatalf("缺少 --artifact-dir 必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestGoTestGroupsScript 覆盖分组清单脚本的核验与查询契约。
//
// 分组清单是普通 CI 与真实测试的唯一范围来源，因此它的正向结论、拒绝路径与
// GITHUB_OUTPUT 结构都要被机械约束，而不是只在 workflow YAML 里"看起来对"。
func TestGoTestGroupsScript(t *testing.T) {
	t.Parallel()

	t.Run("分组覆盖完整", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		outputPath := filepath.ToSlash(filepath.Join(dir, "output.txt"))
		code, stdout := runCIScript(t, "go-test-groups.sh",
			[]string{"GITHUB_OUTPUT=" + outputPath},
			"--check", "--declared-groups", "unit,local,e2e")
		if code != 0 {
			t.Fatalf("仓库当前分组应当完整覆盖全部测试包，实际退出码 %d：%s", code, stdout)
		}
		if !strings.Contains(stdout, "分组核验通过") {
			t.Errorf("核验通过时必须给出可读结论，实际输出:\n%s", stdout)
		}
		output := readTestFile(t, outputPath, true)
		for _, expected := range []string{
			"coverage=passed", "package_total=", "groups_markdown<<AGENTFLOW_GROUPS_MARKDOWN",
		} {
			if !strings.Contains(output, expected) {
				t.Errorf("GITHUB_OUTPUT 缺少 %q，实际:\n%s", expected, output)
			}
		}
	})

	t.Run("工作流漏掉分组必须失败", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "go-test-groups.sh",
			nil, "--check", "--declared-groups", "unit,local")
		if code != 1 {
			t.Fatalf("普通分组没有被工作流执行时必须失败，实际退出码 %d：%s", code, stdout)
		}
		if !strings.Contains(stdout, "不一致") {
			t.Errorf("失败原因必须可读，实际输出:\n%s", stdout)
		}
	})

	t.Run("真实分组不得进入普通 CI", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "go-test-groups.sh",
			nil, "--check", "--declared-groups", "unit,local,e2e,pi")
		if code != 1 {
			t.Fatalf("把真实测试分组声明为普通分组时必须失败，实际退出码 %d：%s", code, stdout)
		}
		if !strings.Contains(stdout, "真实外部依赖") {
			t.Errorf("失败原因必须说明分组类型，实际输出:\n%s", stdout)
		}
	})

	t.Run("未知分组以 2 拒绝", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "go-test-groups.sh", nil, "--packages", "nosuch")
		if code != 2 {
			t.Fatalf("未知分组必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("缺少操作以 2 拒绝", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "go-test-groups.sh", nil)
		if code != 2 {
			t.Fatalf("没有指定操作必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestRunGoGroupTestsScript 覆盖分组执行脚本的参数校验与分组归属守卫。
//
// 它不实际运行测试（各分组由 CI 与本地入口覆盖），只保证真实测试分组不会被
// 普通 CI 的入口执行、非法参数明确失败。
func TestRunGoGroupTestsScript(t *testing.T) {
	t.Parallel()

	t.Run("拒绝真实测试分组", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		code, stdout := runCIScript(t, "run-go-group-tests.sh",
			nil,
			"--group", "remote",
			"--artifact-dir", filepath.ToSlash(filepath.Join(dir, "artifacts")))
		if code != 1 {
			t.Fatalf("普通 CI 入口执行真实测试分组必须以 1 结束，实际退出码 %d：%s", code, stdout)
		}
		if !strings.Contains(stdout, "真实外部依赖") {
			t.Errorf("拒绝原因必须可读，实际输出:\n%s", stdout)
		}
	})

	t.Run("未知分组以 2 拒绝", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		code, stdout := runCIScript(t, "run-go-group-tests.sh",
			nil,
			"--group", "nosuch",
			"--artifact-dir", filepath.ToSlash(filepath.Join(dir, "artifacts")))
		if code != 2 {
			t.Fatalf("未知分组必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})

	t.Run("缺少参数以 2 拒绝", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "run-go-group-tests.sh", nil, "--group", "unit")
		if code != 2 {
			t.Fatalf("缺少 --artifact-dir 必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestCollectTestArtifactsScript 校验产物收集脚本的白名单与命名契约。
//
// 上传集合必须只包含诊断必需的文件：多带一个数据库或私钥都会把测试现场的敏感数据
// 送出 Runner，因此正反两面都要断言。
func TestCollectTestArtifactsScript(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	sourceDir := filepath.Join(dir, "unit")
	baseDir := filepath.Join(sourceDir, "lingting-agent-flow-tests")
	caseDir := filepath.Join(baseDir, "unit-agent-1234")
	files := map[string]string{
		filepath.Join(sourceDir, "unit-output.log"):         "测试输出",
		filepath.Join(baseDir, "latest-run.txt"):            "latest",
		filepath.Join(caseDir, "test.log"):                  "用例日志",
		filepath.Join(caseDir, "agentflow.db"):              "数据库",
		filepath.Join(caseDir, "sessions", "session.jsonl"): "会话",
		filepath.Join(caseDir, "keys", "id_ed25519"):        "私钥",
		filepath.Join(sourceDir, "node_modules", "a.js"):    "依赖",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("创建测试产物目录失败: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("写入测试产物失败: %v", err)
		}
	}

	outputPath := filepath.ToSlash(filepath.Join(dir, "output.txt"))
	stagingDir := filepath.Join(dir, "staging")
	code, stdout := runCIScript(t, "collect-test-artifacts.sh", []string{
		"GITHUB_OUTPUT=" + filepath.ToSlash(outputPath),
		"GITHUB_RUN_ID=1234",
		"GITHUB_RUN_ATTEMPT=2",
	},
		"--source-dir", filepath.ToSlash(sourceDir),
		"--job", "go-unit",
		"--staging-dir", filepath.ToSlash(stagingDir))
	if code != 0 {
		t.Fatalf("收集测试产物应当成功，实际退出码 %d：%s", code, stdout)
	}

	output := readTestFile(t, outputPath, true)
	for _, expected := range []string{
		"artifact-name=go-unit-1234-2", "file-count=3",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("GITHUB_OUTPUT 缺少 %q，实际:\n%s", expected, output)
		}
	}

	for _, relative := range []string{
		"unit-output.log",
		"lingting-agent-flow-tests/latest-run.txt",
		"lingting-agent-flow-tests/unit-agent-1234/test.log",
	} {
		if _, err := os.Stat(filepath.Join(stagingDir, filepath.FromSlash(relative))); err != nil {
			t.Errorf("暂存目录缺少白名单文件 %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"lingting-agent-flow-tests/unit-agent-1234/agentflow.db",
		"lingting-agent-flow-tests/unit-agent-1234/sessions/session.jsonl",
		"lingting-agent-flow-tests/unit-agent-1234/keys/id_ed25519",
		"node_modules/a.js",
	} {
		if _, err := os.Stat(filepath.Join(stagingDir, filepath.FromSlash(relative))); err == nil {
			t.Errorf("暂存目录不得包含非白名单文件 %s", relative)
		}
	}

	t.Run("缺少参数以 2 拒绝", func(t *testing.T) {
		t.Parallel()
		code, stdout := runCIScript(t, "collect-test-artifacts.sh", nil, "--job", "go-unit")
		if code != 2 {
			t.Fatalf("缺少 --source-dir 必须以 2 拒绝，实际退出码 %d：%s", code, stdout)
		}
	})
}

// TestAuthorizationScriptWithStubTool 用替身 go 命令校验授权脚本的参数映射。
//
// Windows 下 MSYS 不支持用 PATH 替身验证脚本参数，因此只在类 Unix 环境执行；
// 判定规则本身由 tests/unit/cigate 覆盖。
func TestAuthorizationScriptWithStubTool(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("WARN [SKIP] Windows 下无法用 PATH 替身校验 bash 脚本的参数映射")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("WARN [SKIP] 未找到 bash，跳过授权脚本参数映射测试")
	}
	dir := testkit.Dir(t)
	stubDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("创建替身目录失败: %v", err)
	}
	argsFile := filepath.Join(dir, "go-args.txt")
	stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	stubPath := filepath.Join(stubDir, "go")
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("写入替身命令失败: %v", err)
	}

	scriptPath := filepath.Join(repoRootPath(t), ciScriptsDir, "check-real-test-authorization.sh")
	cmd := exec.Command(bashPath, scriptPath)
	cmd.Dir = repoRootPath(t)
	cmd.Env = scriptEnv(
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AGENTFLOW_REAL_TESTS=collaborator",
		"GITHUB_EVENT_NAME=pull_request",
		"GITHUB_ACTOR=someone-else",
		"REPOSITORY_OWNER=owner",
		"PR_AUTHOR=pr-author",
		"AUTHOR_ASSOCIATION=CONTRIBUTOR",
		"GITHUB_REPOSITORY=owner/repo",
		"GITHUB_API_URL=https://api.example.invalid",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("授权脚本执行失败: %v\n%s", err, output)
	}
	recorded := readTestFile(t, argsFile, true)
	for _, expected := range []string{
		"run\n", "./cmd/agentflow-ci\n", "real-tests-gate\n",
		"-mode\ncollaborator\n",
		"-event\npull_request\n",
		"-actor\nsomeone-else\n",
		"-repository-owner\nowner\n",
		"-pr-author\npr-author\n",
		"-author-association\nCONTRIBUTOR\n",
		"-repository\nowner/repo\n",
	} {
		if !strings.Contains(recorded, expected) {
			t.Errorf("替身命令未收到期望参数 %q，实际记录:\n%s", expected, recorded)
		}
	}
}

// readTestFile 读取测试产物文件；required 为 false 时允许文件不存在。
func readTestFile(t *testing.T, path string, required bool) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if required {
			t.Fatalf("读取文件 %s 失败: %v", path, err)
		}
		return ""
	}
	return string(data)
}
