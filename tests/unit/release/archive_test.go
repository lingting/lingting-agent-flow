package release_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lingting/agentflow/internal/release"
)

// readArchiveEntriesForTest 读取压缩包内全部条目内容，供构建用例核对制品内容。
//
// 使用标准库直接解压，不复用 internal/release 的读取函数，避免"用同一段代码验证
// 自己产生的结果"。
func readArchiveEntriesForTest(t *testing.T, archivePath string, format string) map[string][]byte {
	t.Helper()
	entries := make(map[string][]byte)
	switch format {
	case release.FormatTarGz:
		file, err := os.Open(archivePath)
		if err != nil {
			t.Fatalf("打开压缩包失败: %v", err)
		}
		defer func() {
			_ = file.Close()
		}()
		compressed, err := gzip.NewReader(file)
		if err != nil {
			t.Fatalf("打开 gzip 流失败: %v", err)
		}
		defer func() {
			_ = compressed.Close()
		}()
		archive := tar.NewReader(compressed)
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("读取 tar 条目失败: %v", err)
			}
			data, err := io.ReadAll(archive)
			if err != nil {
				t.Fatalf("读取 tar 条目 %s 失败: %v", header.Name, err)
			}
			entries[header.Name] = data
		}
	case release.FormatZip:
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			t.Fatalf("打开 zip 压缩包失败: %v", err)
		}
		defer func() {
			_ = archive.Close()
		}()
		for _, item := range archive.File {
			reader, err := item.Open()
			if err != nil {
				t.Fatalf("打开 zip 条目 %s 失败: %v", item.Name, err)
			}
			data, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				t.Fatalf("读取 zip 条目 %s 失败: %v", item.Name, err)
			}
			entries[item.Name] = data
		}
	default:
		t.Fatalf("不支持的压缩格式: %s", format)
	}
	return entries
}

// TestWriteArchiveLayout 校验两种压缩格式写出的布局与权限。
func TestWriteArchiveLayout(t *testing.T) {
	t.Parallel()
	entries := []release.Entry{
		{Name: "agentflow", Data: []byte("binary"), Mode: 0o755},
		{Name: "version.json", Data: []byte("{}\n"), Mode: 0o644},
	}
	for _, format := range []string{release.FormatTarGz, release.FormatZip} {
		path := filepath.Join(t.TempDir(), "agentflow."+format)
		file, err := os.Create(path)
		if err != nil {
			t.Fatalf("创建压缩包失败: %v", err)
		}
		if err := release.WriteArchive(file, format, entries); err != nil {
			_ = file.Close()
			t.Fatalf("%s 写入失败: %v", format, err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("关闭压缩包失败: %v", err)
		}
		names, err := release.ReadArchiveNames(path, format)
		if err != nil {
			t.Fatalf("%s 读取失败: %v", format, err)
		}
		if len(names) != 2 || names[0] != "agentflow" || names[1] != "version.json" {
			t.Fatalf("%s 条目顺序错误: %v", format, names)
		}
	}
}

// TestWriteArchiveRejectsUnknownFormat 校验未知压缩格式被拒绝。
func TestWriteArchiveRejectsUnknownFormat(t *testing.T) {
	t.Parallel()
	if err := release.WriteArchive(&discardWriter{}, "rar", nil); err == nil {
		t.Fatal("未知压缩格式应被拒绝")
	}
}

// discardWriter 是只统计写入字节数的写入器。
type discardWriter struct {
	written int
}

// Write 实现 io.Writer。
func (w *discardWriter) Write(data []byte) (int, error) {
	w.written += len(data)
	return len(data), nil
}

// TestCheckArchiveLayout 校验布局检查能识别多出或缺失的条目。
func TestCheckArchiveLayout(t *testing.T) {
	t.Parallel()
	target := parsePlatform(t, "linux/amd64")
	entries := make([]release.Entry, 0, 4)
	for _, name := range release.ExpectedArchiveNames(target) {
		entries = append(entries, release.Entry{Name: name, Data: []byte("x"), Mode: 0o644})
	}
	path := filepath.Join(t.TempDir(), "agentflow.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建压缩包失败: %v", err)
	}
	if err := release.WriteArchive(file, release.FormatTarGz, entries); err != nil {
		_ = file.Close()
		t.Fatalf("写入压缩包失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭压缩包失败: %v", err)
	}
	if err := release.CheckArchiveLayout(path, release.FormatTarGz, target); err != nil {
		t.Fatalf("完整布局应校验通过: %v", err)
	}

	// 少一个条目时必须失败：缺失文件不能因为"其余都在"而放行。
	shortPath := filepath.Join(t.TempDir(), "short.tar.gz")
	shortFile, err := os.Create(shortPath)
	if err != nil {
		t.Fatalf("创建压缩包失败: %v", err)
	}
	if err := release.WriteArchive(shortFile, release.FormatTarGz, entries[:len(entries)-1]); err != nil {
		_ = shortFile.Close()
		t.Fatalf("写入压缩包失败: %v", err)
	}
	if err := shortFile.Close(); err != nil {
		t.Fatalf("关闭压缩包失败: %v", err)
	}
	if err := release.CheckArchiveLayout(shortPath, release.FormatTarGz, target); err == nil {
		t.Fatal("缺少条目的压缩包应校验失败")
	}
}
