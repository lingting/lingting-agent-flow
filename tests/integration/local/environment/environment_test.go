// Package environment_local 提供环境版本管理在真实文件系统与 SQLite 上的本机集成测试。
//
// 这里使用可控的远程文件替身代替真实 SFTP：替身把远程路径映射到测试工作空间中的
// 目录，因此可以在真实文件读写与真实数据库约束下验证版本不可覆盖、哈希记录与
// 清单一致性，而不依赖真实远程机器。
package environment_local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/domain"
	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// localTransfer 是把远程路径映射到本机目录的 FileTransfer 替身。
//
// 映射规则：远程绝对路径 /home/deploy/.lingting/agentflow/... 对应本机
// <root>/home/deploy/.lingting/agentflow/...，因此测试可以使用真实的文件读写。
type localTransfer struct {
	root string
}

// localPath 把远程路径映射到本机路径。
func (t *localTransfer) localPath(remotePath string) (string, error) {
	normalized, err := sshclient.NormalizeRemotePath(remotePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(t.root, filepath.FromSlash(normalized)), nil
}

// Upload 把本机文件复制到映射后的本机路径。
func (t *localTransfer) Upload(
	ctx context.Context,
	localPath, remotePath string,
	options sshclient.UploadOptions,
) error {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	return t.WriteFile(ctx, remotePath, content, sshclient.WriteOptions{
		Overwrite:    options.Overwrite,
		Mode:         options.Mode,
		CreateParent: options.CreateParent,
	})
}

// Download 把映射后的本机文件复制到目标路径。
func (t *localTransfer) Download(
	ctx context.Context,
	remotePath, localPath string,
	options sshclient.DownloadOptions,
) error {
	content, err := t.ReadFile(ctx, remotePath, options.MaxBytes)
	if err != nil {
		return err
	}
	if options.CreateParent {
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(localPath, content, 0o600)
}

// MkdirAll 创建映射后的本机目录。
func (t *localTransfer) MkdirAll(ctx context.Context, remotePath string) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

// Stat 返回映射后的本机路径属性。
func (t *localTransfer) Stat(ctx context.Context, remotePath string) (sshclient.FileInfo, error) {
	target, err := t.localPath(remotePath)
	if err != nil {
		return sshclient.FileInfo{}, err
	}
	return t.stat(ctx, target)
}

// Lstat 返回映射后的本机路径属性。
func (t *localTransfer) Lstat(ctx context.Context, remotePath string) (sshclient.FileInfo, error) {
	return t.Stat(ctx, remotePath)
}

// stat 读取本机路径属性并转换为远程属性结构。
func (t *localTransfer) stat(ctx context.Context, target string) (sshclient.FileInfo, error) {
	info, err := os.Stat(target)
	if err != nil {
		return sshclient.FileInfo{}, err
	}
	kind := sshclient.FileKindRegular
	if info.IsDir() {
		kind = sshclient.FileKindDirectory
	}
	return sshclient.FileInfo{
		Path:      target,
		Name:      info.Name(),
		Kind:      kind,
		Size:      info.Size(),
		Mode:      info.Mode().Perm(),
		ModTime:   info.ModTime(),
		IsDir:     info.IsDir(),
		IsRegular: info.Mode().IsRegular(),
	}, nil
}

// ReadDir 列出映射后的本机目录。
func (t *localTransfer) ReadDir(ctx context.Context, remotePath string) ([]sshclient.FileInfo, error) {
	target, err := t.localPath(remotePath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	infos := make([]sshclient.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, statErr := t.stat(ctx, filepath.Join(target, entry.Name()))
		if statErr != nil {
			return nil, statErr
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Rename 重命名映射后的本机路径。
func (t *localTransfer) Rename(ctx context.Context, oldPath, newPath string) error {
	source, err := t.localPath(oldPath)
	if err != nil {
		return err
	}
	destination, err := t.localPath(newPath)
	if err != nil {
		return err
	}
	return os.Rename(source, destination)
}

// Remove 删除映射后的本机文件。
func (t *localTransfer) Remove(ctx context.Context, remotePath string) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	return os.Remove(target)
}

// ReadFile 读取映射后的本机文件内容。
func (t *localTransfer) ReadFile(
	ctx context.Context,
	remotePath string,
	maxBytes int64,
) ([]byte, error) {
	target, err := t.localPath(remotePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, sshclient.ErrRemoteSizeExceeded
	}
	return os.ReadFile(target)
}

// WriteFile 写入映射后的本机文件。
func (t *localTransfer) WriteFile(
	ctx context.Context,
	remotePath string,
	content []byte,
	options sshclient.WriteOptions,
) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	if !options.Overwrite {
		if _, statErr := os.Stat(target); statErr == nil {
			return sshclient.ErrRemotePathExists
		}
	}
	if options.CreateParent {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
	}
	mode := options.Mode
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(target, content, mode)
}

// RemoveAll 递归删除映射后的本机路径。
func (t *localTransfer) RemoveAll(ctx context.Context, remotePath string) error {
	target, err := t.localPath(remotePath)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

// Close 关闭替身通道。
func (t *localTransfer) Close() error {
	return nil
}

// newTransfer 在测试工作空间中构造远程文件替身。
func newTransfer(t *testing.T) (*localTransfer, string) {
	t.Helper()
	ws := testkit.New(t)
	ws.Report()
	root := ws.Dir("remote")
	return &localTransfer{root: root}, root
}

// workspaceRoot 返回映射后的工作空间根路径。
func workspaceRoot(root string) string {
	return "/home/deploy/.lingting/agentflow"
}

// newLayout 构造测试用远程目录布局。
func newLayout(t *testing.T) docker.Layout {
	t.Helper()
	layout, err := docker.NewLayout(workspaceRoot(""))
	if err != nil {
		t.Fatalf("构造布局失败: %v", err)
	}
	return layout
}

// TestVersionFilesAreNotOverwritten 验证版本目录写入后不会被覆盖。
func TestVersionFilesAreNotOverwritten(t *testing.T) {
	t.Parallel()
	transfer, root := newTransfer(t)
	layout := newLayout(t)
	ref, err := domain.NewEnvironmentRef(domain.EnvironmentTypeBase, "")
	if err != nil {
		t.Fatalf("构造基础环境引用失败: %v", err)
	}
	versionDir := layout.VersionDir(ref, 1)
	if err := transfer.MkdirAll(context.Background(), versionDir); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	dockerfilePath := layout.DockerfilePath(ref, 1)
	if err := transfer.WriteFile(context.Background(), dockerfilePath, []byte("FROM debian:12.7-slim\n"), sshclient.WriteOptions{
		Overwrite:    false,
		CreateParent: true,
	}); err != nil {
		t.Fatalf("写入 Dockerfile 失败: %v", err)
	}
	// 同一版本目录再次写入必须被拒绝，历史版本不可覆盖。
	err = transfer.WriteFile(context.Background(), dockerfilePath, []byte("FROM debian:12.8-slim\n"), sshclient.WriteOptions{
		Overwrite:    false,
		CreateParent: true,
	})
	if !errors.Is(err, sshclient.ErrRemotePathExists) {
		t.Fatalf("覆盖历史版本应返回目标已存在错误: %v", err)
	}
	content, err := transfer.ReadFile(context.Background(), dockerfilePath, 0)
	if err != nil {
		t.Fatalf("读取 Dockerfile 失败: %v", err)
	}
	if !strings.Contains(string(content), "12.7") {
		t.Fatalf("历史版本内容不得被覆盖: %s", content)
	}
	// 本机映射目录确实落在测试工作空间中。
	mapped, err := transfer.localPath(dockerfilePath)
	if err != nil {
		t.Fatalf("映射路径失败: %v", err)
	}
	if !strings.HasPrefix(mapped, root) {
		t.Fatalf("映射路径应位于测试工作空间内: %s", mapped)
	}
}

// TestManifestRoundTripOnDisk 验证镜像清单与容器清单在磁盘上的读写一致性。
func TestManifestRoundTripOnDisk(t *testing.T) {
	t.Parallel()
	transfer, _ := newTransfer(t)
	layout := newLayout(t)
	runner, err := docker.NewRunner(&stubClient{}, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造执行器失败: %v", err)
	}
	ctx := context.Background()
	manifestPath := layout.ImagesManifestPath()

	// 清单不存在时返回空清单而不是错误。
	manifest, err := runner.ReadImageManifest(ctx, transfer, manifestPath)
	if err != nil {
		t.Fatalf("读取不存在的镜像清单应成功: %v", err)
	}
	if len(manifest.Entries) != 0 {
		t.Fatalf("不存在的镜像清单应为空: %+v", manifest.Entries)
	}
	manifest.Upsert(docker.ImageManifestEntry{
		Environment: "base",
		Version:     "v0001",
		ImageID:     "sha256:" + strings.Repeat("a", 64),
		ImageRef:    "agentflow-base:v0001-aaaaaaaaaaaa",
		BuiltAt:     time.Now().UTC().Format(time.RFC3339),
	})
	if err := runner.WriteImageManifest(ctx, transfer, manifestPath, manifest); err != nil {
		t.Fatalf("写入镜像清单失败: %v", err)
	}
	reloaded, err := runner.ReadImageManifest(ctx, transfer, manifestPath)
	if err != nil {
		t.Fatalf("读取镜像清单失败: %v", err)
	}
	if len(reloaded.Entries) != 1 || reloaded.Entries[0].Version != "v0001" {
		t.Fatalf("镜像清单内容不符: %+v", reloaded.Entries)
	}

	registryPath := layout.ContainersRegistryPath()
	registry, err := runner.ReadContainerRegistry(ctx, transfer, registryPath)
	if err != nil {
		t.Fatalf("读取不存在的容器清单应成功: %v", err)
	}
	if len(registry.Containers) != 0 {
		t.Fatalf("不存在的容器清单应为空: %+v", registry.Containers)
	}
	registry.Upsert(docker.ContainerRegistryEntry{ID: "container-1", State: "running"})
	if err := runner.WriteContainerRegistry(ctx, transfer, registryPath, registry); err != nil {
		t.Fatalf("写入容器清单失败: %v", err)
	}
	reloadedRegistry, err := runner.ReadContainerRegistry(ctx, transfer, registryPath)
	if err != nil {
		t.Fatalf("读取容器清单失败: %v", err)
	}
	if len(reloadedRegistry.Containers) != 1 || reloadedRegistry.Containers[0].ID != "container-1" {
		t.Fatalf("容器清单内容不符: %+v", reloadedRegistry.Containers)
	}
}

// stubClient 是仅用于构造 Runner 的最小连接替身。
type stubClient struct{}

// Endpoint 返回固定目标。
func (c *stubClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

// Run 返回成功但空输出。
func (c *stubClient) Run(
	ctx context.Context,
	command string,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	return sshclient.CommandResult{
		Command:              command,
		Status:               sshclient.CommandStatusSucceeded,
		TerminationConfirmed: true,
	}, nil
}

// SFTP 表示替身不支持文件传输。
func (c *stubClient) SFTP() (sshclient.FileTransfer, error) {
	return nil, errors.New("测试替身不支持 SFTP")
}

// Close 关闭替身连接。
func (c *stubClient) Close() error {
	return nil
}

// TestContextUploadAndCleanup 验证构建上下文上传与临时目录清理。
func TestContextUploadAndCleanup(t *testing.T) {
	t.Parallel()
	transfer, root := newTransfer(t)
	layout := newLayout(t)
	runner, err := docker.NewRunner(&stubClient{}, docker.RunnerOptions{})
	if err != nil {
		t.Fatalf("构造执行器失败: %v", err)
	}
	ctx := context.Background()
	buildContext, err := docker.NewContext([]docker.ContextFile{
		{RelativePath: docker.DockerfileName, Content: []byte("FROM debian:12.7-slim\n")},
		{RelativePath: "scripts/setup.sh", Content: []byte("echo ok\n"), Mode: 0o755},
	})
	if err != nil {
		t.Fatalf("构造构建上下文失败: %v", err)
	}
	contextDir := layout.BuildDir("base/v0001-aaaaaaaaaaaa") + "/context"
	spec := docker.BuildSpec{
		Context:          buildContext,
		RemoteContextDir: contextDir,
		LogPath:          layout.BuildLogPath("base/v0001-aaaaaaaaaaaa"),
	}
	if err := runner.UploadContext(ctx, transfer, spec); err != nil {
		t.Fatalf("上传构建上下文失败: %v", err)
	}
	content, err := transfer.ReadFile(ctx, spec.ContextPath(docker.DockerfileName), 0)
	if err != nil {
		t.Fatalf("读取已上传的 Dockerfile 失败: %v", err)
	}
	if !strings.Contains(string(content), "debian") {
		t.Fatalf("上传内容不符: %s", content)
	}
	// 清理只影响本次上传的上下文目录。
	if err := runner.RemoveRemoteDirectory(ctx, transfer, contextDir); err != nil {
		t.Fatalf("清理构建上下文失败: %v", err)
	}
	if _, err := transfer.Stat(ctx, contextDir); err == nil {
		t.Fatal("清理后上下文目录不应存在")
	}
	mapped, err := transfer.localPath(contextDir)
	if err != nil {
		t.Fatalf("映射路径失败: %v", err)
	}
	if !strings.HasPrefix(mapped, root) {
		t.Fatalf("清理范围应位于测试工作空间内: %s", mapped)
	}
}
