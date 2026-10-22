package ssh_unit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
)

// writeKnownHostsFile 把内容写入测试工作空间中的 known_hosts 并返回路径。
func writeKnownHostsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(testkit.Dir(t), "known_hosts")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试 known_hosts 失败: %v", err)
	}
	return path
}

// newAlgorithmStore 构造读取指定 known_hosts 的主机密钥库。
func newAlgorithmStore(t *testing.T, trustedPaths ...string) *sshclient.KnownHostsStore {
	t.Helper()
	paths := trustedPaths
	if len(paths) == 0 {
		// 无记录场景使用空文件：主机密钥库要求至少一个 known_hosts 路径。
		paths = []string{writeKnownHostsFile(t, "")}
	}
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: paths,
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	return store
}

// TestRecordedHostKeyTypesKeepsOrder 验证已记录类型按文件顺序返回并去重。
func TestRecordedHostKeyTypesKeepsOrder(t *testing.T) {
	t.Parallel()
	content := "example.com " + ssh.KeyAlgoED25519 + " AAAA\n" +
		"example.com " + ssh.KeyAlgoECDSA256 + " BBBB\n" +
		"example.com " + ssh.KeyAlgoED25519 + " CCCC\n"
	store := newAlgorithmStore(t, writeKnownHostsFile(t, content))

	got := store.RecordedHostKeyTypes("example.com", 22)
	want := []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256}
	if !slices.Equal(got, want) {
		t.Fatalf("已记录类型应为 %v，实际 %v", want, got)
	}
}

// TestRecordedHostKeyTypesEmptyWithoutRecords 验证无记录时返回空，由库自行协商。
func TestRecordedHostKeyTypesEmptyWithoutRecords(t *testing.T) {
	t.Parallel()
	store := newAlgorithmStore(t)

	if got := store.RecordedHostKeyTypes("unknown.example.com", 22); len(got) != 0 {
		t.Fatalf("无记录时应返回空切片，实际 %v", got)
	}
}

// TestRecordedHostKeyTypesMatchHostFieldForms 验证多种主机字段写法都能匹配。
func TestRecordedHostKeyTypesMatchHostFieldForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		field   string
		host    string
		port    int
		matched bool
	}{
		{name: "精确主机", field: "example.com", host: "example.com", port: 22, matched: true},
		{name: "带端口", field: "[example.com]:2222", host: "example.com", port: 2222, matched: true},
		{name: "逗号多主机", field: "other.com,example.com", host: "example.com", port: 22, matched: true},
		{name: "通配符", field: "*.example.com", host: "a.example.com", port: 22, matched: true},
		{name: "问号通配", field: "host?.example.com", host: "host1.example.com", port: 22, matched: true},
		{name: "端口不同不匹配", field: "[example.com]:2222", host: "example.com", port: 22, matched: false},
		{name: "哈希主机名不匹配", field: "|1|abc=|def=", host: "example.com", port: 22, matched: false},
	}
	for _, item := range cases {
		line := item.field + " " + ssh.KeyAlgoRSASHA512 + " AAAA\n"
		store := newAlgorithmStore(t, writeKnownHostsFile(t, line))
		got := store.RecordedHostKeyTypes(item.host, item.port)
		matched := slices.Contains(got, ssh.KeyAlgoRSASHA512)
		if matched != item.matched {
			t.Fatalf("%s: 匹配结果应为 %v，实际记录 %v", item.name, item.matched, got)
		}
	}
}

// TestRecordedHostKeyTypesIgnoreCommentsAndMarkers 验证注释与标记行被跳过。
func TestRecordedHostKeyTypesIgnoreCommentsAndMarkers(t *testing.T) {
	t.Parallel()
	lines := []string{
		"# 注释行",
		"@revoked example.com " + ssh.KeyAlgoRSASHA512 + " AAAA",
		"@cert-authority example.com " + ssh.KeyAlgoECDSA384 + " BBBB",
	}
	store := newAlgorithmStore(t, writeKnownHostsFile(t, strings.Join(lines, "\n")+"\n"))

	if got := store.RecordedHostKeyTypes("example.com", 22); len(got) != 0 {
		t.Fatalf("标记行不应被当作已记录类型，实际 %v", got)
	}
}
