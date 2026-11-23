// 本文件是 tests/integration/local/ssh/transfer_test.go 中 10 个用例的迁移结果。
//
// 改造点：函数头改成 func run*、删除 t.Parallel()、目录取用例工作空间；断言保持原样。
package ssh

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
)

// 登记传输类 10 条用例，顺序与源文件一致。
var (
	_ = register("run-success", runRunSuccess)
	_ = register("run-non-zero-exit", runRunNonZeroExit)
	_ = register("run-timeout", runRunTimeout)
	_ = register("run-cancel", runRunCancel)
	_ = register("shell-quote", runShellQuote)
	_ = register("normalize-remote-path", runNormalizeRemotePath)
	_ = register("sftp-upload-and-download", runSFTPUploadAndDownload)
	_ = register("sftp-upload-refuses-overwrite", runSFTPUploadRefusesOverwrite)
	_ = register("sftp-mkdir-stat-read-dir-rename-remove", runSFTPMkdirStatReadDirRenameRemove)
	_ = register("sftp-download-refuses-overwrite", runSFTPDownloadRefusesOverwrite)
)

// 原 TestRunSuccess 验证命令成功时分别返回标准输出与标准错误。
func runRunSuccess(t *testrunner.Context) {
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

// 原 TestRunNonZeroExit 验证非零退出码不被误报为成功。
func runRunNonZeroExit(t *testrunner.Context) {
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

// 原 TestRunTimeout 验证超时会中断远程命令并返回错误。
func runRunTimeout(t *testrunner.Context) {
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

// 原 TestRunCancel 验证取消会中断远程命令。
func runRunCancel(t *testrunner.Context) {
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

// 原 TestShellQuote 验证参数转义不会改变命令语义。
func runShellQuote(t *testrunner.Context) {
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

// 原 TestNormalizeRemotePath 验证远程路径规范化与目录穿越防护。
func runNormalizeRemotePath(t *testrunner.Context) {
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

// 原 TestSFTPUploadAndDownload 验证上传下载往返后内容一致。
func runSFTPUploadAndDownload(t *testrunner.Context) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := caseWorkspace(t).Dir("files")
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

// 原 TestSFTPUploadRefusesOverwrite 验证未声明覆盖时不会覆盖已有目标。
func runSFTPUploadRefusesOverwrite(t *testrunner.Context) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := caseWorkspace(t).Dir("files")
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

// 原 TestSFTPMkdirStatReadDirRenameRemove 验证目录与文件管理操作。
func runSFTPMkdirStatReadDirRenameRemove(t *testrunner.Context) {
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
	localSource := filepath.Join(caseWorkspace(t).Dir("files"), "file.txt")
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

// 原 TestSFTPDownloadRefusesOverwrite 验证未声明覆盖时不会覆盖本机文件。
func runSFTPDownloadRefusesOverwrite(t *testrunner.Context) {
	_, client := newAuthenticatedClient(t)
	transfer := openTransfer(t, client)
	dir := caseWorkspace(t).Dir("files")
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
