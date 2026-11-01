package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/release"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// releaseFixtureOptions 描述一套发布目录 fixture 的构造输入。
type releaseFixtureOptions struct {
	// version 是清单版本号。
	version string
	// commit 是清单提交号。
	commit string
	// platforms 是参与构建的平台；留空时使用完整发布矩阵。
	platforms []release.Platform
	// provenance 为真时写入来源信息（正式发布必须具备）。
	provenance bool
}

// writeReleaseFixture 在目录中生成一套完整的发布资产：各平台压缩包、校验文件与清单。
//
// 压缩包由 internal/release 的导出接口真实写入，因此后续核验走的是"读回真实文件"
// 的路径，而不是对着一堆假路径做判断。
func writeReleaseFixture(t *testing.T, dir string, options releaseFixtureOptions) release.Manifest {
	t.Helper()
	platforms := options.platforms
	if len(platforms) == 0 {
		platforms = release.SupportedPlatforms
	}
	manifest := release.Manifest{
		Version:     options.version,
		Commit:      options.commit,
		BuildDate:   testBuildDate,
		GoVersion:   testGoVersion,
		NodeVersion: testNodeVersion,
		PnpmVersion: testPnpmVersion,
	}
	if options.provenance {
		manifest.SourceRepository = testRepository
		manifest.SourceWorkflow = testWorkflow
		manifest.SourceRunID = testRunID
		manifest.AttestationProvider = release.AttestationProviderGitHub
	}
	artifacts := make([]release.Artifact, 0, len(platforms))
	for _, platform := range platforms {
		artifacts = append(artifacts, writeReleaseArchive(t, dir, manifest, platform))
	}
	manifest.Artifacts = artifacts
	if err := manifest.Validate(); err != nil {
		t.Fatalf("构造的发布清单不合法: %v", err)
	}
	writeFixtureManifestFiles(t, dir, manifest)
	return manifest
}

// writeReleaseArchive 写出单个平台的压缩包并返回其制品描述。
func writeReleaseArchive(
	t *testing.T,
	dir string,
	manifest release.Manifest,
	platform release.Platform,
) release.Artifact {
	t.Helper()
	metadata, err := manifest.Metadata(platform).JSON()
	if err != nil {
		t.Fatalf("序列化制品元数据失败: %v", err)
	}
	format := release.ArchiveFormat(platform.OS)
	path := filepath.Join(dir, release.ArtifactName(manifest.Version, platform))
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建压缩包失败: %v", err)
	}
	entries := []release.Entry{
		{
			Name: release.BinaryName(platform.OS),
			Data: []byte("fixture-binary-" + platform.String()),
			Mode: 0o755,
		},
		{Name: release.QuickstartFileName, Data: []byte("# 快速启动\n"), Mode: 0o644},
		{Name: release.NoticesFileName, Data: []byte("# 第三方声明\n"), Mode: 0o644},
		{Name: release.MetadataFileName, Data: metadata, Mode: 0o644},
	}
	if err := release.WriteArchive(file, format, entries); err != nil {
		_ = file.Close()
		t.Fatalf("写入压缩包失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭压缩包失败: %v", err)
	}
	digest, size, err := release.SHA256File(path)
	if err != nil {
		t.Fatalf("计算制品摘要失败: %v", err)
	}
	return release.Artifact{
		File:   filepath.Base(path),
		OS:     platform.OS,
		Arch:   platform.Arch,
		Format: format,
		Binary: release.BinaryName(platform.OS),
		SHA256: digest,
		Size:   size,
	}
}

// writeFixtureManifestFiles 写出发布清单与校验文件。
func writeFixtureManifestFiles(t *testing.T, dir string, manifest release.Manifest) {
	t.Helper()
	data, err := manifest.JSON()
	if err != nil {
		t.Fatalf("序列化发布清单失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, release.ManifestFileName), data, 0o644); err != nil {
		t.Fatalf("写入发布清单失败: %v", err)
	}
	checksums := filepath.Join(dir, release.ChecksumFileName)
	if err := os.WriteFile(checksums, []byte(manifest.Checksums()), 0o644); err != nil {
		t.Fatalf("写入校验文件失败: %v", err)
	}
}

// TestVerifyReleaseAcceptsCompleteAssetSet 校验完整发布目录可以通过核验。
func TestVerifyReleaseAcceptsCompleteAssetSet(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	writeReleaseFixture(t, dir, releaseFixtureOptions{
		version:    testVersion,
		commit:     testCommit,
		provenance: true,
	})
	manifest, err := release.VerifyRelease(release.ReleaseVerifyOptions{
		Dir:               dir,
		Version:           testVersion,
		Commit:            testCommit,
		RequireProvenance: true,
	})
	if err != nil {
		t.Fatalf("完整发布目录应通过核验: %v", err)
	}
	if len(manifest.Artifacts) != len(release.SupportedPlatforms) {
		t.Fatalf("制品数量应为 %d，实际 %d", len(release.SupportedPlatforms), len(manifest.Artifacts))
	}
}

// TestVerifyReleaseRejectsInconsistentDirectory 校验发布目录的各类不一致都被拒绝。
//
// 覆盖遗漏资产、多余文件、摘要被改、校验文件被改、版本与提交不符、平台覆盖不完整
// 与缺少来源信息：任何一项都必须失败，否则"资产完整"就只能靠人眼看。
func TestVerifyReleaseRejectsInconsistentDirectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		option func() releaseFixtureOptions
		mutate func(t *testing.T, dir string, manifest release.Manifest)
		verify func() release.ReleaseVerifyOptions
	}{
		{
			name:   "缺少制品",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
				path := filepath.Join(dir, manifest.Artifacts[0].File)
				if err := os.Remove(path); err != nil {
					t.Fatalf("删除制品失败: %v", err)
				}
			},
			verify: defaultVerify,
		},
		{
			name:   "多出未声明的文件",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "test-report.log"), []byte("noise\n"), 0o644); err != nil {
					t.Fatalf("写入多余文件失败: %v", err)
				}
			},
			verify: defaultVerify,
		},
		{
			name:   "制品内容被篡改",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
				path := filepath.Join(dir, manifest.Artifacts[0].File)
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatalf("打开制品失败: %v", err)
				}
				if _, err := file.WriteString("tampered"); err != nil {
					_ = file.Close()
					t.Fatalf("改写制品失败: %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("关闭制品失败: %v", err)
				}
			},
			verify: defaultVerify,
		},
		{
			name:   "校验文件与清单不一致",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
				tampered := strings.Replace(manifest.Checksums(), manifest.Artifacts[0].SHA256,
					release.SHA256Hex([]byte("other")), 1)
				path := filepath.Join(dir, release.ChecksumFileName)
				if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
					t.Fatalf("改写校验文件失败: %v", err)
				}
			},
			verify: defaultVerify,
		},
		{
			name:   "缺少发布清单",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(dir, release.ManifestFileName)); err != nil {
					t.Fatalf("删除发布清单失败: %v", err)
				}
			},
			verify: defaultVerify,
		},
		{
			name:   "版本与清单不一致",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
			},
			verify: func() release.ReleaseVerifyOptions {
				return release.ReleaseVerifyOptions{Dir: "", Version: "v9.9.9", Commit: testCommit}
			},
		},
		{
			name:   "提交与清单不一致",
			option: defaultFixture,
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
			},
			verify: func() release.ReleaseVerifyOptions {
				return release.ReleaseVerifyOptions{
					Dir:     "",
					Version: testVersion,
					Commit:  "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c",
				}
			},
		},
		{
			name: "平台覆盖不完整",
			option: func() releaseFixtureOptions {
				return releaseFixtureOptions{
					version:   testVersion,
					commit:    testCommit,
					platforms: []release.Platform{{OS: "linux", Arch: "amd64"}},
				}
			},
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
			},
			verify: defaultVerify,
		},
		{
			name: "缺少来源信息",
			option: func() releaseFixtureOptions {
				return releaseFixtureOptions{version: testVersion, commit: testCommit}
			},
			mutate: func(t *testing.T, dir string, manifest release.Manifest) {
				t.Helper()
			},
			verify: func() release.ReleaseVerifyOptions {
				return release.ReleaseVerifyOptions{
					Dir:               "",
					Version:           testVersion,
					Commit:            testCommit,
					RequireProvenance: true,
				}
			},
		},
	}
	for _, testCase := range cases {
		dir := testkit.Dir(t)
		manifest := writeReleaseFixture(t, dir, testCase.option())
		testCase.mutate(t, dir, manifest)
		options := testCase.verify()
		options.Dir = dir
		if _, err := release.VerifyRelease(options); err == nil {
			t.Errorf("%s: 核验应失败", testCase.name)
		}
	}
}

// defaultFixture 返回使用完整发布矩阵与来源信息的 fixture 输入。
func defaultFixture() releaseFixtureOptions {
	return releaseFixtureOptions{version: testVersion, commit: testCommit, provenance: true}
}

// defaultVerify 返回针对完整发布矩阵的核验输入（目录由调用方填充）。
func defaultVerify() release.ReleaseVerifyOptions {
	return release.ReleaseVerifyOptions{
		Version:           testVersion,
		Commit:            testCommit,
		RequireProvenance: true,
	}
}

// TestVerifyReleaseAssets 校验远端资产列表与清单的一致性判定。
func TestVerifyReleaseAssets(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	manifest := writeReleaseFixture(t, dir, defaultFixture())
	expected := manifest.ReleaseAssetNames()
	if len(expected) != len(manifest.Artifacts)+2 {
		t.Fatalf("资产数量应为制品数加 2，实际 %d", len(expected))
	}
	if err := release.VerifyReleaseAssets(manifest, expected); err != nil {
		t.Fatalf("完整资产列表应通过核验: %v", err)
	}

	missing := expected[:len(expected)-1]
	if err := release.VerifyReleaseAssets(manifest, missing); err == nil {
		t.Error("缺少资产时应核验失败")
	}
	unexpected := append(append([]string{}, expected...), "debug.log")
	if err := release.VerifyReleaseAssets(manifest, unexpected); err == nil {
		t.Error("存在未声明资产时应核验失败")
	}
	duplicated := append(append([]string{}, expected...), expected[0])
	if err := release.VerifyReleaseAssets(manifest, duplicated); err == nil {
		t.Error("资产重名时应核验失败")
	}
	if err := release.VerifyReleaseAssets(manifest, nil); err == nil {
		t.Error("空资产列表时应核验失败")
	}
}

// TestReleaseAssetNames 校验资产名集合由清单派生且稳定有序。
func TestReleaseAssetNames(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	manifest := writeReleaseFixture(t, dir, defaultFixture())
	names := manifest.ReleaseAssetNames()
	for index, name := range names {
		if index > 0 && names[index-1] > name {
			t.Fatalf("资产名必须按文件名排序: %s 在 %s 之前", names[index-1], name)
		}
	}
	joined := strings.Join(names, ",")
	for _, required := range []string{
		release.ChecksumFileName,
		release.ManifestFileName,
		release.ArtifactName(testVersion, release.Platform{OS: "windows", Arch: "amd64"}),
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("资产清单缺少 %s", required)
		}
	}
}

// TestManifestRequireProvenance 校验正式发布的来源信息必须完整。
func TestManifestRequireProvenance(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	manifest := writeReleaseFixture(t, dir, defaultFixture())
	if err := manifest.RequireProvenance(); err != nil {
		t.Fatalf("完整来源信息应通过校验: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*release.Manifest)
	}{
		{"缺少仓库", func(m *release.Manifest) { m.SourceRepository = "" }},
		{"缺少工作流", func(m *release.Manifest) { m.SourceWorkflow = "" }},
		{"缺少运行标识", func(m *release.Manifest) { m.SourceRunID = "" }},
		{"缺少证明提供方", func(m *release.Manifest) { m.AttestationProvider = "" }},
		{"不支持的证明提供方", func(m *release.Manifest) { m.AttestationProvider = "self-signed" }},
		{"仓库格式非法", func(m *release.Manifest) { m.SourceRepository = "lingting" }},
	}
	for _, testCase := range cases {
		broken := manifest
		testCase.mutate(&broken)
		if err := broken.RequireProvenance(); err == nil {
			t.Errorf("%s: 应校验失败", testCase.name)
		}
	}
}

// TestManifestNotes 校验 Release 说明包含版本元数据、资产与两种核验方式。
//
// SHA-256 与来源证明是两种能力：说明里必须都出现，并且明确区分。
func TestManifestNotes(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	manifest := writeReleaseFixture(t, dir, defaultFixture())
	notes := manifest.Notes()
	for _, required := range []string{
		"# AgentFlow " + testVersion,
		manifest.Commit,
		manifest.BuildDate,
		manifest.GoVersion,
		testNodeVersion,
		testPnpmVersion,
		testWorkflow,
		testRunID,
		"sha256sum -c " + release.ChecksumFileName,
		"shasum -a 256 -c " + release.ChecksumFileName,
		"Get-FileHash",
		"gh attestation verify",
		testRepository,
	} {
		if !strings.Contains(notes, required) {
			t.Errorf("发布说明缺少 %q", required)
		}
	}
	for _, artifact := range manifest.Artifacts {
		if !strings.Contains(notes, artifact.File) {
			t.Errorf("发布说明缺少制品 %s", artifact.File)
		}
		if !strings.Contains(notes, artifact.SHA256) {
			t.Errorf("发布说明缺少制品摘要 %s", artifact.SHA256)
		}
	}

	// 没有来源信息时不得凭空给出证明核验命令：那会让用户以为制品已被认证。
	local := writeReleaseFixture(t, testkit.Dir(t), releaseFixtureOptions{
		version: release.DevVersion,
		commit:  testCommit,
	})
	localNotes := local.Notes()
	if strings.Contains(localNotes, "gh attestation verify") {
		t.Error("没有来源信息时不应给出证明核验命令")
	}
	if !strings.Contains(localNotes, "sha256sum -c "+release.ChecksumFileName) {
		t.Error("没有来源信息时仍必须给出校验文件核验方式")
	}
}
