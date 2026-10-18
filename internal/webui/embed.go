// Package webui 把前端生产构建产物嵌入可执行文件，并提供静态资源与 SPA 回退服务。
//
// 嵌入目录固定为包目录下的 dist/，由前端构建脚本写入（见仓库根目录 Makefile）。
// 仓库中保留 dist/.gitkeep，使全新检出也能通过 go 编译与 go test；但只有真实构建
// 产物才能提供页面，Handler 会在没有 index.html 时明确报错，不返回空白页。
package webui

import (
	"embed"
	"errors"
	"io/fs"
)

// distDir 是嵌入目录名，与前端构建输出目录保持一致。
const distDir = "dist"

// indexFile 是前端入口文件名。
const indexFile = "index.html"

//go:embed all:dist
var embedded embed.FS

// FS 返回嵌入的前端资源根目录。
//
// 返回的文件系统以 dist 目录为根，因此 "assets/app.js" 这类路径可以直接读取。
func FS() fs.FS {
	sub, err := fs.Sub(embedded, distDir)
	if err != nil {
		// embed.FS 的 dist 目录由 //go:embed 保证存在，这里只防御性地返回空文件系统。
		return emptyFS{}
	}
	return sub
}

// HasIndex 判断嵌入资源中是否存在前端入口文件。
//
// 它用于在启动阶段区分"正式构建产物"与"仅含占位文件的源码检出"。
func HasIndex(filesystem fs.FS) bool {
	if filesystem == nil {
		return false
	}
	info, err := fs.Stat(filesystem, indexFile)
	return err == nil && !info.IsDir()
}

// ErrNoIndex 表示嵌入资源中缺少前端入口文件。
var ErrNoIndex = errors.New("webui: 嵌入的前端资源缺少 " + indexFile + "，请先执行前端生产构建")

// emptyFS 是缺少 dist 目录时使用的空文件系统。
type emptyFS struct{}

// Open 实现 fs.FS。
func (emptyFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}
