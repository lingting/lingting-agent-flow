package docker_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/docker"
	"github.com/lingting/agentflow/internal/sshclient"
)

// socketClient 是同时实现 sshclient.Client 与 Unix 套接字通道的替身。
//
// 它把远端 Docker Engine 的 Unix 套接字替换为一条本机 net.Pipe，使测试可以在
// 不启动真实 Docker 的前提下验证 HTTP 请求内容与响应处理。
type socketClient struct {
	mu sync.Mutex
	// requests 记录收到的 HTTP 请求行。
	requests []string
	// dialErr 非空时模拟套接字建立失败。
	dialErr error
	// handler 处理一次连接上的 HTTP 请求。
	handler func(connection net.Conn, requestLine string)
}

func (c *socketClient) Endpoint() sshclient.Endpoint {
	return sshclient.Endpoint{Host: "127.0.0.1", Port: 22, Username: "deploy"}
}

func (c *socketClient) Run(
	_ context.Context,
	command string,
	_ sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	return sshclient.CommandResult{
		Command:              command,
		Status:               sshclient.CommandStatusSucceeded,
		TerminationConfirmed: true,
	}, nil
}

func (c *socketClient) RunWithInput(
	ctx context.Context,
	command string,
	input io.Reader,
	options sshclient.RunOptions,
) (sshclient.CommandResult, error) {
	if input != nil {
		if _, err := io.Copy(io.Discard, input); err != nil {
			return sshclient.CommandResult{}, err
		}
	}
	return c.Run(ctx, command, options)
}

func (c *socketClient) SFTP() (sshclient.FileTransfer, error) {
	return nil, errors.New("测试替身不支持 SFTP")
}

func (c *socketClient) Close() error { return nil }

// DialUnixSocket 建立本机管道并交给 handler 处理请求。
func (c *socketClient) DialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	if socketPath != docker.DefaultDockerSocketPath {
		return nil, errors.New("套接字路径不符: " + socketPath)
	}
	if c.dialErr != nil {
		return nil, c.dialErr
	}
	server, client := net.Pipe()
	handler := c.handler
	c.mu.Lock()
	recorder := func(line string) {
		c.requests = append(c.requests, line)
	}
	c.mu.Unlock()
	go func() {
		defer func() {
			_ = server.Close()
		}()
		if handler == nil {
			return
		}
		// 读取请求行后交给 handler，由 handler 写回响应。
		buffer := make([]byte, 4096)
		count, err := server.Read(buffer)
		if err != nil {
			return
		}
		request := string(buffer[:count])
		line := request
		if index := strings.Index(request, "\r\n"); index >= 0 {
			line = request[:index]
		}
		recorder(line)
		handler(server, line)
	}()
	return client, nil
}

// requestLines 返回记录的请求行副本。
func (c *socketClient) requestLines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
}

// writeHTTPResponse 写出一条最小可解析的 HTTP 响应。
func writeHTTPResponse(connection net.Conn, status int, reason, body string) {
	response := "HTTP/1.1 " + itoa(status) + " " + reason + "\r\n" +
		"Content-Type: application/json\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n" +
		"Connection: close\r\n\r\n" + body
	_, _ = connection.Write([]byte(response))
}

// itoa 是避免引入 strconv 的极简整数格式化。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// TestInspectDistributionSucceeds 验证成功查询的请求路径与结果解析。
func TestInspectDistributionSucceeds(t *testing.T) {
	t.Parallel()
	body := `{"Descriptor":{"mediaType":"application/vnd.oci.image.index.v1+json",` +
		`"digest":"sha256:1111","size":100,"platform":{"architecture":"amd64","os":"linux"}},` +
		`"Platforms":[{"architecture":"amd64","os":"linux"},` +
		`{"architecture":"arm64","os":"linux","variant":"v8"}]}`
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 200, "OK", body)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "node:22.19.0-bookworm-slim", 0)
	if !result.Succeeded() {
		t.Fatalf("查询应当成功: %s %s", result.Status, result.Message)
	}
	if result.Digest != "sha256:1111" {
		t.Fatalf("摘要不符: %s", result.Digest)
	}
	if len(result.Platforms) != 2 || result.Platforms[1] != "linux/arm64/v8" {
		t.Fatalf("平台列表不符: %+v", result.Platforms)
	}
	requests := client.requestLines()
	if len(requests) != 1 {
		t.Fatalf("应当只发起一次请求: %+v", requests)
	}
	if !strings.Contains(requests[0], "/distribution/node:22.19.0-bookworm-slim/json") {
		t.Fatalf("请求路径不符: %s", requests[0])
	}
}

// TestInspectDistributionClassifiesNotFound 验证 404 被判为镜像不存在。
func TestInspectDistributionClassifiesNotFound(t *testing.T) {
	t.Parallel()
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 404, "Not Found",
			`{"message":"manifest unknown"}`)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:does-not-exist", 0)
	if result.Status != docker.MetadataCodeNotFound {
		t.Fatalf("状态不符: %s (%s)", result.Status, result.Message)
	}
}

// TestInspectDistributionClassifiesAuthFailure 验证 401 被判为认证失败。
func TestInspectDistributionClassifiesAuthFailure(t *testing.T) {
	t.Parallel()
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 401, "Unauthorized",
			`{"message":"unauthorized: authentication required"}`)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "private.example.com/app:1.0", 0)
	if result.Status != docker.MetadataCodeAuthFailed {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInspectDistributionClassifiesUnsupported 验证接口不兼容被单独分类。
func TestInspectDistributionClassifiesUnsupported(t *testing.T) {
	t.Parallel()
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 404, "Not Found",
			`{"message":"page not found"}`)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	// 404 仍然优先判定为镜像不存在；这里断言的是不会被误判为成功。
	if result.Succeeded() {
		t.Fatal("404 不应判定为成功")
	}
	invalid := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 400, "Bad Request", `{"message":"bad request"}`)
	}}
	unsupported := newRunner(t, invalid).InspectDistribution(
		context.Background(), "alpine:3.20", 0,
	)
	if unsupported.Status != docker.MetadataCodeUnsupported {
		t.Fatalf("400 应被判为接口不兼容: %s", unsupported.Status)
	}
}

// TestInspectDistributionRejectsMissingDigest 验证缺少 digest 时不能判定成功。
func TestInspectDistributionRejectsMissingDigest(t *testing.T) {
	t.Parallel()
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 200, "OK", `{"Descriptor":{}}`)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	if result.Succeeded() {
		t.Fatal("缺少 digest 时不应判定为成功")
	}
	if result.Status != docker.MetadataCodeUnsupported {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInspectDistributionClassifiesRegistryUnreachable 验证仓库不可达被单独分类。
func TestInspectDistributionClassifiesRegistryUnreachable(t *testing.T) {
	t.Parallel()
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 500, "Internal Server Error",
			`{"message":"Get \"https://registry-1.docker.io/v2/\": dial tcp: connection refused"}`)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	if result.Status != docker.MetadataCodeRegistryUnreachable {
		t.Fatalf("状态不符: %s (%s)", result.Status, result.Message)
	}
}

// TestInspectDistributionClassifiesSocketFailure 验证套接字不可用时给出明确分类。
func TestInspectDistributionClassifiesSocketFailure(t *testing.T) {
	t.Parallel()
	client := &socketClient{dialErr: errors.New("open /var/run/docker.sock: no such file or directory")}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	if result.Status != docker.MetadataCodeSocketUnavailable {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInspectDistributionClassifiesPermission 验证权限不足被判为认证类失败。
func TestInspectDistributionClassifiesPermission(t *testing.T) {
	t.Parallel()
	client := &socketClient{dialErr: errors.New("open /var/run/docker.sock: permission denied")}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	if result.Status != docker.MetadataCodeAuthFailed {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInspectDistributionHonoursTimeout 验证单镜像超时被单独分类。
func TestInspectDistributionHonoursTimeout(t *testing.T) {
	t.Parallel()
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		// 故意不写响应，触发客户端超时。
		time.Sleep(300 * time.Millisecond)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 50*time.Millisecond)
	if result.Status != docker.MetadataCodeTimeout {
		t.Fatalf("状态不符: %s (%s)", result.Status, result.Message)
	}
}

// TestInspectDistributionHonoursCancellation 验证取消被单独分类。
func TestInspectDistributionHonoursCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		cancel()
		time.Sleep(100 * time.Millisecond)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(ctx, "alpine:3.20", 0)
	if result.Status != docker.MetadataCodeCancelled {
		t.Fatalf("状态不符: %s (%s)", result.Status, result.Message)
	}
}

// TestInspectDistributionRequiresSocketSupport 验证连接不支持套接字通道时明确失败。
func TestInspectDistributionRequiresSocketSupport(t *testing.T) {
	t.Parallel()
	// helpClient 只实现 sshclient.Client，没有 Unix 套接字通道。
	runner := newRunner(t, &helpClient{})
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	if result.Status != docker.MetadataCodeUnsupported {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestInspectDistributionRejectsEmptyReference 验证空引用被拒绝。
func TestInspectDistributionRejectsEmptyReference(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, &socketClient{})
	result := runner.InspectDistribution(context.Background(), "   ", 0)
	if result.Status != docker.MetadataCodeInvalidReference {
		t.Fatalf("状态不符: %s", result.Status)
	}
}

// TestParseDistributionResponseHandlesPlatformDedup 验证平台去重与缺省回退。
func TestParseDistributionResponseHandlesPlatformDedup(t *testing.T) {
	t.Parallel()
	body := `{"Descriptor":{"digest":"sha256:abc","platform":{"os":"linux","architecture":"amd64"}},` +
		`"Platforms":[{"os":"linux","architecture":"amd64"},{"os":"linux","architecture":"amd64"}]}`
	client := &socketClient{handler: func(connection net.Conn, _ string) {
		writeHTTPResponse(connection, 200, "OK", body)
	}}
	runner := newRunner(t, client)
	result := runner.InspectDistribution(context.Background(), "alpine:3.20", 0)
	if !result.Succeeded() {
		t.Fatalf("查询应当成功: %s", result.Message)
	}
	if len(result.Platforms) != 1 {
		t.Fatalf("平台应去重: %+v", result.Platforms)
	}
}

// TestValidationTimeoutsAreCentralised 验证超时默认值集中在 docker 包内。
func TestValidationTimeoutsAreCentralised(t *testing.T) {
	t.Parallel()
	timeouts := docker.DefaultValidationTimeouts()
	if timeouts.SSHSeconds != docker.ValidationSSHTimeoutSeconds {
		t.Fatalf("SSH 超时未使用集中定义: %d", timeouts.SSHSeconds)
	}
	if timeouts.ImageSeconds != docker.ValidationImageTimeoutSeconds {
		t.Fatalf("镜像超时未使用集中定义: %d", timeouts.ImageSeconds)
	}
	if timeouts.TotalSeconds != docker.ValidationTotalTimeoutSeconds {
		t.Fatalf("总超时未使用集中定义: %d", timeouts.TotalSeconds)
	}
	if timeouts.TotalSeconds <= timeouts.SSHSeconds {
		t.Fatal("总时限必须大于单步 SSH 时限")
	}
}
