package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/lingting/agentflow/internal/release"
)

// buildRelease 把命令行参数转换成发布构建输入并执行。
//
// 具体流程（前端校验 → 交叉编译 → 打包 → 校验文件 → 自检）全部位于
// internal/release，测试与本地入口共用同一份实现，不会出现"CI 一套、本地一套"。
func buildRelease(opts *options, stdout, stderr io.Writer) error {
	if err := release.ValidateVersion(opts.version); err != nil {
		return err
	}
	platforms, err := parsePlatforms(opts.platforms)
	if err != nil {
		return err
	}
	_, err = release.BuildDistribution(release.BuildOptions{
		RepoRoot:       opts.repoRoot,
		OutputDir:      opts.output,
		WebDistDir:     opts.webDist,
		QuickstartPath: opts.quickstart,
		NoticesPath:    opts.notices,
		Platforms:      platforms,
		Version:        opts.version,
		Commit:         opts.commit,
		BuildDate:      opts.buildDate,
		GoVersion:      opts.goVersion,
	}, stdout, stderr)
	return err
}

// parsePlatforms 解析逗号分隔的目标平台列表，留空时返回空切片表示使用发布矩阵。
//
// 不在发布矩阵内的平台一律拒绝：能交叉编译不等于产品支持该平台。
func parsePlatforms(value string) ([]release.Platform, error) {
	if value == "" {
		return nil, nil
	}
	fields := splitAndTrim(value)
	platforms := make([]release.Platform, 0, len(fields))
	for _, field := range fields {
		platform, err := release.ParsePlatform(field)
		if err != nil {
			return nil, err
		}
		if !release.IsSupported(platform) {
			return nil, fmt.Errorf("目标平台 %s 不在发布矩阵内", platform)
		}
		platforms = append(platforms, platform)
	}
	return platforms, nil
}

// splitAndTrim 按逗号切分并去掉空白的空项。
func splitAndTrim(value string) []string {
	var parts []string
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field != "" {
			parts = append(parts, field)
		}
	}
	return parts
}
