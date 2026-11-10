package testrunner

import (
	"fmt"
	"strings"
)

// 真实依赖授权模式：auto 复用 CI 环境变量，on/off 由调用方显式指定。
const (
	RealTestsAuto = "auto"
	RealTestsOn   = "on"
	RealTestsOff  = "off"
)

// 与 tests/internal/testkit 保持同一组环境变量，避免测试侧出现第二套门禁。
const (
	// CIMarkerEnv 是 CI 平台自动注入的标记。
	CIMarkerEnv = "GITHUB_ACTIONS"
	// RealTestsGrantEnv 是授权判定步骤给出的授权标记，只有 "true" 表示授权。
	RealTestsGrantEnv = "AGENTFLOW_REAL_TESTS_GRANTED"
	// RealTestsGrantReasonEnv 是授权判定步骤给出的原因，用于说明跳过原因。
	RealTestsGrantReasonEnv = "AGENTFLOW_REAL_TESTS_GRANT_REASON"
)

// RealTestsDecision 是编排进程对真实依赖授权的一次判定结果。
//
// 判定只发生一次，结论随 worker 请求下发；用例通过 Context 读取，不再自行读取环境变量。
type RealTestsDecision struct {
	Allowed bool   `json:"allowed"`
	Mode    string `json:"mode"`
	Reason  string `json:"reason"`
}

// DecideRealTests 依据授权模式与 CI 环境变量给出授权结论。
//
// auto 在 CI 中要求授权标记精确等于 "true"，本机运行环境直接放行（真实测试仍由
// tests/config/*.yml 的 enable 开关决定）；on 表示调用方已完成授权判定；off 一律拒绝。
func DecideRealTests(mode, ciMarker, grant, grantReason string) (RealTestsDecision, error) {
	switch mode {
	case RealTestsOn:
		return RealTestsDecision{Allowed: true, Mode: mode, Reason: "调用方显式指定 --real-tests=on"}, nil
	case RealTestsOff:
		return RealTestsDecision{Allowed: false, Mode: mode, Reason: "调用方显式指定 --real-tests=off"}, nil
	case RealTestsAuto, "":
		if strings.TrimSpace(ciMarker) != "true" {
			return RealTestsDecision{
				Allowed: true,
				Mode:    RealTestsAuto,
				Reason:  "本机运行环境不使用 CI 授权门禁",
			}, nil
		}
		if strings.TrimSpace(grant) == "true" {
			return RealTestsDecision{
				Allowed: true,
				Mode:    RealTestsAuto,
				Reason:  "CI 授权判定通过",
			}, nil
		}
		reason := "CI 授权判定未放行"
		if detail := singleLine(grantReason); detail != "" {
			reason += "：" + detail
		}
		return RealTestsDecision{Allowed: false, Mode: RealTestsAuto, Reason: reason}, nil
	default:
		return RealTestsDecision{}, fmt.Errorf("未知的真实依赖授权模式 %q，只接受 auto/on/off", mode)
	}
}

// singleLine 把多行说明压成单行，避免判定原因破坏报告结构。
func singleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
