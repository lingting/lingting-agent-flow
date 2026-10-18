package webui

import "mime"

// webMimeTypes 是构建产物中常见的扩展名映射。
//
// 不依赖系统 MIME 数据库：Windows 的 mime.TypeByExtension 会读取注册表，
// 同一份二进制在不同机器上可能给出不同结果，甚至缺少 .js 的类型。
var webMimeTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".ico":   "image/x-icon",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

// mimeTypeByExtension 返回扩展名对应的内容类型，未知时返回空字符串。
func mimeTypeByExtension(ext string) string {
	if detected, ok := webMimeTypes[ext]; ok {
		return detected
	}
	return mime.TypeByExtension(ext)
}
