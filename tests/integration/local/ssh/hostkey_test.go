package sshclient_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TestKnownHostsStoreReadsExistingRecord 验证既有 known_hosts 记录可被校验通过。
//
// 主机密钥库只读：信任记录必须由用户在 AgentFlow 之外写入 known_hosts，
// 因此这里直接构造文件，而不是调用主机密钥库写入。
func TestKnownHostsStoreReadsExistingRecord(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	existing := writeKnownHosts(t, host, port, server.hostKey.PublicKey())
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{existing},
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("既有记录应判定为 trusted，实际: %s", check.Verdict)
	}
}

// TestKnownHostsStoreIgnoresDuplicateLines 验证重复的 known_hosts 记录不影响校验。
func TestKnownHostsStoreIgnoresDuplicateLines(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	existing := writeKnownHosts(t, host, port, server.hostKey.PublicKey())
	// 重复写入同一行由用户或 ssh-keyscan 产生，校验必须仍然判定为 trusted。
	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}
	if err := os.WriteFile(existing, append(content, content...), 0o600); err != nil {
		t.Fatalf("写入重复记录失败: %v", err)
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{existing},
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check(host, port, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("重复记录不应影响校验结论，实际: %s", check.Verdict)
	}
}

// TestKnownHostsStoreRequiresPath 验证缺少 known_hosts 路径时构造失败。
func TestKnownHostsStoreRequiresPath(t *testing.T) {
	t.Parallel()
	if _, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{}); err == nil {
		t.Fatal("缺少 known_hosts 路径时应当返回错误")
	}
}

// TestKnownHostsStoreConcurrentCheckIsReadOnly 验证并发校验不会读写同一文件。
//
// 主机密钥库只读，因此并发校验既不加锁也不写文件：同一份 known_hosts 上的
// 并发 Check 必须全部得到相同结论，且文件内容在并发前后完全一致。
func TestKnownHostsStoreConcurrentCheckIsReadOnly(t *testing.T) {
	t.Parallel()
	const count = 8
	servers := make([]*testServer, 0, count)
	content := ""
	addresses := make([]string, 0, count)
	for index := 0; index < count; index++ {
		server := newTestServer(t)
		host, port := server.endpoint()
		servers = append(servers, server)
		address := net.JoinHostPort(host, strconv.Itoa(port))
		addresses = append(addresses, address)
		content += knownhosts.Line([]string{knownhosts.Normalize(address)}, server.hostKey.PublicKey()) + "\n"
	}
	storePath := filepath.Join(testkit.Dir(t), "known_hosts")
	if err := os.WriteFile(storePath, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{storePath},
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	var waitGroup sync.WaitGroup
	verdicts := make([]sshclient.HostKeyVerdict, count)
	errs := make([]error, count)
	for index := range servers {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			host, port, splitErr := net.SplitHostPort(addresses[index])
			if splitErr != nil {
				errs[index] = splitErr
				return
			}
			parsedPort, convErr := strconv.Atoi(port)
			if convErr != nil {
				errs[index] = convErr
				return
			}
			check, checkErr := store.Check(host, parsedPort, servers[index].hostKey.PublicKey())
			errs[index] = checkErr
			verdicts[index] = check.Verdict
		}(index)
	}
	waitGroup.Wait()
	for index, checkErr := range errs {
		if checkErr != nil {
			t.Fatalf("第 %d 个并发校验失败: %v", index, checkErr)
		}
		if verdicts[index] != sshclient.VerdictTrusted {
			t.Fatalf("第 %d 个并发校验结论应为 trusted，实际: %s", index, verdicts[index])
		}
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("只读校验不应修改 known_hosts 文件")
	}
}

// TestKnownHostsStoreDetectsMismatch 验证已信任记录变化被判定为不一致。
func TestKnownHostsStoreDetectsMismatch(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	existing := writeKnownHosts(t, host, port, server.hostKey.PublicKey())
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{existing},
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
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
//
// 用户既有文件必须保持只读：校验任意数量主机后文件内容都不能改变。
func TestKnownHostsStoreReadsTrustedPaths(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, port := server.endpoint()
	existing := writeKnownHosts(t, host, port, server.hostKey.PublicKey())
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{existing},
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
	if otherCheck.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("未记录的主机应判定为 unknown，实际: %s", otherCheck.Verdict)
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
