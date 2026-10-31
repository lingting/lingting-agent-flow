package release_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/release"
)

// stubWebDist 在测试工作空间内构造一份最小但完整的前端产物。
//
// 校验规则只关心 index.html 与 assets 下的 js/css 是否存在且非空，因此这里不需要
// 真正的 Vite 产物；真实前端产物由 tests/e2e/packaging 的端到端用例覆盖。
func stubWebDist(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist")
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatalf("创建前端产物目录失败: %v", err)
	}
	paths := []string{
		filepath.Join(dir, "index.html"),
		filepath.Join(assets, "index.js"),
		filepath.Join(assets, "index.css"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("stub\n"), 0o644); err != nil {
			t.Fatalf("写入前端产物 %s 失败: %v", path, err)
		}
	}
	return dir
}

// currentPlatformList 返回只含当前平台的构建矩阵。
//
// 用例只验证"制品生成与校验"这条链路，逐平台交叉编译由端到端用例覆盖，因此这里
// 限制在当前平台以保持用例速度。
func currentPlatformList() []release.Platform {
	return []release.Platform{{OS: runtime.GOOS, Arch: runtime.GOARCH}}
}

// unsupportedPlatformList 返回一个不在发布矩阵内的平台，用于校验拒绝逻辑。
func unsupportedPlatformList() []release.Platform {
	return []release.Platform{{OS: "freebsd", Arch: "amd64"}}
}

// buildOptions 返回一次只构建当前平台的发布构建输入。
func buildOptions(t *testing.T, repoRoot string, outputDir string) release.BuildOptions {
	t.Helper()
	return release.BuildOptions{
		RepoRoot:       repoRoot,
		OutputDir:      outputDir,
		WebDistDir:     stubWebDist(t),
		QuickstartPath: filepath.Join("docs", "release", "QUICKSTART.md"),
		NoticesPath:    filepath.Join("docs", "release", "THIRD-PARTY-NOTICES.md"),
		Platforms:      currentPlatformList(),
		Version:        testVersion,
		Commit:         testCommit,
		BuildDate:      testBuildDate,
		GoVersion:      testGoVersion,
	}
}

// TestBuildDistributionRejectsBadInput 校验构建入口在输入不合法时直接失败。
//
// 版本号、平台与前端产物三种问题都必须在编译之前暴露，不能产出"看起来成功"的制品。
func TestBuildDistributionRejectsBadInput(t *testing.T) {
	t.Parallel()
	root := repoRootPath(t)
	cases := []struct {
		name   string
		mutate func(*release.BuildOptions)
	}{
		{"版本号非法", func(o *release.BuildOptions) { o.Version = "1.2.3" }},
		{"提交号非法", func(o *release.BuildOptions) { o.Commit = "abc1234" }},
		{"构建时间非法", func(o *release.BuildOptions) { o.BuildDate = "2026-01-02" }},
		{"Go 版本非法", func(o *release.BuildOptions) { o.GoVersion = "1.24.5" }},
		{"平台不受支持", func(o *release.BuildOptions) { o.Platforms = unsupportedPlatformList() }},
		{"前端产物缺失", func(o *release.BuildOptions) { o.WebDistDir = o.OutputDir }},
		{"快速启动说明缺失", func(o *release.BuildOptions) { o.QuickstartPath = "docs/release/missing.md" }},
	}
	for _, testCase := range cases {
		options := buildOptions(t, root, filepath.Join(t.TempDir(), "out"))
		testCase.mutate(&options)
		var stdout, stderr bytes.Buffer
		if _, err := release.BuildDistribution(options, &stdout, &stderr); err == nil {
			t.Errorf("%s: 构建应失败", testCase.name)
		}
	}
}

// TestBuildDistributionProducesVerifiedArtifacts 校验发布构建的产物与校验文件。
func TestBuildDistributionProducesVerifiedArtifacts(t *testing.T) {
	root := repoRootPath(t)
	outputDir := filepath.Join(t.TempDir(), "out")
	options := buildOptions(t, root, outputDir)
	var stdout, stderr bytes.Buffer
	result, err := release.BuildDistribution(options, &stdout, &stderr)
	if err != nil {
		t.Fatalf("发布构建失败: %v\n标准错误: %s", err, stderr.String())
	}

	// 1. 清单必须能被重新读入并通过校验，且与返回值一致。
	data, err := os.ReadFile(filepath.Join(outputDir, release.ManifestFileName))
	if err != nil {
		t.Fatalf("读取发布清单失败: %v", err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("解析发布清单失败: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("发布清单校验失败: %v", err)
	}
	if len(manifest.Artifacts) != 1 || manifest.Artifacts[0] != result.Manifest.Artifacts[0] {
		t.Fatalf("清单制品与构建结果不一致: %+v", manifest.Artifacts)
	}
	if manifest.Version != testVersion || manifest.Commit != testCommit {
		t.Fatalf("清单版本元数据错误: %+v", manifest)
	}
	if manifest.GoVersion != testGoVersion || manifest.BuildDate != testBuildDate {
		t.Fatalf("清单构建信息错误: %+v", manifest)
	}

	// 2. 校验文件必须与清单逐行一致，且摘要与制品内容匹配。
	checksumData, err := os.ReadFile(filepath.Join(outputDir, release.ChecksumFileName))
	if err != nil {
		t.Fatalf("读取校验文件失败: %v", err)
	}
	if string(checksumData) != manifest.Checksums() {
		t.Fatalf("校验文件与清单不一致:\n%s\n%s", checksumData, manifest.Checksums())
	}
	artifact := manifest.Artifacts[0]
	artifactPath := filepath.Join(outputDir, artifact.File)
	if err := release.VerifyChecksum(artifactPath, artifact.SHA256); err != nil {
		t.Fatalf("制品摘要校验失败: %v", err)
	}

	// 3. 压缩包布局与制品内版本元数据。
	if err := release.CheckArchiveLayout(artifactPath, artifact.Format, artifact.Platform()); err != nil {
		t.Fatalf("压缩包布局错误: %v", err)
	}
	entries := readArchiveEntriesForTest(t, artifactPath, artifact.Format)
	metadataData, ok := entries[release.MetadataFileName]
	if !ok {
		t.Fatalf("压缩包缺少 %s", release.MetadataFileName)
	}
	var metadata release.Metadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		t.Fatalf("解析制品版本元数据失败: %v", err)
	}
	if metadata.Version != testVersion || metadata.Commit != testCommit {
		t.Fatalf("制品版本元数据错误: %+v", metadata)
	}
	if metadata.OS != runtime.GOOS || metadata.Arch != runtime.GOARCH {
		t.Fatalf("制品目标平台错误: %+v", metadata)
	}
	binary := entries[release.BinaryName(runtime.GOOS)]
	if len(binary) == 0 {
		t.Fatalf("压缩包缺少可执行文件 %s", release.BinaryName(runtime.GOOS))
	}
	// -ldflags -X 注入的版本必须真的写进二进制：在文件内容里查找注入值。
	if !bytes.Contains(binary, []byte(testVersion)) {
		t.Fatalf("可执行文件中未找到注入的版本号 %s", testVersion)
	}
	if !bytes.Contains(binary, []byte(testCommit)) {
		t.Fatalf("可执行文件中未找到注入的提交号 %s", testCommit)
	}

	// 4. 摘要必须与"用户重新计算"的结果一致，而不只是构建工具自己的返回值。
	actual, size, err := release.SHA256File(artifactPath)
	if err != nil {
		t.Fatalf("重新计算制品摘要失败: %v", err)
	}
	if actual != artifact.SHA256 || size != artifact.Size {
		t.Fatalf("制品摘要或大小与清单不一致: %s/%d vs %s/%d", actual, size, artifact.SHA256, artifact.Size)
	}

	// 5. 临时编译目录不得留在输出目录里。
	dirEntries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("读取输出目录失败: %v", err)
	}
	for _, entry := range dirEntries {
		if strings.HasPrefix(entry.Name(), ".build-") {
			t.Fatalf("输出目录残留临时构建目录: %s", entry.Name())
		}
	}
}

// TestBuildDistributionWithDevVersion 校验本地 dev 构建同样产出带校验信息的制品。
//
// 本地核对打包流程时不允许"因为不是正式版本就跳过校验文件"。
func TestBuildDistributionWithDevVersion(t *testing.T) {
	root := repoRootPath(t)
	outputDir := filepath.Join(t.TempDir(), "out")
	options := buildOptions(t, root, outputDir)
	options.Version = release.DevVersion
	var stdout, stderr bytes.Buffer
	result, err := release.BuildDistribution(options, &stdout, &stderr)
	if err != nil {
		t.Fatalf("dev 版本构建失败: %v\n标准错误: %s", err, stderr.String())
	}
	artifact := result.Manifest.Artifacts[0]
	if !strings.Contains(artifact.File, release.DevVersion) {
		t.Fatalf("dev 制品文件名应包含版本标识: %s", artifact.File)
	}
	if !release.ValidChecksum(artifact.SHA256) {
		t.Fatalf("dev 制品摘要无效: %s", artifact.SHA256)
	}
	if _, err := os.Stat(filepath.Join(outputDir, release.ChecksumFileName)); err != nil {
		t.Fatalf("dev 构建同样必须生成校验文件: %v", err)
	}
}

// TestVerifyWebDist 校验前端产物完整性检查的判定。
//
// 缺少入口页面、脚本或样式时必须失败：否则会产出"编译成功但页面空白"的程序。
func TestVerifyWebDist(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		prepare func(t *testing.T) string
		wantErr bool
	}{
		{"完整产物", stubWebDist, false},
		{"目录不存在", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing")
		}, true},
		{"缺少入口页面", func(t *testing.T) string {
			dir := stubWebDist(t)
			if err := os.Remove(filepath.Join(dir, "index.html")); err != nil {
				t.Fatalf("删除入口页面失败: %v", err)
			}
			return dir
		}, true},
		{"入口页面为空", func(t *testing.T) string {
			dir := stubWebDist(t)
			if err := os.WriteFile(filepath.Join(dir, "index.html"), nil, 0o644); err != nil {
				t.Fatalf("清空入口页面失败: %v", err)
			}
			return dir
		}, true},
		{"缺少脚本资源", func(t *testing.T) string {
			dir := stubWebDist(t)
			if err := os.Remove(filepath.Join(dir, "assets", "index.js")); err != nil {
				t.Fatalf("删除脚本资源失败: %v", err)
			}
			return dir
		}, true},
		{"缺少样式资源", func(t *testing.T) string {
			dir := stubWebDist(t)
			if err := os.Remove(filepath.Join(dir, "assets", "index.css")); err != nil {
				t.Fatalf("删除样式资源失败: %v", err)
			}
			return dir
		}, true},
		{"只有占位文件", func(t *testing.T) string {
			dir := filepath.Join(t.TempDir(), "placeholder")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("创建目录失败: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte("# placeholder\n"), 0o644); err != nil {
				t.Fatalf("写入占位文件失败: %v", err)
			}
			return dir
		}, true},
	}
	for _, testCase := range cases {
		err := release.VerifyWebDist(testCase.prepare(t))
		if testCase.wantErr && err == nil {
			t.Errorf("%s: 应校验失败", testCase.name)
		}
		if !testCase.wantErr && err != nil {
			t.Errorf("%s: 应校验通过，实际错误: %v", testCase.name, err)
		}
	}
}
