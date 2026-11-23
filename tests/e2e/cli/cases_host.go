// 本文件是 tests/e2e/cli/host_test.go 中 19 个用例的迁移结果。
//
// 除两个帮助类用例外，其余用例都会打开数据库，因此声明一份用例独占的 sqlite-db。用例里
// 的"SSH 服务"都是用例自建的本机 TCP 服务，不需要真实主机；源用例里重复出现的"申请端口后
// 立即关闭"改由 fixture_server.go 的 startUnreachableAddress 提供，断言保持不变。
package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记本包 19 条用例，顺序与源文件一致。
var (
	_ = register("host-help", runHostHelp)
	_ = register("host-add-requires-flags", runHostAddRequiresFlags, databaseDecl()...)
	_ = register("host-lifecycle", runHostLifecycle, databaseDecl()...)
	_ = register("host-add-rejects-invalid-input", runHostAddRejectsInvalidInput, databaseDecl()...)
	_ = register("host-set-default-rejects-unknown-id", runHostSetDefaultRejectsUnknownID, databaseDecl()...)
	_ = register("host-probe-detects-ssh", runHostProbeDetectsSSH, databaseDecl()...)
	_ = register("host-probe-not-ssh", runHostProbeNotSSH, databaseDecl()...)
	_ = register("host-probe-unreachable", runHostProbeUnreachable, databaseDecl()...)
	_ = register("host-add-with-private-key-auth", runHostAddWithPrivateKeyAuth, databaseDecl()...)
	_ = register("host-add-rejects-private-key-without-key", runHostAddRejectsPrivateKeyWithoutKey, databaseDecl()...)
	_ = register("host-add-rejects-unknown-auth-method", runHostAddRejectsUnknownAuthMethod, databaseDecl()...)
	_ = register("host-update-changes-auth-method", runHostUpdateChangesAuthMethod, databaseDecl()...)
	_ = register("host-test-fails-without-private-key-in-non-interactive", runHostTestFailsWithoutPrivateKeyInNonInteractive, databaseDecl()...)
	_ = register("host-test-reports-unreachable", runHostTestReportsUnreachable, databaseDecl()...)
	_ = register("host-transfer-requires-remote-absolute-path", runHostTransferRequiresRemoteAbsolutePath, databaseDecl()...)
	_ = register("host-trust-requires-verified-fingerprint", runHostTrustRequiresVerifiedFingerprint, databaseDecl()...)
	_ = register("host-workspace-requires-private-key", runHostWorkspaceRequiresPrivateKey, databaseDecl()...)
	_ = register("host-download-accepts-max-bytes-flag", runHostDownloadAcceptsMaxBytesFlag)
	_ = register("host-trust-records-verified-fingerprint", runHostTrustRecordsVerifiedFingerprint, databaseDecl()...)
)

// 原 TestHostHelp 校验 host 命令组暴露全部子命令。
func runHostHelp(t *testrunner.Context) {
	code, stdout, stderr := run(t, "host", "--help")
	if code != 0 {
		t.Fatalf("host --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{
		"add", "list", "show", "update", "set-default", "probe",
		"test", "trust", "workspace", "transfer", "remove",
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("帮助输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestHostAddRequiresFlags 校验新增节点要求显式指定主机与用户名。
func runHostAddRequiresFlags(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(t, "host", "add", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少必需参数时应返回非零退出码")
	}
	for _, flag := range []string{"host", "user"} {
		if !strings.Contains(stderr, flag) {
			t.Fatalf("错误信息应指出缺少 %s，实际: %s", flag, stderr)
		}
	}
}

// 原 TestHostLifecycle 校验新增、列出、查看、设默认与删除的完整流程。
func runHostLifecycle(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, stdout, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-a",
		"--name", "构建节点",
		"--host", "10.0.0.11",
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	)
	if code != 0 {
		t.Fatalf("新增节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"node-a", "构建节点", "10.0.0.11:22"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("新增输出缺少 %s，实际: %s", expected, stdout)
		}
	}
	if !strings.Contains(stdout, "~/.lingting/agentflow") {
		t.Fatalf("未指定 --workspace 时应使用默认路径，实际: %s", stdout)
	}

	code, stdout, stderr = run(t, "host", "list", "--config", configPath)
	if code != 0 {
		t.Fatalf("列出节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"node-a", "构建节点", "10.0.0.11:22", "deploy"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("列表输出缺少 %s，实际: %s", expected, stdout)
		}
	}

	code, stdout, stderr = run(t, "host", "show", "node-a", "--config", configPath)
	if code != 0 {
		t.Fatalf("查看节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "10.0.0.11") || !strings.Contains(stdout, "~/.lingting/agentflow") {
		t.Fatalf("详情输出不符: %s", stdout)
	}

	code, stdout, stderr = run(t, "host", "set-default", "node-a", "--config", configPath)
	if code != 0 {
		t.Fatalf("设置默认节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "默认执行节点已设置") {
		t.Fatalf("输出不符: %s", stdout)
	}

	code, stdout, stderr = run(t, "host", "remove", "node-a", "--config", configPath)
	if code != 0 {
		t.Fatalf("删除节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "执行节点已删除") {
		t.Fatalf("输出不符: %s", stdout)
	}

	code, _, stderr = run(t, "host", "show", "node-a", "--config", configPath)
	if code == 0 {
		t.Fatal("删除后查看应返回非零退出码")
	}
	if !strings.Contains(stderr, "记录不存在") {
		t.Fatalf("错误信息应说明记录不存在，实际: %s", stderr)
	}
}

// 原 TestHostAddRejectsInvalidInput 校验非法参数不会写入数据库。
func runHostAddRejectsInvalidInput(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	key := writeTestPrivateKey(t)
	cases := map[string][]string{
		"端口超出范围":   {"--host", "10.0.0.11", "--user", "deploy", "--port", "70000", "--key", key},
		"工作空间相对路径": {"--host", "10.0.0.11", "--user", "deploy", "--workspace", "agentflow", "--key", key},
		"主机地址非法":   {"--host", "10.0.0.11:22", "--user", "deploy", "--key", key},
	}
	for name, extra := range cases {
		args := append([]string{"host", "add", "--config", configPath}, extra...)
		if code, _, _ := run(t, args...); code == 0 {
			t.Fatalf("%s 应返回非零退出码", name)
		}
	}
	code, stdout, stderr := run(t, "host", "list", "--config", configPath)
	if code != 0 {
		t.Fatalf("列出节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "尚未配置执行节点") {
		t.Fatalf("非法输入不应写入节点，实际: %s", stdout)
	}
}

// 原 TestHostSetDefaultRejectsUnknownID 校验设置默认节点时节点不存在。
func runHostSetDefaultRejectsUnknownID(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(t, "host", "set-default", "missing", "--config", configPath)
	if code == 0 {
		t.Fatal("不存在的节点应返回非零退出码")
	}
	if !strings.Contains(stderr, "记录不存在") {
		t.Fatalf("错误信息应说明记录不存在，实际: %s", stderr)
	}
}

// 原 TestHostProbeDetectsSSH 校验探测成功时明确说明尚未认证。
func runHostProbeDetectsSSH(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	host, port := startProbeServer(t, "SSH-2.0-OpenSSH_9.6\r\n")
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-probe",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	}
	if code, _, stderr := run(t, addArgs...); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, stderr := run(t, "host", "probe", "node-probe", "--config", configPath)
	if code != 0 {
		t.Fatalf("探测应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"尚未执行身份认证", "ok"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("探测输出缺少 %s，实际: %s", expected, stdout)
		}
	}
	if strings.Contains(stdout, "登录成功") {
		t.Fatalf("探测输出不得声称登录成功: %s", stdout)
	}
}

// 原 TestHostProbeNotSSH 校验非 SSH 服务返回非零退出码与明确说明。
func runHostProbeNotSSH(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	host, port := startProbeServer(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-http",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	}
	if code, _, stderr := run(t, addArgs...); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, stderr := run(t, "host", "probe", "node-http", "--config", configPath)
	if code == 0 {
		t.Fatal("非 SSH 服务应返回非零退出码")
	}
	if !strings.Contains(stdout, "not_ssh") {
		t.Fatalf("输出应标明结果类别，实际: %s", stdout)
	}
	if !strings.Contains(stderr, "未识别到有效的 SSH 服务") {
		t.Fatalf("错误信息应说明未识别到 SSH 服务，实际: %s", stderr)
	}
}

// 原 TestHostProbeUnreachable 校验不可达地址的探测结果。
func runHostProbeUnreachable(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	host, port := startUnreachableAddress(t)
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-down",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	}
	if code, _, stderr := run(t, addArgs...); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, _ := run(t, "host", "probe", "node-down", "--config", configPath)
	if code == 0 {
		t.Fatal("不可达地址应返回非零退出码")
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Fatalf("输出应标明不可达，实际: %s", stdout)
	}
}

// 原 TestHostAddWithPrivateKeyAuth 校验私钥认证参数被保存。
func runHostAddWithPrivateKeyAuth(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, stdout, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-key",
		"--host", "10.0.0.21",
		"--user", "deploy",
		"--auth", "private_key",
		"--key", "/home/deploy/.ssh/id_ed25519",
	)
	if code != 0 {
		t.Fatalf("新增私钥节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "10.0.0.21:22") {
		t.Fatalf("输出不符: %s", stdout)
	}
	code, stdout, stderr = run(t, "host", "show", "node-key", "--config", configPath)
	if code != 0 {
		t.Fatalf("查看节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	for _, expected := range []string{"认证方式: private_key", "私钥文件: /home/deploy/.ssh/id_ed25519"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("详情输出缺少 %s，实际: %s", expected, stdout)
		}
	}
}

// 原 TestHostAddRejectsPrivateKeyWithoutKey 校验私钥认证必须给出私钥路径。
func runHostAddRejectsPrivateKeyWithoutKey(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--host", "10.0.0.22",
		"--user", "deploy",
		"--auth", "private_key",
	)
	if code == 0 {
		t.Fatal("缺少 --key 时应返回非零退出码")
	}
	if !strings.Contains(stderr, "private_key_path") && !strings.Contains(stderr, "私钥") {
		t.Fatalf("错误信息应说明缺少私钥路径，实际: %s", stderr)
	}
}

// 原 TestHostAddRejectsUnknownAuthMethod 校验未知认证方式被拒绝。
func runHostAddRejectsUnknownAuthMethod(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--host", "10.0.0.23",
		"--user", "deploy",
		"--auth", "kerberos",
	)
	if code == 0 {
		t.Fatal("未知认证方式应返回非零退出码")
	}
	if !strings.Contains(stderr, "认证方式") {
		t.Fatalf("错误信息应说明认证方式非法，实际: %s", stderr)
	}
}

// 原 TestHostUpdateChangesAuthMethod 校验更新命令可以修改连接参数与私钥路径。
func runHostUpdateChangesAuthMethod(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-update",
		"--host", "10.0.0.24",
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, stderr := run(
		t,
		"host", "update", "node-update",
		"--config", configPath,
		"--key", "/home/deploy/.ssh/id_ed25519",
		"--workspace", "/srv/agentflow",
	)
	if code != 0 {
		t.Fatalf("更新节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "认证方式: private_key") {
		t.Fatalf("更新输出不符: %s", stdout)
	}
	code, stdout, stderr = run(t, "host", "show", "node-update", "--config", configPath)
	if code != 0 {
		t.Fatalf("查看节点应成功，错误: %s", stderr)
	}
	if !strings.Contains(stdout, "私钥文件: /home/deploy/.ssh/id_ed25519") ||
		!strings.Contains(stdout, "/srv/agentflow") {
		t.Fatalf("更新未生效: %s", stdout)
	}
	if !strings.Contains(stdout, "10.0.0.24") {
		t.Fatalf("未指定的字段不应被修改: %s", stdout)
	}
}

// 原 TestHostTestFailsWithoutPrivateKeyInNonInteractive 校验私钥不可用时立即失败。
func runHostTestFailsWithoutPrivateKeyInNonInteractive(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	missingKey := filepath.Join(t.WorkspaceDir(), "missing_id_ed25519")
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-pwd",
		"--host", "127.0.0.1",
		"--port", "1",
		"--user", "deploy",
		"--key", missingKey,
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, _, stderr := run(t, "host", "test", "node-pwd", "--config", configPath)
	if code == 0 {
		t.Fatal("私钥不可用时应返回非零退出码")
	}
	if !strings.Contains(stderr, "私钥") {
		t.Fatalf("错误信息应说明私钥不可用，实际: %s", stderr)
	}
	if strings.Contains(stderr, "请输入") || strings.Contains(stderr, "口令") {
		t.Fatalf("非交互环境不应挂起等待输入: %s", stderr)
	}
}

// 原 TestHostTestReportsUnreachable 校验连接测试报告 TCP 不可达。
func runHostTestReportsUnreachable(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	host, port := startUnreachableAddress(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-down-ssh",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
		"--auth", "private_key",
		"--key", writeTestPrivateKey(t),
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, testStderr := run(t, "host", "test", "node-down-ssh", "--config", configPath)
	if code == 0 {
		t.Fatal("不可达地址应返回非零退出码")
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Fatalf("输出应标明 unreachable，实际 stdout: %q，stderr: %s", stdout, testStderr)
	}
	if strings.Contains(stdout, "SSH 认证成功") {
		t.Fatalf("不可达时不得声称认证成功: %s", stdout)
	}
}

// 原 TestHostTransferRequiresRemoteAbsolutePath 校验远程路径必须是绝对路径。
func runHostTransferRequiresRemoteAbsolutePath(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-move",
		"--host", "127.0.0.1",
		"--port", "1",
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, _, stderr := run(
		t,
		"host", "transfer", "upload", "node-move",
		"--config", configPath,
		"local.txt",
		"relative/remote.txt",
	)
	if code == 0 {
		t.Fatal("相对远程路径应返回非零退出码")
	}
	if !strings.Contains(stderr, "绝对路径") && !strings.Contains(stderr, "连接执行节点失败") {
		t.Fatalf("错误信息应说明路径或连接问题，实际: %s", stderr)
	}
}

// 原 TestHostTrustRequiresVerifiedFingerprint 校验非交互环境不会自动信任未知主机密钥。
func runHostTrustRequiresVerifiedFingerprint(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	host, port := startSSHHandshakeServer(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-trust",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	// 未给出 --fingerprint 与 --yes 时必须拒绝记录指纹。
	code, _, stderr := run(t, "host", "trust", "node-trust", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少核验指纹时不得记录指纹")
	}
	if !strings.Contains(stderr, "指纹") {
		t.Fatalf("错误信息应说明缺少核验指纹，实际: %s", stderr)
	}
	// 给出与真实服务端不符的指纹时同样必须拒绝。
	code, _, stderr = run(
		t,
		"host", "trust", "node-trust",
		"--config", configPath,
		"--fingerprint", "SHA256:not-the-real-fingerprint",
		"--yes",
	)
	if code == 0 {
		t.Fatal("指纹不一致时不得记录指纹")
	}
	if !strings.Contains(stderr, "探测主机密钥失败") && !strings.Contains(stderr, "指纹") {
		t.Fatalf("错误信息应说明探测或指纹问题，实际: %s", stderr)
	}
}

// 原 TestHostWorkspaceRequiresPrivateKey 校验私钥不可用时工作空间解析立即失败。
func runHostWorkspaceRequiresPrivateKey(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	missingKey := filepath.Join(t.WorkspaceDir(), "missing_id_ed25519")
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-workspace",
		"--host", "127.0.0.1",
		"--port", "1",
		"--user", "deploy",
		"--key", missingKey,
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, _, stderr := run(t, "host", "workspace", "node-workspace", "--config", configPath)
	if code == 0 {
		t.Fatal("私钥不可用时应返回非零退出码")
	}
	if !strings.Contains(stderr, "私钥") {
		t.Fatalf("错误信息应说明私钥不可用，实际: %s", stderr)
	}
}

// 原 TestHostDownloadAcceptsMaxBytesFlag 校验下载命令暴露大小上限参数。
func runHostDownloadAcceptsMaxBytesFlag(t *testrunner.Context) {
	code, stdout, stderr := run(t, "host", "transfer", "download", "--help")
	if code != 0 {
		t.Fatalf("download --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "--max-bytes") {
		t.Fatalf("帮助输出应包含 --max-bytes，实际: %s", stdout)
	}
}

// 原 TestHostTrustRecordsVerifiedFingerprint 校验核验指纹后可以记录指纹。
func runHostTrustRecordsVerifiedFingerprint(t *testrunner.Context) {
	configPath := workspaceConfig(t)
	host, port := startSSHHandshakeServer(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-trust-ok",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
		"--key", writeTestPrivateKey(t),
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	// 先探测取得服务端实际指纹。
	code, stdout, stderr := run(t, "host", "trust", "node-trust-ok", "--config", configPath)
	if code == 0 {
		t.Fatal("未确认时不得记录指纹")
	}
	if !strings.Contains(stdout, "SHA256:") {
		t.Fatalf("探测输出应展示 SHA-256 指纹，实际 stdout: %q，stderr: %s", stdout, stderr)
	}
	fingerprint := extractFingerprint(t, stdout)
	// 使用探测到的指纹显式确认后应当写入成功。
	code, stdout, stderr = run(
		t,
		"host", "trust", "node-trust-ok",
		"--config", configPath,
		"--fingerprint", fingerprint,
		"--yes",
	)
	if code != 0 {
		t.Fatalf("核验指纹后应记录指纹，实际退出码 %d，stdout: %s，stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "已记录主机密钥指纹") {
		t.Fatalf("输出应说明指纹已记录，实际: %s", stdout)
	}
	// AgentFlow 不写 known_hosts，必须提示用户自行用 OpenSSH 追加已核验的密钥。
	if !strings.Contains(stdout, "ssh-keyscan") {
		t.Fatalf("输出应提示用 OpenSSH 写入 known_hosts，实际: %s", stdout)
	}
	// 重复记录是幂等的：AgentFlow 不写 known_hosts，因此探测结论仍是未知，
	// 再次确认指纹只会重写同一个节点指纹，不报错也不产生第二条记录。
	code, _, stderr = run(
		t,
		"host", "trust", "node-trust-ok",
		"--config", configPath,
		"--fingerprint", fingerprint,
		"--yes",
	)
	if code != 0 {
		t.Fatalf("重复记录指纹应幂等成功，实际退出码 %d，stderr: %s", code, stderr)
	}
}
