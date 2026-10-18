package httpapi

import "strings"

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
