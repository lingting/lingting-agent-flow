package release

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AttestationProviderGitHub 是 GitHub Actions OIDC 无密钥来源证明的提供方标识。
//
// 正式发布只认可这一种来源证明：它由 GitHub 依据本次工作流运行签发给制品的摘要，
// 用户可以用官方 CLI 核对"制品是否真的由该仓库的该工作流在对应提交上构建"。
const AttestationProviderGitHub = "github-artifact-attestation"

// 来源字段的格式约束。填写了就必须是真实取值：占位值会让"有来源信息"变成一句空话。
var (
	// sourceRepositoryPattern 是来源仓库字段的格式（owner/name）。
	sourceRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// sourceWorkflowPattern 是来源工作流文件路径的格式。
	sourceWorkflowPattern = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml$`)
	// sourceRunIDPattern 是工作流运行标识的格式。
	sourceRunIDPattern = regexp.MustCompile(`^[0-9]+$`)
)

// ValidateSource 校验清单里的来源信息字段。
//
// 字段是可选的（本地 `make release-dist` 没有 CI 运行上下文），但一旦填写就必须合法：
// 这部分信息会进入正式 Release 的说明与审计记录。
func (m Manifest) ValidateSource() error {
	if m.SourceRepository != "" && !sourceRepositoryPattern.MatchString(m.SourceRepository) {
		return fmt.Errorf("来源仓库 %q 无效：必须为 owner/name", m.SourceRepository)
	}
	if m.SourceWorkflow != "" && !sourceWorkflowPattern.MatchString(m.SourceWorkflow) {
		return fmt.Errorf("来源工作流 %q 无效：必须为 .github/workflows 下的 .yml 或 .yaml 文件", m.SourceWorkflow)
	}
	if m.SourceRunID != "" && !sourceRunIDPattern.MatchString(m.SourceRunID) {
		return fmt.Errorf("来源运行标识 %q 无效：必须为纯数字", m.SourceRunID)
	}
	if m.AttestationProvider != "" && m.AttestationProvider != AttestationProviderGitHub {
		return fmt.Errorf("来源证明提供方 %q 不受支持：只接受 %q",
			m.AttestationProvider, AttestationProviderGitHub)
	}
	return nil
}

// RequireProvenance 校验正式发布必须具备的来源信息。
//
// 发布流程在生成来源证明之后调用它：缺少仓库、工作流、运行标识或证明提供方时，
// 清单无法与构建流程建立可核对的关系，必须拒绝发布，不得降级为"没有证明的普通制品"。
func (m Manifest) RequireProvenance() error {
	if err := m.ValidateSource(); err != nil {
		return err
	}
	missing := make([]string, 0, 4)
	if m.SourceRepository == "" {
		missing = append(missing, "source_repository")
	}
	if m.SourceWorkflow == "" {
		missing = append(missing, "source_workflow")
	}
	if m.SourceRunID == "" {
		missing = append(missing, "source_run_id")
	}
	if m.AttestationProvider == "" {
		missing = append(missing, "attestation_provider")
	}
	if len(missing) > 0 {
		return fmt.Errorf("发布清单缺少来源信息 %s：没有来源证明的制品不得作为正式发布",
			strings.Join(missing, "、"))
	}
	return nil
}

// VerifyPlatformCoverage 要求清单恰好覆盖发布矩阵中的每个平台。
//
// 只校验"不重复、不越界"还不够：少了一个平台同样会让产物不完整，而这种遗漏在上传
// 阶段不会自己暴露出来。
func (m Manifest) VerifyPlatformCoverage() error {
	covered := make(map[Platform]struct{}, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		covered[artifact.Platform()] = struct{}{}
	}
	missing := make([]string, 0, len(SupportedPlatforms))
	for _, platform := range SupportedPlatforms {
		if _, ok := covered[platform]; !ok {
			missing = append(missing, platform.String())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("发布清单缺少必需平台的制品：%s", strings.Join(missing, "、"))
	}
	return nil
}

// ReleaseAssetNames 返回正式 Release 必须发布的资产名（已排序）。
//
// 资产集合只由清单派生：各平台压缩包、SHA256SUMS 与 release-manifest.json。
// 不包含测试报告、日志、配置文件或本地路径产物。
func (m Manifest) ReleaseAssetNames() []string {
	names := make([]string, 0, len(m.Artifacts)+2)
	for _, artifact := range m.Artifacts {
		names = append(names, artifact.File)
	}
	names = append(names, ChecksumFileName, ManifestFileName)
	sort.Strings(names)
	return names
}

// ReadManifest 读入并校验目录下的发布清单。
func ReadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, ManifestFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("读取发布清单失败: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("解析发布清单失败: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("发布清单校验失败: %w", err)
	}
	return manifest, nil
}

// ReleaseVerifyOptions 是一次发布目录核验的输入。
type ReleaseVerifyOptions struct {
	// Dir 是发布制品目录，必须同时包含压缩包、SHA256SUMS 与 release-manifest.json。
	Dir string
	// Version 是期望的版本号；正式发布必须与 Git 标签完全一致。
	Version string
	// Commit 是期望的完整提交 SHA；留空表示不核对（仅用于本地核对）。
	Commit string
	// RequireProvenance 为真时要求清单声明来源信息，供生成来源证明之后核验。
	RequireProvenance bool
}

// VerifyRelease 核验发布目录，返回读入的清单。
//
// 核验项：清单结构、版本与提交一致性、平台覆盖、校验文件内容、每个制品的摘要与
// 压缩包布局、目录中不存在本次发布之外的任何文件。任何一项不通过都必须让调用方
// 以非零结果失败：发布流程不得依赖"上传命令返回成功"判断资产完整。
func VerifyRelease(options ReleaseVerifyOptions) (Manifest, error) {
	if err := ValidateVersion(options.Version); err != nil {
		return Manifest{}, err
	}
	manifest, err := ReadManifest(options.Dir)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Version != options.Version {
		return Manifest{}, fmt.Errorf("发布清单版本 %q 与期望的版本 %q 不一致",
			manifest.Version, options.Version)
	}
	if options.Commit != "" && manifest.Commit != options.Commit {
		return Manifest{}, fmt.Errorf("发布清单提交号 %q 与期望的提交 %q 不一致",
			manifest.Commit, options.Commit)
	}
	if err := manifest.VerifyPlatformCoverage(); err != nil {
		return Manifest{}, err
	}
	checksums, err := os.ReadFile(filepath.Join(options.Dir, ChecksumFileName))
	if err != nil {
		return Manifest{}, fmt.Errorf("读取校验文件失败: %w", err)
	}
	if string(checksums) != manifest.Checksums() {
		return Manifest{}, fmt.Errorf("校验文件 %s 与发布清单不一致", ChecksumFileName)
	}
	for _, artifact := range manifest.Artifacts {
		if err := verifyReleaseArtifact(options.Dir, artifact); err != nil {
			return Manifest{}, err
		}
	}
	if err := verifyReleaseDirEntries(options.Dir, manifest.ReleaseAssetNames()); err != nil {
		return Manifest{}, err
	}
	if options.RequireProvenance {
		if err := manifest.RequireProvenance(); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}

// verifyReleaseArtifact 核验单个制品的摘要、体积与压缩包布局。
func verifyReleaseArtifact(dir string, artifact Artifact) error {
	path := filepath.Join(dir, artifact.File)
	digest, size, err := SHA256File(path)
	if err != nil {
		return fmt.Errorf("读取制品 %s 失败: %w", artifact.File, err)
	}
	if digest != artifact.SHA256 {
		return fmt.Errorf("制品 %s 的 SHA-256 与清单不一致：清单 %s，实际 %s",
			artifact.File, artifact.SHA256, digest)
	}
	if size != artifact.Size {
		return fmt.Errorf("制品 %s 的大小与清单不一致：清单 %d，实际 %d",
			artifact.File, artifact.Size, size)
	}
	return CheckArchiveLayout(path, artifact.Format, artifact.Platform())
}

// verifyReleaseDirEntries 要求目录中只有本次发布声明的资产。
//
// 发布目录里多出来的文件（测试报告、日志、配置、临时产物）一旦被上传，就会成为
// 无法解释来源的 Release 资产，因此必须在发布之前挡住。
func verifyReleaseDirEntries(dir string, expected []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取发布目录失败: %w", err)
	}
	allowed := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		allowed[name] = struct{}{}
	}
	unexpected := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			unexpected = append(unexpected, name+"/")
			continue
		}
		if _, ok := allowed[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("发布目录中存在不属于本次发布的文件：%s", strings.Join(unexpected, "、"))
	}
	return nil
}

// VerifyReleaseAssets 校验远端 Release 的实际资产列表与清单完全一致。
//
// 只比较名称而不是只比较数量：缺一个、多一个或重名都会让"这个 Release 就是这个
// 版本的全部制品"这一结论不成立。
func VerifyReleaseAssets(manifest Manifest, assets []string) error {
	expected := manifest.ReleaseAssetNames()
	allowed := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		allowed[name] = struct{}{}
	}
	counts := make(map[string]int, len(assets))
	unexpected := make([]string, 0, len(assets))
	for _, name := range assets {
		counts[name]++
		if _, ok := allowed[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	duplicates := make([]string, 0)
	missing := make([]string, 0, len(expected))
	for _, name := range expected {
		switch count := counts[name]; {
		case count == 0:
			missing = append(missing, name)
		case count > 1:
			duplicates = append(duplicates, name)
		}
	}
	if len(missing) == 0 && len(duplicates) == 0 && len(unexpected) == 0 {
		return nil
	}
	problems := make([]string, 0, 3)
	if len(missing) > 0 {
		sort.Strings(missing)
		problems = append(problems, "缺少资产 "+strings.Join(missing, "、"))
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		problems = append(problems, "资产重名 "+strings.Join(duplicates, "、"))
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		problems = append(problems, "存在未声明的资产 "+strings.Join(unexpected, "、"))
	}
	return fmt.Errorf("Release 资产与发布清单不一致：%s", strings.Join(problems, "；"))
}
