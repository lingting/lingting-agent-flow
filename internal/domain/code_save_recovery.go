package domain

import "strings"

// CodeSaveObservation 是一次只读核验得到的现场事实。
//
// 它是"恢复判定"的唯一输入：判定不读数据库、不执行 Git 命令，因此可以被完整地
// 单元测试，也不会在判定过程中产生新的副作用。
//
// 每个字段都对应一次独立的只读查询，查询失败必须显式表达，不得用零值冒充"没有
// 变化"：
//   - ReadFailed 表示本次核验本身没有完成，此时其余字段一律不可信；
//   - 空值表示"查询成功但没有结果"（例如远程分支不存在），与"查询失败"不同。
type CodeSaveObservation struct {
	// ReadFailed 表示只读核验没有完成，例如容器不可访问或远程不可达。
	ReadFailed bool
	// ReadError 是核验失败的可读原因，已脱敏并裁剪。
	ReadError string
	// HeadCommit 是任务分支当前 HEAD 指向的 commit。
	HeadCommit string
	// RemoteTaskCommit 是远程任务分支当前指向的 commit；空值表示远程分支不存在。
	RemoteTaskCommit string
	// RemoteBaseCommit 是远程基础分支当前指向的 commit；空值表示远程分支不存在。
	RemoteBaseCommit string
	// ExpectedMergeCommit 是按预期合并关系推算出的合并提交。
	//
	// 只有在能够唯一确定时才给出：它必须同时满足"包含任务分支提交"与"以基线为
	// 第一父提交"。无法唯一确定时留空，由判定函数返回不确定。
	ExpectedMergeCommit string
	// MergeCommitResolvable 表示是否成功完成过合并提交的推算。
	//
	// 它与 ExpectedMergeCommit 为空不同：前者表示"算不出来"，后者表示"算了但没有
	// 候选"。两者都不得当作合并已经发生。
	MergeCommitResolvable bool
}

// CodeSaveRecoveryDecision 是一次恢复判定的结论。
type CodeSaveRecoveryDecision struct {
	// Status 是应当写入记录的恢复结论。
	Status RecoveryStatus
	// CommitSHA 是核验确认的提交 SHA，仅在确认提交已发生时给出。
	CommitSHA string
	// MergeSHA 是核验确认的合并提交 SHA，仅在确认合并已发生时给出。
	MergeSHA string
	// Summary 是面向用户与日志的判定说明。
	Summary string
	// SafeToRetry 表示可以安全地重新执行对应操作。
	//
	// 只有"确认操作没有发生"才为 true：不确定时重试可能产生重复提交或覆盖远程历史。
	SafeToRetry bool
}

// DecideCodeSaveRecovery 依据记录与只读现场计算恢复结论。
//
// 规则（顺序固定，且对三个阶段使用同一套判据）：
//  1. 记录本身未被可靠写入：先核验，不判定；
//  2. 现场核验失败：unverifiable，不允许任何自动操作；
//  3. 记录不是 pending：不需要恢复，保持既有结论；
//  4. 提交 pending：有基线且 HEAD 已变化 → 已提交；有基线且 HEAD 未变化 → 未提交；
//     没有基线 → 不可判定；
//  5. 推送 pending：远程任务分支等于预期提交 → 已推送；等于其他提交 → 不确定；
//     分支不存在 → 未推送；预期提交缺失 → 不可判定；
//  6. 合并 pending：远程基础分支等于唯一候选合并提交 → 已合并；等于基线 → 未合并；
//     其他提交或无法推算 → 不确定。
//
// 该函数永远不返回"成功"：它只回答"Git 操作是否已经发生"，不回答任务是否完成。
func DecideCodeSaveRecovery(
	record CodeSaveRecord,
	observation CodeSaveObservation,
) CodeSaveRecoveryDecision {
	if record.PersistStatus.IsUnconfirmed() {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: "记录未能可靠写入，先核验真实仓库状态再决定后续操作",
		}
	}
	if observation.ReadFailed {
		summary := "无法核验真实仓库状态"
		if detail := strings.TrimSpace(observation.ReadError); detail != "" {
			summary += ": " + detail
		}
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: summary,
		}
	}
	if record.CommitStatus == CommitStatusPending {
		return decideCommitRecovery(record, observation)
	}
	if record.PushStatus == PushStatusPending {
		return decidePushRecovery(record, observation)
	}
	if record.MergeStatus == MergeStatusPending {
		return decideMergeRecovery(record, observation)
	}
	return CodeSaveRecoveryDecision{
		Status:  RecoveryStatusUnverified,
		Summary: "记录中没有待确认的阶段，无需恢复",
	}
}

// decideCommitRecovery 判断提交是否已经发生。
func decideCommitRecovery(
	record CodeSaveRecord,
	observation CodeSaveObservation,
) CodeSaveRecoveryDecision {
	baseline := strings.TrimSpace(record.BaselineCommitSHA)
	if baseline == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: "记录缺少提交前的 HEAD 基线，无法判断提交是否已经发生，请人工核验",
		}
	}
	head := strings.TrimSpace(observation.HeadCommit)
	if head == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: "无法读取任务分支当前 HEAD，不能判断提交是否已经发生",
		}
	}
	if head != baseline {
		return CodeSaveRecoveryDecision{
			Status:    RecoveryStatusConfirmedApplied,
			CommitSHA: head,
			Summary:   "任务分支 HEAD 已从基线 " + baseline + " 前进到 " + head + "，提交确实已经发生",
		}
	}
	return CodeSaveRecoveryDecision{
		Status:      RecoveryStatusConfirmedNotApplied,
		Summary:     "任务分支 HEAD 仍等于基线 " + baseline + "，提交没有发生",
		SafeToRetry: true,
	}
}

// decidePushRecovery 判断推送是否已经发生。
func decidePushRecovery(
	record CodeSaveRecord,
	observation CodeSaveObservation,
) CodeSaveRecoveryDecision {
	expected := strings.TrimSpace(record.CommitSHA)
	if expected == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: "记录缺少预期提交，无法核对远程任务分支，请人工核验",
		}
	}
	remote := strings.TrimSpace(observation.RemoteTaskCommit)
	switch {
	case remote == expected:
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusConfirmedApplied,
			Summary: "远程任务分支已指向预期提交 " + expected + "，推送确实已经发生",
		}
	case remote == "":
		return CodeSaveRecoveryDecision{
			Status:      RecoveryStatusConfirmedNotApplied,
			Summary:     "远程任务分支不存在，推送没有发生",
			SafeToRetry: true,
		}
	default:
		return CodeSaveRecoveryDecision{
			Status: RecoveryStatusUnverifiable,
			Summary: "远程任务分支指向 " + remote + "，与预期提交 " + expected +
				" 不一致，需要人工处理（禁止强制推送）",
		}
	}
}

// decideMergeRecovery 判断基础分支合并是否已经发生。
func decideMergeRecovery(
	record CodeSaveRecord,
	observation CodeSaveObservation,
) CodeSaveRecoveryDecision {
	baseline := strings.TrimSpace(record.BaselineBaseCommitSHA)
	if baseline == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: "记录缺少合并前的远程基础分支基线，无法归因基础分支的变化，请人工核验",
		}
	}
	remote := strings.TrimSpace(observation.RemoteBaseCommit)
	if remote == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Summary: "远程基础分支不存在或无法读取，不能判断合并是否已经发生",
		}
	}
	if remote == baseline {
		return CodeSaveRecoveryDecision{
			Status:      RecoveryStatusConfirmedNotApplied,
			Summary:     "远程基础分支仍等于基线 " + baseline + "，合并没有发生",
			SafeToRetry: true,
		}
	}
	expected := strings.TrimSpace(observation.ExpectedMergeCommit)
	if !observation.MergeCommitResolvable || expected == "" {
		// 基础分支确实变了，但无法证明是本次合并造成的：不得据此判成功。
		return CodeSaveRecoveryDecision{
			Status: RecoveryStatusUnverifiable,
			Summary: "远程基础分支已从基线 " + baseline + " 变化为 " + remote +
				"，但无法唯一确定本次合并提交，请人工核验",
		}
	}
	if remote == expected {
		return CodeSaveRecoveryDecision{
			Status:   RecoveryStatusConfirmedApplied,
			MergeSHA: expected,
			Summary:  "远程基础分支已指向预期合并提交 " + expected + "，合并确实已经发生",
		}
	}
	return CodeSaveRecoveryDecision{
		Status: RecoveryStatusUnverifiable,
		Summary: "远程基础分支指向 " + remote + "，与预期合并提交 " + expected +
			" 不一致，请人工核验",
	}
}
