package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// ExecutePlanningRequest 是通过接口发起一次规划阶段执行的输入。
//
// 与 ExecuteAndValidateRequest 一样，关联标识全部来自经过校验的数据库记录：调用方
// 只能指定任务、步骤与可选的执行参数，不能指定 Session 路径、命令行参数或角色覆盖。
type ExecutePlanningRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// StepID 是待执行的规划阶段步骤标识。
	StepID string
	// Prompt 是可选的补充提示词，会追加在编排器生成的提示词之后。
	Prompt string
	// Timeout 是本次 Planner 调用的超时，零值使用任务配置或默认值。
	Timeout time.Duration
	// Grant 是本次执行持有的任务执行权凭据。
	//
	// 它只由应用层内部使用，不通过 HTTP 请求体暴露。
	Grant *TaskExecutionGrant
}

// ExecutePlanningResult 是一次规划阶段执行的结果。
//
// 它只描述"Planner 这次真实执行发生了什么"：规划阶段不跑确定性的代码验证，因此这里
// 没有验证流程状态、验证结论与修复轮次字段——"Agent 执行成功"不能被称为"代码验证
// 通过"，两件事必须有不同的表达。
type ExecutePlanningResult struct {
	TaskID domain.ID
	StepID domain.ID
	// AttemptID 是本次规划尝试；每次执行都创建新的 Attempt。
	AttemptID domain.ID
	// SessionID 是本次规划尝试对应的独立 Session。
	SessionID     domain.ID
	AttemptNumber int
	// Outcome 是本次 Planner 执行的结论。
	Outcome domain.StepExecutionOutcome
	// Summary 是执行结论摘要。
	Summary string
	// Failure 是执行失败原因，成功时为空。
	Failure string
	// ExitCode 是 Planner 进程退出码，未能取得时为 -1。
	ExitCode int
	// Finalized 表示执行记录与步骤终态是否完整写入。
	Finalized bool
	// FinalizationErrors 是写入失败的原因。
	FinalizationErrors []string
	// StepStatus 是收口后的步骤状态。
	StepStatus domain.StepStatus
	// TaskStatus 是收口后的任务状态。
	TaskStatus domain.TaskStatus
	// StartedAt / FinishedAt 是本次执行的开始与结束时间。
	StartedAt  time.Time
	FinishedAt time.Time
}

// ExecutePlanning 执行一个规划阶段步骤，并依据真实 Planner 执行结论收口步骤状态。
//
// 职责边界（与 ExecuteAndValidate 的区别）：
//   - 规划阶段不校验、也不要求验证配置：任务级必需命令依赖 Developer 的产出，在规划
//     阶段执行必然得到"文件还不存在"的失败；
//   - 不启动确定性验证、不生成 ValidationRun、不进入自动修复循环：规划失败如实停止，
//     不自动重放 Planner；
//   - 执行本身仍然是真实 Pi 调用：Attempt、Session、角色快照、Git 快照与归档状态全部
//     由 StepExecutor 写入，因此规划阶段有完整的可追溯证据；
//   - 步骤状态只由真实执行结论决定：成功进入 completed，失败、超时、取消与中断如实
//     写入，不存在"未执行却标记完成"的路径。
func (s *StepWorkflowService) ExecutePlanning(
	ctx context.Context,
	request ExecutePlanningRequest,
) (ExecutePlanningResult, error) {
	taskID, err := parseWorkflowID("任务", request.TaskID)
	if err != nil {
		return ExecutePlanningResult{}, err
	}
	stepID, err := parseWorkflowID("步骤", request.StepID)
	if err != nil {
		return ExecutePlanningResult{}, err
	}
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return ExecutePlanningResult{}, err
	}
	step, err := s.loadStep(ctx, task, stepID)
	if err != nil {
		return ExecutePlanningResult{}, err
	}
	// 类型校验先于任何副作用：开发步骤走 ExecuteAndValidate，不能被这条路径接管，
	// 否则一次误路由会让开发步骤跳过确定性验证。
	if step.Type != domain.StepTypePlan {
		return ExecutePlanningResult{}, fmt.Errorf(
			"%w: 步骤 %s 的类型是 %s，规划执行只接受 %s",
			ErrStepWorkflowInvalidConfig,
			step.ID,
			step.Type,
			domain.StepTypePlan,
		)
	}
	// 规划执行同样在任务执行权下进行：同一任务不会被另一个入口同时推进。
	grant, ownsGrant, grantErr := acquireTaskGrant(
		s.coordination,
		task.ID,
		request.Grant,
		ErrStepExecutionConflict,
	)
	if grantErr != nil {
		return ExecutePlanningResult{}, grantErr
	}
	if ownsGrant {
		defer s.coordination.Release(grant)
	}
	request.Grant = &grant
	prompt, err := stepExecutionPrompt(task, step, request.Prompt)
	if err != nil {
		return ExecutePlanningResult{}, err
	}
	runCtx, finishRun, runErr := s.beginRun(ctx)
	if runErr != nil {
		return ExecutePlanningResult{}, runErr
	}
	defer finishRun()

	release, acquired := s.running.acquire(step.ID)
	if !acquired {
		return ExecutePlanningResult{}, fmt.Errorf(
			"%w: 步骤 %s 正在执行规划阶段",
			ErrStepExecutionConflict,
			step.ID,
		)
	}
	defer release()

	executed, executeErr := s.steps.Execute(runCtx, StepExecuteRequest{
		TaskID:  task.ID.String(),
		StepID:  step.ID.String(),
		Prompt:  prompt,
		Timeout: request.Timeout,
		// 规划阶段不收口到开发—验证闭环：执行协调器按真实执行结论直接写入步骤终态，
		// 因此不存在"开发成功但仍在等待验证"的中间语义。
		DeferCompletion: false,
		Grant:           request.Grant,
	})
	if executeErr != nil {
		// 协调器返回错误意味着本次没有产生真实执行（状态冲突、前置条件缺失等）：
		// 错误必须原样返回，不能伪装成"执行后正常停止"。
		return ExecutePlanningResult{}, executeErr
	}
	result := planningResultOf(executed)
	s.logger.InfoContext(
		runCtx,
		"规划阶段执行已结束",
		slog.String("task_id", task.ID.String()),
		slog.String("step_id", step.ID.String()),
		slog.String("attempt_id", result.AttemptID.String()),
		slog.String("outcome", string(result.Outcome)),
		slog.String("step_status", string(result.StepStatus)),
		slog.Bool("finalized", result.Finalized),
	)
	return result, nil
}

// planningResultOf 把单步骤执行结果转换为规划阶段结果。
//
// 只搬运真实执行事实，不补充任何"通过"语义：没有验证记录就没有验证结论。
func planningResultOf(executed StepExecutionResult) ExecutePlanningResult {
	return ExecutePlanningResult{
		TaskID:             executed.TaskID,
		StepID:             executed.StepID,
		AttemptID:          executed.AttemptID,
		SessionID:          executed.SessionID,
		AttemptNumber:      executed.AttemptNumber,
		Outcome:            executed.Outcome,
		Summary:            executed.Summary,
		Failure:            executed.Failure,
		ExitCode:           executed.ExitCode,
		Finalized:          executed.Finalized,
		FinalizationErrors: executed.FinalizationErrors,
		StepStatus:         executed.StepStatus,
		TaskStatus:         executed.TaskStatus,
		StartedAt:          executed.StartedAt,
		FinishedAt:         executed.FinishedAt,
	}
}
