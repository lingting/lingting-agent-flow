package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/lingting/agentflow/internal/release"
)

// options 是发布构建子命令（build）的命令行参数。
type options struct {
	// version 是版本号：dev 或 vMAJOR.MINOR.PATCH。
	version string
	// commit 是提交 SHA，留空时通过 git 读取当前 HEAD。
	commit string
	// buildDate 是 UTC 构建时间，留空时取当前时间。
	buildDate string
	// goVersion 是 Go 工具链版本，留空时取本工具的运行期版本。
	goVersion string
	// nodeVersion 是前端构建使用的 Node.js 版本，可带 v 前缀。
	nodeVersion string
	// pnpmVersion 是前端构建使用的 pnpm 版本。
	pnpmVersion string
	// sourceRepository 是构建该制品的仓库（owner/name）。
	sourceRepository string
	// sourceWorkflow 是执行构建的工作流文件。
	sourceWorkflow string
	// sourceRunID 是执行构建的工作流运行标识。
	sourceRunID string
	// attestationProvider 是来源证明的生成机制。
	attestationProvider string
	// output 是制品输出目录。
	output string
	// platforms 是目标平台列表（逗号分隔），留空时使用发布矩阵。
	platforms string
	// webDist 是前端生产构建产物目录。
	webDist string
	// repoRoot 是仓库根目录，用于执行 go build 与解析相对路径。
	repoRoot string
	// quickstart 是打包进制品的快速启动说明。
	quickstart string
	// notices 是打包进制品的第三方声明与许可证说明。
	notices string
}

// parseOptions 解析发布构建子命令的命令行参数。
func parseOptions(args []string, stderr io.Writer) (*options, error) {
	opts := &options{}
	flags := flag.NewFlagSet("agentflow-release build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.version, "version", release.DevVersion,
		"版本号：dev 或 vMAJOR.MINOR.PATCH")
	flags.StringVar(&opts.commit, "commit", "",
		"提交 SHA，留空时读取当前 git HEAD")
	flags.StringVar(&opts.buildDate, "build-date", "",
		"UTC 构建时间（RFC 3339），留空时取当前时间")
	flags.StringVar(&opts.goVersion, "go-version", "",
		"Go 工具链版本，留空时取本工具的运行期版本")
	flags.StringVar(&opts.nodeVersion, "node-version", "",
		"构建前端产物使用的 Node.js 版本，正式版本必填")
	flags.StringVar(&opts.pnpmVersion, "pnpm-version", "",
		"构建前端产物使用的 pnpm 版本，正式版本必填")
	flags.StringVar(&opts.sourceRepository, "source-repository", "",
		"构建仓库（owner/name），正式发布由工作流传入")
	flags.StringVar(&opts.sourceWorkflow, "source-workflow", "",
		"执行构建的工作流文件，正式发布由工作流传入")
	flags.StringVar(&opts.sourceRunID, "source-run-id", "",
		"执行构建的工作流运行标识，正式发布由工作流传入")
	flags.StringVar(&opts.attestationProvider, "attestation-provider", "",
		"来源证明提供方，正式发布为 "+release.AttestationProviderGitHub)
	flags.StringVar(&opts.output, "output", "dist/release", "制品输出目录")
	flags.StringVar(&opts.platforms, "platforms", "",
		"目标平台列表（逗号分隔，例如 linux/amd64），留空时使用发布矩阵")
	flags.StringVar(&opts.webDist, "web-dist", "internal/webui/dist", "前端生产构建产物目录")
	flags.StringVar(&opts.repoRoot, "repo-root", ".", "仓库根目录")
	flags.StringVar(&opts.quickstart, "quickstart", "docs/release/QUICKSTART.md",
		"打包进制品的快速启动说明")
	flags.StringVar(&opts.notices, "notices", "docs/release/THIRD-PARTY-NOTICES.md",
		"打包进制品的第三方声明与许可证说明")
	if err := checkNoPositional(flags, args); err != nil {
		return nil, err
	}
	return opts, nil
}

// verifyOptions 是 verify-release 子命令的命令行参数。
type verifyOptions struct {
	// dir 是发布制品目录。
	dir string
	// version 是期望的版本号。
	version string
	// commit 是期望的完整提交 SHA，留空表示不核对。
	commit string
	// requireProvenance 为真时要求清单声明来源信息。
	requireProvenance bool
}

// parseVerifyOptions 解析 verify-release 子命令的命令行参数。
func parseVerifyOptions(args []string, stderr io.Writer) (*verifyOptions, error) {
	opts := &verifyOptions{}
	flags := flag.NewFlagSet("agentflow-release "+commandVerify, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.dir, "dir", "dist/release", "发布制品目录")
	flags.StringVar(&opts.version, "version", "", "期望的版本号（必填）")
	flags.StringVar(&opts.commit, "commit", "", "期望的完整提交 SHA，留空表示不核对")
	flags.BoolVar(&opts.requireProvenance, "require-provenance", false,
		"要求清单声明来源信息（生成来源证明之后核验使用）")
	if err := checkNoPositional(flags, args); err != nil {
		return nil, err
	}
	return opts, nil
}

// verifyAssetsOptions 是 verify-assets 子命令的命令行参数。
type verifyAssetsOptions struct {
	// dir 是发布制品目录（提供清单与本地资产）。
	dir string
	// version 是期望的版本号。
	version string
	// assetsFile 是远端 Release 实际资产名列表文件（每行一个）。
	assetsFile string
}

// parseVerifyAssetsOptions 解析 verify-assets 子命令的命令行参数。
func parseVerifyAssetsOptions(args []string, stderr io.Writer) (*verifyAssetsOptions, error) {
	opts := &verifyAssetsOptions{}
	flags := flag.NewFlagSet("agentflow-release "+commandVerifyAssets, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.dir, "dir", "dist/release", "发布制品目录")
	flags.StringVar(&opts.version, "version", "", "期望的版本号（必填）")
	flags.StringVar(&opts.assetsFile, "assets-file", "",
		"远端 Release 实际资产名列表（每行一个，必填）")
	if err := checkNoPositional(flags, args); err != nil {
		return nil, err
	}
	return opts, nil
}

// notesOptions 是 notes 子命令的命令行参数。
type notesOptions struct {
	// dir 是发布制品目录。
	dir string
	// version 是期望的版本号。
	version string
	// output 是说明输出文件，留空时写到标准输出。
	output string
}

// parseNotesOptions 解析 notes 子命令的命令行参数。
func parseNotesOptions(args []string, stderr io.Writer) (*notesOptions, error) {
	opts := &notesOptions{}
	flags := flag.NewFlagSet("agentflow-release "+commandNotes, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.dir, "dir", "dist/release", "发布制品目录")
	flags.StringVar(&opts.version, "version", "", "期望的版本号（必填）")
	flags.StringVar(&opts.output, "output", "", "说明输出文件，留空时写到标准输出")
	if err := checkNoPositional(flags, args); err != nil {
		return nil, err
	}
	return opts, nil
}

// checkNoPositional 解析参数并拒绝位置参数。
//
// 核验类子命令不接受额外位置参数：路径与版本必须通过具名参数给出，避免把
// 拼错的参数当作"待核验目录"而静默通过。
func checkNoPositional(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if rest := flags.Args(); len(rest) > 0 {
		return fmt.Errorf("%w: 不支持的位置参数 %v", errUsage, rest)
	}
	return nil
}
