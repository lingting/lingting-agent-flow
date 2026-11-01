package testkit

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// 真实测试的 CI 授权门禁使用的环境变量名。
//
// 只有 GitHub Actions 的授权判定步骤会写入前两个变量，它们由工作流内部的资格判断
// 产生，不是第二个需要用户维护的开关；用户唯一需要配置的仍然只有
// AGENTFLOW_REAL_TESTS（见 internal/cigate）。
const (
	// CIMarkerEnv 是 GitHub Actions 自动注入的 CI 标记。
	//
	// 未设置时按"本机开发环境"处理：真实测试继续由 tests/config/*.yml 的 enable
	// 开关决定是否执行，本机能力不因 CI 改动而变化。
	CIMarkerEnv = "GITHUB_ACTIONS"
	// RealTestsGrantEnv 是授权判定步骤给出的真实测试授权标记，只有 "true" 表示授权。
	RealTestsGrantEnv = "AGENTFLOW_REAL_TESTS_GRANTED"
	// RealTestsGrantReasonEnv 是授权判定步骤给出的判定原因，用于说明跳过原因。
	RealTestsGrantReasonEnv = "AGENTFLOW_REAL_TESTS_GRANT_REASON"
)

// RequireRealTests 在真实测试未获 CI 授权时以 SKIP 结束用例。
//
// 调用位置必须在读取真实测试配置、建立外部连接与执行任何外部命令之前：未授权时
// 用例不得触碰真实设备。本机开发环境（没有 GITHUB_ACTIONS 标记）不受影响，仍由
// tests/config/*.yml 的 enable 开关决定是否执行；CI 中缺少授权信息一律按关闭处理。
func RequireRealTests(t *testing.T, suite string) {
	t.Helper()
	allowed, reason := RealTestsGateDecision(
		os.Getenv(CIMarkerEnv),
		os.Getenv(RealTestsGrantEnv),
	)
	if allowed {
		return
	}
	skip := fmt.Sprintf(
		"WARN [SKIP] 真实测试未获 CI 授权：%s；%s；%s",
		suite,
		reason,
		grantReasonText(os.Getenv(RealTestsGrantReasonEnv)),
	)
	fmt.Fprintln(os.Stdout, skip)
	t.Skip(skip)
}

// RealTestsAllowed 报告当前进程是否允许执行真实外部依赖测试。
//
// 供需要自行决定跳过时机的用例使用；一般用例应直接调用 RequireRealTests。
func RealTestsAllowed() (bool, string) {
	return RealTestsGateDecision(os.Getenv(CIMarkerEnv), os.Getenv(RealTestsGrantEnv))
}

// RealTestsGateDecision 依据 CI 标记与授权标记给出结论，不读取其他环境变量。
//
// CI 中只有授权标记精确等于 "true" 才放行：空值、TRUE、1 等其他写法一律按关闭处理，
// 避免把拼写差异当成授权。没有 CI 标记时按本机开发环境放行：真实测试仍由
// tests/config/*.yml 的 enable 开关决定。
func RealTestsGateDecision(ciMarker, grant string) (bool, string) {
	if !isCIRun(ciMarker) {
		return true, "本机运行环境不使用 CI 授权门禁"
	}
	if strings.TrimSpace(grant) == "true" {
		return true, "CI 授权判定通过"
	}
	return false, "CI 授权判定未放行"
}

// isCIRun 判断当前进程是否运行在 GitHub Actions 中。
func isCIRun(ciMarker string) bool {
	return strings.TrimSpace(ciMarker) == "true"
}

// grantReasonText 把工作流给出的判定原因整理成单行文本。
func grantReasonText(reason string) string {
	cleaned := strings.Join(strings.Fields(reason), " ")
	if cleaned == "" {
		return fmt.Sprintf("判定原因未提供（%s）", RealTestsGrantReasonEnv)
	}
	return "判定原因：" + cleaned
}
