package domain

import (
	"strings"
	"time"
)

// VerificationCheck 是一条人工可读的验收项检查结果。
type VerificationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Validate 校验验收项检查结果。
func (c VerificationCheck) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return invalidField("VerificationCheck", "name", "检查项名称不能为空")
	}
	return nil
}

// CommandResult 是一条验证命令的执行结果。
//
// 输出保留用于失败证据，不作为业务状态的唯一记录。
type CommandResult struct {
	Name     string        `json:"name"`
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	TimedOut bool          `json:"timed_out,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

// Passed 判断命令是否成功结束。
func (r CommandResult) Passed() bool {
	return r.ExitCode == 0 && !r.TimedOut
}

// Validate 校验命令执行结果。
func (r CommandResult) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return invalidField("CommandResult", "name", "命令名称不能为空")
	}
	if strings.TrimSpace(r.Command) == "" {
		return invalidField("CommandResult", "command", "命令内容不能为空")
	}
	if r.Duration < 0 {
		return invalidField("CommandResult", "duration", "耗时不能为负数")
	}
	return nil
}

// VerificationResult 是某次验证的结构化结果。
type VerificationResult struct {
	ID             ID                  `json:"id"`
	StepID         ID                  `json:"step_id"`
	AttemptID      ID                  `json:"attempt_id"`
	Passed         bool                `json:"passed"`
	Checks         []VerificationCheck `json:"checks,omitempty"`
	CommandResults []CommandResult     `json:"command_results,omitempty"`
	FailureSummary string              `json:"failure_summary,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
}

// NewVerificationResult 根据检查项与命令结果构建验证结果。
//
// passed 由检查项与命令结果推导，不接受外部直接指定；失败时要求提供失败摘要，
// 以便重试时作为证据传递给新的 Developer 会话。
func NewVerificationResult(
	stepID, attemptID ID,
	checks []VerificationCheck,
	commands []CommandResult,
	failureSummary string,
	at time.Time,
) (VerificationResult, error) {
	result := VerificationResult{
		ID:             NewID(),
		StepID:         stepID,
		AttemptID:      attemptID,
		Passed:         allPassed(checks, commands),
		Checks:         checks,
		CommandResults: commands,
		FailureSummary: strings.TrimSpace(failureSummary),
		CreatedAt:      normalizeTime(at),
	}
	if err := result.Validate(); err != nil {
		return VerificationResult{}, err
	}
	return result, nil
}

// Validate 校验验证结果定义是否完整。
func (r VerificationResult) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.StepID.Validate(); err != nil {
		return err
	}
	if err := r.AttemptID.Validate(); err != nil {
		return err
	}
	for _, check := range r.Checks {
		if err := check.Validate(); err != nil {
			return err
		}
	}
	for _, command := range r.CommandResults {
		if err := command.Validate(); err != nil {
			return err
		}
	}
	if !r.Passed && strings.TrimSpace(r.FailureSummary) == "" {
		return invalidField("VerificationResult", "failure_summary", "验证失败时必须提供失败摘要")
	}
	return nil
}

// FailedChecks 返回未通过的检查项名称。
func (r VerificationResult) FailedChecks() []string {
	names := make([]string, 0, len(r.Checks))
	for _, check := range r.Checks {
		if !check.Passed {
			names = append(names, check.Name)
		}
	}
	return names
}

// FailedCommands 返回未成功的命令结果名称。
func (r VerificationResult) FailedCommands() []string {
	names := make([]string, 0, len(r.CommandResults))
	for _, command := range r.CommandResults {
		if !command.Passed() {
			names = append(names, command.Name)
		}
	}
	return names
}

// allPassed 判断所有检查项与命令结果是否都通过。
func allPassed(checks []VerificationCheck, commands []CommandResult) bool {
	for _, check := range checks {
		if !check.Passed {
			return false
		}
	}
	for _, command := range commands {
		if !command.Passed() {
			return false
		}
	}
	return true
}
