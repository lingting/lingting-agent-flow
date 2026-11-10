package testrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// piCLIVersionTimeout 是 pi-cli 版本探测的默认上限。
const piCLIVersionTimeout = 10 * time.Second

// PiCLIProvider 分配本机真实 Pi CLI 的占用句柄。
//
// R2 只用 --version 探测可执行文件并记录版本与内容指纹，不发起任何真实 Pi 调用：需要真实
// Pi 的用例在获得授权的批次里通过句柄给出的路径自行调用。释放只清理占用记录，自证内容是
// 可执行文件仍然存在且提供者不再持有该句柄。
type PiCLIProvider struct {
	defaultBinary string
	mu            sync.Mutex
	entries       map[string]Handle
}

// NewPiCLIProvider 创建一个 Pi CLI 提供者；defaultBinary 为空时按 PATH 查找 pi。
func NewPiCLIProvider(defaultBinary string) *PiCLIProvider {
	return &PiCLIProvider{
		defaultBinary: strings.TrimSpace(defaultBinary),
		entries:       make(map[string]Handle),
	}
}

// Acquire 解析可执行文件、探测版本并记录内容指纹。
func (p *PiCLIProvider) Acquire(ctx context.Context, decl ResourceDecl) (Handle, error) {
	if err := acquirePrecheck(ctx, decl); err != nil {
		return Handle{}, err
	}
	configured := specValue(decl, "binary")
	if configured == "" {
		configured = p.defaultBinary
	}
	if configured == "" {
		configured = "pi"
	}
	path, err := resolveExecutable(configured)
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	fingerprint, err := fileSHA256(path)
	if err != nil {
		return Handle{}, err
	}
	version, err := probePiVersion(ctx, path, specDuration(decl, "version_timeout_ms", piCLIVersionTimeout))
	if err != nil {
		return Handle{}, fmt.Errorf("分配资源 %q 失败: %w", decl.Name, err)
	}
	handle, err := buildHandle(decl, map[string]string{"path": path, "version": version}, fingerprint)
	if err != nil {
		return Handle{}, err
	}
	p.mu.Lock()
	p.entries[path] = handle
	p.mu.Unlock()
	return handle, nil
}

// Release 清理占用记录；Pi CLI 是可执行文件，不需要额外清理动作。
func (p *PiCLIProvider) Release(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	delete(p.entries, path)
	p.mu.Unlock()
	return nil
}

// Verify 自证提供者不再持有该可执行文件，且文件本身仍然存在。
func (p *PiCLIProvider) Verify(ctx context.Context, handle Handle) error {
	path := handle.PayloadValue("path")
	p.mu.Lock()
	_, tracked := p.entries[path]
	p.mu.Unlock()
	if tracked {
		return fmt.Errorf("Pi CLI %s 仍被提供者持有，释放没有生效", path)
	}
	if err := verifyFilePresent(path); err != nil {
		return err
	}
	return nil
}

// Snapshot 返回仍被占用的 Pi CLI。
func (p *PiCLIProvider) Snapshot(ctx context.Context) ([]LiveResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := make([]LiveResource, 0, len(p.entries))
	for path, handle := range p.entries {
		entries = append(entries, LiveResource{Handle: handle, Detail: "被占用的 Pi CLI " + path})
	}
	return entries, nil
}

// verifyFilePresent 自证路径仍然存在且不是目录。
func verifyFilePresent(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("可执行文件 %s 不可用: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("可执行文件 %s 是目录", path)
	}
	return nil
}

// probePiVersion 执行 pi --version 并返回首行版本号。
func probePiVersion(ctx context.Context, path string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = piCLIVersionTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(runCtx, path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"执行 %s --version 失败: %w（输出：%s）",
			path,
			err,
			strings.TrimSpace(string(output)),
		)
	}
	version := firstLine(strings.TrimSpace(string(output)))
	if version == "" {
		return "", fmt.Errorf("%s --version 没有输出可识别的版本号", path)
	}
	return version, nil
}
