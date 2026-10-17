//go:build integration

// 本文件提供 opt-in 的真实远程连接测试。
//
// 默认的 go test ./... 不会编译或执行本文件；只有在显式启用 integration 构建标签
// 并给出目标机器环境变量时才会运行。它用于在真实 Linux 机器上验证主机密钥校验、
// 身份认证、远程命令执行与 SFTP 传输。
//
// 用法：
//
//	AGENTFLOW_SSH_HOST=10.0.0.10 \
//	AGENTFLOW_SSH_USER=deploy \
//	AGENTFLOW_SSH_KEY=/home/me/.ssh/id_ed25519 \
//	AGENTFLOW_SSH_KNOWN_HOSTS=/home/me/.ssh/known_hosts \
//	go test -tags integration ./internal/sshclient/ -run TestRealRemote -v
package sshclient_test

import (
	"context"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
)

// realRemoteEnv 汇总真实远程测试所需的环境变量名。
const (
	envHost       = "AGENTFLOW_SSH_HOST"
	envPort       = "AGENTFLOW_SSH_PORT"
	envUser       = "AGENTFLOW_SSH_USER"
	envKey        = "AGENTFLOW_SSH_KEY"
	envKnownHosts = "AGENTFLOW_SSH_KNOWN_HOSTS"
	envStore      = "AGENTFLOW_SSH_STORE"
	envWorkspace  = "AGENTFLOW_SSH_WORKSPACE"
)

// TestRealRemoteSSH 在真实远程机器上验证认证、命令执行与 SFTP 传输。
func TestRealRemoteSSH(t *testing.T) {
	host := os.Getenv(envHost)
	if host == "" {
		t.Skipf("未设置 %s，跳过真实远程连接测试", envHost)
	}
	user := os.Getenv(envUser)
	if user == "" {
		t.Fatalf("设置 %s 时必须同时设置 %s", envHost, envUser)
	}
	port := 22
	if raw := os.Getenv(envPort); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", envPort, err)
		}
		port = parsed
	}
	storePath := os.Getenv(envStore)
	if storePath == "" {
		storePath = path.Join(os.TempDir(), "agentflow_integration_known_hosts")
	}
	hostKeys, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
		TrustedPaths: []string{os.Getenv(envKnownHosts)},
		StorePath:    storePath,
	})
	if err != nil {
		t.Fatalf("构造主机密钥库失败: %v", err)
	}
	dialer, err := sshclient.NewDialer(sshclient.DialOptions{
		HostKeys:       hostKeys,
		ConnectTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造连接器失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	authConfig := sshclient.AuthConfig{
		Method:         sshclient.AuthMethodPrivateKey,
		PrivateKeyPath: os.Getenv(envKey),
	}
	result, err := dialer.Dial(ctx, sshclient.Endpoint{
		Host: host, Port: port, Username: user,
	}, authConfig, nil)
	if err != nil {
		if sshclient.ErrorCodeOf(err) == sshclient.CodeHostKeyUnknown {
			t.Fatalf(
				"主机密钥未知，请先核验指纹并写入 %s: %s %s",
				storePath,
				result.HostKey.Algorithm,
				result.HostKey.Fingerprint,
			)
		}
		t.Fatalf("连接真实远程机器失败: %v", err)
	}
	defer func() {
		if closeErr := result.Client.Close(); closeErr != nil {
			t.Errorf("关闭连接失败: %v", closeErr)
		}
	}()
	if result.HostKeyVerdict != sshclient.VerdictTrusted {
		t.Fatalf("主机密钥应已被信任，实际: %s", result.HostKeyVerdict)
	}
	runResult, err := result.Client.Run(ctx, "id -un", sshclient.RunOptions{})
	if err != nil {
		t.Fatalf("执行远程命令失败: %v", err)
	}
	if !runResult.Succeeded() {
		t.Fatalf("远程命令应以 0 退出，实际: %d: %s", runResult.ExitCode, runResult.Stderr)
	}
	home, err := sshclient.ResolveHome(ctx, result.Client, 30*time.Second)
	if err != nil {
		t.Fatalf("解析远程 Home 失败: %v", err)
	}
	configured := os.Getenv(envWorkspace)
	workspace, err := sshclient.EnsureWorkspace(ctx, result.Client, configured, true)
	if err != nil {
		t.Fatalf("检查远程工作空间失败: %v", err)
	}
	if !workspace.Writable {
		t.Fatalf("远程工作空间应可写: %+v", workspace)
	}
	transfer, err := result.Client.SFTP()
	if err != nil {
		t.Fatalf("打开 SFTP 通道失败: %v", err)
	}
	defer func() {
		if closeErr := transfer.Close(); closeErr != nil {
			t.Errorf("关闭 SFTP 通道失败: %v", closeErr)
		}
	}()
	remotePath := path.Join(workspace.Root, "integration-probe.txt")
	if err := transfer.Upload(ctx, os.Getenv(envKey), remotePath, sshclient.UploadOptions{
		Overwrite: true,
	}); err != nil {
		t.Fatalf("上传文件失败: %v", err)
	}
	info, err := transfer.Stat(ctx, remotePath)
	if err != nil {
		t.Fatalf("读取远程文件属性失败: %v", err)
	}
	if info.IsDir {
		t.Fatalf("远程目标应为文件: %+v", info)
	}
	if err := transfer.Remove(ctx, remotePath); err != nil {
		t.Fatalf("删除远程文件失败: %v", err)
	}
	t.Logf("真实远程验证通过: home=%s workspace=%s", home, workspace.Root)
}
