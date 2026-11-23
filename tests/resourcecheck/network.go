package resourcecheck

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lingting/agentflow/internal/sshclient"
	"github.com/lingting/agentflow/internal/testrunner"
)

// LocalPortCase 验证 local-port 提供者：句柄给出的地址真实可连接。
var LocalPortCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck/local-port",
	Resources: []testrunner.ResourceDecl{{
		Name: "port", Type: testrunner.ResourceLocalPort, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("port")
		if !ok {
			c.Fatalf("缺少资源 port 的句柄")
		}
		address := handle.PayloadValue("address")
		dialer := net.Dialer{Timeout: 5 * time.Second}
		conn, err := dialer.DialContext(c.Context(), "tcp", address)
		if err != nil {
			c.Fatalf("端口 %s 不可连接: %v", address, err)
		}
		_ = conn.Close()
		c.Logf("端口资源可用：%s", address)
	},
})

// LocalHTTPCase 验证 local-http-server 提供者：句柄给出的 base_url 返回 200。
var LocalHTTPCase = testrunner.Register(testrunner.Case{
	ID: "resourcecheck/local-http-server",
	Resources: []testrunner.ResourceDecl{{
		Name: "http", Type: testrunner.ResourceLocalHTTPServer, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("http")
		if !ok {
			c.Fatalf("缺少资源 http 的句柄")
		}
		baseURL := handle.PayloadValue("base_url")
		client := &http.Client{Timeout: 5 * time.Second}
		request, err := http.NewRequestWithContext(c.Context(), http.MethodGet, baseURL, nil)
		if err != nil {
			c.Fatalf("构造请求失败: %v", err)
		}
		response, err := client.Do(request)
		if err != nil {
			c.Fatalf("请求 %s 失败: %v", baseURL, err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			c.Fatalf("%s 返回状态码 %d", baseURL, response.StatusCode)
		}
		c.Logf("HTTP 服务可用：%s", baseURL)
	},
})

// LocalSSHServerCase 验证 local-ssh-server 提供者：用句柄里的私钥与主机密钥真实登录。
var LocalSSHServerCase = testrunner.Register(testrunner.Case{
	ID:      "resourcecheck/local-ssh-server",
	Timeout: 60 * time.Second,
	Resources: []testrunner.ResourceDecl{{
		Name: "ssh", Type: testrunner.ResourceLocalSSHServer, Scope: testrunner.ScopeCaseExclusive,
	}},
	Run: func(c *testrunner.Context) {
		handle, ok := c.Resource("ssh")
		if !ok {
			c.Fatalf("缺少资源 ssh 的句柄")
		}
		knownHosts := filepath.Join(c.ArtifactDir(), "known_hosts")
		line := handle.PayloadValue("host_key") + "\n"
		if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
			c.Fatalf("写出 known_hosts 失败: %v", err)
		}
		store, err := sshclient.NewKnownHostsStore(sshclient.KnownHostsOptions{
			TrustedPaths: []string{knownHosts},
		})
		if err != nil {
			c.Fatalf("读取 known_hosts 失败: %v", err)
		}
		dialer, err := sshclient.NewDialer(sshclient.DialOptions{HostKeys: store})
		if err != nil {
			c.Fatalf("构造连接器失败: %v", err)
		}
		host, portText, err := net.SplitHostPort(handle.PayloadValue("address"))
		if err != nil {
			c.Fatalf("句柄地址不合法: %v", err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			c.Fatalf("句柄端口不合法: %v", err)
		}
		result, err := dialer.Dial(c.Context(), sshclient.Endpoint{
			Host:     host,
			Port:     port,
			Username: handle.PayloadValue("user"),
		}, sshclient.AuthConfig{
			Method:         sshclient.AuthMethodPrivateKey,
			PrivateKeyPath: handle.PayloadValue("private_key_path"),
		})
		if err != nil {
			c.Fatalf("登录测试 SSH 服务失败: %v", err)
		}
		defer func() { _ = result.Client.Close() }()
		command, err := sshclient.ShellCommand("printf", "ok")
		if err != nil {
			c.Fatalf("构造远程命令失败: %v", err)
		}
		run, err := result.Client.Run(c.Context(), command, sshclient.RunOptions{Timeout: 10 * time.Second})
		if err != nil {
			c.Fatalf("执行远程命令失败: %v", err)
		}
		if !run.Succeeded() {
			c.Fatalf("远程命令退出码 %d: %s", run.ExitCode, run.Stderr)
		}
		c.Logf("测试 SSH 服务可用：%s（主机密钥 %s）", handle.PayloadValue("address"), result.HostKey.Fingerprint)
	},
})
