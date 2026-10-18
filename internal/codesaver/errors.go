package codesaver

import (
	"errors"
	"fmt"

	"github.com/lingting/agentflow/internal/domain"
)

// ErrorCode 是代码保存的可诊断错误类别。
//
// 不同阶段与不同原因必须区分：提交失败、推送冲突、合并冲突与结果不确定对应
// 完全不同的恢复方式，不能用同一个"保存失败"掩盖。
type ErrorCode string

// 代码保存错误类别。
const (
	// CodeOK 表示操作成功。
	CodeOK ErrorCode = "ok"
	// CodeInvalidArgument 表示调用参数不合法。
	CodeInvalidArgument ErrorCode = "invalid_argument"
	// CodeContainerNotFound 表示目标容器不存在或不属于 AgentFlow。
	CodeContainerNotFound ErrorCode = "container_not_found"
	// CodeContainerNotManaged 表示容器已脱离 AgentFlow 管理。
	CodeContainerNotManaged ErrorCode = "container_not_managed"
	// CodeContainerNotRunning 表示容器当前不处于可执行命令的状态。
	CodeContainerNotRunning ErrorCode = "container_not_running"
	// CodeGitUnavailable 表示容器内没有可用的 Git。
	CodeGitUnavailable ErrorCode = "git_unavailable"
	// CodeWorktreeInvalid 表示目标目录不是有效的 Git 工作区根目录。
	CodeWorktreeInvalid ErrorCode = "worktree_invalid"
	// CodeTaskBranchMismatch 表示当前分支与约定的任务分支不一致。
	CodeTaskBranchMismatch ErrorCode = "task_branch_mismatch"
	// CodeRemoteInvalid 表示 Git 远程名称缺失或地址无效。
	CodeRemoteInvalid ErrorCode = "remote_invalid"
	// CodeRepositoryAuthFailed 表示仓库认证失败或缺少可用凭据。
	CodeRepositoryAuthFailed ErrorCode = "repository_auth_failed"
	// CodeAuthorMissing 表示无法取得可用的提交身份。
	CodeAuthorMissing ErrorCode = "author_missing"
	// CodeNoChanges 表示工作区没有可提交的变更。
	CodeNoChanges ErrorCode = "no_changes"
	// CodeUnresolvedConflict 表示工作区仍有未解决的合并冲突。
	CodeUnresolvedConflict ErrorCode = "unresolved_conflict"
	// CodeStagedChangesUnknown 表示暂存区内容无法确认是否属于当前任务。
	CodeStagedChangesUnknown ErrorCode = "staged_changes_unknown"
	// CodeCommitFailed 表示提交失败，且可以确认 HEAD 没有变化。
	CodeCommitFailed ErrorCode = "commit_failed"
	// CodePushFailed 表示推送失败，且可以确认远程没有变化。
	CodePushFailed ErrorCode = "push_failed"
	// CodePushConflict 表示远程历史与本地不一致，需要人工处理。
	CodePushConflict ErrorCode = "push_conflict"
	// CodeBaseBranchMissing 表示无法可靠识别基础分支。
	CodeBaseBranchMissing ErrorCode = "base_branch_missing"
	// CodeMergeNotAllowed 表示合并前置条件不满足，拒绝执行。
	CodeMergeNotAllowed ErrorCode = "merge_not_allowed"
	// CodeMergeConflict 表示合并出现冲突。
	CodeMergeConflict ErrorCode = "merge_conflict"
	// CodeMergeFailed 表示合并或合并后的推送失败。
	CodeMergeFailed ErrorCode = "merge_failed"
	// CodeStateUncertain 表示远程操作结果无法确认，需要先核验实际状态。
	CodeStateUncertain ErrorCode = "state_uncertain"
	// CodeInterrupted 表示操作被调用方取消。
	CodeInterrupted ErrorCode = "interrupted"
	// CodeTimeout 表示操作超时。
	CodeTimeout ErrorCode = "timeout"
	// CodeRecordFailed 表示保存记录写入失败。
	CodeRecordFailed ErrorCode = "record_failed"
)

// String 返回错误类别的字符串形式。
func (c ErrorCode) String() string {
	return string(c)
}

// Error 是带类别与阶段上下文的代码保存错误。
//
// Detail 只包含脱敏后的仓库地址、分支名与裁剪后的输出，不携带任何凭据。
type Error struct {
	// Code 是错误类别。
	Code ErrorCode
	// Stage 是失败所处的阶段，例如 commit、push 或 merge。
	Stage string
	// Detail 是可读的失败原因。
	Detail string
	// Cause 是底层错误，可通过 errors.Is 判断超时或取消。
	Cause error
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	detail := e.Detail
	if detail == "" {
		return fmt.Sprintf("代码保存失败(%s): %s", e.Code, e.Stage)
	}
	return fmt.Sprintf("代码保存失败(%s): %s: %s", e.Code, e.Stage, detail)
}

// Unwrap 暴露底层错误。
func (e *Error) Unwrap() error {
	return e.Cause
}

// Is 使同类错误可以通过 errors.Is 比较错误类别。
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return other.Code == e.Code
}

// ErrorCodeOf 从错误链中提取代码保存错误类别，未分类时返回 CodeOK。
func ErrorCodeOf(err error) ErrorCode {
	if err == nil {
		return CodeOK
	}
	var structured *Error
	if errors.As(err, &structured) {
		return structured.Code
	}
	return CodeOK
}

// newError 构造代码保存错误。
func newError(code ErrorCode, stage, detail string, cause error) *Error {
	return &Error{Code: code, Stage: stage, Detail: detail, Cause: cause}
}

// recordError 把存储层错误包装为保存记录错误。
func recordError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return newError(CodeRecordFailed, stage, "写入代码保存记录失败", err)
}

// statusForErrorCode 把错误类别映射为记录的失败分类代码。
func statusForErrorCode(code ErrorCode) string {
	return string(code)
}

// commitStatusForError 把提交阶段的错误类别映射为提交状态。
func commitStatusForError(code ErrorCode) domain.CommitStatus {
	if code == CodeStateUncertain || code == CodeTimeout || code == CodeInterrupted {
		return domain.CommitStatusUnknown
	}
	return domain.CommitStatusFailed
}

// pushStatusForError 把推送阶段的错误类别映射为推送状态。
func pushStatusForError(code ErrorCode) domain.PushStatus {
	switch code {
	case CodePushConflict:
		return domain.PushStatusConflict
	case CodeStateUncertain, CodeTimeout, CodeInterrupted:
		return domain.PushStatusUnknown
	default:
		return domain.PushStatusFailed
	}
}

// mergeStatusForError 把合并阶段的错误类别映射为合并状态。
func mergeStatusForError(code ErrorCode) domain.MergeStatus {
	switch code {
	case CodeMergeConflict, CodeUnresolvedConflict:
		return domain.MergeStatusConflict
	case CodeStateUncertain, CodeTimeout, CodeInterrupted:
		return domain.MergeStatusUnknown
	case CodeMergeNotAllowed, CodeBaseBranchMissing:
		return domain.MergeStatusSkipped
	default:
		return domain.MergeStatusFailed
	}
}
