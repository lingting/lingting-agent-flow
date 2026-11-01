package testkit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/tests/internal/testkit"
)

// TestRealTestsGateDecision 校验真实测试 CI 授权门禁的判定矩阵。
func TestRealTestsGateDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		ciMarker string
		grant    string
		want     bool
	}{
		{name: "本机运行未授权标记也放行", ciMarker: "", grant: "", want: true},
		{name: "本机运行忽略授权标记", ciMarker: "", grant: "false", want: true},
		{name: "CI 缺少授权标记", ciMarker: "true", grant: "", want: false},
		{name: "CI 授权标记为 true", ciMarker: "true", grant: "true", want: true},
		{name: "CI 授权标记带空白", ciMarker: "true", grant: " true ", want: true},
		{name: "CI 授权标记为 TRUE", ciMarker: "true", grant: "TRUE", want: false},
		{name: "CI 授权标记为 1", ciMarker: "true", grant: "1", want: false},
		{name: "CI 授权标记为 false", ciMarker: "true", grant: "false", want: false},
		{name: "CI 标记为 false 视为本机", ciMarker: "false", grant: "", want: true},
		{name: "CI 标记为其他值视为本机", ciMarker: "yes", grant: "", want: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			allowed, reason := testkit.RealTestsGateDecision(testCase.ciMarker, testCase.grant)
			if allowed != testCase.want {
				t.Fatalf("allowed=%v（原因 %s），期望 %v", allowed, reason, testCase.want)
			}
			if reason == "" {
				t.Fatal("门禁结论必须带原因")
			}
			if strings.ContainsAny(reason, "\r\n") {
				t.Fatalf("门禁原因必须单行: %q", reason)
			}
		})
	}
}

// TestRealTestsAllowedWithoutCIMarker 校验本机进程不受 CI 授权门禁影响。
//
// 测试进程没有 GITHUB_ACTIONS 标记时，真实测试是否执行仍由 tests/config/*.yml 的
// enable 开关决定，本轮 CI 改动不会改变本机能力。CI 内运行时该断言没有意义
// （授权标记由工作流注入），因此明确跳过而不是失败。
func TestRealTestsAllowedWithoutCIMarker(t *testing.T) {
	if strings.TrimSpace(os.Getenv(testkit.CIMarkerEnv)) == "true" {
		t.Skip("WARN [SKIP] 当前进程运行在 GitHub Actions 中，本机语义由 TestRealTestsGateDecision 覆盖")
	}
	allowed, reason := testkit.RealTestsAllowed()
	if !allowed {
		t.Fatalf("本机测试进程不应被 CI 授权门禁拦下: %s", reason)
	}
}

// realEntryFiles 是全部真实外部依赖测试的入口文件。
//
// 这些文件里的启用判定函数必须在读取配置与建立连接之前调用 testkit.RequireRealTests，
// 否则新增用例就可能绕过 CI 授权门禁直接访问真实设备。
var realEntryFiles = []string{
	"tests/integration/pi/env.go",
	"tests/integration/ssh/env.go",
	"tests/integration/remote/agent/agent_test.go",
	"tests/integration/remote/codesaver/codesaver_test.go",
	"tests/integration/remote/docker/docker_test.go",
	"tests/integration/remote/workflow/workflow_test.go",
	"tests/integration/remote/workflow/e2e_env_test.go",
}

// TestRealEntryPointsUseGate 校验每个真实测试入口都使用统一的 CI 授权门禁。
func TestRealEntryPointsUseGate(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, relative := range realEntryFiles {
		path := filepath.Join(root, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取真实测试入口 %s 失败: %v", relative, err)
		}
		text := string(data)
		if !strings.Contains(text, "testkit.RequireRealTests(t, ") {
			t.Errorf("%s 未在建立连接之前调用 testkit.RequireRealTests", relative)
			continue
		}
		// 门禁必须在启用判定函数的最前面：配置读取（Load*Config）之前。
		gate := strings.Index(text, "testkit.RequireRealTests(t, ")
		config := strings.Index(text, "testkit.Load")
		if config >= 0 && gate > config {
			t.Errorf("%s 的授权门禁出现在读取配置之后", relative)
		}
	}
}

// repositoryRoot 返回仓库根目录，找不到 go.mod 时结束用例。
func repositoryRoot(t *testing.T) string {
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
			t.Fatalf("未找到仓库根目录: %s", dir)
		}
		dir = parent
	}
}
