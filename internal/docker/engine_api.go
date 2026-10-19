package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Engine API 基准探测的错误分类。
const (
	// APICheckCodeOK 表示基准 API 版本在 Engine 上可用。
	APICheckCodeOK = ""
	// APICheckCodeIncompatible 表示 Engine 声明的 API 区间不包含基准版本。
	APICheckCodeIncompatible = "api_version_incompatible"
	// APICheckCodeUnsupported 表示基准版本路径不可用或响应结构无法识别。
	APICheckCodeUnsupported = "api_version_unsupported"
	// APICheckCodeSocketUnavailable 表示无法打开远端 Docker Engine 套接字。
	APICheckCodeSocketUnavailable = "docker_socket_unavailable"
	// APICheckCodeTimeout 表示探测在给定时限内未完成。
	APICheckCodeTimeout = "api_probe_timeout"
	// APICheckCodeCancelled 表示调用方取消了探测。
	APICheckCodeCancelled = "api_probe_cancelled"
)

// APIVersionResult 是一次 Engine API 基准探测的结果。
//
// 探测通过 GET /v1.52/version 完成：该请求既是只读操作，也直接证明固定基准版本的
// 路径在目标 Engine 上可用，不依赖 CLI 的自动版本协商结果。
type APIVersionResult struct {
	// Baseline 是 AgentFlow 固定的 API 基准版本。
	Baseline string
	// APIVersion 是 Engine 声明的最高 API 版本。
	APIVersion string
	// MinAPIVersion 是 Engine 接受的最低 API 版本。
	MinAPIVersion string
	// Status 是结果分类，成功时为 APICheckCodeOK。
	Status string
	// Message 是简短说明，已截断且不含凭据。
	Message string
	// Duration 是本次探测耗时。
	Duration time.Duration
}

// Succeeded 判断探测是否通过。
func (r APIVersionResult) Succeeded() bool {
	return r.Status == APICheckCodeOK
}

// engineVersionResponse 是 GET /version 的响应中本包需要的字段。
type engineVersionResponse struct {
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Version       string `json:"Version"`
}

// ProbeAPIVersion 通过固定基准路径探测远端 Engine 的 API 兼容性。
//
// 只发起一次只读的 GET /v1.52/version：不创建容器、不拉取镜像、不改变任何远端状态。
// 请求经由已认证 SSH 连接直接访问远端 Docker Engine 的 Unix 套接字，与镜像元数据查询
// 使用同一套传输方式与固定的 API 基准。
func (r *Runner) ProbeAPIVersion(
	ctx context.Context,
	timeout time.Duration,
) APIVersionResult {
	result := APIVersionResult{Baseline: EngineAPIBaseVersion}
	started := time.Now()
	defer func() {
		result.Duration = time.Since(started)
	}()
	if timeout <= 0 {
		timeout = DefaultDistributionTimeout
	}
	dialer, ok := r.client.(interface {
		DialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error)
	})
	if !ok {
		result.Status = APICheckCodeUnsupported
		result.Message = "当前 SSH 连接不支持访问远端 Unix 套接字，无法探测 Engine API 版本"
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialer.DialUnixSocket(probeCtx, DefaultDockerSocketPath)
	if err != nil {
		result.Status = classifyAPIDialError(probeCtx, err)
		result.Message = summarizeOutput(err.Error())
		return result
	}
	defer func() {
		_ = conn.Close()
	}()
	client := &http.Client{Transport: &unixSocketTransport{conn: conn}}
	path := dockerAPIVersionPrefix + "/version"
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		result.Status = APICheckCodeUnsupported
		result.Message = summarizeOutput(err.Error())
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		result.Status = classifyAPIProbeError(probeCtx, err)
		result.Message = summarizeOutput(err.Error())
		return result
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxDistributionBodyBytes))
		_ = response.Body.Close()
	}()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxDistributionBodyBytes))
	if readErr != nil {
		result.Status = classifyAPIProbeError(probeCtx, readErr)
		result.Message = summarizeOutput(readErr.Error())
		return result
	}
	if response.StatusCode != http.StatusOK {
		result.Status = APICheckCodeUnsupported
		result.Message = fmt.Sprintf(
			"GET %s 返回 HTTP %d: %s",
			path,
			response.StatusCode,
			summarizeOutput(string(body)),
		)
		return result
	}
	var payload engineVersionResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		result.Status = APICheckCodeUnsupported
		result.Message = "Engine 版本响应不是合法的 JSON"
		return result
	}
	result.APIVersion = strings.TrimSpace(payload.APIVersion)
	result.MinAPIVersion = strings.TrimSpace(payload.MinAPIVersion)
	check := CheckAPICompatibility(APIRange{
		Maximum: result.APIVersion,
		Minimum: result.MinAPIVersion,
	})
	if !check.Succeeded() {
		result.Status = APICheckCodeIncompatible
		result.Message = check.Err.Error()
		return result
	}
	result.Message = fmt.Sprintf(
		"固定 API 基准 %s 在 Engine 上可用（最高 %s，最低接受 %s）",
		EngineAPIBaseVersion,
		result.APIVersion,
		result.MinAPIVersion,
	)
	return result
}

// classifyAPIDialError 把套接字建立失败归类为可诊断的状态。
func classifyAPIDialError(ctx context.Context, err error) string {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if ctxErr == context.DeadlineExceeded {
			return APICheckCodeTimeout
		}
		return APICheckCodeCancelled
	}
	return APICheckCodeSocketUnavailable
}

// classifyAPIProbeError 把探测过程中的错误归类为可诊断的状态。
func classifyAPIProbeError(ctx context.Context, err error) string {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if ctxErr == context.DeadlineExceeded {
			return APICheckCodeTimeout
		}
		return APICheckCodeCancelled
	}
	return APICheckCodeUnsupported
}
