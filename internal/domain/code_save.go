package domain

import (
	"regexp"
	"strings"
	"time"
)

// CommitStatus 描述一次代码保存中 Git 提交的结果。
//
// 提交、推送与合并是三个独立事实，必须分别持久化：Push 成功而 Merge 失败时，
// 任务分支的保存状态仍然是成功，不能被合并结果覆盖。
type CommitStatus string

// 提交状态取值。
const (
	// CommitStatusPending 表示尚未执行提交。
	CommitStatusPending CommitStatus = "pending"
	// CommitStatusNotAttempted 表示本次调用没有尝试提交（例如只推送）。
	CommitStatusNotAttempted CommitStatus = "not_attempted"
	// CommitStatusNoChanges 表示工作区没有可提交的变更。
	CommitStatusNoChanges CommitStatus = "no_changes"
	// CommitStatusSucceeded 表示已经产生新的提交。
	CommitStatusSucceeded CommitStatus = "succeeded"
	// CommitStatusFailed 表示提交失败，且可以确认 HEAD 没有变化。
	CommitStatusFailed CommitStatus = "failed"
	// CommitStatusUnknown 表示提交结果无法确认，必须先核验仓库状态。
	CommitStatusUnknown CommitStatus = "unknown"
)

// commitStatusMachine 定义提交状态的迁移。
//
// 只有 pending 可以进入其他状态；其余状态都是终态，避免同一记录被反复改写。
var commitStatusMachine = statusMachine{
	entity: "CodeSave",
	transitions: map[string][]string{
		string(CommitStatusPending): {
			string(CommitStatusNotAttempted),
			string(CommitStatusNoChanges),
			string(CommitStatusSucceeded),
			string(CommitStatusFailed),
			string(CommitStatusUnknown),
		},
	},
	terminal: []string{
		string(CommitStatusNotAttempted),
		string(CommitStatusNoChanges),
		string(CommitStatusSucceeded),
		string(CommitStatusFailed),
		string(CommitStatusUnknown),
	},
}

// String 返回提交状态的字符串形式。
func (s CommitStatus) String() string {
	return string(s)
}

// Validate 校验提交状态是否属于已知取值。
func (s CommitStatus) Validate() error {
	if commitStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("CodeSave", "commit_status", "未知的提交状态 "+string(s))
}

// PushStatus 描述任务分支的推送结果。
type PushStatus string

// 推送状态取值。
const (
	// PushStatusPending 表示尚未执行推送。
	PushStatusPending PushStatus = "pending"
	// PushStatusNotAttempted 表示本次调用没有尝试推送。
	PushStatusNotAttempted PushStatus = "not_attempted"
	// PushStatusSucceeded 表示远程任务分支已经包含预期提交。
	PushStatusSucceeded PushStatus = "succeeded"
	// PushStatusFailed 表示推送失败，且可以确认远程没有变化。
	PushStatusFailed PushStatus = "failed"
	// PushStatusConflict 表示远程历史与本地不一致，需要人工处理。
	PushStatusConflict PushStatus = "conflict"
	// PushStatusUnknown 表示无法核实远程是否已经收到提交。
	PushStatusUnknown PushStatus = "unknown"
)

// pushStatusMachine 定义推送状态的迁移。
//
// unknown 可以依据远程核验结果收敛为 succeeded、failed 或 conflict；
// conflict 只能在冲突解决后重新推送成功。任何迁移都不允许自动覆盖远程历史。
var pushStatusMachine = statusMachine{
	entity: "CodeSave",
	transitions: map[string][]string{
		string(PushStatusPending): {
			string(PushStatusNotAttempted),
			string(PushStatusSucceeded),
			string(PushStatusFailed),
			string(PushStatusConflict),
			string(PushStatusUnknown),
		},
		string(PushStatusUnknown): {
			string(PushStatusSucceeded),
			string(PushStatusFailed),
			string(PushStatusConflict),
		},
		string(PushStatusFailed): {
			string(PushStatusSucceeded),
			string(PushStatusConflict),
			string(PushStatusUnknown),
		},
		string(PushStatusConflict): {
			string(PushStatusSucceeded),
			string(PushStatusUnknown),
		},
	},
	terminal: []string{
		string(PushStatusNotAttempted),
		string(PushStatusSucceeded),
	},
}

// String 返回推送状态的字符串形式。
func (s PushStatus) String() string {
	return string(s)
}

// Validate 校验推送状态是否属于已知取值。
func (s PushStatus) Validate() error {
	if pushStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("CodeSave", "push_status", "未知的推送状态 "+string(s))
}

// MergeStatus 描述把任务分支合并到基础分支的结果。
type MergeStatus string

// 合并状态取值。
const (
	// MergeStatusPending 表示已经请求合并但尚未执行。
	MergeStatusPending MergeStatus = "pending"
	// MergeStatusNotRequested 表示本次没有请求合并。
	MergeStatusNotRequested MergeStatus = "not_requested"
	// MergeStatusSkipped 表示请求了合并但前置条件不满足，未执行。
	MergeStatusSkipped MergeStatus = "skipped"
	// MergeStatusSucceeded 表示基础分支已经包含任务分支的提交并推送成功。
	MergeStatusSucceeded MergeStatus = "succeeded"
	// MergeStatusFailed 表示合并或合并后的推送失败。
	MergeStatusFailed MergeStatus = "failed"
	// MergeStatusConflict 表示合并出现冲突，需要人工处理。
	MergeStatusConflict MergeStatus = "conflict"
	// MergeStatusUnknown 表示无法核实远程基础分支的最终状态。
	MergeStatusUnknown MergeStatus = "unknown"
)

// mergeStatusMachine 定义合并状态的迁移。
//
// 与推送一致：结果不确定时先核验远程基础分支，再收敛为确定状态。
var mergeStatusMachine = statusMachine{
	entity: "CodeSave",
	transitions: map[string][]string{
		string(MergeStatusPending): {
			string(MergeStatusNotRequested),
			string(MergeStatusSkipped),
			string(MergeStatusSucceeded),
			string(MergeStatusFailed),
			string(MergeStatusConflict),
			string(MergeStatusUnknown),
		},
		string(MergeStatusUnknown): {
			string(MergeStatusSucceeded),
			string(MergeStatusFailed),
			string(MergeStatusConflict),
		},
		string(MergeStatusFailed): {
			string(MergeStatusSucceeded),
			string(MergeStatusUnknown),
		},
	},
	terminal: []string{
		string(MergeStatusNotRequested),
		string(MergeStatusSkipped),
		string(MergeStatusSucceeded),
		string(MergeStatusConflict),
	},
}

// String 返回合并状态的字符串形式。
func (s MergeStatus) String() string {
	return string(s)
}

// Validate 校验合并状态是否属于已知取值。
func (s MergeStatus) Validate() error {
	if mergeStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("CodeSave", "merge_status", "未知的合并状态 "+string(s))
}

// PersistStatus 表达代码保存记录自身是否被可靠写入。
//
// 它与 CommitStatus / PushStatus / MergeStatus 是不同维度的事实：后者描述 Git 做了
// 什么，前者描述"我们对 Git 做了什么"这条记录是否可信。Git 已经成功但记录写入失败时，
// 记录内容仍是 pending，此时不能依据记录判定 Git 没有执行。
type PersistStatus string

// 记录写入状态取值。
const (
	// PersistStatusPersisted 表示最后一次写入成功，记录内容可信。
	//
	// 空值等价于 persisted：历史记录写入时还没有该字段，不能因此被判定为不可信。
	PersistStatusPersisted PersistStatus = "persisted"
	// PersistStatusUnconfirmed 表示最后一次写入失败，记录可能落后于真实 Git 状态。
	PersistStatusUnconfirmed PersistStatus = "unconfirmed"
)

// persistStatusMachine 定义记录写入状态的迁移。
//
// 只允许 unconfirmed → persisted：写入成功之后不会因为后续读取而重新变成不可信，
// 除非再次写入失败。历史记录的空值由 persisted 归一化处理，因此也可以收敛。
var persistStatusMachine = statusMachine{
	entity: "CodeSave",
	transitions: map[string][]string{
		string(PersistStatusUnconfirmed): {
			string(PersistStatusPersisted),
		},
	},
	terminal: []string{
		string(PersistStatusPersisted),
	},
}

// String 返回记录写入状态的字符串形式。
func (s PersistStatus) String() string {
	return string(s)
}

// Normalized 把空值归一化为 persisted，其余取值原样返回。
//
// 历史记录写入时没有该字段，空值必须按"可信"处理；未知取值保持原样，由 Validate
// 决定是否接受，读取层不得把它映射为默认状态。
func (s PersistStatus) Normalized() PersistStatus {
	if strings.TrimSpace(string(s)) == "" {
		return PersistStatusPersisted
	}
	return s
}

// IsUnconfirmed 判断记录是否可能落后于真实 Git 状态。
func (s PersistStatus) IsUnconfirmed() bool {
	return s.Normalized() == PersistStatusUnconfirmed
}

// Validate 校验记录写入状态是否属于已知取值。
//
// 空值是允许的：它表示该字段写入之前的历史记录。
func (s PersistStatus) Validate() error {
	if strings.TrimSpace(string(s)) == "" {
		return nil
	}
	if persistStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("CodeSave", "persist_status", "未知的记录写入状态 "+string(s))
}

// RecoveryStatus 描述一次只读核验得到的恢复结论。
//
// unverified 与 unverifiable 必须区分：前者表示"还没有核验"，后者表示"已经尝试
// 核验但无法取得可信事实"。两者都不得被当作失败或成功。
type RecoveryStatus string

// 恢复结论取值。
const (
	// RecoveryStatusUnverified 表示尚未核验，记录仍停留在原始状态。
	RecoveryStatusUnverified RecoveryStatus = "unverified"
	// RecoveryStatusConfirmedApplied 表示已核验：Git 操作确实已经发生。
	RecoveryStatusConfirmedApplied RecoveryStatus = "confirmed_applied"
	// RecoveryStatusConfirmedNotApplied 表示已核验：Git 操作确实没有发生。
	RecoveryStatusConfirmedNotApplied RecoveryStatus = "confirmed_not_applied"
	// RecoveryStatusUnverifiable 表示无法取得可信事实，必须人工核验。
	RecoveryStatusUnverifiable RecoveryStatus = "unverifiable"
)

// recoveryStatusMachine 定义恢复结论的迁移。
//
// 核验结论可以因新的核验而修正（例如远端恢复可达后重新核验），但已经收敛为
// applied 或 not_applied 之后不允许在记录层被改写成不确定状态：真实 Git 事实不会
// 因为一次查询失败而消失，此时应当保留既有结论并单独记录查询错误。
//
// 已收敛的结论之间允许互相迁移：一条记录里提交、推送、合并是依次推进的三个阶段，
// 而恢复结论只有一个字段。核验总是针对"第一个仍待确认的阶段"，因此同一字段会被
// 不同阶段依次占用——提交确认已发生后，下一次核验判定的是推送，其结果可能是
// confirmed_not_applied。禁止这类迁移会让数据库永远停在 push_status=pending，
// 使恢复只能反复要求核验而无法收敛。阶段归属由 CodeSaveRecoveryDecision.Stage
// 显式表达，写入时与结论一起处理。
var recoveryStatusMachine = statusMachine{
	entity: "CodeSave",
	transitions: map[string][]string{
		string(RecoveryStatusUnverified): {
			string(RecoveryStatusConfirmedApplied),
			string(RecoveryStatusConfirmedNotApplied),
			string(RecoveryStatusUnverifiable),
		},
		string(RecoveryStatusUnverifiable): {
			string(RecoveryStatusConfirmedApplied),
			string(RecoveryStatusConfirmedNotApplied),
		},
		// 已确认的事实可以在后续阶段的核验中被新的已确认事实取代。
		string(RecoveryStatusConfirmedApplied): {
			string(RecoveryStatusConfirmedApplied),
			string(RecoveryStatusConfirmedNotApplied),
		},
		string(RecoveryStatusConfirmedNotApplied): {
			string(RecoveryStatusConfirmedApplied),
			string(RecoveryStatusConfirmedNotApplied),
		},
	},
	terminal: []string{
		string(RecoveryStatusConfirmedApplied),
		string(RecoveryStatusConfirmedNotApplied),
	},
}

// String 返回恢复结论的字符串形式。
func (s RecoveryStatus) String() string {
	return string(s)
}

// Validate 校验恢复结论是否属于已知取值。
func (s RecoveryStatus) Validate() error {
	if strings.TrimSpace(string(s)) == "" {
		return nil
	}
	if recoveryStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("CodeSave", "recovery_status", "未知的恢复结论 "+string(s))
}

// normalized 把空值归一化为 unverified，其余取值原样返回。
//
// 空值表示核验字段写入之前的记录，等价于"尚未核验"；它不是一条独立的取值，
// 也不允许出现在状态迁移表里。
func (s RecoveryStatus) normalized() RecoveryStatus {
	if strings.TrimSpace(string(s)) == "" {
		return RecoveryStatusUnverified
	}
	return s
}

// NeedsVerification 判断记录是否仍然需要核验才能继续。
func (s RecoveryStatus) NeedsVerification() bool {
	switch s {
	case RecoveryStatusConfirmedApplied, RecoveryStatusConfirmedNotApplied:
		return false
	default:
		return true
	}
}

// gitCommitSHAPattern 约束 Git 提交 SHA 的书写形式。
//
// SHA-1 仓库为 40 位，SHA-256 仓库为 64 位；两者都只允许小写十六进制字符。
var gitCommitSHAPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidateGitCommitSHA 校验 Git 提交 SHA。
func ValidateGitCommitSHA(field, value string) error {
	if !gitCommitSHAPattern.MatchString(strings.TrimSpace(value)) {
		return invalidField("GitCommit", field, "提交 SHA 必须是 40 或 64 位小写十六进制")
	}
	return nil
}

// ValidateTaskBranchForTask 校验分支名与任务标识一致。
//
// 分支名的生成与校验统一由 TaskBranchName 提供：SourceProvider 与 CodeSaver
// 不得各自拼接或推断分支名，否则容易出现无法察觉的差异。
func ValidateTaskBranchForTask(taskID, branch string) error {
	expected, err := TaskBranchName(taskID)
	if err != nil {
		return err
	}
	actual := strings.TrimSpace(branch)
	if actual == expected {
		return nil
	}
	return invalidField(
		"GitBranch",
		"task_branch",
		"任务分支 "+actual+" 与约定的 "+expected+" 不一致",
	)
}

// GitAuthor 是执行 Git 提交时使用的作者身份。
//
// 身份只在单条 Git 命令上生效（通过 -c 参数），不写入仓库或全局配置。
type GitAuthor struct {
	// Name 是作者姓名。
	Name string `json:"name"`
	// Email 是作者邮箱。
	Email string `json:"email"`
}

// IsZero 判断作者身份是否为空。
func (a GitAuthor) IsZero() bool {
	return strings.TrimSpace(a.Name) == "" && strings.TrimSpace(a.Email) == ""
}

// Validate 校验作者身份。
//
// 不允许伪造：字段必须由调用方显式给出，且不能包含 Git 配置或命令行元字符。
func (a GitAuthor) Validate() error {
	name := strings.TrimSpace(a.Name)
	email := strings.TrimSpace(a.Email)
	if name == "" || email == "" {
		return invalidField("GitAuthor", "name", "作者姓名与邮箱必须同时给出")
	}
	if name != a.Name || email != a.Email {
		return invalidField("GitAuthor", "name", "作者姓名与邮箱首尾不能包含空白")
	}
	if len(name) > 128 || len(email) > 254 {
		return invalidField("GitAuthor", "name", "作者姓名或邮箱过长")
	}
	for _, char := range name {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' {
			return invalidField("GitAuthor", "name", "作者姓名不能包含控制字符")
		}
	}
	for _, char := range email {
		if char == 0 || char == '\n' || char == '\r' || char == '\t' || char == ' ' {
			return invalidField("GitAuthor", "email", "作者邮箱不能包含空白或控制字符")
		}
	}
	if !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return invalidField("GitAuthor", "email", "作者邮箱格式非法")
	}
	return nil
}

// CodeSaveRecord 是一次代码保存操作的完整记录。
//
// 一条记录对应一次 CodeSaver 调用，Commit、Push 与 Merge 的结果分别保存，
// 因此中断或失败后仍能定位最后一个已确认完成的阶段。
type CodeSaveRecord struct {
	ID ID `json:"id"`
	// ProjectID 是可选的项目标识。
	ProjectID string `json:"project_id,omitempty"`
	// TaskID 是任务标识，用于校验任务分支 agent/<task-id>。
	TaskID string `json:"task_id"`
	// ContainerID 是容器登记记录 ID。
	ContainerID string `json:"container_id"`
	// DockerID 是执行时使用的 Docker 容器 ID。
	DockerID string `json:"docker_id,omitempty"`
	// AttemptID 是发起本次保存的执行尝试，用于从尝试反查保存结果。
	AttemptID string `json:"attempt_id,omitempty"`
	// RemoteName 是使用的 Git 远程名称，例如 origin。
	RemoteName string `json:"remote_name"`
	// Repository 是脱敏后的仓库地址，可以安全写入日志与数据库。
	Repository string `json:"repository,omitempty"`
	// TaskBranch 是任务分支名，固定为 agent/<task-id>。
	TaskBranch string `json:"task_branch"`
	// BaseBranch 是识别出的基础分支逻辑名称，例如 main。
	BaseBranch string `json:"base_branch,omitempty"`
	// CommitStatus 是提交结果。
	CommitStatus CommitStatus `json:"commit_status"`
	// CommitSHA 是提交产生的 commit SHA。
	CommitSHA string `json:"commit_sha,omitempty"`
	// CommitMessage 是提交信息的摘要，不保存完整多行内容。
	CommitMessage string `json:"commit_message,omitempty"`
	// PushStatus 是任务分支的推送结果。
	PushStatus PushStatus `json:"push_status"`
	// RemoteCommitSHA 是核验到的远程任务分支 commit。
	RemoteCommitSHA string `json:"remote_commit_sha,omitempty"`
	// MergeEnabled 表示是否请求在推送成功后合并基础分支。
	MergeEnabled bool `json:"merge_enabled"`
	// MergeStatus 是合并结果。
	MergeStatus MergeStatus `json:"merge_status"`
	// MergeSHA 是合并后基础分支的 commit。
	MergeSHA string `json:"merge_sha,omitempty"`
	// Stage 是最后一个进入的操作阶段，用于中断后定位。
	Stage string `json:"stage,omitempty"`
	// ErrorCode 是可诊断的错误分类代码。
	ErrorCode string `json:"error_code,omitempty"`
	// ErrorSummary 是脱敏后的失败摘要。
	ErrorSummary string `json:"error_summary,omitempty"`
	// CreatedAt 是记录创建时间。
	CreatedAt time.Time `json:"created_at"`
	// StartedAt 是开始执行远程操作的时间。
	StartedAt *time.Time `json:"started_at,omitempty"`
	// FinishedAt 是操作结束时间。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// UpdatedAt 是记录最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
	// BaselineCommitSHA 是提交前 HEAD 指向的 commit。
	//
	// 它只在提交之前读取并落盘，用于判断"记录仍是 pending 时 HEAD 是否已经被本次
	// 操作推进"。历史记录没有该值，表示无法判定，不得推断。
	BaselineCommitSHA string `json:"baseline_commit_sha,omitempty"`
	// BaselineBaseCommitSHA 是合并前远程基础分支指向的 commit。
	//
	// 仅凭"基础分支发生变化"不足以证明是本次合并造成的，必须与预期的合并提交
	// 一起比对；没有该值时同样只能转人工核验。
	BaselineBaseCommitSHA string `json:"baseline_base_commit_sha,omitempty"`
	// PersistStatus 表达最后一次状态写入是否成功。
	PersistStatus PersistStatus `json:"persist_status,omitempty"`
	// PersistError 是最后一次写入失败的脱敏摘要，写入成功后清空。
	PersistError string `json:"persist_error,omitempty"`
	// RecoveryStatus 是只读核验得到的恢复结论，空值表示尚未核验。
	RecoveryStatus RecoveryStatus `json:"recovery_status,omitempty"`
}

// NewCodeSaveRecord 创建一条待执行的代码保存记录。
//
// mergeEnabled 为 false 时合并状态直接记为 not_requested：不允许因为开启了代码
// 保存就推断用户同意合并主分支。
func NewCodeSaveRecord(
	id ID,
	taskID, containerID, dockerID, remoteName, repository, taskBranch, baseBranch string,
	mergeEnabled bool,
	at time.Time,
) (CodeSaveRecord, error) {
	createdAt := normalizeTime(at)
	if id.IsEmpty() {
		id = NewID()
	}
	mergeStatus := MergeStatusNotRequested
	if mergeEnabled {
		mergeStatus = MergeStatusPending
	}
	record := CodeSaveRecord{
		ID:           id,
		TaskID:       strings.TrimSpace(taskID),
		ContainerID:  strings.TrimSpace(containerID),
		DockerID:     strings.TrimSpace(dockerID),
		RemoteName:   strings.TrimSpace(remoteName),
		Repository:   strings.TrimSpace(repository),
		TaskBranch:   strings.TrimSpace(taskBranch),
		BaseBranch:   strings.TrimSpace(baseBranch),
		CommitStatus: CommitStatusPending,
		PushStatus:   PushStatusPending,
		MergeEnabled: mergeEnabled,
		MergeStatus:  mergeStatus,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
	if err := record.Validate(); err != nil {
		return CodeSaveRecord{}, err
	}
	return record, nil
}

// Validate 校验记录是否完整自洽。
func (r CodeSaveRecord) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.TaskID) == "" {
		return invalidField("CodeSave", "task_id", "任务标识不能为空")
	}
	if err := ValidateTaskBranchForTask(r.TaskID, r.TaskBranch); err != nil {
		return err
	}
	if strings.TrimSpace(r.ContainerID) == "" {
		return invalidField("CodeSave", "container_id", "容器标识不能为空")
	}
	if strings.TrimSpace(r.RemoteName) == "" {
		return invalidField("CodeSave", "remote_name", "Git 远程名称不能为空")
	}
	if base := strings.TrimSpace(r.BaseBranch); base != "" {
		if err := ValidateGitBranchName(base); err != nil {
			return err
		}
		if base == r.TaskBranch {
			return invalidField("CodeSave", "base_branch", "基础分支不能与任务分支相同")
		}
	}
	if err := r.CommitStatus.Validate(); err != nil {
		return err
	}
	if err := r.PushStatus.Validate(); err != nil {
		return err
	}
	if err := r.MergeStatus.Validate(); err != nil {
		return err
	}
	if r.CommitStatus == CommitStatusSucceeded {
		if err := ValidateGitCommitSHA("commit_sha", r.CommitSHA); err != nil {
			return err
		}
	}
	if r.PushStatus == PushStatusSucceeded {
		if err := ValidateGitCommitSHA("remote_commit_sha", r.RemoteCommitSHA); err != nil {
			return err
		}
	}
	if r.MergeStatus == MergeStatusSucceeded {
		if err := ValidateGitCommitSHA("merge_sha", r.MergeSHA); err != nil {
			return err
		}
	}
	if !r.MergeEnabled && r.MergeStatus != MergeStatusNotRequested {
		return invalidField(
			"CodeSave",
			"merge_status",
			"未请求合并时合并状态必须是 not_requested",
		)
	}
	if err := r.PersistStatus.Validate(); err != nil {
		return err
	}
	if err := r.RecoveryStatus.Validate(); err != nil {
		return err
	}
	if baseline := strings.TrimSpace(r.BaselineCommitSHA); baseline != "" {
		if err := ValidateGitCommitSHA("baseline_commit_sha", baseline); err != nil {
			return err
		}
	}
	if baseline := strings.TrimSpace(r.BaselineBaseCommitSHA); baseline != "" {
		if err := ValidateGitCommitSHA("baseline_base_commit_sha", baseline); err != nil {
			return err
		}
	}
	if !r.MergeEnabled && r.MergeStatus != MergeStatusNotRequested {
		return invalidField(
			"CodeSave",
			"merge_status",
			"未请求合并时合并状态必须是 not_requested",
		)
	}
	return nil
}

// Start 记录远程操作开始时间。
func (r *CodeSaveRecord) Start(stage string, at time.Time) {
	if r == nil {
		return
	}
	now := normalizeTime(at)
	r.Stage = strings.TrimSpace(stage)
	if r.StartedAt == nil {
		r.StartedAt = &now
	}
	r.UpdatedAt = now
}

// MarkCommit 记录提交结果。
//
// 提交成功时必须给出新的 commit SHA；其他状态不允许携带 SHA，避免把未核验的
// 提交当成成功。
func (r *CodeSaveRecord) MarkCommit(
	status CommitStatus,
	sha, message string,
	at time.Time,
) error {
	if r == nil {
		return invalidField("CodeSave", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if err := commitStatusMachine.validate(string(r.CommitStatus), string(status)); err != nil {
		return err
	}
	if status == CommitStatusSucceeded {
		if err := ValidateGitCommitSHA("commit_sha", sha); err != nil {
			return err
		}
	} else if strings.TrimSpace(sha) != "" {
		if err := ValidateGitCommitSHA("commit_sha", sha); err != nil {
			return err
		}
	}
	r.CommitStatus = status
	r.CommitSHA = strings.TrimSpace(sha)
	r.CommitMessage = summarizeCommitMessage(message)
	r.Stage = "commit"
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// MarkPush 记录任务分支的推送结果。
func (r *CodeSaveRecord) MarkPush(status PushStatus, remoteSHA string, at time.Time) error {
	if r == nil {
		return invalidField("CodeSave", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if err := pushStatusMachine.validate(string(r.PushStatus), string(status)); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(remoteSHA)
	if status == PushStatusSucceeded {
		if err := ValidateGitCommitSHA("remote_commit_sha", trimmed); err != nil {
			return err
		}
	} else if trimmed != "" {
		if err := ValidateGitCommitSHA("remote_commit_sha", trimmed); err != nil {
			return err
		}
	}
	r.PushStatus = status
	r.RemoteCommitSHA = trimmed
	r.Stage = "push"
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// MarkMerge 记录基础分支的合并结果。
func (r *CodeSaveRecord) MarkMerge(status MergeStatus, mergeSHA string, at time.Time) error {
	if r == nil {
		return invalidField("CodeSave", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if status != MergeStatusNotRequested && !r.MergeEnabled {
		return invalidField("CodeSave", "merge_status", "未请求合并时不允许记录合并结果")
	}
	if err := mergeStatusMachine.validate(string(r.MergeStatus), string(status)); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(mergeSHA)
	if status == MergeStatusSucceeded {
		if err := ValidateGitCommitSHA("merge_sha", trimmed); err != nil {
			return err
		}
	} else if trimmed != "" {
		if err := ValidateGitCommitSHA("merge_sha", trimmed); err != nil {
			return err
		}
	}
	r.MergeStatus = status
	r.MergeSHA = trimmed
	r.Stage = "merge"
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// MarkError 记录失败阶段与可诊断的错误分类。
func (r *CodeSaveRecord) MarkError(stage, code, summary string, at time.Time) {
	if r == nil {
		return
	}
	r.Stage = strings.TrimSpace(stage)
	r.ErrorCode = strings.TrimSpace(code)
	r.ErrorSummary = summarizeCommitMessage(summary)
	r.UpdatedAt = normalizeTime(at)
}

// Finish 记录操作结束时间。
func (r *CodeSaveRecord) Finish(at time.Time) {
	if r == nil {
		return
	}
	now := normalizeTime(at)
	r.FinishedAt = &now
	r.UpdatedAt = now
}

// MarkBaselineCommit 记录提交前的 HEAD，作为"是否已经提交"的判定基线。
//
// 基线必须在执行提交之前读取并落盘：事后补写会把未知状态伪造成已知状态。
func (r *CodeSaveRecord) MarkBaselineCommit(sha string, at time.Time) error {
	if r == nil {
		return invalidField("CodeSave", "", "实体为空")
	}
	trimmed := strings.TrimSpace(sha)
	if err := ValidateGitCommitSHA("baseline_commit_sha", trimmed); err != nil {
		return err
	}
	r.BaselineCommitSHA = trimmed
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// MarkBaselineBaseCommit 记录合并前的远程基础分支 commit。
//
// 与提交基线同理：它描述"合并之前远程基础分支是什么"，用于把后续观察到的变化
// 归因为本次合并，而不是任意一次他人推送。
func (r *CodeSaveRecord) MarkBaselineBaseCommit(sha string, at time.Time) error {
	if r == nil {
		return invalidField("CodeSave", "", "实体为空")
	}
	trimmed := strings.TrimSpace(sha)
	if err := ValidateGitCommitSHA("baseline_base_commit_sha", trimmed); err != nil {
		return err
	}
	r.BaselineBaseCommitSHA = trimmed
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// MarkPersisted 标记本次状态已成功写入。
//
// 写入成功后清空上一次的写入失败摘要：保留已经消失的故障会让后续读者误判记录
// 仍然不可信。
func (r *CodeSaveRecord) MarkPersisted(at time.Time) {
	if r == nil {
		return
	}
	r.PersistStatus = PersistStatusPersisted
	r.PersistError = ""
	r.UpdatedAt = normalizeTime(at)
}

// MarkPersistFailure 记录状态写入失败。
//
// 记录此时可能落后于真实 Git 状态，因此必须显式标记为 unconfirmed：后续恢复流程
// 据此拒绝"仅凭 pending 就重新执行提交、推送或合并"。
func (r *CodeSaveRecord) MarkPersistFailure(summary string, at time.Time) {
	if r == nil {
		return
	}
	r.PersistStatus = PersistStatusUnconfirmed
	r.PersistError = summarizeCommitMessage(ClipFeedbackText(summary, persistErrorLimit))
	r.UpdatedAt = normalizeTime(at)
}

// MarkRecovery 记录一次只读核验得到的恢复结论。
//
// 状态迁移由 recoveryStatusMachine 校验：已确认的事实不会被一次查询失败改写成
// 不确定状态。
func (r *CodeSaveRecord) MarkRecovery(status RecoveryStatus, at time.Time) error {
	if r == nil {
		return invalidField("CodeSave", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if err := recoveryStatusMachine.validate(string(r.RecoveryStatus.normalized()), string(status)); err != nil {
		return err
	}
	r.RecoveryStatus = status
	r.UpdatedAt = normalizeTime(at)
	return nil
}

// RecoveryConverged 判断恢复是否已经收敛为可终止的结论。
//
// 只有 confirmed_applied 与 confirmed_not_applied 是收敛结论：unverified 表示还没
// 核验，unverifiable 表示核验过了但拿不到可信事实，两者都必须继续等待人工处理。
func (r CodeSaveRecord) RecoveryConverged() bool {
	return !r.RecoveryStatus.NeedsVerification()
}

// persistErrorLimit 限制写入失败摘要的长度。
//
// 摘要会写入数据库并被 CLI 展示，必须与其余失败摘要保持同样的上限与脱敏规则。
const persistErrorLimit = 512

// Finish 记录操作结束时间。
// SavedToRemote 判断任务产生的提交是否已经确认存在于远程仓库。
//
// 只有任务分支推送成功才算安全保存；合并冲突或结果未知都不影响该判断，
// 但也不代表整个保存流程成功。
func (r CodeSaveRecord) SavedToRemote() bool {
	return r.PushStatus == PushStatusSucceeded
}

// RequiresManualRecovery 判断是否需要人工介入。
//
// 提交、推送与合并三个阶段的结果未知都必须先核验真实仓库状态：unknown 既不代表
// 成功也不代表失败，自动重试可能在结果未知时产生重复提交或覆盖远程历史。
func (r CodeSaveRecord) RequiresManualRecovery() bool {
	if r.PersistStatus.IsUnconfirmed() {
		// 记录本身可能落后于真实 Git 状态：此时 pending 不能证明操作没有发生。
		return true
	}
	switch r.CommitStatus {
	case CommitStatusUnknown:
		return true
	}
	switch r.PushStatus {
	case PushStatusConflict, PushStatusUnknown, PushStatusFailed:
		return true
	}
	switch r.MergeStatus {
	case MergeStatusConflict, MergeStatusUnknown, MergeStatusFailed:
		return true
	}
	return false
}

// HasUnresolvedSideEffect 判断记录中是否仍有结果不明的外部 Git 副作用。
//
// 与 RequiresManualRecovery 的区别：后者回答"是否需要人工介入"，本方法只回答
// "是否还有必须核验才能重试的阶段"。提交/推送/合并仍是 pending 表示对应操作可能
// 已经发生但结果没落盘；unknown 与 conflict 表示结果无法确认或需要人工处理。
//
// 核验已经给出结论时以结论为准：
//   - confirmed_not_applied 表示记录里那个待确认的操作确实没有发生。由于后一阶段
//     以"前一阶段已成功"为前提（不提交就不会推送、不推送就不会合并），该结论同时
//     覆盖后续阶段，因此不再算作未决副作用；
//   - confirmed_applied 只把已确认的阶段推进为终态，其余阶段仍需单独核验，因此
//     仍按各阶段的取值判断。
func (r CodeSaveRecord) HasUnresolvedSideEffect() bool {
	if r.RecoveryStatus.normalized() == RecoveryStatusConfirmedNotApplied {
		return false
	}
	switch r.CommitStatus {
	case CommitStatusPending, CommitStatusUnknown:
		return true
	}
	switch r.PushStatus {
	case PushStatusPending, PushStatusUnknown, PushStatusConflict:
		return true
	}
	switch r.MergeStatus {
	case MergeStatusPending, MergeStatusUnknown, MergeStatusConflict:
		return true
	}
	return false
}

// summarizeCommitMessage 生成提交信息的单行摘要。
//
// 只保留第一行并限制长度：完整的多行提交信息既不必要，也容易把敏感内容写入
// 日志与数据库。
func summarizeCommitMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return ""
	}
	if index := strings.IndexAny(trimmed, "\r\n"); index >= 0 {
		trimmed = strings.TrimSpace(trimmed[:index])
	}
	const limit = 200
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}
