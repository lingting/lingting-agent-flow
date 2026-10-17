package sshclient_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// dialTestServer 按给定的主机密钥库连接测试服务并返回已认证连接。
func dialTestServer(
	t *testing.T,
	server *testServer,
	store sshclient.HostKeyStore,
) sshclient.Client {
	t.Helper()
	host, port := server.endpoint()
	dialer := newDialer(t, store)
	config, provider := passwordAuth(testPassword)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err != nil {
		t.Fatalf("建立测试连接失败: %v", err)
	}
	return result.Client
}

// TestRunReportsStructuredStatus 验证命令结果状态能区分成功、非零退出与中断。
func TestRunReportsStructuredStatus(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)

	success, err := client.Run(testContext(t), "stream", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if success.Status != sshclient.CommandStatusSucceeded || success.ExitCode != 0 {
		t.Fatalf("成功结果不符: %+v", success)
	}

	exitError, err := client.Run(testContext(t), "exit 42", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("非零退出码不应作为传输错误返回: %v", err)
	}
	if exitError.Status != sshclient.CommandStatusExitError {
		t.Fatalf("状态应为 exit_error，实际: %s", exitError.Status)
	}
	if exitError.ExitCode != 42 {
		t.Fatalf("退出码不符: %d", exitError.ExitCode)
	}
	if exitError.Succeeded() {
		t.Fatal("非零退出码不得判定为成功")
	}
	if exitError.Stdout != "exiting\n" {
		t.Fatalf("非零退出码时仍应收集标准输出: %q", exitError.Stdout)
	}
}

// TestRunTimeoutReportsStatus 验证超时返回 timeout 状态并关闭远程会话。
func TestRunTimeoutReportsStatus(t *testing.T) {
	t.Parallel()
	server, client := newAuthenticatedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := client.Run(ctx, "slow", sshclient.RunOptions{})
	if err == nil {
		t.Fatal("超时应当返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应包装上下文超时，实际: %v", err)
	}
	if result.Status != sshclient.CommandStatusTimeout {
		t.Fatalf("状态应为 timeout，实际: %s", result.Status)
	}
	if !result.TimedOut() {
		t.Fatal("TimedOut 应识别超时状态")
	}
	if result.Succeeded() {
		t.Fatal("超时不得判定为成功")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("超时应当及时返回，实际耗时: %s", elapsed)
	}
	// 会话通道必须在超时后被关闭，否则测试服务上的会话不会释放。
	waitFor(t, 5*time.Second, func() bool { return server.activeSessions() == 0 },
		"超时后远程会话应当释放")
}

// TestRunOptionsTimeoutInterrupts 验证 RunOptions.Timeout 生效。
func TestRunOptionsTimeoutInterrupts(t *testing.T) {
	t.Parallel()
	server, client := newAuthenticatedClient(t)
	started := time.Now()
	result, err := client.Run(testContext(t), "slow", sshclient.RunOptions{
		Timeout: 250 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("RunOptions.Timeout 超时应当返回错误")
	}
	if result.Status != sshclient.CommandStatusTimeout {
		t.Fatalf("状态应为 timeout，实际: %s", result.Status)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("超时应当及时返回，实际耗时: %s", elapsed)
	}
	waitFor(t, 5*time.Second, func() bool { return server.activeSessions() == 0 },
		"超时后远程会话应当释放")
}

// TestRunCancelDoesNotAffectOtherSessions 验证取消一个命令不影响同一连接上的其他调用。
func TestRunCancelDoesNotAffectOtherSessions(t *testing.T) {
	t.Parallel()
	server, client := newAuthenticatedClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan sshclient.CommandResult, 1)
	go func() {
		result, _ := client.Run(ctx, "slow", sshclient.RunOptions{})
		cancelled <- result
	}()
	// 等待远程命令真正开始执行，再取消它。
	waitFor(t, 5*time.Second, func() bool { return server.executingPeak() >= 1 },
		"远程命令应当已开始执行")
	cancel()
	result := <-cancelled
	if result.Status != sshclient.CommandStatusCancelled {
		t.Fatalf("状态应为 cancelled，实际: %s", result.Status)
	}
	// 同一连接上的独立命令必须继续可用。
	follow, err := client.Run(testContext(t), "stream", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("取消一个命令后其他命令仍应可用: %v", err)
	}
	if follow.Stdout != "out" || follow.Stderr != "err" {
		t.Fatalf("后续命令结果不符: %+v", follow)
	}
}

// TestRunConcurrentCommands 验证同一连接上的并发命令互不干扰。
func TestRunConcurrentCommands(t *testing.T) {
	t.Parallel()
	server, client := newAuthenticatedClient(t)
	const count = 4
	var waitGroup sync.WaitGroup
	results := make([]sshclient.CommandResult, count)
	errs := make([]error, count)
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			results[index], errs[index] = client.Run(
				testContext(t),
				"sleepy",
				sshclient.RunOptions{},
			)
		}(index)
	}
	waitGroup.Wait()
	for index := 0; index < count; index++ {
		if errs[index] != nil {
			t.Fatalf("第 %d 个并发命令失败: %v", index, errs[index])
		}
		if results[index].ExitCode != 0 {
			t.Fatalf("第 %d 个并发命令退出码不符: %d", index, results[index].ExitCode)
		}
	}
	if peak := server.executingPeak(); peak < 2 {
		t.Fatalf("并发命令应当同时执行，观察到的峰值为 %d", peak)
	}
}

// TestRunOutputLimitIsExplicit 验证输出截断被明确标记而不是静默丢弃。
func TestRunOutputLimitIsExplicit(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	result, err := client.Run(testContext(t), "big", sshclient.RunOptions{
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if !result.StdoutTruncated {
		t.Fatal("超过上限时应当标记截断")
	}
	if len(result.Stdout) != 1024 {
		t.Fatalf("截断后应保留上限字节，实际: %d", len(result.Stdout))
	}
	// 未超过上限时不得标记截断。
	full, err := client.Run(testContext(t), "stream", sshclient.RunOptions{
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if full.StdoutTruncated || full.StderrTruncated {
		t.Fatalf("未超过上限时不应标记截断: %+v", full)
	}
}

// TestConnectionResourcesReleased 验证关闭连接后服务端连接与会话都释放。
func TestConnectionResourcesReleased(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	client := dialTestServer(t, server, store)
	waitFor(t, 5*time.Second, func() bool { return server.activeConnections() == 1 },
		"建立连接后应有一个活动连接")
	if _, err := client.Run(testContext(t), "stream", sshclient.RunOptions{}); err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return server.activeSessions() == 0 },
		"命令结束后会话应当释放")
	if err := client.Close(); err != nil {
		t.Fatalf("关闭连接失败: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("重复关闭应当返回 nil: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return server.activeConnections() == 0 },
		"关闭连接后服务端连接应当释放")
}

// TestConcurrentClientsIndependent 验证并发建立的连接各自独立。
func TestConcurrentClientsIndependent(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	const count = 4
	clients := make([]sshclient.Client, count)
	errs := make([]error, count)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			dialer := newDialer(t, store)
			config, provider := passwordAuth(testPassword)
			result, err := dialer.Dial(
				context.Background(),
				sshclient.Endpoint{Host: host, Port: port, Username: testUser},
				config,
				provider,
			)
			if err != nil {
				errs[index] = err
				return
			}
			clients[index] = result.Client
		}(index)
	}
	waitGroup.Wait()
	for index := 0; index < count; index++ {
		if errs[index] != nil {
			t.Fatalf("第 %d 个并发连接失败: %v", index, errs[index])
		}
	}
	// 关闭其中一个连接不影响其他连接。
	if err := clients[0].Close(); err != nil {
		t.Fatalf("关闭首个连接失败: %v", err)
	}
	for index := 1; index < count; index++ {
		if _, err := clients[index].Run(testContext(t), "stream", sshclient.RunOptions{}); err != nil {
			t.Fatalf("关闭其他连接后第 %d 个连接应仍可用: %v", index, err)
		}
	}
	for index := 1; index < count; index++ {
		if err := clients[index].Close(); err != nil {
			t.Errorf("关闭第 %d 个连接失败: %v", index, err)
		}
	}
	waitFor(t, 5*time.Second, func() bool { return server.activeConnections() == 0 },
		"全部连接关闭后服务端连接应当释放")
}

// TestAgentAuthentication 验证通过 SSH Agent 完成认证。
func TestAgentAuthentication(t *testing.T) {
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	socket := startTestAgent(t)
	config := sshclient.AuthConfig{
		Method:      sshclient.AuthMethodAgent,
		AgentSocket: socket,
	}
	dialer := newDialer(t, store)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err != nil {
		t.Fatalf("SSH Agent 认证应成功: %v", err)
	}
	if err := result.Client.Close(); err != nil {
		t.Errorf("关闭连接失败: %v", err)
	}
}

// TestAgentWithoutIdentity 验证 Agent 没有身份时返回明确错误。
func TestAgentWithoutIdentity(t *testing.T) {
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	socket := startEmptyAgent(t)
	config := sshclient.AuthConfig{
		Method:      sshclient.AuthMethodAgent,
		AgentSocket: socket,
	}
	dialer := newDialer(t, store)
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("Agent 没有身份时应当失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeAgentUnavailable {
		t.Fatalf("错误类别应为 agent_unavailable，实际: %s", code)
	}
}

// TestPrivateKeyRejectedByServer 验证私钥不被服务端接受时归类为认证失败。
func TestPrivateKeyRejectedByServer(t *testing.T) {
	t.Parallel()
	// 服务端只接受另一把公钥，客户端私钥认证必然被拒绝。
	otherKeyPath := writePrivateKey(t, "", false)
	authorizedSigner := mustParsePrivateKey(t, otherKeyPath)
	server := newTestServerWithOptions(t, testServerOptions{
		AuthorizedKey: authorizedSigner.PublicKey(),
	})
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: writePrivateKey(t, "", false),
	}
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, nil)
	if err == nil {
		t.Fatal("服务端拒绝私钥时应当认证失败")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeAuthFailed {
		t.Fatalf("错误类别应为 auth_failed，实际: %s", code)
	}
}

// TestSFTPDownloadSizeLimit 验证下载超过大小上限时被拒绝且不留下文件。
func TestSFTPDownloadSizeLimit(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := testkit.Dir(t)
	source := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(source, []byte(strings.Repeat("z", 4096)), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	target := filepath.Join(dir, "limited.txt")
	err := transfer.Download(testContext(t), remotePath, target, sshclient.DownloadOptions{
		MaxBytes: 1024,
	})
	if !errors.Is(err, sshclient.ErrRemoteSizeExceeded) {
		t.Fatalf("应当返回 ErrRemoteSizeExceeded，实际: %v", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("超限下载不应留下最终文件")
	}
	// 上限足够时下载应当成功。
	if err := transfer.Download(testContext(t), remotePath, target, sshclient.DownloadOptions{
		MaxBytes: 8192,
	}); err != nil {
		t.Fatalf("未超限的下载应当成功: %v", err)
	}
}

// TestSFTPDownloadRejectsDirectory 验证下载接口不会对目录操作。
func TestSFTPDownloadRejectsDirectory(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := path.Join(tempRemoteDir(t), "download-dir")
	if err := transfer.MkdirAll(testContext(t), dir); err != nil {
		t.Fatalf("创建远程目录失败: %v", err)
	}
	target := filepath.Join(testkit.Dir(t), "from-dir.txt")
	err := transfer.Download(testContext(t), dir, target, sshclient.DownloadOptions{})
	if !errors.Is(err, sshclient.ErrUnsupportedRemoteType) {
		t.Fatalf("对目录下载应返回 ErrUnsupportedRemoteType，实际: %v", err)
	}
}

// TestSFTPStatReportsKind 验证属性查询区分普通文件与目录。
func TestSFTPStatReportsKind(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := path.Join(tempRemoteDir(t), "kind-dir")
	if err := transfer.MkdirAll(testContext(t), dir); err != nil {
		t.Fatalf("创建远程目录失败: %v", err)
	}
	dirInfo, err := transfer.Stat(testContext(t), dir)
	if err != nil {
		t.Fatalf("读取目录属性失败: %v", err)
	}
	if dirInfo.Kind != sshclient.FileKindDirectory || !dirInfo.IsDir {
		t.Fatalf("目录类型不符: %+v", dirInfo)
	}
	source := filepath.Join(testkit.Dir(t), "kind.txt")
	if err := os.WriteFile(source, []byte("kind"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := path.Join(dir, "kind.txt")
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	fileInfo, err := transfer.Stat(testContext(t), remotePath)
	if err != nil {
		t.Fatalf("读取文件属性失败: %v", err)
	}
	if fileInfo.Kind != sshclient.FileKindRegular || !fileInfo.IsRegular {
		t.Fatalf("文件类型不符: %+v", fileInfo)
	}
	lstatInfo, err := transfer.Lstat(testContext(t), remotePath)
	if err != nil {
		t.Fatalf("Lstat 读取文件属性失败: %v", err)
	}
	if lstatInfo.Kind != sshclient.FileKindRegular {
		t.Fatalf("Lstat 类型不符: %+v", lstatInfo)
	}
}

// TestSFTPConcurrentUploads 验证并发上传不会互相覆盖。
func TestSFTPConcurrentUploads(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := testkit.Dir(t)
	remoteDir := path.Join(tempRemoteDir(t), "concurrent")
	if err := transfer.MkdirAll(testContext(t), remoteDir); err != nil {
		t.Fatalf("创建远程目录失败: %v", err)
	}
	const count = 6
	var waitGroup sync.WaitGroup
	errs := make([]error, count)
	for index := 0; index < count; index++ {
		localPath := filepath.Join(dir, fmt.Sprintf("upload-%d.txt", index))
		content := fmt.Sprintf("content-%d", index)
		if err := os.WriteFile(localPath, []byte(content), 0o600); err != nil {
			t.Fatalf("写入本机测试文件失败: %v", err)
		}
		waitGroup.Add(1)
		go func(index int, localPath, content string) {
			defer waitGroup.Done()
			errs[index] = transfer.Upload(
				context.Background(),
				localPath,
				path.Join(remoteDir, fmt.Sprintf("file-%d.txt", index)),
				sshclient.UploadOptions{},
			)
		}(index, localPath, content)
	}
	waitGroup.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发上传失败: %v", index, err)
		}
	}
	entries, err := transfer.ReadDir(testContext(t), remoteDir)
	if err != nil {
		t.Fatalf("列出远程目录失败: %v", err)
	}
	if len(entries) != count {
		t.Fatalf("并发上传后应有 %d 个文件，实际: %d", count, len(entries))
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, ".tmp") {
			t.Fatalf("上传后不应残留临时文件: %s", entry.Name)
		}
	}
}

// TestSFTPUploadOverwriteIsAtomic 验证覆盖上传使用原子替换且内容完整。
func TestSFTPUploadOverwriteIsAtomic(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := testkit.Dir(t)
	source := filepath.Join(dir, "atomic.txt")
	if err := os.WriteFile(source, []byte("first"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("首次上传失败: %v", err)
	}
	if err := os.WriteFile(source, []byte("second-version"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		Overwrite: true,
	}); err != nil {
		t.Fatalf("覆盖上传失败: %v", err)
	}
	info, err := transfer.Stat(testContext(t), remotePath)
	if err != nil {
		t.Fatalf("读取远程属性失败: %v", err)
	}
	if info.Size != int64(len("second-version")) {
		t.Fatalf("覆盖后大小不符: %d", info.Size)
	}
	entries, err := transfer.ReadDir(testContext(t), path.Dir(remotePath))
	if err != nil {
		t.Fatalf("列出远程目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, ".tmp") {
			t.Fatalf("覆盖上传后不应残留临时文件: %s", entry.Name)
		}
	}
}

// TestSFTPInterruptedUploadLeavesNoFinalFile 验证取消上传不会留下最终文件。
func TestSFTPInterruptedUploadLeavesNoFinalFile(t *testing.T) {
	t.Parallel()
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	source := filepath.Join(testkit.Dir(t), "interrupted.txt")
	if err := os.WriteFile(source, []byte(strings.Repeat("q", 64*1024)), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := transfer.Upload(ctx, source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	})
	if err == nil {
		t.Fatal("已取消的上传应当返回错误")
	}
	if _, statErr := transfer.Stat(testContext(t), remotePath); statErr == nil {
		t.Fatal("取消后不应留下最终文件")
	}
}

// waitFor 在超时前轮询条件，用于验证异步资源释放。
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s（等待超时 %s）", message, timeout)
}

// mustParsePrivateKey 解析测试私钥文件，用于取得对应的公钥。
func mustParsePrivateKey(t *testing.T, path string) ssh.Signer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取测试私钥失败: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("解析测试私钥失败: %v", err)
	}
	return signer
}

// startTestAgent 在测试工作空间中启动一个包含一把身份的 SSH Agent。
func startTestAgent(t *testing.T) string {
	t.Helper()
	keyring := agent.NewKeyring()
	_, privateKey := generateEd25519KeyPair(t)
	// Agent 需要原始私钥对象而不是 ssh.Signer 包装，否则会因类型不受支持而失败。
	if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey}); err != nil {
		t.Fatalf("向测试 Agent 添加身份失败: %v", err)
	}
	return serveTestAgent(t, keyring)
}

// startEmptyAgent 在测试工作空间中启动一个没有任何身份的 SSH Agent。
func startEmptyAgent(t *testing.T) string {
	t.Helper()
	return serveTestAgent(t, agent.NewKeyring())
}

// serveTestAgent 在 Unix 套接字上提供 SSH Agent 协议服务。
func serveTestAgent(t *testing.T, keyring agent.Agent) string {
	socket := filepath.Join(testkit.Dir(t), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("WARN [SKIP] 当前环境不支持 Unix 套接字，无法验证 SSH Agent: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Logf("关闭测试 Agent 监听失败: %v", closeErr)
		}
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() {
					_ = conn.Close()
				}()
				_ = agent.ServeAgent(keyring, conn)
			}(conn)
		}
	}()
	return socket
}

// TestRunTerminationConfirmation 验证正常结束与中断的终止确认状态。
func TestRunTerminationConfirmation(t *testing.T) {
	t.Parallel()
	server, client := newAuthenticatedClient(t)
	done, err := client.Run(testContext(t), "stream", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if !done.TerminationConfirmed {
		t.Fatal("正常结束应确认远程命令已终止")
	}
	interrupted, err := client.Run(
		context.Background(),
		"slow",
		sshclient.RunOptions{Timeout: 200 * time.Millisecond},
	)
	if err == nil {
		t.Fatal("超时应当返回错误")
	}
	if interrupted.TerminationConfirmed {
		t.Fatal("超时不得声称已确认远程进程终止")
	}
	waitFor(t, 5*time.Second, func() bool { return server.activeSessions() == 0 },
		"超时后远程会话应当释放")
}
