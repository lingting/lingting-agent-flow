package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/lingting/agentflow/internal/release"
)

// options 是发布构建工具的命令行参数。
type options struct {
	// version 是版本号：dev 或 vMAJOR.MINOR.PATCH。
	version string
	// commit 是提交 SHA，留空时通过 git 读取当前 HEAD。
	commit string
	// buildDate 是 UTC 构建时间，留空时取当前时间。
	buildDate string
	// goVersion 是 Go 工具链版本，留空时取本工具的运行期版本。
	goVersion string
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

// parseOptions 解析命令行参数。
func parseOptions(args []string, stderr io.Writer) (*options, error) {
	opts := &options{}
	flags := flag.NewFlagSet("agentflow-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.version, "version", release.DevVersion,
		"版本号：dev 或 vMAJOR.MINOR.PATCH")
	flags.StringVar(&opts.commit, "commit", "",
		"提交 SHA，留空时读取当前 git HEAD")
	flags.StringVar(&opts.buildDate, "build-date", "",
		"UTC 构建时间（RFC 3339），留空时取当前时间")
	flags.StringVar(&opts.goVersion, "go-version", "",
		"Go 工具链版本，留空时取本工具的运行期版本")
	flags.StringVar(&opts.output, "output", "dist/release", "制品输出目录")
	flags.StringVar(&opts.platforms, "platforms", "",
		"目标平台列表（逗号分隔，例如 linux/amd64），留空时使用发布矩阵")
	flags.StringVar(&opts.webDist, "web-dist", "internal/webui/dist", "前端生产构建产物目录")
	flags.StringVar(&opts.repoRoot, "repo-root", ".", "仓库根目录")
	flags.StringVar(&opts.quickstart, "quickstart", "docs/release/QUICKSTART.md",
		"打包进制品的快速启动说明")
	flags.StringVar(&opts.notices, "notices", "docs/release/THIRD-PARTY-NOTICES.md",
		"打包进制品的第三方声明与许可证说明")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if rest := flags.Args(); len(rest) > 0 {
		return nil, fmt.Errorf("不支持的位置参数: %v", rest)
	}
	return opts, nil
}
