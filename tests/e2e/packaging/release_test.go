// 发布制品打包验收：真实生成全部目标平台制品，逐项校验压缩包内容、版本元数据与
// SHA-256 一致性；在设置了 AGENTFLOW_RELEASE_ARCHIVE 时改为校验并启动已下载的制品。
//
// 与 packaging_test.go 的区别：那里验证"单文件程序能在独立目录启动"，这里验证
// "发布流水线产出的制品本身是不是可交付的"——命名、布局、元数据、校验文件与清单。
package packaging_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lingting/agentflow/internal/release"
	"github.com/lingting/agentflow/tests/internal/testkit"
)

// checksumLinePattern 描述 SHA256SUMS 的单行格式。
const checksumLineSeparator = "  "

// testReleaseVersion 是打包验收使用的正式版本号。
//
// 必须是 vMAJOR.MINOR.PATCH：发布构建会拒绝 dev 之外的非正式版本号，不复用 dev
// 是为了让本用例真正走一遍正式版本的构建路径。
const testReleaseVersion = "v0.0.0"

// artifactManifest 是发布清单的解析结果（schema 见 internal/release.Manifest）。
type artifactManifest struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Artifacts []struct {
		File   string `json:"file"`
		OS     string `json:"os"`
		Arch   string `json:"arch"`
		Format string `json:"format"`
		Binary string `json:"binary"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"artifacts"`
}

// versionMetadata 是压缩包内 version.json 的解析结果。
type versionMetadata struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// countArtifactBinaries 统计不含调试信息的可执行文件数量，用于确认制品真的是编译产物。
func countArtifactBinaries(files map[string][]byte) int {
	count := 0
	for name, data := range files {
		if strings.HasSuffix(name, "agentflow") || strings.HasSuffix(name, "agentflow.exe") {
			if len(data) > 0 {
				count++
			}
		}
	}
	return count
}

// readArtifactEntries 读取压缩包内全部条目内容。
//
// 使用标准库自行解压，不复用 internal/release 的读取函数：用同一段代码验证自己
// 产生的结果无法发现"写入与读取同时错"的问题。
func readArtifactEntries(t *testing.T, archivePath string, format string) map[string][]byte {
	t.Helper()
	switch format {
	case release.FormatTarGz:
		return readTarGzEntries(t, archivePath)
	case release.FormatZip:
		return readZipEntries(t, archivePath)
	default:
		t.Fatalf("不支持的压缩格式: %s", format)
		return nil
	}
}

// readTarGzEntries 解压 tar.gz 压缩包。
func readTarGzEntries(t *testing.T, archivePath string) map[string][]byte {
	t.Helper()
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
	entries := make(map[string][]byte)
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
			t.Fatalf("读取 tar 条目内容 %s 失败: %v", header.Name, err)
		}
		entries[header.Name] = data
	}
	return entries
}

// readZipEntries 解压 zip 压缩包。
func readZipEntries(t *testing.T, archivePath string) map[string][]byte {
	t.Helper()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("打开 zip 压缩包失败: %v", err)
	}
	defer func() {
		_ = archive.Close()
	}()
	entries := make(map[string][]byte)
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("打开 zip 条目 %s 失败: %v", file.Name, err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatalf("读取 zip 条目内容 %s 失败: %v", file.Name, err)
		}
		entries[file.Name] = data
	}
	return entries
}

// fileSHA256 计算文件摘要。
func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开文件 %s 失败: %v", path, err)
	}
	defer func() {
		_ = file.Close()
	}()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatalf("计算文件摘要失败: %v", err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// TestReleaseArtifacts 真实执行一次发布构建，逐平台校验制品内容与校验文件。
//
// 用例覆盖：压缩包完整性、目录布局与可执行文件名、SHA-256 与文件内容一致、
// version.json 的版本元数据、SHA256SUMS 与清单的一一对应、当前平台制品可执行。
func TestReleaseArtifacts(t *testing.T) {
	root := repoRoot(t)
	uiDir := filepath.Join(root, "lingting-agent-flow-ui")

	// 缺少前端工具链时跳过：发布构建要求真实的前端产物，不允许用占位文件凑数。
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("WARN [SKIP] 本机没有 pnpm，跳过发布制品构建测试")
	}
	if _, err := os.Stat(filepath.Join(uiDir, "node_modules")); err != nil {
		t.Skip("WARN [SKIP] 前端依赖未安装，跳过发布制品构建测试")
	}

	workspace := testkit.New(t)
	outputDir := workspace.Dir("release")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("创建制品输出目录失败: %v", err)
	}

	// 1. 构建前端生产产物，供 //go:embed 使用。
	runCommand(t, uiDir, "pnpm", "build")

	// 2. 真实执行发布构建（与 CI 使用同一个入口）。
	runCommand(t, root, "go", "run", "./cmd/agentflow-release",
		"-version", testReleaseVersion,
		"-output", outputDir,
	)
	t.Logf("发布制品输出目录: %s", outputDir)

	// 3. 清单必须存在、可解析，并与 release 包的规则一致。
	manifestPath := filepath.Join(outputDir, release.ManifestFileName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("读取发布清单失败: %v", err)
	}
	var manifest artifactManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("解析发布清单失败: %v", err)
	}
	if manifest.Version != testReleaseVersion {
		t.Fatalf("清单版本应为 %s，实际 %q", testReleaseVersion, manifest.Version)
	}
	if !release.ValidCommit(manifest.Commit) {
		t.Fatalf("清单提交号不是完整 Git SHA: %q", manifest.Commit)
	}
	if !strings.HasPrefix(manifest.GoVersion, "go") {
		t.Fatalf("清单 Go 工具链版本无效: %q", manifest.GoVersion)
	}
	if len(manifest.Artifacts) != len(release.SupportedPlatforms) {
		t.Fatalf("制品数量应为 %d，实际 %d", len(release.SupportedPlatforms), len(manifest.Artifacts))
	}

	// 4. 逐平台校验制品。
	for _, artifact := range manifest.Artifacts {
		artifactPath := filepath.Join(outputDir, artifact.File)
		target := release.Platform{OS: artifact.OS, Arch: artifact.Arch}
		expectedName := release.ArtifactName(manifest.Version, target)
		if artifact.File != expectedName {
			t.Fatalf("制品文件名 %q 与命名规范不一致，应为 %q", artifact.File, expectedName)
		}
		info, err := os.Stat(artifactPath)
		if err != nil {
			t.Fatalf("制品 %s 不存在: %v", artifact.File, err)
		}
		if info.Size() != artifact.Size {
			t.Fatalf("制品 %s 大小与清单不一致：清单 %d，实际 %d", artifact.File, artifact.Size, info.Size())
		}
		if actual := fileSHA256(t, artifactPath); actual != artifact.SHA256 {
			t.Fatalf("制品 %s 的 SHA-256 与清单不一致：清单 %s，实际 %s", artifact.File, artifact.SHA256, actual)
		}

		entries := readArtifactEntries(t, artifactPath, artifact.Format)
		expectedNames := release.ExpectedArchiveNames(target)
		actualNames := make([]string, 0, len(entries))
		for name := range entries {
			actualNames = append(actualNames, name)
		}
		sort.Strings(actualNames)
		if strings.Join(actualNames, ",") != strings.Join(expectedNames, ",") {
			t.Fatalf("制品 %s 的文件布局错误：期望 %v，实际 %v", artifact.File, expectedNames, actualNames)
		}
		if countArtifactBinaries(entries) != 1 {
			t.Fatalf("制品 %s 应恰好包含一个可执行文件", artifact.File)
		}

		var metadata versionMetadata
		if err := json.Unmarshal(entries[release.MetadataFileName], &metadata); err != nil {
			t.Fatalf("解析制品 %s 的版本元数据失败: %v", artifact.File, err)
		}
		if metadata.Version != manifest.Version || metadata.Commit != manifest.Commit {
			t.Fatalf("制品 %s 的版本元数据与清单不一致: %+v", artifact.File, metadata)
		}
		if metadata.BuildDate != manifest.BuildDate || metadata.GoVersion != manifest.GoVersion {
			t.Fatalf("制品 %s 的构建信息与清单不一致: %+v", artifact.File, metadata)
		}
		if metadata.OS != artifact.OS || metadata.Arch != artifact.Arch {
			t.Fatalf("制品 %s 的目标平台与清单不一致: %+v", artifact.File, metadata)
		}
		quickstart := string(entries[release.QuickstartFileName])
		if !strings.Contains(quickstart, "agentflow server") {
			t.Fatalf("制品 %s 的快速启动说明缺少启动命令", artifact.File)
		}
		if len(entries[release.NoticesFileName]) == 0 {
			t.Fatalf("制品 %s 缺少第三方声明", artifact.File)
		}
	}

	// 5. 校验文件必须与清单一一对应，并且能在下载目录直接用于校验。
	checksumPath := filepath.Join(outputDir, release.ChecksumFileName)
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		t.Fatalf("读取校验文件失败: %v", err)
	}
	checksumLines := strings.Split(strings.TrimSuffix(string(checksumData), "\n"), "\n")
	if len(checksumLines) != len(manifest.Artifacts) {
		t.Fatalf("校验文件应有 %d 行，实际 %d", len(manifest.Artifacts), len(checksumLines))
	}
	expectedChecksums := make(map[string]string, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		expectedChecksums[artifact.File] = artifact.SHA256
	}
	for _, line := range checksumLines {
		parts := strings.SplitN(line, checksumLineSeparator, 2)
		if len(parts) != 2 {
			t.Fatalf("校验行格式错误: %q", line)
		}
		if expectedChecksums[parts[1]] != parts[0] {
			t.Fatalf("校验行 %q 与清单不一致", line)
		}
	}

	// 6. 当前平台制品必须真的能运行：解压后执行 version 并核对元数据。
	verifyCurrentPlatformArtifact(t, workspace, outputDir, manifest)
}

// verifyCurrentPlatformArtifact 解压当前平台制品，执行 version 查询并核对元数据。
func verifyCurrentPlatformArtifact(t *testing.T, workspace *testkit.Workspace, outputDir string, manifest artifactManifest) {
	t.Helper()
	platform := runtime.GOOS + "/" + runtime.GOARCH
	for _, artifact := range manifest.Artifacts {
		if artifact.OS+"/"+artifact.Arch != platform {
			continue
		}
		runDir := workspace.Dir("artifact-run")
		extractArchive(t, filepath.Join(outputDir, artifact.File), artifact.Format, runDir)
		binary := filepath.Join(runDir, artifact.Binary)
		if err := os.Chmod(binary, 0o755); err != nil {
			t.Fatalf("设置可执行权限失败: %v", err)
		}
		stdout, _ := runCommand(t, runDir, binary, "version")
		expectations := []string{manifest.Version, manifest.Commit, manifest.BuildDate}
		for _, expected := range expectations {
			if !strings.Contains(stdout, expected) {
				t.Fatalf("version 输出缺少 %s，实际: %s", expected, stdout)
			}
		}
		// 先确认制品内元数据与命令输出一致，再据此判断"制品显示的版本没有问题"。
		var metadata versionMetadata
		data, err := os.ReadFile(filepath.Join(runDir, release.MetadataFileName))
		if err != nil {
			t.Fatalf("读取制品版本元数据失败: %v", err)
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("解析制品版本元数据失败: %v", err)
		}
		if metadata.OS+"/"+metadata.Arch != platform {
			t.Fatalf("当前平台制品元数据错误: %+v", metadata)
		}
		t.Logf("当前平台 %s 制品版本输出: %s", platform, strings.TrimSpace(stdout))
		return
	}
	t.Fatalf("发布清单缺少当前平台制品: %s", platform)
}

// extractArchive 把压缩包解压到指定目录。
func extractArchive(t *testing.T, archivePath string, format string, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("创建解压目录失败: %v", err)
	}
	entries := readArtifactEntries(t, archivePath, format)
	for name, data := range entries {
		// 只允许扁平文件名：制品内不含子目录，出现路径分隔符说明布局被破坏。
		if strings.ContainsAny(name, `/\`) {
			t.Fatalf("制品内不应包含子目录条目: %s", name)
		}
		mode := os.FileMode(0o644)
		if name == release.BinaryName(runtime.GOOS) {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(target, name), data, mode); err != nil {
			t.Fatalf("写出解压文件 %s 失败: %v", name, err)
		}
	}
}

// TestReleaseArchiveSmoke 校验并运行发布流水线下载到本机的单个制品。
//
// 该用例由 .github/workflows/release.yml 在目标平台的原生 Runner 上执行，环境变量：
//   - AGENTFLOW_RELEASE_ARCHIVE：制品文件绝对路径；
//   - AGENTFLOW_RELEASE_PLATFORM：目标平台，形如 linux/amd64；
//   - AGENTFLOW_RELEASE_VERSION / AGENTFLOW_RELEASE_COMMIT：期望的版本元数据。
//
// 变量缺失时跳过（本地 `make test` 不应因为没有发布制品而失败）。
func TestReleaseArchiveSmoke(t *testing.T) {
	archivePath := os.Getenv("AGENTFLOW_RELEASE_ARCHIVE")
	if archivePath == "" {
		t.Skip("WARN [SKIP] 未提供 AGENTFLOW_RELEASE_ARCHIVE，跳过发布制品运行冒烟")
	}
	value := os.Getenv("AGENTFLOW_RELEASE_PLATFORM")
	target, err := release.ParsePlatform(value)
	if err != nil {
		t.Fatalf("AGENTFLOW_RELEASE_PLATFORM 无效: %v", err)
	}
	if !release.IsSupported(target) {
		t.Fatalf("目标平台 %s 不在发布矩阵内", target)
	}
	if target.OS != runtime.GOOS || target.Arch != runtime.GOARCH {
		// 交叉编译成功不等于能在当前平台运行：这里必须明确跳过，而不是伪装成通过。
		t.Skipf("WARN [SKIP] 当前运行平台为 %s/%s，无法运行 %s 制品",
			runtime.GOOS, runtime.GOARCH, target)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("发布制品不存在: %v", err)
	}
	format := release.ArchiveFormat(target.OS)
	if !strings.HasSuffix(archivePath, "."+format) {
		t.Fatalf("制品扩展名与目标平台不匹配: %s", archivePath)
	}

	workspace := testkit.New(t)
	runDir := workspace.Dir("release-smoke")
	entries := readArtifactEntries(t, archivePath, format)
	expectedNames := release.ExpectedArchiveNames(target)
	actualNames := make([]string, 0, len(entries))
	for name := range entries {
		actualNames = append(actualNames, name)
	}
	sort.Strings(actualNames)
	if strings.Join(actualNames, ",") != strings.Join(expectedNames, ",") {
		t.Fatalf("制品文件布局错误：期望 %v，实际 %v", expectedNames, actualNames)
	}
	for name, data := range entries {
		mode := os.FileMode(0o644)
		if name == release.BinaryName(target.OS) {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(runDir, name), data, mode); err != nil {
			t.Fatalf("写出解压文件 %s 失败: %v", name, err)
		}
	}

	var metadata versionMetadata
	if err := json.Unmarshal(entries[release.MetadataFileName], &metadata); err != nil {
		t.Fatalf("解析制品版本元数据失败: %v", err)
	}
	if metadata.OS != target.OS || metadata.Arch != target.Arch {
		t.Fatalf("制品元数据目标平台错误: %+v", metadata)
	}
	if expected := os.Getenv("AGENTFLOW_RELEASE_VERSION"); expected != "" && metadata.Version != expected {
		t.Fatalf("制品版本应为 %s，实际 %s", expected, metadata.Version)
	}
	if expected := os.Getenv("AGENTFLOW_RELEASE_COMMIT"); expected != "" && metadata.Commit != expected {
		t.Fatalf("制品提交应为 %s，实际 %s", expected, metadata.Commit)
	}

	// 1. 版本查询必须与制品元数据一致。
	binary := filepath.Join(runDir, release.BinaryName(target.OS))
	stdout, _ := runCommand(t, runDir, binary, "version")
	expectations := []string{metadata.Version, metadata.Commit, metadata.BuildDate}
	for _, expected := range expectations {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("version 输出缺少 %s，实际: %s", expected, stdout)
		}
	}

	// 2. 从解压目录启动，核验 HTTP API 与前端静态资源（不依赖仓库目录）。
	configPath := filepath.Join(runDir, "agentflow.yaml")
	if err := os.WriteFile(configPath, []byte(minimalConfig(runDir)), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	baseURL, stop := startServer(t, runDir, configPath)
	t.Cleanup(stop)
	status, header, body := fetch(t, baseURL+"/")
	if status != 200 {
		t.Fatalf("页面入口应返回 200，实际 %d", status)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
		t.Fatalf("页面入口应为 HTML，实际 %q", header.Get("Content-Type"))
	}
	if !strings.Contains(body, `id="root"`) {
		t.Fatalf("页面入口内容不完整: %s", body)
	}
	for _, match := range assetPattern.FindAllStringSubmatch(body, -1) {
		assetStatus, _, assetBody := fetch(t, baseURL+match[1])
		if assetStatus != 200 || assetBody == "" {
			t.Fatalf("静态资源 %s 不可用：状态 %d", match[1], assetStatus)
		}
	}
	status, _, _ = fetch(t, baseURL+"/api/v1/health")
	if status != 200 {
		t.Fatalf("健康检查应返回 200，实际 %d", status)
	}

	// 3. 停止服务并确认端口释放。
	stopServer(t, stop)
	t.Logf("发布制品冒烟通过: %s（%s）", filepath.Base(archivePath), fmt.Sprintf("%s/%s", target.OS, target.Arch))
}
