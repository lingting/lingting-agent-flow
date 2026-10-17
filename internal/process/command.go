// Package process 提供外部子进程的启动、管道与生命周期管理。
//
// 子进程的终止与回收集中在本包实现，其他包不得自行管理进程退出。
// 本包只负责直接子进程：它启动的后代进程是否随之一并终止取决于操作系统的
// 进程模型，不能把强制终止等同于通用的进程树清理。
package process

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// 进程管理相关错误，调用方通过 errors.Is 判断错误类别。
var (
	// ErrInvalidCommand 表示启动参数不合法。
	ErrInvalidCommand = errors.New("process: 启动参数无效")
	// ErrShutdownTimeout 表示等待进程退出超时。
	ErrShutdownTimeout = errors.New("process: 等待进程退出超时")
)

// DefaultGraceTimeout 是关闭宽限时间的默认值。
const DefaultGraceTimeout = 5 * time.Second

// Command 描述一次子进程启动。
type Command struct {
	// Path 是可执行文件路径，允许是 PATH 中的名称。
	Path string
	// Args 是传给子进程的参数，不含可执行文件名。
	Args []string
	// Dir 是子进程的工作目录，为空表示继承当前进程。
	Dir string
	// Env 是子进程环境变量，nil 表示继承当前进程环境。
	Env []string
}

// LookPath 检查可执行程序是否可用，返回解析后的完整路径。
func LookPath(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%w: 可执行文件不能为空", ErrInvalidCommand)
	}
	path, err := exec.LookPath(strings.TrimSpace(name))
	if err != nil {
		return "", fmt.Errorf("process: 找不到可执行文件 %q: %w", name, err)
	}
	return path, nil
}

// Validate 校验启动参数。
func (c Command) Validate() error {
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("%w: 可执行文件不能为空", ErrInvalidCommand)
	}
	return nil
}

// Exit 描述子进程的退出结果。
type Exit struct {
	// Code 是退出码；进程被信号或强制终止时为 -1。
	Code int
	// Err 是回收进程时得到的错误，正常退出时为 nil。
	Err error
}
