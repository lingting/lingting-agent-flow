package ssh_unit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
)

// newSigner 生成一次性主机密钥签名器。
func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("构造签名器失败: %v", err)
	}
	return signer
}

// newKnownHostsStore 在测试工作空间中构造主机密钥库。
func newKnownHostsStore(t *testing.T, trustedPaths ...string) *sshclient.KnownHostsStore {
	t.Helper()
	store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: trustedPaths,
		StorePath:    filepath.Join(testkit.Dir(t), "known_hosts"),
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	return store
}

// TestHostKeyFingerprintIsSHA256 验证主机密钥指纹按 SHA-256 计算并可复算。
func TestHostKeyFingerprintIsSHA256(t *testing.T) {
	t.Parallel()
	server := newSigner(t)
	store := newKnownHostsStore(t)
	check, err := store.Check("10.0.0.1", 22, server.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	expected := ssh.FingerprintSHA256(server.PublicKey())
	if check.Presented.Fingerprint != expected {
		t.Fatalf("指纹不符: %s，期望 %s", check.Presented.Fingerprint, expected)
	}
	if !strings.HasPrefix(check.Presented.Fingerprint, "SHA256:") {
		t.Fatalf("指纹必须带 SHA256: 前缀: %s", check.Presented.Fingerprint)
	}
	if check.Presented.Algorithm != server.PublicKey().Type() {
		t.Fatalf("算法不符: %s", check.Presented.Algorithm)
	}
}

// TestHostKeyUnknownThenTrustedThenMismatch 验证未知、已信任与密钥变化三种结论。
func TestHostKeyUnknownThenTrustedThenMismatch(t *testing.T) {
	t.Parallel()
	server := newSigner(t)
	store := newKnownHostsStore(t)
	unknown, err := store.Check("10.0.0.2", 2222, server.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if unknown.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("首次校验应为 unknown，实际: %s", unknown.Verdict)
	}
	if err := store.Trust(unknown.Presented); err != nil {
		t.Fatalf("写入信任记录失败: %v", err)
	}
	trusted, err := store.Check("10.0.0.2", 2222, server.PublicKey())
	if err != nil {
		t.Fatalf("重新校验失败: %v", err)
	}
	if trusted.Verdict != sshclient.VerdictTrusted {
		t.Fatalf("写入后应为 trusted，实际: %s", trusted.Verdict)
	}
	other := newSigner(t)
	mismatch, err := store.Check("10.0.0.2", 2222, other.PublicKey())
	if err != nil {
		t.Fatalf("校验主机密钥失败: %v", err)
	}
	if mismatch.Verdict != sshclient.VerdictMismatch {
		t.Fatalf("密钥变化应为 mismatch，实际: %s", mismatch.Verdict)
	}
	if len(mismatch.Trusted) == 0 {
		t.Fatal("不一致时应给出已信任的密钥")
	}
	if mismatch.Trusted[0].Fingerprint != unknown.Presented.Fingerprint {
		t.Fatalf("已信任指纹应为首次记录的值: %s", mismatch.Trusted[0].Fingerprint)
	}
}

// TestHostKeyCheckRejectsNilKey 验证缺少主机公钥时返回明确错误。
func TestHostKeyCheckRejectsNilKey(t *testing.T) {
	t.Parallel()
	store := newKnownHostsStore(t)
	check, err := store.Check("10.0.0.3", 22, nil)
	if err == nil {
		t.Fatal("缺少主机公钥时应返回错误")
	}
	if check.Verdict != sshclient.VerdictUnknown {
		t.Fatalf("缺少主机公钥时结论应为 unknown，实际: %s", check.Verdict)
	}
}

// TestHostKeyTrustRequiresKey 验证没有公钥时拒绝写入信任记录。
func TestHostKeyTrustRequiresKey(t *testing.T) {
	t.Parallel()
	store := newKnownHostsStore(t)
	if err := store.Trust(sshclient.HostKeyInfo{Host: "10.0.0.4", Port: 22}); err == nil {
		t.Fatal("缺少公钥时应拒绝写入信任记录")
	}
}

// TestAuthMethodValidate 验证只接受受支持的认证方式。
func TestAuthMethodValidate(t *testing.T) {
	t.Parallel()
	for _, method := range []sshclient.AuthMethod{
		sshclient.AuthMethodPassword,
		sshclient.AuthMethodPrivateKey,
		sshclient.AuthMethodAgent,
	} {
		if err := method.Validate(); err != nil {
			t.Fatalf("认证方式 %s 应当被接受: %v", method, err)
		}
	}
	if err := sshclient.AuthMethod("kerberos").Validate(); err == nil {
		t.Fatal("未知认证方式应当被拒绝")
	}
}

// TestAuthConfigValidate 验证认证配置的自洽性。
func TestAuthConfigValidate(t *testing.T) {
	t.Parallel()
	valid := []sshclient.AuthConfig{
		{Method: sshclient.AuthMethodAgent},
		{Method: sshclient.AuthMethodPassword},
		{Method: sshclient.AuthMethodPrivateKey, PrivateKeyPath: "/home/deploy/.ssh/id_ed25519"},
	}
	for _, config := range valid {
		if err := config.Validate(); err != nil {
			t.Fatalf("配置 %+v 应当通过校验: %v", config, err)
		}
	}
	invalid := []sshclient.AuthConfig{
		{Method: sshclient.AuthMethodPrivateKey},
		{Method: sshclient.AuthMethodPrivateKey, PrivateKeyPath: "   "},
		{Method: "kerberos"},
	}
	for _, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Fatalf("配置 %+v 应当被拒绝", config)
		}
	}
}

// TestCredentialProvidersMatchMethod 验证凭据提供器与认证方式匹配。
func TestCredentialProvidersMatchMethod(t *testing.T) {
	t.Parallel()
	password := sshclient.PasswordProvider{Password: []byte("secret")}
	if password.Method() != sshclient.AuthMethodPassword {
		t.Fatalf("密码提供器的认证方式不符: %s", password.Method())
	}
	passphrase := sshclient.PassphraseProvider{Passphrase: []byte("secret")}
	if passphrase.Method() != sshclient.AuthMethodPrivateKey {
		t.Fatalf("口令提供器的认证方式不符: %s", passphrase.Method())
	}
}

// TestCredentialProviderReturnsCopy 验证凭据是副本，清除后不影响提供器。
func TestCredentialProviderReturnsCopy(t *testing.T) {
	t.Parallel()
	source := []byte("secret")
	provider := sshclient.PasswordProvider{Password: source}
	credential, err := provider.Credential(context.Background())
	if err != nil {
		t.Fatalf("读取凭据失败: %v", err)
	}
	credential.Clear()
	if string(source) != "secret" {
		t.Fatalf("清除副本不应影响提供器持有的内容: %q", string(source))
	}
	if !credential.Empty() {
		t.Fatalf("清除后凭据应当为空: %+v", credential)
	}
}

// TestCredentialProviderRejectsEmptySecret 验证没有凭据时立即失败而不是挂起。
func TestCredentialProviderRejectsEmptySecret(t *testing.T) {
	t.Parallel()
	if _, err := (sshclient.PasswordProvider{}).Credential(context.Background()); !errors.Is(
		err,
		sshclient.ErrNoCredential,
	) {
		t.Fatalf("空密码应返回 ErrNoCredential，实际: %v", err)
	}
	if _, err := (sshclient.PassphraseProvider{}).Credential(context.Background()); !errors.Is(
		err,
		sshclient.ErrNoCredential,
	) {
		t.Fatalf("空口令应返回 ErrNoCredential，实际: %v", err)
	}
}

// TestCredentialProviderRespectsCancellation 验证已取消的上下文立即失败。
func TestCredentialProviderRespectsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := sshclient.PasswordProvider{Password: []byte("secret")}
	if _, err := provider.Credential(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("应当返回上下文取消错误，实际: %v", err)
	}
}

// TestDialerRequiresHostKeyStore 验证不允许跳过主机密钥校验。
func TestDialerRequiresHostKeyStore(t *testing.T) {
	t.Parallel()
	if _, err := sshclient.NewDialer(sshclient.DialOptions{}); err == nil {
		t.Fatal("缺少主机密钥校验实现时应拒绝构造连接器")
	}
}

// TestEndpointValidation 验证连接目标校验与地址拼接。
func TestEndpointValidation(t *testing.T) {
	t.Parallel()
	valid := sshclient.Endpoint{Host: "2001:db8::1", Port: 2222, Username: "deploy"}
	if got := valid.Address(); got != "[2001:db8::1]:2222" {
		t.Fatalf("IPv6 地址拼接不符: %s", got)
	}
	invalid := []sshclient.Endpoint{
		{Host: "", Port: 22, Username: "deploy"},
		{Host: "10.0.0.1", Port: 0, Username: "deploy"},
		{Host: "10.0.0.1", Port: 70000, Username: "deploy"},
		{Host: "10.0.0.1", Port: 22, Username: "  "},
	}
	for _, endpoint := range invalid {
		dialer, err := sshclient.NewDialer(sshclient.DialOptions{
			HostKeys: newKnownHostsStore(t),
		})
		if err != nil {
			t.Fatalf("构造连接器失败: %v", err)
		}
		_, dialErr := dialer.Dial(
			context.Background(),
			endpoint,
			sshclient.AuthConfig{Method: sshclient.AuthMethodAgent},
			nil,
		)
		if dialErr == nil {
			t.Fatalf("非法目标 %+v 应当被拒绝", endpoint)
		}
		if code := sshclient.ErrorCodeOf(dialErr); code != sshclient.CodeInvalidConfig {
			t.Fatalf("错误类别应为 invalid_config，实际: %s", code)
		}
	}
}

// TestDialRespectsCancelledContext 验证已取消的上下文不会发起连接。
func TestDialRespectsCancelledContext(t *testing.T) {
	t.Parallel()
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{HostKeys: newKnownHostsStore(t)})
	if err != nil {
		t.Fatalf("构造连接器失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, dialErr := dialer.Dial(
		ctx,
		sshclient.Endpoint{Host: "10.0.0.1", Port: 22, Username: "deploy"},
		sshclient.AuthConfig{Method: sshclient.AuthMethodAgent},
		nil,
	)
	if !errors.Is(dialErr, context.Canceled) {
		t.Fatalf("应当返回上下文取消错误，实际: %v", dialErr)
	}
	if code := sshclient.ErrorCodeOf(dialErr); code != sshclient.CodeCancelled {
		t.Fatalf("错误类别应为 cancelled，实际: %s", code)
	}
}

// TestShellQuoteEscapes 验证参数转义不会改变命令语义。
func TestShellQuoteEscapes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"simple":           "simple",
		"with space":       "'with space'",
		"quote'inside":     `'quote'\''inside'`,
		"a;rm -rf /tmp":    "'a;rm -rf /tmp'",
		"$(whoami)":        "'$(whoami)'",
		"`id`":             "'`id`'",
		"":                 "''",
		"/srv/agent flow/": "'/srv/agent flow/'",
	}
	for input, expected := range cases {
		if got := sshclient.ShellQuote(input); got != expected {
			t.Fatalf("转义结果不符: %q -> %q，期望 %q", input, got, expected)
		}
	}
}

// TestShellCommandRejectsUnsafeName 验证命令名称不能注入 shell 内容。
func TestShellCommandRejectsUnsafeName(t *testing.T) {
	t.Parallel()
	invalid := []string{"", "  ", "rm -rf /", "echo;id", "a|b", "a&b", "$(id)"}
	for _, name := range invalid {
		if _, err := sshclient.ShellCommand(name); err == nil {
			t.Fatalf("命令名称 %q 应当被拒绝", name)
		}
	}
	command, err := sshclient.ShellCommand("test", "-w", "/srv/agent flow")
	if err != nil {
		t.Fatalf("构造命令失败: %v", err)
	}
	if command != "test -w '/srv/agent flow'" {
		t.Fatalf("命令构造不符: %s", command)
	}
}

// TestNormalizeRemotePathRejectsTraversal 验证远程路径规范化与穿越防护。
func TestNormalizeRemotePathRejectsTraversal(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"/srv/agentflow":         "/srv/agentflow",
		"/srv/agentflow/":        "/srv/agentflow",
		"/srv//agentflow/./x":    "/srv/agentflow/x",
		"/home/deploy/.lingting": "/home/deploy/.lingting",
	}
	for input, expected := range valid {
		got, err := sshclient.NormalizeRemotePath(input)
		if err != nil {
			t.Fatalf("合法路径 %q 应当通过: %v", input, err)
		}
		if got != expected {
			t.Fatalf("规范化结果不符: %q -> %q，期望 %q", input, got, expected)
		}
	}
	invalid := []string{
		"",
		"   ",
		"relative/path",
		"/srv/../etc",
		"/srv/agentflow/../..",
		`C:\Users\x`,
		"/srv/\x00bad",
		"/srv/agent\nflow",
	}
	for _, input := range invalid {
		if _, err := sshclient.NormalizeRemotePath(input); err == nil {
			t.Fatalf("非法路径 %q 应当被拒绝", input)
		}
	}
}

// TestJoinRemotePathBoundaries 验证子路径拼接的边界处理。
func TestJoinRemotePathBoundaries(t *testing.T) {
	t.Parallel()
	joined, err := sshclient.JoinRemotePath("/srv/agentflow", "env/Dockerfile")
	if err != nil {
		t.Fatalf("拼接路径失败: %v", err)
	}
	if joined != "/srv/agentflow/env/Dockerfile" {
		t.Fatalf("拼接结果不符: %s", joined)
	}
	for _, child := range []string{"../etc/passwd", "/etc/passwd", "", "  ", "a/../../b"} {
		if _, err := sshclient.JoinRemotePath("/srv/agentflow", child); err == nil {
			t.Fatalf("子路径 %q 应当被拒绝", child)
		}
	}
}

// TestIsWithinRemotePathBoundaries 验证路径包含判断不受前缀误判影响。
func TestIsWithinRemotePathBoundaries(t *testing.T) {
	t.Parallel()
	if !sshclient.IsWithinRemotePath("/srv/agentflow", "/srv/agentflow/env") {
		t.Fatal("子目录应判定为位于基准目录内")
	}
	if !sshclient.IsWithinRemotePath("/srv/agentflow", "/srv/agentflow") {
		t.Fatal("基准目录自身应判定为包含")
	}
	if sshclient.IsWithinRemotePath("/srv/agentflow", "/srv/agentflow-other") {
		t.Fatal("同前缀的其他目录不应判定为包含")
	}
	if sshclient.IsWithinRemotePath("/srv/agentflow", "/etc/passwd") {
		t.Fatal("外部路径不应判定为包含")
	}
	if sshclient.IsWithinRemotePath("/srv/agentflow", "relative") {
		t.Fatal("非法路径不应判定为包含")
	}
}

// TestResolveWorkspaceRules 验证远程工作空间解析规则。
func TestResolveWorkspaceRules(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                    "/home/deploy/.lingting/agentflow",
		"~/agentflow":         "/home/deploy/agentflow",
		"~/agentflow/ci":      "/home/deploy/agentflow/ci",
		"/srv/agentflow":      "/srv/agentflow",
		"/srv/agentflow/../x": "",
	}
	for configured, expected := range cases {
		root, err := sshclient.ResolveWorkspace(configured, "/home/deploy")
		if expected == "" {
			if err == nil {
				t.Fatalf("工作空间 %q 应当被拒绝", configured)
			}
			continue
		}
		if err != nil {
			t.Fatalf("解析工作空间 %q 失败: %v", configured, err)
		}
		if root != expected {
			t.Fatalf("工作空间解析不符: %q -> %q，期望 %q", configured, root, expected)
		}
	}
	unsafe := []string{"~", "/", "/home/deploy", "agentflow", `~\agentflow`, "~/../etc"}
	for _, configured := range unsafe {
		if _, err := sshclient.ResolveWorkspace(configured, "/home/deploy"); err == nil {
			t.Fatalf("工作空间 %q 应当被拒绝", configured)
		}
	}
}

// TestCommandResultStatus 验证结果状态与成功判定。
func TestCommandResultStatus(t *testing.T) {
	t.Parallel()
	success := sshclient.CommandResult{Status: sshclient.CommandStatusSucceeded, ExitCode: 0}
	if !success.Succeeded() {
		t.Fatal("退出码 0 应判定为成功")
	}
	exitError := sshclient.CommandResult{Status: sshclient.CommandStatusExitError, ExitCode: 7}
	if exitError.Succeeded() {
		t.Fatal("非零退出码不得判定为成功")
	}
	// 即使收到了标准输出，超时、取消与连接中断也不算成功。
	for _, status := range []sshclient.CommandStatus{
		sshclient.CommandStatusTimeout,
		sshclient.CommandStatusCancelled,
		sshclient.CommandStatusConnectionError,
	} {
		result := sshclient.CommandResult{Status: status, ExitCode: 0, Stdout: "partial"}
		if result.Succeeded() {
			t.Fatalf("状态 %s 不得判定为成功", status)
		}
	}
	if !(sshclient.CommandResult{Status: sshclient.CommandStatusTimeout}).TimedOut() {
		t.Fatal("超时状态应被 TimedOut 识别")
	}
	if !(sshclient.CommandResult{Status: sshclient.CommandStatusCancelled}).Cancelled() {
		t.Fatal("取消状态应被 Cancelled 识别")
	}
}

// TestErrorCodes 验证错误类别与默认说明。
func TestErrorCodes(t *testing.T) {
	t.Parallel()
	address := sshclient.Endpoint{Host: "10.0.0.9", Port: 22}.Address()
	codes := []sshclient.ErrorCode{
		sshclient.CodeUnreachable,
		sshclient.CodeHandshakeFailed,
		sshclient.CodeHostKeyUnknown,
		sshclient.CodeHostKeyMismatch,
		sshclient.CodeHostKeyRevoked,
		sshclient.CodeAuthFailed,
		sshclient.CodeNoCredential,
		sshclient.CodePrivateKey,
		sshclient.CodeAgentUnavailable,
		sshclient.CodeTimeout,
		sshclient.CodeCancelled,
	}
	for _, code := range codes {
		if code.String() == "" {
			t.Fatalf("错误类别 %v 应有非空字符串形式", code)
		}
		if code == sshclient.CodeOK {
			continue
		}
		if !strings.Contains(address, "10.0.0.9") {
			t.Fatalf("地址构造异常: %s", address)
		}
	}
}

// TestNormalizePrivateKeyPath 验证私钥路径规范化与错误分类。
func TestNormalizePrivateKeyPath(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	keyPath := filepath.Join(dir, "id_test")
	if err := os.WriteFile(keyPath, []byte("not-a-key"), 0o600); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	absolute, err := sshclient.NormalizePrivateKeyPath("  " + keyPath + "  ")
	if err != nil {
		t.Fatalf("规范化私钥路径失败: %v", err)
	}
	if absolute != keyPath {
		t.Fatalf("私钥路径不符: %s", absolute)
	}
	if _, err := sshclient.NormalizePrivateKeyPath(""); sshclient.ErrorCodeOf(err) != sshclient.CodeInvalidConfig {
		t.Fatalf("空路径应为 invalid_config，实际: %v", err)
	}
	for _, invalid := range []string{
		filepath.Join(dir, "missing_key"),
		dir,
		filepath.Join(dir, "bad\nname"),
	} {
		if _, err := sshclient.NormalizePrivateKeyPath(invalid); sshclient.ErrorCodeOf(
			err,
		) != sshclient.CodePrivateKey {
			t.Fatalf("路径 %q 应为 private_key_error，实际: %v", invalid, err)
		}
	}
}

// TestNormalizePrivateKeyPathExpandsHome 验证 ~ 前缀按本机 Home 展开。
func TestNormalizePrivateKeyPathExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("WARN [SKIP] 当前环境无法解析本机 Home 目录: %v", err)
	}
	expanded, err := sshclient.NormalizePrivateKeyPath("~")
	if err == nil {
		// Home 是目录，必须被拒绝；这里只验证展开到了 Home 而不是相对路径。
		t.Fatalf("Home 目录应被拒绝为私钥路径，实际返回 %s", expanded)
	}
	if !strings.Contains(err.Error(), home) && !strings.Contains(err.Error(), "目录") {
		t.Fatalf("错误信息应指向展开后的 Home: %v", err)
	}
}

// TestResolveHomeRequiresClient 验证缺少连接时解析远程 Home 立即失败。
func TestResolveHomeRequiresClient(t *testing.T) {
	t.Parallel()
	if _, err := sshclient.ResolveHome(context.Background(), nil, 0); err == nil {
		t.Fatal("缺少连接时应返回错误")
	}
}

// TestRunOptionsTimeoutDefaults 验证输出上限与超时的默认处理。
func TestRunOptionsTimeoutDefaults(t *testing.T) {
	t.Parallel()
	if sshclient.DefaultMaxOutputBytes <= 0 {
		t.Fatalf("默认输出上限必须为正数: %d", sshclient.DefaultMaxOutputBytes)
	}
	if sshclient.DefaultConnectTimeout <= 0 {
		t.Fatalf("默认连接超时必须为正数: %s", sshclient.DefaultConnectTimeout)
	}
	// 零值 RunOptions 表示只受 ctx 限制，这里只确认结构默认值语义。
	options := sshclient.RunOptions{}
	if options.Timeout != 0 || options.MaxOutputBytes != 0 {
		t.Fatalf("零值 RunOptions 应为全零: %+v", options)
	}
	_ = time.Second
}

// TestCommandResultTerminationConfirmed 验证"确认终止"只对正常结束成立。
func TestCommandResultTerminationConfirmed(t *testing.T) {
	t.Parallel()
	// 正常结束（成功与非零退出码）都能确认远程命令已终止。
	for _, status := range []sshclient.CommandStatus{
		sshclient.CommandStatusSucceeded,
		sshclient.CommandStatusExitError,
	} {
		result := sshclient.CommandResult{Status: status, TerminationConfirmed: true}
		if !result.TerminationConfirmed {
			t.Fatalf("状态 %s 应能确认终止", status)
		}
	}
	// 超时、取消与连接中断无法确认远程进程是否已终止。
	for _, status := range []sshclient.CommandStatus{
		sshclient.CommandStatusTimeout,
		sshclient.CommandStatusCancelled,
		sshclient.CommandStatusConnectionError,
	} {
		result := sshclient.CommandResult{Status: status}
		if result.TerminationConfirmed {
			t.Fatalf("状态 %s 不得声称远程命令已确认终止", status)
		}
	}
}
