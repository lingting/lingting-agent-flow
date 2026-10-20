package domain

import (
	"errors"
	"fmt"
)

// ErrStepExecutionBlocked 表示当前任务或步骤状态不允许开始一次单步骤执行。
//
// 它是"拒绝开始"的唯一语义：占用冲突、状态不允许与步骤类型不匹配都通过它表达，
// 调用方据此返回状态冲突，而不是伪造一条正在运行的执行记录。
var ErrStepExecutionBlocked = errors.New("domain: 当前状态不允许执行步骤")

// StepExecutionOutcome 是一次单步骤执行的结论。
//
// 它只描述"本次 Agent 调用如何结束"，不代表步骤的验收结论，也不代表任务成功：
// completed 仅说明 Pi 进程正常退出，代码是否正确仍由后续验证阶段决定。
type StepExecutionOutcome string

// 单步骤执行结论取值。
const (
	// StepExecutionCompleted 表示 Agent 调用正常结束。
	StepExecutionCompleted StepExecutionOutcome = "completed"
	// StepExecutionFailed 表示执行失败，例如进程非零退出或启动失败。
	StepExecutionFailed StepExecutionOutcome = "failed"
	// StepExecutionTimedOut 表示调用超时后被终止。
	StepExecutionTimedOut StepExecutionOutcome = "timed_out"
	// StepExecutionCancelled 表示调用方取消了执行。
	StepExecutionCancelled StepExecutionOutcome = "cancelled"
	// StepExecutionInterrupted 表示服务关闭导致执行中断。
	StepExecutionInterrupted StepExecutionOutcome = "interrupted"
)

// Validate 校验执行结论是否属于已知取值。
func (o StepExecutionOutcome) Validate() error {
	switch o {
	case StepExecutionCompleted,
		StepExecutionFailed,
		StepExecutionTimedOut,
		StepExecutionCancelled,
		StepExecutionInterrupted:
		return nil
	default:
		return invalidField("StepExecution", "outcome", "未知的执行结论 "+string(o))
	}
}

// AttemptStatus 返回该结论对应的尝试终态。
//
// 迟到的结论不能改写已确认的终态：调用方必须先读取当前状态再决定是否写入。
func (o StepExecutionOutcome) AttemptStatus() AttemptStatus {
	switch o {
	case StepExecutionCompleted:
		return AttemptStatusCompleted
	case StepExecutionTimedOut:
		return AttemptStatusTimedOut
	case StepExecutionCancelled:
		return AttemptStatusCancelled
	case StepExecutionInterrupted:
		return AttemptStatusInterrupted
	default:
		return AttemptStatusFailed
	}
}

// CanStartExecution 判断步骤状态是否允许开始一次新的执行。
//
// 只有尚未进入执行、上一次尝试已结束或需要恢复核验的步骤可以重新开始；
// running、developing 与 verifying 表示已有执行占用，必须等待其收敛。
func (s StepStatus) CanStartExecution() bool {
	switch s {
	case StepStatusPending,
		StepStatusRetryWait,
		StepStatusFailed,
		StepStatusRecoveryRequired:
		return true
	default:
		return false
	}
}

// allowsStepExecution 判断任务状态是否允许推进到 developing 并执行步骤。
//
// 只列出能合法迁移到 developing 的状态：queued 需要先经过环境准备与代码获取，
// cancelling 与已结束状态不允许再启动新的执行。
func (s TaskStatus) AllowsStepExecution() bool {
	switch s {
	case TaskStatusPreparing,
		TaskStatusPlanned,
		TaskStatusDeveloping,
		TaskStatusRetrying,
		TaskStatusPaused,
		TaskStatusRecoveryRequired:
		return true
	default:
		return false
	}
}

// CheckStepExecutionStart 校验任务与步骤是否允许开始一次单步骤执行。
//
// 校验只读：它不修改任何状态，也不创建执行记录，因此可以在事务外先做快速失败。
// 真正的占用必须在事务内以状态条件更新完成，不能只依赖本函数的判断。
func CheckStepExecutionStart(task Task, step Step) error {
	if task.Deleted() {
		return blockedStepExecution("任务 %s 已被删除", task.ID)
	}
	if step.TaskID != task.ID {
		return blockedStepExecution("步骤 %s 不属于任务 %s", step.ID, task.ID)
	}
	if task.CancelRequested {
		return blockedStepExecution("任务 %s 已请求取消，不能开始新的执行", task.ID)
	}
	if !task.Status.AllowsStepExecution() {
		return blockedStepExecution(
			"任务 %s 当前状态 %s 不允许执行步骤；等待执行的任务需要先完成环境准备与计划",
			task.ID,
			task.Status,
		)
	}
	if !step.Status.CanStartExecution() {
		return blockedStepExecution(
			"步骤 %s 当前状态 %s 不允许开始执行",
			step.ID,
			step.Status,
		)
	}
	if step.CurrentAttemptID != nil {
		return blockedStepExecution("步骤 %s 已被执行尝试 %s 占用", step.ID, *step.CurrentAttemptID)
	}
	if step.Type.AgentRole() == "" {
		return blockedStepExecution(
			"步骤 %s 的类型 %s 不需要 Agent 调用，不能通过执行接口启动",
			step.ID,
			step.Type,
		)
	}
	if step.RoleID.IsEmpty() {
		return blockedStepExecution("步骤 %s 未配置执行角色", step.ID)
	}
	return nil
}

// blockedStepExecution 构造带说明的状态拒绝错误。
func blockedStepExecution(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrStepExecutionBlocked, fmt.Sprintf(format, args...))
}
