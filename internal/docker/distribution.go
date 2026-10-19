package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Docker Engine 镜像元数据查询的默认参数。
const (
	// DefaultDistributionTimeout 是单个镜像元数据查询的超时时间。
	DefaultDistributionTimeout = 8 * time.Second
	// DefaultDockerSocketPath 是远端 Docker Engine 的默认 Unix 套接字路径。
	DefaultDockerSocketPath = "/var/run/docker.sock"
	// maxDistributionBodyBytes 是镜像元数据响应体的读取上限。
	maxDistributionBodyBytes = 1 << 20
	// dockerAPIVersionPrefix 是 Engine API 的版本化路径前缀。
	dockerAPIVersionPrefix = "/v1.24"
)

// 镜像元数据查询的错误分类。
//
// 分类必须区分"镜像不存在"、"仓库不可达"、"认证失败"与"接口不兼容"：
// 它们的处置方式完全不同，不能统一报告为查询失败。
const (
	// MetadataCodeOK 表示查询成功。
	MetadataCodeOK = ""
	// MetadataCodeNotFound 表示镜像或 tag 不存在。
	MetadataCodeNotFound = "image_not_found"
	// MetadataCodeRegistryUnreachable 表示镜像仓库不可达或网络查询失败。
	MetadataCodeRegistryUnreachable = "registry_unreachable"
	// MetadataCodeAuthFailed 表示仓库认证失败或权限不足。
	MetadataCodeAuthFailed = "registry_auth_failed"
	// MetadataCodeTimeout 表示查询在给定时限内未完成。
	MetadataCodeTimeout = "metadata_timeout"
	// MetadataCodeCancelled 表示调用方取消了查询。
	MetadataCodeCancelled = "metadata_cancelled"
	// MetadataCodeUnsupported 表示当前 Engine 不支持该接口或返回结构无法识别。
	MetadataCodeUnsupported = "metadata_api_unsupported"
	// MetadataCodeSocketUnavailable 表示无法打开远端 Docker Engine 套接字。
	MetadataCodeSocketUnavailable = "docker_socket_unavailable"
	// MetadataCodeInvalidReference 表示镜像引用不合法。
	MetadataCodeInvalidReference = "invalid_image_reference"
)

// DistributionInspect 是 GET /distribution/{name}/json 的关键字段。
type DistributionInspect struct {
	// Digest 是镜像清单摘要，接口未返回时为空。
	Digest string
	// Platforms 是清单中的平台列表，形如 linux/amd64。
	Platforms []string
}

// DistributionResult 是一次镜像元数据查询的结果。
type DistributionResult struct {
	// Reference 是被查询的镜像引用。
	Reference string
	// Status 是结果状态，成功时为 MetadataCodeOK。
	Status string
	// Message 是简短说明，已截断并脱敏。
	Message string
	// Digest 是镜像清单摘要。
	Digest string
	// Platforms 是平台列表。
	Platforms []string
	// Duration 是查询耗时。
	Duration time.Duration
}

// Succeeded 判断查询是否成功。
func (r DistributionResult) Succeeded() bool {
	return r.Status == MetadataCodeOK
}

// distributionResponse 是 Engine 返回的镜像分布检查结构。
type distributionResponse struct {
	Descriptor struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		Platform  struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
		} `json:"platform"`
	} `json:"Descriptor"`
	Platforms []struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
	} `json:"Platforms"`
}

// InspectDistribution 通过远端 Docker Engine 查询镜像元数据。
//
// 该方法只发起一次只读的 Distribution Inspect 请求：不拉取镜像层、不创建或运行
// 容器，也不改变远端镜像缓存、容器与卷的状态。请求经由已认证的 SSH 连接直接访问
// 远端 Docker Engine 的 Unix 套接字，因此会走该 Engine 配置的镜像解析路径，
// 不需要远端安装 curl 等额外工具。
func (r *Runner) InspectDistribution(
	ctx context.Context,
	reference string,
	timeout time.Duration,
) DistributionResult {
	result := DistributionResult{Reference: reference}
	started := time.Now()
	defer func() {
		result.Duration = time.Since(started)
	}()
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		result.Status = MetadataCodeInvalidReference
		result.Message = "镜像引用不能为空"
		return result
	}
	if timeout <= 0 {
		timeout = DefaultDistributionTimeout
	}
	dialer, ok := r.client.(interface {
		DialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error)
	})
	if !ok {
		result.Status = MetadataCodeUnsupported
		result.Message = "当前 SSH 连接不支持访问远端 Unix 套接字，无法查询镜像元数据"
		return result
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dialer.DialUnixSocket(queryCtx, DefaultDockerSocketPath)
	if err != nil {
		result.Status = classifySocketError(queryCtx, err)
		result.Message = summarizeOutput(err.Error())
		return result
	}
	defer func() {
		_ = conn.Close()
	}()

	client := &http.Client{Transport: &unixSocketTransport{conn: conn}}
	path := dockerAPIVersionPrefix + "/distribution/" + urlPathEscape(trimmed) + "/json"
	request, err := http.NewRequestWithContext(queryCtx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		result.Status = MetadataCodeInvalidReference
		result.Message = summarizeOutput(err.Error())
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		result.Status = classifyQueryError(queryCtx, err)
		result.Message = summarizeOutput(err.Error())
		return result
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxDistributionBodyBytes))
		_ = response.Body.Close()
	}()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxDistributionBodyBytes))
	if readErr != nil {
		result.Status = classifyQueryError(queryCtx, readErr)
		result.Message = summarizeOutput(readErr.Error())
		return result
	}
	if response.StatusCode != http.StatusOK {
		result.Status = classifyDistributionStatus(response.StatusCode, string(body))
		result.Message = distributionFailureMessage(response.StatusCode, body)
		return result
	}
	parsed, parseErr := parseDistributionResponse(body)
	if parseErr != nil {
		result.Status = MetadataCodeUnsupported
		result.Message = parseErr.Error()
		return result
	}
	result.Digest = parsed.Digest
	result.Platforms = parsed.Platforms
	if result.Digest == "" {
		// 接口返回 200 但没有摘要说明响应结构与预期不符：不能判定为成功。
		result.Status = MetadataCodeUnsupported
		result.Message = "镜像元数据响应缺少 digest，无法确认镜像存在"
		return result
	}
	result.Message = "镜像元数据查询成功"
	return result
}

// unixSocketTransport 把 HTTP 请求转发到一条已建立的 Unix 套接字通道。
//
// 通道由 SSH 连接提供，因此每个 Runner 调用只使用一条通道，不使用连接池。
type unixSocketTransport struct {
	conn net.Conn
}

// RoundTrip 在 Unix 套接字通道上完成一次 HTTP 往返。
//
// net/http 只在传输层实现 CancelRequest 或自行监听上下文时才会中断请求，
// 自定义传输不会自动获得该行为。这里显式监听请求上下文：取消或超时时关闭
// 通道，使阻塞中的写入与读取立即返回，避免查询在给定时限之外继续挂起。
func (t *unixSocketTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.conn == nil {
		return nil, fmt.Errorf("docker: Unix 套接字通道未建立")
	}
	ctx := request.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = t.conn.Close()
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-watcherDone
	}()
	// SSH 通道不支持 SetDeadline（会返回 "deadline not supported"），
	// 因此截止时间只能通过关闭通道来生效，取消与超时由上面的监听协程处理。
	if err := request.Write(t.conn); err != nil {
		return nil, t.contextError(ctx, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(t.conn), request)
	if err != nil {
		return nil, t.contextError(ctx, err)
	}
	return response, nil
}

// contextError 优先返回上下文错误，使取消与超时能被正确分类。
func (t *unixSocketTransport) contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// parseDistributionResponse 解析镜像分布检查响应。
func parseDistributionResponse(body []byte) (DistributionInspect, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return DistributionInspect{}, fmt.Errorf("镜像元数据响应为空")
	}
	var payload distributionResponse
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return DistributionInspect{}, fmt.Errorf("镜像元数据响应不是合法的 JSON")
	}
	inspect := DistributionInspect{Digest: strings.TrimSpace(payload.Descriptor.Digest)}
	seen := make(map[string]struct{}, len(payload.Platforms)+1)
	for _, platform := range payload.Platforms {
		if label := platformLabel(platform.OS, platform.Architecture, platform.Variant); label != "" {
			if _, ok := seen[label]; ok {
				continue
			}
			seen[label] = struct{}{}
			inspect.Platforms = append(inspect.Platforms, label)
		}
	}
	if len(inspect.Platforms) == 0 {
		label := platformLabel(
			payload.Descriptor.Platform.OS,
			payload.Descriptor.Platform.Architecture,
			payload.Descriptor.Platform.Variant,
		)
		if label != "" {
			inspect.Platforms = []string{label}
		}
	}
	return inspect, nil
}

// platformLabel 拼接平台标签，字段缺失时返回空字符串。
func platformLabel(os, architecture, variant string) string {
	trimmedOS := strings.TrimSpace(os)
	trimmedArch := strings.TrimSpace(architecture)
	if trimmedOS == "" || trimmedArch == "" {
		return ""
	}
	label := trimmedOS + "/" + trimmedArch
	if trimmedVariant := strings.TrimSpace(variant); trimmedVariant != "" {
		label += "/" + trimmedVariant
	}
	return label
}

// classifySocketError 把套接字建立失败归类为可诊断的状态。
func classifySocketError(ctx context.Context, err error) string {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contextMetadataStatus(ctxErr)
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "permission denied"):
		return MetadataCodeAuthFailed
	case strings.Contains(message, "no such file"),
		strings.Contains(message, "connection refused"),
		strings.Contains(message, "administratively prohibited"):
		return MetadataCodeSocketUnavailable
	default:
		return MetadataCodeSocketUnavailable
	}
}

// classifyQueryError 把查询过程中的错误归类为可诊断的状态。
func classifyQueryError(ctx context.Context, err error) string {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contextMetadataStatus(ctxErr)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return MetadataCodeTimeout
	}
	return MetadataCodeRegistryUnreachable
}

// contextMetadataStatus 把上下文错误映射为元数据查询状态。
func contextMetadataStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return MetadataCodeTimeout
	}
	return MetadataCodeCancelled
}

// classifyDistributionStatus 依据 HTTP 状态码判断查询失败原因。
func classifyDistributionStatus(status int, body string) string {
	message := strings.ToLower(body)
	switch {
	case status == http.StatusNotFound:
		return MetadataCodeNotFound
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return MetadataCodeAuthFailed
	case status == http.StatusBadRequest,
		status == http.StatusMethodNotAllowed,
		status == http.StatusNotImplemented:
		return MetadataCodeUnsupported
	case status == http.StatusGatewayTimeout:
		return MetadataCodeTimeout
	case strings.Contains(message, "unauthorized"),
		strings.Contains(message, "denied"):
		return MetadataCodeAuthFailed
	case strings.Contains(message, "not found"),
		strings.Contains(message, "manifest unknown"):
		return MetadataCodeNotFound
	case strings.Contains(message, "timeout"),
		strings.Contains(message, "deadline exceeded"):
		return MetadataCodeTimeout
	default:
		return MetadataCodeRegistryUnreachable
	}
}

// distributionFailureMessage 生成失败说明，已截断且不包含凭据。
func distributionFailureMessage(status int, body []byte) string {
	summary := summarizeOutput(string(body))
	prefix := "镜像元数据查询返回 HTTP " + strconv.Itoa(status)
	if summary == "" {
		return prefix
	}
	return prefix + ": " + summary
}

// urlPathEscape 转义镜像引用中的路径分隔符，使其可以安全嵌入 URL 路径。
//
// Docker 的 Distribution Inspect 接口使用 `{name}` 占位符，name 中包含 `/` 时
// 按原样保留在路径中，因此这里只转义会破坏 URL 的字符。
func urlPathEscape(reference string) string {
	var builder strings.Builder
	for _, char := range reference {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '/' || char == ':' || char == '@' || char == '.' ||
			char == '-' || char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteString(fmt.Sprintf("%%%02X", char))
		}
	}
	return builder.String()
}
