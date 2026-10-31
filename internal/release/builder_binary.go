package release

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// buildArtifact 编译单个平台的程序并生成对应压缩包，返回制品描述。
func buildArtifact(
	options BuildOptions,
	output string,
	metadata Metadata,
	platform Platform,
	staging string,
	content docs,
	stderr io.Writer,
) (Artifact, error) {
	binaryName := BinaryName(platform.OS)
	binaryPath := filepath.Join(staging, platform.OS+"_"+platform.Arch, binaryName)
	platformMetadata := metadata
	platformMetadata.OS = platform.OS
	platformMetadata.Arch = platform.Arch
	if err := buildBinary(options.RepoRoot, platform, platformMetadata, binaryPath, stderr); err != nil {
		return Artifact{}, err
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return Artifact{}, fmt.Errorf("读取编译产物失败: %w", err)
	}
	entries, err := archiveEntries(platformMetadata, binary, content)
	if err != nil {
		return Artifact{}, err
	}
	format := ArchiveFormat(platform.OS)
	archivePath := filepath.Join(output, ArtifactName(metadata.Version, platform))
	if err := writeArchiveFile(archivePath, format, entries); err != nil {
		return Artifact{}, err
	}
	if err := CheckArchiveLayout(archivePath, format, platform); err != nil {
		return Artifact{}, err
	}
	checksum, size, err := SHA256File(archivePath)
	if err != nil {
		return Artifact{}, fmt.Errorf("计算制品校验和失败: %w", err)
	}
	return Artifact{
		File:   filepath.Base(archivePath),
		OS:     platform.OS,
		Arch:   platform.Arch,
		Format: format,
		Binary: binaryName,
		SHA256: checksum,
		Size:   size,
	}, nil
}

// buildBinary 为单个目标平台编译单文件程序。
//
// 目标平台通过环境变量声明，并显式关闭 CGO：发布制品不得依赖目标机器上恰好装了
// 某个 C 库，也不允许"CI 环境偶然编译成功"成为支持某平台的依据。
func buildBinary(
	repoRoot string,
	platform Platform,
	metadata Metadata,
	outputPath string,
	stderr io.Writer,
) error {
	ldflags := strings.Join([]string{
		"-s",
		"-w",
		"-X main.version=" + metadata.Version,
		"-X main.commit=" + metadata.Commit,
		"-X main.buildDate=" + metadata.BuildDate,
	}, " ")
	command := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", outputPath, agentflowPackage)
	command.Dir = repoRoot
	command.Env = append(
		os.Environ(),
		"GOOS="+platform.OS,
		"GOARCH="+platform.Arch,
		"CGO_ENABLED=0",
	)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("执行 go build 失败: %w", err)
	}
	return nil
}

// archiveEntries 按固定顺序组装压缩包内容：可执行文件 → 快速启动说明 →
// 第三方声明 → 版本元数据。
func archiveEntries(metadata Metadata, binary []byte, content docs) ([]Entry, error) {
	metadataJSON, err := metadata.JSON()
	if err != nil {
		return nil, fmt.Errorf("序列化版本元数据失败: %w", err)
	}
	return []Entry{
		{
			Name: BinaryName(metadata.OS),
			Data: binary,
			Mode: 0o755,
		},
		{
			Name: QuickstartFileName,
			Data: content.quickstart,
			Mode: 0o644,
		},
		{
			Name: NoticesFileName,
			Data: content.notices,
			Mode: 0o644,
		},
		{
			Name: MetadataFileName,
			Data: metadataJSON,
			Mode: 0o644,
		},
	}, nil
}

// writeArchiveFile 把条目写入压缩包文件。
func writeArchiveFile(path string, format string, entries []Entry) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建压缩包 %s 失败: %w", path, err)
	}
	if err := WriteArchive(file, format, entries); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭压缩包 %s 失败: %w", path, err)
	}
	return nil
}
