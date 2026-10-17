package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// ErrRemotePathExists 表示目标已存在且调用方要求不覆盖。
var ErrRemotePathExists = fmt.Errorf("sshclient: 远程目标已存在")

// ErrLocalPathExists 表示本机目标已存在且调用方要求不覆盖。
var ErrLocalPathExists = fmt.Errorf("sshclient: 本机目标已存在")

// FileInfo 是远程文件或目录的属性快照。
type FileInfo struct {
	// Path 是规范化后的远程绝对路径。
	Path string
	// Name 是路径的最后一段。
	Name string
	// Size 是文件字节数，目录为 0。
	Size int64
	// Mode 是 POSIX 权限位。
	Mode os.FileMode
	// ModTime 是最后修改时间。
	ModTime time.Time
	// IsDir 表示是否为目录。
	IsDir bool
	// IsRegular 表示是否为普通文件。
	IsRegular bool
}

// FileTransfer 是远程文件操作层。
//
// 它对应一条 SFTP 子系统通道，由调用方负责关闭；关闭只释放该通道，
// 不影响底层 SSH 连接上的其他操作。
type FileTransfer interface {
	// Upload 上传本机文件到远程路径。
	Upload(ctx context.Context, localPath, remotePath string, options UploadOptions) error
	// Download 下载远程文件到本机路径。
	Download(ctx context.Context, remotePath, localPath string, options DownloadOptions) error
	// MkdirAll 递归创建远程目录。
	MkdirAll(ctx context.Context, remotePath string) error
	// Stat 返回远程文件或目录的属性。
	Stat(ctx context.Context, remotePath string) (FileInfo, error)
	// ReadDir 列出远程目录内容，按名称升序返回。
	ReadDir(ctx context.Context, remotePath string) ([]FileInfo, error)
	// Rename 重命名远程文件或目录。
	Rename(ctx context.Context, oldPath, newPath string) error
	// Remove 删除远程文件，目标不存在时返回错误。
	Remove(ctx context.Context, remotePath string) error
	// Close 关闭 SFTP 通道。
	Close() error
}

// UploadOptions 约束一次上传。
type UploadOptions struct {
	// Overwrite 允许覆盖已存在的目标文件；为 false 时目标存在即返回 ErrRemotePathExists。
	Overwrite bool
	// Mode 是目标文件权限位，零值使用 0o644。
	Mode os.FileMode
	// CreateParent 表示自动递归创建目标文件的父目录。
	CreateParent bool
}

// DownloadOptions 约束一次下载。
type DownloadOptions struct {
	// Overwrite 允许覆盖已存在的本机文件；为 false 时目标存在即返回 ErrLocalPathExists。
	Overwrite bool
	// Mode 是本机文件权限位，零值使用 0o600。
	//
	// 默认保守：下载内容可能包含敏感信息，不默认给其他用户读取权限。
	Mode os.FileMode
	// CreateParent 表示自动递归创建本机父目录。
	CreateParent bool
}

// transfer 是 FileTransfer 的实现。
type transfer struct {
	client *sftp.Client

	mu     sync.Mutex
	closed bool
}

// SFTP 打开一个新的 SFTP 子系统通道。
func (c *client) SFTP() (FileTransfer, error) {
	if err := c.ensureOpen(); err != nil {
		return nil, err
	}
	sftpClient, err := sftp.NewClient(c.sshClient)
	if err != nil {
		return nil, fmt.Errorf("sshclient: 打开 SFTP 通道失败: %w", err)
	}
	return &transfer{client: sftpClient}, nil
}

// Close 关闭 SFTP 通道，重复调用返回 nil。
func (t *transfer) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.client == nil {
		return nil
	}
	if err := t.client.Close(); err != nil {
		return fmt.Errorf("sshclient: 关闭 SFTP 通道失败: %w", err)
	}
	return nil
}

// ensureOpen 确认 SFTP 通道仍然可用。
func (t *transfer) ensureOpen() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("sshclient: SFTP 通道已关闭")
	}
	if t.client == nil {
		return fmt.Errorf("sshclient: SFTP 通道未初始化")
	}
	return nil
}

// MkdirAll 递归创建远程目录。
func (t *transfer) MkdirAll(ctx context.Context, remotePath string) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	target, err := NormalizeRemotePath(remotePath)
	if err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := t.client.MkdirAll(target); err != nil {
		return fmt.Errorf("sshclient: 创建远程目录失败: %s: %w", target, err)
	}
	return nil
}

// Stat 返回远程文件或目录的属性。
func (t *transfer) Stat(ctx context.Context, remotePath string) (FileInfo, error) {
	if err := t.ensureOpen(); err != nil {
		return FileInfo{}, err
	}
	target, err := NormalizeRemotePath(remotePath)
	if err != nil {
		return FileInfo{}, err
	}
	if err := ctxErr(ctx); err != nil {
		return FileInfo{}, err
	}
	info, err := t.client.Stat(target)
	if err != nil {
		return FileInfo{}, fmt.Errorf("sshclient: 读取远程文件属性失败: %s: %w", target, err)
	}
	return toFileInfo(target, info), nil
}

// ReadDir 列出远程目录内容。
func (t *transfer) ReadDir(ctx context.Context, remotePath string) ([]FileInfo, error) {
	if err := t.ensureOpen(); err != nil {
		return nil, err
	}
	target, err := NormalizeRemotePath(remotePath)
	if err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	entries, err := t.client.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("sshclient: 列出远程目录失败: %s: %w", target, err)
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		infos = append(infos, toFileInfo(path.Join(target, entry.Name()), entry))
	}
	return infos, nil
}

// Rename 重命名远程文件或目录。
func (t *transfer) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	source, err := NormalizeRemotePath(oldPath)
	if err != nil {
		return err
	}
	destination, err := NormalizeRemotePath(newPath)
	if err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := t.client.Rename(source, destination); err != nil {
		return fmt.Errorf(
			"sshclient: 重命名远程路径失败: %s -> %s: %w",
			source,
			destination,
			err,
		)
	}
	return nil
}

// Remove 删除远程文件。
//
// 目录不会被删除：删除目录需要显式递归，属于后续更高层的操作。
func (t *transfer) Remove(ctx context.Context, remotePath string) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	target, err := NormalizeRemotePath(remotePath)
	if err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	info, err := t.client.Stat(target)
	if err != nil {
		return fmt.Errorf("sshclient: 删除远程文件失败: %s: %w", target, err)
	}
	if info.IsDir() {
		return fmt.Errorf("sshclient: 删除远程文件失败: %s 是目录", target)
	}
	if err := t.client.Remove(target); err != nil {
		return fmt.Errorf("sshclient: 删除远程文件失败: %s: %w", target, err)
	}
	return nil
}

// Upload 上传本机文件到远程路径。
//
// 上传先写入同目录下的临时文件，校验写入结果后原子重命名到目标路径，
// 因此失败或中断时不会留下内容不完整的最终文件。
func (t *transfer) Upload(
	ctx context.Context,
	localPath, remotePath string,
	options UploadOptions,
) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	source, err := validateLocalPath(localPath)
	if err != nil {
		return err
	}
	target, err := NormalizeRemotePath(remotePath)
	if err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("sshclient: 读取本机文件失败: %s: %w", source, err)
	}
	if info.IsDir() {
		return fmt.Errorf("sshclient: 上传失败: 本机路径 %s 是目录", source)
	}
	if !options.Overwrite {
		exists, err := t.remoteExists(target)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: %s", ErrRemotePathExists, target)
		}
	}
	if options.CreateParent {
		if err := t.client.MkdirAll(path.Dir(target)); err != nil {
			return fmt.Errorf("sshclient: 创建远程目录失败: %s: %w", path.Dir(target), err)
		}
	}
	mode := options.Mode
	if mode == 0 {
		mode = 0o644
	}
	tempPath, err := t.tempRemotePath(target)
	if err != nil {
		return err
	}
	if err := t.copyLocalToRemote(ctx, source, tempPath, mode); err != nil {
		t.removeQuietly(tempPath)
		return err
	}
	if err := t.commitTemp(tempPath, target, options.Overwrite); err != nil {
		t.removeQuietly(tempPath)
		return err
	}
	return nil
}

// copyLocalToRemote 把本机文件写入远程临时文件。
func (t *transfer) copyLocalToRemote(
	ctx context.Context,
	source, remotePath string,
	mode os.FileMode,
) error {
	local, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("sshclient: 打开本机文件失败: %s: %w", source, err)
	}
	defer func() {
		_ = local.Close()
	}()
	remote, err := t.client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("sshclient: 创建远程临时文件失败: %s: %w", remotePath, err)
	}
	writeErr := copyWithContext(ctx, remote, local)
	// 关键写入错误必须先于关闭错误返回：关闭失败时也可能丢失数据。
	closeErr := remote.Close()
	if writeErr != nil {
		return fmt.Errorf("sshclient: 上传文件失败: %s -> %s: %w", source, remotePath, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("sshclient: 关闭远程临时文件失败: %s: %w", remotePath, closeErr)
	}
	if err := t.client.Chmod(remotePath, mode); err != nil {
		return fmt.Errorf("sshclient: 设置远程文件权限失败: %s: %w", remotePath, err)
	}
	return nil
}

// commitTemp 把临时文件原子重命名到最终目标。
func (t *transfer) commitTemp(tempPath, target string, overwrite bool) error {
	if overwrite {
		// 目标已存在时直接重命名会失败，先删除旧文件。
		exists, err := t.remoteExists(target)
		if err != nil {
			return err
		}
		if exists {
			if err := t.client.Remove(target); err != nil {
				return fmt.Errorf("sshclient: 替换远程文件失败: %s: %w", target, err)
			}
		}
	}
	if err := t.client.Rename(tempPath, target); err != nil {
		return fmt.Errorf("sshclient: 提交远程文件失败: %s -> %s: %w", tempPath, target, err)
	}
	return nil
}

// Download 下载远程文件到本机路径。
//
// 下载先写入同目录下的本机临时文件，成功后再原子重命名到目标路径。
func (t *transfer) Download(
	ctx context.Context,
	remotePath, localPath string,
	options DownloadOptions,
) error {
	if err := t.ensureOpen(); err != nil {
		return err
	}
	source, err := NormalizeRemotePath(remotePath)
	if err != nil {
		return err
	}
	target, err := validateLocalPath(localPath)
	if err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	info, err := t.client.Stat(source)
	if err != nil {
		return fmt.Errorf("sshclient: 读取远程文件属性失败: %s: %w", source, err)
	}
	if info.IsDir() {
		return fmt.Errorf("sshclient: 下载失败: 远程路径 %s 是目录", source)
	}
	if !options.Overwrite {
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("%w: %s", ErrLocalPathExists, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("sshclient: 检查本机目标失败: %s: %w", target, err)
		}
	}
	if options.CreateParent {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("sshclient: 创建本机目录失败: %s: %w", filepath.Dir(target), err)
		}
	}
	mode := options.Mode
	if mode == 0 {
		mode = 0o600
	}
	tempPath, err := localTempPath(target)
	if err != nil {
		return err
	}
	if err := t.copyRemoteToLocal(ctx, source, tempPath); err != nil {
		removeQuietly(tempPath)
		return err
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		removeQuietly(tempPath)
		return fmt.Errorf("sshclient: 设置本机文件权限失败: %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		removeQuietly(tempPath)
		return fmt.Errorf("sshclient: 提交本机文件失败: %s -> %s: %w", tempPath, target, err)
	}
	return nil
}

// copyRemoteToLocal 把远程文件写入本机临时文件。
func (t *transfer) copyRemoteToLocal(ctx context.Context, source, localPath string) error {
	remote, err := t.client.Open(source)
	if err != nil {
		return fmt.Errorf("sshclient: 打开远程文件失败: %s: %w", source, err)
	}
	defer func() {
		_ = remote.Close()
	}()
	local, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("sshclient: 创建本机临时文件失败: %s: %w", localPath, err)
	}
	writeErr := copyWithContext(ctx, local, remote)
	syncErr := local.Sync()
	closeErr := local.Close()
	if writeErr != nil {
		return fmt.Errorf("sshclient: 下载文件失败: %s -> %s: %w", source, localPath, writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sshclient: 同步本机临时文件失败: %s: %w", localPath, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("sshclient: 关闭本机临时文件失败: %s: %w", localPath, closeErr)
	}
	return nil
}

// remoteExists 判断远程路径是否存在。
func (t *transfer) remoteExists(target string) (bool, error) {
	if _, err := t.client.Stat(target); err != nil {
		if isSFTPNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("sshclient: 检查远程路径失败: %s: %w", target, err)
	}
	return true, nil
}

// removeQuietly 尽力删除临时文件，失败时不覆盖原始错误。
func (t *transfer) removeQuietly(target string) {
	_ = t.client.Remove(target)
}

// tempRemotePath 生成目标文件同目录下的临时文件路径。
//
// 同目录保证后续重命名不会跨越文件系统；名称包含进程号与纳秒时间戳，
// 避免并发上传互相覆盖。
func (t *transfer) tempRemotePath(target string) (string, error) {
	directory := path.Dir(target)
	base := path.Base(target)
	suffix := fmt.Sprintf(".%s.%d.%d.tmp", base, os.Getpid(), time.Now().UnixNano())
	return path.Join(directory, suffix), nil
}

// localTempPath 生成目标文件同目录下的本机临时文件路径。
func localTempPath(target string) (string, error) {
	directory := filepath.Dir(target)
	file, err := os.CreateTemp(directory, filepath.Base(target)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("sshclient: 创建本机临时文件失败: %s: %w", directory, err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		removeQuietly(name)
		return "", fmt.Errorf("sshclient: 关闭本机临时文件失败: %s: %w", name, err)
	}
	return name, nil
}

// removeQuietly 尽力删除本机临时文件。
func removeQuietly(target string) {
	_ = os.Remove(target)
}

// copyWithContext 在复制过程中响应取消，返回复制字节数。
func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	if ctx == nil {
		_, err := io.Copy(destination, source)
		return err
	}
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			if _, writeErr := destination.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// isSFTPNotExist 判断错误是否表示远程路径不存在。
func isSFTPNotExist(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var statusErr *sftp.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.FxCode() == sftp.ErrSSHFxNoSuchFile
	}
	return false
}

// toFileInfo 把 SFTP 文件属性转换为 FileInfo。
func toFileInfo(target string, info os.FileInfo) FileInfo {
	if info == nil {
		return FileInfo{Path: target, Name: path.Base(target)}
	}
	return FileInfo{
		Path:      target,
		Name:      info.Name(),
		Size:      info.Size(),
		Mode:      info.Mode().Perm(),
		ModTime:   info.ModTime(),
		IsDir:     info.IsDir(),
		IsRegular: info.Mode().IsRegular(),
	}
}

// 编译期确认实现满足接口约束。
var _ FileTransfer = (*transfer)(nil)
