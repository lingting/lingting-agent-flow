package domain

import (
	"fmt"
	"strings"
)

// 任务级配置的边界常量。
const (
	// MaxTaskMaxAttempts 是任务允许配置的最大尝试次数上限。
	//
	// 重试次数必须有界：任何情况下都不允许无限重试。
	MaxTaskMaxAttempts = 10
	// DefaultMaxPlanSteps 是 Planner 输出允许的最大步骤数。
	DefaultMaxPlanSteps = 20
	// MaxPlanStepsLimit 是计划步骤数的硬上限。
	MaxPlanStepsLimit = 50
	// MaxVerificationCommands 是单个验证规格允许的命令数量上限。
	MaxVerificationCommands = 20
	// MaxVerificationOutputBytes 是单条验证命令保留的输出字节上限。
	MaxVerificationOutputBytes = 64 << 10
	// MaxCommandLength 是单条验证命令行的长度上限。
	MaxCommandLength = 4096
	// MaxAcceptanceCriteria 是验收条件数量上限。
	MaxAcceptanceCriteria = 50
)

// RetryErrorClass 是可重试的错误类别。
//
// 重试决策由 Go 工作流引擎作出，Pi Runner 不会自行重试整次 Agent 调用。
type RetryErrorClass string

// 可重试错误类别取值。
const (
	// RetryClassAgentFailure 表示 Agent 调用以非零退出码结束。
	RetryClassAgentFailure RetryErrorClass = "agent_failure"
	// RetryClassVerificationFailure 表示必需验证命令失败。
	RetryClassVerificationFailure RetryErrorClass = "verification_failure"
	// RetryClassTimeout 表示 Agent 或验证命令超时。
	RetryClassTimeout RetryErrorClass = "timeout"
	// RetryClassNetwork 表示可核验的网络类失败（例如 Git Clone 网络故障）。
	RetryClassNetwork RetryErrorClass = "network"
)

// Validate 校验错误类别是否受支持。
func (c RetryErrorClass) Validate() error {
	switch c {
	case RetryClassAgentFailure,
		RetryClassVerificationFailure,
		RetryClassTimeout,
		RetryClassNetwork:
		return nil
	default:
		return invalidField("RetryConfig", "retry_classes", "未知的可重试错误类别 "+string(c))
	}
}

// RetryConfig 是任务与步骤的重试策略。
//
// 默认值必须有界且明确：未显式配置时使用 DefaultMaxAttempts，
// 并且只对 Agent 失败与验证失败重试；超时与网络错误需要显式开启。
type RetryConfig struct {
	// MaxAttempts 是包含首次执行在内的尝试次数上限。
	MaxAttempts int `json:"max_attempts"`
	// RetryClasses 是允许自动重试的错误类别。
	RetryClasses []RetryErrorClass `json:"retry_classes,omitempty"`
	// RetryTimeout 表示对超时失败重试。
	RetryTimeout bool `json:"retry_timeout,omitempty"`
	// RetryVerification 表示对必需验证失败重试（修复型重试）。
	RetryVerification bool `json:"retry_verification,omitempty"`
	// RetryNetwork 表示对可核验的网络类失败重试。
	RetryNetwork bool `json:"retry_network,omitempty"`
	// WaitSeconds 是两次尝试之间的等待秒数，0 表示不等待。
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

// Validate 校验重试策略。
func (c RetryConfig) Validate() error {
	if c.MaxAttempts < 0 || c.MaxAttempts > MaxTaskMaxAttempts {
		return invalidField(
			"RetryConfig",
			"max_attempts",
			fmt.Sprintf("尝试次数上限必须在 0 到 %d 之间", MaxTaskMaxAttempts),
		)
	}
	if c.WaitSeconds < 0 {
		return invalidField("RetryConfig", "wait_seconds", "等待秒数不能为负数")
	}
	for _, class := range c.RetryClasses {
		if err := class.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Limit 返回生效的尝试次数上限。
func (c RetryConfig) Limit() int {
	if c.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	if c.MaxAttempts > MaxTaskMaxAttempts {
		return MaxTaskMaxAttempts
	}
	return c.MaxAttempts
}

// Allows 判断给定错误类别是否允许自动重试。
//
// 默认策略只对 Agent 失败与验证失败重试；超时与网络错误必须显式开启。
func (c RetryConfig) Allows(class RetryErrorClass) bool {
	if err := class.Validate(); err != nil {
		return false
	}
	switch class {
	case RetryClassTimeout:
		return c.RetryTimeout || containsRetryClass(c.RetryClasses, class)
	case RetryClassNetwork:
		return c.RetryNetwork || containsRetryClass(c.RetryClasses, class)
	case RetryClassVerificationFailure:
		if c.RetryVerification || containsRetryClass(c.RetryClasses, class) {
			return true
		}
		// 未显式配置任何类别时使用默认策略：验证失败属于修复型重试。
		return len(c.RetryClasses) == 0
	default:
		if containsRetryClass(c.RetryClasses, class) {
			return true
		}
		return len(c.RetryClasses) == 0
	}
}

// containsRetryClass 判断类别是否在显式列表中。
func containsRetryClass(classes []RetryErrorClass, target RetryErrorClass) bool {
	for _, class := range classes {
		if class == target {
			return true
		}
	}
	return false
}

// IsZero 判断重试策略是否为空（全部使用默认值）。
func (c RetryConfig) IsZero() bool {
	return c.MaxAttempts == 0 &&
		len(c.RetryClasses) == 0 &&
		!c.RetryTimeout &&
		!c.RetryVerification &&
		!c.RetryNetwork &&
		c.WaitSeconds == 0
}

// TaskConfig 是任务的工作流配置。
//
// 该配置随任务一起持久化，不包含任何凭据：私钥、密码与 Token 只在单次操作期间
// 以内存形式存在，禁止写入任务配置。
type TaskConfig struct {
	// Goal 是任务目标描述。
	Goal string `json:"goal"`
	// Environment 是任务使用的语言环境。
	Environment EnvironmentRef `json:"environment"`
	// EnvironmentVersion 是锁定的环境版本标签，空值表示使用当前可用版本。
	EnvironmentVersion string `json:"environment_version,omitempty"`
	// ProjectID 是可选的项目标识，用于项目级缓存共享范围。
	ProjectID string `json:"project_id,omitempty"`
	// Caches 是允许挂载的缓存类型。
	Caches []string `json:"caches,omitempty"`
	// WorkDir 是容器内项目工作目录，空值使用默认目录。
	WorkDir string `json:"work_dir,omitempty"`
	// Verification 是任务级必需验证命令，由每个开发步骤继承。
	Verification VerificationSpec `json:"verification"`
	// Retry 是任务级重试策略。
	Retry RetryConfig `json:"retry"`
	// MergeAfterPush 表示推送成功后合并基础分支，默认 false。
	MergeAfterPush bool `json:"merge_after_push"`
	// PlannerRoleID 是规划角色标识。
	PlannerRoleID ID `json:"planner_role_id"`
	// DeveloperRoleID 是开发角色标识。
	DeveloperRoleID ID `json:"developer_role_id"`
	// ValidatorRoleID 是验证角色标识。
	ValidatorRoleID ID `json:"validator_role_id"`
	// MaxPlanSteps 是计划步骤数上限，0 表示使用默认值。
	MaxPlanSteps int `json:"max_plan_steps,omitempty"`
	// AgentTimeoutSeconds 是单次 Agent 调用的超时秒数，0 表示使用执行器默认值。
	AgentTimeoutSeconds int `json:"agent_timeout_seconds,omitempty"`
	// VerificationTimeoutSeconds 是单条验证命令的默认超时秒数。
	VerificationTimeoutSeconds int `json:"verification_timeout_seconds,omitempty"`
}

// IsZero 判断配置是否为空，用于兼容不含工作流配置的历史任务记录。
func (c TaskConfig) IsZero() bool {
	return strings.TrimSpace(c.Goal) == "" &&
		c.Environment.IsZero() &&
		strings.TrimSpace(c.EnvironmentVersion) == "" &&
		strings.TrimSpace(c.ProjectID) == "" &&
		len(c.Caches) == 0 &&
		strings.TrimSpace(c.WorkDir) == "" &&
		!c.Verification.HasCommands() &&
		len(c.Verification.AcceptanceCriteria) == 0 &&
		c.Retry.IsZero() &&
		!c.MergeAfterPush &&
		c.PlannerRoleID.IsEmpty() &&
		c.DeveloperRoleID.IsEmpty() &&
		c.ValidatorRoleID.IsEmpty() &&
		c.MaxPlanSteps == 0 &&
		c.AgentTimeoutSeconds == 0 &&
		c.VerificationTimeoutSeconds == 0
}

// Validate 校验任务配置。
func (c TaskConfig) Validate() error {
	if strings.TrimSpace(c.Goal) == "" {
		return invalidField("TaskConfig", "goal", "任务目标不能为空")
	}
	if err := c.Environment.Validate(); err != nil {
		return err
	}
	if c.Environment.IsBase() {
		return invalidField("TaskConfig", "environment", "任务必须使用语言环境")
	}
	if version := strings.TrimSpace(c.EnvironmentVersion); version != "" {
		if _, err := ParseVersionLabel(version); err != nil {
			return err
		}
	}
	workDir := strings.TrimSpace(c.WorkDir)
	if workDir == "" {
		workDir = DefaultContainerWorkDir
	}
	if err := ValidateContainerWorkDir(workDir); err != nil {
		return err
	}
	for _, cacheType := range c.Caches {
		if !EnvironmentNamePattern.MatchString(cacheType) {
			return invalidField("TaskConfig", "caches", "缓存类型书写非法: "+cacheType)
		}
	}
	if err := c.Verification.Validate(); err != nil {
		return err
	}
	if err := c.Retry.Validate(); err != nil {
		return err
	}
	if err := c.PlannerRoleID.Validate(); err != nil {
		return err
	}
	if err := c.DeveloperRoleID.Validate(); err != nil {
		return err
	}
	// 验证角色是可选的：省略时不做辅助评估。
	if !c.ValidatorRoleID.IsEmpty() {
		if err := c.ValidatorRoleID.Validate(); err != nil {
			return err
		}
	}
	if c.MaxPlanSteps < 0 || c.MaxPlanSteps > MaxPlanStepsLimit {
		return invalidField(
			"TaskConfig",
			"max_plan_steps",
			fmt.Sprintf("计划步骤数上限必须在 0 到 %d 之间", MaxPlanStepsLimit),
		)
	}
	if c.AgentTimeoutSeconds < 0 {
		return invalidField("TaskConfig", "agent_timeout_seconds", "超时秒数不能为负数")
	}
	if c.VerificationTimeoutSeconds < 0 {
		return invalidField(
			"TaskConfig",
			"verification_timeout_seconds",
			"超时秒数不能为负数",
		)
	}
	return nil
}

// EffectiveWorkDir 返回生效的容器内项目工作目录。
func (c TaskConfig) EffectiveWorkDir() string {
	if trimmed := strings.TrimSpace(c.WorkDir); trimmed != "" {
		return trimmed
	}
	return DefaultContainerWorkDir
}

// PlanStepLimit 返回生效的计划步骤数上限。
func (c TaskConfig) PlanStepLimit() int {
	if c.MaxPlanSteps <= 0 {
		return DefaultMaxPlanSteps
	}
	if c.MaxPlanSteps > MaxPlanStepsLimit {
		return MaxPlanStepsLimit
	}
	return c.MaxPlanSteps
}
