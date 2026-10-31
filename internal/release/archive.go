package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// 压缩包内除可执行文件外的固定文件。
const (
	// QuickstartFileName 是快速启动说明文件名。
	QuickstartFileName = "QUICKSTART.md"
	// NoticesFileName 是第三方声明与许可证文件名。
	NoticesFileName = "THIRD-PARTY-NOTICES.md"
)

// Entry 是压缩包内的单个文件。
type Entry struct {
	// Name 是包内相对路径，只允许扁平文件名或正斜杠分隔的多级路径。
	Name string
	// Data 是文件内容。
	Data []byte
	// Mode 是文件权限位。
	Mode os.FileMode
}

// WriteArchive 以指定格式把文件写入压缩包。
//
// tar.gz 保留文件权限位（可执行文件必须是 0755）；zip 同样记录权限位，解压工具是否
// 使用它由目标平台决定。条目顺序按调用顺序写入，不做额外排序：调用方按
// 可执行文件 → 说明文件 → 元数据的固定顺序组织布局，便于人工核对。
func WriteArchive(w io.Writer, format string, entries []Entry) error {
	switch format {
	case FormatTarGz:
		return writeTarGz(w, entries)
	case FormatZip:
		return writeZip(w, entries)
	default:
		return fmt.Errorf("不支持的压缩格式 %q", format)
	}
}

// writeTarGz 写入 tar.gz 压缩包。
func writeTarGz(w io.Writer, entries []Entry) error {
	compressed := gzip.NewWriter(w)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.Name,
			Mode:     int64(entry.Mode.Perm()),
			Size:     int64(len(entry.Data)),
			Typeflag: tar.TypeReg,
			Format:   tar.FormatPAX,
		}
		if header.Mode == 0 {
			header.Mode = 0o644
		}
		if err := archive.WriteHeader(header); err != nil {
			return fmt.Errorf("写入压缩包条目 %s 失败: %w", entry.Name, err)
		}
		if _, err := archive.Write(entry.Data); err != nil {
			return fmt.Errorf("写入压缩包内容 %s 失败: %w", entry.Name, err)
		}
	}
	// 关闭顺序与打开顺序相反：先结束归档再结束压缩流，否则尾部数据会丢失。
	if err := archive.Close(); err != nil {
		return fmt.Errorf("结束 tar 归档失败: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("结束 gzip 压缩失败: %w", err)
	}
	return nil
}

// writeZip 写入 zip 压缩包。
func writeZip(w io.Writer, entries []Entry) error {
	archive := zip.NewWriter(w)
	for _, entry := range entries {
		mode := entry.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(mode)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("写入压缩包条目 %s 失败: %w", entry.Name, err)
		}
		if _, err := writer.Write(entry.Data); err != nil {
			return fmt.Errorf("写入压缩包内容 %s 失败: %w", entry.Name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("结束 zip 归档失败: %w", err)
	}
	return nil
}

// ReadArchiveNames 返回压缩包内的文件名列表（按包内顺序）。
//
// 生成端用它自检压缩包确实可读且布局正确；验收端（tests/e2e/packaging）用独立
// 的标准库代码解压，不复用本函数，避免"用同一段代码验证自己"。
func ReadArchiveNames(archivePath string, format string) ([]string, error) {
	switch format {
	case FormatTarGz:
		return readTarGzNames(archivePath)
	case FormatZip:
		return readZipNames(archivePath)
	default:
		return nil, fmt.Errorf("不支持的压缩格式 %q", format)
	}
}

// readTarGzNames 读取 tar.gz 压缩包内的文件名。
func readTarGzNames(archivePath string) ([]string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("打开 gzip 流失败: %w", err)
	}
	defer func() {
		_ = compressed.Close()
	}()
	archive := tar.NewReader(compressed)
	var names []string
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("读取 tar 条目失败: %w", err)
		}
		names = append(names, header.Name)
	}
}

// readZipNames 读取 zip 压缩包内的文件名。
func readZipNames(archivePath string) ([]string, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("打开 zip 压缩包失败: %w", err)
	}
	defer func() {
		_ = archive.Close()
	}()
	names := make([]string, 0, len(archive.File))
	for _, entry := range archive.File {
		names = append(names, entry.Name)
	}
	return names, nil
}

// ExpectedArchiveNames 返回某个平台制品压缩包应包含的文件名（已排序）。
func ExpectedArchiveNames(platform Platform) []string {
	names := []string{
		BinaryName(platform.OS),
		QuickstartFileName,
		NoticesFileName,
		MetadataFileName,
	}
	sort.Strings(names)
	return names
}

// CheckArchiveLayout 校验压缩包内的文件清单与预期布局完全一致。
func CheckArchiveLayout(archivePath string, format string, platform Platform) error {
	names, err := ReadArchiveNames(archivePath, format)
	if err != nil {
		return err
	}
	sorted := make([]string, 0, len(names))
	for _, name := range names {
		// 统一成路径形式，避免 Windows 上的构建产物混入反斜杠。
		sorted = append(sorted, path.Clean(strings.ReplaceAll(name, "\\", "/")))
	}
	sort.Strings(sorted)
	expected := ExpectedArchiveNames(platform)
	if strings.Join(sorted, "\n") != strings.Join(expected, "\n") {
		return fmt.Errorf(
			"压缩包 %s 的文件布局不符合预期：期望 %v，实际 %v",
			path.Base(archivePath),
			expected,
			sorted,
		)
	}
	return nil
}
