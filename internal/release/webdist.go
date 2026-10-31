package release

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 前端生产构建产物的关键文件名与目录。
const (
	// WebDistIndexFile 是前端入口文件名。
	WebDistIndexFile = "index.html"
	// WebDistAssetsDir 是前端静态资源目录名。
	WebDistAssetsDir = "assets"
)

// VerifyWebDist 校验前端生产构建产物是否完整。
//
// 缺少入口页面、脚本或样式时返回错误。发布构建必须在编译 Go 之前执行本校验：
// `//go:embed` 会把 dist 目录原样嵌入，缺少产物时编译仍然成功，只是产出一个
// 页面空白的可执行文件。
func VerifyWebDist(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("前端构建产物目录不可用 %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("前端构建产物路径 %s 不是目录", dir)
	}
	indexPath := filepath.Join(dir, WebDistIndexFile)
	if err := checkNonEmptyFile(indexPath); err != nil {
		return err
	}
	assetsDir := filepath.Join(dir, WebDistAssetsDir)
	entries, err := os.ReadDir(assetsDir)
	if err != nil {
		return fmt.Errorf("前端构建产物缺少资源目录 %s: %w", assetsDir, err)
	}
	var hasScript, hasStyle bool
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".js"):
			hasScript = true
		case strings.HasSuffix(name, ".css"):
			hasStyle = true
		}
	}
	if !hasScript {
		return fmt.Errorf("前端构建产物缺少 JavaScript 资源: %s", assetsDir)
	}
	if !hasStyle {
		return fmt.Errorf("前端构建产物缺少 CSS 资源: %s", assetsDir)
	}
	return nil
}

// checkNonEmptyFile 确认路径是存在且非空的普通文件。
func checkNonEmptyFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("前端构建产物缺少 %s: %w", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("前端构建产物 %s 不是有效的非空文件", path)
	}
	return nil
}
