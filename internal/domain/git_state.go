package domain

import (
	"fmt"
	"strings"
)

// GitWorktreeState 是一次步骤执行前后采集到的 Git 工作区状态。
//
// 该结构只记录"可以核验的事实"：当前分支、HEAD 指向的 commit、是否存在未提交
// 变更，以及采集本身是否成功。它不保存完整 diff——大仓库的 diff 既不适合放进
// 数据库，也无法作为验收证据。
//
// 采集失败与"工作区干净"必须区分：StatusRead 为 false 时其余字段没有意义，
// 不得用零值冒充"没有变更"。
type GitWorktreeState struct {
	// StatusRead 表示本次采集是否成功。
	StatusRead bool
	// Branch 是采集时的工作区分支名，分离头指针时为空。
	Branch string
	// Head 是采集时 HEAD 指向的 commit SHA，无法取得时为空。
	Head string
	// Dirty 表示工作区存在未提交变更（含未跟踪文件）。
	Dirty bool
	// Modified / Added / Deleted / Renamed / Staged / Conflicts 是变更条目计数。
	Modified  int
	Added     int
	Deleted   int
	Renamed   int
	Staged    int
	Conflicts int
	// StatusError 是采集失败的可诊断摘要，采集成功时为空。
	StatusError string
}

// Validate 校验状态是否自洽。
//
// 采集失败时不允许携带"看起来成功"的字段：否则调用方会把未知状态当成干净工作区。
func (s GitWorktreeState) Validate() error {
	if !s.StatusRead {
		if strings.TrimSpace(s.Head) != "" || strings.TrimSpace(s.Branch) != "" {
			return invalidField("GitWorktree", "status_read", "采集失败时不能给出分支或 HEAD")
		}
		return nil
	}
	if s.Modified < 0 || s.Added < 0 || s.Deleted < 0 ||
		s.Renamed < 0 || s.Staged < 0 || s.Conflicts < 0 {
		return invalidField("GitWorktree", "counts", "变更计数不能为负数")
	}
	return nil
}

// Summary 返回适合写入执行记录的紧凑摘要。
func (s GitWorktreeState) Summary() string {
	if !s.StatusRead {
		if trimmed := strings.TrimSpace(s.StatusError); trimmed != "" {
			return "Git 状态读取失败: " + trimmed
		}
		return "Git 状态读取失败"
	}
	if !s.Dirty {
		return "工作区没有未提交变更"
	}
	parts := make([]string, 0, 6)
	appendCount := func(label string, value int) {
		if value > 0 {
			parts = append(parts, label+" "+itoa(value))
		}
	}
	appendCount("修改", s.Modified)
	appendCount("新增", s.Added)
	appendCount("删除", s.Deleted)
	appendCount("重命名", s.Renamed)
	appendCount("已暂存", s.Staged)
	appendCount("冲突", s.Conflicts)
	if len(parts) == 0 {
		return "工作区存在未提交变更"
	}
	return "未提交变更: " + strings.Join(parts, "、")
}

// StepPrerequisiteError 描述某个步骤尚不满足执行的前置条件。
type StepPrerequisiteError struct {
	// StepID 是未满足条件的前序步骤。
	StepID ID
	// Sequence 是前序步骤的序号。
	Sequence int
	// Status 是前序步骤的当前状态。
	Status StepStatus
}

// Error 实现 error 接口。
func (e StepPrerequisiteError) Error() string {
	return "前序步骤 " + itoa(e.Sequence) + "（" + e.StepID.String() + "）当前状态为 " +
		string(e.Status) + "，必须先完成才能执行后续步骤"
}

// CheckStepPrerequisites 校验指定步骤的全部前序步骤是否已完成。
//
// 规则固定为"所有 sequence 更小的步骤都处于 completed 或 skipped"：跳过未完成
// 的前序步骤会让后续步骤在错误的代码基础上运行，因此必须拒绝。steps 必须是按
// sequence 升序排列的同一任务的全部步骤。
func CheckStepPrerequisites(steps []Step, step Step) error {
	for _, candidate := range steps {
		if candidate.TaskID != step.TaskID {
			return blockedStepExecution("步骤 %s 不属于任务 %s", candidate.ID, step.TaskID)
		}
		if candidate.ID == step.ID {
			continue
		}
		if candidate.Sequence >= step.Sequence {
			continue
		}
		switch candidate.Status {
		case StepStatusCompleted, StepStatusSkipped:
			continue
		default:
			return fmt.Errorf("%w: %w", ErrStepExecutionBlocked, StepPrerequisiteError{
				StepID:   candidate.ID,
				Sequence: candidate.Sequence,
				Status:   candidate.Status,
			})
		}
	}
	return nil
}

// NextStepToExecute 返回按计划顺序下一个允许自动推进的步骤。
//
// 判定顺序固定为：先排除"结果不确定"的步骤（recovery_required 必须先核验现场），
// 再按 sequence 取第一个未完成的步骤。可自动推进的阶段只有规划阶段（plan，由
// Planner 执行）与开发步骤（develop，由 Developer 执行），其余阶段（prepare /
// source / verify / review / save / cleanup）不由编排器顺序执行，因此直接跳过。
//
// 返回 error 表示存在需要人工核验的现场；返回 ok=false 表示计划中已经没有可执行
// 的开发步骤。
func NextStepToExecute(steps []Step) (Step, bool, error) {
	for _, step := range steps {
		if step.Status == StepStatusRecoveryRequired {
			return Step{}, false, blockedStepExecution(
				"步骤 %s 的执行结果不确定，必须先核验现场",
				step.ID,
			)
		}
	}
	for _, step := range steps {
		if step.Type != StepTypePlan && step.Type != StepTypeDevelop {
			continue
		}
		switch step.Status {
		case StepStatusCompleted, StepStatusSkipped:
			continue
		case StepStatusRunning, StepStatusDeveloping, StepStatusVerifying:
			return Step{}, false, blockedStepExecution(
				"步骤 %s 当前状态 %s 表示已有执行占用，不能重复执行",
				step.ID,
				step.Status,
			)
		default:
			if err := CheckStepPrerequisites(steps, step); err != nil {
				return Step{}, false, err
			}
			return step, true, nil
		}
	}
	return Step{}, false, nil
}
