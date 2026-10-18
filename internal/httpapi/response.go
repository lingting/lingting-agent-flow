// Package httpapi 实现 AgentFlow 的本地 HTTP API。
//
// 本包只做协议转换：请求解析、JSON 编码与状态码映射；业务规则位于 app 与各领域包。
// 所有 API 路径统一挂在 /api/v1 前缀下，前端使用同源相对路径访问。
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// 错误代码：面向调用方的稳定标识，前端据此区分错误类别，展示文案由 message 提供。
const (
	// CodeNotFound 表示请求的 API 路径不存在。
	CodeNotFound = "not_found"
	// CodeMethodNotAllowed 表示路径存在但不支持该 HTTP 方法。
	CodeMethodNotAllowed = "method_not_allowed"
	// CodeInternal 表示服务内部错误。
	CodeInternal = "internal_error"
)

// ErrorBody 是统一的错误描述。
type ErrorBody struct {
	// Code 是稳定的机器可读错误代码。
	Code string `json:"code"`
	// Message 是面向用户的中文说明，不包含内部实现细节与敏感数据。
	Message string `json:"message"`
	// RequestID 是本次请求的标识，便于与日志对应，缺省时不输出。
	RequestID string `json:"request_id,omitempty"`
}

// ErrorResponse 是统一的错误响应结构，前端只需解析这一个形状。
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// errorResponse 构造错误响应。
func errorResponse(code, message, requestID string) ErrorResponse {
	return ErrorResponse{Error: ErrorBody{Code: code, Message: message, RequestID: requestID}}
}

// writeJSON 以 JSON 写出响应，编码失败时只记录日志，不再改动已写出的状态码。
func writeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil && logger != nil {
		logger.WarnContext(r.Context(), "写出 JSON 响应失败", slog.String("error", err.Error()))
	}
}

// writeError 写出统一错误响应。
func writeError(
	w http.ResponseWriter,
	r *http.Request,
	logger *slog.Logger,
	status int,
	code string,
	message string,
) {
	writeJSON(w, r, logger, status, errorResponse(code, message, requestIDFrom(r.Context())))
}
