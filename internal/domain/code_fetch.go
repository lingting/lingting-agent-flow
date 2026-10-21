package domain

import (
	"strings"
	"time"
)

// CodeSourceType 是代码获取方式的类型。
//
// 第一版只有 git：其他获取方式（HTTP 下载、wget、curl、SCP）尚未实现，
// 取值留在这里是为了让持久化模型与接口一次到位，避免后续改动表结构。
type CodeSourceType string

// 代码获取方式取值。
const (
	// CodeSourceGit 表示通过 Git Clone 获取代码。
	CodeSourceGit CodeSourceType = "git"
)

// Validate 校验代码获取方式是否受支持。
//
// 未知取值直接拒绝：代码获取方式决定执行哪些命令，不能把未知方式当成默认方式执行。
func (t CodeSourceType) Validate() error {
	switch t {
	case CodeSourceGit:
		return nil
	default:
		return invalidField("CodeFetch", "source_type", "未知的代码获取方式 "+string(t))
	}
}

// String 返回代码获取方式的字符串形式。
func (t CodeSourceType) String() string {
	return string(t)
}

// TaskBranchSource 描述任务分支的实际来源。
//
// new 表示基于基础分支新建，remote 表示检出远程已存在的任务分支，
// local 表示检出本地已存在的任务分支。三者对应完全不同的副作用，
// 不能合并成一个"分支已就绪"的布尔值。
type TaskBranchSource string

// 任务分支来源取值。
const (
	// TaskBranchSourceNew 表示基于基础分支新建任务分支。
	TaskBranchSourceNew TaskBranchSource = "new"
	// TaskBranchSourceRemote 表示检出远程已存在的任务分支。
	TaskBranchSourceRemote TaskBranchSource = "remote"
	// TaskBranchSourceLocal 表示检出本地已存在的任务分支。
	TaskBranchSourceLocal TaskBranchSource = "local"
)

// Validate 校验任务分支来源是否受支持。
func (s TaskBranchSource) Validate() error {
	switch s {
	case TaskBranchSourceNew, TaskBranchSourceRemote, TaskBranchSourceLocal:
		return nil
	default:
		return invalidField("CodeFetch", "task_branch_source", "未知的任务分支来源 "+string(s))
	}
}

// String 返回任务分支来源的字符串形式。
func (s TaskBranchSource) String() string {
	return string(s)
}

// CodeFetchStatus 描述一次代码初始化发生了什么。
//
// 它与"代码是否可用"是同一件事的两个侧面：只有 succeeded 表示容器内工作区
// 已经就绪，running 与 pending 都不代表代码已经存在。
type CodeFetchStatus string

// 代码初始化状态取值。
const (
	// CodeFetchStatusPending 表示已登记但尚未开始执行。
	CodeFetchStatusPending CodeFetchStatus = "pending"
	// CodeFetchStatusRunning 表示正在容器内执行获取与分支初始化。
	CodeFetchStatusRunning CodeFetchStatus = "running"
	// CodeFetchStatusSucceeded 表示代码已就位且任务分支已核验。
	CodeFetchStatusSucceeded CodeFetchStatus = "succeeded"
	// CodeFetchStatusFailed 表示初始化失败，失败阶段与原因已记录。
	CodeFetchStatusFailed CodeFetchStatus = "failed"
	// CodeFetchStatusCancelled 表示调用方取消了初始化。
	CodeFetchStatusCancelled CodeFetchStatus = "cancelled"
	// CodeFetchStatusTimedOut 表示初始化超时被终止。
	CodeFetchStatusTimedOut CodeFetchStatus = "timed_out"
	// CodeFetchStatusSkipped 表示目标工作区已就绪且未被本次操作改动。
	//
	// 同一 Attempt 已经成功初始化时返回该状态，而不是重新 Clone 覆盖代码。
	CodeFetchStatusSkipped CodeFetchStatus = "skipped"
	// CodeFetchStatusUncertain 表示执行结果无法确认，必须先核验容器内实际状态。
	CodeFetchStatusUncertain CodeFetchStatus = "uncertain"
)

// codeFetchStatusMachine 定义代码初始化状态迁移。
//
// 一次记录只能结束一次：进入终态后不会被迟到的结果改写。
var codeFetchStatusMachine = statusMachine{
	entity: "CodeFetch",
	transitions: map[string][]string{
		string(CodeFetchStatusPending): {
			string(CodeFetchStatusRunning),
			string(CodeFetchStatusFailed),
			string(CodeFetchStatusCancelled),
			string(CodeFetchStatusTimedOut),
			string(CodeFetchStatusUncertain),
		},
		string(CodeFetchStatusRunning): {
			string(CodeFetchStatusSucceeded),
			string(CodeFetchStatusSkipped),
			string(CodeFetchStatusFailed),
			string(CodeFetchStatusCancelled),
			string(CodeFetchStatusTimedOut),
			string(CodeFetchStatusUncertain),
		},
		string(CodeFetchStatusUncertain): {
			string(CodeFetchStatusSucceeded),
			string(CodeFetchStatusSkipped),
			string(CodeFetchStatusFailed),
		},
	},
	terminal: []string{
		string(CodeFetchStatusSucceeded),
		string(CodeFetchStatusSkipped),
		string(CodeFetchStatusFailed),
		string(CodeFetchStatusCancelled),
		string(CodeFetchStatusTimedOut),
	},
}

// Validate 校验代码初始化状态是否属于已知取值。
func (s CodeFetchStatus) Validate() error {
	if codeFetchStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("CodeFetch", "status", "未知的代码初始化状态 "+string(s))
}

// IsTerminal 判断状态是否已经结束。
func (s CodeFetchStatus) IsTerminal() bool {
	return codeFetchStatusMachine.isTerminal(string(s))
}

// Succeeded 判断状态是否表示代码已经就绪。
//
// skipped 也算就绪：它表示该 Attempt 的工作区在此之前已经完成初始化，
// 本次没有做任何改动。
func (s CodeFetchStatus) Succeeded() bool {
	return s == CodeFetchStatusSucceeded || s == CodeFetchStatusSkipped
}

// String 返回状态的字符串形式。
func (s CodeFetchStatus) String() string {
	return string(s)
}

// CodeFetchStage 是一次代码初始化所处的阶段。
//
// 失败记录必须带上阶段：只记录"失败"无法区分是仓库不可访问、目标目录被占用，
// 还是分支创建失败。
type CodeFetchStage string

// 代码初始化阶段取值。
const (
	// CodeFetchStageValidate 表示校验任务、尝试与容器归属。
	CodeFetchStageValidate CodeFetchStage = "validate"
	// CodeFetchStageProbe 表示容器内 Git 与目标目录的前置探测。
	CodeFetchStageProbe CodeFetchStage = "probe"
	// CodeFetchStageClone 表示在容器内执行 Git Clone。
	CodeFetchStageClone CodeFetchStage = "clone"
	// CodeFetchStageBaseBranch 表示识别基础分支。
	CodeFetchStageBaseBranch CodeFetchStage = "base_branch"
	// CodeFetchStageTaskBranch 表示创建或切换任务分支。
	CodeFetchStageTaskBranch CodeFetchStage = "task_branch"
	// CodeFetchStageVerify 表示核验工作区与当前 HEAD。
	CodeFetchStageVerify CodeFetchStage = "verify"
	// CodeFetchStagePersist 表示写入初始化结果。
	CodeFetchStagePersist CodeFetchStage = "persist"
)

// CodeFetchRecord 是一次代码初始化操作的完整记录。
//
// 一条记录对应一次针对具体 Attempt 的初始化调用：它保存代码来源、目标工作区、
// 实际分支与 HEAD，以及失败阶段与脱敏后的错误摘要。记录只追加不覆盖，
// 因此"同一 Attempt 已初始化成功"可以由记录直接判断，而不需要重新探测文件系统。
//
// 该结构不保存任何凭据：repository 是脱敏后的地址，error_summary 是裁剪后的摘要。
type CodeFetchRecord struct {
	ID ID `json:"id"`
	// ProjectID 是可选的项目标识。
	ProjectID string `json:"project_id,omitempty"`
	// TaskID 是任务标识，用于校验任务分支 agent/<task-id>。
	TaskID string `json:"task_id"`
	// StepID 是发起本次初始化的步骤标识。
	StepID string `json:"step_id"`
	// AttemptID 是本次初始化所属的执行尝试。
	AttemptID string `json:"attempt_id"`
	// SourceType 是代码获取方式，第一版固定为 git。
	SourceType CodeSourceType `json:"source_type"`
	// Repository 是脱敏后的仓库地址，可以安全写入日志与数据库。
	Repository string `json:"repository,omitempty"`
	// WorkDir 是容器内目标工作目录。
	WorkDir string `json:"work_dir"`
	// ContainerID 是容器登记记录 ID。
	ContainerID string `json:"container_id"`
	// DockerID 是执行时使用的 Docker 容器 ID。
	DockerID string `json:"docker_id,omitempty"`
	// BaseBranch 是识别出的基础分支。
	BaseBranch string `json:"base_branch,omitempty"`
	// BaseSelection 记录基础分支的确定方式。
	BaseSelection GitBranchSelection `json:"base_selection,omitempty"`
	// BaseCommit 是基础分支指向的 commit SHA。
	BaseCommit string `json:"base_commit,omitempty"`
	// TaskBranch 是任务分支名，固定为 agent/<task-id>。
	TaskBranch string `json:"task_branch"`
	// TaskBranchSource 描述任务分支的来源。
	TaskBranchSource TaskBranchSource `json:"task_branch_source,omitempty"`
	// CommitSHA 是初始化完成后实际检出的 commit SHA。
	CommitSHA string `json:"commit_sha,omitempty"`
	// Status 是本次初始化的状态。
	Status CodeFetchStatus `json:"status"`
	// Stage 是最后一个进入的阶段，失败时即为失败阶段。
	Stage CodeFetchStage `json:"stage,omitempty"`
	// ErrorCode 是可诊断的错误分类代码。
	ErrorCode string `json:"error_code,omitempty"`
	// ErrorSummary 是脱敏并裁剪后的失败摘要。
	ErrorSummary string `json:"error_summary,omitempty"`
	// StartedAt 是初始化开始时间。
	StartedAt time.Time `json:"started_at"`
	// FinishedAt 是初始化结束时间，未结束时为空。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// CreatedAt 是记录创建时间。
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是记录最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// NewCodeFetchRecord 创建一条待执行的代码初始化记录。
//
// 初始状态固定为 pending：记录先于任何容器内命令写入，使执行失败也能留下痕迹。
func NewCodeFetchRecord(
	id ID,
	projectID, taskID, stepID, attemptID string,
	sourceType CodeSourceType,
	workDir, containerID, dockerID string,
	at time.Time,
) (CodeFetchRecord, error) {
	createdAt := normalizeTime(at)
	if id.IsEmpty() {
		id = NewID()
	}
	taskBranch, err := TaskBranchName(taskID)
	if err != nil {
		return CodeFetchRecord{}, err
	}
	record := CodeFetchRecord{
		ID:          id,
		ProjectID:   strings.TrimSpace(projectID),
		TaskID:      strings.TrimSpace(taskID),
		StepID:      strings.TrimSpace(stepID),
		AttemptID:   strings.TrimSpace(attemptID),
		SourceType:  sourceType,
		WorkDir:     strings.TrimSpace(workDir),
		ContainerID: strings.TrimSpace(containerID),
		DockerID:    strings.TrimSpace(dockerID),
		TaskBranch:  taskBranch,
		Status:      CodeFetchStatusPending,
		StartedAt:   createdAt,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if err := record.Validate(); err != nil {
		return CodeFetchRecord{}, err
	}
	return record, nil
}

// Validate 校验记录是否完整自洽。
func (r CodeFetchRecord) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.TaskID) == "" {
		return invalidField("CodeFetch", "task_id", "任务标识不能为空")
	}
	if strings.TrimSpace(r.StepID) == "" {
		return invalidField("CodeFetch", "step_id", "步骤标识不能为空")
	}
	if strings.TrimSpace(r.AttemptID) == "" {
		return invalidField("CodeFetch", "attempt_id", "尝试标识不能为空")
	}
	if err := r.SourceType.Validate(); err != nil {
		return err
	}
	if err := ValidateTaskBranchForTask(r.TaskID, r.TaskBranch); err != nil {
		return err
	}
	if err := ValidateContainerWorkDir(r.WorkDir); err != nil {
		return err
	}
	if strings.TrimSpace(r.ContainerID) == "" {
		return invalidField("CodeFetch", "container_id", "容器标识不能为空")
	}
	if base := strings.TrimSpace(r.BaseBranch); base != "" {
		if err := ValidateGitBranchName(base); err != nil {
			return err
		}
		if base == r.TaskBranch {
			return invalidField("CodeFetch", "base_branch", "基础分支不能与任务分支相同")
		}
	}
	if r.BaseSelection != "" {
		if err := r.BaseSelection.Validate(); err != nil {
			return err
		}
	}
	if r.TaskBranchSource != "" {
		if err := r.TaskBranchSource.Validate(); err != nil {
			return err
		}
	}
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if r.Stage != "" {
		if err := r.Stage.Validate(); err != nil {
			return err
		}
	}
	// 成功的记录必须带上可核验的 commit：没有 commit 的"成功"无法被后续步骤使用。
	if r.Status.Succeeded() {
		if err := ValidateGitCommitSHA("commit_sha", r.CommitSHA); err != nil {
			return err
		}
	} else if strings.TrimSpace(r.CommitSHA) != "" {
		if err := ValidateGitCommitSHA("commit_sha", r.CommitSHA); err != nil {
			return err
		}
	}
	if r.Status == CodeFetchStatusFailed && strings.TrimSpace(r.ErrorSummary) == "" {
		return invalidField("CodeFetch", "error_summary", "失败记录必须给出失败原因")
	}
	return nil
}

// Validate 校验代码初始化阶段是否属于已知取值。
func (s CodeFetchStage) Validate() error {
	switch s {
	case CodeFetchStageValidate,
		CodeFetchStageProbe,
		CodeFetchStageClone,
		CodeFetchStageBaseBranch,
		CodeFetchStageTaskBranch,
		CodeFetchStageVerify,
		CodeFetchStagePersist:
		return nil
	default:
		return invalidField("CodeFetch", "stage", "未知的代码初始化阶段 "+string(s))
	}
}

// String 返回阶段的字符串形式。
func (s CodeFetchStage) String() string {
	return string(s)
}

// MarkRunning 把记录推进到运行状态并记录当前阶段。
func (r *CodeFetchRecord) MarkRunning(stage CodeFetchStage, at time.Time) error {
	return r.transition(CodeFetchStatusRunning, stage, at)
}

// MarkSucceeded 记录初始化成功。
//
// 成功必须同时给出基础分支信息、任务分支来源与实际 commit SHA：
// 缺少任一项都说明工作区没有被真正核验过。
func (r *CodeFetchRecord) MarkSucceeded(
	baseBranch string,
	selection GitBranchSelection,
	baseCommit, taskBranchSource, commitSHA string,
	at time.Time,
) error {
	if r == nil {
		return invalidField("CodeFetch", "", "实体为空")
	}
	if err := codeFetchStatusMachine.validate(string(r.Status), string(CodeFetchStatusSucceeded)); err != nil {
		return err
	}
	source := TaskBranchSource(strings.TrimSpace(taskBranchSource))
	if err := source.Validate(); err != nil {
		return err
	}
	if err := ValidateGitBranchName(baseBranch); err != nil {
		return err
	}
	if err := selection.Validate(); err != nil {
		return err
	}
	if err := ValidateGitCommitSHA("base_commit", baseCommit); err != nil {
		return err
	}
	if err := ValidateGitCommitSHA("commit_sha", commitSHA); err != nil {
		return err
	}
	now := normalizeTime(at)
	r.Status = CodeFetchStatusSucceeded
	r.BaseBranch = strings.TrimSpace(baseBranch)
	r.BaseSelection = selection
	r.BaseCommit = strings.TrimSpace(baseCommit)
	r.TaskBranchSource = source
	r.CommitSHA = strings.TrimSpace(commitSHA)
	r.Stage = CodeFetchStageVerify
	r.ErrorCode = ""
	r.ErrorSummary = ""
	r.FinishedAt = &now
	r.UpdatedAt = now
	return r.Validate()
}

// MarkSkipped 记录"工作区已就绪、本次未做改动"。
//
// 它用于同一 Attempt 的重复初始化：既不重新 Clone，也不把上次的成功结论
// 覆盖成一条新的成功记录。
func (r *CodeFetchRecord) MarkSkipped(commitSHA string, at time.Time) error {
	if r == nil {
		return invalidField("CodeFetch", "", "实体为空")
	}
	if err := codeFetchStatusMachine.validate(string(r.Status), string(CodeFetchStatusSkipped)); err != nil {
		return err
	}
	if err := ValidateGitCommitSHA("commit_sha", commitSHA); err != nil {
		return err
	}
	now := normalizeTime(at)
	r.Status = CodeFetchStatusSkipped
	r.CommitSHA = strings.TrimSpace(commitSHA)
	r.Stage = CodeFetchStageVerify
	r.FinishedAt = &now
	r.UpdatedAt = now
	return r.Validate()
}

// MarkFailed 记录失败阶段、错误类别与脱敏摘要。
//
// 失败摘要由调用方保证已脱敏：本方法只做长度裁剪，不会识别其中的敏感内容。
func (r *CodeFetchRecord) MarkFailed(
	status CodeFetchStatus,
	stage CodeFetchStage,
	code, summary string,
	at time.Time,
) error {
	if r == nil {
		return invalidField("CodeFetch", "", "实体为空")
	}
	switch status {
	case CodeFetchStatusFailed, CodeFetchStatusCancelled,
		CodeFetchStatusTimedOut, CodeFetchStatusUncertain:
	default:
		return invalidField("CodeFetch", "status", "失败记录的状态只能是失败类状态")
	}
	if err := r.transition(status, stage, at); err != nil {
		return err
	}
	r.ErrorCode = strings.TrimSpace(code)
	r.ErrorSummary = summarizeCommitMessage(summary)
	return r.Validate()
}

// transition 以状态机约束推进记录状态。
func (r *CodeFetchRecord) transition(
	status CodeFetchStatus,
	stage CodeFetchStage,
	at time.Time,
) error {
	if r == nil {
		return invalidField("CodeFetch", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if err := codeFetchStatusMachine.validate(string(r.Status), string(status)); err != nil {
		return err
	}
	if stage != "" {
		if err := stage.Validate(); err != nil {
			return err
		}
	}
	now := normalizeTime(at)
	r.Status = status
	if stage != "" {
		r.Stage = stage
	}
	if status.IsTerminal() {
		r.FinishedAt = &now
	}
	r.UpdatedAt = now
	return nil
}

// Succeeded 判断记录是否表示代码已就绪。
func (r CodeFetchRecord) Succeeded() bool {
	return r.Status.Succeeded()
}
