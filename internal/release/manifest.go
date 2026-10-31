package release

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// gitCommitPattern 是完整 Git 提交 SHA 的格式。
var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidCommit 判断取值是否为完整的 Git 提交 SHA。
//
// 只接受完整 SHA：短 SHA 无法唯一指向源码，"none"/"unknown" 之类的占位值则会让
// 制品看起来有构建信息而实际无从核对。
func ValidCommit(commit string) bool {
	return gitCommitPattern.MatchString(commit)
}

// ManifestFileName 是发布制品汇总清单文件名。
const ManifestFileName = "release-manifest.json"

// ChecksumFileName 是 SHA-256 校验文件名。
const ChecksumFileName = "SHA256SUMS"

// Artifact 描述单个发布制品。
type Artifact struct {
	// File 是制品文件名。
	File string `json:"file"`
	// OS 是制品目标操作系统。
	OS string `json:"os"`
	// Arch 是制品目标架构。
	Arch string `json:"arch"`
	// Format 是压缩包格式。
	Format string `json:"format"`
	// Binary 是压缩包内可执行文件名。
	Binary string `json:"binary"`
	// SHA256 是制品文件的 SHA-256 摘要。
	SHA256 string `json:"sha256"`
	// Size 是制品文件字节数。
	Size int64 `json:"size"`
}

// Platform 返回制品的目标平台。
func (a Artifact) Platform() Platform {
	return Platform{OS: a.OS, Arch: a.Arch}
}

// Manifest 是发布制品的汇总清单（release-manifest.json）。
//
// 清单只记录构建期确定、不含凭据与本地路径的字段：不含仓库绝对路径、环境变量、
// 令牌、私钥路径或测试配置。
type Manifest struct {
	// Version 是版本号。
	Version string `json:"version"`
	// Commit 是实际参与构建的提交 SHA。
	Commit string `json:"commit"`
	// BuildDate 是 UTC 构建时间。
	BuildDate string `json:"build_date"`
	// GoVersion 是实际使用的 Go 工具链版本。
	GoVersion string `json:"go_version"`
	// Artifacts 是本次生成的制品列表。
	Artifacts []Artifact `json:"artifacts"`
}

// Metadata 返回某个制品的版本元数据。
func (m Manifest) Metadata(platform Platform) Metadata {
	return Metadata{
		Version:   m.Version,
		Commit:    m.Commit,
		BuildDate: m.BuildDate,
		GoVersion: m.GoVersion,
		OS:        platform.OS,
		Arch:      platform.Arch,
	}
}

// Validate 校验清单与其中每个制品的一致性。
//
// 它同时充当"发布制品自检"的规则来源：文件名必须与命名规范一致、每个平台只能
// 出现一次、摘要必须是合法的 SHA-256、大小必须为正数。
func (m Manifest) Validate() error {
	header := Metadata{Version: m.Version, Commit: m.Commit, BuildDate: m.BuildDate, GoVersion: m.GoVersion}
	if err := header.Validate(); err != nil {
		return err
	}
	if len(m.Artifacts) == 0 {
		return fmt.Errorf("发布清单没有任何制品")
	}
	seen := make(map[Platform]struct{}, len(m.Artifacts))
	for index, artifact := range m.Artifacts {
		platform := artifact.Platform()
		if !IsSupported(platform) {
			return fmt.Errorf("第 %d 个制品的目标平台 %s 不在发布矩阵内", index+1, platform)
		}
		if _, duplicate := seen[platform]; duplicate {
			return fmt.Errorf("目标平台 %s 出现了多个制品", platform)
		}
		seen[platform] = struct{}{}
		expected := ArtifactName(m.Version, platform)
		if artifact.File != expected {
			return fmt.Errorf("制品文件名 %q 与命名规范不一致，应为 %q", artifact.File, expected)
		}
		if artifact.Format != ArchiveFormat(platform.OS) {
			return fmt.Errorf("制品 %s 的格式 %q 与目标平台不一致", artifact.File, artifact.Format)
		}
		if artifact.Binary != BinaryName(platform.OS) {
			return fmt.Errorf("制品 %s 的可执行文件名 %q 与目标平台不一致", artifact.File, artifact.Binary)
		}
		if !ValidChecksum(artifact.SHA256) {
			return fmt.Errorf("制品 %s 的 SHA-256 摘要无效", artifact.File)
		}
		if artifact.Size <= 0 {
			return fmt.Errorf("制品 %s 的大小无效: %d", artifact.File, artifact.Size)
		}
	}
	return nil
}

// Checksums 返回 SHA256SUMS 内容：每行 "摘要  文件名"，按文件名排序。
//
// 格式与 sha256sum / shasum 的校验输入一致，用户可以在下载目录直接执行
// `sha256sum -c SHA256SUMS`（macOS 用 `shasum -a 256 -c SHA256SUMS`）。
func (m Manifest) Checksums() string {
	sorted := make([]Artifact, len(m.Artifacts))
	copy(sorted, m.Artifacts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].File < sorted[j].File })
	lines := make([]string, 0, len(sorted))
	for _, artifact := range sorted {
		lines = append(lines, fmt.Sprintf("%s  %s", artifact.SHA256, artifact.File))
	}
	return strings.Join(lines, "\n") + "\n"
}

// JSON 返回缩进后的清单 JSON，末尾带换行。
func (m Manifest) JSON() ([]byte, error) {
	return marshalIndent(m)
}
