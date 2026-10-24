package domain

import (
	"strings"
	"time"
)

// 恢复判定使用的阶段名。
//
// 取值与 CodeSaveRecord.Stage 保持一致：判定结论必须说明它针对哪一个阶段，
// 否则调用方无法把"已确认发生"写回正确的状态列。
const (
	recoveryStageCommit = "commit"
	recoveryStagePush   = "push"
	recoveryStageMerge  = "merge"
)

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
	// HeadParentCommit 是 HEAD 的第一父提交。
	//
	// 它用于区分"本次提交产生的 HEAD"与"其他来源造成的 HEAD 变化"：只有第一父
	// 等于已记录基线时，HEAD 的前进才能归因为本次操作。空值表示无法读取（根提交
	// 或查询失败），此时不得把 HEAD 的变化归因为本次提交。
	HeadParentCommit string
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
	// Stage 是本次结论针对的阶段：commit、push 或 merge。
	//
	// 空值表示结论不针对具体阶段（例如现场无法核验、或没有待确认的阶段）。
	Stage string
	// CommitSHA 是核验确认的提交 SHA，仅在确认提交已发生时给出。
	CommitSHA string
	// MergeSHA 是核验确认的合并提交 SHA，仅在确认合并已发生时给出。
	MergeSHA string
	// Summary 是面向用户与日志的判定说明。
	Summary string
	// SafeToRetry 表示可以安全地重新执行对应操作。
	//
	// 只有"确认操作没有发生"且"记录本身可信"才为 true：不确定时重试可能产生重复
	// 提交或覆盖远程历史；记录不可信时即使现场看起来没有变化，也必须先让记录收敛。
	SafeToRetry bool
}

// CodeSaveResumeAction 是"是否允许为该任务再次执行代码保存"的结论。
type CodeSaveResumeAction string

// 恢复续跑动作取值。
const (
	// CodeSaveResumeProceed 表示可以继续执行新的代码保存。
	//
	// 它只表示"没有结果不明的外部副作用"：真正的副作用仍由代码保存组件在执行前
	// 重新核验工作区、分支与远程状态。
	CodeSaveResumeProceed CodeSaveResumeAction = "proceed"
	// CodeSaveResumeVerify 表示必须先做一次只读核验。
	CodeSaveResumeVerify CodeSaveResumeAction = "verify"
	// CodeSaveResumeBlocked 表示禁止自动推进，必须人工介入。
	CodeSaveResumeBlocked CodeSaveResumeAction = "blocked"
)

// CodeSaveResumeDecision 是一次"恢复续跑资格"判定。
type CodeSaveResumeDecision struct {
	// Action 是允许的下一步动作。
	Action CodeSaveResumeAction
	// ErrorClass 是停止时的稳定分类，供工作流与接口透出。
	ErrorClass string
	// Summary 是面向用户的原因说明。
	Summary string
}

// DecideCodeSaveRecovery 依据记录与只读现场计算恢复结论。
//
// 规则（顺序固定，且对三个阶段使用同一套判据）：
//  1. 现场核验失败：unverifiable，不允许任何自动操作；
//  2. 记录不是 pending：不需要恢复，保持既有结论；
//  3. 提交 pending：有基线、HEAD 未变化 → 未提交；HEAD 变化且第一父等于基线 →
//     已提交；HEAD 变化但第一父不等于基线 → 不可归因，unverifiable；没有基线或
//     读不到 HEAD → 不可判定；
//  4. 推送 pending：远程任务分支等于预期提交 → 已推送；等于其他提交 → 不确定；
//     分支不存在 → 未推送；预期提交缺失 → 不可判定；
//  5. 合并 pending：远程基础分支等于唯一候选合并提交 → 已合并；等于基线 → 未合并；
//     其他提交或无法推算 → 不确定。
//
// 记录自身不可信（PersistStatus 为 unconfirmed）时仍然依据现场给出 Git 事实，
// 但一律不给出 SafeToRetry：记录可能落后于真实 Git 状态，重试前必须让记录先收敛。
//
// 该函数永远不返回"成功"：它只回答"Git 操作是否已经发生"，不回答任务是否完成。
func DecideCodeSaveRecovery(
	record CodeSaveRecord,
	observation CodeSaveObservation,
) CodeSaveRecoveryDecision {
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
	var decision CodeSaveRecoveryDecision
	switch {
	case record.CommitStatus == CommitStatusPending:
		decision = decideCommitRecovery(record, observation)
	case record.PushStatus == PushStatusPending:
		decision = decidePushRecovery(record, observation)
	case record.MergeStatus == MergeStatusPending:
		decision = decideMergeRecovery(record, observation)
	default:
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverified,
			Summary: "记录中没有待确认的阶段，无需恢复",
		}
	}
	if decision.Status == RecoveryStatusUnverifiable {
		return decision
	}
	if record.PersistStatus.IsUnconfirmed() {
		// 记录落后于真实 Git 状态：Git 事实仍然可信，但"可以重试"的结论不可信。
		decision.SafeToRetry = false
		decision.Summary = "记录未能可靠写入，重试前必须先让记录收敛；" + decision.Summary
	}
	return decision
}

// DecideCodeSaveResume 依据持久化记录判断是否允许再次执行代码保存。
//
// 规则（顺序固定）：
//  1. 记录写入未确认：先核验，不得仅凭数据库中的 pending 判定操作没有发生；
//  2. 已经核验为 unverifiable：禁止自动重放，保留现场并等待人工处理；
//  3. 已经核验为 confirmed_not_applied：允许继续，由代码保存组件在执行前重新核验
//     分支、工作区与远程状态；
//  4. 仍然存在未决外部副作用（pending / unknown / conflict 等）：先核验；
//  5. 其余情况：没有未决副作用，可以继续。
//
// 该函数只回答"能否推进"，不回答"任务是否成功"；已确认发生的操作绝不会因为
// 再次调用而被重放，因为确认发生后记录已不再处于 pending。
func DecideCodeSaveResume(record CodeSaveRecord) CodeSaveResumeDecision {
	if record.PersistStatus.IsUnconfirmed() {
		return CodeSaveResumeDecision{
			Action:     CodeSaveResumeVerify,
			ErrorClass: "code_save_unconfirmed",
			Summary:    "代码保存记录未能可靠写入，可能落后于真实 Git 状态，必须先核验",
		}
	}
	if !record.HasUnresolvedSideEffect() {
		return CodeSaveResumeDecision{
			Action:  CodeSaveResumeProceed,
			Summary: "代码保存记录中没有结果不明的外部副作用",
		}
	}
	switch record.RecoveryStatus.normalized() {
	case RecoveryStatusUnverifiable:
		return CodeSaveResumeDecision{
			Action:     CodeSaveResumeBlocked,
			ErrorClass: "code_save_indeterminate",
			Summary: "代码保存记录已经核验但无法取得可信事实，禁止自动重放，" +
				"请检查容器与远程可达性后重新执行核验",
		}
	case RecoveryStatusConfirmedNotApplied:
		return CodeSaveResumeDecision{
			Action:  CodeSaveResumeProceed,
			Summary: "已核验确认该操作没有发生，可以重新执行（执行前会重新核验前置条件）",
		}
	default:
		return CodeSaveResumeDecision{
			Action:     CodeSaveResumeVerify,
			ErrorClass: "code_save_indeterminate",
			Summary:    "代码保存记录仍存在结果不明的外部副作用，必须先做一次只读核验",
		}
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
			Stage:   recoveryStageCommit,
			Summary: "记录缺少提交前的 HEAD 基线，无法判断提交是否已经发生，请人工核验",
		}
	}
	head := strings.TrimSpace(observation.HeadCommit)
	if head == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Stage:   recoveryStageCommit,
			Summary: "无法读取任务分支当前 HEAD，不能判断提交是否已经发生",
		}
	}
	if head == baseline {
		return CodeSaveRecoveryDecision{
			Status:      RecoveryStatusConfirmedNotApplied,
			Stage:       recoveryStageCommit,
			Summary:     "任务分支 HEAD 仍等于基线 " + baseline + "，提交没有发生",
			SafeToRetry: true,
		}
	}
	// HEAD 已经变化，但变化本身不能证明是本次提交造成的：只有第一父等于基线，
	// 才能把新的 HEAD 归因为本次操作。
	if parent := strings.TrimSpace(observation.HeadParentCommit); parent != baseline {
		summary := "任务分支 HEAD 已从基线 " + baseline + " 变为 " + head +
			"，但无法证明该提交由本次操作产生（HEAD 的第一父不是记录的基线），请人工核验"
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Stage:   recoveryStageCommit,
			Summary: summary,
		}
	}
	return CodeSaveRecoveryDecision{
		Status:    RecoveryStatusConfirmedApplied,
		Stage:     recoveryStageCommit,
		CommitSHA: head,
		Summary: "任务分支 HEAD 已从基线 " + baseline + " 前进到 " + head +
			"，且第一父与基线一致，提交确实已经发生",
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
			Stage:   recoveryStagePush,
			Summary: "记录缺少预期提交，无法核对远程任务分支，请人工核验",
		}
	}
	remote := strings.TrimSpace(observation.RemoteTaskCommit)
	switch {
	case remote == expected:
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusConfirmedApplied,
			Stage:   recoveryStagePush,
			Summary: "远程任务分支已指向预期提交 " + expected + "，推送确实已经发生",
		}
	case remote == "":
		return CodeSaveRecoveryDecision{
			Status:      RecoveryStatusConfirmedNotApplied,
			Stage:       recoveryStagePush,
			Summary:     "远程任务分支不存在，推送没有发生",
			SafeToRetry: true,
		}
	default:
		return CodeSaveRecoveryDecision{
			Status: RecoveryStatusUnverifiable,
			Stage:  recoveryStagePush,
			Summary: "远程任务分支指向 " + remote + "，与预期提交 " + expected +
				" 不一致（可能已被其他参与者推进），需要人工处理（禁止强制推送）",
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
			Stage:   recoveryStageMerge,
			Summary: "记录缺少合并前的远程基础分支基线，无法归因基础分支的变化，请人工核验",
		}
	}
	remote := strings.TrimSpace(observation.RemoteBaseCommit)
	if remote == "" {
		return CodeSaveRecoveryDecision{
			Status:  RecoveryStatusUnverifiable,
			Stage:   recoveryStageMerge,
			Summary: "远程基础分支不存在或无法读取，不能判断合并是否已经发生",
		}
	}
	if remote == baseline {
		return CodeSaveRecoveryDecision{
			Status:      RecoveryStatusConfirmedNotApplied,
			Stage:       recoveryStageMerge,
			Summary:     "远程基础分支仍等于基线 " + baseline + "，合并没有发生",
			SafeToRetry: true,
		}
	}
	expected := strings.TrimSpace(observation.ExpectedMergeCommit)
	if !observation.MergeCommitResolvable || expected == "" {
		// 基础分支确实变了，但无法证明是本次合并造成的：不得据此判成功。
		return CodeSaveRecoveryDecision{
			Status: RecoveryStatusUnverifiable,
			Stage:  recoveryStageMerge,
			Summary: "远程基础分支已从基线 " + baseline + " 变化为 " + remote +
				"，但无法唯一确定本次合并提交，请人工核验",
		}
	}
	if remote == expected {
		return CodeSaveRecoveryDecision{
			Status:   RecoveryStatusConfirmedApplied,
			Stage:    recoveryStageMerge,
			MergeSHA: expected,
			Summary:  "远程基础分支已指向预期合并提交 " + expected + "，合并确实已经发生",
		}
	}
	return CodeSaveRecoveryDecision{
		Status: RecoveryStatusUnverifiable,
		Stage:  recoveryStageMerge,
		Summary: "远程基础分支指向 " + remote + "，与预期合并提交 " + expected +
			" 不一致，请人工核验",
	}
}

// ApplyRecovery 把一次恢复判定写回记录副本。
//
// 只有 confirmed_applied 会推进阶段状态：它表示该阶段的 Git 操作确实已经发生，
// 必须落盘，否则下一次恢复仍然看到 pending，会把已经发生的外部副作用当成"尚未
// 确认"反复核验。
//
// confirmed_not_applied 只记录恢复结论，不把 pending 改写成 succeeded，也不臆造
// 提交 SHA。它已经足以说明后续阶段都没有发生（不提交就不会推送、不推送就不会
// 合并），因此不再需要逐个阶段核验。
//
// 该方法返回记录副本，调用方仍需通过带条件的存储写入落盘；阶段状态迁移由既有
// 状态机校验，已确认的事实不会被一次查询失败改写。
func (r CodeSaveRecord) ApplyRecovery(
	decision CodeSaveRecoveryDecision,
	at time.Time,
) (CodeSaveRecord, error) {
	updated := r
	if decision.Status == RecoveryStatusConfirmedApplied {
		if err := updated.applyAppliedStage(decision, at); err != nil {
			return r, err
		}
	}
	if updated.RecoveryStatus.normalized() != decision.Status.normalized() {
		if err := updated.MarkRecovery(decision.Status, at); err != nil {
			return r, err
		}
	}
	return updated, nil
}

// applyAppliedStage 把已确认发生的阶段事实写入记录。
func (r *CodeSaveRecord) applyAppliedStage(
	decision CodeSaveRecoveryDecision,
	at time.Time,
) error {
	switch decision.Stage {
	case recoveryStageCommit:
		if r.CommitStatus != CommitStatusPending {
			return nil
		}
		return r.MarkCommit(
			CommitStatusSucceeded,
			decision.CommitSHA,
			r.CommitMessage,
			at,
		)
	case recoveryStagePush:
		if r.PushStatus != PushStatusPending {
			return nil
		}
		if strings.TrimSpace(r.CommitSHA) == "" {
			return invalidField("CodeSave", "commit_sha", "确认推送已发生时记录缺少预期提交")
		}
		return r.MarkPush(PushStatusSucceeded, r.CommitSHA, at)
	case recoveryStageMerge:
		if r.MergeStatus != MergeStatusPending {
			return nil
		}
		return r.MarkMerge(MergeStatusSucceeded, decision.MergeSHA, at)
	default:
		return invalidField("CodeSave", "stage", "恢复结论缺少可写回的阶段")
	}
}

// DecideTaskCodeSaveResume 依据任务的全部代码保存记录判断能否再次执行代码保存。
//
// 代码保存记录是按调用追加的：一次任务可能有多次保存尝试，恢复结论也分别落在各自
// 的记录上。判定必须看完整历史，而不是只看最新一条：
//   - 任一记录存在未决外部副作用且未核验 → 必须先核验；
//   - 任一记录已核验为 unverifiable → 禁止自动重放；
//   - 任一记录写入未确认 → 必须先核验（数据库中的 pending 不能证明操作没有发生）。
//
// 顺序固定为"写入未确认 → 禁止自动重放 → 需要核验 → 可以继续"：越靠前的结论
// 越保守，越不能被靠后的"看起来没问题"覆盖。
func DecideTaskCodeSaveResume(records []CodeSaveRecord) CodeSaveResumeDecision {
	verify := CodeSaveResumeDecision{}
	blocked := CodeSaveResumeDecision{}
	unconfirmed := CodeSaveResumeDecision{}
	hasUnconfirmed := false
	hasBlocked := false
	hasVerify := false
	for _, record := range records {
		decision := DecideCodeSaveResume(record)
		switch decision.Action {
		case CodeSaveResumeBlocked:
			if !hasBlocked {
				hasBlocked = true
				blocked = decision
			}
		case CodeSaveResumeVerify:
			if !hasVerify {
				hasVerify = true
				verify = decision
			}
			if record.PersistStatus.IsUnconfirmed() && !hasUnconfirmed {
				hasUnconfirmed = true
				unconfirmed = decision
			}
		}
	}
	switch {
	case hasUnconfirmed:
		return unconfirmed
	case hasBlocked:
		return blocked
	case hasVerify:
		return verify
	default:
		return CodeSaveResumeDecision{
			Action:  CodeSaveResumeProceed,
			Summary: "代码保存记录中没有结果不明的外部副作用",
		}
	}
}
