package testrunner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// resourceProbeTimeout 是资源就绪与消失自证的默认超时。
const resourceProbeTimeout = 5 * time.Second

// verifyPortClosed 自证本地端口不再接受连接。
//
// 连接成功说明监听器仍然存在，自证失败；超时或其它错误无法区分"已关闭"与"网络异常"，
// 同样判为无法确认，不能用"连不上"冒充"已释放"。
func verifyPortClosed(ctx context.Context, address string) error {
	dialer := net.Dialer{Timeout: resourceProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("端口 %s 仍然可以连接，资源没有真正释放", address)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("连接 %s 超时，无法确认端口已关闭", address)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("核验端口 %s 时上下文已结束: %w", address, err)
	}
	return nil
}

// waitHTTPReady 轮询等待 HTTP 端点返回 200，用于服务类资源的就绪判定。
func waitHTTPReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = resourceProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: resourceProbeTimeout}
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, baseURL, nil)
		if err != nil {
			return fmt.Errorf("构造就绪探测请求失败: %w", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("就绪探测返回状态码 %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-probeCtx.Done():
			return fmt.Errorf("%s 在 %s 内没有就绪: %w", baseURL, timeout, lastErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// verifyHTTPGone 自证 HTTP 端点不再可用。
func verifyHTTPGone(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: resourceProbeTimeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return fmt.Errorf("构造消失核验请求失败: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	_ = response.Body.Close()
	return fmt.Errorf("服务 %s 在释放后仍返回状态码 %d", baseURL, response.StatusCode)
}
