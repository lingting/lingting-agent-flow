package source

import (
	"errors"
	"fmt"
)

// ErrorCode 是代码获取的可诊断错误类别。
//
// 不同原因必须区分：仓库地址非法、容器不可用、目标目录冲突、认证缺失与执行结果
// 不确定对应完全不同的处理方式，不能用同一个"失败"掩盖。
type ErrorCode string

// 代码获取错误类别。
const (
	// CodeOK 表示操作成功。
	CodeOK ErrorCode = "ok"
	// CodeInvalidArgument 表示调用参数不合法。
	CodeInvalidArgument ErrorCode = "invalid_argument"
	// CodeInvalidRepositoryURL 表示仓库地址不符合允许的格式。
	CodeInvalidRepositoryURL ErrorCode = "invalid_repository_url"
	// CodeContainerNotFound 表示目标容器不存在或不属于 AgentFlow。
	CodeContainerNotFound ErrorCode = "container_not_found"
	// CodeContainerNotManaged 表示容器已脱离 AgentFlow 管理。
	CodeContainerNotManaged ErrorCode = "container_not_managed"
	// CodeContainerNotRunning 表示容器当前不处于可执行命令的状态。
	CodeContainerNotRunning ErrorCode = "container_not_running"
	// CodeGitUnavailable 表示容器内没有可用的 Git。
	CodeGitUnavailable ErrorCode = "git_unavailable"
	// CodeRepositoryAccessFailed 表示无法访问仓库（网络、权限或仓库不存在）。
	CodeRepositoryAccessFailed ErrorCode = "repository_access_failed"
	// CodeRepositoryAuthRequired 表示仓库需要当前未配置的认证信息。
	CodeRepositoryAuthRequired ErrorCode = "repository_auth_required"
	// CodeTargetDirNotEmpty 表示目标工作目录已被占用。
	CodeTargetDirNotEmpty ErrorCode = "target_dir_not_empty"
	// CodeCloneInterrupted 表示 Clone 被调用方取消。
	CodeCloneInterrupted ErrorCode = "clone_interrupted"
	// CodeCloneTimeout 表示 Clone 超时。
	CodeCloneTimeout ErrorCode = "clone_timeout"
	// CodeBaseBranchMissing 表示指定的基础分支不存在或无法识别。
	CodeBaseBranchMissing ErrorCode = "base_branch_missing"
	// CodeTaskBranchConflict 表示任务分支冲突，无法安全初始化。
	CodeTaskBranchConflict ErrorCode = "task_branch_conflict"
	// CodeWorktreeInvalid 表示 Clone 后的 Git 工作区校验失败。
	CodeWorktreeInvalid ErrorCode = "worktree_invalid"
	// CodeStateUncertain 表示远程执行结果无法确认，需要先核验实际状态。
	CodeStateUncertain ErrorCode = "state_uncertain"
)

// String 返回错误类别的字符串形式。
func (c ErrorCode) String() string {
	return string(c)
}

// Error 是带类别与操作上下文的代码获取错误。
//
// 该错误不携带凭据内容：Detail 只包含经过脱敏的仓库地址、分支名与裁剪后的输出。
type Error struct {
	// Code 是错误类别。
	Code ErrorCode
	// Op 是失败的操作名，例如 "git clone"。
	Op string
	// Detail 是可读的失败原因。
	Detail string
	// Cause 是底层错误，可通过 errors.Is 判断超时或取消。
	Cause error
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	detail := e.Detail
	if detail == "" {
		return fmt.Sprintf("代码获取失败(%s): %s", e.Code, e.Op)
	}
	return fmt.Sprintf("代码获取失败(%s): %s: %s", e.Code, e.Op, detail)
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

// ErrorCodeOf 从错误链中提取代码获取错误类别，未分类时返回 CodeOK。
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

// newError 构造代码获取错误。
func newError(code ErrorCode, op, detail string, cause error) *Error {
	return &Error{Code: code, Op: op, Detail: detail, Cause: cause}
}
