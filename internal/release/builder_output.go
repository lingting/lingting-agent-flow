package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeManifestFiles 写出 SHA256SUMS 与 release-manifest.json。
//
// 两个文件的内容都只由 Manifest 派生，保证"清单里的摘要"与"校验文件里的摘要"
// 不可能出现两套来源。
func writeManifestFiles(output string, manifest Manifest) error {
	data, err := manifest.JSON()
	if err != nil {
		return fmt.Errorf("序列化发布清单失败: %w", err)
	}
	manifestPath := filepath.Join(output, ManifestFileName)
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return fmt.Errorf("写入发布清单失败: %w", err)
	}
	checksumPath := filepath.Join(output, ChecksumFileName)
	if err := os.WriteFile(checksumPath, []byte(manifest.Checksums()), 0o644); err != nil {
		return fmt.Errorf("写入校验文件失败: %w", err)
	}
	return nil
}

// verifyArtifacts 回读输出目录，核对清单、校验文件与压缩包本身。
//
// 生成之后必须重新读取并校验，而不是直接相信构建过程中的返回值：清单与校验文件
// 是用户核对下载内容的唯一依据，写错一个字就等于没有校验能力。
func verifyArtifacts(output string, manifest Manifest) error {
	manifestPath := filepath.Join(output, ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("回读发布清单失败: %w", err)
	}
	var written Manifest
	if err := json.Unmarshal(data, &written); err != nil {
		return fmt.Errorf("解析发布清单失败: %w", err)
	}
	if err := written.Validate(); err != nil {
		return fmt.Errorf("回读的发布清单校验失败: %w", err)
	}
	if len(written.Artifacts) != len(manifest.Artifacts) {
		return fmt.Errorf("回读的发布清单制品数量不一致：期望 %d，实际 %d",
			len(manifest.Artifacts), len(written.Artifacts))
	}
	checksumPath := filepath.Join(output, ChecksumFileName)
	checksums, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("回读校验文件失败: %w", err)
	}
	if !bytes.Equal(checksums, []byte(written.Checksums())) {
		return fmt.Errorf("校验文件 %s 与发布清单不一致", checksumPath)
	}
	for _, artifact := range written.Artifacts {
		path := filepath.Join(output, artifact.File)
		if err := VerifyChecksum(path, artifact.SHA256); err != nil {
			return err
		}
		_, size, err := SHA256File(path)
		if err != nil {
			return fmt.Errorf("读取制品 %s 失败: %w", artifact.File, err)
		}
		if size != artifact.Size {
			return fmt.Errorf("制品 %s 的大小与清单不一致：清单 %d，实际 %d", artifact.File, artifact.Size, size)
		}
		if err := CheckArchiveLayout(path, artifact.Format, artifact.Platform()); err != nil {
			return err
		}
	}
	return nil
}

// printSummary 把发布结果写入标准输出。
//
// 摘要只包含版本元数据、制品名称、体积与校验和，不含仓库绝对路径、环境变量或
// 凭据：它的内容会进入 CI 日志。
func printSummary(w io.Writer, manifest Manifest, output string) error {
	if _, err := fmt.Fprintf(w, "版本: %s\n提交: %s\n构建时间: %s\nGo: %s\n输出目录: %s\n",
		manifest.Version,
		manifest.Commit,
		manifest.BuildDate,
		manifest.GoVersion,
		filepath.Base(output),
	); err != nil {
		return err
	}
	for _, artifact := range manifest.Artifacts {
		if _, err := fmt.Fprintf(w, "制品: %s  %s  %d 字节\n", artifact.File, artifact.SHA256, artifact.Size); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "校验文件: %s\n清单文件: %s\n", ChecksumFileName, ManifestFileName)
	return err
}
