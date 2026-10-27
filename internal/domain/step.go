package domain

import (
	"strings"
	"time"
)

// StepType 是步骤在工作流中的阶段类型。
//
// 步骤类型决定该阶段由哪个组件执行：确定性验证与代码保存由 Go 直接执行，
// 规划、开发与辅助验证通过独立的 Agent 调用完成。
type StepType string

// 步骤类型取值。
const (
	// StepTypePrepare 表示准备执行环境（创建任务容器）。
	StepTypePrepare StepType = "prepare"
	// StepTypeSource 表示获取源代码并初始化任务分支。
	StepTypeSource StepType = "source"
	// StepTypePlan 表示 Planner 规划阶段。
	StepTypePlan StepType = "plan"
	// StepTypeDevelop 表示 Developer 执行阶段。
	StepTypeDevelop StepType = "develop"
	// StepTypeVerify 表示确定性验证阶段。
	StepTypeVerify StepType = "verify"
	// StepTypeReview 表示 Validator Agent 辅助评估阶段。
	StepTypeReview StepType = "review"
	// StepTypeSave 表示代码保存阶段（Commit / Push / 可选 Merge）。
	StepTypeSave StepType = "save"
	// StepTypeCleanup 表示任务清理阶段。
	StepTypeCleanup StepType = "cleanup"
)

// Validate 校验步骤类型是否受支持。
func (t StepType) Validate() error {
	switch t {
	case StepTypePrepare,
		StepTypeSource,
		StepTypePlan,
		StepTypeDevelop,
		StepTypeVerify,
		StepTypeReview,
		StepTypeSave,
		StepTypeCleanup:
		return nil
	default:
		return invalidField("Step", "step_type", "未知的步骤类型 "+string(t))
	}
}

// IsRequired 判断该阶段是否属于任务成功的必需阶段。
//
// 只有明确声明为可跳过的阶段才允许 skipped：必需验证与必需代码保存都不能
// 通过 skipped 伪装成成功。
func (t StepType) IsRequired() bool {
	switch t {
	case StepTypePrepare,
		StepTypeSource,
		StepTypePlan,
		StepTypeDevelop,
		StepTypeVerify,
		StepTypeSave:
		return true
	default:
		return false
	}
}

// AgentRole 返回该阶段使用的角色类型，不需要 Agent 的阶段返回空值。
func (t StepType) AgentRole() RoleType {
	switch t {
	case StepTypePlan:
		return RoleTypePlanner
	case StepTypeDevelop:
		return RoleTypeDeveloper
	case StepTypeReview:
		return RoleTypeValidator
	default:
		return ""
	}
}

// StepStatus 表示计划中某一步骤的执行状态。
type StepStatus string

// 步骤状态。
const (
	StepStatusPending          StepStatus = "pending"
	StepStatusRunning          StepStatus = "running"
	StepStatusDeveloping       StepStatus = "developing"
	StepStatusVerifying        StepStatus = "verifying"
	StepStatusCompleted        StepStatus = "completed"
	StepStatusFailed           StepStatus = "failed"
	StepStatusRetryWait        StepStatus = "retry_wait"
	StepStatusSkipped          StepStatus = "skipped"
	StepStatusCancelled        StepStatus = "cancelled"
	StepStatusRecoveryRequired StepStatus = "recovery_required"
)

// stepStatusMachine 定义步骤状态迁移。
//
// failed 允许回到 running，用于验证失败后创建新的 Attempt 继续修复；
// cancelled 与 recovery_required 只有在明确核验后才能重新进入执行。
var stepStatusMachine = statusMachine{
	entity: "Step",
	transitions: map[string][]string{
		string(StepStatusPending): {
			string(StepStatusRunning),
			string(StepStatusDeveloping),
			string(StepStatusSkipped),
			string(StepStatusCancelled),
			string(StepStatusRecoveryRequired),
		},
		string(StepStatusRunning): {
			string(StepStatusVerifying),
			string(StepStatusCompleted),
			string(StepStatusFailed),
			string(StepStatusRetryWait),
			string(StepStatusSkipped),
			string(StepStatusCancelled),
			string(StepStatusRecoveryRequired),
		},
		string(StepStatusDeveloping): {
			string(StepStatusVerifying),
			string(StepStatusCompleted),
			string(StepStatusFailed),
			string(StepStatusRetryWait),
			string(StepStatusCancelled),
			string(StepStatusRecoveryRequired),
		},
		string(StepStatusVerifying): {
			string(StepStatusCompleted),
			string(StepStatusFailed),
			string(StepStatusRetryWait),
			string(StepStatusCancelled),
			string(StepStatusRecoveryRequired),
		},
		string(StepStatusRetryWait): {
			string(StepStatusRunning),
			string(StepStatusDeveloping),
			string(StepStatusFailed),
			string(StepStatusCancelled),
			string(StepStatusRecoveryRequired),
		},
		string(StepStatusFailed): {
			string(StepStatusRunning),
			string(StepStatusDeveloping),
			string(StepStatusRetryWait),
			string(StepStatusCancelled),
			string(StepStatusRecoveryRequired),
		},
		string(StepStatusRecoveryRequired): {
			string(StepStatusRunning),
			string(StepStatusDeveloping),
			string(StepStatusFailed),
			string(StepStatusCancelled),
		},
	},
	terminal: []string{
		string(StepStatusCompleted),
		string(StepStatusSkipped),
		string(StepStatusCancelled),
	},
}

// Validate 校验步骤状态是否属于已知状态。
func (s StepStatus) Validate() error {
	if stepStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("Step", "status", "未知状态 "+string(s))
}

// IsTerminal 判断步骤状态是否为终态。
func (s StepStatus) IsTerminal() bool {
	return stepStatusMachine.isTerminal(string(s))
}

// BeginsExecution 判断迁移到该状态是否意味着"步骤真正开始执行"。
//
// 它与 IsTerminal 一起构成步骤时间语义的两端：
//   - 迁移到返回 true 的状态时记录 started_at；
//   - 迁移到终态时记录 finished_at。
//
// pending 只是计划创建后的等待状态，skipped 与 cancelled 可能在步骤从未执行时
// 发生，因此这三者都不表示开始执行。running 与 developing 是执行占用，
// verifying 是开发阶段结束后的验证阶段，三者都属于"已经真正开始执行"。
func (s StepStatus) BeginsExecution() bool {
	switch s {
	case StepStatusRunning, StepStatusDeveloping, StepStatusVerifying:
		return true
	default:
		return false
	}
}

// EndsExecution 判断迁移到该状态是否意味着"步骤本次执行周期结束"。
//
// 除终态外，retry_wait 也结束当前执行周期：它表示本次尝试已经得出失败结论并
// 等待下一次尝试，此后再次进入执行属于新的执行周期。failed 与
// recovery_required 同样结束当前周期，但它们允许重新进入执行，因此不是终态。
func (s StepStatus) EndsExecution() bool {
	if s.IsTerminal() {
		return true
	}
	switch s {
	case StepStatusRetryWait, StepStatusFailed, StepStatusRecoveryRequired:
		return true
	default:
		return false
	}
}

// VerificationCommand 是验证阶段需要执行的一条命令。
//
// Command 与 Args 分开保存：命令以参数切片形式通过容器执行接口传递，
// 不允许把整条命令交给 shell 解释，从而避免外部输入被当作 shell 语法执行。
type VerificationCommand struct {
	// Name 是命令标识，用于结果引用。
	Name string `json:"name"`
	// Command 是可执行文件名或绝对路径。
	Command string `json:"command"`
	// Args 是命令参数。
	Args []string `json:"args,omitempty"`
	// WorkDir 是容器内执行目录，空值表示任务工作目录。
	WorkDir string `json:"work_dir,omitempty"`
	// Required 表示该命令是否必需，默认必需。
	Required bool `json:"required"`
	// Optional 显式声明该命令可以不影响必需检查门槛。
	//
	// Required 与 Optional 同时为真属于非法配置，避免语义歧义。
	Optional bool `json:"optional,omitempty"`
	// TimeoutSeconds 是命令超时秒数，0 表示使用任务默认值。
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// AllowTimeoutPass 表示超时也视为通过，默认不允许。
	AllowTimeoutPass bool `json:"allow_timeout_pass,omitempty"`
	// AllowedExitCodes 是视为通过的退出码集合，空值等价于只允许 0。
	AllowedExitCodes []int `json:"allowed_exit_codes,omitempty"`
	// MaxOutputBytes 是保留的输出字节上限，0 表示使用默认值。
	MaxOutputBytes int `json:"max_output_bytes,omitempty"`
}

// Validate 校验验证命令定义。
func (c VerificationCommand) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return invalidField("VerificationCommand", "name", "命令名称不能为空")
	}
	if len(c.Name) > 128 {
		return invalidField("VerificationCommand", "name", "命令名称不能超过 128 个字符")
	}
	if err := validateCommandToken("VerificationCommand", "command", c.Command); err != nil {
		return err
	}
	if len(c.Command) > MaxCommandLength {
		return invalidField("VerificationCommand", "command", "命令内容过长")
	}
	for index, arg := range c.Args {
		if strings.ContainsRune(arg, 0) {
			return invalidField(
				"VerificationCommand",
				"args",
				"第 "+itoa(index+1)+" 个参数包含非法字符",
			)
		}
	}
	if workDir := strings.TrimSpace(c.WorkDir); workDir != "" {
		if err := ValidateContainerWorkDir(workDir); err != nil {
			return err
		}
	}
	if c.Optional && c.Required {
		return invalidField(
			"VerificationCommand",
			"required",
			"命令不能同时声明为必需与可选",
		)
	}
	if c.TimeoutSeconds < 0 {
		return invalidField("VerificationCommand", "timeout_seconds", "超时时间不能为负数")
	}
	if c.MaxOutputBytes < 0 || c.MaxOutputBytes > MaxVerificationOutputBytes {
		return invalidField("VerificationCommand", "max_output_bytes", "输出上限超出允许范围")
	}
	for _, code := range c.AllowedExitCodes {
		if code < 0 || code > 255 {
			return invalidField("VerificationCommand", "allowed_exit_codes", "退出码必须在 0 到 255 之间")
		}
	}
	return nil
}

// IsRequired 判断该命令是否属于必需检查。
//
// 显式 Optional 优先于默认值：未声明任何标记时默认必需，因此缺少标记的命令
// 不会被当作可忽略的检查。
func (c VerificationCommand) IsRequired() bool {
	if c.Optional {
		return false
	}
	return true
}

// AcceptsExitCode 判断给定退出码是否被视为通过。
func (c VerificationCommand) AcceptsExitCode(code int) bool {
	if len(c.AllowedExitCodes) == 0 {
		return code == 0
	}
	for _, candidate := range c.AllowedExitCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

// validateCommandToken 校验命令片段不含 shell 元字符与控制字符。
//
// 命令通过参数切片传递，不经过 shell；这里仍然拒绝空白与 shell 元字符，
// 避免把整条命令误当成可执行文件名或隐藏拼接行为。
func validateCommandToken(entity, field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return invalidField(entity, field, "命令内容不能为空")
	}
	if trimmed != value {
		return invalidField(entity, field, "命令内容首尾不能包含空白")
	}
	for _, char := range value {
		switch char {
		case 0, '\n', '\r', '\t', ' ':
			return invalidField(entity, field, "命令内容不能包含空白或控制字符")
		}
	}
	return nil
}

// itoa 把整数转换为十进制字符串，避免为一条错误信息引入额外依赖。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buffer := make([]byte, 0, 8)
	for value > 0 {
		buffer = append([]byte{byte('0' + value%10)}, buffer...)
		value /= 10
	}
	if negative {
		return "-" + string(buffer)
	}
	return string(buffer)
}

// VerificationSpec 描述某个步骤的验收标准与自动化验证命令。
type VerificationSpec struct {
	AcceptanceCriteria []string              `json:"acceptance_criteria,omitempty"`
	Commands           []VerificationCommand `json:"commands,omitempty"`
}

// Validate 校验验证规格。
func (s VerificationSpec) Validate() error {
	if len(s.AcceptanceCriteria) > MaxAcceptanceCriteria {
		return invalidField("VerificationSpec", "acceptance_criteria", "验收条件数量超出上限")
	}
	for _, criterion := range s.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return invalidField("VerificationSpec", "acceptance_criteria", "验收标准不能为空")
		}
		if len(criterion) > 1024 {
			return invalidField("VerificationSpec", "acceptance_criteria", "验收标准过长")
		}
	}
	if len(s.Commands) > MaxVerificationCommands {
		return invalidField("VerificationSpec", "commands", "验证命令数量超出上限")
	}
	seen := make(map[string]struct{}, len(s.Commands))
	for _, command := range s.Commands {
		if err := command.Validate(); err != nil {
			return err
		}
		if _, duplicated := seen[command.Name]; duplicated {
			return invalidField("VerificationSpec", "commands", "验证命令名称重复: "+command.Name)
		}
		seen[command.Name] = struct{}{}
	}
	return nil
}

// HasCommands 判断是否存在需要自动执行的验证命令。
func (s VerificationSpec) HasCommands() bool {
	return len(s.Commands) > 0
}

// Merge 合并两个验证规格：命令按名称去重，验收条件拼接后去重。
//
// 任务级必需命令与步骤级命令合并后，步骤不能通过覆盖同名命令来取消任务级
// 必需检查：任务级命令优先保留。
func (s VerificationSpec) Merge(step VerificationSpec) VerificationSpec {
	merged := VerificationSpec{}
	seenCriteria := make(map[string]struct{})
	for _, source := range [][]string{s.AcceptanceCriteria, step.AcceptanceCriteria} {
		for _, criterion := range source {
			trimmed := strings.TrimSpace(criterion)
			if trimmed == "" {
				continue
			}
			if _, duplicated := seenCriteria[trimmed]; duplicated {
				continue
			}
			seenCriteria[trimmed] = struct{}{}
			merged.AcceptanceCriteria = append(merged.AcceptanceCriteria, trimmed)
		}
	}
	seenCommands := make(map[string]struct{})
	for _, source := range [][]VerificationCommand{s.Commands, step.Commands} {
		for _, command := range source {
			name := strings.TrimSpace(command.Name)
			if _, duplicated := seenCommands[name]; duplicated {
				continue
			}
			seenCommands[name] = struct{}{}
			merged.Commands = append(merged.Commands, command)
		}
	}
	return merged
}

// Step 是任务中的一个可追踪执行阶段。
type Step struct {
	ID        ID       `json:"id"`
	TaskID    ID       `json:"task_id"`
	Sequence  int      `json:"sequence"`
	Type      StepType `json:"step_type"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	// Instructions 是该步骤交给 Agent 的附加要求。
	Instructions string `json:"instructions"`
	// RoleID 是执行该步骤的角色；不需要 Agent 的阶段为空。
	RoleID ID `json:"role_id,omitempty"`
	// RoleSnapshot 是计划生成时的角色配置快照。
	RoleSnapshot RoleSnapshot `json:"role_snapshot,omitempty"`
	// VerificationSpec 是该步骤的验收标准与验证命令。
	VerificationSpec VerificationSpec `json:"verification_spec"`
	// Retry 是该步骤的重试策略；零值表示继承任务级策略。
	Retry RetryConfig `json:"retry,omitempty"`
	// MaxAttempts 是生效的尝试次数上限。
	MaxAttempts int `json:"max_attempts"`
	// CurrentAttemptID 是当前正在执行的尝试。
	CurrentAttemptID *ID        `json:"current_attempt_id,omitempty"`
	Status           StepStatus `json:"status"`
	// Result 是该步骤的最终结果说明，对应清单中的 output_summary。
	Result string `json:"result,omitempty"`
	// FailureStage 是失败发生的阶段名，用于定位。
	FailureStage string `json:"failure_stage,omitempty"`
	// FailureSummary 是失败原因摘要，对应清单中的 error。
	//
	// 与 FailureStage 分开保存：阶段名用于定位"哪一步出错"，摘要用于说明"出了什么错"。
	FailureSummary string     `json:"failure_summary,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// NewStep 创建并校验步骤。
//
// role 是计划生成时实际采用的角色，其快照会随步骤一并持久化，避免角色日后修改后
// 历史任务的配置失去可追溯性。
func NewStep(
	taskID ID,
	sequence int,
	title, objective, instructions string,
	role Role,
	spec VerificationSpec,
	at time.Time,
) (Step, error) {
	step := Step{
		ID:               NewID(),
		TaskID:           taskID,
		Sequence:         sequence,
		Type:             StepTypeDevelop,
		Title:            strings.TrimSpace(title),
		Objective:        strings.TrimSpace(objective),
		Instructions:     strings.TrimSpace(instructions),
		RoleID:           role.ID,
		RoleSnapshot:     role.Snapshot(),
		VerificationSpec: spec,
		MaxAttempts:      DefaultMaxAttempts,
		Status:           StepStatusPending,
		CreatedAt:        normalizeTime(at),
		UpdatedAt:        normalizeTime(at),
	}
	if err := step.Validate(); err != nil {
		return Step{}, err
	}
	return step, nil
}

// NewWorkflowStep 创建工作流阶段的步骤。
//
// 非 Agent 阶段（环境准备、代码获取、确定性验证、代码保存、清理）不携带角色，
// 因此 RoleID 与角色快照为空。
func NewWorkflowStep(
	taskID ID,
	sequence int,
	stepType StepType,
	title, objective string,
	spec VerificationSpec,
	maxAttempts int,
	at time.Time,
) (Step, error) {
	step := Step{
		ID:               NewID(),
		TaskID:           taskID,
		Sequence:         sequence,
		Type:             stepType,
		Title:            strings.TrimSpace(title),
		Objective:        strings.TrimSpace(objective),
		VerificationSpec: spec,
		MaxAttempts:      maxAttempts,
		Status:           StepStatusPending,
		CreatedAt:        normalizeTime(at),
		UpdatedAt:        normalizeTime(at),
	}
	if err := step.Validate(); err != nil {
		return Step{}, err
	}
	return step, nil
}

// NewAgentStep 创建需要 Agent 调用的工作流步骤。
func NewAgentStep(
	taskID ID,
	sequence int,
	stepType StepType,
	title, objective, instructions string,
	role Role,
	spec VerificationSpec,
	retry RetryConfig,
	at time.Time,
) (Step, error) {
	step := Step{
		ID:               NewID(),
		TaskID:           taskID,
		Sequence:         sequence,
		Type:             stepType,
		Title:            strings.TrimSpace(title),
		Objective:        strings.TrimSpace(objective),
		Instructions:     strings.TrimSpace(instructions),
		RoleID:           role.ID,
		RoleSnapshot:     role.Snapshot(),
		VerificationSpec: spec,
		Retry:            retry,
		MaxAttempts:      retry.Limit(),
		Status:           StepStatusPending,
		CreatedAt:        normalizeTime(at),
		UpdatedAt:        normalizeTime(at),
	}
	if err := step.Validate(); err != nil {
		return Step{}, err
	}
	return step, nil
}

// Validate 校验步骤定义是否完整。
func (s Step) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return err
	}
	if err := s.TaskID.Validate(); err != nil {
		return err
	}
	if err := s.Type.Validate(); err != nil {
		return err
	}
	if s.Sequence < 1 {
		return invalidField("Step", "sequence", "序号必须从 1 开始")
	}
	if strings.TrimSpace(s.Title) == "" {
		return invalidField("Step", "title", "标题不能为空")
	}
	if strings.TrimSpace(s.Objective) == "" {
		return invalidField("Step", "objective", "目标不能为空")
	}
	if s.Type.AgentRole() == "" {
		if !s.RoleID.IsEmpty() {
			return invalidField("Step", "role_id", "非 Agent 阶段不能关联角色")
		}
	} else {
		if err := s.RoleID.Validate(); err != nil {
			return err
		}
		if err := s.RoleSnapshot.Validate(); err != nil {
			return err
		}
		if s.RoleSnapshot.RoleID != s.RoleID {
			return invalidField("Step", "role_snapshot", "角色快照与角色标识不一致")
		}
		if s.RoleSnapshot.Type != s.Type.AgentRole() {
			return invalidField("Step", "role_snapshot", "角色类型与步骤类型不一致")
		}
	}
	if err := s.VerificationSpec.Validate(); err != nil {
		return err
	}
	if err := s.Retry.Validate(); err != nil {
		return err
	}
	if s.MaxAttempts < 1 || s.MaxAttempts > MaxTaskMaxAttempts {
		return invalidField("Step", "max_attempts", "尝试次数上限必须在 1 到 10 之间")
	}
	if s.CurrentAttemptID != nil {
		if err := s.CurrentAttemptID.Validate(); err != nil {
			return err
		}
	}
	return s.Status.Validate()
}

// Transition 将步骤迁移到下一个状态，并记录更新时间。
func (s *Step) Transition(next StepStatus, at time.Time) error {
	if s == nil {
		return invalidField("Step", "", "实体为空")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := stepStatusMachine.validate(string(s.Status), string(next)); err != nil {
		return err
	}
	now := normalizeTime(at)
	s.Status = next
	s.UpdatedAt = now
	// 时间语义：started_at 只在步骤真正开始执行时写入，pending 之后被跳过或直接取消
	// 都不会产生开始时间；finished_at 只在本轮执行周期结束时写入。
	//
	// 一旦步骤重新进入执行（例如 failed 之后显式重新执行），就开启新的执行周期：
	// finished_at 被清空，started_at 保留首次真正进入执行的时间，因此这两个字段
	// 分别表达"这个步骤第一次开始执行"与"当前执行周期何时结束"。这样既不会把不同
	// 周期混成一条记录，也不会覆盖首次开始时间这一历史事实。
	if next.BeginsExecution() {
		if s.StartedAt == nil {
			s.StartedAt = &now
		}
		s.FinishedAt = nil
	} else if next.EndsExecution() {
		s.FinishedAt = &now
	}
	return nil
}

// SetCurrentAttempt 记录步骤当前正在执行的尝试。
func (s *Step) SetCurrentAttempt(attemptID *ID) error {
	if s == nil {
		return invalidField("Step", "", "实体为空")
	}
	if attemptID != nil {
		if err := attemptID.Validate(); err != nil {
			return err
		}
	}
	s.CurrentAttemptID = attemptID
	return nil
}

// Plan 是 Planner 生成的、可持久化的结构化开发计划。
//
// Plan 只是步骤集合的聚合视图，步骤本身才是持久化单元。
type Plan struct {
	TaskID  ID     `json:"task_id"`
	Summary string `json:"summary,omitempty"`
	Steps   []Step `json:"steps"`
}

// NewPlan 创建并校验开发计划。
func NewPlan(taskID ID, summary string, steps []Step) (Plan, error) {
	plan := Plan{
		TaskID:  taskID,
		Summary: strings.TrimSpace(summary),
		Steps:   steps,
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// Validate 校验计划是否完整：步骤非空、同属一个任务且序号连续递增。
func (p Plan) Validate() error {
	if err := p.TaskID.Validate(); err != nil {
		return err
	}
	if len(p.Steps) == 0 {
		return invalidField("Plan", "steps", "计划至少包含一个步骤")
	}
	for index, step := range p.Steps {
		if err := step.Validate(); err != nil {
			return err
		}
		if step.TaskID != p.TaskID {
			return invalidField("Plan", "steps", "步骤必须属于同一个任务")
		}
		if step.Sequence != index+1 {
			return invalidField("Plan", "steps", "步骤序号必须从 1 开始连续递增")
		}
	}
	return nil
}

// NextPendingStep 返回序号最小的待执行步骤。
func (p Plan) NextPendingStep() (Step, bool) {
	for _, step := range p.Steps {
		switch step.Status {
		case StepStatusPending, StepStatusFailed, StepStatusRetryWait, StepStatusRecoveryRequired:
			return step, true
		}
	}
	return Step{}, false
}

// AllPassed 判断计划中的步骤是否全部通过或被跳过。
func (p Plan) AllPassed() bool {
	for _, step := range p.Steps {
		if !step.Status.IsTerminal() {
			return false
		}
	}
	return true
}
