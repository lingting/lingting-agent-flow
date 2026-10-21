package domain

import (
	"fmt"
	"strings"
	"time"
)

// ValidationRunStatus 表示一次验证流程本身发生了什么。
//
// 它与 ValidationVerdict 严格分离：状态描述流程执行情况（是否跑完、是否被取消），
// 结论描述"现有证据能否支持通过"。两者不得互相推导：流程 completed 不代表
// 结论 passed，流程 failed 也不代表结论一定是 failed。
type ValidationRunStatus string

// 验证流程状态取值。
const (
	// ValidationRunStatusPending 表示验证已登记但尚未开始执行。
	ValidationRunStatusPending ValidationRunStatus = "pending"
	// ValidationRunStatusRunning 表示验证正在按顺序执行检查。
	ValidationRunStatusRunning ValidationRunStatus = "running"
	// ValidationRunStatusCompleted 表示全部检查都已执行并得到结论。
	ValidationRunStatusCompleted ValidationRunStatus = "completed"
	// ValidationRunStatusFailed 表示验证流程本身失败（例如结果无法持久化）。
	ValidationRunStatusFailed ValidationRunStatus = "failed"
	// ValidationRunStatusCancelled 表示验证被调用方取消。
	ValidationRunStatusCancelled ValidationRunStatus = "cancelled"
	// ValidationRunStatusTimedOut 表示验证整体超时后被终止。
	ValidationRunStatusTimedOut ValidationRunStatus = "timed_out"
)

// validationStatusMachine 定义验证流程状态迁移。
//
// 一次验证只能结束一次：结束后不再回到 running，迟到的检查结果不得重新打开
// 已确认的终态。
var validationStatusMachine = statusMachine{
	entity: "ValidationRun",
	transitions: map[string][]string{
		string(ValidationRunStatusPending): {
			string(ValidationRunStatusRunning),
			string(ValidationRunStatusCancelled),
			string(ValidationRunStatusTimedOut),
			string(ValidationRunStatusFailed),
		},
		string(ValidationRunStatusRunning): {
			string(ValidationRunStatusCompleted),
			string(ValidationRunStatusFailed),
			string(ValidationRunStatusCancelled),
			string(ValidationRunStatusTimedOut),
		},
	},
	terminal: []string{
		string(ValidationRunStatusCompleted),
		string(ValidationRunStatusFailed),
		string(ValidationRunStatusCancelled),
		string(ValidationRunStatusTimedOut),
	},
}

// Validate 校验验证流程状态是否属于已知状态。
func (s ValidationRunStatus) Validate() error {
	if validationStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("ValidationRun", "status", "未知状态 "+string(s))
}

// IsTerminal 判断验证流程状态是否为终态。
func (s ValidationRunStatus) IsTerminal() bool {
	return validationStatusMachine.isTerminal(string(s))
}

// ValidationCheckStatus 表示单项确定性检查的执行结果。
//
// 它必须区分"命令以非零码正常结束"与"命令没能正常执行"，否则失败证据会被压平
// 成一个布尔值，无法判断是代码问题还是环境问题。
type ValidationCheckStatus string

// 单项检查状态取值。
const (
	// ValidationCheckStatusPending 表示检查尚未执行。
	ValidationCheckStatusPending ValidationCheckStatus = "pending"
	// ValidationCheckStatusRunning 表示检查正在执行。
	ValidationCheckStatusRunning ValidationCheckStatus = "running"
	// ValidationCheckStatusPassed 表示命令正常结束且满足通过条件。
	ValidationCheckStatusPassed ValidationCheckStatus = "passed"
	// ValidationCheckStatusFailed 表示命令正常结束但未满足通过条件。
	ValidationCheckStatusFailed ValidationCheckStatus = "failed"
	// ValidationCheckStatusSkipped 表示检查因前置条件未满足被跳过。
	ValidationCheckStatusSkipped ValidationCheckStatus = "skipped"
	// ValidationCheckStatusTimedOut 表示命令因超时被终止。
	ValidationCheckStatusTimedOut ValidationCheckStatus = "timed_out"
	// ValidationCheckStatusCancelled 表示命令被调用方取消。
	ValidationCheckStatusCancelled ValidationCheckStatus = "cancelled"
	// ValidationCheckStatusError 表示命令无法启动或执行结果无法确认。
	ValidationCheckStatusError ValidationCheckStatus = "error"
)

// Validate 校验单项检查状态是否属于已知状态。
func (s ValidationCheckStatus) Validate() error {
	switch s {
	case ValidationCheckStatusPending,
		ValidationCheckStatusRunning,
		ValidationCheckStatusPassed,
		ValidationCheckStatusFailed,
		ValidationCheckStatusSkipped,
		ValidationCheckStatusTimedOut,
		ValidationCheckStatusCancelled,
		ValidationCheckStatusError:
		return nil
	default:
		return invalidField("ValidationCheckResult", "status", "未知状态 "+string(s))
	}
}

// IsTerminal 判断检查是否已经结束。
func (s ValidationCheckStatus) IsTerminal() bool {
	switch s {
	case ValidationCheckStatusPassed,
		ValidationCheckStatusFailed,
		ValidationCheckStatusSkipped,
		ValidationCheckStatusTimedOut,
		ValidationCheckStatusCancelled,
		ValidationCheckStatusError:
		return true
	default:
		return false
	}
}

// Passed 判断检查是否通过。
//
// 只有明确执行并满足通过条件的检查才算通过：跳过、超时、取消与错误都不是通过。
func (s ValidationCheckStatus) Passed() bool {
	return s == ValidationCheckStatusPassed
}

// ValidationCheckDefinition 是一项确定性检查的配置。
//
// 命令以可执行文件与参数数组表达，不经过 shell：不允许通过拼接字符串构造命令，
// 也不允许把外部输入当作 shell 语法执行。
type ValidationCheckDefinition struct {
	// ID 是检查的稳定标识，同一次验证内唯一，用于结果引用。
	ID string `json:"id"`
	// Name 是检查名称。
	Name string `json:"name"`
	// Description 是检查说明。
	Description string `json:"description,omitempty"`
	// Type 是检查类型，当前只支持命令型检查。
	Type ValidationCheckType `json:"type"`
	// Enabled 表示该检查是否参与本次验证。
	Enabled bool `json:"enabled"`
	// Required 表示该检查是否属于必需检查。
	Required bool `json:"required"`
	// Sequence 是执行顺序，从 1 开始，数值小的先执行。
	Sequence int `json:"sequence"`
	// Command 是可执行文件路径或 PATH 中的名称。
	Command string `json:"command,omitempty"`
	// Args 是命令参数。
	Args []string `json:"args,omitempty"`
	// WorkDir 是容器内执行目录，空值表示任务工作目录。
	WorkDir string `json:"work_dir,omitempty"`
	// TimeoutSeconds 是单条检查的超时秒数，0 表示使用默认值。
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// MaxOutputBytes 是保留的输出字节上限，0 表示使用默认值。
	MaxOutputBytes int `json:"max_output_bytes,omitempty"`
	// ExpectedExitCodes 是视为通过的退出码集合，空值等价于只允许 0。
	ExpectedExitCodes []int `json:"expected_exit_codes,omitempty"`
	// ExpectedStdoutContains 是标准输出必须包含的片段，空值表示不限制。
	ExpectedStdoutContains string `json:"expected_stdout_contains,omitempty"`
	// ExpectedStderrContains 是标准错误必须包含的片段，空值表示不限制。
	ExpectedStderrContains string `json:"expected_stderr_contains,omitempty"`
}

// ValidationCheckType 是检查类型。
//
// 第一版只支持命令型检查：明确、可测试、可复现。规则引擎等其他类型必须作为
// 独立能力设计，不能在第一版用"预留字段"暗示已支持。
type ValidationCheckType string

// 检查类型取值。
const (
	// ValidationCheckTypeCommand 表示执行一条命令并按退出码与输出匹配判定。
	ValidationCheckTypeCommand ValidationCheckType = "command"
)

// Validate 校验检查类型。
func (t ValidationCheckType) Validate() error {
	switch t {
	case ValidationCheckTypeCommand:
		return nil
	default:
		return invalidField("ValidationCheckDefinition", "type", "不支持的检查类型 "+string(t))
	}
}

// Validate 校验检查定义。
func (d ValidationCheckDefinition) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return invalidField("ValidationCheckDefinition", "id", "检查标识不能为空")
	}
	if len(d.ID) > 128 {
		return invalidField("ValidationCheckDefinition", "id", "检查标识不能超过 128 个字符")
	}
	if strings.TrimSpace(d.Name) == "" {
		return invalidField("ValidationCheckDefinition", "name", "检查名称不能为空")
	}
	if err := d.Type.Validate(); err != nil {
		return err
	}
	if d.Sequence < 1 {
		return invalidField("ValidationCheckDefinition", "sequence", "执行顺序必须从 1 开始")
	}
	if d.Type == ValidationCheckTypeCommand {
		if err := validateCommandToken("ValidationCheckDefinition", "command", d.Command); err != nil {
			return err
		}
		if len(d.Command) > MaxCommandLength {
			return invalidField("ValidationCheckDefinition", "command", "命令内容过长")
		}
		for index, arg := range d.Args {
			if strings.ContainsRune(arg, 0) {
				return invalidField(
					"ValidationCheckDefinition",
					"args",
					"第 "+itoa(index+1)+" 个参数包含非法字符",
				)
			}
		}
		if workDir := strings.TrimSpace(d.WorkDir); workDir != "" {
			if err := ValidateContainerWorkDir(workDir); err != nil {
				return err
			}
		}
	}
	if d.TimeoutSeconds < 0 {
		return invalidField("ValidationCheckDefinition", "timeout_seconds", "超时时间不能为负数")
	}
	if d.MaxOutputBytes < 0 || d.MaxOutputBytes > MaxVerificationOutputBytes {
		return invalidField(
			"ValidationCheckDefinition",
			"max_output_bytes",
			"输出上限超出允许范围",
		)
	}
	for _, code := range d.ExpectedExitCodes {
		if code < 0 || code > 255 {
			return invalidField(
				"ValidationCheckDefinition",
				"expected_exit_codes",
				"退出码必须在 0 到 255 之间",
			)
		}
	}
	return nil
}

// IsRequired 判断该检查是否属于必需检查。
//
// 禁用的检查不参与判定，因此永远不是"必需"的：否则可以通过禁用它绕过门槛。
func (d ValidationCheckDefinition) IsRequired() bool {
	return d.Enabled && d.Required
}

// AcceptsExitCode 判断给定退出码是否被视为通过。
func (d ValidationCheckDefinition) AcceptsExitCode(code int) bool {
	if len(d.ExpectedExitCodes) == 0 {
		return code == 0
	}
	for _, candidate := range d.ExpectedExitCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

// ValidationSpec 是一次验证的完整配置。
//
// 配置随验证结果一并保存快照，使之后可以解释"某次验证为什么通过或失败"，
// 而不必依赖当时可能已经变化的步骤配置。
type ValidationSpec struct {
	// Checks 是全部检查定义。
	Checks []ValidationCheckDefinition `json:"checks"`
	// DefaultTimeoutSeconds 是未声明超时的检查使用的超时秒数。
	DefaultTimeoutSeconds int `json:"default_timeout_seconds,omitempty"`
	// DefaultMaxOutputBytes 是未声明输出上限的检查使用的上限。
	DefaultMaxOutputBytes int `json:"default_max_output_bytes,omitempty"`
}

// Validate 校验验证规格。
func (s ValidationSpec) Validate() error {
	if len(s.Checks) > MaxValidationChecks {
		return invalidField("ValidationSpec", "checks", "检查数量超出上限")
	}
	seen := make(map[string]struct{}, len(s.Checks))
	sequences := make(map[int]struct{}, len(s.Checks))
	for _, check := range s.Checks {
		if err := check.Validate(); err != nil {
			return err
		}
		if _, duplicated := seen[check.ID]; duplicated {
			return invalidField("ValidationSpec", "checks", "检查标识重复: "+check.ID)
		}
		seen[check.ID] = struct{}{}
		if _, duplicated := sequences[check.Sequence]; duplicated {
			return invalidField("ValidationSpec", "checks", "检查执行顺序重复: "+itoa(check.Sequence))
		}
		sequences[check.Sequence] = struct{}{}
	}
	if s.DefaultTimeoutSeconds < 0 {
		return invalidField("ValidationSpec", "default_timeout_seconds", "超时时间不能为负数")
	}
	if s.DefaultMaxOutputBytes < 0 || s.DefaultMaxOutputBytes > MaxVerificationOutputBytes {
		return invalidField("ValidationSpec", "default_max_output_bytes", "输出上限超出允许范围")
	}
	return nil
}

// Ordered 返回按执行顺序排列的启用检查。
//
// 禁用的检查不参与执行；顺序由 Sequence 决定，与配置中的书写顺序无关。
func (s ValidationSpec) Ordered() []ValidationCheckDefinition {
	ordered := make([]ValidationCheckDefinition, 0, len(s.Checks))
	for _, check := range s.Checks {
		if !check.Enabled {
			continue
		}
		ordered = append(ordered, check)
	}
	// 检查数量有上限，插入排序足以保持稳定且不引入额外依赖。
	for index := 1; index < len(ordered); index++ {
		current := ordered[index]
		position := index - 1
		for position >= 0 && ordered[position].Sequence > current.Sequence {
			ordered[position+1] = ordered[position]
			position--
		}
		ordered[position+1] = current
	}
	return ordered
}

// HasRequiredCheck 判断是否存在启用的必需检查。
//
// 没有任何必需检查时不得判定通过：否则可以通过清空配置"通过"验证。
func (s ValidationSpec) HasRequiredCheck() bool {
	for _, check := range s.Checks {
		if check.IsRequired() {
			return true
		}
	}
	return false
}

// ValidationCheckResult 是一项检查的真实执行结果。
//
// 输出保存位置与摘要分开保存：数据库只存路径、状态与摘要，完整输出由文件承载，
// 避免大输出撑爆 SQLite 普通字段。
type ValidationCheckResult struct {
	// ID 是结果标识。
	ID ID `json:"id"`
	// ValidationRunID 是所属验证记录。
	ValidationRunID ID `json:"validation_run_id"`
	// CheckID 是检查定义的稳定标识。
	CheckID string `json:"check_id"`
	// CheckName 是检查名称快照。
	CheckName string `json:"check_name"`
	// Required 是检查是否必需的快照。
	Required bool `json:"required"`
	// Sequence 是执行顺序快照。
	Sequence int `json:"sequence"`
	// Definition 是检查配置快照。
	Definition ValidationCheckDefinition `json:"definition"`
	// Status 是检查状态。
	Status ValidationCheckStatus `json:"status"`
	// ExitCode 是实际退出码；进程未成功启动时为 -1。
	ExitCode int `json:"exit_code"`
	// StdoutPath 与 StderrPath 是完整输出的落盘位置，未落盘时为空。
	StdoutPath string `json:"stdout_path,omitempty"`
	StderrPath string `json:"stderr_path,omitempty"`
	// StdoutSummary 与 StderrSummary 是输出摘要，用于快速诊断。
	StdoutSummary string `json:"stdout_summary,omitempty"`
	StderrSummary string `json:"stderr_summary,omitempty"`
	// OutputTruncated 表示输出因超过上限被截断。
	OutputTruncated bool `json:"output_truncated,omitempty"`
	// OutputArchiveError 记录输出落盘失败的原因。
	//
	// 归档失败不改变命令的退出码与判定：两者是不同事实，必须分别记录。
	OutputArchiveError string `json:"output_archive_error,omitempty"`
	// TimedOut 与 Cancelled 分别表达超时与取消，二者互不替代。
	TimedOut  bool `json:"timed_out,omitempty"`
	Cancelled bool `json:"cancelled,omitempty"`
	// Certain 表示可以确认命令已经结束；为 false 时结果不可信。
	Certain bool `json:"certain"`
	// FailureReason 是未通过或异常的可诊断原因。
	FailureReason string `json:"failure_reason,omitempty"`
	// ErrorDetail 是执行层返回的原始错误摘要。
	ErrorDetail string `json:"error_detail,omitempty"`
	// Duration 是执行耗时。
	Duration time.Duration `json:"duration,omitempty"`
	// StartedAt 与 FinishedAt 是检查的执行时间。
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// CreatedAt 是结果记录创建时间。
	CreatedAt time.Time `json:"created_at"`
}

// Validate 校验检查结果。
func (r ValidationCheckResult) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.ValidationRunID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.CheckID) == "" {
		return invalidField("ValidationCheckResult", "check_id", "检查标识不能为空")
	}
	if strings.TrimSpace(r.CheckName) == "" {
		return invalidField("ValidationCheckResult", "check_name", "检查名称不能为空")
	}
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if r.Duration < 0 {
		return invalidField("ValidationCheckResult", "duration", "耗时不能为负数")
	}
	return nil
}

// BlocksSuccess 判断该检查是否阻止验证通过。
func (r ValidationCheckResult) BlocksSuccess() bool {
	return r.Required && !r.Status.Passed()
}

// ValidationRun 是一次验证的完整记录。
//
// 每次验证都创建独立记录，历史记录只追加不覆盖：重试不得复用旧记录，
// 也不得覆盖旧结论。
type ValidationRun struct {
	// ID 是验证记录标识。
	ID ID `json:"id"`
	// TaskID / StepID / AttemptID 是验证的归属链。
	TaskID    ID `json:"task_id"`
	StepID    ID `json:"step_id"`
	AttemptID ID `json:"attempt_id"`
	// Status 是验证流程状态。
	Status ValidationRunStatus `json:"status"`
	// Verdict 是最终结论，与状态分离。
	Verdict VerificationVerdict `json:"verdict"`
	// Summary 是结论摘要。
	Summary string `json:"summary,omitempty"`
	// FailureSummary 是失败或不确定结论的原因。
	FailureSummary string `json:"failure_summary,omitempty"`
	// Spec 是本次验证的配置快照。
	Spec ValidationSpec `json:"spec"`
	// SpecSnapshot 是配置的 JSON 快照，便于审计"当时到底跑了什么"。
	SpecSnapshot string `json:"spec_snapshot,omitempty"`
	// TotalChecks / PassedChecks / FailedChecks / SkippedChecks 是统计计数。
	TotalChecks   int `json:"total_checks"`
	PassedChecks  int `json:"passed_checks"`
	FailedChecks  int `json:"failed_checks"`
	SkippedChecks int `json:"skipped_checks"`
	// ExecutionEnv 是执行环境类型，例如 container。
	ExecutionEnv string `json:"execution_env,omitempty"`
	// Workspace 是执行命令的工作区标识，便于定位命令到底在哪执行。
	Workspace string `json:"workspace,omitempty"`
	// ContainerID 是执行本次验证的容器登记记录 ID，本机执行为空。
	ContainerID string `json:"container_id,omitempty"`
	// ValidatorSessionID 是可选 Validator Session 的标识。
	//
	// 它与确定性检查结果相互独立：Agent 结论不能改写命令退出码。
	ValidatorSessionID *ID `json:"validator_session_id,omitempty"`
	// StartedAt / FinishedAt 是验证的执行时间。
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Duration 是验证总耗时。
	Duration time.Duration `json:"duration,omitempty"`
	// ErrorDetail 是验证流程本身的错误（例如持久化失败）。
	ErrorDetail string `json:"error_detail,omitempty"`
	// CreatedAt / UpdatedAt 是记录时间。
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate 校验验证记录。
func (r ValidationRun) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.TaskID.Validate(); err != nil {
		return err
	}
	if err := r.StepID.Validate(); err != nil {
		return err
	}
	if err := r.AttemptID.Validate(); err != nil {
		return err
	}
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if err := r.Verdict.Validate(); err != nil {
		return err
	}
	if err := r.Spec.Validate(); err != nil {
		return err
	}
	if r.ValidatorSessionID != nil {
		if err := r.ValidatorSessionID.Validate(); err != nil {
			return err
		}
	}
	if r.Duration < 0 {
		return invalidField("ValidationRun", "duration", "耗时不能为负数")
	}
	if r.TotalChecks < 0 || r.PassedChecks < 0 || r.FailedChecks < 0 || r.SkippedChecks < 0 {
		return invalidField("ValidationRun", "total_checks", "统计计数不能为负数")
	}
	if r.PassedChecks+r.FailedChecks+r.SkippedChecks > r.TotalChecks {
		return invalidField("ValidationRun", "total_checks", "统计计数之和不能超过检查总数")
	}
	// 结论与状态必须自洽：未开始的验证不能给出结论，通过只能出现在 completed 上。
	if r.Status == ValidationRunStatusPending && r.Verdict != VerificationVerdictNone {
		return invalidField("ValidationRun", "verdict", "未开始的验证不能给出结论")
	}
	if r.Verdict == VerificationVerdictPassed && r.Status != ValidationRunStatusCompleted {
		return invalidField("ValidationRun", "verdict", "只有已完成的验证才能判定通过")
	}
	return nil
}

// Transition 将验证流程迁移到下一个状态，并记录更新时间。
func (r *ValidationRun) Transition(next ValidationRunStatus, at time.Time) error {
	if r == nil {
		return invalidField("ValidationRun", "", "实体为空")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := validationStatusMachine.validate(string(r.Status), string(next)); err != nil {
		return err
	}
	now := normalizeTime(at)
	r.Status = next
	r.UpdatedAt = now
	if next.IsTerminal() {
		r.FinishedAt = &now
	}
	return nil
}

// NewValidationRun 创建一条待执行的验证记录。
//
// 记录在命令执行之前写入：验证过程本身失败时也要有可诊断的痕迹，
// 不能出现"真实执行过但数据库里没有记录"。
func NewValidationRun(input ValidationRunInput, at time.Time) (ValidationRun, error) {
	now := normalizeTime(at)
	run := ValidationRun{
		ID:           NewID(),
		TaskID:       input.TaskID,
		StepID:       input.StepID,
		AttemptID:    input.AttemptID,
		Status:       ValidationRunStatusPending,
		Verdict:      VerificationVerdictNone,
		Spec:         input.Spec,
		SpecSnapshot: strings.TrimSpace(input.SpecSnapshot),
		TotalChecks:  len(input.Spec.Ordered()),
		ExecutionEnv: strings.TrimSpace(input.ExecutionEnv),
		Workspace:    strings.TrimSpace(input.Workspace),
		ContainerID:  strings.TrimSpace(input.ContainerID),
		ErrorDetail:  strings.TrimSpace(input.ErrorDetail),
		StartedAt:    now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := run.Validate(); err != nil {
		return ValidationRun{}, err
	}
	return run, nil
}

// ValidationRunInput 是创建验证记录的输入。
type ValidationRunInput struct {
	TaskID       ID
	StepID       ID
	AttemptID    ID
	Spec         ValidationSpec
	SpecSnapshot string
	ExecutionEnv string
	Workspace    string
	ContainerID  string
	ErrorDetail  string
}

// Validate 校验归属链是否自洽。
func (r ValidationRun) CheckScope(task Task, step Step, attempt Attempt) error {
	if task.Deleted() {
		return blockedValidation("任务 %s 已被删除", task.ID)
	}
	if step.TaskID != task.ID {
		return blockedValidation("步骤 %s 不属于任务 %s", step.ID, task.ID)
	}
	if attempt.StepID != step.ID {
		return blockedValidation("尝试 %s 不属于步骤 %s", attempt.ID, step.ID)
	}
	if !attempt.TaskID.IsEmpty() && attempt.TaskID != task.ID {
		return blockedValidation("尝试 %s 不属于任务 %s", attempt.ID, task.ID)
	}
	return nil
}

// ErrValidationBlocked 表示当前状态不允许执行验证。
//
// 它是"拒绝开始"的唯一语义：归属不符、状态不允许、尝试未结束与配置非法都通过
// 它表达，调用方据此返回状态冲突，而不是伪造一条已完成的验证记录。
var ErrValidationBlocked = fmt.Errorf("domain: 当前状态不允许执行验证")

// ErrValidationPrerequisite 表示验证所需的外部依赖尚未满足。
//
// 例如任务没有受管理的运行中容器。它必须在执行任何命令之前暴露，不允许退化为
// "在服务进程的当前目录里执行"。
var ErrValidationPrerequisite = fmt.Errorf("domain: 验证前置条件未满足")

// blockedValidation 构造带说明的状态拒绝错误。
func blockedValidation(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidationBlocked, fmt.Sprintf(format, args...))
}

// CanValidateAttempt 判断尝试状态是否允许进行验证。
//
// 只有真实执行过的尝试才能验证：running 表示执行尚未结束，unknown 与
// recovery_required 表示结果无法确认，三者都必须先收敛，不能直接验证。
func CanValidateAttempt(status AttemptStatus) bool {
	switch status {
	case AttemptStatusCompleted,
		AttemptStatusFailed,
		AttemptStatusCancelled,
		AttemptStatusTimedOut,
		AttemptStatusInterrupted:
		return true
	default:
		return false
	}
}

// ValidationTaskStatus 判断任务状态是否允许开始验证。
//
// 验证属于执行链的一环，因此只有正在执行或需要恢复核验的任务可以验证；
// queued 尚未准备环境，cancelling 与已结束状态一律拒绝。
func ValidationTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskStatusDeveloping,
		TaskStatusVerifying,
		TaskStatusRetrying,
		TaskStatusPaused,
		TaskStatusRecoveryRequired:
		return true
	default:
		return false
	}
}
