package httpapi

import (
	"net/http"
)

// PathSettingsConfig 是基础运行配置只读查询路径。
//
// 它只提供数据库路径、监听地址、调度并发上限与 Session 根目录四项运行参数，
// 与 `/api/v1/system/info` 的构建信息语义不同，因此不合并成一个接口。
const PathSettingsConfig = APIPrefix + "/settings/config"

// settingsConfigHandler 处理基础运行配置的只读查询。
//
// 返回的快照由服务启动阶段生成：配置已完成解析与命令行覆盖，监听地址是最终生效值。
// 请求期间不重新读取配置文件，也不触发磁盘扫描、远程连接、Docker 操作或配置重载。
// 快照缺失时返回 503，不返回伪造的默认配置。
func (rt *router) settingsConfigHandler(w http.ResponseWriter, r *http.Request) {
	if rt.runtimeConfig == nil {
		writeError(
			w,
			r,
			rt.logger,
			http.StatusServiceUnavailable,
			CodeInternal,
			"基础运行配置快照未初始化",
		)
		return
	}
	writeJSON(w, r, rt.logger, http.StatusOK, rt.runtimeConfig)
}
