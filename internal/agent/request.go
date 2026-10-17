package agent

import (
	"fmt"
	"strings"
)

// RunRequest 描述一次 Agent 执行所需的输入。
//
// 模型由角色配置解析后传入，执行层不决定使用哪个角色，也不实现任务级重试。
type RunRequest struct {
	// SessionID 是 AgentFlow 内部创建的会话标识，用于关联日志与事件。
	SessionID string
	// Model 是 Pi CLI 支持的模型标识，例如 provider/model-id。
	Model string
	// Prompt 是本次执行的完整提示词。
	Prompt string
	// WorkingDir 是执行时的工作目录，通常是任务的 Git Worktree。
	WorkingDir string
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
	return nil
}
