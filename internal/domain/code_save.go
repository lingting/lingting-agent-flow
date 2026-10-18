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

// SavedToRemote 判断任务产生的提交是否已经确认存在于远程仓库。
//
// 只有任务分支推送成功才算安全保存；合并冲突或结果未知都不影响该判断，
// 但也不代表整个保存流程成功。
func (r CodeSaveRecord) SavedToRemote() bool {
	return r.PushStatus == PushStatusSucceeded
}

// RequiresManualRecovery 判断是否需要人工介入。
func (r CodeSaveRecord) RequiresManualRecovery() bool {
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
