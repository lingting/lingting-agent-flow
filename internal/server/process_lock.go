package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// ErrServerAlreadyRunning 表示同一份服务端数据目录已经有实例在运行。
//
// 它与数据库初始化失败是两类事实：前者说明另一个进程正在使用这份数据，调用方应当先
// 停止已有服务端，而不是排查数据库。
var ErrServerAlreadyRunning = errors.New("server: 已有 AgentFlow 服务端实例正在运行")

// serverLockSuffix 是服务端进程锁文件的固定后缀。
//
// 锁文件与数据库文件同目录、同名前缀：它归属"这一份服务端数据目录"，与端口无关。
const serverLockSuffix = ".server.lock"

// ServerLockPath 返回数据库文件对应的服务端进程锁文件路径。
//
// 路径先转换为绝对规范路径再拼接后缀，因此相对路径与引用同一数据库文件的不同配置
// 会得到同一个锁文件。锁文件路径只是命名约定，不读取数据库，也不创建它。
func ServerLockPath(databasePath string) (string, error) {
	trimmed := strings.TrimSpace(databasePath)
	if trimmed == "" {
		return "", fmt.Errorf("确定服务端锁文件失败: 数据库路径不能为空")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("确定服务端锁文件失败: %w", err)
	}
	return filepath.Clean(absolute) + serverLockSuffix, nil
}

// ProcessLock 是服务端进程锁。
//
// 它使用操作系统的独占文件锁：同一台机器上的两个 Server 进程不可能同时持有同一个锁
// 文件，进程异常退出时由操作系统释放，不依赖任何进程内状态。
type ProcessLock struct {
	path string
	file *flock.Flock
}

// NewProcessLock 创建服务端进程锁并准备锁文件所在目录。
//
// 只创建父目录与锁文件本身，不提前打开或初始化数据库。
func NewProcessLock(databasePath string) (*ProcessLock, error) {
	lockPath, err := ServerLockPath(databasePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o750); err != nil {
		return nil, fmt.Errorf("创建服务端锁文件目录失败: %s: %w", filepath.Dir(lockPath), err)
	}
	return &ProcessLock{path: lockPath, file: flock.New(lockPath)}, nil
}

// Path 返回锁文件路径。
func (l *ProcessLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// TryAcquire 非阻塞地取得独占文件锁。
//
// 锁被占用时立即返回 ErrServerAlreadyRunning：启动流程必须由调用方放弃，不得等待另
// 一个服务端退出，也不得继续打开数据库或启动后台服务。
func (l *ProcessLock) TryAcquire() error {
	if l == nil || l.file == nil {
		return fmt.Errorf("获取服务端进程锁失败: 锁未初始化")
	}
	locked, err := l.file.TryLock()
	if err != nil {
		return fmt.Errorf("获取服务端进程锁失败: %s: %w", l.path, err)
	}
	if !locked {
		return fmt.Errorf(
			"%w: 锁文件 %s；请先停止已有服务端，再重新启动",
			ErrServerAlreadyRunning,
			l.path,
		)
	}
	return nil
}

// Release 释放文件锁并关闭句柄。
//
// 它不删除锁文件：删除后不同进程可能锁住不同 inode，使互斥失效。锁文件保留在数据目录
// 中，内容始终为空。
func (l *ProcessLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := l.file.Unlock(); err != nil {
		// 释放失败时保留句柄：调用方仍然可以通过再次释放重试，而不是永久丢失锁。
		return fmt.Errorf("释放服务端进程锁失败: %s: %w", l.path, err)
	}
	l.file = nil
	return nil
}
