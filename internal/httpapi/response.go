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
	// CodeInvalidRequest 表示请求体或查询参数不合法。
	CodeInvalidRequest = "invalid_request"
	// CodeConflict 表示资源当前状态不允许该操作。
	CodeConflict = "state_conflict"
	// CodePreconditionFailed 表示执行所需的前置条件尚未满足，
	// 例如任务缺少受管理的运行中容器，或执行节点未通过环境预检。
	CodePreconditionFailed = "precondition_failed"
	// CodeExecutionFailed 表示执行已经发生但未成功：失败、超时、取消或中断。
	CodeExecutionFailed = "execution_failed"
	// CodeExecutionNotPersisted 表示执行已结束但最终状态未能完整写入。
	CodeExecutionNotPersisted = "execution_not_persisted"
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
