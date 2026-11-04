package docker

import "sort"

// 校验器集中定义的超时与限制。
//
// 这些值集中在本文件，不分散写入各个 Handler 或业务方法；后续调整只改这里。
const (
	// ValidationSSHTimeout 是 SSH 连接检查（含握手、认证与一条远程命令）的上限。
	ValidationSSHTimeoutSeconds = 10
	// ValidationVersionTimeout 是 Docker CLI、Engine 与 API 兼容性检查的上限。
	//
	// 该检查包含两条远程 CLI 命令与一次基准路径的 Engine API 探测，因此比单项 CLI 检查宽松。
	ValidationVersionTimeoutSeconds = 15
	// ValidationCLIHelpTimeout 是 Docker CLI 帮助信息检查的上限。
	ValidationCLIHelpTimeoutSeconds = 8
	// ValidationTotalTimeout 是一次完整校验（三项检查）的总时限。
	ValidationTotalTimeoutSeconds = 45
)

// ValidationTimeouts 汇总一次校验使用的超时设置。
type ValidationTimeouts struct {
	// SSH 是 SSH 连接检查的上限。
	SSHSeconds int
	// Version 是 Docker 版本与 API 兼容性检查的上限。
	VersionSeconds int
	// CLIHelp 是 Docker CLI 帮助信息检查的上限。
	CLIHelpSeconds int
	// Total 是整体校验的上限。
	TotalSeconds int
}

// DefaultValidationTimeouts 返回默认超时设置。
func DefaultValidationTimeouts() ValidationTimeouts {
	return ValidationTimeouts{
		SSHSeconds:     ValidationSSHTimeoutSeconds,
		VersionSeconds: ValidationVersionTimeoutSeconds,
		CLIHelpSeconds: ValidationCLIHelpTimeoutSeconds,
		TotalSeconds:   ValidationTotalTimeoutSeconds,
	}
}

// SortedMissingOptions 返回排序后的缺失选项列表，便于稳定输出与断言。
func SortedMissingOptions(options []string) []string {
	if len(options) == 0 {
		return nil
	}
	sorted := append([]string(nil), options...)
	sort.Strings(sorted)
	return sorted
}
