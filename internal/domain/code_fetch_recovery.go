package domain

import "strings"

// CodeFetchRecoveryInput 是一次尝试级代码获取残留收敛所需的持久化事实。
type CodeFetchRecoveryInput struct {
	// Record 是读取到的代码获取记录。
	Record CodeFetchRecord
	// LiveOwner 表示该记录所属任务仍有持有有效执行权的执行者。
	//
	// 它为 true 时不得抢占：另一个进程可能正在容器内执行 Git 操作，收敛会覆盖
	// 真实进行中的状态。
	LiveOwner bool
}

// DecideCodeFetchRecovery 依据持久化事实判断中断的代码获取记录是否需要收敛。
//
// 规则（顺序固定）：
//  1. 记录已是终态 → 不处理；
//  2. 记录所属任务仍有持有有效执行权的执行者 → 不抢占；
//  3. 其余 pending / running → 收敛为 uncertain。
//
// 收敛目标固定为 uncertain，原因如下：
//   - `pending` 只说明记录已写入，不能证明 Clone 未发生；
//   - `running` 只说明曾经开始，不能证明 Clone 是否完成；
//   - 空 `commit_sha` 同样不能作为"未发生"的证据（成功结果可能没落库）。
//
// 因此这里既不判"已成功"，也不判"未发生"，只保留不确定性等待只读核验。
// 该函数是纯函数：不读数据库、不执行容器内命令。
func DecideCodeFetchRecovery(input CodeFetchRecoveryInput) (bool, CodeFetchStatus) {
	if input.Record.Status.IsTerminal() {
		return false, input.Record.Status
	}
	if input.LiveOwner {
		return false, input.Record.Status
	}
	switch input.Record.Status {
	case CodeFetchStatusPending, CodeFetchStatusRunning:
		return true, CodeFetchStatusUncertain
	default:
		// uncertain 已经是目标状态：重复收敛不再改写记录。
		return false, input.Record.Status
	}
}

// CodeFetchInterruptedSummary 返回中断收敛时写入的固定摘要。
//
// 摘要必须说明"结论来自启动恢复"，否则用户无法区分自动收敛与真实执行结果。
func CodeFetchInterruptedSummary() string {
	return "上一次代码初始化的进程已退出，无法确认容器内代码获取是否完成，" +
		"需要先核验容器内实际状态，不能重放代码获取。"
}

// CodeFetchResumeAction 是"是否允许为某个 Attempt 再次执行代码获取"的结论。
type CodeFetchResumeAction string

// 代码获取续跑动作取值。
const (
	// CodeFetchResumeProceed 表示没有未决现场，可以开始新的初始化。
	CodeFetchResumeProceed CodeFetchResumeAction = "proceed"
	// CodeFetchResumeVerify 表示存在仍在活动状态的记录，必须先核验现场。
	CodeFetchResumeVerify CodeFetchResumeAction = "verify"
	// CodeFetchResumeBlocked 表示现场已收敛为不确定，禁止自动重放。
	CodeFetchResumeBlocked CodeFetchResumeAction = "blocked"
)

// CodeFetchResumeDecision 是一次"代码获取续跑资格"判定。
type CodeFetchResumeDecision struct {
	// Action 是允许的下一步动作。
	Action CodeFetchResumeAction
	// ErrorClass 是停止时的稳定分类。
	ErrorClass string
	// Summary 是面向用户的原因说明。
	Summary string
}

// DecideCodeFetchResume 依据同一 Attempt 的历史记录判断能否再次执行代码获取。
//
// 顺序固定为"需要核验 → 禁止重放 → 可以继续"：越靠前的结论越保守，越不能被
// 靠后的"看起来没问题"覆盖。
//
// 已成功的记录不阻止新的初始化：调用方会按既有幂等规则写入一条 skipped 记录，
// 而不是重新 Clone。
func DecideCodeFetchResume(records []CodeFetchRecord) CodeFetchResumeDecision {
	hasUncertain := false
	hasActive := false
	for _, record := range records {
		switch record.Status {
		case CodeFetchStatusUncertain:
			hasUncertain = true
		case CodeFetchStatusPending, CodeFetchStatusRunning:
			hasActive = true
		}
	}
	switch {
	case hasUncertain:
		return CodeFetchResumeDecision{
			Action:     CodeFetchResumeBlocked,
			ErrorClass: "code_fetch_uncertain",
			Summary: "该尝试的代码初始化结果不确定，必须先核验容器内实际状态，" +
				"禁止重放代码获取",
		}
	case hasActive:
		return CodeFetchResumeDecision{
			Action:     CodeFetchResumeVerify,
			ErrorClass: "code_fetch_active",
			Summary:    "该尝试仍存在活动状态的代码初始化记录，必须等待其收敛或先核验现场",
		}
	default:
		return CodeFetchResumeDecision{
			Action:  CodeFetchResumeProceed,
			Summary: "该尝试没有结果不明的代码获取现场",
		}
	}
}

// CodeFetchVerification 是一次只读核验得到的容器内工作区事实。
//
// 每个字段都对应一次独立的只读查询；查询失败必须显式表达，不得用零值冒充
// "目录不存在"。
type CodeFetchVerification struct {
	// ReadFailed 表示核验本身没有完成。
	ReadFailed bool
	// ReadError 是核验失败的可读原因。
	ReadError string
	// WorkDirExists 表示目标工作目录存在。
	WorkDirExists bool
	// IsGitRepository 表示目标目录是一个 Git 仓库。
	IsGitRepository bool
	// Branch 是当前分支名，detached 时为空。
	Branch string
	// Head 是当前 HEAD 指向的 commit。
	Head string
}

// CodeFetchVerificationDecision 是一次只读核验得出的收敛结论。
type CodeFetchVerificationDecision struct {
	// Converged 表示可以据此把记录推进到终态。
	Converged bool
	// Status 是收敛目标状态，Converged 为 true 时有效。
	Status CodeFetchStatus
	// Summary 是面向用户的原因说明。
	Summary string
}

// DecideCodeFetchVerification 依据只读核验事实判断能否收敛一条 uncertain 记录。
//
// 只有在事实充分时才允许收敛：
//   - 目录不存在 → 可以判定"没有获取到代码"，收敛为 failed；
//   - 目录存在且是 Git 仓库、任务分支匹配、HEAD 可读 → 收敛为 succeeded；
//   - 其余（核验失败、目录存在但不是 Git 仓库、分支不匹配、HEAD 不可读）→
//     保持 uncertain，由人工核验。
//
// 该函数不修改任何状态，也不执行写操作；是否落库由调用方决定。
func DecideCodeFetchVerification(
	record CodeFetchRecord,
	verification CodeFetchVerification,
) CodeFetchVerificationDecision {
	if verification.ReadFailed {
		summary := "无法核验容器内工作区"
		if detail := strings.TrimSpace(verification.ReadError); detail != "" {
			summary += ": " + detail
		}
		return CodeFetchVerificationDecision{Summary: summary}
	}
	if !verification.WorkDirExists {
		return CodeFetchVerificationDecision{
			Converged: true,
			Status:    CodeFetchStatusFailed,
			Summary:   "目标工作目录不存在，可以确认代码没有被获取到",
		}
	}
	if !verification.IsGitRepository {
		return CodeFetchVerificationDecision{
			Summary: "目标工作目录存在但不是 Git 仓库，无法确认代码来源，需要人工核验",
		}
	}
	branch := strings.TrimSpace(verification.Branch)
	if branch != strings.TrimSpace(record.TaskBranch) {
		return CodeFetchVerificationDecision{
			Summary: "工作区当前分支是 " + emptyAsUnknownText(branch) +
				"，与记录的任务分支 " + record.TaskBranch + " 不一致，需要人工核验",
		}
	}
	if strings.TrimSpace(verification.Head) == "" {
		return CodeFetchVerificationDecision{
			Summary: "工作区 HEAD 无法读取，不能确认代码已经就位，需要人工核验",
		}
	}
	return CodeFetchVerificationDecision{
		Converged: true,
		Status:    CodeFetchStatusSucceeded,
		Summary: "工作区已确认位于任务分支 " + record.TaskBranch +
			" 且 HEAD 可读，可以确认代码已经获取",
	}
}

// emptyAsUnknownText 把空字符串渲染为"（空）"，用于拼装可读原因。
func emptyAsUnknownText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "（空）"
	}
	return value
}
