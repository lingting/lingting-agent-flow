package release

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// agentflowPackage 是单文件程序的包路径。
const agentflowPackage = "./cmd/agentflow"

// BuildOptions 是一次发布构建的输入。
//
// 版本、提交、构建时间与 Go 工具链版本留空时按真实来源解析：提交读取 git HEAD，
// 构建时间取当前 UTC 时间，Go 版本取实际运行构建工具链的版本。
type BuildOptions struct {
	// RepoRoot 是仓库根目录，用于执行 go build 与解析相对路径。
	RepoRoot string
	// OutputDir 是制品输出目录。
	OutputDir string
	// WebDistDir 是前端生产构建产物目录。
	WebDistDir string
	// QuickstartPath 是打包进制品的快速启动说明。
	QuickstartPath string
	// NoticesPath 是打包进制品的第三方声明与许可证说明。
	NoticesPath string
	// Platforms 是目标平台；留空时使用发布矩阵。
	Platforms []Platform
	// Version 是版本号：dev 或 vMAJOR.MINOR.PATCH。
	Version string
	// Commit 是提交 SHA；留空时读取当前 HEAD。
	Commit string
	// BuildDate 是 UTC 构建时间；留空时取当前时间。
	BuildDate string
	// GoVersion 是 Go 工具链版本；留空时取运行期版本。
	GoVersion string
}

// BuildResult 是一次发布构建的结果。
type BuildResult struct {
	// Manifest 是写出的发布清单。
	Manifest Manifest
	// OutputDir 是制品的输出目录。
	OutputDir string
}

// docs 是打包进每个制品的说明文件内容。
type docs struct {
	// quickstart 是快速启动说明。
	quickstart []byte
	// notices 是第三方声明与许可证说明。
	notices []byte
}

// BuildDistribution 生成全部发布制品、写出校验文件并自检。
//
// 顺序固定：解析版本元数据 → 校验前端产物 → 逐平台编译打包 → 写清单与校验文件
// → 回读产物核对。前端校验在编译之前，避免产出"编译成功但页面空白"的程序。
func BuildDistribution(options BuildOptions, stdout, stderr io.Writer) (BuildResult, error) {
	metadata, err := resolveMetadata(options)
	if err != nil {
		return BuildResult{}, err
	}
	platforms, err := resolvePlatforms(options.Platforms)
	if err != nil {
		return BuildResult{}, err
	}
	if err := VerifyWebDist(resolvePath(options.RepoRoot, options.WebDistDir)); err != nil {
		return BuildResult{}, fmt.Errorf("%w；请先执行前端生产构建", err)
	}
	content, err := readDocs(options)
	if err != nil {
		return BuildResult{}, err
	}
	output := resolvePath(options.RepoRoot, options.OutputDir)
	if err := os.MkdirAll(output, 0o755); err != nil {
		return BuildResult{}, fmt.Errorf("创建输出目录失败: %w", err)
	}
	staging, err := os.MkdirTemp(output, ".build-")
	if err != nil {
		return BuildResult{}, fmt.Errorf("创建临时构建目录失败: %w", err)
	}
	defer func() {
		// 临时编译产物不留在输出目录里：它只用于生成压缩包。
		if removeErr := os.RemoveAll(staging); removeErr != nil {
			fmt.Fprintf(stderr, "警告: 清理临时构建目录失败: %v\n", removeErr)
		}
	}()

	artifacts := make([]Artifact, 0, len(platforms))
	for _, platform := range platforms {
		fmt.Fprintf(stderr, "构建制品 %s\n", platform)
		artifact, err := buildArtifact(options, output, metadata, platform, staging, content, stderr)
		if err != nil {
			return BuildResult{}, fmt.Errorf("%s: %w", platform, err)
		}
		artifacts = append(artifacts, artifact)
	}

	manifest := Manifest{
		Version:   metadata.Version,
		Commit:    metadata.Commit,
		BuildDate: metadata.BuildDate,
		GoVersion: metadata.GoVersion,
		Artifacts: artifacts,
	}
	if err := manifest.Validate(); err != nil {
		return BuildResult{}, fmt.Errorf("发布清单校验失败: %w", err)
	}
	if err := writeManifestFiles(output, manifest); err != nil {
		return BuildResult{}, err
	}
	if err := verifyArtifacts(output, manifest); err != nil {
		return BuildResult{}, err
	}
	if err := printSummary(stdout, manifest, output); err != nil {
		return BuildResult{}, err
	}
	return BuildResult{
		Manifest:  manifest,
		OutputDir: output,
	}, nil
}

// resolveMetadata 解析并校验版本、提交、构建时间与 Go 工具链版本。
//
// 缺省值只来自真实来源：任何一个来源不可用时直接失败，不用占位值冒充正式版本。
func resolveMetadata(options BuildOptions) (Metadata, error) {
	version := strings.TrimSpace(options.Version)
	if err := ValidateVersion(version); err != nil {
		return Metadata{}, err
	}
	commit := strings.TrimSpace(options.Commit)
	if commit == "" {
		resolved, err := resolveCommit(options.RepoRoot)
		if err != nil {
			return Metadata{}, err
		}
		commit = resolved
	}
	buildDate := strings.TrimSpace(options.BuildDate)
	if buildDate == "" {
		buildDate = time.Now().UTC().Format(time.RFC3339)
	}
	goVersion := strings.TrimSpace(options.GoVersion)
	if goVersion == "" {
		goVersion = runtime.Version()
	}
	metadata := Metadata{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: goVersion,
	}
	if err := metadata.Validate(); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

// resolveCommit 读取当前 HEAD 的完整提交 SHA。
func resolveCommit(repoRoot string) (string, error) {
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf(
			"读取 git 提交失败（可通过 Commit 显式指定）: %v: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// resolvePlatforms 解析目标平台，留空时返回完整发布矩阵。
//
// 不在发布矩阵内的平台一律拒绝：能交叉编译不等于产品支持该平台。
func resolvePlatforms(platforms []Platform) ([]Platform, error) {
	if len(platforms) == 0 {
		supported := make([]Platform, len(SupportedPlatforms))
		copy(supported, SupportedPlatforms)
		return supported, nil
	}
	resolved := make([]Platform, 0, len(platforms))
	for _, platform := range platforms {
		if !IsSupported(platform) {
			return nil, fmt.Errorf("目标平台 %s 不在发布矩阵内", platform)
		}
		resolved = append(resolved, platform)
	}
	return resolved, nil
}

// resolvePath 把相对路径按仓库根目录解析，绝对路径原样返回。
func resolvePath(repoRoot string, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(repoRoot, value)
}

// readDocs 读取要打包进制品的说明文件。
func readDocs(options BuildOptions) (docs, error) {
	quickstart, err := os.ReadFile(resolvePath(options.RepoRoot, options.QuickstartPath))
	if err != nil {
		return docs{}, fmt.Errorf("读取快速启动说明失败: %w", err)
	}
	notices, err := os.ReadFile(resolvePath(options.RepoRoot, options.NoticesPath))
	if err != nil {
		return docs{}, fmt.Errorf("读取第三方声明失败: %w", err)
	}
	return docs{
		quickstart: quickstart,
		notices:    notices,
	}, nil
}
