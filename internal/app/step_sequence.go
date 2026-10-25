package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/domain"
)

// 顺序执行的默认参数。
const (
	// maxSequentialSteps 是单次顺序执行允许推进的步骤数量上限。
	//
	// 计划本身已有步骤数上限（domain.MaxPlanStepsLimit），这里再加一层硬限制，
	// 防止异常数据让一次 HTTP 请求无限期地占用执行资源。
	maxSequentialSteps = domain.MaxPlanStepsLimit
)

// ExecuteStepsRequest 是通过接口顺序执行开发步骤的输入。
//
// 关联标识只来自经过校验的数据库记录：调用方不能指定步骤顺序、Session 路径或
// 命令行参数，也不能要求跳过某个步骤。
type ExecuteStepsRequest struct {
	// TaskID 是任务标识。
	TaskID string
	// Prompt 是可选的补充说明，会追加在每一步骤的提示词之后。
	Prompt string
	// Timeout 是单个步骤调用的超时时间，零值使用任务配置或默认值。
	Timeout time.Duration
	// Grant 是本次顺序执行持有的任务执行权凭据。
	//
	// 顺序执行会连续推进多个步骤，每一步都修改同一份工作区与容器，因此任务租约
	// 必须覆盖整个调用周期：凭据非空时每一步的占用与任务终态写入都附带栅栏。
	//
	// 该字段只由应用层内部使用，不通过 HTTP 请求体暴露。
	Grant *WorkflowExecutionGrant
}

// StepSequenceResult 是一次顺序执行的结果。
//
// 它显式区分"每一步发生了什么"与"为什么停止"：调用方不需要再查执行记录就能判断
// 停止位置，同时仍然可以通过 AttemptID / SessionID 追踪完整证据。
type StepSequenceResult struct {
	// TaskID 是任务标识。
	TaskID domain.ID
	// Steps 是按执行顺序排列的步骤结果。
	Steps []StepSequenceStep
	// Stopped 表示是否因失败或中断提前停止。
	Stopped bool
	// StopReason 是停止原因摘要，未提前停止时为空。
	StopReason string
	// StoppedStepID 是触发停止的步骤，未提前停止时为空。
	StoppedStepID *domain.ID
	// TaskStatus 是执行结束后读取到的任务状态。
	TaskStatus domain.TaskStatus
	StartedAt  time.Time
	FinishedAt time.Time
}

// StepSequenceStep 是顺序执行中单个步骤的执行结果。
type StepSequenceStep struct {
	// StepID 是步骤标识。
	StepID domain.ID
	// Sequence 是步骤在计划中的序号。
	Sequence int
	// Title 是步骤标题。
	Title string
	// Result 是该步骤的执行结果，执行未开始时为空值。
	Result StepExecutionResult
	// Skipped 表示该步骤本次没有执行。
	Skipped bool
	// SkipReason 是跳过原因。
	SkipReason string
}

// ExecuteSteps 按计划顺序依次执行任务的开发步骤。
//
// 语义固定为：
//   - 只执行数据库中已存在的计划步骤，不生成、不补充计划；
//   - 每次只执行一个步骤，前一步骤结束后才开始下一步；
//   - 前一步骤失败、超时、取消或中断时立即停止，不自动重试，也不自动执行验证；
//   - 不跳过尚未完成的前置步骤（由 domain.CheckStepPrerequisites 拒绝）；
//   - 不提交、不推送、不合并代码。
//
// 执行是同步的：只有所有已执行步骤进入终态后才返回。单个步骤的成功不代表任务
// 通过验收：任务状态仍由单步骤执行协调器按既有规则推进。
func (e *StepExecutor) ExecuteSteps(
	ctx context.Context,
	request ExecuteStepsRequest,
) (StepSequenceResult, error) {
	taskID, err := parseStepExecutionID("任务", request.TaskID)
	if err != nil {
		return StepSequenceResult{}, err
	}
	task, err := e.loadTask(ctx, taskID)
	if err != nil {
		return StepSequenceResult{}, err
	}
	result := StepSequenceResult{TaskID: task.ID, TaskStatus: task.Status}
	if !task.Status.AllowsStepExecution() {
		return StepSequenceResult{}, fmt.Errorf(
			"%w: 任务 %s 当前状态 %s 不允许顺序执行步骤",
			ErrStepExecutionConflict,
			task.ID,
			task.Status,
		)
	}
	// 顺序执行在进程内以任务为键串行化：同一任务的两个顺序执行请求不能交错推进，
	// 否则后一个请求会在前一个请求刚完成一步时读到过期的步骤列表。
	release, acquired := e.sequences.acquire(task.ID)
	if !acquired {
		return StepSequenceResult{}, fmt.Errorf(
			"%w: 任务 %s 已有顺序执行在进行中",
			ErrStepExecutionConflict,
			task.ID,
		)
	}
	defer release()

	result.StartedAt = e.now()
	executed := 0
	for {
		steps, err := e.store.ListStepsByTask(ctx, task.ID)
		if err != nil {
			return result, fmt.Errorf("读取计划步骤失败: %w", err)
		}
		if len(steps) == 0 {
			result.FinishedAt = e.now()
			result.StopReason = "任务还没有已保存的开发计划步骤"
			return result, nil
		}
		step, ok, nextErr := domain.NextStepToExecute(steps)
		if nextErr != nil {
			// 现场不确定或已有执行占用：停止并如实报告，不跳过、不接管。
			result.Stopped = true
			result.StopReason = nextErr.Error()
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			return result, nil
		}
		if !ok {
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			return result, nil
		}
		if executed >= maxSequentialSteps {
			result.Stopped = true
			result.StopReason = fmt.Sprintf(
				"单次顺序执行最多推进 %d 个步骤，已停止",
				maxSequentialSteps,
			)
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			return result, nil
		}
		stepID := step.ID
		stepResult, runErr := e.Execute(ctx, StepExecuteRequest{
			TaskID:  task.ID.String(),
			StepID:  step.ID.String(),
			Prompt:  request.Prompt,
			Timeout: request.Timeout,
			Grant:   request.Grant,
		})
		executed++
		entry := StepSequenceStep{
			StepID:   step.ID,
			Sequence: step.Sequence,
			Title:    step.Title,
			Result:   stepResult,
		}
		if runErr != nil {
			entry.Skipped = true
			entry.SkipReason = runErr.Error()
			result.Steps = append(result.Steps, entry)
			result.Stopped = true
			result.StopReason = runErr.Error()
			result.StoppedStepID = &stepID
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			// 协调器返回错误意味着本次没有产生真实执行（状态冲突、前置条件缺失、
			// 目标不存在等）：错误必须原样返回，不能伪装成"执行后正常停止"。
			return result, runErr
		}
		result.Steps = append(result.Steps, entry)
		if stepResult.Outcome != domain.StepExecutionCompleted {
			// 失败、超时、取消与中断都必须停止：继续执行下一步只会在错误的代码
			// 基础上叠加改动。
			result.Stopped = true
			result.StoppedStepID = &stepID
			result.StopReason = stopReasonOf(stepResult)
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			return result, nil
		}
		if !stepResult.Finalized {
			result.Stopped = true
			result.StoppedStepID = &stepID
			result.StopReason = "步骤执行已结束但最终状态未能完整写入: " +
				strings.Join(stepResult.FinalizationErrors, "；")
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			result.Stopped = true
			result.StopReason = "调用方取消顺序执行: " + err.Error()
			result.FinishedAt = e.now()
			e.refreshTaskStatus(ctx, &result)
			return result, nil
		}
	}
}

// refreshTaskStatus 读取任务最新状态并写入结果。
//
// 读取失败只保留之前的状态：结果对象的其余字段仍然有效，调用方不会因为一次
// 状态查询失败而丢失已经完成的步骤结果。
func (e *StepExecutor) refreshTaskStatus(ctx context.Context, result *StepSequenceResult) {
	if result == nil {
		return
	}
	task, err := e.store.GetTask(context.WithoutCancel(ctx), result.TaskID)
	if err != nil {
		e.logger.WarnContext(
			ctx,
			"读取任务状态失败",
			slog.String("task_id", result.TaskID.String()),
			slog.String("error", errorSummary(err)),
		)
		return
	}
	result.TaskStatus = task.Status
}

// stopReasonOf 生成顺序执行的停止原因。
func stopReasonOf(result StepExecutionResult) string {
	detail := strings.TrimSpace(result.Failure)
	if detail == "" {
		detail = strings.TrimSpace(result.Summary)
	}
	if detail == "" {
		detail = "步骤执行未成功"
	}
	return "步骤执行结论为 " + string(result.Outcome) + ": " + detail
}
