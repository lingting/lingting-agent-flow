package httpserver

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/webui"
)

// apiPrefix 是 API 的路径前缀，与 httpapi 保持一致。
const apiPrefix = httpapi.APIPrefix

// routes 组合 API 与前端处理器。
//
// /api/ 下的请求全部交给 API 处理器，其余请求交给静态资源处理器；这样未知 API
// 路径只会得到 JSON 404，不会落到前端的 SPA 回退上。
func routes(api http.Handler, web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
}

// isAPIPath 判断请求路径是否属于 API 命名空间。
func isAPIPath(path string) bool {
	return path == apiPrefix || strings.HasPrefix(path, apiPrefix+"/")
}

// buildWebHandler 构造前端静态资源处理器。
//
// 缺少构建产物时按 RequireWebUI 决定是失败还是给出诊断页：静默返回空白页会让
// "打包漏了前端" 这类问题在发布后才被发现。
func buildWebHandler(files fs.FS, require bool, logger *slog.Logger) (http.Handler, error) {
	handler, err := webui.NewHandler(files, logger)
	if err == nil {
		return handler, nil
	}
	if require {
		return nil, err
	}
	logger.Warn("前端构建产物缺失，Web 页面不可用", slog.String("error", err.Error()))
	return webui.NewDiagnosticHandler(err, logger), nil
}
