package domain

import (
	"strings"
	"time"
)

// StepStatus 表示计划中某一步骤的执行状态。
type StepStatus string

// 步骤状态。
const (
	StepStatusPending    StepStatus = "pending"
	StepStatusDeveloping StepStatus = "developing"
	StepStatusVerifying  StepStatus = "verifying"
	StepStatusCompleted  StepStatus = "completed"
	StepStatusFailed     StepStatus = "failed"
	StepStatusSkipped    StepStatus = "skipped"
)

// stepStatusMachine 定义步骤状态迁移。
//
// failed 允许回到 developing，用于验证失败后创建新的 Attempt 继续修复。
var stepStatusMachine = statusMachine{
	entity: "Step",
	transitions: map[string][]string{
		string(StepStatusPending): {
			string(StepStatusDeveloping),
			string(StepStatusSkipped),
		},
		string(StepStatusDeveloping): {
			string(StepStatusVerifying),
			string(StepStatusFailed),
		},
		string(StepStatusVerifying): {
			string(StepStatusCompleted),
			string(StepStatusFailed),
		},
		string(StepStatusFailed): {
			string(StepStatusDeveloping),
		},
	},
	terminal: []string{
		string(StepStatusCompleted),
		string(StepStatusSkipped),
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

// VerificationCommand 是验证阶段需要执行的一条命令。
type VerificationCommand struct {
	Name           string `json:"name"`
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// Validate 校验验证命令定义。
func (c VerificationCommand) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return invalidField("VerificationCommand", "name", "命令名称不能为空")
	}
	if strings.TrimSpace(c.Command) == "" {
		return invalidField("VerificationCommand", "command", "命令内容不能为空")
	}
	if c.TimeoutSeconds < 0 {
		return invalidField("VerificationCommand", "timeout_seconds", "超时时间不能为负数")
	}
	return nil
}

// VerificationSpec 描述某个步骤的验收标准与自动化验证命令。
type VerificationSpec struct {
	AcceptanceCriteria []string              `json:"acceptance_criteria,omitempty"`
	Commands           []VerificationCommand `json:"commands,omitempty"`
}

// Validate 校验验证规格。
func (s VerificationSpec) Validate() error {
	for _, criterion := range s.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return invalidField("VerificationSpec", "acceptance_criteria", "验收标准不能为空")
		}
	}
	for _, command := range s.Commands {
		if err := command.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// HasCommands 判断是否存在需要自动执行的验证命令。
func (s VerificationSpec) HasCommands() bool {
	return len(s.Commands) > 0
}

// Step 是计划中的一个业务步骤。
type Step struct {
	ID               ID               `json:"id"`
	TaskID           ID               `json:"task_id"`
	Sequence         int              `json:"sequence"`
	Title            string           `json:"title"`
	Objective        string           `json:"objective"`
	Instructions     string           `json:"instructions"`
	RoleID           ID               `json:"role_id"`
	RoleSnapshot     RoleSnapshot     `json:"role_snapshot"`
	VerificationSpec VerificationSpec `json:"verification_spec"`
	Status           StepStatus       `json:"status"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
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
	createdAt := normalizeTime(at)
	step := Step{
		ID:               NewID(),
		TaskID:           taskID,
		Sequence:         sequence,
		Title:            strings.TrimSpace(title),
		Objective:        strings.TrimSpace(objective),
		Instructions:     strings.TrimSpace(instructions),
		RoleID:           role.ID,
		RoleSnapshot:     role.Snapshot(),
		VerificationSpec: spec,
		Status:           StepStatusPending,
		CreatedAt:        createdAt,
		UpdatedAt:        createdAt,
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
	if err := s.RoleID.Validate(); err != nil {
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
	if err := s.RoleSnapshot.Validate(); err != nil {
		return err
	}
	if s.RoleSnapshot.RoleID != s.RoleID {
		return invalidField("Step", "role_snapshot", "角色快照与角色标识不一致")
	}
	if err := s.VerificationSpec.Validate(); err != nil {
		return err
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
	s.Status = next
	s.UpdatedAt = normalizeTime(at)
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
		if step.Status == StepStatusPending || step.Status == StepStatusFailed {
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
