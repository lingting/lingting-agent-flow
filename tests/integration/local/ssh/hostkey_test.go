package sshclient_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
)

// TestKnownHostsStoreTrustWritesRecord 验证信任记录写入后可以被校验通过。
func TestKnownHostsStoreTrustWritesRecord(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	storePath := filepath.Join(testkit.Dir(t), "agentflow_known_hosts")
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{StorePath: storePath})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("首次校验应为 unknown，实际: %s", check.Verdict)
	}
	if err := store.Trust(check.Presented); err != nil {
		t.Fatalf("写入信任记录失败: %v", err)
	}
	recheck, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("重新校验主机密钥失败: %v", err)
	}
	if recheck.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("写入后应判定为 trusted，实际: %s", recheck.Verdict)
	}
}

// TestKnownHostsStoreTrustIsIdempotent 验证重复写入同一条记录不会重复追加。
func TestKnownHostsStoreTrustIsIdempotent(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	storePath := filepath.Join(testkit.Dir(t), "agentflow_known_hosts")
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{StorePath: storePath})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	info := sshclient.HostKeyInfo{}
	check, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	info = check.Presented
	for attempt := 0; attempt < 3; attempt++ {
		if err := store.Trust(info); err != nil {
			t.Fatalf("第 %d 次写入信任记录失败: %v", attempt+1, err)
		}
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("读取信任记录失败: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("重复写入应只保留一条记录，实际: %d 行", lines)
	}
}

// TestKnownHostsStoreRejectsEmptyStorePath 验证缺少写入路径时构造失败。
func TestKnownHostsStoreRejectsEmptyStorePath(t *testing.T) {
	t.Parallel()
	if _, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{}); err == nil {
		t.Fatal("缺少记录文件路径时应当返回错误")
	}
}

// TestKnownHostsStoreConcurrentTrust 验证并发写入不会破坏记录文件。
func TestKnownHostsStoreConcurrentTrust(t *testing.T) {
	t.Parallel()
	storePath := filepath.Join(testkit.Dir(t), "agentflow_known_hosts")
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{StorePath: storePath})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	const count = 8
	servers := make([]*testServer, 0, count)
	infos := make([]sshclient.HostKeyInfo, 0, count)
	for index := 0; index < count; index++ {
		server := newTestServer(t)
		host, port := server.endpoint()
		servers = append(servers, server)
		check, checkErr := store.Check(host, port, server.hostKey.PublicKey())
		if checkErr != nil {
			t.Fatalf("校验主机密钥失败: %v", checkErr)
		}
		infos = append(infos, check.Presented)
	}
	var waitGroup sync.WaitGroup
	errs := make([]error, count)
	for index := range infos {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			errs[index] = store.Trust(infos[index])
		}(index)
	}
	waitGroup.Wait()
	for index, writeErr := range errs {
		if writeErr != nil {
			t.Fatalf("第 %d 个并发写入失败: %v", index, writeErr)
		}
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("读取信任记录失败: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines++
		// 每一行都必须是完整的 known_hosts 记录，否则说明并发写入破坏了文件。
		fields := strings.Fields(trimmed)
		if len(fields) != 3 {
			t.Fatalf("记录行被破坏: %q", trimmed)
		}
	}
	if lines != count {
		t.Fatalf("并发写入后应有 %d 条记录，实际: %d", count, lines)
	}
}

// TestKnownHostsStoreDetectsMismatch 验证已信任记录变化被判定为不一致。
func TestKnownHostsStoreDetectsMismatch(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	storePath := filepath.Join(testkit.Dir(t), "agentflow_known_hosts")
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{StorePath: storePath})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if err := store.Trust(check.Presented); err != nil {
		t.Fatalf("写入信任记录失败: %v", err)
	}
	other := newTestServer(t)
	mismatch, err := store.Check(host, port, other.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if mismatch.Verdict != sshclient.VerdictMismatch {
		t.Fatalf("应判定为 mismatch，实际: %s", mismatch.Verdict)
	}
	if len(mismatch.Trusted) == 0 {
		t.Fatal("不一致时应给出已信任的密钥信息")
	}
	if mismatch.Trusted[0].Fingerprint == mismatch.Presented.Fingerprint {
		t.Fatal("已信任与实际指纹不应相同")
	}
}

// TestKnownHostsStoreReadsTrustedPaths 验证复用既有 known_hosts 的信任信息。
func TestKnownHostsStoreReadsTrustedPaths(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	existing := writeKnownHosts(t, host, port, server.hostKey.PublicKey())
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{existing},
		StorePath:    filepath.Join(testkit.Dir(t), "agentflow_known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("既有 known_hosts 中的主机应判定为 trusted，实际: %s", check.Verdict)
	}
	// 用户既有文件必须保持只读：写入只发生在 AgentFlow 自己的记录文件上。
	before, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("读取既有 known_hosts 失败: %v", err)
	}
	other := newTestServer(t)
	otherHost, otherPort := other.endpoint()
	otherCheck, err := store.Check(otherHost, otherPort, other.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if err := store.Trust(otherCheck.Presented); err != nil {
		t.Fatalf("写入信任记录失败: %v", err)
	}
	after, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("读取既有 known_hosts 失败: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("不应修改用户既有的 known_hosts 文件")
	}
}

// TestHostKeyInfoString 验证主机密钥信息的可核验文本。
func TestHostKeyInfoString(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	info := sshclient.HostKeyInfo{
		Host:        "10.0.0.1",
		Port:        2222,
		Algorithm:   "ssh-ed25519",
		Fingerprint: ssh.FingerprintSHA256(server.hostKey.PublicKey()),
	}
	text := info.String()
	for _, expected := range []string{"10.0.0.1:2222", "ssh-ed25519", "SHA256:"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("描述缺少 %s，实际: %s", expected, text)
		}
	}
}
