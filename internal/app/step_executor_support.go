package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lingting/agentflow/internal/agent"
	"github.com/lingting/agentflow/internal/domain"
)

// digestStepExecution 生成本次尝试输入配置的摘要。
//
// 摘要只用于区分"同一配置的再次尝试"与"配置已变化的尝试"，不承载业务判断。
func digestStepExecution(task domain.Task, step domain.Step) string {
	payload, err := json.Marshal(struct {
		TaskConfig domain.TaskConfig
		StepID     string
		StepType   domain.StepType
		Objective  string
		RoleID     domain.ID
	}{
		TaskConfig: task.Config,
		StepID:     step.ID.String(),
		StepType:   step.Type,
		Objective:  step.Objective,
		RoleID:     step.RoleID,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// checkAgentStarted 判断执行器是否报告了可核验的真实运行痕迹。
//
// 完整会话代表进程确实运行并留下了记录；容器内执行不提供本机进程 ID，因此不能
// 只用 PID 判断是否运行过。既没有完整会话也没有进程标识的"成功"无法核验，
// 必须按失败处理，不能伪造完成。
func checkAgentStarted(run stepExecutionRun) error {
	if run.result.Status != agent.RunCompleted {
		return nil
	}
	if errorSummary(run.runErr) != "" {
		return nil
	}
	if run.result.Session.Completeness == agent.SessionComplete {
		return nil
	}
	if run.result.ProcessID > 0 {
		return nil
	}
	return fmt.Errorf("Agent 调用报告成功，但没有可核验的运行痕迹（会话不完整且缺少进程标识）")
}
