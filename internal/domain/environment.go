package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EnvironmentType 标识环境定义的类型。
//
// 基础环境（base）是语言环境的构建基础；语言环境（language）在基础环境之上
// 安装具体语言运行时。两类环境共用同一套版本、构建与容器管理接口。
type EnvironmentType string

// 支持的环境类型取值。
const (
	// EnvironmentTypeBase 表示基础环境。
	EnvironmentTypeBase EnvironmentType = "base"
	// EnvironmentTypeLanguage 表示语言环境。
	EnvironmentTypeLanguage EnvironmentType = "language"
)

// BaseEnvironmentName 是基础环境的固定名称。
const BaseEnvironmentName = "base"

// String 返回环境类型的字符串形式。
func (t EnvironmentType) String() string {
	return string(t)
}

// Validate 校验环境类型是否受支持。
func (t EnvironmentType) Validate() error {
	switch t {
	case EnvironmentTypeBase, EnvironmentTypeLanguage:
		return nil
	default:
		return invalidField("Environment", "type", "未知的环境类型 "+string(t))
	}
}

// ParseEnvironmentType 解析命令行或配置中给出的环境类型名称。
func ParseEnvironmentType(raw string) (EnvironmentType, error) {
	value := EnvironmentType(strings.ToLower(strings.TrimSpace(raw)))
	if err := value.Validate(); err != nil {
		return "", err
	}
	return value, nil
}

// EnvironmentNamePattern 约束环境名称的书写形式。
//
// 名称会进入远程目录路径与镜像仓库名，因此只允许小写字母、数字与连字符。
var EnvironmentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// EnvironmentRef 唯一标识一个环境定义（类型加名称）。
type EnvironmentRef struct {
	// Type 是环境类型。
	Type EnvironmentType
	// Name 是环境名称；基础环境固定为 base。
	Name string
}

// NewEnvironmentRef 构造并校验环境引用。
//
// 基础环境忽略传入名称并统一为 base，避免同一环境出现多种写法。
func NewEnvironmentRef(environmentType EnvironmentType, name string) (EnvironmentRef, error) {
	if err := environmentType.Validate(); err != nil {
		return EnvironmentRef{}, err
	}
	if environmentType == EnvironmentTypeBase {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" && trimmed != BaseEnvironmentName {
			return EnvironmentRef{}, invalidField(
				"Environment",
				"name",
				"基础环境名称固定为 "+BaseEnvironmentName,
			)
		}
		return EnvironmentRef{Type: EnvironmentTypeBase, Name: BaseEnvironmentName}, nil
	}
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if !EnvironmentNamePattern.MatchString(trimmed) {
		return EnvironmentRef{}, invalidField(
			"Environment",
			"name",
			"环境名称只能包含小写字母、数字与连字符，且不超过 32 个字符",
		)
	}
	return EnvironmentRef{Type: EnvironmentTypeLanguage, Name: trimmed}, nil
}

// Validate 校验环境引用。
func (r EnvironmentRef) Validate() error {
	if err := r.Type.Validate(); err != nil {
		return err
	}
	if r.Type == EnvironmentTypeBase {
		if r.Name != BaseEnvironmentName {
			return invalidField("Environment", "name", "基础环境名称固定为 "+BaseEnvironmentName)
		}
		return nil
	}
	if !EnvironmentNamePattern.MatchString(r.Name) {
		return invalidField("Environment", "name", "环境名称书写非法")
	}
	return nil
}

// Label 返回环境引用的人类可读标识，例如 base 或 language/node。
func (r EnvironmentRef) Label() string {
	return string(r.Type) + "/" + r.Name
}

// IsBase 判断是否为基础环境。
func (r EnvironmentRef) IsBase() bool {
	return r.Type == EnvironmentTypeBase
}

// VersionLabelPrefix 是环境版本号的前缀。
const VersionLabelPrefix = "v"

// 环境版本号的取值范围。
const (
	// MinEnvironmentVersionNumber 是第一个可用版本号。
	MinEnvironmentVersionNumber = 1
	// MaxEnvironmentVersionNumber 是版本号上限，避免标签与目录名过长。
	MaxEnvironmentVersionNumber = 9999
)

// FormatVersionLabel 把版本号格式化为 v0001 形式。
func FormatVersionLabel(number int) string {
	return fmt.Sprintf("%s%04d", VersionLabelPrefix, number)
}

// ParseVersionLabel 解析 v0001 形式的版本标签。
func ParseVersionLabel(label string) (int, error) {
	trimmed := strings.TrimSpace(label)
	if !strings.HasPrefix(trimmed, VersionLabelPrefix) {
		return 0, invalidField("EnvironmentVersion", "version", "版本标签必须以 v 开头")
	}
	digits := strings.TrimPrefix(trimmed, VersionLabelPrefix)
	if len(digits) == 0 {
		return 0, invalidField("EnvironmentVersion", "version", "版本标签缺少数字部分")
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return 0, invalidField("EnvironmentVersion", "version", "版本标签只能包含数字")
		}
	}
	number, err := strconv.Atoi(digits)
	if err != nil {
		return 0, invalidField("EnvironmentVersion", "version", "版本标签数字非法")
	}
	if number < MinEnvironmentVersionNumber || number > MaxEnvironmentVersionNumber {
		return 0, invalidField(
			"EnvironmentVersion",
			"version",
			fmt.Sprintf("版本号必须在 %d-%d 之间", MinEnvironmentVersionNumber, MaxEnvironmentVersionNumber),
		)
	}
	return number, nil
}

// NextVersionNumber 返回下一个可用版本号。
//
// 版本号只递增，永不复用：即使历史版本已被标记为构建失败，其编号也不再分配。
func NextVersionNumber(existing []int) (int, error) {
	highest := 0
	for _, number := range existing {
		if number > highest {
			highest = number
		}
	}
	next := highest + 1
	if next > MaxEnvironmentVersionNumber {
		return 0, invalidField("EnvironmentVersion", "version", "版本号已用尽")
	}
	if next < MinEnvironmentVersionNumber {
		next = MinEnvironmentVersionNumber
	}
	return next, nil
}

// BuildStatus 是环境版本对应的镜像构建状态。
type BuildStatus string

// 构建状态取值。
//
// 只有 succeeded 的版本可以被容器使用；failed 版本保留在历史中但不可用。
const (
	// BuildStatusPending 表示版本已创建但尚未构建。
	BuildStatusPending BuildStatus = "pending"
	// BuildStatusBuilding 表示构建正在进行。
	BuildStatusBuilding BuildStatus = "building"
	// BuildStatusSucceeded 表示构建成功并已核实镜像信息。
	BuildStatusSucceeded BuildStatus = "succeeded"
	// BuildStatusFailed 表示构建失败或镜像核实失败。
	BuildStatusFailed BuildStatus = "failed"
)

// String 返回构建状态的字符串形式。
func (s BuildStatus) String() string {
	return string(s)
}

// Validate 校验构建状态是否属于已知取值。
func (s BuildStatus) Validate() error {
	switch s {
	case BuildStatusPending, BuildStatusBuilding, BuildStatusSucceeded, BuildStatusFailed:
		return nil
	default:
		return invalidField("EnvironmentVersion", "build_status", "未知的构建状态 "+string(s))
	}
}

// Usable 判断构建状态是否可用于创建容器。
func (s BuildStatus) Usable() bool {
	return s == BuildStatusSucceeded
}

// sha256Pattern 约束 SHA-256 十六进制摘要的书写形式。
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateSHA256 校验 64 位小写十六进制摘要。
func ValidateSHA256(field, value string) error {
	if !sha256Pattern.MatchString(value) {
		return invalidField("EnvironmentVersion", field, "必须是 64 位小写十六进制 SHA-256 摘要")
	}
	return nil
}

// EnvironmentVersion 是一个不可覆盖的环境配置版本。
//
// 版本一旦创建，其 Dockerfile 内容哈希与构建配置哈希即固定；后续构建只更新
// 构建状态、镜像标签与实际镜像 ID，不修改历史版本内容。
type EnvironmentVersion struct {
	// HostID 是版本所属的执行节点。
	HostID ID `json:"host_id"`
	// Ref 是所属环境。
	Ref EnvironmentRef `json:"environment"`
	// Number 是递增版本号。
	Number int `json:"number"`
	// DockerfileSHA256 是 Dockerfile 内容的 SHA-256。
	DockerfileSHA256 string `json:"dockerfile_sha256"`
	// BuildConfigSHA256 是完整构建配置（含父镜像版本与构建选项）的 SHA-256。
	BuildConfigSHA256 string `json:"build_config_sha256"`
	// ContextSHA256 是规范化构建上下文目录的确定性 SHA-256。
	ContextSHA256 string `json:"context_sha256"`
	// ParentVersion 是依赖的父环境版本标签，基础环境为空。
	ParentVersion string `json:"parent_version,omitempty"`
	// ImageRef 是构建使用的镜像引用，创建版本时即确定。
	ImageRef string `json:"image_ref"`
	// ImageID 是构建成功后取得的实际镜像 ID。
	ImageID string `json:"image_id,omitempty"`
	// Status 是构建状态。
	Status BuildStatus `json:"build_status"`
	// ErrorSummary 是构建失败的可诊断摘要，不包含凭据内容。
	ErrorSummary string `json:"error_summary,omitempty"`
	// CreatedAt 是版本创建时间。
	CreatedAt time.Time `json:"created_at"`
	// BuiltAt 是构建完成时间，未构建时为空。
	BuiltAt *time.Time `json:"built_at,omitempty"`
}

// Label 返回版本标签，例如 v0001。
func (v EnvironmentVersion) Label() string {
	return FormatVersionLabel(v.Number)
}

// Validate 校验版本定义是否完整自洽。
func (v EnvironmentVersion) Validate() error {
	if err := v.HostID.Validate(); err != nil {
		return err
	}
	if err := v.Ref.Validate(); err != nil {
		return err
	}
	if v.Number < MinEnvironmentVersionNumber || v.Number > MaxEnvironmentVersionNumber {
		return invalidField("EnvironmentVersion", "number", "版本号超出允许范围")
	}
	if err := ValidateSHA256("dockerfile_sha256", v.DockerfileSHA256); err != nil {
		return err
	}
	if err := ValidateSHA256("build_config_sha256", v.BuildConfigSHA256); err != nil {
		return err
	}
	if err := ValidateSHA256("context_sha256", v.ContextSHA256); err != nil {
		return err
	}
	if err := validateParentVersion(v); err != nil {
		return err
	}
	if err := ValidateImageRef(v.ImageRef); err != nil {
		return err
	}
	if err := v.Status.Validate(); err != nil {
		return err
	}
	if v.Status == BuildStatusSucceeded {
		if err := ValidateImageID(v.ImageID); err != nil {
			return err
		}
		if v.BuiltAt == nil {
			return invalidField("EnvironmentVersion", "built_at", "构建成功的版本必须记录构建时间")
		}
	}
	return nil
}

// validateParentVersion 校验父环境版本字段。
func validateParentVersion(v EnvironmentVersion) error {
	if v.Ref.IsBase() {
		if v.ParentVersion != "" {
			return invalidField("EnvironmentVersion", "parent_version", "基础环境不能依赖父环境版本")
		}
		return nil
	}
	if strings.TrimSpace(v.ParentVersion) == "" {
		return invalidField("EnvironmentVersion", "parent_version", "语言环境必须记录父基础环境版本")
	}
	if _, err := ParseVersionLabel(v.ParentVersion); err != nil {
		return err
	}
	return nil
}

// Usable 判断版本是否可以用于创建容器。
func (v EnvironmentVersion) Usable() bool {
	return v.Status.Usable() && strings.TrimSpace(v.ImageID) != ""
}

// MarkBuilding 把版本标记为正在构建。
//
// 已成功的版本不会被重新标记，避免覆盖既有构建结果。
func (v *EnvironmentVersion) MarkBuilding() error {
	if v == nil {
		return invalidField("EnvironmentVersion", "", "实体为空")
	}
	if v.Status == BuildStatusSucceeded {
		return fmt.Errorf(
			"%w: EnvironmentVersion 已成功构建，不能重新标记为构建中",
			ErrInvalidState,
		)
	}
	v.Status = BuildStatusBuilding
	v.ErrorSummary = ""
	return nil
}

// MarkBuilt 记录构建成功与核实后的实际镜像 ID。
func (v *EnvironmentVersion) MarkBuilt(imageID string, at time.Time) error {
	if v == nil {
		return invalidField("EnvironmentVersion", "", "实体为空")
	}
	if v.Status == BuildStatusSucceeded {
		return fmt.Errorf("%w: EnvironmentVersion 已成功构建", ErrInvalidState)
	}
	if err := ValidateImageID(imageID); err != nil {
		return err
	}
	builtAt := normalizeTime(at)
	v.Status = BuildStatusSucceeded
	v.ImageID = strings.TrimSpace(imageID)
	v.BuiltAt = &builtAt
	v.ErrorSummary = ""
	return nil
}

// MarkFailed 记录构建失败。
//
// 失败版本保留在历史中，但不会被标记为可用。
func (v *EnvironmentVersion) MarkFailed(summary string) error {
	if v == nil {
		return invalidField("EnvironmentVersion", "", "实体为空")
	}
	if v.Status == BuildStatusSucceeded {
		return fmt.Errorf("%w: EnvironmentVersion 已成功构建，不能标记为失败", ErrInvalidState)
	}
	v.Status = BuildStatusFailed
	v.ImageID = ""
	v.BuiltAt = nil
	v.ErrorSummary = strings.TrimSpace(summary)
	return nil
}
