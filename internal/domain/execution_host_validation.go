package domain

import (
	"fmt"
	"strings"
	"time"
)

// ValidationStatus 是执行节点环境预检的总体状态。
//
// 只使用三种取值，不新增第二种对外状态名；详细结果中的单项状态由 CheckStatus
// 表达，不引入额外的总体状态。
//
// normal 的含义仅限于本方案实际执行的检查项：SSH 连接可用、Docker CLI / Engine 与
// 固定 API 基准兼容、Docker CLI 帮助信息覆盖当前使用的命令与参数。预检既不查询
// 基础镜像 Registry 元数据，也不执行 Docker Build 或容器创建启动，因此 normal 不
// 代表基础镜像存在、环境镜像可构建，也不代表该 Engine 一定能成功运行所需的容器。
type ValidationStatus string

// 总体状态取值。
const (
	// ValidationStatusChecking 表示尚未完成当前配置版本的预检。
	ValidationStatusChecking ValidationStatus = "checking"
	// ValidationStatusNormal 表示当前版本的全部必需预检项均通过。
	ValidationStatusNormal ValidationStatus = "normal"
	// ValidationStatusAbnormal 表示至少一项必需检查失败，或无法完成必要检查。
	ValidationStatusAbnormal ValidationStatus = "abnormal"
)

// String 返回总体状态的字符串形式。
func (s ValidationStatus) String() string {
	return string(s)
}

// IsKnown 判断总体状态是否为本版本定义的取值。
//
// 读取层不强制限制取值：数据库不声明枚举白名单，未知取值必须原样保留，
// 由界面原样展示而不是被映射成某个已知状态。
func (s ValidationStatus) IsKnown() bool {
	switch s {
	case ValidationStatusChecking, ValidationStatusNormal, ValidationStatusAbnormal:
		return true
	default:
		return false
	}
}

// Validate 校验总体状态取值是否受支持。
func (s ValidationStatus) Validate() error {
	if s.IsKnown() {
		return nil
	}
	return invalidField("ExecutionHost", "validation_status", "未知的校验状态 "+string(s))
}

// Usable 判断当前状态是否允许用于真实远程任务执行。
func (s ValidationStatus) Usable() bool {
	return s == ValidationStatusNormal
}

// CheckStatus 是单项检查的结果状态。
//
// 该类型只用于校验详细结果，不新增总体状态：总体状态始终由必需项是否全部通过
// 决定，单项的 skipped / not_run 不会被当成通过。
type CheckStatus string

// 单项检查结果取值。
const (
	// CheckStatusPassed 表示该项检查通过。
	CheckStatusPassed CheckStatus = "passed"
	// CheckStatusFailed 表示该项检查失败。
	CheckStatusFailed CheckStatus = "failed"
	// CheckStatusTimedOut 表示该项检查在给定时限内未完成。
	CheckStatusTimedOut CheckStatus = "timed_out"
	// CheckStatusSkipped 表示该项按产品配置无需执行。
	CheckStatusSkipped CheckStatus = "skipped"
	// CheckStatusNotRun 表示前序必需检查失败，该项未执行。
	CheckStatusNotRun CheckStatus = "not_run"
)

// String 返回单项状态的字符串形式。
func (s CheckStatus) String() string {
	return string(s)
}

// Passed 判断单项检查是否通过。
func (s CheckStatus) Passed() bool {
	return s == CheckStatusPassed
}

// 校验器版本与能力边界说明。
const (
	// ValidatorVersion 是当前校验器实现的版本标识。
	//
	// 版本号参与校验详情，便于区分"旧版本校验结果"与"新版本校验结果"。
	// v2 增加了 Docker CLI / Engine / API 基线检查；v3 移除了基础镜像元数据检查，
	// 因此 v2 与更早版本的 normal 结果都不再被视为有效。
	ValidatorVersion = "v3"

	// 校验项名称，与界面展示一一对应。
	ValidationCheckSSHConnection = "ssh_connection"
	ValidationCheckDockerVersion = "docker_version"
	ValidationCheckDockerCLIHelp = "docker_cli_help"
)

// 校验能力边界说明。
//
// 这些文案同时写入校验详情与界面，避免用户把 normal 误读为"基础镜像可用"或
// "已经证明远端 Docker Engine 能成功创建并运行 AgentFlow 所需的容器"。
const (
	// CapabilityDockerVersionChecked 说明连接与版本检查覆盖的范围。
	CapabilityDockerVersionChecked = "SSH 连接能力已验证：握手、身份认证与一条无副作用的远程命令均正常；Docker CLI、Engine 与固定 API 基准的兼容性也在同一连接上验证。"
	// CapabilityDockerCLIHelpOnly 说明命令检查的边界。
	CapabilityDockerCLIHelpOnly = "Docker 命令与参数仅通过 CLI 帮助信息核对；没有验证基础镜像 Registry 可访问性，也没有执行 Docker Build、容器创建或启动。"
)

// ValidationCheck 是一项校验的结果。
type ValidationCheck struct {
	// Name 是稳定的检查项名称。
	Name string `json:"name"`
	// Title 是面向用户的中文标题。
	Title string `json:"title"`
	// Status 是该项检查的结果状态。
	Status CheckStatus `json:"status"`
	// ErrorCode 是稳定的失败分类代码，成功时为空。
	ErrorCode string `json:"error_code,omitempty"`
	// Message 是简短的成功说明或失败原因，已截断并脱敏。
	Message string `json:"message,omitempty"`
	// StartedAt 是该项检查的开始时间。
	StartedAt time.Time `json:"started_at"`
	// CompletedAt 是该项检查的完成时间，未执行时为空。
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// DurationMillis 是该项检查耗时毫秒数。
	DurationMillis int64 `json:"duration_millis"`
	// Details 是该检查项的补充说明，例如缺失的 Docker 选项列表。
	Details []string `json:"details,omitempty"`
}

// ValidationDetails 是一次预检的完整结构化结果。
//
// 该结构整体以 JSON 保存到 execution_hosts.validation_details_json，因此字段变更
// 不需要数据库结构变更；读取失败时按"详情不可用"处理，不影响总体状态。
//
// Checks 只包含三项必需检查（SSH 连接、Docker 版本与 API 兼容性、Docker CLI 帮助
// 信息），不再携带基础镜像元数据等逐镜像结果。
type ValidationDetails struct {
	// ValidatorVersion 是产生本结果的校验器版本。
	ValidatorVersion string `json:"validator_version"`
	// Generation 是本次校验对应的配置代次。
	Generation int64 `json:"generation"`
	// Checks 是各项检查结果，顺序与执行顺序一致。
	Checks []ValidationCheck `json:"checks"`
	// Capabilities 是本次预检的能力边界说明。
	Capabilities []string `json:"capabilities"`
	// StartedAt 是本次校验的开始时间。
	StartedAt time.Time `json:"started_at"`
	// CompletedAt 是本次校验的完成时间，未完成时为空。
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// DurationMillis 是本次校验总耗时毫秒数。
	DurationMillis int64 `json:"duration_millis"`
}

// Check 返回指定名称的检查结果。
func (d ValidationDetails) Check(name string) (ValidationCheck, bool) {
	for _, check := range d.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return ValidationCheck{}, false
}

// AllRequiredPassed 判断全部必需检查项是否都通过。
//
// skipped 与 not_run 一律不算通过：第一版没有"按配置可以跳过必需检查"的场景，
// 因此这里不会把未执行当成成功。
func (d ValidationDetails) AllRequiredPassed() bool {
	if len(d.Checks) == 0 {
		return false
	}
	for _, check := range d.Checks {
		if !check.Status.Passed() {
			return false
		}
	}
	return true
}

// InvalidateValidation 使既有校验结果失效，并把状态置为 checking。
//
// 任何影响连接目标、认证方式、私钥路径或启用状态的有效变更都必须调用本方法：
// 旧结果一旦失效就不能继续被当作 normal 使用。远程工作空间路径不参与预检输入，
// 因此修改它不需要让校验结果失效。代次随之增加，使正在执行的旧校验任务在写回
// 结果时被拒绝。
func (h *ExecutionHost) InvalidateValidation(at time.Time) {
	invalidateAt := normalizeTime(at)
	h.ValidationStatus = ValidationStatusChecking
	h.ValidationMessage = ""
	h.ValidationDetails = nil
	h.ValidationStartedAt = nil
	h.ValidationCompletedAt = nil
	h.ValidationGeneration++
	h.UpdatedAt = invalidateAt
}

// MarkValidationRunning 记录一次校验的开始。
//
// 代次不一致说明配置已被修改，调用方必须放弃本次校验，因此这里在代次不匹配时
// 直接返回错误。
func (h *ExecutionHost) MarkValidationRunning(generation int64, at time.Time) error {
	if generation != h.ValidationGeneration {
		return fmt.Errorf(
			"校验代次已过期: 期望 %d，当前 %d",
			generation,
			h.ValidationGeneration,
		)
	}
	startedAt := normalizeTime(at)
	h.ValidationStatus = ValidationStatusChecking
	h.ValidationMessage = ""
	h.ValidationDetails = nil
	h.ValidationStartedAt = &startedAt
	h.ValidationCompletedAt = nil
	h.UpdatedAt = startedAt
	return nil
}

// RecordValidationResult 记录一次已完成校验的结果。
//
// 只有在当前代次与本次校验代次一致时才允许写入，避免旧校验覆盖新配置。结果状态
// 由 details 决定：全部必需检查通过才是 normal，否则一律 abnormal。
func (h *ExecutionHost) RecordValidationResult(
	generation int64,
	details ValidationDetails,
	completedAt time.Time,
) error {
	if generation != h.ValidationGeneration {
		return fmt.Errorf(
			"校验代次已过期: 期望 %d，当前 %d",
			generation,
			h.ValidationGeneration,
		)
	}
	status := ValidationStatusAbnormal
	if details.AllRequiredPassed() {
		status = ValidationStatusNormal
	}
	finishedAt := normalizeTime(completedAt)
	details.Generation = generation
	details.CompletedAt = &finishedAt
	// 开始时间以校验详情为准：即使校验器没有单独标记开始，也要记录本次校验的时间范围。
	startedAt := normalizeTime(details.StartedAt)
	if details.StartedAt.IsZero() {
		startedAt = finishedAt
	}
	h.ValidationStartedAt = &startedAt
	h.ValidationStatus = status
	h.ValidationMessage = summarizeValidation(details)
	h.ValidationDetails = &details
	h.ValidationCompletedAt = &finishedAt
	h.UpdatedAt = finishedAt
	return nil
}

// FailValidation 记录一次无法产生结构化结果的校验失败。
//
// 该路径用于调度失败、凭据缺失等"检查根本没有开始"的情况：状态必须收敛为
// abnormal 并给出可读原因，不能让配置永久停留在无法恢复的 checking。
func (h *ExecutionHost) FailValidation(
	generation int64,
	message string,
	at time.Time,
) error {
	if generation != h.ValidationGeneration {
		return fmt.Errorf(
			"校验代次已过期: 期望 %d，当前 %d",
			generation,
			h.ValidationGeneration,
		)
	}
	finishedAt := normalizeTime(at)
	h.ValidationStatus = ValidationStatusAbnormal
	h.ValidationMessage = truncateMessage(message, 500)
	h.ValidationCompletedAt = &finishedAt
	h.UpdatedAt = finishedAt
	return nil
}

// summarizeValidation 生成总体状态的摘要文案。
//
// 摘要只描述第一项未通过的必需检查，完整结果在 ValidationDetails 中。
func summarizeValidation(details ValidationDetails) string {
	for _, check := range details.Checks {
		if check.Status.Passed() {
			continue
		}
		title := check.Title
		if strings.TrimSpace(title) == "" {
			title = check.Name
		}
		message := strings.TrimSpace(check.Message)
		if message == "" {
			return fmt.Sprintf("%s未通过（%s）", title, check.Status)
		}
		return fmt.Sprintf("%s未通过：%s", title, message)
	}
	if details.AllRequiredPassed() {
		return "全部必需预检项通过；未验证基础镜像 Registry 可访问性，也未执行 Docker Build、容器创建或启动。"
	}
	return "校验未完成"
}

// truncateMessage 截断过长的说明文本。
func truncateMessage(message string, limit int) string {
	trimmed := strings.TrimSpace(message)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "..."
}
