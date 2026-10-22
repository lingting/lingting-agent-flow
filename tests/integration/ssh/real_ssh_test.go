package ssh_integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
)

// remoteOpTimeout 是单次远程操作的超时上限。
const remoteOpTimeout = 60 * time.Second

// randomSuffix 返回一个随机的十六进制后缀，用于隔离远程测试目录。
func randomSuffix(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buffer)
}

// TestRealSSHIdentity 验证真实远程机器的身份认证与只读命令执行。
func TestRealSSHIdentity(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	ws := newWorkspace(t)
	connection := dial(t, ws, target)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	result, err := connection.Client.Run(ctx, "id -un", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("执行远程命令失败: %v", err)
	}
	if !result.Succeeded() {
		t.Fatalf("远程命令应以 0 退出，实际: %d: %s", result.ExitCode, result.Stderr)
	}
	actual := strings.TrimSpace(result.Stdout)
	if actual != target.user {
		t.Fatalf("远程用户名不符: 期望 %s，实际 %s", target.user, actual)
	}
	t.Logf("REMOTE_USER=%s", actual)
}

// TestRealSSHRemoteWorkspace 验证远程临时目录的创建、读写与文件传输。
//
// 所有远程操作都在随机的远程临时目录中完成，不写入远程真实项目目录。
func TestRealSSHRemoteWorkspace(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	ws := newWorkspace(t)
	connection := dial(t, ws, target)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	home, err := sshclient.ResolveHome(ctx, connection.Client, remoteOpTimeout)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	remoteRoot := path.Join(home, "agentflow-tests", randomSuffix(t))
	remoteDir, err := sshclient.EnsureWorkspace(ctx, connection.Client, remoteRoot, true)
	if err != nil {
		t.Fatalf("创建远程测试目录失败: %v", err)
	}
	if !remoteDir.Writable {
		t.Fatalf("远程测试目录应可写: %+v", remoteDir)
	}
	t.Logf("REMOTE_TEST_DIR=%s", remoteDir.Root)

	transfer, err := connection.Client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	defer func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Errorf("关闭 SFTP 通道失败: %v", closeErr)
		}
	}()

	localFile := ws.Path("upload.txt")
	content := []byte("agentflow ssh integration " + randomSuffix(t))
	if err := os.WriteFile(localFile, content, 0o600); err != nil {
		t.Fatalf("写入本地上传文件失败: %v", err)
	}
	remoteFile := path.Join(remoteDir.Root, "upload.txt")
	if err := transfer.Upload(ctx, localFile, remoteFile, sshclient.UploadOptions{
		Overwrite: true,
	}); err != nil {
		t.Fatalf("上传文件失败: %v", err)
	}
	info, err := transfer.Stat(ctx, remoteFile)
	if err != nil {
		t.Fatalf("读取远程文件属性失败: %v", err)
	}
	if info.IsDir {
		t.Fatalf("远程目标应为文件: %+v", info)
	}
	downloaded := ws.Path("download.txt")
	if err := transfer.Download(ctx, remoteFile, downloaded, sshclient.DownloadOptions{}); err != nil {
		t.Fatalf("下载文件失败: %v", err)
	}
	roundTrip, err := os.ReadFile(downloaded)
	if err != nil {
		t.Fatalf("读取下载文件失败: %v", err)
	}
	if string(roundTrip) != string(content) {
		t.Fatalf("往返内容不一致: %q", string(roundTrip))
	}
	if err := transfer.Remove(ctx, remoteFile); err != nil {
		t.Fatalf("删除远程文件失败: %v", err)
	}
	if _, err := transfer.Stat(ctx, remoteFile); err == nil {
		t.Fatal("删除后远程文件不应存在")
	}
}

// TestRealSSHRejectsUntrustedHostKey 验证未信任的主机密钥不会被自动接受。
//
// 该用例使用空的信任记录文件，主机密钥校验必须失败而不是被跳过。
func TestRealSSHRejectsUntrustedHostKey(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	ws := newWorkspace(t)
	connection := dial(t, ws, target)
	// 关闭第一个连接后使用空信任库重新连接，避免复用已建立的信任记录。
	if err := connection.Client.Close(); err != nil {
		t.Fatalf("关闭首个连接失败: %v", err)
	}

	// 使用一个空的 known_hosts 文件构造信任库：主机密钥必须被判为未知。
	untrusted := path.Join(ws.Dir("untrusted"), "known_hosts")
	if err := os.WriteFile(untrusted, nil, 0o600); err != nil {
		t.Fatalf("写入空 known_hosts 失败: %v", err)
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{untrusted},
	})
	if err != nil {
		t.Fatalf("构造空主机密钥库失败: %v", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{HostKeys: store})
	if err != nil {
		t.Fatalf("构造连接器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	// 本版本只支持私钥认证：真实远程测试必须提供私钥路径。
	if target.keyPath == "" {
		t.Fatalf("真实远程测试必须设置私钥路径（本版本只支持私钥认证）")
	}
	auth := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: target.keyPath,
	}
	result, dialErr := dialer.Dial(ctx, sshclient.Endpoint{
		Host: target.host, Port: target.port, Username: target.user,
	}, auth)
	if result.Client != nil {
		_ = result.Client.Close()
	}
	if dialErr == nil {
		t.Fatal("未信任的主机密钥必须导致连接失败")
	}
	if code := sshclient.ErrorCodeOf(dialErr); code != sshclient.CodeHostKeyUnknown {
		t.Fatalf("错误类别应为 host key unknown，实际: %s: %v", code, dialErr)
	}
}

// TestRealSSHCommandStatus 验证真实远程命令的退出码与结果状态。
func TestRealSSHCommandStatus(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	ws := newWorkspace(t)
	connection := dial(t, ws, target)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	// 非零退出码是远程命令的正常结果，不应作为传输层错误返回。
	exitErr, err := connection.Client.Run(ctx, "exit 3", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("非零退出码不应作为传输错误返回: %v", err)
	}
	if exitErr.Status != sshclient.CommandStatusExitError {
		t.Fatalf("状态应为 exit_error，实际: %s", exitErr.Status)
	}
	if exitErr.ExitCode != 3 {
		t.Fatalf("退出码不符: %d", exitErr.ExitCode)
	}
	if exitErr.Succeeded() {
		t.Fatal("非零退出码不得判定为成功")
	}
	// 标准输出与标准错误必须分别收集。
	streams, err := connection.Client.Run(
		ctx,
		`printf out; printf err 1>&2`,
		sshclient.RunOptions{},
	)
	if err != nil {
		t.Fatalf("执行远程命令失败: %v", err)
	}
	if strings.TrimSpace(streams.Stdout) != "out" {
		t.Fatalf("标准输出不符: %q", streams.Stdout)
	}
	if strings.TrimSpace(streams.Stderr) != "err" {
		t.Fatalf("标准错误不符: %q", streams.Stderr)
	}
}

// TestRealSSHCommandTimeout 验证超时会中断真实远程命令并释放会话。
func TestRealSSHCommandTimeout(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	ws := newWorkspace(t)
	connection := dial(t, ws, target)

	started := time.Now()
	result, err := connection.Client.Run(context.Background(), "sleep 30", sshclient.RunOptions{
		Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("超时应当返回错误")
	}
	if result.Status != sshclient.CommandStatusTimeout {
		t.Fatalf("状态应为 timeout，实际: %s", result.Status)
	}
	if elapsed := time.Since(started); elapsed > remoteOpTimeout {
		t.Fatalf("超时应当及时返回，实际耗时: %s", elapsed)
	}
	// 超时后同一连接仍应可用。
	follow, err := connection.Client.Run(
		context.Background(),
		"true",
		sshclient.RunOptions{Timeout: remoteOpTimeout},
	)
	if err != nil {
		t.Fatalf("超时后同一连接仍应可用: %v", err)
	}
	if !follow.Succeeded() {
		t.Fatalf("后续命令应当成功: %+v", follow)
	}
}

// TestRealSSHDownloadSizeLimit 验证真实远程下载的大小上限。
func TestRealSSHDownloadSizeLimit(t *testing.T) {
	t.Parallel()
	target := loadTarget(t)
	ws := newWorkspace(t)
	connection := dial(t, ws, target)

	ctx, cancel := context.WithTimeout(context.Background(), remoteOpTimeout)
	defer cancel()
	home, err := sshclient.ResolveHome(ctx, connection.Client, remoteOpTimeout)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	remoteRoot := path.Join(home, "agentflow-tests", randomSuffix(t))
	remoteDir, err := sshclient.EnsureWorkspace(ctx, connection.Client, remoteRoot, true)
	if err != nil {
		t.Fatalf("创建远程测试目录失败: %v", err)
	}
	t.Logf("REMOTE_TEST_DIR=%s", remoteDir.Root)

	transfer, err := connection.Client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	defer func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Errorf("关闭 SFTP 通道失败: %v", closeErr)
		}
	}()

	localFile := ws.Path("large.txt")
	if err := os.WriteFile(localFile, []byte(strings.Repeat("l", 8192)), 0o600); err != nil {
		t.Fatalf("写入本地上传文件失败: %v", err)
	}
	remoteFile := path.Join(remoteDir.Root, "large.txt")
	if err := transfer.Upload(ctx, localFile, remoteFile, sshclient.UploadOptions{
		Overwrite: true,
	}); err != nil {
		t.Fatalf("上传文件失败: %v", err)
	}
	info, err := transfer.Stat(ctx, remoteFile)
	if err != nil {
		t.Fatalf("读取远程文件属性失败: %v", err)
	}
	if info.Kind != sshclient.FileKindRegular {
		t.Fatalf("远程目标应为普通文件: %+v", info)
	}
	downloadTarget := ws.Path("limited.txt")
	err = transfer.Download(ctx, remoteFile, downloadTarget, sshclient.DownloadOptions{MaxBytes: 1024})
	if err == nil {
		t.Fatal("超过上限的下载应当失败")
	}
	if _, statErr := os.Stat(downloadTarget); statErr == nil {
		t.Fatal("超限下载不应留下最终文件")
	}
}
