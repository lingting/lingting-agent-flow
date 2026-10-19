package docker

import (
	"fmt"
	"strconv"
	"strings"
)

// Docker 兼容性基线。
//
// 这三个值是全项目唯一的来源：规则文件（AGENTS.md）、命令构造、SSH 环境预检、
// 直接 Engine API 访问与测试都必须与它们一致。修改基线属于架构决策，必须同时更新
// 规则、代码、命令清单、测试与文档。
const (
	// MinimumCLIVersion 是支持的最低 Docker CLI 版本。
	MinimumCLIVersion = "29.0.0"
	// MinimumEngineVersion 是支持的最低 Docker Engine 版本。
	MinimumEngineVersion = "29.0.0"
	// EngineAPIBaseVersion 是 AgentFlow 使用的 Docker Engine API 基准版本。
	//
	// 所有直接 API 调用使用 /v1.52/ 路径，所有 CLI 调用显式设置
	// DOCKER_API_VERSION=1.52。
	EngineAPIBaseVersion = "1.52"
)

// dockerAPIVersionPrefix 是 Engine API 的版本化路径前缀。
//
// 由 EngineAPIBaseVersion 派生，禁止在别处硬编码版本号。
var dockerAPIVersionPrefix = "/v" + EngineAPIBaseVersion

// dockerAPIVersionEnv 是固定 Docker CLI API 版本的环境变量名。
const dockerAPIVersionEnv = "DOCKER_API_VERSION"

// Version 是解析后的版本号。
//
// 只保存数字分量，便于按分量比较，避免 29.10.0 与 29.9.0 被字典序误判。
type Version struct {
	// Raw 是解析前的原始文本。
	Raw string
	// Parts 是数字分量，例如 29.0.1 解析为 [29 0 1]。
	Parts []int
	// PreRelease 是预发布后缀，例如 29.0.0-rc.1 中的 rc.1。
	PreRelease string
}

// String 返回原始版本文本。
func (v Version) String() string {
	return v.Raw
}

// IsPreRelease 判断是否为预发布版本。
func (v Version) IsPreRelease() bool {
	return v.PreRelease != ""
}

// Compare 按数字分量比较两个版本。
//
// 缺少的分量按 0 处理，因此 29.0 与 29.0.0 相等。调用方必须保证两个版本都已解析成功。
func (v Version) Compare(other Version) int {
	length := len(v.Parts)
	if len(other.Parts) > length {
		length = len(other.Parts)
	}
	for index := 0; index < length; index++ {
		left := 0
		if index < len(v.Parts) {
			left = v.Parts[index]
		}
		right := 0
		if index < len(other.Parts) {
			right = other.Parts[index]
		}
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		}
	}
	return 0
}

// AtLeast 判断当前版本是否不低于给定版本。
func (v Version) AtLeast(minimum Version) bool {
	return v.Compare(minimum) >= 0
}

// ParseVersion 解析形如 29.0.0 或 29.0.0-rc.1 的版本号。
//
// 只接受以数字开头、分量由点分隔的形式；空文本、非数字分量与缺失主版本一律失败，
// 使"无法可靠判定"的版本不会被当成满足基线。
func ParseVersion(raw string) (Version, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Version{}, fmt.Errorf("docker: 版本号为空")
	}
	// 去掉可能存在的 v 前缀，例如 v29.0.0。
	trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "v"), "V")
	numeric := trimmed
	preRelease := ""
	if index := strings.IndexAny(trimmed, "-+"); index >= 0 {
		numeric = trimmed[:index]
		preRelease = strings.TrimPrefix(trimmed[index:], "-")
		preRelease = strings.TrimPrefix(preRelease, "+")
	}
	if numeric == "" {
		return Version{}, fmt.Errorf("docker: 无法解析版本号 %q", raw)
	}
	fields := strings.Split(numeric, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil || value < 0 {
			return Version{}, fmt.Errorf("docker: 无法解析版本号 %q", raw)
		}
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return Version{}, fmt.Errorf("docker: 无法解析版本号 %q", raw)
	}
	return Version{Raw: trimmed, Parts: parts, PreRelease: preRelease}, nil
}

// VersionCheck 是一次最低版本检查的结果。
type VersionCheck struct {
	// Name 是被检查的组件名称，例如 "Docker CLI"。
	Name string
	// Actual 是实际版本文本。
	Actual string
	// Minimum 是要求的最低版本。
	Minimum string
	// Err 是检查失败原因，成功时为 nil。
	Err error
}

// Succeeded 判断检查是否通过。
func (c VersionCheck) Succeeded() bool {
	return c.Err == nil
}

// CheckMinimumVersion 检查实际版本是否满足最低基线。
//
// 预发布版本与无法解析的版本一律拒绝：无法可靠判定是否达到正式版本基线时不得默认通过。
func CheckMinimumVersion(name, actual, minimum string) VersionCheck {
	check := VersionCheck{Name: name, Actual: strings.TrimSpace(actual), Minimum: minimum}
	required, err := ParseVersion(minimum)
	if err != nil {
		check.Err = fmt.Errorf("docker: 基线版本 %q 非法: %w", minimum, err)
		return check
	}
	parsed, err := ParseVersion(actual)
	if err != nil {
		check.Err = fmt.Errorf("docker: 无法解析 %s 版本 %q，不能确认满足最低版本 %s", name, check.Actual, minimum)
		return check
	}
	if parsed.IsPreRelease() {
		check.Err = fmt.Errorf(
			"docker: %s 版本 %s 是预发布版本，不能确认满足最低版本 %s",
			name,
			parsed.String(),
			minimum,
		)
		return check
	}
	if !parsed.AtLeast(required) {
		check.Err = fmt.Errorf(
			"docker: %s 版本 %s 低于要求的最低版本 %s",
			name,
			parsed.String(),
			minimum,
		)
	}
	return check
}

// APIRange 是 Engine 声明的 API 版本区间。
//
// Maximum 是服务端支持的最高 API 版本，Minimum 是服务端接受的最低 API 版本。
// 二者与 AgentFlow 的基准版本不是同一个概念，不得混用。
type APIRange struct {
	// Maximum 是服务端最高 API 版本，例如 1.52。
	Maximum string
	// Minimum 是服务端最低接受 API 版本，例如 1.24。
	Minimum string
}

// APICheck 是一次 API 基准兼容性检查的结果。
type APICheck struct {
	// Baseline 是 AgentFlow 固定的 API 基准版本。
	Baseline string
	// Range 是被检查的 Engine API 版本区间。
	Range APIRange
	// Err 是检查失败原因，成功时为 nil。
	Err error
}

// Succeeded 判断检查是否通过。
func (c APICheck) Succeeded() bool {
	return c.Err == nil
}

// CheckAPICompatibility 检查固定 API 基准是否落在 Engine 声明的版本区间内。
//
// 判定条件是 MinAPIVersion <= 1.52 <= APIVersion。服务端最高版本低于基准时无法使用
// 基准路径；最低接受版本高于基准时基准路径会被服务端拒绝。两种情况都必须失败，
// 不得自动降级或升级到其他 API 版本。
func CheckAPICompatibility(apiRange APIRange) APICheck {
	check := APICheck{Baseline: EngineAPIBaseVersion, Range: apiRange}
	baseline, err := ParseVersion(EngineAPIBaseVersion)
	if err != nil {
		check.Err = fmt.Errorf("docker: API 基准版本 %q 非法: %w", EngineAPIBaseVersion, err)
		return check
	}
	maximum, err := ParseVersion(apiRange.Maximum)
	if err != nil {
		check.Err = fmt.Errorf(
			"docker: 无法解析 Docker Engine 最高 API 版本 %q，不能确认支持 API %s",
			strings.TrimSpace(apiRange.Maximum),
			EngineAPIBaseVersion,
		)
		return check
	}
	minimum, err := ParseVersion(apiRange.Minimum)
	if err != nil {
		check.Err = fmt.Errorf(
			"docker: 无法解析 Docker Engine 最低接受 API 版本 %q，不能确认支持 API %s",
			strings.TrimSpace(apiRange.Minimum),
			EngineAPIBaseVersion,
		)
		return check
	}
	if maximum.Compare(baseline) < 0 {
		check.Err = fmt.Errorf(
			"docker: Docker Engine 最高 API 版本 %s 低于 AgentFlow 基准 API %s",
			maximum.String(),
			EngineAPIBaseVersion,
		)
		return check
	}
	if minimum.Compare(baseline) > 0 {
		check.Err = fmt.Errorf(
			"docker: Docker Engine 最低接受 API 版本 %s 高于 AgentFlow 基准 API %s",
			minimum.String(),
			EngineAPIBaseVersion,
		)
		return check
	}
	return check
}

// CheckVersionSupport 校验 Docker CLI 版本是否满足最低基线。
//
// 保留该入口用于既有的 CLI 预检；Engine 版本与 API 版本分别由
// CheckMinimumVersion 与 CheckAPICompatibility 检查。
func CheckVersionSupport(raw string) error {
	return CheckMinimumVersion("Docker CLI", raw, MinimumCLIVersion).Err
}
