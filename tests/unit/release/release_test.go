// Package release_test 校验发布制品的版本元数据、平台矩阵、命名规范与校验文件规则。
//
// 这些规则是"制品能不能与源码一一对应"的唯一依据，因此逐条覆盖：命名、压缩格式、
// 清单一致性、SHA-256 校验与拒绝不合规输入。
package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lingting/agentflow/internal/release"
)

// 测试用固定版本元数据；提交号是合法 Git SHA，避免依赖具体仓库状态。
const (
	testVersion   = "v1.2.3"
	testCommit    = "cd983e4c8e85d5f38d73e7f094d661defa6da623"
	testBuildDate = "2026-01-02T03:04:05Z"
	testGoVersion = "go1.24.5"
)

// testPlatforms 是发布矩阵的预期字符串形式，顺序与 release.SupportedPlatforms 一致。
var testPlatforms = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"}

// parsePlatform 解析目标平台，解析失败直接结束用例。
func parsePlatform(t *testing.T, value string) release.Platform {
	t.Helper()
	parsed, err := release.ParsePlatform(value)
	if err != nil {
		t.Fatalf("解析平台 %s 失败: %v", value, err)
	}
	return parsed
}

// sampleMetadata 返回一份合法的版本元数据。
func sampleMetadata() release.Metadata {
	return release.Metadata{
		Version:   testVersion,
		Commit:    testCommit,
		BuildDate: testBuildDate,
		GoVersion: testGoVersion,
	}
}

// sampleArtifact 构造某个平台的制品描述，字段全部符合命名规范。
func sampleArtifact(t *testing.T, value string, checksum string) release.Artifact {
	t.Helper()
	target := parsePlatform(t, value)
	return release.Artifact{
		File:   release.ArtifactName(testVersion, target),
		OS:     target.OS,
		Arch:   target.Arch,
		Format: release.ArchiveFormat(target.OS),
		Binary: release.BinaryName(target.OS),
		SHA256: checksum,
		Size:   1024,
	}
}

// sampleManifest 返回一份包含两个平台的合法清单。
func sampleManifest(t *testing.T) release.Manifest {
	t.Helper()
	checksum := release.SHA256Hex([]byte("artifact"))
	return release.Manifest{
		Version:   testVersion,
		Commit:    testCommit,
		BuildDate: testBuildDate,
		GoVersion: testGoVersion,
		Artifacts: []release.Artifact{sampleArtifact(t, "linux/amd64", checksum), sampleArtifact(t, "windows/amd64", checksum)},
	}
}

// TestValidateVersion 校验版本号只接受 dev 与 vMAJOR.MINOR.PATCH。
//
// 用 CI 运行编号、分支名或空值冒充正式版本会让制品无法与源码对应，必须拒绝。
func TestValidateVersion(t *testing.T) {
	t.Parallel()
	valid := []string{"dev", "v1.2.3", "v0.0.1", "v10.20.30"}
	for _, version := range valid {
		if err := release.ValidateVersion(version); err != nil {
			t.Errorf("版本 %q 应被接受，实际错误: %v", version, err)
		}
	}
	invalid := []string{
		"", "1.2.3", "v1.2", "v1.2.3.4", "v1.2.3-rc1",
		"1.2.3+build", "v01.2.3", "dev-1.2.3", "12345",
	}
	for _, version := range invalid {
		if err := release.ValidateVersion(version); err == nil {
			t.Errorf("版本 %q 应被拒绝", version)
		}
	}
}

// TestIsReleaseVersion 校验正式版本判定与 dev 区分。
func TestIsReleaseVersion(t *testing.T) {
	t.Parallel()
	if release.IsReleaseVersion(release.DevVersion) {
		t.Fatal("dev 不应被判为正式版本")
	}
	if !release.IsReleaseVersion("v1.0.0") {
		t.Fatal("v1.0.0 应被判为正式版本")
	}
}

// TestValidCommit 校验提交号必须是完整的 Git SHA。
func TestValidCommit(t *testing.T) {
	t.Parallel()
	if !release.ValidCommit(testCommit) {
		t.Fatalf("完整提交 SHA 应被接受: %s", testCommit)
	}
	invalid := []string{"", "none", "unknown", "cd983e4", strings.ToUpper(testCommit)}
	for _, commit := range invalid {
		if release.ValidCommit(commit) {
			t.Errorf("提交号 %q 应被拒绝", commit)
		}
	}
}

// TestMetadataValidate 校验版本元数据的字段约束。
func TestMetadataValidate(t *testing.T) {
	t.Parallel()
	if err := sampleMetadata().Validate(); err != nil {
		t.Fatalf("合法版本元数据被拒绝: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*release.Metadata)
	}{
		{"版本号非法", func(m *release.Metadata) { m.Version = "1.2.3" }},
		{"提交号不是完整SHA", func(m *release.Metadata) { m.Commit = "abc" }},
		{"构建时间非法", func(m *release.Metadata) { m.BuildDate = "2026-01-02" }},
		{"Go 版本缺失", func(m *release.Metadata) { m.GoVersion = "" }},
	}
	for _, testCase := range cases {
		metadata := sampleMetadata()
		testCase.mutate(&metadata)
		if err := metadata.Validate(); err == nil {
			t.Errorf("%s: 应校验失败", testCase.name)
		}
	}
}

// TestMetadataJSON 校验版本元数据 JSON 字段与结尾换行。
func TestMetadataJSON(t *testing.T) {
	t.Parallel()
	metadata := sampleMetadata()
	metadata.OS = "linux"
	metadata.Arch = "arm64"
	data, err := metadata.JSON()
	if err != nil {
		t.Fatalf("序列化版本元数据失败: %v", err)
	}
	text := string(data)
	expected := []string{
		`"version": "v1.2.3"`,
		`"commit": "` + testCommit + `"`,
		`"build_date": "` + testBuildDate + `"`,
		`"go_version": "go1.24.5"`,
		`"os": "linux"`,
		`"arch": "arm64"`,
	}
	for _, item := range expected {
		if !strings.Contains(text, item) {
			t.Errorf("版本元数据缺少 %s，实际: %s", item, text)
		}
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("版本元数据应以换行结束")
	}
}

// TestSupportedPlatforms 校验发布矩阵内容与目标平台判定。
//
// 矩阵只包含经过核实的五个平台；能交叉编译不等于产品支持，因此未列入的平台必须被拒绝。
func TestSupportedPlatforms(t *testing.T) {
	t.Parallel()
	if len(release.SupportedPlatforms) != len(testPlatforms) {
		t.Fatalf("发布矩阵应有 %d 个平台，实际 %d", len(testPlatforms), len(release.SupportedPlatforms))
	}
	for index, target := range release.SupportedPlatforms {
		if target.String() != testPlatforms[index] {
			t.Errorf("第 %d 个平台应为 %s，实际 %s", index+1, testPlatforms[index], target)
		}
		if !release.IsSupported(target) {
			t.Errorf("%s 应被判定为受支持", target)
		}
	}
	unsupported := []string{"linux/386", "windows/arm64", "freebsd/amd64", "darwin/riscv64"}
	for _, value := range unsupported {
		if release.IsSupported(parsePlatform(t, value)) {
			t.Errorf("%s 不应被判定为受支持", value)
		}
	}
}

// TestParsePlatform 校验平台标识解析：格式错误与"未验证平台"必须能区分。
func TestParsePlatform(t *testing.T) {
	t.Parallel()
	target := parsePlatform(t, " linux/amd64 ")
	if target.OS != "linux" || target.Arch != "amd64" {
		t.Fatalf("平台解析结果错误: %+v", target)
	}
	broken := []string{"linux", "linux/amd64/extra", "/amd64", "linux/", ""}
	for _, value := range broken {
		if _, err := release.ParsePlatform(value); err == nil {
			t.Errorf("平台 %q 应解析失败", value)
		}
	}
}

// TestArtifactName 校验制品命名规范与压缩格式。
func TestArtifactName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		platform string
		expected string
	}{
		{"linux/amd64", "agentflow_v1.2.3_linux_amd64.tar.gz"},
		{"linux/arm64", "agentflow_v1.2.3_linux_arm64.tar.gz"},
		{"darwin/amd64", "agentflow_v1.2.3_darwin_amd64.tar.gz"},
		{"darwin/arm64", "agentflow_v1.2.3_darwin_arm64.tar.gz"},
		{"windows/amd64", "agentflow_v1.2.3_windows_amd64.zip"},
	}
	for _, testCase := range cases {
		actual := release.ArtifactName(testVersion, parsePlatform(t, testCase.platform))
		if actual != testCase.expected {
			t.Errorf("%s 的制品名应为 %s，实际 %s", testCase.platform, testCase.expected, actual)
		}
	}
	if release.BinaryName("windows") != "agentflow.exe" {
		t.Error("Windows 可执行文件名应为 agentflow.exe")
	}
	if release.BinaryName("linux") != "agentflow" {
		t.Error("Linux 可执行文件名应为 agentflow")
	}
}

// TestChecksumHelpers 校验 SHA-256 计算、格式判定与文件校验。
func TestChecksumHelpers(t *testing.T) {
	t.Parallel()
	// 空内容的 SHA-256 是公开常量，用它确认算法没有被替换。
	const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if actual := release.SHA256Hex(nil); actual != emptyDigest {
		t.Fatalf("空内容摘要错误: %s", actual)
	}
	if !release.ValidChecksum(emptyDigest) {
		t.Fatal("合法摘要应被接受")
	}
	invalid := []string{"", strings.ToUpper(emptyDigest), emptyDigest[:63], emptyDigest + "0"}
	for _, digest := range invalid {
		if release.ValidChecksum(digest) {
			t.Errorf("摘要 %q 应被拒绝", digest)
		}
	}
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, []byte("agentflow"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	digest, size, err := release.SHA256File(path)
	if err != nil {
		t.Fatalf("计算文件摘要失败: %v", err)
	}
	if size != int64(len("agentflow")) {
		t.Errorf("文件大小应为 %d，实际 %d", len("agentflow"), size)
	}
	if err := release.VerifyChecksum(path, digest); err != nil {
		t.Fatalf("摘要校验应通过: %v", err)
	}
	if err := release.VerifyChecksum(path, emptyDigest); err == nil {
		t.Error("摘要不一致时应校验失败")
	}
	if err := release.VerifyChecksum(filepath.Join(t.TempDir(), "missing"), digest); err == nil {
		t.Error("文件不存在时应校验失败")
	}
}

// TestManifestValidate 校验清单规则：文件名、格式、摘要、大小与重复平台。
func TestManifestValidate(t *testing.T) {
	t.Parallel()
	valid := sampleManifest(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法清单被拒绝: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*release.Manifest)
	}{
		{"没有制品", func(m *release.Manifest) { m.Artifacts = nil }},
		{"平台不受支持", func(m *release.Manifest) {
			m.Artifacts[1].OS = "windows"
			m.Artifacts[1].Arch = "arm64"
		}},
		{"文件名与规范不一致", func(m *release.Manifest) {
			m.Artifacts[0].File = "agentflow_v1.2.3_linux_amd64.tar"
		}},
		{"压缩格式与平台不一致", func(m *release.Manifest) { m.Artifacts[1].Format = release.FormatTarGz }},
		{"可执行文件名不一致", func(m *release.Manifest) { m.Artifacts[0].Binary = "agentflow.exe" }},
		{"摘要非法", func(m *release.Manifest) { m.Artifacts[0].SHA256 = "not-a-checksum" }},
		{"大小非正", func(m *release.Manifest) { m.Artifacts[0].Size = 0 }},
		{"提交号非法", func(m *release.Manifest) { m.Commit = "abc1234" }},
		{"构建时间非法", func(m *release.Manifest) { m.BuildDate = "yesterday" }},
	}
	for _, testCase := range cases {
		manifest := sampleManifest(t)
		testCase.mutate(&manifest)
		if err := manifest.Validate(); err == nil {
			t.Errorf("%s: 应校验失败", testCase.name)
		}
	}
	duplicated := sampleManifest(t)
	duplicated.Artifacts = []release.Artifact{duplicated.Artifacts[0], duplicated.Artifacts[0]}
	if err := duplicated.Validate(); err == nil {
		t.Error("同一平台出现多个制品应校验失败")
	}
}

// TestManifestChecksums 校验 SHA256SUMS 内容格式与排序稳定性。
//
// 排序必须只依赖文件名而不是输入顺序，否则同一个版本重新生成会得到不同的校验文件，
// 用户无法判断差异是不是真的来自制品变化。
func TestManifestChecksums(t *testing.T) {
	t.Parallel()
	linux := sampleArtifact(t, "linux/amd64", release.SHA256Hex([]byte("linux")))
	windows := sampleArtifact(t, "windows/amd64", release.SHA256Hex([]byte("windows")))
	first := release.Manifest{Artifacts: []release.Artifact{linux, windows}}
	second := release.Manifest{Artifacts: []release.Artifact{windows, linux}}
	if first.Checksums() != second.Checksums() {
		t.Fatalf("校验文件内容应只依赖文件名排序:\n%s\n%s", first.Checksums(), second.Checksums())
	}
	lines := strings.Split(strings.TrimSuffix(first.Checksums(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("校验文件应有 2 行，实际 %d", len(lines))
	}
	if !strings.HasSuffix(lines[0], linux.File) {
		t.Errorf("第一行应指向 %s，实际 %s", linux.File, lines[0])
	}
	if !strings.HasPrefix(lines[0], linux.SHA256+"  ") {
		t.Errorf("校验行格式应为「摘要  文件名」，实际 %s", lines[0])
	}
}

// TestManifestJSON 校验清单 JSON 内容不含本地路径与凭据字段。
func TestManifestJSON(t *testing.T) {
	t.Parallel()
	manifest := sampleManifest(t)
	data, err := manifest.JSON()
	if err != nil {
		t.Fatalf("序列化清单失败: %v", err)
	}
	text := string(data)
	expected := []string{
		`"version": "v1.2.3"`,
		`"commit": "` + testCommit + `"`,
		`"build_date": "` + testBuildDate + `"`,
		`"go_version": "go1.24.5"`,
		`"sha256"`,
		`"size"`,
	}
	for _, item := range expected {
		if !strings.Contains(text, item) {
			t.Errorf("清单缺少 %s，实际: %s", item, text)
		}
	}
	forbidden := []string{"token", "password", "secret", "private_key", "C:\\", "D:\\"}
	for _, item := range forbidden {
		if strings.Contains(strings.ToLower(text), strings.ToLower(item)) {
			t.Errorf("清单不应包含 %q: %s", item, text)
		}
	}
}

// TestMetadataFromManifest 校验清单派生出的单个制品元数据。
func TestMetadataFromManifest(t *testing.T) {
	t.Parallel()
	manifest := release.Manifest{
		Version:   testVersion,
		Commit:    testCommit,
		BuildDate: testBuildDate,
		GoVersion: testGoVersion,
	}
	metadata := manifest.Metadata(parsePlatform(t, "darwin/arm64"))
	if metadata.Version != testVersion || metadata.Commit != testCommit {
		t.Fatalf("元数据版本字段错误: %+v", metadata)
	}
	if metadata.OS != "darwin" || metadata.Arch != "arm64" {
		t.Fatalf("元数据平台字段错误: %+v", metadata)
	}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("清单派生元数据应合法: %v", err)
	}
	// 构建时间必须能被解析为 RFC 3339，保证 version.json 与清单使用同一格式。
	if _, err := time.Parse(time.RFC3339, metadata.BuildDate); err != nil {
		t.Fatalf("构建时间格式错误: %v", err)
	}
}
