package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WorkflowRunStatus 表示一次任务级工作流执行的运行状态。
//
// 它与 TaskStatus 是不同层次的概念，不能互相替代：
//   - TaskStatus 描述任务的业务阶段（是否正在开发、是否已完成）；
//   - WorkflowRunStatus 描述"这一次编排执行"是否在推进、是否被阻塞、是否需要恢复。
//
// 同一条任务可以有多次工作流执行记录（失败后重新开始、阻塞后恢复），历史记录一律保留。
type WorkflowRunStatus string

// 工作流执行状态取值。
const (
	// WorkflowRunPending 表示已登记但尚未开始推进。
	WorkflowRunPending WorkflowRunStatus = "pending"
	// WorkflowRunRunning 表示正在推进。
	WorkflowRunRunning WorkflowRunStatus = "running"
	// WorkflowRunBlocked 表示因步骤失败或前置条件未满足而停止自动推进。
	//
	// 它不是终态：现场仍然可用，用户可以在核验后显式恢复。
	WorkflowRunBlocked WorkflowRunStatus = "blocked"
	// WorkflowRunCancelRequested 表示已记录取消请求，正在等待实际中断。
	WorkflowRunCancelRequested WorkflowRunStatus = "cancel_requested"
	// WorkflowRunCancelled 表示取消已经完成。
	WorkflowRunCancelled WorkflowRunStatus = "cancelled"
	// WorkflowRunCompleted 表示全部必需阶段已完成。
	WorkflowRunCompleted WorkflowRunStatus = "completed"
	// WorkflowRunFailed 表示发生确定性失败并按规则终止。
	WorkflowRunFailed WorkflowRunStatus = "failed"
	// WorkflowRunRecoveryRequired 表示存在无法自动确定的执行结果或资源状态。
	WorkflowRunRecoveryRequired WorkflowRunStatus = "recovery_required"
)

// workflowRunStatusMachine 定义工作流执行状态的迁移。
//
// 关键约束：
//   - completed、cancelled 与 failed 是终态，不会被恢复流程重新打开；
//   - blocked 与 recovery_required 允许回到 running，用于用户显式恢复；
//   - cancel_requested 只能收敛为 cancelled、failed 或 recovery_required，
//     不允许直接回到 running：取消请求一旦记录，就必须先确认现场再决定。
var workflowRunStatusMachine = statusMachine{
	entity: "WorkflowRun",
	transitions: map[string][]string{
		string(WorkflowRunPending): {
			string(WorkflowRunRunning),
			string(WorkflowRunBlocked),
			string(WorkflowRunCancelRequested),
			string(WorkflowRunCancelled),
			string(WorkflowRunFailed),
			string(WorkflowRunRecoveryRequired),
		},
		string(WorkflowRunRunning): {
			string(WorkflowRunBlocked),
			string(WorkflowRunCancelRequested),
			string(WorkflowRunCompleted),
			string(WorkflowRunFailed),
			string(WorkflowRunRecoveryRequired),
		},
		string(WorkflowRunBlocked): {
			string(WorkflowRunRunning),
			string(WorkflowRunCancelRequested),
			string(WorkflowRunCancelled),
			string(WorkflowRunFailed),
			string(WorkflowRunRecoveryRequired),
		},
		string(WorkflowRunRecoveryRequired): {
			string(WorkflowRunRunning),
			string(WorkflowRunCancelRequested),
			string(WorkflowRunCancelled),
			string(WorkflowRunFailed),
		},
		string(WorkflowRunCancelRequested): {
			string(WorkflowRunCancelled),
			string(WorkflowRunFailed),
			string(WorkflowRunRecoveryRequired),
		},
	},
	terminal: []string{
		string(WorkflowRunCompleted),
		string(WorkflowRunCancelled),
		string(WorkflowRunFailed),
	},
}

// Validate 校验工作流执行状态是否属于已知状态。
func (s WorkflowRunStatus) Validate() error {
	if workflowRunStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("WorkflowRun", "status", "未知状态 "+string(s))
}

// IsTerminal 判断工作流执行是否已经结束。
//
// 只有 completed、cancelled 与 failed 是终态；blocked 与 recovery_required 仍然
// 需要用户介入，不能被当作"已结束"。
func (s WorkflowRunStatus) IsTerminal() bool {
	return workflowRunStatusMachine.isTerminal(string(s))
}

// IsActive 判断工作流执行是否仍在推进。
func (s WorkflowRunStatus) IsActive() bool {
	switch s {
	case WorkflowRunRunning, WorkflowRunCancelRequested:
		return true
	default:
		return false
	}
}

// CanStart 判断该状态是否允许开始（或恢复）推进。
func (s WorkflowRunStatus) CanStart() bool {
	switch s {
	case WorkflowRunPending, WorkflowRunBlocked, WorkflowRunRecoveryRequired:
		return true
	default:
		return false
	}
}

// WorkflowStage 表示工作流当前所处的执行阶段。
//
// 阶段只用于定位"中断发生在哪一步"，不参与成功判定。
type WorkflowStage string

// 工作流执行阶段取值。
const (
	// WorkflowStageWaiting 表示尚未进入任何执行阶段。
	WorkflowStageWaiting WorkflowStage = "waiting"
	// WorkflowStageStepExecution 表示正在执行某个开发步骤（含其验证）。
	WorkflowStageStepExecution WorkflowStage = "step_execution"
	// WorkflowStageCodeSave 表示正在保存代码。
	WorkflowStageCodeSave WorkflowStage = "code_save"
	// WorkflowStageFinalize 表示正在做任务收尾。
	WorkflowStageFinalize WorkflowStage = "finalize"
	// WorkflowStageStopped 表示已停止推进。
	WorkflowStageStopped WorkflowStage = "stopped"
)

// Validate 校验工作流执行阶段是否属于已知取值。
func (s WorkflowStage) Validate() error {
	switch s {
	case WorkflowStageWaiting,
		WorkflowStageStepExecution,
		WorkflowStageCodeSave,
		WorkflowStageFinalize,
		WorkflowStageStopped:
		return nil
	default:
		return invalidField("WorkflowRun", "stage", "未知阶段 "+string(s))
	}
}

// WorkflowRun 是一次任务级工作流执行的持久化记录。
//
// 它是"编排进度"的唯一真相来源：服务重启后据此判断停在哪一步，而不是靠内存状态。
// 记录不保存完整计划内容，只保存计划摘要与当前游标，计划本身仍然是 steps 表。
type WorkflowRun struct {
	ID ID `json:"id"`
	// TaskID 是所属任务。
	TaskID ID `json:"task_id"`
	// PlanDigest 是本次执行所用计划的内容摘要。
	//
	// 计划在恢复时必须与登记时一致，否则说明计划已被重新生成，不能沿用旧游标。
	PlanDigest string `json:"plan_digest"`
	// Status 是工作流执行状态。
	Status WorkflowRunStatus `json:"status"`
	// Stage 是当前执行阶段。
	Stage WorkflowStage `json:"stage"`
	// CurrentStepID 是当前正在执行的步骤。
	CurrentStepID *ID `json:"current_step_id,omitempty"`
	// CurrentAttemptID 是当前正在执行的尝试。
	CurrentAttemptID *ID `json:"current_attempt_id,omitempty"`
	// LastCompletedStepID 是最后一个已确认完成的步骤。
	LastCompletedStepID *ID `json:"last_completed_step_id,omitempty"`
	// LastCompletedSequence 是最后一个已确认完成步骤的序号。
	LastCompletedSequence int `json:"last_completed_sequence"`
	// CompletedSteps 是本次执行已确认完成的步骤数量。
	CompletedSteps int `json:"completed_steps"`
	// TotalSteps 是本次执行登记的开发步骤总数。
	TotalSteps int `json:"total_steps"`
	// CancelRequested 表示已请求取消。
	CancelRequested bool `json:"cancel_requested"`
	// CancelRequestedAt 是取消请求时间。
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	// WorkerID 是当前持有执行权的进程标识。
	WorkerID string `json:"worker_id,omitempty"`
	// LeaseExpiresAt 是执行权到期时间，用于识别已退出进程留下的残留。
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	// ErrorClass 是最近一次停止的错误分类。
	ErrorClass string `json:"error_class,omitempty"`
	// ErrorSummary 是最近一次停止的原因摘要。
	ErrorSummary string `json:"error_summary,omitempty"`
	// Revision 是乐观并发版本号：恢复不得覆盖更新的状态。
	Revision int64 `json:"revision"`
	// StartedAt 是首次开始推进的时间。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// FinishedAt 是进入终态的时间；未结束的执行为空。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// NewWorkflowRun 创建一条待执行的工作流记录。
//
// 计划摘要与步骤总数在创建时确定：它们描述"这一次执行基于哪份计划"，恢复时用于
// 判断计划是否已经变化，避免用旧游标推进新计划。
func NewWorkflowRun(
	taskID ID,
	steps []Step,
	at time.Time,
) (WorkflowRun, error) {
	if err := taskID.Validate(); err != nil {
		return WorkflowRun{}, err
	}
	digest, err := WorkflowPlanDigest(steps)
	if err != nil {
		return WorkflowRun{}, err
	}
	now := normalizeTime(at)
	run := WorkflowRun{
		ID:             NewID(),
		TaskID:         taskID,
		PlanDigest:     digest,
		Status:         WorkflowRunPending,
		Stage:          WorkflowStageWaiting,
		TotalSteps:     len(developSteps(steps)),
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision:       0,
		CompletedSteps: 0,
	}
	if err := run.Validate(); err != nil {
		return WorkflowRun{}, err
	}
	return run, nil
}

// Validate 校验工作流记录是否完整。
func (r WorkflowRun) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.TaskID.Validate(); err != nil {
		return err
	}
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if err := r.Stage.Validate(); err != nil {
		return err
	}
	for _, id := range []*ID{r.CurrentStepID, r.CurrentAttemptID, r.LastCompletedStepID} {
		if id == nil {
			continue
		}
		if err := id.Validate(); err != nil {
			return err
		}
	}
	if r.LastCompletedSequence < 0 {
		return invalidField("WorkflowRun", "last_completed_sequence", "序号不能为负数")
	}
	if r.CompletedSteps < 0 || r.TotalSteps < 0 {
		return invalidField("WorkflowRun", "completed_steps", "步骤数量不能为负数")
	}
	if r.CompletedSteps > r.TotalSteps {
		return invalidField("WorkflowRun", "completed_steps", "已完成步骤不能超过步骤总数")
	}
	if r.CancelRequested && r.CancelRequestedAt == nil {
		return invalidField("WorkflowRun", "cancel_requested_at", "取消请求必须记录时间")
	}
	if r.Revision < 0 {
		return invalidField("WorkflowRun", "revision", "版本号不能为负数")
	}
	if r.Status.IsTerminal() && r.FinishedAt == nil {
		return invalidField("WorkflowRun", "finished_at", "终态必须记录结束时间")
	}
	if !r.Status.IsTerminal() && r.FinishedAt != nil {
		return invalidField("WorkflowRun", "finished_at", "未结束的执行不能记录结束时间")
	}
	return nil
}

// Transition 将工作流执行迁移到下一个状态。
//
// 进入终态时写入结束时间；离开终态会被状态机拒绝，因此历史结论不会被恢复流程改写。
func (r *WorkflowRun) Transition(next WorkflowRunStatus, at time.Time) error {
	if r == nil {
		return invalidField("WorkflowRun", "", "实体为空")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if r.Status == next {
		return nil
	}
	if err := workflowRunStatusMachine.validate(string(r.Status), string(next)); err != nil {
		return err
	}
	now := normalizeTime(at)
	r.Status = next
	r.UpdatedAt = now
	if next.IsTerminal() {
		r.FinishedAt = &now
	} else {
		r.FinishedAt = nil
	}
	return nil
}

// Start 把工作流推进到 running 并记录执行者与租约。
func (r *WorkflowRun) Start(workerID string, lease time.Duration, at time.Time) error {
	if r == nil {
		return invalidField("WorkflowRun", "", "实体为空")
	}
	now := normalizeTime(at)
	if err := r.Transition(WorkflowRunRunning, now); err != nil {
		return err
	}
	r.WorkerID = strings.TrimSpace(workerID)
	if lease > 0 {
		expires := now.Add(lease)
		r.LeaseExpiresAt = &expires
	}
	if r.StartedAt == nil {
		r.StartedAt = &now
	}
	r.CancelRequested = false
	r.CancelRequestedAt = nil
	r.ErrorClass = ""
	r.ErrorSummary = ""
	r.UpdatedAt = now
	return nil
}

// SetStage 记录当前执行阶段与步骤游标。
//
// 阶段写入发生在有副作用的调用之前：中断后据此判断停在哪里。
func (r *WorkflowRun) SetStage(
	stage WorkflowStage,
	stepID, attemptID *ID,
	at time.Time,
) error {
	if r == nil {
		return invalidField("WorkflowRun", "", "实体为空")
	}
	if err := stage.Validate(); err != nil {
		return err
	}
	for _, id := range []*ID{stepID, attemptID} {
		if id == nil {
			continue
		}
		if err := id.Validate(); err != nil {
			return err
		}
	}
	if attemptID != nil && stepID == nil {
		return invalidField("WorkflowRun", "current_attempt_id", "关联尝试时必须同时指定步骤")
	}
	r.Stage = stage
	r.CurrentStepID = stepID
	r.CurrentAttemptID = attemptID
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// RecordStepCompleted 记录一个步骤已确认完成并推进游标。
//
// 游标只向前推进：已确认完成的序号不会因为后续失败而回退。
func (r *WorkflowRun) RecordStepCompleted(step Step, at time.Time) error {
	if r == nil {
		return invalidField("WorkflowRun", "", "实体为空")
	}
	if err := step.ID.Validate(); err != nil {
		return err
	}
	if step.Sequence <= 0 {
		return invalidField("WorkflowRun", "last_completed_sequence", "步骤序号必须大于 0")
	}
	stepID := step.ID
	if r.LastCompletedStepID == nil || r.LastCompletedStepID.String() != stepID.String() {
		r.CompletedSteps++
	}
	r.LastCompletedStepID = &stepID
	r.LastCompletedSequence = step.Sequence
	r.CurrentStepID = nil
	r.CurrentAttemptID = nil
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// Stop 记录一次非终态的停止（blocked 或 recovery_required）。
func (r *WorkflowRun) Stop(
	status WorkflowRunStatus,
	errorClass, summary string,
	at time.Time,
) error {
	if status != WorkflowRunBlocked && status != WorkflowRunRecoveryRequired {
		return invalidField("WorkflowRun", "status", "停止状态只能是 blocked 或 recovery_required")
	}
	now := normalizeTime(at)
	if err := r.Transition(status, now); err != nil {
		return err
	}
	r.Stage = WorkflowStageStopped
	r.ErrorClass = strings.TrimSpace(errorClass)
	r.ErrorSummary = strings.TrimSpace(summary)
	r.WorkerID = ""
	r.LeaseExpiresAt = nil
	r.UpdatedAt = now
	return nil
}

// Fail 把工作流收敛为失败终态。
func (r *WorkflowRun) Fail(errorClass, summary string, at time.Time) error {
	now := normalizeTime(at)
	if err := r.Transition(WorkflowRunFailed, now); err != nil {
		return err
	}
	r.Stage = WorkflowStageStopped
	r.ErrorClass = strings.TrimSpace(errorClass)
	r.ErrorSummary = strings.TrimSpace(summary)
	r.WorkerID = ""
	r.LeaseExpiresAt = nil
	r.UpdatedAt = now
	return nil
}

// Complete 把工作流收敛为完成终态。
func (r *WorkflowRun) Complete(summary string, at time.Time) error {
	now := normalizeTime(at)
	if err := r.Transition(WorkflowRunCompleted, now); err != nil {
		return err
	}
	r.Stage = WorkflowStageFinalize
	r.ErrorClass = ""
	r.ErrorSummary = strings.TrimSpace(summary)
	r.WorkerID = ""
	r.LeaseExpiresAt = nil
	r.CurrentStepID = nil
	r.CurrentAttemptID = nil
	r.UpdatedAt = now
	return nil
}

// RequestCancel 记录取消请求。
//
// 重复请求是幂等的：已记录请求时直接返回，不重复改写时间戳。状态推进到
// cancel_requested 由编排器在确认现场后完成，本方法不伪造"已停止"。
func (r *WorkflowRun) RequestCancel(at time.Time) error {
	if r == nil {
		return invalidField("WorkflowRun", "", "实体为空")
	}
	if r.Status.IsTerminal() {
		return fmt.Errorf("%w: 已结束的工作流执行不能请求取消", ErrInvalidState)
	}
	if r.CancelRequested {
		return nil
	}
	now := normalizeTime(at)
	r.CancelRequested = true
	r.CancelRequestedAt = &now
	r.UpdatedAt = now
	return nil
}

// Cancel 把工作流收敛为已取消终态。
func (r *WorkflowRun) Cancel(summary string, at time.Time) error {
	now := normalizeTime(at)
	if err := r.Transition(WorkflowRunCancelled, now); err != nil {
		return err
	}
	r.Stage = WorkflowStageStopped
	r.ErrorClass = ""
	r.ErrorSummary = strings.TrimSpace(summary)
	r.WorkerID = ""
	r.LeaseExpiresAt = nil
	r.UpdatedAt = now
	return nil
}

// LeaseActive 判断执行权在给定时刻是否仍然有效。
func (r WorkflowRun) LeaseActive(at time.Time) bool {
	if strings.TrimSpace(r.WorkerID) == "" || r.LeaseExpiresAt == nil {
		return false
	}
	return r.LeaseExpiresAt.After(normalizeTime(at))
}

// developSteps 返回计划中的开发步骤，按序号升序排列。
func developSteps(steps []Step) []Step {
	develop := make([]Step, 0, len(steps))
	for _, step := range steps {
		if step.Type == StepTypeDevelop {
			develop = append(develop, step)
		}
	}
	return develop
}

// WorkflowPlanDigest 计算计划内容摘要。
//
// 摘要只覆盖"这一次执行基于哪些步骤、顺序如何、目标是什么"，不包含状态与时间，
// 因此同一份计划在步骤状态变化后仍然得到相同摘要，恢复时可以据此判断计划是否被
// 重新生成。
func WorkflowPlanDigest(steps []Step) (string, error) {
	if len(steps) == 0 {
		return "", fmt.Errorf("%w: 计划至少需要一个步骤", ErrInvalidState)
	}
	hasher := sha256.New()
	for _, step := range steps {
		fields := []string{
			step.ID.String(),
			strconv.Itoa(step.Sequence),
			string(step.Type),
			step.Title,
			step.Objective,
		}
		for _, field := range fields {
			// 用长度前缀分隔字段，避免不同字段拼接后产生相同摘要。
			_, _ = hasher.Write([]byte(strconv.Itoa(len(field))))
			_, _ = hasher.Write([]byte(":"))
			_, _ = hasher.Write([]byte(field))
			_, _ = hasher.Write([]byte("|"))
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// CheckWorkflowStart 校验任务与计划是否允许启动一次任务级工作流执行。
//
// 校验只读，不修改任何状态，也不创建执行记录；真正的占用由存储层以唯一索引保证。
// 返回的错误类别用于接口映射：状态不允许属于冲突，配置或环境缺失属于前置条件未满足。
func CheckWorkflowStart(task Task, steps []Step) error {
	if task.Deleted() {
		return fmt.Errorf("%w: 任务 %s 已被删除", ErrInvalidState, task.ID)
	}
	if task.CancelRequested {
		return fmt.Errorf("%w: 任务 %s 已请求取消，不能启动工作流执行", ErrInvalidState, task.ID)
	}
	if !task.Status.AllowsStepExecution() {
		return fmt.Errorf(
			"%w: 任务 %s 当前状态 %s 不允许启动工作流执行",
			ErrInvalidState,
			task.ID,
			task.Status,
		)
	}
	if strings.TrimSpace(task.ContainerID) == "" {
		return fmt.Errorf(
			"%w: 任务 %s 没有受管理的任务容器，无法启动工作流执行",
			ErrInvalidState,
			task.ID,
		)
	}
	if len(steps) == 0 {
		return fmt.Errorf("%w: 任务 %s 还没有已保存的计划步骤", ErrInvalidState, task.ID)
	}
	if err := checkWorkflowStepOrder(steps); err != nil {
		return err
	}
	if err := checkWorkflowStepState(steps); err != nil {
		return err
	}
	pending := 0
	for _, step := range steps {
		if step.Status.IsTerminal() {
			continue
		}
		pending++
		if step.Type != StepTypeDevelop {
			return fmt.Errorf(
				"%w: 步骤 %s 的类型 %s 不是开发步骤，第一版工作流只按顺序执行开发步骤",
				ErrInvalidState,
				step.ID,
				step.Type,
			)
		}
		if step.RoleID.IsEmpty() {
			return fmt.Errorf("%w: 步骤 %s 未配置执行角色", ErrInvalidState, step.ID)
		}
		if err := checkStepHasRequiredCheck(step); err != nil {
			return err
		}
	}
	if pending == 0 {
		return fmt.Errorf("%w: 任务 %s 的全部计划步骤都已完成，无需启动工作流", ErrInvalidState, task.ID)
	}
	return nil
}

// checkWorkflowStepOrder 校验计划步骤的顺序是否明确且唯一。
func checkWorkflowStepOrder(steps []Step) error {
	seen := make(map[int]struct{}, len(steps))
	for _, step := range steps {
		if step.Sequence <= 0 {
			return fmt.Errorf("%w: 步骤 %s 的序号非法", ErrInvalidState, step.ID)
		}
		if _, duplicated := seen[step.Sequence]; duplicated {
			return fmt.Errorf(
				"%w: 计划中存在重复的步骤序号 %d，执行顺序不明确",
				ErrInvalidState,
				step.Sequence,
			)
		}
		seen[step.Sequence] = struct{}{}
	}
	return nil
}

// checkWorkflowStepState 校验计划中不存在无法自动推进的现场。
//
// 结果不确定（recovery_required）与已被占用（running / developing / verifying）的
// 步骤都必须先人工核验：在未知现场上重放 Pi 调用会覆盖真实执行结果。
func checkWorkflowStepState(steps []Step) error {
	for _, step := range steps {
		switch step.Status {
		case StepStatusRecoveryRequired:
			return fmt.Errorf(
				"%w: 步骤 %s（%s）的执行结果不确定，必须先核验现场",
				ErrInvalidState,
				step.Title,
				step.ID,
			)
		case StepStatusRunning, StepStatusDeveloping, StepStatusVerifying:
			return fmt.Errorf(
				"%w: 步骤 %s（%s）仍处于执行占用状态 %s，不能启动或恢复推进",
				ErrInvalidState,
				step.Title,
				step.ID,
				step.Status,
			)
		}
	}
	return nil
}

// checkStepHasRequiredCheck 校验步骤是否具备可用于判定的必需检查。
func checkStepHasRequiredCheck(step Step) error {
	for _, command := range step.VerificationSpec.Commands {
		if command.IsRequired() {
			return nil
		}
	}
	return fmt.Errorf(
		"%w: 步骤 %s 的验证配置缺少启用的必需检查，无法判定是否通过",
		ErrInvalidState,
		step.ID,
	)
}

// WorkflowCloseoutInput 是一次任务级工作流收尾所需的全部证据。
type WorkflowCloseoutInput struct {
	// StepsCompleted 表示全部开发步骤是否都已确认完成。
	StepsCompleted bool
	// CodeSaveRequested 表示是否需要执行代码保存。
	CodeSaveRequested bool
	// CodeSaveSucceeded 表示代码保存是否成功（未请求时忽略）。
	CodeSaveSucceeded bool
	// CodeSaveIndeterminate 表示代码保存结果无法确认（未请求时忽略）。
	CodeSaveIndeterminate bool
	// SessionPersisted 表示必需的会话归档是否已完成。
	SessionPersisted bool
}

// WorkflowCloseoutDecision 是任务级收尾结论。
type WorkflowCloseoutDecision struct {
	// Status 是工作流执行应当进入的状态。
	Status WorkflowRunStatus
	// ErrorClass 是收尾失败时的分类。
	ErrorClass string
	// Summary 是收尾说明。
	Summary string
}

// Completed 判断收尾结论是否为"全部必需阶段已完成"。
func (d WorkflowCloseoutDecision) Completed() bool {
	return d.Status == WorkflowRunCompleted
}

// DecideWorkflowCloseout 依据真实证据计算任务级收尾结论。
//
// 规则（顺序固定）：
//  1. 还有未完成的开发步骤：不进入收尾，保持 blocked；
//  2. 需要代码保存但结果不确定：recovery_required，不伪装成成功也不伪装成失败；
//  3. 需要代码保存但未成功：blocked，等待用户显式决定；
//  4. 会话未可靠归档：recovery_required；
//  5. 全部条件满足：completed。
//
// 该函数是纯函数：不读数据库、不启动进程，因此可以被完整地单元测试。
func DecideWorkflowCloseout(input WorkflowCloseoutInput) WorkflowCloseoutDecision {
	if !input.StepsCompleted {
		return WorkflowCloseoutDecision{
			Status:     WorkflowRunBlocked,
			ErrorClass: "steps_incomplete",
			Summary:    "仍有未完成的开发步骤，不进入任务收尾",
		}
	}
	if input.CodeSaveRequested {
		switch {
		case input.CodeSaveIndeterminate:
			return WorkflowCloseoutDecision{
				Status:     WorkflowRunRecoveryRequired,
				ErrorClass: "code_save_indeterminate",
				Summary:    "代码保存结果无法确认，需要人工核验远程任务分支",
			}
		case !input.CodeSaveSucceeded:
			return WorkflowCloseoutDecision{
				Status:     WorkflowRunBlocked,
				ErrorClass: "code_save_failed",
				Summary:    "代码保存未成功，任务不能判定为完整成功",
			}
		}
	}
	if !input.SessionPersisted {
		return WorkflowCloseoutDecision{
			Status:     WorkflowRunRecoveryRequired,
			ErrorClass: "session_not_persisted",
			Summary:    "必需的会话归档尚未完成，需要人工核验",
		}
	}
	return WorkflowCloseoutDecision{
		Status:  WorkflowRunCompleted,
		Summary: "全部必需步骤已通过验证，代码保存与会话归档已完成",
	}
}

// WorkflowStopReason 是工作流停止的结构化原因。
//
// 它把"为什么停止"从错误文本中分离出来，便于持久化到 WorkflowRun 并展示给用户。
type WorkflowStopReason struct {
	// Class 是稳定的错误分类。
	Class string
	// Summary 是面向用户的原因摘要。
	Summary string
	// Blocked 表示这是可恢复的停止而不是确定性失败。
	Blocked bool
	// NeedsRecovery 表示现场无法自动确认，需要人工核验。
	NeedsRecovery bool
}

// ErrWorkflowNotRunning 表示工作流当前没有在推进。
var ErrWorkflowNotRunning = errors.New("domain: 工作流当前没有在推进")
