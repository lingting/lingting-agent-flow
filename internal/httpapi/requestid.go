package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// requestIDHeader 是请求标识的响应头名称。
const requestIDHeader = "X-Request-Id"

// requestIDKey 是请求标识在 context 中的键。
type requestIDKey struct{}

// withRequestID 为每个请求生成标识，并回写到响应头。
//
// 请求标识只用于把响应与日志关联，不参与业务判断，也不接受外部传入值。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// requestIDFrom 返回当前请求的标识，缺失时返回空字符串。
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID 生成 16 字节随机标识的十六进制形式。
//
// 随机源失败时返回空字符串：请求标识不是业务必需信息，不能因此让请求失败。
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(buf[:])
}
