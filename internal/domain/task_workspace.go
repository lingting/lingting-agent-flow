package domain

import (
	"fmt"
	"strings"
	"time"
)

// TaskWorkspaceInitStatus 描述一次任务级工作区初始化发生了什么。
//
// 它与"代码是否可用"是同一件事的两个侧面：只有 succeeded 与 skipped 表示容器内
// 工作区已经就绪，pending 与 running 都不代表代码已经存在。
type TaskWorkspaceInitStatus string

// 任务级工作区初始化状态取值。
const (
	// TaskWorkspaceInitStatusPending 表示已登记但尚未开始执行。
	TaskWorkspaceInitStatusPending TaskWorkspaceInitStatus = "pending"
	// TaskWorkspaceInitStatusRunning 表示正在容器内执行获取与分支初始化。
	TaskWorkspaceInitStatusRunning TaskWorkspaceInitStatus = "running"
	// TaskWorkspaceInitStatusSucceeded 表示代码已就位且任务分支已核验。
	TaskWorkspaceInitStatusSucceeded TaskWorkspaceInitStatus = "succeeded"
	// TaskWorkspaceInitStatusFailed 表示初始化失败，失败阶段与原因已记录。
	TaskWorkspaceInitStatusFailed TaskWorkspaceInitStatus = "failed"
	// TaskWorkspaceInitStatusCancelled 表示调用方取消了初始化。
	TaskWorkspaceInitStatusCancelled TaskWorkspaceInitStatus = "cancelled"
	// TaskWorkspaceInitStatusTimedOut 表示初始化超时被终止。
	TaskWorkspaceInitStatusTimedOut TaskWorkspaceInitStatus = "timed_out"
	// TaskWorkspaceInitStatusSkipped 表示目标工作区已就绪且未被本次操作改动。
	TaskWorkspaceInitStatusSkipped TaskWorkspaceInitStatus = "skipped"
	// TaskWorkspaceInitStatusUncertain 表示远端执行结果无法确认。
	//
	// 它不是终态：必须先做只读核验再收敛，不得据此重放 Clone。
	TaskWorkspaceInitStatusUncertain TaskWorkspaceInitStatus = "uncertain"
)

// taskWorkspaceInitStatusMachine 定义任务级工作区初始化的状态迁移。
//
// 与尝试级 code_fetches 同构：一次记录只能结束一次，进入终态后不会被迟到的结果
// 改写。uncertain 不是终态，它只能经只读核验收敛为确定的结论。
var taskWorkspaceInitStatusMachine = statusMachine{
	entity: "TaskWorkspaceInit",
	transitions: map[string][]string{
		string(TaskWorkspaceInitStatusPending): {
			string(TaskWorkspaceInitStatusRunning),
			string(TaskWorkspaceInitStatusFailed),
			string(TaskWorkspaceInitStatusCancelled),
			string(TaskWorkspaceInitStatusTimedOut),
			string(TaskWorkspaceInitStatusUncertain),
		},
		string(TaskWorkspaceInitStatusRunning): {
			string(TaskWorkspaceInitStatusSucceeded),
			string(TaskWorkspaceInitStatusSkipped),
			string(TaskWorkspaceInitStatusFailed),
			string(TaskWorkspaceInitStatusCancelled),
			string(TaskWorkspaceInitStatusTimedOut),
			string(TaskWorkspaceInitStatusUncertain),
		},
		string(TaskWorkspaceInitStatusUncertain): {
			string(TaskWorkspaceInitStatusSucceeded),
			string(TaskWorkspaceInitStatusSkipped),
			string(TaskWorkspaceInitStatusFailed),
		},
	},
	terminal: []string{
		string(TaskWorkspaceInitStatusSucceeded),
		string(TaskWorkspaceInitStatusSkipped),
		string(TaskWorkspaceInitStatusFailed),
		string(TaskWorkspaceInitStatusCancelled),
		string(TaskWorkspaceInitStatusTimedOut),
	},
}

// Validate 校验任务级工作区初始化状态是否属于已知取值。
func (s TaskWorkspaceInitStatus) Validate() error {
	if taskWorkspaceInitStatusMachine.isKnown(string(s)) {
		return nil
	}
	return invalidField("TaskWorkspaceInit", "status", "未知的工作区初始化状态 "+string(s))
}

// IsTerminal 判断状态是否已经结束。
func (s TaskWorkspaceInitStatus) IsTerminal() bool {
	return taskWorkspaceInitStatusMachine.isTerminal(string(s))
}

// Succeeded 判断状态是否表示工作区已经就绪。
//
// skipped 也算就绪：它表示该任务在该容器上的工作区此前已经完成初始化，
// 本次没有做任何改动。
func (s TaskWorkspaceInitStatus) Succeeded() bool {
	return s == TaskWorkspaceInitStatusSucceeded || s == TaskWorkspaceInitStatusSkipped
}

// String 返回状态的字符串形式。
func (s TaskWorkspaceInitStatus) String() string {
	return string(s)
}

// TaskWorkspaceInitRecord 是一次任务级工作区初始化的完整记录。
//
// 一条记录对应一次针对"任务 + 容器"的初始化调用：它保存代码来源、目标工作区、
// 实际分支与 HEAD，以及失败阶段与脱敏后的错误摘要。记录只追加不覆盖，因此
// "该任务在该容器上是否已经初始化成功"可以由记录直接判断。
//
// container_id 是容器代次判据：任务重新开始时创建新容器，旧容器上的成功记录
// 不适用于新容器，历史记录一律保留。
//
// 该结构不保存任何凭据：repository 是脱敏后的地址，error_summary 是裁剪后的摘要。
type TaskWorkspaceInitRecord struct {
	ID ID `json:"id"`
	// TaskID 是任务标识，用于校验任务分支 agent/<task-id>。
	TaskID string `json:"task_id"`
	// ContainerID 是容器登记记录 ID，同时作为容器代次判据。
	ContainerID string `json:"container_id"`
	// DockerID 是执行时使用的 Docker 容器 ID。
	DockerID string `json:"docker_id,omitempty"`
	// SourceType 是代码获取方式，第一版固定为 git。
	SourceType CodeSourceType `json:"source_type"`
	// Repository 是脱敏后的仓库地址，可以安全写入日志与数据库。
	Repository string `json:"repository,omitempty"`
	// WorkDir 是容器内目标工作目录。
	WorkDir string `json:"work_dir"`
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
	Status TaskWorkspaceInitStatus `json:"status"`
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

// NewTaskWorkspaceInitRecord 创建一条待执行的任务级工作区初始化记录。
//
// 初始状态固定为 pending：记录先于任何容器内命令写入，使执行失败也能留下痕迹。
func NewTaskWorkspaceInitRecord(
	id ID,
	taskID, containerID, dockerID string,
	sourceType CodeSourceType,
	workDir string,
	at time.Time,
) (TaskWorkspaceInitRecord, error) {
	createdAt := normalizeTime(at)
	if id.IsEmpty() {
		id = NewID()
	}
	taskBranch, err := TaskBranchName(taskID)
	if err != nil {
		return TaskWorkspaceInitRecord{}, err
	}
	record := TaskWorkspaceInitRecord{
		ID:          id,
		TaskID:      strings.TrimSpace(taskID),
		ContainerID: strings.TrimSpace(containerID),
		DockerID:    strings.TrimSpace(dockerID),
		SourceType:  sourceType,
		WorkDir:     strings.TrimSpace(workDir),
		TaskBranch:  taskBranch,
		Status:      TaskWorkspaceInitStatusPending,
		StartedAt:   createdAt,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if err := record.Validate(); err != nil {
		return TaskWorkspaceInitRecord{}, err
	}
	return record, nil
}

// Validate 校验记录是否完整自洽。
func (r TaskWorkspaceInitRecord) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.TaskID) == "" {
		return invalidField("TaskWorkspaceInit", "task_id", "任务标识不能为空")
	}
	if strings.TrimSpace(r.ContainerID) == "" {
		return invalidField("TaskWorkspaceInit", "container_id", "容器标识不能为空")
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
	if base := strings.TrimSpace(r.BaseBranch); base != "" {
		if err := ValidateGitBranchName(base); err != nil {
			return err
		}
		if base == r.TaskBranch {
			return invalidField(
				"TaskWorkspaceInit",
				"base_branch",
				"基础分支不能与任务分支相同",
			)
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
	if r.Status == TaskWorkspaceInitStatusFailed && strings.TrimSpace(r.ErrorSummary) == "" {
		return invalidField("TaskWorkspaceInit", "error_summary", "失败记录必须给出失败原因")
	}
	return nil
}

// MarkRunning 把记录推进到运行状态并记录当前阶段。
func (r *TaskWorkspaceInitRecord) MarkRunning(stage CodeFetchStage, at time.Time) error {
	return r.transition(TaskWorkspaceInitStatusRunning, stage, at)
}

// MarkSucceeded 记录初始化成功。
//
// 成功必须同时给出基础分支信息、任务分支来源与实际 commit SHA：
// 缺少任一项都说明工作区没有被真正核验过。
func (r *TaskWorkspaceInitRecord) MarkSucceeded(
	baseBranch string,
	selection GitBranchSelection,
	baseCommit, taskBranchSource, commitSHA string,
	at time.Time,
) error {
	if r == nil {
		return invalidField("TaskWorkspaceInit", "", "实体为空")
	}
	if err := taskWorkspaceInitStatusMachine.validate(
		string(r.Status),
		string(TaskWorkspaceInitStatusSucceeded),
	); err != nil {
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
	r.Status = TaskWorkspaceInitStatusSucceeded
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
// 它用于同一任务同一容器的重复初始化：既不重新 Clone，也不把上次的成功结论
// 覆盖成一条新的成功记录。
func (r *TaskWorkspaceInitRecord) MarkSkipped(commitSHA string, at time.Time) error {
	if r == nil {
		return invalidField("TaskWorkspaceInit", "", "实体为空")
	}
	if err := taskWorkspaceInitStatusMachine.validate(
		string(r.Status),
		string(TaskWorkspaceInitStatusSkipped),
	); err != nil {
		return err
	}
	if err := ValidateGitCommitSHA("commit_sha", commitSHA); err != nil {
		return err
	}
	now := normalizeTime(at)
	r.Status = TaskWorkspaceInitStatusSkipped
	r.CommitSHA = strings.TrimSpace(commitSHA)
	r.Stage = CodeFetchStageVerify
	r.FinishedAt = &now
	r.UpdatedAt = now
	return r.Validate()
}

// MarkFailed 记录失败阶段、错误类别与脱敏摘要。
//
// 失败摘要由调用方保证已脱敏：本方法只做长度裁剪，不会识别其中的敏感内容。
func (r *TaskWorkspaceInitRecord) MarkFailed(
	status TaskWorkspaceInitStatus,
	stage CodeFetchStage,
	code, summary string,
	at time.Time,
) error {
	if r == nil {
		return invalidField("TaskWorkspaceInit", "", "实体为空")
	}
	switch status {
	case TaskWorkspaceInitStatusFailed,
		TaskWorkspaceInitStatusCancelled,
		TaskWorkspaceInitStatusTimedOut,
		TaskWorkspaceInitStatusUncertain:
	default:
		return invalidField(
			"TaskWorkspaceInit",
			"status",
			"失败记录的状态只能是失败类状态",
		)
	}
	if err := r.transition(status, stage, at); err != nil {
		return err
	}
	r.ErrorCode = strings.TrimSpace(code)
	r.ErrorSummary = summarizeCommitMessage(summary)
	return r.Validate()
}

// transition 以状态机约束推进记录状态。
func (r *TaskWorkspaceInitRecord) transition(
	status TaskWorkspaceInitStatus,
	stage CodeFetchStage,
	at time.Time,
) error {
	if r == nil {
		return invalidField("TaskWorkspaceInit", "", "实体为空")
	}
	if err := status.Validate(); err != nil {
		return err
	}
	if err := taskWorkspaceInitStatusMachine.validate(string(r.Status), string(status)); err != nil {
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

// Succeeded 判断记录是否表示工作区已就绪。
func (r TaskWorkspaceInitRecord) Succeeded() bool {
	return r.Status.Succeeded()
}

// TaskWorkspaceState 是判定"任务级工作区是否需要初始化"所需的持久化事实。
//
// 它只包含从数据库读到的字段与一次真实容器核验结论，不包含任何推测值。
type TaskWorkspaceState struct {
	// Record 是该任务在当前容器上的最新一条初始化记录。
	//
	// 为 nil 表示该任务在该容器上从未初始化过。调用方不得用"工作区看起来是空的"
	// 之类的推断替代这条事实。
	Record *TaskWorkspaceInitRecord
	// ContainerUsable 表示任务记录关联的容器当前受管理且运行中。
	//
	// 只有 Task.ContainerID 非空时才可能有意义：容器标识为空时该字段必须为 false。
	ContainerUsable bool
}

// DecideTaskWorkspaceNext 依据持久化事实判断任务级工作区是否需要初始化。
//
// 规则（顺序固定，只在 domain 中定义一次）：
//  1. 容器不可用 → 返回 ErrInvalidState：调用方本不该在这里询问，阶段判定应已先
//     要求准备容器；
//  2. 没有记录 → 需要初始化；
//  3. 记录不属于当前容器 → 需要初始化（任务重新开始时创建了新容器，旧容器上的
//     成功记录不适用于新容器）；
//  4. 记录成功或跳过 → 不需要初始化；
//  5. 记录结果不确定 → 返回 ErrInvalidState：必须先做只读核验，不得重放 Clone；
//  6. 记录仍在推进 → 返回 ErrInvalidState：已有执行占用同一份现场，不得抢占；
//  7. 记录失败、取消或超时 → 需要初始化（创建新记录，不复用旧记录）。
//
// 该函数是纯函数：不读数据库、不启动进程、不产生任何远程副作用。
func DecideTaskWorkspaceNext(state TaskWorkspaceState) (bool, error) {
	if !state.ContainerUsable {
		return false, fmt.Errorf(
			"%w: 任务容器当前不可用，无法判定工作区是否需要初始化",
			ErrInvalidState,
		)
	}
	record := state.Record
	if record == nil {
		return true, nil
	}
	switch record.Status {
	case TaskWorkspaceInitStatusSucceeded, TaskWorkspaceInitStatusSkipped:
		return false, nil
	case TaskWorkspaceInitStatusUncertain:
		// 远端结果不确定时现场必须保留：重放 Clone 会覆盖可能已经就绪的工作区，
		// 也无法区分"从未开始"与"已经完成但结果没落盘"。
		return false, fmt.Errorf(
			"%w: 任务 %s 在容器 %s 上的工作区初始化结果不确定（阶段 %s），"+
				"必须先核验容器内实际状态，不能重放代码获取",
			ErrInvalidState,
			record.TaskID,
			record.ContainerID,
			record.Stage,
		)
	case TaskWorkspaceInitStatusPending, TaskWorkspaceInitStatusRunning:
		return false, fmt.Errorf(
			"%w: 任务 %s 在容器 %s 上的工作区正在初始化（状态 %s），不能重复启动",
			ErrInvalidState,
			record.TaskID,
			record.ContainerID,
			record.Status,
		)
	default:
		return true, nil
	}
}

// TaskWorkspaceRecoveryInput 是一次"工作区初始化中断残留"收敛所需的持久化事实。
type TaskWorkspaceRecoveryInput struct {
	// Record 是读取到的初始化记录。
	Record TaskWorkspaceInitRecord
}

// DecideTaskWorkspaceRecovery 依据持久化事实判断中断的初始化记录是否需要收敛。
//
// 进程在容器内 Git 操作期间退出时，记录会永久停留在 pending / running。此时既无法
// 断言代码已就绪，也无法断言操作从未发生，因此只能收敛为 uncertain 并等待只读核验。
//
// 返回 next 为 true 时，目标状态固定为 uncertain，不重放任何远程操作。
func DecideTaskWorkspaceRecovery(input TaskWorkspaceRecoveryInput) (bool, TaskWorkspaceInitStatus) {
	switch input.Record.Status {
	case TaskWorkspaceInitStatusPending, TaskWorkspaceInitStatusRunning:
		return true, TaskWorkspaceInitStatusUncertain
	default:
		return false, input.Record.Status
	}
}

// TaskWorkspaceInterruptedSummary 返回中断收敛时写入的固定摘要。
//
// 摘要必须说明"结论来自启动恢复"，否则用户无法区分自动收敛与真实执行结果。
func TaskWorkspaceInterruptedSummary() string {
	return "上一次工作区初始化的进程已退出，无法确认容器内代码获取是否完成，" +
		"需要先核验容器内实际状态，不能重放代码获取。"
}
