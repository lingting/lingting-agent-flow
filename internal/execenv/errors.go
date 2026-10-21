package execenv

import (
	"errors"
	"fmt"
)

// 执行环境契约的统一错误。
//
// 调用方通过 errors.Is 判断类别，不解析错误文本。区分这些类别是必要的：
// "环境尚未准备好"与"命令执行失败"需要完全不同的处理方式。
var (
	// ErrInvalidValue 表示输入参数不合法。
	ErrInvalidValue = errors.New("execenv: 输入参数非法")
	// ErrUnsupportedKind 表示执行环境类型尚未实现。
	ErrUnsupportedKind = errors.New("execenv: 执行环境尚未实现")
	// ErrNotReady 表示执行环境尚未准备完成，不能执行命令。
	ErrNotReady = errors.New("execenv: 执行环境尚未就绪")
	// ErrNotRunning 表示环境已就绪但容器当前不在运行状态。
	ErrNotRunning = errors.New("execenv: 执行环境未运行")
	// ErrReleased 表示执行环境已被释放，不再接受操作。
	ErrReleased = errors.New("execenv: 执行环境已释放")
	// ErrStateUncertain 表示远程状态无法确认，必须先核验再决定下一步。
	ErrStateUncertain = errors.New("execenv: 远程状态无法确认")
)

// Error 是带执行环境上下文的错误。
//
// Detail 只包含可公开的诊断信息：容器标识、阶段名称与经过裁剪的远程输出片段，
// 不包含凭据、私钥内容或完整环境变量。
type Error struct {
	// Kind 是环境类型。
	Kind Kind
	// Stage 是失败阶段，例如 prepare、create、start、verify、exec。
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
		detail = "执行环境操作失败"
	}
	if e.Stage == "" {
		return fmt.Sprintf("执行环境(%s): %s", e.Kind, detail)
	}
	return fmt.Sprintf("执行环境(%s) %s 阶段失败: %s", e.Kind, e.Stage, detail)
}

// Unwrap 暴露底层错误。
func (e *Error) Unwrap() error {
	return e.Cause
}

// wrap 构造带阶段信息的执行环境错误。
func wrap(kind Kind, stage, detail string, cause error) error {
	return &Error{Kind: kind, Stage: stage, Detail: detail, Cause: cause}
}
