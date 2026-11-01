// Command agentflow-release 生成并核验 AgentFlow 的正式发布制品。
//
// 子命令（默认执行 build，保持 `make release-dist` 的既有调用方式）：
//
//	build           按发布矩阵交叉编译、打包、写出 SHA256SUMS 与 release-manifest.json 并自检
//	verify-release  核验发布目录：版本与提交一致、资产齐全、摘要匹配、无多余文件
//	verify-assets   核验远端 Release 的真实资产列表与清单完全一致
//	notes           由清单生成 Release 版本说明（Markdown）
//
// 它只做"构建与核验"：不创建 GitHub Release、不生成签名、不推送镜像、不部署任何环境。
// 正式 Release 的创建、来源证明与上传后核验由 .github/workflows/publish.yml 负责，
// 本工具只提供可被测试覆盖的判定规则，不承担远端写操作。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// 子命令名称。
const (
	commandBuild        = "build"
	commandVerify       = "verify-release"
	commandVerifyAssets = "verify-assets"
	commandNotes        = "notes"
)

// errUsage 表示命令行参数错误。
//
// 参数错误（退出码 2）与核验失败（退出码 1）必须区分：前者说明命令写错了，
// 后者说明制品或远端资产真的不合格。
var errUsage = errors.New("参数错误")

// main 执行发布工具并以退出码结束进程。
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 解析子命令、执行并返回进程退出码。
func run(args []string, stdout, stderr io.Writer) int {
	command, rest := splitCommand(args)
	if command == "" {
		printUsage(stderr)
		return 2
	}
	commands := map[string]func([]string, io.Writer, io.Writer) error{
		commandBuild:        buildRelease,
		commandVerify:       verifyRelease,
		commandVerifyAssets: verifyReleaseAssets,
		commandNotes:        writeReleaseNotes,
	}
	execute, ok := commands[command]
	if !ok {
		fmt.Fprintf(stderr, "错误: 未知子命令 %q\n", command)
		printUsage(stderr)
		return 2
	}
	if err := execute(rest, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

// splitCommand 取出子命令与其余参数。
//
// 第一个参数不是子命令（以 - 开头或为空）时按默认子命令 build 处理，使
// `go run ./cmd/agentflow-release -version v1.2.3` 与显式 `build` 等价。
func splitCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return commandBuild, nil
	}
	if strings.HasPrefix(args[0], "-") {
		return commandBuild, args
	}
	return args[0], args[1:]
}

// printUsage 输出用法说明。
func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `用法: agentflow-release [子命令] [参数]

子命令:
  build           生成发布制品（默认）
  verify-release  核验发布目录
  verify-assets   核验远端 Release 资产列表
  notes           生成 Release 版本说明

示例:
  agentflow-release build -version v1.2.3 -output dist/release
  agentflow-release verify-release -dir dist/release -version v1.2.3 -commit <sha>
  agentflow-release verify-assets -dir dist/release -version v1.2.3 -assets-file assets.txt
  agentflow-release notes -dir dist/release -version v1.2.3 -output RELEASE_NOTES.md
`)
}
