package sshclient_test

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
)

// newAuthenticatedClient 启动测试服务并建立一条已认证连接。
func newAuthenticatedClient(t *testing.T) (*testServer, sshclient.Client) {
	t.Helper()
	server := newTestServer(t)
	host, port := server.endpoint()
	store := newStore(t, writeKnownHosts(t, host, port, server.hostKey.PublicKey()))
	dialer := newDialer(t, store)
	config, provider := passwordAuth(testPassword)
	result, err := dialer.Dial(testContext(t), sshclient.Endpoint{
		Host: host, Port: port, Username: testUser,
	}, config, provider)
	if err != nil {
		t.Fatalf("建立测试连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭测试连接失败: %v", closeErr)
		}
	})
	return server, result.Client
}

// TestRunSuccess 验证命令成功时分别返回标准输出与标准错误。
func TestRunSuccess(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	result, err := client.Run(testContext(t), "stream", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("退出码应为 0，实际: %d", result.ExitCode)
	}
	if result.Stdout != "out" {
		t.Fatalf("标准输出不符: %q", result.Stdout)
	}
	if result.Stderr != "err" {
		t.Fatalf("标准错误不符: %q", result.Stderr)
	}
	if !result.Succeeded() {
		t.Fatal("命令应当判定为成功")
	}
}

// TestRunNonZeroExit 验证非零退出码不被误报为成功。
func TestRunNonZeroExit(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	result, err := client.Run(testContext(t), "fail", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("非零退出码不应作为传输错误返回: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("退出码应为 7，实际: %d", result.ExitCode)
	}
	if result.Succeeded() {
		t.Fatal("非零退出码不应判定为成功")
	}
	if !strings.Contains(result.Stdout, "stdout-line") {
		t.Fatalf("标准输出不符: %q", result.Stdout)
	}
	if !strings.Contains(result.Stderr, "stderr-line") {
		t.Fatalf("标准错误不符: %q", result.Stderr)
	}
}

// TestRunOutputTruncated 验证输出上限生效且不会阻塞命令。
func TestRunOutputTruncated(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	result, err := client.Run(testContext(t), "big", sshclient.RunOptions{MaxOutputBytes: 128})
	if err != nil {
		t.Fatalf("执行命令失败: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("退出码应为 0，实际: %d", result.ExitCode)
	}
	if len(result.Stdout) != 128 {
		t.Fatalf("输出应被截断到 128 字节，实际: %d", len(result.Stdout))
	}
	if !result.StdoutTruncated {
		t.Fatal("应当标记输出被截断")
	}
}

// TestRunTimeout 验证超时会中断远程命令并返回错误。
func TestRunTimeout(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Run(ctx, "slow", sshclient.RunOptions{})
	if err == nil {
		t.Fatal("超时应当返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应包装上下文超时，实际: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("超时应当及时返回，实际耗时: %s", elapsed)
	}
}

// TestRunCancel 验证取消会中断远程命令。
func TestRunCancel(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := client.Run(ctx, "slow", sshclient.RunOptions{})
	if err == nil {
		t.Fatal("取消应当返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误应包装上下文取消，实际: %v", err)
	}
}

// TestRunOnClosedClient 验证关闭后执行命令返回明确错误。
func TestRunOnClosedClient(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	if err := client.Close(); err != nil {
		t.Fatalf("关闭连接失败: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("重复关闭应当返回 nil: %v", err)
	}
	if _, err := client.Run(testContext(t), "stream", sshclient.RunOptions{}); err == nil {
		t.Fatal("已关闭连接执行命令应当返回错误")
	}
	if _, err := client.SFTP(); err == nil {
		t.Fatal("已关闭连接打开 SFTP 应当返回错误")
	}
}

// TestShellQuote 验证参数转义不会改变命令语义。
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"simple":        "simple",
		"with space":    "'with space'",
		"quote'inside":  `'quote'\''inside'`,
		"a;rm -rf /tmp": "'a;rm -rf /tmp'",
		"":              "''",
		"$HOME":         "'$HOME'",
	}
	for input, expected := range cases {
		if got := sshclient.ShellQuote(input); got != expected {
			t.Fatalf("转义结果不符: %q -> %q，期望 %q", input, got, expected)
		}
	}
}

// TestShellCommandRejectsUnsafeName 验证命令名称不能拼接任意 shell 内容。
func TestShellCommandRejectsUnsafeName(t *testing.T) {
	if _, err := sshclient.ShellCommand("rm -rf /"); err == nil {
		t.Fatal("含空格与特殊字符的命令名称应当被拒绝")
	}
	if _, err := sshclient.ShellCommand(""); err == nil {
		t.Fatal("空命令名称应当被拒绝")
	}
	command, err := sshclient.ShellCommand("test", "-w", "/srv/agent flow")
	if err != nil {
		t.Fatalf("构造命令失败: %v", err)
	}
	if command != "test -w '/srv/agent flow'" {
		t.Fatalf("命令构造不符: %s", command)
	}
}

// TestNormalizeRemotePath 验证远程路径规范化与目录穿越防护。
func TestNormalizeRemotePath(t *testing.T) {
	valid := map[string]string{
		"/srv/agentflow":      "/srv/agentflow",
		"/srv/agentflow/":     "/srv/agentflow",
		"/srv//agentflow/./x": "/srv/agentflow/x",
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
		"  ",
		"relative/path",
		"/srv/../etc",
		`C:\Users\x`,
		"/srv/\x00bad",
	}
	for _, input := range invalid {
		if _, err := sshclient.NormalizeRemotePath(input); err == nil {
			t.Fatalf("非法路径 %q 应当被拒绝", input)
		}
	}
}

// TestJoinRemotePath 验证子路径拼接的边界处理。
func TestJoinRemotePath(t *testing.T) {
	joined, err := sshclient.JoinRemotePath("/srv/agentflow", "env/Dockerfile")
	if err != nil {
		t.Fatalf("拼接路径失败: %v", err)
	}
	if joined != "/srv/agentflow/env/Dockerfile" {
		t.Fatalf("拼接结果不符: %s", joined)
	}
	if _, err := sshclient.JoinRemotePath("/srv/agentflow", "../etc/passwd"); err == nil {
		t.Fatal("包含 .. 的子路径应当被拒绝")
	}
	if _, err := sshclient.JoinRemotePath("/srv/agentflow", "/etc/passwd"); err == nil {
		t.Fatal("绝对子路径应当被拒绝")
	}
}

// TestIsWithinRemotePath 验证路径包含判断。
func TestIsWithinRemotePath(t *testing.T) {
	if !sshclient.IsWithinRemotePath("/srv/agentflow", "/srv/agentflow/env") {
		t.Fatal("子目录应当判定为位于基准目录内")
	}
	if !sshclient.IsWithinRemotePath("/srv/agentflow", "/srv/agentflow") {
		t.Fatal("基准目录自身应当判定为包含")
	}
	if sshclient.IsWithinRemotePath("/srv/agentflow", "/srv/agentflow-other") {
		t.Fatal("同前缀的其他目录不应判定为包含")
	}
}

// TestSFTPUploadAndDownload 验证上传下载往返后内容一致。
func TestSFTPUploadAndDownload(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := t.TempDir()
	localSource := filepath.Join(dir, "source.txt")
	content := "agentflow sftp 内容\n第二行\n"
	if err := os.WriteFile(localSource, []byte(content), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), localSource, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	info, err := transfer.Stat(testContext(t), remotePath)
	if err != nil {
		t.Fatalf("读取远程属性失败: %v", err)
	}
	if info.IsDir || !info.IsRegular {
		t.Fatalf("远程目标应为普通文件: %+v", info)
	}
	if info.Size != int64(len(content)) {
		t.Fatalf("远程文件大小不符: %d", info.Size)
	}
	localTarget := filepath.Join(dir, "download.txt")
	if err := transfer.Download(testContext(t), remotePath, localTarget, sshclient.DownloadOptions{}); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	data, err := os.ReadFile(localTarget)
	if err != nil {
		t.Fatalf("读取下载文件失败: %v", err)
	}
	if string(data) != content {
		t.Fatalf("下载内容不符: %q", string(data))
	}
}

// TestSFTPUploadRefusesOverwrite 验证未声明覆盖时不会覆盖已有目标。
func TestSFTPUploadRefusesOverwrite(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("首次上传失败: %v", err)
	}
	err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{})
	if !errors.Is(err, sshclient.ErrRemotePathExists) {
		t.Fatalf("重复上传应当返回 ErrRemotePathExists，实际: %v", err)
	}
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		Overwrite: true,
	}); err != nil {
		t.Fatalf("显式覆盖上传应当成功: %v", err)
	}
}

// TestSFTPUploadLeavesNoTempFile 验证上传成功后不残留临时文件。
func TestSFTPUploadLeavesNoTempFile(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	entries, err := transfer.ReadDir(testContext(t), path.Dir(remotePath))
	if err != nil {
		t.Fatalf("列出远程目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, ".tmp") {
			t.Fatalf("上传后不应残留临时文件: %s", entry.Name)
		}
	}
}

// TestSFTPUploadMissingLocalFile 验证本机文件不存在时返回明确错误。
func TestSFTPUploadMissingLocalFile(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	err := transfer.Upload(
		testContext(t),
		filepath.Join(t.TempDir(), "missing.txt"),
		tempRemotePath(t),
		sshclient.UploadOptions{},
	)
	if err == nil {
		t.Fatal("本机文件不存在时应当失败")
	}
	if !strings.Contains(err.Error(), "读取本机文件失败") {
		t.Fatalf("错误信息应给出操作阶段，实际: %v", err)
	}
}

// TestSFTPMkdirStatReadDirRenameRemove 验证目录与文件管理操作。
func TestSFTPMkdirStatReadDirRenameRemove(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := path.Join(tempRemoteDir(t), "agentflow-dir")
	if err := transfer.MkdirAll(testContext(t), dir); err != nil {
		t.Fatalf("创建远程目录失败: %v", err)
	}
	info, err := transfer.Stat(testContext(t), dir)
	if err != nil {
		t.Fatalf("读取目录属性失败: %v", err)
	}
	if !info.IsDir {
		t.Fatalf("目标应为目录: %+v", info)
	}
	localSource := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(localSource, []byte("x"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	target := path.Join(dir, "file.txt")
	if err := transfer.Upload(testContext(t), localSource, target, sshclient.UploadOptions{}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	entries, err := transfer.ReadDir(testContext(t), dir)
	if err != nil {
		t.Fatalf("列出目录失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "file.txt" {
		t.Fatalf("目录内容不符: %+v", entries)
	}
	renamed := path.Join(dir, "renamed.txt")
	if err := transfer.Rename(testContext(t), target, renamed); err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	if _, err := transfer.Stat(testContext(t), target); err == nil {
		t.Fatal("重命名后原路径不应存在")
	}
	if err := transfer.Remove(testContext(t), renamed); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	if _, err := transfer.Stat(testContext(t), renamed); err == nil {
		t.Fatal("删除后文件不应存在")
	}
}

// TestSFTPRemoveRejectsDirectory 验证删除文件接口不会删除目录。
func TestSFTPRemoveRejectsDirectory(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := path.Join(tempRemoteDir(t), "agentflow-keep")
	if err := transfer.MkdirAll(testContext(t), dir); err != nil {
		t.Fatalf("创建远程目录失败: %v", err)
	}
	if err := transfer.Remove(testContext(t), dir); err == nil {
		t.Fatal("删除目录应当被拒绝")
	}
}

// TestSFTPDownloadRefusesOverwrite 验证未声明覆盖时不会覆盖本机文件。
func TestSFTPDownloadRefusesOverwrite(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("existing"), 0o600); err != nil {
		t.Fatalf("写入本机目标失败: %v", err)
	}
	err := transfer.Download(testContext(t), remotePath, target, sshclient.DownloadOptions{})
	if !errors.Is(err, sshclient.ErrLocalPathExists) {
		t.Fatalf("应当返回 ErrLocalPathExists，实际: %v", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("读取本机目标失败: %v", readErr)
	}
	if string(data) != "existing" {
		t.Fatalf("本机文件不应被覆盖，实际: %q", string(data))
	}
}

// TestSFTPDownloadCancel 验证取消下载不会留下最终文件。
func TestSFTPDownloadCancel(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte(strings.Repeat("y", 32*1024)), 0o600); err != nil {
		t.Fatalf("写入本机测试文件失败: %v", err)
	}
	remotePath := tempRemotePath(t)
	if err := transfer.Upload(testContext(t), source, remotePath, sshclient.UploadOptions{
		CreateParent: true,
	}); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	target := filepath.Join(dir, "cancelled.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := transfer.Download(ctx, remotePath, target, sshclient.DownloadOptions{})
	if err == nil {
		t.Fatal("已取消的上下文应当返回错误")
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("取消后不应留下最终文件")
	}
}

// TestSFTPClosedTransfer 验证关闭后各操作返回明确错误。
func TestSFTPClosedTransfer(t *testing.T) {
	_, client := newAuthenticatedClient(t)
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	if err := transfer.Close(); err != nil {
		t.Fatalf("关闭 SFTP 通道失败: %v", err)
	}
	if err := transfer.Close(); err != nil {
		t.Fatalf("重复关闭应当返回 nil: %v", err)
	}
	if _, err := transfer.Stat(testContext(t), "/tmp"); err == nil {
		t.Fatal("已关闭通道上的操作应当返回错误")
	}
}

// openTransfer 打开一条 SFTP 通道并在测试结束时关闭。
func openTransfer(t *testing.T, client sshclient.Client) sshclient.FileTransfer {
	t.Helper()
	transfer, err := client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Errorf("关闭 SFTP 通道失败: %v", closeErr)
		}
	})
	return transfer
}

// tempRemoteDir 生成一个临时远程目录路径。
func tempRemoteDir(t *testing.T) string {
	t.Helper()
	return toRemotePath(t.TempDir())
}

// tempRemotePath 生成一个临时远程文件路径。
func tempRemotePath(t *testing.T) string {
	t.Helper()
	return path.Join(tempRemoteDir(t), "remote.txt")
}

// toRemotePath 把本机临时目录转换为测试 SFTP 服务可识别的远程路径。
//
// 测试服务运行在本机，因此把本机绝对路径转写为 /<绝对路径> 形式：
// pkg/sftp 在 Windows 上会把它还原为同样的本机路径，从而让测试覆盖真实的
// SFTP 协议交互，而不引入与平台相关的分支逻辑。
func toRemotePath(localPath string) string {
	slashed := filepath.ToSlash(localPath)
	if strings.HasPrefix(slashed, "/") {
		return slashed
	}
	return "/" + slashed
}
