// Package testkit 提供 AgentFlow 测试统一使用的工作空间与环境辅助组件。
//
// 该文件提供容器内文件存在性探针的判定规则：远程断言必须区分"确定的退出码"与
// "无法确认的执行结果"，不得把连接中断、超时或探针自身失败当成文件不存在。
package testkit

import "fmt"

// FileAbsenceVerdict 是容器内文件存在性探针的判定结论。
type FileAbsenceVerdict string

// 探针可以给出的确定结论。
const (
	// FileConfirmedAbsent 表示 `test -e` 以退出码 1 确认目标不存在。
	FileConfirmedAbsent FileAbsenceVerdict = "absent"
	// FileConfirmedPresent 表示 `test -e` 以退出码 0 确认目标存在。
	FileConfirmedPresent FileAbsenceVerdict = "present"
)

// FileAbsenceProbe 是一次容器内 `test -e <path>` 探针的执行事实。
//
// 它只描述已观察到的事实，判定规则集中在 ClassifyFileAbsence：调用方不需要也不
// 应该各自解释退出码，否则同一条退出码在不同用例里会代表不同含义。
type FileAbsenceProbe struct {
	// ExitCode 是远程命令退出码，未能取得时为 -1。
	ExitCode int
	// Started 表示探针命令确实被提交到容器内执行。
	Started bool
	// Certain 表示可以确认容器内命令已经结束。
	Certain bool
	// Determined 表示这次执行按退出码结束，而不是连接中断、超时、取消或前置校验失败。
	Determined bool
}

// ClassifyFileAbsence 依据探针事实判定目标文件状态。
//
// 判定规则：
//   - 只有探针确实执行、确认结束并按退出码返回时才给出结论；
//   - 退出码 1 是唯一被接受的"不存在"证据；
//   - 退出码 0 表示目标存在；
//   - 退出码 2、127、-1 及其他取值都不是 `test -e` 的确定结论，按错误上报。
func ClassifyFileAbsence(probe FileAbsenceProbe) (FileAbsenceVerdict, error) {
	if !probe.Started {
		return "", fmt.Errorf(
			"文件存在性探针未确认已在容器内执行: exit=%d certain=%t determined=%t",
			probe.ExitCode,
			probe.Certain,
			probe.Determined,
		)
	}
	if !probe.Certain {
		return "", fmt.Errorf(
			"文件存在性探针未能确认结束（连接中断、超时或取消）: exit=%d",
			probe.ExitCode,
		)
	}
	if !probe.Determined {
		return "", fmt.Errorf(
			"文件存在性探针不是按退出码结束，结果不可用于判定文件状态: exit=%d",
			probe.ExitCode,
		)
	}
	switch probe.ExitCode {
	case 1:
		return FileConfirmedAbsent, nil
	case 0:
		return FileConfirmedPresent, nil
	default:
		return "", fmt.Errorf(
			"文件存在性探针退出码 %d 不是 test -e 的确定结论（只接受 0 与 1）",
			probe.ExitCode,
		)
	}
}
