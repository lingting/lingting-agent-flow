package cli

import (
	"fmt"
	"strings"

	"github.com/lingting/agentflow/internal/app"
	"github.com/lingting/agentflow/internal/domain"
)

// printEnvironmentSummary 输出环境概览。
func printEnvironmentSummary(a *app.App, summary app.EnvironmentSummary) error {
	lines := []string{
		fmt.Sprintf("环境: %s", summary.Ref.Label()),
		fmt.Sprintf("最新版本: %s", emptyAsUnknown(summary.LatestVersion)),
		fmt.Sprintf("当前可用版本: %s", emptyAsUnknown(summary.CurrentVersion)),
		"版本明细:",
	}
	rows, err := versionRows(summary.Versions)
	if err != nil {
		return err
	}
	lines = append(lines, rows...)
	_, err = fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// printVersionTable 输出历史版本表格。
func printVersionTable(a *app.App, versions []domain.EnvironmentVersion) error {
	if len(versions) == 0 {
		_, err := fmt.Fprintln(a.Stdout(), "该环境尚无版本。")
		return err
	}
	header := "版本\t状态\tDockerfile SHA256\t构建配置 SHA256\t镜像 ID\t构建时间"
	rows, err := versionRows(versions)
	if err != nil {
		return err
	}
	lines := append([]string{header}, rows...)
	_, err = fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// versionRows 生成版本明细行。
func versionRows(versions []domain.EnvironmentVersion) ([]string, error) {
	rows := make([]string, 0, len(versions))
	for _, version := range versions {
		builtAt := "（未构建）"
		if version.BuiltAt != nil {
			builtAt = version.BuiltAt.Format(timeFormat)
		}
		rows = append(rows, fmt.Sprintf(
			"%s\t%s\t%s\t%s\t%s\t%s",
			version.Label(),
			version.Status,
			shortHash(version.DockerfileSHA256),
			shortHash(version.BuildConfigSHA256),
			emptyAsUnknown(shortImageID(version.ImageID)),
			builtAt,
		))
	}
	return rows, nil
}

// printBuildResult 输出构建结果。
func printBuildResult(a *app.App, result app.BuildResult) error {
	version := result.Version
	lines := []string{
		fmt.Sprintf("环境: %s", version.Ref.Label()),
		fmt.Sprintf("版本: %s", version.Label()),
		fmt.Sprintf("构建状态: %s", version.Status),
		fmt.Sprintf("镜像标签: %s", emptyAsUnknown(version.ImageRef)),
		fmt.Sprintf("镜像 ID: %s", emptyAsUnknown(version.ImageID)),
		fmt.Sprintf("Dockerfile SHA256: %s", emptyAsUnknown(version.DockerfileSHA256)),
		fmt.Sprintf("构建配置 SHA256: %s", emptyAsUnknown(version.BuildConfigSHA256)),
	}
	if result.Cached {
		lines = append(lines, "结果: 复用已有成功构建（已核验镜像仍存在）")
	}
	if result.LogPath != "" {
		lines = append(lines, fmt.Sprintf("远程构建日志: %s", result.LogPath))
	}
	if version.ErrorSummary != "" {
		lines = append(lines, fmt.Sprintf("失败摘要: %s", version.ErrorSummary))
	}
	if result.MetadataWarning != "" {
		lines = append(lines, fmt.Sprintf("元数据告警: %s", result.MetadataWarning))
	}
	if result.Build.LogTail != "" {
		lines = append(lines, "构建日志末尾:", indentBlock(result.Build.LogTail))
	}
	_, err := fmt.Fprintln(a.Stdout(), strings.Join(lines, "\n"))
	return err
}

// indentBlock 给多行文本加缩进，便于与摘要字段区分。
func indentBlock(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for index, line := range lines {
		lines[index] = "    " + line
	}
	return strings.Join(lines, "\n")
}

// ensureTrailingNewline 确保输出内容以换行结束。
func ensureTrailingNewline(text string) string {
	if strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

// shortHash 返回哈希的前 12 位，便于表格展示。
func shortHash(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return emptyAsUnknown(value)
}

// shortImageID 返回镜像 ID 的摘要形式。
func shortImageID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if index := strings.LastIndex(trimmed, ":"); index >= 0 && len(trimmed) > index+13 {
		return trimmed[:index+1] + trimmed[index+1:index+13]
	}
	return trimmed
}

// formatBytes 把字节数格式化为可读文本。
func formatBytes(value int64) string {
	if value <= 0 {
		return "（未知）"
	}
	const (
		kib = int64(1024)
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case value >= gib:
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	case value >= mib:
		return fmt.Sprintf("%.1f MiB", float64(value)/float64(mib))
	case value >= kib:
		return fmt.Sprintf("%.1f KiB", float64(value)/float64(kib))
	default:
		return fmt.Sprintf("%d B", value)
	}
}
