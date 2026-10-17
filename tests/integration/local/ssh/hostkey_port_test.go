package sshclient_test

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TestKnownHostsStoreDefaultPortEntry 验证端口为 22 时仍能正确校验信任记录。
//
// known_hosts 在端口为 22 时不写端口，但校验回调要求 host:port 形式的地址；
// 该用例确保两条路径都被覆盖，避免默认端口下主机密钥校验直接报错。
func TestKnownHostsStoreDefaultPortEntry(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	storePath := filepath.Join(testkit.Dir(t), "agentflow_known_hosts")
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{StorePath: storePath})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	check, err := store.Check("10.0.0.21", 22, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("默认端口校验失败: %v", err)
	}
	if check.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("首次校验应为 unknown，实际: %s", check.Verdict)
	}
	if err := store.Trust(check.Presented); err != nil {
		t.Fatalf("写入信任记录失败: %v", err)
	}
	recheck, err := store.Check("10.0.0.21", 22, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("重新校验失败: %v", err)
	}
	if recheck.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("默认端口写入后应为 trusted，实际: %s", recheck.Verdict)
	}
	// 非默认端口写入的记录必须带端口，且不会与默认端口记录互相匹配。
	other, err := store.Check("10.0.0.21", 2222, server.hostKey.PublicKey())
	if err != nil {
		t.Fatalf("非默认端口校验失败: %v", err)
	}
	if other.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("不同端口不应复用默认端口的信任记录，实际: %s", other.Verdict)
	}
}

// TestKnownHostsStoreReadsDefaultPortRecord 验证端口 22 的既有记录参与校验。
func TestKnownHostsStoreReadsDefaultPortRecord(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	host, _ := server.endpoint()
	address := net.JoinHostPort(host, strconv.Itoa(22))
	line := knownhosts.Line(
		[]string{knownhosts.Normalize(address)},
		server.hostKey.PublicKey(),
	) + "\n"
	userKnownHosts := filepath.Join(testkit.Dir(t), "user_known_hosts")
	if err := os.WriteFile(userKnownHosts, []byte(line), 0o600); err != nil {
		t.Fatalf("写入 known_hosts 失败: %v", err)
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{userKnownHosts},
		StorePath:    filepath.Join(testkit.Dir(t), "known_hosts"),
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
