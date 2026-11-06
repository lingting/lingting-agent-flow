package server

import (
	"errors"
	"fmt"
	"io/fs"
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
// 数据库路径先规范化为真实绝对路径再拼接后缀，因此相对路径、含冗余段的路径以及指向同一
// 数据库文件的符号链接别名都会得到同一个锁文件。锁文件路径只是命名约定，不读取数据库，
// 也不创建它。
func ServerLockPath(databasePath string) (string, error) {
	resolved, err := normalizeDatabasePath(databasePath)
	if err != nil {
		return "", err
	}
	return resolved + serverLockSuffix, nil
}

// normalizeDatabasePath 返回数据库文件的规范化绝对路径。
//
// 规范化要解决的是"同一份数据库被不同路径表达式指向"：相对路径、绝对路径与冗余段由
// filepath.Abs / filepath.Clean 收敛；符号链接别名由 filepath.EvalSymlinks 收敛。
//
// 数据库文件通常尚不存在，因此不能直接对整个路径求值：尾部尚未创建的路径段被暂时剥离，
// 解析最近一个已经存在的祖先（它本身可能是指向别处的符号链接），再把尚未创建的路径段
// 原样拼回。悬空符号链接不是"尚未创建的文件"：解析失败时返回错误，由调用方失败关闭。
func normalizeDatabasePath(databasePath string) (string, error) {
	trimmed := strings.TrimSpace(databasePath)
	if trimmed == "" {
		return "", fmt.Errorf("规范化数据库路径失败: 数据库路径不能为空")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("规范化数据库路径失败: %s: %w", trimmed, err)
	}
	return resolveDatabasePath(filepath.Clean(absolute))
}

// resolveDatabasePath 解析路径中最近一个已存在祖先的符号链接，并拼回尚未创建的路径段。
func resolveDatabasePath(clean string) (string, error) {
	// missing 保存从文件本身向上剥离的路径段，按剥离顺序入栈，拼接时反向还原。
	var missing []string
	current := clean
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			// 已存在的部分交给 EvalSymlinks：普通文件与目录本身不带链接，但它们上层的
			// 每一段都可能是指向别处的符号链接。
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				// 悬空符号链接不是"尚未创建的文件"：它的目标不存在，继续启动会打开一
				// 个与现有数据库无关的新文件，因此必须失败关闭。
				if info.Mode()&os.ModeSymlink != 0 && errors.Is(resolveErr, fs.ErrNotExist) {
					return "", fmt.Errorf("规范化数据库路径失败: %s 是悬空符号链接: %w", current, resolveErr)
				}
				return "", fmt.Errorf("规范化数据库路径失败: 解析 %s 失败: %w", current, resolveErr)
			}
			segments := make([]string, 0, len(missing)+1)
			segments = append(segments, resolved)
			for index := len(missing) - 1; index >= 0; index-- {
				segments = append(segments, missing[index])
			}
			return filepath.Join(segments...), nil
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			return "", fmt.Errorf("规范化数据库路径失败: %s: %w", current, statErr)
		}
		if current == filepath.Dir(current) {
			// 已经回退到根目录却仍然不存在：路径本身无法解析。
			return "", fmt.Errorf("规范化数据库路径失败: %s: %w", clean, statErr)
		}
		missing = append(missing, filepath.Base(current))
		current = filepath.Dir(current)
	}
}

// ProcessLock 是服务端进程锁。
//
// 它使用操作系统的独占文件锁：同一台机器上的两个 Server 进程不可能同时持有同一个锁
// 文件，进程异常退出时由操作系统释放，不依赖任何进程内状态。
type ProcessLock struct {
	// databasePath 是规范化后的数据库文件路径。
	//
	// 它与锁文件路径由同一次规范化计算得出：取得锁之后服务端必须用同一条路径打开
	// SQLite，否则锁身份与真正被打开的数据库可能不是同一份文件。
	databasePath string
	path         string
	file         *flock.Flock
}

// NewProcessLock 创建服务端进程锁并准备锁文件所在目录。
//
// 只创建父目录与锁文件本身，不提前打开或初始化数据库。数据库路径的规范化在这里完成：
// 符号链接解析失败（含悬空符号链接）时直接返回错误，调用方不会继续打开数据库。
func NewProcessLock(databasePath string) (*ProcessLock, error) {
	resolved, err := normalizeDatabasePath(databasePath)
	if err != nil {
		return nil, err
	}
	lockPath := resolved + serverLockSuffix
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o750); err != nil {
		return nil, fmt.Errorf("创建服务端锁文件目录失败: %s: %w", filepath.Dir(lockPath), err)
	}
	return &ProcessLock{
		databasePath: resolved,
		path:         lockPath,
		file:         flock.New(lockPath),
	}, nil
}

// Path 返回锁文件路径。
func (l *ProcessLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// DatabasePath 返回规范化后的数据库文件路径。
//
// 服务端必须用它与锁文件路径对应地打开数据库：同一份数据库的不同路径别名会得到同一个
// 锁文件，而打开数据库时若使用原始别名，锁保护的文件与实际写入的文件就不再一致。
func (l *ProcessLock) DatabasePath() string {
	if l == nil {
		return ""
	}
	return l.databasePath
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
