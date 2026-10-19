package docker

import "sort"

// 校验器集中定义的超时与限制。
//
// 这些值集中在本文件，不分散写入各个 Handler 或业务方法；后续调整只改这里。
const (
	// ValidationSSHTimeout 是 SSH 连接检查（含握手、认证与一条远程命令）的上限。
	ValidationSSHTimeoutSeconds = 10
	// ValidationImageTimeout 是单个镜像元数据查询的上限。
	ValidationImageTimeoutSeconds = 8
	// ValidationTotalTimeout 是一次完整校验（三项检查）的总时限。
	ValidationTotalTimeoutSeconds = 30
)

// ValidationTimeouts 汇总一次校验使用的超时设置。
type ValidationTimeouts struct {
	// SSH 是 SSH 连接检查的上限。
	SSHSeconds int
	// Image 是单镜像元数据查询的上限。
	ImageSeconds int
	// Total 是整体校验的上限。
	TotalSeconds int
}

// DefaultValidationTimeouts 返回默认超时设置。
func DefaultValidationTimeouts() ValidationTimeouts {
	return ValidationTimeouts{
		SSHSeconds:   ValidationSSHTimeoutSeconds,
		ImageSeconds: ValidationImageTimeoutSeconds,
		TotalSeconds: ValidationTotalTimeoutSeconds,
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
