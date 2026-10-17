package cli_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/tests/internal/testkit"
	"golang.org/x/crypto/ssh"
)

// hostConfig 生成一份指向临时数据库的执行节点测试配置。
func hostConfig(t *testing.T) string {
	t.Helper()
	dir := testkit.Dir(t)
	content := strings.ReplaceAll(
		minimalConfig,
		".agentflow/agentflow.db",
		filepath.ToSlash(filepath.Join(dir, "agentflow.db")),
	)
	return writeConfig(t, content)
}

// TestHostHelp 校验 host 命令组暴露全部子命令。
func TestHostHelp(t *testing.T) {
	t.Parallel()
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

// TestHostAddRequiresFlags 校验新增节点要求显式指定主机与用户名。
func TestHostAddRequiresFlags(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
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

// TestHostLifecycle 校验新增、列出、查看、设默认与删除的完整流程。
func TestHostLifecycle(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	code, stdout, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-a",
		"--name", "构建节点",
		"--host", "10.0.0.11",
		"--user", "deploy",
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

// TestHostAddRejectsInvalidInput 校验非法参数不会写入数据库。
func TestHostAddRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	cases := map[string][]string{
		"端口超出范围":   {"--host", "10.0.0.11", "--user", "deploy", "--port", "70000"},
		"工作空间相对路径": {"--host", "10.0.0.11", "--user", "deploy", "--workspace", "agentflow"},
		"主机地址非法":   {"--host", "10.0.0.11:22", "--user", "deploy"},
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

// TestHostSetDefaultRejectsUnknownID 校验设置默认节点时节点不存在。
func TestHostSetDefaultRejectsUnknownID(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	code, _, stderr := run(t, "host", "set-default", "missing", "--config", configPath)
	if code == 0 {
		t.Fatal("不存在的节点应返回非零退出码")
	}
	if !strings.Contains(stderr, "记录不存在") {
		t.Fatalf("错误信息应说明记录不存在，实际: %s", stderr)
	}
}

// startProbeServer 启动一个返回固定协议标识的本地 TCP 服务。
func startProbeServer(t *testing.T, banner string) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
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
				if _, err := conn.Write([]byte(banner)); err != nil {
					return
				}
				<-time.After(200 * time.Millisecond)
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	return address.IP.String(), address.Port
}

// TestHostProbeDetectsSSH 校验探测成功时明确说明尚未认证。
func TestHostProbeDetectsSSH(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	host, port := startProbeServer(t, "SSH-2.0-OpenSSH_9.6\r\n")
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-probe",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
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

// TestHostProbeNotSSH 校验非 SSH 服务返回非零退出码与明确说明。
func TestHostProbeNotSSH(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	host, port := startProbeServer(t, "HTTP/1.1 400 Bad Request\r\n\r\n")
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-http",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
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

// TestHostProbeUnreachable 校验不可达地址的探测结果。
func TestHostProbeUnreachable(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试监听失败: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	port := address.Port
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("关闭测试监听失败: %v", closeErr)
	}
	addArgs := []string{
		"host", "add",
		"--config", configPath,
		"--id", "node-down",
		"--host", address.IP.String(),
		"--port", fmt.Sprint(port),
		"--user", "deploy",
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

// TestHostAddWithPrivateKeyAuth 校验私钥认证参数被保存。
func TestHostAddWithPrivateKeyAuth(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
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

// TestHostAddRejectsPrivateKeyWithoutKey 校验私钥认证必须给出私钥路径。
func TestHostAddRejectsPrivateKeyWithoutKey(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
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

// TestHostAddRejectsUnknownAuthMethod 校验未知认证方式被拒绝。
func TestHostAddRejectsUnknownAuthMethod(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
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

// TestHostUpdateChangesAuthMethod 校验更新命令可以切换认证方式。
func TestHostUpdateChangesAuthMethod(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-update",
		"--host", "10.0.0.24",
		"--user", "deploy",
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, stdout, stderr := run(
		t,
		"host", "update", "node-update",
		"--config", configPath,
		"--auth", "password",
		"--workspace", "/srv/agentflow",
	)
	if code != 0 {
		t.Fatalf("更新节点应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "认证方式: password") {
		t.Fatalf("更新输出不符: %s", stdout)
	}
	code, stdout, stderr = run(t, "host", "show", "node-update", "--config", configPath)
	if code != 0 {
		t.Fatalf("查看节点应成功，错误: %s", stderr)
	}
	if !strings.Contains(stdout, "认证方式: password") || !strings.Contains(stdout, "/srv/agentflow") {
		t.Fatalf("更新未生效: %s", stdout)
	}
	if !strings.Contains(stdout, "10.0.0.24") {
		t.Fatalf("未指定的字段不应被修改: %s", stdout)
	}
}

// TestHostTestFailsWithoutCredentialInNonInteractive 校验非交互环境缺少凭据时立即失败。
func TestHostTestFailsWithoutCredentialInNonInteractive(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-pwd",
		"--host", "127.0.0.1",
		"--port", "1",
		"--user", "deploy",
		"--auth", "password",
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, _, stderr := run(t, "host", "test", "node-pwd", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少凭据时应返回非零退出码")
	}
	if !strings.Contains(stderr, "没有可用的凭据") {
		t.Fatalf("错误信息应说明缺少凭据，实际: %s", stderr)
	}
	if strings.Contains(stderr, "密码") && strings.Contains(stderr, "请输入") {
		t.Fatalf("非交互环境不应挂起等待输入: %s", stderr)
	}
}

// TestHostTestReportsUnreachable 校验连接测试报告 TCP 不可达。
func TestHostTestReportsUnreachable(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试监听失败: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	port := address.Port
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("关闭测试监听失败: %v", closeErr)
	}
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-down-ssh",
		"--host", address.IP.String(),
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

// TestHostTransferRequiresRemoteAbsolutePath 校验远程路径必须是绝对路径。
func TestHostTransferRequiresRemoteAbsolutePath(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-move",
		"--host", "127.0.0.1",
		"--port", "1",
		"--user", "deploy",
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

// writeTestPrivateKey 生成一个测试用未加密私钥并返回其路径。
func writeTestPrivateKey(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(privateKey, "agentflow-test")
	if err != nil {
		t.Fatalf("编码测试私钥失败: %v", err)
	}
	path := filepath.Join(testkit.Dir(t), "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("写入测试私钥失败: %v", err)
	}
	return path
}

// TestHostTrustRequiresVerifiedFingerprint 校验非交互环境不会自动信任未知主机密钥。
func TestHostTrustRequiresVerifiedFingerprint(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	host, port := startSSHHandshakeServer(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-trust",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	// 未给出 --fingerprint 与 --yes 时必须拒绝写入信任记录。
	code, _, stderr := run(t, "host", "trust", "node-trust", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少核验指纹时不得写入信任记录")
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
		t.Fatal("指纹不一致时不得写入信任记录")
	}
	if !strings.Contains(stderr, "探测主机密钥失败") && !strings.Contains(stderr, "指纹") {
		t.Fatalf("错误信息应说明探测或指纹问题，实际: %s", stderr)
	}
}

// TestHostWorkspaceRequiresCredentials 校验非交互环境缺少凭据时立即失败。
func TestHostWorkspaceRequiresCredentials(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-workspace",
		"--host", "127.0.0.1",
		"--port", "1",
		"--user", "deploy",
		"--auth", "password",
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	code, _, stderr := run(t, "host", "workspace", "node-workspace", "--config", configPath)
	if code == 0 {
		t.Fatal("缺少凭据时应返回非零退出码")
	}
	if !strings.Contains(stderr, "没有可用的凭据") {
		t.Fatalf("错误信息应说明缺少凭据，实际: %s", stderr)
	}
}

// TestHostDownloadAcceptsMaxBytesFlag 校验下载命令暴露大小上限参数。
func TestHostDownloadAcceptsMaxBytesFlag(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, "host", "transfer", "download", "--help")
	if code != 0 {
		t.Fatalf("download --help 应成功，实际退出码 %d，错误: %s", code, stderr)
	}
	if !strings.Contains(stdout, "--max-bytes") {
		t.Fatalf("帮助输出应包含 --max-bytes，实际: %s", stdout)
	}
}

// startSSHHandshakeServer 启动一个只完成 SSH 握手的最小服务端。
//
// 它用于验证主机密钥探测与信任流程：服务端提供真实的主机密钥，但不接受任何
// 身份认证，因此客户端只会取得主机密钥指纹而不会建立已认证连接。
func startSSHHandshakeServer(t *testing.T) (string, int) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("构造主机密钥签名器失败: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, errors.New("拒绝认证")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试 SSH 服务失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Logf("关闭测试 SSH 监听失败: %v", closeErr)
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
				_, _, _, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					return
				}
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	return address.IP.String(), address.Port
}

// TestHostTrustRecordsVerifiedFingerprint 校验核验指纹后可以写入信任记录。
func TestHostTrustRecordsVerifiedFingerprint(t *testing.T) {
	t.Parallel()
	configPath := hostConfig(t)
	host, port := startSSHHandshakeServer(t)
	if code, _, stderr := run(
		t,
		"host", "add",
		"--config", configPath,
		"--id", "node-trust-ok",
		"--host", host,
		"--port", fmt.Sprint(port),
		"--user", "deploy",
	); code != 0 {
		t.Fatalf("新增节点应成功，错误: %s", stderr)
	}
	// 先探测取得服务端实际指纹。
	code, stdout, stderr := run(t, "host", "trust", "node-trust-ok", "--config", configPath)
	if code == 0 {
		t.Fatal("未确认时不得写入信任记录")
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
		t.Fatalf("核验指纹后应写入信任记录，实际退出码 %d，stdout: %s，stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "已记录主机密钥信任") {
		t.Fatalf("输出应说明信任已记录，实际: %s", stdout)
	}
	// 再次写入应提示已受信任而不是重复追加。
	code, _, stderr = run(
		t,
		"host", "trust", "node-trust-ok",
		"--config", configPath,
		"--fingerprint", fingerprint,
		"--yes",
	)
	if code == 0 {
		t.Fatal("已受信任的主机密钥不应重复写入")
	}
	if !strings.Contains(stderr, "已在信任记录中") {
		t.Fatalf("错误信息应说明已受信任，实际: %s", stderr)
	}
}

// extractFingerprint 从输出中提取 SHA-256 指纹。
func extractFingerprint(t *testing.T, output string) string {
	t.Helper()
	const prefix = "SHA256:"
	index := strings.Index(output, prefix)
	if index < 0 {
		t.Fatalf("输出中找不到指纹: %s", output)
	}
	rest := output[index:]
	end := strings.IndexAny(rest, " \r\n\t")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
