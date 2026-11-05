package httpapi

import (
	"net/http"
	"strings"
)

// APIRoot 是全部后端接口的保留路径前缀。
//
// 该路径空间专用于 API：未注册的路径返回统一 JSON 错误，永远不会回退到前端页面。
const APIRoot = "/api"

// APIPrefix 是当前接口版本的前缀，具体接口都注册在它下面。
const APIPrefix = APIRoot + "/v1"

// IsAPIPath 判断请求路径是否属于 API 保留路径空间。
//
// 判定按路径段边界进行：/api、/api/、/api/v1/health 属于 API，而 /apiary、/apix
// 不属于。因此不能用简单的字符串前缀判断，否则会把普通页面路径误判为接口。
func IsAPIPath(path string) bool {
	return path == APIRoot || strings.HasPrefix(path, APIRoot+"/")
}

// pathID 返回路径参数去掉首尾空白后的取值。
//
// 组合路由后，路径参数由 ServeMux 解码并交给 Request.PathValue；两侧空白不参与标识
// 与业务取值，与集中路由之前的段解析保持一致。
func pathID(r *http.Request, name string) string {
	return strings.TrimSpace(r.PathValue(name))
}

// validPathID 判断路径参数能否作为单个资源标识使用。
//
// 空白标识与含路径分隔符的取值一律无效：ServeMux 会对路径段做百分号解码，编码的
// %2F 会解码成 /，这类取值不能被当作一个标识交给业务服务，必须按接口不存在处理。
func validPathID(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && !strings.Contains(trimmed, "/")
}
