// 本文件是 tests/integration/local/ssh 中连接类用例的迁移结果：
// concurrency_test.go（5）、dialer_test.go（5）与 hostkey_port_test.go（2）。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、目录取用例工作空间；断言保持原样。
package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记连接类 12 条用例，顺序与源文件一致。
var (
	_ = register("run-cancel-does-not-affect-other-sessions", runRunCancelDoesNotAffectOtherSessions)
	_ = register("run-concurrent-commands", runRunConcurrentCommands)
	_ = register("connection-resources-released", runConnectionResourcesReleased)
	_ = register("concurrent-clients-independent", runConcurrentClientsIndependent)
	_ = register("sftp-concurrent-uploads", runSFTPConcurrentUploads)
	_ = register("dial-requires-host-key-store", runDialRequiresHostKeyStore)
	_ = register("dial-unknown-host-key", runDialUnknownHostKey)
	_ = register("dial-trusted-host-key-succeeds", runDialTrustedHostKeySucceeds)
	_ = register("dial-host-key-mismatch", runDialHostKeyMismatch)
	_ = register("private-key-auth", runPrivateKeyAuth)
	_ = register("known-hosts-store-default-port-entry", runKnownHostsStoreDefaultPortEntry)
	_ = register("known-hosts-store-reads-default-port-record", runKnownHostsStoreReadsDefaultPortRecord)
)

// 原 TestRunCancelDoesNotAffectOtherSessions 验证取消一个命令不影响同一连接上的其他调用。
func runRunCancelDoesNotAffectOtherSessions(t *testrunner.Context) {
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

// 原 TestRunConcurrentCommands 验证同一连接上的并发命令互不干扰。
func runRunConcurrentCommands(t *testrunner.Context) {
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

// 原 TestConnectionResourcesReleased 验证关闭连接后服务端连接与会话都释放。
func runConnectionResourcesReleased(t *testrunner.Context) {
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

// 原 TestConcurrentClientsIndependent 验证并发建立的连接各自独立。
func runConcurrentClientsIndependent(t *testrunner.Context) {
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	// 私钥认证不需要凭据输入，因此并发拨号共享同一个私钥配置。
	keyPath := writePrivateKey(t, "", false)
	const count = 4
	clients := make([]sshclient.Client, count)
	errs := make([]error, count)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			dialer := newDialer(t, store)
			config := sshclient.AuthConfig{
				Method:         sshclient.AuthMethodPrivateKey,
				PrivateKeyPath: keyPath,
			}
			result, err := dialer.Dial(
				context.Background(),
				sshclient.Endpoint{Host: host, Port: port, Username: testUser},
				config,
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

// 原 TestSFTPConcurrentUploads 验证并发上传不会互相覆盖。
func runSFTPConcurrentUploads(t *testrunner.Context) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := caseWorkspace(t).Dir("files")
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

// 原 TestDialRequiresHostKeyStore 验证未提供主机密钥校验时不允许建立连接器。
func runDialRequiresHostKeyStore(t *testrunner.Context) {
	if _, err := sshclient.NewDialer(sshclient.DialOptions{}); err == nil {
		t.Fatal("缺少主机密钥校验实现时应当返回错误")
	}
}

// 原 TestDialUnknownHostKey 验证未知主机密钥被拒绝并给出指纹。
func runDialUnknownHostKey(t *testrunner.Context) {
	server := newTestServer(t)
	host, port := server.endpoint()
	dialer := newDialer(t, newStore(t))
	config := privateKeyAuth(t)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config)
	if err == nil {
		t.Fatal("未知主机密钥应当拒绝连接")
	}
	if result.Client != nil {
		t.Fatal("拒绝连接时不应返回已认证连接")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeHostKeyUnknown {
		t.Fatalf("错误类别应为 host_key_unknown，实际: %s", code)
	}
	if result.HostKey.Fingerprint != server.hostKeyFingerprint() {
		t.Fatalf("应报告服务端实际指纹，实际: %s", result.HostKey.Fingerprint)
	}
	if result.HostKeyVerdict != sshclient.VerdictUnknown {
		t.Fatalf("校验结论应为 unknown，实际: %s", result.HostKeyVerdict)
	}
}

// 原 TestDialTrustedHostKeySucceeds 验证已信任主机密钥可以完成私钥认证。
func runDialTrustedHostKeySucceeds(t *testrunner.Context) {
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := privateKeyAuth(t)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config)
	if err != nil {
		t.Fatalf("已信任主机应连接成功: %v", err)
	}
	defer func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭连接失败: %v", closeErr)
		}
	}()
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("校验结论应为 trusted，实际: %s", result.HostKeyVerdict)
	}
	expected := sshclient.Endpoint{Host: host, Port: port}.Address()
	if result.Client.Endpoint().Address() != expected {
		t.Fatalf("连接目标不符: %s", result.Client.Endpoint().Address())
	}
}

// 原 TestDialHostKeyMismatch 验证主机密钥变化时拒绝连接并报告两边指纹。
func runDialHostKeyMismatch(t *testrunner.Context) {
	server := newTestServer(t)
	host, port := server.endpoint()
	// 用另一把主机密钥写入信任记录，模拟服务端密钥被替换。
	other := newTestServer(t)
	store := newStore(t, writeKnownHosts(t, host, port, other.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config := privateKeyAuth(t)
	_, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config)
	if err == nil {
		t.Fatal("主机密钥变化时应当拒绝连接")
	}
	if code := sshclient.ErrorCodeOf(err); code != sshclient.CodeHostKeyMismatch {
		t.Fatalf("错误类别应为 host_key_mismatch，实际: %s", code)
	}
	for _, expected := range []string{server.hostKeyFingerprint(), other.hostKeyFingerprint()} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("错误信息应同时包含实际与已信任指纹 %s，实际: %v", expected, err)
		}
	}
}

// 原 TestPrivateKeyAuth 验证私钥认证可以建立连接。
func runPrivateKeyAuth(t *testrunner.Context) {
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	keyPath := writePrivateKey(t, "", false)
	config := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: keyPath,
	}
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config)
	if err != nil {
		t.Fatalf("私钥认证应成功: %v", err)
	}
	if err := result.Client.Close(); err != nil {
		t.Errorf("关闭连接失败: %v", err)
	}
}

// 原 TestKnownHostsStoreDefaultPortEntry 验证端口为 22 时仍能正确校验信任记录。
//
// known_hosts 在端口为 22 时不写端口，但校验回调要求 host:port 形式的地址；
// 该用例确保两条路径都被覆盖，避免默认端口下主机密钥校验直接报错。
func runKnownHostsStoreDefaultPortEntry(t *testrunner.Context) {
	server := newTestServer(t)
	storePath := filepath.Join(caseWorkspace(t).Dir("known-hosts"), "known_hosts")
	address := net.JoinHostPort("10.0.0.21", strconv.Itoa(22))
	line := knownhosts.Line([]string{knownhosts.Normalize(address)}, server.hostKey.PublicKey()) + "\n"
	if err := os.WriteFile(storePath, []byte(line), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{storePath},
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check("10.0.0.21", 22, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("默认端口校验失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("默认端口记录应判定为 trusted，实际: %s", check.Verdict)
	}
	// 非默认端口的记录必须带端口，因此不会与默认端口记录互相匹配。
	other, err := store.Check("10.0.0.21", 2222, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("非默认端口校验失败: %v", err)
	}
	if other.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("不同端口不应复用默认端口的信任记录，实际: %s", other.Verdict)
	}
}

// 原 TestKnownHostsStoreReadsDefaultPortRecord 验证端口 22 的既有记录参与校验。
func runKnownHostsStoreReadsDefaultPortRecord(t *testrunner.Context) {
	server := newTestServer(t)
	host, _ := server.endpoint()
	address := net.JoinHostPort(host, strconv.Itoa(22))
	line := knownhosts.Line(
		[]string{knownhosts.Normalize(address)},
		server.hostKey.PublicKey(),
	) + "\n"
	userKnownHosts := filepath.Join(caseWorkspace(t).Dir("known-hosts"), "user_known_hosts")
	if err := os.WriteFile(userKnownHosts, []byte(line), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{userKnownHosts},
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check(host, 22, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("默认端口记录应判定为 trusted，实际: %s", check.Verdict)
	}
}
