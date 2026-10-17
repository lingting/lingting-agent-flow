package agent

import (
	"fmt"
	"strings"
	"time"
)

// RunRequest 描述一次 Agent 执行所需的输入。
//
// 模型由角色配置解析后传入，执行层不决定使用哪个角色，也不实现任务级重试。
// 工作目录与 Session 根目录都由调用方显式给出：同一份执行逻辑既可以在本机运行，
// 也可以在容器内运行，执行层不硬编码任何本机路径。
type RunRequest struct {
	// SessionID 是 AgentFlow 内部创建的会话标识，用于关联日志、事件与 Session 文件。
	SessionID string
	// TaskID 是本次调用所属任务，用于确定 Session 文件的目录归属，可为空。
	TaskID string
	// StepID 是本次调用所属开发步骤，可为空。
	StepID string
	// AttemptID 是本次调用所属执行尝试，可为空。
	AttemptID string
	// RoleType 是执行本次调用的角色类型，只用于日志与诊断，可为空。
	RoleType string
	// Model 是 Pi CLI 支持的模型标识，例如 provider/model-id。
	Model string
	// SystemPrompt 是角色的系统提示词，非空时追加到 Pi 的默认系统提示词之后。
	SystemPrompt string
	// Prompt 是本次执行的完整提示词。
	Prompt string
	// WorkingDir 是执行时的工作目录，通常是任务的 Git Worktree。
	WorkingDir string
	// SessionRoot 是 Session 文件的本机持久化根目录，必须显式给出。
	SessionRoot string
	// Timeout 是本次调用的超时时间，零值表示使用执行器的默认超时。
	Timeout time.Duration
}

// Validate 校验执行输入。
//
// WorkingDir 允许为空以继承当前进程工作目录，其余字段必须显式给出。
func (r RunRequest) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return fmt.Errorf("agent: session_id 不能为空")
	}
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("agent: model 不能为空")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return fmt.Errorf("agent: prompt 不能为空")
	}
	if strings.TrimSpace(r.SessionRoot) == "" {
		return fmt.Errorf("agent: session_root 不能为空")
	}
	if r.Timeout < 0 {
		return fmt.Errorf("agent: timeout 不得为负数")
	}
	return nil
}
