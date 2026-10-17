package sshprobe_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/sshprobe"
)

// startServer 启动一个本地 TCP 服务，accept 后由 handler 处理连接。
//
// 返回的地址可以直接作为探测目标；测试结束时监听器与连接都会被关闭。
func startServer(t *testing.T, handler func(conn net.Conn)) (string, int) {
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
				handler(conn)
			}(conn)
		}
	}()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址类型不符: %T", listener.Addr())
	}
	return address.IP.String(), address.Port
}

// newProber 构造指定超时的探测器。
func newProber(t *testing.T, timeout time.Duration) *sshprobe.Prober {
	t.Helper()
	prober, err := sshprobe.NewProber(sshprobe.Options{Timeout: timeout})
	if err != nil {
		t.Fatalf("构造探测器失败: %v", err)
	}
	return prober
}

// probeError 断言错误是结构化探测错误并返回其类别。
func probeError(t *testing.T, err error) *sshprobe.ProbeError {
	t.Helper()
	var probeErr *sshprobe.ProbeError
	if !errors.As(err, &probeErr) {
		t.Fatalf("应当返回 *ProbeError，实际: %v", err)
	}
	return probeErr
}

// TestProbeDetectsSSHService 验证有效协议标识会被识别，且不会被标记为已认证。
func TestProbeDetectsSSHService(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
		<-time.After(200 * time.Millisecond)
	})
	prober := newProber(t, 2*time.Second)
	result, err := prober.Probe(context.Background(), sshprobe.Target{Host: host, Port: port})
	if err != nil {
		t.Fatalf("探测应当成功: %v", err)
	}
	if !result.Reachable || !result.SSHDetected {
		t.Fatalf("应当识别到 SSH 服务: %+v", result)
	}
	if result.Authenticated {
		t.Fatal("本阶段不得标记为已完成身份认证")
	}
	if result.ErrorCode != sshprobe.CodeOK {
		t.Fatalf("结果类别不符: %s", result.ErrorCode)
	}
	if !result.Succeeded() {
		t.Fatalf("结果应当判定为成功: %+v", result)
	}
	if strings.Contains(result.Message, "登录成功") {
		t.Fatalf("说明不得声称登录成功: %s", result.Message)
	}
	if !strings.Contains(result.Message, "尚未执行身份认证") {
		t.Fatalf("说明应明确未认证: %s", result.Message)
	}
	// 本机回环探测可能快于计时精度，只要求耗时非负且不超过超时上限。
	if result.Duration < 0 || result.Duration > 2*time.Second {
		t.Fatalf("探测耗时不合法: %s", result.Duration)
	}
}

// TestProbeSkipsExtraLinesBeforeIdentifier 验证标识行之前的额外行会被跳过。
func TestProbeSkipsExtraLinesBeforeIdentifier(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("device notice: maintenance window\r\n"))
		_, _ = conn.Write([]byte("\r\n"))
		_, _ = conn.Write([]byte("SSH-2.0-dropbear_2022.83\r\n"))
		<-time.After(200 * time.Millisecond)
	})
	result, err := newProber(t, 2*time.Second).Probe(
		context.Background(),
		sshprobe.Target{Host: host, Port: port},
	)
	if err != nil {
		t.Fatalf("探测应当成功: %v", err)
	}
	if !result.SSHDetected {
		t.Fatalf("应当跳过额外行识别到 SSH 服务: %+v", result)
	}
}

// TestProbeReportsUnreachable 验证连接被拒绝时返回不可达。
func TestProbeReportsUnreachable(t *testing.T) {
	t.Parallel()
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
	result, probeErr := newProber(t, time.Second).Probe(
		context.Background(),
		sshprobe.Target{Host: address.IP.String(), Port: port},
	)
	if probeErr == nil {
		t.Fatal("连接被拒绝时应当返回错误")
	}
	if code := probeError(t, probeErr).Code; code != sshprobe.CodeUnreachable {
		t.Fatalf("错误类别不符: %s", code)
	}
	if result.Reachable {
		t.Fatalf("不应标记为可达: %+v", result)
	}
	if result.Authenticated {
		t.Fatal("失败结果不得标记为已认证")
	}
}

// TestProbeReportsTimeout 验证服务端不返回协议标识时按超时结束。
func TestProbeReportsTimeout(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		// 保持连接但不发送任何内容，模拟无响应的服务。
		<-time.After(3 * time.Second)
	})
	started := time.Now()
	result, err := newProber(t, 300*time.Millisecond).Probe(
		context.Background(),
		sshprobe.Target{Host: host, Port: port},
	)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("未返回协议标识时应当返回超时错误")
	}
	if code := probeError(t, err).Code; code != sshprobe.CodeTimeout {
		t.Fatalf("错误类别不符: %s", code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("探测应当在超时后尽快结束，实际耗时: %s", elapsed)
	}
	if !result.Reachable {
		t.Fatalf("TCP 已建立应当标记为可达: %+v", result)
	}
	if result.SSHDetected {
		t.Fatalf("超时不应识别为 SSH 服务: %+v", result)
	}
}

// TestProbeReportsNotSSH 验证非 SSH 服务不会被误判。
func TestProbeReportsNotSSH(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
	})
	result, err := newProber(t, 2*time.Second).Probe(
		context.Background(),
		sshprobe.Target{Host: host, Port: port},
	)
	if err == nil {
		t.Fatal("非 SSH 服务应当返回错误")
	}
	if code := probeError(t, err).Code; code != sshprobe.CodeNotSSH {
		t.Fatalf("错误类别不符: %s", code)
	}
	if !result.Reachable {
		t.Fatalf("TCP 已建立应当标记为可达: %+v", result)
	}
	if result.SSHDetected {
		t.Fatalf("非 SSH 服务不应识别为 SSH: %+v", result)
	}
	if result.Authenticated {
		t.Fatal("失败结果不得标记为已认证")
	}
}

// TestProbeReportsMalformedIdentifier 验证畸形协议标识被拒绝。
func TestProbeReportsMalformedIdentifier(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"缺少版本分隔符": "SSH-2\r\n",
		"版本号非数字":  "SSH-2.x-OpenSSH\r\n",
		"版本号含空格":  "SSH-2.0 OpenSSH\r\n",
		"前缀不完整":   "SSH\r\n",
	}
	for name, banner := range cases {
		host, port := startServer(t, func(conn net.Conn) {
			_, _ = conn.Write([]byte(banner))
		})
		result, err := newProber(t, 2*time.Second).Probe(
			context.Background(),
			sshprobe.Target{Host: host, Port: port},
		)
		if err == nil {
			t.Fatalf("%s: 畸形标识应当返回错误", name)
		}
		if code := probeError(t, err).Code; code != sshprobe.CodeNotSSH {
			t.Fatalf("%s: 错误类别不符: %s", name, code)
		}
		if result.SSHDetected {
			t.Fatalf("%s: 畸形标识不应识别为 SSH: %+v", name, result)
		}
	}
}

// TestProbeRespectsContextCancellation 验证上下文取消会立即结束探测。
func TestProbeRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		<-time.After(3 * time.Second)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-time.After(100 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, err := newProber(t, 5*time.Second).Probe(ctx, sshprobe.Target{Host: host, Port: port})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("取消后应当返回错误")
	}
	if code := probeError(t, err).Code; code != sshprobe.CodeCancelled {
		t.Fatalf("错误类别不符: %s", code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("取消后应当立即结束，实际耗时: %s", elapsed)
	}
}

// TestProbeRejectsInvalidTarget 验证目标地址非法时不发起连接。
func TestProbeRejectsInvalidTarget(t *testing.T) {
	t.Parallel()
	cases := map[string]sshprobe.Target{
		"缺少主机": {Host: "", Port: 22},
		"端口为零": {Host: "127.0.0.1", Port: 0},
		"端口超限": {Host: "127.0.0.1", Port: 70000},
	}
	for name, target := range cases {
		result, err := newProber(t, time.Second).Probe(context.Background(), target)
		if err == nil {
			t.Fatalf("%s: 非法目标应当返回错误", name)
		}
		if code := probeError(t, err).Code; code != sshprobe.CodeInvalidTarget {
			t.Fatalf("%s: 错误类别不符: %s", name, code)
		}
		if result.Reachable || result.SSHDetected {
			t.Fatalf("%s: 非法目标不应产生连接结果: %+v", name, result)
		}
	}
}

// countingConn 记录 Close 调用次数的连接包装。
type countingConn struct {
	net.Conn
	mu     sync.Mutex
	closes int
}

// Close 记录关闭次数并关闭底层连接。
func (c *countingConn) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return c.Conn.Close()
}

// closeCount 返回关闭次数。
func (c *countingConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

// trackingDialer 用受控连接替换真实拨号，用于检查资源释放。
type trackingDialer struct {
	mu    sync.Mutex
	conns []*countingConn
}

// DialContext 建立连接并包装为可统计的连接。
func (d *trackingDialer) DialContext(
	ctx context.Context,
	network, address string,
) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tracked := &countingConn{Conn: conn}
	d.mu.Lock()
	d.conns = append(d.conns, tracked)
	d.mu.Unlock()
	return tracked, nil
}

// TestProbeClosesConnection 验证探测结束后连接被关闭。
func TestProbeClosesConnection(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
		<-time.After(500 * time.Millisecond)
	})
	dialer := &trackingDialer{}
	prober, err := sshprobe.NewProber(sshprobe.Options{Timeout: 2 * time.Second, Dialer: dialer})
	if err != nil {
		t.Fatalf("构造探测器失败: %v", err)
	}
	if _, err := prober.Probe(
		context.Background(),
		sshprobe.Target{Host: host, Port: port},
	); err != nil {
		t.Fatalf("探测应当成功: %v", err)
	}
	dialer.mu.Lock()
	conns := append([]*countingConn(nil), dialer.conns...)
	dialer.mu.Unlock()
	if len(conns) != 1 {
		t.Fatalf("应当只建立一条连接，实际 %d", len(conns))
	}
	if conns[0].closeCount() == 0 {
		t.Fatal("探测结束后连接应当被关闭")
	}
}

// TestNewProberRejectsNegativeOptions 验证非法构造参数被拒绝。
func TestNewProberRejectsNegativeOptions(t *testing.T) {
	t.Parallel()
	if _, err := sshprobe.NewProber(sshprobe.Options{Timeout: -time.Second}); err == nil {
		t.Fatal("负超时应当被拒绝")
	}
	if _, err := sshprobe.NewProber(sshprobe.Options{MaxBannerBytes: -1}); err == nil {
		t.Fatal("负读取上限应当被拒绝")
	}
	if _, err := sshprobe.NewProber(sshprobe.Options{}); err != nil {
		t.Fatalf("默认参数应当可用: %v", err)
	}
}

// TestProbeReportsTooLongBanner 验证超长响应不会无限读取。
func TestProbeReportsTooLongBanner(t *testing.T) {
	t.Parallel()
	host, port := startServer(t, func(conn net.Conn) {
		payload := strings.Repeat("A", 4096) + "\r\n"
		for range 4 {
			if _, err := conn.Write([]byte(payload)); err != nil {
				return
			}
		}
	})
	prober, err := sshprobe.NewProber(sshprobe.Options{
		Timeout:        2 * time.Second,
		MaxBannerBytes: 1024,
	})
	if err != nil {
		t.Fatalf("构造探测器失败: %v", err)
	}
	result, probeErr := prober.Probe(
		context.Background(),
		sshprobe.Target{Host: host, Port: port},
	)
	if probeErr == nil {
		t.Fatal("超长响应应当返回错误")
	}
	if code := probeError(t, probeErr).Code; code != sshprobe.CodeNotSSH {
		t.Fatalf("错误类别不符: %s", code)
	}
	if result.SSHDetected {
		t.Fatalf("超长响应不应识别为 SSH: %+v", result)
	}
}
