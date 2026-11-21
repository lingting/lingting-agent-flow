//go:build windows

package testrunner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// spawnableExecutable 返回本进程可执行文件的、真正能被启动的路径。
//
// Windows 的启动路径解析在文件名没有扩展名时会补 `.exe`（os/exec 的 lookExtensions 与
// CreateProcess 都是如此），所以 `bin/agentflow-test` 这种无扩展名的产物无法直接启动：
// 直接按原路径启动只会找不到文件，或者按 PATHEXT 命中同目录下过期的 .exe。
//
// 这里在本次运行的产物目录里放一个同名 `.exe` 别名（优先硬链接，跨卷时退化为复制），只供
// worker 与子进程启动使用。别名按运行隔离，下一次运行不会复用，因此不存在"加载过期注册表"
// 的风险；删除是尽力而为——Windows 不允许删除仍在运行的镜像的链接，残留只落在本次运行的
// 产物目录里（该目录本身就是可丢弃的运行产物）。
func spawnableExecutable(executable, runDir string) (string, func(), error) {
	noop := func() {}
	if filepath.Ext(executable) != "" {
		return executable, noop, nil
	}
	aliasDir := filepath.Join(runDir, "worker-bin")
	if err := os.MkdirAll(aliasDir, 0o750); err != nil {
		return "", noop, fmt.Errorf("创建 worker 可执行文件目录失败: %w", err)
	}
	aliasPath := filepath.Join(aliasDir, filepath.Base(executable)+".exe")
	if err := os.RemoveAll(aliasPath); err != nil {
		return "", noop, fmt.Errorf("清理旧的 worker 可执行文件失败: %w", err)
	}
	if err := os.Link(executable, aliasPath); err != nil {
		// 跨卷或文件系统不支持硬链接：复制一份，保证启动路径带扩展名。
		if copyErr := copyExecutable(executable, aliasPath); copyErr != nil {
			return "", noop, fmt.Errorf("为 worker 准备可执行文件失败: %w", copyErr)
		}
	}
	return aliasPath, func() {
		// 尽力而为：worker 已回收，但父进程自身仍在运行，它的硬链接删不掉。
		_ = os.RemoveAll(aliasDir)
	}, nil
}

// copyExecutable 复制一份可执行文件，用于硬链接不可用的场景。
func copyExecutable(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o750)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
