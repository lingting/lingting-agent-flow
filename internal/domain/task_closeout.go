package domain

import (
	"fmt"
	"strings"
	"time"
)

// TaskCloseoutState 是"任务结论是否已经闭环"的派生结论。
//
// 它是只读视图，不是第二套状态：取值全部由 tasks.status、final_result 与最近一次
// 工作流执行记录派生，不写回数据库，也不参与状态迁移。它的存在只为了解决一个问题：
// 任务状态可以被推进到 completed，而执行记录的收尾写入可能失败，此时对外必须
// 表达为"未完成"，而不是让调用方从两个接口自行拼接。
type TaskCloseoutState string

// 任务收尾派生结论取值。
const (
	// TaskCloseoutConfirmed 表示任务结论已闭环：任务 completed 且执行记录 completed。
	TaskCloseoutConfirmed TaskCloseoutState = "confirmed"
	// TaskCloseoutPending 表示收尾仍在进行：存在持有有效执行权的执行记录。
	TaskCloseoutPending TaskCloseoutState = "pending"
	// TaskCloseoutUnconfirmed 表示收尾未闭环：任务尚未结束，或任务已结束但执行记录
	// 没有给出对应的完成结论。
	TaskCloseoutUnconfirmed TaskCloseoutState = "unconfirmed"
	// TaskCloseoutInconsistent 表示遗留的不一致现场：任务被标记为成功，但执行记录
	// 没有确认完成。这类现场只能被发现与报告，不得被当作确定成功。
	TaskCloseoutInconsistent TaskCloseoutState = "inconsistent"
	// TaskCloseoutCancelled 表示任务已按取消请求结束。
	TaskCloseoutCancelled TaskCloseoutState = "cancelled"
)

// String 返回派生结论的字符串形式。
func (s TaskCloseoutState) String() string {
	return string(s)
}

// TaskCloseoutViewInput 是派生"任务结论是否闭环"所需的持久化事实。
type TaskCloseoutViewInput struct {
	// Task 是读取到的任务。
	Task Task
	// Run 是最近一次工作流执行记录，没有执行记录时为空。
	Run *WorkflowRun
	// Now 是判定执行权是否仍然有效的时刻。
	Now time.Time
}

// DecideTaskCloseoutView 依据持久化事实派生任务结论的闭环状态。
//
// 规则（顺序固定，只在 domain 中定义一次）：
//  1. 任务已删除 → unconfirmed（已删除任务不对外表达结论）；
//  2. 任务 cancelled → cancelled；
//  3. 任务 completed：
//     a. final_result 不是 succeeded → unconfirmed；
//     b. 没有执行记录 → inconsistent（无法证明收尾闭环）；
//     c. 执行记录 completed → confirmed；
//     d. 执行记录 running 且租约有效 → pending；
//     e. 其余 → inconsistent；
//  4. 执行记录 running 且租约有效 → pending；
//  5. 其余 → unconfirmed。
//
// 该函数是纯函数：不读数据库、不启动进程。
func DecideTaskCloseoutView(input TaskCloseoutViewInput) TaskCloseoutState {
	task := input.Task
	if task.Deleted() {
		return TaskCloseoutUnconfirmed
	}
	if task.Status == TaskStatusCancelled {
		return TaskCloseoutCancelled
	}
	run := input.Run
	if task.Status == TaskStatusCompleted {
		if task.FinalResult != TaskResultSucceeded {
			return TaskCloseoutUnconfirmed
		}
		if run == nil {
			return TaskCloseoutInconsistent
		}
		switch {
		case run.Status == WorkflowRunCompleted:
			return TaskCloseoutConfirmed
		case run.Status == WorkflowRunRunning && run.LeaseActive(input.Now):
			return TaskCloseoutPending
		default:
			return TaskCloseoutInconsistent
		}
	}
	if run != nil && run.Status == WorkflowRunRunning && run.LeaseActive(input.Now) {
		return TaskCloseoutPending
	}
	return TaskCloseoutUnconfirmed
}

// TaskCloseoutNeedsAttention 判断派生结论是否需要人工关注。
func (s TaskCloseoutState) NeedsAttention() bool {
	switch s {
	case TaskCloseoutInconsistent, TaskCloseoutUnconfirmed:
		return true
	default:
		return false
	}
}

// TaskCloseoutSummary 返回派生结论的可读说明。
func (s TaskCloseoutState) Summary() string {
	switch s {
	case TaskCloseoutConfirmed:
		return "任务与工作流执行记录一致确认收尾完成"
	case TaskCloseoutPending:
		return "工作流仍在推进，收尾尚未结束"
	case TaskCloseoutCancelled:
		return "任务已按取消请求结束"
	case TaskCloseoutInconsistent:
		return "任务被标记为成功，但工作流执行记录没有确认完成；" +
			"该现场必须人工核验，不得当作确定成功"
	default:
		return "任务结论尚未闭环，需要继续推进或人工核验"
	}
}

// 任务收尾停滞的分类。
const (
	// CloseoutClassTaskCompletionFailed 表示收尾证据齐备但任务状态写入失败。
	CloseoutClassTaskCompletionFailed = "task_completion_failed"
	// CloseoutClassEvidenceMissing 表示执行记录已完成，但按持久化事实重算后
	// 收尾条件并不成立（例如代码保存记录或会话归档缺失）。
	CloseoutClassEvidenceMissing = "closeout_evidence_missing"
	// CloseoutClassInterrupted 表示收尾阶段被中断，且没有可复用的完成结论。
	CloseoutClassInterrupted = "closeout_interrupted"
)

// TaskCloseoutRecoveryInput 是一次"收尾停滞任务"恢复所需的持久化事实。
type TaskCloseoutRecoveryInput struct {
	// Task 是读取到的任务。
	Task Task
	// Run 是最近一次工作流执行记录，没有时为空。
	Run *WorkflowRun
	// Decision 是按持久化事实重算出的收尾结论。
	//
	// 只有在 Run 已处于 completed 时才需要它：此时执行记录无法再被改写，
	// 任务能否收口只能由重新计算的证据决定。
	Decision WorkflowCloseoutDecision
	// Now 是判定执行权是否仍然有效的时刻。
	Now time.Time
}

// TaskCloseoutRecoveryDecision 是一次收尾停滞恢复的结论。
type TaskCloseoutRecoveryDecision struct {
	// Needed 表示需要改写任务状态。
	Needed bool
	// Next 是任务目标状态，Needed 为 true 时有效。
	Next TaskStatus
	// Complete 表示应当把任务推进到成功终态（而不是写入恢复状态）。
	Complete bool
	// ErrorClass 是稳定分类。
	ErrorClass string
	// Summary 是面向用户的原因说明。
	Summary string
}

// DecideTaskCloseoutRecovery 依据持久化事实判断停滞在收尾阶段的任务该如何收敛。
//
// 只处理"业务阶段已进入收尾（verifying / saving）"的任务：其余状态由计划生成、
// 取消现场与启动流程各自的恢复入口负责，这里不重复判定。
//
// 规则（顺序固定）：
//  1. 任务已删除或不是 verifying / saving → 不处理；
//  2. 执行记录 running 且租约有效 → 不处理：仍有执行者在推进；
//  3. 执行记录 completed 且重算结论为 completed → 补写任务成功终态；
//  4. 执行记录 completed 但证据不成立 → recovery_required / evidence_missing；
//  5. 其余（blocked / recovery_required / failed / 无记录）→
//     recovery_required / closeout_interrupted。
//
// 该函数是纯函数：不读数据库、不启动进程，也不重放任何外部操作。
func DecideTaskCloseoutRecovery(input TaskCloseoutRecoveryInput) TaskCloseoutRecoveryDecision {
	task := input.Task
	if task.Deleted() {
		return TaskCloseoutRecoveryDecision{}
	}
	switch task.Status {
	case TaskStatusVerifying, TaskStatusSaving:
	default:
		return TaskCloseoutRecoveryDecision{}
	}
	run := input.Run
	if run != nil && run.Status == WorkflowRunRunning && run.LeaseActive(input.Now) {
		return TaskCloseoutRecoveryDecision{
			ErrorClass: "closeout_in_progress",
			Summary:    "工作流仍在推进收尾，恢复不会抢占",
		}
	}
	if run != nil && run.Status == WorkflowRunCompleted {
		if input.Decision.Completed() {
			return TaskCloseoutRecoveryDecision{
				Needed:     true,
				Next:       TaskStatusCompleted,
				Complete:   true,
				ErrorClass: "closeout_resumed",
				Summary:    "执行记录已确认完成且证据齐备，补齐任务成功终态",
			}
		}
		class := strings.TrimSpace(input.Decision.ErrorClass)
		if class == "" {
			class = CloseoutClassEvidenceMissing
		}
		return TaskCloseoutRecoveryDecision{
			Needed:     true,
			Next:       TaskStatusRecoveryRequired,
			ErrorClass: CloseoutClassEvidenceMissing,
			Summary: "执行记录已标记完成，但按持久化事实重算收尾条件不成立（" +
				class + "），需要人工核验",
		}
	}
	return TaskCloseoutRecoveryDecision{
		Needed:     true,
		Next:       TaskStatusRecoveryRequired,
		ErrorClass: CloseoutClassInterrupted,
		Summary:    "收尾阶段被中断且没有可复用的完成结论，需要人工核验现场后继续",
	}
}

// TaskCloseoutInconsistentSummary 返回遗留不一致现场的固定说明。
func TaskCloseoutInconsistentSummary(task Task, run *WorkflowRun) string {
	detail := fmt.Sprintf("任务 %s 状态为 %s", task.ID, task.Status)
	if run == nil {
		return detail + "，但没有任何工作流执行记录；该现场不得当作确定成功"
	}
	return detail + "，但最近一次工作流执行记录为 " + string(run.Status) +
		"；该现场不得当作确定成功，需要人工核验"
}
