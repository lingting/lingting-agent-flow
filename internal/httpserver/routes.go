package httpserver

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/lingting/agentflow/internal/httpapi"
	"github.com/lingting/agentflow/internal/webui"
)

// routes 组合 API 与前端处理器。
//
// /api 保留路径空间下的请求全部交给 API 处理器，其余请求交给静态资源处理器；这样
// 未知 API 路径只会得到 JSON 404，不会落到前端的 SPA 回退上。判定使用
// httpapi.IsAPIPath 的路径段边界规则，因此 /apiary、/apix 仍按页面路径处理。
func routes(api http.Handler, web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpapi.IsAPIPath(r.URL.Path) {
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
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
